// Relationship projection — what a lens reads off a bound relationship
// variable, and what the engine refuses to let it write.
//
// A walk binds a relationship variable to the link it crossed, built from the
// adjacency entry the loop already holds, so `type(r)` and `r.key` cost no
// read; `r.data.<field>` costs one point-read of the link document. These
// tests pin that surface, its read cost in both directions, the cardinality
// rule that comes with it (a relationship is part of the row, so two links to
// one neighbour are two rows), the refusals for the shapes that would project
// a silent null, and the OPTIONAL-MATCH null the whole thing has to stay
// indistinguishable from.
package full

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// putLinkBody writes a Contract #1 link envelope into Core KV under the key the
// adjacency entry for the same edge carries, and returns the key and the
// revision it landed at. body is merged over the envelope's own `key`, so a
// caller supplies `data` (the link's payload) and `isDeleted`.
func putLinkBody(t *testing.T, reg *fixtureRegistry, coreKV *substrate.KV,
	name, fromName, toName string, body map[string]any,
) (string, uint64) {
	t.Helper()
	fromID, toID := reg.idByName[fromName], reg.idByName[toName]
	require.NotEmpty(t, fromID, "fixture: %q not registered", fromName)
	require.NotEmpty(t, toID, "fixture: %q not registered", toName)
	key := substrate.LinkKey(reg.typeByID[fromID], fromID, name, reg.typeByID[toID], toID)
	envelope := map[string]any{"key": key}
	for k, v := range body {
		envelope[k] = v
	}
	encoded, err := json.Marshal(envelope)
	require.NoError(t, err)
	rev, err := coreKV.Put(context.Background(), key, encoded)
	require.NoError(t, err)
	return key, rev
}

// TestRelProjection_TypeIsTheTraversedRelation pins type(r) on both hop
// shapes. The typed hop's answer is knowable from the pattern; the untyped
// hop's is not, and it is the one that matters — objects-base binds `-[r]->`
// precisely because the slot name is whatever the attach chose.
func TestRelProjection_TypeIsTheTraversedRelation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")

	typed := parseExec(t,
		`MATCH (o:object {key: $k})-[r:signedLeaseOf]->(owner:leaseapplication) RETURN type(r) AS rel`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}},
		adjKV, coreKV,
	)
	require.Len(t, typed, 1)
	require.Equal(t, "signedLeaseOf", typed[0].Values["rel"])

	untyped := parseExec(t,
		`MATCH (o:object {key: $k})-[r]->(owner:leaseapplication) RETURN type(r) AS rel`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}},
		adjKV, coreKV,
	)
	require.Len(t, untyped, 1)
	require.Equal(t, "signedLeaseOf", untyped[0].Values["rel"],
		"an untyped hop must report the relation it actually crossed, not the empty pattern type")

	upper := parseExec(t,
		`MATCH (o:object {key: $k})-[r]->(owner:leaseapplication) RETURN TYPE(r) AS rel`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}},
		adjKV, coreKV,
	)
	require.Len(t, upper, 1)
	require.Equal(t, "signedLeaseOf", upper[0].Values["rel"], "the function switch is case-insensitive")
}

// TestRelProjection_KeyIsTheContractOneLinkKey pins r.key against the link
// document a Processor commit would have written, byte for byte — this is the
// value DetachObject needs back out of the graph, so a key that merely looks
// right is not enough.
func TestRelProjection_KeyIsTheContractOneLinkKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")
	written := putLink(t, reg, coreKV, "signedLeaseOf", "doc", "lease")

	rows := parseExec(t,
		`MATCH (o:object {key: $k})-[r]->(owner:leaseapplication) RETURN r.key AS linkKey`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}},
		adjKV, coreKV,
	)
	require.Len(t, rows, 1)
	require.Equal(t, written, rows[0].Values["linkKey"])
	require.Equal(t,
		substrate.LinkKey("object", reg.idByName["doc"], "signedLeaseOf", "leaseapplication", reg.idByName["lease"]),
		rows[0].Values["linkKey"],
		"r.key is the 6-segment Contract #1 link key")
}

// TestRelProjection_OptionalMatchNullParity pins that a relationship a walk
// never found is Cypher NULL and nothing else: no error, and the same nil a
// real match would produce for an absent value. An error here would be worse
// than the silent null this feature replaces — every anchor with no
// attachment would stop projecting at all.
func TestRelProjection_OptionalMatchNullParity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "lonely", "object", nil)
	putVertex(t, reg, coreKV, "attached", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "attached", "lease")

	body := `MATCH (o:object {key: $k})
	         OPTIONAL MATCH (o)-[r:signedLeaseOf]->(owner:leaseapplication)
	         RETURN type(r) AS rel, r.key AS linkKey, owner.key AS ownerKey`

	zero := parseExec(t, body,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "lonely")}},
		adjKV, coreKV,
	)
	require.Len(t, zero, 1, "the anchor survives an OPTIONAL MATCH that found nothing")
	require.Nil(t, zero[0].Values["rel"])
	require.Nil(t, zero[0].Values["linkKey"])
	require.Nil(t, zero[0].Values["ownerKey"])

	// The positive vector: the same query over an anchor that does have the
	// relationship, so the nulls above are the absence of a match rather than a
	// projection that never worked.
	real := parseExec(t, body,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "attached")}},
		adjKV, coreKV,
	)
	require.Len(t, real, 1)
	require.Equal(t, "signedLeaseOf", real[0].Values["rel"])
	require.NotNil(t, real[0].Values["linkKey"])
}

