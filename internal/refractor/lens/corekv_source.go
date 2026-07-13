package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/substrate"
)

// lensSourceDurablePrefix is the JetStream durable-consumer name prefix for
// the lens-definition subscription. An instance segment plus a per-boot
// nonce are appended (see Start) so each boot gets a never-before-seen
// durable name and replays the full installed lens set via IncludeHistory —
// mirroring internal/loom/source.go's patternSourceDurablePrefix verbatim
// (refractor-lens-registry-restart-integrity-design.md §4 Fire A).
//
// Why a per-boot durable rather than one ack-floor-resuming name: the lens
// registry is DERIVED in-memory state rebuilt from vtx.meta.> replay. A
// durable that resumes from its ack floor replays nothing once caught up,
// leaving the registry (and every pipeline it should have started) silently
// empty across every subsequent restart — the exact incident this design
// fixes. The nonce is load-bearing: JetStream only honors DeliverPolicy when
// a durable is first created, so a stable instance-only name would still
// defeat full-replay-on-every-connect after the first boot.
//
// Deliberately no trailing dash: PruneStaleDurables(ctx, bucket,
// lensSourceDurablePrefix, ...) then also matches the legacy fixed durable
// name "refractor-lens-source" (pre-2026-07-13 code), so the first boot's
// prune doubles as the one-time migration off it — no separate migration
// step. Multi-cell deployments (Phase 3) will include a cell-id segment.
const lensSourceDurablePrefix = "refractor-lens-source"

// UpdateCallback is called when an existing rule is updated (not on first load).
// old is a snapshot of the previous version; new is the updated version.
// kind is the result of ClassifyUpdate(old, new).
// The callback is called outside the source's mutex, after the rule is indexed and ACK'd.
type UpdateCallback func(old, new *Rule, kind UpdateKind)

// CoreKVSource is the lens-definition source. It subscribes to Core KV under
// `vtx.meta.>` via a Lattice-native durable JetStream consumer
// (substrate.SubscribeKVChanges) and routes only those updates whose envelope
// class is `meta.lens` to the lens loader. Other meta classes
// (`meta.ddl.*`, `meta.event.*`, etc.) are skipped silently
// (data-contracts.md §1.2 line 70).
//
// Lens definitions arrive via the normal Processor write path as
// `vtx.meta.<NanoID>` (vertex, class `meta.lens`) + a
// `vtx.meta.<NanoID>.spec` aspect carrying the LensSpec body.
//
// Each boot subscribes on a fresh per-boot durable name (instance segment +
// nonce) with IncludeHistory=true, so every boot — fresh deployment or
// restart alike — replays the entire installed lens set; see
// lensSourceDurablePrefix.
type CoreKVSource struct {
	conn     *substrate.Conn
	bucket   string
	instance string
	loadCB   func(*Rule)
	updateCB UpdateCallback
	logger   *slog.Logger
	mu       sync.RWMutex
	known    map[string]*Rule // lensId → last-loaded Rule
	// lensVertices tracks IDs of `vtx.meta.<id>` vertices whose document
	// class is `meta.lens`. Aspects under these vertices are routed to
	// the lens loader; everything else under `vtx.meta.>` is skipped.
	// data-contracts.md §1.2 line 70: lens is a flavor of meta,
	// distinguished by `class` field.
	lensVertices map[string]struct{}
	// pendingSpecs holds spec aspects observed BEFORE their parent
	// vertex's class arrived (CDC ordering is not guaranteed). Once
	// the parent vertex with class `meta.lens` is observed, the buffered
	// spec is replayed through handleSpec.
	pendingSpecs map[string][]byte // lensId → last spec body
}

// envelopeProbe is a minimal struct used to peek at the `class` field of
// any document body in Core KV without committing to a full envelope shape.
type envelopeProbe struct {
	Class string `json:"class"`
}

// sourceKindProbe peeks at a LensSpec body's `source.kind` without
// unmarshalling into the (now Chronicler-only) SourceConfig shape.
type sourceKindProbe struct {
	Source *struct {
		Kind string `json:"kind"`
	} `json:"source"`
}

// isEventStreamSpec reports whether a lens spec body declares
// `source.kind: "eventStream"` — a Chronicler-owned definition
// (chronicler-host-reconciliation), never a Refractor lens. Mirrors
// internal/chronicler's own isEventStreamSpec pre-check, inverted: that
// package's discovery loop silently skips every non-eventStream spec: this
// one silently skips every eventStream spec. A malformed body is treated as
// non-eventStream — the normal unmarshal below still validates its shape
// and reports fail-closed.
func isEventStreamSpec(specBody []byte) bool {
	var probe sourceKindProbe
	if err := json.Unmarshal(specBody, &probe); err != nil {
		return false
	}
	return probe.Source != nil && probe.Source.Kind == "eventStream"
}

