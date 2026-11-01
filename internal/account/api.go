// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package account provides the API for signing in and out, the current user's profile, tokens and sessions,
// and the user directory for pickers.
package account

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/settings"
)

// Handler serves the account API.
type Handler struct {
	ID       *identity.Service
	Authz    *authz.Service
	Settings *settings.Service
	Config   *config.Config
	Version  string
}

// GroupRef is a group in the profile.
type GroupRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Me is the signed-in user with what the web application needs at start.
type Me struct {
	User         *identity.User `json:"user"`
	Groups       []GroupRef     `json:"groups"`
	Permissions  []string       `json:"permissions" doc:"Global permissions"`
	CSRFToken    string         `json:"csrfToken,omitempty" doc:"Send in X-CSRF-Token with state-changing requests"`
	InstanceName string         `json:"instanceName"`
	Announcement string         `json:"announcement,omitempty"`
	Version      string         `json:"version"`
	// TokenMaxDays is the longest lifetime of new access tokens (0 = no limit).
	TokenMaxDays int `json:"tokenMaxDays"`
	// ExternalImages says whether documents may show images from other servers (an instance setting).
	ExternalImages bool `json:"externalImages"`
	// MaxAttachmentMB is the largest attachment in MiB (a limit of the settings), for refusing larger files before
	// they are sent.
	MaxAttachmentMB int `json:"maxAttachmentMb"`
	// TwoFactorSetup: the installation requires two-factor authentication, which the user has to set up before
	// anything else.
	TwoFactorSetup bool `json:"twoFactorSetup,omitempty"`
	// TimeZone is the time zone of the user's schedules and relative dates: their own, else the installation's.
	TimeZone string `json:"timeZone"`
	// Preferences are settings of the web application that follow the user from browser to browser.
	Preferences Preferences `json:"preferences"`
}

// Preferences are settings of the web application kept for the user.
type Preferences struct {
	NavigatorColumns []string `json:"navigatorColumns,omitempty" maxItems:"40" doc:"The columns of the issue navigator: field keys such as summary or cf_12; the default ones when empty"`
}

var columnKey = regexp.MustCompile(`^(cf_[0-9]{1,18}|[A-Za-z]{1,40})$`)

// MeOutput wraps Me.
type MeOutput struct {
	Body *Me
}

// LoginOutput sets the session cookie.
type LoginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      *Me
}

// Register adds the operations to the API.
func (h *Handler) Register(a huma.API) {
	huma.Register(a, api.Op("get-auth-config", http.MethodGet, "/api/v1/auth/config", "Sign-in options", "Authentication"),
		h.authConfig)
	huma.Register(a, api.Op("login", http.MethodPost, "/api/v1/auth/login", "Sign in with a local password", "Authentication"),
		h.login)
	huma.Register(a, api.Op("logout", http.MethodPost, "/api/v1/auth/logout", "Sign out", "Authentication"), h.logout)
	huma.Register(a, api.Op("get-me", http.MethodGet, "/api/v1/me", "The signed-in user", "Profile"), h.me)
	huma.Register(a, api.Op("update-me", http.MethodPatch, "/api/v1/me", "Change profile settings", "Profile"), h.updateMe)
	huma.Register(a, api.Op("change-password", http.MethodPut, "/api/v1/me/password", "Change the local password", "Profile"),
		h.changePassword)
	huma.Register(a, api.Op("list-tokens", http.MethodGet, "/api/v1/me/tokens", "Personal access tokens", "Profile"), h.listTokens)
	huma.Register(a, api.Op("create-token", http.MethodPost, "/api/v1/me/tokens", "Create a personal access token", "Profile"),
		h.createToken)
	huma.Register(a, api.Op("revoke-token", http.MethodDelete, "/api/v1/me/tokens/{id}", "Revoke a personal access token", "Profile"),
		h.revokeToken)
	huma.Register(a, api.Op("list-ssh-keys", http.MethodGet, "/api/v1/me/ssh-keys", "SSH keys for Git", "Profile"), h.listSSHKeys)
	huma.Register(a, api.Op("add-ssh-key", http.MethodPost, "/api/v1/me/ssh-keys", "Add an SSH key", "Profile"), h.addSSHKey)
	huma.Register(a, api.Op("delete-ssh-key", http.MethodDelete, "/api/v1/me/ssh-keys/{id}", "Remove an SSH key", "Profile"),
		h.deleteSSHKey)
	h.registerTwoFactor(a)
	huma.Register(a, api.Op("list-sessions", http.MethodGet, "/api/v1/me/sessions", "Active sessions", "Profile"), h.listSessions)
	huma.Register(a, api.Op("end-session", http.MethodDelete, "/api/v1/me/sessions/{id}", "End a session", "Profile"), h.endSession)
	huma.Register(a, api.Op("end-other-sessions", http.MethodDelete, "/api/v1/me/sessions",
		"End all other sessions (sign out everywhere else)", "Profile"), h.endOtherSessions)
	huma.Register(a, api.Op("list-users", http.MethodGet, "/api/v1/users", "Find users", "Users"), h.listUsers)
	huma.Register(a, api.Op("get-user", http.MethodGet, "/api/v1/users/{username}", "A user's profile", "Users"), h.getUser)
	huma.Register(a, api.Op("list-groups", http.MethodGet, "/api/v1/groups", "Find groups", "Users"), h.listGroups)
}

