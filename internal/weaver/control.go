package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TargetSummary is the operator-facing snapshot of one registered target
// : targetId, lensRef, the sorted set of playbook gap columns,
// and the target's current control state.
//
// State is a 2-value enum: "active" or "disabled". A revoke does not produce
// a durable "revoked" state — Revoke also sets the `<targetId>.__control`
// disabled marker (a strict superset of Disable, per the documented
// revoke-vs-reconcile bound), so a revoked target reports "disabled" until an
// operator `enable`s it again, even across a `reconcileConsumers` re-Add.
type TargetSummary struct {
	TargetID string   `json:"targetId"`
	LensRef  string   `json:"lensRef"`
	Gaps     []string `json:"gaps"`
	State    string   `json:"state"`
}

// Target control states (TargetSummary.State).
const (
	targetStateActive   = "active"
	targetStateDisabled = "disabled"
)

// replayResetTimeout bounds ReplayTarget's delete-then-create of a lane-1
// durable. It is deliberately far wider than the two JetStream API round trips
// that pair costs, because a deadline expiring BETWEEN the delete and the create
// is the one outcome that leaves a target with no durable; and it is bounded at
// all so a detached call cannot wedge the control responder's goroutine.
const replayResetTimeout = 30 * time.Second

// seedDisabledTargets scans weaver-state for every `<targetId>.__control`
// dispatch-skip marker and populates the engine's in-memory disabled-set
// before the lane-1/lane-3 consumers start delivering. The
// `<targetId>.__control` marker is the durable truth (Disable/Revoke write
// it; it survives a restart with zero extra persistence — mirrors how the
// lane-1 PauseManual state survives via HealthSink restoreState); this seed
// is the in-memory cache rebuilt from that durable backing.
func (e *Engine) seedDisabledTargets(ctx context.Context) error {
	keys, err := e.conn.KVListKeys(ctx, e.cfg.WeaverStateBucket)
	if err != nil {
		return err
	}
	targetIDForKey := make(map[string]string, len(keys))
	controlKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		targetID, ok := strings.CutSuffix(key, controlKeySuffix)
		if !ok {
			continue
		}
		targetIDForKey[key] = targetID
		controlKeys = append(controlKeys, key)
	}
	entries, err := e.conn.KVGetMulti(ctx, e.cfg.WeaverStateBucket, controlKeys)
	if err != nil {
		return err
	}
	for _, key := range controlKeys {
		targetID := targetIDForKey[key]
		entry, present := entries[key]
		if !present {
			continue
		}
		var cm controlMark
		if uerr := json.Unmarshal(entry.Value, &cm); uerr != nil {
			e.logger.Error("weaver: seed disabled-target read failed", "targetId", targetID, "err", uerr)
			continue
		}
		if cm.Disabled {
			e.disabled.set(targetID, true)
		}
	}
	return nil
}

// ListTargets returns a snapshot of every currently-registered target
// : targetId, lensRef, the sorted gap columns, and the current
// control state from the in-memory disabled-set — "active" or
// "disabled". Read-only over already-thread-safe state (targetSource,
// disabledTargetSet); does not take e.mu (no lock-ordering conflict with
// reconcileConsumers).
func (e *Engine) ListTargets(_ context.Context) ([]TargetSummary, error) {
	ids := e.source.targetIDs()
	sort.Strings(ids)
	out := make([]TargetSummary, 0, len(ids))
	for _, id := range ids {
		t, ok := e.source.target(id)
		if !ok {
			// Removed between targetIDs() and target() (registry update raced
			// this snapshot) — skip; the next list reflects current reality.
			continue
		}
		gaps := make([]string, 0, len(t.Gaps))
		for col := range t.Gaps {
			gaps = append(gaps, col)
		}
		sort.Strings(gaps)
		state := targetStateActive
		if e.isTargetDisabled(id) {
			state = targetStateDisabled
		}
		out = append(out, TargetSummary{
			TargetID: t.TargetID,
			LensRef:  t.LensRef,
			Gaps:     gaps,
			State:    state,
		})
	}
	return out, nil
}

