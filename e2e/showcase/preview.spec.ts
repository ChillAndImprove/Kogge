// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { createReadStream, existsSync, mkdirSync, statSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { dirname, extname, join, normalize, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test as base, type Page } from '@playwright/test';

// Checks the built preview (make showcase): its main pages and what is new render from the recording without
// console errors and without asking for answers the recording lacks, in light and dark mode; what would change
// something or needs a server explains how to deploy Kogge. The preview is served from web/dist-showcase as GitHub
// Pages serves a project's site — in a folder, files only — or checked where KOGGE_PREVIEW_URL says (a deployed
// one). KOGGE_PREVIEW_SHOTS names a folder for screenshots.

const dist = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', 'web', 'dist-showcase');
const shots = process.env.KOGGE_PREVIEW_SHOTS;
let previewURL = process.env.KOGGE_PREVIEW_URL ?? '';
let server: Server | undefined;

const types: Record<string, string> = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
};

base.beforeAll(async () => {
  if (previewURL) {
    // The preview's files are next to its index.html.
    if (!previewURL.endsWith('/')) previewURL += '/';
    return;
  }
  expect(
    existsSync(join(dist, 'index.html')),
    `${dist} is missing: build the preview with make showcase`,
  ).toBe(true);
  server = createServer((req, res) => {
    const path = decodeURIComponent(new URL(req.url ?? '/', 'http://preview').pathname);
    let file = path.startsWith('/kogge/') ? normalize(join(dist, path.slice('/kogge/'.length))) : '';
    if (file && existsSync(file) && statSync(file).isDirectory()) file = join(file, 'index.html');
    if (!file.startsWith(dist) || !existsSync(file) || !statSync(file).isFile()) {
      res.writeHead(404, { 'Content-Type': 'text/plain' }).end('Not found');
      return;
    }
    res.writeHead(200, { 'Content-Type': types[extname(file)] ?? 'application/octet-stream' });
    createReadStream(file).pipe(res);
  });
  await new Promise<void>((done) => server!.listen(0, '127.0.0.1', done));
  previewURL = `http://127.0.0.1:${(server.address() as AddressInfo).port}/kogge/`;
});

base.afterAll(() => {
  server?.close();
});

type Scheme = 'light' | 'dark';

/**
 * A page of the preview in a colour scheme; the test fails on console errors, uncaught errors and answers the
 * recording lacks (the preview logs them).
 */
const test = base.extend<{ scheme: Scheme; preview: Page }>({
  scheme: ['light', { option: true }],
  preview: async ({ browser, scheme }, use) => {
    const context = await browser.newContext({ baseURL: previewURL, colorScheme: scheme, locale: 'en-GB' });
    const page = await context.newPage();
    const problems: string[] = [];
    const missing: string[] = [];
    page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
    page.on('console', (m) => {
      if (m.type() === 'error') problems.push(`console: ${m.text()}`);
      const miss = /^preview: no sample content for (.*)$/.exec(m.text());
      if (miss) missing.push(miss[1]);
    });
    await use(page);
    await context.close();
    expect(problems, problems.join('\n')).toEqual([]);
    expect(missing, `answers the recording lacks:\n${missing.join('\n')}`).toEqual([]);
  },
});

/** Opens an address of the preview (after the #) and waits for its answers. */
async function go(page: Page, path: string) {
  await page.goto(`./#${path}`);
  await page.waitForLoadState('networkidle');
  await expect(page.getByTestId('preview-banner')).toBeVisible();
  await page.waitForTimeout(300);
}

async function shot(page: Page, scheme: Scheme, name: string) {
  if (!shots) return;
  mkdirSync(shots, { recursive: true });
  await page.screenshot({ path: `${shots}/${scheme}-${name}.png` });
}

/** The dialog that explains a change, with the words of what was tried. */
async function explains(page: Page, words: RegExp) {
  const dialog = page.getByRole('dialog', { name: 'Deploy Kogge to do this' });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText(words);
  await dialog.getByRole('button', { name: 'Keep looking around' }).click();
  await expect(dialog).toBeHidden();
}

const shopQuery = `/issues/search?q=${encodeURIComponent('project = SHOP ORDER BY created DESC')}`;

