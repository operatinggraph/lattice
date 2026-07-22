package loom

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// patternSourceDurablePrefix is the JetStream durable-consumer name prefix for
// the single pattern source (1 of the 1+N durables). A per-engine instance
// segment plus a per-boot nonce are appended (see start) so each boot gets a
// never-before-seen durable name and replays the full installed pattern set
// via IncludeHistory.
//
// Why a per-boot durable rather than one ack-floor-resuming name: the pattern
// registry is DERIVED in-memory state (the binding registry + step defs an
// instance needs to advance), exactly like the token index. A durable that
// resumes from its ack floor would replay nothing on restart, leaving the
// registry cold and a resumed instance unable to load its pattern. The source
// has no side effects — replaying the meta set is idempotent — so full replay
// on every connect is the correct semantics. It remains a durable JetStream
// consumer (not an ephemeral kv.Watch, not a one-shot point-read), and a
// pattern installed after startup still registers live via the same callback.
//
// The nonce (not just Instance) is load-bearing: JetStream only honors
// DeliverPolicy when a durable is first created — CreateOrUpdateConsumer
// against an EXISTING durable of the same name resumes from its persisted ack
// floor regardless of the DeliverPolicy requested, so a stable, operator-set
// Instance (recommended by docs/components/loom.md for dashboards/alerting
// across restarts) would silently defeat full-replay-on-every-connect and
// leave a crash-restarted engine's registry cold. Appending a fresh nonce each
// boot guarantees the durable has never existed before, independent of
// whether Instance is stable or auto-generated. Multi-cell deployments (Phase
// 3) will include a cell-id segment.
const patternSourceDurablePrefix = "loom-pattern-source"

// loomPatternClass is the canonical envelope class for loom-pattern
// meta-vertices. Other meta classes under vtx.meta.> are skipped silently.
const loomPatternClass = "meta.loomPattern"

// patternSource is Loom's pattern-definition loader. It subscribes to Core KV
// under vtx.meta.> via a durable JetStream consumer (Conn.SubscribeKVChanges)
// and routes only those updates whose envelope class is meta.loomPattern to
// the load/update callbacks — exactly mirroring Refractor's CoreKVSource.
//
// Patterns arrive via the normal Processor write path as vtx.meta.<NanoID>
// (class meta.loomPattern) plus a vtx.meta.<NanoID>.spec aspect carrying the
// pattern body. IncludeHistory replays the installed pattern set on first
// connect; subsequent restarts resume from the durable ack floor. A pattern
// installed after startup registers live via the same callback — no engine
// restart (the load-bearing reason §10.5 loads patterns "via CDC like a Lens
// def").
type patternSource struct {
	conn     *substrate.Conn
	bucket   string
	instance string
	logger   *slog.Logger

	loadCB   func(*Pattern)
	updateCB func(old, new *Pattern)

	mu              sync.Mutex
	known           map[string]*Pattern // patternId → last-loaded pattern
	patternVertices map[string]struct{} // ids of vtx.meta.<id> with class meta.loomPattern
	pendingSpecs    map[string][]byte   // spec bodies seen before their parent vertex's class
	opMetaByType    map[string]string   // operationType → vtx.meta.<opId> (userTask forOperation resolution)
}

func newPatternSource(conn *substrate.Conn, bucket, instance string, logger *slog.Logger) *patternSource {
	if logger == nil {
		logger = slog.Default()
	}
	return &patternSource{
		conn:            conn,
		bucket:          bucket,
		instance:        instance,
		logger:          logger,
		known:           make(map[string]*Pattern),
		patternVertices: make(map[string]struct{}),
		pendingSpecs:    make(map[string][]byte),
		opMetaByType:    make(map[string]string),
	}
}

func (s *patternSource) setLoadCallback(fn func(*Pattern))            { s.loadCB = fn }
func (s *patternSource) setUpdateCallback(fn func(old, new *Pattern)) { s.updateCB = fn }

