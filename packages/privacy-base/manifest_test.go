package privacybase

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition is a pure unit smoke test: parse the
// on-disk manifest.yaml and confirm it cross-validates against this
// package's exported Definition. Drift between the two surfaces (the YAML
// manifest and the Go Definition) is the most common authoring bug for new
// packages; this test catches it before any NATS integration.
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
