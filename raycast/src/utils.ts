import { SearchResult } from './types';

export function getTypeIcon(type: SearchResult['type']): string {
  switch (type) {
    case 'discussion':
      return '💬';
    case 'issue':
      return '🐛';
    case 'pull_request':
      return '🔀';
    default:
      return '📄';
  }
}

export function formatRelativeTime(dateString: string): string {
  try {
    const date = new Date(dateString);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffSeconds = Math.floor(diffMs / 1000);
    const diffMinutes = Math.floor(diffSeconds / 60);
    const diffHours = Math.floor(diffMinutes / 60);
    const diffDays = Math.floor(diffHours / 24);
    const diffWeeks = Math.floor(diffDays / 7);
    const diffMonths = Math.floor(diffDays / 30);
    const diffYears = Math.floor(diffDays / 365);

    if (diffSeconds < 60) {
      return 'just now';
    } else if (diffMinutes < 60) {
      return `${diffMinutes} minute${diffMinutes === 1 ? '' : 's'} ago`;
    } else if (diffHours < 24) {
      return `${diffHours} hour${diffHours === 1 ? '' : 's'} ago`;
    } else if (diffDays < 7) {
      return `${diffDays} day${diffDays === 1 ? '' : 's'} ago`;
    } else if (diffWeeks < 4) {
      return `${diffWeeks} week${diffWeeks === 1 ? '' : 's'} ago`;
    } else if (diffMonths < 12) {
      return `${diffMonths} month${diffMonths === 1 ? '' : 's'} ago`;
    } else {
      return `${diffYears} year${diffYears === 1 ? '' : 's'} ago`;
    }
  } catch {
    return dateString;
  }
}

export function truncateText(text: string, maxLength: number): string {
  if (text.length <= maxLength) {
    return text;
  }
  return text.substring(0, maxLength - 3) + '...';
}

export function debounce<T extends (...args: any[]) => any>(
  func: T,
  wait: number
): (...args: Parameters<T>) => void {
  let timeout: NodeJS.Timeout | null = null;
  
  return (...args: Parameters<T>) => {
    if (timeout) {
      clearTimeout(timeout);
    }
    
    timeout = setTimeout(() => {
      func(...args);
    }, wait);
  };
}

let resultCache: { query: string; results: SearchResult[]; timestamp: number } | null = null;
const CACHE_DURATION = 30000; // 30 seconds

export function getCachedResults(query: string): SearchResult[] | null {
  if (!resultCache) {
    return null;
  }
  
  const now = Date.now();
  if (
    resultCache.query === query &&
    now - resultCache.timestamp < CACHE_DURATION
  ) {
    return resultCache.results;
  }
  
  return null;
}

export function setCachedResults(query: string, results: SearchResult[]): void {
  resultCache = {
    query,
    results,
    timestamp: Date.now()
  };
}

export function clearCache(): void {
  resultCache = null;
}