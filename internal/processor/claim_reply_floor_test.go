package processor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// ---- unit-tier fixtures for claimReplyFloor itself ----

// recordingPublisher captures every publish the floor performs, stamping the
// instant each one landed so a test can assert on release ordering and offsets.
type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
	at       []time.Time
	count    atomic.Int64
}

func (p *recordingPublisher) Publish(subject string, _ []byte) error {
	p.mu.Lock()
	p.subjects = append(p.subjects, subject)
	p.at = append(p.at, time.Now())
	p.mu.Unlock()
	p.count.Add(1)
	return nil
}

func (p *recordingPublisher) snapshot() ([]string, []time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.subjects...), append([]time.Time(nil), p.at...)
}

// countingHandler is a slog.Handler that counts WARN-and-above records whose
// message contains a substring, so a test can assert the drop path logged
// without scraping stderr.
type countingHandler struct {
	needle string
	hits   atomic.Int64
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn && strings.Contains(r.Message, h.needle) {
		h.hits.Add(1)
	}
	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

// ---- commit-path fixtures ----

// claimRejectHydrator fails step 4 with the ClaimKeyInvalid ScriptError every
// ClaimIdentity rejection cause surfaces as, after spending `work` inside the
// step.
//
// `work` is a deliberate, fixed stand-in for the per-cause hydration work the
// floor exists to hide — an absent target hydrates three missing keys, an
// already-claimed one hydrates three present keys and skips the envelope read
// plus AEAD decrypt, a wrong key pays both. It is not synchronising anything:
// its DURATION is the quantity under test, which is exactly why the sleep is
// fixed rather than condition-driven.
type claimRejectHydrator struct {
	work   time.Duration
	detail string
}

func (h claimRejectHydrator) Hydrate(_ context.Context, env *OperationEnvelope) (HydratedState, error) {
	if h.work > 0 {
		time.Sleep(h.work)
	}
	return HydratedState{}, &ScriptError{
		Code:               "ClaimKeyInvalid",
		Message:            "claim rejected",
		Detail:             h.detail,
		OperationRequestID: env.RequestID,
	}
}

// plainRejectHydrator fails step 4 with an ordinary script fault — a rejection
// class that reflects the submitter's own request rather than the target's
// state, and therefore is NOT floored.
type plainRejectHydrator struct{}

func (plainRejectHydrator) Hydrate(_ context.Context, env *OperationEnvelope) (HydratedState, error) {
	return HydratedState{}, &ScriptError{
		Code:               "ScriptError",
		Message:            "boom",
		OperationRequestID: env.RequestID,
	}
}

// noopCommitter satisfies the non-nil Committer requirement for pipelines whose
// operations always fail before step 8.
type noopCommitter struct{}

func (noopCommitter) Commit(context.Context, *OperationEnvelope, ScriptResult, Tracker) (CommitAck, error) {
	return CommitAck{}, errors.New("noopCommitter: step 8 is unreachable in this fixture")
}

// floorTestPipeline builds a CommitPath over the shared embedded-NATS harness
// whose step 4 always rejects via hydrator, with the given reply floor.
func floorTestPipeline(t *testing.T, conn *substrate.Conn, hydrator Hydrator, floor time.Duration) *CommitPath {
	t.Helper()
	return NewCommitPath(Deps{
		Conn:                conn,
		CoreBucket:          testCoreBucket,
		HealthKV:            testHealthBucket,
		Authorizer:          NewStubAuthorizer(testLogger()),
		Hydrator:            hydrator,
		Committer:           noopCommitter{},
		Metrics:             &Metrics{},
		Logger:              testLogger(),
		ClaimRejectionFloor: floor,
	})
}

// claimFloorReplyWait bounds the wait for a reply the commit path has already
// been driven to produce. A ceiling, not a timing assumption — hitting it is a
// dropped or never-published reply, not a slow runner.
const claimFloorReplyWait = 30 * time.Second

// dispatchAndTimeReply drives one envelope through cp.dispatch and returns the
// interval between the instant BEFORE dispatch was entered and the arrival of
// the reply.
//
// Measuring from before dispatch (rather than from the receipt instant dispatch
// captures internally) is the conservative direction for a floor-holds
// assertion: t0 <= receipt, so an observed offset that clears D bounds the
// real receipt-to-publish offset from below by at least D minus one function
// call.
func dispatchAndTimeReply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *CommitPath, env *OperationEnvelope) (time.Duration, MessageOutcome) {
	t.Helper()
	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe %s: %v", inbox, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := conn.NATS().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	msg := messageFromEnvelope(t, env)
	msg.ReplySubject = inbox

	t0 := time.Now()
	outcome, _ := cp.dispatch(ctx, msg)
	if _, err := sub.NextMsg(claimFloorReplyWait); err != nil {
		t.Fatalf("no reply on %s within %s (outcome %q): %v", inbox, claimFloorReplyWait, outcome, err)
	}
	return time.Since(t0), outcome
}

