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

// Database version constants for table-specific versioning
const (
	REPOSITORIES_VERSION   = 1 // repositories_1
	DISCUSSIONS_VERSION    = 1 // discussions_1
	ISSUES_VERSION         = 1 // issues_1
	PULL_REQUESTS_VERSION  = 1 // pull_requests_1
	SEARCH_VERSION         = 1 // search_1
)

// Global variables for rate limit handling and status tracking
var (
	rateLimitMutex        sync.Mutex
	rateLimitHit          bool
	rateLimitResetTime    time.Time
	secondaryRateLimitHit bool
	secondaryResetTime    time.Time
	backoffDuration       time.Duration = 5 * time.Second  // Increased from 1s to 5s for better handling
	maxBackoffDuration    time.Duration = 10 * time.Minute // Keep at 10 minutes max

	// Rate limit information from headers
	currentRateLimit      RateLimitInfo = RateLimitInfo{Limit: -1, Remaining: -1, Used: -1} // Initialize with -1 for unknown
	rateLimitInfoMutex    sync.RWMutex

	// Status code counters
	statusCounters StatusCounters
	statusMutex    sync.Mutex

	// Global console reference for logging
	globalLogger *slog.Logger
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
	return true // Handle all levels
}

// Handle processes a log record
func (h *ConsoleHandler) Handle(ctx context.Context, record slog.Record) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if h.console == nil {
		return nil // No console available
	}

	// Build the message with attributes
	message := record.Message
	record.Attrs(func(a slog.Attr) bool {
		if a.Key != "" && a.Value.String() != "" {
			message += fmt.Sprintf(" %s=%s", a.Key, a.Value.String())
		}
		return true
	})

	// Add any handler-level attributes
	for _, attr := range h.attrs {
		if attr.Key != "" && attr.Value.String() != "" {
			message += fmt.Sprintf(" %s=%s", attr.Key, attr.Value.String())
		}
	}

	// Log to console (which handles formatting with timestamp)
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
	Limit     int       // x-ratelimit-limit (default -1 for unknown)
	Remaining int       // x-ratelimit-remaining (default -1 for unknown)
	Used      int       // x-ratelimit-used (default -1 for unknown)
	Reset     time.Time // x-ratelimit-reset (Unix timestamp)
}

// StatusCounters tracks HTTP response status codes
type StatusCounters struct {
	Success2XX int // 2XX status codes
	Error4XX   int // 4XX status codes  
	Error5XX   int // 5XX status codes
}

// addRequestDelay adds a delay between API requests to help avoid secondary rate limits
func addRequestDelay() {
	// Check if we're in a secondary rate limit state - if so, use longer delays
	rateLimitMutex.Lock()
	inSecondaryLimit := secondaryRateLimitHit
	inPrimaryLimit := rateLimitHit
	rateLimitMutex.Unlock()
	
	var delay time.Duration
	if inSecondaryLimit {
		// Much longer delay when we're recovering from secondary rate limits
		delay = time.Duration(2000+rand.Intn(2000)) * time.Millisecond // 2-4 seconds
	} else if inPrimaryLimit {
		// Longer delay when recovering from primary rate limits
		delay = time.Duration(1500+rand.Intn(1000)) * time.Millisecond // 1.5-2.5 seconds
	} else {
		// Check current rate limit status for adaptive delays
		rateLimitInfoMutex.RLock()
		remaining := currentRateLimit.Remaining
		limit := currentRateLimit.Limit
		rateLimitInfoMutex.RUnlock()
		
		if remaining > 0 && limit > 0 {
			// Calculate rate limit utilization
			utilization := float64(limit-remaining) / float64(limit)
			
			if utilization > 0.9 { // Above 90% utilization
				// Very conservative delay when close to rate limit
				delay = time.Duration(1000+rand.Intn(1500)) * time.Millisecond // 1-2.5 seconds
			} else if utilization > 0.7 { // Above 70% utilization
				// More conservative delay
				delay = time.Duration(750+rand.Intn(750)) * time.Millisecond // 0.75-1.5 seconds
			} else {
				// Normal delay to avoid secondary rate limits
				delay = time.Duration(500+rand.Intn(500)) * time.Millisecond // 0.5-1 seconds
			}
		} else {
			// Default delay when rate limit info is unknown
			delay = time.Duration(750+rand.Intn(750)) * time.Millisecond // 0.75-1.5 seconds
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

// getRateLimitInfo returns a copy of the current rate limit information
func getRateLimitInfo() RateLimitInfo {
	rateLimitInfoMutex.RLock()
	defer rateLimitInfoMutex.RUnlock()
	return currentRateLimit
}

// updateStatusCounter increments the appropriate status code counter
func updateStatusCounter(statusCode int) {
	statusMutex.Lock()
	defer statusMutex.Unlock()

	switch {
	case statusCode >= 200 && statusCode < 300:
		statusCounters.Success2XX++
	case statusCode >= 400 && statusCode < 500:
		statusCounters.Error4XX++
	case statusCode >= 500:
		statusCounters.Error5XX++
	}
}

// getStatusCounters returns a copy of the current status counters
func getStatusCounters() StatusCounters {
	statusMutex.Lock()
	defer statusMutex.Unlock()
	return statusCounters
}

// CustomTransport wraps the default HTTP transport to capture response headers and status codes
type CustomTransport struct {
	wrapped http.RoundTripper
}

func (ct *CustomTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := ct.wrapped.RoundTrip(req)
	if resp != nil {
		// Update status counters
		updateStatusCounter(resp.StatusCode)
		
		// Update rate limit info from headers
		updateRateLimitInfo(resp.Header)
		
		// Handle 429 status code (rate limit) with Retry-After header
		if resp.StatusCode == 429 {
			retryAfter := resp.Header.Get("Retry-After")
			
			rateLimitMutex.Lock()
			defer rateLimitMutex.Unlock()
			
			// Check if this is secondary rate limit (abuse detection)
			// GitHub secondary rate limits typically have abuse detection messages
			if retryAfter != "" {
				if retryAfterInt, parseErr := strconv.Atoi(retryAfter); parseErr == nil {
					waitDuration := time.Duration(retryAfterInt) * time.Second
					
					// Cap the wait time to prevent excessive waiting
					maxWaitTime := 10 * time.Minute
					if waitDuration > maxWaitTime {
						slog.Warn("Capping excessive Retry-After duration", "from", waitDuration, "to", maxWaitTime)
						waitDuration = maxWaitTime
					}
					
					// Set secondary rate limit if this appears to be abuse detection
					// (typically longer wait times indicate secondary rate limits)
					if waitDuration > 60*time.Second {
						secondaryRateLimitHit = true
						secondaryResetTime = time.Now().Add(waitDuration)
						slog.Info("GitHub API secondary rate limit (429) detected", "retry_after", waitDuration.String(), "until", secondaryResetTime.Format(time.RFC3339))
					} else {
						// Shorter wait times are likely primary rate limits
						rateLimitHit = true
						rateLimitResetTime = time.Now().Add(waitDuration)
						slog.Info("GitHub API primary rate limit (429) detected", "retry_after", waitDuration.String(), "until", rateLimitResetTime.Format(time.RFC3339))
					}
				}
			} else {
				// No Retry-After header, assume secondary rate limit with default backoff
				secondaryRateLimitHit = true
				waitDuration := backoffDuration
				
				// Increase backoff for next time (exponential backoff)
				backoffDuration = backoffDuration * 2
				if backoffDuration > maxBackoffDuration {
					backoffDuration = maxBackoffDuration
				}
				
				secondaryResetTime = time.Now().Add(waitDuration)
				slog.Info("GitHub API secondary rate limit (429) detected without Retry-After", "backoff", waitDuration.String(), "until", secondaryResetTime.Format(time.RFC3339))
			}
		}
	}
	return resp, err
}

func init() {
	// No need to seed the random number generator in Go 1.20+
	// It's automatically seeded with a random value
}

// Config holds all application configuration
type Config struct {
	GithubToken           string
	Organization          string
	DBDir                 string
	Items                 []string // Items to pull (repositories, discussions, issues, pull-requests)
	Force                 bool     // Remove all data before pulling
	ExcludedRepositories  []string // Comma-separated list of repositories to exclude from the pull of discussions, issues, and pull-requests
}

// LoadConfig creates a config from command line arguments and environment variables
// Command line arguments take precedence over environment variables
func LoadConfig(args []string) *Config {
	config := &Config{
		DBDir: "./db", // Default value
	}

	// Load from environment variables first
	config.GithubToken = os.Getenv("GITHUB_TOKEN")
	config.Organization = os.Getenv("ORGANIZATION")

	if dbDir := os.Getenv("DB_DIR"); dbDir != "" {
		config.DBDir = dbDir
	}

	if excludedRepos := os.Getenv("EXCLUDED_REPOSITORIES"); excludedRepos != "" {
		config.ExcludedRepositories = splitItems(excludedRepos)
	}

	// Command line args override environment variables
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			if i+1 < len(args) {
				config.GithubToken = args[i+1]
				i++
			}
		case "-o":
			if i+1 < len(args) {
				config.Organization = args[i+1]
				i++
			}
		case "-db":
			if i+1 < len(args) {
				config.DBDir = args[i+1]
				i++
			}
		case "-i":
			if i+1 < len(args) {
				config.Items = splitItems(args[i+1])
				i++
			}
		case "-e":
			if i+1 < len(args) {
				config.ExcludedRepositories = splitItems(args[i+1])
				i++
			}
		case "-f":
			config.Force = true
		}
	}

	return config
}

// isRepositoryExcluded checks if a repository is in the excluded list
func isRepositoryExcluded(repoName string, excludedRepos []string) bool {
	for _, excluded := range excludedRepos {
		if strings.TrimSpace(excluded) == strings.TrimSpace(repoName) {
			return true
		}
	}
	return false
}

// splitItems splits a comma-separated items list
func splitItems(items string) []string {
	if items == "" {
		return nil
	}

	itemNames := strings.Split(items, ",")
	for i, name := range itemNames {
		itemNames[i] = strings.TrimSpace(name)
	}
	return itemNames
}

// LogEntry represents a timestamped log message
type LogEntry struct {
	timestamp time.Time
	message   string
}

// Console represents a synchronized console output manager
type Console struct {
	mutex       sync.Mutex
	updateChan  chan struct{}
	stopChan    chan struct{}
	throttleMs  int
	progressRef *Progress // Reference to the Progress instance
	logEntries  []LogEntry // Last 5 log messages
	maxLogEntries int      // Maximum number of log entries to keep
}

// NewConsole creates a new console manager with throttled updates
func NewConsole(throttleMs int) *Console {
	if throttleMs <= 0 {
		throttleMs = 150 // Reduced default throttle for faster refresh rate
	}

	console := &Console{
		updateChan: make(chan struct{}, 3), // Reduced buffer size to prevent excessive queuing
		stopChan:   make(chan struct{}),
		throttleMs: throttleMs,
		maxLogEntries: 5, // Keep last 5 log messages
	}

	return console
}

// SetProgressRef sets the reference to the Progress instance
func (c *Console) SetProgressRef(p *Progress) {
	c.progressRef = p
}

// Start begins the console update loop
func (c *Console) Start() {
	go func() {
		throttle := time.NewTicker(time.Duration(c.throttleMs) * time.Millisecond)
		defer throttle.Stop()

		var pendingUpdate bool
		var lastRender time.Time

		for {
			select {
			case <-c.stopChan:
				return
			case <-c.updateChan:
				// Only mark as pending if enough time has passed since last render
				if time.Since(lastRender) > time.Duration(c.throttleMs/2)*time.Millisecond {
					pendingUpdate = true
				}
			case <-throttle.C:
				if pendingUpdate && c.progressRef != nil {
					pendingUpdate = false
					lastRender = time.Now()
					// Actual rendering happens here
					c.progressRef.renderStatus()
				}
			}
		}
	}()
}

// RequestUpdate signals that the console should be updated
func (c *Console) RequestUpdate() {
	select {
	case c.updateChan <- struct{}{}:
		// Update requested
	default:
		// Channel buffer is full, update will happen soon anyway
	}
}

// Stop stops the console manager
func (c *Console) Stop() {
	close(c.stopChan)
}

// Log adds a log message with timestamp to the console with batching
func (c *Console) Log(format string, args ...interface{}) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	
	message := fmt.Sprintf(format, args...)
	entry := LogEntry{
		timestamp: time.Now(),
		message:   message,
	}
	
	// Add new entry
	c.logEntries = append(c.logEntries, entry)
	
	// Keep only the last maxLogEntries
	if len(c.logEntries) > c.maxLogEntries {
		c.logEntries = c.logEntries[1:]
	}
	
	// Only request console update for important messages or errors to reduce spam
	isImportant := strings.Contains(message, "Error:") || 
		         strings.Contains(message, "Failed") || 
		         strings.Contains(message, "rate limit") ||
		         strings.Contains(message, "429")
	
	if isImportant {
		c.RequestUpdate()
	}
	// For non-important messages, let the regular ticker handle updates
}

// GetLogEntries returns a copy of the current log entries
func (c *Console) GetLogEntries() []LogEntry {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	
	// Ensure we never return more than maxLogEntries, even if the slice temporarily exceeds it
	maxEntries := c.maxLogEntries
	if len(c.logEntries) > maxEntries {
		// Trim to maxEntries and update the slice
		c.logEntries = c.logEntries[len(c.logEntries)-maxEntries:]
	}
	
	// Return a copy to avoid race conditions
	entries := make([]LogEntry, len(c.logEntries))
	copy(entries, c.logEntries)
	return entries
}

// ClearScreen clears the console screen efficiently
func (c *Console) ClearScreen() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	// Use minimal escape sequence - just return to beginning of line
	fmt.Print("\r")
}

// getTerminalSize detects terminal size for bounds checking
func getTerminalSize() (width, height int) {
	// Try to get terminal size
	if term.IsTerminal(int(os.Stdout.Fd())) {
		width, height, err := term.GetSize(int(os.Stdout.Fd()))
		if err == nil && width > 0 && height > 0 {
			return width, height
		}
	}
	
	// Default fallback values if detection fails
	return 80, 24
}

// formatNumber formats numbers with comma separators for better readability
func formatNumber(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	
	str := strconv.Itoa(n)
	var result strings.Builder
	
	for i, digit := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result.WriteString(",")
		}
		result.WriteRune(digit)
	}
	
	return result.String()
}

// formatTimeRemaining formats duration in human-friendly format
func formatTimeRemaining(resetTime time.Time) string {
	if resetTime.IsZero() {
		return "?"
	}
	
	remaining := time.Until(resetTime)
	if remaining <= 0 {
		return "now"
	}
	
	hours := int(remaining.Hours())
	minutes := int(remaining.Minutes()) % 60
	seconds := int(remaining.Seconds()) % 60
	
	if hours > 0 {
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	} else {
		return fmt.Sprintf("%ds", seconds)
	}
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Progress represents a progress indicator
type Progress struct {
	message           string
	spinChars         []string              // Modern emoji spinners
	current           int
	stopChan          chan struct{}
	ticker            *time.Ticker
	requestRatePerSec int // Requests per second
	rateUpdateChan    chan int
	minInterval       time.Duration         // Minimum interval for the spinner (fastest speed)
	maxInterval       time.Duration         // Maximum interval for the spinner (slowest speed)
	items             map[string]itemStatus // Status of each item (repositories, discussions, issues, pull-requests)
	currentItem       string                // Currently processing item
	console           *Console              // Console manager for synchronized output
	mutex             sync.Mutex            // Mutex to protect updates to Progress fields
	rendering         bool                  // Flag to prevent overlapping renders
	lastRenderTime    time.Time             // For debounced updates
	terminalWidth     int                   // Terminal width for bounds checking
	terminalHeight    int                   // Terminal height for bounds checking
	savedCursorPos    bool                  // Whether cursor position has been saved
	preserveOnExit    bool                  // Whether to preserve display on signal exit
	signalChan        chan os.Signal        // Channel for signal handling
	signalDone        chan struct{}         // Channel to signal the signal handler to exit
	boxWidth          int                   // Calculated box width for current terminal
	startTime         time.Time             // Track when the process started
}

// itemStatus represents the status of an item being pulled
type itemStatus struct {
	enabled      bool   // Whether the item is enabled for pulling
	completed    bool   // Whether the item has been completed
	failed       bool   // Whether the item has failed
	errorMessage string // Error message if failed
	count        int    // Count of items processed
}

// NewProgress creates a new progress indicator
func NewProgress(message string) *Progress {
	console := NewConsole(200) // Set to 200ms minimum interval for debounced updates

	// Detect terminal size
	width, height := getTerminalSize()

	progress := &Progress{
		message:           message,
		spinChars:         []string{"🔄", "🔃", "⚡", "🔁"}, // Modern emoji spinners
		stopChan:          make(chan struct{}),
		ticker:            time.NewTicker(750 * time.Millisecond), // Faster refresh rate for better responsiveness
		requestRatePerSec: 0,
		rateUpdateChan:    make(chan int, 5),           // Reduced buffer size
		minInterval:       200 * time.Millisecond,      // Slower minimum interval
		maxInterval:       750 * time.Millisecond,      // Faster maximum interval for better responsiveness
		items:             make(map[string]itemStatus),
		currentItem:       "",
		console:           console,
		lastRenderTime:    time.Time{},                 // Initialize to zero time
		terminalWidth:     width,
		terminalHeight:    height,
		savedCursorPos:    false,
		preserveOnExit:    true,                        // Default to preserving display on exit
		signalChan:        make(chan os.Signal, 1),     // Channel for signal handling
		signalDone:        make(chan struct{}),         // Channel to signal the signal handler to exit
		boxWidth:          max(64, width-8),            // Minimum 64 chars, scale with terminal, more margin for safety
		startTime:         time.Now(),                  // Track start time
	}

	// Set the reference to the Progress in the Console
	console.SetProgressRef(progress)
	console.Start()

	// Set up signal handling for graceful exit with preserved display and window resize
	signal.Notify(progress.signalChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)
	go progress.handleSignals()

	// Save current cursor position and hide cursor, then reserve space for display
	fmt.Print("\033[s\033[?25l") // Save cursor position and hide cursor
	
	// Reserve 17 lines for our modern boxed display (with 5 log lines)
	for i := 0; i < 17; i++ {
		fmt.Println()
	}
	
	// Move back to the start of our reserved area
	fmt.Print("\033[17A")
	progress.savedCursorPos = true

	return progress
}

// handleSignals handles OS signals for graceful shutdown with preserved display and window resize
func (p *Progress) handleSignals() {
	for {
		select {
		case sig := <-p.signalChan:
			switch sig {
			case syscall.SIGINT, syscall.SIGTERM:
				// Preserve display on signal exit
				p.preserveOnExit = true
				p.StopWithPreserve()
				os.Exit(0)
			case syscall.SIGWINCH:
				// Handle terminal resize
				p.handleTerminalResize()
			}
		case <-p.signalDone:
			return
		}
	}
}

// handleTerminalResize handles terminal window resize events
func (p *Progress) handleTerminalResize() {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	
	// Get new terminal size
	width, height := getTerminalSize()
	
	// Update terminal dimensions
	p.terminalWidth = width
	p.terminalHeight = height
	
	// Update box width with same calculation as initialization
	p.boxWidth = max(64, width-8)
	
	// Trigger immediate re-render
	p.console.RequestUpdate()
}

// InitItems initializes the items to be displayed
func (p *Progress) InitItems(config *Config) {
	p.mutex.Lock()

	// Check if specific items are requested
	enabledItems := make(map[string]bool)

	// If no specific items provided, enable all
	if len(config.Items) == 0 {
		enabledItems["repositories"] = true
		enabledItems["discussions"] = true
		enabledItems["issues"] = true
		enabledItems["pull-requests"] = true
	} else {
		// Otherwise, only enable the requested items
		for _, item := range config.Items {
			enabledItems[item] = true
		}
	}

	// Initialize item statuses
	p.items["repositories"] = itemStatus{enabled: enabledItems["repositories"], completed: false, failed: false, errorMessage: "", count: 0}
	p.items["discussions"] = itemStatus{enabled: enabledItems["discussions"], completed: false, failed: false, errorMessage: "", count: 0}
	p.items["issues"] = itemStatus{enabled: enabledItems["issues"], completed: false, failed: false, errorMessage: "", count: 0}
	p.items["pull-requests"] = itemStatus{enabled: enabledItems["pull-requests"], completed: false, failed: false, errorMessage: "", count: 0}

	p.mutex.Unlock()

	// Display initial status immediately to establish stable layout
	p.renderStatus()
	
	// Then request throttled updates for future changes
	p.console.RequestUpdate()
}

// SetCurrentItem sets the currently processing item
func (p *Progress) SetCurrentItem(item string) {
	p.mutex.Lock()
	p.currentItem = item
	p.mutex.Unlock()

	p.console.RequestUpdate()
}

// MarkItemCompleted marks an item as completed with final count
func (p *Progress) MarkItemCompleted(item string, count int) {
	p.mutex.Lock()
	if status, exists := p.items[item]; exists {
		status.completed = true
		status.failed = false
		status.errorMessage = ""
		status.count = count
		p.items[item] = status
	}
	// Only clear currentItem if this item was the current one
	if p.currentItem == item {
		p.currentItem = ""
	}
	p.mutex.Unlock()

	p.console.RequestUpdate()
}

// UpdateItemCount updates the count for the current item with reduced update frequency
func (p *Progress) UpdateItemCount(item string, count int) {
	p.mutex.Lock()
	if status, exists := p.items[item]; exists {
		// Only update if count has changed significantly to reduce console spam
		if count != status.count && (count%10 == 0 || count < 10 || count-status.count > 5) {
			status.count = count
			p.items[item] = status
			p.mutex.Unlock()
			// Use throttled console update instead of immediate render to prevent duplicated output
			p.console.RequestUpdate()
		} else {
			// Just update the count without triggering render
			status.count = count
			p.items[item] = status
			p.mutex.Unlock()
		}
	} else {
		p.mutex.Unlock()
	}
}

// MarkItemFailed marks an item as failed with an error message
func (p *Progress) MarkItemFailed(item string, errorMessage string) {
	p.mutex.Lock()
	if status, exists := p.items[item]; exists {
		status.completed = false
		status.failed = true
		status.errorMessage = errorMessage
		p.items[item] = status
	}
	p.currentItem = ""
	p.mutex.Unlock()

	p.console.RequestUpdate()
}

// HasAnyFailed returns true if any item has failed
func (p *Progress) HasAnyFailed() bool {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	
	for _, status := range p.items {
		if status.failed {
			return true
		}
	}
	return false
}

// capitalize first letter of a string
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	// Handle special cases for display names
	switch s {
	case "pull-requests":
		return "Pull Requests"
	default:
		return strings.ToUpper(s[:1]) + s[1:]
	}
}

