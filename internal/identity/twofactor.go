// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"rsc.io/qr"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
)

// Two-factor authentication of local accounts: besides the password, a code of an authenticator
// app (TOTP, RFC 6238) or one of ten recovery codes, each usable once. Accounts that sign in through the identity
// provider get their second factor there. With two factors, Git over HTTPS takes access tokens only: a password
// alone would open the account.

const recoveryCodeCount = 10

// TwoFactorStatus describes the second factor of the signed-in user.
type TwoFactorStatus struct {
	Enabled bool `json:"enabled"`
	// Available: the account signs in with a local password; others get their second factor from the identity
	// provider.
	Available     bool `json:"available" doc:"The account signs in with a local password"`
	RecoveryCodes int  `json:"recoveryCodes" doc:"Unused recovery codes"`
}

// TwoFactorSetup is what an authenticator app needs: the secret as a QR code and as text.
type TwoFactorSetup struct {
	Secret string `json:"secret" doc:"The secret in groups of four characters, to type into an app"`
	URI    string `json:"uri" doc:"otpauth address that the QR code holds"`
	QRCode string `json:"qrCode" doc:"The QR code as an SVG image"`
}

func totpAAD(userID int64) string { return "totp:" + strconv.FormatInt(userID, 10) }

// TwoFactor returns the state of the user's second factor.
func (s *Service) TwoFactor(ctx context.Context, p *Principal) (*TwoFactorStatus, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	st := &TwoFactorStatus{}
	err := s.pool.QueryRow(ctx, `SELECT u.password_hash IS NOT NULL AND u.oidc_subject IS NULL,
		EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL),
		(SELECT count(*) FROM user_recovery_codes c WHERE c.user_id = u.id AND c.used_at IS NULL) FROM users u WHERE u.id = $1`,
		p.UserID()).Scan(&st.Available, &st.Enabled, &st.RecoveryCodes)
	return st, err
}

