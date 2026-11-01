// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package admin provides the administration API: users, groups, global permissions, settings, audit log and
// system information.
package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	netmail "net/mail"
	"runtime"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/live"
	"kogge.de/kogge/internal/platform/mail"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/privacy"
)

// Handler serves the administration API.
type Handler struct {
	Pool     *pgxpool.Pool
	ID       *identity.Service
	Authz    *authz.Service
	Settings *settings.Service
	Config   *config.Config
	Version  string
	// Commit and BuildDate describe the build (internal/version); empty and zero when unknown.
	Commit    string
	BuildDate time.Time
	Started   time.Time
	// Mail sends e-mail; nil when it is not configured.
	Mail *mail.Sender
	// Figures are the modules' numbers for the overview.
	Figures []Figures
}

// Register adds the operations.
func (h *Handler) Register(a huma.API) {
	tag := "Administration"
	huma.Register(a, api.Op("admin-list-users", http.MethodGet, "/api/v1/admin/users", "List users", tag), h.listUsers)
	huma.Register(a, api.Op("admin-create-user", http.MethodPost, "/api/v1/admin/users", "Create a local user", tag), h.createUser)
	huma.Register(a, api.Op("admin-get-user", http.MethodGet, "/api/v1/admin/users/{id}", "Get a user", tag), h.getUser)
	huma.Register(a, api.Op("admin-update-user", http.MethodPatch, "/api/v1/admin/users/{id}", "Change a user", tag), h.updateUser)
	huma.Register(a, api.Op("admin-set-password", http.MethodPut, "/api/v1/admin/users/{id}/password", "Set a local password", tag),
		h.setPassword)
	huma.Register(a, api.Op("admin-end-sessions", http.MethodDelete, "/api/v1/admin/users/{id}/sessions", "End all sessions of a user", tag),
		h.endSessions)
	huma.Register(a, api.Op("admin-reset-two-factor", http.MethodDelete, "/api/v1/admin/users/{id}/two-factor",
		"Turn off a user's two-factor authentication (lost device); the user's sessions end", tag), h.resetTwoFactor)
	huma.Register(a, api.Op("admin-export-user", http.MethodGet, "/api/v1/admin/users/{id}/export",
		"Export what Kogge holds about a person (right of access)", tag), h.exportUser)
	huma.Register(a, api.Op("admin-anonymise-user", http.MethodPost, "/api/v1/admin/users/{id}/anonymise",
		"Anonymise a person (right to erasure); cannot be undone", tag), h.anonymiseUser)
	huma.Register(a, api.Op("admin-get-credentials", http.MethodGet, "/api/v1/admin/users/{id}/credentials",
		"Sessions, access tokens and SSH keys of a user", tag), h.getCredentials)
	huma.Register(a, api.Op("admin-revoke-access", http.MethodDelete, "/api/v1/admin/users/{id}/credentials",
		"End all sessions and revoke all access tokens and SSH keys of a user", tag), h.revokeAccess)
	huma.Register(a, api.Op("admin-revoke-token", http.MethodDelete, "/api/v1/admin/users/{id}/tokens/{tokenId}",
		"Revoke an access token of a user", tag), h.revokeToken)
	huma.Register(a, api.Op("admin-delete-ssh-key", http.MethodDelete, "/api/v1/admin/users/{id}/ssh-keys/{keyId}",
		"Delete an SSH key of a user", tag), h.deleteSSHKey)
	huma.Register(a, api.Op("admin-set-sso-link", http.MethodPut, "/api/v1/admin/users/{id}/sso-link",
		"Let the user of the identity provider with this user name take the account over at the first sign-in", tag), h.setSSOLink)
	huma.Register(a, api.Op("admin-remove-sso-link", http.MethodDelete, "/api/v1/admin/users/{id}/sso-link",
		"Withdraw the single sign-on link of an account", tag), h.removeSSOLink)
	huma.Register(a, api.Op("admin-list-groups", http.MethodGet, "/api/v1/admin/groups", "List groups", tag), h.listGroups)
	huma.Register(a, api.Op("admin-create-group", http.MethodPost, "/api/v1/admin/groups", "Create a group", tag), h.createGroup)
	huma.Register(a, api.Op("admin-update-group", http.MethodPatch, "/api/v1/admin/groups/{id}", "Change a group", tag), h.updateGroup)
	huma.Register(a, api.Op("admin-delete-group", http.MethodDelete, "/api/v1/admin/groups/{id}", "Delete a group", tag), h.deleteGroup)
	huma.Register(a, api.Op("admin-list-members", http.MethodGet, "/api/v1/admin/groups/{id}/members", "Members of a group", tag),
		h.listMembers)
	huma.Register(a, api.Op("admin-add-member", http.MethodPut, "/api/v1/admin/groups/{id}/members/{userId}", "Add a member", tag),
		h.addMember)
	huma.Register(a, api.Op("admin-remove-member", http.MethodDelete, "/api/v1/admin/groups/{id}/members/{userId}", "Remove a member", tag),
		h.removeMember)
	huma.Register(a, api.Op("admin-list-global-permissions", http.MethodGet, "/api/v1/admin/global-permissions",
		"Global permissions and their grants", tag), h.listGrants)
	huma.Register(a, api.Op("admin-grant-global-permission", http.MethodPost, "/api/v1/admin/global-permissions",
		"Grant a global permission", tag), h.addGrant)
	huma.Register(a, api.Op("admin-revoke-global-permission", http.MethodDelete, "/api/v1/admin/global-permissions",
		"Revoke a global permission", tag), h.removeGrant)
	huma.Register(a, api.Op("admin-get-directory-sync", http.MethodGet, "/api/v1/admin/directory-sync", "State of the directory sync", tag),
		h.getDirectorySync)
	huma.Register(a, api.Op("admin-get-sso", http.MethodGet, "/api/v1/admin/sso",
		"The configuration of single sign-on and the addresses to register at the identity provider", tag),
		func(ctx context.Context, _ *struct{}) (*struct{ Body identity.SSOInfo }, error) {
			if _, err := h.admin(ctx); err != nil {
				return nil, err
			}
			return &struct{ Body identity.SSOInfo }{h.ID.SSO()}, nil
		})
	huma.Register(a, api.Op("admin-check-sso", http.MethodPost, "/api/v1/admin/sso/check",
		"Check the connection to the identity provider", tag),
		func(ctx context.Context, _ *struct{}) (*struct{ Body []identity.SSOCheck }, error) {
			if _, err := h.admin(ctx); err != nil {
				return nil, err
			}
			return &struct{ Body []identity.SSOCheck }{h.ID.CheckSSO(ctx)}, nil
		})
	huma.Register(a, api.Op("admin-run-directory-sync", http.MethodPost, "/api/v1/admin/directory-sync", "Run the directory sync now", tag),
		h.runDirectorySync)
	huma.Register(a, api.Op("admin-get-settings", http.MethodGet, "/api/v1/admin/settings", "Instance settings", tag), h.getSettings)
	huma.Register(a, api.Op("admin-update-settings", http.MethodPut, "/api/v1/admin/settings", "Change instance settings", tag),
		h.putSettings)
	huma.Register(a, api.Op("admin-list-audit", http.MethodGet, "/api/v1/admin/audit", "Audit log", tag), h.listAudit)
	huma.Register(a, api.Op("admin-get-audit-forwarding", http.MethodGet, "/api/v1/admin/audit/forwarding",
		"State of the forwarding of audit events to a SIEM", tag), h.auditForwarding)
	huma.Register(a, api.Op("admin-system", http.MethodGet, "/api/v1/admin/system", "System information", tag), h.system)
	huma.Register(a, api.Op("admin-overview", http.MethodGet, "/api/v1/admin/overview",
		"The installation at a glance: health, use, security and what needs attention", tag), h.overview)
	huma.Register(a, api.Op("admin-test-mail", http.MethodPost, "/api/v1/admin/mail/test", "Send a test e-mail to the caller", tag), h.testMail)
}

