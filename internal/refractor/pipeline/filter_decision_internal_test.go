package pipeline

// The Core KV consumer-footprint observation (dynamic-type-taxonomy-design.md
// §14 Fire A item 6, vocabulary §10.3): ConsumerFilter's FilterDecision, the
// broad-reason carried on the rule snapshot, and the health writes at the two
// derivation sites plus the registration-fallback override.
//
// Every test here holds one invariant above all others: the observation must
// not move the filter. A lens that narrows differently than its own client-side
// gate would is an under- or over-grant on the auth plane, and no filter update
// rewinds a JetStream cursor — so the tables below pin the filter SUBJECTS
// beside the decision, and a change that reports better while delivering
// differently fails here rather than in Capability KV.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// capLabels builds n distinct single-letter labels, the same shape
// narrowed_filter_internal_test.go's cap cases use.
func capLabels(n int) map[string]struct{} {
	labels := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		labels[string(rune('a'+i))] = struct{}{}
	}
	return labels
}

// TestConsumerFilter_DecisionIsTheSameDerivationAsTheFilter is the
// observation-only proof. Each row pins BOTH halves of one ConsumerFilter call
// — the filter the consumer registers and the decision the health entry
// records — so the two cannot be changed independently. A row whose decision
// moves while its subjects stay put is a reporting bug; a row whose SUBJECTS
// move is the thing this whole increment promised would not happen.
func TestConsumerFilter_DecisionIsTheSameDerivationAsTheFilter(t *testing.T) {
	cases := []struct {
		name              string
		build             func() *Pipeline
		wantFilterSubject string
		wantSubjects      []string
		wantDecision      FilterDecision
	}{
		{
			name: "no rule ever compiled — broad, not-eligible",
			build: func() *Pipeline {
				return &Pipeline{ruleID: "fd-none", coreKVBucket: "core-kv"}
			},
			wantFilterSubject: "$KV.core-kv.>",
			wantDecision: FilterDecision{
				Mode:        health.FilterModeBroad,
				BroadReason: health.FilterBroadReasonNotEligible,
			},
		},
		{
			name: "exhaustive but empty label set — broad, not-eligible",
			build: func() *Pipeline {
				return &Pipeline{
					ruleID: "fd-empty", coreKVBucket: "core-kv",
					engineKind:           ruleengine.EngineFull,
					plainReprojectLabels: map[string]struct{}{},
				}
			},
			wantFilterSubject: "$KV.core-kv.>",
			wantDecision: FilterDecision{
				Mode:        health.FilterModeBroad,
				BroadReason: health.FilterBroadReasonNotEligible,
			},
		},
		{
			name: "non-exhaustive label set — broad, carrying the site's reason",
			build: func() *Pipeline {
				return &Pipeline{
					ruleID: "fd-nonexhaustive", coreKVBucket: "core-kv",
					engineKind:            ruleengine.EngineFull,
					plainReprojectAll:     true,
					plainNarrowingBlocked: health.FilterBroadReasonNonExhaustive,
				}
			},
			wantFilterSubject: "$KV.core-kv.>",
			wantDecision: FilterDecision{
				Mode:        health.FilterModeBroad,
				BroadReason: health.FilterBroadReasonNonExhaustive,
			},
		},
		{
			name: "taxonomy known but not armed — broad, taxonomy-unarmed",
			build: func() *Pipeline {
				return &Pipeline{
					ruleID: "fd-unarmed", coreKVBucket: "core-kv",
					engineKind:            ruleengine.EngineFull,
					plainReprojectAll:     true,
					plainNarrowingBlocked: health.FilterBroadReasonTaxonomyUnarmed,
				}
			},
			wantFilterSubject: "$KV.core-kv.>",
			wantDecision: FilterDecision{
				Mode:        health.FilterModeBroad,
				BroadReason: health.FilterBroadReasonTaxonomyUnarmed,
			},
		},
		{
			name: "one label, relation set not exhaustive — narrowed-label",
			build: func() *Pipeline {
				return &Pipeline{
					ruleID: "fd-label", coreKVBucket: "core-kv",
					engineKind:           ruleengine.EngineFull,
					plainReprojectLabels: map[string]struct{}{"book": {}},
				}
			},
			wantSubjects: []string{
				"$KV.core-kv.vtx.book.>",
				"$KV.core-kv.lnk.book.>",
				"$KV.core-kv.lnk.*.*.*.book.>",
			},
			wantDecision: FilterDecision{Mode: health.FilterModeNarrowedLabel, LabelCount: 1},
		},
		{
			name: "one label, exhaustive relations within budget — narrowed-relation",
			build: func() *Pipeline {
				return &Pipeline{
					ruleID: "fd-relation", coreKVBucket: "core-kv",
					engineKind:               ruleengine.EngineFull,
					plainReprojectLabels:     map[string]struct{}{"book": {}},
					plainReprojectRelations:  map[string]struct{}{"writtenBy": {}},
					plainRelationsExhaustive: true,
				}
			},
			wantSubjects: []string{
				"$KV.core-kv.vtx.book.>",
				"$KV.core-kv.lnk.book.*.writtenBy.>",
				"$KV.core-kv.lnk.*.*.writtenBy.book.>",
			},
			wantDecision: FilterDecision{Mode: health.FilterModeNarrowedRelation, LabelCount: 1},
		},
		{
			name: "relation set exhaustive but over the subject budget — narrowed-label",
			build: func() *Pipeline {
				// 4 labels x (1 + 2x4 relations) = 36 subjects, past
				// maxNarrowedFilterSubjects — the relation dimension degrades
				// on its own without touching the label narrowing.
				return &Pipeline{
					ruleID: "fd-relation-budget", coreKVBucket: "core-kv",
					engineKind:           ruleengine.EngineFull,
					plainReprojectLabels: capLabels(4),
					plainReprojectRelations: map[string]struct{}{
						"one": {}, "two": {}, "three": {}, "four": {},
					},
					plainRelationsExhaustive: true,
				}
			},
			wantSubjects: []string{
				"$KV.core-kv.vtx.a.>", "$KV.core-kv.lnk.a.>", "$KV.core-kv.lnk.*.*.*.a.>",
				"$KV.core-kv.vtx.b.>", "$KV.core-kv.lnk.b.>", "$KV.core-kv.lnk.*.*.*.b.>",
				"$KV.core-kv.vtx.c.>", "$KV.core-kv.lnk.c.>", "$KV.core-kv.lnk.*.*.*.c.>",
				"$KV.core-kv.vtx.d.>", "$KV.core-kv.lnk.d.>", "$KV.core-kv.lnk.*.*.*.d.>",
			},
			wantDecision: FilterDecision{Mode: health.FilterModeNarrowedLabel, LabelCount: 4},
		},
		{
			name: "declared actor-anchored, enumerator not installed — broad, install-incomplete",
			build: func() *Pipeline {
				// The shape the ordering guard exists for, held at the exact
				// moment it is dangerous: the rule publication has landed an
				// exhaustive label set AND the declaration, and
				// InstallActorAggregate has not run. Every plain condition is
				// met, the exhaustive relation set included, so without the
				// guard this row would deliver the relation-narrowed filter —
				// the most aggressive form in the vocabulary — with none of the
				// actor-aware conjuncts evaluated.
				return &Pipeline{
					ruleID: "fd-preinstall", coreKVBucket: "core-kv",
					engineKind:               ruleengine.EngineFull,
					plainReprojectLabels:     map[string]struct{}{"identity": {}},
					plainReprojectRelations:  map[string]struct{}{"holdsRole": {}},
					plainRelationsExhaustive: true,
					declaresActorAnchor:      true,
				}
			},
			wantFilterSubject: "$KV.core-kv.>",
			wantDecision: FilterDecision{
				Mode:        health.FilterModeBroad,
				BroadReason: health.FilterBroadReasonInstallIncomplete,
			},
		},
		{
			// The same rule state with the enumerator INSTALLED is the
			// companion the row above needs: the guard clears, and the
			// actor-aware conjunction then decides on its own terms. Here it
			// refuses for a §4.2 reason (no pattern-closure declaration, no
			// sweep plan), which reports as the ordinary not-eligible — proving
			// the two refusals stay distinguishable in the health entry.
			name: "declared actor-anchored, enumerator installed — the §4.2 conjunction decides",
			build: func() *Pipeline {
				return &Pipeline{
					ruleID: "fd-postinstall", coreKVBucket: "core-kv",
					engineKind:               ruleengine.EngineFull,
					plainReprojectLabels:     map[string]struct{}{"identity": {}},
					plainReprojectRelations:  map[string]struct{}{"holdsRole": {}},
					plainRelationsExhaustive: true,
					declaresActorAnchor:      true,
					actorEnumerator:          &ActorEnumerator{actorType: "identity"},
				}
			},
			wantFilterSubject: "$KV.core-kv.>",
			wantDecision: FilterDecision{
				Mode:        health.FilterModeBroad,
				BroadReason: health.FilterBroadReasonNotEligible,
			},
		},
		{
			name: "label count past the cap — broad, label-cap",
			build: func() *Pipeline {
				return &Pipeline{
					ruleID: "fd-cap", coreKVBucket: "core-kv",
					engineKind:           ruleengine.EngineFull,
					plainReprojectLabels: capLabels(maxNarrowedFilterLabels + 1),
				}
			},
			wantFilterSubject: "$KV.core-kv.>",
			wantDecision: FilterDecision{
				Mode:        health.FilterModeBroad,
				BroadReason: health.FilterBroadReasonLabelCap,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filterSubjects, filterSubject, decision := tc.build().ConsumerFilter()
			require.Equal(t, tc.wantFilterSubject, filterSubject,
				"the broad FilterSubject must be byte-identical to what this shape delivered before the decision was reported")
			require.ElementsMatch(t, tc.wantSubjects, filterSubjects,
				"the narrowed subject set must be byte-identical to what this shape delivered before the decision was reported")
			require.Equal(t, tc.wantDecision, decision)
		})
	}
}

