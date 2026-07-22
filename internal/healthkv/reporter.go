package healthkv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Status mirrors Contract #5 §5.3.
type Status string

const (
	StatusStarting     Status = "starting"
	StatusHealthy      Status = "healthy"
	StatusDegraded     Status = "degraded"
	StatusUnhealthy    Status = "unhealthy"
	StatusShuttingDown Status = "shuttingDown"
)

// Issue mirrors Contract #5 §5.5. Since is filled by the Reporter itself
// (first-seen tracking by Code, across ticks); a Probe supplies only
// Code/Severity/Message.
type Issue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Since    string `json:"since,omitempty"`
}

// Snapshot is what a Probe returns each tick.
type Snapshot struct {
	Status  Status
	Issues  []Issue
	Metrics map[string]any
}

// Probe reports the caller's current health, re-checked each tick — it must
// not merely echo a boot-time snapshot (§1.3: a static "always healthy" probe
// is worse than no heartbeat at all). A Probe must not block past the
// Reporter's ProbeTimeout and must not panic; a panic is recovered and
// reported as an unhealthy HealthProbePanicked issue.
type Probe func(ctx context.Context) Snapshot

// Config configures a Reporter.
type Config struct {
	Conn      *substrate.Conn
	Bucket    string // bootstrap.HealthKVBucket
	Component string // e.g. "loftspace-app"
	Instance  string // e.g. "loft-<NanoID>"

	// Interval is the heartbeat cadence (Contract #5 §5.6). Default 10s.
	Interval time.Duration
	// TTL is the per-write NATS-TTL (§5.6). Default Interval*10. A negative
	// value is treated as 0 (falls back to a plain KVPut, no expiry).
	TTL time.Duration
	// ProbeTimeout bounds each Probe call. Default 5s.
	ProbeTimeout time.Duration

	Probe  Probe
	Logger *slog.Logger

	now func() time.Time // injectable clock for tests
}

const (
	defaultInterval = 10 * time.Second
	// DefaultTTLMultiplier is the Contract #5 §5.6 architecture-locked default:
	// a heartbeat's TTL = interval × DefaultTTLMultiplier (100s at the 10s
	// floor). Shared by every Health-KV heartbeat writer (this Reporter and the
	// per-component heartbeaters) so they agree on the derived-TTL convention
	// out of the box.
	DefaultTTLMultiplier = 10
	defaultProbeTimeout  = 5 * time.Second
	healthVersion        = "1.0"
	// DefaultDiagnosticTTL is the fixed (not re-armed) TTL for sparse
	// per-instance diagnostic breadcrumbs (malformed-operation markers,
	// claim-attempt/commit-conflict counters) — mirrors the shipped
	// `internal/processor/step3_auth_trace.go` auth-trace precedent. These are
	// write-once/rolling breadcrumbs, not liveness signals, so they use a fixed
	// window rather than the heartbeat's interval-derived TTL.
	DefaultDiagnosticTTL = time.Hour
)

// Reporter runs a Contract #5 heartbeat loop for a simple, consumer-less
// daemon — a vertical app or service daemon that has no JetStream-consumer
// state to track, just its own dependency set. It is the engines' heartbeater
// loop (Weaver/Loom/Bridge) minus the consumer machinery they need and a
// simple daemon doesn't.
type Reporter struct {
	cfg       Config
	startedAt time.Time
	logger    *slog.Logger

	mu    sync.Mutex
	since map[string]string // issue Code -> first-seen RFC3339, across ticks
}

