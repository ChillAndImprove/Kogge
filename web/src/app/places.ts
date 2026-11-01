// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useMatch } from 'react-router';

/** Where the user is: the containers on screen, into which the create buttons and the quick search create. */

/** The issue project on screen (key): of a project's pages or of an issue. */
export function useIssueProjectPlace(): string | undefined {
  const projectPage = useMatch('/projects/:key/*');
  const issuePage = useMatch('/browse/:key');
  return (projectPage?.params.key ?? issuePage?.params.key?.replace(/-\d+$/, ''))?.toUpperCase();
}

/** The space and page on screen, which new pages go into (and below). */
export function useWikiPlace(): { space?: string; pageId?: number } {
  const spacePage = useMatch('/wiki/spaces/:key/*');
  const pageRoute = useMatch('/wiki/spaces/:key/pages/:pageId/*');
  return {
    space: spacePage?.params.key?.toUpperCase(),
    pageId: pageRoute ? Number(pageRoute.params.pageId) : undefined,
  };
}

/** The code project on screen (key), which new repositories go into. */
export function useCodeProjectPlace(): string | undefined {
  const projectPage = useMatch('/code/:key/*');
  const current = projectPage?.params.key;
  return current && !['repos', 'pull-requests'].includes(current) ? current.toUpperCase() : undefined;
}
