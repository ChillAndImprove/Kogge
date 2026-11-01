// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { defineConfig } from '@playwright/test';

// The preview for GitHub Pages (make showcase): seed.spec.ts fills an empty Kogge (KOGGE_SHOWCASE_URL) with sample
// content, record.spec.ts visits its pages and keeps the server's answers in web/showcase/recording.json, from which
// the web application is built to run without a server.
export default defineConfig({
  testDir: './showcase',
  timeout: 30 * 60_000,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL: process.env.KOGGE_SHOWCASE_URL ?? 'http://localhost:18095',
    channel: process.env.KOGGE_E2E_BROWSER_CHANNEL ?? 'chrome',
    headless: true,
    locale: 'en-GB',
    viewport: { width: 1440, height: 900 },
    // What cannot be done (a button a page does not have) fails soon instead of at the end of the long test.
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
  },
});
