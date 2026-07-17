// Package agent is the Edge node's intent uploader + reconcile-by-revision
// (edge-lattice-full-design.md §3.5): a durable FIFO of operation envelopes
// queued by locally-triggered mutations (internal/edge/overlay's optimistic
// apply, §3.4), submitted to the platform on Drain via a pluggable
// Submitter — GatewaySubmitter (submit_gateway.go) is the untrusted
// multi-identity posture EDGE.3 turns on (the Gateway verifies the bearer
// token and stamps env.Actor itself, never trusting anything the caller
// asserts); NATSSubmitter (submit_nats.go) is the EDGE.1/2 trusted-single-
// identity direct-to-core-operations posture, kept for tests and any
// fully-trusted deployment that runs without a Gateway.
//
// A rejected commit is the only hard case (§3.5): RevisionConflict means
// the cloud state moved under the offline edit, so Drain triggers a full
// re-hydrate (no anchor-scoped hydrate RPC ships yet, so the Sync Manager's
// existing personal.hydrate is reused verbatim via Rehydrator) before
// discarding the now-stale optimistic overlay for every key the intent
// touched; any other rejection (validation/auth) discards the same way
// without a re-hydrate, since the cloud state didn't move. An accepted or
// duplicate reply dequeues the intent without touching the overlay — it
// retires itself once the Sync Manager applies the confirmed delta past its
// baseline (overlay.Read), per R3 ("cleared by the authoritative cloud
// value, never local success alone").
//
// Local GC (§3.5 "Federated Weaver ... Local GC") sweeps pending overlays a
// Read never revisited, retiring any the confirmed mirror has already
// superseded.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/asolgan/lattice/internal/edge/overlay"
	"github.com/asolgan/lattice/internal/edge/store"
	"github.com/asolgan/lattice/internal/processor"
)

// Submitter delivers one operation envelope to the platform and waits for
// its terminal reply. Implementations decide the transport and trust
// posture — see GatewaySubmitter and NATSSubmitter.
type Submitter interface {
	Submit(ctx context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error)
}

// Rehydrator triggers a full cold re-projection of the identity's slice.
// *sync.Manager satisfies this via its Rehydrate method.
type Rehydrator interface {
	Rehydrate(ctx context.Context) error
}

// ConflictInfo describes one intent the cloud rejected — the design's
// "re-present the intent to the user" (§3.5/F7), surfaced to the caller's
// Config.Conflict hook since presentation is a UI concern this package
// doesn't own.
type ConflictInfo struct {
	RequestID string
	Keys      []string
	Reply     *processor.OperationReply
}

// Config configures an Agent.
type Config struct {
	// Conflict is called for every rejected intent, after its overlay has
	// been discarded. Optional — nil logs the rejection and takes no
	// further action.
	Conflict func(ConflictInfo)
	Logger   *slog.Logger
}

// Agent is the Edge node's intent uploader + reconciler.
type Agent struct {
	submitter Submitter
	store     store.Store
	overlay   *overlay.Overlay
	rehydrate Rehydrator
	cfg       Config
	logger    *slog.Logger
}

// New builds an Agent. rehydrate may be nil only if the caller never
// expects a RevisionConflict (e.g. tests with a synthetic conflict-free
// harness) — a nil Rehydrator on an actual conflict is a logged no-op, not
// a panic. submitter may be nil only if the caller never calls Drain (e.g.
// GC-only tests).
func New(submitter Submitter, st store.Store, ov *overlay.Overlay, rehydrate Rehydrator, cfg Config) *Agent {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{submitter: submitter, store: st, overlay: ov, rehydrate: rehydrate, cfg: cfg, logger: logger}
}

// intentRecord is the store.IntentRecord.Envelope payload: the operation
// envelope plus the keys overlay.Apply already installed a pending overlay
// for, so Drain's reject path knows what to Discard.
type intentRecord struct {
	Envelope    *processor.OperationEnvelope `json:"envelope"`
	TouchedKeys []string                     `json:"touchedKeys,omitempty"`
}

// Enqueue durably queues env for upload (§3.5's "queue the intent"). Call
// AFTER overlay.Apply installs the optimistic value for every key in
// touchedKeys, so the UI sees the change before the intent is even queued,
// let alone submitted.
func (a *Agent) Enqueue(env *processor.OperationEnvelope, touchedKeys []string) error {
	b, err := json.Marshal(intentRecord{Envelope: env, TouchedKeys: touchedKeys})
	if err != nil {
		return fmt.Errorf("edge/agent: encode intent %s: %w", env.RequestID, err)
	}
	if _, err := a.store.EnqueueIntent(b); err != nil {
		return fmt.Errorf("edge/agent: enqueue intent %s: %w", env.RequestID, err)
	}
	return nil
}