// TestConsumerFilter_DecisionAgreesWithTheFilterItReturns closes the gap a
// per-row table cannot: it asserts the RELATION between the two halves holds
// for every row, rather than that each row's two halves are the pinned pair. A
// mode that says narrowed while the broad FilterSubject is set — or a broad
// entry with no reason, which is what a silent default in broadFilterReason
// would produce — fails here whatever the row.
func TestConsumerFilter_DecisionAgreesWithTheFilterItReturns(t *testing.T) {
	shapes := []func() *Pipeline{
		func() *Pipeline { return &Pipeline{ruleID: "agree-none", coreKVBucket: "core-kv"} },
		func() *Pipeline {
			return &Pipeline{ruleID: "agree-empty", coreKVBucket: "core-kv",
				engineKind: ruleengine.EngineFull, plainReprojectLabels: map[string]struct{}{}}
		},
		func() *Pipeline {
			return &Pipeline{ruleID: "agree-blocked", coreKVBucket: "core-kv",
				engineKind: ruleengine.EngineFull, plainReprojectAll: true,
				plainNarrowingBlocked: health.FilterBroadReasonNonExhaustive}
		},
		func() *Pipeline {
			return &Pipeline{ruleID: "agree-label", coreKVBucket: "core-kv",
				engineKind: ruleengine.EngineFull, plainReprojectLabels: capLabels(3)}
		},
		func() *Pipeline {
			return &Pipeline{ruleID: "agree-relation", coreKVBucket: "core-kv",
				engineKind: ruleengine.EngineFull, plainReprojectLabels: capLabels(2),
				plainReprojectRelations:  map[string]struct{}{"rel": {}},
				plainRelationsExhaustive: true}
		},
		func() *Pipeline {
			return &Pipeline{ruleID: "agree-cap", coreKVBucket: "core-kv",
				engineKind: ruleengine.EngineFull, plainReprojectLabels: capLabels(maxNarrowedFilterLabels + 1)}
		},
		func() *Pipeline { p := eligiblePipeline(t); p.coreKVBucket = "core-kv"; return p },
		func() *Pipeline {
			p := eligiblePipeline(t)
			p.coreKVBucket = "core-kv"
			p.sweeper = nil
			return p
		},
	}
	for _, build := range shapes {
		p := build()
		filterSubjects, filterSubject, dec := p.ConsumerFilter()
		if dec.Mode == health.FilterModeBroad {
			require.Empty(t, filterSubjects, "%s: a broad decision must come with no narrowed subjects", p.ruleID)
			require.NotEmpty(t, filterSubject, "%s: a broad decision must come with the broad filter", p.ruleID)
			require.NotEmpty(t, dec.BroadReason, "%s: every broad decision names a reason — there is no default arm", p.ruleID)
			require.Zero(t, dec.LabelCount, "%s: a broad filter carries no labels", p.ruleID)
			continue
		}
		require.Contains(t, []string{health.FilterModeNarrowedLabel, health.FilterModeNarrowedRelation}, dec.Mode,
			"%s: the mode vocabulary is closed", p.ruleID)
		require.NotEmpty(t, filterSubjects, "%s: a narrowed decision must come with narrowed subjects", p.ruleID)
		require.Empty(t, filterSubject, "%s: a narrowed decision must not also set the broad filter", p.ruleID)
		require.Empty(t, dec.BroadReason, "%s: a narrowed decision has nothing to explain", p.ruleID)
		require.Positive(t, dec.LabelCount, "%s: a narrowed filter is built from at least one label", p.ruleID)
	}
}

