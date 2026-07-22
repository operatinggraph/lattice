// RecordIdentityPII integration tests for the identity-domain Capability
// Package: the sensitive applicant-PII aspects ssn/dob.
//
// These prove the SHIPPED package — not a hand-seeded fixture:
//   - the ssn/dob aspect-type DDLs install through the real InstallPackage
//     path and reach the DDL cache as ref.Sensitive==true (test 1 / test 5);
//   - RecordIdentityPII writes them as aspects with the identity vertex root
//     left minimal (test 2 — the D5 assertion);
//   - bad ssn/dob formats are rejected with no aspect written (test 3);
//   - a sensitive ssn aspect on a NON-identity vertex is rejected by the
//     UNCHANGED step-6 validator using the real installed DDL (test 4 — the
//     identity-anchoring proof, AC #2).
package identitydomain_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func newPIIPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "pii-" + durable,
	})
}

// freshDDLCache builds and refreshes a DDLCache from the installed Core KV —
// the same load path the running Processor uses. Used to assert the install
// round-trip landed ref.Sensitive (test 1) and to drive the step-6 validator
// against the real installed DDLs (test 4).
func freshDDLCache(t *testing.T, ctx context.Context, conn *substrate.Conn) *processor.DDLCache {
	t.Helper()
	cache := processor.NewDDLCache(conn, testutil.HarnessCoreBucket, testutil.TestLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("DDLCache.Refresh: %v", err)
	}
	return cache
}

// createIdentity drives a CreateUnclaimedIdentity and returns the new
// identity key. Mirrors create_test.go.
func createIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, reqLabel string) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	identityKey := "vtx.identity." + identityIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"PII Applicant ` + reqLabel + `","email":"applicant-` + reqLabel + `@example.com","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return identityKey
}

// recordPIIReads is the known-key read set RecordIdentityPII needs: the target
// identity vertex + its state aspect. (.mergedInto is NOT declared — it is
// absent pre-merge; the merged guard keys off state == "merged".)
func recordPIIReads(identityKey string) *processor.ContextHint {
	return &processor.ContextHint{Reads: []string{
		identityKey,
		identityKey + ".state",
	}}
}

// TestRecordPII_SSNDOBAreSensitive_AfterInstall — the sensitivity proof
// (AC #1 + §4). After the real package install, the DDL cache must carry
// ref.Sensitive==true for both ssn and dob, proving the pkgmgr.DDLSpec
// Sensitive field travelled through build.go's `.sensitive` aspect to the
// cache. A non-PII class (no DDL shipped) is a Lookup miss → not sensitive.
func TestRecordPII_SSNDOBAreSensitive_AfterInstall(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cache := freshDDLCache(t, ctx, conn)

	for _, name := range []string{"ssn", "dob"} {
		ref, ok := cache.Lookup(name)
		if !ok {
			t.Fatalf("DDL cache has no entry for %q after install", name)
		}
		if !ref.Sensitive {
			t.Fatalf("%q ref.Sensitive = false after install; the .sensitive aspect did not reach the cache", name)
		}
		if ref.Kind != "aspectType" {
			t.Errorf("%q Kind = %q, want aspectType", name, ref.Kind)
		}
	}

	// Contrast: "anomalyFlag" has no DDL → Lookup miss → not enforced sensitive.
	if _, ok := cache.Lookup("anomalyFlag"); ok {
		t.Errorf("unexpected DDL for anomalyFlag (should be a Lookup miss)")
	}
}