// TestRelProjection_ConstrainedRelVariableMatchesOneLink pins the
// already-bound guard on a relationship variable: reusing `r` in a second
// clause means THE SAME link, exactly as reusing a node variable means the
// same node. Without the guard the second clause re-expands over every
// neighbour and the row count multiplies — silently, since each row still
// looks well-formed.
func TestRelProjection_ConstrainedRelVariableMatchesOneLink(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "admin", "role", map[string]any{"name": "admin"})
	putVertex(t, reg, coreKV, "auditor", "role", map[string]any{"name": "auditor"})
	putEdge(t, reg, adjKV, "holdsRole", "alice", "admin")
	putEdge(t, reg, adjKV, "holdsRole", "alice", "auditor")

	rows := parseExec(t,
		`MATCH (i:identity {key: $k})-[r:holdsRole]->(b:role)
		 MATCH (i)-[r:holdsRole]->(c:role)
		 RETURN b.name AS first, c.name AS second`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, rows, 2,
		"two links, each matching itself — an unconstrained re-expansion would cross-product to four rows")
	for _, row := range rows {
		require.Equal(t, row.Values["first"], row.Values["second"],
			"the second clause must arrive at the link the first one bound")
	}
}

// TestRelProjection_DistinctLinksBetweenOneEndpointPair pins the cardinality
// rule a bound relationship brings with it. Two links between the same pair
// under different relation names are two rows, because the relationship is
// part of what the row IS — an object attached under two slots is the live
// shape objects-base ships, and collapsing it by endpoint would hide a slot.
//
// The anonymous arm pins the other half: with no relationship variable named,
// the row is the endpoint alone and the cardinality is what it always was.
func TestRelProjection_DistinctLinksBetweenOneEndpointPair(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")
	putEdge(t, reg, adjKV, "photoOf", "doc", "lease")

	bound := parseExec(t,
		`MATCH (o:object {key: $k})-[r]->(owner:leaseapplication)
		 RETURN DISTINCT type(r) AS rel, owner.key AS ownerKey`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}},
		adjKV, coreKV,
	)
	require.Len(t, bound, 2)
	rels := map[string]bool{}
	for _, row := range bound {
		rels[row.Values["rel"].(string)] = true
		require.Equal(t, vtxKey(reg, "lease"), row.Values["ownerKey"])
	}
	require.Equal(t, map[string]bool{"signedLeaseOf": true, "photoOf": true}, rels)

	anonymous := parseExec(t,
		`MATCH (o:object {key: $k})-[]->(owner:leaseapplication)
		 RETURN DISTINCT owner.key AS ownerKey`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}},
		adjKV, coreKV,
	)
	require.Len(t, anonymous, 1,
		"with no relationship variable the row is the endpoint alone, and both links reach one endpoint")
}

// TestRelProjection_BindingAddsNoCoreKVRead is the §5.5 cost claim as a test:
// projecting type(r) and r.key costs nothing, because both values are already
// in the adjacency entry the walk holds.
//
// The read census is the evaluation footprint. NodeRevisions is built from the
// node memo (executor.footprint), and every Core KV point-read in the engine
// goes through fetchNode, which populates that memo and serves any repeat from
// it — so the footprint's key set IS the set of point-reads the evaluation
// performed. A link document read would show up as an `lnk.` key that the
// anonymous form does not have.
func TestRelProjection_BindingAddsNoCoreKVRead(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")
	linkKey := putLink(t, reg, coreKV, "signedLeaseOf", "doc", "lease")

	ec := ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}}
	readsFor := func(body string) map[string]uint64 {
		t.Helper()
		eng := New()
		cr, err := eng.Parse(body)
		require.NoError(t, err)
		rows, fp, err := eng.ExecuteWithFootprint(context.Background(), cr, ec, adjKV, coreKV)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return fp.NodeRevisions
	}

	anonymous := readsFor(
		`MATCH (o:object {key: $k})-[:signedLeaseOf]->(owner:leaseapplication) RETURN owner.key AS ownerKey`)
	binding := readsFor(
		`MATCH (o:object {key: $k})-[r:signedLeaseOf]->(owner:leaseapplication)
		 RETURN owner.key AS ownerKey, type(r) AS rel, r.key AS linkKey`)

	require.Equal(t, anonymous, binding,
		"binding the relationship and projecting type(r)/r.key must read exactly what the anonymous form reads")
	require.NotContains(t, binding, linkKey, "the link document is never read")
}

// TestRelProjection_TypeRefusesANonRelationship pins the fail-closed posture
// nanoIdFromKey set: a wrong argument is an error, not a null. A null here
// would be the same answer as "this row had no relationship", which is the
// exact confusion this projection exists to end.
func TestRelProjection_TypeRefusesANonRelationship(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})

	for _, tc := range []struct {
		name string
		body string
	}{
		{"a node binding", `MATCH (i:identity {key: $k}) RETURN type(i) AS rel`},
		{"a scalar", `MATCH (i:identity {key: $k}) RETURN type(i.name) AS rel`},
		{"two arguments", `MATCH (i:identity {key: $k}) RETURN type(i, i) AS rel`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := New()
			cr, err := eng.Parse(tc.body)
			require.NoError(t, err)
			_, err = eng.ExecuteWith(context.Background(), cr,
				ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
				adjKV, coreKV)
			require.Error(t, err)
			require.Contains(t, err.Error(), "type")
		})
	}
}

