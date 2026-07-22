package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/edge/overlay"
	edgevault "github.com/operatinggraph/lattice/internal/edge/vault"
	"github.com/operatinggraph/lattice/internal/processor"
)

// frame is one SSE event pushed to every connected browser tab. kind selects
// the SSE `event:` name the browser's EventSource listens on
// (facet-app-ux.md §4's "the SSE stream is the only data path"):
//
//   - "manifest" — a manifest.* row changed (or, on (re)connect, the initial
//     snapshot replayed as a burst of these). Carries the overlay-merged
//     value (Pending included) so a locally-queued write shows immediately.
//   - "outbox"   — one Outbox entry's lifecycle state changed
//     (queued/submitting/confirmed/rejected, §3.4a/b).
//   - "ready"    — the cold hydration catch-up finished (§3.0's "Hydrating"
//     → "Home" transition signal).
//   - "revoked"  — the Gateway refused this identity's credential outright
//     (agent.ErrCredentialRejected: revoked via the kill-switch, or
//     expired). Drives §4.4's sign-out flow. The browser cannot observe this
//     itself: its writes are async (POST /api/enqueue returns long before
//     the drain loop ever contacts the Gateway), so the failure surfaces
//     only here, on the host's drain loop.
//   - "connectivity" — the engine's host↔NATS connection went up or down
//     (design §4.4/§3.0's "Reconnecting…" banner). Deliberately NOT the
//     browser↔host EventSource's own open/error events: those reflect the
//     SSE transport this same process serves, not whether its NATS
//     connection is actually alive — a stale EventSource state would keep
//     the banner showing "Reconnecting…" after NATS came back, or hide it
//     while NATS is genuinely down but the SSE tab never blinked.
//     The same frame carries syncDegraded — the sync manager is in its
//     restart-backoff loop (runSyncLoop) — a distinct axis from connected:
//     the NATS socket can be perfectly healthy while every sync attempt
//     wedges fail-closed (e.g. a controlauth denial on personal.syncgap),
//     which without this bit renders as a healthy-looking stale world.
type frame struct {
	Kind         string          `json:"-"`
	Key          string          `json:"key,omitempty"`
	Deleted      bool            `json:"deleted,omitempty"`
	Pending      bool            `json:"pending,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
	Revision     uint64          `json:"revision,omitempty"`
	Outbox       *outboxEntry    `json:"outbox,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	Connected    bool            `json:"connected"`
	SyncDegraded bool            `json:"syncDegraded,omitempty"`
}

// outboxEntry mirrors facet-app-ux.md §3.4a: one enqueued write and its
// lifecycle. Payload/Reads/OptionalReads/AuthContext are retained so the
// browser can rebuild the exact envelope, or reopen the descriptor form
// pre-filled for Review (§3.4b) without re-deriving anything server-side.
type outboxEntry struct {
	RequestID     string                 `json:"requestId"`
	OperationType string                 `json:"operationType"`
	Payload       json.RawMessage        `json:"payload"`
	Reads         []string               `json:"reads,omitempty"`
	OptionalReads []string               `json:"optionalReads,omitempty"`
	AuthContext   *processor.AuthContext `json:"authContext,omitempty"`
	State         string                 `json:"state"` // queued|submitting|confirmed|rejected
	ErrorCode     string                 `json:"errorCode,omitempty"`
	ErrorMessage  string                 `json:"errorMessage,omitempty"`
	CreatedAt     time.Time              `json:"createdAt"`
}

// maxOutboxEntries bounds the in-memory outbox map (never persisted across
// restart — a fresh process just starts empty, same as the local mirror
// would on a fresh EDGE_STORE_PATH). A long-running dev host trims the
// oldest terminal (confirmed/rejected) entries past this cap rather than
// growing unbounded.
const maxOutboxEntries = 200

// feed is the SSE broadcaster + the Outbox's server-side state. One per
// process; safe for concurrent use from the sync Manager's callback
// goroutine, the agent's drain loop, and every connected SSE handler.
type feed struct {
	mu      sync.Mutex
	subs    map[chan frame]struct{}
	outbox  map[string]*outboxEntry
	outboxQ []string // insertion order, for trimming
	// revokedReason is sticky once set: an engine's credential is minted once
	// (engineManager.Acquire) and never refreshed, so a dead one stays dead
	// for the engine's whole lifetime — and a browser that connects or
	// reloads AFTER the drain loop discovered the revocation must still be
	// told, hence writeSSE replays it. What ENDS the state is sign-out:
	// engineManager.Purge evicts the engine outright, so the next login
	// builds a fresh one, with a fresh credential and a fresh feed. Without
	// that eviction this stickiness would be a permanent lockout.
	revokedReason string
	// connected is the sticky host↔NATS connectivity state (see the frame
	// doc comment's "connectivity" kind) — a fresh SSE connection replays it
	// immediately (writeSSE) instead of leaving the banner in its default
	// hidden state until the next transition, which would show "connected"
	// even mid-outage for a tab that opened during one.
	connected bool
	// syncDegraded is the sticky sync-manager-in-restart-backoff state (the
	// frame doc comment's syncDegraded axis): set by runSyncLoop when a Run
	// exits with an error, cleared via the manager's OnRunEstablished once a
	// retry gets past its freshness gate. Replayed to fresh SSE connections
	// for the same reason connected is.
	syncDegraded bool
	// selfName decrypts this identity's sealed name into the manifest.me
	// row's displayName on its way to the browser. nil (no Vault control
	// plane wired) passes every row through untouched.
	selfName *edgevault.SelfName
}

