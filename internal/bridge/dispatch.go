package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// eventBody is the minimal view of a core-events message the bridge reads: the
// Event envelope (Contract #3 §3.4) carries top-level fields plus a payload
// object, and the business fields live under payload (read-from-body discipline,
// never from the subject). The external.<adapter> event is emitted by the
// instanceOp's transactional outbox as an ordinary business event, so its body
// is {requestId, …, payload:{instanceKey, adapter, …}}.
type eventBody struct {
	Payload externalEvent `json:"payload"`
}

// externalEvent is the external.<adapter> envelope's payload (bridge.md). The
// bridge dispatches on Adapter and keys idempotency off InstanceKey; it treats
// InstanceKey/ExternalRef/IdempotencyKey as a single OPAQUE correlation token —
// it never parses a type segment and never assumes a vtx.<type>.<id> shape.
type externalEvent struct {
	// InstanceKey is the opaque correlation token (13.2 mints a bare handle). It
	// is the adapter's dedup key, the value echoed back as the result op's
	// externalRef, and the seed for the deterministic result-op requestId.
	InstanceKey string `json:"instanceKey"`
	// Adapter names the registered adapter to dispatch to.
	Adapter string `json:"adapter"`
	// Params are adapter call inputs (free-form JSON; the Fake* adapters ignore
	// them).
	Params json.RawMessage `json:"params"`
	// ReplyOp is the result-op type the bridge posts back on a terminal
	// (Resolved) outcome.
	ReplyOp string `json:"replyOp"`
	// DispatchOp is the op the bridge posts on a Pending outcome — it records the
	// pending marker (the vendor reference) and posts NO terminal outcome (the
	// token stays parked). Empty means the externalTask is sync-only: a Pending
	// adapter for it is a config error (handled like a missing adapter), never a
	// hot Nak loop.
	DispatchOp string `json:"dispatchOp"`
	// IdempotencyKey is = InstanceKey (the adapter's dedup key). When present it
	// is preferred; an empty value falls back to InstanceKey.
	IdempotencyKey string `json:"idempotencyKey"`
	// ExternalRef is = InstanceKey (echoed on the reply op). When present it is
	// preferred; an empty value falls back to InstanceKey.
	ExternalRef string `json:"externalRef"`
}

// idempotencyKey returns the load-bearing dedup key: idempotencyKey when set,
// else the instanceKey (one claim vertex = one external call; the fields are
// equal by construction, so either resolves the same opaque token).
func (ev externalEvent) idempotencyKey() string {
	if ev.IdempotencyKey != "" {
		return ev.IdempotencyKey
	}
	return ev.InstanceKey
}

// externalRefValue returns the token echoed back on the reply op: externalRef
// when set, else the instanceKey.
func (ev externalEvent) externalRefValue() string {
	if ev.ExternalRef != "" {
		return ev.ExternalRef
	}
	return ev.InstanceKey
}