// TestConsumerFilter_LabelCapBoundary is §15's Cap case, stated as the design
// states it: a lens at exactly maxNarrowedFilterLabels narrows and reports the
// count it narrowed on; one label more takes the broad filter and says
// label-cap. Both halves matter — pinning only the broad side would pass on a
// build that never narrows at all.
func TestConsumerFilter_LabelCapBoundary(t *testing.T) {
	atCap := &Pipeline{
		ruleID: "cap-at", coreKVBucket: "core-kv",
		engineKind:           ruleengine.EngineFull,
		plainReprojectLabels: capLabels(maxNarrowedFilterLabels),
	}
	filterSubjects, filterSubject, dec := atCap.ConsumerFilter()
	require.Empty(t, filterSubject, "a lens exactly at the cap still narrows")
	require.Len(t, filterSubjects, maxNarrowedFilterLabels*3)
	require.Equal(t, FilterDecision{
		Mode:       health.FilterModeNarrowedLabel,
		LabelCount: maxNarrowedFilterLabels,
	}, dec)

	overCap := &Pipeline{
		ruleID: "cap-over", coreKVBucket: "core-kv",
		engineKind:           ruleengine.EngineFull,
		plainReprojectLabels: capLabels(maxNarrowedFilterLabels + 1),
	}
	filterSubjects, filterSubject, dec = overCap.ConsumerFilter()
	require.Empty(t, filterSubjects)
	require.Equal(t, "$KV.core-kv.>", filterSubject)
	require.Equal(t, FilterDecision{
		Mode:        health.FilterModeBroad,
		BroadReason: health.FilterBroadReasonLabelCap,
	}, dec)
}

// TestConsumerFilter_ActorAwareInstallationGapsReportNotEligible pins the arm
// with no per-site cause to carry. Each knocked-out conjunct is a property of
// how the lens was WIRED, not of its cypher — the rule itself is exhaustive —
// so the reason is not-eligible rather than a taxonomy or exhaustiveness
// answer that would send an operator to the wrong file.
func TestConsumerFilter_ActorAwareInstallationGapsReportNotEligible(t *testing.T) {
	knockOuts := map[string]func(p *Pipeline){
		"output is not pattern-closed": func(p *Pipeline) { p.patternClosedOutput = false },
		"no convergence sweep":         func(p *Pipeline) { p.sweeper = nil },
		"anchor type outside the label set": func(p *Pipeline) {
			p.actorEnumerator = NewActorEnumerator(nil, nil, "service")
		},
	}
	for name, knockOut := range knockOuts {
		t.Run(name, func(t *testing.T) {
			p := eligiblePipeline(t)
			p.coreKVBucket = "core-kv"
			knockOut(p)

			filterSubjects, filterSubject, dec := p.ConsumerFilter()
			require.Empty(t, filterSubjects)
			require.Equal(t, "$KV.core-kv.>", filterSubject)
			require.Equal(t, FilterDecision{
				Mode:        health.FilterModeBroad,
				BroadReason: health.FilterBroadReasonNotEligible,
			}, dec)
		})
	}
}

