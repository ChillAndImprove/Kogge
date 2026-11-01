// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Box,
  ColorSwatch,
  Divider,
  Group,
  Menu,
  Paper,
  Popover,
  SegmentedControl,
  Tooltip,
} from '@mantine/core';
import {
  IconArrowBackUp,
  IconArrowForwardUp,
  IconBlockquote,
  IconBold,
  IconCode,
  IconH1,
  IconH2,
  IconH3,
  IconInfoCircle,
  IconItalic,
  IconLink,
  IconList,
  IconListCheck,
  IconListNumbers,
  IconMathFunction,
  IconPalette,
  IconPhoto,
  IconPlus,
  IconSeparatorHorizontal,
  IconSourceCode,
  IconStrikethrough,
  IconTable,
  IconUnderline,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import type { AnyExtension } from '@tiptap/core';
import type { Schema } from '@tiptap/pm/model';
import { EditorContent, useEditor, useEditorState, type Editor as TiptapEditor } from '@tiptap/react';
import type { Transaction } from '@tiptap/pm/state';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { useOptionalSession } from '../../app/session';
import { latexOf } from '../math/latex';
import type { Doc } from './doc';
import type { DocContext } from './DocView';
import { editorExtensions } from './extensions';
import { FindReplacePanel } from './FindReplacePanel';
import { FormulaDialog } from './FormulaDialog';
import { ExpandDialog, palette, StatusDialog } from './InsertDialogs';
import { LinkBubble, LinkForm } from './LinkEditor';
import { looksLikeMarkdown, pasteMarkdown } from './markdownPaste';
import { MacroDialog } from './MacroDialog';
import { flushMarkdown, MarkdownMode } from './MarkdownMode';
import { selectedFormula } from './math';
import { MacroRender } from './preview';
import { onSlashRequest, requestDialog, type SlashRequest } from './SlashMenu';
import { ColumnsTools } from './ColumnsTools';
import { TableTools } from './TableTools';
import './editor.css';

/** Uploads a file (images pasted or dropped into the editor) and returns where it is stored. */
export type UploadFile = (
  file: File,
) => Promise<{ id: number; url: string; contentType: string; filename: string }>;

const issueKeyPattern = /^(?:.*\/browse\/)?([A-Z][A-Z0-9_]{1,9}-[1-9][0-9]{0,9})$/;

export { isEmptyDoc, type Doc } from './doc';

function Button({
  label,
  active,
  onClick,
  children,
  disabled,
}: {
  label: string;
  active?: boolean;
  onClick: () => void;
  children: ReactNode;
  disabled?: boolean;
}) {
  return (
    <Tooltip label={label} openDelay={500}>
      <ActionIcon
        variant={active ? 'light' : 'subtle'}
        color={active ? undefined : 'gray'}
        size="md"
        onMouseDown={(e) => e.preventDefault()}
        onClick={onClick}
        aria-label={label}
        aria-pressed={active}
        disabled={disabled}
      >
        {children}
      </ActionIcon>
    </Tooltip>
  );
}

async function insertUpload(editor: TiptapEditor, upload: UploadFile, file: File) {
  try {
    const at = await upload(file);
    if (at.contentType.startsWith('image/')) {
      editor
        .chain()
        .focus()
        .setImage({ src: at.url, alt: at.filename })
        .updateAttributes('image', { attachment: at.id })
        .run();
    } else {
      editor
        .chain()
        .focus()
        .insertContent({
          type: 'text',
          text: at.filename,
          marks: [{ type: 'link', attrs: { href: at.url } }],
        })
        .run();
    }
  } catch (e) {
    const { notifyError } = await import('../notify');
    notifyError(e);
  }
}

type Dialog = 'macro' | 'mermaid' | 'expand' | 'status' | 'math' | 'inlineMath' | null;

/** Writing formatted text, or its Markdown beside a preview. */
type EditorMode = 'rich' | 'markdown';

const modeKey = 'kogge.editor.mode';

/** The way of writing the reader chose last (kept in the browser). */
function storedMode(): EditorMode {
  try {
    return localStorage.getItem(modeKey) === 'markdown' ? 'markdown' : 'rich';
  } catch {
    return 'rich';
  }
}

/** The find panel: open (with the text it starts with) or closed; key changes to focus it again. */
type FindPanel = { initial: string; key: number } | null;

function Toolbar({
  editor,
  macros,
  onUploadFile,
  linkOpen,
  setLinkOpen,
  find,
  setFind,
  extra,
}: {
  editor: TiptapEditor;
  macros?: boolean;
  onUploadFile?: UploadFile;
  linkOpen: boolean;
  setLinkOpen: (open: boolean) => void;
  find: FindPanel;
  setFind: (f: FindPanel) => void;
  /** At the end of the toolbar: the switch to writing Markdown, where it is offered. */
  extra?: ReactNode;
}) {
  const { t } = useTranslation();
  const [dialog, setDialog] = useState<Dialog>(null);
  // The LaTeX of the formula that the formula dialog edits; null while it inserts one.
  const [formula, setFormula] = useState<string | null>(null);
  // Where that formula stands, followed through every change while the dialog is open: a double click moves the
  // selection after opening it, and others may write in a shared draft meanwhile.
  const formulaAt = useRef<number | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const s = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      bold: e.isActive('bold'),
      italic: e.isActive('italic'),
      underline: e.isActive('underline'),
      strike: e.isActive('strike'),
      code: e.isActive('code'),
      link: e.isActive('link'),
      h1: e.isActive('heading', { level: 1 }),
      h2: e.isActive('heading', { level: 2 }),
      h3: e.isActive('heading', { level: 3 }),
      bullet: e.isActive('bulletList'),
      ordered: e.isActive('orderedList'),
      task: e.isActive('taskList'),
      quote: e.isActive('blockquote'),
      codeBlock: e.isActive('codeBlock'),
      panel: e.isActive('panel'),
      table: e.isActive('table'),
      columns: e.isActive('columns'),
      formula: e.isActive('math') || e.isActive('inlineMath'),
      editable: e.isEditable,
      canUndo: e.can().undo(),
      canRedo: e.can().redo(),
    }),
  });
  // The "/" menu asks for dialogs and the file picker here; so do formulas (typed, double-clicked, Enter).
  useEffect(
    () =>
      onSlashRequest(editor, (r: SlashRequest) => {
        if (r === 'image') fileInput.current?.click();
        else if (r === 'math' || r === 'inlineMath') {
          // The formula selected is edited; else one is inserted (on a line of its own or in the text).
          const node = selectedFormula(editor);
          formulaAt.current = node ? editor.state.selection.from : null;
          setFormula(node ? latexOf(node.attrs) : null);
          setDialog(node ? (node.type.name as 'math' | 'inlineMath') : r);
        } else setDialog(r);
      }),
    [editor],
  );
  useEffect(() => {
    if (dialog !== 'math' && dialog !== 'inlineMath') return;
    const follow = ({ transaction }: { transaction: Transaction }) => {
      const at = formulaAt.current;
      if (at !== null && transaction.docChanged) formulaAt.current = transaction.mapping.map(at, 1);
    };
    editor.on('transaction', follow);
    return () => {
      editor.off('transaction', follow);
    };
  }, [editor, dialog]);
  const c = () => editor.chain().focus();
  return (
    <div className="kogge-editor-toolbar">
      <Group gap={2} px={6} py={4} wrap="wrap">
        <Button label={t('editor.bold')} active={s.bold} onClick={() => c().toggleBold().run()}>
          <IconBold size={16} />
        </Button>
        <Button label={t('editor.italic')} active={s.italic} onClick={() => c().toggleItalic().run()}>
          <IconItalic size={16} />
        </Button>
        <Button
          label={t('editor.underline')}
          active={s.underline}
          onClick={() => c().toggleUnderline().run()}
        >
          <IconUnderline size={16} />
        </Button>
        <Button label={t('editor.strike')} active={s.strike} onClick={() => c().toggleStrike().run()}>
          <IconStrikethrough size={16} />
        </Button>
        <Button label={t('editor.code')} active={s.code} onClick={() => c().toggleCode().run()}>
          <IconCode size={16} />
        </Button>
        <Popover
          opened={linkOpen}
          onChange={setLinkOpen}
          position="bottom-start"
          withArrow
          trapFocus
          shadow="md"
        >
          <Popover.Target>
            <Tooltip label={t('editor.link')} openDelay={500}>
              <ActionIcon
                variant={s.link ? 'light' : 'subtle'}
                color={s.link ? undefined : 'gray'}
                size="md"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => setLinkOpen(!linkOpen)}
                aria-label={t('editor.link')}
                aria-pressed={s.link}
              >
                <IconLink size={16} />
              </ActionIcon>
            </Tooltip>
          </Popover.Target>
          <Popover.Dropdown>
            {linkOpen && <LinkForm editor={editor} onDone={() => setLinkOpen(false)} />}
          </Popover.Dropdown>
        </Popover>
        <Divider orientation="vertical" />
        <Button label={t('editor.h1')} active={s.h1} onClick={() => c().toggleHeading({ level: 1 }).run()}>
          <IconH1 size={16} />
        </Button>
        <Button label={t('editor.h2')} active={s.h2} onClick={() => c().toggleHeading({ level: 2 }).run()}>
          <IconH2 size={16} />
        </Button>
        <Button label={t('editor.h3')} active={s.h3} onClick={() => c().toggleHeading({ level: 3 }).run()}>
          <IconH3 size={16} />
        </Button>
        <Divider orientation="vertical" />
        <Button label={t('editor.bulletList')} active={s.bullet} onClick={() => c().toggleBulletList().run()}>
          <IconList size={16} />
        </Button>
        <Button
          label={t('editor.orderedList')}
          active={s.ordered}
          onClick={() => c().toggleOrderedList().run()}
        >
          <IconListNumbers size={16} />
        </Button>
        <Button label={t('editor.taskList')} active={s.task} onClick={() => c().toggleTaskList().run()}>
          <IconListCheck size={16} />
        </Button>
        <Button label={t('editor.quote')} active={s.quote} onClick={() => c().toggleBlockquote().run()}>
          <IconBlockquote size={16} />
        </Button>
        <Button
          label={t('editor.codeBlock')}
          active={s.codeBlock}
          onClick={() => c().toggleCodeBlock().run()}
        >
          <IconSourceCode size={16} />
        </Button>
        <Button
          label={t('editor.panel')}
          active={s.panel}
          onClick={() => (s.panel ? c().lift('panel').run() : c().wrapIn('panel', { kind: 'info' }).run())}
        >
          <IconInfoCircle size={16} />
        </Button>
        <Button
          label={t('editor.table')}
          active={s.table}
          onClick={() => c().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()}
          disabled={s.table}
        >
          <IconTable size={16} />
        </Button>
        <Button label={t('editor.rule')} onClick={() => c().setHorizontalRule().run()}>
          <IconSeparatorHorizontal size={16} />
        </Button>
        <Button
          label={t('editor.formula')}
          active={s.formula}
          onClick={() => requestDialog(editor, 'inlineMath')}
          disabled={s.codeBlock || s.code}
        >
          <IconMathFunction size={16} />
        </Button>
        <Menu position="bottom-start" withinPortal>
          <Menu.Target>
            <ActionIcon
              variant="subtle"
              color="gray"
              size="md"
              aria-label={t('editor.color')}
              onMouseDown={(e) => e.preventDefault()}
            >
              <IconPalette size={16} />
            </ActionIcon>
          </Menu.Target>
          <Menu.Dropdown>
            <Group gap={4} p={4} maw={180}>
              {palette.map((color) => (
                <ColorSwatch
                  key={color}
                  component="button"
                  type="button"
                  color={`var(--mantine-color-${color}-7)`}
                  size={20}
                  aria-label={t(`colors.${color}`)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => c().setMark('textColor', { color }).run()}
                />
              ))}
            </Group>
            <Menu.Item onClick={() => c().unsetMark('textColor').run()}>{t('editor.defaultColor')}</Menu.Item>
          </Menu.Dropdown>
        </Menu>
        {onUploadFile && (
          <>
            <Button label={t('editor.image')} onClick={() => fileInput.current?.click()}>
              <IconPhoto size={16} />
            </Button>
            <input
              ref={fileInput}
              type="file"
              accept="image/*"
              hidden
              onChange={(e) => {
                const file = e.currentTarget.files?.[0];
                e.currentTarget.value = '';
                if (file) void insertUpload(editor, onUploadFile, file);
              }}
            />
          </>
        )}
        {macros && (
          <Menu position="bottom-start" withinPortal>
            <Menu.Target>
              <ActionIcon
                variant="subtle"
                color="gray"
                size="md"
                aria-label={t('editor.insert')}
                onMouseDown={(e) => e.preventDefault()}
              >
                <IconPlus size={16} />
              </ActionIcon>
            </Menu.Target>
            <Menu.Dropdown>
              <Menu.Item onClick={() => setDialog('macro')}>{t('editor.macro')}</Menu.Item>
              <Menu.Item onClick={() => setDialog('mermaid')}>{t('editor.slash.diagram')}</Menu.Item>
              <Menu.Item onClick={() => setDialog('expand')}>{t('editor.expand')}</Menu.Item>
              <Menu.Item
                onClick={() =>
                  c()
                    .insertContent({
                      type: 'date',
                      attrs: { value: new Date().toISOString().slice(0, 10) },
                    })
                    .run()
                }
              >
                {t('editor.date')}
              </Menu.Item>
              <Menu.Item onClick={() => setDialog('status')}>{t('editor.status')}</Menu.Item>
              <Menu.Item onClick={() => c().toggleCodeBlock().run()}>{t('editor.codeBlock')}</Menu.Item>
              <Menu.Item onClick={() => c().insertColumns(2).run()}>{t('editor.twoColumns')}</Menu.Item>
              <Menu.Item onClick={() => c().insertColumns(3).run()}>{t('editor.threeColumns')}</Menu.Item>
              <Menu.Divider />
              <Menu.Label>{t('editor.slash.hint')}</Menu.Label>
            </Menu.Dropdown>
          </Menu>
        )}
        <Divider orientation="vertical" />
        <Button label={t('editor.undo')} onClick={() => c().undo().run()} disabled={!s.canUndo}>
          <IconArrowBackUp size={16} />
        </Button>
        <Button label={t('editor.redo')} onClick={() => c().redo().run()} disabled={!s.canRedo}>
          <IconArrowForwardUp size={16} />
        </Button>
        {extra && <Box ml="auto">{extra}</Box>}
      </Group>
      {s.table && s.editable && <TableTools editor={editor} />}
      {s.columns && s.editable && <ColumnsTools editor={editor} />}
      {find && (
        <FindReplacePanel
          editor={editor}
          initial={find.initial}
          focusKey={find.key}
          onClose={() => setFind(null)}
        />
      )}
      {(dialog === 'macro' || dialog === 'mermaid') && (
        <MacroDialog
          value={null}
          preset={dialog === 'mermaid' ? 'mermaid' : undefined}
          onClose={() => setDialog(null)}
          onSave={(v) =>
            c()
              .insertContent({ type: 'macro', attrs: { name: v.name, params: v.params } })
              .run()
          }
        />
      )}
      {dialog === 'expand' && (
        <ExpandDialog
          onClose={() => setDialog(null)}
          onInsert={(title) => c().wrapIn('expand', { title }).run()}
        />
      )}
      {dialog === 'status' && (
        <StatusDialog
          onClose={() => setDialog(null)}
          onInsert={(status) => c().insertContent({ type: 'status', attrs: status }).run()}
        />
      )}
      {(dialog === 'math' || dialog === 'inlineMath') && (
        <FormulaDialog
          value={formula}
          display={dialog === 'math'}
          onClose={() => setDialog(null)}
          onSave={(f) => {
            // The formula opened, where it still is (others may have removed it meanwhile); else a new one.
            const at = formulaAt.current;
            if (formula === null || at === null || !c().updateMath(f.latex, at).run()) {
              c().insertMath(f.latex, f.display).run();
            }
          }}
        />
      )}
    </div>
  );
}

