// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/httpx"
)

// Routes registers the file endpoints (multipart upload and download) on the authenticated router.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/v1/issues/{key}/attachments", h.upload)
	r.Get("/api/v1/issue-attachments/{id}", h.download)
}

// upload streams the files of a multipart request into attachments. The request is limited before it is read (who
// may attach, the free space, the size); a file that is refused ends it, the files before it are kept.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	p := identity.From(r.Context())
	if !p.Authenticated() {
		httpx.WriteProblem(w, r, apperr.Unauthorized("auth.required", "Please sign in."))
		return
	}
	key := chi.URLParam(r, "key")
	maxFile, err := h.S.UploadLimit(r.Context(), p, key, r.ContentLength)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	if !httpx.LimitUpload(w, r, httpx.UploadLimit(maxFile), attachmentTooLarge(maxFile)) {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteProblem(w, r, apperr.BadRequest("attachment.form", "Send the files as multipart/form-data."))
		return
	}
	var out []*Attachment
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if httpx.TooLarge(err) {
			httpx.WriteProblem(w, r, attachmentTooLarge(maxFile))
			return
		}
		if err != nil {
			httpx.WriteProblem(w, r, apperr.BadRequest("attachment.form", "The upload was interrupted."))
			return
		}
		if part.FileName() == "" {
			_ = part.Close()
			continue
		}
		if len(out) == httpx.MaxFiles {
			_ = part.Close()
			httpx.WriteProblem(w, r, httpx.TooManyFiles())
			return
		}
		at, err := h.S.AddAttachment(r.Context(), p, key, part.FileName(), part.Header.Get("Content-Type"), part)
		_ = part.Close()
		if httpx.TooLarge(err) {
			err = attachmentTooLarge(maxFile)
		}
		if err != nil {
			httpx.WriteProblem(w, r, err)
			return
		}
		out = append(out, at)
	}
	if len(out) == 0 {
		httpx.WriteProblem(w, r, apperr.BadRequest("attachment.empty", "No file was sent."))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.NotFound("attachment.not-found", "The attachment does not exist."))
		return
	}
	at, sha, err := h.S.AttachmentFile(r.Context(), identity.From(r.Context()), id)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	f, err := h.S.blobs.Open(sha)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.Internal(err))
		return
	}
	defer f.Close()
	httpx.FileHeaders(w, at.ContentType, at.Filename, r.URL.Query().Get("download") != "")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", at.CreatedAt, f)
}
