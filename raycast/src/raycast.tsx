// @ts-nocheck
import React, { useState, useEffect } from "react";
import { ActionPanel, Action, List, Icon, Color } from "@raycast/api";
import { spawn } from "child_process";
import { promisify } from "util";

interface SearchResult {
  title: string;
  url: string;
  repository: string;
  type: "issue" | "pull_request" | "discussion";
  state: "open" | "closed" | "merged";
  author: string;
  created_at: string;
}

function getIconAndColor(type: string, state: string) {
  switch (`${type}:${state}`) {
    case "issue:open":
      return { icon: Icon.Circle, color: Color.Green };
    case "issue:closed":
      return { icon: Icon.XMarkCircle, color: Color.Purple };
    case "pull_request:open":
      return { icon: Icon.CircleEllipsis, color: Color.Green };
    case "pull_request:closed":
      return { icon: Icon.XMarkCircle, color: Color.Red };
    case "pull_request:merged":
      return { icon: Icon.CheckCircle, color: Color.Purple };
    case "discussion:open":
      return { icon: Icon.SpeechBubble, color: Color.Green };
    case "discussion:closed":
      return { icon: Icon.SpeechBubbleImportant, color: Color.Purple };
    default:
      return { icon: Icon.Circle, color: Color.SecondaryText };
  }
}

async function callMCPSearch(query: string): Promise<SearchResult[]> {
  if (!query.trim()) return [];

  return new Promise((resolve, reject) => {
    // Get the full path to scripts/run from environment variable
    const scriptPath = process.env.GITHUB_BRAIN_SCRIPT_PATH || "../scripts/run";
    
    // Start the MCP server process
    const mcpProcess = spawn(scriptPath, ["mcp"], {
      stdio: ["pipe", "pipe", "pipe"],
    });

    let responseData = "";
    let errorData = "";

    mcpProcess.stdout.on("data", (data) => {
      responseData += data.toString();
    });

    mcpProcess.stderr.on("data", (data) => {
      errorData += data.toString();
    });

    mcpProcess.on("error", (error) => {
      reject(new Error(`Failed to start MCP server: ${error.message}`));
    });

    // Send the search request via JSON-RPC
    const searchRequest = {
      jsonrpc: "2.0",
      id: 1,
      method: "tools/call",
      params: {
        name: "search",
        arguments: {
          query: query,
          fields: [
            "title",
            "url",
            "repository",
            "created_at",
            "author",
            "type",
            "state",
          ],
        },
      },
    };

    mcpProcess.stdin.write(JSON.stringify(searchRequest) + "\n");
    mcpProcess.stdin.end();

    mcpProcess.on("close", (code) => {
      if (code !== 0) {
        reject(new Error(`MCP server exited with code ${code}: ${errorData}`));
        return;
      }

      try {
        // Parse the MCP response
        const results = parseMCPResponse(responseData);
        resolve(results);
      } catch (error) {
        reject(new Error(`Failed to parse MCP response: ${error.message}`));
      }
    });
  });
}

function parseMCPResponse(responseData: string): SearchResult[] {
  const lines = responseData.split("\n").filter((line) => line.trim());

  for (const line of lines) {
    try {
      const response = JSON.parse(line);
      if (response.result && response.result.content) {
        const content = response.result.content[0]?.text || "";
        return parseSearchResults(content);
      }
    } catch (e) {
      // Continue to next line
    }
  }

  return [];
}

function parseSearchResults(content: string): SearchResult[] {
  const results: SearchResult[] = [];
  const sections = content.split("---").filter((section) => section.trim());

  for (const section of sections) {
    const lines = section.trim().split("\n");
    if (lines.length < 2) continue;

    const titleMatch = lines[0].match(/^##\s*(.+)$/);
    if (!titleMatch) continue;

    const title = titleMatch[1];
    let url = "";
    let repository = "";
    let type: "issue" | "pull_request" | "discussion" = "issue";
    let state: "open" | "closed" | "merged" = "open";
    let author = "";
    let created_at = "";

    for (const line of lines.slice(1)) {
      const urlMatch = line.match(/^-\s*URL:\s*(.+)$/);
      const repoMatch = line.match(/^-\s*Repository:\s*(.+)$/);
      const typeMatch = line.match(/^-\s*Type:\s*(.+)$/);
      const stateMatch = line.match(/^-\s*State:\s*(.+)$/);
      const authorMatch = line.match(/^-\s*Author:\s*(.+)$/);
      const createdMatch = line.match(/^-\s*Created at:\s*(.+)$/);

      if (urlMatch) url = urlMatch[1];
      if (repoMatch) repository = repoMatch[1];
      if (typeMatch)
        type = typeMatch[1] as "issue" | "pull_request" | "discussion";
      if (stateMatch) state = stateMatch[1] as "open" | "closed" | "merged";
      if (authorMatch) author = authorMatch[1];
      if (createdMatch) created_at = createdMatch[1];
    }

    if (url && title) {
      results.push({
        title,
        url,
        repository,
        type,
        state,
        author,
        created_at,
      });
    }
  }

  return results.slice(0, 10); // Limit to 10 results as specified
}

export default function Command() {
  const [searchText, setSearchText] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (searchText.trim()) {
      setIsLoading(true);
      setError(null);

      callMCPSearch(searchText)
        .then((searchResults) => {
          setResults(searchResults);
          setIsLoading(false);
        })
        .catch((err) => {
          setError(err.message);
          setResults([]);
          setIsLoading(false);
        });
    } else {
      setResults([]);
      setError(null);
      setIsLoading(false);
    }
  }, [searchText]);

  return (
    <List
      isLoading={isLoading}
      onSearchTextChange={setSearchText}
      searchBarPlaceholder="Search discussions, issues, and pull requests..."
      throttle={true}
    >
      {error ? (
        <List.Item
          title="Error occurred"
          subtitle={error}
          icon={{ source: Icon.ExclamationMark, tintColor: Color.Red }}
        />
      ) : results.length === 0 && searchText.trim() ? (
        <List.Item
          title="No results found"
          subtitle={`No results for "${searchText}"`}
          icon={{
            source: Icon.MagnifyingGlass,
            tintColor: Color.SecondaryText,
          }}
        />
      ) : (
        results.map((result, index) => {
          const { icon, color } = getIconAndColor(result.type, result.state);

          return (
            <List.Item
              key={`${result.url}-${index}`}
              title={result.title}
              subtitle={result.repository}
              icon={{ source: icon, tintColor: color }}
              accessories={[
                { text: result.author },
                { text: new Date(result.created_at).toLocaleDateString() },
              ]}
              actions={
                <ActionPanel>
                  <Action.OpenInBrowser url={result.url} />
                  <Action.CopyToClipboard
                    title="Copy URL"
                    content={result.url}
                  />
                </ActionPanel>
              }
            />
          );
        })
      )}
    </List>
  );
}
