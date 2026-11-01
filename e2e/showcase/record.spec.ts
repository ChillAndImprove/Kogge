// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { mkdirSync, writeFileSync } from 'node:fs';
import { homedir, hostname, tmpdir, userInfo } from 'node:os';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test, type Locator, type Page } from '@playwright/test';
import { apiGet } from '../tests/fixtures';

// Visits the pages of a Kogge with the sample content (seed.spec.ts) as one of its people and keeps every answer of
// the API, for the preview that runs without a server (web/showcase/recording.json). Addresses of the machine it
// was recorded on are replaced by neutral ones.

/** The checkout the recording is made in; test servers often keep their data directories in it. */
const checkout = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');
const output = resolve(checkout, 'web', 'showcase', 'recording.json');
const password = 'Sample-Person-Secret-2026';

interface Recorded {
  status: number;
  type: string;
  body: string;
}

/**
 * Requests that read with POST, kept by their bodies (as web/src/showcase/preview.ts answers them): highlighting
 * code, rendering Markdown, and what a bulk change or a move would do.
 */
const readingPosts = [
  '/api/v1/code/highlight',
  '/api/v1/code/markdown',
  '/api/v1/issues/bulk/check',
  '/api/v1/issues/move-plan',
];

/** Texts typed into the quick search, letter by letter, as visitors of the preview would. */
const quickSearches = [
  'refund',
  'card',
  'search',
  'release',
  'payments',
  'checkout',
  'sprint',
  'onboarding',
  'storefront',
  'deploy',
  'SHOP-13',
  'lena',
  'jonas',
  'mara',
];

/** Texts searched for on the search page. */
const searches = ['refund', 'card', 'search', 'release', 'payments', 'checkout', 'pipeline', 'design'];

