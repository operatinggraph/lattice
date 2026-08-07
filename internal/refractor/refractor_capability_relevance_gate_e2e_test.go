// Auth-plane relevance-gate e2e — auth-plane-projection-latency-design.md
// Increment 1, acceptance items (a) and (b).
//
// The three fan-out arms skip an event whose vertex types capabilityRoles'
// patterns provably cannot bind, gated by the §4.2 conjunction. That gate is a
// narrowing on the write-side authorization surface, so the thing that must be
// proven end-to-end is that nothing it narrows away is load-bearing: a grant
// appears on AssignRole, and — the over-grant direction, which is the one that
// matters — a revocation and an actor soft-delete still retract.
//
// The gate arms here because the pipeline is installed through the REAL
// projection.InstallActorAggregate, not field-by-field: pattern-closure and the
// convergence sweep are two of the conjuncts, and both are decisions that
// install makes. The sibling capability e2es wire their pipelines by hand and so
// run broad — which is the fail-closed default doing its job, not a gap.
package refractor_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// stableGateID returns a deterministic NanoID for a relevance-gate fixture.
func stableGateID(role string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 14695981039346656037
	for _, b := range []byte("relevancegate:" + role) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

// capabilityRolesRuleForTest builds a lens.Rule around rbac-domain's real
// capabilityRoles spec and §6.13 descriptor, so InstallActorAggregate takes the
// same decisions it takes in production for this exact lens.
func capabilityRolesRuleForTest(t *testing.T, id string) *lens.Rule {
	t.Helper()
	spec := capabilityRolesSpecForTest(t)
	require.NotNil(t, spec.Output, "capabilityRoles must declare a §6.13 Output descriptor")

	cr, err := full.New().Parse(spec.Spec)
	require.NoError(t, err, "capabilityRoles spec must parse")

	return &lens.Rule{
		ID:             id,
		CanonicalName:  spec.CanonicalName,
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into: lens.IntoConfig{
			Target: "nats_kv",
			Bucket: bootstrap.CapabilityKVBucket,
			Key:    lens.KeyField{"key"},
		},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       spec.Output.AnchorType,
			OutputKeyPattern: spec.Output.OutputKeyPattern,
			BodyColumns:      spec.Output.BodyColumns,
			EmptyBehavior:    spec.Output.EmptyBehavior,
			RealnessFilter:   spec.Output.RealnessFilter,
			Freshness:        spec.Output.Freshness,
			ActorField:       spec.Output.ActorField,
			Lanes:            spec.Output.Lanes,
		},
	}
}