// claimEnvelope builds a ClaimIdentity envelope with a fresh requestId so each
// submission is a genuine first delivery rather than a step-2 dedup hit.
func claimEnvelope(t *testing.T) *OperationEnvelope {
	t.Helper()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("nanoid: %v", err)
	}
	env := newTestEnvelope(id)
	env.OperationType = "ClaimIdentity"
	return env
}

// TestClaimRejectionFloor_HoldsForEveryCause pins the invariant for all three
// NFR-S6 rejection causes: none of them is answered before the floor has
// elapsed. The causes are modelled by their Health-KV outcome detail plus the
// step-4 work each one does, since what distinguishes them on the wire is
// nothing and what distinguishes them in time is exactly that work.
func TestClaimRejectionFloor_HoldsForEveryCause(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// Small but real, so the assertion is about the mechanism rather than about
	// scheduler noise.
	const floor = 200 * time.Millisecond

	causes := []struct {
		name   string
		detail string
		work   time.Duration
	}{
		{"absent-target", "no-target", 0},
		{"already-claimed", "wrong-state", 10 * time.Millisecond},
		{"wrong-key", "invalid-key", 20 * time.Millisecond},
	}
	for _, c := range causes {
		t.Run(c.name, func(t *testing.T) {
			cp := floorTestPipeline(t, conn, claimRejectHydrator{work: c.work, detail: c.detail}, floor)
			elapsed, outcome := dispatchAndTimeReply(t, ctx, conn, cp, claimEnvelope(t))
			if outcome != OutcomeRejected {
				t.Fatalf("outcome = %q, want rejected", outcome)
			}
			if elapsed < floor {
				t.Fatalf("%s answered after %s, before the %s floor — the rejection is released early",
					c.name, elapsed, floor)
			}
		})
	}
}

// TestClaimRejectionFloor_AnchoredAtReceiptNotAtRejection is the test that
// proves the fix rather than merely the delay. Two rejections whose internal
// work differs by an amount far larger than scheduler noise must be answered at
// the same offset from ARRIVAL.
//
// An implementation that waited the floor from the moment the rejection was
// reached would answer the slow arm at floor+work and the fast arm at floor,
// reproducing the very per-cause difference the floor exists to erase; that
// implementation fails here by roughly injectedWork.
func TestClaimRejectionFloor_AnchoredAtReceiptNotAtRejection(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	const floor = 400 * time.Millisecond
	const injectedWork = 150 * time.Millisecond
	// Comfortably below injectedWork (so a post-work anchor cannot slip
	// through) and comfortably above the goroutine-wakeup and NATS round-trip
	// jitter the two arms each pay.
	const tolerance = 60 * time.Millisecond

	fast := floorTestPipeline(t, conn, claimRejectHydrator{work: 0, detail: "no-target"}, floor)
	slow := floorTestPipeline(t, conn, claimRejectHydrator{work: injectedWork, detail: "invalid-key"}, floor)

	fastElapsed, _ := dispatchAndTimeReply(t, ctx, conn, fast, claimEnvelope(t))
	slowElapsed, _ := dispatchAndTimeReply(t, ctx, conn, slow, claimEnvelope(t))

	if fastElapsed < floor || slowElapsed < floor {
		t.Fatalf("floor breached: fast=%s slow=%s, floor=%s", fastElapsed, slowElapsed, floor)
	}
	gap := slowElapsed - fastElapsed
	if gap < 0 {
		gap = -gap
	}
	if gap > tolerance {
		t.Fatalf("the %s of injected step-4 work is visible in the reply timing: fast=%s slow=%s gap=%s > %s — "+
			"the floor is anchored at the rejection, not at receipt",
			injectedWork, fastElapsed, slowElapsed, gap, tolerance)
	}
}

