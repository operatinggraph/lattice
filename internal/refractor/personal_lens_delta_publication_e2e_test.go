// Package refractor_test — end-to-end proof for personal-lens-delta-
// publication-design.md Increment 1 (T6). Reuses pl2Harness / writePL2Vertex /
// writePL2Link / pl2NanoID / pl3Consumer / drainUntilQuiet / drainBriefly and
// the grant-edge wiring of personal_lens_grant_change_e2e_test.go.
package refractor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// deltaScopeFixture is a personal lens over an actor holding TWO leases, with a
// real per-entry cap-read producer wired to the grant-change edge.
//
// Two anchors, not one: a scoped publish and a whole-actor publish are the same
// message set for a one-row actor, so a single-lease fixture could not tell
// them apart at all.
type deltaScopeFixture struct {
	h            *pl2Harness
	reprojector  *grantchange.Reprojector
	personalLens string
	personal     *pipeline.Pipeline
	producer     *pipeline.Pipeline
	identityID   string
	// alpha is granted before the measurement begins; beta is the anchor whose
	// grant lands during it.
	alphaID, betaID string
	stopPersonal    func()
}

const (
	deltaAlphaEntity = "lease-alpha"
	deltaBetaEntity  = "lease-beta"
)

func newDeltaScopeFixture(t *testing.T, name string) *deltaScopeFixture {
	t.Helper()
	h := newPL2Harness(t)
	f := &deltaScopeFixture{
		h:            h,
		reprojector:  grantchange.New(),
		personalLens: pl2NanoID(name + "-personal"),
		identityID:   pl2NanoID(name + "-identity"),
		alphaID:      pl2NanoID(name + "-alpha"),
		betaID:       pl2NanoID(name + "-beta"),
	}

	personalAdpt, err := adapter.NewNatsSubjectAdapter(h.ctx, h.conn, f.personalLens,
		"lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	personalRule := &lens.Rule{
		ID:             f.personalLens,
		CanonicalName:  "lens.delta-scope-personal",
		ResolvedEngine: ruleengine.EngineFull,
		Into: lens.IntoConfig{
			Target: "nats_subject", Personal: true,
			Key: lens.KeyField{adapter.PersonalActorKeyField, "entityId"},
		},
	}
	personalRule.CompiledRule = mustParse(t, `
MATCH (identity {key: $actorKey})-[:holds]->(l:lease)
RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId
`)
	personalPipe, err := pipeline.New(f.personalLens, "nats_subject", "core-kv", h.adjKV, h.coreKV, personalAdpt, nil)
	require.NoError(t, err)
	personalPipe.UseFullEngine(fullEngineSingleton, personalRule.CompiledRule)
	require.True(t, projection.InstallPersonalLens(personalPipe, personalRule,
		h.adjKV, h.coreKV, h.interestKV, h.capKV, false, h.logger))
	f.reprojector.RegisterPersonal(f.personalLens, personalPipe)
	f.personal = personalPipe

	producerID := pl2NanoID(name + "-producer")
	producerRule := &lens.Rule{
		ID:             producerID,
		CanonicalName:  "lens.delta-scope-producer",
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		Into: lens.IntoConfig{
			Target: "nats_kv", Bucket: projection.AuthPlaneBucket, Key: lens.KeyField{"key"},
		},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    string(projection.EmptyDelete),
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	}
	producerRule.CompiledRule = mustParse(t, `
MATCH (identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:mayRead]->(l:lease)
RETURN identity.key AS actorKey,
  collect({anchorId: nanoIdFromKey(l.key), anchorType: 'lease'}) AS readableAnchors
`)
	producerAdpt, err := adapter.New(h.capKV, producerRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	f.producer, err = pipeline.New(producerID, "nats_kv", "core-kv", h.adjKV, h.coreKV, producerAdpt, nil)
	require.NoError(t, err)
	f.producer.UseFullEngine(fullEngineSingleton, producerRule.CompiledRule)

	projectionRevision := func(k string) uint64 {
		entry, gErr := h.coreKV.Get(h.ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	require.True(t, projection.InstallActorAggregate(f.producer, producerAdpt, producerRule,
		projectionRevision, h.adjKV, h.coreKV, h.logger, projection.WithGrantChangeSink(f.reprojector)))
	require.True(t, f.producer.HasGrantChangeSink())

	f.producer.RunOn(h.conn, e2eSpec(producerID, "core-kv"))
	producerCtx, producerCancel := context.WithCancel(h.ctx)
	producerDone := make(chan struct{})
	go func() { defer close(producerDone); f.producer.Run(producerCtx) }()
	t.Cleanup(func() { producerCancel(); <-producerDone })

	// The personal pipeline consumes CDC for the seed phase only, then stops —
	// the same posture the grant-change e2e takes, and for the same reason: a
	// live personal consumer would re-evaluate this identity for reasons
	// unrelated to the grant, and every assertion below is about what the grant
	// edge and the healer publish with no event on the lens's own subgraph.
	personalPipe.RunOn(h.conn, e2eSpec(f.personalLens, "core-kv"))
	personalCtx, personalCancel := context.WithCancel(h.ctx)
	personalDone := make(chan struct{})
	go func() { defer close(personalDone); personalPipe.Run(personalCtx) }()
	stopped := false
	f.stopPersonal = func() {
		if stopped {
			return
		}
		stopped = true
		personalCancel()
		<-personalDone
	}
	t.Cleanup(f.stopPersonal)

	writePL2Vertex(t, h, substrate.VertexKey("identity", f.identityID), "identity", map[string]any{"name": "tech"})
	writePL2Vertex(t, h, substrate.VertexKey("lease", f.alphaID), "lease",
		map[string]any{"id": deltaAlphaEntity, "monthlyRent": 1800})
	writePL2Vertex(t, h, substrate.VertexKey("lease", f.betaID), "lease",
		map[string]any{"id": deltaBetaEntity, "monthlyRent": 2400})
	writePL2Link(t, h, "identity", f.identityID, "holds", "lease", f.alphaID)
	writePL2Link(t, h, "identity", f.identityID, "holds", "lease", f.betaID)
	// Alpha is readable from the start; beta's grant is what lands during the
	// measurement.
	writePL2Link(t, h, "identity", f.identityID, "mayRead", "lease", f.alphaID)

	return f
}

// capReadKey is the per-entry grant key the producer writes for one anchor.
func (f *deltaScopeFixture) capReadKey(anchorID string) string {
	return "cap-read.identity." + f.identityID + "." + anchorID
}

// awaitGrant blocks until the producer's own write has landed the anchor's
// cap-read key live.
func (f *deltaScopeFixture) awaitGrant(t *testing.T, anchorID string) {
	t.Helper()
	key := f.capReadKey(anchorID)
	require.Eventually(t, func() bool {
		entry, err := f.h.capKV.Get(f.h.ctx, key)
		if err != nil || entry == nil {
			return false
		}
		var doc struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if json.Unmarshal(entry.Value, &doc) != nil {
			return false
		}
		return !doc.IsDeleted
	}, 20*time.Second, 100*time.Millisecond, "the producer must reach cap-read live for %s", key)
}

// settle brings the fixture to the state every assertion below starts from:
// the personal pipeline holds a real ordering token, alpha's grant has landed,
// every signal it produced has been drained, the personal consumer is stopped,
// and the device's subject is quiet.
func (f *deltaScopeFixture) settle(t *testing.T, cons jetstream.Consumer) {
	t.Helper()
	f.awaitGrant(t, f.alphaID)
	require.Eventually(t, func() bool {
		return f.personal.Progress().LastAppliedSeq > 0
	}, 20*time.Second, 100*time.Millisecond,
		"the personal pipeline must apply its seed events before it can publish a frame at a usable revision")
	f.stopPersonal()
	f.reprojector.Drain(f.h.ctx)
	drainUntilQuiet(t, cons)
	require.Empty(t, drainBriefly(t, cons), "the fixture must be quiet before anything is measured")
}

// upsertKeys is the `key` of every upsert one lens published, which is what a
// scoped publish is measured in.
func upsertKeys(envs []map[string]any, lensID string) []string {
	var out []string
	for _, env := range envs {
		if env["op"] != "upsert" || env["lens"] != lensID {
			continue
		}
		key, _ := env["key"].(string)
		out = append(out, key)
	}
	return out
}

// frames is every keyset frame one lens published, each as its key list.
func frames(envs []map[string]any, lensID string) [][]string {
	var out [][]string
	for _, env := range envs {
		if env["op"] != "keyset" || env["lens"] != lensID {
			continue
		}
		raw, _ := env["keys"].([]any)
		keys := make([]string, 0, len(raw))
		for _, k := range raw {
			s, _ := k.(string)
			keys = append(keys, s)
		}
		out = append(out, keys)
	}
	return out
}

// TestPersonalLensDeltaPublication_T6_GrantPublishesOneAnchorsRow is T6's grant
// vector (personal-lens-delta-publication-design.md §10).
//
// A grant landing for ONE anchor publishes that anchor's row and a frame naming
// everything the actor holds. Both halves are asserted on the wire, and both
// are load-bearing: the withheld row is one the device already holds, and the
// frame is the only thing that stops the client pruning it.
func TestPersonalLensDeltaPublication_T6_GrantPublishesOneAnchorsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	f := newDeltaScopeFixture(t, "t6delta-grant")
	cons := pl3Consumer(t, f.h, f.identityID)
	f.settle(t, cons)

	// Beta's grant lands. Nothing touches the personal lens's own subgraph, and
	// its consumer is stopped — so everything below arrived through the edge.
	writePL2Link(t, f.h, "identity", f.identityID, "mayRead", "lease", f.betaID)
	f.awaitGrant(t, f.betaID)
	require.Eventually(t, func() bool { return f.reprojector.QueueDepth() > 0 }, 15*time.Second, 50*time.Millisecond,
		"a grant landing must enqueue its actor for reprojection")
	f.reprojector.Drain(f.h.ctx)

	published := drainUntilQuiet(t, cons)

	assert.Equal(t, []string{deltaBetaEntity}, upsertKeys(published, f.personalLens),
		"exactly the row whose grant moved is republished; alpha's row is unchanged on the device and is not re-sent")
	got := frames(published, f.personalLens)
	require.Len(t, got, 1, "one authoritative frame per reprojection")
	assert.ElementsMatch(t, []string{deltaAlphaEntity, deltaBetaEntity}, got[0],
		"the frame names everything the actor holds — a key it omitted would be pruned on the device")
}

// TestPersonalLensDeltaPublication_T6_SweepPassPublishesFramesOnly is T6's
// sweep vector: between content cycles the standing healer publishes the
// authoritative frame and no row.
//
// The first cycle after a sweeper is constructed is a CONTENT cycle — a process
// that just started republishes rows once — so this test asserts both: the boot
// cycle carries rows, and the cycle after it, inside the heal interval, carries
// frames alone. Asserting only the second half would pass against a sweeper that
// never published a row at all.
func TestPersonalLensDeltaPublication_T6_SweepPassPublishesFramesOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	f := newDeltaScopeFixture(t, "t6delta-sweep")
	cons := pl3Consumer(t, f.h, f.identityID)
	f.settle(t, cons)

	// The sweep as cmd/refractor starts it, its ticks driven from the test: a
	// free-running sweeper republishes a frame for every identity on every tick,
	// so a drain-until-quiet against one never goes quiet.
	sweeper := grantchange.NewPersonalSweeper(f.reprojector, f.h.coreKV, nil)
	sweeper.SetBounds(100, 0)
	sweepOneDeltaCycle := func(t *testing.T) {
		t.Helper()
		before := sweeper.CycleCompletedAt()
		for range 20 {
			sweeper.Sweep(f.h.ctx)
			if sweeper.CycleCompletedAt().After(before) {
				return
			}
		}
		t.Fatal("the sweep did not close a cycle over the identity population within 20 ticks")
	}

	sweepOneDeltaCycle(t)
	boot := drainUntilQuiet(t, cons)
	assert.Equal(t, []string{deltaAlphaEntity}, upsertKeys(boot, f.personalLens),
		"the first cycle after boot is a content cycle: it republishes the actor's rows")
	require.NotEmpty(t, frames(boot, f.personalLens))

	sweepOneDeltaCycle(t)
	ordinary := drainUntilQuiet(t, cons)
	assert.Empty(t, upsertKeys(ordinary, f.personalLens),
		"a pass inside the content-heal interval publishes no row")
	got := frames(ordinary, f.personalLens)
	require.NotEmpty(t, got, "and still publishes the authoritative frame, which is what the healer exists to re-ask")
	for _, keys := range got {
		assert.Equal(t, []string{deltaAlphaEntity}, keys,
			"the frame names every row the actor may read, so nothing it holds is pruned")
	}
}

// TestPersonalLensDeltaPublication_T6_HydratePublishesEverything is T6's
// hydrate vector: a device attaching gets every row, scope or no scope.
//
// A cold device holds nothing, so the frame alone would leave it empty. Hydrate
// takes no scope for exactly that reason, and this pins it on the wire rather
// than by reading the signature.
func TestPersonalLensDeltaPublication_T6_HydratePublishesEverything(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	f := newDeltaScopeFixture(t, "t6delta-hydrate")
	cons := pl3Consumer(t, f.h, f.identityID)
	f.settle(t, cons)

	// Both anchors readable, so "everything" is more than one row.
	writePL2Link(t, f.h, "identity", f.identityID, "mayRead", "lease", f.betaID)
	f.awaitGrant(t, f.betaID)
	f.reprojector.Drain(f.h.ctx)
	drainUntilQuiet(t, cons)

	_, err := f.personal.Hydrate(f.h.ctx, f.identityID)
	require.NoError(t, err)

	published := drainUntilQuiet(t, cons)
	assert.ElementsMatch(t, []string{deltaAlphaEntity, deltaBetaEntity}, upsertKeys(published, f.personalLens),
		"a hydrate answers a device that holds nothing, so it publishes every row")
	got := frames(published, f.personalLens)
	require.NotEmpty(t, got)
	assert.ElementsMatch(t, []string{deltaAlphaEntity, deltaBetaEntity}, got[len(got)-1])
}
