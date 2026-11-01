// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { formatMiB, inUnit, toMiB, unitOf } from './sizes';

describe('sizes of limits', () => {
  it('shows whole GiB in GiB and everything else in MiB', () => {
    expect(unitOf(2048)).toBe('GiB');
    expect(unitOf(1024)).toBe('GiB');
    expect(unitOf(1536)).toBe('MiB');
    expect(unitOf(100)).toBe('MiB');
    expect(unitOf(0)).toBe('MiB');
  });

  it('converts between MiB and the units', () => {
    expect(inUnit(2048, 'GiB')).toBe(2);
    expect(inUnit(1536, 'GiB')).toBe(1.5);
    expect(inUnit(300, 'MiB')).toBe(300);
    expect(toMiB(1.5, 'GiB')).toBe(1536);
    expect(toMiB(2, 'GiB')).toBe(2048);
    expect(toMiB(300, 'MiB')).toBe(300);
    // Fractions of a MiB are rounded: the settings hold whole MiB.
    expect(toMiB(0.001, 'GiB')).toBe(1);
    expect(toMiB(10.4, 'MiB')).toBe(10);
  });

  it('formats sizes for people', () => {
    expect(formatMiB(2048, 'en-GB')).toBe('2 GiB');
    expect(formatMiB(1536, 'en-GB')).toBe('1.5 GiB');
    expect(formatMiB(1536, 'de-DE')).toBe('1,5 GiB');
    expect(formatMiB(100, 'en-GB')).toBe('100 MiB');
  });
});
