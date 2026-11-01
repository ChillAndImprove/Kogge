// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Badge,
  Box,
  Button,
  Card,
  Checkbox,
  CopyButton,
  Group,
  Loader,
  SegmentedControl,
  Stack,
  Text,
  TextInput,
  Tooltip,
  UnstyledButton,
} from '@mantine/core';
import { useMediaQuery } from '@mantine/hooks';
import {
  IconArrowsVertical,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconChevronUp,
  IconChevronsDown,
  IconChevronsUp,
  IconCopy,
  IconFileCode,
  IconFolder,
  IconLayoutColumns,
  IconLayoutRows,
  IconLayoutSidebar,
  IconLayoutSidebarLeftCollapse,
  IconMessagePlus,
  IconSearch,
  type Icon,
} from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../../api/client';
import { CommentForm, escapeHtml } from './components';
import { diffGaps, gapLines, type Gap, type Shown } from './diffGaps';
import { lineAnchor, splitRows, type DiffLine } from './diffRows';
import { repoParams, srcPath, type Diff, type DiffFile, type PRComment, type Repository } from './hooks';
import { markHtml, wordChanges, type Range } from './wordDiff';
import './code.css';

/** Where a new line comment goes. */
export interface LineTarget {
  path: string;
  line: number;
  side: 'old' | 'new';
}

interface DiffViewProps {
  repo: Repository;
  diff: Diff;
  /** The ref the "view file" links open (the new side). */
  viewRef?: string;
  /** The commit whose files show the lines a diff leaves out (the new side); by default the view's ref. */
  contentRef?: string;
  /** Under which name the browser keeps the files the reader marked as viewed; without it, files have no mark. */
  viewedKey?: string;
  /** Comments on lines and files, shown below their line. */
  comments?: PRComment[];
  /** Renders a thread of comments. */
  renderThread?: (comments: PRComment[]) => ReactNode;
  /** Adds a comment; when set, line numbers can be clicked to comment. */
  onComment?: (target: LineTarget | { path: string }, markdown: string, task: boolean) => Promise<unknown>;
}

type Hunk = DiffFile['hunks'][number];

function lineKey(path: string, side: string, line: number) {
  return `${path}\u0000${side}\u0000${line}`;
}

function filePath(f: DiffFile) {
  return f.newPath || f.oldPath || '';
}

function fileId(path: string) {
  return `file-${path}`;
}

function stored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function store(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    // storage unavailable — the choice lasts for this page only
  }
}

type DiffMode = 'unified' | 'split';

const diffModeKey = 'kogge.code.diffView';
const treeKey = 'kogge.code.diffTree';

/** How diffs are shown, as the reader chose last (kept in the browser). */
function useDiffMode(): [DiffMode, (m: DiffMode) => void] {
  const [mode, setMode] = useState<DiffMode>(() => (stored(diffModeKey) === 'split' ? 'split' : 'unified'));
  const choose = (m: DiffMode) => {
    setMode(m);
    store(diffModeKey, m);
  };
  return [mode, choose];
}

/** Whether the list of files stands beside the changes, as the reader chose last. */
function useTreeOpen(): [boolean, (open: boolean) => void] {
  const [open, setOpen] = useState(() => stored(treeKey) !== 'hidden');
  const choose = (o: boolean) => {
    setOpen(o);
    store(treeKey, o ? null : 'hidden');
  };
  return [open, choose];
}

/** A file's changes in a few characters: a file stays marked as viewed while its changes stay the same. */
function fingerprint(f: DiffFile): string {
  let h = 0x811c9dc5;
  const add = (s: string) => {
    for (let i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      h = Math.imul(h, 0x01000193);
    }
  };
  add(
    `${f.status}\u0000${f.oldPath ?? ''}\u0000${f.newPath ?? ''}\u0000${f.oldMode ?? ''}\u0000${f.newMode ?? ''}`,
  );
  for (const hunk of f.hunks) for (const l of hunk.lines) add(`\u0000${l.kind}${l.text}`);
  return `${(h >>> 0).toString(36)}.${f.additions}.${f.deletions}`;
}

function readMarks(storageKey: string | undefined): Record<string, string> {
  if (!storageKey) return {};
  try {
    const v: unknown = JSON.parse(stored(storageKey) ?? '{}');
    return v && typeof v === 'object' ? (v as Record<string, string>) : {};
  } catch {
    return {};
  }
}

