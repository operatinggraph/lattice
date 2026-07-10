package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Poll/timeout lane subjects (Contract #10 §10.4). A pending external call arms
// two @at schedules keyed on the bare claim handle (a NanoID, dot-free — never a
// dotted vertex key):
//
//	schedule.bridge.poll.<handle>      @ nextPollAt → fired schedule.bridge.poll.fired.<handle>
//	schedule.bridge.timeout.<handle>   @ deadline   → fired schedule.bridge.timeout.fired.<handle>
//
// The fired consumer filters schedule.bridge.*.fired.> so it catches both kinds;
// the kind segment (poll|timeout) selects the handler branch. The republish target
// is a fired-namespaced sibling within schedule.> (the server requires the target
// to stay in the scheduling stream's own subject space).
const (
	schedulePrefix       = "schedule.bridge."
	scheduleKindPoll     = "poll"
	scheduleKindTimeout  = "timeout"
	firedToken           = "fired"
	firedFilterSubject   = "schedule.bridge.*.fired.>"
	scheduleConsumerName = "bridge-schedule"
)

// schedulePayload is the schedule-message body: published when the poll/timeout
// schedules are armed and delivered verbatim at the fired subject. It carries the
// routing the fired handler acts on — which adapter to poll, the vendor reference,
// and the replyOp to post on resolution — so the handler stays TYPE-AGNOSTIC: it
// never synthesizes or reads a typed claim-vertex key (the bridge does not know the
// claim type; only the package's dispatchOp/replyOp do). The routing is safe to
// carry verbatim: the handle binds one external call to one immutable vendorRef
// (the dispatch marker is create-only), so a redelivered or re-armed firing cannot
// carry a stale ref. fireAt carries the fire instant so a re-armed schedule (a new
// fireAt) is a genuinely new firing.
type schedulePayload struct {
	Handle    string `json:"handle"`
	VendorRef string `json:"vendorRef"`
	Adapter   string `json:"adapter"`
	ReplyOp   string `json:"replyOp"`
	FireAt    string `json:"fireAt"`
}

// pollSubject / timeoutSubject build the armed-schedule subjects for handle.
func pollSubject(handle string) string    { return schedulePrefix + scheduleKindPoll + "." + handle }
func timeoutSubject(handle string) string { return schedulePrefix + scheduleKindTimeout + "." + handle }

// pollFiredTarget / timeoutFiredTarget build the republish targets (within
// schedule.>) the armed schedules fire to.
func pollFiredTarget(handle string) string {
	return schedulePrefix + scheduleKindPoll + "." + firedToken + "." + handle
}
func timeoutFiredTarget(handle string) string {
	return schedulePrefix + scheduleKindTimeout + "." + firedToken + "." + handle
}

// scheduleSpec describes the poll/timeout fired consumer: a fixed durable on the
// core-schedules stream filtered to the bridge's fired subject space. Like Weaver's
// lane-3 it is a FIXED durable (not per-handle): its ack floor IS the
// missed-while-down recovery — a fired message that arrived while the bridge was
// down replays from the floor on restart, and the handler's read-before-act guard
// makes a stale replay harmless.
func (e *Engine) scheduleSpec() substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:          scheduleConsumerName,
		Stream:        e.cfg.CoreSchedulesStream,
		FilterSubject: firedFilterSubject,
		DeliverPolicy: substrate.DeliverAll,
		Handler:       supervisedHandler(e.handleFiredSchedule),
		Health:        e.healthSinkFor(scheduleConsumerName),
		Logger:        e.logger,
	}
}

