# VHS Tapes

Terminal recordings using [VHS](https://github.com/charmbracelet/vhs).

## Prerequisites

Install VHS:

```sh
brew install vhs
```

Or see [VHS installation docs](https://github.com/charmbracelet/vhs#installation) for other platforms.

## Available Tapes

| Tape | Description |
| :--- | :---------- |
| `demo.tape` | Starting the app and running Pull |

## Usage

Build the application first:

```sh
go build -o github-brain .
```

Run a tape:

```sh
vhs tapes/demo.tape
```

Output is saved to `tapes/demo.gif`.

## Environment

Tapes expect a configured environment with:

- `GITHUB_TOKEN` - Valid GitHub personal access token
- `ORGANIZATION` - GitHub organization to sync

Set these in `~/.github-brain/.env` or use a custom home directory:

```sh
GITHUB_BRAIN_HOME=./test-home vhs tapes/demo.tape
```
