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
  
  // Truncate title for display
  const displayTitle = truncateText(result.title, 60);
  
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
      detail={
        <List.Item.Detail
          markdown={`# ${result.title}\n\n${result.body}`}
          metadata={
            <List.Item.Detail.Metadata>
              <List.Item.Detail.Metadata.Label title="Type" text={result.type.replace('_', ' ')} />
              <List.Item.Detail.Metadata.Label title="Repository" text={result.repository} />
              <List.Item.Detail.Metadata.Label title="Author" text={result.author} />
              <List.Item.Detail.Metadata.Label title="Created" text={result.created_at} />
              {result.state && (
                <List.Item.Detail.Metadata.Label title="Status" text={result.state} />
              )}
              <List.Item.Detail.Metadata.Separator />
              <List.Item.Detail.Metadata.Link title="Open on GitHub" target={result.url} text={result.url} />
            </List.Item.Detail.Metadata>
          }
        />
      }
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