// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package identity_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestTwoFactor: local accounts add an authenticator app; sign-ins then need a code of it — each counts once — or
// a recovery code, which is used up; Git takes access tokens only; the user turns it off with password and code,
// an administrator for a lost device.
func TestTwoFactor(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	anna := env.CreateUser("anna")

	var status struct {
		Enabled       bool `json:"enabled"`
		Available     bool `json:"available"`
		RecoveryCodes int  `json:"recoveryCodes"`
	}
	anna.Get("/api/v1/me/two-factor", &status)
	if !status.Available || status.Enabled {
		t.Fatalf("status = %+v", status)
	}
	var setup struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
		QRCode string `json:"qrCode"`
	}
	anna.MustDo(http.MethodPost, "/api/v1/me/two-factor", nil, &setup)
	if !strings.HasPrefix(setup.URI, "otpauth://totp/") || !strings.Contains(setup.URI, ":anna?") || !strings.HasPrefix(setup.QRCode, "<svg") {
		t.Fatalf("setup = %+v", setup)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(setup.Secret, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	step := crypto.TOTPStep(time.Now())
	code := func(d int64) string { return crypto.TOTPCode(secret, step+d) }

	other := env.Login("anna", apitest.UserPassword) // a second session, opened with the password alone
	enable := func(password, c string) *apitest.Response {
		return anna.Do(http.MethodPost, "/api/v1/me/two-factor/enable", map[string]string{"password": password, "code": c})
	}
	if r := enable(apitest.UserPassword, wrong(code(0))); r.Code() != "two-factor.code" {
		t.Errorf("wrong code: %d %s", r.Status, r.Body)
	}
	if r := enable("Not-The-Password-1", code(0)); r.Code() != "password.wrong" {
		t.Errorf("wrong password: %d %s", r.Status, r.Body)
	}
	var recovery struct {
		Codes []string `json:"codes"`
	}
	r := enable(apitest.UserPassword, code(0))
	if r.Status != http.StatusOK {
		t.Fatalf("enable: %d %s", r.Status, r.Body)
	}
	r.Decode(&recovery)
	if len(recovery.Codes) != 10 || len(recovery.Codes[0]) != 9 || recovery.Codes[0][4] != '-' {
		t.Fatalf("recovery codes = %v", recovery.Codes)
	}
	var me struct {
		User struct {
			ID        int64 `json:"id"`
			TwoFactor bool  `json:"twoFactor"`
		} `json:"user"`
	}
	anna.Get("/api/v1/me", &me)
	if !me.User.TwoFactor {
		t.Error("me does not show two-factor authentication")
	}

	// Signing in.
	login := func(c string) *apitest.Response {
		body := map[string]string{"username": "anna", "password": apitest.UserPassword}
		if c != "" {
			body["code"] = c
		}
		return env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", body)
	}
	if r := login(""); r.Status != http.StatusUnauthorized || r.Code() != "auth.second-factor-required" || r.Header.Get("Set-Cookie") != "" {
		t.Errorf("without code: %d %s", r.Status, r.Body)
	}
	if r := login(wrong(code(1))); r.Code() != "auth.second-factor-invalid" {
		t.Errorf("wrong code: %d %s", r.Status, r.Body)
	}
	if r := login(code(0)); r.Code() != "auth.second-factor-invalid" {
		t.Errorf("the code that enabled it again: %d %s", r.Status, r.Body)
	}
	env.LoginWithCode("anna", apitest.UserPassword, code(1))
	if r := login(code(1)); r.Code() != "auth.second-factor-invalid" {
		t.Errorf("a code used twice: %d %s", r.Status, r.Body)
	}
	// Enabling ended the other sessions, opened with the password alone; the one that enabled it stays.
	if r := other.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("the other session: %d", r.Status)
	}
	if r := anna.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusOK {
		t.Errorf("the session that enabled it: %d", r.Status)
	}

	// Sign-ins count per account: ben goes on with recovery codes, Git and turning it off.
	ben := env.CreateUser("ben")
	var benSetup struct {
		Secret string `json:"secret"`
	}
	ben.MustDo(http.MethodPost, "/api/v1/me/two-factor", nil, &benSetup)
	benSecret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(benSetup.Secret, " ", ""))
	ben.MustDo(http.MethodPost, "/api/v1/me/two-factor/enable", map[string]string{"password": apitest.UserPassword,
		"code": crypto.TOTPCode(benSecret, step)}, &recovery)
	login = func(c string) *apitest.Response {
		body := map[string]string{"username": "ben", "password": apitest.UserPassword}
		if c != "" {
			body["code"] = c
		}
		return env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", body)
	}
	signedIn := env.LoginWithCode("ben", apitest.UserPassword, strings.ToUpper(recovery.Codes[0]))
	if r := login(recovery.Codes[0]); r.Code() != "auth.second-factor-invalid" {
		t.Errorf("a recovery code used twice: %d %s", r.Status, r.Body)
	}
	signedIn.Get("/api/v1/me/two-factor", &status)
	if !status.Enabled || status.RecoveryCodes != 9 {
		t.Errorf("status after a recovery code = %+v", status)
	}

	// Git: a password alone opens nothing.
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "TF", "name": "Two factors"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/TF/repos", map[string]any{"name": "app", "readme": true}, nil)
	req, _ := http.NewRequest(http.MethodGet, env.URL+"/scm/TF/app.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("ben", apitest.UserPassword)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	gitBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(gitBody), "personal access token") {
		t.Errorf("git with the password: %d %s", res.StatusCode, gitBody)
	}

	// New recovery codes replace the old ones.
	var renewed struct {
		Codes []string `json:"codes"`
	}
	signedIn.MustDo(http.MethodPost, "/api/v1/me/two-factor/recovery-codes", map[string]string{"password": apitest.UserPassword}, &renewed)
	if len(renewed.Codes) != 10 {
		t.Fatalf("renewed = %v", renewed.Codes)
	}
	if r := login(recovery.Codes[1]); r.Code() != "auth.second-factor-invalid" {
		t.Errorf("an old recovery code: %d %s", r.Status, r.Body)
	}

	// Turning it off takes the password and a code.
	disable := func(c string) *apitest.Response {
		return signedIn.Do(http.MethodPost, "/api/v1/me/two-factor/disable", map[string]string{"password": apitest.UserPassword, "code": c})
	}
	if r := disable(wrong(crypto.TOTPCode(benSecret, step+1))); r.Code() != "two-factor.code" {
		t.Errorf("disable with a wrong code: %d %s", r.Status, r.Body)
	}
	if r := disable(renewed.Codes[0]); r.Status != http.StatusNoContent {
		t.Fatalf("disable: %d %s", r.Status, r.Body)
	}
	if r := login(""); r.Status != http.StatusOK {
		t.Errorf("sign-in after turning it off: %d %s", r.Status, r.Body)
	}

	// An administrator resets the second factor of a user who lost the device; the user's sessions end.
	carol := env.CreateUser("carol")
	var carolMe struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	carol.Get("/api/v1/me", &carolMe)
	carol.MustDo(http.MethodPost, "/api/v1/me/two-factor", nil, &setup)
	carolSecret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(setup.Secret, " ", ""))
	carol.MustDo(http.MethodPost, "/api/v1/me/two-factor/enable", map[string]string{"password": apitest.UserPassword,
		"code": crypto.TOTPCode(carolSecret, step)}, nil)
	carolLogin := func() *apitest.Response {
		return env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "carol", "password": apitest.UserPassword})
	}
	if r := carolLogin(); r.Code() != "auth.second-factor-required" {
		t.Fatalf("carol without code: %d %s", r.Status, r.Body)
	}
	if r := ben.Do(http.MethodDelete, apitest.Fmt("/api/v1/admin/users/%d/two-factor", carolMe.User.ID), nil); r.Status != http.StatusForbidden {
		t.Errorf("a user resets: %d", r.Status)
	}
	admin.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/admin/users/%d/two-factor", carolMe.User.ID), nil, nil)
	if r := carol.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("session after the reset: %d", r.Status)
	}
	if r := carolLogin(); r.Status != http.StatusOK {
		t.Errorf("sign-in after the reset: %d %s", r.Status, r.Body)
	}

	var events []struct {
		Action string `json:"action"`
	}
	admin.Get("/api/v1/admin/audit?action=auth.two-factor", &events)
	seen := map[string]int{}
	for _, e := range events {
		seen[e.Action]++
	}
	for _, a := range []string{"auth.two-factor.enable", "auth.two-factor.disable", "auth.two-factor.reset", "auth.two-factor.recovery-code-used",
		"auth.two-factor.recovery-codes"} {
		if seen[a] == 0 {
			t.Errorf("no audit event %s: %v", a, seen)
		}
	}
}

