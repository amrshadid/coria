package sqlkit

import (
	"time"

	"github.com/amrshadid/coria/internal/tenant"
)

// The statements of jobs' work that read tables which grow, named so the
// plan tests of both drivers read the very text that runs (Plans). Each is a
// range or a lookup of the index named beside it.
const (
	// jobScanByWorkSQL reads the scan a unit sends: job_scans_by_work,
	// then the scan's key.
	jobScanByWorkSQL = `SELECT ` + jobScanColumns + ` FROM job_scans AS js JOIN scans AS s ON s.id = js.scan_id WHERE js.work_id = ?`
	// forwardScansSQL lists a reception's scans after one: reception_scans'
	// key.
	forwardScansSQL = `SELECT scan_id, instances, bytes FROM reception_scans WHERE reception_id = ? AND scan_id > ?
		ORDER BY scan_id LIMIT ?`
	// targetScansColumns are what a listing reads of a scan of the catalog.
	targetScansColumns = `SELECT id, instances, bytes, session_folded, folded, updated FROM scans WHERE `
	// targetScanSQL reads one scan by its labels: scans_by_label.
	targetScanSQL = targetScansColumns + `subject_folded = ? AND session_folded = ? AND folded = ? AND subject_label = ?
		AND session_label = ? AND label = ?`
	// sessionScansSQL lists a session's scans after one: a range of
	// scans_by_label.
	sessionScansSQL = targetScansColumns + `subject_folded = ? AND session_folded = ? AND subject_label = ? AND session_label = ?
		AND folded > ? ORDER BY folded LIMIT ?`
	// subjectScansSQL lists a subject's scans after one: a range of
	// scans_by_label.
	subjectScansSQL = targetScansColumns + `subject_folded = ? AND subject_label = ? AND (session_folded, folded) > (?, ?)
		ORDER BY session_folded, folded LIMIT ?`
	// heldByCountSQL counts a scan's instances held by a time: a range of
	// the instances' key.
	heldByCountSQL = `SELECT count(*), CAST(coalesce(sum(size), 0) AS BIGINT) FROM instances WHERE scan_id = ? AND held_at <= ?`
	// retriedScansSQL lists the ended job's scans a retry reads, after
	// one: job_scans' key.
	retriedScansSQL = `SELECT ` + jobScanColumns + ` FROM job_scans AS js JOIN scans AS s ON s.id = js.scan_id
		WHERE js.job_id = ? AND js.scan_id > ? ORDER BY js.scan_id LIMIT ?`
	// streamCountSQL and streamPageSQL count and read a scan's stream,
	// before its condition (SendingDialect.stream): the instances' key.
	streamCountSQL = `SELECT count(*), CAST(coalesce(sum(i.size), 0) AS BIGINT) FROM instances AS i WHERE `
	streamPageSQL  = `SELECT i.sop_instance_uid, i.reception_id, i.storage_key, i.byte_offset, i.byte_length, i.size,
		i.sha256, coalesce(i.archived_sha256, i.sha256), i.held_at FROM instances AS i WHERE `
	// scanRefusedSQL reads the instances an ended job's scan refused, by
	// the scan's key: job_failures_by_scan.
	scanRefusedSQL = `SELECT scan_id, sop_instance_uid FROM job_failures WHERE job_id = ? AND scan_id = ? AND warning = ?`
	// owedCountSQL counts the instances a scan's unit owes: job_owed's key.
	owedCountSQL = `SELECT count(*) FROM job_owed WHERE job_id = ? AND scan_id = ?`
	// owedInstancesSQL reads the instances a scan's unit owes, as a page of
	// its stream: job_owed's key, then the instances' key.
	owedInstancesSQL = streamPageSQL + `i.sop_instance_uid IN (SELECT sop_instance_uid FROM job_owed WHERE job_id = ?
		AND scan_id = ?) AND i.scan_id = ? ORDER BY i.sop_instance_uid LIMIT ?`
	// receptionArchivedSQL asks whether a reception archived an instance:
	// reception_scans' key.
	receptionArchivedSQL = `SELECT 1 FROM reception_scans WHERE reception_id = ? AND instances > 0 LIMIT 1`
	// queuedUnitsSQL reads a job's units that are queued: the work's key,
	// for its listing and each scan of job_scans' key.
	queuedUnitsSQL = `SELECT id FROM work WHERE state = 'queued' AND (id = ? OR id IN (SELECT work_id FROM job_scans
		WHERE job_id = ?))`
)

