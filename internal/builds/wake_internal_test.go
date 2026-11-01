// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"testing"
)

func woken(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestNotified: the notifications of all nodes wake the agents waiting at this node and the streams of the job's
// log only; the end of a job drops its secrets, a resync wakes everyone.
func TestNotified(t *testing.T) {
	s := &Service{}
	agents, one, two := s.wake.wait(), s.logWake.wait(1), s.logWake.wait(2)
	s.Notified("log:1")
	if !woken(one) || woken(two) || woken(agents) {
		t.Fatalf("log:1 woke the stream of job 1 %v, of job 2 %v, the agents %v", woken(one), woken(two), woken(agents))
	}
	s.Notified("jobs")
	if !woken(agents) || woken(two) {
		t.Fatal("jobs did not wake the agents, or woke a log stream")
	}
	s.masks.put(2, 1, []string{"s3cr3t"})
	s.Notified("ended:2")
	if _, ok := s.masks.get(2, 1); ok || !woken(two) {
		t.Fatal("ended:2 kept the secrets of job 2 or did not wake its stream")
	}
	for _, payload := range []string{"", "log:", "log:x", "nonsense:1"} {
		s.Notified(payload) // ignored
	}
	agents, one = s.wake.wait(), s.logWake.wait(1)
	s.Resync()
	if !woken(agents) || !woken(one) {
		t.Fatal("a resync did not wake everyone")
	}
}

// TestJobMasks: the secrets of a job are those of its attempt; a node keeps those of a bounded number of jobs.
func TestJobMasks(t *testing.T) {
	var m jobMasks
	m.put(7, 1, []string{"first"})
	if v, ok := m.get(7, 1); !ok || v[0] != "first" {
		t.Fatalf("get = %v %v", v, ok)
	}
	if _, ok := m.get(7, 2); ok {
		t.Fatal("the secrets of attempt 1 serve attempt 2")
	}
	for i := range int64(maskedJobs) {
		m.put(100+i, 1, nil)
	}
	if len(m.jobs) > maskedJobs {
		t.Fatalf("%d jobs kept", len(m.jobs))
	}
}