// Disable writes the `<targetId>.__control` dispatch-skip marker to
// weaver-state and updates the in-memory disabled-set, THEN
// pauses targetID's lane-1 KV-CDC consumer
// (substrate.ConsumerSupervisor.Pause — PauseManual, survives a restart via
// HealthSink restoreState with no new persistence). handleRow skips the
// remediation loop for targetID immediately — including for an
// already-in-flight/redelivered message.
//
// Order is fail-safe-to-inert: the marker (durable remediation-skip authority)
// is written before the Pause, so a partial failure (marker set, Pause
// failed/process died) lands on "still disabled (inert)" — handleRow's
// remediation-skip is already in effect — never "acting when the operator said
// stop". On restart the `__control` marker is the authority for the
// remediation-skip; the HealthSink pause-restore is independent and governs
// only lane-1 pumping.
//
// Disabling a target does not remove its meta.weaverTarget registration, does
// not call reconcileConsumers's removal path, and does not touch the target's
// Lens definition — the target stays "installed", just inert.
//
// Returns an error if targetID is not currently registered.
func (e *Engine) Disable(ctx context.Context, targetID string) error {
	if _, ok := e.source.target(targetID); !ok {
		return fmt.Errorf("weaver: target %q not registered", targetID)
	}
	if err := e.marks.setDisabled(ctx, targetID, true); err != nil {
		return fmt.Errorf("weaver: disable %q: write control marker: %w", targetID, err)
	}
	e.disabled.set(targetID, true)
	e.supervisor.Pause(ctx, laneConsumerPrefix+targetID)
	e.logger.Info("weaver: target disabled", "targetId", targetID)
	return nil
}

// Enable resumes targetID's lane-1 KV-CDC consumer
// (substrate.ConsumerSupervisor.Resume) FIRST, THEN deletes the
// `<targetId>.__control` dispatch-skip marker and clears the in-memory
// disabled-set. Resuming first is fail-safe-to-inert: if the marker
// delete (or the process) fails after the Resume, the target lands on "resumed
// but still remediation-inert" (the surviving marker keeps handleRow skipping)
// — never "pumping rows and remediating after a half-applied enable". The
// operator re-issues enable to heal.
//
// Returns an error if targetID is not currently registered.
func (e *Engine) Enable(ctx context.Context, targetID string) error {
	if _, ok := e.source.target(targetID); !ok {
		return fmt.Errorf("weaver: target %q not registered", targetID)
	}
	e.supervisor.Resume(ctx, laneConsumerPrefix+targetID)
	if err := e.marks.setDisabled(ctx, targetID, false); err != nil {
		return fmt.Errorf("weaver: enable %q: clear control marker: %w", targetID, err)
	}
	e.disabled.set(targetID, false)
	// A revoke removed the lane-1 durable; a subsequent enable must restore the
	// consumer immediately rather than waiting for the next registry event. The
	// reconcile re-Adds an inert consumer (the marker is now cleared, so it
	// pumps live) for any still-registered target whose durable is absent.
	e.reconcileConsumers()
	e.logger.Info("weaver: target enabled", "targetId", targetID)
	return nil
}

