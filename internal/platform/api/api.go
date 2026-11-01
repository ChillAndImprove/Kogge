// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package api sets up the huma API (OpenAPI 3.1, problem details) on the router and provides helpers for
// operations, pagination and optimistic concurrency.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"kogge.de/kogge/internal/platform/apperr"
)

func init() {
	// Kogge never returns null for lists; the generated client types should say so.
	huma.DefaultArrayNullable = false
	// All errors produced by huma (validation, unknown routes inside the API) become Kogge problem details, and
	// the OpenAPI document describes that error model.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		if status == http.StatusRequestTimeout {
			// The request body did not arrive in time (httpx.Limits, or the operation's own body deadline).
			return apperr.New(http.StatusRequestTimeout, "request.body-timeout", "The request body arrived too slowly.")
		}
		if status >= 500 {
			for _, err := range errs {
				// A request whose client went away failed nowhere in the server; one that its time limit stopped
				// says so (504).
				if errors.Is(err, context.Canceled) {
					return apperr.From(err)
				}
				if errors.Is(err, context.DeadlineExceeded) {
					slog.Default().Warn("a request was stopped at its time limit", "error", err)
					return apperr.From(err)
				}
			}
			// Internal errors never reach clients; they are logged with the request id.
			for _, err := range errs {
				if err != nil {
					slog.Default().Error("internal error", "error", err)
				}
			}
			return apperr.New(status, codeFor(status), "An internal error occurred.")
		}
		if status == http.StatusRequestEntityTooLarge {
			// huma words it with the limit in bytes; every operation has the same limit.
			return apperr.New(status, codeFor(status), "The request is larger than the %d MiB an operation accepts.", MaxBodyBytes>>20)
		}
		e := &apperr.Error{Title: http.StatusText(status), Status: status, Code: codeFor(status), Detail: msg}
		for _, err := range errs {
			if err == nil {
				continue
			}
			if d, ok := err.(huma.ErrorDetailer); ok {
				det := d.ErrorDetail()
				e.Errors = append(e.Errors, apperr.FieldError{Location: det.Location, Message: det.Message, Value: det.Value})
			} else {
				e.Errors = append(e.Errors, apperr.FieldError{Message: err.Error()})
			}
		}
		return e
	}
}

func codeFor(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "request.invalid"
	case http.StatusUnprocessableEntity:
		return "request.validation"
	case http.StatusUnauthorized:
		return "auth.required"
	case http.StatusForbidden:
		return "permission.denied"
	case http.StatusNotFound:
		return "not-found"
	case http.StatusMethodNotAllowed:
		return "request.method"
	case http.StatusRequestEntityTooLarge:
		return "request.too-large"
	case http.StatusPreconditionFailed:
		return "request.stale"
	case http.StatusConflict:
		return "conflict"
	default:
		if status >= 500 {
			return "internal"
		}
		return "error"
	}
}