// wrong returns another code than c.
// TestRecoveryCodesKeyed: recovery codes are stored as keyed hashes under the master key, so that a copy of the
// database alone does not tell them (40 bits can be tried out against a plain hash); codes stored as plain hashes
// before keep working until they are used.
func TestRecoveryCodesKeyed(t *testing.T) {
	env := apitest.New(t)
	cara := env.CreateUser("cara")
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	cara.Get("/api/v1/me", &me)
	var setup struct {
		Secret string `json:"secret"`
	}
	cara.MustDo(http.MethodPost, "/api/v1/me/two-factor", nil, &setup)
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(setup.Secret, " ", ""))
	var recovery struct {
		Codes []string `json:"codes"`
	}
	cara.MustDo(http.MethodPost, "/api/v1/me/two-factor/enable", map[string]string{"password": apitest.UserPassword,
		"code": crypto.TOTPCode(secret, crypto.TOTPStep(time.Now()))}, &recovery)
	plain := func(code string) []byte { return crypto.HashToken(strings.ToLower(strings.ReplaceAll(code, "-", ""))) }

	ctx := context.Background()
	rows, err := env.App.Pool.Query(ctx, "SELECT code_hash FROM user_recovery_codes WHERE user_id = $1", me.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil || len(stored) != len(recovery.Codes) {
		t.Fatalf("stored: %d %v", len(stored), err)
	}
	for _, c := range recovery.Codes {
		if slices.ContainsFunc(stored, func(h []byte) bool { return bytes.Equal(h, plain(c)) }) {
			t.Errorf("the recovery code %s is stored as its plain hash", c)
		}
	}
	login := func(c string) *apitest.Response {
		return env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": "cara", "password": apitest.UserPassword, "code": c})
	}
	if r := login(recovery.Codes[3]); r.Status != http.StatusOK {
		t.Errorf("a recovery code: %d %s", r.Status, r.Body)
	}

	// A code of before: its plain hash works once.
	if _, err := env.App.Pool.Exec(ctx, "INSERT INTO user_recovery_codes (user_id, code_hash) VALUES ($1, $2)", me.User.ID,
		plain("abcd-efgh")); err != nil {
		t.Fatal(err)
	}
	if r := login("ABCD-EFGH"); r.Status != http.StatusOK {
		t.Errorf("a recovery code stored as its plain hash: %d %s", r.Status, r.Body)
	}
	if r := login("abcd-efgh"); r.Code() != "auth.second-factor-invalid" {
		t.Errorf("a plain recovery code used twice: %d %s", r.Status, r.Body)
	}
}

func wrong(c string) string {
	if c == "000000" {
		return "000001"
	}
	return "000000"
}