// TestRecordPII_WritesAspects_RootMinimal_D5 — the write op + D5 (AC #1 +
// invariant b). RecordIdentityPII writes ssn (normalized) + dob as aspects on
// an existing identity; the identity vertex root data stays minimal ({}).
func TestRecordPII_WritesAspects_RootMinimal_D5(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-write")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PIIWrite")

	// Snapshot the identity vertex root before recording PII (D5 baseline).
	preRoot := readVertexRootData(t, ctx, conn, identityKey)
	if len(preRoot) != 0 {
		t.Fatalf("precondition: identity root data not minimal: %v", preRoot)
	}

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIWriteOp"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		ContextHint:   recordPIIReads(identityKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// ssn aspect: stored NORMALIZED to 9 digits.
	ssn := readDecryptedAspectData(t, ctx, conn, identityKey, "ssn")
	if got, _ := ssn["value"].(string); got != "123456789" {
		t.Fatalf("ssn aspect value = %q, want normalized 123456789", got)
	}
	ssnClass := readAspectClass(t, ctx, conn, identityKey+".ssn")
	if ssnClass != "ssn" {
		t.Fatalf("ssn aspect class = %q, want ssn", ssnClass)
	}
	// dob aspect: stored verbatim.
	dob := readDecryptedAspectData(t, ctx, conn, identityKey, "dob")
	if got, _ := dob["value"].(string); got != "1990-01-15" {
		t.Fatalf("dob aspect value = %q, want 1990-01-15", got)
	}
	if c := readAspectClass(t, ctx, conn, identityKey+".dob"); c != "dob" {
		t.Fatalf("dob aspect class = %q, want dob", c)
	}

	// D5: the identity vertex root data is UNCHANGED / minimal — no PII leaked
	// into the vertex root.
	postRoot := readVertexRootData(t, ctx, conn, identityKey)
	if len(postRoot) != 0 {
		t.Fatalf("D5 violation: identity root data is not minimal after RecordIdentityPII: %v", postRoot)
	}

	assertTrackerEvent(t, ctx, conn, env.RequestID, "identity.piiRecorded")

	// The emitted event is PII-free: the faithful EventList persisted in the
	// outbox aspect must not carry the ssn/dob keys, nor leak the ssn digits or
	// dob string anywhere in its payload values. (No outbox consumer runs in the
	// CapabilityPipeline, so the aspect is not tombstoned and reads back.)
	assertEventPIIFree(t, ctx, conn, env.RequestID, "identity.piiRecorded", "123456789", "1990-01-15")
}

// TestRecordPII_AcceptsLeapDayDOB — the calendar-validation positive: a real
// leap-year Feb 29 is accepted and stored verbatim (proves the calendar gate
// admits valid dates, not just rejects bad ones).
func TestRecordPII_AcceptsLeapDayDOB(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-leapday")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PIILeap")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIILeapOp"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","ssn":"123-45-6789","dob":"2000-02-29"}`),
		ContextHint:   recordPIIReads(identityKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	dob := readDecryptedAspectData(t, ctx, conn, identityKey, "dob")
	if got, _ := dob["value"].(string); got != "2000-02-29" {
		t.Fatalf("leap-day dob aspect value = %q, want 2000-02-29", got)
	}
}

// TestRecordPII_ResubmitRejected — re-submission safety: a SECOND
// RecordIdentityPII on an identity whose .ssn/.dob already exist is rejected
// (the aspects are written op:"create" → create-only → the atomic batch
// conflicts), and the original stored values are left UNCHANGED (not clobbered).
func TestRecordPII_ResubmitRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-resubmit")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PIIResub")

	first := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIResub1"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		ContextHint:   recordPIIReads(identityKey),
	}
	testutil.PublishOp(t, conn, first)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Second op with DIFFERENT values must be rejected (create-only conflict).
	second := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIResub2"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T12:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","ssn":"987-65-4321","dob":"1985-12-31"}`),
		ContextHint:   recordPIIReads(identityKey),
	}
	testutil.PublishOp(t, conn, second)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// The original values survive — the second op did not clobber them.
	ssn := readDecryptedAspectData(t, ctx, conn, identityKey, "ssn")
	if got, _ := ssn["value"].(string); got != "123456789" {
		t.Fatalf("ssn aspect value = %q after rejected resubmit, want original 123456789", got)
	}
	dob := readDecryptedAspectData(t, ctx, conn, identityKey, "dob")
	if got, _ := dob["value"].(string); got != "1990-01-15" {
		t.Fatalf("dob aspect value = %q after rejected resubmit, want original 1990-01-15", got)
	}
}

