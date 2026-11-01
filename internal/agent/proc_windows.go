// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no process groups to signal: a command runs in a job object instead, which holds the process and
// every process it starts. Cancelling the command terminates the job object, and closing its last handle (when the
// job no longer needs what a step left running, or when the agent itself ends) kills what is still in it
// (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE). The process starts suspended and runs only once it is in the job object,
// so that no process it starts escapes.

// processTree is a command with the processes it starts, which cancelling the command stops together.
type processTree struct {
	cmd     *exec.Cmd
	warning string
	mu      sync.Mutex     // cancelling (from the goroutine that watches the context) and closing use the handle
	job     windows.Handle // 0: the command runs without a job object (see warning)
}

// startTree starts a command in a job object of its own.
func startTree(cmd *exec.Cmd) (*processTree, error) {
	t := &processTree{cmd: cmd}
	job, err := newKillOnCloseJob()
	if err != nil {
		t.warning = fmt.Sprintf("Cancelling stops only the step's own process: no job object (%v).", err)
	} else {
		t.job = job
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	}
	cmd.Cancel = func() error {
		t.mu.Lock()
		if t.job != 0 {
			_ = windows.TerminateJobObject(t.job, 1)
		}
		t.mu.Unlock()
		// Also the process itself, should it not be in the job object yet.
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Start(); err != nil {
		t.close()
		return nil, err
	}
	if job != 0 {
		if err := assign(job, cmd.Process.Pid); err != nil {
			// Without the job object the command still runs; cancelling stops its own process only.
			t.warning = fmt.Sprintf("Cancelling stops only the step's own process: it is not in a job object (%v).", err)
			t.close()
		}
		if err := resume(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.close()
			return nil, fmt.Errorf("cannot start %s: %w", cmd.Path, err)
		}
	}
	return t, nil
}

// newKillOnCloseJob creates a job object whose processes are killed when its last handle is closed.
func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// assign puts a process into a job object.
func assign(job windows.Handle, pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(job, h)
}

// resume lets a process that was started suspended run: it resumes the threads of the process (a new process has
// one).
func resume(pid int) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if err != nil {
			return err
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	if resumed == 0 {
		return errors.New("the process has no thread to resume")
	}
	return nil
}

// Wait waits for the command to end.
func (t *processTree) Wait() error { return t.cmd.Wait() }

// Warning tells why cancelling may not stop all processes of the tree ("" when it does).
func (t *processTree) Warning() string { return t.warning }

// Release ends what the command left running in the background (when the job no longer needs it).
func (t *processTree) Release() { t.close() }

func (t *processTree) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

// runTree runs a command as a process tree (see startTree); what it leaves running ends with it.
func runTree(cmd *exec.Cmd) error {
	t, err := startTree(cmd)
	if err != nil {
		return err
	}
	defer t.Release()
	return t.Wait()
}

// ownerArgs is empty on Windows: container files need no change of owner.
func ownerArgs() string { return "" }
