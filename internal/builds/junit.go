// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
)

// TestReport sums up the JUnit reports of a job.
type TestReport struct {
	Total      int           `json:"total"`
	Failed     int           `json:"failed"`
	Errors     int           `json:"errors"`
	Skipped    int           `json:"skipped"`
	DurationMs int64         `json:"durationMs"`
	Failures   []TestFailure `json:"failures"`
}

// mask replaces the secrets in the texts of a report.
func (r *TestReport) mask(secrets []string) {
	if len(secrets) == 0 {
		return
	}
	m := func(s string) string { return string(maskSecrets([]byte(s), secrets)) }
	for i := range r.Failures {
		f := &r.Failures[i]
		f.Suite, f.Name, f.Class, f.Message, f.Output = m(f.Suite), m(f.Name), m(f.Class), m(f.Message), m(f.Output)
	}
}

// TestFailure is a failed or erroneous test case.
type TestFailure struct {
	Suite   string `json:"suite"`
	Name    string `json:"name"`
	Class   string `json:"class,omitempty"`
	Kind    string `json:"kind" enum:"failure,error"`
	Message string `json:"message"`
	Output  string `json:"output,omitempty"`
}

type junitCase struct {
	Name      string    `xml:"name,attr"`
	Classname string    `xml:"classname,attr"`
	Time      string    `xml:"time,attr"`
	Failure   *jProblem `xml:"failure"`
	Error     *jProblem `xml:"error"`
	Skipped   *struct{} `xml:"skipped"`
	SystemOut string    `xml:"system-out"`
}

type jProblem struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type junitSuite struct {
	Name   string       `xml:"name,attr"`
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}

// ParseJUnit reads JUnit XML (a testsuites or a testsuite root).
func ParseJUnit(data []byte) (*TestReport, error) {
	var root struct {
		XMLName xml.Name
		junitSuite
	}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	dec.Strict = false
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	rep := &TestReport{Failures: []TestFailure{}}
	var walk func(s junitSuite)
	walk = func(s junitSuite) {
		for _, c := range s.Cases {
			rep.Total++
			if d, err := strconv.ParseFloat(c.Time, 64); err == nil {
				rep.DurationMs += int64(d * 1000)
			}
			switch {
			case c.Failure != nil:
				rep.Failed++
				rep.addFailure(s.Name, c, "failure", c.Failure)
			case c.Error != nil:
				rep.Errors++
				rep.addFailure(s.Name, c, "error", c.Error)
			case c.Skipped != nil:
				rep.Skipped++
			}
		}
		for _, sub := range s.Suites {
			walk(sub)
		}
	}
	walk(root.junitSuite)
	return rep, nil
}

func (r *TestReport) addFailure(suite string, c junitCase, kind string, p *jProblem) {
	if len(r.Failures) >= 500 {
		return
	}
	msg := p.Message
	if msg == "" {
		msg = p.Type
	}
	out := strings.TrimSpace(p.Text)
	if len(out) > 8000 {
		out = out[:8000] + "…"
	}
	r.Failures = append(r.Failures, TestFailure{Suite: suite, Name: c.Name, Class: c.Classname, Kind: kind, Message: truncate(msg, 2000), Output: out})
}

// addReport merges a report into the job's report.
func (s *Service) addReport(ctx context.Context, jobID int64, rep *TestReport) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var raw []byte
		var cur TestReport
		err := tx.QueryRow(ctx, "SELECT total, failed, errors, skipped, duration_ms, failures FROM test_reports WHERE job_id = $1 FOR UPDATE", jobID).
			Scan(&cur.Total, &cur.Failed, &cur.Errors, &cur.Skipped, &cur.DurationMs, &raw)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		_ = json.Unmarshal(raw, &cur.Failures)
		cur.Total += rep.Total
		cur.Failed += rep.Failed
		cur.Errors += rep.Errors
		cur.Skipped += rep.Skipped
		cur.DurationMs += rep.DurationMs
		cur.Failures = append(nonNil(cur.Failures), rep.Failures...)
		if len(cur.Failures) > 500 {
			cur.Failures = cur.Failures[:500]
		}
		b, _ := json.Marshal(cur.Failures)
		_, err = tx.Exec(ctx, `INSERT INTO test_reports (job_id, total, failed, errors, skipped, duration_ms, failures) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (job_id) DO UPDATE SET total = EXCLUDED.total, failed = EXCLUDED.failed, errors = EXCLUDED.errors, skipped = EXCLUDED.skipped,
			duration_ms = EXCLUDED.duration_ms, failures = EXCLUDED.failures`, jobID, cur.Total, cur.Failed, cur.Errors, cur.Skipped, cur.DurationMs, b)
		return err
	})
}
