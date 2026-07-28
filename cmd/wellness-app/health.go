package main

import (
	"context"
	"time"

	"github.com/operatinggraph/lattice/internal/healthkv"
)

// healthProbe re-checks wellness-app's own dependencies each tick —
// bootstrap, NATS, the protected read-model pool, and the session auth
// posture — so a heartbeat can never merely echo a boot-time snapshot
// (mirrors cafe-app/loftspace-app/clinic-app's probe).
func (s *server) healthProbe(ctx context.Context) healthkv.Snapshot {
	var issues []healthkv.Issue

	if !s.bootstrapLoaded {
		issues = append(issues, healthkv.Issue{
			Code:     "BootstrapUnloaded",
			Severity: "error",
			Message:  "bootstrap.json not loaded (version mismatch?); platform-derived identifiers are unavailable",
		})
	}
	if s.conn == nil || !s.conn.NATS().IsConnected() {
		issues = append(issues, healthkv.Issue{
			Code:     "NatsUnreachable",
			Severity: "error",
			Message:  "NATS connection is down; every /api/* read will fail",
		})
	}
	if s.pgPool != nil {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := s.pgPool.Ping(pingCtx)
		cancel()
		if err != nil {
			issues = append(issues, healthkv.Issue{
				Code:     "ReadModelUnreachable",
				Severity: "warning",
				Message:  "protected wellnessIdentitiesRead Postgres pool unreachable; /api/identities will 502",
			})
		}
	}
	if s.authn == nil {
		issues = append(issues, healthkv.Issue{
			Code:     "NoAuthPosture",
			Severity: "warning",
			Message:  "no session auth posture configured (set WELLNESS_APP_DEV_AUTH, or WELLNESS_APP_JWT_PUBLIC_KEY + WELLNESS_APP_JWT_ISSUER); every gated /api/* request will 401",
		})
	}

	status := healthkv.StatusHealthy
	for _, iss := range issues {
		if iss.Severity == "error" {
			status = healthkv.StatusUnhealthy
			break
		}
		status = healthkv.StatusDegraded
	}

	return healthkv.Snapshot{Status: status, Issues: issues}
}
