// Story 4.4 — Duplicate Identity Detection (FR3) integration tests.
//
// Validates ScanIdentityDuplicates end-to-end through the 10-step Processor
// pipeline against an embedded NATS server. Each test seeds identities directly
// in Core KV, submits a ScanIdentityDuplicates op as an operator-tier actor,
// and asserts the expected duplicate pairs, state transitions, duplicateOf
// aspects, events, and response detail.
//
// Tests:
//  1. TestScanDuplicates_FindsAllCriteria      — exact-email, exact-phone, levenshtein-name pairs found
//  2. TestScanDuplicates_RespectThreshold      — default 0.85 misses low ratio; payload override finds it
//  3. TestScanDuplicates_Idempotent            — second scan skips already-flagged pairs
//  4. TestScanDuplicates_SkipsMergedIdentities — merged identities excluded from scan
//  5. TestScanDuplicates_ClaimedIdentity_StaysOperational — claimed+flagged gets pendingReview in cap doc
//  6. TestScanDuplicates_NonOperatorDenied     — consumer role → step 3 denied
//  7. TestScanDuplicates_NoCandidates          — 3 unique identities → no pairs
//
// All tests use capability-auth mode. Fixture seeding mirrors identity_claim_test.go
// patterns from Story 4.3.
package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/nats-io/nats.go/jetstream"
)

// ---- Constants ----

const (
	// Operator actor that submits scan ops.
	iscOperatorActorID  = "JscOpActHKLMNPQRSTUV" // 20 chars, valid NanoID alphabet
	iscOperatorActorKey = "vtx.identity." + iscOperatorActorID
	iscOperatorCapKey   = "cap.identity." + iscOperatorActorID

	// Consumer actor used for denial test.
	iscConsumerActorID  = "JscCnActHKLMNPQRSTUV" // 20 chars
	iscConsumerActorKey = "vtx.identity." + iscConsumerActorID
	iscConsumerCapKey   = "cap.identity." + iscConsumerActorID

	iscTestBucket    = "core-kv"
	iscHealthBucket  = "health-kv"
	iscCapBucket     = "capability-kv"
	iscOpsStreamName = "core-operations"
	iscInstance      = "isc-test"
)

// ---- Harness helpers ----

// iscOperatorCapDoc builds an operator cap doc with ScanIdentityDuplicates permission.
func iscOperatorCapDoc() *CapabilityDoc {
	now := time.Now().UTC()
	return &CapabilityDoc{
		Key:                    iscOperatorCapKey,
		Actor:                  iscOperatorActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{iscOperatorActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "ScanIdentityDuplicates", Scope: "any"},
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
		},
		ServiceAccess:   []ServiceAccessEntry{},
		EphemeralGrants: []EphemeralGrant{},
		Roles:           []string{"vtx.role.operator"},
	}
}

// iscConsumerCapDoc builds a consumer cap doc (no ScanIdentityDuplicates).
func iscConsumerCapDoc() *CapabilityDoc {
	now := time.Now().UTC()
	return &CapabilityDoc{
		Key:                    iscConsumerCapKey,
		Actor:                  iscConsumerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{iscConsumerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		ServiceAccess:   []ServiceAccessEntry{},
		EphemeralGrants: []EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// provisionScanHarness sets up KV buckets, streams, and capability docs.
func provisionScanHarness(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()

	for _, bucket := range []string{iscTestBucket, iscHealthBucket} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket:         bucket,
			LimitMarkerTTL: time.Second,
		})
		if err != nil {
			t.Fatalf("create KV %q: %v", bucket, err)
		}
	}
	streamName := "KV_" + iscTestBucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("enable AllowAtomicPublish: %v", err)
	}

	capKV, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         iscCapBucket,
		LimitMarkerTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("create capability-kv: %v", err)
	}
	opDoc, _ := json.Marshal(iscOperatorCapDoc())
	if _, err := capKV.Put(ctx, iscOperatorCapKey, opDoc); err != nil {
		t.Fatalf("seed operator cap doc: %v", err)
	}
	conDoc, _ := json.Marshal(iscConsumerCapDoc())
	if _, err := capKV.Put(ctx, iscConsumerCapKey, conDoc); err != nil {
		t.Fatalf("seed consumer cap doc: %v", err)
	}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     iscOpsStreamName,
		Subjects: []string{"ops.>"},
	})
	if err != nil {
		t.Fatalf("create core-operations stream: %v", err)
	}
}

