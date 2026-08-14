package pipeline

// The plain arm's own affected-anchor derivation (Increment 2 of
// plain-lens-neighbour-anchor-derivation-design.md), mirroring
// anchor_derivation_internal_test.go's structure for the actor-aware arm:
// driven against a real adjacency graph, every refusal paired with a
// positive vector so "refused" is never indistinguishable from "the harness
// never reached the code" (§11's own instruction).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// plainDerivationPipeline builds a PLAIN (no ActorEnumerator, no envelope)
// pipeline over spec, sharing the ActorEnumerator fixtures' adjacency bucket
// helper — deriveAnchorsForPlain* reads only adjKV, never coreKV, so the
// lighter fixture (no embedded target/adapter) is enough here.
func plainDerivationPipeline(t *testing.T, adjKV *substrate.KV, spec string) *Pipeline {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	p := &Pipeline{ruleID: "testPlainLens", adjKV: adjKV}
	p.UseFullEngine(eng, cr)
	return p
}

// providerSpec is the clinicProviders shape reduced to its 1-hop skeleton:
// anchor `provider`, one OPTIONAL hop to a neighbour `identity`.
// The row projects the neighbour's DATA, not only its key: a property
// mutation on a key-only projection moves no row at all (the same trap
// rolesDataSpec's own doc names in anchor_derivation_differential_test.go),
// which would make a differential test pass over an empty ground truth no
// matter what the derivation returns.
const providerSpec = `
MATCH (pr:provider)
OPTIONAL MATCH (pr)-[:identifiedBy]->(id:identity)
RETURN pr.key AS key, id.key AS identityKey, id.data.name AS identityName
`

// providerOrgLocationSpec is a 2-hop plain shape: anchor `provider`, through
// `org` to `location` — the walk crosses one adjacency document per hop.
const providerOrgLocationSpec = `
MATCH (pr:provider)
OPTIONAL MATCH (pr)-[:employedBy]->(org:org)-[:locatedIn]->(loc:location)
RETURN pr.key AS key, loc.key AS locKey, loc.data.city AS city
`

// TestDeriveAnchorsForPlainVertex_OneHop is the clinicProviders payoff traced
// at the vertex-derivation level (§4.2's worked example): the root's own
// event is the zero-read fast path, and a neighbour vertex event walks
// exactly one adjacency document back to the root.
func TestDeriveAnchorsForPlainVertex_OneHop(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("id1", "identity")
	f.edge("identifiedBy", "pr1", "id1")

	p := plainDerivationPipeline(t, adjKV, providerSpec)
	rs := p.ruleState()
	require.True(t, rs.rootHops.Complete,
		"a 1-hop clinicProviders-shaped lens must be scan-root-indexable: %s", rs.rootHops.Incomplete)

	ctx := context.Background()
	derived, ok, err := p.deriveAnchorsForPlainVertex(ctx, rs, f.key("pr1", "provider"), "provider")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{f.key("pr1", "provider")}, derived,
		"the anchor's own event derives itself with no adjacency read")

	derived, ok, err = p.deriveAnchorsForPlainVertex(ctx, rs, f.key("id1", "identity"), "identity")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{f.key("pr1", "provider")}, derived,
		"a neighbour vertex event walks back to its provider")
}

// TestDeriveAnchorsForPlainAspect_DerivesThroughItsParent mirrors
// TestDeriveAnchors_VertexAndAspectEventsSeedTheSamePosition for the plain
// arm: an aspect event derives through its PARENT vertex's position, never
// around it.
func TestDeriveAnchorsForPlainAspect_DerivesThroughItsParent(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("id1", "identity")
	f.edge("identifiedBy", "pr1", "id1")

	p := plainDerivationPipeline(t, adjKV, providerSpec)
	rs := p.ruleState()
	ctx := context.Background()

	want := []string{f.key("pr1", "provider")}
	derived, ok, err := p.deriveAnchorsForPlainAspect(ctx, rs, f.key("id1", "identity")+".profile")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, derived)
}

