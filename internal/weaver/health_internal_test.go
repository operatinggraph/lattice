package weaver

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// aggregateStatus must reconcile the lifecycle status with the open issue set
// per Contract #5 §5.3: a heartbeat is "healthy" only when issues is empty; an
// open warning (or any other unrecognized non-empty severity) ⇒ "degraded"; an
// open error ⇒ "unhealthy" (worst-wins). An unknown severity must NOT leave the
// status clean — that would let an issue sit open while the heartbeat reports
// healthy, breaking §5.3's issues-empty-iff-healthy invariant. The "starting" /
// "shutdown" lifecycle phases are reported verbatim regardless of transient
// issues.
func TestAggregateStatus(t *testing.T) {
	t.Parallel()
	warn := healthIssue{Severity: "warning", Code: "TemplateDataError"}
	err := healthIssue{Severity: "error", Code: "TargetRejected"}

	cases := []struct {
		name      string
		lifecycle string
		issues    []healthIssue
		want      string
	}{
		{"healthy no issues", "healthy", nil, "healthy"},
		{"healthy empty slice", "healthy", []healthIssue{}, "healthy"},
		{"healthy with warning degrades", "healthy", []healthIssue{warn}, "degraded"},
		{"healthy with error is unhealthy", "healthy", []healthIssue{err}, "unhealthy"},
		{"error wins over warning", "healthy", []healthIssue{warn, err}, "unhealthy"},
		{"error wins regardless of order", "healthy", []healthIssue{err, warn}, "unhealthy"},
		{"multiple warnings stay degraded", "healthy", []healthIssue{warn, warn}, "degraded"},
		{"starting verbatim despite error", "starting", []healthIssue{err}, "starting"},
		{"shutdown verbatim despite error", "shutdown", []healthIssue{err}, "shutdown"},
		{"unknown severity degrades not ignored", "healthy", []healthIssue{{Severity: "info", Code: "X"}}, "degraded"},
		{"unknown severity still loses to error", "healthy", []healthIssue{{Severity: "critical", Code: "X"}, err}, "unhealthy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregateStatus(tc.lifecycle, tc.issues); got != tc.want {
				t.Fatalf("aggregateStatus(%q, %v) = %q, want %q", tc.lifecycle, tc.issues, got, tc.want)
			}
		})
	}
}

// The heartbeat TTL (Contract #5 §5.6) derives from interval × ttlMultiplier,
// defaults to healthkv.DefaultTTLMultiplier, and 0 disables it (an escape
// hatch for an operator who wants sticky keys). Real NATS expiry mechanics are
// proven once at the substrate layer (internal/substrate) and by the
// Processor heartbeater's end-to-end TTL test; this pins the derivation only.
func TestHeartbeaterTTLDerivation(t *testing.T) {
	t.Parallel()
	h := &heartbeater{interval: 10 * time.Second, ttlMultiplier: 10}
	if got, want := h.heartbeatTTL(), 100*time.Second; got != want {
		t.Fatalf("heartbeatTTL() = %v, want %v", got, want)
	}
	h.SetTTLMultiplier(0)
	if got, want := h.heartbeatTTL(), time.Duration(0); got != want {
		t.Fatalf("multiplier=0 heartbeatTTL() = %v, want %v (disabled)", got, want)
	}
	h.SetTTLMultiplier(-5)
	if got, want := h.heartbeatTTL(), time.Duration(0); got != want {
		t.Fatalf("negative multiplier must clamp to 0, heartbeatTTL() = %v, want %v", got, want)
	}
}

// issueCache.set must stamp since (Contract #5 §5.5) on first appearance, hold
// it steady across repeat set calls for the same key while the issue stays
// open, and clear it with the issue so a later re-occurrence gets a fresh
// since rather than reusing the stale one.
func TestIssueCacheSincePersistence(t *testing.T) {
	t.Parallel()
	c := newIssueCache()

	c.set("k", "warning", "Code", "first")
	first := c.snapshot()
	if len(first) != 1 || first[0].Since == "" {
		t.Fatalf("first set: got %+v, want one issue with a non-empty since", first)
	}
	since := first[0].Since

	c.set("k", "warning", "Code", "still open")
	second := c.snapshot()
	if len(second) != 1 || second[0].Since != since {
		t.Fatalf("since not persisted across repeat set: first %q, second %+v", since, second)
	}

	c.clear("k")
	if len(c.snapshot()) != 0 {
		t.Fatalf("cleared key still present: %+v", c.snapshot())
	}

	c.set("k", "warning", "Code", "reoccurred")
	reoccurred := c.snapshot()
	if len(reoccurred) != 1 || reoccurred[0].Since == since {
		t.Fatalf("reoccurred issue reused stale since %q: %+v", since, reoccurred)
	}
}

