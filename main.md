# github-brain

AI coding agent specification. Human documentation in README.md.

## CLI

Implement CLI from [Usage](README.md#usage) section. Follow exact argument/variable names. Support only `pull`, `mcp`, and `ui` commands.

Concurrency control:

- Prevent concurrent `pull` commands using database lock
- Return error if `pull` already running
- Lock renewal: 1-second intervals
- Lock expiration: 5 seconds
- Release the lock when `pull` finishes
- Other commands (`mcp`, `ui`) can run concurrently

Use RFC3339 date format consistently.
Use https://pkg.go.dev/log/slog for logging (`slog.Info`, `slog.Error`). Do not use `fmt.Println` or `log.Println`.

## pull

- Verify no concurrent `pull` execution
- Measure GraphQL request rate every second. Adjust `/` spin speed based on rate
- Resolve CLI arguments and environment variables into `Config` struct:
  - `Organization`: Organization name (required)
  - `GithubToken`: GitHub API token (required)
  - `DBDir`: SQLite database path (default: `./db`)
  - `Items`: Comma-separated list to pull (default: empty - pull all)
  - `Force`: Remove all data before pulling (default: false)
  - `ExcludedRepositories`: Comma-separated list of repositories to exclude from the pull of discussions, issues, and pull-requests (default: empty)
- Use `Config` struct consistently, avoid multiple environment variable reads
- If `Config.Force` is set, remove all data from database before pulling. If `Config.Items` is set, only remove specified items
- Pull items: Repositories, Discussions, Issues, Pull Requests
- Maintain console output showing selected items and status
- Use `log/slog` custom logger for last 5 log messages with timestamps in console output

### Console Rendering Requirements

Console display must be stable and prevent jumping/flickering:

- Establish fixed display area of exactly 13 lines (4 items + 1 empty + 2 status + 1 empty + 5 logs)
- Save/restore cursor position to maintain original terminal position
- Use atomic rendering: build complete output in memory, then write once
- Implement debounced updates with minimum 200ms interval to prevent excessive refreshing
- Detect terminal size and ensure display fits within bounds
- Use proper mutex locking to prevent overlapping renders
- Clear entire display area before each update to prevent stale content

Console at the beginning of the `pull` command - all items selected:

```
┌─ GitHub 🧠 pull ─────────────────────────────────────────────┐
│                                                                │
│  ⚪ Repositories                                               │
│  ⚪ Discussions                                                │
│  ⚪ Issues                                                     │
│  ⚪ Pull Requests                                              │
│                                                                │
│  📊 API Status    ✅ 0   ⚠️ 0   ❌ 0                            │
│  🚀 Rate Limit    ? / ? used, resets ?                         │
│                                                                │
│  💬 Activity                                                   │
│     [timestamp] Starting GitHub data synchronization...        │
│     [timestamp] <log message>                                  │
│     [timestamp] <log message>                                  │
│     [timestamp] <log message>                                  │
│     [timestamp] <log message>                                  │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

### Modern Console Design Elements

- **Box Drawing**: Use Unicode box-drawing characters for elegant borders
- **Emojis & Icons**: Modern visual indicators for status and categories
- **Color Scheme**:
  - ⚪ White circle for pending items
  - 🔄 Blue spinner for active items
  - ✅ Green checkmark for completed items
  - 🔕 Gray circle for skipped items
  - ❌ Red X for failed items
- **Sections**: Clear visual separation with headers and spacing
- **Responsive Layout**: Adjust to terminal width (minimum 64 chars)

Console at the beginning of the `pull` command - `-i repositories`:

```
┌─ GitHub 🧠 pull ─────────────────────────────────────────────┐
│                                                                │
│  ⚪ Repositories                                               │
│  🔕 Discussions                                               │
│  🔕 Issues                                                    │
│  🔕 Pull Requests                                             │
│                                                                │
│  📊 API Status    ✅ 0   ⚠️ 0   ❌ 0                          │
│  🚀 Rate Limit    ? / ? used, resets ?                        │
│                                                                │
│  💬 Activity                                                   │
│     [timestamp] Starting selective sync...                    │
│     [timestamp] <log message>                                 │
│     [timestamp] <log message>                                 │
│     [timestamp] <log message>                                 │
│     [timestamp] <log message>                                 │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

Note 🔕 for skipped items with dimmed appearance.

Console during first item pull:

```
┌─ GitHub 🧠 pull ─────────────────────────────────────────────┐
│                                                                │
│  🔄 Repositories: 1,247                                       │
│  ⚪ Discussions                                                │
│  ⚪ Issues                                                     │
│  ⚪ Pull Requests                                              │
│                                                                │
│  📊 API Status    ✅ 120   ⚠️ 1   ❌ 2                        │
│  🚀 Rate Limit    1000/5000 used, resets in 2h 15m           │
│                                                                │
│  💬 Activity                                                   │
│     21:37:54 Clearing existing repositories...                │
│     21:37:55 Fetching page 12 of repositories                 │
│     21:37:56 Processing batch 3 (repos 201-300)               │
│     21:37:57 Rate limit: 89% remaining                        │
│     21:37:58 Saved 47 repositories to database                │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

- 🔄 Animated spinner that rotates based on request rate
- 1,247 = number of items processed so far with comma formatting
- Time format shows only HH:MM:SS for brevity
- Rate limit shows friendly "resets in Xh Ym" format

Console when first item completes:

```
┌─ GitHub 🧠 pull ─────────────────────────────────────────────┐
│                                                                │
│  ✅ Repositories: 2,847                                       │
│  🔄 Discussions: 156                                          │
│  ⚪ Issues                                                     │
│  ⚪ Pull Requests                                              │
│                                                                │
│  📊 API Status    ✅ 160   ⚠️ 1   ❌ 2                        │
│  🚀 Rate Limit    1500/5000 used, resets in 1h 45m           │
│                                                                │
│  💬 Activity                                                   │
│     21:41:23 ✅ Repositories completed (2,847 synced)         │
│     21:41:24 🔄 Starting discussions sync...                  │
│     21:41:25 Fetching discussions from repo-1                 │
│     21:41:26 Processing discussions batch 1                   │
│     21:41:27 Found 23 new discussions                         │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

- ✅ Green checkmark for completed items with final count
- 🔄 Blue spinner automatically moves to next active item
- Completion announcements in activity log with emojis

Console when an error occurs:

```
┌─ GitHub 🧠 pull ─────────────────────────────────────────────┐
│                                                                │
│  ✅ Repositories: 2,847                                       │
│  ❌ Discussions: 156 (3 errors)                               │
│  ⚪ Issues                                                     │
│  ⚪ Pull Requests                                              │
│                                                                │
│  📊 API Status    ✅ 160   ⚠️ 1   ❌ 5                        │
│  🚀 Rate Limit    1500/5000 used, resets in 1h 45m           │
│                                                                │
│  💬 Activity                                                   │
│     21:42:15 ❌ API Error: Rate limit exceeded                │
│     21:42:16 🔄 Retrying in 30 seconds...                     │
│     21:42:47 ⚠️  Repository access denied: private-repo       │
│     21:42:48 🔄 Continuing with next repository...            │
│     21:42:49 ❌ Failed to save discussion #4521               │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

- ❌ Red X for items with errors, showing error count
- Error details in activity log with appropriate emoji indicators
- System continues processing after recoverable errors

### Modern Console Implementation Requirements

**Box Drawing Characters:**

- Top border: `┌─` + title + `─` repeated + `┐`
- Side borders: `│` with proper padding
- Bottom border: `└` + `─` repeated + `┘`
- Minimum width: 64 characters, scales with terminal width

**Emoji Status Indicators:**

- ⚪ Pending (white circle)
- 🔄 Active (blue arrows, animate between: 🔄🔃⚡🔁)
- ✅ Completed (green checkmark)
- 🔕 Skipped (muted bell with gray text)
- ❌ Failed (red X)

**Number Formatting:**

- Use comma separators for numbers > 999: `1,247`
- Show error counts in parentheses: `156 (3 errors)`
- Right-align counts within available space

**Time Formatting:**

- Activity logs: `HH:MM:SS` format only
- Rate limit resets: Human-friendly `2h 15m`, `45m`, `30s`
- No timezone display (use local time)

**Responsive Layout:**

- Minimum 64 characters width
- Scale sections proportionally for wider terminals
- Listen for SIGWINCH signal to detect terminal resize events
- Dynamically update table width and re-render when terminal is resized
- Truncate long messages with `...` if needed
- Maintain fixed box structure regardless of content

**Color Scheme:**

- Box borders: Bright white/cyan
- Section headers: Bold white with emojis
- Completed items: Green text + ✅
- Active items: Blue text + animated spinner
- Skipped items: Gray/dim text + 🔕
- Failed items: Red text + ❌
- Log messages: Default white, errors in red

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

### Finally

- Truncate `search_X` FTS5 table and repopulate it from `discussions_X`, `issues_X`, and `pull_requests_X` tables (where X represents current version numbers)

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
- Use template define blocks to re-use `index.html` for search results
- Use https://htmx.org as the frontend framework. Vanilla JS/CSS otherwise
- Implement Google-style layout: large search input with instant results below
- Use HTMX for dynamic search and result updates
- Display top 10 results
- Design: modern brutalism with purple accents, dark theme
- Use the unified SearchEngine with FTS5 `bm25(search)` ranking (same as MCP)

## GitHub

Use GitHub's GraphQL API exclusively. Use https://github.com/shurcooL/githubv4 package. 100 results per page, max 50 concurrent requests.

Get the content of https://docs.github.com/en/graphql/overview/rate-limits-and-node-limits-for-the-graphql-api to learn how to handle rate limits. Use the
defined headers to determine the current rate limit and remaining requests. When you reach the limit,
pause new requests until the rate limit resets.

Handle both primary and secondary rate limits.

If you encounter a 5xx server error, retry the request once after a short delay.

Avoid making special request to get page count. For the first page request,
you don't have to display the page count since you don't know it yet. For subsequent pages, you can display the page number in the status message.

Centralize error handling into a single function. Use for each GraphQL query.

## Database

SQLite database in `{Config.DbDir}/{Config.Organization}.db` (create folder if needed). Avoid transactions. Save each GraphQL item immediately. Use `github.com/mattn/go-sqlite3` package. Build with FTS5 support.

### Database Versioning

#### Table-Specific Version Management

- Each table has its own version number suffix: `<table_name>_<version>`
- Version numbers are simple integers that increment with each schema change per table
- Each table maintains its own version constant in the code
- At startup, drop all tables with different version numbers than expected
- Create tables with current version suffix if they don't exist

#### Version Constants

````go
const (
    REPOSITORIES_VERSION   = 1  // repositories_1
    DISCUSSIONS_VERSION    = 1  // discussions_1
    ISSUES_VERSION         = 1  // issues_1
    PULL_REQUESTS_VERSION  = 1  // pull_requests_1
    SEARCH_VERSION         = 1  // search_1
)
```#### Startup Flow

1. For each table type (repositories, discussions, issues, pull_requests, search):
   - Check for existing tables with pattern `<table_name>_*`
   - Drop any tables that don't match the current version suffix
   - Create table with current version suffix if it doesn't exist
2. Log cleanup actions (dropped old versions, created new tables)

#### Schema Change Process

1. Update table definitions, indexes, or constraints in code
2. Increment the appropriate table version constant
3. On next startup, application detects version mismatch for that table only
4. Drops old version(s) of that specific table and creates new version
5. Other tables remain untouched if their versions haven't changed

#### Example Table Names

- `repositories_1` (current version)
- `discussions_1` (current version)
- `issues_1` (current version)
- `pull_requests_1` (current version)
- `search_1` (current version)

### Tables

All table names include version suffixes (e.g., `discussions_2`, `issues_1`). Use the version constants to generate actual table names in queries.

#### table:discussions_X (where X = DISCUSSIONS_VERSION)

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

#### table:issues_X (where X = ISSUES_VERSION)

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

#### table:pull_requests_X (where X = PULL_REQUESTS_VERSION)

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

#### table:repositories_X (where X = REPOSITORIES_VERSION)

- Primary key: `name`
- Index: `updated_at`

- `name`: Repository name (e.g., `repo`), without organization prefix
- `has_discussions_enabled`: Boolean indicating if the repository has discussions feature enabled
- `has_issues_enabled`: Boolean indicating if the repository has issues feature enabled
- `updated_at`: Last update timestamp

#### table:search_X (where X = SEARCH_VERSION)

- FTS5 virtual table for full-text search across discussions, issues, and pull requests
- Indexed columns: `type`, `title`, `body`, `url`, `repository`, `author`
- Unindexed columns: `created_at`, `state`
- Uses `bm25(search, 1.0, 2.0, 1.0, 1.0, 1.0, 1.0)` ranking with 2x title weight for relevance scoring
- Use workaround https://stackoverflow.com/questions/40877078/drop-a-table-originally-created-with-unknown-tokenizer?rq=1 to drop the table

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
````