func (h *Handler) admin(ctx context.Context) (*identity.Principal, error) {
	return h.Authz.RequireGlobal(ctx, authz.Admin)
}

type userListInput struct {
	Query  string `query:"query" maxLength:"200"`
	Status string `query:"status" enum:"active,disabled,"`
	api.Page
}

type userList struct {
	Total int              `json:"total"`
	Items []*identity.User `json:"items"`
}

func (h *Handler) listUsers(ctx context.Context, in *userListInput) (*struct{ Body userList }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	users, total, err := identity.ListUsers(ctx, h.Pool, identity.UserFilter{Query: in.Query, Status: in.Status,
		Limit: in.Limit, Offset: in.Offset})
	if err != nil {
		return nil, err
	}
	if users == nil {
		users = []*identity.User{}
	}
	return &struct{ Body userList }{userList{Total: total, Items: users}}, nil
}

type createUserInput struct {
	Body struct {
		Username    string `json:"username" minLength:"1" maxLength:"64" pattern:"^[a-z0-9._@-]+$"`
		DisplayName string `json:"displayName" minLength:"1" maxLength:"200"`
		Email       string `json:"email,omitempty" maxLength:"320"`
		Password    string `json:"password,omitempty" maxLength:"1024" doc:"Local password; empty for users who sign in through the identity provider"`
	}
}

