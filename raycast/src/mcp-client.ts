import { spawn, ChildProcess } from 'child_process';
import { EventEmitter } from 'events';
import { existsSync, accessSync, constants } from 'fs';
import { join, resolve } from 'path';
import { homedir } from 'os';
import { getPreferenceValues } from '@raycast/api';
import { MCPRequest, MCPResponse, SearchResult, SearchToolParams } from './types';

interface Preferences {
  githubBrainPath?: string;
  organization?: string;
  resultsLimit: string;
  autoOpen: boolean;
  previewLength: string;
}

export class MCPClient extends EventEmitter {
  private process: ChildProcess | null = null;
  private requestId = 0;
  private pendingRequests = new Map<number | string, { resolve: Function; reject: Function }>();
  private buffer = '';
  private isConnected = false;

  constructor() {
    super();
  }

  private findGithubBrainScript(): { script?: string; binary?: string; cwd: string } | null {
    const preferences = getPreferenceValues<Preferences>();
    
    // Check user-configured path first (now expecting path to github-brain directory)
    if (preferences.githubBrainPath) {
      // Handle both absolute and relative paths
      let projectPath = preferences.githubBrainPath;
      
      // If path starts with ~, expand to home directory
      if (projectPath.startsWith('~')) {
        projectPath = projectPath.replace('~', homedir());
      }
      
      // Check if binary exists first (more reliable in Raycast)
      const binaryPath = join(projectPath, 'build', 'github-brain');
      const scriptPath = join(projectPath, 'scripts', 'run');
      
      if (existsSync(binaryPath)) {
        try {
          accessSync(binaryPath, constants.X_OK);
          console.log('Found GitHub Brain binary at:', binaryPath);
          return { binary: binaryPath, cwd: projectPath };
        } catch {
          console.log('Binary not executable:', binaryPath);
        }
      }
      
      // Fallback to scripts/run if binary not available
      if (existsSync(scriptPath)) {
        try {
          accessSync(scriptPath, constants.X_OK);
          console.log('Found GitHub Brain script at:', scriptPath);
          return { script: scriptPath, cwd: projectPath };
        } catch {
          console.log('Script not executable:', scriptPath);
        }
      }
    }

    // Check common project locations for scripts/run
    const projectLocations = [
      join(homedir(), 'code', 'github-brain'),
      join(homedir(), 'github-brain'),
      join(homedir(), 'Documents', 'github-brain'),
      join(homedir(), 'Projects', 'github-brain'),
      join(homedir(), 'dev', 'github-brain'),
      join(homedir(), 'workspace', 'github-brain'),
      join(homedir(), 'src', 'github-brain'),
      join(homedir(), 'repos', 'github-brain'),
      '/Users/wham/code/github-brain' // Your specific location
    ];

    console.log('Searching for GitHub Brain project with scripts/run...');
    
    for (const projectDir of projectLocations) {
      const binaryPath = join(projectDir, 'build', 'github-brain');
      const scriptPath = join(projectDir, 'scripts', 'run');
      
      // Check binary first (more reliable in Raycast)
      if (existsSync(binaryPath)) {
        try {
          accessSync(binaryPath, constants.X_OK);
          console.log('Found GitHub Brain binary at:', binaryPath);
          return { binary: binaryPath, cwd: projectDir };
        } catch {
          console.log('Binary found but not executable:', binaryPath);
        }
      }
      
      // Fallback to scripts/run
      if (existsSync(scriptPath)) {
        try {
          accessSync(scriptPath, constants.X_OK);
          console.log('Found GitHub Brain script at:', scriptPath);
          return { script: scriptPath, cwd: projectDir };
        } catch {
          console.log('Found but not executable:', scriptPath);
        }
      }
    }

    console.error('GitHub Brain scripts/run not found in any location');
    console.error('Searched project directories:', projectLocations);
    return null;
  }

