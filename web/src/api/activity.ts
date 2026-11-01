// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/**
 * The user's activity in the page: pointer, keyboard, wheel and touch input, and the tab coming to the front. The
 * requests that the application makes while its user is idle — refreshing lists, polling — say so (header
 * X-Kogge-Background), so that the server does not keep an unattended session alive: its idle timeout ends it as
 * if the browser were closed.
 */

/** How long without input counts as idle. */
export const idleAfter = 5 * 60_000;

/** The header that marks requests made while the user is idle. */
export const backgroundHeader = 'X-Kogge-Background';

let lastActivity = Date.now();

/** Notes that the user did something now. */
export function noteActivity(now = Date.now()) {
  lastActivity = now;
}

/** Whether the user has done nothing in the page for a while. */
export function idle(now = Date.now()): boolean {
  return now - lastActivity > idleAfter;
}

if (typeof window !== 'undefined') {
  for (const type of ['pointerdown', 'pointermove', 'keydown', 'wheel', 'touchstart'] as const) {
    window.addEventListener(type, () => noteActivity(), { capture: true, passive: true });
  }
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') noteActivity();
  });
}
