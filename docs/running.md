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
the storage, checked again by a separate read for the identifying values the
profile removed or replaced (names, identifiers, dates and UIDs; not every
removed value: a number or a description a profile removes is not searched
for, though a name typed into it is, through the name; an identifier made of
words alone, such as an institution's or a station's name, with no digit and
in a script written with spaces, is found only as a whole word, `_` counted as
part of a word, so "TOSHIBA" is not found in a kept maker's name
"TOSHIBA_MEC"; every other identifier, and every one that identifies the
patient (Patient ID, Accession Number, issuers and the like), is found
wherever it stands, glued to other text included), and quarantined
(`deid_failed`, `residual_value`, `profile_refused`, `burned_in_risk`) when it
cannot be; the storage, the catalog and every list then hold only what the
profile leaves, and UIDs remapped by the tenant's key. A file held at such a
receiver is released only through a receiver that de-identifies (a release
through one that archives as received is refused, `FAILED_PRECONDITION`), and
one whose receiver was changed to archive as received after its release is
held again (`deid_failed`, cause `no_profile`): it is never archived as
received. A release may name the profile version it is de-identified with
(`coria quarantine release lab1 q_7Zr2 --profile research@2`, admin; the
version must pass the same checks as a receiver's), in place of the one its
route or receiver names, for that reception's files.
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
and cannot be released into the archive. A file in the Deflated Explicit VR
Little Endian transfer syntax is held at such a receiver (`deid_failed`, cause
`check_unreadable`): the check after reads the bytes as written, which a
deflated file holds compressed, so it cannot show the identifying values are
gone. A STOW-RS receiver that
de-identifies answers each stored instance by the SOP Instance UID it was sent
with and gives no Retrieve URL, neither the instance's nor the study's: the
archive holds it under remapped UIDs, and a URL by them would hand the sender
the mapping. How it works, step by step: [the reception pipeline](../internal/receive/doc.go).

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
no value; `profile describe` says in plain words what a version does, with its
method record and every rule it runs;
`profile diff` shows what a new version changes. Trying needs the operator role
(it answers the files' values, and can read held files); making a version needs
admin. Every command takes `--json`.

A rule can also choose elements by their value, and cut only part of a value.
A `valuePattern` selector (an RE2 pattern of at most 256 bytes; the whole value
by default, anywhere in it with `"search": true`) selects the SH, LO, UC, ST,
LT, UT and PN elements whose text matches, at any depth, each value of a
multi-valued element apart with its padding removed. Matching is exact: case
counts unless the pattern says `(?i)`, and an accented letter written composed
differs from the same letter written decomposed. A search takes the longest
match at each place, so `MRN|MRN\d+` finds the whole MRN. A rule naming an
element by its tag, path, private creator or group wins over it, and it wins
over a rule of a whole VR. The `redact` action cuts the matches of its pattern
out of a text value and keeps the rest: with `{"redact": {"pattern":
"MRN\\d{6}"}}` on Study Description, `Head CT MRN123456 follow-up` becomes
`Head CT  follow-up`. A name component is cut apart from the others, so a name
keeps its `^` and `=`; but a person's name is never partly kept, so redact on a
PN removes it as keep would.

Neither ever passes a value unread or half done: the file is held when a value
a pattern would read is longer than 64 KiB (`too_large`) or cannot be read as
text (`bad_value`), when a pattern finds more than 256 distinct texts in one
value (`too_many_matches`), when the value left after a cut would not be valid
for its VR or would not fit its element (`redact_invalid`), and when a file's
patterns would run past their bound of work (`too_large`: the bytes each reads
times its compiled size). Every distinct text a pattern rule removes or cuts is
searched for by the check after, as a whole word when it begins or ends with a
letter, so a copy of a redacted MRN kept elsewhere in the file holds it.
`profile try` shows each element's `redactions` and `patternMatches`, and
`profile explain` names the selector `value_pattern`. A version that uses
either is of language edition 2; versions that do not keep their checksums.

### Describe a version, and try it with no server

`coria profile describe lab1 research@1` says what the version does in plain
words: for each class of element (names of persons, identifiers and addresses,
dates, times and ages, UIDs, private elements, pixels, the rest), which
elements are removed, kept, emptied, remapped, shifted, hashed or looked up,
those under a condition marked; the defaults and what is never kept whatever
the rules say; and the method record every file it makes carries, the text a
DICOM Conformance Statement needs (PS3.15 E.1.1, E.3): the PS3.16 CID 7050
codes in De-identification Method Code Sequence (0012,0064) with their
meanings, the values of De-identification Method (0012,0063) this server
writes (the meanings, the version, CORIA's and go-dicom's versions), Patient
Identity Removed (0012,0062) YES, and Longitudinal Temporal Information
Modified (0028,0303). A version whose own rules keep what an Annex E base
changes claims no code, and says why. Then every rule in order (`--no-rules`
leaves them out); `--json` gives all of it. The classes are judged from each
rule's selector and VRs, not from a file: an element's own decision is
`profile explain`'s. Needs the operator role.

`coria profile try --local --document draft.json ./samples/` runs the same
trial as `profile try` inside the command, with no server and no tenant: the
engine and the check after run in the process, under keys made for the run and
thrown away, every lookup `UNASSIGNED`, nothing sent and nothing written. The
document may be a version's export (`profile export`, or its `--json` answer,
which names the version; one whose checksum this build does not reach is
refused). Files are read one at a time, each up to 64 MiB, so a directory of
any size can be tried; the verdict is de-identification's alone, with
`receiver.header-limit` and `deid.pixel-limit` at their defaults. The answer
and `--json` are those of a server's trial with no receiver.

### Find a subject from its identifier, and fill lookup tables

The archive keeps no map from a label or a code back to the identifier it
replaces. When a person withdraws consent, an admin finds their data the
forward way, from the identifier the site holds:

```
coria deid code lab1 --patient-id -          # reads the ID from standard input
coria deid code lab1 --value - --json < id.txt
```

The answer is the keyed hash of the value under the tenant's naming key (what
`{#PatientID}` writes into a label and a `hash` rule into a file), the subject
label each receiver whose naming rule makes it from that hash alone gives the
value, whether the catalog holds that subject now, and the code each lookup
table holds for the value. `--attribute` names another attribute (a keyword or
eight hexadecimal digits); the value's padding is removed as its VR says,
exactly as at reception. It needs the admin role on the tenant, takes one value
per call, at most 60 calls a minute a key and 60 a minute into one tenant (exit
7 past either), and each call is in the tenant's audit log, written before the
answer (a call whose entry cannot be written is refused), and in the server's
log, with the key and the attribute, never the value. `--patient-id -` and
`--value -` read the value from standard input, keeping it out of the shell's
history and the process list. The call is never served over HTTP GET, so a
value never travels in a URL.