type authConfigBody struct {
	LocalLogin    bool   `json:"localLogin"`
	SSO           bool   `json:"sso"`
	SSOName       string `json:"ssoName,omitempty"`
	SSOLoginURL   string `json:"ssoLoginUrl,omitempty"`
	InstanceName  string `json:"instanceName"`
	DefaultLocale string `json:"defaultLocale"`
}

func (h *Handler) authConfig(ctx context.Context, _ *struct{}) (*struct{ Body authConfigBody }, error) {
	st, err := h.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	b := authConfigBody{LocalLogin: h.Config.LocalLogin, InstanceName: st.Name, DefaultLocale: st.DefaultLocale}
	if o := h.ID.OIDC(); o != nil {
		b.SSO, b.SSOName, b.SSOLoginURL = true, o.ProviderName(), "/auth/oidc/login"
	}
	return &struct{ Body authConfigBody }{b}, nil
}

func (h *Handler) buildMe(ctx context.Context, p *identity.Principal, csrf string) (*Me, error) {
	perms, err := h.Authz.Global(ctx, p)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(perms))
	for k := range perms {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	groups, err := identity.UserGroups(ctx, h.ID.Pool(), p.UserID())
	if err != nil {
		return nil, err
	}
	st, err := h.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	me := &Me{User: p.User, Permissions: keys, CSRFToken: csrf, InstanceName: st.Name,
		Announcement: st.Announcement, Version: h.Version, Groups: []GroupRef{}, TokenMaxDays: st.MaxTokenDays,
		ExternalImages: st.AllowExternalImages, TwoFactorSetup: p.TwoFactorSetup, TimeZone: st.DefaultTimeZone, MaxAttachmentMB: st.MaxAttachmentMB}
	if p.User.TimeZone != "" {
		me.TimeZone = p.User.TimeZone
	}
	var prefs []byte
	if err := h.ID.Pool().QueryRow(ctx, "SELECT preferences FROM users WHERE id = $1", p.UserID()).Scan(&prefs); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(prefs, &me.Preferences)
	for _, g := range groups {
		me.Groups = append(me.Groups, GroupRef{ID: g.ID, Name: g.Name})
	}
	return me, nil
}

type loginInput struct {
	UserAgent string `header:"User-Agent"`
	Body      struct {
		Username string `json:"username" minLength:"1" maxLength:"200"`
		Password string `json:"password" minLength:"1" maxLength:"1024"`
		Code     string `json:"code,omitempty" maxLength:"32" doc:"For accounts with two-factor authentication: a code of the authenticator app or a recovery code"`
	}
}

