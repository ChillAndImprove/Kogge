// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  Alert,
  Anchor,
  Button,
  Checkbox,
  Collapse,
  Group,
  Loader,
  Modal,
  Radio,
  SegmentedControl,
  Select,
  Stack,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { keepPreviousData, useMutation, useQuery } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { api, unwrap, type Schemas } from '../../api/client';
import { UserPicker } from '../../components/Pickers';
import { Editor, isEmptyDoc, type Doc } from '../../components/editor/Editor';
import { ErrorAlert } from '../../components/ErrorAlert';
import {
  bulkActions,
  bulkEditInput,
  emptyEdit,
  modeFields,
  selectionOf,
  type BulkAction,
  type BulkTarget,
  type EditState,
  type Mode,
} from './bulkInput';
import { FieldInput, useFieldContext, useFieldLabel, type FieldContext } from './fields';
import {
  MovePlanView,
  TargetProjectSelect,
  chosenInput,
  moveInput,
  noChoices,
  planReady,
  type MoveChoices,
} from './MoveIssue';
import { useIssueConfig } from './shared';

type BulkCheck = Schemas['BulkCheck'];
type BulkField = Schemas['BulkField'];

/** The issues that will be changed and those that will be skipped, with why. */
function Chosen({ check }: { check: BulkCheck }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const skipped = check.issues.filter((i) => !i.permitted);
  const permitted = check.issues.length - skipped.length;
  return (
    <Stack gap={4}>
      <Text size="sm" data-testid="bulk-count">
        {t('bulk.permitted', { count: permitted, total: check.issues.length })}
      </Text>
      {check.truncated && (
        <Text size="xs" c="orange">
          {t('bulk.truncated', { total: check.total })}
        </Text>
      )}
      {skipped.length > 0 && (
        <>
          <Anchor component="button" type="button" size="xs" onClick={() => setOpen((o) => !o)} ta="left">
            {open ? t('bulk.hideSkipped') : t('bulk.showSkipped', { count: skipped.length })}
          </Anchor>
          <Collapse expanded={open}>
            <Stack gap={2}>
              {skipped.map((i) => (
                <Text key={i.key} size="xs">
                  <b>{i.key}</b> {i.reason}
                </Text>
              ))}
            </Stack>
          </Collapse>
        </>
      )}
    </Stack>
  );
}

/** The issues a field does not apply to, briefly. */
function missingText(t: TFunction, f: BulkField): string {
  const shown = f.missing.slice(0, 3).map((m) => `${m.key}: ${m.reason}`);
  const more = f.missing.length - shown.length;
  return [...shown, ...(more > 0 ? [t('bulk.more', { count: more })] : [])].join(' · ');
}

/** The fields of a bulk edit: those that apply to every issue can be chosen and given a value. */
function EditFields({
  check,
  state,
  onState,
  ctx,
  singleProject,
}: {
  check: BulkCheck;
  state: EditState;
  onState: (s: EditState) => void;
  ctx: FieldContext;
  singleProject: boolean;
}) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const label = useFieldLabel();
  return (
    <Stack gap="xs">
      {check.fields.map((f) => {
        const def = config.data?.fields.find((d) => d.key === f.key);
        const name = def ? label(def) : f.name;
        const on = !!state.on[f.key];
        const box = (
          <Checkbox
            label={name}
            checked={on}
            disabled={!f.applies}
            onChange={(e) => onState({ ...state, on: { ...state.on, [f.key]: e.currentTarget.checked } })}
          />
        );
        return (
          <Stack key={f.key} gap={4} data-testid={`bulk-field-${f.key}`}>
            {f.applies ? (
              box
            ) : (
              <Tooltip label={missingText(t, f)} multiline w={360} withinPortal>
                <div>{box}</div>
              </Tooltip>
            )}
            {!f.applies && f.missing.length > 0 && (
              <Text size="xs" c="dimmed" pl={30}>
                {t('bulk.notFor', { count: f.missing.length })}: {missingText(t, f)}
              </Text>
            )}
            {on && def && (
              <Stack gap={4} pl={30}>
                {(modeFields as readonly string[]).includes(f.key) && (
                  <SegmentedControl
                    size="xs"
                    w="fit-content"
                    aria-label={t('bulk.mode', { field: name })}
                    data={(['add', 'remove', 'replace'] as Mode[]).map((m) => ({
                      value: m,
                      label: t(`bulk.modes.${m}`),
                    }))}
                    value={state.modes[f.key] ?? 'add'}
                    onChange={(v) => onState({ ...state, modes: { ...state.modes, [f.key]: v as Mode } })}
                  />
                )}
                {f.key === 'assignee' && !singleProject ? (
                  <UserPicker
                    aria-label={name}
                    value={(state.values[f.key] as number | null) ?? null}
                    onChange={(v) => onState({ ...state, values: { ...state.values, [f.key]: v } })}
                    placeholder={t('issues.unassigned')}
                  />
                ) : (
                  <FieldInput
                    def={def}
                    value={state.values[f.key] ?? null}
                    onChange={(v) => onState({ ...state, values: { ...state.values, [f.key]: v } })}
                    ctx={ctx}
                  />
                )}
              </Stack>
            )}
          </Stack>
        );
      })}
    </Stack>
  );
}