A `lookup` rule replaces a value by the code its table gives it. Codes are
assigned in order on first sight (`"onMissing": "LOOKUP_MISSING_ASSIGN"`), or
chosen by a person beforehand: then a file whose value has no code is held
(`deid_failed`, `lookup_missing`), and the codes are imported from a CSV of
value and code:

```
printf 'value,code\n98890234,SUBJ001\n' > cohort.csv
coria profile lookup import lab1 subjects cohort.csv
coria profile lookup list lab1
```

Each value is stored as its keyed hash, never as given (its padding removed as
an LO value's). An import is all or nothing, at most 10000 entries: one value
with two codes in the file, or one code for two values, refuses all of it,
naming the entry; values the table gives another code, or codes it gives
another value, refuse all of it too, answered by how many conflict and never
which, so an import cannot be used to learn whether a person is in the table.
An entry it already holds is counted unchanged. A code assigned later in order
passes over every imported code. A key makes at most 10 imports an hour,
stored or refused (exit 7 past it). Every import is audited before its answer,
with counts and no value or code. Import needs admin; `lookup list` (each
table's count, nothing else) needs operator.

### Check the archive, and dry-run a version over it

```
coria deid check lab1 --wait
coria deid dry-run lab1 research@2 sub-0017 --wait
coria job failures lab1 <job id> --warnings
```

Both are jobs of the kind deid that change nothing: no catalog row, no stored
byte, no lookup table. `deid check` verifies each archived instance against the
profile version its catalog row records, by what that version would leave of
each element the archive holds: no element it removes (a private element it
does not keep among them), no value in an element it empties
(`kept_value`), nothing but what the IOD keeps of an element it removes or
empties unless required; Patient Identity Removed (0012,0062) YES;
De-identification Method (0012,0063) naming the version; and no UID outside the
standard's registry that a file the tenant received carried, found by
remapping the file's UIDs with the tenant's UID key, as the reception did, and
looking them up in the catalog (the lookup answers only whether one was found;
a UID the remapping itself made, as in de-identified data received again, is
none). Only Study, Series and SOP Instance UIDs the catalog holds are
recognized: an original Frame of Reference UID, or a Referenced UID of an
instance never archived, is not. An instance archived as received passes with
a note. An archived file no longer holds the Patient ID a version that shifts
dates keyed each subject's shift by, so both jobs give the engine the
instance's subject label in its place; a check compares no shifted date, only
which elements remain. `deid dry-run` runs a version (NAME alone is the latest, fixed when the
job is made; `--own` runs each instance's own) over each archived file in
memory, as `profile try` does: keys made for the job, never the tenant's; no
lookup table read; the check after; no receiver's checks. It says what the
version would archive and what it would hold and why.

A rule's conditions read the instance's reception: its receiver, its calling AE
title when its files came with exactly one, and its receiver's called AE title
as configured now. A condition on Modality, SOP Class UID, Manufacturer, Model
Name or Software Versions reads the archived file, which says what the file
said only where the archiving version kept that element. In a check, an element
whose decision rests on such a condition, or on a reception fact not known, is
a note (`check|undecidable|<tag>`), not a finding; in a dry run, the version
decides on what the archive says.

With no target a job judges the whole tenant. `job watch`, `show`, `failures`,
`cancel` and `retry` work on them as on send jobs: a unit per scan, progress
recorded every 64 instances or second, a cancel seen within one batch, a
restart judging again at most one batch per unit. `job show` adds the counts
per rule and per reason (`rule|<rule>|<action>`, `hold|<reason>|<cause>`,
`check|<finding>|<tag>`, `class|<SOP class>|passed`), names and tags only,
never a value read from a file; `--json` gives them as `tallies`. A file larger
than 64 MiB is not read and passes with a note (`too_large`); one whose bytes
differ from the catalog's checksum is `damaged_in_storage`. A dry run holds
about three times the file it judges, and a pixel rule's image within the
lower of `deid.pixel-limit` and 256 MiB. The server works at most two units of deid
jobs at once, one per tenant, and their reads count in the same bound as the
API's and the senders'. Both need the operator role.

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
  it would not route is shown held as `unrouted`. A held file (`--record`) of
  a study that already has a session is tried by the session's recorded
  choice, as its release would be, not by the routes as they are now. At a
  receiver that archives as received no route's version is used.
- **Limits.** 64 routes per receiver, 16 conditions per route, a pattern of 256
  bytes with no repetition above 100. A storage or a sender a route names
  cannot be removed while it names it.
- **Changes.** Each `route` command reads the receiver, changes its routes and
  writes them back only if nobody changed the receiver meanwhile; otherwise it
  is refused (exit 5) and changes nothing. In the API the routes are the
  receiver's `routing`, replaced whole by `SetReceiver` with the mask
  `routing`, and `expected_update_time` makes the same check. Changing needs
  admin; reading, reader. Every change is in the tenant's audit log.
- **Edit the routing as a file.** `coria receiver route export lab1 scanner1
  --to routes.json` writes the receiver's name, the time it was last changed
  (`updateTime`) and its whole routing (the API's `ReceiverRouting` as JSON);
  `coria receiver route import lab1 scanner1 routes.json` writes it back
  whole, only if the receiver was last changed at that time (exit 5 and no
  change otherwise: export again and redo the edit). A file without an
  `updateTime`, a file exported from another receiver, or
  `--ignore-update-time`, is written over the receiver as it is when read; a
  field the routing does not have is refused, never ignored.
