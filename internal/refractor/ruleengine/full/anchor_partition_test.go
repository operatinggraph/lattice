package full

// The partition predicate's shape table and its per-event half.
//
// PartitionsByAnchor authorises a scoped Delete on live rows — the pipeline
// lists one anchor's partition and tombstones every key in it the fresh row set
// no longer carries — so both directions are load-bearing. A shape admitted
// that should refuse hands that listing a scope the rule does not own; a shape
// refused that should admit costs a whole-corpus rescan and a whole-target
// listing on every event.
//
// The corpus census (internal/refractor/plain_partition_census_test.go) pins
// WHICH installed lenses land where. This file pins WHY, on the smallest query
// carrying each shape, plus the four real cyphers the design argues from.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	identityhygiene "github.com/operatinggraph/lattice/packages/identity-hygiene"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
)

// TestPartitionsByAnchor_Shapes is the shape table: every case is the smallest
// query carrying the shape, judged by the real predicate.
func TestPartitionsByAnchor_Shapes(t *testing.T) {
	cases := []struct {
		name        string
		spec        string
		keyCols     []string
		admit       bool
		identifying []string
		why         string
	}{
		{
			name:        "neighbour-bound key column beside an identifying one",
			spec:        "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN nanoIdFromKey(app.key) AS app_id, nanoIdFromKey(u.key) AS unit_id",
			keyCols:     []string{"app_id", "unit_id"},
			admit:       true,
			identifying: []string{"app_id"},
			why:         "the row is computed from the anchor's own bindings and app_id says which anchor it belongs to",
		},
		{
			name:    "every key column binds a neighbour",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN nanoIdFromKey(u.key) AS unit_id, u.address.data.city AS city",
			keyCols: []string{"unit_id", "city"},
			admit:   false,
			why:     "no column identifies the anchor, so a listing scoped by this key could not name one leaseapp's rows",
		},
		{
			name:    "the identifying column is a literal",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN 'fixed' AS app_id, nanoIdFromKey(u.key) AS unit_id",
			keyCols: []string{"app_id", "unit_id"},
			admit:   false,
			why:     "a literal merges every anchor into one group, so a seeded evaluation would compute a truncated row",
		},
		{
			name:    "a non-key property stands in for the identifier",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN app.status AS app_id, nanoIdFromKey(u.key) AS unit_id",
			keyCols: []string{"app_id", "unit_id"},
			admit:   false,
			why:     "a status is distinct in today's data and identifies nothing — exprIdentifiesVariable admits `.key` forms only",
		},
		{
			name:    "an aggregate in a key column",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN nanoIdFromKey(app.key) AS app_id, count(u) AS n",
			keyCols: []string{"app_id", "n"},
			admit:   false,
			why:     "an aggregate key's value depends on the grouped row set, which is exactly what the partition argument may not assume",
		},
		{
			name: "an aggregate hidden behind a WITH alias",
			spec: "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH app, count(u) AS n " +
				"RETURN nanoIdFromKey(app.key) AS app_id, n AS unit_count",
			keyCols: []string{"app_id", "unit_count"},
			admit:   false,
			why:     "substituteAliases inlines the aggregating item's own call, so the alias cannot smuggle it past the walk",
		},
		{
			name:        "a function over a relationship variable",
			spec:        "MATCH (o:object)-[r]->(owner:identity) RETURN nanoIdFromKey(o.key) AS oid_id, type(r) AS link_name",
			keyCols:     []string{"oid_id", "link_name"},
			admit:       true,
			identifying: []string{"oid_id"},
			why:         "a relationship variable is a pattern variable, and type(r) is a non-aggregating expression over the row's own binding",
		},
		{
			name:    "a pattern comprehension in a key column",
			spec:    "MATCH (u:unit) RETURN nanoIdFromKey(u.key) AS unit_id, [(u)-[:containedIn]->(b:building) | b.key] AS buildings",
			keyCols: []string{"unit_id", "buildings"},
			admit:   false,
			why:     "a pattern form is traversal-dependent, and the walk refuses it under both readings",
		},
		{
			name:        "two identifying columns",
			spec:        "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN app.key AS entity_key, nanoIdFromKey(app.key) AS app_id, nanoIdFromKey(u.key) AS unit_id",
			keyCols:     []string{"entity_key", "app_id", "unit_id"},
			admit:       true,
			identifying: []string{"entity_key", "app_id"},
			why:         "both name the same anchor, and the predicate returns every column that does rather than the first",
		},
		{
			name:    "a KEYED anchor pattern",
			spec:    "MATCH (a:identity {key: 'vtx.identity.Hj4kPmRtw9nbCxz5vQ2y'})-[:duplicateOf]->(b:identity) RETURN nanoIdFromKey(a.key) AS aid, nanoIdFromKey(b.key) AS bid",
			keyCols: []string{"aid", "bid"},
			admit:   false,
			why: "the pattern already point-reads the key it names and seedAnchorBinds refuses to displace it, so a seeded " +
				"evaluation stays pinned to the literal while the predicate would name the EVENT's partition — a wipe of a scope the evaluation never computed",
		},
		{
			name:    "an unmodelled node reached from a key column",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH CASE WHEN app.status = 'live' THEN app.key ELSE app.key END AS c, u RETURN c AS app_id, nanoIdFromKey(u.key) AS unit_id",
			keyCols: []string{"app_id", "unit_id"},
			admit:   false,
			why:     "the resolver does not model a CASE, so which variable produced the value is unknown and the shape refuses",
		},
	}

	eng := New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := parseForShape(t, eng, tc.spec, tc.keyCols)
			identifying, ok := cr.PartitionsByAnchor()
			require.Equalf(t, tc.admit, ok, "%s", tc.why)
			if tc.admit {
				require.Equalf(t, tc.identifying, identifying, "%s", tc.why)
			} else {
				require.Nil(t, identifying, "a refused rule names no identifying column")
			}
		})
	}
}

