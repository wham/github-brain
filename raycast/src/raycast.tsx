// @ts-nocheck
import React, { useState, useEffect } from "react";
import { ActionPanel, Action, List, Icon, Color } from "@raycast/api";

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

function searchMockData(query: string): SearchResult[] {
  if (!query.trim()) return [];

  // Mock data for now - in reality this would call the MCP server
  return [
    {
      title: `Mock issue: ${query}`,
      url: "https://github.com/example/repo/issues/1",
      repository: "example-repo",
      type: "issue",
      state: "open",
      author: "user123",
      created_at: new Date().toISOString(),
    },
    {
      title: `Mock PR: ${query}`,
      url: "https://github.com/example/repo/pulls/2",
      repository: "example-repo",
      type: "pull_request",
      state: "merged",
      author: "user456",
      created_at: new Date().toISOString(),
    },
  ];
}

export default function Command() {
  const [searchText, setSearchText] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [isLoading, setIsLoading] = useState(false);

  useEffect(() => {
    if (searchText.trim()) {
      setIsLoading(true);
      // Simulate async search
      setTimeout(() => {
        const searchResults = searchMockData(searchText);
        setResults(searchResults);
        setIsLoading(false);
      }, 300);
    } else {
      setResults([]);
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
      {results.length === 0 && searchText.trim() ? (
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
