// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { createInstance } from 'i18next';
import { describe, expect, it } from 'vitest';
import de from '../../i18n/de';
import en from '../../i18n/en';
import { checkText, strategyText, type MergeCheck, type StrategyCheck } from './checks';

async function translator(lng: 'de' | 'en') {
  const i18n = createInstance();
  await i18n.init({
    resources: { de: { translation: de }, en: { translation: en } },
    lng,
    fallbackLng: 'en',
    interpolation: { escapeValue: false },
  });
  return i18n.t;
}

const check = (c: Partial<MergeCheck> & Pick<MergeCheck, 'key' | 'reason'>): MergeCheck => ({
  ok: false,
  message: 'English from the server.',
  ...c,
});

describe('checkText', () => {
  it('words the checks with their numbers in English', async () => {
    const t = await translator('en');
    expect(
      checkText(t, check({ key: 'approvals', reason: 'count', values: { approved: 1, required: 2 } })),
    ).toBe('Approvals: 1 of 2 required.');
    expect(checkText(t, check({ key: 'tasks', reason: 'open', values: { count: 1 } }))).toBe(
      'One task is open.',
    );
    expect(checkText(t, check({ key: 'tasks', reason: 'open', values: { count: 3 } }))).toBe(
      '3 tasks are open.',
    );
    expect(checkText(t, check({ key: 'needs-work', reason: 'some', values: { count: 2 } }))).toBe(
      '2 reviewers ask for more work.',
    );
    expect(checkText(t, check({ key: 'conflicts', reason: 'clean', ok: true }))).toBe('No conflicts.');
  });

  it('counts successful builds apart from skipped ones', async () => {
    const t = await translator('en');
    const passed = check({
      key: 'builds',
      reason: 'passed',
      ok: true,
      values: { done: 3, required: 2, successful: 1, skipped: 2, running: 0, failed: 0 },
    });
    expect(checkText(t, passed)).toBe(
      'One successful build. 2 pipelines were skipped: no changed file matches their paths.',
    );
    expect(checkText(t, { ...passed, values: { ...passed.values, done: 2, successful: 0 } })).toBe(
      '2 pipelines were skipped: no changed file matches their paths.',
    );
    const running = check({
      key: 'builds',
      reason: 'running',
      values: { done: 1, required: 2, successful: 1, skipped: 0, running: 1, failed: 0 },
    });
    expect(checkText(t, running)).toBe('Required builds: 1 of 2 succeeded, 1 still running.');
    const failed = check({
      key: 'builds',
      reason: 'failed',
      values: { done: 0, required: 1, successful: 0, skipped: 1, running: 0, failed: 2 },
    });
    expect(checkText(t, failed)).toBe(
      '2 builds of the source commit failed. One pipeline was skipped: no changed file matches its paths.',
    );
  });

  it('speaks German', async () => {
    const t = await translator('de');
    expect(
      checkText(t, check({ key: 'approvals', reason: 'count', values: { approved: 0, required: 1 } })),
    ).toBe('Genehmigungen: 0 von 1 benötigten.');
    expect(checkText(t, check({ key: 'tasks', reason: 'open', values: { count: 2 } }))).toBe(
      '2 Aufgaben sind offen.',
    );
    const missing = check({
      key: 'builds',
      reason: 'missing',
      values: { done: 1, required: 2, successful: 0, skipped: 1, running: 0, failed: 0 },
    });
    expect(checkText(t, missing)).toBe(
      'Benötigte Builds: 1 von 2 erfolgreich. Eine Pipeline wurde übersprungen: Keine geänderte Datei passt zu ihren Pfaden.',
    );
  });

  it('words what a strategy needs, with the commit and files of a rebase that does not apply', async () => {
    const en = await translator('en');
    const conflicted: StrategyCheck = {
      strategy: 'rebase',
      ok: false,
      reason: 'conflicted',
      commit: '0123456789abcdef0123456789abcdef01234567',
      subject: 'Try another rate',
      paths: ['rate.txt', 'docs/rates.md'],
      message: 'English from the server.',
    };
    expect(strategyText(en, conflicted)).toBe(
      'The commit 0123456789 (Try another rate) cannot be applied to the target branch: it conflicts in rate.txt, docs/rates.md. Rebase the source branch yourself, or choose another strategy.',
    );
    const de = await translator('de');
    expect(strategyText(de, { ...conflicted, reason: 'too-many', limit: 250 })).toBe(
      'Der Quell-Branch hat mehr als 250 Commits zum Rebasen; rebasen Sie ihn selbst, oder wählen Sie eine andere Strategie.',
    );
    expect(strategyText(de, { ...conflicted, strategy: 'fast-forward', reason: 'diverged' })).toMatch(
      /^Der Ziel-Branch hat Commits/,
    );
  });

  it("falls back to the server's message for reasons it does not know", async () => {
    const t = await translator('de');
    const unknown = {
      ...check({ key: 'builds', reason: 'unavailable' }),
      reason: 'later',
    } as unknown as MergeCheck;
    expect(checkText(t, unknown)).toBe('English from the server.');
  });
});
