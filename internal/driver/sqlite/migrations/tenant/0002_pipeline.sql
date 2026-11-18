-- What the reception pipeline keeps in a tenant's database: the tenant's
-- storages and receivers, its receptions and the files they received, its
-- catalog (subjects, sessions, scans and the instances held or claimed), its
-- quarantine with running totals, and the leased units of work that carry a
-- reception through its steps. Everything the pipeline must not lose in a
-- restart is a row here.
--
-- The rules of 0001_init.sql hold: every table is STRICT; times are INTEGER
-- microseconds since 1970 in UTC; text a caller did not give is '' rather
-- than NULL; a checksum is a 32-byte BLOB. There are no foreign keys: a
-- reception and a quarantine record keep the name of a receiver removed, by
-- the API contract's rule, and every other reference is checked by the
-- change that makes it, inside its transaction. Counts, sizes and checksum
-- lengths are held by CHECK constraints. The state of a reception and the
-- reason of a quarantine record are the core's and the contract's words and
-- have no CHECK: a later version adds to both, and SQLite cannot change a
-- table's CHECK without rebuilding the table.
--
-- Each list, claim and lookup has the index named for it below; the plan
-- tests (pipeline_plan_test.go) show SQLite's planner uses it.

-- A storage: a directory under a root the installation names, or a bucket
-- of an S3 store. The columns of the other type are ''/0. The secret access
-- key is sealed by the caller, bound to the storage, and never read back by
-- a read operation: s3_secret is selected only as whether it is set.
CREATE TABLE storages (
    name             TEXT    NOT NULL PRIMARY KEY,
    type             TEXT    NOT NULL CHECK (type IN ('directory', 's3')),
    dir_root         TEXT    NOT NULL,
    dir_path         TEXT    NOT NULL,
    s3_endpoint      TEXT    NOT NULL,
    s3_region        TEXT    NOT NULL,
    s3_bucket        TEXT    NOT NULL,
    s3_prefix        TEXT    NOT NULL,
    s3_path_style    INTEGER NOT NULL CHECK (s3_path_style IN (0, 1)),
    s3_access_key_id TEXT    NOT NULL,
    s3_secret        BLOB,
    capacity_bytes   INTEGER NOT NULL CHECK (capacity_bytes >= 0),
    created          INTEGER NOT NULL,
    updated          INTEGER NOT NULL
) STRICT;

-- The overlap checks of a change read the storages of one root, or of one
-- endpoint and bucket, and no other.
CREATE INDEX storages_by_root   ON storages (dir_root) WHERE type = 'directory';
CREATE INDEX storages_by_bucket ON storages (s3_endpoint, s3_bucket) WHERE type = 's3';

-- A receiver. Its protocol's configuration (callers, TLS, SOP classes,
-- transfer syntaxes) and its naming rule are JSON, stored and returned whole
-- and never searched inside; the port and bind address are columns as well,
-- since a change checks them against the other receivers.
CREATE TABLE receivers (
    name           TEXT    NOT NULL PRIMARY KEY,
    type           TEXT    NOT NULL CHECK (type IN ('dimse')),
    enabled        INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    storage        TEXT    NOT NULL,
    port           INTEGER NOT NULL,
    bind_address   TEXT    NOT NULL,
    config         TEXT    NOT NULL,
    naming         TEXT    NOT NULL,
    quiet_us       INTEGER NOT NULL,
    max_open_us    INTEGER NOT NULL,
    max_file_bytes INTEGER NOT NULL,
    created        INTEGER NOT NULL,
    updated        INTEGER NOT NULL
) STRICT;

-- Removing a storage asks whether a receiver names it; adding or changing a
-- receiver asks which receivers listen on its port.
CREATE INDEX receivers_by_storage ON receivers (storage);
CREATE INDEX receivers_by_port    ON receivers (port);

