// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import '@fontsource-variable/inter';
import '@fontsource-variable/jetbrains-mono';
import '@mantine/core/styles.css';
import '@mantine/notifications/styles.css';
import '@mantine/dates/styles.css';
import './styles/global.css';
import './styles/highlight.css';
import './i18n';

import { MantineProvider, mergeThemeOverrides } from '@mantine/core';
import { DatesProvider } from '@mantine/dates';
import dayjs from 'dayjs';
import 'dayjs/locale/de';
import 'dayjs/locale/en-gb';
import localizedFormat from 'dayjs/plugin/localizedFormat';
import { ModalsProvider } from '@mantine/modals';
import { Notifications } from '@mantine/notifications';
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { createRoot } from 'react-dom/client';
import { RouterProvider } from 'react-router';
import { ApiError } from './api/client';
import { router } from './app/router';
import { meQueryKey, SessionProvider } from './app/session';
import { TwoFactorRequired } from './app/TwoFactorRequired';
import { LoginPage } from './pages/LoginPage';
import { productOf, products, type ProductId } from './products';
import { PreviewChrome } from './showcase/PreviewChrome';
import { installPreview, isPreview } from './showcase/preview';
import { accessibleColors, themeFor } from './theme';

// Date inputs show days in the language's format (valueFormat "L").
dayjs.extend(localizedFormat);

// A 401 anywhere means the session ended: drop the user, which shows the sign-in page.
const onError = (error: unknown) => {
  if (error instanceof ApiError && error.status === 401) {
    queryClient.setQueryData(meQueryKey, null);
  }
};

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
  defaultOptions: {
    queries: {
      retry: (count, error) => !(error instanceof ApiError && error.status < 500) && count < 2,
      refetchOnWindowFocus: true,
      staleTime: 15_000,
    },
  },
});

/** The product of the current address, following the router. */
function useCurrentProduct(): ProductId | null {
  const [product, setProduct] = useState(() => productOf(router.state.location.pathname));
  useEffect(() => router.subscribe((state) => setProduct(productOf(state.location.pathname))), []);
  return product;
}

/**
 * The theme in the colours of the current product, with translated accessible names for icon-only buttons of
 * Mantine components.
 */
function ThemeProvider({ children }: { children: ReactNode }) {
  const { t, i18n } = useTranslation();
  const product = useCurrentProduct();
  const localized = useMemo(
    () =>
      mergeThemeOverrides(themeFor(product ? products[product].color : undefined), {
        components: {
          Modal: { defaultProps: { closeButtonProps: { 'aria-label': t('common.close') } } },
          Drawer: { defaultProps: { closeButtonProps: { 'aria-label': t('common.close') } } },
        },
      }),
    [t, product],
  );
  return (
    <MantineProvider theme={localized} defaultColorScheme="auto" cssVariablesResolver={accessibleColors}>
      <DatesProvider settings={{ locale: i18n.language === 'de' ? 'de' : 'en-gb', firstDayOfWeek: 1 }}>
        {children}
      </DatesProvider>
    </MantineProvider>
  );
}

const render = () =>
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <ModalsProvider>
            <SessionProvider
              signedOut={
                <>
                  <Notifications position="top-right" />
                  <LoginPage />
                </>
              }
              twoFactorSetup={
                <>
                  <Notifications position="top-right" />
                  <TwoFactorRequired />
                </>
              }
            >
              <RouterProvider router={router} />
            </SessionProvider>
            {isPreview && <PreviewChrome />}
          </ModalsProvider>
        </QueryClientProvider>
      </ThemeProvider>
    </StrictMode>,
  );

// The preview (for GitHub Pages) answers from its recording; the real application talks to the server.
if (isPreview) void installPreview().then(render);
else render();
