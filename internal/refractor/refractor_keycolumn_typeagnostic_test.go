package refractor_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestKeyColumn_NoTypeLeakInEngine enforces Epic-13/14 invariant (a): the
// engines stay type-blind; concrete types live in packages. The §10.2 Option (b)
// keyColumn mechanism keys off the descriptor field, never a type/bucket literal.
// This pins that the four 14.2-touched engine files carry no convergence type
// literal as a key-shape switch — a hardcoded type assumption would break the
// throwaway-anchor proof e2e, and this test catches it at the source level.
//
// capability-kv is deliberately NOT in the forbidden set: it appears in plan.go
// as the auth-plane guard classifier (AuthPlaneBucket), which is a guard fork,
// not a key-shape switch.
func TestKeyColumn_NoTypeLeakInEngine(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	dir := filepath.Dir(thisFile)

	engineFiles := []string{
		filepath.Join(dir, "projection", "output.go"),
		filepath.Join(dir, "projection", "plan.go"),
		filepath.Join(dir, "projection", "driver.go"),
		filepath.Join(dir, "lens", "corekv_source.go"),
	}
	forbidden := []string{"leaseApp", "weaver-targets", "leaseApplicationComplete", "service"}

	for _, f := range engineFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		content := string(src)
		for _, lit := range forbidden {
			if strings.Contains(content, lit) {
				t.Errorf("engine file %s contains forbidden convergence literal %q — the keyColumn mechanism must key off the descriptor field, never a concrete type/bucket name (invariant a)",
					filepath.Base(f), lit)
			}
		}
	}
}
