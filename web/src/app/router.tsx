// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { ComponentType } from 'react';
import { createBrowserRouter, createHashRouter, Navigate, useLocation, useParams } from 'react-router';
import { isPreview } from '../showcase/preview';
import { Launcher } from '../pages/Launcher';
import { NotFoundPage } from '../pages/NotFoundPage';
import { PersonPage } from '../pages/PersonPage';
import { ProfilePage } from '../pages/profile/ProfilePage';
import { SearchAllPage } from '../pages/SearchAllPage';
import { RouteError } from './RouteError';
import { Root } from './Shell';
import { HafenShell, HellingShell, KontorShell, LogbuchShell, PlatformShell } from './shells';

/** Loads a page module on first use, keeping the editor and administration out of the initial bundle. */
function page<M>(load: () => Promise<M>, pick: (m: M) => ComponentType) {
  return async () => ({ Component: pick(await load()) });
}

const issues = () => import('../pages/issues/routes');
const agile = () => import('../pages/agile/routes');
const reports = () => import('../pages/agile/ReportsPage');
const dashboards = () => import('../pages/dashboards/DashboardsPage');
const wiki = () => import('../pages/wiki/routes');
const admin = () => import('../pages/admin/routes');
const code = () => import('../pages/code/routes');
const builds = () => import('../pages/builds/routes');
const developer = () => import('../pages/developer/ApiDocsPage');

/** Sends an address that moved to another place, keeping the rest of the path and the query. */
function Moved({ from, to }: { from: string; to: string }) {
  const location = useLocation();
  return <Navigate to={to + location.pathname.slice(from.length) + location.search} replace />;
}

/** Old addresses of the build pages, which were part of the repository pages before Helling. */
function MovedToHelling({ section }: { section: 'runs' | 'run' | 'environments' }) {
  const { key = '', repo = '', number = '' } = useParams();
  const base = `/builds/${key}/${repo}`;
  const to =
    section === 'run' ? `${base}/runs/${number}` : section === 'environments' ? `${base}/environments` : base;
  return <Navigate to={to} replace />;
}

