// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Sizes of the limits (Administration → Settings → Limits): the settings hold whole MiB, people read and write
// them in MiB or GiB.

export type SizeUnit = 'MiB' | 'GiB';

const factor: Record<SizeUnit, number> = { MiB: 1, GiB: 1024 };

/** The unit a size in MiB reads best in: GiB for whole GiB, MiB otherwise. */
export function unitOf(mib: number): SizeUnit {
  return mib >= 1024 && mib % 1024 === 0 ? 'GiB' : 'MiB';
}

/** A size in MiB as an amount of a unit. */
export function inUnit(mib: number, unit: SizeUnit): number {
  return mib / factor[unit];
}

/** An amount of a unit in whole MiB. */
export function toMiB(amount: number, unit: SizeUnit): number {
  return Math.round(amount * factor[unit]);
}

/** A size in MiB for people, in the unit it reads best in: "2 GiB", "1.5 GiB", "300 MiB". */
export function formatMiB(mib: number, locale: string): string {
  const unit = mib >= 1024 ? 'GiB' : 'MiB';
  const amount = inUnit(mib, unit).toLocaleString(locale, { maximumFractionDigits: 2 });
  return `${amount} ${unit}`;
}
