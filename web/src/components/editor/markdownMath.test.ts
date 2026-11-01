// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { highlightTree, tagHighlighter, tags } from '@lezer/highlight';
import { GFM, parser } from '@lezer/markdown';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { MathMarkdown, mathTag } from './markdownMath';

const markdown = parser.configure([GFM, MathMarkdown]);

/** The nodes of the given names a text parses into, each with its text: "InlineMath:$x$". */
function nodes(src: string, names = ['InlineMath', 'BlockMath', 'MathText']): string[] {
  const out: string[] = [];
  markdown.parse(src).iterate({
    enter(n) {
      if (names.includes(n.name)) out.push(`${n.name}:${src.slice(n.from, n.to)}`);
    },
  });
  return out;
}

/** The parts of a text a highlighter styles, each with its class: "x^2=math". */
function styled(src: string, rules: { tag: typeof tags.monospace; class: string }[]): string[] {
  const out: string[] = [];
  highlightTree(markdown.parse(src), tagHighlighter(rules), (from, to, cls) =>
    out.push(`${src.slice(from, to)}=${cls}`),
  );
  return out;
}

describe('formulas within a line', () => {
  it.each([
    ['Euler: $e^{i\\pi}+1=0$.', ['InlineMath:$e^{i\\pi}+1=0$', 'MathText:e^{i\\pi}+1=0']],
    ['$x\\$y$', ['InlineMath:$x\\$y$', 'MathText:x\\$y']],
    ['$a$$b$', ['InlineMath:$a$', 'MathText:a', 'InlineMath:$b$', 'MathText:b']],
    ['a $$x$$ b', ['InlineMath:$$x$$', 'MathText:x']],
    ['$$x$$ is famous', ['InlineMath:$$x$$', 'MathText:x']],
    ['$n$th', ['InlineMath:$n$', 'MathText:n']],
    ['$\\{x\\}$', ['InlineMath:$\\{x\\}$', 'MathText:\\{x\\}']],
    ['US\\$$x$', ['InlineMath:$x$', 'MathText:x']],
    ['$x$&#50;', ['InlineMath:$x$', 'MathText:x']],
    // GitHub's other form: the server leaves the backticks out of the LaTeX.
    ['$`a+b`$', ['InlineMath:$`a+b`$', 'MathText:`a+b`']],
    // As on the server: a shell variable before another one reads as a formula.
    ['$HOME/$USER', ['InlineMath:$HOME/$', 'MathText:HOME/']],
    ['# Formula $x^2$', ['InlineMath:$x^2$', 'MathText:x^2']],
    ['| A |\n| --- |\n| $\\|x\\|$ |', ['InlineMath:$\\|x\\|$', 'MathText:\\|x\\|']],
    // Link destinations are not formulas.
    ['[a](c$d$e) $f$', ['InlineMath:$f$', 'MathText:f']],
  ])('%j', (src, want) => {
    expect(nodes(src)).toEqual(want);
  });

  it.each([
    ['$5 and $10'],
    ['from $5-$10'],
    ['$x$5'],
    ['$ x$'],
    ['$x $'],
    ['\\$x$'],
    ['`$x$`'],
    ['costs $$'],
    ['$$$'],
    ['$$x$'],
    ['costs $$x$ now'],
    ['$a\nb$'],
    ['$$x\ny$$'],
    // A backslash at the end of a line escapes nothing in the next one.
    ['$a\\\nb$'],
    ['see https://example.com/a$b$c ok'],
    ['```\n$x$\n```'],
    ['    $x$'],
  ])('%j has none', (src) => {
    expect(nodes(src)).toEqual([]);
  });

  it('keeps emphasis, links and code out of a formula', () => {
    expect(nodes('$a_1$ and $b_1$ and $c*d*e$', ['InlineMath', 'Emphasis'])).toEqual([
      'InlineMath:$a_1$',
      'InlineMath:$b_1$',
      'InlineMath:$c*d*e$',
    ]);
    expect(nodes('$a`b$ c`', ['InlineMath', 'InlineCode'])).toEqual(['InlineMath:$a`b$']);
    expect(nodes('`a$`b$', ['InlineMath', 'InlineCode'])).toEqual(['InlineCode:`a$`']);
    expect(nodes('\\$x$', ['Escape', 'InlineMath'])).toEqual(['Escape:\\$']);
  });
});

