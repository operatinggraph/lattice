package identitydomain

// Rule-engine proof of the package's three lenses, driven through the `full`
// engine — the one activation selects via engine:"full" — against an embedded
// NATS Core/Adjacency KV. Same harness shape as edge-manifest / clinic-domain's
// lens cypher tests.
//
// identity-domain is the idiom source the rest of the corpus mirrors, and its
// lenses back three live read seams: the Gateway's provision-time probe
// (identityIndexHint), the account page's own-credentials view
// (identityCredentialsRead), and whoami's anchors[] (identityAnchors). Nothing
// until now executed any of their cypher.
//
// What only execution shows:
//
//   - identityCredentialsRead's `WHERE u.credentialBinding.data <> null` is
//     fail-closed, and its `authz_anchors` carries the reader's OWN NanoID.
//     Both halves are the whole access control: this row's single column is a
//     Vault-decrypted credential list, and an anchor naming anyone else hands
//     it to them through RLS while the table still looks correct.
//   - identityAnchors survives CONCATENATION: all four walks contributing to
//     one row, each stamping its own container. lenses_internal_test.go
//     already drives this spec relation-by-relation (the Osei regression, the
//     landlord hat) — that file is simply not named lens_cypher_test.go, so
//     S6's gate could not see it. What is added here is the together case and
//     the empty case, not a second copy of the per-relation proofs.
//   - an identity holding NONE of the four relations still projects a row of
//     purely degenerate {key:null,…} entries — the shape RealnessFilter:"key"
//     is premised on, and the only thing that lets a revoked binding retract.
//   - identityIndexHint projects the hash key and the identity it resolves to,
//     and nothing else: the index vertices are the dedup probes, and a column
//     carrying the pre-image would put an email back in an open KV bucket.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func idCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natstest.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-identitydomain-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-identitydomain-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-identitydomain-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-identitydomain-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// idNanoID returns a deterministic 20-char Contract #1 NanoID from a logical
// name (the edge-manifest / wellness-domain helper's derivation).
func idNanoID(name string) string {
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

type idFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newIDFixture(t *testing.T) *idFixture {
	adjKV, coreKV := idCypherKVs(t)
	return &idFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *idFixture) vtx(t *testing.T, name, typ string, data map[string]any) string {
	t.Helper()
	id := idNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	if data == nil {
		data = map[string]any{}
	}
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *idFixture) key(name string) string {
	return "vtx." + f.types[f.ids[name]] + "." + f.ids[name]
}

func (f *idFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := f.key(ownerName)
	k := owner + "." + local
	body := map[string]any{"key": k, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), k, raw)
	require.NoError(t, err)
}

func (f *idFixture) edge(t *testing.T, name, fromName, toName string) {
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

// named gives a vertex the generic presentation.name the anchors lens reads for
// its human label.
func (f *idFixture) named(t *testing.T, name, label string) {
	t.Helper()
	f.aspect(t, name, "presentation", "presentation", map[string]any{"name": label})
}

func (f *idFixture) project(t *testing.T, spec, actorKey string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "identity-domain lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": actorKey, "now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// ---------------------------------------------------------------- indexHint

func TestIdentityIndexHint_ProjectsTheHashKeyAndNothingElse(t *testing.T) {
	f := newIDFixture(t)

	// The index vertices are keyed by a one-way hash of the contact value; the
	// probe reads them to learn whether a registration would collide.
	f.vtx(t, "emailIdx", "identityindex", map[string]any{
		"identityKey": "vtx.identity." + idNanoID("resident"), "contactType": "email"})
	f.vtx(t, "phoneIdx", "identityindex", map[string]any{
		"identityKey": "vtx.identity." + idNanoID("resident"), "contactType": "phone"})
	// A non-index vertex the flat MATCH must not pick up.
	f.vtx(t, "resident", "identity", nil)

	rows := f.project(t, identityIndexHintSpec, "")
	require.Len(t, rows, 2, "one row per live identityindex vertex, and only those; got %v", rows)

	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r.Values["key"].(string)] = r.Values
	}
	require.Contains(t, byKey, f.key("emailIdx"))
	require.Contains(t, byKey, f.key("phoneIdx"))

	row := byKey[f.key("emailIdx")]
	require.Equal(t, "vtx.identity."+idNanoID("resident"), row["identityKey"])
	require.Equal(t, "email", row["contactType"])
	require.Len(t, row, 3,
		"the row carries only the hash key, the identity it resolves to, and the contact type — an extra column here is how the pre-image gets back into an open KV bucket")
}

