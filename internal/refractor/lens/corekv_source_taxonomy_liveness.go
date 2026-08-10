package lens

import (
	"context"
	"time"
)

// The taxonomy-liveness barrier: what makes CoreKVSource's "my snapshots are
// current" claim (SetTaxonomyLivenessCallback, consumed by
// taxonomy.Resolver.SetArmed) something the source can actually back.
//
// The claim is expensive to get wrong in one direction only. A resolver
// answering taxonomy.StatusArmed lets a `*` lens publish a NARROWED consumer
// filter plus a client gate that acks-and-drops everything outside it
// (dynamic-type-taxonomy-design.md §4.2), so an armed answer over an
// incomplete downward closure silently takes the lens dark for the subtypes
// it has not read yet — §6.5's one unacceptable state. An unarmed answer
// costs a broad filter on a lens that keeps running. Every ambiguity below
// therefore resolves away from armed.
//
// Two windows make "an event arrived" an unsound proxy for currency:
//
//  1. BOOT REPLAY. The source subscribes on a fresh per-boot durable with
//     IncludeHistory (lensSourceDurablePrefix), so its first events are the
//     taxonomy replaying from the start of the stream. Every snapshot emitted
//     mid-replay describes a PREFIX of the graph — a leaf whose meta has not
//     been replayed yet is indistinguishable from one that does not exist.
//  2. CONNECTION LOSS. A durable resumes from its ack floor, so nothing is
//     lost — but for the length of the outage the source sees no taxonomy
//     edits at all, and the reconnect itself does not mean the backlog has
//     been re-delivered.
//
// The test that closes both is the same one, and it already exists:
// substrate.Conn.ConsumerCaughtUp — nothing pending delivery AND nothing
// delivered-but-unacked on this boot's durable. The second half is what makes
// it usable here: substrate acks a KV message only after the dispatch
// goroutine has RECEIVED it, and the channel it receives on is unbuffered, so
// an event still in flight anywhere between the server and consume's select
// leaves NumPending or NumAckPending non-zero. A verdict of "caught up" is
// then delivered to consume's own select (armCh), which cannot be reached
// while handle() is running — so by the time the verdict is applied, every
// event it covered has been folded into a snapshot and installed.
//
// Why a poll. An in-band drain signal does exist on this path — JetStream's
// per-message num-pending, which substrate.Message.NumPending already surfaces
// for the sibling primitive and substrate.KVEvent simply does not carry — and
// a barrier built on it would need no probe at all for the BOOT case. It is
// not used here because it does not cover the other case: a reconnect on an
// idle stream delivers no message to carry the signal, so the source would sit
// unarmed until the next unrelated taxonomy edit, however long that takes. One
// mechanism that answers both questions is worth a round trip that only ever
// happens while the answer is "not yet".
//
// The poll's cost is bounded by the latch: once the verdict lands the watcher
// stops probing entirely, which is also what keeps a busy stream from flapping
// the flag — and each flap would drive a re-derivation sweep across every live
// `*` lens.

// taxonomyArmPollInterval is how often watchTaxonomyLiveness asks whether
// this boot's durable has drained, while the source is not armed. It bounds
// how long after the boot replay (or a reconnect) a `*` lens keeps running on
// the broad filter it correctly took in the meantime — a latency cost, never
// a correctness one, which is why it is sized for a cheap steady state rather
// than for speed.
//
// A var, not a const, so this package's tests can shrink it — mirroring
// substrate.PruneStaleDurableAge's own reason. Production never reassigns it.
var taxonomyArmPollInterval = 250 * time.Millisecond

// taxonomyArmProbeTimeout bounds one caught-up probe. A probe issued while
// the connection is re-establishing would otherwise sit in nats.go's pending
// buffer, and a watcher wedged on it stops noticing everything else.
const taxonomyArmProbeTimeout = 5 * time.Second

// taxonomyEventAckWait is how long JetStream waits for this subscription's
// ack before redelivering. Sized against the slowest thing handle() does per
// event — a lens activation, which compiles a rule, provisions a read-model
// table and starts a pipeline — not against the common case, because the
// consequence of being wrong is a redelivery of an event whose handler is
// still running.
const taxonomyEventAckWait = 2 * time.Minute

// taxonomyProbeFailureWarnAfter is the consecutive-failure count at which a
// failing probe stops being a Debug detail and becomes a Warn. It is a run,
// not a single failure, because the first ticks of any boot legitimately fail
// (the consumer is still being created) — and it fires ONCE at the threshold
// rather than per tick, because a persistent fault would otherwise write the
// same line forever.
const taxonomyProbeFailureWarnAfter = 8

