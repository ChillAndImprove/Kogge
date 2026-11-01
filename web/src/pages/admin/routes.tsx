// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Administration pages, loaded lazily by the router.
import { SimpleConfigPage } from './issues/SimpleConfig';

export { AdminLayout, IssueAdminLayout } from './AdminLayout';
export { AgentsPage } from './AgentsPage';
export { WebhooksPage } from './WebhooksPage';
export { AuditPage } from './AuditPage';
export { GroupsPage } from './GroupsPage';
export { OverviewPage } from './OverviewPage';
export { PermissionsPage } from './PermissionsPage';
export { SettingsPage } from './SettingsPage';
export { SSOPage } from './SSOPage';
export { StoragePage } from './StoragePage';
export { SystemPage } from './SystemPage';
export { UsersPage } from './UsersPage';
export { FieldsPage } from './issues/FieldsPage';
export { NotificationSchemeEditorPage, NotificationSchemesPage } from './issues/NotificationSchemesPage';
export { PermissionSchemeEditorPage, PermissionSchemesPage } from './issues/PermissionSchemesPage';
export { SecuritySchemeEditorPage, SecuritySchemesPage } from './issues/SecuritySchemesPage';
export { WorkflowEditorPage } from './issues/WorkflowEditor';
export { WorkflowsPage } from './issues/WorkflowsPage';

export const IssueTypesPage = () => <SimpleConfigPage kind="issue-types" />;
export const StatusesPage = () => <SimpleConfigPage kind="statuses" />;
export const PrioritiesPage = () => <SimpleConfigPage kind="priorities" />;
export const ResolutionsPage = () => <SimpleConfigPage kind="resolutions" />;
export const LinkTypesPage = () => <SimpleConfigPage kind="link-types" />;
export const RolesPage = () => <SimpleConfigPage kind="roles" />;
