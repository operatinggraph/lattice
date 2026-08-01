package pipeline

// D1 (refractor-footprint-reduction-design.md): narrowed Core KV
// FilterSubjects eligibility/derivation (NarrowedFilterEligible,
// ConsumerFilter) and the registration-error fallback
// (registerWithFilterFallback) — pure-function coverage, independent of any
// real compiled rule or supervisor. The e2e proof that a narrowed consumer
// actually narrows delivery, and that a real registration failure recovers
// end-to-end with a health signal recorded, lives in
// narrowed_filter_e2e_test.go (package pipeline_test).

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// TestNarrowedFilterEligible_Table pins NarrowedFilterEligible's contract
// directly against hand-built Pipeline state, mirroring
// TestPlainVertexRelevant_Table's style (vertex_relevance_internal_test.go)
// since eligibility here is deliberately the SAME data those gates already
// trust.
func TestNarrowedFilterEligible_Table(t *testing.T) {
	bookOnly := map[string]struct{}{"book": {}}

	cases := []struct {
		name       string
		engineKind string
		actorAware bool
		all        bool
		labels     map[string]struct{}
		wantOK     bool
	}{
		{
			name: "plain full-engine exhaustive labels is eligible",
			engineKind: ruleengine.EngineFull, labels: bookOnly, wantOK: true,
		},
		{
			name: "non-full engine is never eligible even with labels",
			labels: bookOnly, wantOK: false,
			// engineKind left zero-value ("") — deliberately not EngineFull.
		},
		{
			name: "non-exhaustive label set (plainReprojectAll) is never eligible",
			engineKind: ruleengine.EngineFull, all: true, wantOK: false,
		},
		{
			name: "actor-aware pipeline is never eligible regardless of labels",
			engineKind: ruleengine.EngineFull, labels: bookOnly, actorAware: true, wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{
				engineKind:           tc.engineKind,
				plainReprojectAll:    tc.all,
				plainReprojectLabels: tc.labels,
			}
			if tc.actorAware {
				p.actorEnumerator = &ActorEnumerator{}
			}
			labels, ok := p.NarrowedFilterEligible()
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.labels, labels)
			} else {
				require.Nil(t, labels)
			}
		})
	}
}

// TestConsumerFilter_Table pins ConsumerFilter's narrow-vs-broad decision,
// including the maxNarrowedFilterLabels cap and the coreKVBucket threading
// into both forms.
func TestConsumerFilter_Table(t *testing.T) {
	t.Run("eligible single label narrows", func(t *testing.T) {
		p := &Pipeline{
			coreKVBucket:         "core-kv",
			engineKind:           ruleengine.EngineFull,
			plainReprojectLabels: map[string]struct{}{"book": {}},
		}
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.ElementsMatch(t, []string{
			"$KV.core-kv.vtx.book.>", "$KV.core-kv.lnk.book.>", "$KV.core-kv.lnk.*.*.*.book.>",
		}, filterSubjects)
	})

	t.Run("ineligible falls back to the broad filter", func(t *testing.T) {
		p := &Pipeline{coreKVBucket: "core-kv"} // engineKind zero-value: not Full.
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubjects)
		require.Equal(t, "$KV.core-kv.>", filterSubject)
	})

	t.Run("label count past the cap falls back to the broad filter", func(t *testing.T) {
		labels := make(map[string]struct{}, maxNarrowedFilterLabels+1)
		for i := 0; i < maxNarrowedFilterLabels+1; i++ {
			labels[string(rune('a'+i))] = struct{}{}
		}
		p := &Pipeline{
			coreKVBucket:         "core-kv",
			engineKind:           ruleengine.EngineFull,
			plainReprojectLabels: labels,
		}
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubjects, "label count %d exceeds the cap of %d", len(labels), maxNarrowedFilterLabels)
		require.Equal(t, "$KV.core-kv.>", filterSubject)
	})

	t.Run("label count exactly at the cap still narrows", func(t *testing.T) {
		labels := make(map[string]struct{}, maxNarrowedFilterLabels)
		for i := 0; i < maxNarrowedFilterLabels; i++ {
			labels[string(rune('a'+i))] = struct{}{}
		}
		p := &Pipeline{
			coreKVBucket:         "core-kv",
			engineKind:           ruleengine.EngineFull,
			plainReprojectLabels: labels,
		}
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.Len(t, filterSubjects, maxNarrowedFilterLabels*3)
	})

	t.Run("empty exhaustive label set falls back to the broad filter", func(t *testing.T) {
		// Defensive: an exhaustive-but-empty set is not a real compiled-rule
		// shape (every lens has at least one MATCH node), but ConsumerFilter
		// must not hand JetStream an empty FilterSubjects slice — that is a
		// hard DeliverLastPerSubject config error, not a fail-safe no-op.
		p := &Pipeline{
			coreKVBucket:         "core-kv",
			engineKind:           ruleengine.EngineFull,
			plainReprojectLabels: map[string]struct{}{},
		}
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubjects)
		require.Equal(t, "$KV.core-kv.>", filterSubject)
	})
}

