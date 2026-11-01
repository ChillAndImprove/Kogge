// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
)

// Pushes run git receive-pack, which calls Kogge's hook scripts; the scripts run `kogge hook <name>`, which asks
// the server over a Unix socket. A one-time token, known only to the receive-pack process, ties the
// hook to the push and its user. Every node has a socket of its own, outside the data volume that the nodes share:
// the hooks of a push reach the node that runs it, whose socket is in their environment.

// RefUpdate is one ref update of a push.
type RefUpdate struct {
	Ref         string `json:"ref"`
	Old         string `json:"old"`
	New         string `json:"new"`
	FastForward bool   `json:"fastForward,omitempty"`
}

// Created reports whether the ref is new.
func (u RefUpdate) Created() bool { return isZero(u.Old) }

// Deleted reports whether the ref is deleted.
func (u RefUpdate) Deleted() bool { return isZero(u.New) }

func isZero(sha string) bool { return strings.Trim(sha, "0") == "" }

// pushContext is what the server knows about a running push.
type pushContext struct {
	repoID      int64
	principal   *identity.Principal // nil for deploy keys
	deployKeyID int64
	protocol    string
	maxFile     int64 // files without LFS larger than this are refused (0: any size); the hook looks for them
	created     time.Time
	release     func() // the Git write lock, held from the end of pre-receive to the end of the push
	incoming    int64  // the size of the objects the push brings, once pre-receive allowed it
}

type hookRequest struct {
	Token   string      `json:"token"`
	Updates []RefUpdate `json:"updates"`
	// Incoming is the size of the objects the push brings (pre-receive).
	Incoming int64 `json:"incoming,omitempty"`
	// Large are the files of the push larger than the limit the hook was given (pre-receive).
	Large []LargeFile `json:"large,omitempty"`
	// Problem says why the hook could not measure the push.
	Problem string `json:"problem,omitempty"`
}

type hookResponse struct {
	Allow    bool     `json:"allow"`
	Messages []string `json:"messages"`
}

type hookServer struct {
	s       *Service
	program string // the program the hook scripts run for this node's pushes
	once    sync.Once
	err     error
	dir     string // this node's own directory with the socket
	path    string
	srv     *http.Server

	mu     sync.Mutex
	pushes map[string]*pushContext
}

// errHooksStopped refuses the pushes that start after the server of the hooks stopped.
var errHooksStopped = errors.New("the server of the Git hooks has stopped")

func newHookServer(s *Service, program string) *hookServer {
	return &hookServer{s: s, program: program, pushes: map[string]*pushContext{}}
}

// start listens on the socket (on first use). The socket is in a new directory in the node's temporary directory
// (TMPDIR), never on the data volume: there, the socket of one node would take the place of another's, and a Unix
// socket on a network file system reaches only processes of the host that created it.
func (h *hookServer) start() error {
	h.once.Do(func() {
		dir, err := os.MkdirTemp("", "kogge-hooks-")
		if err == nil && len(filepath.Join(dir, "git-hooks.sock")) > 100 {
			// Unix socket paths are short (about 100 bytes): a long TMPDIR gives way to /tmp.
			_ = os.Remove(dir)
			dir, err = os.MkdirTemp("/tmp", "kogge-hooks-")
		}
		if err != nil {
			h.err = fmt.Errorf("a directory for the socket of the Git hooks: %w", err)
			return
		}
		h.dir, h.path = dir, filepath.Join(dir, "git-hooks.sock")
		l, err := net.Listen("unix", h.path)
		if err != nil {
			_ = os.RemoveAll(dir)
			h.err = err
			return
		}
		_ = os.Chmod(h.path, 0o600)
		mux := http.NewServeMux()
		mux.HandleFunc("POST /pre-receive", h.preReceive)
		mux.HandleFunc("POST /post-receive", h.postReceive)
		h.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := h.srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				h.s.log.Error("git hook server stopped", "error", err)
			}
		}()
	})
	return h.err
}

// close stops the server and removes the directory of its socket; pushes that start afterwards are refused.
func (h *hookServer) close() {
	// A server that has not started does not start any more; one that is starting has started when Do returns.
	h.once.Do(func() { h.err = errHooksStopped })
	if h.srv != nil {
		_ = h.srv.Close()
	}
	if h.dir != "" {
		_ = os.RemoveAll(h.dir)
	}
}

// register prepares a push and returns the environment for receive-pack.
func (h *hookServer) register(pc *pushContext) (string, []string, error) {
	if err := h.start(); err != nil {
		return "", nil, err
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(b)
	pc.created = time.Now()
	h.mu.Lock()
	for t, old := range h.pushes {
		if time.Since(old.created) > 6*time.Hour {
			delete(h.pushes, t)
		}
	}
	h.pushes[token] = pc
	h.mu.Unlock()
	env := []string{"KOGGE_HOOK_PROGRAM=" + h.program, "KOGGE_HOOK_SOCKET=" + h.path, "KOGGE_HOOK_TOKEN=" + token}
	if pc.maxFile > 0 {
		env = append(env, "KOGGE_HOOK_MAX_FILE="+strconv.FormatInt(pc.maxFile, 10))
	}
	return token, env, nil
}

func (h *hookServer) done(token string) {
	h.mu.Lock()
	pc := h.pushes[token]
	delete(h.pushes, token)
	h.mu.Unlock()
	if pc != nil {
		h.unlock(pc)
	}
}

// unlock releases the Git write lock of a push (git runs post-receive only if it updated a ref).
func (h *hookServer) unlock(pc *pushContext) {
	h.mu.Lock()
	release := pc.release
	pc.release = nil
	h.mu.Unlock()
	if release != nil {
		release()
	}
}

func (h *hookServer) context(token string) *pushContext {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pushes[token]
}

func (h *hookServer) read(w http.ResponseWriter, r *http.Request) (*pushContext, hookRequest, bool) {
	var req hookRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, req, false
	}
	pc := h.context(req.Token)
	if pc == nil {
		http.Error(w, "unknown push", http.StatusForbidden)
		return nil, req, false
	}
	return pc, req, true
}

