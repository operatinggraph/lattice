// SetBookingAttendance's no-show fee resolution: a noShow mark that names no
// fee bills the policy recorded on the studio the session is at now
// (studioProfile.noShowFeeCents), or the documented 2500 default for a studio
// with none; an explicit payload fee (0 included — the waiver and the Weaver
// sweep's documentation-lapse signal) wins over the policy; a malformed stored
// policy is refused, never billed. The writers' half (CreateStudio /
// SetStudioProfile validating the value at the mint) lives in
// studio_profile_test.go.
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
)

// nsfBookedSeat creates a studio carrying the given profile payload (the name
// plus an optional policy), a class on it, and one booked seat, returning the
// three keys. The class starts 2026-07-08T09:00Z; marks are submitted after.
func nsfBookedSeat(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label string, studioPayload map[string]any, bookerID string) (studioKey, sessionKey, bookingKey string) {
	t.Helper()
	studioKey, outcome, why := createStudioWith(t, ctx, conn, cp, cons, label+"studio1", studioPayload)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateStudio = %v (%s), want Accepted", outcome, why)
	}
	sessionKey, outcome = createSession(t, ctx, conn, cp, cons, label+"sessn1", studioKey, "Vinyasa Flow",
		"2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession = %v, want Accepted", outcome)
	}
	bookerKey := seedIdentity(t, ctx, conn, bookerID)
	bookingKey, outcome = createBooking(t, ctx, conn, cp, cons, label+"bookg1", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking = %v, want Accepted", outcome)
	}
	return studioKey, sessionKey, bookingKey
}

// markNoShowWithFee submits a noShow mark as the operator, with or without an
// explicit payload fee (feeCents nil omits the field), and returns the outcome
// and the rejection's message.
func markNoShowWithFee(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, bookingKey, sessionKey string, feeCents any) (processor.MessageOutcome, string) {
	t.Helper()
	env := attendanceEnv(t, label, bookingKey, sessionKey, "noShow", "", domainActorKey, "2026-07-08T09:05:00Z")
	if feeCents != nil {
		var payloadMap map[string]any
		if err := json.Unmarshal(env.Payload, &payloadMap); err != nil {
			t.Fatalf("unmarshal attendance payload: %v", err)
		}
		payloadMap["noShowFeeCents"] = feeCents
		env.Payload, _ = json.Marshal(payloadMap)
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	why := ""
	if reply != nil && reply.Error != nil {
		why = reply.Error.Message
	}
	return outcome, why
}

// assertNoShowFee reads the booking's .status and checks it is noShow carrying
// exactly the expected fee (wantFee < 0 means the field must be absent — a
// fee-free no-show writes nothing, which is what wellnessNoShowSettlement's
// feeCents > 0 gate reads as "nothing to bill").
func assertNoShowFee(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey string, wantFee float64) {
	t.Helper()
	status := attendanceStatus(t, ctx, conn, bookingKey)
	if got, _ := status["value"].(string); got != "noShow" {
		t.Fatalf("status.value = %q, want noShow", got)
	}
	got, present := status["noShowFeeCents"]
	if wantFee < 0 {
		if present {
			t.Fatalf("status.noShowFeeCents = %v, want absent — a fee-free no-show writes no fee", got)
		}
		return
	}
	if !present || got != wantFee {
		t.Fatalf("status.noShowFeeCents = %v (present=%v), want %v", got, present, wantFee)
	}
}

// nsfMoveClassTo leaves the session in the state ReassignSession's studio
// move leaves it: the current live atStudio link tombstoned and a live one to
// a freshly seeded studio (id newStudioID, policy feeCents) written beside it.
func nsfMoveClassTo(t *testing.T, ctx context.Context, conn *substrate.Conn,
	sessionKey, fromStudioKey, newStudioID string, feeCents float64) string {
	t.Helper()
	_, sessionID, _ := substrate.ParseVertexKey(sessionKey)
	newStudioKey := "vtx.studio." + newStudioID
	seedVertex(t, ctx, conn, newStudioKey, "studio", nil)
	seedAspect(t, ctx, conn, newStudioKey, "profile", "studioProfile", map[string]any{"name": "Moved Room " + newStudioID[:1], "noShowFeeCents": feeCents})
	dead := map[string]any{
		"class": "atStudio", "isDeleted": true,
		"sourceVertex": sessionKey, "targetVertex": fromStudioKey,
		"localName": "atStudio", "data": map[string]any{},
	}
	b, _ := json.Marshal(dead)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, atStudioLnkKey(t, sessionKey, fromStudioKey), b); err != nil {
		t.Fatalf("tombstone the atStudio link to %s: %v", fromStudioKey, err)
	}
	seedLink(t, ctx, conn, "lnk.session."+sessionID+".atStudio.studio."+newStudioID, sessionKey, newStudioKey, "atStudio", "atStudio")
	return newStudioKey
}

