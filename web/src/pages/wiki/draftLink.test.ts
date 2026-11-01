// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Editor } from '@tiptap/core';
import { getVersion } from 'prosemirror-collab';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { collaboration } from '../../components/editor/collab';
import { editorExtensions } from '../../components/editor/extensions';
import { DraftLink, type LinkStatus } from './draftLink';

type Message = Record<string, unknown>;

// Polling asks the API for the steps since a version; the tests answer with none.
const polls = vi.hoisted(() => [] as string[]);
vi.mock('../../api/client', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/client')>();
  return {
    ...real,
    api: {
      ...real.api,
      GET: async (path: string) => {
        polls.push(path);
        const data = { baseVersion: 1, title: 'Notes', version: 0, steps: [], carets: [] };
        return { data, response: new Response(null, { status: 200 }) };
      },
    },
  };
});

/** A WebSocket that keeps what the editor sends and delivers what the test lets the server say. */
class FakeSocket {
  static OPEN = 1;
  static last: FakeSocket | null = null;
  static opened = 0;
  readyState = 1;
  sent: Message[] = [];
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor(public url: string) {
    FakeSocket.last = this;
    FakeSocket.opened++;
  }
  send(data: string) {
    this.sent.push(JSON.parse(data) as Message);
  }
  close() {
    this.readyState = 3;
    this.onclose?.();
  }
  deliver(m: Message) {
    this.onmessage?.({ data: JSON.stringify(m) });
  }
  steps() {
    return this.sent.filter((m) => m.t === 'steps');
  }
}

const cleanups: (() => void)[] = [];
afterEach(() => cleanups.splice(0).forEach((f) => f()));

/** An editor connected to a draft at version 0 through a fake socket (welcomed by the server, unless not). */
function connected(welcome = true) {
  const realSocket = globalThis.WebSocket;
  (globalThis as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
  const editor = new Editor({
    extensions: [...editorExtensions('', { collaborative: true, wiki: true }), ...collaboration(0, '1:me')],
    content: { type: 'doc', content: [{ type: 'paragraph' }] },
  });
  const statuses: LinkStatus[] = [];
  const link = new DraftLink(
    editor,
    42,
    '1:me',
    { baseVersion: 1, title: 'Notes' },
    {
      status: (s) => statuses.push(s),
      people: () => undefined,
      title: () => undefined,
      published: () => undefined,
      discarded: () => undefined,
      resync: () => undefined,
      denied: () => undefined,
    },
  );
  link.start();
  const socket = FakeSocket.last!;
  if (welcome) socket.deliver({ t: 'welcome', version: 0 });
  cleanups.push(() => {
    link.stop();
    editor.destroy();
    globalThis.WebSocket = realSocket;
  });
  return { editor, link, socket, statuses };
}

// Another editor's step, which the server took before this editor's.
const theirs = { stepType: 'replace', from: 1, to: 1, slice: { content: [{ type: 'text', text: 'B' }] } };

/** Lets the editor send what it queued after a change. */
const sent = () => new Promise((r) => setTimeout(r, 0));

describe('DraftLink', () => {
  it('sends its steps again when the steps it missed came before the refusal', async () => {
    const { editor, link, socket, statuses } = connected();
    editor.commands.insertContent('A');
    await sent();
    expect(socket.steps()).toHaveLength(1);

    socket.deliver({ t: 'steps', from: 0, to: 1, steps: [{ step: theirs, client: '2:other' }] });
    socket.deliver({ t: 'nack', id: socket.steps()[0].id, code: 'draft.behind', message: 'behind' });

    // Rebased over the other editor's step, the change is sent again at once, building on version 1.
    expect(socket.steps()).toHaveLength(2);
    const resent = socket.steps()[1];
    expect(resent.version).toBe(1);
    socket.deliver({ t: 'ack', id: resent.id, version: 2 });
    socket.deliver({
      t: 'steps',
      from: 1,
      to: 2,
      steps: (resent.steps as unknown[]).map((step) => ({ step, client: '1:me' })),
    });
    expect(getVersion(editor.state)).toBe(2);
    expect(link.synced()).toBe(true);
    expect(statuses.at(-1)).toBe('saved');
    // Both changes are kept, the other editor's first: the server took it first.
    expect(editor.getText()).toBe('BA');
  });

  it('waits for the steps it missed when the refusal comes first', async () => {
    const { editor, socket } = connected();
    editor.commands.insertContent('A');
    await sent();
    socket.deliver({ t: 'nack', id: socket.steps()[0].id, code: 'draft.behind', message: 'behind' });
    // Nothing to rebase over yet: sending again now would only be refused again.
    expect(socket.steps()).toHaveLength(1);

    socket.deliver({ t: 'steps', from: 0, to: 1, steps: [{ step: theirs, client: '2:other' }] });
    expect(socket.steps()).toHaveLength(2);
    expect(socket.steps()[1].version).toBe(1);
  });

  it('asks over HTTP when no WebSocket gets through, and takes a WebSocket again once one does', async () => {
    vi.useFakeTimers();
    polls.length = 0;
    cleanups.push(() => vi.useRealTimers());
    const { editor } = connected(false);
    // The network is gone: three WebSockets end before the server welcomes them.
    for (let i = 0; i < 3; i++) {
      FakeSocket.last!.close();
      await vi.advanceTimersByTimeAsync(10_000);
    }
    expect(polls.length).toBeGreaterThan(0);
    expect(polls.every((path) => path === '/api/v1/wiki/pages/{id}/edit/steps')).toBe(true);

    // The network is back: a WebSocket tried on the side takes over, and the polling stops.
    const opened = FakeSocket.opened;
    await vi.advanceTimersByTimeAsync(35_000);
    expect(FakeSocket.opened).toBe(opened + 1);
    FakeSocket.last!.deliver({ t: 'welcome', version: 0 });
    const polled = polls.length;
    await vi.advanceTimersByTimeAsync(20_000);
    expect(polls.length).toBe(polled);
    editor.commands.insertContent('A');
    await vi.advanceTimersByTimeAsync(0);
    expect(FakeSocket.last!.steps()).toHaveLength(1);
  });

  it('keeps asking over HTTP, and tries WebSockets less often, while none gets through', async () => {
    vi.useFakeTimers();
    cleanups.push(() => vi.useRealTimers());
    connected(false);
    for (let i = 0; i < 3; i++) {
      FakeSocket.last!.close();
      await vi.advanceTimersByTimeAsync(10_000);
    }
    const opened = FakeSocket.opened;
    // A proxy refuses WebSockets: those tried on the side end at once, after 30 s, then 60 s, then 120 s.
    for (const wait of [35_000, 65_000, 125_000]) {
      await vi.advanceTimersByTimeAsync(wait);
      FakeSocket.last!.close();
    }
    expect(FakeSocket.opened).toBe(opened + 3);
  });
});