// The inline ConsumerPaused issue (built from live consumer state, not routed
// through issueCache) must carry the same since-persistence guarantee: stamped
// once while a consumer stays pausedStructural, cleared and re-stamped once it
// resumes and pauses again.
func TestPausedIssuesSincePersistence(t *testing.T) {
	t.Parallel()
	h := &heartbeater{consumerPausedSince: make(map[string]string)}
	t1 := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(30 * time.Second)

	paused := map[string]string{"c1": "pausedStructural"}

	first := h.pausedIssues(paused, t1)
	if len(first) != 1 || first[0].Code != "ConsumerPaused" || first[0].Since == "" {
		t.Fatalf("first tick: got %+v, want one ConsumerPaused issue with a since", first)
	}
	since := first[0].Since

	second := h.pausedIssues(paused, t2)
	if len(second) != 1 || second[0].Since != since {
		t.Fatalf("since not persisted: first %q, second %+v", since, second)
	}

	resumed := h.pausedIssues(map[string]string{"c1": "running"}, t2.Add(10*time.Second))
	if len(resumed) != 0 {
		t.Fatalf("resumed tick: got %d issues, want 0", len(resumed))
	}
	if _, ok := h.consumerPausedSince["c1"]; ok {
		t.Fatalf("resumed consumer still tracked in consumerPausedSince")
	}

	repaused := h.pausedIssues(paused, t2.Add(time.Minute))
	if len(repaused) != 1 || repaused[0].Since == since {
		t.Fatalf("repaused consumer reused stale since %q: %+v", since, repaused)
	}
}

// flagEffectMismatches is the loud surface for "dispatches commit but closes
// never arrive" (weaver-planner-mandate-design.md §3.4): once an `__effect`
// confidence window fills with zero observed closes it raises a standing
// LensEffectMismatch, and the issue clears on the first pass that no longer
// lists the window (a close finally lands). This drives the heartbeater method
// end-to-end through a real weaver-state markStore — the markStore scan itself
// is pinned by TestScanEffectMismatches_FullWindowZeroCloses; this pins the
// issue set → recover → clear lifecycle and the effectMismatches metric.
func TestFlagEffectMismatches_SetThenClearOnRecovery(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	h := &heartbeater{
		marks:                 m,
		issues:                newIssueCache(),
		effectMismatchAlerted: make(map[string]struct{}),
		logger:                slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	}
	const targetID, gap, action = "t1", "missing_x", "directOp"
	wantKey := issueKeyEffect(targetID, gap, action)

	// Fill the window with zero closes — the mismatch must now be raised.
	for i := 0; i < effectWindowSize; i++ {
		if err := m.recordEffectDispatch(ctx, targetID, gap, action); err != nil {
			t.Fatalf("recordEffectDispatch #%d: %v", i, err)
		}
	}
	metrics := map[string]any{}
	h.flagEffectMismatches(ctx, metrics)
	if got := metrics["effectMismatches"]; got != 1 {
		t.Fatalf("effectMismatches metric = %v, want 1", got)
	}
	if _, ok := h.effectMismatchAlerted[wantKey]; !ok {
		t.Fatalf("effectMismatchAlerted missing %q after a full zero-close window", wantKey)
	}
	if is, ok := effectIssue(h.issues.snapshot()); !ok {
		t.Fatalf("LensEffectMismatch issue not set; snapshot=%+v", h.issues.snapshot())
	} else if is.Severity != "warning" || !strings.Contains(is.Message, targetID) || !strings.Contains(is.Message, action) {
		t.Fatalf("issue = %+v, want severity=warning and message naming target %q action %q", is, targetID, action)
	}

	// A single close recovers the window — the next pass must clear the issue
	// and drop it from effectMismatchAlerted.
	if err := m.recordEffectClose(ctx, targetID, gap, action); err != nil {
		t.Fatalf("recordEffectClose: %v", err)
	}
	metrics2 := map[string]any{}
	h.flagEffectMismatches(ctx, metrics2)
	if got := metrics2["effectMismatches"]; got != 0 {
		t.Fatalf("effectMismatches metric after recovery = %v, want 0", got)
	}
	if _, ok := h.effectMismatchAlerted[wantKey]; ok {
		t.Fatalf("effectMismatchAlerted still tracks %q after recovery", wantKey)
	}
	if _, ok := effectIssue(h.issues.snapshot()); ok {
		t.Fatalf("LensEffectMismatch issue not cleared after recovery; snapshot=%+v", h.issues.snapshot())
	}
}

// effectIssue returns the single LensEffectMismatch issue in a snapshot, if
// present. The snapshot value does not carry its cache key, so the test matches
// on Code (the lifecycle test raises exactly one).
func effectIssue(issues []healthIssue) (healthIssue, bool) {
	for _, is := range issues {
		if is.Code == "LensEffectMismatch" {
			return is, true
		}
	}
	return healthIssue{}, false
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// formatISODuration renders a duration as an ISO 8601 duration, clamping a
// negative input to zero and rolling seconds up through minutes and hours.
func TestFormatISODuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"zero", 0, "PT0S"},
		{"negative clamps to zero", -5 * time.Second, "PT0S"},
		{"sub-minute", 30 * time.Second, "PT30S"},
		{"minutes and seconds", 90 * time.Second, "PT1M30S"},
		{"exact hour boundary", time.Hour, "PT1H0M0S"},
		{"hours minutes seconds", 2*time.Hour + 3*time.Minute + 4*time.Second, "PT2H3M4S"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatISODuration(tc.in); got != tc.want {
				t.Fatalf("formatISODuration(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
