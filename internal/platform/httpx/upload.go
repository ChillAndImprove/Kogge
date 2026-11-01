// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package httpx

import (
	"errors"
	"net/http"

	"kogge.de/kogge/internal/platform/apperr"
)

// Uploads of files (multipart/form-data, such as attachments) carry at most MaxFiles files of at most a size each.
// Their bodies are limited before they are read: a request that announces a larger body is refused at once, and
// reading more than the limit fails.

// MaxFiles is the most files one upload may carry.
const MaxFiles = 100

// formOverhead is what a multipart body takes besides the files: the boundaries and headers of the parts, and small
// fields such as a comment.
const formOverhead = MaxFiles<<12 + 1<<20

// UploadLimit returns the largest body of an upload of files of at most maxFile bytes each.
func UploadLimit(maxFile int64) int64 { return MaxFiles*maxFile + formOverhead }

// LimitUpload refuses a request whose announced body is longer than limit with the error tooLarge (413), and makes
// reading more of its body than limit fail (see TooLarge). It reports whether the request goes on.
func LimitUpload(w http.ResponseWriter, r *http.Request, limit int64, tooLarge error) bool {
	if r.ContentLength > limit {
		WriteProblem(w, r, tooLarge)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return true
}

// TooLarge reports whether err comes from reading beyond the limit of LimitUpload.
func TooLarge(err error) bool {
	var e *http.MaxBytesError
	return errors.As(err, &e)
}

// TooManyFiles is the error of uploads with more than MaxFiles files.
func TooManyFiles() error {
	return apperr.New(http.StatusRequestEntityTooLarge, "attachment.too-many", "Send at most %d files at once.", MaxFiles)
}
