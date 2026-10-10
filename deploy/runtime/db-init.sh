#!/bin/sh
# One-shot job run before YAGPDB starts: makes sure the unprivileged "yagpdb" role
# and its database exist, and (re)applies the role password from its secret file so
# rotating the password only takes a deploy. YAGPDB creates its own tables at
# startup, which works because the role owns the database.
set -eu

PGPASSWORD=$(cat /run/secrets/postgres_admin_password)
export PGPASSWORD

[ -s /run/secrets/yagpdb_db_password ] || { echo 'db-init: yagpdb database password is empty' >&2; exit 1; }

psql -v ON_ERROR_STOP=1 --no-psqlrc <<'SQL'
\set app_password `cat /run/secrets/yagpdb_db_password`
SELECT 'CREATE ROLE yagpdb LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'yagpdb')\gexec
ALTER ROLE yagpdb WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD :'app_password';
SELECT 'CREATE DATABASE yagpdb OWNER yagpdb' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'yagpdb')\gexec
ALTER DATABASE yagpdb OWNER TO yagpdb;
REVOKE ALL ON DATABASE yagpdb FROM PUBLIC;
SQL

echo 'db-init: role and database ready'
