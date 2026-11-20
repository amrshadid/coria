# Testing

How the tests and checks are run, what `-short` means here, how a slow test is
marked, and every `CORIA_*` environment variable the repository reads.

## Running the tests

| Command | What it runs |
|---|---|
| `make gates` | The gates before each push, and never a scale test: `check-comments`, `fmt`, `go vet` on 64 bits, linux/386 and windows, `lint`, and `make test`. |
| `make test` | `go test -race -count=1 ./...`: every package under the race detector. A test that needs a service (Postgres, S3, a tool such as dcmtk or sqlite3) skips, saying why, when the service is not there. |
| `make test-scale` | The tests that measure at scale, without the race detector (see below). Takes minutes. |
| `make test-postgres` | The database drivers' tests against a Postgres, failing if there is none (`CORIA_TEST_POSTGRES`, or the container `make pg-up` starts). |
| `make test-s3` | The S3 driver's tests against each S3 server `make s3-up` starts, failing if one is not up. |
| `make test-386` | The tests as 32-bit binaries, on a Linux x86-64 host. |
| `make fuzz` | Each fuzz target for `FUZZTIME` (10 seconds unless given). |
| `go test -short ./...` | Only the tests that start no process and take under a second. |

The outside checks use a tool that is not CORIA's own code, so a result is
not only the program agreeing with itself:

| Command | What it checks against what |
|---|---|
| `make check-seal` | The sealed-secret format, against Python's cryptography package |
| `make check-metadata` | The Metadata service's answers, against pydicom's `Dataset.to_json_dict()` |
| `make check-catalog` | The Catalog service's answers, against what pydicom reads of the received files |
| `make check-frames` | Each file's frame table, against the fragments pydicom divides each frame into |
| `make check-dicomweb` | QIDO-RS and WADO-RS, from dicomweb-client, against pydicom's reading of the files received |
| `make check-stow` | STOW-RS in a real server process, from dicomweb-client, against the bytes sent and pydicom |
| `make check-master-sqlite`, `make check-tenant-sqlite` | The SQLite databases, read back with the `sqlite3` tool |
| `make check-master-postgres`, `make check-tenant-postgres` | The Postgres databases, read back with `psql` and `pg_dump` |
| `make check-server` | The server's contract, with grpcurl, Python's gRPC client and curl |
| `make check-e2e` | The real program from `init` to a restart, each command a process |
| `make check-json` | Every command's `--json` output, parsed by Python's `json` module |
| `make check-completion` | The shell completion script |
| `make check-tui` | The interactive program on a real pseudo-terminal, driven from Python |
| `make prove` | The proof script: checks made from outside the binary |
| `make outside` | Every outside check that needs a tool that is not Go |
| `make ci` | Every job of the CI workflow on one machine |

## What `-short` means

`-short` means "start no process and take under a second". A test that starts a
child process, a server or a tool, or that takes longer, calls `t.Skip` under
`testing.Short()`. `go test -short ./...` is therefore a quick check that needs
nothing installed. The ordinary run (`make test`) does not use it.

## How a slow test is marked

A test that measures at scale is named `TestScale*` and runs only in its own
run, `make test-scale`; the ordinary run skips it. A test that needs a service
asks for it through an environment variable below and skips when the variable
is unset (or fails, when the matching `CORIA_REQUIRE_*` variable is set, as
`make test-postgres` and `make test-s3` set it).

The catalog is measured at scale by `TestScaleCatalog` in each database
driver, on a synthetic tenant `testkit.GenerateTenant` writes through the
tenant port: it runs only when `CORIA_SCALE_INSTANCES` says how large, since
ten million instances take from minutes to hours. `TestScaleCatalog` is
skipped in `make test-scale` without it. One run of each:

