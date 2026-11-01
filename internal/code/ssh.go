// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/files"
	"kogge.de/kogge/internal/platform/metrics"
	"kogge.de/kogge/internal/platform/settings"
)

// The built-in SSH server accepts only Git commands, with users' keys and deploy keys.

// DeployKey is an SSH key with access to one repository.
type DeployKey struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Fingerprint string     `json:"fingerprint"`
	Write       bool       `json:"write" doc:"The key may push"`
	Created     time.Time  `json:"created"`
	LastUsed    *time.Time `json:"lastUsed,omitempty"`
}

// DeployKeys lists the deploy keys of a repository (repository administrators).
func (s *Service) DeployKeys(ctx context.Context, p *identity.Principal, key, slug string) ([]DeployKey, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	if a.Level(r.ProjectID, r.ID) < LevelAdmin {
		return nil, apperr.Forbidden("repository.admin-required", "Only administrators of the repository can see its deploy keys.")
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name, public_key, fingerprint, write, created_at, last_used_at FROM ssh_keys
		WHERE kind = 'deploy' AND repository_id = $1 ORDER BY created_at`, r.ID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DeployKey, error) {
		var k DeployKey
		var pub string
		err := row.Scan(&k.ID, &k.Name, &pub, &k.Fingerprint, &k.Write, &k.Created, &k.LastUsed)
		if f := strings.Fields(pub); len(f) > 0 {
			k.Type = f[0]
		}
		return k, err
	})
	return nonNil(list), err
}

// AddDeployKey adds a deploy key to a repository.
func (s *Service) AddDeployKey(ctx context.Context, p *identity.Principal, key, slug, name, text string, write bool) (*DeployKey, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, err
	}
	if a.Level(r.ProjectID, r.ID) < LevelAdmin {
		return nil, apperr.Forbidden("repository.admin-required", "Only administrators of the repository can add deploy keys.")
	}
	k, err := identity.ParseSSHKey(text)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = cmpOr(k.Comment, k.Key.Type())
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO ssh_keys (kind, repository_id, name, public_key, fingerprint, write, created_by)
			VALUES ('deploy', $1, $2, $3, $4, $5, $6) RETURNING id`, r.ID, name, k.Normalized, k.Fingerprint, write, p.UserID()).Scan(&id)
		if db.IsUniqueViolation(err, "ssh_keys_fingerprint_key") {
			return apperr.Conflict("ssh-key.taken", "This key is already in use.")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "repository.deploy-key.add", TargetType: "repository",
			TargetID: r.ProjectKey + "/" + r.Slug, Details: map[string]any{"fingerprint": k.Fingerprint, "write": write}})
	})
	if err != nil {
		return nil, err
	}
	return &DeployKey{ID: id, Name: name, Type: k.Key.Type(), Fingerprint: k.Fingerprint, Write: write, Created: now()}, nil
}

// DeleteDeployKey removes a deploy key.
func (s *Service) DeleteDeployKey(ctx context.Context, p *identity.Principal, key, slug string, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return err
	}
	if a.Level(r.ProjectID, r.ID) < LevelAdmin {
		return apperr.Forbidden("repository.admin-required", "Only administrators of the repository can remove deploy keys.")
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM ssh_keys WHERE id = $1 AND kind = 'deploy' AND repository_id = $2", id, r.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("ssh-key.not-found", "The key does not exist.")
	}
	// A revocation takes effect whether or not its record can be written (audit.Report).
	audit.Report(ctx, s.pool, s.log, audit.Entry{ActorID: p.UserID(), Action: "repository.deploy-key.delete", TargetType: "repository",
		TargetID: r.ProjectKey + "/" + r.Slug})
	return nil
}

// SSHServer serves Git over SSH.
type SSHServer struct {
	s      *Service
	config *ssh.ServerConfig
	conns  sync.WaitGroup
}

// sshKey is the owner of an SSH key.
type sshKey struct {
	id     int64
	userID int64
	repoID int64
	write  bool
}

// NewSSHServer loads (or creates at first start) the host keys in the data directory.
func (s *Service) NewSSHServer() (*SSHServer, error) {
	srv := &SSHServer{s: s}
	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-Kogge",
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			k, err := s.lookupSSHKey(context.Background(), ssh.FingerprintSHA256(key))
			if err != nil {
				return nil, fmt.Errorf("unknown key")
			}
			return &ssh.Permissions{Extensions: map[string]string{
				"key": strconv.FormatInt(k.id, 10), "user": strconv.FormatInt(k.userID, 10), "repo": strconv.FormatInt(k.repoID, 10),
				"write": strconv.FormatBool(k.write)}}, nil
		},
	}
	dir := filepath.Join(s.opt.DataDir, "ssh")
	for _, kind := range []string{"ed25519", "rsa"} {
		signer, err := loadHostKey(dir, kind)
		if err != nil {
			return nil, fmt.Errorf("ssh host key: %w", err)
		}
		cfg.AddHostKey(signer)
	}
	srv.config = cfg
	return srv, nil
}

