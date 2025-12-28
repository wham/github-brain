---
name: testing
description: Guide for testing and verifying code changes in this TUI application. Use this skill after making ANY code changes to main.go or main.md to verify they work correctly.
---

# Testing

This skill helps you create and run tests for terminal user interface (TUI) applications.

## When to use this skill

Use this skill:

- **After making ANY code changes** to verify they work correctly
- When modifying UI elements, menus, or display logic
- When changing application behavior or adding features
- To ensure existing functionality as specified in `main.md` and `README.md` is not broken

## Starting the application

- Run `scripts/run` where the user would normally run `github-brain`
  - `scripts/run pull` equivalently runs `github-brain pull`
  - `scripts/run mcp` equivalently runs `github-brain mcp`
- Ensure `.env` files is configured to use the `github-brain-test` organization
- Use GitHub MCP to add new issue/PRs/discussions as needed for testing
- Simulate user input: Send key presses, control combinations, or specific commands to the running application.
- Capture and analyze screen output: The testing tool captures the terminal display (or buffer) at specific moments in time.
- Make assertions: Verify that the screen content matches the expected output (e.g., checking if specific text is present at certain coordinates or if the cursor position is correct).
