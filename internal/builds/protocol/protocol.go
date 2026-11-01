// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package protocol defines the messages between Kogge and its build agents; the server and the agent
// share it.
//
// Agents register once with a registration token and keep the agent secret they receive. They long-poll
// POST /jobs/claim (Authorization: Bearer <agent secret>); a claimed job comes with a job token that authenticates
// everything about the job (Authorization: Bearer <job token>, and as Basic auth password for Git):
//
//	POST /jobs/{id}/heartbeat           → HeartbeatResponse; at least every LeaseRenewal
//	POST /jobs/{id}/log?offset=N        → LogResponse; 409 with LogResponse when N is not the stored size
//	PUT  /jobs/{id}/artifacts/{name}    zip archive
//	GET  /jobs/{id}/artifacts/{id}      zip archive of an earlier job
//	GET  /jobs/{id}/caches/{key}        tar.gz archive; 404 when there is none
//	PUT  /jobs/{id}/caches/{key}        tar.gz archive
//	POST /jobs/{id}/reports/junit       JUnit XML
//	POST /jobs/{id}/complete            CompleteRequest
package protocol

import (
	"time"

	"kogge.de/kogge/internal/builds/spec"
)

// BasePath is the prefix of the agent protocol.
const BasePath = "/api/agent/v1"

// Timing of the protocol.
const (
	LeaseRenewal = 10 * time.Second // agents send heartbeats this often (cancellations reach them with the answer)
	Lease        = 90 * time.Second // a job whose agent sent no heartbeat for this long is failed ("agent lost")
	PollTimeout  = 25 * time.Second // the server answers a claim with 204 No Content after this long
	MaxLogChunk  = 1 << 20          // bytes per log request
)

// Results of jobs.
const (
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Lines of a job log that start with these markers are shown specially: a step header, a message of the agent,
// an error of the agent.
const (
	MarkStep  = "##[step] "
	MarkInfo  = "##[info] "
	MarkError = "##[error] "
)

// RegisterRequest is an agent's first contact.
type RegisterRequest struct {
	Token   string   `json:"token"`
	Name    string   `json:"name"`
	Labels  []string `json:"labels"`
	OS      string   `json:"os"`
	Arch    string   `json:"arch"`
	Version string   `json:"version"`
}

// ClaimRequest asks for a job; it also updates what the agent announced at registration.
type ClaimRequest struct {
	Labels  []string `json:"labels"`
	OS      string   `json:"os"`
	Arch    string   `json:"arch"`
	Version string   `json:"version"`
	// Limits are the most the agent allows one container of a job (its configuration): it gets only jobs that ask
	// for no more. Agents that announce none take every job.
	Limits *Limits `json:"limits,omitempty"`
}

// Limits are the most an agent allows one container; 0: no limit.
type Limits struct {
	MilliCPUs int64 `json:"milliCpus,omitempty"`
	Memory    int64 `json:"memory,omitempty" doc:"Bytes"`
}

// RegisterResponse carries the agent's secret (shown once).
type RegisterResponse struct {
	ID     int64  `json:"id"`
	Secret string `json:"secret"`
}

// ArtifactRef is an artifact of an earlier job that a job downloads before it starts.
type ArtifactRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Job  string `json:"job" doc:"stage/job; of the run that this run followed: pipeline #number: stage/job"`
	Size int64  `json:"size"`
}

// JobPayload is a job handed to an agent.
type JobPayload struct {
	ID         int64             `json:"id"`
	Token      string            `json:"token"`
	Attempt    int               `json:"attempt"`
	RunID      int64             `json:"runId"`
	RunNumber  int               `json:"runNumber"`
	Pipeline   string            `json:"pipeline"`
	Stage      string            `json:"stage"`
	CloneURL   string            `json:"cloneUrl"`
	Commit     string            `json:"commit"`
	Branch     string            `json:"branch,omitempty"`
	Tag        string            `json:"tag,omitempty"`
	Job        spec.Job          `json:"job"`
	Variables  map[string]string `json:"variables"`
	Mask       []string          `json:"mask" doc:"Secret values to mask in the log"`
	Artifacts  []ArtifactRef     `json:"artifacts"`
	LogOffset  int64             `json:"logOffset" doc:"Size of the log so far (retried jobs continue it)"`
	ServerURL  string            `json:"serverUrl"`
	Repository string            `json:"repository" doc:"KEY/slug"`
}

// HeartbeatResponse tells the agent whether to stop the job.
type HeartbeatResponse struct {
	Cancel bool `json:"cancel"`
}

// LogResponse is the stored size of the log, the offset of the next chunk.
type LogResponse struct {
	Size int64 `json:"size"`
}

// CompleteRequest reports a job's result.
type CompleteRequest struct {
	State    string `json:"state"` // StateSucceeded, StateFailed or StateCancelled
	ExitCode *int   `json:"exitCode,omitempty"`
	Failure  string `json:"failure,omitempty" doc:"Why the job failed, for people"`
}