// TestRecordPII_RejectsBadFormats — format validation (AC #1 / Item D). Bad
// ssn/dob are rejected; the aspect keys are absent from Core KV afterward.
func TestRecordPII_RejectsBadFormats(t *testing.T) {
	t.Parallel()
	// suffix makes each case's identity key and op RequestID unique within the
	// shared environment (GenReqID is deterministic in its label, so distinct
	// labels are required to avoid idempotent-dedup collisions).
	cases := []struct {
		name   string
		suffix string
		ssn    string
		dob    string
	}{
		{"ssn-too-short", "Sa", "12-34", "1990-01-15"},
		{"ssn-non-numeric", "Sb", "abcdefghi", "1990-01-15"},
		{"ssn-ten-digits", "Sc", "1234567890", "1990-01-15"},
		{"ssn-unicode-digits", "Sd", "१२३४५६७८९", "1990-01-15"},
		{"dob-slashes", "Da", "123-45-6789", "1990/01/15"},
		{"dob-wrong-order", "Db", "123-45-6789", "15-01-1990"},
		{"dob-not-a-date", "Dc", "123-45-6789", "not-a-date"},
		{"dob-month-13", "Dd", "123-45-6789", "2000-13-01"},
		{"dob-month-00", "De", "123-45-6789", "2000-00-15"},
		{"dob-feb-30", "Df", "123-45-6789", "2000-02-30"},
		{"dob-feb-29-non-leap", "Dg", "123-45-6789", "2001-02-29"},
		{"dob-apr-31", "Dh", "123-45-6789", "2000-04-31"},
		{"dob-year-0000", "Dj", "123-45-6789", "0000-01-01"},
		{"ssn-empty", "Se", "", "1990-01-15"},
		{"dob-empty", "Dk", "123-45-6789", ""},
	}
	// One real install + one durable-consumer pipeline shared across all cases;
	// each case drives a fresh identity through it sequentially.
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-bad")

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			identityKey := createIdentity(t, ctx, conn, cp, cons, "PIIBad"+tc.suffix)

			payload, _ := json.Marshal(map[string]any{
				"identityKey": identityKey,
				"ssn":         tc.ssn,
				"dob":         tc.dob,
			})
			env := &processor.OperationEnvelope{
				RequestID:     testutil.GenReqID("PIIBadOp" + tc.suffix),
				Lane:          processor.LaneDefault,
				OperationType: "RecordIdentityPII",
				Actor:         staffActorKey,
				SubmittedAt:   "2026-05-22T11:00:00Z",
				Class:         "identity",
				Payload:       json.RawMessage(payload),
				ContextHint:   recordPIIReads(identityKey),
			}
			testutil.PublishOp(t, conn, env)
			testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

			// No partial write: neither aspect key reached Core KV.
			if kvExists(t, ctx, conn, identityKey+".ssn") {
				t.Errorf("ssn aspect written despite rejection")
			}
			if kvExists(t, ctx, conn, identityKey+".dob") {
				t.Errorf("dob aspect written despite rejection")
			}
		})
	}
}