```
CORIA_SCALE_INSTANCES=10000000 CORIA_SCALE_SUMMARY=$GITHUB_STEP_SUMMARY \
  go test -count=1 -v -timeout 6h -run '^TestScaleCatalog$' ./internal/driver/sqlite
CORIA_TEST_POSTGRES=postgres://... CORIA_SCALE_INSTANCES=10000000 CORIA_SCALE_SUMMARY=$GITHUB_STEP_SUMMARY \
  go test -count=1 -v -timeout 6h -run '^TestScaleCatalog$' ./internal/driver/postgres
go test -run '^$' -bench BenchmarkPackedScan -benchtime 10x ./internal/blob/dir
CORIA_TEST_S3=http://KEY:SECRET@host:port/bucket go test -run '^$' -bench BenchmarkPackedScan -benchtime 10x ./internal/blob/s3
```

A Postgres tenant takes about 1.7 KB of disk per instance, tables and
indexes together. The storage drivers' benchmark
`BenchmarkPackedScan` reads a scan of 500 small instances kept one object per
instance and kept as one packed object read by ranges.

The SQLite driver's event feed is measured at scale by `TestScaleEventFeed` (a
million events) and `TestScaleWriterBesideIdleWatches` (a thousand idle
watches). Both run in `make test-scale` and are skipped under `-short`, under
the race detector, on 32 bits and on Windows. They take no variable.

## Environment variables

Most variables below are read by tests and scripts. The four that the program
itself reads are listed first.

### Read by the program

| Variable | What it does | Default | Read by |
|---|---|---|---|
| `CORIA_DATA_DIR` | The data directory: where `init` and `serve` work, and whose `run/coria.sock` the commands reach. `--data-dir` wins. | none: a command that needs one says so | `init`, `serve`, every command; the end-to-end and interactive-program tests and checks, the proof script |
| `CORIA_SERVER` | The address of a server on the network, reached over TLS. `--server` wins. | none | every command; the command-line tests, the end-to-end test, the proof script |
| `CORIA_KEY` | The key used over the network; the only place a key comes from besides `--key-file`. | none | every command; the same tests and scripts |
| `CORIA_CA_FILE` | A PEM file of certificate authorities to trust for the network, in place of the system's. `--ca-file` wins. | the system's | every command; the command-line tests, the end-to-end test, the proof script |
| `CORIA_TLS_CLIENT_CERT` | A PEM file of a TLS client certificate to present to the server on the network, for a key that works only with it. `--tls-client-cert` wins. | none | every command; the command-line tests |
| `CORIA_TLS_CLIENT_KEY` | The PEM file of that certificate's private key, private to its owner. `--tls-client-key` wins. | none | every command; the command-line tests |

### Select or enable a test

