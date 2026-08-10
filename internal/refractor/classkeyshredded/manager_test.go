package classkeyshredded

// Coverage of the retention-class destruction consumer's handler: which lenses
// it rebuilds, when it attests, and what it refuses to attest
// (retention-class-key-custody-design.md §6.3).
//
// The handler is driven directly rather than through JetStream — the durable
// wiring is substrate's, already covered there, and what matters here is the
// decision table.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	classHolder = "vtx.retentionclass.RCkeyHLDRabcdefghijk"
	lensSecure  = "rule-secure-clinical"
	lensOther   = "rule-secure-other"
	// attestingActor enables the finalization submit. The harness wires no Conn,
	// so a submit dereferences a nil connection and panics — which is what makes
	// "the attestation was withheld" an assertion rather than a comment.
	attestingActor = "vtx.identity.privacySvcActor0000"
)

// recordingRebuilder records which lenses were rebuilt, and can fail a chosen
// one. It stands in for the pipelines a live control service holds.
type recordingRebuilder struct {
	ruleID string
	failed *[]string
	built  *[]string
	err    error
}

func (r recordingRebuilder) Rebuild(context.Context, bool) error { return r.err }
func (r recordingRebuilder) RebuildAndWait(_ context.Context, truncate bool, wait time.Duration) error {
	if wait <= 0 {
		// Guarded here rather than left to review: this wait runs inside a
		// strictly serial durable handler, so an unbounded one stops every later
		// class-key destruction for the life of the process.
		panic("a destruction rebuild must pass a bounded wait")
	}
	if truncate {
		// Guarded here rather than left to review: truncating would delete every
		// row of a lens that also carries other classes' records, when the whole
		// point is to RETAIN the row and null only its plaintext.
		panic("a destruction rebuild must never truncate")
	}
	if r.err != nil {
		*r.failed = append(*r.failed, r.ruleID)
		return r.err
	}
	*r.built = append(*r.built, r.ruleID)
	return nil
}

// recordingPauser makes "was this lens paused?" observable, which is the whole
// question for an error class that must NOT pause.
type recordingPauser struct {
	ruleID string
	paused *[]string
}

func (p recordingPauser) Pause(context.Context) { *p.paused = append(*p.paused, p.ruleID) }

type harness struct {
	mgr    *Manager
	svc    *control.Service
	built  []string
	failed []string
	paused []string
}

func newHarness(t *testing.T, actorKey string) *harness {
	t.Helper()
	h := &harness{svc: control.NewService()}
	h.mgr = New(Config{Control: h.svc, ActorKey: actorKey})
	// Ready by default: the tests that care about readiness override it.
	h.mgr.SetRegistryReady(func(context.Context, string) error { return nil })
	return h
}

func (h *harness) register(ruleID string, err error) {
	h.svc.RegisterRebuilder(ruleID, recordingRebuilder{
		ruleID: ruleID, built: &h.built, failed: &h.failed, err: err,
	})
	h.svc.RegisterPauser(ruleID, recordingPauser{ruleID: ruleID, paused: &h.paused})
}

func eventFor(t *testing.T, holderKey string) substrate.Message {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"payload": map[string]any{"retentionClassKey": holderKey},
	})
	require.NoError(t, err)
	return substrate.Message{Body: body, Sequence: 42, NumDelivered: 1}
}

// The enumeration is by holder TYPE, so every lens the lister reports for
// "retentionclass" is rebuilt — including ones carrying other classes' rows.
// That over-rebuild is the fail-closed direction and the event is rare.
func TestHandle_RebuildsEveryLensDeclaringTheHolderType(t *testing.T) {
	h := newHarness(t, "")
	h.register(lensSecure, nil)
	h.register(lensOther, nil)
	h.mgr.SetTargetLister(func(holderType string) []RebuildTarget {
		assert.Equal(t, "retentionclass", holderType,
			"the holder type is derived from the destroyed holder's own key")
		return []RebuildTarget{{RuleID: lensSecure}, {RuleID: lensOther}}
	})

	dec := h.mgr.handleClassKeyShredded(context.Background(), eventFor(t, classHolder))

	assert.Equal(t, substrate.Ack, dec)
	assert.ElementsMatch(t, []string{lensSecure, lensOther}, h.built)
	assert.Equal(t, uint64(1), h.mgr.HandledTotal())
}

