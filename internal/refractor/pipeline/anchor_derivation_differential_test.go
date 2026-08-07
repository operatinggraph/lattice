package pipeline

// The superset proof §9 asks for, and the evidence that licenses Increment 3b's
// flip (auth-plane-projection-latency-design.md §17).
//
// Every other test of the derivation asserts what it returns for one shape the
// test author had in mind. That proves the derivation agrees with its author,
// which is not the property the auth plane needs. The property it needs is:
//
//	for every mutation, every anchor whose PROJECTED ROW actually changes
//	appears in the derived set.
//
// So this test never states an expected set. It executes the real cypher for
// every anchor before and after each mutation, takes the anchors whose rows
// genuinely differ as ground truth, and requires the derivation to have named
// all of them. A shrinking bug fails here rather than in production, where its
// symptom is a grant that outlives its revocation.
//
// Ground truth is the recompute, NOT the ActorEnumerator BFS — which is the
// difference between this and the shadow. The BFS is a superset of the truth by
// construction, so agreeing with it proves nothing about the direction that
// matters, and disagreeing with it is the win being measured.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// diffFixture holds the graph both halves of the comparison read: the recompute
// reads coreKV + adjKV through the engine, the derivation reads adjKV through
// its own walk, and a mutation must land in both exactly as the pipeline lands
// it.
type diffFixture struct {
	t       *testing.T
	coreKV  *substrate.KV
	adjKV   *substrate.KV
	p       *Pipeline
	eng     *full.Engine
	cr      ruleengine.CompiledRule
	anchors []string // every identity vertex key, the anchors to recompute
	ids     map[string]string
	types   map[string]string
	now     string
}

func newDiffFixture(t *testing.T, spec string) *diffFixture {
	t.Helper()
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)

	p := &Pipeline{
		ruleID:       "differential",
		coreKVBucket: "CORE",
		coreKV:       coreKV,
		adjKV:        adjKV,
		engineKind:   ruleengine.EngineFull,
	}
	p.UseFullEngine(eng, cr)
	p.SetActorEnumerator(NewActorEnumerator(adjKV, coreKV, "identity"))

	return &diffFixture{
		t: t, coreKV: coreKV, adjKV: adjKV, p: p, eng: eng, cr: cr,
		ids:   map[string]string{},
		types: map[string]string{},
		// Far enough ahead that every expiresAt in the fixture is live, so the
		// `$now` predicate never becomes the reason a row changed.
		now: "2020-01-01T00:00:00Z",
	}
}

// vertex writes a Contract #1 vertex document and registers it under a logical
// name. Identities are additionally recorded as anchors, since the recompute
// runs once per anchor and the set has to be complete for "every anchor whose
// row changed" to mean anything.
func (f *diffFixture) vertex(name, vtype string, data map[string]any) string {
	f.t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(f.t, err)
	f.ids[name] = id
	f.types[id] = vtype
	key := substrate.VertexKey(vtype, id)
	f.writeVertex(key, vtype, data)
	if vtype == "identity" {
		f.anchors = append(f.anchors, key)
	}
	return key
}

func (f *diffFixture) writeVertex(key, vtype string, data map[string]any) {
	f.t.Helper()
	body := map[string]any{
		"key": key, "class": vtype, "isDeleted": false,
		"createdAt": "2019-01-01T00:00:00Z", "lastModifiedAt": "2019-01-01T00:00:00Z",
		"data": data,
	}
	raw, err := json.Marshal(body)
	require.NoError(f.t, err)
	_, err = f.coreKV.Put(context.Background(), key, raw)
	require.NoError(f.t, err)
}

func (f *diffFixture) key(name string) string {
	id := f.ids[name]
	require.NotEmpty(f.t, id, "fixture: %q not registered", name)
	return substrate.VertexKey(f.types[id], id)
}

