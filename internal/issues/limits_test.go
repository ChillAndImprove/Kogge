// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"kogge.de/kogge/internal/platform/storage"
	"kogge.de/kogge/internal/testutil/apitest"
)

// multipartFiles is a multipart body with files of the given sizes.
func multipartFiles(sizes ...int) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i, n := range sizes {
		fw, _ := mw.CreateFormFile("file", fmt.Sprintf("file-%d.bin", i))
		_, _ = fw.Write(bytes.Repeat([]byte{'x'}, n))
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// TestLargeDescription: an issue description larger than huma's default limit of a request body (a log or a table
// pasted into it) saves.
func TestLargeDescription(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "BIG", "name": "Big", "template": "kanban"}, nil)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/BIG/issue-types", &types)
	var paragraphs []any
	for i := range 12000 {
		paragraphs = append(paragraphs, map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text",
			"text": fmt.Sprintf("2026-10-21 12:00:%02d INFO request %d took %d ms — Größe ok", i%60, i, i%997)}}})
	}
	desc := map[string]any{"type": "doc", "content": paragraphs}
	body, _ := json.Marshal(map[string]any{"project": "BIG", "typeId": types[0].ID, "summary": "Slow requests", "description": desc})
	if len(body) <= 1<<20 {
		t.Fatalf("the description has only %d bytes", len(body))
	}
	var issue struct {
		Key         string `json:"key"`
		Description struct {
			Content []any `json:"content"`
		} `json:"description"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", bytes.NewReader(body), &issue, "Content-Type", "application/json")
	admin.Get("/api/v1/issues/"+issue.Key, &issue)
	if len(issue.Description.Content) != 12000 {
		t.Errorf("the description has %d paragraphs", len(issue.Description.Content))
	}
}

// TestAttachmentLimits: attachments keep to the limits of the settings — the size of a file, 100 files at once, a
// body limited before it is read, and the free space — and who may not see the issue learns nothing of them.
func TestAttachmentLimits(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	outsider := env.CreateUser("eve")
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "ATT", "name": "Attachments", "template": "kanban"}, nil)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/ATT/issue-types", &types)
	var issue struct {
		Key         string `json:"key"`
		Attachments []any  `json:"attachments"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "ATT", "typeId": types[0].ID, "summary": "Files"}, &issue)
	var st map[string]any
	admin.Get("/api/v1/admin/settings", &st)
	st["maxAttachmentMb"] = 1
	admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
	path := "/api/v1/issues/" + issue.Key + "/attachments"
	upload := func(c *apitest.Client, sizes ...int) *apitest.Response {
		body, ct := multipartFiles(sizes...)
		return c.Do(http.MethodPost, path, body, "Content-Type", ct, "Accept-Language", "de")
	}
	// The web application knows the limit, so that it refuses larger files before sending them.
	var me struct {
		MaxAttachmentMB int `json:"maxAttachmentMb"`
	}
	admin.Get("/api/v1/me", &me)
	if me.MaxAttachmentMB != 1 {
		t.Errorf("the session's limit = %d", me.MaxAttachmentMB)
	}

	if r := upload(admin, 512<<10, 10); r.Status != http.StatusCreated {
		t.Fatalf("upload under the limit: %d %s", r.Status, r.Body)
	}
	if r := upload(admin, 1<<20+1); r.Status != http.StatusRequestEntityTooLarge || r.Code() != "attachment.too-large" ||
		!strings.Contains(string(r.Body), "Die Datei ist größer als 1 MiB.") {
		t.Errorf("a file over the limit: %d %s", r.Status, r.Body)
	}
	sizes := make([]int, 101)
	for i := range sizes {
		sizes[i] = 10
	}
	if r := upload(admin, sizes...); r.Status != http.StatusRequestEntityTooLarge || r.Code() != "attachment.too-many" {
		t.Errorf("101 files: %d %s", r.Status, r.Body)
	}
	admin.Get("/api/v1/issues/"+issue.Key, &issue)
	if len(issue.Attachments) != 102 {
		t.Errorf("%d attachments: the 2 first and the 100 files before the refused one", len(issue.Attachments))
	}

	// A body announced larger than 100 files of the largest size is refused before it is sent; one that does not
	// say its length is stopped while it is read.
	early := func(c *apitest.Client) int {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		never, _ := io.Pipe()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, env.URL+path, never)
		req.ContentLength = 200 << 20
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		req.Header.Set("Expect", "100-continue")
		res, err := c.Send(req)
		if err != nil {
			t.Fatalf("announced upload: %v", err)
		}
		defer res.Body.Close()
		var p struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(res.Body).Decode(&p)
		if p.Code != "" && res.StatusCode == http.StatusRequestEntityTooLarge && p.Code != "attachment.too-large" {
			t.Errorf("announced upload: code %s", p.Code)
		}
		return res.StatusCode
	}
	if status := early(admin); status != http.StatusRequestEntityTooLarge {
		t.Errorf("announced upload over the limit: %d", status)
	}
	// Who may not see the issue hears that it does not exist, whatever the upload.
	if status := early(outsider); status != http.StatusNotFound {
		t.Errorf("an outsider's upload: %d", status)
	}
	if r := upload(outsider, 10); r.Status != http.StatusNotFound {
		t.Errorf("an outsider's upload: %d", r.Status)
	}

	// No uploads on a low disk.
	restore := storage.Fake(1<<30, 100<<30)
	defer restore()
	if r := upload(admin, 10); r.Status != http.StatusInsufficientStorage || r.Code() != "storage.low" {
		t.Errorf("upload on a low disk: %d %s", r.Status, r.Body)
	}
	if r := upload(outsider, 10); r.Status != http.StatusNotFound {
		t.Errorf("an outsider's upload on a low disk: %d", r.Status)
	}
}
