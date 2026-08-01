// Security-critical equivalence proof for the read-grant producer staging
// primitive (internal/pkgmgr/anchorwalk.go's generateProducerSpec): a domain's
// member walks each get their own STAGE — the walk's own OPTIONAL MATCH
// clauses folded into a `collect(DISTINCT …) AS grantSliceN` by a `WITH`
// before the next walk's clauses run — instead of every walk's clauses
// running in one flat OPTIONAL MATCH sequence, one shared row set.
//
// The two forms must grant the IDENTICAL anchor set for the same corpus
// (collect(DISTINCT …) dedupes each branch's own contribution regardless of
// how many rows fed it, so the cross product the flat form multiplies through
// never changes WHAT is collected — only how many rows are live at once). What
// differs is the peak binding-set size: the flat form's independent branches
// share one row stream, so their fan-outs MULTIPLY, while the staged form's
// peak is the largest SINGLE walk's fan-out. A corpus sized so the flat form's
// product comfortably clears a binding cap the staged form never approaches is
// exactly the shape that put a live edgeManifestReadGrants evaluation over a
// 1,000,001-row cross product on one event.
package full

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

// --- corpus -----------------------------------------------------------------
//
// Modelled on packages/edge-manifest's domainBase (edgeManifestReadGrants)
// walks: a resident's residence chain reaches two containers, each carrying
// service templates (with their meta ops), wellness studios (with their
// sessions), clinic providers, and café menu items; off the identity itself
// hang open tasks, service instances, bookings, and a lease application with
// its open tabs.
//
// Sized so the FLAT form's independent branches, sharing one row stream,
// multiply past 200,000 rows while staying under the engine's default
// 1,000,000-row cap (so TestReadGrantProducer_StagedMatchesFlatAnchorSet can
// run both forms to completion and compare results) — and so the STAGED
// form's peak (the largest single walk's own fan-out) stays in the low tens,
// nowhere near either cap.
const (
	edgeManifestContainerCount        = 2
	edgeManifestTplPerContainer       = 4
	edgeManifestOpPerTpl              = 3
	edgeManifestStudioPerContainer    = 2
	edgeManifestSessionsPerStudio     = 8
	edgeManifestProviderPerContainer  = 3
	edgeManifestMenuItemsPerContainer = 6
	edgeManifestTaskCount             = 3
	edgeManifestInstanceCount         = 2
	edgeManifestBookingCount          = 4
	edgeManifestTabCount              = 2
)

// edgeManifestReadGrantCorpus is the seeded fixture both the equivalence test
// and the cap test share — one corpus, evaluated by both cypher forms.
type edgeManifestReadGrantCorpus struct {
	adjKV, coreKV *substrate.KV
	actorKey      string
}