// No live lens declares the type → nothing holds the plaintext → the erasure is
// complete and MUST still be attested. Leaving it unattested would park the
// operator's retentionKeyStatus row forever on the strength of there being
// nothing to do.
//
// This is only sound because the registry is READY: an empty target set means
// "nothing declares this holder type" only when the enumeration it came from
// could see everything. The readiness tests below cover the other reading.
func TestHandle_NoDeclaringLensStillAttests(t *testing.T) {
	h := newHarness(t, "")
	h.mgr.SetTargetLister(func(string) []RebuildTarget { return nil })

	dec := h.mgr.handleClassKeyShredded(context.Background(), eventFor(t, classHolder))

	assert.Equal(t, substrate.Ack, dec)
	assert.Empty(t, h.built)
	assert.Equal(t, uint64(1), h.mgr.HandledTotal(),
		"a vacuously-clean destruction is still handled, not skipped")
}

// A lens whose rebuild genuinely failed may still be rendering plaintext whose
// key no longer exists. It is paused, and — the load-bearing half — the
// attestation is withheld, so nothing records an erasure that did not complete.
func TestHandle_AFailedRebuildWithholdsTheAttestation(t *testing.T) {
	h := newHarness(t, attestingActor)
	h.register(lensSecure, errors.New("target unreachable"))
	h.register(lensOther, nil)
	h.mgr.SetTargetLister(func(string) []RebuildTarget {
		return []RebuildTarget{{RuleID: lensSecure}, {RuleID: lensOther}}
	})

	dec := h.mgr.handleClassKeyShredded(context.Background(), eventFor(t, classHolder))

	assert.Equal(t, substrate.Ack, dec, "a privacy-critical failure is not auto-retried")
	assert.Equal(t, []string{lensSecure}, h.failed)
	assert.Equal(t, []string{lensOther}, h.built,
		"one lens's failure must not skip the remaining lenses")
	// allClean is false, so no finalization is submitted. The harness carries an
	// ActorKey (so the submit is enabled) and no Conn (so a submit would panic
	// on a nil connection) — reaching Ack without panicking is the assertion.
}

// A lens that exists in the corpus but has not registered yet (a Refractor
// still starting) is a RETRY, not a failure: rebuilding is idempotent, so
// re-attempting the ones that already succeeded costs a rescan, not correctness.
func TestHandle_UnregisteredLensRetriesRatherThanAttesting(t *testing.T) {
	h := newHarness(t, "")
	h.mgr.SetTargetLister(func(string) []RebuildTarget {
		return []RebuildTarget{{RuleID: "rule-not-yet-registered"}}
	})

	dec := h.mgr.handleClassKeyShredded(context.Background(), eventFor(t, classHolder))

	assert.Equal(t, substrate.NakWithDelay, dec)
	assert.Empty(t, h.built)
	assert.Zero(t, h.mgr.HandledTotal(), "a redelivery is not a completed handling")
}

// The redelivery budget is bounded, so a permanently-misconfigured RuleID stops
// nak-looping. It gives up LOUDLY and still withholds the attestation — the
// erasure is genuinely incomplete, and the stuck row is the signal.
func TestHandle_UnregisteredLensGivesUpAfterTheBudgetWithoutAttesting(t *testing.T) {
	h := newHarness(t, attestingActor)
	h.mgr.SetTargetLister(func(string) []RebuildTarget {
		return []RebuildTarget{{RuleID: "rule-never-registers"}}
	})

	msg := eventFor(t, classHolder)
	msg.NumDelivered = maxNotRegisteredDeliveries + 1
	dec := h.mgr.handleClassKeyShredded(context.Background(), msg)

	assert.Equal(t, substrate.Ack, dec, "the redelivery budget is exhausted")
	assert.Empty(t, h.built)
}

