// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"context"
	"time"
)

// WithoutPauses lets the extraction try again at once (for tests of answers that fail).
func (j *Jira) WithoutPauses() {
	j.client.pause = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
}

// WithoutPauses lets the extraction try again at once (for tests of answers that fail).
func (c *Confluence) WithoutPauses() {
	c.client.pause = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
}
