package identity

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// The driver's whole job before it submits anything is classification: which
// credentials are genuinely orphaned residue of an erasure, which are still
// bound (and so belong to the ordinary sweep), and which have a live owner (and
// so belong to reconcile-bindings, or to nobody). Getting that wrong is what
// would turn a residue cleanup into a tool that retires a live person's sign-in
// method, so it is asserted directly, in dry-run — where nothing is submitted
// and the counts are the only output.
//
// The operation re-checks every one of these conditions itself and refuses on
// each, so what these tests pin is that the driver does not GENERATE work the
// operation will decline — never that the classification is what keeps the sweep
// safe. The safety proof is in packages/identity-domain's own tests, through the
// real Processor commit path.

const (
	residueCredA = "vtx.identity.JresCredAHJKMNPQRSTU"
	residueCredB = "vtx.identity.JresCredBHJKMNPQRSTU"
	residueCredC = "vtx.identity.JresCredCHJKMNPQRSTU"
	residueCredD = "vtx.identity.JresCredDHJKMNPQRSTU"
	residueCredE = "vtx.identity.JresCredEHJKMNPQRSTU"
	residueCredF = "vtx.identity.JresCredFHJKMNPQRSTU"
	residueCredH = "vtx.identity.JresCredHHJKMNPQRSTU"

	residueErasedOwner = "vtx.identity.JresErsdHJKMNPQRSTUV"
	residueLiveOwner   = "vtx.identity.JresLiveHJKMNPQRSTUV"
)

// seedResidueIndex seeds an index vertex AT ITS REAL KEY — the content address
// of its own actorKey, exactly as every writer of this vertex produces it.
//
// The driver checks that agreement before submitting, because the operation
// derives the index key from the payload rather than being handed the scanned
// one: a row seeded at an arbitrary key would be classified "malformed" and
// skipped, and a fixture that used one would be asserting the malformed path
// while claiming to assert classification. Only the malformed case seeds by
// hand, and it does so deliberately.
func seedResidueIndex(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey, ownerKey string, deleted bool) {
	t.Helper()
	// derived-key: test fixture placing a credentialindex vertex at the same
	// content-addressed key the package's own writers compute for it. Nothing is
	// read at this key by any submitter — it is the corpus under test.
	seedIndex(t, ctx, conn, substrate.SHA256NanoID(actorKey), actorKey, ownerKey, deleted)
}

// seedShreddedPiiKey writes the piiKey envelope a bare ShredIdentityKey leaves
// behind — the historical erasure shape whose residue this tool clears, and the
// only erasure fact those subjects carry (no marker was ever written for them).
func seedShreddedPiiKey(t *testing.T, ctx context.Context, conn *substrate.Conn, ownerKey string) {
	t.Helper()
	putDoc(t, ctx, conn, ownerKey+".piiKey", map[string]any{
		"class":     "piiKey",
		"vertexKey": ownerKey,
		"localName": "piiKey",
		"isDeleted": false,
		"data": map[string]any{
			"shredded":   true,
			"shreddedAt": "2026-08-07T08:59:00Z",
		},
	})
}

// seedLivePiiKey writes an UNSHREDDED envelope — a person whose sensitive
// aspects are readable, i.e. not erased at all. The false-positive control: a
// driver keying on the envelope's PRESENCE rather than its shredded flag would
// select every identity that has ever recorded a sensitive aspect.
func seedLivePiiKey(t *testing.T, ctx context.Context, conn *substrate.Conn, ownerKey string) {
	t.Helper()
	putDoc(t, ctx, conn, ownerKey+".piiKey", map[string]any{
		"class":     "piiKey",
		"vertexKey": ownerKey,
		"localName": "piiKey",
		"isDeleted": false,
		"data":      map[string]any{"wrappedDEK": "not-a-real-envelope"},
	})
}

