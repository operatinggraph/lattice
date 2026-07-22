package objectsbase

// Rule-engine proof of the objectLiveness convergence cypher (the v1b GC orphan
// detection). These drive objectLivenessSpec through the `full` rule engine
// directly — the same engine selected at activation via engine:"full" — against
// an embedded NATS Core/Adjacency KV, asserting the projection ROW.
//
// The load-bearing properties pinned here (§20/§21):
//   - liveLinks=0 ⇒ orphaned; liveLinks>0 ⇒ not orphaned. Liveness is the
//     authoritative root-data counter, NOT the adjacency fan.
//   - ATTACH-LAG race guard (§21): a fresh attach commits the link AND
//     liveLinks=1 atomically, but refractor-adjacency lags — so an object with
//     liveLinks=1 and NO adjacency edge yet must NOT be flagged orphaned (the old
//     adjacency-count cypher reaped it → irreversible data loss).
//   - DEAD-TARGET is now a deferred leak, not reaped here: a stale liveLinks>=1
//     left by an owner-tombstone keeps the object un-reaped (a bounded byte leak,
//     never data loss); authoritative dead-target reclaim is the deferred
//     owner-cascade trigger's job (§21).
//   - ONE ROW PER ANCHOR even with several links (the §0.C guard).
//   - linkEpoch (the object's root-data link-set version) is projected for the
//     reclaim op's epoch-CAS.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func objCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	opts := &natsserver.Options{JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true, Port: natsserver.RANDOM_PORT}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-obj-cypher"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-obj-cypher"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-obj-cypher")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-obj-cypher")
	require.NoError(t, err)
	return adjKV, coreKV
}

func objCNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

type objLensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newObjLensFixture(t *testing.T) *objLensFixture {
	adjKV, coreKV := objCypherKVs(t)
	return &objLensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

// object writes an object vertex with the two GC scalars: data.linkEpoch (the
// re-link CAS version) and data.liveLinks (the authoritative live-link count the
// objectLiveness lens now decides orphan-ness on). Tests set liveLinks to the
// count the DDL would have maintained, INDEPENDENT of the adjacency edges built
// by link() — that independence is exactly what lets a test reproduce the
// attach-adjacency-lag race (liveLinks=1 with no adjacency edge yet).
func (f *objLensFixture) object(t *testing.T, name string, epoch, liveLinks int) string {
	t.Helper()
	id := objCNanoID(name)
	f.ids[name] = id
	f.types[id] = "object"
	key := "vtx.object." + id
	body := map[string]any{"key": key, "class": "object", "isDeleted": false,
		"data": map[string]any{"linkEpoch": epoch, "liveLinks": liveLinks}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

// content writes the object's .content aspect (the byte-plane metadata the
// objectAttachments display lens projects). The object vertex must already exist.
func (f *objLensFixture) content(t *testing.T, objName, storeName, contentType string, size int) {
	t.Helper()
	id := f.ids[objName]
	key := "vtx.object." + id + ".content"
	body := map[string]any{"key": key, "class": "object", "isDeleted": false,
		"data": map[string]any{"storeName": storeName, "contentType": contentType, "size": size}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// contentSensitive writes the object's .content aspect as a sensitive
// (crypto-shreddable) attach would leave it — the same shape attach_object's
// AttachObject DDL persists (packages/objects-base/ddls.go), so this proves
// the objectAttachments lens's P5-compliant read seam against the real
// on-wire document shape, not a hand-picked one.
func (f *objLensFixture) contentSensitive(t *testing.T, objName, storeName, contentType string, size int, governingIdentity string) {
	t.Helper()
	id := f.ids[objName]
	key := "vtx.object." + id + ".content"
	body := map[string]any{"key": key, "class": "object", "isDeleted": false,
		"data": map[string]any{
			"storeName": storeName, "contentType": contentType, "size": size,
			"digest": "SHA-256=sensitiveLensTestDigest", "sensitive": true, "governingIdentity": governingIdentity,
			"encryption": map[string]any{
				"algo": "AES-256-GCM", "nonce": "bm9uY2U=", "wrappedCEK": "d3JhcHBlZA==", "keyId": governingIdentity,
			},
		}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// owner writes an owner (identity) vertex, live or tombstoned (the dead-target case).
func (f *objLensFixture) owner(t *testing.T, name string, deleted bool) string {
	t.Helper()
	id := objCNanoID(name)
	f.ids[name] = id
	f.types[id] = "identity"
	key := "vtx.identity." + id
	body := map[string]any{"key": key, "class": "identity", "isDeleted": deleted, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

// link builds the object→owner adjacency edge (object is the source, Contract #1 §1.1).
func (f *objLensFixture) link(t *testing.T, name, objName, ownerName string) {
	t.Helper()
	ctx := context.Background()
	objID, ownerID := f.ids[objName], f.ids[ownerName]
	objType, ownerType := f.types[objID], f.types[ownerID]
	linkKey := "lnk." + objType + "." + objID + "." + name + "." + ownerType + "." + ownerID
	edgeID := name + "_" + objID + "_" + ownerID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: objID, OtherNodeID: ownerID, OtherType: ownerType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: ownerID, OtherNodeID: objID, OtherType: objType}))
}

func (f *objLensFixture) project(t *testing.T, objName string) []ruleengine.ProjectionResult {
	t.Helper()
	return f.projectSpec(t, objectLivenessSpec, objName)
}

func (f *objLensFixture) projectAttachments(t *testing.T, objName string) []ruleengine.ProjectionResult {
	t.Helper()
	return f.projectSpec(t, objectAttachmentsSpec, objName)
}

func (f *objLensFixture) projectSpec(t *testing.T, spec, objName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "cypher must parse on the full engine")
	objKey := "vtx.object." + f.ids[objName]
	now := time.Now().UTC().Format(time.RFC3339)
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": objKey, "now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// ownerKeys extracts the non-null owner keys from an objectAttachments `owners`
// column (the app's filter input), dropping the degenerate {ownerKey:null}
// artifact a zero-link object null-restores.
func ownerKeys(t *testing.T, owners any) []string {
	t.Helper()
	list, ok := owners.([]any)
	require.True(t, ok, "owners must be a list, got %T", owners)
	var out []string
	for _, e := range list {
		m, ok := e.(map[string]any)
		require.True(t, ok, "owners entry must be a map, got %T", e)
		if k, _ := m["ownerKey"].(string); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// Test 1 — liveLinks>0 ⇒ not orphaned; linkEpoch projected.
func TestObjectLiveness_OneLiveLink_NotOrphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	objKey := f.object(t, "photo", 3, 1)
	f.owner(t, "alice", false)
	f.link(t, "photoOf", "photo", "alice")

	rows := f.project(t, "photo")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, objKey, v["entityKey"])
	require.Equal(t, false, v["missing_owner"], "liveLinks>0 ⇒ not orphaned")
	require.Equal(t, false, v["violating"])
	require.EqualValues(t, 3, v["linkEpoch"], "linkEpoch is projected for the reclaim CAS")
}

// Test 2 — liveLinks=0 ⇒ orphaned (one null-restored row, not dropped).
func TestObjectLiveness_ZeroLinks_Orphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 5, 0)

	rows := f.project(t, "photo")
	require.Len(t, rows, 1, "a zero-link object null-restores to exactly one row, not dropped")
	v := rows[0].Values
	require.Equal(t, true, v["missing_owner"], "liveLinks=0 ⇒ orphaned")
	require.Equal(t, true, v["violating"])
	require.EqualValues(t, 5, v["linkEpoch"])
}

// Test 3 — THE §21 attach-adjacency-lag race guard (the data-loss bug this fix
// closes): a freshly-attached object commits its link AND liveLinks=1 atomically,
// but refractor-adjacency lags — so here liveLinks=1 with NO adjacency edge built
// at all. The object must NOT be flagged orphaned. The OLD adjacency-count cypher
// saw count(owner)=0 and reaped it → irreversible byte loss. This is the #1
// regression guard for this fix.
func TestObjectLiveness_AttachLag_NotOrphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 1, 1) // attached: liveLinks=1...
	f.owner(t, "alice", false) // ...owner exists in core-kv...
	// ...but NO f.link(): the adjacency edge has not been projected yet (lag).

	rows := f.project(t, "photo")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_owner"],
		"a freshly-attached object (liveLinks=1) must NOT be reaped while adjacency lags")
	require.Equal(t, false, v["violating"])
}

// Test 3b — DEAD-TARGET is now a deferred leak, NOT reaped by this lens. An
// owner-tombstone leaves a stale liveLinks>=1 (it never touches the object), so a
// dangling link to a dead owner keeps the object un-reaped BY THE LENS ALONE — the
// lens is intentionally lag-free-but-owner-blind (§21). Authoritative dead-target
// reclaim is the owner-tombstone-cascade's job (§22): it reacts to the owner's
// core-kv tombstone and submits DetachObject, which decrements liveLinks and lets
// the SAME Loop A+B reap the orphan — proven end-to-end by
// TestObjectGC_OwnerTombstoneCascadeReclaims. So this asserts the lens-only
// behaviour (unchanged); the cascade closes the loop around it. (Pre-§21 this
// asserted orphaned=true off the adjacency signal, which cannot be trusted
// without reaping fresh attaches — Test 3.)
func TestObjectLiveness_DeadTargetOwner_LeakedNotReaped(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 2, 1) // liveLinks stale at 1: the owner-tombstone never decremented it
	f.owner(t, "ghost", true)  // tombstoned owner
	f.link(t, "photoOf", "photo", "ghost")

	rows := f.project(t, "photo")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_owner"],
		"a dead-target dangling link is a deferred leak (stale liveLinks), not reaped here")
	require.Equal(t, false, v["violating"])
}

