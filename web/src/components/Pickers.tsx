// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { MultiSelect, Select, type MultiSelectProps, type SelectProps } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { api, unwrap } from '../api/client';
import type { paths } from '../api/schema';

type PickerProps = Omit<SelectProps, 'data' | 'value' | 'onChange'> & {
  value: number | null;
  /** Gets the chosen id and the label it was shown with. */
  onChange: (id: number | null, label?: string) => void;
};

/** Searches users by name, user name or e-mail; known gives the label of the user selected before. */
export function UserPicker({
  value,
  onChange,
  known,
  ...props
}: PickerProps & { known?: { id: number; label: string } }) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const users = useQuery({
    queryKey: ['users', 'search', debounced],
    queryFn: () => unwrap(api.GET('/api/v1/users', { params: { query: { query: debounced, limit: 20 } } })),
  });
  const data = (users.data?.items ?? []).map((u) => ({
    value: String(u.id),
    label: `${u.displayName} (${u.username})`,
  }));
  if (known && !data.some((d) => d.value === String(known.id))) {
    data.unshift({ value: String(known.id), label: known.label });
  }
  return (
    <Select
      searchable
      clearable
      data={data}
      value={value === null ? null : String(value)}
      onChange={(v, option) => onChange(v ? Number(v) : null, option?.label)}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

/** Chooses several users; known gives the labels of users selected before. */
export function UsersPicker({
  value,
  onChange,
  known = [],
  ...props
}: Omit<MultiSelectProps, 'data' | 'value' | 'onChange'> & {
  value: number[];
  onChange: (ids: number[]) => void;
  known?: { id: number; label: string }[];
}) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const [labels, setLabels] = useState<Record<string, string>>({});
  const users = useQuery({
    queryKey: ['users', 'search', debounced],
    queryFn: () => unwrap(api.GET('/api/v1/users', { params: { query: { query: debounced, limit: 20 } } })),
  });
  const all: Record<string, string> = { ...labels };
  for (const k of known) all[String(k.id)] = k.label;
  for (const u of users.data?.items ?? []) all[String(u.id)] = u.displayName;
  const data = Object.entries(all).map(([v, label]) => ({ value: v, label }));
  return (
    <MultiSelect
      searchable
      data={data}
      value={value.map(String)}
      onChange={(v) => {
        setLabels(all);
        onChange(v.map(Number));
      }}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

/** Chooses several groups; known gives the labels of groups selected before. */
export function GroupsPicker({
  value,
  onChange,
  known = [],
  ...props
}: Omit<MultiSelectProps, 'data' | 'value' | 'onChange'> & {
  value: number[];
  onChange: (ids: number[]) => void;
  known?: { id: number; label: string }[];
}) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const [labels, setLabels] = useState<Record<string, string>>({});
  const groups = useQuery({
    queryKey: ['groups', 'search', debounced],
    queryFn: () => unwrap(api.GET('/api/v1/groups', { params: { query: { query: debounced, limit: 20 } } })),
  });
  const all: Record<string, string> = { ...labels };
  for (const k of known) all[String(k.id)] = k.label;
  for (const g of groups.data?.items ?? []) all[String(g.id)] = g.name;
  const data = Object.entries(all).map(([v, label]) => ({ value: v, label }));
  return (
    <MultiSelect
      searchable
      data={data}
      value={value.map(String)}
      onChange={(v) => {
        setLabels(all);
        onChange(v.map(Number));
      }}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

/** Searches groups by name; known gives the label of the group selected before. */
export function GroupPicker({
  value,
  onChange,
  known,
  ...props
}: PickerProps & { known?: { id: number; label: string } }) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const groups = useQuery({
    queryKey: ['groups', 'search', debounced],
    queryFn: () => unwrap(api.GET('/api/v1/groups', { params: { query: { query: debounced, limit: 20 } } })),
  });
  const data = (groups.data?.items ?? []).map((g) => ({ value: String(g.id), label: g.name }));
  if (known && !data.some((d) => d.value === String(known.id))) {
    data.unshift({ value: String(known.id), label: known.label });
  }
  return (
    <Select
      searchable
      clearable
      data={data}
      value={value === null ? null : String(value)}
      onChange={(v, option) => onChange(v ? Number(v) : null, option?.label)}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

/** Searches wiki pages by title and text, optionally of one space and kind only. */
export function PagePicker({
  value,
  onChange,
  space,
  kind,
  ...props
}: PickerProps & { space?: string; kind?: 'page' | 'blog' }) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const current = useQuery({
    queryKey: ['page', value],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/pages/{id}', { params: { path: { id: value! } } })),
    enabled: !!value,
    retry: false,
  });
  // Within one space the titles say enough.
  const label = (p: { title: string; space: { key: string } }) =>
    space ? p.title : `${p.title} (${p.space.key})`;
  // The input shows the chosen page's title; that is no search: offer the other pages too.
  const query = current.data && debounced === label(current.data) ? '' : debounced;
  const results = useQuery({
    queryKey: ['page', 'search', query, space, kind],
    queryFn: () =>
      unwrap(api.GET('/api/v1/wiki/search', { params: { query: { q: query, space, kind, limit: 20 } } })),
  });
  const data = (results.data?.pages ?? []).map((p) => ({ value: String(p.id), label: label(p) }));
  if (value && current.data && !data.some((d) => d.value === String(value))) {
    data.unshift({ value: String(value), label: label(current.data) });
  }
  return (
    <Select
      searchable
      clearable
      data={data}
      value={value === null ? null : String(value)}
      onChange={(v, option) => onChange(v ? Number(v) : null, option?.label)}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

type SpacePermission = NonNullable<
  NonNullable<paths['/api/v1/wiki/spaces']['get']['parameters']['query']>['permission']
>;

type SpacePickerProps = Omit<SelectProps, 'data' | 'value' | 'onChange'> & {
  value: string | null;
  onChange: (key: string | null) => void;
  /** Offers only the spaces in which the user holds this permission, e.g. page.edit to write in them. */
  permission?: SpacePermission;
};

/** Searches the wiki spaces the user may see by name or key; the value is the key. */
export function SpacePicker({ value, onChange, permission, ...props }: SpacePickerProps) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const spaces = useQuery({
    queryKey: ['space', 'list', 'picker', debounced, permission],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/wiki/spaces', { params: { query: { query: debounced, permission, limit: 30 } } }),
      ),
  });
  // The chosen space keeps its name while the search shows others.
  const chosen = useQuery({
    queryKey: ['space', value],
    queryFn: () => unwrap(api.GET('/api/v1/wiki/spaces/{key}', { params: { path: { key: value! } } })),
    enabled: !!value,
    retry: false,
  });
  const data = (spaces.data?.items ?? []).map((s) => ({ value: s.key, label: `${s.name} (${s.key})` }));
  if (value && !data.some((d) => d.value === value)) {
    data.unshift({ value, label: chosen.data ? `${chosen.data.name} (${chosen.data.key})` : value });
  }
  return (
    <Select
      searchable
      clearable
      data={data}
      value={value}
      onChange={onChange}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

type RepositoryPermission = NonNullable<
  NonNullable<paths['/api/v1/code/repos']['get']['parameters']['query']>['permission']
>;

/** Searches the repositories the user can read by name, slug or project key; the value is KEY/slug. */
export function RepositoryPicker({
  value,
  onChange,
  permission,
  active = false,
  ...props
}: Omit<SelectProps, 'data' | 'value' | 'onChange'> & {
  value: string | null;
  onChange: (v: string | null) => void;
  /** Offers only the repositories the user may access at this level, e.g. write to push to them. */
  permission?: RepositoryPermission;
  /** Leaves out archived repositories. */
  active?: boolean;
}) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const [picked, setPicked] = useState<{ value: string; label: string } | null>(null);
  // The chosen repository keeps its name while the search shows others.
  const parts = value?.split('/') ?? [];
  const [key, slug] = parts.length === 2 ? parts : [];
  const chosen = useQuery({
    queryKey: ['repository', 'path', key?.toUpperCase(), slug],
    queryFn: () =>
      unwrap(api.GET('/api/v1/code/repos/{key}/{slug}', { params: { path: { key: key!, slug: slug! } } })),
    enabled: !!key && !!slug && picked?.value !== value,
    retry: false,
  });
  const label = (r: { project: { key: string }; name: string }) => `${r.project.key} / ${r.name}`;
  const chosenLabel = picked?.value === value ? picked.label : chosen.data ? label(chosen.data) : undefined;
  // The input shows the chosen repository's name; that is no search: offer the others too.
  const query = chosenLabel !== undefined && debounced === chosenLabel ? '' : debounced;
  const repos = useQuery({
    queryKey: ['repository', 'list', 'picker', query, permission, active],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/repos', {
          params: { query: { query, permission, archived: active ? 'false' : undefined, limit: 30 } },
        }),
      ),
  });
  const data = (repos.data?.items ?? []).map((r) => ({
    value: `${r.project.key}/${r.slug}`,
    label: label(r),
  }));
  if (value && !data.some((d) => d.value === value)) data.unshift({ value, label: chosenLabel ?? value });
  return (
    <Select
      searchable
      clearable
      data={data}
      value={value}
      onChange={(v, option) => {
        setPicked(v ? { value: v, label: option.label } : null);
        onChange(v);
      }}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}

/**
 * Searches the code projects the user can see by key or name; the value is the key. admin offers only those the
 * user administers: the projects to create repositories in or to move them to.
 */
export function CodeProjectPicker({
  value,
  onChange,
  admin = false,
  ...props
}: Omit<SelectProps, 'data' | 'value' | 'onChange'> & {
  value: string | null;
  onChange: (key: string | null) => void;
  admin?: boolean;
}) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 200);
  const [picked, setPicked] = useState<{ value: string; label: string } | null>(null);
  // The chosen project keeps its name while the search shows others.
  const chosen = useQuery({
    queryKey: ['code-project', value?.toUpperCase()],
    queryFn: () => unwrap(api.GET('/api/v1/code/projects/{key}', { params: { path: { key: value! } } })),
    enabled: !!value && picked?.value !== value,
    retry: false,
  });
  const label = (p: { key: string; name: string }) => `${p.name} (${p.key})`;
  const chosenLabel = picked?.value === value ? picked.label : chosen.data ? label(chosen.data) : undefined;
  // The input shows the chosen project's name; that is no search: offer the others too.
  const query = chosenLabel !== undefined && debounced === chosenLabel ? '' : debounced;
  const projects = useQuery({
    queryKey: ['code-project', 'list', 'picker', query, admin],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/code/projects', {
          params: { query: { query, admin: admin || undefined, limit: 30 } },
        }),
      ),
  });
  const data = (projects.data?.items ?? []).map((p) => ({ value: p.key, label: label(p) }));
  if (value && !data.some((d) => d.value === value)) data.unshift({ value, label: chosenLabel ?? value });
  return (
    <Select
      searchable
      clearable
      data={data}
      value={value}
      onChange={(v, option) => {
        setPicked(v ? { value: v, label: option.label } : null);
        onChange(v);
      }}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options }) => options}
      nothingFoundMessage="—"
      {...props}
    />
  );
}
