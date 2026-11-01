// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, Anchor, Box, Group, Loader, Switch, Text, Tooltip } from '@mantine/core';
import { IconChevronDown, IconChevronRight, IconDownload, IconFileText } from '@tabler/icons-react';
import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import { LogParser, sections, type LogLine, type Segment } from './ansi';
import './builds.css';

const maxLines = 20000;

/** Streams the log of a job (server-sent events) and keeps it parsed; mount it anew for another job or attempt. */
function useJobLog(jobId: number, attempt: number) {
  const [state, setState] = useState<{ lines: LogLine[]; done: boolean; expired: boolean; loading: boolean }>(
    {
      lines: [],
      done: false,
      expired: false,
      loading: true,
    },
  );
  useEffect(() => {
    const parser = new LogParser();
    let frame = 0;
    const publish = (patch: Partial<typeof state> = {}) => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() =>
        setState((s) => ({ ...s, ...patch, lines: parser.all(), loading: false })),
      );
    };
    const source = new EventSource(`/api/v1/builds/jobs/${jobId}/log/stream?offset=0`);
    source.addEventListener('log', (e) => {
      const msg = JSON.parse((e as MessageEvent<string>).data) as { text: string };
      parser.push(msg.text);
      publish();
    });
    source.addEventListener('end', (e) => {
      const msg = JSON.parse((e as MessageEvent<string>).data) as { expired?: boolean };
      source.close();
      publish({ done: true, expired: !!msg.expired });
    });
    source.onerror = () => {
      // The browser reconnects and continues at the last offset; a closed stream stays closed.
      if (source.readyState === EventSource.CLOSED) publish({ done: true });
    };
    return () => {
      cancelAnimationFrame(frame);
      source.close();
    };
  }, [jobId, attempt]);
  return state;
}

function segmentStyle(s: Segment): CSSProperties | undefined {
  if (!s.fg && !s.bg && !s.bold && !s.dim && !s.italic && !s.underline) return undefined;
  return {
    color: s.fg,
    background: s.bg,
    fontWeight: s.bold ? 700 : undefined,
    opacity: s.dim ? 0.7 : undefined,
    fontStyle: s.italic ? 'italic' : undefined,
    textDecoration: s.underline ? 'underline' : undefined,
  };
}

function Line({ line, number }: { line: LogLine; number: number }) {
  return (
    <div className={`kogge-log-line kogge-log-${line.kind}`}>
      <span className="kogge-log-number">{number}</span>
      <span className="kogge-log-text">
        {line.segments.map((s, i) => (
          <span key={i} style={segmentStyle(s)}>
            {s.text}
          </span>
        ))}
      </span>
    </div>
  );
}

/** The log of a job: live while it runs, with foldable steps. */
export function LogView({ jobId, attempt, running }: { jobId: number; attempt: number; running: boolean }) {
  const { t } = useTranslation();
  const log = useJobLog(jobId, attempt);
  const [follow, setFollow] = useState(true);
  const [folded, setFolded] = useState<Set<number>>(new Set());
  const end = useRef<HTMLDivElement>(null);
  const lines = log.lines.length > maxLines ? log.lines.slice(log.lines.length - maxLines) : log.lines;
  const offset = log.lines.length - lines.length;
  const groups = useMemo(() => sections(lines), [lines]);
  useEffect(() => {
    if (follow && running) end.current?.scrollIntoView({ block: 'nearest' });
  }, [lines, follow, running]);

  const toggle = (start: number) =>
    setFolded((f) => {
      const next = new Set(f);
      if (next.has(start)) next.delete(start);
      else next.add(start);
      return next;
    });

  return (
    <Box>
      <Group justify="space-between" mb={6}>
        <Group gap="xs">
          {!log.done && <Loader size="xs" type="dots" />}
          {running && (
            <Switch
              size="xs"
              label={t('builds.followLog')}
              checked={follow}
              onChange={(e) => setFollow(e.currentTarget.checked)}
            />
          )}
        </Group>
        <Group gap={4}>
          <Tooltip label={t('builds.rawLog')}>
            <ActionIcon
              variant="subtle"
              component="a"
              href={`/api/v1/builds/jobs/${jobId}/log/raw`}
              target="_blank"
              aria-label={t('builds.rawLog')}
            >
              <IconFileText size={16} />
            </ActionIcon>
          </Tooltip>
          <Tooltip label={t('builds.downloadLog')}>
            <ActionIcon
              variant="subtle"
              component="a"
              href={`/api/v1/builds/jobs/${jobId}/log/raw?download=1`}
              aria-label={t('builds.downloadLog')}
            >
              <IconDownload size={16} />
            </ActionIcon>
          </Tooltip>
        </Group>
      </Group>
      <div className="kogge-log" role="log" aria-live={running ? 'polite' : 'off'}>
        {log.expired && <Text c="dimmed">{t('builds.logExpired')}</Text>}
        {!log.loading && !log.expired && log.lines.length === 0 && (
          <Text c="dimmed">{log.done ? t('builds.logEmpty') : t('builds.logWaiting')}</Text>
        )}
        {offset > 0 && (
          <Text c="dimmed" size="xs" mb={4}>
            {t('builds.logTruncated', { count: offset })}{' '}
            <Anchor href={`/api/v1/builds/jobs/${jobId}/log/raw`} target="_blank" size="xs">
              {t('builds.rawLog')}
            </Anchor>
          </Text>
        )}
        {groups.map((g) =>
          g.title ? (
            <div key={g.start}>
              <button
                type="button"
                className="kogge-log-line kogge-log-step"
                onClick={() => toggle(g.start)}
                aria-expanded={!folded.has(g.start)}
              >
                <span className="kogge-log-number">{offset + g.start + 1}</span>
                <span className="kogge-log-text">
                  {folded.has(g.start) ? <IconChevronRight size={12} /> : <IconChevronDown size={12} />}{' '}
                  {g.title.segments[0]?.text}
                </span>
              </button>
              {!folded.has(g.start) &&
                g.lines.map((l, i) => <Line key={i} line={l} number={offset + g.start + i + 2} />)}
            </div>
          ) : (
            <div key="head">
              {g.lines.map((l, i) => (
                <Line key={i} line={l} number={offset + i + 1} />
              ))}
            </div>
          ),
        )}
        <div ref={end} />
      </div>
    </Box>
  );
}
