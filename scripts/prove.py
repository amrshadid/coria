#!/usr/bin/env python3
"""The parts of scripts/prove.sh that are easier to say in Python.

Each subcommand is one step of the proof, run by prove.sh against the
real binary it started; none imports anything of Magnetar's. They share the
script's work directory:

  results.tsv  one line per named check: name, ok / FAIL / skipped, numbers
  ledger.tsv   one line per request the proof made to the main server: its
               request id (from the response header, or "-" when the client
               does not show it), method, key id ("local" for the local
               operator), the outcome the audit log must hold, the tenant it
               named ("*" when not checked), and which client sent it
  secrets      every secret the proof made, one per line, so that no line
               printed here can show one (a secret is shown as its length and
               the first 8 hex digits of its SHA-256)

Subcommands:

  refusals  the refusal matrix of points 2 and 4: every method of the
            contract, read from the proto files, with no key and with each kind
            of bad key, through grpcurl, Python's grpcio and curl over TLS; the
            answers compared byte for byte within each client
  audit     point 8: the audit logs read through the API and reconciled
            against the ledger in both directions
  tui       point 10: the interactive program under a pseudo-terminal, with
            the terminal emulator of scripts/check-tui.py
  json      point 10: every --json document parsed
  metrics   point 8: /metrics parsed by the Prometheus client; no audit entry
            dropped or failed

Needs grpcio and prometheus-client (scripts/requirements-check-server.txt) for
refusals and metrics; the others use the standard library only.
"""

import argparse
import datetime
import hashlib
import importlib.util
import json
import os
import re
import secrets as pysecrets
import subprocess
import sys

UNAUTH_MESSAGE = "the request carries no valid key"
DENIED_MESSAGE = "the key may not do this"
NOT_FOUND_MESSAGE = "no tenant has this name"

BASE58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

# Methods that never change anything: what the interactive program may call.
READ_PREFIXES = ("Get", "List", "WhoAmI")

# The audit entries the server writes by itself, not for one request
# (internal/registry and servecmd): known and accounted for by name. Any other
# method that does not start with "/" or "GET " is unexpected, among them
# access.AuditWriter, the notice that entries were dropped.
INIT_METHOD = "magnetar init"
STARTED_METHOD = "magnetar.registry/CreateTenantStarted"
STATE_METHOD = "magnetar.registry/SetTenantState"


class Work:
    """The shared files of the work directory."""

    def __init__(self, path):
        self.path = path
        self.failed = 0

    def secrets(self):
        try:
            with open(os.path.join(self.path, "secrets")) as f:
                return [s for s in f.read().split("\n") if s]
        except FileNotFoundError:
            return []

    def redact(self, text):
        """Replace every known secret by its length and fingerprint, longest
        first, so a secret that contains another is replaced whole."""
        for s in sorted(self.secrets(), key=len, reverse=True):
            if s in text:
                fp = hashlib.sha256(s.encode()).hexdigest()[:8]
                text = text.replace(s, "<secret, %d bytes, sha256 %s...>" % (len(s), fp))
        return text

    def result(self, name, ok, detail, evidence=None, status=None):
        """Record and print one named check."""
        status = status or ("ok" if ok else "FAIL")
        detail = self.redact(detail).replace("\t", " ").replace("\n", " ")
        print("%-4s %s: %s" % (status, name, detail), flush=True)
        if status == "FAIL":
            self.failed += 1
            for line in (evidence or [])[:20]:
                print("     | " + self.redact(str(line)), flush=True)
        with open(os.path.join(self.path, "results.tsv"), "a") as f:
            f.write("%s\t%s\t%s\n" % (name, status, detail))

    def ledger(self, rid, method, key_id, outcome, tenant, source):
        with open(os.path.join(self.path, "ledger.tsv"), "a") as f:
            f.write("\t".join([rid or "-", method, key_id, outcome, tenant, source]) + "\n")


def outcome_of(code):
    """The audit outcome the contract gives an answer's code (audit.proto,
    AuditOutcome): done for OK, refused for UNAUTHENTICATED and
    PERMISSION_DENIED, failed for every other."""
    code = code.lower()
    if code in ("ok", "200"):
        return "done"
    if code in ("unauthenticated", "permission_denied", "permissiondenied", "401", "403"):
        return "refused"
    return "failed"


# ---------------------------------------------------------------------------
# The contract, read from the proto files.


