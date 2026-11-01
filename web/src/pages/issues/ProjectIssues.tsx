// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useTranslation } from 'react-i18next';
import { PageHeader } from '../../components/PageHeader';
import { useProjectContext } from './ProjectLayout';
import { SearchView } from './SearchView';

export function ProjectIssues() {
  const { t } = useTranslation();
  const project = useProjectContext();
  return (
    <>
      <PageHeader title={t('issues.issuesNav')} subtitle={project.description || undefined} />
      <SearchView
        key={project.key}
        defaultQuery={`project = ${project.key} AND resolution = Unresolved ORDER BY priority DESC, updated DESC`}
      />
    </>
  );
}
