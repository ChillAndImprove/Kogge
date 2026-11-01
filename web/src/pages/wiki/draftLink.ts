// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { Editor } from '@tiptap/core';
import type { EditorState, Transaction } from '@tiptap/pm/state';
import { Step, type StepMap } from '@tiptap/pm/transform';
import { getVersion, receiveTransaction, sendableSteps } from 'prosemirror-collab';
import { api, ApiError, unwrap } from '../../api/client';
import { caretsKey, type CaretsMeta } from '../../components/editor/carets';

/** Someone editing the draft (from their caret). */
export interface DraftPerson {
  client: string;
  userId: number;
  name: string;
  color: string;
}

export interface DraftUser {
  id: number;
  name: string;
}

/** The state of the connection: the editor's changes are being sent, are all saved, or wait for the network. */
export type LinkStatus = 'connecting' | 'saving' | 'saved' | 'offline';

export interface DraftLinkHandlers {
  status(status: LinkStatus): void;
  people(people: DraftPerson[]): void;
  title(title: string, user?: DraftUser): void;
  published(pageVersion: number, title: string, user?: DraftUser): void;
  discarded(user?: DraftUser): void;
  /** The editor has to load the draft again: its changes were refused, or it fell too far behind. */
  resync(code: string, message?: string): void;
  /** The user may no longer edit the page. */
  denied(): void;
}

interface StepsEvent {
  from: number;
  to: number;
  steps: { step: unknown; client: string }[];
}

interface CaretEvent {
  client: string;
  user?: DraftUser;
  version: number;
  anchor: number;
  head: number;
}

const palette = ['blue', 'teal', 'grape', 'orange', 'pink', 'cyan', 'lime', 'violet', 'red', 'indigo'];

/** A colour of the palette for a person, the same in every editor. */
export function personColor(userId: number): string {
  return palette[Math.abs(userId * 7919) % palette.length];
}

/** A new editor id: the user's id, a colon and a random part (the server checks the user's part). */
export function newClientId(userId: number): string {
  const bytes = crypto.getRandomValues(new Uint8Array(9));
  return `${userId}:${btoa(String.fromCharCode(...bytes)).replace(/[+/=]/g, (c) => (c === '+' ? '-' : c === '/' ? '_' : ''))}`;
}

/**
 * The connection of an editor to the shared draft of a page. It sends the editor's steps (ProseMirror's
 * collaborative editing: steps build on a version; refused ones are rebased over the others' and sent again),
 * applies the others' steps, and passes carets, titles and what happens to the draft. It talks over a WebSocket;
 * where none can be opened (a proxy that does not pass them), it asks the server over HTTP every few seconds.
 */
export class DraftLink {
  private socket: WebSocket | null = null;
  private mode: 'socket' | 'poll' = 'socket';
  private stopped = false;
  private welcomed = false;
  private failures = 0;
  private sending: { id: number; version: number } | null = null;
  private awaiting = 0; // a version the server acknowledged (or announced) whose steps have not arrived yet
  private awaitTimer: number | undefined;
  private nextId = 1;
  private pendingTitle: { id: number; title: string } | null = null;
  private retry: number | undefined;
  private pollTimer: number | undefined;
  private pinger: number | undefined;
  private caretTimer: number | undefined;
  private caretDirty = false;
  private lastCaret = '';
  private behindSince = 0;
  /** While polling: when to try a WebSocket again, how long it waited before, and the one being tried. */
  private socketRetry = { at: 0, delay: 0 };
  private probe: WebSocket | null = null;
  /** The maps of the latest confirmed steps, to bring carets from older versions to this one. */
  private maps: { version: number; map: StepMap }[] = [];
  private pending = new Map<string, CaretEvent>();
  private people = new Map<string, DraftPerson & { seen: number }>();
  private status: LinkStatus = 'connecting';

  private baseVersion: number;
  private title: string;

  constructor(
    private editor: Editor,
    private pageId: number,
    private client: string,
    draft: { baseVersion: number; title: string },
    private on: DraftLinkHandlers,
  ) {
    this.baseVersion = draft.baseVersion;
    this.title = draft.title;
  }

  private get state(): EditorState {
    return this.editor.state;
  }

