// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Badge, Group, Loader, Stack, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useParams } from 'react-router';
import { api, unwrap } from '../api/client';
import { ActivityStream } from '../components/ActivityStream';
import { ErrorAlert } from '../components/ErrorAlert';
import { PersonAvatar } from '../components/PersonAvatar';

/** A person (found by the quick search): who they are, their groups, and what they did that the viewer may see. */
export function PersonPage() {
  const { t } = useTranslation();
  const { username = '' } = useParams();
  const person = useQuery({
    queryKey: ['person', username],
    queryFn: () => unwrap(api.GET('/api/v1/users/{username}', { params: { path: { username } } })),
    retry: false,
  });
  if (person.error) return <ErrorAlert error={person.error} />;
  if (!person.data) return <Loader size="sm" />;
  const u = person.data;
  return (
    <Stack maw={960} mx="auto" gap="lg">
      <Group gap="md" wrap="nowrap">
        <PersonAvatar person={u} size={64} />
        <Stack gap={2} miw={0}>
          <Title order={2}>{u.displayName}</Title>
          <Text c="dimmed" size="sm">
            {u.username}
            {u.email ? ` · ${u.email}` : ''}
            {u.active ? '' : ` · ${t('people.inactive')}`}
          </Text>
          {u.groups.length > 0 && (
            <Group gap={4} mt={4} aria-label={t('people.groups')}>
              {u.groups.map((g) => (
                <Badge key={g.id} variant="light" color="gray" tt="none">
                  {g.name}
                </Badge>
              ))}
            </Group>
          )}
        </Stack>
      </Group>
      <Stack gap="xs">
        <Title order={3}>{t('people.activity')}</Title>
        <ActivityStream user={u.username} productFilter />
      </Stack>
    </Stack>
  );
}
