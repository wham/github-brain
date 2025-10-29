#!/usr/bin/env node

// This install script runs after npm install and verifies the binary is accessible
const { execSync } = require("child_process");
const path = require("path");

try {
  // Try to run the binary with --version to verify it works
  const binPath = path.join(__dirname, "bin", "github-brain.js");
  execSync(`node "${binPath}" --version`, { stdio: "inherit" });
  console.log("✓ github-brain installed successfully!");
} catch (error) {
  // If the binary isn't available yet (e.g., during CI or --no-optional),
  // that's okay - it will be resolved when the user tries to run it
  console.log("✓ github-brain package installed");
}