func (f *diffFixture) linkKey(rel, from, to string) string {
	fromID, toID := f.ids[from], f.ids[to]
	require.NotEmpty(f.t, fromID, "fixture: %q not registered", from)
	require.NotEmpty(f.t, toID, "fixture: %q not registered", to)
	return fmt.Sprintf("lnk.%s.%s.%s.%s.%s", f.types[fromID], fromID, rel, f.types[toID], toID)
}

// applyLink mirrors evaluateLinkFanOut's own idempotent adjacency write —
// including that a tombstone removes the edge BEFORE the derivation walks, which
// is the ordering the derivation's seeding has to survive.
func (f *diffFixture) applyLink(rel, from, to string, deleted bool) string {
	f.t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[from], f.ids[to]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := f.linkKey(rel, from, to)
	for _, evt := range []adjacency.CoreKVEvent{
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "outbound",
			NodeID: fromID, OtherNodeID: toID, OtherType: toType, IsDeleted: deleted},
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "inbound",
			NodeID: toID, OtherNodeID: fromID, OtherType: fromType, IsDeleted: deleted},
	} {
		require.NoError(f.t, adjacency.Build(ctx, f.adjKV, evt))
	}
	return linkKey
}

// rows executes the real cypher for one anchor and renders its result set as a
// comparable string. Ordering inside a result set is not part of the
// projection's meaning, so it is sorted away — otherwise a reordering would be
// counted as a change and the test would pass for the wrong reason, demanding
// the derivation name anchors nothing actually happened to.
func (f *diffFixture) rows(anchor string) string {
	f.t.Helper()
	results, err := f.eng.ExecuteWith(context.Background(), f.cr, ruleengine.EventContext{
		NodeKey:    anchor,
		Parameters: map[string]any{"actorKey": anchor, "now": f.now},
	}, f.adjKV, f.coreKV)
	require.NoError(f.t, err)
	rendered := make([]string, 0, len(results))
	for _, r := range results {
		k, err := json.Marshal(r.Key)
		require.NoError(f.t, err)
		v, err := json.Marshal(r.Values)
		require.NoError(f.t, err)
		rendered = append(rendered, fmt.Sprintf("%t|%s|%s", r.Delete, k, v))
	}
	sort.Strings(rendered)
	out, err := json.Marshal(rendered)
	require.NoError(f.t, err)
	return string(out)
}

func (f *diffFixture) snapshot() map[string]string {
	out := make(map[string]string, len(f.anchors))
	for _, a := range f.anchors {
		out[a] = f.rows(a)
	}
	return out
}

// changedAnchors is the ground truth: the anchors whose projected row genuinely
// differs across the mutation.
func changedAnchors(before, after map[string]string) []string {
	var changed []string
	for a, pre := range before {
		if after[a] != pre {
			changed = append(changed, a)
		}
	}
	sort.Strings(changed)
	return changed
}

// requireSuperset is the assertion the whole file exists for. A declined
// derivation is not a failure — it is the fallback the design mandates, and the
// caller keeps the BFS — but it IS recorded, because a test where every case
// declines proves nothing and would look identical to a passing one.
func requireSuperset(t *testing.T, label string, derived []string, ok bool, changed []string) (answered bool) {
	t.Helper()
	if !ok {
		t.Logf("%s: derivation declined — the caller falls back to the BFS", label)
		return false
	}
	inDerived := make(map[string]struct{}, len(derived))
	for _, a := range derived {
		inDerived[a] = struct{}{}
	}
	var missed []string
	for _, a := range changed {
		if _, has := inDerived[a]; !has {
			missed = append(missed, a)
		}
	}
	require.Empty(t, missed,
		"%s: the derived set MISSED an anchor whose projected row changed — "+
			"on the auth plane that is a grant outliving its revocation.\n"+
			"  changed: %v\n  derived: %v", label, changed, derived)
	return true
}

