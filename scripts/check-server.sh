#!/usr/bin/env bash
# The server checked by three clients that are not Magnetar: curl (JSON over
# HTTP/1.1), grpcurl (gRPC, Go) and Python's grpcio (gRPC, C core), over TLS
# on loopback and over the local socket, against two real server processes:
# one running the stand-in handlers (internal/server/servertest/standin), and
# the real program (./cmd/magnetar) with the real handlers. Against the real
# one each client also creates and lists a tenant with an admin key, and a
# key with no grant is refused the creation and lists no tenant.
#
# Each client must get the same refusal for no key, a malformed key, an
# unknown key and a key with no grant, and success for an admin key over
# TLS and for the local operator on the socket. Every answer must carry a
# Magnetar-Request-Id. /metrics must parse with the Prometheus Python
# client.
#
# Needs: go, curl, openssl, grpcurl (GRPCURL=...), and a Python (PYTHON=...)
# with scripts/requirements-check-server.txt installed.
#
#   make check-server PYTHON=/path/to/venv/bin/python GRPCURL=grpcurl
set -euo pipefail

GO=${GO:-go}
PYTHON=${PYTHON:-python3}
GRPCURL=${GRPCURL:-grpcurl}
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d /tmp/mgt-check.XXXXXX)
server_pid=
cleanup() {
	if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
	rm -rf "$work"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; [ -f "$work/serve.log" ] && tail -20 "$work/serve.log" >&2; exit 1; }
ok() { echo "ok   $*"; }

"$GO" build -o "$work/standin" "$root/internal/server/servertest/standin"
"$GO" build -o "$work/magnetar" "$root/cmd/magnetar"

# A certificate for 127.0.0.1, made for this run only.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
	-subj /CN=magnetar-check -addext subjectAltName=IP:127.0.0.1 \
	-keyout "$work/key.pem" -out "$work/cert.pem" 2> /dev/null

