// RecordCapabilityInstallReceipt — the durable binding between an approved
// capability proposal and the ONE install/upgrade commit that produced its
// package (capability-proposal-install-receipt-design.md §2). The apply path
// submits it between the real F-004 install/upgrade and
// MarkCapabilityProposalApplied, so recovery names the package the proposal
// actually produced instead of matching on name+version, which any other
// installer's package satisfies equally well.
//
// Every refusal below is proved twice: once as the refusal, and once with the
// single offending condition corrected, so a green negative can never be a
// setup that failed to reach the guard at all.
package capabilityauthor_test

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

// rcptSubmittedAt is the envelope submit time every receipt op below carries.
// It is deliberately NOT a wall clock: .install.recordedAt must be the
// envelope's authoritative stamp (op.submittedAt), the same discipline
// .review.reviewedAt/appliedAt already follow, so a replayed or delayed
// commit records when the operator submitted rather than when the script ran.
const rcptSubmittedAt = "2026-02-03T04:05:06Z"

// rcptNanoIDPad supplies filler from the Contract #1 NanoID alphabet (no I, l,
// O or 0) so a short, readable case tag folds into a valid 20-char id.
const rcptNanoIDPad = "HJKMNPQRSTUVWXYZHJKMNPQRSTUVWXYZ"

func rcptNanoID(t *testing.T, stem string) string {
	t.Helper()
	if len(stem) > 20 {
		t.Fatalf("receipt fixture stem %q is longer than a 20-char NanoID", stem)
	}
	// A stem carrying I, l, O or 0 mints a key the kernel refuses at commit,
	// and the op is then rejected with no script error to read — a fixture
	// fault that reads exactly like a guard firing. Name it here instead.
	for _, bad := range []string{"I", "l", "O", "0"} {
		if strings.Contains(stem, bad) {
			t.Fatalf("receipt fixture stem %q carries %q, which is not in the Contract #1 NanoID alphabet", stem, bad)
		}
	}
	return (stem + rcptNanoIDPad)[:20]
}

func rcptID(t *testing.T, tag string) string     { t.Helper(); return rcptNanoID(t, "CARcpt"+tag) }
func rcptHandle(t *testing.T, tag string) string { t.Helper(); return rcptNanoID(t, "CAHNDR"+tag) }
func rcptPkgKey(t *testing.T, tag string) string {
	t.Helper()
	return "vtx.package." + rcptNanoID(t, "CAPkg"+tag)
}

// receiptEnv builds the RecordCapabilityInstallReceipt op. The payload arrives
// as a raw map so a case can omit or malform a field a typed parameter list
// would always supply.
func receiptEnv(reqID string, payload map[string]any) *processor.OperationEnvelope {
	b, _ := json.Marshal(payload)
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordCapabilityInstallReceipt",
		Actor:         capStaffActorKey,
		SubmittedAt:   rcptSubmittedAt,
		Class:         "capabilityproposal",
		Payload:       json.RawMessage(b),
	}
}

// receiptPayload is the op's whole payload — the three required fields.
func receiptPayload(proposalID, packageKey, installRequestID string) map[string]any {
	return map[string]any{
		"proposalId":       proposalID,
		"packageKey":       packageKey,
		"installRequestId": installRequestID,
	}
}

// driveReceipt submits one receipt and drives it to the wanted outcome,
// returning the requestId so a caller can read the emitted event back off the
// committed outbox aspect. tag makes the requestId unique (testutil.GenReqID is
// deterministic in its label), so a genuine second attempt against the same
// proposal is a fresh op rather than a Contract #4 redelivery collapse.
func driveReceipt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag string, payload map[string]any, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID("CARcpt" + tag)
	driveReceiptAs(t, ctx, conn, cp, cons, reqID, payload, want)
	return reqID
}

