package full

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

func parseFull(t *testing.T, body string) *CompiledRule {
	t.Helper()
	cr, err := New().Parse(body)
	require.NoError(t, err)
	compiled, isFull := cr.(*CompiledRule)
	require.True(t, isFull)
	return compiled
}

func indexOf(t *testing.T, body string) HopIndex {
	t.Helper()
	return parseFull(t, body).AnchorHopIndex()
}

func rootIndexOf(t *testing.T, body string) HopIndex {
	t.Helper()
	return parseFull(t, body).ScanRootHopIndex()
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

// The shipped capabilityServiceAccess, copied VERBATIM from
// packages/service-location/lenses.go's capabilityServiceAccessSpec — the
// `:location*` sigils included, because they are what make this lens the only
// one in the corpus whose index carries a taxonomy expansion, and a fixture that
// drops them cannot exercise the conjunct that governs it.
//
// Its hops live in a WHERE NOT and a RETURN pattern-comprehension, which is the
// adversarial finding §11.2 records; it also carries `containedIn*0..`.
const shippedCapabilityServiceAccess = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location*)-[:containedIn*0..]->(loc:location*)<-[:availableAt]-(svc:service)
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

// TestAnchorHopIndex_ServiceAccessIndexesItsRangedHops pins the shipped
// auth-plane lens whose containment walks are variable-length: both of its
// `containedIn*0..` hops are recorded as ranged hops carrying the executor's own
// clamp, and the index is authoritative.
//
// It is the whole-lens vector behind the mechanism tests: the positive arm's
// hop and the exclusion arm's hop are separate `containedIn` occurrences, and an
// index that recorded only one of them would look complete while the walk lost
// the exclusion — an unavailableAt that stops revoking.
func TestAnchorHopIndex_ServiceAccessIndexesItsRangedHops(t *testing.T) {
	ix := indexOf(t, shippedCapabilityServiceAccess)
	require.True(t, ix.Complete, "%s", ix.Incomplete)

	var ranged []PatternHop
	for _, h := range ix.Hops {
		if h.Rel == "containedIn" {
			ranged = append(ranged, h)
		}
	}
	require.Len(t, ranged, 2, "the positive chain and the exclusion chain each carry one containedIn hop")
	for _, h := range ranged {
		require.Equal(t, 0, h.Min, "`*0..` admits the standing node itself")
		require.Equal(t, maxVarLengthHops, h.Max, "an open range takes the executor's own clamp")
	}
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

// TestAnchorHopIndex_WithScope is the WITH conjunct, stated as the population
// it admits and the population it refuses.
//
// The hazard a WITH creates is narrow: projectItems rebuilds each row from the
// projection aliases alone, so a name the WITH does not carry is unbound
// afterwards and a later clause using it gets a FRESH binding — seeded from a
// bucket by matchPath, which no adjacency walk can see. A WITH whose dropped
// names nothing downstream mentions creates no such re-seed, and the anchor is
// the `$actorKey` PARAMETER rather than a row column, so even dropping the
// anchor's own variable leaves the graph walkable.
//
// The population it admits is therefore "every lens whose WITH is a pure stage
// boundary" — which is what the corpus's WITHs overwhelmingly are, since they
// exist to collapse a fan-out back to one row per anchor before the next arm
// fans out. What every accepted case has to earn is the invariant
// hopIndexBuilder.position assumes and cannot check for itself: that two
// sightings of one variable name are the same executor binding. Each accepted
// case below is a case where that holds by construction; each refused one is a
// case where it does not, or where the projection list is a shape whose answer
// the index declines to guess.
func TestAnchorHopIndex_WithScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		reject string // "" == the index must be complete
	}{
		{
			// Every name a later clause uses survives the boundary, so the
			// positions the walk needs are the ones the executor really binds.
			name: "a WITH carrying everything downstream uses is indexable",
			body: `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH i, r MATCH (r)<-[:grantedBy]-(p:permission) RETURN i.key AS k, p.key AS pk`,
		},
		{
			// The corpus's own shape: a staging WITH that keeps the arm's
			// result and lets the anchor's variable go. `i` is never named
			// again, so nothing re-seeds — and the anchor POSITION keeps its
			// hops, which is all walkToAnchors reads.
			name: "a WITH dropping the anchor's own variable strands nothing",
			body: `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH r MATCH (r)<-[:grantedBy]-(p:permission) RETURN r.key AS k, p.key AS pk`,
		},
		{
			name: "a WITH dropping a variable no later clause references is indexable",
			body: `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) OPTIONAL MATCH (i)-[:residesIn]->(u:unit) WITH i, r RETURN i.key AS k, r.key AS rk`,
		},
		{
			// `WITH a AS a` is the no-op spelling of `WITH a`, and the executor
			// treats it as one (projectItems' alias defaults to the variable's
			// own name). A rename is a different question — see below.
			name: "a self-aliased carry is a carry",
			body: `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH i AS i, r AS r MATCH (r)<-[:grantedBy]-(p:permission) RETURN i.key AS k, p.key AS pk`,
		},
		{
			// The staged-aggregation shape (packages/privacy-base's residue
			// lens): each stage collapses the previous arm's fan-out and lets
			// its variable go, and no arm is ever named again. The counts are
			// values under fresh names, so they collide with no position.
			name: "chained staging WITHs with aggregates are indexable",
			body: `MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)<-[:boundTo]-(c:credential)
WITH i, count(c.key) AS inbound
OPTIONAL MATCH (i)-[:indexes]->(x:credential)
WITH i, inbound, count(x.key) AS outbound
RETURN i.key AS k, inbound AS a, outbound AS b`,
		},
		{
			// The hazard itself. `r` is gone after the WITH, so matchPath seeds
			// the later pattern's head from the whole role bucket — a
			// dependency on a BUCKET, which no hop can express. The builder
			// cannot catch this on its own: position() merges the second `r`
			// onto the first, so ground() sees an already-grounded head.
			name:   "a dropped variable re-referenced as a later MATCH head is refused",
			body:   `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH i MATCH (r)<-[:grantedBy]-(p:permission) RETURN i.key AS k, p.key AS pk`,
			reject: "a WITH dropped `r` and a later clause re-references it",
		},
		{
			// A re-reference does not have to head a pattern to be wrong. Here
			// the second `r` is a fresh binding reached by traversal, and
			// merging it onto the first asserts a hop between vertices no row
			// ever relates — which can shorten a Dist and hand AnchorSideSeeds
			// the wrong endpoint.
			name:   "a dropped variable re-referenced anywhere in a later pattern is refused",
			body:   `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH i MATCH (i)-[:managesRole]->(r) RETURN i.key AS k, r.key AS rk`,
			reject: "a WITH dropped `r` and a later clause re-references it",
		},
		{
			// The WITH's own WHERE runs on the ALREADY-projected rows
			// (applyWith), so it reads the carried scope — a name this boundary
			// just let go of is unbound by the time its own filter evaluates.
			name:   "a dropped variable re-referenced by the WITH's own WHERE is refused",
			body:   `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH r WHERE i.key <> null MATCH (r)<-[:grantedBy]-(p:permission) RETURN r.key AS k, p.key AS pk`,
			reject: "a WITH dropped `i` and a later clause re-references it",
		},
		{
			// Chained boundaries compose: `r` goes at the first WITH and comes
			// back after the second. Judging only the nearest boundary would
			// call this clean, because the second WITH dropped nothing that is
			// named again.
			name: "a drop and a re-reference straddling two WITHs is refused",
			body: `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role)
WITH i
OPTIONAL MATCH (i)-[:residesIn]->(u:unit)
WITH i, u
MATCH (r)<-[:grantedBy]-(p:permission)
RETURN i.key AS k, p.key AS pk`,
			reject: "a WITH dropped `r` and a later clause re-references it",
		},
		{
			// The composition worth checking explicitly: a WITH can only carry
			// a binding the row actually HAS, and existsAsPredicate discards a
			// WHERE pattern's bindings, so `b` is unbound both before and after
			// this boundary. Carrying a name is therefore not evidence that a
			// pattern headed by it is grounded — the grounding conjunct is what
			// refuses this, and the WITH conjunct must not be read as covering
			// it.
			name:   "a WITH carrying a name only a WHERE pattern introduced does not ground it",
			body:   `MATCH (i:identity {key: $actorKey}) WHERE (i)-[:blocked]->(b:badge) WITH i, b MATCH (b)-[:issuedBy]->(o:org) RETURN i.key AS k, o.key AS ok`,
			reject: "not reached from the anchor",
		},
		{
			// A rename carries the BINDING under a name the builder keys
			// nothing to. Modelling it would mean renaming positions across the
			// boundary; refusing costs one BFS fallback.
			name:   "a renaming alias is a shape the index does not model",
			body:   `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH i, r AS q MATCH (q)<-[:grantedBy]-(p:permission) RETURN i.key AS k, p.key AS pk`,
			reject: "renaming the pattern variable `r` to `q`",
		},
		{
			// The dangerous rename: the alias lands on a name some pattern
			// already binds, so the builder would read the carried role as the
			// anchor's own position.
			name:   "a rename onto an existing pattern variable is refused by name",
			body:   `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH r AS i RETURN i.key AS k`,
			reject: "renaming the pattern variable `r` to `i`",
		},
		{
			// Same graft, reached through a computed item: after this WITH `r`
			// holds a number, while every position the builder keyed to `r`
			// still says "role".
			name:   "a computed item named after a pattern variable is refused",
			body:   `MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) WITH i, count(r.key) AS r RETURN i.key AS k, r AS n`,
			reject: "projecting a computed value under `r`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := indexOf(t, tc.body)
			if tc.reject == "" {
				require.True(t, ix.Complete,
					"must index this WITH, not fall back — got %q", ix.Incomplete)
				require.GreaterOrEqual(t, ix.Anchor, 0, "an indexed query has an anchor")
				return
			}
			require.False(t, ix.Complete, "must decline, not answer")
			require.Contains(t, ix.Incomplete, tc.reject)
		})
	}
}

