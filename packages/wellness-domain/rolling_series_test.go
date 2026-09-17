// wellness-domain integration tests for the rolling series — a series the
// desk marks `rolling` keeps occurrenceCount classes on the books: the
// window moves as each class starts. CreateSessionSeries records the
// .horizon, ExtendSessionSeries (Weaver's dispatch actor only) mints one
// occurrence on the cadence and advances it, TombstoneSessionSeries stops
// the roll and ReassignSessionSeries shifts it. Same harness as
// promote_waitlist_test.go: real install + Processor pipeline, hand-built
// envelopes carrying exactly the contextHint the wellnessSeriesHorizon
// target declares (targets.go).
package wellnessdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// The run every vector below schedules: three weekly Wednesday classes from
// Jul 8 at 09:00–09:30, so the batch's last is Jul 22 and the horizon's next
// occurrence is Jul 29; the window moves at the Jul 8 class's own start.
const (
	rsFirstStartsAt = "2026-07-08T09:00:00Z"
	rsFirstEndsAt   = "2026-07-08T09:30:00Z"
	rsNextStartsAt  = "2026-07-29T09:00:00Z"
	rsNextEndsAt    = "2026-07-29T09:30:00Z"
	rsAfterNext     = "2026-08-05T09:00:00Z"
	rsAfterNextEnds = "2026-08-05T09:30:00Z"
	rsCount         = 3
)

// createSeries mints rsCount weekly occurrences at studioKey, led by
// instructorKey when non-empty, rolling when asked, and returns the series
// key plus its occurrence keys in cadence order — createSessionSeriesLed's
// shape with the rolling flag.
func createSeries(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, studioKey, instructorKey string, rolling bool) (string, []string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payloadMap := map[string]any{
		"studio": studioKey, "name": "Evening Flow", "startsAt": rsFirstStartsAt, "endsAt": rsFirstEndsAt,
		"capacity": 20, "priceCents": 1500, "intervalDays": 7, "occurrenceCount": rsCount,
	}
	if rolling {
		payloadMap["rolling"] = true
	}
	reads := []string{studioKey}
	if instructorKey != "" {
		payloadMap["instructor"] = instructorKey
		reads = append(reads, instructorKey)
	}
	payload, _ := json.Marshal(payloadMap)
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSessionSeries",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "sessionseries",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Enumerations: testutil.DeclaredEnumerations("CreateSessionSeries", domainActorKey, wellnessdomain.OpMetas()),
			Reads:        reads,
		},
	})
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSessionSeries outcome = %v, reply = %+v, want Accepted", outcome, reply)
	}
	ids := nanoIDsFromRequestID(reqID, rsCount+1)
	sessionKeys := make([]string, rsCount)
	for i := range sessionKeys {
		sessionKeys[i] = "vtx.session." + ids[i+1]
	}
	return "vtx.sessionseries." + ids[0], sessionKeys
}

// extendEnv builds an ExtendSessionSeries envelope carrying exactly the
// contextHint the wellnessSeriesHorizon gaps declare (targets.go): the
// series, its .definition and .horizon, and the studio as required reads,
// and the gap's own enumerations — resolved from the gap spec, so a
// declaration deleted from targets.go reds these tests. The per-cell probes
// are deliberately undeclared: the script's own derive_reads supplies them.
func extendEnv(label, seriesKey, studioKey, startsAt, endsAt, instructorKey, actorKey, submittedAt string) *processor.OperationEnvelope {
	gap := "missing_occurrence"
	payloadMap := map[string]any{"seriesKey": seriesKey, "studio": studioKey, "startsAt": startsAt, "endsAt": endsAt}
	if instructorKey != "" {
		gap = "missing_led_occurrence"
		payloadMap["instructor"] = instructorKey
	}
	payload, _ := json.Marshal(payloadMap)
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ExtendSessionSeries",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "sessionseries",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads: []string{seriesKey, seriesKey + ".definition", seriesKey + ".horizon", studioKey},
			Enumerations: testutil.DeclaredGapEnumerations(
				wellnessdomain.SeriesHorizonTarget, gap, actorKey,
				map[string]any{"seriesKey": seriesKey, "studioKey": studioKey}, wellnessdomain.WeaverTargets()),
		},
	}
}

// extendSeries submits extendEnv as Weaver and returns the outcome, the
// reply, and the script's own failure text (the refusals here are
// indistinguishable at the outcome level).
func extendSeries(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope) (processor.MessageOutcome, *processor.OperationReply, string) {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	failure := ""
	if reply != nil && reply.Error != nil {
		if i := strings.Index(reply.Error.Message, "fail: "); i >= 0 {
			failure = reply.Error.Message[i+len("fail: "):]
		}
	}
	return outcome, reply, failure
}

// horizonData reads the series' live .horizon data bag, or nil when the
// aspect was never written.
func horizonData(t *testing.T, ctx context.Context, conn *substrate.Conn, seriesKey string) map[string]any {
	t.Helper()
	if !keyExists(t, ctx, conn, seriesKey+".horizon") {
		return nil
	}
	doc := readDoc(t, ctx, conn, seriesKey+".horizon")
	data, _ := doc["data"].(map[string]any)
	return data
}

