#!/usr/bin/env bash
# The proof of this version: it shows, from outside, that the installed binary
# does what this version of Magnetar claims (an installation made and served,
# keys, grants and tenants on SQLite and Postgres, refusals, secrets kept
# sealed, crash safety, the audit log, the local socket, both command line
# forms and the static builds). It builds the real binary as "make build"
# does, starts it, and checks each claim with tools that are not Magnetar.
# Nothing here reaches into a Go package: it drives the installed thing from outside, with the binary's own commands,
# grpcurl, curl, openssl, sqlite3, psql, pg_dump and a Python grpcio client
# (scripts/prove.py).
#
# Each claim is a named check that prints one line, ok or FAIL with its
# evidence; a failed check does not stop the others. The run ends with a table
# of every check and the numbers it measured, and exits 1 when any check
# failed (or, with MAGNETAR_PROVE_STRICT=1, as in CI, when any was skipped).
#
#   1  install and run       6  a tenant stops alone
#   2  the first key         7  nothing half-made after a crash
#   3  tenants on both DBs   8  audited
#   4  refused everywhere    9  one server, one user
#   5  hashed and sealed    10  both forms of the command line
#                           11  the static builds and their sizes
#
# Needs: go, make, curl, openssl, docker (for Postgres), sqlite3 (SQLITE3=...),
# grpcurl (GRPCURL=...), and a Python (PYTHON=...) with
# scripts/requirements-check-server.txt installed. Bash 3.2 or later.
#
# Postgres: by default the script starts its own container (image
# MAGNETAR_PROVE_PG_IMAGE, postgres:18) and removes it at the end. With
# MAGNETAR_PROVE_PG_CONTAINER (a running container, such as CI's service
# container) and MAGNETAR_PROVE_PG_PORT (its port on 127.0.0.1) it uses that
# one, makes its own role and databases in it, and drops them at the end. In
# both cases the container is stopped and started again for section 6, and psql
# and pg_dump are run inside it, so their version is the server's.
#
# The second operating system user of section 9 is made with sudo on Linux; on a
# system where that cannot be done the check says so and is skipped there only.
#
#   make prove PYTHON=/path/to/venv/bin/python GRPCURL=grpcurl
#
# MAGNETAR_PROVE_BUILD=host builds only this machine's binary, for a quicker
# run while working on the script; check 11 is then skipped.
set -euo pipefail

if [ "${BASH_VERSINFO[0]}" -lt 3 ] || { [ "${BASH_VERSINFO[0]}" -eq 3 ] && [ "${BASH_VERSINFO[1]}" -lt 2 ]; }; then
	echo "prove.sh: needs bash 3.2 or later, this is $BASH_VERSION" >&2
	exit 2
fi

GO=${GO:-go}
PYTHON=${PYTHON:-python3}
GRPCURL=${GRPCURL:-grpcurl}
SQLITE3=${SQLITE3:-sqlite3}
DOCKER=${DOCKER:-docker}
PG_CONTAINER=${MAGNETAR_PROVE_PG_CONTAINER:-}
PG_PORT=${MAGNETAR_PROVE_PG_PORT:-}
PG_IMAGE=${MAGNETAR_PROVE_PG_IMAGE:-postgres:18}
STRICT=${MAGNETAR_PROVE_STRICT:-0}
BUILD=${MAGNETAR_PROVE_BUILD:-all}
# At least 20 kills, so that a creation interrupted at different moments is
# seen more than once; more give the adaptive delays
# more chances to land inside the creation.
CRASHES=${MAGNETAR_PROVE_CRASHES:-24}

root=$(cd "$(dirname "$0")/.." && pwd)
helper=$root/scripts/prove.py
started_at=$(date +%s)

for tool in "$GO" make curl openssl "$DOCKER" "$SQLITE3" "$GRPCURL" "$PYTHON"; do
	command -v "$tool" > /dev/null || { echo "prove.sh: $tool is not installed (see the header of this script)" >&2; exit 2; }
done
"$PYTHON" -c 'import grpc, prometheus_client' 2> /dev/null || {
	echo "prove.sh: $PYTHON lacks grpcio or prometheus-client; install scripts/requirements-check-server.txt" >&2
	exit 2
}

# A short directory: the local socket's path must fit the 104 bytes a Unix
# socket address holds on macOS, so the system's temporary directory (long on
# macOS) is not used.
work=$(mktemp -d /tmp/mgt-proof.XXXXXX)
chmod 700 "$work"
: > "$work/results.tsv"
: > "$work/ledger.tsv"
: > "$work/secrets"
: > "$work/search"
mkdir -m 700 "$work/keys"

# --- Cleanup: everything the run made goes, on success, failure or a signal.
pids=""
own_pg=""
pg_made=""
user_made=""
summary_done=0
cleanup() {
	local rc=$?
	set +e
	trap - EXIT INT TERM
	for p in $pids; do kill -TERM "$p" 2> /dev/null; done
	sleep 0.3
	for p in $pids; do kill -KILL "$p" 2> /dev/null; wait "$p" 2> /dev/null; done
	if [ "$summary_done" = 0 ]; then
		echo "prove.sh: stopped before the end; the checks run so far:"
		summary
		[ "$rc" != 0 ] || rc=1
	fi
	if [ -n "$own_pg" ]; then
		"$DOCKER" rm -f "$own_pg" > /dev/null 2>&1
	elif [ -n "$pg_made" ] && [ -n "$PG_CONTAINER" ]; then
		# A container that is not ours is left running as it was found, with
		# what this run made in it dropped.
		"$DOCKER" start "$PG_CONTAINER" > /dev/null 2>&1
		wait_pg > /dev/null 2>&1
		for db in $("$DOCKER" exec "$PG_CONTAINER" psql -U postgres -qtAc "select datname from pg_database where datname like 'mgt_proof%'" 2> /dev/null); do
			"$DOCKER" exec "$PG_CONTAINER" psql -U postgres -qc "drop database if exists $db with (force)" > /dev/null 2>&1
		done
		"$DOCKER" exec "$PG_CONTAINER" psql -U postgres -qc "drop role if exists mgt_proof" > /dev/null 2>&1
	fi
	if [ -n "$user_made" ]; then sudo -n userdel "$user_made" > /dev/null 2>&1; fi
	rm -rf "$work"
	exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
track() { pids="$pids $1"; }
untrack() {
	local p out=""
	for p in $pids; do [ "$p" = "$1" ] || out="$out $p"; done
	pids=$out
}

# --- Output: one line per check. A secret is never printed: every text goes
# through redact, which shows a secret as its length and a fingerprint.
sha256hex() {
	if command -v sha256sum > /dev/null; then sha256sum | cut -d ' ' -f 1; else shasum -a 256 | cut -d ' ' -f 1; fi
}
fingerprint() { printf '%s bytes, sha256 %s...' "${#1}" "$(printf %s "$1" | sha256hex | cut -c 1-8)"; }
redact() {
	local text=$1 s
	while IFS= read -r s; do
		[ -n "$s" ] || continue
		case $text in *"$s"*) text=${text//"$s"/<secret, $(fingerprint "$s")>} ;; esac
	done < "$work/secrets"
	printf '%s' "$text"
}
# secret VALUE [search]: remember a secret, for redaction, and for the search
# of section 5 when the second argument is "search".
secret() {
	printf '%s\n' "$1" >> "$work/secrets"
	if [ "${2:-}" = search ]; then printf '%s\n' "$1" >> "$work/search"; fi
}
result() {
	local name=$1 status=$2 detail ev
	detail=$(redact "$3")
	shift 3
	printf '%-4s %s: %s\n' "$status" "$name" "$detail"
	if [ "$status" = FAIL ]; then
		for ev in "$@"; do redact "$ev" | head -20 | sed 's/^/     | /'; echo; done
	fi
	printf '%s\t%s\t%s\n' "$name" "$status" "$(printf %s "$detail" | tr '\t\n' '  ')" >> "$work/results.tsv"
}
ok() { result "$1" ok "$2"; }
fail() {
	local n=$1 d=$2
	shift 2
	result "$n" FAIL "$d" "$@"
}
skip() { result "$1" skipped "$2"; }
section() { printf '\n== %s\n' "$1"; }

summary() {
	local n_ok n_fail n_skip
	n_ok=$(awk -F '\t' '$2 == "ok"' "$work/results.tsv" | wc -l | tr -d ' ')
	n_fail=$(awk -F '\t' '$2 == "FAIL"' "$work/results.tsv" | wc -l | tr -d ' ')
	n_skip=$(awk -F '\t' '$2 == "skipped"' "$work/results.tsv" | wc -l | tr -d ' ')
	echo
	echo "| Check | Result | Measured |"
	echo "|---|---|---|"
	awk -F '\t' '{ gsub(/\|/, "/", $3); printf "| %s | %s | %s |\n", $1, $2, $3 }' "$work/results.tsv"
	echo
	if [ -s "$work/sizes.txt" ]; then cat "$work/sizes.txt"; echo; fi
	echo "$n_ok ok, $n_fail FAIL, $n_skip skipped, in $(($(date +%s) - started_at)) s on $(uname -s) $(uname -m)"
}

mode_of() { stat -c '%a' "$1" 2> /dev/null || stat -f '%Lp' "$1"; }
free_port() { "$PYTHON" -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
now() { "$PYTHON" -c 'import time; print("%.6f" % time.time())'; }
urlescape() { "$PYTHON" -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }
json_get() { "$PYTHON" -c 'import json, sys; d = json.load(sys.stdin); print(eval("d" + sys.argv[1]))' "$1"; }
# median_max FILE: the median and the largest of the numbers in FILE, in ms.
median_max() { sort -n "$1" | awk '{ v[NR] = $1 * 1000 } END { printf "%.1f %.1f", v[int((NR + 1) / 2)], v[NR] }'; }

# --- The request ledger: every request made to the main server, with the
# request id from its response header when the client shows it, so section 8
# can find each one in the audit log, and the log can be reconciled against
# it the other way.
# The seventh field, for a request without an id, is the second it was sent
# in and the second it ended in, so it is matched only to an entry of that
# time.
ledger() { printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${1:--}" "$2" "$3" "$4" "$5" "$6" "${7:-}" >> "$work/ledger.tsv"; }
outcome_of() {
	case $1 in
		200) echo 'done' ;;
		401 | 403) echo refused ;;
		*) echo failed ;;
	esac
}

