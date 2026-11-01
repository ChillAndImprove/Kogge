// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Command kogge runs the Kogge server and its administrative commands.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/privacy"
	"kogge.de/kogge/internal/version"
)

const usage = `Kogge — issues, wiki, code and builds on your own servers.

Usage:
  kogge serve                      run the server (roles from KOGGE_ROLES)
  kogge migrate                    apply database migrations
  kogge admin init-database [flags]  create the database roles kogge_app and kogge_maint and their grants, with a
                                   superuser's KOGGE_DATABASE_URL (-owner ROLE, -app-password-file FILE)
  kogge admin create-user [flags]  create a local user (password from stdin)
  kogge admin set-password [flags] set a local password (password from stdin)
  kogge admin grant [flags]        grant a global permission
  kogge admin gen-master-key       print a new master key
  kogge admin backup [flags]       write a backup while Kogge runs (-o FILE, or -dir DIR with -keep N)
  kogge admin restore FILE         restore a backup into an empty installation
  kogge admin fsck [flags]         check the data directory against the database (-repair, -hashes, -git)
  kogge admin pause-writes -- CMD  run CMD (e.g. a storage snapshot) while Git writes are paused
  kogge admin export-user -username NAME     write what Kogge holds about a person as JSON
  kogge admin anonymise-user -username NAME  anonymise a person (cannot be undone; asks to confirm)
  kogge admin verify-audit         check the audit log against its daily digest chain
  kogge admin purge-audit          delete audit events older than their retention (with the owner's database URL)
  kogge admin forward-audit -from DAY|now  forward to the SIEM again from DAY (YYYY-MM-DD) on, or skip what waits
  kogge import jira|confluence|bitbucket|bamboo STEP  migrate from Jira, Confluence, Bitbucket or Bamboo Data Center
                                   (STEP: extract, analyse, load, validate or all)
  kogge openapi                    print the OpenAPI document
  kogge version                    print the version, the commit and the build date

Configuration comes from environment variables (KOGGE_*); README.md names the main ones.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "migrate":
		err = migrate()
	case "admin":
		err = adminCommand(os.Args[2:])
	case "import":
		err = importCommand(os.Args[2:])
	case "hook":
		// Run by git during pushes (see internal/code/hooks.go).
		if len(os.Args) < 3 {
			os.Exit(2)
		}
		os.Exit(code.RunHook(os.Args[2], os.Stdin, os.Stdout, os.Stderr))
	case "openapi":
		var doc []byte
		if doc, err = app.OpenAPI(); err == nil {
			_, err = os.Stdout.Write(doc)
		}
	case "version", "--version", "-v":
		fmt.Print(version.Describe("kogge"))
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kogge:", err)
		os.Exit(1)
	}
}

func load() (*config.Config, *slog.Logger, error) {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return nil, nil, err
	}
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler = slog.NewJSONHandler(os.Stderr, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	logger := slog.New(h).With("kogge", version.Version)
	slog.SetDefault(logger)
	return cfg, logger, nil
}

func serve() error {
	cfg, logger, err := load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, cfg, logger, app.Options{})
	if err != nil {
		return err
	}
	defer a.Close()
	return a.Run(ctx)
}

func migrate() error {
	cfg, logger, err := load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL, "kogge-migrate", 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, logger); err != nil {
		return err
	}
	logger.Info("database is up to date")
	return nil
}

// initDatabase sets up the database roles of an installation on an existing PostgreSQL, as deploy/db/roles.sh and
// the migrations do for the Compose stack: Kogge then runs as kogge_app, which cannot change the audit log, and the
// owner runs the migrations. It can run again at any time.
func initDatabase(args []string) error {
	fs := flag.NewFlagSet("init-database", flag.ExitOnError)
	owner := fs.String("owner", "", "the role that owns the schema and runs the migrations (default: the owner of the database)")
	passwordFile := fs.String("app-password-file", "", "a file with the password of the role kogge_app (to create it or change its password)")
	passwordStdin := fs.Bool("app-password-stdin", false, "read the password of the role kogge_app from standard input")
	_ = fs.Parse(args)
	cfg, _, err := load()
	if err != nil {
		return err
	}
	var password string
	switch {
	case *passwordFile != "":
		b, err := os.ReadFile(*passwordFile)
		if err != nil {
			return err
		}
		password = strings.TrimRight(string(b), "\r\n")
	case *passwordStdin:
		if password, err = readPassword(true); err != nil {
			return err
		}
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	done, err := db.SetUpRoles(ctx, conn, db.RoleSetup{Owner: *owner, AppPassword: password})
	for _, d := range done {
		fmt.Println(d)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Run Kogge with the URL of %s (KOGGE_DATABASE_URL) and `kogge migrate` with the owner's.\n", db.AppRole)
	return nil
}

func readPassword(fromStdin bool) (string, error) {
	if !fromStdin {
		return "", nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func adminCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("admin: missing subcommand")
	}
	switch args[0] {
	case "gen-master-key":
		fmt.Println(crypto.GenerateKey())
		return nil
	case "backup", "restore", "fsck", "pause-writes":
		return backupCommand(args[0], args[1:])
	case "init-database":
		return initDatabase(args[1:])
	}
	cfg, logger, err := load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	quiet := *cfg
	quiet.BootstrapAdmin = config.BootstrapAdmin{}
	a, err := app.New(ctx, &quiet, logger, app.Options{})
	if err != nil {
		return err
	}
	defer a.Close()
	actor := identity.SystemPrincipal()

	switch args[0] {
	case "export-user", "anonymise-user":
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		username := fs.String("username", "", "user name")
		yes := fs.Bool("yes", false, "anonymise without asking")
		_ = fs.Parse(args[1:])
		u, err := identity.GetUserByUsername(ctx, a.Pool, *username)
		if err != nil {
			return err
		}
		if args[0] == "export-user" {
			out, err := privacy.ExportUser(ctx, a.Pool, u.ID)
			if err != nil {
				return err
			}
			if err := audit.Record(ctx, a.Pool, audit.Entry{ActorID: actor.UserID(), Action: "user.export", TargetType: "user",
				TargetID: audit.ID(u.ID), Details: map[string]any{"cli": true}}); err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		if !*yes {
			fmt.Fprintf(os.Stderr, "Anonymise %s (%s)? This cannot be undone. Type the user name to confirm: ", u.DisplayName, u.Username)
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(answer) != u.Username {
				return errors.New("not confirmed")
			}
		}
		if err := privacy.AnonymiseUser(ctx, a.Pool, actor, u.ID); err != nil {
			return err
		}
		fmt.Printf("anonymised %s as %q\n", u.Username, privacy.Pseudonym(u.ID))
		return nil
	case "verify-audit":
		st, err := a.Settings.Get(ctx)
		if err != nil {
			return err
		}
		days, findings, err := audit.Verify(ctx, a.Pool, st.AuditRetentionDays)
		if err != nil {
			return err
		}
		for _, f := range findings {
			fmt.Printf("%s: %s\n", f.Day.Format("2006-01-02"), f.Problem)
		}
		if len(findings) > 0 {
			return fmt.Errorf("the audit log does not match its digests on %d of %d days", len(findings), days)
		}
		fmt.Printf("the audit log matches its digests (%d days)\n", days)
		return nil
	case "purge-audit":
		st, err := a.Settings.Get(ctx)
		if err != nil {
			return err
		}
		n, err := audit.Purge(ctx, a.Pool, st.AuditRetentionDays)
		if errors.Is(err, audit.ErrAppendOnly) {
			return errors.New("this database role may not delete audit events; run the command with the owner's KOGGE_DATABASE_URL")
		}
		if err != nil {
			return err
		}
		fmt.Printf("deleted %d audit events older than %d days\n", n, st.AuditRetentionDays)
		return nil
	case "forward-audit":
		fs := flag.NewFlagSet("forward-audit", flag.ExitOnError)
		fromFlag := fs.String("from", "", "forward again from this day (YYYY-MM-DD, UTC) or time (RFC 3339) on, or skip the waiting events: now")
		_ = fs.Parse(args[1:])
		var from time.Time
		switch v := strings.TrimSpace(*fromFlag); v {
		case "":
			return errors.New("forward-audit: -from DAY, -from TIME or -from now is required")
		case "now":
		default:
			if from, err = time.Parse("2006-01-02", v); err != nil {
				if from, err = time.Parse(time.RFC3339, v); err != nil {
					return fmt.Errorf("forward-audit: -from %q is neither a day (YYYY-MM-DD), a time (RFC 3339) nor now", v)
				}
			}
		}
		if err := audit.RewindForwarding(ctx, a.Pool, actor.UserID(), from); err != nil {
			return err
		}
		st, err := audit.Forwarding(ctx, a.Pool)
		if err != nil {
			return err
		}
		if from.IsZero() {
			fmt.Printf("forwarding continues with the events to come (%d waiting)\n", st.Waiting)
		} else {
			fmt.Printf("forwarding continues with the events from %s on (%d waiting)\n", from.UTC().Format(time.RFC3339), st.Waiting)
		}
		if !cfg.AuditForward.Enabled() {
			fmt.Println("note: forwarding is not configured here (KOGGE_AUDIT_FORWARD_URL); the position applies once it is")
		}
		return nil
	case "create-user":
		fs := flag.NewFlagSet("create-user", flag.ExitOnError)
		username := fs.String("username", "", "user name (lower case)")
		name := fs.String("display-name", "", "display name")
		email := fs.String("email", "", "e-mail address")
		isAdmin := fs.Bool("admin", false, "grant the administrator permission")
		stdin := fs.Bool("password-stdin", false, "read the password from standard input")
		_ = fs.Parse(args[1:])
		password, err := readPassword(*stdin)
		if err != nil {
			return err
		}
		return db.InTx(ctx, a.Pool, func(tx pgx.Tx) error {
			u, err := a.Identity.CreateLocalUser(ctx, tx, *username, *name, *email, password)
			if err != nil {
				return err
			}
			if *isAdmin {
				if err := authz.AddGrant(ctx, tx, authz.Grant{Permission: authz.Admin, PrincipalType: authz.PrincipalUser,
					PrincipalID: u.ID}); err != nil {
					return err
				}
			}
			fmt.Printf("created user %s (id %d)\n", u.Username, u.ID)
			return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "user.create", TargetType: "user",
				TargetID: audit.ID(u.ID), Details: map[string]any{"cli": true, "admin": *isAdmin}})
		})
	case "set-password":
		fs := flag.NewFlagSet("set-password", flag.ExitOnError)
		username := fs.String("username", "", "user name")
		_ = fs.Parse(args[1:])
		password, err := readPassword(true)
		if err != nil {
			return err
		}
		u, err := identity.GetUserByUsername(ctx, a.Pool, *username)
		if err != nil {
			return err
		}
		return a.Identity.SetPassword(ctx, actor, u.ID, password)
	case "grant":
		fs := flag.NewFlagSet("grant", flag.ExitOnError)
		perm := fs.String("permission", "", "global permission, e.g. admin")
		user := fs.String("user", "", "user name")
		group := fs.String("group", "", "group name")
		_ = fs.Parse(args[1:])
		g := authz.Grant{Permission: *perm}
		switch {
		case *user != "":
			u, err := identity.GetUserByUsername(ctx, a.Pool, *user)
			if err != nil {
				return err
			}
			g.PrincipalType, g.PrincipalID = authz.PrincipalUser, u.ID
		case *group != "":
			gr, err := identity.GetGroupByName(ctx, a.Pool, *group)
			if err != nil {
				return err
			}
			g.PrincipalType, g.PrincipalID = authz.PrincipalGroup, gr.ID
		default:
			return errors.New("grant: --user or --group is required")
		}
		return db.InTx(ctx, a.Pool, func(tx pgx.Tx) error {
			if err := authz.AddGrant(ctx, tx, g); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID(), Action: "permission.global.grant",
				TargetType: "permission", TargetID: g.Permission, Details: map[string]any{"cli": true}})
		})
	default:
		return apperr.BadRequest("cli", "unknown admin subcommand %q", args[0])
	}
}