interface EditorProps {
  value: Doc | null | undefined;
  onChange?: (doc: Doc) => void;
  placeholder?: string;
  autofocus?: boolean;
  minHeight?: number;
  /** Called with Ctrl/Cmd+Enter. */
  onSubmit?: () => void;
  /** Offers macros and other wiki content in the toolbar. */
  macros?: boolean;
  /** Stores pasted or dropped files; without it, files cannot be added. */
  onUploadFile?: UploadFile;
  /** The accessible name of the editing area; default: the placeholder. */
  label?: string;
  /** Renders macros as readers see them (live preview); without it, macros show their parameters. */
  renderMacro?: DocContext['macro'];
  /** Edits a shared draft with others: these extensions connect it (see collab.ts). */
  collaboration?: AnyExtension[];
  /** Receives the editor once it exists. */
  onReady?: (editor: TiptapEditor) => void;
  /**
   * The shared draft holds content this editor does not know (Kogge was updated while the page was open): the
   * editor stays read-only and is not handed to onReady, so that nothing of it reaches the draft.
   */
  onOutdated?: () => void;
}

/** Whether a schema knows every node and mark of a document. */
function readable(schema: Schema, doc: Doc | null | undefined): boolean {
  try {
    if (doc) schema.nodeFromJSON(doc);
    return true;
  } catch {
    return false;
  }
}