// renderStatus renders the modern boxed console interface
func (p *Progress) renderStatus() {
	// Implement debounced updates with minimum 200ms interval
	now := time.Now()
	if !p.lastRenderTime.IsZero() && now.Sub(p.lastRenderTime) < 200*time.Millisecond {
		return // Skip update, too soon since last render
	}

	// Ensure thread-safe access to Progress fields and prevent overlapping renders
	p.mutex.Lock()
	if p.rendering {
		p.mutex.Unlock()
		return // Skip if already rendering
	}
	p.rendering = true
	defer func() {
		p.rendering = false
		p.lastRenderTime = now
		p.mutex.Unlock()
	}()

	// Initialize box width if not set
	if p.boxWidth == 0 {
		width, height := getTerminalSize()
		p.terminalWidth = width
		p.terminalHeight = height
		p.boxWidth = max(64, width-8) // Minimum 64 chars, scale with terminal, more margin for safety
	}
	
	// Ensure we have enough terminal height for our display
	if p.terminalHeight < 17 {
		return // Terminal too small, skip rendering
	}

	// Don't move cursor - we should already be positioned at the start of our area
	
	// Build complete output in memory for atomic rendering
	var output strings.Builder
	output.Grow(4096) // Pre-allocate larger buffer for box drawing

	// Modern color scheme
	const (
		boxColor      = "\033[96m"  // Bright cyan for borders
		headerColor   = "\033[1;97m" // Bold white for headers  
		greenColor    = "\033[32m"   // Green for completed
		blueColor     = "\033[34m"   // Blue for active
		redColor      = "\033[31m"   // Red for errors
		grayColor     = "\033[90m"   // Gray for skipped
		resetColor    = "\033[0m"    // Reset colors
	)

	// Always render the complete 17-line box structure  
	// Line 1: Top border
	p.renderBoxTop(&output, boxColor, resetColor)
	// Line 2: Empty line
	p.renderEmptyLine(&output, boxColor, resetColor)
	// Lines 3-7: Items section (5 lines)
	p.renderItemsSection(&output, boxColor, resetColor, greenColor, blueColor, redColor, grayColor)
	// Line 8: Empty line
	p.renderEmptyLine(&output, boxColor, resetColor)
	// Line 9: API Status
	p.renderAPIStatusSection(&output, boxColor, headerColor, resetColor, greenColor, redColor)
	// Line 10: Rate Limit
	p.renderRateLimitSection(&output, boxColor, headerColor, resetColor)
	// Line 11: Empty line
	p.renderEmptyLine(&output, boxColor, resetColor)
	// Lines 12-17: Activity section (1 header + 5 log lines = 6 lines)
	p.renderActivitySection(&output, boxColor, headerColor, resetColor, redColor)
	// Line 17: Bottom border
	p.renderBoxBottom(&output, boxColor, resetColor)
	
	// Atomic rendering: write complete output in single operation
	fmt.Print(output.String())
	
	// Move cursor back to start of display area for next update
	fmt.Print("\033[17A")
}

// renderBoxTop renders the top border with title
func (p *Progress) renderBoxTop(output *strings.Builder, boxColor, resetColor string) {
	output.WriteString(boxColor)
	output.WriteString("┌─ GitHub 🧠 pull ")
	
	// Fill remaining space with dashes
	titleLen := 17 // "GitHub 🧠 pull " length (emoji counts as 1 but displays as 2)
	remainingDashes := p.boxWidth - titleLen - 2 // -2 for ┌ and ┐
	for i := 0; i < remainingDashes; i++ {
		output.WriteString("─")
	}
	output.WriteString("┐")
	output.WriteString(resetColor)
	output.WriteString("\033[K\n")
}

// renderBoxBottom renders the bottom border
func (p *Progress) renderBoxBottom(output *strings.Builder, boxColor, resetColor string) {
	output.WriteString(boxColor)
	output.WriteString("└")
	for i := 0; i < p.boxWidth-2; i++ {
		output.WriteString("─")
	}
	output.WriteString("┘")
	output.WriteString(resetColor)
	output.WriteString("\033[K\n")
}

// renderEmptyLine renders an empty line with borders
func (p *Progress) renderEmptyLine(output *strings.Builder, boxColor, resetColor string) {
	output.WriteString(boxColor)
	output.WriteString("│")
	output.WriteString(resetColor)
	for i := 0; i < p.boxWidth-2; i++ {
		output.WriteString(" ")
	}
	output.WriteString(boxColor)
	output.WriteString("│")
	output.WriteString(resetColor)
	output.WriteString("\033[K\n")
}

// visibleLength calculates the visible length of a string, ignoring ANSI escape codes
func visibleLength(s string) int {
	length := 0
	inEscape := false
	
	for _, r := range s {
		if r == '\033' { // Start of ANSI escape sequence
			inEscape = true
		} else if inEscape && r == 'm' { // End of ANSI escape sequence
			inEscape = false
		} else if !inEscape {
			length++
		}
	}
	
	return length
}

// renderItemsSection renders the items status section
func (p *Progress) renderItemsSection(output *strings.Builder, boxColor, resetColor, greenColor, blueColor, redColor, grayColor string) {
	itemOrder := []string{"repositories", "discussions", "issues", "pull-requests"}
	
	for _, item := range itemOrder {
		// Build the line content first
		var lineContent strings.Builder
		
		status, exists := p.items[item]
		
		if !exists || !status.enabled {
			// Skipped items
			lineContent.WriteString(grayColor)
			lineContent.WriteString("🔕 ")
			lineContent.WriteString(capitalize(item))
			lineContent.WriteString(resetColor)
		} else if status.failed {
			// Failed items
			lineContent.WriteString(redColor)
			lineContent.WriteString("❌ ")
			lineContent.WriteString(capitalize(item))
			if status.count > 0 {
				lineContent.WriteString(": ")
				lineContent.WriteString(formatNumber(status.count))
				// Add error indicator if there are errors
				lineContent.WriteString(" (errors)")
			}
			lineContent.WriteString(resetColor)
		} else if status.completed {
			// Completed items
			lineContent.WriteString(greenColor)
			lineContent.WriteString("✅ ")
			lineContent.WriteString(capitalize(item))
			lineContent.WriteString(": ")
			lineContent.WriteString(formatNumber(status.count))
			lineContent.WriteString(resetColor)
		} else if p.currentItem == item {
			// Active item with animated spinner
			lineContent.WriteString(blueColor)
			spinner := p.spinChars[p.current%len(p.spinChars)]
			lineContent.WriteString(spinner)
			lineContent.WriteString(" ")
			lineContent.WriteString(capitalize(item))
			if status.count > 0 {
				lineContent.WriteString(": ")
				lineContent.WriteString(formatNumber(status.count))
			}
			lineContent.WriteString(resetColor)
		} else {
			// Pending items
			lineContent.WriteString("⚪ ")
			lineContent.WriteString(capitalize(item))
		}
		
		// Calculate padding based on actual visible length
		lineContentStr := lineContent.String()
		contentLength := visibleLength(lineContentStr)
		// Account for: "│  " (3) + content + " │" (2)
		usedLength := 3 + contentLength + 2
		padding := p.boxWidth - usedLength
		if padding < 0 {
			padding = 0
		}
		
		// Write the complete line
		output.WriteString(boxColor)
		output.WriteString("│  ")
		output.WriteString(resetColor)
		output.WriteString(lineContentStr)
		
		for i := 0; i < padding; i++ {
			output.WriteString(" ")
		}
		
		output.WriteString(boxColor)
		output.WriteString("│")
		output.WriteString(resetColor)
		output.WriteString("\033[K\n")
	}
}

// renderAPIStatusSection renders the API status section
func (p *Progress) renderAPIStatusSection(output *strings.Builder, boxColor, headerColor, resetColor, greenColor, redColor string) {
	// Build the line content first (same as other sections)
	var lineContent strings.Builder
	
	lineContent.WriteString(headerColor)
	lineContent.WriteString("📊 API Status    ")
	lineContent.WriteString(resetColor)
	
	statusCounters := getStatusCounters()
	
	// Success status
	lineContent.WriteString(greenColor)
	lineContent.WriteString("✅ ")
	lineContent.WriteString(formatNumber(statusCounters.Success2XX))
	lineContent.WriteString(resetColor)
	lineContent.WriteString("   ")
	
	// Warning status  
	lineContent.WriteString("⚠️ ")
	lineContent.WriteString(formatNumber(statusCounters.Error4XX))
	lineContent.WriteString("   ")
	
	// Error status
	lineContent.WriteString(redColor)
	lineContent.WriteString("❌ ")
	lineContent.WriteString(formatNumber(statusCounters.Error5XX))
	lineContent.WriteString(resetColor)
	
	// Calculate padding based on actual visible length (same as other sections)
	lineContentStr := lineContent.String()
	contentLength := visibleLength(lineContentStr)
	// Account for: "│  " (3) + content + " │" (2)
	usedLength := 3 + contentLength + 2
	padding := p.boxWidth - usedLength - 1 // Subtract 1 to fix alignment
	if padding < 0 {
		padding = 0
	}
	
	// Write the complete line
	output.WriteString(boxColor)
	output.WriteString("│  ")
	output.WriteString(resetColor)
	output.WriteString(lineContentStr)
	
	for i := 0; i < padding; i++ {
		output.WriteString(" ")
	}
	
	output.WriteString(boxColor)
	output.WriteString("│")
	output.WriteString(resetColor)
	output.WriteString("\033[K\n")
}

// renderRateLimitSection renders the rate limit section
func (p *Progress) renderRateLimitSection(output *strings.Builder, boxColor, headerColor, resetColor string) {
	// Build the line content first
	var lineContent strings.Builder
	
	lineContent.WriteString(headerColor)
	lineContent.WriteString("🚀 Rate Limit    ")
	lineContent.WriteString(resetColor)
	
	rateLimitInfo := getRateLimitInfo()
	
	var rateLimitText string
	if rateLimitInfo.Used >= 0 && rateLimitInfo.Limit > 0 {
		rateLimitText = fmt.Sprintf("%s/%s used, resets in %s",
			formatNumber(rateLimitInfo.Used),
			formatNumber(rateLimitInfo.Limit),
			formatTimeRemaining(rateLimitInfo.Reset))
	} else {
		rateLimitText = "? / ? used, resets ?"
	}
	
	lineContent.WriteString(rateLimitText)
	
	// Calculate padding based on actual visible length
	lineContentStr := lineContent.String()
	contentLength := visibleLength(lineContentStr)
	// Account for: "│  " (3) + content + " │" (2)
	usedLength := 3 + contentLength + 2
	padding := p.boxWidth - usedLength
	if padding < 0 {
		padding = 0
	}
	
	// Write the complete line
	output.WriteString(boxColor)
	output.WriteString("│  ")
	output.WriteString(resetColor)
	output.WriteString(lineContentStr)
	
	for i := 0; i < padding; i++ {
		output.WriteString(" ")
	}
	
	output.WriteString(boxColor)
	output.WriteString("│")
	output.WriteString(resetColor)
	output.WriteString("\033[K\n")
}

// renderActivitySection renders the activity log section (6 lines total: 1 header + 5 logs)
func (p *Progress) renderActivitySection(output *strings.Builder, boxColor, headerColor, resetColor, redColor string) {
	// Line 1: Activity header
	var headerContent strings.Builder
	headerContent.WriteString(headerColor)
	headerContent.WriteString("💬 Activity")
	headerContent.WriteString(resetColor)
	
	// Calculate padding based on actual visible length
	headerContentStr := headerContent.String()
	headerLength := visibleLength(headerContentStr)
	// Account for: "│  " (3) + content + " │" (2)
	usedLength := 3 + headerLength + 2
	padding := p.boxWidth - usedLength
	if padding < 0 {
		padding = 0
	}
	
	// Write the complete header line
	output.WriteString(boxColor)
	output.WriteString("│  ")
	output.WriteString(resetColor)
	output.WriteString(headerContentStr)
	
	for i := 0; i < padding; i++ {
		output.WriteString(" ")
	}
	
	output.WriteString(boxColor)
	output.WriteString("│")
	output.WriteString(resetColor)
	output.WriteString("\033[K\n")
	
	// Lines 2-6: Log entries (exactly 5 lines)
	logEntries := p.console.GetLogEntries()
	const maxLogLines = 5
	
	for i := 0; i < maxLogLines; i++ {
		// Build the log line content first
		var logContent strings.Builder
		
		if i < len(logEntries) {
			entry := logEntries[i]
			
			// Format timestamp as HH:MM:SS
			timestamp := entry.timestamp.Format("15:04:05")
			logContent.WriteString(timestamp)
			logContent.WriteString(" ")
			
			// Calculate available space for message (account for "│     " + timestamp + " " + " │")
			usedSpace := 5 + len(timestamp) + 1 + 2 // prefix + timestamp + space + border
			availableSpace := p.boxWidth - usedSpace
			
			// Truncate message to fit
			message := entry.message
			if len(message) > availableSpace {
				if availableSpace > 3 {
					message = message[:availableSpace-3] + "..."
				} else {
					message = message[:availableSpace]
				}
			}
			
			// Color error messages red
			if strings.Contains(entry.message, "Error:") || strings.Contains(entry.message, "❌") {
				logContent.WriteString(redColor)
				logContent.WriteString(message)
				logContent.WriteString(resetColor)
			} else {
				logContent.WriteString(message)
			}
		}
		
		// Calculate padding based on actual visible length
		logContentStr := logContent.String()
		contentLength := visibleLength(logContentStr)
		// Account for: "│     " (5) + content + " │" (2)
		usedLength := 5 + contentLength + 2
		padding := p.boxWidth - usedLength
		if padding < 0 {
			padding = 0
		}
		
		// Write the complete log line
		output.WriteString(boxColor)
		output.WriteString("│     ")
		output.WriteString(resetColor)
		output.WriteString(logContentStr)
		
		for j := 0; j < padding; j++ {
			output.WriteString(" ")
		}
		
		output.WriteString(boxColor)
		output.WriteString("│")
		output.WriteString(resetColor)
		output.WriteString("\033[K\n")
	}
}

// Start starts the progress indicator
func (p *Progress) Start() {
	go func() {
		for {
			select {
			case <-p.stopChan:
				return
			case rate := <-p.rateUpdateChan:
				p.mutex.Lock()
				p.requestRatePerSec = rate
				// Adjust ticker speed based on rate with more conservative intervals:
				// 0 req/s -> maxInterval (very slow)
				// 20+ req/s -> minInterval (moderate speed)
				var interval time.Duration
				if rate >= 20 {
					interval = p.minInterval
				} else if rate <= 0 {
					interval = p.maxInterval
				} else {
					// Linear mapping between min and max intervals
					intervalRange := float64(p.maxInterval - p.minInterval)
					ratio := float64(20-rate) / 20.0 // 0 for rate=20, 1 for rate=0
					interval = p.minInterval + time.Duration(ratio*intervalRange)
				}

				// Update ticker with new interval
				p.ticker.Stop()
				p.ticker = time.NewTicker(interval)
				p.mutex.Unlock()

				// Update display with new rate - but throttled
				p.console.RequestUpdate()
			case <-p.ticker.C:
				p.mutex.Lock()
				p.current = (p.current + 1) % len(p.spinChars)
				p.mutex.Unlock()

				// Request a render to update the spinner - but only if there's an active item
				if p.currentItem != "" {
					p.console.RequestUpdate()
				}
			}
		}
	}()
}

// UpdateRequestRate updates the request rate for dynamic spinner speed
func (p *Progress) UpdateRequestRate(requestsPerSecond int) {
	select {
	case p.rateUpdateChan <- requestsPerSecond:
		// Rate update sent
	default:
		// Channel buffer is full, discard this update
	}
}

// StopWithPreserve stops the progress indicator and preserves the console display
func (p *Progress) StopWithPreserve() {
	// Stop signal handling to clean up the goroutine
	signal.Stop(p.signalChan)
	close(p.signalDone)
	
	// Stop the ticker and console update loop first
	if p.ticker != nil {
		p.ticker.Stop()
	}
	
	// Signal stop to the goroutine
	select {
	case p.stopChan <- struct{}{}:
	default:
		// Channel might be closed already
	}
	
	// Stop the console
	p.console.Stop()

	// Show final status one more time
	p.renderStatus()
	
	// Leave the display visible and position cursor at the end
	if p.savedCursorPos {
		fmt.Print("\033[17B") // Move down to end of display area
	}
	
	// Show cursor again but keep display intact
	fmt.Print("\033[?25h") // Show cursor
	fmt.Println()          // Add a newline after the final status
}

// Stop stops the progress indicator
func (p *Progress) Stop() {
	// Check if we should preserve display on exit
	if p.preserveOnExit {
		p.StopWithPreserve()
		return
	}
	
	// Stop signal handling to clean up the goroutine
	signal.Stop(p.signalChan)
	close(p.signalDone)
	
	// Stop the ticker and console update loop first
	if p.ticker != nil {
		p.ticker.Stop()
	}
	
	// Signal stop to the goroutine
	select {
	case p.stopChan <- struct{}{}:
	default:
		// Channel might be closed already
	}
	
	// Stop the console
	p.console.Stop()

	// Show final status one more time
	p.renderStatus()
	
	// Move cursor to the end of our display area and restore original position
	if p.savedCursorPos {
		fmt.Print("\033[17B") // Move down to end of display area
		fmt.Print("\033[u")   // Restore to original cursor position (before our display)
		fmt.Print("\033[17B") // Move down past our display area
	}
	
	// Show cursor again and ensure proper terminal state
	fmt.Print("\033[?25h") // Show cursor
	fmt.Println()          // Add a newline after the final status
	
	// Add a small delay to let user see the final status
	time.Sleep(200 * time.Millisecond)
}

// Log adds a log message through the console
func (p *Progress) Log(format string, args ...interface{}) {
	p.console.Log(format, args...)
}

// UpdateMessage updates the progress message
func (p *Progress) UpdateMessage(message string) {
	p.mutex.Lock()
	p.message = message
	p.mutex.Unlock()

	// Request a throttled update rather than immediately updating
	p.console.RequestUpdate()
}

// DB represents the database connection
type DB struct {
	db *sql.DB
}

// QueryRow is a wrapper for sql.DB.QueryRow
func (d *DB) QueryRow(query string, args ...interface{}) *sql.Row {
	return d.db.QueryRow(query, args...)
}

// Query is a wrapper for sql.DB.Query
func (d *DB) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return d.db.Query(query, args...)
}

// Exec is a wrapper for sql.DB.Exec
func (d *DB) Exec(query string, args ...interface{}) (sql.Result, error) {
	return d.db.Exec(query, args...)
}

// Close is a wrapper for sql.DB.Close
func (d *DB) Close() error {
	return d.db.Close()
}

// getTableName returns the versioned table name for a given table type
func getTableName(tableType string) string {
	switch tableType {
	case "repositories":
		return fmt.Sprintf("repositories_v%d", REPOSITORIES_VERSION)
	case "discussions":
		return fmt.Sprintf("discussions_v%d", DISCUSSIONS_VERSION)
	case "issues":
		return fmt.Sprintf("issues_v%d", ISSUES_VERSION)
	case "pull_requests":
		return fmt.Sprintf("pull_requests_v%d", PULL_REQUESTS_VERSION)
	case "search":
		return fmt.Sprintf("search_v%d", SEARCH_VERSION)
	default:
		panic(fmt.Sprintf("Unknown table type: %s", tableType))
	}
}

// getAllTableNames returns all current versioned table names
func getAllTableNames() map[string]string {
	return map[string]string{
		"repositories":   getTableName("repositories"),
		"discussions":    getTableName("discussions"),
		"issues":         getTableName("issues"),
		"pull_requests":  getTableName("pull_requests"),
		"search":         getTableName("search"),
	}
}

// Repository represents a GitHub repository
type Repository struct {
	Name                    string    `json:"name"`                      // Repository name without organization prefix
	UpdatedAt               time.Time `json:"updated_at"`                // Last update timestamp
	HasIssuesEnabled        bool      `json:"has_issues_enabled"`        // Whether issues are enabled for this repository
	HasDiscussionsEnabled   bool      `json:"has_discussions_enabled"`   // Whether discussions are enabled for this repository
}

// Discussion represents a GitHub discussion
type Discussion struct {
	URL        string    `json:"url"`        // Primary key
	Title      string    `json:"title"`      // Discussion title
	Body       string    `json:"body"`       // Discussion content
	CreatedAt  time.Time `json:"created_at"` // Creation timestamp
	UpdatedAt  time.Time `json:"updated_at"` // Last update timestamp
	Repository string    `json:"repository"` // Repository name without organization prefix
	Author     string    `json:"author"`     // Username
}

// Issue represents a GitHub issue
type Issue struct {
	URL        string     `json:"url"`        // Primary key
	Title      string     `json:"title"`      // Issue title
	Body       string     `json:"body"`       // Issue content
	CreatedAt  time.Time  `json:"created_at"` // Creation timestamp
	UpdatedAt  time.Time  `json:"updated_at"` // Last update timestamp
	ClosedAt   *time.Time `json:"closed_at"`  // Close timestamp (null if open)
	Repository string     `json:"repository"` // Repository name without organization prefix
	Author     string     `json:"author"`     // Username
}

// PullRequest represents a GitHub pull request
type PullRequest struct {
	URL        string     `json:"url"`        // Primary key
	Title      string     `json:"title"`      // Pull request title
	Body       string     `json:"body"`       // Pull request content
	CreatedAt  time.Time  `json:"created_at"` // Creation timestamp
	UpdatedAt  time.Time  `json:"updated_at"` // Last update timestamp
	MergedAt   *time.Time `json:"merged_at"`  // Merge timestamp (null if not merged)
	ClosedAt   *time.Time `json:"closed_at"`  // Close timestamp (null if open)
	Repository string     `json:"repository"` // Repository name without organization prefix
	Author     string     `json:"author"`     // Username
}

// MCPRequest represents an MCP request
type MCPRequest struct {
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
}

// MCPResponse represents an MCP response
type MCPResponse struct {
	Result interface{} `json:"result"`
	Error  string      `json:"error,omitempty"`
}

// ListDiscussionsParams represents parameters for list_discussions
type ListDiscussionsParams struct {
	Repository  string   `json:"repository"`
	CreatedFrom string   `json:"created_from,omitempty"`
	CreatedTo   string   `json:"created_to,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Fields      []string `json:"fields,omitempty"`
}

// ListIssuesParams represents parameters for list_issues
type ListIssuesParams struct {
	Repository  string   `json:"repository"`
	CreatedFrom string   `json:"created_from,omitempty"`
	CreatedTo   string   `json:"created_to,omitempty"`
	ClosedFrom  string   `json:"closed_from,omitempty"`
	ClosedTo    string   `json:"closed_to,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Fields      []string `json:"fields,omitempty"`
}

