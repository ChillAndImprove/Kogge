// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Command scale checks Kogge against its performance and scale targets. It generates data of a
// reference size in an installation and measures the response times of the operations with targets.
//
//	go run ./tools/scale generate -size S -manifest scale.json   # with the server's configuration (KOGGE_DATABASE_URL, …)
//	go run ./tools/scale bench -url http://localhost:8080 -manifest scale.json
//
// Generate into an installation of its own: the data cannot be removed again except by dropping the database.
package main

import (
	"fmt"
	"os"
)

const usage = `Usage:
  scale generate [-size XS|S|M] [-fraction F] [-seed N] [-manifest FILE]
  scale bench -url URL [-manifest FILE] [-requests N] [-concurrency N] [-strict] [-only KEYS]
              [-pushes N] [-jobs N] [-clones N] [-agent FILE] [-git FILE]

generate writes users, issues, pages and repositories through the import interfaces of the modules, with the
server's configuration (KOGGE_DATABASE_URL, KOGGE_DATA_DIR, …), and a manifest for bench that holds access tokens.

bench measures every operation with a performance target and Git over HTTP, and prints p50, p95 and p99:
  issue, kql, board, page, quick-issues, quick-pages, file, diff   GET requests as a reader (-requests, -concurrency)
  push       a push of 100 commits that name issues until the last is in its issue (-pushes, one at a time)
  handover   a job handed to an idle agent after it became pending: a real kogge-agent (-agent) with the shell
             executor runs a pipeline started through the API (-jobs, one at a time)
  clone      git clone of the repository with 200 files (-clones, -concurrency in parallel)
  fetch      git fetch of a clone that is up to date (-clones, -concurrency in parallel)
  fetch-new  git fetch of 100 commits pushed just before (-pushes, one at a time)
-only measures some of them, e.g. -only push,handover. Pushes and runs add to the installation's data.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "generate":
		err = generateCommand(os.Args[2:])
	case "bench":
		err = benchCommand(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "scale:", err)
		os.Exit(1)
	}
}
