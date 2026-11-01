// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useTranslation } from 'react-i18next';
import { PageHeader } from '../../components/PageHeader';
import { SearchView } from './SearchView';

export function SearchPage() {
  const { t } = useTranslation();
  return (
    <>
      <PageHeader title={t('issues.search')} />
      <SearchView defaultQuery="assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC" />
    </>
  );
}