// rolesDiffFixture is capabilityRoles over three co-holders of one role, plus a
// second role nobody in the first group holds — so a mutation on one role has
// an anchor set that genuinely excludes somebody, and "derived ⊇ changed" is not
// satisfiable by returning everyone.
func rolesDiffFixture(t *testing.T) *diffFixture {
	f := newDiffFixture(t, rolesDataSpec)
	for _, n := range []string{"alice", "bob", "carol", "dave"} {
		f.vertex(n, "identity", map[string]any{"name": n})
	}
	f.vertex("admin", "role", map[string]any{"name": "admin"})
	f.vertex("auditor", "role", map[string]any{"name": "auditor"})
	f.vertex("perm1", "permission", map[string]any{"name": "p1"})
	f.vertex("perm2", "permission", map[string]any{"name": "p2"})

	f.applyLink("holdsRole", "alice", "admin", false)
	f.applyLink("holdsRole", "bob", "admin", false)
	f.applyLink("holdsRole", "carol", "admin", false)
	f.applyLink("holdsRole", "dave", "auditor", false)
	f.applyLink("grantedBy", "perm1", "admin", false)
	f.applyLink("grantedBy", "perm2", "auditor", false)
	return f
}

// rolesDataSpec is capabilityRoles' pattern projecting the role's and
// permission's own DATA, not only their keys. The distinction is load-bearing
// for a differential test: with keys alone, a property mutation moves no
// projected row at all, `changedAnchors` is empty, and the superset assertion
// passes over an empty ground truth no matter what the derivation returns. The
// node-seeded arm can only be proved against a lens that reads what a node
// event actually changes.
const rolesDataSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:grantedBy]-(perm:permission)
RETURN identity.key AS actorKey, role.key AS r, role.data.name AS rn,
       perm.key AS p, perm.data.name AS pn
