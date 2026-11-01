// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCutLog(t *testing.T) {
	marker := "\n[Kogge: the log reached its limit (Administration → Settings → Limits); the rest of it was not kept. The job goes on.]\n"
	for name, c := range map[string]struct {
		data string
		room int64
		want string
	}{
		"at the last line end that fits": {"one\ntwo\nthree\n", int64(len("one\ntwo\nthr") + len(marker)), "one\ntwo" + marker},
		"a line longer than the room":    {"a very long line without an end", int64(10 + len(marker)), marker},
		"no room for the note":           {"one\ntwo\n", 5, marker},
		"the whole chunk fits":           {"one\ntwo\n", int64(100 + len(marker)), "one\ntwo" + marker},
		"multi-byte text stays whole":    {"Größe\nÄnderung\n", int64(len("Größe\nÄnd") + len(marker)), "Größe" + marker},
	} {
		got := cutLog([]byte(c.data), c.room)
		if string(got) != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
		if !utf8.Valid(got) || strings.Count(string(got), "[Kogge:") != 1 {
			t.Errorf("%s: %q", name, got)
		}
	}
	// The chunk is not changed in place (it may be a buffer of the caller).
	data := []byte("one\ntwo\n")
	_ = cutLog(data, int64(len(marker)+5))
	if !bytes.Equal(data, []byte("one\ntwo\n")) {
		t.Errorf("the chunk changed: %q", data)
	}
}