// TestWithScopeReject_EmptyProjectionList pins the WITH conjunct's answer for a
// projection body that carries no item at all: the surviving set is not merely
// unknown, it is indistinguishable from "carries nothing", so the walk names the
// shape rather than reporting every variable as dropped.
//
// The conjunct runs over a clause list, not over cypher text, so it is held to
// one here — the cypher that reaches this shape, `WITH *`, is refused by Parse
// before any index is built.
func TestWithScopeReject_EmptyProjectionList(t *testing.T) {
	clauses := []Clause{
		&Match{Patterns: []PathPattern{{
			Nodes: []NodePattern{
				{Variable: "i", Label: "identity"},
				{Variable: "r", Label: "role"},
			},
			Rels: []RelPattern{{Type: "holdsRole", Direction: DirOut, MinHops: 1, MaxHops: 1}},
		}}},
		&With{},
		&Return{Items: []ProjectionItem{{Expr: &PropertyAccess{Target: &VariableRef{Name: "i"}, Key: "key"}, Alias: "k"}}},
	}
	require.Contains(t, withScopeReject(clauses), "a `WITH *`")
}

// TestAnchorHopIndex_WithScopeStagedLensWalks walks the accepted staging shape
// all the way to a seed. `Complete` is not a verdict about the query, it is a
// licence for the pipeline to reproject the anchors this graph derives AND NO
// OTHERS, so a staged lens has to be shown reaching its anchor across the
// boundary rather than merely surviving the predicate.
func TestAnchorHopIndex_WithScopeStagedLensWalks(t *testing.T) {
	ix := indexOf(t, `MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:meta)
WITH op, role
WHERE op.key <> null
RETURN op.key AS anchor, role.key AS viaRole`)
	require.True(t, ix.Complete, "the staged manifest shape must index: %s", ix.Incomplete)

	// A grantedBy event is the one that matters: its anchor-side endpoint is
	// the role, one hop from the anchor, and the walk has to reach the anchor
	// from there rather than from the permission.
	seeds := ix.AnchorSideSeeds("permission", "grantedBy", "role")
	require.Len(t, seeds, 1)
	require.Equal(t, "role", ix.Labels[seeds[0].Pos])
	require.False(t, seeds[0].SrcIsAnchorSide)
	require.Equal(t, 1, ix.Dist[seeds[0].Pos])

	// And the far side of the boundary is still connected to the anchor, which
	// is the property the drop of `identity` could have broken.
	forOp := ix.AnchorSideSeeds("permission", "forOperation", "meta")
	require.NotEmpty(t, forOp)
	for _, s := range forOp {
		require.GreaterOrEqual(t, ix.Dist[s.Pos], 0, "every seeded position must reach the anchor")
	}
}

