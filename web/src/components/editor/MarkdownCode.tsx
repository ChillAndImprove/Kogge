// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { defaultKeymap, history, historyKeymap, redo, undo } from '@codemirror/commands';
import {
  defineLanguageFacet,
  HighlightStyle,
  Language,
  LanguageSupport,
  syntaxHighlighting,
} from '@codemirror/language';
import { highlightSelectionMatches, openSearchPanel, search, searchKeymap } from '@codemirror/search';
import { EditorState } from '@codemirror/state';
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
  placeholder,
  type KeyBinding,
} from '@codemirror/view';
import { tags } from '@lezer/highlight';
import { GFM, parser } from '@lezer/markdown';
import { useEffect, useRef } from 'react';
import { markdownTransaction, type MarkdownAction, type MarkdownWords } from './markdownActions';
import { MathMarkdown, mathTag } from './markdownMath';

/** What the toolbar around the source can do with it. */
export interface MarkdownCodeApi {
  run: (action: MarkdownAction) => void;
  undo: () => void;
  redo: () => void;
  search: () => void;
  focus: () => void;
}

/**
 * Markdown as GitHub writes it (tables, task lists, strikethrough, links without brackets, formulas), from the
 * parser alone: CodeMirror's Markdown package would bring parsers for HTML, CSS and JavaScript within it as well.
 */
const markdownSupport = new LanguageSupport(
  new Language(
    defineLanguageFacet({ commentTokens: { block: { open: '<!--', close: '-->' } } }),
    parser.configure([GFM, MathMarkdown]),
    [],
    'markdown',
  ),
);

/*
 * Markdown's marks are dimmed, what they mark shows as it will read: headings and bold in bold, emphasis in
 * italics, links underlined, code on a shade, formulas on it in a colour of their own. Weight, slant, lines and
 * the shade carry the meaning, so that it shows without colour, too.
 */
const markdownStyle = HighlightStyle.define([
  { tag: tags.heading, fontWeight: '700', color: 'var(--kogge-md-heading)' },
  { tag: tags.strong, fontWeight: '700', color: 'var(--kogge-md-heading)' },
  { tag: tags.emphasis, fontStyle: 'italic' },
  { tag: tags.strikethrough, textDecoration: 'line-through' },
  { tag: tags.link, textDecoration: 'underline', textUnderlineOffset: '3px' },
  { tag: tags.url, color: 'var(--kogge-md-mark)', textDecoration: 'underline', textUnderlineOffset: '3px' },
  { tag: tags.monospace, color: 'var(--kogge-md-code)', backgroundColor: 'var(--kogge-md-code-bg)' },
  { tag: mathTag, color: 'var(--kogge-md-math)', backgroundColor: 'var(--kogge-md-code-bg)' },
  { tag: tags.quote, fontStyle: 'italic', color: 'var(--kogge-md-quote)' },
  { tag: [tags.processingInstruction, tags.meta, tags.contentSeparator], color: 'var(--kogge-md-mark)' },
  { tag: [tags.labelName, tags.atom], color: 'var(--kogge-md-mark)' },
]);

/** The source looks like a code editor of the app: its fonts and colours, a gutter, the line being edited marked. */
const theme = EditorView.theme({
  '&': {
    height: '100%',
    fontSize: '13px',
    color: 'var(--mantine-color-text)',
    backgroundColor: 'transparent',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--mantine-font-family-monospace)', lineHeight: '1.7', overflow: 'auto' },
  '.cm-content': { padding: '10px 0 40px', caretColor: 'var(--mantine-color-text)' },
  '.cm-line': { padding: '0 16px 0 12px' },
  '.cm-gutters': {
    backgroundColor: 'var(--kogge-md-gutter)',
    color: 'var(--mantine-color-dimmed)',
    border: 'none',
    borderRight: '1px solid var(--mantine-color-default-border)',
  },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 14px', minWidth: '36px' },
  '.cm-activeLine': { backgroundColor: 'var(--kogge-md-active)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--kogge-md-active)', color: 'var(--mantine-color-text)' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--mantine-color-text)', borderLeftWidth: '2px' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground': {
    backgroundColor: 'var(--kogge-md-selection)',
  },
  '.cm-selectionMatch': { backgroundColor: 'var(--kogge-md-match)' },
  '.cm-placeholder': { color: 'var(--mantine-color-dimmed)', fontStyle: 'italic' },
  '.cm-panels': {
    backgroundColor: 'var(--mantine-color-body)',
    color: 'var(--mantine-color-text)',
    fontFamily: 'var(--mantine-font-family)',
  },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--mantine-color-default-border)' },
  '.cm-search': { padding: '6px 10px', fontSize: '12px' },
  '.cm-search input, .cm-search button': {
    font: 'inherit',
    color: 'inherit',
    background: 'var(--mantine-color-default)',
    border: '1px solid var(--mantine-color-default-border)',
    borderRadius: '4px',
    padding: '2px 6px',
  },
  '.cm-searchMatch': { backgroundColor: 'var(--kogge-md-match)', outline: '1px solid var(--kogge-md-mark)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--kogge-md-selection)' },
});

