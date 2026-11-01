// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/**
 * Autocompletion of KQL: a tokenizer that follows the server's lexer (internal/issues/kql), the place of the caret in
 * the grammar — at a field, an operator, a value, a keyword — and the suggestions there from the server's catalogue
 * of fields, operators and functions. Values come from the server (only what the user may see).
 */

/** A token of a query. */
export interface Token {
  kind: 'word' | 'string' | 'op' | 'lparen' | 'rparen' | 'comma' | 'custom' | 'other';
  /** The text: a word as written, the content of a string, the id of cf[id]. */
  text: string;
  start: number;
  end: number;
  /** Strings and cf[…] that are not closed (the user is still typing them). */
  open?: boolean;
}

const keywords = new Set([
  'and',
  'or',
  'not',
  'in',
  'is',
  'empty',
  'null',
  'order',
  'by',
  'asc',
  'desc',
  'was',
  'changed',
]);

/** Whether c may appear in an unquoted word (as the server's lexer decides). */
export function isWordChar(c: string): boolean {
  return /[A-Za-z0-9_.\-@:/+*$#%]/.test(c) || c.charCodeAt(0) >= 0x80;
}

/** Splits a query into tokens; unlike the server it never fails, so that half-typed queries can be completed. */
export function tokenize(src: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  while (i < src.length) {
    const c = src[i];
    if (/\s/.test(c)) {
      i++;
      continue;
    }
    const start = i;
    const two = src.slice(i, i + 2);
    if (c === '(' || c === ')' || c === ',') {
      out.push({ kind: c === '(' ? 'lparen' : c === ')' ? 'rparen' : 'comma', text: c, start, end: i + 1 });
      i++;
    } else if (c === '"' || c === "'") {
      let text = '';
      i++;
      let open = true;
      while (i < src.length) {
        if (src[i] === '\\' && i + 1 < src.length) {
          text += src[i + 1];
          i += 2;
        } else if (src[i] === c) {
          i++;
          open = false;
          break;
        } else {
          text += src[i++];
        }
      }
      out.push({ kind: 'string', text, start, end: i, open });
    } else if (two === '!=' || two === '!~' || two === '>=' || two === '<=') {
      out.push({ kind: 'op', text: two, start, end: i + 2 });
      i += 2;
    } else if (c === '=' || c === '~' || c === '>' || c === '<') {
      out.push({ kind: 'op', text: c, start, end: i + 1 });
      i++;
    } else if (c === '!') {
      out.push({ kind: 'word', text: 'not', start, end: i + 1 });
      i++;
    } else if (two === '&&' || two === '||') {
      out.push({ kind: 'word', text: two === '&&' ? 'and' : 'or', start, end: i + 2 });
      i += 2;
    } else if (isWordChar(c)) {
      while (i < src.length && isWordChar(src[i])) i++;
      const word = src.slice(start, i);
      if (word.toLowerCase() === 'cf' && src[i] === '[') {
        const close = src.indexOf(']', i);
        const id = src.slice(i + 1, close < 0 ? src.length : close);
        i = close < 0 ? src.length : close + 1;
        out.push({ kind: 'custom', text: id, start, end: i, open: close < 0 });
      } else {
        out.push({ kind: 'word', text: word, start, end: i });
      }
    } else {
      out.push({ kind: 'other', text: c, start, end: i + 1 });
      i++;
    }
  }
  return out;
}

/** A field of the catalogue (the server's KQLField). */
export interface CatalogField {
  name: string;
  aliases: string[];
  custom: boolean;
  id?: number;
  kind: string;
  operators: string[];
  history: boolean;
  orderable: boolean;
  values: boolean;
}

/** A function of the catalogue. */
export interface CatalogFunction {
  name: string;
  /** What its argument is: group, issue, project, or increment (such as -1d). */
  arg?: string;
  kinds: string[];
  list: boolean;
}

/** What the server tells of KQL. */
export interface Catalog {
  fields: CatalogField[];
  functions: CatalogFunction[];
  keywords: string[];
}

/** What is expected at the caret. */
export type Expect =
  | { kind: 'field'; start: boolean }
  | { kind: 'operator'; field: string }
  | { kind: 'value'; field: string; operator: string; inList: boolean }
  | { kind: 'list'; field: string; operator: string }
  | { kind: 'empty' }
  | { kind: 'argument'; fn: string }
  | { kind: 'logical'; depth: number; ordered: boolean; history?: 'was' | 'changed' }
  | { kind: 'orderField' }
  | { kind: 'direction' }
  | { kind: 'none' };

/** The place of the caret: what is expected there and the text that a suggestion replaces. */
export interface CaretContext {
  expect: Expect;
  /** What was typed of the element (the content of a string, without its quote). */
  prefix: string;
  /** The range a suggestion replaces. */
  from: number;
  to: number;
}

const historyPredicates = ['after', 'before', 'on', 'during', 'by', 'from', 'to'];

/** The field whose values a predicate of WAS and CHANGED takes: who (BY), when (AFTER, BEFORE, ON, DURING), or the
 * values of the field itself (FROM, TO). */
function predicateField(predicate: string, field: string): string {
  if (predicate === 'by') return 'assignee';
  if (predicate === 'from' || predicate === 'to') return field;
  return 'updated';
}

const lower = (t: Token | undefined) => (t && t.kind === 'word' ? t.text.toLowerCase() : '');

/** The name of a field token as written (cf[id] as such). */
function fieldName(t: Token): string {
  return t.kind === 'custom' ? `cf[${t.text}]` : t.text;
}

/** Works out what is expected at the caret. */
export function contextAt(src: string, caret: number): CaretContext {
  const all = tokenize(src);
  // The token being typed: a word, string or cf[…] that the caret is in or right after.
  let current: Token | undefined;
  const before: Token[] = [];
  for (const t of all) {
    if (t.end < caret || (t.end === caret && !['word', 'string', 'custom'].includes(t.kind))) {
      before.push(t);
    } else if (t.start < caret && t.end >= caret && ['word', 'string', 'custom'].includes(t.kind)) {
      current = t;
      break;
    } else {
      break;
    }
  }
  // A closed string the caret is right after is done.
  if (current && current.kind === 'string' && !current.open && current.end === caret) {
    before.push(current);
    current = undefined;
  }
  const prefix = current
    ? current.kind === 'string'
      ? current.text.slice(0, Math.max(0, caret - current.start - 1))
      : current.kind === 'custom'
        ? src.slice(current.start, caret)
        : src.slice(current.start, caret)
    : '';
  const from = current ? current.start : caret;
  const to = current ? current.end : caret;
  return { expect: expectAfter(before), prefix, from, to };
}

/** Follows the grammar (docs/reference/kql.md) through the tokens before the caret. */
function expectAfter(tokens: Token[]): Expect {
  type State =
    | { s: 'clause'; start: boolean }
    | { s: 'op'; field: string }
    | { s: 'not-in'; field: string }
    | { s: 'is'; field: string }
    | { s: 'value'; field: string; op: string }
    | { s: 'list-open'; field: string; op: string }
    | { s: 'list'; field: string; op: string; after: 'value' | 'separator' }
    | { s: 'done'; history?: 'was' | 'changed'; field?: string }
    | { s: 'was'; field: string }
    | { s: 'predicate'; field: string; kind: string; history: 'was' | 'changed' }
    | { s: 'order-by' }
    | { s: 'order-field' }
    | { s: 'order-done' }
    | { s: 'function'; fn: string; ret: State }
    | { s: 'invalid' };
  let st: State = { s: 'clause', start: true };
  let depth = 0;
  let ordered = false;
  // Lists nest one level (IN (…), DURING (…), BY (…)); afterwards the state goes on as given.
  let listEnd: State | null = null;
  for (let k = 0; k < tokens.length; k++) {
    const t = tokens[k];
    const w = lower(t);
    const next = tokens[k + 1];
    switch (st.s) {
      case 'clause':
        if (w === 'not') break;
        if (t.kind === 'lparen') {
          depth++;
          break;
        }
        if (w === 'order' && st.start && depth === 0) {
          st = { s: 'order-by' };
          break;
        }
        if ((t.kind === 'word' && !keywords.has(w)) || t.kind === 'string' || t.kind === 'custom') {
          st = { s: 'op', field: fieldName(t) };
        } else st = { s: 'invalid' };
        break;
      case 'op':
        if (t.kind === 'op') st = { s: 'value', field: st.field, op: t.text };
        else if (w === 'in') st = { s: 'list-open', field: st.field, op: 'in' };
        else if (w === 'not') st = { s: 'not-in', field: st.field };
        else if (w === 'is') st = { s: 'is', field: st.field };
        else if (w === 'was') st = { s: 'was', field: st.field };
        else if (w === 'changed') st = { s: 'done', history: 'changed', field: st.field };
        else st = { s: 'invalid' };
        break;
      case 'not-in':
        st = w === 'in' ? { s: 'list-open', field: st.field, op: 'not in' } : { s: 'invalid' };
        break;
      case 'is':
        if (w === 'not') break;
        st = w === 'empty' || w === 'null' ? { s: 'done' } : { s: 'invalid' };
        break;
      case 'was':
        if (w === 'not') break;
        if (w === 'in') st = { s: 'list-open', field: st.field, op: 'was in' };
        else {
          st = { s: 'value', field: st.field, op: 'was' };
          k--; // the value
        }
        break;
      case 'value': {
        const done: State = st.op.startsWith('was')
          ? { s: 'done', history: 'was', field: st.field }
          : { s: 'done' };
        if (t.kind === 'word' && next?.kind === 'lparen' && !keywords.has(w)) {
          st = { s: 'function', fn: t.text, ret: done };
          k++; // the parenthesis
        } else if (t.kind === 'word' || t.kind === 'string') st = done;
        else st = { s: 'invalid' };
        break;
      }
      case 'list-open': {
        const done: State = st.op.startsWith('was')
          ? { s: 'done', history: 'was', field: st.field }
          : { s: 'done' };
        if (t.kind === 'lparen') {
          listEnd = done;
          st = { s: 'list', field: st.field, op: st.op, after: 'separator' };
        } else if (t.kind === 'word' && next?.kind === 'lparen') {
          st = { s: 'function', fn: t.text, ret: done };
          k++;
        } else st = { s: 'invalid' };
        break;
      }
      case 'list': {
        const list: Extract<State, { s: 'list' }> = st;
        if (list.after === 'separator') {
          if (t.kind === 'word' && next?.kind === 'lparen' && !keywords.has(w)) {
            st = { s: 'function', fn: t.text, ret: { ...list, after: 'value' } };
            k++;
          } else if (t.kind === 'word' || t.kind === 'string') st = { ...list, after: 'value' };
          else st = { s: 'invalid' };
        } else if (t.kind === 'comma') st = { ...list, after: 'separator' };
        else if (t.kind === 'rparen') {
          st = listEnd ?? { s: 'done' };
          listEnd = null;
        } else st = { s: 'invalid' };
        break;
      }
      case 'function':
        if (t.kind === 'rparen') st = st.ret;
        break;
      case 'predicate': {
        const done: State = { s: 'done', history: st.history, field: st.field };
        if (st.kind === 'during' || (st.kind === 'by' && t.kind === 'lparen')) {
          if (t.kind === 'lparen') {
            listEnd = done;
            st = { s: 'list', field: predicateField(st.kind, st.field), op: st.kind, after: 'separator' };
          } else st = { s: 'invalid' };
        } else if (t.kind === 'word' && next?.kind === 'lparen' && !keywords.has(w)) {
          st = { s: 'function', fn: t.text, ret: done };
          k++;
        } else if (t.kind === 'word' || t.kind === 'string') st = done;
        else st = { s: 'invalid' };
        break;
      }
      case 'done':
        if (w === 'and' || w === 'or') st = { s: 'clause', start: false };
        else if (t.kind === 'rparen' && depth > 0) {
          depth--;
          st = { s: 'done' };
        } else if (w === 'order' && depth === 0) st = { s: 'order-by' };
        else if (
          st.history &&
          historyPredicates.includes(w) &&
          (st.history === 'changed' || (w !== 'from' && w !== 'to'))
        ) {
          st = { s: 'predicate', field: st.field ?? '', kind: w, history: st.history };
        } else st = { s: 'invalid' };
        break;
      case 'order-by':
        if (w === 'by') {
          ordered = true;
          st = { s: 'order-field' };
        } else st = { s: 'invalid' };
        break;
      case 'order-field':
        st =
          t.kind === 'word' || t.kind === 'string' || t.kind === 'custom'
            ? { s: 'order-done' }
            : { s: 'invalid' };
        break;
      case 'order-done':
        if (t.kind === 'comma') st = { s: 'order-field' };
        else if (w !== 'asc' && w !== 'desc') st = { s: 'invalid' };
        break;
      case 'invalid':
        return { kind: 'none' };
    }
  }
  switch (st.s) {
    case 'clause':
      return { kind: 'field', start: st.start && depth === 0 };
    case 'op':
      return { kind: 'operator', field: st.field };
    case 'not-in':
    case 'is':
      return st.s === 'is' ? { kind: 'empty' } : { kind: 'none' };
    case 'was':
      return { kind: 'value', field: st.field, operator: 'was', inList: false };
    case 'value':
      return { kind: 'value', field: st.field, operator: st.op, inList: false };
    case 'list-open':
      return { kind: 'list', field: st.field, operator: st.op };
    case 'list':
      return st.after === 'separator'
        ? { kind: 'value', field: st.field, operator: st.op, inList: true }
        : { kind: 'none' };
    case 'function':
      return { kind: 'argument', fn: st.fn };
    case 'predicate':
      return st.kind === 'during'
        ? { kind: 'none' }
        : { kind: 'value', field: predicateField(st.kind, st.field), operator: st.kind, inList: false };
    case 'done':
      return { kind: 'logical', depth, ordered, history: st.history };
    case 'order-by':
      return { kind: 'none' };
    case 'order-field':
      return { kind: 'orderField' };
    case 'order-done':
      return { kind: 'direction' };
  }
  return { kind: 'none' };
}

/** Whether a value must be quoted to be read as one value (as the server prints values). */
export function needsQuotes(s: string): boolean {
  if (s === '' || keywords.has(s.toLowerCase())) return true;
  for (const c of s) if (!isWordChar(c)) return true;
  return false;
}

/** A value as written in a query: quoted where needed. */
export function quote(s: string): string {
  return needsQuotes(s) ? `"${s.replace(/["\\]/g, (c) => `\\${c}`)}"` : s;
}

/** Finds a field of the catalogue by a name as written (case-insensitive, aliases, cf[id]). */
export function findField(catalog: Catalog | undefined, name: string): CatalogField | undefined {
  if (!catalog) return undefined;
  const n = name.trim().toLowerCase();
  const custom = /^cf\[(\d+)\]$/.exec(n);
  if (custom) return catalog.fields.find((f) => f.custom && f.id === Number(custom[1]));
  return catalog.fields.find(
    (f) => f.name.toLowerCase() === n || f.aliases.some((a) => a.toLowerCase() === n),
  );
}

/** A suggestion for the query box. */
export interface Suggestion {
  kind: 'field' | 'operator' | 'value' | 'function' | 'keyword';
  /** What the list shows. */
  label: string;
  /** What replaces the typed text. */
  insert: string;
  /** Where the caret goes, counted back from the end of insert. */
  back?: number;
  detail?: string;
}

const matches = (text: string, prefix: string) => text.toLowerCase().startsWith(prefix.toLowerCase());

/** Suggestions that do not need the server: fields, operators, keywords and functions. */
export function staticSuggestions(catalog: Catalog | undefined, ctx: CaretContext): Suggestion[] {
  if (!catalog) return [];
  const e = ctx.expect;
  const p = ctx.prefix;
  const keyword = (k: string): Suggestion => ({ kind: 'keyword', label: k, insert: `${k} ` });
  const out: Suggestion[] = [];
  switch (e.kind) {
    case 'field':
      for (const f of catalog.fields) {
        if (f.name === 'rank') continue;
        const alias = f.aliases.find((a) => matches(a, p));
        if (
          matches(f.name, p) ||
          alias ||
          (p && f.custom && f.name.toLowerCase().includes(p.toLowerCase()))
        ) {
          out.push({
            kind: 'field',
            label: f.name,
            insert: `${quote(f.name)} `,
            detail: f.custom ? `cf[${f.id}]` : alias,
          });
        }
      }
      if (matches('NOT', p)) out.push(keyword('NOT'));
      if (e.start && p && matches('ORDER BY', p)) out.push(keyword('ORDER BY'));
      break;
    case 'operator': {
      const f = findField(catalog, e.field);
      const ops = f ? f.operators.map((o) => o.toUpperCase()) : [];
      if (f?.history) ops.push('WAS', 'WAS NOT', 'WAS IN', 'WAS NOT IN', 'CHANGED');
      for (const o of ops) if (matches(o, p)) out.push({ kind: 'operator', label: o, insert: `${o} ` });
      break;
    }
    case 'empty':
      for (const k of ['EMPTY']) if (matches(k, p)) out.push(keyword(k));
      break;
    case 'list':
      if (!p) out.push({ kind: 'keyword', label: '( … )', insert: '()', back: 1 });
      out.push(...functionSuggestions(catalog, e.field, p, true));
      break;
    case 'value':
      out.push(
        ...functionSuggestions(catalog, e.field, p, e.inList || e.operator.includes('in') || !e.operator),
      );
      if (!e.inList && ['=', '!='].includes(e.operator) && matches('EMPTY', p) && p)
        out.push(keyword('EMPTY'));
      break;
    case 'logical': {
      const words = ['AND', 'OR'];
      if (e.history) {
        words.push('AFTER', 'BEFORE', 'ON', 'DURING', 'BY');
        if (e.history === 'changed') words.push('FROM', 'TO');
      }
      if (e.depth === 0 && !e.ordered) words.push('ORDER BY');
      for (const k of words) {
        if (matches(k, p)) {
          out.push(
            k === 'DURING'
              ? { kind: 'keyword', label: 'DURING ( … , … )', insert: 'DURING ()', back: 1 }
              : keyword(k),
          );
        }
      }
      if (e.depth > 0 && !p) out.push({ kind: 'keyword', label: ')', insert: ') ' });
      break;
    }
    case 'orderField':
      for (const f of catalog.fields) {
        if (f.orderable && (matches(f.name, p) || f.aliases.some((a) => matches(a, p)))) {
          out.push({
            kind: 'field',
            label: f.name,
            insert: `${quote(f.name)} `,
            detail: f.custom ? `cf[${f.id}]` : undefined,
          });
        }
      }
      break;
    case 'direction':
      for (const k of ['ASC', 'DESC']) if (matches(k, p)) out.push(keyword(k));
      if (!p) out.push({ kind: 'keyword', label: ',', insert: ', ' });
      break;
  }
  return out;
}

/** The functions that suit a field (lists only where a list is expected). */
function functionSuggestions(catalog: Catalog, fieldName: string, p: string, list: boolean): Suggestion[] {
  const f = findField(catalog, fieldName);
  const kinds = f ? [f.kind] : ['date', 'datetime', 'user', 'version', 'sprint'];
  // Dates take functions of either kind.
  if (f?.kind === 'date' || f?.kind === 'datetime') kinds.push('date', 'datetime');
  const out: Suggestion[] = [];
  for (const fn of catalog.functions) {
    if (!fn.kinds.some((k) => kinds.includes(k)) || (fn.list && !list) || !matches(fn.name, p)) continue;
    // The caret goes where the argument is typed: into the quotes of a group name, between the parentheses of an
    // issue or a project; increments are optional.
    const args = fn.arg === 'group' ? '""' : '';
    const back = fn.arg === 'group' ? 2 : fn.arg === 'issue' || fn.arg === 'project' ? 1 : 0;
    out.push({
      kind: 'function',
      label: `${fn.name}()`,
      insert: `${fn.name}(${args})`,
      back,
      detail: fn.arg,
    });
  }
  return out;
}

/** The field whose values the server suggests at the caret, if any (and for which kind of argument). */
export function valueField(catalog: Catalog | undefined, ctx: CaretContext): string | undefined {
  const e = ctx.expect;
  if (e.kind === 'value' && e.field) {
    const f = findField(catalog, e.field);
    return f?.values ? e.field : undefined;
  }
  // The server suggests the arguments of functions by the function's name.
  if (e.kind === 'argument') {
    const fn = catalog?.functions.find((f) => f.name.toLowerCase() === e.fn.toLowerCase());
    return fn?.arg && fn.arg !== 'increment' ? fn.name : undefined;
  }
  return undefined;
}

/** A suggestion of a value the server sent. */
export function valueSuggestion(
  v: { value: string; label?: string; detail?: string },
  inArgument = false,
): Suggestion {
  const text = quote(v.value);
  return {
    kind: 'value',
    label: v.label ? `${v.value} – ${v.label}` : v.value,
    // A function's argument is followed by its parenthesis, not by a space.
    insert: inArgument ? text : `${text} `,
    detail: v.detail,
  };
}

/** Applies a suggestion: the query and the caret after it. */
export function apply(src: string, ctx: CaretContext, s: Suggestion): { text: string; caret: number } {
  // A string the user is typing is replaced as a whole (with its quotes).
  const text = src.slice(0, ctx.from) + s.insert + src.slice(ctx.to);
  return { text, caret: ctx.from + s.insert.length - (s.back ?? 0) };
}
