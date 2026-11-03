import { useInfiniteQuery } from '@tanstack/react-query';
import { api, type Page } from '../api';

export function usePage<T>(identity: string[], resource: string, path: string, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: [...identity, resource, path],
    enabled,
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      api<Page<T>>(`${path}?limit=50&after=${encodeURIComponent(pageParam)}`, { signal }),
    getNextPageParam: (page) => page.next_cursor || undefined,
  });
}
