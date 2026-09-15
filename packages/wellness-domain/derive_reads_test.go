// derive_reads bare-submitter vectors (Contract #2 §2.5 class (g)) for the
// sessionDDL's CreateSession and CreateSessionSeries — each envelope below
// declares no Reads and no OptionalReads, proving the script's own
// derive_reads(op) is what hydrates the studio root (and the per-cell
// studioSlotClaim aspects) rather than a caller's own declaration. Both ops
// go through actor_holds_operator's own holdsRole walk unconditionally
// (workplace_exempt short-circuit), so both declare that one Enumeration —
// Contract #2's orthogonal class (e) channel, always caller-declared and
// never something derive_reads returns. The studio's own locatedAt walk
// (studio_locations, always run to snapshot atLocation) is pre-existing debt
// already carried in internal/testutil/read_drift_baseline.txt ("walk
// CreateSession/CreateSessionSeries vtx.studio.<id> locatedAt out"), so it
// needs no declaration here either.
package wellnessdomain_test

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestCreateSession_UndeclaredSubmitter_SchedulesClass: a CreateSession
// declaring only the mandatory operator-role enumeration schedules the class
// AND leads it with an instructor. derive_reads' own optionalReads carries
// both the studio root and the instructor root, so require_live_typed sees
// each live endpoint rather than misreading an undeclared root as absent.
func TestCreateSession_UndeclaredSubmitter_SchedulesClass(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "createsessionnodecl")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdndclstudio0000001", "Flow Room")
	instructorKey := "vtx.instructor.WDNDCLNSTRCHJKMNPQR1"
	seedVertex(t, ctx, conn, instructorKey, "instructor", map[string]any{})

	reqID := testutil.GenReqID("wdndclsession0000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "session",
		Payload:       json.RawMessage(`{"studio":"` + studioKey + `","instructor":"` + instructorKey + `","name":"Vinyasa Flow","startsAt":"2026-07-08T09:00:00Z","endsAt":"2026-07-08T09:30:00Z","capacity":10}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	sessKey := "vtx.session." + nanoIDFromRequestID(reqID)
	if doc := readDoc(t, ctx, conn, sessKey); doc["class"] != "session" {
		t.Fatalf("session class = %v, want session — the derivation must hydrate the studio root for the class to be scheduled at all", doc["class"])
	}
	_, sessID, _ := substrate.ParseVertexKey(sessKey)
	_, instructorID, _ := substrate.ParseVertexKey(instructorKey)
	ledByLnk := "lnk.session." + sessID + ".ledBy.instructor." + instructorID
	if !keyExists(t, ctx, conn, ledByLnk) {
		t.Fatalf("no ledBy link at %s — the derivation must hydrate the instructor root for the instructor slot-claim/link to run at all", ledByLnk)
	}
}

// TestCreateSessionSeries_UndeclaredSubmitter_SchedulesSeries: a
// CreateSessionSeries declaring only the mandatory operator-role enumeration
// schedules the whole series AND leads every occurrence with an instructor,
// exercising the same studio/instructor root derivation as CreateSession.
func TestCreateSessionSeries_UndeclaredSubmitter_SchedulesSeries(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "createseriesnodecl")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdndclstudio0000002", "Power Room")
	instructorKey := "vtx.instructor.WDNDCLNSTRCHJKMNPQR2"
	seedVertex(t, ctx, conn, instructorKey, "instructor", map[string]any{})

	reqID := testutil.GenReqID("wdndclseries00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSessionSeries",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "sessionseries",
		Payload:       json.RawMessage(`{"studio":"` + studioKey + `","instructor":"` + instructorKey + `","name":"Power Flow","startsAt":"2026-07-08T09:00:00Z","endsAt":"2026-07-08T09:30:00Z","capacity":10,"intervalDays":7,"occurrenceCount":3}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	seriesKey := "vtx.sessionseries." + nanoIDFromRequestID(reqID)
	if doc := readDoc(t, ctx, conn, seriesKey); doc["class"] != "sessionseries" {
		t.Fatalf("sessionseries class = %v, want sessionseries — the derivation must hydrate the studio root for the series to be scheduled at all", doc["class"])
	}
}
