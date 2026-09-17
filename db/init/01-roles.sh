#!/bin/bash
# Runs once, when the Postgres data volume is first created
# (docker-entrypoint-initdb.d). Creates the application roles from
# /db/roles.sql using the passwords from the environment. Migrations are NOT
# applied here - run `task db:migrate` (or `go run ./cmd/migrate up`).
set -euo pipefail

: "${APP_DB_PASSWORD:?APP_DB_PASSWORD is not set - see .env.example}"
: "${MIGRATOR_DB_PASSWORD:?MIGRATOR_DB_PASSWORD is not set - see .env.example}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
     -v dbname="$POSTGRES_DB" \
     -v app_password="$APP_DB_PASSWORD" \
     -v migrator_password="$MIGRATOR_DB_PASSWORD" \
     -f /db/roles.sql

echo "roles templ_app_migrator and templ_app are ready"
