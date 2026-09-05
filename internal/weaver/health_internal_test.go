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
		cache.set(issueKeyDataEntity("t", fmt.Sprintf("%020d", i), "missing_claim"), "warning", "RowDataError",
			"row column missing_claim is not a bool")
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
	if !strings.Contains(last.Message, "RowDataError ×") {
		t.Fatalf("truncation message must name the unlisted codes, got %q", last.Message)
	}
	if doc.Status != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", doc.Status)
	}
}

// TestEmit_ClosesTheRefusalWindow pins the boundary the overflow entry's count
// is measured against. That count is "refusals since the last heartbeat", and
// the heartbeat is the only walk of the cache on the right cadence — snapshot is
// a pure read with no reset leg, so wiring the reset to it would leave the
// number a monotone total that never falls even after every refusal stops.
//
// The vector is a target refused only BEFORE the first heartbeat: the second
// heartbeat must report zero, because nothing was refused in its window.
func TestEmit_ClosesTheRefusalWindow(t *testing.T) {
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

	const targetID = "targetHeartbeatWindow"
	for i := 0; i < rowIssueCapPerTarget; i++ {
		cache.set(issueKeyDataEntity(targetID, fmt.Sprintf("%020d", i), "violating"),
			"warning", "RowDataError", "row column violating is not a bool")
	}
	for i := 0; i < 3; i++ {
		cache.set(issueKeyGapEntity(targetID, "eParked", "missing_a"), "warning", "GapBudgetExhausted",
			"budget spent")
	}

	overflowMessage := func() string {
		t.Helper()
		entry, err := h.conn.KVGet(ctx, healthBucket, h.key())
		if err != nil {
			t.Fatalf("read heartbeat: %v", err)
		}
		var doc struct {
			Issues []healthIssue `json:"issues"`
		}
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			t.Fatalf("unmarshal heartbeat: %v", err)
		}
		for _, is := range doc.Issues {
			if is.Code == rowIssuesCappedCode {
				return is.Message
			}
		}
		t.Fatalf("the heartbeat listed no %s entry; codes = %v", rowIssuesCappedCode, listedCodes(doc.Issues))
		return ""
	}

	h.emit(ctx, "healthy")
	if got := overflowMessage(); !strings.Contains(got, "3 raises for untracked rows were refused") {
		t.Fatalf("the first heartbeat must report the refusals in its own window, got %q", got)
	}

	// Nothing is refused between the two heartbeats.
	h.emit(ctx, "healthy")
	if got := overflowMessage(); !strings.Contains(got, "0 raises for untracked rows were refused") {
		t.Fatalf("a heartbeat whose window saw no refusal must report zero — a count that only ever "+
			"climbs is a total, not a window; got %q", got)
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

// TestAlert_LogsTheArrivalLoudlyAndTheRepeatQuietly pins the two logging
// postures apart, and the boundary between them is the point.
//
// alert logs EVERY raise at Error. Several families raise the same
// (key, severity, code) with a message that differs per occurrence — a dropped
// fired timer names the timer it dropped — so for them the message is the only
// thing telling two genuinely distinct faults apart, and damping by
// severity+code would discard every fault after the first.
//
// alertStanding is the narrow seam for a raise the engine re-derives on a
// CADENCE: the §10.8 exhausted-gap raise is re-evaluated by every sweep pass
// for as long as the retry budget stands, so its continuation logs at Debug and
// only its arrival is loud. A change of severity or code at that key is a
// different fact and arrives loudly again. The Health issue is identical either
// way — the latch is what carries the fact; the level only decides how loudly
// the stream says it just happened.
func TestAlert_LogsTheArrivalLoudlyAndTheRepeatQuietly(t *testing.T) {
	t.Parallel()

	t.Run("alertStanding damps the continuation of one standing fact", func(t *testing.T) {
		logs := &logCapture{}
		e := &Engine{logger: slog.New(logs), issues: newIssueCache()}
		for i := 0; i < 3; i++ {
			e.alertStanding("gap:t1.e1.missing_x", "warning", "GapBudgetExhausted", "budget spent for e1")
		}
		levels := logs.levelsContaining("budget spent for e1")
		if len(levels) != 3 {
			t.Fatalf("captured %d records, want 3 (every raise still logs SOMETHING)", len(levels))
		}
		if levels[0] != slog.LevelError {
			t.Fatalf("first raise logged at %v, want Error (the arrival is the loud one)", levels[0])
		}
		if levels[1] != slog.LevelDebug || levels[2] != slog.LevelDebug {
			t.Fatalf("repeat raises logged at %v/%v, want Debug", levels[1], levels[2])
		}
		if issues := e.issues.snapshot(); len(issues) != 1 || issues[0].Code != "GapBudgetExhausted" {
			t.Fatalf("the standing issue must be unaffected by the log level choice, got %+v", issues)
		}
	})

	t.Run("alertStanding re-arrives when the fact itself changes", func(t *testing.T) {
		logs := &logCapture{}
		e := &Engine{logger: slog.New(logs), issues: newIssueCache()}
		e.alertStanding("gap:t1.e1.missing_x", "warning", "GapBudgetExhausted", "budget spent for e1")
		e.alertStanding("gap:t1.e1.missing_x", "error", "GapBudgetExhausted", "budget spent for e1")
		e.alertStanding("gap:t1.e1.missing_x", "error", "SomethingElse", "budget spent for e1")
		levels := logs.levelsContaining("budget spent for e1")
		for i, lv := range levels {
			if lv != slog.LevelError {
				t.Fatalf("raise %d logged at %v, want Error (severity/code changed — a new fact)", i, lv)
			}
		}
	})

	// The defect this boundary exists to prevent: a family that raises one key
	// with a DISTINCT message per occurrence must not have those occurrences
	// damped away. Each is its own fault, and the Health slot keeps only the
	// last, so the log is the only place the earlier ones survive at all.
	t.Run("alert keeps every distinct fault at one key loud", func(t *testing.T) {
		logs := &logCapture{}
		e := &Engine{logger: slog.New(logs), issues: newIssueCache()}
		e.alert("timer:t1", "warning", "TimerDataError", "dropped fired timer for entity aaa")
		e.alert("timer:t1", "warning", "TimerDataError", "dropped fired timer for entity bbb")
		for _, entity := range []string{"aaa", "bbb"} {
			levels := logs.levelsContaining("entity " + entity)
			if len(levels) != 1 || levels[0] != slog.LevelError {
				t.Fatalf("the drop naming %s logged %v, want exactly one Error", entity, levels)
			}
		}
	})
}

// loudPacedRaise records a raise and reports only whether it was loud, for the
// subtests whose subject is the pacing verdict rather than the arrival stamp.
func loudPacedRaise(c *issueCache, key, severity, code string, now time.Time) bool {
	loud, _ := c.pacedRaise(key, severity, code, now)
	return loud
}

// TestPacedRaise_RationsLoudRecordsAgainstAClock pins the pacing decision at
// the cache, where the clock is a parameter and two simulated hours cost
// nothing. Every subtest here is about a boundary the LATCH cannot express —
// which is why the memory is a separate map with its own lifetime.
func TestPacedRaise_RationsLoudRecordsAgainstAClock(t *testing.T) {
	t.Parallel()

	const key = "gapConfig:t1.missing_x"
	const sev, code = "warning", "UnresolvedReference"

	// The shape of the flood this exists to stop: a parked gap re-derives its
	// failure once per sweep pass — a minute — for the dispatch-count TTL's whole
	// life. Two hours of that cadence is 120 records, of which exactly two may be
	// loud: the arrival, and the one that opens the second interval.
	t.Run("one loud record per interval at the sweep cadence", func(t *testing.T) {
		c := newIssueCache()
		start := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
		loud, loudAt := 0, []int{}
		for minute := 0; minute < 120; minute++ {
			if loudPacedRaise(c, key, sev, code, start.Add(time.Duration(minute)*time.Minute)) {
				loud++
				loudAt = append(loudAt, minute)
			}
		}
		if loud != 2 {
			t.Fatalf("120 passes a minute apart produced %d loud records at minutes %v, want exactly 2 "+
				"(one per %v)", loud, loudAt, logPaceInterval)
		}
		if loudAt[0] != 0 || loudAt[1] != 60 {
			t.Fatalf("loud records at minutes %v, want the arrival and the interval boundary (0, 60)", loudAt)
		}
	})

	// The defect the whole increment exists for. The keys this seam serves are
	// cleared by paths that are not evidence the fault ended — issueKeyGapConfig
	// is target-scoped, so one entity's column closing retires a fact another
	// entity's parked gap is still raising. Damping on latch presence
	// (alertStanding) reports an arrival on the very next pass, so a clock the
	// clear cannot reach is the only memory that holds.
	t.Run("an issues.clear between passes does not restore loudness", func(t *testing.T) {
		c := newIssueCache()
		start := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
		if !loudPacedRaise(c, key, sev, code, start) {
			t.Fatal("setup: the arrival must be loud")
		}
		c.set(key, sev, code, "the reference does not resolve")

		// Another entity's column stops being reported: clearClosedMarks empties
		// the target-scoped latch, but this target's playbook is no more fixed
		// than it was a minute ago.
		c.clear(key)
		if loudPacedRaise(c, key, sev, code, start.Add(time.Minute)) {
			t.Fatal("a clear is not evidence the fault ended; the next pass must stay damped")
		}
		if c.standingAs(key, sev, code) {
			t.Fatal("setup check: the latch really is empty here — that is what makes this the interesting case")
		}
	})

	t.Run("a different severity or code is loud at once", func(t *testing.T) {
		c := newIssueCache()
		start := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
		if !loudPacedRaise(c, key, "warning", "UnresolvedReference", start) {
			t.Fatal("setup: the arrival must be loud")
		}
		if !loudPacedRaise(c, key, "error", "UnresolvedReference", start.Add(time.Minute)) {
			t.Fatal("a severity change at one key is a different fact and must not wait out the window")
		}
		if !loudPacedRaise(c, key, "error", "PlaybookConfigError", start.Add(2*time.Minute)) {
			t.Fatal("a code change at one key is a different fact and must not wait out the window")
		}
		// The new fact resets the clock rather than exempting the key.
		if loudPacedRaise(c, key, "error", "PlaybookConfigError", start.Add(3*time.Minute)) {
			t.Fatal("the changed fact's own repeat must be damped like any other")
		}
	})

	// A prefix clear is the SUBJECT leaving, which is the one clear that IS
	// evidence there is no cadence left to pace. Its neighbour under a
	// longer-named target must be untouched — the trailing separator rule.
	t.Run("clearPrefix takes the pace entry with the issue", func(t *testing.T) {
		c := newIssueCache()
		start := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
		const revoked, survivor = "gapConfig:t1.missing_x", "gapConfig:t10.missing_x"
		for _, k := range []string{revoked, survivor} {
			if !loudPacedRaise(c, k, sev, code, start) {
				t.Fatalf("setup: %q must arrive loud", k)
			}
			c.set(k, sev, code, "the reference does not resolve")
		}

		c.clearPrefix("gapConfig:t1.")

		if !loudPacedRaise(c, revoked, sev, code, start.Add(time.Minute)) {
			t.Fatal("a revoked target's pace entry must go with its issue; a re-registration's first raise is an arrival")
		}
		if loudPacedRaise(c, survivor, sev, code, start.Add(time.Minute)) {
			t.Fatalf("clearPrefix(%q) reached a key under t10.; the trailing separator is what keeps them apart", "gapConfig:t1.")
		}
	})

	t.Run("prunePaced bounds the map and a pruned key re-arrives loud", func(t *testing.T) {
		c := newIssueCache()
		start := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
		const stale, recent = "gapConfig:t1.missing_stale", "gapConfig:t1.missing_recent"
		loudPacedRaise(c, stale, sev, code, start)
		loudPacedRaise(c, recent, sev, code, start.Add(90*time.Minute))

		// Twice the interval past the stale entry's last loud record, and half an
		// interval past the recent one's.
		if n := c.prunePaced(start.Add(2 * logPaceInterval)); n != 1 {
			t.Fatalf("prunePaced dropped %d entries, want 1 (only the one past twice the interval)", n)
		}
		if n := len(c.paced); n != 1 {
			t.Fatalf("pace map holds %d entries after the prune, want 1", n)
		}
		// Behaviour-neutral: the pruned key was already past its interval, so its
		// next raise was going to be loud either way.
		if !loudPacedRaise(c, stale, sev, code, start.Add(2*logPaceInterval)) {
			t.Fatal("a pruned key's next raise must be loud")
		}
		if loudPacedRaise(c, recent, sev, code, start.Add(2*logPaceInterval)) {
			t.Fatal("the prune must not touch an entry inside its window")
		}
	})
}

// TestAlertPaced_KeepsTheLatchWhileRationingTheLog pins the seam over the cache:
// what alertPaced does with the pacing verdict, and what it does regardless of
// it. The Health entry is not paced — only the log is — so every pass sets the
// latch with the current message, and a damped pass moves nothing an operator
// reads off Health.
func TestAlertPaced_KeepsTheLatchWhileRationingTheLog(t *testing.T) {
	t.Parallel()

	const key = "gapConfig:t1.missing_x"

	t.Run("the latch is set on every pass, damped or loud", func(t *testing.T) {
		logs := &logCapture{}
		e := &Engine{logger: slog.New(logs), issues: newIssueCache()}

		e.alertPaced(key, "warning", "UnresolvedReference", "pass 1: pattern ghostFlow does not resolve")
		first, ok := issueAt(e.issues, key)
		if !ok {
			t.Fatalf("the arrival must raise the issue, issues = %+v", e.issues.snapshot())
		}
		// A second pass inside the window, with a DIFFERENT message: damped in the
		// log (message is never compared — an embedded count would re-arrive every
		// pass and bring the flood back) but fully current on Health.
		e.alertPaced(key, "warning", "UnresolvedReference", "pass 2: pattern ghostFlow does not resolve")

		second, ok := issueAt(e.issues, key)
		if !ok {
			t.Fatalf("a damped pass must still set the issue, issues = %+v", e.issues.snapshot())
		}
		if second.Message != "pass 2: pattern ghostFlow does not resolve" {
			t.Fatalf("the latch carries %q, want the newest message — only the log is paced", second.Message)
		}
		if second.Since != first.Since {
			t.Fatalf("the fault's since moved %q -> %q across a damped pass; pacing must not touch the latch",
				first.Since, second.Since)
		}
		if lv := logs.levelsContaining("pass 1:"); len(lv) != 1 || lv[0] != slog.LevelWarn {
			t.Fatalf("the arrival logged %v, want exactly one Warn", lv)
		}
		if lv := logs.levelsContaining("pass 2:"); len(lv) != 1 || lv[0] != slog.LevelDebug {
			t.Fatalf("the damped pass logged %v, want exactly one Debug — a record is never dropped outright", lv)
		}
	})

	// One seam serves both loud levels planGap's switch uses, so the loud level
	// has to come from the raise itself: an `error` raise stays Error and a
	// `warning` raise stays Warn. (alert, by contrast, is Error for every
	// caller — which is why this is not alert with a clock bolted on.)
	t.Run("the loud level is the caller's own severity", func(t *testing.T) {
		logs := &logCapture{}
		e := &Engine{logger: slog.New(logs), issues: newIssueCache()}
		e.alertPaced("gapConfig:t1.missing_warn", "warning", "UnresolvedReference", "a deferred reference")
		e.alertPaced("gapConfig:t1.missing_err", "error", "PlaybookConfigError", "an un-dispatchable action")
		if lv := logs.levelsContaining("a deferred reference"); len(lv) != 1 || lv[0] != slog.LevelWarn {
			t.Fatalf("the warning raise logged %v, want exactly one Warn", lv)
		}
		if lv := logs.levelsContaining("an un-dispatchable action"); len(lv) != 1 || lv[0] != slog.LevelError {
			t.Fatalf("the error raise logged %v, want exactly one Error", lv)
		}
	})

	// The seam-level statement of the defect: the clear that empties this
	// target-scoped latch says nothing about the playbook, so it must not buy the
	// next pass a loud record.
	t.Run("a clear between passes does not make the next one loud", func(t *testing.T) {
		logs := &logCapture{}
		e := &Engine{logger: slog.New(logs), issues: newIssueCache()}
		e.alertPaced(key, "warning", "UnresolvedReference", "arrival: ghostFlow does not resolve")
		e.issues.clear(key)
		e.alertPaced(key, "warning", "UnresolvedReference", "after the clear: ghostFlow does not resolve")
		if lv := logs.levelsContaining("after the clear:"); len(lv) != 1 || lv[0] != slog.LevelDebug {
			t.Fatalf("the pass after a clear logged %v, want exactly one Debug — a clear is not evidence the fault ended", lv)
		}
		if _, ok := issueAt(e.issues, key); !ok {
			t.Fatalf("the pass after a clear must re-raise the latch, issues = %+v", e.issues.snapshot())
		}
	})
}

// TestEmit_PrunesThePaceMemory pins the pace map's age bound to the pass that
// actually runs it. The map is keyed per (target, entity, gap) like the issue
// families it paces, and nothing on the live path deletes an entry for a fault
// that simply stopped being re-derived — a gap that closed, a target that went
// quiet. The heartbeat already walks the cache on its own cadence, so it is
// where the bound is applied; without the call the map only ever grows, one
// entry per key raised since the process started.
func TestEmit_PrunesThePaceMemory(t *testing.T) {
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

	const aged, live = "gapConfig:t1.missing_aged", "gapConfig:t1.missing_live"
	now := time.Now()
	loudPacedRaise(cache, aged, "warning", "UnresolvedReference", now.Add(-3*logPaceInterval))
	loudPacedRaise(cache, live, "warning", "UnresolvedReference", now)

	h.emit(ctx, "healthy")

	if _, ok := cache.paced[aged]; ok {
		t.Fatalf("the heartbeat must bound the pace memory; %q survived past twice the interval", aged)
	}
	if _, ok := cache.paced[live]; !ok {
		t.Fatalf("the heartbeat pruned %q, which is still inside its window; pace map = %+v", live, cache.paced)
	}
}

// TestPacedRaise_CarriesTheArrivalTheLatchCannotKeep pins the pace entry's
// second job. `since` is Contract #5 §5.5's "open since it first arose", and for
// the families alertPaced serves the latch cannot carry it: clear deletes the
// stamp, and the clears these keys see are other entities' closes rather than
// repairs — issueKeyGapConfig is target-scoped, so clearClosedMarks empties it
// whenever ANY one entity's column stops being reported. Left to the latch the
// stamp resets about once a pass, which with the log now damped to Debug would
// leave no surface at all from which to recover a config fault's age.
func TestPacedRaise_CarriesTheArrivalTheLatchCannotKeep(t *testing.T) {
	t.Parallel()

	const key = "gapConfig:t1.missing_x"
	const sev, code = "warning", "UnresolvedReference"
	start := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

	t.Run("a continuously re-derived fault keeps its first arrival", func(t *testing.T) {
		c := newIssueCache()
		_, arrived := c.pacedRaise(key, sev, code, start)
		if !arrived.Equal(start) {
			t.Fatalf("the arrival is the first raise, got %v want %v", arrived, start)
		}
		// Two hours of sweep passes, with another entity's close emptying the
		// latch in the middle of them.
		for minute := 1; minute <= 120; minute++ {
			at := start.Add(time.Duration(minute) * time.Minute)
			if minute == 30 {
				c.clear(key)
			}
			_, arrived = c.pacedRaise(key, sev, code, at)
			if !arrived.Equal(start) {
				t.Fatalf("minute %d reported the arrival as %v; a fault re-derived every pass never stopped "+
					"holding and keeps its first arrival %v", minute, arrived, start)
			}
		}
	})

	t.Run("a silence longer than one interval is a fresh arrival", func(t *testing.T) {
		c := newIssueCache()
		c.pacedRaise(key, sev, code, start)
		// Nothing re-derived this fault for longer than the pacing window, so
		// its return is a genuinely new one rather than a continuation. This is
		// what bounds how far the stamp can reach back.
		back := start.Add(logPaceInterval + time.Minute)
		loud, arrived := c.pacedRaise(key, sev, code, back)
		if !loud {
			t.Fatal("a fault returning after a silence longer than the interval must arrive loudly")
		}
		if !arrived.Equal(back) {
			t.Fatalf("arrival = %v, want the return instant %v", arrived, back)
		}
	})

	t.Run("a different fact at one key restamps the arrival", func(t *testing.T) {
		c := newIssueCache()
		c.pacedRaise(key, "warning", "UnresolvedReference", start)
		at := start.Add(time.Minute)
		_, arrived := c.pacedRaise(key, "error", "PlaybookConfigError", at)
		if !arrived.Equal(at) {
			t.Fatalf("arrival = %v, want %v: a different severity or code is a different fault, "+
				"and it did not arise when the previous one did", arrived, at)
		}
	})

	t.Run("a subject that left starts over", func(t *testing.T) {
		c := newIssueCache()
		c.pacedRaise(key, sev, code, start)
		c.clearPrefix("gapConfig:t1.")
		at := start.Add(time.Minute)
		_, arrived := c.pacedRaise(key, sev, code, at)
		if !arrived.Equal(at) {
			t.Fatalf("arrival = %v, want %v: a revoked target's return is a new fault, not a continuation",
				arrived, at)
		}
	})
}

// TestAlertPaced_DatesTheIssueFromThePaceEntry is the seam-level statement of
// the same fact: the Health entry an operator reads must keep its age across the
// clear, or damping the log would have made the fault both quiet AND undatable.
func TestAlertPaced_DatesTheIssueFromThePaceEntry(t *testing.T) {
	t.Parallel()

	const key = "gapConfig:t1.missing_x"
	logs := &logCapture{}
	e := &Engine{logger: slog.New(logs), issues: newIssueCache()}

	e.alertPaced(key, "warning", "UnresolvedReference", "arrival: ghostFlow does not resolve")
	first, ok := issueAt(e.issues, key)
	if !ok {
		t.Fatalf("the arrival must raise the issue, issues = %+v", e.issues.snapshot())
	}

	// Another entity's column stops being reported: clearClosedMarks empties the
	// target-scoped latch. Nothing about the playbook changed.
	e.issues.clear(key)
	e.alertPaced(key, "warning", "UnresolvedReference", "next pass: ghostFlow does not resolve")

	second, ok := issueAt(e.issues, key)
	if !ok {
		t.Fatalf("the pass after the clear must re-raise the issue, issues = %+v", e.issues.snapshot())
	}
	if second.Since != first.Since {
		t.Fatalf("since moved %q -> %q across a clear that was another entity's close; the fault never "+
			"stopped holding and its age must survive", first.Since, second.Since)
	}
	if lv := logs.levelsContaining("next pass:"); len(lv) != 1 || lv[0] != slog.LevelDebug {
		t.Fatalf("the pass after the clear logged %v, want exactly one Debug", lv)
	}
}
