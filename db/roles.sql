-- =============================================================================
--  Database roles - run ONCE per database by a superuser, before the first
--  migration. Idempotent: re-running updates passwords and settings only.
--
--    psql -v ON_ERROR_STOP=1 -U postgres -d templ_app \
--         -v dbname=templ_app \
--         -v app_password='...' -v migrator_password='...' \
--         -f db/roles.sql
--
--  In Docker, db/init/01-roles.sh runs this automatically when the data volume
--  is first created, taking the passwords from APP_DB_PASSWORD and
--  MIGRATOR_DB_PASSWORD.
--
--  Roles
--    templ_app_migrator  owns every schema object and runs migrations
--    templ_app           runtime role: DML only, on tables explicitly granted
--                        in the migrations. No DDL, no superuser, no BYPASSRLS.
-- =============================================================================

\set ON_ERROR_STOP on

-- ---- templ_app_migrator ------------------------------------------------------
SELECT format('CREATE ROLE templ_app_migrator LOGIN PASSWORD %L', :'migrator_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'templ_app_migrator') \gexec

ALTER ROLE templ_app_migrator WITH LOGIN PASSWORD :'migrator_password'
    NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
-- Fail fast instead of queueing behind application traffic while holding locks.
ALTER ROLE templ_app_migrator SET lock_timeout = '10s';

-- ---- templ_app ---------------------------------------------------------------
SELECT format('CREATE ROLE templ_app LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'templ_app') \gexec

ALTER ROLE templ_app WITH LOGIN PASSWORD :'app_password'
    NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE templ_app SET statement_timeout = '30s';                     -- no runaway queries
ALTER ROLE templ_app SET idle_in_transaction_session_timeout = '60s';   -- no forgotten open transactions
ALTER ROLE templ_app SET search_path = public;                          -- no schema shadowing via "$user"

-- ---- database & schema -------------------------------------------------------
-- The migrator owns the database (and therefore the public schema via
-- pg_database_owner), which lets it CREATE EXTENSION for trusted extensions.
ALTER DATABASE :"dbname" OWNER TO templ_app_migrator;
REVOKE ALL ON DATABASE :"dbname" FROM PUBLIC;
GRANT  CONNECT ON DATABASE :"dbname" TO templ_app;

REVOKE ALL   ON SCHEMA public FROM PUBLIC;
GRANT  USAGE ON SCHEMA public TO templ_app;

-- Identity columns / sequences created by future migrations are usable by the
-- app automatically. Table privileges are NOT defaulted: default deny, every
-- migration grants exactly what the app needs next to each table.
ALTER DEFAULT PRIVILEGES FOR ROLE templ_app_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO templ_app;
