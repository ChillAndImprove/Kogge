// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package docmodel

import (
	"slices"
	"testing"

	"kogge.de/kogge/internal/docmodel/prosemirror"
)

// TestEditorSchema: what documents may hold, the editor's schema knows too. Shared drafts apply changes by the
// editor's schema (prosemirror.Editor, generated from the web application's editor: KOGGE_UPDATE_SCHEMA=1 with
// web/src/components/editor/schema.test.ts writes it) and are published by the rules of documents; a type only one
// of them knows breaks the drafts of every page that has it.
func TestEditorSchema(t *testing.T) {
	for name, spec := range nodes {
		nt := prosemirror.Editor.NodeType(name)
		if nt == nil {
			t.Errorf("the editor's schema lacks the node type %s", name)
			continue
		}
		if nt.Inline != spec.inline || nt.Leaf != spec.leaf {
			t.Errorf("%s: inline %v and leaf %v in the editor, %v and %v in documents", name, nt.Inline, nt.Leaf, spec.inline, spec.leaf)
		}
		for attr := range spec.attrs {
			if !slices.ContainsFunc(nt.Attrs, func(a prosemirror.Attr) bool { return a.Name == attr }) {
				t.Errorf("%s: the editor's schema lacks the attribute %s", name, attr)
			}
		}
	}
	for name := range marks {
		if prosemirror.Editor.MarkType(name) == nil {
			t.Errorf("the editor's schema lacks the mark %s", name)
		}
	}
	// Formulas have their LaTeX, empty when the editor inserts one.
	for _, name := range []string{"math", "inlineMath"} {
		if nt := prosemirror.Editor.NodeType(name); nt != nil {
			if i := slices.IndexFunc(nt.Attrs, func(a prosemirror.Attr) bool { return a.Name == "latex" }); i < 0 ||
				!nt.Attrs[i].HasDefault || nt.Attrs[i].Default != "" {
				t.Errorf("%s: latex is %+v in the editor's schema, want the default \"\"", name, nt.Attrs)
			}
		}
	}
}