// seedEdgeManifestReadGrantCorpus builds the corpus described above under a
// single resident identity, returning its own vtx key as actorKey.
func seedEdgeManifestReadGrantCorpus(t *testing.T) edgeManifestReadGrantCorpus {
	t.Helper()
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	putVertex(t, reg, coreKV, "resident", "identity", nil)
	putVertex(t, reg, coreKV, "home", "location", nil)
	putEdge(t, reg, adjKV, "residesIn", "resident", "home")

	containers := make([]string, edgeManifestContainerCount)
	prev := "home"
	for i := 0; i < edgeManifestContainerCount; i++ {
		containers[i] = fmt.Sprintf("container%d", i)
		putVertex(t, reg, coreKV, containers[i], "location", nil)
		putEdge(t, reg, adjKV, "containedIn", prev, containers[i])
		prev = containers[i]
	}

	for _, c := range containers {
		for i := 0; i < edgeManifestTplPerContainer; i++ {
			tpl := fmt.Sprintf("tpl_%s_%d", c, i)
			putVertex(t, reg, coreKV, tpl, "service", nil)
			putEdge(t, reg, adjKV, "availableAt", tpl, c)
			for j := 0; j < edgeManifestOpPerTpl; j++ {
				op := fmt.Sprintf("op_%s_%d_%d", c, i, j)
				putVertex(t, reg, coreKV, op, "meta", nil)
				putEdge(t, reg, adjKV, "permitsOperation", tpl, op)
			}
		}
		for i := 0; i < edgeManifestStudioPerContainer; i++ {
			studio := fmt.Sprintf("studio_%s_%d", c, i)
			putVertex(t, reg, coreKV, studio, "studio", nil)
			putEdge(t, reg, adjKV, "locatedAt", studio, c)
			for j := 0; j < edgeManifestSessionsPerStudio; j++ {
				sess := fmt.Sprintf("sess_%s_%d_%d", c, i, j)
				putVertex(t, reg, coreKV, sess, "session", nil)
				putEdge(t, reg, adjKV, "atStudio", sess, studio)
			}
		}
		for i := 0; i < edgeManifestProviderPerContainer; i++ {
			prov := fmt.Sprintf("prov_%s_%d", c, i)
			putVertex(t, reg, coreKV, prov, "provider", nil)
			putEdge(t, reg, adjKV, "practicesAt", prov, c)
		}
		for i := 0; i < edgeManifestMenuItemsPerContainer; i++ {
			item := fmt.Sprintf("item_%s_%d", c, i)
			putVertex(t, reg, coreKV, item, "menuitem", nil)
			putEdge(t, reg, adjKV, "servedAt", item, c)
		}
	}

	for i := 0; i < edgeManifestTaskCount; i++ {
		task := fmt.Sprintf("task_%d", i)
		putVertex(t, reg, coreKV, task, "task", nil)
		putEdge(t, reg, adjKV, "assignedTo", task, "resident")
	}
	for i := 0; i < edgeManifestInstanceCount; i++ {
		inst := fmt.Sprintf("inst_%d", i)
		putVertex(t, reg, coreKV, inst, "service", nil)
		putEdge(t, reg, adjKV, "providedTo", inst, "resident")
	}
	for i := 0; i < edgeManifestBookingCount; i++ {
		bk := fmt.Sprintf("bk_%d", i)
		putVertex(t, reg, coreKV, bk, "booking", nil)
		putEdge(t, reg, adjKV, "bookedBy", bk, "resident")
	}
	putVertex(t, reg, coreKV, "leaseapp1", "leaseapp", nil)
	putEdge(t, reg, adjKV, "applicationFor", "leaseapp1", "resident")
	for i := 0; i < edgeManifestTabCount; i++ {
		tab := fmt.Sprintf("tab_%d", i)
		putVertex(t, reg, coreKV, tab, "tab", nil)
		putEdge(t, reg, adjKV, "openFor", tab, "leaseapp1")
	}

	return edgeManifestReadGrantCorpus{adjKV: adjKV, coreKV: coreKV, actorKey: vtxKey(reg, "resident")}
}

// --- the FLAT form ------------------------------------------------------
//
// The nine domainBase walks packages/edge-manifest/lenses.go declares
// (edgeServices, edgeCatalog's base branch, edgeTasks' base branch,
// edgeInstances, edgeEntitySessions' base branch, edgeEntityProviders,
// edgeEntityBookings, edgeEntityTabs, edgeEntityMenuItems), emitted the way a
// single flat run of OPTIONAL MATCHes emits them: the shared residence prefix
// bound ONCE (the four walks that open on it — tpl, sess, prov, item — and the
// op walk continuing off tpl all reuse that one binding), then every other
// walk's own clauses, then one concatenated RETURN. This is deliberately NOT
// the output of any current code path — generateProducerSpec always stages —
// it is a pinned literal standing in for what a flat (unstaged) emitter would
// produce for this domain: the form whose cross product this whole primitive
// exists to bound.
const flatEdgeManifestReadGrantsCypher = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
OPTIONAL MATCH (tpl)-[:permitsOperation]->(op:meta)
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:providedTo]-(inst:service)
OPTIONAL MATCH (container)<-[:locatedAt]-(studio:studio)
OPTIONAL MATCH (studio)<-[:atStudio]-(sess:session)
OPTIONAL MATCH (container)<-[:practicesAt]-(prov:provider)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
OPTIONAL MATCH (identity)<-[:applicationFor]-(la:leaseapp)
OPTIONAL MATCH (la)<-[:openFor]-(tab:tab)
OPTIONAL MATCH (container)<-[:servedAt]-(item:menuitem)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorType: 'service', anchorId: nanoIdFromKey(tpl.key), via: ['residesIn', 'containedIn', 'availableAt']}) +
  collect(DISTINCT {anchorType: 'meta', anchorId: nanoIdFromKey(op.key), via: ['residesIn', 'containedIn', 'availableAt', 'permitsOperation']}) +
  collect(DISTINCT {anchorType: 'task', anchorId: nanoIdFromKey(task.key), via: ['assignedTo']}) +
  collect(DISTINCT {anchorType: 'service', anchorId: nanoIdFromKey(inst.key), via: ['providedTo']}) +
  collect(DISTINCT {anchorType: 'session', anchorId: nanoIdFromKey(sess.key), via: ['residesIn', 'containedIn', 'locatedAt', 'atStudio']}) +
  collect(DISTINCT {anchorType: 'provider', anchorId: nanoIdFromKey(prov.key), via: ['residesIn', 'containedIn', 'practicesAt']}) +
  collect(DISTINCT {anchorType: 'booking', anchorId: nanoIdFromKey(bk.key), via: ['bookedBy']}) +
  collect(DISTINCT {anchorType: 'tab', anchorId: nanoIdFromKey(tab.key), via: ['applicationFor', 'openFor']}) +
  collect(DISTINCT {anchorType: 'menuitem', anchorId: nanoIdFromKey(item.key), via: ['residesIn', 'containedIn', 'servedAt']})
  AS readableAnchors