// Drain submits every currently-queued intent, in FIFO order, stopping at
// the first transport-level failure (the Submitter itself erroring —
// offline again, or the Gateway rejecting the credential outright) so a
// later Drain call resumes where this one stopped. A terminal reply
// (accepted/duplicate/rejected) always dequeues the intent, since the cloud
// has authoritatively decided its fate.
func (a *Agent) Drain(ctx context.Context) error {
	recs, err := a.store.ListIntents()
	if err != nil {
		return fmt.Errorf("edge/agent: list intents: %w", err)
	}
	for _, rec := range recs {
		var ir intentRecord
		unmarshalErr := json.Unmarshal(rec.Envelope, &ir)
		if unmarshalErr != nil || ir.Envelope == nil || ir.Envelope.RequestID == "" {
			// A malformed (or emptied) queued intent can never become
			// submittable on a later attempt — drop it rather than wedging
			// the queue forever or nil-dereffing ir.Envelope below.
			a.logger.Error("edge/agent: malformed queued intent, dropping", "seq", rec.Seq, "err", unmarshalErr)
			if delErr := a.store.DeleteIntent(rec.Seq); delErr != nil {
				return fmt.Errorf("edge/agent: dequeue malformed intent %d: %w", rec.Seq, delErr)
			}
			continue
		}
		reply, err := a.submitter.Submit(ctx, ir.Envelope)
		if err != nil {
			return fmt.Errorf("edge/agent: submit %s: %w", ir.Envelope.RequestID, err)
		}
		if err := a.resolve(ctx, ir, reply); err != nil {
			a.logger.Error("edge/agent: resolve reply failed", "requestId", ir.Envelope.RequestID, "err", err)
		}
		if err := a.store.DeleteIntent(rec.Seq); err != nil {
			return fmt.Errorf("edge/agent: dequeue %s: %w", ir.Envelope.RequestID, err)
		}
	}
	return nil
}

// resolve applies a terminal reply's outcome: accepted/duplicate does
// nothing further (the overlay retires itself once the mirror catches up);
// rejected re-hydrates on RevisionConflict, discards the intent's overlays
// unconditionally, and reports the conflict.
func (a *Agent) resolve(ctx context.Context, ir intentRecord, reply *processor.OperationReply) error {
	switch reply.Status {
	case processor.ReplyStatusAccepted, processor.ReplyStatusDuplicate:
		return nil
	case processor.ReplyStatusRejected:
		if reply.Error != nil && reply.Error.Code == processor.ErrCodeRevisionConflict && a.rehydrate != nil {
			if err := a.rehydrate.Rehydrate(ctx); err != nil {
				a.logger.Error("edge/agent: re-audit rehydrate failed", "requestId", ir.Envelope.RequestID, "err", err)
			}
		}
		for _, key := range ir.TouchedKeys {
			if err := a.overlay.Discard(key); err != nil {
				return fmt.Errorf("discard overlay %q: %w", key, err)
			}
		}
		a.reportConflict(ir, reply)
		return nil
	default:
		return fmt.Errorf("unrecognized reply status %q", reply.Status)
	}
}

func (a *Agent) reportConflict(ir intentRecord, reply *processor.OperationReply) {
	if a.cfg.Conflict != nil {
		a.cfg.Conflict(ConflictInfo{RequestID: ir.Envelope.RequestID, Keys: ir.TouchedKeys, Reply: reply})
		return
	}
	code, msg := "", ""
	if reply.Error != nil {
		code, msg = string(reply.Error.Code), reply.Error.Message
	}
	a.logger.Warn("edge/agent: intent rejected", "requestId", ir.Envelope.RequestID, "code", code, "message", msg)
}

// GC sweeps every key with an active pending overlay, retiring any the
// confirmed mirror has already superseded (§3.5 "Local GC"). Read() already
// retires a stale overlay lazily on access; GC exists so a key nobody reads
// again doesn't hold one forever. Returns the number of keys still pending
// after the sweep.
func (a *Agent) GC() (stillPending int, err error) {
	keys, err := a.overlay.PendingKeys()
	if err != nil {
		return 0, fmt.Errorf("edge/agent: GC: %w", err)
	}
	for _, k := range keys {
		v, ok, err := a.overlay.Read(k)
		if err != nil {
			return 0, fmt.Errorf("edge/agent: GC: read %q: %w", k, err)
		}
		if ok && v.Pending {
			stillPending++
		}
	}
	return stillPending, nil
}