-- A reception: the files of one study arriving at one receiver. state is the
-- core's text (internal/core/reception), kept as it is; ended is whether that
-- state is an end, as the core said when it was written, so "in progress"
-- stays right when a later version adds a state. calling_ae_titles is a JSON
-- array of at most 16.
CREATE TABLE receptions (
    id                TEXT    NOT NULL PRIMARY KEY,
    receiver          TEXT    NOT NULL,
    study_uid         TEXT    NOT NULL,
    arrival           INTEGER NOT NULL CHECK (arrival >= 1),
    state             TEXT    NOT NULL,
    ended             INTEGER NOT NULL CHECK (ended IN (0, 1)),
    files_received    INTEGER NOT NULL CHECK (files_received >= 0),
    bytes_received    INTEGER NOT NULL CHECK (bytes_received >= 0),
    files_archived    INTEGER NOT NULL CHECK (files_archived >= 0),
    bytes_archived    INTEGER NOT NULL CHECK (bytes_archived >= 0),
    files_duplicate   INTEGER NOT NULL CHECK (files_duplicate >= 0),
    bytes_duplicate   INTEGER NOT NULL CHECK (bytes_duplicate >= 0),
    files_quarantined INTEGER NOT NULL CHECK (files_quarantined >= 0),
    bytes_quarantined INTEGER NOT NULL CHECK (bytes_quarantined >= 0),
    files_released    INTEGER NOT NULL CHECK (files_released >= 0),
    subject_label     TEXT    NOT NULL,
    session_label     TEXT    NOT NULL,
    storage           TEXT    NOT NULL,
    failure           TEXT    NOT NULL,
    started           INTEGER NOT NULL,
    last_file         INTEGER NOT NULL,
    step_at           INTEGER NOT NULL,
    ended_at          INTEGER,
    associations      INTEGER NOT NULL CHECK (associations >= 0),
    calling_ae_titles TEXT    NOT NULL,
    calling_truncated INTEGER NOT NULL CHECK (calling_truncated IN (0, 1)),
    retry_of          TEXT    NOT NULL,
    retry_reception   TEXT    NOT NULL,
    discard_reason    TEXT    NOT NULL,
    discard_at        INTEGER
) STRICT;

-- One arrival number per study in the tenant; the next one is read from here.
CREATE UNIQUE INDEX receptions_arrival ON receptions (study_uid, arrival);
-- At most one reception of a study is receiving at one receiver: two opens
-- at once make one, in any database.
CREATE UNIQUE INDEX receptions_open ON receptions (receiver, study_uid) WHERE state = 'receiving';
-- The listings, newest first by start time, then id:
--   no filter, or a time range only   receptions_by_start
--   a receiver                        receptions_by_receiver
--   a study                           receptions_by_study
--   steps                             receptions_by_state
--   in progress                       receptions_in_progress (only those)
CREATE INDEX receptions_by_start       ON receptions (started, id);
CREATE INDEX receptions_by_receiver    ON receptions (receiver, started, id);
CREATE INDEX receptions_by_study       ON receptions (study_uid, started, id);
CREATE INDEX receptions_by_state       ON receptions (state, started, id);
CREATE INDEX receptions_in_progress    ON receptions (started, id) WHERE ended = 0;
-- The combinations of the list's filters that a tenant with many receptions
-- would otherwise answer by testing each row of one index: a receiver with
-- in progress, a receiver with steps, a study with steps. A filter with in
-- progress and steps keeps only the steps that have not ended, and needs no
-- index of its own.
CREATE INDEX receptions_receiver_in_progress ON receptions (receiver, started, id) WHERE ended = 0;
CREATE INDEX receptions_by_receiver_state    ON receptions (receiver, state, started, id);
CREATE INDEX receptions_by_study_state       ON receptions (study_uid, state, started, id);
-- Removing or moving a storage asks whether a reception in progress names
-- it.
CREATE INDEX receptions_by_storage ON receptions (storage, ended);

-- The associations that brought a reception's files, each once, so a batch
-- written again counts none twice.
CREATE TABLE reception_associations (
    reception_id TEXT NOT NULL,
    association  TEXT NOT NULL,
    PRIMARY KEY (reception_id, association)
) STRICT, WITHOUT ROWID;

-- A file that landed in the incoming area. The key is what makes a batch
-- written again change nothing: the same reception, instance and checksum is
-- the same file.
CREATE TABLE received_files (
    reception_id        TEXT    NOT NULL,
    sop_instance_uid    TEXT    NOT NULL,
    sha256              BLOB    NOT NULL CHECK (length(sha256) = 32),
    series_uid          TEXT    NOT NULL,
    study_uid           TEXT    NOT NULL,
    sop_class_uid       TEXT    NOT NULL,
    transfer_syntax_uid TEXT    NOT NULL,
    size                INTEGER NOT NULL CHECK (size >= 0),
    incoming            TEXT    NOT NULL,
    calling_ae          TEXT    NOT NULL,
    called_ae           TEXT    NOT NULL,
    remote_addr         TEXT    NOT NULL,
    association         TEXT    NOT NULL,
    released            INTEGER NOT NULL CHECK (released IN (0, 1)),
    received            INTEGER NOT NULL,
    PRIMARY KEY (reception_id, sop_instance_uid, sha256)
) STRICT, WITHOUT ROWID;

-- The duplicate check looks a batch's instance UIDs up here.
CREATE INDEX received_by_instance ON received_files (sop_instance_uid);

