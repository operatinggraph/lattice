package main

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/healthkv"
)

func hasIssue(issues []healthkv.Issue, code string) bool {
	for _, iss := range issues {
		if iss.Code == code {
			return true
		}
	}
	return false
}

func TestHealthProbe_BootstrapUnloaded(t *testing.T) {
	s := &server{logger: discardLogger()} // bootstrapLoaded false (zero value), conn nil
	snap := s.healthProbe(context.Background())
	if snap.Status != healthkv.StatusUnhealthy {
		t.Fatalf("status = %v, want unhealthy", snap.Status)
	}
	if !hasIssue(snap.Issues, "BootstrapUnloaded") {
		t.Errorf("issues = %+v, want BootstrapUnloaded", snap.Issues)
	}
	if !hasIssue(snap.Issues, "NatsUnreachable") {
		t.Errorf("issues = %+v, want NatsUnreachable (conn is nil)", snap.Issues)
	}
}

func TestHealthProbe_NoAuthPostureIsDegradedNotUnhealthy(t *testing.T) {
	s := &server{
		logger:          discardLogger(),
		bootstrapLoaded: true,
		authn:           nil,
	}
	snap := s.healthProbe(context.Background())
	found := false
	for _, iss := range snap.Issues {
		if iss.Code == "NoAuthPosture" {
			found = true
			if iss.Severity != "warning" {
				t.Errorf("NoAuthPosture severity = %q, want warning", iss.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want NoAuthPosture", snap.Issues)
	}
}

func TestHealthProbe_BootstrapAndAuthConfigured_OnlyNatsUnreachable(t *testing.T) {
	// conn == nil is unavoidable without a live NATS dial in this unit test;
	// every other dependency is configured, so NatsUnreachable should be the
	// ONLY issue reported.
	s := &server{
		logger:          discardLogger(),
		bootstrapLoaded: true,
		authn:           &auth.Authenticator{},
	}
	snap := s.healthProbe(context.Background())
	if len(snap.Issues) != 1 || snap.Issues[0].Code != "NatsUnreachable" {
		t.Fatalf("issues = %+v, want exactly [NatsUnreachable]", snap.Issues)
	}
	if snap.Status != healthkv.StatusUnhealthy {
		t.Fatalf("status = %v, want unhealthy (NatsUnreachable is error-severity)", snap.Status)
	}
}
