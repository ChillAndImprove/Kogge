// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// handover measures how long a pending job waits for an idle agent: a real kogge-agent with the shell executor runs
// the pipeline "handover" of the pipeline repository, one run at a time.
type handover struct {
	b     *bench
	agent *exec.Cmd
	ready bool
}

type runView struct {
	State   string    `json:"state"`
	Created time.Time `json:"created"`
	Stages  []struct {
		Jobs []struct {
			State   string     `json:"state"`
			Started *time.Time `json:"started"`
		} `json:"jobs"`
	} `json:"stages"`
}

func (h *handover) prepare() error {
	if h.ready {
		return nil
	}
	m := h.b.m
	if m.PipelineRepository == "" || m.AgentToken == "" {
		return errors.New("the manifest has no pipeline repository; generate the data with this version of the tool")
	}
	if _, err := os.Stat(h.b.agent); err != nil {
		return fmt.Errorf("no build agent at %s (make build-server, or -agent)", h.b.agent)
	}
	dir := filepath.Join(h.b.work, "agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	config := filepath.Join(dir, "config.json")
	name := "scale-bench-" + strconv.FormatInt(time.Now().Unix(), 36)
	if out, err := exec.Command(h.b.agent, "register", "-config", config, "-server", h.b.base, "-token", m.AgentToken, "-name", name,
		"-labels", "scale-bench", "-shell").CombinedOutput(); err != nil {
		return fmt.Errorf("register the agent: %v: %s", err, out)
	}
	log, err := os.Create(filepath.Join(dir, "agent.log"))
	if err != nil {
		return err
	}
	h.agent = exec.Command(h.b.agent, "run", "-config", config)
	h.agent.Stdout, h.agent.Stderr = log, log
	if err := h.agent.Start(); err != nil {
		return err
	}
	// A first run shows that the agent is connected and takes jobs.
	if _, err := h.once(nil); err != nil {
		return fmt.Errorf("the agent took no job: %w", err)
	}
	h.ready = true
	return nil
}

// once starts a run, waits for its end and returns the time from the job becoming pending (the run's creation)
// to its start on the agent, both as the server recorded them.
func (h *handover) once(*rand.Rand) (time.Duration, error) {
	m := h.b.m
	repo := "/api/v1/code/repos/" + m.PipelineRepository
	// The agent is idle again: it finished the previous job and asks for the next one.
	time.Sleep(300 * time.Millisecond)
	var started []struct {
		Number int `json:"number"`
	}
	if err := h.b.call("POST", repo+"/runs", m.WriterToken, map[string]string{"pipeline": "handover"}, &started); err != nil {
		return 0, err
	}
	if len(started) != 1 {
		return 0, fmt.Errorf("%d runs started instead of one", len(started))
	}
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		var run runView
		if err := h.b.call("GET", repo+"/runs/"+strconv.Itoa(started[0].Number), m.WriterToken, nil, &run); err != nil {
			return 0, err
		}
		switch run.State {
		case "succeeded":
			if len(run.Stages) == 0 || len(run.Stages[0].Jobs) == 0 || run.Stages[0].Jobs[0].Started == nil {
				return 0, errors.New("the run has no started job")
			}
			return run.Stages[0].Jobs[0].Started.Sub(run.Created), nil
		case "failed", "cancelled", "error":
			return 0, fmt.Errorf("run %d ended %s", started[0].Number, run.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0, fmt.Errorf("run %d did not end within a minute", started[0].Number)
}

// stop ends the agent.
func (h *handover) stop() {
	if h.agent != nil && h.agent.Process != nil {
		_ = h.agent.Process.Signal(syscall.SIGINT)
		done := make(chan struct{})
		go func() { _ = h.agent.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = h.agent.Process.Kill()
		}
	}
}