// TestParse_RelVariableOnVariableLengthHopRefused pins the first refusal. A
// multi-hop expansion crosses a different number of links per row, and `*0..`
// admits the source vertex having crossed none, so there is no single
// relationship for the variable to name. Binding the last hop would answer a
// question nobody asked.
func TestParse_RelVariableOnVariableLengthHopRefused(t *testing.T) {
	for _, body := range []string{
		`MATCH (a:identity)-[r:holdsRole*1..3]->(b:role) RETURN r.key AS k`,
		`MATCH (a:identity)-[r:holdsRole*0..]->(b:role) RETURN type(r) AS rel`,
		`MATCH (a:identity)-[r:holdsRole*0..2]->(b:role) RETURN a.key AS k`,
		`MATCH (a:identity)-[r:holdsRole*2..2]->(b:role) RETURN a.key AS k`,
	} {
		_, err := New().Parse(body)
		require.Errorf(t, err, "expected refusal: %s", body)
		require.Containsf(t, err.Error(), "`r`", "the refusal must name the variable: %s", body)
		require.Containsf(t, err.Error(), "variable-length hop", "%s", body)
	}

	// The same hops with no variable are untouched — the refusal is about
	// binding a relationship, not about walking several of them.
	for _, body := range []string{
		`MATCH (a:identity)-[:holdsRole*1..3]->(b:role) RETURN b.key AS k`,
		`MATCH (a:identity)-[:holdsRole*0..]->(b:role) RETURN b.key AS k`,
	} {
		_, err := New().Parse(body)
		require.NoErrorf(t, err, "an anonymous variable-length hop still parses: %s", body)
	}
}

// TestParse_RelVariableDereferenceRefused pins the second refusal: a property
// off a bound relationship that nothing answers. It resolves to null with no
// diagnostic anywhere, which is how the objects-base limitation was recorded
// as a comment instead of caught as an error. The link envelope's own fields
// are refused with everything else — the key is already projectable, and the
// rest is provenance no lens reads.
func TestParse_RelVariableDereferenceRefused(t *testing.T) {
	for _, body := range []string{
		`MATCH (o:object)-[r]->(owner) RETURN r.localName AS n`,
		`MATCH (o:object)-[r]->(owner) RETURN r.class AS c`,
		`MATCH (o:object)-[r]->(owner) WHERE r.sourceVertex = 'x' RETURN o.key AS k`,
		`MATCH (o:object)-[r]->(owner) WITH r.isDeleted AS d RETURN d AS out`,
		// A rename through a WITH carries the binding, so the dereference on
		// the far side is the same silent null under another name.
		`MATCH (o:object)-[r]->(owner) WITH r AS link RETURN link.localName AS n`,
	} {
		_, err := New().Parse(body)
		require.Errorf(t, err, "expected refusal: %s", body)
		require.Containsf(t, err.Error(), "silent null", "%s", body)
	}
}

// TestParse_RelVariableProjectableFormsAccepted is the positive vector for
// both refusals: the forms a bound relationship does answer, including through
// a WITH that carries and renames it.
func TestParse_RelVariableProjectableFormsAccepted(t *testing.T) {
	for _, body := range []string{
		`MATCH (o:object)-[r]->(owner) RETURN type(r) AS rel, r.key AS linkKey`,
		`MATCH (o:object)-[r]->(owner) RETURN r.data.filename AS f`,
		`MATCH (o:object)-[r]->(owner) RETURN r.data AS payload`,
		`MATCH (o:object)-[r]->(owner) WHERE r.data.filename = 'x' RETURN o.key AS k`,
		`MATCH (o:object)-[r:signedLeaseOf]->(owner) WHERE type(r) = 'signedLeaseOf' RETURN o.key AS k`,
		`MATCH (o:object)-[r]->(owner) WITH r AS link, o AS o RETURN link.key AS linkKey`,
		`MATCH (o:object)-[r]->(owner) WITH r AS link, o AS o RETURN link.data.filename AS f`,
		// The name is a relationship only where a pattern bound it: a WITH
		// that projects a value under the same name hands on a scalar, and
		// dereferencing THAT is an ordinary map access.
		`MATCH (o:object)-[r]->(owner) WITH owner.data AS r RETURN r.filename AS f`,
	} {
		_, err := New().Parse(body)
		require.NoErrorf(t, err, "expected acceptance: %s", body)
	}
}

// TestRelProjection_RequiredMatchBindingOnlyARelationshipIsFilteredNotDropped
// pins one half of what a relationship binding decides in applyMatch.
//
// isNonNullExpansion asks whether an expansion bound any NEW pattern variable
// for real, and applyMatch drops a required MATCH's rows that bound none. Where
// the clause's only new variable is the relationship, the relationship binding
// is the ONLY thing that can answer yes — so the whole clause hangs on it: with
// it, the rows are filtered by WHERE, which is what Cypher says the clause
// means; without it every row of that shape would die regardless of WHERE.
func TestRelProjection_RequiredMatchBindingOnlyARelationshipIsFilteredNotDropped(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "admin", "role", nil)
	putEdge(t, reg, adjKV, "holdsRole", "alice", "admin")

	ec := ruleengine.EventContext{Parameters: map[string]any{
		"i": vtxKey(reg, "alice"),
		"r": vtxKey(reg, "admin"),
	}}
	body := func(where string) string {
		return `MATCH (i:identity {key: $i})
		        MATCH (b:role {key: $r})
		        MATCH (i)-[rel:holdsRole]->(b) WHERE ` + where + `
		        RETURN i.key AS identityKey, type(rel) AS relation`
	}

	kept := parseExec(t, body(`type(rel) = 'holdsRole'`), ec, adjKV, coreKV)
	require.Len(t, kept, 1, "a satisfied WHERE keeps the row")
	require.Equal(t, "holdsRole", kept[0].Values["relation"])

	dropped := parseExec(t, body(`type(rel) = 'somethingElse'`), ec, adjKV, coreKV)
	require.Empty(t, dropped, "an unsatisfied WHERE drops the row")
}