-- A quarantined file's record. details is a JSON object of the reason's
-- keys. A record stays after its file is released or rejected.
CREATE TABLE quarantine (
    id                  TEXT    NOT NULL PRIMARY KEY,
    reason              TEXT    NOT NULL,
    details             TEXT    NOT NULL,
    state               TEXT    NOT NULL CHECK (state IN ('held', 'released', 'rejected')),
    receiver            TEXT    NOT NULL,
    reception_id        TEXT    NOT NULL,
    calling_ae          TEXT    NOT NULL,
    called_ae           TEXT    NOT NULL,
    remote_addr         TEXT    NOT NULL,
    size                INTEGER NOT NULL CHECK (size >= 0),
    sha256              BLOB CHECK (sha256 IS NULL OR length(sha256) = 32),
    kept_sha256         BLOB CHECK (kept_sha256 IS NULL OR length(kept_sha256) = 32),
    sop_class_uid       TEXT    NOT NULL,
    sop_instance_uid    TEXT    NOT NULL,
    study_uid           TEXT    NOT NULL,
    series_uid          TEXT    NOT NULL,
    transfer_syntax_uid TEXT    NOT NULL,
    incoming            TEXT    NOT NULL,
    content_kept        INTEGER NOT NULL CHECK (content_kept IN (0, 1)),
    at                  INTEGER NOT NULL,
    decided_at          INTEGER,
    release_mode        TEXT    NOT NULL CHECK (release_mode IN ('', 'as_is', 'overwrite')),
    release_reception   TEXT    NOT NULL,
    release_receiver    TEXT    NOT NULL,
    reject_reason       TEXT    NOT NULL,
    released_from       TEXT    NOT NULL
) STRICT;

-- The listings, newest first by quarantine time, then id, one index per
-- equality filter; a time range narrows whichever is used. The instance
-- index also serves the duplicate check, which leaves out files a reception
-- quarantined.
CREATE INDEX quarantine_by_time      ON quarantine (at, id);
CREATE INDEX quarantine_by_state     ON quarantine (state, at, id);
CREATE INDEX quarantine_by_reason    ON quarantine (reason, at, id);
CREATE INDEX quarantine_by_receiver  ON quarantine (receiver, at, id);
CREATE INDEX quarantine_by_reception ON quarantine (reception_id, at, id);
CREATE INDEX quarantine_by_study     ON quarantine (study_uid, at, id);
CREATE INDEX quarantine_by_instance  ON quarantine (sop_instance_uid, at, id);
-- The combinations of the three filters that each keep many records, so a
-- look through the held files of one receiver or one reason reads only those.
CREATE INDEX quarantine_by_state_reason          ON quarantine (state, reason, at, id);
CREATE INDEX quarantine_by_state_receiver        ON quarantine (state, receiver, at, id);
CREATE INDEX quarantine_by_receiver_reason       ON quarantine (receiver, reason, at, id);
CREATE INDEX quarantine_by_state_receiver_reason ON quarantine (state, receiver, reason, at, id);
-- A discard leaves out the files a record keeps, found by their place.
CREATE INDEX quarantine_kept ON quarantine (incoming) WHERE content_kept = 1;

-- The quarantine's running totals: files held now and the bytes kept for
-- them, changed in the transaction of every change of a record, so they
-- equal a count over the records after every commit.
CREATE TABLE quarantine_totals (
    one   INTEGER NOT NULL PRIMARY KEY CHECK (one = 1),
    files INTEGER NOT NULL CHECK (files >= 0),
    bytes INTEGER NOT NULL CHECK (bytes >= 0)
) STRICT;
INSERT INTO quarantine_totals (one, files, bytes) VALUES (1, 0, 0);

-- The catalog. A subject is found by its label, a session by its Study
-- Instance UID, a scan by its Series Instance UID; each keeps the label it
-- was first given. folded is the label as label.Fold makes it, unique at its
-- level, so two labels that a file system ignoring case would make one
-- directory are never both held.
CREATE TABLE subjects (
    id      INTEGER NOT NULL PRIMARY KEY,
    label   TEXT    NOT NULL,
    folded  TEXT    NOT NULL UNIQUE,
    created INTEGER NOT NULL
) STRICT;

CREATE TABLE sessions (
    id         INTEGER NOT NULL PRIMARY KEY,
    study_uid  TEXT    NOT NULL UNIQUE,
    subject_id INTEGER NOT NULL,
    label      TEXT    NOT NULL,
    folded     TEXT    NOT NULL,
    storage    TEXT    NOT NULL,
    created    INTEGER NOT NULL,
    UNIQUE (subject_id, folded)
) STRICT;

