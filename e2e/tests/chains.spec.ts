// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { agentBinary, haveAgent, startAgent } from './agent';
import { apiCall, expect, signIn, test, uniqueKey } from './fixtures';

test('a deployment runs after the build and ships what it built', async ({ page, baseURL }) => {
  test.skip(!haveAgent, `${agentBinary} is missing (make build-server)`);
  await signIn(page);
  const key = uniqueKey('P');
  const label = `e2e-${key.toLowerCase()}`;
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Chain ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Shop', readme: true });
  const agent = await startAgent(page, baseURL!, label);
  try {
    await apiCall(page, 'PUT', `/api/v1/code/repos/${key}/shop/files`, {
      branch: 'main',
      path: '.kogge/pipelines.yml',
      content: `version: 1
pipelines:
  build:
    name: Build
    on:
      push: { branches: ["**"] }
    stages:
      - name: Build
        jobs:
          - name: compile
            runs-on: [shell, ${label}]
            artifacts: [{ name: app, paths: ["out/**"] }]
            steps:
              - run: mkdir -p out && echo "app-$KOGGE_RUN_NUMBER" > out/app.txt
  deploy:
    name: Deploy
    on:
      pipeline: { name: build, branches: [main] }
    stages:
      - name: Deploy
        jobs:
          - name: ship
            runs-on: [shell, ${label}]
            steps:
              - run: echo "shipping $(cat out/app.txt) after $KOGGE_UPSTREAM_PIPELINE $KOGGE_UPSTREAM_RESULT"
`,
    });

    // The build names the deployment that followed it.
    const runs = `/builds/${key.toLowerCase()}/shop/runs`;
    await page.goto(`${runs}/1`);
    await expect(page.getByRole('heading', { name: 'Build #1' })).toBeVisible();
    const next = page.getByRole('list', { name: 'Then started' });
    await expect(next.getByRole('link', { name: 'Deploy #2' })).toBeVisible({ timeout: 60_000 });
    await next.getByRole('link', { name: 'Deploy #2' }).click();

    // The deployment ran at the build's commit with its artifact, and links back to it.
    await expect(page).toHaveURL(new RegExp(`${runs}/2$`));
    await expect(page.getByRole('heading', { name: 'Deploy #2' })).toBeVisible();
    await expect(page.getByText('After Build #1')).toBeVisible();
    await expect(page.getByRole('log')).toContainText('shipping app-1 after build success', {
      timeout: 60_000,
    });
    await page.getByRole('link', { name: 'Build #1' }).click();
    await expect(page).toHaveURL(new RegExp(`${runs}/1$`));
  } finally {
    await agent.stop();
  }
});
