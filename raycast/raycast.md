# github-brain-raycast

AI coding agent specification. Human documentation in [README.md](../README.md#raycast-extension).

## Overview

Read the official docs:

- https://developers.raycast.com/basics/getting-started
- https://developers.raycast.com/basics/create-your-first-extension

Put everything into one file `src/search.tsx`. Use TypeScript. Create no other
TypeScript files.

Minimize the overall number of files and dependencies to the absolute minimum.

## Manifest

See https://developers.raycast.com/information/manifest

- `name`: github-brain
- `title`: GitHub Brain
- `description`: Search GitHub issues, pull requests, and discussions
- `icon`: 🧠

## Preferences

See https://developers.raycast.com/information/manifest#preference-properties

- `name`: `mcpCommand`
- `title`: GitHub Brain MCP command
- `description`: Absolute path to GitHub Brain executable
- `type`: textfield
- `required`: true
- `default`: github-brain

## Commands

### Search

- `name`: search
- `title`: Search
- `description`: Search GitHub issues, pull requests, and discussions
- `mode`: view

The extension starts with a search bar. As you type, it sends the query to the `search` tool and displays the results.
Show max 10 results. Result looks like this:

```
<icon><title> <repository>
```

`title` is bold, `repository` is subtle. `icon` depends on the type and state of the item (see Display Mapping below).

When user selects a result, open the URL in the browser.

#### Display Mapping

Type/state to Raycast icon and color:

| Type         | State  | Icon                  | Color   |
| ------------ | ------ | --------------------- | ------- |
| issue        | open   | Circle                | #1a7f37 |
| issue        | closed | XMarkCircle           | #8250df |
| pull_request | open   | CircleEllipsis        | #1a7f37 |
| pull_request | closed | XMarkCircle           | #cf222e |
| pull_request | merged | CheckCircle           | #8250df |
| discussion   | open   | SpeechBubble          | #1a7f37 |
| discussion   | closed | SpeechBubbleImportant | #8250df |

## Launcher

- The extension is launched with `scripts/raycast`
- The script first builds GitHub Brain with `scripts/run`
- This creates `build/github-brain` binary
- The launcher determines the full path to `build/github-brain` and `db` directory
- The launcher puts together the full command to launch the MCP: Full path to `build/github-brain` and argument `-db <db-dir>, and any arguments passed to the launcher.
- The launcher then updates the `package.json` preference `mcpCommand` and sets the `default` value to the computed string
- The launcher then starts the Raycast extension with `npm run dev` in the `ray

## Protocol

Connect to github-brain server via MCP stdio transport. Spawn process: `scripts/run mcp <arg>`. Use the `search` tool.
The tool is specified in [maind.md](..main.md#tools). You can find the input and output there.