// LensSpec mirrors the JSON aspect body stored at
// `vtx.meta.<NanoID>.spec` (parent vertex class `meta.lens`). See
// MORPH-DEVIATIONS Deviation 11.
type LensSpec struct {
	ID            string          `json:"id"`            // lens NanoID; matches the key segment
	CanonicalName string          `json:"canonicalName"` // e.g., "lens.contract-view"
	TargetType    string          `json:"targetType"`    // "postgres" | "nats_kv" | "nats_subject"
	TargetConfig  json.RawMessage `json:"targetConfig"`  // adapter-specific JSON object
	CypherRule    string          `json:"cypherRule"`    // openCypher MATCH/RETURN
	OutputSchema  json.RawMessage `json:"outputSchema"`  // JSON schema for projection rows (passthrough)
	// Engine is the explicit engine selector. "" (absent) triggers the
	// simple-then-full fallback. Set to "full" on primordial Capability
	// Lens specs so the full engine is used without falling back to simple.
	Engine string `json:"engine,omitempty"`

	// ProjectionKind opts a lens into a declarative projection plan. The only
	// recognized value is "actorAggregate" (Contract #6 §6.13): the lens
	// aggregates per actor and is compiled into a ProjectionPlan. Absent or any
	// other value means the lens is not actor-aggregate.
	ProjectionKind string `json:"projectionKind,omitempty"`

	// Output carries the §6.13 Output descriptor for an actor-aggregate lens.
	// It is read declaratively in place of the per-canonical-name Go wrappers.
	Output *OutputDescriptorSpec `json:"output,omitempty"`
}

// OutputDescriptorSpec mirrors the JSON shape of the §6.13 Output descriptor
// carried on an actor-aggregate lens spec. The first six fields are the ratified
// Contract #6 §6.13 descriptor. The remaining fields are envelope-shape options
// the projection driver reads so the on-wire document is byte-identical to the
// shape each built-in lens emits; they default to the generic auth-aggregate
// shape (top-level actor field "actor", no lanes, no always-empty columns) so a
// brand-new package lens needs only the six standard fields.
type OutputDescriptorSpec struct {
	AnchorType       string   `json:"anchorType"`       // actor vertex type, e.g. "identity"
	OutputKeyPattern string   `json:"outputKeyPattern"` // constrained key template, e.g. "cap.ephemeral.{actorSuffix}"
	BodyColumns      []string `json:"bodyColumns"`      // RETURN aliases that form the document body
	EmptyBehavior    string   `json:"emptyBehavior"`    // delete | softDelete | emptyDoc | skip
	RealnessFilter   string   `json:"realnessFilter"`   // field whose non-empty value marks a real collect entry
	Freshness        string   `json:"freshness"`        // "auto"

	// KeyColumn, when set, opts an actor-aggregate lens into the §10.2 Option (b)
	// row-key shape: BuildKey emits the anchor's bare-NanoID <entityId> into the
	// {actorSuffix} slot instead of the default <type>.<id> suffix, so a
	// convergence row key stays <targetId>.<entityId> (bare NanoID) and Weaver's
	// splitRowKey accepts it unchanged. Empty leaves the default suffix path.
	KeyColumn string `json:"keyColumn,omitempty"`

	// ActorField names the top-level envelope field that carries the actor
	// vertex key. Defaults to "actor" (the cap.* documents); the my-tasks
	// document uses "assignee".
	ActorField string `json:"actorField,omitempty"`

	// Lanes, when non-empty, is emitted verbatim as the document's `lanes`
	// array. Only the primary cap.<actor> document carries lanes.
	Lanes []string `json:"lanes,omitempty"`

	// StaticEmptyColumns names body columns the document always materializes as
	// an empty array regardless of the RETURN row. The primary cap.<actor>
	// document carries an always-empty `ephemeralGrants` (the live grants live
	// in the disjoint cap.ephemeral.<actor> document; §6.2/§6.3 require the field
	// to be present here).
	StaticEmptyColumns []string `json:"staticEmptyColumns,omitempty"`
}

