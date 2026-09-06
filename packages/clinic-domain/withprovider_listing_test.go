package clinicdomain_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// capturedScriptReads is a per-test ScriptReadObserver that keeps the LAST
// ScriptReadRecord seen for each request id — a retried operation re-enters
// step 4/5 on the same request id, so the last record is the one for the
// execution that actually committed — and wakes any waiter through a channel
// rather than a sleep, since the pipeline drives an execution synchronously on
// the test goroutine and the record can already be there by the time the
// waiter looks.
type capturedScriptReads struct {
	mu      sync.Mutex
	records map[string]processor.ScriptReadRecord
	notify  chan struct{}
}

func newCapturedScriptReads() *capturedScriptReads {
	return &capturedScriptReads{
		records: make(map[string]processor.ScriptReadRecord),
		notify:  make(chan struct{}, 1),
	}
}

func (c *capturedScriptReads) ObserveScriptReads(_ context.Context, env *processor.OperationEnvelope, record processor.ScriptReadRecord) {
	if env.OperationType != "SetAppointmentStatus" {
		return
	}
	c.mu.Lock()
	c.records[env.RequestID] = record
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// waitFor blocks until a record for requestID has been observed, polling on
// the notify channel rather than a fixed sleep, bounded by a ceiling that
// only trips on a real defect.
func (c *capturedScriptReads) waitFor(t *testing.T, requestID string) processor.ScriptReadRecord {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		rec, ok := c.records[requestID]
		c.mu.Unlock()
		if ok {
			return rec
		}
		select {
		case <-c.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for a ScriptReadRecord for requestId %s", requestID)
		}
	}
}

// TestSetAppointmentStatus_WithProviderListedOnce drives one confined
// front-desk SetAppointmentStatus — the same topology as
// TestWorkplace_TombstonedProviderConfersNothing's positive sibling: one
// site, one level, a staffer confined by the workplace walk rather than an
// operator grant — and inspects the execution's ScriptReadRecord.
//
// It asserts the confinement walk issues exactly three listings: the
// operator-role holdsRole walk (actor_holds_operator), the withProvider walk
// (appointment_provider, resolved once by the caller and handed to
// appointment_sites), and the practicesAt walk (sites_for_provider, fed that
// same provider). The observable is ScriptReadRecord.ListCalls, the per-call
// counter: Enumerations is a de-duplicated SET keyed by (hub, relation,
// direction), so a doubled withProvider listing collapses to the same single
// entry a lone listing produces and the set reads 3 either way — only the
// counter can tell one listing from two.
func TestSetAppointmentStatus_WithProviderListedOnce(t *testing.T) {
	ctx, conn := setupClinicEnv(t)
	seedProviderTombstoneTopology(t, ctx, conn)

	reads := newCapturedScriptReads()
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:                  "wplisted",
		Instance:                 "cl-wplisted",
		ExtraScriptReadObservers: []processor.ScriptReadObserver{reads},
	})

	const label = "wplistedreq00000001"
	got, why := submitSetStatusAs(t, ctx, conn, cp, cons, label, "confirmed", ptActorKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("confined front-desk SetAppointmentStatus with a LIVE provider at its own building = %v (%s), want Accepted", got, why)
	}

	rec := reads.waitFor(t, testutil.GenReqID(label))
	if len(rec.Enumerations) != 3 {
		t.Fatalf("SetAppointmentStatus enumerations = %v (len %d), want exactly 3: the operator-role "+
			"holdsRole walk, the withProvider walk, and the practicesAt walk", rec.Enumerations, len(rec.Enumerations))
	}
	if rec.ListCalls != 3 {
		t.Fatalf("SetAppointmentStatus ListCalls = %d over enumerations %v, want 3: appointment_sites must "+
			"reuse the provider its caller resolved, never list withProvider a second time", rec.ListCalls, rec.Enumerations)
	}
}
