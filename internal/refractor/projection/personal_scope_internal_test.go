package projection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/capabilityread"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	personalTestActorID = "Hj4kPmRtw9nbCxz5vQ2y"
	personalTestAnchor  = "vtx.task.Aj4kPmRtw9nbCxz5vQ2y"
	personalTestAnchorD = "Aj4kPmRtw9nbCxz5vQ2y"
	// scopeHolderID is a SECOND actor whose grants the tests read back as an
	// anchor set and then hand to the envelope as the evaluation's scope. An
	// AnchorSet is only constructible through ReadableAnchors, so building
	// one that disagrees with what the envelope's own actor holds live is how
	// the scoped path is told apart from the fallback.
	scopeHolderID = "Bj4kPmRtw9nbCxz5vQ2y"
)

// scopeParams builds the parameters the pipeline hands a scoped envelope: the
// evaluation's own actorKey plus whichever scope entries the case installs.
func scopeParams(entries map[string]any) map[string]any {
	params := map[string]any{"actorKey": personalTestActorKey}
	for k, v := range entries {
		params[k] = v
	}
	return params
}

// grantScope builds a real anchor set admitting exactly the named anchors, by
// granting them to scopeHolderID and reading that actor's set back. The
// envelope under test runs as personalTestActorID, so the set it is handed is
// independent of what its own actor holds live.
func grantScope(t *testing.T, capKV *substrate.KV, anchors ...string) *capabilityread.AnchorSet {
	t.Helper()
	for _, a := range anchors {
		putPerAnchorEntry(t, capKV, "identity."+scopeHolderID, a)
	}
	set, err := capabilityread.ReadableAnchors(context.Background(), capKV, "identity", scopeHolderID)
	if err != nil {
		t.Fatalf("ReadableAnchors: %v", err)
	}
	return set
}

// TestPersonalEnvelope_ScopeWinsOverKV is the proof that the scope is what the
// gates actually answer from, in BOTH directions and for BOTH gates: a scope
// that admits publishes a row the live KV would deny, and a scope that denies
// skips a row the live KV would admit. Agreeing fixtures could not tell the
// scoped path from the fallback.
func TestPersonalEnvelope_ScopeWinsOverKV(t *testing.T) {
	row := map[string]any{"anchor": personalTestAnchor, "kind": "task"}

	t.Run("grant gate: scope admits what the KV denies", func(t *testing.T) {
		// The bucket holds a grant on this anchor for a DIFFERENT actor, so
		// the envelope's own live read denies while the scope admits.
		capKV := newPersonalTestBucket(t, "capability-kv")
		fn := personalEnvelopeFn(nil, capKV, discardTestLogger())

		params := scopeParams(map[string]any{
			personalScopeGrantsParam: grantScope(t, capKV, personalTestAnchorD),
		})
		newRow, newKeys, err := fn(row, nil, params)
		if err != nil {
			t.Fatalf("the scope's admission must decide the row, got err %v", err)
		}
		if newRow["anchor"] != personalTestAnchor {
			t.Fatalf("row must pass through: %v", newRow)
		}
		if newKeys[adapter.PersonalActorKeyField] != personalTestActorID {
			t.Fatalf("recipient must be injected: %v", newKeys)
		}
	})

	t.Run("grant gate: scope denies what the KV admits", func(t *testing.T) {
		capKV := newPersonalTestBucket(t, "capability-kv")
		putPerAnchorEntry(t, capKV, "identity."+personalTestActorID, personalTestAnchorD)
		fn := personalEnvelopeFn(nil, capKV, discardTestLogger())

		params := scopeParams(map[string]any{
			personalScopeGrantsParam: grantScope(t, capKV), // the scope's holder was granted nothing
		})
		if _, _, err := fn(row, nil, params); !errors.Is(err, pipeline.ErrSkipProjection) {
			t.Fatalf("the scope's denial must decide the row even though the live grant exists, got %v", err)
		}
	})

	t.Run("interest gate: scope admits what the KV denies", func(t *testing.T) {
		interestKV := newPersonalTestBucket(t, "personal-lens-interest")
		if err := personalinterest.Register(context.Background(), interestKV, personalTestActorID, "device1",
			[]string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("register: %v", err)
		}
		fn := personalEnvelopeFn(interestKV, nil, discardTestLogger())

		params := scopeParams(map[string]any{
			personalScopeInterestParam: []personalinterest.Registration{{Types: []string{"task"}}},
		})
		if _, _, err := fn(row, nil, params); err != nil {
			t.Fatalf("the scope's registrations must decide relevance, got err %v", err)
		}
	})

	t.Run("interest gate: scope denies what the KV admits", func(t *testing.T) {
		interestKV := newPersonalTestBucket(t, "personal-lens-interest")
		if err := personalinterest.Register(context.Background(), interestKV, personalTestActorID, "device1",
			[]string{"task"}, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("register: %v", err)
		}
		fn := personalEnvelopeFn(interestKV, nil, discardTestLogger())

		params := scopeParams(map[string]any{
			personalScopeInterestParam: []personalinterest.Registration{{Types: []string{"lease"}}},
		})
		if _, _, err := fn(row, nil, params); !errors.Is(err, pipeline.ErrSkipProjection) {
			t.Fatalf("the scope's registrations must decline the delta even though the live filter admits, got %v", err)
		}
	})

	t.Run("security still wins over relevance under the scope", func(t *testing.T) {
		capKV := newPersonalTestBucket(t, "capability-kv")
		interestKV := newPersonalTestBucket(t, "personal-lens-interest")
		fn := personalEnvelopeFn(interestKV, capKV, discardTestLogger())

		params := scopeParams(map[string]any{
			personalScopeGrantsParam:   grantScope(t, capKV), // no anchor admitted
			personalScopeInterestParam: []personalinterest.Registration{{Types: []string{"task"}}},
		})
		if _, _, err := fn(row, nil, params); !errors.Is(err, pipeline.ErrSkipProjection) {
			t.Fatalf("an unreadable anchor must be denied even when the scope's registrations find it relevant, got %v", err)
		}
	})
}

