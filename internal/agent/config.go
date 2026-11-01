// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package agent runs pipeline jobs for Kogge. An agent registers once with a
// registration token, then long-polls the server over HTTPS for jobs and runs them with the shell executor (on the
// build host) or the container executor (Docker or Podman).
package agent

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"kogge.de/kogge/internal/builds/spec"
)

// Config is the agent's configuration file (JSON; it holds the agent secret, so it is readable by its owner only).
type Config struct {
	Server   string   `json:"server"`             // URL of Kogge
	Name     string   `json:"name"`               // shown in the administration
	Secret   string   `json:"secret"`             // received at registration
	Labels   []string `json:"labels,omitempty"`   // custom labels; os, architecture and executors are added
	WorkDir  string   `json:"workDir"`            // workspaces of running jobs
	Parallel int      `json:"parallel,omitempty"` // jobs at once (default 1)
	Shell    bool     `json:"shell"`              // shell executor enabled
	// Container names the CLI of the container executor ("docker" or "podman"); empty disables it.
	Container string `json:"container,omitempty"`
	// MountContainerSocket gives job containers the host's Docker socket. They then control the host: enable it only
	// for trusted repositories.
	MountContainerSocket bool   `json:"mountContainerSocket,omitempty"`
	CACertFile           string `json:"caCertFile,omitempty"` // PEM file with the CA of the server's certificate
	KeepWorkspaces       bool   `json:"keepWorkspaces,omitempty"`
	// Resources protect the build host: what containers of jobs get (container executor).
	Resources *Resources `json:"resources,omitempty"`
}

// Resources are what the agent gives each container of a job (the job's own and its services): defaults for
// containers that ask for nothing, the most one container may have — the agent takes no job that asks for more —
// and a limit of the processes in one container. Empty values set nothing.
type Resources struct {
	CPUs      float64 `json:"cpus,omitempty"`      // CPUs of a container that asks for none
	Memory    string  `json:"memory,omitempty"`    // memory of a container that asks for none, such as 4g
	MaxCPUs   float64 `json:"maxCpus,omitempty"`   // the most CPUs one container may have
	MaxMemory string  `json:"maxMemory,omitempty"` // the most memory one container may have
	Pids      int     `json:"pids,omitempty"`      // processes in one container (--pids-limit)
}

// limits are the parsed resources of the configuration.
type limits struct {
	defaults, max spec.Resources
	pids          int
}

func (r *Resources) parse() (limits, error) {
	var l limits
	if r == nil {
		return l, nil
	}
	cpus := func(v float64, what string) (int64, error) {
		if v == 0 {
			return 0, nil
		}
		c, err := spec.ParseCPUs(strconv.FormatFloat(v, 'f', -1, 64))
		if err != nil {
			return 0, fmt.Errorf("resources.%s: %w", what, err)
		}
		return c, nil
	}
	memory := func(v, what string) (int64, error) {
		if v == "" {
			return 0, nil
		}
		m, err := spec.ParseMemory(v)
		if err != nil {
			return 0, fmt.Errorf("resources.%s: %w", what, err)
		}
		return m, nil
	}
	var err error
	if l.defaults.MilliCPUs, err = cpus(r.CPUs, "cpus"); err != nil {
		return l, err
	}
	if l.max.MilliCPUs, err = cpus(r.MaxCPUs, "maxCpus"); err != nil {
		return l, err
	}
	if l.defaults.Memory, err = memory(r.Memory, "memory"); err != nil {
		return l, err
	}
	if l.max.Memory, err = memory(r.MaxMemory, "maxMemory"); err != nil {
		return l, err
	}
	switch {
	case l.max.MilliCPUs > 0 && l.defaults.MilliCPUs > l.max.MilliCPUs:
		return l, errors.New("resources.cpus may not be more than resources.maxCpus")
	case l.max.Memory > 0 && l.defaults.Memory > l.max.Memory:
		return l, errors.New("resources.memory may not be more than resources.maxMemory")
	case r.Pids < 0 || r.Pids > 0 && r.Pids < 16:
		return l, errors.New("resources.pids must be at least 16")
	}
	l.pids = r.Pids
	return l, nil
}

// LoadConfig reads a configuration file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, c.Validate()
}

// Save writes the configuration file, readable by its owner only.
func (c *Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	u, err := url.Parse(c.Server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("server must be the https URL of Kogge")
	}
	// The agent's secret and job tokens must not cross the network in clear text; plain http is for Kogge on the
	// same host.
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return errors.New("server must be an https URL (http only for localhost)")
	}
	if c.WorkDir == "" {
		return errors.New("workDir is missing")
	}
	if !c.Shell && c.Container == "" {
		return errors.New("enable the shell executor or name a container CLI (docker, podman)")
	}
	if c.Container != "" && c.Container != "docker" && c.Container != "podman" {
		return errors.New("container must be docker or podman")
	}
	if c.Parallel < 0 || c.Parallel > 64 {
		return errors.New("parallel must be between 1 and 64")
	}
	if _, err := c.Resources.parse(); err != nil {
		return err
	}
	return nil
}

// AllLabels returns the labels the agent announces besides its os and architecture.
func (c *Config) AllLabels() []string {
	labels := []string{}
	for _, l := range c.Labels {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			labels = append(labels, l)
		}
	}
	if c.Shell {
		labels = append(labels, "shell")
	}
	if c.Container != "" {
		labels = append(labels, "docker") // the label of container jobs, whatever the CLI
		if c.Container == "podman" {
			labels = append(labels, "podman")
		}
	}
	return labels
}

// OS returns the operating system label.
func OS() string { return runtime.GOOS }

// Arch returns the architecture label.
func Arch() string { return runtime.GOARCH }

// TLSConfig returns the TLS configuration for a private CA, or nil.
func (c *Config) TLSConfig() (*tls.Config, error) {
	if c.CACertFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(c.CACertFile)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificate", c.CACertFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