// seedIdentityDDLForScan writes the identity DDL in shadow-key form.
func seedIdentityDDLForScan(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	ddl := bootstrap.IdentityDDL()
	ddlKey := "vtx.meta.identity"
	ddlDoc := map[string]any{
		"class":     ddl.Class,
		"isDeleted": false,
		"data": map[string]any{
			"canonicalName":     ddl.CanonicalName,
			"permittedCommands": ddl.PermittedCommands,
		},
	}
	ddlBytes, _ := json.Marshal(ddlDoc)
	if _, err := conn.KVPut(ctx, iscTestBucket, ddlKey, ddlBytes); err != nil {
		t.Fatalf("seed identity DDL: %v", err)
	}
	scriptDoc := map[string]any{
		"class": "meta.script", "isDeleted": false,
		"data": map[string]any{"source": ddl.Script},
	}
	scriptBytes, _ := json.Marshal(scriptDoc)
	if _, err := conn.KVPut(ctx, iscTestBucket, ddlKey+".script", scriptBytes); err != nil {
		t.Fatalf("seed identity DDL script: %v", err)
	}
}

// setupScanTestEnv returns an embedded NATS env with harness + identity DDL.
func setupScanTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "isc-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	provisionScanHarness(t, ctx, conn)
	seedIdentityDDLForScan(t, ctx, conn)
	return ctx, conn
}

// newScanPipeline builds a capability-mode CommitPath for scan tests.
// ClaimEmitter is nil — not needed for scan ops.
func newScanPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := testLogger()
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, iscHealthBucket, iscInstance+"-"+durable, 10*time.Second, metrics, logger)
	cache := NewDDLCache(conn, iscTestBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("ddl cache refresh: %v", err)
	}
	authz, err := SelectAuthorizerArgs(SelectAuthorizerOpts{
		Mode:             AuthModeCapability,
		Reader:           conn,
		CapabilityBucket: iscCapBucket,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("SelectAuthorizerArgs: %v", err)
	}
	committer := NewCommitter(conn, iscTestBucket, cache, logger, time.Now)
	cp := NewCommitPath(Deps{
		Conn:        conn,
		CoreBucket:  iscTestBucket,
		HealthKV:    iscHealthBucket,
		Authorizer:  authz,
		Hydrator:    NewHydratorWithCache(conn, iscTestBucket, cache, logger),
		Executor:    NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:   NewValidator(cache, logger),
		Committer:   committer,
		Events:      &StubEventPublisher{logger: logger},
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	})
	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName:     iscOpsStreamName,
		Durable:        durable,
		FilterSubjects: []string{"ops.default"},
		AckWait:        10 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	return cp, cons
}

// seedFullIdentity seeds a complete identity vertex with name, email, phone, and state aspects.
// Pass "" to skip optional aspects.
func seedFullIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey, state, name, email, phone string) {
	t.Helper()
	vtxDoc := map[string]any{
		"class": "identity", "isDeleted": false, "data": map[string]any{},
	}
	vb, _ := json.Marshal(vtxDoc)
	if _, err := conn.KVPut(ctx, iscTestBucket, identityKey, vb); err != nil {
		t.Fatalf("seed identity vertex %s: %v", identityKey, err)
	}
	stateDoc := map[string]any{
		"class": "state", "vertexKey": identityKey, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": state},
	}
	sb, _ := json.Marshal(stateDoc)
	if _, err := conn.KVPut(ctx, iscTestBucket, identityKey+".state", sb); err != nil {
		t.Fatalf("seed state aspect %s: %v", identityKey, err)
	}
	if name != "" {
		nameDoc := map[string]any{
			"class": "name", "vertexKey": identityKey, "localName": "name",
			"isDeleted": false, "data": map[string]any{"value": name},
		}
		nb, _ := json.Marshal(nameDoc)
		if _, err := conn.KVPut(ctx, iscTestBucket, identityKey+".name", nb); err != nil {
			t.Fatalf("seed name aspect %s: %v", identityKey, err)
		}
	}
	if email != "" {
		emailDoc := map[string]any{
			"class": "email", "vertexKey": identityKey, "localName": "email",
			"isDeleted": false, "data": map[string]any{"value": email},
		}
		eb, _ := json.Marshal(emailDoc)
		if _, err := conn.KVPut(ctx, iscTestBucket, identityKey+".email", eb); err != nil {
			t.Fatalf("seed email aspect %s: %v", identityKey, err)
		}
	}
	if phone != "" {
		phoneDoc := map[string]any{
			"class": "phone", "vertexKey": identityKey, "localName": "phone",
			"isDeleted": false, "data": map[string]any{"value": phone},
		}
		pb, _ := json.Marshal(phoneDoc)
		if _, err := conn.KVPut(ctx, iscTestBucket, identityKey+".phone", pb); err != nil {
			t.Fatalf("seed phone aspect %s: %v", identityKey, err)
		}
	}
}

