package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func indexOf(t *testing.T, body string) HopIndex {
	t.Helper()
	cr, err := New().Parse(body)
	require.NoError(t, err)
	compiled, isFull := cr.(*CompiledRule)
	require.True(t, isFull)
	return compiled.AnchorHopIndex()
}

// The shipped cypher of the auth-plane lens the whole design is measured on
// (packages/rbac-domain/lenses.go). Copied verbatim so a change to the real
// lens that the derivation cannot index shows up as a test failure here rather
// than as a silent fallback in production.
const shippedCapabilityRoles = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:grantedBy]-(perm:permission)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    operationType: perm.data.operationType,
    scope: perm.data.scope,
    lanes: perm.data.lanes
  }) AS platformPermissions,
  collect(DISTINCT role.key) AS roles
`

// TestAnchorHopIndex_CapabilityRoles walks the real lens end to end: the graph
// it yields, the anchor it finds, and — the point of the whole increment — that
// a holdsRole event's anchor-side seed IS the anchor, while a grantedBy event's
// is the role, one hop out.
func TestAnchorHopIndex_CapabilityRoles(t *testing.T) {
	ix := indexOf(t, shippedCapabilityRoles)

	require.True(t, ix.Complete, "the shipped capabilityRoles must be indexable: %s", ix.Incomplete)
	require.Equal(t, "identity", ix.Labels[ix.Anchor])
	require.Equal(t, []int{0, 1, 2}, ix.Dist, "identity → role → permission")

	// holdsRole: the anchor-side endpoint is the anchor itself, so the walk
	// terminates immediately on the one identity named by the link. This is the
	// sixty-executions-to-one case.
	seeds := ix.AnchorSideSeeds("identity", "holdsRole", "role")
	require.Len(t, seeds, 1)
	require.Equal(t, ix.Anchor, seeds[0].Pos)
	require.True(t, seeds[0].SrcIsAnchorSide, "the identity endpoint is the source of a holdsRole link")

	// grantedBy: the anchor-side endpoint is the ROLE (distance 1), not the
	// permission (distance 2) — so the walk starts at the role and finds every
	// holder, which is correct and necessary.
	seeds = ix.AnchorSideSeeds("permission", "grantedBy", "role")
	require.Len(t, seeds, 1)
	require.Equal(t, 1, ix.Dist[seeds[0].Pos], "the role sits one hop from the anchor")
	require.False(t, seeds[0].SrcIsAnchorSide, "the role is the TARGET of a grantedBy link")

	// From the role, the single step toward the anchor reads INBOUND holdsRole
	// edges: the pattern writes (identity)-[:holdsRole]->(role), so standing at
	// the head means reading the arrow backwards.
	var towardAnchor []PatternStep
	for _, s := range ix.StepsFrom(seeds[0].Pos) {
		if s.ToPos == ix.Anchor {
			towardAnchor = append(towardAnchor, s)
		}
	}
	require.Len(t, towardAnchor, 1)
	require.Equal(t, "holdsRole", towardAnchor[0].Rel)
	require.Equal(t, "in", towardAnchor[0].EdgeDir)
	require.Equal(t, "identity", towardAnchor[0].ToLabel)
}

// TestAnchorHopIndex_UnrelatedRelationBindsNothing is the skip §4.7 licenses:
// on a COMPLETE index a link whose relation the pattern never traverses can
// change no anchor's output, so it yields no seed at all.
func TestAnchorHopIndex_UnrelatedRelationBindsNothing(t *testing.T) {
	ix := indexOf(t, shippedCapabilityRoles)
	require.True(t, ix.Complete)
	require.Empty(t, ix.AnchorSideSeeds("identity", "bookedBy", "booking"))
}

// The shipped capabilityEphemeral (packages/orchestration-base/lenses.go),
// abridged only by dropping the RETURN's collect() bodies — the pattern
// sources, which are all this derivation reads, are verbatim.
const shippedCapabilityEphemeral = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.expiresAt > $now
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (identity)<-[:reportsTo]-(report:identity)<-[:assignedTo]-(task2:task)
  WHERE task2.data.expiresAt > $now
OPTIONAL MATCH (task2)-[:forOperation]->(op2)
OPTIONAL MATCH (task2)-[:scopedTo]->(tgt2)
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(task3:task)
  WHERE task3.data.expiresAt > $now
OPTIONAL MATCH (task3)-[:forOperation]->(op3)
OPTIONAL MATCH (task3)-[:scopedTo]->(tgt3)
RETURN
  identity.key AS actorKey,
  task.key AS a, task2.key AS b, task3.key AS c,
  op.key AS d, op2.key AS e, op3.key AS f,
  tgt.key AS g, tgt2.key AS h, tgt3.key AS i
`

