// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

//go:build windows

package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestHelperTree is not a test: started by the tests below with KOGGE_TEST_TREE, it plays a step that starts a
// process of its own and prints that process's id.
func TestHelperTree(t *testing.T) {
	switch os.Getenv("KOGGE_TEST_TREE") {
	case "step", "step-leaves":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperTree$")
		child.Env = append(os.Environ(), "KOGGE_TEST_TREE=sleep")
		if err := child.Start(); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		fmt.Println(child.Process.Pid)
		if os.Getenv("KOGGE_TEST_TREE") == "step" {
			time.Sleep(2 * time.Minute)
		}
		os.Exit(0)
	case "sleep":
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
}

// startStep starts the helper as a step and returns the id of the process it started.
func startStep(t *testing.T, ctx context.Context, mode string) (*processTree, uint32) {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperTree$")
	cmd.Env = append(os.Environ(), "KOGGE_TEST_TREE="+mode)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := startTree(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if w := tree.Warning(); w != "" {
		t.Fatalf("the step runs without a job object: %s", w)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the child's id: %v", err)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(line), 10, 32)
	if err != nil {
		t.Fatalf("the step printed %q", line)
	}
	return tree, uint32(pid)
}

// running reports whether a process runs, waiting up to a while for it to end.
func endsWithin(t *testing.T, pid uint32, d time.Duration) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return true // gone already
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(d.Milliseconds()))
	if err != nil {
		t.Fatal(err)
	}
	return ev == windows.WAIT_OBJECT_0
}

// TestCancelEndsTheTree: cancelling a step ends the processes it started, not only its own.
func TestCancelEndsTheTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tree, child := startStep(t, ctx, "step")
	defer tree.Release()
	if endsWithin(t, child, 500*time.Millisecond) {
		t.Fatal("the child ended by itself")
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- tree.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the step did not end when cancelled")
	}
	if !endsWithin(t, child, 10*time.Second) {
		t.Error("the step's child process still runs after cancelling")
	}
}

// TestTimeoutEndsTheTree: a step that runs out of time ends with its processes.
func TestTimeoutEndsTheTree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tree, child := startStep(t, ctx, "step")
	defer tree.Release()
	_ = tree.Wait()
	if !endsWithin(t, child, 10*time.Second) {
		t.Error("the step's child process still runs after the timeout")
	}
}

// TestReleaseEndsLeftovers: what a step leaves running in the background runs until the job releases it.
func TestReleaseEndsLeftovers(t *testing.T) {
	tree, child := startStep(t, context.Background(), "step-leaves")
	if err := tree.Wait(); err != nil {
		t.Fatal(err)
	}
	if endsWithin(t, child, 500*time.Millisecond) {
		t.Fatal("the process left running ended with the step")
	}
	tree.Release()
	if !endsWithin(t, child, 10*time.Second) {
		t.Error("the process left running survived the end of the job")
	}
}
