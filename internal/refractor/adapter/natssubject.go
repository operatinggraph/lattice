package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

var (
	_ Adapter               = (*NatsSubjectAdapter)(nil)
	_ PublishPipelineOpener = (*NatsSubjectAdapter)(nil)
)

// PersonalActorKeyField is the reserved key field a "nats_subject" Personal
// Lens's targetConfig.key must include: the recipient identity used to
// resolve the per-actor delivery subject (personal-secure-lens-design.md
// §3.1). A lens's cypher RETURN aliases this column directly; the pipeline's
// per-actor fan-out (Fire 2) will also drive it via the same reserved name.
const PersonalActorKeyField = "__actor"

// Reserved row fields promoted from Upsert's row map to the delta envelope's
// top-level metadata (personal-secure-lens-design.md §3.1's wire shape) —
// the remainder of row becomes the envelope's Data. A lens's cypher RETURN
// aliases these column names when it wants to carry that metadata.
const (
	rowFieldAnchor = "anchor"
	rowFieldKind   = "kind"
	rowFieldClass  = "class"
)

// deltaEnvelope is the wire shape a Personal Lens delta publishes to
// lattice.sync.user.<actor> (docs/components/refractor.md).
type deltaEnvelope struct {
	Op            string         `json:"op"` // "upsert" | "delete" | "keyset" | "hydrationComplete"
	Key           string         `json:"key"`
	Anchor        string         `json:"anchor,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	Class         string         `json:"class,omitempty"`
	Revision      uint64         `json:"revision"`
	ProjectionSeq uint64         `json:"projectionSeq"`
	Encrypted     bool           `json:"encrypted"`
	Data          map[string]any `json:"data,omitempty"`
	// Lens is the producing lens's rule ID (personal-lens-retraction-
	// design.md §3.1, R1) — set on "upsert" and "keyset" envelopes so the
	// Edge client can attribute a key to the lens that asserts it. A same
	// key can be projected by more than one lens; attribution is what lets
	// the client refcount survive one lens retracting while another still
	// asserts.
	Lens string `json:"lens,omitempty"`
	// Keys carries the complete, authoritative business-key set a "keyset"
	// envelope asserts for its actor+lens as of Revision — nil/empty is a
	// valid, meaningful value (the last-row-retraction case).
	Keys []string `json:"keys,omitempty"`
}

// NatsSubjectAdapter publishes materialized rows as per-recipient delta
// envelopes to a subject resolved per row (`<subjectPrefix>.<actor>`) — the
// Personal Lens transport (personal-secure-lens-design.md Fire 1: PL.1).
//
// Unlike NatsKVAdapter it holds no persistent state of its own: each
// Upsert/Delete is a fire-and-forget JetStream publish, ordered by the
// backing stream's sequence within a subject. There is no CAS/guard concept
// here — an append-only delta log has nothing to compare a write against;
// the recipient (the Edge Sync Manager) dedups/reorders by envelope revision.
type NatsSubjectAdapter struct {
	conn          *substrate.Conn
	ruleID        string
	subjectPrefix string
	stream        string
	keyOrder      []string // full Into.Key order, including PersonalActorKeyField
}

// NewNatsSubjectAdapter creates a NatsSubjectAdapter and ensures the backing
// JetStream stream exists (idempotent — safe on every process startup,
// mirrors the nats_kv case's JIT bucket creation in cmd/refractor). ruleID
// is the owning lens's rule ID, stamped onto every "upsert"/"keyset"
// envelope's Lens field (personal-lens-retraction-design.md §3.1) so the
// Edge client can attribute a key to the lens asserting it.
// keyOrder must include PersonalActorKeyField exactly once; the platform's
// NanoID alphabet carries no dots, so the reserved field's value is a safe
// single subject token (subjects.PersonalSync validates it defensively).
func NewNatsSubjectAdapter(ctx context.Context, conn *substrate.Conn, ruleID, subjectPrefix, stream string, keyOrder []string) (*NatsSubjectAdapter, error) {
	if ruleID == "" {
		return nil, errors.New("natssubject: ruleID must not be empty")
	}
	if subjectPrefix == "" {
		return nil, errors.New("natssubject: subjectPrefix must not be empty")
	}
	if stream == "" {
		return nil, errors.New("natssubject: stream must not be empty")
	}
	if !containsField(keyOrder, PersonalActorKeyField) {
		return nil, fmt.Errorf("natssubject: keyOrder must include %q", PersonalActorKeyField)
	}
	if err := ensureSyncStream(ctx, conn, stream, subjectPrefix); err != nil {
		return nil, fmt.Errorf("natssubject: ensure stream %q: %w", stream, err)
	}
	return &NatsSubjectAdapter{conn: conn, ruleID: ruleID, subjectPrefix: subjectPrefix, stream: stream, keyOrder: keyOrder}, nil
}

// syncStreamMaxAge bounds the SYNC stream's retention (personal-secure-lens-
// design.md §3.2: "short retention ... the vault's 'ephemerality' property").
// A node that falls behind this window re-hydrates from a cold
// personal.hydrate call instead of replaying a long backlog.
const syncStreamMaxAge = 24 * time.Hour

// syncStreamMaxMsgsPerSubject backstops syncStreamMaxAge for a high-volume
// actor: MaxAge alone bounds retention by time, not by count, so an actor
// whose anchors mutate often within the window can still accumulate
// unbounded messages on their subject (personal-secure-lens-design.md §3.2:
// "MaxAge: 24h, per-subject MaxMsgsPerSubject cap"). A device that falls
// behind this many deltas hits the same gap → personal.hydrate re-projection
// path as falling behind MaxAge.
const syncStreamMaxMsgsPerSubject = 10_000

// syncStreamMaxBytes bounds the SYNC stream's total storage to a fixed
// budget — 512 MiB, the same ceiling EnsureAuditStream applies to
// REFRACTOR_AUDIT (internal/refractor/health/audit_writer.go) — independent
// of how many actors are attached. syncStreamMaxAge and
// syncStreamMaxMsgsPerSubject bound one actor's own backlog but not the sum
// across every actor's subject; live 2026-08-10 measurement: 993 MB / 1.07M
// msgs of a 1.66 GB JetStream store, unbounded growth toward the same OOM
// the audit stream's cap was added to prevent. Hitting the cap discards the
// stream's oldest messages first (JetStream's default DiscardOld policy),
// which is exactly the existing MaxAge/per-subject fallback: a node that
// falls behind re-hydrates via personal.hydrate rather than replaying a
// long backlog.
const syncStreamMaxBytes = 512 << 20

// syncDurableReapMargin is slack added atop syncStreamMaxAge for
// SyncConsumerInactiveThreshold: clock skew, plus a node reconnecting right
// at the retention boundary (edge-sync-orphan-expiry-design.md §4.3).
const syncDurableReapMargin = 1 * time.Hour

// SyncConsumerInactiveThreshold is the SYNC stream's ConsumerLimits.
// InactiveThreshold (edge-sync-orphan-expiry-design.md §4.3): every durable
// consumer on SYNC that declares no threshold of its own inherits this one
// (nats-server v2.14.0 — the pinned module — server/consumer.go:662-666), and
// one that asks for a longer threshold is refused (:843-844,
// NewJSConsumerInactiveThresholdExcessError). Expressed as a sum against
// syncStreamMaxAge rather than as a bare literal — a durable's ack floor is
// worth preserving only while a resume from it can still deliver something a
// fresh consumer would not, and past the stream's own retention horizon it
// cannot (edge-sync-orphan-expiry-design.md §7), so the threshold must move
// with whatever that horizon is.
const SyncConsumerInactiveThreshold = syncStreamMaxAge + syncDurableReapMargin

// SyncConsumerMaxAckPending is the SYNC stream's ConsumerLimits.MaxAckPending.
// Its VALUE is the one nats-server already gives every explicit-ack consumer
// that asks for none (JsDefaultMaxAckPending, v2.14.0 server/consumer.go:576,
// :670), so nothing that exists today is affected. Its PRESENCE is forced:
// SyncConsumerInactiveThreshold cannot be adopted without it.
//
// Adopting either consumer limit makes the server re-validate every EXISTING
// consumer against both, and that validation reads a zero MaxAckPending limit
// as an allowance of zero (server/stream.go:2433-2434) rather than guarding it
// with `> 0` the way the consumer-create path does (:840). So on any stream
// that already carries an explicit-ack consumer — every deployment whose
// devices have ever attached — declaring only the InactiveThreshold is refused
// outright ("change to limits violates consumers"), ensureSyncStream fails,
// and the Personal Lens pipeline never activates.
//
// Two consequences a future author on this stream inherits, both refusals at
// consumer-create time rather than silent behaviour changes:
//
//   - A consumer asking for more than 1000 un-acked in flight is refused
//     (server/consumer.go:840-841). Raise this constant if a SYNC host ever
//     genuinely needs a deeper window.
//   - A PUSH consumer with AckPolicy=none is refused outright. The stream
//     limit is stamped onto the config (server/consumer.go:656-660) BEFORE the
//     ack-policy defaulting that would otherwise leave it zero (:669), and
//     checkConsumerCfg's push branch then rejects a non-zero MaxAckPending
//     alongside AckNone (:801). A PULL AckNone consumer is unaffected
//     (verified against the pinned server). Nothing in the tree creates
//     either, but `js.Subscribe(..., nats.OrderedConsumer())` is the natural
//     reach that would hit it.
const SyncConsumerMaxAckPending = 1000

// ensureSyncStream provisions the backing stream, unioning subjectPrefix's
// wildcard into any subjects the stream already carries rather than
// replacing them outright. JetStream's CreateOrUpdateStream (substrate's
// EnsureStream) sets Subjects verbatim — a plain overwrite would let a
// second nats_subject lens sharing the same stream name but a different
// subjectPrefix silently narrow the first lens's subject coverage on every
// process restart or hot-reload (a deterministic config clobber, not a
// race). The SYNC stream is a platform-wide convention meant to carry one
// subjectPrefix, but this makes sharing safe regardless.
func ensureSyncStream(ctx context.Context, conn *substrate.Conn, stream, subjectPrefix string) error {
	wildcard := subjectPrefix + ".>"
	existingSubjects, err := existingStreamSubjects(ctx, conn, stream)
	if err != nil {
		return err
	}
	if slices.Contains(existingSubjects, wildcard) {
		return conn.EnsureStream(ctx, substrate.StreamSpec{
			Name:                      stream,
			Subjects:                  existingSubjects,
			MaxAge:                    syncStreamMaxAge,
			MaxMsgsPerSubject:         syncStreamMaxMsgsPerSubject,
			MaxBytes:                  syncStreamMaxBytes,
			ConsumerInactiveThreshold: SyncConsumerInactiveThreshold,
			ConsumerMaxAckPending:     SyncConsumerMaxAckPending,
		})
	}
	return conn.EnsureStream(ctx, substrate.StreamSpec{
		Name:                      stream,
		Subjects:                  append(existingSubjects, wildcard),
		MaxAge:                    syncStreamMaxAge,
		MaxMsgsPerSubject:         syncStreamMaxMsgsPerSubject,
		MaxBytes:                  syncStreamMaxBytes,
		ConsumerInactiveThreshold: SyncConsumerInactiveThreshold,
		ConsumerMaxAckPending:     SyncConsumerMaxAckPending,
	})
}

// existingStreamSubjects returns stream's currently configured Subjects, or
// nil if the stream does not yet exist.
func existingStreamSubjects(ctx context.Context, conn *substrate.Conn, stream string) ([]string, error) {
	s, err := conn.JetStream().Stream(ctx, stream)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("look up stream %q: %w", stream, err)
	}
	return s.CachedInfo().Config.Subjects, nil
}

func containsField(fields []string, target string) bool {
	for _, f := range fields {
		if f == target {
			return true
		}
	}
	return false
}

// resolveActor extracts the recipient identity from keys[PersonalActorKeyField].
// It fails closed with an error (rather than reaching subjects.PersonalSync's
// panic-on-invalid-token) on a non-string or subject-unsafe value: unlike the
// other subjects-package callers (a lensID/nodeID, a static platform-chosen
// string), this value is untrusted, cypher-projected business data — a
// malformed row must fail that one Upsert/Delete, not crash the pipeline.
func resolveActor(keys map[string]any) (string, error) {
	val, ok := keys[PersonalActorKeyField]
	if !ok {
		return "", fmt.Errorf("key field %q absent from keys map", PersonalActorKeyField)
	}
	actor, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("key field %q must be a string, got %T", PersonalActorKeyField, val)
	}
	if actor == "" {
		return "", fmt.Errorf("key field %q is empty", PersonalActorKeyField)
	}
	if strings.ContainsAny(actor, ".*> \t\n\r") {
		return "", fmt.Errorf("key field %q contains a character invalid in a subject token: %q", PersonalActorKeyField, actor)
	}
	return actor, nil
}

// buildKey concatenates the non-actor key fields in keyOrder order, joined
// with "." — the envelope's `key` field (mirrors NatsKVAdapter.buildKey).
func (a *NatsSubjectAdapter) buildKey(keys map[string]any) (string, error) {
	parts := make([]string, 0, len(a.keyOrder))
	for _, field := range a.keyOrder {
		if field == PersonalActorKeyField {
			continue
		}
		val, ok := keys[field]
		if !ok {
			return "", fmt.Errorf("key field %q absent from keys map", field)
		}
		parts = append(parts, fmt.Sprintf("%v", val))
	}
	return strings.Join(parts, "."), nil
}

// splitEnvelopeRow separates row into the reserved envelope metadata fields
// (anchor/kind/class, when a lens's RETURN clause supplies them) and the
// remaining business columns, which become the envelope's Data.
func splitEnvelopeRow(row map[string]any) (anchor, kind, class string, data map[string]any) {
	data = make(map[string]any, len(row))
	for k, v := range row {
		switch k {
		case rowFieldAnchor:
			anchor, _ = v.(string)
		case rowFieldKind:
			kind, _ = v.(string)
		case rowFieldClass:
			class, _ = v.(string)
		default:
			data[k] = v
		}
	}
	if len(data) == 0 {
		// nil (not an empty map) so json's `omitempty` drops the field —
		// matching Delete's envelope, which never sets Data at all. A
		// non-nil empty map would instead marshal as "data":{}, a
		// wire-visible inconsistency for a row that carries only reserved
		// metadata fields and no business columns.
		data = nil
	}
	return anchor, kind, class, data
}

// Upsert publishes an "upsert" delta envelope to the actor's subject. Personal
// Lens has no SecureDecryptor (Fire 5, personal-secure-lens-design.md §3.6 —
// the cloud never decrypts for the Edge): a sensitive aspect's data reaches
// this row exactly as Core KV stores it, so Encrypted is set from whether any
// Data field is shaped like a Vault ciphertext envelope, not decoded or
// altered.
func (a *NatsSubjectAdapter) Upsert(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) error {
	actor, err := resolveActor(keys)
	if err != nil {
		return fmt.Errorf("natssubject upsert: %w", err)
	}
	key, err := a.buildKey(keys)
	if err != nil {
		return fmt.Errorf("natssubject upsert: %w", err)
	}
	anchor, kind, class, data := splitEnvelopeRow(row)
	env := deltaEnvelope{
		Op:            "upsert",
		Key:           key,
		Anchor:        anchor,
		Kind:          kind,
		Class:         class,
		Revision:      projectionSeq,
		ProjectionSeq: projectionSeq,
		Encrypted:     rowHasCiphertext(data),
		Data:          data,
		Lens:          a.ruleID,
	}
	return a.publish(ctx, actor, env)
}

// PublishKeySet publishes a "keyset" frame to actorID's subject: the
// complete, authoritative set of keys this adapter's lens currently
// projects for that actor, as of revision (personal-lens-retraction-
// design.md §3.1-3.2, R1). Each entry in keys is rendered through the same
// buildKey derivation Upsert/Delete use, so the client diffs directly
// against the key strings it already stores. A nil/empty keys is a valid,
// meaningful frame — the last-row-retraction / missing-actor case — and is
// published exactly like a non-empty one; only Op=="keyset" gates the
// client's interpretation, not the field's presence.
func (a *NatsSubjectAdapter) PublishKeySet(ctx context.Context, actorID string, keys []map[string]any, revision uint64) error {
	keyStrs := make([]string, 0, len(keys))
	for _, k := range keys {
		ks, err := a.buildKey(k)
		if err != nil {
			return fmt.Errorf("natssubject keyset: %w", err)
		}
		keyStrs = append(keyStrs, ks)
	}
	env := deltaEnvelope{
		Op:            "keyset",
		Lens:          a.ruleID,
		Keys:          keyStrs,
		Revision:      revision,
		ProjectionSeq: revision,
	}
	return a.publish(ctx, actorID, env)
}

// rowHasCiphertext reports whether any of data's values is shaped like a
// Vault sensitive-aspect ciphertext envelope ({ct,nonce,keyId} — Contract #3
// §3.10, the same shape pipeline.ciphertextFromMap parses at the Secure
// Lens's decrypt-at-projection surface). Personal Lens never decodes or
// decrypts it — this only flags the envelope so the Edge knows to fetch a
// transient session key (Vault Fire 5) before it can read the field.
// Requires all three of CT/Nonce/KeyID non-empty (not just a non-empty CT):
// json.Unmarshal silently ignores unrecognized/missing fields, so a plain
// business field that merely happens to be named "ct" would otherwise
// false-positive; a real envelope always carries all three.
func rowHasCiphertext(data map[string]any) bool {
	for _, v := range data {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		raw, err := json.Marshal(m)
		if err != nil {
			continue
		}
		var ct vault.Ciphertext
		if err := json.Unmarshal(raw, &ct); err != nil {
			continue
		}
		if len(ct.CT) > 0 && len(ct.Nonce) > 0 && ct.KeyID != "" {
			return true
		}
	}
	return false
}

// Delete publishes a "delete" delta envelope (key + tombstone, no body) to
// the actor's subject.
func (a *NatsSubjectAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	actor, err := resolveActor(keys)
	if err != nil {
		return fmt.Errorf("natssubject delete: %w", err)
	}
	key, err := a.buildKey(keys)
	if err != nil {
		return fmt.Errorf("natssubject delete: %w", err)
	}
	env := deltaEnvelope{
		Op:            "delete",
		Key:           key,
		Revision:      projectionSeq,
		ProjectionSeq: projectionSeq,
	}
	return a.publish(ctx, actor, env)
}

// publish is the one wire seam every envelope kind takes — upsert, delete,
// keyset and hydrationComplete alike. When ctx carries a publish pipeline
// (WithPublishPipeline) the envelope joins it and the store ack is awaited by
// whoever flushes that pipeline; otherwise the publish awaits its own ack.
// Which one a given envelope gets is entirely the caller's choice of context:
// an envelope that must be durable before the next step — the keyset frame —
// is simply published under a context carrying no pipeline.
func (a *NatsSubjectAdapter) publish(ctx context.Context, actor string, env deltaEnvelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("natssubject: marshal envelope: %w", err)
	}
	subject := subjects.PersonalSync(a.subjectPrefix, actor)
	if pipe := publishPipelineFrom(ctx); pipe != nil {
		if err := pipe.Add(ctx, subject, data, nil); err != nil {
			return fmt.Errorf("natssubject: publish %s: %w", subject, err)
		}
		return nil
	}
	if err := a.conn.Publish(ctx, subject, data, nil); err != nil {
		return fmt.Errorf("natssubject: publish %s: %w", subject, err)
	}
	return nil
}

// NewPublishPipeline opens a publish pipeline on the connection this adapter
// publishes through, satisfying PublishPipelineOpener. The caller installs it
// on the context of its write loop with WithPublishPipeline and flushes it
// before anything that depends on those rows being stored.
func (a *NatsSubjectAdapter) NewPublishPipeline() *substrate.PublishPipeline {
	return a.conn.NewPublishPipeline(0)
}

// PublishHydrationComplete publishes a terminal "hydrationComplete" marker to
// actorID's subject, carrying the high-water revision (personal-secure-lens-
// design.md §3.5, Fire PL.4). The device's Sync Manager reverts to
// incremental delivery from this revision once it observes the marker. No
// Key/Data — the marker carries only the revision.
func (a *NatsSubjectAdapter) PublishHydrationComplete(ctx context.Context, actorID string, revision uint64) error {
	env := deltaEnvelope{
		Op:            "hydrationComplete",
		Revision:      revision,
		ProjectionSeq: revision,
	}
	return a.publish(ctx, actorID, env)
}

// Probe checks whether the backing JetStream stream is reachable.
func (a *NatsSubjectAdapter) Probe(ctx context.Context) error {
	if _, err := a.conn.JetStream().Stream(ctx, a.stream); err != nil {
		return fmt.Errorf("natssubject: probe stream %q: %w", a.stream, err)
	}
	return nil
}

// Close is a no-op; the underlying NATS connection's lifecycle is managed
// by the caller.
func (a *NatsSubjectAdapter) Close() error {
	return nil
}
