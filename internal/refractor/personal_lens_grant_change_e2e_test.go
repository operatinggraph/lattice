// Package refractor_test — end-to-end proof for personal-lens-grant-change-
// trigger-design.md Increment 1 (T4). Reuses pl2Harness /
// writePL2Vertex / writePL2Link / pl2NanoID / pl3Consumer / drainUntilQuiet.
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

// grantEdgeFixture is one wired pair: a real cap-read read-grant producer
// driven by real Core KV CDC, and a real Personal Lens gated on the projection
// that producer writes.
type grantEdgeFixture struct {
	h            *pl2Harness
	reprojector  *grantchange.Reprojector
	personalLens string
	personal     *pipeline.Pipeline
	producer     *pipeline.Pipeline
	identityID   string
	leaseID      string
	capReadKey   string
	stopPersonal func()
}

// newGrantEdgeFixture wires the two pipelines. withSink is the mutation switch:
// false installs the producer WITHOUT the grant-change sink, which is the only
// difference between the two halves of T4.
//
// The Personal Lens runs its own Core KV consumer through the SEED phase and is
// then STOPPED, before any grant exists. Both halves of that matter.
//
// Running it first is what gives the pipeline a real ordering token: a pipeline
// that has applied no event holds none, and ReprojectPersonalActor refuses to
// publish a frame at revision 0 because the client discards such a frame
// outright. A fixture that never ran the consumer would be testing that refusal
// instead of the edge.
//
// Stopping it before the grant is what removes the confound. Refractor's
// reaction model is one shared Core-KV CDC subscription, and the link that
// grants a read touches the identity's adjacency — so a live personal consumer
// would re-evaluate that identity for reasons entirely unrelated to the grant
// edge, and the sink-disabled half would pass whether or not the edge exists.
// With it stopped, the ONLY route from "a grant lands in capability-kv" to "a
// frame on the device's SYNC subject" is the mechanism under test.
//
// The combination is also the realistic shape: a device whose lens pipeline has
// been running and holds an ack floor, with no fresh event on its own subgraph
// at the moment its grants change.
func newGrantEdgeFixture(t *testing.T, name string, withSink bool) *grantEdgeFixture {
	t.Helper()
	h := newPL2Harness(t)
	f := &grantEdgeFixture{
		h:            h,
		reprojector:  grantchange.New(),
		personalLens: pl2NanoID(name + "-personal"),
		identityID:   pl2NanoID(name + "-identity"),
		leaseID:      pl2NanoID(name + "-lease"),
	}
	f.capReadKey = "cap-read.identity." + f.identityID + "." + f.leaseID

	// --- the consumer: a Personal Lens gated on the D1 read-grant projection.
	personalAdpt, err := adapter.NewNatsSubjectAdapter(h.ctx, h.conn, f.personalLens,
		"lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	personalRule := &lens.Rule{
		ID:             f.personalLens,
		CanonicalName:  "lens.grant-change-personal",
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

	// --- the producer: a real per-entry cap-read lens, guarded, auth-plane,
	// driven by real Core KV CDC.
	producerID := pl2NanoID(name + "-producer")
	producerRule := &lens.Rule{
		ID:             producerID,
		CanonicalName:  "lens.grant-change-producer",
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
	var opts []projection.InstallOption
	if withSink {
		opts = append(opts, projection.WithGrantChangeSink(f.reprojector))
	}
	require.True(t, projection.InstallActorAggregate(f.producer, producerAdpt, producerRule,
		projectionRevision, h.adjKV, h.coreKV, h.logger, opts...))
	require.Equal(t, withSink, f.producer.HasGrantChangeSink(),
		"the mutation switch must actually change whether the edge is wired")

	f.producer.RunOn(h.conn, e2eSpec(producerID, "core-kv"))
	producerCtx, producerCancel := context.WithCancel(h.ctx)
	producerDone := make(chan struct{})
	go func() { defer close(producerDone); f.producer.Run(producerCtx) }()
	t.Cleanup(func() { producerCancel(); <-producerDone })

	// The personal pipeline consumes CDC for the seed phase only — see this
	// function's doc for why it must run at all, and why it must then stop.
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

	// The identity's own subgraph for the personal lens, seeded before any
	// grant exists.
	writePL2Vertex(t, h, substrate.VertexKey("identity", f.identityID), "identity", map[string]any{"name": "tech"})
	writePL2Vertex(t, h, substrate.VertexKey("lease", f.leaseID), "lease",
		map[string]any{"id": "lease-grant-edge", "monthlyRent": 1800})
	writePL2Link(t, h, "identity", f.identityID, "holds", "lease", f.leaseID)

	return f
}

// settle waits until the personal pipeline has applied CDC (so it holds a real
// ordering token), then stops its consumer and clears the seed traffic off the
// device's subject.
func (f *grantEdgeFixture) settle(t *testing.T, cons jetstream.Consumer) {
	t.Helper()
	require.Eventually(t, func() bool {
		return f.personal.Progress().LastAppliedSeq > 0
	}, 20*time.Second, 100*time.Millisecond,
		"the personal pipeline must apply its seed events before it can publish a frame at a usable revision")
	f.stopPersonal()
	drainUntilQuiet(t, cons)
}

func mustParse(t *testing.T, cypher string) ruleengine.CompiledRule {
	t.Helper()
	cr, err := fullEngineSingleton.Parse(cypher)
	require.NoError(t, err)
	return cr
}

// grant writes the link the producer walks, which is what makes the read grant
// exist. Nothing here touches the personal lens's own subgraph.
func (f *grantEdgeFixture) grant(t *testing.T) {
	t.Helper()
	writePL2Link(t, f.h, "identity", f.identityID, "mayRead", "lease", f.leaseID)
}

// revoke tombstones that same link.
func (f *grantEdgeFixture) revoke(t *testing.T) {
	t.Helper()
	linkKey := substrate.LinkKey("identity", f.identityID, "mayRead", "lease", f.leaseID)
	body, err := json.Marshal(map[string]any{
		"key": linkKey, "class": "mayRead", "isDeleted": true,
		"sourceVertex": substrate.VertexKey("identity", f.identityID),
		"targetVertex": substrate.VertexKey("lease", f.leaseID),
		"localName":    "mayRead",
	})
	require.NoError(t, err)
	_, err = f.h.coreKV.Put(f.h.ctx, linkKey, body)
	require.NoError(t, err)
}

// awaitCapReadLiveness blocks until the producer's own write has landed the
// cap-read key in the state wanted. Waiting on the PRODUCER's output — rather
// than on the personal lens — is what keeps the two halves of T4 comparable:
// both wait for the identical producer-side fact, and only then ask whether
// anything reached the device.
func (f *grantEdgeFixture) awaitCapReadLiveness(t *testing.T, wantLive bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		entry, err := f.h.capKV.Get(f.h.ctx, f.capReadKey)
		if err != nil || entry == nil {
			return !wantLive
		}
		var doc struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if json.Unmarshal(entry.Value, &doc) != nil {
			return false
		}
		return doc.IsDeleted != wantLive
	}, 20*time.Second, 100*time.Millisecond,
		"the producer must reach cap-read live=%v for %s", wantLive, f.capReadKey)
}

