# Receiving data through the API: a walk-through

This walks three uses of the API contract in `api/proto/magnetar/v1`, call by
call, with nothing but the contract in hand:

1. setting a tenant up to receive DICOM;
2. following one study from its first file to archived, through `Watch` and
   `GetStatus`, including a second arrival of the same study later;
3. reviewing a quarantined file and releasing it so that it replaces the file
   archived under the same SOP Instance UID.

The calls are shown as the JSON a plain HTTP client posts with the Connect
protocol: `POST https://<server>/<service>/<method>` with
`Content-Type: application/json` and `Authorization: Bearer mgt_<id>_<secret>`.
A gRPC or Connect client sends the same messages. Field names are in the JSON
form of protobuf: `lowerCamelCase`, 64-bit integers as strings, durations as
`"300s"`, enum values by name, and a field mask as one string of
`lowerCamelCase` paths separated by commas.

The examples use a tenant named `lab`, a server at `archive.example.org`, and
made-up UIDs under the `2.25` root (PS3.5 B.2). Answers are cut to the fields
that matter.

## Who may do what

| Service | Methods | Role on the tenant |
|---|---|---|
| `StorageService` | `AddStorage`, `SetStorage`, `RemoveStorage`, `TestStorage` | admin |
| | `GetStorage`, `ListStorages` | reader |
| `ReceiverService` | `AddReceiver`, `SetReceiver`, `RemoveReceiver` | admin |
| | `GetReceiver`, `ListReceivers` | reader |
| `ReceptionService` | `ListReceptions`, `GetReception` | reader |
| `QuarantineService` | `ReleaseQuarantinedFile`, `RejectQuarantinedFile` | operator |
| | `ListQuarantinedFiles`, `GetQuarantinedFile` | reader |
| `MonitorService` | `GetStatus`, `Watch` on one tenant | reader |
| | `GetStatus`, `Watch` on every tenant | operator on the installation |

A request is checked in a fixed order: the key first (`UNAUTHENTICATED`), then
the role (`PERMISSION_DENIED`), then the request itself (`INVALID_ARGUMENT`).
A caller with no grant on a tenant gets the same `PERMISSION_DENIED` whether
the tenant exists or not.

## Use 1: setting a tenant up to receive

The tenant `lab` exists, and the person setting it up holds a key with admin
on it.

### Allow a directory for storages

A directory storage must lie inside a directory the installation allows. That
is the installation's setting `storage.directory-roots`, set once by an admin
on the installation:

```
POST /magnetar.v1.SettingsService/SetSetting
{"setting": {"name": "storage.directory-roots",
             "scope": {"installation": {}},
             "value": "/srv/archive"}}
```

Without it, `AddStorage` refuses every directory storage as invalid. The rule
exists so that a tenant's admin cannot point a storage at another tenant's
files, or at the server's own data directory.

### Add a storage and test it

```
POST /magnetar.v1.StorageService/AddStorage
{"tenant": "lab",
 "storage": {"name": "main", "directory": {"path": "/srv/archive/lab"}}}
```

The answer is the storage as stored. Adding it records the configuration and
touches nothing on disk; the test does:

```
POST /magnetar.v1.StorageService/TestStorage
{"tenant": "lab", "name": "main"}

{"passed": true,
 "results": [{"step": "STORAGE_TEST_STEP_WRITE",   "outcome": "STORAGE_TEST_OUTCOME_PASSED", "duration": "0.004s"},
             {"step": "STORAGE_TEST_STEP_READ",    "outcome": "STORAGE_TEST_OUTCOME_PASSED", "duration": "0.001s"},
             {"step": "STORAGE_TEST_STEP_COMPARE", "outcome": "STORAGE_TEST_OUTCOME_PASSED", "duration": "0s"},
             {"step": "STORAGE_TEST_STEP_REMOVE",  "outcome": "STORAGE_TEST_OUTCOME_PASSED", "duration": "0.001s"}]}
```

A storage that fails is still a successful call: `passed` is false and the
failed step's `message` says why.

