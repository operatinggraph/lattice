package bootstrap_test

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// requireBootstrapActorKeys sets the package-level actor/op-tracker keys
// NewDocumentEnvelopeAt requires to be non-empty (it panics otherwise).
// toMap's own success/error branches don't depend on real primordial IDs.
func requireBootstrapActorKeys(t *testing.T) {
	t.Helper()
	bootstrap.BootstrapIdentityKey = "vtx.identity.testActor00000000000"
	bootstrap.BootstrapOpKey = "vtx.op.testOpTracker000000000000"
}

// TestMakeVertexEnvelope_MapData exercises toMap's fast path: a
// map[string]any is used as-is, with no marshal/unmarshal round-trip.
func TestMakeVertexEnvelope_MapData(t *testing.T) {
	requireBootstrapActorKeys(t)
	raw, err := bootstrap.MakeVertexEnvelope("vtx.test.k1", "test", map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("MakeVertexEnvelope: unexpected error: %v", err)
	}
	var env substrate.DocumentEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if env.Data["foo"] != "bar" {
		t.Fatalf("Data[foo] = %v, want bar", env.Data["foo"])
	}
}

// TestMakeVertexEnvelope_StructData exercises toMap's round-trip path: an
// arbitrary struct is marshaled then unmarshaled into map[string]any.
func TestMakeVertexEnvelope_StructData(t *testing.T) {
	requireBootstrapActorKeys(t)
	type payload struct {
		Foo string `json:"foo"`
	}
	raw, err := bootstrap.MakeVertexEnvelope("vtx.test.k2", "test", payload{Foo: "bar"})
	if err != nil {
		t.Fatalf("MakeVertexEnvelope: unexpected error: %v", err)
	}
	var env substrate.DocumentEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if env.Data["foo"] != "bar" {
		t.Fatalf("Data[foo] = %v, want bar", env.Data["foo"])
	}
}

// TestMakeVertexEnvelope_NilData verifies the no-data path leaves Data nil
// without invoking toMap at all.
func TestMakeVertexEnvelope_NilData(t *testing.T) {
	requireBootstrapActorKeys(t)
	raw, err := bootstrap.MakeVertexEnvelope("vtx.test.k3", "test", nil)
	if err != nil {
		t.Fatalf("MakeVertexEnvelope: unexpected error: %v", err)
	}
	var env substrate.DocumentEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if env.Data != nil {
		t.Fatalf("Data = %v, want nil", env.Data)
	}
}

// TestMakeVertexEnvelope_UnmarshalableData verifies toMap's marshal error
// path: a value json.Marshal cannot encode (a channel) surfaces as an
// error instead of a panic or a silently empty Data map.
func TestMakeVertexEnvelope_UnmarshalableData(t *testing.T) {
	requireBootstrapActorKeys(t)
	_, err := bootstrap.MakeVertexEnvelope("vtx.test.k4", "test", make(chan int))
	if err == nil {
		t.Fatalf("MakeVertexEnvelope: expected marshal error for a channel value, got nil")
	}
}

// TestMakeVertexEnvelope_NonObjectData verifies toMap's unmarshal error
// path: a value that marshals to a JSON scalar (not an object) fails the
// map[string]any round-trip rather than silently dropping the data.
func TestMakeVertexEnvelope_NonObjectData(t *testing.T) {
	requireBootstrapActorKeys(t)
	_, err := bootstrap.MakeVertexEnvelope("vtx.test.k5", "test", "just a string")
	if err == nil {
		t.Fatalf("MakeVertexEnvelope: expected unmarshal error for a scalar value, got nil")
	}
}