# api VIA KEY METHOD BODY [TENANT]: one call by curl, as JSON over HTTP, on
# TLS (VIA tls) or the local socket (VIA sock), with the key named KEY (a file
# under keys/) or none (""). Sets API_STATUS, API_TIME (s), API_BODY, API_RID.
# The key goes in a header file, never in curl's arguments.
api() {
	local via=$1 key=$2 method=$3 body=$4 tenant=${5:-*} out url key_id=""
	local args=(-sS -o "$work/api.body" -D "$work/api.head" -w '%{http_code} %{time_total}' -H 'Content-Type: application/json' -d "$body")
	if [ "$via" = tls ]; then
		url=https://127.0.0.1:$port/$method
		args+=(--cacert "$work/cert.pem")
	else
		url=http://local/$method
		args+=(--unix-socket "$sock")
		key_id=local
	fi
	if [ -n "$key" ]; then
		(umask 077 && printf 'Authorization: Bearer %s\n' "$(cat "$work/keys/$key")" > "$work/auth.h")
		args+=(-H "@$work/auth.h")
		key_id=$(cat "$work/keys/$key.id")
	fi
	: > "$work/api.body"
	: > "$work/api.head"
	out=$(curl "${args[@]}" "$url" 2> "$work/api.err") || out="000 0"
	rm -f "$work/auth.h"
	API_STATUS=${out%% *}
	API_TIME=${out#* }
	API_BODY=$(cat "$work/api.body")
	API_RID=$(tr -d '\r' < "$work/api.head" | awk -F ': ' 'tolower($1) == "magnetar-request-id" { print $2 }')
	if [ "$API_STATUS" != 000 ]; then
		if [ "$method" = grpc.health.v1.Health/Check ]; then
			ledger "$API_RID" "/$method" "$key_id" none "$tenant" "curl $via health"
		else
			ledger "$API_RID" "/$method" "$key_id" "$(outcome_of "$API_STATUS")" "$tenant" "curl $via"
		fi
	fi
}

# cli DATA METHOD TENANT ARGS...: the binary's own command on the local socket
# of the installation in DATA. Sets CLI_STATUS; stdout in cli.out, stderr in
# cli.err. When METHOD is not "", the request is entered in the ledger: the
# commands show a request id only on an error, so a success is entered
# without one and matched in the audit log by what it was.
cli() {
	local dir=$1 method=$2 tenant=$3 rid outcome
	shift 3
	CLI_STATUS=0
	CLI_T0=$(date +%s)
	env -u MAGNETAR_SERVER -u MAGNETAR_KEY MAGNETAR_DATA_DIR="$dir" "$bin" "$@" > "$work/cli.out" 2> "$work/cli.err" || CLI_STATUS=$?
	[ -n "$method" ] || return 0
	rid=$(sed -n 's/.*(request id \([a-z2-7]*\)).*/\1/p' "$work/cli.err" | tail -1)
	case $CLI_STATUS in
		0) outcome='done' ;;
		3) outcome=refused ;;
		*) outcome=failed ;;
	esac
	# A command that failed before it reached the server has no request id
	# and made no request.
	if [ "$CLI_STATUS" = 0 ] || [ -n "$rid" ]; then ledger "${rid:--}" "$method" local "$outcome" "$tenant" "cli $1 ${2:-}" "$CLI_T0 $(date +%s)"; fi
}

# start_server LOG DATA [serve flags...]: start serve in the background and
# wait until it answers the health check on its local socket. Sets
# SERVER_PID; returns 1 when it exits or does not answer within 15 s.
start_server() {
	local log=$1 dir=$2
	shift 2
	"$bin" serve --data-dir "$dir" "$@" >> "$log" 2>&1 &
	SERVER_PID=$!
	track "$SERVER_PID"
	wait_ready "$dir/run/magnetar.sock" "$SERVER_PID"
}
wait_ready() {
	local i
	for i in $(seq 1 300); do
		kill -0 "$2" 2> /dev/null || return 1
		curl -sf -o /dev/null --max-time 2 --unix-socket "$1" -H 'Content-Type: application/json' -d '{}' \
			http://local/grpc.health.v1.Health/Check 2> /dev/null && return 0
		sleep 0.05
	done
	return 1
}
# stop_server PID: SIGTERM, and the exit status in STOP_STATUS.
stop_server() {
	STOP_STATUS=0
	kill -TERM "$1" 2> /dev/null || true
	wait "$1" 2> /dev/null || STOP_STATUS=$?
	untrack "$1"
}

# --- Postgres.
psql_su() { "$DOCKER" exec -i "$PG_CONTAINER" psql -U postgres -v ON_ERROR_STOP=1 -qtA "$@"; }
wait_pg() {
	local i
	for i in $(seq 1 120); do
		# Over TCP inside the container: the image's first start runs a
		# server on its Unix socket only, then restarts it; TCP answers only
		# once the real server is up.
		"$DOCKER" exec "$PG_CONTAINER" pg_isready -q -h 127.0.0.1 -U postgres 2> /dev/null && return 0
		sleep 0.5
	done
	return 1
}

# ===========================================================================
section "11. the static builds (made first: the binary under test is one of them)"
host_os=$("$GO" env GOOS)
host_arch=$("$GO" env GOARCH)
mkdir -p "$work/bin"
if [ "$BUILD" = host ]; then
	CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w" -o "$work/bin/magnetar-$host_os-$host_arch" "$root/cmd/magnetar"
else
	if ! make -C "$root" --no-print-directory build BIN="$work/bin" GO="$GO" > "$work/build.log" 2>&1; then
		fail "11.1 a static build for every target of make build" "make build failed" "$(tail -20 "$work/build.log")"
		exit 1
	fi
fi
bin=$work/bin/magnetar-$host_os-$host_arch
[ -x "$bin" ] || { fail "11.1 a static build for every target of make build" "no binary for $host_os/$host_arch in $work/bin"; exit 1; }
echo "built $(ls "$work/bin" | tr '\n' ' ')"

# ===========================================================================
section "1. install and run"
out=$("$bin" version 2>&1) || true
case $out in
	"magnetar "*) ok "1.1 magnetar version answers" "$out" ;;
	*) fail "1.1 magnetar version answers" "unexpected output" "$out" ;;
esac

data=$work/d
sock=$data/run/magnetar.sock
mkdir -m 700 "$data"
if "$bin" init --data-dir "$data" > "$work/init.out" 2>&1 &&
	[ "$(mode_of "$data/key")" = 600 ] && [ "$(mode_of "$data/run")" = 700 ] && [ "$(mode_of "$data")" = 700 ] &&
	[ -f "$data/master.db" ] && [ -f "$data/installation" ] && [ -d "$data/tenants" ]; then
	again=0
	"$bin" init --data-dir "$data" > "$work/init2.out" 2>&1 || again=$?
	if [ "$again" != 0 ] && grep -q 'already holds an installation' "$work/init2.out"; then
		ok "1.2 init in an empty directory" "key file 0600, data dir and run/ 0700, master.db, installation id, tenants/; a second init is refused (exit $again)"
	else
		fail "1.2 init in an empty directory" "a second init was not refused" "$(cat "$work/init2.out")"
	fi
