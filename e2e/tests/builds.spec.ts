// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { apiCall, apiGet, expect, signIn, test } from './fixtures';

// The build agent of the repository (make build-server builds it).
const agentBinary = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'kogge-agent');

test('create a pipeline file, run it on an agent and deploy', async ({ page, baseURL }) => {
  test.skip(!existsSync(agentBinary), `${agentBinary} is missing (make build-server)`);
  await signIn(page);
  const key = `B${Date.now() % 100000}`;
  const label = `e2e-${key.toLowerCase()}`;
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Builds ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Shop', readme: true });

  // A pipeline file from a template, edited in the browser.
  await page.goto(`/code/${key.toLowerCase()}/shop/builds`);
  await page.getByRole('button', { name: 'Set up pipelines' }).click();
  await page.getByRole('dialog', { name: 'Set up pipelines' }).getByRole('button', { name: 'Minimal' }).click();
  const dialog = page.getByRole('dialog', { name: 'New file' });
  await dialog.getByRole('textbox', { name: 'Content' }).fill(`version: 1
pipelines:
  ci:
    name: Build and deploy
    on:
      push:
        branches: ["**"]
    stages:
      - name: Build
        jobs:
          - name: compile
            runs-on: [shell, ${label}]
            steps:
              - name: Greet
                run: echo "hello from e2e on $KOGGE_BRANCH"
      - name: Deploy
        jobs:
          - name: production
            runs-on: [shell, ${label}]
            environment: production
            trigger: manual
            steps:
              - run: echo "deploying to $KOGGE_ENVIRONMENT"
`);
  await dialog.getByRole('button', { name: 'Commit changes' }).click();
  await expect(page.getByText('hello from e2e')).toBeVisible();

  // An agent on this machine, with a label only this test uses.
  const dir = mkdtempSync(join(tmpdir(), 'kogge-e2e-agent-'));
  let agent: ChildProcess | undefined;
  try {
    const token = (await apiCall(page, 'POST', '/api/v1/admin/agent-tokens', { description: `e2e ${key}`, expiresInDays: 1 })).token as string;
    const config = join(dir, 'config.json');
    execFileSync(agentBinary, ['register', '-config', config, '-server', baseURL!, '-token', token, '-name', label, '-labels', label, '-shell'], {
      stdio: 'pipe',
    });
    agent = spawn(agentBinary, ['run', '-config', config], { stdio: 'ignore' });

    // A new agent takes jobs only once an administrator approves it in Helling.
    await page.goto('/builds/agents');
    const row = page.getByRole('row', { name: new RegExp(label) });
    await expect(row.getByText('Waiting for approval')).toBeVisible();
    await row.getByRole('button', { name: 'Approve' }).click();
    await expect(row.getByText('Waiting for approval')).toBeHidden();
    await page.goto(`/code/${key.toLowerCase()}/shop`);

    // The push ran the pipeline; the deployment waits for a manual start.
    // The repository's builds are in Helling.
    await page.getByRole('tab', { name: 'Builds' }).click();
    await expect(page).toHaveURL(/\/builds\//);
    await page.getByRole('link', { name: '#1' }).click();
    await expect(page.getByRole('heading', { name: 'Build and deploy #1' })).toBeVisible();
    await expect(page.getByText('Waits for a manual start').first()).toBeVisible({ timeout: 30_000 });
    await page.getByRole('button', { name: 'compile', exact: true }).click();
    await expect(page.getByRole('log')).toContainText('hello from e2e on main');
    await expect(page.getByRole('log')).toContainText('Job succeeded.');

    // Deploy to production.
    await page.getByRole('button', { name: 'Start job', exact: true }).click();
    await expect(page.getByText('Succeeded').first()).toBeVisible();
    await page.getByRole('button', { name: 'production', exact: true }).click();
    await expect(page.getByRole('log')).toContainText('deploying to production', { timeout: 30_000 });
    await page.getByRole('tab', { name: 'Environments' }).click();
    await expect(page.getByRole('heading', { name: 'production' })).toBeVisible();
    await expect(page.getByText('Deployed:')).toBeVisible();

    // Helling's dashboard shows the pipeline of the default branch with its run.
    await page.goto('/builds');
    await expect(page.getByText('Agents online')).toBeVisible();
    await page.getByRole('textbox', { name: 'Find a repository or pipeline' }).fill(key);
    const pipeline = page.getByRole('row', { name: new RegExp(`${key} / Shop`) });
    await expect(pipeline).toBeVisible();
    await expect(pipeline.getByRole('link', { name: '#1', exact: true }).first()).toBeVisible();
    await page.getByRole('tab', { name: 'All runs' }).click();
    await expect(page.getByRole('link', { name: new RegExp(`${key} / Shop`) }).first()).toBeVisible();

    // The agent is listed in the administration.
    await page.goto('/admin/agents');
    await expect(page.getByText(label, { exact: true }).first()).toBeVisible();
  } finally {
    agent?.kill('SIGINT');
    rmSync(dir, { recursive: true, force: true });
    // The agent leaves the administration, too: every run of the test registers one.
    const mine = ((await apiGet(page, '/api/v1/admin/agents')) as { id: number; name: string }[]).find(
      (a) => a.name === label,
    );
    if (mine) await apiCall(page, 'DELETE', `/api/v1/admin/agents/${mine.id}`);
  }
});
