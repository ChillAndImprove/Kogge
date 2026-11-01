// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/testutil/apitest"
)

// bigTable is a document with a table of rows × cols cells of text, as pasted from a spreadsheet.
func bigTable(rows, cols int) map[string]any {
	var trs []any
	for r := range rows {
		var cells []any
		for c := range cols {
			kind := "tableCell"
			if r == 0 {
				kind = "tableHeader"
			}
			cells = append(cells, map[string]any{"type": kind, "content": []any{map[string]any{"type": "paragraph",
				"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("Zeile %d, Spalte %d: Größe", r+1, c+1)}}}}})
		}
		trs = append(trs, map[string]any{"type": "tableRow", "content": cells})
	}
	return map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "heading", "attrs": map[string]any{"level": 1}, "content": []any{map[string]any{"type": "text", "text": "Inventory"}}},
		map[string]any{"type": "table", "content": trs},
	}}
}

// TestLargeDocuments: pages up to the size of documents save through the API — a page with a big table is larger than
// huma's default limit of a request body — and requests larger than the API's limit are refused with 413.
func TestLargeDocuments(t *testing.T) {
	if api.MaxBodyBytes < 3*docmodel.MaxBytes {
		t.Fatalf("the API takes bodies of %d bytes, too little for documents of %d bytes with other fields and escaped text",
			api.MaxBodyBytes, docmodel.MaxBytes)
	}
	env := apitest.New(t)
	admin := env.Admin()
	admin.Post("/api/v1/wiki/spaces", map[string]any{"key": "BIG", "name": "Big pages"}, nil)
	var pg struct {
		ID int64 `json:"id"`
	}
	admin.Post("/api/v1/wiki/spaces/BIG/pages", map[string]any{"title": "Inventory"}, &pg)
	doc := bigTable(1200, 8)
	body, _ := json.Marshal(map[string]any{"title": "Inventory", "baseVersion": 0, "body": doc})
	if len(body) <= 1<<20 || len(body) >= docmodel.MaxBytes {
		t.Fatalf("the page has %d bytes: not between huma's default limit and the size of documents", len(body))
	}
	if r := admin.Do(http.MethodPut, apitest.Fmt("/api/v1/wiki/pages/%d/draft", pg.ID), map[string]any{"title": "Inventory",
		"baseVersion": 0, "body": doc}); r.Status != http.StatusNoContent && r.Status != http.StatusOK {
		t.Fatalf("saving the draft of a large page: %d %s", r.Status, r.Body)
	}
	r := admin.Do(http.MethodPost, apitest.Fmt("/api/v1/wiki/pages/%d/publish", pg.ID), bytes.NewReader(body), "Content-Type", "application/json")
	if r.Status != http.StatusOK {
		t.Fatalf("publishing a large page: %d %.300s", r.Status, r.Body)
	}
	var page struct {
		Version int `json:"version"`
		Body    struct {
			Content []struct {
				Type    string `json:"type"`
				Content []any  `json:"content"`
			} `json:"content"`
		} `json:"body"`
	}
	admin.Get(apitest.Fmt("/api/v1/wiki/pages/%d", pg.ID), &page)
	if page.Version != 1 || len(page.Body.Content) != 2 || page.Body.Content[1].Type != "table" || len(page.Body.Content[1].Content) != 1200 {
		t.Errorf("the published page: version %d, %d blocks", page.Version, len(page.Body.Content))
	}

	// Larger than any document, larger than the API takes.
	huge, _ := json.Marshal(map[string]any{"title": "Inventory", "baseVersion": 1, "body": bigTable(9000, 8)})
	r = admin.Do(http.MethodPost, apitest.Fmt("/api/v1/wiki/pages/%d/publish", pg.ID), bytes.NewReader(huge), "Content-Type", "application/json")
	if len(huge) <= api.MaxBodyBytes || r.Status != http.StatusRequestEntityTooLarge || r.Code() != "request.too-large" ||
		!strings.Contains(string(r.Body), "larger than the 8 MiB") {
		t.Errorf("a body of %d bytes: %d %.300s", len(huge), r.Status, r.Body)
	}
}

// TestPageAttachmentLimits: page attachments keep to the limits of the settings like those of issues.
func TestPageAttachmentLimits(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.Post("/api/v1/wiki/spaces", map[string]any{"key": "ATT", "name": "Attachments"}, nil)
	var pg struct {
		ID int64 `json:"id"`
	}
	admin.Post("/api/v1/wiki/spaces/ATT/pages", map[string]any{"title": "Files"}, &pg)
	var st map[string]any
	admin.Get("/api/v1/admin/settings", &st)
	st["maxAttachmentMb"] = 1
	admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
	upload := func(sizes ...int) *apitest.Response {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("comment", "limits")
		for i, n := range sizes {
			fw, _ := mw.CreateFormFile("file", fmt.Sprintf("file-%d.bin", i))
			_, _ = fw.Write(bytes.Repeat([]byte{'x'}, n))
		}
		_ = mw.Close()
		return admin.Do(http.MethodPost, apitest.Fmt("/api/v1/wiki/pages/%d/attachments", pg.ID), &buf, "Content-Type", mw.FormDataContentType())
	}
	if r := upload(100 << 10); r.Status != http.StatusCreated {
		t.Fatalf("upload: %d %s", r.Status, r.Body)
	}
	if r := upload(2 << 20); r.Status != http.StatusRequestEntityTooLarge || r.Code() != "attachment.too-large" ||
		!strings.Contains(string(r.Body), "larger than 1 MiB") {
		t.Errorf("a file over the limit: %d %s", r.Status, r.Body)
	}
	many := make([]int, 101)
	if r := upload(many...); r.Status != http.StatusRequestEntityTooLarge || r.Code() != "attachment.too-many" {
		t.Errorf("101 files: %d %s", r.Status, r.Body)
	}
	restore := storage.Fake(1<<30, 100<<30)
	defer restore()
	if r := upload(10); r.Status != http.StatusInsufficientStorage || r.Code() != "storage.low" {
		t.Errorf("upload on a low disk: %d %s", r.Status, r.Body)
	}
}
