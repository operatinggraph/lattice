package pipeline

// useFullEngineBranches' taxonomy-expansion wiring (dynamic-type-taxonomy-
// design.md §14 Fire A item 3): the resolver is consulted only when a
// compiled rule's ExpansionLabels() is non-empty (inertness), a set-known-
// but-not-armed answer degrades the filter to broad while the compiled rule
// still matches the resolved types (§4.2's fork), and a set-UNKNOWN answer
// refuses activation outright rather than publish a rule state that could
// project the wrong row set.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// taxID pads base into a valid Contract #1 20-char NanoID, sanitizing away
// keys.Alphabet's excluded I/l/O/0 first — a descriptive base like "cycle"
// spells out a lowercase 'l', which the real alphabet forbids.
func taxID(base string) string {
	repl := strings.NewReplacer("I", "J", "l", "L", "O", "Q", "0", "9")
	id := repl.Replace(base)
	for len(id) < 20 {
		id += "A"
	}
	if len(id) > 20 {
		id = id[:20]
	}
	if !keys.IsValidNanoID(id) {
		panic("taxonomy_expansion_internal_test: taxID produced an invalid NanoID: " + id)
	}
	return id
}

// TestUseFullEngine_NoSigil_NeverConsultsResolver pins the inertness
// guarantee: a lens with no `*` pattern derives exactly the labels it does
// today, and does so even with a resolver installed that would refuse (via
// StatusUnknown) any label it was actually asked to expand — the only way
// this test can pass is if the resolver is never consulted at all.
func TestUseFullEngine_NoSigil_NeverConsultsResolver(t *testing.T) {
	eng := full.New()
	spec := `MATCH (u:unit)-[:managedBy]->(o:owner) RETURN u.key AS key, o.name AS ownerName`
	cr, err := eng.Parse(spec)
	require.NoError(t, err)

	p, err := New("inert-no-sigil", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)

	// An unloaded resolver answers StatusUnknown to ANY Expand call. If
	// useFullEngineBranches ever consulted it despite there being no `*`
	// pattern in spec, activation would fail below.
	p.SetTaxonomyResolver(taxonomy.New())

	require.NoError(t, p.UseFullEngine(eng, cr))

	wantLabels, wantExhaustive := cr.(*full.CompiledRule).ReferencedLabels()
	require.True(t, wantExhaustive)

	rs := p.ruleState()
	require.False(t, rs.reprojectAll)
	require.Equal(t, wantLabels, rs.reprojectLabels)
	require.Same(t, cr.(*full.CompiledRule), rs.cr.(*full.CompiledRule),
		"a lens with no `*` keeps the exact rule object it was given — not merely an equal copy")
}

// TestUseFullEngine_StaleTaxonomy_ReprojectsBroadButMatchesExpandedTypes
// covers §4.2's "known but not armed" tier: a snapshot is loaded (every
// referenced label resolves) but SetArmed was never called. Activation must
// still succeed, degrade to the broad (non-exhaustive) filter, AND — this is
// the part broad delivery alone cannot buy — the compiled rule the pipeline
// actually runs must still MATCH the resolved concrete types. `location` is
// reached via a relationship traversal from the identity anchor, mirroring
// Fire B's real shape (service-location's capabilityServiceAccess).
func TestUseFullEngine_StaleTaxonomy_ReprojectsBroadButMatchesExpandedTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, targetKV := newCollisionKVs(t)

	eng := full.New()
	spec := `MATCH (i:identity) MATCH (i)-[:worksAt]->(l:location*) RETURN i.key AS key, l.key AS locKey`
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	fullCR := cr.(*full.CompiledRule)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	adpt, err := adapter.New(targetKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := New("stale-taxonomy", "nats_kv", "CORE", adjKV, coreKV, adpt, nil)
	require.NoError(t, err)

	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: taxID("TAXstaleLocationMeta"), CanonicalName: "location", Abstract: true},
		{ID: taxID("TAXstaleUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
	})
	// SetArmed is intentionally never called — the taxonomy is known but the
	// invalidation consumer is not live.
	p.SetTaxonomyResolver(resolver)

	require.NoError(t, p.UseFullEngine(eng, cr))
	rs := p.ruleState()
	require.True(t, rs.reprojectAll, "a stale-but-known taxonomy must still force the broad filter")
	require.Empty(t, rs.reprojectLabels)

	const identityID = "STALEidentityAAAAAAA"
	const unitID = "STALEunitAAAAAAAAAAA"
	identityKey := "vtx.identity." + identityID
	unitKey := "vtx.unit." + unitID
	identityBody := seedVertexBody(t, coreKV, identityKey, "identity", nil)
	seedVertexBody(t, coreKV, unitKey, "unit", nil)
	buildCollisionEdge(t, adjKV, "worksAt", "identity", identityID, "unit", unitID)

	handleVertexEvent(t, p, identityKey, identityBody, 1)
	row := targetRow(t, targetKV, identityKey)
	require.Equal(t, unitKey, row["locKey"],
		"the `*` pattern matched a unit — a concrete member of location's resolved downward "+
			"closure — even though the filter itself stayed broad")
}

