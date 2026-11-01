// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestLimitSettings: the limits have defaults that do not get in the way, only administrators change them, the API
// refuses values out of range and changes without them (a client that does not know a limit cannot switch it off),
// and the audit log tells which limit changed from what to what.
func TestLimitSettings(t *testing.T) {
	ctx := context.Background()
	env := apitest.New(t)
	admin := env.Admin()
	user := env.CreateUser("anna")
	var st map[string]any
	admin.Get("/api/v1/admin/settings", &st)
	for name, want := range map[string]float64{"maxPushMb": 2048, "repositoryQuotaMb": 0, "maxGitFileMb": 0, "maxLfsObjectMb": 5120,
		"maxAttachmentMb": 100, "maxArtifactMb": 1024, "maxRunArtifactsMb": 10240, "maxCacheMb": 4096, "maxJobLogMb": 64,
		"minFreeSpaceMb": 2048, "minFreeSpacePercent": 2} {
		if st[name] != want {
			t.Errorf("default %s = %v, want %v", name, st[name], want)
		}
	}

	put := func(c *apitest.Client, change func(map[string]any)) *apitest.Response {
		body := map[string]any{}
		for k, v := range st {
			body[k] = v
		}
		change(body)
		return c.Do(http.MethodPut, "/api/v1/admin/settings", body)
	}
	if r := put(user, func(b map[string]any) { b["maxPushMb"] = 1 }); r.Status != http.StatusForbidden {
		t.Errorf("a user changed the limits: %d", r.Status)
	}
	for name, change := range map[string]func(map[string]any){
		"push of 0":          func(b map[string]any) { b["maxPushMb"] = 0 },
		"negative quota":     func(b map[string]any) { b["repositoryQuotaMb"] = -1 },
		"artifacts of 0":     func(b map[string]any) { b["maxArtifactMb"] = 0 },
		"a log of 20 GiB":    func(b map[string]any) { b["maxJobLogMb"] = 20480 },
		"60 % kept free":     func(b map[string]any) { b["minFreeSpacePercent"] = 60 },
		"no quota field":     func(b map[string]any) { delete(b, "repositoryQuotaMb") },
		"no free space kept": func(b map[string]any) { delete(b, "minFreeSpaceMb") },
	} {
		if r := put(admin, change); r.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}

	if r := put(admin, func(b map[string]any) { b["maxPushMb"] = 4096; b["repositoryQuotaMb"] = 10240 }); r.Status != http.StatusOK {
		t.Fatalf("change: %d %s", r.Status, r.Body)
	}
	var raw []byte
	if err := env.App.Pool.QueryRow(ctx, "SELECT details FROM audit_events WHERE action = 'settings.update' ORDER BY id DESC LIMIT 1").
		Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var details map[string]map[string]int
	_ = json.Unmarshal(raw, &details)
	if len(details) != 2 || details["maxPushMb"]["from"] != 2048 || details["maxPushMb"]["to"] != 4096 ||
		details["repositoryQuotaMb"]["from"] != 0 || details["repositoryQuotaMb"]["to"] != 10240 {
		t.Errorf("audit details = %s", raw)
	}
	admin.Get("/api/v1/admin/settings", &st)
	if st["maxPushMb"] != float64(4096) || st["repositoryQuotaMb"] != float64(10240) {
		t.Errorf("saved: %v %v", st["maxPushMb"], st["repositoryQuotaMb"])
	}
}

// TestLowSpaceOverview: below the free space the settings keep, the overview says that pushes and uploads are refused;
// the free space kept is the larger of the minimum and the share of the disk.
func TestLowSpaceOverview(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	type disk struct {
		Free      uint64      `json:"dataFreeBytes"`
		Total     uint64      `json:"dataTotalBytes"`
		Reserve   uint64      `json:"dataReserveBytes"`
		Attention []attention `json:"attention"`
	}
	var o disk
	// 3 GiB free of 1 TiB: 2 % of it (20 GiB) is more than 2 GiB.
	restore := storage.Fake(3<<30, 1<<40)
	admin.Get("/api/v1/admin/overview", &o)
	restore()
	if o.Free != 3<<30 || o.Total != 1<<40 || o.Reserve != (1<<40)/100*2 || len(o.Attention) == 0 || o.Attention[0].Key != "storage-low" ||
		o.Attention[0].Severity != "critical" {
		t.Errorf("overview on a low disk: %+v", o)
	}
	// Enough space: no warning of it.
	o = disk{}
	admin.Get("/api/v1/admin/overview", &o)
	for _, a := range o.Attention {
		if a.Key == "storage-low" || a.Key == "disk-low" {
			t.Errorf("a warning on a large, empty disk: %+v", a)
		}
	}
}