`

// stagedEdgeManifestReadGrantsSpec pulls the real, generated
// edgeManifestReadGrants producer cypher straight from
// packages/edge-manifest's own Definition via the production compiler
// (internal/pkgmgr.ExpandReadGrantWalks) — not a hand-authored stand-in — so
// this test proves the CURRENT generator's actual output, not a pinned
// approximation of it.
func stagedEdgeManifestReadGrantsSpec(t *testing.T) string {
	t.Helper()
	expanded, err := edgemanifest.Package.ExpandReadGrantWalks()
	require.NoError(t, err, "the shipped edge-manifest package must expand cleanly")
	for _, l := range expanded.Lenses {
		if l.CanonicalName == "edgeManifestReadGrants" {
			require.NotEmpty(t, l.Spec, "edgeManifestReadGrants must carry a generated Spec")
			return l.Spec
		}
	}
	t.Fatal("edgeManifestReadGrants not found among the expanded package's lenses")
	return ""
}

// canonicalAnchorSet flattens a producer's single-row readableAnchors column
// into a comparable set of "anchorType|anchorId|via1,via2,…" entries. A
// binding-less branch's placeholder entry (nanoIdFromKey(null) resolves to
// nil — executor.go's nanoIdFromKey case) is dropped, mirroring the realness
// filter every generated producer's own Output descriptor applies.
func canonicalAnchorSet(t *testing.T, results []ruleengine.ProjectionResult) map[string]bool {
	t.Helper()
	require.Len(t, results, 1, "an actorAggregate producer anchored on $actorKey projects exactly one row")
	anchors, _ := results[0].Values["readableAnchors"].([]any)
	out := make(map[string]bool, len(anchors))
	for _, a := range anchors {
		m, ok := a.(map[string]any)
		require.Truef(t, ok, "readableAnchors entry must be a map, got %T (%v)", a, a)
		id, _ := m["anchorId"].(string)
		if id == "" {
			continue
		}
		ty, _ := m["anchorType"].(string)
		via, _ := m["via"].([]any)
		viaParts := make([]string, len(via))
		for i, v := range via {
			viaParts[i], _ = v.(string)
		}
		out[ty+"|"+id+"|"+strings.Join(viaParts, ",")] = true
	}
	return out
}

// diffAnchorSets returns, sorted, the entries present in a but not b and
// present in b but not a.
func diffAnchorSets(a, b map[string]bool) (onlyInA, onlyInB []string) {
	for k := range a {
		if !b[k] {
			onlyInA = append(onlyInA, k)
		}
	}
	for k := range b {
		if !a[k] {
			onlyInB = append(onlyInB, k)
		}
	}
	sort.Strings(onlyInA)
	sort.Strings(onlyInB)
	return onlyInA, onlyInB
}

// TestReadGrantProducer_StagedMatchesFlatAnchorSet is the load-bearing
// security proof: the staged producer must grant EXACTLY the anchor set the
// flat form grants for the identical corpus — no anchor dropped (a silent D1
// read-path regression: a lens row the actor could see now gets its row
// dropped) and none added (an over-grant: the actor can now read something
// they could not before). collect(DISTINCT …) makes the two forms' branch
// contents independent of row-count multiplication, so this equality is
// expected to hold exactly, not approximately.
func TestReadGrantProducer_StagedMatchesFlatAnchorSet(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	corpus := seedEdgeManifestReadGrantCorpus(t)
	stagedSpec := stagedEdgeManifestReadGrantsSpec(t)

	eng := New()
	params := ruleengine.EventContext{Parameters: map[string]any{"actorKey": corpus.actorKey}}

	stagedCR, err := eng.Parse(stagedSpec)
	require.NoError(t, err, "the generated staged producer must parse:\n%s", stagedSpec)
	stagedOut, err := eng.ExecuteWith(context.Background(), stagedCR, params, corpus.adjKV, corpus.coreKV)
	require.NoError(t, err, "the staged producer must execute under the default binding cap")

	flatCR, err := eng.Parse(flatEdgeManifestReadGrantsCypher)
	require.NoError(t, err, "the flat literal must parse")
	flatOut, err := eng.ExecuteWith(context.Background(), flatCR, params, corpus.adjKV, corpus.coreKV)
	require.NoError(t, err, "the flat form must execute under the default 1,000,000-row cap — "+
		"if this fails, the corpus's cross product grew past the cap; shrink the corpus, don't raise the cap")

	stagedSet := canonicalAnchorSet(t, stagedOut)
	flatSet := canonicalAnchorSet(t, flatOut)
	require.NotEmpty(t, stagedSet, "the corpus granted nothing — this proves nothing")

	onlyInFlat, onlyInStaged := diffAnchorSets(flatSet, stagedSet)
	require.Emptyf(t, onlyInFlat,
		"the staged producer DROPPED anchors the flat form granted (a silent D1 read-path regression — "+
			"the actor loses read access to these): %v", onlyInFlat)
	require.Emptyf(t, onlyInStaged,
		"the staged producer GRANTED anchors the flat form never did (an over-grant — "+
			"the actor gains read access to these): %v", onlyInStaged)
}

// TestReadGrantProducer_FlatFormExceedsBindingCapStagedDoesNot is the bound
// proof: under the SAME low cap, the flat form's cross product refuses while
// the staged form — whose peak binding set is only its largest single walk's
// own fan-out — comfortably succeeds. 50,000 sits well above the staged
// form's ~33-row peak (the sess walk: 2 containers × 2 studios × 8 sessions)
// and well below the flat form's ~330,000-row total (2 containers ×
// (4 tpl × 3 op) × 3 task × 2 inst × (2 studio × 8 sess) × 3 prov × 4 booking
// × 2 tab × 6 item).
func TestReadGrantProducer_FlatFormExceedsBindingCapStagedDoesNot(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	corpus := seedEdgeManifestReadGrantCorpus(t)
	stagedSpec := stagedEdgeManifestReadGrantsSpec(t)

	const lowCap = 50_000
	eng := New().WithMaxBindings(lowCap)
	params := ruleengine.EventContext{Parameters: map[string]any{"actorKey": corpus.actorKey}}

	flatCR, err := eng.Parse(flatEdgeManifestReadGrantsCypher)
	require.NoError(t, err)
	_, err = eng.ExecuteWith(context.Background(), flatCR, params, corpus.adjKV, corpus.coreKV)
	require.Error(t, err, "the flat form's cross product must overrun a %d-row cap", lowCap)
	require.Contains(t, err.Error(), "over the cap of",
		"the refusal must be the binding-set cap, not some other error: %v", err)

	stagedCR, err := eng.Parse(stagedSpec)
	require.NoError(t, err)
	stagedOut, err := eng.ExecuteWith(context.Background(), stagedCR, params, corpus.adjKV, corpus.coreKV)
	require.NoError(t, err, "the staged form's peak (its largest single walk) must stay well under the same cap")
	require.NotEmpty(t, canonicalAnchorSet(t, stagedOut))
}

// TestReadGrantProducer_StagingScopesWalkVariablesApart is the runtime proof
// that staging keeps two walks' unrelated bindings from silently joining, with
// no rename step anywhere in the pipeline. Two Personal lenses in one domain
// each declare a Walk that binds the SAME variable name `x` to a DIFFERENT
// anchor type — legal, because that check (internal/pkgmgr/anchorwalk.go's
// parseWalks boundElsewhere guard) only rejects a collision WITHIN one lens's
// own Walks, not across sibling lenses in a domain; two sibling lenses reusing
// a name is exactly the shape the generated producer must keep apart.
//
// Each walk's `x` dies at its own WITH, so the second walk's `x:booking`
// binds fresh rather than being constrained to whatever the first walk's
// `x:task` bound. A single flat, unstaged query sharing one row stream would
// instead treat the second `(x:booking)` as a join constraint on the `x` the
// first clause already bound, silently turning "task OR booking" into "the one
// vertex both walks happen to agree on" — here, empty, since a task is never a
// booking.
func TestReadGrantProducer_StagingScopesWalkVariablesApart(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	corpus := seedEdgeManifestReadGrantCorpus(t)

	def := pkgmgr.Definition{
		Name:             "collision-fixture",
		Version:          "1.0.0",
		ReadGrantDomains: []pkgmgr.ReadGrantDomainSpec{{Name: "fx"}},
		Lenses: []pkgmgr.LensSpec{
			{
				CanonicalName: "taskLens",
				Class:         "meta.lens",
				Adapter:       "nats-subject",
				SubjectPrefix: "lattice.sync.user",
				Stream:        "SYNC",
				Personal:      true,
				Engine:        "full",
				IntoKey:       []string{"__actor", "ns", "entityId"},
				Walks: []pkgmgr.AnchorWalk{{
					GrantDomain: "fx", AnchorType: "task", AnchorVar: "x",
					Chain: []string{"(identity)<-[:assignedTo]-(x:task)"},
				}},
				Spec: "\nRETURN x.key AS anchor\n",
			},
			{
				CanonicalName: "bookingLens",
				Class:         "meta.lens",
				Adapter:       "nats-subject",
				SubjectPrefix: "lattice.sync.user",
				Stream:        "SYNC",
				Personal:      true,
				Engine:        "full",
				IntoKey:       []string{"__actor", "ns", "entityId"},
				Walks: []pkgmgr.AnchorWalk{{
					GrantDomain: "fx", AnchorType: "booking", AnchorVar: "x",
					Chain: []string{"(identity)<-[:bookedBy]-(x:booking)"},
				}},
				Spec: "\nRETURN x.key AS anchor\n",
			},
		},
	}
	expanded, err := def.ExpandReadGrantWalks()
	require.NoError(t, err, "two sibling lenses binding the same variable name to different anchor types must expand cleanly")
	require.Len(t, expanded.Lenses, 3, "2 data lenses + 1 generated producer")
	producer := expanded.Lenses[2]
	require.Equal(t, "fxReadGrants", producer.CanonicalName)

	eng := New()
	cr, err := eng.Parse(producer.Spec)
	require.NoError(t, err, "generated producer:\n%s", producer.Spec)
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": corpus.actorKey}},
		corpus.adjKV, corpus.coreKV)
	require.NoError(t, err)

	set := canonicalAnchorSet(t, out)
	var taskCount, bookingCount int
	for k := range set {
		switch {
		case strings.HasPrefix(k, "task|"):
			taskCount++
		case strings.HasPrefix(k, "booking|"):
			bookingCount++
		}
	}
	require.Equalf(t, edgeManifestTaskCount, taskCount,
		"the second walk's fresh `x:booking` binding must not have joined against the first walk's `x:task` "+
			"binding (a join would starve or corrupt this count): granted set = %v", set)
	require.Equalf(t, edgeManifestBookingCount, bookingCount,
		"the first walk's `x:task` binding must be unaffected by the second walk's `x:booking` binding: "+
			"granted set = %v", set)
}
