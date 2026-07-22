package chronicler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// lensClassValue is the canonical envelope class for lens meta-vertices
// (data-contracts.md §1.2 line 70) — the same value Refractor's
// CoreKVSource filters on.
const lensClassValue = "meta.lens"

// hostDurablePrefix names Host's own `vtx.meta.>` discovery durable —
// distinct from each Manager's own "chronicler-<lensId>" durable on
// core-events. Per-instance + a boot nonce (mirrors Weaver's targetSource),
// pruning any stale durable a prior boot left behind.
const hostDurablePrefix = "chronicler-defs"

// HostConfig configures a Host.
type HostConfig struct {
	Conn         *substrate.Conn
	CoreKVBucket string // Core KV bucket to watch vtx.meta.> on
	EventsStream string // the JetStream stream backing core-events
	Instance     string // this process's instance id, for the discovery durable's boot nonce
	NewAdapter   func(bucket string, keyField string, deleteMode adapter.DeleteMode) (adapter.Adapter, error)
	Logger       *slog.Logger
}

// runningManager tracks one active eventStream Manager and how to stop it.
type runningManager struct {
	def    *Definition
	cancel context.CancelFunc
	done   chan struct{}
}

// Host discovers eventStream-kind lens definitions from `vtx.meta.>` and
// runs one Manager per definition, mirroring Refractor's CoreKVSource
// structure but restricted to the eventStream shape only — a coreKv (or any
// other kind) definition is silently skipped, exactly as Refractor's
// CoreKVSource skips a non-`meta.lens` vertex; that stays Refractor's
// concern, unchanged by this extraction.
//
// A definition update is NOT hot-reloaded (mirrors the pre-extraction
// behavior in cmd/refractor's updateCB, which explicitly refused eventStream
// lens updates): the discovery loop logs and keeps the already-running
// Manager, so an operator must restart the process to pick up a changed
// definition.
type Host struct {
	cfg    HostConfig
	logger *slog.Logger

	mu           sync.Mutex
	classes      map[string]string // vertex id -> class
	pendingSpecs map[string][]byte // vertex id -> last-seen spec body, buffered ahead of its class
	running      map[string]*runningManager
}

// NewHost constructs a Host. cfg.NewAdapter builds the write-side Adapter for
// one definition (v1: always a *adapter.NatsKVAdapter, injected so tests can
// substitute a fake); nil defaults to natsKVAdapterFactory.
func NewHost(cfg HostConfig) *Host {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.NewAdapter == nil {
		cfg.NewAdapter = natsKVAdapterFactory(cfg.Conn)
	}
	return &Host{
		cfg:          cfg,
		logger:       cfg.Logger,
		classes:      make(map[string]string),
		pendingSpecs: make(map[string][]byte),
		running:      make(map[string]*runningManager),
	}
}

// natsKVAdapterFactory returns the default NewAdapter: open (or create) the
// named NATS-KV bucket and wrap it in a guarded NatsKVAdapter — the same
// guarded-CAS posture the pre-extraction eventStream pipeline used.
func natsKVAdapterFactory(conn *substrate.Conn) func(bucket, keyField string, deleteMode adapter.DeleteMode) (adapter.Adapter, error) {
	return func(bucket, keyField string, deleteMode adapter.DeleteMode) (adapter.Adapter, error) {
		kv, err := conn.OpenKV(context.Background(), bucket)
		if err != nil {
			return nil, fmt.Errorf("open KV bucket %q: %w", bucket, err)
		}
		a, err := adapter.New(kv, []string{keyField}, deleteMode)
		if err != nil {
			return nil, err
		}
		a.SetGuarded(true)
		return a, nil
	}
}

