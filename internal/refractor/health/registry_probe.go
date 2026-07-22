package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// defaultRegistryProbeGraceWindow is how long RegistryProbe waits after
// process start before its first reconciliation check — long enough for a
// normal boot's meta-history replay + pipeline activation to finish, so a
// lens that is merely still starting up is never mistaken for one the
// replay never delivered (refractor-lens-registry-restart-integrity-design.md
// §4 Fire B step 2, §8 "Probe false-positives").
const defaultRegistryProbeGraceWindow = 60 * time.Second

// defaultRegistryProbeTickInterval is the recurring cadence of subsequent
// checks after the boot grace window.
const defaultRegistryProbeTickInterval = 10 * time.Minute

// registryProbeVertexProbe peeks at a `vtx.meta.<id>` vertex envelope's
// `class` and soft-delete marker without committing to the full envelope
// shape — a local duplicate of CoreKVSource's own envelopeProbe (lens is a
// package boundary the health package does not import; §4 Fire B, mirroring
// the established duplication precedent between corekv_source.go and
// internal/chronicler's own inverted copy).
type registryProbeVertexProbe struct {
	Class     string `json:"class"`
	IsDeleted bool   `json:"isDeleted"`
}

// registryProbeSpecProbe peeks at a lens spec body's `source.kind` — bare or
// envelope-wrapped under `data` (bootstrap primordial lenses) — to skip
// Chronicler-owned eventStream specs, the same class CoreKVSource's own
// dispatchSpec skips before ever reaching translateSpec. A local duplicate
// for the same reason as registryProbeVertexProbe.
type registryProbeSpecProbe struct {
	Source *struct {
		Kind string `json:"kind"`
	} `json:"source"`
	Data *struct {
		Source *struct {
			Kind string `json:"kind"`
		} `json:"source"`
	} `json:"data"`
}

func (p registryProbeSpecProbe) isEventStream() bool {
	if p.Source != nil && p.Source.Kind == "eventStream" {
		return true
	}
	return p.Data != nil && p.Data.Source != nil && p.Data.Source.Kind == "eventStream"
}

// RegistryProbe periodically reconciles the set of lens IDs declared in Core
// KV (a `meta.lens` vertex + spec, the platform's persistent registry —
// §4.2 "Core KV already is the persistent registry") against the set of
// currently-registered (started) lens IDs, so a lens the registry-replay
// never delivered — or one whose activation silently failed — becomes a
// visible LensRegistryIncomplete issue instead of a frozen read model behind
// a green heartbeat. The direct detection half of the lens-registry-restart-
// integrity fix (§4 Fire B); Fire A's per-boot durable is the actual fix.
//
// Refractor is a sanctioned direct Core-KV reader (platform binary, P5's one
// exception alongside Loupe) — this probe reads Core KV directly rather than
// through a lens.
type RegistryProbe struct {
	conn         *substrate.Conn
	bucket       string
	registered   func() []string
	graceWindow  time.Duration
	tickInterval time.Duration
	logger       *slog.Logger

	mu      sync.Mutex
	missing []string
}

// NewRegistryProbe constructs a probe. registered must return the lens IDs
// currently in the running registry (the started-pipeline set, e.g.
// cmd/refractor's registry map — the exact set LensCountProvider counts).
// logger may be nil.
func NewRegistryProbe(conn *substrate.Conn, bucket string, registered func() []string, logger *slog.Logger) *RegistryProbe {
	if logger == nil {
		logger = slog.Default()
	}
	return &RegistryProbe{
		conn:         conn,
		bucket:       bucket,
		registered:   registered,
		graceWindow:  defaultRegistryProbeGraceWindow,
		tickInterval: defaultRegistryProbeTickInterval,
		logger:       logger,
	}
}

// Missing returns the lens IDs from the most recent reconciliation that were
// declared in Core KV but absent from the registry. Empty (never nil) before
// the first check completes (the boot grace window) — a probe that hasn't
// run yet must never report a false positive.
func (p *RegistryProbe) Missing() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.missing...)
}

// Run blocks until ctx is cancelled: waits out the boot grace window, checks
// once, then checks again on every tick thereafter.
func (p *RegistryProbe) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(p.graceWindow):
	}
	p.check(ctx)

	t := time.NewTicker(p.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.check(ctx)
		}
	}
}

// check runs one reconciliation pass: enumerate declared lens IDs, diff
// against the registered set, and store the result. A declared-lens
// enumeration failure is logged and skipped — it leaves the prior missing
// set in place rather than clearing it (a transient KV-listing error must
// never look like "registry reconciled clean").
func (p *RegistryProbe) check(ctx context.Context) {
	declared, err := p.declaredLensIDs(ctx)
	if err != nil {
		p.logger.Warn("registry-reconciliation probe: enumerate declared lenses failed", "err", err)
		return
	}

	registeredSet := make(map[string]struct{}, len(p.registered()))
	for _, id := range p.registered() {
		registeredSet[id] = struct{}{}
	}

	var missing []string
	for _, id := range declared {
		if _, ok := registeredSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)

	p.mu.Lock()
	p.missing = missing
	p.mu.Unlock()

	if len(missing) > 0 {
		p.logger.Warn("registry-reconciliation probe: lens(es) declared but not registered",
			"count", len(missing), "lensIds", missing)
	}
}

// declaredLensIDs enumerates every `vtx.meta.<id>` vertex whose envelope
// class is `meta.lens` and is not soft-deleted, skipping Chronicler-owned
// eventStream specs (mirrors CoreKVSource.dispatchSpec's own skip). A vertex
// whose `.spec` aspect fetch fails (absent, or any other error) is still
// counted as declared — deliberately fail-closed: an activation that never
// completes is exactly the class of silent failure this probe exists to
// surface (§4 Fire B step 2), not a case to quietly exclude.
func (p *RegistryProbe) declaredLensIDs(ctx context.Context) ([]string, error) {
	keys, err := p.conn.KVListKeysPrefix(ctx, p.bucket, "vtx.meta.")
	if err != nil {
		return nil, err
	}

	var ids []string
	for _, key := range keys {
		_, id, ok := substrate.ParseVertexKey(key)
		if !ok {
			continue // not a 3-segment vertex root (an aspect key, etc.)
		}

		entry, err := p.conn.KVGet(ctx, p.bucket, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue // hard-tombstoned since the listing
			}
			return nil, err
		}
		var vp registryProbeVertexProbe
		if err := json.Unmarshal(entry.Value, &vp); err != nil {
			continue // malformed envelope — not this probe's concern
		}
		if vp.Class != "meta.lens" || vp.IsDeleted {
			continue
		}

		specEntry, err := p.conn.KVGet(ctx, p.bucket, key+".spec")
		if err == nil {
			var sp registryProbeSpecProbe
			if json.Unmarshal(specEntry.Value, &sp) == nil && sp.isEventStream() {
				continue
			}
		}

		ids = append(ids, id)
	}
	return ids, nil
}
