// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package identity manages users, groups, sessions, access tokens and authentication (local accounts and
// OpenID Connect through Keycloak).
package identity

import (
	"context"
	"slices"
	"time"
)

// User statuses.
const (
	StatusActive     = "active"
	StatusDisabled   = "disabled"
	StatusAnonymised = "anonymised"
)

// User kinds.
const (
	KindPerson      = "person"
	KindService     = "service"
	KindSystem      = "system"
	KindPlaceholder = "placeholder"
)

// SystemUserID is the id of the built-in system user.
const SystemUserID int64 = 1

// User is a person or account.
type User struct {
	ID            int64      `json:"id"`
	Username      string     `json:"username"`
	DisplayName   string     `json:"displayName"`
	Email         string     `json:"email,omitempty"`
	EmailVerified bool       `json:"emailVerified"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	Locale        string     `json:"locale,omitempty"`
	TimeZone      string     `json:"timeZone,omitempty"`
	Theme         string     `json:"theme,omitempty"`
	External      bool       `json:"external" doc:"Signs in through the identity provider"`
	HasPassword   bool       `json:"hasPassword" doc:"Has a local password"`
	TwoFactor     bool       `json:"twoFactor" doc:"Signs in with a password and a code of an authenticator app"`
	SSOLink       string     `json:"ssoLink,omitempty" doc:"User name at the identity provider whose first sign-in takes this account over (set by an administrator, see SetSSOLink)"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastLoginAt   *time.Time `json:"lastLoginAt,omitempty"`
}

// UserRef is the short form of a user embedded in other resources.
type UserRef struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
}

// Ref returns the short form.
func (u *User) Ref() UserRef {
	return UserRef{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Active: u.Status == StatusActive}
}

// Group is a set of users. Groups from the identity provider have source "oidc".
type Group struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Source      string    `json:"source" enum:"local,oidc"`
	Members     int       `json:"members"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Authentication methods of a principal.
const (
	MethodAnonymous = "anonymous"
	MethodSession   = "session"
	MethodToken     = "token"
	MethodInternal  = "internal"
	MethodPassword  = "password" // Git over HTTPS with the local password
	MethodSSH       = "ssh"      // Git over SSH with a user's key
)

// Token scopes.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	User      *User
	Groups    []int64
	Method    string
	Scopes    []string // token scopes; empty for sessions (full rights of the user)
	CSRFToken string
	SessionID []byte
	// TokenID is the access token of a principal of the method token.
	TokenID int64
	// SignIn says how a session began: "password" (a local account) or "oidc" (single sign-on).
	SignIn string
	// TwoFactorSetup: the installation requires two-factor authentication, which the user of this password
	// session has not set up; until then the session reaches nothing but the setup (duringSetup).
	TwoFactorSetup bool
}

// Anonymous is the principal of unauthenticated requests.
var Anonymous = &Principal{Method: MethodAnonymous}

// Authenticated reports whether a user is signed in.
func (p *Principal) Authenticated() bool { return p != nil && p.User != nil }

// UserID returns the user's id, or 0 for anonymous principals.
func (p *Principal) UserID() int64 {
	if p == nil || p.User == nil {
		return 0
	}
	return p.User.ID
}

// InGroup reports whether the principal is a member of the group.
func (p *Principal) InGroup(id int64) bool { return slices.Contains(p.Groups, id) }

// AllowsScope reports whether the principal's token (if any) carries the scope. Sessions allow everything.
func (p *Principal) AllowsScope(scope string) bool {
	if p.Method != MethodToken {
		return true
	}
	if slices.Contains(p.Scopes, ScopeAdmin) {
		return true
	}
	if scope == ScopeRead {
		return slices.Contains(p.Scopes, ScopeRead) || slices.Contains(p.Scopes, ScopeWrite)
	}
	return slices.Contains(p.Scopes, scope)
}

type ctxKey struct{}

// WithPrincipal returns a context carrying the principal.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the request's principal (Anonymous if none).
func From(ctx context.Context) *Principal {
	if p, ok := ctx.Value(ctxKey{}).(*Principal); ok && p != nil {
		return p
	}
	return Anonymous
}

// SystemPrincipal acts for automated changes.
func SystemPrincipal() *Principal {
	return &Principal{User: &User{ID: SystemUserID, Username: "kogge", DisplayName: "Kogge", Kind: KindSystem,
		Status: StatusActive}, Method: MethodInternal}
}

// AccessToken is a personal access token (without its secret).
type AccessToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Hint       string     `json:"hint" doc:"Start and end of the token, to recognise it"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// Session describes a browser session (without its secret).
type Session struct {
	ID         string    `json:"id" doc:"Opaque id (hash prefix) for revocation"`
	AuthMethod string    `json:"authMethod"`
	IP         string    `json:"ip,omitempty"`
	UserAgent  string    `json:"userAgent"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Current    bool      `json:"current"`
}
