// The load-bearing assertion for the grouping-key reduction: no projected row
// moves. The same PARSED rule is executed twice over one corpus — once with the
// analysis as Parse computed it, once with it forced nil (the path every
// evaluation took before it existed) — and the two []ProjectionResult must be
// equal in ORDER and CONTENT. Comparing anchor SETS would hide a reordered
// collect(); comparing the parsed rule against a second parse would leave room
// for the two runs to differ in something other than the reduction.
package full

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// readGrantCorpusShape sizes one actor-rooted corpus across all three
// edge-manifest read-grant domains. Every count may be zero — an empty branch
// is a shape the producer must still handle, and the randomized differential
// draws zeros deliberately.
type readGrantCorpusShape struct {
	Prefix string

	// The base domain hangs off the residence chain (which every one of its
	// four container-rooted walks re-walks: the shared-prefix shape) and off
	// the identity itself.
	Containers, ExtraParents           int
	TplPerContainer, OpPerTpl          int
	StudioPerContainer, SessPerStudio  int
	ProvPerContainer, ItemPerContainer int
	Tasks, Instances, Bookings, Tabs   int

	// The staff domain hangs off held roles and off the workplace chain.
	Roles, PermsPerRole, QueuedPerRole, PanesPerRole int
	WorkPlaces, StudiosPerPlace, WorkOrdersPerPlace  int

	// The provider domain hangs off the identity's provider-hat bindings.
	InstructorSessions, Appointments, ProviderInstances int
}

// fullThreeDomainShape grants in every domain, so a differential over it
// compares non-empty slices rather than three empty ones.
func fullThreeDomainShape(prefix string) readGrantCorpusShape {
	return readGrantCorpusShape{
		Prefix:             prefix,
		Containers:         2,
		ExtraParents:       1,
		TplPerContainer:    2,
		OpPerTpl:           2,
		StudioPerContainer: 2,
		SessPerStudio:      2,
		ProvPerContainer:   2,
		ItemPerContainer:   2,
		Tasks:              3,
		Instances:          2,
		Bookings:           2,
		Tabs:               2,
		Roles:              2,
		PermsPerRole:       2,
		QueuedPerRole:      2,
		PanesPerRole:       2,
		WorkPlaces:         2,
		StudiosPerPlace:    2,
		WorkOrdersPerPlace: 2,
		InstructorSessions: 2,
		Appointments:       2,
		ProviderInstances:  2,
	}
}

// randomCorpusShape draws every count from a deterministic source, zeros
// included — the randomly-empty branches the differential wants.
func randomCorpusShape(prefix string, r *rand.Rand) readGrantCorpusShape {
	n := func(max int) int { return r.Intn(max + 1) }
	return readGrantCorpusShape{
		Prefix:             prefix,
		Containers:         n(3),
		ExtraParents:       n(2),
		TplPerContainer:    n(3),
		OpPerTpl:           n(2),
		StudioPerContainer: n(2),
		SessPerStudio:      n(3),
		ProvPerContainer:   n(2),
		ItemPerContainer:   n(2),
		Tasks:              n(3),
		Instances:          n(2),
		Bookings:           n(3),
		Tabs:               n(2),
		Roles:              n(2),
		PermsPerRole:       n(2),
		QueuedPerRole:      n(2),
		PanesPerRole:       n(2),
		WorkPlaces:         n(2),
		StudiosPerPlace:    n(2),
		WorkOrdersPerPlace: n(2),
		InstructorSessions: n(3),
		Appointments:       n(2),
		ProviderInstances:  n(2),
	}
}

