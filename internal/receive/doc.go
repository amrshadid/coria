// Package receive is the reception pipeline of one tenant: from a file handed
// in by a source to its reception done, with every file archived, held in
// quarantine with its reason, or counted as a duplicate, and nothing lost or
// archived twice whatever happens to the process.
//
// It joins ports and is not a driver: it is handed the tenant's database
// (tenant.Pipeline), a way to open the tenant's storages (blob.Store), the
// tenant's keyed hash for naming, the live feed's wake, a clock, a logger
// and the root of the incoming area, and it imports no database or storage
// driver. Each part is small and is tested alone with fakes of the ports.
//
// # The parts
//
//   - A source hands in one file at a time through Sink.Accept and tells
//     its sender the Outcome. DirSource is one; a DICOM listener is another,
//     added apart.
//   - The incoming area (package incoming) keeps each file whole and synced
//     before its sender is told it was received.
//   - The receiving side checks each file's header (package header: is it
//     DICOM, is its SOP class taken, are its UIDs well formed) and records
//     it as received or holds it, in batches a few times a second (batcher).
//   - Workers, a bounded pool, claim leased units of work and run each step
//     of a reception: quiet, identify, archive (with the scan's metadata
//     sidecar, package sidecar, and its manifest) and verify, which also
//     finishes. Each step's order of effects is written at its top, so that
//     running it twice, or after a crash at any line, changes nothing more.
//   - Recover, at start, reconciles the incoming area with the database.
//   - Release, Reject, Retry and Discard are the store's decisions with what
//     the pipeline does around them to the bytes and the work queue.
//
// # What makes a crash safe
//
// Every state the pipeline must not lose is in the tenant's database or in
// the name of a file in the incoming area; nothing is kept only in memory.
// Every write of a worker carries its unit's fence (tenant.WorkFence), so a
// worker whose lease was taken over changes nothing. Every step that holds a
// file gives the record an id made from the reception and the file, so a
// step run again writes it once. Every Put replaces an object whole, so a
// Put run again leaves the same object. A step queues the next unit in the
// transaction that completes its own (CompleteWork with next), so a
// reception past receiving always has its unit; a receiving one is given
// its quiet unit when it opens, and Start queues what a crash left missing.
//
// # One process per installation
//
// A scan's manifest and sidecar are merged from what the storage holds,
// under a lock this process holds per storage and scan, since the catalog
// offers no listing of a scan's instances to rebuild them from. That holds
// while one process serves an installation, as the server's lock on its
// data directory ensures; two processes writing one scan could each drop
// the other's entry from the manifest (never from the catalog).
//
// # What this package must never do
//
// It must never import a database or storage driver, parse DICOM with its
// own code, read a setting itself, read the clock but through Clock, start a
// goroutine that no one waits for, or keep a global. It must never put a
// value read from a file's header in a log line, an error's text, an event
// or a metric: labels, ids, counts and fixed phrases only. It must never
// remove a file from the incoming area before the database has recorded why
// (a reception verified, a reception discarded, a record rejected), and
// never delete anything from a storage.
package receive
