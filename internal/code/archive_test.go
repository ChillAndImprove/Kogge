// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

// TestArchiveFolder: the entries of an archive lie in one folder named after the repository and the ref. The ref
// may be any revision ("main^{/fix}"), so none of its characters may separate paths, also not "\" (Windows).
func TestArchiveFolder(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "WEB", "name": "Web"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/WEB/repos", map[string]any{"name": "Site", "readme": true}, nil)
	for ref, folder := range map[string]string{
		"main":                  "site-main",
		`main^{/(\..\..\.){0}}`: "site-main-----..-..-.--0--",
	} {
		res := admin.Do(http.MethodGet, "/api/v1/code/repos/WEB/site/archive?format=zip&ref="+url.QueryEscape(ref), nil)
		if res.Status != http.StatusOK {
			t.Fatalf("archive of %s: %d %s", ref, res.Status, res.Body)
		}
		zr, err := zip.NewReader(bytes.NewReader(res.Body), int64(len(res.Body)))
		if err != nil {
			t.Fatal(err)
		}
		if len(zr.File) < 2 {
			t.Errorf("archive of %s: %d entries", ref, len(zr.File))
		}
		for _, f := range zr.File {
			if !strings.HasPrefix(f.Name, folder+"/") || strings.Contains(f.Name, `\`) {
				t.Errorf("archive of %s: entry %q", ref, f.Name)
			}
		}
		if cd := res.Header.Get("Content-Disposition"); !strings.HasSuffix(cd, "''"+folder+".zip") {
			t.Errorf("archive of %s: %s", ref, cd)
		}
	}
}
