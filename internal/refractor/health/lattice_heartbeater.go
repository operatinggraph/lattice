// Package health: Lattice-side heartbeater per Contract #5 §5.2.
// Emits health.refractor.<instance> and per-lens lag every 10s.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/healthkv"
	"github.com/asolgan/lattice/internal/substrate"
)

// defaultCapabilityLensLagThreshold is the consumer-lag (pending message count)
// above which an active capability lens is flagged CapabilityLensLagging
// (severity warning ⇒ degraded). Deployment-overridable via the heartbeater's
// CapabilityLensLagThreshold field. A warning, not a halt: it self-resolves once
// the projector drains its backlog (see the hysteresis below).
const defaultCapabilityLensLagThreshold = 100

// defaultCapabilityLensLagRaiseCycles is how many consecutive over-threshold
// heartbeats a lens must show before CapabilityLensLagging is raised. At the 10s
// NFR-O1 floor, 3 cycles ≈ 30s of sustained backlog — enough that a one-cycle
// spike (a momentary projector stall that drains on the next beat) no longer
// flaps the heartbeat degraded→healthy. Deployment-overridable via
// CapabilityLensLagRaiseCycles. The paused-lens path is a hard error and is
// never debounced.
const defaultCapabilityLensLagRaiseCycles = 3

// Issue codes for capability-lens anomalies (Contract #5 §5.5; component-defined,
// PascalCase).
const (
	issueCapabilityLensPaused  = "CapabilityLensPaused"
	issueCapabilityLensLagging = "CapabilityLensLagging"
)

// defaultLensLagThreshold / defaultLensLagRaiseCycles mirror the capability-lens
// defaults (lens-projection-liveness-design.md §5.7 — the cap path's
// battle-tested defaults, reused rather than a fresh tuning exercise).
// Deployment-overridable via LensLagThreshold / LensLagRaiseCycles.
const (
	defaultLensLagThreshold    = 100
	defaultLensLagRaiseCycles  = 3
	issueLensProjectionPaused  = "LensProjectionPaused"
	issueLensProjectionLagging = "LensProjectionLagging"
)

// issueLensRegistryIncomplete is the registry-reconciliation-probe issue
// code (refractor-lens-registry-restart-integrity-design.md §4 Fire B): a
// lens declared in Core KV (`meta.lens` vertex + spec) is absent from the
// running registry — the direct detection for the cold-registry incident
// class (a healthy heartbeat with a silently empty or partial pipeline set).
const issueLensRegistryIncomplete = "LensRegistryIncomplete"

// CapabilityLensStatus is one auth-plane (capability-kv) lens's liveness snapshot,
// supplied by CapabilityLensProvider for the per-heartbeat threshold evaluation.
// The provider reads it from the lens's health Reporter (status / pauseReason) and
// supervised consumer (consumerLag); it never touches the authorization decision
// path, Core KV, or the projection itself.
type CapabilityLensStatus struct {
	CanonicalName string
	RuleID        string
	Status        string // "active" | "paused" | "rebuilding"
	PauseReason   string // "" when not paused
	ConsumerLag   uint64
}

// LensLivenessStatus is one non-auth-plane (business) lens's liveness snapshot,
// supplied by LensProvider for the general projection-liveness backstop
// (lens-projection-liveness-design.md §3.3). Mirrors CapabilityLensStatus plus
// the lastProjectedAt progress clock; auth-plane lenses are excluded — the
// CapabilityLensProvider path owns them.
type LensLivenessStatus struct {
	CanonicalName   string
	RuleID          string
	Status          string // "active" | "paused" | "rebuilding"
	PauseReason     string // "" when not paused
	ProjectionLag   uint64
	LastProjectedAt time.Time // zero if never projected
}

// issueRecord is one entry of the Health-KV `issues` array (Contract #5 §5.5).
type issueRecord struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // "warning" (degraded) | "error" (unhealthy)
	Message  string `json:"message"`
	Since    string `json:"since"` // ISO 8601; persists across heartbeats while open
}

// capIssue is the in-flight (severity, message) for an active issue code this cycle.
type capIssue struct {
	severity string
	message  string
}