// TargetPostgresConfig is the expected shape of LensSpec.TargetConfig
// when TargetType == "postgres".
type TargetPostgresConfig struct {
	DSN          string   `json:"dsn"`
	Table        string   `json:"table"`
	Key          []string `json:"key"`
	QueryTimeout string   `json:"queryTimeout"`         // optional, e.g., "5s"
	DeleteMode   string   `json:"deleteMode,omitempty"` // optional; "hard" (default) or "soft"

	// Read-path authorization (Contract #6 §6.14, D1.3). A business read model
	// is protected by default; one of Protected/Public should be declared
	// explicitly. Protected provisions an RLS table (FORCE ROW LEVEL SECURITY +
	// the set-membership policy) at activation and projects an authz_anchors
	// column; Public is the auditable opt-out for genuinely public models.
	Protected bool `json:"protected,omitempty"`
	Public    bool `json:"public,omitempty"`

	// Columns declares the business columns of a protected table (name + verbatim
	// Postgres type) so Refractor can provision the table from the lens spec. The
	// platform always adds authz_anchors text[] and projection_seq; key columns
	// are provisioned as text. Ignored for a non-protected lens (its table is
	// provisioned out-of-band).
	Columns []PostgresColumn `json:"columns,omitempty"`

	// GrantTable marks this lens as a cap-read.* grant projector. Its rows are
	// written to the shared actor_read_grants table through the seq-guarded grant
	// writer (not the last-writer-wins business adapter). The table name defaults
	// to actor_read_grants and the key to (actor_id, anchor_id, grant_source).
	GrantTable bool `json:"grantTable,omitempty"`

	// SecureColumns opts the lens into Secure-Lens decrypt-at-projection
	// (Contract #3 §3.10 "the read-path-authorized Secure Lens"): each entry
	// names a RETURN column carrying a sensitive aspect's ciphertext envelope
	// (`node.<aspect>.data`) that Refractor decrypts under the owning
	// identity's DEK before the row lands in the read model. Because the
	// projected value is PLAINTEXT PII, a secure lens MUST be a protected
	// (RLS) model — translateSpec fails any other posture closed.
	SecureColumns []SecureColumn `json:"secureColumns,omitempty"`

	// DiffRetraction opts this plain postgres lens into Fire 3's target-diff
	// retraction (see lens.IntoConfig.DiffRetraction). This mechanism reads the
	// adapter's live key set (adapter.KeyLister), which the NATS-KV adapter
	// (TargetNATSKVConfig.DiffRetraction) also implements and, since
	// dedup-over-encrypted-pii-design.md's duplicateCandidates lens, also uses.
	DiffRetraction bool `json:"diffRetraction,omitempty"`
}

// PostgresColumn declares one provisioned column of a protected read-model
// table: Type is the verbatim Postgres column type (e.g. "text", "bigint").
type PostgresColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SecureColumn declares one decrypt-at-projection column of a Secure Lens.
// Column is the RETURN alias holding the ciphertext envelope;
// IdentityKeyColumn is the RETURN alias holding the owning identity's vertex
// key (vtx.identity.<id>); Field optionally selects one field of the
// decrypted plaintext object (e.g. "value" — empty projects the whole
// object). On-wire mirror of pipeline.SecureColumn.
type SecureColumn struct {
	Column            string `json:"column"`
	IdentityKeyColumn string `json:"identityKeyColumn"`
	Field             string `json:"field,omitempty"`
}

// TargetNATSKVConfig is the expected shape of LensSpec.TargetConfig
// when TargetType == "nats_kv".
type TargetNATSKVConfig struct {
	Bucket     string   `json:"bucket"`
	Key        []string `json:"key"`
	DeleteMode string   `json:"deleteMode,omitempty"` // optional; "hard" (default) or "soft"

	// Protected and GrantTable are read-path-authorization (Contract #6 §6.14)
	// postgres concepts that cannot be honored on a NATS-KV target: RLS is the
	// enforcement boundary and NATS-KV has no row-level guard, and the grant
	// table is the shared Postgres actor_read_grants. They are parsed here only
	// so a misdirected declaration fails closed at activation (D1 §3.3) rather
	// than being silently dropped — which would world-publish a model the author
	// believed was protected, or scatter the read-auth source of truth onto a
	// regular bucket. SecureColumns (Contract #3 §3.10) is parsed for the same
	// reason: honoring it here would decrypt PII into a bucket with no
	// row-level enforcement.
	Protected     bool           `json:"protected,omitempty"`
	GrantTable    bool           `json:"grantTable,omitempty"`
	SecureColumns []SecureColumn `json:"secureColumns,omitempty"`

	// DiffRetraction opts this plain lens into Fire 3's target-diff retraction
	// (see lens.IntoConfig.DiffRetraction) — dedup-over-encrypted-pii-design.md
	// §2.3/§3.3's first NATS-KV consumer (a pair-keyed output defeats
	// anchor-derived retraction). The NATS-KV adapter implements KeyLister /
	// Purge (natskv.go), so it needs no additional adapter-side change.
	DiffRetraction bool `json:"diffRetraction,omitempty"`
}

// TargetNATSSubjectConfig is the expected shape of LensSpec.TargetConfig
// when TargetType == "nats_subject" — the Personal Lens transport
// (personal-secure-lens-design.md Fire 1: PL.1). Key must include
// adapter.PersonalActorKeyField ("__actor") exactly once; a direct (non-
// Personal) lens's cypher RETURN aliases that column to the recipient
// identity itself.
type TargetNATSSubjectConfig struct {
	SubjectPrefix string   `json:"subjectPrefix"` // e.g. "lattice.sync.user"
	Stream        string   `json:"stream"`        // backing JetStream stream name, e.g. "SYNC"
	Key           []string `json:"key"`
	// Personal opts this lens into Fire 2's cross-vertex fan-out (see
	// IntoConfig.Personal). When true, "__actor" is NOT a RETURN alias
	// requirement — the enumerated recipient is injected by the pipeline.
	Personal bool `json:"personal,omitempty"`
}

