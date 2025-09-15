export interface SearchState {
  query: string;
  results: SearchResult[];
  isLoading: boolean;
  error?: string;
}

export interface SearchResult {
  title: string;
  url: string;
  type: 'discussion' | 'issue' | 'pull_request';
  repository: string;
  author: string;
  created_at: string;
  state?: 'open' | 'closed';
  body: string;
}

export interface MCPRequest {
  jsonrpc: '2.0';
  method: string;
  params?: any;
  id: number | string;
}

export interface MCPResponse {
  jsonrpc: '2.0';
  result?: any;
  error?: MCPError;
  id: number | string;
}

export interface MCPError {
  code: number;
  message: string;
  data?: any;
}

export interface MCPToolCall {
  name: string;
  arguments: Record<string, any>;
}

export interface SearchToolParams {
  query: string;
  fields: string[];
}