// LatticeHealthDoc mirrors Contract #5 §5.2 (same shape as Processor).
type LatticeHealthDoc struct {
	Key         string         `json:"key"`
	Component   string         `json:"component"`
	Instance    string         `json:"instance"`
	Version     string         `json:"version"`
	Status      string         `json:"status"`
	HeartbeatAt string         `json:"heartbeatAt"`
	StartedAt   string         `json:"startedAt"`
	Uptime      string         `json:"uptime"`
	Metrics     map[string]any `json:"metrics"`
	Issues      []any          `json:"issues"`
}

// LatticeHeartbeater periodically writes the Refractor instance's
// heartbeat to Health KV at `health.refractor.<instance>`. NFR-O1
// floor: 10s interval.
type LatticeHeartbeater struct {
	conn      *substrate.Conn
	bucket    string
	instance  string
	startedAt time.Time
	interval  time.Duration
	logger    *slog.Logger

	// LagProvider optionally returns per-lens lag (stream_last_seq -
	// consumer_acked_seq) values for inclusion in the heartbeat metrics.
	// May be nil before any lens is active.
	LagProvider func() map[string]uint64

	// LensLatencyProvider optionally returns per-Lens projection latency
	// stats (Contract #5 §5.2 / NFR-P3). The map is keyed by Lens canonical
	// name (e.g. "capability", "capabilityRoleIndex") and produces
	// {mean,p95,p99,count} as nanosecond-precision durations. May be nil
	// before any lens activates with a latency buffer installed.
	LensLatencyProvider func() map[string]LensLatencySnapshot

	// CapabilityLensProvider optionally returns liveness snapshots for the
	// auth-plane (capability-kv) lenses — the authorization read-model the
	// Processor's capability check reads. When a snapshot crosses a threshold the
	// heartbeat raises a Contract #5 §5.5 issue and degrades status, the
	// operational backstop for the Processor's absent per-op freshness gate. nil
	// before any capability lens activates.
	CapabilityLensProvider func() []CapabilityLensStatus

	// LensProvider optionally returns liveness snapshots for the non-auth-plane
	// (business) lenses — the generalized projection-liveness backstop
	// (lens-projection-liveness-design.md §3.3). A sibling of
	// CapabilityLensProvider, deliberately excluding auth-plane lenses (the cap
	// path stays canonical for those — §5.1). nil before any business lens
	// activates.
	LensProvider func() []LensLivenessStatus

	// VaultCallsTotalProvider optionally returns the cumulative count of Vault
	// decryption calls (Contract #5 §5.4 vaultCallsTotal) — the Secure Lenses'
	// decrypt-at-projection calls, summed across every secure lens in the
	// process. General lenses project ciphertext as-is (design §2.3) and make
	// no Vault calls, so a deployment with no Secure Lens legitimately reports
	// 0. nil omits the field.
	VaultCallsTotalProvider func() uint64

	// KeyShreddedHandledTotalProvider optionally returns the cumulative count
	// of privacy.keyShredded events this instance has finished handling
	// (Contract #5 §5.4 keyshreddedHandledTotal) — internal/refractor/keyshredded's
	// Manager.HandledTotal. nil before the listener is wired.
	KeyShreddedHandledTotalProvider func() uint64

	// LensCountProvider optionally returns the current registry size (started
	// pipelines) — emitted as metrics.lensesRegistered
	// (refractor-lens-registry-restart-integrity-design.md §4 Fire B step 1),
	// the counterpart to lensLags that stays a legitimate 0 rather than
	// vanishing when the registry is empty. nil omits the field.
	LensCountProvider func() int

	// RegistryReconciliationProvider optionally returns the lens IDs
	// currently declared in Core KV (a `meta.lens` vertex + spec) but absent
	// from the running registry — the latest snapshot from a
	// RegistryProbe.Missing (§4 Fire B step 2). The probe owns its own
	// boot-grace-window + slow-tick cadence independent of the 10s heartbeat
	// interval; this hook only reads its current snapshot each cycle. nil
	// (or an always-empty snapshot) means no LensRegistryIncomplete issue is
	// raised.
	RegistryReconciliationProvider func() []string

	// CapabilityLensLagThreshold is the consumer-lag (pending count) above which
	// an active capability lens is flagged CapabilityLensLagging (warning).
	// Zero selects defaultCapabilityLensLagThreshold.
	CapabilityLensLagThreshold uint64

	// CapabilityLensLagRaiseCycles is how many consecutive over-threshold
	// heartbeats a lens must show before CapabilityLensLagging is raised — the
	// debounce that keeps a one-cycle spike from flapping the heartbeat. Zero
	// selects defaultCapabilityLensLagRaiseCycles; one disables the debounce
	// (raise on the first over-threshold cycle).
	CapabilityLensLagRaiseCycles uint

	// CapabilityLensLagClearThreshold is the consumer-lag at or below which an
	// already-raised CapabilityLensLagging clears — the lower edge of a hysteresis
	// band that keeps lag hovering around the raise threshold from flapping the
	// issue on/off. Zero (the default) sets it equal to the raise threshold (clear
	// as soon as the lens is no longer over). A value above the raise threshold is
	// clamped down to it (a band cannot invert).
	CapabilityLensLagClearThreshold uint64

	// LensLagThreshold / LensLagRaiseCycles / LensLagClearThreshold are the
	// general-lens sibling of the CapabilityLensLag* fields above — same
	// semantics, applied to non-auth-plane lenses. Zero selects
	// defaultLensLagThreshold / defaultLensLagRaiseCycles respectively.
	LensLagThreshold      uint64
	LensLagRaiseCycles    uint
	LensLagClearThreshold uint64

	// issuesMu guards openCapIssues.
	issuesMu sync.Mutex
	// openCapIssues tracks the `since` timestamp of each currently-open
	// capability-lens issue code (Contract #5 §5.5: components hold open issues in
	// memory so `since` persists across heartbeats; a resolved issue is dropped).
	openCapIssues map[string]string

	// lagMu guards lagState.
	lagMu sync.Mutex
	// lagState holds the per-lens lag-hysteresis state (over-threshold streak +
	// raised flag) keyed by capLensName, so the raise-after-N / clear-band debounce
	// persists across heartbeats. Pruned each cycle to the lenses currently
	// reported, mirroring openCapIssues.
	lagState map[string]*lagHysteresis

	// lensIssuesMu / openLensIssues and lensLagMu / lensLagState are the
	// general-lens sibling of issuesMu/openCapIssues and lagMu/lagState —
	// deliberately SEPARATE maps (not shared with the cap path) so pruning one
	// path's absent lenses never drops the other path's in-flight debounce/issue
	// state (§5.1: additive sibling, zero regression surface on the cap path).
	lensIssuesMu   sync.Mutex
	openLensIssues map[string]string
	lensLagMu      sync.Mutex
	lensLagState   map[string]*lagHysteresis

	// registryIssueMu guards openRegistryIssueSince — its own SEPARATE
	// since-persistence state (not folded into openLensIssues, which
	// reconcileLensIssues clears any code absent from its own active map
	// each cycle; a single shared map called from two independent eval
	// paths would have each clear the other's code).
	registryIssueMu        sync.Mutex
	openRegistryIssueSince string // "" when LensRegistryIncomplete is not open

	// ttlMultiplier derives the heartbeat's Health-KV TTL (interval ×
	// ttlMultiplier, Contract #5 §5.6). Zero disables TTL. Defaults to
	// healthkv.DefaultTTLMultiplier via NewLatticeHeartbeater; overridable with
	// SetTTLMultiplier.
	ttlMultiplier int
}

