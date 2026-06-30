package servicedomain_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServiceType_AbsentFromCore is invariant (a) gate-asserted (the engines
// stay type-blind; the concrete `service` type lives ONLY in the package).
//
// It walks internal/ and asserts the service-domain class discriminator
// strings (service.backgroundCheck.* / service.payment.*) appear NOWHERE in
// core/engine/processor/bootstrap — they are the package's own vocabulary.
//
// The grep is deliberately narrow: it targets the package's class strings, NOT
// the bare token "service". A broad "service" grep would false-positive on
// legitimate, pre-existing, unrelated references:
//   - internal/bootstrap/lenses.go — a comment mentioning the future
//     service/location grant vocabulary (availableAt etc.) and the cap-doc
//     `serviceAccess` field (the deferred read-path scope, NOT this vertex type);
//   - internal/processor/step3_auth_*.go — the Contract #6 `service`
//     auth-scope path + test fixtures (vtx.service.probe / .someOther), which
//     are the generic auth scope concept, not the service-domain vertex type.
//
// None of those reference the service-domain class strings, so this assertion
// stays true while the package owns its type exclusively.
func TestServiceType_AbsentFromCore(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	internalDir := filepath.Join(repoRoot, "internal")
	if _, err := os.Stat(internalDir); err != nil {
		t.Fatalf("internal/ dir not found at %s: %v", internalDir, err)
	}

	// The service-domain class discriminator strings. If any of these appears
	// under internal/, core/engine has been taught the concrete type —
	// invariant (a) is violated.
	forbidden := []string{
		"service.backgroundCheck.template",
		"service.backgroundCheck.instance",
		"service.payment.template",
		"service.payment.instance",
	}

	var violations []string
	walkErr := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Invariant (a) guards ENGINE PRODUCTION code — the processor / refractor /
		// loom / weaver / bootstrap must stay type-blind. Test harnesses that boot
		// the real vertical (e.g. internal/leaseconvergence, the refractor scalar
		// e2e) are legitimately type-AWARE: they install lease-signing and assert
		// its projections, and since the type/subtype discriminator now lives on the
		// vertex envelope class (P7 — service.<family>.instance, no .family shadow
		// aspect), an e2e harness MUST reference that class to discriminate a family.
		// So the scan excludes _test.go — the engines themselves still never name the
		// type (the instanceOf-template resolver walks it generically).
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(b)
		for _, f := range forbidden {
			if strings.Contains(content, f) {
				rel, _ := filepath.Rel(repoRoot, path)
				violations = append(violations, rel+" contains "+f)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal/: %v", walkErr)
	}
	if len(violations) > 0 {
		t.Fatalf("invariant (a) violated — service-domain class strings found in internal/:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
