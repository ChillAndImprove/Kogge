// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/**
 * What the preview (preview.ts) answers that its recording cannot hold: what visitors type into searches and which
 * issues they choose for a bulk change or a move. Searches are answered from the recorded search of the longest
 * beginning of the text, narrowed to what matches all of it as the server matches it — words that begin words of
 * titles and keys for the quick search and the search page, values that contain the text for suggestions and
 * pickers — or, where nothing recorded begins the text, with nothing found. Bulk changes and moves are answered from
 * the recorded check of the fewest issues that include all the chosen ones, narrowed to them. Activity streams end
 * with the entries recorded, and show of one product what the stream of all products has of it.
 */

export interface Recorded {
  status: number;
  type: string;
  body: string;
}

export type Answers = Record<string, Recorded>;

type Json = Record<string, unknown>;

const isObject = (x: unknown): x is Json => typeof x === 'object' && x !== null && !Array.isArray(x);

/** Lower case, trimmed, runs of white space as one space: how texts of searches compare. */
export function normal(text: string): string {
  return text.trim().replace(/\s+/g, ' ').toLowerCase();
}

/** The words of a text as the server's search sees them: letters and digits, in lower case. */
export function wordsOf(text: string): string[] {
  return text
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter(Boolean);
}

// --- searches ---

interface Hit {
  kind: string;
  key?: string;
  title: string;
  excerpt?: string;
}

/** The beginning of an issue key ("SHOP-1"): issues are then found by their keys. */
const keyBeginning = /^[a-z][a-z0-9_]*-\d*$/i;

/** Whether a hit of the quick search or the search page matches the text. */
export function hitMatches(hit: Hit, text: string): boolean {
  const t = normal(text);
  const key = (hit.key ?? '').toLowerCase();
  if (hit.kind === 'issue' && keyBeginning.test(t)) return key.startsWith(t);
  // Words of one letter begin too many words to narrow anything down (as on the server), unless all are so short.
  const words = wordsOf(t).filter((w) => w.length > 1);
  if (words.length === 0) return key.startsWith(t) || normal(hit.title).startsWith(t);
  // The marks of matches in excerpts (\u0001 … \u0002) are no part of the words.
  const excerpt = (hit.excerpt ?? '').replaceAll('\u0001', '').replaceAll('\u0002', '');
  const own = wordsOf(`${hit.key ?? ''} ${hit.title} ${excerpt}`);
  return words.every((w) => own.some((o) => o.startsWith(w)));
}

/** The answer of the quick search or the search page for a beginning of the text, narrowed to the text. */
export function narrowHits(answer: unknown, text: string): unknown {
  if (!isObject(answer) || !Array.isArray(answer.groups)) return answer;
  const groups = (answer.groups as Json[])
    .map((g) => {
      const hits = (g.hits as Hit[]).filter((h) => hitMatches(h, text));
      // The search page lists every kind with its total; the quick search only the products that found something.
      return 'total' in g ? { ...g, hits, total: hits.length } : { ...g, hits };
    })
    .filter((g) => 'total' in g || g.hits.length > 0);
  return { ...answer, groups, incomplete: [] };
}

/** Whether one of the texts of a value of a list (not of the objects or lists in it) contains the text. */
function containsText(value: unknown, text: string): boolean {
  if (typeof value === 'string') return value.toLowerCase().includes(text);
  if (!isObject(value)) return false;
  return Object.values(value).some((v) => typeof v === 'string' && v.toLowerCase().includes(text));
}

/** A list (or a page of one) for a beginning of the text, narrowed to the values that contain the text. */
export function narrowList(answer: unknown, text: string): unknown {
  const t = normal(text);
  if (Array.isArray(answer)) return answer.filter((v) => containsText(v, t));
  if (isObject(answer) && Array.isArray(answer.items)) {
    const items = answer.items.filter((v) => containsText(v, t));
    return 'total' in answer ? { ...answer, items, total: items.length } : { ...answer, items };
  }
  return answer;
}

