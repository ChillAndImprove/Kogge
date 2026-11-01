// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import {
  apply,
  contextAt,
  findField,
  quote,
  staticSuggestions,
  tokenize,
  valueField,
  valueSuggestion,
  type Catalog,
} from './kqlComplete';

const catalog: Catalog = {
  fields: [
    {
      name: 'project',
      aliases: [],
      custom: false,
      kind: 'project',
      operators: ['=', '!=', 'in', 'not in'],
      history: false,
      orderable: true,
      values: true,
    },
    {
      name: 'status',
      aliases: [],
      custom: false,
      kind: 'status',
      operators: ['=', '!=', 'in', 'not in'],
      history: true,
      orderable: true,
      values: true,
    },
    {
      name: 'assignee',
      aliases: [],
      custom: false,
      kind: 'user',
      operators: ['=', '!=', 'in', 'not in', 'is', 'is not'],
      history: true,
      orderable: true,
      values: true,
    },
    {
      name: 'issuetype',
      aliases: ['type'],
      custom: false,
      kind: 'issuetype',
      operators: ['=', '!=', 'in', 'not in'],
      history: false,
      orderable: true,
      values: true,
    },
    {
      name: 'summary',
      aliases: [],
      custom: false,
      kind: 'text',
      operators: ['~', '!~'],
      history: false,
      orderable: true,
      values: false,
    },
    {
      name: 'created',
      aliases: ['createdDate'],
      custom: false,
      kind: 'datetime',
      operators: ['=', '!=', '>', '>=', '<', '<=', 'is', 'is not'],
      history: false,
      orderable: true,
      values: false,
    },
    {
      name: 'labels',
      aliases: ['label'],
      custom: false,
      kind: 'labels',
      operators: ['=', '!=', 'in', 'not in', 'is', 'is not'],
      history: false,
      orderable: false,
      values: true,
    },
    {
      name: 'Story Points',
      aliases: [],
      custom: true,
      id: 10016,
      kind: 'number',
      operators: ['=', '!=', '>', '>=', '<', '<=', 'is', 'is not'],
      history: false,
      orderable: true,
      values: false,
    },
  ],
  functions: [
    { name: 'currentUser', kinds: ['user'], list: false },
    { name: 'membersOf', arg: 'group', kinds: ['user'], list: true },
    { name: 'startOfWeek', arg: 'increment', kinds: ['date', 'datetime'], list: false },
    { name: 'openSprints', kinds: ['sprint'], list: true },
  ],
  keywords: ['AND', 'OR', 'NOT', 'ORDER BY'],
};

/** The context at the end of a query, or at the | in it. */
function at(q: string) {
  const caret = q.includes('|') ? q.indexOf('|') : q.length;
  return contextAt(q.replace('|', ''), caret);
}

const labels = (q: string) => staticSuggestions(catalog, at(q)).map((s) => s.label);

describe('the KQL tokenizer', () => {
  it('splits a query as the server does', () => {
    expect(
      tokenize('project = SHOP AND "Story Points" >= 3 OR cf[10016] in (1, 2)').map((t) => [t.kind, t.text]),
    ).toEqual([
      ['word', 'project'],
      ['op', '='],
      ['word', 'SHOP'],
      ['word', 'AND'],
      ['string', 'Story Points'],
      ['op', '>='],
      ['word', '3'],
      ['word', 'OR'],
      ['custom', '10016'],
      ['word', 'in'],
      ['lparen', '('],
      ['word', '1'],
      ['comma', ','],
      ['word', '2'],
      ['rparen', ')'],
    ]);
  });

  it('knows the other spellings of operators and keywords', () => {
    expect(tokenize('a!=b && !c || d!~"x"').map((t) => t.text)).toEqual([
      'a',
      '!=',
      'b',
      'and',
      'not',
      'c',
      'or',
      'd',
      '!~',
      'x',
    ]);
  });

  it('reads escapes and open strings', () => {
    const [s] = tokenize('"say \\"hi\\"');
    expect(s).toMatchObject({ kind: 'string', text: 'say "hi"', open: true });
    expect(tokenize("'it\\'s'")[0]).toMatchObject({ text: "it's", open: false });
  });

  it('takes issue keys, dates and relative dates as words', () => {
    expect(tokenize('key = SHOP-12 AND created >= -2w AND due < 2026/10/06').map((t) => t.text)).toEqual([
      'key',
      '=',
      'SHOP-12',
      'AND',
      'created',
      '>=',
      '-2w',
      'AND',
      'due',
      '<',
      '2026/10/06',
    ]);
  });
});

