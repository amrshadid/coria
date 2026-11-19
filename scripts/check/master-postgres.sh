#!/usr/bin/env bash
# The outside check of the Postgres master database: the contract suite's
# sample installation is written by the driver, then read with psql and
# pg_dump, which are Postgres's own and not Magnetar's.
#
# It shows the tables and their rows, checks that every constraint is valid
# and the grants' foreign keys hold, reads times and audit details with
# Postgres's own functions, and searches a dump for the sample key's secret,
# which must be absent, and for its SHA-256, which must be present.
#
# The driver is given MAGNETAR_TEST_POSTGRES, the URL of a scratch database
# the login may make a schema in. The sample goes in a schema of its own,
# dropped when the script ends. psql and pg_dump are run by the commands in
# PSQL and PG_DUMP, so a machine without them can use the ones in a
# container, which also keeps their version equal to the server's (pg_dump
# refuses a server newer than itself):
#
#   MAGNETAR_TEST_POSTGRES=postgres://u:p@127.0.0.1:5432/db scripts/check/master-postgres.sh
#   PSQL="docker exec -i some-postgres psql" PG_DUMP="docker exec -i some-postgres pg_dump" \
#     PSQL_URL=postgres://u:p@127.0.0.1:5432/db  scripts/check/master-postgres.sh
#
# PSQL_URL is the URL as the psql command sees it (inside a container the
# server's port is the container's own); it defaults to the one the driver
# is given. Run from the repository's root. Exits non-zero on the first
# failed check. Nothing here prints a URL: it holds the password.
set -euo pipefail

PSQL="${PSQL:-psql}"
PG_DUMP="${PG_DUMP:-pg_dump}"
GO="${GO:-go}"
: "${MAGNETAR_TEST_POSTGRES:?set MAGNETAR_TEST_POSTGRES to the URL of a scratch database}"
PSQL_URL="${PSQL_URL:-$MAGNETAR_TEST_POSTGRES}"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# want <what> <expected> <actual>
want() {
	if [ "$2" != "$3" ]; then
		fail "$1: got '$3', want '$2'"
	fi
	echo "ok: $1 = $3"
}

dir="$(mktemp -d)"
schema="magnetar_check_$(date +%s)_$$"

# q runs one statement and prints its result alone, with no alignment or
# headers; show prints it as a table. Neither shows the URL.
q() {
	# shellcheck disable=SC2086 # PSQL is a command and its arguments.
	$PSQL -X -q -A -t -v ON_ERROR_STOP=1 "$PSQL_URL" -c "$1"
}
show() {
	echo
	echo "== $1"
	# shellcheck disable=SC2086
	$PSQL -X -q -v ON_ERROR_STOP=1 "$PSQL_URL" -c "$2"
}

cleanup() {
	q "DROP SCHEMA IF EXISTS \"$schema\" CASCADE" > /dev/null 2>&1 || true
	rm -rf "$dir"
}
trap cleanup EXIT

echo "psql: $(q 'SHOW server_version')"

