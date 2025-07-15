# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## Development Approach

**This project uses Markdown as the programming language**. The actual source code is written in `main.md` as an AI coding agent specification, which gets compiled to Go code in `main.go`.

### Spec-First Development Workflow

1. **Edit Specification**: Make all changes to `main.md` (the authoritative source)
2. **Compile Implementation**: Generate updated `main.go` from the markdown specification
3. **Never Edit main.go Directly**: The compiled Go code should not be touched manually

### File Hierarchy

- **`main.md`**: AI specification (source of truth)
- **`main.go`**: Compiled Go implementation (generated, do not edit)
- **`README.md`**: End-user documentation and usage instructions
- **`CLAUDE.md`**: Development guidance for Claude Code

### Making Changes

When modifying the application:

1. Update the specification in `main.md`
2. Recompile to generate the new `main.go`
3. Test the implementation
4. Update documentation in `README.md` if user-facing features change

### Important Reminders

- **Specification Authority**: `main.md` is the single source of truth
- **No Direct Code Edits**: Never modify `main.go` directly
- **Consistency**: Keep `README.md` focused on user instructions, `main.md` on implementation specification
- **Testing**: Always test compiled changes before considering them complete

## Quick Development Commands

- **Build**: `go build -o ./build/github-brain`
- **Quick Run**: `./scripts/run <command> [args]` (includes debug flags)
- **Test Build**: Verify compilation after spec changes
