import { List, ActionPanel, Action, Icon } from '@raycast/api';
import { SearchResult as SearchResultType } from '../types';
import { getTypeIcon } from '../utils';

interface SearchResultProps {
  result: SearchResultType;
  index: number;
}

export function SearchResult({ result }: SearchResultProps) {
  const typeIcon = getTypeIcon(result.type);

  // Display full title without truncation
  const displayTitle = result.title;

  // Format subtitle
  const subtitle = `${result.repository} • ${result.author}`;

  // Prepare accessories - state first, then type
  const accessories: List.Item.Accessory[] = [];

  if (result.state) {
    accessories.push({
      text: result.state === 'open' ? 'Open' : 'Closed',
      icon: result.state === 'open' ? Icon.Circle : Icon.CheckCircle,
      tooltip: `Status: ${result.state}`
    });
  }

  accessories.push({
    text: typeIcon,
    tooltip: result.type.replace('_', ' ')
  });

  return (
    <List.Item
      title={displayTitle}
      subtitle={subtitle}
      accessories={accessories}
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