// ListPullRequestsParams represents parameters for list_pull_requests
type ListPullRequestsParams struct {
	Repository  string   `json:"repository"`
	CreatedFrom string   `json:"created_from,omitempty"`
	CreatedTo   string   `json:"created_to,omitempty"`
	ClosedFrom  string   `json:"closed_from,omitempty"`
	ClosedTo    string   `json:"closed_to,omitempty"`
	MergedFrom  string   `json:"merged_from,omitempty"`
	MergedTo    string   `json:"merged_to,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Fields      []string `json:"fields,omitempty"`
}

// InitDB initializes the database
// getDBPath returns the database path for a specific organization
func getDBPath(dbDir, organization string) string {
	if dbDir == "" {
		dbDir = "./db"
	}
	return fmt.Sprintf("%s/%s.db", dbDir, organization)
}

// cleanupOldTables drops all tables that don't match current version suffixes
func cleanupOldTables(db *DB, progress *Progress) error {
	// Get list of all tables in the database (including virtual tables)
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE (type='table' OR type='virtual table') AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return fmt.Errorf("failed to query existing tables: %w", err)
	}
	defer rows.Close()

	var existingTables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return fmt.Errorf("failed to scan table name: %w", err)
		}
		existingTables = append(existingTables, tableName)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading table names: %w", err)
	}

	// Get map of current expected table names
	expectedTables := getAllTableNames()
	
	// Check each existing table and drop if it doesn't match current versions
	droppedTables := 0
	for _, tableName := range existingTables {
		// Skip lock table as it's not versioned
		if tableName == "lock" {
			continue
		}
		
		// Check if this table matches any current expected table name
		isCurrentVersion := false
		for _, expectedName := range expectedTables {
			if tableName == expectedName {
				isCurrentVersion = true
				break
			}
		}
		
		// If it doesn't match current version, drop it
		if !isCurrentVersion {
			// Check if it's a versioned table we care about
			isVersionedTable := false
			for tableType := range expectedTables {
				if strings.HasPrefix(tableName, tableType+"_v") {
					isVersionedTable = true
					break
				}
			}
			
			if isVersionedTable {
				if progress != nil {
					progress.Log("Dropping old table version: %s", tableName)
				} else {
					slog.Info("Dropping old table version", "table", tableName)
				}
				
				// Use special handling for FTS5 virtual tables
				if strings.HasPrefix(tableName, "search_v") {
					if _, err := dropFTS5Table(db, tableName); err != nil {
						return fmt.Errorf("failed to drop FTS5 table %s: %w", tableName, err)
					}
					// Note: Ideally we'd reopen the DB here, but we're in InitDB so we continue
				} else {
					if _, err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS [%s]", tableName)); err != nil {
						return fmt.Errorf("failed to drop old table %s: %w", tableName, err)
					}
				}
				droppedTables++
			}
		}
	}
	
	if droppedTables > 0 {
		if progress != nil {
			progress.Log("Cleaned up %d old table version(s)", droppedTables)
		} else {
			slog.Info("Cleaned up old table versions", "count", droppedTables)
		}
	}
	
	return nil
}

// dropFTS5Table handles dropping FTS5 virtual tables using the workaround from:
// https://stackoverflow.com/questions/40877078/drop-a-table-originally-created-with-unknown-tokenizer
// Returns true if database needs to be reopened (as recommended in the article)
func dropFTS5Table(db *DB, tableName string) (bool, error) {
	// First try normal DROP TABLE
	if _, err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS [%s]", tableName)); err == nil {
		return false, nil // Normal drop worked, no reopen needed
	}
	
	// If normal drop fails, use the workaround for corrupted FTS5 tables
	// Remove all FTS5-related entries from sqlite_master (with IF EXISTS safety)
	ftsTableQueries := []string{
		fmt.Sprintf("DELETE FROM sqlite_master WHERE type='table' AND name='%s' AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='%s')", tableName, tableName),
		fmt.Sprintf("DELETE FROM sqlite_master WHERE type='table' AND name='%s_data' AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='%s_data')", tableName, tableName),
		fmt.Sprintf("DELETE FROM sqlite_master WHERE type='table' AND name='%s_idx' AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='%s_idx')", tableName, tableName),
		fmt.Sprintf("DELETE FROM sqlite_master WHERE type='table' AND name='%s_content' AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='%s_content')", tableName, tableName),
		fmt.Sprintf("DELETE FROM sqlite_master WHERE type='table' AND name='%s_docsize' AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='%s_docsize')", tableName, tableName),
		fmt.Sprintf("DELETE FROM sqlite_master WHERE type='table' AND name='%s_config' AND EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='%s_config')", tableName, tableName),
	}
	
	for _, query := range ftsTableQueries {
		_, _ = db.Exec(query) // Ignore errors as some tables might not exist
	}
	
	// Force WAL checkpoint to clear cached state
	_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	
	// Return true to indicate that database should be reopened as per the article
	return true, nil
}

func InitDB(dbDir, organization string, progress *Progress) (*DB, error) {
	dbPath := getDBPath(dbDir, organization)
	
	// Log the full database file path being opened
	if progress != nil {
		progress.Log("Opening database at path: %s", dbPath)
	} else {
		slog.Info("Opening database at path", "path", dbPath)
	}
	
	// Extract directory from dbPath
	lastSlash := strings.LastIndex(dbPath, "/")
	if lastSlash != -1 {
		dir := dbPath[:lastSlash]
		// Create db directory if it doesn't exist
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if err := os.MkdirAll(dir, 0755); err != nil {
				if progress != nil {
					progress.Log("Error creating database directory %s: %v", dir, err)
				} else {
					slog.Error("Failed to create database directory", "dir", dir, "error", err)
				}
				return nil, fmt.Errorf("failed to create database directory %s: %w", dir, err)
			}
		}
	}

	// Open database connection with SQLite parameters for better concurrency
	dbURL := fmt.Sprintf("%s?_timeout=30000&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=30000", dbPath)
	db, err := sql.Open("sqlite3", dbURL)
	if err != nil {
		if progress != nil {
			progress.Log("Error opening database at %s: %v", dbPath, err)
		} else {
			slog.Error("Failed to open database", "path", dbPath, "error", err)
		}
		return nil, fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}

	// Configure connection pool settings
	db.SetMaxOpenConns(1)    // SQLite works best with single connection
	db.SetMaxIdleConns(1)    // Keep one idle connection
	db.SetConnMaxLifetime(0) // Connections never expire
	
	// Enable WAL mode and set other SQLite pragmas for better performance and concurrency
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL", 
		"PRAGMA cache_size=10000",
		"PRAGMA temp_store=memory",
		"PRAGMA mmap_size=268435456", // 256MB
		"PRAGMA busy_timeout=30000",
	}
	
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			if progress != nil {
				progress.Log("Warning: Failed to set %s: %v", pragma, err)
			} else {
				slog.Warn("Failed to set pragma", "pragma", pragma, "error", err)
			}
		}
	}

	// Cleanup tables with incorrect version suffixes
	if err := cleanupOldTables(&DB{db: db}, progress); err != nil {
		return nil, fmt.Errorf("failed to cleanup old tables: %w", err)
	}

	// Create repositories table with version suffix
	repositoriesTable := getTableName("repositories")
	_, err = db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			name TEXT PRIMARY KEY,
			has_discussions_enabled BOOLEAN DEFAULT 0,
			has_issues_enabled BOOLEAN DEFAULT 0,
			updated_at DATETIME
		)
	`, repositoriesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create repositories table: %w", err)
	}

	// Create performance index for repositories table
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_updated_at ON %s (updated_at)", 
		repositoriesTable, repositoriesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create updated_at index on repositories table: %w", err)
	}

	// Create discussions table with version suffix
	discussionsTable := getTableName("discussions")
	_, err = db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			url TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			repository TEXT NOT NULL,
			author TEXT NOT NULL
		)
	`, discussionsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create discussions table: %w", err)
	}

	// Create indexes for discussions table
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_repository ON %s (repository)", 
		discussionsTable, discussionsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create repository index on discussions table: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_author ON %s (author)", 
		discussionsTable, discussionsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create author index on discussions table: %w", err)
	}

	// Create performance indexes for discussions table
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_created_at ON %s (created_at)", 
		discussionsTable, discussionsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create created_at index on discussions table: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_updated_at ON %s (updated_at)", 
		discussionsTable, discussionsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create updated_at index on discussions table: %w", err)
	}

	// Composite index for common query patterns: repository + created_at
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_repo_created ON %s (repository, created_at)", 
		discussionsTable, discussionsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create repository+created_at index on discussions table: %w", err)
	}

	// Create issues table with version suffix
	issuesTable := getTableName("issues")
	_, err = db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			url TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			closed_at DATETIME,
			repository TEXT NOT NULL,
			author TEXT NOT NULL
		)
	`, issuesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create issues table: %w", err)
	}

	// Create indexes for issues table
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_repository ON %s (repository)", 
		issuesTable, issuesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create repository index on issues table: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_author ON %s (author)", 
		issuesTable, issuesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create author index on issues table: %w", err)
	}

	// Create performance indexes for issues table
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_created_at ON %s (created_at)", 
		issuesTable, issuesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create created_at index on issues table: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_updated_at ON %s (updated_at)", 
		issuesTable, issuesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create updated_at index on issues table: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_closed_at ON %s (closed_at)", 
		issuesTable, issuesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create closed_at index on issues table: %w", err)
	}

	// Composite index for common query patterns: repository + created_at
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_repo_created ON %s (repository, created_at)", 
		issuesTable, issuesTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create repository+created_at index on issues table: %w", err)
	}

	// Create pull_requests table with version suffix
	pullRequestsTable := getTableName("pull_requests")
	_, err = db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			url TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			merged_at DATETIME,
			closed_at DATETIME,
			repository TEXT NOT NULL,
			author TEXT NOT NULL
		)
	`, pullRequestsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create pull_requests table: %w", err)
	}

	// Create indexes for pull_requests table
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_repository ON %s (repository)", 
		pullRequestsTable, pullRequestsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create repository index on pull_requests table: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_author ON %s (author)", 
		pullRequestsTable, pullRequestsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create author index on pull_requests table: %w", err)
	}

	// Create performance indexes for pull_requests table
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_created_at ON %s (created_at)", 
		pullRequestsTable, pullRequestsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create created_at index on pull_requests table: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_updated_at ON %s (updated_at)", 
		pullRequestsTable, pullRequestsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create updated_at index on pull_requests table: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_closed_at ON %s (closed_at)", 
		pullRequestsTable, pullRequestsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create closed_at index on pull_requests table: %w", err)
	}

	// Composite index for common query patterns: repository + created_at
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_repo_created ON %s (repository, created_at)", 
		pullRequestsTable, pullRequestsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create repository+created_at index on pull_requests table: %w", err)
	}

	// Create merged_at index (merged_at column already included in table creation)
	_, err = db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_merged_at ON %s (merged_at)", 
		pullRequestsTable, pullRequestsTable))
	if err != nil {
		return nil, fmt.Errorf("failed to create merged_at index on pull_requests table: %w", err)
	}

	// Create lock table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS lock (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			locked INTEGER NOT NULL DEFAULT 0,
			locked_at DATETIME
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create lock table: %w", err)
	}

	// Ensure single row exists
	_, _ = db.Exec(`INSERT OR IGNORE INTO lock (id, locked, locked_at) VALUES (1, 0, NULL)`)

	// Create search table (FTS5) for full-text search (title prioritization handled in queries) with version suffix
	searchTable := getTableName("search")
	_, err = db.Exec(fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS [%s] USING fts5(
			type, title, body, url, repository, author, created_at UNINDEXED, state UNINDEXED
		)
	`, searchTable))
	if err != nil {
		return nil, fmt.Errorf("FTS5 not available in SQLite - rebuild with FTS5 support: %w", err)
	}

	return &DB{db: db}, nil
}

// PopulateSearchTable populates the search FTS table with data from all tables as specified in main.md
func (db *DB) PopulateSearchTable(progress *Progress) error {
	// Truncate search FTS5 table and repopulate it from discussions, issues, and pull_requests tables
	slog.Info("Truncating and repopulating search FTS table...")
	progress.Log("Clearing existing search index...")
	
	// Get versioned table names
	searchTable := getTableName("search")
	discussionsTable := getTableName("discussions")
	issuesTable := getTableName("issues")
	pullRequestsTable := getTableName("pull_requests")
	
	// Try to clear search table data, recreate if corrupted
	if _, err := db.Exec(fmt.Sprintf("DELETE FROM [%s]", searchTable)); err != nil {
		progress.Log("Search table corrupted, dropping and recreating...")
		
		// Force drop the table completely using FTS5 workaround
		if _, dropErr := dropFTS5Table(db, searchTable); dropErr != nil {
			return fmt.Errorf("failed to drop corrupted search table: %w", dropErr)
		}
		
		// Verify table is gone and recreate
		var exists int
		for i := 0; i < 3; i++ { // Try up to 3 times with small delays
			checkErr := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE (type='table' OR type='virtual table') AND name=?", searchTable).Scan(&exists)
			if checkErr != nil {
				return fmt.Errorf("failed to check if search table exists: %w", checkErr)
			}
			if exists == 0 {
				break
			}
			progress.Log("Table still exists, waiting for cleanup...")
			time.Sleep(100 * time.Millisecond)
		}
		
		// Recreate the search table
		if _, createErr := db.Exec(fmt.Sprintf(`
			CREATE VIRTUAL TABLE [%s] USING fts5(
				type, title, body, url, repository, author, created_at UNINDEXED, state UNINDEXED
			)
		`, searchTable)); createErr != nil {
			// If creation still fails, provide more detailed error info
			return fmt.Errorf("failed to recreate search table [%s]: %w", searchTable, createErr)
		}
		
		progress.Log("Successfully recreated search table [%s]", searchTable)
	}
	
	// Get counts for progress reporting
	var discussionCount, issueCount, prCount int
	db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", discussionsTable)).Scan(&discussionCount)
	db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", issuesTable)).Scan(&issueCount)
	db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", pullRequestsTable)).Scan(&prCount)
	
	totalItems := discussionCount + issueCount + prCount
	progress.Log("Indexing %d total items: %d discussions, %d issues, %d pull requests", 
		totalItems, discussionCount, issueCount, prCount)
	
	slog.Info("Indexing content into search table", 
		"discussions", discussionCount, "issues", issueCount, "pull_requests", prCount)
	
	// Insert discussions
	if discussionCount > 0 {
		progress.Log("Indexing %d discussions into search table...", discussionCount)
		slog.Info("Indexing discussions...")
		_, err := db.Exec(fmt.Sprintf(`
			INSERT INTO [%s](type, title, body, url, repository, author, created_at, state)
			SELECT 'discussion', title, body, url, repository, author, created_at, 'open' FROM [%s]
		`, searchTable, discussionsTable))
		if err != nil {
			return fmt.Errorf("failed to populate discussions in search table: %w", err)
		}
		progress.Log("✅ Completed indexing %d discussions", discussionCount)
	} else {
		progress.Log("No discussions to index")
	}

	// Insert issues
	if issueCount > 0 {
		progress.Log("Indexing %d issues into search table...", issueCount)
		slog.Info("Indexing issues...")
		_, err := db.Exec(fmt.Sprintf(`
			INSERT INTO [%s](type, title, body, url, repository, author, created_at, state)
			SELECT 'issue', title, body, url, repository, author, created_at, 
			       CASE WHEN closed_at IS NULL THEN 'open' ELSE 'closed' END 
			FROM [%s]
		`, searchTable, issuesTable))
		if err != nil {
			return fmt.Errorf("failed to populate issues in search table: %w", err)
		}
		progress.Log("✅ Completed indexing %d issues", issueCount)
	} else {
		progress.Log("No issues to index")
	}

	// Insert pull requests
	if prCount > 0 {
		progress.Log("Indexing %d pull requests into search table...", prCount)
		slog.Info("Indexing pull requests...")
		_, err := db.Exec(fmt.Sprintf(`
			INSERT INTO [%s](type, title, body, url, repository, author, created_at, state)
			SELECT 'pull_request', title, body, url, repository, author, created_at, 
			       CASE WHEN closed_at IS NULL THEN 'open' ELSE 'closed' END 
			FROM [%s]
		`, searchTable, pullRequestsTable))
		if err != nil {
			return fmt.Errorf("failed to populate pull_requests in search table: %w", err)
		}
		progress.Log("✅ Completed indexing %d pull requests", prCount)
	} else {
		progress.Log("No pull requests to index")
	}

	progress.Log("🎉 Search index rebuild completed successfully with %d total items", totalItems)
	return nil
}

// dropAndRecreateFTSTables drops and recreates FTS tables to fix corruption
// LockPull sets the lock for pull command. Returns error if already locked.
func (db *DB) LockPull() error {
	// Check for existing lock and its expiration
	row := db.QueryRow("SELECT locked, locked_at FROM lock WHERE id = 1")
	var locked int
	var lockedAt sql.NullTime
	if err := row.Scan(&locked, &lockedAt); err != nil {
		return fmt.Errorf("failed to check lock: %w", err)
	}

	// If locked, check if the lock has expired (older than 5 seconds)
	if locked != 0 && lockedAt.Valid {
		if time.Since(lockedAt.Time) > 5*time.Second {
			// Lock has expired, we can take over (logged for system tracking only)
		} else {
			return fmt.Errorf("another pull command is already running")
		}
	} else if locked != 0 {
		return fmt.Errorf("another pull command is already running")
	}

	// Set the lock
	_, err := db.Exec("UPDATE lock SET locked = 1, locked_at = ? WHERE id = 1", time.Now())
	if err != nil {
		return fmt.Errorf("failed to set lock: %w", err)
	}
	return nil
}

// UnlockPull releases the lock for pull command.
func (db *DB) UnlockPull() error {
	_, err := db.Exec("UPDATE lock SET locked = 0, locked_at = NULL WHERE id = 1")
	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}
	return nil
}

// RenewPullLock renews the lock timestamp to prevent expiration.
func (db *DB) RenewPullLock() error {
	_, err := db.Exec("UPDATE lock SET locked_at = ? WHERE id = 1 AND locked = 1", time.Now())
	if err != nil {
		return fmt.Errorf("failed to renew lock: %w", err)
	}
	return nil
}

// IsPullLocked checks if a pull is running.
func (db *DB) IsPullLocked() (bool, error) {
	row := db.QueryRow("SELECT locked, locked_at FROM lock WHERE id = 1")
	var locked int
	var lockedAt sql.NullTime
	if err := row.Scan(&locked, &lockedAt); err != nil {
		return false, fmt.Errorf("failed to check lock: %w", err)
	}

	// If locked, check if the lock has expired (older than 5 seconds)
	if locked != 0 && lockedAt.Valid {
		if time.Since(lockedAt.Time) > 5*time.Second {
			// Lock has expired
			return false, nil
		}
		return true, nil
	}

	return locked != 0, nil
}

// executeWithRetry executes a database operation with retry logic for database locks
func (db *DB) executeWithRetry(operation func() error, operationName string) error {
	const maxRetries = 5
	const baseDelay = 100 * time.Millisecond
	
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := operation()
		
		if err == nil {
			return nil // Success
		}
		
		// Check if it's a database lock error
		if strings.Contains(err.Error(), "database is locked") {
			if attempt < maxRetries-1 {
				// Exponential backoff with jitter
				delay := baseDelay * time.Duration(1<<attempt)
				jitter := time.Duration(rand.Intn(int(delay/2)))
				time.Sleep(delay + jitter)
				continue
			}
		}
		
		return fmt.Errorf("failed to %s: %w", operationName, err)
	}
	
	return fmt.Errorf("failed to %s after %d attempts: database persistently locked", operationName, maxRetries)
}

// SaveRepository saves a repository to the database with retry logic for database locks
func (db *DB) SaveRepository(repo *Repository) error {
	return db.executeWithRetry(func() error {
		_, err := db.Exec(
			fmt.Sprintf("INSERT OR REPLACE INTO %s (name, updated_at, has_issues_enabled, has_discussions_enabled) VALUES (?, ?, ?, ?)", getTableName("repositories")),
			repo.Name, repo.UpdatedAt.Format(time.RFC3339), repo.HasIssuesEnabled, repo.HasDiscussionsEnabled,
		)
		return err
	}, "save repository")
}

// SaveDiscussion saves a discussion to the database with retry logic for database locks
func (db *DB) SaveDiscussion(discussion *Discussion) error {
	return db.executeWithRetry(func() error {
		_, err := db.Exec(
			fmt.Sprintf("INSERT OR REPLACE INTO %s (url, title, body, created_at, updated_at, repository, author) VALUES (?, ?, ?, ?, ?, ?, ?)", getTableName("discussions")),
			discussion.URL, discussion.Title, discussion.Body, discussion.CreatedAt.Format(time.RFC3339), discussion.UpdatedAt.Format(time.RFC3339), discussion.Repository, discussion.Author,
		)
		return err
	}, "save discussion")
}

// SaveIssue saves an issue to the database with retry logic for database locks
func (db *DB) SaveIssue(issue *Issue) error {
	var closedAtStr interface{}
	if issue.ClosedAt != nil {
		closedAtStr = issue.ClosedAt.Format(time.RFC3339)
	}
	
	return db.executeWithRetry(func() error {
		_, err := db.Exec(
			fmt.Sprintf("INSERT OR REPLACE INTO %s (url, title, body, created_at, updated_at, closed_at, repository, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", getTableName("issues")),
			issue.URL, issue.Title, issue.Body, issue.CreatedAt.Format(time.RFC3339), issue.UpdatedAt.Format(time.RFC3339), closedAtStr, issue.Repository, issue.Author,
		)
		return err
	}, "save issue")
}

// SavePullRequest saves a pull request to the database with retry logic for database locks
func (db *DB) SavePullRequest(pr *PullRequest) error {
	var mergedAtStr interface{}
	if pr.MergedAt != nil {
		mergedAtStr = pr.MergedAt.Format(time.RFC3339)
	}
	
	var closedAtStr interface{}
	if pr.ClosedAt != nil {
		closedAtStr = pr.ClosedAt.Format(time.RFC3339)
	}
	
	return db.executeWithRetry(func() error {
		_, err := db.Exec(
			fmt.Sprintf("INSERT OR REPLACE INTO %s (url, title, body, created_at, updated_at, merged_at, closed_at, repository, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", getTableName("pull_requests")),
			pr.URL, pr.Title, pr.Body, pr.CreatedAt.Format(time.RFC3339), pr.UpdatedAt.Format(time.RFC3339), mergedAtStr, closedAtStr, pr.Repository, pr.Author,
		)
		return err
	}, "save pull request")
}

