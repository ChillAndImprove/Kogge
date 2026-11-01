// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Button, Group, Menu } from '@mantine/core';
import {
  IconBook2,
  IconChevronDown,
  IconFilePlus,
  IconFolderPlus,
  IconGitPullRequest,
  IconLayoutDashboard,
  IconPlayerPlay,
  IconPlus,
  IconSquarePlus,
  IconTemplate,
} from '@tabler/icons-react';
import { lazy, Suspense, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useMatch, useNavigate } from 'react-router';
import { CreateProject } from '../pages/code/CodeHome';
import { useCreateRepository } from '../pages/code/CreateRepo';
import { useRepository } from '../pages/code/hooks';
import { useCreateIssue } from '../pages/issues/CreateIssue';
import { useCreatePage } from '../pages/wiki/CreatePage';
import { products } from '../products';
import { useCodeProjectPlace, useIssueProjectPlace, useWikiPlace } from './places';
import { Frame } from './Shell';
import { useSession } from './session';

// Dialogs of pages that are loaded with their pages: the header loads them when they are asked for.
const DashboardForm = lazy(() =>
  import('../pages/dashboards/DashboardsPage').then((m) => ({ default: m.DashboardForm })),
);
const RunPipelineModal = lazy(() =>
  import('../pages/builds/components').then((m) => ({ default: m.RunPipelineModal })),
);
const SetUpPipelines = lazy(() =>
  import('../pages/builds/Templates').then((m) => ({ default: m.SetUpPipelines })),
);

const issueKeyPattern = /^[A-Za-z][A-Za-z0-9_]{1,9}-[1-9][0-9]{0,9}$/;

/** The pages of a code project and its repositories (they belong under "Repositories"). */
const inCodeProject = /^\/code\/(?!repos(\/|$)|pull-requests(\/|$))[^/]+/;
/** The pipelines of a code project and its repositories (they belong under "Builds"). */
const inBuildsProject = /^\/builds\/(?!templates(\/|$)|agents(\/|$))[^/]+/;

/** A thing the header's "Create" makes: it opens a dialog (onClick) or a page where it is made (to). */
interface CreateItem {
  label: string;
  icon: ReactNode;
  onClick?: () => void;
  to?: string;
}

function CreateMenuItem({ item }: { item: CreateItem }) {
  return item.to ? (
    <Menu.Item component={Link} to={item.to} leftSection={item.icon}>
      {item.label}
    </Menu.Item>
  ) : (
    <Menu.Item leftSection={item.icon} onClick={item.onClick}>
      {item.label}
    </Menu.Item>
  );
}

/**
 * The header's "Create", the same in every product: a filled button that makes the product's main thing in one
 * click (an issue in Kontor, a page in Logbuch …), with a menu of all it can make; without a main thing the button
 * opens the menu. On phones it is a "+" with the menu.
 */
export function CreateMenu({
  primary,
  items,
  compact = false,
}: {
  primary?: CreateItem;
  items: CreateItem[];
  compact?: boolean;
}) {
  const { t } = useTranslation();
  if (items.length === 0) return null;
  const dropdown = (
    <Menu.Dropdown>
      {items.map((item) => (
        <CreateMenuItem key={item.label} item={item} />
      ))}
    </Menu.Dropdown>
  );
  if (compact) {
    return (
      <Menu position="bottom-end" width={240}>
        <Menu.Target>
          <ActionIcon size="lg" variant="filled" aria-label={t('shell.create')}>
            <IconPlus size={20} />
          </ActionIcon>
        </Menu.Target>
        {dropdown}
      </Menu>
    );
  }
  if (!primary) {
    return (
      <Menu position="bottom-start" width={240}>
        <Menu.Target>
          <Button size="sm" leftSection={<IconPlus size={16} />} rightSection={<IconChevronDown size={14} />}>
            {t('shell.create')}
          </Button>
        </Menu.Target>
        {dropdown}
      </Menu>
    );
  }
  return (
    <Group gap={0} wrap="nowrap" className="kogge-split-button">
      {primary.to ? (
        <Button size="sm" leftSection={<IconPlus size={16} />} component={Link} to={primary.to}>
          {t('shell.create')}
        </Button>
      ) : (
        <Button size="sm" leftSection={<IconPlus size={16} />} onClick={primary.onClick}>
          {t('shell.create')}
        </Button>
      )}
      <Menu position="bottom-end" width={240}>
        <Menu.Target>
          <Button size="sm" px={8} aria-label={t('shell.createMore')}>
            <IconChevronDown size={16} />
          </Button>
        </Menu.Target>
        {dropdown}
      </Menu>
    </Group>
  );
}

