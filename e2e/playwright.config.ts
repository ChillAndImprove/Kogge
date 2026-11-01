// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { defineConfig, type PlaywrightTestOptions } from '@playwright/test';
import { adminState } from './tests/fixtures';

// The browser of a run (KOGGE_E2E_BROWSER): Google Chrome by default, or another Chromium channel
// (KOGGE_E2E_BROWSER_CHANNEL); firefox and webkit are Playwright's builds (npx playwright install firefox webkit).
// One run tests one browser, since the tests share a server; make e2e-browsers runs them one after another.
const browsers: Record<string, Partial<PlaywrightTestOptions>> = {
  chrome: { browserName: 'chromium', channel: process.env.KOGGE_E2E_BROWSER_CHANNEL ?? 'chrome' },
  firefox: { browserName: 'firefox' },
  webkit: { browserName: 'webkit' },
};
const browser = process.env.KOGGE_E2E_BROWSER ?? 'chrome';
if (!browsers[browser]) {
  throw new Error(`KOGGE_E2E_BROWSER must be one of ${Object.keys(browsers).join(', ')}, not ${browser}`);
}
// Playwright's Firefox now and then loses track of a page it opens while another browser is busy (its driver logs
// "FrameTree.js … this.domWindow() is undefined", and the page never reports `load`): Firefox runs one test at a
// time, and a test that fails so is tried once more (reported as flaky, not as passed).
const firefox = browser === 'firefox';

// The tests run against a running Kogge (KOGGE_E2E_URL) with the administrator admin / KOGGE_E2E_ADMIN_PASSWORD,
// signed in once by the global setup.
export default defineConfig({
  testDir: './tests',
  // Each browser keeps its own failures (traces and page snapshots), so that a run in another does not remove them.
  outputDir: `test-results/${browser}`,
  globalSetup: './global-setup.ts',
  timeout: 60_000,
  fullyParallel: false,
  // The tests share one server; more workers mostly measure the machine (KOGGE_E2E_WORKERS overrides).
  workers: Number(process.env.KOGGE_E2E_WORKERS ?? (firefox ? 1 : 2)),
  retries: firefox ? 1 : 0,
  reporter: [['list']],
  use: {
    ...browsers[browser],
    baseURL: process.env.KOGGE_E2E_URL ?? 'http://localhost:18080',
    headless: true,
    locale: 'en-GB',
    trace: 'retain-on-failure',
    storageState: adminState,
  },
  projects: [
    { name: 'main', testIgnore: /instance\// },
    // Tests that change settings of the whole instance (such as a sign-in policy or the limits) run alone, after the
    // others, one at a time.
    { name: 'instance', testMatch: /instance\/.*\.spec\.ts/, dependencies: ['main'], workers: 1 },
  ],
});
