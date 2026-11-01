// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"kogge.de/kogge/internal/backup"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
)

// backupCommand runs `kogge admin backup|restore|fsck|pause-writes`. They work without the
// application: a restore starts from an empty database, and a backup runs next to the servers.
func backupCommand(name string, args []string) error {
	cfg, _, err := load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Progress messages are for the operator; the log only reports problems.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	o := backup.Options{DataDir: cfg.DataDir, Logger: logger, Progress: func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}}
	keyPath := crypto.KeyPath(cfg.MasterKeyFile, cfg.DataDir)

	var run func() error
	switch name {
	case "backup":
		fl := flag.NewFlagSet("backup", flag.ExitOnError)
		out := fl.String("o", "", "the backup file")
		dir := fl.String("dir", ".", "the directory of the backup file, named kogge-backup-DATE-TIME.tar (without -o)")
		keep := fl.Int("keep", 0, "delete all but the newest N backups in -dir after a successful backup (0: keep all)")
		_ = fl.Parse(args)
		run = func() error {
			if o.Keyring, err = crypto.LoadKeyring(cfg.MasterKeyFile, cfg.DataDir); err != nil {
				return err
			}
			path := *out
			if path == "" {
				path = filepath.Join(*dir, backup.FileName(time.Now()))
			}
			started := time.Now()
			sum, err := backup.Write(ctx, o, path)
			// The outcome goes to the audit log, where the administration's overview finds the last backup. A record
			// that cannot be written is reported, and makes the command fail, so that the operator learns why the
			// overview shows an older backup.
			if err != nil {
				if rerr := audit.Record(context.WithoutCancel(ctx), o.Pool, audit.Entry{Action: "backup.failed",
					Details: map[string]any{"file": filepath.Base(path), "error": err.Error()}}); rerr != nil {
					logger.Error("the failed backup could not be recorded in the audit log", "file", path, "error", rerr)
				}
				return err
			}
			recordErr := audit.Record(ctx, o.Pool, audit.Entry{Action: "backup.written", Details: map[string]any{"file": filepath.Base(path),
				"bytes": sum.Bytes, "seconds": int(time.Since(started).Seconds()), "missing": len(sum.Missing)}})
			fmt.Fprintf(os.Stderr, "Backup written to %s in %s (%s of data).\n", path, time.Since(started).Round(time.Second), size(sum.Bytes))
			fmt.Fprintf(os.Stderr, "Keep the master key (%s) separately; the backup does not contain it.\n", keyPath)
			if len(sum.Missing) > 0 {
				return errors.Join(fmt.Errorf("%d files the database refers to were missing; run `kogge admin fsck`", len(sum.Missing)),
					unrecorded(path, recordErr))
			}
			if *out == "" && *keep > 0 {
				deleted, err := backup.Prune(*dir, *keep)
				for _, f := range deleted {
					fmt.Fprintf(os.Stderr, "Deleted the old backup %s.\n", f)
				}
				if err != nil {
					return errors.Join(err, unrecorded(path, recordErr))
				}
			}
			return unrecorded(path, recordErr)
		}
	case "restore":
		fl := flag.NewFlagSet("restore", flag.ExitOnError)
		ignoreKey := fl.Bool("ignore-master-key", false, "restore without the backup's master key; webhook secrets and secret build variables must be entered again")
		_ = fl.Parse(args)
		if fl.NArg() != 1 {
			return errors.New("restore: usage: kogge admin restore [-ignore-master-key] FILE")
		}
		run = func() error {
			o.Keyring, err = crypto.LoadKeyring(cfg.MasterKeyFile, cfg.DataDir)
			if err != nil && (!*ignoreKey || !errors.Is(err, fs.ErrNotExist)) {
				return fmt.Errorf("%w: copy the master key file of the backed-up installation to %s first", err, keyPath)
			}
			report, err := backup.Restore(ctx, o, fl.Arg(0), backup.RestoreOptions{IgnoreMasterKey: *ignoreKey, KeyFile: keyPath})
			if err != nil {
				return err
			}
			printCheck(report)
			if n := report.Errors(); n > 0 {
				return fmt.Errorf("the restored installation has %d errors", n)
			}
			fmt.Fprintln(os.Stderr, "The backup is restored; start Kogge.")
			return nil
		}
	case "fsck":
		fl := flag.NewFlagSet("fsck", flag.ExitOnError)
		var co backup.CheckOptions
		fl.BoolVar(&co.Repair, "repair", false, "repair what can be repaired without losing data")
		fl.BoolVar(&co.Hashes, "hashes", false, "read every blob and verify its SHA-256")
		fl.BoolVar(&co.Git, "git", false, "run git fsck on every repository")
		_ = fl.Parse(args)
		run = func() error {
			report, err := backup.Check(ctx, o, co)
			if err != nil {
				return err
			}
			printCheck(report)
			if n := report.Errors(); n > 0 {
				return fmt.Errorf("fsck found %d errors", n)
			}
			return nil
		}
	case "pause-writes":
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) == 0 {
			return errors.New("pause-writes: usage: kogge admin pause-writes -- COMMAND [ARGS...]")
		}
		run = func() error {
			return backup.PauseWrites(ctx, o, func(ctx context.Context, at time.Time) error {
				cmd := exec.CommandContext(ctx, args[0], args[1:]...)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				cmd.Env = append(os.Environ(), "KOGGE_PAUSED_AT="+at.UTC().Format(time.RFC3339Nano))
				return cmd.Run()
			})
		}
	}
	if o.Pool, err = db.Open(ctx, cfg.DatabaseURL, "kogge-"+name, 4); err != nil {
		return err
	}
	defer o.Pool.Close()
	return run()
}

// unrecorded is the error of a backup that was written but not recorded in the audit log, or nil.
func unrecorded(path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("the backup %s is complete, but it could not be recorded in the audit log, so the administration shows an "+
		"older backup as the latest: %w", path, err)
}

// size formats a number of bytes for people.
func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func printCheck(r *backup.Report) {
	warnings := 0
	for _, p := range r.Problems {
		level := "warning"
		if p.Error {
			level = "error"
		} else {
			warnings++
		}
		if p.Repaired {
			level += ", repaired"
		}
		fmt.Printf("%-17s %-20s %s: %s\n", level, p.Kind, p.Subject, p.Detail)
	}
	fmt.Printf("Checked %d repositories, %d blobs and %d build logs: %d errors, %d warnings.\n",
		r.Repositories, r.Blobs, r.Logs, len(r.Problems)-warnings, warnings)
}