// BeginTwoFactor starts setting up an authenticator app: a new secret, which counts once a code of it is confirmed.
func (s *Service) BeginTwoFactor(ctx context.Context, p *Principal) (*TwoFactorSetup, error) {
	st, err := s.TwoFactor(ctx, p)
	if err != nil {
		return nil, err
	}
	if !st.Available {
		return nil, apperr.BadRequest("two-factor.not-local", "Your account signs in through the identity provider, which asks for the second factor.")
	}
	if st.Enabled {
		return nil, apperr.Conflict("two-factor.enabled", "Two-factor authentication is already on.")
	}
	if s.keyring == nil {
		return nil, apperr.New(http.StatusServiceUnavailable, "two-factor.unavailable", "Two-factor authentication is not available.")
	}
	secret := crypto.NewTOTPSecret()
	sealed, err := s.keyring.Seal(secret, totpAAD(p.UserID()))
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO user_totp (user_id, secret) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET secret = EXCLUDED.secret, confirmed_at = NULL, last_step = 0, created_at = now()
		WHERE user_totp.confirmed_at IS NULL`, p.UserID(), sealed); err != nil {
		return nil, err
	}
	issuer := "Kogge"
	if name := strings.TrimSpace(s.instanceName(ctx)); name != "" {
		issuer = name
	}
	uri := crypto.TOTPURI(issuer, p.User.Username, secret)
	svg, err := qrSVG(uri)
	if err != nil {
		return nil, err
	}
	return &TwoFactorSetup{Secret: crypto.TOTPSecretText(secret), URI: uri, QRCode: svg}, nil
}

// EnableTwoFactor confirms the authenticator app with one of its codes (and the password) and returns the recovery
// codes, which are shown this once.
func (s *Service) EnableTwoFactor(ctx context.Context, p *Principal, password, code string) ([]string, error) {
	if err := s.verifyPassword(ctx, p, password); err != nil {
		return nil, err
	}
	var codes []string
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var sealed string
		var confirmed *time.Time
		err := tx.QueryRow(ctx, "SELECT secret, confirmed_at FROM user_totp WHERE user_id = $1 FOR UPDATE", p.UserID()).Scan(&sealed, &confirmed)
		if db.IsNoRows(err) || err == nil && confirmed != nil {
			return apperr.Conflict("two-factor.no-setup", "Start setting up the authenticator app first.")
		}
		if err != nil {
			return err
		}
		secret, err := s.keyring.Open(sealed, totpAAD(p.UserID()))
		if err != nil {
			return err
		}
		step, ok := crypto.VerifyTOTP(secret, code, time.Now(), 0)
		if !ok {
			return apperr.Invalid("two-factor.code", "The code is not correct. Check the time of your device.")
		}
		if _, err := tx.Exec(ctx, "UPDATE user_totp SET confirmed_at = now(), last_step = $2 WHERE user_id = $1", p.UserID(), step); err != nil {
			return err
		}
		if codes, err = s.replaceRecoveryCodes(ctx, tx, p.UserID()); err != nil {
			return err
		}
		// Other sessions were opened with the password alone: they end.
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1 AND id_hash IS DISTINCT FROM $2", p.UserID(), p.SessionID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "auth.two-factor.enable"})
	})
	return codes, err
}

// DisableTwoFactor turns the second factor off; it takes the password and a code (or a recovery code).
func (s *Service) DisableTwoFactor(ctx context.Context, p *Principal, password, code string) error {
	if err := s.verifyPassword(ctx, p, password); err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		ok, err := s.checkSecondFactor(ctx, tx, p.UserID(), code)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.Invalid("two-factor.code", "The code is not correct.")
		}
		if err := deleteTwoFactor(ctx, tx, p.UserID()); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "auth.two-factor.disable"})
	})
}

// NewRecoveryCodes replaces the recovery codes (with the password); the old ones stop working.
func (s *Service) NewRecoveryCodes(ctx context.Context, p *Principal, password string) ([]string, error) {
	if err := s.verifyPassword(ctx, p, password); err != nil {
		return nil, err
	}
	var codes []string
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM user_totp WHERE user_id = $1 AND confirmed_at IS NOT NULL)",
			p.UserID()).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return apperr.Conflict("two-factor.disabled", "Two-factor authentication is off.")
		}
		var err error
		if codes, err = s.replaceRecoveryCodes(ctx, tx, p.UserID()); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "auth.two-factor.recovery-codes"})
	})
	return codes, err
}

// ResetTwoFactor turns a user's second factor off (administrators: the user lost the device and the recovery codes).
// The user's sessions end.
func (s *Service) ResetTwoFactor(ctx context.Context, actor *Principal, userID int64) error {
	u, err := GetUser(ctx, s.pool, userID)
	if err != nil {
		return err
	}
	if !u.TwoFactor {
		return apperr.Conflict("two-factor.disabled", "%s has no second factor.", u.DisplayName)
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := deleteTwoFactor(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "auth.two-factor.reset", TargetType: "user",
			TargetID: strconv.FormatInt(userID, 10), Details: map[string]any{"username": u.Username}})
	})
}

func deleteTwoFactor(ctx context.Context, tx pgx.Tx, userID int64) error {
	if _, err := tx.Exec(ctx, "DELETE FROM user_totp WHERE user_id = $1", userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "DELETE FROM user_recovery_codes WHERE user_id = $1", userID)
	return err
}

// hasTwoFactor reports whether a user signs in with a second factor.
func hasTwoFactor(ctx context.Context, q db.Querier, userID int64) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM user_totp WHERE user_id = $1 AND confirmed_at IS NOT NULL)", userID).Scan(&on)
	return on, err
}

// checkSecondFactor checks a code of the authenticator app — each time step counts once — or a recovery code,
// which is used up.
func (s *Service) checkSecondFactor(ctx context.Context, tx pgx.Tx, userID int64, code string) (bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return false, nil
	}
	if strings.Contains(code, "-") || len(code) > 8 {
		tag, err := tx.Exec(ctx, "UPDATE user_recovery_codes SET used_at = now() WHERE user_id = $1 AND code_hash = ANY($2) AND used_at IS NULL",
			userID, s.recoveryCodeHashes(code))
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() == 1 {
			// Within the transaction that uses the code up: without its record, the code stays unused.
			if err := audit.Record(ctx, tx, audit.Entry{ActorID: userID, Action: "auth.two-factor.recovery-code-used"}); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	}
	var sealed string
	var last int64
	err := tx.QueryRow(ctx, "SELECT secret, last_step FROM user_totp WHERE user_id = $1 AND confirmed_at IS NOT NULL FOR UPDATE", userID).
		Scan(&sealed, &last)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if s.keyring == nil {
		return false, apperr.New(http.StatusServiceUnavailable, "two-factor.unavailable", "Two-factor authentication is not available.")
	}
	secret, err := s.keyring.Open(sealed, totpAAD(userID))
	if err != nil {
		return false, err
	}
	step, ok := crypto.VerifyTOTP(secret, code, time.Now(), last)
	if !ok {
		return false, nil
	}
	_, err = tx.Exec(ctx, "UPDATE user_totp SET last_step = $2 WHERE user_id = $1", userID, step)
	return err == nil, err
}

// recoveryCodeMAC is the purpose of the keyed hashes of recovery codes.
const recoveryCodeMAC = "recovery-code"

// replaceRecoveryCodes creates new recovery codes (xxxx-xxxx, 40 bits each) and returns them. They are stored as
// keyed hashes under the master key: 40 bits can be tried out against a plain hash within minutes, so a copy of the
// database alone must not tell them.
func (s *Service) replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, userID int64) ([]string, error) {
	if _, err := tx.Exec(ctx, "DELETE FROM user_recovery_codes WHERE user_id = $1", userID); err != nil {
		return nil, err
	}
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		raw := strings.ToLower(crypto.RandomBase32(8)) // 40 bits
		codes[i] = raw[:4] + "-" + raw[4:]
		if _, err := tx.Exec(ctx, "INSERT INTO user_recovery_codes (user_id, code_hash) VALUES ($1, $2)", userID,
			s.keyring.MAC(recoveryCodeMAC, []byte(normalizeRecoveryCode(codes[i])))); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// recoveryCodeHashes returns the forms in which a recovery code can be stored: keyed hashes under each key of the
// master key file, and the plain hash of codes created before keyed hashes, which work until they are used or
// renewed.
func (s *Service) recoveryCodeHashes(code string) [][]byte {
	norm := normalizeRecoveryCode(code)
	hashes := [][]byte{crypto.HashToken(norm)}
	if s.keyring != nil {
		hashes = append(s.keyring.MACs(recoveryCodeMAC, []byte(norm)), hashes...)
	}
	return hashes
}

func normalizeRecoveryCode(code string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(code))
}

// verifyPassword checks the password of the signed-in user before changes of the second factor; attempts count
// like sign-ins.
func (s *Service) verifyPassword(ctx context.Context, p *Principal, password string) error {
	if !p.Authenticated() {
		return apperr.Unauthorized("auth.required", "Please sign in.")
	}
	if s.keyring == nil {
		return apperr.New(http.StatusServiceUnavailable, "two-factor.unavailable", "Two-factor authentication is not available.")
	}
	if ok, err := s.throttle.admit(ctx, client(ctx), p.User.Username, true); err != nil {
		return err
	} else if !ok {
		return apperr.TooManyRequests("auth.rate-limited", "Too many attempts. Try again later.")
	}
	creds, err := loadCredentials(ctx, s.pool, p.User.Username)
	if err != nil {
		return err
	}
	if creds == nil || creds.hash == nil {
		return apperr.BadRequest("two-factor.not-local", "Your account signs in through the identity provider, which asks for the second factor.")
	}
	if creds.lockedUntil != nil && creds.lockedUntil.After(time.Now()) {
		return apperr.TooManyRequests("auth.rate-limited", "Too many attempts. Try again later.")
	}
	ok, _, err := s.hasher.Verify(ctx, password, *creds.hash)
	if err != nil {
		return err
	}
	if !ok {
		s.refused(ctx, creds.id, p.User.Username, "profile", true, true)
		return apperr.BadRequest("password.wrong", "The password is not correct.")
	}
	return nil
}

// instanceName is the name authenticator apps show for the account.
func (s *Service) instanceName(ctx context.Context) string {
	var name string
	_ = s.pool.QueryRow(ctx, "SELECT value->>'name' FROM settings WHERE key = 'instance'").Scan(&name)
	return name
}

// qrSVG draws a QR code as an SVG image, dark modules on white with the quiet zone of four modules.
func qrSVG(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	size := code.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, size, size)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, size, size)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; {
			if !code.Black(x, y) {
				x++
				continue
			}
			run := 1
			for x+run < code.Size && code.Black(x+run, y) {
				run++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", x+quiet, y+quiet, run, run)
			x += run
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}