// TestDeriveAnchorsForPlainLink_AnchorSideCollapsesToTheRoot is §4.2's own
// worked payoff: on the link event itself, AnchorSideSeeds returns the
// root-side seed alone (ds=0 <= dd=1), so the derived set is exactly the
// provider — with zero adjacency reads, matching the design's claim that the
// neighbour endpoint's work "collapses into a duplicate of the first's".
func TestDeriveAnchorsForPlainLink_AnchorSideCollapsesToTheRoot(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("id1", "identity")
	f.edge("identifiedBy", "pr1", "id1")

	p := plainDerivationPipeline(t, adjKV, providerSpec)
	rs := p.ruleState()
	ctx := context.Background()

	derived, ok, err := p.deriveAnchorsForPlainLink(ctx, rs, f.link("identifiedBy", "pr1", "provider", "id1", "identity"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{f.key("pr1", "provider")}, derived)
}

// TestDeriveAnchorsForPlainVertex_TwoHop proves the walk steps more than one
// adjacency document when the pattern does.
func TestDeriveAnchorsForPlainVertex_TwoHop(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("org1", "org")
	f.vertex("loc1", "location")
	f.edge("employedBy", "pr1", "org1")
	f.edge("locatedIn", "org1", "loc1")

	p := plainDerivationPipeline(t, adjKV, providerOrgLocationSpec)
	rs := p.ruleState()
	require.True(t, rs.rootHops.Complete, "2-hop shape must be indexable: %s", rs.rootHops.Incomplete)
	ctx := context.Background()

	derived, ok, err := p.deriveAnchorsForPlainVertex(ctx, rs, f.key("loc1", "location"), "location")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{f.key("pr1", "provider")}, derived,
		"a two-hop neighbour event must walk both adjacency documents back to the root")
}

// TestDeriveAnchorsForPlainVertex_UntraversedRelationDerivesEmpty mirrors
// TestDeriveAnchors_UntraversedRelationDerivesEmpty: an empty set with ok ==
// true is a real answer on a complete index, not a refusal.
func TestDeriveAnchorsForPlainVertex_UntraversedRelationDerivesEmpty(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("booking", "booking")
	f.edge("bookedBy", "booking", "pr1")

	p := plainDerivationPipeline(t, adjKV, providerSpec)
	rs := p.ruleState()
	ctx := context.Background()

	derived, ok, err := p.deriveAnchorsForPlainVertex(ctx, rs, f.key("booking", "booking"), "booking")
	require.NoError(t, err)
	require.True(t, ok, "an indexable lens answers rather than refusing")
	require.Empty(t, derived, "providerSpec never traverses bookedBy, so no anchor's output can change")
}

// TestDeriveAnchorsForPlain_ReadCapFallsBackRatherThanTruncating is the plain
// arm's own mirror of TestDeriveAnchors_ReadCapFallsBackRatherThanTruncating
// (anchor_derivation_internal_test.go): DefaultDerivationReadCap applies to
// the plain walk unchanged (walkToAnchors is reused verbatim), and an
// exhausted budget must fall back — never return the partial set with ok ==
// true. Seeded at the two-hop-out location, the walk reads loc1's own
// adjacency (reaching org1) then org1's (reaching pr1) — two documents — so
// two is exactly enough and one is exactly not.
func TestDeriveAnchorsForPlain_ReadCapFallsBackRatherThanTruncating(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("org1", "org")
	f.vertex("loc1", "location")
	f.edge("employedBy", "pr1", "org1")
	f.edge("locatedIn", "org1", "loc1")

	p := plainDerivationPipeline(t, adjKV, providerOrgLocationSpec)
	rs := p.ruleState()
	require.True(t, rs.rootHops.Complete, "%s", rs.rootHops.Incomplete)
	ctx := context.Background()

	p.SetAnchorDerivationReadCap(2)
	derived, ok, err := p.deriveAnchorsForPlainVertex(ctx, rs, f.key("loc1", "location"), "location")
	require.NoError(t, err)
	require.True(t, ok, "two reads are enough for this walk")
	require.Equal(t, []string{f.key("pr1", "provider")}, derived)

	p.SetAnchorDerivationReadCap(1)
	derived, ok, err = p.deriveAnchorsForPlainVertex(ctx, rs, f.key("loc1", "location"), "location")
	require.NoError(t, err)
	require.False(t, ok, "an exhausted budget must fall back, never return the partial set")
	require.Nil(t, derived, "a refusal carries no anchors at all — not the one it had already found")
}

