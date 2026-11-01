// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/httpx"
)

// Throttling of password checks: sign-ins, Git with a password, and the confirmations of the
// signed-in user (changing the password, two-factor settings). A password is checked only while
//
//   - the client address and the user name together have failed less than a few times (strict: one client guessing
//     one account gets five tries, then one a minute),
//   - the client address has failed less than many times, over all user names (loose: guessing one password for
//     many accounts — password spraying — is stopped, while the typos of everyone behind one address, a terminal
//     server or a proxy not listed in KOGGE_TRUSTED_PROXIES, never reach it), and
//   - the account allows another attempt: every sign-in counts (it protects the server, which hashes each one), for
//     Git only failures (Git sends the password with every request); and the persisted lockout after
//     lockAfterFailures failures in a row, which counts the failures of all addresses and nodes.
//
// So one client failing for one account blocks neither other accounts nor other clients, even behind one address.
// Refused attempts are not checked: they neither count as failures nor reach the audit log. IPv6 clients are
// counted by their /64 network, which one client usually has to itself.
//
// With several nodes behind a load balancer, the limit per client address and the lockout hold for
// all nodes together (both are kept in the database): password spraying gets no further through more nodes, and
// neither does guessing one account. The other limits are kept per node, in memory: that of one client and one
// account (the lockout catches what passes it at the other nodes), that of the sign-ins of an account (it protects
// the node's processors, which hash each password), and that of invalid access tokens (which cannot be guessed;
// it keeps their failures from flooding the audit log).
type throttle struct {
	pair    *httpx.Limiter // failures per client address and user name, at this node
	address *sharedLimit   // failures per client address, at all nodes together
	account *httpx.Limiter // per user name, at this node: sign-in attempts, or failures of Git
	tokens  *httpx.Limiter // invalid access tokens per client address, at this node
}

func newThrottle(pool *pgxpool.Pool) *throttle {
	return &throttle{
		pair:    httpx.NewLimiter(time.Minute, 5),
		address: &sharedLimit{pool: pool, kind: "password-failures", interval: 5 * time.Second, burst: 60},
		account: httpx.NewLimiter(6*time.Second, 10),
		tokens:  httpx.NewLimiter(6*time.Second, 10),
	}
}

// sharedLimit limits events per key for all nodes of an installation together: a token bucket in the database
// (rate_limits), kept as the time at which it is full again. A key has burst events at once, then one per interval.
// A key without events has no row, so that probing many keys costs nothing.
type sharedLimit struct {
	pool     *pgxpool.Pool
	kind     string // what the limit counts: the prefix of its keys
	interval time.Duration
	burst    int
}

// tolerance is how far ahead of now the time of a full bucket may be for one more event.
func (l *sharedLimit) tolerance() time.Duration { return time.Duration(l.burst-1) * l.interval }

// blocked reports whether key has used up its events, without recording one.
func (l *sharedLimit) blocked(ctx context.Context, key string) (bool, error) {
	var blocked bool
	err := l.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM rate_limits WHERE key = $1 AND full_at > now() + $2::interval)",
		l.kind+":"+key, seconds(l.tolerance())).Scan(&blocked)
	return blocked, err
}

// fail records an event for key, unless key has used up its events (then nothing changes, as with Limiter.Fail).
func (l *sharedLimit) fail(ctx context.Context, key string) error {
	_, err := l.pool.Exec(ctx, `INSERT INTO rate_limits AS r (key, full_at) VALUES ($1, now() + $2::interval)
		ON CONFLICT (key) DO UPDATE SET full_at = greatest(r.full_at, now()) + $2::interval
		WHERE r.full_at <= now() + $3::interval`, l.kind+":"+key, seconds(l.interval), seconds(l.tolerance()))
	return err
}

// CleanupRateLimits deletes the limits whose buckets are full again (the session cleanup).
func CleanupRateLimits(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, "DELETE FROM rate_limits WHERE full_at < now()")
	return err
}

// clientKey is the key of a client address: the address, or the /64 network of an IPv6 address.
func clientKey(a netip.Addr) string {
	a = a.Unmap()
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

func pairKey(client, username string) string { return client + "\x00" + strings.ToLower(username) }

// admit reports whether a password of username from the client may be checked now. every counts the attempt for
// the account whatever its outcome (sign-ins); otherwise only failures count for it (Git).
func (t *throttle) admit(ctx context.Context, client, username string, every bool) (bool, error) {
	if t.pair.Blocked(pairKey(client, username)) {
		return false, nil
	}
	if blocked, err := t.address.blocked(ctx, client); err != nil || blocked {
		return false, err
	}
	if every {
		return t.account.Allow(strings.ToLower(username)), nil
	}
	return !t.account.Blocked(strings.ToLower(username)), nil
}

// failed records a wrong password or second factor for username from the client; every as for admit.
func (t *throttle) failed(ctx context.Context, client, username string, every bool) error {
	t.pair.Fail(pairKey(client, username))
	if !every {
		t.account.Fail(strings.ToLower(username))
	}
	return t.address.fail(ctx, client)
}

// client returns the throttling key of the request's client address.
func client(ctx context.Context) string { return clientKey(httpx.ClientIP(ctx)) }

// audit records an event that must not stop what it reports — a refused credential, a lockout, a revocation —
// with audit.Report.
func (s *Service) audit(ctx context.Context, e audit.Entry) { audit.Report(ctx, s.pool, s.log, e) }

// refused counts a refused password (or second factor) in the throttle; every as for admit. A wrong one of an
// account (userID) also counts for the account's lockout, which is audited when this failure locks the account;
// via names the way in (sign-in, git, profile).
func (s *Service) refused(ctx context.Context, userID int64, username, via string, wrong, every bool) {
	// Counted also when the client has gone away: the limits must not depend on the client waiting for the answer.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.throttle.failed(lctx, client(ctx), username, every); err != nil {
		s.log.Error("a failed password check could not be counted for the client's address", "via", via, "error", err)
	}
	if userID == 0 || !wrong {
		return
	}
	lockedNow, err := recordLoginFailure(lctx, s.pool, userID, lockAfterFailures, lockDuration)
	if err != nil {
		s.log.Error("a failed sign-in could not be counted for the account's lockout", "user", userID, "via", via, "error", err)
	} else if lockedNow {
		s.audit(ctx, audit.Entry{ActorID: userID, Action: "auth.locked",
			Details: map[string]any{"username": username, "minutes": int(lockDuration.Minutes()), "via": via}})
	}
}
