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
	"bytes"
	"context"
	"errors"
	"log/slog"
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
			name:       "plain full-engine exhaustive labels is eligible",
			engineKind: ruleengine.EngineFull, labels: bookOnly, wantOK: true,
		},
		{
			name:   "non-full engine is never eligible even with labels",
			labels: bookOnly, wantOK: false,
			// engineKind left zero-value ("") — deliberately not EngineFull.
		},
		{
			name:       "non-exhaustive label set (plainReprojectAll) is never eligible",
			engineKind: ruleengine.EngineFull, all: true, wantOK: false,
		},
		{
			name:       "actor-aware pipeline meeting only the plain conditions is not eligible",
			engineKind: ruleengine.EngineFull, labels: bookOnly, actorAware: true, wantOK: false,
			// The plain branch's two conditions are necessary but nowhere near
			// sufficient for an actor-aware pipeline: §4.2 adds pattern-closure, a
			// sweep plan, the anchor type, and the decryptor. A bare enumerator
			// satisfies none of them, so this stays broad — see
			// TestNarrowedFilterEligible_ActorAwareIsTheFanOutGate for the
			// eligible direction.
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

// TestNarrowedFilterEligible_ActorAwareIsTheFanOutGate pins Increment 2's whole
// claim (auth-plane-projection-latency-design.md §4.6): for an actor-aware
// pipeline, "may the server withhold this event" and "may the fan-out arm skip
// it" are the same question, so eligibility and the label set must come from
// §4.2's conjunction — never a second derivation that could drift from the
// client gate the soundness argument rests on.
//
// eligiblePipeline (actor_aware_relevance_internal_test.go) is the shared
// fixture: it satisfies every §4.2 conjunct and asserts its own eligibility, so
// a knock-out below cannot pass vacuously.
func TestNarrowedFilterEligible_ActorAwareIsTheFanOutGate(t *testing.T) {
	t.Run("an eligible actor-aware pipeline narrows to its derived labels", func(t *testing.T) {
		p := eligiblePipeline(t)
		labels, ok := p.NarrowedFilterEligible()
		require.True(t, ok, "the §4.2 conjunction holds, so the consumer may narrow")
		require.Equal(t, identityRoleLabels(), labels)

		// The set the SERVER filters on and the set the fan-out arms judge by have
		// to be one set, asserted against the independently-stated expectation
		// above rather than against each other — comparing the two call sites
		// would hold under any regression that kept them equal and wrong.
		require.True(t, p.actorAwareFanOutRelevant(p.ruleState(), "identity"))
		require.False(t, p.actorAwareFanOutRelevant(p.ruleState(), "booking"),
			"a type the server would filter away must also be one the arms skip")
	})

	// Each conjunct independently forces the broad filter. The conjunct table in
	// actor_aware_relevance_internal_test.go proves this for the client gate;
	// these re-prove it through NarrowedFilterEligible, because a delivery-side
	// narrowing that outlives a failed conjunct is not recoverable by a code
	// revert (§8.3 — a JetStream filter update never rewinds the cursor).
	knockOuts := []struct {
		name     string
		knockOut func(p *Pipeline)
	}{
		{"non-exhaustive label set", func(p *Pipeline) { p.plainReprojectAll = true }},
		{"output is not pattern-closed", func(p *Pipeline) { p.patternClosedOutput = false }},
		{"no convergence sweep to heal what narrowing stops refreshing", func(p *Pipeline) { p.sweeper = nil }},
		{"anchor type outside the label set", func(p *Pipeline) {
			p.actorEnumerator = NewActorEnumerator(nil, nil, "service")
		}},
		{"secure lens whose declared holder type is outside the label set", func(p *Pipeline) {
			p.secureDecryptor = &SecureDecryptor{columns: []SecureColumn{{Column: "name", HolderTypes: []string{"identity"}}}}
			p.plainReprojectLabels = map[string]struct{}{"role": {}}
			p.actorEnumerator = NewActorEnumerator(nil, nil, "role")
		}},
	}
	for _, tc := range knockOuts {
		t.Run("broad filter when: "+tc.name, func(t *testing.T) {
			p := eligiblePipeline(t)
			p.coreKVBucket = "core-kv"
			tc.knockOut(p)

			labels, ok := p.NarrowedFilterEligible()
			require.False(t, ok)
			require.Nil(t, labels)

			filterSubjects, filterSubject, _ := p.ConsumerFilter()
			require.Empty(t, filterSubjects, "a failed conjunct must fall all the way back to the broad filter")
			require.Equal(t, "$KV.core-kv.>", filterSubject)
		})
	}
}

// TestConsumerFilter_ActorAwareNarrowsByLabelOnly pins the derivation end of
// Increment 2, in both dimensions.
//
// The LABEL dimension: an eligible actor-aware pipeline's filter set is the same
// three-forms-per-label expansion the plain path gets — the vertex form (which
// subsumes the label's aspect keys, Contract #1 §1.5) plus both link directions,
// which is the alignment the fan-out arms' own skip conditions require.
//
// The RELATION dimension: it must NOT apply, even when the compiled rule's
// relation set is exhaustive and would fit the subject budget. Relation
// narrowing is sound for a plain lens because plainLinkReactsTo already skips a
// link on an untraversed relation; the actor-aware link arm judges by endpoint
// type alone, so withholding by relation would deny it an event its client gate
// keeps. The subject that proves it is asserted by name below: a source-pinned
// relation form must never appear.
func TestConsumerFilter_ActorAwareNarrowsByLabelOnly(t *testing.T) {
	everyForm := []string{
		"$KV.core-kv.vtx.identity.>",
		"$KV.core-kv.lnk.identity.>",
		"$KV.core-kv.lnk.*.*.*.identity.>",
		"$KV.core-kv.vtx.role.>",
		"$KV.core-kv.lnk.role.>",
		"$KV.core-kv.lnk.*.*.*.role.>",
	}

	t.Run("relation-blind rule narrows to every form", func(t *testing.T) {
		p := eligiblePipeline(t)
		p.coreKVBucket = "core-kv"

		filterSubjects, filterSubject, _ := p.ConsumerFilter()
		require.Empty(t, filterSubject, "an eligible actor-aware pipeline must not fall back to broad")
		require.ElementsMatch(t, everyForm, filterSubjects)
	})

	t.Run("an exhaustive relation set does not narrow the link forms", func(t *testing.T) {
		p := eligiblePipeline(t)
		p.coreKVBucket = "core-kv"
		// What capabilityRoles actually derives: every traversed edge typed, so
		// the plain path here would relation-narrow.
		p.plainReprojectRelations = map[string]struct{}{"holdsRole": {}}
		p.plainRelationsExhaustive = true

		filterSubjects, filterSubject, _ := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.ElementsMatch(t, everyForm, filterSubjects,
			"an actor-aware lens narrows by label only — its link arm has no relation gate")
		require.NotContains(t, filterSubjects, "$KV.core-kv.lnk.identity.*.holdsRole.>",
			"a relation-pinned form would withhold a link on an untraversed relation whose endpoint type the fan-out arm still binds")
	})

	t.Run("the same rule on a plain pipeline does relation-narrow", func(t *testing.T) {
		// The control that keeps the assertion above from passing because
		// relation narrowing is broken outright rather than gated.
		p := eligiblePipeline(t)
		p.coreKVBucket = "core-kv"
		p.actorEnumerator = nil
		p.plainReprojectRelations = map[string]struct{}{"holdsRole": {}}
		p.plainRelationsExhaustive = true

		filterSubjects, filterSubject, _ := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.Contains(t, filterSubjects, "$KV.core-kv.lnk.identity.*.holdsRole.>")
	})
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
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.ElementsMatch(t, []string{
			"$KV.core-kv.vtx.book.>", "$KV.core-kv.lnk.book.>", "$KV.core-kv.lnk.*.*.*.book.>",
		}, filterSubjects)
	})

	t.Run("ineligible falls back to the broad filter", func(t *testing.T) {
		p := &Pipeline{coreKVBucket: "core-kv"} // engineKind zero-value: not Full.
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
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
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
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
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
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
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
		require.Empty(t, filterSubjects)
		require.Equal(t, "$KV.core-kv.>", filterSubject)
	})
}

