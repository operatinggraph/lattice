// The convergence sweep as the standing healer of a NARROWED consumer —
// auth-plane-projection-latency-design.md §4.2's `hasSweepPlan` conjunct and
// §6's substitution check, end to end over a real embedded NATS server.
//
// §4.2 refuses to narrow a lens that has no sweep plan, because narrowing takes
// away an ACCIDENT: today an unrelated business event on a neighbour vertex
// incidentally re-projects that neighbour's actor, and a row lost out of band is
// repaired as a side effect. The sibling sweep e2es in internal/refractor prove
// the sweep heals a lost row, but both run under a BROAD consumer, where that
// accident is still standing — so neither shows that the sweep is what is left
// once it is gone.
//
// This test runs two pipelines over one graph, identical but for a single §4.2
// conjunct, so the difference between them is the narrowing and nothing else:
//
//   - the NARROWED lens satisfies every conjunct, so its consumer is filtered to
//     its own labels server-side;
//   - the CONTROL lens declares no pattern-closed output — the §4.4 shape a
//     Personal Lens has — so it keeps the broad filter and the unconditional
//     fan-out, which is the world before Increment 2.
//
// A THIRD lens joins them, identical to the control but for a cypher that BINDS
// the neighbour type the healing event lands on. It is what keeps the other two
// honest: it heals off that event, so the incidental reprojection they lose is
// demonstrably real rather than assumed. The three then separate the three
// reasons a lens can stay silent — the event never arrived (the narrowed
// filter), it arrived and the walk crossed nothing (the pattern-scoped walk of
// refractor-hub-walk-and-periodic-load-design.md §5.1), or it arrived and the
// walk did cross (the binding arm, which heals). Every arm carries its own
// positive vector, so no silence is satisfied by a wedged consumer or a fixture
// that delivers nothing.
package pipeline_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// healerCypher is the lens both pipelines run: an actor-aggregate projection
// anchored on identity whose row value comes from a TRAVERSAL rather than from
// the anchor's own body, so restoring it correctly requires the sweep to
// re-execute the pattern and not merely to echo the vertex it walked from.
//
// Its referenced-label set is exactly {identity, role} — exhaustive, with the
// anchor type in it — which is what makes it eligible to narrow.
const healerCypher = `
MATCH (identity:identity {key: $actorKey})-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey, role.data.name AS roleName
`

// healerBindingCypher is healerCypher plus a pattern position that BINDS the
// neighbour type the healing event lands on. Its projected row is identical —
// the OPTIONAL clause contributes no RETURN column — so the two lenses produce
// the same row from the same graph, and the ONLY thing that differs is whether
// the lens's own patterns can bind a `booking`.
//
// That is what makes it the positive vector for the incidental heal: its walk
// stands on the changed booking, finds `bookedBy` among the relations its
// patterns traverse, crosses to the identity, and reprojects it. A lens whose
// patterns cannot bind `booking` has no such edge to cross
// (refractor-hub-walk-and-periodic-load-design.md §5.1).
const healerBindingCypher = `
MATCH (identity:identity {key: $actorKey})-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)<-[:bookedBy]-(booking:booking)
RETURN identity.key AS actorKey, role.data.name AS roleName
`

// healerRule builds the lens Rule for one of the three pipelines. Each gets the
// same anchor type and the same guarded emptyBehavior; the rule ID, the target
// bucket and the output key prefix differ so the rows of one never collide with
// another's, and the CYPHER differs only on the third arm, which is the one
// thing that arm is there to vary.
func healerRule(t *testing.T, ruleID, bucket, keyPrefix, cypher string) *lens.Rule {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(cypher)
	require.NoError(t, err)
	return &lens.Rule{
		ID:             ruleID,
		CanonicalName:  ruleID,
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into: lens.IntoConfig{
			Target: "nats_kv",
			Bucket: bucket,
			Key:    lens.KeyField{"key"},
		},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: keyPrefix + "{actorSuffix}",
			BodyColumns:      []string{"actorKey", "roleName"},
			EmptyBehavior:    string(projection.EmptyDelete),
			Freshness:        "auto",
		},
	}
}

// healerLens is one installed pipeline plus the handles a test assertion needs.
type healerLens struct {
	rule     *lens.Rule
	desc     projection.OutputDescriptor
	pipeline *pipeline.Pipeline
	targetKV *substrate.KV
	reporter *health.Reporter
	rowKey   string
}

