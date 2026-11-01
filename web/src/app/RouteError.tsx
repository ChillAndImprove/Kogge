// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Center, Code, Group, Stack, Text, ThemeIcon, Title } from '@mantine/core';
import { IconAlertTriangle, IconRefresh } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { isRouteErrorResponse, useRouteError } from 'react-router';

/** Kogge was updated while the page was open: the parts of the old version are gone from the server. */
function stale(error: unknown): boolean {
  return (
    error instanceof Error &&
    /dynamically imported module|Importing a module script failed|error loading dynamically imported module|Failed to fetch/i.test(
      error.message,
    )
  );
}

/**
 * What a page shows when it fails: instead of a blank or technical screen, what happened and a way on — after an
 * update of Kogge, loading the page again.
 */
export function RouteError() {
  const { t } = useTranslation();
  const error = useRouteError();
  console.error(error);
  const updated = stale(error);
  const missing = isRouteErrorResponse(error) && error.status === 404;
  const detail = error instanceof Error ? error.message : isRouteErrorResponse(error) ? error.statusText : '';
  return (
    <Center mih="60vh" p="md">
      <Stack align="center" maw={560} gap="sm" ta="center" role="alert">
        <ThemeIcon size={48} radius="xl" variant="light" color="orange">
          <IconAlertTriangle size={26} />
        </ThemeIcon>
        <Title order={2}>
          {updated ? t('routeError.updated') : missing ? t('routeError.missing') : t('routeError.title')}
        </Title>
        <Text c="dimmed">
          {updated
            ? t('routeError.updatedText')
            : missing
              ? t('routeError.missingText')
              : t('routeError.text')}
        </Text>
        <Group>
          <Button leftSection={<IconRefresh size={16} />} onClick={() => window.location.reload()}>
            {t('routeError.reload')}
          </Button>
          <Button variant="default" component="a" href="/">
            {t('routeError.home')}
          </Button>
        </Group>
        {!updated && !missing && detail && (
          <Code block w="100%" ta="left">
            {detail}
          </Code>
        )}
      </Stack>
    </Center>
  );
}