// TestRecordPII_StaffRejectedOnClaimedIdentity — the §3.2 scoping fix: a real
// (non-operator) frontOfHouse actor may RecordIdentityPII on an unclaimed
// identity (the walk-in-registration beat) but is AuthDenied on an
// already-claimed one — closing the unscoped-write gap
// facet-staff-worlds-design.md §3.2 flagged (F4's location-derived
// confinement cannot reach this op; a walk-in identity has no location).
func TestRecordPII_StaffRejectedOnClaimedIdentity(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-staff-claimed")

	claimedKey := "vtx.identity." + testutil.GenReqID("PIIStaffClaimed")
	seedDirectIdentity(t, ctx, conn, claimedKey, "claimed", "")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIStaffClaimedOp"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         frontDeskActorKey,
		SubmittedAt:   "2026-07-22T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + claimedKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		ContextHint:   recordPIIReads(claimedKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if kvExists(t, ctx, conn, claimedKey+".ssn") {
		t.Errorf("ssn aspect written despite AuthDenied rejection")
	}
	if kvExists(t, ctx, conn, claimedKey+".dob") {
		t.Errorf("dob aspect written despite AuthDenied rejection")
	}

	// Positive control, same actor: an unclaimed identity is exactly the
	// walk-in-registration case the op exists for, and is accepted.
	unclaimedKey := createIdentity(t, ctx, conn, cp, cons, "PIIStaffUnclaimed")
	env2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIStaffUnclaimedOp"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         frontDeskActorKey,
		SubmittedAt:   "2026-07-22T11:00:01Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + unclaimedKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		ContextHint:   recordPIIReads(unclaimedKey),
	}
	testutil.PublishOp(t, conn, env2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestRecordPII_TaskScopedNotConfinedToUnclaimed — the §3.2 confinement binds
// the STANDING path only (exactly F4's require_workplace), not a task/self-
// scoped submission: lease-signing's onboarding userTask assigns
// RecordIdentityPII to the applicant identity itself (assignee == subject,
// internal/loom/engine.go's §10.5 invariant), which by the time the applicant
// performs it may already be claimed. A non-operator actor submitting with
// AuthContext.Target set succeeds on a claimed identity — where the same
// actor submitting the STANDING way (no AuthContext) would be denied.
func TestRecordPII_TaskScopedNotConfinedToUnclaimed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-task-scoped")

	claimedKey := "vtx.identity." + testutil.GenReqID("PIITaskScoped")
	seedDirectIdentity(t, ctx, conn, claimedKey, "claimed", "")

	reads := recordPIIReads(claimedKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIITaskScopedOp"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         frontDeskActorKey,
		SubmittedAt:   "2026-07-22T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + claimedKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		AuthContext:   &processor.AuthContext{Target: claimedKey},
		ContextHint:   reads,
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	ssn := readDecryptedAspectData(t, ctx, conn, claimedKey, "ssn")
	if got, _ := ssn["value"].(string); got != "123456789" {
		t.Fatalf("ssn aspect value = %q, want normalized 123456789", got)
	}
}

// TestRecordPII_OperatorAllowedOnClaimedIdentity — root is exempt from the
// §3.2 confinement, mirroring every other confinement guard in the platform
// (F4's workplace guard): operator may RecordIdentityPII on an already-claimed
// identity (e.g. a data-correction), where a real staff actor is denied.
func TestRecordPII_OperatorAllowedOnClaimedIdentity(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-operator-claimed")

	claimedKey := "vtx.identity." + testutil.GenReqID("PIIOpClaimed")
	seedDirectIdentity(t, ctx, conn, claimedKey, "claimed", "")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIOpClaimedOp"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-22T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + claimedKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		ContextHint:   recordPIIReads(claimedKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	ssn := readDecryptedAspectData(t, ctx, conn, claimedKey, "ssn")
	if got, _ := ssn["value"].(string); got != "123456789" {
		t.Fatalf("ssn aspect value = %q, want normalized 123456789", got)
	}
}

// TestRecordPII_RejectsBadTarget — missing / non-identity identityKey is
// rejected (the existing-identity guard).
func TestRecordPII_RejectsBadTarget(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-badtarget")

	// Non-identity-prefix target.
	leaseKey := "vtx.lease." + testutil.GenReqID("PIILeaseTgt")
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIBadTgt"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + leaseKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		ContextHint:   recordPIIReads(leaseKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Absent identity (well-formed key, never created).
	ghostKey := "vtx.identity." + testutil.GenReqID("PIIGhostTgt")
	env2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIGhostOp"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + ghostKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		ContextHint:   recordPIIReads(ghostKey),
	}
	testutil.PublishOp(t, conn, env2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if kvExists(t, ctx, conn, ghostKey+".ssn") {
		t.Errorf("ssn aspect written for absent identity")
	}
}

// TestRecordPII_SensitiveSSNOnNonIdentityRejected — the anchoring negative
// (AC #2 + §3 + the §0 completion-lie trap). With the REAL installed ssn DDL
// (ref.Sensitive==true), the UNCHANGED step-6 validator rejects a ssn aspect
// on a non-identity vertex and permits it on an identity vertex.
func TestRecordPII_SensitiveSSNOnNonIdentityRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cache := freshDDLCache(t, ctx, conn)
	validator := processor.NewValidator(cache, conn, testutil.HarnessCoreBucket, testutil.TestLogger())

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIAnchorNeg"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T11:00:00Z",
	}

	leaseVtx := "vtx.lease." + testutil.GenReqID("PIIAnchorLse")
	reject := processor.ScriptResult{
		Mutations: []processor.MutationOp{{
			Op:  "create",
			Key: leaseVtx + ".ssn",
			Document: map[string]any{
				"class":     "ssn",
				"vertexKey": leaseVtx,
				"localName": "ssn",
				"isDeleted": false,
				"data":      map[string]any{"value": "123456789"},
			},
		}},
	}
	err := validator.Validate(ctx, env, reject, processor.HydratedState{})
	var ddlErr *processor.DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("sensitive ssn on lease: expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "sensitiveAspectScope" {
		t.Fatalf("ViolatedConstraint = %q, want sensitiveAspectScope", ddlErr.ViolatedConstraint)
	}

	// Same DDL also rejects a sensitive dob on a non-identity vertex.
	rejectDob := processor.ScriptResult{
		Mutations: []processor.MutationOp{{
			Op:  "create",
			Key: leaseVtx + ".dob",
			Document: map[string]any{
				"class":     "dob",
				"vertexKey": leaseVtx,
				"localName": "dob",
				"isDeleted": false,
				"data":      map[string]any{"value": "1990-01-15"},
			},
		}},
	}
	if err := validator.Validate(ctx, env, rejectDob, processor.HydratedState{}); !errors.As(err, &ddlErr) || ddlErr.ViolatedConstraint != "sensitiveAspectScope" {
		t.Fatalf("sensitive dob on lease: want sensitiveAspectScope, got %v", err)
	}

	// Positive: a ssn aspect on an identity vertex passes (anchored).
	idVtx := "vtx.identity." + testutil.GenReqID("PIIAnchorPos")
	accept := processor.ScriptResult{
		Mutations: []processor.MutationOp{{
			Op:  "create",
			Key: idVtx + ".ssn",
			Document: map[string]any{
				"class":     "ssn",
				"vertexKey": idVtx,
				"localName": "ssn",
				"isDeleted": false,
				"data":      map[string]any{"value": "123456789"},
			},
		}},
	}
	if err := validator.Validate(ctx, env, accept, processor.HydratedState{}); err != nil {
		t.Fatalf("sensitive ssn on identity: want pass, got %v", err)
	}
}

