// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/**
 * Emoji of documents by name: the emoticons of migrated Confluence pages (ac:emoticon) and common short names.
 * A name without a character here stays visible as :name:.
 */
const glyphs: Record<string, string> = {
  // Confluence's emoticons.
  smile: '🙂',
  sad: '🙁',
  cheeky: '😛',
  laugh: '😄',
  wink: '😉',
  'thumbs-up': '👍',
  'thumbs-down': '👎',
  information: 'ℹ️',
  tick: '✅',
  cross: '❌',
  warning: '⚠️',
  plus: '➕',
  minus: '➖',
  question: '❓',
  'light-on': '💡',
  'light-off': '💡',
  'yellow-star': '⭐',
  'red-star': '⭐',
  'green-star': '⭐',
  'blue-star': '⭐',
  heart: '❤️',
  'broken-heart': '💔',
  // Common short names.
  '+1': '👍',
  '-1': '👎',
  thumbsup: '👍',
  thumbsdown: '👎',
  slight_smile: '🙂',
  grinning: '😀',
  joy: '😂',
  thinking: '🤔',
  tada: '🎉',
  rocket: '🚀',
  fire: '🔥',
  eyes: '👀',
  bulb: '💡',
  star: '⭐',
  sparkles: '✨',
  white_check_mark: '✅',
  heavy_check_mark: '✔️',
  check: '✔️',
  x: '❌',
  bug: '🐛',
  memo: '📝',
  lock: '🔒',
  key: '🔑',
  wrench: '🔧',
  gear: '⚙️',
  calendar: '📅',
  pushpin: '📌',
  link: '🔗',
  construction: '🚧',
  rotating_light: '🚨',
  stop_sign: '🛑',
  clap: '👏',
  pray: '🙏',
  wave: '👋',
  coffee: '☕',
};

/** The character of an emoji, or null for a name Kogge does not know. */
export function emojiGlyph(name: string | null | undefined): string | null {
  if (!name) return null;
  return glyphs[name] ?? glyphs[name.toLowerCase().replace(/^:|:$/g, '')] ?? null;
}
