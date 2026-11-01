// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Command kogge-agent runs pipeline jobs for Kogge on a build host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kogge.de/kogge/internal/agent"
	"kogge.de/kogge/internal/version"
)

const usage = `kogge-agent — runs pipeline jobs for Kogge.

Usage:
  kogge-agent register [flags]   register with a registration token and write the configuration
  kogge-agent run [-config FILE] claim and run jobs (default command)
  kogge-agent version            print the version, the commit and the build date

The configuration file defaults to $KOGGE_AGENT_CONFIG, else %s.
Agents connect to Kogge over HTTPS only; build hosts need no inbound ports. Jobs run repository code: run the agent
as a dedicated user on dedicated build hosts.
`

func defaultConfig() string {
	if p := os.Getenv("KOGGE_AGENT_CONFIG"); p != "" {
		return p
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "kogge-agent", "config.json")
	}
	return "kogge-agent.json"
}

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "register":
		err = register(args)
	case "run":
		err = run(args)
	case "version", "--version":
		fmt.Print(version.Describe("kogge-agent"))
	case "help", "--help", "-h":
		fmt.Printf(usage, defaultConfig())
	default:
		fmt.Fprintf(os.Stderr, usage, defaultConfig())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kogge-agent:", err)
		os.Exit(1)
	}
}

func register(args []string) error {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	config := fs.String("config", defaultConfig(), "configuration file to write")
	server := fs.String("server", "", "URL of Kogge, e.g. https://kogge.example.com (required)")
	token := fs.String("token", os.Getenv("KOGGE_AGENT_TOKEN"), "registration token (or $KOGGE_AGENT_TOKEN)")
	host, _ := os.Hostname()
	name := fs.String("name", host, "name shown in the administration")
	labels := fs.String("labels", "", "custom labels, comma-separated")
	workDir := fs.String("work-dir", "", "directory for workspaces (default: next to the configuration)")
	shell := fs.Bool("shell", false, "enable the shell executor (steps run directly on this host)")
	container := fs.String("container", "", "enable the container executor with this CLI: docker or podman")
	parallel := fs.Int("parallel", 1, "jobs at once")
	caCert := fs.String("ca-cert", "", "PEM file with the CA of the server's certificate")
	cpus := fs.Float64("cpus", 0, "CPUs of a job's container that asks for none (container executor)")
	memory := fs.String("memory", "", "memory of a job's container that asks for none, such as 4g")
	maxCPUs := fs.Float64("max-cpus", 0, "the most CPUs one container may have; jobs that ask for more go to other agents")
	maxMemory := fs.String("max-memory", "", "the most memory one container may have, such as 16g")
	pids := fs.Int("pids-limit", 0, "the most processes in one container")
	_ = fs.Parse(args)
	if *server == "" || *token == "" {
		return errors.New("-server and -token are required")
	}
	if *workDir == "" {
		*workDir = filepath.Join(filepath.Dir(*config), "work")
	}
	cfg := &agent.Config{Server: *server, Name: *name, WorkDir: *workDir, Parallel: *parallel, Shell: *shell, Container: *container,
		CACertFile: *caCert}
	if r := (agent.Resources{CPUs: *cpus, Memory: *memory, MaxCPUs: *maxCPUs, MaxMemory: *maxMemory, Pids: *pids}); r != (agent.Resources{}) {
		cfg.Resources = &r
	}
	for _, l := range strings.Split(*labels, ",") {
		if l = strings.TrimSpace(l); l != "" {
			cfg.Labels = append(cfg.Labels, l)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := agent.Register(ctx, cfg, *token, version.Version); err != nil {
		return err
	}
	if err := cfg.Save(*config); err != nil {
		return err
	}
	fmt.Printf("Registered %q with %s; configuration written to %s.\nStart the agent with: kogge-agent run -config %s\n",
		cfg.Name, cfg.Server, *config, *config)
	return nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	config := fs.String("config", defaultConfig(), "configuration file")
	jsonLog := fs.Bool("json", false, "log in JSON")
	_ = fs.Parse(args)
	var handler slog.Handler = slog.NewTextHandler(os.Stderr, nil)
	if *jsonLog {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	logger := slog.New(handler)
	cfg, err := agent.LoadConfig(*config)
	if err != nil {
		return err
	}
	a, err := agent.New(cfg, version.Version, logger)
	if err != nil {
		return err
	}
	// The first signal stops taking jobs and lets running ones finish; the second one cancels them.
	stop, stopCancel := context.WithCancel(context.Background())
	kill, killCancel := context.WithCancel(context.Background())
	defer stopCancel()
	defer killCancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		logger.Info("stopping after the running jobs; signal again to cancel them")
		stopCancel()
		<-signals
		logger.Info("cancelling the running jobs")
		killCancel()
	}()
	return a.Run(stop, kill)
}