describe('formulas of their own', () => {
  it.each([
    ['$$\n\\int_0^1 x\\,dx\n$$', ['BlockMath:$$\n\\int_0^1 x\\,dx\n$$', 'MathText:\\int_0^1 x\\,dx']],
    ['$$ x^2 $$', ['BlockMath:$$ x^2 $$', 'MathText: x^2 ']],
    ['  $$x$$  ', ['BlockMath:$$x$$  ', 'MathText:x']],
    // A paragraph ends where a formula starts.
    ['Text\n$$\nx\n$$\nmore', ['BlockMath:$$\nx\n$$', 'MathText:x']],
    ['a\n$$x$$\nb', ['BlockMath:$$x$$', 'MathText:x']],
    ['$$\na\n\nb\n$$', ['BlockMath:$$\na\n\nb\n$$', 'MathText:a', 'MathText:b']],
    [
      '$$\n\\begin{aligned}\na&=b\\\\\n\\end{aligned}$$',
      [
        'BlockMath:$$\n\\begin{aligned}\na&=b\\\\\n\\end{aligned}$$',
        'MathText:\\begin{aligned}',
        'MathText:a&=b\\\\',
        'MathText:\\end{aligned}',
      ],
    ],
    ['$$\n\n  x\n\n$$', ['BlockMath:$$\n\n  x\n\n$$', 'MathText:  x']],
    // An escaped dollar does not close the formula; a line break of TeX before the closing dollars does.
    ['$$\na \\$$\nb\n$$', ['BlockMath:$$\na \\$$\nb\n$$', 'MathText:a \\$$', 'MathText:b']],
    ['$$\na \\\\$$\nafter', ['BlockMath:$$\na \\\\$$', 'MathText:a \\\\']],
    // Without a closing line, the formula goes on to the end.
    ['$$\nx\ny', ['BlockMath:$$\nx\ny', 'MathText:x', 'MathText:y']],
    ['$$\n$$', ['BlockMath:$$\n$$']],
  ])('%j', (src, want) => {
    expect(nodes(src)).toEqual(want);
  });

  it('reads no Markdown within a formula', () => {
    const src = '$$\n- a\n# b\n---\n$$';
    expect(nodes(src)).toEqual(['BlockMath:' + src, 'MathText:- a', 'MathText:# b', 'MathText:---']);
    expect(nodes(src, ['BulletList', 'ATXHeading1', 'HorizontalRule', 'SetextHeading2'])).toEqual([]);
  });

  // Only a line of "$$" opens a formula of lines.
  it.each([
    ['$$ dollars'],
    ['$$5 a month\n\nmore'],
    ['$$\\begin{aligned}\na&=b\n\\end{aligned}$$'],
    ['$$$\nx\n$$$'],
    ['    $$\n    x\n    $$'],
    ['a $$\nx'],
  ])('%j is none', (src) => {
    expect(nodes(src, ['BlockMath'])).toEqual([]);
  });

  it('goes on as long as its quote or list item does', () => {
    expect(nodes('> $$\n> x\n>\n> y\n> $$\n\nafter')).toEqual([
      'BlockMath:$$\n> x\n>\n> y\n> $$',
      'MathText:x',
      'MathText:y',
    ]);
    expect(nodes('> $$\n> x\n\nafter $y$')).toEqual([
      'BlockMath:$$\n> x',
      'MathText:x',
      'InlineMath:$y$',
      'MathText:y',
    ]);
    expect(nodes('- $$\n  x\n  $$\n- z', ['ListItem', 'BlockMath'])).toEqual([
      'ListItem:- $$\n  x\n  $$',
      'BlockMath:$$\n  x\n  $$',
      'ListItem:- z',
    ]);
    expect(nodes('- $$\n  x\n\nnot in the list', ['BlockMath', 'MathText', 'Paragraph'])).toEqual([
      'BlockMath:$$\n  x\n',
      'MathText:x',
      'Paragraph:not in the list',
    ]);
  });

  it('ends a quote that the next line does not go on with', () => {
    expect(nodes('> a\n$$\nx\n$$', ['Blockquote', 'BlockMath'])).toEqual([
      'Blockquote:> a',
      'BlockMath:$$\nx\n$$',
    ]);
  });
});

