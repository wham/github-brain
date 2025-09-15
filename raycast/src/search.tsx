import { useState, useEffect, useCallback, useRef } from 'react';
import { List, showToast, Toast, getPreferenceValues, open, Icon } from '@raycast/api';
import { SearchResult as SearchResultType, SearchState } from './types';
import { MCPClient } from './mcp-client';
import { SearchResult } from './components/SearchResult';
import { ErrorView } from './components/ErrorView';
import { LoadingView } from './components/LoadingView';
import { debounce, getCachedResults, setCachedResults, clearCache } from './utils';

interface Preferences {
  autoOpen: boolean;
  resultsLimit: string;
}

export default function SearchCommand() {
  const [state, setState] = useState<SearchState>({
    query: '',
    results: [],
    isLoading: false,
    error: undefined
  });

  const mcpClientRef = useRef<MCPClient | null>(null);
  const preferences = getPreferenceValues<Preferences>();

  // Initialize MCP client
  useEffect(() => {
    const initClient = async () => {
      try {
        const client = new MCPClient();
        mcpClientRef.current = client;
        
        // Set up error handlers
        client.on('error', (error) => {
          console.error('MCP client error:', error);
          showToast({
            style: Toast.Style.Failure,
            title: 'Connection Error',
            message: error.message
          });
        });

        client.on('disconnected', () => {
          console.log('MCP client disconnected');
        });

        // Connect to the server
        await client.connect();
      } catch (error) {
        console.error('Failed to initialize MCP client:', error);
        setState(prev => ({
          ...prev,
          error: error instanceof Error ? error.message : 'Failed to connect to GitHub Brain'
        }));
      }
    };

    initClient();

    // Cleanup on unmount
    return () => {
      if (mcpClientRef.current) {
        mcpClientRef.current.disconnect();
      }
    };
  }, []);

  // Search function
  const performSearch = useCallback(async (searchQuery: string) => {
    if (!searchQuery || searchQuery.length < 2) {
      setState(prev => ({
        ...prev,
        results: [],
        isLoading: false,
        error: undefined
      }));
      return;
    }

    // Check cache first
    const cached = getCachedResults(searchQuery);
    if (cached) {
      setState(prev => ({
        ...prev,
        query: searchQuery,
        results: cached,
        isLoading: false,
        error: undefined
      }));
      
      // Auto-open if single result and preference is set
      if (cached.length === 1 && preferences.autoOpen) {
        await open(cached[0].url);
      }
      return;
    }

    setState(prev => ({
      ...prev,
      query: searchQuery,
      isLoading: true,
      error: undefined
    }));

    try {
      if (!mcpClientRef.current) {
        throw new Error('MCP client not initialized');
      }

      const results = await mcpClientRef.current.search(searchQuery);
      
      // Cache the results
      setCachedResults(searchQuery, results);
      
      setState(prev => ({
        ...prev,
        results,
        isLoading: false,
        error: undefined
      }));

      // Auto-open if single result and preference is set
      if (results.length === 1 && preferences.autoOpen) {
        await open(results[0].url);
        showToast({
          style: Toast.Style.Success,
          title: 'Opened in Browser',
          message: results[0].title
        });
      } else if (results.length === 0) {
        showToast({
          style: Toast.Style.Animated,
          title: 'No Results',
          message: 'Try different keywords'
        });
      }
    } catch (error) {
      console.error('Search error:', error);
      const errorMessage = error instanceof Error ? error.message : 'Search failed';
      
      setState(prev => ({
        ...prev,
        results: [],
        isLoading: false,
        error: errorMessage
      }));

      showToast({
        style: Toast.Style.Failure,
        title: 'Search Failed',
        message: errorMessage
      });
    }
  }, [preferences.autoOpen]);

  // Debounced search
  const debouncedSearch = useCallback(
    debounce((query: string) => {
      performSearch(query);
    }, 300),
    [performSearch]
  );

  // Handle search text change
  const handleSearchTextChange = useCallback((text: string) => {
    setState(prev => ({
      ...prev,
      query: text
    }));
    
    if (text.length >= 2) {
      debouncedSearch(text);
    } else {
      setState(prev => ({
        ...prev,
        results: [],
        error: undefined
      }));
    }
  }, [debouncedSearch]);

  // Render loading state
  if (state.isLoading && state.results.length === 0) {
    return <LoadingView message="Searching GitHub Brain..." />;
  }

  // Render error state
  if (state.error && !state.isLoading) {
    return (
      <List
        searchText={state.query}
        onSearchTextChange={handleSearchTextChange}
        searchBarPlaceholder="Search discussions, issues, and pull requests..."
        throttle
      >
        <ErrorView error={state.error} />
      </List>
    );
  }

  // Render search results
  return (
    <List
      isLoading={state.isLoading}
      searchText={state.query}
      onSearchTextChange={handleSearchTextChange}
      searchBarPlaceholder="Search discussions, issues, and pull requests..."
      throttle
      isShowingDetail={state.results.length > 0}
    >
      {state.results.length === 0 && state.query.length >= 2 && !state.isLoading ? (
        <List.EmptyView
          icon={Icon.MagnifyingGlass}
          title="No Results Found"
          description={`No results for "${state.query}". Try different keywords or check your filters.`}
        />
      ) : state.results.length === 0 && state.query.length < 2 ? (
        <List.EmptyView
          icon={Icon.MagnifyingGlass}
          title="Start Searching"
          description="Type at least 2 characters to search GitHub Brain"
        />
      ) : (
        <>
          {state.results.length > parseInt(preferences.resultsLimit) && (
            <List.Section
              title={`Showing top ${preferences.resultsLimit} results`}
              subtitle="Refine your search for more specific results"
            />
          )}
          {state.results.map((result, index) => (
            <SearchResult key={result.url} result={result} index={index} />
          ))}
        </>
      )}
    </List>
  );
}