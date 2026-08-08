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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	classHolder = "vtx.retentionclass.RCkeyHLDRabcdefghijk"
	lensSecure  = "rule-secure-clinical"
	lensOther   = "rule-secure-other"
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
func (r recordingRebuilder) RebuildAndWait(_ context.Context, truncate bool) error {
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

type harness struct {
	mgr    *Manager
	svc    *control.Service
	built  []string
	failed []string
}

func newHarness(t *testing.T, actorKey string) *harness {
	t.Helper()
	h := &harness{svc: control.NewService()}
	h.mgr = New(Config{Control: h.svc, ActorKey: actorKey})
	return h
}

func (h *harness) register(ruleID string, err error) {
	h.svc.RegisterRebuilder(ruleID, recordingRebuilder{
		ruleID: ruleID, built: &h.built, failed: &h.failed, err: err,
	})
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
	h := newHarness(t, "")
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
	// allClean is false, so no finalization is submitted. With no Conn wired, a
	// submit attempt would panic — reaching Ack without one is the assertion.
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
	h := newHarness(t, "")
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