// TestPartitionsByAnchor_KeyedAnchorRefusalIsPartitionOnly pins that the
// keyed-anchor refusal lives in the PARTITION reading and nowhere else.
//
// The two predicates share a resolver body, so a refusal written into that body
// moves ProjectsOneRowPerAnchor's verdicts too — and closure's consumers do not
// seed (AnchorProjectionKey resolves a key for a retraction its caller has
// already scoped to one anchor), so a keyed anchor costs them nothing. The
// negative vector alone could not tell a correctly-placed refusal from one that
// quietly narrowed the other predicate; this is the half that can.
func TestPartitionsByAnchor_KeyedAnchorRefusalIsPartitionOnly(t *testing.T) {
	eng := New()
	const spec = "MATCH (a:identity {key: 'vtx.identity.Hj4kPmRtw9nbCxz5vQ2y'})-[:duplicateOf]->(b:identity) " +
		"RETURN nanoIdFromKey(a.key) AS aid, a.state.data.value AS state"
	cr := parseForShape(t, eng, spec, []string{"aid", "state"})

	require.True(t, cr.ProjectsOneRowPerAnchor(),
		"a keyed anchor is still CLOSED — its key columns resolve from the anchor alone, which is the only thing closure claims")
	_, ok := cr.PartitionsByAnchor()
	require.False(t, ok, "and the partition reading refuses it, because that reading is the one whose consumer seeds")
}

// TestPartitionsByAnchor_IsASupersetOfClosure holds the construction the design
// rests on, on the shapes rather than on the corpus: every rule the closure
// predicate admits, the partition predicate admits too, with the same
// identifying columns. The corpus half is the census (54 = 54).
func TestPartitionsByAnchor_IsASupersetOfClosure(t *testing.T) {
	closed := []struct {
		spec    string
		keyCols []string
	}{
		{"MATCH (app:leaseapp) RETURN nanoIdFromKey(app.key) AS app_id", []string{"app_id"}},
		{"MATCH (app:leaseapp) WITH app.key AS entityKey RETURN nanoIdFromKey(entityKey) AS app_id", []string{"app_id"}},
		{"MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN app.key AS entity_key, app.status AS status", []string{"entity_key", "status"}},
	}
	eng := New()
	for _, c := range closed {
		cr := parseForShape(t, eng, c.spec, c.keyCols)
		require.Truef(t, cr.ProjectsOneRowPerAnchor(), "positive vector: %s must close", c.spec)
		_, ok := cr.PartitionsByAnchor()
		require.Truef(t, ok, "the partition predicate is the closure predicate minus one conjunct, so it can never refuse what closure admits: %s", c.spec)
	}
}

