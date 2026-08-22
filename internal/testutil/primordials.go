package testutil

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

var (
	primordialsOnce sync.Once
	primordialsErr  error
	primordialsPath string
)

// EnsurePrimordials populates internal/bootstrap's primordial ID set exactly
// once per test process — the production lifecycle (populate at boot, read-only
// thereafter). Every embedded-NATS test server in the process is seeded from
// this one set; servers are disjoint, so sharing collides with nothing.
func EnsurePrimordials(t *testing.T) {
	t.Helper()
	primordialsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lattice-test-bootstrap-*")
		if err != nil {
			primordialsErr = err
			return
		}
		primordialsPath = filepath.Join(dir, "lattice.bootstrap.json")
		_, primordialsErr = bootstrap.LoadOrGenerate(primordialsPath)
	})
	if primordialsErr != nil {
		t.Fatalf("testutil.EnsurePrimordials: %v", primordialsErr)
	}
}

// PrimordialsFilePath returns the path of the bootstrap file EnsurePrimordials
// wrote, populating it first if it has not run yet.
//
// It is for a test whose subject is a component that loads the file ITSELF —
// `lattice capability review`, say, whose binary has no other source for the
// identifier table. Such a test must hand over a path, not rely on the
// in-process globals, or it asserts nothing about the component's own wiring.
// The file and the globals come from the same one-populate-per-process set, so
// the identifiers a test seeds into a graph match the ones the component reads.
func PrimordialsFilePath(t *testing.T) string {
	t.Helper()
	EnsurePrimordials(t)
	return primordialsPath
}
