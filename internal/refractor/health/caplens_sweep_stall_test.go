// CapabilitySweepStalled + CapabilityLensUnreadable — the auth plane's two
// blind spots of last resort. Every other capability signal reports what the
// sweep's last pass FOUND, so a sweep that stops passing republishes that
// finding forever; and an auth-plane lens absent from the metric is
// indistinguishable from one that was never installed, so a lens whose liveness
// inputs cannot be read has to be reported as unknown rather than omitted.
package health

import (
	"strings"
	"testing"
	"time"
)

const stallTestInterval = time.Minute

// stallWindow is the staleness window the default cycle count yields for the
// test interval — the tests express ages relative to it, not to a literal.
var stallWindow = time.Duration(defaultCapabilitySweepStallCycles) * stallTestInterval

// sweepSnap is the lens as of beat time `at`, whose sweep last reached a verdict
// sweptAgo before that. A suppression reason is stamped at `at` — the sweep
// re-records it every tick it is held, and the heartbeat only trusts a fresh one.
func sweepSnap(name string, at time.Time, sweptAgo time.Duration, suppression string) CapabilityLensStatus {
	s := CapabilityLensStatus{
		CanonicalName:    name,
		RuleID:           "lnk-" + name,
		Status:           "active",
		SweepLastPassAt:  at.Add(-sweptAgo),
		SweepSuppression: suppression,
		SweepInterval:    stallTestInterval,
	}
	if suppression != "" {
		s.SweepSuppressionAt = at
	}
	return s
}

// neverSweptSnap is a lens whose sweeper has not yet reached a first verdict —
// the state of every lens for the first interval after it registers.
func neverSweptSnap(name string) CapabilityLensStatus {
	s := sweepSnap(name, time.Time{}, 0, "")
	s.SweepLastPassAt = time.Time{}
	return s
}

// sweptThenHeld drives the real temporal sequence every stall goes through: a
// first beat where the lens has just swept (which is what establishes the
// heartbeater's clock baseline — a sweeper is in-process, so a lens the
// heartbeater has only just seen cannot already have an old verdict), then a beat
// `held` later where the sweep has produced nothing since. Returns the second
// beat's metric and issues. `mutate` adjusts the held snapshot.
func sweptThenHeld(held time.Duration, suppression string, mutate func(*CapabilityLensStatus)) (map[string]map[string]any, []issueRecord) {
	start := time.Now()
	cur := sweepSnap("capability", start, 0, "")
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{cur}
	}}
	h.evalCapabilityLenses(start)

	at := start.Add(held)
	cur = sweepSnap("capability", at, held, suppression)
	if mutate != nil {
		mutate(&cur)
	}
	return h.evalCapabilityLenses(at)
}

func TestEvalCapabilityLenses_ASweepVerifyingNothingIsNotOk(t *testing.T) {
	// The shipped lie: the sweep is suppressed indefinitely (a rebuild that
	// never finishes), so no pass records a verdict and every counter keeps the
	// value of whichever pass last ran. Zero divergences, zero failures, and a
	// projection nothing has checked in hours.
	metric, issues := sweptThenHeld(stallWindow+time.Minute, "rebuild in flight", nil)

	if got := metric["capability"]["alert"]; got != "sweep-stalled" {
		t.Fatalf("alert = %v, want sweep-stalled", got)
	}
	is, ok := issueByCode(issues, issueCapabilitySweepStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilitySweepStalled, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning for a NAMED suppression", is.Severity)
	}
	if !strings.Contains(is.Message, "rebuild in flight") {
		t.Fatalf("message must name the cause the operator has to clear: %q", is.Message)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded", s)
	}
}

func TestEvalCapabilityLenses_AnUnexplainedStallIsAnError(t *testing.T) {
	// No suppression recorded means the sweep should be ticking and is not —
	// a dead goroutine or a pass wedged inside a read. Nothing will clear it.
	_, issues := sweptThenHeld(stallWindow+time.Minute, "", nil)

	is, ok := issueByCode(issues, issueCapabilitySweepStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilitySweepStalled, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error when nothing explains the stall", is.Severity)
	}
	if !strings.Contains(is.Message, "not ticking") {
		t.Fatalf("message must say the sweep is not running: %q", is.Message)
	}
	if s := aggregateStatus(issues); s != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", s)
	}
}

