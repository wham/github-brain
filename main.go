package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	_ "github.com/mattn/go-sqlite3"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
	"golang.org/x/term"
)

// Global variables for rate limit handling and status tracking
var (
	rateLimitMutex        sync.Mutex
	rateLimitHit          bool
	rateLimitResetTime    time.Time
	secondaryRateLimitHit bool
	secondaryResetTime    time.Time
	backoffDuration       time.Duration = 5 * time.Second
	maxBackoffDuration    time.Duration = 10 * time.Minute

	// Rate limit information from headers
	currentRateLimit   RateLimitInfo = RateLimitInfo{Limit: -1, Remaining: -1, Used: -1}
	rateLimitInfoMutex sync.RWMutex

	// Status code counters
	statusCounters StatusCounters
	statusMutex    sync.Mutex

	// Global console reference for logging
	globalLogger *slog.Logger

	// Pull lock for concurrency control
	pullLockMutex sync.Mutex
	pullLockFile  string
	pullLockConn  *sql.DB
	pullIsRunning int32
)

// ConsoleHandler is a custom slog handler that writes to the Console
type ConsoleHandler struct {
	console *Console
	attrs   []slog.Attr
	groups  []string
	mutex   sync.Mutex
}

// NewConsoleHandler creates a new ConsoleHandler
func NewConsoleHandler(console *Console) *ConsoleHandler {
	return &ConsoleHandler{
		console: console,
	}
}

// Enabled returns true if the handler handles records at the given level
func (h *ConsoleHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

// Handle processes a log record
func (h *ConsoleHandler) Handle(ctx context.Context, record slog.Record) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if h.console == nil {
		return nil
	}

	message := record.Message
	record.Attrs(func(a slog.Attr) bool {
		if a.Key != "" && a.Value.String() != "" {
			message += fmt.Sprintf(" %s=%s", a.Key, a.Value.String())
		}
		return true
	})

	for _, attr := range h.attrs {
		if attr.Key != "" && attr.Value.String() != "" {
			message += fmt.Sprintf(" %s=%s", attr.Key, attr.Value.String())
		}
	}

	h.console.Log("%s", message)
	return nil
}

// WithAttrs returns a new handler with the given attributes added
func (h *ConsoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)

	return &ConsoleHandler{
		console: h.console,
		attrs:   newAttrs,
		groups:  h.groups,
	}
}

// WithGroup returns a new handler with the given group name added
func (h *ConsoleHandler) WithGroup(name string) slog.Handler {
	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name

	return &ConsoleHandler{
		console: h.console,
		attrs:   h.attrs,
		groups:  newGroups,
	}
}

// SetupGlobalLogger sets up the global logger with console handler
func SetupGlobalLogger(console *Console) {
	handler := NewConsoleHandler(console)
	globalLogger = slog.New(handler)
	slog.SetDefault(globalLogger)
}

// RateLimitInfo holds rate limit information from GitHub API headers
type RateLimitInfo struct {
	Limit     int
	Remaining int
	Used      int
	Reset     time.Time
}

// StatusCounters tracks HTTP response status codes
type StatusCounters struct {
	Success2XX int
	Error4XX   int
	Error5XX   int
}

// Config holds all configuration options
type Config struct {
	Organization         string
	GithubToken         string
	DBDir               string
	Items               string
	Force               bool
	ExcludedRepositories string
}

// LoadConfig resolves CLI arguments and environment variables into Config struct
func LoadConfig() (*Config, error) {
	_ = godotenv.Load()

	config := &Config{
		DBDir: "./db",
	}

	// Parse command line arguments
	args := os.Args[1:]
	if len(args) < 1 {
		return nil, fmt.Errorf("usage: github-brain <command>")
	}

	command := args[0]
	switch command {
	case "pull":
		if len(args) < 2 {
			return nil, fmt.Errorf("organization is required for pull command")
		}
		config.Organization = args[1]

		// Parse flags
		for i := 2; i < len(args); i++ {
			arg := args[i]
			switch {
			case arg == "-f" || arg == "--force":
				config.Force = true
			case arg == "-i" || arg == "--items":
				if i+1 >= len(args) {
					return nil, fmt.Errorf("--items requires a value")
				}
				i++
				config.Items = args[i]
			case arg == "-e" || arg == "--excluded-repositories":
				if i+1 >= len(args) {
					return nil, fmt.Errorf("--excluded-repositories requires a value")
				}
				i++
				config.ExcludedRepositories = args[i]
			}
		}
	case "mcp", "ui":
		// No additional parsing needed for these commands
	default:
		return nil, fmt.Errorf("unknown command: %s", command)
	}

	// Get required environment variables
	config.GithubToken = os.Getenv("GITHUB_TOKEN")
	if config.GithubToken == "" && command == "pull" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	// Override with environment variables if set
	if dbDir := os.Getenv("DB_DIR"); dbDir != "" {
		config.DBDir = dbDir
	}
	if items := os.Getenv("ITEMS"); items != "" {
		config.Items = items
	}
	if excluded := os.Getenv("EXCLUDED_REPOSITORIES"); excluded != "" {
		config.ExcludedRepositories = excluded
	}
	if os.Getenv("FORCE") != "" {
		config.Force = true
	}

	return config, nil
}

// addRequestDelay adds a delay between API requests
func addRequestDelay() {
	rateLimitMutex.Lock()
	inSecondaryLimit := secondaryRateLimitHit
	inPrimaryLimit := rateLimitHit
	rateLimitMutex.Unlock()

	var delay time.Duration
	if inSecondaryLimit {
		delay = time.Duration(2000+rand.Intn(2000)) * time.Millisecond
	} else if inPrimaryLimit {
		delay = time.Duration(1500+rand.Intn(1000)) * time.Millisecond
	} else {
		rateLimitInfoMutex.RLock()
		remaining := currentRateLimit.Remaining
		limit := currentRateLimit.Limit
		rateLimitInfoMutex.RUnlock()

		if remaining > 0 && limit > 0 {
			utilization := float64(limit-remaining) / float64(limit)

			if utilization > 0.9 {
				delay = time.Duration(1000+rand.Intn(1500)) * time.Millisecond
			} else if utilization > 0.7 {
				delay = time.Duration(750+rand.Intn(750)) * time.Millisecond
			} else {
				delay = time.Duration(500+rand.Intn(500)) * time.Millisecond
			}
		} else {
			delay = time.Duration(750+rand.Intn(750)) * time.Millisecond
		}
	}

	time.Sleep(delay)
}

// updateRateLimitInfo updates the global rate limit information from HTTP headers
func updateRateLimitInfo(headers http.Header) {
	rateLimitInfoMutex.Lock()
	defer rateLimitInfoMutex.Unlock()

	if limit := headers.Get("x-ratelimit-limit"); limit != "" {
		if val, err := strconv.Atoi(limit); err == nil {
			currentRateLimit.Limit = val
		}
	}

	if remaining := headers.Get("x-ratelimit-remaining"); remaining != "" {
		if val, err := strconv.Atoi(remaining); err == nil {
			currentRateLimit.Remaining = val
		}
	}

	if used := headers.Get("x-ratelimit-used"); used != "" {
		if val, err := strconv.Atoi(used); err == nil {
			currentRateLimit.Used = val
		}
	}

	if reset := headers.Get("x-ratelimit-reset"); reset != "" {
		if val, err := strconv.ParseInt(reset, 10, 64); err == nil {
			currentRateLimit.Reset = time.Unix(val, 0)
		}
	}
}

// updateStatusCounters updates HTTP status code counters
func updateStatusCounters(statusCode int) {
	statusMutex.Lock()
	defer statusMutex.Unlock()

	switch {
	case statusCode >= 200 && statusCode < 300:
		statusCounters.Success2XX++
	case statusCode >= 400 && statusCode < 500:
		statusCounters.Error4XX++
	case statusCode >= 500 && statusCode < 600:
		statusCounters.Error5XX++
	}
}

// acquirePullLock attempts to acquire the pull lock
func acquirePullLock(config *Config) error {
	pullLockMutex.Lock()
	defer pullLockMutex.Unlock()

	if atomic.LoadInt32(&pullIsRunning) == 1 {
		return fmt.Errorf("a pull is currently running. Please wait until it finishes")
	}

	// Create DB directory if it doesn't exist
	if err := os.MkdirAll(config.DBDir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database connection for lock
	dbPath := fmt.Sprintf("%s/%s.db", config.DBDir, config.Organization)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Try to acquire exclusive lock
	_, err = db.Exec("BEGIN EXCLUSIVE")
	if err != nil {
		db.Close()
		return fmt.Errorf("failed to acquire database lock: %w", err)
	}

	atomic.StoreInt32(&pullIsRunning, 1)
	pullLockConn = db

	// Start lock renewal goroutine
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if atomic.LoadInt32(&pullIsRunning) == 0 {
					return
				}
				// Renew lock by executing a simple query
				_, err := pullLockConn.Exec("SELECT 1")
				if err != nil {
					slog.Error("Failed to renew database lock", "error", err)
					releasePullLock()
					return
				}
			}
		}
	}()

	return nil
}

// releasePullLock releases the pull lock
func releasePullLock() {
	pullLockMutex.Lock()
	defer pullLockMutex.Unlock()

	atomic.StoreInt32(&pullIsRunning, 0)

	if pullLockConn != nil {
		pullLockConn.Exec("ROLLBACK")
		pullLockConn.Close()
		pullLockConn = nil
	}
}

// isPullRunning checks if a pull is currently running
func isPullRunning() bool {
	return atomic.LoadInt32(&pullIsRunning) == 1
}

