package pipeline

// D1 (refractor-footprint-reduction-design.md): narrowed Core KV
// FilterSubjects eligibility/derivation (NarrowedFilterEligible,
// ConsumerFilter) and the registration-error fallback
// (registerWithFilterFallback), including its health-signal side effects
// (RecordError on fallback, ClearLastError on a clean first attempt) —
// coverage independent of any real compiled rule or supervisor, using
// registerWithFilterFallback's own register/applyBroad callback hooks rather
// than a live consumer. The e2e proof that a narrowed consumer actually
// narrows delivery, and that a real registration failure recovers end-to-end
// against a real nats-server rejection, lives in narrowed_filter_e2e_test.go
// (package pipeline_test).

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
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

// newFallbackHealthReporter stands up an in-memory NATS server with a single
// HEALTH KV bucket and returns a Reporter for ruleID — the minimal fixture the
// tests below need to observe registerWithFilterFallback's real
// RecordError/ClearLastError effects, with no Core/Adj KV, no supervisor, and
// no real consumer (mirrors output_collision_test.go's newCollisionKVs).
func newFallbackHealthReporter(t *testing.T, ruleID string) *health.Reporter {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "HEALTH"})
	require.NoError(t, err)
	kv, err := conn.OpenKV(context.Background(), "HEALTH")
	require.NoError(t, err)
	return health.New(kv, ruleID)
}

// TestRegisterWithFilterFallback_CleanSuccessClearsStaleFault proves item 1 of
// FIRE 2: a clean first attempt (register succeeds, no fallback fired) clears
// a stale LastError an EARLIER boot's narrowed-filter fallback latched on this
// same health entry, while the cumulative ErrorCount survives untouched
// (health-kv-schema.md's "preserved across restarts" contract).
func TestRegisterWithFilterFallback_CleanSuccessClearsStaleFault(t *testing.T) {
	reporter := newFallbackHealthReporter(t, "rwff-clear")
	require.NoError(t, reporter.RecordError(context.Background(),
		"narrowed Core KV filter registration failed, fell back to the broad filter: injected stale rejection"))
	require.NoError(t, reporter.RecordError(context.Background(), "second stale error"))

	p := &Pipeline{ruleID: "rwff-clear", reporter: reporter}
	calls := 0
	err := p.registerWithFilterFallback(context.Background(), []string{"a.>"}, func() {
		t.Fatal("applyBroad must not be called on a clean first-attempt success")
	}, func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)

	entry, gerr := reporter.GetStatus(context.Background())
	require.NoError(t, gerr)
	require.Nil(t, entry.LastError, "a clean registration must clear the stale latch")
	require.Equal(t, uint64(2), entry.ErrorCount, "errorCount must survive the clear")
}

// TestRegisterWithFilterFallback_FallbackSuccessKeepsFreshError is the
// counter-case (item 2): when the narrowed attempt itself fails and the
// broad retry is what succeeds, the fallback's own fresh error must survive —
// only a clean FIRST-attempt success clears, never a recovered fallback.
func TestRegisterWithFilterFallback_FallbackSuccessKeepsFreshError(t *testing.T) {
	reporter := newFallbackHealthReporter(t, "rwff-keeps-fresh")

	p := &Pipeline{ruleID: "rwff-keeps-fresh", reporter: reporter}
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
	require.Equal(t, 2, calls)
	require.True(t, broadApplied)

	entry, gerr := reporter.GetStatus(context.Background())
	require.NoError(t, gerr)
	require.NotNil(t, entry.LastError, "the fallback's fresh error must survive — the clear must not fire")
	require.Contains(t, *entry.LastError, "injected overlapping-filter rejection")
	require.Equal(t, uint64(1), entry.ErrorCount)
}

// TestRegisterWithFilterFallback_CleanSuccessDoesNotUnpauseAPersistedPause
// proves item 3: the clear-on-success call is scoped to LastError alone and
// never touches Status/PauseReason, so a persisted pause survives a clean
// registration exactly as it did before this call existed — the hazard a
// SetActive call here would have created (Pipeline.Run's doc comment: the
// supervisor's restoreState reads Status/PauseReason at startup to decide
// whether to honor a persisted pause, from the pump goroutine Add/Reset
// spawns concurrently with registerWithFilterFallback's caller).
func TestRegisterWithFilterFallback_CleanSuccessDoesNotUnpauseAPersistedPause(t *testing.T) {
	reporter := newFallbackHealthReporter(t, "rwff-paused")
	require.NoError(t, reporter.SetPaused(context.Background(), health.PauseReasonStructural, "bucket not found"))

	p := &Pipeline{ruleID: "rwff-paused", reporter: reporter}
	err := p.registerWithFilterFallback(context.Background(), []string{"a.>"}, func() {
		t.Fatal("applyBroad must not be called on a clean first-attempt success")
	}, func() error { return nil })
	require.NoError(t, err)

	entry, gerr := reporter.GetStatus(context.Background())
	require.NoError(t, gerr)
	require.Equal(t, "paused", entry.Status, "a clean durable registration must not flip a persisted pause active")
	require.NotNil(t, entry.PauseReason)
	require.Equal(t, health.PauseReasonStructural, *entry.PauseReason)
}