// NewCoreKVSource constructs a watcher. instance names the boot for the
// per-boot durable (attributability in logs/dashboards; see
// lensSourceDurablePrefix — uniqueness comes from the nonce Start appends,
// not from instance). logger may be nil.
func NewCoreKVSource(conn *substrate.Conn, bucket, instance string, logger *slog.Logger) *CoreKVSource {
	if logger == nil {
		logger = slog.Default()
	}
	return &CoreKVSource{
		conn:         conn,
		bucket:       bucket,
		instance:     instance,
		logger:       logger,
		known:        make(map[string]*Rule),
		lensVertices: make(map[string]struct{}),
		pendingSpecs: make(map[string][]byte),
	}
}

// lensClassValue is the canonical envelope class for lens meta-vertices.
// data-contracts.md §1.2 line 70.
const lensClassValue = "meta.lens"

// SetLoadCallback registers the first-time-load callback. Must be set before Start.
func (s *CoreKVSource) SetLoadCallback(fn func(*Rule)) { s.loadCB = fn }

// SetUpdateCallback registers the update callback. Must be set before Start.
func (s *CoreKVSource) SetUpdateCallback(fn UpdateCallback) { s.updateCB = fn }

// Get returns the last-loaded Rule for ruleID and whether it was found.
// Satisfies the control.RuleGetter interface so the validate control op can
// inspect the active rule state without importing internal/lens.
func (s *CoreKVSource) Get(ruleID string) (*Rule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.known[ruleID]
	return r, ok
}

// Start subscribes to lens-definition mutations via the substrate's
// durable JetStream-consumer helper and launches the dispatch goroutine.
// Returns when the subscription is established (or fails to establish).
// The goroutine runs until ctx is cancelled.
//
// Per data-contracts.md §1.2 line 70 lens definitions are meta-vertices
// distinguished by envelope class `meta.lens`; the subscription filter
// is the prefix only, and class-based routing happens inside handle().
//
// Each boot's durable name carries the instance segment (attributability)
// plus a fresh per-boot nonce (uniqueness — see lensSourceDurablePrefix), so
// IncludeHistory=true replays the entire installed lens set on every boot,
// not just a fresh deployment's first connect. Before creating its own
// durable, Start prunes any stale "<prefix>*" durables left behind by
// no-longer-running instances (age-guarded — a live sibling's durable is
// never pruned, substrate.PruneStaleDurables); the durable created here is
// then deleted on clean shutdown (consume's ctx.Done branch) so it never
// becomes next boot's stale entry.
func (s *CoreKVSource) Start(ctx context.Context) error {
	bootNonce, err := substrate.NewNanoID()
	if err != nil {
		return fmt.Errorf("lens source: boot nonce: %w", err)
	}
	durable := lensSourceDurablePrefix + "-" + s.instance + "-" + bootNonce
	if err := s.conn.PruneStaleDurables(ctx, s.bucket, lensSourceDurablePrefix, durable, s.logger); err != nil {
		s.logger.Warn("prune stale lens-source durables failed", "err", err)
	}
	events, err := s.conn.SubscribeKVChanges(
		ctx,
		s.bucket,
		"vtx.meta.",
		durable,
		substrate.SubscribeKVOptions{
			IncludeHistory: true,
			Logger:         s.logger,
		},
	)
	if err != nil {
		return fmt.Errorf("subscribe core KV vtx.meta.>: %w", err)
	}
	go s.consume(ctx, events, durable)
	return nil
}

func (s *CoreKVSource) consume(ctx context.Context, events <-chan substrate.KVEvent, durable string) {
	for {
		select {
		case <-ctx.Done():
			s.deleteOwnDurable(durable)
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			s.handle(evt)
		}
	}
}

// deleteOwnDurable removes this boot's per-instance durable on clean
// shutdown so it never lingers as a stale entry for the next boot's
// PruneStaleDurables to clean up. Best-effort: ctx is already cancelled, so
// a fresh background context with a short bound is used for the delete call.
func (s *CoreKVSource) deleteOwnDurable(durable string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.conn.DeleteDurable(ctx, s.bucket, durable); err != nil {
		s.logger.Warn("delete own lens-source durable failed", "durable", durable, "err", err)
	}
}