// TestPartitionsByAnchor_RefusesWithoutIdentifyingColumn is §5.5's mutation pin,
// run against the REAL landlord cypher rather than a reconstruction of it: the
// shipped spec is admitted with identifying = [app_id], and each mutation that
// removes the anchor's identity from the key is refused.
//
// The mutations are the attacks the adversarial pass named. Each is a wrong
// Delete on an RLS-protected table if it is admitted: a literal or a
// neighbour-derived identifier scopes a listing to a partition the rule does not
// own, and an aggregate key column makes the seeded evaluation's row set a
// fraction of the whole one's.
func TestPartitionsByAnchor_RefusesWithoutIdentifyingColumn(t *testing.T) {
	eng := New()
	const (
		realKey = "nanoIdFromKey(entityKey)       AS app_id"
		keyCols = 2
	)
	spec := landlordSpec(t)
	require.Containsf(t, spec, realKey, "the landlord cypher's app_id column moved — re-read it before mutating it")

	t.Run("the shipped cypher is admitted", func(t *testing.T) {
		cr := parseForShape(t, eng, spec, []string{"app_id", "landlord_id"})
		identifying, ok := cr.PartitionsByAnchor()
		require.True(t, ok, "landlordLeaseApplicationsRead's rows partition by its leaseapp anchor")
		require.Equal(t, []string{"app_id"}, identifying,
			"app_id is the column that says which leaseapp a row belongs to; landlord_id binds the neighbour")
		require.False(t, cr.ProjectsOneRowPerAnchor(),
			"and it is partition-ONLY: landlord_id is not derivable from the anchor, which is why the closure predicate refuses it")
	})

	mutations := []struct {
		name    string
		replace string
		why     string
	}{
		{
			name:    "app_id replaced by a literal",
			replace: "'fixed'                        AS app_id",
			why:     "every leaseapp's rows would merge into one group, and a seeded evaluation would produce a truncated row",
		},
		{
			name:    "app_id derived from the unit instead of the anchor",
			replace: "nanoIdFromKey(unitKey)         AS app_id",
			why:     "a neighbour's identity names no anchor, so the partition would be the unit's rows and not the leaseapp's",
		},
		{
			name:    "app_id replaced by an aggregate",
			replace: "count(landlordKey)             AS app_id",
			why:     "an aggregate key column's value depends on the grouped row set the partition argument may not assume",
		},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			mutated := replaceOnce(t, spec, realKey, m.replace)
			cr := parseForShape(t, eng, mutated, []string{"app_id", "landlord_id"})
			identifying, ok := cr.PartitionsByAnchor()
			require.Falsef(t, ok, "%s", m.why)
			require.Nil(t, identifying)
		})
	}

	t.Run("WITH * never reaches the predicate", func(t *testing.T) {
		_, err := eng.Parse("MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH * RETURN nanoIdFromKey(app.key) AS app_id, nanoIdFromKey(u.key) AS unit_id")
		require.Error(t, err, "a WITH * spec must not compile at all, so no partition verdict is ever reached for one")
		require.Contains(t, err.Error(), "a WITH projection may not use `*`")
	})
}

// TestPartitionsByAnchor_CorpusCyphers runs the four shipped cyphers the design
// argues from through the real predicate, and the two it must keep refusing.
// The census pins the whole corpus; these are the named members the design's
// soundness argument is written about.
func TestPartitionsByAnchor_CorpusCyphers(t *testing.T) {
	eng := New()
	cases := []struct {
		name        string
		spec        string
		keyCols     []string
		admit       bool
		identifying []string
		why         string
	}{
		{
			name:        "landlordUnitsRead",
			spec:        lensSpecNamed(t, loftspacedomain.Package.Lenses, "landlordUnitsRead"),
			keyCols:     []string{"unit_id", "landlord_id"},
			admit:       true,
			identifying: []string{"unit_id"},
			why:         "unit_id identifies the unit anchor; landlord_id binds the managing identity the `manages` hop reached",
		},
		{
			name:        "objectIdentityAttachmentsRead",
			spec:        lensSpecNamed(t, loftspacedomain.Package.Lenses, "objectIdentityAttachmentsRead"),
			keyCols:     []string{"oid_id", "owner_id", "link_name"},
			admit:       true,
			identifying: []string{"oid_id"},
			why:         "type(r) over the attachment relationship is a non-aggregating expression over a pattern variable",
		},
		{
			name:        "duplicateCandidates",
			spec:        lensSpecNamed(t, identityhygiene.Package.Lenses, "duplicateCandidates"),
			keyCols:     []string{"primaryId", "secondaryId"},
			admit:       true,
			identifying: []string{"secondaryId"},
			why:         "the anchor is `b`, so secondaryId is what identifies it and primaryId binds the duplicate's target",
		},
		{
			name:    "wellnessMemberAccounts",
			spec:    lensSpecNamed(t, wellnessledger.Package.Lenses, "wellnessMemberAccounts"),
			keyCols: []string{"key"},
			admit:   false,
			why:     "its rows partition by the IDENTITY they key on, not by the booking it anchors on — no key column identifies the anchor",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := parseForShape(t, eng, tc.spec, tc.keyCols)
			identifying, ok := cr.PartitionsByAnchor()
			require.Equalf(t, tc.admit, ok, "%s", tc.why)
			if tc.admit {
				require.Equalf(t, tc.identifying, identifying, "%s", tc.why)
			}
		})
	}
}

