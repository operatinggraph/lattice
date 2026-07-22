// Shared test helpers for identity-hygiene package end-to-end tests.
//
// These live in an external test package (`identityhygiene_test`) so they
// exercise only the public Lattice surface that any Capability Package would
// see in production.
package identityhygiene_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
)

// Test actor NanoIDs — 20 chars, substrate.Alphabet only (no I/O/l/0).
const (
	operatorActorID  = "JopActHygeNPQRSTUVWX"
	operatorActorKey = "vtx.identity." + operatorActorID
	operatorCapKey   = "cap.identity." + operatorActorID

	consumerActorID  = "JcnHygeActNPQRSTUVWX"
	consumerActorKey = "vtx.identity." + consumerActorID
	consumerCapKey   = "cap.identity." + consumerActorID
)

// operatorCapDoc seeds a CapabilityDoc granting MergeIdentity (scope=any)
// to the operator actor. It also carries the operator role key so the
// Capability Authorizer step-3 check passes.
func operatorCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    operatorCapKey,
		Actor:                  operatorActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{operatorActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "MergeIdentity", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// consumerCapDoc seeds a CapabilityDoc granting only ClaimIdentity (scope=self)
// — no MergeIdentity permission. Used for TestMerge_NonOperatorActor_Denied.
func consumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    consumerCapKey,
		Actor:                  consumerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{consumerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// setupTestEnv assembles the standard identity-hygiene test environment:
// embedded NATS, KV buckets, primordials seeded, Phase 1 packages installed,
// operator + consumer cap docs seeded.
func setupTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, operatorCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, consumerCapDoc())
	return ctx, conn
}

// newMergePipeline builds a CapabilityPipeline for MergeIdentity tests.
func newMergePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ihm-" + durable,
	})
}

// seedIdentityVertex writes a minimal identity vertex + state aspect + mergedInto
// aspect directly to Core KV (no op required). The mergedInto aspect is always
// written (with empty data when not merged) so that ContextHint.Reads can include
// it without triggering a HydrationMiss. This mirrors the identity-domain
// seedIdentityVertex helper in packages/identity-domain/state_machine_test.go.
//
// Pass mergedIntoKey="" to write an empty (non-merged) mergedInto aspect.
func seedIdentityVertex(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, state, mergedIntoKey string) {
	t.Helper()
	vtxDoc := map[string]any{
		"class":     "identity",
		"isDeleted": false,
		"data":      map[string]any{},
	}
	vb, _ := json.Marshal(vtxDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey, vb); err != nil {
		t.Fatalf("seed identity vertex %s: %v", identityKey, err)
	}
	stateDoc := map[string]any{
		"class": "state", "vertexKey": identityKey, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": state},
	}
	sb, _ := json.Marshal(stateDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".state", sb); err != nil {
		t.Fatalf("seed state aspect %s: %v", identityKey, err)
	}
	// Always seed mergedInto (empty data when not merged) so the Hydrator
	// finds the key when ContextHint.Reads includes it.
	miData := map[string]any{}
	if mergedIntoKey != "" {
		miData["value"] = mergedIntoKey
	}
	miDoc := map[string]any{
		"class": "mergedInto", "vertexKey": identityKey, "localName": "mergedInto",
		"isDeleted": false, "data": miData,
	}
	mb, _ := json.Marshal(miDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".mergedInto", mb); err != nil {
		t.Fatalf("seed mergedInto aspect %s: %v", identityKey, err)
	}
}

// seedLinkVertex writes a link vertex envelope at the given key to Core KV.
// The key must be in the six-segment form `lnk.<srcType>.<srcId>.<rel>.<tgtType>.<tgtId>`.
// isDeleted=true produces a tombstoned link (for TestMerge_RejectsTombstonedEdge).
func seedLinkVertex(t *testing.T, ctx context.Context, conn *substrate.Conn,
	linkKey string, isDeleted bool) {
	t.Helper()
	seedLinkVertexWithClass(t, ctx, conn, linkKey, "link", isDeleted, nil)
}

// seedLinkVertexWithClass writes a link vertex envelope with an explicit
// class + data — real production links carry their relation name as class
// (e.g. "holdsRole", "duplicateOf", "indexes"), never the literal "link"
// (TestMerge_TrustGateAcceptsRealClassLink, dedup-over-encrypted-pii-design.md
// §2.4/§3.4). data defaults to {} when nil.
func seedLinkVertexWithClass(t *testing.T, ctx context.Context, conn *substrate.Conn,
	linkKey, class string, isDeleted bool, data map[string]any) {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	doc := map[string]any{
		"class":     class,
		"isDeleted": isDeleted,
		"data":      data,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, b); err != nil {
		t.Fatalf("seed link vertex %s: %v", linkKey, err)
	}
}

