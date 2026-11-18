#!/usr/bin/env python3
"""Run the built magnetar binary's interactive program under a pseudo-terminal.

This is the outside check of internal/tui: a tool that is not Magnetar's own
code (Python's pty module, a real pseudo-terminal) starts the real binary against
the in-process fake server (internal/tui/ptyserve), presses keys, reads what the
program draws, quits, and asserts what the terminal was left in. It uses only
the standard library, so it runs wherever python3 does.

It shows, on the built binary and not on a test double:

  * the program opens on a terminal with no arguments and draws its screens;
  * a quit key ends it with status 0, and the terminal is restored: the
    alternate screen entered, then left, and the cursor shown again after;
  * a SIGTERM sent mid-session ends it with status 0 and the same restoration;
    a SIGINT sent mid-session ends it with status 130 and the same;
  * the list of tenants asks the server for a page at a time (the helper counts
    the calls the server really answered);
  * with output that is not a terminal, or with any argument, it does not open:
    the usage line and exit status 2, as before.

Usage: check-tui.py --binary PATH --helper PATH
Linux and macOS only: Windows has no pty module.
"""

import argparse
import fcntl
import os
import pty
import re
import select
import signal
import struct
import subprocess
import sys
import termios
import time

ENTER_ALT = b"\x1b[?1049h"
LEAVE_ALT = b"\x1b[?1049l"
SHOW_CURSOR = b"\x1b[?25h"

# The escape sequences of a terminal's output, removed so text can be looked
# for in what the program drew. Cursor movement is dropped, so words on
# different lines are not told apart by position; the checks look for words.
ANSI = re.compile(rb"\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>]")

failures = []


def check(ok, what):
    """Record one assertion and print its result."""
    print(("ok    " if ok else "FAIL  ") + what)
    if not ok:
        failures.append(what)


def text_of(raw):
    """What a person would read in raw terminal output."""
    return ANSI.sub(b"", raw).decode("utf-8", "replace")


class Session:
    """The binary running on a pseudo-terminal of a given size."""

    def __init__(self, binary, datadir, cols=80, rows=24, env_extra=None):
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        env = dict(os.environ)
        env.pop("NO_COLOR", None)
        env.update({"TERM": "xterm-256color", "MAGNETAR_DATA_DIR": datadir})
        env.update(env_extra or {})
        self.proc = subprocess.Popen(
            [binary], stdin=slave, stdout=slave, stderr=slave, env=env,
            start_new_session=True, close_fds=True,
            preexec_fn=lambda: fcntl.ioctl(0, termios.TIOCSCTTY, 0),
        )
        os.close(slave)
        self.master = master
        self.raw = b""

    def read(self, timeout=0.2):
        """Read what is available for up to timeout seconds."""
        end = time.time() + timeout
        while time.time() < end:
            ready, _, _ = select.select([self.master], [], [], max(end - time.time(), 0))
            if not ready:
                break
            try:
                chunk = os.read(self.master, 65536)
            except OSError:
                break
            if not chunk:
                break
            self.raw += chunk

    def wait_for(self, text, timeout=20.0):
        """Read until the drawn text holds text; False when it never does."""
        end = time.time() + timeout
        while time.time() < end:
            self.read(0.1)
            if text in text_of(self.raw):
                return True
            if self.proc.poll() is not None:
                self.read(0.2)
                return text in text_of(self.raw)
        return False

    def press(self, keys):
        """Type keys, as bytes, into the terminal."""
        os.write(self.master, keys if isinstance(keys, bytes) else keys.encode())

    def finish(self, timeout=20.0):
        """Wait for the program to end; returns its status, or None on a hang."""
        end = time.time() + timeout
        while time.time() < end:
            self.read(0.1)
            code = self.proc.poll()
            if code is not None:
                self.read(0.3)
                os.close(self.master)
                return code
        self.proc.kill()
        self.proc.wait()
        os.close(self.master)
        return None

    def restored(self):
        """True when the terminal was left as found: the alternate screen
        entered and then left last, and the cursor shown after the last enter."""
        i, j = self.raw.rfind(ENTER_ALT), self.raw.rfind(LEAVE_ALT)
        return i >= 0 and j > i and SHOW_CURSOR in self.raw[i:]


def helper_stats(helper):
    """Ask the helper how many calls each method has had."""
    helper.stdin.write(b"stats\n")
    helper.stdin.flush()
    line = helper.stdout.readline().decode()
    return dict((k, int(v)) for k, v in (p.split("=") for p in line.split()[1:]))


