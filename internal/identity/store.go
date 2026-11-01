// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
)

const userColumns = `u.id, u.username, u.display_name, coalesce(u.email, ''), u.email_verified, u.kind, u.status,
	coalesce(u.locale, ''), coalesce(u.time_zone, ''), coalesce(u.theme, ''), u.oidc_subject IS NOT NULL,
	u.password_hash IS NOT NULL, u.created_at, u.last_login_at,
	EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL), coalesce(u.sso_link, '')`

func scanUser(row pgx.Row, extra ...any) (*User, error) {
	u := &User{}
	dest := append([]any{&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.EmailVerified, &u.Kind, &u.Status,
		&u.Locale, &u.TimeZone, &u.Theme, &u.External, &u.HasPassword, &u.CreatedAt, &u.LastLoginAt, &u.TwoFactor,
		&u.SSOLink}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return u, nil
}

func notFoundUser(err error, what string) error {
	if db.IsNoRows(err) {
		return apperr.NotFound("user.not-found", "User %s does not exist.", what)
	}
	return err
}

// GetUser returns a user by id.
func GetUser(ctx context.Context, q db.Querier, id int64) (*User, error) {
	u, err := scanUser(q.QueryRow(ctx, "SELECT "+userColumns+" FROM users u WHERE u.id = $1", id))
	return u, notFoundUser(err, strconv.FormatInt(id, 10))
}

// GetUserByUsername returns a user by user name (case-insensitive).
func GetUserByUsername(ctx context.Context, q db.Querier, username string) (*User, error) {
	u, err := scanUser(q.QueryRow(ctx, "SELECT "+userColumns+
		" FROM users u WHERE lower(u.username) = lower($1) AND u.status <> 'anonymised'", username))
	return u, notFoundUser(err, username)
}

// GetUserRefs returns short forms of users by id.
func GetUserRefs(ctx context.Context, q db.Querier, ids []int64) (map[int64]UserRef, error) {
	out := map[int64]UserRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, "SELECT id, username, display_name, status = 'active' FROM users WHERE id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r UserRef
		if err := rows.Scan(&r.ID, &r.Username, &r.DisplayName, &r.Active); err != nil {
			return nil, err
		}
		out[r.ID] = r
	}
	return out, rows.Err()
}

// UsersByEmail returns the users with verified e-mail addresses among emails (lower case keys), e.g. the authors of
// commits. Placeholder users of migrations match by their (unverified) addresses too.
func UsersByEmail(ctx context.Context, q db.Querier, emails []string) (map[string]UserRef, error) {
	out := map[string]UserRef{}
	if len(emails) == 0 {
		return out, nil
	}
	lower := make([]string, 0, len(emails))
	for _, e := range emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			lower = append(lower, e)
		}
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (lower(email)) lower(email), id, username, display_name, status = 'active' FROM users
		WHERE lower(email) = ANY($1) AND (email_verified OR kind = 'placeholder') AND status <> 'anonymised'
		ORDER BY lower(email), kind = 'placeholder', id`, lower)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var email string
		var r UserRef
		if err := rows.Scan(&email, &r.ID, &r.Username, &r.DisplayName, &r.Active); err != nil {
			return nil, err
		}
		out[email] = r
	}
	return out, rows.Err()
}

// UsersByUsername returns the active people and service accounts among user names (lower case keys).
func UsersByUsername(ctx context.Context, q db.Querier, names []string) (map[string]UserRef, error) {
	out := map[string]UserRef{}
	lower := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			lower = append(lower, n)
		}
	}
	if len(lower) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT lower(username), id, username, display_name, status = 'active' FROM users
		WHERE lower(username) = ANY($1) AND status = 'active' AND kind IN ('person', 'service')`, lower)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var r UserRef
		if err := rows.Scan(&key, &r.ID, &r.Username, &r.DisplayName, &r.Active); err != nil {
			return nil, err
		}
		out[key] = r
	}
	return out, rows.Err()
}

// GroupRef names a group.
type GroupRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GroupsByName returns the groups among names (lower case keys).
func GroupsByName(ctx context.Context, q db.Querier, names []string) (map[string]GroupRef, error) {
	out := map[string]GroupRef{}
	lower := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			lower = append(lower, n)
		}
	}
	if len(lower) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, "SELECT lower(name), id, name FROM groups WHERE lower(name) = ANY($1)", lower)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var g GroupRef
		if err := rows.Scan(&key, &g.ID, &g.Name); err != nil {
			return nil, err
		}
		out[key] = g
	}
	return out, rows.Err()
}

