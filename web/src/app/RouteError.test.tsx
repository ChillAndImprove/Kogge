// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { MantineProvider } from '@mantine/core';
import { cleanup, render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { RouteError } from './RouteError';

window.matchMedia ??= (query: string) =>
  ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }) as MediaQueryList;

function failing(message: string) {
  function Broken(): never {
    throw new Error(message);
  }
  const router = createMemoryRouter([{ path: '/', element: <Broken />, errorElement: <RouteError /> }]);
  render(
    <MantineProvider>
      <RouterProvider router={router} />
    </MantineProvider>,
  );
}

afterEach(cleanup);

describe('the error page', () => {
  it('says what failed and offers to load the page again', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    failing('The board has no columns');
    expect(screen.getByRole('alert').textContent).toContain('Something went wrong');
    expect(screen.getByText('The board has no columns')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Load again' })).toBeTruthy();
  });

  it('recognises an update of Kogge (its old parts are gone)', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    failing('Failed to fetch dynamically imported module: /assets/PageView-abc.js');
    expect(screen.getByRole('alert').textContent).toContain('Kogge was updated');
    expect(screen.queryByText(/PageView-abc/)).toBeNull();
  });
});