// seedIdentityIndexVertex writes a vtx.identityindex.<hash> vertex owned by
// ownerIdentityKey (identity-domain/ddls.go §3.1 shape: {contactType,
// identityKey}).
func seedIdentityIndexVertex(t *testing.T, ctx context.Context, conn *substrate.Conn,
	indexKey, contactType, ownerIdentityKey string) {
	t.Helper()
	doc := map[string]any{
		"class":     "identityindex",
		"isDeleted": false,
		"data": map[string]any{
			"contactType": contactType,
			"identityKey": ownerIdentityKey,
		},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, indexKey, b); err != nil {
		t.Fatalf("seed identityindex vertex %s: %v", indexKey, err)
	}
}

// seedTaskVertex writes a minimal orchestration-base task vertex directly to
// Core KV (no op required) -- used by the MergeIdentity open-task-guard tests
// to seed a task assignedTo the secondary without depending on
// orchestration-base's package being installed in this pipeline.
func seedTaskVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, taskKey, status string) {
	t.Helper()
	doc := map[string]any{
		"class":     "task",
		"isDeleted": false,
		"data":      map[string]any{"status": status, "expiresAt": "2030-01-01T00:00:00Z"},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, taskKey, b); err != nil {
		t.Fatalf("seed task vertex %s: %v", taskKey, err)
	}
}

// assertLinkTombstoned asserts the link/vertex envelope at key exists and
// is tombstoned (isDeleted=true).
func assertLinkTombstoned(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	_ = json.Unmarshal(entry.Value, &doc)
	if isDeleted, _ := doc["isDeleted"].(bool); !isDeleted {
		t.Fatalf("%s should be tombstoned", key)
	}
}

// assertLinkLive asserts the link/vertex envelope at key exists and is NOT
// tombstoned.
func assertLinkLive(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	_ = json.Unmarshal(entry.Value, &doc)
	if isDeleted, _ := doc["isDeleted"].(bool); isDeleted {
		t.Fatalf("%s should be live, is tombstoned", key)
	}
}

// readAspectData reads a KV aspect envelope and returns its "data" map.
func readAspectData(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	data, _ := doc["data"].(map[string]any)
	return data
}

// credentialIndexKey mirrors `crypto.sha256NanoID(actorKey)` (byte-identical
// to substrate.SHA256NanoID — internal/substrate/derive.go's doc comment).
func credentialIndexKey(actorKey string) string {
	return "vtx.credentialindex." + substrate.SHA256NanoID(actorKey)
}

// seedCredentialIndexVertex writes a vtx.credentialindex.<hash> vertex
// directly to Core KV, mirroring what ClaimIdentity/CompleteCredentialLink
// would have written.
func seedCredentialIndexVertex(t *testing.T, ctx context.Context, conn *substrate.Conn,
	actorKey, identityKey, boundAt string) {
	t.Helper()
	doc := map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data": map[string]any{
			"actorKey":    actorKey,
			"identityKey": identityKey,
			"boundAt":     boundAt,
		},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, credentialIndexKey(actorKey), b); err != nil {
		t.Fatalf("seed credentialindex %s: %v", actorKey, err)
	}
}

// seedCredentialBindingSingular writes an identity's .credentialBinding
// aspect in the pre-Fire-2 singular shape (no `credentials` array) —
// multi-credential-identity-linking-design.md §3.1's migration fallback.
// credentialBinding is Vault-sensitivity-classed (identity-domain's DDL), so
// this mirrors the real Processor's step-6.5 encrypt hook exactly like
// identity-domain's seedSensitiveAspect test helper.
func seedCredentialBindingSingular(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, actorKey, boundAt string) {
	t.Helper()
	seedSensitiveCredentialBinding(t, ctx, conn, identityKey, map[string]any{
		"actorKey": actorKey, "boundAt": boundAt,
	})
}

// seedCredentialBindingArray writes an identity's .credentialBinding aspect
// with the Fire-2+ `credentials` array shape (design §3.1). credentials is
// a list of {actorKey, boundAt} maps.
func seedCredentialBindingArray(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey string, credentials []map[string]any) {
	t.Helper()
	first := credentials[0]
	seedSensitiveCredentialBinding(t, ctx, conn, identityKey, map[string]any{
		"actorKey":    first["actorKey"],
		"boundAt":     first["boundAt"],
		"credentials": credentials,
	})
}