// TestAnchorHopIndex_CapabilityEphemeral proves the two properties that let the
// derivation reach a lens Increments 1–2 cannot narrow: the separate OPTIONAL
// MATCH clauses merge into ONE connected graph by variable name, and the three
// UNLABELED targets bind an event of any type.
func TestAnchorHopIndex_CapabilityEphemeral(t *testing.T) {
	ix := indexOf(t, shippedCapabilityEphemeral)
	require.True(t, ix.Complete, "capabilityEphemeral must be indexable: %s", ix.Incomplete)
	require.Equal(t, "identity", ix.Labels[ix.Anchor])

	// Every position reachable — the connectivity conjunct, which is what makes
	// the index complete at all.
	for i, d := range ix.Dist {
		require.GreaterOrEqual(t, d, 0, "position %d (%s) must reach the anchor", i, ix.Labels[i])
	}

	// A booking event binds the unlabeled targets. §4.7's worked example: the
	// derivation reaches this lens precisely because an unlabeled position
	// admits any type.
	bookingPositions := ix.PositionsBinding("booking")
	require.NotEmpty(t, bookingPositions, "an unlabeled target must admit a booking")
	for _, pos := range bookingPositions {
		require.Empty(t, ix.Labels[pos], "only the UNLABELED positions may bind a booking")
		require.Greater(t, ix.Dist[pos], 0)
	}

	// The manager-delegation branch is the one that must survive the merge: a
	// task assigned to a REPORT reaches the anchor two hops out, via reportsTo.
	seeds := ix.AnchorSideSeeds("task", "assignedTo", "identity")
	require.NotEmpty(t, seeds)
	var sawReportSide bool
	for _, s := range seeds {
		if ix.Labels[s.Pos] == "identity" && s.Pos != ix.Anchor {
			sawReportSide = true
			require.Equal(t, 1, ix.Dist[s.Pos], "the report sits one reportsTo hop from the anchor")
		}
	}
	require.True(t, sawReportSide, "the reportsTo delegation branch must be indexed")
}

// The shipped capabilityServiceAccess (packages/service-location/lenses.go).
// Its hops live in a WHERE NOT and a RETURN pattern-comprehension, which is the
// adversarial finding §11.2 records; it also carries `containedIn*0..`.
const shippedCapabilityServiceAccess = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0)-[:containedIn*0..]->(loc)<-[:availableAt]-(svc:service)
WHERE NOT (svc)-[:instanceOf]->(svcTpl:service)
  AND NOT (loc0)-[:containedIn*0..]->(exLoc)<-[:unavailableAt]-(svc)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    service: svc.key,
    resolvedVia: [loc.key],
    allowedOperations: [(svc)-[:permitsOperation]->(op) WHERE op.data.operationType <> null | {operationType: op.data.operationType}]
  }) AS serviceAccess
`

// TestAnchorHopIndex_ServiceAccessFallsBack pins the expected NON-answer. The
// variable-length containedIn hop makes the index incomplete, so this lens
// keeps the shipped BFS on every event — recorded as the design's own
// prediction, not discovered as a defect later.
func TestAnchorHopIndex_ServiceAccessFallsBack(t *testing.T) {
	ix := indexOf(t, shippedCapabilityServiceAccess)
	require.False(t, ix.Complete)
	require.Contains(t, ix.Incomplete, "variable-length")
}

// TestAnchorHopIndex_WalksEveryPatternSource is the adversarial finding §11.2
// turned into a test: hops that exist ONLY inside a WHERE NOT and a RETURN
// pattern-comprehension must be indexed, because an unavailableAt create is a
// REVOCATION. A walk over Match.Patterns alone would silently skip it.
func TestAnchorHopIndex_WalksEveryPatternSource(t *testing.T) {
	// The service-access shape with its variable-length hops removed, so the
	// index is complete and the WHERE/RETURN hops can be asserted on directly.
	ix := indexOf(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc:location)<-[:availableAt]-(svc:service)
WHERE NOT (svc)-[:instanceOf]->(svcTpl:service)
  AND NOT (loc)<-[:unavailableAt]-(svc)
RETURN
  identity.key AS actorKey,
  [(svc)-[:permitsOperation]->(op) WHERE op.data.operationType <> null | op.key] AS ops
`)
	require.True(t, ix.Complete, "%s", ix.Incomplete)

	// From WHERE NOT (…): a revocation relation, indexed.
	require.NotEmpty(t, ix.AnchorSideSeeds("service", "unavailableAt", "location"),
		"unavailableAt lives only in a WHERE NOT and must still be indexed — an unavailableAt create is a revocation")
	require.NotEmpty(t, ix.AnchorSideSeeds("service", "instanceOf", "service"))
	// From the RETURN pattern-comprehension.
	require.NotEmpty(t, ix.AnchorSideSeeds("service", "permitsOperation", "operation"))
}