/** The files the reader marked as viewed, kept in the browser per diff. */
function useViewed(key: string | undefined, files: DiffFile[]) {
  const storageKey = key && `kogge.code.viewed.${key}`;
  const prints = useMemo(() => new Map(files.map((f) => [filePath(f), fingerprint(f)])), [files]);
  const [state, setState] = useState(() => ({ key: storageKey, marks: readMarks(storageKey) }));
  if (state.key !== storageKey) setState({ key: storageKey, marks: readMarks(storageKey) });
  const marks = state.key === storageKey ? state.marks : {};
  const isViewed = (path: string) =>
    !!storageKey && marks[path] !== undefined && marks[path] === prints.get(path);
  const setViewed = (path: string, yes: boolean) => {
    if (!storageKey) return;
    const next = { ...marks };
    if (yes) next[path] = prints.get(path) ?? '';
    else delete next[path];
    store(storageKey, Object.keys(next).length ? JSON.stringify(next) : null);
    setState({ key: storageKey, marks: next });
  };
  return {
    enabled: !!storageKey,
    isViewed,
    setViewed,
    count: files.filter((f) => isViewed(filePath(f))).length,
  };
}

/** The height of the page's header, below which the heads of files stick. */
function headerOffset() {
  return (
    parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--app-shell-header-offset')) || 52
  );
}

/** The file whose changes are at the top of the window. */
function useActiveFile(paths: string[], enabled: boolean) {
  const [active, setActive] = useState<string>();
  useEffect(() => {
    if (!enabled) return;
    let frame = 0;
    const update = () => {
      frame = 0;
      const top = headerOffset() + 48;
      let current = paths[0];
      for (const p of paths) {
        const el = document.getElementById(fileId(p));
        if (!el) continue;
        if (el.getBoundingClientRect().top > top) break;
        current = p;
      }
      setActive(current);
    };
    const later = () => {
      if (!frame) frame = requestAnimationFrame(update);
    };
    update();
    window.addEventListener('scroll', later, { passive: true });
    window.addEventListener('resize', later);
    return () => {
      window.removeEventListener('scroll', later);
      window.removeEventListener('resize', later);
      if (frame) cancelAnimationFrame(frame);
    };
  }, [paths, enabled]);
  return active;
}

/** Lock files and minified code: their changes start hidden, as do changes of more lines than this. */
const generated =
  /(^|\/)(package-lock\.json|npm-shrinkwrap\.json|yarn\.lock|pnpm-lock\.yaml|go\.sum|Cargo\.lock|composer\.lock|poetry\.lock|Pipfile\.lock|Gemfile\.lock|uv\.lock|flake\.lock)$|\.min\.(js|css)$|\.map$/;
const largeChange = 1000;

/** Why a file's changes start hidden, if they do. */
function hiddenAtFirst(f: DiffFile): 'generated' | 'large' | undefined {
  if (generated.test(filePath(f))) return 'generated';
  if (f.additions + f.deletions > largeChange) return 'large';
  return undefined;
}

/** Lines the expand buttons show at a time. */
const step = 20;

/** The lines a diff leaves out of a file, as far as they can be shown. */
interface GapState {
  gaps: Gap[];
  shown: Record<number, Shown>;
  /** The new version's highlighted lines, once loaded, and whether they are all of them. */
  content?: { lines: string[]; complete: boolean };
  loading: boolean;
  failed: boolean;
  expand: (gap: number, how: 'below' | 'above' | 'all') => void;
}

interface GapPlan {
  /** Lines shown below the hunk above (or at the top of the file). */
  top: DiffLine[];
  /** Lines shown above the hunk below. */
  bottom: DiffLine[];
  /** Lines still hidden; after the last hunk not known before the file is loaded. */
  hidden?: number;
}

function planGap(state: GapState, gi: number): GapPlan | undefined {
  const gap = state.gaps[gi];
  const c = state.content;
  if (!c) {
    return {
      top: [],
      bottom: [],
      hidden: gap.end === undefined ? undefined : Math.max(0, gap.end - gap.start),
    };
  }
  if (gap.end === undefined && !c.complete) return undefined;
  const lines = gapLines(gap, state.shown[gi] ?? { top: 0, bottom: 0 }, c.lines.length);
  if (!lines) return undefined;
  const line = (n: number): DiffLine => ({
    kind: 'context',
    old: n + gap.offset,
    new: n,
    text: '',
    html: c.lines[n - 1] ?? '',
  });
  return { top: lines.top.map(line), bottom: lines.bottom.map(line), hidden: lines.hidden };
}

/** What the tables of a file's changes need: its comments, the line comment being written, marks and gaps. */
interface FileLines {
  path: string;
  file: DiffFile;
  byLine: Map<string, PRComment[]>;
  thread: (list: PRComment[]) => ReactNode;
  target: LineTarget | { path: string } | null;
  setTarget: (t: LineTarget | { path: string } | null) => void;
  onComment?: DiffViewProps['onComment'];
  /** The changed words of removed and added lines. */
  marks: Map<DiffLine, Range[]>;
  /** The lines left out, where they can be shown. */
  gaps?: GapState;
}

