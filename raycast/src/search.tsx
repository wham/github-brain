// @ts-nocheck
/// <reference types="@raycast/api" />
import React, { useState, useEffect } from "react";
import {
  ActionPanel,
  Action,
  List,
  Icon,
  Color,
  getPreferenceValues,
} from "@raycast/api";
import { spawn } from "child_process";

interface Preferences {
  githubBrainCommand: string;
  homeDir: string;
}

interface SearchResult {
  title: string;
  url: string;
  repository: string;
  type: "issue" | "pull_request" | "discussion";
  state: "open" | "closed" | "merged";
  author: string;
  created_at: string;
}

function getIconAndColor(
  type: string,
  state: string
): { icon: Icon; color: Color } {
  switch (`${type}:${state}`) {
    case "issue:open":
      return { icon: Icon.Circle, color: Color.Green };
    case "issue:closed":
      return { icon: Icon.Circle, color: Color.Purple };
    case "pull_request:open":
      return { icon: Icon.Code, color: Color.Green };
    case "pull_request:closed":
      return { icon: Icon.Code, color: Color.Red };
    case "pull_request:merged":
      return { icon: Icon.CheckCircle, color: Color.Purple };
    case "discussion:open":
      return { icon: Icon.SpeechBubble, color: Color.Green };
    default:
      return { icon: Icon.Circle, color: Color.SecondaryText };
  }
}

function expandPath(path: string): string {
  if (path.startsWith("~/")) {
    return path.replace("~", process.env.HOME || "~");
  }
  return path;
}

async function callMCPSearch(query: string): Promise<SearchResult[]> {
  if (!query.trim()) return [];

  return new Promise((resolve, reject) => {
    const preferences = getPreferenceValues<Preferences>();

    // Build the command: <githubBrainCommand> mcp [-m <homeDir>]
    // Organization will be loaded from .env file in the home directory
    const binaryPath = expandPath(preferences.githubBrainCommand);
    const args = ["mcp"];
    if (preferences.homeDir) {
      const expandedHomeDir = expandPath(preferences.homeDir);
      args.push("-m", expandedHomeDir);
      console.log(`Using home directory: ${expandedHomeDir}`);
    }
    console.log(`Spawning MCP server: ${binaryPath} ${args.join(" ")}`);

    // Start the MCP server process
    const mcpProcess = spawn(binaryPath, args, {
      stdio: ["pipe", "pipe", "pipe"],
      env: {
        ...process.env,
        PATH:
          process.env.PATH || "/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin",
      },
      shell: false,
    });

    let responseData = "";
    let errorData = "";
    let hasReceivedResponse = false;
    let responseTimeout: NodeJS.Timeout;
    let isInitialized = false;

    // Set a timeout for the MCP response
    responseTimeout = setTimeout(() => {
      if (!hasReceivedResponse) {
        mcpProcess.kill();
        reject(new Error("MCP request timed out"));
      }
    }, 10000); // 10 second timeout

    mcpProcess.stdout.on("data", (data) => {
      const output = data.toString();
      responseData += output;

      // Process line-delimited JSON-RPC messages
      const lines = responseData.split("\n");
      responseData = lines.pop() || ""; // Keep incomplete line for next data event

      for (const line of lines) {
        if (!line.trim()) continue;

        try {
          const message = JSON.parse(line);

          // Handle initialize response
          if (message.id === 1 && message.result && !isInitialized) {
            console.log("MCP server initialized");
            isInitialized = true;

            // Send initialized notification
            const initializedNotification = {
              jsonrpc: "2.0",
              method: "notifications/initialized",
              params: {},
            };
            mcpProcess.stdin.write(
              JSON.stringify(initializedNotification) + "\n"
            );

            // Now send the actual search request
            const searchRequest = {
              jsonrpc: "2.0",
              id: 2,
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
            continue;
          }

          // Handle search response
          if (message.id === 2 && message.result) {
            hasReceivedResponse = true;
            clearTimeout(responseTimeout);

            try {
              const results = parseMCPResponse(line);
              mcpProcess.kill(); // Clean up the process
              resolve(results);
              return;
            } catch (error) {
              console.error("Parse error:", error);
              mcpProcess.kill();
              reject(
                new Error(`Failed to parse MCP response: ${error.message}`)
              );
              return;
            }
          }

          // Handle errors
          if (message.error) {
            console.error("MCP returned error:", message.error);
            mcpProcess.kill();
            reject(
              new Error(
                `MCP error: ${message.error.message || "Unknown error"}`
              )
            );
            return;
          }
        } catch (parseError) {
          // Ignore parse errors for incomplete messages
          console.log("Skipping unparseable message:", line);
        }
      }
    });

    mcpProcess.stderr.on("data", (data) => {
      const error = data.toString();
      errorData += error;
      console.error("MCP stderr:", error);
    });

    mcpProcess.on("error", (error) => {
      console.error("MCP process error:", error);
      if (error.code === "ENOENT") {
        reject(
          new Error(
            `GitHub Brain executable not found: "${binaryPath}". Please check the executable path in preferences.`
          )
        );
      } else {
        reject(new Error(`Failed to start MCP server: ${error.message}`));
      }
    });

    // Start the MCP handshake with initialize request
    const initializeRequest = {
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        protocolVersion: "2025-06-18",
        clientInfo: {
          name: "raycast-github-brain",
          version: "1.0.0",
        },
        capabilities: {},
      },
    };

    try {
      mcpProcess.stdin.write(JSON.stringify(initializeRequest) + "\n");
    } catch (error) {
      console.error("Error writing to MCP stdin:", error);
      clearTimeout(responseTimeout);
      reject(
        new Error(`Failed to send request to MCP server: ${error.message}`)
      );
    }

    mcpProcess.on("close", (code) => {
      clearTimeout(responseTimeout);

      // Only handle close event if we haven't already processed a response
      if (!hasReceivedResponse) {
        if (code !== 0) {
          let errorMessage = `MCP server exited with code ${code}: ${errorData}`;
          if (preferences.homeDir) {
            errorMessage += `\n\nHome directory: ${expandPath(preferences.homeDir)}`;
          } else {
            errorMessage += `\n\nNo home directory specified. Using default: ~/.github-brain`;
          }
          errorMessage += `\nExecutable: ${binaryPath}`;
          console.error("MCP Error:", errorMessage);
          reject(new Error(errorMessage));
          return;
        }

        // Try to parse any remaining response data
        try {
          const results = parseMCPResponse(responseData);
          resolve(results);
        } catch (error) {
          console.error("Parse error on close:", error);
          reject(new Error(`Failed to parse MCP response: ${error.message}`));
        }
      }
    });
  });
}

function parseMCPResponse(responseData: string): SearchResult[] {
  // Handle both single line responses and multi-line responses
  const lines = responseData.split("\n").filter((line) => line.trim());

  // Try to parse each line as JSON
  for (const line of lines) {
    try {
      const response = JSON.parse(line);

      if (response.result && response.result.content) {
        const content = response.result.content[0]?.text || "";
        return parseSearchResults(content);
      } else if (response.error) {
        console.error("MCP returned error:", response.error);
        throw new Error(
          `MCP server error: ${response.error.message || "Unknown error"}`
        );
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
      searchBarPlaceholder="Search GitHub issues, pull requests, and discussions..."
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
