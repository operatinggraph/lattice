package weaver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/asolgan/lattice/internal/healthkv"
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
	for _, key := range keys {
		targetID, ok := strings.CutSuffix(key, controlKeySuffix)
		if !ok {
			continue
		}
		disabled, err := e.marks.isDisabledKey(ctx, key)
		if err != nil {
			e.logger.Error("weaver: seed disabled-target read failed", "targetId", targetID, "err", err)
			continue
		}
		if disabled {
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
// <targetId>.<entityId>.<gapColumn> in-flight mark AND the
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

	sink := healthkv.NewConsumerSink(e.conn, e.cfg.HealthKVBucket, "weaver", e.cfg.Instance, name, e.states)
	if err := sink.Delete(ctx); err != nil {
		e.logger.Error("weaver: revoke: consumer health-state cleanup failed", "targetId", targetID, "err", err)
	}

	if _, err := e.marks.deleteByTargetPrefix(ctx, targetID); err != nil {
		return fmt.Errorf("weaver: revoke %q: delete weaver-state keys: %w", targetID, err)
	}

	e.issues.clear(issueKeyConsumer(targetID))
	e.issues.clear(issueKeyData(targetID, freshUntilColumn))
	e.issues.clear(issueKeyTimer(targetID))
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
