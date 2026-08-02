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