// TestUseFullEngine_UnknownTaxonomy_RefusesActivation covers §4.2's other
// tier: no resolver was ever installed, which is indistinguishable from "no
// snapshot loaded" (taxonomy.StatusUnknown). Activation must refuse outright
// — no broad-filter fallback — and publish nothing.
func TestUseFullEngine_UnknownTaxonomy_RefusesActivation(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)

	p, err := New("unknown-taxonomy", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)
	// No SetTaxonomyResolver call at all.

	err = p.UseFullEngine(eng, cr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "location")

	rs := p.ruleState()
	require.Empty(t, rs.engineKind, "a refused activation must publish nothing")
	require.Nil(t, rs.cr)
}

// TestUseFullEngine_UnknownTaxonomy_UnresolvableLabelAndCycleBothRefuse pins
// the other two set-UNKNOWN causes besides "no resolver at all": a snapshot
// that is loaded but does not contain the referenced label, and one whose
// downward walk hits a cycle. Both must refuse exactly like the no-resolver
// case, never fall back to a partial or empty expansion.
func TestUseFullEngine_UnknownTaxonomy_UnresolvableLabelAndCycleBothRefuse(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)

	t.Run("label absent from an otherwise-loaded snapshot", func(t *testing.T) {
		p, err := New("unresolvable-label", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		resolver := taxonomy.New()
		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{{ID: taxID("TAXunrelatedMeta"), CanonicalName: "owner"}})
		resolver.SetArmed(true)
		p.SetTaxonomyResolver(resolver)

		require.Error(t, p.UseFullEngine(eng, cr))
	})

	t.Run("cyclic taxonomy", func(t *testing.T) {
		p, err := New("cyclic-taxonomy", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		resolver := taxonomy.New()
		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
			{ID: taxID("TAXcycleLocationMeta"), CanonicalName: "location", SubtypeOf: []string{"unit"}},
			{ID: taxID("TAXcycleUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
		})
		resolver.SetArmed(true)
		p.SetTaxonomyResolver(resolver)

		require.Error(t, p.UseFullEngine(eng, cr))
	})
}

