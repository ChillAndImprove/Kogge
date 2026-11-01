// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import {
  ActionIcon,
  alpha,
  Anchor,
  Avatar,
  Badge,
  Button,
  Card,
  createTheme,
  darken,
  DEFAULT_THEME,
  Drawer,
  HoverCard,
  lighten,
  luminance,
  Menu,
  mergeThemeOverrides,
  Modal,
  NavLink,
  Notification,
  Paper,
  Popover,
  rem,
  SegmentedControl,
  Switch,
  Table,
  Tabs,
  toRgba,
  Tooltip,
  type CSSVariablesResolver,
  type MantineColorsTuple,
} from '@mantine/core';

// Kogge's own palette: a deep petrol primary and a warm amber accent (like a ship's lantern).
const petrol: MantineColorsTuple = [
  '#e7f6f7',
  '#cbe9ec',
  '#a3d8dd',
  '#76c3ca',
  '#4dabb4',
  '#2f939d',
  '#1d7b86',
  '#126571',
  '#0b5563',
  '#073f4a',
];

const amber: MantineColorsTuple = [
  '#fff8e6',
  '#ffefc8',
  '#fde29c',
  '#fbd26b',
  '#f8c346',
  '#f2b84b',
  '#de9f22',
  '#c4870f',
  '#a46e08',
  '#7f5404',
];

// Logbuch's leather brown.
const leather: MantineColorsTuple = [
  '#f8f1ec',
  '#ecdfd4',
  '#dbbfa8',
  '#c99f7a',
  '#b98353',
  '#ae7139',
  '#a8682c',
  '#93571f',
  '#834c19',
  '#723f10',
];

// Hafen's navy blue, from its logo: deeper and calmer than a web blue, which stays the colour of information.
const navy: MantineColorsTuple = [
  '#edf3fb',
  '#d9e5f6',
  '#b3cbec',
  '#89aee1',
  '#6393d6',
  '#4a80cc',
  '#3a70bd',
  '#2d5fa8',
  '#244f8f',
  '#1c3d6e',
];

// A cool grey: the subtle surface of sidebars, table heads and hovered rows (0), borders (3) and the borders of
// controls (4), placeholders (5), secondary text (6, 6:1 on white) and the text (9).
const gray: MantineColorsTuple = [
  '#f6f7f9',
  '#eef0f3',
  '#e9ecef',
  '#e3e6ea',
  '#d4d8de',
  '#858f9c',
  '#5a6372',
  '#434b56',
  '#2c323a',
  '#1b1f24',
];

// Helling's orange: the darkest shade carries white text and links at 5:1 (the usual orange reaches 4.3:1).
const orange: MantineColorsTuple = [
  ...DEFAULT_THEME.colors.orange.slice(0, 9),
  '#c0410b',
] as unknown as MantineColorsTuple;

// The dark scheme, from its text (0) and secondary text (2) through the borders (4) to its surfaces: raised (5:
// the header, hovered rows), surface (6: cards, sidebars, inputs) and the base of the page (7).
const dark: MantineColorsTuple = [
  '#e6e9ed',
  '#c3c9d1',
  '#9aa4b0',
  '#69727e',
  '#2a3038',
  '#1c2128',
  '#161a1f',
  '#101317',
  '#0c0f12',
  '#08090b',
];

/**
 * Kogge's design system (calm and precise, for people who work in it all day): Inter, bundled so that Kogge looks
 * the same on every client and needs no font service, on a 14 px base with a few sizes (12 meta, 13 secondary, 14
 * UI, 16 reading, 18 to 30 headings) and weights (400 text, 500 labels, 600 titles); a 4 px spacing grid; 32 px
 * controls (global.css); corners of 6 (controls), 8 (cards, menus, popovers) and 12 (dialogs); no shadows at rest.
 * Product identity comes from accents (the primary colour of each product's theme), never from slabs of colour.
 */
