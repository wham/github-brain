# github-brain-windows

This package contains the native Windows x64 binary for [github-brain](https://www.npmjs.com/package/github-brain).

> **Note:** This package uses the simplified name `github-brain-windows` instead of `github-brain-win32-x64` to avoid NPM's spam detection, which flags packages with platform-specific suffixes.

**You should not install this package directly.** It is automatically installed as an optional dependency when you install the main `github-brain` package:

```bash
npm install -g github-brain
```

## About github-brain

GitHub Brain is an MCP server for searching and summarizing GitHub discussions, issues, and pull requests.

- **Repository:** https://github.com/wham/github-brain
- **Main package:** https://www.npmjs.com/package/github-brain
- **License:** MIT

This package is part of a platform-specific binary distribution system that includes:

- `github-brain-darwin-arm64` (macOS Apple Silicon)
- `github-brain-darwin-x64` (macOS Intel)
- `github-brain-linux-arm64` (Linux ARM64)
- `github-brain-linux-x64` (Linux x64)
- `github-brain-windows` (Windows x64)
