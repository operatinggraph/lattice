package pkgregistry

import (
	"os"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestEveryShippedPackageIsRegistered asserts the registry covers the corpus in
// both directions: every row's key is its definition's own name (the manifest
// lookup key), and every packages/<dir> that ships a parsable manifest has a
// row. An unregistered package is invisible to every consumer at once — it
// cannot be installed by the CLI or by Loupe, and the `lint-package-standard`
// gate never sees it.
func TestEveryShippedPackageIsRegistered(t *testing.T) {
	for _, name := range Names() {
		def, _ := Lookup(name)
		if def.Name != name {
			t.Errorf("registry key %q maps to definition named %q", name, def.Name)
		}
	}
	dirs, err := os.ReadDir("../../packages")
	if err != nil {
		t.Fatalf("read packages dir: %v", err)
	}
	shipped := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		manifest, err := pkgmgr.ParseManifest("../../packages/" + d.Name() + "/manifest.yaml")
		if err != nil {
			continue // not a package dir (no parsable manifest)
		}
		shipped++
		if _, ok := Lookup(manifest.Name); !ok {
			t.Errorf("packages/%s (manifest name %q) is missing from the registry", d.Name(), manifest.Name)
		}
		// A registry name is also a directory path for every consumer that
		// reads a package's own files by name — the lint-package-standard gate
		// stats packages/<name>/lens_cypher_test.go, and both the gate and this
		// test parse packages/<name>/manifest.yaml.
		if manifest.Name != d.Name() {
			t.Errorf("packages/%s declares manifest name %q — the directory and the manifest name must agree, since consumers resolve a package's files by its registered name", d.Name(), manifest.Name)
		}
	}
	if shipped == 0 {
		t.Fatal("no shipped package manifests found — the ../../packages scan is broken")
	}
}

// TestEveryPackageCompilesItsReadGrantWalks runs the install-time compilation
// every shipped package goes through, over the whole registry: read-grant walks
// must compile (which is where the walk grammar and the "every non-self-anchored
// Personal lens declares a Walk" invariant are enforced), and the on-disk
// manifest must agree with the COMPOSED definition — generated cap-read
// producers included, index-wise, so a manifest that lists them out of
// ReadGrantDomains order fails here rather than at install.
//
// A registered package whose manifest does not parse fails outright: it would
// fail the same way at install, and skipping would let the drift check be
// silently disarmed by a malformed file.
func TestEveryPackageCompilesItsReadGrantWalks(t *testing.T) {
	for _, name := range Names() {
		def, _ := Lookup(name)
		t.Run(name, func(t *testing.T) {
			if _, err := def.ExpandReadGrantWalks(); err != nil {
				t.Fatalf("read-grant walks do not compile: %v", err)
			}
			manifest, err := pkgmgr.ParseManifest("../../packages/" + name + "/manifest.yaml")
			if err != nil {
				t.Fatalf("manifest does not parse: %v", err)
			}
			if err := manifest.VerifyAgainstDefinition(def); err != nil {
				t.Errorf("manifest drifts from the composed definition: %v", err)
			}
		})
	}
}