// TestDeriveAnchorsForPlainLink_AnchorSideSeedCostsZeroAdjacencyReads is
// §4.2's headline payoff, proved on the READ COUNT rather than only on the
// derived SET (a wrong derivation could coincidentally produce the right set
// while still reading adjacency it need not have). It deletes the ADJ bucket
// out from under the pipeline immediately before the anchor-side derivation
// runs, so ANY adjacency read at all would surface as an error — and confirms
// two things in the same breath: the anchor-side (provider) endpoint's
// derivation still succeeds despite the bucket being gone (proving it
// performed zero reads), while the SAME now-broken bucket makes the
// neighbour-side (identity) endpoint's derivation fail (proving that
// endpoint genuinely does need at least one read, so the comparison is not
// vacuous).
func TestDeriveAnchorsForPlainLink_AnchorSideSeedCostsZeroAdjacencyReads(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ADJZERO"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "ADJZERO")
	require.NoError(t, err)

	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("id1", "identity")
	f.edge("identifiedBy", "pr1", "id1")

	p := plainDerivationPipeline(t, adjKV, providerSpec)
	rs := p.ruleState()
	require.True(t, rs.rootHops.Complete, "%s", rs.rootHops.Incomplete)

	linkKey := f.link("identifiedBy", "pr1", "provider", "id1", "identity")

	require.NoError(t, js.DeleteKeyValue(ctx, "ADJZERO"),
		"delete the bucket AFTER seeding it, so any read from here on genuinely errors")

	derived, ok, err := p.deriveAnchorsForPlainLink(ctx, rs, linkKey)
	require.NoError(t, err, "the anchor-side seed must not touch adjacency at all")
	require.True(t, ok)
	require.Equal(t, []string{f.key("pr1", "provider")}, derived)

	_, ok, err = p.deriveAnchorsForPlainVertex(ctx, rs, f.key("id1", "identity"), "identity")
	require.Error(t, err, "the neighbour-side derivation DOES need an adjacency read, "+
		"so the deleted bucket must make it fail — otherwise the comparison above proves nothing")
	require.False(t, ok)
}

// TestPlainDerivationIndex_Conjuncts pins every refusal item A lists, each
// paired with a positive vector so a refusal is never indistinguishable from
// the harness never reaching the code.
func TestPlainDerivationIndex_Conjuncts(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("id1", "identity")
	f.edge("identifiedBy", "pr1", "id1")

	t.Run("positive vector: a plain single-branch lens with a complete root index is ready", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, providerSpec)
		_, ready := p.plainDerivationIndex(p.ruleState())
		require.True(t, ready)
	})

	t.Run("an ActorEnumerator installed refuses — this is not a plain pipeline", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, providerSpec)
		p.SetActorEnumerator(NewActorEnumerator(adjKV, nil, "provider"))
		_, ready := p.plainDerivationIndex(p.ruleState())
		require.False(t, ready)
	})

	t.Run("an envelope installed refuses", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, providerSpec)
		p.SetEnvelopeFn(func(row, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
			return row, keys, nil
		})
		_, ready := p.plainDerivationIndex(p.ruleState())
		require.False(t, ready)
	})

	t.Run("diffRetraction refuses — a per-anchor row set would read as every other anchor gone", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, providerSpec)
		// Set directly rather than via SetDiffRetraction: that setter refuses
		// enabling without a KeyLister-capable adapter, which this bare
		// fixture has none of — irrelevant to the conjunct under test.
		p.diffRetraction = true
		_, ready := p.plainDerivationIndex(p.ruleState())
		require.False(t, ready)
	})

	t.Run("more than one branch refuses — one graph cannot speak for N walks", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, providerSpec)
		rs := p.ruleState()
		rs.branches = make([]ruleengine.CompiledRule, 2)
		_, ready := p.plainDerivationIndex(rs)
		require.False(t, ready)
	})

	t.Run("an incomplete rootHops refuses — a variable-length hop cannot be stepped", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, `
MATCH (pr:provider)
OPTIONAL MATCH (pr)-[:employedBy*0..]->(org:org)
RETURN pr.key AS key, org.key AS orgKey
`)
		rs := p.ruleState()
		require.False(t, rs.rootHops.Complete)
		_, ready := p.plainDerivationIndex(rs)
		require.False(t, ready)
	})

	t.Run("an unresolved `*` expansion position refuses — pruning would under-approximate", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, providerSpec)
		rs := p.ruleState()
		require.True(t, rs.rootHops.Complete)
		unresolved := rs.rootHops
		unresolved.LabelExpand = make([]bool, len(unresolved.Labels))
		unresolved.LabelExpand[unresolved.Anchor] = true
		unresolved.Expanded = nil
		rs.rootHops = unresolved
		require.GreaterOrEqual(t, rs.rootHops.UnresolvedExpansionPosition(), 0)
		_, ready := p.plainDerivationIndex(rs)
		require.False(t, ready)
	})
}

