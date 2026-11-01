// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { fallback, type Answers, type Recorded } from './fallback';

/**
 * The preview: the web application without a server, for GitHub Pages (make showcase). It answers the API from
 * answers recorded on a real Kogge with sample content; whatever would change something, run something or need a
 * server (downloads, signing out) opens a dialog that explains how to deploy Kogge instead.
 */
export const isPreview = import.meta.env.MODE === 'showcase';

interface Recording {
  recordedAt: string;
  responses: Answers;
}

/** What someone tried that needs a running Kogge. */
export type PreviewAction =
  'commit' | 'pullRequest' | 'build' | 'issue' | 'page' | 'repository' | 'download' | 'signOut' | 'change';

const blockedEvent = 'kogge-preview-blocked';

/** Calls fn whenever someone tries something the preview cannot do. */
export function onPreviewBlocked(fn: (action: PreviewAction) => void): () => void {
  const listener = (e: Event) => fn((e as CustomEvent<PreviewAction>).detail);
  window.addEventListener(blockedEvent, listener);
  return () => window.removeEventListener(blockedEvent, listener);
}

function blocked(action: PreviewAction) {
  window.dispatchEvent(new CustomEvent<PreviewAction>(blockedEvent, { detail: action }));
}

/** The code of the API errors the preview answers changes with; the dialog explains them, not the page. */
export const previewErrorCode = 'preview.deploy';

/** What a change of the API is about, for the dialog's words. */
export function actionOf(path: string): PreviewAction {
  if (path === '/api/v1/auth/logout') return 'signOut';
  // Exports of pages and spaces are files to download.
  if (/\/exports(\/|$)/.test(path)) return 'download';
  if (/\/files$/.test(path) || /\/branches|\/tags/.test(path)) return 'commit';
  if (path.includes('/pull-requests')) return 'pullRequest';
  if (path.startsWith('/api/v1/builds') || /\/runs|\/pipelines|\/environments/.test(path)) return 'build';
  if (path.startsWith('/api/v1/code/projects')) return 'repository';
  if (path.startsWith('/api/v1/wiki')) return 'page';
  if (
    path.startsWith('/api/v1/issues') ||
    path.startsWith('/api/v1/sprints') ||
    path.startsWith('/api/v1/boards')
  ) {
    return 'issue';
  }
  return 'change';
}

/**
 * What pages note on their own — who has a page open, what was opened last — answered as the server answers when
 * nobody else is there, without a dialog: the visitor did not ask for it.
 */
export function noteAnswer(method: string, path: string): Recorded | null {
  if (method !== 'POST') return null;
  if (/^\/api\/v1\/wiki\/pages\/\d+\/presence$/.test(path))
    return { status: 200, type: 'application/json', body: '[]' };
  if (path === '/api/v1/me/recent') return { status: 204, type: 'application/json', body: '' };
  return null;
}

/** Reading these starts a change on the server: editing a page opens its shared draft. */
export function startsChange(method: string, path: string): boolean {
  return method === 'GET' && /^\/api\/v1\/wiki\/pages\/\d+\/edit$/.test(path);
}

/**
 * The address of a page of the application as a link outside the router can use (a new tab): the preview's
 * addresses are after the #, as it runs from static files.
 */
export function appHref(path: string): string {
  return isPreview ? `#${path}` : path;
}

