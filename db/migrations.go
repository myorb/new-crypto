// Package db holds the database schema as goose migrations (migrations/), the
// sqlc query sources (queries/) and the role bootstrap (roles.sql).
//
// The migrations are embedded so cmd/migrate can apply them from a single
// binary in any environment; sqlc reads the same files to generate
// internal/store.
package db

import "embed"

// Migrations contains db/migrations/*.sql in goose format.
//
//go:embed migrations/*.sql
var Migrations embed.FS