// TestPlainDerivationIndexForAct_AlwaysDeclinesThisFire pins the shadow-only
// invariant in code: the act-mode gate can never answer ready, on any lens,
// including the licensed-shape positive vector below. (The licence itself is
// answerable and answers true for such a lens — see
// TestPlainDerivationLicence_DoesNotReachTheActGate, which pins the two facts
// together.)
func TestPlainDerivationIndexForAct_AlwaysDeclinesThisFire(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("id1", "identity")
	f.edge("identifiedBy", "pr1", "id1")

	p := plainDerivationPipeline(t, adjKV, providerSpec)
	rs := p.ruleState()
	require.True(t, rs.rootHops.Complete, "positive vector: a shape the licence would otherwise consider")

	_, ready := p.plainDerivationIndexForAct(rs)
	require.False(t, ready, "acting is not wired; act mode must never decide a plain lens's event")
}

// TestPlainDerivedAnchorCap_ZeroMeansUnset mirrors
// TestDeriveAnchors_ReadCapZeroMeansUnset's zero-value contract for the new
// cap: non-positive restores the default at both the per-pipeline and the
// package level.
func TestPlainDerivedAnchorCap_ZeroMeansUnset(t *testing.T) {
	p := &Pipeline{ruleID: "capTest"}
	require.Equal(t, DefaultPlainDerivedAnchorCap, p.plainDerivedAnchorCap())
	p.SetPlainDerivedAnchorCap(-1)
	require.Equal(t, DefaultPlainDerivedAnchorCap, p.plainDerivedAnchorCap())
	p.SetPlainDerivedAnchorCap(0)
	require.Equal(t, DefaultPlainDerivedAnchorCap, p.plainDerivedAnchorCap())

	p.SetPlainDerivedAnchorCap(7)
	require.Equal(t, 7, p.plainDerivedAnchorCap())
	p.SetPlainDerivedAnchorCap(0)
	require.Equal(t, DefaultPlainDerivedAnchorCap, p.plainDerivedAnchorCap())
}

// TestPlainDerivedAnchorCap_PackageDefault pins the process-wide override
// SetDefaultPlainDerivedAnchorCap governs, mirroring how the derivation MODE
// package default is tested elsewhere — cleaned up so it cannot leak into
// another test in the same binary.
func TestPlainDerivedAnchorCap_PackageDefault(t *testing.T) {
	t.Cleanup(func() { SetDefaultPlainDerivedAnchorCap(0) })
	SetDefaultPlainDerivedAnchorCap(9)

	p := &Pipeline{ruleID: "capTest"}
	require.Equal(t, 9, p.plainDerivedAnchorCap(), "the package default applies with no per-pipeline override")

	p.SetPlainDerivedAnchorCap(3)
	require.Equal(t, 3, p.plainDerivedAnchorCap(), "a per-pipeline override wins over the package default")
}