An S3 bucket is added the same way with `s3` in place of `directory`. Its
secret access key is sent once and never answered again; a read shows only
`secretAccessKeySet: true`. To rotate the credentials later, a `SetStorage`
names both fields in its mask:

```
POST /magnetar.v1.StorageService/SetStorage
{"tenant": "lab",
 "storage": {"name": "cold", "s3": {"accessKeyId": "AKIA...NEW", "secretAccessKey": "..."}},
 "updateMask": "s3.accessKeyId,s3.secretAccessKey"}
```

A mask that does not name `s3.secretAccessKey` keeps the secret held, so
changing the endpoint never needs the secret sent again.

### Add a receiver

The scanner calls itself `PRISMA1` and sits on `10.20.0.0/24`:

```
POST /magnetar.v1.ReceiverService/AddReceiver
{"tenant": "lab",
 "receiver": {"name": "scanner1",
              "enabled": true,
              "port": 11112,
              "aeTitle": "MAGNETAR",
              "allowedCallers": {"rules": [{"callingAeTitle": "PRISMA1",
                                            "addressRange": "10.20.0.0/24"}]},
              "storage": "main"}}
```

What the answer shows:

- `quietPeriod: "60s"`, `maxOpenTime: "3600s"` and `maxFileBytes: "4294967296"`:
  the defaults, filled in and stored as the receiver's own values.
- `naming` absent: the default labels, subject `{PatientID}`, session
  `{StudyDate}-{StudyInstanceUID}`, scan `{SeriesNumber}-{SeriesInstanceUID}`.
- `sopClasses` and `transferSyntaxes` empty: the server's default sets, every
  Storage SOP Class of PS3.4 Table B.5-1 and every transfer syntax of PS3.5
  Annex A that the server knows.
- `health`: `RECEIVER_STATE_STARTING` or already `RECEIVER_STATE_UP`.

Things that are easy to get wrong, and what the contract does about them:

- `enabled` left out is false: the receiver is stored but does not listen.
- `allowedCallers` left out admits nobody. Admitting every caller takes
  `"anyCaller": true`, and `"anyCaller": false` is refused, so it cannot be
  set by accident.
- A rule with neither an AE title nor an address range is refused: it would
  admit everyone.
- A port already used by something else on the server is not refused (it
  cannot be known in advance, and refusing it would tell one tenant about
  another); the receiver is answered down with
  `RECEIVER_DOWN_REASON_ADDRESS_IN_USE`.

### Change the quiet period

The scanner pauses for minutes between series, so a reception should wait
five minutes of quiet before it is complete:

```
POST /magnetar.v1.ReceiverService/SetReceiver
{"tenant": "lab",
 "receiver": {"name": "scanner1", "quietPeriod": "300s"},
 "updateMask": "quietPeriod"}
```

Only `quietPeriod` changes. Fields the mask does not name are ignored even
when they are present in the message.

### Check it is up

```
POST /magnetar.v1.MonitorService/GetStatus
{"scope": {"tenant": "lab"}}

{"tenants": [{"tenant": "lab",
              "databaseState": "TENANT_STATE_READY",
              "receivers": [{"receiver": "scanner1",
                             "health": {"state": "RECEIVER_STATE_UP", "since": "..."}}],
              "storages": [{"storage": "main", "usedBytes": "0", "freeBytes": "1834013425664"}],
              ...}],
 "time": "..."}
```

The tenant is ready to receive. A C-ECHO from `PRISMA1` (dcmtk's
`echoscu -aec MAGNETAR archive.example.org 11112`) is answered; from any other
caller the association is refused.

## Use 2: following one study to archived

A monitoring client wants to see a study arrive and know when it is safe on
the storage, missing nothing in between.

### Start from a status, then watch from its cursor

```
POST /magnetar.v1.MonitorService/GetStatus
{"scope": {"tenant": "lab"}}

{"tenants": [{"tenant": "lab",
              "eventCursor": "c1",
              "counts": {"subjects": "41", "sessions": "57", "scans": "612",
                         "instances": "88310", "quarantinedFiles": "0"}, ...}]}
```

