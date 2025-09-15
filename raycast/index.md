# GitHub Brain Raycast Extension

AI coding agent specification. Human documentation in README.md.

## Extension Structure

Create a TypeScript-based Raycast extension using the official Raycast Extension Template.

### Extension Metadata

- **Name**: GitHub Brain
- **Description**: Search across GitHub discussions, issues, and pull requests using GitHub Brain
- **Author**: Your Organization
- **Category**: Developer Tools
- **Keywords**: github, search, issues, discussions, pull-requests

### Dependencies

```json
{
  "@raycast/api": "^1.60.0",
  "@raycast/utils": "^1.10.0",
  "node-fetch": "^2.6.7"
}
```

## MCP Integration

### Connection Setup

Connect to the GitHub Brain MCP server using stdio transport:

- **Server Path**: `./build/github-brain mcp` (relative to project root)
- **Transport**: stdio
- **Working Directory**: Raycast extension should detect and use the GitHub Brain project root

### Server Discovery

The extension should:

1. Check for `github-brain` binary in common locations:
   - `./build/github-brain` (relative to current working directory)
   - `../build/github-brain` (one level up)
   - System PATH lookup for `github-brain`

2. If binary not found, show error: "GitHub Brain binary not found. Please run 'go build -o ./build/github-brain' in the GitHub Brain project."

3. Validate MCP server connectivity on extension startup

## Search Command

### Command Configuration

- **Name**: `search`
- **Title**: "Search"
- **Description**: "Search across GitHub discussions, issues, and pull requests"
- **Mode**: `view` with search bar

### Search Interface

```typescript
interface SearchState {
  query: string;
  results: SearchResult[];
  isLoading: boolean;
  error?: string;
}

interface SearchResult {
  title: string;
  url: string;
  type: 'discussion' | 'issue' | 'pull_request';
  repository: string;
  author: string;
  created_at: string;
  state?: 'open' | 'closed';
  body: string; // Full body content (not displayed in list)
}
```

### Search Behavior

1. **Debounced Search**: Wait 300ms after user stops typing before executing search
2. **Minimum Query Length**: Require at least 2 characters before searching
3. **Loading State**: Show loading indicator while search is in progress
4. **Error Handling**: Display user-friendly error messages for MCP connection issues

### MCP Tool Usage

Use the `search` MCP tool with the following parameters:

```typescript
const searchParams = {
  query: userInput,
  fields: ["title", "url", "repository", "created_at", "author", "type", "state", "body"]
};
```

### Result Display

Display search results in a simple list view:

- **Primary Text**: Item title (full title, no truncation)
- **Subtitle**: Repository name and author
- **Accessories** (in order):
  - State badge ("Open" or "Closed" for issues/PRs) - shown first
  - Type icon (💬 for discussions, 🐛 for issues, 🔀 for PRs) - shown second

No detail view or preview panel - keep the interface clean and focused on the list.

### Result Actions

#### Primary Action: Open in Browser
- **Title**: "Open in Browser"
- **Key**: `Enter`
- **Action**: Open the GitHub URL in default web browser using `open(result.url)`

#### Secondary Actions

1. **Copy URL**
   - **Title**: "Copy URL to Clipboard"
   - **Key**: `⌘C`
   - **Action**: Copy GitHub URL to system clipboard

2. **Copy Title**
   - **Title**: "Copy Title to Clipboard"
   - **Key**: `⌘⇧C`
   - **Action**: Copy item title to system clipboard

## Error Handling

### MCP Server Errors

Handle the following error scenarios:

1. **Server Not Running**: "GitHub Brain server is not running. Please check your installation."
2. **Database Not Found**: "GitHub Brain database not found. Please run 'github-brain pull' first."
3. **Pull In Progress**: Display the exact error message from MCP server about pull in progress
4. **Network Errors**: "Unable to connect to GitHub Brain server. Please try again."

### Search Errors

1. **Empty Results**: "No results found for your search query. Try different keywords."
2. **Invalid Query**: "Search query must be at least 2 characters long."
3. **Rate Limiting**: "Search rate limit exceeded. Please wait a moment before searching again."

## Performance Optimizations

### Caching Strategy

- **Result Caching**: Cache search results for 30 seconds to avoid redundant MCP calls
- **Connection Pooling**: Reuse MCP connection across multiple searches
- **Debounced Requests**: Prevent excessive API calls during typing

### Result Limits

- Display maximum 20 results per search
- If more results available, show message: "Showing top 20 results. Refine your search for more specific results."

## User Experience

### Loading States

- **Initial Load**: Show skeleton placeholders while connecting to MCP server
- **Search Loading**: Show spinner with "Searching..." text
- **No Results**: Show helpful message with search tips

### Search Tips

When no results found, show suggestions:
- "Try using different keywords"
- "Search for specific repository names"
- "Use author names to find their contributions" 
- "Try broader terms for more results"

### Keyboard Navigation

- `↑/↓`: Navigate between results
- `Enter`: Open selected result in browser
- `⌘C`: Copy URL of selected result
- `⌘⇧C`: Copy title of selected result
- `Escape`: Clear search and return to input

## Configuration

### Preferences

