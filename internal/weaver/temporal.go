package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Lane-3 subject constants (Contract #10 §10.4). The schedule subject is keyed
// per target AND entity — schedule.weaver.timer.<targetId>.<entityId> — so two
// targets projecting a freshness deadline for the same entity hold independent
// timer slots (no cross-target last-write-wins); the fired target mirrors it
// under the fired prefix. Both segments are single dot-free tokens (targetId is
// install-validated, entityId is the row's NanoID — never the dotted vertex
// key; the full entity key rides the payload).
const (
	scheduleSubjectPrefix = "schedule.weaver.timer."
	firedSubjectPrefix    = "schedule.weaver.timer.fired."
)

// firedToken is the subject token that separates the fired namespace from the
// pending-schedule namespace under schedule.weaver.timer.>. A targetId equal to
// this token would make its pending schedule subjects land inside the temporal
// consumer's filter, so the scheduling leg refuses it loudly.
const firedToken = "fired"

// freshUntilColumn is the engine-recognized optional §10.2 row column carrying
// the freshness deadline (RFC3339). The window computation lives in the target
// cypher — the Lens projects resolve + window; the engine only converts the
// instant into an @at schedule. Carried as a free-form param column; documented
// in docs/components/weaver.md.
const freshUntilColumn = "freshUntil"

// temporalConsumerName is the lane-3 durable on core-schedules. It is a FIXED
// durable (not per-instance): its ack floor IS the missed-while-down recovery —
// fired messages persist in the stream under limits retention and the durable
// resumes from its floor on restart.
//
// core-schedules carries no MaxAge and the tracker dedup horizon (Contract #4,
// 24h) is finite, so a durable deleted+recreated could in principle DeliverAll-
// replay a fired message older than the tracker. This is accepted for Phase 2:
// the handler's read-before-act guard (handleFiredTimer) re-reads the current
// weaver-targets row before submitting, so a replayed old firing whose entity
// has since been deleted or re-armed with a later deadline Acks without acting —
// a stale replay is harmless without a retention knob.
const temporalConsumerName = "weaver-temporal"

// temporalStats holds the since-start lane-3 counters the Contract #5
// heartbeater surfaces.
type temporalStats struct {
	scheduled atomic.Uint64
	fired     atomic.Uint64
}

// timerPayload is the schedule-message body: published by the scheduling leg,
// delivered verbatim back at the fired subject. The subject carries only the
// dot-free <targetId>.<entityId> tokens; the payload carries the full entity
// key and the fire instant.
type timerPayload struct {
	EntityKey string `json:"entityKey"`
	TargetID  string `json:"targetId"`
	FireAt    string `json:"fireAt"`
}

// temporalSpec describes the lane-3 supervised consumer: a static durable on
// the core-schedules stream filtered to Weaver's fired-timer subject space.
func (e *Engine) temporalSpec() substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:          temporalConsumerName,
		Stream:        e.cfg.CoreSchedulesStream,
		FilterSubject: firedSubjectPrefix + ">",
		DeliverPolicy: substrate.DeliverAll,
		Handler:       supervisedHandler(e.handleFiredTimer),
		Health:        newConsumerHealthSink(e.conn, e.cfg.HealthKVBucket, e.cfg.Instance, temporalConsumerName, e.states),
		Logger:        e.logger,
	}
}

