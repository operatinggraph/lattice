package leasesigning

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go Definition
// in lockstep: the install reads the Definition, but the manifest is the
// human-facing declaration, and a drift between the two (a permission / op added to
// one but not the other) is a silent install hazard. VerifyAgainstDefinition
// cross-checks name, version, and the declared DDL/lens/permission/weaverTarget/
// loomPattern/opMeta listings.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}