// driveReceiptAs submits under a CALLER-CHOSEN requestId, for the redelivery
// case where the whole point is that the two submissions share one.
func driveReceiptAs(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, reqID string, payload map[string]any, want processor.MessageOutcome) {
	t.Helper()
	testutil.PublishOp(t, conn, receiptEnv(reqID, payload))
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// seedPackageManifest writes a vtx.package.<id> root plus its .manifest aspect
// straight into Core KV — the fixture idiom internal/pkgmgr's own
// seedInstalledManifest uses. The receipt guard reads exactly two things off a
// manifest, isDeleted and data.name, so a fixture that varies precisely those
// isolates the guard from the incidental preconditions of a real install. A
// tombstoned manifest RETAINS its prior document, name included, which is what
// makes it the honest test of whether the alive() filter runs at all.
// TestCapAuthor_Receipt_BindsRealInstall below binds against a genuinely
// installed package instead, so the two together cover both ends.
func seedPackageManifest(t *testing.T, ctx context.Context, conn *substrate.Conn, packageKey, name string, tombstoned bool) {
	t.Helper()
	seedPackageRoot(t, ctx, conn, packageKey, false)
	manifest, _ := json.Marshal(map[string]any{
		"class": "manifest", "isDeleted": tombstoned,
		"vertexKey": packageKey, "localName": "manifest",
		"data": map[string]any{
			"name": name, "version": "0.1.0",
			"declaredKeys": []string{packageKey, packageKey + ".manifest"},
		},
	})
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, packageKey+".manifest", manifest); err != nil {
		t.Fatalf("seed %s.manifest: %v", packageKey, err)
	}
}

// seedPackageRoot writes the vtx.package.<id> root envelope, live or
// tombstoned. It is separate from the manifest so a case can vary the liveness
// of exactly one of the two keys the guard reads.
func seedPackageRoot(t *testing.T, ctx context.Context, conn *substrate.Conn, packageKey string, tombstoned bool) {
	t.Helper()
	root, _ := json.Marshal(map[string]any{"class": "package", "isDeleted": tombstoned, "data": map[string]any{}})
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, packageKey, root); err != nil {
		t.Fatalf("seed %s: %v", packageKey, err)
	}
}

// requireNoInstallAspect asserts a refused receipt wrote nothing at all.
func requireNoInstallAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey string) {
	t.Helper()
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, proposalKey+".install"); err == nil {
		t.Fatalf("%s.install exists — a refused receipt must write nothing", proposalKey)
	}
}

// assertInstallAspect reads back the whole receipt: all three data fields,
// plus the aspect's own envelope shape (Contract #1's 4-segment aspect key
// with its vertexKey/localName back-pointers).
func assertInstallAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalKey, wantPackageKey, wantInstallRequestID string) {
	t.Helper()
	doc := readDoc(t, ctx, conn, proposalKey+".install")
	if got, _ := doc["class"].(string); got != "capabilityAuthor.install" {
		t.Errorf(".install class = %q, want capabilityAuthor.install", got)
	}
	if got, _ := doc["vertexKey"].(string); got != proposalKey {
		t.Errorf(".install vertexKey = %q, want %q", got, proposalKey)
	}
	if got, _ := doc["localName"].(string); got != "install" {
		t.Errorf(".install localName = %q, want install", got)
	}
	data, _ := doc["data"].(map[string]any)
	if got, _ := data["packageKey"].(string); got != wantPackageKey {
		t.Errorf(".install.packageKey = %q, want %q", got, wantPackageKey)
	}
	if got, _ := data["installRequestId"].(string); got != wantInstallRequestID {
		t.Errorf(".install.installRequestId = %q, want %q", got, wantInstallRequestID)
	}
	if got, _ := data["recordedAt"].(string); got != rcptSubmittedAt {
		t.Errorf(".install.recordedAt = %q, want the envelope submit time %q (never a wall clock read inside the script)", got, rcptSubmittedAt)
	}
}

