package processor

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultClaimRejectionFloor is the release quantum applied to a rejection of an
// NFR-S6 operation when Deps.ClaimRejectionFloor is left at zero.
//
// NFR-S6 collapses every rejection of these operations to one wire shape
// (ErrCodeClaimKeyInvalid, no details) so the reply itself cannot tell a caller
// which cause it hit. What the wire shape cannot hide is TIME: the causes do
// measurably different amounts of work before answering — an absent target
// hydrates three missing keys; an already-claimed one hydrates three present
// keys but takes decryptSensitiveDoc's IsDeleted arm (sensitive_decrypt.go),
// which returns without readPiiKeyEnvelope or Vault.Decrypt; a wrong key pays
// both, and then the secret comparison. The resulting per-cause bias is a few
// tenths of a millisecond, monotone in that order, and extractable from a few
// dozen replies by averaging — which is an identity-existence oracle on an
// endpoint every consumer holds.
//
// The constant is sized against LOADED tail latency, not service time. A
// quantum masks the causes only while the work fits inside it, and an attacker
// can generate the load that pushes latency past a small one — so a value sized
// against the ~2.9 ms unloaded worst case would be exceeded under ordinary
// concurrency. Measured under concurrent submission the rejection path runs
// ~9.5 ms mean, ~12 ms p90, ~17 ms p99; 50 ms leaves roughly 3x headroom over
// that p99 while staying imperceptible on what is already an interactive error
// path.
const DefaultClaimRejectionFloor = 50 * time.Millisecond

// nfrS6Operations is the operation set whose rejections NFR-S6 requires to be
// indistinguishable — in wire shape and in time alike.
//
// Membership means both of these, together:
//
//   - EVERY rejection of the operation answers ErrCodeClaimKeyInvalid with nil
//     details and one fixed message, whatever actually failed. Contract #9
//     (docs/contracts/09-identity-claim-flow.md) states it without
//     qualification: "All failure modes collapse to the generic
//     ClaimKeyInvalid reply code (NFR-S6 anti-enumeration); specific outcomes
//     surface only via Health KV." The real code, message and details go to the
//     Processor log and the Health-KV claim-attempts counter — never to the
//     caller.
//   - EVERY rejection of the operation is released on a quantized offset from
//     receipt, so the differing service times behind those identical replies are
//     not readable either.
//
// The set is keyed on operationType and deliberately NOT on the error code the
// failure happened to produce. Keying on the code left two holes. A step-4
// hydrate or decrypt fault on the operation's own declared keys returns a bare
// fmt.Errorf (step4_hydrate.go), classifies as ErrCodeInternalError, and so
// escaped both halves — leaving a sealed-but-unclaimed identity distinguishable
// from a non-existent one by wire code AND by timing. And it made
// CompleteCredentialLink's coverage accidental: that operation qualifies today
// only because its script reuses ClaimIdentity's "ClaimKeyInvalid: " fail prefix
// (identity-domain/ddls.go's fail_link), so narrowing a code-keyed predicate to
// ClaimIdentity alone would have silently uncovered a real enumeration oracle
// without failing a single test.
var nfrS6Operations = map[string]struct{}{
	"ClaimIdentity":          {},
	"CompleteCredentialLink": {},
}

// isNFRS6Operation reports whether this operationType's rejections must be
// collapsed and quantized. See nfrS6Operations for what membership means.
func isNFRS6Operation(operationType string) bool {
	_, ok := nfrS6Operations[operationType]
	return ok
}

// claimRejectionMessage is the single message every NFR-S6 rejection carries.
//
// It names no step, no key and no underlying error, because each of those is
// itself an oracle: the step name alone separates a script refusal from a
// hydrate fault, and a hydrate fault's text quotes the very key the caller was
// probing (step4_hydrate.go wraps as "step4: decrypt <key>: ...").
const claimRejectionMessage = "claim key invalid"