// TestPersonalLensGrantChange_T4_GrantLandsAndIsRevoked is the headline test.
//
// A grant lands in the D1 projection and the row appears on the device; the
// grant is withdrawn and the next frame omits the key — with no Core-KV event
// on the personal lens's own subgraph in between, and no device attach.
func TestPersonalLensGrantChange_T4_GrantLandsAndIsRevoked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	f := newGrantEdgeFixture(t, "t4-live", true)
	cons := pl3Consumer(t, f.h, f.identityID)

	// Seed traffic settles, the personal consumer stops, and the device's
	// subject is drained — so anything that arrives after this point arrived
	// because of the grant edge and nothing else.
	f.settle(t, cons)
	require.Empty(t, drainBriefly(t, cons), "no grant yet, so nothing further")

	// --- the growth direction.
	f.grant(t)
	f.awaitCapReadLiveness(t, true)
	require.Eventually(t, func() bool { return f.reprojector.QueueDepth() > 0 }, 15*time.Second, 50*time.Millisecond,
		"a grant landing must enqueue its actor for reprojection")
	f.reprojector.Drain(f.h.ctx)

	granted := drainUntilQuiet(t, cons)
	assert.True(t, frameNames(granted, f.personalLens, "lease-grant-edge"),
		"the grant must produce a frame naming the newly-readable row")
	assert.True(t, upsertHappened(granted, f.personalLens),
		"and the row itself must reach the device")

	// --- the shrink direction, which is the one that fails open.
	f.revoke(t)
	f.awaitCapReadLiveness(t, false)
	require.Eventually(t, func() bool { return f.reprojector.QueueDepth() > 0 }, 15*time.Second, 50*time.Millisecond,
		"a revocation must enqueue its actor too — this half is dead without the delete outcome")
	f.reprojector.Drain(f.h.ctx)

	revoked := drainUntilQuiet(t, cons)
	require.NotEmpty(t, revoked, "the revocation must publish a frame")
	assert.False(t, frameNames(revoked, f.personalLens, "lease-grant-edge"),
		"the frame published after the revocation must OMIT the key, which is what prunes it on the device")
	assert.True(t, sawEmptyFrame(revoked, f.personalLens),
		"an actor who may now read nothing is framed with no keys")
}