// start establishes the durable subscription and launches the dispatch
// goroutine. Returns once the subscription is established. IncludeHistory is
// set so every boot — fresh deployment or restart alike — replays the entire
// installed pattern set (see patternSourceDurablePrefix for why a genuinely
// never-before-seen durable name is required to make that true).
//
// Each boot's durable name carries the instance segment (attributability) plus
// a fresh per-boot nonce (uniqueness — see patternSourceDurablePrefix), so a
// prior boot's durable is never reused and would otherwise linger forever as a
// parked consumer on KV_<bucket>. Before creating its own durable, start
// prunes any stale "<prefix>-*" durables left behind by no-longer-running
// instances; the durable created below is then deleted on clean shutdown
// (consume's ctx.Done branch) so it never becomes next boot's stale entry.
func (s *patternSource) start(ctx context.Context) error {
	bootNonce, err := substrate.NewNanoID()
	if err != nil {
		return fmt.Errorf("loom: pattern source boot nonce: %w", err)
	}
	durable := patternSourceDurablePrefix + "-" + s.instance + "-" + bootNonce
	if err := s.conn.PruneStaleDurables(ctx, s.bucket, patternSourceDurablePrefix+"-", durable, s.logger); err != nil {
		s.logger.Warn("loom: prune stale pattern-source durables failed", "err", err)
	}
	events, err := s.conn.SubscribeKVChanges(
		ctx,
		s.bucket,
		"vtx.meta.",
		durable,
		substrate.SubscribeKVOptions{IncludeHistory: true, Logger: s.logger},
	)
	if err != nil {
		return fmt.Errorf("loom: subscribe core KV vtx.meta.>: %w", err)
	}
	go s.consume(ctx, events, durable)
	return nil
}

func (s *patternSource) consume(ctx context.Context, events <-chan substrate.KVEvent, durable string) {
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

// deleteOwnDurable removes this boot's per-instance durable on clean shutdown
// so it never lingers as a stale entry for the next boot's PruneStaleDurables
// to clean up. Best-effort: ctx is already cancelled, so a fresh background
// context with a short bound is used for the delete call.
func (s *patternSource) deleteOwnDurable(durable string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.conn.DeleteDurable(ctx, s.bucket, durable); err != nil {
		s.logger.Warn("loom: delete own pattern-source durable failed", "durable", durable, "err", err)
	}
}

type classProbe struct {
	Class string `json:"class"`
}

// handle dispatches one KV mutation under vtx.meta.>. Routing mirrors
// CoreKVSource: a vertex carrying class meta.loomPattern is registered as a
// pattern vertex (replaying any buffered spec); an aspect named `spec` under a
// known pattern vertex is parsed into a Pattern and dispatched. CDC ordering is
// not guaranteed, so a spec seen before its parent vertex's class is buffered.
func (s *patternSource) handle(evt substrate.KVEvent) {
	kind := substrate.ClassifyKey(evt.Key)
	switch kind {
	case substrate.KindVertex:
		_, id, ok := substrate.ParseVertexKey(evt.Key)
		if !ok {
			return
		}
		if evt.IsDeleted {
			s.removePattern(id)
			s.removeOpMeta(id)
			return
		}
		var probe classProbe
		if err := json.Unmarshal(evt.Value, &probe); err != nil {
			s.logger.Debug("loom: vertex envelope unmarshal failed", "key", evt.Key, "err", err)
			return
		}
		if probe.Class != loomPatternClass {
			s.indexOpMeta(evt.Key, evt.Value)
			return
		}
		s.mu.Lock()
		s.patternVertices[id] = struct{}{}
		buffered, has := s.pendingSpecs[id]
		if has {
			delete(s.pendingSpecs, id)
		}
		s.mu.Unlock()
		if has {
			s.dispatchSpec(id, buffered)
		}

	case substrate.KindAspect:
		_, _, id, localName, ok := substrate.ParseAspectKey(evt.Key)
		if !ok || localName != "spec" {
			return
		}
		if evt.IsDeleted {
			s.removePattern(id)
			return
		}
		s.mu.Lock()
		_, isPattern := s.patternVertices[id]
		if !isPattern {
			s.pendingSpecs[id] = append([]byte(nil), evt.Value...)
		}
		s.mu.Unlock()
		if isPattern {
			s.dispatchSpec(id, evt.Value)
		}
	}
}

// dispatchSpec parses a pattern spec body and fires the load or update
// callback. The body is either a bare Pattern object or a substrate aspect
// envelope wrapping the Pattern under `data`.
func (s *patternSource) dispatchSpec(id string, body []byte) {
	specBody, err := unwrapPatternBody(body)
	if err != nil {
		s.logger.Error("loom: pattern spec unwrap failed", "patternId", id, "err", err)
		return
	}
	var p Pattern
	if err := json.Unmarshal(specBody, &p); err != nil {
		s.logger.Error("loom: pattern spec unmarshal failed", "patternId", id, "err", err)
		return
	}
	if p.PatternID == "" {
		p.PatternID = id
	}
	// MetaKey is the source vertex's canonical key — always the real
	// vtx.meta.<NanoID>, independent of the (possibly human-named) PatternID.
	p.MetaKey = "vtx.meta." + id
	if err := p.validate(); err != nil {
		s.logger.Error("loom: pattern rejected", "patternId", id, "err", err)
		return
	}
	if p.userTaskCompletionUnobservable() {
		s.logger.Warn("loom: userTask pattern completionDomains omits the orchestration domain — userTask completions will never be observed",
			"patternId", id, "completionDomains", p.Domains())
	}
	if p.externalTaskCompletionUnobservable() {
		s.logger.Warn("loom: externalTask pattern completionDomains omits the orchestration domain — externalTask completions will never be observed",
			"patternId", id, "completionDomains", p.Domains())
	}

	s.mu.Lock()
	old, exists := s.known[id]
	s.known[id] = &p
	s.mu.Unlock()

	if !exists {
		s.logger.Info("loom: pattern loaded", "patternId", id, "steps", len(p.Steps))
		if s.loadCB != nil {
			s.loadCB(&p)
		}
		return
	}
	s.logger.Info("loom: pattern updated", "patternId", id, "steps", len(p.Steps))
	if s.updateCB != nil {
		s.updateCB(old, &p)
	}
}

func (s *patternSource) removePattern(id string) {
	s.mu.Lock()
	_, existed := s.known[id]
	delete(s.patternVertices, id)
	delete(s.known, id)
	delete(s.pendingSpecs, id)
	s.mu.Unlock()
	if existed {
		s.logger.Info("loom: pattern removed", "patternId", id)
		if s.updateCB != nil {
			s.updateCB(nil, nil)
		}
	}
}

// opMetaProbe reads the operationType scalar off an op meta-vertex envelope.
type opMetaProbe struct {
	Data struct {
		OperationType string `json:"operationType"`
	} `json:"data"`
}

// indexOpMeta records the operationType → vtx.meta.<opId> mapping for a
// non-pattern meta-vertex that carries data.operationType. A userTask step
// names its bound op by operationType (Contract #10 §10.5); the engine resolves
// that to the op's meta-vertex key (the CreateTask forOperation endpoint) from
// this index, built off the same vtx.meta.> CDC the pattern source already
// consumes — no Core-KV scan, no separate index key shape.
func (s *patternSource) indexOpMeta(vertexKey string, body []byte) {
	var probe opMetaProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		return
	}
	if probe.Data.OperationType == "" {
		return
	}
	s.mu.Lock()
	s.opMetaByType[probe.Data.OperationType] = vertexKey
	s.mu.Unlock()
}

