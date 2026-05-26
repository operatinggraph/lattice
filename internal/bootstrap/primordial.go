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

	"github.com/asolgan/lattice/internal/substrate"
)

const (
	// KV bucket names.
	CoreKVBucket           = "core-kv"
	HealthKVBucket         = "health-kv"
	CapabilityKVBucket     = "capability-kv"
	WeaverStateBucket      = "weaver-state"
	WeaverClaimsBucket     = "weaver-claims"
	RefractorAdjacencyKV   = "refractor-adjacency" // Refractor's internal adjacency store (private, not a Lens target)

	// JetStream stream names.
	CoreOpsStreamName    = "core-operations"
	CoreEventsStreamName = "core-events"

	// JetStream subjects. Per Contract #2 §2.3, lane subjects are
	// `ops.<lane>.>` (multi-segment). The `ops.>` wildcard covers all
	// lanes including future ones — including `ops.meta.>` (the meta lane).
	OpsWildcardSubject    = "ops.>"
	EventsWildcardSubject = "events.>"
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
			// LimitMarkerTTL enables per-key TTL support (Contract #4 §4.3).
			// Enables AllowMsgTTL on the underlying stream.
			// NATS requires LimitMarkerTTL >= 1 second.
			cfg.LimitMarkerTTL = 1 * time.Second
		}

		kv, err := s.js.CreateOrUpdateKeyValue(ctx, cfg)
		if err != nil {
			return fmt.Errorf("create/update KV bucket %q: %w", b.name, err)
		}
		s.logger.Info("KV bucket ready", "bucket", kv.Bucket())

		// For Core KV: also set AllowAtomicPublish: true on the underlying stream.
		// CreateKeyValue does NOT set this automatically; UpdateStream is required.
		if b.name == CoreKVBucket {
			if err := s.enableAtomicPublish(ctx, CoreKVBucket); err != nil {
				return fmt.Errorf("enable AtomicPublish on %q: %w", CoreKVBucket, err)
			}
		}
	}

	// Provision core-operations and core-events streams.
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
			// substrate.PublishBatch step-9 path (see
			// internal/processor/step9_publish.go).
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