func TestEvalCapabilityLenses_ASweepingLensRaisesNoStall(t *testing.T) {
	// The steady state: a converged sweep writes nothing and reports nothing,
	// and must not be confused with a sweep that is not running.
	now := time.Now()
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{sweepSnap("capability", now, stallTestInterval, "")}
	}}
	metric, issues := h.evalCapabilityLenses(now)

	if _, ok := issueByCode(issues, issueCapabilitySweepStalled); ok {
		t.Fatalf("a lens that swept one interval ago is not stalled: %v", issues)
	}
	if got := metric["capability"]["alert"]; got != "ok" {
		t.Fatalf("alert = %v, want ok", got)
	}
	if got := metric["capability"]["sweepLastPassAt"]; got == "" {
		t.Fatal("sweepLastPassAt must be published so the clock is observable, not just alertable")
	}
}

func TestEvalCapabilityLenses_APausedLensIsNotAlsoReportedStalled(t *testing.T) {
	// A paused lens's suppression is deliberate and already an error in its own
	// right; a second issue for the same fact is noise, and would make an
	// operator pause look like two independent failures.
	now := time.Now()
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		s := sweepSnap("capability", now, stallWindow*3, "lens status is paused")
		s.Status = "paused"
		s.PauseReason = "operator"
		return []CapabilityLensStatus{s}
	}}
	metric, issues := h.evalCapabilityLenses(now)

	if _, ok := issueByCode(issues, issueCapabilitySweepStalled); ok {
		t.Fatalf("paused must not double-report as stalled: %v", issues)
	}
	if _, ok := issueByCode(issues, issueCapabilityLensPaused); !ok {
		t.Fatalf("the pause itself must still be raised: %v", issues)
	}
	if got := metric["capability"]["alert"]; got != "paused" {
		t.Fatalf("alert = %v, want paused", got)
	}
}

func TestEvalCapabilityLenses_ALensWithNoSweeperIsNeverStalled(t *testing.T) {
	// Zero interval means no sweep plan is installed, so there is no cadence to
	// be late against — the gate must be the sweeper's existence, not a clock.
	now := time.Now()
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		s := neverSweptSnap("capability")
		s.SweepInterval = 0
		return []CapabilityLensStatus{s}
	}}
	_, issues := h.evalCapabilityLenses(now)

	if _, ok := issueByCode(issues, issueCapabilitySweepStalled); ok {
		t.Fatalf("a lens with no sweeper cannot stall: %v", issues)
	}
}

func TestEvalCapabilityLenses_AFreshlyRegisteredLensGetsItsGraceWindow(t *testing.T) {
	// A lens that has never swept is measured from when this heartbeater first
	// saw it — not from process start, and not from the zero time, which would
	// flag every newly-installed lens on its first beat.
	first := time.Now()
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{neverSweptSnap("capability")}
	}}

	if _, issues := h.evalCapabilityLenses(first); len(issues) != 0 {
		t.Fatalf("a lens seen for the first time must not be stalled yet: %v", issues)
	}
	// Same lens, still never swept, now well past its window.
	_, issues := h.evalCapabilityLenses(first.Add(stallWindow + time.Minute))
	if _, ok := issueByCode(issues, issueCapabilitySweepStalled); !ok {
		t.Fatalf("the grace window must expire, not exempt: %v", issues)
	}
}

func TestEvalCapabilityLenses_AnExplainedStallStillEscalates(t *testing.T) {
	// A named cause buys a warning, not an indefinite one: past the escalation
	// window nobody is clearing it, and the auth plane has been unverified far
	// longer than a deliberate operator pause — which is already an error.
	_, issues := sweptThenHeld(stallWindow*capabilitySweepStallErrorMultiplier+time.Minute,
		"lens status unreadable: health: read existing capability: context deadline exceeded", nil)

	is, ok := issueByCode(issues, issueCapabilitySweepStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilitySweepStalled, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error past the escalation window", is.Severity)
	}
}

func TestEvalCapabilityLenses_ALongRebuildWarnsButNeverEscalates(t *testing.T) {
	// A rebuild is a superset of the sweep — truncate plus full rescan — so a
	// long one is not an unverified projection. It is worth a degrade (the read
	// model is mid-refill) but duration alone is not this detector's verdict.
	// With no progress report yet, the rebuild is UNKNOWN rather than wedged:
	// there is a real gap between Rebuild starting and its first poll landing,
	// and calling that stuck would fire on every rebuild there is.
	_, issues := sweptThenHeld(stallWindow*10, "rebuild in flight", func(s *CapabilityLensStatus) {
		s.Status = "rebuilding"
	})

	is, ok := issueByCode(issues, issueCapabilitySweepStalled)
	if !ok {
		t.Fatalf("a rebuild that outruns the window is still worth reporting: %v", issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning — duration is not this detector's verdict", is.Severity)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded", s)
	}
}

func TestEvalCapabilityLenses_ADrainingRebuildNeverEscalatesHoweverLongItRuns(t *testing.T) {
	// The exemption a rebuild earns is for DRAINING, and it is unconditional on
	// duration: a genuinely large rescan can outrun any window, and the whole
	// reason this detector defers to the rebuild is that it cannot judge how long
	// that legitimately takes.
	start := time.Now()
	cur := sweepSnap("capability", start, 0, "")
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{cur}
	}}
	h.evalCapabilityLenses(start)

	at := start.Add(stallWindow * 20)
	cur = sweepSnap("capability", at, stallWindow*20, "rebuild in flight")
	cur.Status = "rebuilding"
	cur.RebuildOutstanding = 4200
	cur.RebuildProgressAt = at.Add(-time.Second) // drained a moment ago
	_, issues := h.evalCapabilityLenses(at)

	is, ok := issueByCode(issues, issueCapabilitySweepStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilitySweepStalled, issues)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning — a rebuild that is still draining is not stuck", is.Severity)
	}
}

