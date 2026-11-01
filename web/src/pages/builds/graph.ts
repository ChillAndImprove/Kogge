// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { JobInfo, StageInfo } from './hooks';

/** A job placed in the graph of a run: its column is how many jobs it waits for in a row (its depth). */
export interface GraphNode {
  job: JobInfo;
  stage: string;
  column: number;
  row: number;
  /** The jobs it needs (needs of the pipeline file); jobs without needs wait for the earlier stages. */
  needs: JobInfo[];
}

/** A line from a job to a job that needs it. */
export interface GraphEdge {
  from: GraphNode;
  to: GraphNode;
}

export interface Graph {
  nodes: GraphNode[];
  edges: GraphEdge[];
  columns: number;
  rows: number;
}

/** Whether the run's jobs form a graph rather than stages: some job names the jobs it needs. */
export function isGraph(stages: StageInfo[]): boolean {
  return stages.some((s) => s.jobs.some((j) => j.hasNeeds));
}

/**
 * Lays out the jobs of a run from left to right: a job with needs one column after the latest job it needs, a job
 * without needs after all jobs of the earlier stages. Within a column, jobs sit near the jobs they need, so that
 * fewer lines cross. Nodes are in reading order: column by column, top to bottom.
 */
export function layoutGraph(stages: StageInfo[]): Graph {
  const byId = new Map<number, { job: JobInfo; stage: number; name: string; order: number }>();
  let order = 0;
  stages.forEach((s, stage) =>
    s.jobs.forEach((job) => byId.set(job.id, { job, stage, name: s.name, order: order++ })),
  );
  const depth = new Map<number, number>();
  const visiting = new Set<number>();
  const earlierMax = (stage: number): number => {
    let d = -1;
    for (const [id, info] of byId) if (info.stage < stage) d = Math.max(d, depthOf(id));
    return d;
  };
  const depthOf = (id: number): number => {
    const known = depth.get(id);
    if (known !== undefined) return known;
    const info = byId.get(id)!;
    if (visiting.has(id)) return 0; // the server refuses cycles; this keeps a broken run drawable
    visiting.add(id);
    let d: number;
    if (info.job.hasNeeds) {
      d = 0;
      for (const n of info.job.needs ?? []) if (byId.has(n)) d = Math.max(d, depthOf(n) + 1);
    } else {
      d = earlierMax(info.stage) + 1;
    }
    visiting.delete(id);
    depth.set(id, d);
    return d;
  };
  const columns: GraphNode[][] = [];
  for (const [id, info] of [...byId].sort((a, b) => a[1].order - b[1].order)) {
    const column = depthOf(id);
    const node: GraphNode = {
      job: info.job,
      stage: info.name,
      column,
      row: 0,
      needs: (info.job.needs ?? []).flatMap((n) => (byId.has(n) ? [byId.get(n)!.job] : [])),
    };
    (columns[column] ??= []).push(node);
  }
  const nodes = new Map<number, GraphNode>();
  columns.forEach((col) => col?.forEach((n, i) => ((n.row = i), nodes.set(n.job.id, n))));
  const neededBy = new Map<number, GraphNode[]>();
  for (const n of nodes.values()) {
    for (const j of n.needs) neededBy.set(j.id, [...(neededBy.get(j.id) ?? []), n]);
  }
  // A few sweeps to and fro: a job's place is the mean of the places of the jobs it needs (from left to right),
  // then of the jobs that need it (from right to left). Jobs without lines that way keep their place.
  const mean = (list: GraphNode[], own: number) =>
    list.length ? list.reduce((a, n) => a + n.row, 0) / list.length : own;
  const sort = (col: GraphNode[] | undefined, key: (n: GraphNode) => number) => {
    if (!col) return;
    col
      .map((n) => ({ n, k: key(n) }))
      .sort((a, b) => a.k - b.k)
      .forEach(({ n }, i) => (n.row = i));
    col.sort((a, b) => a.row - b.row);
  };
  for (let sweep = 0; sweep < 3; sweep++) {
    for (const col of columns.slice(1))
      sort(col, (n) =>
        mean(
          n.needs.map((j) => nodes.get(j.id)!),
          n.row,
        ),
      );
    for (const col of columns.slice(0, -1).reverse())
      sort(col, (n) => mean(neededBy.get(n.job.id) ?? [], n.row));
  }
  // The last word is the left-to-right one: jobs follow the jobs they need.
  for (const col of columns.slice(1))
    sort(col, (n) =>
      mean(
        n.needs.map((j) => nodes.get(j.id)!),
        n.row,
      ),
    );
  const placed = columns.flatMap((col) => col ?? []);
  const edges = placed.flatMap((to) => to.needs.map((j) => ({ from: nodes.get(j.id)!, to })));
  return {
    nodes: placed,
    edges,
    columns: columns.length,
    rows: Math.max(0, ...columns.map((c) => c?.length ?? 0)),
  };
}
