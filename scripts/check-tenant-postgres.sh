#!/usr/bin/env bash
# The outside check of a tenant's Postgres database: the registry creates a
# tenant on Postgres, as the server will, in a schema of its own, with a
# Postgres master in another, and the tenant suite's sample is written into
# it; then psql and pg_dump, which are Postgres's own and not Magnetar's, read
# the tenant's schema and the master's.
#
# It lists the tables and rows, checks every constraint and index, reads
# times and audit details with Postgres's own functions, reads the tenant's
# row and its state change from the master, checks the schema records the
# tenant's id, and searches both dumps for the login's password, which must
# be absent.
#
# The driver is given MAGNETAR_TEST_POSTGRES, the URL of a scratch database
# the login may make schemas in. PSQL and PG_DUMP are the commands that run
# psql and pg_dump, which may be the ones in a container, as for
# scripts/check-master-postgres.sh (which says why); PSQL_URL is the URL as
# psql sees it.
#
#   MAGNETAR_TEST_POSTGRES=postgres://u:p@127.0.0.1:5432/db scripts/check-tenant-postgres.sh
#
# Run from the repository's root. Exits non-zero on the first failed check.
# Nothing here prints a URL: it holds the password.
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
chmod 700 "$dir"
schema="magnetar_check_t_$(date +%s)_$$"
mschema="${schema}_master"

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
	q "DROP SCHEMA IF EXISTS \"$schema\" CASCADE; DROP SCHEMA IF EXISTS \"$mschema\" CASCADE" > /dev/null 2>&1 || true
	rm -rf "$dir"
}
trap cleanup EXIT

echo "psql: $(q 'SHOW server_version')"

MAGNETAR_SAMPLE_SCHEMA="$schema" MAGNETAR_SAMPLE_DIR="$dir" "$GO" test -count=1 -run '^TestWriteTenantSample$' ./internal/driver/postgres

t="\"$schema\""
m="\"$mschema\""
echo
echo "== the tenant's tables"
want "tables" "audit schema_migrations settings tenant_identity" \
	"$(q "SELECT string_agg(table_name, ' ' ORDER BY table_name COLLATE \"C\") FROM information_schema.tables WHERE table_schema = '$schema'")"

show schema_migrations "SELECT version, name, encode(checksum, 'hex') AS checksum, applied FROM $t.schema_migrations"
show tenant_identity "SELECT * FROM $t.tenant_identity"
show settings "SELECT * FROM $t.settings ORDER BY name"
show audit "SELECT id, at, key_id, remote_addr, method, tenant_name, object, outcome, reason, details FROM $t.audit ORDER BY at, id"
echo

want "schema_migrations rows" 1 "$(q "SELECT count(*) FROM $t.schema_migrations")"
want "the schema records the tenant's id" sample-tenant "$(q "SELECT id FROM $t.tenant_identity")"
want "settings rows" 2 "$(q "SELECT count(*) FROM $t.settings")"
want "audit rows" 5 "$(q "SELECT count(*) FROM $t.audit")"
want "constraints that are not valid" 0 \
	"$(q "SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = '$schema' AND NOT c.convalidated")"
want "the audit indexes" "audit_by_key audit_by_outcome audit_by_tenant audit_by_time audit_pkey" \
	"$(q "SELECT string_agg(indexname, ' ' ORDER BY indexname COLLATE \"C\") FROM pg_indexes WHERE schemaname = '$schema' AND tablename = 'audit'")"
want "columns with the C collation" 5 \
	"$(q "SELECT count(*) FROM information_schema.columns WHERE table_schema = '$schema' AND collation_name = 'C'")"
want "at is timestamptz and details jsonb" "timestamp with time zone jsonb" \
	"$(q "SELECT (SELECT data_type FROM information_schema.columns WHERE table_schema = '$schema' AND table_name = 'audit' AND column_name = 'at') || ' ' || (SELECT data_type FROM information_schema.columns WHERE table_schema = '$schema' AND table_name = 'audit' AND column_name = 'details')")"
want "the override set twice keeps the second value" 90 "$(q "SELECT value FROM $t.settings WHERE name = 'reception.quiet_seconds'")"
want "a Unicode value, as UTF-8" "Zoë-山田" "$(q "SELECT value FROM $t.settings WHERE name = 'label.prefix'")"
want "every audit entry's details are a JSON object" 5 "$(q "SELECT count(*) FROM $t.audit WHERE jsonb_typeof(details) = 'object'")"
want "the refusal's entry" "refused Bad Name" "$(q "SELECT outcome || ' ' || object FROM $t.audit WHERE outcome <> 'done'")"
want "an audit time, with its microseconds, in UTC" "2026-11-18 09:30:05.123456" \
	"$(q "SELECT to_char(max(at) AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') FROM $t.audit")"

# The master: the tenant's row is ready, its address is sealed (the plaintext
# is a URL with a password, and neither is in the row), and the system
# recorded the change from upgrading to ready.
echo
echo "== the master's row and state change"
show tenants "SELECT id, name, driver, length(address) AS sealed_bytes, state, state_reason FROM $m.tenants"
want "the tenant's state in the master" "sample postgres ready" "$(q "SELECT name || ' ' || driver || ' ' || state FROM $m.tenants")"
want "the state change, by the system" "system upgrading -> ready" \
	"$(q "SELECT details ->> 'actor' || ' ' || (details ->> 'old_state') || ' -> ' || (details ->> 'new_state') FROM $m.audit WHERE method = 'magnetar.registry/SetTenantState'")"

# The login's password is in neither dump, nor in the master's rows.
pw="$(printf '%s' "$MAGNETAR_TEST_POSTGRES" | sed -n 's|^[a-zA-Z]*://[^:@/]*:\([^@]*\)@.*|\1|p')"
tdump="$dir/tenant.sql"
mdump="$dir/master.sql"
# shellcheck disable=SC2086
$PG_DUMP --schema="$schema" "$PSQL_URL" > "$tdump"
# shellcheck disable=SC2086
$PG_DUMP --schema="$mschema" "$PSQL_URL" > "$mdump"
echo "dumps: $(wc -l < "$tdump" | tr -d ' ') lines of the tenant's, $(wc -l < "$mdump" | tr -d ' ') of the master's"
if [ -n "$pw" ]; then
	for d in "$tdump" "$mdump"; do
		if grep -qF "$pw" "$d"; then
			fail "the login's password is in $(basename "$d")"
		fi
	done
	echo "ok: the login's password is in neither dump"
else
	echo "skipped: the address holds no password to search for"
fi
grep -q "postgres://" "$mdump" && fail "a database address is in the master's dump in plain text"
echo "ok: no database address in plain text in the master's dump"

echo
echo "check-tenant-postgres: all checks passed"
