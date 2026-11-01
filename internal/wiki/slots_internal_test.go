// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import "time"

// HoldPDFTurns takes every turn of rendering PDFs and shortens the wait for one, as if the server rendered many
// PDFs (for tests of package wiki_test); the function it returns gives the turns back.
func HoldPDFTurns(wait time.Duration) func() {
	before := pdfWait
	pdfWait = wait
	for range cap(pdfSlots) {
		pdfSlots <- struct{}{}
	}
	return func() {
		for range cap(pdfSlots) {
			<-pdfSlots
		}
		pdfWait = before
	}
}