interface Search {
  /** The parameter that holds the text. */
  param: string;
  narrow: (answer: unknown, text: string) => unknown;
  /** The answer when no recorded search begins the text. */
  nothing: unknown;
}

/** The searches whose texts the preview cannot all have recorded, by their paths. */
const searches: Record<string, Search> = {
  '/api/v1/quick-search': { param: 'q', narrow: narrowHits, nothing: { groups: [], incomplete: [] } },
  '/api/v1/search-all': { param: 'q', narrow: narrowHits, nothing: { groups: [], incomplete: [] } },
  '/api/v1/search/suggestions': { param: 'prefix', narrow: narrowList, nothing: [] },
  '/api/v1/users': { param: 'query', narrow: narrowList, nothing: { total: 0, items: [] } },
  '/api/v1/groups': { param: 'query', narrow: narrowList, nothing: { total: 0, items: [] } },
  '/api/v1/wiki/spaces': { param: 'query', narrow: narrowList, nothing: { total: 0, items: [] } },
  '/api/v1/code/repos': { param: 'query', narrow: narrowList, nothing: { total: 0, items: [] } },
  '/api/v1/code/projects': { param: 'query', narrow: narrowList, nothing: { total: 0, items: [] } },
};

/**
 * The name of a field of the query box as the server knows it: in lower case, without quotes, and its name for an
 * alias ("type" is issuetype), by the catalog of KQL the recording holds.
 */
function fieldName(answers: Answers, field: string): string {
  const name = normal(field.replace(/^"(.*)"$/, '$1'));
  const catalog = answers['/api/v1/search/autocomplete'];
  if (!catalog) return name;
  try {
    const fields =
      (JSON.parse(catalog.body) as { fields?: { name: string; aliases?: string[] }[] }).fields ?? [];
    const f = fields.find((x) => [x.name, ...(x.aliases ?? [])].some((n) => normal(n) === name));
    return f ? normal(f.name) : name;
  } catch {
    return name;
  }
}

/** Whether the other parameters of two searches agree: those of a recorded one with those asked for. */
function sameParams(
  answers: Answers,
  path: string,
  search: Search,
  a: URLSearchParams,
  b: URLSearchParams,
): boolean {
  const rest = (p: URLSearchParams) =>
    [...p.entries()]
      .filter(([k]) => k !== search.param && k !== 'limit')
      .map(([k, v]) => [
        k,
        path === '/api/v1/search/suggestions' && k === 'field' ? fieldName(answers, v) : v,
      ])
      .map(([k, v]) => `${k}=${v}`)
      .sort()
      .join('&');
  return rest(a) === rest(b);
}

/**
 * Whether a recorded answer serves a request for another number of entries: the same number, or a first page that
 * holds the whole list (as the space directory asks for 50 spaces as a list and 24 as cards, and the navigator for
 * 1000 issues to export what it shows 50 at a time).
 */
function servesLimit(recorded: Recorded, recordedLimit: string | null, limit: string | null): boolean {
  if (recordedLimit === limit) return true;
  if (!limit) return false;
  try {
    const page = JSON.parse(recorded.body) as unknown;
    if (!isObject(page)) return false;
    const list = Array.isArray(page.items) ? page.items : Array.isArray(page.issues) ? page.issues : null;
    return !!list && list.length === page.total && list.length <= Number(limit);
  } catch {
    return false;
  }
}

/** A first page of a list in another size, from a recorded first page that holds the whole list. */
function pageSizeFallback(answers: Answers, url: URL): Recorded | null {
  const limit = url.searchParams.get('limit');
  if (!limit || (url.searchParams.get('offset') ?? '0') !== '0') return null;
  const rest = (p: URLSearchParams) =>
    [...p.entries()]
      .filter(([k]) => k !== 'limit')
      .map(([k, v]) => `${k}=${v}`)
      .sort()
      .join('&');
  const wanted = rest(url.searchParams);
  for (const [key, recorded] of Object.entries(answers)) {
    if (recorded.status !== 200 || !key.startsWith(url.pathname)) continue;
    const r = new URL(key, url.origin);
    if (r.pathname !== url.pathname || rest(r.searchParams) !== wanted) continue;
    if (servesLimit(recorded, r.searchParams.get('limit'), limit)) return recorded;
  }
  return null;
}

