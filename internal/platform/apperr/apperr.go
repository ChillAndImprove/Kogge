// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package apperr defines Kogge's API errors: RFC 9457 problem details with a stable, machine-readable code.
// Services return *Error for expected failures; anything else becomes a 500 without internal details.
package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// FieldError describes one invalid input.
type FieldError struct {
	Location string `json:"location,omitempty" doc:"Where the error occurred, e.g. body.summary"`
	Message  string `json:"message" doc:"What is wrong"`
	Value    any    `json:"value,omitempty" doc:"The rejected value"`
}

// Error is a problem detail. It implements huma.StatusError and huma.ContentTypeFilter.
type Error struct {
	Type   string       `json:"type,omitempty" doc:"Problem type (about:blank)"`
	Title  string       `json:"title" doc:"Short summary of the problem type"`
	Status int          `json:"status" doc:"HTTP status code"`
	Code   string       `json:"code" doc:"Stable error code, e.g. issue.not-found"`
	Detail string       `json:"detail,omitempty" doc:"Explanation of this occurrence"`
	Errors []FieldError `json:"errors,omitempty" doc:"Invalid inputs"`

	cause    error
	template string // the English detail before formatting: the key of its translations
	args     []any
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

// GetStatus returns the HTTP status (huma.StatusError).
func (e *Error) GetStatus() int { return e.Status }

// ContentType returns the problem media type (huma.ContentTypeFilter).
func (e *Error) ContentType(ct string) string {
	if ct == "application/json" || ct == "" {
		return "application/problem+json"
	}
	return ct
}

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches an internal cause (logged, never sent to clients).
func (e *Error) WithCause(err error) *Error { e.cause = err; return e }

// WithFields adds field errors.
func (e *Error) WithFields(fields ...FieldError) *Error {
	e.Errors = append(e.Errors, fields...)
	return e
}

// New creates an error with the given status, code and message.
func New(status int, code, format string, args ...any) *Error {
	detail := format
	if len(args) > 0 {
		detail = fmt.Sprintf(format, args...)
	}
	return &Error{Title: http.StatusText(status), Status: status, Code: code, Detail: detail, template: format, args: args}
}

// BadRequest is a 400 error.
func BadRequest(code, format string, args ...any) *Error {
	return New(http.StatusBadRequest, code, format, args...)
}

// Invalid is a 422 error for invalid input with field details.
func Invalid(code, detail string, fields ...FieldError) *Error {
	return &Error{Title: http.StatusText(http.StatusUnprocessableEntity), Status: http.StatusUnprocessableEntity,
		Code: code, Detail: detail, Errors: fields, template: detail}
}

// Invalidf is a 422 error for invalid input whose message names values (field details: WithFields).
func Invalidf(code, format string, args ...any) *Error {
	return New(http.StatusUnprocessableEntity, code, format, args...)
}

// Unauthorized is a 401 error.
func Unauthorized(code, format string, args ...any) *Error {
	return New(http.StatusUnauthorized, code, format, args...)
}

// Forbidden is a 403 error.
func Forbidden(code, format string, args ...any) *Error {
	return New(http.StatusForbidden, code, format, args...)
}

// NotFound is a 404 error.
func NotFound(code, format string, args ...any) *Error {
	return New(http.StatusNotFound, code, format, args...)
}

// Conflict is a 409 error.
func Conflict(code, format string, args ...any) *Error {
	return New(http.StatusConflict, code, format, args...)
}

// PreconditionFailed is a 412 error (stale ETag).
func PreconditionFailed(code, format string, args ...any) *Error {
	return New(http.StatusPreconditionFailed, code, format, args...)
}

// TooManyRequests is a 429 error.
func TooManyRequests(code, format string, args ...any) *Error {
	return New(http.StatusTooManyRequests, code, format, args...)
}

// Internal is a 500 error; the cause is kept for logging only. A cause that is the end of the request — its time
// limit or its client going away — makes it that problem instead (see From).
func Internal(cause error) *Error {
	if e := ended(cause); e != nil {
		return e
	}
	return New(http.StatusInternalServerError, "internal", "An internal error occurred.").WithCause(cause)
}

// StatusClientClosedRequest is the status of a request whose client went away before the answer (as nginx's 499):
// nobody reads it, and it is no failure of the server.
const StatusClientClosedRequest = 499

// Timeout is the problem of a request that its time limit stopped (the limits of kinds of requests are in httpx).
func Timeout(cause error) *Error {
	return New(http.StatusGatewayTimeout, "request.timeout",
		"The request took too long and was stopped. Try again later, or ask for less at a time.").WithCause(cause)
}

// ended returns the problem of an error that is the end of the request's context: its time limit (504) or its
// client going away (499); otherwise nil.
func ended(err error) *Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return Timeout(err)
	case errors.Is(err, context.Canceled):
		return New(StatusClientClosedRequest, "request.canceled", "The request was canceled.")
	}
	return nil
}

// From converts any error into a problem: *Error values are returned as they are, except internal errors whose
// cause is the end of the request; a request stopped by its time limit becomes 504, one canceled because its client
// went away 499, everything else an internal error.
func From(err error) *Error {
	var e *Error
	if errors.As(err, &e) && e.Status != http.StatusInternalServerError {
		return e
	}
	if p := ended(err); p != nil {
		return p
	}
	if e != nil {
		return e
	}
	return Internal(err)
}

// Is reports whether err is an *Error with the given code.
func Is(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// IsNotFound reports whether err is an Error with status 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}
