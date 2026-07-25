package full

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// collectDistinctCompositeCypher is the shape every generated read-grant
// producer takes (pkgmgr.generateProducerSpec): several INDEPENDENT OPTIONAL
// MATCH branches off one actor, each collected into its own
// `collect(DISTINCT ...)`, the branches concatenated with `+` into a single
// RETURN item.
//
// The branches are independent, so the binding set reaching the projection is
// their CROSS PRODUCT: with r roles and s services every branch sees r*s rows.
// DISTINCT is what collapses that back to the r and s each branch actually
// reached — without it each branch's list is inflated by the other branch's
// cardinality, and the inflation is multiplicative in the number of branches.
const collectDistinctCompositeCypher = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)-[:availableAt]->(svc:service)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorType: 'role', anchorId: role.key}) +
  collect(DISTINCT {anchorType: 'service', anchorId: svc.key}) AS anchors
`

// TestCollectDistinct_InAComposedItem_DedupesEachBranch pins DISTINCT through a
// RETURN item that is a `+` of aggregators rather than a bare aggregator call.
//
// 2 roles x 3 services = 6 cross-product bindings. Each branch must yield only
// what it reached (2 and 3), for 5 entries total. Collecting per-row instead
// yields 6 + 6 = 12 — every entry repeated by the sibling branch's cardinality.
func TestCollectDistinct_InAComposedItem_DedupesEachBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	for _, r := range []string{"leasingTeam", "frontDesk"} {
		putVertex(t, reg, coreKV, r, "role", nil)
		putEdge(t, reg, adjKV, "holdsRole", "alice", r)
	}
	for _, s := range []string{"svcA", "svcB", "svcC"} {
		putVertex(t, reg, coreKV, s, "service", nil)
		putEdge(t, reg, adjKV, "availableAt", "alice", s)
	}

	results := parseExec(t, collectDistinctCompositeCypher,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Len(t, results, 1, "one actor aggregates to exactly one row")

	anchors, ok := results[0].Values["anchors"].([]any)
	require.True(t, ok, "anchors must be a list, got %T", results[0].Values["anchors"])

	byType := map[string][]string{}
	seen := map[string]struct{}{}
	for _, e := range anchors {
		m, isMap := e.(map[string]any)
		require.True(t, isMap, "each anchor entry is a map, got %T", e)
		sig := normalizeForKey(m)
		_, dup := seen[sig]
		require.False(t, dup, "duplicate anchor entry %s — a branch's DISTINCT did not bind", sig)
		seen[sig] = struct{}{}
		at, _ := m["anchorType"].(string)
		id, _ := m["anchorId"].(string)
		byType[at] = append(byType[at], id)
	}

	require.Len(t, anchors, 5, "2 roles + 3 services, each once — not the 2*3 cross product per branch")
	require.ElementsMatch(t, []string{vtxKey(reg, "leasingTeam"), vtxKey(reg, "frontDesk")}, byType["role"])
	require.ElementsMatch(t, []string{vtxKey(reg, "svcA"), vtxKey(reg, "svcB"), vtxKey(reg, "svcC")}, byType["service"])
}

// TestCollectDistinct_ComposedItemGrowsWithReach_NotWithSiblingBranches is the
// scale property the auth plane depends on: a read-grant document's size is
// bounded by how many anchors the actor can actually reach, so adding a branch
// adds that branch's anchors — it does not multiply the existing ones.
//
// Without per-branch DISTINCT this query yields 3*4*5 = 60 bindings and 180
// entries, and the growth is multiplicative in every branch a package adds:
// that is what drove one live `cap-read.edgeManifest.*` document to 12,558
// entries carrying 34 distinct grants, past NATS's max payload, after which the
// row could never be written again.
func TestCollectDistinct_ComposedItemGrowsWithReach_NotWithSiblingBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	for _, r := range []string{"roleA", "roleB", "roleC"} {
		putVertex(t, reg, coreKV, r, "role", nil)
		putEdge(t, reg, adjKV, "holdsRole", "alice", r)
	}
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		putVertex(t, reg, coreKV, s, "service", nil)
		putEdge(t, reg, adjKV, "availableAt", "alice", s)
	}
	for _, b := range []string{"b1", "b2", "b3", "b4", "b5"} {
		putVertex(t, reg, coreKV, b, "booking", nil)
		putEdge(t, reg, adjKV, "holds", "alice", b)
	}

	const threeBranch = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)-[:availableAt]->(svc:service)
OPTIONAL MATCH (identity)-[:holds]->(bk:booking)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorType: 'role', anchorId: role.key}) +
  collect(DISTINCT {anchorType: 'service', anchorId: svc.key}) +
  collect(DISTINCT {anchorType: 'booking', anchorId: bk.key}) AS anchors
`
	results := parseExec(t, threeBranch,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Len(t, results, 1)
	anchors, ok := results[0].Values["anchors"].([]any)
	require.True(t, ok)
	require.Len(t, anchors, 3+4+5, "the sum of what each branch reached, never the 3*4*5 product")
}

// TestCollectDistinct_BareItemStillDedupes guards the path that already worked:
// a RETURN item that IS the aggregator call, deduped over a multi-row branch.
func TestCollectDistinct_BareItemStillDedupes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "leasingTeam", "role", nil)
	putEdge(t, reg, adjKV, "holdsRole", "alice", "leasingTeam")
	// Two services multiply the role branch's rows without adding roles.
	for _, s := range []string{"svcA", "svcB"} {
		putVertex(t, reg, coreKV, s, "service", nil)
		putEdge(t, reg, adjKV, "availableAt", "alice", s)
	}

	const bare = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)-[:availableAt]->(svc:service)
