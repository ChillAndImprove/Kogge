// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { createContext } from 'react';
import type { DocContext } from './DocView';

/**
 * Renders macros in the editor as readers see them. The editor of wiki pages provides the page's context, so that
 * a macro shows its output while the page is written; elsewhere, macros show their parameters only.
 */
export const MacroRender = createContext<DocContext['macro'] | undefined>(undefined);
