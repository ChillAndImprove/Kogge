// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useEffect, useMemo, useRef, useState } from 'react';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { api, unwrap, upload, type Schemas } from '../../api/client';
import { tooLarge } from '../../app/uploads';
import type { UploadFile } from '../../components/editor/Editor';
import { nextOffset, uniqueItems } from './paging';

export type Space = Schemas['Space'];
export type Page = Schemas['Page'];
export type PageRef = Schemas['PageRef'];
export type PageSummary = Schemas['PageSummary'];

export function slug(title: string) {
  return title
    .toLowerCase()
    .replace(/ä/g, 'ae')
    .replace(/ö/g, 'oe')
    .replace(/ü/g, 'ue')
    .replace(/ß/g, 'ss')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
    .slice(0, 80);
}

export function pagePath(spaceKey: string, id: number, title?: string) {
  const s = title ? slug(title) : '';
  return `/wiki/spaces/${spaceKey}/pages/${id}${s ? `/${s}` : ''}`;
}

export function useSpace(key: string | undefined) {
  return useQuery({
    queryKey: ['space', key],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/spaces/{key}', { params: { path: { key: key! } } })),
    enabled: !!key,
  });
}

export function usePage(id: number | undefined) {
  return useQuery({
    queryKey: ['page', id],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/pages/{id}', { params: { path: { id: id! } } })),
    enabled: !!id,
  });
}

/**
 * "Show more" for a list that is loaded a page at a time: showMore loads the next page and then moves the keyboard
 * focus to the first item that came in (the element at that position of the list's children: its link, or else
 * its first control), so that keyboard and screen reader users go on reading there; the button itself may be gone.
 */
export function useShowMore<E extends HTMLElement>(
  count: number,
  load: () => Promise<boolean>,
  busy: boolean,
) {
  const listRef = useRef<E>(null);
  // The position of the first item being loaded, until it is shown.
  const focusAt = useRef<number | null>(null);
  useEffect(() => {
    const at = focusAt.current;
    if (at === null || count <= at) return;
    focusAt.current = null;
    const item = listRef.current?.children[at];
    (
      item?.querySelector<HTMLElement>('a[href]') ??
      item?.querySelector<HTMLElement>('button:not([disabled]), input:not([disabled])')
    )?.focus();
  }, [count]);
  const showMore = async () => {
    if (busy) return;
    focusAt.current = count;
    if (!(await load())) focusAt.current = null;
  };
  return { listRef, showMore };
}

/** How many child pages the page tree loads at a time: after imports, a page often has thousands of them. */
export const treePageSize = 100;

/** The child pages of a page in the tree, as far as they are loaded. */
export interface TreeLevel {
  items: PageRef[];
  /** The child pages the reader may see, loaded or not. */
  total: number;
  isLoading: boolean;
  hasMore: boolean;
  loadingMore: boolean;
  /** Loads the next child pages; false if that failed or brought no new ones. */
  loadMore: () => Promise<boolean>;
}

/** The child pages of a page (parent 0: the top level of the space), loaded a page at a time. */
export function useTree(spaceKey: string, parent: number, enabled = true): TreeLevel {
  const query = useInfiniteQuery({
    queryKey: ['page', 'tree', spaceKey, parent],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/wiki/spaces/{key}/tree', {
          params: { path: { key: spaceKey }, query: { parent, limit: treePageSize, offset: pageParam } },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (_last, pages) => nextOffset(pages),
    enabled,
  });
  const { data, fetchNextPage } = query;
  const items = useMemo(() => uniqueItems(data?.pages ?? [], (p) => p.id), [data]);
  return {
    items,
    total: data?.pages.at(-1)?.total ?? 0,
    isLoading: query.isLoading,
    hasMore: query.hasNextPage,
    loadingMore: query.isFetchingNextPage,
    loadMore: async () => {
      const next = await fetchNextPage();
      return !next.isError && uniqueItems(next.data?.pages ?? [], (p) => p.id).length > items.length;
    },
  };
}

/** Uploads files as attachments of a page (for images in the editor); files over maxMb are not sent. */
export function pageUpload(pageId: number, maxMb?: number): UploadFile {
  return async (file: File) => {
    const refused = tooLarge(file, maxMb);
    if (refused) throw refused;
    const form = new FormData();
    form.append('file', file);
    const [at] = await upload<Schemas['WikiAttachment'][]>(`/api/v1/wiki/pages/${pageId}/attachments`, form);
    return { id: at.id, url: at.url, contentType: at.contentType, filename: at.filename };
  };
}

export const attachmentUrl = (id: number) => `/api/v1/wiki/attachments/${id}`;

const wideKey = 'kogge.wiki.wide';

/** Whether pages use the full width, as the reader chose last (kept in the browser). */
export function useWidePages(): [boolean, () => void] {
  const [wide, setWide] = useState(() => {
    try {
      return localStorage.getItem(wideKey) === '1';
    } catch {
      return false;
    }
  });
  const toggle = () =>
    setWide((w) => {
      try {
        localStorage.setItem(wideKey, w ? '0' : '1');
      } catch {
        // storage unavailable — the choice lasts for this page only
      }
      return !w;
    });
  return [wide, toggle];
}
