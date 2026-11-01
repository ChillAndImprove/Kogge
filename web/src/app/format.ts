// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import i18n from '../i18n';

let timeZone: string | undefined;

export function setTimeZone(tz: string | undefined) {
  timeZone = tz || undefined;
}

function locale() {
  return i18n.language === 'de' ? 'de-DE' : 'en-GB';
}

export function formatDateTime(value: string | Date | null | undefined): string {
  if (!value) return '';
  return new Intl.DateTimeFormat(locale(), { dateStyle: 'medium', timeStyle: 'short', timeZone }).format(
    new Date(value),
  );
}

export function formatDate(value: string | Date | null | undefined): string {
  if (!value) return '';
  return new Intl.DateTimeFormat(locale(), { dateStyle: 'medium', timeZone }).format(new Date(value));
}

/** Formats a calendar day (YYYY-MM-DD) as it is, in every time zone; what is no day gives ''. */
export function formatDay(value: string | null | undefined): string {
  if (!value) return '';
  const day = new Date(`${value.slice(0, 10)}T00:00:00Z`);
  if (Number.isNaN(day.getTime())) return '';
  return new Intl.DateTimeFormat(locale(), { dateStyle: 'medium', timeZone: 'UTC' }).format(day);
}

/** Today as YYYY-MM-DD in the user's time zone. */
export function today(): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone }).format(new Date());
}

export function formatNumber(n: number): string {
  return n.toLocaleString(locale(), { maximumFractionDigits: 6 });
}

export function formatRelative(value: string | Date | null | undefined): string {
  if (!value) return '';
  const diff = (new Date(value).getTime() - Date.now()) / 1000;
  const rtf = new Intl.RelativeTimeFormat(locale(), { numeric: 'auto' });
  const abs = Math.abs(diff);
  if (abs < 60) return rtf.format(Math.round(diff), 'second');
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), 'day');
  return formatDate(value);
}

export function formatBytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toLocaleString(locale(), { maximumFractionDigits: 1 })} ${units[i]}`;
}