// ---------------------------------------------------- credentials (Protected)

// idCredentialWorld seeds two identities: one that has claimed (a
// credentialBinding aspect exists) and one that never has.
func idCredentialWorld(t *testing.T) *idFixture {
	f := newIDFixture(t)
	f.vtx(t, "claimed", "identity", nil)
	f.vtx(t, "neverClaimed", "identity", nil)
	f.aspect(t, "claimed", "credentialBinding", "credentialBinding", map[string]any{
		"actorKey": "cred-abc",
		"boundAt":  "2026-07-26T00:00:00Z",
		"credentials": []any{
			map[string]any{"credentialKey": "cred-abc", "provider": "idp"},
		},
	})
	return f
}

func TestIdentityCredentialsRead_AnchorsOnTheReadersOwnNanoID(t *testing.T) {
	f := idCredentialWorld(t)

	rows := f.project(t, identityCredentialsReadSpec, "")
	require.Len(t, rows, 1, "only the claimed identity has a binding to read; got %v", rows)

	row := rows[0].Values
	require.Equal(t, idNanoID("claimed"), row["identity_id"])
	require.Equal(t, f.key("claimed"), row["entity_key"])
	require.Equal(t, f.key("claimed"), row["identity_key"],
		"identity_key is the SecureColumn's IdentityKeyColumn — the Vault decrypts this row against it, so it must be the owning identity's key")

	require.Equal(t, []any{idNanoID("claimed")}, row["authz_anchors"],
		"the row is self-anchored: RLS compares lattice.actor_id against this list, so any other NanoID here hands one identity's decrypted credential list to another")

	binding := row["binding"].(map[string]any)
	require.Equal(t, "cred-abc", binding["actorKey"])
	require.Len(t, binding["credentials"].([]any), 1,
		"the whole decrypted binding object projects into the one jsonb column — a bound-credential list only exists inside the ciphertext")
}

func TestIdentityCredentialsRead_UnclaimedIdentityProjectsNoRow(t *testing.T) {
	f := newIDFixture(t)
	// Every ingredient except the binding: the identity exists and is perfectly
	// live, it has simply never claimed.
	f.vtx(t, "neverClaimed", "identity", nil)
	f.aspect(t, "neverClaimed", "state", "state", map[string]any{"value": "unclaimed"})

	rows := f.project(t, identityCredentialsReadSpec, "")
	require.Empty(t, rows,
		"the REQUIRED-walk absence must fail closed — a row with a null binding would give RLS an unanchored row to leak. got %v", rows)
}

// -------------------------------------------------------------- anchors

// idAnchorWorld builds every relation the anchors lens walks, each with its own
// container, plus a bound provider entity on the INVERSE identifiedBy side:
//
//	staffer -residesIn->  unit1  -containedIn-> bldgHome
//	        -worksAt->    clinic -containedIn-> bldgWork
//	        -manages->    unit2  -containedIn-> bldgOwned
//	        <-identifiedBy- provider
func idAnchorWorld(t *testing.T) *idFixture {
	f := newIDFixture(t)

	f.vtx(t, "staffer", "identity", nil)
	f.vtx(t, "unit1", "unit", nil)
	f.vtx(t, "bldgHome", "building", nil)
	f.vtx(t, "clinic", "site", nil)
	f.vtx(t, "bldgWork", "building", nil)
	f.vtx(t, "unit2", "unit", nil)
	f.vtx(t, "bldgOwned", "building", nil)
	f.vtx(t, "provider", "provider", nil)

	f.named(t, "unit1", "Unit 1A")
	f.named(t, "bldgHome", "Maple Court")
	f.named(t, "clinic", "Front Clinic")
	f.named(t, "bldgWork", "Medical Plaza")
	f.named(t, "unit2", "Unit 2B")
	f.named(t, "bldgOwned", "Oak Court")

	f.edge(t, "residesIn", "staffer", "unit1")
	f.edge(t, "containedIn", "unit1", "bldgHome")
	f.edge(t, "worksAt", "staffer", "clinic")
	f.edge(t, "containedIn", "clinic", "bldgWork")
	f.edge(t, "manages", "staffer", "unit2")
	f.edge(t, "containedIn", "unit2", "bldgOwned")
	// Contract #1 §1.1: the later-arriving provider is the SOURCE of
	// identifiedBy, so the lens must walk this edge backwards.
	f.edge(t, "identifiedBy", "provider", "staffer")
	return f
}

