// TombstoneStudio's HasUpcomingClasses guard: a studio still holding a class
// that has not begun is refused, not retired out from under its booked, charged
// classes. Each case leads with the positive sibling — a retire the guard must
// let through — so a Rejected is the guard talking and not a broken path. The
// front-of-house confinement half lives in workplace_confinement_test.go.
package wellnessdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// tombstoneStudioAs submits TombstoneStudio as an arbitrary actor at a chosen
// submittedAt, declaring what the real dispatchers declare: the studio as the
// required read, and the two payload-hubbed walks the script runs (the
// upcoming-class walk over the studio's inbound atStudio links and the
// confinement's locatedAt walk) alongside the descriptor's actor-hubbed
// operator probe. Returns the outcome and the rejection's message ("" when
// accepted).
func tombstoneStudioAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, studioKey, actorKey, submittedAt string) (processor.MessageOutcome, string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneStudio",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "studio",
		Payload:       json.RawMessage(`{"studioKey":"` + studioKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{studioKey},
			Enumerations: append(testutil.DeclaredEnumerations("TombstoneStudio", actorKey, wellnessdomain.OpMetas()),
				processor.EnumerationHint{Hub: studioKey, Relation: "atStudio", Direction: "in"},
				processor.EnumerationHint{Hub: studioKey, Relation: "locatedAt", Direction: "out"}),
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	why := ""
	if reply != nil && reply.Error != nil {
		why = reply.Error.Message
		if code, ok := reply.Error.Details["code"].(string); ok {
			why = code + ": " + why
		}
	}
	return outcome, why
}

// TestTombstoneStudio_RefusesWhileAClassIsStillUpcoming: a studio with no
// classes retires (the positive sibling); one holding a class that starts after
// submittedAt is refused with the class named, and stays alive; once that class
// is called off (TombstoneSession — the atStudio link survives it, so the walk
// has to read the session vertex, not trust the link) the same retire lands.
func TestTombstoneStudio_RefusesWhileAClassIsStillUpcoming(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdtsupcoming")

	empty := createStudio(t, ctx, conn, cp, cons, "wdtsupstudioe000001", "Empty Room")
	if got, why := tombstoneStudioAs(t, ctx, conn, cp, cons,
		"wdtsupretiree000001", empty, domainActorKey, "2026-07-08T08:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("retiring a studio with no classes = %v (%s), want Accepted "+
			"(the positive sibling — if this fails the negatives prove nothing)", got, why)
	}
	if keyExists(t, ctx, conn, empty) {
		t.Fatalf("%s must be tombstoned by the accepted retire", empty)
	}

	studio := createStudio(t, ctx, conn, cp, cons, "wdtsupstudioa000001", "Flow Room")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdtsupsessiona000001",
		studio, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession = %v, want Accepted", outcome)
	}

	got, why := tombstoneStudioAs(t, ctx, conn, cp, cons,
		"wdtsupretirea000001", studio, domainActorKey, "2026-07-08T08:00:00Z")
	if got != processor.OutcomeRejected {
		t.Fatalf("retiring a studio with an UPCOMING class = %v, want Rejected", got)
	}
	if !strings.Contains(why, "HasUpcomingClasses") || !strings.Contains(why, sessionKey) {
		t.Errorf("refused with %q, want the guard's own HasUpcomingClasses naming %s", why, sessionKey)
	}
	if !keyExists(t, ctx, conn, studio) {
		t.Fatalf("the refused retire tombstoned %s; it must refuse before any mutation", studio)
	}

	// Call the class off. TombstoneSession leaves the atStudio link live
	// (no cascade), so a guard that trusted the link alone would still refuse.
	cancel := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdtsupcancela000001"),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T08:05:00Z",
		Class:         "session",
		Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studio + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("TombstoneSession", domainActorKey, wellnessdomain.OpMetas()), Reads: []string{
			sessionKey, sessionKey + ".schedule", atStudioLnkKey(t, sessionKey, studio),
		}},
	}
	testutil.PublishOp(t, conn, cancel)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if !keyExists(t, ctx, conn, atStudioLnkKey(t, sessionKey, studio)) {
		t.Fatalf("the atStudio link must survive TombstoneSession (no-cascade doctrine) for this vector to test the vertex read")
	}

	if got, why := tombstoneStudioAs(t, ctx, conn, cp, cons,
		"wdtsupretirea000002", studio, domainActorKey, "2026-07-08T08:10:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("retiring the studio once its class is called off = %v (%s), want Accepted", got, why)
	}
	if keyExists(t, ctx, conn, studio) {
		t.Fatalf("%s must be tombstoned by the accepted retire", studio)
	}
}