// TestConsumerFilter_RelationNarrowing pins ConsumerFilter's THREE-step
// degradation: relation-narrowed when the relation set is exhaustive and the
// subject count fits, relation-blind narrowed when either fails, broad when
// the label set itself is ineligible.
func TestConsumerFilter_RelationNarrowing(t *testing.T) {
	t.Run("exhaustive relations pin the link forms", func(t *testing.T) {
		p := &Pipeline{
			coreKVBucket:             "core-kv",
			engineKind:               ruleengine.EngineFull,
			plainReprojectLabels:     map[string]struct{}{"provider": {}},
			plainReprojectRelations:  map[string]struct{}{"identifiedBy": {}},
			plainRelationsExhaustive: true,
		}
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.ElementsMatch(t, []string{
			"$KV.core-kv.vtx.provider.>",
			"$KV.core-kv.lnk.provider.*.identifiedBy.>",
			"$KV.core-kv.lnk.*.*.identifiedBy.provider.>",
		}, filterSubjects)
	})

	t.Run("an exhaustive EMPTY relation set subscribes to no link form", func(t *testing.T) {
		p := &Pipeline{
			coreKVBucket:             "core-kv",
			engineKind:               ruleengine.EngineFull,
			plainReprojectLabels:     map[string]struct{}{"patient": {}},
			plainRelationsExhaustive: true,
		}
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.Equal(t, []string{"$KV.core-kv.vtx.patient.>"}, filterSubjects)
	})

	t.Run("a non-exhaustive relation set keeps the relation-blind forms", func(t *testing.T) {
		p := &Pipeline{
			coreKVBucket:            "core-kv",
			engineKind:              ruleengine.EngineFull,
			plainReprojectLabels:    map[string]struct{}{"book": {}},
			plainReprojectRelations: map[string]struct{}{"wrote": {}},
			// plainRelationsExhaustive false: an untyped hop somewhere.
		}
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.ElementsMatch(t, []string{
			"$KV.core-kv.vtx.book.>", "$KV.core-kv.lnk.book.>", "$KV.core-kv.lnk.*.*.*.book.>",
		}, filterSubjects)
	})

	// The ZERO VALUE must read as "not exhaustive", never as "exhaustive and
	// empty" — the latter is the strongest narrowing there is, and a Pipeline
	// that never ran UseFullEngineBranches must not get it by accident.
	t.Run("the zero-value pipeline never relation-narrows", func(t *testing.T) {
		p := &Pipeline{
			coreKVBucket:         "core-kv",
			engineKind:           ruleengine.EngineFull,
			plainReprojectLabels: map[string]struct{}{"book": {}},
		}
		filterSubjects, _ := p.ConsumerFilter()
		require.Len(t, filterSubjects, 3, "relation-blind forms, not a vertex-only set")
	})

	t.Run("a relation set past the subject budget falls back to relation-blind", func(t *testing.T) {
		labels := map[string]struct{}{"a": {}, "b": {}, "c": {}, "d": {}}
		relations := map[string]struct{}{"r1": {}, "r2": {}, "r3": {}}
		// 4 x (1 + 2*3) = 28 > maxNarrowedFilterSubjects (24).
		p := &Pipeline{
			coreKVBucket:             "core-kv",
			engineKind:               ruleengine.EngineFull,
			plainReprojectLabels:     labels,
			plainReprojectRelations:  relations,
			plainRelationsExhaustive: true,
		}
		filterSubjects, filterSubject := p.ConsumerFilter()
		require.Empty(t, filterSubject, "over the SUBJECT budget degrades to relation-blind, not to broad")
		require.Len(t, filterSubjects, len(labels)*3)
	})
}

// TestPlainLinkReactsTo pins the client-side relation gate — load-bearing for
// any lens that keeps a broader filter than its relation set would allow
// (over the subject budget, or actor-aware). Every uncertain input defaults to
// relevant, exactly as plainReactsTo does.
func TestPlainLinkReactsTo(t *testing.T) {
	narrowed := &Pipeline{
		engineKind:               ruleengine.EngineFull,
		plainReprojectRelations:  map[string]struct{}{"identifiedBy": {}},
		plainRelationsExhaustive: true,
	}
	require.True(t, narrowed.plainLinkReactsTo("identifiedBy"))
	require.False(t, narrowed.plainLinkReactsTo("providedTo"),
		"the exact live shape: a relation the lens never traverses")
	require.True(t, narrowed.plainLinkReactsTo(""), "an unparsed relation defaults to relevant")

	noRels := &Pipeline{
		engineKind:               ruleengine.EngineFull,
		plainRelationsExhaustive: true,
	}
	require.False(t, noRels.plainLinkReactsTo("anything"),
		"a lens with no relationship pattern reacts to no link at all")

	notExhaustive := &Pipeline{
		engineKind:              ruleengine.EngineFull,
		plainReprojectRelations: map[string]struct{}{"identifiedBy": {}},
	}
	require.True(t, notExhaustive.plainLinkReactsTo("providedTo"))

	notFull := &Pipeline{
		plainReprojectRelations:  map[string]struct{}{"identifiedBy": {}},
		plainRelationsExhaustive: true,
	}
	require.True(t, notFull.plainLinkReactsTo("providedTo"), "a non-full engine has no relation data to trust")
}
