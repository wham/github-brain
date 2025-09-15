import { List, ActionPanel, Action, Icon, Color, getPreferenceValues } from '@raycast/api';
import { SearchResult as SearchResultType } from '../types';
import { getTypeIcon, formatRelativeTime, truncateText } from '../utils';

interface SearchResultProps {
  result: SearchResultType;
  index: number;
}

interface Preferences {
  autoOpen: boolean;
}

export function SearchResult({ result, index }: SearchResultProps) {
  const preferences = getPreferenceValues<Preferences>();
  const typeIcon = getTypeIcon(result.type);
  const relativeTime = formatRelativeTime(result.created_at);

  // Display full title without truncation
  const displayTitle = result.title;

  // Format subtitle
  const subtitle = `${result.repository} • ${result.author}`;
  
  // Prepare accessories
  const accessories: List.Item.Accessory[] = [
    { text: typeIcon, tooltip: result.type.replace('_', ' ') }
  ];
  
  if (result.state) {
    accessories.push({
      text: result.state === 'open' ? 'Open' : 'Closed',
      icon: result.state === 'open' ? Icon.Circle : Icon.CheckCircle,
      tooltip: `Status: ${result.state}`
    });
  }
  
  accessories.push({
    text: relativeTime,
    tooltip: result.created_at
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