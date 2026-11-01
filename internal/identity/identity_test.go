// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net/netip"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestSafeReturnPath(t *testing.T) {
	cases := map[string]string{
		"":                      "/",
		"/browse/SHOP-1":        "/browse/SHOP-1",
		"https://evil.example":  "/",
		"//evil.example/x":      "/",
		"/\\evil.example":       "/",
		"/ok?x=1#frag":          "/ok?x=1#frag",
		"/bad\r\nSet-Cookie: x": "/",
		"javascript:alert(1)":   "/",
		"/\t/evil.example":      "/",
		"/\t\\evil.example":     "/",
		"/ok\\..\\x":            "/",
		"/%09/evil.example":     "/%09/evil.example",
	}
	for in, want := range cases {
		if got := SafeReturnPath(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// TestClientKey: the throttle counts IPv4 clients by their address and IPv6 clients by their /64 network.
func TestClientKey(t *testing.T) {
	key := func(s string) string { return clientKey(netip.MustParseAddr(s)) }
	if key("2001:db8:1:2::5") != key("2001:db8:1:2:ffff::9") || key("2001:db8:1:2::5") == key("2001:db8:1:3::5") {
		t.Errorf("IPv6 networks: %s %s %s", key("2001:db8:1:2::5"), key("2001:db8:1:2:ffff::9"), key("2001:db8:1:3::5"))
	}
	if key("::ffff:192.0.2.7") != "192.0.2.7" || key("192.0.2.7") == key("192.0.2.8") {
		t.Errorf("IPv4: %s %s", key("::ffff:192.0.2.7"), key("192.0.2.8"))
	}
	if pairKey("192.0.2.7", "Lena") != pairKey("192.0.2.7", "lena") || pairKey("192.0.2.7", "lena") == pairKey("192.0.2.8", "lena") {
		t.Error("pairs")
	}
}

func TestUsernames(t *testing.T) {
	if got := sanitizeUsername("  Anna.Müller@Example "); got != "anna.müller@example" {
		t.Errorf("sanitize: %q", got)
	}
	if got := sanitizeUsername("a b/c"); got != "abc" {
		t.Errorf("sanitize: %q", got)
	}
	for name, ok := range map[string]bool{"anna": true, "anna.m": true, "Anna": false, "a b": false, "": false} {
		if ValidUsername(name) != ok {
			t.Errorf("ValidUsername(%q) != %v", name, ok)
		}
	}
}

func TestScopes(t *testing.T) {
	session := &Principal{User: &User{ID: 1}, Method: MethodSession}
	read := &Principal{User: &User{ID: 1}, Method: MethodToken, Scopes: []string{ScopeRead}}
	write := &Principal{User: &User{ID: 1}, Method: MethodToken, Scopes: []string{ScopeWrite}}
	admin := &Principal{User: &User{ID: 1}, Method: MethodToken, Scopes: []string{ScopeAdmin}}
	check := func(p *Principal, scope string, want bool) {
		if p.AllowsScope(scope) != want {
			t.Errorf("%v allows %s: want %v", p.Scopes, scope, want)
		}
	}
	check(session, ScopeAdmin, true)
	check(read, ScopeRead, true)
	check(read, ScopeWrite, false)
	check(write, ScopeRead, true)
	check(write, ScopeAdmin, false)
	check(admin, ScopeWrite, true)
	if Anonymous.Authenticated() || Anonymous.UserID() != 0 {
		t.Fatal("anonymous")
	}
}

func TestSSHKeyStrength(t *testing.T) {
	rsaKey := func(bits int) string {
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			t.Fatal(err)
		}
		pub, err := ssh.NewPublicKey(&k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return string(ssh.MarshalAuthorizedKey(pub))
	}
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	ed, _ := ssh.NewPublicKey(edPub)
	// A certificate signs in like its key: the key has to meet the requirements, too.
	_, caKey, _ := ed25519.GenerateKey(rand.Reader)
	ca, err := ssh.NewSignerFromKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	certOf := func(authorizedKey string) string {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
		if err != nil {
			t.Fatal(err)
		}
		cert := &ssh.Certificate{Key: pub, CertType: ssh.UserCert, KeyId: "lena", ValidBefore: ssh.CertTimeInfinity}
		if err := cert.SignCert(rand.Reader, ca); err != nil {
			t.Fatal(err)
		}
		return string(ssh.MarshalAuthorizedKey(cert))
	}
	for _, c := range []struct {
		name, key string
		ok        bool
	}{
		{"RSA 2048", rsaKey(2048), false},
		{"RSA 3072", rsaKey(3072), true},
		{"Ed25519", string(ssh.MarshalAuthorizedKey(ed)), true},
		{"garbage", "ssh-rsa AAAA", false},
		{"certificate of RSA 2048", certOf(rsaKey(2048)), false},
		{"certificate of Ed25519", certOf(string(ssh.MarshalAuthorizedKey(ed))), true},
	} {
		if _, err := ParseSSHKey(c.key); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