/** Ctrl/Cmd+B, I, K and E mark text as in the formatted editor; Ctrl/Cmd+Alt+1 to 3 make headings. */
function markdownKeys(run: (action: MarkdownAction) => boolean): KeyBinding[] {
  const key = (k: string, action: MarkdownAction): KeyBinding => ({
    key: k,
    run: () => run(action),
    preventDefault: true,
  });
  return [
    key('Mod-b', 'bold'),
    key('Mod-i', 'italic'),
    key('Mod-k', 'link'),
    key('Mod-e', 'code'),
    key('Mod-Alt-1', 'h1'),
    key('Mod-Alt-2', 'h2'),
    key('Mod-Alt-3', 'h3'),
  ];
}

/**
 * The Markdown source in a code editor (CodeMirror): line numbers, the marks highlighted, undo, search
 * (Ctrl/Cmd+F) and the keys of the toolbar. It reports each change and how far it is scrolled (0 to 1).
 */
export function MarkdownCode({
  initial,
  label,
  placeholderText,
  words,
  phrases,
  onChange,
  onScroll,
  onReady,
}: {
  initial: string;
  label: string;
  placeholderText: string;
  words: MarkdownWords;
  /** The texts of the search panel in the reader's language. */
  phrases: Record<string, string>;
  onChange: (text: string) => void;
  onScroll?: (share: number) => void;
  onReady?: (api: MarkdownCodeApi) => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const handlers = useRef({ onChange, onScroll, onReady, words });
  useEffect(() => {
    handlers.current = { onChange, onScroll, onReady, words };
  });
  // One editor for the life of the component; what it reports goes to the latest handlers.
  useEffect(() => {
    const parent = host.current;
    if (!parent) return;
    let view: EditorView | null = null;
    const run = (action: MarkdownAction) => {
      if (!view) return false;
      const spec = markdownTransaction(view.state, action, handlers.current.words);
      if (spec) view.dispatch(view.state.update(spec, { scrollIntoView: true, userEvent: 'input.format' }));
      view.focus();
      return true;
    };
    view = new EditorView({
      parent,
      state: EditorState.create({
        doc: initial,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightActiveLine(),
          history(),
          drawSelection(),
          EditorView.lineWrapping,
          markdownSupport,
          syntaxHighlighting(markdownStyle),
          search({ top: true }),
          highlightSelectionMatches(),
          placeholder(placeholderText),
          EditorState.phrases.of(phrases),
          keymap.of([...markdownKeys(run), ...searchKeymap, ...historyKeymap, ...defaultKeymap]),
          EditorView.contentAttributes.of({ 'aria-label': label, spellcheck: 'false' }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) handlers.current.onChange(u.state.doc.toString());
          }),
          theme,
        ],
      }),
    });
    const scroller = view.scrollDOM;
    const scrolled = () => {
      const room = scroller.scrollHeight - scroller.clientHeight;
      handlers.current.onScroll?.(room > 0 ? scroller.scrollTop / room : 0);
    };
    scroller.addEventListener('scroll', scrolled, { passive: true });
    const current = view;
    handlers.current.onReady?.({
      run: (action) => void run(action),
      undo: () => void (undo(current), current.focus()),
      redo: () => void (redo(current), current.focus()),
      search: () => void openSearchPanel(current),
      focus: () => current.focus(),
    });
    current.focus();
    return () => {
      scroller.removeEventListener('scroll', scrolled);
      current.destroy();
      view = null;
    };
    // The editor is made once: later values of the props reach it through the handlers.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return <div ref={host} className="kogge-markdown-code" />;
}