export const theme = createTheme({
  primaryColor: 'petrol',
  primaryShade: { light: 7, dark: 5 },
  colors: { petrol, amber, leather, navy, gray, dark, orange },
  // Text on filled colours turns dark where white would read poorly. With the dark text colour of the theme, both
  // reach 4.5:1 (WCAG AA) outside a narrow band of mid-tones (luminance 0.18–0.23), which filled colours avoid.
  autoContrast: true,
  luminanceThreshold: 0.205,
  black: '#1b1f24',
  fontFamily:
    '"Inter Variable", Inter, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif',
  fontFamilyMonospace:
    '"JetBrains Mono Variable", "JetBrains Mono", ui-monospace, SFMono-Regular, "Cascadia Mono", Menlo, Consolas, monospace',
  fontSizes: { xs: rem(12), sm: rem(13), md: rem(14), lg: rem(16), xl: rem(18) },
  lineHeights: { xs: '1.4', sm: '1.45', md: '1.45', lg: '1.6', xl: '1.4' },
  fontWeights: { regular: '400', medium: '500', bold: '600' },
  headings: {
    fontWeight: '600',
    sizes: {
      h1: { fontSize: rem(30), lineHeight: '1.2' },
      h2: { fontSize: rem(24), lineHeight: '1.25' },
      h3: { fontSize: rem(20), lineHeight: '1.3' },
      h4: { fontSize: rem(18), lineHeight: '1.35' },
      h5: { fontSize: rem(16), lineHeight: '1.4' },
      h6: { fontSize: rem(14), lineHeight: '1.45' },
    },
  },
  radius: { xs: rem(2), sm: rem(4), md: rem(6), lg: rem(8), xl: rem(12) },
  defaultRadius: 'md',
  spacing: { xs: rem(8), sm: rem(12), md: rem(16), lg: rem(24), xl: rem(32) },
  shadows: {
    xs: '0 1px 2px rgba(16, 24, 40, 0.05)',
    sm: '0 1px 3px rgba(16, 24, 40, 0.07), 0 1px 2px rgba(16, 24, 40, 0.04)',
    md: '0 8px 20px -6px rgba(16, 24, 40, 0.14), 0 2px 6px -2px rgba(16, 24, 40, 0.06)',
    lg: '0 16px 40px -10px rgba(16, 24, 40, 0.22), 0 4px 10px -4px rgba(16, 24, 40, 0.08)',
    xl: '0 24px 56px -12px rgba(16, 24, 40, 0.26)',
  },
  cursorType: 'pointer',
  focusRing: 'auto',
  components: {
    Anchor: Anchor.extend({ defaultProps: { underline: 'hover' } }),
    // Avatars of people and containers are filled with white initials (PersonAvatar, ContainerAvatar); others
    // (placeholders) are a tint.
    Avatar: Avatar.extend({ defaultProps: { variant: 'light' } }),
    // Badges are subtle lozenges in sentence case: a tint with darker text, 12 px, weight 600, radius 4. A screen
    // may still set tt="uppercase" for one big state badge.
    Badge: Badge.extend({ defaultProps: { radius: 'sm', variant: 'light', tt: 'none', fw: 600 } }),
    Button: Button.extend({ defaultProps: { radius: 'md' } }),
    // Icon buttons are 32 px (components/IconButton.tsx adds the tooltip every icon button needs).
    ActionIcon: ActionIcon.extend({ defaultProps: { size: 32, radius: 'md' } }),
    Card: Card.extend({ defaultProps: { radius: 'lg', padding: 'md' } }),
    Menu: Menu.extend({ defaultProps: { shadow: 'md', radius: 'lg' } }),
    Modal: Modal.extend({
      defaultProps: { radius: 'xl', shadow: 'lg', overlayProps: { backgroundOpacity: 0.45 } },
      styles: { title: { fontWeight: 600, fontSize: rem(18) } },
    }),
    Drawer: Drawer.extend({ defaultProps: { shadow: 'lg' } }),
    NavLink: NavLink.extend({ defaultProps: { variant: 'light' } }),
    Notification: Notification.extend({ defaultProps: { radius: 'lg' } }),
    Paper: Paper.extend({ defaultProps: { radius: 'lg' } }),
    Popover: Popover.extend({ defaultProps: { shadow: 'md', radius: 'lg' } }),
    HoverCard: HoverCard.extend({ defaultProps: { shadow: 'md', radius: 'lg' } }),
    SegmentedControl: SegmentedControl.extend({ defaultProps: { radius: 'md' } }),
    // An off switch is a solid grey track (global.css) with a plain knob: a dotted knob reads as disabled.
    Switch: Switch.extend({ defaultProps: { withThumbIndicator: false } }),
    // Rows of 40 px (32 px where a table sets verticalSpacing="xs").
    Table: Table.extend({
      defaultProps: { highlightOnHover: true, verticalSpacing: rem(10), horizontalSpacing: 'sm' },
    }),
    Tabs: Tabs.extend({ styles: { tab: { fontWeight: 500 } } }),
    Tooltip: Tooltip.extend({ defaultProps: { openDelay: 300, withArrow: true, arrowSize: 6, fz: 'xs' } }),
  },
});

