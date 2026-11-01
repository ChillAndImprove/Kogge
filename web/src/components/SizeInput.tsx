// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Checkbox, Group, NumberInput, Select, Stack, Text } from '@mantine/core';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { inUnit, toMiB, unitOf, type SizeUnit } from '../app/sizes';

/**
 * A size in MiB, entered in MiB or GiB. With offLabel, 0 switches the limit off: a checkbox turns it off, and on
 * again with onValue. The description follows the input, so that inputs side by side stay in line.
 */
export function SizeInput({
  label,
  description,
  value,
  onChange,
  min = 1,
  max = 1048576,
  offLabel,
  onValue = 1024,
}: {
  label: string;
  description?: string;
  value: number;
  onChange: (mib: number) => void;
  min?: number;
  max?: number;
  offLabel?: string;
  onValue?: number;
}) {
  const { t } = useTranslation();
  const descriptionId = useId();
  // The unit stays as chosen while the amount is typed (1.5 GiB is no whole GiB, but shows as GiB).
  const [unit, setUnit] = useState<SizeUnit>(() => unitOf(value));
  const off = offLabel !== undefined && value === 0;
  const amount = inUnit(value, unit);
  return (
    <Stack gap={4}>
      <Group gap="xs" align="flex-end" wrap="nowrap">
        <NumberInput
          label={label}
          aria-describedby={description ? descriptionId : undefined}
          value={off ? '' : amount}
          disabled={off}
          min={min / (unit === 'GiB' ? 1024 : 1)}
          max={max / (unit === 'GiB' ? 1024 : 1)}
          decimalScale={unit === 'GiB' ? 2 : 0}
          allowNegative={false}
          onChange={(x) => {
            const n = typeof x === 'number' ? x : Number.parseFloat(x);
            if (Number.isFinite(n)) onChange(Math.min(Math.max(toMiB(n, unit), min), max));
          }}
          style={{ flex: 1 }}
        />
        <Select
          aria-label={t('common.unitOf', { label })}
          data={['MiB', 'GiB']}
          value={unit}
          disabled={off}
          allowDeselect={false}
          onChange={(u) => u && setUnit(u as SizeUnit)}
          w={90}
        />
      </Group>
      {offLabel !== undefined && (
        <Checkbox
          label={offLabel}
          checked={off}
          onChange={(e) => {
            if (e.currentTarget.checked) {
              onChange(0);
            } else {
              setUnit(unitOf(onValue));
              onChange(onValue);
            }
          }}
        />
      )}
      {description && (
        <Text id={descriptionId} size="xs" c="dimmed">
          {description}
        </Text>
      )}
    </Stack>
  );
}
