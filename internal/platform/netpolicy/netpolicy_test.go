// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package netpolicy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPolicy(t *testing.T) {
	open := New("", false)
	for _, c := range []struct {
		ip string
		ok bool
	}{
		// Without a list: public addresses only; internal networks need to be listed (audit M4).
		{"203.0.113.5", true}, {"2001:db8::1", true},
		{"10.1.2.3", false}, {"172.17.0.1", false}, {"192.168.0.10", false}, {"fd12:3456::1", false}, {"100.64.0.1", false},
		{"::ffff:10.1.2.3", false},
		{"127.0.0.1", false}, {"::1", false}, {"169.254.169.254", false}, {"fe80::1", false}, {"0.0.0.0", false},
		{"fd00:ec2::254", false}, {"100.100.100.200", false}, {"224.0.0.1", false}, {"::ffff:127.0.0.1", false},
		// An address that carries an IPv4 address (NAT64, 6to4, IPv4-compatible) is judged as that one.
		{"64:ff9b::cb00:7105", true}, {"2002:cb00:7105::1", true},
		{"64:ff9b::a9fe:a9fe", false}, {"64:ff9b::7f00:1", false}, {"64:ff9b::a00:1", false}, {"2002:a9fe:a9fe::1", false},
		{"2002:a00:1::1", false}, {"::a00:1", false}, {"::7f00:1", false}, {"64:ff9b::a9fe:a9fe%eth0", false},
		// Other ranges outside the public internet are internal.
		{"64:ff9b:1::a00:1", false}, {"fec0::1", false}, {"fec0::1%eth0", false}, {"100::1", false},
		{"0.1.2.3", false}, {"192.0.0.8", false}, {"198.18.0.1", false}, {"240.0.0.1", false}, {"255.255.255.255", false},
	} {
		if err := open.Check(netip.MustParseAddr(c.ip), "example.com"); (err == nil) != c.ok {
			t.Errorf("open policy, %s: %v", c.ip, err)
		}
	}
	if err := New("", true).Check(netip.MustParseAddr("127.0.0.1"), "localhost"); err != nil {
		t.Errorf("loopback allowed: %v", err)
	}

	// Systems an administrator names (migration sources): internal and public addresses, never link-local, multicast
	// or metadata addresses, also when an IPv6 address carries one.
	internal := Internal()
	for _, c := range []struct {
		ip string
		ok bool
	}{
		{"10.1.2.3", true}, {"172.17.0.1", true}, {"192.168.0.10", true}, {"fd12:3456::1", true}, {"100.64.0.1", true},
		{"127.0.0.1", true}, {"::1", true}, {"203.0.113.5", true}, {"2001:db8::1", true}, {"::ffff:10.1.2.3", true},
		{"169.254.169.254", false}, {"fe80::1", false}, {"0.0.0.0", false}, {"::", false}, {"fd00:ec2::254", false},
		{"100.100.100.200", false}, {"224.0.0.1", false}, {"ff02::1", false}, {"::ffff:169.254.169.254", false},
		{"64:ff9b::a9fe:a9fe", false}, {"2002:a9fe:a9fe::1", false}, {"fe80::1%eth0", false},
	} {
		if err := internal.Check(netip.MustParseAddr(c.ip), "jira.corp.example"); (err == nil) != c.ok {
			t.Errorf("internal policy, %s: %v", c.ip, err)
		}
	}

	listed := New("ci.example.com, *.corp.example\n10.20.0.0/16", false)
	for _, c := range []struct {
		host, ip string
		ok       bool
	}{
		{"ci.example.com", "203.0.113.9", true},
		{"jenkins.corp.example", "10.9.9.9", true},
		{"other.example.com", "10.20.1.1", true}, // in a listed network
		{"other.example.com", "10.21.1.1", false},
		{"other.example.com", "64:ff9b::a14:101", true}, // 10.20.1.1 through NAT64
		{"ci.example.com", "127.0.0.1", false},          // never loopback
		{"10.20.5.5", "10.20.5.5", true},
	} {
		if err := listed.Check(netip.MustParseAddr(c.ip), c.host); (err == nil) != c.ok {
			t.Errorf("allow list, %s at %s: %v", c.host, c.ip, err)
		}
	}
	if !listed.HostAllowed("ci.example.com") || !listed.HostAllowed("x.corp.example") || !listed.HostAllowed("unknown.example") {
		t.Error("host checks before connecting") // unknown hosts may resolve into a listed network
	}
	hostsOnly := New("ci.example.com", false)
	if hostsOnly.HostAllowed("evil.example") {
		t.Error("an unlisted host passed a host-only allow list")
	}
	// "public" adds all public addresses to a list of internal ones.
	mixed := New("public\n10.20.0.0/16", false)
	for _, c := range []struct {
		ip string
		ok bool
	}{{"203.0.113.5", true}, {"10.20.1.1", true}, {"10.21.1.1", false}, {"192.168.1.1", false}, {"127.0.0.1", false}} {
		if err := mixed.Check(netip.MustParseAddr(c.ip), "host.example"); (err == nil) != c.ok {
			t.Errorf("public and a network, %s: %v", c.ip, err)
		}
	}
}

// TestProxy: the proxy for git connects only where the policy allows.
func TestProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") }))
	defer target.Close()
	get := func(p *Policy) (int, string) {
		t.Helper()
		proxyURL, stop, err := p.Proxy()
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		pu, _ := url.Parse(proxyURL)
		c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
		res, err := c.Get(target.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if status, body := get(New("", false)); status != http.StatusForbidden || !strings.Contains(body, "not allowed") {
		t.Errorf("loopback through the proxy: %d %s", status, body)
	}
	if status, body := get(New("", true)); status != http.StatusOK || body != "hello" {
		t.Errorf("allowed target: %d %s", status, body)
	}
}

// TestProxyConnect: https goes through CONNECT, with the same check.
func TestProxyConnect(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "secure") }))
	defer target.Close()
	for _, loopback := range []bool{false, true} {
		proxyURL, stop, err := New("", loopback).Proxy()
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse(proxyURL)
		tr := target.Client().Transport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyURL(pu)
		res, err := (&http.Client{Transport: tr}).Get(target.URL)
		if loopback {
			if err != nil {
				t.Fatalf("allowed CONNECT: %v", err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if string(b) != "secure" {
				t.Errorf("body = %q", b)
			}
		} else if err == nil {
			res.Body.Close()
			t.Errorf("CONNECT to loopback: %d", res.StatusCode)
		}
		stop()
	}
}
