// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Alert, Code, Group, Loader, Tooltip, useComputedColorScheme } from '@mantine/core';
import { IconCode, IconSchema } from '@tabler/icons-react';
import DOMPurify from 'dompurify';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

type Mermaid = typeof import('mermaid').default;

let loaded: Promise<Mermaid> | undefined;

/** Mermaid is large: it loads when the first diagram is shown. */
function loadMermaid(): Promise<Mermaid> {
  loaded ??= import('mermaid').then((m) => m.default);
  return loaded;
}

let renders = 0;
// Mermaid's configuration is global: diagrams are drawn one after the other.
let queue: Promise<unknown> = Promise.resolve();

/**
 * Draws a diagram as SVG text; Mermaid's strict mode encodes the text of labels and allows no scripts or links.
 * Mermaid measures the labels within place, so that they get the size they have in the page.
 */
function draw(code: string, dark: boolean, place: HTMLElement): Promise<string> {
  const run = queue.then(async () => {
    const mermaid = await loadMermaid();
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: 'strict',
      theme: dark ? 'dark' : 'default',
      fontFamily: '"Inter Variable", Inter, system-ui, sans-serif',
      maxTextSize: 100_000,
      maxEdges: 2000,
      // Mermaid wraps labels at 120 px, which breaks German compounds (Zahlungserinnerung) within the word.
      flowchart: { wrappingWidth: 200 },
    });
    const id = `kogge-mermaid-${++renders}`;
    try {
      await mermaid.parse(code);
      const { svg } = await mermaid.render(id, code, place);
      return svg;
    } finally {
      // What Mermaid leaves in the page when a diagram fails.
      document.getElementById(id)?.remove();
      document.getElementById(`d${id}`)?.remove();
    }
  });
  queue = run.catch(() => undefined);
  return run;
}

/**
 * A Mermaid diagram (in the colour scheme of the interface). The SVG is sanitised once more before it becomes part
 * of the page; its source is a click away, and a diagram that cannot be drawn shows why.
 */
export function MermaidDiagram({ code }: { code: string }) {
  const { t } = useTranslation();
  const dark = useComputedColorScheme('light') === 'dark';
  const box = useRef<HTMLDivElement>(null);
  const [drawn, setDrawn] = useState<{ code: string; dark: boolean; error?: string }>();
  const [source, setSource] = useState(false);
  useEffect(() => {
    let current = true;
    if (!box.current) return;
    draw(code, dark, box.current).then(
      (svg) => {
        if (!current || !box.current) return;
        // Labels are HTML (and formulas MathML) within <foreignObject>, which DOMPurify sanitises once told it may
        // contain HTML.
        const fragment = DOMPurify.sanitize(svg, {
          USE_PROFILES: { svg: true, svgFilters: true, html: true, mathMl: true },
          ADD_TAGS: ['foreignObject'],
          HTML_INTEGRATION_POINTS: { foreignobject: true },
          RETURN_DOM_FRAGMENT: true,
        });
        box.current.replaceChildren(fragment);
        setDrawn({ code, dark });
      },
      (e: unknown) => {
        if (!current) return;
        box.current?.replaceChildren();
        setDrawn({ code, dark, error: e instanceof Error ? e.message : String(e) });
      },
    );
    return () => {
      current = false;
    };
  }, [code, dark]);
  const busy = drawn?.code !== code || drawn?.dark !== dark;
  const failed = !busy && drawn?.error;
  return (
    <figure className="kogge-mermaid">
      <Group justify="flex-end" gap={4} className="kogge-mermaid-tools">
        <Tooltip label={source ? t('mermaid.diagram') : t('mermaid.source')} withinPortal>
          <ActionIcon
            size="sm"
            variant="subtle"
            color="gray"
            onClick={() => setSource((s) => !s)}
            aria-label={source ? t('mermaid.diagram') : t('mermaid.source')}
            aria-pressed={source}
          >
            {source ? <IconSchema size={14} /> : <IconCode size={14} />}
          </ActionIcon>
        </Tooltip>
      </Group>
      {busy && !failed && <Loader size="sm" aria-label={t('mermaid.drawing')} />}
      {failed && (
        <Alert color="orange" variant="light" title={t('mermaid.error')}>
          {drawn.error}
        </Alert>
      )}
      <div ref={box} className="kogge-mermaid-svg" hidden={source || !!failed} />
      {(source || failed) && <Code block>{code}</Code>}
    </figure>
  );
}