else
	fail "1.2 init in an empty directory" "init failed or made the wrong files" "$(cat "$work/init.out")" "$(ls -la "$data" 2>&1)"
	exit 1
fi

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
	-subj /CN=magnetar-proof -addext subjectAltName=IP:127.0.0.1 \
	-keyout "$work/key.pem" -out "$work/cert.pem" 2> /dev/null
port=$(free_port)
if start_server "$work/serve.log" "$data" --listen "127.0.0.1:$port" --tls-cert "$work/cert.pem" --tls-key "$work/key.pem" --log-level debug; then
	main_pid=$SERVER_PID
	cli "$data" /magnetar.v1.AccessService/WhoAmI "*" whoami
	whoami=$(head -1 "$work/cli.out")
	tls_subject=$(echo | openssl s_client -connect "127.0.0.1:$port" -CAfile "$work/cert.pem" -verify_return_error 2> /dev/null | grep -c 'Verify return code: 0 (ok)' || true)
	if [ "$CLI_STATUS" = 0 ] && [ "$tls_subject" -ge 1 ] && [ -S "$sock" ] && [ "$(mode_of "$data/run")" = 700 ]; then
		ok "1.3 serve on the local socket and on TLS on loopback" "socket in a 0700 run/ answers: '$whoami'; openssl s_client verifies 127.0.0.1:$port with the script's certificate"
	else
		fail "1.3 serve on the local socket and on TLS on loopback" "whoami exit $CLI_STATUS, TLS verified $tls_subject" "$(cat "$work/cli.err")" "$(tail -5 "$work/serve.log")"
	fi
else
	fail "1.3 serve on the local socket and on TLS on loopback" "the server did not start" "$(tail -20 "$work/serve.log")"
	exit 1
fi

# The standard health protocol, which grpcurl needs as a file: the contract
# does not include it, and the server offers no reflection.
mkdir -p "$work/health/grpc/health/v1"
cat > "$work/health/grpc/health/v1/health.proto" << 'EOF'
syntax = "proto3";
package grpc.health.v1;
message HealthCheckRequest { string service = 1; }
message HealthCheckResponse {
  enum ServingStatus { UNKNOWN = 0; SERVING = 1; NOT_SERVING = 2; SERVICE_UNKNOWN = 3; }
  ServingStatus status = 1;
}
service Health { rpc Check(HealthCheckRequest) returns (HealthCheckResponse); }
EOF
gout=$("$GRPCURL" -import-path "$work/health" -proto grpc/health/v1/health.proto -cacert "$work/cert.pem" -v -d '{}' "127.0.0.1:$port" grpc.health.v1.Health/Check 2>&1) || true
grid=$(printf '%s\n' "$gout" | awk -F ': ' 'tolower($1) == "magnetar-request-id" { print $2 }')
[ -z "$grid" ] || ledger "$grid" /grpc.health.v1.Health/Check "" none "*" "grpcurl health"
api tls "" grpc.health.v1.Health/Check '{}'
if printf '%s' "$gout" | grep -q '"status": "SERVING"' && [ "$API_STATUS" = 200 ] && [ "$API_BODY" = '{"status":"SERVING"}' ]; then
	ok "1.4 health answers without a key" "grpcurl over TLS: SERVING; curl over TLS: 200 $API_BODY"
else
	fail "1.4 health answers without a key" "grpcurl or curl did not get SERVING" "$gout" "$API_STATUS $API_BODY"
fi

# ===========================================================================
section "2. the first key, with no default password"
n=$("$SQLITE3" "$data/master.db" 'select count(*) from api_keys; select count(*) from grants;' | tr '\n' ' ')
if [ "$n" = "0 0 " ]; then
	ok "2.1 no key and no grant exist after init" "sqlite3 counts api_keys and grants in master.db: 0 and 0; the only way in is the local socket"
else
	fail "2.1 no key and no grant exist after init" "api_keys, grants: $n"
fi

# make_key NAME CLI-ARGS...: a key made by "magnetar key create" on the local
# socket; the key in keys/NAME (mode 0600), its id in keys/NAME.id.
make_key() {
	local name=$1
	shift
	cli "$data" /magnetar.v1.AccessService/CreateKey "*" key create --holder "proof $name" "$@"
	(umask 077 && head -1 "$work/cli.out" > "$work/keys/$name")
	local k
	k=$(cat "$work/keys/$name")
	printf '%s' "$k" | grep -Eq '^mgt_[1-9A-HJ-NP-Za-km-z]{12}_[1-9A-HJ-NP-Za-km-z]{40,}$' || return 1
	secret "$k" search
	secret "${k##*_}" search
	printf '%s' "$k" | cut -d _ -f 2 > "$work/keys/$name.id"
}
if make_key admin --no-expiry; then
	admin_id=$(cat "$work/keys/admin.id")
	cli "$data" /magnetar.v1.AccessService/SetGrant "*" grant set "$admin_id" --role admin --installation
	if [ "$CLI_STATUS" = 0 ]; then
		ok "2.2 the first key, made and granted on the local socket" "key create: $admin_id ($(fingerprint "$(cat "$work/keys/admin")")); grant set: admin on the installation"
	else
		fail "2.2 the first key, made and granted on the local socket" "grant set exit $CLI_STATUS" "$(cat "$work/cli.err")"
	fi
else
	fail "2.2 the first key, made and granted on the local socket" "key create exit $CLI_STATUS or the key is not mgt_<id>_<secret>" "$(cat "$work/cli.err")"
	exit 1
fi
"$PYTHON" "$helper" --work "$work" refusals --mode walk --proto "$root/api/proto" --not-served "$root/api/not-served.txt" \
	--addr "127.0.0.1:$port" --ca "$work/cert.pem" --grpcurl "$GRPCURL" || true

# ===========================================================================
section "3. tenants on both databases"
if [ -n "$PG_CONTAINER" ]; then
	[ -n "$PG_PORT" ] || { echo "prove.sh: MAGNETAR_PROVE_PG_CONTAINER needs MAGNETAR_PROVE_PG_PORT" >&2; exit 2; }
	pg_where="the container $PG_CONTAINER"
else
	PG_CONTAINER=mgt-proof-$(basename "$work" | cut -d . -f 2 | tr 'A-Z' 'a-z')
	supw=$(openssl rand -hex 16)
	secret "$supw"
	# A fixed host port: a published port chosen by Docker can change when
	# the container is started again in section 6. A port free here may still
	# be held where Docker runs (a virtual machine, on macOS), so a refused
	# port is tried again with another.
	own_pg=$PG_CONTAINER
	for attempt in 1 2 3; do
		PG_PORT=$(free_port)
		"$DOCKER" rm -f "$PG_CONTAINER" > /dev/null 2>&1 || true
		"$DOCKER" run -d --name "$PG_CONTAINER" -e POSTGRES_PASSWORD="$supw" -p "127.0.0.1:$PG_PORT:5432" "$PG_IMAGE" > /dev/null 2> "$work/docker.err" && break
		[ "$attempt" != 3 ] || { cat "$work/docker.err" >&2; exit 1; }
	done
	own_pg=$PG_CONTAINER
	pg_where="its own container from $PG_IMAGE"
fi
wait_pg || { fail "3.0 Postgres answers" "pg_isready never answered in $PG_CONTAINER"; exit 1; }
pg_version=$(psql_su -c 'show server_version' | cut -d ' ' -f 1)
# A password with the characters a URL must escape, so its escaped form
# differs and both forms are searched for in section 5.
pgpw="pr-$(openssl rand -hex 8)/@:+%$(openssl rand -hex 4)"
pgpw_esc=$(urlescape "$pgpw")
secret "$pgpw" search
secret "$pgpw_esc" search
secret "$(printf %s "$pgpw_esc" | tr 'A-F' 'a-f')" search
pg_made=1
psql_su -c "create role mgt_proof login password '$pgpw'" > /dev/null
psql_su -c "create database mgt_proof_main owner mgt_proof" > /dev/null
pg_dsn() { printf 'postgres://mgt_proof:%s@127.0.0.1:%s/%s?sslmode=disable' "$pgpw_esc" "$PG_PORT" "$1"; }
MGT_PROOF_DSN=$(pg_dsn mgt_proof_main)
secret "$MGT_PROOF_DSN" search
echo "Postgres $pg_version in $pg_where, on 127.0.0.1:$PG_PORT; a role and an empty database made for the tenant"

