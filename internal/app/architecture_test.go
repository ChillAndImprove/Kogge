// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "kogge.de/kogge/"

// The rules of the architecture: modules never import each other (cross-module code lives in links, the importer
// and the composition root); parsers and models are pure.
func TestArchitecture(t *testing.T) {
	root := filepath.Join("..", "..")
	imports := map[string][]string{} // package path → imports
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		pkg := filepath.ToSlash(rel)
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			imports[pkg] = append(imports[pkg], p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(imports) < 10 {
		t.Fatalf("only %d packages found", len(imports))
	}
	modules := []string{"internal/issues", "internal/wiki", "internal/code", "internal/builds"}
	moduleOf := func(pkg string) string {
		for _, m := range modules {
			if pkg == m || strings.HasPrefix(pkg, m+"/") {
				return m
			}
		}
		return ""
	}
	pure := []string{"internal/docmodel", "internal/docmodel/prosemirror", "internal/docmodel/pdf", "internal/issues/kql", "internal/code/gitdiff", "internal/builds/spec", "internal/builds/protocol"}
	impure := []string{"github.com/jackc/pgx", "net/http", "os", "os/exec", "database/sql", "net"}
	for pkg, list := range imports {
		own := moduleOf(pkg)
		for _, imp := range list {
			if !strings.HasPrefix(imp, module) {
				continue
			}
			target := strings.TrimPrefix(imp, module)
			if other := moduleOf(target); own != "" && other != "" && other != own {
				t.Errorf("%s imports %s: modules must not import each other (use internal/links)", pkg, target)
			}
		}
		for _, p := range pure {
			if pkg != p {
				continue
			}
			for _, imp := range list {
				for _, bad := range impure {
					if imp == bad || (bad != "net" && bad != "os" && strings.HasPrefix(imp, bad+"/")) {
						t.Errorf("%s imports %s: it must stay pure", pkg, imp)
					}
				}
			}
		}
	}
}