/** A line number; where comments can be made, it starts one (by mouse, and by keyboard where focusable). */
function LineNumber({
  no,
  className = 'ln',
  onComment,
  focusable,
  label,
}: {
  no?: number;
  className?: string;
  onComment?: () => void;
  focusable?: boolean;
  label?: string;
}) {
  const keyboard = onComment && focusable;
  return (
    <td
      className={className}
      onClick={onComment}
      tabIndex={keyboard ? 0 : undefined}
      role={keyboard ? 'button' : undefined}
      aria-label={keyboard ? label : undefined}
      onKeyDown={
        keyboard
          ? (e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                onComment();
              }
            }
          : undefined
      }
    >
      {no || ''}
    </td>
  );
}

/** The comments of a line and the form for a new one, if it is being written there. */
function LineComments({ at, l, lines }: { at: LineTarget; l: DiffLine; lines: FileLines }) {
  const { byLine, thread, target, setTarget, onComment } = lines;
  const list = byLine.get(lineKey(at.path, at.side, at.line));
  const writing =
    target &&
    'line' in target &&
    target.path === at.path &&
    target.side === at.side &&
    target.line === at.line;
  if (!list && !writing) return null;
  return (
    <Stack gap="xs">
      {list && thread(list)}
      {writing && onComment && (
        <CommentForm
          suggestLine={at.side === 'new' ? l.text : undefined}
          onCancel={() => setTarget(null)}
          onSubmit={(md, task) => onComment(at, md, task).then(() => setTarget(null))}
        />
      )}
    </Stack>
  );
}

function hasComments(at: LineTarget, lines: FileLines) {
  const { byLine, target } = lines;
  return (
    byLine.has(lineKey(at.path, at.side, at.line)) ||
    (!!target &&
      'line' in target &&
      target.path === at.path &&
      target.side === at.side &&
      target.line === at.line)
  );
}

function sourceHtml(l: DiffLine, marks?: Map<DiffLine, Range[]>) {
  // The server escapes the text; only token spans remain.
  const html = l.html || escapeHtml(l.text);
  const changed = marks?.get(l);
  return { __html: (changed ? markHtml(html, l.text, changed) : html) || ' ' };
}

function hunkHeader(h: Hunk) {
  return `@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@ ${h.header}`;
}

/**
 * The rows of a gap: lines shown below the hunk above, a row with the buttons that show more (and the hunk's
 * header), lines shown above the hunk. Without a gap, only the header.
 */
function gapRows(
  lines: FileLines,
  gi: number,
  split: boolean,
  line: (l: DiffLine, key: string) => ReactNode,
  t: TFunction,
): ReactNode[] {
  const h: Hunk | undefined = lines.file.hunks[gi];
  const header = h && hunkHeader(h);
  const plain = header
    ? [
        <tr key={`h${gi}`} className="hunk">
          {!split && <td className="ln" colSpan={2} />}
          <td colSpan={split ? 6 : 2}>{header}</td>
        </tr>,
      ]
    : [];
  const state = lines.gaps;
  const plan = state && planGap(state, gi);
  if (!state || !plan) return plain;
  const revealed = plan.top.length + plan.bottom.length;
  if (plan.hidden === 0 && revealed === 0) return plain;
  const rows = plan.top.map((l) => line(l, `g${gi}-${l.new}`));
  if (plan.hidden !== 0) {
    const first = gi === 0;
    const last = !h;
    const button = (how: 'below' | 'above' | 'all', label: string, Glyph: Icon) => (
      <Tooltip key={how} label={label}>
        <ActionIcon
          size="xs"
          variant="subtle"
          color="gray"
          aria-label={label}
          onClick={() => state.expand(gi, how)}
        >
          <Glyph size={14} />
        </ActionIcon>
      </Tooltip>
    );
    const all = () =>
      button('all', t('code.diffExpandHidden', { count: plan.hidden ?? 0 }), IconArrowsVertical);
    let buttons: ReactNode[];
    if (plan.hidden === undefined) buttons = [button('below', t('code.diffRestOfFile'), IconChevronDown)];
    else if (plan.hidden <= (first || last ? step : 2 * step)) buttons = [all()];
    else if (first) buttons = [button('above', t('code.diffExpandUp'), IconChevronUp), all()];
    else if (last) buttons = [button('below', t('code.diffExpandDown'), IconChevronDown), all()];
    else {
      buttons = [
        button('below', t('code.diffExpandDown'), IconChevronDown),
        button('above', t('code.diffExpandUp'), IconChevronUp),
        all(),
      ];
    }
    const status = state.failed ? (
      <Text span size="xs" c="dimmed" ml="xs">
        {t('code.diffLinesFailed')}
      </Text>
    ) : state.loading ? (
      <Loader size={12} ml="xs" />
    ) : null;
    const controls = state.failed ? null : <span className="kogge-gap-buttons">{buttons}</span>;
    rows.push(
      <tr key={`h${gi}`} className="hunk gap">
        {split ? (
          <td colSpan={6}>
            {controls}
            {header}
            {status}
          </td>
        ) : (
          <>
            <td className="ln gap-ctl" colSpan={2}>
              {controls}
            </td>
            <td colSpan={2}>
              {header}
              {status}
            </td>
          </>
        )}
      </tr>,
    );
  }
  rows.push(...plan.bottom.map((l) => line(l, `g${gi}-${l.new}`)));
  return rows;
}