// Revoke is a strict superset of Disable plus immediate operator-convenience
// cleanup: it (a) removes targetID's lane-1 durable
// (substrate.ConsumerSupervisor.Remove — durable deleted, mirrors
// reconcileConsumers's removal semantics) and deletes the consumer's
// health-sink entry, (b) deletes every weaver-state key with prefix
// "<targetID>." via markStore.deleteByTargetPrefix — every
// <targetId>.<entityId>.<gapColumn> in-flight mark, every
// <targetId>.<entityId>.<gapColumn>.__count retry budget, every
// <targetId>.__effect.<gapColumn>.<actionRef> confidence window, AND the
// <targetId>.__control marker — and (c) clears the standing issueCache
// entries for targetID, THEN (d) re-writes the `<targetId>.__control`
// disabled marker and sets the in-memory disabled-set so that if
// reconcileConsumers later re-Adds this target's consumer, dispatch stays
// inert until an explicit Enable.
//
// Revoke does not mutate the meta.weaverTarget vertex/spec — unregistering the
// Lens definition is an op-path concern, out of this story's scope.
//
// Unlike Disable/Enable, Revoke is NOT an error if targetID is not currently
// registered — a revoke of an already-torn-down/unknown target is idempotent,
// mirroring ConsumerSupervisor.Remove's no-op-if-unmanaged posture. The
// `<targetId>.__control` marker is still written in this case: harmless until
// (unless) targetID is ever (re-)registered, at which point it correctly
// starts that target disabled.
func (e *Engine) Revoke(ctx context.Context, targetID string) error {
	name := laneConsumerPrefix + targetID
	if err := e.supervisor.Remove(ctx, name); err != nil {
		return fmt.Errorf("weaver: revoke %q: remove consumer: %w", targetID, err)
	}
	// Drop the engine's last-applied lane-1 fingerprint for this target so a
	// later reconcileConsumers sees running==false and re-Adds an (inert)
	// consumer for the still-registered target — without this the durable stays
	// permanently removed (reconcile would see running==applied and skip). The
	// re-added consumer pumps rows that all Ack-skip via the re-written
	// `__control` marker (below) until an explicit Enable. Under the same e.mu
	// reconcileConsumers holds.
	e.mu.Lock()
	delete(e.targets, targetID)
	e.mu.Unlock()

	sink := healthkv.NewConsumerSink(e.conn, e.cfg.HealthKVBucket, "weaver", name, e.states)
	if err := sink.Delete(ctx); err != nil {
		e.logger.Error("weaver: revoke: consumer health-state cleanup failed", "targetId", targetID, "err", err)
	}

	if _, err := e.marks.deleteByTargetPrefix(ctx, targetID); err != nil {
		return fmt.Errorf("weaver: revoke %q: delete weaver-state keys: %w", targetID, err)
	}

	e.issues.clear(issueKeyConsumer(targetID))
	e.issues.clear(issueKeyTimer(targetID))
	// The gap, gap-config, data and template families are keyed per
	// (entity, column) below the target, so a revoked target's standing entries
	// have no single key to name: retire each family by prefix, exactly as the
	// weaver-state teardown above retires the target's keys by prefix. Without
	// this, one issue per (entity, column) stands for a target that no longer
	// exists until the process restarts.
	for _, prefix := range issueKeyTargetPrefixes(targetID) {
		e.issues.clearPrefix(prefix)
	}
	// The in-memory republish obligations go with them: the weaver-state
	// teardown above deleted every mark they were owed against, so no delivery
	// can consult them and no publish can retire them.
	e.republish.clearTarget(targetID)
	if ownerID, ok := e.source.ownerVertexID(targetID); ok {
		e.issues.clear("target:" + ownerID)
	}

	// Re-write the disabled marker AFTER the prefix-delete (which removed it
	// along with everything else) so a target re-added by a later reconcile
	// stays inert until an explicit Enable — Revoke is a strict superset of
	// Disable.
	if err := e.marks.setDisabled(ctx, targetID, true); err != nil {
		return fmt.Errorf("weaver: revoke %q: write control marker: %w", targetID, err)
	}
	e.disabled.set(targetID, true)

	e.logger.Info("weaver: target revoked", "targetId", targetID)
	return nil
}