// handle dispatches one KV mutation from `vtx.meta.>`. Routing is driven
// by the envelope `class` field (data-contracts.md §1.2 line 70):
//   - vertex (3-seg) with class `meta.lens`: register as a lens vertex;
//     replay any buffered spec aspect.
//   - vertex (3-seg) with any other class (`meta.ddl.*`, `meta.event.*`,
//     etc.): skip silently — not the Refractor's concern.
//   - aspect (4-seg) under a known lens vertex with localName `spec`:
//     parse + translate + dispatch to loader.
//   - aspect (4-seg) under an unknown parent: buffer until the parent
//     vertex's class is observed (CDC ordering is not guaranteed).
//
// The IsDeleted signal covers both NATS KV tombstones (empty body) and
// soft-delete envelopes (canonical envelope's `isDeleted: true`). Both
// trigger lens removal.
func (s *CoreKVSource) handle(evt substrate.KVEvent) {
	key := evt.Key
	kind := substrate.ClassifyKey(key)

	switch kind {
	case substrate.KindVertex:
		_, lensID, ok := substrate.ParseVertexKey(key)
		if !ok {
			return
		}
		// Vertex delete: purge tracking + emit removal.
		if evt.IsDeleted {
			s.mu.Lock()
			delete(s.lensVertices, lensID)
			_, existed := s.known[lensID]
			delete(s.known, lensID)
			delete(s.pendingSpecs, lensID)
			s.mu.Unlock()
			if existed {
				s.logger.Info("lens removed", "lensId", lensID)
			}
			return
		}
		// Inspect class to decide whether this is a lens vertex.
		var probe envelopeProbe
		if err := json.Unmarshal(evt.Value, &probe); err != nil {
			s.logger.Debug("core-kv subscribe: vertex envelope unmarshal failed",
				"key", key, "err", err)
			return
		}
		if probe.Class != lensClassValue {
			// Some other meta vertex (DDL, event, etc.). Not our concern.
			return
		}
		s.mu.Lock()
		s.lensVertices[lensID] = struct{}{}
		buffered, hasBuffered := s.pendingSpecs[lensID]
		if hasBuffered {
			delete(s.pendingSpecs, lensID)
		}
		s.mu.Unlock()
		if hasBuffered {
			s.dispatchSpec(lensID, buffered, evt.Revision)
		}
		return

	case substrate.KindAspect:
		_, _, lensID, localName, ok := substrate.ParseAspectKey(key)
		if !ok {
			return
		}
		if localName != "spec" {
			return
		}
		if evt.IsDeleted {
			// Spec deletion = lens removed.
			s.mu.Lock()
			_, existed := s.known[lensID]
			delete(s.known, lensID)
			delete(s.pendingSpecs, lensID)
			s.mu.Unlock()
			if existed {
				s.logger.Info("lens removed (spec deleted)", "lensId", lensID)
			}
			return
		}
		s.mu.RLock()
		_, isLens := s.lensVertices[lensID]
		s.mu.RUnlock()
		if !isLens {
			// Parent vertex's class not yet observed (or not a lens).
			// Buffer the body in case the vertex arrives next.
			s.mu.Lock()
			s.pendingSpecs[lensID] = append([]byte(nil), evt.Value...)
			s.mu.Unlock()
			s.logger.Debug("buffering spec until parent meta-vertex class observed",
				"key", key, "lensId", lensID)
			return
		}
		s.dispatchSpec(lensID, evt.Value, evt.Revision)
		return
	}
	// Unknown / link / malformed → ignore.
}

// dispatchSpec parses a LensSpec body, translates it to a *Rule, and
// invokes load or update callbacks as appropriate.
//
// The body may either be a bare LensSpec JSON object (the legacy
// Processor-written form) or a substrate aspect envelope whose `data`
// field carries the LensSpec (the form bootstrap-seeded primordial lenses
// use). Probe the shape: if the body unmarshals to a struct with a non-empty
// `cypherRule`, take it verbatim; otherwise look for `data.cypherRule` and
// re-decode from that sub-object.
func (s *CoreKVSource) dispatchSpec(lensID string, body []byte, revision uint64) {
	specBody, err := unwrapSpecBody(body)
	if err != nil {
		s.logger.Error("lens spec unwrap failed", "lensId", lensID, "err", err)
		return
	}
	if isEventStreamSpec(specBody) {
		// Chronicler-owned (internal/chronicler discovers and projects these
		// independently from its own vtx.meta.> watch) — Refractor no longer
		// hosts eventStream lenses (chronicler-host-reconciliation). Skipped
		// silently, the same way a non-meta.lens vertex is skipped, rather
		// than falling through to translateSpec's cypherRule-required error.
		return
	}
	var spec LensSpec
	if err := json.Unmarshal(specBody, &spec); err != nil {
		s.logger.Error("lens spec unmarshal failed", "lensId", lensID, "err", err)
		return
	}
	if spec.ID == "" {
		spec.ID = lensID
	}
	rule, err := translateSpec(&spec)
	if err != nil {
		s.logger.Error("lens spec translation failed", "lensId", lensID, "err", err)
		return
	}
	rule.Sequence = revision

	s.mu.Lock()
	old, exists := s.known[lensID]
	s.known[lensID] = rule
	s.mu.Unlock()

	if !exists {
		s.logger.Info("lens loaded", "lensId", lensID, "canonicalName", spec.CanonicalName)
		if s.loadCB != nil {
			s.loadCB(rule)
		}
		return
	}
	if s.updateCB != nil {
		kindU := ClassifyUpdate(old, rule)
		s.updateCB(old, rule, kindU)
	}
}