// TestUseFullEngine_ConcreteChildlessSigil_ActivationRefusesReDerivationAccepts
// pins the activation/re-derivation split that closes the "a live `*` lens
// goes dark when its concrete type's last subtypeOf child is uninstalled"
// hazard: a `*` on a concrete type with no subtypeOf children resolves to a
// KNOWN, correct closure of exactly {itself} (taxonomy.Resolver.Expand's
// inert flag) — never taxonomy.StatusUnknown. Two entry points, two
// deliberately opposite decisions about that same answer:
//
//   - ACTIVATION (UseFullEngine/UseFullEngineBranches) refuses on it. An
//     author writing `:unit*` against a taxonomy that cannot currently
//     honour it is exactly the authoring mistake amendment A3 exists to
//     catch, so nothing is published and the error names the label.
//   - A LIVE RE-DERIVATION (UseFullEngineBranchesForReDerivation) must NOT
//     take that same refusal — this is the exact shape of "unit's last
//     subtypeOf child gets uninstalled by a DIFFERENT package while
//     `:unit*` is live and correct" (§6.5): the re-derivation accepts
//     {itself} and keeps the pipeline narrowed on "unit", so the lens's own
//     still-resolvable instances never go dark.
func TestUseFullEngine_ConcreteChildlessSigil_ActivationRefusesReDerivationAccepts(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(`MATCH (l:unit*) RETURN l.key AS key`)
	require.NoError(t, err)

	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: taxID("TAXchildlessUnitMeta"), CanonicalName: "unit"},
	})
	resolver.SetArmed(true)

	t.Run("activation refuses", func(t *testing.T) {
		p, err := New("concrete-childless-activation", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		p.SetTaxonomyResolver(resolver)

		err = p.UseFullEngine(eng, cr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unit")
		require.Contains(t, err.Error(), "resolves to exactly itself")

		rs := p.ruleState()
		require.Empty(t, rs.engineKind, "a refused activation must publish nothing")
	})

	t.Run("live re-derivation accepts and stays narrowed on itself", func(t *testing.T) {
		p, err := New("concrete-childless-rederivation", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		p.SetTaxonomyResolver(resolver)

		require.NoError(t, p.UseFullEngineBranchesForReDerivation(eng, cr, nil))

		rs := p.ruleState()
		require.False(t, rs.reprojectAll, "a live re-derivation must accept the inert {itself} closure and stay narrowed, never go broad and never refuse")
		require.Equal(t, map[string]struct{}{"unit": {}}, rs.reprojectLabels)
	})
}

// TestUseFullEngineBranches_UnknownTaxonomy_LeavesPreviousRuleStatePublished
// pins the hot-reload shape (cmd/refractor/reload.go): a MATCH edit that
// would refuse to activate must leave the PIPELINE'S PREVIOUS rule running,
// not merely publish nothing on a fresh pipeline.
func TestUseFullEngineBranches_UnknownTaxonomy_LeavesPreviousRuleStatePublished(t *testing.T) {
	eng := full.New()
	goodCR, err := eng.Parse(`MATCH (u:unit) RETURN u.key AS key`)
	require.NoError(t, err)
	p, err := New("unknown-taxonomy-reload", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, goodCR))
	genBefore := p.ruleState().gen

	badCR, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)
	require.Error(t, p.UseFullEngine(eng, badCR))

	rs := p.ruleState()
	require.Equal(t, genBefore, rs.gen, "a refused activation must not advance ruleGen or publish anything")
	require.Same(t, goodCR.(*full.CompiledRule), rs.cr.(*full.CompiledRule))
}