// requireHorizon pins the whole .horizon shape: the next occurrence, the
// window's extendAt, the minted count, and the instructor's presence or
// absence — every field, because each is a fact a later writer carries or
// moves.
func requireHorizon(t *testing.T, h map[string]any, nextStartsAt, nextEndsAt, extendAt, instructor string, mintedCount int) {
	t.Helper()
	if h == nil {
		t.Fatalf(".horizon is missing")
	}
	if got, _ := h["nextStartsAt"].(string); got != nextStartsAt {
		t.Fatalf(".horizon.nextStartsAt = %q, want %q", got, nextStartsAt)
	}
	if got, _ := h["nextEndsAt"].(string); got != nextEndsAt {
		t.Fatalf(".horizon.nextEndsAt = %q, want %q", got, nextEndsAt)
	}
	got, has := h["extendAt"].(string)
	if extendAt == "" && has {
		t.Fatalf(".horizon.extendAt = %q, want absent (stopped)", got)
	}
	if extendAt != "" && got != extendAt {
		t.Fatalf(".horizon.extendAt = %q, want %q", got, extendAt)
	}
	gotInstr, hasInstr := h["instructor"].(string)
	if instructor == "" && hasInstr {
		t.Fatalf(".horizon.instructor = %q, want absent", gotInstr)
	}
	if instructor != "" && gotInstr != instructor {
		t.Fatalf(".horizon.instructor = %q, want %q", gotInstr, instructor)
	}
	if got, _ := h["mintedCount"].(float64); int(got) != mintedCount {
		t.Fatalf(".horizon.mintedCount = %v, want %d", h["mintedCount"], mintedCount)
	}
}

// findEmittedEvent reads the committed transactional-outbox aspect for an
// op's requestId and returns the payload of the first event of the given
// class — the lease-signing precedent for asserting an event's DATA, which
// the tracker's eventClasses list cannot carry.
func findEmittedEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID, class string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(requestID))
	if err != nil {
		t.Fatalf("read outbox aspect for %s: %v", requestID, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect for %s: %v", requestID, err)
	}
	for _, e := range ob.Data.Events {
		if e.EventType == class {
			return e.Payload
		}
	}
	classes := make([]string, 0, len(ob.Data.Events))
	for _, e := range ob.Data.Events {
		classes = append(classes, e.EventType)
	}
	t.Fatalf("no %s event emitted by op %s (events: %v)", class, requestID, classes)
	return nil
}

// TestCreateSessionSeries_Rolling_MintsHorizon pins the .horizon a rolling
// create records: next* is the occurrence after the batch's last on the
// cadence, extendAt is the first occurrence's own start (the window's
// earliest class), mintedCount is the batch, and the instructor is carried
// when named — and that a series created without the flag carries no
// horizon at all, so the install changes nothing for the desk's existing
// runs.
func TestCreateSessionSeries_Rolling_MintsHorizon(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingcreate")

	instructorKey := mkSeriesInstructor(t, ctx, conn, cp, cons, "wdrollinstruct000001", "Sam")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollstudio00000001", "Flow Room")

	seriesKey, sessionKeys := createSeries(t, ctx, conn, cp, cons, "wdrollcreate00000001", studioKey, instructorKey, true)
	if len(sessionKeys) != rsCount {
		t.Fatalf("occurrences = %d, want %d", len(sessionKeys), rsCount)
	}
	for i, sk := range sessionKeys {
		if !keyExists(t, ctx, conn, sk) {
			t.Fatalf("occurrence %d %s must be minted by the eager batch", i, sk)
		}
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, rsFirstStartsAt, instructorKey, rsCount)

	// The same run on a second studio, created without the flag: no
	// horizon at all.
	studioTwo := createStudio(t, ctx, conn, cp, cons, "wdrollstudio00000002", "Power Room")
	plainKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollcreate00000002", studioTwo, "", false)
	if h := horizonData(t, ctx, conn, plainKey); h != nil {
		t.Fatalf("a series created without rolling must carry no .horizon, got %v", h)
	}
}

// TestCreateSessionSeries_Rolling_UnledHorizon pins the horizon of a rolling
// series with no instructor: the instructor field is absent, not null, so
// the lens's unled gap (instructorKey = null) owns the row.
func TestCreateSessionSeries_Rolling_UnledHorizon(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingunled")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollustudio0000001", "Flow Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollucreate0000001", studioKey, "", true)
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, rsFirstStartsAt, "", rsCount)
}