// New builds a Reporter, applying Contract #5 §5.6 defaults.
func New(cfg Config) *Reporter {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.TTL == 0 {
		cfg.TTL = cfg.Interval * DefaultTTLMultiplier
	}
	if cfg.TTL < 0 {
		cfg.TTL = 0
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = defaultProbeTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &Reporter{
		cfg:       cfg,
		startedAt: cfg.now().UTC(),
		logger:    cfg.Logger,
		since:     make(map[string]string),
	}
}

// Run blocks, emitting a "starting" heartbeat immediately and then a probed
// heartbeat on each Interval tick, until ctx is cancelled — at which point it
// emits one final "shuttingDown" heartbeat (Contract #5 §5.8 step 7) on a
// short detached context so shutdown isn't lost to ctx's own cancellation.
func (r *Reporter) Run(ctx context.Context) {
	r.emitFixed(ctx, StatusStarting)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			r.emitFixed(detached, StatusShuttingDown)
			cancel()
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Reporter) tick(ctx context.Context) {
	snap := r.safeProbe(ctx)
	r.emit(ctx, snap.Status, snap.Issues, snap.Metrics)
}

// safeProbe calls the configured Probe under ProbeTimeout, recovering a panic
// as an unhealthy HealthProbePanicked issue so a broken probe never crashes
// the host app.
func (r *Reporter) safeProbe(ctx context.Context) (snap Snapshot) {
	defer func() {
		if rec := recover(); rec != nil {
			snap = Snapshot{
				Status: StatusUnhealthy,
				Issues: []Issue{{
					Code:     "HealthProbePanicked",
					Severity: "error",
					Message:  fmt.Sprintf("health probe panicked: %v", rec),
				}},
			}
		}
	}()
	pctx, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	defer cancel()
	return r.cfg.Probe(pctx)
}

func (r *Reporter) emitFixed(ctx context.Context, status Status) {
	r.emit(ctx, status, nil, nil)
}

// emit writes one Contract #5 §5.2 heartbeat document.
func (r *Reporter) emit(ctx context.Context, status Status, issues []Issue, metrics map[string]any) {
	now := r.cfg.now().UTC()
	stamped := r.stampSince(now, issues)
	if metrics == nil {
		metrics = map[string]any{}
	}
	key := "health." + r.cfg.Component + "." + r.cfg.Instance
	doc := map[string]any{
		"key":         key,
		"component":   r.cfg.Component,
		"instance":    r.cfg.Instance,
		"version":     healthVersion,
		"status":      string(status),
		"heartbeatAt": now.Format(time.RFC3339Nano),
		"startedAt":   r.startedAt.Format(time.RFC3339Nano),
		"uptime":      formatISODuration(now.Sub(r.startedAt)),
		"metrics":     metrics,
		"issues":      stamped,
	}
	body, err := json.Marshal(doc)
	if err != nil {
		r.logger.Error("healthkv: marshal heartbeat failed", "component", r.cfg.Component, "error", err)
		return
	}
	if _, err := r.cfg.Conn.KVPutWithTTL(ctx, r.cfg.Bucket, key, body, r.cfg.TTL); err != nil {
		r.logger.Warn("healthkv: heartbeat write failed", "component", r.cfg.Component, "key", key, "error", err)
	}
}

// stampSince fills each issue's Since from first-seen tracking by Code and
// drops resolved codes from the in-memory set (Contract #5 §5.5: a Since
// "persists across heartbeats while the issue continues").
func (r *Reporter) stampSince(now time.Time, issues []Issue) []Issue {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]bool, len(issues))
	out := make([]Issue, len(issues))
	for i, iss := range issues {
		seen[iss.Code] = true
		since, ok := r.since[iss.Code]
		if !ok {
			since = now.Format(time.RFC3339Nano)
			r.since[iss.Code] = since
		}
		iss.Since = since
		out[i] = iss
	}
	for code := range r.since {
		if !seen[code] {
			delete(r.since, code)
		}
	}
	return out
}

// formatISODuration renders a Go duration as the ISO 8601 PT…S form used by
// the other components' heartbeats (mirrors internal/processor/health.go).
func formatISODuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int64(d.Seconds())
	hours := secs / 3600
	mins := (secs % 3600) / 60
	rem := secs % 60
	if hours > 0 {
		return fmt.Sprintf("PT%dH%dM%dS", hours, mins, rem)
	}
	if mins > 0 {
		return fmt.Sprintf("PT%dM%dS", mins, rem)
	}
	return fmt.Sprintf("PT%dS", rem)
}
