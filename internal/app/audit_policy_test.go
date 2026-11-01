// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"context"
	"net/http"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestWhenTheAuditLogTakesNoEvents: changes do not happen without their record, sign-ins included; failed sign-ins
// stay failures, and revocations — revoking a token, signing out — take effect, their records being logged instead.
func TestWhenTheAuditLogTakesNoEvents(t *testing.T) {
	env := apitest.New(t)
	lena := env.CreateUser("lena")
	var token struct {
		Token       string `json:"token"`
		AccessToken struct {
			ID int64 `json:"id"`
		} `json:"accessToken"`
	}
	lena.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "cli", "scopes": []string{"read"}}, &token)
	ctx := context.Background()
	for _, sql := range []string{
		`CREATE FUNCTION refuse_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'the audit log is full'; END $$`,
		`CREATE TRIGGER refuse_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION refuse_audit()`,
	} {
		if _, err := env.App.Pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	tokens := func() int {
		t.Helper()
		var n int
		if err := env.App.Pool.QueryRow(ctx, "SELECT count(*) FROM access_tokens").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if r := lena.Do(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "unrecorded", "scopes": []string{"read"}}); r.Status != http.StatusInternalServerError {
		t.Errorf("a token without its record: %d %s", r.Status, r.Body)
	}
	if n := tokens(); n != 1 {
		t.Errorf("%d tokens after the refused change", n)
	}
	login := func(password string) int {
		return env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "lena", "password": password}).Status
	}
	if s := login(apitest.UserPassword); s != http.StatusInternalServerError {
		t.Errorf("a sign-in without its record: %d", s)
	}
	if s := login("Wrong-Password-2026"); s != http.StatusUnauthorized {
		t.Errorf("a failed sign-in: %d", s)
	}

	if r := lena.Do(http.MethodDelete, apitest.Fmt("/api/v1/me/tokens/%d", token.AccessToken.ID), nil); r.Status != http.StatusNoContent {
		t.Errorf("revoking a token: %d %s", r.Status, r.Body)
	}
	if n := tokens(); n != 0 {
		t.Errorf("the token survived its revocation: %d tokens", n)
	}
	if r := lena.Do(http.MethodPost, "/api/v1/auth/logout", nil); r.Status != http.StatusOK && r.Status != http.StatusNoContent {
		t.Errorf("signing out: %d %s", r.Status, r.Body)
	}
	if r := lena.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("the session survived signing out: %d", r.Status)
	}
}
