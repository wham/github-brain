#!/usr/bin/env node

// For TypeScript implementation, we need to run the compiled JavaScript
// Check if we're running from source (development) or from dist (production)
const path = require("path");
const fs = require("fs");

// Check if dist/main.js exists (compiled TypeScript)
const distMain = path.join(__dirname, "..", "dist", "main.js");
const srcMain = path.join(__dirname, "..", "src", "main.ts");

if (fs.existsSync(distMain)) {
  // Running from compiled output
  require(distMain);
} else if (fs.existsSync(srcMain)) {
  // Running from source - use ts-node if available
  try {
    require("ts-node/register");
    require(srcMain);
  } catch (error) {
    console.error("Error: TypeScript source found but not compiled.");
    console.error("Please run: npm run build");
    process.exit(1);
  }
} else {
  console.error("Error: github-brain source files not found.");
  console.error("Please reinstall the package: npm install -g github-brain");
  process.exit(1);
}