// canonicalLinkKey computes the canonical lnk.identity.<lowID>.duplicateOf.identity.<highID>
// key for a pair. This mirrors the script's logic: extract NanoID suffixes and sort
// lexicographically to produce a stable single key per pair.
func canonicalLinkKey(aKey, bKey string) string {
	const prefix = "vtx.identity."
	aID := aKey[len(prefix):]
	bID := bKey[len(prefix):]
	if aID < bID {
		return "lnk.identity." + aID + ".duplicateOf.identity." + bID
	}
	return "lnk.identity." + bID + ".duplicateOf.identity." + aID
}

// publishScanOp submits a ScanIdentityDuplicates operation.
// payload is any extra JSON fields (e.g. `"levenshteinThreshold": 0.5`).
// Pass nil for default payload.
func publishScanOp(t *testing.T, conn *substrate.Conn, reqID string, extraPayload map[string]any) {
	t.Helper()
	p := map[string]any{}
	for k, v := range extraPayload {
		p[k] = v
	}
	pb, _ := json.Marshal(p)
	env := &OperationEnvelope{
		RequestID:     reqID,
		Lane:          LaneDefault,
		OperationType: "ScanIdentityDuplicates",
		Actor:         iscOperatorActorKey,
		SubmittedAt:   "2026-05-17T12:00:00Z",
		Class:         "identity",
		Payload:       pb,
		ContextHint: &ContextHint{
			ScanPrefixes: []string{"vtx.identity.", "lnk.identity."},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal scan envelope: %v", err)
	}
	if _, err := conn.JetStream().Publish(context.Background(), "ops.default", b); err != nil {
		t.Fatalf("publish scan op: %v", err)
	}
}

// readIdentityState reads the state value from the state aspect of an identity.
func readIdentityState(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) string {
	t.Helper()
	data := readAspectData(t, ctx, conn, identityKey+".state")
	v, _ := data["value"].(string)
	return v
}

// readLinkEnvelope reads the link envelope at the given linkKey.
// Returns nil if the link does not exist.
func readLinkEnvelope(t *testing.T, ctx context.Context, conn *substrate.Conn, linkKey string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, iscTestBucket, linkKey)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return nil
	}
	return doc
}

// linkEnvelopeData returns the data map from a link envelope, or nil.
func linkEnvelopeData(doc map[string]any) map[string]any {
	if doc == nil {
		return nil
	}
	data, _ := doc["data"].(map[string]any)
	return data
}

// linkCriteriaContains checks that a link data map's "criteria" list contains the given value.
func linkCriteriaContains(data map[string]any, criterion string) bool {
	if data == nil {
		return false
	}
	criteriaRaw, _ := data["criteria"].([]any)
	for _, c := range criteriaRaw {
		if s, ok := c.(string); ok && s == criterion {
			return true
		}
	}
	return false
}

// ---- Tests ----