// TestAnchorHopIndex_RangedBindingHopSeedsBothEndpoints is the ranged-hop
// counterpart of the assertion directly above, and the two stand together: that
// fixture carries no ranged hop, so every position it seeds holds a finite
// distance, while a ranged BINDING hop makes a distance an INTERVAL and the
// position takes HopIndex.Dist's incomparable sentinel instead.
//
// Why the sentinel and not a number: AnchorSideSeeds' `consider` drops the
// endpoint whose distance is the LARGER, so a position whose true distance is a
// range must be allowed neither to win nor to lose that comparison. The
// sentinel's branch seeds BOTH endpoints, and seeding both only widens the
// derived set — the safe direction for a walk whose invariant is a superset.
//
// The fixed-hop vector runs first. Without it, "both endpoints are seeded"
// would be indistinguishable from a `consider` that seeds both for every shape.
func TestAnchorHopIndex_RangedBindingHopSeedsBothEndpoints(t *testing.T) {
	fixed := indexOf(t, `MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location)-[:containedIn]->(loc:location)
RETURN identity.key AS actorKey, loc.key AS lk`)
	require.True(t, fixed.Complete, "%s", fixed.Incomplete)
	require.Equal(t, []int{0, 1, 2}, fixed.Dist, "identity → loc0 → loc, one edge per hop")
	fixedSeeds := fixed.AnchorSideSeeds("location", "containedIn", "location")
	require.Len(t, fixedSeeds, 1, "loc0 is provably nearer, so the far endpoint's seed is dropped")
	require.True(t, fixedSeeds[0].SrcIsAnchorSide)
	require.Equal(t, 1, fixed.Dist[fixedSeeds[0].Pos])

	ranged := indexOf(t, `MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location)-[:containedIn*0..]->(loc:location)
RETURN identity.key AS actorKey, loc.key AS lk`)
	require.True(t, ranged.Complete, "a ranged binding hop is indexed, not refused: %s", ranged.Incomplete)

	// The anchor keeps 0 — it is pinned to one vertex by `{key: $actorKey}`, so
	// it is never the endpoint a comparison has to place. Both other positions
	// are poisoned: loc across the ranged hop itself, and loc0 because the
	// undirected binding walk reaches it again THROUGH that same ranged hop.
	// Over-poisoning is deliberate (a position holding both a fixed path and a
	// possibly-shorter ranged one would otherwise keep an OVER-stated distance,
	// and an over-stated distance is what drops a seed).
	require.Equal(t, []int{0, -1, -1}, ranged.Dist)

	rangedSeeds := ranged.AnchorSideSeeds("location", "containedIn", "location")
	require.Len(t, rangedSeeds, 2, "an incomparable distance seeds BOTH endpoints")
	require.True(t, rangedSeeds[0].SrcIsAnchorSide)
	require.False(t, rangedSeeds[1].SrcIsAnchorSide)
	for _, s := range rangedSeeds {
		require.Equal(t, -1, ranged.Dist[s.Pos],
			"both seeds are here BECAUSE the distance is the incomparable sentinel, not by accident")
	}

	// The fixed residesIn hop on the same pattern seeds both endpoints too,
	// because its far endpoint is one of the poisoned positions. That is the
	// widening the sentinel licenses, stated rather than left to be discovered.
	residesSeeds := ranged.AnchorSideSeeds("identity", "residesIn", "location")
	require.Len(t, residesSeeds, 2)
}