The client keeps the counts and opens a stream from `eventCursor`:

```
POST /magnetar.v1.MonitorService/Watch
{"scope": {"tenant": "lab"}, "cursor": "c1"}
```

`Watch` is a server stream. Its first message is a keep-alive, sent at once,
so the client knows the stream is open; after that a keep-alive comes
whenever 15 seconds pass without an event. The client stores the cursor of
every message it receives. If the stream drops, it calls `Watch` again with
the last cursor and loses nothing, as long as that cursor is younger than the
tenant's event retention (90 days unless the setting `events.retention` says
otherwise). An older cursor is answered `OUT_OF_RANGE`; the client then calls
`GetStatus` again and watches from the new `eventCursor`.

The stream has no deadline unless the client sets one. The server keeps no
queue for the client: a client that reads slowly only falls behind.

### The study arrives

The scanner sends a study of three series. The events, in commit order:

```
{"event": {"cursor": "c2", "type": "EVENT_TYPE_ASSOCIATION_ACCEPTED",
           "associationAccepted": {"receiver": "scanner1",
             "caller": {"callingAeTitle": "PRISMA1", "calledAeTitle": "MAGNETAR",
                        "address": "10.20.0.31:50112"}}}}

{"event": {"cursor": "c3", "type": "EVENT_TYPE_RECEPTION_STEP",
           "receptionStep": {"reception": "r-7f3a", "receiver": "scanner1",
             "arrival": 1, "step": "RECEPTION_STEP_RECEIVING",
             "counts": {"filesReceived": "1", "bytesReceived": "528642"}}}}
```

The event carries the reception's id, never the Study Instance UID or any
other identifier the scanner sent. The client reads the reception to know
which study it is:

```
POST /magnetar.v1.ReceptionService/GetReception
{"tenant": "lab", "id": "r-7f3a"}

{"reception": {"id": "r-7f3a", "receiver": "scanner1",
               "studyInstanceUid": "2.25.1234567890123456789",
               "arrival": 1, "step": "RECEPTION_STEP_RECEIVING",
               "counts": {"filesReceived": "212", "bytesReceived": "112074304"},
               "startTime": "...", "lastFileTime": "..."},
 "arrivals": [{"id": "r-7f3a", "arrival": 1, "step": "RECEPTION_STEP_RECEIVING"}]}
```

While files arrive there is no event per file. Progress during receiving is
read from `GetReception` (its counts) or from `GetStatus` (the tenant's
`throughput` and `receptionsInProgress`).

### Complete, identified, archived, verified, done

Five minutes after the last file (the quiet period set in use 1), the
reception moves through its steps, one event each, each with the totals so
far:

```
{"event": {"cursor": "c4", "receptionStep": {"reception": "r-7f3a", "arrival": 1,
           "step": "RECEPTION_STEP_COMPLETE",
           "counts": {"filesReceived": "480", "bytesReceived": "253771776"}}}}
{"event": {"cursor": "c5", "receptionStep": {"reception": "r-7f3a", "arrival": 1,
           "step": "RECEPTION_STEP_IDENTIFIED",
           "subjectLabel": "SUBJ0042", "sessionLabel": "20261118-2.25.1234567890123456789", ...}}}
{"event": {"cursor": "c6", "receptionStep": {..., "step": "RECEPTION_STEP_ARCHIVED",
           "counts": {"filesReceived": "480", "filesArchived": "480", ...}}}}
{"event": {"cursor": "c7", "receptionStep": {..., "step": "RECEPTION_STEP_VERIFIED", ...}}}
{"event": {"cursor": "c8", "receptionStep": {..., "step": "RECEPTION_STEP_DONE",
           "counts": {"filesReceived": "480", "bytesReceived": "253771776",
                      "filesArchived": "480", "bytesArchived": "253771776"}}}}
```