func writeHookResponse(w http.ResponseWriter, res hookResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (h *hookServer) preReceive(w http.ResponseWriter, r *http.Request) {
	pc, req, ok := h.read(w, r)
	if !ok {
		return
	}
	problems, err := h.s.checkPush(r.Context(), pc, req.Updates)
	if err == nil && len(problems) == 0 {
		problems, err = h.s.checkSizes(r.Context(), pc, req)
	}
	if err != nil {
		h.s.log.Error("checking a push", "repository", pc.repoID, "error", err)
		writeHookResponse(w, hookResponse{Messages: []string{"Kogge could not check the push; try again later."}})
		return
	}
	if len(problems) > 0 {
		writeHookResponse(w, hookResponse{Messages: problems})
		return
	}
	// git moves the objects into the repository and updates the refs once this hook allows the push; a backup
	// copying the repositories must not see half of it.
	release, err := h.s.opt.Gate.Enter(r.Context(), pushWait)
	if err != nil {
		message := "Kogge could not accept the push; try again later."
		if errors.Is(err, db.ErrPaused) {
			message = "A backup is running and pushes are paused; try again in a few minutes."
		} else {
			h.s.log.Error("waiting for the Git write lock", "repository", pc.repoID, "error", err)
		}
		writeHookResponse(w, hookResponse{Messages: []string{message}})
		return
	}
	h.mu.Lock()
	pc.release = release
	h.mu.Unlock()
	writeHookResponse(w, hookResponse{Allow: true})
}

func (h *hookServer) postReceive(w http.ResponseWriter, r *http.Request) {
	pc, req, ok := h.read(w, r)
	if !ok {
		return
	}
	messages, err := h.s.recordPush(r.Context(), pc, req.Updates)
	h.unlock(pc)
	if err != nil {
		h.s.log.Error("recording a push", "repository", pc.repoID, "error", err)
		messages = []string{"Kogge could not record the push; it will be processed later."}
	}
	writeHookResponse(w, hookResponse{Allow: true, Messages: messages})
}

// RunHook is the hook program, `kogge hook pre-receive|post-receive`, run by git during pushes.
func RunHook(name string, stdin io.Reader, stdout, stderr io.Writer) int {
	socket, token := os.Getenv("KOGGE_HOOK_SOCKET"), os.Getenv("KOGGE_HOOK_TOKEN")
	if socket == "" || token == "" {
		fmt.Fprintln(stderr, "Kogge: pushes are only accepted through Kogge.")
		return 1
	}
	if name != "pre-receive" && name != "post-receive" {
		fmt.Fprintln(stderr, "Kogge: unknown hook "+name)
		return 1
	}
	var updates []RefUpdate
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		u := RefUpdate{Old: f[0], New: f[1], Ref: f[2]}
		if name == "pre-receive" && !u.Created() && !u.Deleted() {
			// Git runs the hook with the new objects in quarantine; this process can see them.
			cmd := exec.Command("git", "merge-base", "--is-ancestor", u.Old, u.New)
			u.FastForward = cmd.Run() == nil
		}
		updates = append(updates, u)
	}
	req := hookRequest{Token: token, Updates: updates}
	if quarantine := os.Getenv("GIT_QUARANTINE_PATH"); name == "pre-receive" && quarantine != "" {
		// What the push brings, for the limits: git has the objects in quarantine until this hook allows the push.
		req.Incoming = dirSize(quarantine)
		// Files count by their size, objects in quarantine by their compressed size: a small push may bring a large file.
		if limit, _ := strconv.ParseInt(os.Getenv("KOGGE_HOOK_MAX_FILE"), 10, 64); limit > 0 && req.Incoming > 0 {
			slow := time.AfterFunc(5*time.Second, func() { fmt.Fprintln(stderr, "Kogge: checking the sizes of the files ...") })
			large, err := largeFiles(context.Background(), updates, limit)
			slow.Stop()
			if err != nil {
				req.Problem = err.Error()
			}
			req.Large = large
		}
	}
	body, _ := json.Marshal(req)
	// Pushes wait while a backup copies the repositories (up to pushWait).
	client := &http.Client{Timeout: pushWait + 2*time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}}}
	waiting := time.AfterFunc(5*time.Second, func() {
		if name == "pre-receive" {
			fmt.Fprintln(stderr, "Kogge: waiting for the server (pushes wait while a backup runs) ...")
		}
	})
	resp, err := client.Post("http://kogge/"+name, "application/json", bytes.NewReader(body))
	waiting.Stop()
	if err != nil {
		fmt.Fprintln(stderr, "Kogge: the server cannot be reached:", err)
		return 1
	}
	defer resp.Body.Close()
	var res hookResponse
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&res) != nil {
		fmt.Fprintln(stderr, "Kogge: the push was not accepted.")
		return 1
	}
	for _, m := range res.Messages {
		fmt.Fprintln(stderr, m)
	}
	if !res.Allow {
		return 1
	}
	return 0
}

// HookMain runs the hook program when the process was started as a hook; tests call it from TestMain, since
// their binary takes the place of kogge.
func HookMain() {
	if len(os.Args) > 2 && os.Args[1] == "hook" {
		os.Exit(RunHook(os.Args[2], os.Stdin, os.Stdout, os.Stderr))
	}
}
