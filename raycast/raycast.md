# github-brain-raycast

AI coding agent specification for Raycast extension. Human documentation in README.md.

## MCP Protocol

Connect to github-brain server via stdio transport. Spawn process: `github-brain mcp -o <org> -d <dbDir>`.

### Initialization

Request:
```json
{
  "jsonrpc": "2.0",
  "method": "initialize",
  "params": {
    "protocolVersion": "2024-11-05",
    "capabilities": {},
    "clientInfo": {
      "name": "github-brain-raycast",
      "version": "1.0.0"
    }
  },
  "id": 1
}
```

### Search Tool

Request:
```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "search",
    "arguments": {
      "query": "user input",
      "fields": ["title", "url", "repository", "created_at", "type", "state", "body"]
    }
  },
  "id": 2
}
```

Response contains markdown-formatted text in `result.content[0].text`. Parse sections separated by `---` to extract:
- Title after `## `
- URL after `- URL: `
- Type after `- Type: `
- Repository after `- Repository: `
- Created at after `- Created at: `
- State after `- State: `
- Body content after metadata lines

Special responses:
- "No results found" - Empty result set
- "A pull is currently running" - Pull operation blocking search

## Search Behavior

- Minimum 2 characters to trigger search
- Debounce user input by 300ms
- Cache results for 30 seconds per query
- Limit results based on user preference (5-50)
- Auto-open single result if preference enabled

## Display Mapping

Type/state to Raycast icon and color:

| Type | State | Icon | Color |
|------|-------|------|-------|
| issue | open | Circle | #1a7f37 |
| issue | closed | XMarkCircle | #8250df |
| pull_request | open | CircleEllipsis | #1a7f37 |
| pull_request | closed | XMarkCircle | #cf222e |
| pull_request | merged | CheckCircle | #8250df |
| discussion | open | SpeechBubble | #1a7f37 |
| discussion | closed | SpeechBubbleImportant | #8250df |

## Error Handling

Map MCP errors to user messages:
- Binary not found → "GitHub Brain binary not found. Please run 'go build -o ./build/github-brain'"
- Database missing → "GitHub Brain database not found. Please run 'github-brain pull' first"
- Pull in progress → Display exact error from server
- Connection timeout → "Unable to connect to GitHub Brain server"

## Environment

Organization resolution order:
1. `GITHUB_BRAIN_ORG` environment variable
2. `ORGANIZATION` environment variable
3. User preference `organization`
4. Default: `github`

Binary discovery order:
1. User preference `githubBrainPath` + `/build/github-brain`
2. User preference `githubBrainPath` + `/scripts/run`
3. Common locations: `~/code/github-brain`, `~/github-brain`, etc.