// lagHysteresis is one capability lens's lag-debounce state across heartbeats.
type lagHysteresis struct {
	overStreak int  // consecutive cycles strictly over the raise threshold
	raised     bool // CapabilityLensLagging currently raised for this lens
}

// LensLatencySnapshot is the per-Lens summary the heartbeater emits
// under `health.refractor.<instance>.lens.<canonicalName>.*` (or as a
// sub-map of the main heartbeat document — see emit()).
// Count is the number of samples behind the mean/p95/p99 figures.
type LensLatencySnapshot struct {
	Count int           `json:"count"`
	Mean  time.Duration `json:"mean"`
	P95   time.Duration `json:"p95"`
	P99   time.Duration `json:"p99"`
}

// NewLatticeHeartbeater wires the heartbeater. instance must be stable
// across the lifetime of the process (Contract #5 §5.1 convention:
// rfx-<NanoID>).
func NewLatticeHeartbeater(conn *substrate.Conn, bucket, instance string, interval time.Duration, logger *slog.Logger) *LatticeHeartbeater {
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LatticeHeartbeater{
		conn:          conn,
		bucket:        bucket,
		instance:      instance,
		startedAt:     time.Now(),
		interval:      interval,
		logger:        logger,
		ttlMultiplier: healthkv.DefaultTTLMultiplier,
	}
}

