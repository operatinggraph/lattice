package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/healthkv"
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

// boundIssues caps how many entries one heartbeat document lists — the
// per-entity issue classes are unbounded in entity count, and the whole
// document is a single Health-KV value.
//
// What the cap must never do is evict the entry that explains the fault. The
// unbounded families are all warnings and they sort ahead of the config faults
// in key order, so selection is by SEVERITY first (ties on key order): a
// document is truncated to fifty instances of one warning only after every
// error has been listed. The bounded list must also never read as the whole
// set — the synthetic entry names the unlisted count, the true total, the
// distinct codes that went unlisted, and the worst severity among them.
func TestBoundIssues(t *testing.T) {
	t.Parallel()
	mk := func(n int, sev string) []healthIssue {
		out := make([]healthIssue, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, healthIssue{Severity: sev, Code: "C" + strconv.Itoa(i), Since: "2026-08-23T10:00:0" + strconv.Itoa(i%10) + "Z"})
		}
		return out
	}

	t.Run("under the cap is untouched", func(t *testing.T) {
		in := mk(3, "warning")
		got := boundIssues(in, 5)
		if len(got) != 3 || hasIssueCode(got, issuesTruncatedCode) {
			t.Fatalf("a fitting list must pass through verbatim, got %+v", got)
		}
	})

	t.Run("exactly at the cap is untouched", func(t *testing.T) {
		got := boundIssues(mk(5, "warning"), 5)
		if len(got) != 5 || hasIssueCode(got, issuesTruncatedCode) {
			t.Fatalf("a list exactly at the cap must not truncate, got %+v", got)
		}
	})

	t.Run("over the cap lists N plus one synthetic entry", func(t *testing.T) {
		got := boundIssues(mk(12, "warning"), 5)
		if len(got) != 6 {
			t.Fatalf("len = %d, want the cap (5) + 1 synthetic entry", len(got))
		}
		last := got[len(got)-1]
		if last.Code != issuesTruncatedCode {
			t.Fatalf("last entry = %+v, want the %s marker", last, issuesTruncatedCode)
		}
		if !strings.Contains(last.Message, "7 further") || !strings.Contains(last.Message, "12 open in total") {
			t.Fatalf("truncation message must name the unlisted count and the total, got %q", last.Message)
		}
		if last.Severity != "warning" {
			t.Fatalf("truncation severity = %q, want warning", last.Severity)
		}
		if last.Since != "2026-08-23T10:00:00Z" {
			t.Fatalf("truncation since = %q, want the oldest unlisted stamp", last.Since)
		}
		for i, is := range got[:5] {
			if is.Code != "C"+strconv.Itoa(i) {
				t.Fatalf("listed entry %d = %+v, want the head of the deterministic order", i, is)
			}
		}
	})

	t.Run("an error is never evicted by warnings", func(t *testing.T) {
		// The error sorts LAST in key order — exactly where the per-entity
		// families put a config fault once fifty subjects are violating.
		in := mk(12, "warning")
		in[11].Severity, in[11].Code = "error", "PlaybookConfigError"
		got := boundIssues(in, 5)
		if got[0].Code != "PlaybookConfigError" {
			t.Fatalf("the error must be selected first, got %+v", got[:5])
		}
		if !hasIssueCode(got[:5], "PlaybookConfigError") {
			t.Fatalf("an error must never be evicted while warnings are listed, listed = %+v", got[:5])
		}
		if sev := got[len(got)-1].Severity; sev != "warning" {
			t.Fatalf("only warnings went unlisted, so the marker is a warning; got %q", sev)
		}
		// Ties within a severity keep the incoming deterministic key order.
		for i, is := range got[1:5] {
			if is.Code != "C"+strconv.Itoa(i) {
				t.Fatalf("listed entry %d = %+v, want the head of the deterministic order", i+1, is)
			}
		}
	})

	t.Run("errors beyond the cap keep the marker at error", func(t *testing.T) {
		in := mk(12, "error")
		got := boundIssues(in, 5)
		if sev := got[len(got)-1].Severity; sev != "error" {
			t.Fatalf("truncation severity = %q, want error (errors themselves went unlisted)", sev)
		}
	})

	t.Run("ties keep the incoming deterministic key order", func(t *testing.T) {
		// Errors interleaved among the warnings: an unstable sort reorders
		// within each severity group here, so this pins the tiebreak rather
		// than merely observing an already-ordered slice pass through.
		const n, errorEvery, cap = 60, 7, 50
		in := make([]healthIssue, 0, n)
		var wantErrors, wantWarnings []string
		for i := 0; i < n; i++ {
			code := fmt.Sprintf("C%03d", i)
			sev := "warning"
			if i%errorEvery == 0 {
				sev = "error"
				wantErrors = append(wantErrors, code)
			} else {
				wantWarnings = append(wantWarnings, code)
			}
			in = append(in, healthIssue{Severity: sev, Code: code})
		}
		want := append(append([]string{}, wantErrors...), wantWarnings...)[:cap]

		got := boundIssues(in, cap)
		for i, is := range got[:cap] {
			if is.Code != want[i] {
				t.Fatalf("listed entry %d = %q, want %q — errors first, then ties in the incoming key order",
					i, is.Code, want[i])
			}
		}
	})

	t.Run("the marker names the omitted codes, most numerous first", func(t *testing.T) {
		in := append(mk(7, "warning"), healthIssue{Severity: "warning", Code: "UnroutedTasks"})
		for i := range in[:7] {
			in[i].Code = "RowDataError"
		}
		msg := boundIssues(in, 2)[2].Message
		const want = "listed): RowDataError ×5, UnroutedTasks ×1"
		if !strings.HasSuffix(msg, want) {
			t.Fatalf("marker message = %q, want it to end %q", msg, want)
		}
	})

	t.Run("equal code counts render alphabetically", func(t *testing.T) {
		in := []healthIssue{
			{Severity: "warning", Code: "Zebra"}, {Severity: "warning", Code: "Alpha"},
			{Severity: "warning", Code: "Zebra"}, {Severity: "warning", Code: "Alpha"},
			{Severity: "warning", Code: "Keep"},
		}
		// Cap 1 lists the first Zebra; the omitted set is Alpha×2, Zebra×1,
		// Keep×1 — so the count ordering leads and the equal-count pair (Keep,
		// Zebra) breaks alphabetically rather than by encounter.
		msg := boundIssues(in, 1)[1].Message
		const want = "listed): Alpha ×2, Keep ×1, Zebra ×1"
		if !strings.HasSuffix(msg, want) {
			t.Fatalf("marker message = %q, want it to end %q (the same set must always render identically)", msg, want)
		}
	})

	t.Run("the code list is itself bounded", func(t *testing.T) {
		in := make([]healthIssue, 0, 30)
		for i := 0; i < 30; i++ {
			in = append(in, healthIssue{Severity: "warning", Code: fmt.Sprintf("Code%02d", i)})
		}
		msg := boundIssues(in, 1)[1].Message
		if !strings.Contains(msg, "+21 more codes") {
			t.Fatalf("an open-ended code vocabulary must not make the marker the thing needing truncation, got %q", msg)
		}
	})

	t.Run("a non-positive cap disables truncation", func(t *testing.T) {
		if got := boundIssues(mk(12, "warning"), 0); len(got) != 12 {
			t.Fatalf("cap 0 must disable the bound, got %d entries", len(got))
		}
	})
}

