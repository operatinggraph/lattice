package leasesigning

// Rule-engine proof of the leaseApplicationComplete executed-lease document
// chain: the missing_leaseDoc gap opens on signing and closes on a completed
// docGen outcome (terminal on a failed one, suppressed while an async call is
// pending); the pointer columns project off the completed outcome; the
// missing_leaseDocAttach gap opens on a completed-but-unanchored document and
// closes when the signedLease attachment lands. The docGen fan is providedTo
// the LEASEAPP (distinct from the identity fan) and never disturbs the
// one-row-per-anchor guard or the identity-family gaps.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// signedApp builds the minimal signed-application fixture: a leaseapp with an
// applicant and a .signature aspect (the docGen chain's opening condition).
func signedApp(t *testing.T, f *lensFixture) {
	t.Helper()
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-07-01T12:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
}

// docGenClaim adds a docGen claim providedTo the APPLICATION (name is the
// fixture handle; the vertex keys under `service` with the docGen envelope
// class, exactly what CreateLeaseDocInstance mints).
func docGenClaim(t *testing.T, f *lensFixture, name string) {
	t.Helper()
	f.vtxWithClass(t, name, "service", "service.docGen.instance")
	f.edge(t, "providedTo", name, "app")
}

// completedDocPointer is the pointer set RecordLeaseDocOutcome copies onto a
// completed .outcome.
var completedDocPointer = map[string]any{
	"status": "completed", "completedAt": "2026-07-01T12:00:05Z",
	"digest": "SHA-256=abc123", "size": 1264, "contentType": "text/plain; charset=utf-8",
	"storeName": "dgStoreNanoXyz", "filename": "signed-lease-leaseapp.test.txt",
}

// attachedLeaseDoc layers the CONVERGED executed-lease document chain onto a
// signed fixture: a docGen claim providedTo the app, its completed
// pointer-carrying .outcome, and the anchored signedLease object. A signed
// application's done-state includes the document (missing_leaseDoc /
// missing_leaseDocAttach both closed), so fixtures probing OTHER terminal
// states (landlord decision, listing flip, freshness) layer this in.
func attachedLeaseDoc(t *testing.T, f *lensFixture) {
	t.Helper()
	docGenClaim(t, f, "dgDone")
	f.aspect(t, "dgDone", "outcome", "leaseDocOutcome", completedDocPointer)
	f.vtx(t, "dgDoneObj", "object")
	f.edge(t, "signedLease", "dgDoneObj", "app")
}

// TestLeaseApplicationComplete_LeaseDocGapOpensOnSigning: unsigned → no doc
// gap; signed → missing_leaseDoc opens (and folds into violating), the attach
// gap stays closed (nothing produced yet), the pointers project null.
func TestLeaseApplicationComplete_LeaseDocGapOpensOnSigning(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.edge(t, "applicationFor", "app", "alice")

	v := f.project(t, "app")[0].Values
	require.Equal(t, false, v["missing_leaseDoc"], "an unsigned application needs no document")
	require.Equal(t, false, v["missing_leaseDocAttach"])

	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-07-01T12:00:00Z"})
	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v = rows[0].Values
	require.Equal(t, true, v["missing_leaseDoc"], "signing opens the document-generation gap")
	require.Equal(t, false, v["missing_leaseDocAttach"], "nothing to anchor before an outcome exists")
	require.Equal(t, false, v["inflight_docGen"])
	require.Equal(t, false, v["declined_docGen"])
	require.Equal(t, false, v["leaseDocAttached"])
	require.Nil(t, v["docStoreName"], "no pointers before a completed outcome")
	require.Equal(t, true, v["violating"], "the open doc gap folds into violating (Weaver dispatches only violating rows)")
}

// TestLeaseApplicationComplete_DocGenClaimWithoutOutcome: the sync window — a
// claim exists, no outcome yet. The gap stays open (Weaver's dispatch mark +
// the claimId-stable Loom instanceId absorb the window); the pending column
// stays false (no .dispatch marker — the sync adapter never writes one).
func TestLeaseApplicationComplete_DocGenClaimWithoutOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	signedApp(t, f)
	docGenClaim(t, f, "dg1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "the docGen fan must not multiply the anchor")
	v := rows[0].Values
	require.Equal(t, true, v["missing_leaseDoc"])
	require.Equal(t, false, v["inflight_docGen"], "a sync claim with no .dispatch marker is not pending")
	require.Equal(t, false, v["missing_leaseDocAttach"])
}

