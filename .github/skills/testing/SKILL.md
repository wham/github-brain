---
name: testing
description: Guide for testing TUI (terminal user interface) applications. Use this when asked to verify code changes.
---

# Testing

This skill helps you create and run tests for terminal user interface (TUI) applications.

## When to use this skill

Use this skill when you need to:
- Verify that code changes work as intended
- Ensure existing functionality as specified in `maind.md` and `README.md` is not broken

## Starting the application

- Run `scripts/run` where the user would normally run `github-brain`
  - `scripts/run pull` equivalently runs `github-brain pull`
  - `scripts/run mcp` equivalently runs `github-brain mcp` 
- Ensure `.env` files is configured to use the `github-brain-test` organization
- Use GitHub MCP to add new issue/PRs/discussions as needed for testing
- Simulate user input: Send key presses, control combinations, or specific commands to the running application.
-  Capture and analyze screen output: The testing tool captures the terminal display (or buffer) at specific moments in time.
- Make assertions: Verify that the screen content matches the expected output (e.g., checking if specific text is present at certain coordinates or if the cursor position is correct).
