// Command migrate applies the goose migrations embedded in package db.
//
//	go run ./cmd/migrate up            apply every pending migration
//	go run ./cmd/migrate down          roll back the most recent migration
//	go run ./cmd/migrate status        list migrations and whether they are applied
//	go run ./cmd/migrate version       print the current schema version
//	go run ./cmd/migrate reset         roll back everything (dev only)
//	go run ./cmd/migrate create NAME   write db/migrations/000NN_NAME.sql
//
// The connection string comes from -dsn or MIGRATE_DATABASE_URL and must be the
// templ_app_migrator role, which owns the schema. The runtime role templ_app has
// no DDL rights on purpose. See db/README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the database/sql driver "pgx"
	"github.com/pressly/goose/v3"

	"templ-app/db"
)

const (
	embeddedDir = "migrations"    // path inside db.Migrations
	sourceDir   = "db/migrations" // path on disk; only `create` writes here
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dsn := flags.String("dsn", os.Getenv("MIGRATE_DATABASE_URL"), "connection string (default: $MIGRATE_DATABASE_URL)")
	dir := flags.String("dir", sourceDir, "on-disk migrations directory, used by create")
	timeout := flags.Duration("timeout", 10*time.Minute, "abort the command after this long")
	verbose := flags.Bool("v", false, "verbose goose output")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: migrate [flags] up|down|status|version|reset|create NAME")
		flags.PrintDefaults()
	}
	if err := flags.Parse(argv); err != nil {
		return err
	}
	args := flags.Args()
	if len(args) == 0 {
		flags.Usage()
		return errors.New("command required")
	}
	command, rest := args[0], args[1:]

	goose.SetVerbose(*verbose)
	goose.SetSequential(true) // 00001, 00002, ... matches the baseline numbering
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	// create works on the source tree, not on the embedded copy, and needs no database.
	if command == "create" {
		if len(rest) == 0 {
			return errors.New("create needs a name, e.g. migrate create add_kyc_documents")
		}
		return goose.Create(nil, *dir, strings.Join(rest, "_"), "sql")
	}

	if *dsn == "" {
		return errors.New("no connection string: set MIGRATE_DATABASE_URL or pass -dsn")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	sqldb, err := goose.OpenDBWithDriver("pgx", *dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer sqldb.Close()
	if err := sqldb.PingContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	goose.SetBaseFS(db.Migrations)
	return goose.RunContext(ctx, command, sqldb, embeddedDir, rest...)
}
