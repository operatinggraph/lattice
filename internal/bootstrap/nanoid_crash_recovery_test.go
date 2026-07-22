package bootstrap_test

// Tests for the two-phase-commit crash paths of LoadOrGenerate /
// persistWithStatus: ID stability across a crash between SeedPrimordial and
// PersistCommitted, rejection of corrupt in-progress state, and surfacing of
// stat/read/write failures instead of silently minting a fresh primordial
// key space over a possibly-seeded graph.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

// readBootstrapFile parses lattice.bootstrap.json for assertions on the
// persisted two-phase-commit state.
func readBootstrapFile(t *testing.T, path string) bootstrap.BootstrapFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var f bootstrap.BootstrapFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f
}

// TestLoadOrGenerate_CrashRecoveryReusesIDs proves the property the
// two-phase commit exists for: a crash after the in-progress write but
// before PersistCommitted must NOT mint a fresh primordial key space on the
// next boot. The recovery pass re-populates the exact IDs already persisted
// — so a re-run of SeedPrimordial targets the same keys the first attempt
// may have already written — and leaves the file's status in-progress until
// seeding is re-confirmed.
func TestLoadOrGenerate_CrashRecoveryReusesIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")

	fresh, err := bootstrap.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("LoadOrGenerate (no file): unexpected error: %v", err)
	}
	if !fresh {
		t.Fatalf("LoadOrGenerate (no file): freshlyGenerated = false, want true")
	}
	first := readBootstrapFile(t, path)
	if first.Status != "in-progress" {
		t.Fatalf("after fresh generate: status = %q, want in-progress", first.Status)
	}

	// Crash before PersistCommitted: the next boot calls LoadOrGenerate again.
	fresh, err = bootstrap.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("LoadOrGenerate (crash recovery): unexpected error: %v", err)
	}
	if !fresh {
		t.Fatalf("LoadOrGenerate (crash recovery): freshlyGenerated = false, want true (seeding must be re-run)")
	}
	second := readBootstrapFile(t, path)
	if second.PrimordialIDs != first.PrimordialIDs {
		t.Fatalf("crash recovery must reuse the persisted primordial IDs, not regenerate:\nfirst:  %+v\nsecond: %+v",
			first.PrimordialIDs, second.PrimordialIDs)
	}
	if second.Status != "in-progress" {
		t.Fatalf("crash recovery must not flip status before seeding re-runs: status = %q", second.Status)
	}
	if bootstrap.BootstrapOpID != second.PrimordialIDs.BootstrapOp {
		t.Fatalf("package vars must be populated from the recovered file: BootstrapOpID = %q, file = %q",
			bootstrap.BootstrapOpID, second.PrimordialIDs.BootstrapOp)
	}
}

// TestLoadOrGenerate_InProgressInvalidIDsRejected covers the crash-recovery
// arm meeting an in-progress file whose IDs fail Contract #1 validation
// (hand-edited or corrupted): recovery must surface the compliance error —
// not regenerate, and not boot with non-compliant IDs. Sibling of
// TestLoadOrGenerate_InvalidNanoID, which covers the committed arm.
func TestLoadOrGenerate_InProgressInvalidIDsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	fixture := `{"version":"16","generatedAt":"2026-01-01T00:00:00Z","status":"in-progress","primordialIDs":{}}`
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := bootstrap.LoadOrGenerate(path)
	if err == nil {
		t.Fatalf("LoadOrGenerate(%s): expected NanoID-compliance error for in-progress file, got nil", path)
	}
	if !strings.Contains(err.Error(), "not Contract #1-compliant") {
		t.Fatalf("error must reject on Contract #1 compliance, got: %v", err)
	}
}

