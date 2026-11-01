// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Button, getDefaultZIndex, Group, Modal, Paper, SegmentedControl, Stack, Text } from '@mantine/core';
import { IconHeart, IconRocket } from '@tabler/icons-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { setLanguage } from '../i18n';
import { onPreviewBlocked, repositoryUrl, type PreviewAction } from './preview';

/**
 * Where the preview sends people who want to deploy or sponsor Kogge. Sponsoring needs an address of its own
 * (VITE_KOGGE_SPONSOR_URL when building): not every owner of a repository can be sponsored.
 */
function links() {
  const repo = repositoryUrl();
  return {
    deploy: repo ? `${repo}#readme` : null,
    sponsor: (import.meta.env.VITE_KOGGE_SPONSOR_URL as string | undefined) || null,
  };
}

/**
 * What the preview adds to the application: a note that it is a proof of concept with sample content (with the
 * language, how to deploy and how to sponsor), and a dialog whenever someone tries what needs a running Kogge.
 */
export function PreviewChrome() {
  const { t, i18n } = useTranslation();
  const [action, setAction] = useState<PreviewAction | null>(null);
  useEffect(() => onPreviewBlocked(setAction), []);
  const { deploy, sponsor } = links();
  return (
    <>
      <Paper
        shadow="md"
        radius="md"
        withBorder
        px="sm"
        py={6}
        pos="fixed"
        bottom={12}
        left={12}
        right={12}
        mx="auto"
        w="fit-content"
        style={{ zIndex: 300 }}
        data-testid="preview-banner"
      >
        <Group gap="sm" wrap="wrap" justify="center">
          <Text size="sm">{t('preview.banner')}</Text>
          <SegmentedControl
            size="xs"
            value={i18n.language === 'de' ? 'de' : 'en'}
            onChange={(lang) => setLanguage(lang)}
            data={[
              { value: 'de', label: 'DE' },
              { value: 'en', label: 'EN' },
            ]}
            aria-label={t('preview.language')}
          />
          {sponsor && (
            <Button
              size="compact-sm"
              variant="light"
              color="pink"
              leftSection={<IconHeart size={14} />}
              component="a"
              href={sponsor}
              target="_blank"
              rel="noopener"
            >
              {t('preview.sponsor')}
            </Button>
          )}
          {deploy && (
            <Button
              size="compact-sm"
              leftSection={<IconRocket size={14} />}
              component="a"
              href={deploy}
              target="_blank"
              rel="noopener"
            >
              {t('preview.deploy')}
            </Button>
          )}
        </Group>
      </Paper>
      {/* Above the dialog it may be opened from (a bulk change, a move, an export). */}
      <Modal
        opened={action !== null}
        onClose={() => setAction(null)}
        title={t('preview.title')}
        size={560}
        centered
        zIndex={getDefaultZIndex('max')}
      >
        <Stack>
          {action && <Text>{t(`preview.actions.${action}`)}</Text>}
          <Text size="sm" c="dimmed">
            {t('preview.explain')}
          </Text>
          <Text size="sm">{t('preview.poc')}</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setAction(null)}>
              {t('preview.keepLooking')}
            </Button>
            {sponsor && (
              <Button
                variant="light"
                color="pink"
                leftSection={<IconHeart size={16} />}
                component="a"
                href={sponsor}
                target="_blank"
                rel="noopener"
              >
                {t('preview.sponsor')}
              </Button>
            )}
            {deploy && (
              <Button
                leftSection={<IconRocket size={16} />}
                component="a"
                href={deploy}
                target="_blank"
                rel="noopener"
              >
                {t('preview.howTo')}
              </Button>
            )}
          </Group>
        </Stack>
      </Modal>
    </>
  );
}
