package loom

import (
	"testing"
	"time"
)

// TestAggregateStatus locks the Contract #5 §5.2/§5.3 reconciliation: a heartbeat
// carrying issues can never self-report "healthy", lifecycle phases pass through,
// and error wins over warning. Mirrors the Processor/Weaver/Refractor heartbeaters.
func TestAggregateStatus(t *testing.T) {
	t.Parallel()
	warn := healthIssue{Severity: "warning", Code: "ConsumerPaused", Message: "x"}
	errIssue := healthIssue{Severity: "error", Code: "Boom", Message: "y"}

	cases := []struct {
		name      string
		lifecycle string
		issues    []healthIssue
		want      string
	}{
		{"healthy no issues stays healthy", "healthy", nil, "healthy"},
		{"healthy with warning degrades", "healthy", []healthIssue{warn}, "degraded"},
		{"healthy with error is unhealthy", "healthy", []healthIssue{errIssue}, "unhealthy"},
		{"error wins over warning", "healthy", []healthIssue{warn, errIssue}, "unhealthy"},
		{"starting passes through despite issues", "starting", []healthIssue{warn, errIssue}, "starting"},
		{"shutdown passes through despite issues", "shutdown", []healthIssue{errIssue}, "shutdown"},
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
// defaults to healthkv.DefaultTTLMultiplier, and 0 disables it. Real NATS
// expiry mechanics are proven once at the substrate layer and by the
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