// TestSweepCredentialResidue_ClassifiesEachCase covers every state one pass can
// meet. B and C are the ones that matter: a live edge belongs to the ordinary
// UnbindIdentityCredentials sweep, and a live OWNER belongs to nobody here — a
// tool that counted either as work would be asking the operation to retire a
// credential somebody still signs in with.
func TestSweepCredentialResidue_ClassifiesEachCase(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	seedShreddedPiiKey(t, ctx, conn, residueErasedOwner)
	seedLivePiiKey(t, ctx, conn, residueLiveOwner)

	// A — the residue case: live index, TOMBSTONED edge, erased owner. Exactly
	// what a pre-narrowing ShredIdentityKey left behind.
	seedResidueIndex(t, ctx, conn, residueCredA, residueErasedOwner, false)
	seedBoundTo(t, ctx, conn, residueCredA, residueErasedOwner, true)
	// B — still bound: live index, LIVE edge, erased owner. In scope for
	// UnbindIdentityCredentials, which retires the index and the edge together;
	// clearing the index alone here would leave the edge dangling.
	seedResidueIndex(t, ctx, conn, residueCredB, residueErasedOwner, false)
	seedBoundTo(t, ctx, conn, residueCredB, residueErasedOwner, false)
	// C — live owner AND live credential, edge ABSENT. reconcile-bindings' repair
	// job: the edge should be re-published, not the index retired.
	seedResidueIndex(t, ctx, conn, residueCredC, residueLiveOwner, false)
	// D — live owner AND live credential, edge TOMBSTONED. The retraction
	// reconcile-bindings already declines to touch; this tool leaves it exactly
	// as untouched.
	seedResidueIndex(t, ctx, conn, residueCredD, residueLiveOwner, false)
	seedBoundTo(t, ctx, conn, residueCredD, residueLiveOwner, true)
	// E — the residue case reached through an ABSENT edge rather than a
	// tombstoned one. A hard-removed link reads absent, and the operation treats
	// absent and tombstoned as one answer; so must this.
	seedResidueIndex(t, ctx, conn, residueCredE, residueErasedOwner, false)
	// F — already cleared by an earlier run of this very sweep. Must not be
	// re-submitted: the operation refuses CredentialIndexAlreadyClear, and a
	// sweep that can never report clean is a sweep nobody finishes.
	seedResidueIndex(t, ctx, conn, residueCredF, residueErasedOwner, true)
	// G — a merge's implicit self-credential. No edge can exist for it and the
	// operation's self-loop guard refuses it on every run.
	seedResidueIndex(t, ctx, conn, residueErasedOwner, residueErasedOwner, false)
	// H — the OUTBOUND residue shape: the erased subject is the row's CREDENTIAL
	// and the owner is a LIVE person. The pre-narrowing shred tombstoned boundTo
	// in both directions, so this row is residue on exactly the same terms as A
	// and E, and the erased subject is the one it names in the clear. A driver
	// that asked only about the owner would classify it NotErased and skip half
	// the class forever.
	seedShreddedPiiKey(t, ctx, conn, residueCredH)
	seedResidueIndex(t, ctx, conn, residueCredH, residueLiveOwner, false)

	report, err := sweepCredentialResidue(ctx, conn, testActorKey, true)
	if err != nil {
		t.Fatalf("sweepCredentialResidue: %v", err)
	}
	if report.Scanned != 8 {
		t.Fatalf("scanned = %d, want 8", report.Scanned)
	}
	if report.Submitted != 3 {
		t.Errorf("submitted = %d, want 3 (A, E and H) — the tombstoned-edge, absent-edge and outbound residue shapes", report.Submitted)
	}
	if report.StillBound != 1 {
		t.Errorf("stillBound = %d, want 1 (B) — a live edge belongs to UnbindIdentityCredentials' sweep, which retires the pair together", report.StillBound)
	}
	if report.NotErased != 2 {
		t.Errorf("notErased = %d, want 2 (C and D) — a row whose BOTH endpoints are live must never be counted as work", report.NotErased)
	}
	if report.Tombstoned != 1 {
		t.Errorf("tombstoned = %d, want 1 (F) — an already-cleared index would be refused on every re-run", report.Tombstoned)
	}
	if report.SelfLoop != 1 {
		t.Errorf("selfLoop = %d, want 1 (G) — a merge self-credential would reject on every run", report.SelfLoop)
	}
	if report.Malformed != 0 || report.Vanished != 0 {
		t.Errorf("malformed=%d vanished=%d, want 0/0 — every fixture here is a well-formed, present row", report.Malformed, report.Vanished)
	}
	if report.Rejected != 0 {
		t.Errorf("rejected = %d, want 0 in dry-run", report.Rejected)
	}
	if !report.DryRun {
		t.Error("report does not record that this was a dry run")
	}
}

// TestSweepCredentialResidue_OutboundResidueSelectsOnTheCredential isolates the
// outbound arm from the classification sweep above, so the arm is asserted by a
// test that contains nothing else.
//
// The row is at the hash of the ERASED subject's own key and records them as a
// LIVE person's credential — a merged-away identity folded into its survivor, a
// Scenario-B identity later linked to another. The owner is asserted live via
// its own unshredded envelope, so the only fact that can select this row is the
// credential's erasure. The op's gate is symmetric here; a driver that was not
// would skip the row and, if it ever did submit one, would be refused NotErased.
func TestSweepCredentialResidue_OutboundResidueSelectsOnTheCredential(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	seedLivePiiKey(t, ctx, conn, residueLiveOwner)
	seedShreddedPiiKey(t, ctx, conn, residueCredH)
	seedResidueIndex(t, ctx, conn, residueCredH, residueLiveOwner, false)

	report, err := sweepCredentialResidue(ctx, conn, testActorKey, true)
	if err != nil {
		t.Fatalf("sweepCredentialResidue: %v", err)
	}
	if report.Submitted != 1 || report.NotErased != 0 {
		t.Fatalf("submitted=%d notErased=%d, want 1/0 — an erased CREDENTIAL closes the write path for this row just as an erased owner does",
			report.Submitted, report.NotErased)
	}
}