// GetRepositories gets all repositories from the database
func (db *DB) GetRepositories() ([]Repository, error) {
	rows, err := db.Query(fmt.Sprintf("SELECT name, updated_at, has_issues_enabled, has_discussions_enabled FROM %s", getTableName("repositories")))
	if err != nil {
		return nil, fmt.Errorf("failed to query repositories: %w", err)
	}
	defer rows.Close()

	var repositories []Repository
	for rows.Next() {
		var repo Repository
		var updatedAtStr sql.NullString
		if err := rows.Scan(&repo.Name, &updatedAtStr, &repo.HasIssuesEnabled, &repo.HasDiscussionsEnabled); err != nil {
			return nil, fmt.Errorf("failed to scan repository: %w", err)
		}

		// Parse updated_at if available
		if updatedAtStr.Valid {
			parsedTime, err := time.Parse(time.RFC3339, updatedAtStr.String)
			if err == nil {
				repo.UpdatedAt = parsedTime
			}
		}

		repositories = append(repositories, repo)
	}

	return repositories, nil
}

// GetRepository gets a specific repository from the database
func (db *DB) GetRepository(name string) (*Repository, error) {
	query := fmt.Sprintf("SELECT name, updated_at, has_issues_enabled, has_discussions_enabled FROM %s WHERE name = ?", getTableName("repositories"))
	row := db.QueryRow(query, name)

	var repo Repository
	var updatedAtStr sql.NullString
	err := row.Scan(&repo.Name, &updatedAtStr, &repo.HasIssuesEnabled, &repo.HasDiscussionsEnabled)
	if err != nil {
		return nil, err
	}

	// Parse updated_at if available
	if updatedAtStr.Valid {
		parsedTime, err := time.Parse(time.RFC3339, updatedAtStr.String)
		if err == nil {
			repo.UpdatedAt = parsedTime
		}
	}

	return &repo, nil
}

// parseTimestamp safely parses RFC3339 timestamp strings
func parseTimestamp(timeStr string) (time.Time, error) {
	if timeStr == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, timeStr)
}

// parseOptionalTimestamp safely parses optional RFC3339 timestamp strings
func parseOptionalTimestamp(timeStr sql.NullString) *time.Time {
	if !timeStr.Valid {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, timeStr.String); err == nil {
		return &t
	}
	return nil
}

// buildWhereClause builds a WHERE clause with conditions and returns the clause and args
func buildWhereClause(conditions map[string]interface{}) (string, []interface{}) {
	var conditionStrs []string
	var args []interface{}
	
	for field, value := range conditions {
		if value != nil {
			switch v := value.(type) {
			case string:
				if v != "" {
					conditionStrs = append(conditionStrs, field+" = ?")
					args = append(args, v)
				}
			case []string:
				// Handle array of strings for IN clause
				if len(v) > 0 {
					// Filter out empty strings
					var validValues []string
					for _, item := range v {
						if strings.TrimSpace(item) != "" {
							validValues = append(validValues, strings.TrimSpace(item))
						}
					}
					
					if len(validValues) > 0 {
						// Create placeholders for IN clause
						placeholders := make([]string, len(validValues))
						for i := range validValues {
							placeholders[i] = "?"
						}
						conditionStrs = append(conditionStrs, field+" IN ("+strings.Join(placeholders, ", ")+")")
						
						// Add values to args
						for _, validValue := range validValues {
							args = append(args, validValue)
						}
					}
				}
			case time.Time:
				if !v.IsZero() {
					if strings.Contains(field, "<=") {
						conditionStrs = append(conditionStrs, strings.TrimSpace(strings.Replace(field, "<=", "", 1))+" <= ?")
					} else {
						conditionStrs = append(conditionStrs, field+" >= ?")
					}
					args = append(args, v.Format(time.RFC3339))
				}
			case *time.Time:
				if v != nil && !v.IsZero() {
					if strings.Contains(field, "<=") {
						conditionStrs = append(conditionStrs, strings.TrimSpace(strings.Replace(field, "<=", "", 1))+" <= ?")
					} else {
						conditionStrs = append(conditionStrs, field+" >= ?")
					}
					args = append(args, v.Format(time.RFC3339))
				}
			}
		}
	}
	
	var whereClause string
	if len(conditionStrs) > 0 {
		whereClause = "WHERE " + strings.Join(conditionStrs, " AND ")
	}
	
	return whereClause, args
}

// GetDiscussions gets discussions based on filters from the database
func (db *DB) GetDiscussions(repository string, fromDate time.Time, toDate time.Time, authors []string) ([]Discussion, error) {
	conditions := map[string]interface{}{
		"repository": repository,
		"created_at": fromDate,
		"author":     authors,
	}
	if !toDate.IsZero() {
		conditions["created_at <="] = toDate
	}
	
	whereClause, args := buildWhereClause(conditions)

	query := `
		SELECT url, title, body, author, created_at, updated_at, repository
		FROM ` + getTableName("discussions") + ` ` + whereClause + `
		ORDER BY created_at ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query discussions: %w", err)
	}
	defer rows.Close()

	var discussions []Discussion
	for rows.Next() {
		var discussion Discussion
		var createdAtStr, updatedAtStr string
		if err := rows.Scan(
			&discussion.URL,
			&discussion.Title,
			&discussion.Body,
			&discussion.Author,
			&createdAtStr,
			&updatedAtStr,
			&discussion.Repository,
		); err != nil {
			return nil, fmt.Errorf("failed to scan discussion: %w", err)
		}

		// Parse timestamps using helper functions
		if createdAt, err := parseTimestamp(createdAtStr); err == nil {
			discussion.CreatedAt = createdAt
		}
		if updatedAt, err := parseTimestamp(updatedAtStr); err == nil {
			discussion.UpdatedAt = updatedAt
		}

		discussions = append(discussions, discussion)
	}

	return discussions, nil
}

// GetIssues gets issues based on filters from the database
func (db *DB) GetIssues(repository string, createdFromDate time.Time, createdToDate time.Time, closedFromDate *time.Time, closedToDate *time.Time, authors []string) ([]Issue, error) {
	conditions := map[string]interface{}{
		"repository": repository,
		"created_at": createdFromDate,
		"closed_at":  closedFromDate,
		"author":     authors,
	}
	if !createdToDate.IsZero() {
		conditions["created_at <="] = createdToDate
	}
	if closedToDate != nil && !closedToDate.IsZero() {
		conditions["closed_at <="] = closedToDate
	}
	
	whereClause, args := buildWhereClause(conditions)

	query := `
		SELECT url, title, body, author, created_at, updated_at, closed_at, repository
		FROM ` + getTableName("issues") + ` ` + whereClause + `
		ORDER BY created_at ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query issues: %w", err)
	}
	defer rows.Close()

	var issues []Issue
	for rows.Next() {
		var issue Issue
		var createdAtStr, updatedAtStr string
		var closedAtStr sql.NullString
		if err := rows.Scan(
			&issue.URL,
			&issue.Title,
			&issue.Body,
			&issue.Author,
			&createdAtStr,
			&updatedAtStr,
			&closedAtStr,
			&issue.Repository,
		); err != nil {
			return nil, fmt.Errorf("failed to scan issue: %w", err)
		}

		// Parse timestamps using helper functions
		if createdAt, err := parseTimestamp(createdAtStr); err == nil {
			issue.CreatedAt = createdAt
		}
		if updatedAt, err := parseTimestamp(updatedAtStr); err == nil {
			issue.UpdatedAt = updatedAt
		}
		issue.ClosedAt = parseOptionalTimestamp(closedAtStr)

		issues = append(issues, issue)
	}

	return issues, nil
}

