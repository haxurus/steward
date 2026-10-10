#!/bin/sh
# One-shot job run before Steward starts: makes sure the unprivileged "steward" role
# and its database exist, and (re)applies the role password from its secret file so
# rotating the password only takes a deploy. YAGPDB creates its own tables at
# startup, which works because the role owns the database.
set -eu

PGPASSWORD=$(cat /run/secrets/postgres_admin_password)
export PGPASSWORD

[ -s /run/secrets/steward_db_password ] || { echo 'db-init: steward database password is empty' >&2; exit 1; }

psql -v ON_ERROR_STOP=1 --no-psqlrc <<'SQL'
\set app_password `cat /run/secrets/steward_db_password`
SELECT 'CREATE ROLE steward LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'steward')\gexec
ALTER ROLE steward WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD :'app_password';
SELECT 'CREATE DATABASE steward OWNER steward' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'steward')\gexec
ALTER DATABASE steward OWNER TO steward;
REVOKE ALL ON DATABASE steward FROM PUBLIC;
SQL

echo 'db-init: role and database ready'