// ResetConfidence deletes every `<targetId>.__effect.<gapColumn>.<actionRef>`
// confidence window registered under targetID and returns how many were
// removed. It sits on the operator-severity ladder between the verbs that
// touch no data and the one that deletes everything: `disable` pauses and
// deletes nothing, `resetBudget` rewrites one gap's retry budget,
// `resetConfidence` deletes this target's advisory confidence windows, and
// `revoke` deletes everything under the target prefix and disables.
//
// Only `__effect` keys are touched: in-flight marks, `…__count` retry budgets,
// and the `<targetId>.__control` marker all survive, and so do every other
// target's windows. The target's dispatch state is untouched — a reset neither
// disables nor enables, and the lane-1 consumer keeps pumping.
//
// Each delete is conditioned on the revision read in this pass (mirroring the
// sweep's deleteEffect): a dispatch or close that lands between the list and
// the delete wins the conflict and survives as honest new history, so a reset
// can never silently discard a window it never observed. A skipped key is not
// an error and is not counted; re-running the verb is the remedy.
//
// The window is advisory-only today (`flagEffectMismatches`' heartbeat scan
// and planner_shadow's effectCloseRate, which no installed target enables), and
// every reader treats a missing key as "no data" rather than a zero close rate
// — so deletion is safe by construction. Once the windows are gone the next
// heartbeat scan lists nothing for the target and the standing
// LensEffectMismatch issues clear through the existing reconciliation loop;
// honest windows rebuild from the next genuine episode.
//
// Returns an error if targetID is not currently registered (mirroring
// Disable/Enable — a window whose target is gone is already sweepEffect's
// orphan leg, not an operator's).
func (e *Engine) ResetConfidence(ctx context.Context, targetID string) (int, error) {
	if _, ok := e.source.target(targetID); !ok {
		return 0, fmt.Errorf("weaver: target %q not registered", targetID)
	}
	deleted, err := e.marks.deleteEffectWindows(ctx, targetID)
	if err != nil {
		return deleted, fmt.Errorf("weaver: reset confidence %q: %w", targetID, err)
	}
	e.logger.Info("weaver: target confidence reset", "targetId", targetID, "windowsDeleted", deleted)
	return deleted, nil
}