// Test 4 — several live links ⇒ exactly one row (the one-row-per-anchor guard).
func TestObjectLiveness_MultipleLiveLinks_OneRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 9, 2)
	f.owner(t, "alice", false)
	f.owner(t, "bob", false)
	f.link(t, "photoOf", "photo", "alice")
	f.link(t, "avatarOf", "photo", "bob")

	rows := f.project(t, "photo")
	require.Len(t, rows, 1, "exactly one row per object anchor even with several links")
	v := rows[0].Values
	require.Equal(t, false, v["missing_owner"])
	require.Equal(t, false, v["violating"])
}

// Test 5 — liveLinks>0 ⇒ not orphaned regardless of how many owners later die;
// the count is the authoritative signal.
func TestObjectLiveness_MultiOwnerLiveLinks_NotOrphaned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 4, 2) // two attaches → liveLinks=2
	f.owner(t, "alice", false) // live
	f.owner(t, "ghost", true)  // tombstoned
	f.link(t, "photoOf", "photo", "alice")
	f.link(t, "photoOf", "photo", "ghost")

	rows := f.project(t, "photo")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_owner"], "liveLinks=2 ⇒ not orphaned")
	require.Equal(t, false, v["violating"])
}

// objectAttachments display lens — the per-object byte-plane metadata the
// vertical apps read instead of Core KV (P5).

// Test A — the metadata (storeName/contentType/size) projects off .content and
// the owner key is collected, so an app can both stream and list the document.
func TestObjectAttachments_ProjectsMetadataAndOwner(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	objKey := f.object(t, "lease", 1, 1)
	f.content(t, "lease", "store-abc", "application/pdf", 4096)
	f.owner(t, "leaseapp", false)
	f.link(t, "signedLeasePdf", "lease", "leaseapp")

	rows := f.projectAttachments(t, "lease")
	require.Len(t, rows, 1, "exactly one row per object anchor")
	v := rows[0].Values
	require.Equal(t, objKey, v["entityKey"])
	require.Equal(t, "store-abc", v["storeName"], "storeName resolves a GET to the byte store")
	require.Equal(t, "application/pdf", v["contentType"])
	require.EqualValues(t, 4096, v["size"])
	require.Equal(t, []string{"vtx.identity." + f.ids["leaseapp"]}, ownerKeys(t, v["owners"]),
		"the owner key is collected so the app can list a leaseapp's documents")
}

