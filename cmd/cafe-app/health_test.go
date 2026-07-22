package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/operatinggraph/lattice/internal/healthkv"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func hasIssue(issues []healthkv.Issue, code string) bool {
	for _, iss := range issues {
		if iss.Code == code {
			return true
		}
	}
	return false
}

func TestHealthProbe_AdminActorUnconfigured(t *testing.T) {
	s := &server{logger: discardLogger()} // adminActor unset, conn nil
	snap := s.healthProbe(context.Background())
	if snap.Status != healthkv.StatusUnhealthy {
		t.Fatalf("status = %v, want unhealthy", snap.Status)
	}
	if !hasIssue(snap.Issues, "AdminActorUnconfigured") {
		t.Errorf("issues = %+v, want AdminActorUnconfigured", snap.Issues)
	}
	if !hasIssue(snap.Issues, "NatsUnreachable") {
		t.Errorf("issues = %+v, want NatsUnreachable (conn is nil)", snap.Issues)
	}
}

func TestHealthProbe_NoStaffTokenMinterIsDegradedNotUnhealthy(t *testing.T) {
	s := &server{
		logger:     discardLogger(),
		adminActor: "vtx.identity.abc.actor",
		devSigner:  nil,
	}
	snap := s.healthProbe(context.Background())
	found := false
	for _, iss := range snap.Issues {
		if iss.Code == "NoStaffTokenMinter" {
			found = true
			if iss.Severity != "warning" {
				t.Errorf("NoStaffTokenMinter severity = %q, want warning", iss.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want NoStaffTokenMinter", snap.Issues)
	}
}

func TestHealthProbe_AdminActorAndTokenMinterConfigured_OnlyNatsUnreachable(t *testing.T) {
	// conn == nil is unavoidable without a live NATS dial in this unit test;
	// every other dependency is configured, so NatsUnreachable should be the
	// ONLY issue reported.
	s := &server{
		logger:     discardLogger(),
		adminActor: "vtx.identity.abc.actor",
		devSigner:  &devSigner{},
	}
	snap := s.healthProbe(context.Background())
	if len(snap.Issues) != 1 || snap.Issues[0].Code != "NatsUnreachable" {
		t.Fatalf("issues = %+v, want exactly [NatsUnreachable]", snap.Issues)
	}
	if snap.Status != healthkv.StatusUnhealthy {
		t.Fatalf("status = %v, want unhealthy (NatsUnreachable is error-severity)", snap.Status)
	}
}