// TestClaimRejectionFloor_BoundDropsRatherThanAnsweringEarly pins the fail-safe
// direction at saturation. Answering early instead would hand back the timing
// signal at exactly the moment an attacker is generating the load that
// saturated the bound — which is when they are measuring.
func TestClaimRejectionFloor_BoundDropsRatherThanAnsweringEarly(t *testing.T) {
	t.Parallel()

	const floor = 500 * time.Millisecond
	const excess = 200
	total := maxPendingDeferredReplies + excess

	handler := &countingHandler{needle: "dropping reply"}
	f := newClaimReplyFloor(floor, slog.New(handler))
	pub := &recordingPublisher{}

	start := time.Now()
	for i := 0; i < total; i++ {
		f.publishNoEarlierThan(pub, "inbox.bound", []byte(`{}`), start)
	}

	// Every submission reaches a terminal disposition — published or dropped —
	// so wait on that condition rather than on a duration.
	deadline := time.Now().Add(claimFloorReplyWait)
	for {
		published := int(pub.count.Load())
		dropped := int(handler.hits.Load())
		if published+dropped >= total {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d deferred replies settled (published=%d dropped=%d)",
				published+dropped, total, published, dropped)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := int(pub.count.Load()); got != maxPendingDeferredReplies {
		t.Fatalf("published %d replies, want exactly the bound (%d)", got, maxPendingDeferredReplies)
	}
	if got := int(handler.hits.Load()); got != excess {
		t.Fatalf("logged %d drop warnings, want %d", got, excess)
	}

	// The replies that WERE published must still have honoured the floor: the
	// bound must not become a shortcut past it.
	_, at := pub.snapshot()
	floorAt := start.Add(floor)
	for i, ts := range at {
		if ts.Before(floorAt) {
			t.Fatalf("reply %d published %s before the floor elapsed", i, floorAt.Sub(ts))
		}
	}
	if f.pending.Load() != 0 {
		t.Fatalf("pending = %d after every reply settled, want 0", f.pending.Load())
	}
}

// TestClaimRejectionFloor_ConfigPaths pins the two documented Deps values: zero
// takes the production default, negative disables the mechanism outright (the
// posture the timing instrument needs to observe the raw per-cause gap).
func TestClaimRejectionFloor_ConfigPaths(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	t.Run("zero takes the default", func(t *testing.T) {
		cp := floorTestPipeline(t, conn, claimRejectHydrator{detail: "no-target"}, 0)
		if cp.deps.ClaimRejectionFloor != DefaultClaimRejectionFloor {
			t.Fatalf("deps.ClaimRejectionFloor = %s, want the default %s",
				cp.deps.ClaimRejectionFloor, DefaultClaimRejectionFloor)
		}
		if cp.claimFloor.floor != DefaultClaimRejectionFloor {
			t.Fatalf("claimFloor.floor = %s, want %s", cp.claimFloor.floor, DefaultClaimRejectionFloor)
		}
		elapsed, _ := dispatchAndTimeReply(t, ctx, conn, cp, claimEnvelope(t))
		if elapsed < DefaultClaimRejectionFloor {
			t.Fatalf("answered after %s, before the default %s floor", elapsed, DefaultClaimRejectionFloor)
		}
	})

	t.Run("negative disables", func(t *testing.T) {
		cp := floorTestPipeline(t, conn, claimRejectHydrator{detail: "no-target"}, -1)
		if cp.deps.ClaimRejectionFloor != -1 {
			t.Fatalf("deps.ClaimRejectionFloor = %s, want the -1ns disable sentinel preserved",
				cp.deps.ClaimRejectionFloor)
		}
		elapsed, _ := dispatchAndTimeReply(t, ctx, conn, cp, claimEnvelope(t))
		if elapsed >= DefaultClaimRejectionFloor {
			t.Fatalf("answered after %s with the floor disabled — a disabled floor must not delay the reply", elapsed)
		}
	})
}

// TestMakePipeline_LeavesClaimRejectionFloorEnabled pins the composition root.
// The floor is a security mechanism with no production knob, so the property
// that matters is that the deployed wiring cannot silently regress to off:
// MakePipeline is what cmd/processor/main.go calls, and it must yield a
// CommitPath whose floor is the ratified default.
func TestMakePipeline_LeavesClaimRejectionFloorEnabled(t *testing.T) {
	t.Parallel()
	_, conn, _, _, _ := setupTestPipeline(t)

	cp, _, err := MakePipeline(conn, testCoreBucket, testHealthBucket, "", AuthModeStub, false,
		testLogger(), "proc-floor-pin", AuthWiring{}, nil)
	if err != nil {
		t.Fatalf("MakePipeline: %v", err)
	}
	if cp.deps.ClaimRejectionFloor != DefaultClaimRejectionFloor {
		t.Fatalf("production wiring built a CommitPath with ClaimRejectionFloor = %s, want %s",
			cp.deps.ClaimRejectionFloor, DefaultClaimRejectionFloor)
	}
	if cp.claimFloor == nil || cp.claimFloor.floor <= 0 {
		t.Fatalf("production wiring left the claim rejection floor disabled")
	}
}

// TestClaimRejectionFloor_NonFlooredClassesStayFast pins the scope of the
// mechanism. An auth denial, a malformed envelope, and an ordinary script fault
// all reflect properties of the submitter's own request rather than of the
// claimed target, so they carry no identity information and must not pay the
// floor.
func TestClaimRejectionFloor_NonFlooredClassesStayFast(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// Large enough that any accidental flooring of these classes is unmissable.
	const floor = 3 * time.Second
	// A generous ceiling for an in-process dispatch plus one embedded-NATS
	// round trip, and two orders of magnitude under `floor`, so this asserts
	// "not floored" rather than "fast".
	const fastCeiling = 500 * time.Millisecond

	t.Run("auth denied", func(t *testing.T) {
		cp := NewCommitPath(Deps{
			Conn:                conn,
			CoreBucket:          testCoreBucket,
			HealthKV:            testHealthBucket,
			Authorizer:          denyAuthorizer{},
			Committer:           noopCommitter{},
			Metrics:             &Metrics{},
			Logger:              testLogger(),
			ClaimRejectionFloor: floor,
		})
		elapsed, outcome := dispatchAndTimeReply(t, ctx, conn, cp, claimEnvelope(t))
		if outcome != OutcomeRejected {
			t.Fatalf("outcome = %q, want rejected", outcome)
		}
		if elapsed > fastCeiling {
			t.Fatalf("auth denial answered after %s (ceiling %s) — it is paying the claim rejection floor", elapsed, fastCeiling)
		}
	})

	t.Run("malformed envelope", func(t *testing.T) {
		cp := floorTestPipeline(t, conn, claimRejectHydrator{detail: "no-target"}, floor)
		inbox := nats.NewInbox()
		sub, err := conn.NATS().SubscribeSync(inbox)
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		defer func() { _ = sub.Unsubscribe() }()
		if err := conn.NATS().Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		t0 := time.Now()
		outcome, _ := cp.dispatch(ctx, substrate.Message{Body: []byte(`{"lane":"banana"}`), ReplySubject: inbox})
		if _, err := sub.NextMsg(claimFloorReplyWait); err != nil {
			t.Fatalf("no reply (outcome %q): %v", outcome, err)
		}
		if elapsed := time.Since(t0); elapsed > fastCeiling {
			t.Fatalf("malformed rejection answered after %s (ceiling %s) — it is paying the claim rejection floor", elapsed, fastCeiling)
		}
	})

	t.Run("ordinary script fault", func(t *testing.T) {
		cp := floorTestPipeline(t, conn, plainRejectHydrator{}, floor)
		elapsed, outcome := dispatchAndTimeReply(t, ctx, conn, cp, claimEnvelope(t))
		if outcome != OutcomeRejected {
			t.Fatalf("outcome = %q, want rejected", outcome)
		}
		if elapsed > fastCeiling {
			t.Fatalf("ScriptFailed rejection answered after %s (ceiling %s) — it is paying the claim rejection floor", elapsed, fastCeiling)
		}
	})
}

// TestClaimReplyFloor_PublishesInlineWhenWorkOutranTheFloor pins the
// already-late case: when the path took longer than the floor there is nothing
// left to hold, so the reply goes out on the caller's goroutine and no pending
// slot is consumed.
func TestClaimReplyFloor_PublishesInlineWhenWorkOutranTheFloor(t *testing.T) {
	t.Parallel()

	f := newClaimReplyFloor(10*time.Millisecond, testLogger())
	pub := &recordingPublisher{}
	f.publishNoEarlierThan(pub, "inbox.late", []byte(`{}`), time.Now().Add(-time.Second))

	if got := pub.count.Load(); got != 1 {
		t.Fatalf("published %d replies synchronously, want 1", got)
	}
	if f.pending.Load() != 0 {
		t.Fatalf("pending = %d, want 0 — an already-late reply must not consume a deferral slot", f.pending.Load())
	}
}
