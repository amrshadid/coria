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

## Send to another archive

A tenant sends what it holds to a PACS or a DICOMweb service through a sender,
to a destination the installation lists ([Sending](sending.md) has the steps):

```sh
coria setting set sender.destinations "pacs.lab.example ports=104"
coria sender add lab1 pacs --type dimse --host pacs.lab.example --port 104 \
    --called-aet ORTHANC --calling-aet CORIA --enabled
coria sender test lab1 pacs
coria job create lab1 --sender pacs sub-0017 --wait
coria tenant status lab1
```

- **TLS files.** A sender's CA file, client certificate and key are named by
  file name only, and read from `<data-dir>/tls/<tenant>/`, the directory a
  receiver's TLS files are in, so a remote admin cannot point TLS at files
  nobody placed there. Put them there (readable by the server's user only)
  before naming them with `--tls-ca-file`, `--tls-certificate-file` and
  `--tls-private-key-file`.
- **A stop gives the work back.** At a stop signal each sender finishes the
  instance it is sending, records what the peer answered, and gives its unit
  back without spending an attempt; the next start takes it up from there,
  sending again at most one batch (64 instances) per unit. The senders are
  stopped with the receivers and the pipelines, before the API's requests are
  drained, within their own bound of 90 seconds: `--drain` bounds only the
  API's requests in flight, not the sends. A second signal exits at once, and
  a unit cut short is taken up again once its lease runs out.
- **Where to look.** `coria tenant status lab1` shows each sender's health and
  the jobs in progress; `coria watch lab1 --type job-step --type sender-health`
  follows them as they change.

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
removed, and quarantined (`deid_failed`, `residual_value`, `profile_refused`,
`burned_in_risk`) when it cannot be; the storage, the catalog and every list
then hold only what the profile leaves, and UIDs remapped by the tenant's key.
An image whose header says its pixels may carry burned-in text (Burned In
Annotation `YES`; or not `NO` on a secondary capture, an ultrasound, a visible
light (endoscopy, microscopy, photography) or ophthalmic image, or a modality
such as XA, RF, OP, SM, PX or IO; or a whole slide's label or overview image) is
held as `burned_in_risk` unless one of the profile's pixel rules matches it.
Radiographs (DX, CR) are not held by kind: their markers are rarely the person,
and a profile writes a pixel rule for a device known to stamp identity. An image
with only Float or Double Float Pixel Data is not judged by the gate. Pixel data
inside a sequence item, and the Icon Image Sequence, are always removed. A pixel
rule: `redact` fills rectangles in every frame
(the image is then written uncompressed), `detect` fills what the text detector
finds (opt-in, and slow: every frame is decoded and searched), `accept`
archives it as it is, with a reason. Comment and application segments of JPEG
pixel data are always removed, and a frame then holding any segment beyond those
a decoder needs at their defined length is held (`pixel_text_unsearched`); an image left in JPEG 2000, whose segments are
not searched, is held unless the profile says `"unsearchedSegments":
"UNSEARCHED_SEGMENTS_KEEP"`. Work on pixels is bounded by `deid.pixel-limit`
([Settings](settings.md)). A profile version that keeps the UIDs a file is
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

### Route each study by where it comes from

A receiver's routes choose, for each study, the storage it is archived to, and
optionally a profile version that de-identifies it in place of the receiver's
and the senders it is forwarded to in place of the receiver's `--forward-to`.
A study is routed once, for its session: the routes are tried in order on the
first file of its first reception, as received (the first, by SOP Instance UID,
of its first series), and the first whose conditions all hold chooses. A
condition reads the calling or called AE title, or the file's Modality, SOP
Class UID, Manufacturer, Manufacturer's Model Name or Software Versions: never
a patient's name or identifier. It tests equals, starts with, a pattern (RE2,
the whole value) or present, and may be negated.

```sh
coria storage add lab1 lab-a --type dir --root fast
coria storage add lab1 lab-b --type dir --root fast
coria receiver route add lab1 scanner1 lab-a --storage lab-a --equals calling-aet=LAB_A
coria receiver route add lab1 scanner1 lab-b --storage lab-b --equals calling-aet=LAB_B \
    --profile strict@2 --forward-to pacs
coria receiver route default lab1 scanner1 quarantine
coria receiver route list lab1 scanner1
```

- **What is chosen is kept, for the session.** The route that chose, its
  profile version and its senders are kept with the reception when it is
  identified and with the session when its first scan is archived. Every later
  arrival of the session goes by that choice, from whatever AE title it comes;
  a restart, or a change of the routes afterwards, changes nothing for it. A
  reception of a study whose first reception is still being archived goes by
  the first's choice. Should two receptions of a study be routed apart all the
  same (two receivers at the same moment), the files of the second to archive
  are held as `unrouted` (`session_elsewhere`) before any is stored.
- **A route only replaces a profile.** Only a receiver that de-identifies may
  have a route that names a profile version, and the version must pass the
  same checks as a receiver's; a route can never turn de-identification off.
- **A study no route chooses** goes to the receiver's storage with its own
  profile and senders, or, after `route default ... quarantine`, every file of
  it is held as `unrouted`; release them naming the storage:
  `coria quarantine release lab1 q_7Zr2 --storage lab-a`. The release chooses
  the storage only: the routes (mended since, perhaps) still choose the
  profile and senders. A route that cannot be evaluated (one written by a
  later version of the server, or a value in the file longer than 1024 bytes)
  holds the study as `unrouted` too, whatever routes come after it.
- **Trials agree.** `coria profile try --receiver NAME` applies the receiver's
  routes: a file its route would de-identify with another version is tried
  with that version (the answer names the route and the version), and a file
  it would not route is shown held as `unrouted`.
- **Limits.** 64 routes per receiver, 16 conditions per route, a pattern of 256
  bytes with no repetition above 100. A storage or a sender a route names
  cannot be removed while it names it.
- **Changes.** Each `route` command reads the receiver, changes its routes and
  writes them back only if nobody changed the receiver meanwhile; otherwise it
  is refused (exit 5) and changes nothing. In the API the routes are the
  receiver's `routing`, replaced whole by `SetReceiver` with the mask
  `routing`, and `expected_update_time` makes the same check. Changing needs
  admin; reading, reader. Every change is in the tenant's audit log.