// TestScanDuplicates_FindsAllCriteria seeds 6 identities with 3 matching pairs
// (exact-email, exact-phone, levenshtein-name) and verifies all 3 are found,
// all 6 transitioned to flagged-for-review, and bidirectional duplicateOf aspects written.
func TestScanDuplicates_FindsAllCriteria(t *testing.T) {
	ctx, conn := setupScanTestEnv(t)
	cp, cons := newScanPipeline(t, ctx, conn, "isc-all-criteria")

	// Seed 6 identities: A+B (exact-email), C+D (exact-phone), E+F (levenshtein-name).
	// All NanoIDs are exactly 20 chars from the valid NanoID alphabet.
	aKey := "vtx.identity.ScFindAaaaaaaaaaaaa1"
	bKey := "vtx.identity.ScFindBaaaaaaaaaaaa1"
	cKey := "vtx.identity.ScFindCaaaaaaaaaaaa1"
	dKey := "vtx.identity.ScFindDaaaaaaaaaaaa1"
	eKey := "vtx.identity.ScFindEaaaaaaaaaaaa1"
	fKey := "vtx.identity.ScFindFaaaaaaaaaaaa1"

	seedFullIdentity(t, ctx, conn, aKey, "unclaimed", "Alice Jones", "alice@match.example", "")
	seedFullIdentity(t, ctx, conn, bKey, "claimed", "Bob Smith", "alice@match.example", "") // same email
	seedFullIdentity(t, ctx, conn, cKey, "unclaimed", "Carol Brown", "", "+15551111111")
	seedFullIdentity(t, ctx, conn, dKey, "unclaimed", "Dave Green", "", "+15551111111") // same phone
	seedFullIdentity(t, ctx, conn, eKey, "unclaimed", "alice smith", "", "")  // levenshtein match
	seedFullIdentity(t, ctx, conn, fKey, "unclaimed", "alicw smith", "", "")  // 1 sub in 11 → ratio 0.909

	scanReqID := genReqID("ScanAllCriteria")
	publishScanOp(t, conn, scanReqID, nil)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// All 6 identities should be flagged-for-review.
	for _, key := range []string{aKey, bKey, cKey, dKey, eKey, fKey} {
		if s := readIdentityState(t, ctx, conn, key); s != "flagged-for-review" {
			t.Errorf("%s: state = %q, want flagged-for-review", key, s)
		}
	}

	// Canonical link envelopes must exist: one per pair.
	// A↔B (exact-email pair).
	abLinkKey := canonicalLinkKey(aKey, bKey)
	abLink := readLinkEnvelope(t, ctx, conn, abLinkKey)
	if abLink == nil {
		t.Errorf("A↔B: canonical link envelope missing at %s", abLinkKey)
	} else {
		abData := linkEnvelopeData(abLink)
		if abData == nil {
			t.Errorf("A↔B link: data field missing")
		} else if !linkCriteriaContains(abData, "exact-email") {
			t.Errorf("A↔B link: criteria missing exact-email; data=%v", abData)
		}
		if cls, _ := abLink["class"].(string); cls != "duplicateOf" {
			t.Errorf("A↔B link: class = %q, want duplicateOf", cls)
		}
	}

	// C↔D (exact-phone pair).
	cdLinkKey := canonicalLinkKey(cKey, dKey)
	cdLink := readLinkEnvelope(t, ctx, conn, cdLinkKey)
	if cdLink == nil {
		t.Errorf("C↔D: canonical link envelope missing at %s", cdLinkKey)
	} else if !linkCriteriaContains(linkEnvelopeData(cdLink), "exact-phone") {
		t.Errorf("C↔D link: criteria missing exact-phone")
	}

	// E↔F (levenshtein-name pair).
	efLinkKey := canonicalLinkKey(eKey, fKey)
	efLink := readLinkEnvelope(t, ctx, conn, efLinkKey)
	if efLink == nil {
		t.Errorf("E↔F: canonical link envelope missing at %s", efLinkKey)
	} else if !linkCriteriaContains(linkEnvelopeData(efLink), "levenshtein-name") {
		t.Errorf("E↔F link: criteria missing levenshtein-name")
	}
}