// GetPullRequests gets pull requests based on filters from the database
func (db *DB) GetPullRequests(repository string, createdFromDate time.Time, createdToDate time.Time, closedFromDate *time.Time, closedToDate *time.Time, mergedFromDate *time.Time, mergedToDate *time.Time, authors []string) ([]PullRequest, error) {
	conditions := map[string]interface{}{
		"repository": repository,
		"created_at": createdFromDate,
		"closed_at":  closedFromDate,
		"merged_at":  mergedFromDate,
		"author":     authors,
	}
	if !createdToDate.IsZero() {
		conditions["created_at <="] = createdToDate
	}
	if closedToDate != nil && !closedToDate.IsZero() {
		conditions["closed_at <="] = closedToDate
	}
	if mergedToDate != nil && !mergedToDate.IsZero() {
		conditions["merged_at <="] = mergedToDate
	}
	
	whereClause, args := buildWhereClause(conditions)

	query := `
		SELECT url, title, body, author, created_at, updated_at, merged_at, closed_at, repository
		FROM ` + getTableName("pull_requests") + ` ` + whereClause + `
		ORDER BY created_at ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query pull requests: %w", err)
	}
	defer rows.Close()

	var pullRequests []PullRequest
	for rows.Next() {
		var pr PullRequest
		var createdAtStr, updatedAtStr string
		var mergedAtStr, closedAtStr sql.NullString
		if err := rows.Scan(
			&pr.URL,
			&pr.Title,
			&pr.Body,
			&pr.Author,
			&createdAtStr,
			&updatedAtStr,
			&mergedAtStr,
			&closedAtStr,
			&pr.Repository,
		); err != nil {
			return nil, fmt.Errorf("failed to scan pull request: %w", err)
		}

		// Parse timestamps using helper functions
		if createdAt, err := parseTimestamp(createdAtStr); err == nil {
			pr.CreatedAt = createdAt
		}
		if updatedAt, err := parseTimestamp(updatedAtStr); err == nil {
			pr.UpdatedAt = updatedAt
		}
		pr.MergedAt = parseOptionalTimestamp(mergedAtStr)
		pr.ClosedAt = parseOptionalTimestamp(closedAtStr)

		pullRequests = append(pullRequests, pr)
	}

	return pullRequests, nil
}

// getLastUpdatedForTable gets the most recent updated_at date for a repository from a specific table
func (db *DB) getLastUpdatedForTable(tableName, repository string) (time.Time, error) {
	query := fmt.Sprintf("SELECT MAX(updated_at) FROM %s WHERE repository = ?", getTableName(tableName))
	var lastUpdatedStr sql.NullString
	err := db.QueryRow(query, repository).Scan(&lastUpdatedStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get last updated from %s: %w", tableName, err)
	}

	if !lastUpdatedStr.Valid {
		return time.Time{}, nil
	}

	t, err := time.Parse(time.RFC3339, lastUpdatedStr.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse last updated time '%s' as RFC3339 from %s: %w", lastUpdatedStr.String, tableName, err)
	}
	
	return t, nil
}

// GetDiscussionLastUpdated gets the most recent updated_at date for a repository
func (db *DB) GetDiscussionLastUpdated(repository string) (time.Time, error) {
	return db.getLastUpdatedForTable("discussions", repository)
}

// GetIssueLastUpdated gets the most recent updated_at date for a repository
func (db *DB) GetIssueLastUpdated(repository string) (time.Time, error) {
	return db.getLastUpdatedForTable("issues", repository)
}

// GetPullRequestLastUpdated gets the most recent updated_at date for a repository
func (db *DB) GetPullRequestLastUpdated(repository string) (time.Time, error) {
	return db.getLastUpdatedForTable("pull_requests", repository)
}

// removeRepositoryAndAssociatedData removes a repository and all its associated data from the database
func (db *DB) removeRepositoryAndAssociatedData(repositoryName string, progress *Progress) {
	progress.Log("Repository %s does not exist, removing repository and all associated data from database", repositoryName)
	
	// Remove the repository
	_, cleanupErr := db.Exec(fmt.Sprintf("DELETE FROM %s WHERE name = ?", getTableName("repositories")), repositoryName)
	if cleanupErr != nil {
		progress.Log("Warning: failed to remove repository %s from database: %v", repositoryName, cleanupErr)
	}
	
	// Remove all associated discussions
	_, cleanupErr = db.Exec(fmt.Sprintf("DELETE FROM %s WHERE repository = ?", getTableName("discussions")), repositoryName)
	if cleanupErr != nil {
		progress.Log("Warning: failed to remove discussions for repository %s from database: %v", repositoryName, cleanupErr)
	}
	
	// Remove all associated issues
	_, cleanupErr = db.Exec(fmt.Sprintf("DELETE FROM %s WHERE repository = ?", getTableName("issues")), repositoryName)
	if cleanupErr != nil {
		progress.Log("Warning: failed to remove issues for repository %s from database: %v", repositoryName, cleanupErr)
	}
	
	// Remove all associated pull requests
	_, cleanupErr = db.Exec(fmt.Sprintf("DELETE FROM %s WHERE repository = ?", getTableName("pull_requests")), repositoryName)
	if cleanupErr != nil {
		progress.Log("Warning: failed to remove pull requests for repository %s from database: %v", repositoryName, cleanupErr)
	}
}

// GetMostRecentRepositoryTimestamp gets the most recent updated_at timestamp from repositories
func (db *DB) GetMostRecentRepositoryTimestamp(progress *Progress) (time.Time, error) {
	// Create the updated_at column if it doesn't exist
	_, err := db.Exec(`
		PRAGMA table_info(repositories)
	`)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to check table schema: %w", err)
	}

	// Add updated_at column if it doesn't exist (versioned table already has it)
	_, err = db.Exec(fmt.Sprintf(`
		ALTER TABLE %s ADD COLUMN updated_at DATETIME
	`, getTableName("repositories")))
	if err == nil {
		if progress != nil {
			progress.Log("Added updated_at column to repositories table")
		} else {
			slog.Info("Added updated_at column to repositories table")
		}
	} else if !strings.Contains(err.Error(), "duplicate column name") {
		return time.Time{}, fmt.Errorf("failed to add updated_at column: %w", err)
	}

	// Query for most recent timestamp
	row := db.QueryRow(fmt.Sprintf("SELECT MAX(updated_at) FROM %s", getTableName("repositories")))
	var timestampStr sql.NullString
	if err := row.Scan(&timestampStr); err != nil {
		return time.Time{}, fmt.Errorf("failed to get most recent repository timestamp: %w", err)
	}

	// If no timestamp found, return zero time
	if !timestampStr.Valid {
		return time.Time{}, nil
	}

	// Parse the timestamp
	timestamp, err := time.Parse(time.RFC3339, timestampStr.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse repository timestamp: %w", err)
	}

	return timestamp, nil
}

// handleRateLimit checks if the error is a rate limit error and waits if needed
// Returns a tuple of (isRateLimit, waitDuration)
func handleRateLimit(err error) (bool, time.Duration) {
	// Check if error is a GraphQL error
	if err == nil {
		return false, 0
	}

	// Lock the rate limit mutex to check/update global state
	rateLimitMutex.Lock()
	defer rateLimitMutex.Unlock()

	// Maximum wait time to prevent hanging indefinitely
	const maxWaitTime = 15 * time.Minute

	// If we've already hit a rate limit globally, return true
	if rateLimitHit {
		// Calculate remaining wait time based on the stored reset time
		waitTime := time.Until(rateLimitResetTime)
		if waitTime <= 0 {
			// Reset time has passed, clear the rate limit flag
			rateLimitHit = false
			rateLimitResetTime = time.Time{}
			return false, 0
		}

		// Cap the wait time to prevent excessive waiting
		if waitTime > maxWaitTime {
			slog.Warn("Capping excessive rate limit wait time", "from", waitTime, "to", maxWaitTime)
			waitTime = maxWaitTime
		}

		return true, waitTime
	}

	// If we've already hit a secondary rate limit globally, return true
	if secondaryRateLimitHit {
		// Calculate remaining wait time based on the stored reset time
		waitTime := time.Until(secondaryResetTime)
		if waitTime <= 0 {
			// Reset time has passed, clear the secondary rate limit flag
			secondaryRateLimitHit = false
			secondaryResetTime = time.Time{}
			// Reset backoff to initial value, but not too aggressive
			backoffDuration = 5 * time.Second
			return false, 0
		}

		// Cap the wait time to prevent excessive waiting
		if waitTime > maxWaitTime {
			slog.Warn("Capping excessive secondary rate limit wait time", "from", waitTime, "to", maxWaitTime)
			waitTime = maxWaitTime
		}

		return true, waitTime
	}

	// Check if the error message contains rate limit information
	errMsg := err.Error()

	// Check for 429 status code in error message (handled by transport already, but check for completeness)
	if strings.Contains(errMsg, "429") || strings.Contains(errMsg, "Too Many Requests") {
		// Transport should have already handled this, but provide fallback
		resetDuration := 60 * time.Second // Default wait time for 429
		
		// Set appropriate rate limit state based on context
		if strings.Contains(errMsg, "abuse") || strings.Contains(errMsg, "secondary") {
			secondaryRateLimitHit = true
			secondaryResetTime = time.Now().Add(resetDuration)
			slog.Info("GitHub API secondary rate limit detected via error message", "wait", resetDuration.String())
		} else {
			rateLimitHit = true
			rateLimitResetTime = time.Now().Add(resetDuration)
			slog.Info("GitHub API primary rate limit detected via error message", "wait", resetDuration.String())
		}
		
		return true, resetDuration
	}

	// Check for secondary rate limit specifically
	if strings.Contains(errMsg, "secondary rate limit") || strings.Contains(errMsg, "abuse detection") {
		// More aggressive exponential backoff for secondary rate limits
		resetDuration := backoffDuration

		// Increase backoff for next time (exponential backoff with jitter)
		nextBackoff := backoffDuration * 2
		// Add jitter (10-20% randomization) to prevent thundering herd
		jitter := time.Duration(rand.Intn(int(nextBackoff/5))) // 0-20% jitter
		backoffDuration = nextBackoff + jitter
		
		if backoffDuration > maxBackoffDuration {
			backoffDuration = maxBackoffDuration
		}

		// Try to parse wait time from error message if available
		if strings.Contains(errMsg, "Please wait a few minutes") {
			// GitHub typically suggests waiting a few minutes for secondary rate limits
			resetDuration = 5 * time.Minute
		} else if strings.Contains(errMsg, "Please wait") {
			// If we see "Please wait" but no specific time, be more conservative
			resetDuration = 3 * time.Minute
		}

		// Add a buffer to make sure rate limit has fully reset
		resetDuration += 30 * time.Second // Increased buffer from 5s to 30s

		// Cap the reset duration to prevent excessive waiting
		if resetDuration > maxWaitTime {
			slog.Warn("Capping excessive secondary rate limit duration", "from", resetDuration, "to", maxWaitTime)
			resetDuration = maxWaitTime
		}

		// Set the global secondary rate limit state
		secondaryRateLimitHit = true
		secondaryResetTime = time.Now().Add(resetDuration)

		// Log the secondary rate limit hit
		slog.Info("GitHub API secondary rate limit hit", "duration", resetDuration.String(), "until", secondaryResetTime.Format(time.RFC3339))

		return true, resetDuration
	} else if strings.Contains(errMsg, "API rate limit exceeded") ||
		strings.Contains(errMsg, "rate limit exceeded") {
		// Handle primary rate limit

		// Handle primary rate limit with better reset time detection

		// Try to parse reset time from error message if available
		resetDuration := 60 * time.Second // Default wait time

		// Look for "Reset in X minutes" or similar patterns
		resetIndex := strings.Index(errMsg, "Reset in ")
		if resetIndex != -1 {
			afterReset := errMsg[resetIndex+9:] // Skip "Reset in "

			// Try to extract the time value
			var value float64
			var unit string
			if _, err := fmt.Sscanf(afterReset, "%f %s", &value, &unit); err == nil {
				// Convert to duration based on unit
				switch {
				case strings.HasPrefix(unit, "second"):
					resetDuration = time.Duration(value * float64(time.Second))
				case strings.HasPrefix(unit, "minute"):
					resetDuration = time.Duration(value * float64(time.Minute))
				case strings.HasPrefix(unit, "hour"):
					resetDuration = time.Duration(value * float64(time.Hour))
				}
			}
		}

		// If no specific reset time found, use current rate limit info
		rateLimitInfoMutex.RLock()
		if !currentRateLimit.Reset.IsZero() {
			// Use the actual reset time from headers
			resetTime := currentRateLimit.Reset
			resetDuration = time.Until(resetTime)
			// If reset time is in the past or very close, use minimum wait
			if resetDuration <= 0 {
				resetDuration = 60 * time.Second
			}
		}
		rateLimitInfoMutex.RUnlock()

		// Add a buffer to make sure rate limit has fully reset
		resetDuration += 30 * time.Second // Increased buffer from 5s to 30s

		// Cap the reset duration to prevent excessive waiting
		if resetDuration > maxWaitTime {
			slog.Warn("Capping excessive rate limit duration", "from", resetDuration, "to", maxWaitTime)
			resetDuration = maxWaitTime
		}

		// Set the global rate limit state
		rateLimitHit = true
		rateLimitResetTime = time.Now().Add(resetDuration)

		// Log the rate limit hit
		slog.Info("GitHub API rate limit hit", "duration", resetDuration.String(), "until", rateLimitResetTime.Format(time.RFC3339))

		return true, resetDuration
	}

	return false, 0
}

// handleGraphQLError centralizes GraphQL error handling with retries and rate limit management
// Returns (success, shouldRetry, waitDuration, error)
func handleGraphQLError(ctx context.Context, client *githubv4.Client, queryFunc func() error, operation string, page int, requestCount *atomic.Int64, progress *Progress) error {
	const maxRetries = 10 // Increased from 3 to 10 for better rate limit handling
	const baseRetryDelay = 2 * time.Second // Base delay for exponential backoff
	const maxRetryDelay = 10 * time.Minute // Maximum delay between retries
	
	for retries := 0; retries < maxRetries; retries++ {
		// Check for context cancellation
		if ctx.Err() != nil {
			slog.Info("Context cancelled during operation", "operation", operation, "page", page, "error", ctx.Err())
			return ctx.Err()
		}

		// Proactively check if we're currently rate limited before making request
		rateLimitMutex.Lock()
		if rateLimitHit {
			waitTime := time.Until(rateLimitResetTime)
			if waitTime > 0 {
				rateLimitMutex.Unlock()
				slog.Info("Proactive rate limit check: primary rate limit active", "wait", waitTime.String(), "operation", operation, "page", page)
				
				if progress != nil {
					progress.UpdateMessage(fmt.Sprintf("Rate limit active, waiting %v before %s page %d...", waitTime, operation, page))
				}

				// Wait for rate limit to reset with context cancellation support
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(waitTime):
					// Continue after wait time
				}
				continue // Retry after waiting
			} else {
				// Rate limit has expired, clear it
				rateLimitHit = false
				rateLimitResetTime = time.Time{}
			}
		}
		
		if secondaryRateLimitHit {
			waitTime := time.Until(secondaryResetTime)
			if waitTime > 0 {
				rateLimitMutex.Unlock()
				slog.Info("Proactive rate limit check: secondary rate limit active", "wait", waitTime.String(), "operation", operation, "page", page)
				
				if progress != nil {
					progress.UpdateMessage(fmt.Sprintf("Secondary rate limit active, waiting %v before %s page %d...", waitTime, operation, page))
				}

				// Wait for rate limit to reset with context cancellation support
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(waitTime):
					// Continue after wait time
				}
				continue // Retry after waiting
			} else {			// Secondary rate limit has expired, clear it and reset backoff conservatively
			secondaryRateLimitHit = false
			secondaryResetTime = time.Time{}
			backoffDuration = 5 * time.Second // Conservative reset
			}
		}
		rateLimitMutex.Unlock()

		// Execute the GraphQL query
		err := queryFunc()
		if requestCount != nil {
			requestCount.Add(1)
		}

		// Add delay between requests to avoid secondary rate limits
		addRequestDelay()

		if err == nil {
			return nil // Success
		}

		// Check for repository not found errors - do not retry these
		if strings.Contains(err.Error(), "Could not resolve to a Repository") {
			return err // Return immediately without retrying
		}

		// Handle 5xx server errors with exponential backoff
		if strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "502") || 
		   strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "504") {
			// Calculate exponential backoff delay
			retryDelay := time.Duration(1<<uint(retries)) * baseRetryDelay
			if retryDelay > maxRetryDelay {
				retryDelay = maxRetryDelay
			}
			
			// Truncate error message to prevent very long HTML responses from cluttering logs
			errMsg := err.Error()
			if len(errMsg) > 200 {
				errMsg = errMsg[:200] + "..."
			}
			
			if retries < maxRetries-1 {
				slog.Info("5xx server error, retrying", "operation", operation, "page", page, "retry", retries+1, "max_retries", maxRetries, "delay", retryDelay.String(), "error", errMsg)
				if progress != nil {
					progress.UpdateMessage(fmt.Sprintf("5xx error on page %d, retrying in %v (attempt %d/%d)", 
						page, retryDelay, retries+1, maxRetries))
				}
				
				// Wait with context cancellation support
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(retryDelay):
					// Continue to next retry
				}
				continue
			}
			// Final retry attempt failed
			return fmt.Errorf("server error during %s (page %d) after %d retries: %w", operation, page, maxRetries, err)
		}

		// Check for rate limits
		isRateLimit, waitTime := handleRateLimit(err)
		if isRateLimit {
			// Truncate error message to prevent very long responses from cluttering logs
			errMsg := err.Error()
			if len(errMsg) > 200 {
				errMsg = errMsg[:200] + "..."
			}
			slog.Info("Rate limit reached during operation", "operation", operation, "page", page, "error", errMsg)
			
			if progress != nil {
				progress.UpdateMessage(fmt.Sprintf("Rate limit reached on page %d, waiting for %v before retrying...", page, waitTime))
			}

			// Wait for rate limit to reset with context cancellation support
			select {
			case <-ctx.Done():
				slog.Info("Context cancelled while waiting for rate limit", "operation", operation, "error", ctx.Err())
				return ctx.Err()
			case <-time.After(waitTime):
				// Continue after wait time
			}
			continue // Retry after waiting
		}

		// For non-rate-limit errors, apply exponential backoff
		if retries < maxRetries-1 {
			// Calculate exponential backoff delay
			retryDelay := time.Duration(1<<uint(retries)) * baseRetryDelay
			if retryDelay > maxRetryDelay {
				retryDelay = maxRetryDelay
			}
			
			slog.Info("Non-rate-limit error, retrying", "operation", operation, "page", page, "retry", retries+1, "max_retries", maxRetries, "delay", retryDelay.String(), "error", err)
			if progress != nil {
				progress.UpdateMessage(fmt.Sprintf("Error on page %d, retrying in %v (attempt %d/%d)", 
					page, retryDelay, retries+1, maxRetries))
			}
			
			// Wait with context cancellation support
			select {
			case <-ctx.Done():
				slog.Info("Context cancelled while waiting to retry", "operation", operation, "error", ctx.Err())
				return ctx.Err()
			case <-time.After(retryDelay):
				// Continue to next retry
			}
			continue
		}
	}

	return fmt.Errorf("failed %s after %d retries (page %d)", operation, maxRetries, page)
}

// ClearData removes data from database based on config Force flag and Items
func ClearData(db *DB, config *Config, progress *Progress) error {
	if !config.Force {
		return nil
	}

	// If specific items are provided, clear only those
	if len(config.Items) > 0 {
		for _, item := range config.Items {
			switch item {
			case "repositories":
				progress.Log("Deleting repositories table")
				_, err := db.Exec(fmt.Sprintf("DELETE FROM %s", getTableName("repositories")))
				if err != nil {
					return fmt.Errorf("failed to clear repositories: %w", err)
				}
			case "discussions":
				progress.Log("Deleting discussions table")
				_, err := db.Exec(fmt.Sprintf("DELETE FROM %s", getTableName("discussions")))
				if err != nil {
					return fmt.Errorf("failed to clear discussions: %w", err)
				}
			case "issues":
				progress.Log("Deleting issues table")
				_, err := db.Exec(fmt.Sprintf("DELETE FROM %s", getTableName("issues")))
				if err != nil {
					return fmt.Errorf("failed to clear issues: %w", err)
				}
			case "pull-requests":
				progress.Log("Deleting pull_requests table")
				_, err := db.Exec(fmt.Sprintf("DELETE FROM %s", getTableName("pull_requests")))
				if err != nil {
					return fmt.Errorf("failed to clear pull requests: %w", err)
				}

			}
		}
	} else {
		// Clear all data
		tableTypes := []string{"pull_requests", "issues", "discussions", "repositories"}
		for _, tableType := range tableTypes {
			_, err := db.Exec(fmt.Sprintf("DELETE FROM %s", getTableName(tableType)))
			if err != nil {
				return fmt.Errorf("failed to clear %s: %w", tableType, err)
			}
		}
	}

	return nil
}

// PullRepositories pulls repositories from GitHub using GraphQL API
func PullRepositories(ctx context.Context, client *githubv4.Client, db *DB, config *Config, progress *Progress) error {
	if config.Organization == "" {
		return fmt.Errorf("organization is not set")
	}

	progress.SetCurrentItem("repositories")
	progress.UpdateMessage("Fetching repositories")

	// Track total count of repositories processed
	totalCount := atomic.Int64{}

	// Get the most recent repository timestamp from the database
	mostRecentTimestamp, err := db.GetMostRecentRepositoryTimestamp(progress)
	if err != nil {
		progress.Log("Warning: Failed to get most recent repository timestamp: %v", err)
		// Continue without timestamp optimization if we can't get the timestamp
	}

	hasTimestampOptimization := !mostRecentTimestamp.IsZero()
	if hasTimestampOptimization {
		progress.Log("Using timestamp optimization. Most recent repository timestamp: %v", mostRecentTimestamp)
	} else {
		progress.Log("No previous timestamp found, fetching all repositories")
	}

	// Setup GraphQL request rate measurement
	requestCount := atomic.Int64{}
	stopRateMeasurement := make(chan struct{})

	// Start a goroutine to measure and update request rate every second
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		var lastCount int64

		for {
			select {
			case <-stopRateMeasurement:
				return
			case <-ticker.C:
				currentCount := requestCount.Load()
				requestsInLastSecond := currentCount - lastCount
				lastCount = currentCount

				// Update spinner speed based on request rate
				progress.UpdateRequestRate(int(requestsInLastSecond))
			}
		}
	}()

	// Use channels for parallel page fetching
	type repoInfo struct {
		name                  string
		updatedAt             time.Time
		hasIssuesEnabled      bool
		hasDiscussionsEnabled bool
	}

	type pageResult struct {
		page        int
		shouldStop  bool
		endCursor   string
		hasNextPage bool
	}

	resultChan := make(chan pageResult, 100) // Buffer for up to 100 pages
	errChan := make(chan error, 100)
	semaphore := make(chan struct{}, 50) // Limit to 50 concurrent requests

	var wg sync.WaitGroup

	// Define a function to fetch and save a single page
	fetchPage := func(page int, cursor *githubv4.String) {
		defer wg.Done()
		defer func() { <-semaphore }() // Release semaphore when done

		// Ensure we always send a result, even if there's a panic or early return
		resultSent := false
		defer func() {
			if !resultSent {
				slog.Info("Emergency result send", "page", page)
				select {
				case resultChan <- pageResult{
					page:        page,
					shouldStop:  true,
					endCursor:   "",
					hasNextPage: false,
				}:
				default:
					// Channel might be full or closed, don't block
				}
			}
		}()

		progress.Log("Fetching page %d of repositories", page)

		// For first page, don't show page count since we don't know it yet
		if page == 1 {
			progress.UpdateMessage(fmt.Sprintf("Fetching repositories for organization %s", config.Organization))
		} else {
			progress.UpdateMessage(fmt.Sprintf("Fetching repositories for organization %s (page %d)",
				config.Organization, page))
		}

		var query struct {
			Organization struct {
				Repositories struct {
					Nodes []struct {
						Name                  githubv4.String
						UpdatedAt             githubv4.DateTime
						HasIssuesEnabled      githubv4.Boolean
						HasDiscussionsEnabled githubv4.Boolean
					}
					PageInfo struct {
						EndCursor   githubv4.String
						HasNextPage bool
					}
				} `graphql:"repositories(isArchived: false, isFork: false, first: 100, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC})"`
			} `graphql:"organization(login: $login)"`
		}
		variables := map[string]interface{}{
			"login":  githubv4.String(config.Organization),
			"cursor": cursor, // cursor can be nil for first page
		}

		// Use centralized GraphQL error handling
		err := handleGraphQLError(ctx, client, func() error {
			return client.Query(ctx, &query, variables)
		}, "repositories query", page, &requestCount, progress)

		if err != nil {
			// Send error to error channel for structured handling
			errChan <- fmt.Errorf("failed to query repositories (page %d): %w", page, err)

			// Send result to unblock pagination even on error
			resultChan <- pageResult{
				page:        page,
				shouldStop:  true, // Stop pagination on error
				endCursor:   "",
				hasNextPage: false,
			}
			resultSent = true
			return
		}

		progress.Log("Successfully fetched page %d, processing %d repositories", page, len(query.Organization.Repositories.Nodes))

		// Extract repository names and timestamps
		repos := make([]repoInfo, 0, len(query.Organization.Repositories.Nodes))

		// Check if we should stop based on the timestamp optimization
		shouldStopFetching := false

		progress.Log("Starting to process %d repositories from page %d", len(query.Organization.Repositories.Nodes), page)

		for _, repo := range query.Organization.Repositories.Nodes {
			repoUpdatedAt := repo.UpdatedAt.Time
			repoName := string(repo.Name)
			repoHasIssuesEnabled := bool(repo.HasIssuesEnabled)
			repoHasDiscussionsEnabled := bool(repo.HasDiscussionsEnabled)

			// If using timestamp optimization, check if we can stop
			if hasTimestampOptimization && !repoUpdatedAt.After(mostRecentTimestamp) {
				progress.Log("Reached repository with updatedAt (%v) older than or equal to most recent DB timestamp (%v), stopping fetch",
					repoUpdatedAt, mostRecentTimestamp)
				shouldStopFetching = true

				// Stop processing remaining repos in this page
				break
			}

			repos = append(repos, repoInfo{
				name:                  repoName,
				updatedAt:             repoUpdatedAt,
				hasIssuesEnabled:      repoHasIssuesEnabled,
				hasDiscussionsEnabled: repoHasDiscussionsEnabled,
			})
		}

		// If we determined we should stop, log it prominently
		if shouldStopFetching {
			progress.Log("OPTIMIZATION: Page %d contains repositories older than timestamp %v - this and future pages will be stopped",
				page, mostRecentTimestamp.Format(time.RFC3339))
		}

		// Save each repository individually as we process them
		progress.Log("Saving %d repositories from page %d to database", len(repos), page)
		for i, repo := range repos {
			// Only update message every 10 repositories to reduce overhead
			if i%10 == 0 || i == len(repos)-1 {
				progress.UpdateMessage(fmt.Sprintf("Saving repository from page %d (%d/%d): %s",
					page, i+1, len(repos), repo.name))
			}

			// Create repository object and save it
			repository := &Repository{
				Name:                    repo.name,
				UpdatedAt:               repo.updatedAt,
				HasIssuesEnabled:        repo.hasIssuesEnabled,
				HasDiscussionsEnabled:   repo.hasDiscussionsEnabled,
			}

			if err := db.SaveRepository(repository); err != nil {
				slog.Error("Error saving repository", "repository", repo.name, "error", err)
				errChan <- fmt.Errorf("failed to save repository %s: %w", repo.name, err)

				// Send result to unblock pagination even on repository save error
				resultChan <- pageResult{
					page:        page,
					shouldStop:  true,
					endCursor:   "",
					hasNextPage: false,
				}
				resultSent = true
				return
			}
			
			// Update total count for each repository
			newTotal := totalCount.Add(1)
			
			// Update progress display every 10 repositories to reduce overhead
			if i%10 == 0 || i == len(repos)-1 {
				progress.UpdateItemCount("repositories", int(newTotal))
			}
		}

		progress.UpdateMessage(fmt.Sprintf("Saved %d repositories from page %d", len(repos), page))

		// Send result for tracking if we should stop further pages
		// This must always be sent to prevent the pagination logic from hanging
		slog.Info("Sending result", "page", page, "shouldStop", shouldStopFetching, "hasNextPage", query.Organization.Repositories.PageInfo.HasNextPage)
		resultChan <- pageResult{
			page:        page,
			shouldStop:  shouldStopFetching,
			endCursor:   string(query.Organization.Repositories.PageInfo.EndCursor),
			hasNextPage: query.Organization.Repositories.PageInfo.HasNextPage,
		}
		resultSent = true
		slog.Info("Result sent", "page", page)
	}
	// Use the existing fetchPage function for the first page (with nil cursor)
	semaphore <- struct{}{} // Acquire semaphore
	wg.Add(1)
	go fetchPage(1, nil)

	// Wait for first page result to get cursor for subsequent pages
	var hasNextPage bool
	var endCursor githubv4.String
	var shouldStopDueToOptimization bool

	select {
	case result := <-resultChan:
		if result.shouldStop {
			shouldStopDueToOptimization = true
			hasNextPage = false  // Stop pagination due to optimization
		} else {
			endCursor = githubv4.String(result.endCursor)
			hasNextPage = result.hasNextPage
		}
	case <-ctx.Done():
		slog.Info("Context cancelled while waiting for first page")
		hasNextPage = false
	}

	// Continue with additional pages if available
	if shouldStopDueToOptimization {
		slog.Info("Stopping pagination after first page due to timestamp optimization")
	} else if !hasNextPage {
		// Skip fetching additional pages - no more pages available
		slog.Info("No more pages available after first page")
	} else {
		// Sequentially process pages until no more pages or stop condition
		currentPage := 2
		currentCursor := endCursor

		for hasNextPage {
			// Acquire semaphore before starting
			semaphore <- struct{}{}

			wg.Add(1)
			go fetchPage(currentPage, githubv4.NewString(currentCursor))

			// Wait for this page to complete to get the next cursor
			var pageCompleted bool
			var nextCursor string
			var nextHasPage bool

			// Wait for the page result
			var shouldStopPagination bool
			select {
			case result := <-resultChan:
				pageCompleted = true
				if result.shouldStop {
					slog.Info("Stopping pagination due to timestamp optimization", "page", result.page)
					shouldStopPagination = true
				}
				nextCursor = result.endCursor
				nextHasPage = result.hasNextPage
			case <-time.After(5 * time.Minute): // Add timeout for individual pages
				slog.Warn("Timeout waiting for page result", "page", currentPage, "timeout", "5 minutes")
				shouldStopPagination = true
				pageCompleted = false
			case <-ctx.Done():
				slog.Info("Context cancelled during pagination")
				shouldStopPagination = true
			}

			if shouldStopPagination || !pageCompleted || !nextHasPage {
				break
			}

			currentPage++
			currentCursor = githubv4.String(nextCursor)
			hasNextPage = nextHasPage
		}
	}

	// Wait for all goroutines to finish with a timeout
	waitChan := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitChan)
	}()

	// Use a timeout to prevent hanging indefinitely
	const waitTimeout = 3 * time.Minute
	select {
	case <-waitChan:
		// All goroutines completed normally
	case <-time.After(waitTimeout):
		// Timeout occurred, log a warning and continue
		slog.Warn("Timed out waiting for repository fetching goroutines to complete", "timeout", waitTimeout)
	case <-ctx.Done():
		// Context cancelled
		slog.Info("Context cancelled while waiting for repository fetching goroutines")
	}

	close(resultChan)
	close(errChan)
	close(stopRateMeasurement)

	// Check for errors - collect them but don't fail immediately unless fatal
	var errors []error
	for err := range errChan {
		if err != nil {
			errors = append(errors, err)
			progress.Log("Error during repository fetch: %v", err)
		}
	}

	// Mark repositories as completed with final count regardless of errors
	finalCount := int(totalCount.Load())
	progress.MarkItemCompleted("repositories", finalCount)
	
	// Update message based on whether there were errors
	if len(errors) > 0 {
		progress.UpdateMessage(fmt.Sprintf("Completed fetching %d repositories with %d errors", finalCount, len(errors)))
		// Return the first error for upstream handling, but after marking completion
		return errors[0]
	} else {
		progress.UpdateMessage(fmt.Sprintf("Successfully completed fetching %d repositories", finalCount))
	}

	return nil
}

// GraphQLDiscussion represents a GitHub discussion in GraphQL
type GraphQLDiscussion struct {
	ID     string
	Title  string
	Body   string
	URL    string
	Author struct {
		Login string
	}
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PullDiscussions pulls discussions from GitHub using GraphQL with optimal caching and concurrency
func PullDiscussions(ctx context.Context, client *githubv4.Client, db *DB, config *Config, progress *Progress) error {
	allRepositories, err := db.GetRepositories()
	if err != nil {
		return fmt.Errorf("failed to get repositories: %w", err)
	}

	// Filter repositories to only those with discussions enabled and not excluded
	var repositories []Repository
	for _, repo := range allRepositories {
		if repo.HasDiscussionsEnabled && !isRepositoryExcluded(repo.Name, config.ExcludedRepositories) {
			repositories = append(repositories, repo)
		}
	}

	progress.SetCurrentItem("discussions")
	progress.Log("Starting discussions pull for %d repositories (filtered from %d total repositories)", len(repositories), len(allRepositories))
	progress.UpdateMessage(fmt.Sprintf("Preparing to fetch discussions from %d repositories with discussions enabled", len(repositories)))

	// Setup GraphQL request rate measurement
	requestCount := atomic.Int64{}
	stopRateMeasurement := make(chan struct{})

	// Start a goroutine to measure and update request rate every second
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		var lastCount int64

		for {
			select {
			case <-stopRateMeasurement:
				return
			case <-ticker.C:
				currentCount := requestCount.Load()
				requestsInLastSecond := currentCount - lastCount
				lastCount = currentCount

				// Update spinner speed based on request rate
				progress.UpdateRequestRate(int(requestsInLastSecond))
			}
		}
	}()

	// Channels for limiting concurrency and collecting results
	semaphore := make(chan struct{}, 50) // Limit to 50 concurrent repositories
	errChan := make(chan error, len(repositories))
	var wg sync.WaitGroup

	// Atomic counters for statistics
	var totalDiscussionsUpdated int64
	var totalDiscussionsSkipped int64

	// Process repositories in parallel
	for _, repo := range repositories {
		wg.Add(1)
		go func(repo Repository) {
			defer wg.Done()

			// Acquire semaphore slot
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Determine owner and repo name
			var owner, repoName string
			parts := strings.Split(repo.Name, "/")
			if len(parts) ==  2 {
				// If in owner/repo format, use that
				owner, repoName = parts[0], parts[1]
			} else {
				// Otherwise, use organization from config and repo name as is
				owner = config.Organization
				repoName = repo.Name
			}

			progress.UpdateMessage(fmt.Sprintf("Processing repository: %s (owner: %s, repo: %s)", repo.Name, owner, repoName))

			// Get last updated time for this repository to implement caching
			lastUpdated, err := db.GetDiscussionLastUpdated(repo.Name)
			if err != nil {
				errChan <- fmt.Errorf("FATAL: failed to get last updated time for %s: %w", repo.Name, err)
				return
			}

			progress.Log("Processing discussions for repository: %s", repo.Name)

			// Track statistics for this repository
			var repoDiscussionsUpdated int
			var repoDiscussionsSkipped int

			var query struct {
				Repository struct {
					Discussions struct {
						Nodes    []GraphQLDiscussion
						PageInfo struct {
							EndCursor   githubv4.String
							HasNextPage bool
						}
					} `graphql:"discussions(first: 100, after: $cursor, orderBy: $orderBy)"`
				} `graphql:"repository(owner: $owner, name: $name)"`
			}

			variables := map[string]interface{}{
				"owner":  githubv4.String(owner),
				"name":   githubv4.String(repoName),
				"cursor": (*githubv4.String)(nil),
				"orderBy": githubv4.DiscussionOrder{
					Field:     githubv4.DiscussionOrderFieldUpdatedAt,
					Direction: githubv4.OrderDirectionDesc,
				},
			}

			// Note: Avoid transactions, save each discussion immediately as per specification

			pageNum := 1
			for {
				progress.Log("Fetching page %d of discussions for %s", pageNum, repo.Name)
				progress.UpdateMessage(fmt.Sprintf("Fetching discussions for %s (page %d)", repo.Name, pageNum))

				// Use centralized GraphQL error handling
				err := handleGraphQLError(ctx, client, func() error {
					return client.Query(ctx, &query, variables)
				}, "discussions query", pageNum, &requestCount, progress)

				if err != nil {
					// Check if the repository doesn't exist anymore
					if strings.Contains(err.Error(), "Could not resolve to a Repository") {
						// Repository no longer exists, remove it and all associated data from the database
						db.removeRepositoryAndAssociatedData(repo.Name, progress)
						return
					}
					// Not a rate limit error, return the error
					errChan <- fmt.Errorf("failed to query discussions for %s: %w", repo.Name, err)
					return
				}

				nodeCount := len(query.Repository.Discussions.Nodes)
				progress.Log("Successfully fetched page %d, processing %d discussions for %s", pageNum, nodeCount, repo.Name)
				progress.UpdateMessage(fmt.Sprintf("Processing %d discussions for %s", nodeCount, repo.Name))

				// Process discussions individually - save each one immediately
				shouldBreak := false
				for i, d := range query.Repository.Discussions.Nodes {
					if i%20 == 0 && nodeCount > 20 {
						progress.UpdateMessage(fmt.Sprintf("Processing discussion %d/%d for %s", i+1, nodeCount, repo.Name))
					}

					// Stop if this discussion hasn't been updated since we last pulled
					// Since we're ordering by updatedAt DESC, once we hit an old discussion, all subsequent ones will be older
					if !lastUpdated.IsZero() && !d.UpdatedAt.After(lastUpdated) {
						progress.Log("Hit discussion older than last update (%s), stopping for repository %s", lastUpdated.Format(time.RFC3339), repo.Name)
						shouldBreak = true
						break
					}

					// Create discussion object and save immediately
					discussion := Discussion{
						URL:        d.URL,
						Title:      d.Title,
						Body:       d.Body,
						CreatedAt:  d.CreatedAt,
						UpdatedAt:  d.UpdatedAt,
						Repository: repoName, // Store just the repo name without organization prefix
						Author:     d.Author.Login,
					}

					// Save each discussion immediately
					if err := db.SaveDiscussion(&discussion); err != nil {
						errChan <- fmt.Errorf("FATAL: failed to save discussion for %s: %w", repo.Name, err)
						return
					}

					repoDiscussionsUpdated++
					
					// Update global count and progress for each individual discussion
					newTotal := atomic.AddInt64(&totalDiscussionsUpdated, 1)
					progress.UpdateItemCount("discussions", int(newTotal))
				}

				// If we hit old discussions on this page, stop processing this repository
				if shouldBreak {
					progress.Log("Stopping pagination for repository %s due to old discussions", repo.Name)
					break
				}

				// Move to next page if available
				if !query.Repository.Discussions.PageInfo.HasNextPage {
					break
				}

				variables["cursor"] = githubv4.NewString(query.Repository.Discussions.PageInfo.EndCursor)
				pageNum++
			}

			// Update global skipped count
			atomic.AddInt64(&totalDiscussionsSkipped, int64(repoDiscussionsSkipped))

			progress.Log("Repository %s completed: updated %d discussions, skipped %d discussions",
				repo.Name, repoDiscussionsUpdated, repoDiscussionsSkipped)
			progress.UpdateMessage(fmt.Sprintf("Repository %s: updated %d discussions, skipped %d discussions",
				repo.Name, repoDiscussionsUpdated, repoDiscussionsSkipped))
		}(repo)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errChan)
	close(stopRateMeasurement) // Stop rate measurement

	// Check for errors - collect them but don't fail immediately
	var errors []error
	for err := range errChan {
		if err != nil {
			errors = append(errors, err)
			progress.Log("Error during discussions fetch: %v", err)
		}
	}

	// Mark discussions as completed with final count regardless of errors
	finalCount := int(totalDiscussionsUpdated)
	progress.MarkItemCompleted("discussions", finalCount)

	// Update message based on whether there were errors
	if len(errors) > 0 {
		progress.UpdateMessage(fmt.Sprintf("Completed %d discussions with %d errors", finalCount, len(errors)))
		// Return the first error for upstream handling, but after marking completion
		return errors[0]
	} else {
		progress.Log("All discussion repositories processed successfully")
		progress.UpdateMessage(fmt.Sprintf("Successfully completed %d discussions", finalCount))
	}

	return nil
}

// PullIssues pulls issues from GitHub using GraphQL with optimal caching and concurrency
func PullIssues(ctx context.Context, client *githubv4.Client, db *DB, config *Config, progress *Progress) error {
	// Get all repositories in the organization
	allRepositories, err := db.GetRepositories()
	if err != nil {
		return fmt.Errorf("failed to get repositories: %w", err)
	}
	
	// Filter repositories to only include those with issues enabled and not excluded
	var repositories []Repository
	for _, repo := range allRepositories {
		if repo.HasIssuesEnabled && !isRepositoryExcluded(repo.Name, config.ExcludedRepositories) {
			repositories = append(repositories, repo)
		}
	}

	progress.SetCurrentItem("issues")
	progress.UpdateMessage(fmt.Sprintf("Preparing to fetch issues from %d repositories", len(repositories)))

	// Setup GraphQL request rate measurement
	requestCount := atomic.Int64{}
	stopRateMeasurement := make(chan struct{})

	// Start a goroutine to measure and update request rate every second
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		var lastCount int64

		for {
			select {
			case <-stopRateMeasurement:
				return
			case <-ticker.C:
				currentCount := requestCount.Load()
				requestsInLastSecond := currentCount - lastCount
				lastCount = currentCount

				progress.UpdateRequestRate(int(requestsInLastSecond))
			}
		}
	}()

	// Channels for limiting concurrency and collecting results
	semaphore := make(chan struct{}, 50) // Limit to 50 concurrent repositories
	errChan := make(chan error, len(repositories))

	// Atomic counters for statistics
	totalIssues := atomic.Int64{}
	processedRepos := atomic.Int64{}

	// Process repositories in parallel
	var wg sync.WaitGroup
	for _, repo := range repositories {
		wg.Add(1)
		go func(repo Repository) {
			defer wg.Done()

			// Acquire semaphore slot
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			repoProcessed := processedRepos.Add(1)
			progress.UpdateMessage(fmt.Sprintf("Fetching issues for repository %d/%d: %s",
				repoProcessed, len(repositories), repo.Name))

			// Get the most recent issue timestamp for this repository
			lastUpdated, err := db.GetIssueLastUpdated(repo.Name)
			if err != nil {
				errChan <- fmt.Errorf("FATAL: failed to get last updated timestamp for %s: %w", repo.Name, err)
				return
			}

			// Variable for the GraphQL pagination cursor
			var cursor *githubv4.String
			pageNum := 1

			for {
				// Define the GraphQL query variables
				vars := map[string]interface{}{
					"owner":  githubv4.String(config.Organization),
					"name":   githubv4.String(repo.Name),
					"first":  githubv4.Int(100),
					"cursor": cursor,
					"orderBy": githubv4.IssueOrder{
						Field:     githubv4.IssueOrderFieldUpdatedAt,
						Direction: githubv4.OrderDirectionDesc,
					},
				}

				// Define the GraphQL query
				var query struct {
					Repository struct {
						Issues struct {
							Nodes []struct {
								URL       string
								Title     string
								Body      string
								CreatedAt time.Time
								UpdatedAt time.Time
								ClosedAt  *time.Time
								Author    struct {
									Login string
								}
							}
							PageInfo struct {
								HasNextPage bool
								EndCursor   githubv4.String
							}
						} `graphql:"issues(first: $first, after: $cursor, orderBy: $orderBy)"`
					} `graphql:"repository(owner: $owner, name: $name)"`
				}

				// Use centralized GraphQL error handling
				err := handleGraphQLError(ctx, client, func() error {
					return client.Query(ctx, &query, vars)
				}, "issues query", pageNum, &requestCount, progress)

				if err != nil {
					// Check if the repository doesn't exist anymore
					if strings.Contains(err.Error(), "Could not resolve to a Repository") {
						// Repository no longer exists, remove it and all associated data from the database
						db.removeRepositoryAndAssociatedData(repo.Name, progress)
						return
					}

					errChan <- fmt.Errorf("failed to query issues for %s: %w", repo.Name, err)
					return
				}

				// Process issues
				stopProcessing := false
				savedIssuesThisPage := 0
				for _, node := range query.Repository.Issues.Nodes {
					// If we have a lastUpdated time and this issue is older, we can stop
					if !lastUpdated.IsZero() && node.UpdatedAt.Before(lastUpdated) {
						// Reached already processed issues, stopping (no individual logging)
						stopProcessing = true
						break
					}

					// Skip issues older than 400 days but continue processing the page
					cutoffDate := time.Now().AddDate(0, 0, -400)
					if node.UpdatedAt.Before(cutoffDate) {
						// Issue is older than 400 days, skip it but continue (no individual logging)
						continue
					}

					// Save the issue to the database
					issue := Issue{
						URL:        node.URL,
						Title:      node.Title,
						Body:       node.Body,
						CreatedAt:  node.CreatedAt,
						UpdatedAt:  node.UpdatedAt,
						ClosedAt:   node.ClosedAt,
						Repository: repo.Name,
						Author:     node.Author.Login,
					}

					if err := db.SaveIssue(&issue); err != nil {
						errChan <- fmt.Errorf("FATAL: failed to save issue for %s: %w", repo.Name, err)
						return
					}

					newTotal := totalIssues.Add(1)
					
					// Update progress count for each issue
					progress.UpdateItemCount("issues", int(newTotal))
					savedIssuesThisPage++
				}

				// If we processed no issues in this page due to age limits, stop pagination
				if savedIssuesThisPage == 0 && len(query.Repository.Issues.Nodes) > 0 {
					stopProcessing = true
				}

				// Check if we should stop processing due to old issues
				if stopProcessing {
					break
				}

				// Check if there are more pages
				if !query.Repository.Issues.PageInfo.HasNextPage {
					break
				}

				// Update cursor for next page
				cursor = &query.Repository.Issues.PageInfo.EndCursor
				pageNum++
			}
			
			progress.Log("Repository %s completed: processed %d issues", repo.Name, totalIssues.Load())
		}(repo)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errChan)
	close(stopRateMeasurement) // Stop rate measurement

	// Check for errors - collect them but don't fail immediately
	var errors []error
	for err := range errChan {
		if err != nil {
			errors = append(errors, err)
			progress.Log("Error during issues fetch: %v", err)
		}
	}

	// Mark issues as completed with final count regardless of errors
	finalCount := int(totalIssues.Load())
	progress.MarkItemCompleted("issues", finalCount)

	// Update message based on whether there were errors
	if len(errors) > 0 {
		progress.UpdateMessage(fmt.Sprintf("Completed %d issues with %d errors", finalCount, len(errors)))
		// Return the first error for upstream handling, but after marking completion
		return errors[0]
	} else {
		progress.UpdateMessage(fmt.Sprintf("Successfully pulled %d issues from %d repositories",
			finalCount, len(repositories)))
	}

	return nil
}

// PullPullRequests pulls pull requests from GitHub using GraphQL with optimal caching and concurrency
func PullPullRequests(ctx context.Context, client *githubv4.Client, db *DB, config *Config, progress *Progress) error {
	progress.Log("Starting PullPullRequests function")
	
	// Get all repositories in the organization
	progress.Log("Getting repositories from database")
	allRepositories, err := db.GetRepositories()
	if err != nil {
		return fmt.Errorf("failed to get repositories: %w", err)
	}

	// Filter repositories to exclude those in ExcludedRepositories
	var repositories []Repository
	for _, repo := range allRepositories {
		if !isRepositoryExcluded(repo.Name, config.ExcludedRepositories) {
			repositories = append(repositories, repo)
		}
	}

	progress.Log("Found %d repositories to process (filtered from %d total repositories)", len(repositories), len(allRepositories))

	if len(repositories) == 0 {
		progress.Log("No repositories found. Run with 'repositories' item first to populate database.")
		progress.MarkItemCompleted("pull-requests", 0)
		return nil
	}

	progress.SetCurrentItem("pull-requests")
	progress.UpdateMessage(fmt.Sprintf("Preparing to fetch pull requests from %d repositories", len(repositories)))

	// Setup GraphQL request rate measurement
	requestCount := atomic.Int64{}
	stopRateMeasurement := make(chan struct{})

	// Start a goroutine to measure and update request rate every second
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		var lastCount int64

		for {
			select {
			case <-stopRateMeasurement:
				return
			case <-ticker.C:
				currentCount := requestCount.Load()
				requestsInLastSecond := currentCount - lastCount
				lastCount = currentCount

				progress.UpdateRequestRate(int(requestsInLastSecond))
			}
		}
	}()

	// Channels for limiting concurrency and collecting results
	semaphore := make(chan struct{}, 50) // Limit to 50 concurrent repositories
	errChan := make(chan error, len(repositories))

	// Atomic counters for statistics
	totalPullRequests := atomic.Int64{}
	processedRepos := atomic.Int64{}

	// Process repositories in parallel
	var wg sync.WaitGroup
	for _, repo := range repositories {
		wg.Add(1)
		go func(repo Repository) {
			defer wg.Done()

			// Acquire semaphore slot
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			repoProcessed := processedRepos.Add(1)
			progress.Log("Processing pull requests for repository %d/%d: %s", repoProcessed, len(repositories), repo.Name)
			progress.UpdateMessage(fmt.Sprintf("Fetching pull requests for repository %d/%d: %s",
				repoProcessed, len(repositories), repo.Name))

			// Get the most recent pull request timestamp for this repository
			lastUpdated, err := db.GetPullRequestLastUpdated(repo.Name)
			if err != nil {
				errChan <- fmt.Errorf("FATAL: failed to get last updated timestamp for %s: %w", repo.Name, err)
				return
			}

			// Variable for the GraphQL pagination cursor
			var cursor *githubv4.String
			pageNum := 1

			for {
				progress.Log("Fetching page %d of pull requests for %s", pageNum, repo.Name)
				
				// Define the GraphQL query variables
				vars := map[string]interface{}{
					"owner":  githubv4.String(config.Organization),
					"name":   githubv4.String(repo.Name),
					"first":  githubv4.Int(100),
					"cursor": cursor,
				}

				// Define the GraphQL query
				var query struct {
					Repository struct {
						PullRequests struct {
							Nodes []struct {
								URL       string     `graphql:"url"`
								Title     string     `graphql:"title"`
								Body      string     `graphql:"body"`
								CreatedAt time.Time  `graphql:"createdAt"`
								UpdatedAt time.Time  `graphql:"updatedAt"`
								ClosedAt  *time.Time `graphql:"closedAt"`
								MergedAt  *time.Time `graphql:"mergedAt"`
								Author    struct {
									Login string `graphql:"login"`
								} `graphql:"author"`
							} `graphql:"nodes"`
							PageInfo struct {
								HasNextPage githubv4.Boolean
								EndCursor   githubv4.String
							} `graphql:"pageInfo"`
						} `graphql:"pullRequests(first: $first, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC})"`
					} `graphql:"repository(owner: $owner, name: $name)"`
				}

				// Use centralized GraphQL error handling
				err := handleGraphQLError(ctx, client, func() error {
					return client.Query(ctx, &query, vars)
				}, "pull requests query", pageNum, &requestCount, progress)

				if err != nil {
					// Check if it's a repository not found error
					if strings.Contains(err.Error(), "Could not resolve to a Repository") {
						// Repository doesn't exist, remove it and all associated data from the database
						db.removeRepositoryAndAssociatedData(repo.Name, progress)
						return
					}
					errChan <- fmt.Errorf("failed to query pull requests for %s: %w", repo.Name, err)
					return
				}

				pullRequests := query.Repository.PullRequests.Nodes
				progress.Log("Successfully fetched page %d, processing %d pull requests for %s", pageNum, len(pullRequests), repo.Name)
				
				// Process pull requests from this page
				stopProcessing := false
				
				for _, prNode := range pullRequests {
					// If we've encountered a pull request older than our last update, stop processing
					if !lastUpdated.IsZero() && prNode.UpdatedAt.Before(lastUpdated) {
						stopProcessing = true
						break
					}
					
					// Only pull pull requests from the last 400 days
					cutoffDate := time.Now().AddDate(0, 0, -400)
					if prNode.UpdatedAt.Before(cutoffDate) {
						// Pull request is older than 400 days, stop pulling (no individual logging)
						stopProcessing = true
						break
					}

					// Create PullRequest struct
					pr := PullRequest{
						URL:        prNode.URL,
						Title:      prNode.Title,
						Body:       prNode.Body,
						CreatedAt:  prNode.CreatedAt,
						UpdatedAt:  prNode.UpdatedAt,
						ClosedAt:   prNode.ClosedAt,
						MergedAt:   prNode.MergedAt,
						Repository: repo.Name,
						Author:     prNode.Author.Login,
					}

					// Save pull request to database
					if err := db.SavePullRequest(&pr); err != nil {
						errChan <- fmt.Errorf("FATAL: failed to save pull request %s: %w", pr.URL, err)
						return
					}

					newTotal := totalPullRequests.Add(1)
					
					// Update progress count for each pull request
					progress.UpdateItemCount("pull-requests", int(newTotal))
				}

				// Update cursor for next page
				cursor = &query.Repository.PullRequests.PageInfo.EndCursor

				// Stop if we've processed all newer pull requests or there are no more pages
				if stopProcessing {
					break
				}
				if !query.Repository.PullRequests.PageInfo.HasNextPage {
					break
				}
				pageNum++
			}
			
			progress.Log("Repository %s completed: processed pull requests", repo.Name)
		}(repo)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errChan)
	close(stopRateMeasurement) // Stop rate measurement

	// Check for errors - collect them but don't fail immediately
	var errors []error
	for err := range errChan {
		if err != nil {
			errors = append(errors, err)
			progress.Log("Error during pull requests fetch: %v", err)
		}
	}

	// Mark pull requests as completed with final count regardless of errors
	finalCount := int(totalPullRequests.Load())
	progress.MarkItemCompleted("pull-requests", finalCount)

	// Update message based on whether there were errors
	if len(errors) > 0 {
		progress.UpdateMessage(fmt.Sprintf("Completed %d pull requests with %d errors", finalCount, len(errors)))
		// Return the first error for upstream handling, but after marking completion
		return errors[0]
	} else {
		progress.Log("All pull request repositories processed successfully")
		progress.UpdateMessage(fmt.Sprintf("Successfully pulled %d pull requests from %d repositories",
			finalCount, len(repositories)))
	}

	return nil
}

// GetDiscussionByID gets a specific discussion by its URL
func (db *DB) GetDiscussionByID(discussionURL string) (*Discussion, error) {
	query := fmt.Sprintf(`
		SELECT url, title, body, author, created_at, updated_at, repository
		FROM %s
		WHERE url = ?
	`, getTableName("discussions"))
	row := db.QueryRow(query, discussionURL)

	var discussion Discussion
	err := row.Scan(
		&discussion.URL,
		&discussion.Title,
		&discussion.Body,
		&discussion.Author,
		&discussion.CreatedAt,
		&discussion.UpdatedAt,
		&discussion.Repository,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("discussion with URL %s not found", discussionURL)
		}
		return nil, fmt.Errorf("failed to scan discussion: %w", err)
	}

	return &discussion, nil
}

// GetDiscussionsByRepository gets all discussions for a specific repository
func (db *DB) GetDiscussionsByRepository(repositoryName string) ([]Discussion, error) {
	query := fmt.Sprintf(`
		SELECT url, title, body, author, created_at, updated_at, repository
		FROM %s
		WHERE repository = ?
	`, getTableName("discussions"))
	rows, err := db.Query(query, repositoryName)
	if err != nil {
		return nil, fmt.Errorf("failed to query discussions: %w", err)
	}
	defer rows.Close()

	var discussions []Discussion
	for rows.Next() {
		var discussion Discussion
		if err := rows.Scan(
			&discussion.URL,
			&discussion.Title,
			&discussion.Body,
			&discussion.Author,
			&discussion.CreatedAt,
			&discussion.UpdatedAt,
			&discussion.Repository,
		); err != nil {
			return nil, fmt.Errorf("failed to scan discussion: %w", err)
		}
		discussions = append(discussions, discussion)
	}

	return discussions, nil
}

// parseRFC3339Date safely parses RFC3339 date strings for MCP handlers
func parseRFC3339Date(dateStr, fieldName string) (time.Time, error) {
	if dateStr == "" {
		return time.Time{}, nil
	}
	parsedTime, err := time.Parse(time.RFC3339, dateStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s date format, use RFC3339 format (YYYY-MM-DDTHH:MM:SSZ): %v", fieldName, err)
	}
	return parsedTime, nil
}

// parseRFC3339DatePtr safely parses RFC3339 date strings and returns a pointer for MCP handlers
func parseRFC3339DatePtr(dateStr, fieldName string) (*time.Time, error) {
	if dateStr == "" {
		return nil, nil
	}
	parsedTime, err := parseRFC3339Date(dateStr, fieldName)
	if err != nil {
		return nil, err
	}
	return &parsedTime, nil
}

// validateFields validates that all requested fields are available for the given type
func validateFields(fields []string, availableFields []string, fieldType string) error {
	if len(fields) == 0 {
		return nil // Default to all fields
	}
	
	availableSet := make(map[string]bool)
	for _, field := range availableFields {
		availableSet[field] = true
	}
	
	var invalidFields []string
	for _, field := range fields {
		if !availableSet[field] {
			invalidFields = append(invalidFields, field)
		}
	}
	
	if len(invalidFields) > 0 {
		return fmt.Errorf("invalid fields: %s\n\nUse one of the available fields: %s", 
			strings.Join(invalidFields, ", "), 
			strings.Join(availableFields, ", "))
	}
	
	return nil
}

// shouldIncludeField checks if a field should be included based on the fields filter
func shouldIncludeField(fieldName string, fields []string) bool {
	if len(fields) == 0 {
		return true // Include all fields if no filter specified
	}
	
	for _, field := range fields {
		if field == fieldName {
			return true
		}
	}
	return false
}




// RunMCPServer runs the MCP server using the mcp-go library
func RunMCPServer(db *DB) error {
	// Load organization from environment variable
	organization := os.Getenv("ORGANIZATION")
	if organization == "" {
		return fmt.Errorf("ORGANIZATION environment variable is required for MCP server")
	}

	// Create SearchEngine instance for unified search functionality
	searchEngine := NewSearchEngine(db)

	// Create a new MCP server - enable both tool and prompt capabilities
	s := server.NewMCPServer(
		"GitHub Offline MCP Server",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithPromptCapabilities(true),
	)

	// Register the list_discussions tool
	listDiscussionsTool := mcp.NewTool("list_discussions",
		mcp.WithDescription("Lists discussions with optional filtering. Discussions are separated by `---`."),
		mcp.WithString("repository",
			mcp.Description("Filter by repository name. Example: auth-service. Defaults to any repository in the organization."),
		),
		mcp.WithString("created_from",
			mcp.Description("Filter by created_at after the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("created_to",
			mcp.Description("Filter by created_at before the specified date. Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithArray("authors",
			mcp.Description("Array of author usernames. Example: [john_doe, jane_doe]. Defaults to any author."),
			mcp.Items(map[string]interface{}{
				"type": "string",
			}),
		),
		mcp.WithArray("fields",
			mcp.Description("Array of fields to include in the response. Available fields: [\"title\", \"url\", \"repository\", \"created_at\", \"author\", \"body\"]. Defaults to all fields."),
			mcp.Items(map[string]interface{}{
				"type": "string",
			}),
		),
	)

	// Add tool handler for list_discussions
	s.AddTool(listDiscussionsTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract parameters using the library's parameter methods
		repository := request.GetString("repository", "")
		createdFromStr := request.GetString("created_from", "")
		createdToStr := request.GetString("created_to", "")
		authors := request.GetStringSlice("authors", []string{})
		fields := request.GetStringSlice("fields", []string{})

		// Validate fields parameter
		availableFields := []string{"title", "url", "repository", "created_at", "author", "body"}
		if err := validateFields(fields, availableFields, "discussions"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Parse dates using helper functions
		createdFromTime, err := parseRFC3339Date(createdFromStr, "created_from")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		createdToTime, err := parseRFC3339Date(createdToStr, "created_to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Get discussions
		discussions, err := db.GetDiscussions(repository, createdFromTime, createdToTime, authors)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get discussions: %v", err)), nil
		}

		// If no discussions found, return specific message
		if len(discussions) == 0 {
			return mcp.NewToolResultText("No discussions found."), nil
		}

		// Format discussions according to spec
		var result strings.Builder
		responseSize := 0
		discussionsShown := 0
		maxResponseSize := 990 * 1024 // 990 KB
		truncated := false

		// First pass: count how many discussions we can fit
		for i, discussion := range discussions {
			// Format discussion with field filtering
			var formatted strings.Builder
			
			if shouldIncludeField("title", fields) {
				formatted.WriteString(fmt.Sprintf("## %s\n\n", discussion.Title))
			}
			
			if shouldIncludeField("url", fields) {
				formatted.WriteString(fmt.Sprintf("- URL: %s\n", discussion.URL))
			}
			if shouldIncludeField("repository", fields) {
				formatted.WriteString(fmt.Sprintf("- Repository: %s\n", discussion.Repository))
			}
			if shouldIncludeField("created_at", fields) {
				formatted.WriteString(fmt.Sprintf("- Created at: %s\n", discussion.CreatedAt.Format(time.RFC3339)))
			}
			if shouldIncludeField("author", fields) {
				formatted.WriteString(fmt.Sprintf("- Author: %s\n", discussion.Author))
			}
			
			formatted.WriteString("\n")
			
			if shouldIncludeField("body", fields) {
				formatted.WriteString(fmt.Sprintf("%s\n", discussion.Body))
			}
			
			formatted.WriteString("\n---\n\n")
			formattedStr := formatted.String()

			// Check if adding this discussion would exceed the limit
			if responseSize+len(formattedStr) > maxResponseSize && discussionsShown > 0 {
				truncated = true
				break
			}

			responseSize += len(formattedStr)
			discussionsShown = i + 1
		}

		// If truncated, prepend the warning message
		if truncated {
			remaining := len(discussions) - discussionsShown
			result.WriteString(fmt.Sprintf("Showing only the first %d discussions. There's %d more, please refine your search. Use `created_from` and `created_to` parameters to narrow the results.\n\n---\n\n",
				discussionsShown, remaining))
		}

		// Now format the discussions we can fit
		for i := 0; i < discussionsShown; i++ {
			discussion := discussions[i]
			
			if shouldIncludeField("title", fields) {
				result.WriteString(fmt.Sprintf("## %s\n\n", discussion.Title))
			}
			
			if shouldIncludeField("url", fields) {
				result.WriteString(fmt.Sprintf("- URL: %s\n", discussion.URL))
			}
			if shouldIncludeField("repository", fields) {
				result.WriteString(fmt.Sprintf("- Repository: %s\n", discussion.Repository))
			}
			if shouldIncludeField("created_at", fields) {
				result.WriteString(fmt.Sprintf("- Created at: %s\n", discussion.CreatedAt.Format(time.RFC3339)))
			}
			if shouldIncludeField("author", fields) {
				result.WriteString(fmt.Sprintf("- Author: %s\n", discussion.Author))
			}
			
			result.WriteString("\n")
			
			if shouldIncludeField("body", fields) {
				result.WriteString(fmt.Sprintf("%s\n", discussion.Body))
			}
			
			result.WriteString("\n---\n\n")
		}

		return mcp.NewToolResultText(result.String()), nil
	})

	// Register the list_issues tool
	listIssuesTool := mcp.NewTool("list_issues",
		mcp.WithDescription("Lists issues with optional filtering."),
		mcp.WithString("repository",
			mcp.Description("Filter by repository name. Example: auth-service. Defaults to any repository in the organization."),
		),
		mcp.WithString("created_from",
			mcp.Description("Filter by created_at after the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("created_to",
			mcp.Description("Filter by created_at before the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("closed_from",
			mcp.Description("Filter by closed_at after the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("closed_to",
			mcp.Description("Filter by closed_at before the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithArray("authors",
			mcp.Description("Array of author usernames. Example: [john_doe, jane_doe]. Defaults to any author."),
			mcp.Items(map[string]interface{}{
				"type": "string",
			}),
		),
		mcp.WithArray("fields",
			mcp.Description("Array of fields to include in the response. Available fields: [\"title\", \"url\", \"repository\", \"created_at\", \"closed_at\", \"author\", \"status\", \"body\"]. Defaults to all fields."),
			mcp.Items(map[string]interface{}{
				"type": "string",
			}),
		),
	)

	// Add tool handler for list_issues
	s.AddTool(listIssuesTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract parameters using the library's parameter methods
		repository := request.GetString("repository", "")
		createdFromStr := request.GetString("created_from", "")
		createdToStr := request.GetString("created_to", "")
		closedFromStr := request.GetString("closed_from", "")
		closedToStr := request.GetString("closed_to", "")
		authors := request.GetStringSlice("authors", []string{})
		fields := request.GetStringSlice("fields", []string{})

		// Validate fields parameter
		availableFields := []string{"title", "url", "repository", "created_at", "closed_at", "author", "status", "body"}
		if err := validateFields(fields, availableFields, "issues"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Parse dates using helper functions
		createdFromTime, err := parseRFC3339Date(createdFromStr, "created_from")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		createdToTime, err := parseRFC3339Date(createdToStr, "created_to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		closedFromTime, err := parseRFC3339DatePtr(closedFromStr, "closed_from")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		closedToTime, err := parseRFC3339DatePtr(closedToStr, "closed_to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Get issues
		issues, err := db.GetIssues(repository, createdFromTime, createdToTime, closedFromTime, closedToTime, authors)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get issues: %v", err)), nil
		}

		// If no issues found, return specific message
		if len(issues) == 0 {
			return mcp.NewToolResultText("No issues found."), nil
		}

		// Format issues according to spec
		var result strings.Builder
		responseSize := 0
		issuesShown := 0
		maxResponseSize := 990 * 1024 // 990 KB
		truncated := false

		// First pass: count how many issues we can fit
		for i, issue := range issues {
			// Determine status
			status := "open"
			closedAtStr := ""
			if issue.ClosedAt != nil {
				status = "closed"
				closedAtStr = issue.ClosedAt.Format(time.RFC3339)
			} else {
				closedAtStr = ""
			}

			// Format issue with field filtering
			var formatted strings.Builder
			
			if shouldIncludeField("title", fields) {
				formatted.WriteString(fmt.Sprintf("## %s\n\n", issue.Title))
			}
			
			if shouldIncludeField("url", fields) {
				formatted.WriteString(fmt.Sprintf("- URL: %s\n", issue.URL))
			}
			if shouldIncludeField("repository", fields) {
				formatted.WriteString(fmt.Sprintf("- Repository: %s\n", issue.Repository))
			}
			if shouldIncludeField("created_at", fields) {
				formatted.WriteString(fmt.Sprintf("- Created at: %s\n", issue.CreatedAt.Format(time.RFC3339)))
			}
			if shouldIncludeField("closed_at", fields) {
				formatted.WriteString(fmt.Sprintf("- Closed at: %s\n", closedAtStr))
			}
			if shouldIncludeField("author", fields) {
				formatted.WriteString(fmt.Sprintf("- Author: %s\n", issue.Author))
			}
			if shouldIncludeField("status", fields) {
				formatted.WriteString(fmt.Sprintf("- Status: %s\n", status))
			}
			
			formatted.WriteString("\n")
			
			if shouldIncludeField("body", fields) {
				formatted.WriteString(fmt.Sprintf("%s\n", issue.Body))
			}
			
			formatted.WriteString("\n---\n\n")
			formattedStr := formatted.String()

			// Check if adding this issue would exceed the limit
			if responseSize+len(formattedStr) > maxResponseSize && issuesShown > 0 {
				truncated = true
				break
			}

			responseSize += len(formattedStr)
			issuesShown = i + 1
		}

		// If truncated, prepend the warning message
		if truncated {
			remaining := len(issues) - issuesShown
			result.WriteString(fmt.Sprintf("Showing only the first %d issues. There's %d more, please refine your search.\n\n---\n\n",
				issuesShown, remaining))
		}

		// Now format the issues we can fit
		for i := 0; i < issuesShown; i++ {
			issue := issues[i]
			// Determine status
			status := "open"
			closedAtStr := ""
			if issue.ClosedAt != nil {
				status = "closed"
				closedAtStr = issue.ClosedAt.Format(time.RFC3339)
			} else {
				closedAtStr = ""
			}

			if shouldIncludeField("title", fields) {
				result.WriteString(fmt.Sprintf("## %s\n\n", issue.Title))
			}
			
			if shouldIncludeField("url", fields) {
				result.WriteString(fmt.Sprintf("- URL: %s\n", issue.URL))
			}
			if shouldIncludeField("repository", fields) {
				result.WriteString(fmt.Sprintf("- Repository: %s\n", issue.Repository))
			}
			if shouldIncludeField("created_at", fields) {
				result.WriteString(fmt.Sprintf("- Created at: %s\n", issue.CreatedAt.Format(time.RFC3339)))
			}
			if shouldIncludeField("closed_at", fields) {
				result.WriteString(fmt.Sprintf("- Closed at: %s\n", closedAtStr))
			}
			if shouldIncludeField("author", fields) {
				result.WriteString(fmt.Sprintf("- Author: %s\n", issue.Author))
			}
			if shouldIncludeField("status", fields) {
				result.WriteString(fmt.Sprintf("- Status: %s\n", status))
			}
			
			result.WriteString("\n")
			
			if shouldIncludeField("body", fields) {
				result.WriteString(fmt.Sprintf("%s\n", issue.Body))
			}
			
			result.WriteString("\n---\n\n")
		}

		return mcp.NewToolResultText(result.String()), nil
	})

	// Register the list_pull_requests tool
	listPullRequestsTool := mcp.NewTool("list_pull_requests",
		mcp.WithDescription("Lists pull requests with optional filtering."),
		mcp.WithString("repository",
			mcp.Description("Filter by repository name. Example: auth-service. Defaults to any repository in the organization."),
		),
		mcp.WithString("created_from",
			mcp.Description("Filter by created_at after the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("created_to",
			mcp.Description("Filter by created_at before the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("closed_from",
			mcp.Description("Filter by closed_at after the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("closed_to",
			mcp.Description("Filter by closed_at before the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("merged_from",
			mcp.Description("Filter by merged_at after the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithString("merged_to",
			mcp.Description("Filter by merged_at before the specified date (RFC3339 format). Example: 2025-06-18T19:19:08Z. Defaults to any date."),
		),
		mcp.WithArray("authors",
			mcp.Description("Array of author usernames. Example: [john_doe, jane_doe]. Defaults to any author."),
			mcp.Items(map[string]interface{}{
				"type": "string",
			}),
		),
		mcp.WithArray("fields",
			mcp.Description("Array of fields to include in the response. Available fields: [\"title\", \"url\", \"repository\", \"created_at\", \"merged_at\", \"closed_at\", \"author\", \"status\", \"body\"]. Defaults to all fields."),
			mcp.Items(map[string]interface{}{
				"type": "string",
			}),
		),
	)

	// Add tool handler for list_pull_requests
	s.AddTool(listPullRequestsTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract parameters using the library's parameter methods
		repository := request.GetString("repository", "")
		createdFromStr := request.GetString("created_from", "")
		createdToStr := request.GetString("created_to", "")
		closedFromStr := request.GetString("closed_from", "")
		closedToStr := request.GetString("closed_to", "")
		mergedFromStr := request.GetString("merged_from", "")
		mergedToStr := request.GetString("merged_to", "")
		authors := request.GetStringSlice("authors", []string{})
		fieldsArray := request.GetStringSlice("fields", []string{"title", "url", "repository", "created_at", "merged_at", "closed_at", "author", "status", "body"})

		// Define available fields for validation
		validFields := map[string]bool{
			"title": true, "url": true, "repository": true, "created_at": true,
			"merged_at": true, "closed_at": true, "author": true, "status": true, "body": true,
		}

		// Validate fields parameter
		var invalidFields []string
		fieldsToInclude := make(map[string]bool)
		
		for _, field := range fieldsArray {
			fieldName := strings.TrimSpace(field)
			if fieldName != "" {
				if !validFields[fieldName] {
					invalidFields = append(invalidFields, fieldName)
				} else {
					fieldsToInclude[fieldName] = true
				}
			}
		}

		// Return error if invalid fields are found
		if len(invalidFields) > 0 {
			availableFields := []string{"title", "url", "repository", "created_at", "merged_at", "closed_at", "author", "status", "body"}
			errorMsg := fmt.Sprintf("Invalid fields: %s\n\nUse one of the available fields: %s",
				strings.Join(invalidFields, ", "),
				strings.Join(availableFields, ", "))
			return mcp.NewToolResultError(errorMsg), nil
		}

		// Parse dates using helper functions
		createdFromTime, err := parseRFC3339Date(createdFromStr, "created_from")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		createdToTime, err := parseRFC3339Date(createdToStr, "created_to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		closedFromTime, err := parseRFC3339DatePtr(closedFromStr, "closed_from")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		closedToTime, err := parseRFC3339DatePtr(closedToStr, "closed_to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		mergedFromTime, err := parseRFC3339DatePtr(mergedFromStr, "merged_from")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		mergedToTime, err := parseRFC3339DatePtr(mergedToStr, "merged_to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Get pull requests
		pullRequests, err := db.GetPullRequests(repository, createdFromTime, createdToTime, closedFromTime, closedToTime, mergedFromTime, mergedToTime, authors)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get pull requests: %v", err)), nil
		}

		// If no pull requests found, return specific message
		if len(pullRequests) == 0 {
			return mcp.NewToolResultText("No pull requests found."), nil
		}

		// Format pull requests according to spec
		var result strings.Builder
		responseSize := 0
		pullRequestsShown := 0
		maxResponseSize := 990 * 1024 // 990 KB
		truncated := false

		// Add total count according to spec
		totalCount := len(pullRequests)
		totalLine := fmt.Sprintf("Total %d pull requests found.\n\n", totalCount)
		result.WriteString(totalLine)
		responseSize += len(totalLine)

		// First pass: count how many pull requests we can fit
		for i, pr := range pullRequests {
			// Determine status
			status := "open"
			if pr.ClosedAt != nil {
				status = "closed"
			}
			
			// Format merged_at
			mergedAtStr := ""
			if pr.MergedAt != nil {
				mergedAtStr = pr.MergedAt.Format(time.RFC3339)
			}
			
			// Format closed_at
			closedAtStr := ""
			if pr.ClosedAt != nil {
				closedAtStr = pr.ClosedAt.Format(time.RFC3339)
			}

			// Build formatted output based on selected fields
			var formatted strings.Builder
			
			// Title is always shown as the header if included
			if fieldsToInclude["title"] {
				formatted.WriteString(fmt.Sprintf("## %s\n\n", pr.Title))
			} else {
				formatted.WriteString("## [Title not included]\n\n")
			}
			
			// Add fields conditionally based on fieldsToInclude
			if fieldsToInclude["url"] {
				formatted.WriteString(fmt.Sprintf("- URL: %s\n", pr.URL))
			}
			if fieldsToInclude["repository"] {
				formatted.WriteString(fmt.Sprintf("- Repository: %s\n", pr.Repository))
			}
			if fieldsToInclude["created_at"] {
				formatted.WriteString(fmt.Sprintf("- Created at: %s\n", pr.CreatedAt.Format(time.RFC3339)))
			}
			if fieldsToInclude["merged_at"] {
				formatted.WriteString(fmt.Sprintf("- Merged at: %s\n", mergedAtStr))
			}
			if fieldsToInclude["closed_at"] {
				formatted.WriteString(fmt.Sprintf("- Closed at: %s\n", closedAtStr))
			}
			if fieldsToInclude["author"] {
				formatted.WriteString(fmt.Sprintf("- Author: %s\n", pr.Author))
			}
			if fieldsToInclude["status"] {
				formatted.WriteString(fmt.Sprintf("- Status: %s\n", status))
			}
			
			formatted.WriteString("\n")
			
			if fieldsToInclude["body"] {
				formatted.WriteString(fmt.Sprintf("%s\n", pr.Body))
			}
			
			formatted.WriteString("\n---\n\n")
			
			formattedStr := formatted.String()

			// Check if adding this pull request would exceed the limit
			if responseSize+len(formattedStr) > maxResponseSize && pullRequestsShown > 0 {
				truncated = true
				break
			}

			responseSize += len(formattedStr)
			pullRequestsShown = i + 1
		}

		// If truncated, prepend the warning message
		if truncated {
			remaining := len(pullRequests) - pullRequestsShown
			truncationWarning := fmt.Sprintf("Showing only the first %d pull requests. There's %d more, please refine your search.\n\n---\n\n",
				pullRequestsShown, remaining)
			
			// Insert truncation warning after the total count
			finalResult := strings.Builder{}
			finalResult.WriteString(totalLine)
			finalResult.WriteString(truncationWarning)
			
			// Add the pull requests we can fit
			for i := 0; i < pullRequestsShown; i++ {
				pr := pullRequests[i]
				// Determine status
				status := "open"
				if pr.ClosedAt != nil {
					status = "closed"
				}
				
				// Format merged_at
				mergedAtStr := ""
				if pr.MergedAt != nil {
					mergedAtStr = pr.MergedAt.Format(time.RFC3339)
				}
				
				// Format closed_at
				closedAtStr := ""
				if pr.ClosedAt != nil {
					closedAtStr = pr.ClosedAt.Format(time.RFC3339)
				}

				// Build formatted output based on selected fields
				var formatted strings.Builder
				
				// Title is always shown as the header if included
				if fieldsToInclude["title"] {
					formatted.WriteString(fmt.Sprintf("## %s\n\n", pr.Title))
				} else {
					formatted.WriteString("## [Title not included]\n\n")
				}
				
				// Add fields conditionally based on fieldsToInclude
				if fieldsToInclude["url"] {
					formatted.WriteString(fmt.Sprintf("- URL: %s\n", pr.URL))
				}
				if fieldsToInclude["repository"] {
					formatted.WriteString(fmt.Sprintf("- Repository: %s\n", pr.Repository))
				}
				if fieldsToInclude["created_at"] {
					formatted.WriteString(fmt.Sprintf("- Created at: %s\n", pr.CreatedAt.Format(time.RFC3339)))
				}
				if fieldsToInclude["merged_at"] {
					formatted.WriteString(fmt.Sprintf("- Merged at: %s\n", mergedAtStr))
				}
				if fieldsToInclude["closed_at"] {
					formatted.WriteString(fmt.Sprintf("- Closed at: %s\n", closedAtStr))
				}
				if fieldsToInclude["author"] {
					formatted.WriteString(fmt.Sprintf("- Author: %s\n", pr.Author))
				}
				if fieldsToInclude["status"] {
					formatted.WriteString(fmt.Sprintf("- Status: %s\n", status))
				}
				
				formatted.WriteString("\n")
				
				if fieldsToInclude["body"] {
					formatted.WriteString(fmt.Sprintf("%s\n", pr.Body))
				}
				
				formatted.WriteString("\n---\n\n")
				
				finalResult.WriteString(formatted.String())
			}
			
			return mcp.NewToolResultText(finalResult.String()), nil
		}

		// Now format the pull requests we can fit (when not truncated)
		for i := 0; i < pullRequestsShown; i++ {
			pr := pullRequests[i]
			// Determine status
			status := "open"
			if pr.ClosedAt != nil {
				status = "closed"
			}
			
			// Format merged_at
			mergedAtStr := ""
			if pr.MergedAt != nil {
				mergedAtStr = pr.MergedAt.Format(time.RFC3339)
			}
			
			// Format closed_at
			closedAtStr := ""
			if pr.ClosedAt != nil {
				closedAtStr = pr.ClosedAt.Format(time.RFC3339)
			}

			// Build formatted output based on selected fields
			var formatted strings.Builder
			
			// Title is always shown as the header if included
			if fieldsToInclude["title"] {
				formatted.WriteString(fmt.Sprintf("## %s\n\n", pr.Title))
			} else {
				formatted.WriteString("## [Title not included]\n\n")
			}
			
			// Add fields conditionally based on fieldsToInclude
			if fieldsToInclude["url"] {
				formatted.WriteString(fmt.Sprintf("- URL: %s\n", pr.URL))
			}
			if fieldsToInclude["repository"] {
				formatted.WriteString(fmt.Sprintf("- Repository: %s\n", pr.Repository))
			}
			if fieldsToInclude["created_at"] {
				formatted.WriteString(fmt.Sprintf("- Created at: %s\n", pr.CreatedAt.Format(time.RFC3339)))
			}
			if fieldsToInclude["merged_at"] {
				formatted.WriteString(fmt.Sprintf("- Merged at: %s\n", mergedAtStr))
			}
			if fieldsToInclude["closed_at"] {
				formatted.WriteString(fmt.Sprintf("- Closed at: %s\n", closedAtStr))
			}
			if fieldsToInclude["author"] {
				formatted.WriteString(fmt.Sprintf("- Author: %s\n", pr.Author))
			}
			if fieldsToInclude["status"] {
				formatted.WriteString(fmt.Sprintf("- Status: %s\n", status))
			}
			
			formatted.WriteString("\n")
			
			if fieldsToInclude["body"] {
				formatted.WriteString(fmt.Sprintf("%s\n", pr.Body))
			}
			
			formatted.WriteString("\n---\n\n")
			
			result.WriteString(formatted.String())
		}

		return mcp.NewToolResultText(result.String()), nil
	})

	// Register the search tool
	searchTool := mcp.NewTool("search",
		mcp.WithDescription("Full-text search across discussions, issues, and pull requests."),
		mcp.WithString("query",
			mcp.Description("Search query string. Example: authentication bug."),
			mcp.Required(),
		),
		mcp.WithArray("fields",
			mcp.Description("Array of fields to include in the response. Available fields: [\"title\", \"url\", \"repository\", \"created_at\", \"author\", \"type\", \"state\", \"body\"]. Defaults to all fields."),
			mcp.Items(map[string]interface{}{
				"type": "string",
			}),
		),
	)

	// Add tool handler for search
	s.AddTool(searchTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract parameters
		query := request.GetString("query", "")
		if query == "" {
			return mcp.NewToolResultError("query parameter is required"), nil
		}
		fields := request.GetStringSlice("fields", []string{})

		// Validate fields parameter
		availableFields := []string{"title", "url", "repository", "created_at", "author", "type", "state", "body"}
		if err := validateFields(fields, availableFields, "search results"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Set default fields if none provided
		fieldsToInclude := make(map[string]bool)
		if len(fields) == 0 {
			for _, field := range availableFields {
				fieldsToInclude[field] = true
			}
		} else {
			for _, field := range fields {
				fieldsToInclude[field] = true
			}
		}

		// Use unified SearchEngine for consistent results
		searchResults, err := searchEngine.Search(query, 10)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search query failed: %v", err)), nil
		}

		var result strings.Builder
		hasResults := len(searchResults) > 0

		for _, searchResult := range searchResults {
			var formatted strings.Builder
			formatted.WriteString(fmt.Sprintf("## %s\n\n", searchResult.Title))

			if fieldsToInclude["url"] {
				formatted.WriteString(fmt.Sprintf("- URL: %s\n", searchResult.URL))
			}
			if fieldsToInclude["type"] {
				formatted.WriteString(fmt.Sprintf("- Type: %s\n", searchResult.Type))
			}
			if fieldsToInclude["repository"] {
				formatted.WriteString(fmt.Sprintf("- Repository: %s\n", searchResult.Repository))
			}
			if fieldsToInclude["created_at"] {
				formatted.WriteString(fmt.Sprintf("- Created at: %s\n", searchResult.CreatedAt.Format(time.RFC3339)))
			}
			if fieldsToInclude["author"] {
				formatted.WriteString(fmt.Sprintf("- Author: %s\n", searchResult.Author))
			}
			if fieldsToInclude["state"] {
				formatted.WriteString(fmt.Sprintf("- State: %s\n", searchResult.State))
			}

			if fieldsToInclude["body"] {
				formatted.WriteString("\n")
				formatted.WriteString(fmt.Sprintf("%s\n", searchResult.Body))
			}

			formatted.WriteString("\n---\n\n")
			result.WriteString(formatted.String())
		}

		if !hasResults {
			return mcp.NewToolResultText(fmt.Sprintf("No results found for \"%s\".", query)), nil
		}

		return mcp.NewToolResultText(result.String()), nil
	})

	// Register the user_summary prompt
	userSummaryPrompt := mcp.NewPrompt("user_summary",
		mcp.WithPromptDescription("Generates a summary of the user's accomplishments based on created discussions, closed issues, and closed pull requests."),
		mcp.WithArgument("username", mcp.ArgumentDescription("Username. Example: john_doe"), mcp.RequiredArgument()),
		mcp.WithArgument("period", mcp.ArgumentDescription("Examples \"last week\", \"from August 2025 to September 2025\", \"2024-01-01 - 2024-12-31\"")),
	)

	// Add prompt handler for user_summary
	s.AddPrompt(userSummaryPrompt, func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		// Extract parameters from request
		username := ""
		period := ""
		
		// Access arguments through the request structure
		for key, value := range request.Params.Arguments {
			switch key {
			case "username":
				username = value
			case "period":
				period = value
			}
		}

		if username == "" {
			return nil, fmt.Errorf("username parameter is required")
		}

		// Build the prompt text according to specification
		var promptBuilder strings.Builder
		promptBuilder.WriteString(fmt.Sprintf("Summarize the accomplishments of the user `%s` during `%s`, focusing on the most significant contributions first. Use the following approach:\n\n", username, period))
		promptBuilder.WriteString(fmt.Sprintf("- Use `list_discussions` to gather discussions they created within `%s`.\n", period))
		promptBuilder.WriteString(fmt.Sprintf("- Use `list_issues` to gather issues they closed within `%s`.\n", period))
		promptBuilder.WriteString(fmt.Sprintf("- Use `list_pull_requests` to gather pull requests they closed within `%s`.\n", period))
		promptBuilder.WriteString("- Aggregate all results, removing duplicates.\n")
		promptBuilder.WriteString("- Prioritize and highlight:\n")
		promptBuilder.WriteString("  - Discussions (most important)\n")
		promptBuilder.WriteString("  - Pull requests (next most important)\n")
		promptBuilder.WriteString("  - Issues (least important)\n")
		promptBuilder.WriteString("- For each contribution, include a direct link and relevant metrics or facts.\n")
		promptBuilder.WriteString("- Present a concise, unified summary that mixes all types of contributions, with the most impactful items first.")

		// Create prompt result with the generated text
		result := &mcp.GetPromptResult{
			Description: fmt.Sprintf("User summary for %s during %s", username, period),
			Messages: []mcp.PromptMessage{
				{
					Role: "user",
					Content: mcp.TextContent{
						Type: "text",
						Text: promptBuilder.String(),
					},
				},
			},
		}

		return result, nil
	})

	return server.ServeStdio(s)
}

// SearchResult represents a search result item
type SearchResult struct {
	Type       string    `json:"type"`        // "discussion", "issue", "pull_request"
	URL        string    `json:"url"`         // Primary identifier
	Title      string    `json:"title"`       // Item title
	Body       string    `json:"body"`        // Item content
	Repository string    `json:"repository"`  // Repository name
	Author     string    `json:"author"`      // Author username
	CreatedAt  time.Time `json:"created_at"`  // Creation timestamp
	State      string    `json:"state"`       // Item state ("open", "closed", etc.)
}

// SearchEngine performs basic text search across all entities
type SearchEngine struct {
	db *DB
}

// NewSearchEngine creates a new search engine
func NewSearchEngine(db *DB) *SearchEngine {
	return &SearchEngine{db: db}
}

// Search performs a basic search across discussions, issues, and pull requests
func (se *SearchEngine) Search(query string, limit int) ([]SearchResult, error) {
	slog.Debug("Search requested", "query", query, "limit", limit)
	
	if query == "" {
		return []SearchResult{}, nil
	}

	// Minimum 3 characters for search
	if len(strings.TrimSpace(query)) < 3 {
		slog.Debug("Search query too short", "query", query, "length", len(strings.TrimSpace(query)))
		return []SearchResult{}, nil
	}

	// Tokenize the query
	tokens := strings.Fields(strings.ToLower(query))
	if len(tokens) == 0 {
		slog.Debug("Search query has no tokens", "query", query)
		return []SearchResult{}, nil
	}

	slog.Debug("Search tokens extracted", "tokens", tokens)
	
	// Use UNION query to search all tables at once with database-level filtering
	return se.searchAllTables(tokens, limit)
}

// searchAllTables performs fast full-text search using the search FTS table
func (se *SearchEngine) searchAllTables(tokens []string, limit int) ([]SearchResult, error) {
	slog.Debug("Performing FTS search", "tokens", tokens, "limit", limit)
	
	// Build FTS query - FTS5 supports phrase queries and AND operations
	// Join tokens with AND to require all terms to match
	ftsQuery := strings.Join(tokens, " AND ")
	
	// Escape any special FTS characters
	ftsQuery = strings.ReplaceAll(ftsQuery, `"`, `""`)
	
	slog.Debug("Built FTS query", "fts_query", ftsQuery)
	
	// Use pure FTS5 search with bm25() column weights for title prioritization
	// bm25(search, 1.0, 2.0, 1.0, 1.0, 1.0, 1.0) weights: type, title(2x), body, url, repository, author
	searchTable := getTableName("search")
	query := fmt.Sprintf(`
		SELECT type, title, body, url, repository, author, created_at, state
		FROM [%s] 
		WHERE [%s] MATCH ?
		ORDER BY bm25([%s], 1.0, 2.0, 1.0, 1.0, 1.0, 1.0)
		LIMIT ?`, searchTable, searchTable, searchTable)
	
	slog.Debug("Executing FTS query", "sql", query, "search_table", searchTable, "fts_query", ftsQuery, "limit", limit)
	
	// Build args: FTS query + limit
	args := []interface{}{ftsQuery, limit}

	rows, err := se.db.Query(query, args...)
	if err != nil {
		slog.Error("FTS search query failed", "sql", query, "search_table", searchTable, "fts_query", ftsQuery, "error", err)
		return nil, fmt.Errorf("FTS search failed: %w", err)
	}
	defer rows.Close()
	
	var results []SearchResult
	for rows.Next() {
		var result SearchResult
		var createdAtStr string
		
		err := rows.Scan(&result.Type, &result.Title, &result.Body, &result.URL,
			&result.Repository, &result.Author, &createdAtStr, &result.State)
		if err != nil {
			continue
		}
		
		// Parse timestamp
		if createdAt, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
			result.CreatedAt = createdAt
		}
		
		results = append(results, result)
	}
	
	slog.Debug("FTS search completed", "results_count", len(results), "fts_query", ftsQuery)
	return results, nil
}