// watchTaxonomyLiveness drives the source from "not current" to "current",
// and does nothing at all once it gets there. It runs for the source's whole
// life because the transition is re-entered on every connection loss, and
// exits on ctx cancellation (shutdown) or once the subscription is
// permanently dead.
//
// Each pass is deliberately cheap in the common case: a mutex read while
// armed, plus a local connection-status read (no round trip) before spending
// a probe. The verdict carries the epoch it was computed under, so a probe
// that straddles a disconnect is discarded by armTaxonomy rather than
// arming across a window it never saw.
func (s *CoreKVSource) watchTaxonomyLiveness(ctx context.Context, durable string) {
	stream := "KV_" + s.bucket
	ticker := time.NewTicker(taxonomyArmPollInterval)
	defer ticker.Stop()
	consecutiveFailures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		s.taxLiveMu.Lock()
		live, dead, epoch := s.taxonomyLive, s.taxonomyDead, s.taxonomyEpoch
		s.taxLiveMu.Unlock()
		if dead {
			return
		}
		if live {
			consecutiveFailures = 0
			continue
		}
		if !s.conn.Connected() {
			continue
		}

		// The one configuration-dependent assumption this barrier rests on,
		// recorded where it is spent. ConsumerCaughtUp is sound as a "has
		// everything been handled" test only because a message is always in
		// exactly one of the two counters it reads: nats-server decrements
		// the pending count and records the message as awaiting-ack under a
		// single hold of the consumer lock, so no probe can observe a message
		// that has left one counter and not yet entered the other.
		//
		// ReplayOriginal and RateLimit each release that lock between the two
		// steps, so enabling either on THIS consumer would reopen exactly that
		// window and make a "caught up" answer mean less than it says.
		// SubscribeKVChanges sets neither.
		//
		// The other way a message leaves both counters is an exhausted
		// delivery budget — the server drops it from the pending set without
		// it ever having been handled. That is why this subscription runs
		// with unlimited MaxDeliver plus a prefetch of one and a generous
		// AckWait (Start): a taxonomy event Termed by a delivery budget is
		// state loss with no other write path, and it would leave the probe
		// reporting a currency the snapshot does not have.
		probeCtx, cancel := context.WithTimeout(ctx, taxonomyArmProbeTimeout)
		caughtUp, err := s.conn.ConsumerCaughtUp(probeCtx, stream, durable)
		cancel()
		if err != nil {
			// Includes the pre-subscription window (the consumer does not
			// exist yet) and every transient info-fetch failure. Not being
			// able to establish that the feed is current is exactly the
			// state that must not arm, so an error is simply "not yet".
			//
			// "Not yet" forever is invisible on its own, though: the resolver
			// stays StatusStale, every `*` lens stays broad, and nothing in
			// the corpus says why. So a run of failures is escalated once —
			// once, not per tick, or a persistent fault becomes its own log
			// flood.
			consecutiveFailures++
			if consecutiveFailures == taxonomyProbeFailureWarnAfter {
				s.logger.Warn("taxonomy liveness: caught-up probe has failed repeatedly — the taxonomy resolver stays unarmed and every `*` lens stays on the broad filter until it succeeds",
					"durable", durable, "consecutiveFailures", consecutiveFailures, "err", err)
			} else {
				s.logger.Debug("taxonomy liveness: caught-up probe failed, staying unarmed",
					"durable", durable, "consecutiveFailures", consecutiveFailures, "err", err)
			}
			s.recordTaxonomyProbeFailures(consecutiveFailures)
			continue
		}
		consecutiveFailures = 0
		s.recordTaxonomyProbeFailures(0)
		if !caughtUp {
			continue
		}
		s.recordTaxonomyDrainedVerdict()
		select {
		case s.armCh <- epoch:
		case <-s.taxonomyDeadCh:
			// consume has exited through the dead path and will never
			// receive again. Without this arm the send parks forever and
			// this goroutine leaks for the process's life.
			return
		case <-ctx.Done():
			return
		}
	}
}