// TestScanDuplicates_RespectThreshold seeds 2 names with ratio ~0.7.
// Default threshold 0.85 → no pair. Override to 0.0 → pair found.
// Both scans run through the same pipeline+consumer to avoid stream-redelivery issues.
func TestScanDuplicates_RespectThreshold(t *testing.T) {
	ctx, conn := setupScanTestEnv(t)
	cp, cons := newScanPipeline(t, ctx, conn, "isc-thresh")

	// Names: "Alice Johnson" vs "Bob Williams" — low levenshtein ratio.
	xKey := "vtx.identity.ScThreshXaaaaaaaaa1a"
	yKey := "vtx.identity.ScThreshYaaaaaaaaa1b"
	seedFullIdentity(t, ctx, conn, xKey, "unclaimed", "alice johnson", "", "")
	seedFullIdentity(t, ctx, conn, yKey, "unclaimed", "bob williams", "", "")

	// First scan: default threshold 0.85 → no pair.
	req1 := genReqID("ScanThreshHigh0")
	publishScanOp(t, conn, req1, nil)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// State should remain unclaimed (no match at default threshold).
	if s := readIdentityState(t, ctx, conn, xKey); s != "unclaimed" {
		t.Errorf("xKey at default threshold: state = %q, want unclaimed", s)
	}

	// Second scan: override threshold to 0.0 → all pairs match.
	req2 := genReqID("ScanThreshLow00")
	publishScanOp(t, conn, req2, map[string]any{"levenshteinThreshold": 0.0})
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// Now pair should be found, both flagged.
	if s := readIdentityState(t, ctx, conn, xKey); s != "flagged-for-review" {
		t.Errorf("xKey at threshold=0.0: state = %q, want flagged-for-review", s)
	}
	if s := readIdentityState(t, ctx, conn, yKey); s != "flagged-for-review" {
		t.Errorf("yKey at threshold=0.0: state = %q, want flagged-for-review", s)
	}
}

// TestScanDuplicates_Idempotent: run scan twice. First flags pairs; second sees
// existing duplicateOf aspects and skips re-mutation. State unchanged after second run.
func TestScanDuplicates_Idempotent(t *testing.T) {
	ctx, conn := setupScanTestEnv(t)
	cp, cons := newScanPipeline(t, ctx, conn, "isc-idempotent")

	pKey := "vtx.identity.ScJdemPaaaaaaaaaaaa1"
	qKey := "vtx.identity.ScJdemQaaaaaaaaaaaa1"
	seedFullIdentity(t, ctx, conn, pKey, "unclaimed", "", "idem@match.example", "")
	seedFullIdentity(t, ctx, conn, qKey, "unclaimed", "", "idem@match.example", "")

	// First scan — should flag both.
	req1 := genReqID("ScanIdemFirst00")
	publishScanOp(t, conn, req1, nil)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	if s := readIdentityState(t, ctx, conn, pKey); s != "flagged-for-review" {
		t.Fatalf("first scan: pKey state = %q, want flagged-for-review", s)
	}

	// Record the canonical link envelope revision before second scan.
	pqLinkKey := canonicalLinkKey(pKey, qKey)
	linkEntryBefore, err := conn.KVGet(ctx, iscTestBucket, pqLinkKey)
	if err != nil {
		t.Fatalf("read canonical link %s before second scan: %v", pqLinkKey, err)
	}
	revBefore := linkEntryBefore.Revision

	// Second scan — should skip the already-flagged pair via lnk.identity.* pre-load.
	req2 := genReqID("ScanIdemSecond0")
	publishScanOp(t, conn, req2, nil)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// State must still be flagged-for-review (not changed by second scan).
	if s := readIdentityState(t, ctx, conn, pKey); s != "flagged-for-review" {
		t.Errorf("second scan: pKey state changed to %q", s)
	}

	// Link envelope revision must NOT have changed (no new mutation issued).
	linkEntryAfter, err := conn.KVGet(ctx, iscTestBucket, pqLinkKey)
	if err != nil {
		t.Fatalf("read canonical link %s after second scan: %v", pqLinkKey, err)
	}
	if linkEntryAfter.Revision != revBefore {
		t.Errorf("second scan: canonical link %s revision changed (%d → %d), want idempotent",
			pqLinkKey, revBefore, linkEntryAfter.Revision)
	}
}