// scheduleFreshness is the lane-3 scheduling leg, run from handleRow on every
// non-tombstone row delivery (violating or not — level-driven): when the row
// carries a future freshUntil, the Actuator publishes the per-target-per-entity
// @at schedule. Every delivery re-publishes idempotently — one-schedule-per-
// subject replace makes the re-publish a no-op-equivalent, and a row
// re-projected with a NEW freshUntil replaces the prior timer. Returns false
// only when the schedule publish itself failed (the caller Naks with delay —
// bounded cadence, never a hot loop); data errors are surfaced and skipped
// (redelivery cannot fix a projected row).
func (e *Engine) scheduleFreshness(ctx context.Context, targetID, entityID, key string, row map[string]any) bool {
	// Freshness timers arm/re-arm even while the target is disabled: scheduling
	// is bookkeeping that keeps lane-3 state current, so an instant re-enable
	// loses no deadline. The disabled state suppresses only the remediation
	// loop (handleRow's dispatch leg), not this state-recording leg.
	v, ok := row[freshUntilColumn]
	if !ok || v == nil {
		// The column is absent (never projected, or a prior bad value was fixed
		// by removing it): clear any standing RowDataError so the level signal
		// does not lie quiet-but-set after the data is repaired.
		e.issues.clear(issueKeyData(targetID, freshUntilColumn))
		return true
	}
	s, isString := v.(string)
	var fireAt time.Time
	var perr error
	if isString {
		fireAt, perr = time.Parse(time.RFC3339, s)
	}
	if !isString || perr != nil {
		msg := fmt.Sprintf("target %s: row %s column %q is not an RFC3339 string (%v); freshness timer not scheduled",
			targetID, key, freshUntilColumn, v)
		e.logger.Warn("weaver: " + msg)
		e.issues.set(issueKeyData(targetID, freshUntilColumn), "warning", "RowDataError", msg)
		return true
	}
	if targetID == firedToken {
		// schedule.weaver.timer.fired.<entityId> would sit inside the temporal
		// consumer's filter as a pending schedule — the fired namespace is
		// reserved.
		msg := fmt.Sprintf("targetId %q is reserved in the timer subject space (schedule.weaver.timer.%s.>); freshness timer not scheduled",
			firedToken, firedToken)
		e.alert(issueKeyTimer(targetID), "error", "ScheduleConfigError", msg)
		return true
	}
	entityKey, _ := row["entityKey"].(string)
	if entityKey == "" {
		msg := fmt.Sprintf("target %s: row %s carries %q but no entityKey; freshness timer not scheduled",
			targetID, key, freshUntilColumn)
		e.logger.Warn("weaver: " + msg)
		e.issues.set(issueKeyData(targetID, freshUntilColumn), "warning", "RowDataError", msg)
		return true
	}
	e.issues.clear(issueKeyData(targetID, freshUntilColumn))

	// Truncate to whole seconds so the header instant, the payload instant,
	// and the §10.4 requestId seed are byte-identical strings. A past instant
	// is published verbatim: nats-server stores an overdue @at and fires it
	// immediately, which is correct level semantics (the deadline has passed,
	// the freshness expiry should fire now). The payload's fireAt remains the
	// deadline instant — not "now" — so a re-projected past deadline derives
	// the SAME deterministic requestId and the Contract #4 tracker collapses
	// the duplicate.
	fireAtStr := fireAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	payload, err := json.Marshal(timerPayload{EntityKey: entityKey, TargetID: targetID, FireAt: fireAtStr})
	if err != nil {
		// A row that cannot be marshalled will marshal-fail identically on every
		// redelivery — surface as a data error and skip (Ack), never a perpetual
		// NakWithDelay.
		msg := fmt.Sprintf("target %s: row %s: marshalling the freshness timer payload failed: %v; timer not scheduled",
			targetID, key, err)
		e.alert(issueKeyData(targetID, freshUntilColumn), "error", "RowDataError", msg)
		return true
	}
	if err := e.act.scheduleTimer(ctx, targetID, entityID, payload, fireAtStr); err != nil {
		// A publish failure is retryable (core-schedules degraded): NakWithDelay
		// on a bounded cadence and surface the standing failure to Health so the
		// outage is visible, not just logged. Cleared on the next success below.
		msg := fmt.Sprintf("target %s: entity %s: freshness timer schedule publish failed: %v",
			targetID, entityID, err)
		e.logger.Error("weaver: " + msg + "; nak with delay")
		e.issues.set(issueKeyTimer(targetID), "warning", "SchedulePublishError", msg)
		return false
	}
	e.issues.clear(issueKeyTimer(targetID))
	e.temporal.scheduled.Add(1)
	return true
}

