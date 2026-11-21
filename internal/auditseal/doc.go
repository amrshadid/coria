// Package auditseal seals the installation's audit logs and checks them: the
// sealing pass the server runs every audit.seal-interval and when it stops,
// retention by audit.retention, and the verification and export that the
// API's AuditSealService and the command line call.
//
// # The pass
//
// A pass seals the master's log first, then each tenant's. For one log it
// reads the log's latest checkpoint, then the entries that have no seq yet,
// in order of time then id (master.AuditSealWrites.UnsealedAudit), gives
// them the seqs after the checkpoint's, folds them into the chain and signs
// a checkpoint with the installation's audit signer (package auditchain),
// and hands both to the store, which keeps the seqs and the checkpoint in
// one transaction (SealAudit). At most master.MaxAuditSealBatch entries go
// into one checkpoint, so a long backlog, as after an upgrade, is sealed in
// several, each its own transaction.
//
// An entry written while a pass runs, or committed late (on Postgres a
// transaction begun before the pass may commit after it read), is not in
// that pass's checkpoint and is sealed by the next: a seq is given at
// sealing, never at writing, so nothing waits on the order of commits.
//
// Then the cross-anchors: each tenant's log keeps the master's latest
// checkpoint, and the master keeps each tenant's (SetAuditAnchor), so a
// tenant's database rewritten whole, its checkpoints included, disagrees
// with what the master keeps of it, and the other way round. An anchor
// that would go back is refused by the store, and the pass logs it as an
// error: a log it anchored went back to an earlier point. Each checkpoint
// made is also written to the server's log with its digest, for an operator
// who keeps the server's log elsewhere.
//
// Then retention, when audit.retention is not "0": whole checkpoints made
// before now minus the retention lose their entries (PruneAudit); every
// checkpoint row stays, and each pruning writes its own entry, made by the
// server (local user "server").
//
// The pass writes no audit entry of its own for a seal: an entry recording
// each seal would itself wait for the next pass, so a log would never be
// sealed to its end. The checkpoint row, signed, is the record of the seal.
//
// # Restarts
//
// Nothing of a pass is held in memory between its steps that the databases
// do not hold: a seal is all or nothing in the store, and a pass begins by
// reading the latest checkpoint. A pass stopped part way, by a stop, a
// crash or a database that stopped answering, leaves every log sealed up to
// some checkpoint and the rest unsealed, and the next pass, in this process
// or the next, goes on from there. A seal made from what was read before
// another seal (two servers on one database by mistake, or a retry) is
// Conflict in the store and keeps nothing; the pass reads the log again.
//
// # Verify and export
//
// Verify recomputes a range of a log's chain from its entries, checks each
// checkpoint's range, previous digest, head and signature with the public
// keys given (which must come from outside the database checked: derived
// again from the key file, or kept from an earlier export), checks pruned
// checkpoints' signatures and order, and checks the anchor the other log
// keeps of this one and the values kept outside the installation that the
// caller gives. Export writes what an auditor needs to check a range
// without the server: a header with the public keys and the chain value
// before the range, then each entry with its seq, its encoded form and the
// chain after it, each checkpoint after the last entry it covers.
// scripts/check/auditchain.py checks such an export with Python's hashlib
// and openssl alone.
package auditseal