// TestAnchorHopIndex_OpenRangeMaxIsTheExecutorsOwnClamp is the shared-clamp
// proof, MEASURED rather than restated. Asserting the stored bound against the
// constant alone would be a tautology — the builder reads that same identifier —
// so the executor is driven over a chain LONGER than the clamp and the depth it
// actually reaches is what the index's stored Max is compared against.
//
// The claim this carries is the soundness argument itself: the derivation is
// complete with respect to what the EXECUTOR will evaluate, not with respect to
// the graph. A path crossing more of this relation than traverseRel walks
// produces no row at all, so a derivation that stops at the same depth misses
// nothing that could have changed.
func TestAnchorHopIndex_OpenRangeMaxIsTheExecutorsOwnClamp(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	// A containment chain three links longer than any clamp the executor could
	// be reading, so the depth it stops at is a measurement and not the length
	// of the fixture.
	const chain = maxVarLengthHops + 3
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	prev := "alice"
	for i := 1; i <= chain; i++ {
		name := fmt.Sprintf("clamp%02d", i)
		putVertex(t, reg, coreKV, name, "location", nil)
		putEdge(t, reg, adjKV, "containedIn", prev, name)
		prev = name
	}

	const body = `MATCH (identity:identity {key: $actorKey})-[:containedIn*0..]->(loc)
RETURN loc.key AS lkey`
	results := parseExec(t, body,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	// One row per admitted endpoint: the zero-hop admission of `alice` itself,
	// plus one chain node per hop the frontier actually took.
	walked := len(results) - 1
	require.Equal(t, maxVarLengthHops, walked,
		"traverseRel's own frontier stops here, on a chain that goes further")

	ix := indexOf(t, body)
	require.True(t, ix.Complete, "%s", ix.Incomplete)
	require.Len(t, ix.Hops, 1)
	require.Equal(t, 0, ix.Hops[0].Min, "`*0..` keeps its zero-hop admission")
	require.Equal(t, walked, ix.Hops[0].Max,
		"the stored bound IS the depth the executor walked — one clamp, read at one site")
}

// TestAnchorHopIndex_RangeBoundsPassThroughUnderTheClamp is the other half of
// the clamp's contract: it BOUNDS, it does not rewrite. A range the executor
// would honour verbatim is stored verbatim, so a lens that deliberately bounds
// its own walk keeps the bound it wrote — and only an open or over-long one is
// pulled down to what traverseRel will actually walk.
func TestAnchorHopIndex_RangeBoundsPassThroughUnderTheClamp(t *testing.T) {
	for _, tc := range []struct {
		name             string
		rel              string
		wantMin, wantMax int
	}{
		{name: "a bounded range under the clamp is stored verbatim", rel: "containedIn*1..4", wantMin: 1, wantMax: 4},
		{name: "a bound equal to the clamp is stored verbatim", rel: "containedIn*0..10", wantMin: 0, wantMax: maxVarLengthHops},
		{name: "a bound past the clamp is pulled down to it", rel: "containedIn*0..99", wantMin: 0, wantMax: maxVarLengthHops},
		{name: "an open range takes the clamp", rel: "containedIn*1..", wantMin: 1, wantMax: maxVarLengthHops},
		{name: "a fixed single hop is the degenerate range", rel: "containedIn", wantMin: 1, wantMax: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := indexOf(t, `MATCH (identity:identity {key: $actorKey})-[:`+tc.rel+`]->(loc:location)
RETURN identity.key AS actorKey, loc.key AS lk`)
			require.True(t, ix.Complete, "%s", ix.Incomplete)
			require.Len(t, ix.Hops, 1)
			require.Equal(t, tc.wantMin, ix.Hops[0].Min)
			require.Equal(t, tc.wantMax, ix.Hops[0].Max)

			// StepsFrom carries the range through unchanged in BOTH readings —
			// a bounded reachability relation is symmetric, and edgeDirFor has
			// already flipped the direction the walk reads.
			for pos := range ix.Labels {
				for _, s := range ix.StepsFrom(pos) {
					require.Equal(t, tc.wantMin, s.Min, "position %d", pos)
					require.Equal(t, tc.wantMax, s.Max, "position %d", pos)
				}
			}
		})
	}
}

// TestAnchorHopIndex_EmptyExpansionIsUnresolved pins the conjunct that stands
// between an indexable `capabilityServiceAccess` and a silently dropped
// revocation.
//
// A `*` label resolving to a present-but-EMPTY concrete set is a real state, not
// an error: ruleinstall.go warns and degrades the lens to its broad consumer
// filter. But admitsType then admits no type at either expanded position, so a
// walk over such an index builds ZERO seeds and returns an empty derived set —
// which a caller holding a Complete index reads as "no anchor can change" and
// acts on by reprojecting nobody. This lens mints `cap.svc.<actor>`, so the
// event that gets dropped is a revocation.
//
// The empty set therefore has to answer the same way a missing one does. The
// positive vector comes first, or the assertion pins nothing: the SAME index
// with the expansion actually resolved must report -1 and seed both endpoints.
func TestAnchorHopIndex_EmptyExpansionIsUnresolved(t *testing.T) {
	ix := indexOf(t, shippedCapabilityServiceAccess)
	require.True(t, ix.Complete, "%s", ix.Incomplete)

	resolved := ix.WithLabelExpansion(map[string]map[string]struct{}{
		"location": {"unit": {}, "building": {}, "property": {}},
	})
	require.Equal(t, -1, resolved.UnresolvedExpansionPosition(),
		"a resolved expansion is answerable, or the empty-set assertion below pins nothing")
	require.NotEmpty(t, resolved.AnchorSideSeeds("unit", "containedIn", "building"),
		"the resolved index must seed the containment hop it indexes")

	empty := ix.WithLabelExpansion(map[string]map[string]struct{}{"location": {}})
	require.GreaterOrEqual(t, empty.UnresolvedExpansionPosition(), 0,
		"an expansion resolving to nothing admits nothing, so the index must decline rather than derive the empty set")
	require.Empty(t, empty.AnchorSideSeeds("unit", "containedIn", "building"),
		"the empty expansion really does seed nothing — which is why declining is the only safe answer")
}

// TestAnchorHopIndex_RefusesALowerBoundAboveOneHop pins the seeding limit that
// bounds which ranged shapes may be indexed at all.
//
// AnchorSideSeeds seeds the two endpoints of the CHANGED LINK. On a ranged hop
// that link is an intermediate edge of the expansion, so the nodes really bound
// at the From position are every node reaching it within Max-1 steps. Those are
// recovered by the walk bouncing back across the hop from the To seed, which
// covers From-offsets [1-Max, 1-Min]; with the direct seed at offset 0 that
// covers the required [1-Max, 0] only while Min <= 2. Above that the offset -1
// binding needs a second bounce, and a node near the edge of its component has
// nowhere to bounce from — so the derivation would answer ok == true having
// dropped an anchor, which on the auth plane is a revocation that never fires.
//
// The positive vector comes first: the same pattern at Min == 1 indexes, so the
// refusal is the lower bound talking and not the range.
func TestAnchorHopIndex_RefusesALowerBoundAboveOneHop(t *testing.T) {
	const body = `MATCH (identity:identity {key: $actorKey})-[:containedIn*%s]->(loc:location)
RETURN identity.key AS actorKey, loc.key AS lk`

	indexed := indexOf(t, fmt.Sprintf(body, "1..4"))
	require.True(t, indexed.Complete, "%s", indexed.Incomplete)

	for _, rel := range []string{"2..4", "3..", "5..5"} {
		t.Run(rel, func(t *testing.T) {
			ix := indexOf(t, fmt.Sprintf(body, rel))
			require.False(t, ix.Complete)
			require.Contains(t, ix.Incomplete, "lower bound exceeds one hop")
		})
	}
}