// TestShadowPlainDerivation_RecordsSampledAndDeclined mirrors
// TestShadow_SamplingAndDeclineAreBothCounted for the plain arm: a lens the
// derivation cannot resolve is still a sampled event.
func TestShadowPlainDerivation_RecordsSampledAndNotReady(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := plainDerivationPipeline(t, adjKV, `
MATCH (pr:provider)
OPTIONAL MATCH (pr)-[:employedBy*0..]->(org:org)
RETURN pr.key AS key, org.key AS orgKey
`)
	p.SetAnchorDerivationSampling(1)
	rs := p.ruleState()
	require.False(t, rs.rootHops.Complete)

	p.shadowPlainDerivation(rs, func() ([]string, bool, error) {
		t.Fatal("an incomplete index must be refused before the walk is attempted")
		return nil, false, nil
	})
	st := p.AnchorDerivationShadow()
	require.Equal(t, int64(1), st.Sampled)
	require.Equal(t, int64(1), st.PlainNotReady)
	require.Zero(t, st.PlainWalkDeclined)
	require.Zero(t, st.PlainOverCap)
	require.Zero(t, st.DerivedAnchors)

	// Sampling off means no observation at all, not a zero-valued one.
	p.SetAnchorDerivationSampling(-1)
	p.shadowPlainDerivation(p.ruleState(), func() ([]string, bool, error) {
		return nil, true, nil
	})
	require.Equal(t, int64(1), p.AnchorDerivationShadow().Sampled)
}

// TestShadowPlainDerivation_RecordsWalkDeclined pins the SECOND decline
// cause apart from the first: an index that IS ready but whose walk itself
// declines (here, DefaultDerivationReadCap exhaustion on the two-hop shape,
// which needs two adjacency reads to reach the root from the location) must
// land in PlainWalkDeclined, not PlainNotReady — an operator reading the
// tally needs to tell "no index at all" from "the walk ran out of budget"
// apart.
func TestShadowPlainDerivation_RecordsWalkDeclined(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("org1", "org")
	f.vertex("loc1", "location")
	f.edge("employedBy", "pr1", "org1")
	f.edge("locatedIn", "org1", "loc1")

	p := plainDerivationPipeline(t, adjKV, providerOrgLocationSpec)
	p.SetAnchorDerivationSampling(1)
	p.SetAnchorDerivationReadCap(1) // one read is not enough for this two-hop walk
	rs := p.ruleState()
	require.True(t, rs.rootHops.Complete)
	ctx := context.Background()

	p.shadowPlainDerivation(rs, func() ([]string, bool, error) {
		return p.deriveAnchorsForPlainVertex(ctx, rs, f.key("loc1", "location"), "location")
	})
	st := p.AnchorDerivationShadow()
	require.Equal(t, int64(1), st.Sampled)
	require.Zero(t, st.PlainNotReady)
	require.Equal(t, int64(1), st.PlainWalkDeclined)
	require.Zero(t, st.PlainOverCap)
	require.Zero(t, st.DerivedAnchors)
}

// TestShadowPlainDerivation_RecordsDerivedSetSize is §11's measurement,
// pinned as a test: the shadow records the derived-SET SIZE, in the SAME
// shared counters the actor-aware arm's shadow uses (DerivedAnchors), never a
// comparison against a second answer — a plain lens's shipped behaviour has
// no enumerated anchor-key list to diff against.
func TestShadowPlainDerivation_RecordsDerivedSetSize(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("id1", "identity")
	f.edge("identifiedBy", "pr1", "id1")

	p := plainDerivationPipeline(t, adjKV, providerSpec)
	p.SetAnchorDerivationSampling(1)
	rs := p.ruleState()
	ctx := context.Background()

	p.shadowPlainDerivation(rs, func() ([]string, bool, error) {
		return p.deriveAnchorsForPlainVertex(ctx, rs, f.key("id1", "identity"), "identity")
	})
	st := p.AnchorDerivationShadow()
	require.Equal(t, int64(1), st.Sampled)
	require.Zero(t, st.PlainNotReady)
	require.Zero(t, st.PlainWalkDeclined)
	require.Zero(t, st.PlainOverCap)
	require.Equal(t, int64(1), st.DerivedAnchors, "one derived anchor (the provider) for this sampled event")
}

