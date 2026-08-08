package loftspacedomain

// Rule-engine proof of the applicantRosterRead SECURE-LENS cypher.
//
// These tests drive applicantRosterReadSpec through the `full` rule engine
// directly — the same engine selected at activation via engine:"full" —
// against an embedded NATS Core/Adjacency KV seeded with ENVELOPE-shaped
// sensitive aspects (the identity `name` holds only {ct, nonce, keyId} at
// rest, Contract #3 §3.10). They prove:
//
//   - a named identity projects exactly one row whose `name` column carries
//     the ciphertext envelope WHOLE (the shape pipeline/secure.go's
//     SecureDecryptor requires — it decrypts the map, never the engine);
//   - the WHERE keys on ciphertext PRESENCE (i.name.data.ct <> null): an
//     unnamed/service identity projects no row, and a hypothetical
//     plaintext-shaped name ({value: ...}, no ct) also projects no row —
//     this lens can never carry plaintext PII into the table by itself.
//
// The decrypt half (envelope → plaintext under the owning identity's DEK,
// shredded → NULL) is the platform's, proven in
// internal/refractor/pipeline/secure_internal_test.go.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type lensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string // logicalName -> bare NanoID
	types         map[string]string // bare NanoID -> vertex type
}

func newLensFixture(t *testing.T) *lensFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &lensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *lensFixture) identity(t *testing.T, name string) string {
	t.Helper()
	return f.vtx(t, name, "identity")
}

// vtx mints a bare vertex of the given type, tracked by logical name for
// aspect()/edge() to look up later.
func (f *lensFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *lensFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// edge wires a bidirectional adjacency link between two vtx()-minted nodes,
// mirroring cafe-domain's lens_cypher_test.go fixture (adjacency.Build,
// both directions — the rule engine's pattern-comprehension walks need the
// inbound edge too).
func (f *lensFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

func (f *lensFixture) project(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(applicantRosterReadSpec)
	require.NoError(t, err, "applicantRosterRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// envelopeData is an at-rest sensitive-aspect data map as step 6.5's
// encrypt-on-write commits it: base64 ct/nonce + the wrapping key id, no
// plaintext field.
func envelopeData() map[string]any {
	return map[string]any{"ct": "3q2+7w==", "nonce": "AAAAAAAAAAAAAAAA", "keyId": "k1"}
}

// TestApplicantRosterRead_ProjectsEnvelopeWholeForNamedIdentity proves a named
// identity (ciphertext-enveloped .name + .state) projects one row: the name
// column is the envelope MAP (for the secure decryptor), identity_key doubles
// naming the row's owner, authz_anchors carries at least the self-anchor.
func TestApplicantRosterRead_ProjectsEnvelopeWholeForNamedIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	aliceKey := f.identity(t, "alice")
	f.aspect(t, "alice", "name", "name", envelopeData())
	f.aspect(t, "alice", "state", "state", map[string]any{"value": "claimed"})

	rows := f.project(t)
	require.Len(t, rows, 1, "exactly one roster row for the one named identity")
	v := rows[0].Values
	require.Equal(t, f.ids["alice"], v["identity_id"], "identity_id is the bare NanoID (nanoIdFromKey)")
	require.Equal(t, aliceKey, v["entity_key"])
	require.Equal(t, aliceKey, v["identity_key"], "identity_key names the row's owner for its consumers; the decryptor opens the row under the holder the ciphertext names")
	require.Equal(t, "claimed", v["state"])
	name, ok := v["name"].(map[string]any)
	require.True(t, ok, "name must be the ciphertext envelope map, got %T (%v)", v["name"], v["name"])
	require.Equal(t, "3q2+7w==", name["ct"], "the envelope reaches the decryptor whole")
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", v["authz_anchors"])
	require.ElementsMatch(t, []any{f.ids["alice"]}, anchors,
		"no lease application means no fan-out, but the self-anchor must still be present")
}

// TestApplicantRosterRead_LandlordAndBuildingFanOut proves an applicant
// identity's authz_anchors carries the self-anchor PLUS the managing
// landlord's bare NanoID PLUS every building covering the unit they applied
// to — the roster gap: a landlord's cap-read.residence grant token is the
// landlord's own NanoID (mirroring landlordLeaseApplicationsRead's
// `[landlordKey] + [containedIn building tokens]`), and a worksAt-anchored
// staffer's grant token is one of the building keys, neither of which is the
// applicant's own NanoID, so without this fan-out a real landlord/staffer
// session matched no row but its own.
func TestApplicantRosterRead_LandlordAndBuildingFanOut(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	aliceKey := f.identity(t, "alice")
	f.aspect(t, "alice", "name", "name", envelopeData())
	f.vtx(t, "landlord", "identity")
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "unit4b", "unit")
	f.vtx(t, "tower", "building")
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "appliesToUnit", "app", "unit4b")
	f.edge(t, "manages", "landlord", "unit4b")
	f.edge(t, "containedIn", "unit4b", "tower")

	rows := f.project(t)
	require.Len(t, rows, 1)
	require.Equal(t, aliceKey, rows[0].Values["identity_key"])
	require.ElementsMatch(t,
		[]any{f.ids["alice"], f.ids["landlord"], f.ids["tower"]},
		rows[0].Values["authz_anchors"],
		"authz_anchors must carry the self-anchor PLUS the managing landlord and every building covering the applied-to unit")
}

// TestApplicantRosterRead_ExcludesUnnamedAndPlaintextShapedIdentities proves
// the ciphertext-presence WHERE: an identity with no .name aspect (a service
// actor) and an identity whose .name data is plaintext-shaped ({value},
// no ct — a shape step 6.5 can never commit) both project NO row, so the lens
// can neither roster unnamed actors nor carry plaintext PII by itself.
func TestApplicantRosterRead_ExcludesUnnamedAndPlaintextShapedIdentities(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.identity(t, "svc") // no .name at all
	f.identity(t, "legacy")
	f.aspect(t, "legacy", "name", "name", map[string]any{"value": "Plain Text"})
	namedKey := f.identity(t, "bob")
	f.aspect(t, "bob", "name", "name", envelopeData())
	f.aspect(t, "bob", "state", "state", map[string]any{"value": "unclaimed"})

	rows := f.project(t)
	require.Len(t, rows, 1, "only the ciphertext-named identity projects")
	require.Equal(t, namedKey, rows[0].Values["identity_key"])
}
