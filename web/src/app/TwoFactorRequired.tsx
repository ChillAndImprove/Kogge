// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, Card, Center, Group, Stack, Text, ThemeIcon, Title } from '@mantine/core';
import { IconShieldLock } from '@tabler/icons-react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../api/client';
import { notifyError } from '../components/notify';
import { TwoFactorSetup } from '../pages/profile/TwoFactor';
import { meQueryKey, useSession } from './session';

/**
 * The installation requires two-factor authentication and the signed-in user has none: before anything else,
 * they set it up (or sign out). The server lets the session do nothing else meanwhile.
 */
export function TwoFactorRequired() {
  const { t } = useTranslation();
  const { me } = useSession();
  const client = useQueryClient();
  const signOut = async () => {
    try {
      const res = await unwrap(api.POST('/api/v1/auth/logout'));
      client.setQueryData(meQueryKey, null);
      if (res?.redirect) window.location.assign(res.redirect);
    } catch (e) {
      notifyError(e);
    }
  };
  return (
    <Center mih="100vh" p="md" component="main">
      <Card withBorder radius="md" padding="xl" maw={760} w="100%">
        <Stack>
          <Group gap="sm" wrap="nowrap">
            <ThemeIcon size={40} radius="md" variant="light">
              <IconShieldLock size={24} aria-hidden />
            </ThemeIcon>
            <div>
              <Title order={2}>{t('twoFactorRequired.title')}</Title>
              <Text size="sm" c="dimmed">
                {t('twoFactorRequired.signedInAs', { name: me.user.displayName, instance: me.instanceName })}
              </Text>
            </div>
          </Group>
          <Text size="sm">{t('twoFactorRequired.text')}</Text>
          <TwoFactorSetup onDone={() => undefined} />
          <Group justify="flex-start">
            <Button variant="subtle" color="gray" onClick={() => void signOut()}>
              {t('nav.signOut')}
            </Button>
          </Group>
        </Stack>
      </Card>
    </Center>
  );
}