Allow users to configure:

1. **GitHub Brain Path**: Custom path to `github-brain` binary
2. **Results Limit**: Number of results to display (5-50, default: 20)
3. **Auto-open**: Automatically open first result when only one match found

### Settings Validation

Validate user preferences:
- Ensure GitHub Brain binary exists at specified path
- Verify binary is executable and responds to `--version` flag
- Test MCP connection during preference changes

## Development Guidelines

### Code Structure

```
src/
├── index.ts          # Main search command
├── mcp-client.ts     # MCP connection and communication
├── types.ts          # TypeScript interfaces
├── utils.ts          # Helper functions
├── components/       # Reusable UI components
│   ├── SearchResult.tsx
│   ├── ErrorView.tsx
│   └── LoadingView.tsx
└── assets/          # Icons and images
    ├── github-icon.png
    └── type-icons/
        ├── discussion.png
        ├── issue.png
        └── pull-request.png
```

### Error Logging

Use Raycast's built-in logging for debugging:
- Log MCP connection attempts
- Log search queries and response times
- Log error conditions with context
- Avoid logging sensitive user data

### Testing Strategy

1. **Unit Tests**: Test MCP client communication and result parsing
2. **Integration Tests**: Test full search flow with mock MCP responses  
3. **Manual Testing**: Test with real GitHub Brain database and various search queries
4. **Error Scenarios**: Test all error conditions and user-facing messages

## Implementation Status

### ✅ Completed Components

1. **MCP Client** (`src/mcp-client.ts`)
   - Stdio-based connection to GitHub Brain server
   - Automatic binary discovery in multiple locations
   - Request/response handling with JSON-RPC protocol
   - Error handling and timeout management
   - Organization configuration via environment variable

2. **Search Command** (`src/search.tsx`)
   - Main Raycast command with search interface
   - Debounced search (300ms delay)
   - Result caching (30-second duration)
   - Auto-open single result feature
   - Loading and error states

3. **UI Components**
   - `SearchResult.tsx`: Individual result display with actions
   - `ErrorView.tsx`: Error state handling with user-friendly messages
   - `LoadingView.tsx`: Loading indicator during searches

4. **Utilities** (`src/utils.ts`)
   - Type icon mapping for different GitHub item types
   - Relative time formatting
   - Text truncation helpers
   - Debounce implementation
   - Result caching system

5. **Type Definitions** (`src/types.ts`)
   - TypeScript interfaces for all data structures
   - MCP protocol types
   - Search result types

6. **Configuration**
   - User preferences for customization
   - GitHub Brain path configuration
   - Results limit control
   - Auto-open settings

## Known Issues

### TypeScript Compilation
There are React 18+ type compatibility issues with the Raycast API that prevent strict TypeScript compilation. The extension will work at runtime but shows type errors during build. This is a known issue with the current versions of @raycast/api and React types.

Workaround options:
1. Use `skipLibCheck: true` in tsconfig.json
2. Downgrade React types to an earlier version
3. Wait for Raycast API updates to support React 19

### Environment Configuration
The extension requires the `GITHUB_BRAIN_ORG` environment variable to be set for the MCP server to function. This should be documented clearly for users.

## Testing

A test script (`test-mcp.js`) has been included to verify MCP connectivity:

```bash
cd raycast
node test-mcp.js
```

## Deployment Instructions

1. **Build GitHub Brain**:
   ```bash
   go build -o ./build/github-brain
   ```

2. **Sync GitHub Data**:
   ```bash
   ./build/github-brain pull -o your-org -t your-github-token
   ```

3. **Set Environment Variable**:
   ```bash
   export GITHUB_BRAIN_ORG=your-org
   ```

4. **Install Extension**:
   ```bash
   cd raycast
   npm install
   npm run build
   ```

5. **Import in Raycast**:
   - Open Raycast
   - Search for "Import Extension"
   - Navigate to the raycast folder
   - Select and import

## File Structure

```
raycast/
├── src/
│   ├── index.tsx           # Entry point
│   ├── search.tsx          # Main search command
│   ├── mcp-client.ts       # MCP connection handler
│   ├── types.ts            # TypeScript definitions
│   ├── utils.ts            # Helper functions
│   └── components/
│       ├── SearchResult.tsx
│       ├── ErrorView.tsx
│       └── LoadingView.tsx
├── package.json            # Extension manifest
├── tsconfig.json          # TypeScript config
├── icon.png               # Extension icon
├── README.md              # User documentation
├── index.md               # Specification & implementation notes
└── test-mcp.js           # MCP connection test

```

## Future Improvements

1. **Type Safety**: Resolve React type compatibility issues
2. **Advanced Search**: Add filters for date ranges, authors, repositories
3. **Batch Operations**: Support for bulk actions on search results
4. **Performance**: Implement virtual scrolling for large result sets
5. **Offline Mode**: Cache recent searches for offline access
6. **Shortcuts**: Add custom keyboard shortcuts for frequent searches
7. **Export**: Allow exporting search results to CSV/JSON

This specification provides a complete blueprint for building a Raycast extension that integrates seamlessly with the GitHub Brain MCP server, following the same detailed specification approach used in `main.md`.