`

// TestDerivation_Differential_CapabilityRoles walks capabilityRoles through the
// mutation vocabulary the auth plane actually produces — a grant, a revocation,
// a permission attached to a role, a permission detached — and requires the
// derived set to cover every anchor the recompute says changed.
func TestDerivation_Differential_CapabilityRoles(t *testing.T) {
	f := rolesDiffFixture(t)
	ctx := context.Background()
	rs := f.p.ruleState()

	cases := []struct {
		label   string
		rel     string
		from    string
		to      string
		deleted bool
	}{
		{"grant admin to dave", "holdsRole", "dave", "admin", false},
		{"revoke admin from alice", "holdsRole", "alice", "admin", true},
		{"grant perm2 to admin", "grantedBy", "perm2", "admin", false},
		{"detach perm1 from admin", "grantedBy", "perm1", "admin", true},
		{"revoke auditor from dave", "holdsRole", "dave", "auditor", true},
	}

	answered := 0
	for _, tc := range cases {
		before := f.snapshot()
		linkKey := f.applyLink(tc.rel, tc.from, tc.to, tc.deleted)
		after := f.snapshot()
		changed := changedAnchors(before, after)

		derived, ok, err := f.p.deriveAnchorsForLink(ctx, rs, linkKey)
		require.NoError(t, err)
		if requireSuperset(t, tc.label, derived, ok, changed) {
			answered++
		}
		// The superset assertion is only worth anything against a non-empty
		// ground truth: a mutation nobody's row noticed satisfies it vacuously.
		// Applied to the revocations too — they move rows exactly as the grants
		// do, and exempting them was how the guard came to cover three of five.
		require.NotEmpty(t, changed, "%s: fixture defect — no anchor's row moved", tc.label)
	}
	require.Equal(t, len(cases), answered,
		"capabilityRoles must be derivable on every case; a declining index would make this file vacuous")
}

// TestDerivation_Differential_LinkNarrowsBelowTheBFS is the same comparison with
// the win stated: on a co-holder grant the derived set is a STRICT subset of
// what the enumerator returns, and still a superset of what actually changed.
// Without this, a derivation that returned every identity every time would pass
// the superset assertion above.
func TestDerivation_Differential_LinkNarrowsBelowTheBFS(t *testing.T) {
	f := rolesDiffFixture(t)
	ctx := context.Background()
	rs := f.p.ruleState()

	before := f.snapshot()
	linkKey := f.applyLink("holdsRole", "dave", "admin", false)
	after := f.snapshot()
	changed := changedAnchors(before, after)
	require.Equal(t, []string{f.key("dave")}, changed,
		"only the newly-granted identity's row can move")

	derived, ok, err := f.p.deriveAnchorsForLink(ctx, rs, linkKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, changed, derived)

	bfs, err := f.p.actorEnumerator.Enumerate(ctx, f.key("admin"), "role")
	require.NoError(t, err)
	require.Greater(t, len(bfs), len(derived),
		"the BFS from the role endpoint reaches every co-holder; the whole increment is that the derivation does not")
}

// TestDerivation_Differential_VertexAndAspect covers the node-seeded arm (3b):
// a property change on a role, and on a permission two hops out, each with the
// recompute as ground truth. The aspect arm derives through its parent vertex,
// so it is exercised by the same mutation addressed by an aspect key.
func TestDerivation_Differential_VertexAndAspect(t *testing.T) {
	f := rolesDiffFixture(t)
	ctx := context.Background()
	rs := f.p.ruleState()

	for _, tc := range []struct {
		label string
		name  string
		vtype string
		data  map[string]any
	}{
		{"rename the role every co-holder holds", "admin", "role", map[string]any{"name": "admin-renamed"}},
		{"rename a permission two hops out", "perm1", "permission", map[string]any{"name": "p1-renamed"}},
		{"rename a role only dave holds", "auditor", "role", map[string]any{"name": "auditor-renamed"}},
	} {
		before := f.snapshot()
		key := f.key(tc.name)
		f.writeVertex(key, tc.vtype, tc.data)
		after := f.snapshot()
		changed := changedAnchors(before, after)

		require.NotEmpty(t, changed,
			"%s: fixture defect — no anchor's row moved, so the superset assertion below would be vacuous", tc.label)

		derived, ok, err := f.p.deriveAnchorsForVertex(ctx, rs, key, tc.vtype)
		require.NoError(t, err)
		require.True(t, requireSuperset(t, tc.label, derived, ok, changed),
			"%s: a declining derivation here would make this case prove nothing", tc.label)

		// The aspect arm reaches the same conclusion from an aspect key on the
		// same parent, which is what makes it the same proof rather than a
		// second one that happens to agree.
		aspectDerived, aspectOK, err := f.p.deriveAnchorsForAspect(ctx, rs, key+".detail")
		require.NoError(t, err)
		require.Equal(t, ok, aspectOK)
		require.ElementsMatch(t, derived, aspectDerived,
			"%s: the aspect arm must derive through its parent vertex, not around it", tc.label)
	}
}

// ephemeralDiffSpec is the shipped capabilityEphemeral pattern
// (packages/orchestration-base), reduced to the two branches that bind: the
// direct assignment and the role-queue fan-out. It is the lens §4.7's 3b names
// as the reason the derivation exists — broad-filtered and non-exhaustive, so
// Increments 1 and 2 cannot narrow it at all, and its targets are UNLABELED
// positions that only a node-seeded walk can reach.
const ephemeralDiffSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.expiresAt > $now
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(task3:task)
  WHERE task3.data.expiresAt > $now
OPTIONAL MATCH (task3)-[:scopedTo]->(tgt3)
RETURN identity.key AS actorKey,
  collect(DISTINCT {taskKey: task.key, target: tgt.key, ref: tgt.data.ref}) AS direct,
  collect(DISTINCT {taskKey: task3.key, target: tgt3.key, ref: tgt3.data.ref}) AS queued
`

