# Testing

How the tests and checks are run, what `-short` means here, how a slow test is
marked, and every `CORIA_*` environment variable the repository reads.

## Running the tests

| Command | What it runs |
|---|---|
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
CI sets it).

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

### Select or enable a test

| Variable | What it does | Default | Read by |
|---|---|---|---|
| `CORIA_TEST_POSTGRES` | The address of a scratch Postgres database (`postgres://...`) the tests may make schemas in; each test that makes a tenant there gets a schema of its own (`testkit.PostgresSchema`). | unset: the Postgres tests skip | `internal/driver/postgres`, `internal/receive`, `internal/cli/servecmd`; `make test-postgres`, `make outside-postgres`; `scripts/check/master-postgres.sh`, `scripts/check/tenant-postgres.sh` (required) |
| `CORIA_REQUIRE_POSTGRES` | When set to anything, a missing Postgres address is a failure instead of a skip. | unset | `internal/driver/postgres`, `internal/receive`, `internal/cli/servecmd`; set by `make test-postgres` and by CI |
| `CORIA_TEST_S3` | How to reach a scratch S3 server, as `http://KEY:SECRET@host:port/bucket`, with `region=NAME` and `pathstyle=0` in the query when needed. | unset: the tests against a server skip | `internal/blob/s3`, `internal/receive`, `internal/cli/servecmd`; `make test-s3` sets it for each server; the bucket must exist (the S3 driver's tests make it) |
| `CORIA_REQUIRE_S3` | When set to anything, a missing S3 server is a failure instead of a skip. | unset | `internal/blob/s3`, `internal/receive`, `internal/cli/servecmd`; set by `make test-s3` and by CI |
| `CORIA_TEST_SCALE` | When set, runs the scale tests of the reception pipeline that take minutes. | unset: they skip | `internal/receive` (`TestScale*`); set by `make test-scale` |
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
| `CORIA_MIGRATE_HELPER` | Turns the test binary into one of the processes that migrate one database at once | `internal/driver/sqlite`, `internal/driver/postgres` |
| `CORIA_OTHER_USER_DIAL` | The path of the socket the helper dials as the other user | `internal/server` |
| `CORIA_RECEIVE_CRASH_DIR`, `CORIA_RECEIVE_CRASH_POINT`, `CORIA_RECEIVE_CRASH_HIT`, `CORIA_RECEIVE_CRASH_RUN`, `CORIA_RECEIVE_CRASH_RESEND` | The data directory, the crash point, the hit, the run and the files to send again of the pipeline's crash child | `internal/receive` |
| `CORIA_SERVECMD_CHILD_ARGS`, `CORIA_SERVECMD_CHILD_REAL` | The arguments of the command the child runs, and whether it serves the real handlers | `internal/cli/servecmd` |
| `CORIA_STANDIN_DELAY` | How long the stand-in server holds a listing whose page token is `wait`, as a Go duration (default `200ms`) | `internal/server/servertest/standin`, driven by `scripts/check/server.sh` |
