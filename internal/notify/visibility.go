// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package notify

import (
	"context"

	"kogge.de/kogge/internal/identity"
)

// Visibility tells which entities of a type (by the ids notifications keep) a principal may still see.
type Visibility func(ctx context.Context, p *identity.Principal, ids []string) (map[string]bool, error)

// Visibilities are the checks of the modules by entity type. Notifications are written for those who may see
// their subject at the time; access can end later, so the inbox and the e-mails check again (audit L37).
// Notifications of types without a check count as visible.
type Visibilities map[string]Visibility

type subject struct{ typ, id string }

// visible returns which of the subjects the principal may still see.
func (v Visibilities) visible(ctx context.Context, p *identity.Principal, subjects []subject) (map[subject]bool, error) {
	byType := map[string][]string{}
	for _, s := range subjects {
		byType[s.typ] = append(byType[s.typ], s.id)
	}
	out := map[subject]bool{}
	for typ, ids := range byType {
		check, ok := v[typ]
		if !ok {
			for _, id := range ids {
				out[subject{typ, id}] = true
			}
			continue
		}
		seen, err := check(ctx, p, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			out[subject{typ, id}] = seen[id]
		}
	}
	return out, nil
}

// redact hides what notifications say about subjects the principal may no longer see.
func (v Visibilities) redact(ctx context.Context, p *identity.Principal, ns []Notification) error {
	subjects := make([]subject, len(ns))
	for i, n := range ns {
		subjects[i] = subject{n.EntityType, n.EntityID}
	}
	seen, err := v.visible(ctx, p, subjects)
	if err != nil {
		return err
	}
	for i := range ns {
		if !seen[subjects[i]] {
			ns[i].Hidden = true
			ns[i].Title, ns[i].Summary, ns[i].URL, ns[i].EntityID, ns[i].Data = "", "", "", "", map[string]any{}
		}
	}
	return nil
}