// SenderJobsInProgressSQL reads a sender's jobs not ended:
// jobs_of_sender_in_progress.
func (d SendingDialect) SenderJobsInProgressSQL() string {
	return `SELECT id FROM jobs WHERE sender = ? AND ` + d.NotEnded + ` ORDER BY created, id`
}

// SendingPlan is one statement of senders' and jobs' work with arguments a
// plan test runs it with, and the index it must be answered from.
type SendingPlan struct {
	// Name names the read.
	Name string
	// Index is the index the plan must use, or a part of its name; empty
	// for the table's primary key, whose name each database gives its own
	// way.
	Index string
	// SQL is the statement, with "?" for each parameter.
	SQL string
	// Args are its parameters' values.
	Args []any
}

// Plans returns every statement of senders' and jobs' work that reads a
// table that grows, with arguments a plan test fills the tables to match:
// jobs "j<n>" made at n seconds, few of them of sender "s1" or failed;
// job_scans of job "j1" keyed by scan; failures of job "j1"; owed
// instances of job "j1".
func (d SendingDialect) Plans() []SendingPlan {
	at := time.Unix(5000, 0).UTC()
	after := &TimePosition{At: at, ID: "j5000"}
	jobs := func(f tenant.JobFilter) (string, []any) { return d.JobsQuery(f, after, 101) }
	q := func(name, index string, sql string, args []any) SendingPlan {
		return SendingPlan{Name: name, Index: index, SQL: sql, Args: args}
	}
	var out []SendingPlan
	add := func(name, index string, f tenant.JobFilter) {
		sql, args := jobs(f)
		out = append(out, q(name, index, sql, args))
	}
	add("ListJobs", "jobs_by_created", tenant.JobFilter{})
	add("ListJobs of a state", "jobs_by_state", tenant.JobFilter{States: []tenant.JobState{tenant.JobFailed}})
	add("ListJobs of states", "jobs_by_state", tenant.JobFilter{States: []tenant.JobState{tenant.JobFailed, tenant.JobQueued}})
	add("ListJobs of a sender", "jobs_by_sender", tenant.JobFilter{Sender: "s1"})
	add("ListJobs in progress", "jobs_in_progress", tenant.JobFilter{InProgress: true})
	fsql, fargs := d.FailuresQuery("j1", false, []string{"sub-1", "ses-1", "scan-1", "2.25.3.5"}, 101)
	// The refused alone: job_failures_by_kind, or the key with the warnings
	// filtered out where most of a job's failures are refusals; both are a
	// range of the one job's.
	out = append(out, q("ListJobFailures refused", "job_failures_", fsql, fargs))
	fsql, fargs = d.FailuresQuery("j1", true, []string{"sub-1", "ses-1", "scan-1", "2.25.3.5"}, 101)
	out = append(out, q("ListJobFailures", "", fsql, fargs))
	j := tenant.Job{ID: "j1"}
	ssql, sargs := d.StreamPage(j, at, 77, "", "2.25.3.1", 64)
	out = append(out, q("a scan's stream", "", ssql, sargs))
	return append(out,
		q("the scan a unit sends", "job_scans_by_work", jobScanByWorkSQL, []any{"u77"}),
		q("a forward's scans", "", forwardScansSQL, []any{"rc5", int64(0), tenant.JobListPage}),
		q("a session's scans", "scans_by_label", sessionScansSQL, []any{"sub-1", "ses-1", "sub-1", "ses-1", "", tenant.JobListPage}),
		q("a subject's scans", "scans_by_label", subjectScansSQL, []any{"sub-1", "sub-1", "", "", tenant.JobListPage}),
		q("a scan's instances by a time", "", heldByCountSQL, []any{int64(77), d.Time(at)}),
		q("a retry's scans", "", retriedScansSQL, []any{"j1", int64(0), tenant.JobListPage}),
		q("a job's queued units", "", queuedUnitsSQL, []any{"u1", "j1"}),
		q("a retried scan's refused", "job_failures_by_scan", scanRefusedSQL, []any{"j1", int64(7), d.Bool(false)}),
		q("a unit's owed count", "", owedCountSQL, []any{"j1", int64(7)}),
		q("a unit's owed instances", "", owedInstancesSQL, []any{"j1", int64(7), int64(7), tenant.MaxJobOwed}),
		q("whether a reception archived", "", receptionArchivedSQL, []any{"rc5"}),
		q("a sender's jobs in progress", "jobs_of_sender_in_progress", d.SenderJobsInProgressSQL(), []any{"s1"}),
		// Either partial index of the jobs not ended bounds the count.
		q("the jobs in progress by state", "_in_progress", d.JobsInProgressSQL(), nil),
	)
}
