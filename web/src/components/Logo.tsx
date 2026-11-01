// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/** Kogge's mark: a cog ship's single square sail above a hull, in petrol and amber. */
export function Logo({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" aria-hidden="true">
      <rect width="64" height="64" rx="14" fill="#0b5563" />
      <path d="M14 40h36l-5 9H19z" fill="#f2b84b" />
      <path
        d="M31 12v26M31 14c8 3 13 9 14 21H31M29 18c-6 3-10 8-11 17h11"
        fill="none"
        stroke="#fff"
        strokeWidth="3.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