/** The answer of a search that was not recorded, from the recorded search of the longest beginning of its text. */
function searchFallback(answers: Answers, url: URL): Recorded | null {
  const search = searches[url.pathname];
  if (!search) return null;
  const text = url.searchParams.get(search.param) ?? '';
  const typed = normal(text);
  let best: { recorded: Recorded; length: number } | null = null;
  for (const [key, recorded] of Object.entries(answers)) {
    if (recorded.status !== 200 || !key.startsWith('/')) continue;
    const r = new URL(key, url.origin);
    if (r.pathname !== url.pathname) continue;
    const begins = normal(r.searchParams.get(search.param) ?? '');
    if (!typed.startsWith(begins) || (best && best.length >= begins.length)) continue;
    if (!sameParams(answers, url.pathname, search, r.searchParams, url.searchParams)) continue;
    if (!servesLimit(recorded, r.searchParams.get('limit'), url.searchParams.get('limit'))) continue;
    best = { recorded, length: begins.length };
  }
  // Without a text there is nothing to narrow: a list in an order or with filters not recorded stays unknown.
  if (!best && typed === '') return null;
  let body = search.nothing;
  if (best) {
    try {
      body = search.narrow(JSON.parse(best.recorded.body), text);
    } catch {
      return null;
    }
  }
  return { status: 200, type: 'application/json', body: JSON.stringify(body) };
}

// --- bulk changes and moves ---

interface CheckIssue {
  key: string;
}

/** A plan of a move narrowed to issues: they and the sub-tasks that move with them, and the questions about them. */
export function narrowMovePlan(plan: Json, keys: Set<string>): Json {
  const issues = (plan.issues as { key: string; withParent?: string }[]).filter(
    (i) => keys.has(i.key) || (!!i.withParent && keys.has(i.withParent)),
  );
  const moving = new Set(issues.map((i) => i.key));
  const questions = (list: unknown) =>
    ((list as { issues: string[] }[] | undefined) ?? [])
      .map((q) => ({ ...q, issues: q.issues.filter((k) => moving.has(k)) }))
      .filter((q) => q.issues.length > 0);
  return { ...plan, types: questions(plan.types), statuses: questions(plan.statuses), issues };
}

/** A check of a bulk change narrowed to some of its issues. */
export function narrowBulkCheck(check: Json, keys: Set<string>): Json {
  const issues = (check.issues as (CheckIssue & { permitted: boolean })[]).filter((i) => keys.has(i.key));
  const permitted = issues.some((i) => i.permitted);
  const fields = ((check.fields as { missing: CheckIssue[] }[] | undefined) ?? []).map((f) => {
    const missing = f.missing.filter((m) => keys.has(m.key));
    return { ...f, missing, applies: permitted && missing.length === 0 };
  });
  const statuses = ((check.statuses as { issues: CheckIssue[] }[] | undefined) ?? [])
    .map((s) => ({ ...s, issues: s.issues.filter((i) => keys.has(i.key)) }))
    .filter((s) => s.issues.length > 0);
  return {
    ...check,
    total: issues.length,
    truncated: false,
    issues,
    fields,
    statuses,
    ...(isObject(check.move) ? { move: narrowMovePlan(check.move, keys) } : {}),
  };
}

/** The recorded checks of bulk changes: what was asked and what the server answered. */
function recordedChecks(answers: Answers): { asked: Json; check: Json }[] {
  const prefix = 'POST /api/v1/issues/bulk/check ';
  const out: { asked: Json; check: Json }[] = [];
  for (const [key, recorded] of Object.entries(answers)) {
    if (!key.startsWith(prefix) || recorded.status !== 200) continue;
    try {
      out.push({
        asked: JSON.parse(key.slice(prefix.length)) as Json,
        check: JSON.parse(recorded.body) as Json,
      });
    } catch {
      // not a check the preview can use
    }
  }
  return out;
}

