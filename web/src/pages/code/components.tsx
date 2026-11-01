// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  Anchor,
  Avatar,
  Badge,
  Box,
  Button,
  Card,
  Checkbox,
  Code,
  Combobox,
  CopyButton,
  Group,
  InputBase,
  Popover,
  SegmentedControl,
  Select,
  Stack,
  Text,
  TextInput,
  Tooltip,
  useCombobox,
  type SelectProps,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import {
  IconCheck,
  IconChevronDown,
  IconClockPause,
  IconClockPlay,
  IconCopy,
  IconGitBranch,
  IconPlusMinus,
  IconTag,
} from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { formatRelative } from '../../app/format';
import { DocView } from '../../components/editor/DocView';
import { MarkdownEditor } from '../../components/MarkdownEditor';
import {
  personName,
  repoPath,
  shortSha,
  useBranches,
  useTags,
  type CommitInfo,
  type PRComment,
  type Repository,
} from './hooks';
import { useStatuses } from '../builds/hooks';
import { CommitStatusIcon } from '../builds/status';
import './code.css';

/** Highlighted lines with line numbers; lines can be selected for links. */
export function CodeLines({
  lines,
  selected,
  onSelect,
  first = 1,
}: {
  lines: string[];
  selected?: number;
  onSelect?: (line: number) => void;
  /** Number of the first line. */
  first?: number;
}) {
  return (
    <Box style={{ overflowX: 'auto' }}>
      <table className="kogge-code">
        <tbody>
          {lines.map((html, i) => (
            <tr key={i} id={`L${i + first}`} className={selected === i + first ? 'selected' : undefined}>
              <td className="ln" onClick={() => onSelect?.(i + first)}>
                {i + first}
              </td>
              {/* The server escapes the text; only token spans remain. */}
              <td className="src" dangerouslySetInnerHTML={{ __html: html || ' ' }} />
            </tr>
          ))}
        </tbody>
      </table>
    </Box>
  );
}

/** A person of a commit with avatar. */
export function PersonLabel({ person, size = 'sm' }: { person: CommitInfo['author']; size?: 'xs' | 'sm' }) {
  return (
    <Group gap={6} wrap="nowrap">
      <Avatar size={size === 'xs' ? 18 : 22} radius="xl" name={personName(person)} color="initials" />
      <Text size={size} truncate>
        {personName(person)}
      </Text>
    </Group>
  );
}

/** A list of commits. */
export function CommitList({ repo, commits }: { repo: Repository; commits: CommitInfo[] }) {
  const { t } = useTranslation();
  const statuses = useStatuses(
    repo,
    commits.map((c) => c.sha),
  );
  if (commits.length === 0) return <Text c="dimmed">{t('code.noCommits')}</Text>;
  return (
    <Card withBorder padding={0}>
      {commits.map((c, i) => (
        <Group
          key={c.sha}
          justify="space-between"
          wrap="nowrap"
          px="md"
          py="xs"
          style={{ borderTop: i ? '1px solid var(--mantine-color-default-border)' : undefined }}
        >
          <Stack gap={2} style={{ minWidth: 0 }}>
            <Anchor component={Link} to={repoPath(repo, 'commits', c.sha)} fw={500} size="sm" truncate>
              {c.subject}
            </Anchor>
            <Group gap="xs">
              <PersonLabel person={c.author} size="xs" />
              <Text size="xs" c="dimmed">
                {formatRelative(c.author.when)}
              </Text>
            </Group>
          </Stack>
          <Group gap={4} wrap="nowrap">
            <CommitStatusIcon statuses={statuses.data?.[c.sha]} />
            <Code>{shortSha(c.sha)}</Code>
            <CopySha sha={c.sha} />
          </Group>
        </Group>
      ))}
    </Card>
  );
}