// GroupsOfUsers returns the groups of each user.
func GroupsOfUsers(ctx context.Context, q db.Querier, userIDs []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, "SELECT user_id, group_id FROM group_members WHERE user_id = ANY($1)", userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u, g int64
		if err := rows.Scan(&u, &g); err != nil {
			return nil, err
		}
		out[u] = append(out[u], g)
	}
	return out, rows.Err()
}

// ActiveMembers returns the active members of groups that have at most limit members each (larger groups are left
// out), e.g. to tell them about something without telling a whole department.
func ActiveMembers(ctx context.Context, q db.Querier, groupIDs []int64, limit int) ([]int64, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT m.user_id FROM group_members m JOIN users u ON u.id = m.user_id
		WHERE m.group_id = ANY($1) AND u.status = 'active'
		AND (SELECT count(*) FROM group_members c WHERE c.group_id = m.group_id) <= $2 ORDER BY m.user_id`, groupIDs, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// UserFilter selects users.
type UserFilter struct {
	Query string
	// ByName matches the query against user and display names only, not e-mail addresses.
	ByName        bool
	Status        string
	IncludeSystem bool
	// Where adds a condition of another module on the users (alias u), e.g. who may be assigned issues of a project,
	// and binds its values in args. It applies before counting and paging, so that pages are full.
	Where  func(args *db.Args) string
	Limit  int
	Offset int
}

// ListUsers returns users matching the filter and the total count.
func ListUsers(ctx context.Context, q db.Querier, f UserFilter) ([]*User, int, error) {
	var args db.Args
	where := []string{"u.status <> 'anonymised'"}
	if !f.IncludeSystem {
		where = append(where, "u.kind <> 'system'")
	}
	if f.Status != "" {
		where = append(where, "u.status = "+args.Add(f.Status))
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		p := args.Add("%" + db.EscapeLike(strings.ToLower(s)) + "%")
		if f.ByName {
			where = append(where, "lower(u.username || ' ' || u.display_name) LIKE "+p)
		} else {
			where = append(where, "(lower(u.username || ' ' || u.display_name) LIKE "+p+" OR lower(u.email) LIKE "+p+")")
		}
	}
	if f.Where != nil {
		where = append(where, "("+f.Where(&args)+")")
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := q.QueryRow(ctx, "SELECT count(*) FROM users u WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := args.Add(max(1, min(f.Limit, 500)))
	offset := args.Add(max(0, f.Offset))
	rows, err := q.Query(ctx, "SELECT "+userColumns+" FROM users u WHERE "+cond+
		" ORDER BY lower(u.display_name), u.id LIMIT "+limit+" OFFSET "+offset, args...)
	if err != nil {
		return nil, 0, err
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*User, error) { return scanUser(row) })
	return users, total, err
}

// NewUser describes a user to create.
type NewUser struct {
	Username      string
	DisplayName   string
	Email         string
	EmailVerified bool
	Kind          string
	PasswordHash  string
	OIDCIssuer    string
	OIDCSubject   string
	Locale        string
	TimeZone      string
}

// CreateUser inserts a user.
func CreateUser(ctx context.Context, q db.Querier, n NewUser) (*User, error) {
	if n.Kind == "" {
		n.Kind = KindPerson
	}
	u, err := scanUser(q.QueryRow(ctx, `INSERT INTO users AS u (username, display_name, email, email_verified, kind,
		password_hash, oidc_issuer, oidc_subject, locale, time_zone)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''))
		RETURNING `+userColumns, n.Username, n.DisplayName, n.Email, n.EmailVerified, n.Kind, n.PasswordHash,
		n.OIDCIssuer, n.OIDCSubject, n.Locale, n.TimeZone))
	if db.IsUniqueViolation(err, "users_username_key") {
		return nil, apperr.Conflict("user.username-taken", "The user name %s is already taken.", n.Username)
	}
	return u, err
}

// UserUpdate holds optional changes to a user.
type UserUpdate struct {
	DisplayName *string
	Email       *string
	Locale      *string
	TimeZone    *string
	Theme       *string
	Status      *string
}

// UpdateUser applies the non-nil fields.
func UpdateUser(ctx context.Context, q db.Querier, id int64, c UserUpdate) (*User, error) {
	var args db.Args
	var sets []string
	set := func(col string, v *string) {
		if v != nil {
			sets = append(sets, col+" = NULLIF("+args.Add(*v)+", '')")
		}
	}
	set("email", c.Email)
	set("locale", c.Locale)
	set("time_zone", c.TimeZone)
	set("theme", c.Theme)
	if c.DisplayName != nil {
		sets = append(sets, "display_name = "+args.Add(*c.DisplayName))
	}
	if c.Status != nil {
		// An administrator's change of the status is final for the directory sync, too.
		sets = append(sets, "status = "+args.Add(*c.Status), "disabled_by_directory = false")
	}
	if len(sets) == 0 {
		return GetUser(ctx, q, id)
	}
	sets = append(sets, "updated_at = now()")
	u, err := scanUser(q.QueryRow(ctx, "UPDATE users AS u SET "+strings.Join(sets, ", ")+" WHERE u.id = "+
		args.Add(id)+" AND u.kind <> 'system' RETURNING "+userColumns, args...))
	return u, notFoundUser(err, strconv.FormatInt(id, 10))
}

// SetPasswordHash sets (or with "" removes) a local password and unlocks the account.
func SetPasswordHash(ctx context.Context, q db.Querier, id int64, hash string) error {
	_, err := q.Exec(ctx, `UPDATE users SET password_hash = NULLIF($2, ''), failed_logins = 0, locked_until = NULL,
		updated_at = now() WHERE id = $1`, id, hash)
	return err
}

type credentials struct {
	id          int64
	hash        *string
	status      string
	lockedUntil *time.Time
}

func loadCredentials(ctx context.Context, q db.Querier, username string) (*credentials, error) {
	c := &credentials{}
	err := q.QueryRow(ctx, `SELECT id, password_hash, status, locked_until FROM users
		WHERE lower(username) = lower($1) AND status <> 'anonymised' AND kind <> 'system'`, username).
		Scan(&c.id, &c.hash, &c.status, &c.lockedUntil)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return c, err
}

// recordLoginFailure counts a wrong password and locks the account after lockAfter failures in a row; it reports
// whether this failure locked it. Attempts while it is locked neither count nor extend the lock: otherwise anyone
// could keep someone else's account locked for good with one wrong attempt now and then.
func recordLoginFailure(ctx context.Context, q db.Querier, id int64, lockAfter int, lockFor time.Duration) (bool, error) {
	var locked bool
	err := q.QueryRow(ctx, `WITH c AS (SELECT locked_until IS NOT NULL AND locked_until > now() AS locked,
			CASE WHEN locked_until IS NOT NULL AND locked_until <= now() THEN 1 ELSE failed_logins + 1 END AS n FROM users WHERE id = $1)
		UPDATE users SET failed_logins = CASE WHEN c.locked THEN failed_logins ELSE c.n END,
			locked_until = CASE WHEN c.locked THEN locked_until WHEN c.n >= $2 THEN now() + $3::interval ELSE NULL END
		FROM c WHERE id = $1 RETURNING NOT c.locked AND c.n >= $2`, id, lockAfter, strconv.Itoa(int(lockFor.Seconds()))+" seconds").Scan(&locked)
	return locked, err
}

func recordLoginSuccess(ctx context.Context, q db.Querier, id int64) error {
	_, err := q.Exec(ctx, "UPDATE users SET failed_logins = 0, locked_until = NULL, last_login_at = now() WHERE id = $1", id)
	return err
}

// --- groups ---

const groupColumns = `g.id, g.name, g.description, g.source, g.created_at,
	(SELECT count(*) FROM group_members m WHERE m.group_id = g.id)`

func scanGroup(row pgx.Row) (*Group, error) {
	g := &Group{}
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.Source, &g.CreatedAt, &g.Members)
	return g, err
}

// ListGroups returns groups matching query and the total count.
func ListGroups(ctx context.Context, q db.Querier, query string, limit, offset int) ([]*Group, int, error) {
	pattern := "%" + db.EscapeLike(strings.ToLower(strings.TrimSpace(query))) + "%"
	var total int
	if err := q.QueryRow(ctx, "SELECT count(*) FROM groups g WHERE lower(g.name) LIKE $1", pattern).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, "SELECT "+groupColumns+` FROM groups g WHERE lower(g.name) LIKE $1
		ORDER BY lower(g.name) LIMIT $2 OFFSET $3`, pattern, max(1, min(limit, 500)), max(0, offset))
	if err != nil {
		return nil, 0, err
	}
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Group, error) { return scanGroup(row) })
	return groups, total, err
}

// GetGroup returns a group by id.
func GetGroup(ctx context.Context, q db.Querier, id int64) (*Group, error) {
	g, err := scanGroup(q.QueryRow(ctx, "SELECT "+groupColumns+" FROM groups g WHERE g.id = $1", id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("group.not-found", "Group %d does not exist.", id)
	}
	return g, err
}

// GetGroupByName returns a group by name (case-insensitive).
func GetGroupByName(ctx context.Context, q db.Querier, name string) (*Group, error) {
	g, err := scanGroup(q.QueryRow(ctx, "SELECT "+groupColumns+" FROM groups g WHERE lower(g.name) = lower($1)", name))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("group.not-found", "Group %s does not exist.", name)
	}
	return g, err
}

// CreateGroup inserts a group.
func CreateGroup(ctx context.Context, q db.Querier, name, description, source string) (*Group, error) {
	g, err := scanGroup(q.QueryRow(ctx, `INSERT INTO groups AS g (name, description, source) VALUES ($1, $2, $3)
		RETURNING `+groupColumns, name, description, source))
	if db.IsUniqueViolation(err, "groups_name_key") {
		return nil, apperr.Conflict("group.name-taken", "A group named %s already exists.", name)
	}
	return g, err
}

// UpdateGroup changes name and description of a local group.
func UpdateGroup(ctx context.Context, q db.Querier, id int64, name, description string) (*Group, error) {
	g, err := scanGroup(q.QueryRow(ctx, `UPDATE groups AS g SET name = $2, description = $3 WHERE g.id = $1
		RETURNING `+groupColumns, id, name, description))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("group.not-found", "Group %d does not exist.", id)
	}
	if db.IsUniqueViolation(err, "groups_name_key") {
		return nil, apperr.Conflict("group.name-taken", "A group named %s already exists.", name)
	}
	return g, err
}

// DeleteGroup removes a group and its memberships.
func DeleteGroup(ctx context.Context, q db.Querier, id int64) error {
	tag, err := q.Exec(ctx, "DELETE FROM groups WHERE id = $1", id)
	if err == nil && tag.RowsAffected() == 0 {
		return apperr.NotFound("group.not-found", "Group %d does not exist.", id)
	}
	return err
}

// GroupMembers returns the members of a group.
func GroupMembers(ctx context.Context, q db.Querier, groupID int64, limit, offset int) ([]*User, int, error) {
	var total int
	if err := q.QueryRow(ctx, "SELECT count(*) FROM group_members WHERE group_id = $1", groupID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, "SELECT "+userColumns+` FROM users u JOIN group_members m ON m.user_id = u.id
		WHERE m.group_id = $1 ORDER BY lower(u.display_name), u.id LIMIT $2 OFFSET $3`, groupID, max(1, min(limit, 500)), max(0, offset))
	if err != nil {
		return nil, 0, err
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*User, error) { return scanUser(row) })
	return users, total, err
}

// AddMember adds a user to a group.
func AddMember(ctx context.Context, q db.Querier, groupID, userID int64) error {
	_, err := q.Exec(ctx, "INSERT INTO group_members (group_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		groupID, userID)
	if db.IsForeignKeyViolation(err) {
		return apperr.NotFound("group.member-not-found", "The group or the user does not exist.")
	}
	return err
}

// RemoveMember removes a user from a group.
func RemoveMember(ctx context.Context, q db.Querier, groupID, userID int64) error {
	_, err := q.Exec(ctx, "DELETE FROM group_members WHERE group_id = $1 AND user_id = $2", groupID, userID)
	return err
}

// UserGroups returns the groups of a user.
func UserGroups(ctx context.Context, q db.Querier, userID int64) ([]*Group, error) {
	rows, err := q.Query(ctx, "SELECT "+groupColumns+` FROM groups g JOIN group_members m ON m.group_id = g.id
		WHERE m.user_id = $1 ORDER BY lower(g.name)`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Group, error) { return scanGroup(row) })
}

// SyncProviderGroups makes the user's memberships in groups from the identity provider match names. Groups are
// created on first sight; local groups are not touched.
func SyncProviderGroups(ctx context.Context, q db.Querier, userID int64, names []string) error {
	for _, n := range names {
		if _, err := q.Exec(ctx, `INSERT INTO groups (name, source) VALUES ($1, 'oidc') ON CONFLICT DO NOTHING`, n); err != nil {
			return err
		}
	}
	lower := make([]string, len(names))
	for i, n := range names {
		lower[i] = strings.ToLower(n)
	}
	if _, err := q.Exec(ctx, `DELETE FROM group_members m USING groups g WHERE m.group_id = g.id AND m.user_id = $1
		AND g.source = 'oidc' AND NOT (lower(g.name) = ANY($2))`, userID, lower); err != nil {
		return err
	}
	// Only groups of the provider: a provider group named like a local or imported group (say, one with
	// administrative grants) must not make its members members of that group.
	_, err := q.Exec(ctx, `INSERT INTO group_members (group_id, user_id)
		SELECT id, $1 FROM groups WHERE source = 'oidc' AND lower(name) = ANY($2) ON CONFLICT DO NOTHING`, userID, lower)
	return err
}

// --- sessions ---

type sessionRow struct {
	user          *User
	groups        []int64
	method        string // password or oidc
	csrf          string
	idleExpiresAt time.Time
	expiresAt     time.Time
	lastSeenAt    time.Time
}

func createSession(ctx context.Context, q db.Querier, userID int64, method, oidcSID, ip, userAgent string,
	idle, lifetime time.Duration) (token string, err error) {
	token, hash := crypto.NewToken(crypto.PrefixSession)
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	_, err = q.Exec(ctx, `INSERT INTO sessions (id_hash, user_id, csrf_token, auth_method, oidc_sid, ip, user_agent,
		idle_expires_at, expires_at) VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, '')::inet, $7, now() + $8::interval,
		now() + $9::interval)`, hash, userID, crypto.RandomString(24), method, oidcSID, ip, userAgent,
		seconds(idle), seconds(lifetime))
	return token, err
}

func seconds(d time.Duration) string { return strconv.FormatInt(int64(d.Seconds()), 10) + " seconds" }

func lookupSession(ctx context.Context, q db.Querier, hash []byte) (*sessionRow, error) {
	s := &sessionRow{}
	u, err := scanUser(q.QueryRow(ctx, "SELECT "+userColumns+`,
		ARRAY(SELECT group_id FROM group_members WHERE user_id = u.id), s.auth_method, s.csrf_token, s.idle_expires_at, s.expires_at,
		s.last_seen_at FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = $1`, hash),
		&s.groups, &s.method, &s.csrf, &s.idleExpiresAt, &s.expiresAt, &s.lastSeenAt)
	if err != nil {
		return nil, err
	}
	s.user = u
	return s, nil
}

func touchSession(ctx context.Context, q db.Querier, hash []byte, idle time.Duration) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET last_seen_at = now(),
		idle_expires_at = least(expires_at, now() + $2::interval) WHERE id_hash = $1`, hash, seconds(idle))
	return err
}

