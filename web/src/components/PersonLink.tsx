// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Badge,
  Button,
  Group,
  HoverCard,
  Loader,
  Stack,
  Text,
  type MantineFontSize,
  type MantineColor,
} from '@mantine/core';
import { IconMail, IconUserCircle } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../api/client';
import { PersonAvatar, type PersonLike } from './PersonAvatar';

/** The address of a person's page. */
export function personPath(username: string): string {
  return `/people/${encodeURIComponent(username)}`;
}

/** Who a person is, for the hover card: their profile as far as the viewer may see it. */
function PersonCard({ person }: { person: PersonLike & { username: string } }) {
  const { t } = useTranslation();
  const profile = useQuery({
    queryKey: ['person', person.username],
    queryFn: () =>
      unwrap(api.GET('/api/v1/users/{username}', { params: { path: { username: person.username } } })),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const p = profile.data;
  const groups = p?.groups ?? [];
  return (
    <Stack gap="sm" w={280}>
      <Group gap="sm" wrap="nowrap" align="flex-start">
        <PersonAvatar person={{ ...person, active: p?.active ?? person.active }} size={40} />
        <Stack gap={0} miw={0}>
          <Text fw={600} truncate>
            {p?.displayName ?? person.displayName}
          </Text>
          <Text size="xs" c="dimmed" truncate>
            @{person.username}
          </Text>
          {(p?.active ?? person.active) === false && (
            <Badge size="xs" color="gray" variant="light" mt={4}>
              {t('people.inactive')}
            </Badge>
          )}
        </Stack>
        {profile.isLoading && <Loader size="xs" ml="auto" />}
      </Group>
      {p?.email && (
        <Group gap={6} wrap="nowrap">
          <IconMail size={14} aria-hidden style={{ flexShrink: 0 }} />
          <Anchor href={`mailto:${p.email}`} size="sm" truncate>
            {p.email}
          </Anchor>
        </Group>
      )}
      {groups.length > 0 && (
        <Group gap={4} aria-label={t('people.groups')}>
          {groups.slice(0, 4).map((g) => (
            <Badge key={g.id} size="sm" variant="light" color="gray">
              {g.name}
            </Badge>
          ))}
          {groups.length > 4 && (
            <Text size="xs" c="dimmed">
              {t('people.moreGroups', { count: groups.length - 4 })}
            </Text>
          )}
        </Group>
      )}
      <Button
        component={Link}
        to={personPath(person.username)}
        size="xs"
        variant="light"
        leftSection={<IconUserCircle size={14} />}
      >
        {t('people.openPage')}
      </Button>
    </Stack>
  );
}

export interface PersonLinkProps {
  person: (PersonLike & { username?: string }) | null | undefined;
  /** An avatar before the name. */
  withAvatar?: boolean;
  avatarSize?: number;
  size?: MantineFontSize;
  fw?: number;
  /** The colour of the name; that of the text around it by default (people are not links in running text). */
  c?: MantineColor;
  /** Without the card on hover (where the person's details are on screen already). */
  noCard?: boolean;
}

/**
 * A person's name that leads to their page (/people/:username); on hover a card shows who they are (avatar, groups,
 * e-mail). People without an account in Kogge (authors of commits) are plain text.
 */
export function PersonLink({
  person,
  withAvatar = false,
  avatarSize = 20,
  size = 'sm',
  fw,
  c = 'inherit',
  noCard = false,
}: PersonLinkProps) {
  if (!person) return null;
  const avatar = withAvatar && <PersonAvatar person={person} size={avatarSize} />;
  const name = person.displayName;
  if (!person.username) {
    return (
      <Group gap={6} wrap="nowrap" component="span" display="inline-flex">
        {avatar}
        <Text span size={size} fw={fw} c={c === 'inherit' ? undefined : c}>
          {name}
        </Text>
      </Group>
    );
  }
  const username = person.username;
  const link = (
    <Anchor
      component={Link}
      to={personPath(username)}
      size={size}
      fw={fw}
      c={c}
      underline="hover"
      className="kogge-person-link"
      data-inactive={person.active === false || undefined}
    >
      {avatar}
      <span>{name}</span>
    </Anchor>
  );
  if (noCard) return link;
  // The card (and its request) exists only while it is open.
  return (
    <HoverCard openDelay={450} closeDelay={150} position="bottom-start" shadow="md">
      <HoverCard.Target>{link}</HoverCard.Target>
      <HoverCard.Dropdown>
        <PersonCard person={{ ...person, username }} />
      </HoverCard.Dropdown>
    </HoverCard>
  );
}
