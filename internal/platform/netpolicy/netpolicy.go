// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package netpolicy decides which addresses Kogge connects to on behalf of users (webhooks, repository imports):
// never loopback (except for tests), link-local or cloud metadata addresses; private networks and
// reserved ranges only when an administrator lists them; with an allow list, only what it lists. The check applies
// to the address actually connected to, so DNS answers cannot sneak past it, and an IPv6 address that carries an
// IPv4 address (NAT64, 6to4) counts as that IPv4 address.
package netpolicy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"time"
)

// ErrForbidden is returned for connections the policy refuses.
var ErrForbidden = errors.New("the target address is not allowed (an internal, loopback, link-local or metadata address, or not on the allow list)")

// Policy is an outbound policy.
type Policy struct {
	loopback bool
	public   bool     // public addresses are allowed: the list is empty or says "public"
	hosts    []string // allowed host names; "*.example.com" for subdomains
	nets     []netip.Prefix
}

// New returns a policy. allowList holds host names, addresses, networks (CIDR) and the word "public" (all public
// addresses), separated by newlines, commas or spaces. Empty allows public addresses only; internal ones must be
// listed. loopback allows loopback addresses (for tests).
func New(allowList string, loopback bool) *Policy {
	p := &Policy{loopback: loopback}
	entries := 0
	for _, line := range strings.FieldsFunc(allowList, func(r rune) bool { return r == '\n' || r == ',' || r == ' ' }) {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		entries++
		if line == "public" {
			p.public = true
		} else if pfx, err := netip.ParsePrefix(line); err == nil {
			p.nets = append(p.nets, pfx)
		} else if ip, err := netip.ParseAddr(line); err == nil {
			p.nets = append(p.nets, netip.PrefixFrom(ip, ip.BitLen()))
		} else {
			p.hosts = append(p.hosts, line)
		}
	}
	if entries == 0 {
		p.public = true
	}
	return p
}

// Internal returns the policy of connections to systems that an administrator names, such as the sources of a
// migration: any address of them, private networks and loopback included, since such systems usually are internal;
// never link-local, multicast, unspecified or cloud metadata addresses — no such address is a system to read from,
// and a source must not lead Kogge there (through its host names or a redirect).
func Internal() *Policy {
	return &Policy{loopback: true, nets: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}}
}

// reserved are ranges outside the public internet, internal like the private ranges: carrier-grade NAT (RFC 6598),
// "this network", IETF protocol assignments, benchmarking and reserved addresses (RFC 6890); for IPv6 the
// local-use NAT64 prefix (RFC 8215), the former site-local addresses and the discard prefix (RFC 6666).
var reserved = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("fec0::/10"), netip.MustParsePrefix("100::/64"),
}

// internal reports whether an address belongs to a private network (RFC 1918, unique local IPv6) or a reserved
// range.
func internal(ip netip.Addr) bool {
	return ip.IsPrivate() || slices.ContainsFunc(reserved, func(r netip.Prefix) bool { return r.Contains(ip) })
}

// Prefixes of IPv6 addresses that carry an IPv4 address: NAT64 (RFC 6052), 6to4 (RFC 3056) and the deprecated
// IPv4-compatible addresses.
var (
	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour  = netip.MustParsePrefix("2002::/16")
	compatible = netip.MustParsePrefix("::/96")
)

// carried returns the IPv4 address an IPv6 address carries, otherwise the address itself. A connection to such an
// address can end at the IPv4 address, so the policy judges that one.
func carried(ip netip.Addr) netip.Addr {
	if !ip.Is6() || ip.IsLoopback() || ip.IsUnspecified() {
		return ip
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip), compatible.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:]))
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6]))
	}
	return ip
}

func (p *Policy) hostListed(host string) bool {
	host = strings.ToLower(host)
	for _, h := range p.hosts {
		if h == host || strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:]) {
			return true
		}
	}
	return false
}

// HostAllowed is the check before connecting: a listed host, or one whose addresses may pass Check.
func (p *Policy) HostAllowed(host string) bool {
	if p.public || p.hostListed(host) {
		return true
	}
	_, err := netip.ParseAddr(host)
	return err == nil || len(p.nets) > 0
}

// metadata are addresses of cloud metadata services outside the link-local range.
var metadata = []netip.Addr{netip.MustParseAddr("fd00:ec2::254"), netip.MustParseAddr("100.100.100.200")}

// Check is applied to the address actually connected to.
func (p *Policy) Check(ip netip.Addr, host string) error {
	// A zone ("%eth0") does not change what an address is, but prefixes never contain zoned addresses.
	ip = carried(ip.WithZone("").Unmap())
	switch {
	case ip.IsUnspecified(), ip.IsMulticast(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast():
		return ErrForbidden
	case ip.IsLoopback() && !p.loopback:
		return ErrForbidden
	}
	for _, m := range metadata {
		if ip == m {
			return ErrForbidden
		}
	}
	if p.hostListed(host) {
		return nil
	}
	for _, n := range p.nets {
		if n.Contains(ip) {
			return nil
		}
	}
	if p.public && !internal(ip) && !(ip.IsLoopback() && p.loopback) {
		return nil
	}
	if ip.IsLoopback() && p.loopback {
		return nil
	}
	return ErrForbidden
}

// Dialer returns a dialer whose connections to host pass the policy.
func (p *Policy) Dialer(host string, timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: func(_, address string, _ syscall.RawConn) error {
		h, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(h)
		if err != nil {
			return err
		}
		return p.Check(ip, host)
	}}
}

// Forbidden reports whether err comes from the policy.
func Forbidden(err error) bool {
	var opErr *net.OpError
	return errors.Is(err, ErrForbidden) || (errors.As(err, &opErr) && errors.Is(opErr.Err, ErrForbidden))
}

// Proxy starts a forward proxy on the loopback interface for programs that make their own connections (git): it
// connects only where the policy allows. It returns the proxy's URL and a function that stops it.
func (p *Policy) Proxy() (proxyURL string, stop func(), err error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: http.HandlerFunc(p.serveProxy), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(l) }()
	return "http://" + l.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = srv.Close()
	}, nil
}

func (p *Policy) serveProxy(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	if r.Method == http.MethodConnect {
		host, _, _ = net.SplitHostPort(r.Host)
	}
	if host == "" || !p.HostAllowed(host) {
		http.Error(w, ErrForbidden.Error(), http.StatusForbidden)
		return
	}
	dialer := p.Dialer(host, 10*time.Second)
	if r.Method != http.MethodConnect {
		// Plain http: forward the request; redirects reach the client, which asks the proxy again.
		if r.URL.Scheme != "http" {
			http.Error(w, "unsupported", http.StatusBadRequest)
			return
		}
		tr := &http.Transport{DialContext: dialer.DialContext, Proxy: nil, DisableKeepAlives: true}
		defer tr.CloseIdleConnections()
		r.RequestURI = ""
		r.Header.Del("Proxy-Connection")
		res, err := tr.RoundTrip(r)
		if err != nil {
			status := http.StatusBadGateway
			if Forbidden(err) {
				status = http.StatusForbidden
			}
			http.Error(w, err.Error(), status)
			return
		}
		defer res.Body.Close()
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
		return
	}
	target, err := dialer.DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		status := http.StatusBadGateway
		if Forbidden(err) {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		target.Close()
		http.Error(w, "unsupported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		target.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	if n := buf.Reader.Buffered(); n > 0 {
		b, _ := buf.Reader.Peek(n)
		_, _ = target.Write(b)
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, target); done <- struct{}{} }()
	<-done
	client.Close()
	target.Close()
}