// TestExtendSessionSeries_MintsNextOccurrence is the row's whole point: the
// window moved (the first class started), Weaver dispatches the horizon's
// next occurrence, and exactly one class lands on the cadence with the
// series' own shape — schedule, atStudio, partOf, the studio's and the
// instructor's cells — while the horizon advances by one interval and
// re-records the instructor. The reply's primaryKey is the series, the key
// the op wrote its .horizon on.
func TestExtendSessionSeries_MintsNextOccurrence(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingextend")

	instructorKey := mkSeriesInstructor(t, ctx, conn, cp, cons, "wdrollxinstruct00001", "Sam")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollxstudio0000001", "Flow Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollxcreate0000001", studioKey, instructorKey, true)

	env := extendEnv("wdrollxextend0000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, instructorKey,
		bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")
	outcome, reply, why := extendSeries(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("ExtendSessionSeries outcome = %v (%s), want Accepted", outcome, why)
	}
	if reply.PrimaryKey != seriesKey {
		t.Fatalf("primaryKey = %q, want the series %q", reply.PrimaryKey, seriesKey)
	}

	sessKey := "vtx.session." + nanoIDFromRequestID(env.RequestID)
	if doc := readDoc(t, ctx, conn, sessKey); doc["class"] != "session" {
		t.Fatalf("minted occurrence class = %v, want session", doc["class"])
	}
	starts, ends, remind, sched := sessionSchedule(t, ctx, conn, sessKey)
	if starts != rsNextStartsAt || ends != rsNextEndsAt || remind != wdShifted(t, rsNextStartsAt, -24*time.Hour) {
		t.Fatalf("occurrence schedule = %s–%s (remind %s), want %s–%s", starts, ends, remind, rsNextStartsAt, rsNextEndsAt)
	}
	if got, _ := sched["name"].(string); got != "Evening Flow" {
		t.Fatalf("occurrence name = %q, want the series' definition name", got)
	}
	if got, _ := sched["capacity"].(float64); int(got) != 20 {
		t.Fatalf("occurrence capacity = %v, want 20 from the definition", sched["capacity"])
	}
	if got, _ := sched["priceCents"].(float64); int(got) != 1500 {
		t.Fatalf("occurrence priceCents = %v, want 1500 from the definition", sched["priceCents"])
	}
	if !keyExists(t, ctx, conn, atStudioLnkKey(t, sessKey, studioKey)) {
		t.Fatalf("the occurrence must link atStudio to the series' studio")
	}
	_, sessID, _ := substrate.ParseVertexKey(sessKey)
	_, seriesID, _ := substrate.ParseVertexKey(seriesKey)
	_, instructorID, _ := substrate.ParseVertexKey(instructorKey)
	if !keyExists(t, ctx, conn, "lnk.session."+sessID+".partOf.sessionseries."+seriesID) {
		t.Fatalf("the occurrence must link partOf the series")
	}
	if !keyExists(t, ctx, conn, "lnk.session."+sessID+".ledBy.instructor."+instructorID) {
		t.Fatalf("the occurrence must be led by the horizon's instructor")
	}
	assertCells(t, ctx, conn, studioKey, rsNextStartsAt, rsNextEndsAt, true, "the studio's cells for the minted occurrence")
	assertCells(t, ctx, conn, instructorKey, rsNextStartsAt, rsNextEndsAt, true, "the instructor's cells for the minted occurrence")

	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsAfterNext, rsAfterNextEnds, "2026-07-15T09:00:00Z", instructorKey, rsCount+1)

	ev := findEmittedEvent(t, ctx, conn, env.RequestID, "wellness.sessionSeriesExtended")
	if ev["sessionKey"] != sessKey {
		t.Fatalf("event sessionKey = %v, want %s", ev["sessionKey"], sessKey)
	}
	if _, skipped := ev["skipped"]; skipped {
		t.Fatalf("a minted extension must not report skipped: %v", ev)
	}
	if ev["seriesKey"] != seriesKey || ev["studio"] != studioKey || ev["startsAt"] != rsNextStartsAt {
		t.Fatalf("event = %v, want seriesKey/studio/startsAt of the extension", ev)
	}

	// The window moves again: the next dispatch names what the horizon now
	// records, and the run keeps its full count ahead.
	env2 := extendEnv("wdrollxextend0000002", seriesKey, studioKey, rsAfterNext, rsAfterNextEnds, instructorKey,
		bootstrap.WeaverIdentityKey, "2026-07-15T09:00:30Z")
	if outcome, _, why := extendSeries(t, ctx, conn, cp, cons, env2); outcome != processor.OutcomeAccepted {
		t.Fatalf("second ExtendSessionSeries outcome = %v (%s), want Accepted", outcome, why)
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), "2026-08-12T09:00:00Z", "2026-08-12T09:30:00Z", "2026-07-22T09:00:00Z", instructorKey, rsCount+2)
}

// TestExtendSessionSeries_SkipsPastOccurrence: the dispatch lands after the
// occurrence it would mint has already started (the stack was away past the
// deadline), so no class nobody could book is minted — but the horizon still
// moves, mintedCount unchanged, and the event names the skip.
func TestExtendSessionSeries_SkipsPastOccurrence(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingskippast")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollpstudio0000001", "Flow Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollpcreate0000001", studioKey, "", true)

	env := extendEnv("wdrollpextend0000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, "",
		bootstrap.WeaverIdentityKey, "2026-07-29T10:00:00Z")
	outcome, _, why := extendSeries(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("ExtendSessionSeries outcome = %v (%s), want Accepted", outcome, why)
	}
	if keyExists(t, ctx, conn, "vtx.session."+nanoIDFromRequestID(env.RequestID)) {
		t.Fatalf("a past occurrence must not be minted")
	}
	assertCells(t, ctx, conn, studioKey, rsNextStartsAt, rsNextEndsAt, false, "a skipped occurrence claims no cells")
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsAfterNext, rsAfterNextEnds, "2026-07-15T09:00:00Z", "", rsCount)

	ev := findEmittedEvent(t, ctx, conn, env.RequestID, "wellness.sessionSeriesExtended")
	if ev["skipped"] != "PastOccurrence" {
		t.Fatalf("event skipped = %v, want PastOccurrence", ev["skipped"])
	}
	if _, has := ev["sessionKey"]; has {
		t.Fatalf("a skipped extension must not name a sessionKey: %v", ev)
	}
}