// handleExternal processes one external.<adapter> event: parse → (optional)
// skip-on-redelivery → look up the adapter → dispatch → publish the result op →
// ack. Every outcome is an explicit ack Decision (the handler is idempotent —
// at-least-once delivery means the same event can arrive again).
//
//   - empty body → Ack (nothing to do).
//   - unparseable envelope OR missing adapter name / instanceKey → errConfig:
//     Ack + a Health issue (redelivery can never fix malformed/under-specified
//     input; never a silent skip).
//   - skip-probe present (not tombstoned) → the result already landed → Ack
//     without re-calling the adapter; a probe ERROR (not not-found) →
//     NakWithDelay + a Health issue (the probe is an optimization; a transient
//     KV failure falls back to the real call, never drops the event, and a
//     sustained Core KV outage stays observable, not log-only).
//   - adapter not registered → errConfig: Ack + a Health issue.
//   - adapter error (or a contained panic) → NakWithDelay + a Health issue
//     (bounded-cadence redelivery on the same idempotencyKey; the adapter
//     dedups, so a re-attempt is safe).
//   - adapter returns Pending → post the dispatchOp (record the pending marker;
//     the token stays parked, NO replyOp) → Ack. A Pending with no dispatchOp
//     configured is a config error (a sync-only externalTask got a Pending
//     adapter): Ack + a Health issue (never a hot Nak loop), mirroring the
//     unregistered-adapter handling.
//   - publish failure → NakWithDelay (the deterministic requestId makes the
//     re-publish idempotent — it collapses on the Contract #4 tracker).
//   - success (Resolved) → Ack (the ack is the commit point).
func (e *Engine) handleExternal(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}

	var body eventBody
	if err := json.Unmarshal(msg.Body, &body); err != nil {
		e.logger.Error("bridge: external event unparseable; ack + health issue", "err", err, "seq", msg.Sequence)
		e.issues.set("event:unparseable", severityError, codeEventUnparseable,
			"received an external event whose envelope could not be parsed")
		return substrate.Ack
	}
	ev := body.Payload

	instanceKey := ev.idempotencyKey()
	if instanceKey == "" || ev.Adapter == "" {
		e.logger.Error("bridge: external event missing adapter or instanceKey; ack + health issue",
			"adapter", ev.Adapter, "instanceKey", instanceKey, "seq", msg.Sequence)
		e.issues.set("event:malformed", severityError, codeEventUnparseable,
			"received an external event with no adapter name or no instanceKey")
		return substrate.Ack
	}

	// The deterministic result-op requestId: both the op id AND the skip-probe
	// key. Keyed on the OPAQUE token — the type segment, if any, is never parsed.
	replyReqID := deriveReplyRequestID(instanceKey)

	// (Optional) skip-on-redelivery (mechanism #3): GET the generic Contract #4 op
	// tracker for replyReqID. Present and not tombstoned → the result already
	// landed → skip the adapter call. This is a generic op-tracker read (the same
	// key shape for every op), NOT a read of the typed claim vertex.
	if *e.cfg.SkipOnRedelivery {
		landed, err := e.resultAlreadyLanded(ctx, replyReqID)
		if err != nil {
			e.logger.Warn("bridge: skip-probe failed; nak with delay + health issue (will retry, falling back to the real call)",
				"requestId", replyReqID, "instanceKey", instanceKey, "err", err)
			e.issues.set("skipProbe", severityWarning, codeSkipProbeFailed,
				fmt.Sprintf("skip-on-redelivery probe failed to read Core KV (transient; redelivering): %v", err))
			return substrate.NakWithDelay
		}
		// The probe reached Core KV: clear any prior transient skip-probe issue
		// (the outage resolved), symmetric with the adapter/publish legs.
		e.issues.clear("skipProbe")
		if landed {
			e.logger.Info("bridge: result already landed; ack without re-calling adapter",
				"requestId", replyReqID, "instanceKey", instanceKey, "adapter", ev.Adapter)
			e.metrics.incSkipped()
			return substrate.Ack
		}
	}

	adapter, ok := e.registry.Lookup(ev.Adapter)
	if !ok {
		e.logger.Error("bridge: no adapter registered; ack + health issue (errConfig)",
			"adapter", ev.Adapter, "instanceKey", instanceKey)
		e.issues.set("adapter:"+ev.Adapter, severityError, codeAdapterMissing,
			fmt.Sprintf("no adapter registered for %q (config error; redelivery cannot fix it)", ev.Adapter))
		return substrate.Ack
	}

	dispatch, execErr := executeAdapter(ctx, adapter, Request{
		IdempotencyKey: instanceKey,
		Operation:      ev.ReplyOp,
		Subject:        instanceKey,
		Params:         e.coerceParams(ev.Params),
		RawParams:      ev.Params,
	})
	if execErr != nil {
		e.logger.Error("bridge: adapter execute failed; nak with delay + health issue",
			"adapter", ev.Adapter, "instanceKey", instanceKey, "err", execErr)
		e.metrics.incAdapterErrors()
		e.issues.set("adapter:"+ev.Adapter, severityWarning, codeAdapterFailed,
			fmt.Sprintf("adapter %q failed (transient; redelivering on the same idempotencyKey): %v", ev.Adapter, execErr))
		return substrate.NakWithDelay
	}
	// A success (Resolved or Pending) clears any prior transient-failure / missing
	// issue for this adapter (the condition resolved).
	e.issues.clear("adapter:" + ev.Adapter)

	if dispatch.Disposition == Pending {
		return e.handlePending(ctx, ev, instanceKey, dispatch.Ref)
	}

	payload := map[string]any{
		"externalRef": ev.externalRefValue(),
		"status":      string(dispatch.Result.Status),
		"result":      dispatch.Result.Detail,
	}
	if err := e.act.submit(ctx, replyReqID, ev.ReplyOp, payload); err != nil {
		e.logger.Error("bridge: publish replyOp failed; nak with delay",
			"requestId", replyReqID, "instanceKey", instanceKey, "adapter", ev.Adapter, "err", err)
		e.issues.set("publish:"+ev.Adapter, severityWarning, codeReplyPublishFail,
			fmt.Sprintf("failed to publish replyOp for adapter %q (transient; redelivering): %v", ev.Adapter, err))
		return substrate.NakWithDelay
	}
	e.issues.clear("publish:" + ev.Adapter)
	e.metrics.incDispatched()

	e.logger.Info("bridge replyOp posted",
		"instanceKey", instanceKey, "adapter", ev.Adapter, "replyOp", ev.ReplyOp, "requestId", replyReqID)
	return substrate.Ack
}