func TestEvalCapabilityLenses_AWedgedRebuildLosesTheExemption(t *testing.T) {
	// The carve-out exists because a rebuild supersedes the sweep. A rebuild that
	// has not drained a single message in the window that would have escalated
	// the sweep is superseding it with nothing: the sweep will not resume on its
	// own, and the auth-plane read model is unverified with no one told.
	start := time.Now()
	cur := sweepSnap("capability", start, 0, "")
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{cur}
	}}
	h.evalCapabilityLenses(start)

	at := start.Add(stallWindow * 4)
	cur = sweepSnap("capability", at, stallWindow*4, "rebuild in flight")
	cur.Status = "rebuilding"
	cur.RebuildOutstanding = 900
	cur.RebuildProgressAt = start // polled once, has drained nothing since
	metric, issues := h.evalCapabilityLenses(at)

	is, ok := issueByCode(issues, issueCapabilitySweepStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilitySweepStalled, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error — a rebuild draining nothing is stuck, not slow", is.Severity)
	}
	if !strings.Contains(is.Message, "has not drained") {
		t.Fatalf("the message must name the rebuild as the cause, got %q", is.Message)
	}
	if got := metric["capability"]["rebuildOutstanding"]; got != uint64(900) {
		t.Fatalf("rebuildOutstanding = %v, want the count an operator needs to see", got)
	}
}

func TestEvalCapabilityLenses_AFinishedRebuildPublishesNoStaleCount(t *testing.T) {
	// The last count a finished rebuild ended on is not a fact about the lens
	// now; publishing it would read as a rebuild permanently stuck there.
	metric, _ := sweptThenHeld(stallWindow+time.Minute, "", func(s *CapabilityLensStatus) {
		s.RebuildOutstanding = 77
		s.RebuildProgressAt = time.Now()
	})

	if _, present := metric["capability"]["rebuildOutstanding"]; present {
		t.Fatalf("an active lens must not publish a finished rebuild's outstanding count")
	}
}

func TestEvalCapabilityLenses_AStaleSuppressionReasonDoesNotExplainTheStall(t *testing.T) {
	// The reason describes the last tick. A sweep wedged INSIDE a tick leaves the
	// previous tick's reason standing, so trusting it unconditionally would report
	// a wedged sweep as merely suppressed — a warning, naming a cause that has
	// already cleared.
	_, issues := sweptThenHeld(stallWindow+time.Minute, "rebuild in flight", func(s *CapabilityLensStatus) {
		s.SweepSuppressionAt = s.SweepSuppressionAt.Add(-stallWindow) // many intervals old
	})

	is, ok := issueByCode(issues, issueCapabilitySweepStalled)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilitySweepStalled, issues)
	}
	if is.Severity != "error" {
		t.Fatalf("severity = %q, want error — a stale reason explains nothing", is.Severity)
	}
	if strings.Contains(is.Message, "rebuild in flight") {
		t.Fatalf("a cleared cause must not be named as current: %q", is.Message)
	}
}