  start() {
    this.editor.on('transaction', this.onTransaction);
    this.pinger = window.setInterval(() => {
      this.send({ t: 'ping' });
      // Others learn that this editor is still here; who was not heard of for long has left.
      this.lastCaret = '';
      this.caretDirty = true;
      this.flushCaret();
      for (const [client, p] of this.people) if (Date.now() - p.seen > 90_000) this.forget(client);
    }, 30_000);
    this.connect();
  }

  stop() {
    this.stopped = true;
    this.editor.off('transaction', this.onTransaction);
    window.clearInterval(this.pinger);
    window.clearTimeout(this.retry);
    window.clearTimeout(this.pollTimer);
    window.clearTimeout(this.caretTimer);
    window.clearTimeout(this.awaitTimer);
    this.socket?.close();
    this.socket = null;
    this.probe?.close();
    this.probe = null;
  }

  /** The draft's version this editor has (its own steps not counted until confirmed). */
  version(): number {
    return getVersion(this.state);
  }

  /** Whether all of this editor's changes are saved. */
  synced(): boolean {
    return !sendableSteps(this.state);
  }

  /** Waits until all changes are saved (or the time is up). */
  async whenSynced(timeout = 10_000): Promise<boolean> {
    const end = Date.now() + timeout;
    while (!this.synced() && Date.now() < end) {
      this.trySend();
      await new Promise((r) => window.setTimeout(r, 100));
    }
    return this.synced();
  }

  /** Sends the title (the last change of anyone wins); it is sent again after a reconnection until confirmed. */
  sendTitle(title: string) {
    this.title = title;
    if (this.mode === 'socket') {
      this.pendingTitle = { id: this.nextId++, title };
      this.send({ t: 'title', id: this.pendingTitle.id, title });
      return;
    }
    void unwrap(
      api.PUT('/api/v1/wiki/pages/{id}/edit/title', {
        params: { path: { id: this.pageId } },
        body: { title, clientId: this.client },
      }),
    ).catch(() => this.setStatus('offline'));
  }

  private setStatus(status: LinkStatus) {
    if (status === 'saved' && !this.synced()) status = 'saving';
    if (status !== this.status) {
      this.status = status;
      this.on.status(status);
    }
  }

  private socketUrl(): string {
    const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
    return `${scheme}://${window.location.host}/api/v1/wiki/pages/${this.pageId}/edit/socket?version=${this.version()}&client=${encodeURIComponent(this.client)}`;
  }

  private connect() {
    if (this.stopped) return;
    if (this.mode === 'poll') {
      void this.poll();
      return;
    }
    this.welcomed = false;
    this.attach(new WebSocket(this.socketUrl()));
  }

  /** Makes a socket the connection: its messages are received, and when it ends, the editor connects again. */
  private attach(socket: WebSocket) {
    this.socket = socket;
    socket.onmessage = (e) => {
      try {
        this.receive(JSON.parse(String(e.data)) as Record<string, unknown>);
      } catch (err) {
        console.error(err);
      }
    };
    socket.onclose = () => {
      if (this.socket !== socket) return;
      this.socket = null;
      this.sending = null;
      if (this.stopped) return;
      this.setStatus('offline');
      if (!this.welcomed && ++this.failures >= 3) {
        // No WebSocket gets through (a proxy may not pass them, or the network is gone for a while): ask over
        // HTTP, and try a WebSocket again now and then.
        this.mode = 'poll';
        this.socketRetry = { at: Date.now() + 30_000, delay: 30_000 };
        this.connect();
        return;
      }
      this.retry = window.setTimeout(
        () => this.connect(),
        Math.min(10_000, 500 * 2 ** Math.min(this.failures, 5)),
      );
    };
  }