test('record the preview', async ({ page, baseURL }) => {
  const responses: Record<string, Recorded> = {};
  const pending: Promise<void>[] = [];
  page.on('response', (res) => {
    const url = new URL(res.url());
    const method = res.request().method();
    if (!url.pathname.startsWith('/api/') || url.pathname === '/api/v1/live') return;
    const reading = method === 'POST' && readingPosts.includes(url.pathname);
    if (method !== 'GET' && !reading) return;
    const key = reading
      ? `POST ${url.pathname} ${res.request().postData() ?? ''}`
      : url.pathname + url.search;
    pending.push(
      (async () => {
        // Kogge's answers are UTF-8, event streams (build logs) too; the browser reads those, which name no charset,
        // as Windows-1252, so they are fetched again as they are.
        const stream = (res.headers()['content-type'] ?? '').startsWith('text/event-stream');
        const bytes = stream ? page.request.get(res.url()).then((r) => r.body()) : res.body();
        const body = await Promise.race([
          bytes.then((b) => b.toString('utf8')).catch(() => null),
          new Promise<null>((done) => setTimeout(() => done(null), 15_000)),
        ]);
        if (body === null) return;
        responses[key] = {
          status: res.status(),
          type: res.headers()['content-type'] ?? 'application/json',
          body,
        };
      })(),
    );
  });

  await page.goto('/');
  await page.getByRole('textbox', { name: 'User name' }).fill('lena');
  await page.getByRole('textbox', { name: 'Password' }).fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: /Hello/ })).toBeVisible();

  const settle = async (p: Page) => {
    await p.waitForLoadState('networkidle');
    await p.waitForTimeout(150);
  };
  /** Addresses that pages link to, collected to be visited after them (as visitors follow the links). */
  const linked = new Set<string>();
  const visited = new Set<string>();
  const collect = async (p: Page, pattern: RegExp) => {
    for (const href of await p
      .locator('a[href]')
      .evaluateAll((as) => as.map((a) => a.getAttribute('href') ?? '')))
      if (pattern.test(href) && !visited.has(href)) linked.add(href);
  };
  // Every page's links to searches of issues (labels, components, lists of issues on pages) and to the commits of
  // branches and files are followed.
  const followed = /^\/(issues\/search\?|code\/[^/]+\/[^/]+\/commits\?)/;
  const visit = async (path: string, then?: (p: Page) => Promise<void>) => {
    visited.add(path);
    await page.goto(path);
    await settle(page);
    if (then) {
      await then(page).catch((e: unknown) => console.log(`${path}: ${String(e).split('\n')[0]}`));
      await settle(page);
    }
    await collect(page, followed);
  };
  const visitLinked = async () => {
    const hrefs = [...linked];
    linked.clear();
    for (const href of hrefs) if (!visited.has(href)) await visit(href);
  };
  const clickAll = async (p: Page, role: 'tab' | 'button' | 'radio', names: (string | RegExp)[]) => {
    for (const name of names) {
      const target = p.getByRole(role, { name }).first();
      if (await target.isVisible().catch(() => false)) {
        await target.click();
        await settle(p);
      }
    }
  };
  /** Clicks every option of a segmented control, one after another. */
  const segments = async (p: Page, control: Locator) => {
    for (const label of await control.locator('label').all()) {
      await label.click();
      await settle(p);
    }
  };
  /** The activity stream of a page, also with only what one is involved in. */
  const activity = async (p: Page, all = false) => {
    // On the start page: older entries, a page at a time (the preview ends the stream after them), and each product
    // (elsewhere the preview shows of a product what the stream of all products has of it).
    if (all) {
      for (let i = 0; i < 2; i++) {
        const button = p.getByRole('button', { name: 'Show more' });
        if (!(await button.isVisible().catch(() => false))) break;
        await button.click();
        await settle(p);
      }
      const products = p.getByRole('radiogroup', { name: 'Product' });
      if (await products.isVisible().catch(() => false)) {
        await segments(p, products);
        await products.locator('label').first().click();
        await settle(p);
      }
    }
    const mine = p.getByRole('switch', { name: 'Only what I watch or am involved in' });
    if (await mine.isVisible().catch(() => false)) {
      await mine.click({ force: true });
      await settle(p);
      await mine.click({ force: true });
      await settle(p);
    }
  };

  // The platform: the start page with its activity, the people and the search page.
  await visit('/', (p) => activity(p, true));
  await visit('/profile');
  for (const who of ['lena', 'jonas', 'mara', 'tobias', 'sophie', 'felix'])
    await visit(`/people/${who}`, (p) => activity(p));
  for (const q of searches) {
    await visit(`/search?q=${encodeURIComponent(q)}`, async (p) => {
      // Each product, and each kind of things of it.
      const filters = p.getByRole('navigation', { name: 'Filter the results' });
      for (const name of ['Kontor', 'Logbuch', 'Hafen', 'Helling', 'People']) {
        const product = filters.getByRole('button', { name: new RegExp(`^${name}`) });
        if (!(await product.isVisible().catch(() => false))) continue;
        await product.click();
        await settle(p);
        for (const kind of await filters.getByRole('button').all()) {
          if (!(await kind.isVisible().catch(() => false))) continue;
          await kind.click();
          await settle(p);
        }
      }
    });
  }

  // Kontor.
  await visit('/issues');
  await visit('/issues/projects');
  await visit('/issues/search');
  await visit('/issues/filters');
  await visit('/issues/filters?tab=subscriptions');
  for (const f of (await apiGet(page, '/api/v1/filters?limit=500')).items as {
    id: number;
    query: string;
  }[]) {
    await visit(`/issues/search?filter=${f.id}&q=${encodeURIComponent(f.query)}`);
  }
  for (const q of [
    'project = SHOP ORDER BY created DESC',
    'project = APP ORDER BY created DESC',
    'assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC',
    'statusCategory = "In Progress" ORDER BY priority DESC',
    'status WAS "In Progress" AND statusCategory = Done',
    'labels = customer-visible ORDER BY priority DESC',
  ]) {
    await visit(`/issues/search?q=${encodeURIComponent(q)}`);
  }
  // The suggestions of the query box: the values of every field that has some, and the arguments of functions.
  const catalog = (await apiGet(page, '/api/v1/search/autocomplete')) as {
    fields: { name: string; aliases: string[]; values: boolean }[];
    functions: { name: string; arg?: string }[];
  };
  const quoted = (name: string) => (/^[A-Za-z]+$/.test(name) ? name : `"${name}"`);
  const typedQueries = [
    ...catalog.fields.filter((f) => f.values).map((f) => `${quoted(f.name)} = `),
    // Issue keys are more than the twenty values a suggestion shows: the beginnings of the keys of each project.
    ...['SHOP-', 'APP-', ...[1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => `SHOP-${n}`)].map((k) => `key = ${k}`),
    ...catalog.functions
      .filter((f) => f.arg && f.arg !== 'increment')
      .map(
        (f) => `${f.arg === 'group' ? 'assignee' : f.arg === 'issue' ? 'issue' : 'fixVersion'} in ${f.name}(`,
      ),
  ];
  await visit(`/issues/search?q=${encodeURIComponent('project = SHOP ORDER BY created DESC')}`, async (p) => {
    const box = p.getByRole('combobox', { name: 'KQL query' });
    for (const typed of typedQueries) {
      await box.fill(typed);
      await p.waitForTimeout(300);
      await settle(p);
    }
    await box.press('Escape');
  });
  // Bulk changes up to the point where they would change something, of all issues, for each action; the preview
  // answers other choices of issues from these. Fields whose values belong to a project are edited in one project
  // only: the edits of the issues of each project, too.
  const actions = [
    'Edit fields',
    'Change the status',
    'Move to another project',
    'Watch',
    'Stop watching',
    'Delete',
  ];
  for (const [q, chosen] of [
    ['project in (SHOP, APP) ORDER BY key', actions],
    ['project = SHOP ORDER BY created DESC', ['Edit fields']],
    ['project = APP ORDER BY created DESC', ['Edit fields']],
  ] as const) {
    await visit(`/issues/search?q=${encodeURIComponent(q)}`, async (p) => {
      await p
        .getByTestId('issue-table')
        .getByRole('checkbox', { name: 'Choose all issues of the page' })
        .check();
      await p.getByRole('button', { name: 'Bulk change…' }).click();
      const dialog = p.getByRole('dialog');
      for (const action of chosen) {
        await dialog.getByRole('radio', { name: action, exact: true }).check();
        await dialog.getByRole('button', { name: 'Next' }).click();
        await settle(p);
        if (action === 'Move to another project') {
          for (const target of ['SHOP', 'APP']) {
            await dialog.getByRole('combobox', { name: 'Target project' }).click();
            const option = p.getByRole('option', { name: new RegExp(`\\(${target}\\)$`) });
            if (await option.isVisible().catch(() => false)) {
              await option.click();
              await settle(p);
            } else {
              await p.keyboard.press('Escape');
            }
          }
        }
        await dialog.getByRole('button', { name: 'Back' }).click();
      }
      await dialog.getByRole('button', { name: 'Cancel' }).click();
    });
  }
  await visit('/issues/bulk');
  for (const b of (await apiGet(page, '/api/v1/issues/bulk')) as { id: number }[]) {
    await visit(`/issues/bulk/${b.id}`);
  }
  await visit('/dashboards');
  for (const d of (await apiGet(page, '/api/v1/dashboards?limit=500')).items as {
    id: number;
  }[])
    await visit(`/dashboards/${d.id}`);
  for (const key of ['SHOP', 'APP']) {
    await visit(`/projects/${key}`);
    await visit(`/projects/${key}/activity`, (p) => activity(p));
    await visit(`/projects/${key}/boards`);
    for (const b of (await apiGet(page, `/api/v1/boards?project=${key}&limit=500`)).items as {
      id: number;
      type: string;
    }[]) {
      await visit(`/projects/${key}/boards/${b.id}`);
      if (b.type === 'scrum') await visit(`/projects/${key}/boards/${b.id}/backlog`);
      await visit(`/projects/${key}/boards/${b.id}/reports`, (p) =>
        clickAll(p, 'tab', [/Burndown/, /Velocity/, /Cumulative/, /Created/, /Sprint report/]),
      );
    }
    await visit(`/projects/${key}/releases`);
    for (const v of ((await apiGet(page, `/api/v1/projects/${key}`)).versions ?? []) as { id: number }[]) {
      await visit(`/projects/${key}/releases/${v.id}`);
    }
    await visit(`/projects/${key}/components`);
    await visit(`/projects/${key}/settings`);
  }
  const all = await apiGet(
    page,
    `/api/v1/search?q=${encodeURIComponent('project in (SHOP, APP) ORDER BY key')}&limit=200`,
  );
  for (const i of all.issues as { key: string }[]) {
    await visit(`/browse/${i.key}`, async (p) => {
      // The transitions of the status button.
      const status = p.getByRole('button', { name: /^Status: / }).first();
      if (await status.isVisible()) {
        await status.click();
        await settle(p);
        await p.keyboard.press('Escape');
      }
      // The tabs of the activity: comments, history, work log.
      await clickAll(p, 'tab', [/History/, /Work log/, /Comments/]);
    });
  }
  await visitLinked();
  // The issue that moved: its old key leads to it. The server sends the page of an old key on to the new one; the
  // preview, whose addresses are after the #, asks the API for the old key.
  await page.evaluate(() => fetch('/api/v1/issues/SHOP-21').then((r) => r.text()));
  await settle(page);
  // The form to create an issue.
  await visit('/issues', async (p) => {
    await p.getByRole('button', { name: 'Create', exact: true }).first().click();
    await settle(p);
    await p.keyboard.press('Escape');
  });

  // Logbuch.
  await visit('/wiki');
  // The directory of spaces: as a list (each filter, each order) and as cards.
  await page.evaluate(() => localStorage.setItem('kogge.wiki.spaceView', 'list'));
  await visit('/wiki/spaces', async (p) => {
    await segments(p, p.getByRole('radiogroup', { name: 'Which spaces' }));
    await p.getByRole('radiogroup', { name: 'Which spaces' }).locator('label').first().click();
    await settle(p);
    for (const column of ['Key', 'Name', 'Type', 'Last updated']) {
      for (let i = 0; i < 2; i++) {
        await p.getByRole('button', { name: `Sort by ${column}` }).click();
        await settle(p);
      }
    }
  });
  for (const category of ['engineering', 'team', 'customers', 'product', 'archive']) {
    await visit(`/wiki/spaces?category=${category}`);
  }
  await page.evaluate(() => localStorage.setItem('kogge.wiki.spaceView', 'cards'));
  await visit('/wiki/spaces', (p) => segments(p, p.getByRole('radiogroup', { name: 'Which spaces' })));
  await page.evaluate(() => localStorage.setItem('kogge.wiki.spaceView', 'list'));
  const spaces = (
    (await apiGet(page, '/api/v1/wiki/spaces?limit=100')).items as { key: string; homePageId: number }[]
  ).concat((await apiGet(page, '/api/v1/wiki/spaces?archived=true&limit=100')).items);
  for (const { key } of spaces) {
    await visit(`/wiki/spaces/${key}`, async (p) => {
      // The export of the space, with the exports made before.
      await p.getByRole('button', { name: 'Export space' }).click();
      await settle(p);
      await p.keyboard.press('Escape');
      await settle(p);
      // A new page: the templates of the space (where pages can be written).
      const create = p.getByRole('button', { name: 'Create page', exact: true });
      if (await create.isVisible().catch(() => false)) {
        await create.click();
        await settle(p);
        await p.keyboard.press('Escape');
      }
    });
    await visit(`/wiki/spaces/${key}/blog`);
    await visit(`/wiki/spaces/${key}/activity`, (p) => activity(p));
    await visit(`/wiki/spaces/${key}/settings`);
    // Every page of the tree, and the blog.
    const walk = async (parent?: number) => {
      const level = (await apiGet(
        page,
        `/api/v1/wiki/spaces/${key}/tree?limit=500${parent ? `&parent=${parent}` : ''}`,
      )) as { items: { id: number; hasChildren: boolean }[] };
      for (const node of level.items) {
        await visit(`/wiki/spaces/${key}/pages/${node.id}`, async (p) => {
          // The menu of the page with its exports, and the export of the page with its children.
          await p.getByRole('button', { name: 'More', exact: true }).click();
          const tree = p.getByRole('menuitem', { name: 'Export with child pages…' });
          if (await tree.isVisible().catch(() => false)) {
            await tree.click();
            await settle(p);
          }
          await p.keyboard.press('Escape');
        });
        await visit(`/wiki/spaces/${key}/pages/${node.id}/history`);
        if (node.hasChildren) await walk(node.id);
      }
    };
    await walk();
    const blog = (await apiGet(page, `/api/v1/wiki/spaces/${key}/blog?limit=500`)) as {
      pages: { id: number }[];
    };
    for (const post of blog.pages) {
      await visit(`/wiki/spaces/${key}/pages/${post.id}`);
      await visit(`/wiki/spaces/${key}/pages/${post.id}/history`);
    }
  }
  await visitLinked();

  // Hafen.
  await visit('/code');
  await visit('/code/repos');
  await visit('/code/pull-requests');
  await visit('/code/web');
  for (const repo of ['payments', 'storefront']) {
    const base = `/code/web/${repo}`;
    await visit(base);
    const walk = async (path: string, ref = 'main') => {
      const tree = await apiGet(
        page,
        `/api/v1/code/repos/WEB/${repo}/tree?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`,
      );
      for (const e of (tree.entries ?? tree.items ?? tree) as {
        name: string;
        path?: string;
        type: string;
      }[]) {
        const p = e.path ?? (path ? `${path}/${e.name}` : e.name);
        await visit(`${base}/src/${p}${ref === 'main' ? '' : `?at=${encodeURIComponent(ref)}`}`);
        if (e.type === 'tree' || e.type === 'dir') await walk(p, ref);
      }
    };
    await walk('');
    // The source branches of the open pull requests, which their pages link to.
    const open = await apiGet(page, `/api/v1/code/repos/WEB/${repo}/pull-requests?state=open`);
    for (const pr of (open.items ?? open) as { source: string }[]) {
      await visit(`${base}/src?at=${encodeURIComponent(pr.source)}`);
      await walk('', pr.source);
    }
    await visit(`${base}/commits`);
    // The commits of main and of the pull requests.
    const shas = new Set<string>();
    const commits = await apiGet(page, `/api/v1/code/repos/WEB/${repo}/commits?ref=main&limit=50`);
    for (const c of (commits.items ?? commits.commits ?? commits) as { sha: string }[]) shas.add(c.sha);
    const prList = await apiGet(page, `/api/v1/code/repos/WEB/${repo}/pull-requests?state=all`);
    for (const pr of (prList.items ?? prList) as { number: number }[]) {
      const own = await apiGet(page, `/api/v1/code/repos/WEB/${repo}/pull-requests/${pr.number}/commits`);
      for (const c of (own.items ?? own.commits ?? own) as { sha: string }[]) shas.add(c.sha);
    }
    for (const sha of shas) await visit(`${base}/commits/${sha}`);
    await visit(`${base}/branches`);
    await visit(`${base}/tags`);
    await visit(`${base}/search?q=refund`);
    await visit(`${base}/activity`, (p) => activity(p));
    await visit(`${base}/pull-requests`);
    await visit(`${base}/pull-requests?state=all`);
    const prs = await apiGet(page, `/api/v1/code/repos/WEB/${repo}/pull-requests?state=all`);
    for (const pr of (prs.items ?? prs) as { number: number }[]) {
      await visit(`${base}/pull-requests/${pr.number}`, async (p) => {
        // The tabs of the pull request (not those of an editor in one of them): its changes and commits.
        await clickAll(p, 'tab', [/^Changes/, /^Commits/, /^Overview/]);
        // The merge dialog: the strategies, and why it cannot merge yet.
        const merge = p.getByRole('button', { name: 'Merge', exact: true });
        if (await merge.isVisible().catch(() => false)) {
          await merge.click();
          await settle(p);
          await p.keyboard.press('Escape');
        }
      });
    }
    await visit(`${base}/settings`);
  }
  await visitLinked();

  // Helling.
  await visit('/builds');
  await visit('/builds', (p) => clickAll(p, 'tab', [/All runs/]));
  await visit('/builds/agents');
  await visit('/builds/templates');
  await visit('/builds/web');
  for (const repo of ['payments', 'storefront']) {
    await visit(`/builds/web/${repo}`);
    await visit(`/builds/web/${repo}/environments`);
    await visit(`/builds/web/${repo}/settings`);
    const runs = await apiGet(page, `/api/v1/code/repos/WEB/${repo}/runs?limit=50`);
    for (const r of (runs.items ?? runs) as { number: number }[]) {
      await visit(`/builds/web/${repo}/runs/${r.number}`, async (p) => {
        // Every job's log, chosen in the graph of the jobs or in the stages (not their buttons that run them again).
        for (const job of await p.locator('.kogge-job [aria-pressed]').all()) {
          await job.click();
          await settle(p);
          await p.waitForTimeout(300);
        }
        // The files the pipeline file included, at the commit the run read them.
        await collect(p, /^\/code\/[^/]+\/[^/]+\/src\/[^?]+\?at=/);
      });
    }
  }
  await visitLinked();

  // Administration, and the configuration of Kontor.
  for (const path of [
    '/admin',
    '/admin/users',
    '/admin/groups',
    '/admin/permissions',
    '/admin/settings',
    '/admin/sso',
    '/admin/audit',
    '/admin/system',
    '/admin/storage',
    '/admin/webhooks',
  ])
    await visit(path);
  for (const path of ['types', 'statuses', 'priorities', 'resolutions', 'link-types', 'roles', 'fields'])
    await visit(`/issues/admin/${path}`);
  await visit('/issues/admin/workflows', (p) => collect(p, /^\/issues\/admin\/workflows\/\d+$/));
  for (const path of ['permission-schemes', 'security-schemes', 'notification-schemes'])
    await visit(`/issues/admin/${path}`, (p) => collect(p, new RegExp(`^/issues/admin/${path}/\\d+$`)));
  while (linked.size > 0) await visitLinked();

  // The quick search, last: what Lena opened recently are a few typical things, and what she types is found as she
  // types it.
  for (const path of [
    '/code/web/storefront/pull-requests/1',
    '/builds/web/storefront/runs/1',
    '/wiki/spaces/ENG/pages/5',
    '/projects/SHOP/boards/1',
    '/browse/SHOP-13',
    '/browse/SHOP-4',
  ]) {
    await visit(path);
  }
  await visit('/', async (p) => {
    for (const text of quickSearches) {
      await p.keyboard.press('Control+k');
      const box = p.getByRole('combobox', { name: 'Quick search' });
      await expect(box).toBeFocused();
      await settle(p);
      for (let i = 1; i <= text.length; i++) {
        await box.fill(text.slice(0, i));
        await p.waitForTimeout(200);
        await settle(p);
      }
      await p.keyboard.press('Escape');
      await settle(p);
    }
  });
  // Its actions open the dialogs to create things, in the space, code project or project chosen there.
  for (const [typed, action, where] of [
    ['create', 'Create an issue', ['Mobile App', 'Webshop']],
    ['create', 'Create a page', ['Engineering']],
    ['blog', 'Write a blog post', ['Engineering']],
    ['create', 'Create a repository', ['Webshop']],
  ] as const) {
    await visit('/', async (p) => {
      await p.keyboard.press('Control+k');
      await p.getByRole('combobox', { name: 'Quick search' }).fill(typed);
      await settle(p);
      await p.getByRole('option', { name: action }).click();
      await settle(p);
      for (const choice of where) {
        await p
          .getByRole('dialog')
          .getByRole('combobox', { name: /^(Space|Project)$/ })
          .click();
        await p
          .getByRole('option', { name: new RegExp(choice) })
          .first()
          .click();
        await settle(p);
      }
    });
  }

  await Promise.all(pending);

  // Neutral addresses instead of the machine's.
  const origin = new URL(baseURL!).origin;
  const ssh = new URL(
    ((await apiGet(page, '/api/v1/code/repos/WEB/payments')) as { cloneSsh: string }).cloneSsh,
  ).host;
  const swaps: [string, string][] = [
    [origin, 'https://kogge.example.org'],
    [ssh, 'kogge.example.org'],
    ['kogge@localhost', 'kogge@example.org'],
    [hostname(), 'build-01'],
    [checkout, '/srv/kogge'],
    [homedir(), '/home/kogge'],
    [tmpdir(), '/var/lib/kogge'],
    [userInfo().username, 'kogge'],
    ['127.0.0.1', '10.0.0.21'],
  ];
  const clean = (s: string) => swaps.reduce((acc, [from, to]) => acc.split(from).join(to), s);
  const cleaned: Record<string, Recorded> = {};
  for (const [key, r] of Object.entries(responses)) cleaned[clean(key)] = { ...r, body: clean(r.body) };
  const me = cleaned['/api/v1/me'];
  if (me) me.body = me.body.replace(/"csrfToken":"[^"]*"/, '"csrfToken":""');
  // The disk of the machine the recording was made on is no sample content: the administration shows a roomy one,
  // in the data directory of an installation.
  const overview = cleaned['/api/v1/admin/overview'];
  if (overview) {
    const o = JSON.parse(overview.body) as Record<string, unknown> & { attention?: { key: string }[] };
    o.dataTotalBytes = 500 * 2 ** 30;
    o.dataFreeBytes = 412 * 2 ** 30;
    o.dataReserveBytes = 10 * 2 ** 30;
    o.attention = (o.attention ?? []).filter((a) => !a.key.startsWith('disk'));
    overview.body = JSON.stringify(o);
  }
  const system = cleaned['/api/v1/admin/system'];
  if (system) {
    const s = JSON.parse(system.body) as Record<string, unknown>;
    s.dataDir = '/var/lib/kogge';
    s.dataFreeBytes = 412 * 2 ** 30;
    system.body = JSON.stringify(s);
  }
  const text = JSON.stringify({
    recordedAt: new Date().toISOString(),
    responses: cleaned,
  });
  for (const leak of [userInfo().username, hostname(), tmpdir(), 'localhost', '127.0.0.1']) {
    const at = text.indexOf(leak);
    expect(at, `the recording mentions ${leak}: …${text.slice(Math.max(0, at - 160), at + 80)}…`).toBe(-1);
  }
  mkdirSync(dirname(output), { recursive: true });
  writeFileSync(output, text);
  console.log(`${Object.keys(cleaned).length} answers, ${(text.length / 1e6).toFixed(1)} MB → ${output}`);
});
