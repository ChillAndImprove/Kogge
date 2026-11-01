// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package apperr

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
)

// helpers are functions that pass a message on to an error: the index of the message among their arguments.
var helpers = map[string]int{
	"queryError":   1, // issues: KQL errors
	"invalidField": 1, // issues: the hint of an invalid field
	"uniqueErr":    1, // issues: a name that is taken
}

// packageHelpers pass messages on in one package (directory below internal): the parser of the issue query
// language words its errors with newError, and errorf adds what it found.
var packageHelpers = map[string]map[string]int{
	filepath.Join("issues", "kql"): {"newError": 1, "errorf": 0},
}

// otherMessages reach errors as the text of errors of other packages.
var otherMessages = []string{
	"An internal error occurred.",
	"The request took too long and was stopped. Try again later, or ask for less at a time.",
	"The password must have at least 12 characters.",
	"The password is too long.",
	"The password must not contain the user name.",
}

// messages returns the messages of errors in the code below dir (literal strings only).
func messages(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, filepath.Dir(path))
		local := packageHelpers[rel]
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			index := -1
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "apperr" {
					switch fn.Sel.Name {
					case "Msg":
						index = 0
					case "New":
						index = 2
					case "Invalid", "Invalidf", "BadRequest", "Unauthorized", "Forbidden", "NotFound", "Conflict",
						"PreconditionFailed", "TooManyRequests", "Text":
						index = 1
					}
				}
			case *ast.Ident:
				if i, ok := helpers[fn.Name]; ok {
					index = i
				}
				if i, ok := local[fn.Name]; ok {
					index = i
				}
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if i, ok := local[sel.Sel.Name]; ok {
					index = i // methods such as p.errorf
				}
			}
			if index < 0 || index >= len(call.Args) {
				return true
			}
			if s, ok := literal(call.Args[index]); ok && s != "%s" && s != "" {
				out[s] = fset.Position(call.Pos()).String()
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range otherMessages {
		out[m] = "(other packages)"
	}
	return out
}

// literal returns the text of a string literal, also of literals joined with + (long messages are split so).
func literal(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		x, ok := literal(v.X)
		if !ok {
			return "", false
		}
		y, ok := literal(v.Y)
		return x + y, ok
	case *ast.ParenExpr:
		return literal(v.X)
	}
	return "", false
}

var verbs = regexp.MustCompile(`%(\[\d+\])?[-+# 0]*\d*(\.\d+)?[a-zA-Z%]`)

// samples are values for the verbs of a message, each one recognisable in the result.
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

// TestMessagesDE: every message of an error in the code has a German wording, which takes the same values.
func TestMessagesDE(t *testing.T) {
	found := messages(t, filepath.Join("..", ".."))
	if len(found) < 400 {
		t.Fatalf("only %d messages found: is the scan broken?", len(found))
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
			t.Errorf("the English message %q does not format: %s", msg, en)
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
		if strings.TrimSpace(de) == "" || (de == msg && len(msg) > 12) {
			t.Errorf("%q is not translated", msg)
		}
	}
	slices.Sort(missing)
	// KOGGE_MISSING_MESSAGES=file writes the missing messages as JSON, to fill in their German wording.
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
		t.Errorf("%d messages have no German wording (internal/platform/apperr/messages_de.json):\n%s", len(missing),
			strings.Join(missing, "\n"))
	}
}

func TestLocalize(t *testing.T) {
	if got := Language("de-DE,de;q=0.9,en;q=0.8"); got != "de" {
		t.Errorf("German first: %s", got)
	}
	if got := Language("fr-FR,fr;q=0.9,en;q=0.8,de;q=0.7"); got != "en" {
		t.Errorf("English before German: %s", got)
	}
	if got := Language(""); got != "en" {
		t.Errorf("nothing: %s", got)
	}
	e := NotFound("branch.not-found", "The branch %s does not exist.", "main")
	if got := e.Localize("de").Detail; got != "Der Branch main existiert nicht." {
		t.Errorf("German: %q", got)
	}
	if got := e.Localize("en").Detail; got != "The branch main does not exist." || e.Detail != "The branch main does not exist." {
		t.Errorf("English: %q (the error itself must not change: %q)", got, e.Detail)
	}
	if got := Invalid("x", "Not a known message.").Localize("de").Detail; got != "Not a known message." {
		t.Errorf("without a translation: %q", got)
	}
	// Values and field messages that are messages themselves.
	f := Invalidf("issue.field-invalid", "The value of %s is not valid: %s", "duedate", "expected a date as YYYY-MM-DD").
		WithFields(FieldError{Location: "body.fields.duedate", Message: "expected a date as YYYY-MM-DD"})
	de := f.Localize("de")
	if !strings.Contains(de.Detail, "duedate") || strings.Contains(de.Detail, "expected") || strings.Contains(de.Errors[0].Message, "expected") {
		t.Errorf("hint: %q, %q", de.Detail, de.Errors[0].Message)
	}
	if f.Errors[0].Message != "expected a date as YYYY-MM-DD" {
		t.Errorf("the error itself changed: %q", f.Errors[0].Message)
	}
	// Reasons given as messages are worded one by one, with their values.
	reasons := Messages{Msg("The pull request is a draft."), Msg("%d open tasks.", 2), Msg("Not a known message.")}
	m := Conflict("pull-request.checks", "The pull request cannot be merged: %s", reasons)
	if m.Detail != "The pull request cannot be merged: The pull request is a draft. 2 open tasks. Not a known message." {
		t.Errorf("English reasons: %q", m.Detail)
	}
	if got := m.Localize("de").Detail; got != "Der Pull-Request kann nicht gemergt werden: Der Pull-Request ist ein Entwurf. 2 offene Aufgaben. "+
		"Not a known message." {
		t.Errorf("German reasons: %q", got)
	}
}