func newFeed(selfName *edgevault.SelfName) *feed {
	return &feed{
		subs:      make(map[chan frame]struct{}),
		outbox:    make(map[string]*outboxEntry),
		connected: true, // newEngine only calls newFeed after NATS dial succeeds
		selfName:  selfName,
	}
}

func (f *feed) subscribe() chan frame {
	ch := make(chan frame, 32)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch
}

func (f *feed) unsubscribe(ch chan frame) {
	f.mu.Lock()
	delete(f.subs, ch)
	f.mu.Unlock()
}

func (f *feed) publish(fr frame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		select {
		case ch <- fr:
		default:
			// A slow/stuck client drops frames rather than blocking every
			// other subscriber or the caller (the sync Manager's own
			// delivery goroutine) — its next reconnect gets a fresh
			// snapshot anyway (facet-app-ux.md §4).
		}
	}
}

// publishManifestKey re-reads key through the overlay (merging any pending
// optimistic value) and publishes its current state — used both for a live
// OnChange delta and for the initial snapshot replay, so the two code paths
// can't drift.
func (f *feed) publishManifestKey(ov *overlay.Overlay, key string, deleted bool) {
	v, ok, err := ov.Read(key)
	if err != nil || !ok {
		f.publish(frame{Kind: "manifest", Key: key, Deleted: true})
		return
	}
	f.publish(f.manifestFrame(key, v))
}

// manifestFrame builds the browser-facing frame for one manifest row. Both
// paths that put a manifest row on the wire — the live delta above and
// server.go's snapshot replay — go through here, so the decoration they
// apply cannot drift.
func (f *feed) manifestFrame(key string, v overlay.Value) frame {
	return frame{
		Kind:    "manifest",
		Key:     key,
		Deleted: v.Deleted,
		Pending: v.Pending,
		Data:    f.selfName.Decorate(context.Background(), key, v.Data),
	}
}

func (f *feed) publishReady(revision uint64) {
	f.publish(frame{Kind: "ready", Revision: revision})
}

// publishRevoked records + broadcasts that the Gateway refused this
// identity's credential (§4.4's sign-out flow). Idempotent: the drain loop
// re-discovers the same rejection on every tick while intents remain queued,
// and only the first is worth publishing.
func (f *feed) publishRevoked(reason string) {
	f.mu.Lock()
	already := f.revokedReason != ""
	if !already {
		f.revokedReason = reason
	}
	f.mu.Unlock()
	if already {
		return
	}
	f.publish(frame{Kind: "revoked", Reason: reason})
}

// revoked returns the sticky revocation reason, if any.
func (f *feed) revoked() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revokedReason, f.revokedReason != ""
}

// setConnected records the engine's host↔NATS connectivity and broadcasts
// the transition — a no-op when the state didn't actually change (nats.go's
// disconnect/reconnect handlers can each fire more than once per outage).
func (f *feed) setConnected(connected bool) {
	f.mu.Lock()
	changed := f.connected != connected
	f.connected = connected
	degraded := f.syncDegraded
	f.mu.Unlock()
	if changed {
		f.publish(frame{Kind: "connectivity", Connected: connected, SyncDegraded: degraded})
	}
}

// setSyncDegraded records whether the sync manager is in its restart-backoff
// loop and broadcasts the transition on the same "connectivity" frame kind —
// a no-op when the state didn't change (every failed Run re-marks it, every
// established Run re-clears it).
func (f *feed) setSyncDegraded(degraded bool) {
	f.mu.Lock()
	changed := f.syncDegraded != degraded
	f.syncDegraded = degraded
	connected := f.connected
	f.mu.Unlock()
	if changed {
		f.publish(frame{Kind: "connectivity", Connected: connected, SyncDegraded: degraded})
	}
}

// connectivityState returns the current sticky connectivity pair (for a
// fresh SSE connection's initial replay — see writeSSE).
func (f *feed) connectivityState() (connected, syncDegraded bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected, f.syncDegraded
}

