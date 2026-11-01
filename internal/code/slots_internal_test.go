// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import "time"

// HoldHighlightTurns takes every turn of highlighting and shortens the wait for one, as if the node highlighted
// many code blocks (for tests of package code_test); the function it returns gives the turns back.
func HoldHighlightTurns(wait time.Duration) func() {
	before := highlightWait
	highlightWait = wait
	for range cap(highlightSlots) {
		highlightSlots <- struct{}{}
	}
	return func() {
		for range cap(highlightSlots) {
			<-highlightSlots
		}
		highlightWait = before
	}
}