// translateSpec converts a LensSpec into a *Rule that the existing
// Refractor pipeline machinery can consume. Returns an error if
// required fields are missing or the target config can't be unmarshalled.
func translateSpec(spec *LensSpec) (*Rule, error) {
	if spec.ID == "" {
		return nil, fmt.Errorf("lens spec: id required")
	}

	if strings.TrimSpace(spec.CypherRule) == "" {
		return nil, fmt.Errorf("lens %q: cypherRule required", spec.ID)
	}

	r := &Rule{
		ID:             spec.ID,
		Match:          spec.CypherRule,
		RuleEngine:     spec.Engine,
		CanonicalName:  spec.CanonicalName,
		ProjectionKind: spec.ProjectionKind,
		Output:         spec.Output,
	}

	switch spec.TargetType {
	case "postgres":
		var cfg TargetPostgresConfig
		if err := json.Unmarshal(spec.TargetConfig, &cfg); err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig unmarshal: %w", spec.ID, err)
		}
		// A grant lens projects to the shared actor_read_grants table via the
		// seq-guarded writer; its table + composite key default from the platform
		// (the lens need only RETURN actor_id/anchor_id/grant_source).
		table, key := cfg.Table, cfg.Key
		if cfg.GrantTable {
			if table == "" {
				table = adapter.GrantTable
			}
			if len(key) == 0 {
				key = append([]string(nil), adapter.GrantKeyColumns...)
			}
		}
		// A package-declared protected/grant lens carries posture + columns, not a
		// deployment DSN — so an empty DSN resolves from REFRACTOR_PG_DSN at
		// activation (the same env source the bootstrap contract_view lens uses).
		// This keeps the connection string out of the package manifest; the
		// resolved value still fails closed below if neither is set.
		dsn := cfg.DSN
		if dsn == "" {
			dsn = os.Getenv("REFRACTOR_PG_DSN")
		}
		if dsn == "" || table == "" || len(key) == 0 {
			return nil, fmt.Errorf("lens %q: targetConfig.{dsn,table,key} required for postgres (dsn may be left empty to resolve from REFRACTOR_PG_DSN at activation)", spec.ID)
		}
		if cfg.Protected && cfg.Public {
			return nil, fmt.Errorf("lens %q: targetConfig cannot be both protected and public", spec.ID)
		}
		if cfg.Protected && cfg.GrantTable {
			return nil, fmt.Errorf("lens %q: a grant-table lens is not a protected business model (set neither protected nor public)", spec.ID)
		}
		if cfg.Public && cfg.GrantTable {
			return nil, fmt.Errorf("lens %q: a grant-table lens is not a public business model (set neither protected nor public)", spec.ID)
		}
		if !cfg.Protected && !cfg.Public && !cfg.GrantTable {
			return nil, fmt.Errorf("lens %q: targetConfig must declare protected, public, or grantTable — a postgres business read model is protected by default and undeclared posture fails closed (Contract #6 §6.14)", spec.ID)
		}
		cols, arrayCols, err := translatePostgresColumns(spec.ID, cfg)
		if err != nil {
			return nil, err
		}
		if err := validateSecureColumns(spec, cfg); err != nil {
			return nil, err
		}
		dm, err := adapter.ParseDeleteMode(cfg.DeleteMode)
		if err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig.deleteMode: %w", spec.ID, err)
		}
		if err := validateProtectedDeleteMode(spec.ID, cfg.Protected, dm); err != nil {
			return nil, err
		}
		r.Into = IntoConfig{
			Target:          "postgres",
			DSN:             dsn,
			Table:           table,
			Key:             KeyField(key),
			QueryTimeoutRaw: cfg.QueryTimeout,
			QueryTimeout:    parseTimeoutOrDefault(cfg.QueryTimeout, 30*time.Second),
			DeleteMode:      string(dm),
			Protected:       cfg.Protected,
			Public:          cfg.Public,
			GrantTable:      cfg.GrantTable,
			Columns:         cols,
			ArrayColumns:    arrayCols,
			SecureColumns:   cfg.SecureColumns,
			DiffRetraction:  cfg.DiffRetraction,
		}
	case "nats_kv":
		var cfg TargetNATSKVConfig
		if err := json.Unmarshal(spec.TargetConfig, &cfg); err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig unmarshal: %w", spec.ID, err)
		}
		if cfg.Bucket == "" || len(cfg.Key) == 0 {
			return nil, fmt.Errorf("lens %q: targetConfig.{bucket,key} required for nats_kv", spec.ID)
		}
		// A protected read model must target postgres — RLS is the enforcement
		// boundary and NATS-KV has no row-level guard, so honoring protected:true
		// on a NATS-KV target would world-publish what the author believed was
		// access-controlled. A grant lens must target the shared Postgres
		// actor_read_grants. Either flag on a NATS-KV target fails closed at
		// activation (Contract #6 §6.14, D1 §3.3).
		if cfg.Protected {
			return nil, fmt.Errorf("lens %q: a protected read model must target postgres, not nats_kv (Contract #6 §6.14: NATS-KV has no row-level enforcement)", spec.ID)
		}
		if cfg.GrantTable {
			return nil, fmt.Errorf("lens %q: a grant-table lens must target postgres (the shared actor_read_grants table), not nats_kv (Contract #6 §6.14)", spec.ID)
		}
		if len(cfg.SecureColumns) > 0 {
			return nil, fmt.Errorf("lens %q: secureColumns (a Secure Lens, Contract #3 §3.10) must target a protected postgres model, not nats_kv — decrypted PII may only land behind RLS", spec.ID)
		}
		dm, err := adapter.ParseDeleteMode(cfg.DeleteMode)
		if err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig.deleteMode: %w", spec.ID, err)
		}
		r.Into = IntoConfig{
			Target:         "nats_kv",
			Bucket:         cfg.Bucket,
			Key:            KeyField(cfg.Key),
			DeleteMode:     string(dm),
			DiffRetraction: cfg.DiffRetraction,
		}
	case "nats_subject":
		var cfg TargetNATSSubjectConfig
		if err := json.Unmarshal(spec.TargetConfig, &cfg); err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig unmarshal: %w", spec.ID, err)
		}
		if cfg.SubjectPrefix == "" || cfg.Stream == "" || len(cfg.Key) == 0 {
			return nil, fmt.Errorf("lens %q: targetConfig.{subjectPrefix,stream,key} required for nats_subject", spec.ID)
		}
		if !slices.Contains(cfg.Key, adapter.PersonalActorKeyField) {
			return nil, fmt.Errorf("lens %q: targetConfig.key must include %q for nats_subject", spec.ID, adapter.PersonalActorKeyField)
		}
		r.Into = IntoConfig{
			Target:        "nats_subject",
			Key:           KeyField(cfg.Key),
			SubjectPrefix: cfg.SubjectPrefix,
			Stream:        cfg.Stream,
			Personal:      cfg.Personal,
		}
	default:
		return nil, fmt.Errorf("lens %q: unknown targetType %q (expected postgres|nats_kv|nats_subject)", spec.ID, spec.TargetType)
	}

	// Resolve the engine through the registry so the pipeline can route
	// per-engine. On success populate ResolvedEngine + CompiledRule;
	// on failure surface the SelectionError to the caller (dispatchSpec
	// logs and drops the spec).
	_, compiled, attempted, selErr := defaultRegistry.SelectForLens(ruleengine.LensDefinition{
		ID:         r.ID,
		RuleBody:   r.Match,
		RuleEngine: r.RuleEngine,
	})
	r.AttemptedEngines = attempted
	if selErr != nil {
		return nil, fmt.Errorf("lens %q: engine selection: %w", spec.ID, selErr)
	}
	r.ResolvedEngine = attempted[len(attempted)-1]
	r.CompiledRule = compiled
	return r, nil
}