/** The changes of a file one below the other: removed lines, then added ones. */
function UnifiedLines({ lines }: { lines: FileLines }) {
  const { t } = useTranslation();
  const { path, file, setTarget, onComment, marks } = lines;
  const context = (l: DiffLine, key: string) => (
    <tr key={key} className="expanded">
      <LineNumber no={l.old} />
      <LineNumber no={l.new} />
      <td className="marker"> </td>
      <td className="src" dangerouslySetInnerHTML={sourceHtml(l)} />
    </tr>
  );
  return (
    <table className="kogge-code">
      <tbody>
        {file.hunks.map((h, hi) => [
          ...gapRows(lines, hi, false, context, t),
          ...h.lines.flatMap((l, li) => {
            const at: LineTarget = { path, ...lineAnchor(l) };
            const click = onComment ? () => setTarget(at) : undefined;
            const label = t(at.side === 'old' ? 'code.pr.commentOldLine' : 'code.pr.commentLine', {
              line: at.line,
            });
            const rows = [
              <tr
                key={`l${hi}-${li}`}
                className={l.kind === 'add' ? 'add' : l.kind === 'delete' ? 'del' : undefined}
              >
                <LineNumber no={l.old} onComment={click} focusable={at.side === 'old'} label={label} />
                <LineNumber no={l.new} onComment={click} focusable={at.side === 'new'} label={label} />
                <td className="marker">{l.kind === 'add' ? '+' : l.kind === 'delete' ? '−' : ' '}</td>
                <td className="src" dangerouslySetInnerHTML={sourceHtml(l, marks)} />
              </tr>,
            ];
            if (hasComments(at, lines)) {
              rows.push(
                <tr key={`c${hi}-${li}`} className="comments">
                  <td colSpan={4}>
                    <LineComments at={at} l={l} lines={lines} />
                  </td>
                </tr>,
              );
            }
            return rows;
          }),
        ])}
        {gapRows(lines, file.hunks.length, false, context, t)}
      </tbody>
    </table>
  );
}

/**
 * The changes of a file side by side: the old version on the left, the new one on the right. Removed lines are
 * commented on the left, added lines and those that stayed on the right — where the unified view anchors them.
 */
function SplitLines({ lines }: { lines: FileLines }) {
  const { t } = useTranslation();
  const { path, file, setTarget, onComment, marks } = lines;
  const cells = (l: DiffLine | undefined, side: 'old' | 'new', at: LineTarget | undefined) => {
    if (!l) {
      return [
        <td key="n" className="ln empty" />,
        <td key="m" className="marker empty" />,
        <td key="s" className="src empty" />,
      ];
    }
    const kind = l.kind === 'add' ? 'add' : l.kind === 'delete' ? 'del' : '';
    const click = onComment && at ? () => setTarget(at) : undefined;
    const label =
      at && t(at.side === 'old' ? 'code.pr.commentOldLine' : 'code.pr.commentLine', { line: at.line });
    return [
      <LineNumber
        key="n"
        no={side === 'old' ? l.old : l.new}
        className={`ln ${kind}`}
        onComment={click}
        // Lines that stayed stand on both sides; the right one takes the comments (and the focus).
        focusable={at?.side === side}
        label={label}
      />,
      <td key="m" className={`marker ${kind}`}>
        {l.kind === 'add' ? '+' : l.kind === 'delete' ? '−' : ' '}
      </td>,
      <td key="s" className={`src ${kind}`} dangerouslySetInnerHTML={sourceHtml(l, marks)} />,
    ];
  };
  const context = (l: DiffLine, key: string) => (
    <tr key={key} className="expanded">
      {cells(l, 'old', undefined)}
      {cells(l, 'new', undefined)}
    </tr>
  );
  return (
    <table className="kogge-code kogge-split">
      <colgroup>
        <col className="ln-col" />
        <col className="marker-col" />
        <col />
        <col className="ln-col" />
        <col className="marker-col" />
        <col />
      </colgroup>
      <tbody>
        {file.hunks.map((h, hi) => [
          ...gapRows(lines, hi, true, context, t),
          ...splitRows(h.lines).flatMap((row, ri) => {
            const left = row.left && { path, ...lineAnchor(row.left) };
            const right = row.right && { path, ...lineAnchor(row.right) };
            // A line that stayed is one line: its number on the left comments it on the right.
            const leftAt = row.left?.kind === 'context' ? right : left;
            const out = [
              <tr key={`l${hi}-${ri}`}>
                {cells(row.left, 'old', leftAt)}
                {cells(row.right, 'new', right)}
              </tr>,
            ];
            const leftComments = left?.side === 'old' && hasComments(left, lines);
            const rightComments = !!right && hasComments(right, lines);
            if (leftComments || rightComments) {
              out.push(
                <tr key={`c${hi}-${ri}`} className="comments">
                  <td colSpan={3}>
                    {leftComments && row.left && left && (
                      <LineComments at={left} l={row.left} lines={lines} />
                    )}
                  </td>
                  <td colSpan={3}>
                    {rightComments && row.right && right && (
                      <LineComments at={right} l={row.right} lines={lines} />
                    )}
                  </td>
                </tr>,
              );
            }
            return out;
          }),
        ])}
        {gapRows(lines, file.hunks.length, true, context, t)}
      </tbody>
    </table>
  );
}

