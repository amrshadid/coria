#!/usr/bin/env bash
# A reception checked from outside, with the real program and tools that are
# not Magnetar: a fresh installation (magnetar init, magnetar serve), a
# tenant, a directory storage and a DIMSE receiver set up with the program's
# own commands; dcmtk's storescu and pynetdicom each send a public study
# (internal/receive/testdata); the reception is followed to its end through
# `magnetar reception list --json`. Then:
#
#   - every archived file is read by dcmtk's dcmdump and by pydicom, and the
#     SHA-256 of its data set (what follows its file meta information,
#     PS3.10 7.1) is that of the data set sent;
#   - every manifest's checksums are those shasum computes of the objects it
#     lists, and it lists every object of its scan;
#   - the incoming area holds no file once every reception is done;
#   - a caller the receiver does not list is refused (echoscu);
#   - the server is killed with signal 9 in the middle of a study sent one
#     file per association, started again, the study sent again whole, and
#     the tenant's catalog, read with sqlite3, holds each instance once, its
#     manifest lists each once, and the incoming area is empty.
#
# Each check asserts on what a tool printed, not only on its exit status.
#
# Needs: go, dcmtk (storescu, echoscu, dcmdump; DCMTK=/dir to pick a
# directory, since pynetdicom installs programs of the same names), sqlite3,
# shasum, and a Python with pydicom and pynetdicom (PYTHON=...).
#
#   scripts/check-reception.sh
#   PYTHON=/path/to/python DCMTK=/opt/homebrew/bin scripts/check-reception.sh
set -euo pipefail

GO=${GO:-go}
PYTHON=${PYTHON:-python3}
SQLITE3=${SQLITE3:-sqlite3}
root=$(cd "$(dirname "$0")/.." && pwd)
corpus=$root/internal/receive/testdata
scu=$root/internal/receive/dimse/testdata/scu.py

dcmtk() {
	if [ -n "${DCMTK:-}" ]; then echo "$DCMTK/$1"; else command -v "$1"; fi
}
STORESCU=$(dcmtk storescu)
ECHOSCU=$(dcmtk echoscu)
DCMDUMP=$(dcmtk dcmdump)
DCMCONV=$(dcmtk dcmconv)

work=$(mktemp -d /tmp/mgt-reception.XXXXXX)
chmod 700 "$work"
server_pid=
cleanup() {
	if [ -n "$server_pid" ]; then kill -9 "$server_pid" 2> /dev/null || true; wait "$server_pid" 2> /dev/null || true; fi
	rm -rf "$work"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	[ -f "$work/serve.log" ] && tail -20 "$work/serve.log" >&2
	exit 1
}
ok() { echo "ok   $*"; }

version=$("$STORESCU" --version 2>&1 || true)
case $version in *dcmtk*) ;; *) fail "$STORESCU is not dcmtk's storescu; set DCMTK" ;; esac
"$PYTHON" -c 'import pydicom, pynetdicom' || fail "$PYTHON has no pydicom and pynetdicom; set PYTHON"
echo "$(echo "$version" | grep -i -m1 'dcmtk'), $("$SQLITE3" --version | cut -d' ' -f1), $("$PYTHON" -c 'import pydicom, pynetdicom; print("pydicom", pydicom.__version__, "pynetdicom", pynetdicom.__version__)')"

"$GO" build -o "$work/magnetar" "$root/cmd/magnetar"
data=$work/data
"$work/magnetar" init --data-dir "$data" > /dev/null
m() { "$work/magnetar" --data-dir "$data" "$@"; }