// TestShadowPlainDerivation_CapFallbackRecordsItsOwnSize is the shadow half
// of §4.2's new cap: a derived set larger than the cap is a fallback, tallied
// under PlainOverCap/PlainOverCapSize rather than DerivedAnchors — but the
// SIZE that triggered it is still recorded, never dropped, since folding it
// into a bare declined-with-no-size would truncate the derived-set-size
// distribution exactly at the cap it exists to justify.
func TestShadowPlainDerivation_CapFallbackRecordsItsOwnSize(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("pr1", "provider")
	f.vertex("pr2", "provider")
	f.vertex("id1", "identity")
	f.edge("identifiedBy", "pr1", "id1")
	f.edge("identifiedBy", "pr2", "id1")

	p := plainDerivationPipeline(t, adjKV, providerSpec)
	p.SetAnchorDerivationSampling(1)
	p.SetPlainDerivedAnchorCap(1)
	rs := p.ruleState()
	ctx := context.Background()

	p.shadowPlainDerivation(rs, func() ([]string, bool, error) {
		return p.deriveAnchorsForPlainVertex(ctx, rs, f.key("id1", "identity"), "identity")
	})
	st := p.AnchorDerivationShadow()
	require.Equal(t, int64(1), st.Sampled)
	require.Zero(t, st.PlainNotReady)
	require.Zero(t, st.PlainWalkDeclined)
	require.Equal(t, int64(1), st.PlainOverCap, "a derived set of 2 anchors exceeds the cap of 1")
	require.Equal(t, int64(2), st.PlainOverCapSize, "the over-cap SIZE must still be recorded, not dropped")
	require.Zero(t, st.DerivedAnchors, "an over-cap event must not also count toward the under-cap mean")
}

// TestEvaluatePlainDerivedAnchors_DedupesAcrossAnchors proves obligation (i):
// two derived anchors whose seeded evaluations produce the identical result
// row collapse to one written row.
func TestEvaluatePlainDerivedAnchors_DedupesAcrossAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (pr:provider)
RETURN "shared" AS key, "row" AS v
`)
	require.NoError(t, err)
	p := &Pipeline{ruleID: "dedupeTest", coreKV: coreKV, adjKV: adjKV}
	p.UseFullEngine(eng, cr)
	rs := p.ruleState()
	ctx := context.Background()

	writeCollisionVertex(t, coreKV, "vtx.provider.AAAAAAAAAAAAAAAAAAAA", "provider", nil)
	writeCollisionVertex(t, coreKV, "vtx.provider.BBBBBBBBBBBBBBBBBBBB", "provider", nil)

	results, err := p.evaluatePlainDerivedAnchors(ctx, rs,
		[]string{"vtx.provider.AAAAAAAAAAAAAAAAAAAA", "vtx.provider.BBBBBBBBBBBBBBBBBBBB"}, "provider")
	require.NoError(t, err)
	require.Len(t, results, 1, "both anchors project the identically-keyed row; dedupe must collapse them")
}

// TestEvaluatePlainDerivedAnchors_FirstErrorAbortsTheWholeEvent proves
// obligation (ii): the widening is explicit — a failure on the SECOND derived
// anchor discards whatever the first anchor already produced, matching the
// link arm's shipped all-or-nothing disposition.
func TestEvaluatePlainDerivedAnchors_FirstErrorAbortsTheWholeEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (pr:provider)
RETURN pr.key AS key
`)
	require.NoError(t, err)
	p := &Pipeline{ruleID: "abortTest", coreKV: coreKV, adjKV: adjKV}
	p.UseFullEngine(eng, cr)
	rs := p.ruleState()
	ctx := context.Background()

	goodKey := "vtx.provider.AAAAAAAAAAAAAAAAAAAA"
	badKey := "vtx.provider.BBBBBBBBBBBBBBBBBBBB"
	writeCollisionVertex(t, coreKV, goodKey, "provider", nil)
	// A vertex body with neither lastModifiedAt nor createdAt: projectedAt
	// cannot be derived (ErrNoProvenanceTimestamp), the same failure any
	// live malformed-write would produce.
	raw, merr := json.Marshal(map[string]any{"key": badKey, "class": "provider", "isDeleted": false, "data": map[string]any{}})
	require.NoError(t, merr)
	_, perr := coreKV.Put(ctx, badKey, raw)
	require.NoError(t, perr)

	_, err = p.evaluatePlainDerivedAnchors(ctx, rs, []string{goodKey, badKey}, "provider")
	require.Error(t, err, "the second anchor's evaluation failure must abort the whole call")
}

