# Running CORIA

[Documentation index](README.md) · [The command line](command-line.md) · [Settings](settings.md) · [Architecture](architecture.md)

For someone who installs and operates an installation. The commands are in
[the command line](command-line.md); this page is the order to use them in and
what the server needs.

## Make an installation

```sh
coria init --data-dir /var/lib/coria
```

`init` makes the data directory, the **key file** (`key`) and the master
database (`master.db`, SQLite), and prints where the key file is. The key file
seals every secret the installation keeps; losing it loses them all, so copy it
somewhere only you can read. `init` refuses a directory that already holds an
installation. The data directory also holds `tenants/` (a SQLite file per tenant),
the incoming area, and `run/` (the local socket `coria.sock` and the lock that
keeps a second server away).

To keep the master database in Postgres instead, name an environment variable
that holds its address: `MASTER_DB='postgres://...' coria init --data-dir ...
--master-env MASTER_DB`, and pass the same `--master-env` to `serve`. An address
is never an argument.

## Run the server

```sh
coria serve --data-dir /var/lib/coria
coria serve --data-dir /var/lib/coria --listen 0.0.0.0:8443 --tls-cert cert.pem --tls-key key.pem
```

Without `--listen` the server answers only on the local socket. Its flags are
`--listen`, `--tls-cert` and `--tls-key` (a renewed certificate in the same files
is picked up without a restart), `--master-env`, `--log-format json|text`,
`--log-level`, and `--drain` (how long requests in flight may run after a stop
signal, 15 seconds by default). Logs and metrics (`/metrics`) are described in
[obs](../internal/obs/doc.go). Which changes need a restart is in
[Settings](settings.md#which-changes-take-effect-without-a-restart).

The master database and each tenant's live in SQLite files in the data directory
or in Postgres. One process writes a SQLite database: the server's lock on its
data directory keeps a second server away, a second process on the same file
would be safe for the data but could wait for as long as the first keeps writing
and then fail as busy, and Postgres is the database for anything that needs
several writing processes.

## The first key

On the local socket the commands need no key: they run as the user that runs the
server. Make the first key there and give it a role, then use it over the network:

```sh
coria key create --holder "operations" --data-dir /var/lib/coria      # shown once
coria grant set <key-id> --role admin --installation --data-dir /var/lib/coria
export CORIA_KEY=cor_...                                              # on the client
coria whoami --server https://archive.example.org:8443
```

Keys, roles, networks a key may come from, and the audit log are described in
[access](../internal/access/doc.go). The roles are reader, writer, operator and
admin, on a tenant or on the installation.

### Name the servers and keys you use

A person who works with several installations, or with a key per tenant, saves
each as a context instead of repeating flags. `coria context add` sends the key
to the server first, saves only if the server accepts it, and prints who the key
is and what it is granted:

```sh
coria context add prod --server https://archive.example.org:8443 --key-file ~/.coria/prod.key --ca-file ~/.coria/ca.pem
coria context add lab1 --server https://archive.example.org:8443 --key-file ~/.coria/lab1.key --ca-file ~/.coria/ca.pem --tenant lab1
coria --context lab1 setting list        # the tenant lab1, from the context's default tenant
coria context use prod                   # the context commands use when none is named
```

The contexts are in a file only you can read, which holds the paths of the key
and certificate files and never a key; see
[Contexts](command-line.md#contexts).

## Make a tenant that receives

The installation says where data and receivers may be; the tenant then uses them
(the same steps, as API calls, are in
[Receiving through the API](api-walkthrough-reception.md)).

```sh
coria tenant create lab1
coria setting set storage.directory-roots fast=/srv/archive
coria setting set receiver.ports 11112 --tenant lab1
coria setting set receiver.allow-as-received true --tenant lab1   # test data only, see below
coria storage add lab1 main --type dir --root fast --path main
coria storage test lab1 main
coria receiver add lab1 scanner1 --storage main --port 11112 --aet CORIA \
    --allow-aet PRISMA1 --allow-from 10.20.0.0/24 --enabled
```

A tenant on Postgres is made with `--db-env VAR` or `--db-file PATH`. An S3
storage is added with `--type s3` and an endpoint the setting
`storage.s3-endpoints` lists. A receiver admits only callers it lists; open to
everyone is something you write down (`--any-caller`). A STOW-RS receiver
(`--type stow --allow-key ID`) takes HTTP requests from the keys it lists.

From here: send a study ([a reception, end to end](reception.md)), look at what
was archived with `coria catalog subjects lab1` and `coria download`
([reading the archive](reading.md)), and follow a tenant with the interactive
program (`coria`).

## Choose what a receiver does with what it receives

A receiver either de-identifies every file with a version of one of the
tenant's de-identification profiles, or archives it as received. A tenant made
now allows only the first: a receiver that archives as received, and so puts
the names and identifiers the scanner sent into the archive, is refused until
an admin on the installation sets `receiver.allow-as-received` to `true` on the
tenant (as above, for test data). A tenant that had receivers before receivers
could de-identify was given `true` when its database was upgraded, so its
receivers go on as they did. See [Settings](settings.md).

A receiver that de-identifies names its profile version (`"profile": {"name":
"research", "version": 1}` in `AddReceiver` or `SetReceiver`, mask
`deidentification`). Every file is de-identified before any byte of it reaches
the storage, checked again by a separate read for every value the profile
removed, and quarantined (`deid_failed`, `residual_value`, `profile_refused`)
when it cannot be; the storage, the catalog and every list then hold only what
the profile leaves, and UIDs remapped by the tenant's key. A profile version
whose pixel rules this version cannot apply, one that keeps the UIDs a file is
placed by (the Retain UIDs option), and a naming rule that puts a value into a
label unhashed (`{PatientID}` rather than `{#PatientID}`) are refused for such a
receiver. A file whose Study, Series or SOP Instance UID lies under the
standard's own root (`1.2.840.10008.`) is held at such a receiver (`bad_uid`)
and cannot be released into the archive. How it works, step by step: [the reception pipeline](../internal/receive/doc.go).

### Make a profile, try it, then point a receiver at it

A profile is a JSON document: its bases (the profiles the server ships, listed
by `ProfileService.ListProfileBases`, such as `annex-e/basic:2026d`) and its
own rules. A version never changes; each document stored under a name makes its
next version.

```
cat > research.json <<'DOC'
{"bases": ["annex-e/basic:2026d"],
 "rules": [{"name": "keep-weight", "selector": {"keyword": "PatientWeight"},
            "action": {"keep": {}}, "reason": "dose calculations need the weight"}]}
DOC
coria profile create lab1 research --document research.json --note "basic, weight kept"
coria profile try lab1 --profile research@1 ./samples/ --changed-only
coria profile explain lab1 --profile research@1 ./samples/one.dcm
coria receiver set lab1 scanner1 --profile research@1
```

`profile try` runs the version over sample files (or held files, `--record`)
exactly as a receiver would, in memory, and shows each element's value before
and after with the rule that decided it, and whether the file would be archived
or held and why. Nothing is stored and no secret of the tenant is used: each
run has keys of its own, so its remapped UIDs, hashes, shifted dates and codes
never equal what the archive holds (only their form carries over), and it reads
no lookup table (every lookup shows `UNASSIGNED`). With `--receiver`, and for
held files, the receiver's own checks run too, so "passed" means that receiver
would archive the file; without one the verdict is de-identification's alone.
The audit entry keeps the counts per rule and reason as evidence the version
was tried, never a value. At most two trials run at once on a server. `profile explain` shows, for one
file, which rule or base decides each element and why the others do not, with
no value; `profile describe` lists every rule a version runs with its origin;
`profile diff` shows what a new version changes. Trying needs the operator role
(it answers the files' values, and can read held files); making a version needs
admin. Every command takes `--json`.