// TestAnchorHopIndex_WithClauseCarriesItsOwnPatterns covers the half of a WITH
// that is not a scope question at all: the clause's items and its WHERE are
// general expressions, and a pattern living in either one is a pattern the
// graph has to contain.
//
// Accepting a WITH is a licence to act on this index, and the failure this
// pins is the worst-shaped one available. A relation named ONLY inside a WITH
// contributes no hop, so AnchorSideSeeds returns an EMPTY set — and on a
// Complete index the caller reads empty as "no anchor can change" and skips.
// The relation these clauses hold is typically the revocation filter, so the
// skipped reprojection is the one that would have retracted a grant.
//
// The seed assertions are what make each case a positive vector: `Complete` and
// a non-empty Hops slice would both survive a graph that indexed the wrong
// endpoint.
func TestAnchorHopIndex_WithClauseCarriesItsOwnPatterns(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		// the link whose events must reach an anchor, and the position label
		// its anchor-side seed must land on
		srcType, rel, dstType string
		wantSeedLabel         string
	}{
		{
			// A revocation staged behind a WITH — the shape the auth plane
			// writes revocations in, and the one where an empty seed set is an
			// over-grant rather than a slow reprojection.
			name: "a negated pattern in a WITH's WHERE contributes its hop",
			body: `MATCH (a:identity {key: $actorKey})-[:holdsRole]->(r:role)
WITH a, r WHERE NOT (r)-[:revokedBy]->(v:revocation)
RETURN a.key AS actor_key, r.key AS role_key`,
			srcType: "role", rel: "revokedBy", dstType: "revocation",
			wantSeedLabel: "role",
		},
		{
			// A positive WITH-WHERE pattern, which drops nothing at all — so
			// withScopeReject is not even the predicate that admits it, and the
			// traversal is the only thing standing between this lens and a
			// silent skip on every scopedTo event.
			name: "a positive pattern in a WITH's WHERE contributes its hop",
			body: `MATCH (a:identity {key: $actorKey})-[:holdsRole]->(r:role)
WITH a, r WHERE (r)-[:scopedTo]->(u:unit)
RETURN a.key AS k, r.key AS rk`,
			srcType: "role", rel: "scopedTo", dstType: "unit",
			wantSeedLabel: "role",
		},
		{
			// A comprehension in a WITH ITEM. The control for this one is the
			// same comprehension in a RETURN, which the builder has always
			// walked — the omission was never about comprehensions, only about
			// which clause held them.
			name: "a pattern comprehension in a WITH item contributes its hop",
			body: `MATCH (a:identity {key: $actorKey})
WITH a, [(a)-[:holdsRole]->(r:role) | r.key] AS roles
RETURN a.key AS actor_key, roles AS roles`,
			srcType: "identity", rel: "holdsRole", dstType: "role",
			wantSeedLabel: "identity",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := indexOf(t, tc.body)
			require.True(t, ix.Complete, "must index, not fall back — got %q", ix.Incomplete)

			seeds := ix.AnchorSideSeeds(tc.srcType, tc.rel, tc.dstType)
			require.NotEmptyf(t, seeds,
				"a %s event derives NO anchor, which a complete index licenses the caller to read as a skip", tc.rel)
			labels := make([]string, 0, len(seeds))
			for _, s := range seeds {
				labels = append(labels, ix.Labels[s.Pos])
			}
			require.Containsf(t, labels, tc.wantSeedLabel,
				"the anchor-side seed must be the %s position", tc.wantSeedLabel)

			// The same invariant the corpus census enforces mechanically
			// (TestCorpusAnchorHopIndex_CompleteIndexHoldsEveryReferencedRelation),
			// asserted here on vectors the shipped corpus does not contain: on a
			// COMPLETE index every relation the sibling derivation names must sit
			// on a hop. Proving the gate's own predicate catches these shapes is
			// what stops it being a check that only ever passes.
			rels, exhaustive := parseFull(t, tc.body).ReferencedRelations()
			require.True(t, exhaustive, "these vectors carry only typed single hops")
			indexed := map[string]struct{}{}
			for _, h := range ix.Hops {
				indexed[h.Rel] = struct{}{}
			}
			for rel := range rels {
				require.Containsf(t, indexed, rel, "`%s` is read by the pattern but absent from Hops", rel)
			}
		})
	}
}

