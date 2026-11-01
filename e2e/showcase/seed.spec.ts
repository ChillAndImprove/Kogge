// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test, type Browser, type Page } from '@playwright/test';
import { agentBinary } from '../tests/agent';
import { apiCall, apiGet } from '../tests/fixtures';

// The sample content of the preview: people and groups, an issue project with epics, sprints, filters and a
// dashboard, a second project on a Kanban board, an issue moved between them and a bulk change, a wiki with team
// spaces (with layouts and exports), a personal and an archived one, code with pull requests (one merged by
// rebasing, one waiting to be merged automatically), and pipelines — one a graph of jobs that wait for the jobs they
// need — that a build agent of this machine runs. It fills an empty Kogge — for the English preview one started with
// KOGGE_DEFAULT_LOCALE=en, but a German one works as well; people and groups of the same names are used as they
// are. On a Kogge that has the content already it does nothing, and one that an earlier run left half-way it
// refuses.

const adminPassword = process.env.KOGGE_SHOWCASE_ADMIN_PASSWORD ?? 'Preview-Seed-Secret-2026';
const password = 'Sample-Person-Secret-2026';

/** The names of the built-in configuration that the seed uses, in English and German installations. */
const builtin = {
  Developers: ['Developers', 'Entwickler'],
  Users: ['Users', 'Benutzer'],
  'In Progress': ['In Progress', 'In Arbeit'],
  'In Review': ['In Review', 'In Prüfung'],
  Done: ['Done', 'Erledigt'],
  Blocks: ['Blocks', 'Blockiert'],
  Relates: ['Relates', 'Bezieht sich'],
} as const;
type Builtin = keyof typeof builtin;

/** The built-in priorities by their icons, which do not depend on the language. */
const priorityIcons: Record<string, string> = {
  Highest: 'highest',
  High: 'high',
  Medium: 'medium',
  Low: 'low',
  Lowest: 'lowest',
};

/** The object of a list whose name is one of names. */
function named<T extends { name: string }>(list: T[], what: string, names: readonly string[]): T {
  const found = list.find((x) => names.includes(x.name));
  if (!found) throw new Error(`No ${what} named ${names.join(' or ')}`);
  return found;
}

const people = {
  lena: 'Lena Hofmann',
  jonas: 'Jonas Weber',
  mara: 'Mara Lindner',
  tobias: 'Tobias Krämer',
  sophie: 'Sophie Wagner',
  felix: 'Felix Brandt',
} as const;
type Who = keyof typeof people;

/** A day relative to today, YYYY-MM-DD. */
function day(offset: number): string {
  const d = new Date();
  d.setDate(d.getDate() + offset);
  return d.toISOString().slice(0, 10);
}

async function signedIn(browser: Browser, baseURL: string, username: string, pw: string): Promise<Page> {
  const context = await browser.newContext({ baseURL, locale: 'en-GB' });
  const page = await context.newPage();
  await page.goto('/');
  await page.getByRole('textbox', { name: 'User name' }).fill(username);
  await page.getByRole('textbox', { name: 'Password' }).fill(pw);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: /Hello/ })).toBeVisible();
  return page;
}

// --- documents ---

type Node = Record<string, unknown>;
type Inline = string | Node;
const text = (t: string, ...marks: string[]): Node =>
  marks.length
    ? { type: 'text', text: t, marks: marks.map((m) => ({ type: m })) }
    : { type: 'text', text: t };
const link = (t: string, href: string): Node => ({
  type: 'text',
  text: t,
  marks: [{ type: 'link', attrs: { href } }],
});
const inline = (c: Inline[]): Node[] => c.map((x) => (typeof x === 'string' ? text(x) : x));
const p = (...c: Inline[]): Node => ({ type: 'paragraph', content: inline(c) });
const h = (level: number, t: string): Node => ({
  type: 'heading',
  attrs: { level },
  content: [text(t)],
});
const item = (c: Inline | Inline[]) => ({
  type: 'listItem',
  content: [p(...(Array.isArray(c) ? c : [c]))],
});
const ul = (...items: (Inline | Inline[])[]): Node => ({
  type: 'bulletList',
  content: items.map(item),
});
const ol = (...items: (Inline | Inline[])[]): Node => ({
  type: 'orderedList',
  content: items.map(item),
});
const tasks = (...items: [boolean, ...Inline[]][]): Node => ({
  type: 'taskList',
  content: items.map(([checked, ...c]) => ({
    type: 'taskItem',
    attrs: { checked },
    content: [p(...c)],
  })),
});
const code = (language: string, src: string): Node => ({
  type: 'codeBlock',
  attrs: { language },
  content: [text(src)],
});
const panel = (kind: string, ...content: Node[]): Node => ({
  type: 'panel',
  attrs: { kind },
  content,
});
const expand = (title: string, ...content: Node[]): Node => ({
  type: 'expand',
  attrs: { title },
  content,
});
const macro = (name: string, params: Record<string, unknown> = {}): Node => ({
  type: 'macro',
  attrs: { name, params },
});
const status = (t: string, color: string): Node => ({
  type: 'status',
  attrs: { text: t, color },
});
const issueRef = (key: string): Node => ({
  type: 'issue',
  attrs: { key, display: 'inline' },
});
const mention = (user: number, label: string): Node => ({
  type: 'mention',
  attrs: { user, label },
});
const table = (header: string[], rows: Inline[][]): Node => ({
  type: 'table',
  content: [
    {
      type: 'tableRow',
      content: header.map((c) => ({ type: 'tableHeader', content: [p(c)] })),
    },
    ...rows.map((r) => ({
      type: 'tableRow',
      content: r.map((c) => ({ type: 'tableCell', content: [p(c)] })),
    })),
  ],
});
/** A layout of two or three columns side by side, each with its blocks. */
const columns = (layout: 'equal' | 'left' | 'right' | 'sides', ...cols: Node[][]): Node => ({
  type: 'columns',
  attrs: { layout },
  content: cols.map((content) => ({ type: 'column', content })),
});
const doc = (...content: Node[]) => ({ type: 'doc', content });

