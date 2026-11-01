// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { formatBytes } from './format';

describe('formatBytes', () => {
  it('scales to the largest fitting unit', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1536)).toMatch(/^1[.,]5 KiB$/);
    expect(formatBytes(5 * 1024 ** 3)).toBe('5 GiB');
  });
});