// ResetRetryBudget re-arms ONE gap's §E retry budget — the un-park verb. It
// writes the (target, entity, gap) dispatch-count back to 0 and returns the
// value it replaced; the caller reports that, so an operator sees what the park
// had actually spent.
//
// The verb states intent and stops there: it does not clear the gap's standing
// GapBudgetExhausted issue and does not dispatch anything. The next reconciler
// pass (≤ 1 min) does both, because a gap with a fresh budget is a gap the
// count leg finds violating, open, unsuppressed and markless — its re-arm arm
// dispatches, and planGap's own "a plan about to fire disproves the park" clear
// retires the issue. One deterministic key, one writer, one path.
//
// Scope is one gap, never a target: the count is per-(target, entity, gap) and
// the issue latch is keyed the same way, so a target-wide reset would re-arm
// parks nobody looked at.
//
// Errors, each of which means nothing was written:
//   - the target is not registered (mirrors ResetConfidence — a budget whose
//     target is gone is the sweep's orphan problem, not an operator's, and the
//     count leg's registry gate would refuse to act on it anyway);
//   - the arguments are not the §10.2/§10.3 key shapes (a malformed argument
//     must never reach the keyspace splitCountKey later reads);
//   - the sweep's re-arm PERMANENTLY declines the gap whatever its budget says
//     — an orphaned column, a `surface` gap, a plan-time-resolved action, or a
//     collapse-only action (reArmDeclines);
//   - there is no budget at this gap — a count key exists only where a chain
//     has actually dispatched, so this is the honest answer to a typo'd
//     entityId rather than a silent success;
//   - the count changed between the read and the write (a dispatch landed
//     first): the value the operator decided on is gone, so the write is
//     refused rather than clobbering a fresh attempt back to 0 and handing a
//     moving chain a second full budget. Re-running re-reads and re-decides.
func (e *Engine) ResetRetryBudget(ctx context.Context, targetID, entityID, gapColumn string) (previous int, err error) {
	if !singleTokenPattern.MatchString(targetID) {
		return 0, fmt.Errorf("weaver: targetId %q must be a single token matching %s", targetID, singleTokenPattern.String())
	}
	if !substrate.IsValidNanoID(entityID) {
		return 0, fmt.Errorf("weaver: entityId %q must be a %d-character NanoID", entityID, substrate.NanoIDLength)
	}
	if !strings.HasPrefix(gapColumn, gapColumnPrefix) || !singleTokenPattern.MatchString(gapColumn) {
		return 0, fmt.Errorf("weaver: gapColumn %q must be a single-token %s* column", gapColumn, gapColumnPrefix)
	}
	target, ok := e.source.target(targetID)
	if !ok {
		return 0, fmt.Errorf("weaver: target %q not registered", targetID)
	}
	if reason := e.reArmDeclines(ctx, target, targetID, entityID, gapColumn); reason != "" {
		// The sweep's re-arm will not act on this gap whatever the budget says,
		// so writing a 0 would change nothing an operator can observe: the gap
		// would sit at a count of 0, still parked, still holding a
		// GapBudgetExhausted that now describes a budget it no longer has.
		// Refusing keeps that standing issue TRUE and names which shape is in
		// the way — the four shapes are four different problems with four
		// different remedies, so one shared wording would misdirect.
		return 0, fmt.Errorf("weaver: target %q entity %q gap %q: %s; resetting its budget would leave it parked with a fresh budget",
			targetID, entityID, gapColumn, reason)
	}
	previous, revision, found, err := e.budgets.dispatchCountEntry(ctx, targetID, entityID, gapColumn)
	if err != nil {
		return 0, fmt.Errorf("weaver: reset retry budget %s.%s.%s: %w", targetID, entityID, gapColumn, err)
	}
	if !found {
		// Nothing is written for a gap that has no budget: a count key exists
		// only where a chain has actually dispatched, so creating one would
		// invent a park to un-park — and on a mistyped entityId it would hand
		// the sweep's re-arm arm a gap nobody chose.
		return 0, fmt.Errorf("weaver: no retry budget for target %q entity %q gap %q (nothing has dispatched it)",
			targetID, entityID, gapColumn)
	}
	conflict, err := e.budgets.resetDispatchCount(ctx, targetID, entityID, gapColumn, revision)
	if err != nil {
		return 0, fmt.Errorf("weaver: reset retry budget %s.%s.%s: %w", targetID, entityID, gapColumn, err)
	}
	if conflict {
		return previous, fmt.Errorf("weaver: retry budget for target %q entity %q gap %q changed during the reset; re-run to reset the current value",
			targetID, entityID, gapColumn)
	}
	e.logger.Info("weaver: retry budget reset",
		"targetId", targetID, "entityId", entityID, "gap", gapColumn, "previousCount", previous)
	return previous, nil
}

