package pipeline

// The multi-walk half of the superset proof — R2's acceptance for the per-branch
// union (personal-lens-derivation-licence-design.md §4.5, §9 R2, §10).
//
// anchor_derivation_differential_test.go proves the property for a single-walk
// lens: for every mutation, every anchor whose PROJECTED ROW actually changes
// appears in the derived set, with the recompute — never the ActorEnumerator BFS
// — as ground truth. This file asks the same question of the shape the union
// introduced, and asks it of the REAL corpus rather than of a fixture cypher:
// the three multi-walk personal lenses, their branches composed by pkgmgr from
// the shipped Walks exactly as activation composes them.
//
// It runs the comparison TWICE over each mutation, because a union has two ways
// to be wrong and one of them is invisible to the other test:
//
//   - PER BRANCH — each branch's own walk against the rows that branch alone
//     projects. A branch seeded at the wrong pattern position fails here.
//   - UNIONED — the lens's walk against the rows the MERGE projects
//     (executeBranches, the path a personal lens really runs per actor). A union
//     that dropped a branch fails here and nowhere else.
//
// And it drives the arm the lens's ProjectionKind selects: the three are
// Personal nats_subject lenses, so their derivation is the actor-aware one
// (deriveAnchorsFor{Vertex,Aspect,Link} behind affectedAnchors), seeded from an
// ActorEnumerator over the personal plane's actor type. The corpus census pins
// that every branch of these lenses anchors on that same type
// (personal_derivation_corpus_census_test.go, indexReadyForPersonal); this file
// is what proves the walk under it reaches everything.
//
// The boundary the union rests on is stated in deriveAnchorsForLink and pinned
// by the by-branch case below: AnchorSideSeeds is exact only for the pattern
// positions the changed link BINDS, so a branch whose pattern never mentions the
// link seeds nothing — which is a real answer only because every branch that
// does mention it is walked in the same pass and unioned in.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// corpusBranchSpecs returns the composed branch cyphers the installed corpus
// ships for one multi-walk lens, read through the same ExpandReadGrantWalks the
// installer runs. Read from the registry rather than restated, so a lens edited
// into a shape the union cannot serve fails here.
func corpusBranchSpecs(t *testing.T, canonicalName string) []string {
	t.Helper()
	for _, def := range pkgregistry.All() {
		expanded, err := def.ExpandReadGrantWalks()
		require.NoError(t, err)
		for _, l := range expanded.Lenses {
			if l.CanonicalName != canonicalName {
				continue
			}
			require.Greaterf(t, len(l.SpecBranches), 1,
				"%s must still compile to several walks — that is the population this file covers", canonicalName)
			require.Truef(t, l.Personal, "%s must still be a Personal lens", canonicalName)
			return l.SpecBranches
		}
	}
	require.FailNowf(t, "lens not installed", "the corpus must declare %s", canonicalName)
	return nil
}

// newBranchDiffFixture installs the branches through the SAME installer
// cmd/refractor uses, so the per-branch graphs under test are the ones
// ruleinstall.go really publishes rather than ones the test assembled.
func newBranchDiffFixture(t *testing.T, specs []string) *diffFixture {
	t.Helper()
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	branches := make([]ruleengine.CompiledRule, 0, len(specs))
	for _, spec := range specs {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		branches = append(branches, cr)
	}
	p := &Pipeline{
		ruleID:       "branch-differential",
		coreKVBucket: "CORE",
		coreKV:       coreKV,
		adjKV:        adjKV,
		engineKind:   ruleengine.EngineFull,
	}
	require.NoError(t, p.UseFullEngineBranches(eng, branches[0], branches))
	// The personal plane's actor type. Its tie to what the host installs is the
	// corpus census's (indexReadyForPersonal reads projection.PersonalActorType);
	// package pipeline cannot import projection, which imports it.
	p.SetActorEnumerator(NewActorEnumerator(adjKV, coreKV, "identity"))

	return &diffFixture{
		t: t, coreKV: coreKV, adjKV: adjKV, p: p, eng: eng,
		cr: branches[0], branches: branches,
		ids:   map[string]string{},
		types: map[string]string{},
		now:   "2020-01-01T00:00:00Z",
	}
}

