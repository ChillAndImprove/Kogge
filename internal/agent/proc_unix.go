// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
	"time"
)

// processTree is a command with the processes it starts, which cancelling the command stops together.
type processTree struct {
	cmd *exec.Cmd
}

// startTree starts a command in a process group of its own: cancelling its context stops the whole group, first
// politely, then for good.
func startTree(cmd *exec.Cmd) (*processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		go func() {
			time.Sleep(10 * time.Second)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}()
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processTree{cmd: cmd}, nil
}

// Wait waits for the command to end.
func (t *processTree) Wait() error { return t.cmd.Wait() }

// Warning tells why cancelling may not stop all processes of the tree ("" when it does).
func (t *processTree) Warning() string { return "" }

// Release is called when the job no longer needs what the command left running. Processes a step leaves behind
// in the background keep running on Unix: once their group is empty, its number may belong to another one.
func (t *processTree) Release() {}

// runTree runs a command as a process tree (see startTree).
func runTree(cmd *exec.Cmd) error {
	t, err := startTree(cmd)
	if err != nil {
		return err
	}
	defer t.Release()
	return t.Wait()
}

// ownerArgs returns the user and group the agent runs as ("uid:gid"), for giving files back after a container job.
func ownerArgs() string {
	return itoa(int64(syscall.Getuid())) + ":" + itoa(int64(syscall.Getgid()))
}
