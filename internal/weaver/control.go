package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
	// The gap and data families are keyed per (entity, column) below the
	// target, so a revoked target's standing entries have no single key to
	// name: retire each family by prefix, exactly as the weaver-state teardown
	// above retires the target's keys by prefix. Without this, one issue per
	// (entity, column) stands for a target that no longer exists until the
	// process restarts.
	for _, prefix := range issueKeyTargetPrefixes(targetID) {
		e.issues.clearPrefix(prefix)
	}
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
