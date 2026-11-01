// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { formatEstimate } from '../agile/hooks';
import { fillTemplate, templateVariables } from './CreatePageDialog';
import { pagePath, slug } from './hooks';

const template = {
  type: 'doc',
  content: [
    { type: 'paragraph', content: [{ type: 'text', text: 'Customer: {{customer}}' }] },
    {
      type: 'paragraph',
      content: [
        { type: 'text', text: '{{date}}' },
        { type: 'text', text: ' and {{ customer }}' },
      ],
    },
  ],
};

describe('templates', () => {
  it('finds variables in order without duplicates', () => {
    expect(templateVariables(template)).toEqual(['customer', 'date']);
  });
  it('fills variables and drops text nodes that become empty', () => {
    const filled = fillTemplate(template, { customer: 'Stadtwerke' });
    expect(filled.content?.[0].content?.[0].text).toBe('Customer: Stadtwerke');
    expect(filled.content?.[1].content).toEqual([{ type: 'text', text: ' and Stadtwerke' }]);
    expect(templateVariables(template)).toEqual(['customer', 'date']); // the template is unchanged
  });
});

describe('page paths', () => {
  it('builds slugs with German umlauts', () => {
    expect(slug('Übersicht für Größe & Maße')).toBe('uebersicht-fuer-groesse-masse');
    expect(pagePath('DOCS', 7, 'Install guide')).toBe('/wiki/spaces/DOCS/pages/7/install-guide');
  });
});

describe('estimates', () => {
  it('formats story points and durations', () => {
    expect(formatEstimate(5, { type: 'field', field: 'cf_1' })).toBe('5');
    expect(formatEstimate(5400, { type: 'originalEstimate' })).toBe('1.5h');
    expect(formatEstimate(1800, { type: 'originalEstimate' })).toBe('30m');
    expect(formatEstimate(undefined, { type: 'count' })).toBe('');
  });
});