// TestExtendSessionSeries_SkipsStudioConflict: the slot the window would
// mint onto was booked one-off since the horizon was recorded, so the run
// passes that week exactly as the desk would let it — no session, the
// one-off's cells untouched, the horizon moved, the skip named.
func TestExtendSessionSeries_SkipsStudioConflict(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingskipcell")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollcstudio0000001", "Flow Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollccreate0000001", studioKey, "", true)
	oneOffKey, oneOff := createSession(t, ctx, conn, cp, cons, "wdrollconeoff0000001", studioKey, "Drop-in Sculpt", "2026-07-29T09:15:00Z", "2026-07-29T09:45:00Z", 10)
	if oneOff != processor.OutcomeAccepted {
		t.Fatalf("one-off CreateSession outcome = %v, want Accepted", oneOff)
	}

	env := extendEnv("wdrollcextend0000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, "",
		bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")
	outcome, _, why := extendSeries(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("ExtendSessionSeries outcome = %v (%s), want Accepted", outcome, why)
	}
	if keyExists(t, ctx, conn, "vtx.session."+nanoIDFromRequestID(env.RequestID)) {
		t.Fatalf("a colliding occurrence must not be minted")
	}
	if !keyExists(t, ctx, conn, oneOffKey) {
		t.Fatalf("the one-off that holds the slot must survive")
	}
	assertCells(t, ctx, conn, studioKey, "2026-07-29T09:15:00Z", "2026-07-29T09:45:00Z", true, "the one-off's cells")
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsAfterNext, rsAfterNextEnds, "2026-07-15T09:00:00Z", "", rsCount)
	if ev := findEmittedEvent(t, ctx, conn, env.RequestID, "wellness.sessionSeriesExtended"); ev["skipped"] != "StudioConflict" {
		t.Fatalf("event skipped = %v, want StudioConflict", ev["skipped"])
	}
}

