// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Anchor,
  Avatar,
  Box,
  Button,
  Group,
  SegmentedControl,
  Skeleton,
  Stack,
  Switch,
  Text,
} from '@mantine/core';
import { useInfiniteQuery } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useMemo, useState, type ReactNode } from 'react';
import { Trans, useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { api, unwrap } from '../api/client';
import { formatDateTime, formatRelative } from '../app/format';
import { useOptionalSession } from '../app/session';
import { StateIcon } from '../pages/builds/status';
import { StatusBadge } from '../pages/issues/shared';
import { products, productList, type ProductId } from '../products';
import {
  changedFields,
  dayKey,
  dayKind,
  groupEntries,
  isRun,
  pushedCommits,
  statusChain,
  type Entry,
  type EntryGroup,
} from './activityGroups';
import { ErrorAlert } from './ErrorAlert';
import './activity.css';

export interface ActivityStreamProps {
  /** One issue project, space or repository (KEY/slug); without them, everything the user may see. */
  project?: string;
  space?: string;
  repository?: string;
  /** What one person did (user name): the lines leave the person out. */
  user?: string;
  /** Only this product. */
  product?: ProductId;
  /** Offer the filter by product (streams across products). */
  productFilter?: boolean;
  /** Entries per page. */
  limit?: number;
  /** compact: smaller lines without the filters, for gadgets and side columns. */
  density?: 'comfortable' | 'compact';
}

function initials(name: string) {
  return name
    .split(/\s+/)
    .map((p) => p[0])
    .join('')
    .slice(0, 2)
    .toUpperCase();
}

/** How a stream shows its lines. */
interface LineContext {
  /** The stream is that of a project, space or repository: lines do not name it. */
  inContainer: boolean;
  /** The stream spans products: lines show their product's logo. */
  crossProduct: boolean;
  /** The stream is that of one person: lines leave the person out. */
  self: boolean;
  compact: boolean;
  today: string;
  timeZone?: string;
}

/** The size of the sentences of lines: the base size, a step smaller when compact. Their links are underlined on
 * hover; within the sentence they stand out by their weight, not only by their colour (WCAG 1.4.1). */
const sentenceSize = (ctx: LineContext) => (ctx.compact ? 'sm' : 'md');

/** Half the smallest gap of the theme: between the parts of a line. */
const half = 'calc(var(--mantine-spacing-xs) / 2)';

/** The size of the avatars and state icons in front of lines. */
const leadSize = (ctx: LineContext) => (ctx.compact ? 24 : 28);

/** What happened, in words before the link to what it happened to (runs, deployments and all but pushes). */
function verbPhrase(t: TFunction, e: Entry, count = 1): string {
  if (isRun(e)) {
    const state = e.verb.slice(4);
    return count > 1
      ? t(`activity.verb.runs.${state}` as 'activity.verb.runs.succeeded', { count, defaultValue: e.verb })
      : t(`activity.verb.${e.verb}` as 'activity.verb.run.succeeded', { defaultValue: e.verb });
  }
  if (e.verb.startsWith('deployment.')) {
    return t(`activity.verb.${e.verb}` as 'activity.verb.deployment.succeeded', {
      environment: e.detail ?? '',
    });
  }
  if (count > 1 && e.verb.endsWith('.commented')) return t('activity.verb.commentedTimes', { count });
  if (count > 1 && e.verb === 'page.updated') return t('activity.verb.versions', { count });
  return t(`activity.verb.${e.verb}` as 'activity.verb.issue.created', { defaultValue: e.verb });
}

/** Whether the sentence of an entry has no person in front (runs, failed deployments). */
function impersonal(e: Entry) {
  return isRun(e) || e.verb === 'deployment.failed';
}

/** The sentence of a push: how many commits went to which branches, and where (outside the repository). */
function PushSentence({ group, ctx }: { group: Entry[]; ctx: LineContext }) {
  const e = group[0];
  const refs = (e.refs ?? []).map((r) => r.name).join(', ');
  const deleted = (e.refs ?? []).length > 0 && (e.refs ?? []).every((r) => r.change === 'deleted');
  const commits = pushedCommits(group);
  const where = ctx.inContainer ? '' : 'In';
  const key = deleted
    ? `deleted${where}`
    : commits.count
      ? `commits${where}`
      : group.length > 1
        ? `times${where}`
        : `to${where}`;
  return (
    <Trans
      i18nKey={`activity.push.${key}` as 'activity.push.to'}
      count={deleted ? undefined : (commits.count ?? group.length)}
      values={{ refs, repo: e.container.key }}
      components={{
        ref: <Anchor component={Link} to={e.url} size={sentenceSize(ctx)} fw={500} underline="hover" />,
        repo: (
          <Anchor component={Link} to={e.container.url} size={sentenceSize(ctx)} fw={500} underline="hover" />
        ),
      }}
    />
  );
}

/** What a change was: the statuses, the fields, the beginning of a comment or note, the commits of a push. */
function Details({ group, ctx }: { group: Entry[]; ctx: LineContext }) {
  const { t } = useTranslation();
  const e = group[0];
  if (e.verb === 'issue.transitioned') {
    const chain = statusChain(group);
    if (chain.length === 0) return null;
    return (
      <Group gap={half} wrap="wrap" role="group" aria-label={t('issues.status')}>
        {chain.map((s, i) => (
          <Group key={i} gap={half} wrap="nowrap">
            {i > 0 && (
              <Text span size="xs" c="dimmed" aria-hidden>
                →
              </Text>
            )}
            <StatusBadge status={s} />
          </Group>
        ))}
      </Group>
    );
  }
  if (e.verb === 'issue.updated') {
    const fields = changedFields(group);
    if (fields.length === 0) return null;
    return (
      <Text size="xs" c="dimmed">
        {fields
          .map((f) =>
            f.name
              ? f.name
              : f.key === 'sprint'
                ? t('agile.sprint')
                : t(`issues.${f.key}` as 'issues.summary', { defaultValue: f.key }),
          )
          .join(' · ')}
      </Text>
    );
  }
  if (e.verb === 'issue.moved' && e.fromKey) {
    return (
      <Text size="xs" c="dimmed">
        {t('activity.previously', { key: e.fromKey })}
      </Text>
    );
  }
  if (e.verb === 'pushed') {
    const commits = pushedCommits(group);
    if (!commits.count) return null;
    return (
      <Stack gap={half}>
        {commits.newest.map((c) => (
          <Group key={c.sha} gap="xs" wrap="nowrap" miw={0}>
            <Anchor component={Link} to={c.url} className="kogge-activity-sha" underline="hover">
              {c.sha.slice(0, 7)}
            </Anchor>
            <Text size="sm" truncate>
              {c.subject}
            </Text>
          </Group>
        ))}
        {commits.count > commits.newest.length && (
          <Anchor component={Link} to={e.url} size="xs" c="dimmed" underline="hover">
            {t('activity.moreCommits', { count: commits.count - commits.newest.length })}
          </Anchor>
        )}
      </Stack>
    );
  }
  const excerpt = group.find((x) => x.excerpt)?.excerpt;
  if (excerpt) {
    return (
      <Text size="sm" c="dimmed" lineClamp={ctx.compact ? 1 : 2} className="kogge-activity-quote">
        {excerpt}
      </Text>
    );
  }
  return null;
}

/** When a line happened: how long ago today, the time of day before. */
function When({ at, ctx }: { at: string; ctx: LineContext }) {
  const { i18n } = useTranslation();
  let text = formatRelative(at);
  if (dayKey(at, ctx.timeZone) !== ctx.today) {
    try {
      text = new Intl.DateTimeFormat(i18n.language === 'de' ? 'de-DE' : 'en-GB', {
        timeStyle: 'short',
        timeZone: ctx.timeZone,
      }).format(new Date(at));
    } catch {
      text = formatDateTime(at);
    }
  }
  return (
    <Text
      size="xs"
      c="dimmed"
      component="time"
      dateTime={at}
      title={formatDateTime(at)}
      style={{ whiteSpace: 'nowrap' }}
    >
      {text}
    </Text>
  );
}

/** The person, product or state in front of a line. */
function Lead({ e, ctx }: { e: Entry; ctx: LineContext }) {
  const size = leadSize(ctx);
  const deployment = e.verb.startsWith('deployment.');
  // On a person's own page lines show their product, or the state of a run or deployment, not the person.
  if (ctx.self && !impersonal(e) && !deployment) {
    const Logo = products[e.product as ProductId].Logo;
    return (
      <Box className="kogge-activity-lead" w={size} h={size} aria-hidden>
        <Logo size={ctx.compact ? 16 : 20} />
      </Box>
    );
  }
  if (impersonal(e) || (ctx.self && deployment)) {
    return (
      <Box className="kogge-activity-lead">
        <StateIcon state={e.verb.split('.')[1] ?? ''} size={size} />
      </Box>
    );
  }
  const name = e.actor?.displayName ?? '';
  return (
    <Box className="kogge-activity-lead">
      <Avatar size={size} radius="xl" color="initials" name={name || undefined} aria-hidden>
        {name ? initials(name) : null}
      </Avatar>
      {e.verb === 'deployment.succeeded' && (
        <span className="kogge-activity-dot">
          <StateIcon state="succeeded" size={16} />
        </span>
      )}
    </Box>
  );
}

function ActivityLine({ group, ctx }: { group: EntryGroup; ctx: LineContext }) {
  const { t } = useTranslation();
  const entries = group.entries;
  const e = entries[0];
  const product = products[e.product as ProductId];
  const runs = isRun(e) && entries.length > 1;
  const branches = isRun(e) ? [...new Set(entries.map((x) => x.detail).filter(Boolean))] : [];
  let sentence: ReactNode;
  if (e.verb === 'pushed') {
    sentence = <PushSentence group={entries} ctx={ctx} />;
  } else {
    sentence = (
      <>
        {verbPhrase(t, e, entries.length)}{' '}
        <Anchor component={Link} to={e.url} size={sentenceSize(ctx)} fw={500} underline="hover">
          {runs && e.pipeline ? e.pipeline : e.title}
        </Anchor>
      </>
    );
  }
  const actor =
    !ctx.self && !impersonal(e) ? (
      e.actor ? (
        <Anchor
          component={Link}
          to={`/people/${e.actor.username}`}
          size={sentenceSize(ctx)}
          fw={600}
          c="inherit"
          underline="hover"
        >
          {e.actor.displayName}
        </Anchor>
      ) : (
        <Text span size={sentenceSize(ctx)} fw={600}>
          {t('activity.someone')}
        </Text>
      )
    ) : null;
  // The container is named where the stream spans containers and the sentence does not name it already.
  const container = !ctx.inContainer && e.verb !== 'pushed';
  const numbers = runs ? `#${entries[entries.length - 1].number ?? ''}–#${e.number ?? ''}` : '';
  return (
    <div className="kogge-activity-entry">
      <Lead e={e} ctx={ctx} />
      <Stack gap={half} miw={0} style={{ flex: 1 }}>
        <Text size={sentenceSize(ctx)} className={ctx.self ? 'kogge-activity-self' : undefined}>
          {actor}
          {actor && ' '}
          {sentence}
        </Text>
        <Details group={entries} ctx={ctx} />
        <Group gap="xs" wrap="nowrap" miw={0}>
          {ctx.crossProduct && !ctx.self && (
            <Box aria-hidden display="flex">
              <product.Logo size={14} />
            </Box>
          )}
          {container && (
            <Anchor component={Link} to={e.container.url} size="xs" c="dimmed" underline="hover" truncate>
              {e.container.name}
            </Anchor>
          )}
          {numbers && (
            <Text size="xs" c="dimmed" style={{ whiteSpace: 'nowrap' }}>
              {t('activity.runs', { numbers })}
            </Text>
          )}
          {branches.length > 0 && (
            <Text size="xs" c="dimmed" truncate>
              {branches.join(', ')}
            </Text>
          )}
          <When at={e.at} ctx={ctx} />
        </Group>
      </Stack>
    </div>
  );
}

/** The heading of a day: today, yesterday, the weekday and date within the week, the full date before. */
function dayLabel(t: TFunction, day: string, ctx: LineContext, language: string) {
  const kind = dayKind(day, ctx.today);
  if (kind === 'today') return t('activity.today');
  if (kind === 'yesterday') return t('activity.yesterday');
  // The day is a calendar day already: it is formatted as such, at noon UTC.
  return new Intl.DateTimeFormat(language === 'de' ? 'de-DE' : 'en-GB', {
    weekday: 'long',
    day: 'numeric',
    month: 'long',
    year: kind === 'date' ? 'numeric' : undefined,
    timeZone: 'UTC',
  }).format(new Date(`${day}T12:00:00Z`));
}

/** Lines while the stream loads. */
function Placeholder({ ctx }: { ctx: LineContext }) {
  return (
    <Stack gap="sm" aria-hidden>
      {[0, 1, 2].map((i) => (
        <Group key={i} gap={ctx.compact ? 'xs' : 'sm'} wrap="nowrap" align="flex-start">
          <Skeleton circle height={leadSize(ctx)} />
          <Stack gap="xs" style={{ flex: 1 }}>
            <Skeleton height="var(--mantine-font-size-sm)" width={`${70 - i * 12}%`} />
            <Skeleton height="var(--mantine-font-size-xs)" width="30%" />
          </Stack>
        </Group>
      ))}
    </Stack>
  );
}

/**
 * An activity stream: what happened recently in what the user may see — issues, pages, pushes and
 * pull requests, runs and deployments — newest first, by day, with what each change was, a page at a time and
 * refreshed by the live updates. What the same person did to the same thing within an hour reads as one line, and
 * so does a series of runs of a pipeline.
 */
export function ActivityStream({
  project,
  space,
  repository,
  user,
  product: only,
  productFilter,
  limit = 20,
  density = 'comfortable',
}: ActivityStreamProps) {
  const { t, i18n } = useTranslation();
  const session = useOptionalSession();
  const timeZone = session?.me.user.timeZone || undefined;
  const compact = density === 'compact';
  const [chosen, setChosen] = useState('all');
  const [mine, setMine] = useState(false);
  const product = only ?? (chosen === 'all' ? undefined : (chosen as ProductId));
  const filter = {
    project,
    space,
    repository,
    user,
    product,
    mine: mine || undefined,
  };
  const stream = useInfiniteQuery({
    queryKey: ['activity', filter],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/activity-stream', {
          params: { query: { ...filter, before: pageParam || undefined, limit } },
        }),
      ),
    initialPageParam: '',
    getNextPageParam: (last) => last.next || undefined,
  });
  const entries = useMemo(() => stream.data?.pages.flatMap((p) => p.entries) ?? [], [stream.data]);
  const groups = useMemo(() => groupEntries(entries, timeZone), [entries, timeZone]);
  const inContainer = !!(project || space || repository);
  const ctx: LineContext = {
    inContainer,
    crossProduct: !inContainer && !only && chosen === 'all',
    self: !!user,
    compact,
    today: dayKey(new Date(), timeZone),
    timeZone,
  };
  const day = compact ? 'h4' : 'h3';
  return (
    <Stack gap="xs">
      {!compact && (
        <Group gap="sm" justify={productFilter ? 'space-between' : 'flex-start'}>
          {productFilter && (
            <SegmentedControl
              size="xs"
              aria-label={t('activity.product')}
              value={chosen}
              onChange={setChosen}
              data={[
                { value: 'all', label: t('activity.allProducts') },
                ...productList.map((p) => ({ value: p.id, label: p.name })),
              ]}
            />
          )}
          {!user && (
            <Switch
              size="sm"
              label={t('activity.mine')}
              checked={mine}
              onChange={(e) => setMine(e.currentTarget.checked)}
            />
          )}
        </Group>
      )}
      <ErrorAlert error={stream.error} />
      {stream.isPending && <Placeholder ctx={ctx} />}
      {stream.isSuccess && entries.length === 0 && (
        <Text size="sm" c="dimmed">
          {mine ? t('activity.noneMine') : t('activity.none')}
        </Text>
      )}
      <ul className="kogge-activity-stream" data-density={density} aria-label={t('activity.title')}>
        {groups.map((g, i) => {
          const first = i === 0 || groups[i - 1].day !== g.day;
          return (
            <li key={g.key} className="kogge-activity-item" data-day={first ? g.day : undefined}>
              {first && (
                <Text component={day} className="kogge-activity-day">
                  {dayLabel(t, g.day, ctx, i18n.language)}
                </Text>
              )}
              <ActivityLine group={g} ctx={ctx} />
            </li>
          );
        })}
      </ul>
      {stream.hasNextPage && (
        <Group>
          <Button
            variant="default"
            size="xs"
            onClick={() => stream.fetchNextPage()}
            loading={stream.isFetchingNextPage}
          >
            {t('activity.more')}
          </Button>
        </Group>
      )}
    </Stack>
  );
}
