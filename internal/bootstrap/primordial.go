// Package bootstrap implements the primordial seeding sequence for Story 1.3.
// All primordial writes go directly to Core KV — the sole sanctioned non-Processor
// write path (Contract #7 §7.1, handoff brief decision #1).
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

const (
	// KV bucket names.
	CoreKVBucket           = "core-kv"
	HealthKVBucket         = "health-kv"
	CapabilityKVBucket     = "capability-kv"
	WeaverStateBucket      = "weaver-state"
	WeaverClaimsBucket     = "weaver-claims"
	RefractorAdjacencyKV   = "refractor-adjacency" // Story 2.1: Refractor's internal adjacency store (private, not a Lens target)

	// JetStream stream names.
	CoreOpsStreamName    = "core-operations"
	OpsMetaStreamName    = "ops-meta"
	CoreEventsStreamName = "core-events"

	// JetStream subjects. Per Contract #2 §2.3, lane subjects are
	// `ops.<lane>.>` (multi-segment). The `ops.>` wildcard covers all
	// lanes including future ones. Story 1.5's CONTRACT-AMENDMENT-REQUEST
	// flagged the old single-segment `ops.*` pattern as inconsistent with
	// Contract #2; this resolves it.
	OpsWildcardSubject    = "ops.>"
	OpsMetaSubject        = "ops.meta.>" // retained for explicit meta-stream documentation
	EventsWildcardSubject = "events.>"   // Story 1.8: Processor step-9 event fan-out
)

// Seeder holds the NATS JetStream context and performs all primordial writes.
type Seeder struct {
	js     jetstream.JetStream
	nc     *nats.Conn
	logger *slog.Logger
}

// NewSeeder creates a Seeder connected to the given NATS connection.
func NewSeeder(nc *nats.Conn, logger *slog.Logger) (*Seeder, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("jetstream.New: %w", err)
	}
	return &Seeder{js: js, nc: nc, logger: logger}, nil
}

// ProvisionBuckets creates all required KV buckets and JetStream streams.
// Re-running is idempotent: existing buckets are left unchanged.
func (s *Seeder) ProvisionBuckets(ctx context.Context) error {
	buckets := []struct {
		name        string
		description string
		ttl         bool
	}{
		{CoreKVBucket, "Lattice Core KV — primary graph store", true},
		{HealthKVBucket, "Lattice Health KV — component heartbeats", true},
		{CapabilityKVBucket, "Lattice Capability KV — Refractor projection targets", true},
		{WeaverStateBucket, "Lattice Weaver State KV", true},
		{WeaverClaimsBucket, "Lattice Weaver Claims KV", true},
		{RefractorAdjacencyKV, "Refractor internal adjacency store (private)", false},
	}

	for _, b := range buckets {
		cfg := jetstream.KeyValueConfig{
			Bucket:      b.name,
			Description: b.description,
			// MaxValueSize: -1 (unlimited)
			// History: 1 (default)
		}
		if b.ttl {
			// LimitMarkerTTL → AllowMsgTTL: true on the underlying stream.
			// Required for per-key TTL support per Contract #4 §4.3 and
			// Story 1.1 spike finding (nats-batch README).
			// NATS requires LimitMarkerTTL >= 1 second.
			cfg.LimitMarkerTTL = 1 * time.Second
		}

		kv, err := s.js.CreateOrUpdateKeyValue(ctx, cfg)
		if err != nil {
			return fmt.Errorf("create/update KV bucket %q: %w", b.name, err)
		}
		s.logger.Info("KV bucket ready", "bucket", kv.Bucket())

		// For Core KV: also set AllowAtomicPublish: true on the underlying stream.
		// Per Story 1.1 spike: CreateKeyValue does NOT set this automatically.
		// We must UpdateStream after KV creation (handoff brief decision #5).
		if b.name == CoreKVBucket {
			if err := s.enableAtomicPublish(ctx, CoreKVBucket); err != nil {
				return fmt.Errorf("enable AtomicPublish on %q: %w", CoreKVBucket, err)
			}
		}
	}

	// Provision core-operations and ops.meta streams.
	if err := s.provisionStreams(ctx); err != nil {
		return fmt.Errorf("provision streams: %w", err)
	}
	return nil
}

