package objectmanager

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// The owner-tombstone-cascade (v1b GC §22). A SECOND durable consumer — over
// the core-kv KV stream, not core-events — closes the §21.2 dead-target byte
// LEAK: when an OWNER vertex is tombstoned with an object still attached, the
// owner-death never touches the object, so its data.liveLinks stays stale ≥ 1
// and the objectLiveness lens never flags it orphaned → its bytes leak.
//
// The cascade reacts to the AUTHORITATIVE owner-tombstone (the owner's own
// core-kv root transitioning to isDeleted — zero projection lag, so the §21
// attach-lag data-loss hazard cannot recur), enumerates the dead owner's live
// object→owner links, and submits DetachObject per link under the
// object-store-manager's root-equivalent service actor. DetachObject decrements
// liveLinks + OCC-touches the object vertex → the existing Loop A+B
// (objectLiveness liveLinks=0 → Weaver directOp(TombstoneObject) → byte-reclaim)
// reclaims any now-orphaned object. The cascade adds ZERO new reap path — it
// only detaches dangling links — mirroring the identity-hygiene-merge
// link-enumeration precedent (CC4), generalized type-agnostically.

const (
	// CascadeDurable is the owner-tombstone-cascade consumer's durable name.
	CascadeDurable = "object-store-cascade"
	// DefaultOpLane is the ops.<lane> the cascade submits DetachObject on
	// (matches Weaver's default — the Processor consumes ops.system).
	DefaultOpLane = "system"

	objectLinkPrefix = "lnk.object."
)

// cascadeFilterSubject selects only 3-segment vertex ROOT subjects on the
// core-kv KV stream ($KV.<bucket>.vtx.<type>.<id>): `*` matches exactly one
// token, so 4-segment aspects (vtx.T.id.aspect) and lnk.* links are excluded
// (NanoIDs and type names carry no dots, so a root is always exactly 3 segments).
func cascadeFilterSubject(bucket string) string {
	return "$KV." + bucket + ".vtx.*.*"
}

// runCascade drives the owner-tombstone-cascade consumer, blocking until ctx is
// cancelled. No-op (blocks on ctx) when no ActorKey is configured — the cascade
// submits graph ops, so it is disabled without a service actor (a minimal/test
// deployment that wants only the byte-janitor).
func (m *Manager) runCascade(ctx context.Context) error {
	if m.cfg.ActorKey == "" {
		m.cfg.Logger.Warn("object-store-manager: no ActorKey configured; owner-tombstone-cascade disabled")
		<-ctx.Done()
		return ctx.Err()
	}
	durable := m.cfg.CascadeDurable
	if durable == "" {
		durable = CascadeDurable
	}
	// RunDurableConsumer creates with DeliverAllPolicy: the FIRST deploy does a
	// one-time catch-up over core-kv history — so an owner tombstoned BEFORE the
	// cascade existed is reconciled too (its leak is closed), not just new ones —
	// then the durable resumes incrementally from its ack floor on restart. The
	// per-message work is cheap (decode isDeleted + Ack) for the non-tombstone
	// majority; the lnk.object.> enumeration runs only for an actual tombstone.
	return m.cfg.Conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:          "KV_" + m.cfg.CoreKVBucket,
		FilterSubject:   cascadeFilterSubject(m.cfg.CoreKVBucket),
		Durable:         durable,
		RedeliveryDelay: redeliveryDelay,
		Logger:          m.cfg.Logger,
	}, m.handleVertexUpdate)
}