RETURN identity.key AS actorKey, collect(DISTINCT role.key) AS roles
`
	results := parseExec(t, bare,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Len(t, results, 1)
	roles, ok := results[0].Values["roles"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{vtxKey(reg, "leasingTeam")}, roles, "one role, once, despite two service rows")
}

// TestCollectDistinct_WithoutDistinctStillKeepsEveryRow: DISTINCT is opt-in.
// A plain collect() in a composed item must keep one entry per binding, so the
// dedup fix cannot quietly change a non-DISTINCT lens's output.
func TestCollectDistinct_WithoutDistinctStillKeepsEveryRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "leasingTeam", "role", nil)
	putEdge(t, reg, adjKV, "holdsRole", "alice", "leasingTeam")
	for _, s := range []string{"svcA", "svcB"} {
		putVertex(t, reg, coreKV, s, "service", nil)
		putEdge(t, reg, adjKV, "availableAt", "alice", s)
	}

	const undeduped = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)-[:availableAt]->(svc:service)
RETURN
  identity.key AS actorKey,
  collect(role.key) + collect(DISTINCT svc.key) AS mixed
`
	results := parseExec(t, undeduped,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Len(t, results, 1)
	mixed, ok := results[0].Values["mixed"].([]any)
	require.True(t, ok)
	// 2 cross-product rows keep 2 role entries (no DISTINCT); the service
	// branch dedupes to its 2 reached services.
	require.Len(t, mixed, 2+2, "a non-DISTINCT branch keeps one entry per binding")
}

// TestNormalizeForKey_IsInjective pins the property both DISTINCT dedup and
// WITH/RETURN grouping rest on: distinct values never render alike.
//
// A collision has no error path — two values silently become one group, or one
// is dropped from a DISTINCT list. Free text reaches this function (lenses
// collect `presentation.data.name` into anchor maps), so the delimiter cases
// below are authored-data-reachable, not theoretical: an authored name must not
// be able to impersonate structure and absorb a sibling entry.
func TestNormalizeForKey_IsInjective(t *testing.T) {
	for _, c := range []struct {
		name string
		a, b any
	}{
		{
			"a name forging a map separator",
			map[string]any{"key": "KA", "name": "Apt 1,key:KB,name:Apt 2"},
			map[string]any{"key": "KB", "name": "Apt 2"},
		},
		{
			"free text swapping across two fields that bracket the discriminating key",
			map[string]any{"containerName": "Home,key:vtx.l.YYY,name:Bar", "key": "vtx.l.XXX", "name": "Foo"},
			map[string]any{"containerName": "Home", "key": "vtx.l.YYY", "name": "Bar,key:vtx.l.XXX,name:Foo"},
		},
		{"nil versus its own rendering", nil, "<nil>"},
		{"int versus its string", int64(1), "1"},
		{"float versus its string", 1.0, "1"},
		{"bool versus its string", true, "true"},
		{"a list element forging the element separator", []any{"a", "b"}, []any{"a,b"}},
		{"an empty list versus an empty string", []any{}, ""},
		{"an empty map versus an empty string", map[string]any{}, ""},
		{"a key/value boundary shift", map[string]any{"a": "b:c"}, map[string]any{"a:b": "c"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.NotEqual(t, normalizeForKey(c.a), normalizeForKey(c.b),
				"%#v and %#v render alike — one would be silently dropped by DISTINCT "+
					"or merged into the other's group", c.a, c.b)
		})
	}
}

// TestNormalizeForKey_IsStable: equal values must render identically, or
// DISTINCT would keep duplicates and one group would split in two. Map
// iteration order is not something a caller controls, so it must not leak in.
func TestNormalizeForKey_IsStable(t *testing.T) {
	a := map[string]any{"anchorType": "service", "anchorId": "K1bqP7UH8wYRfRTUdY5d", "via": []any{"availableAt"}}
	b := map[string]any{"via": []any{"availableAt"}, "anchorId": "K1bqP7UH8wYRfRTUdY5d", "anchorType": "service"}
	require.Equal(t, normalizeForKey(a), normalizeForKey(b), "same content, different insertion order")
	require.Equal(t, normalizeForKey(nil), normalizeForKey(nil))
}

// TestCollectDistinct_KeepsCrossBranchDuplicates: dedup is per aggregator call,
// so two branches that both reach the same anchor each keep their entry. The
// entries differ in `via` — the justifying path — and Contract #6 §6.14 makes
// the effective set a UNION over slices while `via` is audit-only, so keeping
// both is the auditable, conservative choice. Deduping on `anchorId` across
// branches would silently discard one justification.
func TestCollectDistinct_KeepsCrossBranchDuplicates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "svcA", "service", nil)
	// One service reached by two different relations — two branches, one anchor.
	putEdge(t, reg, adjKV, "availableAt", "alice", "svcA")
	putEdge(t, reg, adjKV, "providedTo", "alice", "svcA")

	const twoPaths = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:availableAt]->(a:service)
OPTIONAL MATCH (identity)-[:providedTo]->(p:service)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorId: a.key, via: ['availableAt']}) +
  collect(DISTINCT {anchorId: p.key, via: ['providedTo']}) AS anchors
`
	results := parseExec(t, twoPaths,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Len(t, results, 1)
	anchors, ok := results[0].Values["anchors"].([]any)
	require.True(t, ok)
	require.Len(t, anchors, 2, "same anchor, two justifying paths, both kept")
}
