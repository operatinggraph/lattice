//go:build js

// Package browser is the Edge engine's second host (edge-browser-node-design.md
// §3.3): the wasm entry point that wires the same semantics packages the
// trusted Go hosts wire — store, overlay, sync, agent — onto browser-supplied
// persistence (IndexedDB) and browser-supplied transport (a JS NATS client
// over WebSocket), and exposes them to the page as a small JS API.
//
// The split this package sits on is FORK-W A′: correctness lives here in Go,
// once, for both hosts; the JS shell owns only the connection. So this package
// deliberately holds no transport policy and no rendering — it is the same
// engine cmd/facet embeds, with its two host-coupled seams pointed at the
// browser instead of at bbolt and nats.go.
//
// The API installed on globalThis:
//
//	latticeEdge.start(config) -> Promise<api>
//
//	config: {identityId, deviceId, gatewayUrl, token, shell, storeName?,
//	         types?: [], anchors?: []}
//	api:    {deliver(subject, body, sequence) -> Promise<"ack"|"nak"|"term">,
//	         enqueue(request)                 -> Promise<{requestId}>,
//	         read(key)                        -> Promise<value|null>,
//	         snapshot()                       -> Promise<[frame]>,
//	         drain()                          -> Promise<void>,
//	         setConnected(bool)               -> void,
//	         onFrame(fn)                      -> unsubscribe fn,
//	         stop()                           -> Promise<void>}
//
// The frame kinds are cmd/facet's SSE kinds verbatim (feed.go) — manifest,
// outbox, ready, revoked, connectivity — because W4 swaps the renderer's
// EventSource for onFrame and nothing else: the frames are the contract that
// makes that swap a transport change rather than a rewrite.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"syscall/js"
	"time"

	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/edge/overlay"
	"github.com/operatinggraph/lattice/internal/edge/store"
	edgesync "github.com/operatinggraph/lattice/internal/edge/sync"
	"github.com/operatinggraph/lattice/internal/edge/transport"
	edgevault "github.com/operatinggraph/lattice/internal/edge/vault"
	"github.com/operatinggraph/lattice/internal/processor/opwire"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// drainInterval is how often the agent retries queued intents. It matches the
// Go host's loop: frequent enough that a reconnect drains promptly, slow
// enough that a persistent outage is not a busy-loop against the Gateway.
const drainInterval = 5 * time.Second

// Host is one identity's running browser edge node.
type Host struct {
	identityID string
	deviceID   string
	store      store.Store
	overlay    *overlay.Overlay
	agent      *agent.Agent
	tr         *jsTransport
	feed       *feed
	shell      js.Value
	logger     *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// funcs holds every js.Func handed to the page, so stop() can release
	// them. A js.Func is a Go-side registration the runtime never reclaims on
	// its own; leaking one per start would leak the whole Host it closes over.
	funcs []js.Func

	// peerUnsub is the shell's onPeerChange unsubscribe function, invoked on
	// Stop so a host that stops while the shell outlives it (a re-start on the
	// same shell) leaves no dangling follower handler behind.
	peerUnsub js.Value

	stopOnce sync.Once
}

// Config is the resolved form of the JS config object.
type Config struct {
	IdentityID string
	DeviceID   string
	GatewayURL string
	Token      string
	StoreName  string
	Types      []string
	Anchors    []string
	Shell      js.Value
	Logger     *slog.Logger
}

