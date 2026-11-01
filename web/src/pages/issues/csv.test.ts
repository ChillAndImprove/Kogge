// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { csvCell, fileName, toCSV } from './csv';

describe('CSV', () => {
  it('quotes what needs quoting', () => {
    expect(csvCell('plain', ',')).toBe('plain');
    expect(csvCell('a, b', ',')).toBe('"a, b"');
    expect(csvCell('a, b', ';')).toBe('a, b');
    expect(csvCell('1,5', ';')).toBe('1,5');
    expect(csvCell('x;y', ';')).toBe('"x;y"');
    expect(csvCell('say "hi"', ',')).toBe('"say ""hi"""');
    expect(csvCell('two\nlines', ',')).toBe('"two\nlines"');
  });

  it('keeps spreadsheets from running cells as formulas', () => {
    expect(csvCell('=HYPERLINK("http://evil")', ',')).toBe(`"'=HYPERLINK(""http://evil"")"`);
    expect(csvCell('+1', ',')).toBe("'+1");
    expect(csvCell('-2', ',')).toBe("'-2");
    expect(csvCell('@SUM(A1)', ',')).toBe("'@SUM(A1)");
    expect(csvCell('\tcmd', ',')).toBe("'\tcmd");
  });

  it('writes a file with a byte order mark and CRLF lines', () => {
    expect(toCSV(['Key', 'Summary'], [['SHOP-1', 'Checkout, fast']], ',')).toBe(
      '\uFEFFKey,Summary\r\nSHOP-1,"Checkout, fast"\r\n',
    );
  });

  it('names files safely', () => {
    expect(fileName('Offene Vorgänge / Team Süd', 'csv')).toBe('Offene-Vorgange-Team-Sud.csv');
    expect(fileName('***', 'csv')).toBe('issues.csv');
  });
});
