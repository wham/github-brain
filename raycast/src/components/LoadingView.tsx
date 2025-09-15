import { List } from '@raycast/api';

interface LoadingViewProps {
  message?: string;
}

export function LoadingView({ message = 'Searching...' }: LoadingViewProps) {
  return <List isLoading={true} searchBarPlaceholder={message} />;
}