// seedReadGrantCorpus writes one actor-rooted corpus of shape s and returns the
// actor's own vertex key. Every logical name is prefixed, so several corpora
// can share one pair of KVs without any of their walks crossing.
func seedReadGrantCorpus(t testing.TB, reg *fixtureRegistry, adjKV, coreKV *substrate.KV, s readGrantCorpusShape) string {
	t.Helper()
	p := s.Prefix
	name := func(format string, args ...any) string { return p + fmt.Sprintf(format, args...) }

	actor := name("actor")
	putVertex(t, reg, coreKV, actor, "identity", nil)

	// The residence chain, plus the extra parents that make `containedIn*0..`
	// a multi-parent walk rather than a line. `container` binds the home itself
	// at hop zero, so every container-rooted walk shares that prefix.
	home := name("home")
	putVertex(t, reg, coreKV, home, "location", nil)
	putEdge(t, reg, adjKV, "residesIn", actor, home)
	containers := []string{home}
	prev := home
	for i := 0; i < s.Containers; i++ {
		c := name("container%d", i)
		putVertex(t, reg, coreKV, c, "location", nil)
		putEdge(t, reg, adjKV, "containedIn", prev, c)
		prev = c
		containers = append(containers, c)
	}
	for i := 0; i < s.ExtraParents; i++ {
		x := name("parent%d", i)
		putVertex(t, reg, coreKV, x, "location", nil)
		putEdge(t, reg, adjKV, "containedIn", home, x)
		containers = append(containers, x)
	}

	for ci, c := range containers {
		for i := 0; i < s.TplPerContainer; i++ {
			tpl := name("tpl_%d_%d", ci, i)
			putVertex(t, reg, coreKV, tpl, "service", nil)
			putEdge(t, reg, adjKV, "availableAt", tpl, c)
			for j := 0; j < s.OpPerTpl; j++ {
				op := name("op_%d_%d_%d", ci, i, j)
				putVertex(t, reg, coreKV, op, "meta", nil)
				putEdge(t, reg, adjKV, "permitsOperation", tpl, op)
			}
		}
		for i := 0; i < s.StudioPerContainer; i++ {
			studio := name("studio_%d_%d", ci, i)
			putVertex(t, reg, coreKV, studio, "studio", nil)
			putEdge(t, reg, adjKV, "locatedAt", studio, c)
			for j := 0; j < s.SessPerStudio; j++ {
				sess := name("sess_%d_%d_%d", ci, i, j)
				putVertex(t, reg, coreKV, sess, "session", nil)
				putEdge(t, reg, adjKV, "atStudio", sess, studio)
			}
		}
		for i := 0; i < s.ProvPerContainer; i++ {
			prov := name("prov_%d_%d", ci, i)
			putVertex(t, reg, coreKV, prov, "provider", nil)
			putEdge(t, reg, adjKV, "practicesAt", prov, c)
		}
		for i := 0; i < s.ItemPerContainer; i++ {
			item := name("item_%d_%d", ci, i)
			putVertex(t, reg, coreKV, item, "menuitem", nil)
			putEdge(t, reg, adjKV, "servedAt", item, c)
		}
	}

	for i := 0; i < s.Tasks; i++ {
		task := name("task%d", i)
		putVertex(t, reg, coreKV, task, "task", nil)
		putEdge(t, reg, adjKV, "assignedTo", task, actor)
	}
	for i := 0; i < s.Instances; i++ {
		inst := name("inst%d", i)
		putVertex(t, reg, coreKV, inst, "service", nil)
		putEdge(t, reg, adjKV, "providedTo", inst, actor)
	}
	for i := 0; i < s.Bookings; i++ {
		bk := name("bk%d", i)
		putVertex(t, reg, coreKV, bk, "booking", nil)
		putEdge(t, reg, adjKV, "bookedBy", bk, actor)
	}
	la := name("leaseapp")
	putVertex(t, reg, coreKV, la, "leaseapp", nil)
	putEdge(t, reg, adjKV, "applicationFor", la, actor)
	for i := 0; i < s.Tabs; i++ {
		tab := name("tab%d", i)
		putVertex(t, reg, coreKV, tab, "tab", nil)
		putEdge(t, reg, adjKV, "openFor", tab, la)
	}

	for r := 0; r < s.Roles; r++ {
		role := name("role%d", r)
		putVertex(t, reg, coreKV, role, "role", nil)
		putEdge(t, reg, adjKV, "holdsRole", actor, role)
		for i := 0; i < s.PermsPerRole; i++ {
			perm := name("perm_%d_%d", r, i)
			op := name("permop_%d_%d", r, i)
			putVertex(t, reg, coreKV, perm, "permission", nil)
			putVertex(t, reg, coreKV, op, "meta", nil)
			putEdge(t, reg, adjKV, "grantedBy", perm, role)
			putEdge(t, reg, adjKV, "forOperation", perm, op)
		}
		for i := 0; i < s.QueuedPerRole; i++ {
			task := name("queued_%d_%d", r, i)
			putVertex(t, reg, coreKV, task, "task", nil)
			putEdge(t, reg, adjKV, "queuedFor", task, role)
		}
		for i := 0; i < s.PanesPerRole; i++ {
			pane := name("pane_%d_%d", r, i)
			putVertex(t, reg, coreKV, pane, "meta", nil)
			putEdge(t, reg, adjKV, "offeredTo", pane, role)
		}
	}

	// The workplace chain the staff studio/work-order walks share, again with a
	// zero-hop arm: the workplace itself is a `place`.
	work := name("work")
	putVertex(t, reg, coreKV, work, "location", nil)
	putEdge(t, reg, adjKV, "worksAt", actor, work)
	places := []string{work}
	for i := 0; i < s.WorkPlaces; i++ {
		place := name("place%d", i)
		putVertex(t, reg, coreKV, place, "location", nil)
		putEdge(t, reg, adjKV, "containedIn", place, work)
		places = append(places, place)
	}
	for pi, place := range places {
		for i := 0; i < s.StudiosPerPlace; i++ {
			studio := name("wstudio_%d_%d", pi, i)
			putVertex(t, reg, coreKV, studio, "studio", nil)
			putEdge(t, reg, adjKV, "locatedAt", studio, place)
		}
		for i := 0; i < s.WorkOrdersPerPlace; i++ {
			wo := name("wo_%d_%d", pi, i)
			putVertex(t, reg, coreKV, wo, "workorder", nil)
			putEdge(t, reg, adjKV, "locatedAt", wo, place)
		}
	}

	instr := name("instructor")
	putVertex(t, reg, coreKV, instr, "instructor", nil)
	putEdge(t, reg, adjKV, "identifiedBy", instr, actor)
	for i := 0; i < s.InstructorSessions; i++ {
		sess := name("ledsess%d", i)
		putVertex(t, reg, coreKV, sess, "session", nil)
		putEdge(t, reg, adjKV, "ledBy", sess, instr)
	}
	pr := name("providerhat")
	putVertex(t, reg, coreKV, pr, "provider", nil)
	putEdge(t, reg, adjKV, "identifiedBy", pr, actor)
	for i := 0; i < s.Appointments; i++ {
		appt := name("appt%d", i)
		putVertex(t, reg, coreKV, appt, "appointment", nil)
		putEdge(t, reg, adjKV, "withProvider", appt, pr)
	}
	sp := name("serviceprovider")
	putVertex(t, reg, coreKV, sp, "serviceprovider", nil)
	putEdge(t, reg, adjKV, "identifiedBy", sp, actor)
	for i := 0; i < s.ProviderInstances; i++ {
		tpl := name("ptpl%d", i)
		inst := name("pinst%d", i)
		putVertex(t, reg, coreKV, tpl, "service", nil)
		putVertex(t, reg, coreKV, inst, "service", nil)
		putEdge(t, reg, adjKV, "providedBy", tpl, sp)
		putEdge(t, reg, adjKV, "instanceOf", inst, tpl)
	}

	return vtxKey(reg, actor)
}