// TestTombstoneStudio_StartedClassesAreHistory: a class that has begun never
// blocks a retire — the studio's record is not what is being removed, and the
// same at-the-boundary reading TombstoneSessionSeries applies holds: starting
// exactly at submittedAt counts as started.
func TestTombstoneStudio_StartedClassesAreHistory(t *testing.T) {
	cases := []struct {
		name, submittedAt string
	}{
		{"after the class ended", "2026-07-08T10:00:00Z"},
		{"exactly at the class's startsAt", "2026-07-08T09:00:00Z"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := setupDomainEnv(t)
			cp, cons := newDomainPipeline(t, ctx, conn, "wdtshistory"+string(rune('a'+i)))

			studio := createStudio(t, ctx, conn, cp, cons, "wdtshistudio"+string(rune('a'+i))+"0000001", "Flow Room")
			if _, outcome := createSession(t, ctx, conn, cp, cons, "wdtshisession"+string(rune('a'+i))+"000001",
				studio, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20); outcome != processor.OutcomeAccepted {
				t.Fatalf("CreateSession = %v, want Accepted", outcome)
			}
			// The class is still ahead one second before it begins — the
			// refusal's own positive, so the acceptance below is the boundary
			// and not the guard failing to see the class at all.
			if got, why := tombstoneStudioAs(t, ctx, conn, cp, cons,
				"wdtshiretire"+string(rune('a'+i))+"0000001", studio, domainActorKey, "2026-07-08T08:59:59Z"); got != processor.OutcomeRejected {
				t.Fatalf("retiring one second before the class begins = %v (%s), want Rejected", got, why)
			}
			if got, why := tombstoneStudioAs(t, ctx, conn, cp, cons,
				"wdtshiretire"+string(rune('a'+i))+"0000002", studio, domainActorKey, tc.submittedAt); got != processor.OutcomeAccepted {
				t.Fatalf("retiring %s = %v (%s), want Accepted — a started class is history", tc.name, got, why)
			}
			if keyExists(t, ctx, conn, studio) {
				t.Fatalf("%s must be tombstoned by the accepted retire", studio)
			}
		})
	}
}

// TestTombstoneStudio_AMovedClassNoLongerBlocks: a session ReassignSession
// moved to another studio has THIS studio's atStudio link tombstoned (the
// vertex and its .schedule stay live and upcoming at the new studio), so the
// walk skips it by the link's own isDeleted and the old studio retires. The
// link is seeded in the state the move leaves rather than driven through the
// move, so the vector pins the guard's link test and nothing else.
func TestTombstoneStudio_AMovedClassNoLongerBlocks(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdtsmoved")

	studio := createStudio(t, ctx, conn, cp, cons, "wdtsmvstudioa000001", "Old Room")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdtsmvsessiona000001",
		studio, "Vinyasa Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession = %v, want Accepted", outcome)
	}
	// POSITIVE SIBLING of the link test: while the link is live the class blocks.
	if got, why := tombstoneStudioAs(t, ctx, conn, cp, cons,
		"wdtsmvretirea000001", studio, domainActorKey, "2026-07-08T08:00:00Z"); got != processor.OutcomeRejected {
		t.Fatalf("retiring under a live atStudio link = %v (%s), want Rejected", got, why)
	}

	lnk := atStudioLnkKey(t, sessionKey, studio)
	doc := map[string]any{
		"class": "atStudio", "isDeleted": true,
		"sourceVertex": sessionKey, "targetVertex": studio,
		"localName": "atStudio", "data": map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, lnk, b); err != nil {
		t.Fatalf("seed tombstoned link %s: %v", lnk, err)
	}
	if !keyExists(t, ctx, conn, sessionKey) {
		t.Fatalf("the moved session must stay live for this vector to test the link, not the vertex")
	}

	if got, why := tombstoneStudioAs(t, ctx, conn, cp, cons,
		"wdtsmvretirea000002", studio, domainActorKey, "2026-07-08T08:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("retiring once the class's atStudio link is tombstoned = %v (%s), want Accepted", got, why)
	}
	if keyExists(t, ctx, conn, studio) {
		t.Fatalf("%s must be tombstoned by the accepted retire", studio)
	}
}