func loadHostKey(dir, kind string) (ssh.Signer, error) {
	file := filepath.Join(dir, "host_"+kind+"_key")
	if b, err := os.ReadFile(file); err == nil {
		return ssh.ParsePrivateKey(b)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var block *pem.Block
	switch kind {
	case "ed25519":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		b, err := ssh.MarshalPrivateKey(priv, "kogge host key")
		if err != nil {
			return nil, err
		}
		block = b
	default:
		priv, err := rsa.GenerateKey(rand.Reader, 3072)
		if err != nil {
			return nil, err
		}
		block = &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}
	}
	// Nodes that start for the first time together on one data volume agree on one key, so that clients see the
	// same host key whichever node they reach.
	data, _, err := files.WriteOnce(file, pem.EncodeToMemory(block), 0o600)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

func (s *Service) lookupSSHKey(ctx context.Context, fingerprint string) (*sshKey, error) {
	var k sshKey
	var userID, repoID *int64
	err := s.pool.QueryRow(ctx, "SELECT id, user_id, repository_id, write FROM ssh_keys WHERE fingerprint = $1", fingerprint).
		Scan(&k.id, &userID, &repoID, &k.write)
	if err != nil {
		return nil, err
	}
	if userID != nil {
		k.userID = *userID
	}
	if repoID != nil {
		k.repoID = *repoID
	}
	_, _ = s.pool.Exec(ctx, "UPDATE ssh_keys SET last_used_at = now() WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')", k.id)
	return &k, nil
}

// Serve accepts connections until the context ends.
func (srv *SSHServer) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				srv.conns.Wait()
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		srv.conns.Add(1)
		go func() {
			defer srv.conns.Done()
			srv.handle(ctx, conn)
		}()
	}
}

func (srv *SSHServer) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	sc, chans, reqs, err := ssh.NewServerConn(conn, srv.config)
	if err != nil {
		return
	}
	defer sc.Close()
	_ = conn.SetDeadline(time.Time{})
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sessions are supported")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go srv.session(ctx, sc.Permissions, ch, requests)
	}
}

func (srv *SSHServer) session(ctx context.Context, perms *ssh.Permissions, ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	protocol := ""
	for req := range requests {
		switch req.Type {
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GIT_PROTOCOL" && gitProtocolPattern.MatchString(kv.Value) {
				protocol = kv.Value
				_ = req.Reply(true, nil)
				continue
			}
			_ = req.Reply(false, nil)
		case "exec":
			var cmd struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &cmd); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			code := srv.exec(ctx, perms, ch, cmd.Command, protocol)
			status := make([]byte, 4)
			binary.BigEndian.PutUint32(status, uint32(code))
			_, _ = ch.SendRequest("exit-status", false, status)
			return
		case "shell", "pty-req":
			_ = req.Reply(req.Type == "pty-req", nil)
			if req.Type == "shell" {
				fmt.Fprint(ch.Stderr(), "Hi! Kogge does not provide shell access; use git to clone and push.\r\n")
				_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 1})
				return
			}
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// parseGitCommand reads "git-upload-pack '/KEY/repo.git'" (also with a space after git and without quotes).
func parseGitCommand(command string) (service, key, slug string, ok bool) {
	command = strings.TrimSpace(command)
	for _, svc := range []string{"git-upload-pack", "git-receive-pack", "git-upload-archive", "git upload-pack", "git receive-pack", "git upload-archive"} {
		rest, found := strings.CutPrefix(command, svc+" ")
		if !found {
			continue
		}
		rest = strings.TrimSpace(rest)
		if len(rest) >= 2 && (rest[0] == '\'' && rest[len(rest)-1] == '\'' || rest[0] == '"' && rest[len(rest)-1] == '"') {
			rest = rest[1 : len(rest)-1]
		}
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, "/"), "scm/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(rest, "'\"\\ ") {
			return "", "", "", false
		}
		return strings.TrimPrefix(strings.ReplaceAll(svc, " ", "-"), "git-"), parts[0], strings.TrimSuffix(parts[1], ".git"), true
	}
	return "", "", "", false
}

