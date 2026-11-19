# Adding a migration

A database's schema changes only by a new migration: a SQL file, forward only,
applied once, never edited after it is merged.

1. **Write it twice**, once per driver, as the next number in
   `internal/driver/sqlite/migrations/<master|tenant>/` and
   `internal/driver/postgres/migrations/<master|tenant>/`: `0008_<what>.sql`.
   Start the file with a comment saying why the change is needed. Text columns
   that hold identifiers use the `C` collation on Postgres.
2. **Keep it safe on a database in use**: a new column has a default or is
   nullable; an index on a large table is created so it does not block
   writes for long; data is moved in the same migration only when it is small.
3. **Record its checksum**: each driver's `tenant_test.go` (or `master_test.go`)
   lists every shipped migration with its checksum, so a merged migration
   that is edited fails the tests.
4. **Update the outside checks**: `scripts/check/tenant-sqlite.sh`,
   `tenant-postgres.sh` (and the master's) and `scripts/prove/run.sh` list
   the tables, the number of migrations, and on Postgres the indexes and the
   `C`-collated columns.
5. **Test the upgrade**: a database at the previous version is migrated by the
   new binary and keeps its rows (`migrate_test.go` in each driver).
