// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestHighlightAPI: snippets are highlighted for signed-in users only, a few at a time on each node; who finds
// every turn taken for a few seconds hears that the server is busy, and when to try again.
func TestHighlightAPI(t *testing.T) {
	env := apitest.New(t)
	lena := env.CreateUser("lena")
	snippet := map[string]any{"language": "go", "text": "func main() {}"}
	if r := env.Anonymous().Do(http.MethodPost, "/api/v1/code/highlight", snippet); r.Status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d %s", r.Status, r.Body)
	}
	var out struct {
		Language string `json:"language"`
		Lines    [][]struct {
			C string `json:"c"`
			T string `json:"t"`
		} `json:"lines"`
	}
	lena.MustDo(http.MethodPost, "/api/v1/code/highlight", snippet, &out)
	if out.Language != "Go" || len(out.Lines) != 1 || out.Lines[0][0].T != "func" {
		t.Fatalf("highlighted: %+v", out)
	}

	release := sync.OnceFunc(code.HoldHighlightTurns(200 * time.Millisecond))
	defer release()
	start := time.Now()
	r := lena.Do(http.MethodPost, "/api/v1/code/highlight", snippet, "Accept-Language", "de")
	if r.Status != http.StatusServiceUnavailable || r.Code() != "code.highlight-busy" || r.Header.Get("Retry-After") != "5" ||
		!strings.Contains(string(r.Body), "Gerade werden viele Codeblöcke hervorgehoben") {
		t.Fatalf("busy: %d %v %s", r.Status, r.Header, r.Body)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond || waited > 10*time.Second {
		t.Errorf("waited %v for a turn", waited)
	}
	release()
	lena.MustDo(http.MethodPost, "/api/v1/code/highlight", snippet, &out)
}