// TestSweepCredentialResidue_MismatchedIndexKeyIsSkipped — a row whose stored
// actorKey does not hash to the key it lives at.
//
// The operation derives index_key from the PAYLOAD, not from the key the driver
// scanned, so for such a row it would look up a different key, read it absent,
// and refuse CredentialIndexAlreadyClear — on this run and on every re-run,
// with a diagnosis that is not what is wrong. Submitting it can only manufacture
// a permanent, misread rejection, so the driver counts it and skips.
func TestSweepCredentialResidue_MismatchedIndexKeyIsSkipped(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	seedShreddedPiiKey(t, ctx, conn, residueErasedOwner)

	// Seeded by hand at a key that is NOT the content address of its actorKey —
	// the whole point of the fixture, and why it does not go through
	// seedResidueIndex.
	seedIndex(t, ctx, conn, "residHashZZZZZZZZZZZ", residueCredA, residueErasedOwner, false)
	// A well-formed sibling that IS selectable, so the assertion below cannot
	// pass merely because the sweep classified nothing at all.
	seedResidueIndex(t, ctx, conn, residueCredB, residueErasedOwner, false)

	report, err := sweepCredentialResidue(ctx, conn, testActorKey, true)
	if err != nil {
		t.Fatalf("sweepCredentialResidue: %v", err)
	}
	if report.Malformed != 1 {
		t.Errorf("malformed = %d, want 1 — a row whose actorKey does not hash to its own key can never be submitted usefully", report.Malformed)
	}
	if report.Submitted != 1 {
		t.Errorf("submitted = %d, want 1 — the well-formed sibling must still be selected, or this test proves nothing about the skip", report.Submitted)
	}
	if report.Rejected != 0 {
		t.Errorf("rejected = %d, want 0 — the malformed row is skipped, never submitted", report.Rejected)
	}
}

// TestSweepCredentialResidue_ErasureMarkerAlsoSelects — the discriminator is a
// disjunction, and the marker arm must select too. A subject sealed through the
// erasure PATTERN carries the marker; one shredded by a bare submit carries only
// the envelope flag. Keying on either alone would leave one whole population's
// residue unreachable by the tool that exists to reach it.
func TestSweepCredentialResidue_ErasureMarkerAlsoSelects(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	// NO piiKey at all — the marker is the only erasure fact present, so only
	// the marker arm can be what selects this.
	putDoc(t, ctx, conn, residueErasedOwner+".erasureRequested", map[string]any{
		"class":     "erasureRequested",
		"vertexKey": residueErasedOwner,
		"localName": "erasureRequested",
		"isDeleted": false,
		"data":      map[string]any{"requestedAt": "2026-08-07T09:00:00Z"},
	})
	seedResidueIndex(t, ctx, conn, residueCredA, residueErasedOwner, false)
	seedBoundTo(t, ctx, conn, residueCredA, residueErasedOwner, true)

	report, err := sweepCredentialResidue(ctx, conn, testActorKey, true)
	if err != nil {
		t.Fatalf("sweepCredentialResidue: %v", err)
	}
	if report.Submitted != 1 {
		t.Fatalf("submitted = %d, want 1 — a live erasureRequested marker closes the write path on its own", report.Submitted)
	}
}

// TestSweepCredentialResidue_ForeignMarkerClassDoesNotSelect is the marker arm's
// false-positive control. privacy-base's aspect-type DDL gates the CLASS, not
// the key, so any package script can write some other class there. The operation
// checks the class; a driver that checked only presence would select a live
// person and then report a NotErased rejection for them on every run.
func TestSweepCredentialResidue_ForeignMarkerClassDoesNotSelect(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	putDoc(t, ctx, conn, residueLiveOwner+".erasureRequested", map[string]any{
		"class":     "someOtherClass",
		"vertexKey": residueLiveOwner,
		"localName": "erasureRequested",
		"isDeleted": false,
		"data":      map[string]any{"requestedAt": "2026-08-07T09:00:00Z"},
	})
	seedResidueIndex(t, ctx, conn, residueCredA, residueLiveOwner, false)
	seedBoundTo(t, ctx, conn, residueCredA, residueLiveOwner, true)

	report, err := sweepCredentialResidue(ctx, conn, testActorKey, true)
	if err != nil {
		t.Fatalf("sweepCredentialResidue: %v", err)
	}
	if report.Submitted != 0 || report.NotErased != 1 {
		t.Fatalf("submitted=%d notErased=%d, want 0/1 — a foreign class at the marker key must not read as an erasure",
			report.Submitted, report.NotErased)
	}
}