for (const scheme of ['light', 'dark'] as const) {
  test.describe(scheme, () => {
    test.use({ scheme });

    test('the main pages', async ({ preview: page }) => {
      const pages: [string, string, RegExp | string][] = [
        ['start', '/', /Hello/],
        ['person', '/people/jonas', 'What they did'],
        ['search', '/search?q=refund', 'Results for “refund”'],
        ['kontor', '/issues', 'Your work'],
        ['navigator', shopQuery, 'SHOP-4'],
        ['board', '/projects/SHOP/boards/1', 'Sprint 13'],
        ['backlog', '/projects/SHOP/boards/1/backlog', 'Sprint 14'],
        ['project-activity', '/projects/SHOP/activity', 'Lena Hofmann'],
        ['issue', '/browse/SHOP-4', 'Pay with a saved card'],
        ['moved-issue', '/browse/SHOP-21', 'Follow the parcel in the app'],
        ['bulk-change', '/issues/bulk/1', 'Partial refunds are rounded to whole euros'],
        ['dashboard', '/dashboards/1', 'Webshop overview'],
        ['logbuch', '/wiki', 'Engineering'],
        ['space', '/wiki/spaces/ENG', 'Onboarding checklist'],
        ['space-activity', '/wiki/spaces/ENG/activity', 'Lena Hofmann'],
        ['hafen', '/code', 'Pay with a saved card'],
        ['pull-request', '/code/web/payments/pull-requests/1', 'Pay with a saved card'],
        ['repository-activity', '/code/web/storefront/activity', 'storefront'],
        ['helling', '/builds', 'storefront'],
        ['admin-storage', '/admin/storage', 'payments'],
        ['admin-settings', '/admin/settings', 'Settings'],
      ];
      for (const [name, path, text] of pages) {
        await go(page, path);
        await expect.soft(page.locator('main').getByText(text).first(), `${name}: ${path}`).toBeVisible();
        if (['start', 'pull-request', 'search', 'board', 'person'].includes(name))
          await shot(page, scheme, name);
      }
    });

    test('a layout of columns, and exports that explain', async ({ preview: page }) => {
      await go(page, '/wiki/spaces/ENG');
      await page.getByRole('navigation').getByRole('link', { name: 'On-call and contacts' }).first().click();
      await page.waitForLoadState('networkidle');
      await expect(page.locator('.kogge-columns').first()).toBeVisible();
      await shot(page, scheme, 'columns');
      await page.getByRole('button', { name: 'More', exact: true }).click();
      await page.getByRole('menuitem', { name: 'Export as PDF' }).click();
      await explains(page, /exports/);
      // The export of the space: the exports made, and a new one explains.
      await page.getByRole('button', { name: 'Export space' }).click();
      const dialog = page.getByRole('dialog', { name: /Export/ });
      await expect(dialog.locator('[data-state="done"]')).toHaveCount(2);
      await shot(page, scheme, 'export');
      await dialog.getByRole('button', { name: 'Start export' }).click();
      await explains(page, /exports/);
    });

    test('the space directory as a list and as cards', async ({ preview: page }) => {
      await go(page, '/wiki/spaces');
      const which = page.getByRole('radiogroup', { name: 'Which spaces' });
      await which.getByText('Archived').click();
      await expect(page.getByText('Shop 2019').first()).toBeVisible();
      await which.getByText('All spaces').click();
      await page.getByRole('button', { name: 'Sort by Key' }).click();
      await page.getByRole('textbox', { name: 'Filter spaces' }).fill('eng');
      await expect(page.getByText('1 space', { exact: true })).toBeVisible();
      await page.getByRole('textbox', { name: 'Filter spaces' }).fill('');
      await expect(page.getByText(/^[2-9] spaces$/)).toBeVisible();
      await shot(page, scheme, 'space-list');
      await page.getByRole('radiogroup', { name: 'Show spaces as' }).getByText('Cards').click();
      await expect(page.getByTestId('space-cards')).toBeVisible();
      await shot(page, scheme, 'space-cards');
      await page.getByRole('radiogroup', { name: 'Show spaces as' }).getByText('List').click();
    });

    test('the quick search', async ({ preview: page }) => {
      await go(page, '/issues');
      await page.keyboard.press('Control+k');
      const box = page.getByRole('combobox', { name: 'Quick search' });
      await expect(box).toBeFocused();
      await expect(page.getByRole('group', { name: 'Recently opened' })).toBeVisible();
      // Typed letter by letter, as recorded.
      await box.pressSequentially('refund', { delay: 160 });
      await expect(page.getByRole('option', { name: /SHOP-13 Partial refunds/ })).toBeVisible();
      await shot(page, scheme, 'quick-search');
      // Texts not recorded: what the recorded beginning found that matches them, or nothing.
      await box.fill('refunds partial');
      await expect(page.getByRole('option', { name: /SHOP-13 Partial refunds/ })).toBeVisible();
      await box.fill('zeppelin');
      await expect(page.getByText('Nothing found by title or key.').first()).toBeVisible();
      // An issue key opens the issue with Enter, and in a new tab of the preview with Ctrl+Enter.
      await box.fill('');
      await box.pressSequentially('SHOP-13', { delay: 160 });
      await expect(page.getByRole('option').first()).toContainText('SHOP-13');
      const [tab] = await Promise.all([page.waitForEvent('popup'), box.press('Control+Enter')]);
      await expect(tab).toHaveURL(/#\/browse\/SHOP-13$/);
      await tab.close();
      await box.press('Enter');
      await expect(page).toHaveURL(/#\/browse\/SHOP-13$/);
    });

    test('creating from the quick search explains', async ({ preview: page }) => {
      const action = async (name: string) => {
        await page.keyboard.press('Control+k');
        await page.getByRole('combobox', { name: 'Quick search' }).fill('create');
        await page.getByRole('option', { name }).click();
        return page.getByRole('dialog');
      };
      await go(page, '/');
      let dialog = await action('Create a page');
      await dialog.getByRole('combobox', { name: 'Space' }).click();
      await page.getByRole('option', { name: /Engineering/ }).click();
      await page
        .getByRole('dialog', { name: 'Create page' })
        .getByRole('button', { name: /^Blank page/ })
        .click();
      await explains(page, /pages/);
      await page.keyboard.press('Escape');
      dialog = await action('Create an issue');
      await dialog.getByRole('combobox', { name: 'Project' }).click();
      await page.getByRole('option', { name: /Webshop/ }).click();
      await dialog.getByRole('textbox', { name: 'Summary' }).fill('Gift cards');
      await dialog.getByRole('button', { name: 'Create', exact: true }).click();
      await explains(page, /issues/);
      await page.keyboard.press('Escape');
      dialog = await action('Create a repository');
      await dialog.getByRole('combobox', { name: 'Project' }).click();
      await page.getByRole('option', { name: /Webshop/ }).click();
      dialog = page.getByRole('dialog', { name: 'Create repository' });
      await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill('gift-cards');
      await dialog.getByRole('button', { name: 'Create', exact: true }).click();
      await explains(page, /repositories/);
    });

    test('links in pages, and editing explains', async ({ preview: page }) => {
      await go(page, '/wiki/spaces/ENG');
      await page.getByRole('navigation').getByRole('link', { name: 'Onboarding checklist' }).first().click();
      await page
        .locator('.kogge-doc')
        .getByRole('link', { name: 'architecture overview', exact: true })
        .click();
      await expect(page.getByRole('heading', { name: 'Architecture overview', level: 1 })).toBeVisible();
      await expect(page).toHaveURL(/#\/wiki\/spaces\/ENG\/pages\/\d+$/);
      await page.getByRole('link', { name: 'Edit', exact: true }).click();
      await explains(page, /pages/);
    });

    test('suggestions of the query box', async ({ preview: page }) => {
      await go(page, shopQuery);
      const query = page.getByRole('combobox', { name: 'KQL query' });
      await query.fill('');
      await query.pressSequentially('assignee = jo', { delay: 60 });
      const list = page.getByRole('listbox', { name: 'Suggestions' });
      await expect(list.getByRole('option', { name: /^jonas/ })).toBeVisible();
      await shot(page, scheme, 'kql');
      await query.fill('');
      await query.pressSequentially('type = s', { delay: 60 });
      await expect(list.getByRole('option', { name: /^Story/ })).toBeVisible();
      await expect(list.getByRole('option', { name: /^Bug/ })).toHaveCount(0);
    });

    test('the navigator exports what it found', async ({ preview: page }) => {
      await go(page, shopQuery);
      await page.getByRole('button', { name: 'Export' }).click();
      const [csv] = await Promise.all([
        page.waitForEvent('download'),
        page.getByRole('menuitem', { name: 'CSV (separated by commas)' }).click(),
      ]);
      expect(csv.suggestedFilename()).toMatch(/\.csv$/);
    });

    test('a bulk change up to the point where it would change something', async ({ preview: page }) => {
      await go(page, shopQuery);
      const table = page.getByTestId('issue-table');
      await table.getByRole('checkbox', { name: 'Choose SHOP-4' }).check();
      await table.getByRole('checkbox', { name: 'Choose SHOP-13' }).check();
      await page.getByRole('button', { name: 'Bulk change…' }).click();
      const bulk = page.getByRole('dialog');
      await bulk.getByRole('radio', { name: 'Change the status' }).check();
      await bulk.getByRole('button', { name: 'Next' }).click();
      await expect(bulk.getByTestId('bulk-count')).toHaveText('2 of 2 issues can be changed.');
      await bulk.getByRole('button', { name: 'Back' }).click();
      await bulk.getByRole('radio', { name: 'Edit fields' }).check();
      await bulk.getByRole('button', { name: 'Next' }).click();
      await expect(bulk.getByTestId('bulk-count')).toHaveText('2 of 2 issues can be changed.');
      await bulk.getByTestId('bulk-field-labels').getByRole('checkbox').check();
      const labels = bulk.getByTestId('bulk-field-labels').getByRole('combobox');
      await labels.fill('preview');
      await labels.press('Enter');
      await shot(page, scheme, 'bulk');
      await bulk.getByRole('button', { name: 'Change 2 issues' }).click();
      await explains(page, /issues/);
    });

    test('moving an issue', async ({ preview: page }) => {
      await go(page, '/browse/SHOP-4');
      await page.getByRole('button', { name: 'More', exact: true }).click();
      await page.getByRole('menuitem', { name: 'Move…' }).click();
      const move = page.getByRole('dialog');
      await move.getByRole('combobox', { name: 'Target project' }).click();
      await page.getByRole('option', { name: /\(APP\)$/ }).click();
      await expect(move.getByTestId('move-plan')).toContainText('SHOP-16');
      await shot(page, scheme, 'move');
      await move.getByRole('button', { name: 'Move', exact: true }).click();
      await explains(page, /issues/);
    });

    test('the merge dialog, auto-merge and the changes side by side', async ({ preview: page }) => {
      await go(page, '/code/web/payments/pull-requests/1');
      await expect(page.getByText(/auto-merge/i).first()).toBeVisible();
      await page.getByRole('button', { name: 'Merge', exact: true }).click();
      const merge = page.getByRole('dialog');
      await merge.getByRole('combobox', { name: 'Strategy' }).click();
      await page.getByRole('option', { name: 'Rebase', exact: true }).click();
      await expect(merge.getByText('The commits keep their messages.')).toBeVisible();
      await expect(merge.getByText('Not mergeable yet:')).toBeVisible();
      await shot(page, scheme, 'merge');
      await merge.getByRole('button', { name: 'Change auto-merge' }).click();
      await explains(page, /pull requests/);
      await page.keyboard.press('Escape');
      await go(page, '/code/web/storefront/pull-requests/2');
      await page.getByRole('tab', { name: 'Changes' }).click();
      await page.getByText('Side by side', { exact: true }).click();
      await expect(page.locator('table.kogge-split').first()).toBeVisible();
      await shot(page, scheme, 'split-diff');
    });

    test('a run whose jobs wait for the jobs they need', async ({ preview: page }) => {
      await go(page, '/builds/web/storefront/runs/5');
      const graph = page.getByRole('group', { name: 'Jobs and what they wait for' });
      await expect(graph).toBeVisible();
      await graph.getByRole('button', { name: 'unit tests', exact: true }).click();
      await expect(page.getByRole('log')).toContainText('puts exact matches first');
      // The log as the agent wrote it (UTF-8).
      await expect(page.getByRole('log')).toContainText('✓ src/search/normalise.test.ts');
      await expect(page.getByRole('log')).toContainText('Kühlbox');
      await shot(page, scheme, 'graph');
    });
  });
}