// TestUseFullEngineBranches_PublishesTheBroadReason drives the cause through
// the REAL derivation rather than a hand-set field, for each site that clears
// exhaustiveness. The hand-built tables above prove ConsumerFilter reports what
// the snapshot carries; this proves the snapshot carries what actually
// happened.
func TestUseFullEngineBranches_PublishesTheBroadReason(t *testing.T) {
	t.Run("an unlabeled node is non-exhaustive", func(t *testing.T) {
		p := taxonomyReasonPipeline(t, "reason-unlabeled")
		eng := full.New()
		cr, err := eng.Parse(`MATCH (u:unit)-[:managedBy]->(o) RETURN u.key AS key, o.name AS ownerName`)
		require.NoError(t, err)
		require.NoError(t, p.UseFullEngine(eng, cr))

		require.True(t, p.ruleState().reprojectAll)
		require.Equal(t, health.FilterBroadReasonNonExhaustive, p.ruleState().narrowingBlocked)
		_, _, dec := p.ConsumerFilter()
		require.Equal(t, health.FilterBroadReasonNonExhaustive, dec.BroadReason)
	})

	t.Run("a known-but-unarmed taxonomy is taxonomy-unarmed", func(t *testing.T) {
		p := taxonomyReasonPipeline(t, "reason-unarmed")
		resolver := taxonomy.New()
		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
			{ID: taxID("TAXreasonLocMeta"), CanonicalName: "location", Abstract: true},
			{ID: taxID("TAXreasonUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
		})
		// SetArmed deliberately not called: the snapshot is known, the
		// invalidation consumer is not live.
		p.SetTaxonomyResolver(resolver)

		eng := full.New()
		cr, err := eng.Parse(`MATCH (i:identity) MATCH (i)-[:worksAt]->(l:location*) RETURN i.key AS key, l.key AS locKey`)
		require.NoError(t, err)
		require.NoError(t, p.UseFullEngine(eng, cr))

		require.True(t, p.ruleState().reprojectAll)
		require.Equal(t, health.FilterBroadReasonTaxonomyUnarmed, p.ruleState().narrowingBlocked)
		_, _, dec := p.ConsumerFilter()
		require.Equal(t, health.FilterBroadReasonTaxonomyUnarmed, dec.BroadReason)
	})

	t.Run("a live re-derivation finding the expansion unknown is taxonomy-unresolvable", func(t *testing.T) {
		eng := full.New()
		cr, err := eng.Parse(`MATCH (i:identity) MATCH (i)-[:worksAt]->(l:location*) RETURN i.key AS key, l.key AS locKey`)
		require.NoError(t, err)

		// Only a LIVE re-derivation reaches the reason site with a
		// StatusUnknown answer: an ACTIVATION refuses outright (§4.2) and
		// publishes nothing at all. The positive vector, on its own pipeline.
		refuser := taxonomyReasonPipeline(t, "reason-unresolvable-activation")
		refuser.SetTaxonomyResolver(taxonomy.New())
		require.Error(t, refuser.UseFullEngine(eng, cr),
			"activation must still refuse an unknown expansion — the positive vector that proves the re-derivation below is the only way here")

		// A lens can only re-derive if it activated, and activation requires a
		// resolvable expansion — which is also the last known good set the
		// degrade keeps matching against.
		p := taxonomyReasonPipeline(t, "reason-unresolvable")
		resolver := taxonomy.New()
		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
			{ID: taxID("TAXunresLocMeta"), CanonicalName: "location", Abstract: true},
			{ID: taxID("TAXunresUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
		})
		resolver.SetArmed(true)
		p.SetTaxonomyResolver(resolver)
		require.NoError(t, p.UseFullEngine(eng, cr))

		// The taxonomy stops being able to answer for "location" at all.
		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{{ID: taxID("TAXunresOtherMeta"), CanonicalName: "somethingElse"}})
		resolver.SetArmed(true)

		require.NoError(t, p.UseFullEngineBranchesForReDerivation(eng, cr, nil))
		require.True(t, p.ruleState().reprojectAll)
		require.Equal(t, health.FilterBroadReasonTaxonomyUnresolvable, p.ruleState().narrowingBlocked,
			"an unresolvable taxonomy needs a package fix and never clears by itself — reporting it as unarmed tells an operator to wait out a state that will not end")
		_, _, dec := p.ConsumerFilter()
		require.Equal(t, health.FilterBroadReasonTaxonomyUnresolvable, dec.BroadReason)
	})

	t.Run("an armed taxonomy resolving to zero concrete types is non-exhaustive", func(t *testing.T) {
		p := taxonomyReasonPipeline(t, "reason-zeroleaf")
		resolver := taxonomy.New()
		// An abstract label whose only descendant is itself abstract: a KNOWN,
		// genuinely empty expanded set, which is a resolver ANSWER rather than
		// a resolver fault — so the cause is the rule's own inexhaustiveness.
		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
			{ID: taxID("TAXzeroPLaceMeta"), CanonicalName: "place", Abstract: true},
			{ID: taxID("TAXzeroSiteMeta"), CanonicalName: "site", Abstract: true, SubtypeOf: []string{"place"}},
		})
		resolver.SetArmed(true)
		p.SetTaxonomyResolver(resolver)

		eng := full.New()
		cr, err := eng.Parse(`MATCH (i:identity) MATCH (i)-[:worksAt]->(l:place*) RETURN i.key AS key, l.key AS locKey`)
		require.NoError(t, err)
		require.NoError(t, p.UseFullEngine(eng, cr))

		require.True(t, p.ruleState().reprojectAll)
		require.Equal(t, health.FilterBroadReasonNonExhaustive, p.ruleState().narrowingBlocked)
		_, _, dec := p.ConsumerFilter()
		require.Equal(t, health.FilterBroadReasonNonExhaustive, dec.BroadReason)
	})

	t.Run("an exhaustive rule carries no reason at all", func(t *testing.T) {
		p := taxonomyReasonPipeline(t, "reason-clean")
		eng := full.New()
		cr, err := eng.Parse(`MATCH (u:unit)-[:managedBy]->(o:owner) RETURN u.key AS key, o.name AS ownerName`)
		require.NoError(t, err)
		require.NoError(t, p.UseFullEngine(eng, cr))

		require.False(t, p.ruleState().reprojectAll)
		require.Empty(t, p.ruleState().narrowingBlocked,
			"a reason left over from an exhaustive derivation would be reported the next time the lens went broad")
	})
}

