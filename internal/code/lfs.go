// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
)

// Git LFS: the batch API with the basic transfer adapter. Objects are stored in the blob store
// under their SHA-256, which is the LFS object id; a repository may only read objects that were uploaded to it.

const lfsMediaType = "application/vnd.git-lfs+json"

var oidPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// lfsClaims is the content of the short-lived tokens of LFS transfers (for SSH clients and transfer links). A token
// records what it was given for — a user, a deploy key, a build job or anonymous reading — and every use checks that
// this still allows it; tokens are not renewed by using them.
type lfsClaims struct {
	RepoID    int64 `json:"r"`
	UserID    int64 `json:"u,omitempty"`
	DeployKey int64 `json:"d,omitempty"`
	// JobToken is the token of the build job the LFS token was given to: valid while the job runs. The LFS token
	// is sealed, so it does not reveal it.
	JobToken  string `json:"j,omitempty"`
	Anonymous bool   `json:"a,omitempty"`
	Write     bool   `json:"w,omitempty"`
	Expires   int64  `json:"e"`
	Nonce     string `json:"n,omitempty"`
}

func (s *Service) lfsToken(c lfsClaims) (string, error) {
	if s.opt.Keyring == nil {
		return "", errors.New("lfs: no keyring")
	}
	b, _ := json.Marshal(c)
	return s.opt.Keyring.Seal(b, "lfs")
}

func (s *Service) openLFSToken(token string) (*lfsClaims, error) {
	if s.opt.Keyring == nil {
		return nil, errors.New("lfs: no keyring")
	}
	b, err := s.opt.Keyring.Open(token, "lfs")
	if err != nil {
		return nil, err
	}
	var c lfsClaims
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if time.Now().Unix() > c.Expires {
		return nil, errors.New("lfs: token expired")
	}
	return &c, nil
}

// lfsAccess authenticates an LFS request and returns the repository with the caller's access, and the claims if the
// request came with an LFS token.
func (h *Handler) lfsAccess(r *http.Request, write bool) (*identity.Principal, *repoRow, *lfsClaims, error) {
	s := h.S
	auth := r.Header.Get("Authorization")
	if bearer, ok := strings.CutPrefix(auth, "Bearer "); ok && !strings.HasPrefix(bearer, "kgp_") {
		c, err := s.openLFSToken(bearer)
		if err != nil {
			return nil, nil, nil, apperr.Unauthorized("lfs.token", "The LFS token is invalid or expired.")
		}
		repo, err := repoByPath(r.Context(), s.pool, chi.URLParam(r, "key"), chi.URLParam(r, "repo"))
		if err != nil || repo.ID != c.RepoID {
			return nil, nil, nil, apperr.Unauthorized("lfs.token", "The LFS token is not valid for this repository.")
		}
		if write && !c.Write {
			return nil, nil, nil, apperr.Forbidden("repository.write-required", "You may not change this repository.")
		}
		if write && repo.Archived {
			return nil, nil, nil, apperr.Conflict("repository.archived", "The repository is archived.")
		}
		p, err := s.lfsGrantHolds(r.Context(), c, repo, write)
		if err != nil {
			return nil, nil, nil, err
		}
		return p, repo, c, nil
	}
	service := "upload-pack"
	if write {
		service = "receive-pack"
	}
	p, repo, err := h.gitRepo(r, service)
	return p, repo, nil, err
}

// lfsGrantHolds checks that what an LFS token was given for still allows its use: the user's current access, the
// deploy key, the running build job, or anonymous reading of the repository. It returns the user's principal.
func (s *Service) lfsGrantHolds(ctx context.Context, c *lfsClaims, repo *repoRow, write bool) (*identity.Principal, error) {
	revoked := apperr.Unauthorized("lfs.token", "The LFS token is no longer valid: the access it was given for has ended.")
	need := LevelRead
	if write {
		need = LevelWrite
	}
	switch {
	case c.UserID != 0:
		p, err := s.identity.PrincipalOf(ctx, c.UserID, identity.MethodSSH)
		if err != nil {
			return nil, revoked
		}
		a, err := s.Access(ctx, p)
		if err != nil {
			return nil, err
		}
		if a.Level(repo.ProjectID, repo.ID) < need {
			return nil, revoked
		}
		return p, nil
	case c.DeployKey != 0:
		var canWrite bool
		err := s.pool.QueryRow(ctx, "SELECT write FROM ssh_keys WHERE id = $1 AND kind = 'deploy' AND repository_id = $2", c.DeployKey, repo.ID).Scan(&canWrite)
		if err != nil || (write && !canWrite) {
			if err != nil && !db.IsNoRows(err) {
				return nil, err
			}
			return nil, revoked
		}
		return nil, nil
	case c.JobToken != "":
		if write || s.jobAuth == nil {
			return nil, revoked
		}
		if repoID, ok := s.jobAuth(ctx, c.JobToken); !ok || repoID != repo.ID {
			return nil, revoked
		}
		return nil, nil
	case c.Anonymous:
		a, err := s.Access(ctx, identity.Anonymous)
		if err != nil {
			return nil, err
		}
		if write || a.Level(repo.ProjectID, repo.ID) < LevelRead {
			return nil, revoked
		}
		return nil, nil
	}
	return nil, revoked
}