// enableAtomicPublish sets AllowAtomicPublish: true on the KV's underlying stream.
// The stream name for a KV bucket is "KV_<bucket>".
func (s *Seeder) enableAtomicPublish(ctx context.Context, bucket string) error {
	streamName := "KV_" + bucket
	stream, err := s.js.Stream(ctx, streamName)
	if err != nil {
		return fmt.Errorf("get stream %q: %w", streamName, err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return fmt.Errorf("stream info %q: %w", streamName, err)
	}
	cfg := info.Config
	cfg.AllowAtomicPublish = true
	_, err = s.js.UpdateStream(ctx, cfg)
	if err != nil {
		return fmt.Errorf("update stream %q AllowAtomicPublish: %w", streamName, err)
	}
	s.logger.Info("AllowAtomicPublish enabled", "stream", streamName)
	return nil
}

// provisionStreams creates the required JetStream streams (not KV).
func (s *Seeder) provisionStreams(ctx context.Context) error {
	streams := []jetstream.StreamConfig{
		{
			Name:        CoreOpsStreamName,
			Description: "Core operations stream — Processor consumes from here",
			Subjects:    []string{OpsWildcardSubject}, // "ops.>" covers all lanes including "ops.meta.>" per Contract #2 §2.3
		},
		{
			// Story 1.8: Processor step-9 event fan-out. Events are
			// short-lived per Contract #3 lifetime norms; 7-day MaxAge is
			// the Phase 1 default. AllowAtomicPublish enables the
			// substrate.PublishBatch step-9 path (sequential-with-retry
			// batch publish — see internal/processor/step9_publish.go).
			Name:               CoreEventsStreamName,
			Description:        "Core events stream — Processor publishes business events here at step 9",
			Subjects:           []string{EventsWildcardSubject},
			Retention:          jetstream.LimitsPolicy,
			Storage:            jetstream.FileStorage,
			MaxAge:             7 * 24 * time.Hour,
			AllowAtomicPublish: true,
		},
	}
	for _, sc := range streams {
		_, err := s.js.CreateOrUpdateStream(ctx, sc)
		if err != nil {
			return fmt.Errorf("create/update stream %q: %w", sc.Name, err)
		}
		s.logger.Info("JetStream stream ready", "stream", sc.Name)
	}
	return nil
}

// SeedPrimordial writes all primordial Core KV entries per Contract #7 §7.2.
// Order per §7.7: op tracker → identities → meta DDLs → Lens definitions → roles → permissions → links.
//
// Story 1.4 refactor: this method now uses substrate.AtomicBatch for the
// initial write of the full primordial set, replacing the prior
// sequential-create loop. Atomic semantics make partial-failure recovery
// unambiguous (either the entire primordial set lands or none of it does).
// The idempotent re-run path (Contract #7 §7.4) is preserved: if the
// bootstrap op tracker key already exists in Core KV, the function returns
// without re-issuing the batch.
func (s *Seeder) SeedPrimordial(ctx context.Context) error {
	kv, err := s.js.KeyValue(ctx, CoreKVBucket)
	if err != nil {
		return fmt.Errorf("open Core KV: %w", err)
	}

	// Idempotent re-run guard: if the op tracker already exists, the
	// primordial set has previously committed. Skip the whole batch.
	if _, err := kv.Get(ctx, BootstrapOpKey); err == nil {
		s.logger.Info("primordial set already present — skipping batch", "key", BootstrapOpKey)
		return nil
	} else if !errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("probe op tracker key: %w", err)
	}

	entries, err := buildPrimordialEntries()
	if err != nil {
		return fmt.Errorf("build primordial entries: %w", err)
	}

	conn, err := substrate.Wrap(s.nc)
	if err != nil {
		return fmt.Errorf("substrate wrap: %w", err)
	}

	ops := make([]substrate.BatchOp, 0, len(entries))
	for _, e := range entries {
		ops = append(ops, substrate.BatchOp{
			Bucket:     CoreKVBucket,
			Key:        e.key,
			Value:      e.value,
			CreateOnly: true, // primordial keys must not pre-exist
		})
	}

	ack, err := conn.AtomicBatch(ops, 10*time.Second)
	if err != nil {
		// If the batch was rejected because a key already exists (e.g., a
		// concurrent bootstrapper raced us), fall back to the idempotent
		// per-key check. This protects re-run safety while keeping the
		// happy path single-batch.
		if errors.Is(err, substrate.ErrAtomicBatchRejected) && looksLikeCreateConflict(err) {
			s.logger.Info("atomic batch rejected (likely concurrent bootstrap) — falling back to per-key idempotent path",
				"error", err)
			return s.seedPrimordialPerKey(ctx, kv, entries)
		}
		return fmt.Errorf("primordial atomic batch: %w", err)
	}
	s.logger.Info("primordial atomic batch committed",
		"count", ack.Count, "stream", ack.Stream, "seq", ack.Sequence, "batchID", ack.BatchID)
	return nil
}

