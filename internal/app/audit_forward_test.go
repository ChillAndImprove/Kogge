// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/testutil/apitest"
)

type forwardStatus struct {
	Configured  bool   `json:"configured"`
	Active      bool   `json:"active"`
	Target      string `json:"target"`
	Forwarded   int    `json:"forwarded"`
	LastEventID *int64 `json:"lastEventId"`
	Waiting     int    `json:"waiting"`
	Error       string `json:"error"`
}

// TestAuditForwarding: what administrators do through the API reaches the SIEM as JSON lines with their names and
// address, and the administration shows the state of the forwarding.
func TestAuditForwarding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	env := apitest.New(t, func(c *config.Config) {
		c.AuditForward.URL = &url.URL{Scheme: "file", Path: path}
	})
	admin := env.Admin()
	var st forwardStatus
	admin.Get("/api/v1/admin/audit/forwarding", &st)
	if !st.Configured || st.Active || st.Target != "file://"+path || st.Forwarded != 0 {
		t.Errorf("before a node forwards: %+v", st)
	}

	// The test server runs the web role; a worker forwards.
	sink, err := audit.NewSink(audit.SinkConfig{URL: env.App.Cfg.AuditForward.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		audit.NewForwarder(env.App.Pool, audit.ForwardOptions{Sink: sink, Target: audit.TargetName(env.App.Cfg.AuditForward.URL),
			Instance: env.URL, Logger: slog.New(slog.DiscardHandler), Poll: 20 * time.Millisecond}).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	waitFor("the forwarder", func() bool { admin.Get("/api/v1/admin/audit/forwarding", &st); return st.Active })

	var group struct {
		ID int64 `json:"id"`
	}
	admin.Post("/api/v1/admin/groups", map[string]any{"name": "Auditors"}, &group)
	var event map[string]any
	waitFor("the forwarded event", func() bool {
		f, err := os.Open(path)
		if err != nil {
			return false
		}
		defer f.Close()
		for s := bufio.NewScanner(f); s.Scan(); {
			var ev map[string]any
			if err := json.Unmarshal(s.Bytes(), &ev); err != nil {
				t.Fatalf("not a JSON line: %s", s.Text())
			}
			if ev["action"] == "group.create" {
				event = ev
				return true
			}
		}
		return false
	})
	for k, want := range map[string]any{"actorUsername": "admin", "actorIp": "127.0.0.1", "targetType": "group",
		"targetId": apitest.Fmt("%d", group.ID), "instance": env.URL} {
		if event[k] != want {
			t.Errorf("%s = %v, want %v", k, event[k], want)
		}
	}
	if d, _ := event["details"].(map[string]any); d["name"] != "Auditors" {
		t.Errorf("details = %v", event["details"])
	}

	waitFor("the status", func() bool { admin.Get("/api/v1/admin/audit/forwarding", &st); return st.Waiting == 0 })
	if !st.Active || st.Forwarded < 2 || st.LastEventID == nil || st.Error != "" {
		t.Errorf("status = %+v", st)
	}
}

// TestAuditForwardingNotConfigured: without a target, the administration says so.
func TestAuditForwardingNotConfigured(t *testing.T) {
	env := apitest.New(t)
	var st forwardStatus
	env.Admin().Get("/api/v1/admin/audit/forwarding", &st)
	if st.Configured || st.Active || st.Target != "" {
		t.Errorf("status = %+v", st)
	}
}