type lfsObject struct {
	OID           string               `json:"oid"`
	Size          int64                `json:"size"`
	Authenticated bool                 `json:"authenticated,omitempty"`
	Actions       map[string]lfsAction `json:"actions,omitempty"`
	Error         *lfsObjectError      `json:"error,omitempty"`
}

type lfsObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lfsAction struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
}

func lfsError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", lfsMediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}

func (h *Handler) lfsFail(w http.ResponseWriter, r *http.Request, p *identity.Principal, err error) {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		if (ae.Status == http.StatusNotFound || ae.Status == http.StatusUnauthorized) && (p == nil || !p.Authenticated()) {
			w.Header().Set("LFS-Authenticate", `Basic realm="Kogge"`)
			w.Header().Set("WWW-Authenticate", `Basic realm="Kogge"`)
			lfsError(w, http.StatusUnauthorized, "Authentication required: use your user name and a personal access token.")
			return
		}
		lfsError(w, ae.Status, ae.Detail)
		return
	}
	h.S.log.Error("lfs request failed", "error", err)
	lfsError(w, http.StatusInternalServerError, "Kogge could not handle the request.")
}

// lfsBatch answers the batch API: which objects to transfer, and how.
func (h *Handler) lfsBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operation string      `json:"operation"`
		Transfers []string    `json:"transfers"`
		Objects   []lfsObject `json:"objects"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil || (req.Operation != "download" && req.Operation != "upload") {
		lfsError(w, http.StatusUnprocessableEntity, "Invalid batch request.")
		return
	}
	if len(req.Objects) > 10000 {
		lfsError(w, http.StatusRequestEntityTooLarge, "Too many objects in one request.")
		return
	}
	write := req.Operation == "upload"
	p, repo, used, err := h.lfsAccess(r, write)
	if err != nil {
		h.lfsFail(w, r, p, err)
		return
	}
	s := h.S
	// A request that came with an LFS token gets the same token back, with its expiry: using a token does not
	// extend it. Others get a token for what they authenticated as.
	var token string
	var expires int64
	if used != nil {
		token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		expires = used.Expires
	} else {
		claims := lfsClaims{RepoID: repo.ID, Write: write, Expires: time.Now().Add(time.Hour).Unix()}
		switch {
		case p != nil && p.Authenticated():
			claims.UserID = p.UserID()
		case p != nil:
			claims.Anonymous = true
		default:
			// Build jobs authenticate with their job token (see gitRepo).
			if _, secret, ok := r.BasicAuth(); ok && strings.HasPrefix(secret, "kgj_") {
				claims.JobToken = secret
			}
		}
		if token, err = s.lfsToken(claims); err != nil {
			h.lfsFail(w, r, p, err)
			return
		}
		expires = claims.Expires
	}
	expiresIn := max(int(expires-time.Now().Unix()), 1)
	st, err := s.settings.Get(r.Context())
	if err != nil {
		h.lfsFail(w, r, p, err)
		return
	}
	maxSize := settings.MiB(st.MaxLFSObjectMB)
	// Uploads count towards the quota of the repository, in their order, and need free space.
	quota, taken := quotaOf(st, repo), repo.Size+repo.LFSSize
	lowSpace := write && storage.Check(st, s.blobs.Dir(), 0) != nil
	base := strings.TrimRight(s.opt.PublicURL.String(), "/") + "/scm/" + strings.ToLower(repo.ProjectKey) + "/" + repo.Slug + ".git/info/lfs/objects/"
	header := map[string]string{"Authorization": "Bearer " + token}
	oids := []string{}
	for _, o := range req.Objects {
		if oidPattern.MatchString(o.OID) {
			oids = append(oids, o.OID)
		}
	}
	present := map[string]bool{}
	rows, err := s.pool.Query(r.Context(), "SELECT oid FROM lfs_objects WHERE repository_id = $1 AND oid = ANY($2)", repo.ID, oids)
	if err != nil {
		h.lfsFail(w, r, p, err)
		return
	}
	for rows.Next() {
		var oid string
		if err := rows.Scan(&oid); err != nil {
			rows.Close()
			h.lfsFail(w, r, p, err)
			return
		}
		present[oid] = true
	}
	rows.Close()
	out := make([]lfsObject, 0, len(req.Objects))
	for _, o := range req.Objects {
		res := lfsObject{OID: o.OID, Size: o.Size}
		switch {
		case !oidPattern.MatchString(o.OID) || o.Size < 0:
			res.Error = &lfsObjectError{422, "invalid object"}
		case write && present[o.OID]:
			// Already uploaded: nothing to do.
		case write && o.Size > maxSize:
			res.Error = &lfsObjectError{http.StatusRequestEntityTooLarge, lfsTooLarge(maxSize)}
		case write && lowSpace:
			res.Error = &lfsObjectError{http.StatusInsufficientStorage, lfsLowSpace}
		case write && quota > 0 && taken+o.Size > quota:
			res.Error = &lfsObjectError{http.StatusInsufficientStorage, lfsOverQuota(taken, o.Size, quota)}
		case write:
			taken += o.Size
			res.Authenticated = true
			res.Actions = map[string]lfsAction{
				"upload": {Href: base + o.OID, Header: header, ExpiresIn: expiresIn},
				"verify": {Href: base + o.OID + "/verify", Header: header, ExpiresIn: expiresIn},
			}
		case present[o.OID]:
			res.Authenticated = true
			res.Actions = map[string]lfsAction{"download": {Href: base + o.OID, Header: header, ExpiresIn: expiresIn}}
		default:
			res.Error = &lfsObjectError{404, "object not found"}
		}
		out = append(out, res)
	}
	w.Header().Set("Content-Type", lfsMediaType)
	_ = json.NewEncoder(w).Encode(map[string]any{"transfer": "basic", "objects": out, "hash_algo": "sha256"})
}

// lfsDownload streams an object of the repository.
func (h *Handler) lfsDownload(w http.ResponseWriter, r *http.Request) {
	p, repo, _, err := h.lfsAccess(r, false)
	if err != nil {
		h.lfsFail(w, r, p, err)
		return
	}
	oid := chi.URLParam(r, "oid")
	var size int64
	if err := h.S.pool.QueryRow(r.Context(), "SELECT size FROM lfs_objects WHERE repository_id = $1 AND oid = $2", repo.ID, oid).Scan(&size); err != nil {
		lfsError(w, http.StatusNotFound, "Object not found.")
		return
	}
	f, err := h.S.blobs.Open(oid)
	if err != nil {
		lfsError(w, http.StatusNotFound, "Object not found.")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = io.Copy(w, f)
}

// lfsUpload stores an object; its content must have the announced id, or nothing of it is kept.
func (h *Handler) lfsUpload(w http.ResponseWriter, r *http.Request) {
	p, repo, _, err := h.lfsAccess(r, true)
	if err != nil {
		h.lfsFail(w, r, p, err)
		return
	}
	oid := chi.URLParam(r, "oid")
	if !oidPattern.MatchString(oid) {
		lfsError(w, http.StatusUnprocessableEntity, "Invalid object id.")
		return
	}
	st, err := h.S.settings.Get(r.Context())
	if err != nil {
		h.lfsFail(w, r, p, err)
		return
	}
	// The limits are checked before the content is read, by its announced length (git-lfs always sends it); the
	// maximum size also stops a longer content while it is read.
	maxSize := settings.MiB(st.MaxLFSObjectMB)
	if r.ContentLength > maxSize {
		lfsError(w, http.StatusRequestEntityTooLarge, lfsTooLarge(maxSize))
		return
	}
	if storage.Check(st, h.S.blobs.Dir(), r.ContentLength) != nil {
		lfsError(w, http.StatusInsufficientStorage, lfsLowSpace)
		return
	}
	if quota, used := quotaOf(st, repo), repo.Size+repo.LFSSize; quota > 0 && !h.S.lfsObjectOf(r.Context(), repo.ID, oid) &&
		used+max(r.ContentLength, 0) > quota {
		lfsError(w, http.StatusInsufficientStorage, lfsOverQuota(used, max(r.ContentLength, 0), quota))
		return
	}
	ref, err := h.S.blobs.PutVerified(r.Context(), r.Body, oid, maxSize, "application/octet-stream")
	switch {
	case errors.Is(err, blob.ErrMismatch):
		lfsError(w, http.StatusUnprocessableEntity, fmt.Sprintf("The content does not match the object id %s.", oid))
		return
	case errors.Is(err, blob.ErrTooLarge):
		lfsError(w, http.StatusRequestEntityTooLarge, lfsTooLarge(maxSize))
		return
	case err != nil:
		h.lfsFail(w, r, p, err)
		return
	}
	var userID int64
	if p != nil {
		userID = p.UserID()
	}
	err = db.InTx(r.Context(), h.S.pool, func(tx pgx.Tx) error {
		return h.S.addLFSObject(r.Context(), tx, repo.ID, oid, ref.Size, userID)
	})
	if err != nil {
		h.lfsFail(w, r, p, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// addLFSObject records an object of a repository (in tx) and counts its size towards the repository's.
func (s *Service) addLFSObject(ctx context.Context, tx pgx.Tx, repoID int64, oid string, size, userID int64) error {
	tag, err := tx.Exec(ctx, `INSERT INTO lfs_objects (repository_id, oid, size, created_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, repoID, oid, size, nullID(userID))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, "UPDATE repositories SET lfs_bytes = lfs_bytes + $2 WHERE id = $1", repoID, size); err != nil {
			return err
		}
	}
	return blob.AddRef(ctx, tx, oid, "lfs", repoID)
}

