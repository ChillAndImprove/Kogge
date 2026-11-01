// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { QueryClient, useQuery } from '@tanstack/react-query';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import { ErrorAlert, errorKind } from './ErrorAlert';
import { renderWithApp, setViewport } from './testing';

beforeAll(() => setViewport(1440));
afterEach(cleanup);

describe('errorKind', () => {
  it('tells refusals from failures', () => {
    expect(
      errorKind(new ApiError(403, 'repository.admin-required', 'Only administrators can do this.')),
    ).toBe('forbidden');
    expect(errorKind(new ApiError(404, 'not-found', 'Not found'))).toBe('notFound');
    expect(errorKind(new ApiError(409, 'issue.conflict', 'Changed meanwhile'))).toBe('invalid');
    expect(errorKind(new ApiError(422, 'validation', 'Name is required'))).toBe('invalid');
    expect(errorKind(new ApiError(500, '', 'Internal Server Error'))).toBe('failure');
    expect(errorKind(new ApiError(502, '', 'Bad Gateway'))).toBe('failure');
    expect(errorKind(new TypeError('Failed to fetch'))).toBe('failure');
  });
});

describe('ErrorAlert', () => {
  it('says "No access" calmly, without the word error', () => {
    renderWithApp(
      <ErrorAlert error={new ApiError(403, 'x', 'Only administrators of the repository can do this.')} />,
    );
    const alert = screen.getByRole('alert');
    expect(alert.textContent).toContain('No access');
    expect(alert.textContent).toContain('Only administrators of the repository can do this.');
    expect(alert.textContent).not.toContain('Something went wrong');
    expect(alert.getAttribute('data-error-kind')).toBe('forbidden');
  });

  it('says "Not found" for what is gone', () => {
    renderWithApp(<ErrorAlert error={new ApiError(404, 'x', 'The page does not exist.')} />);
    expect(screen.getByRole('alert').textContent).toContain('Not found');
  });

  it('shows only the message of a refused value', () => {
    renderWithApp(<ErrorAlert error={new ApiError(422, 'x', 'The key is taken.')} />);
    const alert = screen.getByRole('alert');
    expect(alert.textContent).toBe('The key is taken.');
  });

  it('offers to load a failed query again', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const load = vi
      .fn()
      .mockRejectedValueOnce(new ApiError(503, '', 'Service Unavailable'))
      .mockResolvedValue('ok');
    function Page() {
      const thing = useQuery({ queryKey: ['thing'], queryFn: load });
      return thing.data ? <p>{thing.data}</p> : <ErrorAlert error={thing.error} />;
    }
    renderWithApp(<Page />, client);
    expect((await screen.findByRole('alert')).textContent).toContain('Something went wrong');
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('ok')).toBeTruthy();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('offers no "Try again" for a failed change, whose own button tries again', () => {
    renderWithApp(<ErrorAlert error={new ApiError(500, '', 'Internal Server Error')} />);
    expect(screen.getByRole('alert').textContent).toContain('Something went wrong');
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
  });

  it('calls the given retry', () => {
    const retry = vi.fn();
    renderWithApp(<ErrorAlert error={new TypeError('Failed to fetch')} onRetry={retry} />);
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(retry).toHaveBeenCalled();
  });

  it('shows nothing without an error', () => {
    renderWithApp(<ErrorAlert error={null} />);
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