func TestEvalCapabilityLenses_ResumingALongPauseDoesNotReadAsStalled(t *testing.T) {
	// The pause exemption has to re-baseline the clock, not merely skip the
	// verdict: otherwise the beat right after a resume charges the lens for the
	// whole pause and reports it stalled — for the one interval before its first
	// pass lands, on a lens doing exactly what it was told.
	start := time.Now()
	current := sweepSnap("capability", start, 0, "")
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		return []CapabilityLensStatus{current}
	}}
	// Active and sweeping: the lens has a real last-pass time and a baseline.
	h.evalCapabilityLenses(start)

	// Paused far longer than the window. The last pass stays where the pause
	// caught it — the sweep is suppressed, so nothing advances it.
	pauseEnd := start.Add(stallWindow * 3)
	for at := start; !at.After(pauseEnd); at = at.Add(time.Minute) {
		current = sweepSnap("capability", at, at.Sub(start), "lens status is paused")
		current.Status = "paused"
		current.PauseReason = "operator"
		h.evalCapabilityLenses(at)
	}

	// Resumed. Its first pass is still up to one interval away.
	resumeAt := pauseEnd.Add(10 * time.Second)
	current = sweepSnap("capability", resumeAt, resumeAt.Sub(start), "")
	_, issues := h.evalCapabilityLenses(resumeAt)

	if _, ok := issueByCode(issues, issueCapabilitySweepStalled); ok {
		t.Fatalf("a just-resumed lens gets a fresh window, not the pause charged to it: %v", issues)
	}
	// The fresh window is a grace, not an exemption: still unswept well past it.
	stillDead := resumeAt.Add(stallWindow + time.Minute)
	current = sweepSnap("capability", stillDead, stillDead.Sub(start), "")
	_, issues = h.evalCapabilityLenses(stillDead)
	if _, ok := issueByCode(issues, issueCapabilitySweepStalled); !ok {
		t.Fatalf("the fresh window must still expire: %v", issues)
	}
}

func TestEvalCapabilityLenses_AnUnreadableLensStaysInTheMetric(t *testing.T) {
	// An auth-plane lens missing from metrics.capabilityLens reports nothing at
	// all about the authorization read model, which renders as fine — so a lens
	// whose inputs cannot be read is published as unknown, and raises an issue.
	now := time.Now()
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		s := sweepSnap("capability", now, 0, "")
		s.Status = "unknown"
		s.Unreadable = "lens health entry: unmarshal entry capability: unexpected end of JSON input"
		return []CapabilityLensStatus{s}
	}}
	metric, issues := h.evalCapabilityLenses(now)

	entry, ok := metric["capability"]
	if !ok {
		t.Fatalf("the lens must keep its place in the metric map: %v", metric)
	}
	if got := entry["alert"]; got != "unreadable" {
		t.Fatalf("alert = %v, want unreadable", got)
	}
	if got, present := entry["consumerLag"]; !present || got != nil {
		t.Fatalf("consumerLag = %v (present=%v), want an explicit null — 'unknown' is not 0", got, present)
	}
	is, ok := issueByCode(issues, issueCapabilityLensUnreadable)
	if !ok {
		t.Fatalf("expected %s, got %v", issueCapabilityLensUnreadable, issues)
	}
	if !strings.Contains(is.Message, "unmarshal entry") {
		t.Fatalf("message must name the read that failed: %q", is.Message)
	}
	if s := aggregateStatus(issues); s != "degraded" {
		t.Fatalf("status = %q, want degraded — the observation path failed, not necessarily the lens", s)
	}
}

func TestEvalCapabilityLenses_AnUnreadableLensKeepsItsSweepVerdicts(t *testing.T) {
	// The unreadable input is the lens's health entry; the sweep's verdicts come
	// from the in-process sweeper and are unaffected, so a repair failure must
	// not be lost just because the reporter went unreadable in the same cycle.
	now := time.Now()
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus {
		s := sweepSnap("capability", now, 0, "")
		s.Status = "unknown"
		s.Unreadable = "lens health entry: boom"
		s.SweepFailingActors = 2
		s.SweepFailedStreak = capabilityRepairErrorStreak
		s.SweepLastFailure = "adapter: payload too large"
		return []CapabilityLensStatus{s}
	}}
	metric, issues := h.evalCapabilityLenses(now)

	if _, ok := issueByCode(issues, issueCapabilityRepairFailing); !ok {
		t.Fatalf("the repair verdict must survive an unreadable reporter: %v", issues)
	}
	if got := metric["capability"]["failingActors"]; got != 2 {
		t.Fatalf("failingActors = %v, want 2", got)
	}
	if got := metric["capability"]["alert"]; got != "unreadable" {
		t.Fatalf("alert = %v, want unreadable to outrank repair-failing — nothing else this cycle is trustworthy", got)
	}
}

func TestEvalCapabilityLenses_StallStateIsPrunedWithTheLens(t *testing.T) {
	// The clock-baseline map must not grow for the life of the process; a lens
	// that stops being reported drops its stamp, mirroring the lag debounce.
	now := time.Now()
	lenses := []CapabilityLensStatus{sweepSnap("capability", now, 0, "")}
	h := &LatticeHeartbeater{CapabilityLensProvider: func() []CapabilityLensStatus { return lenses }}
	h.evalCapabilityLenses(now)

	lenses = nil
	h.evalCapabilityLenses(now)

	h.lagMu.Lock()
	defer h.lagMu.Unlock()
	if len(h.sweepBase) != 0 {
		t.Fatalf("sweepBase = %v, want empty after the lens stopped being reported", h.sweepBase)
	}
}