// TestRelProjection_OptionalMatchBindingOnlyARelationshipHonoursWhere pins the
// other half. With the target already bound and only the relationship new, the
// relationship binding is again what makes the expansion a real match — so the
// WHERE applies to it, and a match it excludes stops being a match.
//
// The fixture carries TWO links between the one pair so both directions of
// that verdict are observable in one query: the WHERE admits one link and
// excludes the other, and the surviving row names the admitted one. Read as
// null-preserving instead, the clause would keep a row regardless of WHERE and
// name neither.
//
// The all-excluded case below is a different assertion and says so: with only
// the relationship new, the OPTIONAL null fallback and an unfiltered row carry
// exactly the same columns, so it cannot discriminate between the two
// readings. What it pins is that the WHERE never costs the anchor its row.
func TestRelProjection_OptionalMatchBindingOnlyARelationshipHonoursWhere(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "admin", "role", map[string]any{"name": "admin"})
	putEdge(t, reg, adjKV, "holdsRole", "alice", "admin")
	putEdge(t, reg, adjKV, "delegatesTo", "alice", "admin")

	ec := ruleengine.EventContext{Parameters: map[string]any{
		"i": vtxKey(reg, "alice"),
		"r": vtxKey(reg, "admin"),
	}}
	body := func(where string) string {
		return `MATCH (i:identity {key: $i})
		        MATCH (b:role {key: $r})
		        OPTIONAL MATCH (i)-[rel]->(b) WHERE ` + where + `
		        RETURN i.key AS identityKey, type(rel) AS relation`
	}

	one := parseExec(t, body(`type(rel) = 'holdsRole'`), ec, adjKV, coreKV)
	require.Len(t, one, 1, "one of the two links passes the WHERE, the other is excluded")
	require.Equal(t, "holdsRole", one[0].Values["relation"],
		"the surviving row is the admitted link — a clause that ignored its WHERE could name neither")

	other := parseExec(t, body(`type(rel) = 'delegatesTo'`), ec, adjKV, coreKV)
	require.Len(t, other, 1)
	require.Equal(t, "delegatesTo", other[0].Values["relation"],
		"and the verdict follows the predicate, not the order the edges happen to be stored in")

	all := parseExec(t, body(`type(rel) = 'nothingAtAll'`), ec, adjKV, coreKV)
	require.Len(t, all, 1, "the anchor survives — this is still an OPTIONAL MATCH")
	require.Nil(t, all[0].Values["relation"],
		"with every match excluded the clause yields the OPTIONAL null, not a dropped anchor")
}

// TestRelProjection_BothEndpointsBoundFansOutPerLink pins the cardinality of a
// clause whose endpoints are BOTH already bound. Naming the relationship puts
// it in the row, so two links between one pair are two expansions of that pair;
// leaving it anonymous makes the row the endpoint alone, so the same two links
// are one. A lens that names a relationship on such a clause therefore
// multiplies its rows per link — the clause does not merely produce a row, it
// can produce several.
//
// The anonymous control is taken on the OPTIONAL form: a required anonymous
// clause binding no new variable is discarded wholesale, which is a separate
// open defect this test deliberately does not exercise.
func TestRelProjection_BothEndpointsBoundFansOutPerLink(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "admin", "role", nil)
	putEdge(t, reg, adjKV, "holdsRole", "alice", "admin")
	putEdge(t, reg, adjKV, "delegatesTo", "alice", "admin")

	ec := ruleengine.EventContext{Parameters: map[string]any{
		"i": vtxKey(reg, "alice"),
		"r": vtxKey(reg, "admin"),
	}}
	relationsOf := func(rows []ruleengine.ProjectionResult) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			rel, _ := row.Values["relation"].(string)
			out = append(out, rel)
		}
		sort.Strings(out)
		return out
	}

	required := parseExec(t,
		`MATCH (i:identity {key: $i})
		 MATCH (b:role {key: $r})
		 MATCH (i)-[rel]->(b)
		 RETURN i.key AS identityKey, type(rel) AS relation`,
		ec, adjKV, coreKV)
	require.Equal(t, []string{"delegatesTo", "holdsRole"}, relationsOf(required),
		"a required MATCH over two links between one bound pair is two rows, one per link")

	optional := parseExec(t,
		`MATCH (i:identity {key: $i})
		 MATCH (b:role {key: $r})
		 OPTIONAL MATCH (i)-[rel]->(b)
		 RETURN i.key AS identityKey, type(rel) AS relation`,
		ec, adjKV, coreKV)
	require.Equal(t, []string{"delegatesTo", "holdsRole"}, relationsOf(optional),
		"and so is the OPTIONAL form — the null fallback is for a pair with no link at all")

	// The anonymous form is the control: with no relationship in the row, both
	// links are the same one expansion of the same pair. It is taken on the
	// OPTIONAL form, because the REQUIRED anonymous clause introduces no new
	// variable at all and applyMatch drops every row of it — a separate,
	// still-open defect this change narrows the surface of without resolving.
	anonymous := parseExec(t,
		`MATCH (i:identity {key: $i})
		 MATCH (b:role {key: $r})
		 OPTIONAL MATCH (i)-[]->(b)
		 RETURN i.key AS identityKey`,
		ec, adjKV, coreKV)
	require.Len(t, anonymous, 1,
		"the endpoint pair is one row however many links join it, when no relationship is named")
}