// removeOpMeta drops any operationType entry pointing at the deleted op
// meta-vertex id (vtx.meta.<id>).
func (s *patternSource) removeOpMeta(id string) {
	key := "vtx.meta." + id
	s.mu.Lock()
	for ot, k := range s.opMetaByType {
		if k == key {
			delete(s.opMetaByType, ot)
		}
	}
	s.mu.Unlock()
}

// opMetaKey returns the vtx.meta.<opId> for an operationType, or ("", false)
// when no op meta-vertex with that operationType has been observed.
func (s *patternSource) opMetaKey(operationType string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.opMetaByType[operationType]
	return k, ok
}

// get returns the last-loaded pattern for patternId.
func (s *patternSource) get(patternID string) (*Pattern, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.known[patternID]
	return p, ok
}

// snapshot returns a copy of every currently-registered pattern. Used to
// rebuild the binding registry on each pattern add/remove.
func (s *patternSource) snapshot() []*Pattern {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Pattern, 0, len(s.known))
	for _, p := range s.known {
		out = append(out, p)
	}
	return out
}

// unwrapPatternBody returns either the original body (bare Pattern) or the
// `data` sub-object when the body is a substrate aspect envelope wrapping the
// Pattern under `data` (the form bootstrap/package-seeded specs use).
func unwrapPatternBody(body []byte) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("probe pattern body: %w", err)
	}
	if _, ok := probe["steps"]; ok {
		return body, nil
	}
	if data, ok := probe["data"]; ok {
		return data, nil
	}
	return body, nil
}

// bindingRegistry maps the set of referenced event domains across all
// registered patterns. Its only job is choosing which domains to subscribe to
// (D2: one consumer per domain) — completion is correlated by requestId in the
// event body, never by a registry-named event-type.
func bindingRegistry(patterns []*Pattern) map[string]struct{} {
	domains := make(map[string]struct{})
	for _, p := range patterns {
		for _, d := range p.Domains() {
			domains[d] = struct{}{}
		}
	}
	return domains
}