/** The status issues go to, with the values their transitions ask for. */
function TransitionFields({
  check,
  status,
  onStatus,
  values,
  onValues,
  comment,
  onComment,
  ctx,
}: {
  check: BulkCheck;
  status: number | null;
  onStatus: (s: number | null) => void;
  values: Record<string, unknown>;
  onValues: (v: Record<string, unknown>) => void;
  comment: Doc | null;
  onComment: (d: Doc | null) => void;
  ctx: FieldContext;
}) {
  const { t } = useTranslation();
  const config = useIssueConfig();
  const label = useFieldLabel();
  const permitted = check.issues.filter((i) => i.permitted).length;
  const st = check.statuses.find((s) => s.status.id === status);
  if (check.statuses.length === 0) return <Alert color="gray">{t('bulk.noStatuses')}</Alert>;
  return (
    <Stack gap="sm">
      <Select
        label={t('bulk.toStatus')}
        data={check.statuses.map((s) => ({
          value: String(s.status.id),
          label: `${s.status.name} (${t('issues.issueCount', { count: s.issues.length })})`,
        }))}
        value={status == null ? null : String(status)}
        onChange={(v) => onStatus(v ? Number(v) : null)}
      />
      {st && st.issues.length < permitted && (
        <Text size="xs" c="dimmed">
          {t('bulk.noTransition', { count: permitted - st.issues.length, status: st.status.name })}
        </Text>
      )}
      {st?.screen.map((key) => {
        const set = (v: unknown) => onValues({ ...values, [key]: v });
        if (key === 'resolution') {
          return (
            <Select
              key={key}
              label={t('issues.resolution')}
              data={(config.data?.resolutions ?? []).map((r) => ({ value: String(r.id), label: r.name }))}
              value={values.resolution != null ? String(values.resolution) : null}
              onChange={(v) => set(v ? Number(v) : null)}
            />
          );
        }
        const def = config.data?.fields.find((f) => f.key === key);
        if (!def) return null;
        return (
          <FieldInput
            key={key}
            def={def}
            value={values[key] ?? null}
            onChange={set}
            ctx={ctx}
            label={label(def)}
          />
        );
      })}
      {st && (
        <Stack gap={4}>
          <Text size="sm" fw={550}>
            {t('issues.comment')}
            {st.commentRequired && (
              <Text span c="red">
                {' '}
                *
              </Text>
            )}
          </Text>
          <Editor value={comment} onChange={onComment} placeholder={t('bulk.commentHelp')} minHeight={70} />
        </Stack>
      )}
    </Stack>
  );
}

/**
 * The bulk change of chosen issues: what to do (edit, transition, move, watch, stop watching, delete), what that
 * does to each issue as the server sees it now, and the start of the job (its progress shows on its own page).
 */