// TestRelProjection_DataResolvesTheLinkPayload pins `r.data.<field>` against
// the link document a Processor commit writes — the fact that lives on the
// edge and nowhere else (an attachment's filename, a binding's provenance).
//
// The resolution is two steps and both are pinned here: `r.data` point-reads
// the link and yields its payload object, and the field comes off that object
// as ordinary map navigation. An empty payload therefore answers nil for every
// field rather than erroring, and so does a link that is absent or tombstoned
// — the same nil a missing field gives, which is what Cypher's
// missing-property semantics require.
func TestRelProjection_DataResolvesTheLinkPayload(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	for _, name := range []string{"withData", "emptyData", "noLink", "tombstoned"} {
		putVertex(t, reg, coreKV, name, "object", nil)
		putEdge(t, reg, adjKV, "signedLeaseOf", name, "lease")
	}
	putLinkBody(t, reg, coreKV, "signedLeaseOf", "withData", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{"filename": "lease.pdf"}})
	putLinkBody(t, reg, coreKV, "signedLeaseOf", "emptyData", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{}})
	putLinkBody(t, reg, coreKV, "signedLeaseOf", "tombstoned", "lease",
		map[string]any{"isDeleted": true, "data": map[string]any{"filename": "gone.pdf"}})
	// "noLink" gets no link document at all — the adjacency entry exists and
	// the walk crosses it, but Core KV has nothing at the link key.

	body := `MATCH (o:object {key: $k})-[r]->(owner:leaseapplication)
	         RETURN r.data.filename AS filename, r.data AS payload`

	for _, tc := range []struct {
		anchor   string
		filename any
		payload  any
	}{
		{"withData", "lease.pdf", map[string]any{"filename": "lease.pdf"}},
		{"emptyData", nil, map[string]any{}},
		{"noLink", nil, nil},
		{"tombstoned", nil, nil},
	} {
		t.Run(tc.anchor, func(t *testing.T) {
			rows := parseExec(t, body,
				ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, tc.anchor)}},
				adjKV, coreKV,
			)
			require.Len(t, rows, 1)
			require.Equal(t, tc.filename, rows[0].Values["filename"])
			require.Equal(t, tc.payload, rows[0].Values["payload"])
		})
	}
}

// TestRelProjection_DataOnAnOptionalNullStaysNull keeps the payload column on
// the same null as the rest of the relationship surface: an OPTIONAL MATCH
// that found nothing must answer nil for `r.data.f` exactly as it does for
// `type(r)`, and must not error. An index whose entries are read from one
// place and gated from another has to agree about absence — here the absence
// of a MATCH and the absence of a payload are both nil, and the row survives
// either way.
func TestRelProjection_DataOnAnOptionalNullStaysNull(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "lonely", "object", nil)
	putVertex(t, reg, coreKV, "attached", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "attached", "lease")
	putLinkBody(t, reg, coreKV, "signedLeaseOf", "attached", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{"filename": "lease.pdf"}})

	body := `MATCH (o:object {key: $k})
	         OPTIONAL MATCH (o)-[r:signedLeaseOf]->(owner:leaseapplication)
	         RETURN type(r) AS rel, r.key AS linkKey, r.data.filename AS filename`

	zero := parseExec(t, body,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "lonely")}},
		adjKV, coreKV,
	)
	require.Len(t, zero, 1)
	require.Nil(t, zero[0].Values["rel"])
	require.Nil(t, zero[0].Values["linkKey"])
	require.Nil(t, zero[0].Values["filename"])

	matched := parseExec(t, body,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "attached")}},
		adjKV, coreKV,
	)
	require.Len(t, matched, 1)
	require.Equal(t, "lease.pdf", matched[0].Values["filename"],
		"the positive vector: the nulls above are an absent match, not a projection that never worked")
}

// TestRelProjection_DataReadEntersTheFootprint asserts the §5.2 claim directly
// rather than inferring it from the memo's shape: a lens that dereferences a
// link's payload records the LINK KEY in the evaluation footprint at the
// revision it observed. That is what makes a concurrent write to a projected
// link detectable by the existing evaluation-consistency seam, which re-reads
// every NodeRevisions entry with no regard for key shape.
func TestRelProjection_DataReadEntersTheFootprint(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")
	linkKey, revision := putLinkBody(t, reg, coreKV, "signedLeaseOf", "doc", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{"filename": "lease.pdf"}})

	eng := New()
	cr, err := eng.Parse(
		`MATCH (o:object {key: $k})-[r]->(owner:leaseapplication) RETURN r.data.filename AS filename`)
	require.NoError(t, err)
	rows, fp, err := eng.ExecuteWithFootprint(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}}, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	observed, present := fp.NodeRevisions[linkKey]
	require.Truef(t, present, "the link key must be in the read-surface footprint: %v", fp.NodeRevisions)
	require.Equal(t, revision, observed, "at the revision the evaluation observed")
}

// TestRelProjection_DataAddsNoSelectorDegradation pins that reading a link's
// payload changes the ADJACENCY read surface not at all. The selector
// footprint is driven by the hop's relation type and direction, neither of
// which a projection touches, so a lens that dereferences a link must record
// the same selectors — and the same Fallback verdict — as the anonymous form
// of the same walk. A degradation here would silently widen every such lens to
// whole-document comparison.
func TestRelProjection_DataAddsNoSelectorDegradation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")
	putLinkBody(t, reg, coreKV, "signedLeaseOf", "doc", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{"filename": "lease.pdf"}})

	ec := ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}}
	footprintFor := func(body string) ruleengine.EvalFootprint {
		t.Helper()
		eng := New()
		cr, err := eng.Parse(body)
		require.NoError(t, err)
		rows, fp, err := eng.ExecuteWithFootprint(context.Background(), cr, ec, adjKV, coreKV)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return fp
	}

	anonymous := footprintFor(
		`MATCH (o:object {key: $k})-[:signedLeaseOf]->(owner:leaseapplication) RETURN owner.key AS ownerKey`)
	dereferencing := footprintFor(
		`MATCH (o:object {key: $k})-[r:signedLeaseOf]->(owner:leaseapplication)
		 RETURN owner.key AS ownerKey, r.data.filename AS filename`)

	require.Equal(t, anonymous.EdgeSelectors, dereferencing.EdgeSelectors,
		"the selector footprint is the hop's, and the hop is unchanged")
	require.Equal(t, anonymous.EdgeRevisions, dereferencing.EdgeRevisions)
}

