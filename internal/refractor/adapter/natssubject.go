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

	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
)

var _ Adapter = (*NatsSubjectAdapter)(nil)

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
	Op            string         `json:"op"` // "upsert" | "delete"
	Key           string         `json:"key"`
	Anchor        string         `json:"anchor,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	Class         string         `json:"class,omitempty"`
	Revision      uint64         `json:"revision"`
	ProjectionSeq uint64         `json:"projectionSeq"`
	Encrypted     bool           `json:"encrypted"`
	Data          map[string]any `json:"data,omitempty"`
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
	subjectPrefix string
	stream        string
	keyOrder      []string // full Into.Key order, including PersonalActorKeyField
}

// NewNatsSubjectAdapter creates a NatsSubjectAdapter and ensures the backing
// JetStream stream exists (idempotent — safe on every process startup,
// mirrors the nats_kv case's JIT bucket creation in cmd/refractor).
// keyOrder must include PersonalActorKeyField exactly once; the platform's
// NanoID alphabet carries no dots, so the reserved field's value is a safe
// single subject token (subjects.PersonalSync validates it defensively).
func NewNatsSubjectAdapter(ctx context.Context, conn *substrate.Conn, subjectPrefix, stream string, keyOrder []string) (*NatsSubjectAdapter, error) {
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
	return &NatsSubjectAdapter{conn: conn, subjectPrefix: subjectPrefix, stream: stream, keyOrder: keyOrder}, nil
}

// syncStreamMaxAge bounds the SYNC stream's retention (personal-secure-lens-
// design.md §3.2: "short retention ... the vault's 'ephemerality' property").
// A node that falls behind this window re-hydrates from a cold
// personal.hydrate call instead of replaying a long backlog.
const syncStreamMaxAge = 24 * time.Hour

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
		return conn.EnsureStream(ctx, substrate.StreamSpec{Name: stream, Subjects: existingSubjects, MaxAge: syncStreamMaxAge})
	}
	return conn.EnsureStream(ctx, substrate.StreamSpec{
		Name:     stream,
		Subjects: append(existingSubjects, wildcard),
		MaxAge:   syncStreamMaxAge,
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
	}
	return a.publish(ctx, actor, env)
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

// SkipsAdjWatchWrite reports that the adjacency-watch reprojection path must
// NOT publish its sentinel-seq (0) deltas to this adapter. Keyed on the
// transport, not on personal-ness: every "nats_subject" lens (both the Fire 2
// personal fan-out and the PL.1 direct shape) feeds the Edge SYNC consumer,
// an untrusted device whose LWW gate orders by revision and applies-on-equal,
// so a Revision==0 delta arrives ordered-by-arrival and a redelivered/
// reordered rev-0 upsert can transiently resurrect a rev-0 tombstone or
// vice-versa (edge-lattice-full-design.md §8.1 RR-1). The write is redundant
// regardless: the adjacency reprojection carries no stream sequence, and the
// same link/aspect CDC event flows through the stream consumer's
// evalLinkFanOut/evalAspectFanOut, reprojecting the same keys with the
// triggering message's real sequence (the same pipeline-general property the
// guarded-adapter seq-0 skip relies on, pipeline.handleAdjNode) — so this is
// dropped, not downgraded. The Edge must never receive an unordered delta.
func (a *NatsSubjectAdapter) SkipsAdjWatchWrite() bool { return true }

func (a *NatsSubjectAdapter) publish(ctx context.Context, actor string, env deltaEnvelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("natssubject: marshal envelope: %w", err)
	}
	subject := subjects.PersonalSync(a.subjectPrefix, actor)
	if err := a.conn.Publish(ctx, subject, data, nil); err != nil {
		return fmt.Errorf("natssubject: publish %s: %w", subject, err)
	}
	return nil
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
