// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// The directory sync keeps users and groups in step with the identity provider's directory. It
// creates users before their first sign-in, so that they can be assigned and mentioned; disables users that the
// directory no longer has or has disabled (their sessions end, and tokens, SSH keys and Git passwords stop
// working with the status); enables them again when they come back; and sets the members of the provider's
// groups. It reads the Keycloak admin API as the service account kogge-sync.

// SyncResult is the outcome of a run of the directory sync.
type SyncResult struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Users    int       `json:"users" doc:"Users in the directory"`
	Created  int       `json:"created"`
	Disabled int       `json:"disabled"`
	Enabled  int       `json:"enabled"`
	Groups   int       `json:"groups"`
	Error    string    `json:"error,omitempty"`
}

type dirUser struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	FirstName     string `json:"firstName"`
	LastName      string `json:"lastName"`
	Enabled       bool   `json:"enabled"`
}

type dirGroup struct {
	ID            string     `json:"id"`
	Path          string     `json:"path"`
	SubGroups     []dirGroup `json:"subGroups"`
	SubGroupCount int        `json:"subGroupCount"`
}

const (
	syncPage       = 200
	syncResultKey  = "directory-sync"
	syncMaxEntries = 200000
)

// errSyncGuard stops a run that would disable a large part of the users: an empty or broken answer of the directory
// must not lock everybody out.
var errSyncGuard = errors.New("guard")

// SyncConfigured reports whether the directory sync is set up.
func (s *Service) SyncConfigured() bool { return s.oidc != nil && s.cfg.OIDC.SyncEnabled() }

// SyncDirectory runs the directory sync. Without force, it refuses to disable more than a quarter of the provider's
// users (at least ten) in one run.
func (s *Service) SyncDirectory(ctx context.Context, force bool) (*SyncResult, error) {
	if !s.SyncConfigured() {
		return nil, apperr.New(http.StatusConflict, "directory.not-configured",
			"The directory sync is not configured (KOGGE_SYNC_CLIENT_SECRET with single sign-on through Keycloak).")
	}
	r := &SyncResult{Started: time.Now().UTC()}
	err := s.syncDirectory(ctx, r, force)
	r.Finished = time.Now().UTC()
	if err != nil {
		r.Error = err.Error()
		if errors.Is(err, errSyncGuard) {
			r.Error = fmt.Sprintf("The sync would disable %d of the provider's users; nothing was changed. Check the directory, "+
				"or run the sync with force.", r.Disabled)
		}
	}
	if serr := s.saveSyncResult(ctx, r); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		s.log.Warn("directory sync failed", "error", r.Error)
		return r, apperr.New(http.StatusBadGateway, "directory.sync", "%s", r.Error)
	}
	return r, nil
}

func (s *Service) syncDirectory(ctx context.Context, r *SyncResult, force bool) error {
	users, groups, members, err := s.oidc.readDirectory(ctx, s.cfg.OIDC.SyncClientID, s.cfg.OIDC.SyncClientSecret, s.cfg.OIDC.AdminURL())
	if err != nil {
		return err
	}
	r.Users, r.Groups = len(users), len(groups)
	if len(users) == 0 && !force {
		return errors.New("the directory returned no users; nothing was changed")
	}
	issuer := s.cfg.OIDC.Issuer
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		type known struct {
			id          int64
			status      string
			byDirectory bool
		}
		rows, err := tx.Query(ctx, `SELECT id, oidc_subject, status, disabled_by_directory FROM users
			WHERE oidc_issuer = $1 AND status <> 'anonymised'`, issuer)
		if err != nil {
			return err
		}
		have := map[string]known{}
		for rows.Next() {
			var k known
			var sub string
			if err := rows.Scan(&k.id, &sub, &k.status, &k.byDirectory); err != nil {
				rows.Close()
				return err
			}
			have[sub] = k
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		ids := map[string]int64{} // directory id → user, for the groups
		seen := map[string]bool{}
		for _, du := range users {
			seen[du.ID] = true
			k, exists := have[du.ID]
			if !du.Enabled {
				if exists && k.status == StatusActive {
					if err := s.directoryDisable(ctx, tx, k.id, "disabled in the directory"); err != nil {
						return err
					}
					r.Disabled++
				}
				continue
			}
			u, created, err := s.provision(ctx, tx, &Claims{Issuer: issuer, Subject: du.ID, PreferredUsername: du.Username,
				GivenName: du.FirstName, FamilyName: du.LastName, Email: du.Email, EmailVerified: du.EmailVerified})
			if err != nil {
				return fmt.Errorf("user %s: %w", du.Username, err)
			}
			if created {
				r.Created++
				if err := audit.Record(ctx, tx, audit.Entry{ActorID: SystemUserID, Action: "user.create", TargetType: "user",
					TargetID: audit.ID(u.ID), Details: map[string]any{"by": "directory"}}); err != nil {
					return err
				}
			}
			if exists && k.status == StatusDisabled && k.byDirectory {
				if _, err := tx.Exec(ctx, "UPDATE users SET status = 'active', disabled_by_directory = false, updated_at = now() WHERE id = $1",
					k.id); err != nil {
					return err
				}
				if err := audit.Record(ctx, tx, audit.Entry{ActorID: SystemUserID, Action: "user.update", TargetType: "user",
					TargetID: audit.ID(k.id), Details: map[string]any{"status": StatusActive, "by": "directory"}}); err != nil {
					return err
				}
				r.Enabled++
			}
			ids[du.ID] = u.ID
		}
		for sub, k := range have {
			if !seen[sub] && k.status == StatusActive {
				if err := s.directoryDisable(ctx, tx, k.id, "removed from the directory"); err != nil {
					return err
				}
				r.Disabled++
			}
		}
		if limit := max(10, len(have)/4); r.Disabled > limit && !force {
			return errSyncGuard
		}

		// Members of the provider's groups, by the groups' paths as in the groups claim of sign-ins.
		names := map[string][]string{}
		for _, g := range groups {
			name := strings.Trim(g.Path, "/ ")
			if name == "" {
				continue
			}
			if _, err := tx.Exec(ctx, "INSERT INTO groups (name, source) VALUES ($1, 'oidc') ON CONFLICT DO NOTHING", name); err != nil {
				return err
			}
			for _, m := range members[g.ID] {
				names[m] = append(names[m], name)
			}
		}
		for sub, uid := range ids {
			if err := SyncProviderGroups(ctx, tx, uid, names[sub]); err != nil {
				return err
			}
		}
		return nil
	})
}