type userOutput struct {
	Body *identity.User
}

func (h *Handler) createUser(ctx context.Context, in *createUserInput) (*userOutput, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	if e := strings.TrimSpace(in.Body.Email); e != "" && !validEmail(e) {
		return nil, apperr.Invalid("user.email", "The e-mail address is not valid.",
			apperr.FieldError{Location: "body.email", Value: e})
	}
	var u *identity.User
	err = db.InTx(ctx, h.Pool, func(tx pgx.Tx) error {
		var err error
		u, err = h.ID.CreateLocalUser(ctx, tx, in.Body.Username, in.Body.DisplayName, strings.TrimSpace(in.Body.Email), in.Body.Password)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "user.create", TargetType: "user",
			TargetID: audit.ID(u.ID), Details: map[string]any{"username": u.Username}})
	})
	if err != nil {
		return nil, err
	}
	return &userOutput{u}, nil
}

func validEmail(e string) bool {
	at := strings.LastIndexByte(e, '@')
	return at > 0 && at < len(e)-3 && strings.Contains(e[at:], ".") && !strings.ContainsAny(e, " \t\r\n<>")
}

type userIDPath struct {
	ID int64 `path:"id"`
}

func (h *Handler) getUser(ctx context.Context, in *userIDPath) (*userOutput, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	u, err := identity.GetUser(ctx, h.Pool, in.ID)
	if err != nil {
		return nil, err
	}
	return &userOutput{u}, nil
}

type updateUserInput struct {
	ID   int64 `path:"id"`
	Body struct {
		DisplayName *string `json:"displayName,omitempty" minLength:"1" maxLength:"200"`
		Email       *string `json:"email,omitempty" maxLength:"320"`
		Status      *string `json:"status,omitempty" enum:"active,disabled"`
	}
}