  async connect(): Promise<void> {
    if (this.isConnected) {
      return;
    }

    const info = this.findGithubBrainScript();
    if (!info) {
      throw new Error(
        "GitHub Brain project not found. Please ensure the github-brain project is available, or set the path in preferences."
      );
    }

    return new Promise((resolve, reject) => {
      // Get organization from environment, preferences, or use a default
      const preferences = getPreferenceValues<Preferences>();
      const organization = process.env.GITHUB_BRAIN_ORG || 
                          process.env.ORGANIZATION || 
                          preferences.organization ||
                          'github';
      
      // Ensure PATH includes system directories and Go installation locations
      const systemPaths = [
        '/usr/bin',
        '/bin',
        '/usr/sbin',
        '/sbin',
        '/usr/local/bin',
        '/opt/homebrew/bin',
        '/usr/local/go/bin',
        join(homedir(), 'go', 'bin'),
        join(homedir(), '.local', 'bin')
      ];
      
      // Prepend system paths to ensure basic commands are available
      const envPath = systemPaths.join(':') + (process.env.PATH ? ':' + process.env.PATH : '');
      
      // Set database directory - default to ./db relative to project
      const dbDir = join(info.cwd, 'db');
      
      // Prefer binary over script for better reliability in Raycast environment
      if (info.binary) {
        // Use pre-built binary directly - more reliable in Raycast
        console.log('Starting MCP server using pre-built binary...');
        console.log('Organization:', organization);
        console.log('DB Directory:', dbDir);
        
        this.process = spawn(info.binary, ['mcp', '-o', organization, '-d', dbDir], {
          stdio: ['pipe', 'pipe', 'pipe'],
          cwd: info.cwd,
          env: { 
            ...process.env,
            PATH: envPath,
            ORGANIZATION: organization,
            DB_DIR: dbDir,
            HOME: homedir()
          }
        });
      } else if (info.script) {
        // Fallback to scripts/run if binary not available
        console.log('Starting MCP server using scripts/run...');
        console.log('Organization:', organization);
        console.log('DB Directory:', dbDir);
        
        this.process = spawn('/bin/bash', ['-l', '-c', `cd "${info.cwd}" && "${info.script}" mcp -o ${organization} -d ${dbDir}`], {
          stdio: ['pipe', 'pipe', 'pipe'],
          cwd: info.cwd,
          env: { 
            ...process.env,
            PATH: envPath,
            ORGANIZATION: organization,
            DB_DIR: dbDir,
            HOME: homedir()
          },
          shell: false
        });
      } else {
        throw new Error('Neither script nor binary found for GitHub Brain');
      }

      this.process.stdout?.on('data', (data: Buffer) => {
        this.buffer += data.toString();
        this.processBuffer();
      });

      let stderrBuffer = '';
      this.process.stderr?.on('data', (data: Buffer) => {
        const stderr = data.toString();
        stderrBuffer += stderr;
        console.error('MCP stderr:', stderr);
      });

      this.process.on('error', (error) => {
        this.isConnected = false;
        this.emit('error', error);
        reject(new Error(`Failed to start GitHub Brain MCP server: ${error.message}`));
      });

      this.process.on('exit', (code) => {
        this.isConnected = false;
        this.emit('disconnected', code);
        if (code !== 0 && code !== null) {
          const errorMsg = stderrBuffer.trim() || `GitHub Brain MCP server exited with code ${code}`;
          console.error('MCP server failed:', errorMsg);
          reject(new Error(errorMsg));
        }
      });

      // Send initialization request
      this.sendRequest('initialize', {
        protocolVersion: '2024-11-05',
        capabilities: {},
        clientInfo: {
          name: 'github-brain-raycast',
          version: '1.0.0'
        }
      }).then(() => {
        this.isConnected = true;
        resolve();
      }).catch(reject);
    });
  }

  private processBuffer(): void {
    const lines = this.buffer.split('\n');
    this.buffer = lines.pop() || '';

    for (const line of lines) {
      if (line.trim()) {
        try {
          const message = JSON.parse(line);
          this.handleMessage(message);
        } catch (error) {
          console.error('Failed to parse MCP message:', line, error);
        }
      }
    }
  }