// handleVertexUpdate is the cascade consumer's per-message handler. It recovers
// the vertex key from the KV subject, acts ONLY on a vertex root transitioning
// to isDeleted=true (a tombstone), and drives the detach of that owner's
// dangling object-links. Idempotent: a redelivered tombstone re-enumerates and
// re-submits the same deterministic requestIds (Contract #4 tracker collapse),
// and already-detached links are skipped.
func (m *Manager) handleVertexUpdate(ctx context.Context, msg substrate.Message) substrate.Decision {
	prefix := "$KV." + m.cfg.CoreKVBucket + "."
	if !strings.HasPrefix(msg.Subject, prefix) {
		return substrate.Ack // not a core-kv subject (defensive)
	}
	vertexKey := strings.TrimPrefix(msg.Subject, prefix)
	ownerType, ownerID, ok := splitVertexRoot(vertexKey)
	if !ok {
		return substrate.Ack // not a 3-segment vtx root (the filter already excludes these)
	}
	if len(msg.Body) == 0 {
		return substrate.Ack // a KV delete-marker / empty body — nothing to classify
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if json.Unmarshal(msg.Body, &doc) != nil {
		// Unparseable root — can't classify a tombstone; skip (a later event or
		// the never-attached reconcile covers any fallout). Ack, don't poison-loop.
		return substrate.Ack
	}
	if !doc.IsDeleted {
		return substrate.Ack // a create / revive / touch — not a tombstone
	}
	// An object vertex itself appearing here is a no-op: objects are link
	// SOURCES, never targets, so no lnk.object.* names one as owner — the
	// enumeration below simply matches nothing.
	return m.cascadeDetach(ctx, vertexKey, ownerType, ownerID, msg.Sequence)
}

// cascadeDetach enumerates the dead owner's live inbound object-links and
// submits DetachObject for each. Publish-then-ack (CC10): every detach is
// submitted before the trigger is Ack'd, so a crash/Nak re-delivers the
// tombstone and the deterministic requestIds collapse the re-submits. Any
// authoritative read/publish error Naks the whole tombstone (never guess).
func (m *Manager) cascadeDetach(ctx context.Context, ownerKey, ownerType, ownerID string, seq uint64) substrate.Decision {
	linkKeys, err := m.cfg.Conn.KVListKeysPrefix(ctx, m.cfg.CoreKVBucket, objectLinkPrefix)
	if err != nil {
		if substrate.IsConnectionError(err) {
			return substrate.NakWithDelay
		}
		m.cfg.Logger.Warn("object-store-manager: cascade list object links failed; retrying", "owner", ownerKey, "error", err)
		return substrate.NakWithDelay
	}
	suffix := "." + ownerType + "." + ownerID
	submitted := 0
	for _, lk := range linkKeys {
		if !strings.HasSuffix(lk, suffix) {
			continue
		}
		oid, linkName, ok := parseObjectLinkKey(lk, ownerType, ownerID)
		if !ok {
			continue
		}
		// Act only on a LIVE link (skip already-detached / stale matches). Read
		// the link authoritatively from core-kv.
		entry, getErr := m.cfg.Conn.KVGet(ctx, m.cfg.CoreKVBucket, lk)
		if errors.Is(getErr, substrate.ErrKeyNotFound) {
			continue
		}
		if getErr != nil {
			if substrate.IsConnectionError(getErr) {
				return substrate.NakWithDelay
			}
			m.cfg.Logger.Warn("object-store-manager: cascade read link failed; retrying", "link", lk, "error", getErr)
			return substrate.NakWithDelay
		}
		var ld struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if json.Unmarshal(entry.Value, &ld) == nil && ld.IsDeleted {
			continue // already a soft-tombstoned link — nothing to detach
		}
		objKey := "vtx.object." + oid
		if err := m.submitDetach(ctx, oid, ownerKey, linkName, lk, objKey, seq); err != nil {
			if substrate.IsConnectionError(err) {
				return substrate.NakWithDelay
			}
			m.cfg.Logger.Warn("object-store-manager: cascade submit DetachObject failed; retrying", "link", lk, "error", err)
			return substrate.NakWithDelay
		}
		submitted++
	}
	if submitted > 0 {
		m.cfg.Logger.Info("object-store-manager: owner-tombstone-cascade detached object links",
			"owner", ownerKey, "count", submitted)
	}
	return substrate.Ack
}

// cascadeOpEnvelope is the Contract #2 §2.1 op wire format the cascade publishes
// to ops.<lane> — the same shape internal/processor reads; the manager carries
// its own copy to keep the module boundary clean (substrate-only, no
// internal/processor / internal/weaver import).
type cascadeOpEnvelope struct {
	RequestID     string              `json:"requestId"`
	Lane          string              `json:"lane"`
	OperationType string              `json:"operationType"`
	Actor         string              `json:"actor"`
	SubmittedAt   string              `json:"submittedAt"`
	Payload       json.RawMessage     `json:"payload"`
	ContextHint   *cascadeContextHint `json:"contextHint,omitempty"`
	AuthContext   *cascadeAuthContext `json:"authContext,omitempty"`
}

type cascadeContextHint struct {
	Reads []string `json:"reads,omitempty"`
}

type cascadeAuthContext struct {
	Target string `json:"target,omitempty"`
}

// submitDetach publishes one DetachObject op for a dangling link. The class is
// left empty — the Processor's operationType→class reverse index resolves
// DetachObject to the object DDL (Contract #2 §2.1). contextHint.reads carries
// the link + object-vertex keys the DetachObject script hydrates; authContext
// targets the object vertex (the operator grant is scope:any). The requestId is
// deterministic so an at-least-once redelivery collapses on the Contract #4
// vtx.op.<requestId> tracker.
func (m *Manager) submitDetach(ctx context.Context, oid, ownerKey, linkName, linkKey, objKey string, seq uint64) error {
	payload, err := json.Marshal(map[string]any{
		"oid":       oid,
		"targetKey": ownerKey,
		"linkName":  linkName,
	})
	if err != nil {
		return err
	}
	env := cascadeOpEnvelope{
		RequestID:     deriveCascadeRequestID(objKey, linkKey, seq),
		Lane:          m.cfg.OpLane,
		OperationType: "DetachObject",
		Actor:         m.cfg.ActorKey,
		SubmittedAt:   substrate.FormatTimestamp(m.cfg.now()),
		Payload:       payload,
		ContextHint:   &cascadeContextHint{Reads: []string{linkKey, objKey}},
		AuthContext:   &cascadeAuthContext{Target: objKey},
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return m.cfg.Conn.Publish(ctx, "ops."+m.cfg.OpLane, data, nil)
}

// splitVertexRoot parses a vtx.<type>.<id> root key into (type, id, ok). Returns
// ok=false for anything that is not exactly a 3-segment vtx root (an aspect, a
// link, or a malformed key).
func splitVertexRoot(key string) (vtype, id string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "vtx" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// parseObjectLinkKey parses lnk.object.<oid>.<linkName>.<tgtType>.<tgtId> and
// confirms the target equals (ownerType, ownerID). Returns (oid, linkName, ok).
// linkName is a localName carrying no dots, and oid/type/id are dot-free, so a
// well-formed object link is exactly 6 segments.
func parseObjectLinkKey(key, ownerType, ownerID string) (oid, linkName string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 6 || parts[0] != "lnk" || parts[1] != "object" {
		return "", "", false
	}
	if parts[4] != ownerType || parts[5] != ownerID {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// deriveCascadeRequestID returns a deterministic 20-char Contract #1 NanoID for
// one (object, link, tombstone-delivery) detach. Keyed on the object + link key
// plus the tombstone message's backing-stream sequence: a redelivery of the
// SAME tombstone reuses the sequence → same id → Contract #4 tracker collapse;
// a genuinely new tombstone (e.g. after a revive + re-attach + re-death) is a
// new message with a new sequence → a new id → a real new detach.
func deriveCascadeRequestID(objKey, linkKey string, seq uint64) string {
	var s [8]byte
	binary.BigEndian.PutUint64(s[:], seq)
	sum := sha256.Sum256(append([]byte("objcascade:"+objKey+"\x00"+linkKey+":"), s[:]...))
	id := make([]byte, substrate.NanoIDLength)
	digest := sum[:]
	di := 0
	for i := 0; i < substrate.NanoIDLength; i++ {
		if di >= len(digest) {
			next := sha256.Sum256(digest)
			digest = next[:]
			di = 0
		}
		id[i] = substrate.Alphabet[int(digest[di])%len(substrate.Alphabet)]
		di++
	}
	return string(id)
}