// Console represents the modern console UI
type Console struct {
	mutex         sync.Mutex
	logs          []LogEntry
	maxLogs       int
	items         []ItemStatus
	apiStatus     StatusCounters
	rateLimit     RateLimitInfo
	termWidth     int
	termHeight    int
	displayHeight int
	isVisible     bool
	lastRender    time.Time
	minInterval   time.Duration
}

// LogEntry represents a log message with timestamp
type LogEntry struct {
	Timestamp time.Time
	Message   string
}

// ItemStatus represents the status of a pull item
type ItemStatus struct {
	Name     string
	Status   string // pending, active, completed, skipped, failed
	Count    int
	Errors   int
	Selected bool
}

// NewConsole creates a new console instance
func NewConsole() *Console {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width, height = 80, 24 // Default size
	}

	console := &Console{
		maxLogs:       5,
		termWidth:     width,
		termHeight:    height,
		displayHeight: 13, // Fixed display area
		minInterval:   200 * time.Millisecond,
		items: []ItemStatus{
			{Name: "Repositories", Status: "pending", Selected: true},
			{Name: "Discussions", Status: "pending", Selected: true},
			{Name: "Issues", Status: "pending", Selected: true},
			{Name: "Pull Requests", Status: "pending", Selected: true},
		},
	}

	// Handle terminal resize
	go console.handleResize()

	return console
}

// handleResize handles terminal resize events
func (c *Console) handleResize() {
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)

	for range sigwinch {
		width, height, err := term.GetSize(int(os.Stdout.Fd()))
		if err == nil {
			c.mutex.Lock()
			c.termWidth = width
			c.termHeight = height
			c.mutex.Unlock()
			c.render()
		}
	}
}

// Log adds a log entry to the console
func (c *Console) Log(format string, args ...interface{}) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	entry := LogEntry{
		Timestamp: time.Now(),
		Message:   fmt.Sprintf(format, args...),
	}

	c.logs = append(c.logs, entry)
	if len(c.logs) > c.maxLogs {
		c.logs = c.logs[len(c.logs)-c.maxLogs:]
	}

	c.scheduleRender()
}

// UpdateItemStatus updates the status of a pull item
func (c *Console) UpdateItemStatus(name, status string, count, errors int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for i := range c.items {
		if c.items[i].Name == name {
			c.items[i].Status = status
			c.items[i].Count = count
			c.items[i].Errors = errors
			break
		}
	}

	c.scheduleRender()
}

// UpdateAPIStatus updates the API status counters
func (c *Console) UpdateAPIStatus(counters StatusCounters) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.apiStatus = counters
	c.scheduleRender()
}

// UpdateRateLimit updates the rate limit information
func (c *Console) UpdateRateLimit(rateLimit RateLimitInfo) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.rateLimit = rateLimit
	c.scheduleRender()
}

// SetItemsSelection sets which items are selected for pulling
func (c *Console) SetItemsSelection(items []string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	itemSet := make(map[string]bool)
	for _, item := range items {
		itemSet[strings.ToLower(item)] = true
	}

	for i := range c.items {
		itemName := strings.ToLower(c.items[i].Name)
		if len(items) == 0 {
			c.items[i].Selected = true // All selected if no specific items
		} else {
			c.items[i].Selected = itemSet[itemName] || itemSet[strings.Replace(itemName, " ", "_", -1)]
		}

		if !c.items[i].Selected {
			c.items[i].Status = "skipped"
		}
	}

	c.scheduleRender()
}

// Show displays the console
func (c *Console) Show() {
	c.mutex.Lock()
	c.isVisible = true
	c.mutex.Unlock()
	c.render()
}

// Hide hides the console and restores cursor
func (c *Console) Hide() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.isVisible {
		// Clear the display area
		for i := 0; i < c.displayHeight; i++ {
			fmt.Print("\033[K\n") // Clear line and move down
		}
		// Move cursor back up
		fmt.Printf("\033[%dA", c.displayHeight)
		c.isVisible = false
	}
}

// scheduleRender schedules a render with debouncing
func (c *Console) scheduleRender() {
	now := time.Now()
	if now.Sub(c.lastRender) >= c.minInterval {
		go c.render()
	}
}

// render renders the console display
func (c *Console) render() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.isVisible {
		return
	}

	now := time.Now()
	if now.Sub(c.lastRender) < c.minInterval {
		return
	}
	c.lastRender = now

	width := c.termWidth
	if width < 64 {
		width = 64
	}

	// Build the complete output in memory
	var output strings.Builder

	// Top border
	output.WriteString("┌─ GitHub 🧠 pull ")
	for i := 0; i < width-len("┌─ GitHub 🧠 pull ")- 1; i++ {
		output.WriteRune('─')
	}
	output.WriteString("┐\n")

	// Empty line
	output.WriteString("│")
	for i := 0; i < width-2; i++ {
		output.WriteRune(' ')
	}
	output.WriteString("│\n")

	// Items section
	for _, item := range c.items {
		output.WriteString("│  ")
		
		// Status icon
		switch item.Status {
		case "pending":
			if item.Selected {
				output.WriteString("⚪")
			} else {
				output.WriteString("🔕")
			}
		case "active":
			output.WriteString("🔄")
		case "completed":
			output.WriteString("✅")
		case "failed":
			output.WriteString("❌")
		case "skipped":
			output.WriteString("🔕")
		}

		output.WriteString(" " + item.Name)
		
		// Add count and errors if applicable
		if item.Count > 0 {
			countStr := formatNumber(item.Count)
			if item.Errors > 0 {
				countStr += fmt.Sprintf(" (%d errors)", item.Errors)
			}
			output.WriteString(": " + countStr)
		}

		// Pad to width
		lineLen := 4 + len(item.Name)
		if item.Count > 0 {
			countStr := formatNumber(item.Count)
			if item.Errors > 0 {
				countStr += fmt.Sprintf(" (%d errors)", item.Errors)
			}
			lineLen += 2 + len(countStr)
		}
		
		for i := lineLen; i < width-1; i++ {
			output.WriteRune(' ')
		}
		output.WriteString("│\n")
	}

	// Empty line
	output.WriteString("│")
	for i := 0; i < width-2; i++ {
		output.WriteRune(' ')
	}
	output.WriteString("│\n")

	// API Status line
	apiStatusStr := fmt.Sprintf("📊 API Status    ✅ %d   ⚠️ %d   ❌ %d",
		c.apiStatus.Success2XX, c.apiStatus.Error4XX, c.apiStatus.Error5XX)
	output.WriteString("│  " + apiStatusStr)
	for i := len(apiStatusStr)+2; i < width-1; i++ {
		output.WriteRune(' ')
	}
	output.WriteString("│\n")

	// Rate limit line
	rateLimitStr := "🚀 Rate Limit    "
	if c.rateLimit.Limit > 0 {
		rateLimitStr += fmt.Sprintf("%d/%d used", c.rateLimit.Used, c.rateLimit.Limit)
		if !c.rateLimit.Reset.IsZero() {
			until := time.Until(c.rateLimit.Reset)
			if until > 0 {
				rateLimitStr += ", resets " + formatDuration(until)
			}
		}
	} else {
		rateLimitStr += "? / ? used, resets ?"
	}
	
	output.WriteString("│  " + rateLimitStr)
	for i := len(rateLimitStr)+2; i < width-1; i++ {
		output.WriteRune(' ')
	}
	output.WriteString("│\n")

	// Empty line
	output.WriteString("│")
	for i := 0; i < width-2; i++ {
		output.WriteRune(' ')
	}
	output.WriteString("│\n")

	// Activity section header
	output.WriteString("│  💬 Activity")
	for i := 12; i < width-1; i++ {
		output.WriteRune(' ')
	}
	output.WriteString("│\n")

	// Log entries
	for i := 0; i < c.maxLogs; i++ {
		output.WriteString("│     ")
		
		if i < len(c.logs) {
			entry := c.logs[i]
			timeStr := entry.Timestamp.Format("15:04:05")
			logLine := timeStr + " " + entry.Message
			
			// Truncate if too long
			maxLogLen := width - 7 // Account for borders and indent
			if len(logLine) > maxLogLen {
				logLine = logLine[:maxLogLen-3] + "..."
			}
			
			output.WriteString(logLine)
			for j := len(logLine); j < width-6; j++ {
				output.WriteRune(' ')
			}
		} else {
			for j := 0; j < width-6; j++ {
				output.WriteRune(' ')
			}
		}
		
		output.WriteString("│\n")
	}

	// Empty line
	output.WriteString("│")
	for i := 0; i < width-2; i++ {
		output.WriteRune(' ')
	}
	output.WriteString("│\n")

	// Bottom border
	output.WriteString("└")
	for i := 0; i < width-2; i++ {
		output.WriteRune('─')
	}
	output.WriteString("┘\n")

	// Save cursor position, clear area, write output, restore cursor
	fmt.Print("\033[s") // Save cursor
	
	// Move to start of display area and clear
	for i := 0; i < c.displayHeight; i++ {
		fmt.Print("\033[K\n") // Clear line and move down
	}
	fmt.Printf("\033[%dA", c.displayHeight) // Move back to start

	// Write the output
	fmt.Print(output.String())
	
	// Move cursor back up to saved position
	fmt.Printf("\033[%dA", c.displayHeight)
	fmt.Print("\033[u") // Restore cursor
}

// formatNumber formats a number with comma separators
func formatNumber(n int) string {
	str := strconv.Itoa(n)
	if len(str) <= 3 {
		return str
	}
	
	var result []rune
	for i, r := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, r)
	}
	
	return string(result)
}

