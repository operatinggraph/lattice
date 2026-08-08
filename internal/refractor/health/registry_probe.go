package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
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
	TargetConfig *registryProbeTargetProbe `json:"targetConfig"`
	Data         *struct {
		Source *struct {
			Kind string `json:"kind"`
		} `json:"source"`
		TargetConfig *registryProbeTargetProbe `json:"targetConfig"`
	} `json:"data"`
}

// registryProbeTargetProbe peeks at the Secure-Lens holder-type declarations,
// which live in the spec's targetConfig for both target kinds (the Postgres and
// NATS-KV config structs each carry secureColumns — the NATS-KV one only so a
// misdirected declaration fails closed at activation). Mirrors
// lens.SecureColumn's on-wire shape.
type registryProbeTargetProbe struct {
	SecureColumns []struct {
		HolderTypes []string `json:"holderTypes"`
	} `json:"secureColumns"`
}

func (p registryProbeSpecProbe) isEventStream() bool {
	if p.Source != nil && p.Source.Kind == "eventStream" {
		return true
	}
	return p.Data != nil && p.Data.Source != nil && p.Data.Source.Kind == "eventStream"
}

// declaresHolderType reports whether this spec's Secure-Lens columns name
// holderType — the lens author's own statement about which key holders may open
// its ciphertext, read from the same `targetConfig.secureColumns[].holderTypes` the
// running decryptor is built from rather than inferred from compiled cypher.
func (p registryProbeSpecProbe) declaresHolderType(holderType string) bool {
	tc := p.TargetConfig
	if tc == nil && p.Data != nil {
		tc = p.Data.TargetConfig
	}
	if tc == nil {
		return false
	}
	for _, sc := range tc.SecureColumns {
		if slices.Contains(sc.HolderTypes, holderType) {
			return true
		}
	}
	return false
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
	missing, err := p.reconcile(ctx, "")
	if err != nil {
		p.logger.Warn("registry-reconciliation probe: enumerate declared lenses failed", "err", err)
		return
	}
	p.mu.Lock()
	p.missing = missing
	p.mu.Unlock()

	if len(missing) > 0 {
		p.logger.Warn("registry-reconciliation probe: lens(es) declared but not registered",
			"count", len(missing), "lensIds", missing)
	}
}

// ReconcileNow runs one reconciliation pass immediately and returns the lens
// IDs declared in Core KV but absent from the registry — WITHOUT publishing the
// result.
//
// It exists because "the registry is loaded" is a question with a caller that
// cannot wait for a tick: the retention-class destruction consumer must not
// attest an erasure off an enumeration taken over a registry that has not
// finished loading, where "no lens declares this holder type" and "no lens has
// registered yet" are the same empty set. This answers it against Core KV —
// the platform's persistent lens registry — rather than against the in-process
// map whose incompleteness is the hazard.
//
// Not publishing is the load-bearing half. Missing() is what the heartbeat
// reads to raise LensRegistryIncomplete, and its contract is that it stays
// empty until the first SCHEDULED check completes — the 60s boot grace window
// exists precisely so a lens that is merely still starting is never reported as
// one the replay never delivered. An on-demand caller runs inside that window
// by construction, so storing its result here would publish exactly the false
// positive the grace window was built to suppress.
//
// An error is NOT an empty missing set: a caller must treat it as "unknown",
// never as "reconciled clean", which is why the error is returned rather than
// folded into the slice.
func (p *RegistryProbe) ReconcileNow(ctx context.Context) ([]string, error) {
	return p.reconcile(ctx, "")
}

// ReconcileNowForHolderType is ReconcileNow narrowed to the lenses that could
// actually hold the named key holder's plaintext — those whose spec declares
// holderType in a Secure-Lens column.
//
// Scope is the whole point. Corpus-global readiness answers a question nobody
// asked: ANY single lens that never registers — a bad spec, a failed
// activation, another deployment's — would make readiness false forever, so
// every destruction of every holder type would burn its budget and give up
// without attesting. Narrowing it to the declaring set means "not ready" is a
// statement about lenses that might be serving THIS holder's plaintext, which is
// the condition worth withholding an attestation over.
//
// It stays fail-closed at the edges: a lens whose spec cannot be read or parsed
// keeps its unknown holder types and is reported, because "unknown" must not
// resolve to "not relevant".
func (p *RegistryProbe) ReconcileNowForHolderType(ctx context.Context, holderType string) ([]string, error) {
	return p.reconcile(ctx, holderType)
}

// reconcile is the pure computation behind both: enumerate declared lens IDs
// and diff against the registered set. It stores nothing.
func (p *RegistryProbe) reconcile(ctx context.Context, holderType string) ([]string, error) {
	declared, err := p.declaredLensIDs(ctx, holderType)
	if err != nil {
		return nil, err
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
	return missing, nil
}

// declaredLensIDs enumerates every `vtx.meta.<id>` vertex whose envelope
// class is `meta.lens` and is not soft-deleted, skipping Chronicler-owned
// eventStream specs (mirrors CoreKVSource.dispatchSpec's own skip). A vertex
// whose `.spec` aspect fetch fails (absent, or any other error) is still
// counted as declared — deliberately fail-closed: an activation that never
// completes is exactly the class of silent failure this probe exists to
// surface (§4 Fire B step 2), not a case to quietly exclude.
func (p *RegistryProbe) declaredLensIDs(ctx context.Context, holderType string) ([]string, error) {
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
		specReadable := false
		var sp registryProbeSpecProbe
		if err == nil && json.Unmarshal(specEntry.Value, &sp) == nil {
			specReadable = true
			if sp.isEventStream() {
				continue
			}
		}

		// A holder-type filter, when one is asked for. An unreadable or
		// unparseable spec is kept regardless — its holder types are UNKNOWN, and
		// treating unknown as "not relevant" is the one direction that could
		// silently exclude the lens whose plaintext the caller is asking about.
		if holderType != "" && specReadable && !sp.declaresHolderType(holderType) {
			continue
		}

		ids = append(ids, id)
	}
	return ids, nil
}