  /** While polling: opens a WebSocket on the side, which takes over once the server welcomes it. */
  private trySocket() {
    const delay = Math.min(this.socketRetry.delay * 2, 10 * 60_000);
    this.socketRetry = { at: Date.now() + delay, delay };
    this.probe?.close();
    const socket = new WebSocket(this.socketUrl());
    this.probe = socket;
    socket.onmessage = (e) => {
      let m: Record<string, unknown> | null = null;
      try {
        m = JSON.parse(String(e.data)) as Record<string, unknown>;
      } catch {
        // not a message of the server
      }
      if (this.probe === socket) this.probe = null;
      if (this.stopped || this.mode !== 'poll' || m?.t !== 'welcome') {
        socket.close();
        return;
      }
      window.clearTimeout(this.pollTimer);
      this.mode = 'socket';
      this.failures = 0;
      this.attach(socket);
      this.receive(m);
    };
  }

  private send(m: Record<string, unknown>) {
    if (this.socket?.readyState === WebSocket.OPEN && this.welcomed) this.socket.send(JSON.stringify(m));
  }

  private reconnect() {
    this.socket?.close();
  }

  private receive(m: Record<string, unknown>) {
    switch (m.t) {
      case 'welcome':
        this.welcomed = true;
        this.failures = 0;
        this.sending = null;
        // The steps since this editor's version follow; its own are sent after them.
        this.await(Number(m.version) > this.version() ? Number(m.version) : 0);
        this.setStatus('saved');
        this.caretDirty = true;
        if (this.pendingTitle)
          this.send({ t: 'title', id: this.pendingTitle.id, title: this.pendingTitle.title });
        this.trySend();
        this.flushCaret();
        break;
      case 'steps':
        this.applySteps(m as unknown as StepsEvent);
        break;
      case 'ack':
        if (this.pendingTitle && m.id === this.pendingTitle.id) this.pendingTitle = null;
        if (this.sending && m.id === this.sending.id) {
          this.sending = null;
          this.await(Number(m.version));
          this.afterSteps();
        }
        break;
      case 'nack':
        if (this.pendingTitle && m.id === this.pendingTitle.id) this.pendingTitle = null;
        if (this.sending && m.id === this.sending.id) {
          const sentAt = this.sending.version;
          this.sending = null;
          if (m.code === 'draft.behind') {
            // The others' steps are on their way; ours are rebased over them and sent again. Those that came
            // before the refusal have been applied already: send again now, or nothing would.
            this.behindSince ||= Date.now();
            if (Date.now() - this.behindSince > 5000) this.reconnect();
            else if (this.version() > sentAt) this.trySend();
          } else {
            this.on.resync(String(m.code), m.message ? String(m.message) : undefined);
          }
        }
        break;
      case 'caret':
        this.caret(m as unknown as CaretEvent);
        break;
      case 'leave':
        this.forget(String(m.client));
        break;
      case 'title':
        this.title = String(m.title ?? '');
        this.on.title(this.title, m.user as DraftUser | undefined);
        break;
      case 'published':
        this.baseVersion = Number(m.pageVersion);
        this.title = String(m.title ?? this.title);
        this.on.published(this.baseVersion, this.title, m.user as DraftUser | undefined);
        break;
      case 'discarded':
        this.on.discarded(m.user as DraftUser | undefined);
        break;
      case 'reset':
        this.on.resync('draft.reset');
        break;
      case 'bye':
        if (m.code === 'denied') {
          this.stopped = true;
          this.on.denied();
        }
        break;
      default:
    }
  }

  /** Waits for the steps up to a version before sending more; without them in time, it reconnects to get them. */
  private await(version: number) {
    window.clearTimeout(this.awaitTimer);
    this.awaiting = version > this.version() ? version : 0;
    if (this.awaiting) {
      this.awaitTimer = window.setTimeout(() => {
        if (this.awaiting && this.awaiting > this.version()) this.reconnect();
      }, 5000);
    }
  }