// TestRegisterWithFilterFallback_SuccessOnFirstTry proves the common case
// (no fallback machinery invoked at all) leaves register's result untouched
// and never calls applyBroad.
func TestRegisterWithFilterFallback_SuccessOnFirstTry(t *testing.T) {
	p := &Pipeline{ruleID: "rwff-ok"}
	calls := 0
	broadApplied := false
	err := p.registerWithFilterFallback(context.Background(), []string{"a.>"}, func() { broadApplied = true }, func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.False(t, broadApplied)
}

// TestRegisterWithFilterFallback_NarrowFailsBroadSucceeds proves the core
// contract: a first-attempt failure while filterSubjects is non-empty
// applies the broad filter and retries exactly once, returning the retry's
// (successful) result.
func TestRegisterWithFilterFallback_NarrowFailsBroadSucceeds(t *testing.T) {
	p := &Pipeline{ruleID: "rwff-fallback"}
	calls := 0
	broadApplied := false
	err := p.registerWithFilterFallback(context.Background(), []string{"a.>", "b.>"}, func() { broadApplied = true }, func() error {
		calls++
		if calls == 1 {
			return errors.New("injected overlapping-filter rejection")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls, "must retry exactly once after the narrowed attempt fails")
	require.True(t, broadApplied)
}

// TestRegisterWithFilterFallback_BothAttemptsFail proves a broad-filter
// failure too (the durable itself is unregistrable, not just the narrowed
// filter) propagates the SECOND attempt's error — the caller's own
// registration-failure handling (Run/Rebuild) is what a persistently-dark
// lens surfaces through, not a swallowed first error.
func TestRegisterWithFilterFallback_BothAttemptsFail(t *testing.T) {
	p := &Pipeline{ruleID: "rwff-bothfail"}
	calls := 0
	broadApplied := false
	errBroad := errors.New("broad filter also rejected")
	err := p.registerWithFilterFallback(context.Background(), []string{"a.>"}, func() { broadApplied = true }, func() error {
		calls++
		if calls == 1 {
			return errors.New("injected narrowed-filter rejection")
		}
		return errBroad
	})
	require.ErrorIs(t, err, errBroad)
	require.Equal(t, 2, calls)
	require.True(t, broadApplied)
}

// TestRegisterWithFilterFallback_BroadFilterNeverRetried proves the fallback
// is SCOPED to a narrowed attempt: when filterSubjects is empty (register was
// already trying the broad filter, e.g. an ineligible lens, or Rebuild after
// a prior fallback already applied it), a failure is returned as-is — no
// second attempt, no applyBroad call. registerWithFilterFallback is not a
// generic single-retry wrapper; retrying an already-broad failure would just
// repeat the identical call.
func TestRegisterWithFilterFallback_BroadFilterNeverRetried(t *testing.T) {
	p := &Pipeline{ruleID: "rwff-nobroadretry"}
	calls := 0
	broadApplied := false
	errFirst := errors.New("broad filter rejected")
	err := p.registerWithFilterFallback(context.Background(), nil, func() { broadApplied = true }, func() error {
		calls++
		return errFirst
	})
	require.ErrorIs(t, err, errFirst)
	require.Equal(t, 1, calls)
	require.False(t, broadApplied)
}