// enqueueOutbox records a freshly-queued write and publishes its initial
// "queued" frame.
func (f *feed) enqueueOutbox(e *outboxEntry) {
	f.mu.Lock()
	f.outbox[e.RequestID] = e
	f.outboxQ = append(f.outboxQ, e.RequestID)
	f.trimOutboxLocked()
	f.mu.Unlock()
	f.publish(frame{Kind: "outbox", Outbox: e})
}

// setOutboxState updates a tracked entry's lifecycle state and publishes
// the change. A requestId with no tracked entry (e.g. a stale queued intent
// from a prior process lifetime, drained after a restart) is a silent
// no-op — there is no browser tab to tell anyway.
func (f *feed) setOutboxState(requestID, state, errCode, errMsg string) {
	f.mu.Lock()
	e, ok := f.outbox[requestID]
	if ok {
		e.State = state
		e.ErrorCode = errCode
		e.ErrorMessage = errMsg
	}
	f.mu.Unlock()
	if ok {
		f.publish(frame{Kind: "outbox", Outbox: e})
	}
}

// snapshotOutbox returns every tracked entry (a page refresh's "what's
// still in flight" replay, §3.0/§3.4).
func (f *feed) snapshotOutbox() []*outboxEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*outboxEntry, 0, len(f.outbox))
	for _, id := range f.outboxQ {
		if e, ok := f.outbox[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

// trimOutboxLocked drops the oldest terminal (confirmed/rejected) entries
// once the map exceeds maxOutboxEntries. Caller holds f.mu.
func (f *feed) trimOutboxLocked() {
	for len(f.outboxQ) > maxOutboxEntries {
		id := f.outboxQ[0]
		e, ok := f.outbox[id]
		if ok && (e.State == "confirmed" || e.State == "rejected") {
			delete(f.outbox, id)
			f.outboxQ = f.outboxQ[1:]
			continue
		}
		// Oldest entry is still in flight — stop trimming rather than
		// dropping a queued/submitting entry the UI still needs to see.
		break
	}
}

// writeSSE serves GET /api/feed: replays a full manifest snapshot + the
// current outbox, then streams live frames until the client disconnects.
func writeSSE(w http.ResponseWriter, r *http.Request, logger *slog.Logger, fd *feed, snapshot func() []frame) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch := fd.subscribe()
	defer fd.unsubscribe(ch)

	connected, syncDegraded := fd.connectivityState()
	writeFrame(w, frame{Kind: "connectivity", Connected: connected, SyncDegraded: syncDegraded})
	for _, fr := range snapshot() {
		writeFrame(w, fr)
	}
	for _, e := range fd.snapshotOutbox() {
		writeFrame(w, frame{Kind: "outbox", Outbox: e})
	}
	if reason, ok := fd.revoked(); ok {
		writeFrame(w, frame{Kind: "revoked", Reason: reason})
	}
	fl.Flush()

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case fr := <-ch:
			writeFrame(w, fr)
			fl.Flush()
		case <-ping.C:
			// SSE comment line — keeps intermediary proxies/browsers from
			// timing out an idle connection; ignored by EventSource.
			_, _ = w.Write([]byte(": ping\n\n"))
			fl.Flush()
		}
	}
}

func writeFrame(w http.ResponseWriter, fr frame) {
	data, err := json.Marshal(fr)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("event: " + fr.Kind + "\ndata: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
}

// trackingSubmitter decorates a real agent.Submitter with Outbox lifecycle
// tracking (queued is set by the enqueue handler before Enqueue; this
// wrapper covers submitting/confirmed/rejected) — a pure decorator so
// internal/edge/agent needs no changes for facet's UI-facing state (that
// package deliberately has no "on success" hook; see agent.go's resolve()).
type trackingSubmitter struct {
	inner agent.Submitter
	feed  *feed
}

func (t *trackingSubmitter) Submit(ctx context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	t.feed.setOutboxState(env.RequestID, "submitting", "", "")
	reply, err := t.inner.Submit(ctx, env)
	if err != nil {
		// Transport failure (Gateway/NATS unreachable) — not a business
		// rejection. Revert to queued so the next Drain tick retries and
		// the Outbox keeps showing "Queued", per facet-app-ux.md §5 step 4
		// (offline queue, no user action required on reconnect).
		t.feed.setOutboxState(env.RequestID, "queued", "", "")
		return reply, err
	}
	switch reply.Status {
	case processor.ReplyStatusAccepted, processor.ReplyStatusDuplicate:
		t.feed.setOutboxState(env.RequestID, "confirmed", "", "")
	case processor.ReplyStatusRejected:
		code, msg := "", ""
		if reply.Error != nil {
			code, msg = string(reply.Error.Code), reply.Error.Message
		}
		t.feed.setOutboxState(env.RequestID, "rejected", code, msg)
	}
	return reply, err
}
