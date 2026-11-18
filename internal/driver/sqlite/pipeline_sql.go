package sqlite

// The pipeline's lookups that a test shows SQLite answers from an index
// (pipeline_plan_test.go), named so the test reads the very text the driver
// runs. Each names the index it reads.
const (
	// storageUsedSQL asks whether a reception names a storage:
	// receptions_by_storage.
	storageUsedSQL = `SELECT 1 FROM receptions WHERE storage = ? LIMIT 1`
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
)
