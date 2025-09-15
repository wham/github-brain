import { List, Icon } from '@raycast/api';

interface ErrorViewProps {
  error: string;
}

export function ErrorView({ error }: ErrorViewProps) {
  let icon = Icon.ExclamationMark;
  let title = 'Error';
  let description = error;

  // Customize error messages based on type
  if (error.includes('binary not found')) {
    icon = Icon.Download;
    title = 'GitHub Brain Not Found';
    description = "Please install GitHub Brain and build the binary:\n\n`go build -o ./build/github-brain`";
  } else if (error.includes('database not found')) {
    icon = Icon.HardDrive;
    title = 'Database Not Found';
    description = "Please run 'github-brain pull' to initialize the database first.";
  } else if (error.includes('pull is currently running')) {
    icon = Icon.Clock;
    title = 'Pull In Progress';
    description = 'A data synchronization is currently running. Please wait until it finishes.';
  } else if (error.includes('timeout')) {
    icon = Icon.WifiDisabled;
    title = 'Connection Timeout';
    description = 'Unable to connect to GitHub Brain server. Please check if the server is running.';
  }

  return (
    <List.EmptyView
      icon={icon}
      title={title}
      description={description}
    />
  );
}