// TestUseFullEngineBranches_RuleSwapReplacesTheReason is the lifetime half: the
// carried cause is a property of the CURRENT compiled rule and of nothing else,
// so a MATCH hot-reload that fixes the cypher must clear it and one that breaks
// the cypher must set it — in both directions, on the same live pipeline. A
// reason that survived a swap would describe a rule that is no longer running.
func TestUseFullEngineBranches_RuleSwapReplacesTheReason(t *testing.T) {
	p := taxonomyReasonPipeline(t, "reason-swap")
	eng := full.New()

	broken, err := eng.Parse(`MATCH (u:unit)-[:managedBy]->(o) RETURN u.key AS key, o.name AS ownerName`)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, broken))
	require.Equal(t, health.FilterBroadReasonNonExhaustive, p.ruleState().narrowingBlocked)

	fixed, err := eng.Parse(`MATCH (u:unit)-[:managedBy]->(o:owner) RETURN u.key AS key, o.name AS ownerName`)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, fixed))
	require.Empty(t, p.ruleState().narrowingBlocked, "a swap must replace the reason wholesale, not merge into it")

	require.NoError(t, p.UseFullEngine(eng, broken))
	require.Equal(t, health.FilterBroadReasonNonExhaustive, p.ruleState().narrowingBlocked,
		"and back again — the reason tracks the rule, never latches")
}

// TestUseFullEngineBranches_ReasonPrecedenceIsRankedNotPositional is the case
// every single-site subtest above structurally cannot reach: a derivation that
// trips MORE THAN ONE site. The unarmed-resolver site runs BEFORE the
// zero-concrete-leaves site, so first-writer-wins would report the one cause
// that clears on its own for a rule that also carries one that never does —
// and an operator would wait out an arming that changes nothing.
func TestUseFullEngineBranches_ReasonPrecedenceIsRankedNotPositional(t *testing.T) {
	t.Run("unarmed AND zero concrete leaves reports non-exhaustive", func(t *testing.T) {
		p := taxonomyReasonPipeline(t, "reason-both")
		resolver := taxonomy.New()
		// Both abstract: a KNOWN, genuinely empty expansion (the zero-leaf
		// site, rank 0) on a resolver that was never armed (the unarmed site,
		// rank 2, which fires FIRST).
		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
			{ID: taxID("TAXbothPLaceMeta"), CanonicalName: "place", Abstract: true},
			{ID: taxID("TAXbothSiteMeta"), CanonicalName: "site", Abstract: true, SubtypeOf: []string{"place"}},
		})
		p.SetTaxonomyResolver(resolver)

		eng := full.New()
		cr, err := eng.Parse(`MATCH (i:identity) MATCH (i)-[:worksAt]->(l:place*) RETURN i.key AS key, l.key AS locKey`)
		require.NoError(t, err)
		require.NoError(t, p.UseFullEngine(eng, cr))

		require.Equal(t, health.FilterBroadReasonNonExhaustive, p.ruleState().narrowingBlocked,
			"arming this resolver would not make the rule narrow — the permanent cause is the true verdict, whichever site fired first")
	})

	t.Run("an unlabeled node AND an unresolvable taxonomy reports non-exhaustive", func(t *testing.T) {
		p := taxonomyReasonPipeline(t, "reason-both-unresolvable")
		resolver := taxonomy.New()
		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
			{ID: taxID("TAXbothUnresLocMeta"), CanonicalName: "location", Abstract: true},
			{ID: taxID("TAXbothUnresUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
		})
		resolver.SetArmed(true)
		p.SetTaxonomyResolver(resolver)

		eng := full.New()
		// `o` unlabeled (the ReferencedLabels site, rank 0, which fires FIRST)
		// plus a `*` the resolver stops being able to expand (rank 1).
		cr, err := eng.Parse(`MATCH (i:identity) MATCH (i)-[:worksAt]->(l:location*) MATCH (i)-[:managedBy]->(o) RETURN i.key AS key, l.key AS locKey, o.name AS ownerName`)
		require.NoError(t, err)
		require.NoError(t, p.UseFullEngine(eng, cr))

		resolver.InstallSnapshot([]taxonomy.TypeSnapshot{{ID: taxID("TAXbothUnresOtherMeta"), CanonicalName: "somethingElse"}})
		resolver.SetArmed(true)
		require.NoError(t, p.UseFullEngineBranchesForReDerivation(eng, cr, nil))

		require.Equal(t, health.FilterBroadReasonNonExhaustive, p.ruleState().narrowingBlocked,
			"fixing the taxonomy would still leave an unlabeled node — non-exhaustive survives every other repair")
	})

	t.Run("the rank table is total and ordered", func(t *testing.T) {
		require.Less(t, narrowingBlockRankOf(health.FilterBroadReasonNonExhaustive),
			narrowingBlockRankOf(health.FilterBroadReasonTaxonomyUnresolvable))
		require.Less(t, narrowingBlockRankOf(health.FilterBroadReasonTaxonomyUnresolvable),
			narrowingBlockRankOf(health.FilterBroadReasonTaxonomyUnarmed))
		require.Greater(t, narrowingBlockRankOf("a-reason-nobody-registered"),
			narrowingBlockRankOf(health.FilterBroadReasonTaxonomyUnarmed),
			"an unregistered reason must rank LAST — the map's zero value would silently outrank every real cause")
	})
}