# check BINARY PORT REAL: every check against a fresh installation served by
# BINARY on PORT; REAL is 1 for the real program, whose handlers also serve
# tenants.
check() {
bin=$1 port=$2 real=$3
data=$work/d$port
"$bin" init --data-dir "$data" > /dev/null
"$bin" serve --data-dir "$data" --listen "127.0.0.1:$port" \
	--tls-cert "$work/cert.pem" --tls-key "$work/key.pem" > "$work/serve.log" 2>&1 &
server_pid=$!
sock=$data/run/magnetar.sock
for _ in $(seq 100); do [ -S "$sock" ] && break; sleep 0.1; done
[ -S "$sock" ] || fail "the server did not open its socket"
tls=https://127.0.0.1:$port

# post URL PROCEDURE BODY [curl options...]: prints "status body".
post() {
	local url=$1 proc=$2 body=$3
	shift 3
	curl -sS -o "$work/body" -D "$work/headers" -w '%{http_code}' -H 'Content-Type: application/json' \
		"$@" -d "$body" "$url$proc" > "$work/status"
	grep -qi '^magnetar-request-id: [a-z2-7]\{16\}' "$work/headers" || fail "$proc: no request id"
	echo "$(cat "$work/status") $(cat "$work/body")"
}
viatls() { post "$tls" "$@" --cacert "$work/cert.pem"; }
viasock() { post http://local "$@" --unix-socket "$sock"; }
json() { "$PYTHON" -c 'import json,sys; print(json.load(sys.stdin)'"$1"')'; }

# Two keys, made by the local operator on the socket: one with no grant, one
# admin on the installation.
out=$(viasock /magnetar.v1.AccessService/CreateKey '{"holder":"no grant","neverExpire":true}')
[ "${out%% *}" = 200 ] || fail "CreateKey: $out"
nogrant=$(echo "${out#* }" | json '["secret"]')
out=$(viasock /magnetar.v1.AccessService/CreateKey '{"holder":"admin","neverExpire":true}')
admin=$(echo "${out#* }" | json '["secret"]')
adminid=$(echo "${out#* }" | json '["key"]["id"]')
out=$(viasock /magnetar.v1.AccessService/SetGrant "{\"keyId\":\"$adminid\",\"scope\":{\"installation\":{}},\"role\":\"ROLE_ADMIN\"}")
[ "${out%% *}" = 200 ] || fail "SetGrant: $out"
unknown=mgt_111111111111_1111111111111111111111111111111111111111111

unauth='401 {"code":"unauthenticated","message":"the request carries no valid key"}'
denied='403 {"code":"permission_denied","message":"the key may not do this"}'

echo "== curl"
lk=/magnetar.v1.AccessService/ListKeys
[ "$(viatls $lk '{}')" = "$unauth" ] || fail "curl tls no key: $(viatls $lk '{}')"; ok "curl tls no key: $unauth"
[ "$(viatls $lk '{}' -H 'Authorization: Bearer junk')" = "$unauth" ] || fail "curl tls malformed key"; ok "curl tls malformed key: same"
[ "$(viatls $lk '{}' -H "Authorization: Bearer $unknown")" = "$unauth" ] || fail "curl tls unknown key"; ok "curl tls unknown key: same"
[ "$(viatls $lk '{}' -H "Authorization: Bearer $nogrant")" = "$denied" ] || fail "curl tls no grant"; ok "curl tls key with no grant: $denied"
out=$(viatls $lk '{}' -H "Authorization: Bearer $admin"); [ "${out%% *}" = 200 ] || fail "curl tls admin: $out"; ok "curl tls admin key: 200"
out=$(viasock /magnetar.v1.AccessService/WhoAmI '{}'); [ "${out%% *}" = 200 ] || fail "curl socket: $out"; ok "curl socket local operator: $out"
[ "$(viasock $lk '{}' -H "Authorization: Bearer $unknown")" = "$unauth" ] || fail "curl socket unknown key"; ok "curl socket unknown key: same refusal"
out=$(viatls /grpc.health.v1.Health/Check '{}'); [ "$out" = '200 {"status":"SERVING"}' ] || fail "curl health: $out"; ok "curl tls health without a key: $out"
if [ "$real" = 1 ]; then
	ct=/magnetar.v1.TenantService/CreateTenant
	lt=/magnetar.v1.TenantService/ListTenants
	[ "$(viatls $ct '{"name":"curl1","databaseType":"DATABASE_TYPE_SQLITE"}')" = "$unauth" ] || fail "curl tls create, no key"; ok "curl tls CreateTenant with no key: $unauth"
	[ "$(viatls $ct '{"name":"curl1","databaseType":"DATABASE_TYPE_SQLITE"}' -H "Authorization: Bearer $nogrant")" = "$denied" ] || fail "curl tls create, no grant"; ok "curl tls CreateTenant with a key with no grant: $denied"
	out=$(viatls $ct '{"name":"curl1","databaseType":"DATABASE_TYPE_SQLITE"}' -H "Authorization: Bearer $admin")
	[ "${out%% *}" = 200 ] && [ "$(echo "${out#* }" | json '["tenant"]["state"]')" = TENANT_STATE_READY ] || fail "curl tls create, admin: $out"; ok "curl tls CreateTenant with an admin key: ready"
	out=$(viatls $lt '{}' -H "Authorization: Bearer $admin")
	echo "${out#* }" | grep -q '"name":"curl1"' || fail "curl tls list, admin: $out"; ok "curl tls ListTenants with an admin key: curl1 listed"
	out=$(viatls $lt '{}' -H "Authorization: Bearer $nogrant"); [ "$out" = '200 {}' ] || fail "curl tls list, no grant: $out"; ok "curl tls ListTenants with a key with no grant: none"
fi
curl -sS --unix-socket "$sock" http://local/metrics > "$work/metrics"
grep -q '^magnetar_requests_total' "$work/metrics" || fail "metrics"
if [ "$real" = 1 ]; then
	grep -q '^magnetar_tenants{state="ready"} 1$' "$work/metrics" || fail "metrics: the tenant made above is not counted ready"; ok "metrics: magnetar_tenants{state=\"ready\"} 1"
fi

echo "== grpcurl"
# grpcurl 1.9.2's -unix flag dialed TCP when tried; the gRPC target form
# unix:///path is used for the socket instead.
gc() {
	"$GRPCURL" -import-path "$root/api/proto" -proto magnetar/v1/access.proto "$@" 2>&1 || true
}
gtls() { gc -cacert "$work/cert.pem" "$@"; }
want_unauth='Code: Unauthenticated Message: the request carries no valid key'
want_denied='Code: PermissionDenied Message: the key may not do this'
flat() { tr -s ' \n' ' ' | sed 's/^ //; s/ $//; s/ERROR: //'; }
r=$(gtls -d '{}' "127.0.0.1:$port" magnetar.v1.AccessService/ListKeys | flat); [ "$r" = "$want_unauth" ] || fail "grpcurl tls no key: $r"; ok "grpcurl tls no key: $r"
r=$(gtls -H 'authorization: Bearer junk' -d '{}' "127.0.0.1:$port" magnetar.v1.AccessService/ListKeys | flat); [ "$r" = "$want_unauth" ] || fail "grpcurl malformed: $r"; ok "grpcurl tls malformed key: same"
r=$(gtls -H "authorization: Bearer $unknown" -d '{}' "127.0.0.1:$port" magnetar.v1.AccessService/ListKeys | flat); [ "$r" = "$want_unauth" ] || fail "grpcurl unknown: $r"; ok "grpcurl tls unknown key: same"
r=$(gtls -H "authorization: Bearer $nogrant" -d '{}' "127.0.0.1:$port" magnetar.v1.AccessService/ListKeys | flat); [ "$r" = "$want_denied" ] || fail "grpcurl no grant: $r"; ok "grpcurl tls key with no grant: $r"
r=$(gtls -v -H "authorization: Bearer $admin" -d '{}' "127.0.0.1:$port" magnetar.v1.AccessService/ListKeys)
echo "$r" | grep -q '"keys"' || fail "grpcurl admin: $r"
echo "$r" | grep -qi 'magnetar-request-id' || fail "grpcurl admin: no request id"; ok "grpcurl tls admin key: keys listed, with a request id"
r=$(gc -plaintext -d '{}' "unix://$sock" magnetar.v1.AccessService/WhoAmI | flat); echo "$r" | grep -q localOperator || fail "grpcurl socket: $r"; ok "grpcurl socket local operator: $r"
r=$(gc -plaintext -H "authorization: Bearer $unknown" -d '{}' "unix://$sock" magnetar.v1.AccessService/WhoAmI | flat); [ "$r" = "$want_unauth" ] || fail "grpcurl socket unknown: $r"; ok "grpcurl socket unknown key: same refusal"
if [ "$real" = 1 ]; then
	gt() { "$GRPCURL" -import-path "$root/api/proto" -proto magnetar/v1/tenant.proto -cacert "$work/cert.pem" "$@" 2>&1 || true; }
	r=$(gt -d '{"name":"grpc1","databaseType":"DATABASE_TYPE_SQLITE"}' "127.0.0.1:$port" magnetar.v1.TenantService/CreateTenant | flat); [ "$r" = "$want_unauth" ] || fail "grpcurl create, no key: $r"; ok "grpcurl tls CreateTenant with no key: $r"
	r=$(gt -H "authorization: Bearer $nogrant" -d '{"name":"grpc1","databaseType":"DATABASE_TYPE_SQLITE"}' "127.0.0.1:$port" magnetar.v1.TenantService/CreateTenant | flat); [ "$r" = "$want_denied" ] || fail "grpcurl create, no grant: $r"; ok "grpcurl tls CreateTenant with a key with no grant: $r"
	r=$(gt -H "authorization: Bearer $admin" -d '{"name":"grpc1","databaseType":"DATABASE_TYPE_SQLITE"}' "127.0.0.1:$port" magnetar.v1.TenantService/CreateTenant)
	echo "$r" | grep -q TENANT_STATE_READY || fail "grpcurl create, admin: $r"; ok "grpcurl tls CreateTenant with an admin key: ready"
	r=$(gt -H "authorization: Bearer $admin" -d '{}' "127.0.0.1:$port" magnetar.v1.TenantService/ListTenants)
	echo "$r" | grep -q '"grpc1"' || fail "grpcurl list, admin: $r"; ok "grpcurl tls ListTenants with an admin key: grpc1 listed"
fi

echo "== python grpcio"
MAGNETAR_CHECK_TENANTS=$real "$PYTHON" "$root/scripts/check-server.py" "$root/api/proto" "$sock" "127.0.0.1:$port" "$work/cert.pem" "$nogrant" "$admin" "$work/metrics"

# No key secret in the server's log.
for s in "$nogrant" "$admin"; do
	if grep -qF "${s##*_}" "$work/serve.log"; then fail "a key's secret is in the server's log"; fi
done
ok "no key secret in the server's log ($(wc -l < "$work/serve.log" | tr -d ' ') lines)"

kill -TERM "$server_pid"
status=0; wait "$server_pid" || status=$?
server_pid=
[ "$status" = 0 ] || fail "the server exited $status on SIGTERM"
ok "the server exited 0 on SIGTERM"
}

port=${PORT:-18443}
echo "==== the stand-in handlers"
check "$work/standin" "$port" 0
echo "==== the real program with the real handlers"
check "$work/magnetar" "$((port + 1))" 1
echo "check-server: all checks passed"
