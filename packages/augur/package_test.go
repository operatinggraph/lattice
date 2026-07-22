package augur_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/packages/augur"
)

// TestPackage_ManifestMatchesDefinition catches drift between manifest.yaml and
// the in-code Definition (DDL / lens / permission counts + canonicalNames). The
// installer parses the manifest for the install plan; a mismatch would install a
// shape the package author did not declare.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(augur.Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_AugurProposalsLens pins the read-model review surface: a FLAT (no
// ProjectionKind) nats-kv projection into the augur-proposals bucket, and — the
// trusted-tool posture — NOT protected and NOT a weaver-target convergence lens.
// (The lens cypher itself is exercised end-to-end by the install path in the
// integration tests + the live verify-package-augur run.)
func TestPackage_AugurProposalsLens(t *testing.T) {
	lenses := augur.Lenses()
	if len(lenses) != 2 {
		t.Fatalf("want exactly 2 lenses (augurProposals + augurDispatchPending), got %d", len(lenses))
	}
	l := lenses[0]
	if l.CanonicalName != "augurProposals" {
		t.Errorf("CanonicalName = %q, want augurProposals", l.CanonicalName)
	}
	if l.Class != "meta.lens" {
		t.Errorf("Class = %q, want meta.lens", l.Class)
	}
	if l.Adapter != "nats-kv" {
		t.Errorf("Adapter = %q, want nats-kv", l.Adapter)
	}
	if augur.AugurProposalsBucket != "augur-proposals" {
		t.Errorf("AugurProposalsBucket = %q, want augur-proposals", augur.AugurProposalsBucket)
	}
	if l.Bucket != augur.AugurProposalsBucket {
		t.Errorf("Bucket = %q, want %q", l.Bucket, augur.AugurProposalsBucket)
	}
	if l.Engine != "full" {
		t.Errorf("Engine = %q, want full", l.Engine)
	}
	if l.Protected {
		t.Error("read-model review lens must NOT be protected (trusted-tool posture)")
	}
	if l.ProjectionKind != "" {
		t.Errorf("ProjectionKind = %q, want empty (flat one-row-per-proposal projection)", l.ProjectionKind)
	}
	if l.Output != nil {
		t.Error("flat read-model lens carries no actorAggregate OutputDescriptor")
	}

	// The Package definition wires the lens.
	if got := len(augur.Package.Lenses); got != 2 {
		t.Fatalf("Package.Lenses count = %d, want 2", got)
	}
	if augur.Package.Lenses[0].CanonicalName != "augurProposals" {
		t.Errorf("Package.Lenses[0] = %q, want augurProposals", augur.Package.Lenses[0].CanonicalName)
	}
}

// TestPackage_AugurDispatchPendingLens pins the Fire 2b pickup transport: an
// actorAggregate weaver-target convergence lens projecting into the shared
// weaver-targets bucket under the augurDispatch. prefix (bare-NanoID KeyColumn),
// and the augurDispatch meta.weaverTarget wiring its one gap to proposedOp.
func TestPackage_AugurDispatchPendingLens(t *testing.T) {
	lenses := augur.Lenses()
	if len(lenses) != 2 {
		t.Fatalf("want exactly 2 lenses, got %d", len(lenses))
	}
	l := lenses[1]
	if l.CanonicalName != "augurDispatchPending" {
		t.Fatalf("CanonicalName = %q, want augurDispatchPending", l.CanonicalName)
	}
	if l.Adapter != "nats-kv" || l.Bucket != "weaver-targets" || l.Engine != "full" {
		t.Errorf("adapter/bucket/engine = %q/%q/%q, want nats-kv/weaver-targets/full", l.Adapter, l.Bucket, l.Engine)
	}
	if l.ProjectionKind != "actorAggregate" {
		t.Fatalf("ProjectionKind = %q, want actorAggregate", l.ProjectionKind)
	}
	if l.Output == nil {
		t.Fatal("actorAggregate lens requires an Output descriptor")
	}
	if l.Output.AnchorType != "augurproposal" {
		t.Errorf("Output.AnchorType = %q, want augurproposal", l.Output.AnchorType)
	}
	if l.Output.OutputKeyPattern != "augurDispatch.{actorSuffix}" {
		t.Errorf("Output.OutputKeyPattern = %q, want augurDispatch.{actorSuffix}", l.Output.OutputKeyPattern)
	}
	if l.Output.KeyColumn == "" {
		t.Error("Output.KeyColumn must be set (bare-NanoID row key, §10.2 Option b)")
	}
	if l.Output.EmptyBehavior != "delete" {
		t.Errorf("Output.EmptyBehavior = %q, want delete", l.Output.EmptyBehavior)
	}
	for _, want := range []string{"violating", "missing_dispatch", "entityKey", "proposedAction", "proposedParams", "candidateKey", "targetMetaKey"} {
		found := false
		for _, c := range l.Output.BodyColumns {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Output.BodyColumns missing %q: %v", want, l.Output.BodyColumns)
		}
	}

	targets := augur.WeaverTargets()
	if len(targets) != 1 {
		t.Fatalf("want exactly 1 weaverTarget, got %d", len(targets))
	}
	tgt := targets[0]
	if tgt.TargetID != "augurDispatch" {
		t.Errorf("TargetID = %q, want augurDispatch", tgt.TargetID)
	}
	if tgt.LensRef != "augurDispatchPending" {
		t.Errorf("LensRef = %q, want augurDispatchPending", tgt.LensRef)
	}
	ga, ok := tgt.Gaps["missing_dispatch"]
	if !ok {
		t.Fatal(`Gaps["missing_dispatch"] missing`)
	}
	if ga.Action != "proposedOp" {
		t.Errorf("Gaps[missing_dispatch].Action = %q, want proposedOp", ga.Action)
	}
}