// TestRecordPII_AspectTypeDDLsInstalled — Item A/B install pin: the two
// aspect-type DDL meta-vertices and their `.sensitive` aspects landed in Core
// KV via the real InstallPackage path.
func TestRecordPII_AspectTypeDDLsInstalled(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cache := freshDDLCache(t, ctx, conn)

	for _, name := range []string{"ssn", "dob"} {
		ref, ok := cache.Lookup(name)
		if !ok {
			t.Fatalf("DDL %q not in cache after install", name)
		}
		// The meta-vertex root key resolves; its `.sensitive` aspect exists.
		if !kvExists(t, ctx, conn, ref.MetaVertexKey+".sensitive") {
			t.Fatalf("%q meta-vertex %s has no .sensitive aspect in Core KV", name, ref.MetaVertexKey)
		}
	}
}

// sensitivePIIClasses are the identity-PII aspect types whose DDLs carry
// sensitive=true: written by CreateUnclaimedIdentity / ClaimIdentity /
// identity-hygiene's MergeIdentity, so their DDLs carry NO permittedCommands
// (identity-anchoring is their only enforcement).
var sensitivePIIClasses = []string{"name", "email", "phone", "claimKey", "credentialBinding"}

// TestRecordPII_PIIClassesAreSensitive_AfterInstall — the sensitivity proof for
// the identity-PII classes. After the real package install, the DDL cache must
// carry ref.Sensitive==true for each, confirming the pkgmgr.DDLSpec Sensitive
// field travels through build.go's `.sensitive` aspect to the cache. Their
// permittedCommands MUST be empty (multiple writers across packages) — a
// non-empty list would reject MergeIdentity writing name/email/phone.
func TestRecordPII_PIIClassesAreSensitive_AfterInstall(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cache := freshDDLCache(t, ctx, conn)

	for _, name := range sensitivePIIClasses {
		ref, ok := cache.Lookup(name)
		if !ok {
			t.Fatalf("DDL cache has no entry for %q after install", name)
		}
		if !ref.Sensitive {
			t.Fatalf("%q ref.Sensitive = false after install; the .sensitive aspect did not reach the cache", name)
		}
		if ref.Kind != "aspectType" {
			t.Errorf("%q Kind = %q, want aspectType", name, ref.Kind)
		}
		if len(ref.PermittedCommands) != 0 {
			t.Fatalf("%q permittedCommands = %v, want empty (multiple writers across packages)", name, ref.PermittedCommands)
		}
		// The meta-vertex root resolves and its `.sensitive` aspect exists.
		if !kvExists(t, ctx, conn, ref.MetaVertexKey+".sensitive") {
			t.Fatalf("%q meta-vertex %s has no .sensitive aspect in Core KV", name, ref.MetaVertexKey)
		}
	}
}

