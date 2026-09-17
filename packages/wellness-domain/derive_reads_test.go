// derive_reads bare-submitter vectors (Contract #2 §2.5 class (g)) for the
// sessionDDL's CreateSession, CreateSessionSeries, ExtendSessionSeries,
// TombstoneSessionSeries and ReassignSessionSeries — each envelope below
// declares no Reads and no OptionalReads, proving the script's own
// derive_reads(op) is what hydrates the roots (and the per-cell
// studioSlotClaim aspects, and a rolling series' .horizon) rather than a
// caller's own declaration. The desk-side ops go through
// actor_holds_operator's own holdsRole walk unconditionally
// (workplace_exempt short-circuit), so they declare that one Enumeration —
// Contract #2's orthogonal class (e) channel, always caller-declared and
// never something derive_reads returns — and the series verbs their partOf
// walk beside it, the way every dispatcher does; the Weaver-only
// ExtendSessionSeries declares its playbook's two walks. The studio's own
// locatedAt walk (studio_locations, always run to snapshot atLocation) is
// pre-existing debt already carried in
// internal/testutil/read_drift_baseline.txt ("walk
// CreateSession/CreateSessionSeries vtx.studio.<id> locatedAt out"), so it
// needs no declaration on those two.
package wellnessdomain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
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