// SetTTLMultiplier overrides the heartbeat TTL multiplier (TTL = interval ×
// multiplier, Contract #5 §5.6). Must be called before Run starts. Zero
// disables the TTL (an escape hatch for an operator who wants sticky keys); a
// negative value is clamped to 0.
func (h *LatticeHeartbeater) SetTTLMultiplier(n int) {
	if n < 0 {
		n = 0
	}
	h.ttlMultiplier = n
}

// heartbeatTTL derives the current TTL from interval × ttlMultiplier
// (Contract #5 §5.6) — 0 when TTL is disabled.
func (h *LatticeHeartbeater) heartbeatTTL() time.Duration {
	return h.interval * time.Duration(h.ttlMultiplier)
}

// Run blocks until ctx is cancelled, emitting heartbeats on a ticker.
// One initial heartbeat fires immediately so observers see a fresh
// document within 10s of startup (AC #6).
func (h *LatticeHeartbeater) Run(ctx context.Context) {
	h.emit(ctx, "starting")
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			detached, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			h.emit(detached, "shuttingDown")
			cancel()
			return
		case <-t.C:
			h.emit(ctx, "healthy")
		}
	}
}

func (h *LatticeHeartbeater) emit(ctx context.Context, status string) {
	now := time.Now()
	metrics := map[string]any{}
	if h.LagProvider != nil {
		lags := h.LagProvider()
		if len(lags) > 0 {
			lensLags := make(map[string]uint64, len(lags))
			for k, v := range lags {
				lensLags[k] = v
			}
			metrics["lensLags"] = lensLags
		}
	}
	// Per-Lens projection latency under metrics.lensLatency (Contract #5 §5.2).
	// Each entry carries {count, mean, p95, p99} expressed in nanoseconds.
	// Only Lenses with at least one sample are emitted to avoid misleading
	// zeros on quiet instances.
	if h.LensLatencyProvider != nil {
		stats := h.LensLatencyProvider()
		if len(stats) > 0 {
			out := make(map[string]map[string]any, len(stats))
			for name, s := range stats {
				if s.Count == 0 {
					continue
				}
				out[name] = map[string]any{
					"count":  s.Count,
					"meanNs": s.Mean.Nanoseconds(),
					"p95Ns":  s.P95.Nanoseconds(),
					"p99Ns":  s.P99.Nanoseconds(),
				}
			}
			if len(out) > 0 {
				metrics["lensLatency"] = out
			}
		}
	}
	// Contract #5 §5.4 vaultCallsTotal / keyshreddedHandledTotal. Emitted whenever
	// a provider is wired, including a legitimate 0 (the documented Phase-1-stub
	// value for vaultCallsTotal before Fire 5's Secure Lens calls Vault).
	if h.VaultCallsTotalProvider != nil {
		metrics["vaultCallsTotal"] = h.VaultCallsTotalProvider()
	}
	if h.KeyShreddedHandledTotalProvider != nil {
		metrics["keyshreddedHandledTotal"] = h.KeyShreddedHandledTotalProvider()
	}
	// Registry size (refractor-lens-registry-restart-integrity-design.md §4 Fire
	// B step 1) — the counterpart to lensLags that stays a legitimate 0 instead
	// of vanishing when the registry is empty, so "healthy heartbeat, empty
	// registry" is visible in the metric itself, not just in the reconciliation
	// issue below.
	if h.LensCountProvider != nil {
		metrics["lensesRegistered"] = h.LensCountProvider()
	}
	// Capability-lens liveness backstop: surface a §5.5 issue (and degrade status)
	// when an auth-plane lens is paused or lagging beyond threshold. The
	// metrics.capabilityLens sub-map is emitted on every cycle (including healthy
	// alert:"ok") so observers can render the green state, not only anomalies.
	capMetric, capIssues := h.evalCapabilityLenses(now)
	if len(capMetric) > 0 {
		metrics["capabilityLens"] = capMetric
	}
	// Generalized (non-auth-plane) lens liveness backstop — the sibling that
	// widens the capability-lens machinery to every business lens
	// (lens-projection-liveness-design.md §3.3). Emitted every cycle including
	// alert:"ok" so observers can render the green state and the freshness
	// clock, not only anomalies.
	lensMetric, lensIssues := h.evalLenses(now)
	if len(lensMetric) > 0 {
		metrics["lensLiveness"] = lensMetric
	}
	// Registry-reconciliation probe (§4 Fire B step 2) — a lens declared in
	// Core KV but absent from the running registry, the direct detection for
	// the cold-registry incident class. Own since-persistence (not folded
	// into evalLenses' openLensIssues, an independent eval path).
	registryIssues := h.evalRegistryReconciliation(now)
	allIssues := make([]issueRecord, 0, len(capIssues)+len(lensIssues)+len(registryIssues))
	allIssues = append(allIssues, capIssues...)
	allIssues = append(allIssues, lensIssues...)
	allIssues = append(allIssues, registryIssues...)
	issues := make([]any, 0, len(allIssues))
	for _, is := range allIssues {
		issues = append(issues, is)
	}
	// Elevate to the §5.4 degraded/unhealthy status while a capability or
	// business-lens issue is open — at startup too, so a paused-at-boot lens is
	// visible immediately. A "shuttingDown" beat is left as-is (the instance is
	// tearing down), and a clean cycle keeps its lifecycle status
	// ("starting"/"healthy").
	effectiveStatus := status
	if status != "shuttingDown" && len(allIssues) > 0 {
		effectiveStatus = aggregateStatus(allIssues)
	}
	doc := LatticeHealthDoc{
		Key:         h.healthKey(),
		Component:   "refractor",
		Instance:    h.instance,
		Version:     "1.0",
		Status:      effectiveStatus,
		HeartbeatAt: substrate.FormatTimestamp(now),
		StartedAt:   substrate.FormatTimestamp(h.startedAt),
		Uptime:      formatISODuration(now.Sub(h.startedAt)),
		Metrics:     metrics,
		Issues:      issues,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		h.logger.Error("refractor heartbeat marshal", "err", err)
		return
	}
	if _, err := h.conn.KVPutWithTTL(ctx, h.bucket, h.healthKey(), data, h.heartbeatTTL()); err != nil {
		h.logger.Warn("refractor heartbeat put", "err", err, "key", h.healthKey())
	}
}