// searchAllTablesLike provides fallback LIKE-based search when FTS is unavailable
// RunUIServer starts the web UI server
// RunUIServer starts the web UI server
func RunUIServer(db *DB, port string) error {
	slog.Info("Initializing search engine for UI server")
	searchEngine := NewSearchEngine(db)

	// Read the index.html file and parse it as a template
	indexHTML, err := os.ReadFile("index.html")
	if err != nil {
		return fmt.Errorf("failed to read index.html: %v", err)
	}

	// Parse the template with all defined sub-templates
	tmpl, err := template.New("index").Parse(string(indexHTML))
	if err != nil {
		return fmt.Errorf("failed to parse template: %v", err)
	}

	// Serve the index.html file for the root route
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if err := tmpl.Execute(w, nil); err != nil {
			slog.Error("Failed to execute index template", "error", err)
			http.Error(w, "Template error", http.StatusInternalServerError)
		}
	})

	// Serve the HTMX JavaScript file
	http.HandleFunc("/htmx.min.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		http.ServeFile(w, r, "htmx.min.js")
	})

	// Search handler for HTMX requests
	http.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("value")
		if query == "" {
			// Return empty results for empty query
			w.Header().Set("Content-Type", "text/html")
			if err := tmpl.ExecuteTemplate(w, "empty-results", nil); err != nil {
				slog.Error("Failed to execute empty-results template", "error", err)
				http.Error(w, "Template error", http.StatusInternalServerError)
			}
			return
		}

		results, err := searchEngine.Search(query, 10)
		if err != nil {
			slog.Error("Search query failed", "query", query, "error", err)
			http.Error(w, fmt.Sprintf("Search error: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		
		if len(results) == 0 {
			if err := tmpl.ExecuteTemplate(w, "no-results", map[string]string{"Query": html.EscapeString(query)}); err != nil {
				slog.Error("Failed to execute no-results template", "error", err)
				http.Error(w, "Template error", http.StatusInternalServerError)
			}
			return
		}

		// Render each result using the template
		for _, result := range results {
			// Determine type badge based on URL
			typeBadge := "discussion"
			typeClass := "type-discussion"
			if strings.Contains(result.URL, "/issues/") {
				typeBadge = "issue"
				typeClass = "type-issue"
			} else if strings.Contains(result.URL, "/pull/") {
				typeBadge = "pr"
				typeClass = "type-pr"
			}

			// Truncate body if too long
			body := result.Body
			if len(body) > 200 {
				body = body[:200] + "..."
			}

			// Format the created date
			createdAt := ""
			if !result.CreatedAt.IsZero() {
				createdAt = result.CreatedAt.Format("Jan 2, 2006")
			}

			templateData := map[string]string{
				"URL":        html.EscapeString(result.URL),
				"Title":      html.EscapeString(result.Title),
				"TypeClass":  typeClass,
				"TypeBadge":  typeBadge,
				"Repository": html.EscapeString(result.Repository),
				"Author":     html.EscapeString(result.Author),
				"CreatedAt":  createdAt,
				"Body":       html.EscapeString(body),
			}

			if err := tmpl.ExecuteTemplate(w, "result-item", templateData); err != nil {
				slog.Error("Failed to execute result-item template", "error", err, "result_url", result.URL)
				http.Error(w, "Template error", http.StatusInternalServerError)
				return
			}
		}
	})

	fmt.Printf("Starting GitHub Brain UI server on http://localhost:%s\n", port)
	fmt.Println("Press Ctrl+C to stop the server")

	return http.ListenAndServe(":"+port, nil)
}