describe('the place of the caret', () => {
  it('expects a field at the start and after AND, OR, NOT and (', () => {
    expect(at('').expect).toEqual({ kind: 'field', start: true });
    expect(at('pro')).toMatchObject({ expect: { kind: 'field' }, prefix: 'pro', from: 0, to: 3 });
    expect(at('project = SHOP AND ').expect).toEqual({ kind: 'field', start: false });
    expect(at('NOT (sta').expect.kind).toBe('field');
    expect(at('status = Done OR (assignee = x AND ').expect.kind).toBe('field');
  });

  it('expects an operator after a field', () => {
    expect(at('assignee ').expect).toEqual({ kind: 'operator', field: 'assignee' });
    expect(at('"Story Points" ').expect).toEqual({ kind: 'operator', field: 'Story Points' });
    expect(at('cf[10016] ').expect).toEqual({ kind: 'operator', field: 'cf[10016]' });
    expect(at('assignee i')).toMatchObject({ expect: { kind: 'operator' }, prefix: 'i' });
  });

  it('expects a value after an operator, in lists, and arguments in functions', () => {
    expect(at('project = ').expect).toEqual({
      kind: 'value',
      field: 'project',
      operator: '=',
      inList: false,
    });
    expect(at('project = SH')).toMatchObject({
      expect: { kind: 'value', field: 'project' },
      prefix: 'SH',
      from: 10,
    });
    expect(at('project in ').expect).toEqual({ kind: 'list', field: 'project', operator: 'in' });
    expect(at('project not in (SHOP, ').expect).toEqual({
      kind: 'value',
      field: 'project',
      operator: 'not in',
      inList: true,
    });
    expect(at('assignee is ').expect).toEqual({ kind: 'empty' });
    expect(at('assignee in membersOf("').expect).toEqual({ kind: 'argument', fn: 'membersOf' });
    expect(at('status = "In Pro')).toMatchObject({ expect: { kind: 'value' }, prefix: 'In Pro', from: 9 });
  });

  it('expects AND, OR and ORDER BY after a clause', () => {
    expect(at('project = SHOP ').expect).toEqual({ kind: 'logical', depth: 0, ordered: false });
    expect(at('(project = SHOP ').expect).toEqual({ kind: 'logical', depth: 1, ordered: false });
    expect(at('assignee in (a, b) ').expect.kind).toBe('logical');
    expect(at('assignee = currentUser() ').expect.kind).toBe('logical');
    expect(at('summary ~ "two words" ').expect.kind).toBe('logical');
    expect(at('assignee is not EMPTY ').expect.kind).toBe('logical');
  });

  it('follows WAS and CHANGED with their predicates', () => {
    expect(at('status was ').expect).toMatchObject({ kind: 'value', field: 'status', operator: 'was' });
    expect(at('status was not in (Open) ').expect).toMatchObject({ kind: 'logical', history: 'was' });
    expect(at('status changed ').expect).toMatchObject({ kind: 'logical', history: 'changed' });
    expect(at('status changed from ').expect).toMatchObject({
      kind: 'value',
      field: 'status',
      operator: 'from',
    });
    expect(at('status changed by ').expect).toMatchObject({ kind: 'value', field: 'assignee' });
    expect(at('status was Open after ').expect).toMatchObject({ kind: 'value', field: 'updated' });
    expect(at('status was Open after -1w ').expect).toMatchObject({ kind: 'logical', history: 'was' });
    expect(at('status was Open during (-2w, now()) ').expect).toMatchObject({
      kind: 'logical',
      history: 'was',
    });
  });

  it('expects fields and directions in ORDER BY', () => {
    expect(at('ORDER BY ').expect).toEqual({ kind: 'orderField' });
    expect(at('project = SHOP ORDER BY prio').expect).toEqual({ kind: 'orderField' });
    expect(at('project = SHOP ORDER BY priority ').expect).toEqual({ kind: 'direction' });
    expect(at('project = SHOP ORDER BY priority DESC, ').expect).toEqual({ kind: 'orderField' });
    expect(at('project = SHOP ORDER BY priority DESC ').expect).toEqual({ kind: 'direction' });
  });

  it('gives up on what is not KQL', () => {
    expect(at('project = = ').expect).toEqual({ kind: 'none' });
    expect(at('AND ').expect).toEqual({ kind: 'none' });
  });

  it('completes the word the caret is in, also before the end', () => {
    expect(at('proj|ect = SHOP')).toMatchObject({ prefix: 'proj', from: 0, to: 7 });
    expect(at('project = SHOP AND sta|tus = Open')).toMatchObject({
      expect: { kind: 'field' },
      prefix: 'sta',
    });
  });
});

