// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';

/** How long messages are collected before the queries they affect are refetched: a bulk change of hundreds of
 * issues sends a message for each, and the lists showing them load once, not hundreds of times. */
const coalesceMs = 300;

/** Subscribes to live messages and invalidates the queries they affect. Messages carry no content. */
export function useLiveUpdates(enabled: boolean) {
  const client = useQueryClient();
  useEffect(() => {
    if (!enabled) return;
    const source = new EventSource('/api/v1/live');
    const pending = new Set<string>();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const invalidate = (type: string) => {
      pending.add(type);
      timer ??= setTimeout(() => {
        timer = undefined;
        for (const t of pending) void client.invalidateQueries({ queryKey: [t] });
        pending.clear();
      }, coalesceMs);
    };
    const onNotification = () => invalidate('notifications');
    // Boards show issues and sprints, so their changes refresh boards too.
    const related: Record<string, string[]> = {
      issue: ['board'],
      sprint: ['board'],
      repository: ['pull-request'],
      run: ['pull-request'],
    };
    // Activity streams gather the changes of all products: they refresh once for a burst of changes, a few
    // seconds after the first.
    const streams = new Set(['issue', 'page', 'pull-request', 'run', 'repository']);
    let streamTimer: ReturnType<typeof setTimeout> | undefined;
    const refreshStreams = () => {
      if (streamTimer !== undefined) return;
      streamTimer = setTimeout(() => {
        streamTimer = undefined;
        void client.invalidateQueries({ queryKey: ['activity'] });
      }, 3000);
    };
    const onEntity = (e: MessageEvent<string>) => {
      try {
        const msg = JSON.parse(e.data) as { type: string; id?: string };
        for (const type of [msg.type, ...(related[msg.type] ?? [])]) invalidate(type);
        if (streams.has(msg.type)) refreshStreams();
      } catch {
        // ignore malformed messages
      }
    };
    source.addEventListener('notification', onNotification);
    for (const type of [
      'issue',
      'board',
      'sprint',
      'page',
      'space',
      'pull-request',
      'run',
      'repository',
      'filter-subscriptions',
      'bulk-change',
      'wiki-export',
    ]) {
      source.addEventListener(type, onEntity as EventListener);
    }
    return () => {
      if (timer) clearTimeout(timer);
      clearTimeout(streamTimer);
      source.close();
    };
  }, [enabled, client]);
}
