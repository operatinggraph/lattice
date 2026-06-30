package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/substrate"
)

// lensSourceDurableName is the JetStream durable consumer name shared by
// the per-instance lens-definition subscription. Multi-cell deployments
// (Phase 3) will include a cell-id segment.
const lensSourceDurableName = "refractor-lens-source"

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
// The durable consumer (substrate.SubscribeKVChanges) persists its ack
// floor across restarts. IncludeHistory=true is passed so the first
// connect after a fresh deployment still loads the entire installed lens
// set; subsequent restarts pick up from the ack floor.
type CoreKVSource struct {
	conn     *substrate.Conn
	bucket   string
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

// LensSpec mirrors the JSON aspect body stored at
// `vtx.meta.<NanoID>.spec` (parent vertex class `meta.lens`). See
// MORPH-DEVIATIONS Deviation 11.
type LensSpec struct {
	ID            string          `json:"id"`            // lens NanoID; matches the key segment
	CanonicalName string          `json:"canonicalName"` // e.g., "lens.contract-view"
	TargetType    string          `json:"targetType"`    // "postgres" | "nats_kv"
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
}

// PostgresColumn declares one provisioned column of a protected read-model
// table: Type is the verbatim Postgres column type (e.g. "text", "bigint").
type PostgresColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
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
	// regular bucket.
	Protected  bool `json:"protected,omitempty"`
	GrantTable bool `json:"grantTable,omitempty"`
}

// NewCoreKVSource constructs a watcher. logger may be nil.
func NewCoreKVSource(conn *substrate.Conn, bucket string, logger *slog.Logger) *CoreKVSource {
	if logger == nil {
		logger = slog.Default()
	}
	return &CoreKVSource{
		conn:         conn,
		bucket:       bucket,
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
// IncludeHistory=true preserves the pre-2.4b "replay all meta vertices
// on first connect" behaviour. On subsequent restarts the durable
// consumer's ack floor picks up where the previous session left off, so
// re-replay is bounded by the unacked tail.
func (s *CoreKVSource) Start(ctx context.Context) error {
	events, err := s.conn.SubscribeKVChanges(
		ctx,
		s.bucket,
		"vtx.meta.",
		lensSourceDurableName,
		substrate.SubscribeKVOptions{
			IncludeHistory: true,
			Logger:         s.logger,
		},
	)
	if err != nil {
		return fmt.Errorf("subscribe core KV vtx.meta.>: %w", err)
	}
	go s.consume(ctx, events)
	return nil
}

func (s *CoreKVSource) consume(ctx context.Context, events <-chan substrate.KVEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			s.handle(evt)
		}
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
		cols, arrayCols, err := translatePostgresColumns(spec.ID, cfg)
		if err != nil {
			return nil, err
		}
		dm, err := adapter.ParseDeleteMode(cfg.DeleteMode)
		if err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig.deleteMode: %w", spec.ID, err)
		}
		// A protected business table is provisioned out-of-band from
		// BuildProtectedTableDDL, which carries no is_deleted/deleted_at columns,
		// and the §6.14 set-membership policy does not filter on is_deleted (only
		// the grant table tombstones). A soft delete (UPDATE … SET is_deleted=true,
		// deleted_at=NOW()) would therefore hit a missing column on every delete —
		// a write error the pump misclassifies as transient and retries forever.
		// Reject the unsupported combination at spec load (fail-closed, like the
		// protected/public and protected/grantTable conflicts above) rather than
		// activating into that loop.
		if cfg.Protected && dm == adapter.DeleteModeSoft {
			return nil, fmt.Errorf("lens %q: a protected read model cannot use deleteMode \"soft\" — the RLS table has no is_deleted column and the §6.14 policy does not filter it; use the default \"hard\" delete", spec.ID)
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
		dm, err := adapter.ParseDeleteMode(cfg.DeleteMode)
		if err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig.deleteMode: %w", spec.ID, err)
		}
		r.Into = IntoConfig{
			Target:     "nats_kv",
			Bucket:     cfg.Bucket,
			Key:        KeyField(cfg.Key),
			DeleteMode: string(dm),
		}
	default:
		return nil, fmt.Errorf("lens %q: unknown targetType %q (expected postgres|nats_kv)", spec.ID, spec.TargetType)
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