func main() {
	// Load environment variables
	_ = godotenv.Load()

	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Printf("Usage: %s <command> [<args>]\n\n", os.Args[0])
		fmt.Println("Commands:")
		fmt.Println("  pull   Pull GitHub repositories and discussions")
		fmt.Println("  mcp    Start the MCP server")
		fmt.Println("  ui     Start the web UI server")
		fmt.Println("\nFor command-specific help, use:")
		fmt.Println("  pull -h\n  mcp -h\n  ui -h")
		os.Exit(0)
	}

	cmd := os.Args[1]

	switch cmd {
	case "pull":
		// Load configuration from CLI args and environment variables first
		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			if args[i] == "-h" || args[i] == "--help" {
				fmt.Println("Usage: pull -t <token> -o <organization> [-db <dbpath>] [-i repositories,discussions,issues,pull-requests] [-e excluded_repos] [-f]")
				os.Exit(0)
			}
		}

		config := LoadConfig(args)
		
		// Initialize progress display FIRST - before any other operations  
		progress := NewProgress("Initializing GitHub offline MCP server...")
		progress.Start()
		defer progress.Stop()
		
		// Set up global logger with console handler for pull mode
		SetupGlobalLogger(progress.console)
		
		// Initialize the items display now that we have config
		progress.InitItems(config)
		
		progress.Log("Configuration loaded successfully")
		
		// Continue with the original logic
		
		if config.GithubToken == "" {
			progress.Log("Error: GitHub token is required. Use -t or set GITHUB_TOKEN environment variable.")
			// Give console time to display the error before exiting
			time.Sleep(3 * time.Second)
			return
		}
		if config.Organization == "" {
			progress.Log("Error: Organization is required. Use -o or set ORGANIZATION environment variable.")
			// Give console time to display the error before exiting
			time.Sleep(3 * time.Second)
			return
		}

		// Default pull all items if nothing specified
		if len(config.Items) == 0 {
			config.Items = []string{"repositories", "discussions", "issues", "pull-requests"}
		}

		// Validate items
		validItems := map[string]bool{
			"repositories":  true,
			"discussions":   true,
			"issues":        true,
			"pull-requests": true,
		}
		for _, item := range config.Items {
			if !validItems[item] {
				progress.Log("Error: Invalid item: %s. Valid items are: repositories, discussions, issues, pull-requests", item)
				// Give console time to display the error before exiting
				time.Sleep(3 * time.Second)
				return
			}
		}

		// Check if we should pull each item type
		pullRepositories := false
		pullDiscussions := false
		pullIssues := false
		pullPullRequests := false
		for _, item := range config.Items {
			switch item {
			case "repositories":
				pullRepositories = true
			case "discussions":
				pullDiscussions = true
			case "issues":
				pullIssues = true
			case "pull-requests":
				pullPullRequests = true
			}
		}

		// Initialize database
		progress.Log("Initializing database at path: %s", getDBPath(config.DBDir, config.Organization))
		db, err := InitDB(config.DBDir, config.Organization, progress)
		if err != nil {
			progress.Log("Error: Failed to initialize database: %v", err)
			// Give console time to display the error before exiting
			time.Sleep(3 * time.Second)
			return
		}
		defer db.Close()

		// Acquire lock to prevent concurrent pull operations
		if err := db.LockPull(); err != nil {
			progress.Log("Error: Failed to acquire lock: %v", err)
			time.Sleep(3 * time.Second)
			return
		}

		// Start lock renewal in background
		renewDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					if err := db.RenewPullLock(); err != nil {
						slog.Warn("Failed to renew lock", "error", err)
					}
				case <-renewDone:
					return
				}
			}
		}()

		// Ensure unlock on exit
		defer func() {
			close(renewDone)
			if err := db.UnlockPull(); err != nil {
				slog.Warn("Failed to release lock", "error", err)
			}
		}()

		progress.UpdateMessage("Initializing GitHub client...")

		// Create GitHub clients with custom transport to capture headers
		ctx := context.Background()
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: config.GithubToken},
		)
		tc := oauth2.NewClient(ctx, ts)
		
		// Wrap the transport to capture response headers and status codes
		tc.Transport = &CustomTransport{
			wrapped: tc.Transport,
		}
		
		graphqlClient := githubv4.NewClient(tc)

		// Initialize progress display with all items
		progress.Log("GitHub client initialized, starting data operations")
		
		// Clear data if Force flag is set
		if err := ClearData(db, config, progress); err != nil {
			progress.Log("Error: Failed to clear data: %v", err)
			os.Exit(1)
		}

		// No longer deleting data from other organizations - keeping all data
		// This ensures backward compatibility with existing databases

		// Pull repositories if requested
		if pullRepositories {
			if err := PullRepositories(ctx, graphqlClient, db, config, progress); err != nil {
				progress.MarkItemFailed("repositories", err.Error())
				progress.Log("Failed to pull repositories: %v", err)
				// Stop processing subsequent items if repositories failed
				progress.preserveOnExit = true
				progress.Stop()
				os.Exit(1)
			}
		}

		// Pull discussions if requested
		if pullDiscussions {
			// Check if any previous item failed
			if progress.HasAnyFailed() {
				progress.Log("Skipping discussions due to previous failures")
				// Exit early if any previous item failed
				progress.preserveOnExit = true
				progress.Stop()
				os.Exit(1)
			}

			if err := PullDiscussions(ctx, graphqlClient, db, config, progress); err != nil {
				progress.MarkItemFailed("discussions", err.Error())
				progress.Log("Error: %v", err)
				// Stop processing subsequent items if discussions failed
				progress.preserveOnExit = true
				progress.Stop()
				os.Exit(1)
			}
		}

		// Pull issues if requested
		if pullIssues {
			// Check if any previous item failed
			if progress.HasAnyFailed() {
				progress.Log("Skipping issues due to previous failures")
				// Exit early if any previous item failed
				progress.preserveOnExit = true
				progress.Stop()
				os.Exit(1)
			}

			if err := PullIssues(ctx, graphqlClient, db, config, progress); err != nil {
				progress.MarkItemFailed("issues", err.Error())
				progress.Log("Error: %v", err)
				// Stop processing subsequent items if issues failed
				progress.preserveOnExit = true
				progress.Stop()
				os.Exit(1)
			}
		}

		// Pull pull requests if requested
		if pullPullRequests {
			// Check if any previous item failed
			if progress.HasAnyFailed() {
				progress.Log("Skipping pull requests due to previous failures")
				// Exit early if any previous item failed
				progress.preserveOnExit = true
				progress.Stop()
				os.Exit(1)
			}

			progress.Log("Starting pull requests operation")

			progress.Log("About to call PullPullRequests")
			if err := PullPullRequests(ctx, graphqlClient, db, config, progress); err != nil {
				progress.MarkItemFailed("pull-requests", err.Error())
				progress.Log("Error: %v", err)
				// Stop processing subsequent items if pull requests failed
				progress.preserveOnExit = true
				progress.Stop()
				os.Exit(1)
			}
		}

		// Truncate search FTS5 table and repopulate it from discussions, issues, and pull_requests tables
		progress.UpdateMessage("Updating search index...")
		progress.Log("Starting search FTS5 table rebuild...")
		if err := db.PopulateSearchTable(progress); err != nil {
			progress.Log("❌ Warning: Failed to populate search table: %v", err)
			// Continue despite search table error - don't fail the entire operation
		}

		// Final status update through Progress system
		progress.UpdateMessage("Successfully pulled GitHub data")
		
		// Give time for final display update to render
		time.Sleep(200 * time.Millisecond)
		
		progress.Stop()
		
		// Exit successfully after pull operation
		os.Exit(0)

	case "mcp":
		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			if args[i] == "-h" || args[i] == "--help" {
				fmt.Println("Usage: mcp [-db <dbpath>]")
				os.Exit(0)
			}
		}

		// Load configuration from CLI args and environment variables (only need DB path)
		config := LoadConfig(args)

		// For MCP mode, we need to handle multiple organizations
		// For now, use a default organization or get it from environment
		organization := config.Organization
		if organization == "" {
			organization = os.Getenv("ORGANIZATION")
			if organization == "" {
				slog.Error("Organization is required for MCP mode. Set via -o flag or ORGANIZATION environment variable")
				os.Exit(1)
			}
		}

		// Disable all logging for MCP mode by setting a handler that discards everything
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
			Level: slog.Level(1000), // Set to very high level to disable all logging
		})))

		// Initialize database without progress indicator
		db, err := InitDB(config.DBDir, organization, nil)
		if err != nil {
			slog.Error("Failed to initialize database", "error", err)
			os.Exit(1)
		}
		defer db.Close()

		if err := RunMCPServer(db); err != nil {
			slog.Error("MCP server error", "error", err)
			os.Exit(1)
		}

	case "ui":
		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			if args[i] == "-h" || args[i] == "--help" {
				fmt.Println("Usage: ui -o <organization> [-db <dbpath>] [-p <port>] [-s]")
				fmt.Println("Options:")
				fmt.Println("  -o    GitHub organization (required)")
				fmt.Println("  -db   Database directory (default: ./db)")
				fmt.Println("  -p    Port for UI server (default: 8080)")
				fmt.Println("  -s    Skip creating FTS table")
				os.Exit(0)
			}
		}

		// Load configuration from CLI args and environment variables
		config := LoadConfig(args)

		// Parse port from args and environment (default: 8080)
		port := os.Getenv("UI_PORT")
		if port == "" {
			port = "8080"
		}
		for i := 0; i < len(args); i++ {
			if args[i] == "-p" && i+1 < len(args) {
				port = args[i+1]
			}
		}

		// Get organization from config or environment
		organization := config.Organization
		if organization == "" {
			organization = os.Getenv("ORGANIZATION")
			if organization == "" {
				slog.Error("Organization is required for UI mode. Set via -o flag or ORGANIZATION environment variable")
				os.Exit(1)
			}
		}

		// Initialize database without progress indicator
		db, err := InitDB(config.DBDir, organization, nil)
		if err != nil {
			slog.Error("Failed to initialize database", "error", err)
			os.Exit(1)
		}
		defer db.Close()

		if err := RunUIServer(db, port); err != nil {
			slog.Error("UI server error", "error", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		fmt.Printf("Use %s -h for help\n", os.Args[0])
		os.Exit(1)
	}
}
