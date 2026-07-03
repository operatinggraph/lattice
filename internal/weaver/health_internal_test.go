package weaver

import (
	"testing"
	"time"
)

// aggregateStatus must reconcile the lifecycle status with the open issue set
// per Contract #5 §5.3: a heartbeat is "healthy" only when issues is empty; an
// open warning ⇒ "degraded"; an open error ⇒ "unhealthy" (worst-wins). The
// "starting" / "shutdown" lifecycle phases are reported verbatim regardless of
// transient issues.
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
		{"unknown severity ignored", "healthy", []healthIssue{{Severity: "info", Code: "X"}}, "healthy"},
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