// taxonomyReasonPipeline is a Pipeline with nothing installed but a target
// adapter — the shape UseFullEngine needs and no more.
func taxonomyReasonPipeline(t *testing.T, ruleID string) *Pipeline {
	t.Helper()
	p, err := New(ruleID, "nats_kv", "core-kv", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)
	return p
}

// TestRecordFilterDecision_WritesTheTriple pins the health write itself: all
// three fields land together, and a LATER decision replaces all three rather
// than merging — a stale filterLabelCount left beside a broad filterMode would
// read as a narrowed lens that lost its reason.
func TestRecordFilterDecision_WritesTheTriple(t *testing.T) {
	reporter := newFallbackHealthReporter(t, "fd-write")
	p := &Pipeline{ruleID: "fd-write", reporter: reporter}
	ctx := context.Background()

	p.RecordFilterDecision(ctx, FilterDecision{Mode: health.FilterModeNarrowedRelation, LabelCount: 3})
	entry, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, health.FilterModeNarrowedRelation, entry.FilterMode)
	require.Equal(t, 3, entry.FilterLabelCount)
	require.Empty(t, entry.FilterBroadReason)

	p.RecordFilterDecision(ctx, FilterDecision{
		Mode:        health.FilterModeBroad,
		BroadReason: health.FilterBroadReasonLabelCap,
	})
	entry, err = reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, health.FilterModeBroad, entry.FilterMode)
	require.Zero(t, entry.FilterLabelCount, "the previous narrowed count must not survive a broad decision")
	require.Equal(t, health.FilterBroadReasonLabelCap, entry.FilterBroadReason)
}

// TestRecordFilterDecision_WithoutAReporterIsANoOp pins the posture the two
// derivation sites depend on: a pipeline with no health reporter (every unit
// fixture in this package, and any embedding host that does not report) must
// still derive and register its filter.
func TestRecordFilterDecision_WithoutAReporterIsANoOp(t *testing.T) {
	p := &Pipeline{ruleID: "fd-noreporter", coreKVBucket: "core-kv"}
	_, _, dec := p.ConsumerFilter()
	require.NotPanics(t, func() { p.RecordFilterDecision(context.Background(), dec) })
}

// TestRegisterWithFilterFallback_RecordsTheRegistrationFailedFootprint pins the
// one reason decided AFTER the derivation. The lens derived a narrowed filter,
// JetStream refused it, and the entry must end up describing the filter the
// lens is actually running on — broad, with no labels.
//
// It also pins that this is ADDITIVE to the existing fault signal, not a
// replacement: a refused registration is the single broad reason that is also
// an error, and errorCount/lastError stay exactly where they were.
func TestRegisterWithFilterFallback_RecordsTheRegistrationFailedFootprint(t *testing.T) {
	reporter := newFallbackHealthReporter(t, "fd-regfail")
	p := &Pipeline{ruleID: "fd-regfail", reporter: reporter}
	ctx := context.Background()

	// The state the derivation left behind before registration was attempted.
	p.RecordFilterDecision(ctx, FilterDecision{Mode: health.FilterModeNarrowedLabel, LabelCount: 2})

	calls := 0
	require.NoError(t, p.registerWithFilterFallback(ctx, []string{"a.>", "b.>"}, func() {}, func() error {
		calls++
		if calls == 1 {
			return errors.New("injected overlapping-filter rejection")
		}
		return nil
	}))
	require.Equal(t, 2, calls)

	entry, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, health.FilterModeBroad, entry.FilterMode,
		"the entry must describe the filter the lens actually registered, not the one it derived")
	require.Equal(t, health.FilterBroadReasonRegistrationFailed, entry.FilterBroadReason)
	require.Zero(t, entry.FilterLabelCount)
	require.EqualValues(t, 1, entry.ErrorCount,
		"the footprint report is additive — a refused registration is still a fault")
	require.NotNil(t, entry.LastError)
}

