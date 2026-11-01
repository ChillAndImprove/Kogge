// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import { Loader, UnstyledButton } from '@mantine/core';
import { IconPencil } from '@tabler/icons-react';
import { useId, useState, type ReactNode } from 'react';

/** The labels and values of a details panel, in two columns (see .kogge-details). */
export function DetailsList({ children }: { children: ReactNode }) {
  return <dl className="kogge-details">{children}</dl>;
}

/**
 * Closes an editor of a DetailItem. With refocus (after Enter or Escape), the keyboard focus returns to the value;
 * after a click elsewhere it stays where the click went.
 */
export type DoneEditing = (refocus?: boolean) => void;

/**
 * One label and value of a details panel. Editable values read like text and turn into their editor on click or
 * Enter, so that a panel looks like information rather than a form. The editor saves and calls done; leaving it
 * (focus outside of it and its dropdowns) or Escape closes it too.
 */
export function DetailItem({
  label,
  editable = false,
  editor,
  busy = false,
  footer,
  children,
}: {
  label: string;
  editable?: boolean;
  editor?: (done: DoneEditing) => ReactNode;
  /** The value is being saved. */
  busy?: boolean;
  /** Below the value, e.g. a quick action. */
  footer?: ReactNode;
  children: ReactNode;
}) {
  const [editing, setEditing] = useState(false);
  const labelId = useId();
  const valueId = useId();
  const buttonId = useId();
  const done: DoneEditing = (refocus) => {
    setEditing(false);
    // The value's button exists again after the next render.
    if (refocus) requestAnimationFrame(() => document.getElementById(buttonId)?.focus());
  };
  let value: ReactNode;
  if (editing && editor) {
    value = (
      <div
        className="kogge-inline-editor"
        onBlur={(e) => {
          const next = e.relatedTarget as HTMLElement | null;
          // Dropdowns of selects and date pickers live in portals outside of the editor.
          if (next && (e.currentTarget.contains(next) || next.closest('[data-portal]'))) return;
          done(false);
        }}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && !e.defaultPrevented) {
            e.stopPropagation();
            done(true);
          }
        }}
      >
        {editor(done)}
      </div>
    );
  } else if (editable && editor) {
    value = (
      <UnstyledButton
        id={buttonId}
        className="kogge-inline"
        onClick={() => setEditing(true)}
        aria-labelledby={`${labelId} ${valueId}`}
      >
        <span id={valueId} className="kogge-inline-value">
          {children}
        </span>
        {busy ? (
          <Loader size={12} className="kogge-inline-icon" />
        ) : (
          <IconPencil size={14} className="kogge-inline-icon kogge-inline-pencil" aria-hidden />
        )}
      </UnstyledButton>
    );
  } else {
    value = <div className="kogge-inline-value kogge-inline-static">{children}</div>;
  }
  return (
    <>
      <dt id={labelId} className="kogge-details-label">
        {label}
      </dt>
      <dd className="kogge-details-value">
        {value}
        {footer}
      </dd>
    </>
  );
}
