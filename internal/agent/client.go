// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"kogge.de/kogge/internal/builds/protocol"
)

// Client speaks the agent protocol with the server.
type Client struct {
	base   string
	http   *http.Client
	secret string
	ua     string
	// awaitingApproval: the server gives the agent no work until an administrator approves it.
	awaitingApproval atomic.Bool
}

// NewClient creates a client for the server at base (https://kogge.example.com).
func NewClient(base, secret, version string, tlsConfig *tls.Config) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig != nil {
		tr.TLSClientConfig = tlsConfig
	}
	tr.ResponseHeaderTimeout = protocol.PollTimeout + 30*time.Second
	return &Client{base: strings.TrimRight(base, "/") + protocol.BasePath, http: &http.Client{Transport: tr}, secret: secret,
		ua: "kogge-agent/" + version}
}

// StatusError is an unexpected answer of the server.
type StatusError struct {
	Code   int
	Detail string
}

func (e *StatusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("server answered %d: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("server answered %d", e.Code)
}

// permanent reports whether retrying cannot help: refusals, also for want of space on the server (a few seconds
// later, it has none either).
func permanent(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code >= 400 && se.Code < 500 && se.Code != http.StatusTooManyRequests ||
		se.Code == http.StatusInsufficientStorage)
}

func (c *Client) request(ctx context.Context, method, path, token string, body io.Reader, contentType string, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	if method == http.MethodPut && size > 0 {
		// Archives the server refuses (too large, no space) are refused before they are sent.
		req.Header.Set("Expect", "100-continue")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", c.ua)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// check turns error answers into a StatusError and closes their body.
func check(res *http.Response, ok ...int) error {
	for _, code := range ok {
		if res.StatusCode == code {
			return nil
		}
	}
	defer res.Body.Close()
	var problem struct {
		Detail string `json:"detail"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	_ = json.Unmarshal(b, &problem)
	return &StatusError{Code: res.StatusCode, Detail: problem.Detail}
}

func (c *Client) postJSON(ctx context.Context, path, token string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	res, err := c.request(ctx, http.MethodPost, path, token, bytes.NewReader(b), "application/json", int64(len(b)))
	if err != nil {
		return err
	}
	if err := check(res, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	defer res.Body.Close()
	if out != nil && res.StatusCode == http.StatusOK {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// Register registers the agent with a registration token.
func (c *Client) Register(ctx context.Context, req protocol.RegisterRequest) (*protocol.RegisterResponse, error) {
	var out protocol.RegisterResponse
	if err := c.postJSON(ctx, "/register", "", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AwaitingApproval reports whether the server said at the last claim that the agent waits for an administrator's
// approval.
func (c *Client) AwaitingApproval() bool { return c.awaitingApproval.Load() }

// Claim waits for a job; it returns nil when the server has none (after the long-poll timeout).
func (c *Client) Claim(ctx context.Context, hello protocol.ClaimRequest) (*protocol.JobPayload, error) {
	b, _ := json.Marshal(hello)
	res, err := c.request(ctx, http.MethodPost, "/jobs/claim", c.secret, bytes.NewReader(b), "application/json", int64(len(b)))
	if err != nil {
		return nil, err
	}
	if err := check(res, http.StatusOK, http.StatusNoContent); err != nil {
		return nil, err
	}
	defer res.Body.Close()
	c.awaitingApproval.Store(res.Header.Get("X-Kogge-Agent-State") == "awaiting-approval")
	if res.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	var p protocol.JobPayload
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Job returns a client for the requests of one job.
func (c *Client) Job(p *protocol.JobPayload) *JobClient {
	return &JobClient{c: c, id: p.ID, token: p.Token}
}

// JobClient makes the requests of one job with its token.
type JobClient struct {
	c     *Client
	id    int64
	token string
}

func (j *JobClient) path(rest string) string { return "/jobs/" + strconv.FormatInt(j.id, 10) + rest }

// Heartbeat renews the job's lease and tells whether the job was cancelled.
func (j *JobClient) Heartbeat(ctx context.Context) (bool, error) {
	var out protocol.HeartbeatResponse
	err := j.c.postJSON(ctx, j.path("/heartbeat"), j.token, struct{}{}, &out)
	return out.Cancel, err
}

// errOffset means the server holds a different amount of the log than the agent assumed.
var errOffset = errors.New("log offset mismatch")

// Log appends a chunk at offset; it returns the stored size, which is the next offset.
func (j *JobClient) Log(ctx context.Context, offset int64, data []byte) (int64, error) {
	res, err := j.c.request(ctx, http.MethodPost, j.path("/log?offset="+strconv.FormatInt(offset, 10)), j.token, bytes.NewReader(data),
		"text/plain; charset=utf-8", int64(len(data)))
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var out protocol.LogResponse
	switch res.StatusCode {
	case http.StatusOK:
		err := json.NewDecoder(res.Body).Decode(&out)
		return out.Size, err
	case http.StatusConflict:
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			return 0, err
		}
		return out.Size, errOffset
	}
	return 0, check(res)
}

// PutArtifact uploads a zip archive.
func (j *JobClient) PutArtifact(ctx context.Context, name string, r io.Reader, size int64) error {
	res, err := j.c.request(ctx, http.MethodPut, j.path("/artifacts/"+url.PathEscape(name)), j.token, r, "application/zip", size)
	if err != nil {
		return err
	}
	if err := check(res, http.StatusNoContent, http.StatusOK); err != nil {
		return err
	}
	return res.Body.Close()
}

// GetArtifact downloads the zip archive of an artifact of an earlier job.
func (j *JobClient) GetArtifact(ctx context.Context, id int64, w io.Writer) error {
	res, err := j.c.request(ctx, http.MethodGet, j.path("/artifacts/"+strconv.FormatInt(id, 10)), j.token, nil, "", -1)
	if err != nil {
		return err
	}
	if err := check(res, http.StatusOK); err != nil {
		return err
	}
	defer res.Body.Close()
	_, err = io.Copy(w, res.Body)
	return err
}

// GetCache downloads a cache archive; found is false when the repository has none for the key.
func (j *JobClient) GetCache(ctx context.Context, key string, w io.Writer) (bool, error) {
	res, err := j.c.request(ctx, http.MethodGet, j.path("/caches/"+url.PathEscape(key)), j.token, nil, "", -1)
	if err != nil {
		return false, err
	}
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return false, nil
	}
	if err := check(res, http.StatusOK); err != nil {
		return false, err
	}
	defer res.Body.Close()
	_, err = io.Copy(w, res.Body)
	return err == nil, err
}

// PutCache uploads a cache archive.
func (j *JobClient) PutCache(ctx context.Context, key string, r io.Reader, size int64) error {
	res, err := j.c.request(ctx, http.MethodPut, j.path("/caches/"+url.PathEscape(key)), j.token, r, "application/gzip", size)
	if err != nil {
		return err
	}
	if err := check(res, http.StatusNoContent, http.StatusOK); err != nil {
		return err
	}
	return res.Body.Close()
}

// PutReport uploads a JUnit XML report.
func (j *JobClient) PutReport(ctx context.Context, data []byte) error {
	res, err := j.c.request(ctx, http.MethodPost, j.path("/reports/junit"), j.token, bytes.NewReader(data), "application/xml", int64(len(data)))
	if err != nil {
		return err
	}
	if err := check(res, http.StatusNoContent, http.StatusOK); err != nil {
		return err
	}
	return res.Body.Close()
}

// Complete reports the result of the job.
func (j *JobClient) Complete(ctx context.Context, req protocol.CompleteRequest) error {
	return j.c.postJSON(ctx, j.path("/complete"), j.token, req, nil)
}

// retry runs f until it succeeds, fails permanently, or the attempts are used up.
func retry(ctx context.Context, attempts int, f func() error) error {
	wait := time.Second
	var err error
	for i := 0; i < attempts; i++ {
		if err = f(); err == nil || permanent(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
	return err
}