// TestUseFullEngine_ArmedTaxonomy_ExhaustiveAndNarrowed proves the positive
// side of the fork's happy path: a fully armed, fully resolved taxonomy
// keeps the lens EXHAUSTIVE (narrow filter), with the expanded concrete
// types unioned into reprojectLabels.
func TestUseFullEngine_ArmedTaxonomy_ExhaustiveAndNarrowed(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)

	p, err := New("armed-taxonomy", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)
	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: taxID("TAXarmedLocationMeta"), CanonicalName: "location", Abstract: true},
		{ID: taxID("TAXarmedUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
		{ID: taxID("TAXarmedBuildingMeta"), CanonicalName: "building", SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)
	p.SetTaxonomyResolver(resolver)

	require.NoError(t, p.UseFullEngine(eng, cr))
	rs := p.ruleState()
	require.False(t, rs.reprojectAll)
	require.Equal(t, map[string]struct{}{"unit": {}, "building": {}}, rs.reprojectLabels)
	require.Equal(t, map[string]struct{}{"unit": {}, "building": {}}, rs.seedAnchorLabels,
		"a `*` anchor's seedAnchorLabels holds the resolved downward closure, not the bare label")
}

// TestUseFullEngine_ArmedButEmptyExpansion_DegradesToBroad pins §3.4's
// expanded-set row at the boundary Expand alone cannot enforce: an armed,
// fully-resolved taxonomy whose abstract label has ZERO concrete
// descendants is a KNOWN answer (Expand reports StatusArmed, not
// StatusUnknown), but publishing exhaustive=true on it would make
// reprojectLabels lose the label's contribution entirely — the "stale
// narrow set" §6.5 calls the only unacceptable state, since
// plainVertexRelevant's false branch acks-and-drops with no fallback. Must
// degrade to the broad filter exactly like a not-yet-armed resolver.
func TestUseFullEngine_ArmedButEmptyExpansion_DegradesToBroad(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)

	p, err := New("armed-empty-expansion", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)
	resolver := taxonomy.New()
	// "location" is abstract with no children at all — an armed, fully
	// resolved, genuinely empty concrete closure.
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: taxID("TAXemptyLocationMeta"), CanonicalName: "location", Abstract: true},
	})
	resolver.SetArmed(true)
	p.SetTaxonomyResolver(resolver)

	require.NoError(t, p.UseFullEngine(eng, cr))
	rs := p.ruleState()
	require.True(t, rs.reprojectAll, "an empty resolved set must force the broad filter, never exhaustive=true")
}

