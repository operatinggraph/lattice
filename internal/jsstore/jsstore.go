// Package jsstore provides a test-only helper for the embedded NATS+JetStream
// fixtures used across the Lattice test suite. It depends only on the standard
// library so any package (including internal-test packages) can import it
// without creating an import cycle.
package jsstore

import (
	"os"
	"testing"
	"time"
)

// Dir returns a fresh directory for an embedded NATS JetStream file store and
// registers a cleanup that removes it after the test.
//
// It exists because Server.Shutdown() returns before every JetStream filestore
// goroutine has finished its final write: a consumer or stream store can flush
// one last file just after shutdown. A plain RemoveAll racing that write fails
// with "directory not empty" — which, when the store lives under t.TempDir(),
// surfaces as a test failure from the testing framework's own cleanup. The race
// is invisible when test packages run serially (the box is idle and NATS drains
// instantly) but appears under parallel execution.
//
// The cleanup here retries the removal briefly to absorb that final write, and
// logs rather than fails if a residual file somehow outlasts the window (the OS
// reclaims the temp dir regardless). Because the directory is allocated here
// rather than via t.TempDir(), the framework registers no competing removal.
func Dir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nats-jetstream-")
	if err != nil {
		t.Fatalf("create JetStream store dir: %v", err)
	}
	t.Cleanup(func() {
		var rmErr error
		for i := 0; i < 200; i++ {
			if rmErr = os.RemoveAll(dir); rmErr == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Logf("jsstore: residual JetStream store dir %s not removed: %v", dir, rmErr)
	})
	return dir
}
