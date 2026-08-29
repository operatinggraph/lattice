package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/processor"
)

// TestBuildEnqueueEnvelope_ForwardsEnumerations proves the browser's declared
// dispatch.enumerations (cmd/facet/web/app.js) reach the submitted envelope's
// ContextHint.Enumerations exactly — hub, relation, and direction all
// preserved — closing the gap where this hop silently dropped the field.
func TestBuildEnqueueEnvelope_ForwardsEnumerations(t *testing.T) {
	req := enqueueRequest{
		OperationType: "ResolveWorkOrder",
		Reads:         []string{"vtx.a.1"},
		Enumerations: []processor.EnumerationHint{
			{Hub: "vtx.identity.abc", Relation: "holdsRole", Direction: "out"},
		},
	}
	env := buildEnqueueEnvelope(req, "vtx.identity.actor", "req_1")
	if env.ContextHint == nil {
		t.Fatal("ContextHint = nil, want one built from Reads+Enumerations")
	}
	want := []processor.EnumerationHint{{Hub: "vtx.identity.abc", Relation: "holdsRole", Direction: "out"}}
	require.Equal(t, want, env.ContextHint.Enumerations)
}

// TestBuildEnqueueEnvelope_EnumerationsAloneStillBuildsContextHint proves a
// request declaring ONLY enumerations (no reads, no optionalReads) still gets
// a ContextHint — the guard condition must OR in Enumerations, not require it
// alongside Reads/OptionalReads.
func TestBuildEnqueueEnvelope_EnumerationsAloneStillBuildsContextHint(t *testing.T) {
	req := enqueueRequest{
		OperationType: "ResolveWorkOrder",
		Enumerations: []processor.EnumerationHint{
			{Hub: "vtx.identity.abc", Relation: "holdsRole", Direction: "out"},
		},
	}
	env := buildEnqueueEnvelope(req, "vtx.identity.actor", "req_2")
	if env.ContextHint == nil {
		t.Fatal("ContextHint = nil, want one built from Enumerations alone")
	}
	require.Empty(t, env.ContextHint.Reads)
	require.Empty(t, env.ContextHint.OptionalReads)
	want := []processor.EnumerationHint{{Hub: "vtx.identity.abc", Relation: "holdsRole", Direction: "out"}}
	require.Equal(t, want, env.ContextHint.Enumerations)
}

// TestBuildEnqueueEnvelope_NoDeclarationsBuildsNoContextHint pins the
// unchanged floor: a request declaring none of Reads/OptionalReads/
// Enumerations gets no ContextHint at all.
func TestBuildEnqueueEnvelope_NoDeclarationsBuildsNoContextHint(t *testing.T) {
	env := buildEnqueueEnvelope(enqueueRequest{OperationType: "Ping"}, "vtx.identity.actor", "req_3")
	if env.ContextHint != nil {
		t.Fatalf("ContextHint = %+v, want nil for a request with no declared reads", env.ContextHint)
	}
}