// seedPrimordialPerKey is the legacy sequential seeding path retained as a
// concurrent-bootstrap fallback. Pre-refactor behavior — kept verbatim.
func (s *Seeder) seedPrimordialPerKey(ctx context.Context, kv jetstream.KeyValue, entries []kvEntry) error {
	for _, e := range entries {
		if _, getErr := kv.Get(ctx, e.key); getErr == nil {
			s.logger.Info("key already exists, skipping", "key", e.key)
			continue
		}
		if _, putErr := kv.Create(ctx, e.key, e.value); putErr != nil {
			if strings.Contains(putErr.Error(), "wrong last sequence") ||
				strings.Contains(putErr.Error(), "key exists") {
				s.logger.Info("key created concurrently, skipping", "key", e.key)
				continue
			}
			return fmt.Errorf("seed key %q: %w", e.key, putErr)
		}
		s.logger.Info("seeded primordial key", "key", e.key)
	}
	return nil
}

func looksLikeCreateConflict(err error) bool {
	s := err.Error()
	return strings.Contains(s, "wrong last sequence") ||
		strings.Contains(s, "key exists") ||
		strings.Contains(s, "10071")
}

type kvEntry struct {
	key   string
	value []byte
}

// buildPrimordialEntries assembles all primordial KV entries in seeding order.
func buildPrimordialEntries() ([]kvEntry, error) {
	var entries []kvEntry

	// add appends an entry; callers split the multi-return envelope functions.
	add := func(key string, val []byte, err error) error {
		if err != nil {
			return fmt.Errorf("build entry %q: %w", key, err)
		}
		entries = append(entries, kvEntry{key: key, value: val})
		return nil
	}

	// 1. Bootstrap op tracker — self-referential provenance.
	opVal, opErr := MakeBootstrapOpEnvelope()
	if err := add(BootstrapOpKey, opVal, opErr); err != nil {
		return nil, err
	}

	// 2. Bootstrap identity vertex.
	bsIdVal, bsIdErr := MakeVertexEnvelope(BootstrapIdentityKey, "identity.system.bootstrap",
		map[string]any{"note": "Primordial bootstrap identity. Authors all primordial provenance fields."})
	if err := add(BootstrapIdentityKey, bsIdVal, bsIdErr); err != nil {
		return nil, err
	}

	// 3. Platform actor identity vertex.
	platVal, platErr := MakeVertexEnvelope(PlatformActorKey, "identity.system.platform",
		map[string]any{"note": "Internal platform service actor identity."})
	if err := add(PlatformActorKey, platVal, platErr); err != nil {
		return nil, err
	}

	// 4. Root DDL meta-vertex (vtx.meta.root — the meta-meta root per AC).
	rootVal, rootErr := MakeVertexEnvelope(MetaRootKey, "meta.ddl.root",
		map[string]any{"note": "Root meta-DDL vertex. Anchors the meta-meta layer."})
	if err := add(MetaRootKey, rootVal, rootErr); err != nil {
		return nil, err
	}
	// canonicalName aspect for the root DDL vertex.
	rootCanonicalAspectKey := MetaRootKey + ".canonicalName"
	rca, rcaErr := MakeAspectEnvelope(rootCanonicalAspectKey, MetaRootKey, "canonicalName", "canonicalName",
		map[string]any{"value": "meta.ddl.root"})
	if err := add(rootCanonicalAspectKey, rca, rcaErr); err != nil {
		return nil, err
	}
	// description aspect.
	rootDescAspectKey := MetaRootKey + ".description"
	rda, rdaErr := MakeAspectEnvelope(rootDescAspectKey, MetaRootKey, "description", "description",
		map[string]any{"text": "Root DDL meta-vertex anchoring the Lattice meta-meta layer."})
	if err := add(rootDescAspectKey, rda, rdaErr); err != nil {
		return nil, err
	}

	// 5. Capability Lens definition.
	capLens := CapabilityLensDefinition()
	capLensVal, capLensErr := MakeVertexEnvelope(CapabilityLensKey, "meta.lens", map[string]any{})
	if err := add(CapabilityLensKey, capLensVal, capLensErr); err != nil {
		return nil, err
	}
	if err := addLensAspects(&entries, CapabilityLensKey, capLens); err != nil {
		return nil, err
	}

	// 6. Capability role-index Lens definition.
	roleIdxLens := CapabilityRoleIndexLensDefinition()
	roleIdxLensVal, roleIdxLensErr := MakeVertexEnvelope(CapabilityRoleIndexLensKey, "meta.lens", map[string]any{})
	if err := add(CapabilityRoleIndexLensKey, roleIdxLensVal, roleIdxLensErr); err != nil {
		return nil, err
	}
	if err := addLensAspects(&entries, CapabilityRoleIndexLensKey, roleIdxLens); err != nil {
		return nil, err
	}

	// 7. Five canonical role vertices.
	for _, role := range CanonicalRoles() {
		roleVal, roleErr := MakeVertexEnvelope(role.Key, "role", map[string]any{})
		if err := add(role.Key, roleVal, roleErr); err != nil {
			return nil, err
		}
		// description aspect on each role.
		descKey := role.Key + ".description"
		descVal, descErr := MakeAspectEnvelope(descKey, role.Key, "description", "description",
			map[string]any{"text": role.Description})
		if err := add(descKey, descVal, descErr); err != nil {
			return nil, err
		}
	}

	// 8. Permission vertex for platformInternal role.
	permData := PlatformInternalPermission()
	permVal, permErr := MakeVertexEnvelope(PermPlatformAnyKey, "permission", permData)
	if err := add(PermPlatformAnyKey, permVal, permErr); err != nil {
		return nil, err
	}

	// 9. Topology links.
	// bootstrap identity → holdsRole → platformInternal role.
	bsHoldsVal, bsHoldsErr := MakeLinkEnvelope(
		BootstrapHoldsRoleLinkKey,
		"vtx.identity."+BootstrapIdentityID,
		"vtx.role."+RolePlatformIntlID,
		"holdsRole", "holdsRole", map[string]any{})
	if err := add(BootstrapHoldsRoleLinkKey, bsHoldsVal, bsHoldsErr); err != nil {
		return nil, err
	}

	// platform actor → holdsRole → platformInternal role.
	platHoldsVal, platHoldsErr := MakeLinkEnvelope(
		PlatformHoldsRoleLinkKey,
		"vtx.identity."+PlatformActorID,
		"vtx.role."+RolePlatformIntlID,
		"holdsRole", "holdsRole", map[string]any{})
	if err := add(PlatformHoldsRoleLinkKey, platHoldsVal, platHoldsErr); err != nil {
		return nil, err
	}

	// permission vertex → grantsPermission → platformInternal role.
	// Permission (bspermPlatformAnyV11j) > role (bsrolePlatformIntlV10h) alphabetically
	// → permission is the "younger" vertex (first three segments of link key).
	grantsVal, grantsErr := MakeLinkEnvelope(
		GrantsPermissionLinkKey,
		"vtx.permission."+PermPlatformAnyID,
		"vtx.role."+RolePlatformIntlID,
		"grantsPermission", "grantsPermission", map[string]any{})
	if err := add(GrantsPermissionLinkKey, grantsVal, grantsErr); err != nil {
		return nil, err
	}

	return entries, nil
}

