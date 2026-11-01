// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/** The separators of CSV files: a comma, or a semicolon as spreadsheets in German expect. */
export type Separator = ',' | ';';

/**
 * A cell of a CSV file. Texts that a spreadsheet would run as a formula (starting with =, +, -, @, a tab or a
 * carriage return) get a leading apostrophe; cells with separators, quotes or line breaks are quoted.
 */
export function csvCell(value: string, sep: Separator): string {
  let v = value;
  if (/^[=+\-@\t\r]/.test(v)) v = `'${v}`;
  if (v.includes(sep) || /["\r\n]/.test(v)) v = `"${v.replace(/"/g, '""')}"`;
  return v;
}

/** A CSV file (RFC 4180 with CRLF lines) with a byte order mark, so that spreadsheets read it as UTF-8. */
export function toCSV(header: string[], rows: string[][], sep: Separator): string {
  const line = (cells: string[]) => cells.map((c) => csvCell(c, sep)).join(sep);
  return `\uFEFF${[header, ...rows].map(line).join('\r\n')}\r\n`;
}

/** Offers a text as a file to save. */
export function download(name: string, text: string, type = 'text/csv;charset=utf-8') {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** A file name from a title: letters, digits, dashes and dots only. */
export function fileName(title: string, extension: string): string {
  const base = title
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^A-Za-z0-9._-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80);
  return `${base || 'issues'}.${extension}`;
}
