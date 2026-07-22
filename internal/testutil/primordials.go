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
		_, primordialsErr = bootstrap.LoadOrGenerate(filepath.Join(dir, "lattice.bootstrap.json"))
	})
	if primordialsErr != nil {
		t.Fatalf("testutil.EnsurePrimordials: %v", primordialsErr)
	}
}