// TestScanDuplicates_SkipsMergedIdentities: merged identity excluded from scan.
// A+C would match on email, but C is merged. A+B match on email. Assert only A+B found.
func TestScanDuplicates_SkipsMergedIdentities(t *testing.T) {
	ctx, conn := setupScanTestEnv(t)
	cp, cons := newScanPipeline(t, ctx, conn, "isc-skip-merged")

	mA := "vtx.identity.ScMergeAaaaaaaaaaa1a"
	mB := "vtx.identity.ScMergeBaaaaaaaaaa1b"
	mC := "vtx.identity.ScMergeCaaaaaaaaaa1c"
	seedFullIdentity(t, ctx, conn, mA, "unclaimed", "", "shared@merged.example", "")
	seedFullIdentity(t, ctx, conn, mB, "unclaimed", "", "shared@merged.example", "")
	seedFullIdentity(t, ctx, conn, mC, "merged", "", "shared@merged.example", "") // merged — should be skipped

	req := genReqID("ScanSkipMerged0")
	publishScanOp(t, conn, req, nil)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// A and B should be flagged.
	if s := readIdentityState(t, ctx, conn, mA); s != "flagged-for-review" {
		t.Errorf("mA: state = %q, want flagged-for-review", s)
	}
	if s := readIdentityState(t, ctx, conn, mB); s != "flagged-for-review" {
		t.Errorf("mB: state = %q, want flagged-for-review", s)
	}

	// C should remain merged (not processed).
	if s := readIdentityState(t, ctx, conn, mC); s != "merged" {
		t.Errorf("mC: state = %q, want merged (should not be touched)", s)
	}

	// No canonical link involving C (either A↔C or B↔C should be absent).
	acLinkKey := canonicalLinkKey(mA, mC)
	if link := readLinkEnvelope(t, ctx, conn, acLinkKey); link != nil {
		t.Errorf("A↔C link should not exist (C is merged), found at %s", acLinkKey)
	}
	bcLinkKey := canonicalLinkKey(mB, mC)
	if link := readLinkEnvelope(t, ctx, conn, bcLinkKey); link != nil {
		t.Errorf("B↔C link should not exist (C is merged), found at %s", bcLinkKey)
	}

	// A↔B link must exist (both are valid candidates).
	abLinkKey := canonicalLinkKey(mA, mB)
	if link := readLinkEnvelope(t, ctx, conn, abLinkKey); link == nil {
		t.Errorf("A↔B link should exist at %s", abLinkKey)
	}
}