// TestSetBookingAttendance_NoShowFeeFromStudioPolicy: a studio with
// noShowFeeCents = 1000 bills $10 on a payload-less no-show mark — and the
// policy read is of the studio the session is at NOW. ListLinks returns
// tombstoned links in the page and keys sort by target id, so the class is
// moved twice, once in each order: to a studio whose id sorts AFTER the
// original's (the tombstone leads the page — a walk taking the first link
// bills the old studio) and then to one sorting BEFORE both (the tombstones
// trail — a walk taking the last link without the isDeleted test bills a dead
// link's studio). Each mark must bill the studio the live link names.
func TestSetBookingAttendance_NoShowFeeFromStudioPolicy(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "nsfpolicy")

	studioKey, sessionKey, bookingKey := nsfBookedSeat(t, ctx, conn, cp, cons, "wdnsfpol",
		map[string]any{"name": "Flow Room", "noShowFeeCents": 1000}, "BBWELLNSFPQLBKRHJKMN")
	if got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfpolmark00000001", bookingKey, sessionKey, nil); got != processor.OutcomeAccepted {
		t.Fatalf("noShow mark with no payload fee = %v (%s), want Accepted", got, why)
	}
	assertNoShowFee(t, ctx, conn, bookingKey, 1000)

	_, originalID, _ := substrate.ParseVertexKey(studioKey)
	const afterID, beforeID = "zzzzzzzzzzzzzzzzzzzz", "AAAAAAAAAAAAAAAAAAAA"
	if !(beforeID < originalID && originalID < afterID) {
		t.Fatalf("fixture: the moved-to ids must bracket the original %q for the tombstones to lead one page and trail the other", originalID)
	}

	// Tombstone first in the page: moved to a studio sorting after the original.
	afterKey := nsfMoveClassTo(t, ctx, conn, sessionKey, studioKey, afterID, 500)
	booker2 := seedIdentity(t, ctx, conn, "BBWELLNSFPQLBKRJKMNP")
	booking2, outcome := createBooking(t, ctx, conn, cp, cons, "wdnsfpolbookg2000001", sessionKey, booker2, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("second CreateBooking = %v, want Accepted", outcome)
	}
	if got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfpolmark00000002", booking2, sessionKey, nil); got != processor.OutcomeAccepted {
		t.Fatalf("noShow mark on the class moved to %s = %v (%s), want Accepted", afterKey, got, why)
	}
	assertNoShowFee(t, ctx, conn, booking2, 500)

	// Tombstones last in the page: moved again, to a studio sorting before both.
	beforeKey := nsfMoveClassTo(t, ctx, conn, sessionKey, afterKey, beforeID, 750)
	booker3 := seedIdentity(t, ctx, conn, "BBWELLNSFPQLBKRKMNPQ")
	booking3, outcome := createBooking(t, ctx, conn, cp, cons, "wdnsfpolbookg3000001", sessionKey, booker3, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("third CreateBooking = %v, want Accepted", outcome)
	}
	if got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfpolmark00000003", booking3, sessionKey, nil); got != processor.OutcomeAccepted {
		t.Fatalf("noShow mark on the class moved to %s = %v (%s), want Accepted", beforeKey, got, why)
	}
	assertNoShowFee(t, ctx, conn, booking3, 750)
}

// TestSetBookingAttendance_StudioPolicyZeroBillsNothing: a studio whose
// recorded policy is 0 writes no fee at all — the same shape the Weaver
// sweep's explicit json:0 leaves, so wellnessNoShowSettlement has nothing to
// bill.
func TestSetBookingAttendance_StudioPolicyZeroBillsNothing(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "nsfzero")

	_, sessionKey, bookingKey := nsfBookedSeat(t, ctx, conn, cp, cons, "wdnsfzer",
		map[string]any{"name": "Free Room", "noShowFeeCents": 0}, "BBWELLNSFZERBKRHJKMN")
	if got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfzermark00000001", bookingKey, sessionKey, nil); got != processor.OutcomeAccepted {
		t.Fatalf("noShow mark under a 0 policy = %v (%s), want Accepted", got, why)
	}
	assertNoShowFee(t, ctx, conn, bookingKey, -1)
}

