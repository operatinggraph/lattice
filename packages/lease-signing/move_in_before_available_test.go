// MoveInBeforeAvailable vectors through the real install + Processor pipeline
// (design docs/reviews/loftspace-unit-turnover-2026-09-17.md decision 3): a
// requested move-in before the unit's listing.availableFrom is REFUSED at the
// terms' writer (CreateLeaseApplication) and again at their reader
// (DecideLeaseApplication's first approve) — never clamped. External test
// package, mirroring lease_signing_test.go's helpers.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// miaCreateWithTerms submits a dated CreateLeaseApplication (the FE's shape:
// the guard link and the unit's .listing declared optional) and returns the
// outcome, the minted key, and the script's own failure message on a
// rejection.
func miaCreateWithTerms(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, applicantKey, unitKey, moveInDate string) (processor.MessageOutcome, string, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	appID := nanoIDFromRequestID(reqID)
	payload := map[string]any{"applicant": applicantKey, "unit": unitKey, "moveInDate": moveInDate, "leaseTermMonths": 12}
	pb, _ := json.Marshal(payload)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(pb),
		ContextHint: &processor.ContextHint{
			Reads:         []string{applicantKey, unitKey},
			OptionalReads: []string{guardLinkKey(applicantKey, unitKey), unitKey + ".listing"},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, "vtx.leaseapp." + appID, msg
}

// miaDecide submits DecideLeaseApplication with the same declared reads +
// enumerations the `decide` helper uses and returns the outcome + message.
func miaDecide(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, decision string) (processor.MessageOutcome, string) {
	t.Helper()
	hint := decideReadsFor(leaseAppKey, "")
	hint.Enumerations = declaredEnumerationsBound("DecideLeaseApplication", lsActorKey, leaseAppKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "DecideLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-26T10:00:00Z",
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","decision":"` + decision + `"}`),
		ContextHint:   hint,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

// TestCreateLeaseApplication_MoveInBeforeAvailable_Refused: the writer's leg.
// A move-in before the listing's availableFrom is refused with the code; the
// application, its links and its guard are not minted. The equal day (as an
// instant and as a bare date), a later day and a listing carrying a bare
// date are all admitted.
func TestCreateLeaseApplication_MoveInBeforeAvailable_Refused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-mia")

	applicantKey := seedApplicant(t, ctx, conn, "MvAcreateappHJKMNPQR")
	unitKey := seedUnitWithListing(t, ctx, conn, "MvAcreateuntHJKMNPQR", "2026-09-01T00:00:00Z", 12, 2050)

	outcome, appKey, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaCreate01", applicantKey, unitKey, "2026-08-31T00:00:00Z")
	if outcome != processor.OutcomeRejected || !strings.Contains(msg, "MoveInBeforeAvailable:") {
		t.Fatalf("move-in the day before availability: outcome=%v msg=%q, want Rejected MoveInBeforeAvailable", outcome, msg)
	}
	if keyExists(t, ctx, conn, appKey) || keyExists(t, ctx, conn, guardLinkKey(applicantKey, unitKey)) {
		t.Fatalf("a refused application must mint neither the leaseapp nor the (applicant, unit) guard")
	}

	// A bare date the day before normalizes to midnight UTC and is refused the same.
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaCreate02", applicantKey, unitKey, "2026-08-31"); outcome != processor.OutcomeRejected || !strings.Contains(msg, "MoveInBeforeAvailable:") {
		t.Fatalf("bare move-in the day before availability: outcome=%v msg=%q", outcome, msg)
	}

	// The available day itself is admitted (equal is not before), as an instant …
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaCreate03", applicantKey, unitKey, "2026-09-01T00:00:00Z"); outcome != processor.OutcomeAccepted {
		t.Fatalf("move-in ON the available day: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
	// … and, for a different applicant, as a bare date; and a later day too.
	applicant2 := seedApplicant(t, ctx, conn, "MvAcreateap2HJKMNPQR")
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaCreate04", applicant2, unitKey, "2026-09-01"); outcome != processor.OutcomeAccepted {
		t.Fatalf("bare move-in ON the available day: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
	applicant3 := seedApplicant(t, ctx, conn, "MvAcreateap3HJKMNPQR")
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaCreate05", applicant3, unitKey, "2026-10-15T00:00:00Z"); outcome != processor.OutcomeAccepted {
		t.Fatalf("move-in after the available day: outcome=%v msg=%q, want Accepted", outcome, msg)
	}

	// A listing whose availableFrom is a bare date (seed data) compares as its
	// midnight instant: the day before is refused, the day itself admitted.
	bareUnit := seedUnitWithListing(t, ctx, conn, "MvAcreatebarHJKMNPQR", "2026-09-01", 12, 2050)
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaCreate06", applicantKey, bareUnit, "2026-08-31T00:00:00Z"); outcome != processor.OutcomeRejected || !strings.Contains(msg, "MoveInBeforeAvailable:") {
		t.Fatalf("move-in before a bare-dated listing: outcome=%v msg=%q", outcome, msg)
	}
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaCreate07", applicantKey, bareUnit, "2026-09-01T00:00:00Z"); outcome != processor.OutcomeAccepted {
		t.Fatalf("move-in on a bare-dated listing's day: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
}

// TestCreateLeaseApplication_DatedApplicationOnUnlistedUnit_Admitted: a unit
// with no .listing has no availability to floor on (and no rent to fall back
// to) — the dated application is admitted with .terms carrying no
// requestedRent.
func TestCreateLeaseApplication_DatedApplicationOnUnlistedUnit_Admitted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-mia-nolisting")

	applicantKey := seedApplicant(t, ctx, conn, "MvAnojistappHJKMNPQR")
	unitKey := "vtx.unit.MvAnojistuntHJKMNPQR"
	seedVertex(t, ctx, conn, unitKey, "location", map[string]any{})
	outcome, appKey, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaNoList01", applicantKey, unitKey, "2026-01-01T00:00:00Z")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("dated application on an unlisted unit: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
	terms, _ := readDoc(t, ctx, conn, appKey+".terms")["data"].(map[string]any)
	if terms["moveInDate"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("terms.moveInDate = %v, want 2026-01-01T00:00:00Z", terms["moveInDate"])
	}
	if _, has := terms["requestedRent"]; has {
		t.Fatalf("no listing → no rent fallback, got requestedRent=%v", terms["requestedRent"])
	}
}

// TestDecideLeaseApplication_MoveInBeforeAvailable_Refused: the reader's leg.
// The listing's availableFrom is FLOORED after the applicant recorded their
// terms (the tenancyEnd target's FloorListingAvailability), so the reviewed
// move-in now precedes it: the approve is refused with the code, no .decision
// and no .tenancy are written, and a later approve on a fresh application
// dated at the floored day is admitted. A decline on the same application
// carries no such floor.
func TestDecideLeaseApplication_MoveInBeforeAvailable_Refused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "decide-mia")

	applicantKey := seedApplicant(t, ctx, conn, "MvAdecideappHJKMNPQR")
	unitKey := seedUnitWithListing(t, ctx, conn, "MvAdecideuntHJKMNPQR", "2026-08-01T00:00:00Z", 12, 2050)
	appKey := createApplicationWithTerms(t, ctx, conn, cp, cons, applicantKey, unitKey, "2026-09-15T00:00:00Z", 6, nil)
	signLease(t, ctx, conn, cp, cons, "miaDecSign1", appKey, "2026-06-26T09:30:00Z")

	// The unit's availability moves past the reviewed move-in.
	setListingAspect(t, ctx, conn, unitKey, "2026-10-01T00:00:00Z", 12, 2050)

	outcome, msg := miaDecide(t, ctx, conn, cp, cons, "miaDecide01", appKey, "approved")
	if outcome != processor.OutcomeRejected || !strings.Contains(msg, "MoveInBeforeAvailable:") {
		t.Fatalf("approve with a move-in before the floored availability: outcome=%v msg=%q, want Rejected MoveInBeforeAvailable", outcome, msg)
	}
	if keyExists(t, ctx, conn, appKey+".decision") || keyExists(t, ctx, conn, appKey+".tenancy") {
		t.Fatalf("a refused approve must record neither .decision nor .tenancy")
	}

	// A decline is not gated on availability.
	if outcome, msg := miaDecide(t, ctx, conn, cp, cons, "miaDecide02", appKey, "declined"); outcome != processor.OutcomeAccepted {
		t.Fatalf("decline under a floored availability: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
}

// TestDecideLeaseApplication_MoveInAtOrAfterAvailable_Admitted pins the
// positive vectors of the approve-side refusal: a move-in ON the available
// day (as an instant), a later move-in, and a bare application (no .terms —
// leaseStart IS availableFrom, equal by construction) are all approved and
// stamp .tenancy from the reviewed terms, never clamped.
func TestDecideLeaseApplication_MoveInAtOrAfterAvailable_Admitted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "decide-mia-ok")

	// Equal: the listing is floored to exactly the reviewed move-in.
	applicantEq := seedApplicant(t, ctx, conn, "MvAokeqappHJKMNPQRST")
	unitEq := seedUnitWithListing(t, ctx, conn, "MvAokequntHJKMNPQRST", "2026-08-01T00:00:00Z", 12, 2050)
	appEq := createApplicationWithTerms(t, ctx, conn, cp, cons, applicantEq, unitEq, "2026-09-15T00:00:00Z", 6, nil)
	signLease(t, ctx, conn, cp, cons, "miaOkEqSign", appEq, "2026-06-26T09:30:00Z")
	setListingAspect(t, ctx, conn, unitEq, "2026-09-15", 12, 2050)
	if outcome, msg := miaDecide(t, ctx, conn, cp, cons, "miaOkEq01", appEq, "approved"); outcome != processor.OutcomeAccepted {
		t.Fatalf("approve with move-in ON the (bare) available day: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
	if got, _ := readDoc(t, ctx, conn, appEq+".tenancy")["data"].(map[string]any)["leaseStart"].(string); got != "2026-09-15T00:00:00Z" {
		t.Fatalf("tenancy.leaseStart = %q, want the reviewed 2026-09-15T00:00:00Z", got)
	}

	// Later: the reviewed move-in is after availability (the common case).
	applicantLater := seedApplicant(t, ctx, conn, "MvAokjaterapHJKMNPQR")
	unitLater := seedUnitWithListing(t, ctx, conn, "MvAokjaterunHJKMNPQR", "2026-08-01T00:00:00Z", 12, 2050)
	appLater := createApplicationWithTerms(t, ctx, conn, cp, cons, applicantLater, unitLater, "2026-09-15T00:00:00Z", 6, nil)
	signLease(t, ctx, conn, cp, cons, "miaOkLaterSign", appLater, "2026-06-26T09:30:00Z")
	if outcome, msg := miaDecide(t, ctx, conn, cp, cons, "miaOkLater01", appLater, "approved"); outcome != processor.OutcomeAccepted {
		t.Fatalf("approve with move-in after availability: outcome=%v msg=%q, want Accepted", outcome, msg)
	}

	// Bare: no .terms at all — the start is the listing's own date, so the
	// floor can never precede it, whatever it was raised to since.
	applicantBare := seedApplicant(t, ctx, conn, "MvAokbareappHJKMNPQR")
	unitBare := seedUnitWithListing(t, ctx, conn, "MvAokbareuntHJKMNPQR", "2026-08-01T00:00:00Z", 12, 2050)
	appBare := createApplicationForUnit(t, ctx, conn, cp, cons, applicantBare, unitBare)
	signLease(t, ctx, conn, cp, cons, "miaOkBareSign", appBare, "2026-06-26T09:30:00Z")
	setListingAspect(t, ctx, conn, unitBare, "2027-01-31T00:00:00Z", 12, 2050)
	if outcome, msg := miaDecide(t, ctx, conn, cp, cons, "miaOkBare01", appBare, "approved"); outcome != processor.OutcomeAccepted {
		t.Fatalf("approve of a bare application under a floored listing: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
	if got, _ := readDoc(t, ctx, conn, appBare+".tenancy")["data"].(map[string]any)["leaseStart"].(string); got != "2027-01-31T00:00:00Z" {
		t.Fatalf("bare application tenancy.leaseStart = %q, want the floored availableFrom 2027-01-31T00:00:00Z", got)
	}
}

// TestCreateLeaseApplication_MoveInComparesByUTCDay pins the DAY compare at the
// writer: a landlord's datetime control (Facet's SetListing) and the seeds
// store availableFrom as a wall-clock instant, while a move-in is a day (bare,
// or its midnight instant) — a same-day move-in reads BELOW the listing's
// instant yet is the promised day, so it is admitted; the previous day is
// refused.
func TestCreateLeaseApplication_MoveInComparesByUTCDay(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-mia-day")

	unitKey := seedUnitWithListing(t, ctx, conn, "MvAdaycreuntHJKMNPQR", "2026-09-01T14:23:11Z", 12, 2050)
	sameDay := seedApplicant(t, ctx, conn, "MvAdaycreap1HJKMNPQR")
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaDayCre01", sameDay, unitKey, "2026-09-01"); outcome != processor.OutcomeAccepted {
		t.Fatalf("bare same-day move-in against a wall-clock availableFrom: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
	sameDayInstant := seedApplicant(t, ctx, conn, "MvAdaycreap2HJKMNPQR")
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaDayCre02", sameDayInstant, unitKey, "2026-09-01T00:00:00Z"); outcome != processor.OutcomeAccepted {
		t.Fatalf("midnight same-day move-in against a wall-clock availableFrom: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
	previousDay := seedApplicant(t, ctx, conn, "MvAdaycreap3HJKMNPQR")
	if outcome, _, msg := miaCreateWithTerms(t, ctx, conn, cp, cons, "miaDayCre03", previousDay, unitKey, "2026-08-31T23:00:00Z"); outcome != processor.OutcomeRejected || !strings.Contains(msg, "MoveInBeforeAvailable:") {
		t.Fatalf("previous-day move-in: outcome=%v msg=%q, want Rejected MoveInBeforeAvailable", outcome, msg)
	}
}

// TestDecideLeaseApplication_MoveInComparesByUTCDay is the reader's half of the
// day compare: the listing floored to a wall-clock instant on the reviewed
// move-in's own day still approves; floored to the NEXT day it refuses.
func TestDecideLeaseApplication_MoveInComparesByUTCDay(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "decide-mia-day")

	applicantKey := seedApplicant(t, ctx, conn, "MvAdaydecappHJKMNPQR")
	unitKey := seedUnitWithListing(t, ctx, conn, "MvAdaydecuntHJKMNPQR", "2026-08-01T00:00:00Z", 12, 2050)
	appKey := createApplicationWithTerms(t, ctx, conn, cp, cons, applicantKey, unitKey, "2026-09-15", 6, nil)
	signLease(t, ctx, conn, cp, cons, "miaDayDecSign", appKey, "2026-06-26T09:30:00Z")

	setListingAspect(t, ctx, conn, unitKey, "2026-09-16T14:23:11Z", 12, 2050)
	if outcome, msg := miaDecide(t, ctx, conn, cp, cons, "miaDayDec01", appKey, "approved"); outcome != processor.OutcomeRejected || !strings.Contains(msg, "MoveInBeforeAvailable:") {
		t.Fatalf("approve with the listing floored to the next day: outcome=%v msg=%q, want Rejected MoveInBeforeAvailable", outcome, msg)
	}

	setListingAspect(t, ctx, conn, unitKey, "2026-09-15T14:23:11Z", 12, 2050)
	if outcome, msg := miaDecide(t, ctx, conn, cp, cons, "miaDayDec02", appKey, "approved"); outcome != processor.OutcomeAccepted {
		t.Fatalf("approve with a same-day wall-clock availableFrom: outcome=%v msg=%q, want Accepted", outcome, msg)
	}
	if got, _ := readDoc(t, ctx, conn, appKey+".tenancy")["data"].(map[string]any)["leaseStart"].(string); got != "2026-09-15T00:00:00Z" {
		t.Fatalf("tenancy.leaseStart = %q, want the reviewed day's midnight 2026-09-15T00:00:00Z", got)
	}
}