  private handleMessage(message: MCPResponse): void {
    if (message.id !== undefined) {
      const pending = this.pendingRequests.get(message.id);
      if (pending) {
        this.pendingRequests.delete(message.id);
        if (message.error) {
          pending.reject(new Error(message.error.message));
        } else {
          pending.resolve(message.result);
        }
      }
    }
  }

  private sendRequest(method: string, params?: any): Promise<any> {
    return new Promise((resolve, reject) => {
      if (!this.process || !this.process.stdin) {
        reject(new Error('MCP server not connected'));
        return;
      }

      const id = ++this.requestId;
      const request: MCPRequest = {
        jsonrpc: '2.0',
        method,
        params,
        id
      };

      this.pendingRequests.set(id, { resolve, reject });
      
      const message = JSON.stringify(request) + '\n';
      this.process.stdin.write(message, (error) => {
        if (error) {
          this.pendingRequests.delete(id);
          reject(error);
        }
      });

      // Timeout after 30 seconds
      setTimeout(() => {
        if (this.pendingRequests.has(id)) {
          this.pendingRequests.delete(id);
          reject(new Error(`Request timeout: ${method}`));
        }
      }, 30000);
    });
  }

  async search(query: string): Promise<SearchResult[]> {
    if (!this.isConnected) {
      await this.connect();
    }

    const preferences = getPreferenceValues<Preferences>();
    const previewLength = parseInt(preferences.previewLength);

    try {
      // Call the search tool
      const response = await this.sendRequest('tools/call', {
        name: 'search',
        arguments: {
          query,
          fields: ['title', 'url', 'repository', 'created_at', 'author', 'type', 'state', 'body']
        } as SearchToolParams
      });

      // Parse the response content
      if (!response?.content?.[0]?.text) {
        return [];
      }

      const content = response.content[0].text;
      
      // Check for "No results found" message
      if (content.includes('No results found')) {
        return [];
      }

      // Check for pull in progress error
      if (content.includes('A pull is currently running')) {
        throw new Error('A pull is currently running. Please wait until it finishes.');
      }

      // Parse the results
      const results: SearchResult[] = [];
      const sections = content.split('---').filter((s: string) => s.trim());

      for (const section of sections) {
        const titleMatch = section.match(/^##\s+(.+)$/m);
        const urlMatch = section.match(/- URL:\s+(.+)$/m);
        const typeMatch = section.match(/- Type:\s+(.+)$/m);
        const repoMatch = section.match(/- Repository:\s+(.+)$/m);
        const createdMatch = section.match(/- Created at:\s+(.+)$/m);
        const authorMatch = section.match(/- Author:\s+(.+)$/m);
        const stateMatch = section.match(/- State:\s+(.+)$/m);

        if (titleMatch && urlMatch) {
          // Extract body content (everything after the metadata lines)
          const metadataEnd = section.lastIndexOf('- ');
          const bodyStart = section.indexOf('\n', metadataEnd) + 1;
          let body = section.substring(bodyStart).trim();
          
          // Truncate body to preview length
          if (body.length > previewLength) {
            body = body.substring(0, previewLength) + '...';
          }

          const type = typeMatch?.[1]?.toLowerCase() as 'discussion' | 'issue' | 'pull_request' || 'issue';
          
          results.push({
            title: titleMatch[1],
            url: urlMatch[1],
            type,
            repository: repoMatch?.[1] || '',
            created_at: createdMatch?.[1] || '',
            author: authorMatch?.[1] || '',
            state: stateMatch?.[1]?.toLowerCase() as 'open' | 'closed',
            body
          });
        }
      }

      // Limit results based on preference
      const limit = parseInt(preferences.resultsLimit);
      return results.slice(0, limit);
    } catch (error) {
      console.error('Search error:', error);
      throw error;
    }
  }

  disconnect(): void {
    if (this.process) {
      this.process.kill();
      this.process = null;
      this.isConnected = false;
    }
    this.pendingRequests.clear();
  }
}