// TestRelProjection_LinkReadIsMemoizedWithinOneEvaluation pins the link read
// on the executor's repeatable-read contract, the same one aspects have: once
// an evaluation has read a link, every later access inside THAT evaluation
// answers from the memo, so a link a query dereferences in two clauses costs
// one read and cannot supply two different values to one row.
//
// The mutation stands in for a Processor commit landing mid-evaluation, driven
// explicitly so the proof is deterministic.
func TestRelProjection_LinkReadIsMemoizedWithinOneEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")
	linkKey, _ := putLinkBody(t, reg, coreKV, "signedLeaseOf", "doc", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{"filename": "first.pdf"}})

	ex := newTestExecutor(adjKV, coreKV)
	relRef := &nodeRef{key: linkKey, rel: "signedLeaseOf"}

	first, err := ex.resolveProperty(relRef, "data")
	require.NoError(t, err)
	require.Equal(t, "first.pdf", propertyOf(first, "filename"))

	putLinkBody(t, reg, coreKV, "signedLeaseOf", "doc", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{"filename": "second.pdf"}})

	second, err := ex.resolveProperty(relRef, "data")
	require.NoError(t, err)
	require.Equal(t, "first.pdf", propertyOf(second, "filename"),
		"a second access inside ONE evaluation must observe the value the evaluation already saw")

	next := newTestExecutor(adjKV, coreKV)
	fresh, err := next.resolveProperty(&nodeRef{key: linkKey, rel: "signedLeaseOf"}, "data")
	require.NoError(t, err)
	require.Equal(t, "second.pdf", propertyOf(fresh, "filename"),
		"a fresh evaluation must observe the committed value, or the read model would never catch up")
}

// TestRelProjection_ReadFreeModeRefusesALinkRead pins that the read-free
// key-resolution mode (the anchor-tombstone delete path, which builds an
// executor with no Core KV) reports a link payload as unresolvable instead of
// panicking or answering nil. A nil there would resolve a delete key from a
// value nothing read.
func TestRelProjection_ReadFreeModeRefusesALinkRead(t *testing.T) {
	ex := newTestExecutor(nil, nil)
	_, err := ex.resolveProperty(&nodeRef{key: "lnk.object.a.signedLeaseOf.leaseapplication.b", rel: "signedLeaseOf"}, "data")
	require.ErrorIs(t, err, errCoreKVReadDisabled)
}