// parseLFSCommand reads "git-lfs-authenticate '/KEY/repo.git' download".
func parseLFSCommand(command string) (key, slug string, write, ok bool) {
	f := strings.Fields(command)
	if len(f) < 3 || f[0] != "git-lfs-authenticate" || (f[2] != "download" && f[2] != "upload") {
		return "", "", false, false
	}
	path := strings.Trim(f[1], `'"`)
	path = strings.TrimPrefix(strings.TrimPrefix(path, "/"), "scm/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false, false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), f[2] == "upload", true
}

func (srv *SSHServer) exec(ctx context.Context, perms *ssh.Permissions, ch ssh.Channel, command, protocol string) int {
	s := srv.s
	if strings.HasPrefix(strings.TrimSpace(command), "git-lfs-authenticate ") {
		return srv.lfsAuthenticate(ctx, perms, ch, command)
	}
	service, key, slug, ok := parseGitCommand(command)
	if !ok {
		fmt.Fprintln(ch.Stderr(), "Kogge only accepts git-upload-pack, git-receive-pack and git-upload-archive.")
		return 1
	}
	userID, _ := strconv.ParseInt(perms.Extensions["user"], 10, 64)
	keyRepo, _ := strconv.ParseInt(perms.Extensions["repo"], 10, 64)
	keyID, _ := strconv.ParseInt(perms.Extensions["key"], 10, 64)
	var principal *identity.Principal
	var a *Access
	var err error
	if userID != 0 {
		if principal, err = s.identity.PrincipalOf(ctx, userID, identity.MethodSSH); err != nil {
			fmt.Fprintln(ch.Stderr(), "Your account is not active.")
			return 1
		}
		if a, err = s.Access(ctx, principal); err != nil {
			fmt.Fprintln(ch.Stderr(), "Kogge is not available; try again later.")
			return 1
		}
	} else {
		a = deployAccess(keyRepo, perms.Extensions["write"] == "true")
	}
	level := LevelRead
	if service == "receive-pack" {
		level = LevelWrite
	}
	r, err := s.repo(ctx, a, key, slug, level)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			fmt.Fprintln(ch.Stderr(), ae.Detail)
		} else {
			fmt.Fprintln(ch.Stderr(), "Kogge is not available; try again later.")
		}
		return 1
	}
	env := []string{"GIT_PROTOCOL=" + protocol}
	args := []string{service, s.repoPath(r.ID)}
	if service == "receive-pack" {
		st, err := s.settings.Get(ctx)
		if err != nil {
			fmt.Fprintln(ch.Stderr(), "Kogge is not available; try again later.")
			return 1
		}
		refusal, err := s.pushRefusal(ctx, r)
		if err != nil {
			fmt.Fprintln(ch.Stderr(), "Kogge is not available; try again later.")
			return 1
		}
		if refusal != "" {
			// In place of the advertisement of refs: git shows it as "remote error: …".
			_, _ = io.WriteString(ch, pktLine("ERR "+refusal+"\n"))
			return 1
		}
		args = receivePackArgs(st, s.repoPath(r.ID))
		pc := &pushContext{repoID: r.ID, principal: principal, protocol: "ssh", maxFile: settings.MiB(st.MaxGitFileMB)}
		if userID == 0 {
			pc.deployKeyID = keyID
		}
		token, hookEnv, err := s.hooks.register(pc)
		if err != nil {
			fmt.Fprintln(ch.Stderr(), "Kogge is not available; try again later.")
			return 1
		}
		defer s.hooks.done(token)
		env = append(env, hookEnv...)
	}
	cmd := s.git.Command(ctx, "", env, args...)
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	start := time.Now()
	err = cmd.Run()
	metrics.Git(service, "ssh", start, err)
	if err != nil {
		var ee interface{ ExitCode() int }
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}

// lfsAuthenticate gives SSH clients the address and a token for the LFS API.
func (srv *SSHServer) lfsAuthenticate(ctx context.Context, perms *ssh.Permissions, ch ssh.Channel, command string) int {
	s := srv.s
	key, slug, write, ok := parseLFSCommand(command)
	if !ok {
		fmt.Fprintln(ch.Stderr(), "usage: git-lfs-authenticate <repository> download|upload")
		return 1
	}
	userID, _ := strconv.ParseInt(perms.Extensions["user"], 10, 64)
	keyRepo, _ := strconv.ParseInt(perms.Extensions["repo"], 10, 64)
	keyID, _ := strconv.ParseInt(perms.Extensions["key"], 10, 64)
	var a *Access
	if userID != 0 {
		p, err := s.identity.PrincipalOf(ctx, userID, identity.MethodSSH)
		if err != nil {
			fmt.Fprintln(ch.Stderr(), "Your account is not active.")
			return 1
		}
		if a, err = s.Access(ctx, p); err != nil {
			return 1
		}
	} else {
		a = deployAccess(keyRepo, perms.Extensions["write"] == "true")
	}
	level := LevelRead
	if write {
		level = LevelWrite
	}
	r, err := s.repo(ctx, a, key, slug, level)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			fmt.Fprintln(ch.Stderr(), ae.Detail)
		}
		return 1
	}
	deploy := int64(0)
	if userID == 0 {
		deploy = keyID
	}
	out, err := s.lfsAuthenticate(ctx, r, userID, deploy, write)
	if err != nil {
		fmt.Fprintln(ch.Stderr(), "Kogge is not available; try again later.")
		return 1
	}
	_, _ = ch.Write(append(out, '\n'))
	return 0
}