/** The platform: the overview of the products, administration and the profile. */
export function PlatformShell() {
  const { t } = useTranslation();
  const { can } = useSession();
  const createIssue = useCreateIssue();
  const createPage = useCreatePage();
  const createRepository = useCreateRepository();
  const items: CreateItem[] = [
    { label: t('shell.item.issue'), icon: <IconSquarePlus size={16} />, onClick: () => createIssue({}) },
    {
      label: t('shell.item.page'),
      icon: <IconFilePlus size={16} />,
      onClick: () => createPage({ kind: 'page' }),
    },
    {
      label: t('shell.item.blogPost'),
      icon: <IconBook2 size={16} />,
      onClick: () => createPage({ kind: 'blog' }),
    },
    {
      label: t('shell.item.repository'),
      icon: <IconFolderPlus size={16} />,
      onClick: () => createRepository({}),
    },
  ];
  return (
    <Frame
      product={null}
      nav={[
        { to: '/', label: t('platform.overview'), end: true },
        { to: '/admin', label: t('nav.administration'), hidden: !can('admin') },
      ]}
      search={{ placeholder: t('nav.search'), go: (q) => `/search?q=${encodeURIComponent(q)}` }}
      create={(compact) => <CreateMenu items={items} compact={compact} />}
    />
  );
}

/** Kontor's "Create": an issue in the project on screen; dashboards from the menu. */
function KontorCreate({ compact }: { compact: boolean }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const createIssue = useCreateIssue();
  // Preselect the project of the current page.
  const project = useIssueProjectPlace();
  const [dashboard, setDashboard] = useState(false);
  const issue: CreateItem = {
    label: t('shell.item.issue'),
    icon: <IconSquarePlus size={16} />,
    onClick: () => createIssue({ project }),
  };
  return (
    <>
      <CreateMenu
        primary={issue}
        compact={compact}
        items={[
          issue,
          {
            label: t('shell.item.dashboard'),
            icon: <IconLayoutDashboard size={16} />,
            onClick: () => setDashboard(true),
          },
        ]}
      />
      {dashboard && (
        <Suspense>
          <DashboardForm
            dashboard={null}
            onClose={(id) => {
              setDashboard(false);
              if (id) navigate(`/dashboards/${id}`);
            }}
          />
        </Suspense>
      )}
    </>
  );
}

/** Kontor — issues, boards and planning. */
export function KontorShell() {
  const { t } = useTranslation();
  const { can } = useSession();
  return (
    <Frame
      product={products.kontor}
      nav={[
        { to: '/issues', label: t('kontor.yourWork'), end: true },
        { to: '/issues/projects', label: t('kontor.projects'), also: (p) => p.startsWith('/projects/') },
        { to: '/issues/search', label: t('kontor.issues'), also: (p) => p.startsWith('/browse/') },
        { to: '/issues/filters', label: t('kontor.filters') },
        { to: '/dashboards', label: t('kontor.dashboards') },
        { to: '/issues/admin', label: t('kontor.configuration'), hidden: !can('admin') },
      ]}
      search={{
        placeholder: t('kontor.search'),
        go: (q) =>
          issueKeyPattern.test(q)
            ? `/browse/${q.toUpperCase()}`
            : `/issues/search?q=${encodeURIComponent(`text ~ "${q.replace(/["\\]/g, '')}" ORDER BY updated DESC`)}`,
      }}
      create={(compact) => <KontorCreate compact={compact} />}
    />
  );
}

/** Logbuch's "Create": a page below the page on screen, in the current space, or in a chosen one; blog posts. */
function LogbuchCreate({ compact }: { compact: boolean }) {
  const { t } = useTranslation();
  const createPage = useCreatePage();
  const place = useWikiPlace();
  const page: CreateItem = {
    label: t('shell.item.page'),
    icon: <IconFilePlus size={16} />,
    onClick: () => createPage({ kind: 'page', ...place }),
  };
  return (
    <CreateMenu
      primary={page}
      compact={compact}
      items={[
        page,
        {
          label: t('shell.item.blogPost'),
          icon: <IconBook2 size={16} />,
          onClick: () => createPage({ kind: 'blog', ...place }),
        },
      ]}
    />
  );
}

/** Logbuch — the wiki. */
export function LogbuchShell() {
  const { t } = useTranslation();
  return (
    <Frame
      product={products.logbuch}
      nav={[
        { to: '/wiki', label: t('logbuch.home'), end: true },
        { to: '/wiki/spaces', label: t('logbuch.spaces') },
      ]}
      search={{ placeholder: t('logbuch.search'), go: (q) => `/wiki/search?q=${encodeURIComponent(q)}` }}
      create={(compact) => <LogbuchCreate compact={compact} />}
    />
  );
}