export function CopySha({ sha }: { sha: string }) {
  const { t } = useTranslation();
  return (
    <CopyButton value={sha}>
      {({ copied, copy }) => (
        <Tooltip label={copied ? t('common.copied') : t('code.copySha')}>
          <ActionIcon variant="subtle" size="sm" onClick={copy} aria-label={t('code.copySha')}>
            {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
          </ActionIcon>
        </Tooltip>
      )}
    </CopyButton>
  );
}

/** Searches the branches of a repository as the user types (one named like the search first); the value is a name. */
export function BranchPicker({
  repo,
  value,
  onChange,
  ...props
}: Omit<SelectProps, 'data' | 'value' | 'onChange'> & {
  repo: Repository;
  value: string;
  onChange: (name: string | null) => void;
}) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  // The input shows the chosen branch; that is no search: offer the others too.
  const branches = useBranches(repo, debounced === value ? '' : debounced, 30);
  const data = (branches.data?.items ?? []).map((b) => b.name);
  if (value && !data.includes(value)) data.unshift(value);
  return (
    <Select
      searchable
      data={data}
      value={value}
      onChange={onChange}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

/** Chooses a branch or tag. */
export function RefSelect({
  repo,
  value,
  onChange,
  tags = true,
  label,
}: {
  repo: Repository;
  value: string;
  onChange: (ref: string) => void;
  tags?: boolean;
  label?: string;
}) {
  const { t } = useTranslation();
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const combobox = useCombobox({ onDropdownClose: () => setSearch('') });
  // The server searches all branches and tags; one named like the search comes first.
  const branches = useBranches(repo, debounced, 50);
  const tagList = useTags(repo, debounced, 50, 0, tags);
  const options = [
    ...(branches.data?.items ?? []).map((b) => ({ name: b.name, tag: false })),
    ...(tags ? (tagList.data?.items ?? []).map((tg) => ({ name: tg.name, tag: true })) : []),
  ];
  const isTag = (tagList.data?.items ?? []).some((tg) => tg.name === value);
  return (
    <Combobox
      store={combobox}
      onOptionSubmit={(v) => {
        onChange(v);
        combobox.closeDropdown();
      }}
      width={320}
      position="bottom-start"
    >
      <Combobox.Target>
        <InputBase
          component="button"
          type="button"
          pointer
          label={label}
          leftSection={isTag ? <IconTag size={14} /> : <IconGitBranch size={14} />}
          rightSection={<IconChevronDown size={14} />}
          rightSectionPointerEvents="none"
          onClick={() => combobox.toggleDropdown()}
          w={240}
          aria-label={label ?? t('code.ref')}
        >
          <Text size="sm" truncate>
            {value}
          </Text>
        </InputBase>
      </Combobox.Target>
      <Combobox.Dropdown>
        <Combobox.Search
          value={search}
          onChange={(e) => setSearch(e.currentTarget.value)}
          placeholder={t('code.filterBranches')}
        />
        <Combobox.Options mah={300} style={{ overflowY: 'auto' }}>
          {options.map((o) => (
            <Combobox.Option key={(o.tag ? 't:' : 'b:') + o.name} value={o.name} active={o.name === value}>
              <Group gap={6} wrap="nowrap">
                {o.tag ? <IconTag size={14} /> : <IconGitBranch size={14} />}
                <Text size="sm" truncate>
                  {o.name}
                </Text>
              </Group>
            </Combobox.Option>
          ))}
        </Combobox.Options>
      </Combobox.Dropdown>
    </Combobox>
  );
}

/** Shows the clone addresses of a repository. */
export function CloneButton({ repo }: { repo: Repository }) {
  const { t } = useTranslation();
  const [kind, setKind] = useState<'http' | 'ssh'>('http');
  const url = kind === 'ssh' && repo.cloneSsh ? repo.cloneSsh : repo.cloneHttp;
  return (
    <Popover width={460} position="bottom-end" withArrow shadow="md">
      <Popover.Target>
        <Button variant="default" leftSection={<IconCopy size={14} />}>
          {t('code.clone')}
        </Button>
      </Popover.Target>
      <Popover.Dropdown>
        <Stack gap="xs">
          {repo.cloneSsh && (
            <SegmentedControl
              size="xs"
              value={kind}
              onChange={(v) => setKind(v as 'http' | 'ssh')}
              data={[
                { value: 'http', label: 'HTTPS' },
                { value: 'ssh', label: 'SSH' },
              ]}
            />
          )}
          <Group gap={4} wrap="nowrap">
            <TextInput
              value={url}
              readOnly
              style={{ flex: 1 }}
              onFocus={(e) => e.currentTarget.select()}
              aria-label={t('code.clone')}
            />
            <CopyButton value={url}>
              {({ copied, copy }) => (
                <ActionIcon variant="default" size="lg" onClick={copy} aria-label={t('common.copy')}>
                  {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
                </ActionIcon>
              )}
            </CopyButton>
          </Group>
          <Text size="xs" c="dimmed">
            {kind === 'ssh' ? t('code.cloneSshHelp') : t('code.cloneHttpHelp')}
          </Text>
        </Stack>
      </Popover.Dropdown>
    </Popover>
  );
}

export function escapeHtml(s: string) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

/** A Markdown comment form with the task option. */
export function CommentForm({
  onSubmit,
  onCancel,
  initial = '',
  taskOption = true,
  submitLabel,
  suggestLine,
}: {
  onSubmit: (markdown: string, task: boolean) => Promise<unknown>;
  onCancel?: () => void;
  initial?: string;
  taskOption?: boolean;
  submitLabel?: string;
  /** A line of the new side: the form offers to suggest a change of it. */
  suggestLine?: string;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState(initial);
  const suggest = () =>
    setText(
      (v) => `${v}${v && !v.endsWith('\n') ? '\n' : ''}\`\`\`suggestion\n${suggestLine ?? ''}\n\`\`\`\n`,
    );
  const [task, setTask] = useState(false);
  const [busy, setBusy] = useState(false);
  const submit = () => {
    if (!text.trim()) return;
    setBusy(true);
    onSubmit(text, task)
      .then(() => {
        setText('');
        setTask(false);
      })
      .finally(() => setBusy(false));
  };
  return (
    <Stack gap="xs">
      <MarkdownEditor
        value={text}
        onChange={setText}
        placeholder={t('code.pr.addComment')}
        ariaLabel={t('code.pr.addComment')}
        minRows={2}
        autoFocus={!!onCancel}
        onSubmit={submit}
      />
      <Group justify="space-between">
        <Group gap="md">
          {taskOption && (
            <Checkbox
              size="xs"
              label={t('code.pr.asTask')}
              checked={task}
              onChange={(e) => setTask(e.currentTarget.checked)}
            />
          )}
          {suggestLine !== undefined && (
            <Button
              size="compact-xs"
              variant="subtle"
              leftSection={<IconPlusMinus size={12} />}
              onClick={suggest}
            >
              {t('code.pr.suggest')}
            </Button>
          )}
        </Group>
        <Group gap="xs">
          {onCancel && (
            <Button size="xs" variant="default" onClick={onCancel}>
              {t('common.cancel')}
            </Button>
          )}
          <Button size="xs" onClick={submit} loading={busy} disabled={!text.trim()}>
            {submitLabel ?? t('code.pr.comment')}
          </Button>
        </Group>
      </Group>
    </Stack>
  );
}

/** A comment body (rich text). */
export function CommentBody({ comment }: { comment: PRComment }) {
  return <DocView doc={comment.body} />;
}

/** Shows that a pull request merges by itself when ready (orange while something blocks it). */
export function AutoMergeBadge({ blocked, size = 'lg' }: { blocked: boolean; size?: 'xs' | 'lg' }) {
  const { t } = useTranslation();
  const icon = size === 'xs' ? 10 : 14;
  return (
    <Badge
      size={size}
      variant="light"
      color={blocked ? 'orange' : 'grape'}
      leftSection={blocked ? <IconClockPause size={icon} /> : <IconClockPlay size={icon} />}
    >
      {t('code.pr.autoMerge.badge')}
    </Badge>
  );
}