function problem(status: number, code: string, detail: string): Response {
  return new Response(JSON.stringify({ status, code, title: 'Preview', detail }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function answer(r: Recorded): Response {
  return new Response(r.status === 204 ? null : r.body, {
    status: r.status,
    headers: { 'Content-Type': r.type },
  });
}

const serverPaths = /^\/(api|auth|scm)\//;

/**
 * Requests that read with POST; the recording has their answers by the request's body: highlighting code,
 * rendering Markdown, and what a bulk change or a move of issues would do.
 */
const readingPosts = [
  '/api/v1/code/highlight',
  '/api/v1/code/markdown',
  '/api/v1/issues/bulk/check',
  '/api/v1/issues/move-plan',
];

/** Of the reading POSTs, those that prepare a change: without an answer they explain, as changes do. */
const preparingPosts = ['/api/v1/issues/bulk/check', '/api/v1/issues/move-plan'];

const missing = () =>
  problem(404, 'preview.missing', 'The preview has no sample content for this; a Kogge of your own has.');

/** Loads the recording and answers the API from it from now on. */
export async function installPreview(): Promise<void> {
  const realFetch = window.fetch.bind(window);
  // Only what the visitor does opens the dialog; pages that note something on their own (that a notification was
  // read, a page was visited) are refused silently.
  let lastAction = 0;
  for (const type of ['pointerdown', 'keydown', 'submit']) {
    window.addEventListener(type, () => (lastAction = Date.now()), true);
  }
  const recording = (await (
    await realFetch(new URL('recording.json', document.baseURI))
  ).json()) as Recording;
  const answers = recording.responses;
  const refuse = (method: string, path: string) => {
    console.debug(`preview: refused ${method} ${path}`);
    if (Date.now() - lastAction < 3000) blocked(actionOf(path));
    return problem(
      403,
      previewErrorCode,
      'This is a preview with sample content: deploy Kogge on your own server to change things.',
    );
  };

  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const request = new Request(input, init);
    const url = new URL(request.url);
    if (url.origin !== window.location.origin || !serverPaths.test(url.pathname))
      return realFetch(input, init);
    const note = noteAnswer(request.method, url.pathname);
    if (note) return answer(note);
    if (request.method === 'POST' && readingPosts.includes(url.pathname)) {
      const body = await request.clone().text();
      const found = answers[`POST ${url.pathname} ${body}`] ?? fallback(answers, 'POST', url, body);
      if (found) return answer(found);
      return preparingPosts.includes(url.pathname) ? refuse(request.method, url.pathname) : missing();
    }
    if (
      (request.method !== 'GET' && request.method !== 'HEAD') ||
      startsChange(request.method, url.pathname)
    ) {
      return refuse(request.method, url.pathname);
    }
    const found = answers[url.pathname + url.search] ?? fallback(answers, 'GET', url);
    if (!found) {
      console.debug(`preview: no sample content for ${url.pathname}${url.search}`);
      return missing();
    }
    return answer(found);
  };

  // Live messages never come; build logs are replayed from the recording.
  class PreviewEventSource extends EventTarget {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    readonly CONNECTING = 0;
    readonly OPEN = 1;
    readonly CLOSED = 2;
    readonly url: string;
    readonly withCredentials = false;
    readyState = 0;
    onopen: ((e: Event) => void) | null = null;
    onmessage: ((e: MessageEvent) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;

    constructor(url: string | URL) {
      super();
      const u = new URL(url, window.location.href);
      this.url = u.href;
      const recorded = answers[u.pathname + u.search];
      if (!recorded || u.pathname === '/api/v1/live') return;
      setTimeout(() => {
        this.readyState = 1;
        this.emit(new Event('open'));
        for (const block of recorded.body.split(/\r?\n\r?\n/)) {
          let type = 'message';
          const data: string[] = [];
          for (const line of block.split(/\r?\n/)) {
            if (line.startsWith('event:')) type = line.slice(6).trim();
            else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''));
          }
          if (data.length > 0) this.emit(new MessageEvent(type, { data: data.join('\n') }));
        }
        this.readyState = 2;
        this.emit(new Event('error'));
      }, 50);
    }

    private emit(e: Event) {
      this.dispatchEvent(e);
      const handler = { open: this.onopen, message: this.onmessage, error: this.onerror }[e.type];
      (handler as ((e: Event) => void) | null | undefined)?.call(this, e);
    }

    close() {
      this.readyState = 2;
    }
  }
  window.EventSource = PreviewEventSource as unknown as typeof EventSource;

  // Editing together needs a server.
  class PreviewWebSocket extends EventTarget {
    readyState = 3;
    constructor() {
      super();
      setTimeout(() => this.dispatchEvent(new CloseEvent('close', { code: 1000 })), 0);
    }
    send() {}
    close() {}
  }
  window.WebSocket = PreviewWebSocket as unknown as typeof WebSocket;

  // Links outside the router: downloads and other addresses of the server explain; in-page anchors (#section) and
  // addresses of the application (/browse/…) in documents work as they do with a server, after the #.
  document.addEventListener(
    'click',
    (e) => {
      const a = (e.target as Element | null)?.closest?.('a[href]');
      const href = a?.getAttribute('href');
      if (!href) return;
      if (serverPaths.test(href)) {
        e.preventDefault();
        blocked('download');
        return;
      }
      if (e.defaultPrevented || e.button !== 0) return;
      if (href.startsWith('#') && !href.startsWith('#/')) {
        e.preventDefault();
        document.getElementById(decodeURIComponent(href.slice(1)))?.scrollIntoView();
      } else if (/^\/(?!\/)/.test(href)) {
        e.preventDefault();
        if (e.ctrlKey || e.metaKey || e.shiftKey || a?.getAttribute('target') === '_blank')
          window.open(appHref(href), '_blank', 'noopener');
        else window.location.hash = href;
      }
    },
    true,
  );
}

/** The address of the repository the preview belongs to (for "how to deploy"), if known. */
export function repositoryUrl(): string | null {
  const configured = import.meta.env.VITE_KOGGE_REPOSITORY_URL as string | undefined;
  if (configured) return configured;
  const { hostname, pathname } = window.location;
  const owner = /^([^.]+)\.github\.io$/.exec(hostname)?.[1];
  const repo = pathname.split('/')[1];
  return owner && repo ? `https://github.com/${owner}/${repo}` : null;
}