func (h *Handler) updateUser(ctx context.Context, in *updateUserInput) (*userOutput, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	if in.Body.Status != nil && *in.Body.Status == identity.StatusDisabled && in.ID == actor.UserID() {
		return nil, apperr.Conflict("user.self-disable", "You cannot disable your own account.")
	}
	var u *identity.User
	err = db.InTx(ctx, h.Pool, func(tx pgx.Tx) error {
		var err error
		u, err = identity.UpdateUser(ctx, tx, in.ID, identity.UserUpdate{DisplayName: in.Body.DisplayName,
			Email: in.Body.Email, Status: in.Body.Status})
		if err != nil {
			return err
		}
		if u.Status == identity.StatusDisabled {
			if err := identity.DeleteUserSessions(ctx, tx, u.ID); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "user.update", TargetType: "user",
			TargetID: audit.ID(u.ID), Details: map[string]any{"status": u.Status}})
	})
	if err != nil {
		return nil, err
	}
	return &userOutput{u}, nil
}

type setPasswordInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Password string `json:"password" minLength:"12" maxLength:"1024"`
	}
}

func (h *Handler) setPassword(ctx context.Context, in *setPasswordInput) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.SetPassword(ctx, actor, in.ID, in.Body.Password)
}

func (h *Handler) resetTwoFactor(ctx context.Context, in *userIDPath) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.ResetTwoFactor(ctx, actor, in.ID)
}

func (h *Handler) endSessions(ctx context.Context, in *userIDPath) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.EndUserSessions(ctx, actor, in.ID)
}

func (h *Handler) exportUser(ctx context.Context, in *userIDPath) (*struct {
	ContentDisposition string `header:"Content-Disposition"`
	Body               *privacy.Export
}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	out, err := privacy.ExportUser(ctx, h.Pool, in.ID)
	if err != nil {
		return nil, err
	}
	if err := audit.Record(ctx, h.Pool, audit.Entry{ActorID: actor.UserID(), Action: "user.export", TargetType: "user",
		TargetID: audit.ID(in.ID)}); err != nil {
		return nil, err
	}
	return &struct {
		ContentDisposition string `header:"Content-Disposition"`
		Body               *privacy.Export
	}{fmt.Sprintf(`attachment; filename="kogge-user-%d.json"`, in.ID), out}, nil
}

type anonymiseInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Confirm string `json:"confirm" minLength:"1" maxLength:"200" doc:"The user name of the person, to confirm"`
	}
}

func (h *Handler) anonymiseUser(ctx context.Context, in *anonymiseInput) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	u, err := identity.GetUser(ctx, h.Pool, in.ID)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(in.Body.Confirm), u.Username) {
		return nil, apperr.Invalid("user.anonymise-confirm", "Type the user name of the person to confirm.")
	}
	return nil, privacy.AnonymiseUser(ctx, h.Pool, actor, in.ID)
}

func (h *Handler) getCredentials(ctx context.Context, in *userIDPath) (*struct{ Body *identity.Credentials }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	c, err := h.ID.UserCredentials(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &struct{ Body *identity.Credentials }{c}, nil
}

func (h *Handler) revokeAccess(ctx context.Context, in *userIDPath) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.RevokeUserAccess(ctx, actor, in.ID)
}

func (h *Handler) revokeToken(ctx context.Context, in *struct {
	ID      int64 `path:"id"`
	TokenID int64 `path:"tokenId"`
}) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.RevokeUserToken(ctx, actor, in.ID, in.TokenID)
}

func (h *Handler) deleteSSHKey(ctx context.Context, in *struct {
	ID    int64 `path:"id"`
	KeyID int64 `path:"keyId"`
}) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.DeleteUserSSHKey(ctx, actor, in.ID, in.KeyID)
}

type ssoLinkInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Username string `json:"username" minLength:"1" maxLength:"200" doc:"The person's user name at the identity provider"`
	}
}

func (h *Handler) setSSOLink(ctx context.Context, in *ssoLinkInput) (*userOutput, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	u, err := h.ID.SetSSOLink(ctx, actor, in.ID, in.Body.Username)
	if err != nil {
		return nil, err
	}
	return &userOutput{u}, nil
}

func (h *Handler) removeSSOLink(ctx context.Context, in *userIDPath) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	return nil, h.ID.RemoveSSOLink(ctx, actor, in.ID)
}