// handlePending records the pending marker for an external call the adapter
// submitted but has not yet resolved (a Pending Dispatch), then arms the poll and
// timeout schedules that will drive its resolution. It posts the dispatchOp —
// payload {externalRef, vendorRef, adapter, replyOp, nextPollAt, deadline} — under
// a deterministic dispatch-op requestId (so a redelivered Pending event collapses
// on the Contract #4 tracker, exactly one create-only .dispatch marker), posts NO
// replyOp and writes NO .outcome (the Loom token stays parked), then arms
// schedule.bridge.poll.<handle> @ nextPollAt and schedule.bridge.timeout.<handle>
// @ deadline, and Acks. nextPollAt / deadline are computed from now and the
// configured horizons and supplied BOTH on the marker and as the schedule
// instants, so the marker and the schedules agree.
//
// A redelivered Pending re-posts the create-only dispatch op (it collapses on the
// tracker) and re-arms the schedules (one-schedule-per-subject REPLACE) — minor
// instant drift across a re-arm is acceptable — the timeout fires from its armed
// schedule. The handle (the bare claim token the schedules key on, echoed to
// the dispatchOp/replyOp that reconstruct the typed claim vertex — the bridge never
// does) is ev.externalRefValue(); a dotted/wildcard value is a config error (the
// same posture as a missing dispatchOp — redelivery cannot fix it).
//
// A Pending outcome for an externalTask with no dispatchOp configured is a config
// error: a sync-only task was wired to an adapter that went Pending, and there is
// nowhere to record the marker. It is handled exactly like an unregistered
// adapter — Ack + a Health issue, never a hot Nak loop (redelivery cannot fix a
// missing dispatchOp). A publish or schedule-arm failure NakWithDelays (the
// deterministic requestId + REPLACE arm make the re-drive idempotent).
func (e *Engine) handlePending(ctx context.Context, ev externalEvent, instanceKey, vendorRef string) substrate.Decision {
	if ev.DispatchOp == "" {
		e.logger.Error("bridge: adapter returned Pending but the externalTask has no dispatchOp; ack + health issue (errConfig)",
			"adapter", ev.Adapter, "instanceKey", instanceKey)
		e.issues.set("dispatch:"+ev.Adapter, severityError, codeDispatchOpMissing,
			fmt.Sprintf("adapter %q returned Pending but no dispatchOp is configured for the event (config error; redelivery cannot fix it)", ev.Adapter))
		return substrate.Ack
	}

	handle := ev.externalRefValue()
	if !isBareHandle(handle) {
		e.logger.Error("bridge: Pending externalRef is not a bare handle; ack + health issue (errConfig)",
			"adapter", ev.Adapter, "instanceKey", instanceKey, "externalRef", handle)
		e.issues.set("dispatch:"+ev.Adapter, severityError, codeDispatchOpMissing,
			fmt.Sprintf("Pending externalRef %q carries dots/wildcards/whitespace; cannot key the poll/timeout schedules (config error; redelivery cannot fix it)", handle))
		return substrate.Ack
	}

	// Compute the schedule instants once, truncated to whole seconds so the marker
	// strings are byte-identical to the @at schedule headers (scheduleAt also
	// truncates). The marker carries them as RFC3339; the schedules fire at them.
	now := time.Now().UTC().Truncate(time.Second)
	nextPollAt := now.Add(e.cfg.PollInterval)
	deadline := now.Add(e.cfg.CallDeadline)

	dispatchReqID := deriveDispatchRequestID(instanceKey)
	payload := map[string]any{
		"externalRef": handle,
		"vendorRef":   vendorRef,
		"adapter":     ev.Adapter,
		"replyOp":     ev.ReplyOp,
		"nextPollAt":  nextPollAt.Format(time.RFC3339),
		"deadline":    deadline.Format(time.RFC3339),
	}
	if err := e.act.submit(ctx, dispatchReqID, ev.DispatchOp, payload); err != nil {
		e.logger.Error("bridge: publish dispatchOp failed; nak with delay",
			"requestId", dispatchReqID, "instanceKey", instanceKey, "adapter", ev.Adapter, "err", err)
		e.issues.set("publish:"+ev.Adapter, severityWarning, codeReplyPublishFail,
			fmt.Sprintf("failed to publish dispatchOp for adapter %q (transient; redelivering): %v", ev.Adapter, err))
		return substrate.NakWithDelay
	}
	e.issues.clear("publish:" + ev.Adapter)
	e.issues.clear("dispatch:" + ev.Adapter)
	e.metrics.incPending()

	if err := e.armSchedules(ctx, handle, vendorRef, ev.Adapter, ev.ReplyOp, nextPollAt, deadline); err != nil {
		// A schedule-arm publish failure is retryable (core-schedules degraded):
		// NakWithDelay. The redelivery re-posts the create-only dispatch op (it
		// collapses) and re-arms by REPLACE — no duplicate marker, no duplicate
		// schedule.
		e.logger.Error("bridge: arm poll/timeout schedules failed; nak with delay",
			"handle", handle, "adapter", ev.Adapter, "err", err)
		e.issues.set("schedule:arm", severityWarning, codeSchedulePublishFail,
			fmt.Sprintf("arming the poll/timeout schedules failed (transient; redelivering): %v", err))
		return substrate.NakWithDelay
	}
	e.issues.clear("schedule:arm")

	e.logger.Info("bridge dispatchOp posted (pending); schedules armed",
		"instanceKey", instanceKey, "adapter", ev.Adapter, "dispatchOp", ev.DispatchOp, "vendorRef", vendorRef, "requestId", dispatchReqID)
	return substrate.Ack
}

