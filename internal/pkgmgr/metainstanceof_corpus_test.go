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

// kernelSourcedTypes mirrors the Processor's own kernelVertexTypes
// (internal/processor/step6_resolve_ddl.go): the vertex types whose roots the
// committed-only governing-DDL walk short-circuits, and therefore the source
// types at which an instanceOf edge would be invisible to it.
var kernelSourcedTypes = map[string]bool{
	"meta":       true,
	"permission": true,
	"role":       true,
	"roleindex":  true,
}

// kernelSourced reports the source TYPE and relation of a link key sourced at a
// kernel-typed vertex; ok is false for every other key.
//
// The test is on the raw segments, not on a successful ParseLinkKey: a key the
// parser rejects (a malformed NanoID, say) must still be counted, or the census
// would quietly excuse exactly the shapes it exists to find.
func kernelSourced(key string) (sourceType, relation string, ok bool) {
	seg := strings.Split(key, ".")
	if len(seg) != 6 || seg[0] != "lnk" || !kernelSourcedTypes[seg[1]] {
		return "", "", false
	}
	return seg[1], seg[3], true
}

// The step-6 governing-DDL walk short-circuits a kernel-typed root under the
// committed-only disposition (resolveGoverningDDL's resolveWithFault): a
// vtx.meta, vtx.permission, vtx.role or vtx.roleindex vertex is typed by the
// kernel, not by an instanceOf edge, so the walk could only ever come back
// empty while a package-uninstall, package-upgrade or meta-vertex tombstone
// cascade — bulk, and made almost entirely of these types — paid one Core KV
// read per root for the privilege.
//
// That premise rested on a comment. This is the closed-set census that holds
// it: EVERY registered package's install batch, built through the same builder
// the installer runs, must emit no instanceOf link sourced at any of the four.
// The kernel half of the corpus is pinned by
// TestPrimordialEntries_NeverEmitAMetaRootedInstanceOfLink in
// internal/bootstrap, where the seeder's entries are in-package.
//
// A package that ever needed one would have to move the short-circuit first.
func TestPackages_NeverEmitAMetaRootedInstanceOfLink(t *testing.T) {
	names := pkgregistry.Names()
	if len(names) == 0 {
		t.Fatal("pkgregistry.Names() is empty — the census would pass over nothing")
	}
	linksSeen, metaRootedSeen, kernelSourcedSeen := 0, 0, 0
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
			sourceType, relation, kernel := kernelSourced(m.Key)
			if !kernel {
				continue
			}
			kernelSourcedSeen++
			if relation == "instanceOf" {
				t.Errorf("%s emits an instanceOf link sourced at a kernel-typed vertex, %q — the step-6 committed-only walk short-circuits every vtx.%s root and would never see it", name, m.Key, sourceType)
			}
		}
	}
	// The positive vectors: the census really did walk link keys, the corpus
	// really does emit meta-ROOTED links (subtypeOf, offeredTo), and it really
	// does emit links sourced at a kernel-typed vertex — so "none of them is
	// instanceOf" discriminates a relation rather than reporting an empty loop.
	if linksSeen == 0 {
		t.Fatal("the corpus census saw no link mutations at all — the install batch builder is not emitting what this test reads")
	}
	if metaRootedSeen == 0 {
		t.Fatal("the corpus census saw no meta-rooted links at all — the shape this test discriminates within is absent, so its verdict is vacuous")
	}
	if kernelSourcedSeen == 0 {
		t.Fatal("the corpus census saw no link sourced at a kernel-typed vertex at all — the shape this test discriminates within is absent, so its verdict is vacuous")
	}
}