cli "$data" /magnetar.v1.TenantService/CreateTenant lite --json tenant create lite
lite_state=$(json_get '["tenant"]["state"]' < "$work/cli.out" 2> /dev/null || echo "exit $CLI_STATUS")
if [ "$lite_state" = TENANT_STATE_READY ] && [ -f "$data/tenants/lite.db" ]; then
	ok "3.1 a tenant on SQLite, ready" "tenant create lite: $lite_state, file tenants/lite.db mode $(mode_of "$data/tenants/lite.db")"
else
	fail "3.1 a tenant on SQLite, ready" "state $lite_state" "$(cat "$work/cli.out" "$work/cli.err")"
fi
export MGT_PROOF_DSN
cli "$data" /magnetar.v1.TenantService/CreateTenant pg --json tenant create pg --db-env MGT_PROOF_DSN
unset MGT_PROOF_DSN
pg_state=$(json_get '["tenant"]["state"]' < "$work/cli.out" 2> /dev/null || echo "exit $CLI_STATUS")
if [ "$pg_state" = TENANT_STATE_READY ]; then
	ok "3.2 a tenant on Postgres, ready" "tenant create pg --db-env MGT_PROOF_DSN (sslmode=disable to loopback): $pg_state"
else
	fail "3.2 a tenant on Postgres, ready" "state $pg_state" "$(cat "$work/cli.out" "$work/cli.err")"
fi

# An address to another machine must say how the connection is protected:
# the driver's default tries encryption and silently falls back to none.
MGT_PROOF_REMOTE="postgres://mgt_proof:$pgpw_esc@db.example.invalid:5432/mgt_proof_main"
secret "$MGT_PROOF_REMOTE" search
export MGT_PROOF_REMOTE
cli "$data" /magnetar.v1.TenantService/CreateTenant remote tenant create remote --db-env MGT_PROOF_REMOTE
remote_status=$CLI_STATUS
remote_err=$(cat "$work/cli.err")
unset MGT_PROOF_REMOTE
rows=$("$SQLITE3" "$data/master.db" "select count(*) from tenants where name = 'remote'")
files=$(ls "$data/tenants" | grep -c '^remote' || true)
cli "$data" /magnetar.v1.TenantService/ListTenants "*" --json tenant list
listed=$(grep -c '"name": "remote"' "$work/cli.out" || true)
if [ "$remote_status" = 2 ] && printf '%s' "$remote_err" | grep -q 'is not one the database type postgres takes' && [ "$rows" = 0 ] && [ "$files" = 0 ] && [ "$listed" = 0 ]; then
	ok "3.3 a remote address without sslmode is refused as invalid and leaves no tenant" "exit 2 (invalid): '$(printf '%s' "$remote_err" | sed 's/ (request id.*//')'; master rows 0, files 0, listed 0"
else
	fail "3.3 a remote address without sslmode is refused as invalid and leaves no tenant" "exit $remote_status, master rows $rows, files $files, listed $listed" "$remote_err"
fi

master_tables=$("$SQLITE3" "$data/master.db" "select name from sqlite_master where type = 'table' order by name" | tr '\n' ' ')
lite_tables=$("$SQLITE3" "$data/tenants/lite.db" "select name from sqlite_master where type = 'table' order by name" | tr '\n' ' ')
pg_tables=$(psql_su -d mgt_proof_main -c "select tablename from pg_tables where schemaname = 'public' order by tablename" | tr '\n' ' ')
pg_owner=$(psql_su -d mgt_proof_main -c "select distinct tableowner from pg_tables where schemaname = 'public'" | tr '\n' ' ')
# A Postgres tenant also keeps its tenant's id (tenant_identity), so that a
# database given to two tenants by mistake is refused; a SQLite tenant is a
# file of its own and has none. The id kept there must be the master's.
pg_identity=$(psql_su -d mgt_proof_main -c "select id from tenant_identity" 2> /dev/null || true)
master_pg_id=$("$SQLITE3" "$data/master.db" "select id from tenants where name = 'pg'")
want_master="api_keys audit grants schema_migrations settings tenants "
# A SQLite tenant also holds what a reception needs; a Postgres tenant gains
# the same tables when its driver does.
want_lite="audit catalog_totals event_marks events instances quarantine quarantine_totals received_files receivers reception_associations reception_steps receptions scans schema_migrations sessions settings sqlite_sequence storages subjects work "
want_pg="audit schema_migrations settings tenant_identity "
if [ "$master_tables" = "$want_master" ] && [ "$lite_tables" = "$want_lite" ] && [ "$pg_tables" = "$want_pg" ] &&
	[ "$pg_owner" = "mgt_proof " ] && [ -n "$pg_identity" ] && [ "$pg_identity" = "$master_pg_id" ]; then
	ok "3.4 sqlite3 and psql list each database's tables" "master (sqlite3): $master_tables; lite (sqlite3): $lite_tables; pg (psql): $pg_tables(owner mgt_proof; tenant_identity holds pg's id in the master)"
else
	fail "3.4 sqlite3 and psql list each database's tables" "master: $master_tables; lite: $lite_tables; pg: $pg_tables; pg owner $pg_owner; pg identity '$pg_identity', master's '$master_pg_id'"
fi