export function BulkChangeModal({ target, onClose }: { target: BulkTarget; onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [step, setStep] = useState<'action' | 'details'>('action');
  const [action, setAction] = useState<BulkAction>('edit');
  const [edit, setEdit] = useState<EditState>(emptyEdit);
  const [status, setStatus] = useState<number | null>(null);
  const [screen, setScreen] = useState<Record<string, unknown>>({});
  const [comment, setComment] = useState<Doc | null>(null);
  const [project, setProject] = useState<string | null>(null);
  const [choices, setChoices] = useState<MoveChoices>(noChoices);
  const [confirm, setConfirm] = useState('');
  const [notify, setNotify] = useState(true);
  const selection = selectionOf(target);
  const check = useQuery({
    queryKey: ['bulk-check', action, selection, project, choices],
    queryFn: () =>
      unwrap(
        api.POST('/api/v1/issues/bulk/check', {
          body: {
            ...selection,
            action,
            move: action === 'move' && project ? chosenInput(project, choices) : undefined,
          },
        }),
      ),
    enabled: step === 'details' && (action !== 'move' || !!project),
    placeholderData: keepPreviousData,
    retry: false,
  });
  const data = check.data;
  const projects = new Set(
    (data?.issues ?? []).filter((i) => i.permitted && i.project).map((i) => i.project!.key),
  );
  const singleProject = projects.size === 1 ? [...projects][0] : undefined;
  const ctx = useFieldContext(singleProject, 0);
  const permitted = (data?.issues ?? []).filter((i) => i.permitted).length;
  const plan = action === 'move' && data?.move && data.move.project.key === project ? data.move : undefined;

  const body = (): Schemas['BulkInput'] => {
    const b: Schemas['BulkInput'] = { ...selection, action, notify: data?.canMute ? notify : undefined };
    switch (action) {
      case 'edit':
        b.edit = bulkEditInput(edit);
        break;
      case 'transition': {
        const st = data?.statuses.find((s) => s.status.id === status);
        const fields: Record<string, unknown> = {};
        for (const key of st?.screen ?? []) if (key in screen) fields[key] = screen[key];
        b.transition = {
          status: status!,
          fields: Object.keys(fields).length ? (fields as Record<string, never>) : undefined,
          comment: comment && !isEmptyDoc(comment) ? (comment as Schemas['Doc']) : undefined,
        };
        break;
      }
      case 'move':
        b.move = moveInput(project!, plan, choices);
        break;
      case 'delete':
        b.confirm = Number(confirm);
        break;
    }
    return b;
  };
  const start = useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/issues/bulk', { body: body() })),
    onSuccess: (b) => {
      onClose();
      navigate(`/issues/bulk/${b.id}`);
    },
  });
  const st = data?.statuses.find((s) => s.status.id === status);
  const ready =
    !!data &&
    permitted > 0 &&
    !check.isFetching &&
    (action !== 'edit' || !!bulkEditInput(edit)) &&
    (action !== 'transition' || (!!st && (!st.commentRequired || (!!comment && !isEmptyDoc(comment))))) &&
    (action !== 'move' || planReady(plan)) &&
    (action !== 'delete' || Number(confirm) === permitted);

  return (
    <Modal opened onClose={onClose} size="xl" title={t('bulk.title', { count: target.count })}>
      {step === 'action' ? (
        <Stack>
          <Radio.Group label={t('bulk.what')} value={action} onChange={(v) => setAction(v as BulkAction)}>
            <Stack gap="xs" mt="xs">
              {bulkActions.map((a) => (
                <Radio
                  key={a}
                  value={a}
                  label={t(`bulk.actions.${a}`)}
                  description={t(`bulk.actionHelp.${a}`)}
                />
              ))}
            </Stack>
          </Radio.Group>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => setStep('details')}>{t('bulk.next')}</Button>
          </Group>
        </Stack>
      ) : (
        <Stack>
          <Text fw={600}>{t(`bulk.actions.${action}`)}</Text>
          {action === 'move' && (
            <TargetProjectSelect
              value={project}
              onChange={(v) => {
                setProject(v);
                setChoices(noChoices);
              }}
            />
          )}
          {check.isPending && check.fetchStatus !== 'idle' && <Loader size="sm" />}
          <ErrorAlert error={check.error} />
          {data && <Chosen check={data} />}
          {data && action === 'edit' && (
            <EditFields
              check={data}
              state={edit}
              onState={setEdit}
              ctx={ctx}
              singleProject={!!singleProject}
            />
          )}
          {data && action === 'transition' && (
            <TransitionFields
              check={data}
              status={status}
              onStatus={setStatus}
              values={screen}
              onValues={setScreen}
              comment={comment}
              onComment={setComment}
              ctx={ctx}
            />
          )}
          {plan && <MovePlanView plan={plan} choices={choices} onChoices={setChoices} />}
          {data && action === 'delete' && (
            <Stack gap="xs">
              <Alert color="red" variant="light">
                {t('bulk.deleteWarning', { count: permitted })}
              </Alert>
              <TextInput
                label={t('bulk.confirmDelete', { count: permitted })}
                value={confirm}
                onChange={(e) => setConfirm(e.currentTarget.value)}
                inputMode="numeric"
              />
            </Stack>
          )}
          {data?.canMute && action !== 'delete' && action !== 'watch' && action !== 'unwatch' && (
            <Checkbox
              label={t('bulk.notify')}
              description={t('bulk.notifyHelp')}
              checked={notify}
              onChange={(e) => setNotify(e.currentTarget.checked)}
            />
          )}
          <ErrorAlert error={start.error} />
          <Group justify="space-between">
            <Button variant="subtle" onClick={() => setStep('action')}>
              {t('bulk.back')}
            </Button>
            <Group>
              <Button variant="default" onClick={onClose}>
                {t('common.cancel')}
              </Button>
              <Button
                color={action === 'delete' ? 'red' : undefined}
                onClick={() => start.mutate()}
                loading={start.isPending}
                disabled={!ready}
              >
                {t(`bulk.start.${action}`, { count: permitted })}
              </Button>
            </Group>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
