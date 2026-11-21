# Changelog

All notable changes to CORIA are written here. The form is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- The check after de-identification searches each written file as a stream,
  in overlapping windows, instead of holding it whole: its memory is set by
  the window and the needles, not the file (a 128 MiB file: about 1 MiB
  allocated, where it took 128 MiB). Its reservation counts decoded text at
  up to three bytes of UTF-8 per byte received (a 4 MiB TIS 620 text
  reserves 59 MiB against a measured peak of 25 MiB, where it reserved
  13 MiB) and the automata of the raw forms as built; a trial counts the
  longest text value and the needles of the check it runs.
- Text is compared in Unicode NFC (UAX #15) by clean, the check after, value
  patterns and redact: a name written decomposed (`u` and a combining
  diaeresis) is found and cut as its composed letter. A value or redact
  pattern not written in NFC is refused as `bad_pattern`; before, it matched
  nothing and redact cut nothing. The check no longer reports a raw match
  that a following combining mark composes into another letter ("RENE"
  followed by U+0301 is "RENÉ"), agreeing with clean; a mark that composes
  with nothing, or a reading in another received character set, is still a
  finding.
- JPEG 2000 and HTJ2K pixel data no longer need `unsearched_segments: KEEP`:
  a marker walk removes comment segments from the main header and from every
  tile-part header (the tile-part's Psot and TLM entry lowered to match),
  and the main header's CPF and CRG segments, whose words are free. A CAP is
  taken only when it names Part 15 alone with no reserved Ccap15 bit set.
  A frame holding a marker the walk does not know, PPM or PPT, packet
  lengths that do not add up, or a JP2 wrapper is held. What the walk
  leaves decodes with OpenJPEG to the samples received (`make check-j2k`).

- A STOW-RS receiver that de-identifies gives no Retrieve URLs in its
  answer (they would hand the sender the mapping of its UIDs). A trial of a
  held file whose study already has a session goes by the session's
  recorded route and version. An identifier made of words alone (letters,
  no digit, in a script written with spaces), such as an institution's
  name, is searched for and cleaned only as a whole word; every other
  identifier, and every one that identifies the patient, is found wherever
  it stands, glued to other text included. Public coding-scheme UIDs (PS3.16
  Table 8-1) are kept; any other Coding Scheme, Context, Mapping Resource or
  Context Group Extension Creator UID is remapped.
- A trial run's written file takes its room once instead of growing by
  doubling, lowering its peak memory (a 20 MiB file: 3.9 to 2.1 times its
  size beyond the file).
- The check after de-identification searches only for values that can
  identify someone by themselves: names; identifiers (the attributes of
  PS3.15 Table E.1-1 that identify a person, place, device, order or study,
  GPS coordinates, attributes the data dictionary does not know, and any
  text a rule hashes or looks up); dates; and UIDs the standard does not
  register. Every value of a removed identification code sequence, issuer
  sequence, Other Patient IDs Sequence or Person Identification Code
  Sequence is searched for, at any depth, but numbers, codes, times and
  coding scheme names. Times of day, numbers, coded strings, descriptions
  and comments, and private values but names, dates and UIDs, are still
  removed as the profile says but are no longer searched for, so ordinary
  files are no longer held for a removed weight of 0.000000 found in a
  gantry tilt, a removed series description "Scout" found in "SCOUT MODE",
  or a removed study time of 1200 found in an exposure time; a name typed
  into a description is still found through the name itself. A test fails
  when a row of Table E.1-1 is neither searched for nor listed as no
  identifier. Of pydicom's public corpus, a receiver under the basic profile
  archives 27 files instead of 24, none held for a residual value.
- `job get` is now `job show` (`get` remains as an alias). The stow probe and
  `sender test` send `GET <url>/studies?limit=1` (QIDO-RS) and pass only on
  a 2xx answer; a redirect, 404 (the URL names no DICOMweb service),
  401/403/407 and 5xx each fail it, and a whole STOW request answered 404
  fails the sender instead of refusing its instances. `private-address` no
  longer admits link-local addresses, and 169.254.169.254 and fd00:ec2::254
  are never a destination. A credential file others may read is refused as
  a credential file, not a key file.
- An instance whose file meta information cannot be read, or that names
  another SOP class or instance than its data set, is refused with the new
  code `JOB_FAILURE_CODE_META_UNREADABLE` (`meta_unreadable`) instead of
  `syntax_not_accepted`. The contract states that a STOW-RS answer of 401,
  403 or 407 refuses no instance: it is a failed connection
  (`SENDER_FAULT_REJECTED`) counted toward the sender's health, and what was
  held back is sent once the credential is fixed.
- De-identification's clean removes dates in more written forms (no
  leading zeros, two-digit years, "Mar. 5", ordinals, compact 05032024,
  2024年3月5日, 2024년 3월 5일, "2024. 3. 5."), a date with an unknown month or
  day (00) by the precision it has, full-width letters and digits as their
  ASCII ones, and a year beside a letter of another script; a name's form
  holding a part of one or two letters is removed only as a whole word, so
  "SCAN LEFT" is left whole for the name LE^AN. The forms are shared with the
  check after.
- The rule that an address is a global unicast address (the S3 endpoint
  check) is written once, in `internal/core/publicaddr`, and used by senders
  too.
- The server's TLS certificate is read again from its files when they
  change, at most every five seconds: a renewed certificate is served without
  a restart, and files that do not load are logged while the last good one is
  still served.
- A stream's or a read's key recheck ends before its handler returns.
- Reading the archive is faster. A whole instance's SHA-256 is computed
  while the piece before it is written, so a 16 MiB download goes from 1.4 to
  2.1 GB/s on loopback and a WADO-RS study retrieve from 1.3 to 2.0 GB/s, the
  speed of one SHA-256 stream. ReadFiles messages are sent uncompressed
  whatever the client accepts (gzip ran at about 80 MB/s on DICOM files);
  compressed requests are still read. A range of a download over plain
  HTTP/1.1 is sent by the system's file-to-socket copy (3.2 to 5.0 GB/s). A
  scan's metadata passes each element through (300 instances 38 to 17 ms),
  and a directory storage looks up each directory of a key once.
- STOW-RS: a request holds at most 10,000 parts (413 past it) and each part's
  header at most 16 KiB (400 past it); a receiver that stops cuts a request
  in flight after its drain and answers 503 with Retry-After.
- A duration setting also takes seconds and minutes (`30s`, `15m`), within
  each setting's bounds.
- The everyday tests leave the tests at scale to `make test-scale`, which
  every push still runs in CI.
- A tenant's catalog is leaner and faster to write. A claim on an instance
  is a row of its own, removed by the commit that confirms it, so a held
  instance is written once and never updated; it no longer repeats its
  study, series, storage, key or an archived checksum equal to the received
  one, and on SQLite a scan's instances are stored together. A reception that
  ends done or quarantined no longer keeps its received files (a failed one
  keeps them for its retry or discard), and a study's instances are read from
  an index in order. At 200,000 synthetic instances SQLite receives 2.4 times
  as fast into a full tenant, in a file 6 times smaller, and Postgres's
  instance rows are 3.8 times smaller. Existing tenants are converted when
  the database is upgraded; the upgrade refuses, with a message, a database
  where a held instance is on another storage than its scan.

### Fixed

- A series that the naming rule gives a scan label (or a session label)
  already held by another series (or study) has its files held as a label
  conflict (`held_by_another_scan`, `held_by_another_session`) before
  anything is written; the other scan's files, sidecar and manifest are
  left untouched. Before, the reception failed after its retries, or, into
  an older sidecar, the other scan's sidecar and manifest lost its
  instances.
- A patient whose name is a word of the record of the de-identification
  (the software's name "Coria", or "Basic", "Profile" from a code meaning)
  is no longer held as residual_value: the check after skips De-identification
  Method (0012,0063) and its code sequence (0012,0064) at the top level only
  where they hold exactly what the engine wrote in the same run; any other
  value there, or the same value inside a sequence, is searched as before.
- A file held at a receiver that de-identifies, joining no reception when
  released, could be released through a receiver that archives as received
  and archived identified: such a release is refused, and a landing of it is
  held. `coria deid check` and `dry-run` judge archives made by a profile
  that shifts dates (a job identifies the subject by its label). The
  metadata of a de-identified instance was refused as data loss: it was
  compared with the checksum of the bytes received instead of those stored.

- `job watch` and the Python and TypeScript follow-a-job examples no longer
  stop when the server ends a stream at its 30-second deadline. A unit given
  back after its job ended (a sender removed with `--cancel-jobs`, or a job
  canceled then its sender disabled) is completed and its scan counted,
  instead of staying queued with the job stuck one scan short. An idle
  sender tries a down peer when its next try is due, and a forward made by a
  reception and a tenant's removal reach the senders at once. `sender show`
  prints no TLS line for an http:// stow sender.

- A pipeline's start gives back only the work units of its own kinds that a
  stopped process left leased. It gave back every leased unit, including a
  send unit the server's senders held live: that unit lost its fence, a
  batch could be sent twice, and an attempt was spent at every start of a
  tenant's pipeline. The senders give back only their own kinds, the same
  way.

- `GetQuarantinedFile` with `include_header` returns the held file's header
  on a real server, read from the incoming area within the receiver's header
  limit and written as DICOM JSON without pixel data or any value over 1 KiB;
  `coria quarantine show --header` works against a real server.
- The command-line test server refuses the catalog filter combinations the
  real server refuses.
- A tenant's reception pipeline that failed to start no longer leaves its
  incoming directory open each time it is retried; it has 15 minutes to start
  after a crash instead of 30 seconds; and the bytes of a file rejected or
  discarded while it restarted are removed once the next pipeline has started.
- `coria key revoke` says which keys it revoked before reporting a failure of
  a later call.
- `coria catalog scans <tenant> <subject>` lists the subject's scans by label;
  every such call was refused. A help example named a flag that does not
  exist; every example is now parsed by a test. The root help lists every
  exit code.
- A stalled client of a long method over TLS no longer holds the handler
  five seconds past the stall timeout: a connection whose write timed out
  fails later writes at once.
- An installation's admin sees a tenant whose database stopped as
  unreachable, not as an error of the master; a tenant network rule that
  cannot be read is reported as the tenant's database not answering.
- A STOW-RS request left unread no longer lets a quiet client hold its
  connection.
- `coria download` continues a partial first file by range before it opens a
  stream, so no bytes are fetched twice.
- The outside schema checks cover the key rules, rotation and stow
  receivers.
- Behind a trusted proxy the caller's address is read from one header only,
  named by the setting `access.forwarded-header` (`x-forwarded-for`, the
  default, or `forwarded`); a client could write its own address in the other
  and pass a key's network rule or have another address blocked.
- An installation made before the keys' network rules starts: the master
  database is upgraded before anything reads it.
- A download or metadata read right after another no longer answers 503: a
  storage opened for reading writes nothing, so two opens no longer remove
  each other's probe file, nor a reception's temporary file.
- The limits on failed calls hold under a flood (a new caller 1.3 ms to
  240 ns; the block check of every request 5 ms to 32 ns); refusals by the
  installation's networks and after a malformed message are counted.
- The access settings are read again even when the request that triggers the
  read is canceled, one read at a time, outside the lock every request takes.
  A key's last use is written only by an allowed request.
- A DICOMweb search holds one of the caller's open reads and one scan's
  metadata file at a time, reads at most 256 MiB of metadata files, and
  streams its answer (an instance answer stops at 64 MiB with the more-results
  Warning); a search of 24 instances of 840 KB headers raised the heap by
  72 MiB, now 7 MiB.
- A read of a scan's metadata file that races its rewrite reads the index
  again and tries once more instead of reporting data loss. A WADO-RS request
  for several frames finds the frame table once and refuses repeated frames.
- `coria download` continues a .part only when it is the start of the file
  announced now (its SHA-256 is recorded beside it), and gives a file up as
  soon as more bytes arrive than were announced.
- STOW-RS: a body that ends inside a part is answered 400 and that part is
  not kept; a request refused part way is answered without waiting for the
  rest of its body.
- A test helper of reception no longer moves its clock while a received file
  waits for its row, which made two tests fail on slow runners.
- A file in the Deflated Explicit VR Little Endian transfer syntax is
  archived. Its scan's metadata file records no pixel data place for it
  (offsets counted in inflated bytes are not places in the file); it was
  refused and retried without end.
- The metadata service reads only what it needs from storage: a header read
  passes over pixel data without fetching its frames (a 12 MB JPEG 2000 file
  read under 1%), a scan's page is located by the index at the end of its
  metadata file instead of read from the start, and empty pixel data is
  answered with a reference on every path.
- The setting `api.cors-origins` refuses an IPv4 address in a form no browser
  sends, such as `127.000.0.1`.

### Added

- API contract for importing a directory (defined, not yet served): an
  import receiver type (`Receiver.importer`: a root the installation names
  and allots to the tenant; the plain layout, or a pattern of named path
  parts with a unit depth; an optional CSV map that fills one part, such as
  the subject, from key columns matched to path parts) and an import job
  kind (`Job.import_job`: receiver, path under the root, a map file inside
  the root or inline, and its own map configuration). Two jobs of one
  receiver never read overlapping
  paths at once. Each file's arrival is written with the unit's cursor, so
  none is handed in twice, and the job's counts reconcile what the walk
  found with what each file became (skipped by reason, handed in, archived,
  already held at any revision, quarantined by reason, lost, failed,
  discarded, still processing); the job stays "settling" until every file
  has an outcome, and ends partial if that times out.
  `JobService.ListImportFiles` and `ListImportUnits` (operator) list files
  and units by path. Quarantined files name the import job and, to
  operators, the source path. New settings import.roots (refused where it
  would reach the archive's own directories or the key file),
  import.allowed-roots, import.max-units, import.server-max-units and
  import.max-dir-entries. Paths under an import root and inline maps are
  redacted from logs and never appear in events or audit entries. Each new
  method answers UNIMPLEMENTED.
- Scan metadata sidecars are written in version 3. The first line carries a
  scan header with the study and series UIDs, the receivers of the session
  and the scan, the session's routing, the values the catalog keeps (study
  date, modality, series description, series number) and the created and
  updated times, every one the catalog's after the commit that wrote it,
  never one an attempt that did not commit left on the storage. Each line
  also has its reception, when it was held, the received checksum and
  transfer syntax, how the file arrived, the profile version's checksum and
  the build that wrote it. At start, a pass rewrites every version 2
  sidecar as version 3 from the catalog, and leaves alone, counted and
  logged, a scan where the two disagree.
- A later revision of an instance is kept under a key of its own
  (`<UID>.r<n>.dcm`); correction records (`meta/.corrections/<operation
  id>.json`, the id a lowercase ULID, naming a delete's level and each
  scan's labels, series UID and study UID) and the owner record
  (`meta/.owner.json`) have a fixed, checked JSON form. Nothing writes them
  yet.
- The open reads are settings: `read.per-caller` (default 16, 1 to 1024)
  and `read.per-server` (default 256, 16 to 65536) bound the reads of
  archived bytes (downloads, ReadFiles, DICOMweb) one caller and the whole
  server hold open. Both are read live, so a change applies to the next
  read within a second, and reads already open keep their place when a
  limit is lowered. A read past a limit is still refused, never queued
  (resource exhausted; HTTP 429 with Retry-After). A change that would put
  per-caller above per-server is refused, naming the setting.
- The audit logs are sealed: every `audit.seal-interval` (default 10m) and
  when the server stops, a pass gives each unsealed entry of the
  installation's log and of every tenant's its place in a SHA-256 hash chain
  and signs a checkpoint with an Ed25519 key derived from the key file. The
  installation's log keeps each tenant's latest checkpoint and each tenant's
  log the installation's, so a log rewritten whole disagrees with the other.
  Verification and export are in place for the API, which does not serve
  them yet; `make check-auditchain` checks a sealed SQLite and Postgres log
  with hashlib and openssl. `audit.retention` (default 0: keep every entry)
  prunes whole sealed checkpoints older than it, keeping every checkpoint so
  the chain is still checked across what was pruned, each pruning writing
  its own entry made by the server. Migrations: master `0004_audit_seal`,
  tenant `0017_audit_seal`.
- Import paths and layouts: the rule of a path under an import root
  (segments of 1 to 255 bytes of UTF-8, none "." or "..", none holding "\",
  ":" or NUL, at most 4096 bytes), the walk order (segment by segment, a
  directory's subtree before its next name), when two jobs' paths overlap,
  and layouts that read named parts from a file's path with an anchored RE2
  pattern and a unit depth; a path the layout does not match is skipped as
  outside_layout. Map files: a CSV table whose configured key columns give
  one more part (such as a session's subject by project and session),
  strict UTF-8, at most 3 MiB inline or 64 MiB from a file, one value per
  key, refusals naming a line by number, never a cell, the header or a
  path. Naming rules may use parts a layout and map give; a rule using a
  part nothing gives, or one two sources would fill, is refused when set.
  Not yet used by a served import.
- Outside tools for checking an archive without CORIA (`make
  check-prove-tools`): `audit_verify` (an exported audit log's chain and
  signatures, against public keys the auditor gives), `catalog_dump` (a
  tenant catalog from SQLite or Postgres as canonical JSON, to compare a
  live and a rebuilt catalog) and `backup_open` (a backup opened and checked
  with the key file, its plaintext staged until every chunk opens).
- `make prove-viewer`: OHIF Viewer and headless Chromium in containers open
  public studies (CT, MR, multi-frame, JPEG baseline, JPEG 2000, RLE)
  through DICOMweb with a reader key, and the harness reports blank
  pictures, failed requests and the most reads kept open at once.
- The contract, defined and not yet served, for keeping the archive whole:
  scrub and rebuild jobs (JobService; a rebuild always builds a shadow
  catalog in a scratch schema that must be empty and apart from the
  tenant's own, and, to replace, makes it live in one transaction; failure
  codes deleted_in_archive and not_online; each failure names the revision
  it concerns); CorrectionService to delete a subject, session, scan or
  instance with a required reason and a single-use confirm token from a
  preview that also says what each storage will do with a removed object,
  a grace period fixed at commit, undo, purge and allowing a deleted target
  to arrive again, to relabel a whole target in one switch, to replace an
  instance at a new revision from its quarantined corrected file or a
  re-sent copy of a damaged one, to list scrub findings and to resolve
  (instance, revision) pairs as current, superseded, deleted or unknown;
  every correction has a time-ordered operation id, moves through prepared,
  committed, purging, purged, undone or abandoned, reports per storage
  whether objects were removed, hidden, retained or unknown, and a repeated
  Delete, Relabel or Replace answers the correction it made;
  AuditSealService to verify an audit log's signed hash chain over a range
  of positions (optionally against an anchor kept outside), list its
  checkpoints, export it with the public keys and anchors that check it
  outside the server, across a tenant's move between installations, and
  read the Ed25519 public keys; BackupService to stream an encrypted backup
  with a recovery point per database and no catalog, and to hand a tenant
  to another installation with a one-time transfer key;
  TenantService.ReclaimTenant to undo a hand-off that did not go ahead;
  AccessService.ReinstateKeys for keys a restore suspended. Archived files
  and metadata name their revision; tenants gain the restoring, moved and
  fenced states; keys can be suspended; errors carry reasons as
  google.rpc.ErrorInfo; an S3 storage gains purge_versions and its status
  its versioning and object lock; a tenant's status gains its last scrub,
  open findings, audit seal, pending deletions, event history and owner
  generation; the event feed gains scrub findings, catalog changes and
  catalog rebuilds, and its cursors are bound to a history
  (EVENT_HISTORY_CHANGED); a file matching deleted data is quarantined as
  was_deleted, and one repairing a damaged instance as repairs_damaged.
  Both clients expose the new services and methods; each new method
  answers UNIMPLEMENTED until it is served, including the two new methods
  of served services.
- Keys derived from the installation key with HKDF-SHA-256 (RFC 5869), one
  fixed label per purpose (`coria/audit-seal/v1`, `coria/backup/v1`), from
  any key version, and an Ed25519 signer (RFC 8032) from the audit seed;
  neither prints its key under any fmt verb, log/slog or encoding/json,
  held by value or by pointer, in an exported or an unexported field. A key
  file's versions run from 1 to 2147483647, the largest the API's int32
  carries.
- A chunked backup cipher: AES-256-GCM (NIST SP 800-38D) over 64 KiB chunks
  with a per-file key from a random salt, the chunk index and a last-chunk
  flag in nonce and associated data, so a cut, reordered, changed or
  extended file does not open; streamed in bounded memory, and the reader
  and writer print a fixed text, never a key or plaintext. Checked against
  Python's cryptography package (`make check-backup-cipher`) and fuzzed.
- A canonical, length-prefixed audit entry encoding, the SHA-256 hash chain
  over sealed entries, Ed25519-signed checkpoints of the chain's head, a
  streaming verifier that names the position and kind of any change, and
  the public key as PEM (PKIX). Along a log a checkpoint's key version and
  time never go down, so a retired key cannot append after its successor;
  a checkpoint kept outside the databases (an anchor) finds a tail deleted
  from them. Checked with Python's hashlib and `openssl pkeyutl -verify
  -rawin` (`make check-auditchain`).

- `make prove-deid`: the proof of de-identification from outside, driving
  the built program through its API and command line and reading the
  archive with pydicom, dcmtk and sqlite3. It looks for planted values in
  every archived file, manifest, database and log, and covers every way a
  file is held and its release, pixel rules, routing, value patterns, JPEG
  and JPEG 2000 comment segments, names written decomposed, the forward
  lookup, `profile try --local`, `deid.memory`, and the server killed while
  archiving. `make check-j2k` checks the JPEG 2000 walk against OpenJPEG.
- A tenant's senders and jobs are kept in its database (both database
  types, migration 11): senders with their sealed credential, send jobs of a
  subject, a session or a scan, each made of one unit of work per scan with a
  fenced cursor, counts and failures; a receiver's forward_to makes one job
  per sender when a reception reaches done, in the same transaction; cancel
  and retry; the events job_step and job_progress written with their change.
  An instance whose archive ended after its scan was listed is sent and grows
  the job's totals; a job whose listing runs out of attempts ends at once with
  its units not started, and its counts are final; a unit can be given back
  for a cause not its own (its sender down, no read slot free) without using
  one of its 30 attempts; an instance a STOW-RS peer leaves unaccounted is
  kept owed and tried again by name, never holding the unit's place back; a
  reception that archived nothing makes no forward job. Adding a sender
  audits its type and destination, never its credential. Not served by the
  API yet.
- A receiver de-identifies what it receives with a version of one of the
  tenant's profiles, or archives it as received (`Receiver.deidentification`).
  Each file is de-identified in the archive step, written beside its original,
  checked again for every value the profile removed by a separate read, then
  compared with its checksum, put on the storage and read back before its row
  is committed; each instance records its profile version. At such a receiver
  every row of the tenant (received files, receptions, claims, quarantine,
  catalog) holds UIDs remapped by the tenant's UID key and a keyed received
  checksum, and each row says how the file was recorded, which decides how it
  is archived: a file recorded as received is never archived as it was at a
  receiver that de-identifies (`deid_failed`, `recorded_as_received`; its
  release lands it again, de-identified, through the receiver the release
  names, also after a restart). A placing UID under the standard's own root
  is held as `bad_uid`. The catalog values, sidecar and manifest come from the
  de-identified file. A file that cannot be de-identified is quarantined with
  `deid_failed`, `residual_value` or `profile_refused`, never archived as it
  was. Such a receiver is refused a profile version with pixel rules (not
  applied yet), one that keeps the UIDs a file is placed by (the Retain UIDs
  option), and a naming rule that puts a value into a label unhashed. A new
  tenant setting, `receiver.allow-as-received` (set by the installation's
  admin, `false` by default; `true` for tenants that already had receivers),
  decides whether a receiver may archive as received.
- De-identification adds no needle for a date or time of only zeros, which
  identifies no one and was found inside ordinary decimals.
- The check after de-identification (`internal/deid/check`): reads a
  de-identified file with the codec-free reader and searches, in one pass
  each, every element's decoded text (values of VR UN and OB and the
  preamble also as UTF-8 and in the received character set) and the raw
  bytes outside the image data in UTF-8 and the received character set, for
  every value the profile removed or replaced, in the written forms clean
  removes; a finding names the element and the kind of value, never the
  value; a file not read whole (a sequence or item cut short), deflated, or
  past its bounds (file, value, needles, bytes of forms, work) is refused,
  never passed. Agrees with pydicom over the public corpus and synthetic
  files.
- A tenant's de-identification profiles and lookup tables are kept in its
  database (both database types, migration 12): each version stored as the
  exact bytes of its document and expanded form with their SHA-256, checked
  on every read, created with an expected latest version in one transaction
  (a document equal to the latest is answered with it, not stored again), at
  most 10,000 versions a profile; lookup codes assigned in order from 1 per
  table, all of a file's codes in one transaction or none, never changed for
  a value; each held instance records the profile version that de-identified
  it (none when archived as received). Each tenant is given two more secrets
  beside its naming key, for UIDs and for dates, made and sealed the same way
  and never answered. Not served by the API yet.
- The de-identification engine walks a sequence a file holds as UN bytes
  (a private sequence of defined length) instead of refusing it, its text
  decoded by the character set the file was written in, which the caller
  passes; text it cannot decode, or that still holds an ISO 2022 escape,
  is never kept. It records the PS3.16 codes of the Annex E profile and
  options in De-identification Method Code Sequence (0012,0064), unless the
  profile's own rules keep what a base changes; writes Longitudinal Temporal
  Information Modified (0028,0303) as each dates option says (PS3.15 E.3.6);
  refuses more than 1,000 lookup codes in one file; and runs a stored version
  only with the Annex E bases of its edition. Under Retain Safe Private a kept
  private element held as UN takes the VR PS3.15 Table E.3.10-1 gives it when
  its value fits that VR; a value that reads as a person's name is removed
  whatever the table says. A profile can select OV, SV and UV.
- The de-identification engine, not yet called by reception: a data set
  de-identified in place by an expanded profile version under a tenant's
  keys, every element visited (sequences, items, private blocks, content
  items wherever they sit), each by the rule the profile's precedence
  chooses, with the VRs validation stored. UIDs remapped to 2.25 UUIDs keyed
  per tenant (registered UIDs kept), dates shifted per subject by a keyed
  offset (times kept, a year or month shifted from its middle, the shift
  written nowhere), keyed hashes equal to the subject labels' codes, dummy
  values of non-zero length valid for every VR, removal unless the object's
  IOD requires the attribute.
  The names (any element of VR PN, a PN attribute written with another VR, a
  private value of unknown VR shaped as a name), identifiers, birth date,
  addresses, residence and telephone numbers of persons are never kept,
  whatever the profile says. Command elements, directory elements (group
  0004) and group 7FE0 apart from the pixel data and its frame table are
  removed from every file, from the one list the profile language refuses to
  name. A date shift is keyed by the Patient ID alone, as the subject label
  is; a year or year-and-month the shift would leave unchanged is emptied.
  De-identification Method records CORIA's and the DICOM library's
  versions. Clean removes the file's values, names in every script and dates
  in their written forms from free text, within a bound. Specific Character Set is the engine's: ISO_IR 192
  when text outside ASCII remains. A file the profile cannot process, text
  that is not UTF-8, a UN value holding items that would be kept, or a fault
  of the engine is refused with a fixed cause and left unchanged; a lookup
  table assigns a code only for a file that succeeds; pixel data is never
  changed.
- The contract of senders and jobs: SenderService adds, reads, changes,
  removes and tests a tenant's senders, a DIMSE peer reached by C-STORE or a
  DICOMweb service reached by STOW-RS, each with its TLS files named, at most
  16 associations and an optional byte rate; a STOW credential is sealed and
  never answered. JobService makes a job that sends a subject, a session or a
  scan to a sender, lists, reads, cancels and retries jobs, and lists each
  instance refused with its reason and the peer's status. A receiver can name
  senders in forward_to. The event feed gains job steps, job progress and
  sender health; a tenant's status gains its jobs in progress and its senders.
  Where senders may send is the installation's (sender.destinations,
  sender.allow-plain-http). The services are not served yet. Both clients
  have them as `senders` and `jobs`.
- SenderService and JobService are served: a tenant's admin adds, reads,
  changes, removes and tests senders (a DIMSE peer or a STOW-RS service), and
  an operator makes, lists, cancels and retries send jobs of a subject, a
  session or a scan and reads what a job refused. A stow sender's credential
  is sealed with the installation key and is never answered, logged or
  audited. A receiver may name senders in `forward_to`; the tenant status
  lists its jobs in progress and its senders.
- Senders send: a tenant's send jobs are carried out by the server. A DIMSE
  sender sends each archived data set by C-STORE exactly as stored, on a
  presentation context of its stored transfer syntax and naming the data
  set's own SOP class; a stow sender sends the archived files by STOW-RS in
  requests bounded by the sender's instance and byte limits. Every byte is
  checked against the catalog's SHA-256 and length, and a damaged instance
  never reaches the peer whole; an instance whose file meta information
  cannot be placed, or names another class or instance than its data set, is
  refused alone. Progress is recorded every 64 instances or second, so a
  crash or cancel resends at most one batch; peer statuses are read as the
  contract says. A send is cut only when the peer takes no bytes for the
  sender's response timeout, so slow links send instances of any size; a
  store answer is read up to 4 MiB. Each connection resolves the host once,
  tries each address sender.destinations allows in turn, and reaches no
  other; each unit reads its sender as stored, so a credential only ever
  goes to the URL it was stored with, and a change of destination, AE
  titles, authentication or credential stops what is in flight. A peer that
  fails three connections in a row makes its sender down and holds its jobs
  without using attempts until a C-ECHO or OPTIONS probe answers; health is
  reported in sender_health events and the status. The API and the senders
  share one bound of 256 open reads, of which the senders hold at most half;
  TestSender tries the peer; the senders take up a new, canceled or retried
  job at once.
- `coria job create --wait` follows the job it makes, with the exit codes of
  `job watch`; `coria tenant status <tenant>` shows receivers, storages, jobs
  in progress and senders with their health; `coria watch <tenant> [--type
  ...] [--cursor ...] [--count N]` follows a tenant's event feed.
  `JobFailure.peer_status_http` says when a failure's status is an HTTP
  status, which `job failures` prints as "HTTP 415" (a DICOM status in
  hexadecimal). A `sender.destinations` line takes a `link-local` flag.
  `BenchmarkSendScan` (`make bench-send`): 64 small instances on one
  association to an in-process peer, about 935 instances/s by DIMSE and
  1,080 by STOW-RS on an Apple Silicon laptop.
- The forward lookup of a value's codes, for a withdrawal of consent:
  `DeidService.Code` and `coria deid code <tenant> --patient-id -` (or
  `--attribute A --value -`; `-` reads the value from standard input) answer
  the keyed hash of the value under the tenant's naming key, the subject
  label each receiver gives it and whether the catalog holds that subject,
  and each lookup table's code. Admin only, one value per call, at most 60
  calls a minute a key and 60 a minute a tenant, never served over HTTP GET;
  every call is audited before its answer, with the key and the attribute,
  never the value, and a call whose entry cannot be written is refused.
- Lookup tables filled by a person: `DeidService.ImportLookupCodes` and
  `coria profile lookup import <tenant> <table> FILE` (a CSV of value,code)
  store each value as its keyed hash, all or nothing; entries that conflict
  with the table are answered by their count only, never which, and a key
  makes at most 10 imports an hour; every import is audited before its
  answer. `lookup list` answers each table's count.
- `ProfileService.DescribeProfile`; `coria profile describe` says in plain
  words what each class of element gets and prints the method record a
  Conformance Statement needs: the CID 7050 codes, the (0012,0063) text,
  (0012,0062) and (0028,0303). `coria profile try --local` runs the engine
  and the check after in the command itself, with no server, under keys made
  for the run. `coria receiver route export|import` write a receiver's
  routing to a file and back whole, refused when it changed since the export.
  An instance's metadata and the scan's sidecar name the profile version
  that de-identified it.
- A release may name the profile version a file is de-identified with
  (`coria quarantine release --profile NAME@VERSION`, admin); it applies to
  the whole reception the file joins. The installation setting `deid.memory`
  (default 2GiB) bounds the bytes de-identification holds in memory at once,
  across every tenant, first come first served: the check after's copy of
  each file, trial runs and dry runs; a file whose check alone needs more
  than the whole bound is held (`check_too_large`) with its bytes and
  archived by a release once the setting is raised.
- Deid jobs: `coria deid check` verifies archived instances against the
  profile version each records, by what that version would leave of each
  element the archive holds (no element it removes, no private element it
  does not keep, no value in an element it empties, only what the IOD keeps
  of one it removes unless required), Patient Identity Removed YES, the
  method naming the version, and no Study, Series or SOP Instance UID a
  received file carried (only UIDs the catalog holds are recognized); an
  element whose decision rests on a condition the archive cannot answer is
  a note. `coria deid dry-run` runs a profile version over archived
  instances in memory under keys made for the job and says what it would
  hold and why. Both are jobs of a new kind, `deid`, over a subject,
  session, scan or the whole tenant; they change nothing, run a unit per
  scan with fenced progress, cancel, retry and the job events, read through
  checked reads within the server's read slots, use only the tenant's UID
  key and only for a check, and answer their counts per rule and per reason
  in `Job.tallies`. Tenant migration 0015 adds the job's mode and profile
  columns and `job_tallies`.
- De-identification profiles can choose elements by their value and cut
  only part of a value (language edition 2). A `valuePattern` selector (RE2,
  at most 256 bytes; whole value by default, anywhere with `search`, longest
  match at each place; exact case and Unicode form) selects SH, LO, UC, ST,
  LT, UT and PN elements whose text matches, each value trimmed, at any
  depth; it ranks below tag, path, private and group rules and above VR
  rules. The `redact` action cuts the matches of a pattern out of a text
  value (a fixed replacement or none). Nothing is passed unread or half
  done: a file is held when a value a pattern would read is over 64 KiB
  (`too_large`) or unreadable (`bad_value`), when a pattern finds more than
  256 distinct texts in a value (`too_many_matches`), when a cut would leave
  a value invalid for its VR or its length field (`redact_invalid`), or when
  its patterns would pass their bound of work (`too_large`). Every distinct
  text a pattern rule removes or cuts is searched for by the check after,
  whole and as a whole word when it begins or ends with a letter. A
  person's name is never partly kept: redact on a PN removes it. TryProfile
  answers `redactions` and `patternMatches`; Explain names the selector
  `value_pattern`. Profiles that use neither keep their edition and
  checksum.
- De-identification works on pixels. A profile's pixel rules fill
  rectangles (redact), or what the text detector finds (detect, opt-in and
  slow), in every frame of the images they match, decoded a frame at a time
  as the file is written, so memory does not grow with the image; a filled
  image is written uncompressed and records Burned In Annotation NO and the
  Clean Pixel Data code (PS3.15 E.3.1), unless the file holds Float Pixel
  Data left unfilled; a detector that finds nothing says so in
  De-identification Method. An image whose header says it may carry
  burned-in text (Burned In Annotation YES; a secondary capture, ultrasound,
  visible light or ophthalmic image, or modality XA, RF, OP, SM, PX, IO and
  the like, unless Burned In Annotation is NO; a whole slide's label or
  overview image) and that no pixel rule matches is quarantined with the new
  reason `burned_in_risk`. Native pixels holding more bytes than their stated
  frames are held (`frames_mismatch`), never filled in part. Pixel data
  inside a sequence item and the Icon Image Sequence are always removed, and
  the check after holds a file in which any is found. Comment and
  application segments of JPEG pixel data are removed, and a frame left with
  any segment beyond those a decoder needs at their defined length is held;
  an image left in JPEG 2000, whose segments are not searched, is held unless
  the profile says `unsearched_segments: KEEP`. The bases
  `annex-e/clean-pixel-data:2026d` and `coria/longitudinal:1` are available.
  `TryProfile` answers each file's pixel work. New setting `deid.pixel-limit`
  (default 1GiB) bounds the decoded image and, at 8 times their length,
  compressed pixels read to remove their segments: an image past it is held
  as `deid_failed`/`pixels_too_large`, its bytes kept, and released once the
  setting is raised.
- A receiver can route each study. A study is routed once, for its session:
  the routes are tried in order on the first file of its first reception, as
  received. A route's conditions read the calling or called AE title,
  Modality, SOP Class UID, Manufacturer, Manufacturer's Model Name or
  Software Versions (a value longer than 1024 bytes is not tested, and the
  study is held), testing equals, starts with, an RE2 pattern or present,
  optionally negated. The first route that holds chooses the storage, and can
  name a profile version that replaces the receiver's (only on a receiver
  that de-identifies: a route never turns de-identification off) and the
  senders a reception is forwarded to in place of `forward_to`. What was
  chosen is kept with the reception and the session; every later arrival of
  the session, and a reception of a study whose first reception is still in
  flight, go by that choice, and should two receptions be routed apart all
  the same, the second's files are held before any is stored. A study no
  route chooses goes to the receiver's storage or, if the routing says so, is
  held as `unrouted`; a release can name the storage (`coria quarantine
  release --storage`), choosing the storage only. The routes are the
  receiver's `routing` field; `expected_update_time` refuses a change to a
  receiver that changed since it was read. `coria profile try --receiver`
  applies the routes. Limits: 64 routes per receiver, 16 conditions per
  route, patterns of at most 256 bytes. New commands: `coria receiver route
  list|add|set|remove|move|default`.
- `ProfileService` is served: a tenant's de-identification profiles are made
  (admin), read, listed, exported and validated (operator) through the API,
  and `coria profile create|get|list|versions|export|validate|describe` with
  `--json`. A document with a problem is refused with every problem in a
  `ProfileProblems` error detail.
- Trial runs: `TryProfile` and `coria profile try` run a profile version, or
  an unsaved document, over sample files or held files as a receiver would,
  in memory, and answer each element's value before and after with the rule
  or base that decided it, and whether the file would be archived, held (and
  why) or undecided (it depends on a lookup table). With a receiver named,
  and for held files, the receiver's own checks run too (placing UIDs, SOP
  classes, naming rule and labels); otherwise the answer says the verdict is
  de-identification's alone. Nothing is stored and no secret of the tenant is
  used: each run has keys of its own, so its remapped UIDs, hashes, shifted
  dates and codes never equal the archive's, and it reads no lookup table. At
  most two trials run at once, and held files are bounded per request. The
  audit entry keeps the counts per rule and reason, never a value.
  `ExplainProfile` and `coria profile explain` show which rule decides each
  element of a file and why the others do not; `DiffProfileVersions` and
  `coria profile diff` show what changes between two versions: rules added,
  removed, changed or moved, the effect per selector, bases reordered, and a
  change no list shows.
- `coria receiver add|set --profile NAME@VERSION | --as-received`.
- `make prove-sending`: the proof of sending, from outside the binary.
  Studies archived from dcmtk's storescu are sent to an Orthanc by DIMSE and
  STOW-RS and checked against the archive with Orthanc's REST API and
  pydicom: a restart midway by SIGTERM and SIGKILL, cancel and retry,
  forwarding, a peer that refuses some instances, de-identified data sent as
  archived, and instances per second by each way.
- `coria context add|list|show|use|remove` saves named client contexts
  (server address, CA and TLS client certificate paths, key file path,
  optional default tenant) in a file only the user can read, in a directory
  only the user can change (`$XDG_CONFIG_HOME/coria/contexts.json`,
  `~/.config/coria/contexts.json`, `%AppData%\coria\contexts.json`, or
  `CORIA_CONFIG`). `add` checks the key with WhoAmI first and prints what it
  is granted. `--context NAME`, `CORIA_CONTEXT` or the current context picks
  one; a named context cannot be combined with `--server`, `--data-dir`,
  `CORIA_SERVER` or `CORIA_DATA_DIR`, and a set `CORIA_KEY` is refused so it
  never travels to a context's server (`--key-file` overrides). A context's
  default tenant fills `--tenant` of `setting get|set|unset|list` and `audit
  list` when it is not given, and says so on standard error. The file is
  refused when it holds a key, user information in a URL or a relative path,
  and a save is refused when the file changed since it was read.
- `coria sender add|list|show|set|remove|test` and `coria job
  create|get|list|watch|cancel|retry|failures`: a tenant's senders and jobs
  on the command line. A stow sender's credential is read from a private
  file, standard input or a variable, never an argument; `sender test` says
  in plain words what the peer answered; `job watch` follows the event feed
  until the job ends and exits 0 only when it ended done. `coria receiver
  add|set --forward-to SENDER` sends each archived reception on. The
  interactive program lists a tenant's senders with their health and a test
  of the peer (`n`), and its jobs with progress bars that follow the event
  feed, a job's failures, and cancel and retry that ask for the id typed
  (`w`). A Python and a TypeScript example make a send job and follow its
  events until it ends; `docs/sending.md` explains sending.
- Watch answers job step, job progress and sender health events with their
  payload: they came with their type alone, so a client could not tell
  which job moved, how far it was, or why a sender was down.
- Settings `sender.destinations` (the hosts and address ranges, with
  optional ports, that senders may reach; none by default),
  `sender.allow-plain-http`, `sender.max-associations`,
  `sender.tenant-max-associations` and `job.failures-kept`. A name that
  resolves to a loopback, link-local or private address is refused unless
  its line says `private-address`; the unspecified, multicast and broadcast
  addresses are never allowed; a range of the whole address space and a host
  name a resolver reads as a number are refused. A sender's change is checked
  against the destinations in one step with its write, and a value that would
  take away a destination or plain http a sender uses is refused, as is
  unsetting it.
- The contract of de-identification profiles: ProfileService makes a
  profile's versions, reads and lists them, checks a document without storing
  it, and lists the bases a profile may build on. A profile is a document of
  rules (selector, conditions, action) and pixel rules; its file form is the
  protobuf JSON of ProfileDocument. A version never changes: it is stored
  expanded, its bases' rules copied in, every keyword written as its tag,
  every default written out, with the VRs validation decided and the language
  edition whose meaning it has, and with the SHA-256 of that text; a stored
  version runs without the data dictionary. A group or VR rule selects private
  elements only when it says include_private, and a content item rule only
  its item's value element. Actions include dummy and remove-unless-required
  (PS3.15's combined codes, resolved by the IOD's Type); pixel data and the
  elements the pipeline removes from every file cannot be named. Constants and
  templates are checked against every VR they may be written into; regular
  expressions are bounded by their compiled size. A document with problems is
  refused with every problem; a valid one is answered with warnings where a
  base's rule overrides or is overridden by its own. A version's expanded form
  is answered on request, and every version fits one answer. Creating a
  version can name the latest version it expects. The bases of PS3.15 Annex E
  (edition 2026d) are built from the DICOM library's Tables E.1-1 and
  E.3.10-1; the two options that change pixels, and coria/longitudinal:1
  which names one, are refused as not available. The service is not served
  yet. Both clients have it
  as `profiles`.
- `coria` and the interactive program can present a TLS client certificate
  (`--tls-client-cert`, `--tls-client-key`, or `CORIA_TLS_CLIENT_CERT` and
  `CORIA_TLS_CLIENT_KEY`), so a key limited to one can be used from the command
  line.
- `make gates`: the checks before each push, never the scale tests; `make
  check` no longer runs the scale tests.
- Keys can be rotated with no gap: `coria key rotate <id> --overlap 24h`, or
  AccessService.RotateKey, which a key may call on itself (keeping at least an
  hour of overlap). The successor has the same grants, holder and network
  rules; a key is rotated once and no successor outlives `key.max-lifetime`
  (3650d). `coria key revoke` names a live successor, and
  `--with-successors` revokes the chain.
- Where a key may be used from: its own networks and optionally a TLS client
  certificate (`--network`, `--client-cert`, `coria key set`,
  AccessService.SetKeyRules); a tenant's networks (`access.tenant-networks`);
  the installation's allowed and denied networks. A refusal is answered as a
  wrong key and audited with the rule's name. Trusted proxies
  (`access.trusted-proxies`) give the real caller in the audit and the rules.
  A key's last-used address is kept and shown.
- Limits on failed calls only (`access.failure-limit`, `failure-window`,
  `block-time`): an address that keeps sending bad keys, and a good key that
  keeps being refused, are slowed and then blocked, each block audited; a
  key's successful requests are never limited. Keys expiring within
  `key.expiry-warning` (7d) are listed in the status.
- The catalog service lists and gets subjects, sessions and scans in the
  orders and with the filters the contract names, with totals only where a
  running total gives them; checked against pydicom's reading of the files.
- The data service streams the instances of an instance, scan, session or
  subject, each announced with its key, place, length and SHA-256 and checked
  as it is sent; a stream resumes after a position and can announce without
  bytes. GET and HEAD of one instance by plain HTTP serve single byte ranges
  (RFC 9110 14), If-Range, If-None-Match and the window of a bulk data URI,
  with the SHA-256 as the strong ETag. A caller holds at most 16 open reads
  at once, shared by its streams, its downloads and its DICOMweb requests,
  the server 256. No deadline applies to these long reads;
  a client that reads nothing for 5 minutes is cut.
- The scan's metadata file records where each frame lies in its file and each
  frame's SHA-256; a pass at each start gives scans archived before their
  tables, resumable and yielding to reception, reported as the event
  `frames_added`.
- DICOMweb per tenant at /tenants/<tenant>/dicom-web, behind the key and
  grant check and audited without UIDs: QIDO-RS answered from the catalog
  with real paging; WADO-RS retrieve of studies, series and instances as
  stored, each checked against its SHA-256 as it is sent, their metadata,
  bulk data and frames (each frame checked against its SHA-256). Nothing is
  transcoded.
- A receiver of type stow, the tenant's STOW-RS service (PS3.18 10.5), closed
  to every key it does not list (a key rotated from a listed one is taken);
  each part is synced in the incoming area before the answer and received
  like a DIMSE file. A part whose placing UIDs read whole differ from those
  read from its first bytes is held as `uid_mismatch`. The key is checked
  again every 15 seconds while a request is open, as for a long read: a
  request whose key is revoked, expires or loses its role ends at its next
  read of the body, answered 401 or 403, keeping the parts held whole before
  it, and is audited as refused.
- `coria catalog subjects|sessions|scans`, `coria scan show --metadata` and
  `coria download` (checked against the announced SHA-256 as it arrives,
  resumed by a range guarded by the checksum); the interactive program
  browses the catalog, shows a scan's headers, downloads, and shows a key's
  rules, successor, last-used address and expiry warning.
- Both clients are tested against the catalog, metadata and data services.
- The proof reads the archive from outside (sections 13 to 19): the catalog
  equals pydicom's reading, every scan's headers equal pydicom's
  to_json_dict, downloads whole and in random ranges equal the manifests with
  curl, a killed `coria download` finishes whole, dicomweb-client searches,
  loads and stores a study that is archived byte for byte, the Python client
  reads, and every way in refuses the same way, each refusal audited.
- The scan's metadata file records each instance's transfer syntax, and an
  index of where its lines start; the metadata service reads both instead of
  opening instance files or reading from the start. Files written before this
  still work.
- The metadata service answers a scan's or an instance's header as DICOM
  JSON (PS3.18 Annex F), with bulk data URIs for pixel data and other large
  values, from one read of the scan's metadata file and a bounded read of an
  instance's file header for what that file does not hold; checked against
  pydicom on the public corpus files.
- The plain HTTP download of an instance, `GET /tenants/<tenant>/instances/
  <subject>/<session>/<scan>/<instance id>`, is routed, checked and audited:
  no key, a wrong key, or a key without reader on the tenant the path names
  is refused as on every route; a path with percent-encoding, a dot segment,
  or an empty or over-long segment is answered 404 after the check and never
  redirected.
- The setting `api.cors-origins` (installation only, empty by default) lists
  the web origins whose pages may call the API from a browser, one per line as
  a browser sends them, or `*` for any. A listed origin's preflight is
  answered with the methods and headers the Connect, gRPC-Web, download and
  DICOMweb clients send (Accept among them, which a WADO-RS media type with
  quotes takes out of the headers a browser sends without asking), and a
  QIDO-RS answer's Warning is exposed; an origin not listed gets no CORS
  header. No answer allows
  credentials; every answer carries `Vary: Origin`.
- The catalog keeps what reading needs: each scan's modality, series
  description and series number and each session's study date, read from the
  files' headers; the receiver that first archived into each; running counts
  of sessions, scans, instances and bytes on subjects and sessions; and an
  index for every list order and filter, so a page of subjects, sessions,
  scans or a scan's instances is read from one index at any size. When the
  database is upgraded, an existing catalog gets its receivers, counts and
  indexes at once; the header values, which only the files hold, are filled
  in by the pass each start makes over the scans, from each scan's first
  file, for every scan the catalog lists without a modality. Until that pass
  reaches a scan, searches that filter by these values (QIDO-RS's StudyDate,
  ModalitiesInStudy and Modality among them) do not find it.
- A generated TypeScript client, `coria` (`clients/typescript`): the API
  contract compiled with protoc-gen-es, one typed client per service over the
  Connect protocol (browser and Node) or gRPC and gRPC-web (Node), a key
  interceptor, a paging helper and one error class carrying the request id.
  Every failure, deadlines and cancellation included, is a `CoriaError`; the
  default deadline applies to single calls, never to server streams.
- Both clients refuse a malformed key without echoing it, refuse to send a key
  unencrypted to another machine unless told to (`allow_insecure`,
  `allowInsecure`), and raise instead of looping when the server repeats a
  page token. The Python client reports the request id of every failed call,
  and its generated modules are `coria.v1.*_pb2`, typed.
- A generated Python client, `coria` (`clients/python`): the API contract
  compiled with grpcio-tools and checked in, a thin `coria.Client` that sends
  the key, exposes each service's stub, pages through list calls by cursor and
  raises one `coria.Error`, and tests that run it against the real server.
- The tenant port states that a tenant holds one instance per SOP Instance
  UID (a file whose UID is held in another series, or a different file under
  a held UID, is quarantined), and its contract suite holds both drivers to
  it.
- Scale measurements, run on demand: a generator of synthetic tenants in
  `internal/testkit`, written through the tenant port; `TestScaleCatalog` in
  each database driver (sizes, insert rates, the catalog's query plans and
  times, index sizes, vacuum time), sized by `CORIA_SCALE_INSTANCES`; and
  `BenchmarkPackedScan` on the directory and S3 drivers (a scan read one
  object at a time against one packed object).
- The contract for reading the archive: `CatalogService` (list and get a
  tenant's subjects, sessions and scans, with filters and paging),
  `MetadataService` (a scan's or an instance's headers as DICOM JSON) and
  `DataService` (a stream of the files of an instance, scan, session or
  subject, each with its key, size and SHA-256; and, as stated in the
  contract, a plain HTTP download of one file with byte ranges). A reader on
  the tenant may call them, and an audit entry never holds a UID or a
  storage key. The
  error kinds gain `DATA_LOSS`.
- The repository had a README and nothing else: no module, no license, and no
  check that a change builds, is formatted or passes its tests. It is now the Go
  module `github.com/amrshadid/coria`, under the Apache License 2.0, with the
  gates every change is judged by: `gofmt -s`, `go vet` on 64 and 32 bits,
  golangci-lint, govulncheck, `go test -race`, and static builds for linux/amd64,
  linux/386, linux/arm64, darwin/arm64 and windows/amd64 with their sizes
  reported. Each gate is one `make` target, and CI runs the same targets.
- A lint rule holds the architecture before its first package exists:
  outside its tests, `internal/core` may import only the standard library and
  its own packages, and from the standard library no database, network, file
  or system package (`net/netip` and `net/url`, which do no I/O, excepted).
- `coria version` (and `coria --version`) prints the version, the commit
  and the Go release the binary was built with, read from the binary's own
  build information, so a report can name the build that wrote the data. Any
  other command is a usage error: one line on standard error and exit code 2.
- A pull request into `main` is refused unless it comes from `develop`.
- The API had no written contract, so handlers, the command line and outside
  clients had nothing fixed to agree on. It is now protobuf, package
  `coria.v1` in `api/proto`: `TenantService`, `AccessService` (keys,
  grants, `WhoAmI`), `SettingsService` and `AuditService`. Each method's
  comment states the role it needs, what it writes to the audit log and the
  errors it can answer, and each read or repeatable change is marked as such.
  The contract also states its limits, the order in which a request is
  checked, the request id the server returns, and when a missing tenant is
  "not found" and when it is "permission denied". Removing a tenant only
  unregisters it and says where its data was left. Secrets cross the API in
  two fields only, the database address of a new tenant and a new key's
  secret; they are marked for the server's redactor, and a test fails on any
  text field of the contract that has not been listed as reviewed. `buf
  lint`, a breaking-change check against the base branch, and a check that the
  committed Go code in `gen/` is current run in CI, with Python's grpcio-tools
  compiling the same files as a second toolchain.
- An error had no way to say what sort of failure it was, so the API and the
  command line would each have had to guess a code from its text. Package
  `internal/fault` gives an error a kind (invalid, unauthenticated, denied, not
  found, conflict, unavailable, internal) and the exit code of each kind, and
  keeps the message a caller may see apart from the cause, which is for the
  log only. An error with no kind is internal.
- There was nowhere to keep a secret such as a tenant database's password
  except in plain text, and no way to make an API key. Package `internal/secret`
  adds the installation key file (one `coria-key-v1 <version> <key>` line
  per key version, created only where no file exists). The file must be private
  to the user coria runs as, or it is refused with the command that fixes
  it: on Linux and macOS a regular file owned by that user with mode 0600 or
  narrower, with no macOS access list entry granting anything, in a directory
  owned by that user that nobody else can write; on Windows a regular file
  owned by the current user whose access list names nobody else. A FIFO at the
  path is refused at once instead of hanging the start. It seals a secret with
  AES-256-GCM under a random nonce, bound to the context it is stored for, so a
  sealed value moved to another row does not open, and opening fails the same
  way whatever is wrong. `scripts/check/seal.py` checks the format against
  Python's `cryptography` package in both directions, installed in CI from
  hashed requirements. API keys look like `cor_<id>_<secret>`, with 256 random
  bits in the secret, of which only the SHA-256 is to be stored, compared in
  constant time. Printing the loaded keys or a new API key, by any `fmt` verb,
  `log/slog` or `encoding/json`, shows no key material.
- `make lint` also lints for Windows and macOS, so files built for one system
  only are linted in CI.
- There was nowhere to keep tenants, keys, grants, settings or the audit log.
  The master database is now reached through a port stated as operations
  (`internal/master`), not SQL, with a contract suite every database driver
  must pass (`internal/master/mastertest`) and a SQLite driver that passes it
  (`internal/driver/sqlite`, on `modernc.org/sqlite`, pure Go, so the binary
  stays static on every target). Every change takes its audit entry and
  writes both or neither; setting a grant replaces its role in one step,
  returns the old one, and is refused on a revoked key; one lookup gives a
  key's roles on the installation and on a named tenant for the access check;
  lists are paged by a cursor bound to the list and its arguments, never by
  offset; times keep their microseconds in UTC; a duplicate audit entry id is
  told apart from a conflict without reading text. SQLite runs in write-ahead
  mode with one writing connection (`BEGIN IMMEDIATE`), a read-only pool,
  foreign keys on, full sync (with `F_FULLFSYNC` on macOS) and a busy
  timeout, and refuses on Unix a database file that others can read or write.
  Migrations are embedded SQL files, checksummed and applied under a lock that
  holds across processes; a changed applied file, or a database a newer binary
  upgraded, is refused. `scripts/check/master-sqlite.sh` reads what the driver wrote
  with the `sqlite3` tool and finds a key's hash but never its secret.
- The binary had a version command and nothing that could use the API, so a
  person had no way to make a tenant or a key without writing a client. It now
  has the single commands of the command line, built with cobra and written
  only as a client of the API: `tenant create|list|show|remove`,
  `key create|list|show|revoke`, `grant set|list|remove`, `setting
  get|set|unset|list`, `audit list`, `whoami` and `version`, with `--help` and
  shell completion. They reach the server on the local socket in the data
  directory (`--data-dir`, `CORIA_DATA_DIR`) or on the network over TLS
  (`--server`, `CORIA_SERVER`) with the key read from `CORIA_KEY` or
  `--key-file`, which must name a file only its owner can open; no flag takes a
  key, a password or a database address, the filters that take a key's public
  id are `--key-id`, and `http://` is refused for any host that is not
  loopback. TLS is 1.2 or later, and `--ca-file` (or `CORIA_CA_FILE`) names
  the authorities to trust for `--server` in place of the system's, for an
  institution with a private authority. Output is a table
  for a person, or with `--json` one document that starts with
  `"schemaVersion": 1`, checked by Python's `json` module in CI. An error is
  one line on standard error with the server's message and request id, and the
  exit code is 0 done, 1 failed, 2 wrong usage, 3 refused, 4 not found, from
  the kind of the error through one table of API codes in `internal/api/codes`;
  a code the contract names no kind for (a message over the size limit, one a
  newer server adds) is a failure, exit 1.
  `tenant remove` sends nothing without `--yes`. Lists fetch up to `--limit`
  (100) items and say when more exist, or every item with `--all`. A new key is
  written alone on its line on standard output and never again. The commands
  are tested against an in-memory server built from the generated handlers and
  served over a Unix socket and over TLS, not against a mock of the client.
  The program grew from 1.6 to 10.2 MiB (linux/amd64: 1.57 to 10.67 MiB), most
  of it the Connect and protobuf client and `net/http`; cobra and its flag
  library account for about 0.9 MiB of it.
- Nothing decided who a caller is or what it may do. `internal/access` now
  has one check that every request will go through: it reads the key by its
  id, compares its secret in constant time, refuses a revoked or expired one,
  then reads the key's roles and the tenant the request named in one
  statement, and compares them with what the request needs, in the order the
  API contract states. Every bad credential gets one answer
  (unauthenticated), and a missing grant and a missing tenant get one answer
  (permission denied) after the same two reads, so a key cannot learn which
  tenants exist; a caller who may list every tenant is told "not found"
  instead. Nothing is cached, so a revoked key is refused on its next
  request, and a check's decision is for one request: the operations on keys
  and grants read the caller's key again and refuse it once it is revoked,
  expired or no longer admin. A refusal is written to the installation's
  log, an allowed request to the log of the tenant or the installation it
  concerns, and to the installation's when a tenant's log does not answer. A
  change is audited in its own transaction; a refusal or a read goes to a
  bounded audit writer, with a queue for each so a flood of bad keys cannot
  push out reads, that appends in batches, retries a log that does not
  answer a bounded number of times, and counts every entry exactly once as
  written, failed or dropped, reporting drops in the log. Text a caller
  controls is escaped before it reaches an entry. A key's last use is
  recorded at most once a minute, by a bounded background worker. Making,
  listing and revoking keys and setting, listing and removing grants need
  the installation admin; a new key expires after 90 days unless another
  period (at most 10 years) or "never" is asked for.
- The master port's `RolesFor` now also reports whether the named tenant
  exists and its id, in one statement on SQLite and Postgres, and a name that
  is not a valid tenant name finds no tenant instead of failing, so the
  access check reads the tenant and the roles at once.
- The contract's comments now say which log each entry goes to (a refusal
  always to the installation's), that the entries of refusals and reads are
  written in batches and can be dropped by an overwhelmed server, which says
  how many, and no longer promise an order of a key's grants by tenant name.
- A tenant had a row in the master and nowhere to keep its own data, and
  nothing opened, upgraded or watched a tenant's database. Each tenant now
  has a database reached through a port stated as operations
  (`internal/tenant`): its overrides of the installation's settings and its
  own audit log, with the master's bounds, the master's entry type and the
  master's rules: each change written with its audit entry or not at all, a
  duplicate audit id told apart from a conflict, cursors bound to their list,
  their filter and their tenant, and the same error kinds. A contract suite
  every driver must pass (`internal/tenant/tenanttest`) runs on SQLite, where
  the master and the tenants now share one implementation of settings and of
  the audit log, and a tenant without an address gets the file
  `<data-dir>/tenants/<name>.db`, private to its owner, built so that no name
  can put it elsewhere. Errors from the SQLite driver now name the database
  they are about, master or tenant, and a symbolic link planted at a
  database's path is refused rather than followed. A tenant may not be named
  for a device Windows reserves (`con`, `nul`, `com1` and the like), on any
  system, so a data directory moved to Windows keeps working. The tenant
  registry (`internal/registry`) holds one open store per tenant and is the
  only way to get one: getting a ready tenant's store takes no lock and makes
  no call to the master (12 ns, no allocation, among 1,000 tenants). It opens
  every tenant in parallel, a bounded number at once, each under its own
  deadline, and checks an open tenant with a short ping that waits for no
  open, so one dead database delays neither the start nor the others; a
  tenant whose database does not answer is `unreachable` and refused while
  the others are served, and returns to `ready` on its own; one whose address
  does not open with the key file, whose database refuses its address or its
  file, whose schema cannot be upgraded or whose driver panics is `failed`
  with its reason. The registry's view of a tenant's state, with its reason
  and the time it was reached, is current the moment a check completes; the
  master records it, with an audit entry whose actor is the system, a
  flapping database at most once a minute. A reason, an error, an audit
  entry and a log line hold only the registry's own words and a driver's
  caller-safe message, never a driver's raw error, so no form of the
  database's address or password reaches them. Creating a tenant is whole or
  nothing: when its database cannot be opened or upgraded, the row is removed
  again, the undoing is audited, and the failure is answered; only a crash
  leaves an `upgrading` row, which the next start finishes, and repeating a
  creation with another address is a conflict. Removing a tenant unregisters
  it and leaves its data in place, and says where. Each round also reconciles
  the registry with the master's list of tenants, so a removal or creation
  whose answer was lost does not leave the two apart. At start it warns when
  the tenants could not all hold their files under the process's limit.
  Closing the registry stops its timer, waits for opens in progress up to the
  deadline it is given, and closes every store. `scripts/check/tenant-sqlite.sh`
  reads a tenant the registry created with the `sqlite3` tool.
- The commands could not be browsed: finding a tenant's grants, its settings
  and its audit entries took a command each, and making a key meant copying
  its secret from standard output. `coria` with no arguments, when standard
  input and output are both terminals, now opens an interactive program (Bubble
  Tea 2.1.1 and Lip Gloss 2.1.1, MIT, with the modules they need) with a menu,
  scrolling lists and detail screens for tenants, keys, grants, settings and
  the audit log, as a client of the same API through the same client. A list
  fetches a page of 100 and the next when the cursor nears the end, never
  everything; a call runs beside the screen, with the commands' deadline, and
  esc cancels it; an error is one line with the server's message and request
  id. A new key is drawn on one screen only, with the words that it will not be
  shown again, leaving that screen asks once, and the secret is zeroed then;
  revoking a key and removing a grant, a setting override or a tenant need the
  id or name typed. The alternate screen is left and the cursor shown on quit,
  on SIGTERM and SIGINT, and on a panic. With any argument, or when either end
  is not a terminal, the usage line and exit 2 are as before.
  `scripts/check/tui.py` runs the built binary under a pseudo-terminal against
  the fake server (`make check-tui`, a step of its own in CI, Linux and macOS
  only). The binaries grew by 1.67 to 2.09 MiB (linux/amd64: 10.78 to 12.73
  MiB).
- The master database could only live in a SQLite file. It can now live in
  Postgres too (`internal/driver/postgres`, on `pgx/v5` and its pool, pure
  Go), at a `postgres://` address, and it passes the same contract suite
  unchanged on Postgres 16 and 18. The driver makes and upgrades its
  tables in the schema the address's `search_path` names and works with a login
  that owns only that schema; it never makes a database or a role. Every change
  and its audit entry are one transaction; a grant's old role is read and its new
  one written in one statement under a lock on the key's row, so two racing sets
  are ordered; a unique tenant name is kept by the database and its violation is
  recognized by SQLSTATE and constraint name, never by message text. Text that is
  compared or ordered is `COLLATE "C"`, so a database created with another
  collation pages through the same order. Migrations take a session advisory lock
  on one dedicated connection, released on every path, so processes that start
  together apply each file once. No error repeats the address, the password or
  the host. CI runs the driver against Postgres 16 and 18 and fails, instead of
  skipping, when the server is missing; `scripts/check/master-postgres.sh` reads
  what the driver wrote with `psql` and `pg_dump` and finds a key's hash but
  never its secret.
- A tenant's database could only be a SQLite file. It can now be a Postgres
  database too (`internal/driver/postgres`, `OpenTenant`), and a tenant created
  with type `postgres` and a `postgres://` address is served by the registry
  like any other: ready when its database answers, unreachable alone when it
  does not, and back by itself when it does again, with no restart. The tenant
  suite passes unchanged on Postgres 16 and 18, two instances on one database
  and two tenants apart included. The settings and audit code is now one
  implementation shared by the master and the tenant, as on SQLite. The schema
  records the id of the tenant it was made for and every open compares it, so
  two tenants pointed at one database and schema, or an address pasted for the
  wrong tenant, are refused as Invalid instead of sharing one audit log. A
  tenant's pool is small (four connections, idle ones let go after a minute;
  `pool_max_conns` and `pool_max_conn_idle_time` in the address change them).
  The registry's warning about the limit on open files counts each tenant by
  what its driver says it holds (an optional `FilesPerTenant`): eleven files
  for SQLite, the pool's size for Postgres. `scripts/check/tenant-postgres.sh`
  reads a tenant the registry made with `psql` and `pg_dump`, and the Postgres
  CI jobs run the registry against real tenants.
- The API contract had generated interfaces and nothing behind them.
  `internal/api` now implements its four services (tenants, keys and grants,
  settings, audit), each method a short function that takes the access
  check's decision from its context, validates the request and answers
  `INVALID_ARGUMENT` naming the field, then acts through the package that
  owns the change, with the audit entry in the same transaction. It also
  holds the table of what each method needs of its caller, which the server
  asks before every request. A handler that finds no decision answers
  `INTERNAL` and never decides by itself; a tenant missing at a lookup after
  the check is answered `PERMISSION_DENIED`, as for no grant, to a caller who
  may not list every tenant; a key that may not list every tenant sees only
  the tenants it holds a grant on, paged without reading every tenant; a
  removal needs the name twice; a tenant's database address reaches no
  answer, audit entry, error or log line, which a test shows by searching
  all of them, and the master's files, for the address, its password, user
  and host. A tenant's state is the one the registry sees now, not the
  master's row, which can lag a minute behind, with the time that state was
  reached (`Tenant.state_time`, a new field). A read costs 16 µs (a tenant)
  to 24 µs (a tenant's setting) through the check and the handler without
  the network, its audit entry batched into the log of the scope it read; a
  change, one synced commit (5 ms on a laptop).
- Settings were any name anyone wrote, with no default and no rule for their
  values, so the contract's "not found" and "a value the setting does not
  accept" could not happen. The server now knows a fixed list
  (`internal/core/settings`), each setting with a built-in default, a rule
  for its value and whether a tenant may override it. The value that applies
  is the tenant's override, else the installation's value, else the built-in
  default, and every answer says which (`Setting.source`, a new field of the
  contract). Unsetting the installation's value returns to the built-in
  default. The list's one setting, `key.default-lifetime` (whole hours or
  days, such as `90d`, from 1h to 3650d; 90 days by default), is the
  lifetime of a key made with no expiry.
- `coria serve` served no API method: the handlers existed and nothing
  joined them to the server, and the commands and the interactive program
  had only run against an in-memory stand-in. `serve` now builds the tenant
  registry, with the SQLite and Postgres tenant drivers, and the handlers,
  and registers them on the checked path. Every tenant is opened before the
  server listens, sixteen at a time and each within ten seconds, so a tenant
  whose database does not answer is unreachable and answers unavailable while
  the others are served, and is tried again every thirty seconds. `/metrics`
  counts the tenants in each state. The server's own settings, such as the
  installation's id, are kept under a prefix no setting may use
  (`settings.ReservedPrefix`), so the API neither lists, reads nor changes
  them: to a caller they do not exist. A test runs the real program end to
  end (`make check-e2e`, in CI's server job): init, serve with the local
  socket and TLS, the real commands as processes over both, a key with no
  grant refused, a revoked key refused on its next request, a second serve
  refused, a stop by signal that writes every queued audit entry, and a
  restart that serves the same tenants, keys and log; `make check-server` now
  also points curl, grpcurl and Python's grpcio at the real program.
- Nothing showed the installed program doing, in order, what this version
  claims: each part was checked on its own. `make prove` (`scripts/prove/run.sh`,
  with `scripts/prove/steps.py`; CI's "Proof: the installed binary, from outside"
  job, on Linux, with a Postgres service container) builds every target as
  `make build` does and runs the real binary against tools that are not
  CORIA: curl, grpcurl, openssl, sqlite3, psql and pg_dump, and Python's
  grpcio. It shows, each as a named check with what it measured: `init` and
  `serve` on the local socket and on TLS; no key or grant after `init`, the
  first key made on the socket, and every method of the contract (with
  `/metrics`) refused without a key, health answering; tenants on SQLite and
  on Postgres, and a remote address without its sslmode refused and leaving
  nothing; no key, a malformed, unknown, wrong, expired (`--expires 1s`) and
  revoked key, each with every method, through the three clients, with one
  answer per client byte for byte, "no grant" and "no such tenant" identical,
  and a caller who may list tenants told not found; no secret in the master's
  dump, the tenant's `pg_dump`, any file of the data directory, the logs at
  debug level or the audit log, and each key's hash present; a copy of the
  installation given another installation's key file refused at start,
  listening on nothing and left byte for byte as it was, and with its own key
  file put back serving both tenants with their data; the Postgres container
  stopped and started under a running server, the SQLite tenant answering in
  its usual time meanwhile; the server killed with signal 9 during tenant
  creation 24 times, each kill classed by what the master held right after
  it, and the master's integrity checked each time; every request it made
  found in the audit log with its outcome, and nothing else there; a second
  server and another operating system user refused; every list command's
  `--json` and the interactive program under a pseudo-terminal; and the size
  of each binary. It does not show Windows, a loss of power on real hardware,
  or any speed.
- The API could create tenants and keys but had no way to give a tenant a place to
  keep data or a way for data to arrive. The contract now has five more services in
  `coria.v1`: `StorageService` (a
  directory under a root the installation names, or an S3 bucket at an endpoint the
  installation lists, whose secret access key goes in and never comes back),
  `ReceiverService` (a receiver, a DICOM listener on a port the installation
  allotted to the tenant that admits nobody until its callers are listed, or
  a STOW-RS service), `ReceptionService` (one study's arrival at a receiver and its steps,
  with a retry or a discard for one that failed), `QuarantineService` (why a file
  was held, released to run again or rejected with a reason) and `MonitorService`
  (a tenant's numbers and a stream of its events, read from one snapshot so a
  client misses nothing). By default a label is a keyed hash, so no label or event
  carries an identifier the sender gave; a site's own naming rule makes labels
  exactly as identifying as the rule. A change to a storage or a receiver names its
  fields in a field mask; a state that forbids a change is answered
  FAILED_PRECONDITION. Secrets cross the API in three fields. A walk-through in
  `docs/api-walkthrough-reception.md` follows three uses call by call with only the
  contract in hand.
- The server had no rules for where a received file goes or what state its
  reception is in. `internal/core` now holds them, with no I/O, so each is
  tested without a database, a network or a storage: labels (1 to 128 of
  `A-Z a-z 0-9 . _ -`, never a hidden name, a trailing dot or a Windows device
  name, so every label is a safe directory name on Linux, macOS and Windows);
  the naming rule, a template over DICOM attributes or a pattern over one
  attribute's value, checked when it is set, never cleaning a value up
  silently and never putting one in an error's text, where `{#PatientID}` and
  the like give the keyed hash of a value (HMAC-SHA-256 under the tenant's
  key, 16 characters of base32) instead of the value; a default rule,
  `sub-{#PatientID}`, `ses-{#StudyInstanceUID}`, `scan-{#SeriesInstanceUID}`,
  so that by default no label carries an identifier the sender gave, and no
  well-formed study is held for an empty date or number; the UID check of
  PS3.5 9.1; the one place that builds and parses storage keys
  (`data/<subject>/<session>/<scan>/<SOP Instance UID>.dcm`, and
  `manifest.json` and `metadata.json` under `meta/`); the scan manifest,
  versioned, written in one byte form only and read with bounds; and the
  reception state machine as one table, with completion decided from the
  quiet period and the longest open time, a clock set back included. The
  template, pattern, key and manifest parsers are fuzzed.
- CORIA had nowhere to put a file. The storage port, `internal/blob`, now
  states what every storage promises: Put streams bytes to a key and returns
  their size and SHA-256 computed as they pass, and the object appears whole or
  not at all, a replaced one included; Read gives a byte range as a stream of
  the object as it was when the read began; List pages through keys in a stable
  order, one level at a time with a delimiter; Delete of a missing key is not an
  error; Usage says when it cannot know the free space. Keys are the archive's
  own, ASCII letters, digits, ".", "_" and "-", checked in one place, so a key
  names the same object on every storage and file system and never a path
  outside it. One contract suite holds every driver to this, and is itself
  checked against drivers broken on purpose. The first driver, a directory on a
  local disk or NFS, writes each object to a temporary directory at the
  storage's root, syncs it, renames it into place and syncs both directories,
  and returns only when every directory on the key's path is durable. It
  removes nothing but the object a Delete names. It refuses a storage that
  spans two file systems, or whose file system cannot sync a directory, rather
  than copy or pretend. It follows no symbolic link, never blocks on a file
  swapped in under it, names no path in its errors, and pages through a
  directory of 200,000 files in under a second. At start it reads only its
  temporary directory. A child process killed in the middle of its Puts leaves
  every key whole or absent, and the files written match `shasum` and `cmp`.
- A tenant could keep its files only in a directory. The S3 driver,
  `internal/blob/s3`, is a storage on Amazon S3 or an S3-compatible server,
  under an optional prefix of a bucket, and passes the storage contract suite
  unchanged against two different servers. An object of up to 8 MiB is one
  request; a larger one is a multipart upload, aborted on any failure, in
  memory bounded for the whole store, not only for one Put. A Put succeeds
  only on what the server says about the bytes it was sent: the echo of the
  checksum of each upload and part, the composite checksum of a completed
  upload or else a HEAD of the object; a server that says nothing is refused
  when the storage is opened. Read returns exactly the bytes asked or an
  error. Every answer that is not an object's bytes is cut at 4 MiB. Uploads a
  crash left unfinished are aborted when the storage opens, which never makes
  the opening fail. The driver uses only the credentials it is given, and
  neither they nor anything holding them can be printed; it asks a function of
  the caller's whether each address may be connected to, and the one provided
  passes global unicast addresses only; it follows no redirect; and its errors
  carry neither the server's words nor the endpoint. A storage's prefix is at
  most 128 bytes and the storage port's longest key is now 896, so every key
  fits S3's 1024 bytes under every prefix. It adds the AWS SDK for Go's S3
  client, about 5.6 MB of the binary when the driver is used.
- A tenant's database held settings and an audit log, and nothing the reception
  of a study needs. It now keeps, on SQLite, everything that must not be lost:
  storages and receivers, with partial changes, S3 endpoints kept in one
  spelling, and the contract's rules on names, overlap, ports and what is in
  use, an S3 secret stored sealed and never read back; receptions, opened or
  found per receiver and study, numbered by arrival, stepped by compare-and-set
  through the core's state table, retried or discarded when failed; received
  files, in batches that change nothing when written twice; a catalog of
  subjects, sessions, scans and instances, in which an instance is claimed from
  a reception's received files before it is written to a storage and confirmed
  with its scan's rows, so that no instance is archived twice, none is ever
  replaced by a reception, and what is held does not depend on what later
  happened to the reception that archived it; quarantine, with release, reject
  and running totals, where a different file under an archived UID can only be
  rejected; and leased units of work, every write fenced by the attempt number,
  whose reception fails with them when they give up, so that no claim outlives
  its workers. A reception's received count is of every file that arrived, so
  that received always equals archived plus duplicates plus quarantined when it
  ends. The store reads no clock. Every list, claim and lookup is answered from
  an index, also every combination of list filters at millions of rows. These
  are a second interface, `tenant.Pipeline`.
- Nothing told a system outside what was happening: a reception moved through
  its steps and the only way to know was to ask again. A tenant's events are now
  recorded with the change they report, in the same transaction: each step a
  reception reaches, with its counts and arrival number, each file quarantined
  and each decision on one, and what the server reports (failed associations,
  receivers up or down, storages nearly full). An event carries the archive's
  labels, ids and counts and never a UID or a value from a file. The feed is
  read in commit order from a cursor that resumes exactly; a cursor older than
  what is kept is refused, not skipped; old events are removed in batches; and
  a status is read in one snapshot with the cursor of its moment, so that what
  comes after the cursor is not in its counts. `internal/events` follows
  tenants' feeds live without holding events for any client: a watch is a
  cursor and a one-slot wake, so a client that stops reading costs no memory
  and loses nothing until its cursor expires.
- A study could not be received into a tenant on Postgres: only SQLite kept
  what a reception needs. The Postgres driver now has the same operations
  (storages and receivers, receptions and their received files, the catalog
  with claimed and held instances, quarantine, leased work) and passes the
  same contract suite unchanged, on Postgres 16 and 18. With several writers
  each operation keeps its rules at the default isolation: rows are locked in
  one stated order and never upgraded, so claims of shared instances taken in
  opposite orders do not deadlock; two opens of one study make one reception;
  commits made at once share one subject and one session; two workers never
  take one unit of work, and neither waits for the other; a storage cannot be
  removed under a commit. A deadlock or serialization failure that still
  occurs is retried inside the driver and never reaches a caller as an
  unknown error. Every list, claim and lookup is answered from an index at
  millions of rows, checked from the query plan in CI.
- Four services of the contract answered "not implemented": a storage, a
  receiver, a reception and a quarantined file could be described but not
  made or read. `StorageService`, `ReceiverService`, `ReceptionService` and
  `QuarantineService` are now served, each method behind the same key and
  grant check as the rest. What a tenant's administrator may point at is
  bounded by the installation: a directory storage lives under a root the
  installation lists, at `<root>/<tenant>/<path>`; an S3 storage's endpoint
  must be on the installation's list; a receiver's port must be one allotted
  to its tenant, and no two receivers of the installation share one. A
  storage's secret is sealed when it is written and is never returned.
  `TestStorage` writes a small object, reads it back and removes it, and
  says which of those failed. A storage that holds files cannot be removed.
  The settings behind these
  (`storage.reserve`, `receiver.ports`, the directory roots, the S3
  endpoints, the bind addresses, `events.retention`) are parsed by
  functions that are fuzzed in CI. The command line maps three more answers
  to exit codes: 5 a state that forbids the change, 6 a position that can
  no longer be answered, 7 a bound of the server reached.
- The command line had no command for the archive's four services. It now has
  `storage` (add, list, show, set, remove, test), `receiver` (add, list, show,
  set, remove), `reception` (list, show, retry, discard) and `quarantine` (list,
  show, release, reject), each taking the tenant first, with a table or --json,
  the exit codes of the other commands and shell completion. An S3 storage's
  secret access key is read from the variable named by --credential-env or the
  private file named by --credential-file and never printed. Discarding a
  failed reception and rejecting a quarantined file need a --reason, and ask for
  the id to be typed again unless --yes is given.
- Nothing turned received files into an archive. Package `receive` now does:
  a file lands in a local incoming area with its SHA-256, its header is read
  (never its pixel data), it is named subject, session and scan by the
  tenant's rules, and a reception ends after its quiet period. Each scan is
  then archived in order: its instances are claimed, each file is written to
  the storage and read back and compared before its catalog row is
  committed, then the scan's sidecar and manifest. A file that reads back
  different is held in quarantine with its incoming copy kept, and is never
  counted archived. Before the incoming copies are removed the reception is
  checked whole: every instance at its key with its size, every manifest
  agreeing with the catalog, no claim left. Every step is leased work
  recorded in the tenant's database, so a server killed before or after any durable effect of any step
  (tested with signal 9) resumes without losing a file or archiving one
  twice. A header is bounded at 64 MiB before pixel data, so a file that
  declares a huge value is refused without the memory being allocated.
- The stream of every tenant's events could stall or mislead in ways its
  first form did not say. A tenant whose database does not answer a read
  within 5 seconds is now skipped, named in each keep-alive's
  `unanswered_tenants`, and tried again after a wait that doubles from one
  second to a minute, so one slow tenant does not hold the others. A tenant
  that leaves the list of tenants is paused with its cursor kept; one removed
  and created again under the same name is followed from its first event. A
  stream resumed with `resume` set starts an unlisted tenant from its first
  event kept instead of from now, so no event is lost between two streams.
  A cursor after the newest event a tenant holds, as after a restore from an
  older backup, is answered out of range instead of waiting forever. An
  event carries the step its reception left (`previous_step`), so a client
  can keep the counts of receptions by step from a status and the events
  after it. One stream follows at most 10000 tenants.
- A quarantined file's release can carry a reason, kept on its record
  (`ReleaseQuarantinedFileRequest.reason`, `QuarantinedFile.release_reason`),
  and a release's audit entry names the reception the file joined.
- Two quarantine reasons: `QUARANTINE_REASON_HEADER_TOO_LARGE`, whose bytes
  are kept so a person can release or reject the file, and
  `QUARANTINE_REASON_INCOMING_LOST`, for a recorded file whose incoming copy
  is gone.
- The tenant's store can list a scan's instances, tell a caller which
  receptions failed when it gave up their work, and refuse a write of
  received or held files from a worker that lost its lease.
- `coria quarantine release --reason TEXT` keeps why a held file was
  released with its record, as a rejection keeps its reason; `quarantine show`
  prints it as "Release reason". The reason is optional, 1 to 1024 bytes, not
  only spaces.
- Nothing listened for DICOM: a receiver could be made through the API, but a
  scanner could not send to it. Each enabled receiver now has a listener on
  its port and address, started, restarted and stopped as its row changes,
  with no restart of the server, and started from the database after one. It
  admits only the callers it lists (calling AE title, address range, or both;
  an empty list admits nobody), rejecting a stranger as
  calling-AE-title-not-recognized before anything is negotiated and a wrong
  called title as called-AE-title-not-recognized. It negotiates only the
  receiver's SOP classes and transfer syntaxes, and answers C-ECHO and
  C-STORE and nothing else. A store is written as the bytes sent into the
  incoming area in bounded memory (a 512 MiB file with a live heap under
  11 MiB) and answered success only once it is synced there. A file past the
  size bound, a full disk or storage, or a tenant over its incoming limit is
  answered A700 and new associations are rejected as transient, with one
  event when it starts and one when it ends. A receiver that cannot start
  (port taken, TLS files unusable) shows down with its reason and is retried
  while the others run. Stopping the server finishes the stores in flight
  within 30 seconds.
- A receiver in `coria serve` archives what it receives: the reception
  pipeline opens a tenant's storage by the same rules the API applies when
  the storage is added, checked again at every use, and names labels with the
  tenant's own keyed hash. Each tenant has a naming key of 32 random bytes,
  sealed with the installation key and bound to the tenant, kept in the
  tenant's database; a tenant made before this version is given one when it
  is next opened. No API answers it and no log holds it.
- The setting `receiver.header-limit` (installation-only, default `64MiB`,
  from `1MiB` to `4GiB`) bounds how much of a received file is read as its
  header. A file whose header is longer is quarantined as `header_too_large`
  with its bytes kept, so it can be released once the bound is raised; a
  change applies within a second, without a restart.
- `make prove-reception`: the reception proven from outside the binary with
  dcmtk, pynetdicom, pydicom, hashlib, openssl and sqlite3, each claim one
  named check: a study archived byte for byte with every manifest checksum
  right; a study in two associations made one session; a study sent again
  counted duplicate; a corrected file under the same UID and a file cut short
  quarantined with their bytes; strangers refused; signal 9 while receiving,
  in the quiet period and while archiving losing nothing and doubling
  nothing; a nearly full storage refusing as transient and serving again;
  the status and the event stream agreeing with the catalog; and streams
  that stop reading costing no memory; with `PROVE_S3`, a tenant on an S3
  bucket, each object read back by a GET the proof signs itself, through
  kills as well; with `PROVE_POSTGRES` and `PROVE_PSQL`, a tenant on
  Postgres whose catalog psql reads, through kills as well. It replaces `make check-reception`.
- `docs/testing.md` lists how tests and checks are run, what `-short` means,
  how a slow test is marked, and every `CORIA_*` variable; the reception
  walk-through says which changes take effect without a restart.
- A tenant on Postgres has the event feed a SQLite tenant has: every step of
  a reception, each file quarantined and each decision on one, receivers'
  health, storages' space and turned-away callers are events written with
  the change they report, read in commit order from a cursor, with the
  status snapshot, retention by checkpoints and the live stream. Several
  transactions write at once on Postgres, so a change that writes events
  takes the feed's lock just before its first and holds it to its commit:
  event ids follow commit order and a reader never passes an event that
  commits behind it. Changes that write no event, such as a batch of
  received files, are not slowed.
- `MonitorService` is served, the last service of the contract that was
  not. `GetStatus` answers a tenant's numbers now, or a page of every
  tenant's: its database's state, its counts and its feed's cursor read in
  one snapshot, each storage's used bytes (a running total kept with each
  commit) and free bytes, each receiver's health, the throughput of the
  last ten seconds, and its incoming limit. A tenant whose database does
  not answer is answered with what the server holds in memory rather than
  refused. `Watch` streams one tenant's events, or every tenant's, as they
  are committed, from a cursor or from now, with the caller's key and grant
  checked again every 15 seconds and an audit entry when the stream opens
  and when it ends, with the events it carried.
- The interactive program has a tenant's archive: from a tenant's screen,
  its status (`t`), which asks for itself again each time the tenant
  commits an event, through the live stream it follows while it is open;
  its storages (`o`), receivers with their health (`r`), receptions (`e`)
  and quarantined files (`h`). A quarantined file is released or rejected
  from its screen, each with a reason; the rejection, which removes its
  bytes for good, needs the file's id typed again.
- A receiver added without a port (`receiver add` without `--port`, or a
  port of 0 in `AddReceiver`) is given one by the server: the lowest of the
  tenant's allotted ports that no receiver of the tenant holds and that the
  server can bind now, IPv4 included. None such is refused, naming the
  setting `receiver.ports`.
- `make bench-gate`, and a CI job of its own: reception's speed (files a
  second from landing to verified, small slices on SQLite and a directory)
  measured on the base branch's tree and on the change's, in turns on the
  same machine, failing when the change is slower by more than 25%.
- The reception's speed is measured on Postgres and on an S3 bucket as well
  as on SQLite and a directory (`BenchmarkLandToVerified`).

### Changed

- go-dicom is taken at its release v1.11.0 instead of a commit of its
  development branch. The release holds everything the receiver uses (a stop
  that lets the stores in progress finish, admission before negotiation, a
  C-STORE as a stream, a logger of its own, a bound on any one value read).
  Nothing the receiver answers changes; a DICOM sender now sees the
  implementation version name `GO-DICOM-1.11.0` in the association's reply.
  The Linux binary grows by 135 KB.
- The project is named CORIA, Core Research Imaging Archive. The module is
  `github.com/amrshadid/coria`; the command is `coria`; the API's package is
  `coria.v1` (every method's path starts `/coria.v1.`); the environment
  variables start `CORIA_`; a key starts `cor_`; a key file's lines start
  `coria-key-v1`; the request id header is `Coria-Request-Id`; the metrics
  start `coria_`; the local socket is `run/coria.sock`. Nothing was released
  under the old name, so nothing reads it: an installation made before this
  change is made again. `api/renamed.txt` lets the API's breaking-change
  check read a commit before the rename under the new name.
- A scan's files are put to the storage, and read back, eight at a time
  instead of one after another, every put still ended before any read-back.
  A reception of 200 slices of 256 KiB went from 21.5 to 30.6 files a second
  on SQLite and a directory, 36 to 44 on Postgres, and 31.5 to 38 on S3.
- A keep-alive of the stream of every tenant names, after the first, only
  the tenants whose position changed since the keep-alive before
  (`WatchKeepAlive.full` marks the first, which names them all), so a
  mostly idle stream at a thousand tenants sends a few bytes every 15
  seconds instead of the whole map of positions.

- A recorded file whose copy in the incoming area is gone is quarantined as
  `incoming_lost` and the rest of its study is archived; before, the whole
  reception failed. A scan's manifest is written from the catalog's list of
  the scan, and verification checks that it lists every instance the catalog
  holds in the scan, whichever reception archived it.
- A receiver added, changed or removed through the API is started, restarted
  or stopped at once; the supervisor's own reading of the receivers is a
  backstop every 30 seconds. A running receiver whose TLS certificate expires
  is stopped and shown down within 5 seconds.
- A receiver with no bind address is refused, address in use, when another
  program holds its port on the IPv4 wildcard address, on macOS as elsewhere.

- `coria receiver add` and `set` refuse `--allow-aet-from` with no title or
  no range, naming the missing part; an AE title that contains `@` is read
  whole, since the rule splits at the last `@`.
- The comments of the storage and settings contract state the 1023-byte limit
  of a directory's path, the segments safe on Windows, the refusal of roots
  that overlap once symbolic links are followed, that credentials change with
  the endpoint, `path_style` at private-address endpoints, and what a caller
  who is not an admin reads of `storage.directory-roots`. No field, number or
  name changed.

- Where a tenant's data may be (directory roots and their resolution, S3
  endpoints, port allotment, opening a storage) is one package,
  `internal/place`, that the handlers and the reception pipeline both use.
  The rule of a name, Windows device names and the nesting of paths are
  written once, in `internal/core/pathname`.
- An S3 storage's prefix is at most 128 bytes (it was 512), so with the
  longest key the server writes every object's key fits the 1024 bytes S3
  allows.

- Running what CI runs meant reading the workflow and assembling it by hand:
  the Python packages, Postgres, two S3 servers, the proof. `make ci` now runs
  every job of the workflow in one go, cheapest first, ending with the proof.
  It removes every container it started however it ends, and lists what it
  could not run on the machine it ran on (on a Mac, the 32-bit tests, which it
  builds and vets instead). `make tools-python`, `pg-up`/`pg-down`,
  `s3-up`/`s3-down`, `test-postgres`, `test-s3`, `outside` and `check-static`
  are its parts, and CI's Postgres, S3 and build jobs call them. Go and the
  Python tools are set up by one composite action each instead of being
  repeated in nine jobs. The contract's Python toolchain
  (`scripts/requirements/proto.txt`) is now pinned with hashes and installed
  with `--require-hashes` like the others.
- The API's handlers imported the database drivers for the names of the two
  database types, which linked both database libraries into them. The names are
  now `master.SQLiteDriver` and `master.PostgresDriver`, beside the tenant's
  field they are values of, and a lint rule keeps the handlers from importing a
  driver. Another lint rule lets only `internal/driver` import the SQLite and
  Postgres drivers, which their documentation had claimed and nothing held. The
  redactor moved from `internal/server/redact` to `internal/obs/redact`, beside
  the logger that uses it.
- Each change through the SQLite drivers, the master's and a tenant's, was a
  transaction of its own with a sync of its own, so a SQLite-backed server
  made at most a few hundred changes a second however many callers waited,
  and they queued one behind another (about 185 a second with 1, 8, 64 or
  256 callers, each change a setting with its audit entry). Changes waiting at
  the same moment are now written in one transaction and one sync, each in a
  savepoint of its own, up to 256 at a time, with no timer: about 700 a second
  with 8 callers, 4,300 with 64 and 14,500 with 256 on the same disk, while a
  caller alone waits as long as before (about 5.4 ms). Each caller still gets
  its own result, and every answer, success or failure, describes the
  database as committed: a change that fails (a taken name, a taken audit
  id) or panics fails alone and undoes nothing of the others; a caller is
  answered only after the commit that holds its change, and so is a caller
  refused because of a change before it in the same transaction, which runs
  again if that transaction is undone instead; a caller whose context ends
  while its change is queued is answered at once and the change is never
  written, and one whose change has started learns whether it was written,
  which is whole or not at all. A new test kills a
  process making changes with SIGKILL and has the sqlite3 tool check the file:
  integrity, every answered change there with its audit entry, none half
  there. The ports do not change.
- About 130 comment lines, a test file's name, a test failure's message and the
  comments of the shipped SQL migrations pointed at notes that are not in the
  repository, so a reader could not tell why the code was the way it was.
  Each now says its reason itself, or cites a published rule or the API
  contract. `make check-comments` (part of `make check`, and run in CI's API
  contract job) fails on such a reference, naming the file and line. The
  migrations' statements are unchanged but their checksums are not: a
  development database made before this change must be made again.

### Fixed

- A quarantined file released through the API or the command line was
  released by the tenant's store alone: the reception pipeline never heard
  of it. A released file that joined no reception (a file held for its
  header's size, released after `receiver.header-limit` was raised, say)
  was never checked again, and the next start of the server deleted it as
  kept by nothing; a file that joined a reception, and a retried reception,
  waited for the next start before going on. A release and a retry now run
  through the tenant's running pipeline, which gives the file's bytes their
  landed name before the release is recorded and queues the next step at
  once; with no pipeline running they are refused as unavailable, recording
  nothing. A rejected or discarded file's bytes are removed at once too.
- A server stopped during a release, after it was recorded and before the
  released file was checked again, gave the file at its next start a record
  whose id was the released record's; the store skipped it as already held,
  and the start after deleted the file. At start such a file is now checked
  again as a release of its record, under a record of its own; and a hold
  the store skips because its id is taken leaves the file's bytes alone.
- A release in flight while the tenant's reception pipeline was restarted
  in the same process (its database reopened) could leave the released
  file under a name nothing kept, removed at the start after. A release, a
  retry, a rejection and a discard are now admitted as a received file is:
  refused once the pipeline stops, and waited for by its stop.
- A receiver added without a port had its bind address probed before the
  request was checked, so a host name was resolved and bound for a moment;
  the request is now checked first.
- The event feed's keeper logged that a checkpoint or a pruning failed, but
  not why.
- A server killed while it archived a study, and started again, left the
  study's work claimed by the killed process for two minutes, the length of
  a lease, before archiving it. It now takes that work back at its start.
- The end-to-end tests of reception on Postgres and on S3 never ran in CI:
  they read variables nothing set, and `make test-s3` ran only the S3
  driver's tests. `make test-postgres` and `make test-s3` now run them, each
  test on a scratch schema or prefix of its own.
- A tenant's events were never pruned: the setting `events.retention` was
  read by nothing. The server now records, at its start and every hour, a
  checkpoint of each tenant's feed with its own clock (at this time the
  newest event was this one), and prunes every event up to the newest
  checkpoint older than the tenant's retention. An event's own time plays
  no part, so an event dated far ahead or a run of wrong dates no longer
  holds back or stalls the pruning; an idle feed adds no checkpoint.

- A receiver's associations are now negotiated from its own SOP classes and
  transfer syntaxes from the first one: they are set before the port opens,
  where before they were set just after it, and a caller in between was
  turned away. Stopping a receiver lets a store in progress finish through
  the DICOM library's own graceful stop; a new caller meanwhile is turned
  away and keeps its file. An abort is reported as a protocol error only
  when the caller broke the protocol or went quiet; a failed TLS handshake
  is reported as `tls_failed`. The DICOM library logs nowhere for the
  receivers and the header reader, by a logger given to each, where before
  its global logger was silenced for the whole process. A value after a
  file's pixel data is held to `receiver.header-limit`, as one before it is.

- Two directory roots whose paths differ in text could be one directory on
  the disk: a root whose path was a symbolic link into another root's tenant
  directory let two tenants share a storage. Every link in a root's path is
  now followed when `storage.directory-roots` is set and whenever a root is
  used, and two roots that are one directory, or one inside the other, once
  followed are refused.
- On Windows `/` also separates a path's segments and a segment's final dots
  and spaces are dropped, so roots such as `D:\srv` and `D:\srv/x` were
  accepted as apart. A root's path is now read that way, and a directory
  storage's path may not have a segment ending in a dot or one that is a
  Windows device name, on any system.
- A directory storage could be added under a path too long for the server to
  open, and then failed every write. Its directory, root included, is now
  bounded at 1023 bytes when it is added.
- A tenant's reader could read the installation's absolute root paths through
  `GetSetting storage.directory-roots`. A caller who is not an admin on the
  installation now reads the roots' names only.
- An S3 storage at an endpoint listed with `private-address` could address its
  bucket in the host name, so a bucket name a tenant chose decided which host
  in the server's network was reached. Such a storage must use path style.
- `SetStorage` could move an S3 storage to another endpoint and keep its
  credentials, signing requests to the new host with them. A mask that names
  `s3.endpoint` must now name both credentials too.
- `TestStorage` on an S3 storage answered an internal error: the tenant's
  store could not hand back the sealed secret. It can now, on both databases.
- `TestStorage` told a storage's faults apart by the drivers' message text;
  the storage drivers now mark each cause with an exported error value.
- Discarding a failed reception could list, for removal, a place in the
  incoming area that a released file had given to a reception still running
  or one that failed and may be retried. Such a place is no longer listed.
- On Postgres, two opens of a reception at once with the same id answered a
  conflict, a unit of work queued under a held key was refused when its
  reception did not exist, and a refused batch of files named a different
  reception than on SQLite. Both databases now give the one answer the
  store's contract states, and a change retried after a deadlock is tested.
- A precondition from the tenant's store is answered FAILED_PRECONDITION and
  an expired event cursor OUT_OF_RANGE by their own kind, not by a mapping
  in the handlers.

- A request its caller canceled could be recorded under another reason than the
  cancel: when the cancel arrived while the server waited for the request's
  message, the entry said the message could not be read; and a cancel that
  arrived just after the check still ran the handler, whose success was then
  recorded as done. A request whose caller has gone is now recorded as
  canceled at whatever moment the cancel lands, and nothing further is done
  for it, a change included.
- A key revoked or expired while a request's body was still arriving was
  still honored by methods whose requirement reads the request (GetTenant,
  the settings methods, ListAuditEntries), for as long as the body took, up to
  its 10-second bound: the key judged when the headers arrived was not judged
  again. Such a method's check now reads the key again once the message has
  arrived, and every check judges a key's expiry at the moment it decides.
  Every request that the check allows now writes exactly one audit entry
  however it ends (an unreadable message, a handler's failure or panic, a
  deadline, a cancel, the server stopping); a request canceled by its caller
  during the check is recorded as having ended, not as a failure of the
  master database; a refused HTTP/1.1 connection is closed as soon as the
  refusal is written instead of after the body's read bound; and while the
  server stops, only the health check's exact path is still answered.
- A caller with no key, or a bad one, could tell from the server's answer which
  methods it serves, and some of its requests were held or went unaudited. Who
  the caller was got decided after the router and the RPC library had looked at
  the path, the HTTP method and the content type, so the answer differed by what
  the path named: over HTTP/1.1 a gRPC, gRPC-Web or Connect streaming call to a
  name not served got HTTP 505 and a served one the refusal; a streaming content
  type got 415 with a list of accepted types from a served method and a refusal
  from one not served; a GET or a POST without an RPC content type got 405 with
  an `Allow` header, 404 or 401 by path; a percent-encoded path reached the real
  handler; a method that reads its request held a request whose body never ended
  for 10 seconds; and the RPC library's own answers wrote no audit entry. The
  server now authenticates every request before it routes it: a caller who is
  not authenticated gets one refusal, chosen only by the protocol its
  `Content-Type` names, written at once without reading the body, the same for
  every path and every HTTP method, with the request id where a client reads
  initial metadata. Only an authenticated caller reaches the router, which
  serves exact paths only; anything else, a percent-encoded path included, is
  answered UNIMPLEMENTED (NOT_FOUND to plain HTTP) in the caller's protocol over
  any HTTP version. Every request but the health check writes exactly one audit
  entry, under the method asked for when it has the shape of a protobuf method
  name within 256 bytes and under `/` otherwise, including a call the RPC
  library refuses before the check. The key is still read once per request.
- A key revoked, expired, or stripped of its grant after its request was
  checked could still make that request's change: the management calls read
  the key again just before the change, in a step of their own, and the
  handlers of tenants and settings did not read it again at all. Every
  change of the master now takes who makes it and checks the key inside its
  own transaction, in each database driver, so a key that may no longer act
  writes nothing, neither the change nor its audit entry. The answer is the
  one the access check gives such a key, `UNAUTHENTICATED` or
  `PERMISSION_DENIED` with the same message, and the request is recorded as
  refused in the installation's log with the real cause ("key revoked since
  the check"). A change in a tenant's own database, which holds no keys,
  asks the master for the same check from inside the tenant's transaction,
  just before its commit; a revocation that commits in that one commit's
  time is not seen by that change. The key's expiry is judged at the time
  the server's clock gives inside the transaction, after its locks are held,
  so a change that waited for a lock past the expiry is refused. A handler
  that changes something checks the key first, before it validates the
  request or reads what it names, so such a key is never told whether a key
  or a tenant exists, a name is taken, or an argument is wrong. Reads check
  the key once before they read. On Postgres the check locks the key's row,
  and a tenant's row with the lock the change itself needs, and reads the key
  and its grant after, in a statement of its own, so a grant removed while
  the change waited for its lock is seen; a change of keys or grants by a key
  locks both keys' rows in the order of their ids, so two such changes are
  ordered rather than deadlocked. Creating a tenant no longer reads the audit
  log to learn whether its request was recorded: the registry says so.

- `serve` started with a key file other than the installation's ran: every
  tenant then failed with "the database address did not open with this key
  file", SQLite tenants included, and a tenant created in that state would
  have had its address sealed with the wrong key, leaving an installation
  that no one key file could open. `init` now writes a key check into the
  master, beside the installation's id: the id sealed with the key file.
  `serve` opens it after the identity check, before it migrates, opens any
  tenant or listens, and refuses to start (exit 1) when it does not open,
  saying the key file is not the installation's and nothing was changed. A
  master with no key check is refused with its own line: a data directory
  made before this change must be made again with `init`. The check opens
  with any key file that still holds the version that sealed it, so a newer
  key version added for rotation does not refuse a start. `init` now also
  makes the lock file, and `serve` empties it when it lets go of it, so a
  refused start leaves the data directory byte-identical.

- The command line and the interactive program had been written against the
  in-memory stand-in, which disagreed with the real server. `setting unset`
  required `--tenant`, but the installation's value can now be removed;
  `setting get`, `set`, `unset` and `list` said "installation (default)" for
  a value that was the built-in default, and now say where each value comes
  from; `tenant show` shows since when a tenant is in its state; the
  examples named settings the server does not know. The stand-in now takes
  the server's own list of settings and resolves them as the server does,
  lists a key only the tenants it holds a grant on, answers an unknown key's
  grants with an empty list and a malformed tenant name as invalid.

- A failed creation of a tenant was audited as done: the request's entry
  was written with the tenant's row, before its database was opened, and
  the row's removal then had a system entry of its own with no request id,
  so the request's id led to an entry that said done for a request answered
  unavailable or invalid. The row is now written with a system entry saying
  the creation started, which carries the request's id; the request's own
  entry is written once, with the change that settles the creation: with the
  tenant's state becoming ready (done), or with the removal of its row
  (failed, with the reason). After a crash between the two, the next start
  records that it finished the creation that request began.
- The Linux job on Go 1.27 took up to 12 of its 15 minutes: the Postgres
  jobs shared its build cache key, finished first, and saved a cache that
  held only the database driver, so the Linux job recompiled everything in
  each step. They now have a key of their own. The 1,000-tenant test runs
  once, without the race detector, as a step of its own (`make test-scale`),
  instead of inside the race and 32-bit runs.
- `coria init` and `coria serve`. Nothing could make an installation or
  serve the API. `init` makes the data directory private to its user, the key
  file, the migrated master database (SQLite unless `--master-env` names an
  environment variable holding another address), `tenants/` and `run/`; it
  refuses a directory that already holds an installation and says that losing
  the key file loses every secret. `serve` takes an operating system lock on
  the data directory (a second server is refused, naming the first; a server
  killed with signal 9 leaves nothing stale), listens on the local socket
  `run/coria.sock` (HTTP/2 without TLS; the caller must be the server's own
  user, read from the kernel on Linux and macOS; on Windows the directory's
  access list is the only guard), and on the network only with `--listen`
  (TLS 1.2 or later; without a certificate, a loopback address only). Every
  request, RPC or plain HTTP, goes through one checked path: request id in
  `Coria-Request-Id`, credential, the access check with the method's
  requirement from a table tested against the contract's `Role:` lines, then
  the handler, with a panic answered as internal. Messages over 4 MiB are
  refused both ways, a call without a deadline gets 30 seconds and no call
  runs past 5 minutes, a body read before the check must arrive within 10
  seconds, and headers, streams and idle connections are bounded. Logs are
  `log/slog` JSON with the two secret fields redacted by reading
  `debug_redact`, and every answer, including those the RPC library gives
  before the check, is logged and counted; `/metrics` is Prometheus text; the
  standard gRPC health service answers Check without a key (Watch is not
  served). `init` gives the installation an id, kept in the data directory
  and in the master, and `serve` refuses a master with another id or none,
  before migrating it. A stop signal drains requests in flight, writes the
  queued audit entries, closes everything within 29 seconds even when a
  handler ignores its context, and exits; a second signal exits at once.
  `make check-server` checks it with curl,
  grpcurl and Python's grpcio.

[Unreleased]: https://github.com/amrshadid/coria/commits/develop
