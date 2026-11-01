// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import type { ComponentType } from 'react';

/**
 * The products of the Kogge platform. Each has its own name, logo, accent colour, navigation and start
 * page, and connects to the others like separate products do; all share one neutral header, where the logo tile
 * and the accent (the active item, "Create", links, focus) tell them apart. The platform itself (sign-in, the
 * product overview, administration of people and settings) carries the Kogge brand.
 */
export type ProductId = 'kontor' | 'logbuch' | 'hafen' | 'helling';

export interface Product {
  id: ProductId;
  name: string;
  /** Mantine palette of the product's accent colour. */
  color: string;
  /** The product's start page. */
  home: string;
  /** Path prefixes that belong to the product. */
  prefixes: string[];
  Logo: ComponentType<{ size?: number }>;
}

/** The rounded tile all logos share. */
function Tile({ size, bg, children }: { size: number; bg: string; children: React.ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" aria-hidden="true">
      <rect width="64" height="64" rx="14" fill={bg} />
      {children}
    </svg>
  );
}

/** Kontor: three columns of cards, as on a board. */
function KontorLogo({ size = 28 }: { size?: number }) {
  return (
    <Tile size={size} bg="#5f3dc4">
      <rect x="13" y="15" width="10" height="34" rx="3" fill="#fff" />
      <rect x="27" y="15" width="10" height="22" rx="3" fill="#fff" opacity="0.85" />
      <rect x="41" y="15" width="10" height="28" rx="3" fill="#d0bfff" />
    </Tile>
  );
}

/** Logbuch: an open book with a ribbon. */
function LogbuchLogo({ size = 28 }: { size?: number }) {
  return (
    <Tile size={size} bg="#8a5527">
      <path d="M32 20c-5-4-12-5-19-4v29c7-1 14 0 19 4 5-4 12-5 19-4V16c-7-1-14 0-19 4z" fill="#fbf4ea" />
      <path d="M32 20v29" stroke="#8a5527" strokeWidth="2.5" />
      <path d="M40 16v17l3.5-3 3.5 3V15.5" fill="#f2b84b" />
    </Tile>
  );
}

/** Hafen: an anchor. */
function HafenLogo({ size = 28 }: { size?: number }) {
  return (
    <Tile size={size} bg="#1c3d6e">
      <g fill="none" stroke="#fff" strokeWidth="4" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="32" cy="16.5" r="4.5" />
        <path d="M32 21v28M23 28h18M16 38c2 7 8 11 16 11s14-4 16-11" />
      </g>
    </Tile>
  );
}

/** Helling: a hull on the slipway. */
function HellingLogo({ size = 28 }: { size?: number }) {
  return (
    <Tile size={size} bg="#d9480f">
      <path d="M10 50 54 37" stroke="#fff" strokeWidth="3.5" strokeLinecap="round" opacity="0.7" />
      <path d="M16 36.5 47 27.5l-2.5 10-23 6.7z" fill="#fff" />
      <path d="M30 31V15l11 9-11 3" fill="#ffd8a8" />
    </Tile>
  );
}

export const products: Record<ProductId, Product> = {
  kontor: {
    id: 'kontor',
    name: 'Kontor',
    color: 'violet',
    home: '/issues',
    prefixes: ['/issues', '/projects', '/browse', '/dashboards'],
    Logo: KontorLogo,
  },
  logbuch: {
    id: 'logbuch',
    name: 'Logbuch',
    color: 'leather',
    home: '/wiki',
    prefixes: ['/wiki'],
    Logo: LogbuchLogo,
  },
  hafen: {
    id: 'hafen',
    name: 'Hafen',
    color: 'navy',
    home: '/code',
    prefixes: ['/code'],
    Logo: HafenLogo,
  },
  helling: {
    id: 'helling',
    name: 'Helling',
    color: 'orange',
    home: '/builds',
    prefixes: ['/builds'],
    Logo: HellingLogo,
  },
};

export const productList: Product[] = [products.kontor, products.logbuch, products.hafen, products.helling];

/** The product an address belongs to; null for the platform (overview, administration, profile). */
export function productOf(path: string): ProductId | null {
  const p = path.split(/[?#]/)[0];
  for (const product of productList) {
    if (product.prefixes.some((prefix) => p === prefix || p.startsWith(prefix + '/'))) return product.id;
  }
  return null;
}
