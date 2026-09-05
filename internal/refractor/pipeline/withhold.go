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
// unwritten because the target already held the identical body, and
// WithholdReadFailures how many batched read-backs failed so that a whole
// actor's entries were written unconditionally. Both are cumulative for the
// life of the process; the lag poller publishes them onto the lens's health
// entry.
func (p *Pipeline) EntriesWithheld() uint64 {
	return p.entriesWithheld.Load()
}

// WithholdReadFailures reports the batched read-backs that failed. It is a
// RATE an operator reads against EntriesWithheld, never a latch: a failure
// costs the entries of one actor for one event and nothing afterwards, so the
// mechanism is never disabled by one.
func (p *Pipeline) WithholdReadFailures() uint64 {
	return p.withholdReadFailures.Load()
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
func (p *Pipeline) withholdingArmed(adpt adapter.Adapter) bool {
	if p.multiEnvelopeFn == nil {
		return false
	}
	if _, ok := adpt.(adapter.RowsReader); !ok {
		return false
	}
	guard, guarded := adpt.(adapter.SeqGuarded)
	if !guarded || !guard.Guarded() {
		return false
	}
	if p.secureDecryptor != nil {
		p.logSecureWithholdRefusal()
		return false
	}
	return true
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
	if !p.withholdingArmed(adpt) {
		return
	}
	reader, ok := adpt.(adapter.RowsReader)
	if !ok {
		return
	}
	stored, err := reader.GetRows(ctx, keys)
	if err != nil {
		p.withholdReadFailures.Add(1)
		slog.Warn("pipeline: unchanged-entry read-back failed — every entry of this actor is written",
			"ruleId", p.ruleID, "actorKey", actorKey, "keys", len(keys), "err", err)
		return
	}
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
			fresh[i].Unchanged = true
		}
	}
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