// Start subscribes to `vtx.meta.>` and launches the dispatch goroutine.
// Returns once the subscription is established. The goroutine — and every
// Manager it starts — runs until ctx is cancelled.
func (h *Host) Start(ctx context.Context) error {
	bootNonce, err := substrate.NewNanoID()
	if err != nil {
		return fmt.Errorf("chronicler: host boot nonce: %w", err)
	}
	durable := hostDurablePrefix + "-" + h.cfg.Instance + "-" + bootNonce
	if err := h.cfg.Conn.PruneStaleDurables(ctx, h.cfg.CoreKVBucket, hostDurablePrefix+"-", durable, h.logger); err != nil {
		h.logger.Warn("chronicler: prune stale definition-source durables failed", "err", err)
	}
	events, err := h.cfg.Conn.SubscribeKVChanges(
		ctx,
		h.cfg.CoreKVBucket,
		"vtx.meta.",
		durable,
		substrate.SubscribeKVOptions{IncludeHistory: true, Logger: h.logger},
	)
	if err != nil {
		return fmt.Errorf("chronicler: subscribe core KV vtx.meta.>: %w", err)
	}
	go h.consume(ctx, events)
	return nil
}

func (h *Host) consume(ctx context.Context, events <-chan substrate.KVEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			h.handle(ctx, evt)
		}
	}
}

type lensEnvelopeProbe struct {
	Class string `json:"class"`
}

// handle dispatches one KV mutation from `vtx.meta.>` — see the type doc for
// the routing rules. hostCtx is the Host's own lifetime context, the parent
// for each Manager's per-definition context.
func (h *Host) handle(hostCtx context.Context, evt substrate.KVEvent) {
	key := evt.Key
	switch substrate.ClassifyKey(key) {
	case substrate.KindVertex:
		_, id, ok := substrate.ParseVertexKey(key)
		if !ok {
			return
		}
		if evt.IsDeleted {
			h.removeDefinition(id, "vertex deleted")
			h.mu.Lock()
			delete(h.classes, id)
			delete(h.pendingSpecs, id)
			h.mu.Unlock()
			return
		}
		var probe lensEnvelopeProbe
		if err := json.Unmarshal(evt.Value, &probe); err != nil {
			h.logger.Debug("chronicler: vertex envelope unmarshal failed", "key", key, "err", err)
			return
		}
		h.mu.Lock()
		h.classes[id] = probe.Class
		buffered, hasBuffered := h.pendingSpecs[id]
		if hasBuffered {
			delete(h.pendingSpecs, id)
		}
		h.mu.Unlock()
		if probe.Class != lensClassValue {
			// A vertex reclassified away from meta.lens (a genuine, if rare,
			// lens-lifecycle operation) must tear down its running Manager —
			// otherwise it would keep consuming its subject and writing its
			// target bucket indefinitely after Core KV no longer considers it
			// a lens at all. No-op if nothing was running.
			h.removeDefinition(id, "vertex reclassified away from meta.lens")
			return
		}
		if hasBuffered {
			h.dispatchSpec(hostCtx, id, buffered)
		}

	case substrate.KindAspect:
		_, _, id, localName, ok := substrate.ParseAspectKey(key)
		if !ok {
			return
		}
		if localName != "spec" {
			return
		}
		if evt.IsDeleted {
			h.removeDefinition(id, "spec deleted")
			return
		}
		h.mu.Lock()
		class, known := h.classes[id]
		h.mu.Unlock()
		if !known || class != lensClassValue {
			h.mu.Lock()
			h.pendingSpecs[id] = append([]byte(nil), evt.Value...)
			h.mu.Unlock()
			return
		}
		h.dispatchSpec(hostCtx, id, evt.Value)
	}
}