// TestRelBindings_ReportsWhatEachBindingIsReadFor pins the compile-time
// diagnostic the corpus census runs on. It answers a cost question as much as
// a shape one — `type` and `key` are free, `data` is a point-read per
// traversed edge — so a lens acquiring `data` has to become visible somewhere,
// and a read made through a WITH rename is a read of the relationship the
// pattern bound, not of a second one.
func TestRelBindings_ReportsWhatEachBindingIsReadFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []RelBinding
	}{
		{
			name: "bound but never read",
			body: `MATCH (o:object)-[r]->(owner) RETURN o.key AS k`,
			want: []RelBinding{{Variable: "r"}},
		},
		{
			name: "the free surface",
			body: `MATCH (o:object)-[r:attachedTo]->(owner) RETURN type(r) AS slot, r.key AS linkKey`,
			want: []RelBinding{{Variable: "r", Type: "attachedTo", Reads: []string{"key", "type"}}},
		},
		{
			name: "the paid surface",
			body: `MATCH (o:object)-[r]->(owner) RETURN r.data.filename AS f`,
			want: []RelBinding{{Variable: "r", Reads: []string{"data"}}},
		},
		{
			name: "read through a WITH rename",
			body: `MATCH (o:object)-[r]->(owner) WITH r AS link, o AS o RETURN link.data.filename AS f`,
			want: []RelBinding{{Variable: "r", Reads: []string{"data"}}},
		},
		{
			name: "two bindings",
			body: `MATCH (o:object)-[r]->(owner) MATCH (owner)-[q:holdsRole]->(role) RETURN type(q) AS t, r.key AS k`,
			want: []RelBinding{
				{Variable: "q", Type: "holdsRole", Reads: []string{"type"}},
				{Variable: "r", Reads: []string{"key"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr, err := New().Parse(tc.body)
			require.NoError(t, err)
			require.Equal(t, tc.want, cr.(*CompiledRule).RelBindings())
		})
	}
}

// TestParse_RelBindingCannotBeSmuggledPastTheGate walks the routes a
// relationship binding can take to a place the gate was not looking. Each one
// ends in the same place if it is missed: a real link-envelope value in a
// projected column, out of a query the gate reports as clean.
//
// The rule the gate enforces is about the BINDING, not about the name it
// arrived under, so every route to it has to be closed at once — a carry
// through a function that returns its argument, a pattern's inline property
// map, a use of the relationship as a value, and a name a WITH stopped
// carrying.
func TestParse_RelBindingCannotBeSmuggledPastTheGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			// coalesce returns the argument it selects, so the binding lands
			// under the alias with none of the alias's own provenance.
			name: "carried by coalesce",
			body: `MATCH (o:object)-[r]->(x) WITH coalesce(r, r) AS rr, x AS x RETURN rr.localName AS n`,
			want: "used as a value",
		},
		{
			name: "carried by a CASE branch",
			body: `MATCH (o:object)-[r]->(x) WITH CASE WHEN 1 = 1 THEN r ELSE r END AS rr, x AS x RETURN rr.class AS c`,
			want: "used as a value",
		},
		{
			name: "carried by max",
			body: `MATCH (o:object)-[r]->(x) WITH max(r) AS rr, x AS x RETURN rr.localName AS n`,
			want: "used as a value",
		},
		{
			// A map literal keeps the binding as a member, and navigating back
			// in returns it.
			name: "captured in a map literal",
			body: `MATCH (o:object)-[r]->(x) WITH {slot: r} AS m, x AS x RETURN m.slot.localName AS n`,
			want: "used as a value",
		},
		{
			// A pattern's inline property map is evaluated by seedNodes and
			// propsAllMatch exactly as a WHERE is, and its value decides which
			// vertices the walk even looks at.
			name: "read from a pattern property map",
			body: `MATCH (o:object)-[r]->(x) MATCH (y {key: r.localName}) RETURN y.key AS k`,
			want: "silent null",
		},
		{
			name: "referenced after a WITH that dropped it",
			body: `MATCH (o:object)-[r]->(x) WITH x AS y RETURN type(r) AS slot`,
			want: "does not carry it",
		},
		{
			name: "dereferenced after a WITH that dropped it",
			body: `MATCH (o:object)-[r]->(x) WITH x AS y RETURN r.key AS lk`,
			want: "does not carry it",
		},
		{
			name: "returned as an entity",
			body: `MATCH (o:object)-[r]->(x) RETURN r AS rel`,
			want: "used as a value",
		},
		{
			// count() folds on a bare `v != nil`, so a bound relationship
			// increments a count that was structurally always zero.
			name: "counted",
			body: `MATCH (o:object)-[r]->(x) RETURN count(r) AS n`,
			want: "used as a value",
		},
		{
			name: "collected",
			body: `MATCH (o:object)-[r]->(x) RETURN collect(r) AS rels`,
			want: "used as a value",
		},
		{
			name: "carried through a WITH and then returned",
			body: `MATCH (o:object)-[r]->(x) WITH r AS rr, count(x) AS n RETURN rr AS out, n AS n`,
			want: "used as a value",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New().Parse(tc.body)
			require.Error(t, err, "the gate must refuse this")
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestParse_RelBindingGateDoesNotOverRefuse is the other side of the same
// coin. A gate that refuses more than it means to is a gate authors route
// around, so the shapes that legitimately read a relationship — and the ones
// that merely reuse a NAME that once held one — must all still compile.
func TestParse_RelBindingGateDoesNotOverRefuse(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"the whole projectable surface",
			`MATCH (o:object)-[r]->(x) RETURN type(r) AS s, r.key AS lk, r.data.filename AS f`},
		{"carried through a WITH under a new name",
			`MATCH (o:object)-[r]->(x) WITH r AS link, x AS x RETURN link.data.filename AS f`},
		{"filtered on the payload",
			`MATCH (o:object)-[r]->(x) WHERE r.data.filename = 'a' RETURN o.key AS k`},
		{"collected into a map, the objects-base shape",
			`MATCH (o:object)-[r]->(x) WITH collect(DISTINCT {a: type(r), b: r.data.filename}) AS owners, o.key AS ek
			 RETURN ek AS ek, owners AS owners`},
		{"the link key used in a pattern property map",
			`MATCH (o:object)-[r]->(x) MATCH (y:unit {key: r.key}) RETURN y.key AS k`},
		{"a WITH rebinding the name to a value of its own",
			`MATCH (o:object)-[r]->(x) WITH x.data AS r, o.key AS k RETURN r.filename AS f, k AS k`},
		{"a later MATCH binding the name afresh",
			`MATCH (o:object)-[r]->(x) WITH o AS o, x AS x MATCH (o)-[r:attachedTo]->(x) RETURN type(r) AS s`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New().Parse(tc.body)
			require.NoError(t, err)
		})
	}
}

// TestRelProjection_ResolverRefusesANonProjectableProperty pins the backstop
// under the parse gate. Parse is where an author is told; this is what holds if
// a shape ever reaches evaluation without having been told — and it holds by
// ERRORING, because answering with nil would be the same undiagnosable empty
// column the parse gate exists to prevent.
func TestRelProjection_ResolverRefusesANonProjectableProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")
	linkKey, _ := putLinkBody(t, reg, coreKV, "signedLeaseOf", "doc", "lease",
		map[string]any{"isDeleted": false, "localName": "signedLeaseOf", "data": map[string]any{}})

	ex := newTestExecutor(adjKV, coreKV)
	relRef := &nodeRef{key: linkKey, rel: "signedLeaseOf"}

	for _, property := range []string{"localName", "class", "sourceVertex", "isDeleted"} {
		v, err := ex.resolveProperty(relRef, property)
		require.Errorf(t, err, "resolving %q off a relationship must error", property)
		require.Contains(t, err.Error(), "no projectable property")
		require.Nil(t, v, "and it must never serve the value")
	}

	payload, err := ex.resolveProperty(relRef, "data")
	require.NoError(t, err, "the projectable properties still resolve")
	require.NotNil(t, payload)
}