// singleBranchPipeline is the per-branch half of the comparison: one branch,
// installed alone over the same graph, so its derivation is asked exactly what
// the union asks it and its rows can be recomputed on their own.
func (f *diffFixture) singleBranchPipeline(branch ruleengine.CompiledRule) *Pipeline {
	f.t.Helper()
	p := &Pipeline{
		ruleID:       "branch-differential-single",
		coreKVBucket: "CORE",
		coreKV:       f.coreKV,
		adjKV:        f.adjKV,
		engineKind:   ruleengine.EngineFull,
	}
	require.NoError(f.t, p.UseFullEngineBranches(f.eng, branch, nil))
	p.SetActorEnumerator(NewActorEnumerator(f.adjKV, f.coreKV, "identity"))
	return p
}

// branchRows renders one BRANCH's row set for one anchor, the way rows() renders
// the merged set.
func (f *diffFixture) branchRows(branch ruleengine.CompiledRule, anchor string) string {
	f.t.Helper()
	results, err := f.eng.ExecuteWith(context.Background(), branch, ruleengine.EventContext{
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

func (f *diffFixture) branchSnapshot(branch ruleengine.CompiledRule) map[string]string {
	out := make(map[string]string, len(f.anchors))
	for _, a := range f.anchors {
		out[a] = f.branchRows(branch, a)
	}
	return out
}

// aspect writes an aspect document under its owner vertex, so a mutation of what
// an aspect-valued RETURN column reads can be addressed by an ASPECT key — the
// third seeding arm, and the only one several of these lenses' value columns can
// be moved through at all.
func (f *diffFixture) aspect(ownerKey, local, class string, data map[string]any) string {
	f.t.Helper()
	key := ownerKey + "." + local
	raw, err := json.Marshal(map[string]any{
		"key": key, "class": class, "vertexKey": ownerKey, "localName": local,
		"isDeleted": false, "data": data,
	})
	require.NoError(f.t, err)
	_, err = f.coreKV.Put(context.Background(), key, raw)
	require.NoError(f.t, err)
	return key
}

// tombstoneVertex soft-deletes a vertex the way a Contract #1 delete lands in
// Core KV: the document stays, `isDeleted` flips. It is a distinct event shape
// from a data rewrite — the row it removes is removed for every anchor that
// could read it — and the derivation is seeded from it exactly as it is from any
// other vertex event.
func (f *diffFixture) tombstoneVertex(name, vtype string) string {
	f.t.Helper()
	key := f.key(name)
	body := map[string]any{
		"key": key, "class": vtype, "isDeleted": true,
		"createdAt": "2019-01-01T00:00:00Z", "lastModifiedAt": "2019-01-01T00:00:00Z",
		"data": map[string]any{},
	}
	raw, err := json.Marshal(body)
	require.NoError(f.t, err)
	_, err = f.coreKV.Put(context.Background(), key, raw)
	require.NoError(f.t, err)
	return key
}

// seedMultiWalkGraph builds one graph serving all three lenses' walks: the
// residence/containment chain, the role-and-permission chain, the task chains
// and the studio/instructor chains. Four identities, each reachable through a
// DIFFERENT subset of the walks, so "derived ⊇ changed" is not satisfiable by
// returning everybody.
func seedMultiWalkGraph(f *diffFixture) {
	for _, n := range []string{"alice", "bob", "carol", "dave"} {
		f.vertex(n, "identity", map[string]any{"name": n})
	}
	f.vertex("home1", "unit", map[string]any{"name": "home1"})
	f.vertex("home2", "unit", map[string]any{"name": "home2"})
	f.vertex("site1", "site", map[string]any{"name": "site1"})
	f.vertex("svc1", "service", map[string]any{"name": "svc1"})
	f.vertex("svc2", "service", map[string]any{"name": "svc2"})
	f.vertex("op1", "meta", map[string]any{"operationType": "op1"})
	f.vertex("op2", "meta", map[string]any{"operationType": "op2"})
	f.vertex("op3", "meta", map[string]any{"operationType": "op3"})
	f.vertex("op4", "meta", map[string]any{"operationType": "op4"})
	f.vertex("role1", "role", map[string]any{"name": "role1"})
	f.vertex("perm1", "permission", map[string]any{"name": "perm1"})
	f.vertex("perm2", "permission", map[string]any{"name": "perm2"})
	open := map[string]any{"status": "open", "expiresAt": "2030-01-01T00:00:00Z"}
	f.vertex("task1", "task", open)
	f.vertex("task2", "task", open)
	f.vertex("task3", "task", open)
	f.vertex("bk1", "booking", map[string]any{"ref": "bk1"})
	f.vertex("unit1", "unit", map[string]any{"name": "unit1"})
	f.vertex("studio1", "studio", map[string]any{"name": "studio1"})
	f.vertex("studio2", "studio", map[string]any{"name": "studio2"})
	f.vertex("sess1", "session", map[string]any{"name": "sess1"})
	f.vertex("sess2", "session", map[string]any{"name": "sess2"})
	f.vertex("sess3", "session", map[string]any{"name": "sess3"})
	f.vertex("instr1", "instructor", map[string]any{"name": "instr1"})

	// Residence + containment: alice and bob reach site1, carol and dave do not.
	f.applyLink("residesIn", "alice", "home1", false)
	f.applyLink("residesIn", "bob", "home2", false)
	f.applyLink("containedIn", "home1", "site1", false)
	f.applyLink("containedIn", "home2", "site1", false)

	// edgeCatalog walk 0: a template available at the container, permitting an op.
	f.applyLink("availableAt", "svc1", "site1", false)
	f.applyLink("permitsOperation", "svc1", "op1", false)

	// edgeCatalog walk 1 / edgeTasks walk 1: the role chain. carol holds the role
	// and reaches NOTHING through residence, which is what makes her the witness
	// that the two walks are separately load-bearing.
	f.applyLink("holdsRole", "alice", "role1", false)
	f.applyLink("holdsRole", "carol", "role1", false)
	f.applyLink("grantedBy", "perm1", "role1", false)
	f.applyLink("forOperation", "perm1", "op2", false)

	// edgeCatalog walk 2 / edgeTasks walk 0: the assigned-task chain.
	f.applyLink("assignedTo", "task1", "alice", false)
	f.applyLink("forOperation", "task1", "op3", false)
	f.applyLink("scopedTo", "task1", "bk1", false)
	f.applyLink("appliesToUnit", "bk1", "unit1", false)

	// edgeTasks walk 1: a task queued for the role.
	f.applyLink("queuedFor", "task2", "role1", false)
	f.applyLink("forOperation", "task2", "op3", false)

	// edgeEntitySessions walk 0: a studio at the container, with a session.
	f.applyLink("locatedAt", "studio1", "site1", false)
	f.applyLink("atStudio", "sess1", "studio1", false)
	f.applyLink("ledBy", "sess1", "instr1", false)
	f.aspect(f.key("sess1"), "schedule", "sessionSchedule", map[string]any{
		"name": "morning flow", "startsAt": "2026-01-01T09:00:00Z",
	})

	// edgeEntitySessions walk 1: dave is the instructor, and reaches sessions
	// through no container at all.
	f.applyLink("identifiedBy", "instr1", "dave", false)
}

// branchMutation is one event, and which of the two comparisons it is expected
// to be non-vacuous for.
type branchMutation struct {
	label string
	// quiet declares that this event moves NO merged row — a real answer for a
	// link neither endpoint of which is yet reachable from any identity, and one
	// the derivation must still cover. Declared per case rather than counted,
	// because a superset assertion over an empty ground truth passes whatever
	// the derivation returns: a case that silently goes quiet would stop
	// proving anything and nothing would say so.
	quiet bool
	// apply performs the mutation and returns the key the derivation is seeded
	// with, plus the seeding arm to use.
	apply func(f *diffFixture) (key string, arm derivationArm)
}

type derivationArm int

const (
	armLink derivationArm = iota
	armVertex
	armAspect
)

func (f *diffFixture) derive(p *Pipeline, key string, arm derivationArm, vertexType string) ([]string, bool, error) {
	rs := p.ruleState()
	switch arm {
	case armLink:
		return p.deriveAnchorsForLink(context.Background(), rs, key)
	case armAspect:
		return p.deriveAnchorsForAspect(context.Background(), rs, key)
	default:
		return p.deriveAnchorsForVertex(context.Background(), rs, key, vertexType)
	}
}

// TestDerivation_Differential_MultiWalkCorpus is the acceptance. For every
// mutation, on every one of the three shipped multi-walk personal lenses: the
// derived set covers every anchor whose merged row set moved, and each branch's
// own derived set covers every anchor whose branch rows moved.
func TestDerivation_Differential_MultiWalkCorpus(t *testing.T) {
	for _, tc := range []struct {
		lens      string
		mutations []branchMutation
	}{
		{
			lens: "edgeCatalog",
			mutations: []branchMutation{
				{"a second template starts permitting an already-visible operation", false, func(f *diffFixture) (string, derivationArm) {
					// One event per case, deliberately: the RETURN carries a
					// pattern comprehension over `permitsOperation`, so this
					// moves the rows of every identity that reaches op2 through
					// ANY walk — the case that catches a union seeded from one
					// branch's positions only.
					return f.applyLink("permitsOperation", "svc2", "op2", false), armLink
				}},
				{"a template becomes available at the container", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("availableAt", "svc2", "site1", false), armLink
				}},
				{"a template stops permitting an operation", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("permitsOperation", "svc1", "op1", true), armLink
				}},
				{"a permission is bound to an operation it does not yet reach", true, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("forOperation", "perm2", "op3", false), armLink
				}},
				{"a permission is granted by the held role", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("grantedBy", "perm2", "role1", false), armLink
				}},
				{"a LIVE permission is bound to a second operation", false, func(f *diffFixture) (string, derivationArm) {
					// The same edge type as the quiet case above, at the far end
					// of a chain that is now reachable: the posture of an event
					// is a property of the graph around it, not of the relation,
					// so both postures have to be exercised or the case only
					// ever proves the vacuous one.
					return f.applyLink("forOperation", "perm2", "op4", false), armLink
				}},
				{"a role is revoked", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("holdsRole", "carol", "role1", true), armLink
				}},
				{"a task is bound to an operation while assigned to nobody", true, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("forOperation", "task3", "op1", false), armLink
				}},
				{"a task is assigned", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("assignedTo", "task3", "bob", false), armLink
				}},
				{"an ASSIGNED task is bound to a second operation", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("forOperation", "task3", "op4", false), armLink
				}},
				{"a residence leaves the containment chain", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("containedIn", "home2", "site1", true), armLink
				}},
				{"an op meta is renamed", false, func(f *diffFixture) (string, derivationArm) {
					key := f.key("op2")
					f.writeVertex(key, "meta", map[string]any{"operationType": "op2-renamed"})
					return key, armVertex
				}},
				{"an op meta is tombstoned", false, func(f *diffFixture) (string, derivationArm) {
					return f.tombstoneVertex("op2", "meta"), armVertex
				}},
			},
		},
		{
			lens: "edgeTasks",
			mutations: []branchMutation{
				{"a task is assigned", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("assignedTo", "task3", "bob", false), armLink
				}},
				{"an ASSIGNED task is bound to an operation", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("forOperation", "task3", "op2", false), armLink
				}},
				{"a task is unassigned", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("assignedTo", "task1", "alice", true), armLink
				}},
				{"an UNREACHABLE task is bound to an operation", true, func(f *diffFixture) (string, derivationArm) {
					// The same edge, the same relation, the opposite posture:
					// task1 is assigned to nobody and queued nowhere, so nothing
					// reads it and no merged row moves.
					return f.applyLink("forOperation", "task1", "op2", false), armLink
				}},
				{"a task is queued for the held role", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("queuedFor", "task3", "role1", false), armLink
				}},
				{"a queued task's scope moves", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("scopedTo", "task2", "bk1", false), armLink
				}},
				{"a queued task leaves the queue", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("queuedFor", "task2", "role1", true), armLink
				}},
				{"a task closes", false, func(f *diffFixture) (string, derivationArm) {
					key := f.key("task3")
					f.writeVertex(key, "task", map[string]any{"status": "closed", "expiresAt": "2030-01-01T00:00:00Z"})
					return key, armVertex
				}},
			},
		},
		{
			lens: "edgeEntitySessions",
			mutations: []branchMutation{
				{"a session is scheduled at the studio", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("atStudio", "sess2", "studio1", false), armLink
				}},
				{"a session is scheduled at a studio outside the chain", true, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("atStudio", "sess3", "studio2", false), armLink
				}},
				{"a second studio opens at the container", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("locatedAt", "studio2", "site1", false), armLink
				}},
				{"an instructor binding is withdrawn", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("identifiedBy", "instr1", "dave", true), armLink
				}},
				{"an instructor stops leading a session", false, func(f *diffFixture) (string, derivationArm) {
					return f.applyLink("ledBy", "sess1", "instr1", true), armLink
				}},
				{"a session is renamed through its schedule aspect", false, func(f *diffFixture) (string, derivationArm) {
					return f.aspect(f.key("sess1"), "schedule", "sessionSchedule", map[string]any{
						"name": "evening flow", "startsAt": "2026-01-01T18:00:00Z",
					}), armAspect
				}},
				{"a session is tombstoned", false, func(f *diffFixture) (string, derivationArm) {
					return f.tombstoneVertex("sess2", "session"), armVertex
				}},
			},
		},
	} {
		t.Run(tc.lens, func(t *testing.T) {
			f := newBranchDiffFixture(t, corpusBranchSpecs(t, tc.lens))
			seedMultiWalkGraph(f)

			rs := f.p.ruleState()
			require.Empty(t, rs.anchorHopsPerBranchRefusal,
				"%s must resolve a pattern graph for every walk — that is what this increment armed", tc.lens)
			require.Len(t, rs.anchorHopsPerBranch, len(f.branches))
			idxs, ready := f.p.derivationIndexes(rs)
			require.True(t, ready, "%s's per-branch index set must be usable", tc.lens)
			require.Len(t, idxs, len(f.branches))

			singles := make([]*Pipeline, len(f.branches))
			for i, b := range f.branches {
				singles[i] = f.singleBranchPipeline(b)
			}

			answered := 0
			for _, m := range tc.mutations {
				before := f.snapshot()
				branchBefore := make([]map[string]string, len(f.branches))
				for i, b := range f.branches {
					branchBefore[i] = f.branchSnapshot(b)
				}

				key, arm := m.apply(f)
				vertexType := ""
				if arm == armVertex {
					vt, _, ok := substrate.ParseVertexKey(key)
					require.True(t, ok)
					vertexType = vt
				}

				after := f.snapshot()
				changed := changedAnchors(before, after)

				derived, ok, err := f.derive(f.p, key, arm, vertexType)
				require.NoError(t, err)
				require.Truef(t, ok,
					"%s / %s: the union declined — a multi-walk lens whose branches all index must derive, or the increment delivers nothing",
					tc.lens, m.label)
				require.True(t, requireSuperset(t, tc.lens+" / "+m.label, derived, ok, changed))
				answered++
				require.Equalf(t, m.quiet, len(changed) == 0,
					"%s / %s: this case's declared row-moving posture is wrong — a superset assertion over an empty ground truth proves nothing",
					tc.lens, m.label)

				// Per branch, against that branch's own rows — and the union has
				// to be exactly the branches' union, which is what fails if a
				// branch is silently dropped from the walk.
				perBranch := map[string]struct{}{}
				for i, b := range f.branches {
					branchAfter := f.branchSnapshot(b)
					branchChanged := changedAnchors(branchBefore[i], branchAfter)
					bDerived, bOK, err := f.derive(singles[i], key, arm, vertexType)
					require.NoError(t, err)
					require.Truef(t, bOK, "%s / %s: walk %d declined on its own", tc.lens, m.label, i)
					require.True(t, requireSuperset(t,
						fmt.Sprintf("%s / %s / walk %d", tc.lens, m.label, i), bDerived, bOK, branchChanged))
					for _, a := range bDerived {
						perBranch[a] = struct{}{}
					}
				}
				inUnion := map[string]struct{}{}
				for _, a := range derived {
					inUnion[a] = struct{}{}
				}
				require.Equalf(t, sortedKeys(perBranch), sortedKeys(inUnion),
					"%s / %s: the lens's derived set must be exactly the union of its branches' — a missing branch is an anchor never reprojected",
					tc.lens, m.label)
			}
			require.Equal(t, len(tc.mutations), answered)
		})
	}
}

