#!/usr/bin/env node

const { spawnSync } = require("child_process");
const path = require("path");
const fs = require("fs");

// Platform-specific package mapping
// Note: Windows package uses simplified name "github-brain-windows" instead of
// "github-brain-win32-x64" to avoid NPM's spam detection which flags packages
// with platform-specific suffixes like win32-x64.
const PLATFORMS = {
  "darwin-arm64": "github-brain-darwin-arm64",
  "darwin-x64": "github-brain-darwin-x64",
  "linux-arm64": "github-brain-linux-arm64",
  "linux-x64": "github-brain-linux-x64",
  "win32-x64": "github-brain-windows",
};

function getPlatformPackage() {
  const platform = process.platform;
  const arch = process.arch;

  // Map Node.js arch to our naming convention
  const archMap = {
    arm64: "arm64",
    x64: "x64",
  };

  const mappedArch = archMap[arch];
  if (!mappedArch) {
    throw new Error(`Unsupported architecture: ${arch}`);
  }

  const key = `${platform}-${mappedArch}`;
  const pkg = PLATFORMS[key];

  if (!pkg) {
    throw new Error(`Unsupported platform: ${platform} ${arch}`);
  }

  return pkg;
}

function getBinaryPath() {
  try {
    const pkg = getPlatformPackage();
    const binaryName =
      process.platform === "win32" ? "github-brain.exe" : "github-brain";

    // Try to resolve the binary from the platform-specific package
    const pkgPath = require.resolve(`${pkg}/package.json`);
    const pkgDir = path.dirname(pkgPath);
    const binaryPath = path.join(pkgDir, binaryName);

    if (fs.existsSync(binaryPath)) {
      return binaryPath;
    }

    throw new Error(`Binary not found at ${binaryPath}`);
  } catch (error) {
    console.error("Error:", error.message);
    console.error(
      "\nThe platform-specific binary package may not be installed."
    );
    console.error("Try running: npm install");
    process.exit(1);
  }
}

// Get the binary path and execute it
const binaryPath = getBinaryPath();
const result = spawnSync(binaryPath, process.argv.slice(2), {
  stdio: "inherit",
  windowsHide: false,
});

process.exit(result.status || 0);