type groupListInput struct {
	Query string `query:"query" maxLength:"200"`
	api.Page
}

type groupList struct {
	Total int               `json:"total"`
	Items []*identity.Group `json:"items"`
}

func (h *Handler) listGroups(ctx context.Context, in *groupListInput) (*struct{ Body groupList }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	groups, total, err := identity.ListGroups(ctx, h.Pool, in.Query, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	if groups == nil {
		groups = []*identity.Group{}
	}
	return &struct{ Body groupList }{groupList{Total: total, Items: groups}}, nil
}

type groupBody struct {
	Name        string `json:"name" minLength:"1" maxLength:"200"`
	Description string `json:"description,omitempty" maxLength:"1000"`
}

type groupOutput struct {
	Body *identity.Group
}

func (h *Handler) createGroup(ctx context.Context, in *struct{ Body groupBody }) (*groupOutput, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	var g *identity.Group
	err = db.InTx(ctx, h.Pool, func(tx pgx.Tx) error {
		var err error
		if g, err = identity.CreateGroup(ctx, tx, strings.TrimSpace(in.Body.Name), in.Body.Description, "local"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "group.create", TargetType: "group",
			TargetID: audit.ID(g.ID), Details: map[string]any{"name": g.Name}})
	})
	if err != nil {
		return nil, err
	}
	return &groupOutput{g}, nil
}

func (h *Handler) updateGroup(ctx context.Context, in *struct {
	ID   int64 `path:"id"`
	Body groupBody
}) (*groupOutput, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	var g *identity.Group
	err = db.InTx(ctx, h.Pool, func(tx pgx.Tx) error {
		cur, err := identity.GetGroup(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		if cur.Source != "local" && !strings.EqualFold(cur.Name, in.Body.Name) {
			return apperr.Conflict("group.provider", "Groups from the identity provider cannot be renamed.")
		}
		if g, err = identity.UpdateGroup(ctx, tx, in.ID, strings.TrimSpace(in.Body.Name), in.Body.Description); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "group.update", TargetType: "group",
			TargetID: audit.ID(g.ID), Details: map[string]any{"name": g.Name}})
	})
	if err != nil {
		return nil, err
	}
	return &groupOutput{g}, nil
}

func (h *Handler) deleteGroup(ctx context.Context, in *userIDPath) (*struct{}, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, h.Pool, func(tx pgx.Tx) error {
		g, err := identity.GetGroup(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		// Through RemoveGrant: the last administrator grant must not go with the group.
		rows, err := tx.Query(ctx, "SELECT permission FROM global_grants WHERE principal_type = 'group' AND principal_id = $1", in.ID)
		if err != nil {
			return err
		}
		perms, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, perm := range perms {
			if err := authz.RemoveGrant(ctx, tx, authz.Grant{Permission: perm, PrincipalType: authz.PrincipalGroup, PrincipalID: in.ID}); err != nil {
				return err
			}
		}
		if err := identity.DeleteGroup(ctx, tx, in.ID); err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "group.delete", TargetType: "group",
			TargetID: audit.ID(in.ID), Details: map[string]any{"name": g.Name}})
	})
	return nil, err
}

type memberList struct {
	Total int                `json:"total"`
	Items []identity.UserRef `json:"items"`
}

