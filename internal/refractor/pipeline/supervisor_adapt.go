package pipeline

import (
	"context"
	"sync"

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
	RecordStructuralAutoRecovery(ctx context.Context, cause string, attempts int) error
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

	// restoredStructuralCause carries a RESTORED structural pause's diagnosis
	// from Load to the self-heal announcement, which is the only path on which
	// the supervisor has none of its own: substrate.HealthSink.Load reports
	// status and reason, never the cause, so a pause this process did not itself
	// enter has no diagnosis in memory. That is the likeliest real recovery of
	// all — a lens paused before a restart, healed by the probe on the way back
	// up — and an announcement with an empty cause is barely better than the
	// silent auto-heal the announcement exists to refuse.
	//
	// Lifetime, because a cached string with no boundary is how a later
	// recovery inherits an unrelated diagnosis:
	//   - Written by Load, and only for a restored STRUCTURAL pause.
	//   - Read once, by RecordStructuralAutoRecovery, and only when the
	//     supervisor supplied no cause.
	//   - Cleared on that read, and on a STRUCTURAL SetPaused: a newly persisted
	//     structural pause carries its own cause, which the supervisor hands back
	//     itself. NOT cleared on an infra or manual pause — neither carries a
	//     structural diagnosis, so clearing there would drop the restored cause
	//     with nothing to replace it, and the restart path runs an
	//     InitialPause-seeded SetPaused(infra, "") between the structural clear
	//     and the announcement.
	//   - NOT cleared by SetActive — deliberately. SetActive is what nils
	//     LastError on the entry, and it runs at the structural clear, BEFORE
	//     the announcement. Re-reading the entry at announcement time would find
	//     nothing, which is why the capture is at Load.
	//   - In-process and per-sink; a restart re-establishes it from the entry on
	//     the next Load.
	//
	// The string is there to be read at all only because a structurally-paused
	// entry keeps its LastError for the life of the pause — the clean-registration
	// clear skips it. That guarantee and this fallback are one mechanism.
	mu                      sync.Mutex
	restoredStructuralCause string
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
	// A STRUCTURAL pause persisted by this process supersedes whatever was
	// restored: the supervisor now holds a diagnosis of its own and hands it
	// back at the announcement, so keeping the older one could only attribute it
	// to a newer recovery.
	//
	// Only structural. An infra or manual pause carries no structural diagnosis
	// and never will, so discarding on those would drop the restored cause with
	// nothing to replace it — and on the restart path that is not a corner case,
	// it is THE path. Every lens opted into the structural probe is opted in by
	// the same predicate that gives it InitialPause, so its way back up is:
	// restore structural → probe passes → runPump re-seeds the infra gate →
	// SetPaused(infra, "") → infra probe passes → announce. Clearing here would
	// eat the cause one step before the announcement that needs it, on the
	// recovery the design calls the likeliest of all.
	if reason == substrate.PauseStructural {
		h.takeRestoredStructuralCause()
	}
	return h.reporter.SetPaused(ctx, pauseReasonToHealth(reason), lastErr)
}

// takeRestoredStructuralCause returns the restored diagnosis and clears it, so
// it can be consumed exactly once.
func (h *healthSink) takeRestoredStructuralCause() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	cause := h.restoredStructuralCause
	h.restoredStructuralCause = ""
	return cause
}

// RecordStructuralAutoRecovery satisfies substrate.StructuralRecoveryAnnouncer,
// the optional half of HealthSink: Refractor is the one component whose lenses
// probe their own way out of a structural pause, so it is the one sink that has
// anything to record. Status and pause reason are already written by the
// supervisor's own SetActive path; this adds only the fact of the self-heal,
// which nothing else on the entry can express — a recovered lens reads `active`
// exactly like one that never faulted.
//
// An empty cause means the pause was RESTORED rather than entered by this
// process, so the supervisor never saw a diagnosis; the one Load stashed off the
// entry stands in. A cause the supervisor did supply always wins — it describes
// the pause that actually just cleared, where the stashed one describes whatever
// was on the entry at boot.
func (h *healthSink) RecordStructuralAutoRecovery(ctx context.Context, cause string, attempts int) error {
	restored := h.takeRestoredStructuralCause()
	if cause == "" {
		cause = restored
	}
	return h.reporter.RecordStructuralAutoRecovery(ctx, cause, attempts)
}

func (h *healthSink) Load(ctx context.Context) (substrate.HealthStatus, substrate.PauseReason, error) {
	entry, err := h.reporter.GetStatus(ctx)
	if err != nil {
		return substrate.StatusActive, "", err
	}
	if entry.Status != health.StatusPaused {
		// "active", "rebuilding" (interrupted), or unknown — treat as active.
		return substrate.StatusActive, "", nil
	}
	if entry.PauseReason == nil {
		// Malformed paused entry — treat as active.
		return substrate.StatusActive, "", nil
	}
	reason := healthReasonToPause(*entry.PauseReason)
	if reason == substrate.PauseStructural && entry.LastError != nil {
		// The one read that captures the restored pause's diagnosis while it
		// still exists. See restoredStructuralCause for why it cannot be read
		// any later.
		h.mu.Lock()
		h.restoredStructuralCause = *entry.LastError
		h.mu.Unlock()
	}
	return substrate.StatusPaused, reason, nil
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
	case failure.CatPrivacyCritical:
		// Unreachable today: a shredded identity's projected row that cannot be
		// nullified is raised via failure.PrivacyCritical at
		// keyshredded/manager.go:382,399 and routed through control.PauseRule
		// straight to a manual pause — never through a spec.Handler return
		// value, so this arm never actually runs (docs/components/
		// refractor-failure-tiers.md: "paused immediately, alerted, and never
		// auto-retried"). The case exists so a future change that DOES route a
		// CatPrivacyCritical error through the handler does not fall to
		// default and get ClassTransient — an infinite auto-redelivery loop,
		// the literal opposite of "never auto-retry".
		//
		// ClassTerminal, not ClassStructural or ClassInfra: this fire adds
		// probe-driven auto-recovery to a structural pause whenever
		// ConsumerSpec.StructuralProbe is set, which cmd/refractor/main.go
		// does for exactly the Protected/GrantTable postgres lenses — the same
		// lenses a nullification failure would occur on. VerifyProtectedTable/
		// VerifyGrantTable verify table/RLS/constraint POSTURE; they say
		// nothing about whether a specific row was ever nullified, so a
		// passing probe would clear the pause while the confidentiality
		// breach is still live — auto-recovering exactly the failure this
		// tier exists to keep operator-gated. ClassInfra is no safer: its
		// probe loop runs unconditionally whenever spec.Probe is set (no
		// opt-in at all, consumer_supervisor_pump.go's runProbeLoop), so it
		// would clear on the next successful pool.Ping regardless. Terminal
		// carries none of that pause-then-probe machinery — a Terminal
		// classification never enters the pump's probe loop — so nothing here
		// can silently recover it away.
		return substrate.ClassTerminal
	default:
		return substrate.ClassTransient
	}
}
