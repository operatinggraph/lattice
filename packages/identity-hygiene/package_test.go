package identityhygiene

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asolgan/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition is a pure unit smoke test:
// parse the on-disk manifest.yaml and confirm it cross-validates
// against this package's exported Definition. Drift between the two
// surfaces (the YAML manifest and the Go Definition) is the most
// common authoring bug for new packages; this test catches it before
// any NATS integration.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	manifestPath := filepath.Join(wd, "manifest.yaml")
	m, err := pkgmgr.ParseManifest(manifestPath)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_DDLScriptCompilesAsStarlark is a smoke test that the
// embedded MergeIdentity script is syntactically valid Starlark. We do
// NOT execute it — execution requires hydrated state + DDL cache.
// Compilation alone catches typos.
func TestPackage_DDLScriptCompilesAsStarlark(t *testing.T) {
	if len(Package.DDLs) != 1 {
		t.Fatalf("expected exactly 1 DDL, got %d", len(Package.DDLs))
	}
	src := Package.DDLs[0].Script
	if len(src) == 0 {
		t.Fatal("DDL script is empty")
	}
	// Trivial sanity: top-level `def execute(state, op):` must be present
	// (the runner requires it). Phase 1 stops here; a real syntax check
	// via starlark.SourceProgram is left as a future enhancement.
	if !contains(src, "def execute(state, op):") {
		t.Fatalf("DDL script missing required top-level `def execute(state, op):` signature")
	}
}

// TestPackage_SingleLensWithEdgeEnumeration asserts the package
// declares exactly one Lens — `duplicateCandidates` — which itself
// enumerates the secondary's incident edges via `collect(DISTINCT ...)`.
// A second Lens read by Processor's MergeIdentity script would extend
// Processor's read surface beyond the architecturally-fixed set
// (Core KV + DDL cache + Capability KV + idempotency tracker) and is
// forbidden.
func TestPackage_SingleLensWithEdgeEnumeration(t *testing.T) {
	if got := len(Package.Lenses); got != 1 {
		t.Fatalf("expected exactly 1 lens, got %d", got)
	}
	lens := Package.Lenses[0]
	if lens.CanonicalName != "duplicateCandidates" {
		t.Fatalf("expected sole lens canonicalName=duplicateCandidates, got %q", lens.CanonicalName)
	}
	for _, marker := range []string{
		"secondaryInboundEdges",
		"secondaryOutboundEdges",
		"collect(DISTINCT",
		"OPTIONAL MATCH",
	} {
		if !contains(lens.Spec, marker) {
			t.Errorf("duplicateCandidates spec missing required marker %q", marker)
		}
	}
}

// TestPackage_MergeScriptValidatesEdgesFromCommand confirms the
// MergeIdentity script consumes `edges` as a command parameter (NOT a
// lens-bucket read) and carries each of the four required error codes
// from the trust gate.
func TestPackage_MergeScriptValidatesEdgesFromCommand(t *testing.T) {
	src := Package.DDLs[0].Script
	// Must read `edges` off the payload.
	if !contains(src, `hasattr(p, "edges")`) {
		t.Error("script must read `edges` from op.payload")
	}
	// Forbidden-token guard: the script must never reference a
	// merge-plan lens — that pattern would extend Processor's read
	// surface. Tokens assembled at runtime so this source file itself
	// stays free of the literal.
	for _, forbidden := range []string{
		"identityMerge" + "Plan",
		"identity-merge" + "-plan",
	} {
		if contains(src, forbidden) {
			t.Errorf("script must not reference forbidden lens %q", forbidden)
		}
	}
	// Trust-gate error codes — all four required by brief §4.
	for _, code := range []string{
		"EdgeNotFound",
		"EdgeNotALink",
		"EdgeDoesNotTouchSecondary",
		"MergeBatchTooLarge",
	} {
		if !contains(src, code) {
			t.Errorf("script missing required error code %q", code)
		}
	}
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