/** The rich-text editor (TipTap) used for issues, comments and pages. */
export function Editor({
  value,
  onChange,
  placeholder,
  autofocus,
  minHeight = 120,
  onSubmit,
  macros,
  onUploadFile,
  label,
  renderMacro,
  collaboration,
  onReady,
  onOutdated,
}: EditorProps) {
  const { t } = useTranslation();
  const session = useOptionalSession();
  const externalImages = session?.me.externalImages ?? false;
  // A pasted key becomes an issue only for the projects the user sees: "UTF-8" or "SHA-256" look like keys, too.
  const projects = useQuery({
    queryKey: ['projects'],
    queryFn: () => unwrap(api.GET('/api/v1/projects')),
    enabled: !!session,
    staleTime: 5 * 60_000,
  });
  const projectKeys = useRef(new Set<string>());
  useEffect(() => {
    projectKeys.current = new Set((projects.data ?? []).map((p) => p.key));
  }, [projects.data]);
  const editorRef = useRef<TiptapEditor | null>(null);
  // Pages can be written as Markdown, too; comments and descriptions stay formatted.
  const [mode, setMode] = useState<EditorMode>(() => (macros ? storedMode() : 'rich'));
  const [linkOpen, setLinkOpen] = useState(false);
  const [find, setFind] = useState<FindPanel>(null);
  const outdated = useRef(false);
  const editor = useEditor({
    extensions: [
      ...editorExtensions(placeholder ?? t('editor.placeholder'), {
        collaborative: !!collaboration,
        wiki: macros,
        images: !!onUploadFile,
        externalImages,
      }),
      ...(collaboration ?? []),
    ],
    content: value ?? undefined,
    // A shared draft is checked as it loads. One with nodes or marks that this editor does not know would become an
    // empty document, and the changes made to that would land in the draft at the wrong places. Content that the
    // schema only orders differently (documents made through the API) loads as before.
    enableContentCheck: !!collaboration,
    onContentError: ({ editor: e }) => {
      if (!readable(e.schema, value)) outdated.current = true;
    },
    autofocus: autofocus ? 'end' : false,
    immediatelyRender: true,
    onUpdate: ({ editor: e }) => onChange?.(e.getJSON()),
    editorProps: {
      attributes: { 'aria-label': label ?? placeholder ?? t('editor.placeholder'), 'aria-multiline': 'true' },
      handleKeyDown: (view, event) => {
        if (onSubmit && event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
          onSubmit();
          return true;
        }
        if (
          (event.ctrlKey || event.metaKey) &&
          !event.shiftKey &&
          !event.altKey &&
          event.key.toLowerCase() === 'k'
        ) {
          setLinkOpen(true);
          return true;
        }
        // Ctrl/Cmd+F searches the text being edited, starting with what is selected (within a block).
        if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === 'f') {
          const { from, to, $from } = view.state.selection;
          const selected =
            from < to && $from.sameParent(view.state.doc.resolve(to))
              ? view.state.doc.textBetween(from, to).slice(0, 200)
              : '';
          setFind((f) => ({ initial: selected || (f?.initial ?? ''), key: (f?.key ?? 0) + 1 }));
          return true;
        }
        return false;
      },
      // A pasted issue key or issue URL of a known project becomes an issue node; pasted or dropped files are
      // uploaded.
      handlePaste: (view, event) => {
        const files = Array.from(event.clipboardData?.files ?? []);
        if (files.length && onUploadFile && editorRef.current) {
          for (const f of files) void insertUpload(editorRef.current, onUploadFile, f);
          return true;
        }
        const text = event.clipboardData?.getData('text/plain')?.trim() ?? '';
        const m = issueKeyPattern.exec(text);
        if (m && projectKeys.current.has(m[1].slice(0, m[1].lastIndexOf('-')))) {
          const node = view.state.schema.nodes.issue.create({ key: m[1] });
          view.dispatch(view.state.tr.replaceSelectionWith(node).scrollIntoView());
          return true;
        }
        // Markdown pasted as text (pages copied from elsewhere bring HTML) becomes formatted, except in code.
        const raw = event.clipboardData?.getData('text/plain') ?? '';
        if (
          editorRef.current &&
          !event.clipboardData?.types.includes('text/html') &&
          !view.state.selection.$from.parent.type.spec.code &&
          looksLikeMarkdown(raw)
        ) {
          pasteMarkdown(editorRef.current, raw, externalImages);
          return true;
        }
        return false;
      },
      handleDrop: (_view, event) => {
        const files = Array.from((event as DragEvent).dataTransfer?.files ?? []);
        if (files.length && onUploadFile && editorRef.current) {
          event.preventDefault();
          for (const f of files) void insertUpload(editorRef.current, onUploadFile, f);
          return true;
        }
        return false;
      },
    },
  });
  useEffect(() => {
    editorRef.current = editor;
    if (!editor) return;
    if (outdated.current) {
      editor.setEditable(false);
      onOutdated?.();
    } else onReady?.(editor);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editor]);
  useEffect(() => {
    if (editor && value === null && !editor.isEmpty) editor.commands.clearContent();
  }, [editor, value]);
  if (!editor) return null;
  const chooseMode = async (m: EditorMode) => {
    // Back to formatted writing with all that was typed as Markdown.
    if (m === 'rich') await flushMarkdown(editor);
    setMode(m);
    try {
      localStorage.setItem(modeKey, m);
    } catch {
      // storage unavailable — the choice lasts for this page only
    }
  };
  // Pages switch between writing formatted and Markdown; the switch ends either toolbar.
  const switcher = macros ? (
    <SegmentedControl
      size="xs"
      value={mode}
      onChange={(v) => void chooseMode(v as EditorMode)}
      aria-label={t('editor.mode')}
      data={[
        { value: 'rich', label: t('editor.modeRich') },
        { value: 'markdown', label: t('editor.modeMarkdown') },
      ]}
    />
  ) : undefined;
  return (
    <Paper withBorder className="kogge-editor">
      {mode === 'markdown' ? (
        <MarkdownMode
          editor={editor}
          externalImages={externalImages}
          context={renderMacro ? { macro: renderMacro } : undefined}
          switcher={switcher}
        />
      ) : (
        <Toolbar
          editor={editor}
          macros={macros}
          onUploadFile={onUploadFile}
          linkOpen={linkOpen}
          setLinkOpen={setLinkOpen}
          find={find}
          setFind={setFind}
          extra={switcher}
        />
      )}
      <MacroRender.Provider value={renderMacro}>
        <EditorContent
          editor={editor}
          className="kogge-doc kogge-editor-content"
          style={{ minHeight, display: mode === 'markdown' ? 'none' : undefined }}
        />
      </MacroRender.Provider>
      <LinkBubble editor={editor} />
    </Paper>
  );
}