// TestAnchorHopIndex_Refusals covers the completeness predicate conjunct by
// conjunct. Each of these must decline rather than answer.
func TestAnchorHopIndex_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			// The unsound-if-omitted conjunct. `other` is bound by a bucket
			// scan, so an event on it affects EVERY anchor — a derived-empty
			// answer would license exactly the wrong skip.
			name: "a position disconnected from the anchor is a cartesian seed",
			body: `MATCH (i:identity {key: $actorKey}) MATCH (other:role) RETURN i.key AS k, other.key AS r`,
			want: "not reached from the anchor",
		},
		{
			name: "an untyped relationship cannot be indexed by relation name",
			body: `MATCH (i:identity {key: $actorKey})-[]->(x:role) RETURN i.key AS k, x.key AS r`,
			want: "untyped relationship",
		},
		{
			name: "a variable-length hop cannot be stepped hop-by-hop",
			body: `MATCH (i:identity {key: $actorKey})-[:containedIn*0..]->(x:location) RETURN i.key AS k, x.key AS r`,
			want: "variable-length relationship",
		},
		{
			name: "a WITH re-seeds by bucket scan, which no adjacency walk can see",
			body: `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH r AS r MATCH (r)<-[:grantedBy]-(p:permission) RETURN r.key AS k, p.key AS pk`,
			want: "WITH",
		},
		{
			// The adversarial case: a scan-seeded position RESCUED by a hop
			// that does not bind it. Reachability in the hop graph says
			// "connected"; the executor still scans every unit and every
			// anchor's row still depends on all of them, so the only sound
			// answer is to refuse.
			name: "an optional hop does not ground a bucket-scanned position",
			body: `MATCH (i:identity {key: $actorKey}) MATCH (u:unit) OPTIONAL MATCH (i)-[:owns]->(u) RETURN i.key AS k, u.key AS uk`,
			want: "not reached from the anchor",
		},
		{
			name: "a negated hop does not ground one either",
			body: `MATCH (i:identity {key: $actorKey}) MATCH (u:unit) WHERE NOT (u)-[:restricts]->(i) RETURN i.key AS k, u.key AS uk`,
			want: "not reached from the anchor",
		},
		{
			// $actorKey under any property other than `key` makes the anchor a
			// label-prefix SCAN filtered by that property, so the position binds
			// MANY vertices — and both the "never expand from the anchor" rule
			// and the vtx.<type>.<id> key the walk mints assume exactly one.
			name: "a non-key property does not pin the anchor",
			body: `MATCH (i:identity {managedBy: $actorKey})-[:holdsRole]->(r:role) RETURN i.key AS k, r.key AS rk`,
			want: "no pattern position binds $actorKey",
		},
		{
			// `$actorKey` must be the WHOLE expression: this one pins a
			// different vertex entirely.
			name: "$actorKey merely embedded in the key expression does not pin it",
			body: `MATCH (i:identity {key: $actorKey + '-shadow'})-[:holdsRole]->(r:role) RETURN i.key AS k, r.key AS rk`,
			want: "no pattern position binds $actorKey",
		},
		{
			name: "an unanchored query has nowhere to walk to",
			body: `MATCH (u:unit)-[:managedBy]->(i:identity) RETURN u.key AS k, i.key AS ik`,
			want: "no pattern position binds $actorKey",
		},
		{
			// The first case in this table, spelled with the clauses SWAPPED.
			// Grounding is judged as each pattern is walked, so a pattern
			// reached before the anchor is identified was neither grounded nor
			// refused, and nothing revisited it. Clause order is not something
			// a lens author owes us.
			name: "a cartesian seed BEFORE the anchor clause is refused too",
			body: `MATCH (other:role) MATCH (i:identity {key: $actorKey}) RETURN i.key AS k, other.key AS r`,
			want: "not reached from the anchor",
		},
		{
			// A variable introduced inside a WHERE pattern is never bound in
			// the outer row — existsAsPredicate calls matchPath and discards
			// the bindings. So the later MATCH headed by `b` seeds from the
			// badge bucket, every anchor's row depends on all of it, and the
			// walk (whose only route back is the non-binding `blocked` hop)
			// derives the empty set for almost every event: a skip, not a
			// wider answer.
			name: "a WHERE pattern does not bind, so it cannot ground a later head",
			body: `MATCH (i:identity {key: $actorKey}) WHERE NOT (i)-[:blocked]->(b:badge) MATCH (b)-[:issuedBy]->(o:org) RETURN i.key AS k, o.key AS ok`,
			want: "not reached from the anchor",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := indexOf(t, tc.body)
			require.False(t, ix.Complete, "must decline, not answer")
			require.Contains(t, ix.Incomplete, tc.want)
		})
	}
}