// TestEvaluatePlainNeighbourEvent_NoModeChangesTheResultThisFire is the
// fire's own invariant, pinned directly against evaluatePlainNeighbourEvent
// rather than only inferred from the regression suite staying green: for
// EVERY mode — including `act`, which is builtinDerivationMode
// (anchor_derivation_mode.go) and therefore what a live Refractor runs today
// with no env var set — and an out-of-range mode, the returned results are
// byte-identical to calling executeFullForActor with an empty seed directly,
// with a genuinely indexable (and therefore, once acting is wired,
// act-eligible) lens shape. `act` stays a no-op only
// because plainDerivationIndexForAct always declines
// (TestPlainDerivationIndexForAct_AlwaysDeclinesThisFire pins that
// separately); this test proves the outcome, not just the gate.
func TestEvaluatePlainNeighbourEvent_NoModeChangesTheResultThisFire(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	cr, err := eng.Parse(providerSpec)
	require.NoError(t, err)
	p := &Pipeline{ruleID: "invariantTest", coreKV: coreKV, adjKV: adjKV}
	p.UseFullEngine(eng, cr)
	rs := p.ruleState()
	require.True(t, rs.rootHops.Complete)
	ctx := context.Background()

	prKey := "vtx.provider.AAAAAAAAAAAAAAAAAAAA"
	idKey := "vtx.identity.BBBBBBBBBBBBBBBBBBBB"
	writeCollisionVertex(t, coreKV, prKey, "provider", nil)
	idProps := writeAndReturnVertex(t, coreKV, idKey, "identity", nil)
	buildCollisionEdge(t, adjKV, "identifiedBy", "provider", "AAAAAAAAAAAAAAAAAAAA", "identity", "BBBBBBBBBBBBBBBBBBBB")

	entry := ruleengine.NodeEntry{CoreKVKey: idKey, NodeLabel: "identity", Properties: idProps}

	for _, mode := range []DerivationMode{
		DerivationModeOff, DerivationModeShadow, DerivationModeAct, DerivationMode(99),
	} {
		p.SetAnchorDerivationMode(mode)
		p.SetAnchorDerivationSampling(1)

		got, gerr := p.evaluatePlainNeighbourEvent(ctx, rs, entry)
		require.NoError(t, gerr)
		want, werr := p.executeFullForActor(ctx, rs, entry.CoreKVKey, entry.Properties, "")
		require.NoError(t, werr)

		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		require.JSONEq(t, string(wantJSON), string(gotJSON), "mode %v must not change the outcome — shadow decides nothing", mode)
	}
}

// writeAndReturnVertex writes a Contract #1 vertex body and returns its
// decoded properties map, for a caller that needs both the CDC payload shape
// and the properties evaluateForEntryRaw expects on ruleengine.NodeEntry.
func writeAndReturnVertex(t *testing.T, coreKV *substrate.KV, key, class string, data map[string]any) map[string]any {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	props := map[string]any{
		"key": key, "class": class, "isDeleted": false,
		"createdAt": "2026-05-15T10:00:00Z", "lastModifiedAt": "2026-05-15T10:00:00Z",
		"data": data,
	}
	raw, err := json.Marshal(props)
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return props
}