func TestHandle_MalformedEventsAreDroppedNotRetried(t *testing.T) {
	h := newHarness(t, "")
	h.mgr.SetTargetLister(func(string) []RebuildTarget {
		t.Fatal("a malformed event must not reach the enumeration")
		return nil
	})

	assert.Equal(t, substrate.Ack, h.mgr.handleClassKeyShredded(
		context.Background(), substrate.Message{}), "an empty body is nothing to do")

	assert.Equal(t, substrate.Term, h.mgr.handleClassKeyShredded(
		context.Background(), substrate.Message{Body: []byte("{not json")}))

	assert.Equal(t, substrate.Term, h.mgr.handleClassKeyShredded(
		context.Background(), eventFor(t, "")),
		"an event naming no holder can never be acted on, so redelivering it is pointless")

	assert.Equal(t, substrate.Term, h.mgr.handleClassKeyShredded(
		context.Background(), eventFor(t, "not-a-vertex-key")),
		"a holder key with no type segment yields no enumeration key")
}

func TestNew_PanicsWithoutAControlService(t *testing.T) {
	assert.Panics(t, func() { New(Config{}) },
		"every handler path dereferences Control; failing at construction beats panicking on the first real event")
}

// The registry is the enumeration's ground truth, so an incomplete one makes the
// target set SHORT, not merely empty — every lens that has not registered yet is
// silently absent from it. Attesting off that would record a complete erasure
// while the missing lenses kept serving plaintext, which is the vacuous-Ack
// failure the identity half's static floor target exists to prevent. A class
// holder can have no such floor (nothing obliges a lens to bind a retention
// class), so the readiness signal is explicit and gates the whole delivery.
func TestHandle_AnUnreconciledRegistryRetriesRatherThanEnumeratingOverIt(t *testing.T) {
	h := newHarness(t, attestingActor)
	h.mgr.SetRegistryReady(func(context.Context, string) error {
		return errors.New("2 lens(es) declaring this holder type are in Core KV but not registered")
	})
	h.register(lensSecure, nil)
	h.mgr.SetTargetLister(func(string) []RebuildTarget { return []RebuildTarget{{RuleID: lensSecure}} })

	dec := h.mgr.handleClassKeyShredded(context.Background(), eventFor(t, classHolder))

	assert.Equal(t, substrate.NakWithDelay, dec)
	assert.Empty(t, h.built, "the enumeration must not even run over a registry that cannot be trusted")
	assert.Zero(t, h.mgr.HandledTotal(), "a redelivery is not a completed handling")
}

// Absence of a readiness check is NOT readiness. The check is wired late in the
// Refractor's startup — after the lens source starts — while this consumer's
// durable is running from the first moment, and its DeliverAll policy replays
// the whole subject history. So the un-wired window is exactly when a vacuous
// attestation is both most likely and most wrong.
func TestHandle_AnUnwiredReadinessCheckIsTreatedAsNotReady(t *testing.T) {
	h := &harness{svc: control.NewService()}
	h.mgr = New(Config{Control: h.svc, ActorKey: attestingActor})
	h.mgr.SetTargetLister(func(string) []RebuildTarget { return nil })

	dec := h.mgr.handleClassKeyShredded(context.Background(), eventFor(t, classHolder))

	assert.Equal(t, substrate.NakWithDelay, dec,
		"an empty target set read off a registry with no readiness signal must never attest")
}

// The retry is bounded, because a registry that never reconciles is an operator
// condition (LensRegistryIncomplete), not something to nak-loop on forever. Past
// the budget it delivers to whatever IS registered — better than nothing — and
// still withholds the attestation, leaving the retentionKeyStatus row visibly
// in-flight, which is the honest reading of a registry this process cannot see
// all of.
func TestHandle_AnUnreconciledRegistryGivesUpAfterTheBudgetWithoutAttesting(t *testing.T) {
	h := newHarness(t, attestingActor)
	h.mgr.SetRegistryReady(func(context.Context, string) error { return errors.New("registry never reconciled") })
	h.register(lensSecure, nil)
	h.mgr.SetTargetLister(func(string) []RebuildTarget { return []RebuildTarget{{RuleID: lensSecure}} })

	msg := eventFor(t, classHolder)
	msg.NumDelivered = maxNotReadyDeliveries + 1
	dec := h.mgr.handleClassKeyShredded(context.Background(), msg)

	assert.Equal(t, substrate.Ack, dec, "the redelivery budget is exhausted")
	assert.Equal(t, []string{lensSecure}, h.built,
		"what IS registered is still rebuilt — the destruction reaches what it can reach")
	// No submit, so no panic on the nil Conn: the attestation is withheld.
}

