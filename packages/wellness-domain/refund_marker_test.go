// wellness-domain integration test proving CancelBooking mints a
// wellnessrefund marker when the booking being cancelled already carries a
// posted settlesClassPrice charge. wellness-ledger is not installed in this
// harness (wellness-domain has no dependency on it), so the charge
// transaction + its postedTo/settlesClassPrice links are seeded directly —
// the exact shape WellnessDebitAccount{priceBookingRef} would have left,
// mirroring the raw-seed idiom seedVertex/seedLink already use in
// integration_test.go for cross-package known-key reads.
package wellnessdomain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seedAspect directly seeds an aspect document — the mirror of seedVertex/
// seedLink for the one shape neither covers.
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexKey, localName, class string, data map[string]any) {
	t.Helper()
	key := vertexKey + "." + localName
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"vertexKey": vertexKey, "localName": localName, "data": data,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed aspect %s: %v", key, err)
	}
}

// submitCancelBooking dispatches CancelBooking with the same declared reads
// TestCancelBooking_ReleasesSeatForNextClaimant uses, and asserts Accepted.
func submitCancelBooking(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, reqID, bookingKey, sessionKey string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status", sessionKey + ".schedule",
			forSessionLnkKey(t, bookingKey, sessionKey),
		}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestCancelBooking_MintsRefundMarkerWhenAlreadyCharged proves the core
// mechanism this fire adds: cancelling a booking whose class-price charge
// already posted mints a wellnessrefund marker (root {} + .detail aspect)
// carrying the exact accountKey/amountCents the original charge posted,
// plus a reverses link back to it — the marker wellness-ledger's
// wellnessRefundSettlement lens converges into a WellnessCreditAccount.
func TestCancelBooking_MintsRefundMarkerWhenAlreadyCharged(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "refundmarker")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrefundstudio000001", "Priced Studio")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdrefundsession000001", studioKey, "Priced Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}

	bookerKey := seedIdentity(t, ctx, conn, "BBWELLREFUNDBKR1HJKM")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdrefundbooking000001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
	}
	_, bookID, _ := substrate.ParseVertexKey(bookingKey)

	// Seed the charge exactly as WellnessDebitAccount{priceBookingRef} would
	// have left it — a wellnesstransaction + .entry aspect + postedTo (→ a
	// seeded wellnessaccount) + settlesClassPrice (→ this booking).
	acctKey := "vtx.wellnessaccount.BBWELLREFUNDACT1HJKM"
	seedVertex(t, ctx, conn, acctKey, "wellnessaccount", nil)
	_, acctID, _ := substrate.ParseVertexKey(acctKey)

	txKey := "vtx.wellnesstransaction.BBWELLREFUNDTXN1HJKM"
	seedVertex(t, ctx, conn, txKey, "wellnesstransaction", nil)
	_, txID, _ := substrate.ParseVertexKey(txKey)
	seedAspect(t, ctx, conn, txKey, "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": 1500.0, "postedAt": "2026-07-08T08:00:00Z",
	})
	seedLink(t, ctx, conn, "lnk.wellnesstransaction."+txID+".postedTo.wellnessaccount."+acctID,
		txKey, acctKey, "postedTo", "postedTo")
	seedLink(t, ctx, conn, "lnk.wellnesstransaction."+txID+".settlesClassPrice.booking."+bookID,
		txKey, bookingKey, "settlesClassPrice", "settlesClassPrice")

	cancelReqID := testutil.GenReqID("wdrefundcancel000001")
	submitCancelBooking(t, ctx, conn, cp, cons, cancelReqID, bookingKey, sessionKey)

	refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(cancelReqID)
	if !keyExists(t, ctx, conn, refundKey) {
		t.Fatalf("wellnessrefund marker must exist: %s", refundKey)
	}
	detail := readDoc(t, ctx, conn, refundKey+".detail")
	data, _ := detail["data"].(map[string]any)
	if data["accountKey"] != acctKey {
		t.Fatalf("refund detail accountKey = %v, want %v", data["accountKey"], acctKey)
	}
	if data["amountCents"] != 1500.0 {
		t.Fatalf("refund detail amountCents = %v, want 1500", data["amountCents"])
	}
	if data["bookingKey"] != bookingKey {
		t.Fatalf("refund detail bookingKey = %v, want %v", data["bookingKey"], bookingKey)
	}

	_, refundID, _ := substrate.ParseVertexKey(refundKey)
	reversesLnk := "lnk.wellnessrefund." + refundID + ".reverses.wellnesstransaction." + txID
	if !keyExists(t, ctx, conn, reversesLnk) {
		t.Fatalf("reverses link must exist: %s", reversesLnk)
	}
}

// TestCancelBooking_NoRefundMarkerWhenNeverCharged proves the negative case:
// cancelling a priced booking that was NEVER charged mints no wellnessrefund
// marker at all — the tombstoned-booking self-resolution this fire's design
// relies on (wellnessClassPriceSettlement's own MATCH stops matching a
// cancelled booking, so no charge — and therefore no refund — ever happens).
func TestCancelBooking_NoRefundMarkerWhenNeverCharged(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "refundmarkernone")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdnorefundstudio00001", "Priced Studio Two")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdnorefundsession0001", studioKey, "Priced Flow Two", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}

	bookerKey := seedIdentity(t, ctx, conn, "BBWELLNXREFUNDBKRHJK")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdnorefundbooking0001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
	}

	cancelReqID := testutil.GenReqID("wdnorefundcancel00001")
	submitCancelBooking(t, ctx, conn, cp, cons, cancelReqID, bookingKey, sessionKey)

	refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(cancelReqID)
	if keyExists(t, ctx, conn, refundKey) {
		t.Fatalf("no wellnessrefund marker should exist for a never-charged booking: %s", refundKey)
	}
}