def start_helper(path, *args):
    """Start the fake server helper and return it with its data directory."""
    helper = subprocess.Popen([path, *args], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    line = helper.stdout.readline().decode().strip()
    if not line.startswith("DATADIR="):
        raise SystemExit("the helper did not start: %r" % line)
    return helper, line[len("DATADIR="):]


def stop_helper(helper):
    helper.stdin.close()
    try:
        helper.wait(timeout=5)
    except subprocess.TimeoutExpired:
        helper.kill()


def scenario_quit(binary, helper_path):
    print("-- quit with q")
    helper, datadir = start_helper(helper_path)
    try:
        s = Session(binary, datadir)
        check(s.wait_for("local operator"), "the header says who is connected")
        check(s.wait_for("Audit log"), "the menu is drawn")
        s.press("1")
        check(s.wait_for("t000"), "the tenant list is drawn after a key")
        check("connection refused" in text_of(s.raw), "a tenant that is not ready shows its reason")
        s.press("q")
        code = s.finish()
        check(code == 0, "q ends the program with status 0 (got %r)" % code)
        check(s.restored(), "the alternate screen was left and the cursor shown")
        check(ENTER_ALT in s.raw, "the program used the alternate screen")
    finally:
        stop_helper(helper)


def scenario_signal(binary, helper_path, sig, expect_code, name):
    print("-- %s mid-session" % name)
    helper, datadir = start_helper(helper_path)
    try:
        s = Session(binary, datadir)
        check(s.wait_for("local operator"), "the program is up")
        s.press("1")
        check(s.wait_for("t000"), "the list is on screen when the signal arrives")
        os.kill(s.proc.pid, sig)
        code = s.finish()
        check(code == expect_code, "%s ends the program with status %d (got %r)" % (name, expect_code, code))
        check(s.restored(), "after %s the alternate screen was left and the cursor shown" % name)
    finally:
        stop_helper(helper)


def scenario_paging(binary, helper_path):
    print("-- paging on demand, 250 tenants")
    helper, datadir = start_helper(helper_path, "-tenants", "250")
    try:
        s = Session(binary, datadir)
        check(s.wait_for("local operator"), "the program is up")
        s.press("1")
        check(s.wait_for("100+ rows"), "the list says 100+ rows after the first page")
        check(helper_stats(helper).get("ListTenants") == 1, "one page was fetched when the list opened")
        for _ in range(60):
            s.press("j")
        s.read(0.5)
        check(helper_stats(helper).get("ListTenants") == 1, "60 rows down, still one page")
        for _ in range(40):
            s.press("j")
        # The screen is drawn as differences, so one changed digit is not a
        # word to look for; the server's own count of the calls it answered is
        # the proof, read soon, before the list's five-second refresh adds one.
        calls = 1
        end = time.time() + 3
        while calls < 2 and time.time() < end:
            time.sleep(0.05)
            calls = helper_stats(helper).get("ListTenants", 0)
        check(calls == 2, "near the end of the first page the second is fetched (%d calls)" % calls)
        s.read(0.3)
        check(helper_stats(helper).get("ListTenants") == 2, "exactly two pages fetched, not the third")
        s.press("q")
        check(s.finish() == 0, "q ends the program")
    finally:
        stop_helper(helper)


def scenario_small_and_resize(binary, helper_path):
    print("-- resize")
    helper, datadir = start_helper(helper_path)
    try:
        s = Session(binary, datadir, cols=80, rows=24)
        check(s.wait_for("local operator"), "the program is up at 80 by 24")
        fcntl.ioctl(s.master, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 30, 0, 0))
        os.kill(s.proc.pid, signal.SIGWINCH)
        check(s.wait_for("terminal too small"), "a terminal too small says so after a resize")
        fcntl.ioctl(s.master, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 200, 0, 0))
        os.kill(s.proc.pid, signal.SIGWINCH)
        s.press("2")
        check(s.wait_for("nightly export"), "after resizing back the screens draw again")
        s.press("q")
        check(s.finish() == 0, "q ends the program")
    finally:
        stop_helper(helper)


def scenario_not_a_terminal(binary, helper_path):
    print("-- no terminal, or an argument: it does not open")
    helper, datadir = start_helper(helper_path)
    try:
        env = dict(os.environ, MAGNETAR_DATA_DIR=datadir)
        want = b'magnetar: no command (usage: magnetar <noun> <verb> [flags], for example magnetar tenant list); try "magnetar --help"\n'
        # Standard output is a pipe; standard input is the null device, which
        # is a character device and must not be taken for a terminal.
        with open(os.devnull) as null:
            r = subprocess.run([binary], stdin=null, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, timeout=10)
        check(r.returncode == 2 and r.stderr == want and r.stdout == b"", "output a pipe: usage line, exit 2")
        # Standard input a terminal, standard output a pipe.
        master, slave = pty.openpty()
        r = subprocess.run([binary], stdin=slave, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, timeout=10)
        os.close(master)
        os.close(slave)
        check(r.returncode == 2 and r.stderr == want, "input a terminal, output a pipe: usage line, exit 2")
        # Standard output a terminal, standard input a pipe.
        master, slave = pty.openpty()
        r = subprocess.run([binary], stdin=subprocess.DEVNULL, stdout=slave, stderr=subprocess.PIPE, env=env, timeout=10)
        os.close(master)
        os.close(slave)
        check(r.returncode == 2 and r.stderr == want, "output a terminal, input not: usage line, exit 2")
        # An argument, on a terminal.
        master, slave = pty.openpty()
        r = subprocess.run([binary, "tenant", "list"], stdin=slave, stdout=slave, stderr=subprocess.PIPE, env=env, timeout=10)
        out = os.read(master, 65536)
        os.close(master)
        os.close(slave)
        check(r.returncode == 0 and ENTER_ALT not in out and b"t000" in out, "an argument on a terminal runs the command, not the program")
    finally:
        stop_helper(helper)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--binary", required=True, help="the built magnetar binary")
    ap.add_argument("--helper", required=True, help="the built ptyserve helper")
    args = ap.parse_args()

    scenario_quit(args.binary, args.helper)
    scenario_signal(args.binary, args.helper, signal.SIGTERM, 0, "SIGTERM")
    scenario_signal(args.binary, args.helper, signal.SIGINT, 130, "SIGINT")
    scenario_paging(args.binary, args.helper)
    scenario_small_and_resize(args.binary, args.helper)
    scenario_not_a_terminal(args.binary, args.helper)

    if failures:
        print("\n%d check(s) failed" % len(failures))
        sys.exit(1)
    print("\nall checks passed")


if __name__ == "__main__":
    main()
