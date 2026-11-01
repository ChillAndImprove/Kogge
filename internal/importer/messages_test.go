// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// reportMessages returns the texts of the reports in the package's code: the literal formats of calls of t.
func reportMessages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "t" || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: the text of a report must be a literal (for its German wording)", fset.Position(call.Pos()))
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out[s] = fset.Position(call.Pos()).String()
			}
			return true
		})
	}
	return out
}

var verbs = regexp.MustCompile(`%(\[\d+\])?[-+# 0]*\d*(\.\d+)?[a-zA-Z%]`)

// samples are values for the verbs of a text, each one recognisable in the result.
func samples(message string) []any {
	var out []any
	for _, v := range verbs.FindAllString(message, -1) {
		switch v[len(v)-1] {
		case '%':
		case 'd':
			out = append(out, 7001+len(out))
		default:
			out = append(out, fmt.Sprintf("‹%d›", len(out)+1))
		}
	}
	return out
}

// TestMessagesDE: every text of the reports has a German wording, which takes the same values.
func TestMessagesDE(t *testing.T) {
	found := reportMessages(t)
	if len(found) < 200 {
		t.Fatalf("only %d texts found: is the scan broken?", len(found))
	}
	var missing []string
	for msg, where := range found {
		de, ok := catalogDE[msg]
		if !ok {
			missing = append(missing, fmt.Sprintf("%q (%s)", msg, where))
			continue
		}
		args := samples(msg)
		if en := fmt.Sprintf(msg, args...); strings.Contains(en, "%!") && len(args) > 0 {
			t.Errorf("the English text %q does not format: %s", msg, en)
			continue
		}
		got := de
		if len(args) > 0 {
			got = fmt.Sprintf(de, args...)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("%q: the German wording %q does not take the values: %s", msg, de, got)
		}
		for _, a := range args {
			if !strings.Contains(got, fmt.Sprint(a)) {
				t.Errorf("%q: the German wording %q leaves out a value: %s", msg, de, got)
			}
		}
		if strings.TrimSpace(de) == "" || (de == msg && len(msg) > 14) {
			t.Errorf("%q is not translated", msg)
		}
	}
	for msg := range catalogDE {
		if _, ok := found[msg]; !ok {
			t.Errorf("messages_de.json has %q, which the code does not use", msg)
		}
	}
	slices.Sort(missing)
	// KOGGE_MISSING_MESSAGES=file writes the missing texts as JSON, to fill in their German wording.
	if path := os.Getenv("KOGGE_MISSING_MESSAGES"); path != "" && len(missing) > 0 {
		todo := map[string]string{}
		for msg := range found {
			if _, ok := catalogDE[msg]; !ok {
				todo[msg] = ""
			}
		}
		data, _ := json.MarshalIndent(todo, "", "  ")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d texts have no German wording (internal/importer/messages_de.json):\n%s", len(missing), strings.Join(missing, "\n"))
	}
}

func TestLanguage(t *testing.T) {
	de, en := lang(German), lang(English)
	if got := de.n(1234567); got != "1.234.567" {
		t.Errorf("German number: %s", got)
	}
	if got := en.n(-1234); got != "-1,234" {
		t.Errorf("English number: %s", got)
	}
	if got := de.bytes(1536); got != "1,5 KiB" {
		t.Errorf("German size: %s", got)
	}
	if got := en.duration(90 * time.Minute); got != "1 h 30 min" {
		t.Errorf("duration: %s", got)
	}
	if got := de.t("Analysis of the %s migration", "Jira"); got != "Analyse der Migration von Jira" {
		t.Errorf("German text: %s", got)
	}
	if _, err := ParseLanguage("fr"); err == nil {
		t.Error("French accepted")
	}
}