// ReplayTarget re-delivers targetID's CURRENT row set through the unchanged
// lane-1 evaluation ladder, by delete-then-creating the target's lane-1 durable
// (substrate.ConsumerSupervisor.Reset, which runs the pair under that
// consumer's own resetMu). A JetStream DeliverPolicy is fixed at first create,
// so recreating the durable — never updating it — is the only route back to the
// head of every subject under the target's prefix; the recreated consumer is
// DeliverLastPerSubject, so it delivers the current revision of every row the
// target has, once each.
//
// The durable's NAME is stable across the recreate. A lane-1 durable is
// per-target and its name keys the per-consumer health sink, so a nonce would
// churn those keys and need a prune pass of its own; the rule the recreate
// honors is DeliverPolicy-at-first-create, which a stable name satisfies.
//
// It exists for the two populations the standing decline loop cannot reach.
// A row DECLINED AND ACKED — every such row projected before the loop existed,
// and the narrow Ack-exit windows that remain — is owed no redelivery by
// anything, so only a re-enumeration reaches it. And a target whose Nak'd-
// pending set outlived a NATS restart holds no armed server-side redelivery
// timer: any single delivery under the target's prefix re-arms the whole set,
// so a target with live traffic heals itself and a fully-quiet one does not.
// Ordinary operation needs the verb for neither: a declined row rides its own
// redelivery floor and re-evaluates against current config on every tick.
//
// One invocation costs O(the target's current rows) through the full lane-1
// preamble, and for a violating row whose mark has aged past its lease it
// re-fires that row's episode. That is why it is manual: an operator invokes it
// holding evidence, rather than the engine paying it on every boot.
//
// Returns how many rows the recreated durable had queued to deliver at the
// instant the verb returned — the size of the burst just ordered. Reset signals
// each pump to re-open and returns WITHOUT waiting for it (that is Reset's
// documented contract; ResetAwaitReopen is the variant that waits), so in the
// ordinary case the pump still holds its old iterator when this count is read
// and the number is the whole replay set. It is nonetheless a LOWER BOUND rather
// than a promise: nothing orders the pump's re-open against this read, so a pump
// that reopened first will have drained some of it. A count that cannot be read
// is reported as 0 with a log line rather than as an error, because the replay
// has already happened by then and failing the call would describe it wrongly.
//
// Refusals. The verb's whole effect is what the lane-1 ladder does with the
// replayed rows, so it must refuse exactly the shapes that ladder PERMANENTLY
// declines — accepting one would recreate a durable, re-deliver every row, and
// report success for a pass in which nothing observable changed. TRANSIENT
// declines are the opposite case and are all accepted: a row not yet projected,
// an unresolved reference mid-convergence, an admission deferral, a gap whose
// mark is live (the anti-storm Ack) and a gap whose budget is spent all lift or
// are owed on their own, and the replay is exactly what re-presents them. Three
// shapes are permanent, each with its own message because each is a different
// problem with a different fix:
//
//   - the target is not registered — handleRow returns at the registry miss for
//     every row of an unregistered target, so the replay would deliver a set
//     nothing evaluates (mirrors Disable/Enable/ResetConfidence's own check);
//   - the target is DISABLED — every replayed row Acks at handleRow's
//     dispatch-skip without remediating, and the lane-1 pump is paused besides,
//     so the recreated durable would not even be read. Only an operator Enable
//     lifts it, and Enable already resumes remediation for every row still
//     violating, so the order is enable-then-replay;
//   - the lane-1 consumer is not managed by this instance — a Revoke removed
//     the durable, or its Add failed (the target carries a standing
//     ConsumerReconcileError). There is nothing to recreate, so the reset would
//     report "not managed" from inside the supervisor rather than naming the
//     remedy, which is `enable` (it re-Adds the consumer).
//
// A pump paused by the supervisor's own failure-class probe loop is deliberately
// NOT refused: that pause lifts on its own once the probe succeeds, and the
// replayed rows are owed to it either way.
func (e *Engine) ReplayTarget(ctx context.Context, targetID string) (rowsQueued int, err error) {
	if _, ok := e.source.target(targetID); !ok {
		return 0, fmt.Errorf("weaver: target %q not registered", targetID)
	}
	if e.isTargetDisabled(targetID) {
		return 0, fmt.Errorf("weaver: target %q is disabled: its lane-1 pump is paused and every row it delivers "+
			"Acks at the dispatch-skip without remediating, so a replay would change nothing observable; "+
			"enable it first — enable itself resumes remediation for every row still violating", targetID)
	}
	name := laneConsumerPrefix + targetID
	if !e.supervisor.IsManaged(name) {
		return 0, fmt.Errorf("weaver: replay %q: lane-1 consumer %q is not managed by this instance, so there is no "+
			"durable to recreate — a revoke removed it, or its add failed (the target's ConsumerReconcileError "+
			"issue names which); `enable` re-adds the consumer", targetID, name)
	}
	// The delete/create pair runs on a context detached from the caller's, under
	// its own generous bound. Reset deletes the durable and then creates it, and
	// a cancellation landing BETWEEN those two calls leaves the target with no
	// durable at all — a state nothing else in the engine repairs: the pump
	// retries `Consumer(...)` forever on a capped backoff without taking a pause
	// reason, so the health sink still reads `running`, and reconcileConsumers
	// skips a target whose fingerprint is unchanged. The control plane's handler
	// deadline is 5 s, which is well inside the range where that window is
	// reachable, so the pair must not inherit it. The bound below still exists,
	// because a detached call must not be able to wedge the responder goroutine
	// — and the standing issue raised on failure is what makes the residual
	// window observable instead of silent.
	resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replayResetTimeout)
	defer cancel()
	if err := e.supervisor.Reset(resetCtx, name); err != nil {
		// A failed reset can have deleted the durable without recreating it, and
		// that target then evaluates nothing while every other signal reports it
		// healthy. Raise the same standing issue reconcileConsumers raises for
		// its own failed Add/Remove/Reset — same key, so a later successful
		// reconcile or replay retires it — and name the remedy, which is this
		// verb again rather than `enable` (Resume is a no-op on an unpaused pump
		// and the reconcile skips an unchanged fingerprint).
		e.issues.set(issueKeyConsumer(targetID), "error", "ConsumerReconcileError",
			"target "+targetID+": lane-1 consumer replay failed: "+err.Error()+
				" — the durable may have been deleted without being recreated, in which case the target "+
				"evaluates nothing until this verb is re-run")
		return 0, fmt.Errorf("weaver: replay %q: %w", targetID, err)
	}
	// A completed reset disproves any wedge standing at that key, including one
	// an earlier failed replay or reconcile left there.
	e.issues.clear(issueKeyConsumer(targetID))

	// The count the operator is owed is how much work this replay re-queued,
	// which includes a row the pump has already fetched but not yet acked. The
	// pump starts consuming the instant Reset recreates the durable, so a row it
	// wins the race to fetch has already left NumPending — and PendingForConsumer
	// reads NumPending alone, so racing it reports rowsQueued 0 for a replay that
	// did queue a row, leaving the operator's diagnostic standing over a fact
	// that is not true. OutstandingForConsumer adds NumAckPending, which on a
	// just-recreated durable counts exactly this replay's own deliveries.
	queued, perr := e.supervisor.OutstandingForConsumer(resetCtx, name)
	if perr != nil {
		e.logger.Warn("weaver: replay recreated the durable but its queued-row count could not be read",
			"targetId", targetID, "durable", name, "err", perr)
		queued = 0
	}
	e.logger.Info("weaver: target replayed",
		"targetId", targetID, "durable", name, "rowsQueued", queued)
	return int(queued), nil
}