// TestExtendSessionSeries_UndeclaredSubmitter_MintsOccurrence: an
// ExtendSessionSeries declaring only the two walks its playbook names (the
// series' atStudio link and the studio's locatedAt link) mints the next
// occurrence and advances the horizon. derive_reads' own optionalReads
// carries the series root, its .definition and .horizon, the studio root and
// the per-cell probes, so vertex_alive, the horizon pin and cell_live all
// answer off hydrated state — and the bare .horizon update is conditioned on
// the hydrated revision rather than landing unconditioned.
func TestExtendSessionSeries_UndeclaredSubmitter_MintsOccurrence(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "extendseriesnodecl")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdndclstudio0000003", "Flow Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdndclrolling0000001", studioKey, "", true)

	reqID := testutil.GenReqID("wdndclextend00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ExtendSessionSeries",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-07-08T09:00:30Z",
		Class:         "sessionseries",
		Payload:       json.RawMessage(`{"seriesKey":"` + seriesKey + `","studio":"` + studioKey + `","startsAt":"` + rsNextStartsAt + `","endsAt":"` + rsNextEndsAt + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: seriesKey, Relation: "atStudio", Direction: "out"},
				{Hub: studioKey, Relation: "locatedAt", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	sessKey := "vtx.session." + nanoIDFromRequestID(reqID)
	if doc := readDoc(t, ctx, conn, sessKey); doc["class"] != "session" {
		t.Fatalf("occurrence class = %v, want session — the derivation must hydrate the series and studio roots for the mint to run at all", doc["class"])
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsAfterNext, rsAfterNextEnds, "2026-07-15T09:00:00Z", "", rsCount+1)
}

// TestTombstoneSessionSeries_UndeclaredSubmitter_StopsRolling: a
// TombstoneSessionSeries declaring only its two walks (the operator-role
// probe and the partOf enumeration) cancels the still-upcoming occurrences
// AND stops the roll. derive_reads' own optionalReads carries the series
// root, its .horizon, the studio confirmation link and the studio root, so a
// client that never learned to declare the horizon still hydrates it — the
// alternative is a call-off that cancels the classes and leaves the horizon
// minting the next window.
func TestTombstoneSessionSeries_UndeclaredSubmitter_StopsRolling(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "tombseriesnodecl")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdndclstudio0000004", "Flow Room")
	seriesKey, sessionKeys := createSeries(t, ctx, conn, cp, cons, "wdndclrolling0000002", studioKey, "", true)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdndclstop0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSessionSeries",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-09T12:00:00Z",
		Class:         "sessionseries",
		Payload:       json.RawMessage(`{"seriesKey":"` + seriesKey + `","studio":"` + studioKey + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: seriesKey, Relation: "partOf", Direction: "in"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	for _, sk := range sessionKeys[1:] {
		if keyExists(t, ctx, conn, sk) {
			t.Fatalf("%s must be cancelled — the derivation must hydrate the series root for the walk to run at all", sk)
		}
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, "", "", rsCount)
	if got, _ := horizonData(t, ctx, conn, seriesKey)["stoppedAt"].(string); got != "2026-07-09T12:00:00Z" {
		t.Fatalf(".horizon.stoppedAt = %q, want the call-off's submittedAt — the derivation must hydrate the horizon for the stop to be written", got)
	}
}

// TestReassignSessionSeries_UndeclaredSubmitter_ShiftsHorizon: a
// ReassignSessionSeries declaring only its two walks moves the run AND
// shifts its horizon by the same delta. derive_reads' own optionalReads
// carries the series root, its .horizon, the studio confirmation link, the
// studio root and the anchor pin (the anchor and its .schedule), so the
// horizon a bare submitter never named still moves with the classes.
func TestReassignSessionSeries_UndeclaredSubmitter_ShiftsHorizon(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "moveseriesnodecl")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdndclstudio0000005", "Flow Room")
	seriesKey, sessionKeys := createSeries(t, ctx, conn, cp, cons, "wdndclrolling0000003", studioKey, "", true)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdndclmove0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ReassignSessionSeries",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "sessionseries",
		Payload: json.RawMessage(`{"seriesKey":"` + seriesKey + `","studio":"` + studioKey +
			`","anchorKey":"` + sessionKeys[0] + `","anchorStartsAt":"` + rsFirstStartsAt +
			`","startsAt":"2026-07-10T10:00:00Z","endsAt":"2026-07-10T11:15:00Z"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: seriesKey, Relation: "partOf", Direction: "in"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		msg := ""
		if reply != nil && reply.Error != nil {
			msg = reply.Error.Message
		}
		t.Fatalf("outcome = %v, want Accepted (%s)", outcome, msg)
	}
	starts, _, _, _ := sessionSchedule(t, ctx, conn, sessionKeys[2])
	if !strings.HasPrefix(starts, "2026-07-24T10:00:00Z") {
		t.Fatalf("occurrence 3 startsAt = %s, want 2026-07-24T10:00:00Z (+2 days 1 hour)", starts)
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), "2026-07-31T10:00:00Z", "2026-07-31T11:15:00Z", "2026-07-10T10:00:00Z", "", rsCount)
}

// TestStopSessionSeries_UndeclaredSubmitter_StopsRolling: a StopSessionSeries
// declaring only the operator-role walk stops the roll. derive_reads' own
// optionalReads carries the series root, its .horizon, the studio
// confirmation link and the studio root, so the one series verb that never
// walks reaches every key it reads off hydrated state, and its bare .horizon
// update is conditioned on the hydrated revision.
func TestStopSessionSeries_UndeclaredSubmitter_StopsRolling(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "stopseriesnodecl")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdndclstudio0000006", "Flow Room")
	seriesKey, sessionKeys := createSeries(t, ctx, conn, cp, cons, "wdndclrolling0000004", studioKey, "", true)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdndclstopop00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "StopSessionSeries",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-09T12:00:00Z",
		Class:         "sessionseries",
		Payload:       json.RawMessage(`{"seriesKey":"` + seriesKey + `","studio":"` + studioKey + `"}`),
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
	for i, sk := range sessionKeys {
		if !keyExists(t, ctx, conn, sk) {
			t.Fatalf("occurrence %d must survive the stop", i)
		}
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, "", "", rsCount)
	if got, _ := horizonData(t, ctx, conn, seriesKey)["stoppedAt"].(string); got != "2026-07-09T12:00:00Z" {
		t.Fatalf(".horizon.stoppedAt = %q, want the stop's submittedAt — the derivation must hydrate the horizon for the stop to be written", got)
	}
}