func deleteSession(ctx context.Context, q db.Querier, hash []byte) error {
	_, err := q.Exec(ctx, "DELETE FROM sessions WHERE id_hash = $1", hash)
	return err
}

// DeleteUserSessions ends all sessions of a user.
func DeleteUserSessions(ctx context.Context, q db.Querier, userID int64) error {
	_, err := q.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", userID)
	return err
}

// CleanupSessions removes expired sessions and stale OIDC login attempts.
func CleanupSessions(ctx context.Context, q db.Querier) (int64, error) {
	tag, err := q.Exec(ctx, "DELETE FROM sessions WHERE expires_at < now() OR idle_expires_at < now()")
	if err != nil {
		return 0, err
	}
	_, err = q.Exec(ctx, "DELETE FROM oidc_logins WHERE created_at < now() - interval '15 minutes'")
	return tag.RowsAffected(), err
}

func listSessions(ctx context.Context, q db.Querier, userID int64, current []byte) ([]Session, error) {
	rows, err := q.Query(ctx, `SELECT encode(id_hash, 'hex'), auth_method, coalesce(host(ip), ''), user_agent, created_at,
		last_seen_at, least(expires_at, idle_expires_at), id_hash = $2 FROM sessions
		WHERE user_id = $1 AND expires_at > now() AND idle_expires_at > now() ORDER BY last_seen_at DESC`, userID, current)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Session, error) {
		var s Session
		err := row.Scan(&s.ID, &s.AuthMethod, &s.IP, &s.UserAgent, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.Current)
		if len(s.ID) > 16 {
			s.ID = s.ID[:16]
		}
		return s, err
	})
}