// formatDuration formats a duration in human-readable form
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("in %ds", int(d.Seconds()))
	} else if d < time.Hour {
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	} else {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		if minutes == 0 {
			return fmt.Sprintf("in %dh", hours)
		}
		return fmt.Sprintf("in %dh %dm", hours, minutes)
	}
}

// Database operations

// initDatabase initializes the database and creates tables
func initDatabase(config *Config) (*sql.DB, error) {
	if err := os.MkdirAll(config.DBDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	dbPath := fmt.Sprintf("%s/%s.db", config.DBDir, config.Organization)
	db, err := sql.Open("sqlite3", dbPath+"?_fts=1")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create tables
	if err := createTables(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// createTables creates all required database tables
func createTables(db *sql.DB) error {
	queries := []string{
		// Repositories table
		`CREATE TABLE IF NOT EXISTS repositories (
			name TEXT PRIMARY KEY,
			has_discussions_enabled BOOLEAN,
			has_issues_enabled BOOLEAN,
			updated_at TEXT
		)`,
		
		// Discussions table
		`CREATE TABLE IF NOT EXISTS discussions (
			url TEXT PRIMARY KEY,
			title TEXT,
			body TEXT,
			created_at TEXT,
			updated_at TEXT,
			repository TEXT,
			author TEXT
		)`,
		
		// Issues table
		`CREATE TABLE IF NOT EXISTS issues (
			url TEXT PRIMARY KEY,
			title TEXT,
			body TEXT,
			created_at TEXT,
			updated_at TEXT,
			closed_at TEXT,
			repository TEXT,
			author TEXT
		)`,
		
		// Pull requests table
		`CREATE TABLE IF NOT EXISTS pull_requests (
			url TEXT PRIMARY KEY,
			title TEXT,
			body TEXT,
			created_at TEXT,
			updated_at TEXT,
			merged_at TEXT,
			closed_at TEXT,
			repository TEXT,
			author TEXT
		)`,
		
		// FTS5 search table
		`CREATE VIRTUAL TABLE IF NOT EXISTS search USING fts5(
			type,
			title,
			body,
			url UNINDEXED,
			repository UNINDEXED,
			author UNINDEXED,
			created_at UNINDEXED,
			state UNINDEXED
		)`,
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	// Create indexes
	indexes := []string{
		// Repositories indexes
		"CREATE INDEX IF NOT EXISTS idx_repositories_updated_at ON repositories(updated_at)",
		
		// Discussions indexes
		"CREATE INDEX IF NOT EXISTS idx_discussions_repository ON discussions(repository)",
		"CREATE INDEX IF NOT EXISTS idx_discussions_author ON discussions(author)",
		"CREATE INDEX IF NOT EXISTS idx_discussions_created_at ON discussions(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_discussions_updated_at ON discussions(updated_at)",
		"CREATE INDEX IF NOT EXISTS idx_discussions_repo_created ON discussions(repository, created_at)",
		
		// Issues indexes
		"CREATE INDEX IF NOT EXISTS idx_issues_repository ON issues(repository)",
		"CREATE INDEX IF NOT EXISTS idx_issues_author ON issues(author)",
		"CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_issues_updated_at ON issues(updated_at)",
		"CREATE INDEX IF NOT EXISTS idx_issues_closed_at ON issues(closed_at)",
		"CREATE INDEX IF NOT EXISTS idx_issues_repo_created ON issues(repository, created_at)",
		
		// Pull requests indexes
		"CREATE INDEX IF NOT EXISTS idx_pull_requests_repository ON pull_requests(repository)",
		"CREATE INDEX IF NOT EXISTS idx_pull_requests_author ON pull_requests(author)",
		"CREATE INDEX IF NOT EXISTS idx_pull_requests_created_at ON pull_requests(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_pull_requests_updated_at ON pull_requests(updated_at)",
		"CREATE INDEX IF NOT EXISTS idx_pull_requests_merged_at ON pull_requests(merged_at)",
		"CREATE INDEX IF NOT EXISTS idx_pull_requests_closed_at ON pull_requests(closed_at)",
		"CREATE INDEX IF NOT EXISTS idx_pull_requests_repo_created ON pull_requests(repository, created_at)",
	}

	for _, index := range indexes {
		if _, err := db.Exec(index); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	return nil
}

// clearData removes data from database based on config
func clearData(db *sql.DB, config *Config) error {
	if !config.Force {
		return nil
	}

	items := []string{"repositories", "discussions", "issues", "pull_requests"}
	if config.Items != "" {
		items = strings.Split(config.Items, ",")
		for i := range items {
			items[i] = strings.TrimSpace(strings.ToLower(items[i]))
			// Map CLI names to table names
			switch items[i] {
			case "pull_requests", "pullrequests", "prs":
				items[i] = "pull_requests"
			}
		}
	}

	for _, item := range items {
		tableName := item
		switch item {
		case "repositories", "discussions", "issues", "pull_requests":
			slog.Info("Clearing table", "table", tableName)
			if _, err := db.Exec("DELETE FROM " + tableName); err != nil {
				return fmt.Errorf("failed to clear %s: %w", tableName, err)
			}
		}
	}

	return nil
}

// GitHub client and operations

// createGitHubClient creates a GitHub GraphQL client
func createGitHubClient(token string) *githubv4.Client {
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(context.Background(), tokenSource)
	
	// Wrap the HTTP client to handle rate limiting and add delays
	httpClient.Transport = &rateLimitTransport{
		base: httpClient.Transport,
	}
	
	return githubv4.NewClient(httpClient)
}

// rateLimitTransport wraps HTTP transport for rate limiting
type rateLimitTransport struct {
	base http.RoundTripper
}

func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Add delay before request
	addRequestDelay()
	
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	
	resp, err := base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	
	// Update rate limit info from response headers
	updateRateLimitInfo(resp.Header)
	updateStatusCounters(resp.StatusCode)
	
	// Handle rate limit responses
	if resp.StatusCode == 429 {
		rateLimitMutex.Lock()
		rateLimitHit = true
		rateLimitResetTime = time.Now().Add(backoffDuration)
		rateLimitMutex.Unlock()
		
		slog.Error("Rate limit exceeded, backing off", "duration", backoffDuration)
		
		// Double backoff duration for next time, up to max
		backoffDuration *= 2
		if backoffDuration > maxBackoffDuration {
			backoffDuration = maxBackoffDuration
		}
	} else if resp.StatusCode == 403 {
		// Check if this is a secondary rate limit
		if strings.Contains(resp.Header.Get("X-RateLimit-Resource"), "secondary") {
			rateLimitMutex.Lock()
			secondaryRateLimitHit = true
			secondaryResetTime = time.Now().Add(60 * time.Second)
			rateLimitMutex.Unlock()
			
			slog.Error("Secondary rate limit exceeded")
		}
	} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Reset rate limit flags on success
		rateLimitMutex.Lock()
		if rateLimitHit && time.Now().After(rateLimitResetTime) {
			rateLimitHit = false
			backoffDuration = 5 * time.Second // Reset to initial value
		}
		if secondaryRateLimitHit && time.Now().After(secondaryResetTime) {
			secondaryRateLimitHit = false
		}
		rateLimitMutex.Unlock()
	}
	
	return resp, err
}

// Repository operations

type Repository struct {
	Name                    string `json:"name"`
	HasDiscussionsEnabled   bool   `json:"hasDiscussionsEnabled"`
	HasIssuesEnabled        bool   `json:"hasIssuesEnabled"`
	UpdatedAt               string `json:"updatedAt"`
}

type RepositoriesQuery struct {
	Organization struct {
		Repositories struct {
			Nodes []Repository `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `graphql:"repositories(isArchived: false, isFork: false, first: 100, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"organization(login: $org)"`
}

// pullRepositories pulls repositories from GitHub
func pullRepositories(client *githubv4.Client, db *sql.DB, config *Config, console *Console) error {
	slog.Info("Starting repositories sync")
	console.UpdateItemStatus("Repositories", "active", 0, 0)

	// Get the most recent updated_at timestamp
	var lastUpdated string
	err := db.QueryRow("SELECT MAX(updated_at) FROM repositories").Scan(&lastUpdated)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to get last updated timestamp: %w", err)
	}

	var cursor *string
	totalCount := 0
	
	for {
		variables := map[string]interface{}{
			"org":    githubv4.String(config.Organization),
			"cursor": (*githubv4.String)(cursor),
		}

		var query RepositoriesQuery
		err := client.Query(context.Background(), &query, variables)
		if err != nil {
			return fmt.Errorf("failed to query repositories: %w", err)
		}

		repositories := query.Organization.Repositories.Nodes
		if len(repositories) == 0 {
			break
		}

		slog.Info("Processing repositories batch", "count", len(repositories))

		for _, repo := range repositories {
			// Stop if we've reached data we already have
			if lastUpdated != "" && repo.UpdatedAt <= lastUpdated {
				slog.Info("Reached existing data, stopping")
				goto done
			}

			// Save repository immediately
			_, err := db.Exec(`
				INSERT OR REPLACE INTO repositories (name, has_discussions_enabled, has_issues_enabled, updated_at)
				VALUES (?, ?, ?, ?)
			`, repo.Name, repo.HasDiscussionsEnabled, repo.HasIssuesEnabled, repo.UpdatedAt)
			
			if err != nil {
				slog.Error("Failed to save repository", "name", repo.Name, "error", err)
				continue
			}

			totalCount++
			if totalCount%50 == 0 {
				console.UpdateItemStatus("Repositories", "active", totalCount, 0)
			}
		}

		if !query.Organization.Repositories.PageInfo.HasNextPage {
			break
		}
		cursor = &query.Organization.Repositories.PageInfo.EndCursor
	}

done:
	console.UpdateItemStatus("Repositories", "completed", totalCount, 0)
	slog.Info("Repositories sync completed", "count", totalCount)
	return nil
}

// Discussion operations

type Discussion struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
}

type DiscussionsQuery struct {
	Repository struct {
		Discussions struct {
			Nodes []Discussion `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `graphql:"discussions(first: 100, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// pullDiscussions pulls discussions for all repositories
func pullDiscussions(client *githubv4.Client, db *sql.DB, config *Config, console *Console) error {
	slog.Info("Starting discussions sync")
	console.UpdateItemStatus("Discussions", "active", 0, 0)

	// Get repositories with discussions enabled
	rows, err := db.Query(`
		SELECT name FROM repositories 
		WHERE has_discussions_enabled = 1 
		AND name NOT IN (` + buildExclusionList(config.ExcludedRepositories) + `)
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to query repositories: %w", err)
	}
	defer rows.Close()

	totalCount := 0
	errorCount := 0

	for rows.Next() {
		var repoName string
		if err := rows.Scan(&repoName); err != nil {
			continue
		}

		count, err := pullRepositoryDiscussions(client, db, config, repoName, console)
		if err != nil {
			slog.Error("Failed to pull discussions", "repository", repoName, "error", err)
			errorCount++
			
			// Check if repository doesn't exist
			if strings.Contains(err.Error(), "NOT_FOUND") {
				slog.Info("Repository not found, removing from database", "repository", repoName)
				removeRepositoryData(db, repoName)
			}
		} else {
			totalCount += count
		}

		console.UpdateItemStatus("Discussions", "active", totalCount, errorCount)
	}

	status := "completed"
	if errorCount > 0 {
		status = "failed"
	}
	console.UpdateItemStatus("Discussions", status, totalCount, errorCount)
	slog.Info("Discussions sync completed", "count", totalCount, "errors", errorCount)
	return nil
}

// pullRepositoryDiscussions pulls discussions for a single repository
func pullRepositoryDiscussions(client *githubv4.Client, db *sql.DB, config *Config, repoName string, console *Console) (int, error) {
	// Get last updated timestamp for this repository
	var lastUpdated string
	err := db.QueryRow("SELECT MAX(updated_at) FROM discussions WHERE repository = ?", repoName).Scan(&lastUpdated)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to get last updated timestamp: %w", err)
	}

	var cursor *string
	count := 0

	for {
		variables := map[string]interface{}{
			"owner":  githubv4.String(config.Organization),
			"name":   githubv4.String(repoName),
			"cursor": (*githubv4.String)(cursor),
		}

		var query DiscussionsQuery
		err := client.Query(context.Background(), &query, variables)
		if err != nil {
			return count, err
		}

		// Check if repository exists
		if query.Repository.Discussions.Nodes == nil {
			return count, fmt.Errorf("repository not found: NOT_FOUND")
		}

		discussions := query.Repository.Discussions.Nodes
		if len(discussions) == 0 {
			break
		}

		for _, discussion := range discussions {
			// Stop if we've reached data we already have
			if lastUpdated != "" && discussion.UpdatedAt <= lastUpdated {
				return count, nil
			}

			authorLogin := ""
			if discussion.Author.Login != "" {
				authorLogin = discussion.Author.Login
			}

			// Save discussion immediately
			_, err := db.Exec(`
				INSERT OR REPLACE INTO discussions (url, title, body, created_at, updated_at, repository, author)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, discussion.URL, discussion.Title, discussion.Body, discussion.CreatedAt, 
			   discussion.UpdatedAt, repoName, authorLogin)

			if err != nil {
				slog.Error("Failed to save discussion", "url", discussion.URL, "error", err)
				continue
			}

			count++
		}

		if !query.Repository.Discussions.PageInfo.HasNextPage {
			break
		}
		cursor = &query.Repository.Discussions.PageInfo.EndCursor
	}

	return count, nil
}

// Issue operations

type Issue struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	ClosedAt  string `json:"closedAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
}

type IssuesQuery struct {
	Repository struct {
		Issues struct {
			Nodes []Issue `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `graphql:"issues(first: 100, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// pullIssues pulls issues for all repositories
func pullIssues(client *githubv4.Client, db *sql.DB, config *Config, console *Console) error {
	slog.Info("Starting issues sync")
	console.UpdateItemStatus("Issues", "active", 0, 0)

	// Get repositories with issues enabled
	rows, err := db.Query(`
		SELECT name FROM repositories 
		WHERE has_issues_enabled = 1 
		AND name NOT IN (` + buildExclusionList(config.ExcludedRepositories) + `)
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to query repositories: %w", err)
	}
	defer rows.Close()

	totalCount := 0
	errorCount := 0

	for rows.Next() {
		var repoName string
		if err := rows.Scan(&repoName); err != nil {
			continue
		}

		count, err := pullRepositoryIssues(client, db, config, repoName, console)
		if err != nil {
			slog.Error("Failed to pull issues", "repository", repoName, "error", err)
			errorCount++
			
			// Check if repository doesn't exist
			if strings.Contains(err.Error(), "NOT_FOUND") {
				slog.Info("Repository not found, removing from database", "repository", repoName)
				removeRepositoryData(db, repoName)
			}
		} else {
			totalCount += count
		}

		console.UpdateItemStatus("Issues", "active", totalCount, errorCount)
	}

	status := "completed"
	if errorCount > 0 {
		status = "failed"
	}
	console.UpdateItemStatus("Issues", status, totalCount, errorCount)
	slog.Info("Issues sync completed", "count", totalCount, "errors", errorCount)
	return nil
}

// pullRepositoryIssues pulls issues for a single repository
func pullRepositoryIssues(client *githubv4.Client, db *sql.DB, config *Config, repoName string, console *Console) (int, error) {
	// Get last updated timestamp for this repository
	var lastUpdated string
	err := db.QueryRow("SELECT MAX(updated_at) FROM issues WHERE repository = ?", repoName).Scan(&lastUpdated)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to get last updated timestamp: %w", err)
	}

	var cursor *string
	count := 0
	cutoffDate := time.Now().AddDate(0, 0, -400).Format(time.RFC3339) // 400 days ago

	for {
		variables := map[string]interface{}{
			"owner":  githubv4.String(config.Organization),
			"name":   githubv4.String(repoName),
			"cursor": (*githubv4.String)(cursor),
		}

		var query IssuesQuery
		err := client.Query(context.Background(), &query, variables)
		if err != nil {
			return count, err
		}

		// Check if repository exists
		if query.Repository.Issues.Nodes == nil {
			return count, fmt.Errorf("repository not found: NOT_FOUND")
		}

		issues := query.Repository.Issues.Nodes
		if len(issues) == 0 {
			break
		}

		for _, issue := range issues {
			// Stop if we've reached data we already have
			if lastUpdated != "" && issue.UpdatedAt <= lastUpdated {
				return count, nil
			}

			// Stop if issue is older than 400 days
			if issue.UpdatedAt < cutoffDate {
				return count, nil
			}

			authorLogin := ""
			if issue.Author.Login != "" {
				authorLogin = issue.Author.Login
			}

			// Save issue immediately
			_, err := db.Exec(`
				INSERT OR REPLACE INTO issues (url, title, body, created_at, updated_at, closed_at, repository, author)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			`, issue.URL, issue.Title, issue.Body, issue.CreatedAt, 
			   issue.UpdatedAt, issue.ClosedAt, repoName, authorLogin)

			if err != nil {
				slog.Error("Failed to save issue", "url", issue.URL, "error", err)
				continue
			}

			count++
		}

		if !query.Repository.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = &query.Repository.Issues.PageInfo.EndCursor
	}

	return count, nil
}

// Pull Request operations

type PullRequest struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	MergedAt  string `json:"mergedAt"`
	ClosedAt  string `json:"closedAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
}

type PullRequestsQuery struct {
	Repository struct {
		PullRequests struct {
			Nodes []PullRequest `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `graphql:"pullRequests(first: 100, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// pullPullRequests pulls pull requests for all repositories
func pullPullRequests(client *githubv4.Client, db *sql.DB, config *Config, console *Console) error {
	slog.Info("Starting pull requests sync")
	console.UpdateItemStatus("Pull Requests", "active", 0, 0)

	// Get all repositories
	rows, err := db.Query(`
		SELECT name FROM repositories 
		WHERE name NOT IN (` + buildExclusionList(config.ExcludedRepositories) + `)
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to query repositories: %w", err)
	}
	defer rows.Close()

	totalCount := 0
	errorCount := 0

	for rows.Next() {
		var repoName string
		if err := rows.Scan(&repoName); err != nil {
			continue
		}

		count, err := pullRepositoryPullRequests(client, db, config, repoName, console)
		if err != nil {
			slog.Error("Failed to pull pull requests", "repository", repoName, "error", err)
			errorCount++
			
			// Check if repository doesn't exist
			if strings.Contains(err.Error(), "NOT_FOUND") {
				slog.Info("Repository not found, removing from database", "repository", repoName)
				removeRepositoryData(db, repoName)
			}
		} else {
			totalCount += count
		}

		console.UpdateItemStatus("Pull Requests", "active", totalCount, errorCount)
	}

	status := "completed"
	if errorCount > 0 {
		status = "failed"
	}
	console.UpdateItemStatus("Pull Requests", status, totalCount, errorCount)
	slog.Info("Pull requests sync completed", "count", totalCount, "errors", errorCount)
	return nil
}

