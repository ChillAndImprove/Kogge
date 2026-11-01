// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import createClient, { type Middleware } from 'openapi-fetch';
import i18n from '../i18n';
import { backgroundHeader, idle } from './activity';
import type { components, paths } from './schema';

export type Schemas = components['schemas'];

let csrfToken = '';

/** Remembers the session's CSRF token, sent with every state-changing request. */
export function setCsrfToken(token: string | undefined) {
  csrfToken = token ?? '';
}

export interface FieldError {
  location?: string;
  message: string;
}

/** An RFC 9457 problem returned by the API. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly errors: FieldError[];

  constructor(status: number, code: string, detail: string, errors: FieldError[] = []) {
    super(detail || code || `HTTP ${status}`);
    this.status = status;
    this.code = code;
    this.errors = errors;
  }
}

const csrf: Middleware = {
  onRequest({ request }) {
    if (!['GET', 'HEAD', 'OPTIONS'].includes(request.method) && csrfToken) {
      request.headers.set('X-CSRF-Token', csrfToken);
    }
    return request;
  },
};

/** The server words its errors in the language of the interface. */
const language: Middleware = {
  onRequest({ request }) {
    request.headers.set('Accept-Language', i18n.language || 'en');
    return request;
  },
};

/** Requests made while the user is idle do not keep the session alive (see activity.ts). */
const background: Middleware = {
  onRequest({ request }) {
    if (idle()) request.headers.set(backgroundHeader, '1');
    return request;
  },
};

// fetch is looked up on every request, so that the preview can answer them (showcase/preview.ts).
export const api = createClient<paths>({
  baseUrl: '',
  credentials: 'same-origin',
  fetch: (request) => globalThis.fetch(request),
});
api.use(csrf, language, background);

function toApiError(response: Response, error: unknown): ApiError {
  if (error && typeof error === 'object') {
    const e = error as { code?: string; detail?: string; title?: string; errors?: FieldError[] };
    return new ApiError(response.status, e.code ?? '', e.detail ?? e.title ?? '', e.errors ?? []);
  }
  return new ApiError(response.status, '', response.statusText);
}

/** Awaits an openapi-fetch call and returns its data or throws an ApiError. */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  const { data, error, response } = await p;
  if (error !== undefined || !response.ok) {
    throw toApiError(response, error);
  }
  return data as T;
}

/** Plain JSON request for endpoints outside the OpenAPI document. */
export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Accept-Language': i18n.language || 'en' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken;
  if (idle()) headers[backgroundHeader] = '1';
  const response = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!response.ok) throw toApiError(response, data);
  return data as T;
}

/** Uploads multipart form data, e.g. attachments; returns the parsed JSON response. */
export async function upload<T>(path: string, form: FormData): Promise<T> {
  // Refusals (too large, no space on the server) are worded in the reader's language.
  const headers: Record<string, string> = { 'Accept-Language': i18n.language || 'en' };
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
  const response = await fetch(path, {
    method: 'POST',
    headers,
    body: form,
    credentials: 'same-origin',
  });
  const text = await response.text();
  if (!response.ok) {
    // A proxy in front of Kogge may refuse a large upload with a page of its own.
    let problem: unknown;
    try {
      problem = text ? JSON.parse(text) : undefined;
    } catch {
      problem = undefined;
    }
    throw toApiError(response, problem);
  }
  return (text ? JSON.parse(text) : undefined) as T;
}