// rcptProposalInState drives one proposal through the IDENTICAL setup every
// other row uses and leaves it in `state`, so a refusal below can only be about
// the state itself. packageKey is used only by the "applied" row, which needs a
// real mark-applied to get there.
func rcptProposalInState(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, state, packageName, packageKey string) (string, string) {
	t.Helper()
	proposalID := rcptID(t, tag)
	proposalKey := drivePendingProposalForApply(t, ctx, conn, cp, cons, tag, proposalID, rcptHandle(t, tag), packageName)
	switch state {
	case "pending":
		// drivePendingProposalForApply already leaves it pending.
	case "rejected":
		driveReview(t, ctx, conn, cp, cons, tag, proposalID, "reject", nil, processor.OutcomeAccepted)
	case "invalid":
		// An approve carrying a non-"valid" fresh verdict fail-closes to invalid.
		driveReview(t, ctx, conn, cp, cons, tag, proposalID, "approve", map[string]any{"state": "stale"}, processor.OutcomeAccepted)
	case "approved":
		driveReview(t, ctx, conn, cp, cons, tag, proposalID, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
	case "applied":
		driveReview(t, ctx, conn, cp, cons, tag, proposalID, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)
		driveApply(t, ctx, conn, cp, cons, tag, proposalID, packageKey, "REQ-mark-"+tag, processor.OutcomeAccepted)
	default:
		t.Fatalf("unknown review state %q", state)
	}
	if got := reviewState(t, ctx, conn, proposalKey); got != state {
		t.Fatalf("precondition: review.state = %q, want %q", got, state)
	}
	return proposalID, proposalKey
}

// TestCapAuthor_Receipt_BindsRealInstall: the whole point of the op, against a
// genuinely installed package. An approved proposal is applied through the real
// F-004 path, and the receipt records THAT apply's own packageKey and
// installRequestId on a create-only .install aspect — leaving review.state
// alone, because the receipt records provenance and MarkCapabilityProposalApplied
// is what closes the loop.
//
// Both stored values come from the live ApplyResult, never from a literal. A
// literal would let the whole receipt-threading chain
// (Installer.Install/submitUpgradeOp → InstallResult/UpgradeResult →
// ApplyResult.InstallRequestID) be deleted with this test still green, while in
// production the dispatchers would see an empty installRequestId, take their
// early-out, and write no receipt at all.
func TestCapAuthor_Receipt_BindsRealInstall(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rcpt-real")

	proposalID := rcptID(t, "Rea")
	pk := drivePendingProposalForApply(t, ctx, conn, cp, cons, "rcptrea", proposalID, rcptHandle(t, "Rea"), "ai-lens-receipt-real")
	driveReview(t, ctx, conn, cp, cons, "rcptrea", proposalID, "approve", map[string]any{"state": "valid"}, processor.OutcomeAccepted)

	applyResult := applyRealPackage(t, ctx, conn, pk)
	if applyResult.PackageKey == "" {
		t.Fatalf("ApplyResult.PackageKey is empty")
	}
	if applyResult.InstallRequestID == "" {
		t.Fatalf("ApplyResult.InstallRequestID is empty — the apply reply's requestId never reached ApplyResult, so both dispatchers would early-out and no receipt would ever be written")
	}

	payload := receiptPayload(proposalID, applyResult.PackageKey, applyResult.InstallRequestID)
	reqID := driveReceipt(t, ctx, conn, cp, cons, "Rea", payload, processor.OutcomeAccepted)

	assertInstallAspect(t, ctx, conn, pk, applyResult.PackageKey, applyResult.InstallRequestID)

	// The receipt is provenance, not a state machine: the applied-flip is still
	// MarkCapabilityProposalApplied's, and it has not run.
	if got := reviewState(t, ctx, conn, pk); got != "approved" {
		t.Fatalf("review.state = %q, want approved (the receipt flips nothing)", got)
	}
	if got := reviewField(t, ctx, conn, pk, "appliedAt"); got != "" {
		t.Fatalf("appliedAt = %q, want empty (the receipt stamps nothing on .review)", got)
	}

	ev := findEmittedEvent(t, ctx, conn, reqID, "capabilityAuthor.installReceipted")
	if got, _ := ev["proposalKey"].(string); got != pk {
		t.Errorf("event proposalKey = %q, want %q", got, pk)
	}
	if got, _ := ev["packageKey"].(string); got != applyResult.PackageKey {
		t.Errorf("event packageKey = %q, want %q", got, applyResult.PackageKey)
	}
	if got, _ := ev["installRequestId"].(string); got != applyResult.InstallRequestID {
		t.Errorf("event installRequestId = %q, want %q", got, applyResult.InstallRequestID)
	}

	// MarkCapabilityProposalApplied still closes the loop over the very package
	// the receipt named, and stamps the same install pointer, so the two records
	// agree rather than describing two different commits.
	driveApply(t, ctx, conn, cp, cons, "rcptrea", proposalID, applyResult.PackageKey, applyResult.InstallRequestID, processor.OutcomeAccepted)
	if got := reviewState(t, ctx, conn, pk); got != "applied" {
		t.Fatalf("review.state = %q, want applied", got)
	}
	if got := reviewField(t, ctx, conn, pk, "appliedByOp"); got != applyResult.InstallRequestID {
		t.Fatalf("appliedByOp = %q, want %q (the same install the receipt bound)", got, applyResult.InstallRequestID)
	}
	assertInstallAspect(t, ctx, conn, pk, applyResult.PackageKey, applyResult.InstallRequestID)
}

// TestCapAuthor_Receipt_NonApprovedState_Refused: only an APPROVED proposal's
// install may be receipted. Each row drives an otherwise-identical proposal to
// one non-approved state and is refused; the paired approved row proves the
// same setup DOES produce a receipt, so the refusal is the state guard rather
// than a fixture that never reached it.
func TestCapAuthor_Receipt_NonApprovedState_Refused(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rcpt-state")

	const packageName = "ai-lens-receipt-state"
	packageKey := rcptPkgKey(t, "St")
	seedPackageManifest(t, ctx, conn, packageKey, packageName, false)

	// The tags are the fixtures' NanoID stems, so they draw only on the
	// Contract #1 alphabet (no I, l, O or 0) — a stem carrying a banned
	// character mints a key the kernel refuses, and the row would then go red
	// for its fixture rather than for its guard.
	for _, tc := range []struct{ state, badTag, goodTag string }{
		{"pending", "StPend", "GdPend"},
		{"rejected", "StRej", "GdRej"},
		{"invalid", "StNvd", "GdNvd"},
		{"applied", "StApd", "GdApd"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			badID, badKey := rcptProposalInState(t, ctx, conn, cp, cons, tc.badTag, tc.state, packageName, packageKey)
			driveReceipt(t, ctx, conn, cp, cons, tc.badTag,
				receiptPayload(badID, packageKey, "REQ-install-"+tc.badTag), processor.OutcomeRejected)
			requireNoInstallAspect(t, ctx, conn, badKey)
			if got := reviewState(t, ctx, conn, badKey); got != tc.state {
				t.Fatalf("review.state = %q, want %q (unchanged by the refused receipt)", got, tc.state)
			}

			// The positive vector: the one offending condition corrected.
			goodID, goodKey := rcptProposalInState(t, ctx, conn, cp, cons, tc.goodTag, "approved", packageName, packageKey)
			driveReceipt(t, ctx, conn, cp, cons, tc.goodTag,
				receiptPayload(goodID, packageKey, "REQ-install-"+tc.goodTag), processor.OutcomeAccepted)
			assertInstallAspect(t, ctx, conn, goodKey, packageKey, "REQ-install-"+tc.goodTag)
		})
	}
}