// TestRegisterWithFilterFallback_ApplyBroadRunsBeforeASucceedingRetry pins the
// contract Rebuild's applyBroad closure depends on, on the one transition no
// Rebuild fixture can reach: the fallback fires and the RETRY SUCCEEDS, so the
// caller returns normally and goes on to report a footprint. applyBroad must
// have run by then — that is the caller's only chance to learn its derived
// filter was refused, and a caller that reported its derivation instead would
// advertise a narrowed footprint over a consumer running broad.
//
// Asserted on the same registrationFailedDecision Rebuild's closure assigns, so
// the two cannot drift into describing the refusal differently.
func TestRegisterWithFilterFallback_ApplyBroadRunsBeforeASucceedingRetry(t *testing.T) {
	p := &Pipeline{ruleID: "rwff-applybroad-order"}

	// Exactly Rebuild's closure shape: the derived decision, rewritten by the
	// fallback to what the consumer actually adopted.
	reported := FilterDecision{Mode: health.FilterModeNarrowedLabel, LabelCount: 2}
	calls := 0
	require.NoError(t, p.registerWithFilterFallback(context.Background(), []string{"a.>", "b.>"}, func() {
		reported = registrationFailedDecision()
	}, func() error {
		calls++
		if calls == 1 {
			return errors.New("injected overlapping-filter rejection")
		}
		return nil
	}))
	require.Equal(t, 2, calls, "the retry must have succeeded — this is the returns-normally path")
	require.Equal(t, registrationFailedDecision(), reported,
		"a caller that reaches its own report after a successful retry must already know the narrowed filter was refused")

	// The positive vector: with no fallback, the caller's derived decision is
	// untouched and it reports what it derived.
	untouched := FilterDecision{Mode: health.FilterModeNarrowedLabel, LabelCount: 2}
	require.NoError(t, p.registerWithFilterFallback(context.Background(), []string{"a.>"}, func() {
		untouched = registrationFailedDecision()
	}, func() error { return nil }))
	require.Equal(t, FilterDecision{Mode: health.FilterModeNarrowedLabel, LabelCount: 2}, untouched,
		"a clean registration must leave the derivation alone — otherwise every lens would report a refusal")
}

// TestRebuild_AbandonedResetDescribesWhatWasAdopted pins the ordering fix on the
// rebuild path: the entry must describe the filter the consumer ACTUALLY got.
// Recording the derivation before the reset made an abandoned rebuild advertise
// a footprint that was never adopted — on exactly the path where the lens's
// PREVIOUS filter is still the live one, so the entry became wrong about a lens
// that is otherwise fine.
//
// The reset is failed deterministically and with no NATS: a supervisor that has
// never managed this consumer name refuses UpdateSpec outright. The two subtests
// are the two shapes that reach it, and they must NOT be conflated — a rebuild
// whose registration was refused is a different fact from one that never got
// that far.
func TestRebuild_AbandonedResetDescribesWhatWasAdopted(t *testing.T) {
	newAbandoningPipeline := func(t *testing.T, ruleID string) (*Pipeline, *health.Reporter) {
		t.Helper()
		reporter := newFallbackHealthReporter(t, ruleID)
		p, err := New(ruleID, "nats_kv", "core-kv", nil, nil, &keyListerAdapter{}, reporter)
		require.NoError(t, err)
		p.supervisor = substrate.NewConsumerSupervisor(nil)
		p.consumerCfg = substrate.ConsumerSpec{Name: "never-managed"}
		return p, reporter
	}

	t.Run("a broad derivation that never registers leaves the previous footprint in place", func(t *testing.T) {
		// engineKind left zero-value, so the derivation is broad and
		// filterSubjects is empty — registerWithFilterFallback returns the
		// failure as-is, with no fallback and therefore no override of its own.
		// Nothing about this rebuild was adopted, so nothing about it may be
		// reported: the lens is still running on the filter it had.
		p, reporter := newAbandoningPipeline(t, "fd-abandon-broad")
		ctx := context.Background()

		// The footprint an earlier, successful derivation left behind. Also the
		// vector that keeps this from passing vacuously on an empty entry.
		require.NoError(t, reporter.SetFilterState(ctx, health.FilterModeNarrowedRelation, 3, ""))

		_, _, dec := p.ConsumerFilter()
		require.Equal(t, health.FilterModeBroad, dec.Mode,
			"this fixture must derive broad, or the fallback below would fire and report its own reason")

		require.Error(t, p.Rebuild(ctx, false), "an unmanaged consumer name must abandon the rebuild")

		entry, err := reporter.GetStatus(ctx)
		require.NoError(t, err)
		require.Equal(t, health.FilterModeNarrowedRelation, entry.FilterMode,
			"an abandoned rebuild adopted no filter — the lens still runs on the one it had, and the entry must still say so")
		require.Equal(t, 3, entry.FilterLabelCount)
		require.Empty(t, entry.FilterBroadReason)
	})

	t.Run("a narrowed derivation whose registration is refused reports registration-failed", func(t *testing.T) {
		// The other shape: filterSubjects is non-empty, so the fallback fires,
		// applies broad, retries, and fails again. registerWithFilterFallback's
		// own override is the honest last word here — the registration really
		// was refused — and it must survive the abandon rather than be replaced
		// by the narrowed derivation this rebuild started from.
		p, reporter := newAbandoningPipeline(t, "fd-abandon-narrow")
		p.engineKind = ruleengine.EngineFull
		p.plainReprojectLabels = map[string]struct{}{"book": {}}
		ctx := context.Background()

		_, _, dec := p.ConsumerFilter()
		require.Equal(t, health.FilterModeNarrowedLabel, dec.Mode)

		require.Error(t, p.Rebuild(ctx, false))

		entry, err := reporter.GetStatus(ctx)
		require.NoError(t, err)
		require.Equal(t, health.FilterModeBroad, entry.FilterMode)
		require.Equal(t, health.FilterBroadReasonRegistrationFailed, entry.FilterBroadReason,
			"the derivation must not overwrite the refusal that came after it")
		require.Zero(t, entry.FilterLabelCount)
	})
}