  /** Applies steps of the server: runs of one editor at a time, so that this editor recognises its own. */
  private applySteps(e: StepsEvent) {
    if (this.stopped || this.editor.isDestroyed) return;
    const version = this.version();
    if (e.to <= version) return;
    if (e.from > version) {
      // Steps are missing: the server sends them after reconnecting.
      this.reconnect();
      return;
    }
    const list = e.steps.slice(version - e.from);
    let i = 0;
    while (i < list.length) {
      let j = i;
      while (j < list.length && list[j].client === list[i].client) j++;
      const run = list.slice(i, j);
      const before = this.version();
      let tr: Transaction;
      let maps: StepMap[];
      try {
        const steps = run.map((s) => Step.fromJSON(this.state.schema, s.step as never));
        // The maps of the confirmed steps, as they changed the confirmed document.
        maps =
          run[0].client === this.client
            ? (sendableSteps(this.state)?.steps ?? []).slice(0, run.length).map((s) => s.getMap())
            : steps.map((s) => s.getMap());
        tr = receiveTransaction(
          this.state,
          steps,
          run.map((s) => s.client),
          { mapSelectionBackward: true },
        );
      } catch (err) {
        console.error(err);
        this.on.resync('draft.apply');
        return;
      }
      // Changes of others are not typing of this editor: no autolinks, trailing paragraphs or input rules.
      tr.setMeta('preventAutolink', true)
        .setMeta('skipTrailingNode', true)
        .setMeta('preventClearDocument', true);
      this.editor.view.dispatch(tr);
      maps.forEach((map, k) => this.maps.push({ version: before + k + 1, map }));
      i = j;
    }
    if (this.maps.length > 2000) this.maps.splice(0, this.maps.length - 1000);
    this.behindSince = 0;
    this.afterSteps();
  }

  private afterSteps() {
    // Carets of versions this editor had not reached yet.
    for (const c of [...this.pending.values()]) if (c.version <= this.version()) this.caret(c);
    if (this.awaiting <= this.version()) this.await(0);
    this.trySend();
    this.flushCaret();
    this.setStatus(this.synced() ? 'saved' : 'saving');
  }

  private trySend() {
    if (this.sending || this.awaiting || this.stopped) return;
    const sendable = sendableSteps(this.state);
    if (!sendable) {
      this.setStatus(this.welcomed || this.mode === 'poll' ? 'saved' : this.status);
      return;
    }
    this.setStatus('saving');
    const steps = sendable.steps.slice(0, 200).map((s) => s.toJSON());
    if (this.mode === 'socket') {
      if (!this.welcomed) return;
      this.sending = { id: this.nextId++, version: sendable.version };
      this.send({ t: 'steps', id: this.sending.id, version: sendable.version, steps });
      return;
    }
    this.sending = { id: this.nextId++, version: sendable.version };
    unwrap(
      api.POST('/api/v1/wiki/pages/{id}/edit/steps', {
        params: { path: { id: this.pageId } },
        body: { version: sendable.version, clientId: this.client, steps: steps as never },
      }),
    ).then(
      () => {
        this.sending = null;
        // Over a WebSocket that took over meanwhile, the steps come back as the others' do.
        if (this.mode === 'poll') void this.poll(true);
      },
      (err: unknown) => {
        this.sending = null;
        if (err instanceof ApiError && err.status === 422) this.on.resync(err.code, err.message);
        else if (this.mode !== 'poll') this.trySend();
        else if (err instanceof ApiError && err.code === 'draft.behind') void this.poll(true);
        else this.setStatus('offline');
      },
    );
  }

  /** HTTP: asks for the steps since this editor's version, and the others' carets and title. */
  private async poll(now = false) {
    window.clearTimeout(this.pollTimer);
    if (this.stopped || this.mode !== 'poll') return;
    try {
      const res = await unwrap(
        api.GET('/api/v1/wiki/pages/{id}/edit/steps', {
          params: { path: { id: this.pageId }, query: { since: this.version() } },
        }),
      );
      this.welcomed = true;
      if (res.baseVersion !== this.baseVersion) {
        this.baseVersion = res.baseVersion;
        this.on.published(res.baseVersion, res.title);
      }
      if (res.title !== this.title && !this.pendingTitle) {
        this.title = res.title;
        this.on.title(res.title);
      }
      if (res.steps.length) {
        const from = res.steps[0].version - 1;
        this.applySteps({
          from,
          to: res.version,
          steps: res.steps.map((s) => ({ step: s.step, client: s.clientId })),
        });
      } else {
        this.afterSteps();
      }
      const seen = new Set<string>();
      for (const c of res.carets) {
        if (c.clientId === this.client) continue;
        seen.add(c.clientId);
        this.caret({
          client: c.clientId,
          user: { id: c.userId, name: c.name },
          version: c.version,
          anchor: c.anchor,
          head: c.head,
        });
      }
      for (const client of [...this.people.keys()]) if (!seen.has(client)) this.forget(client);
      if (this.pendingTitle) {
        const title = this.pendingTitle.title;
        this.pendingTitle = null;
        this.sendTitle(title);
      }
      if (Date.now() >= this.socketRetry.at) this.trySocket();
    } catch (err) {
      if (
        err instanceof ApiError &&
        (err.code === 'draft.reset' || err.code === 'draft.ahead' || err.code === 'draft.gone')
      ) {
        this.on.resync(err.code);
        return;
      }
      if (err instanceof ApiError && err.status === 403) {
        this.stopped = true;
        this.on.denied();
        return;
      }
      this.setStatus('offline');
    }
    this.pollTimer = window.setTimeout(() => void this.poll(), now ? 500 : 2000);
  }

