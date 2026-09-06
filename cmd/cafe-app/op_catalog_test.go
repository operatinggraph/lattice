package main

import (
	"reflect"
	"regexp"
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

// TestKnownCatalogOpsCoversEveryCacheRead closes the silent-failure gap
// between app.js's KNOWN_CATALOG_OPS literal and the reads it feeds. The
// literal is what the client sends as `?types=`, so an op missing from it is
// never fetched, `opCatalogCache.<Op>` is forever undefined, and the form
// that depends on it reports itself unavailable — with no error anywhere, in
// the browser or the build. Reading the embedded script and demanding every
// cache read name an entry in the literal turns that into a compile-time
// failure the moment a new descriptor-driven form lands.
func TestKnownCatalogOpsCoversEveryCacheRead(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	app := string(src)

	literal := regexp.MustCompile(`(?s)const KNOWN_CATALOG_OPS = \[(.*?)\];`).FindStringSubmatch(app)
	if literal == nil {
		t.Fatal("app.js: no `const KNOWN_CATALOG_OPS = [...]` literal found")
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([A-Za-z0-9_]+)"`).FindAllStringSubmatch(literal[1], -1) {
		declared[m[1]] = true
	}
	if len(declared) == 0 {
		t.Fatalf("KNOWN_CATALOG_OPS parsed empty from %q", literal[1])
	}

	// Both read shapes the cache is consulted through: the dotted property
	// and the bracketed string index. The declaration itself
	// (`let opCatalogCache = null`) and the assignment in loadOpCatalog
	// carry neither, so they need no exclusion.
	reads := map[string]bool{}
	for _, m := range regexp.MustCompile(`opCatalogCache\.([A-Za-z0-9_]+)`).FindAllStringSubmatch(app, -1) {
		reads[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`opCatalogCache\["([A-Za-z0-9_]+)"\]`).FindAllStringSubmatch(app, -1) {
		reads[m[1]] = true
	}
	if len(reads) == 0 {
		t.Fatal("app.js: no opCatalogCache reads found — the regexes no longer match this file")
	}

	for op := range reads {
		if !declared[op] {
			t.Errorf("app.js reads opCatalogCache.%s but KNOWN_CATALOG_OPS omits it: the descriptor is never fetched and the form silently reports itself unavailable", op)
		}
	}
}