// TestRegisterWithFilterFallback_CleanRegistrationLeavesTheFootprintAlone is
// the negative vector for the test above. A registration that succeeds must not
// overwrite the derivation's own decision with registration-failed — the
// override belongs to the fallback path alone, and a version that wrote it
// unconditionally would report every narrowed lens in the fleet as refused.
func TestRegisterWithFilterFallback_CleanRegistrationLeavesTheFootprintAlone(t *testing.T) {
	reporter := newFallbackHealthReporter(t, "fd-regok")
	p := &Pipeline{ruleID: "fd-regok", reporter: reporter}
	ctx := context.Background()

	p.RecordFilterDecision(ctx, FilterDecision{Mode: health.FilterModeNarrowedLabel, LabelCount: 2})
	require.NoError(t, p.registerWithFilterFallback(ctx, []string{"a.>", "b.>"}, func() {}, func() error { return nil }))

	entry, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, health.FilterModeNarrowedLabel, entry.FilterMode)
	require.Equal(t, 2, entry.FilterLabelCount)
	require.Empty(t, entry.FilterBroadReason)
}

// TestDeclaresActorAnchor_RidesTheRulePublication proves the ordering guard's
// input is actually derived and published, and — the part a table row cannot
// reach — that it has the rule snapshot's LIFETIME rather than one of its own.
// A declaration left standing across a reload would refuse to narrow a lens the
// author has since rewritten as plain, permanently.
func TestDeclaresActorAnchor_RidesTheRulePublication(t *testing.T) {
	const anchored = `
MATCH (identity:identity {key: $actorKey})-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey, role.key AS r
`
	const plain = `
MATCH (u:unit)-[:managedBy]->(o:owner)
RETURN u.key AS key, o.name AS ownerName
`
	eng := full.New()
	parse := func(spec string) ruleengine.CompiledRule {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		return cr
	}

	t.Run("single walk", func(t *testing.T) {
		p, err := New("decl-single", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)

		require.NoError(t, p.UseFullEngine(eng, parse(anchored)))
		require.True(t, p.ruleState().declaresActorAnchor)

		// A reload to a plain body must clear it.
		require.NoError(t, p.UseFullEngine(eng, parse(plain)))
		require.False(t, p.ruleState().declaresActorAnchor,
			"a reload must not leave the previous rule body's declaration armed")
	})

	t.Run("multi walk — one branch is enough", func(t *testing.T) {
		// anchorHops is derived only on the single-walk arm, so a multi-walk
		// Personal lens would read as plain if the declaration were taken from
		// it. It is derived over every branch instead, and the anchored branch
		// is deliberately the SECOND one so a first-branch-only derivation fails
		// here.
		p, err := New("decl-multi", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)

		branches := []ruleengine.CompiledRule{parse(plain), parse(anchored)}
		require.NoError(t, p.UseFullEngineBranches(eng, branches[0], branches))
		require.True(t, p.ruleState().declaresActorAnchor)

		// And the trap that shape would walk into. The multi-walk arm publishes
		// NO pattern graph, so anchorHops stays at its zero value — whose Anchor
		// is 0, an ordinary valid position, not the -1 an anchorless index
		// carries. Reading the declaration off `anchorHops.Anchor >= 0` would
		// therefore call every non-full and every multi-walk pipeline
		// actor-anchored and refuse to narrow it forever.
		require.Zero(t, p.ruleState().anchorHops.Anchor)
		require.False(t, p.ruleState().anchorHops.Complete)
	})
}

// TestNarrowedFilterEligible_RefusesOnAnIncompleteInstall covers the exported
// eligibility probe, which is the third door onto the same derivation and the
// one with no health entry and no log line to make a wrong answer visible. It
// has no production caller, so this pins the property for the activation-path
// caller that acquires one: an eligibility computed off components that are not
// installed describes a lens the deployment does not have.
func TestNarrowedFilterEligible_RefusesOnAnIncompleteInstall(t *testing.T) {
	preInstall := &Pipeline{
		ruleID: "elig-preinstall", coreKVBucket: "core-kv",
		engineKind:           ruleengine.EngineFull,
		plainReprojectLabels: map[string]struct{}{"identity": {}},
		declaresActorAnchor:  true,
	}
	labels, ok := preInstall.NarrowedFilterEligible()
	require.False(t, ok,
		"a declared actor-anchored rule with no enumerator must not answer the PLAIN branch's eligibility")
	require.Nil(t, labels)

	// The same rule state on a lens that declares no anchor is a genuinely plain
	// lens, and the probe answers for it exactly as it does for every other.
	plain := &Pipeline{
		ruleID: "elig-plain", coreKVBucket: "core-kv",
		engineKind:           ruleengine.EngineFull,
		plainReprojectLabels: map[string]struct{}{"identity": {}},
	}
	labels, ok = plain.NarrowedFilterEligible()
	require.True(t, ok)
	require.Equal(t, map[string]struct{}{"identity": {}}, labels)
}