func (h *Handler) listMembers(ctx context.Context, in *struct {
	ID int64 `path:"id"`
	api.Page
}) (*struct{ Body memberList }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	users, total, err := identity.GroupMembers(ctx, h.Pool, in.ID, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	out := memberList{Total: total, Items: make([]identity.UserRef, 0, len(users))}
	for _, u := range users {
		out.Items = append(out.Items, u.Ref())
	}
	return &struct{ Body memberList }{out}, nil
}

type memberPath struct {
	ID     int64 `path:"id"`
	UserID int64 `path:"userId"`
}

func (h *Handler) changeMember(ctx context.Context, in *memberPath, add bool) error {
	actor, err := h.admin(ctx)
	if err != nil {
		return err
	}
	return db.InTx(ctx, h.Pool, func(tx pgx.Tx) error {
		g, err := identity.GetGroup(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		if g.Source != "local" {
			return apperr.Conflict("group.provider", "Members of groups from the identity provider are managed there.")
		}
		action := "group.member.remove"
		if add {
			action = "group.member.add"
			err = identity.AddMember(ctx, tx, in.ID, in.UserID)
		} else {
			err = identity.RemoveMember(ctx, tx, in.ID, in.UserID)
		}
		if err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: action, TargetType: "group",
			TargetID: audit.ID(in.ID), Details: map[string]any{"user": in.UserID}})
	})
}

func (h *Handler) addMember(ctx context.Context, in *memberPath) (*struct{}, error) {
	return nil, h.changeMember(ctx, in, true)
}

func (h *Handler) removeMember(ctx context.Context, in *memberPath) (*struct{}, error) {
	return nil, h.changeMember(ctx, in, false)
}

type globalPermission struct {
	authz.PermissionInfo
	Grants []authz.Grant `json:"grants"`
}

func (h *Handler) listGrants(ctx context.Context, _ *struct{}) (*struct{ Body []globalPermission }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	grants, err := authz.ListGrants(ctx, h.Pool)
	if err != nil {
		return nil, err
	}
	out := make([]globalPermission, 0, len(authz.GlobalPermissions))
	for _, p := range authz.GlobalPermissions {
		gp := globalPermission{PermissionInfo: p, Grants: []authz.Grant{}}
		for _, g := range grants {
			if g.Permission == p.Key {
				gp.Grants = append(gp.Grants, g)
			}
		}
		out = append(out, gp)
	}
	return &struct{ Body []globalPermission }{out}, nil
}

type grantInput struct {
	Body authz.Grant
}

func (h *Handler) changeGrant(ctx context.Context, g authz.Grant, add bool) error {
	actor, err := h.admin(ctx)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, h.Pool, func(tx pgx.Tx) error {
		action := "permission.global.revoke"
		if add {
			action = "permission.global.grant"
			err = authz.AddGrant(ctx, tx, g)
		} else {
			err = authz.RemoveGrant(ctx, tx, g)
		}
		if err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "authz"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: action, TargetType: "permission",
			TargetID: g.Permission, Details: map[string]any{"principalType": g.PrincipalType, "principalId": g.PrincipalID}})
	})
	if err == nil {
		h.Authz.Invalidate()
	}
	return err
}

func (h *Handler) addGrant(ctx context.Context, in *grantInput) (*struct{}, error) {
	return nil, h.changeGrant(ctx, in.Body, true)
}

func (h *Handler) removeGrant(ctx context.Context, in *struct {
	Permission    string `query:"permission" required:"true"`
	PrincipalType string `query:"principalType" required:"true" enum:"user,group,logged-in,anyone"`
	PrincipalID   int64  `query:"principalId"`
}) (*struct{}, error) {
	return nil, h.changeGrant(ctx, authz.Grant{Permission: in.Permission, PrincipalType: in.PrincipalType,
		PrincipalID: in.PrincipalID}, false)
}

func (h *Handler) getSettings(ctx context.Context, _ *struct{}) (*struct{ Body settings.Instance }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	st, err := h.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &struct{ Body settings.Instance }{st}, nil
}