// TestRefractor_CapabilityLens_RelevanceGate_GrantAndRetraction_E2E is
// acceptance (a) and (b): with the actor-aware relevance gate armed, AssignRole
// still projects the actor's grant, a revocation still retracts it, and an actor
// soft-delete still deletes the actor's cap.roles.* key.
func TestRefractor_CapabilityLens_RelevanceGate_GrantAndRetraction_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping relevance-gate capability e2e test in -short mode")
	}

	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	const rolesLensID = "RevGateRbacRoLes9977"
	rule := capabilityRolesRuleForTest(t, rolesLensID)

	adpt, err := adapter.New(capabilityKV, rule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)

	p, err := pipeline.New(rule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(full.New(), rule.CompiledRule)

	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	require.True(t,
		projection.InstallActorAggregate(p, adpt, rule, projectionRevision, adjKV, coreKV, logger),
		"capabilityRoles must install through the real gate")

	// The whole conjunction, asserted against the DERIVED label set rather than
	// assumed. Without this the grant/retraction assertions below would re-prove
	// the broad path the sibling e2es already cover, and would keep passing
	// byte-identically if a cypher edit or a ReferencedLabels regression made
	// capabilityRoles non-exhaustive — the fail-safe direction, but silent.
	require.True(t, p.PatternClosedOutput(),
		"an actor-aggregate install must declare pattern-closed output")
	require.NotNil(t, p.Sweeper(),
		"narrowing requires a standing healer — capabilityRoles must enrol in the convergence sweep")
	labels, eligible := p.ActorAwareNarrowingLabels()
	require.True(t, eligible,
		"the real capabilityRoles spec, installed through the real gate, must be eligible to narrow")
	require.Equal(t,
		map[string]struct{}{"identity": {}, "role": {}, "permission": {}}, labels,
		"the gate must judge against the label set the compiled rule actually derives")

	p.RunOn(conn, e2eSpec(rule.ID, bootstrap.CoreKVBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); p.Run(pipelineCtx) }()
	t.Cleanup(func() {
		pipelineCancel()
		<-done
	})

	identityID := stableGateID("actor")
	roleID := stableGateID("role")
	permID := stableGateID("perm")

	identityKey := substrate.VertexKey("identity", identityID)
	roleKey := substrate.VertexKey("role", roleID)
	permKey := substrate.VertexKey("permission", permID)

	const provenanceAt = "2026-08-02T10:00:00Z"
	writeVertex := func(key, class string, isDeleted bool, extra map[string]any) {
		body := map[string]any{
			"key":            key,
			"class":          class,
			"isDeleted":      isDeleted,
			"createdAt":      provenanceAt,
			"lastModifiedAt": provenanceAt,
			"data":           extra,
		}
		raw, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
	}
	writeLink := func(srcType, srcID, name, dstType, dstID string, isDeleted bool) {
		linkKey := substrate.LinkKey(srcType, srcID, name, dstType, dstID)
		raw, jerr := json.Marshal(map[string]any{
			"key":          linkKey,
			"class":        name,
			"isDeleted":    isDeleted,
			"sourceVertex": substrate.VertexKey(srcType, srcID),
			"targetVertex": substrate.VertexKey(dstType, dstID),
			"localName":    name,
		})
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, linkKey, raw)
		require.NoError(t, perr)
	}

	writeVertex(roleKey, "role", false, map[string]any{"canonicalName": "author"})
	writeVertex(permKey, "permission", false, map[string]any{
		"operationType": "create", "scope": "book"})
	writeVertex(identityKey, "identity", false, map[string]any{"name": "gate-actor"})

	capKey := "cap.roles.identity." + identityID
	capDoc := func() (map[string]any, bool) {
		entry, gErr := capabilityKV.Get(ctx, capKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return nil, false
		}
		var env map[string]any
		if jerr := json.Unmarshal(entry.Value, &env); jerr != nil {
			return nil, false
		}
		return env, true
	}
	// Contract #6 §6.8 absence = denial. A GUARDED auth-plane lens expresses
	// that absence as an isDeleted tombstone rather than a hard delete, so the
	// projectionSeq ordering token survives the retraction — asserting only on
	// ErrKeyNotFound would read a correct retraction as a failure.
	capDenied := func() bool {
		entry, gErr := capabilityKV.Get(ctx, capKey)
		if errors.Is(gErr, substrate.ErrKeyNotFound) || entry == nil || len(entry.Value) == 0 {
			return true
		}
		var env map[string]any
		if jerr := json.Unmarshal(entry.Value, &env); jerr != nil {
			return false
		}
		deleted, _ := env["isDeleted"].(bool)
		return deleted
	}
	hasCreateBook := func() bool {
		env, ok := capDoc()
		if !ok {
			return false
		}
		perms, _ := env["platformPermissions"].([]any)
		for _, e := range perms {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if m["operationType"] == "create" && m["scope"] == "book" {
				return true
			}
		}
		return false
	}

	require.Eventually(t, capDenied, 20*time.Second, 100*time.Millisecond,
		"no role held yet — absence is denial (Contract #6 §6.8)")

	// (a) AssignRole. Both link events carry endpoint types inside the lens's
	// label set, so the gate keeps the fan-out and the grant must appear.
	writeLink("permission", permID, "grantedBy", "role", roleID, false)
	writeLink("identity", identityID, "holdsRole", "role", roleID, false)

	require.Eventually(t, hasCreateBook, 20*time.Second, 100*time.Millisecond,
		"the actor's grant must still project under the relevance gate")

	// (b1) RevokeRole. A missed retraction is an over-grant, which is the
	// failure direction narrowing could plausibly introduce.
	writeLink("identity", identityID, "holdsRole", "role", roleID, true)
	require.Eventually(t, capDenied, 20*time.Second, 100*time.Millisecond,
		"revocation must still retract the actor's cap row under the relevance gate")

	// (b2) Actor soft-delete. The anchorType ∈ labels conjunct exists precisely
	// so this vertex event is never filtered away: it is what deletes the key.
	writeLink("identity", identityID, "holdsRole", "role", roleID, false)
	require.Eventually(t, hasCreateBook, 20*time.Second, 100*time.Millisecond,
		"the actor must regain its grant before the soft-delete leg")

	writeVertex(identityKey, "identity", true, map[string]any{"name": "gate-actor"})
	require.Eventually(t, capDenied, 20*time.Second, 100*time.Millisecond,
		"an actor soft-delete must still delete its cap.roles.* key under the relevance gate")
}

