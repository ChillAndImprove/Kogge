// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { MemoryRouter } from 'react-router';
import '../i18n';
import { theme } from '../theme';

/** Lets media queries of the tests match a phone's width (390 px) or a desktop's (1440 px). */
export function setViewport(width: number) {
  window.matchMedia = (query: string) => {
    const max = /max-width:\s*([\d.]+)em/.exec(query);
    const min = /min-width:\s*([\d.]+)em/.exec(query);
    const matches = (!max || width <= Number(max[1]) * 16) && (!min || width >= Number(min[1]) * 16);
    return {
      matches,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    } as MediaQueryList;
  };
}

/** Renders a part of a page as the application does: with the theme, a router and a query client. */
export function renderWithApp(ui: ReactElement, client = new QueryClient()) {
  return render(
    <MantineProvider theme={theme}>
      <QueryClientProvider client={client}>
        <MemoryRouter>{ui}</MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}
