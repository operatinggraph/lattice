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

	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
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
	// issueCapabilityCoverageDivergence is raised when the auth-plane
	// convergence sweep heals a graph ↔ Capability-KV divergence
	// (capability-projection-reconciliation-design.md §3.2). The two lag codes
	// above watch the consumer; this one watches the truth — a caught-up
	// consumer that MISSED events reads as fine on every other signal.
	issueCapabilityCoverageDivergence = "CapabilityCoverageDivergence"
	// issueCapabilityRepairFailing is raised when the convergence sweep's own
	// repair cannot be written. It covers the blind spot of the code above: a
	// repair that errors heals nothing, so the divergent streak clears and
	// every other signal reads as converged while the row stays wrong.
	issueCapabilityRepairFailing = "CapabilityRepairFailing"
	// issueCapabilitySweepStalled is raised when the sweep has reached no
	// verdict for longer than its staleness window. It covers the blind spot of
	// BOTH codes above: they are keyed on what the last pass found, and a sweep
	// that stops passing keeps republishing that last finding forever. A
	// suppressed tick (rebuild in flight, unreadable lens status) verifies
	// nothing, so without this code a lens held suppressed indefinitely reads
	// converged — the detector's own liveness needs its own signal.
	issueCapabilitySweepStalled = "CapabilitySweepStalled"
	// issueCapabilityLensUnreadable is raised when a lens's liveness inputs
	// cannot be read this cycle. Its purpose is that such a lens is still
	// REPORTED: an auth-plane lens missing from metrics.capabilityLens is
	// indistinguishable from one that was never installed, which is the
	// monitoring equivalent of reporting healthy.
	issueCapabilityLensUnreadable = "CapabilityLensUnreadable"
	// issueCapabilityLensStructuralPauseAutoRecovered is the auth-plane sibling
	// of issueLensStructuralPauseAutoRecovered: an auth-plane lens cleared a
	// structural pause under its own probe, with no operator involved.
	//
	// A separate code from the business one, not a shared code raised twice.
	// Every capability condition here already has its own code even where the
	// business path carries an exactly parallel one (Paused, Lagging,
	// Unreadable), and the parallel case is precisely the case that convention
	// answers. Two reasons of its own: the messages are materially different —
	// this one names a rebuild obligation that exists only for grants — and the
	// two planes reconcile through independent open-issue state keyed by code,
	// so one code written by both reconcilers would be two `since` clocks
	// disagreeing about when one issue began. They are two identities; this
	// makes them two codes.
	issueCapabilityLensStructuralPauseAutoRecovered = "CapabilityLensStructuralPauseAutoRecovered"
	// issueCapabilityAuditUnverified is raised when the sweep examined an
	// anchor and could reach NO conclusion about it. It covers the blind spot
	// the divergence and repair codes share: both are inferred from what the
	// sweep managed to WRITE, so an anchor whose divergence has no repair
	// transport at all produces no write, no heal, no error — and clears the
	// divergent streak. The lens then reads healthier the more thoroughly it
	// is broken. An unverified anchor is worse than a healed divergence (the
	// sweep does not know what it is looking at) and better than a confirmed
	// unrepairable row (which it does know, and cannot fix).
	issueCapabilityAuditUnverified = "CapabilityAuditUnverified"
	// issueCapabilityRepairBlocked is raised when the sweep found a divergence
	// and the ordering guard refused the repair (Contract #6 §6.2 — the stored
	// watermark equals or exceeds the reconciliation's token). It is the worst
	// of this family: there is no error to retry, because the write returned
	// success having changed nothing, and the row stays wrong until a real CDC
	// event above that watermark reprojects it. On the auth plane that row is
	// a permission set the graph no longer grants.
	issueCapabilityRepairBlocked = "CapabilityRepairBlocked"
)

// alertRank orders the single-valued per-lens `alert` field. Two conditions can
// hold at once — a lens can be lagging AND holding an unrepairable row — so the
// field needs a total order, and encoding that order in the CALL SEQUENCE of
// the checks that set it makes it invisible and re-derivable only by reading
// every branch. It ships as a table with a test instead.
//
// The order, worst first, and why:
//   - secure-redaction: the read model is not stale or frozen but CONFIDENTLY
//     WRONG, and being served that way — a row rendering a null the reader
//     cannot distinguish from a lawfully erased record. Above paused because a
//     frozen lens misleads nobody, and every condition below is about a read
//     model being behind or unverified rather than actively lying.
//   - paused: the sweep is suppressed while paused, so any verdict under it is
//     a frozen artifact rather than a live one.
//   - unreadable: the lens's own inputs could not be read, so the quieter
//     values below are claims made on missing evidence.
//   - repair-failing: a row that is wrong NOW and whose repair errored.
//   - repair-blocked: also wrong now, and the repair returned success having
//     changed nothing — one rank quieter than repair-failing only because
//     there is no fault to chase, not because it is more benign.
//   - sweep-stalled: the detector itself is not running, so every value below
//     is stale by construction.
//   - audit-stalled: the OTHER detector is not running. One rank quieter than
//     sweep-stalled for one reason only — the sweep both detects and repairs, so
//     its silence stops repairs as well as verdicts, while the audit is
//     read-only and its silence costs verdicts alone. Still above everything
//     below it, for sweep-stalled's own reason: a halted detector makes every
//     quieter value stale by construction.
//   - unverified: the detector ran and could reach no conclusion.
//   - diverged: the audit ran, reached a conclusion, and the conclusion is that
//     the read model is WRONG. Below unverified deliberately: a divergence is a
//     bounded, named fact an operator can act on with the reproject RPC, while
//     an unverified anchor means the detector does not know what it is looking
//     at — and an unknown of unknown size outranks a known of known size.
//   - lagging: the read model is merely behind, and will catch up.
//   - structural-pause-auto-recovered: the quietest token, and the only one
//     that describes a lens which is fine RIGHT NOW — it reports a window that
//     has already closed, so anything currently wrong outranks it. It is not
//     "ok" because the recovery may have left the read model owing a rebuild.
//
// Nothing displaced is lost: each condition raises its own issue, and the
// underlying counters travel in the same metrics map.
var alertRank = map[string]int{
	"secure-redaction":                11,
	"paused":                          10,
	"unreadable":                      9,
	"repair-failing":                  8,
	"repair-blocked":                  7,
	"sweep-stalled":                   6,
	"audit-stalled":                   5,
	"unverified":                      4,
	"diverged":                        3,
	"lagging":                         2,
	"structural-pause-auto-recovered": 1,
	"ok":                              0,
	"":                                0,
}

// raiseAlert returns whichever of the two alerts ranks worse, so a caller never
// has to know what else may already have been set.
func raiseAlert(current, candidate string) string {
	if alertRank[candidate] > alertRank[current] {
		return candidate
	}
	return current
}

// capabilityDivergenceErrorStreak is the number of CONSECUTIVE sweep passes
// that must each heal a divergence before the issue escalates from warning to
// error. One divergent pass is a repaired incident; a second in a row means the
// sweep is papering over an ongoing delivery gap rather than a past one.
const capabilityDivergenceErrorStreak = 2

// capabilityRepairWarnStreak / capabilityRepairErrorStreak are the escalation
// window for an UNREPAIRED divergence. A single failing pass raises nothing: a
// failing anchor is retried on the very next pass (no backoff after the first
// failure), so an isolated write error clears itself inside one interval and
// alerting on it would flip the whole instance to degraded for every blip. Two
// consecutive failing passes is a real fault, and a third means it is not
// converging on its own — which, unlike a healed divergence, leaves the row
// wrong the entire time.
const (
	capabilityRepairWarnStreak  = 2
	capabilityRepairErrorStreak = 3
)

// defaultCapabilitySweepStallCycles is how many sweep intervals may elapse with
// no verdict before CapabilitySweepStalled is raised — 10, so ~10 minutes at the
// 60s sweep default. The window is scaled off the sweep's own cadence rather
// than being a second independently-tuned duration, and it is generous on
// purpose: a suppressed sweep is a *detector* outage, not a data outage, so
// alerting inside one or two intervals would flag every ordinary rebuild.
// Deployment-overridable via CapabilitySweepStallCycles.
const defaultCapabilitySweepStallCycles = 10

// capabilitySweepStallErrorMultiplier escalates a stall that has a named cause
// from warning to error once it has lasted this many staleness windows — the
// escalation ladder its sibling codes carry as streak counts. A named cause is
// something an operator can clear, so the first window is a warning; still
// unswept 30 minutes later (at the defaults) nobody is clearing it, and the
// auth-plane read model has gone unverified long enough to outrank a degrade.
// A rebuild that is DRAINING is exempt from the escalation, never from the
// warning: a rebuild is a SUPERSET of the sweep (truncate + full rescan), so a
// long one is not an unverified projection. How long a rebuild may legitimately
// run is the rebuild's own signal to own — and it owns one now (see
// evalRebuildWedged), so a rebuild that has stopped draining no longer inherits
// the exemption its draining sibling earns.
const capabilitySweepStallErrorMultiplier = 3

// evalRebuildWedged reports how long a rebuild has gone without draining a
// single message, and whether that is long enough to call it stuck rather than
// slow. The window is the same staleness window the sweep would have been judged
// on: the rebuild took the sweep's place, so it answers to the sweep's clock.
//
// A rebuild that has never reported (zero timestamp) is unknown, not wedged —
// the poll interval means there is a real gap between Rebuild returning and the
// first count arriving, and reporting a rebuild as stuck the instant it starts
// would fire on every one of them.
func evalRebuildWedged(s CapabilityLensStatus, now time.Time, stallAfter time.Duration) (time.Duration, bool) {
	if s.RebuildProgressAt.IsZero() {
		return 0, false
	}
	since := now.Sub(s.RebuildProgressAt)
	return since, since > stallAfter
}