// seedSensitiveCredentialBinding writes identityKey's .credentialBinding
// aspect ciphertext-encoded exactly as the real Processor's step-6.5
// encrypt hook would produce it, lazily minting the identity's piiKey via
// the shared TestVault if absent.
func seedSensitiveCredentialBinding(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey string, plaintext map[string]any) {
	t.Helper()
	v := testutil.TestVault(t)
	env := ensureTestPiiKey(t, ctx, conn, v, identityKey)

	pt, err := json.Marshal(plaintext)
	if err != nil {
		t.Fatalf("marshal plaintext for %s.credentialBinding: %v", identityKey, err)
	}
	ct, err := v.Encrypt(ctx, identityKey, env, pt)
	if err != nil {
		t.Fatalf("encrypt %s.credentialBinding: %v", identityKey, err)
	}
	doc := map[string]any{
		"class": "credentialBinding", "vertexKey": identityKey, "localName": "credentialBinding",
		"isDeleted": false,
		"data":      ct,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".credentialBinding", b); err != nil {
		t.Fatalf("seed credentialBinding %s: %v", identityKey, err)
	}
}

// ensureTestPiiKey returns identityKey's existing piiKey envelope, or mints
// and seeds a fresh one via v — the fixture-side mirror of the Processor's
// step-6.5 lazy piiKey creation (identity-domain/testhelpers_test.go's
// helper of the same name).
func ensureTestPiiKey(t *testing.T, ctx context.Context, conn *substrate.Conn, v vault.Vault, identityKey string) vault.Envelope {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".piiKey")
	if err == nil {
		var doc struct {
			Data vault.Envelope `json:"data"`
		}
		if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
			t.Fatalf("unmarshal piiKey for %s: %v", identityKey, uerr)
		}
		return doc.Data
	}
	if !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("read piiKey for %s: %v", identityKey, err)
	}
	env, err := v.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey for %s: %v", identityKey, err)
	}
	doc := map[string]any{
		"class":     "piiKey",
		"vertexKey": identityKey,
		"localName": "piiKey",
		"isDeleted": false,
		"data":      env,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".piiKey", b); err != nil {
		t.Fatalf("seed piiKey for %s: %v", identityKey, err)
	}
	return env
}

// assertCredentialIndexPointsTo asserts the credentialindex vertex for
// actorKey is live and its identityKey field equals wantIdentityKey.
func assertCredentialIndexPointsTo(t *testing.T, ctx context.Context, conn *substrate.Conn,
	actorKey, wantIdentityKey string) {
	t.Helper()
	data := readAspectData(t, ctx, conn, credentialIndexKey(actorKey))
	if got, _ := data["identityKey"].(string); got != wantIdentityKey {
		t.Fatalf("credentialindex(%s).identityKey = %q, want %s", actorKey, got, wantIdentityKey)
	}
}

// assertCredentialBindingAbsent asserts the identity's .credentialBinding
// aspect key does not exist in Core KV at all (never written — distinct
// from a tombstoned key, which exists with isDeleted=true; use
// assertLinkTombstoned for that case).
func assertCredentialBindingAbsent(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".credentialBinding"); err == nil {
		t.Fatalf("%s.credentialBinding should not exist", identityKey)
	}
}