// TestPartitionPredicate is the per-event half: the fixed values one anchor's
// partition is listed by, and every refusal that keeps a wrong value out of a
// listing filter.
func TestPartitionPredicate(t *testing.T) {
	eng := New()
	const anchorID = "3hWUq2mTbKxRvNpYaZcd"
	anchorKey := "vtx.leaseapp." + anchorID

	t.Run("the shipped landlord cypher names its own partition", func(t *testing.T) {
		cr := parseForShape(t, eng, landlordSpec(t), []string{"app_id", "landlord_id"})
		fixed, ok := eng.PartitionPredicate(cr, anchorKey, "leaseapp")
		require.True(t, ok)
		require.Equal(t, map[string]any{"app_id": anchorID}, fixed,
			"only the identifying column is fixed — landlord_id is what the listing enumerates within the partition")
	})

	t.Run("a root-tombstoned anchor still names its partition", func(t *testing.T) {
		// No body is passed at all, which is the tombstone case: an identifying
		// column is a `.key` form, so nothing stored is on the path.
		cr := parseForShape(t, eng, landlordSpec(t), []string{"app_id", "landlord_id"})
		fixed, ok := eng.PartitionPredicate(cr, anchorKey, "leaseapp")
		require.True(t, ok, "the predicate reads the key and never the body, which is what makes §3.4's one path work")
		require.Equal(t, map[string]any{"app_id": anchorID}, fixed)
	})

	t.Run("a non-anchor event type refuses", func(t *testing.T) {
		cr := parseForShape(t, eng, landlordSpec(t), []string{"app_id", "landlord_id"})
		_, ok := eng.PartitionPredicate(cr, "vtx.identity."+anchorID, "identity")
		require.False(t, ok, "a neighbour vertex names no partition of this lens, and answering one would scope the diff to a stranger")
	})

	t.Run("an empty key refuses", func(t *testing.T) {
		cr := parseForShape(t, eng, landlordSpec(t), []string{"app_id", "landlord_id"})
		_, ok := eng.PartitionPredicate(cr, "", "leaseapp")
		require.False(t, ok, "nanoIdFromKey of an empty key is not a value any partition may be scoped by")
	})

	t.Run("a rule that does not partition refuses", func(t *testing.T) {
		cr := parseForShape(t, eng,
			"MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN nanoIdFromKey(u.key) AS unit_id, u.address.data.city AS city",
			[]string{"unit_id", "city"})
		_, ok := eng.PartitionPredicate(cr, anchorKey, "leaseapp")
		require.False(t, ok, "the per-event half never answers for a shape PartitionsByAnchor refuses")
	})

	t.Run("a value outside the key alphabet refuses", func(t *testing.T) {
		// The whole key IS the identifying column here, so a key carrying a
		// subject wildcard reaches the value check rather than nanoIdFromKey's
		// prefix strip.
		cr := parseForShape(t, eng, "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN app.key AS entity_key, nanoIdFromKey(u.key) AS unit_id",
			[]string{"entity_key", "unit_id"})

		fixed, ok := eng.PartitionPredicate(cr, anchorKey, "leaseapp")
		require.True(t, ok, "positive vector: a well-formed vtx key is admitted whole")
		require.Equal(t, map[string]any{"entity_key": anchorKey}, fixed)

		for _, bad := range []string{
			"vtx.leaseapp.*",
			"vtx.leaseapp.>",
			"vtx.leaseapp." + anchorID + ".extra",
			"not-a-key",
		} {
			_, ok := eng.PartitionPredicate(cr, bad, "leaseapp")
			require.Falsef(t, ok, "%q must never reach a listing filter or a bound parameter", bad)
		}
	})

	t.Run("a node-valued key column refuses", func(t *testing.T) {
		cr := parseForShape(t, eng, "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) RETURN app.key AS entity_key, u AS unit",
			[]string{"entity_key", "unit"})
		_, structural := cr.PartitionsByAnchor()
		require.True(t, structural, "a bare node variable is still an expression over a pattern variable")
		fixed, ok := eng.PartitionPredicate(cr, anchorKey, "leaseapp")
		require.True(t, ok, "the node column is not identifying, so it is never fixed and never evaluated")
		require.Equal(t, map[string]any{"entity_key": anchorKey}, fixed)
	})
}

// landlordSpec returns landlordLeaseApplicationsRead's shipped cypher, straight
// from its own package definition so the mutations above are mutations of the
// live lens rather than of a copy that stopped tracking it.
func landlordSpec(t *testing.T) string {
	t.Helper()
	return lensSpecNamed(t, leasesigning.Package.Lenses, "landlordLeaseApplicationsRead")
}

// replaceOnce substitutes old for new exactly once, failing when the anchor text
// is absent or ambiguous — a mutation that silently matched nothing would leave
// the test asserting the unmutated cypher's verdict.
func replaceOnce(t *testing.T, s, old, replacement string) string {
	t.Helper()
	require.Equalf(t, 1, strings.Count(s, old), "the mutation anchor %q must appear exactly once", old)
	return strings.Replace(s, old, replacement, 1)
}