// waitGateConsumerSettled polls the named durable until it has drained past
// throughStreamSeq, and returns the settled info. Delivered.Consumer is a
// per-consumer sequence, so a write the filter excludes never advances it, which
// is what makes a delta an exact tally of what the lens was HANDED rather than of
// what it wrote.
//
// The settle condition is Delivered.Stream >= throughStreamSeq, and NumPending ==
// 0 alone is deliberately NOT it. ConsumerInfo reports pending from the
// incrementally maintained o.npc, bumped by the stream's asynchronous signal loop
// which pushes AFTER the publish is acked (nats-server v2.14.0
// server/consumer.go checkNumPending / processStreamSignal, server/stream.go's
// sigq push). So pending can read 0 while a stored, matching, unsignalled message
// is still undelivered — and a count read there could come from the wrong
// messages and pass. Naming a stream sequence to drain THROUGH turns the caller's
// fence from an assertion into something the server answers: a single consumer's
// delivery is one ordered walk of the stream, so once the fence's own sequence has
// been delivered, every earlier matching message has been too.
//
// Pass 0 for throughStreamSeq to take a baseline with no fence to wait on.
func waitGateConsumerSettled(t *testing.T, conn *substrate.Conn, durable string, throughStreamSeq uint64) *jetstream.ConsumerInfo {
	t.Helper()
	var settled *jetstream.ConsumerInfo
	require.Eventually(t, func() bool {
		cons, err := conn.JetStream().Consumer(context.Background(),
			subjects.CoreKVStream(bootstrap.CoreKVBucket), durable)
		if err != nil {
			return false
		}
		info, err := cons.Info(context.Background())
		if err != nil || info.NumPending != 0 {
			return false
		}
		if info.Delivered.Stream < throughStreamSeq {
			return false
		}
		settled = info
		return true
	}, 20*time.Second, 100*time.Millisecond,
		"consumer %q never drained through stream seq %d", durable, throughStreamSeq)
	return settled
}