// TestAnchorHopIndex_DirectionMapping pins the arrow-to-adjacency translation
// in both readings, since getting it backwards would walk away from the anchor
// and derive an empty set — silent under-approximation.
func TestAnchorHopIndex_DirectionMapping(t *testing.T) {
	// Outbound as written: (anchor)-[:r]->(x). Standing at x, step to the
	// anchor by reading x's INBOUND r edges.
	out := indexOf(t, `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(x:role) RETURN i.key AS k, x.key AS r`)
	require.True(t, out.Complete, "%s", out.Incomplete)
	far := 1 - out.Anchor
	require.Equal(t, []PatternStep{{ToPos: out.Anchor, Rel: "holdsRole", EdgeDir: "in", ToLabel: "identity"}}, out.StepsFrom(far))
	require.Equal(t, []PatternStep{{ToPos: far, Rel: "holdsRole", EdgeDir: "out", ToLabel: "role"}}, out.StepsFrom(out.Anchor))

	// Inbound as written: (anchor)<-[:r]-(x). Standing at x, the anchor is
	// reached by x's OUTBOUND r edges.
	in := indexOf(t, `MATCH (i:identity {key: $actorKey})<-[:assignedTo]-(x:task) RETURN i.key AS k, x.key AS r`)
	require.True(t, in.Complete, "%s", in.Incomplete)
	far = 1 - in.Anchor
	require.Equal(t, []PatternStep{{ToPos: in.Anchor, Rel: "assignedTo", EdgeDir: "out", ToLabel: "identity"}}, in.StepsFrom(far))
}

// TestAnchorHopIndex_LabelIsNeverUpgraded is the regression for the unsoundest
// shape the adversarial pass found. A later OPTIONAL MATCH re-references an
// already-bound variable with a label; when that label fails, the executor
// restores the row with the ORIGINAL binding intact, so the variable really can
// hold another type. Adopting the later label would narrow PositionsBinding
// below what the executor binds, and the event on the other type would derive
// no anchors at all.
func TestAnchorHopIndex_LabelIsNeverUpgraded(t *testing.T) {
	ix := indexOf(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (tgt:booking)-[:bookedBy]->(who:identity)
RETURN identity.key AS actorKey, tgt.key AS t, who.key AS w
`)
	require.True(t, ix.Complete, "%s", ix.Incomplete)

	var tgtPos = -1
	for _, pos := range ix.PositionsBinding("room") {
		if ix.Dist[pos] == 2 {
			tgtPos = pos
		}
	}
	require.NotEqual(t, -1, tgtPos,
		"tgt binds by scopedTo with no label, so a room event must still seed it — the later (tgt:booking) is a filter, not a binding")
	require.Empty(t, ix.Labels[tgtPos], "the first occurrence is unlabeled and that is what binds")
}

// TestAnchorHopIndex_DistIgnoresNonBindingHops pins the seed-choice fix. A
// WHERE NOT shortcut from the anchor must not make a far position look near:
// if it did, a link tombstone deeper in the binding chain would be seeded at
// the endpoint that can only reach the anchor by crossing the edge the
// tombstone just removed.
func TestAnchorHopIndex_DistIgnoresNonBindingHops(t *testing.T) {
	ix := indexOf(t, `
MATCH (identity:identity {key: $actorKey})-[:owns]->(t:team)-[:hasProject]->(p:project)-[:hasTask]->(k:task)
WHERE NOT (identity)-[:muted]->(k)
RETURN identity.key AS actorKey, k.key AS kk
`)
	require.True(t, ix.Complete, "%s", ix.Incomplete)
	require.Equal(t, []int{0, 1, 2, 3}, ix.Dist,
		"the muted hop is a filter, so it must not shorten k's distance from 3 to 1")

	// The anchor-side endpoint of a hasTask link is therefore the PROJECT, which
	// reaches the anchor without re-crossing the removed edge.
	seeds := ix.AnchorSideSeeds("project", "hasTask", "task")
	require.Len(t, seeds, 1)
	require.Equal(t, 2, ix.Dist[seeds[0].Pos])
	require.True(t, seeds[0].SrcIsAnchorSide)
}
