// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Group, Select, Text } from '@mantine/core';
import CodeBlock from '@tiptap/extension-code-block';
import {
  NodeViewContent,
  NodeViewWrapper,
  ReactNodeViewRenderer,
  type ReactNodeViewProps,
} from '@tiptap/react';
import { useTranslation } from 'react-i18next';

/** Languages the highlighter knows, by the names code blocks use (Markdown's fence names). */
const languages: [string, string][] = [
  ['bash', 'Bash'],
  ['c', 'C'],
  ['cpp', 'C++'],
  ['csharp', 'C#'],
  ['css', 'CSS'],
  ['diff', 'Diff'],
  ['dockerfile', 'Dockerfile'],
  ['go', 'Go'],
  ['graphql', 'GraphQL'],
  ['groovy', 'Groovy'],
  ['hcl', 'HCL (Terraform)'],
  ['html', 'HTML'],
  ['ini', 'INI'],
  ['java', 'Java'],
  ['javascript', 'JavaScript'],
  ['json', 'JSON'],
  ['kotlin', 'Kotlin'],
  ['makefile', 'Makefile'],
  ['markdown', 'Markdown'],
  ['mermaid', 'Mermaid'],
  ['php', 'PHP'],
  ['powershell', 'PowerShell'],
  ['python', 'Python'],
  ['ruby', 'Ruby'],
  ['rust', 'Rust'],
  ['scala', 'Scala'],
  ['sql', 'SQL'],
  ['swift', 'Swift'],
  ['toml', 'TOML'],
  ['typescript', 'TypeScript'],
  ['xml', 'XML'],
  ['yaml', 'YAML'],
];

/** A code block in the editor, with the language it is highlighted in, as readers see it. */
function CodeBlockView({ node, updateAttributes, editor }: ReactNodeViewProps) {
  const { t } = useTranslation();
  const language = String(node.attrs.language ?? '');
  const data = [
    { value: '', label: t('editor.plainText') },
    ...languages.map(([value, label]) => ({ value, label })),
  ];
  if (language && !languages.some(([v]) => v === language)) data.push({ value: language, label: language });
  return (
    <NodeViewWrapper className="kogge-editor-codeblock" data-language={language || undefined}>
      <Group justify="flex-end" contentEditable={false} className="kogge-editor-codeblock-head">
        {editor.isEditable ? (
          <Select
            size="xs"
            w={170}
            data={data}
            value={language}
            onChange={(v) => updateAttributes({ language: v || null })}
            searchable
            allowDeselect={false}
            aria-label={t('editor.codeLanguage')}
            comboboxProps={{ withinPortal: true }}
          />
        ) : (
          <Text size="xs" c="dimmed">
            {data.find((d) => d.value === language)?.label ?? language}
          </Text>
        )}
      </Group>
      {/* The classes of the server's highlighter colour the code as on the page (codeHighlight.ts). */}
      <pre spellCheck={false} className="kogge-hl">
        <NodeViewContent<'code'> as="code" />
      </pre>
    </NodeViewWrapper>
  );
}

/** The code block of the editor, with a choice of language. */
export const EditorCodeBlock = CodeBlock.extend({
  addNodeView() {
    return ReactNodeViewRenderer(CodeBlockView);
  },
});