| Variable | What it does | Default | Read by |
|---|---|---|---|
| `CORIA_TEST_POSTGRES` | The address of a scratch Postgres database (`postgres://...`) the tests may make schemas in; each test that makes a tenant there gets a schema of its own (`testkit.PostgresSchema`). | unset: the Postgres tests skip | `internal/driver/postgres`, `internal/receive`, `internal/cli/servecmd`; `make test-postgres`, `make outside-postgres`; `scripts/check/master-postgres.sh`, `scripts/check/tenant-postgres.sh` (required) |
| `CORIA_REQUIRE_POSTGRES` | When set to anything, a missing Postgres address is a failure instead of a skip. | unset | `internal/driver/postgres`, `internal/receive`, `internal/cli/servecmd`; set by `make test-postgres` and by CI |
| `CORIA_TEST_S3` | How to reach a scratch S3 server, as `http://KEY:SECRET@host:port/bucket`, with `region=NAME` and `pathstyle=0` in the query when needed. | unset: the tests against a server skip | `internal/blob/s3`, `internal/receive`, `internal/cli/servecmd`; `make test-s3` sets it for each server; the bucket must exist (the S3 driver's tests make it) |
| `CORIA_REQUIRE_S3` | When set to anything, a missing S3 server is a failure instead of a skip. | unset | `internal/blob/s3`, `internal/receive`, `internal/cli/servecmd`; set by `make test-s3` and by CI |
| `CORIA_TEST_SCALE` | When set, runs the scale tests (`TestScale*`, and the tests of the SQLite and Postgres drivers and the registry that work at millions of rows or 1,000 tenants); without it they skip, so a plain `go test` stays quick. | unset: they skip | `internal/receive`, `internal/driver/sqlite`, `internal/driver/postgres`, `internal/registry`; set by `make test-scale` |
| `CORIA_TEST_ALL_SEEDS` | When set, `TestPipelineModelAllSeeds` runs all 150 seeds of the SQLite pipeline model (minutes, without the race detector); the everyday run takes 8 seeds (6 under the race detector). | unset: it skips | `internal/driver/sqlite`; set by `make test-scale`, which CI runs |
| `CORIA_SCALE_INSTANCES` | How many instances the catalog's scale tests make, through the tenant port, from a seed (`testkit.GenerateTenant`); they then log reception's rate into the empty and the large tenant, table and index sizes, the plan and the median and 99th percentile time of each catalog read, and the time of `VACUUM`. Ten million takes hours on SQLite and about 1.7 KB of database per instance on Postgres. No make target sets it. | unset: they skip | `internal/driver/sqlite`, `internal/driver/postgres` (`TestScaleCatalog`) |
| `CORIA_SCALE_SUMMARY` | A file the catalog's scale tests append their numbers to as a Markdown table (in CI, `$GITHUB_STEP_SUMMARY`); the table is logged either way. | unset: logged only | `internal/driver/sqlite`, `internal/driver/postgres` (`TestScaleCatalog`) |
| `CORIA_SCALE_KEEP` | When set to anything, the catalog's scale tests keep the database they made (a SQLite file in the temporary directory, a Postgres schema `scale_...`) and log where it is, so its plans can be read again with `sqlite3` or `psql`; whoever set it removes it. | unset: removed at the end | `internal/driver/sqlite`, `internal/driver/postgres` (`TestScaleCatalog`) |
| `CORIA_SCALE` | When set, the event feed's adversarial test runs its long form (400 operations instead of 60). No make target sets it. | unset | `internal/driver/sqlite` (`TestEventFeedUnderAdversaries`) |
| `CORIA_MEASURE` | When set, runs the measurement of the gap between the two denial paths; it is a measurement, not a check. Run it without the race detector. | unset: it skips | `internal/access` (`TestMeasureDeniedGap`) |
| `CORIA_BLOB_SMALL_VOLUME` | A directory on a small volume (a tmpfs of a few megabytes, say) for the full-volume test of the directory storage driver. | unset: it skips | `internal/blob/dir` (`TestFullVolumeKeepsOldObject`) |
| `CORIA_E2E` | When set, runs the real program end to end (about fifty processes). | unset: it skips | `cmd/coria` (`TestRealServerEndToEnd`); set by `make check-e2e` |
| `CORIA_TEST_OTHER_USER` | The name of a second operating system user, for the test that the local socket refuses another user. It needs `sudo -n -u` to work without a password. | unset: it skips | `internal/server`; set by CI; `make ci` runs the test when it is set |
| `CORIA_SQLITE3_REQUIRED` | When set to anything, a missing `sqlite3` tool is a failure instead of a skip. | unset | `internal/driver/sqlite` (kill and plan tests); set by `scripts/check/master-sqlite.sh` |
| `CORIA_PYTHON` | The Python, with pynetdicom, that the DICOM receiver's outside tests use. | `python3` on the `PATH` | `internal/receive/dimse` |
| `CORIA_DCMTK` | A directory holding dcmtk's programs, looked in before the `PATH` (another program of the same name may come first on it). | the `PATH` only; the test skips when dcmtk is not found | `internal/receive/dimse`, `internal/cli/servecmd`; set by `make test-postgres` and `make test-s3` from `DCMTK` |
| `CORIA_REQUIRE_DCMTK` | When set to anything, a missing dcmtk `storescu` is a failure instead of a skip. | unset | `internal/cli/servecmd`; set by `make test-postgres` and `make test-s3` |
| `CORIA_RECEIVE_CRASH_ONLY` | A comma-separated list of crash points to try, instead of all of them. | unset: every point | `internal/receive` (crash test) |

### Written for a script or an outside check

| Variable | What it does | Default | Read by |
|---|---|---|---|
| `CORIA_CHECK_JSON_DIR` | The directory the test writes each command's `--json` output into, for `scripts/check/cli-json.py`. | unset: the test skips | `internal/cli` (`TestWriteJSONOutputs`); set by `make check-json` |
| `CORIA_CHECK_SEAL_PYTHON` | The Python, with the cryptography package, that the sealed-format check runs. Set, even to nothing, it means the check was asked for and a missing interpreter fails. | unset: the test skips | `internal/secret` (`TestSealOpensInPython`); set by `make check-seal` |
| `CORIA_CHECK_METADATA_PYTHON` | The Python, with pydicom, that compares the Metadata service's answers with `Dataset.to_json_dict()` (`scripts/check/metadata-pydicom.py`). Set, even to nothing, it means the check was asked for and a missing interpreter fails. | unset: the test skips | `internal/api` (`TestMetadataEqualsPydicom`); set by `make check-metadata` |
| `CORIA_CHECK_CATALOG_PYTHON` | The Python, with pydicom, that compares the Catalog service's answers (subjects, sessions, scans, their counts and bytes, and the study date, series description and modality lists) with what pydicom reads of the received files (`scripts/check/catalog-pydicom.py`). Set, even to nothing, it means the check was asked for and a missing interpreter fails. `CORIA_PYDICOM_CORPUS` adds files to the archive. | unset: the test skips | `internal/api` (`TestCatalogEqualsPydicom`); set by `make check-catalog` |
| `CORIA_CHECK_DOWNLOAD_PYTHON` | The Python that checks the plain HTTP download of instances, from the real server's local socket after a reception through it, with curl (whole, resumed in two ranges and by `curl -C -`, a range past the end, If-Range with another tag, HEAD) and hashlib against each scan's manifest as the storage holds it (`scripts/check/download-curl.py`); curl must be on the `PATH`. Set, even to nothing, it means the check was asked for and a missing interpreter fails. | unset: the check is left out of the test | `internal/cli/servecmd` (`TestAStudyIsArchivedThroughServe`, which needs dcmtk's `storescu`) |
| `CORIA_CHECK_FRAMES_PYTHON` | The Python, with pydicom, that compares each file's frame table (where each frame lies in the file) with the fragments pydicom divides each frame into, the item offsets walked by the script itself, and the bytes each range names with pydicom's (`scripts/check/frames-pydicom.py`). It runs with `-I`, so the pydicom it uses is the one installed; with pydicom 3 the division is `encaps.generate_fragmented_frames` (Extended Offset Table included), with pydicom 2 `encaps.generate_pixel_data`. Set, even to nothing, it means the check was asked for and a missing interpreter fails. `CORIA_PYDICOM_CORPUS` adds every corpus file with pixel data and two files the script builds from it. | unset: the test skips | `internal/receive/frames` (`TestTableEqualsPydicom`); set by `make check-frames` |
| `CORIA_CHECK_DICOMWEB_PYTHON` | The Python, with dicomweb-client and pydicom, that checks a tenant's DICOMweb services from a client that is not CORIA's (`scripts/check/dicomweb-client.py`): it searches every study, series and instance of an archive made by reception (QIDO-RS, paging through the results), retrieves each instance as stored, its metadata, its bulk data and its first and last frame (WADO-RS), and compares each with pydicom's reading of the file received. The key reaches the script in its environment, never its command line. dicomweb-client is not in the base environment; install it into a virtual environment (`python3 -m venv`, then `pip install dicomweb-client pydicom`). Set, even to nothing, it means the check was asked for and a missing interpreter fails. `CORIA_PYDICOM_CORPUS` adds files to the archive. | unset: the test skips | `internal/api` (`TestDICOMwebWithDicomwebClient`); set by `make check-dicomweb` |
| `CORIA_CHECK_STOW_PYTHON` | The Python, with dicomweb-client and pydicom, that checks the STOW-RS receiver from a client that is not CORIA's (`scripts/check/stow-dicomweb-client.py`), in a real server process: it stores the public CT study with dicomweb-client and checks the answer (each instance with its WADO-RS Retrieve URL; an instance of another study refused 409 with A900H), then, once the reception is done, compares each archived file with the bytes sent, with hashlib against its scan's manifest and as a data set with pydicom. The key reaches the script in its environment, never its command line. Install dicomweb-client into a virtual environment as for `CORIA_CHECK_DICOMWEB_PYTHON`. Set, even to nothing, it means the check was asked for and a missing interpreter fails. | unset: the test skips | `internal/cli/servecmd` (`TestSTOWWithDicomwebClient`); set by `make check-stow` |
| `CORIA_DICOMWEB_KEY` | The key `scripts/check/dicomweb-client.py` sends, as `Authorization: Bearer`; given in the script's environment so it never appears on a command line. | none: the script fails without it | set by `internal/api` (`TestDICOMwebWithDicomwebClient`) and `scripts/prove/reading_web.py`; read by `scripts/check/dicomweb-client.py` |
| `CORIA_STOW_KEY` | The key `scripts/check/stow-dicomweb-client.py` stores with, given the same way. | none: the script fails without it | set by `internal/cli/servecmd` (`TestSTOWWithDicomwebClient`) and `scripts/prove/reading_web.py`; read by `scripts/check/stow-dicomweb-client.py` |
| `CORIA_PYDICOM_CORPUS` | pydicom's `test_files` directory, from which the metadata comparison also receives implicit VR, big endian, padded, JPEG-LS and explicit big endian files, and whose files with pixel data the frame table comparison adds. | unset: only the files of `internal/receive/testdata` | `internal/api` (`TestMetadataEqualsPydicom`, `TestCatalogEqualsPydicom`, `TestDICOMwebWithDicomwebClient`), `internal/receive/frames` (`TestTableEqualsPydicom`); set from `PYDICOM_CORPUS=` by `make check-metadata`, `make check-catalog`, `make check-dicomweb` and `make check-frames` |
| `CORIA_CHECK_TENANTS` | `1` when the server under check is the real program, which makes the server check go on to tenants and receptions. | unset | `scripts/check/server.sh` sets it, `scripts/check/server.py` reads it |
| `CORIA_CHECK_ROOT` | The directory root the real server was given as `fast`, for the reception calls of the server check. | none; required with `CORIA_CHECK_TENANTS=1` | `scripts/check/server.sh`, `scripts/check/server.py` |
| `CORIA_SAMPLE_DIR` | A directory the sample-writing test uses as its data directory, for a tool such as `sqlite3` to read back. | unset: the test skips | `internal/driver/sqlite`, `internal/driver/postgres`, `internal/registry`; set by `scripts/check-master-*.sh` and `scripts/check-tenant-*.sh` |
| `CORIA_SAMPLE_SCHEMA` | The name of a Postgres schema to make for the sample-writing test (it must not exist). | unset: the test skips | `internal/driver/postgres`; set by `scripts/check/master-postgres.sh`, `scripts/check/tenant-postgres.sh` |
| `CORIA_S3_OUTSIDE_MANIFEST` | A file the S3 driver's test writes the keys, sizes and SHA-256 of what it put into, for an outside reader to compare. | unset: the test skips | `internal/blob/s3` (`TestWriteForOutsideCheck`) |
| `CORIA_SERVED_ANSWERS` | A file the test of refused requests writes the served method's answers into, so two builds can be compared. | unset: nothing is written | `internal/server` (`TestNotServedRefusedAsServed`) |

### The proof script

| Variable | What it does | Default | Read by |
|---|---|---|---|
| `CORIA_PROVE_STRICT` | `1`: a check of the proof that is skipped is a failure, as in CI. | `0` | `scripts/prove/run.sh`; set to `1` by `make ci` on Linux and by CI, not by `make prove` itself |
| `CORIA_PROVE_BUILD` | `host` builds only this machine's binary, for a quicker run; the check of the static build for every target is then skipped. | `all` | `scripts/prove/run.sh` |
| `CORIA_PROVE_ONLY` | `reading` runs the builds and the sections that read the archive (13 to 19: an installation of their own built by sending public studies with dcmtk's `storescu`, then the catalog, metadata, downloads, DICOMweb and STOW-RS, the Python client and the refusals) alone, without Docker; their tenant on Postgres is then skipped. They need dcmtk (`DCMTK=` its directory) and a `PYTHON` with `scripts/requirements/check-server.txt` and `scripts/requirements/prove-reading.txt`. | unset: every section | `scripts/prove/run.sh` |
| `CORIA_PROVE_CRASHES` | How many times the crash check kills the server while it creates a tenant. | `24` | `scripts/prove/run.sh` |
| `CORIA_PROVE_PG_IMAGE` | The Postgres image the proof starts, when it starts its own container. | `postgres:18` | `scripts/prove/run.sh` |
| `CORIA_PROVE_PG_CONTAINER` | A running Postgres container (such as CI's service container) the proof uses instead of starting one. Needs `CORIA_PROVE_PG_PORT`. | none: the proof starts and removes its own | `scripts/prove/run.sh`; set by `make ci` and by CI |
| `CORIA_PROVE_PG_PORT` | The port of that container on `127.0.0.1`. | none | `scripts/prove/run.sh`; set by `make ci` and by CI |

### Set by a test for its own child process

A test that must kill or run a real process starts its own test binary again
with these set. Nobody sets them by hand, and the test that reads one does
nothing when it is run alone.

| Variable | What it carries | Set and read by |
|---|---|---|
| `CORIA_BLOBTEST_FLAW` | Which promise of the storage interface a deliberately flawed driver breaks | `internal/blob/suitecheck` |
| `CORIA_BLOB_CRASH_ROOT`, `CORIA_BLOB_CRASH_GENERATION`, `CORIA_BLOB_CRASH_STALL` | The root, the first generation and the stall point of the child that writes until it is killed | `internal/blob/dir` |
| `CORIA_E2E_ARGS` | The arguments of a command the end-to-end test runs as the program | `cmd/coria` |
| `CORIA_HEADER_QUIET_CHILD` | Makes the child read damaged files so the test can check nothing reaches standard error | `internal/receive/header` |
| `CORIA_INCOMING_CRASH_ROOT`, `CORIA_INCOMING_CRASH_POINT`, `CORIA_INCOMING_CRASH_HIT` | The area's root, the step to stop at and on which hit | `internal/receive/incoming` |
| `CORIA_KILL_CHILD_DB` | The database the child makes changes to until it is killed | `internal/driver/sqlite` |
| `CORIA_KILL_RECEIVED_DB` | The tenant database the child records received files into, from many receptions at once, until it is killed | `internal/driver/sqlite` (`TestKillDuringReceivedBatches`) |
| `CORIA_MIGRATE_HELPER` | Turns the test binary into one of the processes that migrate one database at once | `internal/driver/sqlite`, `internal/driver/postgres` |
| `CORIA_OTHER_USER_DIAL` | The path of the socket the helper dials as the other user | `internal/server` |
| `CORIA_RECEIVE_CRASH_DIR`, `CORIA_RECEIVE_CRASH_POINT`, `CORIA_RECEIVE_CRASH_HIT`, `CORIA_RECEIVE_CRASH_RUN`, `CORIA_RECEIVE_CRASH_RESEND` | The data directory, the crash point, the hit, the run and the files to send again of the pipeline's crash child | `internal/receive` |
| `CORIA_SERVECMD_CHILD_ARGS`, `CORIA_SERVECMD_CHILD_REAL` | The arguments of the command the child runs, and whether it serves the real handlers | `internal/cli/servecmd` |
| `CORIA_STANDIN_DELAY` | How long the stand-in server holds a listing whose page token is `wait`, as a Go duration (default `200ms`) | `internal/server/servertest/standin`, driven by `scripts/check/server.sh` |