// TestDerivation_Differential_MultiWalkNarrowsBelowTheBFS is the win stated, and
// what stops the superset assertions above from being satisfiable by returning
// every identity. A role revocation reaches exactly the identity that held it,
// while the enumerator's walk from the role endpoint reaches every co-holder.
func TestDerivation_Differential_MultiWalkNarrowsBelowTheBFS(t *testing.T) {
	f := newBranchDiffFixture(t, corpusBranchSpecs(t, "edgeCatalog"))
	seedMultiWalkGraph(f)
	ctx := context.Background()
	rs := f.p.ruleState()

	before := f.snapshot()
	linkKey := f.applyLink("holdsRole", "dave", "role1", false)
	after := f.snapshot()
	changed := changedAnchors(before, after)
	require.Equal(t, []string{f.key("dave")}, changed,
		"only the newly-granted identity's rows can move through the role walk")

	derived, ok, err := f.p.deriveAnchorsForLink(ctx, rs, linkKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, changed, derived)

	bfs, err := f.p.actorEnumerator.Enumerate(ctx, f.key("role1"), "role")
	require.NoError(t, err)
	require.Greaterf(t, len(bfs), len(derived),
		"the enumerator reaches every co-holder of the role; the whole increment is that the union does not")
}

// TestBranchUnion_MergeRestsOnEveryBranchRerunPerActor pins the reliance the
// union's soundness actually rests on.
//
// mergeBranchRows can make a row depend on a SIBLING branch: a key several
// branches produce is merged column by column, so what a branch contributes can
// change what another branch's key renders. The union would therefore
// under-approximate if the pipeline evaluated one branch per derived actor —
// deriving an actor through walk 0 and re-running walk 0 alone would leave walk
// 1's contribution to that actor's row unrecomputed.
//
// It does not: executeBranches re-runs EVERY branch for every actor it is called
// with. That is the property, asserted here by name so a future change to the
// execution path fails in front of the union rather than behind it.
func TestBranchUnion_MergeRestsOnEveryBranchRerunPerActor(t *testing.T) {
	f := newBranchDiffFixture(t, corpusBranchSpecs(t, "edgeCatalog"))
	seedMultiWalkGraph(f)
	rs := f.p.ruleState()
	require.Greater(t, len(rs.branches), 1, "precondition: a genuinely multi-walk rule")

	// alice reaches op1 through walk 0 (residence → template) and op2 through
	// walk 1 (role → permission). One execution for her must carry both, which
	// is only true if every branch ran.
	merged := f.rows(f.key("alice"))
	require.Contains(t, merged, f.ids["op1"], "walk 0's anchor must be in the merged row set")
	require.Contains(t, merged, f.ids["op2"], "walk 1's anchor must be in the merged row set")

	// And the same execution for an identity reachable through walk 1 alone
	// carries walk 1's anchor, so the merge is not simply walk 0's answer.
	require.Contains(t, f.rows(f.key("carol")), f.ids["op2"])
	require.NotContains(t, f.rows(f.key("carol")), f.ids["op1"],
		"carol reaches nothing through the residence walk — otherwise the narrowing assertions are vacuous")
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