/** The theme of a product (its accent colour), or of the platform. */
export function themeFor(color: string | undefined) {
  if (!color) return theme;
  // White text needs the darker shades of orange.
  const primaryShade = color === 'orange' ? ({ light: 9, dark: 7 } as const) : theme.primaryShade;
  return mergeThemeOverrides(theme, createTheme({ primaryColor: color, primaryShade }));
}

/** The contrast ratio of two colours (WCAG 2). */
export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** A colour, possibly translucent, as it shows on an opaque background (for checks of contrast). */
export function over(color: string, background: string): string {
  const c = toRgba(color);
  const b = toRgba(background);
  const mix = (x: number, y: number) => Math.round(x * c.a + y * (1 - c.a));
  return `rgb(${mix(c.r, b.r)}, ${mix(c.g, b.g)}, ${mix(c.b, b.b)})`;
}

/** A colour darkened until text in it reaches 4.6:1 on a background. */
function readableOn(color: string, background: string): string {
  let c = color;
  for (let i = 0; i < 12 && contrast(c, background) < 4.6; i++) c = darken(c, 0.06);
  return c;
}

/** A colour lightened until text in it reaches 4.6:1 on a dark background. */
function readableOnDark(color: string, background: string): string {
  let c = color;
  for (let i = 0; i < 12 && contrast(c, background) < 4.6; i++) c = lighten(c, 0.06);
  return c;
}

/**
 * The filled shade of a colour that its text reads well on. autoContrast picks white or dark text by the shade of
 * the light colour scheme (also in the dark one); shades where that text stays below 4.5:1 move (darker for white
 * text, lighter for dark text).
 */
function readableFilled(
  shades: readonly string[],
  shade: number,
  textShade: number,
  black: string,
  threshold: number,
): string {
  const white = luminance(shades[textShade]) < threshold;
  const text = white ? '#ffffff' : black;
  const order = white ? [shade, shade + 1, shade + 2, shade + 3] : [shade, shade - 1, shade - 2, shade - 3];
  for (const s of order) {
    if (s >= 0 && s <= 9 && contrast(shades[s], text) >= 4.5) return shades[s];
  }
  return white ? readableOn(shades[shade], '#ffffff') : shades[shade];
}

/** Filled colours (and the primary colour) whose text reads well, for a colour scheme's filled shade. */
function filledColors(
  t: Parameters<CSSVariablesResolver>[0],
  shade: number,
  textShade: number,
): Record<string, string> {
  const vars: Record<string, string> = {};
  const threshold = t.luminanceThreshold ?? 0.3;
  for (const [name, shades] of Object.entries(t.colors)) {
    const filled = readableFilled(shades, shade, textShade, t.black, threshold);
    if (filled !== shades[shade]) {
      vars[`--mantine-color-${name}-filled`] = filled;
      vars[`--mantine-color-${name}-filled-hover`] = darken(filled, 0.1);
    }
  }
  const primary = readableFilled(t.colors[t.primaryColor], shade, textShade, t.black, threshold);
  vars['--mantine-primary-color-filled'] = primary;
  vars['--mantine-primary-color-filled-hover'] = darken(primary, 0.1);
  return vars;
}

