// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Card, Group, Stack, Text } from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { api, unwrap } from '../../api/client';
import { formatDate } from '../../app/format';
import { ErrorAlert } from '../../components/ErrorAlert';
import { PageHeader } from '../../components/PageHeader';
import { Pagination } from '../../components/Pagination';
import { UserBadge } from '../issues/shared';
import { CreatePageDialog } from './CreatePageDialog';
import { useSpaceContext } from './SpaceLayout';

/** Blog posts per page of the blog. */
const pageSize = 20;

/** The blog of a space, newest first, a page at a time; the page is kept in the address. */
export function BlogPage() {
  const { t } = useTranslation();
  const space = useSpaceContext();
  const [creating, setCreating] = useState(false);
  const [params, setParams] = useSearchParams();
  const page = Math.max(1, Number(params.get('page')) || 1);
  const posts = useQuery({
    queryKey: ['page', 'blog', space.key, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/wiki/spaces/{key}/blog', {
          params: { path: { key: space.key }, query: { limit: pageSize, offset: (page - 1) * pageSize } },
        }),
      ),
    placeholderData: (previous) => previous,
  });
  const total = posts.data?.total ?? 0;
  const pages = Math.ceil(total / pageSize);
  return (
    <>
      <PageHeader
        title={t('wiki.blog')}
        subtitle={space.name}
        actions={
          space.permissions.includes('page.edit') && (
            <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>
              {t('wiki.createBlogPost')}
            </Button>
          )
        }
      />
      <ErrorAlert error={posts.error} />
      {posts.isSuccess && total === 0 && <Text c="dimmed">{t('wiki.noBlogPosts')}</Text>}
      {total > 0 && (
        <Text size="sm" c="dimmed" mb="sm" aria-live="polite">
          {t('wiki.blogPostCount', { count: total })}
        </Text>
      )}
      <Stack gap="sm" maw={860}>
        {(posts.data?.pages ?? []).map((p) => (
          <Card
            key={p.id}
            withBorder
            padding="md"
            radius="md"
            component={Link}
            to={p.url}
            className="logbuch-post"
          >
            <Text fw={650} size="md" className="logbuch-post-title">
              {p.title}
            </Text>
            <Group gap="xs" mt={2} mb={6}>
              {p.updatedBy && <UserBadge user={p.updatedBy} size="xs" />}
              <Text size="xs" c="dimmed">
                {p.published ? formatDate(p.published) : ''}
              </Text>
            </Group>
            <Text size="sm" c="dimmed" lineClamp={2}>
              {p.excerpt}
            </Text>
          </Card>
        ))}
      </Stack>
      {pages > 1 && (
        <Group justify="center" mt="md">
          <Pagination
            total={pages}
            value={Math.min(page, pages)}
            onChange={(p) => {
              const next = new URLSearchParams(params);
              if (p > 1) next.set('page', String(p));
              else next.delete('page');
              setParams(next);
            }}
          />
        </Group>
      )}
      {creating && (
        <CreatePageDialog space={space} kind="blog" parentId={0} onClose={() => setCreating(false)} />
      )}
    </>
  );
}
