// @ts-nocheck
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

function parseMCPResponse(responseData: string): SearchResult[] {
  // Handle both single line responses and multi-line responses
  const lines = responseData.split("\n").filter((line) => line.trim());

  // Try to parse each line as JSON
  for (const line of lines) {
    try {
      const response = JSON.parse(line);
      console.log("Parsed JSON response:", response);

      if (response.result && response.result.content) {
        const content = response.result.content[0]?.text || "";
        console.log("Extracted content:", content);
        return parseSearchResults(content);
      } else if (response.error) {
        console.error("MCP returned error:", response.error);
        throw new Error(
          `MCP server error: ${response.error.message || "Unknown error"}`
        );
      }
    } catch (e) {
      console.log("Failed to parse line as JSON:", line, "Error:", e);
      // Continue to next line
    }
  }

  console.log("No valid JSON-RPC response found in:", responseData);
  return [];
}

interface Preferences {
  mcpCommand: string;
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

function getIconAndColor(type: string, state: string) {
  switch (`${type}:${state}`) {
    case "issue:open":
      return { icon: Icon.Circle, color: "#1a7f37" };
    case "issue:closed":
      return { icon: Icon.XMarkCircle, color: "#8250df" };
    case "pull_request:open":
      return { icon: Icon.CircleEllipsis, color: "#1a7f37" };
    case "pull_request:closed":
      return { icon: Icon.XMarkCircle, color: "#cf222e" };
    case "pull_request:merged":
      return { icon: Icon.CheckCircle, color: "#8250df" };
    case "discussion:open":
      return { icon: Icon.SpeechBubble, color: "#1a7f37" };
    case "discussion:closed":
      return { icon: Icon.SpeechBubbleImportant, color: "#8250df" };
    default:
      return { icon: Icon.Circle, color: Color.SecondaryText };
  }
}

async function callMCPSearch(query: string): Promise<SearchResult[]> {
  if (!query.trim()) return [];

  return new Promise((resolve, reject) => {
    const preferences = getPreferenceValues<Preferences>();
    let mcpCommand = preferences.mcpCommand;

    console.log("=== MCP Search Debug Info ===");
    console.log("Query:", query);
    console.log("Raw mcpCommand from preferences:", mcpCommand);

    // Auto-fix old command format if detected
    if (mcpCommand && mcpCommand.includes(" -db ")) {
      // Check if this is the old format: "/path/to/binary -db /path/to/db"
      const parts = mcpCommand.split(" ");
      if (parts.length >= 3 && parts[1] === "-db" && !parts.includes("mcp")) {
        // Convert to new format: "/path/to/binary mcp -db /path/to/db"
        const binaryPath = parts[0];
        const dbPath = parts.slice(2).join(" ");
        mcpCommand = `${binaryPath} mcp -db ${dbPath}`;
        console.log("Auto-corrected command format to:", mcpCommand);
      }
    }

    // Parse command and arguments - the command should now include 'mcp' in the right place
    const commandParts = mcpCommand.split(" ");
    const binaryPath = commandParts[0];
    const args = commandParts.slice(1);

    console.log("Parsed binary path:", binaryPath);
    console.log("Parsed args:", args);
    console.log("Full command will be:", binaryPath, args.join(" "));

    // Start the MCP server process
    const mcpProcess = spawn(binaryPath, args, {
      stdio: ["pipe", "pipe", "pipe"],
      env: {
        ...process.env,
        ORGANIZATION: "github", // Set the required environment variable
      },
    });

    let responseData = "";
    let errorData = "";
    let hasReceivedResponse = false;
    let responseTimeout: NodeJS.Timeout;

    // Set a timeout for the MCP response
    responseTimeout = setTimeout(() => {
      if (!hasReceivedResponse) {
        console.log("MCP request timed out after 10 seconds");
        mcpProcess.kill();
        reject(new Error("MCP request timed out"));
      }
    }, 10000); // 10 second timeout

    mcpProcess.stdout.on("data", (data) => {
      const output = data.toString();
      console.log("MCP stdout:", output);
      responseData += output;

      // Check if we have a complete JSON-RPC response
      const lines = responseData.split("\n");
      for (const line of lines) {
        if (
          line.trim() &&
          line.includes('"jsonrpc":"2.0"') &&
          line.includes('"id":1')
        ) {
          hasReceivedResponse = true;
          clearTimeout(responseTimeout);

          // Process the response immediately
          try {
            console.log("Processing complete response:", line);
            const results = parseMCPResponse(line);
            console.log("Parsed results:", results);
            mcpProcess.kill(); // Clean up the process
            resolve(results);
            return;
          } catch (error) {
            console.error("Parse error:", error);
            mcpProcess.kill();
            reject(new Error(`Failed to parse MCP response: ${error.message}`));
            return;
          }
        }
      }
    });

    mcpProcess.stderr.on("data", (data) => {
      const error = data.toString();
      console.log("MCP stderr:", error);
      errorData += error;
    });

    mcpProcess.on("error", (error) => {
      console.error("MCP process error:", error);
      clearTimeout(responseTimeout);
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

    console.log(
      "Sending JSON-RPC request:",
      JSON.stringify(searchRequest, null, 2)
    );

    try {
      mcpProcess.stdin.write(JSON.stringify(searchRequest) + "\n");
      // Don't end stdin immediately - let the process handle the response first
      // The stdin will be closed when we kill the process after getting the response
    } catch (error) {
      console.error("Error writing to MCP stdin:", error);
      clearTimeout(responseTimeout);
      reject(
        new Error(`Failed to send request to MCP server: ${error.message}`)
      );
    }

    mcpProcess.on("close", (code) => {
      console.log("MCP process closed with code:", code);
      clearTimeout(responseTimeout);

      // Only handle close event if we haven't already processed a response
      if (!hasReceivedResponse) {
        console.log("Final stdout:", responseData);
        console.log("Final stderr:", errorData);

        if (code !== 0) {
          const errorMessage = `MCP server exited with code ${code}: ${errorData}`;
          console.error("MCP Error:", errorMessage);
          reject(new Error(errorMessage));
          return;
        }

        // Try to parse any remaining response data
        try {
          const results = parseMCPResponse(responseData);
          console.log("Parsed results from close event:", results);
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
