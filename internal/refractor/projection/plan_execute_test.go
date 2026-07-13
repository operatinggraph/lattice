package projection_test

import (
	"context"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
)

func TestProjectionPlan_RequiresGuard(t *testing.T) {
	cases := []struct {
		name      string
		authPlane bool
		empty     string
		want      bool
	}{
		{"auth-plane always guards", true, string(projection.EmptySkip), true},
		{"delete tombstone guards off the auth plane", false, string(projection.EmptyDelete), true},
		{"softDelete tombstone guards off the auth plane", false, string(projection.EmptySoftDelete), true},
		{"skip on a non-auth-plane lens does not guard", false, string(projection.EmptySkip), false},
		{"emptyDoc on a non-auth-plane lens does not guard", false, string(projection.EmptyDoc), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := installRule(t, "my-tasks", tc.empty)
			plan, err := projection.Compile(r)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			plan.AuthPlane = tc.authPlane
			if got := plan.RequiresGuard(); got != tc.want {
				t.Fatalf("RequiresGuard() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExecutionPlan_Execute_DelegatesToTheFullEngine asserts the thin wrapper
// runs the exact per-actor cypher evaluation the live pipeline uses — a
// MATCH-less literal RETURN needs no KV, so nil adjKV/coreKV proves the
// delegation without a NATS fixture.
func TestExecutionPlan_Execute_DelegatesToTheFullEngine(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(`RETURN 1 AS x`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ep := projection.ExecutionPlan{
		Engine:       ruleengine.EngineFull,
		CompiledRule: cr,
		AnchorType:   "identity",
	}
	out, err := ep.Execute(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 row, got %d: %v", len(out), out)
	}
	if out[0].Values["x"] != int64(1) {
		t.Fatalf("expected x=1, got %v (%T)", out[0].Values["x"], out[0].Values["x"])
	}
}
