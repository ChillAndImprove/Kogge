// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Alert, Button, Center, Divider, Paper, PasswordInput, Stack, TextInput, Title } from '@mantine/core';
import { IconKey } from '@tabler/icons-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, api, setCsrfToken, unwrap } from '../api/client';
import { meQueryKey } from '../app/session';
import { ErrorAlert } from '../components/ErrorAlert';
import { Logo } from '../components/Logo';
import { setLanguage } from '../i18n';

export function LoginPage() {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  // Accounts with two-factor authentication sign in in two steps: password, then a code.
  const [step, setStep] = useState<'password' | 'code'>('password');
  const [code, setCode] = useState('');
  const config = useQuery({
    queryKey: ['auth-config'],
    queryFn: () => unwrap(api.GET('/api/v1/auth/config')),
  });
  useEffect(() => {
    if (!config.data) return;
    // The browser's language wins if Kogge speaks it; otherwise the installation's default.
    const browser = navigator.language.slice(0, 2).toLowerCase();
    setLanguage(browser === 'de' || browser === 'en' ? browser : config.data.defaultLocale);
  }, [config.data]);
  const login = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/api/v1/auth/login', {
          body: { username, password, code: step === 'code' ? code.trim() : undefined },
        }),
      ),
    onSuccess: (me) => {
      setCsrfToken(me.csrfToken);
      client.setQueryData(meQueryKey, me);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === 'auth.second-factor-required') {
        setStep('code');
        setCode('');
      }
    },
  });
  // Asking for the code is the next step, not an error.
  const loginError =
    login.error instanceof ApiError && login.error.code === 'auth.second-factor-required'
      ? null
      : login.error;
  const params = new URLSearchParams(window.location.search);
  const error = params.get('error');
  const returnTo =
    window.location.pathname === '/login' ? '/' : window.location.pathname + window.location.search;
  const name = config.data?.instanceName ?? 'Kogge';

  return (
    <Center mih="100vh" p="md">
      <Paper withBorder shadow="sm" p="xl" w={400} radius="lg">
        <Stack>
          <Center>
            <Logo size={56} />
          </Center>
          <Title order={3} ta="center">
            {t('auth.title', { name })}
          </Title>
          {error === 'sso' && <Alert color="red">{t('auth.errorSso')}</Alert>}
          {error === 'disabled' && <Alert color="red">{t('auth.errorDisabled')}</Alert>}
          {config.data?.sso && (
            <Button
              component="a"
              href={`${config.data.ssoLoginUrl}?return=${encodeURIComponent(returnTo)}`}
              leftSection={<IconKey size={18} />}
              size="md"
            >
              {t('auth.sso', { name: config.data.ssoName })}
            </Button>
          )}
          {config.data?.sso && config.data?.localLogin && (
            <Divider label={t('auth.or')} labelPosition="center" />
          )}
          {config.data?.localLogin && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                login.mutate();
              }}
            >
              {step === 'password' ? (
                <Stack>
                  <TextInput
                    label={t('auth.username')}
                    value={username}
                    onChange={(e) => setUsername(e.currentTarget.value)}
                    autoComplete="username"
                    autoFocus={!config.data?.sso}
                    required
                  />
                  <PasswordInput
                    label={t('auth.password')}
                    value={password}
                    onChange={(e) => setPassword(e.currentTarget.value)}
                    autoComplete="current-password"
                    required
                  />
                  <ErrorAlert error={loginError} />
                  <Button
                    type="submit"
                    loading={login.isPending}
                    variant={config.data?.sso ? 'default' : 'filled'}
                  >
                    {t('auth.signIn')}
                  </Button>
                </Stack>
              ) : (
                <Stack>
                  <TextInput
                    label={t('auth.code')}
                    description={t('auth.codeHelp')}
                    value={code}
                    onChange={(e) => setCode(e.currentTarget.value)}
                    autoComplete="one-time-code"
                    inputMode="text"
                    autoFocus
                    required
                    maxLength={32}
                  />
                  <ErrorAlert error={loginError} />
                  <Button type="submit" loading={login.isPending}>
                    {t('auth.signIn')}
                  </Button>
                  <Button
                    variant="subtle"
                    onClick={() => {
                      setStep('password');
                      login.reset();
                    }}
                  >
                    {t('auth.back')}
                  </Button>
                </Stack>
              )}
            </form>
          )}
        </Stack>
      </Paper>
    </Center>
  );
}