# ===========================================================================
section "4. refused everywhere, the same way"
make_key nogrant --no-expiry || fail "4.0 the keys of the refusals" "key create nogrant failed" "$(cat "$work/cli.err")"
make_key reader --no-expiry || fail "4.0 the keys of the refusals" "key create reader failed" "$(cat "$work/cli.err")"
cli "$data" /magnetar.v1.AccessService/SetGrant "*" grant set "$(cat "$work/keys/reader.id")" --role reader --tenant lite
make_key revoked --no-expiry || fail "4.0 the keys of the refusals" "key create revoked failed" "$(cat "$work/cli.err")"
cli "$data" /magnetar.v1.AccessService/RevokeKey "*" key revoke "$(cat "$work/keys/revoked.id")"
revoke_status=$CLI_STATUS
# The shortest expiry the command takes: its lengths are a whole number of
# s, m, h, d or w, so one second (the server takes any positive duration).
make_key expired --expires 1s || fail "4.0 the keys of the refusals" "key create --expires 1s failed" "$(cat "$work/cli.err")"
expired_made=$(date +%s)
cli "$data" /magnetar.v1.AccessService/GetKey "*" --json key show "$(cat "$work/keys/expired.id")"
life=$("$PYTHON" -c '
import json, sys, datetime
k = json.load(open(sys.argv[1]))["key"]
p = lambda s: datetime.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timestamp() + float("0" + s[19:].rstrip("Z"))
print("%.3f" % (p(k["expireTime"]) - p(k["createTime"])))' "$work/cli.out" 2> /dev/null || echo "?")
while [ $(($(date +%s) - expired_made)) -lt 2 ]; do sleep 0.2; done
"$PYTHON" -c '
import json, sys
keys = {}
for name in sys.argv[2:]:
    keys[name] = {"key": open("%s/keys/%s" % (sys.argv[1], name)).read().strip(), "id": open("%s/keys/%s.id" % (sys.argv[1], name)).read().strip()}
import os
fd = os.open(sys.argv[1] + "/keys.json", os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
os.write(fd, json.dumps(keys).encode())' "$work" admin nogrant reader revoked expired
if [ "$revoke_status" = 0 ] && [ "$life" = 1.000 ]; then
	ok "4.0 the keys of the refusals" "no grant; reader of lite only; revoked (key revoke exit 0); expired: --expires 1s, the shortest the command takes, expire minus create = ${life} s, used 2 s after it was made"
else
	fail "4.0 the keys of the refusals" "revoke exit $revoke_status, expired key lifetime $life s" "$(cat "$work/cli.err")"
fi
"$PYTHON" "$helper" --work "$work" refusals --mode matrix --proto "$root/api/proto" --not-served "$root/api/not-served.txt" \
	--addr "127.0.0.1:$port" --ca "$work/cert.pem" --grpcurl "$GRPCURL" --pg-tenant pg --missing-tenant nosuch \
	--data-dir "$data" --lite-tenant lite || true

# ===========================================================================
section "5. sealed: the server with another key file"
# A copy of the installation, its databases copied by sqlite3's online backup
# while the server runs, given the key file of another installation. Its run/
# holds the empty lock file a stopped server leaves, as an installation whose
# key file was swapped after it had been served would. Each tenant's own log
# is given an entry first (a read of its settings, by request id), so the
# copy can be asked for both tenants' data; the pause lets the batching
# audit writer put both entries in their logs before the copy is taken.
api sock "" magnetar.v1.SettingsService/ListSettings '{"scope":{"tenant":"lite"}}' lite
rid_lite=$API_RID
api sock "" magnetar.v1.SettingsService/ListSettings '{"scope":{"tenant":"pg"}}' pg
rid_pg=$API_RID
sleep 2
cli "$data" /magnetar.v1.AccessService/ListKeys "*" --json key list
main_keys=$(grep -c '"holder"' "$work/cli.out" || true)
copy=$work/c
mkdir -m 700 "$copy" "$copy/run" "$copy/tenants"
"$SQLITE3" "$data/master.db" ".backup '$copy/master.db'"
"$SQLITE3" "$data/tenants/lite.db" ".backup '$copy/tenants/lite.db'"
cp "$data/installation" "$copy/installation"
: > "$copy/run/serve.lock"
"$bin" init --data-dir "$work/o" > /dev/null
cp "$work/o/key" "$copy/key"
chmod 600 "$copy/key" "$copy/installation" "$copy/master.db" "$copy/tenants/lite.db" "$copy/run/serve.lock"
# tree_sums DIR: every entry under DIR with its type, mode and, for a file,
# the SHA-256 of its bytes, one line each, in a fixed order.
tree_sums() {
	local p
	(cd "$1" && find . | LC_ALL=C sort) | while IFS= read -r p; do
		if [ -f "$1/$p" ]; then
			printf '%s file %s %s\n' "$p" "$(mode_of "$1/$p")" "$(sha256hex < "$1/$p")"
		else
			printf '%s %s %s\n' "$p" "$(test -d "$1/$p" && echo dir || echo other)" "$(mode_of "$1/$p")"
		fi
	done
}
tree_sums "$copy" > "$work/copy.before"
cport=$(free_port)
refused=0
if cmp -s "$data/key" "$copy/key"; then refused="same key file"; fi
"$bin" serve --data-dir "$copy" --listen "127.0.0.1:$cport" --tls-cert "$work/cert.pem" --tls-key "$work/key.pem" > "$work/copy.log" 2>&1 &
cpid=$!
track "$cpid"
for i in $(seq 1 150); do kill -0 "$cpid" 2> /dev/null || break; sleep 0.1; done
# The port is tried before a server that did not exit is stopped, so a
# server that listened is seen listening.
port_status=0
curl -sS --max-time 3 -o /dev/null --cacert "$work/cert.pem" "https://127.0.0.1:$cport/grpc.health.v1.Health/Check" 2> /dev/null || port_status=$?
socket_made=no
[ -e "$copy/run/magnetar.sock" ] && socket_made=yes
if kill -0 "$cpid" 2> /dev/null; then
	kill -KILL "$cpid"
fi
wait "$cpid" 2> /dev/null || refused=$?
untrack "$cpid"
line=$(grep -v '^{' "$work/copy.log" | head -1 || true)
if [ "$refused" = 1 ] && printf '%s' "$line" | grep -q 'the key file is not the one this installation was made with; nothing was changed' &&
	[ "$socket_made" = no ] && [ "$port_status" = 7 ]; then
	ok "5.7 serve refuses another key file before it opens anything" "exit 1: '$line'; no socket made, the TLS port refuses connections (curl exit 7)"
else
	fail "5.7 serve refuses another key file before it opens anything" "exit $refused, socket made: $socket_made, curl on the port exit $port_status" "$(cat "$work/copy.log")"
fi
tree_sums "$copy" > "$work/copy.after"
if cmp -s "$work/copy.before" "$work/copy.after"; then
	ok "5.8 the refused start changed nothing in the data directory" "$(grep -c ' file ' "$work/copy.after") files and $(grep -c ' dir ' "$work/copy.after") directories: names, modes and SHA-256 of every file the same before and after"
else
	fail "5.8 the refused start changed nothing in the data directory" "the tree differs" "$(diff "$work/copy.before" "$work/copy.after")"
fi
# The installation's identity is its installation file and the master's
# copy of it; the key file only seals. The id must not be in the key file.
inst_id=$(tr -d '\n' < "$copy/installation")
"$SQLITE3" "$copy/master.db" .dump > "$work/master.backup.dump"
if [ -n "$inst_id" ] && ! grep -qF "$inst_id" "$data/key" && grep -qF "$inst_id" "$work/master.backup.dump"; then
	ok "5.9 the installation's identity is not in its key file" "the id in the installation file (${#inst_id} bytes) is in the master and not in the key file"
else
	fail "5.9 the installation's identity is not in its key file" "id ${#inst_id} bytes; in the key file: $(grep -cF "$inst_id" "$data/key"); in the master: $(grep -cF "$inst_id" "$work/master.backup.dump")"
fi
# The installation's own key file put back: the same copy starts, and both
# tenants are served. The Postgres tenant is the same database the main
# server uses; it is only read here.
cp "$data/key" "$copy/key"
chmod 600 "$copy/key"
if start_server "$work/copy.log" "$copy"; then
	copy_pid=$SERVER_PID
	cli "$copy" "" "" --json tenant list
	cp "$work/cli.out" "$work/copy-tenants.json"
	t_state() { "$PYTHON" -c 'import json, sys; t = [t for t in json.load(open(sys.argv[1]))["tenants"] if t["name"] == sys.argv[2]]; print(t[0]["state"] if t else "absent")' "$work/copy-tenants.json" "$1"; }
	pgs=$(t_state pg)
	lis=$(t_state lite)
	cli "$copy" "" "" setting list --tenant lite
	lite_cli=$CLI_STATUS
	cli "$copy" "" "" --json tenant show pg
	pg_show=$(json_get '["tenant"]["state"]' < "$work/cli.out" 2> /dev/null || echo "exit $CLI_STATUS")
	if [ "$pgs" = TENANT_STATE_READY ] && [ "$lis" = TENANT_STATE_READY ] && [ "$lite_cli" = 0 ] && [ "$pg_show" = TENANT_STATE_READY ]; then
		ok "5.10 with its own key file put back, the same copy starts and serves both tenants" "pg $pgs (tenant show: $pg_show), lite $lis (setting list --tenant lite exit 0)"
	else
		fail "5.10 with its own key file put back, the same copy starts and serves both tenants" "pg $pgs / $pg_show, lite $lis, setting list exit $lite_cli" "$(cat "$work/copy-tenants.json")" "$(tail -5 "$work/copy.log")"
	fi
	# The data the copy was made with, read back through it: the entry made
	# above in each tenant's own log, and every key. Reading the Postgres
	# tenant's log is itself recorded in that log, which is the main
	# server's too, so it is entered in the ledger like the main server's
	# requests.
	cli "$copy" "" "" --json audit list --tenant lite --limit 1000
	lite_has=$(grep -c "\"requestId\": \"$rid_lite\"" "$work/cli.out" || true)
	cli "$copy" /magnetar.v1.AuditService/ListAuditEntries pg --json audit list --tenant pg --limit 1000
	pg_has=$(grep -c "\"requestId\": \"$rid_pg\"" "$work/cli.out" || true)
	cli "$copy" "" "" --json key list
	copy_keys=$(grep -c '"holder"' "$work/cli.out" || true)
	if [ -n "$rid_lite" ] && [ -n "$rid_pg" ] && [ "$lite_has" = 1 ] && [ "$pg_has" = 1 ] && [ "$copy_keys" = "$main_keys" ] && [ "$main_keys" -ge 5 ]; then
		ok "5.11 the copy started with its own key file keeps both tenants' data" "the entry of request $rid_lite in lite's own log and of $rid_pg in pg's, read through the copy; $copy_keys keys, as many as the installation it was copied from"
	else
		fail "5.11 the copy started with its own key file keeps both tenants' data" "lite's entry found $lite_has times, pg's $pg_has (want 1 each); keys $copy_keys of $main_keys" "$(cat "$work/cli.err")"
	fi
	stop_server "$copy_pid"
else
	fail "5.10 with its own key file put back, the same copy starts and serves both tenants" "the copy did not start" "$(tail -20 "$work/copy.log")"
fi

# ===========================================================================
section "6. a tenant stops alone"
make_key tenants --no-expiry || fail "6.0 a key for both tenants" "key create failed"
cli "$data" /magnetar.v1.AccessService/SetGrant "*" grant set "$(cat "$work/keys/tenants.id")" --role reader --tenant lite
cli "$data" /magnetar.v1.AccessService/SetGrant "*" grant set "$(cat "$work/keys/tenants.id")" --role reader --tenant pg
lite_req='{"scope":{"tenant":"lite"}}'
pg_req='{"scope":{"tenant":"pg"}}'
# time_lite FILE N: N calls to the SQLite tenant over TLS, their times in FILE,
# each followed by a WhoAmI, which touches no tenant, its time in FILE.ref:
# the reference of the same moments, so a machine that is busy at that time
# is not taken for a tenant slowed by another. Prints how many calls to the
# tenant did not answer 200.
time_lite() {
	local i bad=0
	: > "$1"
	: > "$1.ref"
	for i in $(seq 1 "$2"); do
		api tls tenants magnetar.v1.SettingsService/ListSettings "$lite_req" lite
		echo "$API_TIME" >> "$1"
		[ "$API_STATUS" = 200 ] || bad=$((bad + 1))
		api tls tenants magnetar.v1.AccessService/WhoAmI '{}'
		echo "$API_TIME" >> "$1.ref"
	done
	echo "$bad"
}
tenant_state() {
	api tls admin magnetar.v1.TenantService/GetTenant "{\"name\":\"$1\"}" "$1"
	printf '%s' "$API_BODY" | json_get '["tenant"]["state"]' 2> /dev/null || echo "status $API_STATUS"
}
bad_before=$(time_lite "$work/lite.before" 30)
read -r before_med before_max <<< "$(median_max "$work/lite.before")"
api tls tenants magnetar.v1.SettingsService/ListSettings "$pg_req" pg
pg_before=$API_STATUS

# Stopped with "docker stop": the database's server goes away as on a crash
# of its machine, here and in CI (where it is the job's service container).
stopped_at=$(now)
"$DOCKER" stop -t 5 "$PG_CONTAINER" > /dev/null
state=""
for i in $(seq 1 120); do
	state=$(tenant_state pg)
	[ "$state" = TENANT_STATE_READY ] || break
	sleep 1
done
detected=$("$PYTHON" -c "import time; print('%.1f' % (time.time() - $stopped_at))")
if [ "$state" = TENANT_STATE_UNREACHABLE ]; then
	ok "6.1 the Postgres tenant becomes unreachable when its database stops" "docker stop of the container; GetTenant says $state after ${detected} s (the server checks every tenant each 30 s)"
else
	fail "6.1 the Postgres tenant becomes unreachable when its database stops" "after ${detected} s: $state" "$API_BODY"
fi
api tls tenants magnetar.v1.SettingsService/ListSettings "$pg_req" pg
pg_down_status=$API_STATUS
pg_down_time=$(awk -v t="$API_TIME" 'BEGIN { printf "%.1f", t * 1000 }')
pg_down_body=$API_BODY
if [ "$pg_down_status" = 503 ] && printf '%s' "$pg_down_body" | grep -q '"code":"unavailable"'; then
	ok "6.2 the stopped tenant is answered unavailable" "ListSettings on pg: 503 unavailable in $pg_down_time ms: $pg_down_body"
else
	fail "6.2 the stopped tenant is answered unavailable" "status $pg_down_status in $pg_down_time ms" "$pg_down_body"
fi
bad_during=$(time_lite "$work/lite.during" 30)
read -r during_med during_max <<< "$(median_max "$work/lite.during")"
read -r ref_med ref_max <<< "$(median_max "$work/lite.during.ref")"
# "Its usual time": the median no worse than twice the median before plus
# 10 ms, or than twice that of the calls touching no tenant made in between
# plus 10 ms (when the machine itself was slower then); and no call over
# half a second. All are far below the 30 s a call would wait on a dead
# database.
if [ "$bad_before" = 0 ] && [ "$bad_during" = 0 ] && [ "$pg_before" = 200 ] &&
	awk -v b="$before_med" -v r="$ref_med" -v d="$during_med" -v m="$during_max" 'BEGIN { exit !((d <= 2 * b + 10 || d <= 2 * r + 10) && m <= 500) }'; then
	ok "6.3 the SQLite tenant answers in its usual time while Postgres is down" "30 calls each over TLS: before median $before_med ms, max $before_max ms; during median $during_med ms, max $during_max ms (WhoAmI between them: median $ref_med ms)"
else
	fail "6.3 the SQLite tenant answers in its usual time while Postgres is down" "before median $before_med max $before_max ms ($bad_before not 200); during median $during_med max $during_max ms ($bad_during not 200); WhoAmI between them median $ref_med ms; pg before: $pg_before"
fi
"$DOCKER" start "$PG_CONTAINER" > /dev/null
wait_pg || true
back_at=$(now)
for i in $(seq 1 180); do
	state=$(tenant_state pg)
	[ "$state" != TENANT_STATE_READY ] || break
	sleep 1
done
back=$("$PYTHON" -c "import time; print('%.1f' % (time.time() - $back_at))")
api tls tenants magnetar.v1.SettingsService/ListSettings "$pg_req" pg
if [ "$state" = TENANT_STATE_READY ] && [ "$API_STATUS" = 200 ] && kill -0 "$main_pid" 2> /dev/null; then
	ok "6.4 the Postgres tenant is ready again without a restart" "$state ${back} s after Postgres answered; same server process ($main_pid); ListSettings on pg: 200"
else
	fail "6.4 the Postgres tenant is ready again without a restart" "after ${back} s: $state; ListSettings $API_STATUS" "$API_BODY"
fi

# ===========================================================================
section "7. nothing half-made after a crash"
# A separate installation, so the kills lose nothing the audit check of
# section 8 counts on (a killed server may lose the batched entries of its
# last moment, as the contract says).
crash=$work/k
"$bin" init --data-dir "$crash" > /dev/null
start_server "$work/crash.log" "$crash" || { fail "7.1 after each kill the tenant is whole or absent" "the server did not start" "$(tail -20 "$work/crash.log")"; }
crash_pid=$SERVER_PID
want_pg_tables=$(printf '%s' "$pg_tables" | wc -w | tr -d ' ')
: > "$work/crash.tsv"
lo=0
hi=200
odd=""
bad_integrity=""
for i in $(seq 1 "$CRASHES"); do
	db=mgt_proof_crash_$i
	psql_su -c "create database $db owner mgt_proof" > /dev/null
	MGT_PROOF_CRASH=$(pg_dsn "$db")
	secret "$MGT_PROOF_CRASH" search
	# The first kills are spread over 0 to 120 ms after the command starts;
	# the rest aim between the latest kill that came before the creation
	# and the earliest that came after it, where a kill lands inside it.
	if [ "$i" -le 8 ] || [ $((hi - lo)) -le 2 ]; then
		ms=$((RANDOM % 121))
	else
		ms=$((lo + 1 + RANDOM % (hi - lo - 1)))
	fi
	MGT_PROOF_CRASH=$MGT_PROOF_CRASH MAGNETAR_DATA_DIR=$crash "$bin" tenant create "c$i" --db-env MGT_PROOF_CRASH > /dev/null 2>&1 &
	cpid=$!
	sleep "$(printf '%d.%03d' $((ms / 1000)) $((ms % 1000)))"
	kill -KILL "$crash_pid" 2> /dev/null || true
	wait "$crash_pid" 2> /dev/null || true
	untrack "$crash_pid"
	wait "$cpid" 2> /dev/null || true
	row=$("$SQLITE3" "$crash/master.db" "select state from tenants where name = 'c$i'")
	integrity=$("$SQLITE3" "$crash/master.db" 'PRAGMA integrity_check' | tr '\n' ' ')
	tables=$(psql_su -d "$db" -c "select count(*) from pg_tables where schemaname = 'public'")
	case $row in
		"") class=before; [ "$ms" -gt "$lo" ] && lo=$ms ;;
		upgrading) class=mid ;;
		ready) class=after; [ "$ms" -lt "$hi" ] && hi=$ms ;;
		*) class="odd:$row" ;;
	esac
	[ "$integrity" = "ok " ] || bad_integrity="$bad_integrity kill $i: $integrity;"
	if start_server "$work/crash.log" "$crash"; then
		crash_pid=$SERVER_PID
		cli "$crash" "" "" --json tenant list
		after=$("$PYTHON" -c 'import json, sys; t = [t for t in json.load(open(sys.argv[1]))["tenants"] if t["name"] == sys.argv[2]]; print(t[0]["state"] if t else "absent")' "$work/cli.out" "c$i" 2> /dev/null || echo "unreadable")
		rows_after=$("$SQLITE3" "$crash/master.db" "select count(*) from tenants where name = 'c$i'")
		tables_after=$(psql_su -d "$db" -c "select count(*) from pg_tables where schemaname = 'public'")
		case $after in
			TENANT_STATE_READY)
				cli "$crash" "" "" setting list --tenant "c$i"
				[ "$CLI_STATUS" = 0 ] && [ "$tables_after" = "$want_pg_tables" ] || odd="$odd kill $i: ready but setting list exit $CLI_STATUS, $tables_after tables;"
				;;
			absent) [ "$rows_after" = 0 ] || odd="$odd kill $i: not listed but $rows_after master rows;" ;;
			*) odd="$odd kill $i: $after after the restart;" ;;
		esac
	else
		after="no restart"
		odd="$odd kill $i: the server did not start again: $(tail -3 "$work/crash.log" | tr '\n' ' ');"
		break
	fi
	case $class in odd:*) odd="$odd kill $i: master row state $row right after the kill;" ;; esac
	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$i" "$ms" "$class" "$tables" "$after" "$integrity" >> "$work/crash.tsv"
