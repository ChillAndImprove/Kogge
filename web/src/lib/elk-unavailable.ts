// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/**
 * Stands in for elkjs, Mermaid's optional layout engine, whose licence (EPL-2.0) Kogge's licence policy does not allow: the
 * build does not ship it (vite.config.ts), and Mermaid lays out diagrams that ask for it with its default layout.
 */
export default class ELK {
  layout(): Promise<never> {
    return Promise.reject(new Error('The ELK layout is not part of Kogge.'));
  }
}