// Answers to LFS clients that limits refuse.
const lfsLowSpace = "The server is running out of disk space, so it accepts no uploads for now. Please tell an administrator."

func lfsTooLarge(limit int64) string {
	return fmt.Sprintf("The object is larger than the %s this installation allows.", storage.Format(limit))
}

func lfsOverQuota(used, size, quota int64) string {
	return fmt.Sprintf("The object of %s does not fit into the quota of the repository, which takes %s of %s. Ask an administrator for a larger quota.",
		storage.Format(size), storage.Format(used), storage.Format(quota))
}

// lfsVerify confirms an upload.
func (h *Handler) lfsVerify(w http.ResponseWriter, r *http.Request) {
	p, repo, _, err := h.lfsAccess(r, true)
	if err != nil {
		h.lfsFail(w, r, p, err)
		return
	}
	var req struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	var size int64
	if err := h.S.pool.QueryRow(r.Context(), "SELECT size FROM lfs_objects WHERE repository_id = $1 AND oid = $2", repo.ID, chi.URLParam(r, "oid")).
		Scan(&size); err != nil || (req.Size != 0 && req.Size != size) {
		lfsError(w, http.StatusNotFound, "Object not found.")
		return
	}
	w.Header().Set("Content-Type", lfsMediaType)
	w.WriteHeader(http.StatusOK)
}

// lfsAuthenticate answers git-lfs-authenticate over SSH: the address of the LFS API with a short-lived token.
func (s *Service) lfsAuthenticate(ctx context.Context, r *repoRow, userID, deployKey int64, write bool) ([]byte, error) {
	token, err := s.lfsToken(lfsClaims{RepoID: r.ID, UserID: userID, DeployKey: deployKey, Write: write, Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		return nil, err
	}
	href := strings.TrimRight(s.opt.PublicURL.String(), "/") + "/scm/" + strings.ToLower(r.ProjectKey) + "/" + r.Slug + ".git/info/lfs"
	return json.Marshal(map[string]any{"href": href, "header": map[string]string{"Authorization": "Bearer " + token}, "expires_in": 3600})
}

// lfsObjectOf reports whether an LFS object belongs to a repository (for showing files stored with LFS).
func (s *Service) lfsObjectOf(ctx context.Context, repoID int64, oid string) bool {
	var ok bool
	_ = s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM lfs_objects WHERE repository_id = $1 AND oid = $2)", repoID, oid).Scan(&ok)
	return ok
}