done
kills=$(wc -l < "$work/crash.tsv" | tr -d ' ')
n_before=$(awk -F '\t' '$3 == "before"' "$work/crash.tsv" | wc -l | tr -d ' ')
n_mid=$(awk -F '\t' '$3 == "mid"' "$work/crash.tsv" | wc -l | tr -d ' ')
n_after=$(awk -F '\t' '$3 == "after"' "$work/crash.tsv" | wc -l | tr -d ' ')
mid_delays=$(awk -F '\t' '$3 == "mid" { printf "%s%s", sep, $2; sep = "," }' "$work/crash.tsv")
mid_ready=$(awk -F '\t' '$3 == "mid" && $5 == "TENANT_STATE_READY"' "$work/crash.tsv" | wc -l | tr -d ' ')
mid_partial=$(awk -F '\t' '$3 == "mid" && $4 > 0' "$work/crash.tsv" | wc -l | tr -d ' ')
if [ -z "$odd" ] && [ "$kills" -ge 20 ]; then
	ok "7.1 after each kill -9 during tenant create the tenant is whole and ready, or absent" "$kills kills, $kills restarts: $(awk -F '\t' '$5 == "TENANT_STATE_READY"' "$work/crash.tsv" | wc -l | tr -d ' ') ready (setting list answers, $want_pg_tables tables), $(awk -F '\t' '$5 == "absent"' "$work/crash.tsv" | wc -l | tr -d ' ') absent (no master row), 0 other"
