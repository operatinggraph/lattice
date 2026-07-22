package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCLI_HelpExits0 builds the lattice binary and asserts that
// lattice --help exits 0 and lists the expected command groups.
func TestCLI_HelpExits0(t *testing.T) {
	binPath := buildLatticeBin(t)

	cmd := exec.Command(binPath, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lattice --help failed: %v\noutput:\n%s", err, string(out))
	}

	outputStr := string(out)
	expectedGroups := []string{
		"lattice op",
		"lattice graph",
		"lattice lens",
		"lattice query",
		"lattice health",
		"lattice identity",
		"lattice candidates",
		"lattice capability",
		"lattice auth-trace",
		"lattice bootstrap",
	}
	for _, group := range expectedGroups {
		shortName := strings.SplitN(group, " ", 2)[1]
		if !strings.Contains(outputStr, shortName) {
			t.Errorf("--help output missing command group %q\noutput:\n%s", group, outputStr)
		}
	}
}

// TestCLI_VersionExits0 asserts that lattice --version exits 0 and
// emits a non-empty version string.
func TestCLI_VersionExits0(t *testing.T) {
	binPath := buildLatticeBin(t)

	cmd := exec.Command(binPath, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lattice --version failed: %v\noutput:\n%s", err, string(out))
	}

	outputStr := string(out)
	if outputStr == "" {
		t.Error("--version output is empty")
	}
	if !strings.Contains(outputStr, "lattice") {
		t.Errorf("--version output does not contain 'lattice': %q", outputStr)
	}
}

// buildLatticeBin builds the lattice binary to a temp path and returns it.
func buildLatticeBin(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "lattice-test-bin")
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/operatinggraph/lattice/cmd/lattice")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	// Ensure go can find the module root regardless of working directory.
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/lattice failed: %v\n%s", err, string(out))
	}
	return binPath
}

// repoRoot returns the absolute path of the repository root by walking up
// from this test file's location until a go.mod is found.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod in any parent directory")
		}
		dir = parent
	}
}