// TestExtendSessionSeries_Refusals pins every refusal against the positive
// vector above, each proven by what it leaves untouched: StaleHorizon (a row
// naming a class the horizon does not record next — one week early, and the
// instructor absent on a led run), WrongStudio (a studio that is not the
// series'), NotRolling (a series created without the flag), and AuthDenied
// for an operator-role holder that is not Weaver's dispatch actor, whose
// grant would otherwise admit it.
func TestExtendSessionSeries_Refusals(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingrefuse")

	instructorKey := mkSeriesInstructor(t, ctx, conn, cp, cons, "wdrollrinstruct00001", "Sam")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollrstudio0000001", "Flow Room")
	otherStudio := createStudio(t, ctx, conn, cp, cons, "wdrollrstudio0000002", "Power Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollrcreate0000001", studioKey, instructorKey, true)
	plainKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollrcreate0000002", otherStudio, "", false)

	// The playbook declares .horizon as a REQUIRED read, and the lens never
	// dispatches over a series without one — so the NotRolling vector is a
	// submitter that tolerates the aspect's absence, the one shape that can
	// reach the script's own refusal rather than a hydration miss.
	notRolling := extendEnv("wdrollrplain00000001", plainKey, otherStudio, rsNextStartsAt, rsNextEndsAt, "", bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")
	notRolling.ContextHint.Reads = []string{plainKey, plainKey + ".definition", otherStudio}
	notRolling.ContextHint.OptionalReads = []string{plainKey + ".horizon"}

	cases := []struct {
		name string
		env  *processor.OperationEnvelope
		code string
	}{
		{"one week early", extendEnv("wdrollrstale00000001", seriesKey, studioKey, "2026-07-22T09:00:00Z", "2026-07-22T09:30:00Z", instructorKey, bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z"), "StaleHorizon"},
		{"instructor dropped", extendEnv("wdrollrstale00000002", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, "", bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z"), "StaleHorizon"},
		{"wrong studio", extendEnv("wdrollrwrong00000001", seriesKey, otherStudio, rsNextStartsAt, rsNextEndsAt, instructorKey, bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z"), "WrongStudio"},
		{"not rolling", notRolling, "NotRolling"},
		{"not Weaver", extendEnv("wdrollrdenied0000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, instructorKey, domainActorKey, "2026-07-08T09:00:30Z"), "AuthDenied"},
	}
	for _, tc := range cases {
		outcome, _, why := extendSeries(t, ctx, conn, cp, cons, tc.env)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("%s: outcome = %v, want Rejected", tc.name, outcome)
		}
		if !strings.HasPrefix(why, tc.code+":") {
			t.Fatalf("%s: failure = %q, want %s", tc.name, why, tc.code)
		}
		if keyExists(t, ctx, conn, "vtx.session."+nanoIDFromRequestID(tc.env.RequestID)) {
			t.Fatalf("%s: a refused extension must mint nothing", tc.name)
		}
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, rsFirstStartsAt, instructorKey, rsCount)
	if h := horizonData(t, ctx, conn, plainKey); h != nil {
		t.Fatalf("a refused NotRolling must not write a horizon: %v", h)
	}

	// The positive vector the refusals are measured against: the same
	// series, the same dispatch instant, the horizon's own pin.
	if outcome, _, why := extendSeries(t, ctx, conn, cp, cons, extendEnv("wdrollrmint000000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, instructorKey, bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")); outcome != processor.OutcomeAccepted {
		t.Fatalf("the positive vector must be Accepted, got %v (%s)", outcome, why)
	}
}

// TestTombstoneSessionSeries_StopsRolling: the call-off on a rolling series
// cancels the still-upcoming occurrences AND stops the roll — .horizon loses
// extendAt and records stoppedAt, next*/instructor/mintedCount carried —
// so a later dispatch refuses NotRolling and the lens arms nothing. A rolling
// series with nothing left to cancel still stops rather than refusing
// NoUpcomingOccurrences; a non-rolling one with nothing to cancel still
// refuses.
func TestTombstoneSessionSeries_StopsRolling(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingstop")

	instructorKey := mkSeriesInstructor(t, ctx, conn, cp, cons, "wdrollsinstruct00001", "Sam")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollsstudio0000001", "Flow Room")
	seriesKey, sessionKeys := createSeries(t, ctx, conn, cp, cons, "wdrollscreate0000001", studioKey, instructorKey, true)

	// Called off between the first and the second class.
	stopReq := "wdrollsstop000000001"
	if got, why := tombstoneSeries(t, ctx, conn, cp, cons, stopReq, seriesKey, studioKey, "2026-07-09T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("TombstoneSessionSeries outcome = %v (%s), want Accepted", got, why)
	}
	if !keyExists(t, ctx, conn, sessionKeys[0]) {
		t.Fatalf("the class that already ran must survive the call-off")
	}
	for _, sk := range sessionKeys[1:] {
		if keyExists(t, ctx, conn, sk) {
			t.Fatalf("%s must be cancelled by the call-off", sk)
		}
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, "", instructorKey, rsCount)
	if got, _ := horizonData(t, ctx, conn, seriesKey)["stoppedAt"].(string); got != "2026-07-09T12:00:00Z" {
		t.Fatalf(".horizon.stoppedAt = %q, want the call-off's submittedAt", got)
	}
	if ev := findEmittedEvent(t, ctx, conn, testutil.GenReqID(stopReq), "wellness.sessionSeriesCancelled"); ev["stoppedRolling"] != true {
		t.Fatalf("event stoppedRolling = %v, want true", ev["stoppedRolling"])
	}

	// The roll is over: the window never moves again.
	if outcome, _, why := extendSeries(t, ctx, conn, cp, cons, extendEnv("wdrollsextend0000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, instructorKey, bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")); outcome != processor.OutcomeRejected || !strings.HasPrefix(why, "NotRolling:") {
		t.Fatalf("an extension after the stop must refuse NotRolling, got %v (%s)", outcome, why)
	}

	// A rolling series whose classes have all started: nothing to cancel,
	// but the stop is the work, so the call-off succeeds.
	studioTwo := createStudio(t, ctx, conn, cp, cons, "wdrollsstudio0000002", "Power Room")
	lateKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollscreate0000002", studioTwo, "", true)
	lateReq := "wdrollsstop000000002"
	if got, why := tombstoneSeries(t, ctx, conn, cp, cons, lateReq, lateKey, studioTwo, "2026-07-23T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("call-off of a rolling series with nothing to cancel = %v (%s), want Accepted (the stop is the work)", got, why)
	}
	requireHorizon(t, horizonData(t, ctx, conn, lateKey), rsNextStartsAt, rsNextEndsAt, "", "", rsCount)
	ev := findEmittedEvent(t, ctx, conn, testutil.GenReqID(lateReq), "wellness.sessionSeriesCancelled")
	if keys, _ := ev["sessionKeys"].([]any); len(keys) != 0 || ev["stoppedRolling"] != true {
		t.Fatalf("event = %v, want no sessionKeys and stoppedRolling true", ev)
	}

	// The non-rolling twin of that shape still refuses.
	studioThree := createStudio(t, ctx, conn, cp, cons, "wdrollsstudio0000003", "Quiet Room")
	plainKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollscreate0000003", studioThree, "", false)
	if got, why := tombstoneSeries(t, ctx, conn, cp, cons, "wdrollsstop000000003", plainKey, studioThree, "2026-07-23T12:00:00Z"); got != processor.OutcomeRejected || !strings.HasPrefix(why, "NoUpcomingOccurrences:") {
		t.Fatalf("call-off of a non-rolling series with nothing to cancel = %v (%s), want NoUpcomingOccurrences", got, why)
	}
	if h := horizonData(t, ctx, conn, plainKey); h != nil {
		t.Fatalf("a non-rolling series must gain no horizon from its call-off: %v", h)
	}
}

// TestReassignSessionSeries_ShiftsHorizon: moving the run carries its
// horizon with it — nextStartsAt and extendAt shift by the move's delta, and
// nextEndsAt takes on the span every moved occurrence takes on — so the next
// window the platform mints stays on the moved cadence. A non-rolling series
// moved the same way gains no horizon.
func TestReassignSessionSeries_ShiftsHorizon(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingshift")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollmstudio0000001", "Flow Room")
	seriesKey, sessionKeys := createSeries(t, ctx, conn, cp, cons, "wdrollmcreate0000001", studioKey, "", true)

	// Anchored on the first class, moved +2 days 1 hour onto a 75-minute span.
	if got, why := reassignSeries(t, ctx, conn, cp, cons, "wdrollmmove000000001", seriesKey, studioKey,
		sessionKeys[0], rsFirstStartsAt, "2026-07-10T10:00:00Z", "2026-07-10T11:15:00Z", "2026-07-07T13:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("ReassignSessionSeries outcome = %v (%s), want Accepted", got, why)
	}
	shift := 49 * time.Hour
	assertMoved(t, ctx, conn, sessionKeys[2], "2026-07-22T09:00:00Z", shift, 75*time.Minute)
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey),
		wdShifted(t, rsNextStartsAt, shift), wdShifted(t, rsNextStartsAt, shift+75*time.Minute), wdShifted(t, rsFirstStartsAt, shift), "", rsCount)

	// The moved horizon is what the next dispatch must name.
	env := extendEnv("wdrollmextend0000001", seriesKey, studioKey, wdShifted(t, rsNextStartsAt, shift), wdShifted(t, rsNextStartsAt, shift+75*time.Minute), "",
		bootstrap.WeaverIdentityKey, "2026-07-10T10:00:30Z")
	if outcome, _, why := extendSeries(t, ctx, conn, cp, cons, env); outcome != processor.OutcomeAccepted {
		t.Fatalf("ExtendSessionSeries on the moved horizon = %v (%s), want Accepted", outcome, why)
	}
	starts, ends, _, _ := sessionSchedule(t, ctx, conn, "vtx.session."+nanoIDFromRequestID(env.RequestID))
	if starts != wdShifted(t, rsNextStartsAt, shift) || ends != wdShifted(t, rsNextStartsAt, shift+75*time.Minute) {
		t.Fatalf("the minted occurrence = %s–%s, want the moved cadence and span", starts, ends)
	}

	studioTwo := createStudio(t, ctx, conn, cp, cons, "wdrollmstudio0000002", "Power Room")
	plainKey, plainKeys := createSeries(t, ctx, conn, cp, cons, "wdrollmcreate0000002", studioTwo, "", false)
	if got, why := reassignSeries(t, ctx, conn, cp, cons, "wdrollmmove000000002", plainKey, studioTwo,
		plainKeys[0], rsFirstStartsAt, "2026-07-10T10:00:00Z", "2026-07-10T11:15:00Z", "2026-07-07T13:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("ReassignSessionSeries on a non-rolling series = %v (%s), want Accepted", got, why)
	}
	if h := horizonData(t, ctx, conn, plainKey); h != nil {
		t.Fatalf("a non-rolling series must gain no horizon from its move: %v", h)
	}
}

// TestExtendSessionSeries_MintsUnledWhenInstructorRetired: TombstoneInstructor
// has no upcoming-classes guard, so a rolling run's horizon can name an
// instructor who is gone. The extension mints the class UNLED and the horizon
// drops the instructor — the desk assigns a leader per class or stops the
// run — rather than refusing, which would spend the gap's budget and let the
// schedule run out. The event names the dropped instructor.
func TestExtendSessionSeries_MintsUnledWhenInstructorRetired(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingretired")

	instructorKey := mkSeriesInstructor(t, ctx, conn, cp, cons, "wdrollrinstruct00001", "Sam")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollrstudio0000001", "Flow Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollrcreate0000001", studioKey, instructorKey, true)

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "TombstoneInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdrollrretire0000001"), Lane: processor.LaneDefault, OperationType: "TombstoneInstructor",
		Actor: domainActorKey, SubmittedAt: "2026-07-08T08:00:00Z", Class: "instructor",
		Payload:     json.RawMessage(`{"instructorKey":"` + instructorKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{instructorKey}},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The led gap still dispatches with the instructor the horizon records.
	env := extendEnv("wdrollrextend0000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, instructorKey,
		bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")
	outcome, _, why := extendSeries(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("ExtendSessionSeries outcome = %v (%s), want Accepted — a retired instructor must not park the run", outcome, why)
	}
	sessKey := "vtx.session." + nanoIDFromRequestID(env.RequestID)
	if doc := readDoc(t, ctx, conn, sessKey); doc["class"] != "session" {
		t.Fatalf("minted occurrence class = %v, want session", doc["class"])
	}
	_, sessID, _ := substrate.ParseVertexKey(sessKey)
	_, instructorID, _ := substrate.ParseVertexKey(instructorKey)
	if keyExists(t, ctx, conn, "lnk.session."+sessID+".ledBy.instructor."+instructorID) {
		t.Fatalf("the occurrence must be minted unled — its instructor is retired")
	}
	assertCells(t, ctx, conn, studioKey, rsNextStartsAt, rsNextEndsAt, true, "the studio's cells for the unled occurrence")
	assertCells(t, ctx, conn, instructorKey, rsNextStartsAt, rsNextEndsAt, false, "no cells on a retired instructor")

	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsAfterNext, rsAfterNextEnds, "2026-07-15T09:00:00Z", "", rsCount+1)

	ev := findEmittedEvent(t, ctx, conn, env.RequestID, "wellness.sessionSeriesExtended")
	if ev["instructorDropped"] != instructorKey {
		t.Fatalf("event instructorDropped = %v, want %s", ev["instructorDropped"], instructorKey)
	}
	if ev["sessionKey"] != sessKey {
		t.Fatalf("event sessionKey = %v, want %s", ev["sessionKey"], sessKey)
	}
}

// TestExtendSessionSeries_SkipsInstructorConflict pins the skip's second cell
// arm: the studio's cells for the occurrence are free, but the instructor the
// horizon names is already booked for that span at ANOTHER studio (a one-off
// CreateSession there), so the run passes that week — no session, the
// one-off untouched, the horizon moved, the skip named InstructorConflict.
func TestExtendSessionSeries_SkipsInstructorConflict(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingskipinstr")

	instructorKey := mkSeriesInstructor(t, ctx, conn, cp, cons, "wdrollninstruct00001", "Sam")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollnstudio0000001", "Flow Room")
	elsewhere := createStudio(t, ctx, conn, cp, cons, "wdrollnstudio0000002", "Power Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollncreate0000001", studioKey, instructorKey, true)
	oneOffKey, oneOff, reply := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdrollnoneoff0000001", elsewhere, instructorKey, "Private Session", "2026-07-29T09:15:00Z", "2026-07-29T09:45:00Z", 1)
	if oneOff != processor.OutcomeAccepted {
		t.Fatalf("one-off CreateSession elsewhere outcome = %v (%+v), want Accepted", oneOff, reply)
	}

	env := extendEnv("wdrollnextend0000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, instructorKey,
		bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")
	outcome, _, why := extendSeries(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("ExtendSessionSeries outcome = %v (%s), want Accepted", outcome, why)
	}
	if keyExists(t, ctx, conn, "vtx.session."+nanoIDFromRequestID(env.RequestID)) {
		t.Fatalf("an occurrence whose instructor is booked elsewhere must not be minted")
	}
	if !keyExists(t, ctx, conn, oneOffKey) {
		t.Fatalf("the one-off that holds the instructor must survive")
	}
	assertCells(t, ctx, conn, studioKey, rsNextStartsAt, rsNextEndsAt, false, "the studio's cells stay free on an instructor skip")
	assertCells(t, ctx, conn, instructorKey, "2026-07-29T09:15:00Z", "2026-07-29T09:45:00Z", true, "the instructor's one-off cells")
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsAfterNext, rsAfterNextEnds, "2026-07-15T09:00:00Z", instructorKey, rsCount)
	if ev := findEmittedEvent(t, ctx, conn, env.RequestID, "wellness.sessionSeriesExtended"); ev["skipped"] != "InstructorConflict" {
		t.Fatalf("event skipped = %v, want InstructorConflict", ev["skipped"])
	}
}

// TestExtendSessionSeries_StaleHorizon_InstructorAgainstUnledHorizon pins the
// other half of the instructor pin: a dispatch that names an instructor for a
// horizon that records none is stale (the led gap fired on a row the horizon
// no longer matches), refused, and mints nothing.
func TestExtendSessionSeries_StaleHorizon_InstructorAgainstUnledHorizon(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingstaleled")

	instructorKey := mkSeriesInstructor(t, ctx, conn, cp, cons, "wdrollsinstruct00002", "Sam")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollsstudio0000004", "Flow Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollscreate0000004", studioKey, "", true)

	env := extendEnv("wdrollsextend0000002", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, instructorKey,
		bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")
	outcome, _, why := extendSeries(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected || !strings.HasPrefix(why, "StaleHorizon:") {
		t.Fatalf("an instructor against an unled horizon = %v (%s), want StaleHorizon", outcome, why)
	}
	if keyExists(t, ctx, conn, "vtx.session."+nanoIDFromRequestID(env.RequestID)) {
		t.Fatalf("a refused extension must mint nothing")
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, rsFirstStartsAt, "", rsCount)
}

// ---- StopSessionSeries -----------------------------------------------------

// stopSeriesAs submits StopSessionSeries with exactly the read posture its
// op-meta declares (opmetas.go): the series vertex as a required read; the
// studio confirmation link, the studio vertex and the series' .horizon as
// optional reads; the operator-role probe as its one walk. Returns the
// outcome, the reply and the script's own failure text (WrongStudio and
// NotRolling are indistinguishable at the outcome level).
func stopSeriesAs(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, seriesKey, studioKey, submittedAt, actorKey string) (processor.MessageOutcome, *processor.OperationReply, string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "StopSessionSeries",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "sessionseries",
		Payload:       json.RawMessage(`{"seriesKey":"` + seriesKey + `","studio":"` + studioKey + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations:  testutil.DeclaredEnumerations("StopSessionSeries", actorKey, wellnessdomain.OpMetas()),
			Reads:         []string{seriesKey},
			OptionalReads: []string{seriesAtStudioLnkKey(t, seriesKey, studioKey), studioKey, seriesKey + ".horizon"},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	failure := ""
	if reply != nil && reply.Error != nil {
		if i := strings.Index(reply.Error.Message, "fail: "); i >= 0 {
			failure = reply.Error.Message[i+len("fail: "):]
		}
	}
	return outcome, reply, failure
}

// TestStopSessionSeries_StopsRolling: the walk-free off switch closes the
// horizon (extendAt dropped, stoppedAt recorded, next*/instructor/mintedCount
// carried), touches nothing on the grid, emits the stop and returns the
// series; a later dispatch refuses NotRolling. Its refusals, each measured
// against that positive vector: WrongStudio for a studio that is not the
// series', NotRolling for a series created without the flag and for one
// already stopped.
func TestStopSessionSeries_StopsRolling(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingstopop")

	instructorKey := mkSeriesInstructor(t, ctx, conn, cp, cons, "wdrollqinstruct00001", "Sam")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollqstudio0000001", "Flow Room")
	otherStudio := createStudio(t, ctx, conn, cp, cons, "wdrollqstudio0000002", "Power Room")
	seriesKey, sessionKeys := createSeries(t, ctx, conn, cp, cons, "wdrollqcreate0000001", studioKey, instructorKey, true)
	plainKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollqcreate0000002", otherStudio, "", false)

	if got, _, why := stopSeriesAs(t, ctx, conn, cp, cons, "wdrollqwrong00000001", seriesKey, otherStudio, "2026-07-09T12:00:00Z", domainActorKey); got != processor.OutcomeRejected || !strings.HasPrefix(why, "WrongStudio:") {
		t.Fatalf("stop with another studio = %v (%s), want WrongStudio", got, why)
	}
	if got, _, why := stopSeriesAs(t, ctx, conn, cp, cons, "wdrollqplain00000001", plainKey, otherStudio, "2026-07-09T12:00:00Z", domainActorKey); got != processor.OutcomeRejected || !strings.HasPrefix(why, "NotRolling:") {
		t.Fatalf("stop of a non-rolling series = %v (%s), want NotRolling", got, why)
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, rsFirstStartsAt, instructorKey, rsCount)

	stopReq := "wdrollqstop000000001"
	got, reply, why := stopSeriesAs(t, ctx, conn, cp, cons, stopReq, seriesKey, studioKey, "2026-07-09T12:00:00Z", domainActorKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("StopSessionSeries outcome = %v (%s), want Accepted", got, why)
	}
	if reply.PrimaryKey != seriesKey {
		t.Fatalf("primaryKey = %q, want the series %q", reply.PrimaryKey, seriesKey)
	}
	for i, sk := range sessionKeys {
		if !keyExists(t, ctx, conn, sk) {
			t.Fatalf("occurrence %d must survive the stop — nothing on the grid is touched", i)
		}
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, "", instructorKey, rsCount)
	if got, _ := horizonData(t, ctx, conn, seriesKey)["stoppedAt"].(string); got != "2026-07-09T12:00:00Z" {
		t.Fatalf(".horizon.stoppedAt = %q, want the stop's submittedAt", got)
	}
	ev := findEmittedEvent(t, ctx, conn, testutil.GenReqID(stopReq), "wellness.sessionSeriesStopped")
	if ev["seriesKey"] != seriesKey || ev["studio"] != studioKey {
		t.Fatalf("event = %v, want seriesKey/studio of the stop", ev)
	}

	if got, _, why := stopSeriesAs(t, ctx, conn, cp, cons, "wdrollqagain00000001", seriesKey, studioKey, "2026-07-09T12:30:00Z", domainActorKey); got != processor.OutcomeRejected || !strings.HasPrefix(why, "NotRolling:") {
		t.Fatalf("a second stop = %v (%s), want NotRolling", got, why)
	}
	if outcome, _, why := extendSeries(t, ctx, conn, cp, cons, extendEnv("wdrollqextend0000001", seriesKey, studioKey, rsNextStartsAt, rsNextEndsAt, instructorKey, bootstrap.WeaverIdentityKey, "2026-07-08T09:00:30Z")); outcome != processor.OutcomeRejected || !strings.HasPrefix(why, "NotRolling:") {
		t.Fatalf("an extension after the stop must refuse NotRolling, got %v (%s)", outcome, why)
	}
}

// seedSeriesHistory hangs n already-run occurrences off a series — session
// vertices with a .schedule in the past, each partOf-linked — directly in
// Core KV, the footprint a long-lived rolling run accumulates one extension
// at a time. Their ids come off one deterministic NanoID stream.
func seedSeriesHistory(t *testing.T, ctx context.Context, conn *substrate.Conn, seriesKey, studioKey string, n int) {
	t.Helper()
	_, seriesID, _ := substrate.ParseVertexKey(seriesKey)
	_, studioID, _ := substrate.ParseVertexKey(studioKey)
	for i, id := range nanoIDsFromRequestID(testutil.GenReqID("wdrollhistory0000001"), n) {
		sessKey := "vtx.session." + id
		seedVertex(t, ctx, conn, sessKey, "session", map[string]any{})
		startsAt := wdShifted(t, rsFirstStartsAt, -time.Duration(n-i)*7*24*time.Hour)
		seedAspect(t, ctx, conn, sessKey, "schedule", "sessionSchedule", map[string]any{
			"name": "Evening Flow", "startsAt": startsAt, "endsAt": wdShifted(t, startsAt, 30*time.Minute), "capacity": 20,
		})
		seedLink(t, ctx, conn, "lnk.session."+id+".partOf.sessionseries."+seriesID, sessKey, seriesKey, "partOf", "partOf")
		seedLink(t, ctx, conn, "lnk.session."+id+".atStudio.studio."+studioID, sessKey, studioKey, "atStudio", "atStudio")
	}
}

// TestStopSessionSeries_OutlivesTheWalk is the pin for the rolling run's off
// switch: a rolling series whose partOf history has grown past the walk's
// page budget (SERIES_OCCURRENCE_MAX_PAGES × SERIES_OCCURRENCE_PAGE_LIMIT)
// can no longer be called off as a whole — TombstoneSessionSeries refuses
// SeriesWalkBound — but StopSessionSeries, which never walks, still stops
// it.
func TestStopSessionSeries_OutlivesTheWalk(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "rollingoutlive")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrollostudio0000001", "Flow Room")
	seriesKey, _ := createSeries(t, ctx, conn, cp, cons, "wdrollocreate0000001", studioKey, "", true)
	seedSeriesHistory(t, ctx, conn, seriesKey, studioKey, 130)

	if got, why := tombstoneSeries(t, ctx, conn, cp, cons, "wdrollotomb000000001", seriesKey, studioKey, "2026-07-09T12:00:00Z"); got != processor.OutcomeRejected || !strings.HasPrefix(why, "SeriesWalkBound:") {
		t.Fatalf("whole-run call-off of a run whose history outgrew the walk = %v (%s), want SeriesWalkBound", got, why)
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, rsFirstStartsAt, "", rsCount)

	if got, _, why := stopSeriesAs(t, ctx, conn, cp, cons, "wdrollostop000000001", seriesKey, studioKey, "2026-07-09T12:00:00Z", domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("StopSessionSeries on the same run = %v (%s), want Accepted — the off switch must never walk", got, why)
	}
	requireHorizon(t, horizonData(t, ctx, conn, seriesKey), rsNextStartsAt, rsNextEndsAt, "", "", rsCount)
}