// Start opens the identity's IndexedDB store, wires the engine over cfg.Shell,
// and starts the sync manager and drain loop. The returned Host owns both
// goroutines until stop().
func Start(ctx context.Context, cfg Config) (*Host, error) {
	if cfg.IdentityID == "" || cfg.DeviceID == "" {
		return nil, errors.New("edge/browser: identityId and deviceId are both required")
	}
	if cfg.GatewayURL == "" {
		return nil, errors.New("edge/browser: gatewayUrl is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	tr, err := newJSTransport(cfg.Shell)
	if err != nil {
		return nil, err
	}

	name := cfg.StoreName
	if name == "" {
		// One database per identity, so two identities on one browser origin
		// never share a mirror (internal/edge/store's OpenIDB contract).
		name = "lattice-edge-" + cfg.IdentityID
	}
	st, err := store.OpenIDB(name)
	if err != nil {
		return nil, err
	}

	hostCtx, cancel := context.WithCancel(ctx)
	ov := overlay.New(st)

	// A Vault session-key client for this identity, so the sealed `name`
	// aspect the manifest.me row carries can be decrypted in memory for
	// display (display-name-convention-design.md §3 N3 — "the W3/W4 browser
	// engine carries the same client"). A construction failure only costs the
	// resident's name: the renderer keeps painting its typed fallback, the
	// same degraded state a shredded identity produces by design.
	var selfName *edgevault.SelfName
	if vaultClient, verr := edgevault.New(tr, edgevault.Config{
		IdentityID:  cfg.IdentityID,
		ActorHeader: "vtx.identity." + cfg.IdentityID,
		Logger:      logger,
	}); verr != nil {
		logger.Warn("edge/browser: vault client unavailable; self-name stays sealed", "identityId", cfg.IdentityID, "err", verr)
	} else {
		selfName = edgevault.NewSelfName(vaultClient)
	}
	fd := newFeed(selfName)

	mgr, err := edgesync.New(tr, st, edgesync.Config{
		IdentityID: cfg.IdentityID,
		DeviceID:   cfg.DeviceID,
		// The literal actor key, not the bearer JWT: the JWT authenticates the
		// WebSocket connection itself (the shell's business) and every Gateway
		// submit; this header is what the control plane authorizes against.
		// Same value cmd/facet's engine.go passes, for the same reason.
		ActorHeader: "vtx.identity." + cfg.IdentityID,
		Types:       cfg.Types,
		Anchors:     cfg.Anchors,
		Logger:      logger,
		OnChange: func(key string, deleted bool) {
			fd.publishManifestKey(ov, key, deleted)
			// The leader tab is the only one running a consumer, so OnChange
			// fires here only for the tab that actually landed the delta. Tell
			// the other tabs — which hold no consumer and so never saw this
			// message — that the shared mirror moved, so their followers can
			// re-read the key (edge-browser-node-design.md §3.3). A no-op when
			// the shell exposes no signalChange (a single-context host has no
			// peers): signalPeer guards on that.
			signalPeer(cfg.Shell, key, deleted)
		},
		OnHydrationComplete: func(revision uint64) {
			fd.publish(frame{Kind: "ready", Revision: revision})
		},
		OnRunEstablished: func() {
			fd.setSyncDegraded(false)
		},
	})
	if err != nil {
		cancel()
		_ = st.Close()
		return nil, err
	}

	// fetchSubmitter is the same Gateway write path the Go host uses (POST
	// /v1/operations, Bearer token), issued over the browser's native fetch
	// rather than net/http — see submit_fetch.go for why. P2 holds identically:
	// the Processor stays the sole Core-KV writer. getToken pulls from the
	// SAME live source the WS shell's own reconnect authenticator reads
	// (shellGetTokenFunc) rather than the cfg.Token snapshot Start() was
	// called with, so a page that refreshes its session (boot.mjs) keeps
	// Gateway writes working past the credential's original TTL too.
	sub := &trackingSubmitter{
		inner: &fetchSubmitter{url: cfg.GatewayURL, getToken: shellGetTokenFunc(cfg.Shell, cfg.Token)},
		feed:  fd,
	}
	ag := agent.New(sub, st, ov, mgr, agent.Config{
		Logger: logger,
		Conflict: func(c agent.ConflictInfo) {
			logger.Warn("edge/browser: intent rejected", "requestId", c.RequestID, "keys", c.Keys)
		},
	})

	h := &Host{
		identityID: cfg.IdentityID,
		deviceID:   cfg.DeviceID,
		store:      st,
		overlay:    ov,
		agent:      ag,
		tr:         tr,
		feed:       fd,
		shell:      cfg.Shell,
		logger:     logger,
		cancel:     cancel,
	}
	h.watchPeers()

	h.wg.Add(2)
	go func() {
		defer h.wg.Done()
		if err := mgr.Run(hostCtx); err != nil && hostCtx.Err() == nil {
			// No restart loop here (one Run per page lifetime) — mark the
			// degraded axis so the renderer shows a stale-world banner
			// instead of a healthy-looking stale world; a reload retries.
			fd.setSyncDegraded(true)
			logger.Warn("edge/browser: sync manager exited", "err", err)
		}
	}()
	go func() {
		defer h.wg.Done()
		h.runDrainLoop(hostCtx)
	}()
	return h, nil
}

// signalPeer posts one landed change onto the shell's cross-tab channel so a
// follower (which runs no consumer and so never receives this delta) learns the
// shared IndexedDB mirror moved. A single-context host — the parity harness, a
// browser without BroadcastChannel — exposes no signalChange; there are no peers
// to tell, so this is a no-op there.
func signalPeer(shell js.Value, key string, deleted bool) {
	if shell.Get("signalChange").Type() == js.TypeFunction {
		shell.Call("signalChange", key, deleted)
	}
}

// shellGetTokenFunc returns a getter for fetchSubmitter reading the shell's
// own live token source (shell.mjs's getToken, wired to boot.mjs's refreshed
// cell — the same value the WS transport's reconnect authenticator already
// re-reads on every attempt) when the shell exposes one. Falls back to the
// static token Start() was called with otherwise — a shell fixture that
// doesn't implement it (older shell, a test double), same disposition as
// signalPeer/onPeerChange's feature detection above.
func shellGetTokenFunc(shell js.Value, fallback string) func() string {
	if shell.Get("getToken").Type() != js.TypeFunction {
		return func() string { return fallback }
	}
	return func() string {
		v := shell.Call("getToken")
		if v.Type() != js.TypeString {
			return fallback
		}
		return v.String()
	}
}

// watchPeers registers the follower change-signal handler on the shell: when the
// leader tab signals a landed change, this tab re-reads the touched key from the
// shared store and republishes its manifest frame, so a follower's renderer
// tracks the mirror the leader alone is writing (edge-browser-node-design.md
// §3.3). A no-op when the shell exposes no onPeerChange (a single-context host).
//
// The re-read runs on a fresh goroutine, not inline: the handler is invoked
// synchronously from the JS event loop, and publishManifestKey blocks on an
// IndexedDB read whose completion is itself an event-loop callback — doing it
// inline would deadlock the loop against its own read. The same reason onFrame
// invokes the renderer's callback from a goroutine.
func (h *Host) watchPeers() {
	if h.shell.Get("onPeerChange").Type() != js.TypeFunction {
		return
	}
	handler := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 || args[0].Type() != js.TypeString {
			return nil
		}
		key := args[0].String()
		deleted := len(args) > 1 && args[1].Truthy()
		go h.feed.publishManifestKey(h.overlay, key, deleted)
		return nil
	})
	h.funcs = append(h.funcs, handler)
	if unsub := h.shell.Call("onPeerChange", handler); unsub.Type() == js.TypeFunction {
		h.peerUnsub = unsub
	}
}

