// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { isGraph, layoutGraph } from './graph';
import type { JobInfo, StageInfo } from './hooks';

let next = 1;
function job(name: string, needs?: number[]): JobInfo {
  return {
    id: next++,
    name,
    state: 'created',
    attempt: 1,
    manual: false,
    allowFailure: false,
    steps: [],
    artifacts: [],
    needs,
    hasNeeds: needs !== undefined,
  } as JobInfo;
}

describe('layoutGraph', () => {
  it('places jobs after the jobs they need and after the earlier stages', () => {
    const compile = job('compile');
    const slow = job('slow');
    const unit = job('unit', [compile.id]);
    const lint = job('lint', [compile.id]);
    const docs = job('docs');
    const pkg = job('package', [unit.id, lint.id]);
    const after = job('after-all');
    const early = job('early', []);
    const stages: StageInfo[] = [
      { name: 'Build', jobs: [compile, slow] },
      { name: 'Test', jobs: [unit, lint, docs] },
      { name: 'Package', jobs: [pkg, after, early] },
    ];
    expect(isGraph(stages)).toBe(true);
    const g = layoutGraph(stages);
    const at = Object.fromEntries(g.nodes.map((n) => [n.job.name, [n.column, n.row]]));
    expect(at).toEqual({
      compile: [0, 0],
      slow: [0, 1],
      early: [0, 2],
      unit: [1, 0],
      lint: [1, 1],
      docs: [1, 2],
      package: [2, 0],
      'after-all': [2, 1],
    });
    expect(g.columns).toBe(3);
    expect(g.rows).toBe(3);
    expect(g.edges.map((e) => `${e.from.job.name}>${e.to.job.name}`)).toEqual([
      'compile>unit',
      'compile>lint',
      'unit>package',
      'lint>package',
    ]);
    // Reading order: column by column, top to bottom.
    expect(g.nodes.map((n) => n.job.name)).toEqual([
      'compile',
      'slow',
      'early',
      'unit',
      'lint',
      'docs',
      'package',
      'after-all',
    ]);
  });

  it('keeps jobs near the jobs they need', () => {
    const a = job('a');
    const b = job('b');
    const fromB = job('from-b', [b.id]);
    const fromA = job('from-a', [a.id]);
    const g = layoutGraph([{ name: 'S', jobs: [a, b, fromB, fromA] }]);
    const row = (name: string) => g.nodes.find((n) => n.job.name === name)!.row;
    expect([row('from-a'), row('from-b')]).toEqual([0, 1]);
  });

  it('tells stages from graphs', () => {
    expect(
      isGraph([
        { name: 'A', jobs: [job('a')] },
        { name: 'B', jobs: [job('b')] },
      ]),
    ).toBe(false);
  });
});