// validateSecureColumns enforces the Secure-Lens declaration invariants
// (Contract #3 §3.10; vault-crypto-shredding-design.md §2.3): decrypted PII
// may only land in a protected (RLS) postgres business model, every secure
// column must be a declared, provisioned column so the plaintext's
// destination is explicit, and no secure column may be a platform RLS column
// (decrypted user-supplied data as the row's authz_anchors would make read
// authorization user-controllable) or an output-key column (the decryptor
// rewrites Row values only — a key would stay ciphertext). Fail-closed at
// spec-load time — a lens that would decrypt into the wrong posture never
// activates.
func validateSecureColumns(spec *LensSpec, cfg TargetPostgresConfig) error {
	if len(cfg.SecureColumns) == 0 {
		return nil
	}
	if !cfg.Protected {
		return fmt.Errorf("lens %q: secureColumns require protected: true — a Secure Lens projects plaintext PII and may only target an RLS-protected model (Contract #3 §3.10)", spec.ID)
	}
	if cfg.GrantTable {
		return fmt.Errorf("lens %q: secureColumns cannot be combined with grantTable (the grant table carries no business columns)", spec.ID)
	}
	if spec.ProjectionKind != "" {
		return fmt.Errorf("lens %q: secureColumns are supported on plain projection lenses only, not projectionKind %q", spec.ID, spec.ProjectionKind)
	}
	declared := make(map[string]struct{}, len(cfg.Columns))
	for _, c := range cfg.Columns {
		declared[c.Name] = struct{}{}
	}
	keyCols := make(map[string]struct{}, len(cfg.Key))
	for _, k := range cfg.Key {
		keyCols[k] = struct{}{}
	}
	reserved := map[string]struct{}{
		adapter.AuthzAnchorsColumn:  {},
		adapter.ProjectionSeqColumn: {},
		adapter.IsDeletedColumn:     {},
		adapter.DeletedAtColumn:     {},
	}
	seen := make(map[string]struct{}, len(cfg.SecureColumns))
	for _, sc := range cfg.SecureColumns {
		if sc.Column == "" || sc.IdentityKeyColumn == "" {
			return fmt.Errorf("lens %q: each secureColumns entry needs both column and identityKeyColumn", spec.ID)
		}
		if _, dup := seen[sc.Column]; dup {
			return fmt.Errorf("lens %q: secureColumns declares column %q twice", spec.ID, sc.Column)
		}
		seen[sc.Column] = struct{}{}
		if _, bad := reserved[sc.Column]; bad {
			return fmt.Errorf("lens %q: secure column %q is a platform RLS column — decrypted data must never drive read authorization or the write guard", spec.ID, sc.Column)
		}
		if _, isKey := keyCols[sc.Column]; isKey {
			return fmt.Errorf("lens %q: secure column %q is an output-key column — the projection key cannot be a ciphertext envelope", spec.ID, sc.Column)
		}
		if _, ok := declared[sc.Column]; !ok {
			return fmt.Errorf("lens %q: secure column %q is not among the declared targetConfig.columns", spec.ID, sc.Column)
		}
		if _, bad := reserved[sc.IdentityKeyColumn]; bad {
			return fmt.Errorf("lens %q: identityKeyColumn %q is a platform RLS column", spec.ID, sc.IdentityKeyColumn)
		}
		if _, ok := declared[sc.IdentityKeyColumn]; !ok {
			if _, isKey := keyCols[sc.IdentityKeyColumn]; !isKey {
				return fmt.Errorf("lens %q: identityKeyColumn %q is not among the declared targetConfig.columns or key columns — the adapter writes every row field as a table column, so an undeclared column fails at write time", spec.ID, sc.IdentityKeyColumn)
			}
		}
	}
	return nil
}