// withoutGroupingAnalysis returns a shallow copy of cr with the grouping
// analysis cleared — the path every evaluation took before it existed.
//
// A COPY, never a write to cr. ast.go's contract is that a compiled rule is
// immutable after Parse precisely so a reader can never observe a
// half-rewritten rule, and a differential that clears the field in place on a
// rule it is concurrently evaluating breaks that contract the day anyone adds
// t.Parallel to this package. Mirrors WithLabelExpansion, including its aliased
// read-only Query.
func withoutGroupingAnalysis(cr *CompiledRule) *CompiledRule {
	if cr == nil {
		return nil
	}
	next := *cr
	next.groupingRedundant = nil
	return &next
}

// unanchoredProducer rewrites a generated producer's head so it binds EVERY
// identity instead of the one `$actorKey` names.
//
// It is what gives the producer differential any discriminating power at all.
// Anchored, a producer binds one actor, so every staging clause has exactly one
// group with or without the reduction and the comparison is one row against one
// row — which an analysis merging every group into one would also pass.
// Unanchored, the grouping key is doing real work: one row per actor, and a
// merged key collapses them.
func unanchoredProducer(t testing.TB, spec string) string {
	t.Helper()
	const anchored, free = "MATCH (identity:identity {key: $actorKey})", "MATCH (identity:identity)"
	require.Containsf(t, spec, anchored, "the generated producer no longer opens on the anchored head")
	return strings.Replace(spec, anchored, free, 1)
}

