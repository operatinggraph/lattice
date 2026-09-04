package pkgmgr_test

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// censusAbstractMetaID stands in for a resolved abstract DDL meta-vertex so the
// taxonomy's subtypeOf links are emitted. Any valid 20-char NanoID does: the
// census reads the link's shape, never its target.
const censusAbstractMetaID = "Qb7kTn2wZr9dGm4sXp1v"

// metaRootedInstanceOf reports whether key is a link sourced at a meta-vertex
// whose relation is instanceOf.
//
// The test is on the raw segments, not on a successful ParseLinkKey: a key the
// parser rejects (a malformed NanoID, say) must still be counted, or the census
// would quietly excuse exactly the shapes it exists to find.
func metaRootedInstanceOf(key string) bool {
	seg := strings.Split(key, ".")
	return len(seg) == 6 && seg[0] == "lnk" && seg[1] == "meta" && seg[3] == "instanceOf"
}

// The step-6 governing-DDL walk short-circuits a meta-rooted key under the
// committed-only disposition (resolveGoverningDDL's resolveWithFault): a
// vtx.meta.<id> is typed by the kernel, not by an instanceOf edge, so the walk
// could only ever come back empty while a package-uninstall or meta-vertex
// tombstone cascade — bulk, and exclusively meta keys — paid one Core KV read
// per root for the privilege.
//
// That premise rested on a comment. This is the closed-set census that holds
// it: EVERY registered package's install batch, built through the same builder
// the installer runs, must emit no meta-rooted instanceOf link. The kernel half
// of the corpus is pinned by
// TestPrimordialEntries_NeverEmitAMetaRootedInstanceOfLink in
// internal/bootstrap, where the seeder's entries are in-package.
//
// A package that ever needed one would have to move the short-circuit first.
func TestPackages_NeverEmitAMetaRootedInstanceOfLink(t *testing.T) {
	names := pkgregistry.Names()
	if len(names) == 0 {
		t.Fatal("pkgregistry.Names() is empty — the census would pass over nothing")
	}
	linksSeen, metaRootedSeen := 0, 0
	for _, name := range names {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			t.Fatalf("pkgregistry.Names() returned %q but Lookup does not know it", name)
		}
		// The SHIPPED definition: a read-grant lens's installed body is the
		// composed walk chain, not the presentation tail its package declared.
		expanded, err := def.ExpandReadGrantWalks()
		if err != nil {
			t.Fatalf("%s: ExpandReadGrantWalks: %v", name, err)
		}
		// Every DDL index is handed a resolved abstract meta-vertex, so the
		// taxonomy's `subtypeOf` emission — the other meta-rooted link shape the
		// installer builds, and the one a nil map silently skips — is inside the
		// census rather than beside it.
		subtypes := make(map[int]string, len(expanded.DDLs))
		for idx := range expanded.DDLs {
			subtypes[idx] = censusAbstractMetaID
		}
		muts, _, err := pkgmgr.BuildInstallBatchWithSubtypesForTest(expanded, subtypes)
		if err != nil {
			t.Fatalf("%s: BuildInstallBatchWithSubtypesForTest: %v", name, err)
		}
		for _, m := range muts {
			if !strings.HasPrefix(m.Key, "lnk.") {
				continue
			}
			linksSeen++
			if strings.HasPrefix(m.Key, "lnk.meta.") {
				metaRootedSeen++
			}
			if metaRootedInstanceOf(m.Key) {
				t.Errorf("%s emits a meta-rooted instanceOf link %q — the step-6 committed-only walk short-circuits every vtx.meta root and would never see it", name, m.Key)
			}
		}
	}
	// The positive vectors: the census really did walk link keys, and the corpus
	// really does emit meta-ROOTED links (subtypeOf, offeredTo) — so "none of
	// them is instanceOf" discriminates a relation rather than reporting an
	// empty loop.
	if linksSeen == 0 {
		t.Fatal("the corpus census saw no link mutations at all — the install batch builder is not emitting what this test reads")
	}
	if metaRootedSeen == 0 {
		t.Fatal("the corpus census saw no meta-rooted links at all — the shape this test discriminates within is absent, so its verdict is vacuous")
	}
}