// installHealerLens wires a pipeline through the REAL install gate
// (projection.InstallActorAggregate — the one that declares pattern closure and
// enrols the sweep), then compresses the sweep tick so the test exercises many
// bounded passes instead of waiting out the production interval. Everything else
// is what the driver just derived.
func installHealerLens(t *testing.T, env *pipelineEnv, ruleID, bucket, keyPrefix, cypher string, logger *slog.Logger) *healerLens {
	t.Helper()
	rule := healerRule(t, ruleID, bucket, keyPrefix, cypher)
	targetKV, adpt := newTargetKV(t, env, bucket, []string{"key"})
	reporter := newHealthReporter(t, env, ruleID)

	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, reporter)
	require.NoError(t, err)
	p.UseFullEngine(full.New(), rule.CompiledRule)

	projectionRevision := func(k string) uint64 {
		entry, getErr := env.coreKV.Get(context.Background(), k)
		if getErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	require.True(t,
		projection.InstallActorAggregate(p, adpt, rule, projectionRevision, env.adjKV, env.coreKV, logger),
		"a guarded actor-aggregate lens that can name its own keys must install")

	desc, err := projection.ParseOutputDescriptor(rule.Output)
	require.NoError(t, err)
	prefix, ok := desc.KeyPrefix()
	require.True(t, ok, "the lens must be able to scope a listing to its own keys")
	require.NotNil(t, p.Sweeper(), "§4.2's sweep-plan conjunct must be satisfied by the install itself")
	p.SetSweepPlan(pipeline.SweepPlan{
		AnchorType:    desc.AnchorType,
		AnchorFromKey: desc.AnchorFromKey,
		KeyPrefix:     prefix,
		Interval:      250 * time.Millisecond,
		Batch:         25,
	})

	return &healerLens{rule: rule, desc: desc, pipeline: p, targetKV: targetKV, reporter: reporter}
}

// rowValue reads the lens's row for its anchor, returning nil when the row is
// absent (the divergence this test creates deliberately).
func (h *healerLens) rowValue(t *testing.T) map[string]any {
	t.Helper()
	entry, err := h.targetKV.Get(context.Background(), h.rowKey)
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return nil
	}
	var env map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &env))
	return env
}

// rowRevision is the lens row's current NATS-KV revision, 0 when the row is
// absent. It is the test's "the handler has finished" barrier, and it exists
// because a settled consumer is NOT one: NumPending drops the instant a
// delivery is prefetched into the client buffer, so waiting on it and then
// purging races the write it was meant to wait for.
func (h *healerLens) rowRevision(t *testing.T) uint64 {
	t.Helper()
	entry, err := h.targetKV.Get(context.Background(), h.rowKey)
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return 0
	}
	return entry.Revision
}

// runHealerPipeline registers the consumer on the supplied filter and runs the
// pipeline for the rest of the test.
func runHealerPipeline(t *testing.T, env *pipelineEnv, h *healerLens, filterSubjects []string, filterSubject string) {
	t.Helper()
	spec := specFor(h.rule.ID)
	spec.FilterSubject = filterSubject
	spec.FilterSubjects = filterSubjects
	h.pipeline.RunOn(env.conn, spec)

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.pipeline.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })
}

