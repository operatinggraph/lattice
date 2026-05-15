package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

// CoreKVSource watches Core KV under `vtx.meta.>` and routes only those
// updates whose envelope class is `meta.lens` to the lens loader. Other
// meta classes (`meta.ddl.*`, `meta.event.*`, etc.) are skipped silently
// — they belong to future routers, not the Refractor (data-contracts.md
// §1.2 line 70).
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
// delivers them here through the Core KV watch and the class filter
// routes them to the loader (Story 2.1b correctness pass).
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

// Start launches the Core KV watch goroutine. Returns when the watch is
// established (or fails to establish). The goroutine runs until ctx is
// cancelled.
func (s *CoreKVSource) Start(ctx context.Context) error {
	js := s.conn.JetStream()
	kv, err := js.KeyValue(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("open core KV %q: %w", s.bucket, err)
	}
	// Watch all meta-vertices and their aspects. Per data-contracts.md
	// §1.2 line 70, lens definitions are meta-vertices distinguished by
	// envelope class `meta.lens`. We route by class, not by key prefix.
	// Watch starts from existing entries so on restart we re-load all
	// known lenses.
	watcher, err := kv.Watch(ctx, "vtx.meta.>", jetstream.IncludeHistory())
	if err != nil {
		return fmt.Errorf("kv watch vtx.meta.>: %w", err)
	}
	go s.consume(ctx, watcher)
	return nil
}

func (s *CoreKVSource) consume(ctx context.Context, watcher jetstream.KeyWatcher) {
	defer func() {
		if err := watcher.Stop(); err != nil {
			s.logger.Warn("core-kv lens watcher stop", "err", err)
		}
	}()
	updates := watcher.Updates()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-updates:
			if !ok {
				return
			}
			if entry == nil {
				// Initial-state-complete sentinel — nothing to do.
				continue
			}
			s.handle(entry)
		}
	}
}

// handle dispatches one KV update from `vtx.meta.>`. Routing is driven
// by the envelope `class` field (data-contracts.md §1.2 line 70):
//   - vertex (3-seg) with class `meta.lens`: register as a lens vertex;
//     replay any buffered spec aspect.
//   - vertex (3-seg) with any other class (`meta.ddl.*`, `meta.event.*`,
//     etc.): skip silently — not the Refractor's concern.
//   - aspect (4-seg) under a known lens vertex with localName `spec`:
//     parse + translate + dispatch to loader.
//   - aspect (4-seg) under an unknown parent: buffer until the parent
//     vertex's class is observed (CDC ordering is not guaranteed).
func (s *CoreKVSource) handle(entry jetstream.KeyValueEntry) {
	key := entry.Key()
	kind := substrate.ClassifyKey(key)
	op := entry.Operation()

	switch kind {
	case substrate.KindVertex:
		_, lensID, ok := substrate.ParseVertexKey(key)
		if !ok {
			return
		}
		// Vertex delete: purge tracking + emit removal.
		if op == jetstream.KeyValueDelete || op == jetstream.KeyValuePurge {
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
		if err := json.Unmarshal(entry.Value(), &probe); err != nil {
			s.logger.Debug("core-kv watch: vertex envelope unmarshal failed",
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
			s.dispatchSpec(lensID, buffered, entry.Revision())
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
		if op == jetstream.KeyValueDelete || op == jetstream.KeyValuePurge {
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
			s.pendingSpecs[lensID] = append([]byte(nil), entry.Value()...)
			s.mu.Unlock()
			s.logger.Debug("buffering spec until parent meta-vertex class observed",
				"key", key, "lensId", lensID)
			return
		}
		s.dispatchSpec(lensID, entry.Value(), entry.Revision())
		return
	}
	// Unknown / link / malformed → ignore.
}

// dispatchSpec parses a LensSpec body, translates it to a *Rule, and
// invokes load or update callbacks as appropriate.
func (s *CoreKVSource) dispatchSpec(lensID string, body []byte, revision uint64) {
	var spec LensSpec
	if err := json.Unmarshal(body, &spec); err != nil {
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
// Materializer pipeline machinery can consume. Returns an error if
// required fields are missing or the target config can't be unmarshalled.
func translateSpec(spec *LensSpec) (*Rule, error) {
	if spec.ID == "" {
		return nil, fmt.Errorf("lens spec: id required")
	}
	if strings.TrimSpace(spec.CypherRule) == "" {
		return nil, fmt.Errorf("lens %q: cypherRule required", spec.ID)
	}

	r := &Rule{
		ID:    spec.ID,
		Team:  "lattice", // Story 2.1 retains a constant Team (see MORPH-DEVIATIONS Deviation 4)
		Match: spec.CypherRule,
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
	return r, nil
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