`RECEPTION_STEP_VERIFIED` is the point the client was waiting for: every
archived file was read back from the storage and its SHA-256 matched the one
computed when it was received. At `RECEPTION_STEP_DONE`,
`filesReceived = filesArchived + filesDuplicate + filesQuarantined`.

The scans it archived:

```
POST /magnetar.v1.ReceptionService/GetReception
{"tenant": "lab", "id": "r-7f3a"}

{"reception": {..., "step": "RECEPTION_STEP_DONE", "storage": "main"},
 "scans": [{"label": "1-2.25.111", "seriesInstanceUid": "2.25.111", "files": "1", "bytes": "..."},
           {"label": "2-2.25.222", "seriesInstanceUid": "2.25.222", "files": "176", "bytes": "..."},
           {"label": "3-2.25.333", "seriesInstanceUid": "2.25.333", "files": "303", "bytes": "..."}]}
```

A new `GetStatus` shows `sessions` up by one, `scans` by three and
`instances` by 480. Since a reception's events carry totals, not changes, a
client that applies an event twice (one at the boundary of the status it
started from, say) counts nothing twice.

### A second arrival, later

Forty minutes later the scanner sends one more series of the same study. The
first reception has ended, so these files start a new reception: the second
arrival of the same session.

```
{"event": {"cursor": "c12", "receptionStep": {"reception": "r-80c1", "receiver": "scanner1",
           "arrival": 2, "step": "RECEPTION_STEP_RECEIVING", ...}}}
...
{"event": {"cursor": "c16", "receptionStep": {"reception": "r-80c1", "arrival": 2,
           "step": "RECEPTION_STEP_IDENTIFIED",
           "subjectLabel": "SUBJ0042", "sessionLabel": "20261118-2.25.1234567890123456789", ...}}}
...
{"event": {"cursor": "c19", "receptionStep": {"reception": "r-80c1", "arrival": 2,
           "step": "RECEPTION_STEP_DONE",
           "counts": {"filesReceived": "96", "filesArchived": "96", ...}}}}
```

The session label is the same as the first arrival's: a file is matched to its
session by its Study Instance UID before any naming rule runs, so a late file
always lands where the first ones did, on the same storage, even if the
receiver has been pointed at another storage since. `arrival: 2` tells a
consumer that this is an addition, and the counts are this arrival's own, so
it acts on the 96 new files only.

```
POST /magnetar.v1.ReceptionService/GetReception
{"tenant": "lab", "id": "r-80c1"}

{"reception": {"id": "r-80c1", "arrival": 2, "step": "RECEPTION_STEP_DONE", ...},
 "scans": [{"label": "4-2.25.444", "seriesInstanceUid": "2.25.444", "files": "96"}],
 "arrivals": [{"id": "r-7f3a", "arrival": 1, "step": "RECEPTION_STEP_DONE"},
              {"id": "r-80c1", "arrival": 2, "step": "RECEPTION_STEP_DONE"}]}
```

Every arrival of a study can also be listed with
`ListReceptions {"tenant": "lab", "studyInstanceUid": "2.25.1234567890123456789"}`.

## Use 3: reviewing a quarantined file and releasing it with overwrite

The scanner's operator corrected the patient's birth date and sent one image
again under the same SOP Instance UID (PS3.4 B.4.1.3 allows that). The archive
already holds that UID with other bytes, so it keeps the archived file,
answers the sender success, and holds the new file in quarantine.

### See it

On the stream:

```
{"event": {"cursor": "c25", "type": "EVENT_TYPE_FILE_QUARANTINED",
           "fileQuarantined": {"file": "q-19d2", "reception": "r-8a77", "receiver": "scanner1",
             "reason": "QUARANTINE_REASON_DIFFERENT_FILE", "sizeBytes": "528650"}}}
```

Or by listing what is held, which is how someone who was not watching
finds it:

```
POST /magnetar.v1.QuarantineService/ListQuarantinedFiles
{"tenant": "lab", "state": "QUARANTINE_STATE_HELD"}
```