// TestLoadOrGenerate_MissingStatusTreatedAsCommitted pins the legacy arm: a
// current-version file with no status field reads as already-committed
// (freshlyGenerated=false). Only writers predating the two-phase protocol
// omit status, and regenerating IDs under an already-seeded graph would
// orphan it.
func TestLoadOrGenerate_MissingStatusTreatedAsCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	if _, err := bootstrap.LoadOrGenerate(path); err != nil {
		t.Fatalf("LoadOrGenerate (no file): unexpected error: %v", err)
	}
	f := readBootstrapFile(t, path)
	f.Status = ""
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if strings.Contains(string(data), `"status"`) {
		t.Fatalf("fixture must carry no status field (omitempty), got: %s", data)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	fresh, err := bootstrap.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("LoadOrGenerate (missing status): unexpected error: %v", err)
	}
	if fresh {
		t.Fatalf("LoadOrGenerate (missing status): freshlyGenerated = true, want false (treated as committed)")
	}
	if bootstrap.BootstrapOpID != f.PrimordialIDs.BootstrapOp {
		t.Fatalf("package vars must be populated from the file: BootstrapOpID = %q, file = %q",
			bootstrap.BootstrapOpID, f.PrimordialIDs.BootstrapOp)
	}
}

// TestLoadOrGenerate_UnreadableFileSurfacesError verifies an existing but
// unreadable path (here: a directory) surfaces the read error rather than
// falling through to generating a fresh key space over it.
func TestLoadOrGenerate_UnreadableFileSurfacesError(t *testing.T) {
	dir := t.TempDir() // exists, so the stat probe passes; reading it fails

	_, err := bootstrap.LoadOrGenerate(dir)
	if err == nil {
		t.Fatalf("LoadOrGenerate(%s): expected read error for directory path, got nil", dir)
	}
	if !strings.Contains(err.Error(), "read") {
		t.Fatalf("error must surface the read failure, got: %v", err)
	}
}

// TestLoadOrGenerate_StatFailureSurfacesError verifies a stat failure other
// than not-exist (here: an unsearchable parent directory) surfaces instead
// of being treated as the fresh-generate case — regenerating a key space
// just because the file is temporarily unreachable would orphan a seeded
// graph.
func TestLoadOrGenerate_StatFailureSurfacesError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not bind root")
	}
	parent := filepath.Join(t.TempDir(), "sealed")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(parent, "lattice.bootstrap.json")
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	_, err := bootstrap.LoadOrGenerate(path)
	if err == nil {
		t.Fatalf("LoadOrGenerate(%s): expected stat error, got nil", path)
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Fatalf("error must surface the stat failure, got: %v", err)
	}
}

// TestLoadOrGenerate_InProgressWriteFailureSurfaces verifies the fresh-
// generate path surfaces a failed in-progress write (parent directory
// absent) and leaves no file behind — a silent failure here would seed a
// graph whose IDs die with the process.
func TestLoadOrGenerate_InProgressWriteFailureSurfaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "lattice.bootstrap.json")

	_, err := bootstrap.LoadOrGenerate(path)
	if err == nil {
		t.Fatalf("LoadOrGenerate(%s): expected write error, got nil", path)
	}
	if !strings.Contains(err.Error(), "write in-progress bootstrap file") {
		t.Fatalf("error must surface the in-progress persist failure, got: %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("no bootstrap file must be left behind after a failed write, stat: %v", statErr)
	}
}

// TestPersistCommitted_WriteFailureSurfaces verifies the commit phase of the
// two-phase protocol surfaces a write failure (here: the path is a
// directory) instead of reporting a commit that never landed.
func TestPersistCommitted_WriteFailureSurfaces(t *testing.T) {
	good := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	if _, err := bootstrap.LoadOrGenerate(good); err != nil {
		t.Fatalf("LoadOrGenerate (populate IDs): unexpected error: %v", err)
	}

	dir := t.TempDir()
	err := bootstrap.PersistCommitted(dir)
	if err == nil {
		t.Fatalf("PersistCommitted(%s): expected write error for directory path, got nil", dir)
	}
	if !strings.Contains(err.Error(), "write") {
		t.Fatalf("error must surface the write failure, got: %v", err)
	}
}
