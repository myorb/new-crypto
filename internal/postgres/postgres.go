// Package postgres owns the connection pool and the transaction helper every
// domain service uses. It is the only place that knows pgx error codes.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is what services return instead of pgx.ErrNoRows so callers
// never import pgx to check for a missing row.
var ErrNotFound = errors.New("not found")

// Open parses the URL, applies pool defaults and pings the database.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse DATABASE_URL: %w", err)
	}
	if cfg.MaxConns == 0 || cfg.MaxConns > 20 {
		cfg.MaxConns = 20 // keep well below max_connections; PgBouncer sits in front in prod
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// Beginner is satisfied by *pgxpool.Pool and *pgx.Conn.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// InTx runs fn inside a transaction, committing on nil and rolling back on
// error or panic. Deferred constraint triggers (the ledger balance check) fire
// at Commit, so a commit error is a real business error and is returned as is.
func InTx(ctx context.Context, db Beginner, fn func(tx pgx.Tx) error) (err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// InTxRet is InTx for callbacks that return a value.
func InTxRet[T any](ctx context.Context, db Beginner, fn func(tx pgx.Tx) (T, error)) (T, error) {
	var out T
	err := InTx(ctx, db, func(tx pgx.Tx) error {
		var err error
		out, err = fn(tx)
		return err
	})
	return out, err
}

// MapNotFound turns pgx.ErrNoRows into ErrNotFound and leaves other errors alone.
func MapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// IsNotFound reports whether err is a missing-row error from either layer.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows)
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// IsUniqueViolation reports a 23505 error (duplicate key).
func IsUniqueViolation(err error) bool { return pgCode(err) == "23505" }

// IsCheckViolation reports a 23514 error, which is also what the address /
// tx-hash format triggers raise.
func IsCheckViolation(err error) bool { return pgCode(err) == "23514" }

// IsForeignKeyViolation reports a 23503 error.
func IsForeignKeyViolation(err error) bool { return pgCode(err) == "23503" }

// ConstraintName returns the violated constraint, if the error carries one.
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// InTxRet2 is InTx for callbacks that return two values.
func InTxRet2[A, B any](ctx context.Context, db Beginner, fn func(tx pgx.Tx) (A, B, error)) (A, B, error) {
	var a A
	var b B
	err := InTx(ctx, db, func(tx pgx.Tx) error {
		var err error
		a, b, err = fn(tx)
		return err
	})
	return a, b, err
}