// armTaxonomy applies a caught-up verdict computed under epoch. Called only
// from consume's dispatch goroutine, which is what orders it behind the last
// replayed event's snapshot (see the case in consume) — and what lets it run
// the Changed sweep inline, on the one goroutine allowed to.
//
// Three ways a verdict is stale rather than wrong, all of them refusals: the
// subscription died while it was in flight; the flag is already true (nothing
// to report, and a re-report would cost a full re-derivation sweep); or the
// connection dropped after the probe read the consumer's counters, in which
// case the epoch has moved and this verdict describes a feed that no longer
// exists.
func (s *CoreKVSource) armTaxonomy(epoch uint64) {
	s.taxLiveMu.Lock()
	if s.taxonomyDead || s.taxonomyLive || epoch != s.taxonomyEpoch {
		s.taxLiveMu.Unlock()
		return
	}
	s.taxonomyLive = true
	s.taxonomySweepDue = false
	s.taxonomyUnarmedSince = time.Time{}
	if s.taxonomyLiveness.Armed != nil {
		s.taxonomyLiveness.Armed(true)
	}
	s.taxLiveMu.Unlock()

	s.logger.Info("taxonomy liveness: consumer drained — taxonomy snapshots are current",
		"epoch", epoch)
	if s.taxonomyLiveness.Changed != nil {
		s.taxonomyLiveness.Changed()
	}
}

// connectionLost is substrate's connection-state fan-out arriving on the
// false edge: the NATS connection under this source is gone, so no taxonomy
// edit reaches it until it comes back.
func (s *CoreKVSource) connectionLost() {
	s.taxonomyFeedInterrupted("NATS connection lost")
}

// subscriptionReopened is substrate's reopen hook: the messages iterator
// stalled and is being re-opened against the same durable.
//
// It disarms for the same reason a connection loss does, and the reason is
// worth stating because the reopen looks harmless from the outside — the
// channel never closed, and nothing was LOST (the durable resumes from its ack
// floor, so every undelivered event comes back). What was lost is CURRENCY:
// for the length of the stall this source saw nothing, and a resolver still
// answering StatusArmed through that window is telling a `*` lens its narrow
// filter is safe against a taxonomy nobody was reading. "Nothing was lost" and
// "I am up to date" are different claims, and only the second one is what
// armed means.
//
// Re-arming then goes through the ordinary drain probe, exactly like boot and
// exactly like a reconnect: nothing about recovery is special-cased here.
func (s *CoreKVSource) subscriptionReopened() {
	s.taxonomyFeedInterrupted("subscription iterator stalled and is re-opening")
}

// taxonomyFeedInterrupted is the shared body for every way this source can go
// blind without dying. It bumps the epoch unconditionally — even when the
// source was not armed, because a drain verdict may be in flight right now and
// it was computed against a feed that has since had a gap — flips the
// fail-closed flag immediately, and leaves the SWEEP owed to the dispatch
// goroutine.
//
// The split is the whole point. Callers arrive here on goroutines that must
// not be held: nats.go's single serial async-callback goroutine, and
// substrate's subscription goroutine (which cannot re-open, and so cannot
// deliver, until this returns). The sweep is not merely slow — it re-derives
// the live lens corpus, which the dispatch goroutine is concurrently doing
// from the snapshot path, and two goroutines running that concurrently
// interleave a publish with another's baseline commit and race the activation
// path's check-then-register. So the flag — the part that must not wait, and
// that cannot race anything, being one guarded write — happens here, and the
// sweep is handed over.
func (s *CoreKVSource) taxonomyFeedInterrupted(reason string) {
	s.taxLiveMu.Lock()
	s.taxonomyEpoch++
	epoch := s.taxonomyEpoch
	if !s.taxonomyLive {
		s.taxLiveMu.Unlock()
		return
	}
	s.taxonomyLive = false
	s.taxonomySweepDue = true
	s.taxonomyUnarmedSince = time.Now()
	if s.taxonomyLiveness.Armed != nil {
		s.taxonomyLiveness.Armed(false)
	}
	s.taxLiveMu.Unlock()

	s.logger.Warn("taxonomy liveness: taxonomy snapshots are no longer current",
		"reason", reason, "epoch", epoch)
	s.wakeTaxonomySweep()
}

// wakeTaxonomySweep asks the dispatch goroutine to run a sweep it is owed.
// The send is non-blocking over a depth-1 channel: the caller may be nats.go's
// serial callback goroutine, which must never park, and a wake already queued
// covers this transition too — taxonomySweepDue is the debt, the channel is
// only the doorbell.
func (s *CoreKVSource) wakeTaxonomySweep() {
	select {
	case s.sweepCh <- struct{}{}:
	default:
	}
}

// runDueTaxonomySweep runs the Changed callback if one is owed. Called only
// from consume's dispatch goroutine, which is the confinement the sweep
// depends on; the debt flag is cleared before the sweep runs, so a transition
// landing DURING it rings the doorbell again and gets its own sweep rather
// than being swallowed.
func (s *CoreKVSource) runDueTaxonomySweep() {
	s.taxLiveMu.Lock()
	due := s.taxonomySweepDue
	s.taxonomySweepDue = false
	s.taxLiveMu.Unlock()
	if !due {
		return
	}
	if s.taxonomyLiveness.Changed != nil {
		s.taxonomyLiveness.Changed()
	}
}

