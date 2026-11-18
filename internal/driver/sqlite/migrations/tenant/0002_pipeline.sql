-- What the reception pipeline keeps in a tenant's database: the tenant's
-- storages and receivers, its receptions and the files they received, its
-- quarantine with running totals, and the leased units of work that carry a
-- reception through its steps. Everything the pipeline must not lose in a
-- restart is a row here.
--
-- The rules of 0001_init.sql hold: every table is STRICT; times are INTEGER
-- microseconds since 1970 in UTC; text a caller did not give is '' rather
-- than NULL; a checksum is a 32-byte BLOB. There are no foreign keys: a
-- reception and a quarantine record keep the name of a receiver removed, by
-- the API contract's rule, and every other reference is checked by the
-- change that makes it, inside its transaction.
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
    files_received    INTEGER NOT NULL,
    bytes_received    INTEGER NOT NULL,
    files_archived    INTEGER NOT NULL,
    bytes_archived    INTEGER NOT NULL,
    files_duplicate   INTEGER NOT NULL,
    bytes_duplicate   INTEGER NOT NULL,
    files_quarantined INTEGER NOT NULL,
    bytes_quarantined INTEGER NOT NULL,
    files_released    INTEGER NOT NULL,
    subject_label     TEXT    NOT NULL,
    session_label     TEXT    NOT NULL,
    storage           TEXT    NOT NULL,
    failure           TEXT    NOT NULL,
    started           INTEGER NOT NULL,
    last_file         INTEGER NOT NULL,
    step_at           INTEGER NOT NULL,
    ended_at          INTEGER,
    associations      INTEGER NOT NULL,
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
-- Removing or moving a storage asks whether a reception names it.
CREATE INDEX receptions_by_storage     ON receptions (storage);

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
    sha256              BLOB,
    kept_sha256         BLOB,
    sop_class_uid       TEXT    NOT NULL,
    sop_instance_uid    TEXT    NOT NULL,
    study_uid           TEXT    NOT NULL,
    series_uid          TEXT    NOT NULL,
    transfer_syntax_uid TEXT    NOT NULL,
    incoming            TEXT    NOT NULL,
    content_kept        INTEGER NOT NULL CHECK (content_kept IN (0, 1)),
    at                  INTEGER NOT NULL,
    decided_at          INTEGER,
    release_mode        TEXT    NOT NULL,
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

-- The quarantine's running totals: files held now and the bytes kept for
-- them, changed in the transaction of every change of a record, so they
-- equal a count over the records after every commit.
CREATE TABLE quarantine_totals (
    one   INTEGER NOT NULL PRIMARY KEY CHECK (one = 1),
    files INTEGER NOT NULL CHECK (files >= 0),
    bytes INTEGER NOT NULL CHECK (bytes >= 0)
) STRICT;
INSERT INTO quarantine_totals (one, files, bytes) VALUES (1, 0, 0);

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