// runDrainLoop retries queued intents until ctx is cancelled. A drain failure
// is the offline case, not a fault: the intents stay queued and the next tick
// retries, which is exactly the design's offline-queue behaviour.
func (h *Host) runDrainLoop(ctx context.Context) {
	t := time.NewTicker(drainInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := h.agent.Drain(ctx); err != nil && ctx.Err() == nil {
				if errors.Is(err, agent.ErrCredentialRejected) {
					h.feed.publishRevoked(err.Error())
					continue
				}
				h.logger.Debug("edge/browser: drain failed, intents stay queued", "err", err)
			}
		}
	}
}

// enqueueRequest is the JS-side shape of one locally-triggered write — the
// same fields cmd/facet's POST /api/enqueue takes, so the renderer's call site
// is unchanged when W4 drops the HTTP hop.
type enqueueRequest struct {
	OperationType string              `json:"operationType"`
	Payload       json.RawMessage     `json:"payload"`
	Class         string              `json:"class,omitempty"`
	Reads         []string            `json:"reads,omitempty"`
	OptionalReads []string            `json:"optionalReads,omitempty"`
	AuthContext   *opwire.AuthContext `json:"authContext,omitempty"`
	TouchedKey    string              `json:"touchedKey,omitempty"`
}

// Enqueue applies the optimistic overlay for the write's touched key, queues
// the intent durably, and publishes its Outbox frame — in that order, so the
// UI shows the change before the intent is even queued, let alone submitted
// (edge-lattice-full-design.md §3.4/§3.5).
func (h *Host) Enqueue(req enqueueRequest, requestID string) error {
	if req.OperationType == "" {
		return errors.New("operationType is required")
	}
	env := &opwire.OperationEnvelope{
		RequestID:     requestID,
		Lane:          opwire.LaneDefault,
		OperationType: req.OperationType,
		Actor:         h.identityID,
		Payload:       req.Payload,
		Class:         req.Class,
		AuthContext:   req.AuthContext,
	}
	if len(req.Reads) > 0 || len(req.OptionalReads) > 0 {
		env.ContextHint = &opwire.ContextHint{Reads: req.Reads, OptionalReads: req.OptionalReads}
	}

	var touched []string
	if req.TouchedKey != "" {
		if err := h.overlay.Apply(req.TouchedKey, requestID, req.Payload, false); err != nil {
			// The overlay is a latency affordance, not the write: losing it
			// costs the optimistic paint, while refusing the write would cost
			// the write. Same disposition cmd/facet's handler takes.
			h.logger.Warn("edge/browser: optimistic overlay apply failed, continuing without it", "key", req.TouchedKey, "err", err)
		} else {
			touched = []string{req.TouchedKey}
		}
	}
	if err := h.agent.Enqueue(env, touched); err != nil {
		return err
	}
	h.feed.enqueueOutbox(&outboxEntry{
		RequestID:     requestID,
		OperationType: req.OperationType,
		Payload:       req.Payload,
		Reads:         req.Reads,
		OptionalReads: req.OptionalReads,
		AuthContext:   req.AuthContext,
		State:         "queued",
		CreatedAt:     time.Now().UTC(),
	})
	return nil
}