func (h *Handler) putSettings(ctx context.Context, in *struct{ Body settings.Instance }) (*struct{ Body settings.Instance }, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := time.LoadLocation(in.Body.DefaultTimeZone); err != nil || in.Body.DefaultTimeZone == "" {
		return nil, apperr.Invalid("settings.time-zone", "Unknown time zone.")
	}
	if strings.TrimSpace(in.Body.Name) == "" {
		return nil, apperr.Invalid("settings.name", "The name must not be empty.")
	}
	if in.Body.MaxLFSObjectMB == 0 {
		// Optional: left out, it is the default (as it reads after saving).
		in.Body.MaxLFSObjectMB = settings.Defaults().MaxLFSObjectMB
	}
	current, err := h.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	// Who requires a second factor has one: otherwise this very session could only set one up.
	if in.Body.RequireTwoFactor && !current.RequireTwoFactor && actor.SignIn == "password" && !actor.User.TwoFactor {
		return nil, apperr.Conflict("settings.two-factor-own", "Set up two-factor authentication for your own account before you require it.")
	}
	err = db.InTx(ctx, h.Pool, func(tx pgx.Tx) error {
		if err := h.Settings.Save(ctx, tx, in.Body, actor.UserID()); err != nil {
			return err
		}
		if err := live.InvalidateCache(ctx, tx, "settings"); err != nil {
			return err
		}
		details := map[string]any{}
		if in.Body.RequireTwoFactor != current.RequireTwoFactor {
			details["requireTwoFactor"] = in.Body.RequireTwoFactor
		}
		// Who raised or switched off a limit is for the audit log to tell: the changed limits with their old values.
		before, after := current.Limits(), in.Body.Limits()
		for name, v := range after {
			if before[name] != v {
				details[name] = map[string]int{"from": before[name], "to": v}
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "settings.update", TargetType: "settings",
			TargetID: "instance", Details: details})
	})
	if err != nil {
		return nil, err
	}
	// This node at once; the others (and this one again) at the notification.
	h.Settings.Invalidate()
	return &struct{ Body settings.Instance }{in.Body}, nil
}

type auditInput struct {
	Action  string    `query:"action" maxLength:"100" doc:"Prefix of the action, e.g. auth."`
	ActorID int64     `query:"actorId"`
	Target  string    `query:"target" maxLength:"200" doc:"type:id"`
	From    time.Time `query:"from"`
	To      time.Time `query:"to"`
	Before  int64     `query:"before" doc:"Return events with smaller ids (paging)"`
	Limit   int       `query:"limit" minimum:"1" maximum:"500" default:"100"`
}

func (h *Handler) listAudit(ctx context.Context, in *auditInput) (*struct{ Body []audit.Event }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	events, err := audit.List(ctx, h.Pool, audit.Filter{Action: in.Action, ActorID: in.ActorID, Target: in.Target,
		From: in.From, To: in.To, Before: in.Before, Limit: in.Limit})
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []audit.Event{}
	}
	return &struct{ Body []audit.Event }{events}, nil
}

func (h *Handler) auditForwarding(ctx context.Context, _ *struct{}) (*struct{ Body audit.ForwardStatus }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	st, err := audit.Forwarding(ctx, h.Pool)
	if err != nil {
		return nil, err
	}
	st.Configured = h.Config.AuditForward.Enabled()
	if st.Configured && st.Target == "" {
		st.Target = audit.TargetName(h.Config.AuditForward.URL)
	}
	return &struct{ Body audit.ForwardStatus }{st}, nil
}

type systemInfo struct {
	Version       string          `json:"version"`
	Commit        string          `json:"commit,omitempty" doc:"The Git commit Kogge was built from"`
	BuildDate     *time.Time      `json:"buildDate,omitempty" doc:"When this build was made (for releases, the date of its commit)"`
	GoVersion     string          `json:"goVersion"`
	Database      string          `json:"database"`
	StartedAt     time.Time       `json:"startedAt"`
	Roles         []string        `json:"roles"`
	PublicURL     string          `json:"publicUrl"`
	SSO           bool            `json:"sso"`
	Mail          bool            `json:"mail"`
	Users         int             `json:"users"`
	Jobs          map[string]int  `json:"jobs" doc:"Jobs by state"`
	Features      map[string]bool `json:"features"`
	DataDir       string          `json:"dataDir"`
	DataFreeBytes uint64          `json:"dataFreeBytes"`
}

