// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Button,
  Checkbox,
  Chip,
  Group,
  Input,
  Modal,
  NumberInput,
  SegmentedControl,
  type SegmentedControlProps,
  Select,
  Stack,
  Text,
} from '@mantine/core';
import { TimeInput } from '@mantine/dates';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';
import { formatDateTime } from '../../app/format';
import { useSession } from '../../app/session';
import { ErrorAlert } from '../../components/ErrorAlert';
import { notifySuccess } from '../../components/notify';
import {
  hourWindow,
  hoursBetween,
  minuteOfTime,
  timeOfMinute,
  weekdayNames,
  type Subscription,
} from './schedule';

type Frequency = Subscription['frequency'];

const hourOptions = Array.from({ length: 24 }, (_, h) => ({
  value: String(h),
  label: `${String(h).padStart(2, '0')}:00`,
}));

/** A labelled choice of segments: a radio group named by its label. */
function Choice({ label, ...props }: { label: string } & SegmentedControlProps) {
  const id = useId();
  return (
    <Input.Wrapper label={label} labelElement="div" id={id}>
      <div>
        <SegmentedControl aria-labelledby={`${id}-label`} {...props} />
      </div>
    </Input.Wrapper>
  );
}

/** The fields of a schedule as the form edits them. */
function initial(s?: Subscription) {
  const window = s?.frequency === 'hourly' ? hourWindow(s.hours ?? []) : null;
  return {
    frequency: (s?.frequency ?? 'daily') as Frequency,
    time: timeOfMinute(s && s.frequency !== 'hourly' ? s.minute : 8 * 60),
    minute: s?.frequency === 'hourly' ? s.minute : 0,
    days: (s?.weekdays?.length ? s.weekdays : [1, 2, 3, 4, 5]).map(String),
    window: s?.frequency === 'hourly' ? window !== null : true,
    from: window ? window[0] : 8,
    until: window ? (window[1] + 1) % 24 : 18,
    monthDay: String(s?.monthDay || 1),
    sendEmpty: s?.sendEmpty ?? false,
  };
}

/**
 * Subscribes to a filter, or changes when a subscription sends. The filter's owner (and administrators) may
 * subscribe a group they belong to instead of themselves.
 */
