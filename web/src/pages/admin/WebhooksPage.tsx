// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useTranslation } from 'react-i18next';
import { PageHeader } from '../../components/PageHeader';
import { WebhooksEditor } from '../../components/Webhooks';

/** Webhooks that receive the events of the whole instance. */
export function WebhooksPage() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t('webhooks.title')} subtitle={t('webhooks.instanceHelp')} />
      <WebhooksEditor scope="instance" />
    </>
  );
}
