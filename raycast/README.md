# GitHub Brain Raycast Extension

A Raycast extension that provides instant search across GitHub discussions, issues, and pull requests using the GitHub Brain MCP server.

## Features

- 🔍 **Fast Full-Text Search**: Search across all your GitHub data instantly
- 💬 **Discussion Search**: Find relevant discussions across repositories
- 🐛 **Issue Tracking**: Quickly locate issues by keywords
- 🔀 **Pull Request Discovery**: Search through pull request titles and content
- ⚡ **Real-time Results**: See results as you type with intelligent debouncing
- 📋 **Quick Actions**: Open in browser, copy URL, or copy title with keyboard shortcuts
- 🎯 **Smart Caching**: 30-second result caching for repeated searches

## Prerequisites

1. **GitHub Brain**: You must have the GitHub Brain project cloned and data synchronized:
   ```bash
   # Clone GitHub Brain (if not already done)
   git clone https://github.com/your-org/github-brain.git
   cd github-brain
   
   # Build the binary (recommended for Raycast)
   go build -o ./build/github-brain
   
   # Sync your GitHub data
   ./build/github-brain pull -o your-org -t your-token
   ```

2. **Raycast**: Install [Raycast](https://raycast.com) on your Mac

## Installation

1. Clone this repository or download the extension
2. Set your GitHub organization as an environment variable:
   ```bash
   export GITHUB_BRAIN_ORG=your-org-name
   # Or add to your shell profile (~/.zshrc or ~/.bashrc)
   echo 'export GITHUB_BRAIN_ORG=your-org-name' >> ~/.zshrc
   ```
3. Navigate to the raycast folder and build:
   ```bash
   cd raycast
   npm install
   npm run build
   ```
   
   The build should complete with:
   ```
   ready - built extension successfully
   ```

4. Import the extension in Raycast:
   - Open Raycast
   - Search for "Import Extension"
   - Select the raycast folder

## Configuration

The extension can be configured through Raycast preferences:

- **GitHub Brain Project Path**: Path to the github-brain project directory (default: `~/code/github-brain`)
- **GitHub Organization**: The organization to search (must match the one used in `github-brain pull`)
- **Results Limit**: Number of results to display (5-50, default: 20)
- **Auto-open Single Result**: Automatically open when only one match is found
- **Preview Length**: Characters to show in body preview (100-500, default: 200)

## Usage

1. Open Raycast (default: `⌘ Space`)
2. Type "Search GitHub Brain" or your configured alias
3. Enter your search query (minimum 2 characters)
4. Browse results with arrow keys
5. Press `Enter` to open in browser

### Keyboard Shortcuts

- `Enter`: Open selected result in browser
- `⌘C`: Copy URL of selected result
- `⌘⇧C`: Copy title of selected result
- `⌘D`: Show full content details
- `Escape`: Clear search and return to input

## Search Tips

- Use specific keywords for better results
- Search by repository name to filter results
- Include author names to find their contributions
- Try broader terms if you get no results

## Troubleshooting

### "GitHub Brain project not found"
- Ensure the GitHub Brain project is cloned to a standard location (e.g., `~/code/github-brain`)
- Update the "GitHub Brain Project Path" in Raycast preferences to point to your project directory
- The extension prioritizes using `build/github-brain` for reliability, with fallback to `scripts/run` if needed
- Verify the files are executable: `chmod +x scripts/run` or `chmod +x build/github-brain`

### "Exit code 127" or "dirname: command not found" error  
- This happens when using scripts/run in Raycast's restricted environment
- **Solution**: Build the binary first with `go build -o ./build/github-brain` in the project directory
- The extension prioritizes using the pre-built binary over scripts/run for better reliability

### "Database not found"
- Run `github-brain pull` to sync your GitHub data first
- Ensure the database exists in the configured location

### "Pull is currently running"
- Wait for the current data sync to complete
- Check the GitHub Brain console for progress

### No search results
- Ensure your database contains data
- Try different or broader search terms
- Check that the MCP server is running correctly

## Development

To modify the extension:

1. Make changes to the TypeScript source files
2. Run `npm run dev` for development mode with hot reload
3. Run `npm run build` to compile for production
4. Run `npm run lint` to check code style
5. Run `npm run fix-lint` to auto-fix style issues

## Architecture

The extension uses the Model Context Protocol (MCP) to communicate with GitHub Brain:

- **MCP Client**: Manages stdio connection to GitHub Brain server
- **Search Command**: Main Raycast command with debounced search
- **Result Caching**: 30-second cache to reduce server load
- **Error Handling**: Graceful handling of connection and search errors

## License

MIT