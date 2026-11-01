// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { TFunction } from 'i18next';
import type { Schemas } from '../../api/client';
import i18n from '../../i18n';

export type Subscription = Schemas['Subscription'];
export type Schedule = Pick<Subscription, 'frequency' | 'minute' | 'weekdays' | 'hours' | 'monthDay'>;

const pad = (n: number) => String(n).padStart(2, '0');

/** The time of a minute of the day, HH:MM. */
export function timeOfMinute(minute: number): string {
  return `${pad(Math.floor(minute / 60))}:${pad(minute % 60)}`;
}

/** The minute of the day of a time HH:MM, or null. */
export function minuteOfTime(time: string): number | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(time.trim());
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  return h < 24 && min < 60 ? h * 60 + min : null;
}

/** The names of the ISO days of the week (index 0 is Monday) in the language of the UI. */
export function weekdayNames(style: 'short' | 'long' = 'short'): string[] {
  const fmt = new Intl.DateTimeFormat(i18n.language === 'de' ? 'de-DE' : 'en-GB', {
    weekday: style,
    timeZone: 'UTC',
  });
  // 5 January 2026 is a Monday.
  return [0, 1, 2, 3, 4, 5, 6].map((d) => fmt.format(new Date(Date.UTC(2026, 0, 5 + d))));
}

/** Runs of consecutive numbers: [1, 2, 3, 5] → [[1, 3], [5, 5]]. */
export function runs(values: readonly number[]): [number, number][] {
  const out: [number, number][] = [];
  for (const v of [...values].sort((a, b) => a - b)) {
    const last = out[out.length - 1];
    if (last && v === last[1] + 1) last[1] = v;
    else if (!last || v !== last[1]) out.push([v, v]);
  }
  return out;
}

/** Describes days of the week: "Mon–Fri", "Mon, Wed", "every day". */
export function describeDays(t: TFunction, days: readonly number[]): string {
  if (days.length >= 7) return t('filters.schedule.everyDay');
  const names = weekdayNames();
  return runs(days)
    .map(([a, b]) =>
      a === b
        ? names[a - 1]
        : b === a + 1
          ? `${names[a - 1]}, ${names[b - 1]}`
          : `${names[a - 1]}–${names[b - 1]}`,
    )
    .join(', ');
}

/** The hours of an hourly schedule between two hours (inclusive; past midnight when the first is later). */
export function hoursBetween(from: number, to: number): number[] {
  const out: number[] = [];
  for (let h = from; ; h = (h + 1) % 24) {
    out.push(h);
    if (h === to || out.length === 24) return out;
  }
}

/** The first and last hour of the hours of an hourly schedule, if they are one stretch (possibly past midnight). */
export function hourWindow(hours: readonly number[]): [number, number] | null {
  if (hours.length === 0 || hours.length === 24) return null;
  const r = runs(hours);
  if (r.length === 1) return r[0];
  // A stretch past midnight: [[0, 2], [22, 23]].
  if (r.length === 2 && r[0][0] === 0 && r[1][1] === 23) return [r[1][0], r[0][1]];
  return null;
}

/** Describes a schedule: "Mon–Fri at 08:30", "Hourly at :15, every day, 08:00–17:59". */
export function describeSchedule(t: TFunction, s: Schedule): string {
  const days = describeDays(t, s.weekdays ?? []);
  switch (s.frequency) {
    case 'hourly': {
      const text = t('filters.schedule.hourly', { minute: pad(s.minute), days });
      const hours = s.hours ?? [];
      if (hours.length === 0 || hours.length === 24) return text;
      const window = hourWindow(hours);
      const ranges = window ? [window] : runs(hours);
      return `${text}, ${ranges.map(([a, b]) => `${pad(a)}:00–${pad(b)}:59`).join(', ')}`;
    }
    case 'daily':
      return (s.weekdays ?? []).length >= 7
        ? t('filters.schedule.dailyAll', { time: timeOfMinute(s.minute) })
        : t('filters.schedule.daily', { time: timeOfMinute(s.minute), days });
    default:
      return s.monthDay === 31
        ? t('filters.schedule.monthlyLast', { time: timeOfMinute(s.minute) })
        : t('filters.schedule.monthly', { day: s.monthDay, time: timeOfMinute(s.minute) });
  }
}