func deleteSessionByID(ctx context.Context, q db.Querier, userID int64, id string) (bool, error) {
	if len(id) != 16 {
		return false, nil
	}
	tag, err := q.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1 AND left(encode(id_hash, 'hex'), 16) = $2", userID, id)
	return tag.RowsAffected() > 0, err
}

// --- access tokens ---

func createAccessToken(ctx context.Context, q db.Querier, userID int64, name string, scopes []string,
	expiresAt *time.Time) (string, *AccessToken, error) {
	token, hash := crypto.NewToken(crypto.PrefixAccessToken)
	t := &AccessToken{Name: name, Hint: crypto.TokenHint(token), Scopes: scopes, ExpiresAt: expiresAt}
	err := q.QueryRow(ctx, `INSERT INTO access_tokens (user_id, name, token_hash, hint, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`, userID, name, hash, t.Hint, scopes, expiresAt).
		Scan(&t.ID, &t.CreatedAt)
	return token, t, err
}

func listAccessTokens(ctx context.Context, q db.Querier, userID int64) ([]AccessToken, error) {
	rows, err := q.Query(ctx, `SELECT id, name, hint, scopes, expires_at, last_used_at, created_at FROM access_tokens
		WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccessToken, error) {
		var t AccessToken
		err := row.Scan(&t.ID, &t.Name, &t.Hint, &t.Scopes, &t.ExpiresAt, &t.LastUsedAt, &t.CreatedAt)
		return t, err
	})
}

func deleteAccessToken(ctx context.Context, q db.Querier, userID, id int64) (bool, error) {
	tag, err := q.Exec(ctx, "DELETE FROM access_tokens WHERE user_id = $1 AND id = $2", userID, id)
	return tag.RowsAffected() > 0, err
}

type tokenRow struct {
	id        int64
	scopes    []string
	expiresAt *time.Time
	lastUsed  *time.Time
	user      *User
	groups    []int64
}

func lookupAccessToken(ctx context.Context, q db.Querier, hash []byte) (*tokenRow, error) {
	t := &tokenRow{}
	u, err := scanUser(q.QueryRow(ctx, "SELECT "+userColumns+`, ARRAY(SELECT group_id FROM group_members WHERE user_id = u.id),
		t.id, t.scopes, t.expires_at, t.last_used_at FROM access_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1`, hash), &t.groups, &t.id, &t.scopes, &t.expiresAt, &t.lastUsed)
	if err != nil {
		return nil, err
	}
	t.user = u
	return t, nil
}

// ImportMatch is the Kogge user that a user of a migrated product matches.
type ImportMatch struct {
	ID          int64
	Username    string
	Email       string
	By          string // "username" or "email"
	Placeholder bool   // a placeholder user, e.g. of an earlier migration
}

// ImportUsername is the user name a placeholder for a user of a migrated product gets (before a suffix that
// makes it unique).
func ImportUsername(username string) string {
	if name := sanitizeUsername(username); name != "" {
		return name
	}
	return "imported-user"
}

// FindImportUser finds the Kogge user that a user of a migrated product matches, by user name and then by e-mail
// address, without creating one; ok is false when the migration creates a placeholder.
func FindImportUser(ctx context.Context, q db.Querier, username, email string) (m ImportMatch, ok bool, err error) {
	var kind string
	if name := sanitizeUsername(username); name != "" {
		err := q.QueryRow(ctx, "SELECT id, username, coalesce(email, ''), kind FROM users WHERE lower(username) = $1 AND status <> 'anonymised'",
			name).Scan(&m.ID, &m.Username, &m.Email, &kind)
		if err == nil {
			m.By, m.Placeholder = "username", kind == "placeholder"
			return m, true, nil
		}
		if !db.IsNoRows(err) {
			return m, false, err
		}
	}
	// By e-mail address only where the address is verified (or a placeholder of an earlier migration has it): an
	// unverified address does not make a source user anyone's (audit L6).
	if email = strings.TrimSpace(email); email != "" {
		err := q.QueryRow(ctx, `SELECT id, username, coalesce(email, ''), kind FROM users WHERE lower(email) = lower($1) AND status <> 'anonymised'
			AND (email_verified OR kind = 'placeholder') ORDER BY kind = 'placeholder', id LIMIT 1`, email).Scan(&m.ID, &m.Username, &m.Email, &kind)
		if err == nil {
			m.By, m.Placeholder = "email", kind == "placeholder"
			return m, true, nil
		}
		if !db.IsNoRows(err) {
			return m, false, err
		}
	}
	return ImportMatch{}, false, nil
}

// ImportUser finds the Kogge user for a user of a migrated product, by user name and then by e-mail address, or
// creates a disabled placeholder user that can be merged later. It reports whether the user
// matched an existing one.
func ImportUser(ctx context.Context, q db.Querier, username, displayName, email string) (int64, bool, error) {
	m, found, err := FindImportUser(ctx, q, username, email)
	if err != nil {
		return 0, false, err
	}
	if found {
		return m.ID, !m.Placeholder, nil
	}
	email = strings.TrimSpace(email)
	name := ImportUsername(username)
	if strings.TrimSpace(displayName) == "" {
		displayName = username
	}
	var id int64
	candidate := name
	for i := 2; ; i++ {
		err := q.QueryRow(ctx, `INSERT INTO users (username, display_name, email, kind, status) VALUES ($1, $2, NULLIF($3, ''), 'placeholder', 'disabled')
			ON CONFLICT DO NOTHING RETURNING id`, candidate, displayName, email).Scan(&id)
		if err == nil {
			return id, false, nil
		}
		if !db.IsNoRows(err) {
			return 0, false, err
		}
		candidate = name + "-" + strconv.Itoa(i)
		if i > 100 {
			return 0, false, errors.New("no free user name for " + name)
		}
	}
}

// ImportGroup returns the group for a group of a migrated product and its name in Kogge, creating it if needed.
// Groups that migrations created are reused; Kogge's own groups are not merged with source groups of the same
// name — they may hold permissions the source group's members must not get (audit L6) — so the migrated group
// then gets a name of its own.
func ImportGroup(ctx context.Context, q db.Querier, name string) (int64, string, error) {
	id, kname, err := FindImportGroup(ctx, q, name)
	if err != nil || id != 0 {
		return id, kname, err
	}
	err = q.QueryRow(ctx, `INSERT INTO groups (name, description, imported) VALUES ($1, 'Created by a migration.', true)
		RETURNING id`, kname).Scan(&id)
	return id, kname, err
}

// FindImportGroup returns the group that a group of a migrated product goes into (see ImportGroup) without
// creating it: the group of an earlier migration, or id 0 and the name the new group gets.
func FindImportGroup(ctx context.Context, q db.Querier, name string) (int64, string, error) {
	candidate := name
	for i := 2; i <= 100; i++ {
		var id int64
		var stored string
		var imported bool
		err := q.QueryRow(ctx, "SELECT id, name, imported FROM groups WHERE lower(name) = lower($1)", candidate).Scan(&id, &stored, &imported)
		switch {
		case err == nil && imported:
			return id, stored, nil
		case db.IsNoRows(err):
			return 0, candidate, nil
		case err != nil:
			return 0, "", err
		}
		candidate = name + " (migrated)"
		if i > 2 {
			candidate = fmt.Sprintf("%s (migrated %d)", name, i-1)
		}
	}
	return 0, "", errors.New("no free group name for " + name)
}

// GroupNames returns the names of groups by id.
func GroupNames(ctx context.Context, q db.Querier, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, "SELECT id, name FROM groups WHERE id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// UsersOf returns the active users among userIDs and the members of groupIDs, without duplicates.
func UsersOf(ctx context.Context, q db.Querier, userIDs, groupIDs []int64) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT id FROM users WHERE status = 'active' AND (id = ANY($1)
		OR id IN (SELECT user_id FROM group_members WHERE group_id = ANY($2))) ORDER BY id`, nonNilIDs(userIDs), nonNilIDs(groupIDs))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}
