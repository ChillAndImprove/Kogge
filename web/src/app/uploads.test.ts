// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { attachable, tooLarge } from './uploads';

const file = (name: string, size: number) => new File([new Uint8Array(size)], name);

describe('attachments over the limit', () => {
  it('refuses files larger than the limit before they are sent', () => {
    expect(tooLarge(file('small.txt', 1024), 1)).toBeNull();
    expect(tooLarge(file('exact.bin', 1024 * 1024), 1)).toBeNull();
    expect(tooLarge(file('big.bin', 1024 * 1024 + 1), 1)?.message).toContain('big.bin');
    // Without a known limit the server decides.
    expect(tooLarge(file('big.bin', 1024 * 1024 + 1), undefined)).toBeNull();
  });

  it('keeps the files that fit and reports the others', () => {
    const refused: string[] = [];
    const kept = attachable([file('a.txt', 10), file('b.bin', 2 * 1024 * 1024), file('c.txt', 20)], 1, (e) =>
      refused.push(e.message),
    );
    expect(kept.map((f) => f.name)).toEqual(['a.txt', 'c.txt']);
    expect(refused).toHaveLength(1);
    expect(refused[0]).toContain('b.bin');
  });
});