// TestPersonalEnvelope_FallsBackWithoutScope pins that an envelope handed no
// scope entries decides exactly as it always has, from the live KV — the
// property that keeps every unscoped caller (and every existing test) correct.
func TestPersonalEnvelope_FallsBackWithoutScope(t *testing.T) {
	row := map[string]any{"anchor": personalTestAnchor, "kind": "task"}

	t.Run("grant gate reads the live KV", func(t *testing.T) {
		capKV := newPersonalTestBucket(t, "capability-kv")
		fn := personalEnvelopeFn(nil, capKV, discardTestLogger())

		if _, _, err := fn(row, nil, scopeParams(nil)); !errors.Is(err, pipeline.ErrSkipProjection) {
			t.Fatalf("with no scope and no live grant the row must be denied, got %v", err)
		}
		putPerAnchorEntry(t, capKV, "identity."+personalTestActorID, personalTestAnchorD)
		if _, _, err := fn(row, nil, scopeParams(nil)); err != nil {
			t.Fatalf("with no scope a live grant must admit, got %v", err)
		}
	})

	t.Run("interest gate reads the live KV", func(t *testing.T) {
		interestKV := newPersonalTestBucket(t, "personal-lens-interest")
		if err := personalinterest.Register(context.Background(), interestKV, personalTestActorID, "device1",
			[]string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("register: %v", err)
		}
		fn := personalEnvelopeFn(interestKV, nil, discardTestLogger())

		if _, _, err := fn(row, nil, scopeParams(nil)); !errors.Is(err, pipeline.ErrSkipProjection) {
			t.Fatalf("with no scope the live registration must decline this delta, got %v", err)
		}
	})

	t.Run("a scope entry of the wrong type is not a scope", func(t *testing.T) {
		capKV := newPersonalTestBucket(t, "capability-kv")
		putPerAnchorEntry(t, capKV, "identity."+personalTestActorID, personalTestAnchorD)
		fn := personalEnvelopeFn(nil, capKV, discardTestLogger())

		params := scopeParams(map[string]any{personalScopeGrantsParam: "not-an-anchor-set"})
		if _, _, err := fn(row, nil, params); err != nil {
			t.Fatalf("an unusable entry must fall back to the live read, not deny silently, got %v", err)
		}
	})
}