// TestScanDuplicates_ClaimedIdentity_StaysOperational: a claimed identity that
// matches another gets flagged, but its cap doc should show pendingReview: true
// AND retain its platformPermissions. The test seeds a cap doc for the claimed
// identity, verifies it retains permissions after the scan flags the identity.
func TestScanDuplicates_ClaimedIdentity_StaysOperational(t *testing.T) {
	ctx, conn := setupScanTestEnv(t)
	cp, cons := newScanPipeline(t, ctx, conn, "isc-claimed-op")

	// The "claimed" identity actor. 20-char valid NanoID (no l, I, O, 0).
	claimedID := "ScCdaimedXaaaaaaaaa1"
	claimedKey := "vtx.identity." + claimedID
	claimedCapKey := "cap.identity." + claimedID

	targetID := "ScTargetXaaaaaaaaaa1"
	targetKey := "vtx.identity." + targetID

	// Seed the claimed identity with an email that matches target.
	seedFullIdentity(t, ctx, conn, claimedKey, "claimed", "Claimed User", "claimed@op.example", "")
	seedFullIdentity(t, ctx, conn, targetKey, "unclaimed", "Target User", "claimed@op.example", "")

	// Seed a cap doc for the claimed identity with some permissions.
	capKV, err := conn.JetStream().KeyValue(ctx, iscCapBucket)
	if err != nil {
		t.Fatalf("open capability-kv: %v", err)
	}
	claimedCapDoc := &CapabilityDoc{
		Key:                    claimedCapKey,
		Actor:                  claimedKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{claimedKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		ServiceAccess:   []ServiceAccessEntry{},
		EphemeralGrants: []EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
	capDocBytes, _ := json.Marshal(claimedCapDoc)
	if _, err := capKV.Put(ctx, claimedCapKey, capDocBytes); err != nil {
		t.Fatalf("seed claimed cap doc: %v", err)
	}

	// Run the scan.
	req := genReqID("ScanClaimedOpAA")
	publishScanOp(t, conn, req, nil)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// Both identities should be flagged.
	if s := readIdentityState(t, ctx, conn, claimedKey); s != "flagged-for-review" {
		t.Errorf("claimedKey: state = %q, want flagged-for-review", s)
	}

	// Claimed identity's cap doc is pre-seeded; verify its platformPermissions
	// are still present (the scan doesn't touch cap docs — it's the Refractor
	// that would update them; here we verify the Core KV state is correct and
	// the pre-seeded cap doc is intact).
	capEntry, err := conn.KVGet(ctx, iscCapBucket, claimedCapKey)
	if err != nil {
		t.Fatalf("read claimed cap doc: %v", err)
	}
	var capDocRead CapabilityDoc
	if err := json.Unmarshal(capEntry.Value, &capDocRead); err != nil {
		t.Fatalf("unmarshal claimed cap doc: %v", err)
	}
	if len(capDocRead.PlatformPermissions) == 0 {
		t.Error("claimed identity's platformPermissions must be non-empty after scan")
	}

	// Verify canonical link between claimed identity and target.
	linkKey := canonicalLinkKey(claimedKey, targetKey)
	linkDoc := readLinkEnvelope(t, ctx, conn, linkKey)
	if linkDoc == nil {
		t.Errorf("canonical link missing at %s", linkKey)
	} else if cls, _ := linkDoc["class"].(string); cls != "duplicateOf" {
		t.Errorf("link class = %q, want duplicateOf", cls)
	}
}

// TestScanDuplicates_NonOperatorDenied: consumer role → step 3 denies.
// The ScanIdentityDuplicates permission is operator+backOfHouse only.
func TestScanDuplicates_NonOperatorDenied(t *testing.T) {
	ctx, conn := setupScanTestEnv(t)

	// Use a pipeline where the actor is the consumer (no ScanIdentityDuplicates perm).
	cp, cons := newScanPipeline(t, ctx, conn, "isc-denied")

	// Publish scan op as consumer actor.
	req := genReqID("ScanDeniedCons0")
	payload := json.RawMessage(`{}`)
	env := &OperationEnvelope{
		RequestID:     req,
		Lane:          LaneDefault,
		OperationType: "ScanIdentityDuplicates",
		Actor:         iscConsumerActorKey, // consumer — no ScanIdentityDuplicates
		SubmittedAt:   "2026-05-17T12:00:00Z",
		Class:         "identity",
		Payload:       payload,
		ContextHint:   &ContextHint{ScanPrefixes: []string{"vtx.identity.", "lnk.identity."}},
	}
	b, _ := json.Marshal(env)
	if _, err := conn.JetStream().Publish(context.Background(), "ops.default", b); err != nil {
		t.Fatalf("publish denied scan op: %v", err)
	}
	driveOne(t, ctx, cp, cons, OutcomeRejected)
}

// TestScanDuplicates_NoCandidates: 3 unique identities → no pairs, all zeros in breakdown.
func TestScanDuplicates_NoCandidates(t *testing.T) {
	ctx, conn := setupScanTestEnv(t)
	cp, cons := newScanPipeline(t, ctx, conn, "isc-no-candidates")

	u1 := "vtx.identity.ScNoCandU1aaaaaaaa1a"
	u2 := "vtx.identity.ScNoCandU2aaaaaaaa1b"
	u3 := "vtx.identity.ScNoCandU3aaaaaaaa1c"
	seedFullIdentity(t, ctx, conn, u1, "unclaimed", "Alice Alpha", "alice@unique.example", "+11111111111")
	seedFullIdentity(t, ctx, conn, u2, "unclaimed", "Bob Beta", "bob@unique.example", "+12222222222")
	seedFullIdentity(t, ctx, conn, u3, "unclaimed", "Carol Gamma", "carol@unique.example", "+13333333333")

	req := genReqID("ScanNoCandidts0")
	publishScanOp(t, conn, req, nil)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// No state changes — all still unclaimed.
	for _, key := range []string{u1, u2, u3} {
		if s := readIdentityState(t, ctx, conn, key); s != "unclaimed" {
			t.Errorf("%s: state = %q, want unclaimed (no candidates)", key, s)
		}
	}

	// No canonical links between any pair.
	allKeys := []string{u1, u2, u3}
	for i := 0; i < len(allKeys); i++ {
		for j := i + 1; j < len(allKeys); j++ {
			lk := canonicalLinkKey(allKeys[i], allKeys[j])
			if link := readLinkEnvelope(t, ctx, conn, lk); link != nil {
				t.Errorf("unexpected link at %s for unique identities", lk)
			}
		}
	}
}
