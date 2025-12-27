# github-brain

AI coding agent specification. Human documentation in README.md. Read https://github.blog/ai-and-ml/generative-ai/spec-driven-development-using-markdown-as-a-programming-language-when-building-with-ai/ to understand the approach.

Keep the app in one file `main.go`.

## CLI

Running `github-brain` without arguments starts the main interactive TUI. The only subcommand is `mcp` which starts the MCP server.

```
github-brain [-m <home>]      # Start interactive TUI
github-brain mcp [args]       # Start MCP server
```

If the GitHub Brain home directory doesn't exist, create it.

Concurrency control:

- Prevent concurrent `pull` operations using database lock
- Return error if `pull` already running
- Lock renewal: 1-second intervals
- Lock expiration: 5 seconds
- Release the lock when `pull` finishes
- `mcp` command can run concurrently

Use RFC3339 date format consistently.
Use https://pkg.go.dev/log/slog for logging (`slog.Info`, `slog.Error`). Do not use `fmt.Println` or `log.Println`.

## Main TUI

When `github-brain` is run without arguments, display an interactive menu:

```
╭────────────────────────────────────────────────────────────────╮
│  GitHub 🧠                                                      │
│                                                                │
│  > Setup     Configure authentication and settings             │
│    Pull      Sync GitHub data to local database                │
│    Quit      Exit                                              │
│                                                                │
│  Status: Not logged in                                         │
│                                                                │
│  Press Enter to select, q to quit                              │
│                                                                │
│  dev (unknown)                                                 │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

After successful login with organization configured:

```
╭────────────────────────────────────────────────────────────────╮
│  GitHub 🧠                                                      │
│                                                                │
│    Setup     Configure authentication and settings             │
│  > Pull      Sync GitHub data to local database                │
│    Quit      Exit                                              │
│                                                                │
│  Status: Logged in as @wham (my-org)                           │
│                                                                │
│  Press Enter to select, q to quit                              │
│                                                                │
│  dev (unknown)                                                 │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

### Menu Navigation

- Use arrow keys (↑/↓) or j/k to navigate
- Press Enter to select
- Press Esc to go back (in submenus)
- Press q or Ctrl+C to quit
- Highlight current selection with `>`

### Menu Items