// A cancelled context is a rolling deploy, not a lens fault. Acking here would
// advance the durable cursor past a destruction no lens was rebuilt for, with
// nothing left to redrive it: the projectionsRebuilt step would never be
// recorded and no later event would enumerate this holder again.
func TestHandle_AShutdownMidRebuildIsRetriedNotAcked(t *testing.T) {
	h := newHarness(t, attestingActor)
	h.register(lensSecure, context.Canceled)
	h.mgr.SetTargetLister(func(string) []RebuildTarget { return []RebuildTarget{{RuleID: lensSecure}} })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dec := h.mgr.handleClassKeyShredded(ctx, eventFor(t, classHolder))

	assert.Equal(t, substrate.NakWithDelay, dec,
		"a shutdown must redeliver the destruction to the next process, not lose it")
	assert.Zero(t, h.mgr.HandledTotal())
}

// The two "not known to be rebuilt, but not the lens's fault" classes must not
// reach the pause arm. Pausing here is self-perpetuating: a paused lens cannot
// drain a rebuild (Rebuild's supervisor reset requests a reopen without clearing
// a pause), so a lens whose rescan legitimately outran the budget once would
// burn the whole budget, time out and re-pause on every later destruction —
// serving its pre-destruction rows the entire time. That is the wedge the budget
// removed, relocated one arm down.
func TestHandle_ARebuildNotConfirmedDrainedWithholdsTheAttestationWithoutPausing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"wait budget expired", control.ErrRebuildWaitTimeout},
		{"ended without draining", control.ErrRebuildNotDrained},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, attestingActor)
			h.register(lensSecure, tc.err)
			h.mgr.SetTargetLister(func(string) []RebuildTarget {
				return []RebuildTarget{{RuleID: lensSecure}}
			})

			dec := h.mgr.handleClassKeyShredded(context.Background(), eventFor(t, classHolder))

			assert.Equal(t, substrate.Ack, dec)
			assert.Empty(t, h.paused,
				"a slow or unobserved rebuild is not a lens fault — pausing it makes the next destruction wedge too")
			// No submit, so no panic on the nil Conn: the attestation is withheld.
		})
	}
}

// A genuine rebuild failure still pauses, so the arm above is a narrowing and
// not a removal.
func TestHandle_ARealRebuildFailureStillPausesTheLens(t *testing.T) {
	h := newHarness(t, attestingActor)
	h.register(lensSecure, errors.New("target unreachable"))
	h.mgr.SetTargetLister(func(string) []RebuildTarget {
		return []RebuildTarget{{RuleID: lensSecure}}
	})

	dec := h.mgr.handleClassKeyShredded(context.Background(), eventFor(t, classHolder))

	assert.Equal(t, substrate.Ack, dec)
	assert.Equal(t, []string{lensSecure}, h.paused,
		"a lens that may still be rendering plaintext whose key is gone is halted")
}

// Both retry budgets are counted off the SAME msg.NumDelivered, so sizing them
// equally made the second dead: a boot that spent its deliveries on readiness
// left the first ErrRuleNotRegistered with zero retries. Staging them is what
// gives each a real window, and this is the delivery that proves the second one
// still has one.
func TestHandle_TheNotRegisteredBudgetSurvivesAnExhaustedReadinessBudget(t *testing.T) {
	h := newHarness(t, attestingActor)
	h.mgr.SetTargetLister(func(string) []RebuildTarget {
		return []RebuildTarget{{RuleID: "rule-not-yet-registered"}}
	})

	msg := eventFor(t, classHolder)
	msg.NumDelivered = maxNotReadyDeliveries + 1 // readiness is spent; this lens is not
	dec := h.mgr.handleClassKeyShredded(context.Background(), msg)

	assert.Equal(t, substrate.NakWithDelay, dec,
		"an unregistered lens must still get its own retries after readiness used up its own")
}