// TestCapAuthor_Receipt_UnknownPackage_Refused: packageKey must name a LIVE
// installed package. Two shapes of "not live" — no manifest at all, and a
// TOMBSTONED manifest that retains its prior document (matching name included,
// so only the isDeleted bit separates it from the accepted case). The paired
// second submission flips exactly that bit and is accepted, which is the proof
// the alive() filter runs rather than the name comparison quietly carrying it.
func TestCapAuthor_Receipt_UnknownPackage_Refused(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rcpt-unkpkg")

	const packageName = "ai-lens-receipt-unknown"

	t.Run("noManifest", func(t *testing.T) {
		packageKey := rcptPkgKey(t, "Unk")
		proposalID, pk := rcptProposalInState(t, ctx, conn, cp, cons, "Unk", "approved", packageName, "")

		driveReceipt(t, ctx, conn, cp, cons, "UnkA",
			receiptPayload(proposalID, packageKey, "REQ-install-unk"), processor.OutcomeRejected)
		requireNoInstallAspect(t, ctx, conn, pk)

		seedPackageManifest(t, ctx, conn, packageKey, packageName, false)
		driveReceipt(t, ctx, conn, cp, cons, "UnkB",
			receiptPayload(proposalID, packageKey, "REQ-install-unk"), processor.OutcomeAccepted)
		assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-unk")
	})

	t.Run("tombstonedManifest", func(t *testing.T) {
		packageKey := rcptPkgKey(t, "Tmb")
		proposalID, pk := rcptProposalInState(t, ctx, conn, cp, cons, "Tmb", "approved", packageName, "")

		// The tombstone keeps data.name == packageName: a reader that skipped
		// isDeleted would find a perfectly matching package here.
		seedPackageManifest(t, ctx, conn, packageKey, packageName, true)
		driveReceipt(t, ctx, conn, cp, cons, "TmbA",
			receiptPayload(proposalID, packageKey, "REQ-install-tmb"), processor.OutcomeRejected)
		requireNoInstallAspect(t, ctx, conn, pk)

		seedPackageManifest(t, ctx, conn, packageKey, packageName, false)
		driveReceipt(t, ctx, conn, cp, cons, "TmbB",
			receiptPayload(proposalID, packageKey, "REQ-install-tmb"), processor.OutcomeAccepted)
		assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-tmb")
	})
}

