// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/wiki"
)

// TestImportedPagesHiddenUntilFinished: pages a migration loads stay hidden until the space is finished and their
// view restrictions are computed — before, every reader of the space saw restricted pages while they were loaded,
// and forever if the load stopped (audit H9).
func TestImportedPagesHiddenUntilFinished(t *testing.T) {
	env := apitest.New(t)
	member, owner := env.CreateUser("member"), env.CreateUser("owner")
	ids := map[string]int64{}
	for name, c := range map[string]*apitest.Client{"member": member, "owner": owner} {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	ctx := context.Background()
	load := func(f func(im *wiki.Importer) error) {
		t.Helper()
		if err := db.InTx(ctx, env.App.Pool, func(tx pgx.Tx) error { return f(env.App.Wiki.NewImporter(tx, "confluence:test")) }); err != nil {
			t.Fatal(err)
		}
	}
	var spaceID, public, salaries int64
	now := time.Now()
	load(func(im *wiki.Importer) error {
		var err error
		if spaceID, err = im.Space(wiki.ImportSpace{SourceID: "1", Key: "HR", Name: "Human resources", CreatorID: ids["owner"], Created: now}); err != nil {
			return err
		}
		if err := im.SpacePermissions(spaceID, []wiki.ImportGrant{
			{Permission: "space.view", HolderType: "user", HolderID: ids["member"]},
			{Permission: "space.view", HolderType: "user", HolderID: ids["owner"]},
		}); err != nil {
			return err
		}
		if public, _, err = im.Page(wiki.ImportPage{SourceID: "10", SpaceID: spaceID, Kind: "page", Status: "current", Title: "Holidays",
			CreatorID: ids["owner"], Created: now, Updated: now}); err != nil {
			return err
		}
		salaries, _, err = im.Page(wiki.ImportPage{SourceID: "11", SpaceID: spaceID, ParentID: public, Kind: "page", Status: "current",
			Title: "Salaries", CreatorID: ids["owner"], Created: now, Updated: now})
		return err
	})
	status := func(c *apitest.Client, id int64) int {
		t.Helper()
		return c.Do(http.MethodGet, apitest.Fmt("/api/v1/wiki/pages/%d", id), nil).Status
	}
	// Loaded, restrictions not yet: nobody reads the pages.
	for _, id := range []int64{public, salaries} {
		if s := status(member, id); s != http.StatusNotFound {
			t.Fatalf("page %d readable while the space is loaded: %d", id, s)
		}
	}
	load(func(im *wiki.Importer) error {
		if err := im.Restrictions(salaries, "view", []int64{ids["owner"]}, nil); err != nil {
			return err
		}
		return im.FinishSpace(ctx, spaceID, public)
	})
	if s := status(member, public); s != http.StatusOK {
		t.Fatalf("the unrestricted page after the load: %d", s)
	}
	if s := status(member, salaries); s != http.StatusNotFound {
		t.Fatalf("the restricted page after the load: %d", s)
	}
	if s := status(owner, salaries); s != http.StatusOK {
		t.Fatalf("the restricted page for its reader: %d", s)
	}

	// Loading again hides what is loaded until it is finished again.
	load(func(im *wiki.Importer) error {
		_, _, err := im.Page(wiki.ImportPage{SourceID: "10", SpaceID: spaceID, Kind: "page", Status: "current", Title: "Holidays",
			CreatorID: ids["owner"], Created: now, Updated: now})
		return err
	})
	if s := status(member, public); s != http.StatusNotFound {
		t.Fatalf("a page being loaded again is readable: %d", s)
	}
	load(func(im *wiki.Importer) error { return im.FinishSpace(ctx, spaceID, public) })
	if s := status(member, public); s != http.StatusOK {
		t.Fatalf("the page after the second load: %d", s)
	}
}
