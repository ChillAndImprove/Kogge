// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"KOGGE_DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.SSHAddr != ":2222" || c.SSHPublicPort != 2222 {
		t.Errorf("addresses: %q %q %d", c.HTTPAddr, c.SSHAddr, c.SSHPublicPort)
	}
	if !c.HasRole(RoleWeb) || !c.HasRole(RoleWorker) {
		t.Errorf("default roles: %v", c.Roles)
	}
	if c.PublicURL.String() != "http://localhost:8080" || c.SecureCookies() {
		t.Errorf("public url: %v", c.PublicURL)
	}
	if c.SessionIdleTimeout != 2*time.Hour || !c.LocalLogin || c.OIDC.Enabled() {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if c.RequestTimeout != time.Minute || c.StallTimeout != time.Minute {
		t.Errorf("time limits of requests: %v %v", c.RequestTimeout, c.StallTimeout)
	}
	if !filepath.IsAbs(c.DataDir) {
		t.Errorf("data dir not absolute: %s", c.DataDir)
	}
}

func TestErrorsAreCollected(t *testing.T) {
	_, err := Load(env(map[string]string{
		"KOGGE_ROLES":          "web,banana",
		"KOGGE_PUBLIC_URL":     "ftp://x",
		"KOGGE_LOG_LEVEL":      "loud",
		"KOGGE_OIDC_ISSUER":    "https://sso",
		"KOGGE_TLS_CERT_FILE":  "/c",
		"KOGGE_LOCAL_LOGIN":    "maybe",
		"KOGGE_DEFAULT_LOCALE": "fr",
	}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"KOGGE_DATABASE_URL", "banana", "KOGGE_PUBLIC_URL", "KOGGE_LOG_LEVEL",
		"KOGGE_OIDC_CLIENT_ID", "KOGGE_TLS_KEY_FILE", "KOGGE_LOCAL_LOGIN", "KOGGE_DEFAULT_LOCALE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestSecretFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if err := os.WriteFile(path, []byte("postgres://secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(env(map[string]string{"KOGGE_DATABASE_URL_FILE": path, "KOGGE_PUBLIC_URL": "https://kogge.example.com/",
		"KOGGE_ROLES": "worker", "KOGGE_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.1", "KOGGE_SSH_ADDR": ":7999"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseURL != "postgres://secret" {
		t.Errorf("secret: %q", c.DatabaseURL)
	}
	if !c.SecureCookies() || c.PublicURL.String() != "https://kogge.example.com" {
		t.Errorf("public url: %v", c.PublicURL)
	}
	if c.HasRole(RoleWeb) || !c.HasRole(RoleWorker) {
		t.Errorf("roles: %v", c.Roles)
	}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[1].Bits() != 32 {
		t.Errorf("proxies: %v", c.TrustedProxies)
	}
	if c.SSHPublicPort != 7999 {
		t.Errorf("ssh public port: %d", c.SSHPublicPort)
	}
}

func TestBootstrapPasswordPolicy(t *testing.T) {
	_, err := Load(env(map[string]string{"KOGGE_DATABASE_URL": "x", "KOGGE_BOOTSTRAP_ADMIN_USERNAME": "admin",
		"KOGGE_BOOTSTRAP_ADMIN_PASSWORD": "short"}))
	if err == nil || !strings.Contains(err.Error(), "12 characters") {
		t.Fatalf("expected password policy error, got %v", err)
	}
}

// TestAuditForward: the targets of the audit forwarding are checked at start, and errors never repeat a value
// that may hold credentials.
func TestAuditForward(t *testing.T) {
	load := func(m map[string]string) (*Config, error) {
		m["KOGGE_DATABASE_URL"] = "postgres://x"
		return Load(env(m))
	}
	for raw, want := range map[string]string{
		"file:///var/log/kogge/../kogge/audit.jsonl":     "file:///var/log/kogge/audit.jsonl",
		"tcp://siem.example.com:5170":                    "tcp://siem.example.com:5170",
		"tls://[2001:db8::1]:5171":                       "tls://[2001:db8::1]:5171",
		"syslog+udp://siem.example.com":                  "syslog+udp://siem.example.com",
		"syslog+tls://siem.example.com:6514":             "syslog+tls://siem.example.com:6514",
		"https://user:pass@collector.example.com/in?x=1": "https://user:pass@collector.example.com/in?x=1",
	} {
		c, err := load(map[string]string{"KOGGE_AUDIT_FORWARD_URL": raw})
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if !c.AuditForward.Enabled() || c.AuditForward.URL.String() != want {
			t.Errorf("%s: %v", raw, c.AuditForward.URL)
		}
	}
	if c, err := load(map[string]string{}); err != nil || c.AuditForward.Enabled() {
		t.Errorf("forwarding without a target: %v %+v", err, c.AuditForward)
	}

	for raw, want := range map[string]string{
		"file:relative/audit.jsonl":              "absolute path",
		"file://host/var/log/audit.jsonl":        "absolute path",
		"tcp://siem.example.com":                 "need a port",
		"tls://:5170":                            "need a host",
		"syslog+tcp://siem.example.com:99999":    "port",
		"syslog+tcp://u:secret@siem.example.com": "host and a port only",
		"tcp://siem.example.com:5170/path":       "host and a port only",
		"ftp://secret-host/":                     "must be a file://",
		"https:///nohost":                        "need a host",
	} {
		_, err := load(map[string]string{"KOGGE_AUDIT_FORWARD_URL": raw})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the error repeats the value: %v", raw, err)
		}
	}

	_, err := load(map[string]string{"KOGGE_AUDIT_FORWARD_URL": "syslog+tcp://siem.example.com",
		"KOGGE_AUDIT_FORWARD_CA_FILE": "/ca.pem", "KOGGE_AUDIT_FORWARD_CERT_FILE": "/c.pem",
		"KOGGE_AUDIT_FORWARD_AUTHORIZATION": "Bearer x"})
	for _, want := range []string{"_KEY_FILE must be set together", "apply to tls, syslog+tls and https targets only",
		"AUTHORIZATION applies to http and https targets only"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error does not say %q: %v", want, err)
		}
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("Splunk 1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := load(map[string]string{"KOGGE_AUDIT_FORWARD_URL": "https://collector.example.com/raw",
		"KOGGE_AUDIT_FORWARD_AUTHORIZATION_FILE": path, "KOGGE_AUDIT_FORWARD_CA_FILE": "/ca.pem"})
	if err != nil || c.AuditForward.Authorization != "Splunk 1234" || c.AuditForward.CAFile != "/ca.pem" {
		t.Errorf("https with a token from a file: %v %+v", err, c)
	}
}

// TestPlainHTTP: an http:// public URL is flagged unless it names this machine.
func TestPlainHTTP(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost:8080": false, "http://127.0.0.1:8080": false, "http://[::1]:8080": false,
		"http://kogge.localhost": false, "https://kogge.example.com": false,
		"http://kogge.example.com": true, "http://192.0.2.10:8080": true,
	} {
		c, err := Load(env(map[string]string{"KOGGE_DATABASE_URL": "postgres://x", "KOGGE_PUBLIC_URL": url}))
		if err != nil {
			t.Fatal(err)
		}
		if got := c.PlainHTTP(); got != want {
			t.Errorf("%s: PlainHTTP() = %v, want %v", url, got, want)
		}
	}
}
