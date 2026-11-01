// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Badge, Button, NavLink as MantineNavLink, Stack } from '@mantine/core';
import {
  IconChartLine,
  IconLayoutKanban,
  IconListDetails,
  IconListNumbers,
  IconPackage,
  IconPlus,
  IconPuzzle,
  IconSettings,
  IconStack2,
  IconTimeline,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { NavLink, Outlet, useOutletContext, useParams } from 'react-router';
import { useRecordVisit } from '../../app/visits';
import { ActivityStream } from '../../components/ActivityStream';
import { ContainerAvatar } from '../../components/ContainerAvatar';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { SidebarLayout } from '../../components/SidebarLayout';
import { useCurrentBoard } from '../agile/hooks';
import { useCreateIssue } from './CreateIssue';
import { useProject } from './shared';
import type { Schemas } from '../../api/client';

export type ProjectView = Schemas['ProjectView'];

/** A project's avatar: two letters of its key on the key's colour. */
export function ProjectAvatar({ projectKey, size = 32 }: { projectKey: string; size?: number }) {
  return <ContainerAvatar containerKey={projectKey} size={size} />;
}

/** The project of the surrounding ProjectLayout. */
export function useProjectContext() {
  return useOutletContext<ProjectView>();
}

/** What happened recently in the project. */
export function ProjectActivity() {
  const { t } = useTranslation();
  const p = useProjectContext();
  return (
    <>
      <PageHeader title={t('activity.title')} subtitle={t('activity.ofProject', { name: p.name })} />
      <ActivityStream project={p.key} />
    </>
  );
}

export function ProjectLayout() {
  const { t } = useTranslation();
  const { key = '' } = useParams();
  const project = useProject(key.toUpperCase());
  useRecordVisit('project', project.data?.id);
  const createIssue = useCreateIssue();
  const { board } = useCurrentBoard(key.toUpperCase());
  if (project.error) return <ErrorAlert error={project.error} />;
  const p = project.data;
  if (!p) return null;
  const admin = p.permissions.includes('project.admin');
  const boardBase = board ? `/projects/${p.key}/boards/${board.id}` : `/projects/${p.key}/boards`;
  const items = [
    ...(board?.type === 'scrum'
      ? [{ to: `${boardBase}/backlog`, label: t('agile.backlog'), icon: IconListNumbers, end: true }]
      : []),
    {
      to: boardBase,
      label: board?.type === 'scrum' ? t('agile.activeSprint') : t('agile.board'),
      icon: IconLayoutKanban,
      end: true,
    },
    ...(board
      ? [{ to: `${boardBase}/reports`, label: t('agile.reports'), icon: IconChartLine, end: true }]
      : []),
    { to: `/projects/${p.key}`, label: t('issues.issuesNav'), icon: IconListDetails, end: true },
    ...(p.issueTypes.some((ty) => ty.level === 1)
      ? [{ to: `/projects/${p.key}/epics`, label: t('agile.epics'), icon: IconStack2 }]
      : []),
    { to: `/projects/${p.key}/releases`, label: t('issues.releases'), icon: IconPackage },
    { to: `/projects/${p.key}/components`, label: t('issues.components'), icon: IconPuzzle },
    { to: `/projects/${p.key}/activity`, label: t('activity.title'), icon: IconTimeline },
    ...(admin
      ? [{ to: `/projects/${p.key}/settings`, label: t('issues.settings'), icon: IconSettings }]
      : []),
  ];
  return (
    <SidebarLayout
      header={{
        avatar: <ContainerAvatar containerKey={p.key} size={32} />,
        title: p.name,
        meta: `${p.key} · ${t(`issues.projectTypes.${p.type}`)}`,
        to: `/projects/${p.key}`,
      }}
      sidebar={
        <Stack gap={2}>
          {p.archived && (
            <Badge color="gray" size="sm" mb={4}>
              {t('issues.archived')}
            </Badge>
          )}
          {items.map((item) => (
            <MantineNavLink
              key={item.to}
              component={NavLink}
              to={item.to}
              end={item.end}
              label={item.label}
              leftSection={<item.icon size={16} />}
            />
          ))}
          {p.permissions.includes('issue.create') && !p.archived && (
            <Button
              mt="md"
              variant="default"
              leftSection={<IconPlus size={16} />}
              onClick={() => createIssue({ project: p.key })}
            >
              {t('issues.createIssue')}
            </Button>
          )}
        </Stack>
      }
    >
      <Outlet context={p} />
    </SidebarLayout>
  );
}
