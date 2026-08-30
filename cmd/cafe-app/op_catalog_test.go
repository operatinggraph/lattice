package main

import (
	"reflect"
	"testing"
)

// TestComputeOpCatalog_ReNestsTheDescriptorVocabulary pins the descriptor
// round trip for this app's own op_catalog.go (identical to
// cmd/loftspace-app's own copy) — a compact fixture, not the full loftspace
// suite, since the flattening logic is shared verbatim; this proves cafe-app
// wires its own toDescriptor the same way rather than assuming it by mirror.
// In particular, deleting `Enumerations: p.DispatchEnumerations` from this
// app's toDescriptor would leave the whole tree green without this test.
func TestComputeOpCatalog_ReNestsTheDescriptorVocabulary(t *testing.T) {
	entries := map[string]any{
		"vtx.meta.wo": map[string]any{
			"operationType":         "ResolveWorkOrder",
			"dispatchClass":         "workOrder",
			"dispatchReads":         []string{"{payload.workOrderKey}"},
			"dispatchOptionalReads": []string{"{payload.workOrderKey}.resolution"},
			"dispatchEnumerations": []map[string]any{
				{"hub": "{actor}", "relation": "holdsRole", "direction": "out"},
			},
		},
	}
	keys, get := fakeKV(entries)
	got := computeOpCatalog(keys, get)
	d, ok := got["ResolveWorkOrder"]
	if !ok {
		t.Fatalf("want a ResolveWorkOrder descriptor, got %+v", got)
	}
	if d.Dispatch == nil {
		t.Fatal("dispatch: nil")
	}
	want := opEnumeration{Hub: "{actor}", Relation: "holdsRole", Direction: "out"}
	if len(d.Dispatch.Enumerations) != 1 || d.Dispatch.Enumerations[0] != want {
		t.Errorf("enumerations: got %+v, want [%+v]", d.Dispatch.Enumerations, want)
	}
}

// TestOpCatalogKeysFromTypesParam pins the two outcomes handleOpCatalog's
// `?types=` branch depends on: absent/empty must come back nil (so the
// `keys == nil` check falls back to KVListKeys and the full catalog still
// works), and a comma list must split into exactly those keys with no
// trimming or dedup — a caller that gets this wrong either serves the whole
// bucket when it meant to narrow, or silently drops a wanted op.
func TestOpCatalogKeysFromTypesParam(t *testing.T) {
	if got := opCatalogKeysFromTypesParam(""); got != nil {
		t.Errorf("empty types: got %#v, want nil", got)
	}
	got := opCatalogKeysFromTypesParam("VoidCharge,CreditCafeAccount")
	want := []string{"VoidCharge", "CreditCafeAccount"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}