// TestPersonalEnvelopeScope_ReadsBothGatesForTheActor drives the installed
// scope function itself against live buckets: it emits one entry per WIRED
// gate, scoped to the evaluation's own actor, and emits nothing for a gate
// whose handle is nil (which is what leaves that gate's fallback in charge).
func TestPersonalEnvelopeScope_ReadsBothGatesForTheActor(t *testing.T) {
	capKV := newPersonalTestBucket(t, "capability-kv")
	interestKV := newPersonalTestBucket(t, "personal-lens-interest")
	putPerAnchorEntry(t, capKV, "identity."+personalTestActorID, personalTestAnchorD)
	putPerAnchorEntry(t, capKV, "identity."+scopeHolderID, "Cj4kPmRtw9nbCxz5vQ2y")
	if err := personalinterest.Register(context.Background(), interestKV, personalTestActorID, "device1",
		[]string{"task"}, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("register: %v", err)
	}

	scope, err := personalEnvelopeScope(interestKV, capKV)(context.Background(), map[string]any{"actorKey": personalTestActorKey})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	grants, ok := scope[personalScopeGrantsParam].(*capabilityread.AnchorSet)
	if !ok {
		t.Fatalf("a wired cap gate must emit an anchor set, got %T", scope[personalScopeGrantsParam])
	}
	if !grants.Admits(personalTestAnchorD) {
		t.Fatalf("the actor's own grant must be in the set")
	}
	if grants.Admits("Cj4kPmRtw9nbCxz5vQ2y") {
		t.Fatalf("another actor's grant must not be in this actor's set")
	}
	regs, ok := scope[personalScopeInterestParam].([]personalinterest.Registration)
	if !ok {
		t.Fatalf("a wired interest gate must emit registrations, got %T", scope[personalScopeInterestParam])
	}
	if !personalinterest.RelevantIn(regs, "task", personalTestAnchor) {
		t.Fatalf("the registered device's declared type must read as relevant")
	}

	capOnly, err := personalEnvelopeScope(nil, capKV)(context.Background(), map[string]any{"actorKey": personalTestActorKey})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if _, present := capOnly[personalScopeInterestParam]; present {
		t.Fatalf("a nil interest handle must emit no interest entry, leaving that gate exactly as InstallPersonalLens wired it")
	}

	interestOnly, err := personalEnvelopeScope(interestKV, nil)(context.Background(), map[string]any{"actorKey": personalTestActorKey})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if _, present := interestOnly[personalScopeGrantsParam]; present {
		t.Fatalf("a nil cap handle must emit no grant entry")
	}
}

// TestPersonalEnvelopeScope_MalformedActorKeyErrors pins the fail-closed arm:
// the scope refuses an actorKey it cannot parse rather than emitting an empty
// set, which would deny every row of the evaluation with no diagnosis. An
// empty actorKey emits nothing — the envelope declines that row itself.
func TestPersonalEnvelopeScope_MalformedActorKeyErrors(t *testing.T) {
	capKV := newPersonalTestBucket(t, "capability-kv")
	fn := personalEnvelopeScope(nil, capKV)

	if _, err := fn(context.Background(), map[string]any{"actorKey": "not-a-vertex-key"}); err == nil {
		t.Fatalf("a malformed actorKey must error, not silently scope to nothing")
	}
	scope, err := fn(context.Background(), map[string]any{"actorKey": ""})
	if err != nil {
		t.Fatalf("an absent actorKey is the envelope's own skip case, not a scope failure: %v", err)
	}
	if len(scope) != 0 {
		t.Fatalf("an absent actorKey must emit no scope entries, got %v", scope)
	}
}