// armSchedules arms the poll and timeout @at schedules for a freshly-recorded
// pending call. The routing (vendorRef / adapter / replyOp) rides each schedule
// payload so the fired handler needs no typed claim read. nextPollAt / deadline are
// the instants written to the .dispatch marker, so the marker and the schedules
// agree (a redelivered Pending re-arms by REPLACE — minor instant drift across a
// re-arm is acceptable — the timeout fires from its own armed schedule). Returns the first
// publish error (the caller NakWithDelays so the redelivery re-arms).
func (e *Engine) armSchedules(ctx context.Context, handle, vendorRef, adapter, replyOp string, nextPollAt, deadline time.Time) error {
	pollBody, err := json.Marshal(schedulePayload{
		Handle: handle, VendorRef: vendorRef, Adapter: adapter, ReplyOp: replyOp,
		FireAt: nextPollAt.UTC().Truncate(time.Second).Format(time.RFC3339)})
	if err != nil {
		return fmt.Errorf("bridge: marshal poll schedule payload: %w", err)
	}
	if err := e.act.scheduleAt(ctx, pollSubject(handle), pollFiredTarget(handle), nextPollAt, pollBody); err != nil {
		return err
	}
	timeoutBody, err := json.Marshal(schedulePayload{
		Handle: handle, VendorRef: vendorRef, Adapter: adapter, ReplyOp: replyOp,
		FireAt: deadline.UTC().Truncate(time.Second).Format(time.RFC3339)})
	if err != nil {
		return fmt.Errorf("bridge: marshal timeout schedule payload: %w", err)
	}
	if err := e.act.scheduleAt(ctx, timeoutSubject(handle), timeoutFiredTarget(handle), deadline, timeoutBody); err != nil {
		return err
	}
	e.logger.Info("bridge schedules armed (pending)",
		"handle", handle, "nextPollAt", nextPollAt.UTC().Format(time.RFC3339), "deadline", deadline.UTC().Format(time.RFC3339))
	return nil
}

// handleFiredSchedule is the poll/timeout lane handler: one delivered message = one
// fired schedule republished by the NATS scheduler at
// schedule.bridge.<kind>.fired.<handle>. It reads the routing from the schedule
// payload and either polls the vendor (kind=poll) or posts a terminal failed reply
// (kind=timeout). The bridge stays TYPE-AGNOSTIC — it never synthesizes or reads a
// typed claim-vertex key; resolution is judged by the reply op-tracker (the generic
// Contract #4 vtx.op.<requestId> key) and the routing rides the payload.
//
// Read-before-act (the staleness guard, mirroring Weaver's handleFiredTimer): the
// reply op-tracker for this handle already committed → Ack (a prior poll, a webhook,
// or the timeout already posted the replyOp; the create-only .outcome is the final
// backstop, so a late firing never double-resolves).
//
// Idempotency: a poll that resolves posts the replyOp under the SAME deterministic
// deriveReplyRequestID(handle) the synchronous resolve path uses, so an
// at-least-once redelivery collapses on the Contract #4 tracker and the create-only
// .outcome. A timeout posts the same replyOp id (status failed) — the read-before-
// act suppresses it once any resolution landed, and the create-only .outcome decides
// first-writer-wins for a timeout racing a late success.
func (e *Engine) handleFiredSchedule(ctx context.Context, msg substrate.Message) substrate.Decision {
	kind, handle, ok := parseFiredSubject(msg.Subject)
	if !ok {
		// A malformed fired subject is unrecoverable on redelivery; drop loudly.
		e.logger.Error("bridge: fired-schedule subject not schedule.bridge.<kind>.fired.<handle>; ack",
			"subject", msg.Subject)
		e.issues.set("schedule:subject", severityWarning, codeScheduleSubject,
			"fired-schedule subject "+msg.Subject+" is not schedule.bridge.<kind>.fired.<handle>; dropped")
		return substrate.Ack
	}
	e.issues.clear("schedule:subject")

	// Read-before-act: the call already resolved → suppress (a prior poll, a webhook,
	// or the timeout already committed the replyOp). The reply tracker is the generic
	// Contract #4 op key — no typed claim read.
	landed, err := e.resultAlreadyLanded(ctx, deriveReplyRequestID(handle))
	if err != nil {
		e.logger.Warn("bridge: fired-schedule resolution probe failed; nak with delay",
			"kind", kind, "handle", handle, "err", err)
		e.issues.set("schedule:read", severityWarning, codeScheduleReadFailed,
			fmt.Sprintf("fired-schedule resolution probe failed (transient; redelivering): %v", err))
		return substrate.NakWithDelay
	}
	e.issues.clear("schedule:read")
	if landed {
		e.logger.Info("bridge: fired schedule for an already-resolved call; ack",
			"kind", kind, "handle", handle)
		return substrate.Ack
	}

	var payload schedulePayload
	if uerr := json.Unmarshal(msg.Body, &payload); uerr != nil {
		// A malformed payload is unrecoverable on redelivery; drop loudly.
		e.logger.Error("bridge: fired-schedule payload unparseable; ack",
			"kind", kind, "handle", handle, "err", uerr)
		e.issues.set("schedule:payload", severityWarning, codeScheduleSubject,
			fmt.Sprintf("fired-schedule payload for %q is unparseable; dropped: %v", handle, uerr))
		return substrate.Ack
	}
	e.issues.clear("schedule:payload")

	switch kind {
	case scheduleKindPoll:
		return e.handleFiredPoll(ctx, handle, payload)
	case scheduleKindTimeout:
		return e.handleFiredTimeout(ctx, handle, payload)
	default:
		// parseFiredSubject only yields poll|timeout, so this is unreachable; drop
		// loudly rather than silently if a future subject shape slips through.
		e.logger.Error("bridge: fired-schedule unknown kind; ack", "kind", kind, "handle", handle)
		return substrate.Ack
	}
}

