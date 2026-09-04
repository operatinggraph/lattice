package bootstrap

// The kernel half of the census behind the step-6 governing-DDL walk's
// kernel-typed short-circuit. The Processor skips the instanceOf chain walk for
// a vtx.meta / vtx.permission / vtx.role / vtx.roleindex key under the
// committed-only disposition because each of those is typed by the kernel and
// carries no instanceOf edge — a premise that, until this file, rested on a
// comment. The package half is
// TestPackages_NeverEmitAMetaRootedInstanceOfLink in internal/pkgmgr.

import (
	"strings"
	"testing"
)

// kernelSourcedTypes mirrors the Processor's own kernelVertexTypes
// (internal/processor/step6_resolve_ddl.go). A link SOURCED at one of these
// types is what the short-circuit would never see.
var kernelSourcedTypes = map[string]bool{
	"meta":       true,
	"permission": true,
	"role":       true,
	"roleindex":  true,
}

func TestPrimordialEntries_NeverEmitAMetaRootedInstanceOfLink(t *testing.T) {
	populateForTest(t)

	entries, err := buildPrimordialEntries()
	if err != nil {
		t.Fatalf("buildPrimordialEntries: %v", err)
	}

	links, metaRooted, kernelRooted := 0, 0, 0
	for _, e := range entries {
		if !strings.HasPrefix(e.key, "lnk.") {
			continue
		}
		links++
		seg := strings.Split(e.key, ".")
		if len(seg) != 6 {
			continue
		}
		if !kernelSourcedTypes[seg[1]] {
			continue
		}
		kernelRooted++
		if seg[1] == "meta" {
			metaRooted++
		}
		if seg[3] == "instanceOf" {
			t.Errorf("the seeder emits an instanceOf link sourced at a kernel-typed vertex, %q — the step-6 committed-only walk short-circuits every vtx.%s root and would never see it", e.key, seg[1])
		}
	}

	// The seeder emits links at all — otherwise the loop above proves nothing.
	if links == 0 {
		t.Fatal("the primordial entries carry no link keys; this census read nothing")
	}
	// And it really does emit links sourced at a kernel-typed vertex (the six
	// grantedBy edges are sourced at vtx.permission), so "none of them is
	// instanceOf" discriminates a relation rather than reporting an empty loop.
	if kernelRooted == 0 {
		t.Fatal("the census saw no link sourced at a kernel-typed vertex at all — the shape it discriminates within is absent, so its verdict is vacuous")
	}
	// And it emits no meta-rooted link of ANY relation, which is the stronger
	// fact the short-circuit's comment states. Asserted rather than assumed: if
	// the kernel ever seeds one, the relation is what has to be checked, and
	// this line is where that changes.
	if metaRooted != 0 {
		t.Errorf("the seeder emits %d meta-rooted link(s); the short-circuit's comment says it emits none", metaRooted)
	}
}