// TestSetBookingAttendance_NoPolicyBillsDefault: a studio with no recorded
// policy bills the documented 2500 default.
func TestSetBookingAttendance_NoPolicyBillsDefault(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "nsfdefault")

	_, sessionKey, bookingKey := nsfBookedSeat(t, ctx, conn, cp, cons, "wdnsfdef",
		map[string]any{"name": "Quiet Room"}, "BBWELLNSFDEFBKRHJKMN")
	if got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfdefmark00000001", bookingKey, sessionKey, nil); got != processor.OutcomeAccepted {
		t.Fatalf("noShow mark under no policy = %v (%s), want Accepted", got, why)
	}
	assertNoShowFee(t, ctx, conn, bookingKey, 2500)
}

// TestSetBookingAttendance_ExplicitZeroOverridesPolicy: a payload fee wins
// over the studio's policy in both directions — 0 (the desk's waiver, the
// sweep's documentation-lapse signal) bills nothing at a $10 studio, and a
// positive payload fee bills at a fee-free studio.
func TestSetBookingAttendance_ExplicitZeroOverridesPolicy(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "nsfoverride")

	_, sessionKey, bookingKey := nsfBookedSeat(t, ctx, conn, cp, cons, "wdnsfovr",
		map[string]any{"name": "Flow Room", "noShowFeeCents": 1000}, "BBWELLNSFQVRBKRHJKMN")
	if got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfovrmark00000001", bookingKey, sessionKey, 0); got != processor.OutcomeAccepted {
		t.Fatalf("noShow mark with an explicit 0 at a $10 studio = %v (%s), want Accepted", got, why)
	}
	assertNoShowFee(t, ctx, conn, bookingKey, -1)

	_, freeSession, freeBooking := nsfBookedSeat(t, ctx, conn, cp, cons, "wdnsfovf",
		map[string]any{"name": "Free Room", "noShowFeeCents": 0}, "BBWELLNSFQVFBKRHJKMN")
	if got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfovfmark00000001", freeBooking, freeSession, 750); got != processor.OutcomeAccepted {
		t.Fatalf("noShow mark with an explicit 750 at a fee-free studio = %v (%s), want Accepted", got, why)
	}
	assertNoShowFee(t, ctx, conn, freeBooking, 750)
}

// TestSetBookingAttendance_MalformedStoredPolicyRefused: a recorded policy
// that is not a non-negative whole number (a row that predates the writers'
// validation) is refused InvalidState, never billed and never defaulted; once
// SetStudioProfile repairs it the same mark lands with the repaired fee.
func TestSetBookingAttendance_MalformedStoredPolicyRefused(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "nsfmalformed")

	studioKey, sessionKey, bookingKey := nsfBookedSeat(t, ctx, conn, cp, cons, "wdnsfbad",
		map[string]any{"name": "Flow Room"}, "BBWELLNSFBADBKRHJKMN")
	for i, bad := range []any{"twenty", -100.0, 12.5} {
		seedAspect(t, ctx, conn, studioKey, "profile", "studioProfile", map[string]any{"name": "Flow Room", "noShowFeeCents": bad})
		got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfbadmark0000000"+string(rune('1'+i)), bookingKey, sessionKey, nil)
		if got != processor.OutcomeRejected {
			t.Fatalf("noShow mark under a stored policy of %v = %v, want Rejected", bad, got)
		}
		if !strings.Contains(why, "InvalidState") || !strings.Contains(why, "noShowFeeCents") {
			t.Errorf("stored policy %v: refused with %q, want InvalidState naming noShowFeeCents", bad, why)
		}
		if got, _ := attendanceStatus(t, ctx, conn, bookingKey)["value"].(string); got != "booked" {
			t.Fatalf("status.value = %q after the refused mark, want booked (unchanged)", got)
		}
	}

	// The refusal points at the repair; the repaired policy then bills.
	if got, why := setStudioProfileAs(t, ctx, conn, cp, cons, "wdnsfbadrepair000001", studioKey, domainActorKey,
		map[string]any{"noShowFeeCents": 1500}); got != processor.OutcomeAccepted {
		t.Fatalf("SetStudioProfile repairing the policy = %v (%s), want Accepted", got, why)
	}
	if got, why := markNoShowWithFee(t, ctx, conn, cp, cons, "wdnsfbadmark00000009", bookingKey, sessionKey, nil); got != processor.OutcomeAccepted {
		t.Fatalf("noShow mark after the repair = %v (%s), want Accepted", got, why)
	}
	assertNoShowFee(t, ctx, conn, bookingKey, 1500)
}