// captureDefaultLogger swaps slog's default logger for one writing to a
// buffer, for the duration of the test, restoring the previous default on
// cleanup. Package-level slog is what ConsumerFilter's label-cap warning
// (and every other production log call in this package) actually writes
// through — there is no per-Pipeline injectable logger to substitute
// instead.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestConsumerFilter_LabelCapFallback_LogsWarn pins §14 Fire A item 5's
// audibility rule: a cap-driven fallback to the broad filter must log a
// slog.Warn naming the rule id and the count vs. the cap — unlike a failed
// registration, which signals via registerWithFilterFallback, this arm
// otherwise has no other signal anywhere. The not-eligible and empty arms —
// ordinary, frequent shapes most lenses take — must NOT log, or the signal
// would be drowned in noise.
func TestConsumerFilter_LabelCapFallback_LogsWarn(t *testing.T) {
	t.Run("label count past the cap logs a warning", func(t *testing.T) {
		buf := captureDefaultLogger(t)
		labels := make(map[string]struct{}, maxNarrowedFilterLabels+1)
		for i := 0; i < maxNarrowedFilterLabels+1; i++ {
			labels[string(rune('a'+i))] = struct{}{}
		}
		p := &Pipeline{
			ruleID:               "cap-warn-rule",
			coreKVBucket:         "core-kv",
			engineKind:           ruleengine.EngineFull,
			plainReprojectLabels: labels,
		}
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
		require.Empty(t, filterSubjects)
		require.Equal(t, "$KV.core-kv.>", filterSubject)

		logged := buf.String()
		require.Contains(t, logged, "cap-warn-rule")
		require.Contains(t, logged, "label count exceeds the cap")
	})

	t.Run("ineligible (not full engine) logs nothing", func(t *testing.T) {
		buf := captureDefaultLogger(t)
		p := &Pipeline{ruleID: "ineligible-rule", coreKVBucket: "core-kv"}
		_, filterSubject, _ := p.ConsumerFilter()
		require.Equal(t, "$KV.core-kv.>", filterSubject)
		require.Empty(t, buf.String(), "the not-eligible arm is an ordinary, frequent shape — it must not log")
	})

	t.Run("empty exhaustive label set logs nothing", func(t *testing.T) {
		buf := captureDefaultLogger(t)
		p := &Pipeline{
			ruleID:               "empty-rule",
			coreKVBucket:         "core-kv",
			engineKind:           ruleengine.EngineFull,
			plainReprojectLabels: map[string]struct{}{},
		}
		_, filterSubject, _ := p.ConsumerFilter()
		require.Equal(t, "$KV.core-kv.>", filterSubject)
		require.Empty(t, buf.String(), "an empty exhaustive set is not a cap overrun — it must not log")
	})

	t.Run("eligible and under the cap logs nothing", func(t *testing.T) {
		buf := captureDefaultLogger(t)
		p := &Pipeline{
			ruleID:               "under-cap-rule",
			coreKVBucket:         "core-kv",
			engineKind:           ruleengine.EngineFull,
			plainReprojectLabels: map[string]struct{}{"book": {}},
		}
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
		require.Empty(t, filterSubject)
		require.NotEmpty(t, filterSubjects)
		require.Empty(t, buf.String(), "the common eligible-and-narrowed path must gain nothing")
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
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
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
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
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
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
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
		filterSubjects, _, _ := p.ConsumerFilter()
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
		filterSubjects, filterSubject, _ := p.ConsumerFilter()
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
	require.True(t, narrowed.ruleState().plainLinkReactsTo("identifiedBy"))
	require.False(t, narrowed.ruleState().plainLinkReactsTo("providedTo"),
		"the exact live shape: a relation the lens never traverses")
	require.True(t, narrowed.ruleState().plainLinkReactsTo(""), "an unparsed relation defaults to relevant")

	noRels := &Pipeline{
		engineKind:               ruleengine.EngineFull,
		plainRelationsExhaustive: true,
	}
	require.False(t, noRels.ruleState().plainLinkReactsTo("anything"),
		"a lens with no relationship pattern reacts to no link at all")

	notExhaustive := &Pipeline{
		engineKind:              ruleengine.EngineFull,
		plainReprojectRelations: map[string]struct{}{"identifiedBy": {}},
	}
	require.True(t, notExhaustive.ruleState().plainLinkReactsTo("providedTo"))

	notFull := &Pipeline{
		plainReprojectRelations:  map[string]struct{}{"identifiedBy": {}},
		plainRelationsExhaustive: true,
	}
	require.True(t, notFull.ruleState().plainLinkReactsTo("providedTo"), "a non-full engine has no relation data to trust")
}
