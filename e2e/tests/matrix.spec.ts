// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { apiCall, apiGet, expect, signIn, test, uniqueKey } from './fixtures';

test('the jobs of a matrix are grouped under their job', async ({ page }) => {
  await signIn(page);
  const key = uniqueKey('M');
  const label = `e2e-${key.toLowerCase()}`; // no agent has it: the jobs wait
  await apiCall(page, 'POST', '/api/v1/code/projects', { key, name: `Matrix ${key}` });
  await apiCall(page, 'POST', `/api/v1/code/projects/${key}/repos`, { name: 'Service', readme: true });
  await apiCall(page, 'PUT', `/api/v1/code/repos/${key}/service/files`, {
    branch: 'main',
    path: '.kogge/pipelines.yml',
    content: `version: 1
pipelines:
  ci:
    stages:
      - name: Test
        jobs:
          - name: unit
            runs-on: [${label}]
            matrix:
              GO_VERSION: ["1.26", "1.27"]
              DATABASE: [postgres, sqlite]
            matrix-exclude:
              - { GO_VERSION: "1.26", DATABASE: sqlite }
            steps:
              - run: go test ./...
          - name: lint
            runs-on: [${label}]
            steps:
              - run: make lint
`,
  });
  await expect
    .poll(async () => (await apiGet(page, `/api/v1/code/repos/${key}/service/runs`)).items.length)
    .toBe(1);
  const run = (await apiGet(page, `/api/v1/code/repos/${key}/service/runs`)).items[0];

  await page.goto(`/builds/${key.toLowerCase()}/service/runs/${run.number}`);
  const matrix = page.getByRole('group', { name: 'unit' });
  await expect(matrix).toContainText('3 jobs');
  for (const name of ['unit (1.26, postgres)', 'unit (1.27, postgres)', 'unit (1.27, sqlite)']) {
    await expect(matrix.getByRole('button', { name })).toBeVisible();
  }
  await expect(matrix.getByRole('button', { name: 'unit (1.27, sqlite)' })).toHaveText('1.27, sqlite');
  // The job outside the matrix stands alone.
  await expect(page.getByRole('button', { name: 'lint', exact: true })).toBeVisible();
  await expect(matrix.getByRole('button', { name: 'lint', exact: true })).toHaveCount(0);

  await matrix.getByRole('button', { name: 'unit (1.27, sqlite)' }).click();
  await expect(page.getByRole('heading', { name: 'unit (1.27, sqlite)' })).toBeVisible();

  await apiCall(page, 'POST', `/api/v1/builds/runs/${run.id}/cancel`);
});