// readDecryptedCredentialBinding reads and Vault-decrypts identityKey's
// .credentialBinding aspect (identity-domain/testhelpers_test.go's
// readDecryptedAspectData, local copy — credentialBinding is
// sensitivity-classed).
func readDecryptedCredentialBinding(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) map[string]any {
	t.Helper()
	v := testutil.TestVault(t)
	env := readTestPiiKeyEnvelope(t, ctx, conn, identityKey)

	aspectKey := identityKey + ".credentialBinding"
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, aspectKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", aspectKey, err)
	}
	var doc struct {
		Data vault.Ciphertext `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", aspectKey, err)
	}
	plaintext, err := v.Decrypt(ctx, identityKey, env, doc.Data)
	if err != nil {
		t.Fatalf("decrypt %s: %v", aspectKey, err)
	}
	var value map[string]any
	if err := json.Unmarshal(plaintext, &value); err != nil {
		t.Fatalf("unmarshal decrypted %s: %v", aspectKey, err)
	}
	return value
}

// readTestPiiKeyEnvelope reads and parses identityKey's piiKey aspect
// (identity-domain/testhelpers_test.go's helper of the same name, local
// copy).
func readTestPiiKeyEnvelope(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) vault.Envelope {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".piiKey")
	if err != nil {
		t.Fatalf("KVGet piiKey for %s: %v", identityKey, err)
	}
	var doc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal piiKey for %s: %v", identityKey, err)
	}
	return doc.Data
}

// primaryCredentialActorKeys returns the set of actorKey values in the
// primary's unioned .credentialBinding.credentials array.
func primaryCredentialActorKeys(t *testing.T, ctx context.Context, conn *substrate.Conn, primaryKey string) map[string]bool {
	t.Helper()
	data := readDecryptedCredentialBinding(t, ctx, conn, primaryKey)
	arr, _ := data["credentials"].([]any)
	out := map[string]bool{}
	for _, c := range arr {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if a, ok := m["actorKey"].(string); ok {
			out[a] = true
		}
	}
	return out
}

// assertTrackerEvent asserts the op tracker for reqID records an event of class eventClass.
func assertTrackerEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found for %s: %v", reqID, err)
	}
	tr, err := processor.ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	for _, ec := range ecs {
		if ec == eventClass {
			return
		}
	}
	t.Fatalf("%s not in tracker eventClasses: %v", eventClass, ecs)
}

// countTrackerEventClass returns how many times eventClass appears in the
// op tracker's eventClasses list (one entry is appended per emitted event —
// multiple identity.rebound events, one per repointed credential, all land
// in the same tracker).
func countTrackerEventClass(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) int {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found for %s: %v", reqID, err)
	}
	tr, err := processor.ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	count := 0
	for _, ec := range ecs {
		if ec == eventClass {
			count++
		}
	}
	return count
}

// seedDuplicateCandidateEntry writes a simulated `duplicate-candidates`
// Lens output bucket entry. This mirrors how a real Refractor would write
// the entry after projecting the duplicateCandidates cypher; the test
// controls it directly to avoid needing a live Refractor.
//
// The bucket is a NATS KV store. The key format is:
//
//	flagged.identity.<loID>.identity.<hiID>
//
// and the value contains secondaryInboundEdges + secondaryOutboundEdges as
// the operator CLI would read before submitting MergeIdentity.
func seedDuplicateCandidateEntry(
	t *testing.T, ctx context.Context, conn *substrate.Conn,
	primaryKey, secondaryKey string,
	inboundEdges, outboundEdges []string,
) {
	t.Helper()

	// Build the bucket key: flagged.identity.<loID>.identity.<hiID>
	// The lens orders by key; in tests we just use primaryKey < secondaryKey.
	entry := map[string]any{
		"primaryKey":             primaryKey,
		"secondaryKey":           secondaryKey,
		"secondaryInboundEdges":  inboundEdges,
		"secondaryOutboundEdges": outboundEdges,
	}
	b, _ := json.Marshal(entry)

	js := conn.JetStream()
	// The pkgmgr installer records the Lens meta-vertex in core-kv but does NOT
	// create the output bucket (that's the Refractor's job at runtime). In tests
	// we create the bucket here so we can seed it directly, simulating what a
	// running Refractor would have projected.
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: "duplicate-candidates",
	})
	if err != nil {
		t.Fatalf("create/open duplicate-candidates KV: %v", err)
	}
	// Key: flagged.identity.<primaryID>.identity.<secondaryID>
	primaryID := primaryKey[len("vtx.identity."):]
	secondaryID := secondaryKey[len("vtx.identity."):]
	candidateKey := "flagged.identity." + primaryID + ".identity." + secondaryID
	if _, err := kv.Put(ctx, candidateKey, b); err != nil {
		t.Fatalf("seed duplicate-candidates entry %s: %v", candidateKey, err)
	}
}

// mergeReads builds the ContextHint.Reads slice for a MergeIdentity op:
// primary vertex, secondary vertex, their state aspects, and all edge keys.
func mergeReads(primaryKey, secondaryKey string, edges []string) []string {
	reads := []string{
		primaryKey,
		secondaryKey,
		primaryKey + ".state",
		primaryKey + ".mergedInto",
		secondaryKey + ".state",
		secondaryKey + ".mergedInto",
	}
	reads = append(reads, edges...)
	return reads
}

// mergeCredentialOptionalReads builds the credentialBinding half of
// ContextHint.OptionalReads for a MergeIdentity op (multi-credential-
// identity-linking-design.md §3.3).
func mergeCredentialOptionalReads(primaryKey, secondaryKey string) []string {
	return []string{
		secondaryKey + ".credentialBinding",
		primaryKey + ".credentialBinding",
	}
}

// mergePayload builds the JSON payload for a MergeIdentity op.
func mergePayload(primaryKey, secondaryKey string, edges []string) json.RawMessage {
	type payload struct {
		Primary   string   `json:"primary"`
		Secondary string   `json:"secondary"`
		Edges     []string `json:"edges"`
	}
	b, _ := json.Marshal(payload{
		Primary:   primaryKey,
		Secondary: secondaryKey,
		Edges:     edges,
	})
	return b
}