1. **Setup** - Opens the setup submenu (see [Setup Menu](#setup-menu) section)
2. **Pull** - Runs the pull operation (see [pull](#pull) section)
3. **Quit** - Exit the application

### Default Selection

- If user is logged in AND organization is configured → default to **Pull**
- Otherwise → default to **Setup**

### Status Line

Display current authentication status:

- `Not logged in` - No GITHUB_TOKEN in .env
- `Logged in as @username` - Token exists and is valid, but no organization
- `Logged in as @username (org)` - Token and organization configured

Check token validity on startup by making a GraphQL query for `viewer { login }`.

### Flow

1. On startup, check if GITHUB_TOKEN exists and is valid
2. Show menu with appropriate status and default selection
3. When user selects Setup, show the setup submenu
4. When user selects Pull, prompt for organization if not set, then run pull
5. After pull completes, return to menu
6. When user selects Quit, exit cleanly

## Setup Menu

The Setup submenu provides authentication and configuration options:

```
╭────────────────────────────────────────────────────────────────╮
│  GitHub 🧠 Setup                                                │
│                                                                │
│  > Login with GitHub (OAuth)                                   │
│    Login with Personal Access Token                            │
│    Open configuration file                                     │
│    ← Back                                                      │
│                                                                │
│  Press Enter to select, Esc to go back                         │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

### Setup Menu Items

1. **Login with GitHub (OAuth)** - Runs the OAuth device flow (see [OAuth Login](#oauth-login) section)
2. **Login with Personal Access Token** - Manually enter a PAT (see [PAT Login](#pat-login) section)
3. **Open configuration file** - Opens `.env` file in default editor
4. **← Back** - Return to main menu

### Open Configuration File

Opens the `.env` file located at `{HomeDir}/.env` using the system default editor:

- Use `open` command on macOS
- Use `xdg-open` command on Linux
- Create the file if it doesn't exist (empty file)
- Show brief message and return to setup menu

### Bubble Tea Integration

Use **Bubble Tea** framework (https://github.com/charmbracelet/bubbletea) for terminal UI:

- **Core packages:**
  - `github.com/charmbracelet/bubbletea` - TUI framework (Elm Architecture)
  - `github.com/charmbracelet/lipgloss` - Styling and layout
  - `github.com/charmbracelet/bubbles/spinner` - Built-in animated spinners
- **Architecture:**
  - Main menu is the root Bubble Tea model
  - Login and Pull are sub-views that take over the screen temporarily
  - Background goroutines send messages to update UI via `tea.Program.Send()`
  - Framework handles all cursor positioning, screen clearing, and render batching
  - Window resize events handled automatically via `tea.WindowSizeMsg`
- **Implementation:**
  - `UIProgress` struct wraps `tea.Program` and implements `ProgressInterface`
  - No manual ANSI escape codes or cursor management
  - No Console struct needed - Bubble Tea handles everything
  - Messages sent to model via typed message structs (e.g., `itemUpdateMsg`, `logMsg`)
- **Graceful shutdown:**
  - `UIProgress` has a `done` channel to track when `Run()` completes
  - The goroutine running `Run()` closes the `done` channel when finished
  - `Stop()` calls `Quit()` then waits on the `done` channel before returning
  - This ensures alternate screen mode is properly exited and terminal state is restored
- **Playful enhancements:**
  - Animated spinner using `bubbles/spinner` with Dot style
  - Smooth color transitions for status changes (pending → active → complete)
  - Celebration emojis at milestones (✨ at 1000+ items, 🎉 at 5000+)
  - Gradient animated borders (purple → blue → cyan) updated every second
  - Right-aligned comma-formatted counters

## OAuth Login

Interactive GitHub authentication using OAuth Device Flow. Stores the resulting token in the `.env` file.

### OAuth App

The app uses a registered OAuth App for authentication:

- **Client ID**: Embedded in the binary (public, safe to commit)
- **Client Secret**: Not required for device flow (public clients)
- **Scopes**: `read:org repo` (read organization data and full repository access)

### Device Flow

1. Request device code from GitHub:

   ```
   POST https://github.com/login/device/code
   client_id=<CLIENT_ID>&scope=read:org repo
   ```

2. GitHub returns:

   - `device_code`: Secret code for polling
   - `user_code`: Code user enters (e.g., `ABCD-1234`)
   - `verification_uri`: `https://github.com/login/device`
   - `expires_in`: Code expiration (usually 900 seconds)
   - `interval`: Polling interval (usually 5 seconds)

3. Display the code and open browser:

   ```
   ╭────────────────────────────────────────────────────────────────╮
   │  GitHub 🧠 Login                                               │
   │                                                                │
   │  🔐 GitHub Authentication (OAuth)                              │
   │                                                                │
   │  1. Opening browser to: github.com/login/device                │
   │                                                                │
   │  2. Enter this code:                                           │
   │                                                                │
   │     ╭──────────────────╮                                       │
   │     │    ABCD-1234     │                                       │
   │     ╰──────────────────╯                                       │
   │                                                                │
   │  ⠋ Waiting for authorization...                                │
   │                                                                │
   │  Press Ctrl+C to cancel                                        │
   │                                                                │
   ╰────────────────────────────────────────────────────────────────╯
   ```

4. Poll for access token:

   ```
   POST https://github.com/login/oauth/access_token
   client_id=<CLIENT_ID>&device_code=<DEVICE_CODE>&grant_type=urn:ietf:params:oauth:grant-type:device_code
   ```

5. Handle poll responses:

   - `authorization_pending`: Keep polling
   - `slow_down`: Increase interval by 5 seconds
   - `expired_token`: Code expired, start over
   - `access_denied`: User denied, show error
   - Success: Returns `access_token` (long-lived, does not expire)

6. On success, prompt for organization:

   ```
   ╭────────────────────────────────────────────────────────────────╮
   │  GitHub 🧠 Login                                               │
   │                                                                │
   │  ✅ Successfully authenticated as @wham                        │
   │                                                                │
   │  Enter your GitHub organization (optional):                    │
   │  > my-org█                                                     │
   │                                                                │
   │  Press Enter to skip, or type organization name                │
   │                                                                │
   ╰────────────────────────────────────────────────────────────────╯
   ```

7. Save tokens (and organization if provided) to `.env` file:

   ```
   ╭────────────────────────────────────────────────────────────────╮
   │  GitHub 🧠 Login                                               │
   │                                                                │
   │  ✅ Setup complete!                                            │
   │                                                                │
   │  Logged in as: @wham                                           │
   │  Organization: my-org                                          │
   │  Saved to: ~/.github-brain/.env                                │
   │                                                                │
   │  Press any key to continue...                                  │
   │                                                                │
   ╰────────────────────────────────────────────────────────────────╯
   ```

8. Return to main menu after key press.

## PAT Login

Manual authentication using a Personal Access Token (PAT). Useful when OAuth flow is not available or when using fine-grained tokens.

### PAT Flow

1. Open browser to pre-filled PAT creation page:

   ```
   https://github.com/settings/personal-access-tokens/new?name=github-brain&description=http%3A%2F%2Fgithub.com%2Fwham%2Fgithub-brain&issues=read&pull_requests=read&discussions=read
   ```

2. Display token input screen:

   ```
   ╭────────────────────────────────────────────────────────────────╮
   │  GitHub 🧠 Login                                               │
   │                                                                │
   │  🔑 Personal Access Token                                      │
   │                                                                │
   │  1. Create a token at github.com (opened in browser)           │
   │                                                                │
   │  2. Paste your token here:                                     │
   │  > github_pat_█                                                │
   │                                                                │
   │  Press Enter to continue, Esc to cancel                        │
   │                                                                │
   ╰────────────────────────────────────────────────────────────────╯
   ```

3. Verify the token by calling `viewer { login }` GraphQL query

4. On success, prompt for organization (same as OAuth flow)

5. Save token and organization to `.env` file

6. Return to main menu

### Token Storage

Save token and organization to `{HomeDir}/.env` file:

- If `.env` exists and has `GITHUB_TOKEN`, replace it
- If `.env` exists without `GITHUB_TOKEN`, append it
- If `.env` doesn't exist, create it
- Same logic for `ORGANIZATION`

Format:

```
GITHUB_TOKEN=gho_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
ORGANIZATION=my-org
```

OAuth App tokens are long-lived and do not expire unless revoked.

### Implementation Notes

- Use Bubble Tea for the interactive UI (consistent with `pull` command)
- Use `github.com/pkg/browser` to open the verification URL
- Use `github.com/charmbracelet/bubbles/textinput` for organization input
- Poll interval: Start with GitHub's `interval` value (usually 5 seconds)
- Timeout: Code expires after `expires_in` seconds (usually 15 minutes)
- After saving token, verify it works by fetching `viewer { login }`

## pull

Accessed from the main menu. Before starting pull:

1. Check if `ORGANIZATION` is set in environment/`.env`
2. If not set, prompt user to enter organization name (similar to login flow)
3. Save organization to `.env` if entered
4. Proceed with pull operation

Config resolution:

- `Organization`: From `.env` or prompted (required)
- `GithubToken`: From `.env` (required - redirect to login if missing)
- `HomeDir`: GitHub Brain home directory (default: `~/.github-brain`)
- `DBDir`: SQLite database path, constructed as `<HomeDir>/db`
- `ExcludedRepositories`: From `EXCLUDED_REPOSITORIES` env var (comma-separated, optional)

Operation:

- Verify no concurrent `pull` execution
- Measure GraphQL request rate every second. Adjust `/` spin speed based on rate
- If `Config.Force` is set, remove all data from database before pulling. If `Config.Items` is set, only remove specified items
- Pull items: Repositories, Discussions, Issues, Pull Requests
- Always pull all items (no selective sync from TUI)
- Maintain console output showing selected items and status
- Use `log/slog` custom logger for last 5 log messages with timestamps in console output
- On completion or error, show "Press any key to continue..." and return to main menu

### Console Rendering with Bubble Tea

Bubble Tea handles all rendering automatically:

- No manual cursor management or screen clearing
- No debouncing or mutex locks needed
- Automatic terminal resize handling
- Smooth animations with `tea.Tick`
- Background goroutines send messages to update UI via channels

Console at the beginning of pull:

```
╭────────────────────────────────────────────────────────────────╮
│  GitHub 🧠 Pull                                                 │
│                                                                │
│  📋 Repositories                                               │
│  📋 Discussions                                                │
│  📋 Issues                                                     │
│  📋 Pull-requests                                              │
│                                                                │
│  📊 API Status    ✅ 0   🟡 0   ❌ 0                           │
│  🚀 Rate Limit    ? / ? used, resets ?                        │
│                                                                │
│  💬 Activity                                                   │
│     21:37:12 ✨ Summoning data from the cloud...              │
│     21:37:13 🔍 Fetching current user info                    │
│                                                                │
│                                                                │
│                                                                │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

Console during first item pull:

```
╭────────────────────────────────────────────────────────────────╮
│  GitHub 🧠 Pull                                                 │
│                                                                │
│  ⠋ Repositories: 1,247                                        │
│  📋 Discussions                                                │
│  📋 Issues                                                     │
│  📋 Pull-requests                                              │
│                                                                │
│  📊 API Status    ✅ 120   🟡 1   ❌ 2                         │
│  🚀 Rate Limit    1,000 / 5,000 used, resets in 2h 15m        │
│                                                                │
│  💬 Activity                                                   │
│     21:37:54 📦 Wrangling repositories...                     │
│     21:37:55 📄 Fetching page 12                              │
│     21:37:56 💾 Processing batch 3 (repos 201-300)            │
│     21:37:57 ⚡ Rate limit: 89% remaining                     │
│     21:37:58 ✨ Saved 47 repositories to database             │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

Console when first item completes:

```
╭────────────────────────────────────────────────────────────────╮
│  GitHub 🧠 Pull                                                 │
│                                                                │
│  ✅ Repositories: 2,847                                        │
│  ⠙ Discussions: 156                                           │
│  📋 Issues                                                     │
│  📋 Pull-requests                                              │
│                                                                │
│  📊 API Status    ✅ 160   🟡 1   ❌ 2                         │
│  🚀 Rate Limit    1,500 / 5,000 used, resets in 1h 45m        │
│                                                                │
│  💬 Activity                                                   │
│     21:41:23 🎉 Repositories completed (2,847 synced)          │
│     21:41:24 💬 Herding discussions...                         │
│     21:41:25 📄 Fetching from auth-service                    │
│     21:41:26 💾 Processing batch 1                             │
│     21:41:27 ✨ Found 23 new discussions                       │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

Console when an error occurs:

```
╭────────────────────────────────────────────────────────────────╮
│  GitHub 🧠 Pull                                                 │
│                                                                │
│  ✅ Repositories: 2,847                                        │
│  ❌ Discussions: 156 (errors)                                  │
│  📋 Issues                                                     │
│  📋 Pull-requests                                              │
│                                                                │
│  📊 API Status    ✅ 160   🟡 1   ❌ 5                         │
│  🚀 Rate Limit    1,500 / 5,000 used, resets in 1h 45m        │
│                                                                │
│  💬 Activity                                                   │
│     21:42:15 ❌ API Error: Rate limit exceeded                 │
│     21:42:16 ⏳ Retrying in 30 seconds...                      │
│     21:42:47 ⚠️  Repository access denied: private-repo        │
│     21:42:48 ➡️  Continuing with next repository...            │
│     21:42:49 ❌ Failed to save discussion #4521                │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

### Console Icons

- 📋 = Pending (not started)
- ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏ = Spinner (active item, bright blue)
- ✅ = Completed (bright green)
- ❌ = Failed (bright red)

### Layout

- Gradient animated borders (purple → blue → cyan) updated every second
- Responsive width: `max(76, terminalWidth - 4)`
- Box expands to full terminal width
- Numbers formatted with commas: `1,247`
- Time format: `15:04:05` (HH:MM:SS)
- Rate limit: friendly format like `2h 15m`
- API Status: ✅ success, 🟡 warning (yellow circle), ❌ errors
- Note: Avoid ⚠️ emoji (has variation selector causing width issues)

### Implementation Notes

**Bubble Tea Message Handling:**

- All UI updates use `tea.Program.Send()` to send typed messages
- Model's `Update()` method processes messages and returns new state
- View automatically re-renders when model changes
- No manual cursor or screen management needed

**Box Drawing:**

- Use standard lipgloss borders - no custom border painting or string manipulation
- Rounded borders (╭╮╰╯) styled with `lipgloss.RoundedBorder()`
- Title rendered as bold text inside the box, not embedded in border
- Border colors animated via `tickMsg` sent every second
- Responsive width: `max(64, terminalWidth - 4)`

**Spinners:**

- Use `bubbles/spinner` with Dot style (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏)
- Spinner state managed by Bubble Tea's `spinner.Model`
- Only one spinner shown at a time (for active item)
- Spinner ticks handled via `spinner.TickMsg`

**Number Formatting:**

- Add commas to numbers > 999: `1,247` not `1247`
- Helper function: `formatNumber(n int) string`
- Used in item counts, API stats, and rate limit display

**Time Formatting:**

- Activity logs: `15:04:05` format (HH:MM:SS only)
- Rate limit resets: `formatTimeRemaining()` returns friendly format like "2h 15m"

**Window Resize:**

- Listen for `tea.WindowSizeMsg` in model's `Update()`
- Store width/height in model state
- Layout adjusts automatically on next render

**Color Scheme:**

- Purple/blue gradient for borders (via `borderColors` array)
- Bright blue (#12) for active items
- Bright green (#10) for completed ✅
- Dim gray (#240) for skipped 🔕
- Bright red (#9) for failed ❌
- Applied via `lipgloss.NewStyle().Foreground()`

**Milestone Celebrations:**

- 1,000 items: ✨ emoji in log
- 5,000 items: 🎉 emoji in log
- 10,000 items: 🚀✨🎉 emojis in log
- Triggered in `itemCompleteMsg` handler

### Repositories

- Get most recent `updated_at` timestamp from database for repositories
- Query repositories for `Config.Organization`

```graphql
{
  organization(login: "<organization>") {
    repositories(
      isArchived: false
      isFork: false
      first: 100
      after: null
      orderBy: { field: UPDATED_AT, direction: DESC }
    ) {
      nodes {
        name
        hasDiscussionsEnabled
        hasIssuesEnabled
        updatedAt
      }
      pageInfo {
        hasNextPage
        endCursor
      }
    }
  }
}
```

- Query repositories ordered by `updatedAt` descending
- Stop pulling when hitting repository with `updatedAt` older than recorded timestamp
- Save each repository immediately. Avoid storing all repositories in memory. No long-running transactions
- Save or update by primary key `name`
- Filter `isArchived: false` and `isFork: false` for faster processing
- Ignore `Config.ExcludedRepositories` in this step, OK to save them in the database

### Discussions

- Query discussions for each repository with `has_discussions_enabled: true` and not in `Config.ExcludedRepositories`
- Record most recent repository discussion `updated_at` timestamp from database before pulling first page

```graphql
{
  repository(owner: "github", name: "licensing") {
    discussions(first: 100, orderBy: { field: UPDATED_AT, direction: DESC }) {
      nodes {
        url
        title
        body
        createdAt
        updatedAt
        author {
          login
        }
      }
    }
  }
}
```

- If provided repository doesn't exist, GraphQL will return following error:

```
{
  "data": {
    "repository": null
  },
  "errors": [
    {
      "type": "NOT_FOUND",
      "path": [
        "repository"
      ],
      "locations": [
        {
          "line": 2,
          "column": 3
        }
      ],
      "message": "Could not resolve to a Repository with the name 'github/non-existing-repository'."
    }
  ]
}
```

- If repository doesn't exist, remove the repository, and all associated discussions, issues, and pull requests from the database and continue
- Query discussions ordered by most recent `updatedAt`
- Stop pulling when hitting discussions with `updatedAt` older than recorded timestamp
- Save each discussion immediately. Avoid storing all discussions in memory. No long-running transactions
- Save or update by primary key `url`
- Preserve the discussion markdown body

### Issues

- Query issues for each repository which has `has_issues_enabled: true` and not in `Config.ExcludedRepositories`
- Record most recent repository issue `updated_at` timestamp from database before pulling first page

```graphql
{
  repository(owner: "<organiation>", name: "<repository>") {
    issues(first: 100, orderBy: { field: UPDATED_AT, direction: DESC }) {
      nodes {
        url
        title
        body
        createdAt
        updatedAt
        closedAt
        author {
          login
        }
      }
    }
  }
}
```

- If provided repository doesn't exist, GraphQL will return following error:

```
{
  "data": {
    "repository": null
  },
  "errors": [
    {
      "type": "NOT_FOUND",
      "path": [
        "repository"
      ],
      "locations": [
        {
          "line": 2,
          "column": 3
        }
      ],
      "message": "Could not resolve to a Repository with the name 'github/non-existing-repository'."
    }
  ]
}
```

- If repository doesn't exist, remove the repository, and all associated discussions, issues, and pull requests from the database and continue
- Query issues ordered by most recent `updatedAt`
- Stop pulling when hitting issue with `updatedAt` older than recorded timestamp
- Only pull issues updated in the last 400 days. Stop pulling when hitting issue updated older than that
- Save each issue immediately. Avoid storing all issues in memory. No long-running transactions
- Save or update by primary key `url`
- Preserve the issue markdown body

### Pull Requests

- Query pull requests for each repository that's not in `Config.ExcludedRepositories`
- Record most recent repository pull request `updated_at` timestamp from database before pulling first page

```graphql
{
  repository(owner: "<organiation>", name: "<repository>") {
    pullRequests(first: 100, orderBy: { field: UPDATED_AT, direction: DESC }) {
      nodes {
        url
        title
        body
        createdAt
        updatedAt
        closedAt
        mergedAt
        author {
          login
        }
      }
    }
  }
}
```

- If provided repository doesn't exist, GraphQL will return following error:

```
{
  "data": {
    "repository": null
  },
  "errors": [
    {
      "type": "NOT_FOUND",
      "path": [
        "repository"
      ],
      "locations": [
        {
          "line": 2,
          "column": 3
        }
      ],
      "message": "Could not resolve to a Repository with the name 'github/non-existing-repository'."
    }
  ]
}
```

- If repository doesn't exist, remove the repository, and all associated discussions, issues, and pull requests from the database and continue
- Query pull requests ordered by most recent `updatedAt`
- Stop pulling when hitting pull request with `updatedAt` older than recorded timestamp
- Only pull pull requests updated in the last 400 days. Stop pulling when hitting pull request updated older than that
- Save each pull request immediately. Avoid storing all pull requests in memory. No long-running transactions
- Save or update by primary key `url`
- Preserve the pull request markdown body

### Finally - Rebuild Search Index

- Fetch the current authenticated user with:

```graphql
{
  viewer {
    login
  }
}
```

- Truncate `search` FTS5 table and repopulate it from `discussions`, `issues`, and `pull_requests` tables
- When repopulating the search index:
  1. Use the current authenticateduser's login stored in memory
  2. Query for all unique repository names where the user is the author in `discussions`, `issues`, or `pull_requests`
  3. For each item being inserted into the search table, calculate `boost` on the fly:
     - Set to `2.0` if the item's repository is in the user's contribution set (2x boost)
     - Set to `1.0` otherwise (no boost)
  4. Insert into search table with the calculated `boost` value

### Current User

- Fetch the current authenticated user before processing repositories
- This step always runs, even when using `-i` to select specific items

```graphql
{
  viewer {
    login
  }
}
```

- Store the username in memory for use during search index rebuild
- This is a single quick request that should complete immediately

## mcp

- Use the official MCP SDK for Go: https://github.com/modelcontextprotocol/go-sdk
- Important: Pull library repository for docs/examples instead of using `go doc`
- Update library to latest version when modifying MCP code
- Use stdio MCP transport
- Implement only tools listed below

### Tools

When parameters are required, use `mcp.Required()` to mark them as required. Do not include _required_ in the parameter description.

#### list_discussions

Lists discussions with optional filtering. Discussions are separated by `---`.

##### Parameters

- `repository`: Filter by repository name. Example: `auth-service`. Defaults to any repository in the organization.
- `created_from`: Filter by `created_at` after the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `created_to`: Filter by `created_at` before the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `authors`: Array of author usernames. Example: `[john_doe, jane_doe]`. Defaults to any author.
- `fields`: Array of fields to include in the response. Available fields: `["title", "url", "repository", "created_at", "author", "body"]`. Defaults to all fields.

##### Response

Validate `fields` parameter. If it contains invalid fields, output:

```
Invalid fields: <invalid_fields>

Use one of the available fields: <available_fields>
```

Where `<invalid_fields>` is a comma-separated list of invalid fields, and `<available_fields>` is a comma-separated list of available fields.

Next, prepare the query statement and execute. Order by `created_at` ascending. If no discussions are found, output:

```
No discussions found.
```

If discussions are found, start looping through them and output for each:

```
## <title>

- URL: <url>
- Repository: <repository>
- Created at: <created_at>
- Author: <author>

<body>

---
```

The example above includes all fields. If `fields` parameter is provided, only include those fields in the output.

While looping through discussions, keep track of the total size of the response. If the next discussions would take the response size
over 990 kb, stop the loop. Prepend the response with:

```
Showing only the first <n> discussions. There's <x> more, please refine your search. Use `created_from` and `created_to` parameters
to narrow the results.

---
```

Where `<n>` is the number of discussions shown, and `<x>` is the number of discussions not shown.

#### list_issues

Lists issues with optional filtering. Issues are separated by `---`.

##### Parameters

- `repository`: Filter by repository name. Example: `auth-service`. Defaults to any repository in the organization.
- `created_from`: Filter by `created_at` after the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `created_to`: Filter by `created_at` before the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `closed_from`: Filter by `closed_at` after the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `closed_to`: Filter by `closed_at` before the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `authors`: Array of author usernames. Example: `[john_doe, jane_doe]`. Defaults to any author.
- `fields`: Array of fields to include in the response. Available fields: `["title", "url", "repository", "created_at", "closed_at", "author", "status", "body"]`. Defaults to all fields.

##### Response

Validate `fields` parameter. If it contains invalid fields, output:

```
Invalid fields: <invalid_fields>

Use one of the available fields: <available_fields>
```

Where `<invalid_fields>` is a comma-separated list of invalid fields, and `<available_fields>` is a comma-separated list of available fields.

Next, prepare the query statement and execute. Order by `created_at` ascending. If no issues are found, output:

```
No issues found.
```

If issues are found, start looping through them and output for each:

```
## <title>

- URL: <url>
- Repository: <repository>
- Created at: <created_at>
- Closed at: <closed_at>
- Author: <author>
- Status: <open/closed>

<body>

---
```

The example above includes all fields. If `fields` parameter is provided, only include those fields in the output.

While looping through issues, keep track of the total size of the response. If the next issue would take the response size
over 990 kb, stop the loop. Prepend the response with:

```
Showing only the first <n> issues. There's <x> more, please refine your search.

---
```

Where `<n>` is the number of issues shown, and `<x>` is the number of issues not shown.

#### list_pull_requests

Lists pull requests with optional filtering. Pull requests are separated by `---`.

##### Parameters

- `repository`: Filter by repository name. Example: `auth-service`. Defaults to any repository in the organization.
- `created_from`: Filter by `created_at` after the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `created_to`: Filter by `created_at` before the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `merged_from`: Filter by `merged_at` after the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `merged_to`: Filter by `merged_at` before the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `closed_from`: Filter by `closed_at` after the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `closed_to`: Filter by `closed_at` before the specified date. Example: `2025-06-18T19:19:08Z`. Defaults to any date.
- `authors`: Array of author usernames. Example: `[john_doe, jane_doe]`. Defaults to any author.
- `fields`: Array of fields to include in the response. Available fields: `["title", "url", "repository", "created_at", "merged_at", "closed_at", "author", "status", "body"]`. Defaults to all fields.

##### Response

Validate `fields` parameter. If it contains invalid fields, output:

```
Invalid fields: <invalid_fields>

Use one of the available fields: <available_fields>
```

Where `<invalid_fields>` is a comma-separated list of invalid fields, and `<available_fields>` is a comma-separated list of available fields.

Next, prepare the query statement and execute. Order by `created_at` ascending. If no pull requests are found, output:

```
No pull requests found.
```

If pull requests are found, output:

```
Total <x> pull requests found.
```

Next, start looping through them and output for each:

```
## <title>

- URL: <url>
- Repository: <repository>
- Created at: <created_at>
- Merged at: <merged_at>
- Closed at: <closed_at>
- Author: <author>
- Status: <open/closed>

<body>

---
```

The example above includes all fields. If `fields` parameter is provided, only include those fields in the output.

While looping through pull requests, keep track of the total size of the response. If the next pull request would take the response size
over 990 kb, stop the loop. Prepend the response with:

```
Showing only the first <n> pull requests. There's <x> more, please refine your search.

---
```

Where `<n>` is the number of pull requests shown, and `<x>` is the number of pull requests not shown.

#### search

Full-text search across discussions, issues, and pull requests.

##### Parameters

- `query`: Search query string. Example: `authentication bug`. (required)
- `fields`: Array of fields to include in the response. Available fields: `["title", "url", "repository", "created_at", "author", "type", "state", "body"]`. Defaults to all fields.

##### Response

Validate `fields` parameter. If it contains invalid fields, output:

```
Invalid fields: <invalid_fields>

Use one of the available fields: <available_fields>
```

Where `<invalid_fields>` is a comma-separated list of invalid fields, and `<available_fields>` is a comma-separated list of available fields.

Next, prepare the FTS5 search query using the `search` table. Build the query with:

- Use FTS5 MATCH operator for the search query
- Order by `bm25(search)` for optimal relevance ranking (titles are weighted 3x higher)
- Limit to 10 results
- Use the unified SearchEngine implementation shared with the UI

If no results are found, output:

```
No results found for "<query>".
```

If results are found, start looping through results and output for each:

```
## <title>

- URL: <url>
- Type: <type>
- Repository: <repository>
- Created at: <created_at>
- Author: <author>
- State: <state>

<body>

---
```

The example above includes all fields. If `fields` parameter is provided, only include those fields in the output.

For the `body` field, show the full content from the matched item.

### Prompts

Each prompt should just return the template string with parameter interpolation, and the MCP client will handle calling the actual tools.

#### user_summary

Generates a summary of the user's accomplishments based on created discussions, closed issues, and closed pull requests.

#### Parameters

- `username`: Username. Example: `john_doe`. (required)
- `period`: Examples "last week", "from August 2025 to September 2025", "2024-01-01 - 2024-12-31"

##### Prompt

Summarize the accomplishments of the user `<username>` during `<period>`, focusing on the most significant contributions first. Use the following approach:

- Use `list_discussions` to gather discussions they created within `<period>`.
- Use `list_issues` to gather issues they closed within `<period>`.
- Use `list_pull_requests` to gather pull requests they closed within `<period>`.
- Aggregate all results, removing duplicates.
- Prioritize and highlight:
  - Discussions (most important)
  - Pull requests (next most important)
  - Issues (least important)
- For each contribution, include a direct link and relevant metrics or facts.
- Present a concise, unified summary that mixes all types of contributions, with the most impactful items first.

## GitHub

Use GitHub's GraphQL API exclusively. Use https://github.com/shurcooL/githubv4 package. 100 results per page, max 100 concurrent requests (GitHub limit).

### Rate Limit and Network Handling

Implement comprehensive error handling with unified retry and recovery strategies:

**Primary Rate Limit (Points System):**

- GitHub uses a points-based system: 5,000 points per hour for personal access tokens
- Each query consumes points based on complexity (minimum 1 point)
- Track headers: `x-ratelimit-limit`, `x-ratelimit-remaining`, `x-ratelimit-used`, `x-ratelimit-reset`
- When exceeded: response status `200` with error message, `x-ratelimit-remaining` = `0`
- Wait until `x-ratelimit-reset` time before retrying

**Secondary Rate Limits (Abuse Prevention):**

- No more than 100 concurrent requests (shared across REST and GraphQL)
- No more than 2,000 points per minute for GraphQL endpoint
- No more than 90 seconds of CPU time per 60 seconds of real time
- When exceeded: status `200` or `403` with error message
- If `retry-after` header present: wait that many seconds
- If no `retry-after`: wait at least 1 minute, then exponential backoff
- GraphQL queries without mutations = 1 point, with mutations = 5 points (for secondary limits)

**Unified Error Handling:**

- Centralize all error handling in `handleGraphQLError` function
- Maximum 10 retries per request with exponential backoff (5s base, 30m max wait)
- Handle different error types:
  - Primary rate limit: wait until `x-ratelimit-reset` + 30s buffer, retry indefinitely
  - Secondary rate limit: use `retry-after` header or wait 1+ minutes with exponential backoff
  - Network errors (`EOF`, `connection reset`, `broken pipe`, `i/o timeout`): wait 60-120s with jitter
  - 5xx server errors: exponential backoff retry
  - Repository not found: no retry, remove from database
  - Timeouts (>10 seconds): GitHub terminates request, additional points deducted next hour
- Clear stale rate limit states after extended failures (>5 minutes)
- Reset HTTP client connection pool on persistent network failures

**Proactive Management:**

- Check global rate limit state before each request
- Add conservative delays between requests based on points utilization:
  - > 90% points used: 3-5 seconds delay
  - > 70% points used: 2-3 seconds delay
  - > 50% points used: 1-2 seconds delay
  - Normal: 1-1.5 seconds delay (GitHub recommends 1+ second between mutations)
  - During recovery: 5-10 seconds depending on error type

**Concurrency and Timeouts:**

- Limit concurrent requests to 50 using semaphore (conservative limit to prevent rate limiting)
- Global rate limit state shared across all goroutines with mutex protection
- Context cancellation support for all wait operations
- Request timeout: 10 seconds (GitHub's server timeout)
- Page-level timeout: 5 minutes
- Global operation timeout: 3 minutes for repository processing completion

Avoid making special request to get page count. For the first page request,
you don't have to display the page count since you don't know it yet. For subsequent pages, you can display the page number in the status message.

## Database

SQLite database in `{Config.DbDir}/{Config.Organization}.db` (create folder if needed). Avoid transactions. Save each GraphQL item immediately. Use `github.com/mattn/go-sqlite3` package. Build with FTS5 support.

### Database Versioning System

The application uses a simple GUID-based versioning system to handle schema changes:

- Single schema version GUID for the entire database
- On any schema change, generate a new unique GUID
- At startup, check if stored schema GUID matches current GUID
- If different or missing, drop entire database and recreate from scratch

#### Schema Version

```go
const SCHEMA_GUID = "550e8400-e29b-41d4-a716-446655440001" // Change this GUID on any schema modification
```

#### Startup Flow

1. Check if database exists and has a `schema_version` table
2. If table exists, read the stored GUID and compare with `SCHEMA_GUID`
3. If GUID matches, proceed normally
4. If GUID is different or missing:
   - Log schema version mismatch
   - Drop entire database file
   - Create new database with current schema
   - Store current `SCHEMA_GUID` in `schema_version` table

#### Schema Change Process

1. Update table definitions, indexes, or constraints in code
2. Generate new unique GUID and update `SCHEMA_GUID` constant
3. On next startup, application detects GUID mismatch
4. Drops entire database and recreates with new schema
5. All data is re-fetched from GitHub APIs

### Tables

#### table:discussions

- Primary key: `url`
- Index: `repository`
- Index: `author`
- Index: `created_at`
- Index: `updated_at`
- Index: `repository, created_at`

- `url`: Primary key (e.g., `https://github.com/org/repo/discussions/1`)
- `title`: Discussion title
- `body`: Discussion content
- `created_at`: Creation timestamp (e.g., `2023-01-01T00:00:00Z`)
- `updated_at`: Last update timestamp
- `repository`: Repository name, without organization prefix (e.g., `repo`)
- `author`: Username

#### table:issues

- Primary key: `url`
- Index: `repository`
- Index: `author`
- Index: `created_at`
- Index: `updated_at`
- Index: `closed_at`
- Index: `repository, created_at`

- `url`: Primary key (e.g., `https://github.com/org/repo/issues/1`)
- `title`: Issue title
- `body`: Issue content
- `created_at`: Creation timestamp (e.g., `2023-01-01T00:00:00Z`)
- `updated_at`: Last update timestamp
- `closed_at`: Optional close timestamp (e.g., `2023-01-01T00:00:00Z`). Null if open.
- `repository`: Repository name, without organization prefix (e.g., `repo`)
- `author`: Username

#### table:pull_requests

- Primary key: `url`
- Index: `repository`
- Index: `author`
- Index: `created_at`
- Index: `updated_at`
- Index: `merged_at`
- Index: `closed_at`
- Index: `repository, created_at`

- `url`: Pull request URL (e.g., `https://github.com/org/repo/pulls/1`)
- `title`: Pull request title
- `body`: Pull request content
- `created_at`: Creation timestamp (e.g., `2023-01-01T00:00:00Z`)
- `updated_at`: Last update timestamp
- `merged_at`: Optional merge timestamp (e.g., `2023-01-01T00:00:00Z`). Null if not merged.
- `closed_at`: Optional close timestamp (e.g., `2023-01-01T00:00:00Z`). Null if open.
- `repository`: Repository name, without organization prefix (e.g., `repo`)
- `author`: Username

#### table:repositories

- Primary key: `name`
- Index: `updated_at`

- `name`: Repository name (e.g., `repo`), without organization prefix
- `has_discussions_enabled`: Boolean indicating if the repository has discussions feature enabled
- `has_issues_enabled`: Boolean indicating if the repository has issues feature enabled
- `updated_at`: Last update timestamp

#### table:search

- FTS5 virtual table for full-text search across discussions, issues, and pull requests
- Indexed columns: `type`, `title`, `body`, `url`, `repository`, `author`
- Unindexed columns: `created_at`, `state`, `boost`
- `boost`: Numeric value (e.g., `1.0`, `2.0`) used to multiply BM25 scores for ranking
- Uses `bm25(search, 1.0, 2.0, 1.0, 1.0, 1.0, 1.0)` ranking with 2x title weight for relevance scoring
- Search results should be ordered by: `(bm25(search) * boost)` for optimal relevance
  - Items from user's repositories get 2x boost, ensuring they appear higher in results
  - This approach is more flexible than boolean flags and allows for future ranking adjustments

#### table:schema_version

- Stores the current schema GUID for version tracking
- Single row table with `guid` column
- Used to detect schema changes and trigger database recreation

### Database Performance

Performance indexes are implemented to optimize common query patterns:

#### Date-based queries

- Single-column indexes on `created_at`, `updated_at`, `closed_at`, `merged_at` optimize date range filtering and `ORDER BY` operations
- Used by MCP tools for date-filtered queries (e.g., `created_from`, `created_to` parameters)

#### Repository-specific queries

- Composite indexes on `(repository, created_at)` optimize queries that filter by repository and sort by date
- Critical for incremental sync operations using `MAX(updated_at)` queries

#### Incremental sync optimization

- Index on `repositories.updated_at` optimizes `MAX(updated_at)` queries for determining last sync timestamps

## Distribution

### Installation

**Quick install (recommended):**

```bash
npm install -g github-brain
```

NPM handles:

- Platform detection (macOS, Linux, Windows)
- Architecture detection (x64, arm64)
- Automatic binary download via `optionalDependencies`
- PATH configuration
- Easy updates: `npm update -g github-brain`
- Easy uninstall: `npm uninstall -g github-brain`

**Manual installation:**
Download the appropriate archive for your platform from [releases](https://github.com/wham/github-brain/releases):

```bash
# Specific version
curl -L https://github.com/wham/github-brain/releases/download/v1.2.3/github-brain-darwin-arm64.tar.gz | tar xz
```

## Code Quality

### Linting

Use **golangci-lint** with default configuration for code quality checks.

**Running the linter:**

```bash
# Standalone
golangci-lint run

# Integrated with build (via scripts/run)
./scripts/run [command]
```

**CI Integration:**

- Linting runs automatically on all PRs via `.github/workflows/build.yml`
- Build fails if linter finds issues (blocking)
- In local development (`scripts/run`), linting runs but is non-blocking to allow rapid iteration

### Release Model

Coded in `.github/workflow/release.yml` and `.github/workflow/build.yml`.

- **Semantic Versioning**: Automatically bump version on every merge to `main` based on PR labels
- **PR Label Requirements** (build fails without one of these):
  - `major` - Breaking changes, incompatible API changes (e.g., 1.0.0 → 2.0.0)
  - `minor` - New features, backward compatible (e.g., 1.0.0 → 1.1.0)
  - `patch` - Bug fixes, backward compatible (e.g., 1.0.0 → 1.0.1)
- **Starting version**: 1.0.0
- **Version storage**: GitHub releases (reads latest release tag, increments based on PR label)
- **Release artifacts**: GitHub release created with tag `v{version}` (e.g., v1.2.3)
- **NPM packages**: Main package and platform packages all published with same version
- **NPM handles "latest"**: No need for GitHub "latest" release - npm automatically serves latest version

### Binary Versioning

- Embed version and build date at compile time:
  ```bash
  go build -ldflags "-X main.Version={semver} -X main.BuildDate=$(date -u +%Y-%m-%d)"
  ```
- Display with `--version`: `github-brain 1.2.3 (2025-10-29)`
- NPM package version always matches binary version

### Build Targets

Read https://github.com/mattn/go-sqlite3?tab=readme-ov-file#compiling to understand CGO requirements for SQLite FTS5 support.

- `darwin-amd64` - Intel Macs
- `darwin-arm64` - Apple Silicon
- `linux-amd64` - x86_64 servers/desktops
- `linux-arm64` - ARM servers (AWS Graviton, Raspberry Pi)
- `windows-amd64` - Windows machines

### Artifacts

**GitHub Releases:**

- Archives: `github-brain-{platform}.tar.gz` (Unix), `.zip` (Windows)
- Executables inside archives: `github-brain` (Unix), `github-brain.exe` (Windows)
- Checksums: `SHA256SUMS.txt` for verification
- Tagged with version: `v1.2.3`

**NPM Packages:**

- Main package: `github-brain` - contains Node.js shim and installation logic
- Platform packages: `github-brain-{platform}-{arch}` - contain platform-specific binaries
  - `github-brain-darwin-arm64`
  - `github-brain-darwin-x64`
  - `github-brain-linux-arm64`
  - `github-brain-linux-x64`
  - `github-brain-windows`
- All packages published with same version number

**Build System:**

- Built natively on platform-specific GitHub Actions runners (ubuntu-latest, macos-latest, windows-latest)
- Linux ARM64 cross-compiled using `gcc-aarch64-linux-gnu`
- CGO build flags: `CGO_CFLAGS="-DSQLITE_ENABLE_FTS5"`, `CGO_LDFLAGS="-lm"` (Linux only)
