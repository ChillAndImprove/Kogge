// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package crypto

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	kr, err := ParseKeyring(GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := kr.Seal([]byte("s3cret"), "smtp:password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:"+kr.CurrentKeyID()+":") || strings.Contains(sealed, "s3cret") {
		t.Fatalf("unexpected format: %s", sealed)
	}
	plain, err := kr.Open(sealed, "smtp:password")
	if err != nil || string(plain) != "s3cret" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := kr.Open(sealed, "webhook:1:secret"); err == nil {
		t.Fatal("a ciphertext must not open with another record's binding")
	}
	other, _ := ParseKeyring(GenerateKey())
	if _, err := other.Open(sealed, "smtp:password"); err == nil {
		t.Fatal("a ciphertext must not open with another key")
	}
}

func TestKeyRotation(t *testing.T) {
	oldKey, newKey := GenerateKey(), GenerateKey()
	before, _ := ParseKeyring(oldKey)
	sealed, _ := before.Seal([]byte("value"), "a")
	after, err := ParseKeyring("# rotated\n" + newKey + "\n" + oldKey + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentKeyID() == before.CurrentKeyID() {
		t.Fatal("the first key must be current")
	}
	if p, err := after.Open(sealed, "a"); err != nil || string(p) != "value" {
		t.Fatalf("old values must stay readable: %v", err)
	}
	if KeyID(sealed) != before.CurrentKeyID() {
		t.Fatal("KeyID")
	}
}

// TestMAC: keyed hashes depend on the master key and the purpose, not only on the data, and are recognised under
// every key of the keyring after a rotation.
func TestMAC(t *testing.T) {
	oldKey, newKey := GenerateKey(), GenerateKey()
	before, _ := ParseKeyring(oldKey)
	after, _ := ParseKeyring(newKey + "\n" + oldKey + "\n")
	data := []byte("abcd2345")
	mac := before.MAC("recovery-code", data)
	plain := HashToken(string(data))
	if len(mac) != 32 || bytes.Equal(mac, plain) || !bytes.Equal(mac, before.MAC("recovery-code", data)) {
		t.Fatalf("MAC %x (plain hash %x)", mac, plain)
	}
	if bytes.Equal(mac, before.MAC("other", data)) || bytes.Equal(mac, before.MAC("recovery-code", []byte("abcd2346"))) {
		t.Error("the MAC does not depend on the purpose or the data")
	}
	if bytes.Equal(mac, after.MAC("recovery-code", data)) {
		t.Error("the MAC does not depend on the key")
	}
	if all := after.MACs("recovery-code", data); len(all) != 2 || !bytes.Equal(all[0], after.MAC("recovery-code", data)) ||
		!bytes.Equal(all[1], mac) {
		t.Errorf("MACs after a rotation: %x", all)
	}
}

func TestParseKeyringRejectsBadKeys(t *testing.T) {
	for _, in := range []string{"", "# only a comment", "not-base64", "c2hvcnQ="} {
		if _, err := ParseKeyring(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestLoadOrCreateKeyring(t *testing.T) {
	dir := t.TempDir()
	kr, created, err := LoadOrCreateKeyring("", dir)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	st, _ := os.Stat(filepath.Join(dir, "master.key"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("permissions %v", st.Mode().Perm())
	}
	again, created, err := LoadOrCreateKeyring("", dir)
	if err != nil || created || again.CurrentKeyID() != kr.CurrentKeyID() {
		t.Fatalf("reload: %v %v", created, err)
	}
}

// TestNodesAgreeOnTheMasterKey: nodes that start for the first time together on one data directory use one key.
func TestNodesAgreeOnTheMasterKey(t *testing.T) {
	dir := t.TempDir()
	ids := make([]string, 8)
	done := make(chan struct{})
	for i := range ids {
		go func() {
			defer func() { done <- struct{}{} }()
			kr, _, err := LoadOrCreateKeyring("", dir)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = kr.CurrentKeyID()
		}()
	}
	for range ids {
		<-done
	}
	for i, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("node %d uses key %q, node 0 key %q", i, id, ids[0])
		}
	}
}

func TestTokens(t *testing.T) {
	a, ha := NewToken(PrefixAccessToken)
	b, _ := NewToken(PrefixAccessToken)
	if a == b || !strings.HasPrefix(a, "kgp_") || len(a) < 50 {
		t.Fatalf("tokens: %s %s", a, b)
	}
	if string(HashToken(a)) != string(ha) {
		t.Fatal("hash mismatch")
	}
	if h := TokenHint(a); !strings.HasPrefix(h, "kgp_") || strings.Contains(h, a[10:40]) {
		t.Fatalf("hint reveals too much: %s", h)
	}
}

func TestPasswords(t *testing.T) {
	ctx := context.Background()
	weak := NewPasswordHasher(Argon2Params{Memory: 1024, Time: 1, Threads: 1}, 1)
	strong := NewPasswordHasher(Argon2Params{Memory: 2048, Time: 1, Threads: 1}, 1)
	h, err := weak.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash, err := weak.Verify(ctx, "correct horse battery", h)
	if err != nil || !ok || rehash {
		t.Fatalf("verify: %v %v %v", ok, rehash, err)
	}
	if ok, _, _ := weak.Verify(ctx, "wrong", h); ok {
		t.Fatal("wrong password accepted")
	}
	if _, rehash, _ := strong.Verify(ctx, "correct horse battery", h); !rehash {
		t.Fatal("weaker parameters must ask for a rehash")
	}
	if _, _, err := weak.Verify(ctx, "x", "$2a$10$bcrypt"); err == nil {
		t.Fatal("foreign hash formats must be rejected")
	}
}

func TestValidatePassword(t *testing.T) {
	cases := map[string]bool{"short": false, "exactly12chr": true, "my-anna-password": false, "Lange-genug-123": true}
	for pw, ok := range cases {
		if err := ValidatePassword(pw, "anna"); (err == nil) != ok {
			t.Errorf("%q: %v", pw, err)
		}
	}
}