// capabilitySweepSuppressionFreshnessCycles bounds how old a recorded
// suppression reason may be and still count as explaining the current stall.
// The reason describes the last tick, so a sweep wedged INSIDE a tick keeps
// publishing the previous tick's reason; without this bound a wedged sweep would
// present as merely suppressed and be downgraded to a warning. Two intervals,
// because the reason is refreshed once per interval while suppression holds.
const capabilitySweepSuppressionFreshnessCycles = 2

// defaultLensLagThreshold / defaultLensLagRaiseCycles mirror the capability-lens
// defaults (lens-projection-liveness-design.md §5.7 — the cap path's
// battle-tested defaults, reused rather than a fresh tuning exercise).
// Deployment-overridable via LensLagThreshold / LensLagRaiseCycles.
const (
	defaultLensLagThreshold    = 100
	defaultLensLagRaiseCycles  = 3
	issueLensProjectionPaused  = "LensProjectionPaused"
	issueLensProjectionLagging = "LensProjectionLagging"
	// issueLensSecureRedaction is raised when a lens has projected any
	// secure-column null it could not resolve. `error`, not `warning`, and
	// unlike every other business-lens code here: those describe a read model
	// that is stale or frozen, which is visibly wrong to whoever reads it. This
	// one describes a read model that is CONFIDENTLY wrong — the row renders,
	// with a null that is indistinguishable from an erased record, so nothing
	// downstream can tell a defect from a lawful erasure. Nothing else in the
	// system carries that distinction.
	issueLensSecureRedaction = "LensSecureRedaction"
	// issueLensProjectionUnreadable is raised when a business lens's liveness
	// inputs cannot be read. The general-lens mirror of
	// CapabilityLensUnreadable: an unobserved read model must say so rather
	// than vanish from the metric map, where it is indistinguishable from a
	// lens that never existed.
	issueLensProjectionUnreadable = "LensProjectionUnreadable"
	// The convergence sweep's three verdicts for a business lens, mirroring the
	// capability codes one for one. They stay `warning` at every streak length,
	// where the cap path escalates to `error`: the invariant this path is built
	// on is that a single business lens's read model, however wrong, degrades
	// the instance rather than failing it. The streaks are still carried in the
	// message, so an operator sees the difference between a blip and a lens
	// that has been unrepaired for an hour.
	issueLensCoverageDivergence = "LensCoverageDivergence"
	issueLensRepairFailing      = "LensRepairFailing"
	issueLensSweepStalled       = "LensSweepStalled"
	issueLensAuditUnverified    = "LensAuditUnverified"
	issueLensRepairBlocked      = "LensRepairBlocked"
	// The divergence audit's two codes (lens-projection-divergence-audit-
	// design.md §4.3). They are NOT the sweep's codes reused: every code above
	// is inferred from what a repair managed to WRITE, while these two come from
	// a detector that writes nothing at all, so a lens can carry one family
	// silent and the other loud — and which one it is, is the finding.
	//
	// issueLensProjectionDiverged says the projection disagrees with the graph
	// and NOTHING has been done about it. That is deliberate: the audit detects
	// and reports, and repair on a shared, unguarded plain target was rejected
	// (§8.1) precisely because coupling this detector to a writer would rebuild
	// the structure whose collapse produced the twelve-day silence. The
	// remediation is the operator's control-plane reproject RPC or Rebuild.
	issueLensProjectionDiverged = "LensProjectionDiverged"
	// issueLensAuditStalled is the audit's own liveness, and it covers exactly
	// the blind spot the code above has: every audit counter describes the last
	// pass, so an audit that stops passing republishes its final clean verdict
	// forever. A refused lens never raises it — it has no cadence to be late
	// against, and "not audited" must not read as "audited and late".
	issueLensAuditStalled = "LensAuditStalled"
	// issueLensStructuralPauseAutoRecovered is raised when a business lens
	// cleared a structural pause on its own — its probe re-verified the condition
	// and the consumer resumed with nobody involved. It and its auth-plane
	// sibling (CapabilityLensStructuralPauseAutoRecovered) are the only codes
	// that fire on a lens whose read model is healthy at the moment they fire:
	// the lens was DARK for the length of the pause, the resume replays nothing
	// from that window, and the entry it leaves behind reads `active` exactly
	// like a lens that never faulted. Without this the operator's only evidence
	// that a rebuild may be owed is a log line. It is deliberately short-lived
	// (structuralAutoRecoveredFreshnessCycles) — a recovery is an event, not a
	// condition, and an issue that never clears stops being read.
	issueLensStructuralPauseAutoRecovered = "LensStructuralPauseAutoRecovered"
)

// structuralAutoRecoveredFreshnessCycles is how many heartbeat cycles a
// probe-driven structural recovery stays raised for. Two, not one: the pump
// stamps the recovery at an arbitrary point inside a cycle, so a strict
// one-cycle window can be straddled — the stamp lands just after one beat has
// read the entry and has already aged out by the next — and emit NOTHING, which
// is exactly the silent self-heal this signal exists to refuse. Two guarantees
// at least one emission and at most two. Mirrors
// capabilitySweepSuppressionFreshnessCycles, which bounds a recorded value
// against the same cadence for the same kind of reason.
const structuralAutoRecoveredFreshnessCycles = 2

// minHeartbeatInterval is the NFR-O1 heartbeat floor. Every cadence-derived
// window is measured against at least this much, so a heartbeater assembled
// without NewLatticeHeartbeater never computes a zero-length one.
const minHeartbeatInterval = 10 * time.Second

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
	Status        string // "active" | "paused" | "rebuilding" | "unknown"
	PauseReason   string // "" when not paused
	// LastError is the pause's recorded cause, "" when there is none. A
	// structural pause is held until a human reconciles it, so this text is the
	// whole of what the operator has to act on — the issue message carries it,
	// truncated, rather than making them open a browser to learn which column or
	// table the projection failed on.
	LastError   string
	ConsumerLag uint64
	// Unreadable, when non-empty, is the error that stopped this cycle from
	// reading the lens's liveness inputs (its health entry or its consumer's
	// pending count). Status/ConsumerLag are then not to be trusted, and the
	// snapshot exists only so the lens keeps its place in the metric map with an
	// honest "we cannot tell" — the alternative, dropping it, is indistinguishable
	// from a lens that no longer exists.
	Unreadable string
	// SweepReconciled is the lens's cumulative count of divergent projections
	// the convergence sweep has healed; SweepDivergentStreak is how many
	// consecutive sweep passes have each healed at least one. Zero for a lens
	// with no sweeper installed — which is NOT "every non-auth-plane target":
	// the enrolment gate grants a plan to any actor-aggregate lens that can
	// scope a listing to its own rows and round-trip its own keys, business
	// lenses included.
	SweepReconciled      uint64
	SweepDivergentStreak int
	// SweepFailingActors is how many anchors currently carry an unrepaired
	// reprojection failure and SweepFailedStreak how many consecutive passes
	// ended with at least one; SweepLastFailure is the governing error's text.
	// This is the sweep's REPAIR verdict, independent of the divergence verdict
	// above and strictly worse: a divergence the sweep healed leaves a correct
	// row, one it could not write leaves the row wrong — and heals nothing, so
	// the divergent streak clears and the lens otherwise reads as converged.
	SweepFailingActors int
	SweepFailedStreak  int
	SweepLastFailure   string
	// SweepUnverified is how many anchors the last pass reached no verdict on,
	// SweepUnverifiedStreak how many consecutive passes carried at least one,
	// and SweepLastUnverified the governing reason. SweepBlocked and its
	// siblings are the same shape for a divergence the ordering guard refused
	// to let the sweep repair.
	//
	// These are the outcomes the divergence and repair verdicts above cannot
	// express, because both are inferred from a WRITE: an anchor with no
	// repair transport produces no write and no error, so it clears the
	// divergent streak and reads as converged.
	SweepUnverified       int
	SweepUnverifiedStreak int
	SweepLastUnverified   string
	SweepBlocked          int
	SweepBlockedStreak    int
	SweepLastBlocked      string
	// SweepLastPassAt is when the sweep last reached a verdict and SweepInterval
	// its tick period; SweepSuppression names why the most recent tick was
	// skipped ("" when it ran). Together they are the sweep's own liveness: every
	// counter above describes the last pass, so a sweep that stops passing
	// republishes a stale verdict indefinitely and only this clock contradicts it.
	// SweepInterval is zero for a lens with no sweeper installed, which is what
	// gates the staleness evaluation. SweepSuppressionAt is when the reason was
	// recorded: it describes the LAST tick, so a stale one means the sweep is
	// wedged rather than suppressed, and only the timestamp separates the two.
	SweepLastPassAt    time.Time
	SweepSuppression   string
	SweepSuppressionAt time.Time
	SweepInterval      time.Duration
	// The divergence audit's verdicts. An auth-plane lens cannot enrol today —
	// every one is actor-aggregate (the audit refuses those to the sweep) or
	// targets a store that cannot read a row back — so in practice this path
	// carries AuditEnrolled=false plus the reason, which is the whole point of
	// carrying it: the GrantTable producers and the Protected secure lens must
	// read as REFUSED rather than as absent, or "not audited" is
	// indistinguishable from "audited, clean".
	AuditEnrolled         bool
	AuditRefusal          string
	Audited               int
	DivergentRows         map[string]int
	DivergentTotal        int
	AuditUnverified       int
	AuditLastUnverified   string
	AuditLastPassAt       time.Time
	AuditCycleCompletedAt time.Time
	AuditCoverageBasis    string
	AuditListingSize      int
	AuditSuppression      string
	AuditSuppressionAt    time.Time
	AuditInterval         time.Duration
	// RebuildOutstanding is the rebuild's most recent un-drained count and
	// RebuildProgressAt when that count last DECREASED. A rebuild suppresses the
	// sweep, so while one runs the sweep's silence is explained and the stall
	// detector can say nothing about it — these are what let the detector judge
	// the rebuild instead. A rebuild that is draining keeps advancing the
	// timestamp; one that is wedged does not, and neither does one whose
	// outstanding poll keeps erroring (that retry is unbounded). Zero
	// RebuildProgressAt means no rebuild has reported yet — unknown, not stalled.
	RebuildOutstanding uint64
	RebuildProgressAt  time.Time
	// The lens's last probe-driven structural recovery: when it cleared, the
	// diagnosis it cleared from, and which self-heal attempt lifted it. Zero
	// time for a lens that has never self-healed.
	//
	// Every grant-table lens is auth-plane by definition (projection.IsAuthPlane
	// returns true for postgres+GrantTable), and a grant-table lens is exactly
	// the class whose Probe verifies its own posture — so the lenses that feed
	// actor_read_grants, the read-path authorization source of truth, are the
	// ones most able to clear a structural pause with nobody watching. The
	// pause's own backlog replays on resume, so a schema fix owes nothing; but a
	// condition cleared by re-provisioning or restoring the table leaves every
	// grant written BEFORE the pause gone and unreplayable — a persistent
	// under-grant, safe in direction (reads fail closed) but silent, and these
	// fields are the only thing that says which of the two happened.
	StructuralAutoRecoveredAt      time.Time
	StructuralAutoRecoveredCause   string
	StructuralAutoRecoveryAttempts int
}