// validateProtectedDeleteMode rejects a protected lens declaring deleteMode
// "soft": a protected table's tombstone semantics are platform-owned, not
// lens-configurable — NewProtectedAdapter's guarded Delete always performs the
// seq-guarded soft tombstone (buildDeleteSQL) regardless of the adapter's
// deleteMode, so a lens declaring "soft" would imply a choice it doesn't
// actually have. Reused by translateSpec (spec-load time, the live activation
// path) and EmitReadPathDDL (DDL-emission time, the operator-facing CLI) so the
// two views of "is this lens spec coherent" cannot silently diverge — a spec
// loadable is DDL-emittable and vice versa.
func validateProtectedDeleteMode(lensID string, protected bool, dm adapter.DeleteMode) error {
	if protected && dm == adapter.DeleteModeSoft {
		return fmt.Errorf("lens %q: a protected read model's delete semantics are platform-owned (always a seq-guarded soft tombstone) — declaring deleteMode \"soft\" implies a lens-level choice that does not exist; leave deleteMode unset or \"hard\"", lensID)
	}
	return nil
}

// translatePostgresColumns converts a protected lens's declared columns into
// adapter.ColumnDef provisioning specs and derives the set of array columns the
// ProtectedAdapter must encode as Postgres arrays. The platform always treats
// authz_anchors as a text[] array; a declared column whose type ends in "[]" is
// also an array column. Returns empty slices for a non-protected lens (its table
// is provisioned out-of-band, and declared columns are ignored).
func translatePostgresColumns(lensID string, cfg TargetPostgresConfig) (cols []adapter.ColumnDef, arrayCols []string, err error) {
	if !cfg.Protected {
		return nil, nil, nil
	}
	arrayCols = []string{adapter.AuthzAnchorsColumn}
	for _, c := range cfg.Columns {
		if c.Name == "" || c.Type == "" {
			return nil, nil, fmt.Errorf("lens %q: targetConfig.columns entry needs both name and type", lensID)
		}
		cols = append(cols, adapter.ColumnDef{Name: c.Name, Type: c.Type})
		if strings.HasSuffix(strings.TrimSpace(c.Type), "[]") {
			arrayCols = append(arrayCols, c.Name)
		}
	}
	return cols, arrayCols, nil
}

// unwrapSpecBody returns either the original body (bare LensSpec) or the
// `data` sub-object (when the body is a substrate aspect envelope that
// wraps the LensSpec under `data`). Primordial bootstrap lenses seed
// LensSpec via the aspect envelope path.
func unwrapSpecBody(body []byte) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("probe spec body: %w", err)
	}
	// Bare LensSpec — has cypherRule at top level.
	if _, ok := probe["cypherRule"]; ok {
		return body, nil
	}
	// Envelope-wrapped — pull `data`.
	if data, ok := probe["data"]; ok {
		return data, nil
	}
	// Fall through — return original; the LensSpec unmarshal will
	// produce the "cypherRule required" downstream error which is
	// still the right thing to log.
	return body, nil
}

func parseTimeoutOrDefault(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
