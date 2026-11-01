// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { getSchema } from '@tiptap/core';
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { editorExtensions } from './extensions';

/**
 * The editor's schema as the server applies changes to shared drafts with it (internal/docmodel/prosemirror):
 * node and mark types in the editor's order, their content expressions, groups, allowed marks and attributes.
 */
export function schemaSpec() {
  const schema = getSchema(editorExtensions(''));
  const attrs = (spec: Record<string, { default?: unknown }> | undefined) =>
    Object.fromEntries(
      Object.entries(spec ?? {}).map(([name, a]) => [
        name,
        'default' in a ? { default: a.default ?? null } : {},
      ]),
    );
  return {
    topNode: schema.topNodeType.name,
    nodes: Object.values(schema.nodes).map((t) => ({
      name: t.name,
      content: t.spec.content ?? '',
      group: t.spec.group ?? '',
      inline: !!t.spec.inline,
      atom: !!t.spec.atom,
      marks: t.spec.marks ?? null,
      attrs: attrs(t.spec.attrs as Record<string, { default?: unknown }>),
    })),
    marks: Object.values(schema.marks).map((t) => ({
      name: t.name,
      excludes: t.spec.excludes ?? null,
      attrs: attrs(t.spec.attrs as Record<string, { default?: unknown }>),
    })),
  };
}

const file = resolve(__dirname, '../../../../internal/docmodel/prosemirror/schema.json');

describe('the schema of shared drafts', () => {
  it('is the editor schema (KOGGE_UPDATE_SCHEMA=1 writes it)', () => {
    const spec = JSON.stringify(schemaSpec(), null, 2) + '\n';
    if (process.env.KOGGE_UPDATE_SCHEMA) writeFileSync(file, spec);
    expect(readFileSync(file, 'utf8')).toBe(spec);
  });
});
