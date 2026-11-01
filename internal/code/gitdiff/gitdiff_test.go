// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package gitdiff

import "testing"

const patch = `diff --git a/main.go b/main.go
index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222 100644
--- a/main.go
+++ b/main.go
@@ -1,4 +1,5 @@ package main
 package main

-func a() {}
+func a() { b() }
+func b() {}
 // end
\ No newline at end of file
diff --git a/my file.txt b/my file.txt
new file mode 100644
index 0000000000000000000000000000000000000000..3333333333333333333333333333333333333333
--- /dev/null
+++ b/my file.txt
@@ -0,0 +1 @@
+hello
diff --git a/old.txt b/new.txt
similarity index 90%
rename from old.txt
rename to new.txt
index 4444444444444444444444444444444444444444..5555555555555555555555555555555555555555 100644
--- a/old.txt
+++ b/new.txt
@@ -3 +3 @@
-x
+y
diff --git "a/tab\there.bin" "b/tab\there.bin"
index 6666666666666666666666666666666666666666..7777777777777777777777777777777777777777 100644
Binary files "a/tab\there.bin" and "b/tab\there.bin" differ
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 8888888888888888888888888888888888888888..0000000000000000000000000000000000000000
--- a/gone.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-one
-two
diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755
`

func TestParse(t *testing.T) {
	d := Parse([]byte(patch), Limits{})
	if len(d.Files) != 6 || d.Truncated {
		t.Fatalf("files = %d", len(d.Files))
	}
	f := d.Files[0]
	if f.NewPath != "main.go" || f.Status != Modified || f.Additions != 2 || f.Deletions != 1 || len(f.Hunks) != 1 {
		t.Errorf("main.go = %+v", f)
	}
	h := f.Hunks[0]
	if h.OldStart != 1 || h.OldLines != 4 || h.NewStart != 1 || h.NewLines != 5 || h.Header != "package main" || len(h.Lines) != 6 {
		t.Errorf("hunk = %+v", h)
	}
	if l := h.Lines[3]; l.Kind != Addition || l.New != 3 || l.Old != 0 || l.Text != "func a() { b() }" {
		t.Errorf("line = %+v", l)
	}
	if l := h.Lines[5]; l.Kind != Context || l.Old != 4 || l.New != 5 || !l.NoNewline {
		t.Errorf("last line = %+v", l)
	}
	if l := h.Lines[1]; l.Kind != Context || l.Text != "" {
		t.Errorf("empty context line = %+v", l)
	}
	if f := d.Files[1]; f.NewPath != "my file.txt" || f.OldPath != "" || f.Status != Added || f.Additions != 1 {
		t.Errorf("added = %+v", f)
	}
	if f := d.Files[2]; f.OldPath != "old.txt" || f.NewPath != "new.txt" || f.Status != Renamed || f.Similarity != 90 ||
		f.Hunks[0].Lines[0].Old != 3 || f.Hunks[0].Lines[1].New != 3 {
		t.Errorf("renamed = %+v", f)
	}
	if f := d.Files[3]; !f.Binary || f.NewPath != "tab\there.bin" {
		t.Errorf("binary = %+v", f)
	}
	if f := d.Files[4]; f.Status != Deleted || f.OldPath != "gone.txt" || f.NewPath != "" || f.Deletions != 2 {
		t.Errorf("deleted = %+v", f)
	}
	if f := d.Files[5]; f.OldMode != "100644" || f.NewMode != "100755" || len(f.Hunks) != 0 {
		t.Errorf("mode change = %+v", f)
	}
}

func TestLimits(t *testing.T) {
	d := Parse([]byte(patch), Limits{Files: 2, LinesPerFile: 2})
	if len(d.Files) != 2 || !d.Truncated {
		t.Fatalf("files = %d, truncated %v", len(d.Files), d.Truncated)
	}
	if f := d.Files[0]; !f.Truncated || len(f.Hunks[0].Lines) != 2 || f.Additions != 2 {
		t.Errorf("truncated file = %+v", f)
	}
}

func TestSplitHeader(t *testing.T) {
	for in, want := range map[string][2]string{
		"a/x b/x":                           {"x", "x"},
		"a/with space b/with space":         {"with space", "with space"},
		`"a/\303\244.txt" "b/\303\244.txt"`: {"ä.txt", "ä.txt"},
		"a/x b/y":                           {"x", "y"},
	} {
		o, n := splitHeader(in)
		if o != want[0] || n != want[1] {
			t.Errorf("%q: %q %q", in, o, n)
		}
	}
}
