// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import i18n from '../i18n';
import { formatBytes } from './format';

/**
 * The error of a file larger than an attachment may be (maxMb is the limit of the settings, from the session), or
 * null: such files are not sent at all, the server would refuse them.
 */
export function tooLarge(file: File, maxMb: number | undefined): Error | null {
  if (!maxMb || maxMb <= 0) return null;
  const max = maxMb * 1024 * 1024;
  if (file.size <= max) return null;
  return new Error(i18n.t('common.fileTooLarge', { name: file.name, max: formatBytes(max) }));
}

/** The files no larger than an attachment may be; onRefused hears of the others. */
export function attachable(files: File[], maxMb: number | undefined, onRefused: (e: Error) => void): File[] {
  return files.filter((f) => {
    const e = tooLarge(f, maxMb);
    if (e) onRefused(e);
    return !e;
  });
}
