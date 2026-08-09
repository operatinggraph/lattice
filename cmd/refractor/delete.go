package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
)

// deleteTimeout bounds the automatic tombstone-triggered removal below —
// stop the consumer, delete its durable, delete the health entry. This runs
// synchronously on CoreKVSource's single dispatch goroutine (the same
// goroutine startPipeline's activation work already runs on), so a hang here
// would stall every other lens's spec processing, not just this one's
// removal — generous relative to the NATS round trips involved, but bounded.
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
}

// Delete implements control.Deleter. See Pipeline.RemoveConsumer's doc for
// why the durable must be removed BEFORE the run context is cancelled.
func (d pipelineDeleter) Delete(ctx context.Context) error {
	if d.clearRefused != nil {
		d.clearRefused(d.ruleID)
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

// remover applies a lens tombstone (parent vertex deleted, or its .spec
// aspect deleted) to the running pipeline registry: it stops the pipeline
// and removes its durable consumer through the exact same pipelineDeleter
// the operator "delete" control RPC uses, so a lens whose Core KV definition
// disappears cannot strand a durable JetStream consumer just because no
// operator happened to call delete first (every uninstalled lens did,
// before this — the leak this type closes).
//
// Never invoked for a spec UPDATE — lens.CoreKVSource only calls the removal
// callback from its two IsDeleted branches, never from dispatchSpec's update
// path (which drives updateCB / reloader.update instead), so the hot-reload
// and removal paths cannot be confused structurally.
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
}

// remove is lens.RemoveCallback. old is CoreKVSource's last-loaded snapshot
// of the tombstoned rule; CoreKVSource never passes nil (the callback only
// fires for a rule that previously reached the load callback).
func (rm *remover) remove(old *lens.Rule) {
	if old == nil {
		return
	}
	if rm.clearRefused != nil {
		rm.clearRefused(old.ID)
	}
	entry, ok := rm.take(old.ID)
	if !ok {
		// Never started (activation failed before the registry insert — the
		// clearRefused call above is what actually matters for THAT case)
		// or already removed by a concurrent delete — nothing else to tear
		// down.
		return
	}
	delCtx, cancel := context.WithTimeout(context.Background(), deleteTimeout)
	defer cancel()
	if err := (pipelineDeleter{ruleID: old.ID, entry: entry, clearRefused: rm.clearRefused}).Delete(delCtx); err != nil {
		rm.logger.Error("remove tombstoned lens", "lensId", old.ID, "err", err)
	}
	rm.unregister(old.ID)
	rm.logger.Info("lens pipeline removed (tombstone)", "lensId", old.ID, "canonicalName", old.CanonicalName)
}
