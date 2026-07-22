package pipeline

import (
	"context"

	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// healthReporter is the slice of *health.Reporter the sink writes through.
// Narrowed to an interface so the sink's status arbitration is unit-testable
// without a backing KV bucket.
type healthReporter interface {
	SetActive(ctx context.Context) error
	SetPaused(ctx context.Context, reason, lastError string) error
	SetRebuilding(ctx context.Context) error
	GetStatus(ctx context.Context) (health.Entry, error)
}

// healthSink adapts a *health.Reporter to substrate.HealthSink. The Entry
// schema, KV bucket, and key (the bare ruleID) stay byte-identical — the
// reporter is unchanged; this only maps the substrate pause-reason vocabulary
// onto the reporter's string reasons and back. rebuildInFlight reports whether
// a rebuild rescan is still draining, so a supervisor active-persist during
// that window re-persists "rebuilding" instead of a premature "active".
type healthSink struct {
	reporter        healthReporter
	rebuildInFlight func() bool
}

func newHealthSink(r *health.Reporter, rebuildInFlight func() bool) substrate.HealthSink {
	if r == nil {
		return nil
	}
	return &healthSink{reporter: r, rebuildInFlight: rebuildInFlight}
}

func (h *healthSink) SetActive(ctx context.Context) error {
	if h.rebuildInFlight != nil && h.rebuildInFlight() {
		// A pause that recovers mid-rebuild returns the entry to "rebuilding",
		// not "active" — consumer lag is still non-zero; the rebuild watcher
		// owns the eventual rebuilding → active transition.
		if err := h.reporter.SetRebuilding(ctx); err != nil {
			return err
		}
		if h.rebuildInFlight() {
			return nil
		}
		// The rebuild completed between the flag check and the write — fall
		// through to "active" so the entry is not left "rebuilding" with no
		// watcher remaining to clear it.
	}
	return h.reporter.SetActive(ctx)
}

func (h *healthSink) SetPaused(ctx context.Context, reason substrate.PauseReason, lastErr string) error {
	return h.reporter.SetPaused(ctx, pauseReasonToHealth(reason), lastErr)
}

func (h *healthSink) Load(ctx context.Context) (substrate.HealthStatus, substrate.PauseReason, error) {
	entry, err := h.reporter.GetStatus(ctx)
	if err != nil {
		return substrate.StatusActive, "", err
	}
	if entry.Status != "paused" {
		// "active", "rebuilding" (interrupted), or unknown — treat as active.
		return substrate.StatusActive, "", nil
	}
	if entry.PauseReason == nil {
		// Malformed paused entry — treat as active.
		return substrate.StatusActive, "", nil
	}
	return substrate.StatusPaused, healthReasonToPause(*entry.PauseReason), nil
}

func pauseReasonToHealth(r substrate.PauseReason) string {
	switch r {
	case substrate.PauseManual:
		return health.PauseReasonManual
	case substrate.PauseStructural:
		return health.PauseReasonStructural
	default:
		return health.PauseReasonInfra
	}
}

func healthReasonToPause(s string) substrate.PauseReason {
	switch s {
	case health.PauseReasonManual:
		return substrate.PauseManual
	case health.PauseReasonStructural:
		return substrate.PauseStructural
	default:
		return substrate.PauseInfra
	}
}

// classifyForSupervisor maps Refractor's failure.Category to substrate's
// FailureClass. The supervisor must not import internal/refractor/failure, so
// this adaptation lives on the Refractor side.
func classifyForSupervisor(err error) substrate.FailureClass {
	switch failure.Classify(err) {
	case failure.CatInfra:
		return substrate.ClassInfra
	case failure.CatStructural:
		return substrate.ClassStructural
	case failure.CatTerminal:
		return substrate.ClassTerminal
	default:
		return substrate.ClassTransient
	}
}