// Snapshot replays the current manifest rows plus the outbox — what a fresh
// renderer needs to paint before any live frame arrives (cmd/facet serves the
// identical burst on SSE connect).
func (h *Host) Snapshot() ([]frame, error) {
	entries, err := h.store.ScanPrefix(manifestPrefix)
	if err != nil {
		return nil, fmt.Errorf("edge/browser: snapshot: %w", err)
	}
	out := make([]frame, 0, len(entries)+len(h.feed.snapshotOutbox())+1)
	connected, syncDegraded := h.feed.connectivityState()
	out = append(out, frame{Kind: "connectivity", Connected: connected, SyncDegraded: syncDegraded})
	for _, e := range entries {
		v, ok, err := h.overlay.Read(e.Key)
		if err != nil || !ok {
			continue
		}
		if v.Deleted {
			continue
		}
		out = append(out, h.feed.manifestFrame(e.Key, v))
	}
	for _, e := range h.feed.snapshotOutbox() {
		out = append(out, frame{Kind: "outbox", Outbox: e})
	}
	if reason, ok := h.feed.revoked(); ok {
		out = append(out, frame{Kind: "revoked", Reason: reason})
	}
	return out, nil
}

// manifestPrefix is the Personal Lens manifest row namespace the renderer
// paints from (internal/edge/store's reserved projection-row prefix).
const manifestPrefix = "manifest."

// Stop cancels the host's goroutines, closes the store, and releases every
// js.Func handed to the page. Idempotent: the page can call stop() on unload
// and again on an explicit sign-out.
func (h *Host) Stop() {
	h.stopOnce.Do(func() {
		h.cancel()
		h.wg.Wait()
		// Unregister the follower handler before releasing its js.Func: the
		// shell may outlive this host (sign-out then a fresh start on the same
		// shell), and a released js.Func the shell still holds would panic if
		// re-invoked.
		if h.peerUnsub.Type() == js.TypeFunction {
			h.peerUnsub.Invoke()
		}
		_ = h.store.Close()
		for _, f := range h.funcs {
			f.Release()
		}
	})
}

// Register installs globalThis.latticeEdge. It is the whole surface the wasm
// artifact exposes; cmd/edge-wasm's main is nothing but this call plus a park.
func Register(logger *slog.Logger) {
	start := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 || args[0].Type() != js.TypeObject {
			return promise(func() (any, error) { return nil, errors.New("edge/browser: start(config) requires a config object") })
		}
		cfgVal := args[0]
		return promise(func() (any, error) {
			cfg, err := parseConfig(cfgVal, logger)
			if err != nil {
				return nil, err
			}
			h, err := Start(context.Background(), cfg)
			if err != nil {
				return nil, err
			}
			return h.jsAPI(), nil
		})
	})
	js.Global().Set("latticeEdge", map[string]any{"start": start})
}

func parseConfig(v js.Value, logger *slog.Logger) (Config, error) {
	shell := v.Get("shell")
	if shell.Type() != js.TypeObject {
		return Config{}, errors.New("edge/browser: config.shell must be the transport shell object")
	}
	return Config{
		IdentityID: optString(v, "identityId"),
		DeviceID:   optString(v, "deviceId"),
		GatewayURL: optString(v, "gatewayUrl"),
		Token:      optString(v, "token"),
		StoreName:  optString(v, "storeName"),
		Types:      optStringSlice(v, "types"),
		Anchors:    optStringSlice(v, "anchors"),
		Shell:      shell,
		Logger:     logger,
	}, nil
}

func optStringSlice(o js.Value, name string) []string {
	v := o.Get(name)
	if v.Type() != js.TypeObject || !v.InstanceOf(js.Global().Get("Array")) {
		return nil
	}
	n := v.Length()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if e := v.Index(i); e.Type() == js.TypeString {
			out = append(out, e.String())
		}
	}
	return out
}