test('sample content of the preview', async ({ browser, baseURL }) => {
  const admin = await signedIn(browser, baseURL!, 'admin', adminPassword);
  type Account = { id: number; username: string };
  const account = async (username: string) =>
    ((await apiGet(admin, `/api/v1/admin/users?query=${username}&limit=100`)).items as Account[]).find(
      (u) => u.username === username,
    );
  // lena starts the content and her favourite space ends it.
  if (await account('lena')) {
    const lena = await signedIn(browser, baseURL!, 'lena', password);
    const favourites = await lena.request.get('/api/v1/wiki/spaces?favourite=true&limit=100');
    const done =
      favourites.ok() && ((await favourites.json()).items as { key: string }[]).some((s) => s.key === 'ENG');
    test.skip(done, 'The sample content exists already.');
    throw new Error(
      'The sample content is incomplete: an earlier run stopped half-way. Seed an empty Kogge.',
    );
  }

  // --- people and groups ---
  const ids = {} as Record<Who, number>;
  for (const [username, displayName] of Object.entries(people)) {
    const found = await account(username);
    if (found) {
      // Someone of the same name: the seed signs in as them.
      await apiCall(admin, 'PUT', `/api/v1/admin/users/${found.id}/password`, { password });
      ids[username as Who] = found.id;
      continue;
    }
    const u = await apiCall(admin, 'POST', '/api/v1/admin/users', {
      username,
      displayName,
      password,
      email: `${username}@example.org`,
    });
    ids[username as Who] = u.id;
  }
  for (const permission of ['admin', 'project.create', 'space.create', 'codeproject.create']) {
    await apiCall(admin, 'POST', '/api/v1/admin/global-permissions', {
      permission,
      principalType: 'user',
      principalId: ids.lena,
    });
  }
  await apiCall(admin, 'POST', '/api/v1/admin/global-permissions', {
    permission: 'space.create',
    principalType: 'user',
    principalId: ids.sophie,
  });
  // A group of Kogge's own with the name is used as it is; one of the identity provider (whose members are managed
  // there) leaves the name to it.
  const group = async (name: string, description: string, members: Who[]) => {
    type Group = { id: number; name: string; source: string };
    const same = (
      (await apiGet(admin, `/api/v1/admin/groups?query=${encodeURIComponent(name)}&limit=100`))
        .items as Group[]
    ).filter((g) => g.name.toLowerCase() === name.toLowerCase());
    const g =
      same.find((x) => x.source === 'local') ??
      (await apiCall(admin, 'POST', '/api/v1/admin/groups', {
        name: same.length > 0 ? `${name} (sample)` : name,
        description,
      }));
    for (const m of members) await apiCall(admin, 'PUT', `/api/v1/admin/groups/${g.id}/members/${ids[m]}`);
    return g.id as number;
  };
  const developers = await group('developers', 'Everyone who builds the shop', [
    'lena',
    'jonas',
    'mara',
    'tobias',
  ]);
  const support = await group('support', 'Customer support', ['sophie', 'lena']);
  const design = await group('design', 'Product design', ['felix']);

  // Everyone signs in once their groups exist.
  const as = {} as Record<Who, Page>;
  for (const who of Object.keys(people) as Who[]) as[who] = await signedIn(browser, baseURL!, who, password);
  const call = (who: Who, method: 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, body?: unknown) =>
    apiCall(as[who], method, path, body);
  const get = (who: Who, path: string) => apiGet(as[who], path);

  // --- Kontor ---
  const cfg = await get('lena', '/api/v1/issue-config');
  const typeID = (icon: string) => cfg.issueTypes.find((t: { icon: string }) => t.icon === icon).id as number;
  const prio = (name: string) =>
    cfg.priorities.find((x: { icon: string }) => x.icon === priorityIcons[name]).id as number;
  const role = (name: Builtin) =>
    named(cfg.roles as { id: number; name: string }[], 'role', builtin[name]).id;
  const linkType = (name: Builtin) =>
    named(cfg.linkTypes as { id: number; name: string }[], 'link type', builtin[name]).id;
  const points = cfg.fields.find((f: { name: string }) => f.name === 'Story Points')?.key as
    string | undefined;
  // Queries name the issue type as the installation does.
  const bug = JSON.stringify(cfg.issueTypes.find((t: { icon: string }) => t.icon === 'bug').name as string);
  const openBugsQuery = `project = SHOP AND type = ${bug} AND statusCategory != Done ORDER BY priority DESC`;

  await call('lena', 'POST', '/api/v1/projects', {
    key: 'SHOP',
    name: 'Webshop',
    template: 'scrum',
    description: 'The online shop: catalogue, checkout, payments and customer accounts.',
  });
  await call('lena', 'POST', `/api/v1/projects/SHOP/roles/${role('Developers')}/members`, {
    type: 'group',
    id: developers,
  });
  for (const id of [support, design]) {
    await call('lena', 'POST', `/api/v1/projects/SHOP/roles/${role('Users')}/members`, { type: 'group', id });
  }
  const comp: Record<string, number> = {};
  for (const [name, description] of [
    ['Catalogue', 'Products, categories and search'],
    ['Checkout', 'Basket, checkout and order confirmation'],
    ['Payments', 'Cards, invoices and refunds'],
    ['Accounts', 'Sign-in, profiles and saved addresses'],
  ]) {
    comp[name] = (
      await call('lena', 'POST', '/api/v1/projects/SHOP/components', {
        name,
        description,
      })
    ).id;
  }
  const version = (name: string, description: string, start: number, release: number) =>
    call('lena', 'POST', '/api/v1/projects/SHOP/versions', {
      name,
      description,
      startDate: day(start),
      releaseDate: day(release),
    }).then((v) => v.id as number);
  const v10 = await version('1.0', 'The first public release', -60, -21);
  const v11 = await version('1.1', 'Saved payment methods and faster search', -20, 14);
  const v20 = await version('2.0', 'An accessible checkout and a wish list', 15, 90);

  interface IssueSpec {
    project?: string;
    type: string;
    summary: string;
    md?: string;
    by?: Who;
    assignee?: Who;
    priority?: string;
    labels?: string[];
    components?: string[];
    fix?: number[];
    parent?: string;
    points?: number;
    estimate?: string;
    due?: number;
  }
  const issue = async (s: IssueSpec): Promise<string> => {
    const created = await call(s.by ?? 'lena', 'POST', '/api/v1/issues', {
      project: s.project ?? 'SHOP',
      typeId: typeID(s.type),
      summary: s.summary,
      descriptionMarkdown: s.md,
      parent: s.parent,
    });
    const fields: Record<string, unknown> = {};
    if (s.priority) fields.priority = prio(s.priority);
    if (s.assignee) fields.assignee = ids[s.assignee];
    if (s.labels) fields.labels = s.labels;
    if (s.components) fields.components = s.components.map((c) => comp[c]);
    if (s.fix) fields.fixVersions = s.fix;
    if (s.points && points) fields[points] = s.points;
    if (s.estimate) fields.originalEstimate = s.estimate;
    if (s.due !== undefined) fields.duedate = day(s.due);
    if (Object.keys(fields).length > 0) {
      await call('lena', 'PATCH', `/api/v1/issues/${created.key}`, { fields });
    }
    return created.key as string;
  };
  const moveTo = async (who: Who, key: string, statusName: Builtin) => {
    const transitions = await get(who, `/api/v1/issues/${key}/transitions`);
    const names: readonly string[] = builtin[statusName];
    const t = transitions.find((x: { to: { name: string } }) => names.includes(x.to.name));
    expect(t, `${key}: no transition to ${statusName}`).toBeTruthy();
    await call(who, 'POST', `/api/v1/issues/${key}/transitions`, {
      transitionId: t.id,
    });
  };
  const comment = (who: Who, key: string, markdown: string) =>
    call(who, 'POST', `/api/v1/issues/${key}/comments`, { markdown });

  const savedCards = await issue({
    type: 'epic',
    summary: 'Saved payment methods',
    md: 'Customers keep their cards for the next order. Tokens only — the shop never sees card numbers.',
    labels: ['payments'],
    fix: [v11],
  });
  const search = await issue({
    type: 'epic',
    summary: 'Faster search',
    md: 'Suggestions while typing, filters and tolerant matching.',
    fix: [v10, v11],
  });
  const accessible = await issue({
    type: 'epic',
    summary: 'Accessible checkout',
    md: 'The checkout meets WCAG 2.1 AA: keyboard, screen readers and contrast.',
    fix: [v20],
  });
  const payCard = await issue({
    type: 'story',
    summary: 'Pay with a saved card',
    md: [
      'As a returning customer I pay with a card I saved before, without typing it again.',
      '',
      '**Acceptance criteria**',
      '- The checkout offers saved cards first',
      '- An expired card is shown but cannot be chosen',
      '- 3-D Secure still runs when the bank asks for it',
    ].join('\n'),
    parent: savedCards,
    assignee: 'jonas',
    priority: 'High',
    components: ['Payments', 'Checkout'],
    fix: [v11],
    points: 5,
    labels: ['payments'],
  });
  const cardBrand = await issue({
    type: 'story',
    summary: 'Show the card brand and the last four digits',
    parent: savedCards,
    assignee: 'mara',
    components: ['Payments'],
    fix: [v11],
    points: 3,
  });
  const vault = await issue({
    type: 'task',
    summary: 'Encrypt card tokens with the vault key',
    md: 'Tokens at rest are encrypted with the key from the vault; rotating the key re-encrypts them in the background.',
    parent: savedCards,
    assignee: 'mara',
    priority: 'Highest',
    components: ['Payments'],
    fix: [v11],
    points: 3,
    labels: ['security'],
  });
  const lostCards = await issue({
    type: 'bug',
    by: 'tobias',
    summary: 'Saved cards disappear after signing in again',
    md: [
      '**Steps**',
      '1. Save a card at checkout',
      '2. Sign out and sign in again',
      '3. Open *Account → Payment methods*',
      '',
      '**Expected** the saved card is listed. **Actual** the list is empty until the page is reloaded.',
    ].join('\n'),
    assignee: 'jonas',
    priority: 'High',
    components: ['Accounts', 'Payments'],
    fix: [v11],
    points: 2,
  });
  const suggestions = await issue({
    type: 'story',
    summary: 'Search suggestions while typing',
    parent: search,
    assignee: 'mara',
    components: ['Catalogue'],
    fix: [v10],
    points: 5,
  });
  const umlauts = await issue({
    type: 'bug',
    by: 'sophie',
    summary: 'Search finds nothing for words with umlauts',
    md: 'Customers searching for *Kühlschrank* get no results, *Kuhlschrank* finds the products.',
    assignee: 'jonas',
    priority: 'High',
    components: ['Catalogue'],
    fix: [v10],
    points: 2,
  });
  const priceFilter = await issue({
    type: 'story',
    summary: 'Filter results by price and brand',
    parent: search,
    components: ['Catalogue'],
    fix: [v20],
    points: 8,
  });
  const keyboard = await issue({
    type: 'story',
    summary: 'Keyboard navigation through the checkout',
    md: 'Every step of the checkout works with the keyboard alone, with a visible focus.',
    parent: accessible,
    assignee: 'lena',
    components: ['Checkout'],
    fix: [v20],
    points: 5,
    labels: ['accessibility'],
  });
  const contrast = await issue({
    type: 'task',
    by: 'felix',
    summary: 'Order summary reaches WCAG AA contrast',
    parent: accessible,
    components: ['Checkout'],
    fix: [v20],
    points: 2,
    labels: ['accessibility'],
  });
  const refunds = await issue({
    type: 'bug',
    by: 'sophie',
    summary: 'Partial refunds are rounded to whole euros',
    md: 'A refund of 12.49 € reached the customer as 12.00 €. Three customers wrote to support this week.',
    assignee: 'mara',
    priority: 'Highest',
    components: ['Payments'],
    fix: [v11],
    points: 3,
  });
  const confirmation = await issue({
    type: 'story',
    summary: 'Order confirmation e-mail in German and English',
    assignee: 'lena',
    components: ['Checkout'],
    fix: [v10],
    points: 3,
  });
  const sdk = await issue({
    type: 'task',
    summary: 'Upgrade the payment provider SDK',
    components: ['Payments'],
    fix: [v11],
    estimate: '1d',
  });
  const savedApi = await issue({
    type: 'subtask',
    summary: 'API for saved cards',
    parent: payCard,
    assignee: 'jonas',
    estimate: '1d',
  });
  const savedList = await issue({
    type: 'subtask',
    summary: 'Card list in the account settings',
    parent: payCard,
    assignee: 'jonas',
    estimate: '4h',
  });
  const decimals = await issue({
    type: 'bug',
    by: 'sophie',
    summary: 'Basket total shows three decimals for some currencies',
    priority: 'Medium',
    components: ['Checkout'],
  });
  const wishlist = await issue({
    type: 'story',
    by: 'felix',
    summary: 'Wish list',
    md: 'Customers keep products for later and get an e-mail when the price drops.',
    priority: 'Low',
    fix: [v20],
    points: 8,
  });
  const loadTest = await issue({
    type: 'task',
    summary: 'Load test the checkout before 1.1',
    assignee: 'tobias',
    components: ['Checkout'],
    fix: [v11],
    due: 10,
    estimate: '2d',
  });

  // Sprints of the board: the last one is completed, the current one runs, the next one is planned.
  const { items: boards } = await get('lena', '/api/v1/boards?project=SHOP');
  const board = boards[0].id as number;
  const sprint = async (name: string, goal: string, keys: string[]) => {
    const s = await call('lena', 'POST', `/api/v1/boards/${board}/sprints`, {
      name,
      goal,
    });
    await call('lena', 'POST', `/api/v1/sprints/${s.id}/issues`, {
      issues: keys,
    });
    return s.id as number;
  };
  const sprint12 = await sprint('Sprint 12', 'Search that understands customers', [
    suggestions,
    umlauts,
    confirmation,
  ]);
  await call('lena', 'POST', `/api/v1/sprints/${sprint12}/start`, {
    start: `${day(-24)}T09:00:00Z`,
    end: `${day(-10)}T17:00:00Z`,
  });
  for (const key of [suggestions, umlauts, confirmation]) {
    await moveTo(key === confirmation ? 'lena' : key === suggestions ? 'mara' : 'jonas', key, 'In Progress');
  }
  await comment(
    'jonas',
    umlauts,
    'The search index folds case but not accents. Normalising both the index and the query to NFKD fixes it.',
  );
  for (const key of [suggestions, umlauts, confirmation]) {
    await moveTo(key === confirmation ? 'lena' : key === suggestions ? 'mara' : 'jonas', key, 'Done');
  }
  await call('lena', 'POST', `/api/v1/sprints/${sprint12}/complete`, {
    moveTo: 'backlog',
  });

  const sprint13 = await sprint('Sprint 13', 'Customers pay with saved cards', [
    payCard,
    cardBrand,
    vault,
    lostCards,
    keyboard,
    refunds,
    loadTest,
  ]);
  await call('lena', 'POST', `/api/v1/sprints/${sprint13}/start`, {
    start: `${day(-9)}T09:00:00Z`,
    end: `${day(5)}T17:00:00Z`,
  });
  await sprint('Sprint 14', 'Find products faster', [priceFilter, contrast, decimals]);

  await moveTo('jonas', payCard, 'In Progress');
  await moveTo('jonas', savedApi, 'In Progress');
  await moveTo('jonas', savedApi, 'Done');
  await moveTo('jonas', savedList, 'In Progress');
  await moveTo('mara', vault, 'In Progress');
  await moveTo('mara', vault, 'In Review');
  await moveTo('mara', refunds, 'In Progress');
  await moveTo('lena', keyboard, 'In Progress');
  await call('jonas', 'POST', `/api/v1/issues/${payCard}/worklogs`, {
    timeSpent: '6h',
    comment: 'Token API and the checkout step',
  });
  await call('mara', 'POST', `/api/v1/issues/${vault}/worklogs`, {
    timeSpent: '1d 2h',
    comment: 'Key rotation job',
  });
  await call('mara', 'POST', `/api/v1/issues/${refunds}/worklogs`, {
    timeSpent: '3h',
    comment: 'Found the rounding in the refund service',
  });
  await call('lena', 'POST', `/api/v1/issues/${vault}/links`, {
    typeId: linkType('Blocks'),
    issue: payCard,
    direction: 'outward',
  });
  await call('tobias', 'POST', `/api/v1/issues/${lostCards}/links`, {
    typeId: linkType('Relates'),
    issue: payCard,
    direction: 'outward',
  });
  await comment(
    'sophie',
    refunds,
    'Two more customers called today. Can we refund the difference by hand until the fix is out?',
  );
  await comment(
    'mara',
    refunds,
    `@sophie yes — the amounts are in the refund log. The fix (keep cents end to end) is in review.`,
  );
  await comment('lena', payCard, 'Please keep the old checkout behind a switch until the load test is done.');
  await comment(
    'jonas',
    lostCards,
    'Reproduced: the account page caches the empty list from before the sign-in.',
  );
  await call('tobias', 'PUT', `/api/v1/issues/${refunds}/vote`);
  await call('sophie', 'PUT', `/api/v1/issues/${lostCards}/vote`);
  await call('felix', 'PUT', `/api/v1/issues/${keyboard}/vote`);
  await call('tobias', 'PUT', `/api/v1/issues/${loadTest}/watchers`);

  // A second project on a Kanban board.
  await call('lena', 'POST', '/api/v1/projects', {
    key: 'APP',
    name: 'Mobile App',
    template: 'kanban',
    description: 'The shop as an app for Android and iOS.',
    leadId: ids.mara,
  });
  await call('lena', 'POST', `/api/v1/projects/APP/roles/${role('Developers')}/members`, {
    type: 'group',
    id: developers,
  });
  await call('lena', 'POST', `/api/v1/projects/APP/roles/${role('Users')}/members`, {
    type: 'group',
    id: support,
  });
  const app = (s: Omit<IssueSpec, 'project'>) => issue({ ...s, project: 'APP' });
  const push = await app({
    type: 'story',
    summary: 'Push notification when an order ships',
    assignee: 'jonas',
  });
  const dark = await app({
    type: 'story',
    summary: 'Dark mode',
    assignee: 'mara',
  });
  const crash = await app({
    type: 'bug',
    by: 'tobias',
    summary: 'The app closes when the basket is empty on Android 12',
    priority: 'High',
    md: 'Opening the basket tab with an empty basket closes the app. Android 12 only.',
  });
  await app({
    type: 'story',
    summary: 'Sign in with a passkey',
    priority: 'Medium',
  });
  const offline = await app({
    type: 'story',
    summary: 'Keep the basket when offline',
    assignee: 'jonas',
  });
  const german = await app({
    type: 'task',
    summary: 'Translate the app into German',
    assignee: 'mara',
  });
  await moveTo('jonas', push, 'In Progress');
  await moveTo('mara', dark, 'In Progress');
  await moveTo('mara', dark, 'Done');
  await moveTo('jonas', offline, 'In Progress');
  await moveTo('mara', german, 'In Progress');
  await moveTo('mara', german, 'Done');
  await comment('tobias', crash, 'Crash log attached in the test report of build 412.');

  // An idea filed in the wrong project, moved to the app: its old key still leads to it.
  const tracking = await issue({
    type: 'story',
    by: 'sophie',
    summary: 'Follow the parcel in the app',
    md: 'Customers ask support where their parcel is. The app could show the tracking of the carrier.',
    priority: 'Medium',
  });
  type Choice = { from: { id: number }; type?: { id: number }; chosen?: number };
  const plan = (await call('lena', 'POST', '/api/v1/issues/move-plan', {
    issues: [tracking],
    project: 'APP',
    types: [],
    statuses: [],
  })) as { types: Choice[]; statuses: Choice[] };
  const moved = await call('lena', 'POST', `/api/v1/issues/${tracking}/move`, {
    project: 'APP',
    types: plan.types.map((q) => ({ from: q.from.id, to: q.chosen })),
    statuses: plan.statuses.map((q) => ({ type: q.type!.id, from: q.from.id, to: q.chosen })),
  });
  await comment('mara', moved.key, 'Moved here from the webshop: the app team owns this.');

  // A bulk change: the bugs customers notice get a label, in one go.
  const bulk = await call('lena', 'POST', '/api/v1/issues/bulk', {
    issues: [lostCards, umlauts, refunds, decimals, crash],
    action: 'edit',
    edit: { labels: { mode: 'add', values: ['customer-visible'] } },
    notify: false,
  });
  await expect
    .poll(async () => (await get('lena', `/api/v1/issues/bulk/${bulk.id}`)).state, { timeout: 60_000 })
    .toBe('done');

  // Filters, a subscription and a dashboard.
  const project = (await get('lena', '/api/v1/projects/SHOP')) as {
    id: number;
  };
  const openBugs = await call('lena', 'POST', '/api/v1/filters', {
    name: 'Open bugs in the webshop',
    description: 'Everything support should know about',
    query: `${openBugsQuery}, created`,
    shares: [
      { type: 'project', projectId: project.id },
      { type: 'group', id: support },
    ],
    columns: ['issuetype', 'key', 'summary', 'status', 'priority', 'assignee', 'reporter', 'created'],
  });
  await call('lena', 'PUT', `/api/v1/filters/${openBugs.id}/favourite`);
  await call('sophie', 'PUT', `/api/v1/filters/${openBugs.id}/favourite`);
  const release = await call('lena', 'POST', '/api/v1/filters', {
    name: 'Release 1.1',
    query: 'fixVersion = "1.1" ORDER BY rank',
    shares: [{ type: 'logged-in' }],
    columns: ['issuetype', 'key', 'summary', 'status', 'assignee', 'sprint', 'fixVersions'],
  });
  await call('lena', 'PUT', `/api/v1/filters/${release.id}/favourite`);
  await call('jonas', 'POST', '/api/v1/filters', {
    name: 'My open work',
    query: 'assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC',
  });
  // Subscriptions send e-mail: an installation without (KOGGE_SMTP_HOST) has none.
  if ((await apiGet(admin, '/api/v1/admin/system')).mail) {
    await call('lena', 'POST', `/api/v1/filters/${openBugs.id}/subscriptions`, {
      frequency: 'daily',
      minute: 8 * 60,
      weekdays: [1, 2, 3, 4, 5],
    });
    await call('lena', 'POST', `/api/v1/filters/${openBugs.id}/subscriptions`, {
      frequency: 'hourly',
      minute: 0,
      weekdays: [1, 2, 3, 4, 5],
      hours: [8, 9, 10, 11, 12, 13, 14, 15, 16, 17],
      groupId: support,
    });
  }
  await call('lena', 'POST', '/api/v1/dashboards', {
    name: 'Webshop overview',
    description: 'Sprint, bugs and what changed',
    columns: 2,
    shares: [{ type: 'project', projectId: project.id }],
    widgets: [
      {
        id: 'w1',
        type: 'sprint-health',
        title: 'Sprint 13',
        column: 0,
        position: 0,
        config: { boardId: board },
      },
      {
        id: 'w2',
        type: 'filter-results',
        title: 'Open bugs',
        column: 0,
        position: 1,
        config: {
          query: openBugsQuery,
          limit: 10,
        },
      },
      {
        id: 'w3',
        type: 'distribution',
        title: 'Release 1.1 by status',
        column: 1,
        position: 0,
        config: { query: 'fixVersion = "1.1"', field: 'status' },
      },
      {
        id: 'w4',
        type: 'activity',
        title: 'Activity',
        column: 1,
        position: 1,
        config: { project: 'SHOP', limit: 12 },
      },
    ],
  });

  // --- Logbuch ---
  const space = async (who: Who, key: string, name: string, description: string, categories: string[] = []) =>
    (await call(who, 'POST', '/api/v1/wiki/spaces', {
      key,
      name,
      description,
      categories,
    })) as { homePageId: number };
  const publish = async (who: Who, id: number, title: string, body: unknown) => {
    const page = await get(who, `/api/v1/wiki/pages/${id}`);
    await call(who, 'POST', `/api/v1/wiki/pages/${id}/publish`, {
      title,
      baseVersion: page.version ?? 0,
      body,
    });
  };
  const page = async (
    who: Who,
    spaceKey: string,
    parentId: number,
    title: string,
    body: unknown,
    kind = 'page',
  ) => {
    const draft = await call(who, 'POST', `/api/v1/wiki/spaces/${spaceKey}/pages`, { title, parentId, kind });
    await call(who, 'POST', `/api/v1/wiki/pages/${draft.id}/publish`, {
      title,
      baseVersion: 0,
      body,
    });
    return draft.id as number;
  };
  const grants = async (
    who: Who,
    key: string,
    add: { permission: string; holder: Record<string, unknown> }[],
  ) => {
    const current = (await get(who, `/api/v1/wiki/spaces/${key}/permissions`)) as {
      permission: string;
      holder: { type: string; id?: number };
    }[];
    const keep = current.map((g) => ({
      permission: g.permission,
      holder: { type: g.holder.type, id: g.holder.id },
    }));
    await call(who, 'PUT', `/api/v1/wiki/spaces/${key}/permissions`, [...keep, ...add]);
  };

  const eng = await space('lena', 'ENG', 'Engineering', 'How we build, test and release the shop.', [
    'engineering',
    'team',
  ]);
  const editors = ['space.view', 'page.edit', 'page.delete-own', 'comment.add', 'attachment.add'];
  await grants('lena', 'ENG', [
    ...editors.map((permission) => ({
      permission,
      holder: { type: 'group', id: developers },
    })),
    ...['space.view', 'comment.add'].map((permission) => ({
      permission,
      holder: { type: 'logged-in' },
    })),
  ]);
  await publish(
    'lena',
    eng.homePageId,
    'Engineering',
    doc(
      p(
        'Welcome to the engineering space of the webshop team. Start with the ',
        text('onboarding checklist', 'bold'),
        ', then read how our services fit together.',
      ),
      panel(
        'info',
        p(
          'Something unclear or outdated? Comment on the page — every page has an owner who answers within a day.',
        ),
      ),
      h(2, 'Pages'),
      macro('children', { depth: 2 }),
      h(2, 'Recently updated'),
      macro('recently-updated', { limit: 5 }),
    ),
  );
  // The checklist links to the architecture overview once that is written (its second version).
  const onboardingDoc = (architecture: Inline) =>
    doc(
      p('Your first week. Tick off what is done — your mentor sees the progress.'),
      h(2, 'Before the first day'),
      tasks(
        [true, 'Laptop ordered and set up'],
        [true, 'Accounts for Kogge, mail and chat'],
        [false, 'Hardware security key for the vault'],
      ),
      h(2, 'First week'),
      tasks(
        [false, 'Read the ', architecture],
        [false, 'Pair with ', mention(ids.jonas, 'Jonas Weber'), ' on a small ticket'],
        [false, 'Run the payments service locally (see the README of the repository)'],
        [false, 'Ship one change to staging'],
      ),
      panel('success', p('After the first week you join the sprint planning.')),
    );
  const onboarding = await page(
    'lena',
    'ENG',
    eng.homePageId,
    'Onboarding checklist',
    onboardingDoc('architecture overview'),
  );
  const architecture = await page(
    'jonas',
    'ENG',
    eng.homePageId,
    'Architecture overview',
    doc(
      p('The shop consists of three services behind one gateway. Each service owns its data.'),
      macro('mermaid', {
        code: [
          'flowchart LR',
          '  Browser --> Gateway',
          '  App[Mobile app] --> Gateway',
          '  Gateway --> Storefront',
          '  Gateway --> Payments',
          '  Gateway --> Orders',
          '  Payments --> Vault[(Key vault)]',
          '  Payments --> Provider[Payment provider]',
          '  Orders --> DB[(PostgreSQL)]',
        ].join('\n'),
      }),
      h(2, 'Services'),
      table(
        ['Service', 'Language', 'Owner', 'State'],
        [
          ['Storefront', 'TypeScript', 'Mara Lindner', status('Stable', 'green')],
          ['Payments', 'Go', 'Jonas Weber', status('Changing', 'orange')],
          ['Orders', 'Go', 'Lena Hofmann', status('Stable', 'green')],
        ],
      ),
      h(2, 'Calling a service'),
      code(
        'go',
        'resp, err := payments.Charge(ctx, payments.Request{\n\tOrder:  order.ID,\n\tAmount: order.Total, // in cents\n\tToken:  card.Token,\n})',
      ),
      panel(
        'warning',
        p(
          'Amounts are always integer cents. Never use floating point for money — see ',
          issueRef(refunds),
          '.',
        ),
      ),
    ),
  );
  const releaseProcess = await page(
    'lena',
    'ENG',
    eng.homePageId,
    'Release process',
    doc(
      p(
        'We release every four weeks. The release manager rotates; the next one is ',
        mention(ids.mara, 'Mara Lindner'),
        '.',
      ),
      ol(
        'Freeze the scope in the sprint planning',
        'All issues of the version are done (see below)',
        'The pipeline of main is green and staging is deployed',
        'Production is deployed with an approval',
        'Release the version in Kontor and publish the release notes',
      ),
      h(2, 'Version 1.1'),
      p(
        'Status: ',
        status('In progress', 'blue'),
        ' — planned for ',
        { type: 'date', attrs: { value: day(14) } },
        '.',
      ),
      macro('issue-list', {
        query: 'fixVersion = "1.1" ORDER BY status',
        max: 20,
      }),
      expand(
        'What if a release goes wrong?',
        p(
          'Redeploy the previous deployment of production in Helling — it takes the same commit and artifacts again.',
        ),
      ),
    ),
  );
  await page(
    'mara',
    'ENG',
    eng.homePageId,
    'Coding guidelines',
    doc(
      p('Short rules that keep the code base easy to change.'),
      h(2, 'Go'),
      ul(
        'Errors are values: wrap them with context, never ignore them',
        'Packages do not import each other in circles',
        'Every exported name has a comment',
      ),
      code('go', 'if err != nil {\n\treturn fmt.Errorf("refund %s: %w", id, err)\n}'),
      h(2, 'TypeScript'),
      ul('Strict mode everywhere', 'Components are small and named after what they show'),
      h(2, 'Reviews'),
      p('Every pull request needs one approval. Code owners approve changes of their paths.'),
    ),
  );
  await page(
    'lena',
    'ENG',
    eng.homePageId,
    'Version 1.0 is live',
    doc(
      p('After eight months of work the new shop is live for all customers. Thank you, everyone!'),
      ul('Search with suggestions', 'Order confirmations in German and English', 'A checkout in three steps'),
      p(
        'What comes next is in ',
        link('the release process', `/wiki/spaces/ENG/pages/${releaseProcess}`),
        '.',
      ),
    ),
    'blog',
  );
  await publish(
    'lena',
    onboarding,
    'Onboarding checklist',
    onboardingDoc(link('architecture overview', `/wiki/spaces/ENG/pages/${architecture}`)),
  );
  await call('mara', 'POST', `/api/v1/wiki/pages/${architecture}/comments`, {
    body: doc(p('Should the gateway also do the rate limiting for the app?')),
  });
  await call('jonas', 'POST', `/api/v1/wiki/pages/${releaseProcess}/comments`, {
    body: doc(p('Added the approval step for production — it is now required in Helling as well.')),
  });

  const sup = await space(
    'sophie',
    'SUP',
    'Support',
    'Answers for customers and how support works with the team.',
    ['customers', 'team'],
  );
  await grants('sophie', 'SUP', [
    ...editors.map((permission) => ({
      permission,
      holder: { type: 'group', id: support },
    })),
    ...['space.view', 'comment.add'].map((permission) => ({
      permission,
      holder: { type: 'logged-in' },
    })),
  ]);
  await publish(
    'sophie',
    sup.homePageId,
    'Support',
    doc(p('How we help customers, and what we know about open problems.'), macro('children', { depth: 1 })),
  );
  await page(
    'sophie',
    'SUP',
    sup.homePageId,
    'Frequently asked questions',
    doc(
      expand(
        'How do I save a card?',
        p(
          'At the last step of the checkout, tick “Save this card”. Saved cards are in Account → Payment methods.',
        ),
      ),
      expand('When does the refund arrive?', p('Within three to five working days, depending on the bank.')),
      expand(
        'Can I change an order?',
        p('Until it ships: open the order in the account and choose “Change”.'),
      ),
    ),
  );
  await page(
    'sophie',
    'SUP',
    sup.homePageId,
    'Known problems',
    doc(
      p('Open bugs that customers may notice. The list updates itself from Kontor.'),
      macro('issue-list', {
        query: openBugsQuery,
        max: 10,
      }),
    ),
  );

  // Layouts: who to call next to what to do, and three principles side by side.
  await page(
    'lena',
    'ENG',
    eng.homePageId,
    'On-call and contacts',
    doc(
      p('Who answers when something breaks, and what they do first.'),
      columns(
        'left',
        [
          h(3, 'On call this week'),
          p(mention(ids.jonas, 'Jonas Weber')),
          h(3, 'Backup'),
          p(mention(ids.mara, 'Mara Lindner')),
          panel('note', p('Hand over on Monday at ten, in the stand-up.')),
        ],
        [
          h(3, 'When an alert comes in'),
          ol(
            'Acknowledge it within fifteen minutes',
            'Look at the latest deployment in Helling — redeploy the previous one if it is the cause',
            'Write what you see in the incident channel, even when you do not know the cause yet',
            'Open a bug in Kontor with the label incident',
          ),
          h(3, 'Payments are failing'),
          p(
            'Check the status page of the payment provider first. Refunds can wait; charges cannot — see ',
            issueRef(refunds),
            ' for the last incident.',
          ),
        ],
      ),
    ),
  );
  const des = await space('lena', 'DES', 'Design', 'How the shop looks, reads and feels.', [
    'product',
    'team',
  ]);
  await grants('lena', 'DES', [
    ...editors.map((permission) => ({ permission, holder: { type: 'group', id: design } })),
    ...['space.view', 'comment.add'].map((permission) => ({ permission, holder: { type: 'logged-in' } })),
  ]);
  await publish(
    'lena',
    des.homePageId,
    'Design',
    doc(p('The principles, components and words of the shop.'), macro('children', { depth: 1 })),
  );
  await page(
    'felix',
    'DES',
    des.homePageId,
    'Design principles',
    doc(
      p('Three rules we check every screen against.'),
      columns(
        'equal',
        [
          h(3, 'Clear'),
          p('One main action per screen. The price, the delivery date and the total are never hidden.'),
        ],
        [h(3, 'Calm'), p('No countdowns, no pop-ups during the checkout, no colour that only shouts.')],
        [
          h(3, 'Accessible'),
          p('Everything works with the keyboard and a screen reader; text reaches WCAG AA contrast.'),
        ],
      ),
      h(2, 'Words'),
      table(
        ['We write', 'Instead of'],
        [
          ['Basket', 'Cart'],
          ['Pay now', 'Submit order'],
          ['Saved cards', 'Payment instruments'],
        ],
      ),
    ),
  );
  // Lena's own space, and the space of the old shop, archived.
  const personal = (await call('lena', 'POST', '/api/v1/wiki/spaces/personal')) as {
    key: string;
    homePageId: number;
  };
  await publish(
    'lena',
    personal.homePageId,
    'Lena Hofmann',
    doc(
      p('Notes for myself: what I am on, and what I promised.'),
      tasks(
        [true, 'Plan Sprint 13 with the team'],
        [false, 'Release notes for 1.1'],
        [false, 'Ask support which refunds were rounded'],
      ),
    ),
  );
  const old = await space(
    'lena',
    'SHOP19',
    'Shop 2019',
    'The documentation of the old shop, kept for reference.',
    ['archive'],
  );
  await publish(
    'lena',
    old.homePageId,
    'Shop 2019',
    doc(
      panel('warning', p('The old shop was switched off in 2025. These pages are kept for reference only.')),
    ),
  );
  await call('lena', 'PATCH', '/api/v1/wiki/spaces/SHOP19', { archived: true });

  // Exports of the engineering space, ready for download.
  for (const format of ['pdf', 'html']) {
    await call('lena', 'POST', '/api/v1/wiki/spaces/ENG/exports', {
      format,
      paper: format === 'pdf' ? 'a4' : undefined,
    });
  }
  await expect
    .poll(
      async () =>
        ((await get('lena', '/api/v1/wiki/exports?space=ENG')) as { state: string }[])
          .map((e) => e.state)
          .join(),
      { timeout: 120_000, intervals: [1000] },
    )
    .toBe('done,done');

  // --- Hafen ---
  await call('lena', 'POST', '/api/v1/code/projects', {
    key: 'WEB',
    name: 'Webshop',
    description: 'The code of the shop',
  });
  const codeGrants = (await get('lena', '/api/v1/code/projects/WEB/permissions')) as {
    permission: string;
    holder: { type: string; id?: number };
  }[];
  await call('lena', 'PUT', '/api/v1/code/projects/WEB/permissions', [
    ...codeGrants.map((g) => ({
      permission: g.permission,
      holder: { type: g.holder.type, id: g.holder.id },
    })),
    { permission: 'repo.write', holder: { type: 'group', id: developers } },
    { permission: 'repo.read', holder: { type: 'group', id: support } },
  ]);
  const commit = (
    who: Who,
    repo: string,
    path: string,
    content: string,
    message: string,
    branch = 'main',
    newBranch?: string,
  ) =>
    call(who, 'PUT', `/api/v1/code/repos/WEB/${repo}/files`, {
      branch,
      newBranch,
      path,
      content,
      message,
    });

  await call('lena', 'POST', '/api/v1/code/projects/WEB/repos', {
    name: 'payments',
    readme: false,
  });
  // Every strategy, so that the merge dialog explains them all; the first is the default.
  const allow = async (repo: string, strategies: string[], settings: Record<string, unknown>) => {
    const r = await get('lena', `/api/v1/code/repos/WEB/${repo}`);
    await call('lena', 'PATCH', `/api/v1/code/repos/WEB/${repo}`, {
      settings: { ...r.settings, mergeStrategies: strategies, ...settings },
    });
  };
  await allow('payments', ['merge', 'squash', 'rebase', 'rebase-merge', 'fast-forward'], {
    requiredApprovals: 1,
    deleteSourceBranch: true,
  });
  await commit(
    'lena',
    'payments',
    'README.md',
    [
      '# Payments',
      '',
      'The payments service of the shop: charges, saved cards and refunds.',
      '',
      '## Run it',
      '',
      '```sh',
      'go run ./cmd/payments',
      '```',
      '',
      '## Rules',
      '',
      '- Amounts are integer cents.',
      "- Card numbers never reach the service; it keeps the provider's tokens, encrypted with the vault key.",
      '',
    ].join('\n'),
    'Describe the service',
  );
  await commit(
    'jonas',
    'payments',
    'go.mod',
    'module example.org/shop/payments\n\ngo 1.27\n',
    'Start the payments module',
  );
  await commit(
    'jonas',
    'payments',
    'cmd/payments/main.go',
    [
      '// Command payments serves charges, saved cards and refunds.',
      'package main',
      '',
      'import (',
      '\t"log"',
      '\t"net/http"',
      '',
      '\t"example.org/shop/payments/internal/refunds"',
      ')',
      '',
      'func main() {',
      '\thttp.Handle("/refunds", refunds.Handler())',
      '\tlog.Fatal(http.ListenAndServe(":8080", nil))',
      '}',
      '',
    ].join('\n'),
    'Serve refunds over HTTP',
  );
  await commit(
    'mara',
    'payments',
    'internal/refunds/refunds.go',
    [
      '// Package refunds pays money back to customers.',
      'package refunds',
      '',
      'import "net/http"',
      '',
      '// Partial returns the refund for part of an order, in cents.',
      'func Partial(total, share int64) int64 {',
      '\treturn total * share / 100 / 100 * 100 // SHOP-13: rounds to whole euros',
      '}',
      '',
      '// Handler serves refund requests.',
      'func Handler() http.Handler {',
      '\treturn http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {',
      '\t\tw.WriteHeader(http.StatusAccepted)',
      '\t})',
      '}',
      '',
    ].join('\n'),
    'Pay back parts of orders',
  );
  await commit(
    'mara',
    'payments',
    'CODEOWNERS',
    '# Who reviews what\n* @jonas\n/internal/refunds/ @mara\n',
    'Name the code owners',
  );

  // Pipelines, run by a build agent of this machine.
  await commit(
    'lena',
    'payments',
    '.kogge/pipelines.yml',
    [
      'version: 1',
      '',
      'defaults:',
      '  runs-on: [shell, linux]',
      '',
      'pipelines:',
      '  ci:',
      '    name: Build and deploy',
      '    on:',
      '      push:',
      '        branches: ["**"]',
      '      pull-request: {}',
      '    stages:',
      '      - name: Test',
      '        jobs:',
      '          - name: unit tests',
      '            steps:',
      '              - name: Run the tests',
      '                run: |',
      '                  echo "=== RUN   TestPartialRefund"',
      '                  echo "--- PASS: TestPartialRefund (0.00s)"',
      '                  echo "=== RUN   TestSavedCardExpired"',
      '                  echo "--- PASS: TestSavedCardExpired (0.01s)"',
      '                  echo "ok   example.org/shop/payments/internal/refunds  0.012s"',
      "                  cat > report.xml <<'XML'",
      '                  <testsuites><testsuite name="refunds" tests="3" failures="0" time="0.012">',
      '                  <testcase classname="refunds" name="TestPartialRefund" time="0.001"/>',
      '                  <testcase classname="refunds" name="TestFullRefund" time="0.001"/>',
      '                  <testcase classname="cards" name="TestSavedCardExpired" time="0.010"/>',
      '                  </testsuite></testsuites>',
      '                  XML',
      '            reports:',
      '              junit: [report.xml]',
      '          - name: vet',
      '            steps:',
      '              - run: echo "go vet ./... — no problems found"',
      '      - name: Build',
      '        jobs:',
      '          - name: binary',
      '            steps:',
      '              - run: |',
      '                  mkdir -p dist',
      '                  echo "payments $KOGGE_COMMIT" > dist/payments',
      '                  echo "built dist/payments for linux/amd64"',
      '            artifacts:',
      '              - name: payments',
      '                paths: [dist/payments]',
      '      - name: Deploy',
      '        jobs:',
      '          - name: staging',
      '            environment: staging',
      '            only:',
      '              branches: [main]',
      '            steps:',
      '              - run: echo "deploying $KOGGE_COMMIT to $KOGGE_ENVIRONMENT"',
      '          - name: production',
      '            environment: production',
      '            trigger: manual',
      '            only:',
      '              branches: [main]',
      '            steps:',
      '              - run: echo "deploying $KOGGE_COMMIT to $KOGGE_ENVIRONMENT"',
      '',
    ].join('\n'),
    'Build, test and deploy with Helling',
  );

  await call('lena', 'PUT', '/api/v1/code/repos/WEB/payments/environments/staging', {
    name: 'staging',
    description: 'Test system, deployed from main',
    refs: ['main'],
    deployerUsers: [],
    deployerGroups: [developers],
    approvals: 0,
    approverUsers: [],
    approverGroups: [],
  });
  await call('lena', 'PUT', '/api/v1/code/repos/WEB/payments/environments/production', {
    name: 'production',
    description: 'Customers use this one',
    refs: ['main'],
    deployerUsers: [ids.lena],
    deployerGroups: [],
    approvals: 1,
    approverUsers: [ids.mara],
    approverGroups: [],
  });

  const dir = mkdtempSync(join(tmpdir(), 'kogge-showcase-agent-'));
  const token = (
    await apiCall(admin, 'POST', '/api/v1/admin/agent-tokens', {
      description: 'Build agent',
      expiresInDays: 1,
      autoApprove: true,
    })
  ).token as string;
  const config = join(dir, 'config.json');
  execFileSync(
    agentBinary,
    [
      'register',
      '-config',
      config,
      '-server',
      baseURL!,
      '-token',
      token,
      '-name',
      'build-01',
      '-labels',
      'linux',
      '-shell',
    ],
    {
      stdio: 'pipe',
    },
  );
  const agent = spawn(agentBinary, ['run', '-config', config], {
    stdio: 'ignore',
  });
  const settled = async (repo: string) => {
    await expect
      .poll(
        async () => {
          const runs = await get('lena', `/api/v1/code/repos/WEB/${repo}/runs?limit=50`);
          const list = (runs.runs ?? runs.items ?? runs) as { state: string }[];
          return list.length > 0 && list.every((r) => !['queued', 'running', 'pending'].includes(r.state));
        },
        { timeout: 180_000, intervals: [1000] },
      )
      .toBe(true);
  };
  try {
    // A feature branch with a pull request in review.
    await commit(
      'jonas',
      'payments',
      'internal/cards/saved.go',
      [
        '// Package cards keeps the cards customers saved.',
        'package cards',
        '',
        'import "time"',
        '',
        "// Card is a saved card: the provider's token, never the number.",
        'type Card struct {',
        '\tToken   string',
        '\tBrand   string',
        '\tLast4   string',
        '\tExpires time.Time',
        '}',
        '',
        '// Usable reports whether the card can pay now.',
        'func (c Card) Usable(now time.Time) bool { return now.Before(c.Expires) }',
        '',
      ].join('\n'),
      `${payCard}: Keep saved cards`,
      'main',
      `feature/${payCard}-saved-cards`,
    );
    await commit(
      'jonas',
      'payments',
      'internal/cards/saved_test.go',
      [
        'package cards',
        '',
        'import (',
        '\t"testing"',
        '\t"time"',
        ')',
        '',
        'func TestSavedCardExpired(t *testing.T) {',
        '\tc := Card{Expires: time.Now().Add(-time.Hour)}',
        '\tif c.Usable(time.Now()) {',
        '\t\tt.Fatal("an expired card is usable")',
        '\t}',
        '}',
        '',
      ].join('\n'),
      `${payCard}: Test expired cards`,
      `feature/${payCard}-saved-cards`,
    );
    const pr = await call('jonas', 'POST', '/api/v1/code/repos/WEB/payments/pull-requests', {
      title: `${payCard}: Pay with a saved card`,
      source: `feature/${payCard}-saved-cards`,
      target: 'main',
      descriptionMarkdown: [
        'Customers pay with a card they saved before.',
        '',
        '- `Card` keeps the provider token, brand, last four digits and expiry',
        '- Expired cards are listed but not usable',
        '',
        `Part of ${savedCards}.`,
      ].join('\n'),
      reviewers: [ids.mara, ids.lena],
    });
    await call('mara', 'POST', `/api/v1/code/repos/WEB/payments/pull-requests/${pr.number}/comments`, {
      markdown:
        'Should a card that expires today still work until midnight?\n\n```suggestion\nfunc (c Card) Usable(now time.Time) bool { return !now.After(c.Expires) }\n```',
      path: 'internal/cards/saved.go',
      line: 15,
      side: 'new',
    });
    await call('lena', 'POST', `/api/v1/code/repos/WEB/payments/pull-requests/${pr.number}/comments`, {
      markdown: 'Add a test for a card that expires today before merging.',
      task: true,
    });
    await call('lena', 'PUT', `/api/v1/code/repos/WEB/payments/pull-requests/${pr.number}/review`, {
      state: 'approved',
    });
    // Jonas wants it merged as soon as the open task is resolved.
    await call('jonas', 'PUT', `/api/v1/code/repos/WEB/payments/pull-requests/${pr.number}/auto-merge`, {
      strategy: 'squash',
      deleteSource: true,
    });

    // A fix that was reviewed and merged.
    await commit(
      'mara',
      'payments',
      'internal/refunds/refunds.go',
      [
        '// Package refunds pays money back to customers.',
        'package refunds',
        '',
        'import "net/http"',
        '',
        '// Partial returns the refund for part of an order, in cents: share is in hundredths of a percent.',
        'func Partial(total, share int64) int64 {',
        '\treturn total * share / 10000',
        '}',
        '',
        '// Handler serves refund requests.',
        'func Handler() http.Handler {',
        '\treturn http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {',
        '\t\tw.WriteHeader(http.StatusAccepted)',
        '\t})',
        '}',
        '',
      ].join('\n'),
      `${refunds}: Keep cents in partial refunds`,
      'main',
      `fix/${refunds}-cents`,
    );
    const fix = await call('mara', 'POST', '/api/v1/code/repos/WEB/payments/pull-requests', {
      title: `${refunds}: Keep cents in partial refunds`,
      source: `fix/${refunds}-cents`,
      target: 'main',
      descriptionMarkdown:
        'Refunds were divided by 100 twice. Now they stay in cents from the start to the end.',
      reviewers: [ids.jonas],
    });
    await call('jonas', 'PUT', `/api/v1/code/repos/WEB/payments/pull-requests/${fix.number}/review`, {
      state: 'approved',
    });
    await settled('payments');
    await call('mara', 'POST', `/api/v1/code/repos/WEB/payments/pull-requests/${fix.number}/merge`, {});
    await moveTo('mara', refunds, 'In Review');
    await settled('payments');

    // Production: started by Lena, approved by Mara.
    const runs = await get('lena', '/api/v1/code/repos/WEB/payments/runs?branch=main&limit=1');
    const latest = (runs.runs ?? runs.items ?? runs)[0];
    const run = await get('lena', `/api/v1/code/repos/WEB/payments/runs/${latest.number}`);
    const production = JSON.stringify(run).match(/"id":(\d+),"name":"production"/);
    if (production) {
      await call('lena', 'POST', `/api/v1/builds/jobs/${production[1]}/play`);
      await expect
        .poll(
          async () =>
            JSON.stringify(
              await get('mara', '/api/v1/code/repos/WEB/payments/environments/production/deployments'),
            ),
          {
            timeout: 60_000,
          },
        )
        .toContain('"id"');
      const deployments = await get(
        'mara',
        '/api/v1/code/repos/WEB/payments/environments/production/deployments',
      );
      const waiting = (deployments.deployments ?? deployments.items ?? deployments)[0];
      await call('mara', 'POST', `/api/v1/builds/deployments/${waiting.id}/approval`, {
        approved: true,
        comment: 'Checked on staging.',
      });
      await settled('payments');
    }

    // The storefront.
    await call('lena', 'POST', '/api/v1/code/projects/WEB/repos', {
      name: 'storefront',
      readme: false,
    });
    await commit(
      'mara',
      'storefront',
      'README.md',
      '# Storefront\n\nThe shop in the browser: catalogue, search, basket and checkout.\n\n```sh\nnpm install\nnpm run dev\n```\n',
      'Describe the storefront',
    );
    await commit(
      'mara',
      'storefront',
      'package.json',
      JSON.stringify(
        {
          name: 'storefront',
          private: true,
          scripts: { dev: 'vite', test: 'vitest' },
        },
        null,
        2,
      ) + '\n',
      'Start the storefront',
    );
    await commit(
      'mara',
      'storefront',
      'src/search/normalise.ts',
      [
        '/** Folds a search term for matching: lower case, without accents (Kühlschrank finds Kuhlschrank). */',
        'export function normalise(term: string): string {',
        "  return term.normalize('NFKD').replace(/[\\u0300-\\u036f]/g, '').toLowerCase().trim();",
        '}',
        '',
      ].join('\n'),
      `${umlauts}: Fold accents before searching`,
    );
    await commit(
      'mara',
      'storefront',
      'src/search/suggest.ts',
      [
        "import { normalise } from './normalise';",
        '',
        '/** Suggests products whose names start with what the customer typed. */',
        'export function suggest(names: string[], typed: string, max = 8): string[] {',
        '  const t = normalise(typed);',
        '  return names.filter((n) => normalise(n).startsWith(t)).slice(0, max);',
        '}',
        '',
      ].join('\n'),
      `${suggestions}: Suggestions while typing`,
    );
    await allow('storefront', ['squash', 'rebase', 'merge', 'rebase-merge', 'fast-forward'], {
      requiredApprovals: 1,
      deleteSourceBranch: true,
    });

    // Pipelines whose jobs wait for the jobs they need (a graph), with a shared file and conditions.
    await commit(
      'lena',
      'storefront',
      '.kogge/checks.yml',
      [
        '# What the pipelines of the storefront share: where their jobs run and how the tests behave.',
        'defaults:',
        '  runs-on: [shell, linux]',
        '',
        'variables:',
        '  NODE_ENV: test',
        '',
      ].join('\n'),
      'Share the settings of the pipelines',
    );
    await commit(
      'lena',
      'storefront',
      '.kogge/pipelines.yml',
      [
        'version: 1',
        '',
        'include:',
        '  - .kogge/checks.yml',
        '',
        'pipelines:',
        '  ci:',
        '    name: Build and test',
        '    on:',
        '      push:',
        '        branches: ["**"]',
        '      pull-request: {}',
        '    stages:',
        '      - name: Check',
        '        jobs:',
        '          - name: lint',
        '            steps:',
        '              - run: echo "eslint src — no problems found"',
        '          - name: types',
        '            steps:',
        '              - run: echo "tsc --noEmit — no errors"',
        '          - name: unit tests',
        '            steps:',
        '              - name: Run the tests',
        '                run: |',
        '                  echo " ✓ src/search/normalise.test.ts (4 tests) 3ms"',
        '                  if grep -q TODO src/search/suggest.ts; then',
        '                    echo " ✗ src/search/suggest.test.ts > puts exact matches first"',
        "                    echo \"   expected [ 'Kühlbox', 'Kühlschrank' ], got [ 'Kühlschrank', 'Kühlbox' ]\"",
        '                    exit 1',
        '                  fi',
        '                  echo " ✓ src/search/suggest.test.ts (6 tests) 5ms"',
        '                  echo " Test Files  2 passed (2)"',
        '      - name: Build',
        '        jobs:',
        '          - name: bundle',
        '            needs: [lint, types]',
        '            steps:',
        '              - run: |',
        '                  mkdir -p dist',
        '                  echo "storefront $KOGGE_COMMIT" > dist/index.html',
        '                  echo "dist/index.html            0.41 kB"',
        '                  echo "dist/assets/storefront.js  41.20 kB │ gzip: 13.07 kB"',
        '            artifacts:',
        '              - name: storefront',
        '                paths: ["dist/**"]',
        '          - name: docs',
        "            if: changed('docs/**')",
        '            steps:',
        '              - run: echo "the documentation is built"',
        '      - name: Verify',
        '        jobs:',
        '          - name: browser tests',
        '            needs: [bundle, unit tests]',
        '            steps:',
        '              - run: echo "12 passed (8.1s)"',
        '          - name: size limit',
        '            needs: [bundle]',
        '            steps:',
        '              - run: echo "dist/assets/storefront.js takes 41.2 kB of 50 kB"',
        '      - name: Deliver',
        '        jobs:',
        '          - name: preview',
        "            if: event == 'pull-request'",
        '            needs: [browser tests]',
        '            steps:',
        '              - run: echo "the preview of this pull request is at https://preview.example.org/$KOGGE_COMMIT"',
        '          - name: publish',
        '            needs: [browser tests, size limit]',
        '            only:',
        '              branches: [main]',
        '            steps:',
        '              - run: echo "published storefront $KOGGE_COMMIT"',
        '',
      ].join('\n'),
      'Build and test the storefront with Helling',
    );
    await settled('storefront');

    // A fix that was merged by rebasing its commit onto main.
    await commit(
      'jonas',
      'storefront',
      'src/basket/total.ts',
      [
        '/** Formats a total in cents with the digits of its currency: two for the euro, none for the yen. */',
        "export function formatTotal(cents: number, currency: string, locale = 'de-DE'): string {",
        "  return new Intl.NumberFormat(locale, { style: 'currency', currency }).format(cents / 100);",
        '}',
        '',
      ].join('\n'),
      `${decimals}: Show totals with the digits of their currency`,
      'main',
      `fix/${decimals}-digits`,
    );
    const digits = await call('jonas', 'POST', '/api/v1/code/repos/WEB/storefront/pull-requests', {
      title: `${decimals}: Show totals with the digits of their currency`,
      source: `fix/${decimals}-digits`,
      target: 'main',
      descriptionMarkdown: 'Intl knows how many digits a currency has; the basket stops rounding by hand.',
      reviewers: [ids.mara],
    });
    await call('mara', 'PUT', `/api/v1/code/repos/WEB/storefront/pull-requests/${digits.number}/review`, {
      state: 'approved',
    });
    await settled('storefront');
    await call('jonas', 'POST', `/api/v1/code/repos/WEB/storefront/pull-requests/${digits.number}/merge`, {
      strategy: 'rebase',
      deleteSource: true,
    });
    await settled('storefront');

    // A change in review: its first push failed the tests, the second one fixed them.
    const exact = `feature/${suggestions}-exact-first`;
    const suggestSource = (body: string[]) =>
      [
        "import { normalise } from './normalise';",
        '',
        '/** Suggests products whose names start with what the customer typed: exact matches first, then the shorter names. */',
        'export function suggest(names: string[], typed: string, max = 8): string[] {',
        '  const t = normalise(typed);',
        ...body,
        '}',
        '',
      ].join('\n');
    await commit(
      'mara',
      'storefront',
      'src/search/suggest.ts',
      suggestSource([
        '  // TODO: put exact matches first',
        '  return names.filter((n) => normalise(n).startsWith(t)).slice(0, max);',
      ]),
      `${suggestions}: Suggest exact matches first`,
      'main',
      exact,
    );
    const ranking = await call('mara', 'POST', '/api/v1/code/repos/WEB/storefront/pull-requests', {
      title: `${suggestions}: Exact matches first`,
      source: exact,
      target: 'main',
      descriptionMarkdown: [
        'Customers who type a whole product name expect it at the top of the suggestions.',
        '',
        '- Exact matches come first',
        '- Then shorter names before longer ones',
      ].join('\n'),
      reviewers: [ids.jonas],
    });
    await settled('storefront');
    await commit(
      'mara',
      'storefront',
      'src/search/suggest.ts',
      suggestSource([
        '  return names',
        '    .filter((n) => normalise(n).startsWith(t))',
        '    .sort((a, b) => Number(normalise(b) === t) - Number(normalise(a) === t) || a.length - b.length)',
        '    .slice(0, max);',
      ]),
      `${suggestions}: Sort exact matches before the others`,
      exact,
    );
    await call(
      'jonas',
      'POST',
      `/api/v1/code/repos/WEB/storefront/pull-requests/${ranking.number}/comments`,
      {
        markdown: 'Nice. Does the sort keep the order of the catalogue for names of the same length?',
        path: 'src/search/suggest.ts',
        line: 9,
        side: 'new',
      },
    );
    await settled('storefront');
  } finally {
    agent.kill('SIGINT');
    rmSync(dir, { recursive: true, force: true });
  }

  // The engineering space is Lena's favourite: the last of the content.
  await call('lena', 'PUT', '/api/v1/wiki/spaces/ENG/favourite');
});
