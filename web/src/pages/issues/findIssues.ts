// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

const quote = (s: string) => `"${s.replace(/["\\]/g, (c) => `\\${c}`)}"`;

/**
 * Whether what someone typed names an issue rather than describing one: a key ("SHOP-12"), the number of an issue
 * of the project ("12") or the start of a key ("SH", "SHOP-").
 */
export function looksLikeKey(text: string, project: string): boolean {
  const s = text.trim();
  return (
    /^[A-Za-z][A-Za-z0-9_]*-\d*$/.test(s) ||
    /^\d+$/.test(s) ||
    (s.length >= 2 && `${project}-`.startsWith(s.toUpperCase()))
  );
}

/**
 * The query that finds issues from what someone typed in a picker: an issue by its key ("SHOP-12", or "12" in the
 * project), the issues of a project changed last for the start of its key ("SHOP"), or else issues whose text has
 * words starting so (in the project with inProject). The issue exclude is left out.
 */
export function findIssuesQuery(
  text: string,
  { project, exclude, inProject = false }: { project: string; exclude: string; inProject?: boolean },
): string {
  const s = text.trim();
  const key = /^([A-Za-z][A-Za-z0-9_]*)-(\d*)$/.exec(s);
  if (key?.[2]) return `key = ${s.toUpperCase()}`;
  if (/^\d+$/.test(s)) return `key = ${project}-${s}`;
  const prefix = key ? key[1].toUpperCase() : looksLikeKey(s, project) ? project : '';
  if (prefix) return `project = ${prefix} AND key != ${exclude} ORDER BY updated DESC`;
  const scope = inProject ? `project = ${project} AND ` : '';
  return `${scope}key != ${exclude}${s ? ` AND text ~ ${quote(`${s}*`)}` : ''} ORDER BY updated DESC`;
}
