const { spawn } = require('child_process');
const path = require('path');

// Test MCP connection using scripts/run
const projectPath = path.resolve('..');
const scriptPath = path.join(projectPath, 'scripts', 'run');
console.log('Testing MCP connection using:', scriptPath);
console.log('Working directory:', projectPath);

const dbDir = path.join(projectPath, 'db');
const mcpProcess = spawn(scriptPath, ['mcp', '-o', 'github', '-d', dbDir], {
  stdio: ['pipe', 'pipe', 'pipe'],
  cwd: projectPath,
  env: { ...process.env, ORGANIZATION: 'github', DB_DIR: dbDir }
});

let buffer = '';

mcpProcess.stdout.on('data', (data) => {
  buffer += data.toString();
  const lines = buffer.split('\n');
  buffer = lines.pop() || '';
  
  for (const line of lines) {
    if (line.trim()) {
      console.log('Received:', line);
      try {
        const message = JSON.parse(line);
        console.log('Parsed message:', message);
      } catch (e) {
        console.log('Not JSON:', line);
      }
    }
  }
});

mcpProcess.stderr.on('data', (data) => {
  console.error('MCP stderr:', data.toString());
});

mcpProcess.on('error', (error) => {
  console.error('Failed to start:', error);
});

mcpProcess.on('exit', (code) => {
  console.log('Process exited with code:', code);
});

// Send initialization
setTimeout(() => {
  const initRequest = {
    jsonrpc: '2.0',
    method: 'initialize',
    params: {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: {
        name: 'test-client',
        version: '1.0.0'
      }
    },
    id: 1
  };
  
  console.log('Sending initialization:', JSON.stringify(initRequest));
  mcpProcess.stdin.write(JSON.stringify(initRequest) + '\n');
  
  // Test search after init
  setTimeout(() => {
    const searchRequest = {
      jsonrpc: '2.0',
      method: 'tools/call',
      params: {
        name: 'search',
        arguments: {
          query: 'test',
          fields: ['title', 'url', 'type']
        }
      },
      id: 2
    };
    
    console.log('Sending search request:', JSON.stringify(searchRequest));
    mcpProcess.stdin.write(JSON.stringify(searchRequest) + '\n');
    
    // Exit after 5 seconds
    setTimeout(() => {
      console.log('Test complete, exiting...');
      mcpProcess.kill();
    }, 5000);
  }, 1000);
}, 500);