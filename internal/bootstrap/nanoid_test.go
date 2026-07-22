package bootstrap_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

// TestLoad_AbsentFile verifies the strict loader errors when the bootstrap
// file does not exist, rather than minting fresh primordial IDs. Engine
// binaries (cmd/loom, cmd/weaver) rely on this to fail fast instead of
// running with an unrecognized actor identity.
func TestLoad_AbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")

	if err := bootstrap.Load(path); err == nil {
		t.Fatalf("Load(%s): expected error for absent file, got nil", path)
	}
}

// TestLoadOrGenerate_FreshThenRecoverThenCommitted walks the two-phase
// commit protocol's three read states in sequence against the same path:
// no file (generate + write in-progress), file present with
// status="in-progress" (crash recovery — re-run seeding), and file present
// with status="committed" (seeding already done).
func TestLoadOrGenerate_FreshThenRecoverThenCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")

	fresh, err := bootstrap.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("LoadOrGenerate (no file): unexpected error: %v", err)
	}
	if !fresh {
		t.Fatalf("LoadOrGenerate (no file): freshlyGenerated = false, want true")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), `"status": "in-progress"`) {
		t.Fatalf("bootstrap file after fresh generate: want status=in-progress, got %s", data)
	}

	fresh, err = bootstrap.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("LoadOrGenerate (in-progress recovery): unexpected error: %v", err)
	}
	if !fresh {
		t.Fatalf("LoadOrGenerate (in-progress recovery): freshlyGenerated = false, want true (caller must re-run seeding)")
	}

	if err := bootstrap.PersistCommitted(path); err != nil {
		t.Fatalf("PersistCommitted: unexpected error: %v", err)
	}
	fresh, err = bootstrap.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("LoadOrGenerate (committed): unexpected error: %v", err)
	}
	if fresh {
		t.Fatalf("LoadOrGenerate (committed): freshlyGenerated = true, want false (seeding already done)")
	}
}

// TestPersist_WritesCommittedStatus verifies the legacy single-phase Persist
// entry point (retained for callers that predate the two-phase
// LoadOrGenerate/PersistCommitted protocol) writes status="committed" and
// that a subsequent LoadOrGenerate reads it back as already-seeded.
func TestPersist_WritesCommittedStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")

	if _, err := bootstrap.LoadOrGenerate(path); err != nil {
		t.Fatalf("LoadOrGenerate (no file): unexpected error: %v", err)
	}

	if err := bootstrap.Persist(path); err != nil {
		t.Fatalf("Persist: unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), `"status": "committed"`) {
		t.Fatalf("bootstrap file after Persist: want status=committed, got %s", data)
	}

	fresh, err := bootstrap.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("LoadOrGenerate (post-Persist): unexpected error: %v", err)
	}
	if fresh {
		t.Fatalf("LoadOrGenerate (post-Persist): freshlyGenerated = true, want false (Persist wrote status=committed)")
	}
}

// TestLoadOrGenerate_MalformedJSON verifies a corrupt bootstrap file (e.g.
// truncated by a crash mid-write) surfaces a parse error rather than
// silently regenerating a fresh key space.
func TestLoadOrGenerate_MalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := bootstrap.LoadOrGenerate(path); err == nil {
		t.Fatalf("LoadOrGenerate(%s): expected parse error, got nil", path)
	}
}

// TestLoadOrGenerate_VersionMismatch verifies an older-version bootstrap
// file (e.g. left over from before an operator ran `make down && make up`)
// is rejected with a clear message instead of populating a stale/partial ID
// set.
func TestLoadOrGenerate_VersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	fixture := `{"version":"3","generatedAt":"2026-01-01T00:00:00Z","status":"committed","primordialIDs":{}}`
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := bootstrap.LoadOrGenerate(path); err == nil {
		t.Fatalf("LoadOrGenerate(%s): expected version-mismatch error, got nil", path)
	}
}

// TestLoadOrGenerate_InvalidNanoID verifies a bootstrap file whose
// primordialIDs fail Contract #1 validation (e.g. hand-edited or corrupted)
// is rejected rather than populating the package's ID variables with
// non-compliant values.
func TestLoadOrGenerate_InvalidNanoID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	fixture := `{"version":"16","generatedAt":"2026-01-01T00:00:00Z","status":"committed","primordialIDs":{}}`
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := bootstrap.LoadOrGenerate(path); err == nil {
		t.Fatalf("LoadOrGenerate(%s): expected NanoID-compliance error, got nil", path)
	}
}

// TestLoad_MalformedJSON mirrors TestLoadOrGenerate_MalformedJSON for the
// read-only loader used by scripts/verify-kernel and the stub engines.
func TestLoad_MalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := bootstrap.Load(path); err == nil {
		t.Fatalf("Load(%s): expected parse error, got nil", path)
	}
}

// TestLoad_VersionMismatch mirrors TestLoadOrGenerate_VersionMismatch for
// the read-only loader.
func TestLoad_VersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	fixture := `{"version":"99","generatedAt":"2026-01-01T00:00:00Z","status":"committed","primordialIDs":{}}`
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := bootstrap.Load(path); err == nil {
		t.Fatalf("Load(%s): expected version-mismatch error, got nil", path)
	}
}