// anchorsByRelation indexes a projected anchors[] list by its relation stamp.
func anchorsByRelation(t *testing.T, row map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, a := range row["anchors"].([]any) {
		e := a.(map[string]any)
		if e["key"] == nil {
			continue
		}
		rel, _ := e["relation"].(string)
		out[rel] = e
	}
	return out
}

func TestIdentityAnchors_StampsEveryRelationWithItsContainer(t *testing.T) {
	f := idAnchorWorld(t)

	rows := f.project(t, identityAnchorsSpec, f.key("staffer"))
	require.Len(t, rows, 1, "an actor-aggregate lens projects exactly one row per anchor; got %v", rows)
	require.Equal(t, f.key("staffer"), rows[0].Values["actorKey"])

	byRel := anchorsByRelation(t, rows[0].Values)
	require.Len(t, byRel, 4,
		"all four walks must contribute: residesIn, worksAt, manages, and the inverse identifiedBy. got %v", byRel)

	home := byRel["residesIn"]
	require.Equal(t, f.key("unit1"), home["key"])
	require.Equal(t, "Unit 1A", home["name"])
	require.Equal(t, f.key("bldgHome"), home["container"])
	require.Equal(t, "Maple Court", home["containerName"])

	work := byRel["worksAt"]
	require.Equal(t, f.key("clinic"), work["key"])
	require.Equal(t, f.key("bldgWork"), work["container"],
		"the workplace walk must stamp its OWN container — a clinic sits in a building, and the staff read paths key off which one")

	managed := byRel["manages"]
	require.Equal(t, f.key("unit2"), managed["key"],
		"the manages anchor is the landlord hat: a session carrying it is exactly one whose landlord console has rows")
	require.Equal(t, f.key("bldgOwned"), managed["container"])

	// The inverse walk in company: lenses_internal_test.go proves it in
	// isolation (the Osei regression), so what is added here is that it
	// survives concatenation with the three outbound walks.
	bound := byRel["identifiedBy"]
	require.Equal(t, f.key("provider"), bound["key"])
	require.Nil(t, bound["name"],
		"the walk is untyped so identity-domain stays domain-agnostic: a provider's name lives on .profile.data.fullName, which this lens has no way to resolve")
}

func TestIdentityAnchors_UnboundIdentityYieldsOnlyDegenerateEntries(t *testing.T) {
	f := idAnchorWorld(t)
	// A second identity in the same graph, holding none of the four relations —
	// the rows it must not inherit are all present.
	f.vtx(t, "unbound", "identity", nil)

	rows := f.project(t, identityAnchorsSpec, f.key("unbound"))
	require.Len(t, rows, 1,
		"the OPTIONAL walks must still project a row — the delete behavior needs one to evaluate; no row leaves anchors.<id> stranded after the last binding is revoked")
	require.Equal(t, f.key("unbound"), rows[0].Values["actorKey"])

	require.Empty(t, anchorsByRelation(t, rows[0].Values),
		"another identity's anchors must not leak through the $actorKey anchor; got %v", rows[0].Values["anchors"])

	for _, a := range rows[0].Values["anchors"].([]any) {
		require.Nil(t, a.(map[string]any)["key"],
			"every surviving entry is the degenerate {key:null,…} one RealnessFilter:\"key\" drops before EmptyBehavior evaluates")
	}
}

func TestIdentityCredentialsRead_ActivatesWithItsDeclaredIntoKey(t *testing.T) {
	eng := full.New()
	compiled, err := eng.Parse(identityCredentialsReadSpec)
	require.NoError(t, err)
	cr, ok := compiled.(*full.CompiledRule)
	require.True(t, ok)
	cr.KeyColumns = []string{"identity_id"}
	require.NoError(t, cr.ValidateKeyColumns(),
		"the lens must activate against its declared IntoKey — the activation-time gate a mis-declared key column dies on")
}