export function SubscriptionForm({
  filter,
  subscription,
  onClose,
}: {
  filter: { id: number; name: string; canEdit?: boolean };
  subscription?: Subscription;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { me } = useSession();
  const client = useQueryClient();
  const [v, setV] = useState(() => initial(subscription));
  const set = (patch: Partial<typeof v>) => setV((cur) => ({ ...cur, ...patch }));
  const [recipient, setRecipient] = useState<'me' | 'group'>('me');
  const [groupId, setGroupId] = useState<string | null>(null);
  const daysLabel = useId();
  const canGroup = !subscription && !!filter.canEdit && me.groups.length > 0;
  const minuteOfDay = minuteOfTime(v.time);
  const names = weekdayNames('long');
  const short = weekdayNames();

  const schedule = () => {
    switch (v.frequency) {
      case 'hourly':
        return {
          frequency: v.frequency,
          minute: v.minute,
          weekdays: v.days.map(Number),
          hours: v.window ? hoursBetween(v.from, (v.until + 23) % 24) : [],
        };
      case 'daily':
        return { frequency: v.frequency, minute: minuteOfDay ?? 0, weekdays: v.days.map(Number) };
      default:
        return { frequency: v.frequency, minute: minuteOfDay ?? 0, monthDay: Number(v.monthDay) };
    }
  };
  const save = useMutation({
    mutationFn: () => {
      const body = { ...schedule(), sendEmpty: v.sendEmpty };
      if (subscription) {
        return unwrap(
          api.PUT('/api/v1/filter-subscriptions/{id}', { params: { path: { id: subscription.id } }, body }),
        );
      }
      return unwrap(
        api.POST('/api/v1/filters/{id}/subscriptions', {
          params: { path: { id: filter.id } },
          body: { ...body, groupId: recipient === 'group' && groupId ? Number(groupId) : undefined },
        }),
      );
    },
    onSuccess: (s) => {
      void client.invalidateQueries({ queryKey: ['filter-subscriptions'] });
      notifySuccess(
        subscription
          ? t('filters.subscription.updated')
          : t('filters.subscription.created', { when: formatDateTime(s.next) }),
      );
      onClose();
    },
  });
  const invalid =
    (v.frequency !== 'hourly' && minuteOfDay === null) ||
    (v.frequency !== 'monthly' && v.days.length === 0) ||
    (recipient === 'group' && !groupId);

  return (
    <Modal
      opened
      onClose={onClose}
      size="lg"
      title={
        subscription
          ? t('filters.subscription.editTitle', { name: filter.name })
          : t('filters.subscription.title', { name: filter.name })
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (!invalid) save.mutate();
        }}
      >
        <Stack>
          <Text size="sm" c="dimmed">
            {t('filters.subscription.intro')}
          </Text>
          {canGroup && (
            <Group align="flex-end" gap="sm">
              <Choice
                label={t('filters.subscription.recipient')}
                value={recipient}
                onChange={(r) => setRecipient(r as 'me' | 'group')}
                data={[
                  { value: 'me', label: t('filters.subscription.me') },
                  { value: 'group', label: t('filters.subscription.group') },
                ]}
              />
              {recipient === 'group' && (
                <Select
                  label={t('filters.subscription.groupPick')}
                  data={me.groups.map((g) => ({ value: String(g.id), label: g.name }))}
                  value={groupId}
                  onChange={setGroupId}
                  searchable
                  w={240}
                />
              )}
            </Group>
          )}
          {canGroup && recipient === 'group' && (
            <Text size="xs" c="dimmed" mt={-8}>
              {t('filters.subscription.groupHelp')}
            </Text>
          )}
          <Choice
            label={t('filters.subscription.frequency')}
            value={v.frequency}
            onChange={(f) => set({ frequency: f as Frequency })}
            data={(['hourly', 'daily', 'monthly'] as const).map((f) => ({
              value: f,
              label: t(`filters.subscription.${f}`),
            }))}
          />
          {v.frequency === 'monthly' ? (
            <Group align="flex-start">
              <Select
                label={t('filters.subscription.monthDay')}
                description={t('filters.subscription.monthDayHelp')}
                data={Array.from({ length: 31 }, (_, i) => String(i + 1))}
                value={v.monthDay}
                onChange={(d) => set({ monthDay: d ?? '1' })}
                allowDeselect={false}
                w={220}
              />
              <TimeInput
                label={t('filters.subscription.time')}
                value={v.time}
                onChange={(e) => set({ time: e.currentTarget.value })}
                w={140}
              />
            </Group>
          ) : v.frequency === 'daily' ? (
            <TimeInput
              label={t('filters.subscription.time')}
              value={v.time}
              onChange={(e) => set({ time: e.currentTarget.value })}
              w={140}
            />
          ) : (
            <>
              <NumberInput
                label={t('filters.subscription.minute')}
                value={v.minute}
                onChange={(m) => set({ minute: Math.min(59, Math.max(0, Number(m) || 0)) })}
                min={0}
                max={59}
                clampBehavior="strict"
                allowDecimal={false}
                w={180}
              />
              <Group align="flex-end" gap="sm">
                <Choice
                  label={t('filters.subscription.hours')}
                  value={v.window ? 'window' : 'all'}
                  onChange={(m) => set({ window: m === 'window' })}
                  data={[
                    { value: 'all', label: t('filters.subscription.hoursAll') },
                    { value: 'window', label: t('filters.subscription.hoursBetween') },
                  ]}
                />
                {v.window && (
                  <>
                    <Select
                      label={t('filters.subscription.from')}
                      data={hourOptions}
                      value={String(v.from)}
                      onChange={(h) => set({ from: Number(h ?? 0) })}
                      allowDeselect={false}
                      w={110}
                    />
                    <Select
                      label={t('filters.subscription.to')}
                      data={hourOptions}
                      value={String(v.until)}
                      onChange={(h) => set({ until: Number(h ?? 0) })}
                      allowDeselect={false}
                      w={110}
                    />
                  </>
                )}
              </Group>
            </>
          )}
          {v.frequency !== 'monthly' && (
            <Input.Wrapper label={t('filters.subscription.days')} labelElement="div" id={daysLabel}>
              <Chip.Group multiple value={v.days} onChange={(days) => set({ days })}>
                <Group gap={6} mt={4} role="group" aria-labelledby={`${daysLabel}-label`}>
                  {short.map((d, i) => (
                    <Chip key={i} value={String(i + 1)} size="sm" aria-label={names[i]}>
                      {d}
                    </Chip>
                  ))}
                </Group>
              </Chip.Group>
            </Input.Wrapper>
          )}
          <Checkbox
            label={t('filters.subscription.sendEmpty')}
            checked={v.sendEmpty}
            onChange={(e) => set({ sendEmpty: e.currentTarget.checked })}
          />
          <Text size="xs" c="dimmed">
            {t('filters.subscription.timeZone', { zone: subscription?.timeZone ?? me.timeZone })}
          </Text>
          <ErrorAlert error={save.error} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={save.isPending} disabled={invalid}>
              {subscription ? t('common.save') : t('filters.subscribe')}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