// executeBothWays runs one parsed rule twice over the same corpus — reduced
// grouping key, then today's full key — and fails unless the two projections
// are identical in order and content AND the two evaluations certified the same
// read-surface footprint. The footprint half is what pins "evaluate, don't
// render", and it has to be an EQUALITY rather than a re-validation:
// pipeline.footprintValid re-reads only the keys the RECORDED footprint names,
// so an evaluation that stopped reading something would simply validate fewer
// keys and pass. Nothing downstream would notice the drift-detection coverage
// it lost, which is why the comparison has to happen here.
//
// It also fails when the rule armed no reduction at all, since two identical
// code paths prove nothing.
func executeBothWays(t *testing.T, spec, actorKey string, adjKV, coreKV *substrate.KV) []ruleengine.ProjectionResult {
	t.Helper()
	eng := New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse:\n%s", spec)
	compiled := cr.(*CompiledRule)
	require.NotEmpty(t, compiled.groupingRedundant,
		"this rule arms no grouping reduction, so running it twice compares one path with itself:\n%s", spec)

	params := ruleengine.EventContext{Parameters: map[string]any{"actorKey": actorKey}}
	reduced, reducedPrint, err := eng.ExecuteWithFootprint(context.Background(), compiled, params, adjKV, coreKV)
	require.NoError(t, err)

	unreduced, fullPrint, err := eng.ExecuteWithFootprint(
		context.Background(), withoutGroupingAnalysis(compiled), params, adjKV, coreKV)
	require.NoError(t, err)

	require.Equal(t, unreduced, reduced,
		"the reduced grouping key moved a projected row — order and content must be identical:\n%s", spec)
	require.Equal(t, fullPrint, reducedPrint,
		"the reduced grouping key changed the evaluation's read-surface footprint:\n%s", spec)
	return reduced
}