// TestRecordPII_SensitivePIIOnNonIdentityRejected — the anchoring negative for
// the identity-PII classes (the real proof). With the REAL installed DDLs
// (ref.Sensitive==true, empty permittedCommands), the step-6 validator rejects
// a name/email/claimKey aspect on a non-identity vertex with
// sensitiveAspectScope, and permits the same class on an identity vertex.
func TestRecordPII_SensitivePIIOnNonIdentityRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cache := freshDDLCache(t, ctx, conn)
	validator := processor.NewValidator(cache, conn, testutil.HarnessCoreBucket, testutil.TestLogger())

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PIIBackfillAnchor"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T11:00:00Z",
	}

	// Cover name + email + claimKey on a non-identity (widget) vertex.
	for _, class := range []string{"name", "email", "claimKey"} {
		widgetVtx := "vtx.widget." + testutil.GenReqID("PIIBackfillWdg")
		reject := processor.ScriptResult{
			Mutations: []processor.MutationOp{{
				Op:  "create",
				Key: widgetVtx + "." + class,
				Document: map[string]any{
					"class":     class,
					"vertexKey": widgetVtx,
					"localName": class,
					"isDeleted": false,
					"data":      map[string]any{"value": "x"},
				},
			}},
		}
		err := validator.Validate(ctx, env, reject, processor.HydratedState{})
		var ddlErr *processor.DDLViolation
		if !errors.As(err, &ddlErr) {
			t.Fatalf("sensitive %q on widget: expected *DDLViolation, got %T: %v", class, err, err)
		}
		if ddlErr.ViolatedConstraint != "sensitiveAspectScope" {
			t.Fatalf("%q ViolatedConstraint = %q, want sensitiveAspectScope", class, ddlErr.ViolatedConstraint)
		}

		// Positive: same class on an identity vertex passes (anchored). Empty
		// permittedCommands means the writer's operationType is not checked.
		idVtx := "vtx.identity." + testutil.GenReqID("PIIBackfillId")
		accept := processor.ScriptResult{
			Mutations: []processor.MutationOp{{
				Op:  "create",
				Key: idVtx + "." + class,
				Document: map[string]any{
					"class":     class,
					"vertexKey": idVtx,
					"localName": class,
					"isDeleted": false,
					"data":      map[string]any{"value": "x"},
				},
			}},
		}
		if err := validator.Validate(ctx, env, accept, processor.HydratedState{}); err != nil {
			t.Fatalf("sensitive %q on identity: want pass, got %v", class, err)
		}
	}
}