// TestSweepCredentialResidue_EmptyKeyspaceIsClean — a corpus with no credentials
// at all must report zeroes rather than error, or the command is unrunnable on a
// fresh stack.
func TestSweepCredentialResidue_EmptyKeyspaceIsClean(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	report, err := sweepCredentialResidue(ctx, conn, testActorKey, true)
	if err != nil {
		t.Fatalf("sweepCredentialResidue on an empty keyspace: %v", err)
	}
	if report.Scanned != 0 || report.Submitted != 0 {
		t.Fatalf("scanned=%d submitted=%d, want 0/0", report.Scanned, report.Submitted)
	}
}

// TestSweepCredentialResidue_MalformedIndexFailsLoud — an index vertex naming no
// owner cannot be classified, and guessing one would submit a tombstone nothing
// asserts. The driver stops instead of skipping quietly.
//
// It also pins the partial-report contract: the abort still hands back the
// counts the run had reached. On a real keyspace the aborting row can be the
// four-thousandth, after thousands of tombstones have already committed, and a
// driver that returned a nil report would make every one of those commits
// invisible to the operator and leave the re-run unanchored.
func TestSweepCredentialResidue_MalformedIndexFailsLoud(t *testing.T) {
	ctx, conn := setupReconcileEnv(t)

	// A row the sweep classifies BEFORE it aborts, so the partial report has
	// something to be partial about. The keyspace is walked in key order and this
	// row's key is not predictable relative to the bad one, so the assertion
	// below reads Scanned rather than a specific bucket.
	seedShreddedPiiKey(t, ctx, conn, residueErasedOwner)
	seedResidueIndex(t, ctx, conn, residueCredA, residueErasedOwner, false)

	putDoc(t, ctx, conn, credentialIndexPrefix+"residHashXXXXXXXXXXX", map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data":      map[string]any{"actorKey": residueCredA},
	})

	report, err := sweepCredentialResidue(ctx, conn, testActorKey, true)
	if err == nil {
		t.Fatal("want an error for an index vertex with no identityKey, got nil")
	}
	if report == nil {
		t.Fatal("aborted run returned a nil report — the counts a partial run reached are the only thing that makes the abort actionable")
	}
	if report.Scanned == 0 {
		t.Errorf("scanned = 0 on an aborted run that had already read at least one row; the partial report must carry what the run reached")
	}
	if !report.DryRun {
		t.Error("the partial report does not record that this was a dry run")
	}
}

// TestSweepCredentialResidue_ConcurrentClearIsNotAFailure — two operators
// sweeping the same corpus at once.
//
// Both classify the same row as work. The loser's commit is hydrated AFTER the
// winner's tombstone lands, so the operation correctly re-executes to
// CredentialIndexAlreadyClear. That is not a failure: the row IS clear, which is
// the outcome this run wanted. Counting it as a rejection would exit the loser
// non-zero — on a tool whose whole contract is that it can be re-run until it
// reports clean, and run concurrently without either operator having to
// coordinate.
//
// The interleave is produced deterministically, not by racing goroutines: the
// winner's tombstone is written inside the consume handler, which IS the window
// between this run's classification and its commit's hydration.
func TestSweepCredentialResidue_ConcurrentClearIsNotAFailure(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)

	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    testCapKey,
		Actor:                  testActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{testActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "TombstoneOrphanedCredentialIndex", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	})
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        "credresidue-cmd-test",
		Instance:       "credresidue-cmd",
		FilterSubjects: []string{"ops.default"},
	})

	seedShreddedPiiKey(t, ctx, conn, residueErasedOwner)
	seedResidueIndex(t, ctx, conn, residueCredA, residueErasedOwner, false)

	cc, err := cons.Consume(func(m jetstream.Msg) {
		// The OTHER operator's run committing first.
		seedResidueIndex(t, ctx, conn, residueCredA, residueErasedOwner, true)
		cp.HandleMessage(ctx, m)
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	report, err := sweepCredentialResidue(ctx, conn, testActorKey, false)
	if err != nil {
		t.Fatalf("sweepCredentialResidue: %v", err)
	}
	if report.Rejected != 0 || len(report.Failures) != 0 {
		t.Fatalf("rejected=%d failures=%v, want 0/none — the row IS clear, which is what this run wanted; a losing concurrent run must not exit non-zero over it",
			report.Rejected, report.Failures)
	}
	if report.Tombstoned != 1 {
		t.Fatalf("tombstoned = %d, want 1 — an already-clear refusal is an already-handled row, not a failure", report.Tombstoned)
	}
	if report.Submitted != 0 {
		t.Errorf("submitted = %d, want 0 — nothing was committed by this run", report.Submitted)
	}
}