// TestDerivation_Differential_CapabilityEphemeral is the case the increment is
// FOR. capabilityEphemeral binds an unlabeled `tgt` at two positions, so an
// event on a booking — a type the lens's label set never names — still reaches
// it, and the derivation has to find the anchors through the typed chains
// rather than by knowing the type.
func TestDerivation_Differential_CapabilityEphemeral(t *testing.T) {
	f := newDiffFixture(t, ephemeralDiffSpec)
	ctx := context.Background()

	for _, n := range []string{"alice", "bob", "carol"} {
		f.vertex(n, "identity", map[string]any{"name": n})
	}
	f.vertex("ops", "role", map[string]any{"name": "ops"})
	live := map[string]any{"expiresAt": "2030-01-01T00:00:00Z"}
	f.vertex("t1", "task", live)
	f.vertex("t2", "task", live)
	f.vertex("bk1", "booking", map[string]any{"ref": "b1"})
	f.vertex("bk2", "booking", map[string]any{"ref": "b2"})

	f.applyLink("holdsRole", "alice", "ops", false)
	f.applyLink("holdsRole", "bob", "ops", false)
	f.applyLink("assignedTo", "t1", "carol", false)
	f.applyLink("scopedTo", "t1", "bk1", false)
	f.applyLink("queuedFor", "t2", "ops", false)
	f.applyLink("scopedTo", "t2", "bk2", false)

	rs := f.p.ruleState()
	require.True(t, rs.anchorHops.Complete,
		"capabilityEphemeral must be derivable: %s", rs.anchorHops.Incomplete)

	// A vertex event on an UNLABELED pattern position, two chains deep.
	before := f.snapshot()
	f.writeVertex(f.key("bk1"), "booking", map[string]any{"ref": "b1-renamed"})
	after := f.snapshot()
	changed := changedAnchors(before, after)
	require.NotEmpty(t, changed,
		"the scoped booking's data must reach a row, or the unlabeled-position case proves nothing")
	derived, ok, err := f.p.deriveAnchorsForVertex(ctx, rs, f.key("bk1"), "booking")
	require.NoError(t, err)
	require.True(t, requireSuperset(t, "rename a directly-scoped booking", derived, ok, changed))

	// A claim: the queued task is assigned, so it leaves the role queue. Both
	// halves are separate events and each must cover its own change.
	before = f.snapshot()
	claimKey := f.applyLink("assignedTo", "t2", "alice", false)
	after = f.snapshot()
	derived, ok, err = f.p.deriveAnchorsForLink(ctx, rs, claimKey)
	require.NoError(t, err)
	require.True(t, requireSuperset(t, "claim the queued task", derived, ok, changedAnchors(before, after)))

	before = f.snapshot()
	dequeueKey := f.applyLink("queuedFor", "t2", "ops", true)
	after = f.snapshot()
	changed = changedAnchors(before, after)
	require.NotEmpty(t, changed, "dequeuing must move at least the other holder's row")
	derived, ok, err = f.p.deriveAnchorsForLink(ctx, rs, dequeueKey)
	require.NoError(t, err)
	require.True(t, requireSuperset(t, "dequeue the claimed task", derived, ok, changed))
}

// TestDerivation_Differential_ExpiredTaskIsNotTheDerivationsProblem pins the
// boundary §4.4 draws. A task expiring changes rows with NO event at all, so no
// derivation could name the affected anchors — that plane is the freshness
// marker's and the sweep's. The test exists so a future reader does not mistake
// the absence of a case here for an oversight.
func TestDerivation_Differential_ExpiredTaskIsNotTheDerivationsProblem(t *testing.T) {
	f := newDiffFixture(t, ephemeralDiffSpec)
	f.vertex("alice", "identity", map[string]any{"name": "alice"})
	f.vertex("t1", "task", map[string]any{"expiresAt": "2030-01-01T00:00:00Z"})
	f.applyLink("assignedTo", "t1", "alice", false)

	before := f.snapshot()
	// Advance wall-clock past the task's expiry. No CDC event accompanies this.
	f.now = time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	after := f.snapshot()

	require.NotEmpty(t, changedAnchors(before, after),
		"the row really does change when $now passes expiresAt")
	require.True(t, f.p.ruleState().anchorHops.Complete,
		"and the index is complete — the derivation is sound, it simply is not asked, "+
			"because there is no event to ask it about (§4.4: freshness plane, not this mechanism)")
}