/** Hafen's "Create": a repository in the project on screen or a chosen one; a code project; a pull request. */
function HafenCreate({ compact }: { compact: boolean }) {
  const { t } = useTranslation();
  const { can } = useSession();
  const createRepository = useCreateRepository();
  const project = useCodeProjectPlace();
  const repoPage = useMatch('/code/:key/:repo/*');
  const [creatingProject, setCreatingProject] = useState(false);
  const repository: CreateItem = {
    label: t('shell.item.repository'),
    icon: <IconFolderPlus size={16} />,
    onClick: () => createRepository({ project }),
  };
  const items: CreateItem[] = [repository];
  if (repoPage && !['repos', 'pull-requests'].includes(repoPage.params.key ?? '')) {
    items.push({
      label: t('shell.item.pullRequest'),
      icon: <IconGitPullRequest size={16} />,
      to: `/code/${repoPage.params.key}/${repoPage.params.repo}/pull-requests/new`,
    });
  }
  if (can('codeproject.create')) {
    items.push({
      label: t('shell.item.codeProject'),
      icon: <IconFolderPlus size={16} />,
      onClick: () => setCreatingProject(true),
    });
  }
  return (
    <>
      <CreateMenu primary={repository} items={items} compact={compact} />
      {creatingProject && <CreateProject onClose={() => setCreatingProject(false)} />}
    </>
  );
}

/** Hafen — Git hosting and pull requests. */
export function HafenShell() {
  const { t } = useTranslation();
  return (
    <Frame
      product={products.hafen}
      nav={[
        { to: '/code', label: t('hafen.dashboard'), end: true },
        { to: '/code/repos', label: t('hafen.repositories'), also: (p) => inCodeProject.test(p) },
        { to: '/code/pull-requests', label: t('hafen.pullRequests') },
      ]}
      search={{
        placeholder: t('hafen.search'),
        go: (q) => `/search?q=${encodeURIComponent(q)}&product=hafen`,
      }}
      create={(compact) => <HafenCreate compact={compact} />}
    />
  );
}

/**
 * Helling's "Create": on a repository's pipelines a run of a pipeline (and pipelines from a template); elsewhere a
 * repository (pipelines live in repositories) and the pipeline templates.
 */
function HellingCreate({ compact }: { compact: boolean }) {
  const { t } = useTranslation();
  const createRepository = useCreateRepository();
  const pipelinePage = useMatch('/builds/:key/:repo/*');
  const key = pipelinePage && pipelinePage.params.key !== 'templates' ? pipelinePage.params.key : undefined;
  const repo = useRepository(key, key ? pipelinePage?.params.repo : undefined);
  const [dialog, setDialog] = useState<'run' | 'setup' | null>(null);
  const writable = repo.data && (repo.data.permission === 'write' || repo.data.permission === 'admin');
  const run: CreateItem | undefined = writable
    ? { label: t('builds.runPipeline'), icon: <IconPlayerPlay size={16} />, onClick: () => setDialog('run') }
    : undefined;
  const items: CreateItem[] = [
    ...(run
      ? [
          run,
          {
            label: t('builds.setUpPipelines'),
            icon: <IconTemplate size={16} />,
            onClick: () => setDialog('setup'),
          },
        ]
      : []),
    {
      label: t('shell.item.repository'),
      icon: <IconFolderPlus size={16} />,
      onClick: () => createRepository({ project: key?.toUpperCase() }),
    },
    { label: t('shell.item.pipelineTemplate'), icon: <IconTemplate size={16} />, to: '/builds/templates' },
  ];
  return (
    <>
      <CreateMenu primary={run} items={items} compact={compact} />
      {repo.data && dialog === 'run' && (
        <Suspense>
          <RunPipelineModal repo={repo.data} opened onClose={() => setDialog(null)} />
        </Suspense>
      )}
      {repo.data && dialog === 'setup' && (
        <Suspense>
          <SetUpPipelines repo={repo.data} onClose={() => setDialog(null)} />
        </Suspense>
      )}
    </>
  );
}

/** Helling — builds and deployments. */
export function HellingShell() {
  const { t } = useTranslation();
  const { can } = useSession();
  return (
    <Frame
      product={products.helling}
      nav={[
        { to: '/builds', label: t('helling.dashboard'), end: true, also: (p) => inBuildsProject.test(p) },
        { to: '/builds/templates', label: t('helling.templates') },
        { to: '/builds/agents', label: t('helling.agents'), hidden: !can('agent.manage') && !can('admin') },
      ]}
      search={{
        placeholder: t('helling.search'),
        go: (q) => `/search?q=${encodeURIComponent(q)}&product=helling`,
      }}
      create={(compact) => <HellingCreate compact={compact} />}
    />
  );
}
