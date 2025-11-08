package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	_ "github.com/mattn/go-sqlite3"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// Embedded static assets
//go:embed index.html
var indexHTML string

//go:embed htmx.min.js
var htmxJS []byte

// Database schema version GUID - change this on any schema modification
const SCHEMA_GUID = "b8f3c2a1-9e7d-4f6b-8c5a-3d2e1f0a9b8c"

// Version information (set via ldflags at build time)
var (
	Version   = "dev"
	BuildDate = "unknown"
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
)

// Removed ConsoleHandler - not needed with Bubble Tea

// BubbleTeaHandler is a custom slog handler that routes logs to Bubble Tea UI
type BubbleTeaHandler struct {
	program *tea.Program
}

// NewBubbleTeaHandler creates a new slog handler that sends logs to Bubble Tea
func NewBubbleTeaHandler(program *tea.Program) *BubbleTeaHandler {
	return &BubbleTeaHandler{program: program}
}

func (h *BubbleTeaHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *BubbleTeaHandler) Handle(_ context.Context, r slog.Record) error {
	// Build message with attributes
	var b strings.Builder
	b.WriteString(r.Message)
	
	// Add structured attributes as key=value pairs
	if r.NumAttrs() > 0 {
		first := true
		r.Attrs(func(a slog.Attr) bool {
			if first {
				b.WriteString(" ")
				first = false
			} else {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%s=%v", a.Key, a.Value))
			return true
		})
	}
	
	// Send to Bubble Tea
	if h.program != nil {
		h.program.Send(logMsg(b.String()))
	}
	
	return nil
}

func (h *BubbleTeaHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h // Simplified - we don't need to accumulate attrs
}

func (h *BubbleTeaHandler) WithGroup(name string) slog.Handler {
	return h // Simplified - we don't need group support
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
		delay = time.Duration(7000+rand.Intn(3000)) * time.Millisecond // 7-10 seconds
	} else if inPrimaryLimit {
		// Longer delay when recovering from primary rate limits
		delay = time.Duration(5000+rand.Intn(3000)) * time.Millisecond // 5-8 seconds
	} else {
		// Check current rate limit status for adaptive delays based on points utilization
		rateLimitInfoMutex.RLock()
		remaining := currentRateLimit.Remaining
		limit := currentRateLimit.Limit
		rateLimitInfoMutex.RUnlock()
		
		if remaining > 0 && limit > 0 {
			// Calculate points utilization (GitHub's rate limiting is points-based)
			pointsUsed := float64(limit-remaining) / float64(limit)
			
			if pointsUsed > 0.9 { // Above 90% points used
				// Very conservative delay when close to rate limit
				delay = time.Duration(3000+rand.Intn(2000)) * time.Millisecond // 3-5 seconds
			} else if pointsUsed > 0.7 { // Above 70% points used
				// More conservative delay
				delay = time.Duration(2000+rand.Intn(1000)) * time.Millisecond // 2-3 seconds
			} else if pointsUsed > 0.5 { // Above 50% points used
				// Moderate delay
				delay = time.Duration(1000+rand.Intn(1000)) * time.Millisecond // 1-2 seconds
			} else {
				// Normal delay (GitHub recommends 1+ second between mutations)
				delay = time.Duration(1000+rand.Intn(500)) * time.Millisecond // 1-1.5 seconds
			}
		} else {
			// Default delay when rate limit info is unknown - be conservative
			delay = time.Duration(1500+rand.Intn(1000)) * time.Millisecond // 1.5-2.5 seconds
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
	HomeDir               string   // GitHub Brain home directory (default: ~/.github-brain)
	DBDir                 string   // SQLite database path, constructed as <HomeDir>/db
	Items                 []string // Items to pull (repositories, discussions, issues, pull-requests)
	Force                 bool     // Remove all data before pulling
	ExcludedRepositories  []string // Comma-separated list of repositories to exclude from the pull of discussions, issues, and pull-requests
}

// LoadConfig creates a config from command line arguments and environment variables
// Command line arguments take precedence over environment variables
func LoadConfig(args []string) *Config {
	// Get default home directory with expansion
	defaultHomeDir := os.Getenv("HOME")
	if defaultHomeDir == "" {
		defaultHomeDir = "."
	}
	defaultHomeDir = defaultHomeDir + "/.github-brain"
	
	config := &Config{
		HomeDir: defaultHomeDir,
	}

	// Load from environment variables first
	config.GithubToken = os.Getenv("GITHUB_TOKEN")
	config.Organization = os.Getenv("ORGANIZATION")

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
		case "-m":
			if i+1 < len(args) {
				homeDir := args[i+1]
				// Expand ~ to home directory
				if strings.HasPrefix(homeDir, "~/") {
					userHomeDir := os.Getenv("HOME")
					if userHomeDir != "" {
						homeDir = userHomeDir + homeDir[1:]
					}
				}
				config.HomeDir = homeDir
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

	// Construct DBDir from HomeDir after all arguments are parsed
	config.DBDir = config.HomeDir + "/db"

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

// Removed Console struct - Bubble Tea handles all rendering

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

// visibleLength calculates the visible length of a string, ignoring ANSI escape codes
// This handles all CSI (Control Sequence Introducer) escape sequences
func visibleLength(s string) int {
	length := 0
	i := 0
	runes := []rune(s)
	
	for i < len(runes) {
		if runes[i] == '\033' && i+1 < len(runes) && runes[i+1] == '[' {
			// Skip CSI sequence: ESC [ ... (terminated by a letter)
			i += 2
			for i < len(runes) && !((runes[i] >= 'A' && runes[i] <= 'Z') || (runes[i] >= 'a' && runes[i] <= 'z')) {
				i++
			}
			i++ // Skip the terminating letter
		} else if runes[i] == '\033' {
			// Skip other escape sequences (ESC followed by one char)
			i += 2
		} else {
			length++
			i++
		}
	}
	
	return length
}

// Old Progress struct and Console removed - now using Bubble Tea for UI rendering
// See ProgressInterface and UIProgress below after DB section

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
	return fmt.Sprintf("%s/%s.db", dbDir, organization)
}





// checkSchemaVersion checks if the database schema version matches current SCHEMA_GUID
// Returns true if schema is current, false if database needs recreation
func checkSchemaVersion(db *sql.DB, progress ProgressInterface) (bool, error) {
	// Check if schema_version table exists
	var tableExists int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_version'").Scan(&tableExists)
	if err != nil {
		return false, fmt.Errorf("failed to check schema_version table existence: %w", err)
	}
	
	if tableExists == 0 {
		if progress != nil {
			progress.Log("No schema_version table found - database recreation needed")
		} else {
			slog.Info("No schema_version table found - database recreation needed")
		}
		return false, nil
	}
	
	// Read stored GUID
	var storedGUID string
	err = db.QueryRow("SELECT guid FROM schema_version LIMIT 1").Scan(&storedGUID)
	if err != nil {
		if err == sql.ErrNoRows {
			if progress != nil {
				progress.Log("No schema version GUID found - database recreation needed")
			} else {
				slog.Info("No schema version GUID found - database recreation needed")
			}
			return false, nil
		}
		return false, fmt.Errorf("failed to read schema version: %w", err)
	}
	
	// Compare GUIDs
	if storedGUID != SCHEMA_GUID {
		if progress != nil {
			progress.Log("Schema version mismatch (stored: %s, current: %s) - database recreation needed", storedGUID, SCHEMA_GUID)
		} else {
			slog.Info("Schema version mismatch - database recreation needed", "stored", storedGUID, "current", SCHEMA_GUID)
		}
		return false, nil
	}
	
	if progress != nil {
		progress.Log("Schema version matches - using existing database")
	} else {
		slog.Info("Schema version matches - using existing database")
	}
	return true, nil
}

// createAllTables creates all database tables and indexes
func createAllTables(db *sql.DB, progress ProgressInterface) error {
	// Create schema_version table and store current GUID
	_, err := db.Exec(`
		CREATE TABLE schema_version (
			guid TEXT PRIMARY KEY
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_version table: %w", err)
	}
	
	// Store current schema GUID
	_, err = db.Exec("INSERT INTO schema_version (guid) VALUES (?)", SCHEMA_GUID)
	if err != nil {
		return fmt.Errorf("failed to store schema version: %w", err)
	}

	// Create repositories table
	_, err = db.Exec(`
		CREATE TABLE repositories (
			name TEXT PRIMARY KEY,
			has_discussions_enabled BOOLEAN DEFAULT 0,
			has_issues_enabled BOOLEAN DEFAULT 0,
			updated_at DATETIME
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create repositories table: %w", err)
	}

	// Create performance index for repositories table
	_, err = db.Exec("CREATE INDEX idx_repositories_updated_at ON repositories (updated_at)")
	if err != nil {
		return fmt.Errorf("failed to create updated_at index on repositories table: %w", err)
	}

	// Create discussions table
	_, err = db.Exec(`
		CREATE TABLE discussions (
			url TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			repository TEXT NOT NULL,
			author TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create discussions table: %w", err)
	}

	// Create indexes for discussions table
	_, err = db.Exec("CREATE INDEX idx_discussions_repository ON discussions (repository)")
	if err != nil {
		return fmt.Errorf("failed to create repository index on discussions table: %w", err)
	}

	_, err = db.Exec("CREATE INDEX idx_discussions_author ON discussions (author)")
	if err != nil {
		return fmt.Errorf("failed to create author index on discussions table: %w", err)
	}

	// Create performance indexes for discussions table
	_, err = db.Exec("CREATE INDEX idx_discussions_created_at ON discussions (created_at)")
	if err != nil {
		return fmt.Errorf("failed to create created_at index on discussions table: %w", err)
	}

	_, err = db.Exec("CREATE INDEX idx_discussions_updated_at ON discussions (updated_at)")
	if err != nil {
		return fmt.Errorf("failed to create updated_at index on discussions table: %w", err)
	}

	// Composite index for common query patterns: repository + created_at
	_, err = db.Exec("CREATE INDEX idx_discussions_repo_created ON discussions (repository, created_at)")
	if err != nil {
		return fmt.Errorf("failed to create repository+created_at index on discussions table: %w", err)
	}

	// Create issues table
	_, err = db.Exec(`
		CREATE TABLE issues (
			url TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			closed_at DATETIME,
			repository TEXT NOT NULL,
			author TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create issues table: %w", err)
	}

	// Create indexes for issues table
	_, err = db.Exec("CREATE INDEX idx_issues_repository ON issues (repository)")
	if err != nil {
		return fmt.Errorf("failed to create repository index on issues table: %w", err)
	}

	_, err = db.Exec("CREATE INDEX idx_issues_author ON issues (author)")
	if err != nil {
		return fmt.Errorf("failed to create author index on issues table: %w", err)
	}

	// Create performance indexes for issues table
	_, err = db.Exec("CREATE INDEX idx_issues_created_at ON issues (created_at)")
	if err != nil {
		return fmt.Errorf("failed to create created_at index on issues table: %w", err)
	}

	_, err = db.Exec("CREATE INDEX idx_issues_updated_at ON issues (updated_at)")
	if err != nil {
		return fmt.Errorf("failed to create updated_at index on issues table: %w", err)
	}

	_, err = db.Exec("CREATE INDEX idx_issues_closed_at ON issues (closed_at)")
	if err != nil {
		return fmt.Errorf("failed to create closed_at index on issues table: %w", err)
	}

	// Composite index for common query patterns: repository + created_at
	_, err = db.Exec("CREATE INDEX idx_issues_repo_created ON issues (repository, created_at)")
	if err != nil {
		return fmt.Errorf("failed to create repository+created_at index on issues table: %w", err)
	}

	// Create pull_requests table
	_, err = db.Exec(`
		CREATE TABLE pull_requests (
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
	`)
	if err != nil {
		return fmt.Errorf("failed to create pull_requests table: %w", err)
	}

	// Create indexes for pull_requests table
	_, err = db.Exec("CREATE INDEX idx_pull_requests_repository ON pull_requests (repository)")
	if err != nil {
		return fmt.Errorf("failed to create repository index on pull_requests table: %w", err)
	}

	_, err = db.Exec("CREATE INDEX idx_pull_requests_author ON pull_requests (author)")
	if err != nil {
		return fmt.Errorf("failed to create author index on pull_requests table: %w", err)
	}

	// Create performance indexes for pull_requests table
	_, err = db.Exec("CREATE INDEX idx_pull_requests_created_at ON pull_requests (created_at)")
	if err != nil {
		return fmt.Errorf("failed to create created_at index on pull_requests table: %w", err)
	}

	_, err = db.Exec("CREATE INDEX idx_pull_requests_updated_at ON pull_requests (updated_at)")
	if err != nil {
		return fmt.Errorf("failed to create updated_at index on pull_requests table: %w", err)
	}

	_, err = db.Exec("CREATE INDEX idx_pull_requests_closed_at ON pull_requests (closed_at)")
	if err != nil {
		return fmt.Errorf("failed to create closed_at index on pull_requests table: %w", err)
	}

	// Composite index for common query patterns: repository + created_at
	_, err = db.Exec("CREATE INDEX idx_pull_requests_repo_created ON pull_requests (repository, created_at)")
	if err != nil {
		return fmt.Errorf("failed to create repository+created_at index on pull_requests table: %w", err)
	}

	// Create merged_at index
	_, err = db.Exec("CREATE INDEX idx_pull_requests_merged_at ON pull_requests (merged_at)")
	if err != nil {
		return fmt.Errorf("failed to create merged_at index on pull_requests table: %w", err)
	}

	// Create lock table
	_, err = db.Exec(`
		CREATE TABLE lock (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			locked INTEGER NOT NULL DEFAULT 0,
			locked_at DATETIME
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create lock table: %w", err)
	}

	// Ensure single row exists in lock table
	_, _ = db.Exec(`INSERT INTO lock (id, locked, locked_at) VALUES (1, 0, NULL)`)

	// Create search table (FTS5) for full-text search
	_, err = db.Exec(`
		CREATE VIRTUAL TABLE search USING fts5(
			type, title, body, url, repository, author, created_at UNINDEXED, state UNINDEXED, boost UNINDEXED
		)
	`)
	if err != nil {
		return fmt.Errorf("FTS5 not available in SQLite - rebuild with FTS5 support: %w", err)
	}

	return nil
}

func InitDB(dbDir, organization string, progress ProgressInterface) (*DB, error) {
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

	// Check if database file exists and if schema version matches
	var needsRecreation bool = true
	if _, err := os.Stat(dbPath); err == nil {
		// Database exists, check schema version
		tempDB, err := sql.Open("sqlite3", dbPath)
		if err == nil {
			defer tempDB.Close()
			schemaMatches, checkErr := checkSchemaVersion(tempDB, progress)
			if checkErr != nil {
				if progress != nil {
					progress.Log("Error checking schema version: %v - recreating database", checkErr)
				} else {
					slog.Warn("Error checking schema version - recreating database", "error", checkErr)
				}
			} else if schemaMatches {
				needsRecreation = false
			}
		}
	}
	
	// Drop and recreate database if needed
	if needsRecreation {
		if progress != nil {
			progress.Log("Dropping existing database and creating new one")
		} else {
			slog.Info("Dropping existing database and creating new one")
		}
		
		// Remove existing database file
		if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
			if progress != nil {
				progress.Log("Warning: Failed to remove existing database: %v", err)
			} else {
				slog.Warn("Failed to remove existing database", "error", err)
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

	// Only create tables if we're creating a new database
	if needsRecreation {
		if err := createAllTables(db, progress); err != nil {
			return nil, fmt.Errorf("failed to create database tables: %w", err)
		}
	}



	return &DB{db: db}, nil
}

// PopulateSearchTable populates the search FTS table with data from all tables as specified in main.md
func (db *DB) PopulateSearchTable(currentUsername string, progress ProgressInterface) error {
	// Truncate search FTS5 table and repopulate it from discussions, issues, and pull_requests tables
	slog.Info("Truncating and repopulating search FTS table...")
	progress.Log("Clearing existing search index...")
	
	// Delete all data from search table
	if _, err := db.Exec("DELETE FROM search"); err != nil {
		return fmt.Errorf("failed to truncate search table: %w", err)
	}
	
	// Get counts for progress reporting
	var discussionCount, issueCount, prCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM discussions").Scan(&discussionCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&issueCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM pull_requests").Scan(&prCount)
	
	totalItems := discussionCount + issueCount + prCount
	progress.Log("Indexing %d total items: %d discussions, %d issues, %d pull requests", 
		totalItems, discussionCount, issueCount, prCount)
	
	slog.Info("Indexing content into search table", 
		"discussions", discussionCount, "issues", issueCount, "pull_requests", prCount)
	
	// Query for all unique repository names where the user is the author
	slog.Info("Querying repositories where user is author", "username", currentUsername)
	progress.Log("Identifying repositories with your contributions...")
	
	userReposMap := make(map[string]bool)
	
	// Get repositories from discussions
	rows, err := db.Query(`
		SELECT DISTINCT repository FROM discussions WHERE author = ?
		UNION
		SELECT DISTINCT repository FROM issues WHERE author = ?
		UNION
		SELECT DISTINCT repository FROM pull_requests WHERE author = ?
	`, currentUsername, currentUsername, currentUsername)
	
	if err != nil {
		slog.Warn("Failed to query user repositories, proceeding with boost=1.0 for all", "error", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var repo string
			if err := rows.Scan(&repo); err == nil {
				userReposMap[repo] = true
			}
		}
	}
	
	progress.Log("Found %d repositories with your contributions (will receive 2x boost)", len(userReposMap))
	slog.Info("User contribution repositories identified", "count", len(userReposMap), "username", currentUsername)
	
	// Insert discussions with boost calculation
	if discussionCount > 0 {
		progress.Log("Indexing %d discussions into search table...", discussionCount)
		slog.Info("Indexing discussions...")
		_, err := db.Exec(`
			INSERT INTO search(type, title, body, url, repository, author, created_at, state, boost)
			SELECT 'discussion', title, body, url, repository, author, created_at, 'open',
			       CASE WHEN author = ? THEN 2.0 ELSE 1.0 END
			FROM discussions
		`, currentUsername)
		if err != nil {
			return fmt.Errorf("failed to populate discussions in search table: %w", err)
		}
		progress.Log("✅ Completed indexing %d discussions", discussionCount)
	} else {
		progress.Log("No discussions to index")
	}

	// Insert issues with boost calculation
	if issueCount > 0 {
		progress.Log("Indexing %d issues into search table...", issueCount)
		slog.Info("Indexing issues...")
		_, err := db.Exec(`
			INSERT INTO search(type, title, body, url, repository, author, created_at, state, boost)
			SELECT 'issue', title, body, url, repository, author, created_at, 
			       CASE WHEN closed_at IS NULL THEN 'open' ELSE 'closed' END,
			       CASE WHEN author = ? THEN 2.0 ELSE 1.0 END
			FROM issues
		`, currentUsername)
		if err != nil {
			return fmt.Errorf("failed to populate issues in search table: %w", err)
		}
		progress.Log("✅ Completed indexing %d issues", issueCount)
	} else {
		progress.Log("No issues to index")
	}

	// Insert pull requests with boost calculation
	if prCount > 0 {
		progress.Log("Indexing %d pull requests into search table...", prCount)
		slog.Info("Indexing pull requests...")
		_, err := db.Exec(`
			INSERT INTO search(type, title, body, url, repository, author, created_at, state, boost)
			SELECT 'pull_request', title, body, url, repository, author, created_at, 
			       CASE WHEN closed_at IS NULL THEN 'open' ELSE 'closed' END,
			       CASE WHEN author = ? THEN 2.0 ELSE 1.0 END
			FROM pull_requests
		`, currentUsername)
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
			"INSERT OR REPLACE INTO repositories (name, updated_at, has_issues_enabled, has_discussions_enabled) VALUES (?, ?, ?, ?)",
			repo.Name, repo.UpdatedAt.Format(time.RFC3339), repo.HasIssuesEnabled, repo.HasDiscussionsEnabled,
		)
		return err
	}, "save repository")
}

// SaveDiscussion saves a discussion to the database with retry logic for database locks
func (db *DB) SaveDiscussion(discussion *Discussion) error {
	return db.executeWithRetry(func() error {
		_, err := db.Exec(
			"INSERT OR REPLACE INTO discussions (url, title, body, created_at, updated_at, repository, author) VALUES (?, ?, ?, ?, ?, ?, ?)",
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
			"INSERT OR REPLACE INTO issues (url, title, body, created_at, updated_at, closed_at, repository, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
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
			"INSERT OR REPLACE INTO pull_requests (url, title, body, created_at, updated_at, merged_at, closed_at, repository, author) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			pr.URL, pr.Title, pr.Body, pr.CreatedAt.Format(time.RFC3339), pr.UpdatedAt.Format(time.RFC3339), mergedAtStr, closedAtStr, pr.Repository, pr.Author,
		)
		return err
	}, "save pull request")
}

// GetRepositories gets all repositories from the database
func (db *DB) GetRepositories() ([]Repository, error) {
	rows, err := db.Query("SELECT name, updated_at, has_issues_enabled, has_discussions_enabled FROM repositories")
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
	query := "SELECT name, updated_at, has_issues_enabled, has_discussions_enabled FROM repositories WHERE name = ?"
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
		FROM discussions ` + whereClause + `
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
		FROM issues ` + whereClause + `
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
		FROM pull_requests ` + whereClause + `
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
	var query string
	switch tableName {
	case "discussions":
		query = "SELECT MAX(updated_at) FROM discussions WHERE repository = ?"
	case "issues":
		query = "SELECT MAX(updated_at) FROM issues WHERE repository = ?"
	case "pull_requests":
		query = "SELECT MAX(updated_at) FROM pull_requests WHERE repository = ?"
	default:
		return time.Time{}, fmt.Errorf("unknown table name: %s", tableName)
	}
	
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
func (db *DB) removeRepositoryAndAssociatedData(repositoryName string, progress ProgressInterface) {
	progress.Log("Repository %s does not exist, removing repository and all associated data from database", repositoryName)
	
	// Remove the repository
	_, cleanupErr := db.Exec("DELETE FROM repositories WHERE name = ?", repositoryName)
	if cleanupErr != nil {
		progress.Log("Warning: failed to remove repository %s from database: %v", repositoryName, cleanupErr)
	}
	
	// Remove all associated discussions
	_, cleanupErr = db.Exec("DELETE FROM discussions WHERE repository = ?", repositoryName)
	if cleanupErr != nil {
		progress.Log("Warning: failed to remove discussions for repository %s from database: %v", repositoryName, cleanupErr)
	}
	
	// Remove all associated issues
	_, cleanupErr = db.Exec("DELETE FROM issues WHERE repository = ?", repositoryName)
	if cleanupErr != nil {
		progress.Log("Warning: failed to remove issues for repository %s from database: %v", repositoryName, cleanupErr)
	}
	
	// Remove all associated pull requests
	_, cleanupErr = db.Exec("DELETE FROM pull_requests WHERE repository = ?", repositoryName)
	if cleanupErr != nil {
		progress.Log("Warning: failed to remove pull requests for repository %s from database: %v", repositoryName, cleanupErr)
	}
}

// GetMostRecentRepositoryTimestamp gets the most recent updated_at timestamp from repositories
func (db *DB) GetMostRecentRepositoryTimestamp(progress ProgressInterface) (time.Time, error) {
	// Create the updated_at column if it doesn't exist
	_, err := db.Exec(`
		PRAGMA table_info(repositories)
	`)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to check table schema: %w", err)
	}

	// Add updated_at column if it doesn't exist (versioned table already has it)
	_, err = db.Exec(`
		ALTER TABLE repositories ADD COLUMN updated_at DATETIME
	`)
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
	row := db.QueryRow("SELECT MAX(updated_at) FROM repositories")
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
	
	// Debug logging to help identify rate limit detection issues
	if strings.Contains(errMsg, "rate limit") {
		slog.Debug("Rate limit error detected", "error", errMsg)
	}

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
	} else if isRateLimitError(errMsg) {
		// Handle primary rate limit
		slog.Info("Primary rate limit detected via error message", "error", errMsg)

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

	// If we get here, the error was not recognized as a rate limit
	if strings.Contains(errMsg, "rate limit") {
		slog.Warn("Rate limit error not properly detected", "error", errMsg)
	}

	return false, 0
}

// isNetworkError checks if the error is a network-related error that might be resolved by waiting
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "network unreachable")
}

// isRateLimitError checks if the error message indicates a rate limit
func isRateLimitError(errMsg string) bool {
	// Convert to lowercase for case-insensitive matching
	lowerErr := strings.ToLower(errMsg)
	
	// Check for various GitHub rate limit error patterns
	return strings.Contains(lowerErr, "api rate limit exceeded") ||
		strings.Contains(lowerErr, "rate limit exceeded") ||
		strings.Contains(lowerErr, "rate limit already exceeded") ||
		strings.Contains(lowerErr, "you have exceeded") ||
		strings.Contains(lowerErr, "rate limit") && strings.Contains(lowerErr, "exceeded") ||
		strings.Contains(lowerErr, "rate limit") && strings.Contains(lowerErr, "user id")
}

// handleGraphQLError centralizes GraphQL error handling with retries and rate limit management
// Returns (success, shouldRetry, waitDuration, error)
func handleGraphQLError(ctx context.Context, client *githubv4.Client, queryFunc func() error, operation string, page int, requestCount *atomic.Int64, progress ProgressInterface) error {
	const maxRetries = 10 // Increased from 3 to 10 for better rate limit handling
	const baseRetryDelay = 5 * time.Second // Base delay for exponential backoff (increased)
	const maxRetryDelay = 30 * time.Minute // Maximum delay between retries (increased)
	
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

		// Handle timeouts (GitHub terminates requests >10 seconds, deducts additional points)
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline exceeded") {
			slog.Warn("Request timeout detected - GitHub deducts additional points next hour", "operation", operation, "page", page)
			if progress != nil {
				progress.UpdateMessage(fmt.Sprintf("Request timeout on page %d (additional points deducted)", page))
			}
			// Continue with normal retry logic for timeouts
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

		// Handle network errors (sleep/wake scenarios)
		if isNetworkError(err) {
			// Network error - wait 60-120 seconds with jitter to allow recovery
			baseWait := 60 * time.Second
			jitter := time.Duration(rand.Intn(60)) * time.Second
			waitTime := baseWait + jitter
			
			slog.Info("Network error detected, waiting for recovery", "operation", operation, "page", page, "wait", waitTime.String(), "error", err.Error())
			if progress != nil {
				progress.UpdateMessage(fmt.Sprintf("Network error on page %d, waiting %v for recovery...", page, waitTime))
			}
			
			// Wait with context cancellation support
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(waitTime):
				// Continue to retry after network recovery wait
			}
			continue
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
func ClearData(db *DB, config *Config, progress ProgressInterface) error {
	if !config.Force {
		return nil
	}

	// If specific items are provided, clear only those
	if len(config.Items) > 0 {
		for _, item := range config.Items {
			switch item {
			case "repositories":
				progress.Log("Deleting repositories table")
				_, err := db.Exec("DELETE FROM repositories")
				if err != nil {
					return fmt.Errorf("failed to clear repositories: %w", err)
				}
			case "discussions":
				progress.Log("Deleting discussions table")
				_, err := db.Exec("DELETE FROM discussions")
				if err != nil {
					return fmt.Errorf("failed to clear discussions: %w", err)
				}
			case "issues":
				progress.Log("Deleting issues table")
				_, err := db.Exec("DELETE FROM issues")
				if err != nil {
					return fmt.Errorf("failed to clear issues: %w", err)
				}
			case "pull-requests":
				progress.Log("Deleting pull_requests table")
				_, err := db.Exec("DELETE FROM pull_requests")
				if err != nil {
					return fmt.Errorf("failed to clear pull requests: %w", err)
				}

			}
		}
	} else {
		// Clear all data
		tables := []string{"pull_requests", "issues", "discussions", "repositories"}
		for _, table := range tables {
			_, err := db.Exec(fmt.Sprintf("DELETE FROM %s", table))
			if err != nil {
				return fmt.Errorf("failed to clear %s: %w", table, err)
			}
		}
	}

	return nil
}

// PullRepositories pulls repositories from GitHub using GraphQL API
func PullRepositories(ctx context.Context, client *githubv4.Client, db *DB, config *Config, progress ProgressInterface) error {
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
	semaphore := make(chan struct{}, 50) // Limit to 50 concurrent requests (conservative limit)

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
func PullDiscussions(ctx context.Context, client *githubv4.Client, db *DB, config *Config, progress ProgressInterface) error {
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
	semaphore := make(chan struct{}, 50) // Limit to 50 concurrent repositories (conservative limit)
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
func PullIssues(ctx context.Context, client *githubv4.Client, db *DB, config *Config, progress ProgressInterface) error {
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
	semaphore := make(chan struct{}, 50) // Limit to 50 concurrent repositories (conservative limit)
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
func PullPullRequests(ctx context.Context, client *githubv4.Client, db *DB, config *Config, progress ProgressInterface) error {
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
	semaphore := make(chan struct{}, 50) // Limit to 50 concurrent repositories (conservative limit)
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
	query := `
		SELECT url, title, body, author, created_at, updated_at, repository
		FROM discussions
		WHERE url = ?
	`
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
	query := `
		SELECT url, title, body, author, created_at, updated_at, repository
		FROM discussions
		WHERE repository = ?
	`
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
	// bm25(search, 1.0, 3.0, 1.0, 1.0, 1.0, 1.0) weights: type, title(3x), body, url, repository, author
	// Multiply by boost to prioritize user's authored content (2x boost)
	query := `
		SELECT type, title, body, url, repository, author, created_at, state
		FROM search 
		WHERE search MATCH ?
		ORDER BY (bm25(search, 1.0, 3.0, 1.0, 1.0, 1.0, 1.0) * boost)
		LIMIT ?`
	
	slog.Debug("Executing FTS query", "sql", query, "search_table", "search", "fts_query", ftsQuery, "limit", limit)
	
	// Build args: FTS query + limit
	args := []interface{}{ftsQuery, limit}

	rows, err := se.db.Query(query, args...)
	if err != nil {
		slog.Error("FTS search query failed", "sql", query, "search_table", "search", "fts_query", ftsQuery, "error", err)
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

	// Parse the embedded index.html template
	tmpl, err := template.New("index").Parse(indexHTML)
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

	// Serve the embedded HTMX JavaScript file
	http.HandleFunc("/htmx.min.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		if _, err := w.Write(htmxJS); err != nil {
			slog.Error("Failed to write HTMX JavaScript", "error", err)
		}
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
	// Handle --version flag before any other processing
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("github-brain %s (%s)\n", Version, BuildDate)
		os.Exit(0)
		return
	}

	// Parse home directory early to load .env from the correct location
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = "."
	}
	homeDir = homeDir + "/.github-brain"
	
	// Check for -m flag to override home directory
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-m" && i+1 < len(os.Args) {
			customHome := os.Args[i+1]
			// Expand ~ to home directory
			if strings.HasPrefix(customHome, "~/") {
				userHomeDir := os.Getenv("HOME")
				if userHomeDir != "" {
					customHome = userHomeDir + customHome[1:]
				}
			}
			homeDir = customHome
			break
		}
	}
	
	// Load environment variables from home directory
	envPath := homeDir + "/.env"
	_ = godotenv.Load(envPath)

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
				fmt.Println("Usage: pull -t <token> -o <organization> [-m <home_dir>] [-i repositories,discussions,issues,pull-requests] [-e excluded_repos] [-f]")
				fmt.Println("Options:")
				fmt.Println("  -t    GitHub token (or set GITHUB_TOKEN)")
				fmt.Println("  -o    GitHub organization (or set ORGANIZATION)")
				fmt.Println("  -m    Home directory (default: ~/.github-brain)")
				fmt.Println("  -i    Items to pull (default: all)")
				fmt.Println("  -e    Excluded repositories (comma-separated)")
				fmt.Println("  -f    Force: clear data before pulling")
				os.Exit(0)
			}
		}

		config := LoadConfig(args)
		
		// Initialize progress display FIRST - before any other operations  
		progress := NewUIProgress("Initializing GitHub offline MCP server...")
		progress.Start()
		defer progress.Stop()
		
		// Initialize the items display now that we have config
		progress.InitItems(config)
		
		// Set up slog to route to Bubble Tea UI
		slog.SetDefault(slog.New(NewBubbleTeaHandler(progress.program)))
		
		slog.Info("Configuration loaded successfully")
		
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

		// Create GitHub Brain home directory if it doesn't exist
		if _, err := os.Stat(config.HomeDir); os.IsNotExist(err) {
			progress.Log("Creating GitHub Brain home directory: %s", config.HomeDir)
			if err := os.MkdirAll(config.HomeDir, 0755); err != nil {
				progress.Log("Error: Failed to create home directory: %v", err)
				time.Sleep(3 * time.Second)
				return
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
		
		// Fetch current user (always runs, even when using -i)
		progress.Log("Fetching current authenticated user...")
		var currentUser struct {
			Viewer struct {
				Login string
			}
		}
		if err := graphqlClient.Query(ctx, &currentUser, nil); err != nil {
			progress.Log("Error: Failed to fetch current user: %v", err)
			progress.preserveOnExit = true
			progress.Stop()
			os.Exit(1)
		}
		currentUsername := currentUser.Viewer.Login
		progress.Log("Authenticated as user: %s", currentUsername)
		
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
		if err := db.PopulateSearchTable(currentUsername, progress); err != nil {
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
				fmt.Println("Usage: mcp [-m <home_dir>] [-o <organization>]")
				fmt.Println("Options:")
				fmt.Println("  -m    Home directory (default: ~/.github-brain)")
				fmt.Println("  -o    GitHub organization (or set ORGANIZATION)")
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
				fmt.Println("Usage: ui -o <organization> [-m <home_dir>] [-p <port>] [-s]")
				fmt.Println("Options:")
				fmt.Println("  -o    GitHub organization (required)")
				fmt.Println("  -m    Home directory (default: ~/.github-brain)")
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

// ============================================================================
// Bubble Tea UI Implementation
// ============================================================================

// ProgressInterface defines the common interface for progress indicators
type ProgressInterface interface {
	Start()
	Stop()
	StopWithPreserve()
	InitItems(config *Config)
	UpdateItemCount(item string, count int)
	MarkItemCompleted(item string, count int)
	MarkItemFailed(item string, message string)
	SetCurrentItem(item string)
	Log(format string, args ...interface{})
	UpdateMessage(message string)
	HasAnyFailed() bool
	UpdateAPIStatus(success, warning, errors int)
	UpdateRateLimit(used, limit int, resetTime time.Time)
	UpdateRequestRate(requestsPerSecond int)
}

// UIProgress implements the ProgressInterface using Bubble Tea for rendering
type UIProgress struct {
	program        *tea.Program
	preserveOnExit bool
}

// NewUIProgress creates a new Bubble Tea-based progress indicator
func NewUIProgress(message string) *UIProgress {
	return &UIProgress{
		program: nil, // Will be initialized in Start()
	}
}

// Start initializes and starts the Bubble Tea program
func (p *UIProgress) Start() {
	// Program will be started in InitItems after we know which items are enabled
}

// InitItems initializes the items to display based on config
func (p *UIProgress) InitItems(config *Config) {
	enabledItems := make(map[string]bool)
	for _, item := range config.Items {
		enabledItems[item] = true
	}
	
	m := newModel(enabledItems)
	// Use WithAltScreen to run in alternate screen mode (prevents multiple boxes)
	p.program = tea.NewProgram(m, tea.WithAltScreen())
	
	// Start the program in a goroutine
	go func() {
		if _, err := p.program.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error running Bubble Tea program: %v\n", err)
		}
	}()
	
	// Give the program time to initialize
	time.Sleep(100 * time.Millisecond)
}

// Stop stops the Bubble Tea program
func (p *UIProgress) Stop() {
	if p.program != nil {
		p.program.Quit()
	}
}

// StopWithPreserve stops the program while preserving display
func (p *UIProgress) StopWithPreserve() {
	p.Stop()
}

// UpdateItemCount updates the count for an item
func (p *UIProgress) UpdateItemCount(item string, count int) {
	if p.program != nil {
		p.program.Send(itemUpdateMsg{item: item, count: count})
	}
}

// MarkItemCompleted marks an item as completed
func (p *UIProgress) MarkItemCompleted(item string, count int) {
	if p.program != nil {
		p.program.Send(itemCompleteMsg{item: item, count: count})
	}
}

// MarkItemFailed marks an item as failed
func (p *UIProgress) MarkItemFailed(item string, message string) {
	if p.program != nil {
		p.program.Send(itemFailedMsg{item: item, message: message})
	}
}

// SetCurrentItem sets the currently processing item
func (p *UIProgress) SetCurrentItem(item string) {
	if p.program != nil {
		p.program.Send(setCurrentItemMsg(item))
	}
}

// Log adds a log message
func (p *UIProgress) Log(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if p.program != nil {
		p.program.Send(logMsg(message))
	}
}

// UpdateMessage updates the main message (maps to log)
func (p *UIProgress) UpdateMessage(message string) {
	p.Log("%s", message)
}

// HasAnyFailed checks if any item has failed
func (p *UIProgress) HasAnyFailed() bool {
	// This would need to query the model state, but for simplicity
	// we'll rely on the caller tracking failures
	return false
}

// UpdateAPIStatus updates API call statistics
func (p *UIProgress) UpdateAPIStatus(success, warning, errors int) {
	if p.program != nil {
		p.program.Send(apiStatusMsg{success: success, warning: warning, errors: errors})
	}
}

// UpdateRateLimit updates rate limit information
func (p *UIProgress) UpdateRateLimit(used, limit int, resetTime time.Time) {
	if p.program != nil {
		p.program.Send(rateLimitMsg{used: used, limit: limit, resetTime: resetTime})
	}
}

// UpdateRequestRate updates the requests per second rate (not directly shown in Bubble Tea UI)
func (p *UIProgress) UpdateRequestRate(requestsPerSecond int) {
	// This could be added to the model if desired, but for now we just ignore it
}

// Message types for Bubble Tea updates
type (
	tickMsg          time.Time
	itemUpdateMsg    struct {
		item  string
		count int
	}
	itemCompleteMsg struct {
		item  string
		count int
	}
	itemFailedMsg struct {
		item    string
		message string
	}
	setCurrentItemMsg string
	logMsg            string
	apiStatusMsg      struct {
		success int
		warning int
		errors  int
	}
	rateLimitMsg struct {
		used      int
		limit     int
		resetTime time.Time
	}
)

// itemState represents the state of a pull item
type itemState struct {
	name      string
	enabled   bool
	active    bool
	completed bool
	failed    bool
	count     int
}

// model is the Bubble Tea model for the pull command UI
type model struct {
	items          map[string]itemState
	itemOrder      []string
	spinner        spinner.Model
	logs           []logEntry
	apiSuccess     int
	apiWarning     int
	apiErrors      int
	rateLimitUsed  int
	rateLimitMax   int
	rateLimitReset time.Time
	width          int
	height         int
	borderColors   []lipgloss.AdaptiveColor
	colorIndex     int
}

// logEntry represents a timestamped log message (renamed from LogEntry to avoid conflict)
type logEntry struct {
	time    time.Time
	message string
}

// newModel creates a new Bubble Tea model
func newModel(enabledItems map[string]bool) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // Bright blue

	// Define gradient colors for border animation (purple → blue → cyan)
	gradientColors := []lipgloss.AdaptiveColor{
		{Light: "#874BFD", Dark: "#7D56F4"}, // Purple
		{Light: "#7D56F4", Dark: "#6B4FD8"}, // Purple-blue
		{Light: "#5B4FE0", Dark: "#5948C8"}, // Blue-purple
		{Light: "#4F7BD8", Dark: "#4B6FD0"}, // Blue
		{Light: "#48A8D8", Dark: "#45A0D0"}, // Cyan-blue
		{Light: "#48D8D0", Dark: "#45D0C8"}, // Cyan
	}

	itemOrder := []string{"repositories", "discussions", "issues", "pull-requests"}
	items := make(map[string]itemState)
	for _, name := range itemOrder {
		items[name] = itemState{
			name:      name,
			enabled:   enabledItems[name],
			active:    false,
			completed: false,
			failed:    false,
			count:     0,
		}
	}

	return model{
		items:        items,
		itemOrder:    itemOrder,
		spinner:      s,
		logs:         make([]logEntry, 0, 5),
		width:        80,
		height:       24,
		borderColors: gradientColors,
		colorIndex:   0,
	}
}

// Init initializes the Bubble Tea model
func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		tickCmd(),
	)
}

// tickCmd returns a command that ticks every second for border animation
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles messages and updates the model
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		// Rotate border color
		m.colorIndex = (m.colorIndex + 1) % len(m.borderColors)
		return m, tickCmd()

	case itemUpdateMsg:
		if state, exists := m.items[msg.item]; exists {
			state.count = msg.count
			state.active = true
			m.items[msg.item] = state
		}
		return m, nil

	case setCurrentItemMsg:
		// Clear active state from all items
		for name, state := range m.items {
			state.active = false
			m.items[name] = state
		}
		// Set active state on current item
		if state, exists := m.items[string(msg)]; exists {
			state.active = true
			m.items[string(msg)] = state
		}
		return m, nil

	case itemCompleteMsg:
		if state, exists := m.items[msg.item]; exists {
			state.completed = true
			state.active = false
			state.count = msg.count
			m.items[msg.item] = state
		}
		// Add celebration log for milestones
		if msg.count >= 10000 {
			m.addLog(fmt.Sprintf("🚀✨🎉 %s completed (%s synced)! 🎉✨🚀", capitalize(msg.item), formatNumber(msg.count)))
		} else if msg.count >= 5000 {
			m.addLog(fmt.Sprintf("🎉 %s completed (%s synced)!", capitalize(msg.item), formatNumber(msg.count)))
		} else if msg.count >= 1000 {
			m.addLog(fmt.Sprintf("✨ %s completed (%s synced)", capitalize(msg.item), formatNumber(msg.count)))
		}
		return m, nil

	case itemFailedMsg:
		if state, exists := m.items[msg.item]; exists {
			state.failed = true
			state.active = false
			m.items[msg.item] = state
		}
		m.addLog(fmt.Sprintf("❌ %s failed: %s", capitalize(msg.item), msg.message))
		return m, nil

	case logMsg:
		m.addLog(string(msg))
		return m, nil

	case apiStatusMsg:
		m.apiSuccess = msg.success
		m.apiWarning = msg.warning
		m.apiErrors = msg.errors
		return m, nil

	case rateLimitMsg:
		m.rateLimitUsed = msg.used
		m.rateLimitMax = msg.limit
		m.rateLimitReset = msg.resetTime
		return m, nil

	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

// addLog adds a log entry to the model
func (m *model) addLog(message string) {
	entry := logEntry{
		time:    time.Now(),
		message: message,
	}
	m.logs = append(m.logs, entry)
	if len(m.logs) > 5 {
		m.logs = m.logs[1:]
	}
}

// View renders the UI
func (m model) View() string {
	// Use a fixed box width that doesn't change (minimum 80, or slightly less than terminal width)
	boxWidth := 80
	if m.width > 84 {
		boxWidth = m.width - 4
	}

	// Define colors
	borderColor := m.borderColors[m.colorIndex]
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("12"))  // Bright blue
	completeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // Bright green
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))     // Bright red
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(borderColor)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("7")) // White

	var b strings.Builder
	b.Grow(4096)

	// Top border with title (ensure consistent width calculation)
	titleText := "GitHub 🧠 pull"
	titleLen := visibleLength(titleText)
	titleRendered := titleStyle.Render(titleText)
	remainingWidth := boxWidth - titleLen - 4 // 4 = "╭─ " + " ╮"
	
	b.WriteString(lipgloss.NewStyle().Foreground(borderColor).Render("╭─ "))
	b.WriteString(titleRendered)
	b.WriteString(lipgloss.NewStyle().Foreground(borderColor).Render(" " + strings.Repeat("─", remainingWidth) + "╮\n"))

	// Empty line
	b.WriteString(renderEmptyLine(boxWidth, borderColor))

	// Items section
	for _, name := range m.itemOrder {
		state := m.items[name]
		b.WriteString(renderItem(state, m.spinner.View(), boxWidth, borderColor, dimStyle, activeStyle, completeStyle, errorStyle))
	}

	// Empty line
	b.WriteString(renderEmptyLine(boxWidth, borderColor))

	// API Status line
	b.WriteString(renderAPIStatus(m.apiSuccess, m.apiWarning, m.apiErrors, boxWidth, borderColor, headerStyle, completeStyle, errorStyle))

	// Rate Limit line
	b.WriteString(renderRateLimit(m.rateLimitUsed, m.rateLimitMax, m.rateLimitReset, boxWidth, borderColor, headerStyle))

	// Empty line
	b.WriteString(renderEmptyLine(boxWidth, borderColor))

	// Activity section (header + 5 log lines)
	b.WriteString(renderActivityHeader(boxWidth, borderColor, headerStyle))
	for i := 0; i < 5; i++ {
		if i < len(m.logs) {
			b.WriteString(renderLogLine(m.logs[i], boxWidth, borderColor, errorStyle))
		} else {
			b.WriteString(renderEmptyActivityLine(boxWidth, borderColor))
		}
	}

	// Bottom border
	b.WriteString(lipgloss.NewStyle().Foreground(borderColor).Render("╰" + strings.Repeat("─", boxWidth-2) + "╯\n"))

	return b.String()
}

// Helper rendering functions

func renderEmptyLine(width int, borderColor lipgloss.AdaptiveColor) string {
	return lipgloss.NewStyle().Foreground(borderColor).Render("│") +
		strings.Repeat(" ", width-2) +
		lipgloss.NewStyle().Foreground(borderColor).Render("│\n")
}

func renderItem(state itemState, spinnerView string, width int, borderColor lipgloss.AdaptiveColor, dimStyle, activeStyle, completeStyle, errorStyle lipgloss.Style) string {
	var icon string
	var style lipgloss.Style
	var text string

	displayName := capitalize(state.name)

	// Check status flags first (completed/failed/active), then check enabled
	if state.failed {
		icon = "❌"
		style = errorStyle
		if state.count > 0 {
			text = fmt.Sprintf("%s: %s (errors)", displayName, formatNumber(state.count))
		} else {
			text = displayName
		}
	} else if state.completed {
		icon = "✅"
		style = completeStyle
		text = fmt.Sprintf("%s: %s", displayName, formatNumber(state.count))
	} else if state.active {
		icon = spinnerView
		style = activeStyle
		if state.count > 0 {
			text = fmt.Sprintf("%s: %s", displayName, formatNumber(state.count))
		} else {
			text = displayName
		}
	} else if !state.enabled {
		icon = "🔕"
		style = dimStyle
		text = displayName
	} else {
		icon = "⋯"
		style = lipgloss.NewStyle()
		text = displayName
	}

	content := style.Render(icon + " " + text)
	contentLen := visibleLength(content)
	padding := width - contentLen - 5 // 5 = "│  " + " │"
	
	// Ensure padding is never negative
	if padding < 0 {
		padding = 0
	}

	return lipgloss.NewStyle().Foreground(borderColor).Render("│  ") +
		content +
		strings.Repeat(" ", padding) +
		lipgloss.NewStyle().Foreground(borderColor).Render("│\n")
}

func renderAPIStatus(success, warning, errors, width int, borderColor lipgloss.AdaptiveColor, headerStyle, completeStyle, errorStyle lipgloss.Style) string {
	content := headerStyle.Render("📊 API Status    ") +
		completeStyle.Render("✅ "+formatNumber(success)) + "   " +
		"⚠️ " + formatNumber(warning) + "   " +
		errorStyle.Render("❌ "+formatNumber(errors))

	contentLen := visibleLength(content)
	padding := width - contentLen - 5
	
	// Ensure padding is never negative
	if padding < 0 {
		padding = 0
	}

	return lipgloss.NewStyle().Foreground(borderColor).Render("│  ") +
		content +
		strings.Repeat(" ", padding) +
		lipgloss.NewStyle().Foreground(borderColor).Render("│\n")
}

func renderRateLimit(used, limit int, resetTime time.Time, width int, borderColor lipgloss.AdaptiveColor, headerStyle lipgloss.Style) string {
	var rateLimitText string
	if limit > 0 {
		resetStr := formatTimeRemaining(resetTime)
		rateLimitText = fmt.Sprintf("%s / %s used, resets in %s",
			formatNumber(used), formatNumber(limit), resetStr)
	} else {
		rateLimitText = "? / ? used, resets ?"
	}

	content := headerStyle.Render("🚀 Rate Limit    ") + rateLimitText
	contentLen := visibleLength(content)
	padding := width - contentLen - 5
	
	// Ensure padding is never negative
	if padding < 0 {
		padding = 0
	}

	return lipgloss.NewStyle().Foreground(borderColor).Render("│  ") +
		content +
		strings.Repeat(" ", padding) +
		lipgloss.NewStyle().Foreground(borderColor).Render("│\n")
}

func renderActivityHeader(width int, borderColor lipgloss.AdaptiveColor, headerStyle lipgloss.Style) string {
	content := headerStyle.Render("💬 Activity")
	contentLen := visibleLength(content)
	padding := width - contentLen - 5
	
	// Ensure padding is never negative
	if padding < 0 {
		padding = 0
	}

	return lipgloss.NewStyle().Foreground(borderColor).Render("│  ") +
		content +
		strings.Repeat(" ", padding) +
		lipgloss.NewStyle().Foreground(borderColor).Render("│\n")
}

func renderLogLine(entry logEntry, width int, borderColor lipgloss.AdaptiveColor, errorStyle lipgloss.Style) string {
	timestamp := entry.time.Format("15:04:05")
	message := entry.message

	// Truncate message if too long
	availableSpace := width - len(timestamp) - 8 // 8 = "│     " + " " + "│"
	if len(message) > availableSpace {
		if availableSpace > 3 {
			message = message[:availableSpace-3] + "..."
		} else {
			message = message[:availableSpace]
		}
	}

	// Color error messages
	var styledMessage string
	if strings.Contains(entry.message, "❌") || strings.Contains(entry.message, "Error:") {
		styledMessage = errorStyle.Render(message)
	} else {
		styledMessage = message
	}

	content := timestamp + " " + styledMessage
	contentLen := visibleLength(content)
	padding := width - contentLen - 7 // 7 = "│     " + "│"

	return lipgloss.NewStyle().Foreground(borderColor).Render("│     ") +
		content +
		strings.Repeat(" ", padding) +
		lipgloss.NewStyle().Foreground(borderColor).Render("│\n")
}

func renderEmptyActivityLine(width int, borderColor lipgloss.AdaptiveColor) string {
	return lipgloss.NewStyle().Foreground(borderColor).Render("│     ") +
		strings.Repeat(" ", width-7) +
		lipgloss.NewStyle().Foreground(borderColor).Render("│\n")
}