// Kernel composition:
//
//   - 1 bootstrap op tracker
//   - 1 primordial admin identity (vtx.identity.<NanoID>); no .state aspect
//     (state is identity-domain-package territory)
//   - 1 meta-meta DDL (vtx.meta.<NanoID-root>, canonicalName="root") +
//     9 aspects (canonicalName, permittedCommands, description, script,
//     inputSchema, outputSchema, fieldDescription, examples, compensation)
//   - 2 Lens meta-vertices (Capability + capabilityRoleIndex) × 5 aspects each
//     (canonicalName, targetBucket, cypherRule, outputSchema, spec)
//   - 5 aspect-type meta-vertices × 7 aspects each
//     (vertex + canonicalName, description, inputSchema, outputSchema,
//     fieldDescription, examples)
//   - 1 operator role vertex + canonicalName + description aspects
//   - 3 meta-permission vertices (CreateMetaVertex, UpdateMetaVertex,
//     TombstoneMetaVertex), all scope=any
//   - 3 grantedBy links (each meta-permission → operator)
//   - 1 admin→operator holdsRole link
//
// Total ≈ 69 Core KV entries. See `scripts/verify-kernel.go`.
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

	// AtomicBatch does not accept a context, so the caller's ctx cancellation
	// is not propagated to this call. The 30s timeout is a fixed substrate
	// limit; it cannot be shortened by a SIGTERM during the batch.
	ack, err := conn.AtomicBatch(ops, 30*time.Second)
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
	// state is identity-domain-package territory).
	bsIdVal, bsIdErr := MakeVertexEnvelope(BootstrapIdentityKey, "identity",
		map[string]any{"note": "Primordial admin identity. Authors all primordial provenance fields. No state aspect."})
	if err := add(BootstrapIdentityKey, bsIdVal, bsIdErr); err != nil {
		return nil, err
	}

	// 3. Meta-meta root DDL meta-vertex — the kernel's sole DDL.
	rootVal, rootErr := MakeVertexEnvelope(MetaRootKey, "meta.ddl.vertexType",
		map[string]any{"note": "Meta-meta-DDL. Governs all vtx.meta.* mutations via " +
			"CreateMetaVertex / UpdateMetaVertex / TombstoneMetaVertex."})
	if err := add(MetaRootKey, rootVal, rootErr); err != nil {
		return nil, err
	}
	rootCanonicalAspectKey := MetaRootKey + ".canonicalName"
	rca, rcaErr := MakeAspectEnvelope(rootCanonicalAspectKey, MetaRootKey, "canonicalName", "canonicalName",
		map[string]any{"value": "root"})
	if err := add(rootCanonicalAspectKey, rca, rcaErr); err != nil {
		return nil, err
	}
	rootPCKey := MetaRootKey + ".permittedCommands"
	rpc, rpcErr := MakeAspectEnvelope(rootPCKey, MetaRootKey, "permittedCommands", "permittedCommands",
		map[string]any{"commands": []string{"CreateMetaVertex", "UpdateMetaVertex", "TombstoneMetaVertex"}})
	if err := add(rootPCKey, rpc, rpcErr); err != nil {
		return nil, err
	}
	rootDescAspectKey := MetaRootKey + ".description"
	rda, rdaErr := MakeAspectEnvelope(rootDescAspectKey, MetaRootKey, "description", "description",
		map[string]any{"text": "Meta-meta-DDL governing all vtx.meta.* mutations. Dispatches on op.payload.targetClass."})
	if err := add(rootDescAspectKey, rda, rdaErr); err != nil {
		return nil, err
	}
	rootScriptKey := MetaRootKey + ".script"
	rsv, rsErr := MakeAspectEnvelope(rootScriptKey, MetaRootKey, "script", "script",
		map[string]any{"source": MetaRootDDLScript})
	if err := add(rootScriptKey, rsv, rsErr); err != nil {
		return nil, err
	}
	rootInputSchemaKey := MetaRootKey + ".inputSchema"
	risa, risaErr := MakeAspectEnvelope(rootInputSchemaKey, MetaRootKey, "inputSchema", "inputSchema",
		map[string]any{"schema": `{"type":"object","required":["targetClass","canonicalName"],"properties":{"targetClass":{"type":"string","description":"One of meta.ddl.vertexType|linkType|aspectType|eventType|meta.lens"},"canonicalName":{"type":"string"},"permittedCommands":{"type":"array","items":{"type":"string"}},"description":{"type":"string"},"script":{"type":"string"},"inputSchema":{"type":"string"},"outputSchema":{"type":"string"},"fieldDescription":{"type":"object"},"examples":{"type":"array"},"spec":{"type":"string"}}}`})
	if err := add(rootInputSchemaKey, risa, risaErr); err != nil {
		return nil, err
	}
	rootOutputSchemaKey := MetaRootKey + ".outputSchema"
	rosa, rosaErr := MakeAspectEnvelope(rootOutputSchemaKey, MetaRootKey, "outputSchema", "outputSchema",
		map[string]any{"schema": `{"type":"object","properties":{"metaKey":{"type":"string"}},"required":["metaKey"]}`})
	if err := add(rootOutputSchemaKey, rosa, rosaErr); err != nil {
		return nil, err
	}
	rootFDKey := MetaRootKey + ".fieldDescription"
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
	rootExamplesKey := MetaRootKey + ".examples"
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
	rootCompKey := MetaRootKey + "." + CompensationAspectClass
	rootComp, rootCompErr := MakeAspectEnvelope(rootCompKey, MetaRootKey, CompensationAspectClass, CompensationAspectClass,
		map[string]any{
			"inverseOperationType": "TombstoneMetaVertex",
			"payloadTemplate":      map[string]any{"metaKey": "{{detail.metaKey}}"},
			"revisionTemplate":     map[string]any{"metaKey": "{{revisions[detail.metaKey]}}"},
		})
	if err := add(rootCompKey, rootComp, rootCompErr); err != nil {
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

	// 6a. Five aspect-type meta-vertices — the DDLs for the self-description
	// aspect classes. Each has class=meta.ddl.aspectType and carries all 5
	// descriptive aspects itself (bootstrapped primordially to avoid a
	// chicken-and-egg dependency with post-bootstrap DDL enforcement).
	if err := seedAspectTypeMeta(&entries, add); err != nil {
		return nil, err
	}

	// 7. Operator role — the only primordial role. Identity-domain
	// installs the user-facing roles (consumer/frontOfHouse/backOfHouse)
	// via its PreInstall hook.
	{
		roleVal, roleErr := MakeVertexEnvelope(RoleOperatorKey, "role", map[string]any{})
		if err := add(RoleOperatorKey, roleVal, roleErr); err != nil {
			return nil, err
		}
		cnKey := RoleOperatorKey + ".canonicalName"
		cnVal, cnErr := MakeAspectEnvelope(cnKey, RoleOperatorKey, "canonicalName", "canonicalName",
			map[string]any{"value": "operator"})
		if err := add(cnKey, cnVal, cnErr); err != nil {
			return nil, err
		}
		descKey := RoleOperatorKey + ".description"
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
	for _, mp := range metaPerms {
		data := map[string]any{
			"operationType": mp.op,
			"scope":         "any",
			"note":          "Kernel meta-permission. Authorizes operator to mutate vtx.meta.* vertices.",
		}
		permVal, permErr := MakeVertexEnvelope(mp.key, "permission", data)
		if err := add(mp.key, permVal, permErr); err != nil {
			return nil, err
		}
	}

	// 9. Three grantedBy links: meta-permission → operator role.
	for _, mp := range metaPerms {
		linkKey := "lnk.permission." + mp.id + ".grantedBy.role." + RoleOperatorID
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

	return entries, nil
}

// seedAspectTypeMeta seeds the five aspect-type meta-vertices — the
// primordial DDLs for the self-description aspect classes:
// description, inputSchema, outputSchema, fieldDescription, examples.
// Each carries all 5 descriptive aspects (bootstrapped directly so the
// enforcement rule in CreateMetaVertex can reference them without circularity).
func seedAspectTypeMeta(entries *[]kvEntry, add func(string, []byte, error) error) error {
	type aspectTypeDef struct {
		key         string
		name        string
		description string
		inputSchema  string
		outputSchema string
		fieldDescriptions map[string]any
		examples    []any
	}
	defs := []aspectTypeDef{
		{
			key:         AspectTypeDescriptionKey,
			name:        "description",
			description: "Plain-language markdown description for a DDL meta-vertex, lens, role, or aspect type. Stored at vtx.meta.<X>.description. Max 10KB.",
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
			key:         AspectTypeInputSchemaKey,
			name:        "inputSchema",
			description: "JSON Schema object describing the valid input payload for a DDL operation. Stored at vtx.meta.<X>.inputSchema.",
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
			key:         AspectTypeOutputSchemaKey,
			name:        "outputSchema",
			description: "JSON Schema object describing the structure of the operation's response payload. Stored at vtx.meta.<X>.outputSchema.",
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
			key:         AspectTypeFieldDescriptionKey,
			name:        "fieldDescription",
			description: "Map of field paths to plain-language descriptions. Enables AI agents to understand each input field for a DDL operation. Stored at vtx.meta.<X>.fieldDescription.",
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
			key:         AspectTypeExamplesKey,
			name:        "examples",
			description: "Array of named usage examples for a DDL operation. Each includes a sample payload and expected outcome. Stored at vtx.meta.<X>.examples.",
			inputSchema:  `{"type":"object","properties":{"examples":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"payload":{"type":"object"},"expectedOutcome":{"type":"string"}},"required":["name","payload","expectedOutcome"]}}},"required":["examples"]}`,
			outputSchema: `{"type":"object","properties":{"examples":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"payload":{"type":"object"},"expectedOutcome":{"type":"string"}},"required":["name","payload","expectedOutcome"]}}},"required":["examples"]}`,
			fieldDescriptions: map[string]any{
				"examples":              "Array of example invocations.",
				"examples[].name":       "Short descriptive name for this example.",
				"examples[].payload":    "The operation payload sent by the client.",
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
		cnVal, cnErr := MakeAspectEnvelope(d.key+".canonicalName", d.key, "canonicalName", "canonicalName",
			map[string]any{"value": d.name})
		if err := add(d.key+".canonicalName", cnVal, cnErr); err != nil {
			return err
		}
		descVal, descErr := MakeAspectEnvelope(d.key+".description", d.key, "description", "description",
			map[string]any{"text": d.description})
		if err := add(d.key+".description", descVal, descErr); err != nil {
			return err
		}
		isVal, isErr := MakeAspectEnvelope(d.key+".inputSchema", d.key, "inputSchema", "inputSchema",
			map[string]any{"schema": d.inputSchema})
		if err := add(d.key+".inputSchema", isVal, isErr); err != nil {
			return err
		}
		osVal, osErr := MakeAspectEnvelope(d.key+".outputSchema", d.key, "outputSchema", "outputSchema",
			map[string]any{"schema": d.outputSchema})
		if err := add(d.key+".outputSchema", osVal, osErr); err != nil {
			return err
		}
		fdVal, fdErr := MakeAspectEnvelope(d.key+".fieldDescription", d.key, "fieldDescription", "fieldDescription",
			map[string]any{"fieldDescriptions": d.fieldDescriptions})
		if err := add(d.key+".fieldDescription", fdVal, fdErr); err != nil {
			return err
		}
		exVal, exErr := MakeAspectEnvelope(d.key+".examples", d.key, "examples", "examples",
			map[string]any{"examples": d.examples})
		if err := add(d.key+".examples", exVal, exErr); err != nil {
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
	specKey := lensKey + ".spec"
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
func makeLensSpecBody(lensID string, def LensDefinition) (map[string]any, error) {
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
	// Key field list matches the RETURN's first/primary output column.
	// For the primary capability Lens that's the envelope `key` (added
	// by the pipeline envelope wrapper at write time). The secondary
	// capabilityRoleIndex projects per-operationType records; its key
	// column is `operationType` and the bucket entry is keyed by that.
	keyField := []string{"key"}
	if def.CanonicalName == "capabilityRoleIndex" {
		keyField = []string{"operationType"}
	}
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
	return spec, nil
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

	// Check immediately before starting the poll loop — the key is typically
	// already present since MarkBootstrapComplete runs just before this call.
	if _, err := kv.Get(ctx, HealthBootstrapCompleteKey); err == nil {
		logger.Info("readiness gate satisfied", "key", HealthBootstrapCompleteKey)
		return nil
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
