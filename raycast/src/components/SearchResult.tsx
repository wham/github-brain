import { List, ActionPanel, Action, Icon } from '@raycast/api';
import { SearchResult as SearchResultType } from '../types';
import { getResultIcon } from '../utils';

interface SearchResultProps {
  result: SearchResultType;
  index: number;
}

export function SearchResult({ result }: SearchResultProps) {
  // Get the appropriate icon based on type and state
  const icon = getResultIcon(result.type, result.state);

  // Display full title without truncation
  const displayTitle = result.title;

  // Format subtitle
  const subtitle = result.repository;

  return (
    <List.Item
      icon={icon}
      title={displayTitle}
      subtitle={subtitle}
      actions={
        <ActionPanel>
          <ActionPanel.Section>
            <Action.OpenInBrowser
              title="Open in Browser"
              url={result.url}
              icon={Icon.Globe}
            />
            <Action.CopyToClipboard
              title="Copy URL"
              content={result.url}
              shortcut={{ modifiers: ["cmd"], key: "c" }}
            />
            <Action.CopyToClipboard
              title="Copy Title"
              content={result.title}
              shortcut={{ modifiers: ["cmd", "shift"], key: "c" }}
            />
          </ActionPanel.Section>
        </ActionPanel>
      }
    />
  );
}