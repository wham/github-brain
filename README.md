<div align="center">
  <img src="logo.svg" alt="GitHub Brain Logo" width="150" height="150">
  <h1>GitHub Brain MCP Server</h1>
</div>

**GitHub Brain** is an experimental MCP server for summarizing GitHub discussions, issues, and pull requests. Answer questions like:

- _What are the contributions of user X in the last month?_
- _Summarize this month's discussions._

https://github.com/user-attachments/assets/80910025-9d58-4367-af00-bf4c51e6ce86

GitHub Brain complements (but does not replace) the [official GitHub MCP server](https://github.com/github/github-mcp-server). It stores GitHub data in a local database for:

- Fast responses
- More than the standard 100-item API limit
- Token-efficient Markdown output

![](./docs/pull.png)

GitHub Brain is [programmed in Markdown](https://github.blog/ai-and-ml/generative-ai/spec-driven-development-using-markdown-as-a-programming-language-when-building-with-ai/).

## Installation

```sh
npm i -g github-brain
```

Rerun to update. `sudo` may be required on some systems.

Alternatively use `npx` to run without installing globally and needing `sudo`.

```sh
npx github-brain@latest
```

## Usage

```sh
github-brain
```

Launches the interactive TUI where you can:

1. **Setup** - Configure authentication and settings
   - Login with GitHub (OAuth) - Recommended for most users
   - Login with Personal Access Token - For fine-grained tokens or when OAuth is unavailable
   - Open configuration file - Edit `.env` directly
2. **Pull** - Populate the local database with GitHub data

Re-run pull anytime to update the database with new GitHub data.

The app loads environment variables from a `.env` file in the GitHub Brain's home directory - `~/.github-brain` by default.

<details>
    <summary>Example .env file</summary>

    GITHUB_TOKEN=your_github_token
    ORGANIZATION=my-org

</details>

| Argument | Description                                |
| :------- | :----------------------------------------- |
| `-m`     | Home directory. Default: `~/.github-brain` |

<details>
    <summary>Personal access token scopes</summary>

    Use [fine-grained personal access tokens](https://github.com/settings/personal-access-tokens).

    **Private organizations:** Token needs read access to discussions, issues, metadata, and pull requests. [Generate token](https://github.com/settings/personal-access-tokens/new?name=github-brain&description=http%3A%2F%2Fgithub.com%2Fwham%2Fgithub-brain&issues=read&pull_requests=read&discussions=read).

    **Public organizations:** Any token works (data is publicly accessible).

</details>

## MCP Server

Start the MCP server using the local database:

```sh
github-brain mcp
```

| Argument | Variable       | Description                                |
| :------- | :------------- | :----------------------------------------- |
| `-o`     | `ORGANIZATION` | GitHub organization. **Required.**         |
| `-m`     |                | Home directory. Default: `~/.github-brain` |

## MCP Configuration

### Claude

Add to the Claude MCP configuration file:

```json
{
  "mcpServers": {
    "github-brain": {
      "type": "stdio",
      "command": "github-brain",
      "args": ["mcp"]
    }
}
```

Merge with existing `mcpServers` if present.

### VS Code

Add to the VS Code MCP configuration file:

```json
{
  "servers": {
    "github-brain": {
      "type": "stdio",
      "command": "github-brain",
      "args": ["mcp"],
      "version": "0.0.1"
    }
  }
}
```

Merge with existing `servers` if present.

## Development

`scripts/run` builds and runs `github-brain` with the checkout directory as home `-m` (database in `db/`, config in `.env`).
