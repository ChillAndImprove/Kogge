// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Navigate, useLocation, useParams } from 'react-router';
import { useIssue } from './issueHooks';
import { IssueView } from './IssueView';

export function IssuePage() {
  const { key } = useParams();
  const location = useLocation();
  const wanted = (key ?? '').toUpperCase();
  const issue = useIssue(wanted);
  // A key the issue had before it moved (or its project's key changed) leads to its current one.
  if (issue.data && issue.data.key !== wanted) {
    return <Navigate to={`/browse/${issue.data.key}`} replace state={{ formerKey: wanted }} />;
  }
  const formerKey = (location.state as { formerKey?: string } | null)?.formerKey;
  return <IssueView key={key} issueKey={wanted} formerKey={formerKey} />;
}
