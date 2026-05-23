package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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

// CoreKVSource subscribes to Core KV under `vtx.meta.>` via a Lattice-
// native durable JetStream consumer (substrate.SubscribeKVChanges) and
// routes only those updates whose envelope class is `meta.lens` to the
// lens loader. Other meta classes (`meta.ddl.*`, `meta.event.*`, etc.)
// are skipped silently — they belong to future routers, not the
// Refractor (data-contracts.md §1.2 line 70).
//
// It pushes loaded / updated / deleted events through the supplied load
// and update callbacks — the SAME callbacks the JetStream-backed Loader
// uses, so the rest of the pipeline lifecycle is unchanged (handoff brief
// Decision #5).
//
// Story 2.1: this REPLACES the MATERIALIZER_RULES JetStream loader as the
// source of lens definitions. Lens definitions arrive via the normal
// Processor write path as `vtx.meta.<NanoID>` (vertex, class `meta.lens`)
// + a `vtx.meta.<NanoID>.spec` aspect carrying the LensSpec body. CDC
// delivers them here through the durable consumer subscription and the
// class filter routes them to the loader (Story 2.1b correctness pass).
//
// Story 2.4b: migrated from jetstream.KeyValue.Watch (ephemeral, replays
// full history on every connect) to substrate.SubscribeKVChanges (durable
// JetStream consumer on the KV_<bucket> backing stream filtered to
// `$KV.<bucket>.vtx.meta.>`). Sequence position now persists across
// restarts. IncludeHistory=true is passed so the first connect after a
// fresh deployment still loads the entire installed lens set; subsequent
// restarts pick up from the durable ack floor.
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
	// simple-then-full fallback. Story 3.2a — set to "full" on the
	// primordial Capability Lens specs so the full engine handles them
	// without depending on simple's parser to fail first.
	Engine string `json:"engine,omitempty"`
}

// TargetPostgresConfig is the expected shape of LensSpec.TargetConfig
// when TargetType == "postgres".
type TargetPostgresConfig struct {
	DSN          string   `json:"dsn"`
	Table        string   `json:"table"`
	Key          []string `json:"key"`
	QueryTimeout string   `json:"queryTimeout"` // optional, e.g., "5s"
}

// TargetNATSKVConfig is the expected shape of LensSpec.TargetConfig
// when TargetType == "nats_kv".
type TargetNATSKVConfig struct {
	Bucket string   `json:"bucket"`
	Key    []string `json:"key"`
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
// field carries the LensSpec (the form bootstrap-seeded primordial
// lenses use — Story 3.2a Phase D). Probe the shape: if the body
// unmarshals to a struct with a non-empty `cypherRule`, take it
// verbatim; otherwise look for `data.cypherRule` and re-decode from
// that sub-object.
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
		ID:            spec.ID,
		Match:         spec.CypherRule,
		RuleEngine:    spec.Engine,
		CanonicalName: spec.CanonicalName,
	}

	switch spec.TargetType {
	case "postgres":
		var cfg TargetPostgresConfig
		if err := json.Unmarshal(spec.TargetConfig, &cfg); err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig unmarshal: %w", spec.ID, err)
		}
		if cfg.DSN == "" || cfg.Table == "" || len(cfg.Key) == 0 {
			return nil, fmt.Errorf("lens %q: targetConfig.{dsn,table,key} required for postgres", spec.ID)
		}
		r.Into = IntoConfig{
			Target:          "postgres",
			DSN:             cfg.DSN,
			Table:           cfg.Table,
			Key:             KeyField(cfg.Key),
			QueryTimeoutRaw: cfg.QueryTimeout,
			QueryTimeout:    parseTimeoutOrDefault(cfg.QueryTimeout, 30*time.Second),
		}
	case "nats_kv":
		var cfg TargetNATSKVConfig
		if err := json.Unmarshal(spec.TargetConfig, &cfg); err != nil {
			return nil, fmt.Errorf("lens %q: targetConfig unmarshal: %w", spec.ID, err)
		}
		if cfg.Bucket == "" || len(cfg.Key) == 0 {
			return nil, fmt.Errorf("lens %q: targetConfig.{bucket,key} required for nats_kv", spec.ID)
		}
		r.Into = IntoConfig{
			Target: "nats_kv",
			Bucket: cfg.Bucket,
			Key:    KeyField(cfg.Key),
		}
	default:
		return nil, fmt.Errorf("lens %q: unknown targetType %q (expected postgres|nats_kv)", spec.ID, spec.TargetType)
	}

	// Story 3.2a: resolve the engine through the registry so the pipeline
	// can route per-engine (Decision #2). On success populate
	// ResolvedEngine + CompiledRule; on failure surface the SelectionError
	// to the caller (dispatchSpec logs and drops the spec).
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

// unwrapSpecBody returns either the original body (bare LensSpec) or
// the `data` sub-object (when the body is a substrate aspect envelope
// that wraps the LensSpec under `data`). Per Story 3.2a Phase D the
// primordial bootstrap seeds LensSpec via the aspect envelope path.
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
