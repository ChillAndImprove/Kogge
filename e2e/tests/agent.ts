// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { execFileSync, spawn } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { apiCall, apiGet } from './fixtures';

/** The build agent of the repository (make build-server builds it). */
export const agentBinary = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'kogge-agent');

export const haveAgent = existsSync(agentBinary);

/**
 * Starts an agent of this machine with the shell executor and a label of its own, approved through its token;
 * stop ends it and removes it from the administration.
 */
export async function startAgent(page: Page, baseURL: string, label: string) {
  const dir = mkdtempSync(join(tmpdir(), 'kogge-e2e-agent-'));
  const token = (
    await apiCall(page, 'POST', '/api/v1/admin/agent-tokens', {
      description: `e2e ${label}`,
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
      baseURL,
      '-token',
      token,
      '-name',
      label,
      '-labels',
      label,
      '-shell',
    ],
    { stdio: 'pipe' },
  );
  const agent = spawn(agentBinary, ['run', '-config', config], { stdio: 'ignore' });
  return {
    async stop() {
      agent.kill('SIGINT');
      rmSync(dir, { recursive: true, force: true });
      const mine = ((await apiGet(page, '/api/v1/admin/agents')) as { id: number; name: string }[]).find(
        (a) => a.name === label,
      );
      if (mine) await apiCall(page, 'DELETE', `/api/v1/admin/agents/${mine.id}`);
    },
  };
}