func (h *Handler) login(ctx context.Context, in *loginInput) (*LoginOutput, error) {
	token, user, err := h.ID.LoginLocal(ctx, strings.TrimSpace(in.Body.Username), in.Body.Password, in.Body.Code, in.UserAgent)
	if err != nil {
		return nil, err
	}
	p := &identity.Principal{User: user, Method: identity.MethodSession, SignIn: "password",
		TwoFactorSetup: h.ID.TwoFactorSetupNeeded(ctx, user)}
	groups, err := identity.UserGroups(ctx, h.ID.Pool(), user.ID)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		p.Groups = append(p.Groups, g.ID)
	}
	var csrf string
	if err := h.ID.Pool().QueryRow(ctx, `SELECT csrf_token FROM sessions WHERE id_hash = $1`,
		crypto.HashToken(token)).Scan(&csrf); err != nil {
		return nil, err
	}
	me, err := h.buildMe(ctx, p, csrf)
	if err != nil {
		return nil, err
	}
	return &LoginOutput{SetCookie: *h.ID.SessionCookie(token), Body: me}, nil
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		Redirect string `json:"redirect,omitempty" doc:"Where to send the browser to end the single sign-on session"`
	}
}

func (h *Handler) logout(ctx context.Context, _ *struct{}) (*logoutOutput, error) {
	p := identity.From(ctx)
	out := &logoutOutput{SetCookie: *h.ID.ClearCookie()}
	if !p.Authenticated() {
		return out, nil
	}
	if err := h.ID.Logout(ctx, p); err != nil {
		return nil, err
	}
	if o := h.ID.OIDC(); o != nil && p.User.External {
		out.Body.Redirect = o.EndSessionURL(ctx)
	}
	return out, nil
}

func (h *Handler) me(ctx context.Context, _ *struct{}) (*MeOutput, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	me, err := h.buildMe(ctx, p, p.CSRFToken)
	if err != nil {
		return nil, err
	}
	return &MeOutput{Body: me}, nil
}

type updateMeInput struct {
	Body struct {
		DisplayName *string `json:"displayName,omitempty" minLength:"1" maxLength:"200" doc:"Only for local accounts"`
		Locale      *string `json:"locale,omitempty" enum:"de,en,"`
		TimeZone    *string `json:"timeZone,omitempty" maxLength:"64"`
		Theme       *string `json:"theme,omitempty" enum:"light,dark,auto,"`
		// Preferences replace the preferences they name.
		Preferences *Preferences `json:"preferences,omitempty"`
	}
}

func (h *Handler) updateMe(ctx context.Context, in *updateMeInput) (*MeOutput, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	if in.Body.DisplayName != nil && p.User.External {
		return nil, apperr.BadRequest("profile.external", "Your name is managed by the identity provider.")
	}
	if tz := in.Body.TimeZone; tz != nil && *tz != "" {
		if _, err := time.LoadLocation(*tz); err != nil {
			return nil, apperr.Invalid("profile.time-zone", "Unknown time zone.", apperr.FieldError{Location: "body.timeZone", Value: *tz})
		}
	}
	if pr := in.Body.Preferences; pr != nil {
		for _, c := range pr.NavigatorColumns {
			if !columnKey.MatchString(c) {
				return nil, apperr.Invalidf("profile.column", "%q is not a column.", c)
			}
		}
		cols := pr.NavigatorColumns
		if cols == nil {
			cols = []string{}
		}
		patch, _ := json.Marshal(map[string]any{"navigatorColumns": cols})
		if _, err := h.ID.Pool().Exec(ctx, "UPDATE users SET preferences = preferences || $2::jsonb WHERE id = $1", p.UserID(),
			patch); err != nil {
			return nil, err
		}
	}
	u, err := identity.UpdateUser(ctx, h.ID.Pool(), p.UserID(), identity.UserUpdate{DisplayName: in.Body.DisplayName,
		Locale: in.Body.Locale, TimeZone: in.Body.TimeZone, Theme: in.Body.Theme})
	if err != nil {
		return nil, err
	}
	p.User = u
	me, err := h.buildMe(ctx, p, p.CSRFToken)
	if err != nil {
		return nil, err
	}
	return &MeOutput{Body: me}, nil
}

type passwordInput struct {
	Body struct {
		Current string `json:"current" minLength:"1" maxLength:"1024"`
		New     string `json:"new" minLength:"12" maxLength:"1024"`
	}
}