// pullRepositoryPullRequests pulls pull requests for a single repository
func pullRepositoryPullRequests(client *githubv4.Client, db *sql.DB, config *Config, repoName string, console *Console) (int, error) {
	// Get last updated timestamp for this repository
	var lastUpdated string
	err := db.QueryRow("SELECT MAX(updated_at) FROM pull_requests WHERE repository = ?", repoName).Scan(&lastUpdated)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to get last updated timestamp: %w", err)
	}

	var cursor *string
	count := 0
	cutoffDate := time.Now().AddDate(0, 0, -400).Format(time.RFC3339) // 400 days ago

	for {
		variables := map[string]interface{}{
			"owner":  githubv4.String(config.Organization),
			"name":   githubv4.String(repoName),
			"cursor": (*githubv4.String)(cursor),
		}

		var query PullRequestsQuery
		err := client.Query(context.Background(), &query, variables)
		if err != nil {
			return count, err
		}

		// Check if repository exists
		if query.Repository.PullRequests.Nodes == nil {
			return count, fmt.Errorf("repository not found: NOT_FOUND")
		}

		pullRequests := query.Repository.PullRequests.Nodes
		if len(pullRequests) == 0 {
			break
		}

		for _, pr := range pullRequests {
			// Stop if we've reached data we already have
			if lastUpdated != "" && pr.UpdatedAt <= lastUpdated {
				return count, nil
			}

			// Stop if PR is older than 400 days
			if pr.UpdatedAt < cutoffDate {
				return count, nil
			}

			authorLogin := ""
			if pr.Author.Login != "" {
				authorLogin = pr.Author.Login
			}

			// Save pull request immediately
			_, err := db.Exec(`
				INSERT OR REPLACE INTO pull_requests (url, title, body, created_at, updated_at, merged_at, closed_at, repository, author)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, pr.URL, pr.Title, pr.Body, pr.CreatedAt, 
			   pr.UpdatedAt, pr.MergedAt, pr.ClosedAt, repoName, authorLogin)

			if err != nil {
				slog.Error("Failed to save pull request", "url", pr.URL, "error", err)
				continue
			}

			count++
		}

		if !query.Repository.PullRequests.PageInfo.HasNextPage {
			break
		}
		cursor = &query.Repository.PullRequests.PageInfo.EndCursor
	}

	return count, nil
}

// buildExclusionList builds SQL IN clause for excluded repositories
func buildExclusionList(excludedRepos string) string {
	if excludedRepos == "" {
		return "''"
	}

	repos := strings.Split(excludedRepos, ",")
	var quoted []string
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo != "" {
			quoted = append(quoted, "'"+strings.Replace(repo, "'", "''", -1)+"'")
		}
	}

	if len(quoted) == 0 {
		return "''"
	}

	return strings.Join(quoted, ",")
}

// removeRepositoryData removes all data for a repository that no longer exists
func removeRepositoryData(db *sql.DB, repoName string) {
	tables := []string{"repositories", "discussions", "issues", "pull_requests"}
	
	for _, table := range tables {
		var column string
		if table == "repositories" {
			column = "name"
		} else {
			column = "repository"
		}
		
		_, err := db.Exec("DELETE FROM "+table+" WHERE "+column+" = ?", repoName)
		if err != nil {
			slog.Error("Failed to remove data", "table", table, "repository", repoName, "error", err)
		}
	}
}

// rebuildSearchIndex rebuilds the FTS5 search index
func rebuildSearchIndex(db *sql.DB, console *Console) error {
	slog.Info("Rebuilding search index")

	// Clear existing search data
	_, err := db.Exec("DELETE FROM search")
	if err != nil {
		return fmt.Errorf("failed to clear search table: %w", err)
	}

	// Insert discussions
	_, err = db.Exec(`
		INSERT INTO search (type, title, body, url, repository, author, created_at, state)
		SELECT 'discussion', title, body, url, repository, author, created_at, 'open'
		FROM discussions
	`)
	if err != nil {
		return fmt.Errorf("failed to insert discussions into search: %w", err)
	}

	// Insert issues
	_, err = db.Exec(`
		INSERT INTO search (type, title, body, url, repository, author, created_at, state)
		SELECT 'issue', title, body, url, repository, author, created_at, 
		       CASE WHEN closed_at IS NULL THEN 'open' ELSE 'closed' END
		FROM issues
	`)
	if err != nil {
		return fmt.Errorf("failed to insert issues into search: %w", err)
	}

	// Insert pull requests
	_, err = db.Exec(`
		INSERT INTO search (type, title, body, url, repository, author, created_at, state)
		SELECT 'pull_request', title, body, url, repository, author, created_at,
		       CASE 
		         WHEN merged_at IS NOT NULL THEN 'merged'
		         WHEN closed_at IS NOT NULL THEN 'closed' 
		         ELSE 'open' 
		       END
		FROM pull_requests
	`)
	if err != nil {
		return fmt.Errorf("failed to insert pull requests into search: %w", err)
	}

	slog.Info("Search index rebuild completed")
	return nil
}

// MCP Server implementation

// setupMCPServer sets up and runs the MCP server
func setupMCPServer(config *Config) error {
	if isPullRunning() {
		return fmt.Errorf("a pull is currently running. Please wait until it finishes")
	}

	db, err := initDatabase(config)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	mcpServer := server.NewMCPServer(
		"github-brain",
		"1.0.0",
		server.WithStdioTransport(),
	)

	// Register tools
	registerMCPTools(mcpServer, db)
	registerMCPPrompts(mcpServer)

	return mcpServer.Serve(context.Background())
}

// registerMCPTools registers all MCP tools
func registerMCPTools(mcpServer *server.MCPServer, db *sql.DB) {
	// list_discussions tool
	mcpServer.AddTool("list_discussions", "Lists discussions with optional filtering", map[string]interface{}{
		"repository": map[string]interface{}{
			"type":        "string",
			"description": "Filter by repository name. Example: auth-service. Defaults to any repository in the organization.",
		},
		"created_from": map[string]interface{}{
			"type":        "string",
			"description": "Filter by created_at after the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"created_to": map[string]interface{}{
			"type":        "string",
			"description": "Filter by created_at before the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"authors": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Array of author usernames. Example: [john_doe, jane_doe]. Defaults to any author.",
		},
		"fields": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Array of fields to include in the response. Available fields: [title, url, repository, created_at, author, body]. Defaults to all fields.",
		},
	}, func(args map[string]interface{}) (interface{}, error) {
		if isPullRunning() {
			return "A pull is currently running. Please wait until it finishes.", nil
		}
		return handleListDiscussions(db, args)
	})

	// list_issues tool
	mcpServer.AddTool("list_issues", "Lists issues with optional filtering", map[string]interface{}{
		"repository": map[string]interface{}{
			"type":        "string",
			"description": "Filter by repository name. Example: auth-service. Defaults to any repository in the organization.",
		},
		"created_from": map[string]interface{}{
			"type":        "string",
			"description": "Filter by created_at after the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"created_to": map[string]interface{}{
			"type":        "string",
			"description": "Filter by created_at before the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"closed_from": map[string]interface{}{
			"type":        "string",
			"description": "Filter by closed_at after the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"closed_to": map[string]interface{}{
			"type":        "string",
			"description": "Filter by closed_at before the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"authors": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Array of author usernames. Example: [john_doe, jane_doe]. Defaults to any author.",
		},
		"fields": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Array of fields to include in the response. Available fields: [title, url, repository, created_at, closed_at, author, status, body]. Defaults to all fields.",
		},
	}, func(args map[string]interface{}) (interface{}, error) {
		if isPullRunning() {
			return "A pull is currently running. Please wait until it finishes.", nil
		}
		return handleListIssues(db, args)
	})

	// list_pull_requests tool
	mcpServer.AddTool("list_pull_requests", "Lists pull requests with optional filtering", map[string]interface{}{
		"repository": map[string]interface{}{
			"type":        "string",
			"description": "Filter by repository name. Example: auth-service. Defaults to any repository in the organization.",
		},
		"created_from": map[string]interface{}{
			"type":        "string",
			"description": "Filter by created_at after the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"created_to": map[string]interface{}{
			"type":        "string",
			"description": "Filter by created_at before the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"merged_from": map[string]interface{}{
			"type":        "string",
			"description": "Filter by merged_at after the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"merged_to": map[string]interface{}{
			"type":        "string",
			"description": "Filter by merged_at before the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"closed_from": map[string]interface{}{
			"type":        "string",
			"description": "Filter by closed_at after the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"closed_to": map[string]interface{}{
			"type":        "string",
			"description": "Filter by closed_at before the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date.",
		},
		"authors": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Array of author usernames. Example: [john_doe, jane_doe]. Defaults to any author.",
		},
		"fields": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Array of fields to include in the response. Available fields: [title, url, repository, created_at, merged_at, closed_at, author, status, body]. Defaults to all fields.",
		},
	}, func(args map[string]interface{}) (interface{}, error) {
		if isPullRunning() {
			return "A pull is currently running. Please wait until it finishes.", nil
		}
		return handleListPullRequests(db, args)
	})

	// search tool
	mcpServer.AddTool("search", "Full-text search across discussions, issues, and pull requests", map[string]interface{}{
		"query": map[string]interface{}{
			"type":        "string",
			"description": "Search query string. Example: authentication bug.",
		},
		"fields": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "string",
			},
			"description": "Array of fields to include in the response. Available fields: [title, url, repository, created_at, author, type, state, snippet]. Defaults to all fields.",
		},
	}, func(args map[string]interface{}) (interface{}, error) {
		if isPullRunning() {
			return "A pull is currently running. Please wait until it finishes.", nil
		}
		return handleSearch(db, args)
	})
}

// registerMCPPrompts registers MCP prompts
func registerMCPPrompts(mcpServer *server.MCPServer) {
	mcpServer.AddPrompt("user_summary", "Generates a summary of the user's accomplishments", map[string]interface{}{
		"username": mcp.Required(map[string]interface{}{
			"type":        "string",
			"description": "Username. Example: john_doe.",
		}),
		"period": map[string]interface{}{
			"type":        "string",
			"description": "Examples: last week, from August 2025 to September 2025, 2024-01-01 - 2024-12-31",
		},
	}, func(args map[string]interface{}) (interface{}, error) {
		username := args["username"].(string)
		period := ""
		if p, ok := args["period"].(string); ok {
			period = p
		}
		
		return fmt.Sprintf(`Summarize the accomplishments of the user %s during %s, focusing on the most significant contributions first. Use the following approach:

- Use list_discussions to gather discussions they created within %s.
- Use list_issues to gather issues they closed within %s.
- Use list_pull_requests to gather pull requests they closed within %s.
- Aggregate all results, removing duplicates.
- Prioritize and highlight:
  - Discussions (most important)
  - Pull requests (next most important)  
  - Issues (least important)
- For each contribution, include a direct link and relevant metrics or facts.
- Present a concise, unified summary that mixes all types of contributions, with the most impactful items first.`, 
			username, period, period, period, period), nil
	})
}

// MCP tool handlers

// handleListDiscussions handles the list_discussions tool
func handleListDiscussions(db *sql.DB, args map[string]interface{}) (string, error) {
	// Validate fields
	availableFields := []string{"title", "url", "repository", "created_at", "author", "body"}
	fields, err := validateFields(args, availableFields)
	if err != nil {
		return err.Error(), nil
	}

	// Build query
	query := "SELECT title, url, repository, created_at, author, body FROM discussions WHERE 1=1"
	var queryArgs []interface{}

	// Add filters
	if repo, ok := args["repository"].(string); ok && repo != "" {
		query += " AND repository = ?"
		queryArgs = append(queryArgs, repo)
	}

	if createdFrom, ok := args["created_from"].(string); ok && createdFrom != "" {
		query += " AND created_at >= ?"
		queryArgs = append(queryArgs, createdFrom)
	}

	if createdTo, ok := args["created_to"].(string); ok && createdTo != "" {
		query += " AND created_at <= ?"
		queryArgs = append(queryArgs, createdTo)
	}

	if authors, ok := args["authors"].([]interface{}); ok && len(authors) > 0 {
		placeholders := strings.Repeat("?,", len(authors)-1) + "?"
		query += " AND author IN (" + placeholders + ")"
		for _, author := range authors {
			queryArgs = append(queryArgs, author)
		}
	}

	query += " ORDER BY created_at ASC"

	// Execute query
	rows, err := db.Query(query, queryArgs...)
	if err != nil {
		return "", fmt.Errorf("failed to execute query: %w", err)
	}
	defer rows.Close()

	// Process results
	var result strings.Builder
	count := 0
	totalSize := 0
	const maxSize = 990 * 1024 // 990 KB

	for rows.Next() {
		var title, url, repository, createdAt, author, body string
		err := rows.Scan(&title, &url, &repository, &createdAt, &author, &body)
		if err != nil {
			continue
		}

		// Build discussion entry
		entry := buildDiscussionEntry(title, url, repository, createdAt, author, body, fields)
		
		// Check size limit
		if totalSize+len(entry) > maxSize {
			// Get remaining count
			remaining := 0
			for rows.Next() {
				remaining++
			}
			
			if count > 0 {
				result.WriteString(fmt.Sprintf("Showing only the first %d discussions. There's %d more, please refine your search. Use `created_from` and `created_to` parameters to narrow the results.\n\n---\n\n", count, remaining))
			}
			break
		}

		result.WriteString(entry)
		totalSize += len(entry)
		count++
	}

	if count == 0 {
		return "No discussions found.", nil
	}

	return result.String(), nil
}

// handleListIssues handles the list_issues tool
func handleListIssues(db *sql.DB, args map[string]interface{}) (string, error) {
	// Validate fields
	availableFields := []string{"title", "url", "repository", "created_at", "closed_at", "author", "status", "body"}
	fields, err := validateFields(args, availableFields)
	if err != nil {
		return err.Error(), nil
	}

	// Build query
	query := "SELECT title, url, repository, created_at, closed_at, author, body FROM issues WHERE 1=1"
	var queryArgs []interface{}

	// Add filters
	if repo, ok := args["repository"].(string); ok && repo != "" {
		query += " AND repository = ?"
		queryArgs = append(queryArgs, repo)
	}

	if createdFrom, ok := args["created_from"].(string); ok && createdFrom != "" {
		query += " AND created_at >= ?"
		queryArgs = append(queryArgs, createdFrom)
	}

	if createdTo, ok := args["created_to"].(string); ok && createdTo != "" {
		query += " AND created_at <= ?"
		queryArgs = append(queryArgs, createdTo)
	}

	if closedFrom, ok := args["closed_from"].(string); ok && closedFrom != "" {
		query += " AND closed_at >= ?"
		queryArgs = append(queryArgs, closedFrom)
	}

	if closedTo, ok := args["closed_to"].(string); ok && closedTo != "" {
		query += " AND closed_at <= ?"
		queryArgs = append(queryArgs, closedTo)
	}

	if authors, ok := args["authors"].([]interface{}); ok && len(authors) > 0 {
		placeholders := strings.Repeat("?,", len(authors)-1) + "?"
		query += " AND author IN (" + placeholders + ")"
		for _, author := range authors {
			queryArgs = append(queryArgs, author)
		}
	}

	query += " ORDER BY created_at ASC"

	// Execute query
	rows, err := db.Query(query, queryArgs...)
	if err != nil {
		return "", fmt.Errorf("failed to execute query: %w", err)
	}
	defer rows.Close()

	// Process results
	var result strings.Builder
	count := 0
	totalSize := 0
	const maxSize = 990 * 1024 // 990 KB

	for rows.Next() {
		var title, url, repository, createdAt, author, body string
		var closedAt sql.NullString
		err := rows.Scan(&title, &url, &repository, &createdAt, &closedAt, &author, &body)
		if err != nil {
			continue
		}

		status := "open"
		closedAtStr := ""
		if closedAt.Valid {
			status = "closed"
			closedAtStr = closedAt.String
		}

		// Build issue entry
		entry := buildIssueEntry(title, url, repository, createdAt, closedAtStr, author, status, body, fields)
		
		// Check size limit
		if totalSize+len(entry) > maxSize {
			// Get remaining count
			remaining := 0
			for rows.Next() {
				remaining++
			}
			
			if count > 0 {
				result.WriteString(fmt.Sprintf("Showing only the first %d issues. There's %d more, please refine your search.\n\n---\n\n", count, remaining))
			}
			break
		}

		result.WriteString(entry)
		totalSize += len(entry)
		count++
	}

	if count == 0 {
		return "No issues found.", nil
	}

	return result.String(), nil
}

// handleListPullRequests handles the list_pull_requests tool
func handleListPullRequests(db *sql.DB, args map[string]interface{}) (string, error) {
	// Validate fields
	availableFields := []string{"title", "url", "repository", "created_at", "merged_at", "closed_at", "author", "status", "body"}
	fields, err := validateFields(args, availableFields)
	if err != nil {
		return err.Error(), nil
	}

	// Build query
	query := "SELECT title, url, repository, created_at, merged_at, closed_at, author, body FROM pull_requests WHERE 1=1"
	var queryArgs []interface{}

	// Add filters
	if repo, ok := args["repository"].(string); ok && repo != "" {
		query += " AND repository = ?"
		queryArgs = append(queryArgs, repo)
	}

	if createdFrom, ok := args["created_from"].(string); ok && createdFrom != "" {
		query += " AND created_at >= ?"
		queryArgs = append(queryArgs, createdFrom)
	}

	if createdTo, ok := args["created_to"].(string); ok && createdTo != "" {
		query += " AND created_at <= ?"
		queryArgs = append(queryArgs, createdTo)
	}

	if mergedFrom, ok := args["merged_from"].(string); ok && mergedFrom != "" {
		query += " AND merged_at >= ?"
		queryArgs = append(queryArgs, mergedFrom)
	}

	if mergedTo, ok := args["merged_to"].(string); ok && mergedTo != "" {
		query += " AND merged_at <= ?"
		queryArgs = append(queryArgs, mergedTo)
	}

	if closedFrom, ok := args["closed_from"].(string); ok && closedFrom != "" {
		query += " AND closed_at >= ?"
		queryArgs = append(queryArgs, closedFrom)
	}

	if closedTo, ok := args["closed_to"].(string); ok && closedTo != "" {
		query += " AND closed_at <= ?"
		queryArgs = append(queryArgs, closedTo)
	}

	if authors, ok := args["authors"].([]interface{}); ok && len(authors) > 0 {
		placeholders := strings.Repeat("?,", len(authors)-1) + "?"
		query += " AND author IN (" + placeholders + ")"
		for _, author := range authors {
			queryArgs = append(queryArgs, author)
		}
	}

	query += " ORDER BY created_at ASC"

	// Get total count first
	countQuery := strings.Replace(query, "SELECT title, url, repository, created_at, merged_at, closed_at, author, body FROM", "SELECT COUNT(*) FROM", 1)
	countQuery = strings.Replace(countQuery, " ORDER BY created_at ASC", "", 1)
	
	var totalCount int
	err = db.QueryRow(countQuery, queryArgs...).Scan(&totalCount)
	if err != nil {
		return "", fmt.Errorf("failed to get count: %w", err)
	}

	if totalCount == 0 {
		return "No pull requests found.", nil
	}

	// Execute main query
	rows, err := db.Query(query, queryArgs...)
	if err != nil {
		return "", fmt.Errorf("failed to execute query: %w", err)
	}
	defer rows.Close()

	// Process results
	var result strings.Builder
	result.WriteString(fmt.Sprintf("Total %d pull requests found.\n\n", totalCount))

	count := 0
	totalSize := len(result.String())
	const maxSize = 990 * 1024 // 990 KB

	for rows.Next() {
		var title, url, repository, createdAt, author, body string
		var mergedAt, closedAt sql.NullString
		err := rows.Scan(&title, &url, &repository, &createdAt, &mergedAt, &closedAt, &author, &body)
		if err != nil {
			continue
		}

		status := "open"
		mergedAtStr := ""
		closedAtStr := ""
		
		if mergedAt.Valid {
			status = "closed" // merged PRs are also closed
			mergedAtStr = mergedAt.String
		}
		if closedAt.Valid {
			closedAtStr = closedAt.String
			if status != "closed" {
				status = "closed"
			}
		}

		// Build PR entry
		entry := buildPullRequestEntry(title, url, repository, createdAt, mergedAtStr, closedAtStr, author, status, body, fields)
		
		// Check size limit
		if totalSize+len(entry) > maxSize {
			// Get remaining count
			remaining := totalCount - count
			
			result.WriteString(fmt.Sprintf("Showing only the first %d pull requests. There's %d more, please refine your search.\n\n---\n\n", count, remaining))
			break
		}

		result.WriteString(entry)
		totalSize += len(entry)
		count++
	}

	return result.String(), nil
}

// handleSearch handles the search tool
func handleSearch(db *sql.DB, args map[string]interface{}) (string, error) {
	// Get required query parameter
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "Search query is required.", nil
	}

	// Validate fields
	availableFields := []string{"title", "url", "repository", "created_at", "author", "type", "state", "snippet"}
	fields, err := validateFields(args, availableFields)
	if err != nil {
		return err.Error(), nil
	}

	// Build FTS5 query
	sqlQuery := `
		SELECT type, title, body, url, repository, author, created_at, state, 
		       snippet(search, 1, '**', '**', '...', 64) as snippet
		FROM search 
		WHERE search MATCH ?
		ORDER BY bm25(search)
		LIMIT 10
	`

	// Execute query
	rows, err := db.Query(sqlQuery, query)
	if err != nil {
		return "", fmt.Errorf("failed to execute search: %w", err)
	}
	defer rows.Close()

	// Process results
	var result strings.Builder
	count := 0

	for rows.Next() {
		var itemType, title, body, url, repository, author, createdAt, state, snippet string
		err := rows.Scan(&itemType, &title, &body, &url, &repository, &author, &createdAt, &state, &snippet)
		if err != nil {
			continue
		}

		// Build search result entry
		entry := buildSearchEntry(title, url, repository, createdAt, author, itemType, state, snippet, fields)
		result.WriteString(entry)
		count++
	}

	if count == 0 {
		return fmt.Sprintf("No results found for \"%s\".", query), nil
	}

	return result.String(), nil
}

// Helper functions for building entries

// validateFields validates the fields parameter
func validateFields(args map[string]interface{}, availableFields []string) ([]string, error) {
	fieldsInterface, ok := args["fields"]
	if !ok {
		return availableFields, nil // Default to all fields
	}

	fieldsArray, ok := fieldsInterface.([]interface{})
	if !ok {
		return nil, fmt.Errorf("fields must be an array")
	}

	var fields []string
	var invalidFields []string

	for _, f := range fieldsArray {
		field, ok := f.(string)
		if !ok {
			continue
		}

		// Check if field is valid
		valid := false
		for _, available := range availableFields {
			if field == available {
				valid = true
				break
			}
		}

		if valid {
			fields = append(fields, field)
		} else {
			invalidFields = append(invalidFields, field)
		}
	}

	if len(invalidFields) > 0 {
		return nil, fmt.Errorf("Invalid fields: %s\n\nUse one of the available fields: %s",
			strings.Join(invalidFields, ", "),
			strings.Join(availableFields, ", "))
	}

	if len(fields) == 0 {
		return availableFields, nil
	}

	return fields, nil
}

// buildDiscussionEntry builds a formatted discussion entry
func buildDiscussionEntry(title, url, repository, createdAt, author, body string, fields []string) string {
	var entry strings.Builder
	entry.WriteString("## " + title + "\n\n")

	fieldMap := map[string]string{
		"url":        "- URL: " + url + "\n",
		"repository": "- Repository: " + repository + "\n",
		"created_at": "- Created at: " + createdAt + "\n",
		"author":     "- Author: " + author + "\n",
	}

	for _, field := range fields {
		if field == "title" || field == "body" {
			continue // Handled separately
		}
		if content, ok := fieldMap[field]; ok {
			entry.WriteString(content)
		}
	}

	// Add body if requested
	for _, field := range fields {
		if field == "body" {
			entry.WriteString("\n" + body + "\n")
			break
		}
	}

	entry.WriteString("\n---\n\n")
	return entry.String()
}

// buildIssueEntry builds a formatted issue entry
func buildIssueEntry(title, url, repository, createdAt, closedAt, author, status, body string, fields []string) string {
	var entry strings.Builder
	entry.WriteString("## " + title + "\n\n")

	fieldMap := map[string]string{
		"url":        "- URL: " + url + "\n",
		"repository": "- Repository: " + repository + "\n",
		"created_at": "- Created at: " + createdAt + "\n",
		"closed_at":  "- Closed at: " + closedAt + "\n",
		"author":     "- Author: " + author + "\n",
		"status":     "- Status: " + status + "\n",
	}

	for _, field := range fields {
		if field == "title" || field == "body" {
			continue // Handled separately
		}
		if content, ok := fieldMap[field]; ok {
			if field == "closed_at" && closedAt == "" {
				continue // Skip empty closed_at
			}
			entry.WriteString(content)
		}
	}

	// Add body if requested
	for _, field := range fields {
		if field == "body" {
			entry.WriteString("\n" + body + "\n")
			break
		}
	}

	entry.WriteString("\n---\n\n")
	return entry.String()
}

// buildPullRequestEntry builds a formatted pull request entry
func buildPullRequestEntry(title, url, repository, createdAt, mergedAt, closedAt, author, status, body string, fields []string) string {
	var entry strings.Builder
	entry.WriteString("## " + title + "\n\n")

	fieldMap := map[string]string{
		"url":        "- URL: " + url + "\n",
		"repository": "- Repository: " + repository + "\n",
		"created_at": "- Created at: " + createdAt + "\n",
		"merged_at":  "- Merged at: " + mergedAt + "\n",
		"closed_at":  "- Closed at: " + closedAt + "\n",
		"author":     "- Author: " + author + "\n",
		"status":     "- Status: " + status + "\n",
	}

	for _, field := range fields {
		if field == "title" || field == "body" {
			continue // Handled separately
		}
		if content, ok := fieldMap[field]; ok {
			if (field == "merged_at" && mergedAt == "") || (field == "closed_at" && closedAt == "") {
				continue // Skip empty dates
			}
			entry.WriteString(content)
		}
	}

	// Add body if requested
	for _, field := range fields {
		if field == "body" {
			entry.WriteString("\n" + body + "\n")
			break
		}
	}

	entry.WriteString("\n---\n\n")
	return entry.String()
}

// buildSearchEntry builds a formatted search result entry
func buildSearchEntry(title, url, repository, createdAt, author, itemType, state, snippet string, fields []string) string {
	var entry strings.Builder
	entry.WriteString("## " + title + "\n\n")

	fieldMap := map[string]string{
		"url":        "- URL: " + url + "\n",
		"type":       "- Type: " + itemType + "\n",
		"repository": "- Repository: " + repository + "\n",
		"created_at": "- Created at: " + createdAt + "\n",
		"author":     "- Author: " + author + "\n",
		"state":      "- State: " + state + "\n",
	}

	for _, field := range fields {
		if field == "title" || field == "snippet" {
			continue // Handled separately
		}
		if content, ok := fieldMap[field]; ok {
			entry.WriteString(content)
		}
	}

	// Add snippet if requested
	for _, field := range fields {
		if field == "snippet" {
			entry.WriteString("\n" + snippet + "\n")
			break
		}
	}

	entry.WriteString("\n---\n\n")
	return entry.String()
}

// Web UI implementation

// setupWebUI starts the web server for the search UI
func setupWebUI(config *Config) error {
	if isPullRunning() {
		return fmt.Errorf("a pull is currently running. Please wait until it finishes")
	}

	db, err := initDatabase(config)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	// Serve the web UI
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		serveIndex(w, r, db)
	})

	http.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		serveSearch(w, r, db)
	})

	fmt.Printf("Starting web server on http://localhost:8080\n")
	return http.ListenAndServe(":8080", nil)
}

// serveIndex serves the main index page
func serveIndex(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	tmpl := `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>GitHub Brain - Search</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', sans-serif;
            background: linear-gradient(135deg, #1a1a1a 0%, #2d1b69 100%);
            color: #ffffff;
            min-height: 100vh;
            line-height: 1.6;
        }

        .container {
            max-width: 1200px;
            margin: 0 auto;
            padding: 2rem;
        }

        .header {
            text-align: center;
            margin-bottom: 3rem;
        }

        .header h1 {
            font-size: 3rem;
            font-weight: 900;
            margin-bottom: 1rem;
            text-transform: uppercase;
            letter-spacing: 0.1em;
            text-shadow: 2px 2px 0px #7c3aed;
        }

        .header p {
            font-size: 1.2rem;
            opacity: 0.8;
        }

        .search-container {
            margin-bottom: 2rem;
        }

        .search-box {
            position: relative;
            max-width: 600px;
            margin: 0 auto;
        }

        .search-input {
            width: 100%;
            padding: 1rem 1.5rem;
            font-size: 1.2rem;
            border: 3px solid #7c3aed;
            border-radius: 50px;
            background: #ffffff;
            color: #1a1a1a;
            outline: none;
            transition: all 0.3s ease;
            box-shadow: 0 4px 20px rgba(124, 58, 237, 0.3);
        }

        .search-input:focus {
            border-color: #a855f7;
            transform: translateY(-2px);
            box-shadow: 0 6px 25px rgba(124, 58, 237, 0.4);
        }

        .search-input::placeholder {
            color: #666;
        }

        .results {
            margin-top: 2rem;
        }

        .result-item {
            background: rgba(255, 255, 255, 0.1);
            border: 2px solid #374151;
            border-radius: 8px;
            padding: 1.5rem;
            margin-bottom: 1rem;
            transition: all 0.3s ease;
            backdrop-filter: blur(10px);
        }

        .result-item:hover {
            border-color: #7c3aed;
            transform: translateY(-2px);
            box-shadow: 0 4px 20px rgba(124, 58, 237, 0.2);
        }

        .result-title {
            font-size: 1.3rem;
            font-weight: 700;
            margin-bottom: 0.5rem;
            text-transform: uppercase;
            letter-spacing: 0.05em;
        }

        .result-title a {
            color: #ffffff;
            text-decoration: none;
        }

        .result-title a:hover {
            color: #a855f7;
        }

        .result-meta {
            font-size: 0.9rem;
            opacity: 0.8;
            margin-bottom: 1rem;
        }

        .result-meta span {
            margin-right: 1rem;
            padding: 0.2rem 0.5rem;
            background: rgba(124, 58, 237, 0.3);
            border-radius: 4px;
        }

        .result-snippet {
            font-size: 1rem;
            line-height: 1.6;
            border-left: 3px solid #7c3aed;
            padding-left: 1rem;
            opacity: 0.9;
        }

        .loading {
            text-align: center;
            padding: 2rem;
            font-size: 1.1rem;
            opacity: 0.7;
        }

        .no-results {
            text-align: center;
            padding: 3rem;
            font-size: 1.2rem;
            opacity: 0.7;
        }

        .htmx-indicator {
            display: none;
        }

        .htmx-request .htmx-indicator {
            display: inline;
        }

        .htmx-request.htmx-indicator {
            display: inline;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🧠 GitHub Brain</h1>
            <p>Search across discussions, issues, and pull requests</p>
        </div>

        <div class="search-container">
            <div class="search-box">
                <input type="text" 
                       class="search-input" 
                       placeholder="Search for discussions, issues, pull requests..."
                       hx-post="/search"
                       hx-target="#results"
                       hx-trigger="keyup changed delay:300ms, search"
                       hx-indicator=".htmx-indicator">
            </div>
        </div>

        <div id="results" class="results">
            <div class="loading htmx-indicator">
                Searching...
            </div>
        </div>
    </div>
</body>
</html>
`

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(tmpl))
}

// serveSearch handles search requests
func serveSearch(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.FormValue("search-input")
	if strings.TrimSpace(query) == "" {
		w.Write([]byte(""))
		return
	}

	// Perform FTS5 search
	sqlQuery := `
		SELECT type, title, body, url, repository, author, created_at, state,
		       snippet(search, 1, '<mark>', '</mark>', '...', 64) as snippet
		FROM search 
		WHERE search MATCH ?
		ORDER BY bm25(search)
		LIMIT 10
	`

	rows, err := db.Query(sqlQuery, query)
	if err != nil {
		http.Error(w, "Search failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []map[string]string
	for rows.Next() {
		var itemType, title, body, url, repository, author, createdAt, state, snippet string
		err := rows.Scan(&itemType, &title, &body, &url, &repository, &author, &createdAt, &state, &snippet)
		if err != nil {
			continue
		}

		results = append(results, map[string]string{
			"type":       itemType,
			"title":      html.EscapeString(title),
			"url":        url,
			"repository": html.EscapeString(repository),
			"author":     html.EscapeString(author),
			"created_at": createdAt,
			"state":      state,
			"snippet":    snippet, // Already contains HTML markup from snippet()
		})
	}

	if len(results) == 0 {
		w.Write([]byte(`<div class="no-results">No results found for "` + html.EscapeString(query) + `"</div>`))
		return
	}

	// Render results
	var response strings.Builder
	for _, result := range results {
		response.WriteString(`<div class="result-item">`)
		response.WriteString(`<div class="result-title"><a href="` + result["url"] + `" target="_blank">` + result["title"] + `</a></div>`)
		response.WriteString(`<div class="result-meta">`)
		response.WriteString(`<span>` + strings.Title(result["type"]) + `</span>`)
		response.WriteString(`<span>` + result["repository"] + `</span>`)
		response.WriteString(`<span>` + result["author"] + `</span>`)
		response.WriteString(`<span>` + result["state"] + `</span>`)
		response.WriteString(`</div>`)
		response.WriteString(`<div class="result-snippet">` + result["snippet"] + `</div>`)
		response.WriteString(`</div>`)
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(response.String()))
}

// Main function and command handling

func main() {
	config, err := LoadConfig()
	if err != nil {
		slog.Error("Configuration error", "error", err)
		os.Exit(1)
	}

	args := os.Args[1:]
	if len(args) < 1 {
		slog.Error("Usage: github-brain <command>")
		os.Exit(1)
	}

	command := args[0]

	switch command {
	case "pull":
		err = runPull(config)
	case "mcp":
		err = setupMCPServer(config)
	case "ui":
		err = setupWebUI(config)
	default:
		slog.Error("Unknown command", "command", command)
		os.Exit(1)
	}

	if err != nil {
		slog.Error("Command failed", "command", command, "error", err)
		os.Exit(1)
	}
}

// runPull executes the pull command
func runPull(config *Config) error {
	// Acquire pull lock
	if err := acquirePullLock(config); err != nil {
		return err
	}
	defer releasePullLock()

	// Initialize database
	db, err := initDatabase(config)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	// Initialize console
	console := NewConsole()
	SetupGlobalLogger(console)

	// Parse items to pull
	var itemsToPull []string
	if config.Items != "" {
		for _, item := range strings.Split(config.Items, ",") {
			itemsToPull = append(itemsToPull, strings.TrimSpace(item))
		}
		console.SetItemsSelection(itemsToPull)
	}

	console.Show()
	defer console.Hide()

	slog.Info("Starting GitHub data synchronization")

	// Clear data if force flag is set
	if err := clearData(db, config); err != nil {
		return fmt.Errorf("failed to clear data: %w", err)
	}

	// Create GitHub client
	client := createGitHubClient(config.GithubToken)

	// Update console with rate limit info periodically
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		
		for {
			select {
			case <-ticker.C:
				if atomic.LoadInt32(&pullIsRunning) == 0 {
					return
				}
				
				// Update console with current status
				rateLimitInfoMutex.RLock()
				rateLimit := currentRateLimit
				rateLimitInfoMutex.RUnlock()
				
				statusMutex.Lock()
				status := statusCounters
				statusMutex.Unlock()
				
				console.UpdateAPIStatus(status)
				console.UpdateRateLimit(rateLimit)
			}
		}
	}()

	// Pull items based on selection
	shouldPull := func(item string) bool {
		if len(itemsToPull) == 0 {
			return true // Pull all if no specific selection
		}
		for _, selected := range itemsToPull {
			if strings.EqualFold(selected, item) ||
				strings.EqualFold(selected, strings.Replace(item, " ", "_", -1)) {
				return true
			}
		}
		return false
	}

	// Pull repositories
	if shouldPull("repositories") {
		if err := pullRepositories(client, db, config, console); err != nil {
			slog.Error("Failed to pull repositories", "error", err)
			console.UpdateItemStatus("Repositories", "failed", 0, 1)
		}
	}

	// Pull discussions
	if shouldPull("discussions") {
		if err := pullDiscussions(client, db, config, console); err != nil {
			slog.Error("Failed to pull discussions", "error", err)
		}
	}

	// Pull issues
	if shouldPull("issues") {
		if err := pullIssues(client, db, config, console); err != nil {
			slog.Error("Failed to pull issues", "error", err)
		}
	}

	// Pull pull requests
	if shouldPull("pull_requests") || shouldPull("pullrequests") || shouldPull("prs") {
		if err := pullPullRequests(client, db, config, console); err != nil {
			slog.Error("Failed to pull pull requests", "error", err)
		}
	}

	// Rebuild search index
	slog.Info("Rebuilding search index")
	if err := rebuildSearchIndex(db, console); err != nil {
		slog.Error("Failed to rebuild search index", "error", err)
	}

	slog.Info("GitHub data synchronization completed")
	
	// Wait a moment for user to see final state
	time.Sleep(2 * time.Second)
	
	return nil
}