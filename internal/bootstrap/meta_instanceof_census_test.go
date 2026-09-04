package bootstrap

// The kernel half of the census behind the step-6 governing-DDL walk's
// meta-vertex short-circuit. The Processor skips the instanceOf chain walk for
// a vtx.meta.<id> key under the committed-only disposition because a
// meta-vertex is typed by the kernel and carries no instanceOf edge — a premise
// that, until this file, rested on a comment. The package half is
// TestPackages_NeverEmitAMetaRootedInstanceOfLink in internal/pkgmgr.

import (
	"strings"
	"testing"
)

func TestPrimordialEntries_NeverEmitAMetaRootedInstanceOfLink(t *testing.T) {
	populateForTest(t)

	entries, err := buildPrimordialEntries()
	if err != nil {
		t.Fatalf("buildPrimordialEntries: %v", err)
	}

	links, metaRooted := 0, 0
	for _, e := range entries {
		if !strings.HasPrefix(e.key, "lnk.") {
			continue
		}
		links++
		seg := strings.Split(e.key, ".")
		if len(seg) == 6 && seg[1] == "meta" {
			metaRooted++
			if seg[3] == "instanceOf" {
				t.Errorf("the seeder emits a meta-rooted instanceOf link %q — the step-6 committed-only walk short-circuits every vtx.meta root and would never see it", e.key)
			}
		}
	}

	// The seeder emits links at all — otherwise the loop above proves nothing.
	if links == 0 {
		t.Fatal("the primordial entries carry no link keys; this census read nothing")
	}
	// And it emits no meta-rooted link of ANY relation, which is the stronger
	// fact the short-circuit's comment states. Asserted rather than assumed: if
	// the kernel ever seeds one, the relation is what has to be checked, and
	// this line is where that changes.
	if metaRooted != 0 {
		t.Errorf("the seeder emits %d meta-rooted link(s); the short-circuit's comment says it emits none", metaRooted)
	}
}
