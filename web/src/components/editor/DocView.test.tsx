// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import '../../i18n';
import { DocView, type DocContext } from './DocView';
import type { Doc } from './Editor';

// jsdom has no media queries, which Mantine asks for the colour scheme.
window.matchMedia ??= (query: string) =>
  ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }) as MediaQueryList;

function view(doc: Doc, context?: DocContext) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <DocView doc={doc} context={context} />
      </QueryClientProvider>
    </MantineProvider>,
  );
}

const text = (t: string) => ({ type: 'text', text: t });
const para = (t: string) => ({ type: 'paragraph', content: [text(t)] });

describe('DocView', () => {
  it('shows panels with the icon of their kind, and unknown kinds as information', () => {
    const { container } = view({
      type: 'doc',
      content: [
        { type: 'panel', attrs: { kind: 'warning' }, content: [para('Careful')] },
        { type: 'panel', attrs: { kind: 'shout' }, content: [para('Loud')] },
      ],
    } as Doc);
    const panels = container.querySelectorAll('.kogge-panel');
    expect(panels).toHaveLength(2);
    expect(panels[0].getAttribute('data-kind')).toBe('warning');
    expect(panels[0].querySelector('svg.kogge-panel-icon')).not.toBeNull();
    expect(panels[0].querySelector('.kogge-panel-body')?.textContent).toBe('Careful');
    expect(panels[1].getAttribute('data-kind')).toBe('info');
  });

  it('colours table cells only with colours of the palette', () => {
    const { container } = view({
      type: 'doc',
      content: [
        {
          type: 'table',
          content: [
            {
              type: 'tableRow',
              content: [
                { type: 'tableHeader', attrs: { background: 'blue' }, content: [para('Head')] },
                {
                  type: 'tableCell',
                  attrs: { background: 'red;background:url(x)' },
                  content: [para('Cell')],
                },
              ],
            },
          ],
        },
      ],
    } as Doc);
    expect(container.querySelector('.kogge-doc-table table')).not.toBeNull();
    expect(container.querySelector('th')?.getAttribute('data-bg')).toBe('blue');
    expect(container.querySelector('td')?.hasAttribute('data-bg')).toBe(false);
  });

  it('shows code blocks with their title, a copy button and the code as text', () => {
    const { container, getByRole } = view({
      type: 'doc',
      content: [
        {
          type: 'codeBlock',
          attrs: { language: 'html', title: 'Snippet' },
          content: [text('<script>alert(1)</script>')],
        },
      ],
    } as Doc);
    expect(container.querySelector('.kogge-codeblock figcaption')?.textContent).toBe('Snippet');
    expect(container.querySelector('pre code')?.textContent).toBe('<script>alert(1)</script>');
    expect(container.querySelector('script')).toBeNull();
    expect(getByRole('button', { name: 'Copy' })).toBeTruthy();
  });

  it('gives headings an id and a link to themselves', () => {
    const { container } = view({
      type: 'doc',
      content: [{ type: 'heading', attrs: { level: 2 }, content: [text('Rolling back')] }],
    } as Doc);
    const h = container.querySelector('h2');
    expect(h?.id).toBe('h-rolling-back');
    expect(h?.querySelector('a.kogge-heading-anchor')?.getAttribute('href')).toBe('#h-rolling-back');
  });

  it('shows columns side by side with the widths of their layout, and unknown layouts as equal', () => {
    const { container } = view({
      type: 'doc',
      content: [
        {
          type: 'columns',
          attrs: { layout: 'left' },
          content: [
            { type: 'column', content: [para('Side')] },
            { type: 'column', content: [para('Main')] },
          ],
        },
        {
          type: 'columns',
          attrs: { layout: 'width:999px' },
          content: [
            { type: 'column', content: [para('A')] },
            { type: 'column', content: [para('B')] },
          ],
        },
      ],
    } as Doc);
    const layouts = container.querySelectorAll('.kogge-columns');
    expect([...layouts].map((l) => l.getAttribute('data-layout'))).toEqual(['left', 'equal']);
    const cols = layouts[0].querySelectorAll(':scope > .kogge-columns-row > .kogge-column');
    expect([...cols].map((c) => c.textContent)).toEqual(['Side', 'Main']);
  });

  it('shows dates as days of the language', () => {
    const { container } = view({
      type: 'doc',
      content: [{ type: 'paragraph', content: [{ type: 'date', attrs: { value: '2026-10-07' } }] }],
    } as Doc);
    const time = container.querySelector('time');
    expect(time?.getAttribute('datetime')).toBe('2026-10-07');
    expect(time?.textContent).toMatch(/2026/);
  });
  it('shows images within text, linked where they are, and images from elsewhere only where allowed', () => {
    const doc = {
      type: 'doc',
      content: [
        {
          type: 'paragraph',
          content: [
            text('Build '),
            {
              type: 'inlineImage',
              attrs: { src: '/badge.svg', alt: 'build', href: 'https://ci.example.com' },
            },
            text(' chart '),
            { type: 'inlineImage', attrs: { src: 'https://charts.example.com/c.png', alt: 'Chart' } },
            { type: 'inlineImage', attrs: { src: 'javascript:alert(1)', alt: 'Bad' } },
          ],
        },
        { type: 'image', attrs: { src: 'https://charts.example.com/big.png', alt: 'Big' } },
      ],
    } as Doc;
    const { container, unmount } = view(doc);
    let imgs = [...container.querySelectorAll('img')];
    expect(imgs.map((i) => i.getAttribute('src'))).toEqual(['/badge.svg']);
    expect(imgs[0].closest('a')?.getAttribute('href')).toBe('https://ci.example.com');
    expect(imgs[0].closest('a')?.getAttribute('target')).toBe('_blank');
    expect(imgs[0].getAttribute('referrerpolicy')).toBe('no-referrer');
    // Not shown: their descriptions stay.
    expect(container.textContent).toContain('Chart');
    expect(container.textContent).toContain('Bad');
    unmount();

    imgs = [...view(doc, { externalImages: true }).container.querySelectorAll('img')];
    expect(imgs.map((i) => i.getAttribute('src'))).toEqual([
      '/badge.svg',
      'https://charts.example.com/c.png',
      'https://charts.example.com/big.png',
    ]);
  });
});
