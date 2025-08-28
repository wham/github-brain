<div align="center">
  <img src="logo.svg" alt="GitHub Brain Logo" width="150" height="150">
  <h1>GitHub Brain MCP Server</h1>
</div>

**GitHub Brain** is an experimental MCP server for summarizing GitHub discussions, issues, and pull requests. It helps answer questions like:

- _What are the contributions of user X in the last month?_
- _List all pull requests merged by the members of team Y in the last week._
- _Summarize this month's discussions._

https://github.com/user-attachments/assets/80910025-9d58-4367-af00-bf4c51e6ce86

GitHub Brain complements (but does not replace) the [official GitHub MCP server](https://github.com/github/github-mcp-server). It uses a local database to store data pulled from GitHub, enabling:

- Fast responses
- More results than the standard 100-item API limit
- Markdown output for token efficiency

![](./docs/pull.png)

As a bonus, GitHub Brain also includes a simple web-based UI for ultra-fast search of the discussions, issues, and pull requests.

## Prerequisites

- [Go](https://go.dev/doc/install) installed

## Usage

```sh
scripts/run <command> [<args>]
```

**Workflow:**

1. Populate the local database with the `pull` command.
2. Start the MCP server with the `mcp` command.

Re-run `pull` anytime to update the database with new GitHub data. You can do this while `mcp` is running, but MCP requests may temporarily return errors during the update.

Each command has its own arguments. Some can be set via environment variables. The app will also load variables from a `.env` file in the current directory.

<details>
    <summary>Example .env file</summary>

    GITHUB_TOKEN=your_github_token
    ORGANIZATION=my-org

</details>

### `pull`

Populate the local database with GitHub data.

Example:

```sh
scripts/run pull -o my-org
```

The first call for an organization may take a while. Subsequent calls are faster, updating only with new data.

| Argument | Variable                | Description                                                                                                                                |
| :------- | :---------------------- | :----------------------------------------------------------------------------------------------------------------------------------------- |
| `-t`     | `GITHUB_TOKEN`          | Your GitHub [personal token](https://github.com/settings/personal-access-tokens) to access the API. **Required.**                          |
| `-o`     | `ORGANIZATION`          | The GitHub organization to pull data from. **Required.**                                                                                   |
| `-db`    | `DB_DIR`                | Path to the SQLite database directory. Default: `db` folder in the current directory. An `<organization>.db` file will be created here.    |
| `-i`     |                         | Only pull selected entities. Choose from: `repositories`, `discussions`, `issues`, `pull-requests`, `teams`. Comma-separated list.         |
| `-f`     |                         | Remove all data before pulling. If combined with `-i`, only the specified items will be removed.                                           |
| `-e`     | `EXCLUDED_REPOSITORIES` | Comma-separated list of repositories to exclude from the pull of discussions, issues, and pull-requests. Useful for large repositories that are not relevant to the analysis. |   

<details>
    <summary>Personal access token scopes</summary>

    Use the [fine-grained personal access tokens](https://github.com/settings/personal-access-tokens).

    For private organizations, the token must have the following configuration:

    - Organization permissions: Read access to members
    - Repository permissions: Read access to discussions, issues, metadata, and pull requests

    For public organizations, an empty token is sufficient, as the data is publicly accessible.

</details>

### `mcp`

Start the MCP server using the local database.

Example:

```sh
scripts/run mcp -o my-org
```

| Argument | Variable       | Description                                                                                                                                  |
| :------- | :------------- | :------------------------------------------------------------------------------------------------------------------------------------------- |
| `-o`     | `ORGANIZATION` | The GitHub organization to work with. **Required.**                                                                                          |
| `-db`    | `DB_DIR`       | Path to the SQLite database directory. Default: `db` folder in the current directory. Loads data from `<organization>.db` in this directory. |

### `ui`

Start the UI server, which provides a web-based interface for interacting with the GitHub data. Alternative to MCP for quick lookups.

Example:

```sh
scripts/run ui -o my-org
```

| Argument | Variable       | Description                                                                                                                                  |
| :------- | :------------- | :------------------------------------------------------------------------------------------------------------------------------------------- |
| `-o`     | `ORGANIZATION` | The GitHub organization to work with. **Required.**                                                                                          |
| `-db`    | `DB_DIR`       | Path to the SQLite database directory. Default: `db` folder in the current directory. Loads data from `<organization>.db` in this directory. |
| `-p`     | `UI_PORT`      | Port for the UI server. Default: `8080`.                                                                                                     |
| `-s`     |                | Skip creating the FTS table.                                                                                                                 |

## Installation

`scripts/run` is a convenience script that runs the MCP server. It builds the Go code and runs the `mcp` command with the checkout directory as the working directory. As a result, the SQLite database will be created in the `db` folder of the checkout directory.

### Claude

Add to the Claude MCP configuration file:

```json
{
  "mcpServers": {
    "github-brain": {
      "type": "stdio",
      "command": "<path-to-the-checkout-directory>/scripts/run",
      "args": ["mcp"]
    }
}
```

Where `<path-to-the-checkout-directory>` is the path to the GitHub Brain repository on your local machine. Merge if `mcpServers` already exists.

### VS Code

Add to the VS Code MCP configuration file:

```json
{
  "servers": {
    "github-brain": {
      "type": "stdio",
      "command": "<path-to-the-checkout-directory>/scripts/run",
      "args": ["mcp"],
      "version": "0.0.1"
    }
  }
}
```

Where `<path-to-the-checkout-directory>` is the path to the GitHub Brain repository on your local machine. Merge if `servers` already exists.