// TestGroupingReduction_GeneratedProducersProjectIdenticalRows is §8.1: the
// three real generated producers, over a real corpus, project byte-identical
// rows with the reduction on and off.
func TestGroupingReduction_GeneratedProducersProjectIdenticalRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	specs := generatedReadGrantProducers(t)

	// The corpus the staging primitive's own equivalence proof already builds —
	// same fixture, same shape, so this reduction is measured against the
	// evidence base that fire established.
	t.Run("staging-primitive corpus", func(t *testing.T) {
		corpus := seedEdgeManifestReadGrantCorpus(t)
		for _, name := range sortedNames(namesOf(specs)) {
			rows := executeBothWays(t, specs[name], corpus.actorKey, corpus.adjKV, corpus.coreKV)
			require.Lenf(t, rows, 1, "%s is an actorAggregate producer: one row per actor", name)
			if name == "edgeManifestReadGrants" {
				require.NotEmpty(t, canonicalAnchorSet(t, rows),
					"this corpus is the base domain's — its producer must grant something")
			}
		}
	})

	// A corpus that grants in ALL THREE domains, so no producer's differential
	// is a comparison of two empty slices.
	t.Run("three-domain corpus", func(t *testing.T) {
		adjKV, coreKV := startExecKVs(t)
		reg := newFixtureRegistry()
		actorKey := seedReadGrantCorpus(t, reg, adjKV, coreKV, fullThreeDomainShape("three_"))
		for _, name := range sortedNames(namesOf(specs)) {
			rows := executeBothWays(t, specs[name], actorKey, adjKV, coreKV)
			require.NotEmptyf(t, canonicalAnchorSet(t, rows),
				"%s granted nothing over the three-domain corpus — the differential would prove nothing", name)
		}
	})

	// The same producers over SEVERAL actors, unanchored, so the grouping key
	// separates rows that must not merge. This is the subtest with teeth: the
	// two above compare one row with one row, which an analysis that merged
	// every group into one would also satisfy.
	t.Run("multi-actor unanchored", func(t *testing.T) {
		adjKV, coreKV := startExecKVs(t)
		reg := newFixtureRegistry()
		const actors = 3
		for i := 0; i < actors; i++ {
			seedReadGrantCorpus(t, reg, adjKV, coreKV,
				randomCorpusShape(fmt.Sprintf("multi%d_", i), rand.New(rand.NewSource(int64(i)+7))))
		}
		for _, name := range sortedNames(namesOf(specs)) {
			rows := executeBothWays(t, unanchoredProducer(t, specs[name]), "", adjKV, coreKV)
			require.Lenf(t, rows, actors,
				"%s must project one row per actor — a merged grouping key shows up here as a short row set", name)
			granted := 0
			for _, row := range rows {
				anchors, _ := row.Values["readableAnchors"].([]any)
				granted += len(anchors)
			}
			require.NotZerof(t, granted, "%s granted nothing across %d actors", name, actors)
		}
	})
}

// TestGroupingReduction_RandomizedCorporaDifferential is §8.2's corpus half:
// the same on/off comparison over independently randomized actor-rooted
// corpora, each with its own shared prefixes, multi-parent containedIn arms,
// zero-hop walks and randomly-empty branches. Seeding is deterministic, so a
// failure reproduces exactly.
func TestGroupingReduction_RandomizedCorporaDifferential(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	specs := generatedReadGrantProducers(t)
	names := sortedNames(namesOf(specs))

	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	const corpora = 6
	grantedPerCorpus := make([]int, corpora)
	for i := 0; i < corpora; i++ {
		shape := randomCorpusShape(fmt.Sprintf("rnd%d_", i), rand.New(rand.NewSource(int64(i)+1)))
		actorKey := seedReadGrantCorpus(t, reg, adjKV, coreKV, shape)
		for _, name := range names {
			t.Run(fmt.Sprintf("corpus%d/%s", i, name), func(t *testing.T) {
				rows := executeBothWays(t, specs[name], actorKey, adjKV, coreKV)
				grantedPerCorpus[i] += len(canonicalAnchorSet(t, rows))
			})
		}
		// Per corpus, not summed across all of them: an aggregate guard is
		// satisfied by one productive corpus while five compare empty slices.
		require.NotZerof(t, grantedPerCorpus[i],
			"randomized corpus %d granted nothing in ANY domain — its three differentials compared empty slices", i)
	}

	// One unanchored pass over every corpus at once: now each producer projects
	// one row per actor, so the grouping key has to keep six actors apart and
	// the comparison can see a merge.
	for _, name := range names {
		t.Run("multi-actor/"+name, func(t *testing.T) {
			rows := executeBothWays(t, unanchoredProducer(t, specs[name]), "", adjKV, coreKV)
			require.Lenf(t, rows, corpora,
				"%s must project one row per seeded actor", name)
		})
	}
}