// evalCapabilityLenses applies the §5.5 threshold model to the capability-lens
// snapshots, returning the metrics.capabilityLens sub-map and the open issue
// records. It reconciles the in-memory open-issue set so each issue's `since`
// persists across heartbeats and a resolved issue is dropped on the next cycle.
// Returns (nil, nil) when no provider is wired.
func (h *LatticeHeartbeater) evalCapabilityLenses(now time.Time) (map[string]map[string]any, []issueRecord) {
	if h.CapabilityLensProvider == nil {
		return nil, nil
	}
	threshold := h.CapabilityLensLagThreshold
	if threshold == 0 {
		threshold = defaultCapabilityLensLagThreshold
	}
	raiseCycles := h.CapabilityLensLagRaiseCycles
	if raiseCycles == 0 {
		raiseCycles = defaultCapabilityLensLagRaiseCycles
	}
	clearThreshold := h.CapabilityLensLagClearThreshold
	if clearThreshold == 0 || clearThreshold > threshold {
		clearThreshold = threshold
	}

	snaps := h.CapabilityLensProvider()
	metric := make(map[string]map[string]any, len(snaps))
	var paused, lagging []string
	seen := make(map[string]struct{}, len(snaps))
	for _, s := range snaps {
		name := capLensName(s)
		seen[name] = struct{}{}
		alert := "ok"
		switch s.Status {
		case "paused":
			alert = "paused"
			reason := s.PauseReason
			if reason == "" {
				reason = "unknown"
			}
			paused = append(paused, fmt.Sprintf("%s (%s)", name, reason))
			// A paused lens is a hard error; its lag debounce is irrelevant and
			// must not carry a stale streak into the next active cycle.
			h.resetLagState(name)
		case "active":
			if h.evalLagHysteresis(name, s.ConsumerLag, threshold, clearThreshold, int(raiseCycles)) {
				alert = "lagging"
				lagging = append(lagging, fmt.Sprintf("%s (lag %d)", name, s.ConsumerLag))
			}
		default:
			// rebuilding (or any non-active, non-paused state): not lagging; clear
			// any pending streak.
			h.resetLagState(name)
		}
		metric[name] = map[string]any{
			"status":      s.Status,
			"consumerLag": s.ConsumerLag,
			"alert":       alert,
		}
	}
	h.pruneLagState(seen)

	active := map[string]capIssue{}
	if len(paused) > 0 {
		active[issueCapabilityLensPaused] = capIssue{
			severity: "error",
			message: "capability lens paused; authorization read-model is frozen — grants/revocations will not project: " +
				strings.Join(paused, ", "),
		}
	}
	if len(lagging) > 0 {
		active[issueCapabilityLensLagging] = capIssue{
			severity: "warning",
			message: fmt.Sprintf("capability lens consumer lag exceeds threshold %d; authorization reads may be stale: %s",
				threshold, strings.Join(lagging, ", ")),
		}
	}
	return metric, h.reconcileCapIssues(active, now)
}