// TestPersonalLensGrantChange_T4_MutationSinkDisabled is T4's mutation control,
// kept permanently rather than run once and deleted.
//
// It is byte-identical to the test above except that the producer installs
// without the grant-change sink. If it ever passes alongside a green T4, T4 is
// passing for some reason other than the mechanism it claims to test.
func TestPersonalLensGrantChange_T4_MutationSinkDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	f := newGrantEdgeFixture(t, "t4-mutation", false)
	cons := pl3Consumer(t, f.h, f.identityID)
	f.settle(t, cons)
	require.Empty(t, drainBriefly(t, cons))

	f.grant(t)
	f.awaitCapReadLiveness(t, true)
	f.reprojector.Drain(f.h.ctx)

	assert.Zero(t, f.reprojector.QueueDepth(),
		"with no sink installed, a real grant transition must enqueue nothing")
	assert.Empty(t, drainBriefly(t, cons),
		"without the edge the device hears nothing about a grant that landed — this is the bug the edge closes, and T4's proof that it tests the edge")

	f.revoke(t)
	f.awaitCapReadLiveness(t, false)
	f.reprojector.Drain(f.h.ctx)

	assert.Empty(t, drainBriefly(t, cons),
		"and nothing about one that was withdrawn")
}

// drainBriefly is drainUntilQuiet with a short first wait, for the NEGATIVE
// assertions: those wait out a window in which nothing should arrive, and the
// producer-side fact they follow (awaitCapReadLiveness) has already been
// observed, so the long first wait drainUntilQuiet spends on fan-out lag buys
// nothing but wall clock.
func drainBriefly(t *testing.T, cons jetstream.Consumer) []map[string]any {
	t.Helper()
	var envs []map[string]any
	wait := 3 * time.Second
	for {
		msg, err := cons.Next(jetstream.FetchMaxWait(wait))
		if err != nil {
			return envs
		}
		var env map[string]any
		require.NoError(t, json.Unmarshal(msg.Data(), &env))
		envs = append(envs, env)
		wait = time.Second
	}
}

// frameNames reports whether any keyset frame for lens names key.
func frameNames(envs []map[string]any, lens, key string) bool {
	for _, env := range envs {
		if env["op"] != "keyset" || env["lens"] != lens {
			continue
		}
		keys, _ := env["keys"].([]any)
		for _, k := range keys {
			if k == key {
				return true
			}
		}
	}
	return false
}

