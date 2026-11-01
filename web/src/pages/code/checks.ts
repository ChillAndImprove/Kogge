// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { TFunction } from 'i18next';
import type { components } from '../../api/schema';

export type MergeCheck = components['schemas']['MergeCheck'];
export type StrategyCheck = components['schemas']['StrategyCheck'];

/** Words what a strategy needs of the branches, in the reader's language (the server's English as fallback). */
export function strategyText(t: TFunction, c: StrategyCheck): string {
  return t(`code.pr.strategyCheck.${c.reason}`, {
    commit: c.commit?.slice(0, 10),
    subject: c.subject,
    paths: (c.paths ?? []).join(', '),
    limit: c.limit,
    defaultValue: c.message,
  });
}

/** Words a merge check in the reader's language from its reason and numbers (the server's English as fallback). */
export function checkText(t: TFunction, c: MergeCheck): string {
  const v = c.values ?? {};
  const count = c.key === 'builds' ? (c.reason === 'failed' ? v.failed : v.successful) : v.count;
  const text = t(`code.pr.check.${c.key}.${c.reason}` as 'code.pr.check.draft.draft', {
    ...v,
    count,
    defaultValue: c.message,
  });
  if (c.key !== 'builds' || !v.skipped || c.reason === 'unavailable') return text;
  const skipped = t('code.pr.check.builds.skipped', { count: v.skipped });
  // Only skipped pipelines: the number of successful builds (none) says nothing.
  return c.reason === 'passed' && !v.successful ? skipped : `${text} ${skipped}`;
}