else
	fail "7.1 after each kill -9 during tenant create the tenant is whole and ready, or absent" "$kills kills" "$odd" "$(cat "$work/crash.tsv")"
fi
if [ -z "$bad_integrity" ] && [ "$kills" -ge 20 ]; then
	ok "7.2 the master passes PRAGMA integrity_check after every kill" "$kills of $kills: ok (sqlite3, before each restart)"
else
	fail "7.2 the master passes PRAGMA integrity_check after every kill" "$bad_integrity"
fi
if [ "$n_mid" -gt 0 ]; then
	ok "7.3 kills landed inside the creation" "$n_mid of $kills kills found the tenant's row upgrading (at $mid_delays ms; $mid_partial with some of its tables made), all $mid_ready finished ready by the next start; $n_before before the row, $n_after after it was ready"
else
	fail "7.3 kills landed inside the creation" "none of $kills kills found the row upgrading: $n_before before, $n_after after; the check proved nothing" "$(cat "$work/crash.tsv")"
fi
stop_server "$crash_pid"

# ===========================================================================
section "9. one server, one user"
second=0
"$bin" serve --data-dir "$data" > "$work/second.log" 2>&1 &
spid=$!
track "$spid"
for i in $(seq 1 100); do kill -0 "$spid" 2> /dev/null || break; sleep 0.1; done
if kill -0 "$spid" 2> /dev/null; then
	kill -KILL "$spid"
	second=running
fi
wait "$spid" 2> /dev/null || second=$?
untrack "$spid"
api sock "" magnetar.v1.AccessService/WhoAmI '{}'
# Refused for the reason that matters, the lock, and not for another one
# (such as the socket's address being in use) that a server without the lock
# might also meet.
if [ "$second" != 0 ] && [ "$second" != running ] && grep -q 'another magnetar serve is running' "$work/second.log" && [ "$API_STATUS" = 200 ]; then
	ok "9.1 a second serve on the same directory is refused" "exit $second: '$(head -1 "$work/second.log")'; the first still answers on its socket"
else
	fail "9.1 a second serve on the same directory is refused" "second serve: $second; first server's answer: $API_STATUS" "$(cat "$work/second.log")"
fi

if [ "$(uname -s)" = Linux ] && sudo -n true 2> /dev/null; then
	other=mgtproof$$
	sudo -n useradd --no-create-home --shell /usr/sbin/nologin "$other"
	user_made=$other
	other_uid=$(id -u "$other")
	try_other() {
		OTHER_STATUS=0
		sudo -n -u "$other" curl -sS --max-time 5 -o /dev/null -w '%{http_code}' --unix-socket "$sock" -H 'Content-Type: application/json' \
			-d '{}' http://local/magnetar.v1.AccessService/WhoAmI > "$work/other.out" 2> "$work/other.err" || OTHER_STATUS=$?
	}
	# As the server keeps its directories: the socket cannot be reached.
	try_other
	s1=$OTHER_STATUS
	e1=$(cat "$work/other.err")
	h1=$(cat "$work/other.out")
	# With every directory on the way and the socket opened to everyone, the
	# peer credential check is the only guard left: the connection must be
	# closed unanswered.
	chmod o+x "$work" "$data" "$data/run"
	chmod o+rw "$sock"
	try_other
	s2=$OTHER_STATUS
	e2=$(cat "$work/other.err")
	h2=$(cat "$work/other.out")
	chmod o-x "$work" "$data" "$data/run"
	chmod o-rw "$sock"
	sleep 0.2
	peer_log=$(grep -c "\"peer_user\":\"$other_uid\"" "$work/serve.log" || true)
	if [ "$s1" != 0 ] && [ "$s2" != 0 ] && [ "$h1" = 000 ] && [ "$h2" = 000 ] && [ "$peer_log" -ge 1 ]; then
		ok "9.2 another operating system user cannot use the local socket" "user $other (uid $other_uid): with the server's modes curl exits $s1 ($e1); with every mode opened curl exits $s2 ($e2), no HTTP answer, and the server logs the refused peer"
	else
		fail "9.2 another operating system user cannot use the local socket" "closed: exit $s1 http $h1; opened: exit $s2 http $h2; peer refusals in the log: $peer_log" "$e1" "$e2"
	fi
	sudo -n userdel "$other" && user_made=""
else
	skip "9.2 another operating system user cannot use the local socket" "needs Linux and sudo without a password to make a second user; this is $(uname -s) $(sudo -n true 2>&1 | head -1 | cut -c 1-60)"
fi

# ===========================================================================
section "10. both forms of the command line"
reader_id=$(cat "$work/keys/reader.id")
docs=""
jsonout() {
	local name=$1 method=$2 field=$3 tenant=$4
	shift 4
	cli "$data" "$method" "$tenant" --json "$@"
	cp "$work/cli.out" "$work/json-$name.json"
	docs="$docs $work/json-$name.json:$field"
}
jsonout tenants /magnetar.v1.TenantService/ListTenants tenants "*" tenant list
jsonout keys /magnetar.v1.AccessService/ListKeys keys "*" key list
jsonout grants-inst /magnetar.v1.AccessService/ListGrants grants "*" grant list --installation
jsonout grants-key /magnetar.v1.AccessService/ListGrants grants "*" grant list --key-id "$reader_id"
jsonout grants-tenant /magnetar.v1.AccessService/ListGrants grants "*" grant list --tenant lite
jsonout settings /magnetar.v1.SettingsService/ListSettings settings "*" setting list
jsonout settings-lite /magnetar.v1.SettingsService/ListSettings settings lite setting list --tenant lite
jsonout audit /magnetar.v1.AuditService/ListAuditEntries entries "*" audit list
jsonout audit-lite /magnetar.v1.AuditService/ListAuditEntries entries lite audit list --tenant lite
jsonout audit-pg /magnetar.v1.AuditService/ListAuditEntries entries pg audit list --tenant pg
# shellcheck disable=SC2086
"$PYTHON" "$helper" --work "$work" json $docs || true
# The interactive program's requests cannot be told by id; section 8 counts
# them by their time window, which these two seconds keep apart from the
# commands above (matched to the second).
sleep 2
"$PYTHON" "$helper" --work "$work" tui --binary "$bin" --data-dir "$data" --tenant lite --tenant pg || true

