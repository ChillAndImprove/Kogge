// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { layoutGraph, type GraphNode } from './graph';
import type { StageInfo } from './hooks';
import { JobCard, skipText } from './JobCard';

// The size of a job and the space between them, in pixels.
const width = 220;
const height = 62;
const gapX = 48;
const gapY = 10;

const left = (n: GraphNode) => n.column * (width + gapX);
const top = (n: GraphNode) => n.row * (height + gapY);

/**
 * The jobs of a run whose pipeline file names what they need: from left to right as they wait for each other, with a
 * line from every job to the jobs that need it. Jobs without needs wait for the earlier stages; they stand after
 * them. The lines are drawing only: every job tells screen readers what it waits for.
 */
export function JobGraph({
  stages,
  selected,
  canRun,
  onSelect,
}: {
  stages: StageInfo[];
  selected?: number;
  canRun: boolean;
  onSelect: (id: number) => void;
}) {
  const { t } = useTranslation();
  const graph = useMemo(() => layoutGraph(stages), [stages]);
  const w = Math.max(0, graph.columns * (width + gapX) - gapX);
  const h = Math.max(0, graph.rows * (height + gapY) - gapY);
  const describe = (n: GraphNode) =>
    [
      t('builds.inStage', { stage: n.stage }),
      !n.job.hasNeeds
        ? t('builds.afterStages')
        : n.needs.length > 0
          ? t('builds.needsList', { jobs: n.needs.map((j) => j.name).join(', ') })
          : t('builds.needsNone'),
      n.job.skip ? skipText(t, n.job.skip) : '',
    ]
      .filter(Boolean)
      .join(' ');
  return (
    <div className="kogge-graph-scroll">
      <div
        className="kogge-graph"
        role="group"
        aria-label={t('builds.graph')}
        style={{ width: w, height: h }}
      >
        <svg width={w} height={h} aria-hidden="true" focusable="false">
          {graph.edges.map((e) => {
            const x1 = left(e.from) + width;
            const y1 = top(e.from) + height / 2;
            const x2 = left(e.to);
            const y2 = top(e.to) + height / 2;
            const dx = Math.max(16, (x2 - x1) / 2);
            return (
              <path
                key={`${e.from.job.id}-${e.to.job.id}`}
                className="kogge-graph-edge"
                data-state={e.from.job.state}
                data-active={e.from.job.id === selected || e.to.job.id === selected}
                d={`M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`}
              />
            );
          })}
        </svg>
        {graph.nodes.map((n) => (
          <div
            key={n.job.id}
            className="kogge-graph-node"
            data-job={n.job.name}
            style={{ left: left(n), top: top(n), width, height }}
          >
            <JobCard
              job={n.job}
              caption={n.stage}
              description={describe(n)}
              selected={n.job.id === selected}
              canRun={canRun}
              onSelect={() => onSelect(n.job.id)}
            />
          </div>
        ))}
      </div>
    </div>
  );
}
