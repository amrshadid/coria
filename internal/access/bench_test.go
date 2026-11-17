package access_test

import (
	"context"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/secret"
)

// The benchmarks of the check on the SQLite master, with the last-use worker
// and the audit writer running as a server runs them: the cost every later
// API call pays before its handler runs (acceptance 7). They are reported,
// not claimed against a target yet. RolesFor is the test adapter over
// GrantsByKey until the port has its own statement.

// benchEnv returns an env with a key that holds one grant, writer on lab,
// marked used a moment before, as a busy key is.
func benchEnv(b *testing.B) (*env, testKey, access.Requirement) {
	e := newEnv(b)
	k := e.key(time.Time{}, on(master.TenantScope(e.lab.ID), master.RoleWriter))
	if _, err := e.master.MarkKeyUsed(e.ctx, k.id, t0.Add(-time.Second)); err != nil {
		b.Fatal(err)
	}
	return e, k, access.OnTenant("lab", master.RoleReader)
}

// BenchmarkCheck measures the check of a good key with one grant: parse,
// read the key, compare, read the tenant, read the roles. The key was marked
// within the minute, so no mark is asked for.
func BenchmarkCheck(b *testing.B) {
	e, k, need := benchEnv(b)
	cred := access.APIKey(k.full)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := e.access.Check(e.ctx, cred, need, facts("bench")); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCheckAndRecord measures an allowed read in all: the check and its
// audit entry handed to the audit writer, which writes it in a batch.
func BenchmarkCheckAndRecord(b *testing.B) {
	e, k, need := benchEnv(b)
	cred := access.APIKey(k.full)
	b.ReportAllocs()
	for b.Loop() {
		d, err := e.access.Check(e.ctx, cred, need, facts("bench"))
		if err != nil {
			b.Fatal(err)
		}
		e.access.Record(d, master.OutcomeDone, "")
	}
	b.StopTimer()
	e.flush()
	s := e.access.AuditStats()
	b.ReportMetric(float64(s.Dropped)/float64(b.N), "dropped/op")
	b.ReportMetric(float64(s.Written)/float64(b.N), "written/op")
}

// BenchmarkCheckAndSyncedAudit measures the same read with its entry
// appended on the request's path, one synced commit each: what the audit
// writer saves a read.
func BenchmarkCheckAndSyncedAudit(b *testing.B) {
	e, k, need := benchEnv(b)
	cred := access.APIKey(k.full)
	b.ReportAllocs()
	for b.Loop() {
		d, err := e.access.Check(e.ctx, cred, need, facts("bench"))
		if err != nil {
			b.Fatal(err)
		}
		if err := e.master.AppendAudit(e.ctx, d.Entry(master.OutcomeDone, "")); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCheckRefused measures the refusal of a key with a known id and a
// wrong secret, its entry going to the audit writer: the cost of a flood of
// bad keys to the server.
func BenchmarkCheckRefused(b *testing.B) {
	e, k, need := benchEnv(b)
	other, err := secret.MakeAPIKey()
	if err != nil {
		b.Fatal(err)
	}
	cred := access.APIKey("mgt_" + k.id + "_" + other.Key()[len("mgt_")+13:])
	b.ReportAllocs()
	for b.Loop() {
		if _, err := e.access.Check(e.ctx, cred, need, facts("bench")); err == nil {
			b.Fatal("a wrong secret was allowed")
		}
	}
	b.StopTimer()
	e.flush()
	s := e.access.AuditStats()
	b.ReportMetric(float64(s.Dropped)/float64(b.N), "dropped/op")
	b.ReportMetric(float64(s.Written)/float64(b.N), "written/op")
}

// BenchmarkCheckWithMark measures the check with its last-use mark written
// in line, the clock moved past the minute each time so every mark writes:
// what a request would cost if the mark were on its path, and the worst
// case of the last-use worker's load.
func BenchmarkCheckWithMark(b *testing.B) {
	e, k, need := benchEnv(b)
	cred := access.APIKey(k.full)
	ctx := context.Background()
	at := t0
	b.ReportAllocs()
	for b.Loop() {
		at = at.Add(61 * time.Second)
		e.clock.Set(at)
		d, err := e.access.Check(ctx, cred, need, facts("bench"))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := e.master.MarkKeyUsed(ctx, d.Principal().KeyID(), at); err != nil {
			b.Fatal(err)
		}
	}
}
