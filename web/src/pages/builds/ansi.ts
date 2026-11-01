// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Parses job logs: ANSI colours (SGR), carriage returns of progress output, and the markers of the agent
// (##[step], ##[info], ##[error]).

export type Segment = {
  text: string;
  fg?: string;
  bg?: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
};
export type LogLine = { kind: 'text' | 'step' | 'info' | 'error'; segments: Segment[] };

type Style = Omit<Segment, 'text'>;

const basic = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white'];

/** A colour of the 256-colour palette as CSS. */
function color256(n: number): string | undefined {
  if (n < 8) return `var(--log-${basic[n]})`;
  if (n < 16) return `var(--log-bright-${basic[n - 8]})`;
  if (n < 232) {
    const v = n - 16;
    const c = (x: number) => (x === 0 ? 0 : 55 + x * 40);
    return `rgb(${c(Math.floor(v / 36))}, ${c(Math.floor(v / 6) % 6)}, ${c(v % 6)})`;
  }
  if (n < 256) {
    const g = 8 + (n - 232) * 10;
    return `rgb(${g}, ${g}, ${g})`;
  }
  return undefined;
}

function applySGR(style: Style, params: number[]): Style {
  const s = { ...style };
  if (params.length === 0) params = [0];
  for (let i = 0; i < params.length; i++) {
    const p = params[i];
    if (p === 0) {
      for (const k of Object.keys(s)) delete s[k as keyof Style];
    } else if (p === 1) s.bold = true;
    else if (p === 2) s.dim = true;
    else if (p === 3) s.italic = true;
    else if (p === 4) s.underline = true;
    else if (p === 22) {
      s.bold = false;
      s.dim = false;
    } else if (p === 23) s.italic = false;
    else if (p === 24) s.underline = false;
    else if (p >= 30 && p <= 37) s.fg = `var(--log-${basic[p - 30]})`;
    else if (p >= 90 && p <= 97) s.fg = `var(--log-bright-${basic[p - 90]})`;
    else if (p >= 40 && p <= 47) s.bg = `var(--log-${basic[p - 40]})`;
    else if (p >= 100 && p <= 107) s.bg = `var(--log-bright-${basic[p - 100]})`;
    else if (p === 39) s.fg = undefined;
    else if (p === 49) s.bg = undefined;
    else if (p === 38 || p === 48) {
      let c: string | undefined;
      if (params[i + 1] === 5) {
        c = color256(params[i + 2]);
        i += 2;
      } else if (params[i + 1] === 2) {
        c = `rgb(${params[i + 2] ?? 0}, ${params[i + 3] ?? 0}, ${params[i + 4] ?? 0})`;
        i += 4;
      }
      if (p === 38) s.fg = c;
      else s.bg = c;
    }
  }
  return s;
}

// CSI sequences (colours and cursor movement) and OSC sequences (titles, links).
// eslint-disable-next-line no-control-regex
const escapes = /\x1b\[([0-9;?]*)([A-Za-z])|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Z0-9]|\x1b[=>]/g;

/** Splits a line into styled segments; the style carries over to the next line. */
function parseLine(text: string, style: Style): { segments: Segment[]; style: Style } {
  // Progress output rewrites the line: only the text after the last carriage return stays.
  const cr = text.lastIndexOf('\r');
  if (cr >= 0) text = text.slice(cr + 1);
  const segments: Segment[] = [];
  let last = 0;
  let current = style;
  for (const m of text.matchAll(escapes)) {
    if (m.index > last) segments.push({ ...current, text: text.slice(last, m.index) });
    if (m[2] === 'm') {
      current = applySGR(
        current,
        (m[1] ?? '')
          .split(';')
          .filter((x) => x !== '')
          .map(Number),
      );
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) segments.push({ ...current, text: text.slice(last) });
  return { segments, style: current };
}

const markers: [string, LogLine['kind']][] = [
  ['##[step] ', 'step'],
  ['##[info] ', 'info'],
  ['##[error] ', 'error'],
];

/** Turns log text into lines; feed it chunk by chunk. The unfinished last line is kept until more arrives. */
export class LogParser {
  lines: LogLine[] = [];
  private partial = '';
  private style: Style = {};

  push(chunk: string) {
    const text = this.partial + chunk;
    const parts = text.split('\n');
    this.partial = parts.pop() ?? '';
    for (const p of parts) this.lines.push(this.line(p));
  }

  /** The lines including the unfinished one. */
  all(): LogLine[] {
    if (!this.partial) return this.lines;
    const s = this.style;
    const l = this.line(this.partial);
    this.style = s;
    return [...this.lines, l];
  }

  private line(raw: string): LogLine {
    for (const [mark, kind] of markers) {
      if (raw.startsWith(mark)) return { kind, segments: [{ text: raw.slice(mark.length) }] };
    }
    const { segments, style } = parseLine(raw.replace(/\r$/, ''), this.style);
    this.style = style;
    return { kind: 'text', segments };
  }
}

/** Groups lines into sections that start at step headers. */
export function sections(lines: LogLine[]) {
  const out: { title: LogLine | null; start: number; lines: LogLine[] }[] = [
    { title: null, start: 0, lines: [] },
  ];
  lines.forEach((l, i) => {
    if (l.kind === 'step') out.push({ title: l, start: i, lines: [] });
    else out[out.length - 1].lines.push(l);
  });
  return out.filter((s, i) => i > 0 || s.lines.length > 0);
}
