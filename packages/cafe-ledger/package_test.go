package cafeledger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asolgan/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go
// Definition in lockstep (the loftspace-ledger/clinic-ledger precedent): the
// install reads the Definition, but the manifest is the human-facing
// declaration, and a drift between the two is a silent install hazard.
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
