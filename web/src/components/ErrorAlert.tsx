// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Alert, Button, Group, Stack, Text, type MantineSpacing } from '@mantine/core';
import { IconAlertCircle, IconAlertTriangle, IconLock, IconRefresh, IconSearch } from '@tabler/icons-react';
import { QueryClientContext } from '@tanstack/react-query';
import { use } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../api/client';
import i18n from '../i18n';

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    // Errors the interface knows by their code are said in the reader's language; others as the server says them.
    const key = `errors.api.${error.code}`;
    const message = error.code && i18n.exists(key) ? i18n.t(key as 'errors.forbidden') : error.message;
    const fields = error.errors.map((e) => e.message).filter(Boolean);
    return fields.length ? `${message} (${fields.join('; ')})` : message;
  }
  if (error instanceof Error) return error.message;
  return String(error);
}

/** Errors of changes the preview refused: its dialog explains them. */
export function isPreviewRefusal(error: unknown): boolean {
  return error instanceof ApiError && error.code === 'preview.deploy';
}

/**
 * What kind of error it is, which decides how it is shown: the expected refusals (no permission, gone, a value the
 * server does not accept) are calm; only failures (the server or the network) are red.
 */
export type ErrorKind = 'forbidden' | 'notFound' | 'invalid' | 'failure';

export function errorKind(error: unknown): ErrorKind {
  if (!(error instanceof ApiError)) return 'failure';
  if (error.status === 403) return 'forbidden';
  if (error.status === 404 || error.status === 410) return 'notFound';
  if (error.status >= 400 && error.status < 500 && error.status !== 401 && error.status !== 408)
    return 'invalid';
  return 'failure';
}

interface Props {
  error: unknown;
  /** Loads again what failed; by default the query whose error this is, if it is one. */
  onRetry?: () => void;
  my?: MantineSpacing;
}

/**
 * An error, shown by its kind: "No access" with a lock (403) and "Not found" (404) in calm grey, a value the server
 * refused (409, 422 …) as an orange note with just its message, and failures of the server or the network in red as
 * "Something went wrong" with "Try again". Pages that know beforehand that something is not allowed show NoAccess
 * instead.
 */
export function ErrorAlert({ error, onRetry, my = 'sm' }: Props) {
  const { t } = useTranslation();
  const client = use(QueryClientContext);
  if (!error || isPreviewRefusal(error)) return null;
  const kind = errorKind(error);
  const message = errorMessage(error);
  if (kind === 'forbidden' || kind === 'notFound') {
    return (
      <Alert
        color="gray"
        icon={kind === 'forbidden' ? <IconLock size={18} /> : <IconSearch size={18} />}
        title={kind === 'forbidden' ? t('errors.noAccess') : t('errors.notFoundTitle')}
        my={my}
        data-error-kind={kind}
      >
        {message}
      </Alert>
    );
  }
  if (kind === 'invalid') {
    return (
      <Alert color="orange" icon={<IconAlertCircle size={18} />} my={my} data-error-kind={kind}>
        {message}
      </Alert>
    );
  }
  // A failed query can be loaded again from here; a failed change is tried again with its own button.
  const failed = client?.getQueryCache().findAll({ predicate: (q) => q.state.error === error }) ?? [];
  const retry =
    onRetry ??
    (failed.length > 0
      ? () => void client?.refetchQueries({ predicate: (q) => q.state.error === error })
      : undefined);
  return (
    <Alert
      color="red"
      icon={<IconAlertTriangle size={18} />}
      title={t('common.error')}
      my={my}
      data-error-kind={kind}
    >
      <Stack gap="xs" align="flex-start">
        <Text size="sm" style={{ overflowWrap: 'anywhere' }}>
          {message}
        </Text>
        {retry && (
          <Group>
            <Button size="xs" variant="default" leftSection={<IconRefresh size={14} />} onClick={retry}>
              {t('common.retry')}
            </Button>
          </Group>
        )}
      </Stack>
    </Alert>
  );
}