// The preview runs from static files (GitHub Pages): its addresses are after the #.
export const router = (isPreview ? createHashRouter : createBrowserRouter)([
  {
    element: <Root />,
    errorElement: <RouteError />,
    children: [
      // The platform: overview of the products, administration of people and settings, the profile.
      {
        element: <PlatformShell />,
        children: [
          // Errors of a page show within the product's frame.
          {
            errorElement: <RouteError />,
            children: [
              { index: true, element: <Launcher /> },
              { path: 'login', element: <Navigate to="/" replace /> },
              { path: 'profile', element: <ProfilePage /> },
              { path: 'search', element: <SearchAllPage /> },
              { path: 'people/:username', element: <PersonPage /> },
              { path: 'api/docs', lazy: page(developer, (m) => m.ApiDocsPage) },
              {
                path: 'admin',
                lazy: page(admin, (m) => m.AdminLayout),
                children: [
                  { index: true, lazy: page(admin, (m) => m.OverviewPage) },
                  { path: 'users', lazy: page(admin, (m) => m.UsersPage) },
                  { path: 'groups', lazy: page(admin, (m) => m.GroupsPage) },
                  { path: 'permissions', lazy: page(admin, (m) => m.PermissionsPage) },
                  { path: 'settings', lazy: page(admin, (m) => m.SettingsPage) },
                  { path: 'sso', lazy: page(admin, (m) => m.SSOPage) },
                  { path: 'audit', lazy: page(admin, (m) => m.AuditPage) },
                  { path: 'system', lazy: page(admin, (m) => m.SystemPage) },
                  { path: 'storage', lazy: page(admin, (m) => m.StoragePage) },
                  { path: 'webhooks', lazy: page(admin, (m) => m.WebhooksPage) },
                ],
              },
              { path: 'admin/issues/*', element: <Moved from="/admin/issues" to="/issues/admin" /> },
              { path: 'admin/agents', element: <Navigate to="/builds/agents" replace /> },
            ],
          },
        ],
      },
      // Kontor: issues, boards, planning.
      {
        element: <KontorShell />,
        children: [
          // Errors of a page show within the product's frame.
          {
            errorElement: <RouteError />,
            children: [
              { path: 'issues', lazy: page(issues, (m) => m.KontorHome) },
              { path: 'issues/projects', lazy: page(issues, (m) => m.ProjectsPage) },
              { path: 'issues/search', lazy: page(issues, (m) => m.SearchPage) },
              { path: 'issues/filters', lazy: page(issues, (m) => m.FiltersPage) },
              { path: 'issues/bulk', lazy: page(issues, (m) => m.BulkPage) },
              { path: 'issues/bulk/:id', lazy: page(issues, (m) => m.BulkPage) },
              {
                path: 'issues/admin',
                lazy: page(admin, (m) => m.IssueAdminLayout),
                children: [
                  { index: true, element: <Navigate to="/issues/admin/types" replace /> },
                  { path: 'types', lazy: page(admin, (m) => m.IssueTypesPage) },
                  { path: 'statuses', lazy: page(admin, (m) => m.StatusesPage) },
                  { path: 'priorities', lazy: page(admin, (m) => m.PrioritiesPage) },
                  { path: 'resolutions', lazy: page(admin, (m) => m.ResolutionsPage) },
                  { path: 'link-types', lazy: page(admin, (m) => m.LinkTypesPage) },
                  { path: 'roles', lazy: page(admin, (m) => m.RolesPage) },
                  { path: 'fields', lazy: page(admin, (m) => m.FieldsPage) },
                  { path: 'workflows', lazy: page(admin, (m) => m.WorkflowsPage) },
                  { path: 'workflows/:id', lazy: page(admin, (m) => m.WorkflowEditorPage) },
                  { path: 'permission-schemes', lazy: page(admin, (m) => m.PermissionSchemesPage) },
                  { path: 'permission-schemes/:id', lazy: page(admin, (m) => m.PermissionSchemeEditorPage) },
                  { path: 'security-schemes', lazy: page(admin, (m) => m.SecuritySchemesPage) },
                  { path: 'security-schemes/:id', lazy: page(admin, (m) => m.SecuritySchemeEditorPage) },
                  { path: 'notification-schemes', lazy: page(admin, (m) => m.NotificationSchemesPage) },
                  {
                    path: 'notification-schemes/:id',
                    lazy: page(admin, (m) => m.NotificationSchemeEditorPage),
                  },
                ],
              },
              { path: 'browse/:key', lazy: page(issues, (m) => m.IssuePage) },
              {
                path: 'projects/:key',
                lazy: page(issues, (m) => m.ProjectLayout),
                children: [
                  { index: true, lazy: page(issues, (m) => m.ProjectIssues) },
                  { path: 'epics', lazy: page(issues, (m) => m.ProjectEpics) },
                  { path: 'releases', lazy: page(issues, (m) => m.ProjectReleases) },
                  { path: 'releases/:versionId', lazy: page(issues, (m) => m.VersionPage) },
                  { path: 'components', lazy: page(issues, (m) => m.ProjectComponents) },
                  { path: 'activity', lazy: page(issues, (m) => m.ProjectActivity) },
                  { path: 'settings', lazy: page(issues, (m) => m.ProjectSettings) },
                  { path: 'boards', lazy: page(agile, (m) => m.BoardsPage) },
                  { path: 'boards/:boardId', lazy: page(agile, (m) => m.BoardPage) },
                  { path: 'boards/:boardId/backlog', lazy: page(agile, (m) => m.BacklogPage) },
                  { path: 'boards/:boardId/reports', lazy: page(reports, (m) => m.ReportsPage) },
                  { path: 'boards/:boardId/settings', lazy: page(agile, (m) => m.BoardSettingsPage) },
                ],
              },
              { path: 'dashboards', lazy: page(dashboards, (m) => m.DashboardsPage) },
              { path: 'dashboards/:id', lazy: page(dashboards, (m) => m.DashboardPage) },
            ],
          },
        ],
      },
      // Logbuch: the wiki.
      {
        element: <LogbuchShell />,
        children: [
          // Errors of a page show within the product's frame.
          {
            errorElement: <RouteError />,
            children: [
              { path: 'wiki', lazy: page(wiki, (m) => m.LogbuchHome) },
              { path: 'wiki/spaces', lazy: page(wiki, (m) => m.SpaceDirectory) },
              { path: 'wiki/search', lazy: page(wiki, (m) => m.WikiSearch) },
              {
                path: 'wiki/spaces/:key',
                lazy: page(wiki, (m) => m.SpaceLayout),
                children: [
                  { index: true, lazy: page(wiki, (m) => m.SpaceHome) },
                  { path: 'pages/:pageId/edit', lazy: page(wiki, (m) => m.PageEditor) },
                  { path: 'pages/:pageId/history', lazy: page(wiki, (m) => m.PageHistory) },
                  { path: 'pages/:pageId/:slug?', lazy: page(wiki, (m) => m.PageView) },
                  { path: 'blog', lazy: page(wiki, (m) => m.BlogPage) },
                  { path: 'activity', lazy: page(wiki, (m) => m.SpaceActivity) },
                  { path: 'settings', lazy: page(wiki, (m) => m.SpaceSettings) },
                ],
              },
            ],
          },
        ],
      },
      // Hafen: Git hosting and pull requests.
      {
        element: <HafenShell />,
        children: [
          // Errors of a page show within the product's frame.
          {
            errorElement: <RouteError />,
            children: [
              { path: 'code', lazy: page(code, (m) => m.HafenHome) },
              { path: 'code/repos', lazy: page(code, (m) => m.CodeHome) },
              { path: 'code/pull-requests', lazy: page(code, (m) => m.PullRequestsHome) },
              { path: 'code/:key', lazy: page(code, (m) => m.ProjectPage) },
              {
                path: 'code/:key/:repo',
                lazy: page(code, (m) => m.RepoLayout),
                children: [
                  { index: true, lazy: page(code, (m) => m.Source) },
                  { path: 'src/*', lazy: page(code, (m) => m.Source) },
                  { path: 'commits', lazy: page(code, (m) => m.Commits) },
                  { path: 'commits/:sha', lazy: page(code, (m) => m.CommitPage) },
                  { path: 'branches', lazy: page(code, (m) => m.Branches) },
                  { path: 'tags', lazy: page(code, (m) => m.Branches) },
                  { path: 'compare', lazy: page(code, (m) => m.Compare) },
                  { path: 'search', lazy: page(code, (m) => m.CodeSearch) },
                  { path: 'pull-requests', lazy: page(code, (m) => m.PullRequests) },
                  { path: 'pull-requests/new', lazy: page(code, (m) => m.NewPullRequest) },
                  { path: 'pull-requests/:number', lazy: page(code, (m) => m.PullRequestPage) },
                  { path: 'activity', lazy: page(code, (m) => m.RepoActivity) },
                  { path: 'settings', lazy: page(code, (m) => m.RepoSettings) },
                ],
              },
              { path: 'code/:key/:repo/builds', element: <MovedToHelling section="runs" /> },
              { path: 'code/:key/:repo/builds/:number', element: <MovedToHelling section="run" /> },
              { path: 'code/:key/:repo/environments', element: <MovedToHelling section="environments" /> },
            ],
          },
        ],
      },
      // Helling: builds and deployments.
      {
        element: <HellingShell />,
        children: [
          // Errors of a page show within the product's frame.
          {
            errorElement: <RouteError />,
            children: [
              { path: 'builds', lazy: page(builds, (m) => m.BuildsHome) },
              { path: 'builds/agents', lazy: page(admin, (m) => m.AgentsPage) },
              { path: 'builds/templates', lazy: page(builds, (m) => m.TemplatesPage) },
              { path: 'builds/:key', lazy: page(builds, (m) => m.HellingProject) },
              {
                path: 'builds/:key/:repo',
                lazy: page(builds, (m) => m.PipelineLayout),
                children: [
                  { index: true, lazy: page(builds, (m) => m.Runs) },
                  { path: 'runs/:number', lazy: page(builds, (m) => m.RunPage) },
                  { path: 'environments', lazy: page(builds, (m) => m.Environments) },
                  { path: 'settings', lazy: page(builds, (m) => m.PipelineSettingsPage) },
                ],
              },
            ],
          },
        ],
      },
      { element: <PlatformShell />, children: [{ path: '*', element: <NotFoundPage /> }] },
    ],
  },
]);
