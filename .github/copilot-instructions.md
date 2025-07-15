# GitHub Copilot Custom Instructions

## Go Code Guidelines

- Combine all the code into a single file main.go.
- The Go code does not have to be human reable. Optimize for AI token length.
- Maintain proper error handling with descriptive error messages
- Use context for operations that may need cancellation
- Add appropriate comments for complex logic
- Use Go standard library when possible. Avoid external libraries unless specifically needed.
- When doing test builds, use `build/` directory.