// Test B — several owners collapse to one row carrying every owner key (the
// one-row-per-anchor guard + the list filter input).
func TestObjectAttachments_MultipleOwners_OneRowAllKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "doc", 2, 2)
	f.content(t, "doc", "store-xyz", "image/png", 100)
	f.owner(t, "alice", false)
	f.owner(t, "bob", false)
	f.link(t, "idDocument", "doc", "alice")
	f.link(t, "idDocument", "doc", "bob")

	rows := f.projectAttachments(t, "doc")
	require.Len(t, rows, 1, "several links collapse to exactly one row")
	keys := ownerKeys(t, rows[0].Values["owners"])
	require.ElementsMatch(t, []string{"vtx.identity." + f.ids["alice"], "vtx.identity." + f.ids["bob"]}, keys)
}

// Test C — a zero-link object null-restores to one row whose owners carry only
// the degenerate {ownerKey:null} artifact (dropped by the app) — the metadata
// still projects so a just-detached doc remains viewable until GC.
func TestObjectAttachments_ZeroLinks_OneRowNoOwners(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "orphan", 3, 0)
	f.content(t, "orphan", "store-orphan", "application/octet-stream", 7)

	rows := f.projectAttachments(t, "orphan")
	require.Len(t, rows, 1, "a zero-link object null-restores to exactly one row")
	v := rows[0].Values
	require.Equal(t, "store-orphan", v["storeName"])
	require.Empty(t, ownerKeys(t, v["owners"]), "no real owner key after the null artifact is dropped")
}

// Test D — a non-sensitive object's row projects null for the sensitive-object
// columns (Cypher missing-property semantics — the key is never written in the
// non-sensitive branch, object-store-crypto-shred-design.md §9 Fire 4
// Increment 2), never a zero-value that could be mistaken for "sensitive:false
// but present."
func TestObjectAttachments_NonSensitive_ProjectsNullEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "photo", 1, 1)
	f.content(t, "photo", "store-plain", "image/png", 10)

	rows := f.projectAttachments(t, "photo")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["sensitive"])
	require.Nil(t, v["governingIdentity"])
	require.Nil(t, v["encryption"])
}

// Test E — a sensitive object's row projects sensitive/governingIdentity/the
// FULL nested encryption envelope verbatim — the P5-compliant read seam a
// vertical app's decrypt-capable GET depends on in place of Loupe's direct
// Core-KV `.content` read (object-store-crypto-shred-design.md §9 Fire 4
// Increment 2). Proves the engine resolves a nested `.data.encryption.<field>`
// object as a single projected column, not just flat `.data.<field>` scalars.
func TestObjectAttachments_Sensitive_ProjectsEncryptionEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObjLensFixture(t)
	f.object(t, "idscan", 1, 1)
	applicantKey := f.owner(t, "applicant", false)
	governingIdentity := applicantKey
	f.contentSensitive(t, "idscan", "store-cipher", "image/jpeg", 2048, governingIdentity)
	f.link(t, "idDocument", "idscan", "applicant")

	rows := f.projectAttachments(t, "idscan")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["sensitive"])
	require.Equal(t, governingIdentity, v["governingIdentity"])
	require.Equal(t, "SHA-256=sensitiveLensTestDigest", v["digest"])
	enc, ok := v["encryption"].(map[string]any)
	require.True(t, ok, "encryption must project as a nested object, got %T", v["encryption"])
	require.Equal(t, "AES-256-GCM", enc["algo"])
	require.Equal(t, "d3JhcHBlZA==", enc["wrappedCEK"])
	require.Equal(t, governingIdentity, enc["keyId"])
}