def contract_methods(proto_dir):
    """Every (service, method, proto file) of the contract, in file order."""
    out = []
    pkg_dir = os.path.join(proto_dir, "magnetar", "v1")
    for name in sorted(os.listdir(pkg_dir)):
        if not name.endswith(".proto"):
            continue
        with open(os.path.join(pkg_dir, name)) as f:
            text = re.sub(r"//[^\n]*", "", f.read())
        for svc in re.finditer(r"service\s+(\w+)\s*\{", text):
            depth, i = 1, svc.end()
            while depth and i < len(text):
                depth += {"{": 1, "}": -1}.get(text[i], 0)
                i += 1
            for rpc in re.finditer(r"\brpc\s+(\w+)\s*\(", text[svc.end():i]):
                out.append(("magnetar.v1." + svc.group(1), rpc.group(1), "magnetar/v1/" + name))
    return out


def pb_string(field, value):
    """A protobuf length-delimited field: a string or an embedded message."""
    data = value.encode() if isinstance(value, str) else value
    assert len(data) < 128
    return bytes([field << 3 | 2, len(data)]) + data


# The requests that name a tenant, used to compare "no grant on this tenant"
# with "no such tenant": (service, method, proto, JSON body, protobuf body).
def tenant_requests(tenant):
    scope = pb_string(1, pb_string(1, tenant))
    return [
        ("magnetar.v1.TenantService", "GetTenant", "magnetar/v1/tenant.proto", {"name": tenant}, pb_string(1, tenant)),
        ("magnetar.v1.SettingsService", "ListSettings", "magnetar/v1/settings.proto", {"scope": {"tenant": tenant}}, scope),
        ("magnetar.v1.AuditService", "ListAuditEntries", "magnetar/v1/audit.proto", {"scope": {"tenant": tenant}}, scope),
    ]


# ---------------------------------------------------------------------------
# The three clients. Each call returns (code, message, normalized answer,
# request id); the normalized answer is everything the client received
# except the request id and the date, so two refusals can be compared byte for
# byte.