// New creates the API on the router. The OpenAPI document is served at /api/v1/openapi.json; the interactive
// documentation is part of the web application (no external assets).
// New creates the API on a router. The bodies of its operations must arrive within bodyTimeout (0: BodyReadTimeout),
// the time limit of ordinary requests (KOGGE_REQUEST_TIMEOUT): huma sets the read deadline of an operation's body
// itself, in place of the one the request limits set.
func New(r chi.Router, version string, bodyTimeout time.Duration) huma.API {
	cfg := huma.DefaultConfig("Kogge API", version)
	cfg.Components.Schemas = huma.NewMapRegistry("#/components/schemas/", schemaNamer)
	cfg.CreateHooks = nil
	cfg.DocsPath = ""
	cfg.SchemasPath = ""
	cfg.OpenAPIPath = "/api/v1/openapi"
	cfg.Info.Description = "REST API of Kogge: issues, wiki, code and builds. Authenticate with a session " +
		"(web application, CSRF token in X-CSRF-Token) or a personal access token (Authorization: Bearer). " +
		"Errors are problem details (RFC 9457) with a stable code; their detail is in English, or in German with " +
		"Accept-Language: de."
	cfg.Info.License = &huma.License{Name: "EUPL-1.2", URL: "https://interoperable-europe.ec.europa.eu/collection/eupl"}
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"token":   {Type: "http", Scheme: "bearer", Description: "Personal access token"},
		"session": {Type: "apiKey", In: "cookie", Name: "__Host-kogge_session", Description: "Browser session"},
	}
	cfg.Security = []map[string][]string{{"token": {}}, {"session": {}}}
	// Errors are worded in the language the client asks for (Accept-Language: de or en).
	cfg.Transformers = append(cfg.Transformers, func(ctx huma.Context, _ string, v any) (any, error) {
		if e, ok := v.(*apperr.Error); ok {
			return e.Localize(apperr.Language(ctx.Header("Accept-Language"))), nil
		}
		return v, nil
	})
	h := humachi.New(r, cfg)
	if bodyTimeout > 0 {
		oapi := h.OpenAPI()
		oapi.OnAddOperation = append(oapi.OnAddOperation, func(_ *huma.OpenAPI, op *huma.Operation) {
			if op.BodyReadTimeout > 0 {
				op.BodyReadTimeout = bodyTimeout
			}
		})
	}
	return h
}

var schemaNames = struct {
	sync.Mutex
	byName map[string]reflect.Type
}{byName: map[string]reflect.Type{}}

// schemaNamer names schemas like huma's default, but prefixes the package (admin.userList → AdminUserList) for
// unexported types and for exported types whose name another package already uses (authz.Grant, issues.Grant).
func schemaNamer(t reflect.Type, hint string) string {
	name := huma.DefaultSchemaNamer(t, hint)
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	n := t.Name()
	if n == "" || !strings.HasPrefix(t.PkgPath(), "kogge.de/") {
		return name
	}
	pkg := path.Base(t.PkgPath())
	prefixed := strings.ToUpper(pkg[:1]) + pkg[1:] + name
	if unicode.IsLower(rune(n[0])) {
		return prefixed
	}
	schemaNames.Lock()
	defer schemaNames.Unlock()
	if existing, ok := schemaNames.byName[name]; ok && existing != t {
		return prefixed
	}
	schemaNames.byName[name] = t
	return name
}

// The bodies of operations: huma reads them into memory before it decodes them, so their size is limited (huma's
// default would be 1 MiB, too little for the largest documents). Documents (pages, issue descriptions) may have up
// to 2 MiB in compact JSON (docmodel.MaxBytes) and files edited in the browser 2 MiB; the limit leaves room for
// clients that escape every non-ASCII character or indent their JSON, and for a document with other fields. Files
// go through the upload routes, not through operations.
const (
	// MaxBodyBytes is the largest request body of an operation.
	MaxBodyBytes = 8 << 20
	// BodyReadTimeout is how long reading the body of an operation may take unless New is given the time limit of
	// requests (huma's default of 5 seconds is too short for the largest bodies on a slow connection).
	BodyReadTimeout = time.Minute
)

// Op returns an operation with the common fields set.
func Op(id, method, path, summary string, tags ...string) huma.Operation {
	return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: tags,
		MaxBodyBytes: MaxBodyBytes, BodyReadTimeout: BodyReadTimeout}
}

// Page is the common pagination input.
type Page struct {
	Limit  int `query:"limit" minimum:"1" maximum:"500" default:"50" doc:"Maximum number of items"`
	Offset int `query:"offset" minimum:"0" default:"0" doc:"Number of items to skip"`
}

// ETag formats a version as a strong entity tag.
func ETag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// IfMatch parses an If-Match header with a version produced by ETag. ok is false if the header is absent.
func IfMatch(header string) (version int64, ok bool, err error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false, nil
	}
	v, convErr := strconv.ParseInt(strings.Trim(strings.TrimPrefix(header, "W/"), `"`), 10, 64)
	if convErr != nil {
		return 0, false, apperr.BadRequest("request.if-match", "If-Match must be an ETag returned by the server.")
	}
	return v, true, nil
}