// claimOutcomeInternalFault is the Health-KV claim-attempts outcome recorded
// when an NFR-S6 operation is rejected by a fault the script never reached — a
// hydrate, decrypt, validate or encrypt failure. The caller cannot tell it from
// an ordinary refusal, by construction, so this counter is the only place it
// becomes visible as something an operator should look at.
const claimOutcomeInternalFault = "internal-fault"

// maxPendingDeferredReplies bounds how many deferred replies may be waiting out
// their quantum at once, so a flood of rejections cannot grow goroutines and
// buffered reply bodies without limit.
const maxPendingDeferredReplies = 1024

// claimFloorDropLogEvery rate-limits the bound-hit warning to one record per N
// drops. Saturating the bound is attacker-reachable — ClaimIdentity is
// `scope: self` and nothing rate-limits it — so a warning per dropped reply
// would hand a caller a log-amplification lever. Metrics.ClaimFloorDropped is
// the lossless signal; the log exists to put a human-readable line next to it.
const claimFloorDropLogEvery = 64

// replyPublisher is the minimal publish surface the floor needs. *nats.Conn
// satisfies it; tests substitute a recorder.
type replyPublisher interface {
	Publish(subject string, data []byte) error
}

// claimReplyFloor holds a marshalled rejection reply until a quantized offset
// from the operation's receipt has elapsed, then publishes it from its own
// goroutine.
//
// The shape mirrors AuthTraceEmitter.Emit (step3_auth_trace.go) — capture
// everything synchronously off the caller's stack frame, do the rest on a
// goroutine, log failures — with one addition it does not need: a WaitGroup, so
// a shutting-down process can drain the replies it has already accepted instead
// of discarding up to maxPendingDeferredReplies of them on every SIGTERM.
//
// Nothing here is durable. A reply was never durable (replyTo's contract is that
// the commit is already durable and a failed reply is observability-only), so
// deferring one widens an already-lossy window rather than creating a new
// durability class. Deferred-reply state is created per rejection, carried only
// in the waiting goroutine, and flushed by Drain at shutdown; nothing survives
// process exit and nothing is replayed on the next start.
type claimReplyFloor struct {
	quantum time.Duration
	pending atomic.Int64
	drops   atomic.Uint64
	wg      sync.WaitGroup
	metrics *Metrics
	logger  *slog.Logger
}

// newClaimReplyFloor constructs the floor. A quantum of zero or less disables
// deferral: publishNoEarlierThan then publishes on the caller's goroutine.
// metrics may be nil, in which case no counters are recorded.
func newClaimReplyFloor(quantum time.Duration, metrics *Metrics, logger *slog.Logger) *claimReplyFloor {
	if logger == nil {
		logger = slog.Default()
	}
	return &claimReplyFloor{quantum: quantum, metrics: metrics, logger: logger}
}

// releaseAt returns the instant a reply received at `receipt` and finished at
// `done` may be published: the first multiple of the quantum after `receipt`
// that is not before `done`.
//
// Quantizing rather than flooring is what makes the mechanism hold against a
// caller who can lengthen its own request. A plain floor answers
// max(receipt+quantum, done) — so any request whose work outruns the quantum is
// answered at `done` and leaks its full service time. That is one request away:
// opwire.MaxDeclaredReads allows 1000 declared reads, the Gateway copies
// contextHint into the envelope verbatim and step 3 never inspects it, and every
// declared read resolves inside Hydrate — in one batched KVGetMulti whose cost
// grows with the declared set — i.e. inside the window. A caller pads declared
// reads until the work exceeds the quantum and then reads the raw per-cause
// timing straight back, with no load and no coordination.
//
// Under quantization there is no such escape hatch: the answer always lands on a
// lattice of fixed offsets from ARRIVAL (receipt+Q, receipt+2Q, ...), so what a
// caller learns is only which quantum its request fell in — a number it already
// controls by padding, and one that carries no information about the target.
// Ordinary traffic (2-10 ms against a 50 ms quantum) lands in the first quantum,
// so nothing changes for it.
func releaseAt(receipt, done time.Time, quantum time.Duration) time.Time {
	elapsed := done.Sub(receipt)
	// Integer ceiling division; n is at least 1 so a reply is never answered
	// before receipt+quantum, including when done precedes receipt.
	n := int64(1)
	if elapsed > 0 {
		n = int64(elapsed/quantum) + 1
		if elapsed%quantum == 0 {
			n = int64(elapsed / quantum)
		}
	}
	return receipt.Add(time.Duration(n) * quantum)
}

