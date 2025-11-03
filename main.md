# github-brain

AI coding agent specification. Human documentation in README.md. Read https://github.blog/ai-and-ml/generative-ai/spec-driven-development-using-markdown-as-a-programming-language-when-building-with-ai/ to understand the approach.

## CLI

Implement CLI from [Usage](README.md#usage) section. Follow exact argument/variable names. Support only `pull`, `mcp`, and `ui` commands.

If the GitHub Brain home directory doesn't exist, create it.

Concurrency control:

- Prevent concurrent `pull` commands using database lock
- Return error if `pull` already running
- Lock renewal: 1-second intervals
- Lock expiration: 5 seconds
- Release the lock when `pull` finishes
- Other commands (`mcp`, `ui`) can run concurrently

Use RFC3339 date format consistently.
Use https://pkg.go.dev/log/slog for logging (`slog.Info`, `slog.Error`). Do not use `fmt.Println` or `log.Println`.

### Bubble Tea Integration

Use **Bubble Tea** framework (https://github.com/charmbracelet/bubbletea) for terminal UI:

- **Core packages:**
  - `github.com/charmbracelet/bubbletea` - TUI framework (Elm Architecture)
  - `github.com/charmbracelet/lipgloss` - Styling and layout
  - `github.com/charmbracelet/bubbles/spinner` - Built-in animated spinners
- **Architecture:**
  - Bubble Tea Model holds UI state (item counts, status, logs)
  - Background goroutines send messages to update UI (no manual rendering)
  - Framework handles all cursor positioning, screen clearing, and render batching
  - Window resize events handled automatically
- **Playful enhancements:**
  - Different spinner styles for each item type (dots, lines, bouncing)
  - Smooth color transitions for status changes (pending → active → complete)
  - Celebration emojis at milestones (✨ at 1000+ items, 🎉 at 5000+)
  - Fun loading messages ("Wrangling repositories...", "Herding discussions...")
  - Gradient animated borders (purple → blue → cyan)
  - Gentle "breathing" animation when idle

## pull

- Verify no concurrent `pull` execution
- Measure GraphQL request rate every second. Adjust `/` spin speed based on rate
- Resolve CLI arguments and environment variables into `Config` struct:
  - `Organization`: Organization name (required)
  - `GithubToken`: GitHub API token (required)
  - `HomeDir`: GitHub Brain home directory (default: `~/.github-brain`)
  - `DBDir`: SQLite database path, constructed as `<HomeDir>/db`
  - `Items`: Comma-separated list to pull (default: empty - pull all)
  - `Force`: Remove all data before pulling (default: false)
  - `ExcludedRepositories`: Comma-separated list of repositories to exclude from the pull of discussions, issues, and pull-requests (default: empty)
- Use `Config` struct consistently, avoid multiple environment variable reads
- If `Config.Force` is set, remove all data from database before pulling. If `Config.Items` is set, only remove specified items
- Pull items: Repositories, Discussions, Issues, Pull Requests
- Maintain console output showing selected items and status
- Use `log/slog` custom logger for last 5 log messages with timestamps in console output

### Console Rendering with Bubble Tea

Bubble Tea handles all rendering automatically:

- No manual cursor management or screen clearing
- No debouncing or mutex locks needed
- Automatic terminal resize handling
- Smooth animations with `tea.Tick`
- Background goroutines send messages to update UI via channels

Console at the beginning of the `pull` command - all items selected:

```
╭─ GitHub 🧠 pull ─────────────────────────────────────────────╮
│                                                                │
│  ⋯ Repositories                                                │
│  ⋯ Discussions                                                 │
│  ⋯ Issues                                                      │
│  ⋯ Pull Requests                                               │
│                                                                │
│  📊 API Status    ✅ 0   ⚠️ 0   ❌ 0                            │
│  🚀 Rate Limit    ? / ? used, resets ?                         │
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

### Modern Console Design Elements

- **Box Drawing**: Use lipgloss rounded or thick borders
- **Emojis & Icons**: Visual status indicators throughout
- **Animated Spinners**: Different styles per item type (dots, lines, bouncing points)
- **Color Scheme**:
  - Dim gray for pending/skipped items
  - Bright blue for active items (with spinner)
  - Bright green for completed ✅
  - Bright red for failed ❌
  - Purple accents for borders and highlights
- **Gradient Borders**: Animated color rotation (purple → blue → cyan)
- **Responsive Layout**: Minimum 64 chars, scales to terminal width
- **Playful Touches**:
  - Random loading messages from a pool
  - Milestone emoji bursts (🎉✨🚀 at 1000, 5000, 10000)
  - Comma-formatted counters that "bounce" when updating
  - Gentle border pulse animation when idle

Console at the beginning of the `pull` command - `-i repositories`:

```
╭─ GitHub 🧠 pull ─────────────────────────────────────────────╮
│                                                                │
│  ⋯ Repositories                                                │
│  🔕 Discussions                                               │
│  🔕 Issues                                                    │
│  🔕 Pull Requests                                             │
│                                                                │
│  📊 API Status    ✅ 0   ⚠️ 0   ❌ 0                          │
│  🚀 Rate Limit    ? / ? used, resets ?                        │
│                                                                │
│  💬 Activity                                                   │
│     21:37:12 🎯 Starting selective sync...                   │
│     21:37:13 📦 Clearing existing repositories...            │
│                                                                │
│                                                                │
│                                                                │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

Note: 🔕 for skipped items with dimmed text, ⋯ for pending (animated dots).

Console during first item pull:

```
╭─ GitHub 🧠 pull ─────────────────────────────────────────────╮
│                                                                │
│  ⠋ Repositories: 1,247                                        │
│  ⋯ Discussions                                                 │
│  ⋯ Issues                                                      │
│  ⋯ Pull Requests                                               │
│                                                                │
│  📊 API Status    ✅ 120   ⚠️ 1   ❌ 2                        │
│  🚀 Rate Limit    1,000 / 5,000 used, resets in 2h 15m       │
│                                                                │
│  💬 Activity                                                   │
│     21:37:54 📦 Wrangling repositories...                    │
│     21:37:55 📄 Fetching page 12                             │
│     21:37:56 💾 Processing batch 3 (repos 201-300)           │
│     21:37:57 ⚡ Rate limit: 89% remaining                    │
│     21:37:58 ✨ Saved 47 repositories to database            │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

Notes:

- ⠋ = Animated spinner (rotates: ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏)
- ⋯ = Pending animated dots
- Numbers formatted with commas: 1,247
- Time format: HH:MM:SS only
- Rate limit: friendly "2h 15m" format

Console when first item completes:

```
╭─ GitHub 🧠 pull ─────────────────────────────────────────────╮
│                                                                │
│  ✅ Repositories: 2,847                                       │
│  ⠙ Discussions: 156                                           │
│  ⋯ Issues                                                      │
│  ⋯ Pull Requests                                               │
│                                                                │
│  📊 API Status    ✅ 160   ⚠️ 1   ❌ 2                        │
│  🚀 Rate Limit    1,500 / 5,000 used, resets in 1h 45m       │
│                                                                │
│  💬 Activity                                                   │
│     21:41:23 🎉 Repositories completed (2,847 synced)         │
│     21:41:24 💬 Herding discussions...                        │
│     21:41:25 📄 Fetching from auth-service                   │
│     21:41:26 💾 Processing batch 1                            │
│     21:41:27 ✨ Found 23 new discussions                      │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

Notes:

- ✅ marks completed items (green text)
- ⠙ spinner automatically moves to next active item (blue text)
- Completion announcements with celebration emojis

Console when an error occurs:

```
╭─ GitHub 🧠 pull ─────────────────────────────────────────────╮
│                                                                │
│  ✅ Repositories: 2,847                                       │
│  ❌ Discussions: 156 (3 errors)                               │
│  ⋯ Issues                                                      │
│  ⋯ Pull Requests                                               │
│                                                                │
│  📊 API Status    ✅ 160   ⚠️ 1   ❌ 5                        │
│  🚀 Rate Limit    1,500 / 5,000 used, resets in 1h 45m       │
│                                                                │
│  💬 Activity                                                   │
│     21:42:15 ❌ API Error: Rate limit exceeded                │
│     21:42:16 ⏳ Retrying in 30 seconds...                     │
│     21:42:47 ⚠️  Repository access denied: private-repo       │
│     21:42:48 ➡️  Continuing with next repository...           │
│     21:42:49 ❌ Failed to save discussion #4521               │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

Notes:

- ❌ marks failed items (red text) with error count in parentheses
- Error details logged with appropriate emoji indicators
- System continues processing after recoverable errors

### Implementation Notes

**Box Drawing:**

- Use lipgloss rounded borders (╭╮╰╯) instead of sharp corners
- Apply purple/blue gradient to border colors
- Animate border colors on a 1-second tick

**Spinners:**

- Use `bubbles/spinner` with Dot style (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏)
- Create separate spinner instances for each item type
- Only render spinner for currently active item

**Number Formatting:**

- Add commas to numbers > 999: `1,247` not `1247`
- Format in activity logs and status counters
- Right-align counters when possible

**Time Formatting:**

- Activity logs: `21:37:54` (HH:MM:SS only)
- Rate limit resets: `2h 15m`, `45m`, `30s` (human-friendly)

**Window Resize:**

- Listen for `tea.WindowSizeMsg`
- Adjust box width: `max(64, terminalWidth - 4)`
- Re-render layout automatically

**Color Scheme:**

- Purple/blue for borders and title
- Bright blue for active items (with spinner)
- Bright green for completed ✅
- Dim gray for skipped 🔕
- Bright red for failed ❌

**Loading Messages Pool:**

```
"Wrangling repositories..."
"Herding discussions..."
"Catching issues..."
"Corralling pull requests..."
"Summoning data from the cloud..."
"Asking GitHub nicely..."
"Negotiating with the API..."
"Convincing servers to cooperate..."
```

**Milestone Celebrations:**

- 1,000 items: ✨
- 5,000 items: 🎉
- 10,000 items: 🚀
- Show brief celebration message in activity log

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

- Use https://github.com/mark3labs/mcp-go
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

## ui

- Use `net/http` package with [template/html](https://pkg.go.dev/html/template) for rendering
- Save all HTML/JS/CSS code in `index.html`
- Embed all static assets using `embed` package
- Use template define blocks to re-use `index.html` for search results
- Use https://htmx.org as the frontend framework. Vanilla JS/CSS otherwise
- Implement Google-style layout: large search input with instant results below
- Use HTMX for dynamic search and result updates
- Display top 10 results
- Design: modern brutalism with purple accents, dark theme
- Use the unified SearchEngine with FTS5 `bm25(search)` ranking (same as MCP)

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