// sawEmptyFrame reports whether lens published a frame carrying no keys.
func sawEmptyFrame(envs []map[string]any, lens string) bool {
	for _, env := range envs {
		if env["op"] != "keyset" || env["lens"] != lens {
			continue
		}
		if keys, _ := env["keys"].([]any); len(keys) == 0 {
			return true
		}
	}
	return false
}

func upsertHappened(envs []map[string]any, lens string) bool {
	for _, env := range envs {
		if env["op"] == "upsert" && env["lens"] == lens {
			return true
		}
	}
	return false
}

// TestPersonalLensGrantChange_T6_SweepConvergesWithoutTheEdge is Increment 2's
// headline (design §10, T6): with the notification edge absent, a flipped grant
// still converges — within one sweep cycle — because the personal plane now has
// the standing healer it never had.
//
// The fixture is T4's own MUTATION control, reused deliberately: the producer
// installs with no grant-change sink, so nothing enqueues, nothing drains, and
// the assertions T4's mutation half makes about silence are exactly what this
// test starts from. The only thing added is the sweeper. That is what makes the
// claim honest — the sweep is not being credited for a frame the fast path
// published, because in this fixture the fast path is not wired at all.
//
// It is also the crash case, faithfully: a signal lost with the process is
// indistinguishable, from the sweeper's side, from a signal never emitted.
func TestPersonalLensGrantChange_T6_SweepConvergesWithoutTheEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	f := newGrantEdgeFixture(t, "t6-sweep", false)
	cons := pl3Consumer(t, f.h, f.identityID)
	f.settle(t, cons)
	require.Empty(t, drainBriefly(t, cons), "no grant yet, so nothing further")

	// The sweep as cmd/refractor starts it, with the batch widened to cover the
	// harness's whole identity population in one tick. Its ticks are driven from
	// the test rather than by its own ticker, and not for speed: an
	// authoritative frame is republished for every swept identity on every tick,
	// by construction, so a drain-until-quiet against a free-running sweeper
	// never goes quiet and could not tell the frame under test from the next
	// tick's. The ticker is unit-tested; what this proves is that ONE CYCLE
	// converges, which is exactly T6's claim.
	// nil health lister: this test is about the sweep converging a cycle, and
	// the instance census it would otherwise perform only feeds the derivation
	// licence, which this fixture does not assert.
	sweeper := grantchange.NewPersonalSweeper(f.reprojector, f.h.coreKV, nil)
	sweeper.SetBounds(100, 0)
	sweepOneCycle := func(t *testing.T) {
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

	// --- the growth direction: a grant lands with nothing to announce it.
	f.grant(t)
	f.awaitCapReadLiveness(t, true)
	require.Zero(t, f.reprojector.QueueDepth(),
		"the edge is not wired in this fixture — anything that reaches the device below reached it through the sweep")
	require.Empty(t, drainBriefly(t, cons),
		"and nothing reaches the device on its own: this is the staleness window the sweep exists to close")

	sweepOneCycle(t)
	granted := drainUntilQuiet(t, cons)
	assert.True(t, frameNames(granted, f.personalLens, "lease-grant-edge"),
		"the sweep must publish a frame naming the newly-readable row, with no signal and no event on the lens's own subgraph")
	assert.True(t, upsertHappened(granted, f.personalLens),
		"and the row itself must reach the device")

	// --- the shrink direction, the one that fails open.
	f.revoke(t)
	f.awaitCapReadLiveness(t, false)
	require.Zero(t, f.reprojector.QueueDepth(), "still no edge, still only the sweep")

	sweepOneCycle(t)
	revoked := drainUntilQuiet(t, cons)
	require.NotEmpty(t, revoked, "the sweep must publish a frame after the revocation")
	assert.False(t, frameNames(revoked, f.personalLens, "lease-grant-edge"),
		"the frame the sweep publishes after the revocation must OMIT the key, which is what prunes it on the device")
	assert.True(t, sawEmptyFrame(revoked, f.personalLens),
		"an actor who may now read nothing is framed with no keys")
}