// isBareHandle reports whether s is a non-empty token with no dots / wildcards /
// whitespace — the discipline the schedule subjects require (and the
// dispatchOp/replyOp's claim-vertex reconstruction; it mirrors the dispatchOp DDL's
// required_bare_handle).
func isBareHandle(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, ".*> \t\n")
}

// resultAlreadyLanded reports whether the result op for replyReqID has ALREADY
// committed in Core KV — a generic Contract #4 op-tracker GET (vtx.op.<reqId>),
// the same key shape for every op (never a typed claim-vertex read; the bridge
// stays type-agnostic). The landed test mirrors Contract #4's dedup rule
// exactly: "found AND isDeleted:false". Core KV holds logically-deleted entries
// by design (§4.3 reserves isDeleted:true as an operator-driven retry signal —
// "treat as not-found and proceed"), so a present-but-tombstoned tracker is NOT
// a landed result: skipping on it would silently abandon a genuinely-incomplete
// call. ErrKeyNotFound or an unparseable/tombstoned envelope ⇒ not landed (the
// dispatch proceeds; the adapter dedups the reused idempotencyKey).
func (e *Engine) resultAlreadyLanded(ctx context.Context, replyReqID string) (bool, error) {
	entry, err := e.conn.KVGet(ctx, e.cfg.CoreKVBucket, "vtx.op."+replyReqID)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("bridge: probe result tracker %q: %w", replyReqID, err)
	}
	var env substrate.DocumentEnvelope
	if uerr := json.Unmarshal(entry.Value, &env); uerr != nil {
		// An unparseable tracker is not trustworthy landed evidence; treat as
		// not-landed and dispatch (the adapter dedups the reused idempotencyKey).
		e.logger.Warn("bridge: result tracker unparseable; treating as not landed",
			"requestId", replyReqID, "err", uerr)
		return false, nil
	}
	if env.IsDeleted {
		// Contract #4 §4.3: a tombstoned tracker is the operator-driven retry
		// signal — treat as not-found, not landed.
		return false, nil
	}
	return true, nil
}

// executeAdapter calls the adapter under panic containment. The bridge is the
// safety boundary, not the adapter: a panic inside Execute is recovered and
// returned as an ordinary error, so the event is re-driven (NakWithDelay) on the
// same idempotencyKey instead of crashing the dispatch goroutine.
func executeAdapter(ctx context.Context, adapter Adapter, req Request) (dispatch Dispatch, err error) {
	defer func() {
		if r := recover(); r != nil {
			dispatch = Dispatch{}
			err = fmt.Errorf("bridge: adapter panicked during execute: %v", r)
		}
	}()
	return adapter.Execute(ctx, req)
}

// coerceParams maps the envelope's free-form params JSON onto the adapter
// Request's Params (map[string]string). The Request carries params as a flat
// string map: the reference Fake* adapters read only IdempotencyKey and Subject,
// so a non-object or non-string-valued params blob is ignored (nil) and only
// string-valued entries are carried through. A richer adapter that needs
// structured params reads them from a structured Request field instead.
func (e *Engine) coerceParams(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil || len(generic) == 0 {
		return nil
	}
	out := make(map[string]string, len(generic))
	for k, v := range generic {
		if s, ok := v.(string); ok {
			out[k] = s
			continue
		}
		// A non-string param value is dropped (the flat string map carries only
		// string entries). Name the dropped key at debug level — a signal for a
		// future richer-param adapter, not a runtime concern.
		e.logger.Debug("bridge: dropping non-string param", "key", k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