/** The surfaces of the dark colour scheme that tinted things lie on: the page, cards and inputs, hovered rows. */
export function darkSurfaces(t: Parameters<CSSVariablesResolver>[0]): string[] {
  return [t.colors.dark[7], t.colors.dark[6], t.colors.dark[5]];
}

/**
 * The tints of the dark colour scheme (light variants: badges, alerts, light buttons, selected items) and their
 * text. Mantine darkens a colour's darkest shade, which turns Kogge's palettes (whose darkest shades are deep)
 * into slabs darker than the page; and it writes on them in the lightest shade, which turns status icons white.
 * Here a tint is the colour's middle shade at a low opacity (neutral colours a lighter shade, a little stronger),
 * so that it is lighter than every surface it lies on and keeps its hue; its text is the colour's shade 4,
 * lightened until it reads at 4.6:1 on the tint over the lightest surface.
 */
function darkTints(
  t: Parameters<CSSVariablesResolver>[0],
  name: string,
): { light: string; hover: string; color: string } {
  const shades = t.colors[name];
  const neutral = name === 'gray' || name === 'dark';
  // The dark palette runs from light (0) to dark (9).
  const base = name === 'dark' ? shades[2] : neutral ? shades[5] : shades[6];
  const strength = neutral ? 0.22 : 0.2;
  const light = alpha(base, strength);
  const hover = alpha(base, strength + 0.08);
  const backgrounds = darkSurfaces(t).flatMap((s) => [over(light, s), over(hover, s)]);
  const lightest = backgrounds.reduce((a, b) => (luminance(b) > luminance(a) ? b : a));
  const text = name === 'dark' ? shades[1] : shades[4];
  return { light, hover, color: readableOnDark(text, lightest) };
}

/**
 * CSS variables that keep text readable (WCAG AA, 4.5:1) in both colour schemes: filled colours with their text,
 * the text of light variants (badges, alerts) on their tint, links in the product's colour, and dimmed text; in the
 * dark scheme also the tints themselves and disabled controls, which keep their shape on cards.
 */
export const accessibleColors: CSSVariablesResolver = (t) => {
  const lightShade = typeof t.primaryShade === 'number' ? t.primaryShade : t.primaryShade.light;
  const darkShade = typeof t.primaryShade === 'number' ? t.primaryShade : t.primaryShade.dark;
  const light: Record<string, string> = filledColors(t, lightShade, lightShade);
  for (const [name, shades] of Object.entries(t.colors)) {
    light[`--mantine-color-${name}-light-color`] = readableOn(shades[9], shades[1]);
  }
  const primary = t.colors[t.primaryColor];
  // Links keep 4.6:1 also on the light grey surfaces (hovered rows, heads of cards); links within text on tints
  // (alerts) are underlined in the alert's colour (global.css).
  light['--mantine-color-anchor'] = readableOn(primary[lightShade], t.colors.gray[1]);
  light['--mantine-primary-color-light-color'] = readableOn(primary[9], primary[1]);
  // Dimmed text keeps 4.6:1 on the dark surfaces, also on the lighter ones (inputs, hovered rows).
  const dark: Record<string, string> = {
    ...filledColors(t, darkShade, lightShade),
    '--mantine-color-dimmed': readableOnDark(t.colors.dark[2], t.colors.dark[5]),
    '--mantine-color-anchor': readableOnDark(primary[4], t.colors.dark[5]),
    // Disabled buttons keep a visible shape on cards (dark-6): a lighter fill, an edge (global.css) and dim text.
    '--mantine-color-disabled': t.colors.dark[5],
    '--mantine-color-disabled-color': t.colors.dark[2],
    '--mantine-color-disabled-border': t.colors.dark[4],
  };
  for (const name of Object.keys(t.colors)) {
    const tint = darkTints(t, name);
    dark[`--mantine-color-${name}-light`] = tint.light;
    dark[`--mantine-color-${name}-light-hover`] = tint.hover;
    dark[`--mantine-color-${name}-light-color`] = tint.color;
  }
  return { variables: {}, light, dark };
};