// reconcileCapIssues merges this cycle's active capability issues with the
// in-memory open set (Contract #5 §5.5): a newly-active code is stamped with
// `since=now`; an already-open code keeps its original `since`; a code no longer
// active is dropped. Output is sorted by code for deterministic heartbeats.
func (h *LatticeHeartbeater) reconcileCapIssues(active map[string]capIssue, now time.Time) []issueRecord {
	h.issuesMu.Lock()
	defer h.issuesMu.Unlock()
	if h.openCapIssues == nil {
		h.openCapIssues = map[string]string{}
	}
	for code := range h.openCapIssues {
		if _, ok := active[code]; !ok {
			delete(h.openCapIssues, code)
		}
	}
	out := make([]issueRecord, 0, len(active))
	for code, ci := range active {
		since, ok := h.openCapIssues[code]
		if !ok {
			since = substrate.FormatTimestamp(now)
			h.openCapIssues[code] = since
		}
		out = append(out, issueRecord{Code: code, Severity: ci.severity, Message: ci.message, Since: since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// evalLagHysteresis advances one active lens's lag-debounce state and reports
// whether it counts as lagging this cycle. A lens must stay strictly over the
// raise threshold for raiseCycles consecutive heartbeats before it is raised
// (killing the one-cycle spike), and once raised it stays raised until its lag
// falls to or below clearThreshold (the lower edge of the hysteresis band).
func (h *LatticeHeartbeater) evalLagHysteresis(name string, lag, threshold, clearThreshold uint64, raiseCycles int) bool {
	h.lagMu.Lock()
	defer h.lagMu.Unlock()
	if h.lagState == nil {
		h.lagState = map[string]*lagHysteresis{}
	}
	st := h.lagState[name]
	if st == nil {
		st = &lagHysteresis{}
		h.lagState[name] = st
	}
	if st.raised {
		if lag <= clearThreshold {
			st.raised = false
			st.overStreak = 0
		}
		return st.raised
	}
	if lag > threshold {
		st.overStreak++
		if st.overStreak >= raiseCycles {
			st.raised = true
		}
	} else {
		st.overStreak = 0
	}
	return st.raised
}

// resetLagState clears one lens's lag-debounce state — used when a lens leaves
// the active state (paused/rebuilding), where lag is not a meaningful signal.
func (h *LatticeHeartbeater) resetLagState(name string) {
	h.lagMu.Lock()
	defer h.lagMu.Unlock()
	delete(h.lagState, name)
}

// pruneLagState drops debounce state for lenses no longer reported this cycle,
// keeping the map bounded to live lenses (mirrors reconcileCapIssues).
func (h *LatticeHeartbeater) pruneLagState(seen map[string]struct{}) {
	h.lagMu.Lock()
	defer h.lagMu.Unlock()
	for name := range h.lagState {
		if _, ok := seen[name]; !ok {
			delete(h.lagState, name)
		}
	}
}

// aggregateStatus maps the open issues to a Contract #5 §5.4 status: any error ⇒
// unhealthy, any warning ⇒ degraded, otherwise healthy.
func aggregateStatus(issues []issueRecord) string {
	worst := "healthy"
	for _, is := range issues {
		if is.Severity == "error" {
			return "unhealthy"
		}
		if is.Severity == "warning" {
			worst = "degraded"
		}
	}
	return worst
}

// capLensName prefers the canonical name, falling back to the rule ID.
func capLensName(s CapabilityLensStatus) string {
	if s.CanonicalName != "" {
		return s.CanonicalName
	}
	return s.RuleID
}

// lensName prefers the canonical name, falling back to the rule ID (mirrors
// capLensName for the general-lens sibling).
func lensName(s LensLivenessStatus) string {
	if s.CanonicalName != "" {
		return s.CanonicalName
	}
	return s.RuleID
}

// evalLenses applies the same §5.5 threshold model as evalCapabilityLenses to
// the non-auth-plane (business) lens snapshots (lens-projection-liveness-design.md
// §3.3), returning the metrics.lensLiveness sub-map and the open issue records.
// Deliberately a sibling of evalCapabilityLenses rather than a shared code path
// — auth-plane lenses are excluded (the cap path stays canonical for them, §5.1)
// and the lag-hysteresis/open-issue state lives in separate maps so pruning
// one path's absent lenses never touches the other's in-flight state. The one
// substantive difference from the cap path: a paused BUSINESS lens is
// `severity: warning` (⇒ degraded), not `error` (⇒ unhealthy) — a single frozen
// business lens is a real outage for that vertical but must not nuke the whole
// Refractor instance to unhealthy (design §3.3). Returns (nil, nil) when no
// provider is wired.
func (h *LatticeHeartbeater) evalLenses(now time.Time) (map[string]map[string]any, []issueRecord) {
	if h.LensProvider == nil {
		return nil, nil
	}
	threshold := h.LensLagThreshold
	if threshold == 0 {
		threshold = defaultLensLagThreshold
	}
	raiseCycles := h.LensLagRaiseCycles
	if raiseCycles == 0 {
		raiseCycles = defaultLensLagRaiseCycles
	}
	clearThreshold := h.LensLagClearThreshold
	if clearThreshold == 0 || clearThreshold > threshold {
		clearThreshold = threshold
	}

	snaps := h.LensProvider()
	metric := make(map[string]map[string]any, len(snaps))
	var paused, lagging []string
	seen := make(map[string]struct{}, len(snaps))
	for _, s := range snaps {
		name := lensName(s)
		seen[name] = struct{}{}
		alert := "ok"
		switch s.Status {
		case "paused":
			alert = "paused"
			reason := s.PauseReason
			if reason == "" {
				reason = "unknown"
			}
			paused = append(paused, fmt.Sprintf("%s (%s)", name, reason))
			h.resetLensLagState(name)
		case "active":
			if h.evalLensLagHysteresis(name, s.ProjectionLag, threshold, clearThreshold, int(raiseCycles)) {
				alert = "lagging"
				lagging = append(lagging, fmt.Sprintf("%s (lag %d)", name, s.ProjectionLag))
			}
		default:
			// rebuilding (or any non-active, non-paused state): not lagging; clear
			// any pending streak.
			h.resetLensLagState(name)
		}
		lastProjectedAt := ""
		if !s.LastProjectedAt.IsZero() {
			lastProjectedAt = substrate.FormatTimestamp(s.LastProjectedAt)
		}
		metric[name] = map[string]any{
			"status":          s.Status,
			"projectionLag":   s.ProjectionLag,
			"lastProjectedAt": lastProjectedAt,
			"alert":           alert,
		}
	}
	h.pruneLensLagState(seen)

	active := map[string]capIssue{}
	if len(paused) > 0 {
		active[issueLensProjectionPaused] = capIssue{
			severity: "warning",
			message: "lens paused; its read model is frozen (not authorization-critical, so this degrades rather than fails the instance): " +
				strings.Join(paused, ", "),
		}
	}
	if len(lagging) > 0 {
		active[issueLensProjectionLagging] = capIssue{
			severity: "warning",
			message: fmt.Sprintf("lens projection lag exceeds threshold %d; its read model may be stale: %s",
				threshold, strings.Join(lagging, ", ")),
		}
	}
	return metric, h.reconcileLensIssues(active, now)
}

// evalRegistryReconciliation surfaces the RegistryReconciliationProvider's
// latest snapshot as a LensRegistryIncomplete issue (§4 Fire B step 2).
// Severity error (⇒ unhealthy, not just degraded): an incomplete registry
// means real lens data is not being projected, the same class of outage a
// paused capability lens represents. Returns nil when no provider is wired
// or the latest snapshot is empty (registry complete); the empty case also
// clears any previously-open issue, so `since` does not persist past
// resolution.
func (h *LatticeHeartbeater) evalRegistryReconciliation(now time.Time) []issueRecord {
	if h.RegistryReconciliationProvider == nil {
		return nil
	}
	missing := h.RegistryReconciliationProvider()

	h.registryIssueMu.Lock()
	defer h.registryIssueMu.Unlock()
	if len(missing) == 0 {
		h.openRegistryIssueSince = ""
		return nil
	}
	if h.openRegistryIssueSince == "" {
		h.openRegistryIssueSince = substrate.FormatTimestamp(now)
	}

	const capN = 10
	shown := missing
	suffix := ""
	if len(shown) > capN {
		shown = shown[:capN]
		suffix = ", ..."
	}
	message := fmt.Sprintf("%d lens(es) declared in Core KV but not registered: %s%s",
		len(missing), strings.Join(shown, ", "), suffix)

	return []issueRecord{{
		Code:     issueLensRegistryIncomplete,
		Severity: "error",
		Message:  message,
		Since:    h.openRegistryIssueSince,
	}}
}

// reconcileLensIssues is the general-lens sibling of reconcileCapIssues — same
// since-persistence/drop-on-resolve semantics, backed by the separate
// openLensIssues map.
func (h *LatticeHeartbeater) reconcileLensIssues(active map[string]capIssue, now time.Time) []issueRecord {
	h.lensIssuesMu.Lock()
	defer h.lensIssuesMu.Unlock()
	if h.openLensIssues == nil {
		h.openLensIssues = map[string]string{}
	}
	for code := range h.openLensIssues {
		if _, ok := active[code]; !ok {
			delete(h.openLensIssues, code)
		}
	}
	out := make([]issueRecord, 0, len(active))
	for code, ci := range active {
		since, ok := h.openLensIssues[code]
		if !ok {
			since = substrate.FormatTimestamp(now)
			h.openLensIssues[code] = since
		}
		out = append(out, issueRecord{Code: code, Severity: ci.severity, Message: ci.message, Since: since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// evalLensLagHysteresis is the general-lens sibling of evalLagHysteresis,
// backed by the separate lensLagState map (same raise-after-N / clear-band
// debounce semantics).
func (h *LatticeHeartbeater) evalLensLagHysteresis(name string, lag, threshold, clearThreshold uint64, raiseCycles int) bool {
	h.lensLagMu.Lock()
	defer h.lensLagMu.Unlock()
	if h.lensLagState == nil {
		h.lensLagState = map[string]*lagHysteresis{}
	}
	st := h.lensLagState[name]
	if st == nil {
		st = &lagHysteresis{}
		h.lensLagState[name] = st
	}
	if st.raised {
		if lag <= clearThreshold {
			st.raised = false
			st.overStreak = 0
		}
		return st.raised
	}
	if lag > threshold {
		st.overStreak++
		if st.overStreak >= raiseCycles {
			st.raised = true
		}
	} else {
		st.overStreak = 0
	}
	return st.raised
}

// resetLensLagState clears one lens's lag-debounce state (general-lens sibling
// of resetLagState).
func (h *LatticeHeartbeater) resetLensLagState(name string) {
	h.lensLagMu.Lock()
	defer h.lensLagMu.Unlock()
	delete(h.lensLagState, name)
}

// pruneLensLagState drops debounce state for lenses no longer reported this
// cycle (general-lens sibling of pruneLagState).
func (h *LatticeHeartbeater) pruneLensLagState(seen map[string]struct{}) {
	h.lensLagMu.Lock()
	defer h.lensLagMu.Unlock()
	for name := range h.lensLagState {
		if _, ok := seen[name]; !ok {
			delete(h.lensLagState, name)
		}
	}
}

func (h *LatticeHeartbeater) healthKey() string {
	return "health.refractor." + h.instance
}

// formatISODuration converts a duration to an ISO 8601 duration string (e.g. "PT2M30S").
func formatISODuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	itoa := func(n int64) string { return strconv.FormatInt(n, 10) }
	seconds := int64(d.Seconds())
	if seconds < 60 {
		return "PT" + itoa(seconds) + "S"
	}
	if seconds < 3600 {
		return "PT" + itoa(seconds/60) + "M" + itoa(seconds%60) + "S"
	}
	h := seconds / 3600
	rem := seconds % 3600
	return "PT" + itoa(h) + "H" + itoa(rem/60) + "M" + itoa(rem%60) + "S"
}
