// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { DEFAULT_THEME, luminance, mergeMantineTheme, toRgba } from '@mantine/core';
import { describe, expect, it } from 'vitest';
import { avatarColors } from './components/avatarColor';
import { accessibleColors, contrast, darkSurfaces, over, theme, themeFor } from './theme';

/** The hue (0–360) and saturation (0–1) of a colour, as HSL has them. */
function hueOf(color: string): { hue: number; saturation: number } {
  const { r, g, b } = toRgba(color);
  const [R, G, B] = [r / 255, g / 255, b / 255];
  const max = Math.max(R, G, B);
  const min = Math.min(R, G, B);
  const l = (max + min) / 2;
  if (max === min) return { hue: 0, saturation: 0 };
  const d = max - min;
  const saturation = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  let hue = max === R ? (G - B) / d + (G < B ? 6 : 0) : max === G ? (B - R) / d + 2 : (R - G) / d + 4;
  hue *= 60;
  return { hue, saturation };
}

const hueDistance = (a: number, b: number) => Math.min(Math.abs(a - b), 360 - Math.abs(a - b));

/** Colours that tell a state (runs, checks, deletions, warnings) and must keep it in the dark scheme. */
const statusColors = ['red', 'green', 'teal', 'orange', 'yellow', 'blue', 'violet', 'grape', 'cyan', 'amber'];

describe('colours', () => {
  const full = mergeMantineTheme(DEFAULT_THEME, theme);

  it('avatars carry white letters at 4.5:1', () => {
    for (const c of avatarColors) {
      const [name, shade] = c.split('.');
      expect(contrast(full.colors[name][Number(shade)], '#ffffff'), c).toBeGreaterThanOrEqual(4.5);
    }
  });

  it('filled colours, links and light variants keep 4.5:1 in every product', () => {
    for (const product of [undefined, 'violet', 'leather', 'navy', 'orange']) {
      const t = mergeMantineTheme(DEFAULT_THEME, themeFor(product));
      const { light, dark } = accessibleColors(t);
      const white = '#ffffff';
      // Primary buttons: white text on the primary filled colour, in both schemes.
      for (const [scheme, vars] of [
        ['light', light],
        ['dark', dark],
      ] as const) {
        const filled = vars['--mantine-primary-color-filled'];
        // autoContrast decides by the light scheme's shade, in both schemes.
        const lightShade = typeof t.primaryShade === 'number' ? t.primaryShade : t.primaryShade.light;
        const text =
          luminance(t.colors[t.primaryColor][lightShade]) < (t.luminanceThreshold ?? 0.3) ? white : t.black;
        expect(contrast(filled, text), `${product} ${scheme} primary`).toBeGreaterThanOrEqual(4.5);
      }
      // Links also on the light grey surfaces (hovered rows, heads of cards).
      for (const surface of [white, t.colors.gray[0], t.colors.gray[1]]) {
        expect(contrast(light['--mantine-color-anchor'], surface), `${product} links`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
      for (const [name, shades] of Object.entries(t.colors)) {
        const color = light[`--mantine-color-${name}-light-color`];
        expect(contrast(color, shades[1]), `${product} ${name} light`).toBeGreaterThanOrEqual(4.5);
      }
      expect(
        contrast(dark['--mantine-color-dimmed'], t.colors.dark[5]),
        `${product} dimmed`,
      ).toBeGreaterThanOrEqual(4.5);
      expect(
        contrast(dark['--mantine-color-anchor'], t.colors.dark[5]),
        `${product} dark links`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  describe('the dark scheme', () => {
    for (const product of [undefined, 'violet', 'leather', 'navy', 'orange']) {
      const t = mergeMantineTheme(DEFAULT_THEME, themeFor(product));
      const { dark } = accessibleColors(t);
      const surfaces = darkSurfaces(t);
      const body = t.colors.dark[7];

      it(`tints are lighter than the page and the surface they lie on (${product ?? 'platform'})`, () => {
        for (const name of Object.keys(t.colors)) {
          for (const tint of [
            dark[`--mantine-color-${name}-light`],
            dark[`--mantine-color-${name}-light-hover`],
          ]) {
            expect(tint, name).toBeDefined();
            expect(luminance(over(tint, body)), `${name} on the page`).toBeGreaterThan(luminance(body));
            for (const surface of surfaces) {
              expect(luminance(over(tint, surface)), `${name} on ${surface}`).toBeGreaterThanOrEqual(
                luminance(surface),
              );
            }
          }
        }
      });

      it(`text on tints reads at 4.5:1 on every surface (${product ?? 'platform'})`, () => {
        for (const name of Object.keys(t.colors)) {
          const color = dark[`--mantine-color-${name}-light-color`];
          for (const surface of surfaces) {
            for (const tint of [
              dark[`--mantine-color-${name}-light`],
              dark[`--mantine-color-${name}-light-hover`],
            ]) {
              expect(contrast(color, over(tint, surface)), `${name} on ${surface}`).toBeGreaterThanOrEqual(
                4.5,
              );
            }
          }
        }
      });

      it(`status colours keep their hue and 3:1 on the surfaces (${product ?? 'platform'})`, () => {
        for (const name of statusColors) {
          const color = dark[`--mantine-color-${name}-light-color`];
          const own = hueOf(t.colors[name][6]);
          const shown = hueOf(color);
          expect(hueDistance(own.hue, shown.hue), `${name} hue`).toBeLessThanOrEqual(12);
          // Not washed out to white (Mantine's default is the palest shade).
          expect(shown.saturation, `${name} saturation`).toBeGreaterThanOrEqual(0.5);
          expect(color.toLowerCase(), name).not.toBe(t.colors[name][0].toLowerCase());
          for (const surface of surfaces) {
            expect(contrast(color, surface), `${name} on ${surface}`).toBeGreaterThanOrEqual(3);
          }
        }
      });

      it(`disabled buttons keep a visible shape on cards (${product ?? 'platform'})`, () => {
        const card = t.colors.dark[6];
        expect(luminance(dark['--mantine-color-disabled'])).toBeGreaterThan(luminance(card));
        expect(contrast(dark['--mantine-color-disabled-border'], card)).toBeGreaterThan(
          contrast(dark['--mantine-color-disabled'], card),
        );
        expect(
          contrast(dark['--mantine-color-disabled-color'], dark['--mantine-color-disabled']),
        ).toBeGreaterThan(2.5);
      });
    }
  });
});
