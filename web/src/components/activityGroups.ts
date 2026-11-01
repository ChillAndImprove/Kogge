// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { Schemas } from '../api/client';

/**
 * How activity streams arrange their entries: by day, and entries that read as one in a row — the
 * same person doing the same to the same thing within an hour, a series of runs of the same pipeline.
 */

export type Entry = Schemas['Entry'];
type IssueStatus = Schemas['IssueStatus'];
type PushCommit = Schemas['PushCommit'];
type ChangedField = Schemas['ChangedField'];

/** Entries shown as one line, newest first, with the day they belong to (YYYY-MM-DD in the user's time zone). */
export interface EntryGroup {
  key: string;
  day: string;
  entries: Entry[];
}

/** How far apart the entries of a group may be. */
export const groupWindow = 60 * 60 * 1000;

/** The day of a moment as YYYY-MM-DD in a time zone (the browser's without one). */
export function dayKey(at: string | Date, timeZone?: string): string {
  try {
    return new Intl.DateTimeFormat('en-CA', { timeZone }).format(new Date(at));
  } catch {
    return new Intl.DateTimeFormat('en-CA').format(new Date(at));
  }
}

export function isRun(e: Entry) {
  return e.verb.startsWith('run.');
}

/** What an entry happened to: the issue, page or pull request (without the comment in its address), the branches
 * and tags of a push, the pipeline of a run. */
export function targetOf(e: Entry): string {
  if (e.verb === 'pushed')
    return `${e.container.key} ${(e.refs ?? []).map((r) => (r.change === 'deleted' ? `-${r.name}` : r.name)).join(' ')}`;
  if (isRun(e)) return `${e.container.key} ${e.pipeline ?? e.title}`;
  return e.url.replace(/#.*$/, '');
}

function joins(group: Entry[], e: Entry): boolean {
  const first = group[0];
  if (first.verb !== e.verb || targetOf(first) !== targetOf(e)) return false;
  // Runs of a pipeline in a row are a series, whoever started them.
  if (isRun(e)) return true;
  return (
    !!first.actor &&
    first.actor.id === e.actor?.id &&
    new Date(first.at).getTime() - new Date(e.at).getTime() <= groupWindow
  );
}

/** Arranges a stream (newest first) into groups of entries that read as one, within the days. */
export function groupEntries(entries: Entry[], timeZone?: string): EntryGroup[] {
  const out: EntryGroup[] = [];
  for (const e of entries) {
    const day = dayKey(e.at, timeZone);
    const last = out[out.length - 1];
    if (last && last.day === day && joins(last.entries, e)) last.entries.push(e);
    else out.push({ key: e.key, day, entries: [e] });
  }
  return out;
}

/** The day before a day (YYYY-MM-DD). */
function previousDay(day: string): string {
  const d = new Date(`${day}T12:00:00Z`);
  d.setUTCDate(d.getUTCDate() - 1);
  return d.toISOString().slice(0, 10);
}

/** How a day is named: today, yesterday, by its weekday within the past week, else by its date. */
export function dayKind(day: string, today: string): 'today' | 'yesterday' | 'weekday' | 'date' {
  if (day === today) return 'today';
  const yesterday = previousDay(today);
  if (day === yesterday) return 'yesterday';
  let d = yesterday;
  for (let i = 0; i < 5; i++) {
    d = previousDay(d);
    if (day === d) return 'weekday';
  }
  return 'date';
}

/** The statuses an issue went through in a group of status changes, oldest first; empty when one lacks them. */
export function statusChain(group: Entry[]): IssueStatus[] {
  if (group.some((e) => !e.status)) return [];
  const oldest = [...group].reverse();
  return [oldest[0].status!.from, ...oldest.map((e) => e.status!.to)];
}

/** The commits of a group of pushes: how many there are (null when a push does not say) and the newest. */
export function pushedCommits(group: Entry[], newest = 3): { count: number | null; newest: PushCommit[] } {
  if (group.some((e) => !e.commits)) return { count: null, newest: [] };
  return {
    count: group.reduce((n, e) => n + (e.commits?.count ?? 0), 0),
    newest: group.flatMap((e) => e.commits?.newest ?? []).slice(0, newest),
  };
}

/** The fields a group of changes changed, each once, in the order they first changed. */
export function changedFields(group: Entry[]): ChangedField[] {
  const seen = new Set<string>();
  const out: ChangedField[] = [];
  for (const e of [...group].reverse()) {
    for (const f of e.fields ?? []) {
      if (!seen.has(f.key)) {
        seen.add(f.key);
        out.push(f);
      }
    }
  }
  return out;
}
