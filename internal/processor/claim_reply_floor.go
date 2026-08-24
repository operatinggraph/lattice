package processor

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// DefaultClaimRejectionFloor is the reply floor applied to a ClaimIdentity
// rejection when Deps.ClaimRejectionFloor is left at zero.
//
// NFR-S6 collapses every ClaimIdentity rejection to one wire shape
// (ErrCodeClaimKeyInvalid, no details) so the reply itself cannot tell a caller
// which cause it hit. What the wire shape cannot hide is TIME: the three causes
// do measurably different amounts of work before answering — absent-target
// hydrates three missing keys; already-claimed hydrates three present keys but
// takes decryptSensitiveDoc's IsDeleted arm (sensitive_decrypt.go), which
// returns without readPiiKeyEnvelope or Vault.Decrypt; wrong-key pays both, and
// then the secret comparison. The resulting per-cause bias is a few tenths of a
// millisecond, monotone in that order, and extractable from a few dozen replies
// by averaging — which is an identity-existence oracle on an endpoint every
// consumer holds.
//
// The constant is sized against LOADED tail latency, not service time. A floor
// masks the causes only while it exceeds actual latency, and an attacker can
// generate the load that pushes latency past a small floor — so a floor sized
// against the ~2.9 ms unloaded worst case would be exceeded under ordinary
// concurrency and the full gap would reappear. Measured under concurrent
// submission the rejection path runs ~9.5 ms mean, ~12 ms p90, ~17 ms p99;
// 50 ms leaves roughly 3x headroom over that p99 while staying imperceptible on
// what is already an interactive error path.
const DefaultClaimRejectionFloor = 50 * time.Millisecond

// maxPendingDeferredReplies bounds how many floored replies may be waiting out
// their floor at once, so a flood of rejections cannot grow goroutines and
// buffered reply bodies without limit.
const maxPendingDeferredReplies = 1024

// replyPublisher is the minimal publish surface the floor needs. *nats.Conn
// satisfies it; tests substitute a recorder.
type replyPublisher interface {
	Publish(subject string, data []byte) error
}

// claimReplyFloor holds a marshalled rejection reply until a fixed offset from
// the operation's receipt has elapsed, then publishes it from its own
// goroutine. It owns no durable state and has no shutdown drain: a reply was
// never durable (replyTo's contract is that the commit is already durable and a
// failed reply is observability-only), so deferring one widens an
// already-lossy window rather than creating a new durability class. The shape
// mirrors AuthTraceEmitter.Emit (step3_auth_trace.go) — capture everything
// synchronously off the caller's stack frame, do the rest on a goroutine, log
// failures.
type claimReplyFloor struct {
	floor   time.Duration
	pending atomic.Int64
	logger  *slog.Logger
}

// newClaimReplyFloor constructs the floor. A floor of zero or less disables
// deferral: publishNoEarlierThan then publishes on the caller's goroutine.
func newClaimReplyFloor(floor time.Duration, logger *slog.Logger) *claimReplyFloor {
	if logger == nil {
		logger = slog.Default()
	}
	return &claimReplyFloor{floor: floor, logger: logger}
}

// publishNoEarlierThan publishes body on subject no earlier than
// receipt + floor, returning immediately.
//
// receipt is the instant the message entered dispatch, captured before any work
// whose duration depends on the target's state. Anchoring there is the entire
// mechanism: the answer then lands at a fixed offset from ARRIVAL, so the
// variable work between arrival and rejection is not observable in the reply's
// timing. Anchoring the wait at the rejection instead would produce
// floor + work and leave the per-cause difference completely intact.
//
// The wait runs on its own goroutine, never on the caller's. The caller is a
// lane pump worker, of which a lane has 2-4 (lanes.go's LaneConsumerDefaults),
// so parking the floor there would cap the whole lane — every operation on it,
// not just the floored ones — at workers * (1s/floor) operations per second,
// with the attacker choosing how much of that budget to burn.
func (f *claimReplyFloor) publishNoEarlierThan(pub replyPublisher, subject string, body []byte, receipt time.Time) {
	if f.floor <= 0 {
		f.publish(pub, subject, body)
		return
	}
	deadline := receipt.Add(f.floor)
	if !time.Now().Before(deadline) {
		// The path already took longer than the floor, so there is nothing left
		// to hold: publish on the caller's goroutine rather than pay a
		// goroutine to wait zero.
		f.publish(pub, subject, body)
		return
	}
	if n := f.pending.Add(1); n > maxPendingDeferredReplies {
		f.pending.Add(-1)
		// Dropping is the fail-safe direction, and answering early is not an
		// option: an early answer restores the timing signal at exactly the
		// moment an attacker is generating the load that saturated the bound,
		// which is when they are measuring. The caller times out instead.
		f.logger.Warn("claim-rejection floor: pending deferred replies at bound; dropping reply",
			"subject", subject, "bound", maxPendingDeferredReplies)
		return
	}
	go func() {
		defer f.pending.Add(-1)
		// Computed from the deadline rather than from the remaining duration
		// measured at call time, so the goroutine's own scheduling delay is
		// absorbed by the wait instead of added to it. deadline inherits
		// receipt's monotonic reading, so a wall-clock step cannot move it.
		if d := time.Until(deadline); d > 0 {
			timer := time.NewTimer(d)
			defer timer.Stop()
			<-timer.C
		}
		f.publish(pub, subject, body)
	}()
}

// publish sends the reply body, logging a failure rather than surfacing it.
func (f *claimReplyFloor) publish(pub replyPublisher, subject string, body []byte) {
	if err := pub.Publish(subject, body); err != nil {
		f.logger.Warn("reply publish failed", "subject", subject, "error", err)
	}
}
