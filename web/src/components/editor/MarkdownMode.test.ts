// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { beyondMarkdown } from './MarkdownMode';

describe('beyondMarkdown', () => {
  it('finds nothing in what Markdown holds', () => {
    const doc = {
      type: 'doc',
      content: [
        {
          type: 'heading',
          attrs: { level: 2 },
          content: [{ type: 'text', text: 'A', marks: [{ type: 'bold' }] }],
        },
        { type: 'taskList', content: [{ type: 'taskItem', content: [{ type: 'paragraph' }] }] },
        { type: 'codeBlock', attrs: { language: 'go' }, content: [{ type: 'text', text: 'x := 1' }] },
      ],
    };
    expect(beyondMarkdown(doc)).toBe(false);
  });

  it('finds nothing in formulas: Markdown holds them as $…$ and $$ blocks', () => {
    const formulas = [
      {
        type: 'paragraph',
        content: [
          { type: 'text', text: 'Area ' },
          { type: 'inlineMath', attrs: { latex: '\\pi r^2' } },
        ],
      },
      { type: 'math', attrs: { latex: '\\int_0^1 x\\,dx' } },
    ];
    expect(beyondMarkdown({ type: 'doc', content: formulas })).toBe(false);
    const panel = { type: 'panel', content: [{ type: 'paragraph' }] };
    expect(beyondMarkdown({ type: 'doc', content: [...formulas, panel] })).toBe(true);
  });

  it('finds macros, panels and colours, also deep inside', () => {
    expect(beyondMarkdown({ type: 'doc', content: [{ type: 'macro', attrs: { name: 'toc' } }] })).toBe(true);
    const panel = { type: 'panel', content: [{ type: 'paragraph' }] };
    expect(beyondMarkdown({ type: 'doc', content: [{ type: 'blockquote', content: [panel] }] })).toBe(true);
    const coloured = { type: 'text', text: 'red', marks: [{ type: 'textColor', attrs: { color: 'red' } }] };
    expect(beyondMarkdown({ type: 'doc', content: [{ type: 'paragraph', content: [coloured] }] })).toBe(true);
  });
});