// TestCapAuthor_Receipt_PackageNameMismatch_Refused: a live package whose
// recorded name is not this proposal's own target.packageName is refused — the
// binding must be to the package the proposal targeted, not merely to one that
// exists. Correcting only the manifest's name admits the identical submission.
func TestCapAuthor_Receipt_PackageNameMismatch_Refused(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rcpt-mismatch")

	const packageName = "ai-lens-receipt-mine"
	packageKey := rcptPkgKey(t, "Mism")
	proposalID, pk := rcptProposalInState(t, ctx, conn, cp, cons, "Mism", "approved", packageName, "")

	seedPackageManifest(t, ctx, conn, packageKey, "ai-lens-receipt-someone-else", false)
	driveReceipt(t, ctx, conn, cp, cons, "MismA",
		receiptPayload(proposalID, packageKey, "REQ-install-mism"), processor.OutcomeRejected)
	requireNoInstallAspect(t, ctx, conn, pk)

	seedPackageManifest(t, ctx, conn, packageKey, packageName, false)
	driveReceipt(t, ctx, conn, cp, cons, "MismB",
		receiptPayload(proposalID, packageKey, "REQ-install-mism"), processor.OutcomeAccepted)
	assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-mism")
}

// TestCapAuthor_Receipt_MalformedPackageKey_Refused: the key-shape guard, ahead
// of every read. Only a 3-segment vtx.package.<NanoID> is admitted; the paired
// final row submits the well-formed key against the same live manifest and is
// accepted.
func TestCapAuthor_Receipt_MalformedPackageKey_Refused(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rcpt-keyshape")

	const packageName = "ai-lens-receipt-shape"
	packageKey := rcptPkgKey(t, "Shp")
	seedPackageManifest(t, ctx, conn, packageKey, packageName, false)
	proposalID, pk := rcptProposalInState(t, ctx, conn, cp, cons, "Shp", "approved", packageName, packageKey)

	bare := packageKey[len("vtx.package."):]
	for _, tc := range []struct{ tag, key string }{
		{"ShpType", "vtx.lens." + bare},
		{"ShpShort", "vtx.package"},
		{"ShpAspect", packageKey + ".manifest"},
		{"ShpEmptyType", "vtx.." + bare},
		{"ShpBare", bare},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			driveReceipt(t, ctx, conn, cp, cons, tc.tag,
				receiptPayload(proposalID, tc.key, "REQ-install-shape"), processor.OutcomeRejected)
			requireNoInstallAspect(t, ctx, conn, pk)
		})
	}

	// The positive vector: the same proposal, the same live package, the only
	// difference being a well-formed key.
	driveReceipt(t, ctx, conn, cp, cons, "ShpGood",
		receiptPayload(proposalID, packageKey, "REQ-install-shape"), processor.OutcomeAccepted)
	assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-shape")
}

// TestCapAuthor_Receipt_SecondDifferentReceipt_Refused is the write-once proof.
// The .install aspect is a CREATE and never an update, so the commit batch's
// create-only conditioning (Contract #3 §3.2) rejects a second, DIFFERENT
// receipt for the same proposal wholesale: one proposal binds to exactly one
// install, permanently. The second submission carries a fresh requestId, so the
// Contract #4 tracker cannot be what collapsed it, and the identical payload is
// then accepted against a SIBLING proposal — proving the loser was refused for
// colliding on the key, not for being malformed.
func TestCapAuthor_Receipt_SecondDifferentReceipt_Refused(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rcpt-writeonce")

	const packageName = "ai-lens-receipt-once"
	packageKey := rcptPkgKey(t, "Bind")
	seedPackageManifest(t, ctx, conn, packageKey, packageName, false)

	proposalID, pk := rcptProposalInState(t, ctx, conn, cp, cons, "Bind", "approved", packageName, packageKey)

	first := receiptPayload(proposalID, packageKey, "REQ-install-first")
	driveReceipt(t, ctx, conn, cp, cons, "BindA", first, processor.OutcomeAccepted)
	assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-first")

	second := receiptPayload(proposalID, packageKey, "REQ-install-second")
	driveReceipt(t, ctx, conn, cp, cons, "BindB", second, processor.OutcomeRejected)

	// The first binding survives byte for byte — the loser wrote nothing.
	assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-first")

	// The refused payload is perfectly good against a proposal that has no
	// receipt yet, so the refusal above was the key collision and nothing else.
	siblingID, siblingKey := rcptProposalInState(t, ctx, conn, cp, cons, "Sib", "approved", packageName, packageKey)
	sibling := receiptPayload(siblingID, packageKey, "REQ-install-second")
	driveReceipt(t, ctx, conn, cp, cons, "SibA", sibling, processor.OutcomeAccepted)
	assertInstallAspect(t, ctx, conn, siblingKey, packageKey, "REQ-install-second")
}