// TestAnchorHopIndex_WithPatternIsNonBinding pins the POSTURE those hops arrive
// with. A WITH's WHERE and its items both discard their bindings
// (existsAsPredicate, evalPatternComprehension), so counting them as binding
// would let a negated shortcut make a far position look nearer the anchor —
// and the walk would then be seeded at the endpoint that can only reach the
// anchor by crossing the edge a tombstone just removed.
func TestAnchorHopIndex_WithPatternIsNonBinding(t *testing.T) {
	ix := indexOf(t, `MATCH (a:identity {key: $actorKey})-[:holdsRole]->(r:role)
WITH a, r WHERE NOT (a)-[:blocked]->(r)
RETURN a.key AS k, r.key AS rk`)
	require.True(t, ix.Complete, "%s", ix.Incomplete)

	var holds, blocked *PatternHop
	for i := range ix.Hops {
		switch ix.Hops[i].Rel {
		case "holdsRole":
			holds = &ix.Hops[i]
		case "blocked":
			blocked = &ix.Hops[i]
		}
	}
	require.NotNil(t, holds, "the MATCH hop must be indexed")
	require.NotNil(t, blocked, "the WITH-WHERE hop must be indexed")
	require.True(t, holds.Binding, "a MATCH pattern binds")
	require.False(t, blocked.Binding, "a WITH's WHERE pattern is a filter, not a binding")
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
	require.Equal(t, []PatternStep{{ToPos: out.Anchor, Rel: "holdsRole", EdgeDir: "in", ToLabel: "identity", Min: 1, Max: 1}}, out.StepsFrom(far))
	require.Equal(t, []PatternStep{{ToPos: far, Rel: "holdsRole", EdgeDir: "out", ToLabel: "role", Min: 1, Max: 1}}, out.StepsFrom(out.Anchor))

	// Inbound as written: (anchor)<-[:r]-(x). Standing at x, the anchor is
	// reached by x's OUTBOUND r edges.
	in := indexOf(t, `MATCH (i:identity {key: $actorKey})<-[:assignedTo]-(x:task) RETURN i.key AS k, x.key AS r`)
	require.True(t, in.Complete, "%s", in.Incomplete)
	far = 1 - in.Anchor
	require.Equal(t, []PatternStep{{ToPos: in.Anchor, Rel: "assignedTo", EdgeDir: "out", ToLabel: "identity", Min: 1, Max: 1}}, in.StepsFrom(far))
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

// TestDeclaresActorAnchor_IsIndependentOfCompleteness is the property
// ConsumerFilter's install-completeness guard rests on: the DECLARATION survives
// every refusal the index can raise for some other reason. Reading Anchor only
// on a Complete index would report the shapes below — a real slice of the
// shipped actorAggregate and Personal corpus — as plain, and the guard would
// then let a pre-install call narrow exactly the lenses it exists to protect.
func TestDeclaresActorAnchor_IsIndependentOfCompleteness(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantDeclared   bool
		wantIncomplete string
	}{
		{
			name:         "indexable actor-anchored lens",
			body:         shippedCapabilityRoles,
			wantDeclared: true,
		},
		{
			// An untyped relationship — objectAttachments' shape. It is a
			// WILDCARD hop, not a refusal: the index is complete and the hop
			// names no relation. TestAnchorHopIndex_UntypedHopIsAWildcard pins
			// what the hop then admits.
			name: "untyped relationship — objectAttachments' shape",
			body: `
MATCH (identity:identity {key: $actorKey})-[]->(o:object)
RETURN identity.key AS actorKey, o.key AS ok
`,
			wantDeclared: true,
		},
		{
			name: "pattern head the anchor never reaches — the ungrounded refusal",
			body: `
MATCH (identity:identity {key: $actorKey})
MATCH (r:role)-[:grantedBy]->(p:permission)
RETURN identity.key AS actorKey, p.key AS pk
`,
			wantDeclared:   true,
			wantIncomplete: "is not reached from the anchor",
		},
		{
			name: "two positions pin the anchor — the multi-anchor refusal",
			body: `
MATCH (a:identity {key: $actorKey})-[:knows]->(b:identity {key: $actorKey})
RETURN a.key AS actorKey, b.key AS bk
`,
			wantDeclared:   true,
			wantIncomplete: "several pattern positions bind $actorKey",
		},
		{
			name: "a plain lens declares nothing",
			body: `
MATCH (u:unit)-[:listedBy]->(l:landlord)
RETURN u.key AS k, l.key AS lk
`,
			wantDeclared:   false,
			wantIncomplete: "no pattern position binds $actorKey",
		},
		{
			name: "the parameter appears, but not as the key property",
			body: `
MATCH (u:unit)
WHERE u.data.owner <> $actorKey
RETURN u.key AS k
`,
			wantDeclared:   false,
			wantIncomplete: "no pattern position binds $actorKey",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := parseFull(t, tc.body)
			require.Equal(t, tc.wantDeclared, cr.DeclaresActorAnchor())
			ix := cr.AnchorHopIndex()
			if tc.wantIncomplete == "" {
				require.True(t, ix.Complete, "%s", ix.Incomplete)
				return
			}
			require.False(t, ix.Complete)
			require.Contains(t, ix.Incomplete, tc.wantIncomplete)
		})
	}
}

// TestDeclaresActorAnchor_NilSafe pins the two zero shapes the pipeline can hold
// before any rule is published: neither may claim a declaration.
func TestDeclaresActorAnchor_NilSafe(t *testing.T) {
	var nilCR *CompiledRule
	require.False(t, nilCR.DeclaresActorAnchor())
	require.False(t, (&CompiledRule{}).DeclaresActorAnchor())
}

// The shipped cypher of clinicProviders (packages/clinic-domain/lenses.go),
// the plain lens plain-lens-neighbour-anchor-derivation-design.md §4.2 traces
// its payoff through conjunct by conjunct. Copied verbatim so a change to the
// real lens that ScanRootHopIndex cannot index shows up as a test failure
// here rather than as a silent fallback in production.
const shippedClinicProviders = `MATCH (pr:provider)
WHERE pr.profile.data.fullName <> null
OPTIONAL MATCH (pr)-[:identifiedBy]->(id:identity)
RETURN
  pr.key AS key,
  pr.key AS providerKey,
  pr.profile.data.fullName AS name,
  id.key AS identityKey`

// TestScanRootHopIndex_ClinicProviders walks the real lens end to end: the
// terminus it finds, and — the point of the whole increment — that an
// identifiedBy link event's anchor-side seed IS the root itself (distance 0),
// so the identity endpoint's rescan collapses into a duplicate of the
// provider endpoint's own seed rather than a second, unseeded walk.
func TestScanRootHopIndex_ClinicProviders(t *testing.T) {
	ix := rootIndexOf(t, shippedClinicProviders)

	require.True(t, ix.Complete, "the shipped clinicProviders must be indexable: %s", ix.Incomplete)
	require.Equal(t, "provider", ix.Labels[ix.Anchor])
	require.Equal(t, []int{0, 1}, ix.Dist, "provider → identity")

	seeds := ix.AnchorSideSeeds("provider", "identifiedBy", "identity")
	require.Len(t, seeds, 1)
	require.Equal(t, ix.Anchor, seeds[0].Pos, "the root-side endpoint IS the terminus — a zero-adjacency-read seed")
	require.True(t, seeds[0].SrcIsAnchorSide, "the provider endpoint is the source of an identifiedBy link")

	// The identity endpoint reaches the root by reading its own OUTBOUND
	// identifiedBy edge backwards (the pattern writes (pr)-[:identifiedBy]->(id)),
	// which is the one adjacency read a neighbour-side event would pay if it
	// ever needed to walk instead of seeding directly.
	var towardRoot []PatternStep
	for _, s := range ix.StepsFrom(1 - ix.Anchor) {
		if s.ToPos == ix.Anchor {
			towardRoot = append(towardRoot, s)
		}
	}
	require.Len(t, towardRoot, 1)
	require.Equal(t, "identifiedBy", towardRoot[0].Rel)
	require.Equal(t, "in", towardRoot[0].EdgeDir)
	require.Equal(t, "provider", towardRoot[0].ToLabel)
}