// TestKeyColumnsThreading_MustPrecedePublish_NotFollow pins the ordering
// invariant cmd/refractor/main.go's activation depends on: KeyColumns is the
// one CompiledRule field a caller mutates AFTER construction
// (projection.ThreadKeyColumns, called from a Personal Lens's activation),
// and UseFullEngineBranches' publish is copy-on-write for any `*`-carrying
// rule (full.WithLabelExpansion returns a shallow copy). Threading BEFORE
// the UseFullEngineBranches call must reach the published rule; threading
// AFTER must NOT — activation must always thread first, or the adapter
// rejects every write with "key field %q absent from keys map", retried
// forever.
func TestKeyColumnsThreading_MustPrecedePublish_NotFollow(t *testing.T) {
	eng := full.New()
	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: taxID("TAXkeycolLocationMeta"), CanonicalName: "location", Abstract: true},
		{ID: taxID("TAXkeycolUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)

	spec := `MATCH (i:identity {key: $actorKey}) MATCH (i)-[:manages]->(l:location*) RETURN l.key AS anchor, l.key AS entityId`

	t.Run("threaded before UseFullEngineBranches reaches the published copy", func(t *testing.T) {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		fullCR := cr.(*full.CompiledRule)
		fullCR.KeyColumns = []string{"entityId"}
		require.NoError(t, fullCR.ValidateKeyColumns())

		p, err := New("keycol-before", "nats_subject", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		p.SetTaxonomyResolver(resolver)
		require.NoError(t, p.UseFullEngine(eng, cr))

		published := p.ruleState().cr.(*full.CompiledRule)
		require.Equal(t, []string{"entityId"}, published.KeyColumns,
			"threading before publish must survive into the copy the pipeline evaluates")
	})

	t.Run("threaded after UseFullEngineBranches never reaches the published copy", func(t *testing.T) {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		fullCR := cr.(*full.CompiledRule)

		p, err := New("keycol-after", "nats_subject", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		p.SetTaxonomyResolver(resolver)
		require.NoError(t, p.UseFullEngine(eng, cr))

		// Mutate the ORIGINAL cr after the pipeline already published a
		// taxonomy-expanded COPY of it — the shape a caller that threads
		// KeyColumns after activation would produce.
		fullCR.KeyColumns = []string{"entityId"}

		published := p.ruleState().cr.(*full.CompiledRule)
		require.Empty(t, published.KeyColumns,
			"mutating the pre-publish object after the copy was made must NOT reach the published rule — "+
				"that is why activation must thread KeyColumns before calling UseFullEngineBranches")
	})
}

// TestSeedAnchorFor_LabelExpand_LeafTypeEventNarrowsInsteadOfFullRescan pins
// the outcome that makes seedAnchorLabels load-bearing rather than merely
// descriptive: a leaf-type event on a `*`-anchored lens must narrow to ONE
// anchor instead of paying a full corpus rescan. Asserting only the
// ruleState's seedAnchorLabels CONTENTS (as TestUseFullEngine_ArmedTaxonomy_
// ExhaustiveAndNarrowed already does) doesn't pin that outcome — only
// calling seedAnchorFor itself and getting back the event key, not "", does.
func TestSeedAnchorFor_LabelExpand_LeafTypeEventNarrowsInsteadOfFullRescan(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)

	p, err := New("seed-anchor-expand", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)
	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: taxID("TAXseedLocationMeta"), CanonicalName: "location", Abstract: true},
		{ID: taxID("TAXseedUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)
	p.SetTaxonomyResolver(resolver)
	require.NoError(t, p.UseFullEngine(eng, cr))

	rs := p.ruleState()
	const unitKey = "vtx.unit.SEEDexpandAAAAAAAAAA"
	require.Equal(t, unitKey, p.seedAnchorFor(rs, "unit", unitKey),
		"a leaf-type event on a `*`-anchored lens must narrow to that one anchor — "+
			"an empty return here means every unit write pays a full corpus rescan, strictly worse than today")

	require.Empty(t, p.seedAnchorFor(rs, "owner", "vtx.owner.SEEDexpandBBBBBBBBBB"),
		"a type outside location's resolved set must not seed anything")
}

// TestPlainLens_LabelExpandAnchor_LeafTombstoneRetractsGrantShapedRow pins
// §5.1 site 3 (AnchorProjectionKey/AnchorDeleteResult) end to end: left as
// bare equality, an abstract-anchored lens's leaf-type tombstone never
// retracts — a grant that never revokes. Asserting only the returned key MAP
// (as label_expansion_test.go's AnchorProjectionKey unit tests do) does not
// prove the retraction actually reaches a target; this drives a real
// leaf-type (unit) vertex tombstone through the full pipeline dispatch
// (p.handle) against a real NATS-KV adapter and confirms the previously
// projected row is gone.
func TestPlainLens_LabelExpandAnchor_LeafTombstoneRetractsGrantShapedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	coreKV, adjKV, targetKV := newCollisionKVs(t)

	eng := full.New()
	spec := `MATCH (l:location*) RETURN l.key AS key, l.name AS name`
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	fullCR := cr.(*full.CompiledRule)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	adpt, err := adapter.New(targetKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := New("leaf-tombstone-retraction", "nats_kv", "CORE", adjKV, coreKV, adpt, nil)
	require.NoError(t, err)

	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: taxID("TAXretractLocationMeta"), CanonicalName: "location", Abstract: true},
		{ID: taxID("TAXretractUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)
	p.SetTaxonomyResolver(resolver)
	require.NoError(t, p.UseFullEngine(eng, cr))

	const unitID = "RETRACTunitAAAAAAAAA"
	unitKey := "vtx.unit." + unitID
	unitBody := seedVertexBody(t, coreKV, unitKey, "unit", map[string]any{"name": "Loft A"})
	handleVertexEvent(t, p, unitKey, unitBody, 1)

	_, err = targetKV.Get(ctx, unitKey)
	require.NoError(t, err, "the leaf-type row must project before the tombstone")

	tombstone, merr := json.Marshal(map[string]any{"key": unitKey, "class": "unit", "isDeleted": true})
	require.NoError(t, merr)
	dec, herr := p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + unitKey, Body: tombstone, Sequence: 2,
	})
	require.NoError(t, herr)
	require.Equal(t, substrate.Ack, dec)

	_, err = targetKV.Get(ctx, unitKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"an abstract-anchored lens's leaf-type tombstone must retract the row through a real target write — "+
			"left as bare equality, this never fires: a grant that never revokes")
}
