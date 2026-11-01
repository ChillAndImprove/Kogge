// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Divider, NavLink as MantineNavLink, Stack } from '@mantine/core';
import {
  IconFileText,
  IconFileZip,
  IconNews,
  IconPlus,
  IconSettings,
  IconTimeline,
} from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Navigate, NavLink, Outlet, useOutletContext, useParams } from 'react-router';
import { useRecordVisit } from '../../app/visits';
import { ActivityStream } from '../../components/ActivityStream';
import { ContainerAvatar } from '../../components/ContainerAvatar';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { SidebarLayout } from '../../components/SidebarLayout';
import { CreatePageDialog } from './CreatePageDialog';
import { ExportDialog } from './ExportDialog';
import { pagePath, usePage, useSpace, type PageRef, type Space } from './hooks';
import { PageTree } from './PageTree';

/** The space of the surrounding SpaceLayout. */
export function useSpaceContext() {
  return useOutletContext<Space>();
}

/** What happened recently in the space. */
export function SpaceActivity() {
  const { t } = useTranslation();
  const s = useSpaceContext();
  return (
    <>
      <PageHeader title={t('activity.title')} subtitle={t('activity.ofSpace', { name: s.name })} />
      <ActivityStream space={s.key} />
    </>
  );
}

export function SpaceLayout() {
  const { t } = useTranslation();
  const { key = '', pageId } = useParams();
  const space = useSpace(key);
  // A page on screen is the visit, not its space.
  useRecordVisit('space', pageId ? undefined : space.data?.id);
  const page = usePage(pageId ? Number(pageId) : undefined);
  const [creating, setCreating] = useState<{ kind: 'page' | 'blog'; parentId: number } | null>(null);
  const [exporting, setExporting] = useState(false);
  if (space.error) return <ErrorAlert error={space.error} />;
  const s = space.data;
  if (!s) return null;
  const canEdit = s.permissions.includes('page.edit');
  // The page on screen with the pages above it: the tree opens them and shows them even where their siblings are
  // not loaded yet.
  const path: PageRef[] = page.data
    ? [
        ...page.data.ancestors,
        {
          id: page.data.id,
          title: page.data.title,
          kind: page.data.kind,
          spaceKey: page.data.space.key,
          status: page.data.status,
          hasChildren: page.data.children > 0,
          restricted: page.data.restricted,
        },
      ]
    : [];
  // New pages go below the page on screen, or below the home page.
  const currentParent =
    page.data && page.data.kind === 'page' && page.data.status === 'current'
      ? page.data.id
      : (s.homePageId ?? 0);
  return (
    <>
      <SidebarLayout
        header={{
          avatar: <ContainerAvatar containerKey={s.key} size={32} />,
          title: s.name,
          meta: `${s.key}${s.archived ? ` · ${t('wiki.archivedSpace')}` : ''}`,
          to: pagePath(s.key, s.homePageId ?? 0),
        }}
        sidebar={
          <Stack gap={2}>
            <MantineNavLink
              component={NavLink}
              to={pagePath(s.key, s.homePageId ?? 0)}
              label={t('wiki.overview')}
              leftSection={<IconFileText size={16} />}
              active={!!pageId && Number(pageId) === s.homePageId}
            />
            <MantineNavLink
              component={NavLink}
              to={`/wiki/spaces/${s.key}/blog`}
              label={t('wiki.blog')}
              leftSection={<IconNews size={16} />}
            />
            <MantineNavLink
              component={NavLink}
              to={`/wiki/spaces/${s.key}/activity`}
              label={t('activity.title')}
              leftSection={<IconTimeline size={16} />}
            />
            {s.permissions.includes('space.admin') && (
              <MantineNavLink
                component={NavLink}
                to={`/wiki/spaces/${s.key}/settings`}
                label={t('wiki.spaceSettings')}
                leftSection={<IconSettings size={16} />}
              />
            )}
            {s.permissions.includes('space.export') && (
              <MantineNavLink
                component="button"
                label={t('wiki.exportSpace')}
                leftSection={<IconFileZip size={16} />}
                onClick={() => setExporting(true)}
              />
            )}
            {canEdit && (
              <Button
                mt="xs"
                variant="default"
                leftSection={<IconPlus size={16} />}
                onClick={() => setCreating({ kind: 'page', parentId: currentParent })}
              >
                {t('wiki.createPage')}
              </Button>
            )}
            <Divider label={t('wiki.pages')} labelPosition="left" mt="sm" mb={4} />
            <PageTree
              spaceKey={s.key}
              current={pageId ? Number(pageId) : undefined}
              path={path}
              canEdit={canEdit}
              onCreateChild={(parentId) => setCreating({ kind: 'page', parentId })}
            />
          </Stack>
        }
      >
        <Outlet context={s} />
      </SidebarLayout>
      {exporting && <ExportDialog space={s} onClose={() => setExporting(false)} />}
      {creating && (
        <CreatePageDialog
          space={s}
          kind={creating.kind}
          parentId={creating.parentId}
          onClose={() => setCreating(null)}
        />
      )}
    </>
  );
}

/** /wiki/spaces/:key opens the home page. */
export function SpaceHome() {
  const s = useSpaceContext();
  return <Navigate to={pagePath(s.key, s.homePageId ?? 0)} replace />;
}
