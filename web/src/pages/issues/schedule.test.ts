// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import i18n from '../../i18n';
import {
  describeSchedule,
  hoursBetween,
  hourWindow,
  minuteOfTime,
  runs,
  timeOfMinute,
  weekdayNames,
  type Schedule,
} from './schedule';

const workdays = [1, 2, 3, 4, 5];

describe('schedules', () => {
  it('finds runs of days and hours', () => {
    expect(runs([5, 1, 2, 3])).toEqual([
      [1, 3],
      [5, 5],
    ]);
    expect(runs([])).toEqual([]);
    expect(hoursBetween(8, 17)).toEqual([8, 9, 10, 11, 12, 13, 14, 15, 16, 17]);
    expect(hoursBetween(22, 2)).toEqual([22, 23, 0, 1, 2]);
    expect(hoursBetween(8, 7)).toHaveLength(24);
    expect(hourWindow([8, 9, 10])).toEqual([8, 10]);
    expect(hourWindow([0, 1, 2, 22, 23])).toEqual([22, 2]);
    expect(hourWindow([])).toBeNull();
    expect(hourWindow([1, 3])).toBeNull();
  });

  it('converts times of the day', () => {
    expect(timeOfMinute(510)).toBe('08:30');
    expect(minuteOfTime('08:30')).toBe(510);
    expect(minuteOfTime('7:05')).toBe(425);
    expect(minuteOfTime('24:00')).toBeNull();
    expect(minuteOfTime('soon')).toBeNull();
  });

  it('describes schedules in English and German', async () => {
    // The short names of the days come from the browser's locale data ("Mo." or "Mo" in German).
    await i18n.changeLanguage('de');
    const d = weekdayNames();
    const cases: [Schedule, string, string][] = [
      [
        { frequency: 'daily', minute: 510, weekdays: workdays },
        'Mon–Fri at 08:30',
        `${d[0]}–${d[4]} um 08:30`,
      ],
      [
        { frequency: 'daily', minute: 600, weekdays: [1, 2, 3, 4, 5, 6, 7] },
        'Every day at 10:00',
        'Täglich um 10:00',
      ],
      [
        { frequency: 'daily', minute: 600, weekdays: [1, 3] },
        'Mon, Wed at 10:00',
        `${d[0]}, ${d[2]} um 10:00`,
      ],
      [
        { frequency: 'hourly', minute: 15, weekdays: workdays, hours: hoursBetween(8, 17) },
        'Hourly at :15, Mon–Fri, 08:00–17:59',
        `Stündlich zur Minute 15, ${d[0]}–${d[4]}, 08:00–17:59`,
      ],
      [
        { frequency: 'hourly', minute: 0, weekdays: [1, 2, 3, 4, 5, 6, 7], hours: [] },
        'Hourly at :00, every day',
        'Stündlich zur Minute 00, täglich',
      ],
      [
        { frequency: 'hourly', minute: 0, weekdays: [6, 7], hours: hoursBetween(22, 1) },
        'Hourly at :00, Sat, Sun, 22:00–01:59',
        `Stündlich zur Minute 00, ${d[5]}, ${d[6]}, 22:00–01:59`,
      ],
      [
        { frequency: 'monthly', minute: 420, monthDay: 15 },
        'Monthly on day 15 at 07:00',
        'Monatlich am 15. um 07:00',
      ],
      [
        { frequency: 'monthly', minute: 450, monthDay: 31 },
        'Monthly on the last day at 07:30',
        'Monatlich am letzten Tag um 07:30',
      ],
    ];
    for (const [lang, column] of [
      ['en', 1],
      ['de', 2],
    ] as const) {
      await i18n.changeLanguage(lang);
      for (const c of cases) expect(describeSchedule(i18n.t, c[0])).toBe(c[column]);
    }
  });
});