# ===========================================================================
section "8. audited"
curl -sS --unix-socket "$sock" http://local/metrics > "$work/metrics.txt"
"$PYTHON" "$helper" --work "$work" metrics --file "$work/metrics.txt" || true
cutoff=$(now)
stop_server "$main_pid"
stopped=$(grep '"msg":"server: stopped"' "$work/serve.log" | tail -1)
if [ "$STOP_STATUS" = 0 ] && printf '%s' "$stopped" | grep -q '"audit_failed":0' && printf '%s' "$stopped" | grep -q '"audit_dropped":0'; then
	ok "8.4 a clean stop exits 0 and writes every held audit entry" "SIGTERM: exit 0; its last line: $(printf '%s' "$stopped" | sed 's/.*"msg":"server: stopped",//; s/}$//')"
else
	fail "8.4 a clean stop exits 0 and writes every held audit entry" "exit $STOP_STATUS" "$stopped" "$(tail -5 "$work/serve.log")"
fi

# ===========================================================================
section "5. stored hashed and sealed"
# The databases and files at rest, after the clean stop.
search_n=$(wc -l < "$work/search" | tr -d ' ')
# found FILE...: the files in which any secret of the search list is found,
# searched as bytes; prints their names, never what matched.
found() { grep -laF -f "$work/search" "$@" 2> /dev/null || true; }
# where_found DUMP: which secret (by fingerprint) is on which line of a dump,
# and in which table's rows; never the line itself.
where_found() {
	local s
	while IFS= read -r s; do
		(umask 077 && printf '%s\n' "$s" > "$work/one")
		grep -naF -f "$work/one" "$1" | head -3 |
			awk -v fp="$(fingerprint "$s")" '{
				n = $0; sub(/:.*/, "", n); sub(/^[0-9]+:/, "")
				w = ""; if ($1 == "INSERT" || $1 == "COPY") { w = ($1 == "COPY") ? $2 : $3; sub(/\(.*/, "", w); w = ", rows of " w }
				print fp ": line " n w }'
	done < "$work/search"
	rm -f "$work/one"
}
"$SQLITE3" "$data/master.db" .dump > "$work/master.dump"
f=$(found "$work/master.dump")
if [ -z "$f" ] && [ -s "$work/master.dump" ]; then
	ok "5.1 no secret in the master's dump" "$search_n secrets (6 keys' secrets whole and alone, the Postgres password plain and URL-escaped, the addresses) absent from sqlite3 .dump ($(wc -c < "$work/master.dump" | tr -d ' ') bytes)"
else
	fail "5.1 no secret in the master's dump" "found in the dump" "$(where_found "$work/master.dump")"
fi
"$DOCKER" exec "$PG_CONTAINER" pg_dump -U postgres mgt_proof_main > "$work/tenant.dump"
f=$(found "$work/tenant.dump")
if [ -z "$f" ] && [ -s "$work/tenant.dump" ]; then
	ok "5.2 no secret in the Postgres tenant's pg_dump" "absent from pg_dump of mgt_proof_main ($(wc -c < "$work/tenant.dump" | tr -d ' ') bytes, its audit log included)"
else
	fail "5.2 no secret in the Postgres tenant's pg_dump" "found in the dump" "$(where_found "$work/tenant.dump")"
fi
nfiles=$(find "$data" -type f | wc -l | tr -d ' ')
nbytes=$(find "$data" -type f -exec cat {} + | wc -c | tr -d ' ')
f=$(found -r "$data")
if [ -z "$f" ]; then
	ok "5.3 no secret in any file under the data directory" "$nfiles files, $nbytes bytes, searched as bytes (grep -a): $(cd "$data" && find . -type f | sed 's|^\./||' | sort | tr '\n' ' ')"
else
	fail "5.3 no secret in any file under the data directory" "found in: $f"
fi
# The hash the master keeps for each key: the SHA-256 of its secret part.
missing_hash=""
for k in admin nogrant reader revoked expired tenants; do
	h=$(printf %s "$(cut -d _ -f 3 < "$work/keys/$k")" | sha256hex)
	grep -qi "X'$h'" "$work/master.dump" || missing_hash="$missing_hash $k"
done
if [ -z "$missing_hash" ]; then
	ok "5.4 each key's SHA-256 is in the master" "6 keys: the SHA-256 of each secret found as X'...' in the dump, and nothing else of it"
else
	fail "5.4 each key's SHA-256 is in the master" "missing for:$missing_hash"
fi

# The audit logs, read through the API on a restart, reconciled for section 8
# and searched here.
if start_server "$work/serve.log" "$data" --listen "127.0.0.1:$port" --tls-cert "$work/cert.pem" --tls-key "$work/key.pem" --log-level debug; then
	main_pid=$SERVER_PID
	"$PYTHON" "$helper" --work "$work" audit --sock "$sock" --cutoff "$cutoff" || true
	n_entries=$("$PYTHON" -c 'import json, sys; print(len(json.load(open(sys.argv[1]))))' "$work/audit.json" 2> /dev/null || echo 0)
	f=$(found "$work/audit.json")
	if [ -z "$f" ] && [ "$n_entries" -gt 0 ]; then
		ok "5.5 no secret in the audit log listed through the API" "$n_entries entries of every log, as JSON ($(wc -c < "$work/audit.json" | tr -d ' ') bytes)"
	else
		fail "5.5 no secret in the audit log listed through the API" "$n_entries entries; found: $f"
	fi
	stop_server "$main_pid"
else
	fail "8.1 every request the proof made is in the audit log, with its outcome" "the server did not start again" "$(tail -10 "$work/serve.log")"
fi
logs="$work/serve.log $work/copy.log $work/crash.log"
f=$(found $logs)
debug_lines=$(grep -c '"level":"DEBUG"' "$work/serve.log" || true)
if [ -z "$f" ] && [ "$debug_lines" -gt 0 ]; then
	ok "5.6 no secret in the servers' logs" "$(cat $logs | wc -l | tr -d ' ') lines, the main server's at debug level ($debug_lines debug lines), the copy's and the crashed installation's"
else
	fail "5.6 no secret in the servers' logs" "found in: $f; debug lines $debug_lines"
fi

# ===========================================================================
section "11. the static builds and their sizes"
if [ "$BUILD" = host ]; then
	skip "11.1 a static build for every target of make build" "MAGNETAR_PROVE_BUILD=host: only $host_os/$host_arch was built"
else
	targets=$(sed -n 's/^PLATFORMS *:= *//p' "$root/Makefile")
	bad=""
	{
		echo "| Binary | Bytes | MiB |"
		echo "|---|---:|---:|"
	} > "$work/sizes.txt"
	for t in $targets; do
		f=$work/bin/magnetar-${t%/*}-${t#*/}
		[ "${t%/*}" = windows ] && f=$f.exe
		if [ ! -f "$f" ]; then
			bad="$bad $t: missing;"
			continue
		fi
		bytes=$(wc -c < "$f" | tr -d ' ')
		echo "| $(basename "$f") | $bytes | $(awk -v b="$bytes" 'BEGIN { printf "%.2f", b / 1048576 }') |" >> "$work/sizes.txt"
		"$GO" version -m "$f" | grep -q "CGO_ENABLED=0" || bad="$bad $t: not built with CGO_ENABLED=0;"
		if [ "${t%/*}" = linux ] && command -v file > /dev/null; then
			file "$f" | grep -q 'statically linked' || bad="$bad $t: not statically linked ($(file -b "$f" | cut -c 1-60));"
		fi
	done
	if [ -z "$bad" ]; then
		ok "11.1 a static build for every target of make build" "$(echo "$targets" | wc -w | tr -d ' ') targets ($targets), each CGO_ENABLED=0 (go version -m), the Linux ones statically linked (file); sizes below"
	else
		fail "11.1 a static build for every target of make build" "$bad"
	fi
fi

summary_done=1
summary
n_fail=$(awk -F '\t' '$2 == "FAIL"' "$work/results.tsv" | wc -l | tr -d ' ')
n_skip=$(awk -F '\t' '$2 == "skipped"' "$work/results.tsv" | wc -l | tr -d ' ')
if [ "$n_fail" != 0 ]; then exit 1; fi
if [ "$STRICT" = 1 ] && [ "$n_skip" != 0 ]; then
	echo "prove.sh: MAGNETAR_PROVE_STRICT=1 and $n_skip check(s) skipped: nothing may be skipped here" >&2
	exit 1
fi