/**
 * A formula of the corpus: its LaTeX within the text (inline) or on lines of its own (display). source is what the
 * Markdown holds of an inline one where the server reads the LaTeX from it differently (pipes escaped in a table's
 * cell, GitHub's $`…`$).
 */
interface Formula {
  inline?: string;
  display?: string;
  source?: string;
}

// Markdown and the formulas the server reads in it: its tests read the same file (TestMarkdownMathCorpus).
const corpus = JSON.parse(
  readFileSync(resolve(__dirname, '../../../../internal/docmodel/testdata/markdown_math.json'), 'utf8'),
) as { note: string; markdown: string; formulas: Formula[] }[];

/** The lines of a formula of its own without the spaces around them, and without blank lines: the highlighting
 * marks the LaTeX of each line, not the indentation that the server leaves out. */
const latexLines = (latex: string) =>
  latex
    .split('\n')
    .map((l) => l.replace(/^[ \t]+|[ \t]+$/g, ''))
    .filter((l) => l)
    .join('\n');

/** The formulas the highlighting finds, as the server's are written in the corpus. */
function formulas(src: string): Formula[] {
  const out: Formula[] = [];
  markdown.parse(src).iterate({
    enter(n) {
      // The server reads the description of an image as text.
      if (n.name === 'Image') return false;
      if (n.name === 'InlineMath') {
        const text = n.node.getChild('MathText');
        out.push({ inline: text ? src.slice(text.from, text.to) : '' });
      } else if (n.name === 'BlockMath') {
        const latex = n.node
          .getChildren('MathText')
          .map((t) => src.slice(t.from, t.to))
          .join('\n');
        // The server keeps no empty formulas.
        if (latexLines(latex)) out.push({ display: latexLines(latex) });
      }
    },
  });
  return out;
}

describe('the formulas the server reads', () => {
  it.each(corpus.map((c) => [c.note, c.markdown, c.formulas] as const))('%s: %j', (_, src, want) => {
    expect(formulas(src)).toEqual(
      want.map((f) =>
        f.display !== undefined ? { display: latexLines(f.display) } : { inline: f.source ?? f.inline },
      ),
    );
  });
});

describe('highlighting', () => {
  const src = 'a $x^2$ b\n\n$$\n\\int x\n$$\n';

  it('shows the dollars as marks and the LaTeX as a formula', () => {
    const rules = [
      { tag: mathTag, class: 'math' },
      { tag: tags.monospace, class: 'mono' },
      { tag: tags.processingInstruction, class: 'mark' },
    ];
    expect(styled(src, rules)).toEqual([
      '$=mark',
      'x^2=math',
      '$=mark',
      '$$=mark',
      '\\int x=math',
      '$$=mark',
    ]);
  });

  it('shows formulas as code where a style knows nothing of them', () => {
    const rules = [
      { tag: tags.monospace, class: 'mono' },
      { tag: tags.processingInstruction, class: 'mark' },
    ];
    expect(styled(src, rules)).toEqual([
      '$=mark',
      'x^2=mono',
      '$=mark',
      '$$=mark',
      '\\int x=mono',
      '$$=mark',
    ]);
  });
});

describe('long texts', () => {
  // Each formula is looked for up to the next dollar only, so these take as long as their length.
  it.each([
    ['openers', '$a '.repeat(100000)],
    ['amounts', '$5-$1 '.repeat(50000)],
    ['dollar runs', '$$x '.repeat(80000)],
    ['escaped dollars', '$' + 'a\\$'.repeat(100000)],
    ['display openers', '$$ a\n'.repeat(60000)],
    ['an unclosed formula', '$$\n' + 'a\n'.repeat(150000)],
    ['many formulas', '$$\nx\n$$\n'.repeat(40000)],
    ['backticks', '$`'.repeat(150000)],
  ])('%s', (_, src) => {
    expect(markdown.parse(src).length).toBe(src.length);
  });
});