// handleFiredPoll polls the vendor for a still-pending call and resolves, re-arms,
// or retries:
//   - Resolved → post the replyOp (status / result verbatim, the synchronous
//     resolve shape) under deriveReplyRequestID(handle) → Ack.
//   - Pending → re-arm schedule.bridge.poll.<handle> @ now+PollInterval (the
//     self-rescheduling @at chain, carrying the same routing) → Ack.
//   - error → NakWithDelay (a transient probe failure; redelivery re-polls).
func (e *Engine) handleFiredPoll(ctx context.Context, handle string, payload schedulePayload) substrate.Decision {
	adapter, ok := e.registry.Lookup(payload.Adapter)
	if !ok {
		// The payload names an adapter not registered in this process (a config
		// drift): redelivery cannot fix it. Ack + a Health issue, never a hot Nak
		// loop — mirrors the event-path unregistered-adapter handling. The timeout
		// schedule remains armed as the backstop.
		e.logger.Error("bridge: fired poll for an unregistered adapter; ack + health issue",
			"adapter", payload.Adapter, "handle", handle)
		e.issues.set("adapter:"+payload.Adapter, severityError, codeAdapterMissing,
			fmt.Sprintf("fired poll names unregistered adapter %q (config error; redelivery cannot fix it)", payload.Adapter))
		return substrate.Ack
	}

	dispatch, err := pollAdapter(ctx, adapter, payload.VendorRef)
	if err != nil {
		e.logger.Error("bridge: adapter poll failed; nak with delay + health issue",
			"adapter", payload.Adapter, "handle", handle, "vendorRef", payload.VendorRef, "err", err)
		e.metrics.incAdapterErrors()
		e.issues.set("poll:"+payload.Adapter, severityWarning, codePollFailed,
			fmt.Sprintf("adapter %q poll failed (transient; redelivering): %v", payload.Adapter, err))
		return substrate.NakWithDelay
	}
	e.issues.clear("poll:" + payload.Adapter)

	if dispatch.Disposition == Pending {
		// Still in flight: re-arm the next poll (the @at chain), carrying the same
		// routing. Only the poll schedule re-arms; the timeout schedule stays armed
		// at its original deadline.
		nextPollAt := time.Now().Add(e.cfg.PollInterval)
		body, mErr := json.Marshal(schedulePayload{
			Handle: handle, VendorRef: payload.VendorRef, Adapter: payload.Adapter, ReplyOp: payload.ReplyOp,
			FireAt: nextPollAt.UTC().Truncate(time.Second).Format(time.RFC3339)})
		if mErr != nil {
			e.logger.Error("bridge: marshal re-arm poll payload failed; nak with delay", "handle", handle, "err", mErr)
			return substrate.NakWithDelay
		}
		if aErr := e.act.scheduleAt(ctx, pollSubject(handle), pollFiredTarget(handle), nextPollAt, body); aErr != nil {
			e.logger.Error("bridge: re-arm poll schedule failed; nak with delay", "handle", handle, "err", aErr)
			e.issues.set("schedule:arm", severityWarning, codeSchedulePublishFail,
				fmt.Sprintf("re-arming the poll schedule failed (transient; redelivering): %v", aErr))
			return substrate.NakWithDelay
		}
		e.issues.clear("schedule:arm")
		e.logger.Info("bridge poll still pending; re-armed", "handle", handle, "nextPollAt", nextPollAt.UTC().Format(time.RFC3339))
		return substrate.Ack
	}

	// Resolved: post the replyOp exactly as the synchronous resolve path does — the
	// create-only .outcome + deterministic reply id make it idempotent.
	replyReqID := deriveReplyRequestID(handle)
	replyPayload := map[string]any{
		"externalRef": handle,
		"status":      string(dispatch.Result.Status),
		"result":      dispatch.Result.Detail,
	}
	if err := e.act.submit(ctx, replyReqID, payload.ReplyOp, replyPayload, replyOpReads(payload.ReplyOp, handle)); err != nil {
		e.logger.Error("bridge: publish poll-resolved replyOp failed; nak with delay",
			"requestId", replyReqID, "handle", handle, "adapter", payload.Adapter, "err", err)
		e.issues.set("publish:"+payload.Adapter, severityWarning, codeReplyPublishFail,
			fmt.Sprintf("failed to publish poll-resolved replyOp for adapter %q (transient; redelivering): %v", payload.Adapter, err))
		return substrate.NakWithDelay
	}
	e.issues.clear("publish:" + payload.Adapter)
	e.metrics.incDispatched()
	e.logger.Info("bridge poll resolved; replyOp posted",
		"handle", handle, "adapter", payload.Adapter, "replyOp", payload.ReplyOp, "status", string(dispatch.Result.Status), "requestId", replyReqID)
	return substrate.Ack
}

