// Package bootstrap implements the Lattice primordial seeding sequence.
// All primordial writes go directly to Core KV — the sole sanctioned non-Processor
// write path (Contract #7 §7.1).
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

	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	// KV bucket names.
	CoreKVBucket        = "core-kv"
	HealthKVBucket      = "health-kv"
	CapabilityKVBucket  = "capability-kv"
	WeaverStateBucket   = "weaver-state"
	LoomStateBucket     = "loom-state"     // Loom's per-instance cursor store (Contract #10 §10.3)
	WeaverTargetsBucket = "weaver-targets" // shared target-Lens projection bucket (Contract #10 §10.2)
	// OrchestrationHistoryBucket is the Chronicler's durable Loom-flow history
	// read model (orchestration-history-read-model-design.md §2.6) — an
	// eventStream lens target (Refractor projects `events.loom.>` into it),
	// not Core KV. Primordial like WeaverTargetsBucket/LoomStateBucket: a
	// durable read-model bucket every deployment needs, not a per-install
	// package artifact.
	OrchestrationHistoryBucket = "orchestration-history"
	RefractorAdjacencyKV       = "refractor-adjacency" // Refractor's internal adjacency store (private, not a Lens target)
	// PersonalLensInterestKV is the Refractor's per-device Personal Lens
	// Interest Set registry (personal-secure-lens-design.md §3.3, Fire PL.2):
	// operational subscription state, not business truth (P1) — keyed
	// `<identityId>.<deviceId>`, written only by the Refractor's own
	// personal.register/.deregister control RPCs, never a Lens target.
	PersonalLensInterestKV = "personal-lens-interest"
	// GatewayRevocationBucket is the Gateway's token-revocation kill-switch set
	// (must match internal/gateway/revocation.BucketName by value — bootstrap
	// does not import the gateway package). Materialized by the Gateway's own
	// events.gateway.> consumer, not a Lens target.
	GatewayRevocationBucket = "token-revocation"
	// GatewayCredentialBindingsBucket is the Gateway's credential→identity
	// resolution set (must match internal/gateway/credentialbinding.BucketName
	// by value — bootstrap does not import the gateway package). Materialized
	// by the Gateway's own events.identity.> consumer, not a Lens target
	// (gateway-claim-flow-identity-provisioning-design.md §11.0/§11.5 R1).
	GatewayCredentialBindingsBucket = "credential-bindings"

	// CoreObjectsBucket is the off-graph binary blob store — a JetStream Object
	// Store (backed by stream OBJ_core-objects), NOT a KV bucket. It is the
	// third sanctioned write plane (bytes), parallel to Health KV being a third
	// state plane (Decision #4): trusted clients (Loupe) stream blob bytes here
	// directly and the bytes never enter the Processor/CDC path. Provisioned as
	// primordial substrate because pkgmgr writes Core-KV manifests only and has
	// no path to provision an Object Store.
	CoreObjectsBucket = "core-objects"

	// JetStream stream names.
	CoreOpsStreamName       = "core-operations"
	CoreEventsStreamName    = "core-events"
	CoreSchedulesStreamName = "core-schedules"

	// JetStream subjects. Per Contract #2 §2.3, lane subjects are
	// `ops.<lane>.>` (multi-segment). The `ops.>` wildcard covers all
	// lanes including future ones — including `ops.meta.>` (the meta lane).
	OpsWildcardSubject       = "ops.>"
	EventsWildcardSubject    = "events.>"
	SchedulesWildcardSubject = "schedule.>"
)

