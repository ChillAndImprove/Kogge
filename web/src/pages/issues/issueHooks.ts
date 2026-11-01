// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { ApiError, api, unwrap } from '../../api/client';
import { notifyError } from '../../components/notify';
import type { Issue } from './shared';

export function useIssue(key: string) {
  return useQuery({
    queryKey: ['issue', key],
    queryFn: () => unwrap(api.GET('/api/v1/issues/{key}', { params: { path: { key } } })),
  });
}

/**
 * Changes fields of an issue. The change carries the version the user saw: when someone else changed the issue
 * meanwhile, it is refused, and the issue is loaded again.
 */
export function useUpdateIssue(issue: Pick<Issue, 'key' | 'version'>) {
  const { t } = useTranslation();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (fields: Record<string, unknown>) =>
      unwrap(
        api.PATCH('/api/v1/issues/{key}', {
          params: { path: { key: issue.key }, header: { 'If-Match': `"${issue.version}"` } },
          body: { fields: fields as Record<string, never> },
        }),
      ),
    onSuccess: (data) => {
      client.setQueryData(['issue', issue.key], data);
      void client.invalidateQueries({ queryKey: ['issue', issue.key, 'history'] });
      void client.invalidateQueries({ queryKey: ['board'] });
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 412) {
        notifyError(new Error(t('issues.staleIssue')));
        void client.invalidateQueries({ queryKey: ['issue', issue.key] });
      } else {
        notifyError(e);
      }
    },
  });
}