// TestGroupingReduction_UndeterminedCarryKeepsGroupsApart is the differential's
// own positive vector. Every generated producer binds exactly ONE actor, so on
// those a merged grouping key is structurally invisible — one group either way.
// This query instead carries a variable whose value genuinely differs row to
// row alongside the accumulator, so the partition is observable: an analysis
// that dropped an undetermined carry would collapse the tasks into one row, and
// the equality below would fail.
func TestGroupingReduction_UndeterminedCarryKeepsGroupsApart(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	shape := fullThreeDomainShape("undet_")
	actorKey := seedReadGrantCorpus(t, reg, adjKV, coreKV, shape)

	rows := executeBothWays(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, task, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, task, s0, collect(DISTINCT bk.key) AS s1
RETURN task.key AS taskKey, s0 AS a, s1 AS b
`, actorKey, adjKV, coreKV)

	require.Greater(t, shape.Tasks, 1, "the corpus must hold several tasks, or one group is all there could be")
	require.Lenf(t, rows, shape.Tasks,
		"one row per task: an undetermined carry must stay in the grouping key")
}

// TestGroupingReduction_DeterminedAliasReprojectedIsNotACarry is the killer for
// the BARE-CARRY half of the redundancy condition. `s1` names a value the
// previous clause aggregated — so it is determined — but this clause projects a
// different binding under that name, which the analysis must not treat as a
// carry. Dropping `isCarry` from the condition marks it redundant and collapses
// one row per task into one row total.
//
// The `s0.first AS s0` case elsewhere is sound to drop either way; this one is
// not, which is what makes it the vector.
func TestGroupingReduction_DeterminedAliasReprojectedIsNotACarry(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	shape := fullThreeDomainShape("recarry_")
	actorKey := seedReadGrantCorpus(t, reg, adjKV, coreKV, shape)

	rows := executeBothWays(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, s0, collect(DISTINCT bk.key) AS s1
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, s0, task AS s1, collect(DISTINCT task.key) AS s2
RETURN s0 AS a, s1 AS b, s2 AS c
`, actorKey, adjKV, coreKV)

	require.Greater(t, shape.Tasks, 1)
	require.Lenf(t, rows, shape.Tasks,
		"one row per task: an alias re-projected from a different binding is not a carry, "+
			"however determined the previous clause left that name")
}

