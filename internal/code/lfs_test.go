// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"kogge.de/kogge/internal/testutil/apitest"
)

type lfsBatch struct {
	Objects []struct {
		OID     string `json:"oid"`
		Actions map[string]struct {
			Href   string            `json:"href"`
			Header map[string]string `json:"header"`
		} `json:"actions"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	} `json:"objects"`
}

func TestLFS(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "ART", "name": "Artwork"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/ART/repos", map[string]any{"name": "assets", "readme": true}, &repo)
	var token struct {
		Token string `json:"token"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "lfs", "scopes": []string{"read", "write"}}, &token)

	content := []byte("a large design file")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	lfsURL := strings.TrimSuffix(repo.CloneHTTP, ".git") + ".git/info/lfs/objects/batch"
	batch := func(op string, auth func(*http.Request)) (*lfsBatch, int) {
		body, _ := json.Marshal(map[string]any{"operation": op, "transfers": []string{"basic"},
			"objects": []map[string]any{{"oid": oid, "size": len(content)}}})
		req, _ := http.NewRequest(http.MethodPost, lfsURL, bytes.NewReader(body))
		req.Header.Set("Accept", "application/vnd.git-lfs+json")
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		auth(req)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var b lfsBatch
		_ = json.NewDecoder(res.Body).Decode(&b)
		return &b, res.StatusCode
	}
	basic := func(r *http.Request) { r.SetBasicAuth("admin", token.Token) }

	// Anonymous clients are asked for credentials.
	if _, status := batch("download", func(*http.Request) {}); status != http.StatusUnauthorized {
		t.Fatalf("anonymous batch: %d", status)
	}
	// Unknown objects cannot be downloaded.
	b, status := batch("download", basic)
	if status != http.StatusOK || len(b.Objects) != 1 || b.Objects[0].Error == nil || b.Objects[0].Error.Code != 404 {
		t.Fatalf("download of a missing object: %d %+v", status, b)
	}
	// Upload: a wrong content is refused, the right one stored.
	b, _ = batch("upload", basic)
	up, ok := b.Objects[0].Actions["upload"]
	if !ok {
		t.Fatalf("no upload action: %+v", b)
	}
	put := func(data []byte) int {
		req, _ := http.NewRequest(http.MethodPut, up.Href, bytes.NewReader(data))
		for k, v := range up.Header {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if status := put([]byte("something else")); status != http.StatusUnprocessableEntity {
		t.Errorf("upload of a wrong content: %d", status)
	}
	if status := put(content); status != http.StatusOK {
		t.Fatalf("upload: %d", status)
	}
	verify := b.Objects[0].Actions["verify"]
	vreq, _ := http.NewRequest(http.MethodPost, verify.Href, strings.NewReader(`{"oid":"`+oid+`","size":19}`))
	for k, v := range verify.Header {
		vreq.Header.Set(k, v)
	}
	if res, err := http.DefaultClient.Do(vreq); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("verify: %v %v", err, res)
	}
	b, _ = batch("upload", basic)
	if len(b.Objects[0].Actions) != 0 {
		t.Errorf("an uploaded object is uploaded again: %+v", b)
	}

	// Download with a token from git-lfs-authenticate over SSH.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	admin.MustDo(http.MethodPost, "/api/v1/me/ssh-keys", map[string]any{"key": string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}, nil)
	srv, err := env.App.Code.NewSSHServer()
	if err != nil {
		t.Fatal(err)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, l) }()
	client, err := ssh.Dial("tcp", l.Addr().String(), &ssh.ClientConfig{User: "git", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, _ := client.NewSession()
	out, err := session.Output("git-lfs-authenticate 'art/assets.git' download")
	if err != nil {
		t.Fatalf("git-lfs-authenticate: %v %s", err, out)
	}
	var auth struct {
		Href   string            `json:"href"`
		Header map[string]string `json:"header"`
	}
	if err := json.Unmarshal(out, &auth); err != nil || !strings.HasSuffix(auth.Href, "/scm/art/assets.git/info/lfs") {
		t.Fatalf("authenticate answer: %s", out)
	}
	b, _ = batch("download", func(r *http.Request) { r.Header.Set("Authorization", auth.Header["Authorization"]) })
	down, ok := b.Objects[0].Actions["download"]
	if !ok {
		t.Fatalf("no download action: %+v", b)
	}
	req, _ := http.NewRequest(http.MethodGet, down.Href, nil)
	for k, v := range down.Header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Equal(got, content) {
		t.Errorf("downloaded %q", got)
	}
	// A download token does not allow uploads.
	if _, status := batch("upload", func(r *http.Request) { r.Header.Set("Authorization", auth.Header["Authorization"]) }); status != http.StatusForbidden {
		t.Errorf("upload with a download token: %d", status)
	}
	// Other repositories cannot read the object.
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/ART/repos", map[string]any{"name": "other"}, nil)
	other := strings.Replace(lfsURL, "/assets.git/", "/other.git/", 1)
	body, _ := json.Marshal(map[string]any{"operation": "download", "objects": []map[string]any{{"oid": oid, "size": len(content)}}})
	oreq, _ := http.NewRequest(http.MethodPost, other, bytes.NewReader(body))
	oreq.SetBasicAuth("admin", token.Token)
	ores, err := http.DefaultClient.Do(oreq)
	if err != nil {
		t.Fatal(err)
	}
	var ob lfsBatch
	_ = json.NewDecoder(ores.Body).Decode(&ob)
	ores.Body.Close()
	if len(ob.Objects) != 1 || ob.Objects[0].Error == nil {
		t.Errorf("another repository can read the object: %+v", ob)
	}
}

// TestLFSTokensEndWithAccess: LFS tokens are not renewed by using them, and they stop working when the access they
// were given for ends — a user's permissions, a deploy key (audit H8). Uploads with a wrong content leave nothing
// behind, and objects are limited in size (audit M17).
func TestLFSTokensEndWithAccess(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	bob := env.CreateUser("bob")
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	bob.Get("/api/v1/me", &me)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "LFR", "name": "LFS"}, nil)
	var repo struct {
		CloneHTTP string `json:"cloneHttp"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/LFR/repos", map[string]any{"name": "assets", "readme": true}, &repo)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/LFR/assets/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": me.User.ID}, "permission": "repo.read"}}, nil)
	token := func(c *apitest.Client, scopes ...string) string {
		var tok struct {
			Token string `json:"token"`
		}
		c.MustDo(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "lfs", "scopes": scopes}, &tok)
		return tok.Token
	}
	adminToken, bobToken := token(admin, "read", "write"), token(bob, "read")
	batchURL := strings.TrimSuffix(repo.CloneHTTP, ".git") + ".git/info/lfs/objects/batch"
	batch := func(op, oid string, size int, auth func(*http.Request)) (*lfsBatch, int) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"operation": op, "objects": []map[string]any{{"oid": oid, "size": size}}})
		req, _ := http.NewRequest(http.MethodPost, batchURL, bytes.NewReader(body))
		req.Header.Set("Accept", "application/vnd.git-lfs+json")
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		auth(req)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var b lfsBatch
		_ = json.NewDecoder(res.Body).Decode(&b)
		return &b, res.StatusCode
	}
	send := func(method string, action struct {
		Href   string            `json:"href"`
		Header map[string]string `json:"header"`
	}, body []byte) int {
		t.Helper()
		req, _ := http.NewRequest(method, action.Href, bytes.NewReader(body))
		for k, v := range action.Header {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	basic := func(user, secret string) func(*http.Request) {
		return func(r *http.Request) { r.SetBasicAuth(user, secret) }
	}
	bearer := func(h map[string]string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", h["Authorization"]) }
	}

	content := []byte("a texture")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	b, _ := batch("upload", oid, len(content), basic("admin", adminToken))
	up := b.Objects[0].Actions["upload"]
	// A wrong content leaves nothing behind.
	wrong := []byte("something else entirely")
	wrongSum := sha256.Sum256(wrong)
	if status := send(http.MethodPut, up, wrong); status != http.StatusUnprocessableEntity {
		t.Fatalf("upload of a wrong content: %d", status)
	}
	var kept int
	if err := env.App.Pool.QueryRow(context.Background(), "SELECT count(*) FROM blobs WHERE sha256 = $1", hex.EncodeToString(wrongSum[:])).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 0 {
		t.Fatal("the wrong content was stored")
	}
	if status := send(http.MethodPut, up, content); status != http.StatusOK {
		t.Fatalf("upload: %d", status)
	}

	// Bob's LFS token: using it gives the same token back, not a fresh one.
	b, _ = batch("download", oid, len(content), basic("bob", bobToken))
	first := b.Objects[0].Actions["download"]
	if first.Header["Authorization"] == "" {
		t.Fatalf("no download action for Bob: %+v", b)
	}
	b, _ = batch("download", oid, len(content), bearer(first.Header))
	if again := b.Objects[0].Actions["download"]; again.Header["Authorization"] != first.Header["Authorization"] {
		t.Fatal("using an LFS token renewed it")
	}
	// Bob loses his access: the token stops working at once.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/LFR/assets/permissions", []map[string]any{}, nil)
	if status := send(http.MethodGet, first, nil); status != http.StatusUnauthorized {
		t.Fatalf("download with the token of a user without access: %d", status)
	}
	if _, status := batch("download", oid, len(content), bearer(first.Header)); status != http.StatusUnauthorized {
		t.Fatalf("batch with the token of a user without access: %d", status)
	}

	// A deploy key's token ends with the deploy key.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	var key struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/LFR/assets/deploy-keys", map[string]any{"name": "mirror",
		"key": string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}, &key)
	srv, err := env.App.Code.NewSSHServer()
	if err != nil {
		t.Fatal(err)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, l) }()
	client, err := ssh.Dial("tcp", l.Addr().String(), &ssh.ClientConfig{User: "git", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, _ := client.NewSession()
	out, err := session.Output("git-lfs-authenticate 'lfr/assets.git' download")
	if err != nil {
		t.Fatalf("git-lfs-authenticate: %v %s", err, out)
	}
	var auth struct {
		Header map[string]string `json:"header"`
	}
	_ = json.Unmarshal(out, &auth)
	if _, status := batch("download", oid, len(content), bearer(auth.Header)); status != http.StatusOK {
		t.Fatalf("batch with the deploy key's token: %d", status)
	}
	admin.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/code/repos/LFR/assets/deploy-keys/%d", key.ID), nil, nil)
	if _, status := batch("download", oid, len(content), bearer(auth.Header)); status != http.StatusUnauthorized {
		t.Fatalf("batch with the token of a deleted deploy key: %d", status)
	}

	// Objects larger than the installation allows are refused before and during the upload.
	var st map[string]any
	admin.Get("/api/v1/admin/settings", &st)
	st["maxLfsObjectMb"] = 1
	admin.MustDo(http.MethodPut, "/api/v1/admin/settings", st, nil)
	big := bytes.Repeat([]byte("x"), 2<<20)
	bigSum := sha256.Sum256(big)
	bigOID := hex.EncodeToString(bigSum[:])
	b, _ = batch("upload", bigOID, len(big), basic("admin", adminToken))
	if b.Objects[0].Error == nil || b.Objects[0].Error.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("batch of a too large object: %+v", b.Objects[0])
	}
	if status := send(http.MethodPut, struct {
		Href   string            `json:"href"`
		Header map[string]string `json:"header"`
	}{Href: strings.TrimSuffix(batchURL, "batch") + bigOID, Header: up.Header}, big); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("upload of a too large object: %d", status)
	}
}
