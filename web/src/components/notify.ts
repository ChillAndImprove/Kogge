// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { notifications } from '@mantine/notifications';
import { errorMessage, isPreviewRefusal } from './ErrorAlert';

export function notifyError(error: unknown) {
  if (isPreviewRefusal(error)) return;
  notifications.show({ color: 'red', title: 'Kogge', message: errorMessage(error) });
}

export function notifySuccess(message: string) {
  notifications.show({ color: 'teal', message });
}
