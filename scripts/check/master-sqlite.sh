#!/usr/bin/env bash
# The outside check of the SQLite master database: the contract suite's
# sample installation is written by the driver, then read with the sqlite3
# command line tool, which is SQLite's own and not Magnetar's.
#
# It shows the tables and their rows, runs SQLite's integrity and foreign key
# checks, reads times and audit details with SQLite's own functions, and
# searches a dump and the raw files for the sample key's secret, which must
# be absent, and for its SHA-256, which must be present. Last, it reads the
# database of a process killed while making changes that share commits.
#
#   scripts/check/master-sqlite.sh            uses sqlite3 and go from PATH
#   SQLITE3=/path/to/sqlite3 GO=go1.27 scripts/check/master-sqlite.sh
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

echo "sqlite3 $("$SQLITE3" --version | cut -d' ' -f1)"

# The secret lives in one place, the Go constant the sample hands to the
# store only as a hash.
secret="$(sed -n 's/^const SampleSecret = "\([^"]*\)".*/\1/p' internal/master/mastertest/sample.go)"
[ -n "$secret" ] || fail "cannot read SampleSecret from internal/master/mastertest/sample.go"

MAGNETAR_SAMPLE_DIR="$dir" "$GO" test -count=1 -run '^TestWriteSample$' ./internal/driver/sqlite
db="$dir/master.db"
hash="$(tr -d '\n' < "$dir/hash.hex")"
[ -f "$db" ] || fail "the sample database was not written"

q() {
	"$SQLITE3" -batch -noheader "$db" "$1"
}

echo
echo "== tables"
"$SQLITE3" -batch "$db" '.tables'
want "tables" "api_keys audit grants schema_migrations settings tenants" \
	"$(q "SELECT group_concat(name, ' ') FROM (SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name)")"

# Every row of every table, with byte columns as hex so a terminal can show
# them.
show() {
	echo
	echo "== $1"
	"$SQLITE3" -batch -header -column "$db" "$2"
}
show schema_migrations "SELECT version, name, hex(checksum) AS checksum, applied FROM schema_migrations"
show tenants "SELECT id, name, driver, hex(address) AS address, state, state_reason, created FROM tenants"
show api_keys "SELECT id, hex(hash) AS hash, holder, created, expires, last_used, revoked FROM api_keys"
show grants "SELECT * FROM grants"
show settings "SELECT * FROM settings"
show audit "SELECT * FROM audit"
echo

want "schema_migrations rows" 1 "$(q 'SELECT count(*) FROM schema_migrations')"
want "tenants rows" 2 "$(q 'SELECT count(*) FROM tenants')"
want "api_keys rows" 3 "$(q 'SELECT count(*) FROM api_keys')"
want "grants rows" 2 "$(q 'SELECT count(*) FROM grants')"
want "settings rows" 2 "$(q 'SELECT count(*) FROM settings')"
want "audit rows" 12 "$(q 'SELECT count(*) FROM audit')"

want "integrity_check" ok "$(q 'PRAGMA integrity_check')"
want "foreign_key_check (empty is clean)" "" "$(q 'PRAGMA foreign_key_check')"
want "journal_mode" wal "$(q 'PRAGMA journal_mode')"
want "every table is STRICT" 6 "$(q "SELECT count(*) FROM pragma_table_list WHERE schema = 'main' AND strict = 1 AND name NOT LIKE 'sqlite_%'")"

# Times are microseconds since 1970 in UTC; SQLite's own date functions read
# them back as the sample's T0, 2026-11-18 09:30:00.123456.
want "a tenant's creation time" "2026-11-18 09:30:00 and 123456 microseconds" \
	"$(q "SELECT datetime(created / 1000000, 'unixepoch') || ' and ' || (created % 1000000) || ' microseconds' FROM tenants WHERE name = 'lab'")"
want "the revoked key's revocation time" "2026-11-18 10:30:00" \
	"$(q "SELECT datetime(revoked / 1000000, 'unixepoch') FROM api_keys WHERE id = 'SampleKey003'")"
want "a Unicode holder, as UTF-8" "Zoë 山田 🧠" "$(q "SELECT holder FROM api_keys WHERE id = 'SampleKey002'")"

# Grants: one per key and scope, and setting one again replaced its role in
# one step, never leaving the key without one; the audit entry of that change
# says the old and the new role.
want "the replaced grant's role" writer "$(q "SELECT role FROM grants WHERE key_id = 'SampleKey002'")"
want "every audit entry's details are a JSON object" 12 "$(q "SELECT count(*) FROM audit WHERE json_valid(details) AND json_type(details) = 'object'")"
want "the replacement's audit entry" "reader -> writer" \
	"$(q "SELECT json_extract(details, '\$.old_role') || ' -> ' || json_extract(details, '\$.new_role') FROM audit WHERE method = 'SetGrant' ORDER BY at DESC LIMIT 1")"
want "an entry for a tenant that has no row" "never-existed refused" \
	"$(q "SELECT tenant_name || ' ' || outcome FROM audit WHERE tenant_name NOT IN (SELECT name FROM tenants) AND tenant_name <> ''")"

# The key's secret is nowhere, and its hash is where the key is.
dump="$dir/dump.sql"
"$SQLITE3" -batch "$db" .dump > "$dump"
echo "dump: $(wc -l < "$dump" | tr -d ' ') lines"
if grep -qF "$secret" "$dump"; then
	fail "the sample key's secret is in the dump"
fi
echo "ok: the sample key's secret is not in the dump"
for f in "$db" "$db-wal"; do
	if [ -f "$f" ] && grep -qaF "$secret" "$f"; then
		fail "the sample key's secret is in $(basename "$f")"
	fi
done
echo "ok: the sample key's secret is not in the database's files"
grep -qiF "X'$hash'" "$dump" || fail "the sample key's hash is not in the dump"
want "the key row holding the hash" SampleKey001 "$(q "SELECT id FROM api_keys WHERE hex(hash) = upper('$hash')")"

# A crash in the middle of changes that share commits: a process making
# changes from many goroutines is killed, and this same sqlite3 tool checks
# the file's integrity and that every change the process was answered for is
# there with its audit entry and none is half there. Required here, so a
# missing tool fails instead of skipping.
echo
echo "== a process killed while making changes"
SQLITE3="$(command -v "$SQLITE3")" MAGNETAR_SQLITE3_REQUIRED=1 \
	"$GO" test -count=1 -v -run '^TestKillDuringChanges$' ./internal/driver/sqlite

echo
echo "check-master-sqlite: all checks passed"