// TestRefractor_CapabilityLens_NarrowedFilter_UnrelatedWriteNeverDelivered_E2E is
// acceptance (c): with the eligible actor-aware pipeline's consumer narrowed
// server-side, an unrelated business write is never DELIVERED to capabilityRoles
// at all — Term A, the term that made auth-plane latency depend on whatever else
// the graph happened to be doing.
//
// It asserts on the consumer's delivery count rather than on the absence of a
// capability write. A client-side relevance skip already produces the absence of
// a write; what the narrowed consumer adds is that the event never costs the lens
// a queue slot, and only the server's own counter can see that.
func TestRefractor_CapabilityLens_NarrowedFilter_UnrelatedWriteNeverDelivered_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping narrowed-filter capability e2e test in -short mode")
	}

	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	const rolesLensID = "NrwGateRbacRoLes8811"
	rule := capabilityRolesRuleForTest(t, rolesLensID)

	adpt, err := adapter.New(capabilityKV, rule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)

	p, err := pipeline.New(rule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(full.New(), rule.CompiledRule)

	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	require.True(t,
		projection.InstallActorAggregate(p, adpt, rule, projectionRevision, adjKV, coreKV, logger),
		"capabilityRoles must install through the real gate")

	// The server-side filter is derived from the SAME predicate the fan-out arms
	// consult, so assert that identity rather than assuming it — a second
	// derivation drifting from the client gate is the one way this narrowing could
	// stop being conservative.
	filterLabels, filterEligible := p.NarrowedFilterEligible()
	require.True(t, filterEligible,
		"the real capabilityRoles spec must be eligible for a narrowed consumer")
	require.Equal(t,
		map[string]struct{}{"identity": {}, "role": {}, "permission": {}}, filterLabels,
		"delivery must be filtered on the label set the compiled rule actually derives")

	filterSubjects, filterSubject := p.ConsumerFilter()
	require.Empty(t, filterSubject,
		"the real capabilityRoles spec must narrow, not fall back to the broad filter")
	require.Len(t, filterSubjects, 9,
		"three labels expand to 3 forms each — label-narrowed, never relation-narrowed")

	spec := e2eSpec(rule.ID, bootstrap.CoreKVBucket)
	spec.FilterSubject = filterSubject
	spec.FilterSubjects = filterSubjects
	p.RunOn(conn, spec)

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); p.Run(pipelineCtx) }()
	t.Cleanup(func() {
		pipelineCancel()
		<-done
	})

	identityID := stableGateID("narrow-actor")
	roleID := stableGateID("narrow-role")
	permID := stableGateID("narrow-perm")
	bookingID := stableGateID("narrow-booking")
	unitID := stableGateID("narrow-unit")

	const provenanceAt = "2026-08-03T10:00:00Z"
	// Returns the write's revision, which for a NATS KV bucket IS its stream
	// sequence — what lets a later write be used as a drain fence.
	putVertex := func(typ, id string, data map[string]any) uint64 {
		key := substrate.VertexKey(typ, id)
		raw, jerr := json.Marshal(map[string]any{
			"key": key, "class": typ, "isDeleted": false,
			"createdAt": provenanceAt, "lastModifiedAt": provenanceAt, "data": data,
		})
		require.NoError(t, jerr)
		rev, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
		return rev
	}
	putLink := func(srcType, srcID, name, dstType, dstID string) uint64 {
		key := substrate.LinkKey(srcType, srcID, name, dstType, dstID)
		raw, jerr := json.Marshal(map[string]any{
			"key": key, "class": name, "isDeleted": false,
			"sourceVertex": substrate.VertexKey(srcType, srcID),
			"targetVertex": substrate.VertexKey(dstType, dstID),
			"localName":    name,
		})
		require.NoError(t, jerr)
		rev, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
		return rev
	}

	putVertex("role", roleID, map[string]any{"canonicalName": "narrow-author"})
	putVertex("permission", permID, map[string]any{"operationType": "create", "scope": "ledger"})
	putVertex("identity", identityID, map[string]any{"name": "narrow-actor"})
	putLink("permission", permID, "grantedBy", "role", roleID)
	putLink("identity", identityID, "holdsRole", "role", roleID)

	capKey := "cap.roles.identity." + identityID
	hasCreateLedger := func() bool {
		entry, gErr := capabilityKV.Get(ctx, capKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if jerr := json.Unmarshal(entry.Value, &env); jerr != nil {
			return false
		}
		perms, _ := env["platformPermissions"].([]any)
		for _, e := range perms {
			m, ok := e.(map[string]any)
			if ok && m["operationType"] == "create" && m["scope"] == "ledger" {
				return true
			}
		}
		return false
	}
	require.Eventually(t, hasCreateLedger, 20*time.Second, 100*time.Millisecond,
		"the grant must still project through a narrowed consumer")

	durable := subjects.LensDurable(rule.ID)
	settledBefore := waitGateConsumerSettled(t, conn, durable, 0)
	baseline := settledBefore.Delivered.Consumer

	// What the SERVER actually registered, not what this test asked for.
	// registerWithFilterFallback deliberately retries a rejected narrowed filter
	// with the broad one, so a derivation JetStream refuses degrades silently by
	// design; without this read-back that degradation would surface only as a
	// confusing count below.
	require.ElementsMatch(t, filterSubjects, settledBefore.Config.FilterSubjects,
		"the live durable must carry the narrowed set — a broad fallback means the derivation was rejected")

	// The unrelated business traffic: a vertex of a type the patterns cannot
	// bind, an aspect on it, and a link with NEITHER endpoint in the label set.
	// One of each key shape, because the three fan-out arms are three separate
	// filter forms.
	putVertex("booking", bookingID, map[string]any{"state": "confirmed"})
	aspectKey := substrate.VertexKey("booking", bookingID) + ".details"
	aspectRaw, err := json.Marshal(map[string]any{
		"key": aspectKey, "class": "details", "isDeleted": false,
		"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
		"data": map[string]any{"seats": 2},
	})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, aspectKey, aspectRaw)
	require.NoError(t, err)
	putVertex("unit", unitID, map[string]any{"name": "narrow-unit"})
	putLink("booking", bookingID, "forUnit", "unit", unitID)

	// A link with exactly ONE endpoint in the label set must still be delivered,
	// and its relation (bookedBy) is one this lens never traverses. So this single
	// write pins both halves of the alignment: the filter set pins each label in
	// both the source and the target position precisely because the link arm skips
	// only when NEITHER endpoint can bind, and it must NOT pin the relation,
	// because that arm has no relation gate to be conservative against.
	putLink("identity", identityID, "bookedBy", "booking", bookingID)

	// A fence: one in-label write published AFTER all the business traffic.
	// JetStream delivers a single consumer's messages in stream order, so once the
	// fence has been delivered and the consumer has settled, any business write
	// that was going to be delivered already would have been. That is what makes
	// the count below a proof rather than a race with a slow delivery.
	fenceSeq := putVertex("permission", permID, map[string]any{
		"operationType": "create", "scope": "ledger", "note": "fence"})

	settled := waitGateConsumerSettled(t, conn, durable, fenceSeq)

	// The half-in-label link was DELIVERED, so the fan-out ran on it. Delivery is
	// only half the claim: re-projecting an event that touches the anchor over a
	// relation the pattern never walks must leave the grant exactly as it was.
	require.True(t, hasCreateLedger(),
		"the delivered out-of-pattern link must not have disturbed the projected grant")

	require.EqualValues(t, baseline+2, settled.Delivered.Consumer,
		"exactly two deliveries: the half-in-label identity→booking link and the fence — the booking vertex, its aspect, the unit vertex and the booking→unit link must never have cost this lens a queue slot")
}