// TestPersonalEnvelopeScope_KVFailurePropagates pins that a gate that could
// not be READ fails the evaluation rather than producing an empty set: an
// empty grant set silently denies every row of a wide actor, which on the
// security plane reads as a successful evaluation that published nothing.
// Both a cancelled parent and an already-expired deadline take that arm — the
// scope derives its own bounded ctx from the caller's, and a derived ctx
// inherits the parent's cancellation.
func TestPersonalEnvelopeScope_KVFailurePropagates(t *testing.T) {
	capKV := newPersonalTestBucket(t, "capability-kv")
	interestKV := newPersonalTestBucket(t, "personal-lens-interest")
	params := map[string]any{"actorKey": personalTestActorKey}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if scope, err := personalEnvelopeScope(nil, capKV)(cancelled, params); err == nil {
		t.Fatalf("a cancelled ctx must fail the grant scope, not yield %v", scope)
	}

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	if scope, err := personalEnvelopeScope(nil, capKV)(expired, params); err == nil {
		t.Fatalf("an expired deadline must fail the grant scope, not yield %v", scope)
	}
	if scope, err := personalEnvelopeScope(interestKV, nil)(expired, params); err == nil {
		t.Fatalf("an expired deadline must fail the interest scope, not yield %v", scope)
	}
}

// TestPersonalScopeReadTimeout_IsBounded pins that the scope's own ceiling is
// actually a ceiling. The read it bounds is a wide multi-get for every actor
// that matters — a filter resolution plus a chunk of exact-key requests, each
// with a bound of its own but no bound on their SUM — and it runs on the
// evaluation's context with the lens's consumer stalled behind it. A zero value
// here would remove the only ceiling over that sum, because
// context.WithTimeout(ctx, 0) yields an already-expired ctx, and any non-zero
// misreading (a bare `15`, nanoseconds) would too.
func TestPersonalScopeReadTimeout_IsBounded(t *testing.T) {
	if personalScopeReadTimeout <= 0 {
		t.Fatalf("the scope read timeout must be positive; %v disables the bound", personalScopeReadTimeout)
	}
	if personalScopeReadTimeout < time.Second {
		t.Fatalf("the scope read timeout %v is shorter than the widest live actor's read; it would fail every wide evaluation", personalScopeReadTimeout)
	}
	if personalScopeReadTimeout >= 80*time.Second {
		t.Fatalf("the scope read timeout %v is long enough to stall the lens's consumer rather than bound it", personalScopeReadTimeout)
	}
}

// TestInstallPersonalLens_InstallsEnvelopeScope pins the wiring: a personal
// lens installs the per-evaluation scope alongside its envelope, so the gates
// are answered once per actor on the shipped path and not only in tests that
// build the scope by hand.
func TestInstallPersonalLens_InstallsEnvelopeScope(t *testing.T) {
	r := personalTestRule(t, personalMatch, lens.KeyField{adapter.PersonalActorKeyField, "anchor"})
	p := newPersonalPipeline(t)
	if p.HasEnvelopeScope() {
		t.Fatalf("a fresh pipeline must carry no envelope scope, or the assertion below is vacuous")
	}

	if !InstallPersonalLens(p, r, nil, nil, nil, nil, false, discardTestLogger()) {
		t.Fatalf("a well-formed personal lens must install")
	}
	if !p.HasEnvelopeScope() {
		t.Fatalf("InstallPersonalLens must install the per-evaluation gate scope beside the envelope")
	}

	// The ORDER is load-bearing: installing an envelope clears any scope, so
	// a SetEnvelopeScope that ran first would leave the lens unscoped and
	// every gate back on its per-row read — a silent performance regression
	// with no failing assertion anywhere else.
	p.SetEnvelopeFn(personalEnvelopeFn(nil, nil, discardTestLogger()))
	if p.HasEnvelopeScope() {
		t.Fatalf("installing an envelope must clear the scope, or InstallPersonalLens's ordering is not load-bearing and this pin is vacuous")
	}
	if !InstallPersonalLens(p, r, nil, nil, nil, nil, false, discardTestLogger()) {
		t.Fatalf("re-install must succeed")
	}
	if !p.HasEnvelopeScope() {
		t.Fatalf("InstallPersonalLens must set the scope AFTER the envelope")
	}
}
