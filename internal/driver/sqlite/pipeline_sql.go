package sqlite

// The pipeline's lookups that a test shows SQLite answers from an index
// (pipeline_plan_test.go), named so the test reads the very text the driver
// runs. Each names the index it reads.
const (
	// storageUsedSQL asks whether a reception in progress names a storage:
	// receptions_by_storage.
	storageUsedSQL = `SELECT 1 FROM receptions WHERE storage = ? AND ended = 0 LIMIT 1`
	// receiverOfStorageSQL names a receiver that archives to a storage:
	// receivers_by_storage.
	receiverOfStorageSQL = `SELECT name FROM receivers WHERE storage = ? ORDER BY name LIMIT 1`
	// receiversOnPortSQL reads the other receivers on a port:
	// receivers_by_port.
	receiversOnPortSQL = `SELECT ` + receiverColumns + ` FROM receivers WHERE port = ? AND name <> ?`
	// receptionNamesReceiverSQL and quarantineNamesReceiverSQL ask whether
	// a record still names a receiver: receptions_by_receiver and
	// quarantine_by_receiver.
	receptionNamesReceiverSQL  = `SELECT 1 FROM receptions WHERE receiver = ? LIMIT 1`
	quarantineNamesReceiverSQL = `SELECT 1 FROM quarantine WHERE receiver = ? LIMIT 1`
	// listStoragesSQL and listReceiversSQL page by name: each table's
	// primary key.
	listStoragesSQL  = `SELECT ` + storageColumns + ` FROM storages WHERE name > ? ORDER BY name LIMIT ?`
	listReceiversSQL = `SELECT ` + receiverColumns + ` FROM receivers WHERE name > ? ORDER BY name LIMIT ?`
	// openReceptionSQL finds the reception of a study receiving at a
	// receiver; the literal state matches receptions_open, the partial
	// unique index that holds one per receiver and study even across
	// processes.
	openReceptionSQL = `SELECT ` + receptionColumns + ` FROM receptions
		WHERE receiver = ? AND study_uid = ? AND state = 'receiving'`
	// nextArrivalSQL reads a study's next arrival number: receptions_arrival.
	nextArrivalSQL = `SELECT coalesce(max(arrival), 0) + 1 FROM receptions WHERE study_uid = ?`
	// arrivalsSQL reads a study's latest arrivals: receptions_arrival,
	// backward.
	arrivalsSQL = `SELECT id, arrival, state, started FROM receptions WHERE study_uid = ? ORDER BY arrival DESC LIMIT ?`
	// listReceivedSQL pages a reception's received files: the primary key.
	listReceivedSQL = `SELECT ` + receivedColumns + ` FROM received_files WHERE reception_id = ? AND (sop_instance_uid, sha256) > (?, ?)
		ORDER BY sop_instance_uid, sha256 LIMIT ?`
	// workByKeySQL finds the unit queued or leased under a unique key; the
	// literal state list matches work_by_key's.
	workByKeySQL = `SELECT ` + workColumns + ` FROM work WHERE unique_key = ? AND state IN ('queued', 'leased')`
	// claimNextSQL reads the earliest due units of a kind among those
	// queued or leased; the literal state list matches work_claimable's,
	// which is what lets SQLite's planner use that partial index, and
	// finished units, however many, are not in it.
	claimNextSQL = `SELECT ` + workColumns + ` FROM work
		WHERE kind = ? AND state IN ('queued', 'leased') AND due <= ? ORDER BY due, id LIMIT ?`
	// sessionLabelsSQL reads a study's session's labels and storage: the
	// sessions' unique index on study_uid, and the subjects' primary key.
	sessionLabelsSQL = `SELECT sub.label, s.label, s.storage FROM sessions AS s
		JOIN subjects AS sub ON sub.id = s.subject_id WHERE s.study_uid = ?`
	// scanLabelsSQL reads the labels of the scans of a list of series: the
	// scans' unique index on series_uid.
	scanLabelsSQL = `SELECT series_uid, label FROM scans WHERE series_uid IN (SELECT value FROM json_each(?))`
	// receptionScansSQL reads the scans a reception confirmed instances in:
	// instances_held_by, and the scans' primary key.
	receptionScansSQL = `SELECT s.label, s.series_uid, count(*), sum(i.size) FROM instances AS i
		JOIN scans AS s ON s.id = i.scan_id WHERE i.held_by = ? AND i.held_sha256 IS NOT NULL
		GROUP BY s.id ORDER BY s.label LIMIT ?`
	// storageHoldsSQL asks whether a storage holds an instance:
	// instances_on_storage.
	storageHoldsSQL = `SELECT 1 FROM instances WHERE storage = ? AND held_sha256 IS NOT NULL LIMIT 1`
	// releaseClaimedSQL and releaseOverwriteSQL release a reception's
	// claims: rows only claimed go, and a claim to overwrite a held
	// instance is cleared. instances_claimed.
	releaseClaimedSQL   = `DELETE FROM instances WHERE claimed_by = ? AND held_sha256 IS NULL`
	releaseOverwriteSQL = `UPDATE instances SET claim_sha256 = NULL, claimed_by = NULL, claimed_at = NULL WHERE claimed_by = ?`
)
