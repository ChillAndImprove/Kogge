// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package prosemirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

var strictRefusal = regexp.MustCompile(`^(Range \d+-\d+ outside of the document|Gap outside of the range|Position inside a surrogate pair)$`)

// hashOf is the hash the session generator computes: JSON with sorted keys, without HTML escapes.
func hashOf(t *testing.T, n *Node) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(n.JSON()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])[:16]
}

type sessionEntry struct {
	Steps []json.RawMessage `json:"steps"`
	Probe json.RawMessage   `json:"probe"`
	Hash  *string           `json:"hash"`
}

// TestSessions replays editing sessions recorded in the editor (web/src/components/editor/collab-vectors.test.ts):
// every transaction must give the editor's document, and broken steps must be refused where ProseMirror refuses
// them and give ProseMirror's document where it accepts them.
func TestSessions(t *testing.T) {
	data, err := os.ReadFile("testdata/sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Sessions []struct {
			Seed    int             `json:"seed"`
			Doc     json.RawMessage `json:"doc"`
			Entries []sessionEntry  `json:"entries"`
			Final   json.RawMessage `json:"final"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Sessions) == 0 {
		t.Fatal("no sessions")
	}
	steps, probes, strict := 0, 0, 0
	for _, s := range in.Sessions {
		doc, err := Editor.ParseNode(s.Doc)
		if err != nil {
			t.Fatalf("session %d: %v", s.Seed, err)
		}
		before := doc
	entries:
		for i, e := range s.Entries {
			if e.Probe == nil {
				before = doc
				for _, raw := range e.Steps {
					step, err := Editor.ParseStep(raw)
					if err == nil {
						// The step's JSON, as the server sends it on, is the same step.
						var again Step
						if again, err = roundTrip(step); err == nil {
							var a, b *Node
							a, err = Apply(doc, step)
							if b, _ = Apply(doc, again); err == nil && (b == nil || hashOf(t, a) != hashOf(t, b)) {
								t.Errorf("session %d, transaction %d: the JSON of %s is another step", s.Seed, i, raw)
							}
							doc = a
						}
					}
					if err != nil {
						t.Errorf("session %d, transaction %d: %s: %v", s.Seed, i, raw, err)
						break entries
					}
					steps++
				}
				if got := hashOf(t, doc); got != *e.Hash {
					t.Errorf("session %d, transaction %d: document differs from the editor's after %s", s.Seed, i, e.Steps)
					break
				}
				continue
			}
			probes++
			var got *string
			step, err := Editor.ParseStep(e.Probe)
			if err == nil {
				var out *Node
				if out, err = Apply(before, step); err == nil {
					h := hashOf(t, out)
					got = &h
				}
			}
			switch {
			case e.Hash == nil && got != nil:
				t.Errorf("session %d, probe %d: accepted a step ProseMirror refuses: %s", s.Seed, i, e.Probe)
			case e.Hash != nil && got == nil:
				// Stricter than ProseMirror on purpose: reversed ranges and gaps, and cuts through surrogate pairs
				// (ProseMirror accepts them with strange results). Editors never make such steps.
				if !strictRefusal.MatchString(err.Error()) {
					t.Errorf("session %d, probe %d: refused a step ProseMirror accepts (%v): %s", s.Seed, i, err, e.Probe)
				}
				strict++
			case e.Hash != nil && *got != *e.Hash:
				t.Errorf("session %d, probe %d: document differs from ProseMirror's: %s", s.Seed, i, e.Probe)
			}
		}
		final, err := Editor.ParseNode(s.Final)
		if err != nil {
			t.Fatal(err)
		}
		if hashOf(t, final) != hashOf(t, doc) {
			t.Errorf("session %d: final document differs", s.Seed)
		}
	}
	t.Logf("%d sessions, %d steps, %d probes (%d refused only here)", len(in.Sessions), steps, probes, strict)
}

func roundTrip(step Step) (Step, error) {
	raw, err := json.Marshal(step.JSON())
	if err != nil {
		return nil, err
	}
	return Editor.ParseStep(raw)
}