// TestGroupingReduction_UnknownExpressionRefusalIsReachableFromCypher pins that
// the analysis's default-deny arm answers a query an author can actually write,
// not only a hand-built AST node. `head(xs).key` is a property access on a
// function call, which variableRefChainRoot cannot trace a binding through, so
// the clause holding it determines nothing — including the accumulator it would
// otherwise have reduced.
func TestGroupingReduction_UnknownExpressionRefusalIsReachableFromCypher(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (n:task)
WITH n, collect(n.key) AS xs
WITH n, xs, head(xs).key AS z, collect(n.key) AS s1
RETURN z AS a, s1 AS b`)
	require.Len(t, plans, 3)
	require.Empty(t, plans[0].Refusal)
	require.Contains(t, plans[1].Refusal, `"z"`,
		"the refusal must name the column whose shape stopped the walk")
	require.Equal(t, []bool{false, false, false, false}, plans[1].Redundant,
		"`xs` is determined and carried, but a clause the walk cannot read whole claims nothing")
	require.Equal(t, []string{"n", "xs", "z"}, plans[1].Key)
}

// randomStagedQuery builds a small multi-WITH query in the generated producers'
// shape, drawing each carry deterministically from the shapes the analysis has
// to tell apart:
//
//   - a plain bare carry (reducible, once the alias is determined);
//   - a rename, of the actor or of an accumulator (fail-closed / not a carry);
//   - a computed value under a key alias (fail-closed);
//   - a duplicated NON-aggregating alias (fail-closed);
//   - an aggregate landing on an alias the clause ALSO carries — the collision
//     that makes the analysis's alias sets stop describing the executor's
//     partition, and the one shape whose absence here let a real over-merge
//     ship;
//   - the branch's own anchor variable, an UNDETERMINED carry whose value
//     really does differ row to row. Without at least one of these in the key a
//     query has one group either way and its differential proves nothing.
//
// Every stage stays LIVE: the branch clause is rooted on whatever the actor is
// currently called, and once a rename or a computed projection has cost the
// actor its node binding the branch binds its own anchor instead, so a walk
// never runs against an unbound or string-valued `identity`.
func randomStagedQuery(r *rand.Rand) string {
	branches := []struct{ rooted, free, anchor string }{
		{"OPTIONAL MATCH (%s)<-[:assignedTo]-(task:task)", "OPTIONAL MATCH (task:task)", "task"},
		{"OPTIONAL MATCH (%s)<-[:bookedBy]-(bk:booking)", "OPTIONAL MATCH (bk:booking)", "bk"},
		{"OPTIONAL MATCH (%s)<-[:providedTo]-(inst:service)", "OPTIONAL MATCH (inst:service)", "inst"},
		{"OPTIONAL MATCH (%s)-[:holdsRole]->(role:role)", "OPTIONAL MATCH (role:role)", "role"},
		{"OPTIONAL MATCH (%s)-[:residesIn]->(home)-[:containedIn*0..]->(container)",
			"OPTIONAL MATCH (container:location)", "container"},
	}

	var b strings.Builder
	b.WriteString("\nMATCH (identity:identity {key: $actorKey})\n")

	actor, actorIsNode := "identity", true
	live := []string{}
	stages := 2 + r.Intn(3)
	for k := 0; k < stages; k++ {
		br := branches[r.Intn(len(branches))]
		if actorIsNode {
			b.WriteString(fmt.Sprintf(br.rooted, actor) + "\n")
		} else {
			b.WriteString(br.free + "\n")
		}

		carries := []string{}
		switch r.Intn(12) {
		case 0:
			// A rename: the binding survives under a name no dependence was
			// proved for, so the clause must fail closed.
			carries = append(carries, actor+" AS actor")
			actor = "actor"
		case 1:
			// A computed value under the key's own alias: the name survives, the
			// binding it named does not.
			carries = append(carries, actor+".key AS "+actor)
			actorIsNode = false
		default:
			carries = append(carries, actor)
		}

		next := []string{}
		if r.Intn(2) == 0 {
			carries = append(carries, br.anchor)
			next = append(next, br.anchor)
		}
		for _, a := range live {
			switch r.Intn(12) {
			case 0: // dropped entirely
			case 1:
				carries = append(carries, a+" AS "+a+"r")
				next = append(next, a+"r")
			case 2:
				carries = append(carries, a, a)
				next = append(next, a)
			default:
				carries = append(carries, a)
				next = append(next, a)
			}
		}

		// The aggregate's alias: usually its own fresh name, sometimes one this
		// very clause also carries. The collision is legal cypher and the
		// executor resolves it — the aggregate wins the row — so the analysis
		// must notice that the alias now names two items and claim nothing.
		slice := fmt.Sprintf("s%d", k)
		if collidable := append(append([]string{}, next...), actor); r.Intn(8) == 0 {
			slice = collidable[r.Intn(len(collidable))]
			next = removeName(next, slice)
			if slice == actor {
				// The actor's own name now holds the collected list, so the next
				// stage roots its walk on its own anchor instead of a dead one.
				actorIsNode = false
			}
		}
		b.WriteString("WITH " + strings.Join(carries, ", ") + ",\n  collect(DISTINCT " + br.anchor + ".key) AS " + slice + "\n")
		live = appendUnique(next, slice)
	}

	cols := make([]string, 0, len(live))
	for i, a := range live {
		cols = append(cols, fmt.Sprintf("%s AS c%d", a, i))
	}
	b.WriteString("RETURN " + strings.Join(cols, ", ") + "\n")
	return b.String()
}

func removeName(names []string, drop string) []string {
	out := names[:0]
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

func appendUnique(names []string, add string) []string {
	for _, n := range names {
		if n == add {
			return names
		}
	}
	return append(names, add)
}

// hasDuplicateProjectionAlias reports whether any projecting clause of q names
// one alias twice — derived from the AST directly rather than from the
// analysis's refusal string, so it is independent evidence about the query
// rather than a restatement of what the code under test decided.
func hasDuplicateProjectionAlias(q *Query) bool {
	for _, c := range q.Clauses {
		var items []ProjectionItem
		switch cl := c.(type) {
		case *With:
			items = cl.Items
		case *Return:
			items = cl.Items
		default:
			continue
		}
		seen := map[string]struct{}{}
		for i, it := range items {
			a := it.Alias
			if a == "" {
				a = projectionAutoAlias(it.Expr, i)
			}
			if _, repeat := seen[a]; repeat {
				return true
			}
			seen[a] = struct{}{}
		}
	}
	return false
}

// TestGroupingReduction_RandomizedQueriesDifferential is §8.2's query half: the
// analysis's branches — reducible and fail-closed alike — exercised over one
// corpus, each generated query executed with the reduction on and off.
func TestGroupingReduction_RandomizedQueriesDifferential(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	actorKey := seedReadGrantCorpus(t, reg, adjKV, coreKV, fullThreeDomainShape("q_"))

	eng := New()
	params := ruleengine.EventContext{Parameters: map[string]any{"actorKey": actorKey}}

	const queries = 80
	armedCount, refusedCount, collisionCount, multiRowCount, nonDeterministicCount := 0, 0, 0, 0, 0
	for i := 0; i < queries; i++ {
		spec := randomStagedQuery(rand.New(rand.NewSource(int64(i) + 1)))
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "generated query %d must parse:\n%s", i, spec)
		compiled := cr.(*CompiledRule)

		if len(compiled.groupingRedundant) > 0 {
			armedCount++
		}
		for _, p := range analyseGrouping(compiled.Query) {
			if !p.Grouping {
				continue
			}
			if p.Refusal != "" {
				refusedCount++
			}
			if strings.Contains(p.Refusal, "twice") {
				collisionCount++
			}
		}

		reduced, err := eng.ExecuteWith(context.Background(), compiled, params, adjKV, coreKV)
		require.NoErrorf(t, err, "generated query %d must execute:\n%s", i, spec)

		// The control: the SAME configuration again. A query whose own output is
		// not reproducible cannot have its content compared across two
		// configurations, and the executor has one such shape — two
		// non-aggregating items sharing an alias, where the surviving value is
		// picked by map iteration order (projectItems' groupVals walk). Detect
		// it rather than assume it away.
		control, err := eng.ExecuteWith(context.Background(), compiled, params, adjKV, coreKV)
		require.NoErrorf(t, err, "generated query %d must execute twice:\n%s", i, spec)

		unreduced, err := eng.ExecuteWith(
			context.Background(), withoutGroupingAnalysis(compiled), params, adjKV, coreKV)
		require.NoErrorf(t, err, "generated query %d must execute unreduced:\n%s", i, spec)

		if reflect.DeepEqual(control, reduced) {
			require.Equalf(t, unreduced, reduced,
				"generated query %d projected different rows with the grouping key reduced:\n%s", i, spec)
		} else {
			// Only that one executor shape may be irreproducible. Anything else
			// is a defect this differential must not absorb.
			nonDeterministicCount++
			require.Truef(t, hasDuplicateProjectionAlias(compiled.Query),
				"generated query %d is not reproducible under a FIXED configuration and carries no "+
					"duplicated projection alias — that is a defect, not a known shape:\n%s", i, spec)
			// The row COUNT is stable even there, and a merged grouping key is
			// exactly a change in row count, so the failure mode stays pinned.
			require.Lenf(t, unreduced, len(reduced),
				"generated query %d changed its ROW COUNT with the grouping key reduced:\n%s", i, spec)
		}
		if len(reduced) > 1 {
			multiRowCount++
		}
	}

	t.Logf("armed=%d refused=%d collisions=%d multiRow=%d nondet=%d of %d", armedCount, refusedCount, collisionCount, multiRowCount, nonDeterministicCount, queries)
	require.Greaterf(t, armedCount, queries/4,
		"only %d of %d generated queries armed a reduction — the differential is mostly comparing one path with itself",
		armedCount, queries)
	require.Greaterf(t, refusedCount, 0,
		"no generated query exercised a fail-closed branch of the analysis")
	require.Greaterf(t, collisionCount, 0,
		"no generated query put an aggregate on an alias its own clause also carried — "+
			"the collision that made the alias sets stop describing the partition went untested")
	require.Greaterf(t, multiRowCount, queries/4,
		"only %d of %d generated queries projected more than ONE row — a one-row comparison "+
			"cannot see a merged grouping key, which is the whole failure mode", multiRowCount, queries)
}