// TestLeaseApplicationComplete_DocGenPending_SuppressesGap: an async vendor's
// .dispatch marker (no outcome) closes the gap while the call is in flight —
// no re-dispatch — and reads as inflight_docGen for the FE.
func TestLeaseApplicationComplete_DocGenPending_SuppressesGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	signedApp(t, f)
	docGenClaim(t, f, "dg1")
	f.aspect(t, "dg1", "dispatch", "leaseServiceDispatchMarker", map[string]any{
		"vendorRef": "vendor-dg-1", "adapter": "docGen", "replyOp": "RecordLeaseDocOutcome",
	})

	v := f.project(t, "app")[0].Values
	require.Equal(t, true, v["inflight_docGen"], "a .dispatch marker with no outcome is a pending call")
	require.Equal(t, false, v["missing_leaseDoc"], "no re-dispatch while the vendor call is in flight")
	require.Equal(t, false, v["missing_leaseDocAttach"])
}

// TestLeaseApplicationComplete_CompletedOutcome_OpensAttachGap: a completed
// outcome closes generation, projects the pointer columns (the §10.8
// AttachObject params), and opens the attach gap.
func TestLeaseApplicationComplete_CompletedOutcome_OpensAttachGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	signedApp(t, f)
	docGenClaim(t, f, "dg1")
	f.aspect(t, "dg1", "outcome", "leaseDocOutcome", completedDocPointer)

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_leaseDoc"], "a completed outcome closes generation")
	require.Equal(t, true, v["missing_leaseDocAttach"], "a produced-but-unanchored document opens the attach gap")
	require.Equal(t, true, v["violating"], "the attach gap folds into violating")
	require.Equal(t, "dgStoreNanoXyz", v["docStoreName"])
	require.Equal(t, "signed-lease-leaseapp.test.txt", v["docFilename"])
	require.Equal(t, "text/plain; charset=utf-8", v["docContentType"])
	require.Equal(t, "SHA-256=abc123", v["docDigest"])
	require.EqualValues(t, 1264, v["docSize"])
	require.Equal(t, false, v["leaseDocAttached"])
}

// TestLeaseApplicationComplete_Attached_ClosesChain: the signedLease
// attachment (the object→owner link AttachObject commits) closes the attach
// gap; a detach re-opens it (self-healing re-attach).
func TestLeaseApplicationComplete_Attached_ClosesChain(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	signedApp(t, f)
	docGenClaim(t, f, "dg1")
	f.aspect(t, "dg1", "outcome", "leaseDocOutcome", completedDocPointer)
	f.vtx(t, "leaseDocObj", "object")
	f.edge(t, "signedLease", "leaseDocObj", "app")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "the attachment fan must not multiply the anchor")
	v := rows[0].Values
	require.Equal(t, false, v["missing_leaseDocAttach"], "the anchored artifact closes the chain")
	require.Equal(t, true, v["leaseDocAttached"])
	require.Equal(t, false, v["missing_leaseDoc"])
	require.Equal(t, "dgStoreNanoXyz", v["docStoreName"], "the pointers keep projecting for the display path")
}

// TestLeaseApplicationComplete_FailedRender_Terminal: a failed outcome is the
// terminal declined disposition — the gap does NOT re-open (no auto-retry; a
// re-generation is a fresh manual StartLoomPattern), and nothing anchors.
func TestLeaseApplicationComplete_FailedRender_Terminal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	signedApp(t, f)
	docGenClaim(t, f, "dg1")
	f.aspect(t, "dg1", "outcome", "leaseDocOutcome", map[string]any{
		"status": "failed", "completedAt": "2026-07-01T12:00:05Z",
	})

	v := f.project(t, "app")[0].Values
	require.Equal(t, true, v["declined_docGen"], "a standing failed render reads as declined")
	require.Equal(t, false, v["missing_leaseDoc"], "a failed render is terminal — no auto-retry")
	require.Equal(t, false, v["missing_leaseDocAttach"], "nothing to anchor off a failed render")
	require.Nil(t, v["docStoreName"])
}

// TestLeaseApplicationComplete_DocGenIsolatedFromIdentityFamilies: the docGen
// fan (providedTo the app) and the identity families (providedTo the
// applicant) discriminate cleanly — a completed bgcheck neither closes the doc
// gap nor picks up docGen pointers, and a completed docGen outcome leaves
// missing_bgcheck open.
func TestLeaseApplicationComplete_DocGenIsolatedFromIdentityFamilies(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	signedApp(t, f)
	// A fresh completed bgcheck providedTo the APPLICANT.
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
	f.edge(t, "providedTo", "bg1", "alice")
	// A completed docGen claim providedTo the APPLICATION.
	docGenClaim(t, f, "dg1")
	f.aspect(t, "dg1", "outcome", "leaseDocOutcome", completedDocPointer)

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "both fans aggregated — still one row per anchor")
	v := rows[0].Values
	require.Equal(t, false, v["missing_bgcheck"], "the bgcheck outcome closes ITS gap")
	require.Equal(t, false, v["missing_leaseDoc"], "the docGen outcome closes ITS gap")
	require.Equal(t, true, v["missing_payment"], "no payment instance — its gap is untouched by either fan")
	require.Equal(t, true, v["missing_leaseDocAttach"])
	require.Equal(t, "dgStoreNanoXyz", v["docStoreName"], "pointers come from the docGen outcome only")
}
