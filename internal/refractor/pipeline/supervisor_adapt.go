package pipeline

import (
	"context"

	"github.com/asolgan/lattice/internal/refractor/failure"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/substrate"
)

// healthSink adapts a *health.Reporter to substrate.HealthSink. The Entry
// schema, KV bucket, and key (the bare ruleID) stay byte-identical — the
// reporter is unchanged; this only maps the substrate pause-reason vocabulary
// onto the reporter's string reasons and back.
type healthSink struct {
	reporter *health.Reporter
}

func newHealthSink(r *health.Reporter) substrate.HealthSink {
	if r == nil {
		return nil
	}
	return &healthSink{reporter: r}
}

func (h *healthSink) SetActive(ctx context.Context) error {
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