// TestScanRootHopIndex_Refusals covers ScanRootHopIndex's own completeness
// conjuncts (plain-lens-neighbour-anchor-derivation-design.md §4.1's table),
// one at a time. Per the standing rule a negative test needs a positive
// vector first, each case pairs the refusing shape with a positive twin that
// DOES narrow — so "refused" is never indistinguishable from "the harness
// never reached the code" (mirrors TestAnchorHopIndex_Refusals).
func TestScanRootHopIndex_Refusals(t *testing.T) {
	const positive = `MATCH (pr:provider)-[:identifiedBy]->(id:identity) RETURN pr.key AS key, id.key AS idk`

	for _, tc := range []struct {
		name     string
		refused  string
		positive string
		want     string
	}{
		{
			name:     "an unlabeled anchor pattern position carries no label",
			refused:  `MATCH (pr)-[:identifiedBy]->(id:identity) RETURN pr.key AS key, id.key AS idk`,
			positive: positive,
			want:     "the anchor pattern position carries no label",
		},
		{
			// Already a point read (the executor's key-property fast path) —
			// there is no scan here for a terminus to remove.
			name:     "an anchor pattern pinned by its own key is already a point read",
			refused:  `MATCH (pr:provider {key: $actorKey})-[:identifiedBy]->(id:identity) RETURN pr.key AS key, id.key AS idk`,
			positive: positive,
			want:     "the anchor pattern is pinned by its own key",
		},
		{
			name:     "a `*` sigil on the anchor pattern cannot mint one key prefix",
			refused:  `MATCH (pr:provider*)-[:identifiedBy]->(id:identity) RETURN pr.key AS key, id.key AS idk`,
			positive: positive,
			want:     "taxonomy-expansion sigil",
		},
		{
			// The conjunct AnchorHopIndex's own `b.anchor < 0` early-out
			// swallows for every plain lens: there is no $actorKey position
			// to judge groundedness relative to, so AnchorHopIndex's switch
			// never reaches its ungrounded case for one (see
			// TestScanRootHopIndex_UngroundedIsReachableWhereAnchorHopIndexSwallowsIt).
			// ScanRootHopIndex has a terminus for every plain lens, so a
			// cartesian second MATCH is now the real, load-bearing refusal.
			name:     "a second MATCH headed by a variable the first never bound is refused",
			refused:  `MATCH (pr:provider) MATCH (o:org)-[:employs]->(x:identity) RETURN pr.key AS key, o.key AS ok`,
			positive: `MATCH (pr:provider)-[:employedBy]->(o:org) MATCH (o)-[:owns]->(x:asset) RETURN pr.key AS key, x.key AS xk`,
			want:     "not reached from the anchor",
		},
		{
			// `pr` is gone after the WITH, so a later MATCH headed by it seeds
			// from the whole provider bucket — the same bucket-scan hazard
			// TestAnchorHopIndex_WithScope pins, over the same withScopeReject
			// mechanism, on the position that here is a row column
			// (`pr.key`) rather than the `$actorKey` parameter.
			name:     "a WITH dropping the anchor pattern's own variable and re-referencing it is refused",
			refused:  `MATCH (pr:provider)-[:identifiedBy]->(id:identity) WITH id MATCH (pr)-[:employedBy]->(o:org) RETURN id.key AS key, o.key AS ok`,
			positive: `MATCH (pr:provider)-[:identifiedBy]->(id:identity) WITH pr, id MATCH (id)-[:worksAt]->(o:org) RETURN pr.key AS key, o.key AS ok`,
			want:     "a WITH dropped",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refused := rootIndexOf(t, tc.refused)
			require.False(t, refused.Complete, "must decline, not answer")
			require.Contains(t, refused.Incomplete, tc.want)

			ok := rootIndexOf(t, tc.positive)
			require.True(t, ok.Complete, "the positive vector must narrow: %s", ok.Incomplete)
			require.GreaterOrEqual(t, ok.Anchor, 0, "an indexed query has a terminus")
		})
	}
}

// TestScanRootHopIndex_UngroundedIsReachableWhereAnchorHopIndexSwallowsIt is
// the regression for §4.1's ordering note: the SAME query refuses for two
// DIFFERENT reasons depending on which index asks, because AnchorHopIndex's
// `b.anchor < 0` conjunct fires first for any plain lens (there is no
// $actorKey position at all) and never reaches its own ungrounded check.
func TestScanRootHopIndex_UngroundedIsReachableWhereAnchorHopIndexSwallowsIt(t *testing.T) {
	const body = `MATCH (pr:provider) MATCH (o:org)-[:employs]->(x:identity) RETURN pr.key AS key, o.key AS ok`

	anchor := indexOf(t, body)
	require.False(t, anchor.Complete)
	require.Equal(t, "no pattern position binds $actorKey", anchor.Incomplete,
		"AnchorHopIndex refuses every plain lens on the anchor conjunct alone — it never reaches the ungrounded one")

	root := rootIndexOf(t, body)
	require.False(t, root.Complete)
	require.Contains(t, root.Incomplete, "not reached from the anchor",
		"ScanRootHopIndex has a terminus for this query, so the cartesian second MATCH is the real, load-bearing refusal")
}

