// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/** An item of an ORDER BY clause. */
export interface OrderItem {
  field: string;
  desc: boolean;
}

// Other names of fields to order by (as KQL knows them).
const aliases: Record<string, string> = {
  issuekey: 'key',
  id: 'key',
  issue: 'key',
  type: 'issuetype',
  duedate: 'due',
  createddate: 'created',
  updateddate: 'updated',
  resolutiondate: 'resolved',
};

/** The name of a field to order by, for comparing: lower case, without quotes, aliases resolved. */
export function canonicalOrderField(field: string): string {
  let f = field.trim();
  if (f.length >= 2 && (f[0] === '"' || f[0] === "'") && f[f.length - 1] === f[0]) f = f.slice(1, -1);
  f = f.toLowerCase();
  return aliases[f] ?? f;
}

/**
 * Splits a query into its condition and the items of its ORDER BY clause. Only an ORDER BY outside of quoted text
 * and parentheses counts.
 */
export function parseOrder(query: string): { where: string; items: OrderItem[] } {
  let quote = '';
  let depth = 0;
  let at = -1;
  let length = 0;
  for (let i = 0; i < query.length; i++) {
    const c = query[i];
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = '';
      continue;
    }
    if (c === '"' || c === "'") quote = c;
    else if (c === '(') depth++;
    else if (c === ')') depth = Math.max(0, depth - 1);
    else if (depth === 0 && (i === 0 || /\s|\)/.test(query[i - 1]))) {
      const m = /^order\s+by\b/i.exec(query.slice(i));
      if (m) {
        at = i;
        length = m[0].length;
      }
    }
  }
  if (at < 0) return { where: query.trim(), items: [] };
  const items: OrderItem[] = [];
  let part = '';
  quote = '';
  const rest = query.slice(at + length);
  const flush = () => {
    const text = part.trim();
    part = '';
    if (!text) return;
    const m = /^(.*?)(?:\s+(asc|desc))?$/is.exec(text);
    items.push({ field: (m?.[1] ?? text).trim(), desc: m?.[2]?.toLowerCase() === 'desc' });
  };
  for (let i = 0; i < rest.length; i++) {
    const c = rest[i];
    if (quote) {
      if (c === quote) quote = '';
    } else if (c === '"' || c === "'") quote = c;
    else if (c === ',') {
      flush();
      continue;
    }
    part += c;
  }
  flush();
  return { where: query.slice(0, at).trim(), items };
}

/** The query ordered by a field first (then by its earlier order, at most three fields in all). */
export function orderBy(query: string, field: string, desc: boolean): string {
  const { where, items } = parseOrder(query);
  const name = canonicalOrderField(field);
  const rest = items.filter((o) => canonicalOrderField(o.field) !== name).slice(0, 2);
  const order = [{ field, desc }, ...rest].map((o) => `${o.field} ${o.desc ? 'DESC' : 'ASC'}`).join(', ');
  return where ? `${where} ORDER BY ${order}` : `ORDER BY ${order}`;
}