# Two free ports on loopback, one per tenant's receiver.
read -r port crash_port < <("$PYTHON" -c '
import socket
s = [socket.socket() for _ in range(2)]
for x in s: x.bind(("127.0.0.1", 0))
print(*[x.getsockname()[1] for x in s])')

# serve starts the server itself in the background, not a shell function
# that runs it, so $! is the server's own process and signal 9 reaches it.
serve() {
	"$work/magnetar" --data-dir "$data" serve >> "$work/serve.log" 2>&1 &
	server_pid=$!
	for _ in $(seq 200); do
		kill -0 "$server_pid" 2> /dev/null || fail "the server exited at its start"
		[ -S "$data/run/magnetar.sock" ] && m whoami > /dev/null 2>&1 && return 0
		sleep 0.1
	done
	fail "the server did not answer on its socket"
}
serve
ok "magnetar init and serve: the server answers on its socket"

mkdir -p "$work/roots/fast"
m setting set storage.directory-roots "fast=$work/roots/fast" > /dev/null

# setup TENANT PORT: a tenant with a directory storage and a receiver on
# PORT that admits STORESCU and PYNETDICOM only, quiet after 5 seconds.
setup() {
	local t=$1
	local p=$2
	m tenant create "$t" > /dev/null
	m setting set receiver.ports "$p" --tenant "$t" > /dev/null
	m storage add "$t" main --type dir --root fast --path main > /dev/null
	m receiver add "$t" scanner --storage main --port "$p" --aet ARCHIVE \
		--allow-aet STORESCU --allow-aet PYNETDICOM --quiet-period 5s --enabled > /dev/null
	for _ in $(seq 100); do
		m receiver list "$t" --json | grep -q '"RECEIVER_STATE_UP"' && return 0
		sleep 0.1
	done
	fail "the receiver of $t is not up: $(m receiver list "$t" --json)"
}
setup lab "$port"
ok "tenant lab, storage main under the root fast, receiver scanner on port $port: up"

# wait_done TENANT N: waits, at most 120 seconds, until N receptions of
# TENANT are done and none is in progress, and keeps the list.
wait_done() {
	local t=$1
	local want=$2 out
	for _ in $(seq 600); do
		out=$(m reception list "$t" --json)
		if [ "$(echo "$out" | grep -c '"RECEPTION_STEP_DONE"')" = "$want" ] &&
			! echo "$out" | grep -q '"RECEPTION_STEP_\(RECEIVING\|COMPLETE\|IDENTIFIED\|ARCHIVED\|VERIFIED\)"'; then
			echo "$out" > "$work/receptions-$t.json"
			return 0
		fi
		sleep 0.2
	done
	fail "the receptions of $t did not end done: $out"
}

ct=("$corpus"/ct2n-6293.dcm "$corpus"/ct2n-6924.dcm "$corpus"/ct5n-2062.dcm "$corpus"/ct5n-2392.dcm \
	"$corpus"/ct5n-2693.dcm "$corpus"/ct5n-3023.dcm "$corpus"/ct5n-3353.dcm)
# What storescu sends of each: dcmtk encodes a data set again as it sends it
# (a sequence of undefined length is given its length), so the data set it
# sends is that of the file as dcmtk's dcmconv writes it. pynetdicom sends a
# file's own bytes.
mkdir -p "$work/sent"
sent_ct=()
for f in "${ct[@]}"; do
	"$DCMCONV" "$f" "$work/sent/$(basename "$f")"
	sent_ct+=("$work/sent/$(basename "$f")")
done
out=$("$STORESCU" -v -aet STORESCU -aec ARCHIVE 127.0.0.1 "$port" "${ct[@]}" 2>&1)
n=$(echo "$out" | grep -c 'Received Store Response (Success)' || true)
[ "$n" = 7 ] || fail "storescu: $n stores answered success, want 7:
$out"
ok "storescu sent the CT study (7 files, 2 series): 7 stores answered success"

out=$("$PYTHON" -I "$scu" store 127.0.0.1 "$port" PYNETDICOM ARCHIVE "$corpus/MR_small.dcm" "$corpus/liver_1frame.dcm" | tail -1)
[ "$out" = '{"assoc": "accepted", "statuses": [0, 0]}' ] || fail "pynetdicom: $out"
ok "pynetdicom sent MR_small and liver_1frame (two studies): $out"

wait_done lab 3
grep -q '"filesArchived": "7"' "$work/receptions-lab.json" || fail "the CT reception did not archive 7 files: $(cat "$work/receptions-lab.json")"
ok "magnetar reception list --json: 3 receptions done, the CT one with 7 files archived"

# The outside reading of what was archived: dcmdump, pydicom, shasum.
check_storage() {
	local t=$1
	local store=$work/roots/fast/$t/main
	[ -d "$store/data" ] || fail "$t: nothing on the storage"
	local files
	files=$(find "$store/data" -type f -name '*.dcm' | sort)
	local count=0
	for f in $files; do
		out=$("$DCMDUMP" "$f" 2>&1) || fail "dcmdump $f: $out"
		echo "$out" | grep -q '(0008,0018) UI \[' || fail "dcmdump $f printed no SOP Instance UID"
		count=$((count + 1))
	done
	ok "$t: dcmdump read all $count archived files, each with its SOP Instance UID"
	"$PYTHON" -I - "$store" "${@:2}" << 'EOF' || fail "$t: the pydicom check failed"
import glob, hashlib, os, struct, sys
import pydicom

store, sent = sys.argv[1], sys.argv[2:]

def data_set(path):
    # The bytes after the file meta information, whose length the first
    # element (0002,0000) gives (PS3.10 7.1).
    b = open(path, "rb").read()
    assert b[128:132] == b"DICM", path
    n = struct.unpack("<I", b[140:144])[0]
    return b[144 + n:]

want = {}
for p in sent:
    uid = str(pydicom.dcmread(p, stop_before_pixels=True).SOPInstanceUID)
    want[uid] = hashlib.sha256(data_set(p)).hexdigest()
got = {}
for p in glob.glob(os.path.join(store, "data", "**", "*.dcm"), recursive=True):
    ds = pydicom.dcmread(p)
    uid = str(ds.SOPInstanceUID)
    assert os.path.basename(p) == uid + ".dcm", p
    got[uid] = hashlib.sha256(data_set(p)).hexdigest()
if got != want:
    sys.exit("archived data sets %s, sent %s" % (sorted(got.items()), sorted(want.items())))
print("ok   pydicom read all %d archived files; each data set's SHA-256 is the sent one's" % len(got))
EOF
	local manifests listed=0
	manifests=$(find "$store/meta" -name manifest.json | sort)
	for mf in $manifests; do
		scan=$(dirname "${mf#"$store/meta/"}")
		"$PYTHON" -I -c '
import json, sys
for f in json.load(open(sys.argv[1]))["files"]:
    print(f["sha256"] + "  " + f["key"])' "$mf" > "$work/listed"
		(cd "$store" && shasum -a 256 -c "$work/listed" > "$work/shasum.out" 2>&1) || fail "shasum of $mf: $(cat "$work/shasum.out")"
		n=$(grep -c ': OK$' "$work/shasum.out")
		objects=$(find "$store/data/$scan" -type f | wc -l | tr -d ' ')
		[ "$n" = "$objects" ] || fail "$mf lists $n objects, the scan holds $objects"
		listed=$((listed + n))
	done
	[ "$listed" = "$count" ] || fail "$t: the manifests list $listed objects, the storage holds $count"
	ok "$t: shasum -a 256 -c: every manifest entry OK, $listed in all, one per object on the storage"
	local left
	left=$(find "$data/incoming" -type f | wc -l | tr -d ' ')
	[ "$left" = 0 ] || fail "$t: the incoming area holds $left files: $(find "$data/incoming" -type f)"
	ok "$t: the incoming area holds no file"
}
check_storage lab "${sent_ct[@]}" "$corpus/MR_small.dcm" "$corpus/liver_1frame.dcm"

out=$("$ECHOSCU" -aet STRANGER -aec ARCHIVE 127.0.0.1 "$port" 2>&1) && fail "echoscu as a stranger was answered: $out"
echo "$out" | grep -q 'Association Rejected' && echo "$out" | grep -q 'Calling AE Title Not Recognized' ||
	fail "echoscu as a stranger: $out"
ok "echoscu as STRANGER: Association Rejected, Calling AE Title Not Recognized"
out=$("$ECHOSCU" -v -aet STORESCU -aec ARCHIVE 127.0.0.1 "$port" 2>&1) || fail "echoscu as STORESCU: $out"
echo "$out" | grep -q 'Received Echo Response (Success)' || fail "echoscu as STORESCU: $out"
ok "echoscu as STORESCU: Received Echo Response (Success)"

# The crash: a tenant of its own, the CT study sent one file per
# association, and the server killed with signal 9 after the third file is
# answered, while the reception is receiving.
setup crash "$crash_port"
for i in 0 1 2; do
	out=$("$STORESCU" -v -aet STORESCU -aec ARCHIVE 127.0.0.1 "$crash_port" "${ct[$i]}" 2>&1)
	echo "$out" | grep -q 'Received Store Response (Success)' || fail "storescu before the crash: $out"
done
# The files answered may not have their rows yet (rows are written in
# batches, a few times a second): a restart finds those in the incoming area.
out=$(m reception list crash --json)
echo "$out" | grep -q '"RECEPTION_STEP_RECEIVING"' || fail "the reception is not receiving before the kill: $out"
rows=$(echo "$out" | sed -n 's/.*"filesReceived": "\([0-9]*\)".*/\1/p')
killed=$server_pid
kill -9 "$killed"
status=0
wait "$killed" 2> /dev/null || status=$?
server_pid=
[ "$status" = 137 ] || fail "the server's exit status after signal 9 is $status, want 137"
kill -0 "$killed" 2> /dev/null && fail "the server still runs after signal 9"
ok "3 of 7 files answered success, the reception receiving with $rows of them recorded, then the server killed with signal 9 (exit 137)"
serve
[ "$server_pid" != "$killed" ] || fail "the restarted server is the killed one"
for _ in $(seq 100); do
	m receiver list crash --json | grep -q '"RECEIVER_STATE_UP"' && break
	sleep 0.1
done
out=$("$STORESCU" -v -aet STORESCU -aec ARCHIVE 127.0.0.1 "$crash_port" "${ct[@]}" 2>&1)
n=$(echo "$out" | grep -c 'Received Store Response (Success)' || true)
[ "$n" = 7 ] || fail "storescu after the restart: $n of 7 answered success:
$out"
ok "after the restart the study sent again whole: 7 stores answered success"
wait_done crash 1
db=$data/tenants/crash.db
rows=$("$SQLITE3" -batch -noheader "$db" "SELECT count(*) || ' ' || count(DISTINCT sop_instance_uid) FROM instances WHERE state = 'held'")
[ "$rows" = "7 7" ] || fail "sqlite3: the catalog holds '$rows' instances (rows, distinct UIDs), want '7 7'"
claimed=$("$SQLITE3" -batch -noheader "$db" "SELECT count(*) FROM instances WHERE state <> 'held'")
[ "$claimed" = 0 ] || fail "sqlite3: $claimed instances are left claimed"
held=$("$SQLITE3" -batch -noheader "$db" "SELECT count(*) FROM quarantine WHERE state = 'held'")
ok "sqlite3 on tenants/crash.db: 7 instances held, 7 distinct UIDs, none left claimed, $held files in quarantine"
check_storage crash "${sent_ct[@]}"

kill -TERM "$server_pid"
status=0
wait "$server_pid" || status=$?
server_pid=
[ "$status" = 0 ] || fail "the server exited $status on SIGTERM"
ok "the server exited 0 on SIGTERM"
echo "check-reception: all checks passed"