// TestScanRootHopIndex_MultiAnchorHasNoCounterpart pins §4.1's dead-code note:
// several `{key: $actorKey}` positions still refuse AnchorHopIndex (its own
// conjunct), but say nothing about ScanRootHopIndex at all — the root is one
// position by construction, however many times $actorKey is pinned elsewhere
// in the same query.
func TestScanRootHopIndex_MultiAnchorHasNoCounterpart(t *testing.T) {
	// Two DIFFERENT positions pin $actorKey — refuses AnchorHopIndex on its
	// own multiAnchor conjunct. Neither is the root (pr, which carries no
	// key property at all), so ScanRootHopIndex's own key-pinned conjunct
	// does not fire either — the two refusals are structurally unrelated.
	const body = `MATCH (pr:provider)-[:employedBy]->(a:identity {key: $actorKey})
MATCH (pr)-[:reviewedBy]->(b:identity {key: $actorKey})
RETURN pr.key AS key, a.key AS ak, b.key AS bk`

	anchor := indexOf(t, body)
	require.False(t, anchor.Complete)
	require.Contains(t, anchor.Incomplete, "several pattern positions bind $actorKey")

	root := rootIndexOf(t, body)
	require.True(t, root.Complete, "%s", root.Incomplete)
	require.Equal(t, "provider", root.Labels[root.Anchor])
}

// TestScanRootHopIndex_PatternInsideAnchorPropertyMap is the §4.1 ordering
// constraint's own trip-wire: addPattern's rootHere branch must record the
// terminus from the anchor pattern's Nodes[0] BEFORE the loop that walks that
// same node's property-map expressions, because a PatternExpr inside the
// property map (an `exists((pr)-[...]->(...))` predicate on the anchor's own
// properties) reaches ground() through addExpr while the terminus is still
// unset if the ordering is ever inverted — and ground() would then refuse the
// whole index as ungrounded, for a lens that is genuinely indexable. Getting
// this wrong would not show up in TestScanRootHopIndex_Refusals (none of
// those shapes carry a pattern inside the ROOT's own property map), so it
// needs its own case.
func TestScanRootHopIndex_PatternInsideAnchorPropertyMap(t *testing.T) {
	const body = `MATCH (pr:provider {tag: exists((pr)-[:taggedBy]->(t:tag))})-[:identifiedBy]->(id:identity)
RETURN pr.key AS key, id.key AS idk`

	ix := rootIndexOf(t, body)
	require.True(t, ix.Complete,
		"a PatternExpr in the anchor's OWN property map must not make ground() see an unset terminus: %s", ix.Incomplete)
	require.Equal(t, "provider", ix.Labels[ix.Anchor])
}

// TestScanRootHopIndex_UnnamedRootPins pins today's answer for an anchor
// pattern with no variable at all — position() mints an unnamed node its own
// fresh class (hopindex.go's position doc), and addPattern's rootHere branch
// reuses b.root for Nodes[0] rather than calling position() a second time
// (which would orphan the terminus onto a second, unnamed class). A future
// edit that "simplified" that special case away would silently stop indexing
// every plain lens whose anchor pattern carries no variable, with the rest of
// this file staying green.
func TestScanRootHopIndex_UnnamedRootPins(t *testing.T) {
	const body = `MATCH (:provider)-[:identifiedBy]->(id:identity) RETURN id.key AS key`

	ix := rootIndexOf(t, body)
	require.True(t, ix.Complete, "%s", ix.Incomplete)
	require.Equal(t, "provider", ix.Labels[ix.Anchor], "the unnamed anchor pattern is still the terminus")
	require.Equal(t, []int{0, 1}, ix.Dist, "the neighbour identity sits one binding hop from the unnamed root")
}

// TestAnchorHopIndex_UntypedHopIsAWildcard pins what an untyped `-[r]->`
// becomes: a hop carrying Rel == "" on a COMPLETE index, which every consumer
// reads as admit-any in the relation dimension.
//
// The negative vector is what gives the positive one meaning. A wildcard hop
// admits by RELATION and still discriminates by TYPE: the pattern's positions
// are as labeled as they ever were, so a link whose endpoints cannot sit at them
// seeds nothing, and the empty answer stays the licence to skip that
// AnchorSideSeeds' contract says it is.
func TestAnchorHopIndex_UntypedHopIsAWildcard(t *testing.T) {
	ix := indexOf(t, `
MATCH (a:object {key: $actorKey})-[r]->(b)
RETURN a.key AS actorKey, b.key AS bk
`)
	require.True(t, ix.Complete, "%s", ix.Incomplete)
	require.Len(t, ix.Hops, 1)
	require.Equal(t, "", ix.Hops[0].Rel,
		"an untyped hop names no relation, and that empty name IS the wildcard")

	for _, rel := range []string{"photoOf", "avatarOf", "someRelationNoPatternMentions"} {
		require.NotEmptyf(t, ix.AnchorSideSeeds("object", rel, "identity"),
			"a wildcard hop is a candidate for every relation, `%s` included", rel)
	}

	// The far end is unlabeled, so it admits every destination type too.
	require.NotEmpty(t, ix.AnchorSideSeeds("object", "photoOf", "object"))

	// Discrimination by type survives: `a` must be an `object`, so a link whose
	// source is an identity binds no hop and the executor could not have bound
	// it either.
	require.Empty(t, ix.AnchorSideSeeds("identity", "holdsRole", "role"),
		"the wildcard admits by relation; the pattern's own labels still decide the type")

	// ScanRootHopIndex reads the same builder, so the plain arm records the same
	// wildcard — objectIdentityAttachmentsRead's shape.
	rx := rootIndexOf(t, `
MATCH (pr:provider)-[r]->(id:identity)
RETURN pr.key AS key, id.key AS idk
`)
	require.True(t, rx.Complete, "%s", rx.Incomplete)
	require.Len(t, rx.Hops, 1)
	require.Equal(t, "", rx.Hops[0].Rel)
}
