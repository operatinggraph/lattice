package pipeline

import (
	"context"
	"log/slog"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// SetAdjacencyAppliedFn installs the adjacency index's progress cursor
// (consumer.Bootstrapper.AppliedSeq). The host calls it once per pipeline at
// construction, handing every lens the one process-wide bootstrapper's
// accessor.
//
// Leaving it unset is safe and refusing: the reader below reads a nil source
// as a cursor of 0, and every consumer of a 0 cursor declines to act.
func (p *Pipeline) SetAdjacencyAppliedFn(fn func() uint64) {
	p.adjacencyApplied = fn
}

// adjacencyAppliedSeq reads the adjacency index's progress cursor, or 0 when
// no source is installed. Zero is the never-measured answer as well as the
// empty-stream one, so a caller acts only on a value strictly above it.
func (p *Pipeline) adjacencyAppliedSeq() uint64 {
	if p.adjacencyApplied == nil {
		return 0
	}
	return p.adjacencyApplied()
}

// EntriesWithheld reports how many perEntry entries this lens has left
// unwritten because the target already held the identical body. Cumulative for
// the life of the process.
func (p *Pipeline) EntriesWithheld() uint64 {
	return p.entriesWithheld.Load()
}

// WithholdReadFailures reports the batched read-backs that failed, each costing
// one actor's entries an unconditional rewrite for one event. It is a RATE an
// operator reads against EntriesWithheld, never a latch: a failure costs that
// one evaluation and nothing afterwards, so the mechanism is never disabled by
// one. Cumulative for the life of the process.
func (p *Pipeline) WithholdReadFailures() uint64 {
	return p.withholdReadFailures.Load()
}

// WithholdCounts is the health poller's source for both tallies, and it answers
// whether this lens is CAPABLE of withholding at all — health.WithholdCountsFunc.
//
// A lens with no multiEnvelopeFn is not a perEntry producer: it has no
// per-entry set to compare, so it will never withhold and has measured nothing.
// Reporting a zero for it would put "installed, saved nothing" on its health
// entry, which is a claim; absent is the truth (Contract #5 §5.4). The
// capability is read off the lens's own shape rather than off the adapter,
// because the shape is what makes the question applicable — an armed-or-not
// adapter changes the ANSWER, not whether there is one.
func (p *Pipeline) WithholdCounts() (uint64, uint64, bool) {
	if p.multiEnvelopeFn == nil {
		return 0, 0, false
	}
	return p.entriesWithheld.Load(), p.withholdReadFailures.Load(), true
}

// withholdingArmed reports whether this pipeline may leave an unchanged
// perEntry entry unwritten, for the adapter the caller has already captured.
//
// Three conjuncts, read per call and never cached, because each can change
// under a running pipeline (a hot reload swaps the adapter; the installer sets
// the decryptor after construction):
//
//  1. multiEnvelopeFn — the perEntry family, and only it. A doc-mode
//     actor-aggregate lens writes one key per actor (nothing to narrow) and
//     retries a transient write through the raw replay, which replays a
//     captured row against a target this predicate reasoned about at
//     evaluation time.
//  2. the adapter reads rows back AND enforces the §6.2 ordering guard. A
//     target that cannot be read back has no stored body to compare; an
//     unguarded one already skips a byte-identical row on its own, and the
//     ordering argument that makes a withheld write safe (the stored
//     watermark carries the entry's last presence change) is the guard's.
//  3. no Secure decryptor. The comparison runs on the evaluation's own rows,
//     before decrypt-at-projection transforms them, so a Secure perEntry lens
//     would compare ciphertext against a stored plaintext body and never find
//     them equal. Refusing outright says so instead of withholding nothing
//     while claiming to be armed.
func (p *Pipeline) withholdingArmed(adpt adapter.Adapter) (adapter.RowsReader, bool) {
	if p.multiEnvelopeFn == nil {
		return nil, false
	}
	reader, readable := adpt.(adapter.RowsReader)
	if !readable {
		return nil, false
	}
	guard, guarded := adpt.(adapter.SeqGuarded)
	if !guarded || !guard.Guarded() {
		return nil, false
	}
	if p.secureDecryptor != nil {
		p.logSecureWithholdRefusal()
		return nil, false
	}
	// The reader comes back with the verdict so the caller cannot re-derive it
	// from a DIFFERENT adapter than the one that was armed.
	return reader, true
}

// markUnchangedEntries reads back the stored bodies of keys — the entries this
// evaluation is about to rewrite that the target already holds — and marks
// every fresh result whose stored body already equals it, so the write loop
// skips it.
//
// The predicate is rowsEquivalent, the same one the convergence sweep uses to
// call a row converged: canonical JSON modulo the fields restamped on every
// evaluation, against a stored side the adapter has already stripped of the
// guard's projectionSeq. A key the read did not resolve — absent, tombstoned,
// unparseable — is left alone and therefore written, which is what "different"
// means for an entry the target does not hold. So is a body neither side can
// render.
//
// FAILURE IS A RATE, NOT A LATCH. A batch that errors marks nothing: this
// actor's entries are all written, exactly as they are with withholding
// disarmed, one Warn is logged for the actor and a counter moves. Nothing is
// remembered, so the next event withholds normally — a read fault must not
// make itself permanent.
func (p *Pipeline) markUnchangedEntries(ctx context.Context, adpt adapter.Adapter, actorKey string, keys []string, fresh []ruleengine.EvalResult) {
	reader, armed := p.withholdingArmed(adpt)
	if !armed {
		return
	}
	// Captured with the read, not after it: the verdict below is a statement
	// about the adapter this read ran against, and the write loop honours it
	// only while that is still the installed one.
	generation := p.adapterGeneration()
	stored, err := reader.GetRows(ctx, keys)
	if err != nil {
		p.withholdReadFailures.Add(1)
		p.warnWithholdReadFailure(actorKey, len(keys), err)
		return
	}
	p.withholdReadWarned.Store(false)
	for i := range fresh {
		// A retraction is always written, and a FailClosed result's whole
		// purpose is that its write cannot be passed over silently.
		if fresh[i].Delete || fresh[i].FailClosed {
			continue
		}
		k, hasKey := fresh[i].Keys["key"].(string)
		if !hasKey {
			continue
		}
		body, found := stored[k]
		if !found {
			continue
		}
		if rowsEquivalent(body, fresh[i].Row) {
			fresh[i].UnchangedAt = generation
		}
	}
}

// honoursUnchanged reports whether a result's withholding verdict still stands:
// it was reached (a live generation is never zero) AND the adapter it was
// reached against is still the installed one. Every consumer of the verdict
// asks through here, so a hot reload invalidates all of them at once.
func (p *Pipeline) honoursUnchanged(result ruleengine.EvalResult) bool {
	if result.Delete || result.FailClosed {
		// Belt beside the brace in markUnchangedEntries: neither shape is ever
		// marked, and neither may ever be skipped, so the two places that could
		// get it wrong both refuse rather than one trusting the other.
		return false
	}
	return result.UnchangedAt != 0 && result.UnchangedAt == p.adapterGeneration()
}

// warnWithholdReadFailure reports a failed read-back at most once per pipeline
// until one succeeds again. A fan-out event reaches thousands of actors and
// calls the read once per actor, so a target that has gone unreadable would
// otherwise emit thousands of identical lines per event; the latch keeps the
// first one, and WithholdReadFailures carries the rate. A success clears it, so
// the next outage is reported again rather than swallowed by the last.
func (p *Pipeline) warnWithholdReadFailure(actorKey string, keys int, err error) {
	if p.withholdReadWarned.Swap(true) {
		return
	}
	slog.Warn("pipeline: unchanged-entry read-back failed — every entry of this actor is written; further failures are counted, not logged, until one succeeds",
		"ruleId", p.ruleID, "actorKey", actorKey, "keys", keys, "err", err)
}

// logSecureWithholdRefusal reports conjunct 3's refusal once per pipeline. It
// is the only conjunct whose negation is invisible in the lens's declaration
// to a reader looking at write volume: the other two are read off the lens
// shape and the target, which an operator can see.
func (p *Pipeline) logSecureWithholdRefusal() {
	p.secureWithholdRefusalOnce.Do(func() {
		slog.Info("pipeline: unchanged-entry withholding refused — a Secure lens's entries are compared before decryption, so every entry is written",
			"ruleId", p.ruleID, "adapter", p.adapterName)
	})
}
