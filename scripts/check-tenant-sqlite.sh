#!/usr/bin/env bash
# The outside check of a tenant's SQLite database: the registry creates a
# tenant on SQLite in a fresh data directory, as the server will, and the
# tenant suite's sample is written into it; then the sqlite3 command line
# tool, which is SQLite's own and not Magnetar's, opens the tenant's file and
# the master's.
#
# It checks that the file is where the design puts it
# (<data-dir>/tenants/<name>.db) and private, lists its tables and rows, runs
# SQLite's integrity and foreign key checks, and reads the tenant's row and
# its state change from the master.
#
#   scripts/check-tenant-sqlite.sh            uses sqlite3 and go from PATH
#   SQLITE3=/path/to/sqlite3 GO=go1.27 scripts/check-tenant-sqlite.sh
#
# Run from the repository's root. Exits non-zero on the first failed check.
set -euo pipefail

SQLITE3="${SQLITE3:-sqlite3}"
GO="${GO:-go}"

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
trap 'rm -rf "$dir"' EXIT
# The key file's directory must be private to its owner (package secret).
chmod 700 "$dir"

echo "sqlite3 $("$SQLITE3" --version | cut -d' ' -f1)"

MAGNETAR_SAMPLE_DIR="$dir" "$GO" test -count=1 -run '^TestWriteTenantSample$' ./internal/registry
db="$dir/tenants/sample.db"
master="$dir/master.db"
[ -f "$db" ] || fail "the tenant's database is not at <data-dir>/tenants/sample.db"

# Modes as octal digits, read the way each system's stat spells it.
mode() {
	stat -c '%a' "$1" 2> /dev/null || stat -f '%Lp' "$1"
}
want "the tenants directory's mode" 700 "$(mode "$dir/tenants")"
want "the tenant file's mode" 600 "$(mode "$db")"

q() {
	"$SQLITE3" -batch -noheader "$db" "$1"
}

echo
echo "== tables"
"$SQLITE3" -batch "$db" '.tables'
want "tables" "audit catalog_totals event_marks events instances quarantine quarantine_totals received_files receivers reception_associations reception_steps receptions scans schema_migrations sessions settings sqlite_sequence storages subjects tenant_secrets work" \
	"$(q "SELECT group_concat(name, ' ') FROM (SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name)")"

show() {
	echo
	echo "== $1"
	"$SQLITE3" -batch -header -column "$db" "$2"
}
show schema_migrations "SELECT version, name, hex(checksum) AS checksum, applied FROM schema_migrations"
show settings "SELECT * FROM settings"
show audit "SELECT * FROM audit"
echo

want "schema_migrations rows" 5 "$(q 'SELECT count(*) FROM schema_migrations')"
want "settings rows" 2 "$(q 'SELECT count(*) FROM settings')"
want "audit rows" 5 "$(q 'SELECT count(*) FROM audit')"
want "integrity_check" ok "$(q 'PRAGMA integrity_check')"
want "foreign_key_check (empty is clean)" "" "$(q 'PRAGMA foreign_key_check')"
want "journal_mode" wal "$(q 'PRAGMA journal_mode')"
want "tables that are not STRICT" 0 "$(q "SELECT count(*) FROM pragma_table_list WHERE schema = 'main' AND strict = 0 AND type = 'table' AND name NOT LIKE 'sqlite_%'")"
want "the override set twice keeps the second value" 90 "$(q "SELECT value FROM settings WHERE name = 'reception.quiet_seconds'")"
want "a Unicode value, as UTF-8" "Zoë-山田" "$(q "SELECT value FROM settings WHERE name = 'label.prefix'")"
want "every audit entry's details are a JSON object" 5 "$(q "SELECT count(*) FROM audit WHERE json_valid(details) AND json_type(details) = 'object'")"
want "the refusal's entry" "refused Bad Name" "$(q "SELECT outcome || ' ' || object FROM audit WHERE outcome <> 'done'")"
want "an audit time, read by SQLite" "2026-11-18 09:30:05" "$(q "SELECT datetime(max(at) / 1000000, 'unixepoch') FROM audit")"

# The master: the tenant's row is ready, its address is sealed (not the
# empty plaintext a SQLite tenant's address is), and the system recorded the
# change from upgrading to ready.
echo
echo "== the master's row and state change"
"$SQLITE3" -batch -header -column "$master" "SELECT id, name, driver, length(address) AS sealed_bytes, state, state_reason FROM tenants"
mq() {
	"$SQLITE3" -batch -noheader "$master" "$1"
}
want "the tenant's state in the master" "sample sqlite ready" "$(mq "SELECT name || ' ' || driver || ' ' || state FROM tenants")"
want "the sealed address's length (format, version, nonce, tag)" 33 "$(mq "SELECT length(address) FROM tenants")"
# The creation's audit trail, read by the request's id: a system entry
# written with the row (the creation started), which names the request's
# entry, and the request's own entry, written once with the change that made
# the tenant ready, with the state before and after.
echo
echo "== the creation's audit entries, by request id"
"$SQLITE3" -batch -header -column "$master" "SELECT id, method, outcome, request_id, details FROM audit WHERE request_id = 'sample-request' ORDER BY at, id"
want "entries of the creation's request" 2 "$(mq "SELECT count(*) FROM audit WHERE request_id = 'sample-request'")"
want "the creation started, by the system" "done system sample-create sqlite" \
	"$(mq "SELECT outcome || ' ' || json_extract(details, '\$.actor') || ' ' || json_extract(details, '\$.request_entry') || ' ' || json_extract(details, '\$.database_type') FROM audit WHERE request_id = 'sample-request' AND method = 'magnetar.registry/CreateTenantStarted'")"
want "the request's own entry, with the change to ready" "sample-create done upgrading -> ready" \
	"$(mq "SELECT id || ' ' || outcome || ' ' || json_extract(details, '\$.old_state') || ' -> ' || json_extract(details, '\$.new_state') FROM audit WHERE request_id = 'sample-request' AND method = '/magnetar.v1.TenantService/CreateTenant'")"
want "no other state change was written" 0 "$(mq "SELECT count(*) FROM audit WHERE method = 'magnetar.registry/SetTenantState'")"
want "the master's integrity_check" ok "$(mq 'PRAGMA integrity_check')"

echo
echo "check-tenant-sqlite: all checks passed"
