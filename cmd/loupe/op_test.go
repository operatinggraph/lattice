package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestBuildEnvelope(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	const actor = "vtx.identity.admin1"
	const reqID = "RequestIDexample0000"

	t.Run("full request", func(t *testing.T) {
		req := opRequest{
			OperationType: "CreateIdentity",
			Lane:          "meta",
			Class:         "identity",
			Payload:       json.RawMessage(`{"name":"x"}`),
		}
		env, err := buildEnvelope(req, reqID, actor, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if env.RequestID != reqID {
			t.Errorf("RequestID = %q", env.RequestID)
		}
		if env.Lane != processor.LaneMeta {
			t.Errorf("Lane = %q, want meta", env.Lane)
		}
		if env.OperationType != "CreateIdentity" {
			t.Errorf("OperationType = %q", env.OperationType)
		}
		if env.Actor != actor {
			t.Errorf("Actor = %q", env.Actor)
		}
		if env.Class != "identity" {
			t.Errorf("Class = %q", env.Class)
		}
		if env.SubmittedAt != "2026-06-18T12:00:00Z" {
			t.Errorf("SubmittedAt = %q", env.SubmittedAt)
		}
		if string(env.Payload) != `{"name":"x"}` {
			t.Errorf("Payload = %s", env.Payload)
		}
	})

	t.Run("lane defaults to default", func(t *testing.T) {
		env, err := buildEnvelope(opRequest{OperationType: "X"}, reqID, actor, now)
		if err != nil {
			t.Fatal(err)
		}
		if env.Lane != processor.LaneDefault {
			t.Errorf("Lane = %q, want default", env.Lane)
		}
	})

	t.Run("empty payload defaults to {}", func(t *testing.T) {
		env, err := buildEnvelope(opRequest{OperationType: "X"}, reqID, actor, now)
		if err != nil {
			t.Fatal(err)
		}
		if string(env.Payload) != "{}" {
			t.Errorf("Payload = %s, want {}", env.Payload)
		}
	})

	t.Run("missing operationType rejected", func(t *testing.T) {
		if _, err := buildEnvelope(opRequest{}, reqID, actor, now); err == nil {
			t.Fatal("expected error for missing operationType")
		}
	})

	t.Run("unknown lane rejected", func(t *testing.T) {
		req := opRequest{OperationType: "X", Lane: "fastlane"}
		if _, err := buildEnvelope(req, reqID, actor, now); err == nil {
			t.Fatal("expected error for unknown lane")
		}
	})

	t.Run("invalid payload JSON rejected", func(t *testing.T) {
		req := opRequest{OperationType: "X", Payload: json.RawMessage(`{not json`)}
		if _, err := buildEnvelope(req, reqID, actor, now); err == nil {
			t.Fatal("expected error for invalid payload JSON")
		}
	})

	t.Run("reads populate ContextHint (trimmed + deduped)", func(t *testing.T) {
		req := opRequest{
			OperationType: "TombstoneRole",
			Reads:         []string{"vtx.role.X", "  ", "vtx.role.X", "vtx.identity.Y"},
		}
		env, err := buildEnvelope(req, reqID, actor, now)
		if err != nil {
			t.Fatal(err)
		}
		if env.ContextHint == nil {
			t.Fatal("ContextHint is nil; reads were dropped")
		}
		got := env.ContextHint.Reads
		if len(got) != 2 || got[0] != "vtx.role.X" || got[1] != "vtx.identity.Y" {
			t.Errorf("reads = %v, want [vtx.role.X vtx.identity.Y]", got)
		}
	})

	t.Run("no reads leaves ContextHint nil", func(t *testing.T) {
		env, err := buildEnvelope(opRequest{OperationType: "X"}, reqID, actor, now)
		if err != nil {
			t.Fatal(err)
		}
		if env.ContextHint != nil {
			t.Errorf("ContextHint should be nil when no reads declared, got %+v", env.ContextHint)
		}
	})
}

// The envelope buildEnvelope produces must satisfy the Processor's own
// ParseEnvelope so the wire contract holds end-to-end.
func TestBuildEnvelopeRoundTripsThroughParseEnvelope(t *testing.T) {
	req := opRequest{OperationType: "CreateIdentity", Lane: "default", Payload: json.RawMessage(`{}`)}
	reqID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatal(err)
	}
	env, err := buildEnvelope(req, reqID, "vtx.identity.admin1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.ParseEnvelope(data); err != nil {
		t.Fatalf("ParseEnvelope rejected a Loupe-built envelope: %v", err)
	}
}