interface TreeDir {
  name: string;
  dirs: Map<string, TreeDir>;
  files: DiffFile[];
}

/** The files in the tree of their directories; a directory that holds only one directory joins it ("a/b"). */
function fileTree(files: DiffFile[]): TreeDir {
  const root: TreeDir = { name: '', dirs: new Map(), files: [] };
  for (const f of files) {
    const parts = filePath(f).split('/');
    let dir = root;
    for (const part of parts.slice(0, -1)) {
      let sub = dir.dirs.get(part);
      if (!sub) {
        sub = { name: part, dirs: new Map(), files: [] };
        dir.dirs.set(part, sub);
      }
      dir = sub;
    }
    dir.files.push(f);
  }
  const join = (d: TreeDir): TreeDir => {
    const dirs = new Map<string, TreeDir>();
    for (let sub of d.dirs.values()) {
      while (sub.files.length === 0 && sub.dirs.size === 1) {
        const [only] = sub.dirs.values();
        sub = { name: `${sub.name}/${only.name}`, dirs: only.dirs, files: only.files };
      }
      dirs.set(sub.name, join(sub));
    }
    return { ...d, dirs };
  };
  return join(root);
}

const statusLetters: Record<string, string> = {
  added: 'A',
  deleted: 'D',
  modified: 'M',
  renamed: 'R',
  copied: 'C',
};

/** The changed files by directory beside the changes, to jump to one; the file at the top of the window stands out. */
function FileTree({
  files,
  active,
  viewed,
  onJump,
}: {
  files: DiffFile[];
  active?: string;
  viewed: (path: string) => boolean;
  onJump: (path: string) => void;
}) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState('');
  const [closed, setClosed] = useState<Record<string, boolean>>({});
  const tree = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return fileTree(q ? files.filter((f) => filePath(f).toLowerCase().includes(q)) : files);
  }, [files, filter]);
  const rows: ReactNode[] = [];
  const walk = (dir: TreeDir, depth: number, prefix: string) => {
    for (const sub of dir.dirs.values()) {
      const key = prefix + sub.name;
      const open = !closed[key];
      rows.push(
        <UnstyledButton
          key={`d:${key}`}
          className="kogge-filetree-row"
          style={{ paddingInlineStart: 4 + depth * 12 }}
          onClick={() => setClosed({ ...closed, [key]: open })}
          aria-expanded={open}
        >
          {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
          <IconFolder size={14} className="kogge-filetree-folder" />
          <span className="kogge-filetree-name">{sub.name}</span>
        </UnstyledButton>,
      );
      if (open) walk(sub, depth + 1, `${key}/`);
    }
    for (const f of dir.files) {
      const path = filePath(f);
      const seen = viewed(path);
      const status = t(`code.status.${f.status}`, { defaultValue: f.status });
      rows.push(
        <UnstyledButton
          key={`f:${path}`}
          className="kogge-filetree-row"
          data-active={active === path || undefined}
          data-viewed={seen || undefined}
          aria-current={active === path ? 'location' : undefined}
          aria-label={[path, status, seen && t('code.diffViewed')].filter(Boolean).join(', ')}
          title={path}
          style={{ paddingInlineStart: 16 + depth * 12 }}
          onClick={() => onJump(path)}
        >
          <span className="kogge-filetree-status" data-status={f.status} aria-hidden>
            {statusLetters[f.status] ?? '?'}
          </span>
          <span className="kogge-filetree-name">{path.slice(path.lastIndexOf('/') + 1)}</span>
          {seen && <IconCheck size={12} className="kogge-filetree-seen" aria-hidden />}
        </UnstyledButton>,
      );
    }
  };
  walk(tree, 0, '');
  return (
    <nav className="kogge-diff-tree" aria-label={t('code.diffFiles')}>
      <TextInput
        size="xs"
        mb={6}
        leftSection={<IconSearch size={14} />}
        placeholder={t('code.diffFilterFiles')}
        aria-label={t('code.diffFilterFiles')}
        value={filter}
        onChange={(e) => setFilter(e.currentTarget.value)}
      />
      {rows.length > 0 ? (
        rows
      ) : (
        <Text size="xs" c="dimmed" px={6}>
          {t('code.diffNoFiles')}
        </Text>
      )}
    </nav>
  );
}

