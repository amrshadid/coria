# The command line and the interactive program

[Documentation index](README.md) · [Running CORIA](running.md) · [Settings](settings.md)

`coria <noun> <verb> [name] [flags]`. `coria --help` lists the commands,
`coria <noun> <verb> --help` gives each one's flags with examples, and
`coria completion bash` (also `zsh`, `fish`, `powershell`) writes the shell
completion. The commands are clients of the API and can do nothing it does not
([internal/cli](../internal/cli/doc.go)); `init` and `serve` are the exceptions
that make and run an installation ([internal/cli/servecmd](../internal/cli/servecmd/doc.go)).

## The commands

| Command | Verbs and what they do |
|---|---|
| `coria init`, `coria serve` | Make an installation; run its server. See [Running CORIA](running.md) |
| `coria version`, `coria whoami` | The build; who the server takes the caller to be and what it may do |
| `coria tenant` | `create <name> [--db-env VAR \| --db-file PATH]`, `list`, `show`, `remove <name> --yes` |
| `coria key` | `create --holder TEXT [--expires 90d \| --no-expiry] [--network CIDR]... [--client-cert FILE \| --client-cert-sha256 HEX]`, `list`, `show`, `rotate <id> [--overlap 24h]`, `set <id>` (networks, client certificate), `revoke <id> [--with-successors]` |
| `coria grant` | `set <key-id> --role reader\|writer\|operator\|admin (--tenant NAME \| --installation)`, `list (--key-id ID \| --tenant NAME \| --installation)`, `remove` |
| `coria setting` | `get`, `set <name> <value>`, `unset`, `list`, each with `[--tenant NAME]`. The names are in [Settings](settings.md) |
| `coria audit list` | Filters `--tenant`, `--key-id`, `--outcome done\|refused\|failed`, `--since`, `--until` |
| `coria storage` | `add <tenant> <name> --type dir\|s3 ...`, `list`, `show`, `set`, `remove --yes`, `test` |
| `coria receiver` | `add <tenant> <name> [--type dimse\|stow] ...`, `list`, `show`, `set`, `remove --yes` |
| `coria reception` | `list` (filters `--receiver`, `--step`, `--in-progress`, `--since`, `--until`, `--study-uid`), `show`, `retry`, `discard --reason TEXT` |
| `coria quarantine` | `list` (filters `--state`, `--reason`, `--receiver`, `--reception`, `--sop-uid`, `--study-uid`, `--since`, `--until`), `show [--header]`, `release`, `reject --reason TEXT` |
| `coria catalog` | `subjects <tenant>`, `sessions <tenant> [<subject>]`, `scans <tenant> [<subject> [<session>]]`, with filters and `--order` |
| `coria scan show` | `<tenant> <subject>/<session>/<scan> [--metadata]`: a scan, and the DICOM header of every instance as JSON |
| `coria download` | `<tenant> <subject>[/<session>[/<scan>]] --to DIR [--quiet]` |

A command that removes or discards something says what it would do and exits
2 until it is given `--yes`.

## How a command behaves

- **Where it connects.** By default the local socket `run/coria.sock` in the
  data directory (`--data-dir` or `CORIA_DATA_DIR`), as the user that runs the
  server, with no key. With `--server https://host:port` (or `CORIA_SERVER`) it
  connects over TLS with the key from `CORIA_KEY` or the file named by
  `--key-file`. `http://` is accepted only for loopback hosts. A call is
  bounded by `--timeout` (30 seconds).
- **A secret is never an argument.** No flag takes a key, a password or a
  database address: a tenant's Postgres address is read from the variable named
  by `--db-env` or the file named by `--db-file`, an S3 storage's secret access
  key from `--credential-env` or `--credential-file`, a server's master database
  address from `--master-env`. Filters that take a key's public id are
  `--key-id`; a whole key given where an id goes is refused. `--key-file` must
  name a file only you can open (`chmod 600`).
- **TLS.** TLS 1.2 or later. `--ca-file` (or `CORIA_CA_FILE`) names the
  certificate authorities to trust for `--server` instead of the system's. A key
  that works only with a TLS client certificate is used with `--tls-client-cert`
  and `--tls-client-key` (or `CORIA_TLS_CLIENT_CERT` and `CORIA_TLS_CLIENT_KEY`).
- **Output.** A table, or with `--json` one document that starts with
  `"schemaVersion": 1` ([internal/cli/output](../internal/cli/output/doc.go)).
  Times are shown in the local zone with their offset; there is no color when
  `NO_COLOR` is set or the output is not a terminal.
- **Errors** are one line on standard error with the server's message and its
  request id. Exit codes: 0 done, 1 failed, 2 wrong usage, 3 refused
  (unauthenticated or denied), 4 not found, 5 a state that forbids the change (a
  storage that holds files, a port not allotted), 6 a position that can no longer
  be answered (an event cursor too old), 7 a bound of the server reached.
- **Lists** fetch up to `--limit` (100) items and say when more exist; `--all`
  fetches every item.
- **The catalog** serves only the combinations of filter and order an index
  answers, and the server names them when it refuses one: newest first by
  default; a label prefix with `--order label`, a study date range with
  `--order study-date`, a description prefix with `--order series-description`.
  `catalog scans` of a subject named alone lists by label.
- **Downloads** are laid out `<dir>/<subject>/<session>/<scan>/<instance>.dcm`,
  each written as `.part` and renamed only once its SHA-256 matches the one the
  server announced. Run again, a download skips what is complete and resumes a
  `.part` by range ([internal/cli/download](../internal/cli/download/download.go)).

## The interactive program

`coria` with no arguments, when standard input and output are both a terminal,
opens a program with a menu, lists that scroll and detail screens: tenants (with
each tenant's grants, settings and audit log, its status that follows its events,
and its storages, receivers, receptions and quarantine, where a held file is
released or rejected), the subjects, sessions and scans of a tenant with a scan's
headers and a download of what is shown, keys, settings and the audit log. It
finds the server with the same flags and environment as the commands and is a
client of the same API ([internal/tui](../internal/tui/doc.go)). With any
argument, or with either end not a terminal, `coria` prints the usage line and
exits 2.

- **Keys.** Arrows or `j`/`k`, page up and down, enter to open, esc to go back
  (or to cancel a call that is waiting), `/` to filter the loaded rows, `?` for
  the keys of the screen, `q` to quit. A list loads 100 rows and the next 100
  when the cursor nears the end.
- **A new key** (`n` on the key list) is shown once, alone, and leaving that
  screen asks once more.
- **Nothing destructive happens on one key.** Revoking a key and removing a
  grant, a setting override or a tenant need its id or name typed.
- **Terminal.** It fits 80 by 24 and redraws on resize; there is no color when
  `NO_COLOR` is set, with `TERM=dumb`, or on Windows. The terminal is restored
  on quit, on SIGTERM and SIGINT, and on a panic.
