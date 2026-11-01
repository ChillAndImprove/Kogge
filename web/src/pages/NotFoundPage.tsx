// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Card, Group, SimpleGrid, Stack, Text, TextInput } from '@mantine/core';
import { IconArrowLeft, IconMapQuestion, IconSearch } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { EmptyState } from '../components/EmptyState';
import { productList } from '../products';

/**
 * What an address that leads nowhere shows (also to people who may not see what is there): what happened, and the
 * ways on — a search of everything, the products, and back to where one came from.
 */
export function NotFoundPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  // The router counts the pages of this visit: "Back" only where there is one to go back to.
  const canGoBack = ((window.history.state as { idx?: number } | null)?.idx ?? 0) > 0;
  return (
    <Stack maw={720} mx="auto" gap="lg" py="xl">
      <EmptyState
        icon={IconMapQuestion}
        title={t('errors.notFound')}
        description={t('errors.notFoundText')}
        action={
          <form
            role="search"
            onSubmit={(e) => {
              e.preventDefault();
              const q = query.trim();
              if (q) navigate(`/search?q=${encodeURIComponent(q)}`);
            }}
          >
            <Group gap="xs" wrap="nowrap">
              <TextInput
                w={280}
                maw="60vw"
                leftSection={<IconSearch size={16} />}
                placeholder={t('nav.search')}
                aria-label={t('nav.search')}
                value={query}
                onChange={(e) => setQuery(e.currentTarget.value)}
              />
              <Button type="submit">{t('common.search')}</Button>
            </Group>
          </form>
        }
        secondary={
          canGoBack ? (
            <Button variant="default" leftSection={<IconArrowLeft size={16} />} onClick={() => navigate(-1)}>
              {t('common.back')}
            </Button>
          ) : (
            <Button variant="default" component={Link} to="/">
              {t('routeError.home')}
            </Button>
          )
        }
      />
      <Stack gap="xs">
        <Text size="sm" c="dimmed" ta="center">
          {t('notFound.products')}
        </Text>
        <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="sm">
          {productList.map((p) => (
            <Card
              key={p.id}
              withBorder
              component={Link}
              to={p.home}
              className="kogge-product-card"
              padding="sm"
            >
              <Group gap="sm" wrap="nowrap">
                <p.Logo size={32} />
                <Stack gap={0} miw={0}>
                  <Text fw={600} size="sm">
                    {p.name}
                  </Text>
                  <Text size="xs" c="dimmed" truncate>
                    {t(`products.${p.id}.tagline`)}
                  </Text>
                </Stack>
              </Group>
            </Card>
          ))}
        </SimpleGrid>
      </Stack>
    </Stack>
  );
}