// TestNarrowedConsumer_ConvergenceSweepIsTheOnlyRemainingHealer_E2E is the
// acceptance for the whole design (§18.3): a row deleted under a NARROWED
// consumer is restored by RunSweep, and by nothing else.
//
// Five steps, each asserted rather than assumed:
//
//  1. the lens really narrows — the exact subject set, not the broad fallback;
//  2. the row projects normally and carries the traversed value;
//  3. the row is removed out of band, the hole no CDC event will refill;
//  4. an event the narrowed filter EXCLUDES arrives, and three arms separate
//     the three reasons a lens can stay silent: a lens whose cypher BINDS the
//     event vertex's type heals off it (the heal is real), the narrowed lens is
//     never handed it (its counter never moves), and the control is handed it
//     (its counter moves) and still does not heal, because its own patterns
//     bind no position for that type;
//  5. RunSweep restores the row, with the correct value.
func TestNarrowedConsumer_ConvergenceSweepIsTheOnlyRemainingHealer_E2E(t *testing.T) {
	env := startPipelineEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// The adjacency index the fan-out enumerator walks is maintained by its own
	// dedicated whole-stream consumer, NOT by either lens's deliveries — which
	// is the premise that lets §4.6 narrow an actor-aware lens at all. Running
	// the real bootstrapper here is what keeps that premise under test instead
	// of assumed.
	boots := consumer.NewBootstrapper(env.conn, coreKVBucket, env.adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(15 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 15s")
	}

	const (
		narrowedRuleID = "narrowed-heal-lens"
		controlRuleID  = "control-heal-lens"
		bindingRuleID  = "binding-heal-lens"
	)
	narrowed := installHealerLens(t, env, narrowedRuleID, "narrowed-heal-target", "narrowedHeal.", healerCypher, logger)
	control := installHealerLens(t, env, controlRuleID, "control-heal-target", "controlHeal.", healerCypher, logger)
	binding := installHealerLens(t, env, bindingRuleID, "binding-heal-target", "bindingHeal.", healerBindingCypher, logger)

	// Both controls' ONE difference from the narrowed lens: they declare no
	// pattern-closed output, the §4.4 shape whose row depends on an input
	// outside its compiled pattern. That single failed conjunct is what puts
	// them on the broad filter with their fan-out arms ungated — the
	// pre-Increment-2 world, reproduced by a real conjunct rather than by
	// hand-editing a filter.
	control.pipeline.SetPatternClosedOutput(false)
	binding.pipeline.SetPatternClosedOutput(false)

	// --- STEP 1: the lens under test is ACTUALLY narrowed ---------------------
	// A silent fall-back to the broad filter would make every later assertion
	// vacuous, so the subject set is pinned exactly, not merely checked for
	// non-emptiness.
	narrowedSubjects, narrowedBroad, narrowedDecision := narrowed.pipeline.ConsumerFilter()
	require.Empty(t, narrowedBroad,
		"the lens satisfies every §4.2 conjunct — a broad filter here means the narrowing under test never happened")
	require.Equal(t, health.FilterModeNarrowedRelation, narrowedDecision.Mode)
	require.ElementsMatch(t, []string{
		"$KV." + coreKVBucket + ".vtx.identity.>",
		"$KV." + coreKVBucket + ".lnk.identity.*.holdsRole.>",
		"$KV." + coreKVBucket + ".lnk.*.*.holdsRole.identity.>",
		"$KV." + coreKVBucket + ".vtx.role.>",
		"$KV." + coreKVBucket + ".lnk.role.*.holdsRole.>",
		"$KV." + coreKVBucket + ".lnk.*.*.holdsRole.role.>",
	}, narrowedSubjects,
		"the vertex form per referenced label plus the one traversed relation in both link directions — nothing outside {identity, role} x {holdsRole}")

	for _, h := range []*healerLens{control, binding} {
		subs, broad, dec := h.pipeline.ConsumerFilter()
		require.Emptyf(t, subs,
			"%s must be on the broad filter — otherwise it is a second narrowed lens and proves nothing", h.rule.ID)
		require.Equal(t, subjects.CoreKVFilter(coreKVBucket), broad)
		require.Equal(t, health.FilterModeBroad, dec.Mode)
	}
	controlSubjects, controlBroad, _ := control.pipeline.ConsumerFilter()
	bindingSubjects, bindingBroad, _ := binding.pipeline.ConsumerFilter()

	runHealerPipeline(t, env, narrowed, narrowedSubjects, narrowedBroad)
	runHealerPipeline(t, env, control, controlSubjects, controlBroad)
	runHealerPipeline(t, env, binding, bindingSubjects, bindingBroad)

	// --- STEP 2: the row projects normally ------------------------------------
	// The graph: one identity holding one role (the lens's own pattern), plus a
	// booking joined to that identity — a neighbour of a type the lens never
	// references, which is the whole point of it.
	roleID := narrowedID(t, "SweepGrant")
	identityID := narrowedID(t, "SweepAnchor")
	bookingID := narrowedID(t, "SweepBooking")
	roleKey := substrate.VertexKey("role", roleID)
	identityKey := substrate.VertexKey("identity", identityID)
	bookingKey := substrate.VertexKey("booking", bookingID)

	putNode(t, env.coreKV, roleKey, map[string]any{
		"key": roleKey, "class": "role", "data": map[string]any{"name": "auditor"},
	})
	putNode(t, env.coreKV, identityKey, map[string]any{
		"key": identityKey, "class": "identity", "data": map[string]any{"name": "narrow-heal-alice"},
	})
	putLink(t, env.coreKV, "identity", identityID, "holdsRole", "role", roleID)
	putNode(t, env.coreKV, bookingKey, map[string]any{
		"key": bookingKey, "class": "booking", "data": map[string]any{"status": "confirmed"},
	})
	putLink(t, env.coreKV, "booking", bookingID, "bookedBy", "identity", identityID)

	narrowed.rowKey = narrowed.desc.BuildKey(identityKey)
	control.rowKey = control.desc.BuildKey(identityKey)
	binding.rowKey = binding.desc.BuildKey(identityKey)

	for _, h := range []*healerLens{narrowed, control, binding} {
		pollUntil(t, 30*time.Second, func() bool { return h.rowValue(t) != nil })
		row := h.rowValue(t)
		require.Equal(t, identityKey, row["actor"], "lens %s", h.rule.ID)
		require.Equal(t, "auditor", row["roleName"],
			"lens %s must project the role reached by the traversal, not a value read off the anchor", h.rule.ID)
	}
	settled := waitConsumerSettled(t, env, "refractor-"+narrowedRuleID)
	waitConsumerSettled(t, env, "refractor-"+controlRuleID)
	waitConsumerSettled(t, env, "refractor-"+bindingRuleID)

	// The positive vector for the counter step 4 reads. A frozen
	// Delivered.Consumer would satisfy step 4's inequality for any reason at all
	// — a wedged consumer, a stream nobody publishes to — so an IN-label write
	// has to move it first, or the exclusion below proves only that this fixture
	// delivers nothing to anybody.
	//
	// It doubles as the barrier the purge below needs. Both lenses re-project
	// off this write, so once both rows have advanced a revision, every event
	// published so far has been APPLIED (a consumer delivers in order), and the
	// purge cannot race an in-flight write back into the row it just removed.
	narrowedRev, controlRev, bindingRev := narrowed.rowRevision(t), control.rowRevision(t), binding.rowRevision(t)
	putNode(t, env.coreKV, roleKey, map[string]any{
		"key": roleKey, "class": "role", "data": map[string]any{"name": "auditor"},
		"lastModifiedAt": "2026-01-02T00:00:00Z",
	})
	pollUntil(t, 30*time.Second, func() bool { return narrowed.rowRevision(t) > narrowedRev })
	pollUntil(t, 30*time.Second, func() bool { return control.rowRevision(t) > controlRev })
	pollUntil(t, 30*time.Second, func() bool { return binding.rowRevision(t) > bindingRev })
	inLabel := waitConsumerSettled(t, env, "refractor-"+narrowedRuleID)
	require.Greater(t, inLabel.Delivered.Consumer, settled.Delivered.Consumer,
		"a write on a type INSIDE the label set must still be delivered — this counter has to be able to move")

	// --- STEP 3: the row is lost out of band ----------------------------------
	// A restore, an errant purge — the class of hole no CDC event will ever
	// refill, because the event that would have is in the past. Both lenses lose
	// their row, so the only asymmetry left is the filter.
	for _, h := range []*healerLens{narrowed, control, binding} {
		require.NoError(t, h.targetKV.Purge(ctx, h.rowKey))
		require.Nil(t, h.rowValue(t), "lens %s row must be absent before the healing question is asked", h.rule.ID)
	}

	// --- STEP 4: three arms, three claims, one event -------------------------
	// A write to the booking vertex. Each arm below asserts a DIFFERENT claim
	// about that one event, and each carries its own positive vector, so no
	// arm's silence can be satisfied by a wedged consumer or a fixture that
	// delivers nothing:
	//
	//  a. BINDING (broad filter, cypher binds `booking`) — the event is
	//     delivered AND it heals. This is the proof that the incidental heal is
	//     REAL: without it, "the row stayed missing" everywhere else would be
	//     satisfied by an event that never healed anything.
	//  b. NARROWED (narrowed filter, cypher does not bind `booking`) — the event
	//     is never HANDED to it; its delivery counter cannot move. This is D1's
	//     server-side narrowing, and (a) is what makes it a real loss.
	//  c. CONTROL (broad filter, same cypher as the narrowed lens) — the event
	//     IS handed to it, its counter moves, and it still does not heal,
	//     because the actor walk follows only the relations of pattern hops
	//     incident to a position admitting the type it is standing on
	//     (refractor-hub-walk-and-periodic-load-design.md §5.1) and no position
	//     of THIS cypher admits `booking`. (a) and (c) differ by the cypher
	//     alone, which is what isolates the walk from the filter.
	//
	// Both broad arms are scoped even though they fail §4.2's pattern-closure
	// conjunct, and that is not an inconsistency: the walk scope carries §4.2's
	// OTHER conjunct, the standing healer, and installHealerLens gives all three
	// pipelines a SweepPlan (it asserts p.Sweeper() before setting one). An
	// actor-aware lens with no healer at all keeps the relation-blind walk and
	// its accident — walkScopeRefusalNoHealer, pinned in
	// walkscope_internal_test.go.
	narrowedBefore := waitConsumerSettled(t, env, "refractor-"+narrowedRuleID)
	controlBefore := waitConsumerSettled(t, env, "refractor-"+controlRuleID)
	bindingBefore := waitConsumerSettled(t, env, "refractor-"+bindingRuleID)
	putNode(t, env.coreKV, bookingKey, map[string]any{
		"key": bookingKey, "class": "booking", "data": map[string]any{"status": "cancelled"},
		"lastModifiedAt": "2026-01-02T00:00:00Z",
	})

	// (a) The heal is real.
	pollUntil(t, 30*time.Second, func() bool { return binding.rowValue(t) != nil })
	require.Equal(t, "auditor", binding.rowValue(t)["roleName"],
		"a lens whose pattern binds `booking` must be healed by the booking event, with the traversed value — "+
			"if it is not, this event never healed anything and the two silences below prove nothing")
	bindingAfter := waitConsumerSettled(t, env, "refractor-"+bindingRuleID)
	require.Greater(t, bindingAfter.Delivered.Consumer, bindingBefore.Delivered.Consumer)

	// (c) Delivered, and still silent — the WALK's doing, not the filter's.
	controlAfter := waitConsumerSettled(t, env, "refractor-"+controlRuleID)
	require.Greater(t, controlAfter.Delivered.Consumer, controlBefore.Delivered.Consumer,
		"the control must have been handed the booking event — otherwise its silence is the filter's")
	require.Nil(t, control.rowValue(t),
		"the control received the event and its walk crossed nothing: `booking` binds no position of ITS cypher, "+
			"and the binding arm above shows the same event healing a lens whose cypher does bind it")

	// (b) Never handed it at all — the server-side narrowing.
	narrowedAfter := waitConsumerSettled(t, env, "refractor-"+narrowedRuleID)
	require.Equal(t, narrowedBefore.Delivered.Consumer, narrowedAfter.Delivered.Consumer,
		"the narrowed consumer must never have been HANDED the booking event — a client-side skip would still move this counter")
	require.Nil(t, narrowed.rowValue(t),
		"the narrowed lens has no incidental heal left; if its row came back, the narrowing is not what this test thinks it is")

	// --- STEP 5: the sweep is what restores it --------------------------------
	// Nobody names the actor. The sweep compares the lens's anchors against the
	// keys under its own prefix, finds the hole itself, and repairs it through
	// the same per-actor Reproject the operator-driven reconciliation verb uses.
	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	swept := make(chan struct{})
	go func() { defer close(swept); narrowed.pipeline.RunSweep(sweepCtx) }()

	pollUntil(t, 30*time.Second, func() bool { return narrowed.rowValue(t) != nil })
	healed := narrowed.rowValue(t)
	require.Equal(t, narrowed.rowKey, healed["key"])
	require.Equal(t, identityKey, healed["actor"])
	require.Equal(t, "auditor", healed["roleName"],
		"the restored row must carry the traversed value — a row that is merely present is not a heal")

	// The heal count is folded in when the pass ends, which is strictly after
	// the write it counts, so it is polled rather than read off the instant the
	// row appeared.
	pollUntil(t, 30*time.Second, func() bool { return narrowed.pipeline.Sweeper().Status().Reconciled >= 1 })

	// The cursor and the cumulative count persist on the lens's own health
	// entry, so a restarted process resumes the walk rather than restarting it.
	persisted, err := narrowed.reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, persisted.SweepCursor, "the round-robin cursor must be persisted")

	stopSweep()
	select {
	case <-swept:
	case <-time.After(10 * time.Second):
		t.Fatal("RunSweep did not stop with its context")
	}
}