const targetOf = (asked: Json) => (isObject(asked.move) ? asked.move.project : undefined);

/**
 * The answer of a check of a bulk change (POST /api/v1/issues/bulk/check) or of the plan of a move
 * (POST /api/v1/issues/move-plan) for issues chosen otherwise than while recording.
 */
function issuesFallback(answers: Answers, path: string, body: string): Recorded | null {
  let asked: Json;
  try {
    asked = JSON.parse(body) as Json;
  } catch {
    return null;
  }
  if (!Array.isArray(asked.issues) || asked.issues.length === 0) return null;
  const keys = new Set((asked.issues as string[]).map((k) => k.toUpperCase()));
  const plan = path === '/api/v1/issues/move-plan';
  const action = plan ? 'move' : asked.action;
  const target = plan ? asked.project : targetOf(asked);
  let best: Json | null = null;
  for (const { asked: a, check } of recordedChecks(answers)) {
    if (a.action !== action || targetOf(a) !== target || !Array.isArray(check.issues)) continue;
    const covered = new Set((check.issues as CheckIssue[]).map((i) => i.key));
    if (![...keys].every((k) => covered.has(k))) continue;
    if (plan && !isObject(check.move)) continue;
    if (!best || (check.issues as unknown[]).length < (best.issues as unknown[]).length) best = check;
  }
  if (!best) return null;
  const narrowed = plan ? narrowMovePlan(best.move as Json, keys) : narrowBulkCheck(best, keys);
  return { status: 200, type: 'application/json', body: JSON.stringify(narrowed) };
}

// --- activity ---

/**
 * The entries of the recorded activity stream of a path with these parameters, in whatever order and spelling they
 * were recorded and whatever its limit.
 */
function recordedEntries(answers: Answers, url: URL, params: URLSearchParams): { product: string }[] | null {
  const canonical = (p: URLSearchParams) =>
    [...p.entries()]
      .filter(([k]) => k !== 'limit')
      .map(([k, v]) => `${k}=${v}`)
      .sort()
      .join('&');
  const wanted = canonical(params);
  for (const [key, recorded] of Object.entries(answers)) {
    if (recorded.status !== 200 || !key.startsWith(url.pathname)) continue;
    const r = new URL(key, url.origin);
    if (r.pathname !== url.pathname || canonical(r.searchParams) !== wanted) continue;
    try {
      return (JSON.parse(recorded.body) as { entries: { product: string }[] }).entries;
    } catch {
      return null;
    }
  }
  return null;
}

/**
 * An activity stream that was not recorded: older entries end the stream; a shorter one is the start of the
 * recorded one; the entries of one product are those of the recorded stream of all products.
 */
function activityFallback(answers: Answers, url: URL): Recorded | null {
  const page = (entries: unknown[]) => ({
    status: 200,
    type: 'application/json',
    body: JSON.stringify({
      entries: entries.slice(0, Number(url.searchParams.get('limit') ?? 20)),
      next: '',
    }),
  });
  if (url.searchParams.get('before')) return page([]);
  const same = recordedEntries(answers, url, url.searchParams);
  if (same) return page(same);
  const product = url.searchParams.get('product');
  if (!product) return null;
  const all = new URLSearchParams(url.searchParams);
  all.delete('product');
  const entries = recordedEntries(answers, url, all);
  return entries ? page(entries.filter((e) => e.product === product)) : null;
}

/** What the preview answers for a request it has no recorded answer for, if it can derive one. */
export function fallback(answers: Answers, method: string, url: URL, body = ''): Recorded | null {
  if (method === 'GET' && url.pathname === '/api/v1/activity-stream') return activityFallback(answers, url);
  if (method === 'GET') return searchFallback(answers, url) ?? pageSizeFallback(answers, url);
  if (
    method === 'POST' &&
    (url.pathname === '/api/v1/issues/bulk/check' || url.pathname === '/api/v1/issues/move-plan')
  )
    return issuesFallback(answers, url.pathname, body);
  return null;
}