# The secret lives in one place, the Go constant the sample hands to the
# store only as a hash.
secret="$(sed -n 's/^const SampleSecret = "\([^"]*\)".*/\1/p' internal/master/mastertest/sample.go)"
[ -n "$secret" ] || fail "cannot read SampleSecret from internal/master/mastertest/sample.go"

MAGNETAR_SAMPLE_SCHEMA="$schema" MAGNETAR_SAMPLE_DIR="$dir" "$GO" test -count=1 -run '^TestWriteSample$' ./internal/driver/postgres
hash="$(tr -d '\n' < "$dir/hash.hex")"
[ -n "$hash" ] || fail "the sample's hash was not written"

s="\"$schema\""
echo
echo "== tables"
want "tables" "api_keys audit grants schema_migrations settings tenants" \
	"$(q "SELECT string_agg(table_name, ' ' ORDER BY table_name COLLATE \"C\") FROM information_schema.tables WHERE table_schema = '$schema'")"

show schema_migrations "SELECT version, name, encode(checksum, 'hex') AS checksum, applied FROM $s.schema_migrations"
show tenants "SELECT id, name, driver, encode(address, 'hex') AS address, state, state_reason, created FROM $s.tenants ORDER BY name"
show api_keys "SELECT id, encode(hash, 'hex') AS hash, holder, created, expires, last_used, revoked FROM $s.api_keys ORDER BY id"
show grants "SELECT * FROM $s.grants ORDER BY key_id, scope"
show settings "SELECT * FROM $s.settings ORDER BY name"
show audit "SELECT id, at, key_id, local_user, remote_addr, method, tenant_name, outcome, reason, details FROM $s.audit ORDER BY at, id"
echo

want "schema_migrations rows" 1 "$(q "SELECT count(*) FROM $s.schema_migrations")"
want "tenants rows" 2 "$(q "SELECT count(*) FROM $s.tenants")"
want "api_keys rows" 3 "$(q "SELECT count(*) FROM $s.api_keys")"
want "grants rows" 2 "$(q "SELECT count(*) FROM $s.grants")"
want "settings rows" 2 "$(q "SELECT count(*) FROM $s.settings")"
want "audit rows" 12 "$(q "SELECT count(*) FROM $s.audit")"

# Postgres's own view of the schema's integrity: every constraint is valid,
# and a join finds no grant that points at nothing.
want "constraints that are not valid" 0 \
	"$(q "SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = '$schema' AND NOT c.convalidated")"
want "grants with no key" 0 "$(q "SELECT count(*) FROM $s.grants g LEFT JOIN $s.api_keys k ON k.id = g.key_id WHERE k.id IS NULL")"
want "grants on a tenant that is gone" 0 \
	"$(q "SELECT count(*) FROM $s.grants g LEFT JOIN $s.tenants t ON t.id = g.tenant_id WHERE g.tenant_id IS NOT NULL AND t.id IS NULL")"
want "time columns are timestamptz" "api_keys.created audit.at schema_migrations.applied tenants.created" \
	"$(q "SELECT string_agg(table_name || '.' || column_name, ' ' ORDER BY table_name COLLATE \"C\", column_name COLLATE \"C\") FROM information_schema.columns WHERE table_schema = '$schema' AND data_type = 'timestamp with time zone' AND column_name IN ('created', 'at', 'applied') AND is_nullable = 'NO'")"
want "details is jsonb" jsonb "$(q "SELECT data_type FROM information_schema.columns WHERE table_schema = '$schema' AND table_name = 'audit' AND column_name = 'details'")"
# The columns that are compared or ordered carry the byte-order collation.
want "columns with the C collation" 10 \
	"$(q "SELECT count(*) FROM information_schema.columns WHERE table_schema = '$schema' AND collation_name = 'C'")"

# Times keep their microseconds and are read here in UTC, as the sample's T0,
# 2026-11-18 09:30:00.123456.
want "a tenant's creation time" "2026-11-18 09:30:00.123456" \
	"$(q "SELECT to_char(created AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') FROM $s.tenants WHERE name = 'lab'")"
want "the revoked key's revocation time" "2026-11-18 10:30:00.123456" \
	"$(q "SELECT to_char(revoked AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') FROM $s.api_keys WHERE id = 'SampleKey003'")"
want "a Unicode holder, as UTF-8" "Zoë 山田 🧠" "$(q "SELECT holder FROM $s.api_keys WHERE id = 'SampleKey002'")"

# Grants: one per key and scope, and setting one again replaced its role in
# one step, never leaving the key without one; the audit entry of that change
# says the old and the new role.
want "the replaced grant's role" writer "$(q "SELECT role FROM $s.grants WHERE key_id = 'SampleKey002'")"
want "every audit entry's details are a JSON object" 12 "$(q "SELECT count(*) FROM $s.audit WHERE jsonb_typeof(details) = 'object'")"
want "the replacement's audit entry" "reader -> writer" \
	"$(q "SELECT details ->> 'old_role' || ' -> ' || (details ->> 'new_role') FROM $s.audit WHERE method = 'SetGrant' ORDER BY at DESC LIMIT 1")"
want "an entry for a tenant that has no row" "never-existed refused" \
	"$(q "SELECT tenant_name || ' ' || outcome FROM $s.audit WHERE tenant_name NOT IN (SELECT name FROM $s.tenants) AND tenant_name <> ''")"

# The key's secret is nowhere, and its hash is where the key is. pg_dump
# writes a bytea in hex, so the hash is searched for in hex.
dump="$dir/dump.sql"
# shellcheck disable=SC2086
$PG_DUMP --schema="$schema" "$PSQL_URL" > "$dump"
echo "dump: $(wc -l < "$dump" | tr -d ' ') lines"
if grep -qF "$secret" "$dump"; then
	fail "the sample key's secret is in the dump"
fi
echo "ok: the sample key's secret is not in the dump"
grep -qiF "$hash" "$dump" || fail "the sample key's hash is not in the dump"
echo "ok: the sample key's hash is in the dump"
want "the key row holding the hash" SampleKey001 "$(q "SELECT id FROM $s.api_keys WHERE encode(hash, 'hex') = '$hash'")"
want "rows of any table holding the secret" 0 \
	"$(q "SELECT count(*) FROM (SELECT t::text AS r FROM $s.tenants t UNION ALL SELECT k::text FROM $s.api_keys k UNION ALL SELECT g::text FROM $s.grants g UNION ALL SELECT x::text FROM $s.settings x UNION ALL SELECT a::text FROM $s.audit a) rows WHERE r LIKE '%$secret%'")"

echo
echo "check-master-postgres: all checks passed"
