// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Component, type ErrorInfo, type ReactNode } from 'react';

interface Props {
  /** Shown instead of the children after an error. */
  fallback: ReactNode;
  /** When it changes, the children are tried again (e.g. the parameters of a macro). */
  resetKey?: unknown;
  children: ReactNode;
}

/** Keeps an error in a part of a page (a macro, a diagram) from taking the whole page down. */
export class ErrorBoundary extends Component<Props, { error: Error | null }> {
  state: { error: Error | null } = { error: null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(error, info.componentStack);
  }

  componentDidUpdate(prev: Props) {
    if (this.state.error && prev.resetKey !== this.props.resetKey) this.setState({ error: null });
  }

  render() {
    return this.state.error ? this.props.fallback : this.props.children;
  }
}
