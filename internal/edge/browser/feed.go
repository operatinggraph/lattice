//go:build js

package browser

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/edge/overlay"
	edgevault "github.com/operatinggraph/lattice/internal/edge/vault"
	"github.com/operatinggraph/lattice/internal/processor/opwire"
)

// The engine's outbound event surface. These are cmd/facet's SSE frames
// (cmd/facet/feed.go) with the transport removed: same kinds, same fields,
// same JSON — delivered to an onFrame callback in-page instead of over an
// EventSource. Keeping them identical is what makes W4's renderer swap a
// transport change rather than a rewrite.
//
// One of facet's kinds is driven differently here: facet raises "connectivity"
// from nats.go's disconnect/reconnect handlers on the host's own connection.
// Here the connection belongs to the JS shell, so the shell reports it in
// through setConnected — the engine has no view of a socket it does not own,
// and inferring connectivity from delta silence would show "offline" for any
// quiet slice.
type frame struct {
	Kind         string          `json:"kind"`
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

func (f frame) toJS() any { return jsonParse(f) }

// outboxEntry is one enqueued write and its lifecycle (facet-app-ux.md §3.4a),
// carrying enough of the original request that the renderer can reopen a
// rejected write's form pre-filled without re-deriving anything.
type outboxEntry struct {
	RequestID     string              `json:"requestId"`
	OperationType string              `json:"operationType"`
	Payload       json.RawMessage     `json:"payload"`
	Reads         []string            `json:"reads,omitempty"`
	OptionalReads []string            `json:"optionalReads,omitempty"`
	AuthContext   *opwire.AuthContext `json:"authContext,omitempty"`
	State         string              `json:"state"` // queued|submitting|confirmed|rejected
	ErrorCode     string              `json:"errorCode,omitempty"`
	ErrorMessage  string              `json:"errorMessage,omitempty"`
	CreatedAt     time.Time           `json:"createdAt"`
}

// maxOutboxEntries bounds the in-memory outbox. Unlike the intent queue, this
// is presentation state only — it is never persisted, and a reload rebuilds
// what still matters from the queue itself.
const maxOutboxEntries = 200

// feed broadcasts frames to every onFrame subscriber and holds the Outbox's
// state. Safe for concurrent use: the sync manager's delivery goroutine, the
// drain loop, and the page's own calls all reach it.
type feed struct {
	mu      sync.Mutex
	subs    map[chan frame]struct{}
	outbox  map[string]*outboxEntry
	outboxQ []string

	// revokedReason is sticky: the token is minted once per start and never
	// refreshed here, so a dead credential stays dead for this host's whole
	// lifetime, and a renderer that subscribes after the drain loop found the
	// revocation must still be told. Sign-out ends it by calling stop() and
	// starting a fresh host.
	revokedReason string
	connected     bool
	// syncDegraded mirrors facet's sticky sync-manager-degraded axis on the
	// same "connectivity" frame kind. This host runs the sync manager once
	// per page lifetime (no restart loop), so an error exit marks it and a
	// reload is what clears it.
	syncDegraded bool

	// selfName decrypts this identity's sealed name into the manifest.me
	// row's displayName on its way to the renderer. nil passes every row
	// through untouched.
	selfName *edgevault.SelfName
}

func newFeed(selfName *edgevault.SelfName) *feed {
	return &feed{
		subs:     make(map[chan frame]struct{}),
		outbox:   make(map[string]*outboxEntry),
		selfName: selfName,
		// The shell reports the real state through setConnected as soon as it
		// knows; starting optimistic keeps a freshly-started host from
		// flashing an offline banner before the first report arrives.
		connected: true,
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
			// A stuck subscriber drops frames rather than blocking the sync
			// manager's delivery goroutine; snapshot() is the recovery.
		}
	}
}

