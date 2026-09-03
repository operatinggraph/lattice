package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// deleteTimeout bounds the automatic tombstone-triggered removal below —
// stop the consumer, delete its durable, delete the health entry. This runs
// synchronously on CoreKVSource's single dispatch goroutine (the same
// goroutine startPipeline's activation work already runs on), so a hang here
// would stall every other lens's spec processing, not just this one's
// removal — generous relative to the NATS round trips involved, but bounded.
//
// It is the default remover.timeout resolves to, not a value read directly:
// exercising the teardown's own expiry is the only way to reach its failure
// path with no supervisor behind the pipeline, and waiting out thirty real
// seconds to do that is a fixed sleep standing in for synchronisation.
const deleteTimeout = 30 * time.Second

// pipelineDeleter implements control.Deleter for one lens: stops its
// supervised consumer and deletes the server-side durable, then cancels the
// pipeline's run context and waits for Run to return, then deletes the
// lens's health KV entry. Shared by the operator "delete" control RPC
// (control.Service.deleteRule, via RegisterDeleter below) and the automatic
// tombstone-triggered removal (remover.remove) — one removal mechanism, two
// triggers, per refractor.md's Lens lifecycle step 9.
//
// Every step is independently idempotent (RemoveConsumer, cancel, and
// reporter.Delete are all safe to repeat), so the two triggers racing
// against the same lens — an operator's delete RPC alongside a concurrent
// tombstone write — cannot corrupt anything; at worst one repeats work the
// other already did.
type pipelineDeleter struct {
	ruleID string
	entry  *pipelineEntry
	// clearRefused evicts ruleID from the taxonomy refused-lens registry
	// (reloader.clearRefusedForTaxonomy) unconditionally, regardless of
	// whether it happens to be queued there right now — deleting a lens ID
	// must guarantee it stops being resurrectable by a later
	// retryRefused sweep, whichever of the two Deleter triggers reached it.
	// May be nil (tests that do not exercise the taxonomy seam).
	clearRefused func(ruleID string)
	// dropGrantConsumer evicts ruleID from the grant-change reprojector's
	// personal-lens registry (grantchange.Reprojector.DeregisterPersonal),
	// unconditionally and idempotently, for the same reason clearRefused is
	// unconditional: a lens that no longer runs must stop being re-driven.
	// Without it the drain would keep calling ReprojectPersonalActor on a
	// pipeline whose run context is cancelled, failing on every dirty actor and
	// raising a Health fault against the entry this Deleter just removed. May
	// be nil.
	dropGrantConsumer func(ruleID string)
}

