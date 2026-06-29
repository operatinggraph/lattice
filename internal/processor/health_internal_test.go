package processor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// fakeBacklogReader satisfies LaneBacklogReader for the heartbeat lane_lag tests:
// PendingForConsumer returns the configured pending count, or err when set.
type fakeBacklogReader struct {
	pending uint64
	err     error
}

func (f fakeBacklogReader) PendingForConsumer(context.Context, string) (uint64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.pending, nil
}

func newTestHeartbeater() *HealthHeartbeater {
	return NewHealthHeartbeater(nil, "health", "proc-test", 10*time.Second, &Metrics{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// aggregateStatus reconciles the lifecycle phase with the open issue set:
// "starting"/"shuttingDown" pass through; otherwise any error ⇒ unhealthy, any
// warning ⇒ degraded, else the lifecycle status is kept (Contract #5 §5.3).
func TestAggregateStatus(t *testing.T) {
	warn := []healthIssue{{Severity: "warning", Code: "ProcessorLaneLagging"}}
	errIss := []healthIssue{{Severity: "error", Code: "CoreKVUnwritable"}}
	both := []healthIssue{{Severity: "warning"}, {Severity: "error"}}

	cases := []struct {
		name      string
		lifecycle string
		issues    []healthIssue
		want      string
	}{
		{"healthy no issues", "healthy", nil, "healthy"},
		{"healthy with warning", "healthy", warn, "degraded"},
		{"healthy with error", "healthy", errIss, "unhealthy"},
		{"error wins over warning", "healthy", both, "unhealthy"},
		{"starting protected from warning", "starting", warn, "starting"},
		{"starting protected from error", "starting", errIss, "starting"},
		{"shuttingDown protected", "shuttingDown", errIss, "shuttingDown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregateStatus(tc.lifecycle, tc.issues); got != tc.want {
				t.Fatalf("aggregateStatus(%q, %v) = %q, want %q", tc.lifecycle, tc.issues, got, tc.want)
			}
		})
	}
}

// reconcileIssues must carry a code's since timestamp across heartbeats while it
// stays open and drop it once it resolves (Contract #5 §5.5).
func TestReconcileIssuesSincePersistence(t *testing.T) {
	h := newTestHeartbeater()
	t1 := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(30 * time.Second)

	active := map[string]activeIssue{"ProcessorLaneLagging": {severity: "warning", message: "lagging"}}

	first := h.reconcileIssues(active, t1)
	if len(first) != 1 {
		t.Fatalf("first tick: got %d issues, want 1", len(first))
	}
	since := first[0].Since

	second := h.reconcileIssues(active, t2)
	if len(second) != 1 {
		t.Fatalf("second tick: got %d issues, want 1", len(second))
	}
	if second[0].Since != since {
		t.Fatalf("since not persisted: first %q, second %q", since, second[0].Since)
	}

	resolved := h.reconcileIssues(map[string]activeIssue{}, t2.Add(10*time.Second))
	if len(resolved) != 0 {
		t.Fatalf("resolved tick: got %d issues, want 0", len(resolved))
	}
	if _, ok := h.openIssues["ProcessorLaneLagging"]; ok {
		t.Fatalf("resolved code still tracked in openIssues")
	}

	// A re-occurrence after resolve must get a fresh since, not the stale one.
	reopened := h.reconcileIssues(active, t2.Add(time.Minute))
	if reopened[0].Since == since {
		t.Fatalf("reopened issue reused stale since %q", since)
	}
}

func laneLagTotal(t *testing.T, doc HealthDoc) any {
	t.Helper()
	v, ok := doc.Metrics["lane_lag_total"]
	if !ok {
		t.Fatalf("metrics missing lane_lag_total")
	}
	// Per-lane lane_lag keys must always be present and nil (never fabricated).
	lanes, ok := doc.Metrics["lane_lag"].(map[string]any)
	if !ok {
		t.Fatalf("lane_lag not a map: %T", doc.Metrics["lane_lag"])
	}
	for _, lane := range []string{"default", "meta", "urgent", "system"} {
		if val, present := lanes[lane]; !present || val != nil {
			t.Fatalf("lane_lag[%q] = %v, want nil (per-lane not measurable)", lane, val)
		}
	}
	return v
}

// buildHealthDoc reports the real consumer backlog as lane_lag_total, raises
// ProcessorLaneLagging (degraded) past the threshold, and reports null rather
// than a fabricated zero when the backlog can't be read.
func TestBuildHealthDocLaneLag(t *testing.T) {
	ctx := context.Background()

	t.Run("no consumer attached → null total, healthy", func(t *testing.T) {
		h := newTestHeartbeater()
		doc := h.buildHealthDoc(ctx, "healthy", time.Now())
		if total := laneLagTotal(t, doc); total != nil {
			t.Fatalf("lane_lag_total = %v, want nil", total)
		}
		if doc.Status != "healthy" || len(doc.Issues) != 0 {
			t.Fatalf("status=%q issues=%d, want healthy/0", doc.Status, len(doc.Issues))
		}
	})

	t.Run("below threshold → total reported, healthy", func(t *testing.T) {
		h := newTestHeartbeater()
		h.AttachBacklogReader(fakeBacklogReader{pending: 5}, "processor-main")
		doc := h.buildHealthDoc(ctx, "healthy", time.Now())
		if total := laneLagTotal(t, doc); total != uint64(5) {
			t.Fatalf("lane_lag_total = %v, want 5", total)
		}
		if doc.Status != "healthy" || len(doc.Issues) != 0 {
			t.Fatalf("status=%q issues=%d, want healthy/0", doc.Status, len(doc.Issues))
		}
	})

	t.Run("above threshold → lagging warning, degraded", func(t *testing.T) {
		h := newTestHeartbeater()
		h.AttachBacklogReader(fakeBacklogReader{pending: 250}, "processor-main")
		doc := h.buildHealthDoc(ctx, "healthy", time.Now())
		if total := laneLagTotal(t, doc); total != uint64(250) {
			t.Fatalf("lane_lag_total = %v, want 250", total)
		}
		if doc.Status != "degraded" {
			t.Fatalf("status = %q, want degraded", doc.Status)
		}
		if len(doc.Issues) != 1 || doc.Issues[0].Code != "ProcessorLaneLagging" || doc.Issues[0].Severity != "warning" {
			t.Fatalf("issues = %+v, want one ProcessorLaneLagging warning", doc.Issues)
		}
		if doc.Issues[0].Since == "" {
			t.Fatalf("issue missing since timestamp")
		}
	})

	t.Run("info error → null total, no false-healthy fabrication", func(t *testing.T) {
		h := newTestHeartbeater()
		h.AttachBacklogReader(fakeBacklogReader{err: errors.New("server unreachable")}, "processor-main")
		doc := h.buildHealthDoc(ctx, "healthy", time.Now())
		if total := laneLagTotal(t, doc); total != nil {
			t.Fatalf("lane_lag_total = %v, want nil on Info error", total)
		}
		if doc.Status != "healthy" || len(doc.Issues) != 0 {
			t.Fatalf("status=%q issues=%d, want healthy/0 (can't assess)", doc.Status, len(doc.Issues))
		}
	})

	t.Run("starting lifecycle protected even when lagging", func(t *testing.T) {
		h := newTestHeartbeater()
		h.AttachBacklogReader(fakeBacklogReader{pending: 250}, "processor-main")
		doc := h.buildHealthDoc(ctx, "starting", time.Now())
		if doc.Status != "starting" {
			t.Fatalf("status = %q, want starting (protected)", doc.Status)
		}
	})

	t.Run("custom threshold via SetLagThreshold", func(t *testing.T) {
		h := newTestHeartbeater()
		h.SetLagThreshold(10)
		h.AttachBacklogReader(fakeBacklogReader{pending: 20}, "processor-main")
		doc := h.buildHealthDoc(ctx, "healthy", time.Now())
		if doc.Status != "degraded" {
			t.Fatalf("status = %q, want degraded at custom threshold 10/pending 20", doc.Status)
		}
	})
}

// emitted issues must marshal as a JSON array (never null) so a §5.5 reader sees
// an empty list when healthy.
func TestHealthDocIssuesMarshalAsArray(t *testing.T) {
	h := newTestHeartbeater()
	doc := h.buildHealthDoc(context.Background(), "healthy", time.Now())
	if doc.Issues == nil {
		t.Fatalf("Issues is nil; must be an empty slice to marshal as []")
	}
}