### Review it

```
POST /magnetar.v1.QuarantineService/GetQuarantinedFile
{"tenant": "lab", "id": "q-19d2", "includeHeader": true}

{"file": {"id": "q-19d2",
          "reason": "QUARANTINE_REASON_DIFFERENT_FILE",
          "state": "QUARANTINE_STATE_HELD",
          "receiver": "scanner1", "reception": "r-8a77",
          "caller": {"callingAeTitle": "PRISMA1", "calledAeTitle": "MAGNETAR",
                     "address": "10.20.0.31:50288"},
          "sizeBytes": "528650",
          "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
          "keptSha256": "60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752",
          "sopClassUid": "1.2.840.10008.5.1.4.1.1.4",
          "sopInstanceUid": "2.25.987654321",
          "studyInstanceUid": "2.25.1234567890123456789",
          "seriesInstanceUid": "2.25.222",
          "transferSyntaxUid": "1.2.840.10008.1.2.1",
          "contentKept": true,
          "quarantineTime": "..."},
 "headerJson": "{\"00100030\":{\"vr\":\"DA\",\"Value\":[\"19800102\"]}, ...}"}
```

The person reviewing it sees why (`reason`), what is held (`sha256`) and what it would
replace (`keptSha256`), who sent it, and the file's header in the DICOM JSON
model (PS3.18 Annex F) without its pixel data. The header holds what the
sender sent, identifying data included: reading it needs reader on the tenant,
like any of the tenant's data, and the read is recorded in the tenant's audit
log.

This contract has no call that reads the archived file itself, so the
comparison with the kept file rests on what the person reviewing it knows of
the correction and on the two checksums.

### Release it with overwrite

```
POST /magnetar.v1.QuarantineService/ReleaseQuarantinedFile
{"tenant": "lab", "id": "q-19d2", "mode": "RELEASE_MODE_OVERWRITE"}

{"file": {"id": "q-19d2", "state": "QUARANTINE_STATE_RELEASED",
          "releaseMode": "RELEASE_MODE_OVERWRITE",
          "releaseReception": "r-8b02", "decideTime": "...", ...}}
```

The file runs through the pipeline again as if it arrived now at `scanner1`,
in reception `r-8b02`. With overwrite, it replaces the archived file on the
storage; the replaced file's checksum stays on the record (`keptSha256`), and
the release is in the tenant's audit log with the caller's key, so the
replacement has its reason and its proof. The stream shows the reception's
steps; at `RECEPTION_STEP_DONE` its counts read
`{"filesReceived": "1", "filesArchived": "1", "filesReleased": "1"}`, and the
replacing file was read back and checked like any other.

What the contract settles about this call:

- Releasing again with the same mode answers the record as it stands and
  does nothing more, so a client may retry after a lost answer.
- `RELEASE_MODE_OVERWRITE` is refused as invalid for any other reason: an
  instance under another study is never moved by a release.
- A rejected file, or one whose bytes were never kept (too large), cannot be
  released: `ALREADY_EXISTS`, the contract's conflict.
- If the file fails again, it is quarantined under a new record with a new
  id; this record stays released.

Had the person reviewing it decided the new file was wrong, the call would have been a
reject, which needs a reason and removes the held bytes for good:

```
POST /magnetar.v1.QuarantineService/RejectQuarantinedFile
{"tenant": "lab", "id": "q-19d2", "reason": "re-sent by mistake; the archived file is correct"}
```

## What this contract does not do

Said plainly, so a client does not look for it:

- It does not read archived files or their headers back; the catalog and
  data services do that.
- A reception that ended in `RECEPTION_STEP_FAILED` has no call that retries
  it; its files stay in the server's incoming area.
- Quarantined files are released or rejected one at a time, by id.
- A receiver's TLS certificate and key are files placed on the server by a
  person with access to it; the API names them and never carries their
  content.
- `Watch` on every tenant at once cannot be resumed: it starts from now, and
  each event's cursor resumes only a `Watch` of that event's tenant.
