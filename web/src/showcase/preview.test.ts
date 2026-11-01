// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { describe, expect, it } from 'vitest';
import { actionOf, appHref, noteAnswer, startsChange } from './preview';

describe('what the preview explains', () => {
  it('names what a change is about', () => {
    expect(actionOf('/api/v1/issues')).toBe('issue');
    expect(actionOf('/api/v1/issues/bulk')).toBe('issue');
    expect(actionOf('/api/v1/issues/SHOP-4/move')).toBe('issue');
    expect(actionOf('/api/v1/issues/bulk/check')).toBe('issue');
    expect(actionOf('/api/v1/code/repos/WEB/payments/pull-requests/1/auto-merge')).toBe('pullRequest');
    expect(actionOf('/api/v1/code/repos/WEB/payments/pull-requests/1/merge')).toBe('pullRequest');
    expect(actionOf('/api/v1/code/projects/WEB/repos')).toBe('repository');
    expect(actionOf('/api/v1/wiki/spaces/ENG/pages')).toBe('page');
    expect(actionOf('/api/v1/wiki/pages/5/edit')).toBe('page');
    expect(actionOf('/api/v1/wiki/spaces/ENG/exports')).toBe('download');
    expect(actionOf('/api/v1/wiki/exports/3')).toBe('download');
    expect(actionOf('/api/v1/builds/jobs/8/play')).toBe('build');
    expect(actionOf('/api/v1/auth/logout')).toBe('signOut');
    expect(actionOf('/api/v1/me')).toBe('change');
  });

  it('answers what pages note on their own, without a dialog', () => {
    expect(noteAnswer('POST', '/api/v1/wiki/pages/12/presence')).toEqual({
      status: 200,
      type: 'application/json',
      body: '[]',
    });
    expect(noteAnswer('POST', '/api/v1/me/recent')?.status).toBe(204);
    expect(noteAnswer('GET', '/api/v1/me/recent')).toBeNull();
    expect(noteAnswer('POST', '/api/v1/wiki/pages/12/comments')).toBeNull();
  });

  it('takes opening the editor of a page for a change', () => {
    expect(startsChange('GET', '/api/v1/wiki/pages/12/edit')).toBe(true);
    expect(startsChange('GET', '/api/v1/wiki/pages/12')).toBe(false);
    expect(startsChange('POST', '/api/v1/wiki/pages/12/edit')).toBe(false);
  });

  it('links to pages of the application as the router addresses them', () => {
    // The tests run the application, not the preview (whose addresses are after the #).
    expect(appHref('/browse/SHOP-4')).toBe('/browse/SHOP-4');
  });
});