// A truncated heartbeat must still let an operator SEE the cause, not just the
// verdict. Sixty violating subjects plus one PlaybookConfigError is the shape
// the per-entity families make routine: in key order the config error sorts
// behind every `gap:` entry and would be the first thing evicted, leaving a
// document that reports unhealthy while listing fifty identical warnings and
// naming nothing that explains it. The error must be listed, and the marker
// must name what went unlisted.
//
// This test makes no claim about the ORDER of aggregation and truncation in
// emit — see aggregateStatus's own test. Those two orders are provably
// equivalent (severity-first selection cannot evict an error while warnings are
// listed, and the marker carries the worst omitted severity besides), so no
// test can distinguish them.
func TestEmit_BoundsTheListingWithoutHidingTheCause(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const healthBucket = "health-kv"
	if _, err := m.conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: healthBucket}); err != nil {
		t.Fatalf("create %s: %v", healthBucket, err)
	}
	cache := newIssueCache()
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	h := &heartbeater{
		conn:                  m.conn,
		bucket:                healthBucket,
		instance:              "unit-" + testNanoID(t),
		startedAt:             time.Now(),
		interval:              time.Second,
		states:                healthkv.NewConsumerStateCache(),
		issues:                cache,
		source:                newTargetSource(nil, "core-kv", "test", cache, logger),
		marks:                 m,
		effectMismatchAlerted: make(map[string]struct{}),
		consumerPausedSince:   make(map[string]string),
		logger:                logger,
	}

	// One warning per violating subject, plus a single config error whose key
	// sorts behind every one of them — well past the cap.
	const total = maxHeartbeatIssues + 10
	for i := 0; i < total-1; i++ {
		cache.set(fmt.Sprintf("gap:t.%020d.missing_claim", i), "warning", "UnroutedTasks", "row column missing_claim is true")
	}
	cache.set(issueKeyGapConfig("t", "missing_claim"), "error", "PlaybookConfigError", "the playbook does not resolve")

	h.emit(ctx, "healthy")

	entry, err := h.conn.KVGet(ctx, healthBucket, h.key())
	if err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	var doc struct {
		Status string        `json:"status"`
		Issues []healthIssue `json:"issues"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal heartbeat: %v", err)
	}
	if len(doc.Issues) != maxHeartbeatIssues+1 {
		t.Fatalf("listed %d issues, want the cap (%d) + 1 synthetic entry", len(doc.Issues), maxHeartbeatIssues)
	}
	if !hasIssueCode(doc.Issues, "PlaybookConfigError") {
		t.Fatalf("the cap evicted the only entry that explains the fault; listed codes = %v",
			listedCodes(doc.Issues))
	}
	if doc.Issues[0].Code != "PlaybookConfigError" {
		t.Fatalf("the error must be selected ahead of the warnings, got %+v", doc.Issues[0])
	}
	last := doc.Issues[len(doc.Issues)-1]
	if last.Code != issuesTruncatedCode {
		t.Fatalf("last entry = %+v, want the %s marker", last, issuesTruncatedCode)
	}
	if !strings.Contains(last.Message, strconv.Itoa(total)+" open in total") {
		t.Fatalf("truncation message must name the true total (%d), got %q", total, last.Message)
	}
	if !strings.Contains(last.Message, "UnroutedTasks ×") {
		t.Fatalf("truncation message must name the unlisted codes, got %q", last.Message)
	}
	if doc.Status != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", doc.Status)
	}
}

// listedCodes renders the distinct codes a heartbeat document listed, for a
// failure message that has to explain what the cap kept instead.
func listedCodes(issues []healthIssue) []string {
	seen := map[string]bool{}
	var out []string
	for _, is := range issues {
		if !seen[is.Code] {
			seen[is.Code] = true
			out = append(out, is.Code)
		}
	}
	return out
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