// publishNoEarlierThan publishes body on subject at the next quantum boundary
// after the work finished, returning immediately.
//
// receipt is the instant the message entered dispatch, captured before any work
// whose duration depends on the target's state. Anchoring there is the entire
// mechanism: the answer then lands at a fixed offset from ARRIVAL, so the
// variable work between arrival and rejection is not observable in the reply's
// timing. Anchoring at the rejection instead would produce quantum + work and
// leave the per-cause difference completely intact.
//
// The wait runs on its own goroutine, never on the caller's. The caller is a
// lane pump worker, of which a lane has 2-4 (lanes.go's LaneConsumerDefaults),
// so parking the wait there would cap the whole lane — every operation on it,
// not just the deferred ones — at workers * (1s/quantum) operations per second,
// with the attacker choosing how much of that budget to burn.
func (f *claimReplyFloor) publishNoEarlierThan(pub replyPublisher, subject string, body []byte, receipt time.Time) {
	if f == nil {
		return
	}
	if f.quantum <= 0 {
		f.publish(pub, subject, body)
		return
	}
	deadline := releaseAt(receipt, time.Now(), f.quantum)
	if f.metrics != nil {
		f.metrics.ClaimFloorApplied.Add(1)
		if deadline.Sub(receipt) > f.quantum {
			f.metrics.ClaimFloorLate.Add(1)
		}
	}
	if n := f.pending.Add(1); n > maxPendingDeferredReplies {
		f.pending.Add(-1)
		// Dropping is the fail-safe direction, and answering early is not an
		// option: an early answer restores the timing signal at exactly the
		// moment an attacker is generating the load that saturated the bound,
		// which is when they are measuring. The caller times out instead.
		if f.metrics != nil {
			f.metrics.ClaimFloorDropped.Add(1)
		}
		if f.drops.Add(1)%claimFloorDropLogEvery == 1 {
			f.logger.Warn("claim-rejection floor: pending deferred replies at bound; dropping reply",
				"subject", subject, "bound", maxPendingDeferredReplies,
				"droppedTotal", f.drops.Load(), "logEvery", claimFloorDropLogEvery)
		}
		return
	}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
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

// Drain waits for every already-accepted deferred reply to be published, up to
// budget. It reports whether the drain completed; false means the budget expired
// with replies still in flight, and those replies are lost when the process
// exits.
//
// Without it a SIGTERM or a rolling deploy discards up to
// maxPendingDeferredReplies marshalled replies whose callers are still waiting.
// The wait is bounded rather than unconditional because a deferred reply is not
// durable state: a shutdown that hangs on it would trade a real availability
// property for an observability one.
func (f *claimReplyFloor) Drain(budget time.Duration) bool {
	if f == nil || f.quantum <= 0 {
		return true
	}
	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		f.logger.Warn("claim-rejection floor: drain budget expired with replies still deferred",
			"budget", budget, "pending", f.pending.Load())
		return false
	}
}

// publish sends the reply body, logging a failure rather than surfacing it.
func (f *claimReplyFloor) publish(pub replyPublisher, subject string, body []byte) {
	if err := pub.Publish(subject, body); err != nil {
		f.logger.Warn("reply publish failed", "subject", subject, "error", err)
	}
}