// TestRelProjection_AnEdgeWithNoLinkKeyBindsNothing pins the fail-closed drop
// for an adjacency entry that carries no Contract #1 link key. The legacy event
// path indexes an edge off any Core KV message carrying a nodeId, taking the
// key and relation verbatim from the body — adjacency's own overflow latch
// refuses to engage on a node holding such edges for exactly this reason.
// Binding one would project a malformed key, and a payload dereference would
// point-read it and fail the whole evaluation rather than degrade.
func TestRelProjection_AnEdgeWithNoLinkKeyBindsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	objKey := putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	objID, leaseID := reg.idByName["doc"], reg.idByName["lease"]
	// An edge as the legacy path builds one: no link key at all.
	require.NoError(t, adjacency.Build(context.Background(), adjKV, adjacency.CoreKVEvent{
		CoreKvKey: "", EdgeID: "legacy_" + objID + "_" + leaseID, Name: "signedLeaseOf",
		Direction: "outbound", NodeID: objID, OtherNodeID: leaseID, OtherType: "leaseapplication",
	}))

	bound := parseExec(t,
		`MATCH (o:object {key: $k})-[r]->(owner:leaseapplication) RETURN type(r) AS rel, r.key AS linkKey`,
		ruleengine.EventContext{Parameters: map[string]any{"k": objKey}},
		adjKV, coreKV,
	)
	require.Empty(t, bound, "an edge with no link key cannot bind a relationship, so it yields no row")

	// The same walk with no relationship variable is untouched: the edge is
	// still a perfectly good hop to the neighbour.
	anonymous := parseExec(t,
		`MATCH (o:object {key: $k})-[]->(owner:leaseapplication) RETURN owner.key AS ownerKey`,
		ruleengine.EventContext{Parameters: map[string]any{"k": objKey}},
		adjKV, coreKV,
	)
	require.Len(t, anonymous, 1, "the hop itself is unaffected — only the binding is refused")
}

// TestRelProjection_WhereOnTheLinkPayloadFiltersRows pins `WHERE r.data.f = …`
// behaviourally, not merely as something that parses.
//
// It is a ROW filter applied after the expansion, not an edge filter: the walk
// still consults the same relation and direction, so recordEdgeSelector records
// the same selectors and certifies the same footprint. That distinction is the
// whole reason a payload predicate is admissible where a data-driven EDGE
// filter would not be — a filter the walk itself honoured could not be
// expressed as a selector, and every lens using one would fall back to
// whole-document comparison. The selector assertion below is that worry,
// proven absent.
func TestRelProjection_WhereOnTheLinkPayloadFiltersRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "doc", "object", nil)
	putVertex(t, reg, coreKV, "lease", "leaseapplication", nil)
	putEdge(t, reg, adjKV, "signedLeaseOf", "doc", "lease")
	putEdge(t, reg, adjKV, "photoOf", "doc", "lease")
	putLinkBody(t, reg, coreKV, "signedLeaseOf", "doc", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{"filename": "lease.pdf"}})
	putLinkBody(t, reg, coreKV, "photoOf", "doc", "lease",
		map[string]any{"isDeleted": false, "data": map[string]any{"filename": "snap.png"}})

	ec := ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "doc")}}
	body := func(where string) string {
		return `MATCH (o:object {key: $k})-[r]->(owner:leaseapplication) WHERE ` + where + `
		        RETURN type(r) AS rel, r.data.filename AS filename`
	}

	matched := parseExec(t, body(`r.data.filename = 'lease.pdf'`), ec, adjKV, coreKV)
	require.Len(t, matched, 1, "the predicate keeps the one link whose payload satisfies it")
	require.Equal(t, "signedLeaseOf", matched[0].Values["rel"])
	require.Equal(t, "lease.pdf", matched[0].Values["filename"])

	other := parseExec(t, body(`r.data.filename = 'snap.png'`), ec, adjKV, coreKV)
	require.Len(t, other, 1)
	require.Equal(t, "photoOf", other[0].Values["rel"])

	none := parseExec(t, body(`r.data.filename = 'absent.txt'`), ec, adjKV, coreKV)
	require.Empty(t, none, "a predicate no link satisfies keeps none of them")

	// The walk's own read surface is untouched by any of it.
	footprintFor := func(spec string) ruleengine.EvalFootprint {
		t.Helper()
		eng := New()
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		_, fp, err := eng.ExecuteWithFootprint(context.Background(), cr, ec, adjKV, coreKV)
		require.NoError(t, err)
		return fp
	}
	anonymous := footprintFor(
		`MATCH (o:object {key: $k})-[]->(owner:leaseapplication) RETURN owner.key AS ownerKey`)
	filtered := footprintFor(body(`r.data.filename = 'lease.pdf'`))
	require.Equal(t, anonymous.EdgeSelectors, filtered.EdgeSelectors,
		"a payload predicate is a row filter — the hop's selector footprint is byte-identical to the "+
			"anonymous walk's, so no lens degrades to whole-document comparison")
	require.Equal(t, anonymous.EdgeRevisions, filtered.EdgeRevisions)
}

// TestParse_ANameRebornAfterADropIsNotTheRelationship keeps the
// dropped-reference refusal from spreading to names that are bound again. The
// refusal is about a reference to nothing, so anything that binds the name
// afresh — a later MATCH, or a WITH projecting a value under it — ends it.
func TestParse_ANameRebornAfterADropIsNotTheRelationship(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"rebound by a later WITH",
			`MATCH (o:object)-[r]->(x) WITH x AS y, o AS o WITH y AS r, o AS o RETURN r.key AS k`},
		{"rebound by a later MATCH as a node",
			`MATCH (o:object)-[r]->(x) WITH o AS o WITH o AS o MATCH (r:unit) RETURN r.key AS k`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New().Parse(tc.body)
			require.NoError(t, err)
		})
	}

	_, err := New().Parse(
		`MATCH (o:object)-[r]->(x) WITH x AS y, o AS o WITH y AS z, o AS o RETURN r.key AS k`)
	require.Error(t, err, "a name nothing rebinds stays dropped across any number of WITHs")
	require.Contains(t, err.Error(), "does not carry it")
}