// handleFiredTimeout posts the terminal failed reply for a call that did not resolve
// before its deadline. It reuses the existing failed verdict (a distinct timedOut
// value is a later refinement). The read-before-act in handleFiredSchedule already
// suppressed this for any call that resolved, and the create-only .outcome is the
// final first-writer-wins backstop for a timeout racing a late success.
func (e *Engine) handleFiredTimeout(ctx context.Context, handle string, payload schedulePayload) substrate.Decision {
	replyReqID := deriveReplyRequestID(handle)
	replyPayload := map[string]any{
		"externalRef": handle,
		"status":      string(OutcomeFailed),
		"result":      "external call did not resolve before deadline",
	}
	if err := e.act.submit(ctx, replyReqID, payload.ReplyOp, replyPayload, replyOpReads(payload.ReplyOp, handle)); err != nil {
		e.logger.Error("bridge: publish timeout replyOp failed; nak with delay",
			"requestId", replyReqID, "handle", handle, "err", err)
		e.issues.set("publish:"+payload.Adapter, severityWarning, codeReplyPublishFail,
			fmt.Sprintf("failed to publish timeout replyOp for adapter %q (transient; redelivering): %v", payload.Adapter, err))
		return substrate.NakWithDelay
	}
	e.issues.clear("publish:" + payload.Adapter)
	e.issues.clear("poll:" + payload.Adapter)
	e.metrics.incTimedOut()
	e.logger.Info("bridge call timed out; failed replyOp posted",
		"handle", handle, "adapter", payload.Adapter, "replyOp", payload.ReplyOp, "requestId", replyReqID)
	return substrate.Ack
}

// parseFiredSubject splits a fired-schedule subject
// schedule.bridge.<kind>.fired.<handle> into (kind, handle). ok=false for any
// subject not in that exact shape, or a handle carrying further dots (the handle is
// a single dot-free NanoID token). kind must be poll or timeout.
func parseFiredSubject(subject string) (kind, handle string, ok bool) {
	rest, found := strings.CutPrefix(subject, schedulePrefix)
	if !found {
		return "", "", false
	}
	// rest is <kind>.fired.<handle> — exactly three dot-free segments.
	parts := strings.Split(rest, ".")
	if len(parts) != 3 || parts[1] != firedToken {
		return "", "", false
	}
	kind, handle = parts[0], parts[2]
	if kind != scheduleKindPoll && kind != scheduleKindTimeout {
		return "", "", false
	}
	if handle == "" {
		return "", "", false
	}
	return kind, handle, true
}

// pollAdapter calls the adapter's Poll under panic containment — the bridge is the
// safety boundary, not the adapter: a panic inside Poll is recovered and returned as
// an ordinary error so the firing is re-driven (NakWithDelay) instead of crashing
// the consumer goroutine. Mirrors executeAdapter for the Execute path.
func pollAdapter(ctx context.Context, adapter Adapter, ref string) (dispatch Dispatch, err error) {
	defer func() {
		if r := recover(); r != nil {
			dispatch = Dispatch{}
			err = fmt.Errorf("bridge: adapter panicked during poll: %v", r)
		}
	}()
	return adapter.Poll(ctx, ref)
}