class Clients:
    def __init__(self, work, args):
        self.work = work
        self.args = args
        self.proto = args.proto
        with open(args.ca, "rb") as f:
            ca = f.read()
        import grpc  # imported here: the audit, tui and json steps do not need it

        self.grpc = grpc
        self.channel = grpc.secure_channel(args.addr, grpc.ssl_channel_credentials(root_certificates=ca))
        self.headers = os.path.join(work.path, "auth.headers")

    def close(self):
        self.channel.close()

    def grpcurl(self, service, method, proto, body, key):
        # The key reaches grpcurl through its environment and -expand-headers,
        # never as an argument, which every user of the machine could read.
        env = dict(os.environ)
        cmd = [self.args.grpcurl, "-import-path", self.proto, "-proto", proto, "-cacert", self.args.ca, "-v"]
        if key is not None:
            env["MGT_PROOF_AUTH"] = key
            cmd += ["-expand-headers", "-H", "authorization: ${MGT_PROOF_AUTH}"]
        cmd += ["-d", json.dumps(body), self.args.addr, "%s/%s" % (service, method)]
        p = subprocess.run(cmd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        out = p.stdout.decode("utf-8", "replace")
        # The method's description, which grpcurl prints from the proto file,
        # comes before what the server sent; only the latter is compared.
        start = out.find("Response headers received:")
        answer = out[start:] if start >= 0 else out
        rid = ""
        kept = []
        for line in answer.split("\n"):
            low = line.lower()
            if low.startswith("magnetar-request-id:"):
                rid = line.split(":", 1)[1].strip()
                continue
            if low.startswith("date:"):
                continue
            kept.append(line)
        m = re.search(r"Code: (\w+)\s+Message: ([^\n]*)", answer)
        code = m.group(1) if m else ("OK" if p.returncode == 0 else "exit %d" % p.returncode)
        message = m.group(2).strip() if m else ""
        norm = "exit=%d\n%s" % (p.returncode, "\n".join(kept))
        return code, message, norm, rid

    def python(self, service, method, raw, key):
        grpc = self.grpc
        call = self.channel.unary_unary("/%s/%s" % (service, method), request_serializer=None, response_deserializer=None)
        md = [("authorization", key)] if key is not None else []
        answer = b""
        try:
            answer, rpc = call.with_call(raw, metadata=md, timeout=30)
            code, message, init, trail = grpc.StatusCode.OK, "", rpc.initial_metadata(), rpc.trailing_metadata()
        except grpc.RpcError as e:
            code, message, init, trail = e.code(), e.details() or "", e.initial_metadata() or (), e.trailing_metadata() or ()
        rid = ""
        for k, v in list(init) + list(trail):
            if k == "magnetar-request-id":
                rid = v
        keep = lambda m: sorted((k, v) for k, v in m if k not in ("magnetar-request-id", "date"))
        norm = "%s|%s|headers=%r|trailers=%r|body=%r" % (code.name, message, keep(init), keep(trail), answer)
        return code.name, message, norm, rid

    def curl(self, path, body, key):
        # The key goes in a header file only the script's user can read.
        hdr = []
        if key is not None:
            fd = os.open(self.headers, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as f:
                f.write("Authorization: %s\n" % key)
            hdr = ["-H", "@" + self.headers]
        out_body = os.path.join(self.work.path, "curl.body")
        out_head = os.path.join(self.work.path, "curl.head")
        cmd = ["curl", "-sS", "--cacert", self.args.ca, "-o", out_body, "-D", out_head, "-w", "%{http_code}"] + hdr
        url = "https://%s%s" % (self.args.addr, path)
        if body is not None:
            cmd += ["-H", "Content-Type: application/json", "-d", json.dumps(body)]
        p = subprocess.run(cmd + [url], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
        if key is not None:
            os.unlink(self.headers)
        status = p.stdout.decode().strip()
        with open(out_body, "rb") as f:
            data = f.read()
        with open(out_head, "rb") as f:
            head = f.read().decode("latin-1")
        rid = ""
        kept = []
        for line in head.split("\r\n"):
            low = line.lower()
            if low.startswith("magnetar-request-id:"):
                rid = line.split(":", 1)[1].strip()
                continue
            if low.startswith("date:"):
                continue
            kept.append(line)
        code, message = status, ""
        try:
            doc = json.loads(data)
            code, message = doc.get("code", status), doc.get("message", "")
        except ValueError:
            pass
        if status == "200":
            code = "ok"
        norm = "%s\n%s\n%s" % (status, "\n".join(kept), data.decode("utf-8", "replace"))
        return code, message, norm, rid


def key_of(keys, name):
    return keys[name]["key"], keys[name]["id"]


def bad_keys(keys):
    """The six ways of carrying no valid key, with the key id the audit
    entry must hold for each (AuditEntry.key_id: read from the key even when
    it is refused; empty when there is none to read)."""
    unknown_id = "".join(pysecrets.choice(BASE58) for _ in range(12))
    real_secret = keys["nogrant"]["key"].split("_", 2)[2]
    wrong = "".join(pysecrets.choice(BASE58) for _ in range(len(real_secret)))
    unknown = "mgt_%s_%s" % (unknown_id, "".join(pysecrets.choice(BASE58) for _ in range(len(real_secret))))
    wrong_secret = "mgt_%s_%s" % (keys["nogrant"]["id"], wrong)
    return [
        ("no key", None, ""),
        ("malformed key", "Bearer not-a-key", ""),
        ("unknown key id", "Bearer " + unknown, unknown_id),
        ("wrong secret", "Bearer " + wrong_secret, keys["nogrant"]["id"]),
        ("expired key", "Bearer " + keys["expired"]["key"], keys["expired"]["id"]),
        ("revoked key", "Bearer " + keys["revoked"]["key"], keys["revoked"]["id"]),
    ]


def cmd_refusals(work, args):
    methods = contract_methods(args.proto)
    cl = Clients(work, args)
    try:
        if args.mode == "walk":
            walk(work, cl, methods)
        else:
            with open(os.path.join(work.path, "keys.json")) as f:
                keys = json.load(f)
            matrix(work, cl, methods, keys, args)
    finally:
        cl.close()
    return 1 if work.failed else 0


def walk(work, cl, methods):
    """Point 2: every method of the contract with no key, by grpcurl, and
    /metrics by curl: all refused, with one answer."""
    answers, evidence = {}, []
    for svc, m, proto in methods:
        code, msg, norm, rid = cl.grpcurl(svc, m, proto, {}, None)
        work.ledger(rid, "/%s/%s" % (svc, m), "", outcome_of(code), "*", "grpcurl walk")
        answers.setdefault((code, msg, norm), []).append(m)
        if code != "Unauthenticated" or msg != UNAUTH_MESSAGE or not rid:
            evidence.append("%s/%s: %s %r request-id=%r" % (svc, m, code, msg, rid))
    ok = len(answers) == 1 and not evidence
    not_refused = len(evidence)
    if len(answers) > 1:
        evidence += ["answer %d, for %s:\n%s" % (i, ",".join(v), k[2]) for i, (k, v) in enumerate(answers.items())]
    work.result("2.3 no key over TLS: every method refused, one answer (grpcurl)", ok,
                "%d methods read from the proto files, %d distinct answer(s), %d not Unauthenticated %r"
                % (len(methods), len(answers), not_refused, UNAUTH_MESSAGE),
                evidence)
    code, msg, norm, rid = cl.curl("/metrics", None, None)
    work.ledger(rid, "GET /metrics", "", outcome_of(code), "*", "curl walk")
    work.result("2.4 no key over TLS: /metrics refused (curl)", code == "unauthenticated" and msg == UNAUTH_MESSAGE and bool(rid),
                "%s %r" % (code, msg), [norm])


def matrix(work, cl, methods, keys, args):
    """Point 4: each kind of refusal by each client, compared."""
    bad = bad_keys(keys)
    nogrant_key, nogrant_id = key_of(keys, "nogrant")
    reader_key, reader_id = key_of(keys, "reader")
    admin_key, admin_id = key_of(keys, "admin")
    clients = ("grpcurl", "python", "curl")
    seen = {c: {} for c in clients}  # client -> kind -> {norm: [labels]}
    agree = {}  # (kind) -> {(client): (code, message)}
    total = 0

    def send(client, svc, m, proto, jbody, raw, auth, key_id, kind, tenant="*"):
        nonlocal total
        total += 1
        if client == "grpcurl":
            code, msg, norm, rid = cl.grpcurl(svc, m, proto, jbody, auth)
        elif client == "python":
            code, msg, norm, rid = cl.python(svc, m, raw, auth)
        else:
            code, msg, norm, rid = cl.curl("/%s/%s" % (svc, m), jbody, auth)
        work.ledger(rid, "/%s/%s" % (svc, m), key_id, outcome_of(code), tenant, client + " " + kind)
        seen[client].setdefault(kind, {}).setdefault(norm, []).append("%s %s" % (m, kind))
        agree.setdefault(kind, {}).setdefault(client, set()).add((code.lower().replace("_", ""), msg))
        return code, msg, rid

    for client in clients:
        for svc, m, proto in methods:
            for label, auth, key_id in bad:
                send(client, svc, m, proto, {}, b"", auth, key_id, "unauthenticated")
            # Two methods answer a key with no grant (access.proto): WhoAmI,
            # which any authenticated caller may ask, and ListTenants, which
            # lists the tenants the key holds a grant on: none.
            if m == "WhoAmI":
                send(client, svc, m, proto, {}, b"", "Bearer " + nogrant_key, nogrant_id, "whoami-nogrant")
            elif m == "ListTenants":
                send(client, svc, m, proto, {}, b"", "Bearer " + nogrant_key, nogrant_id, "list-nogrant")
            else:
                send(client, svc, m, proto, {}, b"", "Bearer " + nogrant_key, nogrant_id, "permission denied")
        if client == "curl":
            for label, auth, key_id in bad:
                code, msg, norm, rid = cl.curl("/metrics", None, auth)
                total += 1
                work.ledger(rid, "GET /metrics", key_id, outcome_of(code), "*", "curl metrics " + label)
                seen[client].setdefault("metrics", {}).setdefault(norm, []).append("metrics " + label)
        for svc, m, proto, jbody, raw in tenant_requests(args.pg_tenant):
            send(client, svc, m, proto, jbody, raw, "Bearer " + reader_key, reader_id, "tenant-denied", args.pg_tenant)
        for svc, m, proto, jbody, raw in tenant_requests(args.missing_tenant):
            send(client, svc, m, proto, jbody, raw, "Bearer " + reader_key, reader_id, "tenant-denied", args.missing_tenant)
        svc, m, proto, jbody, raw = tenant_requests(args.missing_tenant)[0]
        send(client, svc, m, proto, jbody, raw, "Bearer " + admin_key, admin_id, "not-found", args.missing_tenant)

    want = {"unauthenticated": UNAUTH_MESSAGE, "permission denied": DENIED_MESSAGE, "tenant-denied": DENIED_MESSAGE,
            "not-found": NOT_FOUND_MESSAGE, "whoami-nogrant": "", "list-nogrant": ""}
    want_code = {"unauthenticated": "unauthenticated", "permission denied": "permissiondenied", "tenant-denied": "permissiondenied",
                 "not-found": "notfound", "whoami-nogrant": "ok", "list-nogrant": "ok"}
    n_methods = len(methods)
    names = {"grpcurl": "4.1", "python": "4.2", "curl": "4.3"}
    for client in clients:
        kinds = seen[client]
        u = kinds.get("unauthenticated", {})
        codes = agree["unauthenticated"][client]
        ok = len(u) == 1 and codes == {(want_code["unauthenticated"], UNAUTH_MESSAGE)}
        detail = "%d methods x %d kinds (%s): %d distinct answer(s)" % (n_methods, len(bad), ", ".join(b[0] for b in bad), len(u))
        if client == "curl":
            mt = kinds.get("metrics", {})
            ok = ok and len(mt) == 1
            detail += "; /metrics x %d kinds: %d distinct" % (len(bad), len(mt))
        work.result("%s unauthenticated, one answer (%s)" % (names[client], client), ok, detail,
                    ["%r: %s" % (k, ", ".join(v[:4])) for k, v in list(u.items())[:6]] + ["codes %r" % codes])

    def kind_check(name, kind, detail, extra_ok=True):
        evidence, ok = [], extra_ok
        for client in clients:
            group = seen[client].get(kind, {})
            codes = agree.get(kind, {}).get(client, set())
            if len(group) != 1 or codes != {(want_code[kind], want[kind])}:
                ok = False
                evidence.append("%s: %d distinct answers, codes %r" % (client, len(group), codes))
                evidence += ["  %r: %s" % (k, ", ".join(v[:6])) for k, v in group.items()]
        work.result(name, ok, detail, evidence)

    kind_check("4.4 a key with no grant: one permission-denied answer", "permission denied",
               "%d methods x 3 clients, one answer per client: PermissionDenied %r" % (n_methods - 2, DENIED_MESSAGE))
    kind_check("4.5 a key with no grant may still ask who it is", "whoami-nogrant", "WhoAmI answered OK to the key with no grant, 3 clients")
    # The list must hold no tenant: an empty message, whatever the client.
    empty = {"grpcurl": "Response contents:\n{}\n", "python": "|body=b''", "curl": "\n{}"}
    listed = [c + ": " + n for c in clients for n in seen[c].get("list-nogrant", {}) if empty[c] not in n]
    kind_check("4.6 a key with no grant lists no tenant", "list-nogrant",
               "ListTenants answered OK with no tenant (2 exist), 3 clients", extra_ok=not listed)
    if listed:
        print("     | " + "\n     | ".join(listed))
    kind_check("4.7 no grant on a tenant = no such tenant, byte for byte", "tenant-denied",
               "reader of one tenant asks for %s (exists) and %s (does not): GetTenant, ListSettings, ListAuditEntries x 3 clients, one answer per client"
               % (args.pg_tenant, args.missing_tenant))
    # The denial for a tenant must also be the very answer of "no grant" on
    # the same method (GetTenant), not merely the same code.
    same = True
    ev = []
    for client in clients:
        a = set(seen[client].get("tenant-denied", {}))
        b = set()
        for norm, labels in seen[client].get("permission denied", {}).items():
            if any(l.startswith("GetTenant ") for l in labels):
                b.add(norm)
        if not a or a != b:
            same = False
            ev.append("%s: tenant denial %r, no-grant GetTenant %r" % (client, a, b))
    work.result("4.8 the tenant denial is the no-grant answer itself", same, "GetTenant compared within each client", ev)
    kind_check("4.9 a caller who may list tenants is told not found", "not-found",
               "admin of the installation asks for %s: NotFound %r, 3 clients" % (args.missing_tenant, NOT_FOUND_MESSAGE))
    # Across clients: one code and one message for each kind of refusal.
    ev = []
    for kind, per in agree.items():
        allv = set()
        for v in per.values():
            allv |= v
        if len(allv) != 1:
            ev.append("%s: %r" % (kind, per))
    work.result("4.10 the three clients get the same code and message", not ev, "%d requests in all, %d kinds" % (total, len(agree)), ev)


# ---------------------------------------------------------------------------
# Point 8: the audit logs, read through the API, reconciled with the ledger.


def parse_time(s):
    """An RFC 3339 time as the API writes it, to seconds since the epoch."""
    s = s.replace("Z", "+00:00")
    m = re.match(r"(.*T\d\d:\d\d:\d\d)(\.\d+)?(.*)", s)
    frac = (m.group(2) or ".0")[:7]
    t = datetime.datetime.strptime(m.group(1) + frac + m.group(3).replace(":", ""), "%Y-%m-%dT%H:%M:%S.%f%z")
    return t.timestamp()


def socket_post(sock, path, body):
    p = subprocess.run(["curl", "-sS", "--fail-with-body", "--unix-socket", sock, "-H", "Content-Type: application/json",
                        "-D", "-", "-d", json.dumps(body), "http://local" + path], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
    if p.returncode != 0:
        raise SystemExit("prove.py: %s answered %s %s" % (path, p.stdout.decode()[-400:], p.stderr.decode()))
    head, _, data = p.stdout.decode().partition("\r\n\r\n")
    rid = ""
    for line in head.split("\r\n"):
        if line.lower().startswith("magnetar-request-id:"):
            rid = line.split(":", 1)[1].strip()
    return json.loads(data), rid


def read_logs(sock, out_path):
    """Every entry of the installation's log and of each tenant's log, through
    the API as the local operator. Returns the entries, each with "log" set,
    and the request ids of the reads made here."""
    reads = []
    tenants, rid = socket_post(sock, "/magnetar.v1.TenantService/ListTenants", {"pageSize": 1000})
    reads.append(("ListTenants", rid))
    entries = []
    scopes = [("installation", {"installation": {}})] + [(t["name"], {"tenant": t["name"]}) for t in tenants.get("tenants", [])]
    for log, scope in scopes:
        token = ""
        while True:
            page, rid = socket_post(sock, "/magnetar.v1.AuditService/ListAuditEntries", {"scope": scope, "pageSize": 1000, "pageToken": token})
            reads.append(("ListAuditEntries", rid))
            for e in page.get("entries", []):
                e["log"] = log
                entries.append(e)
            token = page.get("nextPageToken", "")
            if not token:
                break
    with open(out_path, "w") as f:
        json.dump(entries, f)
    return entries, reads


def cmd_audit(work, args):
    entries, reads = read_logs(args.sock, os.path.join(work.path, "audit.json"))
    with open(os.path.join(work.path, "ledger.tsv")) as f:
        ledger = [line.rstrip("\n").split("\t") for line in f if line.strip()]
    tui = None
    if os.path.exists(os.path.join(work.path, "tui.window")):
        with open(os.path.join(work.path, "tui.window")) as f:
            tui = [float(x) for x in f.read().split()]
    cutoff = args.cutoff

    def system(e):
        m = e.get("method", "")
        return not (m.startswith("/") or m.startswith("GET "))

    def caller(e):
        return "local" if e.get("localUserId") else e.get("keyId", "")

    by_rid = {}
    for e in entries:
        if not system(e) and e.get("requestId"):
            by_rid.setdefault(e["requestId"], []).append(e)
    used = set()
    missing, wrong, misplaced, unaudited_ok = [], [], [], 0
    no_rid = []
    found = found_rid = 0
    for row in ledger:
        rid, method, key_id, outcome, tenant, source = row[:6]
        if outcome == "none":
            # A request the contract does not audit (the health check): it
            # must not be in the log.
            if rid in by_rid:
                wrong.append("%s %s is in the log: %r" % (source, method, by_rid[rid]))
            else:
                unaudited_ok += 1
            continue
        if rid == "-":
            no_rid.append(row)
            continue
        cands = by_rid.get(rid, [])
        if len(cands) != 1:
            missing.append("%s %s request-id %s: %d entries" % (source, method, rid, len(cands)))
            continue
        e = cands[0]
        used.add(id(e))
        found += 1
        found_rid += 1
        problems = []
        if e.get("method") != method:
            problems.append("method %r" % e.get("method"))
        if e.get("outcome", "").replace("AUDIT_OUTCOME_", "").lower() != outcome:
            problems.append("outcome %r, want %s" % (e.get("outcome"), outcome))
        if caller(e) != key_id:
            problems.append("caller %r, want %r" % (caller(e), key_id))
        if tenant != "*" and e.get("tenant", "") != tenant:
            problems.append("tenant %r, want %r" % (e.get("tenant", ""), tenant))
        if problems:
            wrong.append("%s %s %s: %s" % (source, method, rid, "; ".join(problems)))
        # A refusal is always in the installation's log: a caller who was
        # refused must not be able to cause a write in a tenant's database.
        if outcome == "refused" and e["log"] != "installation":
            misplaced.append("%s %s %s refused, in the log of %s" % (source, method, rid, e["log"]))
    # The commands of the command line show a request id only on an error:
    # their requests are matched by what they were and when (to the second,
    # with a second's margin), never with an entry of the interactive
    # program's window, which is accounted for below.
    def in_tui(e):
        return tui is not None and tui[0] <= parse_time(e["time"]) <= tui[1]

    rest = [e for e in entries if id(e) not in used and not system(e) and not in_tui(e)]
    for row in no_rid:
        rid, method, key_id, outcome, tenant, source = row[:6]
        t0, t1 = (float(x) for x in row[6].split()) if len(row) > 6 and row[6] else (0, 1e12)
        match = None
        for e in rest:
            if id(e) in used:
                continue
            if not (t0 - 1 <= parse_time(e["time"]) <= t1 + 1):
                continue
            if (e.get("method") == method and caller(e) == key_id
                    and e.get("outcome", "").replace("AUDIT_OUTCOME_", "").lower() == outcome
                    and (tenant == "*" or e.get("tenant", "") == tenant)):
                match = e
                break
        if match is None:
            missing.append("%s %s (%s, %s): no entry" % (source, method, key_id, outcome))
        else:
            used.add(id(match))
            found += 1
    expected = len([r for r in ledger if r[3] != "none"])
    work.result("8.1 every request the proof made is in the audit log, with its outcome",
                not missing and not wrong and not misplaced,
                "%d of %d found (%d by request id, %d command line requests by what they were); %d health checks not audited, as the contract says"
                % (found, expected, found_rid, found - found_rid, unaudited_ok),
                missing[:8] + wrong[:8] + misplaced[:8])

    # The other direction: nothing in the logs that the proof did not do.
    started = set()
    for e in entries:
        if id(e) in used and e.get("method") == "/magnetar.v1.TenantService/CreateTenant":
            started.add(e.get("requestId"))
    unexpected, counts = [], {"init": 0, "started": 0, "state": 0, "tui": 0, "after the stop": 0}
    states = []
    tui_lists = 0
    # What init writes (the installation's id, its key check) comes before
    # the first request of all: an "init" entry after that is not init's.
    first_request = min([parse_time(e["time"]) for e in entries if not system(e)] or [0])
    for e in entries:
        if id(e) in used:
            continue
        m = e.get("method", "")
        t = parse_time(e["time"])
        if m == INIT_METHOD and t < first_request:
            counts["init"] += 1
        elif m == STARTED_METHOD and e.get("requestId") in started:
            counts["started"] += 1
        elif m == STATE_METHOD:
            counts["state"] += 1
            d = e.get("details", {})
            states.append("%s %s>%s" % (e.get("tenant"), d.get("old_state", "?"), d.get("new_state", "?")))
        elif system(e):
            unexpected.append(e)
        elif t > cutoff and caller(e) == "local" and m.rsplit("/", 1)[-1] in ("ListAuditEntries", "ListTenants"):
            counts["after the stop"] += 1
        elif tui and tui[0] <= t <= tui[1] and caller(e) == "local" and m.rsplit("/", 1)[-1].startswith(READ_PREFIXES) \
                and e.get("outcome") == "AUDIT_OUTCOME_DONE":
            counts["tui"] += 1
            tui_lists += m.endswith("/ListTenants")
        else:
            unexpected.append(e)
    ok = not unexpected and counts["init"] >= 1 and (tui is None or tui_lists > 0)
    ev = [json.dumps({k: v for k, v in e.items() if k != "id"}) for e in unexpected[:10]]
    if counts["init"] < 1:
        ev.append("no entry written by init")
    if tui is not None and not tui_lists:
        ev.append("no ListTenants by the interactive program in its window")
    work.result("8.2 nothing in the audit log that the proof did not do", ok,
                "%d entries in %d log(s); beside the proof's requests: %d by init,%d creation-started, %d state changes (%s), %d by the interactive program (%d ListTenants), %d reads after the stop; %d unexpected"
                % (len(entries), len(set(e["log"] for e in entries)), counts["init"], counts["started"], counts["state"],
                   ", ".join(states) or "none", counts["tui"], tui_lists, counts["after the stop"], len(unexpected)), ev)
    return 1 if work.failed else 0


# ---------------------------------------------------------------------------
# Point 10.


def cmd_tui(work, args):
    here = os.path.dirname(os.path.abspath(__file__))
    # No __pycache__ left in the checkout by the import below.
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location("check_tui", os.path.join(here, "check-tui.py"))
    tui = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(tui)
    for var in ("MAGNETAR_SERVER", "MAGNETAR_KEY", "MAGNETAR_CA_FILE"):
        os.environ.pop(var, None)
    start = datetime.datetime.now().timestamp()
    s = tui.Session(args.binary, args.data_dir, cols=120, rows=40)
    seen = {"local operator": s.wait_for("local operator")}
    s.press("1")
    for name in args.tenant:
        seen[name] = s.wait_for(name)
    s.press("q")
    code = s.finish()
    end = datetime.datetime.now().timestamp()
    with open(os.path.join(work.path, "tui.window"), "w") as f:
        # The server and this script read the same clock, so the window is
        # exact; prove.sh leaves two seconds before it free of other
        # requests, as the command line's are matched to the second.
        f.write("%f %f\n" % (start, end))
    restored = s.restored()
    ok = all(seen.values()) and code == 0 and restored
    work.result("10.2 the interactive program lists the tenants and quits cleanly", ok,
                "under a pseudo-terminal, 120x40: header %s, %s listed, q exits %r, terminal restored %s"
                % ("shown" if seen["local operator"] else "MISSING", " and ".join("%s %s" % (n, "yes" if seen[n] else "NO") for n in args.tenant), code, restored),
                [s.screen.text()])
    return 1 if work.failed else 0


def cmd_json(work, args):
    evidence, n = [], 0
    for spec in args.doc:
        path, field = spec.rsplit(":", 1)
        n += 1
        try:
            with open(path) as f:
                doc = json.load(f)
        except ValueError as e:
            evidence.append("%s: does not parse: %s" % (os.path.basename(path), e))
            continue
        if doc.get("schemaVersion") != 1 or not isinstance(doc.get(field), list):
            evidence.append("%s: schemaVersion %r, %s %r" % (os.path.basename(path), doc.get("schemaVersion"), field, type(doc.get(field)).__name__))
    work.result("10.1 every list command's --json parses", not evidence,
                "%d documents parsed by Python's json, each with schemaVersion 1 and its list" % n, evidence)
    return 1 if work.failed else 0


def cmd_metrics(work, args):
    from prometheus_client.parser import text_string_to_metric_families

    with open(args.file) as f:
        families = {fam.name: fam for fam in text_string_to_metric_families(f.read())}
    want_zero = ["magnetar_audit_entries_dropped_refusals", "magnetar_audit_entries_dropped_records",
                 "magnetar_audit_entries_dropped_writes", "magnetar_audit_entries_failed", "magnetar_audit_entries_notices"]
    values, evidence = {}, []
    for name in want_zero + ["magnetar_audit_entries_written"]:
        fam = families.get(name)
        if fam is None or not fam.samples:
            evidence.append("no family " + name)
            continue
        values[name] = sum(s.value for s in fam.samples)
    for name in want_zero:
        if values.get(name, 0) != 0:
            evidence.append("%s = %g" % (name, values[name]))
    written = values.get("magnetar_audit_entries_written", 0)
    if written <= 0:
        evidence.append("no entry written by the batching writer")
    work.result("8.3 the server's metrics count no dropped or failed audit entry", not evidence,
                "/metrics parsed by prometheus_client: written %d, dropped 0+0+0, failed 0, notices 0" % written
                if not evidence else "see evidence", evidence)
    return 1 if work.failed else 0


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--work", required=True)
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("refusals")
    r.add_argument("--mode", choices=["walk", "matrix"], required=True)
    r.add_argument("--proto", required=True)
    r.add_argument("--addr", required=True)
    r.add_argument("--ca", required=True)
    r.add_argument("--grpcurl", required=True)
    r.add_argument("--pg-tenant", default="")
    r.add_argument("--missing-tenant", default="")
    a = sub.add_parser("audit")
    a.add_argument("--sock", required=True)
    a.add_argument("--cutoff", type=float, required=True)
    t = sub.add_parser("tui")
    t.add_argument("--binary", required=True)
    t.add_argument("--data-dir", required=True)
    t.add_argument("--tenant", action="append", default=[])
    j = sub.add_parser("json")
    j.add_argument("doc", nargs="+", help="PATH:FIELD, FIELD naming the list the document must hold")
    m = sub.add_parser("metrics")
    m.add_argument("--file", required=True)
    args = ap.parse_args()
    work = Work(args.work)
    fn = {"refusals": cmd_refusals, "audit": cmd_audit, "tui": cmd_tui, "json": cmd_json, "metrics": cmd_metrics}[args.cmd]
    sys.exit(fn(work, args))


if __name__ == "__main__":
    main()
