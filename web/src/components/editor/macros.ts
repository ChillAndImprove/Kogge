// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/** Parameters of macros and how the insert dialog asks for them. */
export type MacroParamType =
  'text' | 'number' | 'kql' | 'page' | 'space' | 'label' | 'issue' | 'select' | 'code' | 'repository';

export interface MacroParam {
  key: string;
  type: MacroParamType;
  options?: string[];
  required?: boolean;
  default?: string | number;
}

export interface MacroDef {
  name: string;
  /** Has a body of blocks (e.g. excerpt). */
  body?: boolean;
  params: MacroParam[];
}

export const macroDefs: MacroDef[] = [
  { name: 'toc', params: [{ key: 'maxLevel', type: 'number', default: 3 }] },
  { name: 'children', params: [{ key: 'depth', type: 'number', default: 1 }] },
  { name: 'page-tree', params: [{ key: 'root', type: 'page' }] },
  { name: 'include', params: [{ key: 'page', type: 'page', required: true }] },
  { name: 'excerpt', body: true, params: [] },
  { name: 'excerpt-include', params: [{ key: 'page', type: 'page', required: true }] },
  {
    name: 'recently-updated',
    params: [
      { key: 'space', type: 'space' },
      { key: 'max', type: 'number', default: 10 },
    ],
  },
  {
    name: 'content-by-label',
    params: [
      { key: 'label', type: 'label', required: true },
      { key: 'space', type: 'space' },
      { key: 'max', type: 'number', default: 20 },
    ],
  },
  { name: 'attachments', params: [] },
  {
    name: 'issue',
    params: [
      { key: 'key', type: 'issue', required: true },
      { key: 'display', type: 'select', options: ['inline', 'card'], default: 'card' },
    ],
  },
  {
    name: 'issue-list',
    params: [
      { key: 'query', type: 'kql', required: true },
      { key: 'max', type: 'number', default: 20 },
    ],
  },
  { name: 'issue-count', params: [{ key: 'query', type: 'kql', required: true }] },
  { name: 'anchor', params: [{ key: 'name', type: 'text', required: true }] },
  { name: 'mermaid', params: [{ key: 'code', type: 'code', required: true }] },
  {
    name: 'build-status',
    params: [
      { key: 'repository', type: 'repository', required: true },
      { key: 'pipeline', type: 'text' },
      { key: 'branch', type: 'text' },
    ],
  },
  {
    name: 'code-snippet',
    params: [
      { key: 'repository', type: 'repository', required: true },
      { key: 'path', type: 'text', required: true },
      { key: 'ref', type: 'text' },
      { key: 'lines', type: 'text' },
    ],
  },
  {
    name: 'readme',
    params: [
      { key: 'repository', type: 'repository', required: true },
      { key: 'ref', type: 'text' },
    ],
  },
];

export function macroDef(name: string): MacroDef | undefined {
  return macroDefs.find((m) => m.name === name);
}

/** A short description of the parameters for the editor's macro box. */
export function paramSummary(params: Record<string, unknown> | null | undefined): string {
  return Object.entries(params ?? {})
    .filter(([, v]) => v !== '' && v !== null && v !== undefined)
    .map(([k, v]) => `${k}: ${typeof v === 'object' ? JSON.stringify(v) : String(v)}`)
    .join(' · ');
}