// CoreObjectsMaxBytes is the store-level byte cap on core-objects (5 GiB
// default for the single-cell MVP). It is distinct from the per-upload cap
// (OBJECTS_MAX_UPLOAD_BYTES, enforced inside substrate.ObjectPut): this bounds
// the entire store, that bounds one blob.
const CoreObjectsMaxBytes int64 = 5 << 30

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
	for _, b := range PlatformBuckets() {
		cfg := jetstream.KeyValueConfig{
			Bucket:      b.Name,
			Description: b.Description,
			// MaxValueSize: -1 (unlimited)
			// History: 1 (default)
		}
		if b.PerKeyTTL {
			// LimitMarkerTTL enables per-key TTL support (Contract #4 §4.3).
			// Enables AllowMsgTTL on the underlying stream.
			// NATS requires LimitMarkerTTL >= 1 second.
			cfg.LimitMarkerTTL = 1 * time.Second
		}

		kv, err := s.js.CreateOrUpdateKeyValue(ctx, cfg)
		if err != nil {
			return fmt.Errorf("create/update KV bucket %q: %w", b.Name, err)
		}
		s.logger.Info("KV bucket ready", "bucket", kv.Bucket())

		// AllowAtomicPublish must be set on the underlying stream for buckets
		// whose writers use Conn.AtomicBatch: Core KV (the Processor's commit
		// batch + tracker) and loom-state (Loom's per-transition cursor + token
		// batch, Contract #10 §10.3). CreateKeyValue does NOT set this
		// automatically; UpdateStream is required.
		if b.Name == CoreKVBucket || b.Name == LoomStateBucket {
			if err := s.enableAtomicPublish(ctx, b.Name); err != nil {
				return fmt.Errorf("enable AtomicPublish on %q: %w", b.Name, err)
			}
		}
	}

	// Provision the core-objects Object Store — the off-graph blob plane. It is
	// a JetStream Object Store (stream OBJ_core-objects), not a KV bucket, so it
	// is provisioned separately from the bucket loop. Idempotent
	// (CreateOrUpdate), like the buckets above.
	if _, err := s.js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:      CoreObjectsBucket,
		Description: "Lattice Core Objects — off-graph binary blob store",
		Storage:     jetstream.FileStorage,
		MaxBytes:    CoreObjectsMaxBytes,
	}); err != nil {
		return fmt.Errorf("create/update object store %q: %w", CoreObjectsBucket, err)
	}
	s.logger.Info("Object Store ready", "bucket", CoreObjectsBucket)

	// Provision core-operations, core-events, and core-schedules streams.
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
			// Events are short-lived per Contract #3 lifetime norms; 7-day
			// MaxAge is the Phase 1 default. AllowAtomicPublish enables the
			// substrate.PublishBatch outbox path (see
			// internal/processor/outbox/publisher.go).
			Name:               CoreEventsStreamName,
			Description:        "Core events stream — the Processor's outbox consumer publishes business events here",
			Subjects:           []string{EventsWildcardSubject},
			Retention:          jetstream.LimitsPolicy,
			Storage:            jetstream.FileStorage,
			MaxAge:             7 * 24 * time.Hour,
			AllowAtomicPublish: true,
		},
		{
			// Contract #10 §10.4 (FROZEN 2026-06-02). AllowMsgSchedules enables
			// NATS-native @at scheduling; the server auto-enables AllowRollup
			// which enforces per-subject rollup (one active schedule per subject).
			// MaxMsgsPerSubject: 1 provides an additional storage bound so the
			// stream cannot accumulate unbounded pending-schedule entries.
			Name:              CoreSchedulesStreamName,
			Description:       "Platform-wide message-scheduling stream (ADR-51). AllowMsgSchedules enables NATS-native @at/@every scheduling. Subject root: schedule.>",
			Subjects:          []string{SchedulesWildcardSubject},
			Retention:         jetstream.LimitsPolicy,
			Storage:           jetstream.FileStorage,
			MaxMsgsPerSubject: 1,
			AllowMsgSchedules: true,
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

// PrimordialSeeded reports whether the primordial set is present in Core KV,
// probing the bootstrap op tracker key that SeedPrimordial writes first
// (Contract #7 §7.7 ordering) and that therefore stands for the whole set.
//
// This is the authority on whether THIS Core KV has been seeded.
// `lattice.bootstrap.json` records only what a bootstrap run once did on some
// Core KV — a recreated or wiped bucket behind a surviving `status="committed"`
// file leaves the two disagreeing, so a caller deciding whether to seed must
// consult this and not the file alone.
func (s *Seeder) PrimordialSeeded(ctx context.Context) (bool, error) {
	kv, err := s.js.KeyValue(ctx, CoreKVBucket)
	if err != nil {
		return false, fmt.Errorf("open Core KV: %w", err)
	}
	if _, err := kv.Get(ctx, BootstrapOpKey); err == nil {
		return true, nil
	} else if !errors.Is(err, jetstream.ErrKeyNotFound) {
		return false, fmt.Errorf("probe op tracker key: %w", err)
	}
	return false, nil
}

// DecideReseed is the file/bucket-agreement check `cmd/bootstrap` runs before
// deciding whether to invoke SeedPrimordial. A freshly generated ID set (no
// prior file, or a crash-recovered `in-progress` one) always needs seeding.
// Otherwise it consults PrimordialSeeded: if Core KV already holds the
// primordial set, no reseed is needed; if not — a recreated or wiped bucket
// behind a surviving `status="committed"` file — it reopens the two-phase
// commit window at bootstrapJSONPath (keeping the file's stable NanoIDs) and
// reports that seeding must run.
func DecideReseed(ctx context.Context, seeder *Seeder, bootstrapJSONPath string, freshlyGenerated bool, logger *slog.Logger) (shouldSeed bool, err error) {
	if freshlyGenerated {
		return true, nil
	}
	seeded, err := seeder.PrimordialSeeded(ctx)
	if err != nil {
		return false, fmt.Errorf("probe Core KV for the primordial set: %w", err)
	}
	if seeded {
		return false, nil
	}
	logger.Warn("lattice.bootstrap.json says committed but Core KV holds no op tracker — bucket recreated or wiped; re-seeding with the file's stable NanoIDs",
		"path", bootstrapJSONPath, "key", BootstrapOpKey)
	if err := PersistInProgress(bootstrapJSONPath); err != nil {
		return false, fmt.Errorf("mark %s in-progress: %w", bootstrapJSONPath, err)
	}
	return true, nil
}

// Kernel composition:
//
//   - 1 bootstrap op tracker
//   - 1 primordial admin identity (vtx.identity.<NanoID>); no .state aspect
//     (state is identity-domain-package territory)
//   - 3 internal service-actor identities (Loom + Weaver + Bridge, arch §92;
//     class identity.system.loom / identity.system.weaver /
//     identity.system.bridge); no .state aspect
//   - 1 meta-meta DDL (vtx.meta.<NanoID-root>, canonicalName="root") +
//     9 aspects (canonicalName, permittedCommands, description, script,
//     inputSchema, outputSchema, fieldDescription, examples, compensation)
//   - 1 Lens meta-vertex (the Capability primordial-identity anchor) × 5
//     aspects (canonicalName, targetBucket, cypherRule, outputSchema, spec)
//     plus projectionKind + output. The role-by-operation index is owned by
//     the rbac-domain package, not seeded here.
//   - 5 aspect-type meta-vertices × 7 aspects each
//     (vertex + canonicalName, description, inputSchema, outputSchema,
//     fieldDescription, examples)
//   - 1 operator role vertex + canonicalName + description aspects
//   - 3 meta-permission vertices (CreateMetaVertex, UpdateMetaVertex,
//     TombstoneMetaVertex), all scope=any
//   - 3 grantedBy links (each meta-permission → operator)
//   - 1 admin→operator holdsRole link
//   - 5 service-actor→operator holdsRole links (Loom + Weaver + Bridge + object-store-manager + privacy)
//
// The Gateway service-actor identity is also seeded, deliberately WITHOUT a
// holdsRole→operator link (narrow-role fork; earns only identityProvisioner,
// via a one-time ops action, once identity-domain installs) — not counted
// in the 5 service-actor holdsRole links above.
//
// Total ≈ 76 Core KV entries. See `scripts/verify-kernel.go`.
//
// Roles consumer/frontOfHouse/backOfHouse and the identity DDL + its
// permissions and grants move to packages (rbac-domain, identity-domain,
// identity-hygiene). The five RoleMgmt DDLs are likewise gone; the
// `rbac` package DDL handles all role/permission/grant operations.
//
// SeedPrimordial writes all primordial Core KV entries per Contract #7 §7.2.
// Order per §7.7: op tracker → identities → meta DDLs → Lens definitions → roles → permissions → links.
// Uses substrate.AtomicBatch so either the entire primordial set lands or none
// of it does. The idempotent re-run path (Contract #7 §7.4) is preserved: if
// the bootstrap op tracker key already exists in Core KV, the function returns
// without re-issuing the batch.
func (s *Seeder) SeedPrimordial(ctx context.Context) error {
	// Idempotent re-run guard: if the primordial set is already present in
	// this Core KV, skip the whole batch.
	seeded, err := s.PrimordialSeeded(ctx)
	if err != nil {
		return err
	}
	if seeded {
		s.logger.Info("primordial set already present — skipping batch", "key", BootstrapOpKey)
		return nil
	}

	// The per-key fallback below needs its own handle on Core KV.
	kv, err := s.js.KeyValue(ctx, CoreKVBucket)
	if err != nil {
		return fmt.Errorf("open Core KV: %w", err)
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

	ack, err := conn.AtomicBatch(ctx, ops)
	if err != nil {
		// If the batch was rejected because a key already exists (e.g., a
		// concurrent bootstrapper raced us), fall back to the idempotent
		// per-key check. This protects re-run safety while keeping the
		// happy path single-batch.
		if errors.Is(err, substrate.ErrAtomicBatchRejected) && substrate.IsRevisionConflict(err) {
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
			if substrate.IsRevisionConflict(putErr) {
				s.logger.Info("key created concurrently, skipping", "key", e.key)
				continue
			}
			return fmt.Errorf("seed key %q: %w", e.key, putErr)
		}
		s.logger.Info("seeded primordial key", "key", e.key)
	}
	return nil
}

type kvEntry struct {
	key   string
	value []byte
}

// buildPrimordialEntries assembles all primordial KV entries in seeding
// order for the post-Story-4.7 minimized kernel. Roles consumer/
// frontOfHouse/backOfHouse, the identity DDL, and the 5 RoleMgmt DDLs
// have all moved to installable packages. See SeedPrimordial doc comment
// for the full composition.
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

	// 2. Primordial admin identity (class=identity; no .state aspect —
	// state is identity-domain-package territory). Protected (§3.4) so a
	// package uninstall can never tombstone the kernel admin.
	bsIdVal, bsIdErr := MakeVertexEnvelope(BootstrapIdentityKey, "identity",
		map[string]any{"protected": true,
			"note": "Primordial admin identity. Authors all primordial provenance fields. No state aspect."})
	if err := add(BootstrapIdentityKey, bsIdVal, bsIdErr); err != nil {
		return nil, err
	}

	// 2a. Internal service-actor identities — Loom, Weaver, and the Bridge
	// (arch §92). Root-equivalent actors that submit ops directly to the
	// ledger within the trust boundary. Root capability is established solely
	// by their holdsRole link to the operator role (entry 10a below), projected
	// by the Capability Lens identically to the admin — the `identity.system.*`
	// class is a descriptive marker and never gates capability (Contract #7
	// §7.7). Protected (§3.4) so a package uninstall can never tombstone a
	// kernel service actor.
	//
	// "Pre-provisioned signing keys" (arch §92) are NOT graph material:
	// the Processor authorizes on `env.Actor` → `cap.<actor>` with no
	// signature check, and there is no Gateway in Phase 2. The signing key
	// is the engine process's NATS transport credential for `ops.system.>`,
	// deferred to Stream 3 / deployment (arch lines 285/325). When envelope-
	// signature verification is ever added, these actors receive key
	// material at that time. The graph contribution here is the identity
	// vertex + root-role topology that makes the actor authorizable.
	loomIDVal, loomIDErr := MakeVertexEnvelope(LoomIdentityKey, "identity.system.loom",
		map[string]any{"protected": true,
			"note": "Internal Loom service-actor identity. Root-equivalent via holdsRole to the operator role."})
	if err := add(LoomIdentityKey, loomIDVal, loomIDErr); err != nil {
		return nil, err
	}
	weaverIDVal, weaverIDErr := MakeVertexEnvelope(WeaverIdentityKey, "identity.system.weaver",
		map[string]any{"protected": true,
			"note": "Internal Weaver service-actor identity. Root-equivalent via holdsRole to the operator role."})
	if err := add(WeaverIdentityKey, weaverIDVal, weaverIDErr); err != nil {
		return nil, err
	}
	bridgeIDVal, bridgeIDErr := MakeVertexEnvelope(BridgeIdentityKey, "identity.system.bridge",
		map[string]any{"protected": true,
			"note": "Internal Bridge service-actor identity. Root-equivalent via holdsRole to the operator role."})
	if err := add(BridgeIdentityKey, bridgeIDVal, bridgeIDErr); err != nil {
		return nil, err
	}
	objmgrIDVal, objmgrIDErr := MakeVertexEnvelope(ObjmgrIdentityKey, "identity.system.object-store-manager",
		map[string]any{"protected": true,
			"note": "Internal object-store-manager service-actor identity (the v1b GC owner-tombstone-cascade, §22). Root-equivalent via holdsRole to the operator role."})
	if err := add(ObjmgrIdentityKey, objmgrIDVal, objmgrIDErr); err != nil {
		return nil, err
	}
	privacyIDVal, privacyIDErr := MakeVertexEnvelope(PrivacyIdentityKey, "identity.system.privacy",
		map[string]any{"protected": true,
			"note": "Internal privacy-plane service-actor identity (crypto-shred finalization). The privacyworker + Refractor keyshredded listeners submit RecordShredFinalization under it. Root-equivalent via holdsRole to the operator role."})
	if err := add(PrivacyIdentityKey, privacyIDVal, privacyIDErr); err != nil {
		return nil, err
	}

	// The Gateway is seeded here like the other service actors, but — unlike
	// every one of them — deliberately gets NO holdsRole→operator link (no
	// entry in 10a below). It is internet-facing (triggered by every
	// unauthenticated HTTP request that reaches it, unlike the internal-only
	// triggers of Loom/Weaver/Bridge/objmgr/privacy), so it earns only the
	// narrow `identityProvisioner` role via a one-time ops action once
	// identity-domain installs (gateway-claim-flow-identity-provisioning-
	// design.md §3.3, §4 Option B). Before that action runs, its
	// ProvisionConsumerIdentity calls simply deny (fail-closed).
	gatewayIDVal, gatewayIDErr := MakeVertexEnvelope(GatewayIdentityKey, "identity.system.gateway",
		map[string]any{"protected": true,
			"note": "Internal Gateway service-actor identity. NOT root-equivalent — narrowly scoped to identityProvisioner once wired, not holdsRole→operator."})
	if err := add(GatewayIdentityKey, gatewayIDVal, gatewayIDErr); err != nil {
		return nil, err
	}

	// 3. Meta-meta root DDL meta-vertex — the kernel's sole DDL.
	rootVal, rootErr := MakeVertexEnvelope(MetaRootKey, "meta.ddl.vertexType",
		map[string]any{"protected": true,
			"note": "Meta-meta-DDL. Governs all vtx.meta.* mutations via " +
				"CreateMetaVertex / UpdateMetaVertex / TombstoneMetaVertex."})
	if err := add(MetaRootKey, rootVal, rootErr); err != nil {
		return nil, err
	}
	rootCanonicalAspectKey := substrate.AspectKey(MetaRootKey, "canonicalName")
	rca, rcaErr := MakeAspectEnvelope(rootCanonicalAspectKey, MetaRootKey, "canonicalName", "canonicalName",
		map[string]any{"value": "root"})
	if err := add(rootCanonicalAspectKey, rca, rcaErr); err != nil {
		return nil, err
	}
	rootPCKey := substrate.AspectKey(MetaRootKey, "permittedCommands")
	rpc, rpcErr := MakeAspectEnvelope(rootPCKey, MetaRootKey, "permittedCommands", "permittedCommands",
		map[string]any{"commands": []string{"CreateMetaVertex", "UpdateMetaVertex", "TombstoneMetaVertex"}})
	if err := add(rootPCKey, rpc, rpcErr); err != nil {
		return nil, err
	}
	rootDescAspectKey := substrate.AspectKey(MetaRootKey, "description")
	rda, rdaErr := MakeAspectEnvelope(rootDescAspectKey, MetaRootKey, "description", "description",
		map[string]any{"text": "Meta-meta-DDL governing all vtx.meta.* mutations. Dispatches on op.payload.targetClass."})
	if err := add(rootDescAspectKey, rda, rdaErr); err != nil {
		return nil, err
	}
	rootScriptKey := substrate.AspectKey(MetaRootKey, "script")
	rsv, rsErr := MakeAspectEnvelope(rootScriptKey, MetaRootKey, "script", "script",
		map[string]any{"source": MetaRootDDLScript})
	if err := add(rootScriptKey, rsv, rsErr); err != nil {
		return nil, err
	}
	rootInputSchemaKey := substrate.AspectKey(MetaRootKey, "inputSchema")
	risa, risaErr := MakeAspectEnvelope(rootInputSchemaKey, MetaRootKey, "inputSchema", "inputSchema",
		map[string]any{"schema": `{"type":"object","required":["targetClass","canonicalName"],"properties":{"targetClass":{"type":"string","description":"One of meta.ddl.vertexType|linkType|aspectType|eventType|meta.lens"},"canonicalName":{"type":"string"},"permittedCommands":{"type":"array","items":{"type":"string"}},"description":{"type":"string"},"script":{"type":"string"},"inputSchema":{"type":"string"},"outputSchema":{"type":"string"},"fieldDescription":{"type":"object"},"examples":{"type":"array"},"spec":{"type":"string"}}}`})
	if err := add(rootInputSchemaKey, risa, risaErr); err != nil {
		return nil, err
	}
	rootOutputSchemaKey := substrate.AspectKey(MetaRootKey, "outputSchema")
	rosa, rosaErr := MakeAspectEnvelope(rootOutputSchemaKey, MetaRootKey, "outputSchema", "outputSchema",
		map[string]any{"schema": `{"type":"object","properties":{"metaKey":{"type":"string"}},"required":["metaKey"]}`})
	if err := add(rootOutputSchemaKey, rosa, rosaErr); err != nil {
		return nil, err
	}
	rootFDKey := substrate.AspectKey(MetaRootKey, "fieldDescription")
	rfd, rfdErr := MakeAspectEnvelope(rootFDKey, MetaRootKey, "fieldDescription", "fieldDescription",
		map[string]any{"fieldDescriptions": map[string]any{
			"targetClass":       "The meta-vertex class to create. DDL classes: meta.ddl.vertexType, meta.ddl.linkType, meta.ddl.aspectType, meta.ddl.eventType. Lens class: meta.lens.",
			"canonicalName":     "The unique canonical name for this DDL. Used by Processor DDL cache for class lookup.",
			"permittedCommands": "List of operationType strings that may produce mutations of this vertex type.",
			"description":       "Plain-language markdown description of this DDL's purpose and behaviour.",
			"script":            "Starlark source for the DDL's execute(state, op) function.",
			"inputSchema":       "JSON Schema string for the operation payload accepted by this DDL.",
			"outputSchema":      "JSON Schema string for the operation response produced by this DDL.",
			"fieldDescription":  "Map of fieldPath to plain-language description for each payload field.",
			"examples":          "Array of {name, payload, expectedOutcome} usage examples.",
		}})
	if err := add(rootFDKey, rfd, rfdErr); err != nil {
		return nil, err
	}
	rootExamplesKey := substrate.AspectKey(MetaRootKey, "examples")
	rex, rexErr := MakeAspectEnvelope(rootExamplesKey, MetaRootKey, "examples", "examples",
		map[string]any{"examples": []any{
			map[string]any{
				"name": "CreateMetaVertex — new DDL",
				"payload": map[string]any{
					"targetClass":       "meta.ddl.vertexType",
					"canonicalName":     "book",
					"permittedCommands": []string{"CreateBook", "UpdateBook"},
					"description":       "Book vertex DDL. Carries title, author, isbn aspects.",
					"script":            "def execute(state, op): ...",
					"inputSchema":       `{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`,
					"outputSchema":      `{"type":"object","required":["bookKey"],"properties":{"bookKey":{"type":"string"}}}`,
					"fieldDescription":  map[string]any{"title": "Book title, max 500 chars."},
					"examples":          []any{},
				},
				"expectedOutcome": "Creates vtx.meta.<NanoID> with class=meta.ddl.vertexType and 9 aspect keys.",
			},
		}})
	if err := add(rootExamplesKey, rex, rexErr); err != nil {
		return nil, err
	}

	// 4a. Seed the .compensation aspect on the primordial kernel root DDL
	// meta-vertex. Describes how to roll back a CreateMetaVertex call:
	// tombstone the created meta-vertex. The Processor reads NO compensation
	// aspects (Guardrail 2); this entry is for client-side traversal only
	// (aiagent.Traverser.ReadCompensation).
	rootCompKey := substrate.AspectKey(MetaRootKey, CompensationAspectClass)
	rootComp, rootCompErr := MakeAspectEnvelope(rootCompKey, MetaRootKey, CompensationAspectClass, CompensationAspectClass,
		map[string]any{
			"inverseOperationType": "TombstoneMetaVertex",
			"payloadTemplate":      map[string]any{"metaKey": "{{primaryKey}}"},
			"revisionTemplate":     map[string]any{"metaKey": "{{revisions[primaryKey]}}"},
		})
	if err := add(rootCompKey, rootComp, rootCompErr); err != nil {
		return nil, err
	}

	// 4b. InstallPackage / UninstallPackage / UpgradePackage primordial DDLs.
	// Privileged kernel DDLs that route package install/uninstall/upgrade
	// through the Processor. Each is protected (§3.4) so it cannot be
	// tombstoned/updated or overwritten by an install.
	if err := seedPackageInstallDDL(add, InstallPackageDDLKey, "InstallPackage",
		[]string{"InstallPackage"},
		"Installs a Capability Package by applying its pre-built mutation manifest as one atomic commit. "+
			"Privileged: enforces key-shape, protected-key, system-aspect, and create-only guardrails.",
		InstallPackageDDLScript, installPackageInputSchema, installPackageOutputSchema,
		installPackageFieldDescription, installPackageExamples, "UninstallPackage"); err != nil {
		return nil, err
	}
	if err := seedPackageInstallDDL(add, UninstallPackageDDLKey, "UninstallPackage",
		[]string{"UninstallPackage"},
		"Uninstalls a Capability Package by tombstoning its declared keys as one atomic commit. "+
			"Carries optional per-key expectedRevision (OCC) and rejects protected kernel keys.",
		UninstallPackageDDLScript, uninstallPackageInputSchema, uninstallPackageOutputSchema,
		uninstallPackageFieldDescription, uninstallPackageExamples, "UninstallPackage"); err != nil {
		return nil, err
	}
	// UpgradePackage (Contract #8 §8.6) applies a mixed create/update/tombstone
	// diff in place. It is NOT create-only, so its safety rests on the step-8
	// protected-key guard. Its compensation inverse is itself — an upgrade is
	// rolled back by upgrading to the prior version (the prior manifest is not
	// templatable, so the saga hint names the inverse op, not a payload).
	if err := seedPackageInstallDDL(add, UpgradePackageDDLKey, "UpgradePackage",
		[]string{"UpgradePackage"},
		"Upgrades a Capability Package in place by applying a pre-computed create/update/tombstone "+
			"diff as one atomic commit. Privileged: enforces key-shape + system-aspect guardrails; "+
			"protected kernel/auth roots are rejected by the Processor commit-time guard.",
		UpgradePackageDDLScript, upgradePackageInputSchema, upgradePackageOutputSchema,
		upgradePackageFieldDescription, upgradePackageExamples, "UpgradePackage"); err != nil {
		return nil, err
	}

	// 5. Capability Lens definition.
	capLens := CapabilityLensDefinition()
	capLensVal, capLensErr := MakeVertexEnvelope(CapabilityLensKey, "meta.lens", map[string]any{"protected": true})
	if err := add(CapabilityLensKey, capLensVal, capLensErr); err != nil {
		return nil, err
	}
	if err := addLensAspects(&entries, CapabilityLensKey, capLens); err != nil {
		return nil, err
	}

	// 5b. Capability-Read Lens definition — the base read-path authorization
	// lens (Contract #6 §6.14, D1). Projects each actor's self anchor to
	// cap-read.<actor> in the same Capability KV bucket (disjoint key space);
	// package lenses contribute the rest of the read-grant union.
	capReadLens := CapabilityReadLensDefinition()
	capReadLensVal, capReadLensErr := MakeVertexEnvelope(CapabilityReadLensKey, "meta.lens", map[string]any{"protected": true})
	if err := add(CapabilityReadLensKey, capReadLensVal, capReadLensErr); err != nil {
		return nil, err
	}
	if err := addLensAspects(&entries, CapabilityReadLensKey, capReadLens); err != nil {
		return nil, err
	}

	// 5c. Capability-Read GRANTS Lens — the base read-grant PRODUCER (Contract
	// #6 §6.14, D1.3). The Postgres twin of 5b: it projects each actor's
	// self-anchor as a flat (actor_id, anchor_id, grant_source) grant row into
	// the shared actor_read_grants table — the source of truth Postgres-RLS
	// (the ratified Path-A enforcement boundary) reads. Without it the grant
	// table is empty and RLS denies every protected read. Adapter:postgres;
	// the DSN resolves from REFRACTOR_PG_DSN at activation.
	capReadGrantsLens := CapabilityReadGrantsLensDefinition()
	capReadGrantsLensVal, capReadGrantsLensErr := MakeVertexEnvelope(CapabilityReadGrantsLensKey, "meta.lens", map[string]any{"protected": true})
	if err := add(CapabilityReadGrantsLensKey, capReadGrantsLensVal, capReadGrantsLensErr); err != nil {
		return nil, err
	}
	if err := addLensAspects(&entries, CapabilityReadGrantsLensKey, capReadGrantsLens); err != nil {
		return nil, err
	}

	// 5d. Capability-Read WILDCARD Grants Lens — the base ALL-ACCESS read-grant
	// PRODUCER (Contract #6 §6.14, D1 design §3.4 M5). The wildcard sibling of
	// 5c: grants the reserved WildcardAnchor ("*") to the same fixed,
	// kernel-seeded root-equivalent identities the write-path capability lens
	// (5) grants root to — those holding the primordial `operator` role via
	// `holdsRole` (Contract #7 §7.7 / #6 §6.1) — so an all-access read (e.g. a
	// clinic staff/admin worklist) still passes through the §6.14 set-membership
	// RLS policy — never a bypass. Selection is the same bounded
	// `holdsRole → operator` existence check; `data.protected` is retired as a
	// capability designator (anti-brick only, Fork A 2026-07-02).
	capReadWildcardGrantsLens := CapabilityReadWildcardGrantsLensDefinition()
	capReadWildcardGrantsLensVal, capReadWildcardGrantsLensErr := MakeVertexEnvelope(CapabilityReadWildcardGrantsLensKey, "meta.lens", map[string]any{"protected": true})
	if err := add(CapabilityReadWildcardGrantsLensKey, capReadWildcardGrantsLensVal, capReadWildcardGrantsLensErr); err != nil {
		return nil, err
	}
	if err := addLensAspects(&entries, CapabilityReadWildcardGrantsLensKey, capReadWildcardGrantsLens); err != nil {
		return nil, err
	}

	// 6. Five aspect-type meta-vertices — the DDLs for the self-description
	// aspect classes. Each has class=meta.ddl.aspectType and carries all 5
	// descriptive aspects itself (bootstrapped primordially to avoid a
	// chicken-and-egg dependency with post-bootstrap DDL enforcement).
	if err := seedAspectTypeMeta(&entries, add); err != nil {
		return nil, err
	}

	// 7. Operator role — the only primordial role. Identity-domain
	// installs the user-facing roles (consumer/frontOfHouse/backOfHouse)
	// in its own install batch (Definition.Roles).
	{
		roleVal, roleErr := MakeVertexEnvelope(RoleOperatorKey, "role", map[string]any{"protected": true})
		if err := add(RoleOperatorKey, roleVal, roleErr); err != nil {
			return nil, err
		}
		cnKey := substrate.AspectKey(RoleOperatorKey, "canonicalName")
		cnVal, cnErr := MakeAspectEnvelope(cnKey, RoleOperatorKey, "canonicalName", "canonicalName",
			map[string]any{"value": "operator"})
		if err := add(cnKey, cnVal, cnErr); err != nil {
			return nil, err
		}
		descKey := substrate.AspectKey(RoleOperatorKey, "description")
		descVal, descErr := MakeAspectEnvelope(descKey, RoleOperatorKey, "description", "description",
			map[string]any{"text": "Platform operator role with kernel-meta privileges. " +
				"Receives CreateMetaVertex/UpdateMetaVertex/TombstoneMetaVertex grants from the kernel " +
				"and additional package-defined grants from rbac-domain + identity-domain after install."})
		if err := add(descKey, descVal, descErr); err != nil {
			return nil, err
		}
	}

	// 8. Three meta-permission vertices (CreateMetaVertex / UpdateMetaVertex /
	// TombstoneMetaVertex). These authorize the operator to mutate
	// vtx.meta.* — the entry point for package-installed DDLs and Lenses.
	metaPerms := []struct {
		key, id, op string
	}{
		{PermCreateMetaVertexKey, PermCreateMetaVertexID, "CreateMetaVertex"},
		{PermUpdateMetaVertexKey, PermUpdateMetaVertexID, "UpdateMetaVertex"},
		{PermTombstoneMetaVertexKey, PermTombstoneMetaVertexID, "TombstoneMetaVertex"},
	}
	// Package-install permissions authorizing the operator to submit the
	// InstallPackage / UninstallPackage / UpgradePackage ops. Projected into
	// the admin's Capability doc via the holdsRole → grantedBy chain.
	installPerms := []struct {
		key, id, op string
	}{
		{PermInstallPackageKey, PermInstallPackageID, "InstallPackage"},
		{PermUninstallPackageKey, PermUninstallPackageID, "UninstallPackage"},
		{PermUpgradePackageKey, PermUpgradePackageID, "UpgradePackage"},
	}
	for _, mp := range metaPerms {
		data := map[string]any{
			"protected":     true,
			"operationType": mp.op,
			"scope":         "any",
			"note":          "Kernel meta-permission. Authorizes operator to mutate vtx.meta.* vertices.",
		}
		permVal, permErr := MakeVertexEnvelope(mp.key, "permission", data)
		if err := add(mp.key, permVal, permErr); err != nil {
			return nil, err
		}
	}
	for _, mp := range installPerms {
		data := map[string]any{
			"protected":     true,
			"operationType": mp.op,
			"scope":         "any",
			"note":          "Kernel package-install permission. Authorizes operator to submit " + mp.op + ".",
		}
		permVal, permErr := MakeVertexEnvelope(mp.key, "permission", data)
		if err := add(mp.key, permVal, permErr); err != nil {
			return nil, err
		}
	}

	// 9. grantedBy links: each meta- + install-permission → operator role.
	for _, mp := range metaPerms {
		linkKey := substrate.LinkKey("permission", mp.id, "grantedBy", "role", RoleOperatorID)
		linkVal, linkErr := MakeLinkEnvelope(
			linkKey,
			"vtx.permission."+mp.id,
			"vtx.role."+RoleOperatorID,
			"grantedBy", "grantedBy", map[string]any{})
		if err := add(linkKey, linkVal, linkErr); err != nil {
			return nil, err
		}
	}
	for _, mp := range installPerms {
		linkKey := substrate.LinkKey("permission", mp.id, "grantedBy", "role", RoleOperatorID)
		linkVal, linkErr := MakeLinkEnvelope(
			linkKey,
			"vtx.permission."+mp.id,
			"vtx.role."+RoleOperatorID,
			"grantedBy", "grantedBy", map[string]any{})
		if err := add(linkKey, linkVal, linkErr); err != nil {
			return nil, err
		}
	}

	// 10. Primordial admin → operator holdsRole link. Decision #11: even
	// though `holdsRole` is an rbac-domain-package op type, the link
	// itself is a primordial relationship — the admin pre-exists the
	// rbac-domain package install.
	bsHoldsVal, bsHoldsErr := MakeLinkEnvelope(
		BootstrapHoldsRoleLinkKey,
		"vtx.identity."+BootstrapIdentityID,
		"vtx.role."+RoleOperatorID,
		"holdsRole", "holdsRole", map[string]any{})
	if err := add(BootstrapHoldsRoleLinkKey, bsHoldsVal, bsHoldsErr); err != nil {
		return nil, err
	}

	// 10a. Service-actor → operator holdsRole links. Identity is the source
	// (later-arriving vertex per Contract #1 §1.1); the operator role is the
	// target. Reads as the sentence "<service> holdsRole operator". This edge
	// establishes root-equivalence topologically (Contract #7 §7.7) — no new
	// role, permission, grantedBy link, cypher branch, or step-3 code. The
	// `cap.<actor>` doc itself is projected by the write-path Capability Lens
	// by walking this very `holdsRole → operator` edge (Contract #7 §7.7 /
	// #6 §6.1). `data.protected` is retired as a capability designator — it
	// carries only its anti-brick (immutability) meaning now (Fork A, 2026-07-02).
	// Role-derived package grants project to the disjoint `cap.roles.<actor>`.
	loomHoldsVal, loomHoldsErr := MakeLinkEnvelope(
		LoomHoldsRoleLinkKey,
		"vtx.identity."+LoomIdentityID,
		"vtx.role."+RoleOperatorID,
		"holdsRole", "holdsRole", map[string]any{})
	if err := add(LoomHoldsRoleLinkKey, loomHoldsVal, loomHoldsErr); err != nil {
		return nil, err
	}
	weaverHoldsVal, weaverHoldsErr := MakeLinkEnvelope(
		WeaverHoldsRoleLinkKey,
		"vtx.identity."+WeaverIdentityID,
		"vtx.role."+RoleOperatorID,
		"holdsRole", "holdsRole", map[string]any{})
	if err := add(WeaverHoldsRoleLinkKey, weaverHoldsVal, weaverHoldsErr); err != nil {
		return nil, err
	}
	bridgeHoldsVal, bridgeHoldsErr := MakeLinkEnvelope(
		BridgeHoldsRoleLinkKey,
		"vtx.identity."+BridgeIdentityID,
		"vtx.role."+RoleOperatorID,
		"holdsRole", "holdsRole", map[string]any{})
	if err := add(BridgeHoldsRoleLinkKey, bridgeHoldsVal, bridgeHoldsErr); err != nil {
		return nil, err
	}
	objmgrHoldsVal, objmgrHoldsErr := MakeLinkEnvelope(
		ObjmgrHoldsRoleLinkKey,
		"vtx.identity."+ObjmgrIdentityID,
		"vtx.role."+RoleOperatorID,
		"holdsRole", "holdsRole", map[string]any{})
	if err := add(ObjmgrHoldsRoleLinkKey, objmgrHoldsVal, objmgrHoldsErr); err != nil {
		return nil, err
	}
	privacyHoldsVal, privacyHoldsErr := MakeLinkEnvelope(
		PrivacyHoldsRoleLinkKey,
		"vtx.identity."+PrivacyIdentityID,
		"vtx.role."+RoleOperatorID,
		"holdsRole", "holdsRole", map[string]any{})
	if err := add(PrivacyHoldsRoleLinkKey, privacyHoldsVal, privacyHoldsErr); err != nil {
		return nil, err
	}

	// No Gateway entry here, deliberately — see the seeding comment above
	// (narrow-role fork). It is the one seeded service actor that never
	// gets a holdsRole→operator link.

	return entries, nil
}

// seedPackageInstallDDL seeds one privileged package-install DDL
// meta-vertex (InstallPackage / UninstallPackage) with all nine
// self-description aspects (4 structural + 4 self-description +
// .compensation). The root vertex is marked protected (§3.4) so it can
// never be tombstoned/updated or overwritten by an install.
func seedPackageInstallDDL(
	add func(string, []byte, error) error,
	ddlKey, canonicalName string,
	permittedCommands []string,
	description, script, inputSchema, outputSchema string,
	fieldDescriptions map[string]any,
	examples []any,
	inverseOperationType string,
) error {
	vtxVal, vtxErr := MakeVertexEnvelope(ddlKey, "meta.ddl.vertexType",
		map[string]any{"protected": true,
			"note": canonicalName + " primordial DDL. Routes Capability-Package " +
				"install/uninstall through the Processor (Story 1.5.5)."})
	if err := add(ddlKey, vtxVal, vtxErr); err != nil {
		return err
	}
	aspects := []struct {
		name, class string
		data        map[string]any
	}{
		{"canonicalName", "canonicalName", map[string]any{"value": canonicalName}},
		{"permittedCommands", "permittedCommands", map[string]any{"commands": permittedCommands}},
		{"description", "description", map[string]any{"text": description}},
		{"script", "script", map[string]any{"source": script}},
		{"inputSchema", "inputSchema", map[string]any{"schema": inputSchema}},
		{"outputSchema", "outputSchema", map[string]any{"schema": outputSchema}},
		{"fieldDescription", "fieldDescription", map[string]any{"fieldDescriptions": fieldDescriptions}},
		{"examples", "examples", map[string]any{"examples": examples}},
		{CompensationAspectClass, CompensationAspectClass, map[string]any{
			"inverseOperationType": inverseOperationType,
			"payloadTemplate":      map[string]any{"name": "{{payload.name}}"},
			"revisionTemplate":     map[string]any{},
		}},
	}
	for _, a := range aspects {
		key := substrate.AspectKey(ddlKey, a.name)
		val, err := MakeAspectEnvelope(key, ddlKey, a.name, a.class, a.data)
		if err := add(key, val, err); err != nil {
			return err
		}
	}
	return nil
}

// seedAspectTypeMeta seeds the five aspect-type meta-vertices — the
// primordial DDLs for the self-description aspect classes:
// description, inputSchema, outputSchema, fieldDescription, examples.
// Each carries all 5 descriptive aspects (bootstrapped directly so the
// enforcement rule in CreateMetaVertex can reference them without circularity).
func seedAspectTypeMeta(entries *[]kvEntry, add func(string, []byte, error) error) error {
	type aspectTypeDef struct {
		key               string
		name              string
		description       string
		inputSchema       string
		outputSchema      string
		fieldDescriptions map[string]any
		examples          []any
	}
	defs := []aspectTypeDef{
		{
			key:          AspectTypeDescriptionKey,
			name:         "description",
			description:  "Plain-language markdown description for a DDL meta-vertex, lens, role, or aspect type. Stored at vtx.meta.<X>.description. Max 10KB.",
			inputSchema:  `{"type":"object","properties":{"text":{"type":"string","maxLength":10240}},"required":["text"]}`,
			outputSchema: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
			fieldDescriptions: map[string]any{
				"text": "The markdown-formatted description content. Used by AI agents and humans to understand the entity.",
			},
			examples: []any{
				map[string]any{
					"name":            "identity DDL description",
					"payload":         map[string]any{"text": "Identity vertex. Carries name, email, phone, state, claimKey, credentialBinding, mergedInto aspects."},
					"expectedOutcome": "Stored at vtx.meta.<id>.description; readable by AI traversers.",
				},
			},
		},
		{
			key:          AspectTypeInputSchemaKey,
			name:         "inputSchema",
			description:  "JSON Schema object describing the valid input payload for a DDL operation. Stored at vtx.meta.<X>.inputSchema.",
			inputSchema:  `{"type":"object","properties":{"schema":{"type":"string"}},"required":["schema"]}`,
			outputSchema: `{"type":"object","properties":{"schema":{"type":"string"}},"required":["schema"]}`,
			fieldDescriptions: map[string]any{
				"schema": "The JSON Schema for the operation's input payload, serialized as a string.",
			},
			examples: []any{
				map[string]any{
					"name":            "CreateRole inputSchema",
					"payload":         map[string]any{"schema": `{"type":"object","properties":{"name":{"type":"string"},"description":{"type":"string"}},"required":["name"]}`},
					"expectedOutcome": "Validates CreateRole payloads; rejects missing `name`.",
				},
			},
		},
		{
			key:          AspectTypeOutputSchemaKey,
			name:         "outputSchema",
			description:  "JSON Schema object describing the structure of the operation's response payload. Stored at vtx.meta.<X>.outputSchema.",
			inputSchema:  `{"type":"object","properties":{"schema":{"type":"string"}},"required":["schema"]}`,
			outputSchema: `{"type":"object","properties":{"schema":{"type":"string"}},"required":["schema"]}`,
			fieldDescriptions: map[string]any{
				"schema": "The JSON Schema for the operation's response, serialized as a string.",
			},
			examples: []any{
				map[string]any{
					"name":            "CreateRole outputSchema",
					"payload":         map[string]any{"schema": `{"type":"object","properties":{"roleKey":{"type":"string"}},"required":["roleKey"]}`},
					"expectedOutcome": "Documents that CreateRole returns a roleKey.",
				},
			},
		},
		{
			key:          AspectTypeFieldDescriptionKey,
			name:         "fieldDescription",
			description:  "Map of field paths to plain-language descriptions. Enables AI agents to understand each input field for a DDL operation. Stored at vtx.meta.<X>.fieldDescription.",
			inputSchema:  `{"type":"object","properties":{"fieldDescriptions":{"type":"object","additionalProperties":{"type":"string"}}},"required":["fieldDescriptions"]}`,
			outputSchema: `{"type":"object","properties":{"fieldDescriptions":{"type":"object","additionalProperties":{"type":"string"}}},"required":["fieldDescriptions"]}`,
			fieldDescriptions: map[string]any{
				"fieldDescriptions": "A map where each key is a field path (e.g. `name`, `actorKey`) and each value is a plain-language description.",
			},
			examples: []any{
				map[string]any{
					"name": "CreateRole fieldDescription",
					"payload": map[string]any{"fieldDescriptions": map[string]any{
						"name":        "The canonical name for the new role (e.g. `consumer`, `backOfHouse`).",
						"description": "Optional human-readable description of the role's purpose.",
					}},
					"expectedOutcome": "Helps AI agents understand each CreateRole parameter.",
				},
			},
		},
		{
			key:          AspectTypeExamplesKey,
			name:         "examples",
			description:  "Array of named usage examples for a DDL operation. Each includes a sample payload and expected outcome. Stored at vtx.meta.<X>.examples.",
			inputSchema:  `{"type":"object","properties":{"examples":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"payload":{"type":"object"},"expectedOutcome":{"type":"string"}},"required":["name","payload","expectedOutcome"]}}},"required":["examples"]}`,
			outputSchema: `{"type":"object","properties":{"examples":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"payload":{"type":"object"},"expectedOutcome":{"type":"string"}},"required":["name","payload","expectedOutcome"]}}},"required":["examples"]}`,
			fieldDescriptions: map[string]any{
				"examples":                   "Array of example invocations.",
				"examples[].name":            "Short descriptive name for this example.",
				"examples[].payload":         "The operation payload sent by the client.",
				"examples[].expectedOutcome": "Plain English description of what the platform does.",
			},
			examples: []any{
				map[string]any{
					"name": "examples self-example",
					"payload": map[string]any{"examples": []any{
						map[string]any{
							"name":            "CreateRole example",
							"payload":         map[string]any{"name": "barista"},
							"expectedOutcome": "Creates vtx.role.<NanoID> with canonicalName=barista.",
						},
					}},
					"expectedOutcome": "This is the examples aspect for the examples aspect type — recursive but valid.",
				},
			},
		},
	}

	for _, d := range defs {
		vtxVal, vtxErr := MakeVertexEnvelope(d.key, "meta.ddl.aspectType", map[string]any{})
		if err := add(d.key, vtxVal, vtxErr); err != nil {
			return err
		}
		cnKey := substrate.AspectKey(d.key, "canonicalName")
		cnVal, cnErr := MakeAspectEnvelope(cnKey, d.key, "canonicalName", "canonicalName",
			map[string]any{"value": d.name})
		if err := add(cnKey, cnVal, cnErr); err != nil {
			return err
		}
		descKey := substrate.AspectKey(d.key, "description")
		descVal, descErr := MakeAspectEnvelope(descKey, d.key, "description", "description",
			map[string]any{"text": d.description})
		if err := add(descKey, descVal, descErr); err != nil {
			return err
		}
		isKey := substrate.AspectKey(d.key, "inputSchema")
		isVal, isErr := MakeAspectEnvelope(isKey, d.key, "inputSchema", "inputSchema",
			map[string]any{"schema": d.inputSchema})
		if err := add(isKey, isVal, isErr); err != nil {
			return err
		}
		osKey := substrate.AspectKey(d.key, "outputSchema")
		osVal, osErr := MakeAspectEnvelope(osKey, d.key, "outputSchema", "outputSchema",
			map[string]any{"schema": d.outputSchema})
		if err := add(osKey, osVal, osErr); err != nil {
			return err
		}
		fdKey := substrate.AspectKey(d.key, "fieldDescription")
		fdVal, fdErr := MakeAspectEnvelope(fdKey, d.key, "fieldDescription", "fieldDescription",
			map[string]any{"fieldDescriptions": d.fieldDescriptions})
		if err := add(fdKey, fdVal, fdErr); err != nil {
			return err
		}
		exKey := substrate.AspectKey(d.key, "examples")
		exVal, exErr := MakeAspectEnvelope(exKey, d.key, "examples", "examples",
			map[string]any{"examples": d.examples})
		if err := add(exKey, exVal, exErr); err != nil {
			return err
		}
	}
	return nil
}

// addLensAspects appends aspect entries for a Lens definition vertex.
// Emits five aspects: canonicalName, targetBucket, cypherRule, outputSchema,
// and spec. The `spec` aspect carries a complete LensSpec JSON body that
// Refractor's CoreKVSource reads to activate the lens. The four individual
// aspects are documentation surface for operators and for the
// verify-bootstrap regression gate.
//
// nats-kv primordial lenses carry the {targetBucket, outputSchema} doc-aspects;
// a postgres lens (the base read-grant producer, D1.3) carries a {targetTable}
// doc-aspect instead and no outputSchema (a grant lens declares none). The
// load-bearing `spec` aspect (consumed by Refractor) is emitted for both via
// makeLensSpecBody. Package lenses (pkgmgr.LensSpec) seed their own meta-vertex.
func addLensAspects(entries *[]kvEntry, lensKey string, def LensDefinition) error {
	type lensAspect struct {
		localName string
		class     string
		data      any
	}
	aspects := []lensAspect{
		{"canonicalName", "canonicalName", map[string]any{"value": def.CanonicalName}},
		{"cypherRule", "cypherRule", map[string]any{"rule": strings.TrimSpace(def.CypherRule)}},
	}
	if def.Adapter == "postgres" {
		table := def.Table
		if def.GrantTable && table == "" {
			table = "actor_read_grants"
		}
		aspects = append(aspects,
			lensAspect{"targetTable", "targetTable", map[string]any{"value": table, "adapter": "postgres"}})
	} else {
		aspects = append(aspects,
			lensAspect{"targetBucket", "targetBucket", map[string]any{"value": def.TargetBucket, "adapter": "nats-kv"}},
			lensAspect{"outputSchema", "outputSchema", map[string]any{"jsonSchema": json.RawMessage(def.OutputSchema)}})
	}
	// Actor-aggregate lenses carry the projectionKind + §6.13 Output descriptor
	// aspects; the operation-aggregate role-index lens carries neither.
	if def.ProjectionKind != "" {
		aspects = append(aspects,
			lensAspect{"projectionKind", "projectionKind", map[string]any{"value": def.ProjectionKind}})
	}
	if def.Output != nil {
		aspects = append(aspects,
			lensAspect{"output", "output", map[string]any{"descriptor": def.Output}})
	}
	for _, a := range aspects {
		key := substrate.AspectKey(lensKey, a.localName)
		val, err := MakeAspectEnvelope(key, lensKey, a.localName, a.class, a.data)
		if err != nil {
			return fmt.Errorf("build lens aspect %q: %w", key, err)
		}
		*entries = append(*entries, kvEntry{key: key, value: val})
	}

	// `spec` — full LensSpec body consumed by Refractor's CoreKVSource
	// activation watch. The seeded capability lenses target the
	// capability-kv bucket and select the full openCypher engine explicitly.
	_, lensID, ok := strings.Cut(lensKey, "vtx.meta.")
	if !ok {
		return fmt.Errorf("addLensAspects: lensKey %q does not have expected vtx.meta. prefix", lensKey)
	}
	specBody, err := makeLensSpecBody(lensID, def)
	if err != nil {
		return fmt.Errorf("build lens spec body for %q: %w", lensKey, err)
	}
	specKey := substrate.AspectKey(lensKey, "spec")
	specVal, err := MakeAspectEnvelope(specKey, lensKey, "spec", "lensSpec", specBody)
	if err != nil {
		return fmt.Errorf("build lens spec aspect %q: %w", specKey, err)
	}
	*entries = append(*entries, kvEntry{key: specKey, value: specVal})
	return nil
}

// makeLensSpecBody constructs the on-wire LensSpec JSON body for a
// primordial Capability Lens. The Refractor CoreKVSource consumes
// exactly this shape (LensSpec in internal/refractor/lens/corekv_source.go).
//
// A postgres lens (Adapter:"postgres", e.g. the base read-grant producer) emits
// a postgres targetType + targetConfig carrying the read-path posture
// (grantTable/protected/columns), mirroring pkgmgr.lensSpecBody. The DSN is left
// empty — Refractor's translateSpec resolves it from REFRACTOR_PG_DSN at
// activation, so the kernel declares posture, not a connection string. The
// nats-kv path (every other primordial lens) is unchanged.
func makeLensSpecBody(lensID string, def LensDefinition) (map[string]any, error) {
	if def.Adapter == "postgres" {
		return makePostgresLensSpecBody(lensID, def)
	}
	target := def.TargetBucket
	if target == "" {
		target = CapabilityKVBucket
	}
	// Refractor maps lens spec target buckets to provisioned NATS KV
	// buckets by name. The bootstrap LensDefinition.TargetBucket is the
	// short canonical name ("capability"); the actual provisioned
	// bucket is CapabilityKVBucket ("capability-kv"). Translate here.
	bucket := target
	if bucket == "capability" {
		bucket = CapabilityKVBucket
	}
	// Key field list matches the RETURN's first/primary output column. For
	// the capability anchor that's the envelope `key` added by the pipeline
	// envelope wrapper at write time.
	keyField := []string{"key"}
	targetConfig := map[string]any{
		"bucket": bucket,
		"key":    keyField,
	}
	cfgJSON, err := json.Marshal(targetConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal targetConfig: %w", err)
	}
	schemaRaw := json.RawMessage(def.OutputSchema)
	spec := map[string]any{
		"id":            lensID,
		"canonicalName": def.CanonicalName,
		"targetType":    "nats_kv",
		"targetConfig":  json.RawMessage(cfgJSON),
		"cypherRule":    strings.TrimSpace(def.CypherRule),
		"outputSchema":  schemaRaw,
		"engine":        "full",
	}
	// Actor-aggregate lenses carry projectionKind + the §6.13 Output descriptor
	// so Refractor's CoreKVSource compiles a ProjectionPlan; the operation-
	// aggregate role-index lens carries neither.
	if def.ProjectionKind != "" {
		spec["projectionKind"] = def.ProjectionKind
	}
	if def.Output != nil {
		spec["output"] = def.Output
	}
	return spec, nil
}

// makePostgresLensSpecBody builds the on-wire LensSpec body for a postgres
// primordial lens (the read-path posture), mirroring pkgmgr.lensSpecBody so the
// kernel-seeded shape is byte-identical to a package-declared one. The DSN is
// emitted empty — Refractor resolves REFRACTOR_PG_DSN at activation. A
// GrantTable lens omits the key so Refractor applies the platform grant
// composite (actor_id, anchor_id, grant_source); a protected lens carries its
// columns. No projectionKind/Output — a postgres read-path lens is a plain
// projection.
func makePostgresLensSpecBody(lensID string, def LensDefinition) (map[string]any, error) {
	targetConfig := map[string]any{
		"dsn":   "",
		"table": def.Table,
	}
	if !def.GrantTable {
		targetConfig["key"] = []string{"key"}
	}
	if def.Protected {
		targetConfig["protected"] = true
	}
	if def.GrantTable {
		targetConfig["grantTable"] = true
	}
	if len(def.Columns) > 0 {
		cols := make([]map[string]any, len(def.Columns))
		for i, c := range def.Columns {
			cols[i] = map[string]any{"name": c.Name, "type": c.Type}
		}
		targetConfig["columns"] = cols
	}
	cfgJSON, err := json.Marshal(targetConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal postgres targetConfig: %w", err)
	}
	return map[string]any{
		"id":            lensID,
		"canonicalName": def.CanonicalName,
		"targetType":    "postgres",
		"targetConfig":  json.RawMessage(cfgJSON),
		"cypherRule":    strings.TrimSpace(def.CypherRule),
		"engine":        "full",
	}, nil
}

// MarkBootstrapComplete writes the `health.bootstrap.complete` marker
// to the Health KV bucket. cmd/bootstrap writes this marker itself because
// it is the only process guaranteed to run after primordial seeding completes.
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

// WaitForBootstrapComplete blocks until the platform is ready for ops or ctx
// is cancelled. Readiness (Contract #7 §7.5) requires both:
//
//   - the Health KV `health.bootstrap.complete` marker, and
//   - the Capability KV projections of every actor that must be able to
//     submit ops at startup: the primordial admin and the three internal
//     service actors (Loom + Weaver + Bridge). Their `cap.identity.<id>` docs
//     are produced asynchronously by the Capability Lens once Refractor runs;
//     gating on them guarantees the engines are authorizable the moment
//     `make up` returns ready (the AC #4 intent).
//
// The Capability projections are produced by Refractor, so this MUST be
// called only after Refractor has been started — otherwise the cap.* poll
// can never satisfy. The single ctx deadline bounds the whole wait: a
// missing projection times out cleanly with a named-key error and never
// hangs past the caller's bound.
func WaitForBootstrapComplete(ctx context.Context, nc *nats.Conn, logger *slog.Logger) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jetstream.New: %w", err)
	}
	healthKV, err := js.KeyValue(ctx, HealthKVBucket)
	if err != nil {
		return fmt.Errorf("open Health KV: %w", err)
	}
	capabilityKV, err := js.KeyValue(ctx, CapabilityKVBucket)
	if err != nil {
		return fmt.Errorf("open Capability KV: %w", err)
	}

	// The actor capability projections required before declaring ready.
	// Keyed by the human-readable actor name for clear timeout diagnostics.
	capProjections := []struct{ actor, key string }{
		{"admin", capabilityKeyForIdentity(BootstrapIdentityID)},
		{"loom", capabilityKeyForIdentity(LoomIdentityID)},
		{"weaver", capabilityKeyForIdentity(WeaverIdentityID)},
		{"bridge", capabilityKeyForIdentity(BridgeIdentityID)},
		{"object-store-manager", capabilityKeyForIdentity(ObjmgrIdentityID)},
		{"privacy", capabilityKeyForIdentity(PrivacyIdentityID)},
	}

	// checkAll classifies each key's Get error into three outcomes. A genuine
	// key-absence (jetstream.ErrKeyNotFound) is a definitive not-ready signal:
	// it names the missing key so the eventual timeout reports it. A context
	// cancellation/deadline or a transient NATS condition is NOT definitive —
	// the key's presence is undetermined — so it returns an empty name and the
	// caller must not let it overwrite the last known-missing key (otherwise a
	// deadline firing mid-poll, always during the first/Health Get, would
	// mislabel the timeout as "Health missing" rather than the real laggard).
	// Any other Get error is a transport/bucket failure, returned immediately so
	// the caller fails fast on the true cause rather than burning the timeout.
	classify := func(bucket, key string, err error) (missing string, fatal error) {
		switch {
		case errors.Is(err, jetstream.ErrKeyNotFound):
			return key, nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
			errors.Is(err, nats.ErrTimeout), errors.Is(err, nats.ErrNoResponders):
			// Undetermined (caller's own deadline, or a transient NATS blip the
			// gate is meant to wait through) — keep polling, but report no
			// specific missing key.
			return "", nil
		default:
			return "", fmt.Errorf("read %s %s: %w", bucket, key, err)
		}
	}

	checkAll := func() (missing string, ok bool, fatal error) {
		if _, err := healthKV.Get(ctx, HealthBootstrapCompleteKey); err != nil {
			m, fatal := classify("Health KV", HealthBootstrapCompleteKey, err)
			return m, false, fatal
		}
		for _, p := range capProjections {
			if _, err := capabilityKV.Get(ctx, p.key); err != nil {
				m, fatal := classify("Capability KV", p.actor+" ("+p.key+")", err)
				return m, false, fatal
			}
		}
		return "", true, nil
	}

	// Check immediately before starting the poll loop — the Health marker is
	// typically already present since MarkBootstrapComplete runs just before
	// this call, though the cap.* projections usually lag behind Refractor.
	var lastMissing string
	if missing, ok, fatal := checkAll(); fatal != nil {
		return fatal
	} else if ok {
		logger.Info("readiness gate satisfied", "marker", HealthBootstrapCompleteKey,
			"capProjections", len(capProjections))
		return nil
	} else if missing != "" {
		lastMissing = missing
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness gate timed out waiting for %s: %w", lastMissing, ctx.Err())
		case <-ticker.C:
			missing, ok, fatal := checkAll()
			if fatal != nil {
				return fatal
			}
			if ok {
				logger.Info("readiness gate satisfied", "marker", HealthBootstrapCompleteKey,
					"capProjections", len(capProjections))
				return nil
			}
			// Only a definitively-absent key updates lastMissing; an
			// undetermined poll (empty name) leaves the prior value intact so the
			// timeout names the real laggard, not a deadline-interrupted Get.
			if missing != "" {
				lastMissing = missing
			}
			logger.Debug("waiting for readiness gate", "missing", missing)
		}
	}
}

// capabilityKeyForIdentity maps an identity NanoID to its Capability KV
// projection key (`cap.identity.<id>`) per Contract #6 §6.1.
func capabilityKeyForIdentity(id string) string {
	return "cap.identity." + id
}
