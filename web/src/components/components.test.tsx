// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Menu } from '@mantine/core';
import { IconGitPullRequest, IconTrash } from '@tabler/icons-react';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ContainerAvatar } from './ContainerAvatar';
import { EmptyState } from './EmptyState';
import { IconButton } from './IconButton';
import { NoAccess } from './NoAccess';
import { PageHeader } from './PageHeader';
import { PersonAvatar } from './PersonAvatar';
import { PersonLink } from './PersonLink';
import { renderWithApp, setViewport } from './testing';

afterEach(cleanup);

describe('PageHeader', () => {
  const header = () => (
    <PageHeader
      title="SHOP-4: Pay with a saved card"
      subtitle="Jonas Weber wants to merge feature/SHOP-4 into main"
      actions={<Button>Merge</Button>}
      secondary={[{ label: 'Approve', onClick: vi.fn() }, { label: 'Needs work' }]}
      menu={<Menu.Item>Decline</Menu.Item>}
    />
  );

  it('shows all actions as buttons where there is room', () => {
    setViewport(1440);
    renderWithApp(header());
    expect(screen.getByRole('heading', { name: 'SHOP-4: Pay with a saved card' })).toBeTruthy();
    for (const name of ['Merge', 'Approve', 'Needs work', 'Other actions']) {
      expect(screen.getByRole('button', { name })).toBeTruthy();
    }
  });

  it('keeps the main action on a phone and folds the others into "…"', async () => {
    setViewport(390);
    renderWithApp(header());
    expect(screen.getByRole('button', { name: 'Merge' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Other actions' }));
    // The menu is still in its opening transition in jsdom: its items count as hidden.
    for (const name of ['Approve', 'Needs work', 'Decline']) {
      expect(await screen.findByRole('menuitem', { name, hidden: true })).toBeTruthy();
    }
  });

  it('has no "…" without further actions', () => {
    setViewport(390);
    renderWithApp(<PageHeader title="Releases" actions={<Button>New version</Button>} />);
    expect(screen.queryByRole('button', { name: 'Other actions' })).toBeNull();
  });
});

describe('EmptyState and NoAccess', () => {
  it('say what is missing and what to do', () => {
    setViewport(1440);
    renderWithApp(
      <EmptyState
        icon={IconGitPullRequest}
        title="No pull requests to review"
        description="Pull requests that ask for your review appear here."
        action={<Button>New pull request</Button>}
      />,
    );
    expect(screen.getByText('No pull requests to review')).toBeTruthy();
    expect(screen.getByText('Pull requests that ask for your review appear here.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'New pull request' })).toBeTruthy();
  });

  it('NoAccess explains calmly, by default and in the page’s words', () => {
    setViewport(1440);
    renderWithApp(<NoAccess />);
    expect(screen.getByText('No access')).toBeTruthy();
    expect(screen.getByText(/do not have permission/)).toBeTruthy();
    cleanup();
    renderWithApp(<NoAccess title="Pipeline settings are managed by the administrators of payments" />);
    expect(screen.getByText('Pipeline settings are managed by the administrators of payments')).toBeTruthy();
  });
});

describe('people and containers', () => {
  it('PersonAvatar shows initials, round, and is named only when it stands alone', () => {
    setViewport(1440);
    renderWithApp(
      <>
        <PersonAvatar person={{ displayName: 'Mara Lindner', username: 'mara' }} size={32} />
        <PersonAvatar person="Jonas Weber" labelled />
      </>,
    );
    expect(screen.getByText('ML')).toBeTruthy();
    expect(screen.getByRole('img', { name: 'Jonas Weber' }).textContent).toBe('JW');
    expect(screen.queryByRole('img', { name: 'Mara Lindner' })).toBeNull();
  });

  it('PersonLink leads to the person’s page; people without an account are text', () => {
    setViewport(1440);
    renderWithApp(
      <>
        <PersonLink person={{ displayName: 'Lena Hofmann', username: 'lena' }} withAvatar />
        <PersonLink person={{ displayName: 'Build Bot' }} />
      </>,
    );
    const link = screen.getByRole('link', { name: /Lena Hofmann/ });
    expect(link.getAttribute('href')).toBe('/people/lena');
    expect(link.textContent).toContain('LH');
    expect(screen.getByText('Build Bot').closest('a')).toBeNull();
  });

  it('ContainerAvatar shows two letters of the key', () => {
    setViewport(1440);
    renderWithApp(<ContainerAvatar containerKey="SHOP" size={40} />);
    expect(screen.getByText('SH')).toBeTruthy();
  });
});

describe('IconButton', () => {
  it('is named by its label and does what it is for', () => {
    setViewport(1440);
    const remove = vi.fn();
    renderWithApp(<IconButton label="Delete the component" icon={IconTrash} color="red" onClick={remove} />);
    fireEvent.click(screen.getByRole('button', { name: 'Delete the component' }));
    expect(remove).toHaveBeenCalled();
  });
});
