package main

import (
	"reflect"
	"regexp"
	"strings"
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
//
// The set of read shapes is CLOSED, not a regex sample: every occurrence of
// the cache identifier, the loaders and the promise must be classified as a
// known non-read or as a read this test extracts a name from. An occurrence
// the classifier does not recognise — optional chaining, destructuring, a
// variable index, a loader whose result is bound — fails loudly rather than
// passing as "not a read"; the author extends the classifier and the literal
// together.
func TestKnownCatalogOpsCoversEveryCacheRead(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	// Comment lines are dropped before scanning: the prose beside the
	// literal names the read shapes it asks authors to grep for, and a
	// placeholder like `opCatalogCache.X` in it is not a read.
	app := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(src), "")

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

	reads := map[string]bool{}
	// nonReadLines match the whole (trimmed) line — the declaration, the
	// assignment, the return — so a destructuring `const { X } = cache;` does
	// not pass as the return it ends like; nonReadTails and readShapes match
	// from the occurrence onward, since one line can carry a guard and a read.
	classify := func(ident string, nonReadLines, nonReadTails, readShapes []*regexp.Regexp) {
		t.Helper()
		lines := strings.Split(app, "\n")
		occurrence := regexp.MustCompile(`\b` + ident + `\b`)
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			for _, loc := range occurrence.FindAllStringIndex(line, -1) {
				tail := line[loc[0]:]
				matched := false
				for _, re := range nonReadLines {
					if re.MatchString(trimmed) {
						matched = true
						break
					}
				}
				for _, re := range nonReadTails {
					if re.MatchString(tail) {
						matched = true
						break
					}
				}
				for _, re := range readShapes {
					if m := re.FindStringSubmatch(tail); m != nil {
						reads[m[1]] = true
						matched = true
						break
					}
				}
				if !matched {
					t.Errorf("app.js:%d: unclassified `%s` reference — %q — extend this test's classifier (and KNOWN_CATALOG_OPS if it is a read)", i+1, ident, strings.TrimSpace(line))
				}
			}
		}
	}

	// The cache identifier: its declaration, the assignment and return in
	// loadOpCatalog, and the truthiness guards are the non-reads; a dotted
	// property or a bracketed string is a read.
	classify("opCatalogCache",
		[]*regexp.Regexp{
			regexp.MustCompile(`^let opCatalogCache = null;$`),
			regexp.MustCompile(`^opCatalogCache = await opCatalogPromise;$`),
			regexp.MustCompile(`^return opCatalogCache;$`),
		},
		[]*regexp.Regexp{
			regexp.MustCompile(`^opCatalogCache && `),
		},
		[]*regexp.Regexp{
			regexp.MustCompile(`^opCatalogCache\.([A-Z][A-Za-z0-9_]*)`),
			regexp.MustCompile(`^opCatalogCache\["([A-Z][A-Za-z0-9_]*)"\]`),
		})

	// The loaders and the promise are the other way to reach the catalog
	// without naming the cache: `loadOpCatalog().then(c => c.X)` is a read the
	// identifier scan above never sees. Their closed set is the definitions,
	// the promise plumbing, and value-discarding `await` statements — a call
	// whose result is bound or chained is unclassified.
	classify("loadOpCatalog",
		[]*regexp.Regexp{
			regexp.MustCompile(`^async function loadOpCatalog\(\) \{$`),
			regexp.MustCompile(`^await loadOpCatalog\(\);$`),
		}, nil, nil)
	classify("loadOpCatalogQuiet",
		[]*regexp.Regexp{
			regexp.MustCompile(`^async function loadOpCatalogQuiet\(\) \{$`),
			regexp.MustCompile(`^(?:await )?loadOpCatalogQuiet\(\);$`),
		}, nil, nil)
	classify("opCatalogPromise",
		[]*regexp.Regexp{
			regexp.MustCompile(`^let opCatalogPromise = null;$`),
			regexp.MustCompile(`^if \(!opCatalogPromise\) \{$`),
			regexp.MustCompile(`^opCatalogPromise = appGet\(`),
			regexp.MustCompile(`^opCatalogPromise = null;$`),
			regexp.MustCompile(`^opCatalogCache = await opCatalogPromise;$`),
		}, nil, nil)

	if len(reads) == 0 {
		t.Fatal("app.js: no opCatalogCache reads found — the classifier no longer matches this file")
	}
	for op := range reads {
		if !declared[op] {
			t.Errorf("app.js reads opCatalogCache.%s but KNOWN_CATALOG_OPS omits it: the descriptor is never fetched and the form silently reports itself unavailable", op)
		}
	}
}