func (h *Handler) testMail(ctx context.Context, _ *struct{}) (*struct{}, error) {
	p, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	if h.Mail == nil {
		return nil, apperr.Conflict("mail.not-configured", "E-mail is not configured (KOGGE_SMTP_HOST).")
	}
	if p.User.Email == "" {
		return nil, apperr.Invalid("mail.no-address", "Your user has no e-mail address.")
	}
	st, _ := h.Settings.Get(ctx)
	err = h.Mail.Send(ctx, mail.Message{To: netmail.Address{Name: p.User.DisplayName, Address: p.User.Email},
		Subject: "[" + st.Name + "] Test e-mail / Test-E-Mail",
		Text: "This test e-mail shows that " + st.Name + " can send e-mail.\n\n" +
			"Diese Test-E-Mail zeigt, dass " + st.Name + " E-Mails versenden kann.\n"})
	if err != nil {
		return nil, apperr.Invalidf("mail.failed", "The e-mail could not be sent: %v", err)
	}
	return nil, nil
}

func (h *Handler) system(ctx context.Context, _ *struct{}) (*struct{ Body systemInfo }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	info := systemInfo{Version: h.Version, Commit: h.Commit, GoVersion: runtime.Version(), StartedAt: h.Started,
		PublicURL: h.Config.PublicURL.String(), SSO: h.Config.OIDC.Enabled(), Mail: h.Config.SMTP.Enabled(),
		Jobs: map[string]int{}, Features: map[string]bool{}, DataDir: h.Config.DataDir}
	info.DataFreeBytes, _ = storage.Space(h.Config.DataDir)
	if !h.BuildDate.IsZero() {
		info.BuildDate = &h.BuildDate
	}
	for r := range h.Config.Roles {
		info.Roles = append(info.Roles, string(r))
	}
	if err := h.Pool.QueryRow(ctx, "SELECT version()").Scan(&info.Database); err != nil {
		return nil, err
	}
	if err := h.Pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE status = 'active' AND kind = 'person'").Scan(&info.Users); err != nil {
		return nil, err
	}
	rows, err := h.Pool.Query(ctx, "SELECT state::text, count(*) FROM river_job GROUP BY state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		info.Jobs[state] = n
	}
	return &struct{ Body systemInfo }{info}, rows.Err()
}

// DirectorySync is the state of the directory sync.
type DirectorySync struct {
	Configured bool                 `json:"configured"`
	Last       *identity.SyncResult `json:"last,omitempty"`
}

func (h *Handler) getDirectorySync(ctx context.Context, _ *struct{}) (*struct{ Body DirectorySync }, error) {
	if _, err := h.admin(ctx); err != nil {
		return nil, err
	}
	last, err := h.ID.LastSync(ctx)
	if err != nil {
		return nil, err
	}
	return &struct{ Body DirectorySync }{DirectorySync{Configured: h.ID.SyncConfigured(), Last: last}}, nil
}

func (h *Handler) runDirectorySync(ctx context.Context, in *struct {
	Force bool `query:"force" doc:"Also when the sync would disable many users"`
}) (*struct{ Body identity.SyncResult }, error) {
	actor, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.ID.SyncDirectory(ctx, in.Force)
	if err != nil {
		return nil, err
	}
	// The sync recorded its changes in its own transaction; this run's event reports that the administrator started
	// it and cannot undo it any more (audit.Report).
	audit.Report(ctx, h.Pool, slog.Default(), audit.Entry{ActorID: actor.UserID(), Action: "directory.sync",
		Details: map[string]any{"created": r.Created, "disabled": r.Disabled, "enabled": r.Enabled, "force": in.Force}})
	return &struct{ Body identity.SyncResult }{*r}, nil
}
