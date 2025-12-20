#!/usr/bin/env node
/**
 * GitHub Brain - MCP server for searching GitHub discussions, issues, and pull requests
 * 
 * This is the main TypeScript implementation rewritten from Go.
 * Keep all code in this single file as per specification.
 */

import { Command } from 'commander';
import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';

// Version information (will be set at build time)
const VERSION = process.env.npm_package_version || 'dev';
const BUILD_DATE = process.env.BUILD_DATE || 'unknown';

// Database schema version GUID - change this on any schema modification
const SCHEMA_GUID = 'b8f3c2a1-9e7d-4f6b-8c5a-3d2e1f0a9b8c';

// OAuth App Client ID (public, safe to embed)
// Scopes: read:org repo
const GITHUB_CLIENT_ID = 'Ov23ctgXe80Z1KsXE3vJ';

/**
 * Configuration interface for the application
 */
interface Config {
  organization?: string;
  githubToken?: string;
  homeDir: string;
  dbDir: string;
  items: string[];
  force: boolean;
  excludedRepositories: string[];
}

/**
 * Load configuration from command line arguments and environment variables
 */
function loadConfig(options: any): Config {
  // Get default home directory
  const defaultHomeDir = path.join(os.homedir(), '.github-brain');
  
  // Load .env file from home directory if it exists
  const envPath = path.join(options.homeDir || defaultHomeDir, '.env');
  if (fs.existsSync(envPath)) {
    require('dotenv').config({ path: envPath });
  }
  
  const config: Config = {
    homeDir: options.homeDir || defaultHomeDir,
    dbDir: '',
    organization: options.organization || process.env.ORGANIZATION,
    githubToken: process.env.GITHUB_TOKEN,
    items: options.items ? options.items.split(',').map((s: string) => s.trim()) : [],
    force: options.force || false,
    excludedRepositories: options.excludedRepositories 
      ? options.excludedRepositories.split(',').map((s: string) => s.trim())
      : (process.env.EXCLUDED_REPOSITORIES || '').split(',').map((s: string) => s.trim()).filter(Boolean),
  };
  
  // Construct DBDir from HomeDir
  config.dbDir = path.join(config.homeDir, 'db');
  
  return config;
}

/**
 * Login command - Interactive GitHub authentication using OAuth Device Flow
 */
async function loginCommand(options: any) {
  console.log('Login command - to be implemented');
  // TODO: Implement OAuth device flow
  // 1. Request device code from GitHub
  // 2. Display code and open browser
  // 3. Poll for access token
  // 4. Verify token and get username
  // 5. Prompt for organization
  // 6. Save to .env file
}

/**
 * Pull command - Populate local database with GitHub data
 */
async function pullCommand(options: any) {
  console.log('Pull command - to be implemented');
  const config = loadConfig(options);
  
  // TODO: Implement pull logic
  // 1. Verify no concurrent pull execution
  // 2. Initialize database
  // 3. Create GitHub client
  // 4. Fetch current user
  // 5. Pull repositories, discussions, issues, pull requests
  // 6. Rebuild search index
}

/**
 * MCP command - Start MCP server
 */
async function mcpCommand(options: any) {
  console.log('MCP command - to be implemented');
  const config = loadConfig(options);
  
  // TODO: Implement MCP server
  // 1. Initialize database
  // 2. Create MCP server with tools
  // 3. Start stdio transport
}

/**
 * Main entry point
 */
async function main() {
  const program = new Command();
  
  program
    .name('github-brain')
    .description('MCP server for searching GitHub discussions, issues, and pull requests')
    .version(`${VERSION} (${BUILD_DATE})`);
  
  // Login command
  program
    .command('login')
    .description('Authenticate with GitHub using OAuth')
    .option('-m, --home-dir <dir>', 'Home directory', path.join(os.homedir(), '.github-brain'))
    .action(loginCommand);
  
  // Pull command
  program
    .command('pull')
    .description('Populate local database with GitHub data')
    .option('-o, --organization <org>', 'GitHub organization')
    .option('-m, --home-dir <dir>', 'Home directory', path.join(os.homedir(), '.github-brain'))
    .option('-i, --items <items>', 'Items to pull (comma-separated): repositories,discussions,issues,pull-requests')
    .option('-f, --force', 'Remove all data before pulling')
    .option('-e, --excluded-repositories <repos>', 'Repositories to exclude (comma-separated)')
    .action(pullCommand);
  
  // MCP command
  program
    .command('mcp')
    .description('Start MCP server')
    .option('-o, --organization <org>', 'GitHub organization')
    .option('-m, --home-dir <dir>', 'Home directory', path.join(os.homedir(), '.github-brain'))
    .action(mcpCommand);
  
  await program.parseAsync(process.argv);
}

// Run main function
main().catch((error) => {
  console.error('Error:', error.message);
  process.exit(1);
});