func (h *Handler) changePassword(ctx context.Context, in *passwordInput) (*struct{}, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.ChangePassword(ctx, p, in.Body.Current, in.Body.New)
}

func (h *Handler) listTokens(ctx context.Context, _ *struct{}) (*struct{ Body []identity.AccessToken }, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	tokens, err := h.ID.AccessTokens(ctx, p)
	if err != nil {
		return nil, err
	}
	if tokens == nil {
		tokens = []identity.AccessToken{}
	}
	return &struct{ Body []identity.AccessToken }{tokens}, nil
}

type createTokenInput struct {
	Body struct {
		Name          string   `json:"name" minLength:"1" maxLength:"100"`
		Scopes        []string `json:"scopes" minItems:"1" enum:"read,write,admin"`
		ExpiresInDays int      `json:"expiresInDays,omitempty" minimum:"0" maximum:"3650" doc:"Days until the token expires; 0: the longest lifetime the installation allows (no expiry where it sets no limit)"`
	}
}

type createTokenBody struct {
	Token       string                `json:"token" doc:"The token; it is shown only once"`
	AccessToken *identity.AccessToken `json:"accessToken"`
}

func (h *Handler) createToken(ctx context.Context, in *createTokenInput) (*struct{ Body createTokenBody }, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	// The installation may bound the lifetime of tokens: a token that never expires is a credential forever.
	st, err := h.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	days := in.Body.ExpiresInDays
	if st.MaxTokenDays > 0 {
		if days > st.MaxTokenDays {
			return nil, apperr.Invalidf("token.lifetime", "Access tokens expire after at most %d days in this installation.", st.MaxTokenDays)
		}
		if days <= 0 {
			days = st.MaxTokenDays
		}
	}
	var expires *time.Time
	if days > 0 {
		t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
		expires = &t
	}
	token, t, err := h.ID.CreateAccessToken(ctx, p, in.Body.Name, in.Body.Scopes, expires)
	if err != nil {
		return nil, err
	}
	return &struct{ Body createTokenBody }{createTokenBody{Token: token, AccessToken: t}}, nil
}

type idPath struct {
	ID int64 `path:"id"`
}

func (h *Handler) revokeToken(ctx context.Context, in *idPath) (*struct{}, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.RevokeAccessToken(ctx, p, in.ID)
}

func (h *Handler) listSSHKeys(ctx context.Context, _ *struct{}) (*struct{ Body []identity.SSHKey }, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := h.ID.SSHKeys(ctx, p)
	if err != nil {
		return nil, err
	}
	if keys == nil {
		keys = []identity.SSHKey{}
	}
	return &struct{ Body []identity.SSHKey }{keys}, nil
}

type addSSHKeyInput struct {
	Body struct {
		Name string `json:"name,omitempty" maxLength:"100" doc:"Default: the key's comment"`
		Key  string `json:"key" minLength:"1" maxLength:"16000" doc:"The public key, as in id_ed25519.pub"`
	}
}

func (h *Handler) addSSHKey(ctx context.Context, in *addSSHKeyInput) (*struct{ Body *identity.SSHKey }, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	k, err := h.ID.AddSSHKey(ctx, p, in.Body.Name, in.Body.Key)
	if err != nil {
		return nil, err
	}
	return &struct{ Body *identity.SSHKey }{k}, nil
}

func (h *Handler) deleteSSHKey(ctx context.Context, in *idPath) (*struct{}, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.DeleteSSHKey(ctx, p, in.ID)
}

func (h *Handler) listSessions(ctx context.Context, _ *struct{}) (*struct{ Body []identity.Session }, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.ID.Sessions(ctx, p)
	if err != nil {
		return nil, err
	}
	if s == nil {
		s = []identity.Session{}
	}
	return &struct{ Body []identity.Session }{s}, nil
}

func (h *Handler) endSession(ctx context.Context, in *struct {
	ID string `path:"id" minLength:"16" maxLength:"16"`
}) (*struct{}, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.EndSession(ctx, p, in.ID)
}

type endedSessions struct {
	Ended int64 `json:"ended" doc:"Number of sessions that ended"`
}

