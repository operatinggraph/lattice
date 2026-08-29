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
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
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

	t.Run("an incomplete rootHops refuses — an untyped hop cannot be indexed by relation name", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, `
MATCH (pr:provider)
OPTIONAL MATCH (pr)-[]->(org:org)
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

// TestPlainDerivationIndexForAct_RequiresBothTheIndexAndTheLicence pins the act
// gate as the conjunction it is: §4.2's index must be ready AND §5's licence
// must admit the lens. Each half gets its own negative, and both sit beside a
// positive — a gate that declined every lens outright would satisfy either
// negative on its own and prove nothing about the conjunction.
func TestPlainDerivationIndexForAct_RequiresBothTheIndexAndTheLicence(t *testing.T) {
	t.Run("positive vector: an indexable, licensed lens is admitted", func(t *testing.T) {
		f := licenceFixture(t, seedUnitsSpec)
		rs := f.p.ruleState()
		idx, ready, licenceRefusal := f.p.plainDerivationIndexForAct(rs)
		require.True(t, ready)
		require.Equal(t, rs.rootHops, idx,
			"the gate hands back the lens's own scan-root index, never a second derivation of it")
		require.Empty(t, licenceRefusal, "an admitted lens carries no refusal to report")
	})

	t.Run("an indexable lens the licence refuses is declined", func(t *testing.T) {
		adjKV := newActorEnumeratorAdjKV(t)
		f := newEnumFixture(t, adjKV)
		f.vertex("pr1", "provider")
		f.vertex("id1", "identity")
		f.edge("identifiedBy", "pr1", "id1")

		p := plainDerivationPipeline(t, adjKV, providerSpec)
		rs := p.ruleState()
		_, indexReady := p.plainDerivationIndex(rs)
		require.True(t, indexReady,
			"the INDEX half must hold here, or the refusal below says nothing about the licence")
		licensed, refusal := p.plainDerivationLicence(rs)
		require.False(t, licensed)
		require.Contains(t, refusal, "no divergence audit is enrolled")

		_, ready, licenceRefusal := p.plainDerivationIndexForAct(rs)
		require.False(t, ready, "an unlicensed lens keeps today's unseeded whole-corpus rescan")
		require.Equal(t, refusal, licenceRefusal,
			"the gate hands the licence's own reason back rather than making its caller recompute it")
	})

	t.Run("a licensed lens the index refuses is declined", func(t *testing.T) {
		f := licenceFixture(t, seedUnitsSpec)
		rs := f.p.ruleState()
		licensed, refusal := f.p.plainDerivationLicence(rs)
		require.True(t, licensed, "the LICENCE half must hold here; refusal: %s", refusal)

		rs.branches = make([]ruleengine.CompiledRule, 2)
		_, ready, licenceRefusal := f.p.plainDerivationIndexForAct(rs)
		require.False(t, ready, "a multi-walk lens has no single derived set for a licence to admit")
		require.Empty(t, licenceRefusal,
			"the index refused first, so the licence was never asked and has nothing to report")
	})
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
OPTIONAL MATCH (pr)-[]->(org:org)
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

// TestEvaluatePlainNeighbourEvent_AnUnlicensedLensIsModeIndependent pins the
// blast radius of the act flip on the OUTCOME rather than only on the gate: a
// lens §5's licence refuses returns byte-identical results under every mode —
// `off`, `shadow`, `act` (which is builtinDerivationMode, anchor_derivation_mode.go,
// and therefore what a live Refractor runs with no env var set) and an
// out-of-range one — namely what calling executeFullForActor with an empty seed
// returns.
//
// The lens shape is genuinely INDEXABLE, asserted below, so the invariant is
// carried by the licence alone and not by a lens the walk could never have
// derived from. The refusal is asserted too: without that, a future change that
// licensed this fixture would leave the test comparing the derived path against
// itself and reporting green.
func TestEvaluatePlainNeighbourEvent_AnUnlicensedLensIsModeIndependent(t *testing.T) {
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
	_, indexReady := p.plainDerivationIndex(rs)
	require.True(t, indexReady, "an unindexable lens would carry this invariant for the wrong reason")
	licensed, refusal := p.plainDerivationLicence(rs)
	require.False(t, licensed)
	require.NotEmpty(t, refusal)
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
		require.JSONEq(t, string(wantJSON), string(gotJSON),
			"mode %v must not change an unlicensed lens's outcome", mode)
	}
}

// probeUnitNever is the anchor of the §6 probe's negative arm: a unit whose
// listing aspect never existed, so seedUnitsSpec's WHERE has never once matched
// it and the target holds no row for it.
const probeUnitNever = "PrbunitNeverrrrrrrrr"

// TestEvaluatePlainDerivedAnchors_ZeroRowDeleteProbe is §6's presence probe,
// pinned as a pair on ONE lens and ONE evaluation: both anchors below have just
// dropped out of the matched set, and the only thing that differs between them
// is whether the target actually holds a row.
//
// The first half of the test is the more important one, and it pins the probe's
// PLACEMENT: the genuine top-level anchor event goes through
// evaluatePlainFromVertex — exactly as the vertex, aspect and link arms reach it
// — and must emit its Delete for an anchor with no live row, because that is
// evaluateForEntryRaw's own idempotent filter-retraction and every plain lens
// depends on it, licensed or not. The probe belongs to the walk-derived re-entry
// alone. Sited in evaluate.go's general retraction check instead, it would
// quietly narrow retraction for the whole plain corpus — and this half is what
// would fail.
func TestEvaluatePlainDerivedAnchors_ZeroRowDeleteProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	f := newAuditFixture(t, seedUnitsSpec, nil)
	ctx := context.Background()

	// The anchor with a LIVE row: projected through the lens's own write path,
	// then dropped out of the matched set by tombstoning the aspect its WHERE
	// reads.
	liveKey := f.project(t, auditUnitA, "Loft A", 1)
	require.NotZero(t, targetRevision(t, f.targetKV, liveKey),
		"the positive arm needs a row that really exists")
	putBody(t, f.coreKV, liveKey+".listing",
		aspectBody(liveKey, "listing", map[string]any{"status": "active"}, true))

	// The anchor that never projected at all.
	neverKey := "vtx.unit." + probeUnitNever
	seedVertexBody(t, f.coreKV, neverKey, "unit", nil)
	_, err := f.targetKV.Get(ctx, neverKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"the negative arm needs an anchor the target has no row for")

	rs := f.p.ruleState()

	// The genuine top-level anchor event, un-probed and unchanged: BOTH anchors
	// retract, the never-projected one included — an idempotent Delete against
	// an absent key, which is what this path has always emitted.
	for _, anchorKey := range []string{liveKey, neverKey} {
		results, verr := f.p.evaluatePlainFromVertex(ctx, rs, anchorKey, "unit")
		require.NoError(t, verr)
		require.Len(t, results, 1, "anchor %s", anchorKey)
		require.True(t, results[0].Delete, "anchor %s", anchorKey)
		require.Equal(t, anchorKey, results[0].Keys["key"])
	}

	// The walk-derived re-entry, probed: the live row's retraction survives, and
	// the never-projected anchor's is dropped rather than manufacturing a
	// tombstone for a row that never existed.
	derived, err := f.p.evaluatePlainDerivedAnchors(ctx, rs, []string{liveKey, neverKey}, "unit")
	require.NoError(t, err)
	require.Len(t, derived, 1, "exactly one of the two Deletes may survive the presence probe")
	require.True(t, derived[0].Delete)
	require.Equal(t, liveKey, derived[0].Keys["key"],
		"the surviving Delete must be the one whose row the target actually holds")
}

// erringRowReader is a target that CAN be asked for a row and always fails to
// answer — the transient read fault the §6 probe must decline on rather than
// read as a confirmed absence. Written by delegation rather than embedding,
// matching notARowReader (audit_enrolment_test.go), so its surface is exactly
// the Adapter interface plus the one RowReader method under test.
type erringRowReader struct {
	inner adapter.Adapter
	err   error
}

func (a erringRowReader) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	return a.inner.Upsert(ctx, keys, row, seq)
}
func (a erringRowReader) Delete(ctx context.Context, keys map[string]any, seq uint64) error {
	return a.inner.Delete(ctx, keys, seq)
}
func (a erringRowReader) Probe(ctx context.Context) error { return a.inner.Probe(ctx) }
func (a erringRowReader) Close() error                    { return a.inner.Close() }
func (a erringRowReader) GetRow(context.Context, map[string]any) (map[string]any, bool, error) {
	return nil, false, a.err
}

// TestDerivedRowIsLive_DeclinesWhatItCannotConfirm walks all four answers the
// probe can give. Only a positively confirmed live row earns a retraction; the
// other three all withhold it, and the whole point of the test is that they do
// so in TWO different ways.
//
// A confirmed absence and a target that cannot be asked at all are facts about
// the ROW: they answer (false, nil), the caller drops the Delete, and the event
// acks. An unanswerable READ is not a fact about anything — it says the probe
// could not tell — so it answers an error, which its caller makes the event's
// outcome rather than acking a retraction it never resolved.
//
// The counter separates the first two afterwards. A confirmed absence and a
// failed read leave the target in precisely the same state, so without
// PlainProbeUnreadable a target having a bad minute would be indistinguishable
// from a lens with nothing to retract. The two arms that are facts about the row
// must NOT move that counter, which is asserted here too, or it would report
// noise instead of faults.
func TestDerivedRowIsLive_DeclinesWhatItCannotConfirm(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	f := newAuditFixture(t, seedUnitsSpec, nil)
	ctx := context.Background()
	liveKey := f.project(t, auditUnitA, "Loft A", 1)

	live, err := f.p.derivedRowIsLive(ctx, map[string]any{"key": liveKey})
	require.NoError(t, err)
	require.True(t, live, "positive vector: a row the lens really wrote must read live")

	live, err = f.p.derivedRowIsLive(ctx, map[string]any{"key": "vtx.unit." + probeUnitNever})
	require.NoError(t, err, "a confirmed absence is an ANSWER, so it must not fail the event")
	require.False(t, live, "an absent row must not earn a Delete")
	require.Zero(t, f.p.AnchorDerivationShadow().PlainProbeUnreadable,
		"a probe the target ANSWERED is not an unreadable one, whichever way it answered")

	readErr := errors.New("target unavailable")
	require.NoError(t, f.p.HotReloadInto(erringRowReader{inner: f.p.currentAdapter(), err: readErr}))
	live, err = f.p.derivedRowIsLive(ctx, map[string]any{"key": liveKey})
	require.ErrorIs(t, err, readErr,
		"an unanswerable probe cannot decide a retraction, so it must fail the event rather than drop one")
	require.False(t, live)
	require.Equal(t, int64(1), f.p.AnchorDerivationShadow().PlainProbeUnreadable,
		"and it is counted, because redelivery alone never makes a target's bad minute visible")

	require.NoError(t, f.p.HotReloadInto(notARowReader{inner: f.p.currentAdapter()}))
	live, err = f.p.derivedRowIsLive(ctx, map[string]any{"key": liveKey})
	require.NoError(t, err, "a lens-shape fact the licence already excludes is not a read fault to fail an event on")
	require.False(t, live, "a target that cannot be asked answers no, never yes")
	require.Equal(t, int64(1), f.p.AnchorDerivationShadow().PlainProbeUnreadable,
		"a target with no read-back at all is a lens-shape fact the licence already excludes, not a read fault")
}

// TestEvaluatePlainDerivedAnchors_UnreadableProbeFailsTheEvent is the
// disposition that finding rests on, at the level the disposition actually
// matters: the caller's. §6's probe fires on a non-anchor-incident neighbour
// event, which for the derived anchor it names is a ONE-SHOT event — nothing
// re-derives that anchor later the way an actor's own next event would. So a
// Delete dropped on an unreadable probe has no second chance and leaves a
// permanent orphan, while the same evaluation's upsert half would have been
// retried. The error is what makes the two halves symmetric: it propagates out
// of evaluateForEntryRaw and Naks (dispositionEvalErr), so the event comes back.
//
// The positive vector is the same probe on the same lens one line earlier, with
// a target that CAN answer: without it a green error here could equally come
// from an evaluation that fails on everything.
func TestEvaluatePlainDerivedAnchors_UnreadableProbeFailsTheEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	f := newAuditFixture(t, seedUnitsSpec, nil)
	ctx := context.Background()

	// One anchor with a live row, dropped out of the matched set — so the derived
	// re-entry produces a Delete the probe has to rule on.
	liveKey := f.project(t, auditUnitA, "Loft A", 1)
	putBody(t, f.coreKV, liveKey+".listing",
		aspectBody(liveKey, "listing", map[string]any{"status": "active"}, true))
	rs := f.p.ruleState()

	derived, err := f.p.evaluatePlainDerivedAnchors(ctx, rs, []string{liveKey}, "unit")
	require.NoError(t, err, "positive vector: a target that can answer resolves the retraction")
	require.Len(t, derived, 1)
	require.True(t, derived[0].Delete)

	readErr := errors.New("target unavailable")
	require.NoError(t, f.p.HotReloadInto(erringRowReader{inner: f.p.currentAdapter(), err: readErr}))

	derived, err = f.p.evaluatePlainDerivedAnchors(ctx, rs, []string{liveKey}, "unit")
	require.ErrorIs(t, err, readErr,
		"an unresolvable retraction must become the EVENT's outcome — acking here drops it forever")
	require.Nil(t, derived,
		"and no partial result may be returned, or the write path would ack the half it did resolve")
}

// TestNoteStaticPlainDerivationRefusal_NamesTheGoverningConjunct pins what an
// operator actually reads when a lens cannot act. The switch has to answer with
// the conjunct that GOVERNS — the one the gate itself refused on — and the gate
// asks the index before the licence, so an index conjunct must win even while a
// licence refusal is also in hand.
func TestNoteStaticPlainDerivationRefusal_NamesTheGoverningConjunct(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := plainDerivationPipeline(t, adjKV, providerSpec)

	staticRefusal := func() string {
		p.derivShadow.mu.Lock()
		defer p.derivShadow.mu.Unlock()
		return p.derivShadow.staticRefusal
	}

	// The licence branch: every §4.2 index conjunct holds on this lens, so the
	// reason is the string the gate handed over, verbatim and unrecomputed.
	rs := p.ruleState()
	_, ready, licenceRefusal := p.plainDerivationIndexForAct(rs)
	require.False(t, ready)
	require.Contains(t, licenceRefusal, "no divergence audit is enrolled")
	p.noteStaticPlainDerivationRefusal(rs, licenceRefusal)
	require.Equal(t, licenceRefusal, staticRefusal())

	// An index conjunct takes precedence over it, with the same licence refusal
	// still supplied: the index is what the gate refused on, so that is what the
	// operator is told.
	multiWalk := rs
	multiWalk.branches = make([]ruleengine.CompiledRule, 2)
	p.noteStaticPlainDerivationRefusal(multiWalk, licenceRefusal)
	require.Equal(t, "it is a multi-walk lens, and one scan-root graph cannot speak for N independent queries",
		staticRefusal())

	// The default arm, posed as the ONLY state that reaches it. An empty
	// licenceRefusal on a gate that answered "not ready" cannot mean the licence
	// refused — every plainDerivationLicence refusal carries a reason — so it
	// means the INDEX refused and the licence was never asked. Reaching the
	// default from there needs that index conjunct to have moved back before the
	// note was written, which is the live-field race, and it is driven here rather
	// than simulated by handing the note an empty string it could not have got.
	p.SetActorEnumerator(&ActorEnumerator{})
	_, ready, raceRefusal := p.plainDerivationIndexForAct(rs)
	require.False(t, ready, "an actor-aware pipeline has no plain derived set to license")
	require.Empty(t, raceRefusal, "the index refused first, so the licence was never asked for a reason")

	p.SetActorEnumerator(nil)
	p.noteStaticPlainDerivationRefusal(rs, raceRefusal)
	require.Contains(t, staticRefusal(), "an index conjunct it reads live",
		"the empty refusal came from the INDEX half, so blaming a licence conjunct would misdirect the operator")
	require.Contains(t, staticRefusal(), "moved between the act gate's answer and this one")
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

// multiPositionAnchorSpec is a SYNTHETIC shape, NOT a mirror of any live
// lens (plain-lens-neighbour-anchor-derivation-design.md's build note: the
// real identity-hygiene duplicateCandidates sets DiffRetraction, so it
// never seeds at all, and identity-domain's identityCredentialBindingsRead
// binds the same label at two positions but every column is key-derived, so
// no property change at the far position can ever move its row). It exists
// to exercise §4.4's gap directly: the SAME label, identity, bound at TWO
// pattern positions — `b`, the anchor position seedAnchorFor's engine-level
// seed can narrow to, and `a`, a position no engine-level seed can ever
// reach. The row's own value comes from `a`, so an event on the vertex
// playing that role is the shape §4.4 exists for.
const multiPositionAnchorSpec = `
MATCH (b:identity)-[:duplicateOf]->(a:identity)
RETURN b.key AS key, a.key AS dupOf, a.data.name AS dupName
`

// TestSeedMultiPosition is a negative test paired with a positive vector
// (§11's own instruction): the shape whose anchor label binds a second
// position reports true, and the ordinary single-position shape — the
// overwhelming majority of the corpus — reports false, so a lens this
// predicate does not concern stays on today's narrow seeded call unchanged.
func TestSeedMultiPosition(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)

	t.Run("positive vector: the anchor label bound at a second position reports true", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, multiPositionAnchorSpec)
		rs := p.ruleState()
		require.True(t, rs.rootHops.Complete, "must be scan-root-indexable: %s", rs.rootHops.Incomplete)
		idx, ready := p.plainDerivationIndex(rs)
		require.True(t, ready)
		require.Len(t, idx.PositionsBinding("identity"), 2, "both b and a bind the identity label")
		require.True(t, p.seedMultiPosition(rs, "identity"))
	})

	t.Run("the common shape: an anchor label bound at ONE position reports false", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, providerSpec)
		rs := p.ruleState()
		require.False(t, p.seedMultiPosition(rs, "provider"),
			"clinicProviders' anchor label binds only the anchor position")
	})

	t.Run("a type the pattern never binds at all reports false", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, multiPositionAnchorSpec)
		rs := p.ruleState()
		require.False(t, p.seedMultiPosition(rs, "provider"))
	})

	t.Run("a not-ready index reports false rather than guessing", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, multiPositionAnchorSpec)
		p.SetActorEnumerator(NewActorEnumerator(adjKV, nil, "identity"))
		rs := p.ruleState()
		_, ready := p.plainDerivationIndex(rs)
		require.False(t, ready)
		require.False(t, p.seedMultiPosition(rs, "identity"))
	})
}

// TestIsPlainDerivedAnchorReentry pins the reentrancy marker
// evaluatePlainDerivedAnchors sets and evaluateForEntryRaw's seeded-multi-
// position dispatch reads — the seam that keeps a multi-position anchor
// label (multiPositionAnchorSpec's own shape) from recursing forever
// through its own derived anchors.
func TestIsPlainDerivedAnchorReentry(t *testing.T) {
	require.False(t, isPlainDerivedAnchorReentry(context.Background()),
		"an ordinary context is never marked")
	marked := context.WithValue(context.Background(), plainDerivedAnchorReentryKey{}, true)
	require.True(t, isPlainDerivedAnchorReentry(marked))
}