// directoryDisable disables a user on behalf of the directory and ends their sessions.
func (s *Service) directoryDisable(ctx context.Context, tx pgx.Tx, id int64, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE users SET status = 'disabled', disabled_by_directory = true, updated_at = now()
		WHERE id = $1`, id); err != nil {
		return err
	}
	if err := DeleteUserSessions(ctx, tx, id); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{ActorID: SystemUserID, Action: "user.update", TargetType: "user", TargetID: audit.ID(id),
		Details: map[string]any{"status": StatusDisabled, "by": "directory", "reason": reason}})
}

func (s *Service) saveSyncResult(ctx context.Context, r *SyncResult) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, syncResultKey, b)
	return err
}

// LastSync returns the result of the latest run of the directory sync, or nil.
func (s *Service) LastSync(ctx context.Context) (*SyncResult, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, "SELECT value FROM settings WHERE key = $1", syncResultKey).Scan(&raw)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r SyncResult
	return &r, json.Unmarshal(raw, &r)
}

// readDirectory reads the users, the groups (flattened, with paths) and the members of each group.
func (o *OIDC) readDirectory(ctx context.Context, clientID, secret, adminURL string) ([]dirUser, []dirGroup, map[string][]string, error) {
	if err := o.init(ctx); err != nil {
		return nil, nil, nil, err
	}
	token, err := o.serviceToken(ctx, clientID, secret)
	if err != nil {
		return nil, nil, nil, err
	}
	base := o.internalize(adminURL)
	get := func(path string, v any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		resp, err := o.client.Do(req)
		if err != nil {
			return fmt.Errorf("directory: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("directory: %s answered %d", path, resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
	}
	page := func(path string, first int) string {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		return path + sep + "first=" + strconv.Itoa(first) + "&max=" + strconv.Itoa(syncPage)
	}

	var users []dirUser
	for first := 0; ; first += syncPage {
		var batch []dirUser
		if err := get(page("/users?briefRepresentation=false", first), &batch); err != nil {
			return nil, nil, nil, err
		}
		users = append(users, batch...)
		if len(batch) < syncPage || len(users) > syncMaxEntries {
			break
		}
	}

	var groups []dirGroup
	var walk func(list []dirGroup) error
	walk = func(list []dirGroup) error {
		for _, g := range list {
			groups = append(groups, g)
			if len(groups) > syncMaxEntries {
				return errors.New("directory: too many groups")
			}
			children := g.SubGroups
			if len(children) == 0 && g.SubGroupCount > 0 {
				// Newer Keycloak versions list subgroups separately.
				for first := 0; ; first += syncPage {
					var batch []dirGroup
					if err := get(page("/groups/"+url.PathEscape(g.ID)+"/children", first), &batch); err != nil {
						return err
					}
					children = append(children, batch...)
					if len(batch) < syncPage {
						break
					}
				}
			}
			if err := walk(children); err != nil {
				return err
			}
		}
		return nil
	}
	for first := 0; ; first += syncPage {
		var batch []dirGroup
		if err := get(page("/groups?briefRepresentation=false", first), &batch); err != nil {
			return nil, nil, nil, err
		}
		if err := walk(batch); err != nil {
			return nil, nil, nil, err
		}
		if len(batch) < syncPage {
			break
		}
	}

	members := map[string][]string{}
	for _, g := range groups {
		for first := 0; ; first += syncPage {
			var batch []dirUser
			if err := get(page("/groups/"+url.PathEscape(g.ID)+"/members?briefRepresentation=true", first), &batch); err != nil {
				return nil, nil, nil, err
			}
			for _, m := range batch {
				members[g.ID] = append(members[g.ID], m.ID)
			}
			if len(batch) < syncPage {
				break
			}
		}
	}
	return users, groups, members, nil
}

// serviceToken gets an access token of the sync's service account (client credentials).
func (o *OIDC) serviceToken(ctx context.Context, clientID, secret string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("directory: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("directory: the service account %s could not sign in (%d)", clientID, resp.StatusCode)
	}
	return tok.AccessToken, nil
}