// Delete implements control.Deleter. See Pipeline.RemoveConsumer's doc for
// why the durable must be removed BEFORE the run context is cancelled.
func (d pipelineDeleter) Delete(ctx context.Context) error {
	if d.clearRefused != nil {
		d.clearRefused(d.ruleID)
	}
	if d.dropGrantConsumer != nil {
		d.dropGrantConsumer(d.ruleID)
	}

	err := retryTransientBoot(ctx, func() error {
		return d.entry.pipeline.RemoveConsumer(ctx)
	})
	if err != nil {
		return fmt.Errorf("remove durable: %w", err)
	}

	d.entry.cancel()
	select {
	case <-d.entry.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	// The process-level census of cap-read producers installed with no
	// grant-change sink, evicted only once the pipeline has actually STOPPED.
	//
	// It sits here rather than beside the two registries above, and the two
	// orderings are both right for their own reason. Those registries evict
	// EARLY because their failure mode is a mechanism continuing to drive a lens
	// that is going away — the drain calling ReprojectPersonalActor on a
	// cancelled pipeline, a taxonomy sweep resurrecting a deleted id — so the
	// sooner they stop naming it the better, and a teardown that then fails
	// leaves a lens nobody re-drives, which is safe. This census is the mirror
	// image: an entry standing here REFUSES the personal derivation licence, so
	// evicting it early and then failing to remove the consumer would relicense
	// a narrowing while a sink-less producer is still running. Evicting a
	// producer that is still writing grants is the fail-OPEN direction, so it
	// waits for the run context to be cancelled and Run to return.
	projection.ForgetReadGrantProducer(d.ruleID)

	if d.entry.reporter != nil {
		err := retryTransientBoot(ctx, func() error {
			return d.entry.reporter.Delete(ctx)
		})
		if err != nil {
			return fmt.Errorf("delete health entry: %w", err)
		}
	}
	return nil
}

// remover takes a lens out of the running pipeline registry: it stops the
// pipeline and removes its durable consumer through the exact same
// pipelineDeleter the operator "delete" control RPC uses. Its standing trigger
// is a lens tombstone (parent vertex deleted, or its .spec aspect deleted), so a
// lens whose Core KV definition disappears cannot strand a durable JetStream
// consumer just because no operator happened to call delete first.
//
// lens.CoreKVSource itself calls the removal callback only from its two
// IsDeleted branches, never from dispatchSpec's update path (which drives
// updateCB / reloader.update instead), so a tombstone and a spec edit cannot be
// confused structurally. The update path reaches remove on exactly one arm, and
// deliberately: an Output-descriptor change, where reloader.reactivate composes
// this removal with a fresh activation because the envelope, delete key and
// sweep plan the descriptor shapes are installed only by activation.
type remover struct {
	logger *slog.Logger
	// take looks up the registry entry for lensID and removes it in the same
	// locked step, so a concurrent duplicate tombstone (redelivery, or a
	// vertex-then-spec double tombstone for the same lens) finds nothing the
	// second time and is a no-op.
	take func(lensID string) (*pipelineEntry, bool)
	// unregister clears the lens's control-plane registrations
	// (control.Service.Unregister) — the same cleanup deleteRule performs
	// after a successful operator-triggered delete.
	unregister func(lensID string)
	// clearRefused evicts a tombstoned lens's ID from the taxonomy
	// refused-lens registry (reloader.clearRefusedForTaxonomy), called
	// UNCONDITIONALLY — not gated on take finding a registry entry. A lens
	// refused for an unknown taxonomy expansion never reaches the pipeline
	// registry take looks up, but CoreKVSource still calls remove for its
	// tombstone (dispatchSpec sets s.known regardless of whether the load
	// callback's activation attempt succeeded), so take's !ok branch is
	// EXACTLY the path a refused lens's tombstone takes — gating the
	// eviction on take's result would leave a deleted lens's stale rule
	// queued forever, for retryRefused to resurrect the instant the next
	// taxonomy event lands: it builds an adapter, registers a durable, and
	// projects rows for a lens definition Core KV no longer has (on a grant
	// producer, a resurrected grant writer with no definition behind it).
	// May be nil (tests that do not exercise the taxonomy seam).
	clearRefused func(ruleID string)
	// dropGrantConsumer evicts a tombstoned lens's ID from the grant-change
	// reprojector's personal-lens registry, and is called UNCONDITIONALLY for
	// the same reason clearRefused is: a lens whose activation failed before
	// the registry insert still takes take's !ok branch here, and a personal
	// lens that registered as a reprojection consumer and then failed a later
	// activation step would otherwise stay on that list forever. May be nil.
	dropGrantConsumer func(ruleID string)
	// timeout bounds one teardown. Usable at its zero value — which resolves to
	// deleteTimeout, so production wiring and every fixture that builds a bare
	// &remover{} get the real bound with nothing to set. A test shortens it to
	// reach the expiry branch deterministically, since that branch is the only
	// teardown failure a pipeline with no supervisor can produce.
	timeout time.Duration
}

// teardownTimeout is the bound one removal runs under: the configured value, or
// the package default when none was set.
func (rm *remover) teardownTimeout() time.Duration {
	if rm.timeout > 0 {
		return rm.timeout
	}
	return deleteTimeout
}

// errLensNotRunning reports that there was nothing to stop: no registry entry
// for this lens ID. It is not a failure — a lens whose activation never reached
// the registry, or one a concurrent operator `delete` already removed, is
// exactly as stopped as one this call tore down — but it is not a teardown
// either, and a caller about to start a replacement has to tell the two apart.
var errLensNotRunning = errors.New("lens is not in the pipeline registry")

// remove is lens.RemoveCallback. old is CoreKVSource's last-loaded snapshot
// of the tombstoned rule; CoreKVSource never passes nil (the callback only
// fires for a rule that previously reached the load callback).
//
// A tombstone has nothing left to decide on the outcome: the lens definition is
// gone either way, and stop has already recorded a failed teardown on the lens's
// own health entry. So this logs and returns, while the re-activation trigger
// calls stop directly — it has something that must not happen if the stop
// failed.
func (rm *remover) remove(old *lens.Rule) {
	if err := rm.stop(old, "tombstone"); err != nil && !errors.Is(err, errLensNotRunning) {
		rm.logger.Error("remove tombstoned lens", "lensId", lensIDOf(old), "err", err)
	}
}

// stop is the removal itself: it takes the lens out of the registry, tears the
// pipeline down through pipelineDeleter, and reports whether the lens actually
// STOPPED. trigger names why, for the log line.
//
// The error matters to a caller that intends to start something in this lens's
// place. pipelineDeleter.Delete returns its "remove durable" failure BEFORE it
// cancels the run context, so a failure there leaves the old pump alive — and
// activating a replacement on the strength of a teardown that did not happen
// puts two pipelines, two durables and two writers on one lens.
//
// The control-plane registration and the "removed" log both wait for a
// successful Delete, for the same reason: a lens whose pump is still running
// must stay addressable by the operator "delete" RPC, which drives the identical
// idempotent pipelineDeleter and is the retry.
func (rm *remover) stop(old *lens.Rule, trigger string) error {
	if old == nil {
		return errors.New("no rule to remove")
	}
	if rm.clearRefused != nil {
		rm.clearRefused(old.ID)
	}
	if rm.dropGrantConsumer != nil {
		rm.dropGrantConsumer(old.ID)
	}
	entry, ok := rm.take(old.ID)
	if !ok {
		// Never started (activation failed before the registry insert — the
		// clearRefused call above is what actually matters for THAT case)
		// or already removed by a concurrent delete. Nothing to tear down, and
		// nothing to put in its place either: whoever removed it decided this
		// lens ID is going away.
		return errLensNotRunning
	}
	delCtx, cancel := context.WithTimeout(context.Background(), rm.teardownTimeout())
	defer cancel()
	if err := (pipelineDeleter{ruleID: old.ID, entry: entry, clearRefused: rm.clearRefused, dropGrantConsumer: rm.dropGrantConsumer}).Delete(delCtx); err != nil {
		// The health entry survives a failed teardown: Delete removes the durable
		// FIRST and only reaches reporter.Delete once the pump has stopped, so a
		// failure here leaves an entry that still describes a lens — one whose
		// pump may well still be running. Recording the diagnosis on it is the
		// only account an operator gets; the alternative is a lens that has
		// silently left the registry while still writing.
		if entry.reporter != nil {
			// A FRESH context, deliberately: the commonest teardown failure is
			// delCtx expiring while Delete waits for Run to return, and recording
			// the diagnosis on the very context whose expiry IS the diagnosis
			// would drop it silently.
			recCtx, recCancel := context.WithTimeout(context.Background(), rm.teardownTimeout())
			msg := fmt.Sprintf("lens teardown failed — %v; the pump may still be running; retry with the operator delete op", err)
			if recErr := entry.reporter.RecordError(recCtx, msg); recErr != nil {
				rm.logger.Error("record lens teardown failure in health", "lensId", old.ID, "err", recErr)
			}
			recCancel()
		}
		return err
	}
	rm.unregister(old.ID)
	rm.logger.Info("lens pipeline removed", "lensId", old.ID, "canonicalName", old.CanonicalName, "trigger", trigger)
	return nil
}

// lensIDOf names a rule for a log line, tolerating the nil stop refuses.
func lensIDOf(r *lens.Rule) string {
	if r == nil {
		return ""
	}
	return r.ID
}
