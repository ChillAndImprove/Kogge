// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { afterEach, describe, expect, it, vi } from 'vitest';
import { backgroundHeader, idle, idleAfter, noteActivity } from './activity';
import { request } from './client';

describe('activity', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    noteActivity();
  });

  it('counts the user as idle only after a while without input', () => {
    noteActivity(1_000);
    expect(idle(1_000 + idleAfter - 1)).toBe(false);
    expect(idle(1_000 + idleAfter + 1)).toBe(true);
    noteActivity(1_000 + idleAfter + 2);
    expect(idle(1_000 + idleAfter + 3)).toBe(false);
  });

  it('marks the requests made while the user is idle, and input ends the idleness', async () => {
    const sent: (string | null)[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (_path, init) => {
      sent.push(new Headers(init?.headers).get(backgroundHeader));
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    await request('GET', '/api/v1/notifications');
    noteActivity(Date.now() - idleAfter - 1000);
    await request('GET', '/api/v1/notifications');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a' }));
    await request('GET', '/api/v1/notifications');
    expect(sent).toEqual([null, '1', null]);
  });
});
