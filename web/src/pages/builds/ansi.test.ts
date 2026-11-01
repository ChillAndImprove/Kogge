// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { LogParser, sections } from './ansi';

describe('LogParser', () => {
  it('reads colours, markers and carriage returns across chunks', () => {
    const p = new LogParser();
    p.push('##[step] Build\n\x1b[1;32mok\x1b[0m pla');
    p.push('in\n\x1b[31mred\nstill red\x1b[39m\n');
    p.push('10%\r50%\r100%\n##[error] boom\n');
    p.push('unfinished');
    const lines = p.all();
    expect(lines.map((l) => l.kind)).toEqual(['step', 'text', 'text', 'text', 'text', 'error', 'text']);
    expect(lines[0].segments[0].text).toBe('Build');
    expect(lines[1].segments).toEqual([
      { text: 'ok', bold: true, fg: 'var(--log-green)' },
      { text: ' plain' },
    ]);
    expect(lines[2].segments[0]).toMatchObject({ text: 'red', fg: 'var(--log-red)' });
    expect(lines[3].segments[0]).toMatchObject({ text: 'still red', fg: 'var(--log-red)' });
    expect(lines[4].segments.map((s) => s.text).join('')).toBe('100%');
    expect(lines[6].segments[0].text).toBe('unfinished');
    // The unfinished line does not change the parser's state.
    p.push(' now\n');
    expect(
      p
        .all()[6]
        .segments.map((s) => s.text)
        .join(''),
    ).toBe('unfinished now');
  });

  it('understands 256 and true colours and strips other escapes', () => {
    const p = new LogParser();
    p.push('\x1b[38;5;196mA\x1b[48;2;1;2;3mB\x1b[0m\x1b[2K\x1b]0;title\x07C\n');
    const segs = p.all()[0].segments;
    expect(segs[0]).toMatchObject({ text: 'A', fg: 'rgb(255, 0, 0)' });
    expect(segs[1]).toMatchObject({ text: 'B', bg: 'rgb(1, 2, 3)' });
    expect(segs.map((s) => s.text).join('')).toBe('ABC');
  });

  it('groups lines into steps', () => {
    const p = new LogParser();
    p.push('##[info] start\n##[step] One\na\nb\n##[step] Two\nc\n');
    const s = sections(p.all());
    expect(s.map((x) => [x.title?.segments[0].text ?? null, x.start, x.lines.length])).toEqual([
      [null, 0, 1],
      ['One', 1, 2],
      ['Two', 4, 1],
    ]);
  });
});