// TestRecordPII_CreateIdentityWritesSensitiveAspects — positive no-regression:
// CreateUnclaimedIdentity still succeeds end-to-end and writes the
// sensitive name/email/claimKey aspects onto the identity vertex. They are
// identity-anchored, so they pass the step-6 sensitiveAspectScope check on the
// real commit path.
func TestRecordPII_CreateIdentityWritesSensitiveAspects(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newPIIPipeline(t, ctx, conn, "pii-backfill-create")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PIIBackfillCreate")

	// name aspect landed (sensitive, identity-anchored).
	name := readDecryptedAspectData(t, ctx, conn, identityKey, "name")
	if got, _ := name["value"].(string); got != "PII Applicant PIIBackfillCreate" {
		t.Fatalf("name aspect value = %q, want PII Applicant PIIBackfillCreate", got)
	}
	if c := readAspectClass(t, ctx, conn, identityKey+".name"); c != "name" {
		t.Fatalf("name aspect class = %q, want name", c)
	}
	// email aspect landed.
	if !kvExists(t, ctx, conn, identityKey+".email") {
		t.Fatalf("email aspect not written by CreateUnclaimedIdentity")
	}
	if c := readAspectClass(t, ctx, conn, identityKey+".email"); c != "email" {
		t.Fatalf("email aspect class = %q, want email", c)
	}
	// claimKey aspect landed (the create writes it; ClaimIdentity tombstones it).
	if !kvExists(t, ctx, conn, identityKey+".claimKey") {
		t.Fatalf("claimKey aspect not written by CreateUnclaimedIdentity")
	}
	if c := readAspectClass(t, ctx, conn, identityKey+".claimKey"); c != "claimKey" {
		t.Fatalf("claimKey aspect class = %q, want claimKey", c)
	}
}

// --- helpers ---

// readVertexRootData reads a vertex root document and returns its data map.
func readVertexRootData(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet root %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal root %s: %v", key, err)
	}
	data, _ := doc["data"].(map[string]any)
	return data
}

// readAspectClass reads an aspect document and returns its class.
func readAspectClass(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) string {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	c, _ := doc["class"].(string)
	return c
}

// assertEventPIIFree reads the durable outbox aspect for reqID, finds the event
// with class wantClass, and asserts its payload neither has an ssn/dob key nor
// contains any of the forbidden substrings (the raw ssn digits / dob string)
// anywhere in its values. Guards against a future edit leaking PII into events.
func assertEventPIIFree(t *testing.T, ctx context.Context, conn *substrate.Conn,
	reqID, wantClass string, forbidden ...string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(reqID))
	if err != nil {
		t.Fatalf("outbox aspect missing for %s: %v", reqID, err)
	}
	aspect, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	var found bool
	for _, ev := range aspect.Data.Events {
		if ev.EventType != wantClass {
			continue
		}
		found = true
		if _, ok := ev.Payload["ssn"]; ok {
			t.Errorf("%s event payload leaks an ssn key: %v", wantClass, ev.Payload)
		}
		if _, ok := ev.Payload["dob"]; ok {
			t.Errorf("%s event payload leaks a dob key: %v", wantClass, ev.Payload)
		}
		blob, _ := json.Marshal(ev.Payload)
		for _, sub := range forbidden {
			if sub != "" && bytes.Contains(blob, []byte(sub)) {
				t.Errorf("%s event payload leaks %q: %s", wantClass, sub, blob)
			}
		}
	}
	if !found {
		t.Fatalf("no %s event in outbox aspect for %s", wantClass, reqID)
	}
}

// kvExists reports whether key is present in Core KV. A clean ErrKeyNotFound
// means absent; any other non-nil error is an infrastructure failure that must
// not be silently read as "absent" (that would let a "no partial write"
// negative assertion pass on a transient error), so it fails the test.
func kvExists(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	_, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err == nil {
		return true
	}
	if errors.Is(err, substrate.ErrKeyNotFound) {
		return false
	}
	t.Fatalf("kvExists %s: unexpected error: %v", key, err)
	return false
}
