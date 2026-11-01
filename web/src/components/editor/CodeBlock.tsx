// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { ActionIcon, CopyButton, Group, Text, Tooltip } from '@mantine/core';
import { IconCheck, IconCopy } from '@tabler/icons-react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, unwrap } from '../../api/client';

/**
 * A code block of a document: highlighted by the server's highlighter when its language is known (as tokens,
 * rendered as React elements — no HTML is injected), with its title or language and a copy button.
 */
export function CodeBlock({ code, language, title }: { code: string; language?: string; title?: string }) {
  const { t } = useTranslation();
  const lang = (language ?? '').trim();
  const highlighted = useQuery({
    queryKey: ['highlight', lang, code],
    queryFn: () => unwrap(api.POST('/api/v1/code/highlight', { body: { language: lang, text: code } })),
    enabled: !!lang && code.length > 0 && code.length <= 65536,
    staleTime: Infinity,
    gcTime: 10 * 60_000,
    retry: false,
  });
  const lines = highlighted.data?.lines;
  const label = title || highlighted.data?.language || lang;
  return (
    <figure className="kogge-codeblock">
      <Group justify="space-between" wrap="nowrap" className="kogge-codeblock-head">
        <Text size="xs" fw={600} c="dimmed" truncate component={title ? 'figcaption' : 'span'}>
          {label}
        </Text>
        <CopyButton value={code} timeout={1500}>
          {({ copied, copy }) => (
            <Tooltip label={copied ? t('common.copied') : t('common.copy')} withinPortal>
              <ActionIcon
                size="sm"
                variant="subtle"
                color={copied ? 'teal' : 'gray'}
                onClick={copy}
                aria-label={t('common.copy')}
              >
                {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
              </ActionIcon>
            </Tooltip>
          )}
        </CopyButton>
      </Group>
      <pre className="kogge-hl" data-language={lang || undefined}>
        <code>
          {lines
            ? lines.map((line, i) => (
                <span key={i} className="kogge-hl-line">
                  {line.map((tok, j) =>
                    tok.c ? (
                      <span key={j} className={tok.c}>
                        {tok.t}
                      </span>
                    ) : (
                      tok.t
                    ),
                  )}
                  {i < lines.length - 1 ? '\n' : ''}
                </span>
              ))
            : code}
        </code>
      </pre>
    </figure>
  );
}