  private onTransaction = ({ transaction }: { transaction: Transaction }) => {
    if (transaction.getMeta('addToHistory') === false && transaction.getMeta('rebased') !== undefined) return;
    if (transaction.docChanged) {
      this.setStatus('saving');
      queueMicrotask(() => this.trySend());
    }
    if (transaction.docChanged || transaction.selectionSet) {
      this.caretDirty = true;
      window.clearTimeout(this.caretTimer);
      this.caretTimer = window.setTimeout(() => this.flushCaret(), 120);
    }
  };

  /** Sends this editor's caret, at a version the others can place it at (when all its steps are confirmed). */
  private flushCaret() {
    if (!this.caretDirty || !this.synced() || !this.welcomed || this.editor.isDestroyed) return;
    const { anchor, head } = this.state.selection;
    const key = `${this.version()}:${anchor}:${head}`;
    if (key === this.lastCaret) return;
    this.caretDirty = false;
    this.lastCaret = key;
    const caret = { version: this.version(), anchor, head };
    if (this.mode === 'socket') {
      this.send({ t: 'caret', ...caret });
      return;
    }
    void unwrap(
      api.POST('/api/v1/wiki/pages/{id}/edit/caret', {
        params: { path: { id: this.pageId } },
        body: { clientId: this.client, ...caret },
      }),
    ).catch(() => undefined);
  }

  /** Places another editor's caret: from its version to this editor's document. */
  private caret(c: CaretEvent) {
    if (c.client === this.client || !c.user || this.stopped || this.editor.isDestroyed) return;
    const version = this.version();
    if (c.version > version) {
      this.pending.set(c.client, c);
      return;
    }
    this.pending.delete(c.client);
    let anchor = c.anchor;
    let head = c.head;
    if (c.version < version) {
      const from = this.maps.findIndex((m) => m.version === c.version + 1);
      if (from < 0) return; // too old to place
      for (const m of this.maps.slice(from)) {
        anchor = m.map.map(anchor);
        head = m.map.map(head);
      }
    }
    // Through this editor's steps that are not confirmed yet.
    for (const s of sendableSteps(this.state)?.steps ?? []) {
      anchor = s.getMap().map(anchor);
      head = s.getMap().map(head);
    }
    const person = {
      client: c.client,
      userId: c.user.id,
      name: c.user.name,
      color: personColor(c.user.id),
      seen: Date.now(),
    };
    const meta: CaretsMeta = {
      set: { client: c.client, name: person.name, color: person.color, anchor, head },
    };
    this.editor.view.dispatch(this.state.tr.setMeta(caretsKey, meta).setMeta('addToHistory', false));
    const known = this.people.has(c.client);
    this.people.set(c.client, person);
    if (!known) this.on.people(this.list());
  }

  private list(): DraftPerson[] {
    return [...this.people.values()].map(({ client, userId, name, color }) => ({
      client,
      userId,
      name,
      color,
    }));
  }

  private forget(client: string) {
    this.pending.delete(client);
    if (!this.people.delete(client) || this.editor.isDestroyed) return;
    const meta: CaretsMeta = { remove: client };
    this.editor.view.dispatch(this.state.tr.setMeta(caretsKey, meta).setMeta('addToHistory', false));
    this.on.people(this.list());
  }
}