CREATE TABLE scans (
    id              INTEGER NOT NULL PRIMARY KEY,
    session_id      INTEGER NOT NULL,
    series_uid      TEXT    NOT NULL UNIQUE,
    label           TEXT    NOT NULL,
    folded          TEXT    NOT NULL,
    instances       INTEGER NOT NULL CHECK (instances >= 0),
    bytes           INTEGER NOT NULL CHECK (bytes >= 0),
    manifest_sha256 BLOB CHECK (manifest_sha256 IS NULL OR length(manifest_sha256) = 32),
    created         INTEGER NOT NULL,
    updated         INTEGER NOT NULL,
    UNIQUE (session_id, folded)
) STRICT;

-- One row per SOP Instance UID in the tenant: the instance held (held_*,
-- confirmed by the reception held_by, in a scan, at a key of a storage),
-- the claim of a reception that has not confirmed yet (claim_*), or both,
-- for a claim to overwrite. A claim not confirmed is removed when its
-- reception ends; what is held stays.
CREATE TABLE instances (
    sop_instance_uid TEXT    NOT NULL PRIMARY KEY,
    study_uid        TEXT    NOT NULL,
    series_uid       TEXT    NOT NULL,
    held_sha256      BLOB CHECK (held_sha256 IS NULL OR length(held_sha256) = 32),
    held_by          TEXT    NOT NULL,
    scan_id          INTEGER,
    storage          TEXT    NOT NULL,
    storage_key      TEXT    NOT NULL,
    byte_offset      INTEGER NOT NULL CHECK (byte_offset >= 0),
    byte_length      INTEGER NOT NULL CHECK (byte_length >= 0),
    size             INTEGER NOT NULL CHECK (size >= 0),
    archived_sha256  BLOB CHECK (archived_sha256 IS NULL OR length(archived_sha256) = 32),
    held_at          INTEGER,
    claim_sha256     BLOB CHECK (claim_sha256 IS NULL OR length(claim_sha256) = 32),
    claimed_by       TEXT,
    claimed_at       INTEGER,
    CHECK (held_sha256 IS NOT NULL OR claimed_by IS NOT NULL),
    CHECK ((held_sha256 IS NULL) = (scan_id IS NULL)),
    CHECK ((claimed_by IS NULL) = (claim_sha256 IS NULL))
) STRICT, WITHOUT ROWID;

-- A reception's claims, to confirm or release them; the instances held on a
-- storage, which make it hold data; the instances a reception confirmed, by
-- scan, for its list of scans.
CREATE INDEX instances_claimed    ON instances (claimed_by) WHERE claimed_by IS NOT NULL;
CREATE INDEX instances_on_storage ON instances (storage) WHERE held_sha256 IS NOT NULL;
CREATE INDEX instances_held_by    ON instances (held_by, scan_id) WHERE held_sha256 IS NOT NULL;
CREATE INDEX scans_by_session     ON scans (session_id);

-- The catalog's running totals, changed in the transaction of every
-- commit, so they equal a count over the catalog after each.
CREATE TABLE catalog_totals (
    one       INTEGER NOT NULL PRIMARY KEY CHECK (one = 1),
    subjects  INTEGER NOT NULL CHECK (subjects >= 0),
    sessions  INTEGER NOT NULL CHECK (sessions >= 0),
    scans     INTEGER NOT NULL CHECK (scans >= 0),
    instances INTEGER NOT NULL CHECK (instances >= 0)
) STRICT;
INSERT INTO catalog_totals (one, subjects, sessions, scans, instances) VALUES (1, 0, 0, 0, 0);

-- A unit of work. due is when a queued unit may be claimed, and when a
-- leased unit's lease ends; attempt counts claims and fences every write of
-- the worker holding the lease. unique_key is NULL for none.
CREATE TABLE work (
    id           TEXT    NOT NULL PRIMARY KEY,
    kind         TEXT    NOT NULL,
    subject      TEXT    NOT NULL,
    unique_key   TEXT,
    state        TEXT    NOT NULL CHECK (state IN ('queued', 'leased', 'done', 'failed')),
    due          INTEGER NOT NULL,
    attempt      INTEGER NOT NULL CHECK (attempt >= 0),
    max_attempts INTEGER NOT NULL CHECK (max_attempts >= 1),
    owner        TEXT    NOT NULL,
    last_error   TEXT    NOT NULL,
    created      INTEGER NOT NULL,
    updated      INTEGER NOT NULL
) STRICT;

-- A key queues its work once while the work is queued or leased; a finished
-- unit frees it.
CREATE UNIQUE INDEX work_by_key ON work (unique_key) WHERE unique_key IS NOT NULL AND state IN ('queued', 'leased');
-- A claim reads the earliest due of a kind among the units not finished,
-- and only those: finished units, however many, are not in this index.
CREATE INDEX work_claimable ON work (kind, due, id) WHERE state IN ('queued', 'leased');
