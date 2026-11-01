// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useEffect } from 'react';
import { api } from '../api/client';

/** The kinds of things whose visits the quick search shows; pages include blog posts. */
export type VisitKind =
  'issue' | 'project' | 'page' | 'space' | 'code-project' | 'repository' | 'pull-request' | 'run';

/**
 * Remembers that the user opened something, for the quick search's recently opened (kept on the server, so it
 * follows the user from device to device). It records once per thing shown; failures are of no concern to the
 * page.
 */
export function useRecordVisit(kind: VisitKind, id: number | undefined) {
  useEffect(() => {
    if (!id) return;
    api.POST('/api/v1/me/recent', { body: { kind, id } }).catch(() => {});
  }, [kind, id]);
}