// publishManifestKey re-reads key through the overlay (merging any pending
// optimistic value) and publishes its current state — the same function serves
// a live delta and a snapshot row, so the two cannot drift.
func (f *feed) publishManifestKey(ov *overlay.Overlay, key string, deleted bool) {
	v, ok, err := ov.Read(key)
	if err != nil || !ok {
		f.publish(frame{Kind: "manifest", Key: key, Deleted: true})
		return
	}
	f.publish(f.manifestFrame(key, v))
}

// manifestFrame builds the renderer-facing frame for one manifest row. Every
// path that hands a manifest row to the page — the live delta above, the
// snapshot replay, and the read() export — goes through here, so the
// decoration they apply cannot drift (the same seam cmd/facet/feed.go holds).
func (f *feed) manifestFrame(key string, v overlay.Value) frame {
	return frame{
		Kind:    "manifest",
		Key:     key,
		Deleted: v.Deleted,
		Pending: v.Pending,
		Data:    f.selfName.Decorate(context.Background(), key, v.Data),
	}
}

// publishRevoked records and broadcasts that the Gateway refused this
// identity's credential. Idempotent: the drain loop re-discovers the same
// rejection every tick while intents remain queued.
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

func (f *feed) revoked() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revokedReason, f.revokedReason != ""
}

// setConnected records the shell's reported connectivity and broadcasts real
// transitions only — a shell may report the same state repeatedly across a
// flapping socket.
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

// setSyncDegraded records the sync manager's degraded axis and broadcasts
// real transitions on the same "connectivity" frame kind.
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

// connectivityState returns the sticky connectivity pair (for the snapshot
// replay a fresh onFrame subscriber receives).
func (f *feed) connectivityState() (connected, syncDegraded bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected, f.syncDegraded
}

func (f *feed) enqueueOutbox(e *outboxEntry) {
	f.mu.Lock()
	f.outbox[e.RequestID] = e
	f.outboxQ = append(f.outboxQ, e.RequestID)
	f.trimOutboxLocked()
	f.mu.Unlock()
	f.publish(frame{Kind: "outbox", Outbox: e})
}

// setOutboxState updates a tracked entry and publishes the change. An unknown
// requestId is a silent no-op: a queued intent from a prior page lifetime
// drains after a reload with no outbox entry to update.
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

// trimOutboxLocked drops the oldest terminal entries past the cap, stopping at
// the first still-in-flight one rather than dropping a write the UI needs.
// Caller holds f.mu.
func (f *feed) trimOutboxLocked() {
	for len(f.outboxQ) > maxOutboxEntries {
		id := f.outboxQ[0]
		e, ok := f.outbox[id]
		if ok && e.State != "confirmed" && e.State != "rejected" {
			break
		}
		delete(f.outbox, id)
		f.outboxQ = f.outboxQ[1:]
	}
}

// trackingSubmitter decorates a real agent.Submitter with Outbox lifecycle
// tracking — a pure decorator, so internal/edge/agent needs no UI-facing hook
// (it deliberately has none; see its resolve()).
type trackingSubmitter struct {
	inner agent.Submitter
	feed  *feed
}

func (t *trackingSubmitter) Submit(ctx context.Context, env *opwire.OperationEnvelope) (*opwire.OperationReply, error) {
	t.feed.setOutboxState(env.RequestID, "submitting", "", "")
	reply, err := t.inner.Submit(ctx, env)
	if err != nil {
		// Transport failure, not a business rejection: revert to queued so the
		// Outbox keeps showing "Queued" and the next drain tick retries, which
		// is the offline path requiring no user action.
		t.feed.setOutboxState(env.RequestID, "queued", "", "")
		return reply, err
	}
	switch reply.Status {
	case opwire.ReplyStatusAccepted, opwire.ReplyStatusDuplicate:
		t.feed.setOutboxState(env.RequestID, "confirmed", "", "")
	case opwire.ReplyStatusRejected:
		code, msg := "", ""
		if reply.Error != nil {
			code, msg = string(reply.Error.Code), reply.Error.Message
		}
		t.feed.setOutboxState(env.RequestID, "rejected", code, msg)
	}
	return reply, err
}
