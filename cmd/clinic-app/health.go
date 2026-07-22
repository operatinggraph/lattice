package main

import (
	"context"
	"time"

	"github.com/operatinggraph/lattice/internal/healthkv"
)

// healthProbe re-checks clinic-app's own dependencies each tick — the admin
// actor, NATS, the protected read-model pool, and the read-auth posture — so
// a heartbeat can never merely echo a boot-time snapshot (mirrors
// loftspace-app's probe; see cmd/loftspace-app/health.go for the incident
// this pattern exists to surface).
func (s *server) healthProbe(ctx context.Context) healthkv.Snapshot {
	var issues []healthkv.Issue

	if s.adminActor == "" {
		issues = append(issues, healthkv.Issue{
			Code:     "AdminActorUnconfigured",
			Severity: "error",
			Message:  "bootstrap.json not loaded (version mismatch?); booking/cancel/create will 400",
		})
	}
	if s.conn == nil || !s.conn.NATS().IsConnected() {
		issues = append(issues, healthkv.Issue{
			Code:     "NatsUnreachable",
			Severity: "error",
			Message:  "NATS connection is down; every /api/* write and read will fail",
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
				Message:  "protected clinicAppointmentsRead Postgres pool unreachable; protected endpoints will 502",
			})
		}
	}
	if s.authn == nil {
		issues = append(issues, healthkv.Issue{
			Code:     "NoAuthPosture",
			Severity: "warning",
			Message:  "no read-auth posture configured; protected reads (/api/my-appointments etc.) will 401",
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
