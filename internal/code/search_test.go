// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestCodeSearch: the lines of a repository's files at a ref that contain a text or match an expression, by file;
// limited to a path; refused for queries that are too short or invalid; invisible repositories stay invisible.
func TestCodeSearch(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SRCH", "name": "Search"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SRCH/repos", map[string]any{"name": "shop", "readme": true}, nil)
	for path, content := range map[string]string{
		"src/cart.go":  "package src\n\n// Checkout pays the cart.\nfunc Checkout() error { return pay() }\n",
		"src/pay.go":   "package src\n\nfunc pay() error { return nil }\n",
		"docs/faq.md":  "# FAQ\n\nHow do I pay?\n",
		"img/logo.bin": "PNG\x00\x01pay",
		"data/fifty":   strings.Repeat("row\n", 50),
		"data/many":    strings.Repeat("row\n", 60),
		"data/aaa":     strings.Repeat("a", 40) + "b\n",
	} {
		admin.MustDo(http.MethodPut, "/api/v1/code/repos/SRCH/shop/files", map[string]any{"branch": "main", "path": path, "content": content}, nil)
	}
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/SRCH/shop/files", map[string]any{"branch": "main", "newBranch": "refunds",
		"path": "src/refund.go", "content": "package src\n\nfunc Refund() error { return pay() }\n"}, nil)

	type result struct {
		Ref   string `json:"ref"`
		Files []struct {
			Path  string `json:"path"`
			Lines []struct {
				Line int    `json:"line"`
				Text string `json:"text"`
			} `json:"lines"`
			More bool `json:"more"`
		} `json:"files"`
		Matches  int  `json:"matches"`
		Complete bool `json:"complete"`
	}
	search := func(query string) (result, string) {
		t.Helper()
		var res result
		r := admin.Do(http.MethodGet, "/api/v1/code/repos/SRCH/shop/search?"+query, nil)
		if r.Status == http.StatusOK {
			r.Decode(&res)
		}
		var parts []string
		for _, f := range res.Files {
			for _, l := range f.Lines {
				parts = append(parts, f.Path+":"+strconv.Itoa(l.Line))
			}
		}
		return res, strings.Join(parts, " ") + "|" + r.Code()
	}
	q := func(v url.Values) string { return v.Encode() }
	if res, got := search(q(url.Values{"q": {"pay"}})); !strings.Contains(got, "src/cart.go:3") || !strings.Contains(got, "src/cart.go:4") ||
		!strings.Contains(got, "src/pay.go:3") || !strings.Contains(got, "docs/faq.md:3") || strings.Contains(got, "logo.bin") ||
		strings.Contains(got, "refund") || res.Ref != "main" || !res.Complete {
		t.Errorf("text: %s %+v", got, res)
	}
	// At most 50 lines per file; a file with more says so.
	if res, _ := search(q(url.Values{"q": {"row"}})); len(res.Files) != 2 || res.Matches != 100 || !res.Complete ||
		res.Files[0].Path != "data/fifty" || len(res.Files[0].Lines) != 50 || res.Files[0].More ||
		res.Files[1].Path != "data/many" || len(res.Files[1].Lines) != 50 || !res.Files[1].More {
		t.Errorf("per file: %+v", res)
	}
	if _, got := search(q(url.Values{"q": {"Pay"}, "case": {"true"}})); got != "|" {
		t.Errorf("case-sensitive: %s", got)
	}
	if _, got := search(q(url.Values{"q": {`func [A-Z][a-z]+\(`}, "regex": {"true"}, "case": {"true"}})); got != "src/cart.go:4|" {
		t.Errorf("expression: %s", got)
	}
	if _, got := search(q(url.Values{"q": {"pay"}, "path": {"docs"}})); got != "docs/faq.md:3|" {
		t.Errorf("below docs: %s", got)
	}
	if res, got := search(q(url.Values{"q": {"Refund"}, "ref": {"refunds"}})); got != "src/refund.go:3|" || res.Ref != "refunds" {
		t.Errorf("another branch: %s", got)
	}
	for v, code := range map[string]string{
		q(url.Values{"q": {"p"}}):                              "search.query",
		q(url.Values{"q": {"a(b"}, "regex": {"true"}}):         "search.expression",
		q(url.Values{"q": {"(a+)+$"}, "regex": {"true"}}):      "search.unsearchable", // too many steps
		q(url.Values{"q": {"pay"}, "path": {"../etc"}}):        "file.path",
		q(url.Values{"q": {"pay"}, "ref": {"no-such-branch"}}): "ref.not-found",
	} {
		if _, got := search(v); !strings.HasSuffix(got, "|"+code) {
			t.Errorf("%s: %s, want %s", v, got, code)
		}
	}
	// What is wrong with an expression is said at the field.
	var problem struct {
		Errors []struct {
			Location string `json:"location"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	admin.Do(http.MethodGet, "/api/v1/code/repos/SRCH/shop/search?"+q(url.Values{"q": {"a(b"}, "regex": {"true"}}), nil).Decode(&problem)
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "query.q" || problem.Errors[0].Message != "missing closing ): `a(b`" {
		t.Errorf("expression problem: %+v", problem)
	}
	// Who may not read the repository cannot tell it from one that does not exist.
	outsider := env.CreateUser("outsider")
	r1 := outsider.Do(http.MethodGet, "/api/v1/code/repos/SRCH/shop/search?q=pay", nil)
	r2 := outsider.Do(http.MethodGet, "/api/v1/code/repos/SRCH/nothing/search?q=pay", nil)
	if r1.Status != http.StatusNotFound || r1.Status != r2.Status || r1.Code() != r2.Code() {
		t.Errorf("outsider: %d %s, nothing: %d %s", r1.Status, r1.Code(), r2.Status, r2.Code())
	}
}
