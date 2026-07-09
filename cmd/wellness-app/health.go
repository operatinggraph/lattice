package main

import (
	"context"

	"github.com/asolgan/lattice/internal/healthkv"
)

// healthProbe re-checks wellness-app's own dependencies each tick — the admin
// actor, NATS, and the staff dev-token minter — so a heartbeat can never
// merely echo a boot-time snapshot (mirrors cafe-app/loftspace-app/clinic-app's
// probe).
func (s *server) healthProbe(ctx context.Context) healthkv.Snapshot {
	var issues []healthkv.Issue

	if s.adminActor == "" {
		issues = append(issues, healthkv.Issue{
			Code:     "AdminActorUnconfigured",
			Severity: "error",
			Message:  "bootstrap.json not loaded (version mismatch?); booking/cancelling will 400",
		})
	}
	if s.conn == nil || !s.conn.NATS().IsConnected() {
		issues = append(issues, healthkv.Issue{
			Code:     "NatsUnreachable",
			Severity: "error",
			Message:  "NATS connection is down; every /api/* read will fail",
		})
	}
	if s.devSigner == nil {
		issues = append(issues, healthkv.Issue{
			Code:     "NoStaffTokenMinter",
			Severity: "warning",
			Message:  "no staff dev-token minter configured (set WELLNESS_APP_DEV_AUTH); booking/cancelling writes cannot obtain a Bearer token",
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