// reArmDeclines reports WHY the reconciler's count-leg re-arm would refuse to
// dispatch this gap, or "" when nothing about the gap's own definition stands
// in the way.
//
// The arm's declines split in two, and only one half belongs here. A TRANSIENT
// decline — the registry warm-up, replay lag, a row not yet projected — lifts on
// its own and the arm acts on a later pass, so the verb must ACCEPT it: the
// budget an operator resets is spent the moment the condition clears. A
// PERMANENT decline is a property of the gap's CLASS, never lifts for that gap,
// and makes the reset a false success — a 0 nothing will ever act on, reported
// as if the gap were re-armed. Four shapes are permanent, and each mirrors the
// arm's own predicate rather than re-deriving one:
//
//   - the playbook no longer names the column (the leg's orphan-column arm).
//     Weaver has no remediation to re-arm, and the arm cannot even retire the
//     standing issue there, because lane 1 raises at the same latch for the
//     same column;
//   - the action is FR29's `surface` (surfaceOnlyGap, the predicate the arm
//     itself calls): the gap dispatches nothing at all, so it has no episode to
//     re-arm and its count is stranded from an action a package upgrade
//     replaced;
//   - the playbook names no action, leaving a planned/goal-mode gap to resolve
//     one at plan time. The arm refuses it because only a plan could say what it
//     would fire, and running one to find out would consume an admission token
//     and clear the gap's standing issues for a dispatch that may not happen;
//   - the action is collapse-only (the arm's collapseOnlyReclaim over
//     staleMark), read over the gap's current row so an EXTERNAL gap — whose
//     re-dispatch §10.3 calls for — is not refused alongside the human ones. A
//     row that is missing or unreadable classifies as collapse-only for the
//     three collapse-only actions, which is the same answer the arm reaches: it
//     does not dispatch a gap with no row either.
//
// It answers only what this gap's own definition decides, never "will this gap
// dispatch" — that depends on gates (a freeze, the registry, the row's own
// columns) which move independently of the operator's decision and are the
// sweep's to apply, not this verb's to predict. An UNREGISTERED target is
// likewise not this function's business: replay lag is not evidence a playbook
// dropped anything, so the verb refuses that one check earlier, under its own
// reason, and a target mid-replay is never reported as an orphaned column.
//
// The two action-shape declines are answered before the row is read, mirroring
// the arm's own order: neither consults the row, and a gap that dispatches
// nothing has no reason to cost a KV round trip.
func (e *Engine) reArmDeclines(ctx context.Context, target *Target, targetID, entityID, gapColumn string) string {
	ga, planned := target.Gaps[gapColumn]
	if !planned {
		return fmt.Sprintf("its playbook declares no gaps entry for %q, so there is no remediation to re-arm — "+
			"the column is orphaned (a package re-author dropped it) and its budget expires with its own TTL", gapColumn)
	}
	if surfaceOnlyGap(ga) {
		return fmt.Sprintf("its playbook action is %q, which raises a Health issue while the column is true and "+
			"dispatches nothing — there is no episode to re-arm, and the budget is stranded from the action "+
			"the package replaced", ga.Action)
	}
	if ga.Action == "" {
		return "its playbook names no action and resolves one at plan time (a planned/goal-mode gap), and the " +
			"sweep's re-arm never runs a plan to find out what it would fire — the re-arm this verb serves " +
			"would decline it on every pass"
	}
	var row map[string]any
	if entry, err := e.conn.KVGet(ctx, e.cfg.WeaverTargetsBucket, targetID+"."+entityID); err == nil {
		if uErr := json.Unmarshal(entry.Value, &row); uErr != nil {
			row = nil
		}
	}
	if collapseOnlyReclaim(ga.Action, e.staleMark(targetID, entityID, row, gapColumn, ga)) {
		return fmt.Sprintf("it dispatches %q, whose artifact may still be open, and the sweep never re-arms a "+
			"collapse-only gap — a fresh episode would mint a new claimId and duplicate it", ga.Action)
	}
	return ""
}