interface FileCardProps {
  repo: Repository;
  file: DiffFile;
  open: boolean;
  onOpen: (open: boolean) => void;
  /** Why the changes are hidden, while they are because of what the file is. */
  hiddenBecause?: 'generated' | 'large';
  /** Whether the reader marked the file as viewed; nothing where files have no mark. */
  viewed?: boolean;
  onViewed: (viewed: boolean) => void;
  mode: DiffMode;
  /** Switches this file between the two layouts, where it can be. */
  onMode?: () => void;
  viewRef?: string;
  contentRef?: string;
  shared: Pick<FileLines, 'byLine' | 'thread' | 'target' | 'setTarget' | 'onComment'>;
}

/** A file of a diff: its head (which stays at the top while its changes scroll), comments and changes. */
function FileCard({
  repo,
  file: f,
  open,
  onOpen,
  hiddenBecause,
  viewed,
  onViewed,
  mode,
  onMode,
  viewRef,
  contentRef,
  shared,
}: FileCardProps) {
  const { t } = useTranslation();
  const path = filePath(f);
  const marks = useMemo(() => {
    const m = new Map<DiffLine, Range[]>();
    for (const h of f.hunks) for (const [l, r] of wordChanges(h.lines)) m.set(l, r);
    return m;
  }, [f]);
  const gaps = useMemo(() => diffGaps(f.hunks), [f]);
  // Lines left out stayed the same on both sides: they come from the new version.
  const expandable =
    !!contentRef && !f.binary && f.hunks.length > 0 && f.status !== 'added' && f.status !== 'deleted';
  const [shown, setShown] = useState<Record<number, Shown>>({});
  const [wanted, setWanted] = useState(false);
  const content = useQuery({
    queryKey: ['repository', repo.id, 'file', contentRef, path],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos/{key}/{slug}/file', {
          params: { path: repoParams(repo), query: { ref: contentRef, path } },
        }),
      ),
    enabled: expandable && wanted,
  });
  const gapState: GapState | undefined = expandable
    ? {
        gaps,
        shown,
        content:
          content.data && !content.data.binary
            ? { lines: content.data.lines, complete: !content.data.truncated }
            : undefined,
        loading: wanted && content.isPending,
        failed: content.isError || !!content.data?.binary,
        expand: (gap, how) => {
          setWanted(true);
          setShown((s) => {
            const cur = s[gap] ?? { top: 0, bottom: 0 };
            const next =
              how === 'all'
                ? { top: Number.MAX_SAFE_INTEGER, bottom: cur.bottom }
                : how === 'below'
                  ? { ...cur, top: cur.top + step }
                  : { ...cur, bottom: cur.bottom + step };
            return { ...s, [gap]: next };
          });
        },
      }
    : undefined;
  const { byLine, thread, target, setTarget, onComment } = shared;
  const lines: FileLines = { path, file: f, ...shared, marks, gaps: gapState };
  const fileComments = byLine.get(lineKey(path, 'file', 0));
  const slash = path.lastIndexOf('/');
  return (
    <Card withBorder padding={0} id={fileId(path)} className="kogge-diff-file">
      <Group
        justify="space-between"
        px="sm"
        py={6}
        wrap="nowrap"
        className="kogge-diff-file-head"
        data-open={open || undefined}
      >
        <Group gap="xs" wrap="nowrap" style={{ minWidth: 0 }}>
          <ActionIcon
            variant="subtle"
            size="sm"
            onClick={() => onOpen(!open)}
            aria-label={path}
            aria-expanded={open}
          >
            {open ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
          </ActionIcon>
          <Text size="sm" ff="monospace" truncate title={path}>
            {slash >= 0 && <span className="kogge-diff-dir">{path.slice(0, slash + 1)}</span>}
            <span className="kogge-diff-name">{path.slice(slash + 1)}</span>
          </Text>
          <CopyButton value={path}>
            {({ copied, copy }) => (
              <Tooltip label={copied ? t('code.diffPathCopied') : t('code.diffCopyPath')}>
                <ActionIcon
                  variant="subtle"
                  color="gray"
                  size="sm"
                  onClick={copy}
                  aria-label={t('code.diffCopyPath')}
                >
                  {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                </ActionIcon>
              </Tooltip>
            )}
          </CopyButton>
          {f.status === 'renamed' && f.oldPath && (
            <Text size="xs" c="dimmed" truncate>
              {t('code.renamedFrom', { path: f.oldPath })}
            </Text>
          )}
          {f.status !== 'modified' && (
            <Badge
              size="xs"
              variant="light"
              color={
                ({ added: 'teal', deleted: 'red', renamed: 'blue' } as Record<string, string>)[f.status] ??
                'gray'
              }
            >
              {t(`code.status.${f.status}`, { defaultValue: f.status })}
            </Badge>
          )}
        </Group>
        <Group gap="xs" wrap="nowrap">
          <Text size="xs" className="kogge-additions">
            +{f.additions}
          </Text>
          <Text size="xs" className="kogge-deletions">
            −{f.deletions}
          </Text>
          {onMode && (
            <Tooltip label={mode === 'split' ? t('code.diffUnifiedFile') : t('code.diffSplitFile')}>
              <ActionIcon
                variant="subtle"
                size="sm"
                color="gray"
                onClick={onMode}
                aria-label={mode === 'split' ? t('code.diffUnifiedFile') : t('code.diffSplitFile')}
              >
                {mode === 'split' ? <IconLayoutRows size={14} /> : <IconLayoutColumns size={14} />}
              </ActionIcon>
            </Tooltip>
          )}
          {onComment && (
            <Tooltip label={t('code.pr.fileComment')}>
              <ActionIcon
                variant="subtle"
                size="sm"
                color="gray"
                onClick={() => setTarget({ path })}
                aria-label={t('code.pr.fileComment')}
              >
                <IconMessagePlus size={14} />
              </ActionIcon>
            </Tooltip>
          )}
          {f.status !== 'deleted' && viewRef && (
            <Tooltip label={t('code.viewFile')}>
              <ActionIcon
                component={Link}
                to={srcPath(repo, viewRef, path)}
                variant="subtle"
                size="sm"
                color="gray"
                aria-label={t('code.viewFile')}
              >
                <IconFileCode size={14} />
              </ActionIcon>
            </Tooltip>
          )}
          {viewed !== undefined && (
            <Checkbox
              size="xs"
              label={t('code.diffViewed')}
              checked={viewed}
              onChange={(e) => onViewed(e.currentTarget.checked)}
            />
          )}
        </Group>
      </Group>
      {!open && hiddenBecause && (
        <Group gap="xs" px="sm" py={6} wrap="nowrap">
          <Text size="xs" c="dimmed">
            {hiddenBecause === 'generated'
              ? t('code.diffGenerated')
              : t('code.diffLarge', { count: f.additions + f.deletions })}
          </Text>
          <Button size="compact-xs" variant="subtle" onClick={() => onOpen(true)}>
            {t('code.diffShowChange')}
          </Button>
        </Group>
      )}
      {open && (
        <>
          {fileComments && <Box p="sm">{thread(fileComments)}</Box>}
          {target && !('line' in target) && target.path === path && onComment && (
            <Box p="sm">
              <CommentForm
                onCancel={() => setTarget(null)}
                onSubmit={(md, task) => onComment(target, md, task).then(() => setTarget(null))}
              />
            </Box>
          )}
          {f.binary && (
            <Text size="sm" c="dimmed" p="sm">
              {t('code.binaryChanged')}
            </Text>
          )}
          {!f.binary && f.hunks.length === 0 && f.oldMode && f.newMode && (
            <Text size="sm" c="dimmed" p="sm">
              {t('code.modeChanged', { from: f.oldMode, to: f.newMode })}
            </Text>
          )}
          {f.hunks.length > 0 && (
            <Box style={{ overflowX: 'auto' }}>
              {mode === 'split' ? <SplitLines lines={lines} /> : <UnifiedLines lines={lines} />}
            </Box>
          )}
          {f.truncated && (
            <Text size="xs" c="orange" p="xs">
              {t('code.fileTruncated')}
            </Text>
          )}
        </>
      )}
    </Card>
  );
}

/**
 * A diff with highlighting and (for pull requests) comments on lines and files, one below the other or side by side
 * (the reader's choice, kept in the browser; files can be switched one by one). Narrow screens show it one below
 * the other. Changed words stand out within their lines, the lines a diff leaves out can be shown, and on wide
 * screens a list of the files stands beside the changes. Where a diff has a `viewedKey`, the reader marks files as
 * viewed.
 */
export function DiffView({
  repo,
  diff,
  viewRef,
  contentRef = viewRef,
  viewedKey,
  comments = [],
  renderThread,
  onComment,
}: DiffViewProps) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [target, setTarget] = useState<LineTarget | { path: string } | null>(null);
  const [mode, setMode] = useDiffMode();
  const [fileModes, setFileModes] = useState<Record<string, DiffMode>>({});
  const [treeOpen, setTreeOpen] = useTreeOpen();
  const narrow = useMediaQuery('(max-width: 48em)');
  const wide = useMediaQuery('(min-width: 75em)');
  const viewed = useViewed(viewedKey, diff.files);
  const paths = useMemo(() => diff.files.map(filePath), [diff.files]);
  const treeFits = !!wide && diff.files.length >= 3;
  const withTree = treeFits && treeOpen;
  const active = useActiveFile(paths, withTree);
  const byLine = useMemo(() => {
    const m = new Map<string, PRComment[]>();
    for (const c of comments) {
      if (c.parentId || !c.path || c.outdated) continue;
      const k = c.line ? lineKey(c.path, c.side ?? 'new', c.line) : lineKey(c.path, 'file', 0);
      m.set(k, [...(m.get(k) ?? []), c]);
    }
    return m;
  }, [comments]);
  const replies = (c: PRComment) => comments.filter((r) => r.parentId === c.id);
  const thread = (list: PRComment[]) => renderThread?.(list.flatMap((c) => [c, ...replies(c)]));
  const additions = diff.files.reduce((n, f) => n + f.additions, 0);
  const deletions = diff.files.reduce((n, f) => n + f.deletions, 0);
  if (diff.files.length === 0) return <Text c="dimmed">{t('code.noChanges')}</Text>;
  const isOpen = (f: DiffFile) => {
    const p = filePath(f);
    return collapsed[p] === undefined ? !(viewed.isViewed(p) || hiddenAtFirst(f)) : !collapsed[p];
  };
  const allOpen = diff.files.every(isOpen);
  const jump = (path: string) => {
    setCollapsed((c) => ({ ...c, [path]: false }));
    requestAnimationFrame(() => document.getElementById(fileId(path))?.scrollIntoView({ block: 'start' }));
  };
  const shared = { byLine, thread, target, setTarget, onComment };
  return (
    <Stack gap="md">
      <Group justify="space-between" gap="xs">
        <Group gap="xs">
          {treeFits && (
            <Tooltip label={treeOpen ? t('code.diffHideTree') : t('code.diffShowTree')}>
              <ActionIcon
                variant="subtle"
                color="gray"
                size="sm"
                onClick={() => setTreeOpen(!treeOpen)}
                aria-label={treeOpen ? t('code.diffHideTree') : t('code.diffShowTree')}
              >
                {treeOpen ? <IconLayoutSidebarLeftCollapse size={16} /> : <IconLayoutSidebar size={16} />}
              </ActionIcon>
            </Tooltip>
          )}
          <Text size="sm">{t('code.filesChanged', { count: diff.files.length })}</Text>
          <Text size="sm" className="kogge-additions">
            {t('code.additions', { count: additions })}
          </Text>
          <Text size="sm" className="kogge-deletions">
            {t('code.deletions', { count: deletions })}
          </Text>
          {viewed.enabled && (
            <Text size="sm" c="dimmed">
              {t('code.diffViewedCount', { viewed: viewed.count, count: diff.files.length })}
            </Text>
          )}
        </Group>
        <Group gap="xs">
          {diff.files.length > 1 && (
            <Tooltip label={allOpen ? t('code.diffCollapseAll') : t('code.diffExpandAll')}>
              <ActionIcon
                variant="subtle"
                color="gray"
                size="sm"
                onClick={() => setCollapsed(Object.fromEntries(paths.map((p) => [p, allOpen])))}
                aria-label={allOpen ? t('code.diffCollapseAll') : t('code.diffExpandAll')}
              >
                {allOpen ? <IconChevronsUp size={16} /> : <IconChevronsDown size={16} />}
              </ActionIcon>
            </Tooltip>
          )}
          {!narrow && (
            <SegmentedControl
              size="xs"
              value={mode}
              onChange={(v) => {
                setMode(v as DiffMode);
                setFileModes({});
              }}
              aria-label={t('code.diffLayout')}
              data={[
                { value: 'unified', label: t('code.diffUnified') },
                { value: 'split', label: t('code.diffSplit') },
              ]}
            />
          )}
        </Group>
      </Group>
      {diff.truncated && (
        <Text size="sm" c="orange">
          {t('code.diffTruncated')}
        </Text>
      )}
      <div className="kogge-diff-layout" data-tree={withTree || undefined}>
        {withTree && <FileTree files={diff.files} active={active} viewed={viewed.isViewed} onJump={jump} />}
        <Stack gap="md" style={{ minWidth: 0 }}>
          {diff.files.map((f) => {
            const path = filePath(f);
            // Side by side shows a file that is new or gone as one empty side: those stay one below the other.
            const twoSided = f.status !== 'added' && f.status !== 'deleted';
            const fileMode: DiffMode = narrow || !twoSided ? 'unified' : (fileModes[path] ?? mode);
            return (
              <FileCard
                key={path}
                repo={repo}
                file={f}
                open={isOpen(f)}
                onOpen={(o) => setCollapsed((c) => ({ ...c, [path]: !o }))}
                hiddenBecause={
                  collapsed[path] === undefined && !viewed.isViewed(path) ? hiddenAtFirst(f) : undefined
                }
                viewed={viewed.enabled ? viewed.isViewed(path) : undefined}
                onViewed={(yes) => {
                  viewed.setViewed(path, yes);
                  setCollapsed((c) => ({ ...c, [path]: yes }));
                }}
                mode={fileMode}
                onMode={
                  !narrow && twoSided && f.hunks.length > 0
                    ? () => setFileModes({ ...fileModes, [path]: fileMode === 'split' ? 'unified' : 'split' })
                    : undefined
                }
                viewRef={viewRef}
                contentRef={contentRef}
                shared={shared}
              />
            );
          })}
        </Stack>
      </div>
    </Stack>
  );
}