// addLensAspects appends aspect entries for a Lens definition vertex.
func addLensAspects(entries *[]kvEntry, lensKey string, def LensDefinition) error {
	aspects := []struct {
		localName string
		class     string
		data      any
	}{
		{"canonicalName", "canonicalName", map[string]any{"value": def.CanonicalName}},
		{"targetBucket", "targetBucket", map[string]any{"value": def.TargetBucket, "adapter": "nats-kv"}},
		{"cypherRule", "cypherRule", map[string]any{"rule": strings.TrimSpace(def.CypherRule)}},
		{"outputSchema", "outputSchema", map[string]any{"jsonSchema": json.RawMessage(def.OutputSchema)}},
	}
	for _, a := range aspects {
		key := lensKey + "." + a.localName
		val, err := MakeAspectEnvelope(key, lensKey, a.localName, a.class, a.data)
		if err != nil {
			return fmt.Errorf("build lens aspect %q: %w", key, err)
		}
		*entries = append(*entries, kvEntry{key: key, value: val})
	}
	return nil
}

// MarkBootstrapComplete writes the `health.bootstrap.complete` marker
// to the Health KV bucket. Historically this was refractor-stub's job;
// after Story 2.1 deleted refractor-stub (MORPH-DEVIATIONS Deviation 9)
// the marker write moved to cmd/bootstrap itself — see Story 2.1b
// housekeeping.
//
// The marker value is a tiny JSON blob with a wall-clock timestamp so
// operators can read it via `nats kv get health-kv health.bootstrap.complete`.
func MarkBootstrapComplete(ctx context.Context, nc *nats.Conn, logger *slog.Logger) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jetstream.New: %w", err)
	}
	kv, err := js.KeyValue(ctx, HealthKVBucket)
	if err != nil {
		return fmt.Errorf("open Health KV: %w", err)
	}
	payload := fmt.Sprintf(`{"completedAt":%q,"writer":"cmd/bootstrap"}`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if _, err := kv.Put(ctx, HealthBootstrapCompleteKey, []byte(payload)); err != nil {
		return fmt.Errorf("put %s: %w", HealthBootstrapCompleteKey, err)
	}
	logger.Info("bootstrap readiness marker written", "key", HealthBootstrapCompleteKey)
	return nil
}

// WaitForBootstrapComplete polls Health KV until health.bootstrap.complete is present
// or ctx is cancelled.
func WaitForBootstrapComplete(ctx context.Context, nc *nats.Conn, logger *slog.Logger) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jetstream.New: %w", err)
	}
	kv, err := js.KeyValue(ctx, HealthKVBucket)
	if err != nil {
		return fmt.Errorf("open Health KV: %w", err)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness gate timed out: %w", ctx.Err())
		case <-ticker.C:
			_, err := kv.Get(ctx, HealthBootstrapCompleteKey)
			if err == nil {
				logger.Info("readiness gate satisfied", "key", HealthBootstrapCompleteKey)
				return nil
			}
			logger.Debug("waiting for readiness gate", "key", HealthBootstrapCompleteKey)
		}
	}
}