describe('suggestions', () => {
  it('suggests fields, with their aliases, quoted where needed', () => {
    expect(labels('st')).toEqual(['status', 'Story Points']);
    expect(labels('typ')).toEqual(['issuetype']);
    const s = staticSuggestions(catalog, at('Story'))[0];
    expect(s.insert).toBe('"Story Points" ');
    expect(labels('').slice(-1)).toEqual(['NOT']);
    expect(labels('ord')).toEqual(['ORDER BY']);
  });

  it('suggests the operators of a field', () => {
    expect(labels('summary ')).toEqual(['~', '!~']);
    expect(labels('assignee ')).toEqual([
      '=',
      '!=',
      'IN',
      'NOT IN',
      'IS',
      'IS NOT',
      'WAS',
      'WAS NOT',
      'WAS IN',
      'WAS NOT IN',
      'CHANGED',
    ]);
    expect(labels('assignee i')).toEqual(['IN', 'IS', 'IS NOT']);
  });

  it('suggests functions that suit the field', () => {
    expect(labels('assignee = ')).toEqual(['currentUser()']);
    expect(labels('assignee in ')).toEqual(['( … )', 'currentUser()', 'membersOf()']);
    expect(labels('created >= start')).toEqual(['startOfWeek()']);
    expect(labels('assignee is ')).toEqual(['EMPTY']);
  });

  it('suggests keywords after a clause', () => {
    expect(labels('project = SHOP ')).toEqual(['AND', 'OR', 'ORDER BY']);
    expect(labels('(project = SHOP ')).toEqual(['AND', 'OR', ')']);
    expect(labels('project = SHOP ORDER BY created DESC, ')).toEqual([
      'project',
      'status',
      'assignee',
      'issuetype',
      'summary',
      'created',
      'Story Points',
    ]);
    expect(labels('status changed ')).toEqual([
      'AND',
      'OR',
      'AFTER',
      'BEFORE',
      'ON',
      'DURING ( … , … )',
      'BY',
      'FROM',
      'TO',
      'ORDER BY',
    ]);
    expect(labels('project = SHOP ORDER BY created ')).toEqual(['ASC', 'DESC', ',']);
  });

  it('asks the server for values of fields that have them, and for arguments', () => {
    expect(valueField(catalog, at('project = '))).toBe('project');
    expect(valueField(catalog, at('summary ~ '))).toBeUndefined();
    expect(valueField(catalog, at('assignee in membersOf("'))).toBe('membersOf');
    expect(valueField(catalog, at('created > startOfWeek('))).toBeUndefined();
    expect(valueField(catalog, at('project = SHOP AND '))).toBeUndefined();
  });

  it('inserts what was chosen and puts the caret after it', () => {
    const q = 'project = SH';
    const ctx = at(q);
    expect(apply(q, ctx, valueSuggestion({ value: 'SHOP', label: 'Webshop' }))).toEqual({
      text: 'project = SHOP ',
      caret: 15,
    });
    const q2 = 'status = "In Pro';
    expect(apply(q2, at(q2), valueSuggestion({ value: 'In Progress' })).text).toBe('status = "In Progress" ');
    const q3 = 'assignee in mem';
    const fn = staticSuggestions(catalog, at(q3)).find((s) => s.label === 'membersOf()')!;
    expect(apply(q3, at(q3), fn)).toEqual({ text: 'assignee in membersOf("")', caret: 23 });
    const q4 = 'assignee in membersOf("|")';
    expect(apply(q4.replace('|', ''), at(q4), valueSuggestion({ value: 'dev team' }, true)).text).toBe(
      'assignee in membersOf("dev team")',
    );
  });

  it('quotes as the server reads', () => {
    expect(quote('SHOP')).toBe('SHOP');
    expect(quote('In Progress')).toBe('"In Progress"');
    expect(quote('order')).toBe('"order"');
    expect(quote('say "hi"')).toBe('"say \\"hi\\""');
    expect(quote('Größe')).toBe('Größe');
  });

  it('finds fields by name, alias and id', () => {
    expect(findField(catalog, 'TYPE')?.name).toBe('issuetype');
    expect(findField(catalog, 'cf[10016]')?.name).toBe('Story Points');
    expect(findField(catalog, '"x"')).toBeUndefined();
  });
});