// LensLivenessStatus is one non-auth-plane (business) lens's liveness snapshot,
// supplied by LensProvider for the general projection-liveness backstop
// (lens-projection-liveness-design.md §3.3). Mirrors CapabilityLensStatus plus
// the lastProjectedAt progress clock; auth-plane lenses are excluded — the
// CapabilityLensProvider path owns them.
type LensLivenessStatus struct {
	CanonicalName string
	RuleID        string
	Status        string // "active" | "paused" | "rebuilding" | "unknown"
	PauseReason   string // "" when not paused
	// LastError is the pause's recorded cause, "" when there is none — carried
	// into the issue message for the reason the capability path carries it.
	LastError       string
	ProjectionLag   uint64
	LastProjectedAt time.Time // zero if never projected
	// Unreadable, when non-empty, is the error that stopped this cycle from
	// reading the lens's liveness inputs. Status/ProjectionLag are then not to be
	// trusted, and the snapshot exists only so the lens keeps its place in the
	// metric map with an honest "we cannot tell" — dropping it is
	// indistinguishable from a lens that was never installed, which is the
	// quietest way for a read model to go unobserved.
	Unreadable string
	// The convergence sweep's verdicts, carried for a business lens exactly as
	// CapabilityLensStatus carries them for an auth-plane one. They answer what
	// the lag and pause fields structurally cannot: a lens can be active and
	// caught up while its projection has a hole, because the events that would
	// have filled it are in the past.
	SweepReconciled       uint64
	SweepDivergentStreak  int
	SweepFailingActors    int
	SweepFailedStreak     int
	SweepLastFailure      string
	SweepUnverified       int
	SweepUnverifiedStreak int
	SweepLastUnverified   string
	SweepBlocked          int
	SweepBlockedStreak    int
	SweepLastBlocked      string
	SweepLastPassAt       time.Time
	SweepSuppression      string
	SweepSuppressionAt    time.Time
	SweepInterval         time.Duration
	// The divergence audit's verdicts — the plain corpus's FIRST per-row
	// correctness signal (lens-projection-divergence-audit-design.md §4.3).
	// They answer what neither the liveness fields nor the sweep fields can for
	// a plain lens: the liveness plane says whether the lens is still moving,
	// the sweep is never installed here at all, and nothing says whether what
	// the lens already wrote is still TRUE.
	//
	// AuditEnrolled is the install-time verdict and AuditRefusal the reason
	// behind a false one; a refused lens publishes nothing else and can never
	// read as audit-stalled, because "not audited" must stay distinguishable
	// from "audited, clean".
	AuditEnrolled bool
	AuditRefusal  string
	// Audited counts ANCHORS the last pass concluded about; DivergentRows and
	// DivergentTotal count ROWS. DivergentRows carries only the classes that
	// actually fired (missing / stale / retained), so a direction that has
	// silently stopped detecting reads as ABSENT rather than as zero.
	Audited        int
	DivergentRows  map[string]int
	DivergentTotal int
	// AuditUnverified is how many anchors the last pass could conclude nothing
	// about, and AuditLastUnverified the governing reason. Named apart from the
	// sweep's SweepUnverified deliberately: they are two detectors' verdicts
	// about different anchors, and one field for both would let either erase
	// the other's finding.
	AuditUnverified     int
	AuditLastUnverified string
	// AuditLastPassAt is the audit's liveness clock and AuditInterval its
	// cadence — zero for a refused lens, which is what gates the staleness
	// evaluation off. AuditCycleCompletedAt is the coverage claim: a pass
	// covers at most one batch, so DivergentTotal == 0 says nothing about the
	// whole lens until a cycle has closed over it. AuditListingSize is how many
	// anchor keys the type filter matched, so a pathologically large anchor
	// type is visible rather than merely expensive, and AuditCoverageBasis
	// names how they were enumerated — the audit's under-coverage boundary,
	// published rather than assumed away.
	AuditLastPassAt       time.Time
	AuditCycleCompletedAt time.Time
	AuditCoverageBasis    string
	AuditListingSize      int
	// AuditSuppression names why the most recent tick verified nothing ("" when
	// it ran) and AuditSuppressionAt when that was recorded — the timestamp is
	// what separates an audit held suppressed from one wedged inside a tick,
	// since both keep publishing the same reason.
	AuditSuppression   string
	AuditSuppressionAt time.Time
	AuditInterval      time.Duration
	// SecureRedactions is the cumulative count of secure-column values this
	// lens projected as null because it could not resolve them (fork F2). It
	// answers what no liveness field can: the lens is active, caught up and
	// writing rows, and those rows carry nulls where plaintext belongs. A
	// legitimate shred is not counted, so any nonzero value is a defect.
	SecureRedactions uint64
	// The lens's last probe-driven structural recovery: when it cleared, the
	// diagnosis it cleared from, and which self-heal attempt lifted it. Zero
	// time for a lens that has never self-healed. They are the only inputs here
	// that describe a lens which is CURRENTLY fine — every other field is a
	// present-tense condition — and they are carried for exactly that reason: a
	// pause that heals itself leaves an entry indistinguishable from one that
	// never faulted, so nothing would tell an operator that a window of events
	// went unprojected and a rebuild may still be owed.
	StructuralAutoRecoveredAt      time.Time
	StructuralAutoRecoveredCause   string
	StructuralAutoRecoveryAttempts int
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
// TaxonomyLivenessSnapshot is the taxonomy resolver's currency claim as the
// heartbeat renders it. It mirrors lens.TaxonomyLivenessStatus rather than
// importing it: internal/refractor/health does not depend on
// internal/refractor/lens, and the health entry's shape is its own contract
// (docs/observability/health-kv-schema.md) regardless of who produces it.
type TaxonomyLivenessSnapshot struct {
	// Armed is the claim itself: the resolver's snapshot is backed by a live,
	// drained invalidation consumer, so a `*` lens may narrow against it.
	Armed bool
	// Dead reports that the feeding subscription failed terminally, so Armed
	// can never become true again without a restart — the difference between
	// "waiting" and "will wait forever".
	Dead bool
	// UnarmedSince is when the resolver last stopped being (or has never yet
	// become) armed. Zero while armed.
	UnarmedSince time.Time
	// ProbeFailures is the current run of consecutive drain-probe failures,
	// the ordinary reason a resolver stays unarmed with nothing else to show.
	ProbeFailures int
}

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

	// TaxonomyLivenessProvider optionally returns the taxonomy resolver's
	// currency claim — emitted as metrics.taxonomyLiveness
	// (dynamic-type-taxonomy-design.md §4.2). It exists because that state is
	// otherwise invisible: an unarmed resolver forces every `*`-carrying lens
	// onto the broad filter, and the per-lens reason recording that
	// (filterBroadReason `taxonomy-unarmed`) is the LOWEST-ranked cause, so a
	// lens that is also non-exhaustive for its own reasons reports that
	// instead and the unarmed state never surfaces at all. A resolver stuck
	// unarmed is an instance-level fact and belongs on the instance's entry,
	// not inferred from whichever reason a lens happened to rank first. nil
	// omits the field.
	TaxonomyLivenessProvider func() TaxonomyLivenessSnapshot

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

	// CapabilitySweepStallCycles is how many sweep intervals may elapse with no
	// verdict before CapabilitySweepStalled is raised. Zero selects
	// defaultCapabilitySweepStallCycles.
	CapabilitySweepStallCycles uint64

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
	// sweepBase is the floor under each capability lens's sweep-staleness clock,
	// keyed by capLensName and guarded by lagMu alongside lagState (same per-lens
	// cap-path state, pruned in the same place). It is stamped when a lens is
	// first reported and moved forward while the lens is exempt from the verdict,
	// so neither a lens that registered a moment ago nor one just resumed from a
	// long pause is charged for time it could not have swept in. Deliberately NOT
	// folded into lagState, which resetLagState deletes whenever a lens leaves the
	// active state — precisely the case (rebuilding) this clock must survive.
	sweepBase map[string]time.Time
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
	// lensSweepBase is the business-lens sibling of sweepBase, separate for the
	// same reason: the cap path prunes sweepBase against the auth-plane lens
	// set, so one shared map would have each path evicting the other's
	// staleness baselines every cycle and neither lens ever reading stalled.
	lensSweepBase map[string]time.Time
	// lensAuditBase is the divergence audit's staleness baseline, separate from
	// lensSweepBase for a reason the two-map split above does not cover: the two
	// detectors run on DIFFERENT cadences (15 minutes against 5), so one shared
	// baseline would have whichever ran last excuse the other's silence — and
	// the audit's whole subject is a detector that has stopped without saying so.
	lensAuditBase map[string]time.Time

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
	if interval < minHeartbeatInterval {
		interval = minHeartbeatInterval
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
	// Taxonomy currency (dynamic-type-taxonomy-design.md §4.2). Emitted every
	// cycle including the healthy armed:true state, so an observer can render
	// the green case and — more to the point — can tell "unarmed for a second
	// during boot replay", which is the design working, from "unarmed for ten
	// minutes", which is not. unarmedSeconds is the field that distinguishes
	// them; the flag alone cannot.
	if h.TaxonomyLivenessProvider != nil {
		tl := h.TaxonomyLivenessProvider()
		entry := map[string]any{
			"armed":         tl.Armed,
			"dead":          tl.Dead,
			"probeFailures": tl.ProbeFailures,
		}
		if !tl.Armed && !tl.UnarmedSince.IsZero() {
			entry["unarmedSeconds"] = int64(now.Sub(tl.UnarmedSince).Seconds())
		}
		metrics["taxonomyLiveness"] = entry
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

	stallCycles := h.CapabilitySweepStallCycles
	if stallCycles == 0 {
		stallCycles = defaultCapabilitySweepStallCycles
	}

	snaps := h.CapabilityLensProvider()
	metric := make(map[string]map[string]any, len(snaps))
	var paused, lagging, diverging, unrepaired, stalled, unreadable, blocked, unverified, recovered []string
	divergenceSeverity := ""
	repairSeverity := ""
	stallSeverity := ""
	blockedSeverity := ""
	unverifiedSeverity := ""
	seen := make(map[string]struct{}, len(snaps))
	for _, s := range snaps {
		name := capLensName(s)
		seen[name] = struct{}{}
		alert := "ok"
		if s.Unreadable != "" {
			// Only the reporter-derived inputs are untrusted (status, pause
			// reason, consumer lag), so the status/lag thresholds are skipped
			// rather than applied to a zero — a lens whose lag is unknown must
			// not read as a lens with lag 0. The sweep verdicts below come from
			// the in-process sweeper, which is unaffected, so they still count.
			alert = "unreadable"
			unreadable = append(unreadable, fmt.Sprintf("%s (%s)", name, s.Unreadable))
			h.resetLagState(name)
		} else {
			switch s.Status {
			case StatusPaused:
				alert = "paused"
				paused = append(paused, pausedLabel(name, s.PauseReason, s.LastError))
				// A paused lens is a hard error; its lag debounce is irrelevant and
				// must not carry a stale streak into the next active cycle.
				h.resetLagState(name)
			case StatusActive:
				if h.evalLagHysteresis(name, s.ConsumerLag, threshold, clearThreshold, int(raiseCycles)) {
					alert = "lagging"
					lagging = append(lagging, fmt.Sprintf("%s (lag %d)", name, s.ConsumerLag))
				}
			default:
				// rebuilding (or any non-active, non-paused state): not lagging; clear
				// any pending streak.
				h.resetLagState(name)
			}
		}
		// Placed outside the unreadable branch above, so it is evaluated on
		// every cycle: the recovery reaches this snapshot from a status read
		// that SUCCEEDED (the provider transfers it before the pending read that
		// can fail on its own), and the cycle a reader most needs to be told
		// that an authorization lens healed itself is the one where something
		// else about it could not be observed.
		alert = raiseAlert(alert, h.evalStructuralAutoRecovery(name, now, s.structuralRecovery(), &recovered))
		// The sweep's coverage verdict is independent of the consumer's own
		// state: a lens can be active and caught-up while its projection has a
		// hole, which is exactly the class the sweep exists to detect.
		if s.SweepDivergentStreak > 0 {
			diverging = append(diverging, fmt.Sprintf("%s (%d consecutive sweeps, %d healed)",
				name, s.SweepDivergentStreak, s.SweepReconciled))
			if s.SweepDivergentStreak >= capabilityDivergenceErrorStreak {
				divergenceSeverity = "error"
			} else if divergenceSeverity == "" {
				divergenceSeverity = "warning"
			}
		}
		// A repair the sweep could not write is the one verdict every other
		// signal misses: the consumer is caught up, nothing was healed, so the
		// divergent streak reads zero — and the row is still wrong.
		if s.SweepFailedStreak >= capabilityRepairWarnStreak {
			var detail string
			if s.SweepFailingActors > 0 {
				detail = fmt.Sprintf("%s (%d actor(s) unrepaired over %d consecutive passes",
					name, s.SweepFailingActors, s.SweepFailedStreak)
			} else {
				// A pass-level fault — an unreadable survey, or a tick
				// abandoned before it verified anything — names no actor.
				detail = fmt.Sprintf("%s (%d consecutive passes verified nothing",
					name, s.SweepFailedStreak)
			}
			if s.SweepLastFailure != "" {
				detail += ": " + s.SweepLastFailure
			}
			unrepaired = append(unrepaired, detail+")")
			if s.SweepFailedStreak >= capabilityRepairErrorStreak {
				repairSeverity = "error"
			} else if repairSeverity == "" {
				repairSeverity = "warning"
			}
			alert = raiseAlert(alert, "repair-failing")
		}
		// A divergence the ordering guard refused to let the sweep repair. It
		// is the one verdict that reports itself as a SUCCESS on every other
		// signal: the write returned nil, so nothing errored, the heal counter
		// ticked, and the row never changed.
		if s.SweepBlockedStreak > 0 {
			detail := fmt.Sprintf("%s (%d row(s) unrepairable over %d consecutive passes",
				name, s.SweepBlocked, s.SweepBlockedStreak)
			if s.SweepLastBlocked != "" {
				detail += ": " + s.SweepLastBlocked
			}
			blocked = append(blocked, detail+")")
			if s.SweepBlockedStreak >= capabilityDivergenceErrorStreak {
				blockedSeverity = "error"
			} else if blockedSeverity == "" {
				blockedSeverity = "warning"
			}
			alert = raiseAlert(alert, "repair-blocked")
		}
		// An anchor the sweep examined and could conclude nothing about.
		if s.SweepUnverifiedStreak > 0 {
			detail := fmt.Sprintf("%s (%d anchor(s) unverified over %d consecutive passes",
				name, s.SweepUnverified, s.SweepUnverifiedStreak)
			if s.SweepLastUnverified != "" {
				detail += ": " + s.SweepLastUnverified
			}
			unverified = append(unverified, detail+")")
			if s.SweepUnverifiedStreak >= capabilityDivergenceErrorStreak {
				unverifiedSeverity = "error"
			} else if unverifiedSeverity == "" {
				unverifiedSeverity = "warning"
			}
			alert = raiseAlert(alert, "unverified")
		}
		// Every verdict above describes the last pass the sweep completed, so a
		// sweep that has stopped passing keeps republishing them — the detector's
		// own liveness is the one thing none of them can report. A paused lens is
		// excluded: its suppression is deliberate, indefinite by design, and
		// already an error in its own right, so stalling on it is not news. The
		// exclusion also re-baselines the clock, so resuming a long pause does not
		// start the lens off already stalled for the length of the pause.
		stallAfter := time.Duration(stallCycles) * s.SweepInterval
		stalledFor, isStalled := time.Duration(0), false
		switch {
		case s.SweepInterval <= 0 || stallAfter <= 0:
			// No sweeper installed (or a window so large it overflowed): there is
			// no cadence to be late against.
		case s.Status == StatusPaused:
			h.rebaseSweepClock(name, now)
		default:
			stalledFor, isStalled = h.evalSweepStall(name, now, s.SweepLastPassAt, stallAfter)
		}
		if isStalled {
			detail := fmt.Sprintf("%s (no verdict for %s", name, stalledFor.Round(time.Second))
			// A reason recorded more than a couple of intervals ago does not
			// describe the current tick — the sweep is wedged, not suppressed.
			explained := s.SweepSuppression != "" &&
				now.Sub(s.SweepSuppressionAt) <= time.Duration(capabilitySweepSuppressionFreshnessCycles)*s.SweepInterval
			switch {
			case !explained:
				detail += ", no suppression recorded — the sweep is not ticking"
				stallSeverity = "error"
			case s.Status == StatusRebuilding:
				detail += ", suppressed: " + s.SweepSuppression
				// How long a rebuild may legitimately run is the rebuild's own
				// signal to own, and now it owns one. A rebuild still draining
				// stays exempt however long it takes; one that has not drained a
				// message in the same window that would have escalated the sweep
				// is not a long rebuild, it is a stuck one, and the sweep it
				// suppresses is not going to resume on its own.
				if wedgedFor, wedged := evalRebuildWedged(s, now, stallAfter); wedged {
					detail += fmt.Sprintf(", rebuild has not drained for %s (%d outstanding)",
						wedgedFor.Round(time.Second), s.RebuildOutstanding)
					stallSeverity = "error"
				} else if stallSeverity == "" {
					stallSeverity = "warning"
				}
			case stalledFor > time.Duration(capabilitySweepStallErrorMultiplier)*stallAfter:
				detail += ", suppressed: " + s.SweepSuppression
				stallSeverity = "error"
			default:
				detail += ", suppressed: " + s.SweepSuppression
				if stallSeverity == "" {
					stallSeverity = "warning"
				}
			}
			stalled = append(stalled, detail+")")
			alert = raiseAlert(alert, "sweep-stalled")
		}
		lastPassAt := ""
		if !s.SweepLastPassAt.IsZero() {
			lastPassAt = substrate.FormatTimestamp(s.SweepLastPassAt)
		}
		entryMetric := map[string]any{
			"status":           s.Status,
			"alert":            alert,
			"reconciled":       s.SweepReconciled,
			"failingActors":    s.SweepFailingActors,
			"unverified":       s.SweepUnverified,
			"blocked":          s.SweepBlocked,
			"sweepLastPassAt":  lastPassAt,
			"sweepSuppression": s.SweepSuppression,
		}
		if s.SweepLastUnverified != "" {
			entryMetric["unverifiedReason"] = s.SweepLastUnverified
		}
		if s.SweepLastBlocked != "" {
			entryMetric["blockedReason"] = s.SweepLastBlocked
		}
		if s.Status == StatusRebuilding && !s.RebuildProgressAt.IsZero() {
			// Only while one is running: a finished rebuild's last count is not a
			// fact about the lens now, and publishing it would read as a rebuild
			// permanently stuck at whatever it ended on.
			entryMetric["rebuildOutstanding"] = s.RebuildOutstanding
			entryMetric["rebuildProgressAt"] = substrate.FormatTimestamp(s.RebuildProgressAt)
		}
		if s.Unreadable != "" {
			// An explicit null, not a zero: "we could not read the lag" and "the
			// lag is 0" are opposite facts and must not render identically.
			entryMetric["consumerLag"] = nil
			entryMetric["unreadable"] = s.Unreadable
		} else {
			entryMetric["consumerLag"] = s.ConsumerLag
		}
		// The divergence audit is PUBLISHED here and evaluated nowhere: no
		// auth-plane lens can enrol (every one is actor-aggregate, which the
		// audit refuses to the sweep, or targets a store that cannot read a row
		// back), so an issue code on this path would be one nothing can raise.
		// The refusal itself still has to be readable — the GrantTable producers
		// and the Protected secure lens must show as REFUSED rather than absent.
		addAuditMetrics(entryMetric, s.audit())
		metric[name] = entryMetric
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
	if len(recovered) > 0 {
		// `warning`, not `error` — the one place this path's severity ladder is
		// deliberately NOT mirrored, so do not "fix" it upward. Every other
		// capability code escalates to `error` (⇒ instance `unhealthy`) because
		// it describes an authorization read model that is frozen, stale or
		// wrong RIGHT NOW. This one describes a lens that SUCCESSFULLY
		// recovered: raising `error` would take the whole instance unhealthy for
		// a self-heal that worked, which is a false alarm, and a signal that
		// cries wolf on the working path is one operators learn to skip — the
		// silence this issue exists to break, restored by another route.
		// `warning` ⇒ degraded, for at most two cycles, is the honest shape:
		// something was dark, it is back, and a rebuild may still be owed.
		active[issueCapabilityLensStructuralPauseAutoRecovered] = capIssue{
			severity: "warning",
			message: "capability lens cleared a structural pause under its own probe, with no operator involved. " +
				"The pause's own backlog replays on resume (the failing message was never acked), so a schema fix " +
				"that left the rows intact owes nothing. But if the condition was cleared by RE-PROVISIONING or " +
				"RESTORING the grant table, grants written before the pause are gone from it and never redeliver: " +
				"the read model stays under-granted — reads fail closed, not open — until a rebuild: " +
				strings.Join(recovered, ", "),
		}
	}
	if len(diverging) > 0 {
		active[issueCapabilityCoverageDivergence] = capIssue{
			severity: divergenceSeverity,
			message: "capability projection diverged from the graph and was healed by the convergence sweep; " +
				"a nonzero rate means events are being lost, not just repaired: " + strings.Join(diverging, ", "),
		}
	}
	if len(unrepaired) > 0 {
		active[issueCapabilityRepairFailing] = capIssue{
			severity: repairSeverity,
			message: "the convergence sweep could not repair a divergent capability projection; the row stays wrong " +
				"and no other signal reports it, because a failed repair heals nothing and so clears the divergence " +
				"streak: " + strings.Join(unrepaired, ", "),
		}
	}
	if len(blocked) > 0 {
		active[issueCapabilityRepairBlocked] = capIssue{
			severity: blockedSeverity,
			message: "the convergence sweep found a divergent capability projection and the ordering guard declined its repair; " +
				"the write returned success having changed nothing, so the row stays wrong while every other signal " +
				"reports a heal: " + strings.Join(blocked, ", "),
		}
	}
	if len(unverified) > 0 {
		active[issueCapabilityAuditUnverified] = capIssue{
			severity: unverifiedSeverity,
			message: "the convergence sweep examined a capability anchor and could reach no verdict on it; an anchor whose " +
				"divergence has no repair transport writes nothing, which every count below this one reads as " +
				"convergence: " + strings.Join(unverified, ", "),
		}
	}
	if len(stalled) > 0 {
		active[issueCapabilitySweepStalled] = capIssue{
			severity: stallSeverity,
			message: "the auth-plane convergence sweep has reached no verdict within its staleness window; every " +
				"other capability signal reports the last pass that ran, so an indefinitely suppressed sweep " +
				"publishes a converged verdict over a projection nothing is checking: " + strings.Join(stalled, ", "),
		}
	}
	if len(unreadable) > 0 {
		active[issueCapabilityLensUnreadable] = capIssue{
			severity: "warning",
			message: "a capability lens's liveness inputs could not be read this cycle, so its pause state and " +
				"consumer lag are unknown rather than healthy (the projection itself may be fine — what failed " +
				"is the observation path): " + strings.Join(unreadable, ", "),
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

// sweepClockBase returns the lens's staleness baseline, stamping it at now if it
// has none yet. The baseline is the floor under the staleness clock: a lens that
// has never swept, or one whose last pass predates the baseline, is measured from
// here rather than from the instance's start — a lens installed (or resumed)
// mid-run has legitimately not swept yet, and its first pass is one interval away
// from ITS activation, not from boot.
func (h *LatticeHeartbeater) sweepClockBase(name string, now time.Time) time.Time {
	h.lagMu.Lock()
	defer h.lagMu.Unlock()
	if h.sweepBase == nil {
		h.sweepBase = map[string]time.Time{}
	}
	if base, ok := h.sweepBase[name]; ok {
		return base
	}
	h.sweepBase[name] = now
	return now
}

// rebaseSweepClock moves a lens's staleness baseline forward — used while the
// lens is exempt from the staleness verdict, so the exempt period is not counted
// against it once the exemption lifts.
func (h *LatticeHeartbeater) rebaseSweepClock(name string, now time.Time) {
	h.lagMu.Lock()
	defer h.lagMu.Unlock()
	if h.sweepBase == nil {
		h.sweepBase = map[string]time.Time{}
	}
	h.sweepBase[name] = now
}

// evalSweepStall reports how long the sweep has gone without a verdict and
// whether that exceeds stallAfter, measured from the later of the lens's last
// verdict and its staleness baseline.
func (h *LatticeHeartbeater) evalSweepStall(name string, now, lastPassAt time.Time, stallAfter time.Duration) (time.Duration, bool) {
	since := h.sweepClockBase(name, now)
	if lastPassAt.After(since) {
		since = lastPassAt
	}
	elapsed := now.Sub(since)
	return elapsed, elapsed > stallAfter
}

// resetLagState clears one lens's lag-debounce state — used when a lens leaves
// the active state (paused/rebuilding), where lag is not a meaningful signal.
func (h *LatticeHeartbeater) resetLagState(name string) {
	h.lagMu.Lock()
	defer h.lagMu.Unlock()
	delete(h.lagState, name)
}

// pruneLagState drops per-lens cap-path state — lag debounce and sweep first-seen
// — for lenses no longer reported this cycle, keeping both maps bounded to live
// lenses (mirrors reconcileCapIssues).
func (h *LatticeHeartbeater) pruneLagState(seen map[string]struct{}) {
	h.lagMu.Lock()
	defer h.lagMu.Unlock()
	for name := range h.lagState {
		if _, ok := seen[name]; !ok {
			delete(h.lagState, name)
		}
	}
	for name := range h.sweepBase {
		if _, ok := seen[name]; !ok {
			delete(h.sweepBase, name)
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
	var paused, lagging, unreadable, blocked, unverified []string
	var diverging, unrepaired, stalled, redacting, recovered []string
	var diverged, auditStalled []string
	seen := make(map[string]struct{}, len(snaps))
	for _, s := range snaps {
		name := lensName(s)
		seen[name] = struct{}{}
		alert := "ok"
		// The sweep's verdicts are read before the reporter-derived ones and
		// survive an unreadable entry, the same order the cap path takes: a
		// repair that is failing right now is a fact about the projection, and
		// losing it to a fault in observing something else would leave the lens
		// looking merely unobserved.
		sweepAlert := h.evalLensSweep(name, now, s, &diverging, &unrepaired, &stalled, &blocked, &unverified)
		// The divergence audit's verdicts are read here for the same reason and
		// on the same terms: they come from the in-process auditor, not from the
		// health entry, so they stay valid across an unreadable one. A row the
		// audit has PROVEN wrong must not be lost to a fault observing something
		// else — the audit writes nothing, so nothing else will find it again.
		sweepAlert = raiseAlert(sweepAlert, h.evalLensAudit(name, now, s, &diverged, &auditStalled))
		if s.Unreadable != "" {
			// The reporter-derived inputs (status, pause reason, projection lag)
			// are the untrusted ones, so their thresholds are skipped rather
			// than applied to a zero — a lens whose lag is unknown must not read
			// as a lens with lag 0. The streak resets with them, exactly as the
			// auth-plane path does: an unreadable cycle is not evidence either
			// way, and carrying a partial streak across it would let a lag alert
			// raise on cycles that never measured anything.
			alert = "unreadable"
			unreadable = append(unreadable, fmt.Sprintf("%s (%s)", name, s.Unreadable))
			h.resetLensLagState(name)
			// The redaction count survives an unreadable cycle for the same
			// reason the sweep verdicts do: the reporter-derived inputs are the
			// untrusted ones, and this count comes from a status read that
			// SUCCEEDED (the provider carries it forward past a failed pending
			// read). A known count of nulls being served is not made unknown by
			// a fault observing something else.
			alert = raiseAlert(alert, evalSecureRedaction(name, s, &redacting))
			// The recovery stamp reaches this snapshot from the same successful
			// status read the redaction count does, so it survives an unreadable
			// cycle for the same reason: a lens that healed itself is a fact in
			// hand, and the one cycle a reader most needs to be told is the one
			// where something else about the lens could not be observed.
			alert = raiseAlert(alert, h.evalStructuralAutoRecovery(name, now, s.structuralRecovery(), &recovered))
			m := map[string]any{
				"status":          s.Status,
				"projectionLag":   nil,
				"lastProjectedAt": "",
				"alert":           alert,
				"unreadable":      s.Unreadable,
			}
			if s.SecureRedactions > 0 {
				m["secureRedactions"] = s.SecureRedactions
			}
			addLensSweepMetrics(m, s)
			addAuditMetrics(m, s.audit())
			metric[name] = m
			continue
		}
		switch s.Status {
		case StatusPaused:
			alert = "paused"
			paused = append(paused, pausedLabel(name, s.PauseReason, s.LastError))
			h.resetLensLagState(name)
		case StatusActive:
			if h.evalLensLagHysteresis(name, s.ProjectionLag, threshold, clearThreshold, int(raiseCycles)) {
				alert = "lagging"
				lagging = append(lagging, fmt.Sprintf("%s (lag %d)", name, s.ProjectionLag))
			}
		default:
			// rebuilding (or any non-active, non-paused state): not lagging; clear
			// any pending streak.
			h.resetLensLagState(name)
		}
		alert = raiseAlert(alert, sweepAlert)
		lastProjectedAt := ""
		if !s.LastProjectedAt.IsZero() {
			lastProjectedAt = substrate.FormatTimestamp(s.LastProjectedAt)
		}
		alert = raiseAlert(alert, evalSecureRedaction(name, s, &redacting))
		alert = raiseAlert(alert, h.evalStructuralAutoRecovery(name, now, s.structuralRecovery(), &recovered))
		m := map[string]any{
			"status":          s.Status,
			"projectionLag":   s.ProjectionLag,
			"lastProjectedAt": lastProjectedAt,
			"alert":           alert,
		}
		if s.SecureRedactions > 0 {
			m["secureRedactions"] = s.SecureRedactions
		}
		addLensSweepMetrics(m, s)
		addAuditMetrics(m, s.audit())
		metric[name] = m
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
	if len(redacting) > 0 {
		active[issueLensSecureRedaction] = capIssue{
			severity: "error",
			message: "lens projected a secure column as null because it could not resolve the value; the row renders " +
				"as if the record were erased, and only this counter distinguishes that from a real erasure: " +
				strings.Join(redacting, ", "),
		}
	}
	if len(recovered) > 0 {
		active[issueLensStructuralPauseAutoRecovered] = capIssue{
			severity: "warning",
			message: "lens cleared a structural pause under its own probe, with no operator involved. The pause's own " +
				"backlog replays on resume (the failing message was never acked), so a schema fix that left the rows " +
				"intact owes nothing. But if the condition was cleared by RE-PROVISIONING or RESTORING the target, " +
				"rows written before the pause are gone from it and never redeliver — a rebuild is owed, and its " +
				"scope is the whole lens, not the outage window: " + strings.Join(recovered, ", "),
		}
	}
	if len(unreadable) > 0 {
		active[issueLensProjectionUnreadable] = capIssue{
			severity: "warning",
			message: "lens liveness inputs could not be read, so this lens's read model is unobserved rather than known-healthy: " +
				strings.Join(unreadable, ", "),
		}
	}
	if len(diverging) > 0 {
		active[issueLensCoverageDivergence] = capIssue{
			severity: "warning",
			message: "convergence sweep healed a graph ↔ read-model divergence; the lens's consumer is caught up but MISSED events: " +
				strings.Join(diverging, ", "),
		}
	}
	if len(unrepaired) > 0 {
		active[issueLensRepairFailing] = capIssue{
			severity: "warning",
			message: "convergence sweep could not write its repair, so the read model is still wrong and no other signal says so: " +
				strings.Join(unrepaired, ", "),
		}
	}
	if len(blocked) > 0 {
		active[issueLensRepairBlocked] = capIssue{
			severity: "warning",
			message: "convergence sweep found a divergent row and the ordering guard declined its repair, so the write " +
				"reported success having changed nothing: " + strings.Join(blocked, ", "),
		}
	}
	if len(unverified) > 0 {
		active[issueLensAuditUnverified] = capIssue{
			severity: "warning",
			message: "convergence sweep reached no verdict on a lens anchor, so its correctness is unknown rather than " +
				"confirmed: " + strings.Join(unverified, ", "),
		}
	}
	if len(stalled) > 0 {
		active[issueLensSweepStalled] = capIssue{
			severity: "warning",
			message: "convergence sweep has reached no verdict for longer than its staleness window, so every verdict above describes a pass that is no longer happening: " +
				strings.Join(stalled, ", "),
		}
	}
	if len(diverged) > 0 {
		active[issueLensProjectionDiverged] = capIssue{
			severity: "warning",
			message: "the divergence audit recomputed a lens's rows from the graph and the read model disagrees. NOTHING has been " +
				"repaired: the audit detects and never writes, so the row stays wrong until an operator reprojects or rebuilds the " +
				"lens. Read divergentTotal against auditCycleCompletedAt — a pass covers one batch, so a clean sibling number is a " +
				"claim about those anchors, not about the lens: " + strings.Join(diverged, ", "),
		}
	}
	if len(auditStalled) > 0 {
		active[issueLensAuditStalled] = capIssue{
			severity: "warning",
			message: "the divergence audit has reached no verdict for longer than its staleness window, so every audit field above " +
				"describes a pass that is no longer happening — including a clean one: " + strings.Join(auditStalled, ", "),
		}
	}
	return metric, h.reconcileLensIssues(active, now)
}

// addLensSweepMetrics publishes the sweep's own state alongside the lens's
// liveness, mirroring the capability path's fields.
//
// `sweepEnrolled` is the one field the cap path has no need of: enrolment there
// is universal, while here the install gate declines any lens that cannot scope
// a listing to its own rows. That decision is otherwise a line in the
// activation log, and a lens quietly running without its only stale-row
// detector reads exactly like a lens whose sweep keeps finding nothing.
// evalSecureRedaction returns the alert token for a lens that has projected one
// or more secure columns as null because it could not resolve them, appending
// its label to the report list.
//
// Raised on the CUMULATIVE count, not a per-cycle delta: a redaction is not a
// transient the next cycle clears — the null is written into the read model and
// stays there until whatever made it unresolvable is fixed and the lens
// reprojects. A delta-based signal would go quiet while the wrong rows were
// still being served.
func evalSecureRedaction(name string, s LensLivenessStatus, redacting *[]string) string {
	if s.SecureRedactions == 0 {
		return ""
	}
	*redacting = append(*redacting, fmt.Sprintf("%s (%d)", name, s.SecureRedactions))
	return "secure-redaction"
}

// structuralAutoRecoveryWindow is how long after a probe-driven structural
// recovery the issue stays raised — scaled off the heartbeat's own cadence
// rather than being a second independently-tuned duration, so a deployment that
// beats slowly still gets its emissions. Floored at minHeartbeatInterval: a zero
// window raises nothing, which on this signal is indistinguishable from the
// recovery never having happened.
func (h *LatticeHeartbeater) structuralAutoRecoveryWindow() time.Duration {
	interval := h.interval
	if interval < minHeartbeatInterval {
		interval = minHeartbeatInterval
	}
	return structuralAutoRecoveredFreshnessCycles * interval
}

// structuralRecovery is one lens's last probe-driven structural recovery, in the
// shape both evaluators read it. It exists so the freshness window and the label
// are written exactly once: the business and auth-plane paths are deliberately
// separate evaluators, but "did this lens heal itself, and how recently" has one
// right answer, and two copies of a time comparison is how the two planes come
// to disagree about what "recently" means.
type structuralRecovery struct {
	at       time.Time
	cause    string
	attempts int
}

func (s LensLivenessStatus) structuralRecovery() structuralRecovery {
	return structuralRecovery{
		at:       s.StructuralAutoRecoveredAt,
		cause:    s.StructuralAutoRecoveredCause,
		attempts: s.StructuralAutoRecoveryAttempts,
	}
}

func (s CapabilityLensStatus) structuralRecovery() structuralRecovery {
	return structuralRecovery{
		at:       s.StructuralAutoRecoveredAt,
		cause:    s.StructuralAutoRecoveredCause,
		attempts: s.StructuralAutoRecoveryAttempts,
	}
}

// evalStructuralAutoRecovery returns the alert token for a lens that has
// recovered from a structural pause under its own probe within the freshness
// window, appending its label to the report list.
//
// Raised on the AGE of the recovery, not on its presence: the stamp persists on
// the health entry for the life of the lens (it is the record of what was wrong
// and whether a rebuild was ever owed), while the issue is an announcement of
// something that just happened. Left unbounded it would sit open forever on a
// lens that is fine, and an issue that never clears is one nobody reads.
func (h *LatticeHeartbeater) evalStructuralAutoRecovery(name string, now time.Time, rec structuralRecovery, recovered *[]string) string {
	if rec.at.IsZero() {
		return ""
	}
	if now.Sub(rec.at) >= h.structuralAutoRecoveryWindow() {
		return ""
	}
	*recovered = append(*recovered, structuralRecoveryLabel(name, rec))
	return "structural-pause-auto-recovered"
}

// structuralRecoveryLabel renders one self-healed lens for the issue message:
// the lens name, which self-heal attempt lifted the pause, and the cause it
// recovered from — truncated on the same cap as pausedLabel, since it is the
// same operator-facing diagnosis and several lenses can heal in one cycle. The
// attempt count is what separates a clean one-shot recovery from a lens
// flapping toward its relapse latch, and the cause is the only surviving
// statement of what the read model missed while it was dark.
func structuralRecoveryLabel(name string, rec structuralRecovery) string {
	if rec.cause == "" {
		return fmt.Sprintf("%s (attempt %d)", name, rec.attempts)
	}
	const causeCap = 160
	cause := strings.TrimSpace(rec.cause)
	if len(cause) > causeCap {
		cause = cause[:causeCap] + "..."
	}
	return fmt.Sprintf("%s (attempt %d, recovered from: %s)", name, rec.attempts, cause)
}

func addLensSweepMetrics(m map[string]any, s LensLivenessStatus) {
	enrolled := s.SweepInterval > 0
	m["sweepEnrolled"] = enrolled
	if !enrolled {
		return
	}
	lastPassAt := ""
	if !s.SweepLastPassAt.IsZero() {
		lastPassAt = substrate.FormatTimestamp(s.SweepLastPassAt)
	}
	m["reconciled"] = s.SweepReconciled
	m["failingActors"] = s.SweepFailingActors
	m["sweepLastPassAt"] = lastPassAt
	m["sweepSuppression"] = s.SweepSuppression
	// Always published, including at zero: "no unverified anchors" is a
	// verdict, and an absent field is indistinguishable from a Refractor that
	// does not compute one — which is exactly the ambiguity this whole change
	// exists to remove. The reasons are omitted when empty, per the
	// suppression field's precedent.
	m["unverified"] = s.SweepUnverified
	m["blocked"] = s.SweepBlocked
	if s.SweepLastUnverified != "" {
		m["unverifiedReason"] = s.SweepLastUnverified
	}
	if s.SweepLastBlocked != "" {
		m["blockedReason"] = s.SweepLastBlocked
	}
}

// evalLensSweep collects one business lens's convergence-sweep verdicts into the
// caller's detail lists and returns the alert this lens's sweep argues for
// ("repair-failing", "repair-blocked", "unverified", "sweep-stalled", or "" for
// none) — the caller merges it against the consumer-derived alerts through
// raiseAlert.
//
// The sibling of the cap path's inline sweep block, and deliberately not a
// shared one: it raises different issue codes, keeps its staleness clock in its
// own map (the cap path prunes that map against the auth-plane lens set, so
// sharing it would have each path evicting the other's baselines every cycle),
// and — the substantive difference — never escalates past `warning`. A wrong
// business read model is one vertical's outage; failing the whole Refractor
// instance for it would take down the auth plane with it.
func (h *LatticeHeartbeater) evalLensSweep(
	name string,
	now time.Time,
	s LensLivenessStatus,
	diverging, unrepaired, stalled, blocked, unverified *[]string,
) string {
	alert := ""
	if s.SweepDivergentStreak > 0 {
		*diverging = append(*diverging, fmt.Sprintf("%s (%d consecutive sweeps, %d healed)",
			name, s.SweepDivergentStreak, s.SweepReconciled))
	}
	if s.SweepFailedStreak >= capabilityRepairWarnStreak {
		var detail string
		if s.SweepFailingActors > 0 {
			detail = fmt.Sprintf("%s (%d actor(s) unrepaired over %d consecutive passes",
				name, s.SweepFailingActors, s.SweepFailedStreak)
		} else {
			// A pass-level fault — an unreadable survey, or a tick abandoned
			// before it verified anything — names no actor.
			detail = fmt.Sprintf("%s (%d consecutive passes verified nothing",
				name, s.SweepFailedStreak)
		}
		if s.SweepLastFailure != "" {
			detail += ": " + s.SweepLastFailure
		}
		*unrepaired = append(*unrepaired, detail+")")
		alert = raiseAlert(alert, "repair-failing")
	}
	if s.SweepBlockedStreak > 0 {
		detail := fmt.Sprintf("%s (%d row(s) unrepairable over %d consecutive passes",
			name, s.SweepBlocked, s.SweepBlockedStreak)
		if s.SweepLastBlocked != "" {
			detail += ": " + s.SweepLastBlocked
		}
		*blocked = append(*blocked, detail+")")
		alert = raiseAlert(alert, "repair-blocked")
	}
	if s.SweepUnverifiedStreak > 0 {
		detail := fmt.Sprintf("%s (%d anchor(s) unverified over %d consecutive passes",
			name, s.SweepUnverified, s.SweepUnverifiedStreak)
		if s.SweepLastUnverified != "" {
			detail += ": " + s.SweepLastUnverified
		}
		*unverified = append(*unverified, detail+")")
		alert = raiseAlert(alert, "unverified")
	}

	stallCycles := h.CapabilitySweepStallCycles
	if stallCycles == 0 {
		stallCycles = defaultCapabilitySweepStallCycles
	}
	stallAfter := time.Duration(stallCycles) * s.SweepInterval
	switch {
	case s.SweepInterval <= 0 || stallAfter <= 0:
		// No sweeper installed (a lens whose key pattern cannot scope a
		// listing), or a window so large it overflowed: no cadence to be late
		// against.
		return alert
	case s.Status == StatusPaused:
		// Suppression while paused is deliberate and indefinite, and the pause
		// is already an issue of its own. Rebasing also stops a resumed lens
		// starting out stalled for the length of its pause.
		h.rebaseLensSweepClock(name, now)
		return alert
	}
	stalledFor, isStalled := h.evalLensSweepStall(name, now, s.SweepLastPassAt, stallAfter)
	if !isStalled {
		return alert
	}
	detail := fmt.Sprintf("%s (no verdict for %s", name, stalledFor.Round(time.Second))
	// A reason recorded more than a couple of intervals ago describes an
	// earlier tick — the sweep is wedged, not suppressed.
	explained := s.SweepSuppression != "" &&
		now.Sub(s.SweepSuppressionAt) <= time.Duration(capabilitySweepSuppressionFreshnessCycles)*s.SweepInterval
	if explained {
		detail += ", suppressed: " + s.SweepSuppression
	} else {
		detail += ", no suppression recorded — the sweep is not ticking"
	}
	*stalled = append(*stalled, detail+")")
	return raiseAlert(alert, "sweep-stalled")
}

// auditSnapshot is one lens's divergence-audit state, lifted off whichever
// status struct carries it so the publication and evaluation below are written
// once rather than per plane. It mirrors structuralRecovery's accessor pattern
// for the same reason: the two status structs are deliberately separate, and the
// logic reading them must not be.
type auditSnapshot struct {
	enrolled         bool
	refusal          string
	audited          int
	divergent        map[string]int
	divergentTotal   int
	unverified       int
	lastUnverified   string
	lastPassAt       time.Time
	cycleCompletedAt time.Time
	coverageBasis    string
	listingSize      int
	suppression      string
	suppressionAt    time.Time
	interval         time.Duration
}

func (s LensLivenessStatus) audit() auditSnapshot {
	return auditSnapshot{
		enrolled: s.AuditEnrolled, refusal: s.AuditRefusal,
		audited: s.Audited, divergent: s.DivergentRows, divergentTotal: s.DivergentTotal,
		unverified: s.AuditUnverified, lastUnverified: s.AuditLastUnverified,
		lastPassAt: s.AuditLastPassAt, cycleCompletedAt: s.AuditCycleCompletedAt,
		coverageBasis: s.AuditCoverageBasis, listingSize: s.AuditListingSize,
		suppression: s.AuditSuppression, suppressionAt: s.AuditSuppressionAt,
		interval: s.AuditInterval,
	}
}

func (s CapabilityLensStatus) audit() auditSnapshot {
	return auditSnapshot{
		enrolled: s.AuditEnrolled, refusal: s.AuditRefusal,
		audited: s.Audited, divergent: s.DivergentRows, divergentTotal: s.DivergentTotal,
		unverified: s.AuditUnverified, lastUnverified: s.AuditLastUnverified,
		lastPassAt: s.AuditLastPassAt, cycleCompletedAt: s.AuditCycleCompletedAt,
		coverageBasis: s.AuditCoverageBasis, listingSize: s.AuditListingSize,
		suppression: s.AuditSuppression, suppressionAt: s.AuditSuppressionAt,
		interval: s.AuditInterval,
	}
}

// addAuditMetrics publishes the divergence audit's own state alongside the
// lens's liveness.
//
// `auditEnrolled` is published for EVERY lens, enrolled or not, and the refusal
// beside it. That is the field the whole enrolment gate exists to produce: a
// lens quietly running without its only per-row correctness detector reads
// exactly like a lens whose audit keeps finding nothing, and the audit's own
// coverage would then shrink silently as lens shapes evolve.
//
// The audit's unverified count is published as `auditUnverified`, NOT as the
// `unverified` the sweep already owns in this same map. They are two detectors'
// verdicts about different anchors, and one key for both would let either erase
// the other's finding.
func addAuditMetrics(m map[string]any, a auditSnapshot) {
	m["auditEnrolled"] = a.enrolled
	if !a.enrolled {
		if a.refusal != "" {
			m["auditRefusal"] = a.refusal
		}
		return
	}
	lastPassAt := ""
	if !a.lastPassAt.IsZero() {
		lastPassAt = substrate.FormatTimestamp(a.lastPassAt)
	}
	cycleCompletedAt := ""
	if !a.cycleCompletedAt.IsZero() {
		cycleCompletedAt = substrate.FormatTimestamp(a.cycleCompletedAt)
	}
	m["audited"] = a.audited
	// The map is published even when empty, and carries only the classes that
	// FIRED. An always-present zero per class would make a direction that has
	// silently stopped detecting indistinguishable from one with nothing to
	// find; the total beside it is the single number a first glance keys on.
	//
	// An empty verdict renders as `{}`, never as `null`: in this document a null
	// means "could not be read" (consumerLag says so explicitly), and a pass
	// that found nothing read everything it was asked to.
	divergent := a.divergent
	if divergent == nil {
		divergent = map[string]int{}
	}
	m["divergentRows"] = divergent
	m["divergentTotal"] = a.divergentTotal
	m["auditUnverified"] = a.unverified
	m["auditLastPassAt"] = lastPassAt
	m["auditCycleCompletedAt"] = cycleCompletedAt
	m["auditCoverageBasis"] = a.coverageBasis
	m["auditListingSize"] = a.listingSize
	m["auditSuppression"] = a.suppression
	if a.lastUnverified != "" {
		m["auditUnverifiedReason"] = a.lastUnverified
	}
}

// evalLensAudit collects one business lens's divergence-audit verdicts into the
// caller's detail lists and returns the alert this lens's audit argues for
// ("audit-stalled", "unverified", "diverged", or "" for none) — the caller merges
// it against the other alerts through raiseAlert.
//
// A REFUSED lens returns "" immediately and reaches none of the branches below.
// That is the enrolment gate's whole point carried through to the alert plane: a
// lens with no audit must never read as one whose audit ran, whether the reading
// is "clean" or "late".
//
// Severity is the caller's, and it is `warning` at every magnitude — never
// error. The audit runs only on business lenses, and a single business lens's
// read model, however wrong, degrades the instance rather than failing it
// (docs/components/refractor.md's standing rule). The escalation the sweep's
// AUTH-PLANE sibling carries is deliberately not inherited: the audit has no
// auth-plane lens to escalate for.
func (h *LatticeHeartbeater) evalLensAudit(
	name string,
	now time.Time,
	s LensLivenessStatus,
	diverged, auditStalled *[]string,
) string {
	a := s.audit()
	if !a.enrolled {
		return ""
	}
	alert := ""
	if a.divergentTotal > 0 {
		*diverged = append(*diverged, fmt.Sprintf("%s (%d divergent row(s) %s across %d audited anchor(s))",
			name, a.divergentTotal, divergenceClassSummary(a.divergent), a.audited))
		alert = raiseAlert(alert, "diverged")
	}
	if a.unverified > 0 {
		// The audit's unverified anchors raise the `unverified` ALERT token but
		// no issue of their own: the sweep owns LensAuditUnverified, and one code
		// written by two independent detectors would be two `since` clocks
		// disagreeing about when the condition began. The count and the governing
		// reason travel in the metrics map beside it.
		alert = raiseAlert(alert, "unverified")
	}

	stallCycles := h.CapabilitySweepStallCycles
	if stallCycles == 0 {
		stallCycles = defaultCapabilitySweepStallCycles
	}
	stallAfter := time.Duration(stallCycles) * a.interval
	switch {
	case a.interval <= 0 || stallAfter <= 0:
		// No cadence to be late against.
		return alert
	case s.Status == StatusPaused:
		// Suppression while paused is deliberate and indefinite, and the pause
		// is already an issue of its own. Rebasing also stops a resumed lens
		// starting out stalled for the length of its pause.
		h.rebaseLensAuditClock(name, now)
		return alert
	}
	stalledFor, isStalled := h.evalLensAuditStall(name, now, a.lastPassAt, stallAfter)
	if !isStalled {
		return alert
	}
	detail := fmt.Sprintf("%s (no verdict for %s", name, stalledFor.Round(time.Second))
	// A reason recorded more than a couple of intervals ago describes an earlier
	// tick — the audit is wedged, not suppressed.
	explained := a.suppression != "" &&
		now.Sub(a.suppressionAt) <= time.Duration(capabilitySweepSuppressionFreshnessCycles)*a.interval
	if explained {
		detail += ", suppressed: " + a.suppression
	} else {
		detail += ", no suppression recorded — the audit is not ticking"
	}
	*auditStalled = append(*auditStalled, detail+")")
	return raiseAlert(alert, "audit-stalled")
}

// divergenceClassSummary renders the per-class breakdown in a stable order, so
// the issue text names WHICH direction diverged rather than only how much. The
// order is fixed rather than map order for the reason governingReasonLocked
// sorts its own output: a message that reshuffles every cycle reads as a
// changing condition.
func divergenceClassSummary(classes map[string]int) string {
	if len(classes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(classes))
	for _, class := range []string{"missing", "stale", "retained"} {
		if n, ok := classes[class]; ok {
			parts = append(parts, fmt.Sprintf("%s=%d", class, n))
		}
	}
	// A class this list does not name is still reported rather than dropped —
	// an unrendered class would be a divergence counted into the total with
	// nothing saying what it was.
	var extra []string
	for class, n := range classes {
		switch class {
		case "missing", "stale", "retained":
		default:
			extra = append(extra, fmt.Sprintf("%s=%d", class, n))
		}
	}
	sort.Strings(extra)
	parts = append(parts, extra...)
	return "(" + strings.Join(parts, ", ") + ")"
}

// lensAuditClockBase / rebaseLensAuditClock / evalLensAuditStall are the
// divergence audit's staleness clock, the exact shape the sweep's own trio
// takes and over its own map: the two detectors tick on different cadences, so
// a shared baseline would have whichever ran last excuse the other's silence.
func (h *LatticeHeartbeater) lensAuditClockBase(name string, now time.Time) time.Time {
	h.lensLagMu.Lock()
	defer h.lensLagMu.Unlock()
	if h.lensAuditBase == nil {
		h.lensAuditBase = map[string]time.Time{}
	}
	if base, ok := h.lensAuditBase[name]; ok {
		return base
	}
	h.lensAuditBase[name] = now
	return now
}

func (h *LatticeHeartbeater) rebaseLensAuditClock(name string, now time.Time) {
	h.lensLagMu.Lock()
	defer h.lensLagMu.Unlock()
	if h.lensAuditBase == nil {
		h.lensAuditBase = map[string]time.Time{}
	}
	h.lensAuditBase[name] = now
}

func (h *LatticeHeartbeater) evalLensAuditStall(name string, now, lastPassAt time.Time, stallAfter time.Duration) (time.Duration, bool) {
	since := h.lensAuditClockBase(name, now)
	if lastPassAt.After(since) {
		since = lastPassAt
	}
	elapsed := now.Sub(since)
	return elapsed, elapsed > stallAfter
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

// pruneLensLagState drops per-lens business-path state — lag debounce and sweep
// staleness baseline — for lenses no longer reported this cycle (general-lens
// sibling of pruneLagState), keeping both maps bounded to live lenses.
func (h *LatticeHeartbeater) pruneLensLagState(seen map[string]struct{}) {
	h.lensLagMu.Lock()
	defer h.lensLagMu.Unlock()
	for name := range h.lensLagState {
		if _, ok := seen[name]; !ok {
			delete(h.lensLagState, name)
		}
	}
	for name := range h.lensSweepBase {
		if _, ok := seen[name]; !ok {
			delete(h.lensSweepBase, name)
		}
	}
	for name := range h.lensAuditBase {
		if _, ok := seen[name]; !ok {
			delete(h.lensAuditBase, name)
		}
	}
}

// lensSweepClockBase returns a business lens's staleness baseline, stamping it
// at now if it has none. It is the floor under the staleness clock: a lens
// installed or resumed mid-run has legitimately not swept yet, and its first
// pass is one interval from ITS activation, not from boot.
func (h *LatticeHeartbeater) lensSweepClockBase(name string, now time.Time) time.Time {
	h.lensLagMu.Lock()
	defer h.lensLagMu.Unlock()
	if h.lensSweepBase == nil {
		h.lensSweepBase = map[string]time.Time{}
	}
	if base, ok := h.lensSweepBase[name]; ok {
		return base
	}
	h.lensSweepBase[name] = now
	return now
}

// rebaseLensSweepClock moves a business lens's staleness baseline forward, so a
// period the lens is exempt from the verdict is not counted against it once the
// exemption lifts.
func (h *LatticeHeartbeater) rebaseLensSweepClock(name string, now time.Time) {
	h.lensLagMu.Lock()
	defer h.lensLagMu.Unlock()
	if h.lensSweepBase == nil {
		h.lensSweepBase = map[string]time.Time{}
	}
	h.lensSweepBase[name] = now
}

// evalLensSweepStall reports how long a business lens's sweep has gone without a
// verdict and whether that exceeds stallAfter, measured from the later of its
// last verdict and its staleness baseline.
func (h *LatticeHeartbeater) evalLensSweepStall(name string, now, lastPassAt time.Time, stallAfter time.Duration) (time.Duration, bool) {
	since := h.lensSweepClockBase(name, now)
	if lastPassAt.After(since) {
		since = lastPassAt
	}
	elapsed := now.Sub(since)
	return elapsed, elapsed > stallAfter
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

// pausedLabel renders one paused lens for a LensProjectionPaused issue message:
// the lens name, why it is paused, and — when one was recorded — the cause,
// truncated so a handful of paused lenses cannot push the issue past what a
// health entry should carry. A structural pause is held until a human
// reconciles it, so the cause is the operator's whole starting point; without
// it the message says only "structural", which names the tier and not the
// column, table or constraint the projection actually failed on.
func pausedLabel(name, reason, lastError string) string {
	if reason == "" {
		reason = "unknown"
	}
	if lastError == "" {
		return fmt.Sprintf("%s (%s)", name, reason)
	}
	const causeCap = 160
	cause := strings.TrimSpace(lastError)
	if len(cause) > causeCap {
		cause = cause[:causeCap] + "..."
	}
	return fmt.Sprintf("%s (%s: %s)", name, reason, cause)
}
