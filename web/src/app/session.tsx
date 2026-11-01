// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { useMantineColorScheme } from '@mantine/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { createContext, useContext, useEffect, type ReactNode } from 'react';
import { ApiError, api, setCsrfToken, unwrap, type Schemas } from '../api/client';
import { setLanguage } from '../i18n';

export type Me = Schemas['Me'];

interface SessionValue {
  me: Me;
  can: (permission: string) => boolean;
  refresh: () => Promise<void>;
}

const SessionContext = createContext<SessionValue | null>(null);

export function useSession(): SessionValue {
  const s = useContext(SessionContext);
  if (!s) throw new Error('useSession outside of SessionProvider');
  return s;
}

export function useOptionalSession(): SessionValue | null {
  return useContext(SessionContext);
}

export const meQueryKey = ['me'] as const;

export function useMeQuery() {
  return useQuery({
    queryKey: meQueryKey,
    queryFn: async () => {
      try {
        const me = await unwrap(api.GET('/api/v1/me'));
        setCsrfToken(me.csrfToken);
        return me;
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null;
        throw e;
      }
    },
    staleTime: 60_000,
    retry: false,
  });
}

/**
 * Provides the signed-in user; renders `signedOut` when there is no session, and `twoFactorSetup` while the
 * installation requires a second factor that the user has to set up first.
 */
export function SessionProvider({
  children,
  signedOut,
  twoFactorSetup,
}: {
  children: ReactNode;
  signedOut: ReactNode;
  twoFactorSetup: ReactNode;
}) {
  const query = useMeQuery();
  const client = useQueryClient();
  const { setColorScheme } = useMantineColorScheme();
  const me = query.data;

  useEffect(() => {
    setLanguage(me?.user.locale);
    if (me?.user.theme === 'light' || me?.user.theme === 'dark' || me?.user.theme === 'auto') {
      setColorScheme(me.user.theme);
    }
  }, [me?.user.locale, me?.user.theme, setColorScheme]);

  if (query.isPending) return null;
  if (!me) return <>{signedOut}</>;

  const permissions = new Set(me.permissions ?? []);
  const value: SessionValue = {
    me,
    can: (p) => permissions.has(p) || permissions.has('admin'),
    refresh: () => client.invalidateQueries({ queryKey: meQueryKey }),
  };
  return (
    <SessionContext.Provider value={value}>
      {me.twoFactorSetup ? twoFactorSetup : children}
    </SessionContext.Provider>
  );
}
