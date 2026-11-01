// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Group, Menu, SegmentedControl } from '@mantine/core';
import { IconChevronDown, IconLayoutKanban } from '@tabler/icons-react';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { rememberBoard, useBoards, type Board } from './hooks';

type Section = 'board' | 'backlog' | 'reports' | 'settings';

/** Switches between the views of a board and between the boards of a project. */
export function BoardNav({
  projectKey,
  board,
  active,
}: {
  projectKey: string;
  board: Board;
  active: Section;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const boards = useBoards(projectKey);
  useEffect(() => rememberBoard(projectKey, board.id), [projectKey, board.id]);
  const base = `/projects/${projectKey}/boards/${board.id}`;
  const sections = [
    ...(board.type === 'scrum' ? [{ value: 'backlog', label: t('agile.backlog') }] : []),
    { value: 'board', label: board.type === 'scrum' ? t('agile.activeSprint') : t('agile.board') },
    { value: 'reports', label: t('agile.reports') },
    ...(board.canAdmin ? [{ value: 'settings', label: t('agile.boardSettings') }] : []),
  ];
  return (
    <Group justify="space-between">
      <Menu position="bottom-start">
        <Menu.Target>
          <Button
            variant="subtle"
            leftSection={<IconLayoutKanban size={16} />}
            rightSection={<IconChevronDown size={14} />}
          >
            {board.name}
          </Button>
        </Menu.Target>
        <Menu.Dropdown>
          {(boards.data?.items ?? []).map((b) => (
            <Menu.Item key={b.id} component={Link} to={`/projects/${projectKey}/boards/${b.id}`}>
              {b.name}
            </Menu.Item>
          ))}
          <Menu.Divider />
          <Menu.Item component={Link} to={`/projects/${projectKey}/boards`}>
            {t('agile.allBoards')}
          </Menu.Item>
        </Menu.Dropdown>
      </Menu>
      <SegmentedControl
        size="xs"
        value={active}
        data={sections}
        onChange={(v) => navigate(v === 'board' ? base : `${base}/${v}`)}
      />
    </Group>
  );
}
