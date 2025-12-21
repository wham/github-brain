# github-brain

AI coding agent specification. Human documentation in README.md. Read https://github.blog/ai-and-ml/generative-ai/spec-driven-development-using-markdown-as-a-programming-language-when-building-with-ai/ to understand the approach.

Keep the app in one file `main.go`.

## CLI

Implement CLI from [Usage](README.md#usage) section. Follow exact argument/variable names. Support only `login`, `pull`, and `mcp` commands.

If the GitHub Brain home directory doesn't exist, create it.

Concurrency control:

- Prevent concurrent `pull` commands using database lock
- Return error if `pull` already running
- Lock renewal: 1-second intervals
- Lock expiration: 5 seconds
- Release the lock when `pull` finishes
- Other commands (`mcp`) can run concurrently

Use RFC3339 date format consistently.
Use https://pkg.go.dev/log/slog for logging (`slog.Info`, `slog.Error`). Do not use `fmt.Println` or `log.Println`.

### Bubble Tea Integration

Use **Bubble Tea** framework (https://github.com/charmbracelet/bubbletea) for terminal UI:

- **Core packages:**
  - `github.com/charmbracelet/bubbletea` - TUI framework (Elm Architecture)
  - `github.com/charmbracelet/lipgloss` - Styling and layout
  - `github.com/charmbracelet/bubbles/spinner` - Built-in animated spinners
  - `github.com/charmbracelet/bubbles/progress` - Progress bars with smooth animations
  - `github.com/charmbracelet/bubbles/help` - Interactive help key bindings
- **Architecture:**
  - Bubble Tea Model holds UI state (item counts, status, logs)
  - Background goroutines send messages to update UI via `tea.Program.Send()`
  - Framework handles all cursor positioning, screen clearing, and render batching
  - Window resize events handled automatically via `tea.WindowSizeMsg`
- **Implementation:**
  - `UIProgress` struct wraps `tea.Program` and implements `ProgressInterface`
  - No manual ANSI escape codes or cursor management
  - No Console struct needed - Bubble Tea handles everything
  - Messages sent to model via typed message structs (e.g., `itemUpdateMsg`, `logMsg`)
- **Enhanced Playful Design:**
  - **Visual Polish:**
    - Multiple animated spinner styles - alternate between Dot, Line, Points every 10 seconds
    - Smooth progress bars showing completion percentage for each sync item
    - Rainbow gradient animated borders (purple → blue → cyan → green → yellow) cycling every 800ms
    - Pulsing glow effect on active items using lipgloss gradients
    - Rich color palette using lipgloss adaptive colors for light/dark terminal themes
  - **Interactive Features:**
    - Keyboard shortcuts: `p` to pause/resume, `s` for stats view, `h` for help, `q` to quit
    - Live keyboard help footer showing available actions
    - Toggle between compact and detailed view modes with `d` key
    - Copy stats to clipboard with `c` key (using bubbles/clipboard)
  - **Playful Animations:**
    - Celebration emojis at milestones: ✨ 100 items, 🎊 1,000 items, 🎉 5,000 items, 🚀 10,000 items
    - Animated "bouncing" emoji for active operations (moves position slightly)
    - Success confetti animation when all items complete (burst of colored dots)
    - Smooth count-up animation for numbers using easing functions
    - Item checkmarks animate in with a "pop" effect (scale from small to normal)
  - **Enhanced Information Display:**
    - Horizontal progress bars below each item showing percentage complete
    - Estimated time remaining (ETA) calculated from rate and shown per item
    - Real-time throughput display (items/second) with sparkline mini-chart
    - Color-coded rate limit indicator: green (>70%), yellow (30-70%), red (<30%)
    - Animated wave pattern in background of box for visual interest
  - **Smart Status Messages:**
    - Context-aware activity messages that adapt to what's happening
    - Show current repository name being synced in real-time
    - Display batch sizes and pagination info dynamically
    - Rotating fun facts about GitHub during long operations
    - Tip messages about features during idle moments

## login

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

3. Display the code and open browser (enhanced design):

   ```
   ╭────────────────────────────────────────────────────────────────╮
   │  ✨ GitHub 🧠 Login                                            │
   │                                                                │
   │  ┌──────────────────────────────────────────────────────────┐ │
   │  │  🔐 GitHub Authentication                                 │ │
   │  │                                                           │ │
   │  │  Step 1 of 2: Authorize Access                           │ │
   │  │                                                           │ │
   │  │  ✅ Browser opened automatically                          │ │
   │  │  🌐 github.com/login/device                              │ │
   │  └──────────────────────────────────────────────────────────┘ │
   │                                                                │
   │  📋 Enter this verification code in your browser:             │
   │                                                                │
   │     ╭─────────────────────────────╮                           │
   │     │                             │                           │
   │     │       A B C D - 1 2 3 4     │                           │
   │     │                             │                           │
   │     ╰─────────────────────────────╯                           │
   │                                                                │
   │  ⠋ Waiting for your authorization...                          │
   │                                                                │
   │  ┌──────────────────────────────────────────────────────────┐ │
   │  │  ⏱️  Time remaining: 14m 32s                              │ │
   │  │  🔄 Checking status...                                    │ │
   │  └──────────────────────────────────────────────────────────┘ │
   │                                                                │
   │  💡 Press Ctrl+C to cancel                                    │
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

6. On success, prompt for organization (enhanced design):

   ```
   ╭────────────────────────────────────────────────────────────────╮
   │  ✨ GitHub 🧠 Login                                            │
   │                                                                │
   │  ┌──────────────────────────────────────────────────────────┐ │
   │  │  ✅ Authentication Successful!                            │ │
   │  │                                                           │ │
   │  │  👤 Logged in as: @wham                                   │ │
   │  │  🔑 Access token received                                 │ │
   │  └──────────────────────────────────────────────────────────┘ │
   │                                                                │
   │  Step 2 of 2: Configure Organization (Optional)               │
   │                                                                │
   │  📂 Which GitHub organization would you like to sync?         │
   │                                                                │
   │     ╭─────────────────────────────────────────────────────╮   │
   │     │ > my-org█                                           │   │
   │     ╰─────────────────────────────────────────────────────╯   │
   │                                                                │
   │  💡 Press Enter to skip or type organization name             │
   │  💡 You can change this later in ~/.github-brain/.env         │
   │                                                                │
   ╰────────────────────────────────────────────────────────────────╯
   ```

7. Save tokens (and organization if provided) to `.env` file (enhanced design):
   ```
   ╭────────────────────────────────────────────────────────────────╮
   │  ✨ GitHub 🧠 Login                                            │
   │                                                                │
   │  ┌──────────────────────────────────────────────────────────┐ │
   │  │  🎉 Setup Complete!                                       │ │
   │  │                                                           │ │
   │  │  ✅ All set and ready to go!                             │ │
   │  └──────────────────────────────────────────────────────────┘ │
   │                                                                │
   │  📋 Configuration Summary:                                     │
   │                                                                │
   │     👤 User:         @wham                                     │
   │     🏢 Organization: my-org                                    │
   │     📁 Config:       ~/.github-brain/.env                      │
   │     🔑 Token:        gho_****....**** (hidden for security)    │
   │                                                                │
   │  🚀 Next Steps:                                                │
   │                                                                │
   │     1. Run this command to sync data:                          │
   │        $ github-brain pull                                     │
   │                                                                │
   │     2. Then start the MCP server:                              │
   │        $ github-brain mcp                                      │
   │                                                                │
   │  💡 Tip: Data syncs are incremental - first run takes longer! │
   │                                                                │
   ╰────────────────────────────────────────────────────────────────╯
   ```

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

### Error Handling

When authentication fails or times out:

```
╭────────────────────────────────────────────────────────────────╮
│  ✨ GitHub 🧠 Login                                            │
│                                                                │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │  ❌ Authentication Failed                                 │ │
│  │                                                           │ │
│  │  The verification code has expired or was denied.        │ │
│  └──────────────────────────────────────────────────────────┘ │
│                                                                │
│  🔄 What happened?                                             │
│                                                                │
│     • Code expired after 15 minutes                            │
│     OR                                                         │
│     • Authorization was denied in browser                      │
│                                                                │
│  💡 What to do:                                                │
│                                                                │
│     Run 'github-brain login' again to get a new code          │
│                                                                │
│  📚 Need help? Visit: github.com/wham/github-brain            │
│                                                                │
╰────────────────────────────────────────────────────────────────╯
```

### Implementation Notes

- Use Bubble Tea for the interactive UI (consistent with `pull` command)
- Use `github.com/pkg/browser` to open the verification URL
- Use `github.com/charmbracelet/bubbles/textinput` for organization input
- Poll interval: Start with GitHub's `interval` value (usually 5 seconds)
- Timeout: Code expires after `expires_in` seconds (usually 15 minutes)
- After saving token, verify it works by fetching `viewer { login }`

**Enhanced Visual Elements:**

- Use animated spinner during polling (bubbles/spinner with Dot style)
- Display countdown timer showing time remaining until code expires
- Format verification code with spaces for readability (ABCD-1234)
- Enlarge verification code box with larger padding
- Use nested boxes for status panels (authentication status, summary)
- Rainbow gradient border animation (same as pull command)
- Show progress indicator during polling (checking status...)
- Mask token in final display for security (show only first 4 and last 4 chars)

**Interactive Features:**

- Text input for organization uses bubbles/textinput with cursor
- Input field has rounded border and placeholder text
- Real-time validation feedback as user types
- Clear visual indication of required vs optional fields
- Step indicators (Step 1 of 2, Step 2 of 2) for progress tracking
- Helpful tips at bottom of each screen

**Error States:**

- Clear error messages with visual emphasis (❌ icon, red text)
- Explain what went wrong in plain language
- Provide actionable next steps
- Link to documentation for additional help
- Graceful handling of network errors, timeouts, and denials

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

Console at the beginning of the `pull` command - all items selected (enhanced design):

```
╭────────────────────────────────────────────────────────────────╮
│  ✨ GitHub 🧠 pull                                             │
│                                                                │
│  📦 Repositories                                               │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  💬 Discussions                                                │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  🐛 Issues                                                     │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  🔀 Pull-requests                                              │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │  📊 API Status   ✅ 0   ⚡ 0   ❌ 0                      │  │
│  │  🚦 Rate Limit   ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░░░░ 100%               │  │
│  │  ⏱️  Elapsed      0s                                      │  │
│  └─────────────────────────────────────────────────────────┘  │
│                                                                │
│  💬 Activity                                                   │
│     21:37:12 ✨ Summoning data from the cloud...              │
│     21:37:13 🔍 Authenticating with GitHub API                │
│     21:37:14 🎯 Ready to sync 4 item types!                   │
│                                                                │
│  💡 Tip: Press 'h' for help, 'p' to pause, 'q' to quit       │
╰────────────────────────────────────────────────────────────────╯
```

Console at the beginning of the `pull` command - `-i repositories`:

```
╭────────────────────────────────────────────────────────────────╮
│  ✨ GitHub 🧠 pull                                             │
│                                                                │
│  📦 Repositories                                               │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  🔕 Discussions           (skipped)                            │
│  🔕 Issues                (skipped)                            │
│  🔕 Pull-requests         (skipped)                            │
│                                                                │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │  📊 API Status   ✅ 0   ⚡ 0   ❌ 0                      │  │
│  │  🚦 Rate Limit   ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░░░░ 100%               │  │
│  │  ⏱️  Elapsed      0s                                      │  │
│  └─────────────────────────────────────────────────────────┘  │
│                                                                │
│  💬 Activity                                                   │
│     21:37:12 🎯 Starting selective sync...                    │
│     21:37:13 📦 Clearing existing repositories...             │
│     21:37:14 🔧 Preparing database for fresh data             │
│                                                                │
│  💡 Tip: Use -i to sync specific items                        │
╰────────────────────────────────────────────────────────────────╯
```

Console during first item pull:

```
╭────────────────────────────────────────────────────────────────╮
│  ✨ GitHub 🧠 pull                                             │
│                                                                │
│  ⠋ Repositories: 1,247          📈 ~42/s     ⏱️  ETA 1m 23s  │
│     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░░░░░░░░░░░░░ 44%                         │
│                                                                │
│  💬 Discussions                                                │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  🐛 Issues                                                     │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  🔀 Pull-requests                                              │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │  📊 API Status   ✅ 120   ⚡ 1   ❌ 2                    │  │
│  │  🚦 Rate Limit   ▓▓▓▓▓▓▓▓▓▓▓▓░░░░░░░ 80% │ resets 2h 15m │  │
│  │  ⏱️  Elapsed      2m 34s                                  │  │
│  └─────────────────────────────────────────────────────────┘  │
│                                                                │
│  💬 Activity                                                   │
│     21:37:54 📦 Wrangling repositories... [auth-service]      │
│     21:37:55 📄 Fetching page 12 of ~25                       │
│     21:37:56 💾 Saved batch 3 (repos 201-300)                 │
│     21:37:57 ⚡ Rate limit: 80% remaining                     │
│     21:37:58 ✨ 100 repos synced! Keep going!                 │
│                                                                │
│  💡 Press 's' for detailed stats, 'p' to pause               │
╰────────────────────────────────────────────────────────────────╯
```

Console when first item completes:

```
╭────────────────────────────────────────────────────────────────╮
│  ✨ GitHub 🧠 pull                                             │
│                                                                │
│  ✅ Repositories: 2,847         🎊 Complete!                   │
│     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 100%             │
│                                                                │
│  ⠙ Discussions: 156             📈 ~8/s      ⏱️  ETA 42s     │
│     ▓▓▓░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  12%              │
│                                                                │
│  🐛 Issues                                                     │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  🔀 Pull-requests                                              │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │  📊 API Status   ✅ 160   ⚡ 1   ❌ 2                    │  │
│  │  🚦 Rate Limit   ▓▓▓▓▓▓▓▓▓▓░░░░░░░ 70% │ resets 1h 45m   │  │
│  │  ⏱️  Elapsed      5m 12s                                  │  │
│  └─────────────────────────────────────────────────────────┘  │
│                                                                │
│  💬 Activity                                                   │
│     21:41:23 🎉 Repositories completed! (2,847 synced)        │
│     21:41:24 💬 Now herding discussions... [platform-api]     │
│     21:41:25 📄 Fetching from repository 3 of 47              │
│     21:41:26 💾 Processing batch 1 (12 new discussions)       │
│     21:41:27 ✨ Found 23 new discussions - looking good!      │
│                                                                │
│  💡 Did you know? Use -e to exclude large repos               │
╰────────────────────────────────────────────────────────────────╯
```

Console when an error occurs:

```
╭────────────────────────────────────────────────────────────────╮
│  ✨ GitHub 🧠 pull                                             │
│                                                                │
│  ✅ Repositories: 2,847         🎊 Complete!                   │
│     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 100%             │
│                                                                │
│  ❌ Discussions: 156             ⚠️  Errors encountered        │
│     ▓▓▓▓▓▓░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  28%              │
│                                                                │
│  🐛 Issues                                                     │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  🔀 Pull-requests                                              │
│     ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  0%              │
│                                                                │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │  📊 API Status   ✅ 160   ⚡ 1   ❌ 5                    │  │
│  │  🚦 Rate Limit   ▓▓░░░░░░░░░░░░░░ 30% │ ⚠️  resets 1h 45m│  │
│  │  ⏱️  Elapsed      7m 45s                                  │  │
│  └─────────────────────────────────────────────────────────┘  │
│                                                                │
│  💬 Activity                                                   │
│     21:42:15 ❌ API Error: Rate limit exceeded                 │
│     21:42:16 ⏳ Auto-retry in 30 seconds... [attempt 2/10]    │
│     21:42:47 ⚠️  Repository access denied: private-repo        │
│     21:42:48 ➡️  Skipped - continuing with next repo           │
│     21:42:49 ❌ Failed to save discussion #4521                │
│                                                                │
│  💡 Some errors are normal - check logs after completion      │
╰────────────────────────────────────────────────────────────────╯
```

Console when all operations complete successfully:

```
╭────────────────────────────────────────────────────────────────╮
│  ✨ GitHub 🧠 pull                                             │
│                                                                │
│  ✅ Repositories: 2,847         🎊 Complete!                   │
│     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 100%             │
│                                                                │
│  ✅ Discussions: 1,234          🎊 Complete!                   │
│     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 100%             │
│                                                                │
│  ✅ Issues: 5,678               🎊 Complete!                   │
│     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 100%             │
│                                                                │
│  ✅ Pull-requests: 3,421        🎊 Complete!                   │
│     ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 100%             │
│                                                                │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │  🎉 Sync Complete! 13,180 total items synced              │  │
│  │  📊 API Status   ✅ 427   ⚡ 3   ❌ 2                    │  │
│  │  🚦 Rate Limit   ▓▓▓▓▓░░░░░░░░░ 55% │ resets 45m         │  │
│  │  ⏱️  Total Time   12m 34s                                 │  │
│  └─────────────────────────────────────────────────────────┘  │
│                                                                │
│  💬 Activity                                                   │
│     21:47:23 ✨ Rebuilding search index...                    │
│     21:47:24 🔍 Indexed 13,180 items                          │
│     21:47:25 💾 Optimizing database...                        │
│     21:47:26 ✅ Database optimized                            │
│     21:47:27 🚀 All done! Ready to query with 'mcp' command   │
│                                                                │
│  💡 Your GitHub Brain is now up to date! 🧠✨                 │
╰────────────────────────────────────────────────────────────────╯
```

### Console Icons

**Item Status:**
- 📦 = Repositories (pending/active)
- 💬 = Discussions (pending/active)
- 🐛 = Issues (pending/active)
- 🔀 = Pull-requests (pending/active)
- 🔕 = Disabled (not in `-i` selection), shown with dimmed text
- ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏ = Spinner (active item, bright blue, rotates every 80ms)
- ✅ = Completed (bright green with glow effect)
- ❌ = Failed (bright red with error indicator)

**Progress Indicators:**
- ░ = Empty progress bar segment (dim gray)
- ▓ = Filled progress bar segment (gradient: blue → cyan → green based on percentage)
- Percentage shown at end of bar (0-100%)
- Smooth animation as bar fills

**API Status:**
- ✅ = Successful requests (bright green)
- ⚡ = Warnings/retries (bright yellow)
- ❌ = Errors (bright red)

**Rate Limit Indicator:**
- 🚦 = Rate limit status with visual bar
- Bar color: 
  - Green (▓▓▓▓▓▓▓▓▓▓) when >70% remaining
  - Yellow (▓▓▓▓▓▓) when 30-70% remaining
  - Red (▓▓) when <30% remaining (with ⚠️ warning)

**Activity Log Emojis:**
- ✨ = Milestone reached, special events
- 🎊 = Major milestone (1000+ items)
- 🎉 = Completion celebration (5000+ items)
- 🚀 = Huge milestone (10,000+ items)
- 📦 = Repository operations
- 💬 = Discussion operations
- 🐛 = Issue operations
- 🔀 = Pull request operations
- 📄 = Pagination/fetching
- 💾 = Database operations
- ⚡ = Rate limit info
- 🔍 = Search/query operations
- ⏳ = Waiting/retrying
- ➡️ = Continuation/skipping
- 🎯 = Starting/targeting
- 🔧 = Configuration/setup
- 💡 = Tips and suggestions

### Layout

**Visual Structure:**
- Rainbow gradient animated borders (purple → blue → cyan → green → yellow → orange) cycling every 800ms
- Responsive width: `max(76, terminalWidth - 4)`
- Box expands to full terminal width
- Content padding: 1 space on each side
- Title with sparkle emoji: ✨ GitHub 🧠 pull

**Item Display:**
- Each item has 2 lines: status line + progress bar
- Status line format: `[icon] [name]: [count]     [rate] [ETA]`
- Progress bar uses lipgloss for smooth rendering
- 40 character wide progress bar for consistency
- Real-time count updates with smooth number animation

**Stats Panel:**
- Boxed section with rounded corners (┌─┐ └─┘)
- 3-4 rows of key metrics
- Right-aligned numbers for easy scanning
- Color-coded indicators for quick assessment

**Activity Log:**
- Shows last 5 log entries with timestamps
- Time format: `15:04:05` (HH:MM:SS)
- Auto-scrolls as new entries arrive
- Entries fade color slightly for older messages
- Emoji prefix for visual categorization

**Footer Tips:**
- Rotating helpful tips shown at bottom
- Updates every 30 seconds or on state change
- Context-aware based on current operation
- Keyboard shortcuts highlighted with single quotes

**Number Formatting:**
- Commas for thousands: `1,247` not `1247`
- Percentage with % symbol: `44%`
- Rate format: `~42/s` (approximate items per second)
- Time remaining: `1m 23s` or `2h 15m` format

**Color Palette:**
- Title/headers: Bold white (#FFFFFF)
- Active spinner: Bright blue (#12)
- Complete items: Bright green (#10) with subtle glow
- Failed items: Bright red (#9) with warning
- Disabled items: Dim gray (#240)
- Progress bars: Gradient based on percentage
  - 0-30%: Blue (#12)
  - 30-70%: Cyan (#14)
  - 70-100%: Green (#10)
- Border animation: 6-color rainbow gradient
- Background: Transparent, adapts to terminal theme

### Implementation Notes

**Bubble Tea Message Handling:**

- All UI updates use `tea.Program.Send()` to send typed messages
- Model's `Update()` method processes messages and returns new state
- View automatically re-renders when model changes
- No manual cursor or screen management needed

**Enhanced Message Types:**
- `itemUpdateMsg` - Update item count and status
- `itemCompleteMsg` - Mark item as complete
- `itemErrorMsg` - Mark item as failed
- `progressMsg` - Update progress percentage (0.0-1.0)
- `rateMsg` - Update items/second rate
- `etaMsg` - Update estimated time remaining
- `logMsg` - Add log entry to activity feed
- `celebrationMsg` - Trigger milestone celebration
- `statsMsg` - Update API and rate limit stats
- `tickMsg` - Drive border and animation updates (every 800ms)
- `spinnerTickMsg` - Update spinner animation (every 80ms)
- `tipRotateMsg` - Rotate footer tip (every 30s)

**Box Drawing:**

- Use standard lipgloss borders - no custom border painting or string manipulation
- Rounded borders (╭╮╰╯) styled with `lipgloss.RoundedBorder()`
- Title rendered as bold text inside the box, not embedded in border
- Border colors animated via `tickMsg` sent every 800ms for smoother animation
- Responsive width: `max(76, terminalWidth - 4)`
- Nested box for stats panel using `lipgloss.Border(lipgloss.NormalBorder())`

**Progress Bars:**

- Use `bubbles/progress` package for smooth animations
- Initialize with `progress.New(progress.WithDefaultGradient())`
- Update via `progress.SetPercent(value)` where value is 0.0-1.0
- Width set to 40 characters for consistency across all items
- Custom gradient colors: blue → cyan → green as progress increases
- Render with `progressBar.View()` method

**Spinners:**

- Use `bubbles/spinner` with alternating styles every 10 seconds
- Styles cycle: Dot → Line → Points → back to Dot
- Spinner state managed by Bubble Tea's `spinner.Model`
- Only one spinner shown at a time (for active item)
- Spinner ticks handled via `spinner.TickMsg` at 80ms intervals
- Color: Bright blue (#12) matching theme

**Keyboard Shortcuts:**

- `h` - Toggle help panel showing all keyboard shortcuts
- `p` - Pause/resume sync operations
- `s` - Toggle stats view (expanded metrics)
- `d` - Toggle detailed/compact view modes
- `c` - Copy current stats to clipboard (requires bubbles/clipboard)
- `q` or `Ctrl+C` - Quit application
- Shortcuts displayed in footer with visual hints
- Use `bubbles/key` package for key binding management

**Number Animation:**

- Implement count-up animation using easing functions
- Update target count, animate from current to target over 500ms
- Use quadratic easing out for smooth, natural feel
- Store current and target in model, interpolate in View
- Apply to all numeric counters for polish

**Celebration Effects:**

- Trigger on milestone messages (100, 1000, 5000, 10000 items)
- Display special emoji in log: ✨🎊🎉🚀
- Brief border color change to celebrate (flash green)
- Optional: animated "confetti" using colored dots for 2 seconds
- Return to normal animation after celebration

**Rate Limit Visualization:**

- Convert rate limit percentage to progress bar
- Dynamic color based on remaining percentage:
  - Green (>70%): Normal operations
  - Yellow (30-70%): Approaching limit, show warning
  - Red (<30%): Critical, may pause soon
- Show reset time in human-readable format
- Update every API response to keep accurate

**ETA Calculation:**

- Track items processed per second over sliding 30-second window
- Calculate remaining items: total - current
- ETA = remaining / rate (in seconds)
- Format: `42s`, `1m 23s`, `2h 15m` depending on duration
- Show "calculating..." during first 10 seconds
- Update every 2 seconds for smooth display

**Window Resize:**

- Listen for `tea.WindowSizeMsg` in model's `Update()`
- Store width/height in model state
- Recalculate box width: `max(76, width - 4)`
- Adjust progress bar width proportionally if needed
- Layout adjusts automatically on next render
- All content reflows based on new dimensions

**Color Scheme:**

- Use `lipgloss.AdaptiveColor` for light/dark terminal compatibility
- Define palette at package level for consistency
- Purple/blue/cyan/green/yellow/orange gradient for borders
- Bright blue (#12) for active items and spinners
- Bright green (#10) for completed items and success
- Bright red (#9) for failed items and errors
- Dim gray (#240) for disabled/skipped items
- Applied via `lipgloss.NewStyle().Foreground(color)`
- Background always transparent

**Milestone Celebrations:**

- 100 items: ✨ emoji in log with encouraging message
- 1,000 items: 🎊 emoji with "Great progress!" message
- 5,000 items: 🎉 emoji with "Amazing work!" message
- 10,000 items: 🚀✨🎉 triple emoji combo with "Stellar!" message
- Triggered in `itemUpdateMsg` handler when crossing threshold
- Check previous count vs new count to trigger once

**Activity Log Formatting:**

- Maximum 5 visible entries
- Newest at bottom (natural reading order)
- Timestamp prefix: `HH:MM:SS` in dim color
- Emoji for categorization (visual scanning)
- Message text in default color
- Older entries fade slightly (reduce alpha)
- Auto-trim to fit width, add ellipsis if needed
- Store last 10 entries, display last 5

**Footer Tips System:**

- Array of helpful tip strings
- Rotate index every 30 seconds via `tipRotateMsg`
- Context-aware: show different tips based on state
  - During sync: show progress shortcuts
  - On errors: show error recovery tips
  - On completion: show next steps
- Format: `💡 Tip: [message]` or `💡 [keyboard shortcut help]`
- Always visible at bottom of box

**Interactive Keyboard Controls:**

The pull command supports real-time keyboard interaction during sync operations:

- **`h` or `?`** - Toggle help overlay showing all keyboard shortcuts
  - Displays modal box with keybinding reference
  - Press any key to dismiss and return to sync view
  - Help stays on top of main UI

- **`p`** - Pause/Resume sync operations
  - Pauses all active API requests gracefully
  - Shows "⏸️  Paused" indicator in stats panel
  - Press again to resume from where it stopped
  - Useful when rate limit is low or to conserve bandwidth

- **`s`** - Toggle expanded stats view
  - Switches between compact and detailed stats display
  - Detailed view shows:
    - Per-item success/warning/error counts
    - Request latency (min/avg/max)
    - Throughput graph (sparkline)
    - Detailed rate limit breakdown
  - Compact view (default): single-line stats

- **`d`** - Toggle detailed/compact activity log
  - Compact: 5 lines of recent activity
  - Detailed: 15 lines with expanded context
  - Scrolls automatically as new entries arrive

- **`c`** - Copy current statistics to clipboard
  - Copies formatted summary of sync progress
  - Includes counts, timing, rate limit status
  - Ready to paste into reports or issues
  - Requires `bubbles/clipboard` package

- **`r`** - Force refresh of current display
  - Manually triggers full UI re-render
  - Clears any rendering artifacts
  - Useful if terminal gets corrupted

- **`q` or `Ctrl+C`** - Graceful shutdown
  - Stops all operations cleanly
  - Saves current progress to database
  - Shows summary of what was completed
  - Exits after cleanup (takes 1-2 seconds)

**Keyboard Shortcuts Display:**

When `h` is pressed, show overlay:

```
╭────────────────────────────────────────────────────────────────╮
│  ⌨️  Keyboard Shortcuts                                        │
│                                                                │
│  h or ?    Show this help                                     │
│  p         Pause/Resume sync                                  │
│  s         Toggle stats view (compact ↔ detailed)            │
│  d         Toggle activity log (compact ↔ detailed)          │
│  c         Copy stats to clipboard                           │
│  r         Force refresh display                             │
│  q         Quit (Ctrl+C also works)                          │
│                                                                │
│  Press any key to dismiss                                      │
╰────────────────────────────────────────────────────────────────╯
```

This overlay appears centered on screen, on top of the main UI, with a semi-transparent background effect using lipgloss styling.

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
