// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import de from './de';
import en, { type Resources } from './en';

declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'translation';
    resources: { translation: Resources };
  }
}

const initial = navigator.language?.toLowerCase().startsWith('de') ? 'de' : 'en';

void i18n.use(initReactI18next).init({
  resources: { de: { translation: de }, en: { translation: en } },
  lng: initial,
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
});

export function setLanguage(lang: string | undefined) {
  const next = lang === 'de' || lang === 'en' ? lang : initial;
  if (i18n.language !== next) void i18n.changeLanguage(next);
  document.documentElement.lang = next;
}

export default i18n;