// dispatchSpec parses body as a LensSpec, skips silently if it is not an
// eventStream definition (Refractor's concern), and otherwise translates +
// starts (or, for an already-running id, logs the no-hot-reload notice
// instead of restarting).
func (h *Host) dispatchSpec(hostCtx context.Context, id string, body []byte) {
	specBody, err := unwrapSpecBody(body)
	if err != nil {
		h.logger.Error("chronicler: lens spec unwrap failed", "lensId", id, "err", err)
		return
	}
	var spec LensSpec
	if err := json.Unmarshal(specBody, &spec); err != nil {
		h.logger.Error("chronicler: lens spec unmarshal failed", "lensId", id, "err", err)
		return
	}
	// id (the vertex/aspect key's parsed id segment) is always used, never the
	// JSON body's own `id` field: substrate.ParseVertexKey/ParseAspectKey
	// already guarantee id is a valid, dot-free NanoID (safe as a durable-
	// name/subject-token segment), while the body's `id` is untrusted
	// free-form JSON a lens author could set to anything — using it verbatim
	// for the "chronicler-"+id durable name would let a malformed value
	// degrade a load failure into a NATS-client error instead of this
	// package's own clear, load-time-reject doctrine.
	spec.ID = id
	if !isEventStreamSpec(&spec) {
		return // a coreKv (or unrecognized) lens — Refractor's concern, not ours.
	}

	h.mu.Lock()
	_, alreadyRunning := h.running[id]
	h.mu.Unlock()
	if alreadyRunning {
		h.logger.Warn("chronicler: eventStream lens definitions are not hot-reloadable; restart chronicler to pick up the change",
			"lensId", id, "canonicalName", spec.CanonicalName)
		return
	}

	def, err := translateDefinition(&spec)
	if err != nil {
		h.logger.Error("chronicler: definition translation failed", "lensId", id, "err", err)
		return
	}
	h.startManager(hostCtx, def)
}

// startManager builds the write-side Adapter + Manager for def and runs it in
// its own cancelable goroutine, tracked so a later removal can stop it.
func (h *Host) startManager(hostCtx context.Context, def *Definition) {
	a, err := h.cfg.NewAdapter(def.Bucket, def.KeyField, def.DeleteMode)
	if err != nil {
		h.logger.Error("chronicler: build adapter failed", "lensId", def.ID, "bucket", def.Bucket, "err", err)
		return
	}
	mgr, err := NewManager(ManagerConfig{
		Conn:         h.cfg.Conn,
		EventsStream: h.cfg.EventsStream,
		Subject:      def.Subject,
		Durable:      "chronicler-" + def.ID,
		KeyField:     def.KeyField,
		Project:      def.Project,
		Adapter:      a,
		Logger:       h.logger,
	})
	if err != nil {
		h.logger.Error("chronicler: build manager failed", "lensId", def.ID, "err", err)
		return
	}

	ctx, cancel := context.WithCancel(hostCtx)
	done := make(chan struct{})
	h.mu.Lock()
	h.running[def.ID] = &runningManager{def: def, cancel: cancel, done: done}
	h.mu.Unlock()

	h.logger.Info("chronicler: definition loaded", "lensId", def.ID, "canonicalName", def.CanonicalName,
		"subject", def.Subject, "bucket", def.Bucket)
	go func() {
		defer close(done)
		if err := mgr.Run(ctx); err != nil && ctx.Err() == nil {
			h.logger.Error("chronicler: manager exited with error", "lensId", def.ID, "err", err)
		}
	}()
}

// removeDefinition stops and untracks id's running Manager, if any. Called
// synchronously from handle (the sole discovery-loop goroutine), so a
// Manager whose Run doesn't return promptly after cancel (e.g. a wedged
// JetStream call) stalls discovery for every OTHER definition until it
// exits — an accepted head-of-line-blocking risk for v1's single-goroutine
// discovery loop, not something this dormant increment builds async teardown
// for.
func (h *Host) removeDefinition(id, reason string) {
	h.mu.Lock()
	rm, ok := h.running[id]
	if ok {
		delete(h.running, id)
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	rm.cancel()
	<-rm.done
	h.logger.Info("chronicler: definition removed", "lensId", id, "reason", reason)
}

// Active returns the number of currently-running definitions and their IDs —
// used by the health Probe.
func (h *Host) Active() (count int, ids []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids = make([]string, 0, len(h.running))
	for id := range h.running {
		ids = append(ids, id)
	}
	return len(h.running), ids
}
