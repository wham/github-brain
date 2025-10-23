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

- `name`: `organization`
- `title`: GitHub organization
- `description`: The GitHub organization to work with
- `type`: textfield
- `required`: true

- `name`: `githubBrainCommand`
- `title`: GitHub Brain executable
- `description`: Absolute path to the GitHub Brain executable command or binary
- `type`: textfield
- `required`: true
- `default`: github-brain

- `name`: `homeDir`
- `title`: Home directory
- `description`: Absolute path to the GitHub Brain home directory
- `type`: textfield
- `required`: true
- `default`: ~/.github-brain

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

Type/state to Raycast [icon](https://developers.raycast.com/api-reference/user-interface/icons-and-images) and [color](https://developers.raycast.com/api-reference/user-interface/colors):

| Type         | State  | Icon         | Color  |
| ------------ | ------ | ------------ | ------ |
| issue        | open   | Circle       | Green  |
| issue        | closed | Circle       | Purple |
| pull_request | open   | Code         | Green  |
| pull_request | closed | Code         | Red    |
| pull_request | merged | CheckCircle  | Purple |
| discussion   | open   | SpeechBubble | Green  |

## Launcher

- The extension is launched with `scripts/raycast`
- The script first builds GitHub Brain with `scripts/run`
- This creates `build/github-brain` binary
- The launched determines the absolute path to the `build/github-brain` binary and
  sets is as the `default` value of the `githubBrainCommand` preference in `package.json`
- The launcher determines the absolute path to the home directory and sets it as the `default` value of the `homeDir` preference in `package.json`
- The launcher then starts the Raycast extension with `npm run dev` in the `ray

## Protocol

Connect to github-brain server via MCP stdio transport. Spawn process: `<githubBrainCommand> mcp`. Use the `search` tool.
The tool is specified in [maind.md](..main.md#tools). You can find the input and output there.