// freezeOscillatingPair disables both fighting targets (Engine.Disable — the
// same operator-facing `__control` seam a manual freeze uses) and raises ONE
// standing Health issue naming the causal pair and the contested aspect
// path (design weaver-planner-mandate-design.md §3.4). Freeze-and-alert
// only: neither target is un-registered and no new dispatch is made — an
// operator investigates and re-Enables once the authoring conflict is
// fixed. A Disable failure (e.g. the target was removed between its last
// dispatch and this call) is logged, not fatal — the issue still names the
// pair for the operator.
func (e *Engine) freezeOscillatingPair(ctx context.Context, targetA, targetB string, path guardgrammar.Path) {
	for _, id := range []string{targetA, targetB} {
		if err := e.Disable(ctx, id); err != nil {
			e.logger.Error("weaver: oscillation freeze failed", "targetId", id, "err", err)
		}
	}
	e.alert(issueKeyOscillation(targetA, targetB, path), "error", "TargetOscillation",
		"targets "+targetA+" and "+targetB+" are alternately dispatching against "+pathString(path)+
			"; both frozen pending operator review")
}

// pathString renders a guard-grammar Path in its §10.5 dotted form, for a
// human-readable oscillation alert.
func pathString(p guardgrammar.Path) string {
	if p.Aspect == "" {
		return "subject.data." + p.Field
	}
	return "subject." + p.Aspect + ".data." + p.Field
}

// issueKeyOscillation is deterministic regardless of which target the
// alternation started on (A,B,A,B and B,A,B,A name the same fight) — sorting
// the pair means the same fight always collapses to the same issue key.
func issueKeyOscillation(targetA, targetB string, path guardgrammar.Path) string {
	if targetB < targetA {
		targetA, targetB = targetB, targetA
	}
	return "oscillation:" + targetA + "." + targetB + "." + pathString(path)
}