func (h *Handler) endOtherSessions(ctx context.Context, _ *struct{}) (*struct{ Body endedSessions }, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	n, err := h.ID.EndOtherSessions(ctx, p)
	if err != nil {
		return nil, err
	}
	return &struct{ Body endedSessions }{endedSessions{Ended: n}}, nil
}

type listUsersInput struct {
	Query string `query:"query" maxLength:"200" doc:"Part of the user name, display name or e-mail"`
	api.Page
}

type userList struct {
	Total int                `json:"total"`
	Items []identity.UserRef `json:"items"`
}

func (h *Handler) listUsers(ctx context.Context, in *listUsersInput) (*struct{ Body userList }, error) {
	if _, err := h.Authz.RequireGlobal(ctx, authz.UserBrowse); err != nil {
		return nil, err
	}
	users, total, err := identity.ListUsers(ctx, h.ID.Pool(), identity.UserFilter{Query: in.Query,
		Status: identity.StatusActive, Limit: in.Limit, Offset: in.Offset})
	if err != nil {
		return nil, err
	}
	out := userList{Total: total, Items: make([]identity.UserRef, 0, len(users))}
	for _, u := range users {
		out.Items = append(out.Items, u.Ref())
	}
	return &struct{ Body userList }{out}, nil
}

type profile struct {
	identity.UserRef
	Email  string     `json:"email,omitempty"`
	Groups []GroupRef `json:"groups"`
}

func (h *Handler) getUser(ctx context.Context, in *struct {
	Username string `path:"username"`
}) (*struct{ Body profile }, error) {
	if _, err := h.Authz.RequireGlobal(ctx, authz.UserBrowse); err != nil {
		return nil, err
	}
	u, err := identity.GetUserByUsername(ctx, h.ID.Pool(), in.Username)
	if err != nil {
		return nil, err
	}
	if u.Kind == identity.KindSystem {
		return nil, apperr.NotFound("user.not-found", "User %s does not exist.", in.Username)
	}
	groups, err := identity.UserGroups(ctx, h.ID.Pool(), u.ID)
	if err != nil {
		return nil, err
	}
	pr := profile{UserRef: u.Ref(), Email: u.Email, Groups: []GroupRef{}}
	for _, g := range groups {
		pr.Groups = append(pr.Groups, GroupRef{ID: g.ID, Name: g.Name})
	}
	return &struct{ Body profile }{pr}, nil
}

type groupList struct {
	Total int        `json:"total"`
	Items []GroupRef `json:"items"`
}

func (h *Handler) listGroups(ctx context.Context, in *listUsersInput) (*struct{ Body groupList }, error) {
	if _, err := h.Authz.RequireGlobal(ctx, authz.UserBrowse); err != nil {
		return nil, err
	}
	groups, total, err := identity.ListGroups(ctx, h.ID.Pool(), in.Query, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	out := groupList{Total: total, Items: make([]GroupRef, 0, len(groups))}
	for _, g := range groups {
		out.Items = append(out.Items, GroupRef{ID: g.ID, Name: g.Name})
	}
	return &struct{ Body groupList }{out}, nil
}

// EnsureBootstrapAdmin creates the configured local administrator if no administrator exists yet.
func EnsureBootstrapAdmin(ctx context.Context, id *identity.Service, cfg config.BootstrapAdmin) (bool, error) {
	if cfg.Username == "" {
		return false, nil
	}
	created := false
	err := db.InTx(ctx, id.Pool(), func(tx pgx.Tx) error {
		// Nodes that start for the first time together create the administrator once (the others find it).
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7366300)"); err != nil {
			return err
		}
		var admins int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM global_grants WHERE permission = 'admin'").Scan(&admins); err != nil {
			return err
		}
		if admins > 0 {
			return nil
		}
		u, err := identity.GetUserByUsername(ctx, tx, cfg.Username)
		if apperr.Is(err, "user.not-found") {
			u, err = id.CreateLocalUser(ctx, tx, strings.ToLower(cfg.Username), cfg.Username, cfg.Email, cfg.Password)
		}
		if err != nil {
			return err
		}
		created = true
		return authz.AddGrant(ctx, tx, authz.Grant{Permission: authz.Admin, PrincipalType: authz.PrincipalUser, PrincipalID: u.ID})
	})
	return created, err
}
