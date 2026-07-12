package edgemanifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
)

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

func TestPackage_NoDDLsOrPermissions(t *testing.T) {
	if got := len(Package.DDLs); got != 0 {
		t.Fatalf("expected 0 DDLs (edge-manifest is projection-only), got %d", got)
	}
	if got := len(Package.Permissions); got != 0 {
		t.Fatalf("expected 0 permissions (edge-manifest is projection-only), got %d", got)
	}
}

func TestPackage_FiveLenses(t *testing.T) {
	if got := len(Package.Lenses); got != 5 {
		t.Fatalf("expected 5 lenses, got %d", got)
	}
	names := map[string]bool{}
	for _, l := range Package.Lenses {
		names[l.CanonicalName] = true
	}
	for _, want := range []string{"edgeIdentity", "edgeServices", "edgeCatalog", "edgeTasks", "edgeInstances"} {
		if !names[want] {
			t.Fatalf("missing lens %q (have %v)", want, names)
		}
	}
}

// TestPackage_LensesAreNatsSubjectPersonal pins the Personal Lens transport
// shape every edge-manifest lens must share (edge-showcase-app-design.md
// §3.1): nats-subject adapter, the shared SYNC stream + lattice.sync.user
// subject prefix, Personal:true fan-out, and __actor present in IntoKey
// (bucketguard.go enforces this at build time — this test pins the intent
// so a future lens addition doesn't silently omit it).
func TestPackage_LensesAreNatsSubjectPersonal(t *testing.T) {
	for _, l := range Package.Lenses {
		if l.Adapter != "nats-subject" {
			t.Errorf("%s: adapter = %q, want nats-subject", l.CanonicalName, l.Adapter)
		}
		if l.SubjectPrefix != "lattice.sync.user" {
			t.Errorf("%s: subjectPrefix = %q, want lattice.sync.user", l.CanonicalName, l.SubjectPrefix)
		}
		if l.Stream != "SYNC" {
			t.Errorf("%s: stream = %q, want SYNC", l.CanonicalName, l.Stream)
		}
		if !l.Personal {
			t.Errorf("%s: Personal = false, want true", l.CanonicalName)
		}
		hasActor := false
		for _, k := range l.IntoKey {
			if k == "__actor" {
				hasActor = true
			}
		}
		if !hasActor {
			t.Errorf("%s: IntoKey %v missing __actor", l.CanonicalName, l.IntoKey)
		}
	}
}

// TestPackage_LensRowKeysAreManifestNamespaced pins that every lens's first
// non-actor RETURN column is a literal "manifest.<ns>" string (the
// buildKey dot-join anchor) — the reserved namespace edge/store.go's
// isStorableKey exemption recognizes.
func TestPackage_LensRowKeysAreManifestNamespaced(t *testing.T) {
	want := map[string]string{
		"edgeIdentity":  `"manifest.me" AS ns`,
		"edgeServices":  `"manifest.svc" AS ns`,
		"edgeCatalog":   `"manifest.op" AS ns`,
		"edgeTasks":     `"manifest.task" AS ns`,
		"edgeInstances": `"manifest.inst" AS ns`,
	}
	for _, l := range Package.Lenses {
		lit, ok := want[l.CanonicalName]
		if !ok {
			t.Fatalf("unexpected lens %q", l.CanonicalName)
		}
		if !strings.Contains(l.Spec, lit) {
			t.Errorf("%s: spec missing %q", l.CanonicalName, lit)
		}
	}
}

// TestPackage_SpecsParseUnderFullEngine runs every lens's cypher through the
// same lex/parse/AST-visitor pipeline Refractor uses at activation
// (ruleengine/full.Engine.Parse) — a live-graph-free syntax + supported-
// construct check (no NATS/Postgres needed), catching the class of bug a
// unit test can't otherwise reach for a brand-new package with no running
// stack to install against.
func TestPackage_SpecsParseUnderFullEngine(t *testing.T) {
	eng := full.New()
	for _, l := range Package.Lenses {
		if _, err := eng.Parse(l.Spec); err != nil {
			t.Errorf("%s: cypher failed to parse under the full engine: %v\nspec:\n%s", l.CanonicalName, err, l.Spec)
		}
	}
}

// TestPackage_SpecsUseNullTestNotIsNull guards against reintroducing the
// silently-mis-evaluated `IS NULL`/`IS NOT NULL` form (full/visitor.go
// only supports `= null` / `<> null` — see lease-signing/lenses.go:565 and
// semantic-contracts/lenses.go:61 for the same guard elsewhere).
func TestPackage_SpecsUseNullTestNotIsNull(t *testing.T) {
	for _, l := range Package.Lenses {
		upper := strings.ToUpper(l.Spec)
		if strings.Contains(upper, "IS NULL") || strings.Contains(upper, "IS NOT NULL") {
			t.Errorf("%s: spec uses unsupported IS [NOT] NULL — use = null / <> null instead", l.CanonicalName)
		}
	}
}