// handleFiredTimer is the lane-3 handler: one delivered message = one fired
// timer republished by the NATS scheduler at schedule.weaver.timer.fired.
// <targetId>.<entityId>. It converts the firing into a MarkExpired op via the
// Processor — never injected into core-events — under the §10.4 deterministic
// requestId (schedule subject + fire instant), so an at-least-once redelivery
// of the same firing collapses on the Contract #4 vtx.op.<requestId> tracker
// while a new firing of a re-armed timer (new fireAt) is a genuinely new op.
// No weaver-state mark is taken: the requestId is the dedup for this leg;
// marks/OCC belong to remediation dispatch (lane-1, after the violation
// flips). A MarkExpired rejected at the Processor is not re-attempted — the
// freshness flip then waits for the next CDC touch of the entity.
//
// Before submitting, the handler reads the current weaver-targets row: a
// deleted entity (absent row) or a row re-armed with a strictly later
// freshUntil (renewed while the engine was down) suppress the firing with an
// Ack. A present row whose target is not in the registry cache NakWithDelays
// (the registry replays asynchronously at startup with no replay-done signal —
// dropping would discard a valid missed-while-down firing during that window).
// This read-before-act is the only stale-firing guard on the mark-less temporal
// leg, and it makes a durable-replay of an old fired message harmless.
func (e *Engine) handleFiredTimer(ctx context.Context, msg substrate.Message) substrate.Decision {
	tail := strings.TrimPrefix(msg.Subject, firedSubjectPrefix)
	targetID, entityID, ok := splitRowKey(tail)
	if !ok {
		// Redelivery cannot fix a malformed subject; drop loudly (FR29). No
		// targetId is recoverable, so the issue is keyed on the fired prefix —
		// one bounded slot carrying the latest offending tail.
		e.alert(issueKeyTimer(""), "warning", "TimerDataError",
			"fired-timer subject tail "+tail+" is not <targetId>.<entityId>; dropped")
		return substrate.Ack
	}
	// A disabled target's already-armed timer STILL submits MarkExpired:
	// recording a freshness expiry is state-recording bookkeeping, not
	// remediation (the §9.3 read-before-act guards below already gate it on
	// the current row's presence/renewed deadline). Suppressing it here would
	// silently drop a freshness expiry across a disable→enable window. The
	// disabled state suppresses only the remediation loop (handleRow's
	// dispatch leg), not violation-detection bookkeeping.

	// The schedule subject is reconstructed from the fired subject — never
	// trusted from the payload.
	scheduleSubject := scheduleSubjectPrefix + targetID + "." + entityID

	var p timerPayload
	if err := json.Unmarshal(msg.Body, &p); err != nil {
		e.alert(issueKeyTimer(targetID), "warning", "TimerDataError",
			"fired timer "+tail+": payload unparseable; dropped: "+err.Error())
		return substrate.Ack
	}
	if p.EntityKey == "" || p.FireAt == "" {
		e.alert(issueKeyTimer(targetID), "warning", "TimerDataError",
			"fired timer "+tail+": payload lacks entityKey or fireAt; dropped")
		return substrate.Ack
	}
	firedAt, err := time.Parse(time.RFC3339, p.FireAt)
	if err != nil {
		e.alert(issueKeyTimer(targetID), "warning", "TimerDataError",
			"fired timer "+tail+": fireAt is not RFC3339; dropped: "+err.Error())
		return substrate.Ack
	}
	// The subject is authoritative for the targetId (it is install-validated and
	// reconstructed, not trusted from the wire); a payload targetId that
	// disagrees is a foreign or corrupt publish into schedule.> — drop loudly
	// rather than submit an op carrying one target's id with another's
	// entityKey.
	if p.TargetID != "" && p.TargetID != targetID {
		e.alert(issueKeyTimer(targetID), "warning", "TimerDataError",
			"fired timer "+tail+": payload targetId "+p.TargetID+" disagrees with the subject-derived "+targetID+"; dropped")
		return substrate.Ack
	}

	// Read-before-act: the firing may be stale (the engine was down while the
	// entity was re-armed with a later deadline, or deleted). The temporal leg
	// takes no mark, so this KV cross-check is the only suppression of a stale
	// MarkExpired — and it also makes a durable-replay of an old firing
	// harmless (the current row's freshUntil is strictly later, or the row is
	// gone, so the replay Acks without acting).
	rowEntry, err := e.conn.KVGet(ctx, e.cfg.WeaverTargetsBucket, targetID+"."+entityID)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// The entity is gone (tombstoned/never projected, or the target's
			// Lens stopped projecting after the target was removed): no expiry to
			// flip. Drop.
			e.logger.Debug("weaver: fired timer for absent entity row; dropping",
				"targetId", targetID, "entityId", entityID)
			return substrate.Ack
		}
		e.logger.Error("weaver: fired-timer row read failed; nak with delay",
			"targetId", targetID, "entityId", entityID, "err", err)
		return substrate.NakWithDelay
	}
	if _, ok := e.source.target(targetID); !ok {
		// The row is still present but the target is not in the registry cache.
		// This is ambiguous: at engine startup the registry replays
		// asynchronously (no replay-done signal), so an early miss may be replay
		// lag, not a genuine removal — and the temporal consumer can deliver a
		// missed-while-down firing before that replay lands. Ack-dropping here
		// would irreversibly discard a valid firing during the startup window, so
		// NakWithDelay on a bounded cadence: a replay-lag miss resolves on the
		// retry, and a genuinely-removed target's firing retries only until its
		// Lens stops projecting the row (then the absent-row leg above Acks it).
		e.logger.Debug("weaver: fired timer for an unregistered target (replay lag or removed); nak with delay",
			"targetId", targetID, "entityId", entityID)
		return substrate.NakWithDelay
	}
	if current, ok := currentFreshUntil(rowEntry.Value); ok && current.After(firedAt) {
		// The entity was re-armed with a strictly later deadline while this
		// firing waited (engine down across the re-projection): the pending
		// schedule already supersedes it, so this firing is stale — suppress.
		e.logger.Debug("weaver: fired timer superseded by a later freshUntil; dropping",
			"targetId", targetID, "entityId", entityID, "firedAt", p.FireAt, "current", current.Format(time.RFC3339))
		return substrate.Ack
	}

	requestID := deriveTimerRequestID(scheduleSubject, p.FireAt)
	payload := map[string]any{
		"entityKey": p.EntityKey,
		"targetId":  targetID,
		"expiredAt": p.FireAt,
	}
	// No authContext: MarkExpired is submitted under Weaver's service-actor
	// authority (the target-less directOp posture); the op's DDL/grants are
	// package data. ContextHint.Reads carries the entity ROOT key: the
	// freshnessMarker DDL hydrates it to assert the target exists + is alive
	// before writing the (non-sensitive) marker — a stale firing whose entity was
	// deleted fails closed rather than minting a dangling marker. The marker
	// write itself stays an UNCONDITIONED update (no expectedRevision); the read
	// is a parent-existence guard, not OCC on the marker.
	reads := []string{p.EntityKey}
	if err := e.act.submit(ctx, requestID, opMarkExpired, payload, "", reads); err != nil {
		// Retryable publish failure (core-operations degraded): NakWithDelay on
		// a bounded cadence, never a hot loop. The redelivery re-derives the
		// same deterministic requestId, which collapses on the Contract #4
		// tracker.
		e.logger.Error("weaver: fired-timer op publish failed; nak with delay",
			"targetId", targetID, "entityId", entityID, "requestId", requestID, "err", err)
		return substrate.NakWithDelay
	}
	e.issues.clear(issueKeyTimer(targetID))
	e.temporal.fired.Add(1)
	return substrate.Ack
}

// currentFreshUntil parses the freshUntil deadline off a weaver-targets row
// body, returning ok=false when the body is empty/unparseable or carries no
// parseable freshUntil column (the row is then treated as not-renewed and the
// firing proceeds — the conservative side for a level-driven expiry).
func currentFreshUntil(body []byte) (time.Time, bool) {
	if len(body) == 0 {
		return time.Time{}, false
	}
	var row map[string]any
	if err := json.Unmarshal(body, &row); err != nil {
		return time.Time{}, false
	}
	s, isString := row[freshUntilColumn].(string)
	if !isString {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func issueKeyTimer(key string) string { return "timer:" + key }