// jsAPI builds the per-host object start() resolves with.
func (h *Host) jsAPI() map[string]any {
	reg := func(fn func(js.Value, []js.Value) any) js.Func {
		f := js.FuncOf(fn)
		h.funcs = append(h.funcs, f)
		return f
	}

	deliver := reg(func(_ js.Value, args []js.Value) any {
		if len(args) < 3 {
			return promise(func() (any, error) { return nil, errors.New("deliver(subject, body, sequence)") })
		}
		subject, body, seq := args[0], args[1], args[2]
		return promise(func() (any, error) {
			if subject.Type() != js.TypeString {
				return nil, errors.New("deliver: subject must be a string")
			}
			b, err := toBytes(body)
			if err != nil {
				return nil, fmt.Errorf("deliver: body: %w", err)
			}
			if seq.Type() != js.TypeNumber {
				return nil, errors.New("deliver: sequence must be a number")
			}
			return h.tr.Deliver(context.Background(), transport.Delta{
				Subject:  subject.String(),
				Body:     b,
				Sequence: uint64(seq.Float()),
			}), nil
		})
	})

	enqueue := reg(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return promise(func() (any, error) { return nil, errors.New("enqueue(request)") })
		}
		raw := jsonStringify(args[0])
		return promise(func() (any, error) {
			var req enqueueRequest
			if err := json.Unmarshal([]byte(raw), &req); err != nil {
				return nil, fmt.Errorf("enqueue: decode request: %w", err)
			}
			requestID, err := keys.NewNanoID()
			if err != nil {
				return nil, fmt.Errorf("enqueue: generate requestId: %w", err)
			}
			if err := h.Enqueue(req, requestID); err != nil {
				return nil, err
			}
			return map[string]any{"requestId": requestID}, nil
		})
	})

	read := reg(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 || args[0].Type() != js.TypeString {
			return promise(func() (any, error) { return nil, errors.New("read(key)") })
		}
		key := args[0].String()
		return promise(func() (any, error) {
			v, ok, err := h.overlay.Read(key)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, nil
			}
			return jsonParse(h.feed.manifestFrame(v.Key, v)), nil
		})
	})

	snapshot := reg(func(_ js.Value, _ []js.Value) any {
		return promise(func() (any, error) {
			frames, err := h.Snapshot()
			if err != nil {
				return nil, err
			}
			out := make([]any, 0, len(frames))
			for _, fr := range frames {
				out = append(out, fr.toJS())
			}
			return out, nil
		})
	})

	drain := reg(func(_ js.Value, _ []js.Value) any {
		return promise(func() (any, error) { return nil, h.agent.Drain(context.Background()) })
	})

	setConnected := reg(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			h.feed.setConnected(args[0].Truthy())
		}
		return nil
	})

	onFrame := reg(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 || args[0].Type() != js.TypeFunction {
			return js.Undefined()
		}
		cb := args[0]
		ch := h.feed.subscribe()
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				case fr := <-ch:
					// Invoke on the goroutine: syscall/js marshals the call
					// onto the event loop itself, and the renderer's callback
					// must not run inside a Go lock (feed.publish holds none
					// by the time it reaches this channel).
					cb.Invoke(fr.toJS())
				}
			}
		}()
		unsub := js.FuncOf(func(_ js.Value, _ []js.Value) any {
			close(done)
			h.feed.unsubscribe(ch)
			return nil
		})
		h.funcs = append(h.funcs, unsub)
		return unsub
	})

	stop := reg(func(_ js.Value, _ []js.Value) any {
		return promise(func() (any, error) {
			h.Stop()
			return nil, nil
		})
	})

	return map[string]any{
		"identityId":   h.identityID,
		"deviceId":     h.deviceID,
		"deliver":      deliver,
		"enqueue":      enqueue,
		"read":         read,
		"snapshot":     snapshot,
		"drain":        drain,
		"setConnected": setConnected,
		"onFrame":      onFrame,
		"stop":         stop,
	}
}

// jsonStringify/jsonParse move structured values across the boundary as JSON
// rather than by hand-walking js.Value trees: the frame and envelope shapes
// are already defined by their Go json tags, and re-encoding them field by
// field in interop code is how the two representations drift apart.
func jsonStringify(v js.Value) string {
	return js.Global().Get("JSON").Call("stringify", v).String()
}

func jsonParse(v any) js.Value {
	b, err := json.Marshal(v)
	if err != nil {
		return js.Null()
	}
	return js.Global().Get("JSON").Call("parse", string(b))
}