// taxonomyConsumerDead latches the source out of ever arming again, for the
// unrecoverable subscription failure consume reports through
// taxonomyDeadCB. The liveness pair is deliberately NOT also fired: the dead
// callback is the single report for this transition, and a caller wiring both
// to the same resolver would otherwise disarm twice and pay two re-derivation
// sweeps for one event.
//
// The latch is the load-bearing part. A dead subscription leaves the durable
// sitting in the JetStream catalog with nothing consuming it, so it reports
// nothing pending and nothing unacked — perfectly "caught up" — forever. A
// watcher without this latch would read that as proof of currency on a source
// that has stopped reading the taxonomy altogether.
//
// Closing taxonomyDeadCh is what unparks a watcher already blocked handing
// over a verdict: consume has left its select for good by the time this runs,
// so the latch alone (which the watcher only re-reads at the top of its loop)
// could not rescue it.
func (s *CoreKVSource) taxonomyConsumerDead() {
	s.taxLiveMu.Lock()
	alreadyDead := s.taxonomyDead
	if s.taxonomyLive {
		s.taxonomyUnarmedSince = time.Now()
	}
	s.taxonomyDead = true
	s.taxonomyLive = false
	s.taxonomySweepDue = false
	s.taxonomyEpoch++
	s.taxLiveMu.Unlock()
	if !alreadyDead && s.taxonomyDeadCh != nil {
		close(s.taxonomyDeadCh)
	}
}

// recordTaxonomyProbeFailures publishes the current consecutive-probe-failure
// run for the health entry, so a resolver that never arms is visible as its
// own fact rather than only as whichever broad-filter reason a lens happens to
// rank first.
func (s *CoreKVSource) recordTaxonomyProbeFailures(n int) {
	s.taxLiveMu.Lock()
	s.taxonomyProbeFailures = n
	s.taxLiveMu.Unlock()
}

// recordTaxonomyDrainedVerdict counts one caught-up verdict produced by the
// watcher, before it is handed to the dispatch goroutine. Counting the verdict
// separately from the arming it usually causes is what separates two states
// that otherwise look identical from outside — "the probe has never once found
// the feed drained" and "it finds it drained repeatedly and something keeps
// discarding the verdict" (a connection flapping faster than the dispatch
// goroutine can accept one). The first is a stuck feed, the second is a stuck
// PROCESS, and they are fixed by different things.
func (s *CoreKVSource) recordTaxonomyDrainedVerdict() {
	s.taxLiveMu.Lock()
	s.taxonomyDrainedVerdicts++
	s.taxLiveMu.Unlock()
}

// TaxonomyLivenessStatus is the observable state of this source's taxonomy
// currency claim, for a health emitter.
type TaxonomyLivenessStatus struct {
	// Armed is the claim itself: snapshots emitted by this source are
	// currently backed by a live, drained consumer.
	Armed bool
	// Dead reports that the subscription failed terminally, so Armed can
	// never become true again in this process.
	Dead bool
	// UnarmedSince is when the source last stopped being (or has never yet
	// become) armed. Zero while armed.
	UnarmedSince time.Time
	// ProbeFailures is the current run of consecutive caught-up probe
	// failures — the ordinary reason a source stays unarmed for a long time
	// with nothing else to show for it.
	ProbeFailures int
	// DrainedVerdicts counts how many times the watcher has found the feed
	// drained, whether or not the verdict went on to arm anything. Unarmed
	// with zero here is a feed that has never caught up; unarmed with a
	// growing count is a process discarding its own verdicts.
	DrainedVerdicts int
}

// TaxonomyLivenessStatus reports the source's current currency claim. Safe
// from any goroutine; takes only the liveness lock, so it can be called from
// a health-heartbeat cadence without interacting with the dispatch path.
func (s *CoreKVSource) TaxonomyLivenessStatus() TaxonomyLivenessStatus {
	s.taxLiveMu.Lock()
	defer s.taxLiveMu.Unlock()
	st := TaxonomyLivenessStatus{
		Armed:           s.taxonomyLive,
		Dead:            s.taxonomyDead,
		ProbeFailures:   s.taxonomyProbeFailures,
		DrainedVerdicts: s.taxonomyDrainedVerdicts,
	}
	if !s.taxonomyLive {
		st.UnarmedSince = s.taxonomyUnarmedSince
	}
	return st
}