// TestCapAuthor_Receipt_IdenticalResubmission_TrackerCollapsed pins the OTHER
// half of the write-once story, and the half the DDL comment actually asserts:
// a redelivered IDENTICAL receipt — same requestId, same payload — never
// reaches the script at all. The Contract #4 requestId tracker collapses it to
// OutcomeDuplicate, so a dispatcher that retries its own submission gets a
// success, not the create-only rejection a fresh requestId would earn. Both
// halves matter: without this, an at-least-once retry of a landed receipt would
// look to the caller exactly like a genuine conflicting second binding.
func TestCapAuthor_Receipt_IdenticalResubmission_TrackerCollapsed(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rcpt-redeliver")

	const packageName = "ai-lens-receipt-redeliver"
	packageKey := rcptPkgKey(t, "Redup")
	seedPackageManifest(t, ctx, conn, packageKey, packageName, false)

	proposalID, pk := rcptProposalInState(t, ctx, conn, cp, cons, "Redup", "approved", packageName, packageKey)

	// One requestId, submitted twice with a byte-identical payload — the shape
	// a dispatcher pinning a deterministic requestId produces on retry.
	reqID := testutil.GenReqID("CARcptRedup")
	payload := receiptPayload(proposalID, packageKey, "REQ-install-redup")
	driveReceiptAs(t, ctx, conn, cp, cons, reqID, payload, processor.OutcomeAccepted)
	assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-redup")

	driveReceiptAs(t, ctx, conn, cp, cons, reqID, receiptPayload(proposalID, packageKey, "REQ-install-redup"), processor.OutcomeDuplicate)

	// Collapsed, not re-run: the stored binding is untouched.
	assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-redup")

	// The positive vector for the tracker itself: a DIFFERENT requestId on the
	// same proposal is not collapsed — it reaches the script and is refused by
	// create-only conditioning. So the duplicate above was the tracker, not the
	// same refusal wearing a different outcome.
	driveReceipt(t, ctx, conn, cp, cons, "RedupFresh", receiptPayload(proposalID, packageKey, "REQ-install-redup"), processor.OutcomeRejected)
	assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-redup")
}

// TestCapAuthor_Receipt_TombstonedPackageRoot_Refused: liveness is checked on
// BOTH keys the console's own livePackageNamed reader checks — the
// vtx.package.<id> root as well as its .manifest aspect. A root tombstoned
// while the manifest still reads live (and still carries the matching name) is
// the exact shape a manifest-only check would admit, binding a proposal
// permanently to a dead package. Flipping only the root's isDeleted bit back
// admits the identical submission, so the refusal is that check and nothing
// else.
func TestCapAuthor_Receipt_TombstonedPackageRoot_Refused(t *testing.T) {
	ctx, conn := setupCapAuthorEnv(t)
	cp, cons := newCapAuthorPipeline(t, ctx, conn, "ca-rcpt-deadroot")

	const packageName = "ai-lens-receipt-deadroot"
	packageKey := rcptPkgKey(t, "Root")
	seedPackageManifest(t, ctx, conn, packageKey, packageName, false)
	proposalID, pk := rcptProposalInState(t, ctx, conn, cp, cons, "Root", "approved", packageName, packageKey)

	seedPackageRoot(t, ctx, conn, packageKey, true)
	driveReceipt(t, ctx, conn, cp, cons, "RootA",
		receiptPayload(proposalID, packageKey, "REQ-install-root"), processor.OutcomeRejected)
	requireNoInstallAspect(t, ctx, conn, pk)

	seedPackageRoot(t, ctx, conn, packageKey, false)
	driveReceipt(t, ctx, conn, cp, cons, "RootB",
		receiptPayload(proposalID, packageKey, "REQ-install-root"), processor.OutcomeAccepted)
	assertInstallAspect(t, ctx, conn, pk, packageKey, "REQ-install-root")
}
