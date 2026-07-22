package capabilityauthor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	capabilityauthor "github.com/operatinggraph/lattice/packages/capability-author"
)

// TestPackage_ManifestMatchesDefinition catches drift between manifest.yaml
// and the in-code Definition (DDL / lens / permission / weaverTarget /
// loomPattern / opMeta counts + canonicalNames) — mirrors packages/augur's
// own drift test. The installer parses the manifest for the install plan; a
// mismatch would install a shape the package author did not declare.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(capabilityauthor.Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_ReviewAndCatalogLenses pins the Fire-1-checkpoint read-model
// pair added alongside the escalation dispatch: capabilityProposals (flat
// operator review, not protected, not a weaver-target convergence lens) and
// capabilityAuthorContext (flat installed-DDL self-description catalog).
func TestPackage_ReviewAndCatalogLenses(t *testing.T) {
	lenses := capabilityauthor.Lenses()
	if len(lenses) != 3 {
		t.Fatalf("want exactly 3 lenses (capabilityAuthorPending + capabilityProposals + capabilityAuthorContext), got %d", len(lenses))
	}

	review := lenses[1]
	if review.CanonicalName != "capabilityProposals" {
		t.Errorf("lenses[1].CanonicalName = %q, want capabilityProposals", review.CanonicalName)
	}
	if review.Adapter != "nats-kv" || review.Bucket != capabilityauthor.CapabilityProposalsBucket || review.Engine != "full" {
		t.Errorf("review lens adapter/bucket/engine = %q/%q/%q, want nats-kv/%q/full", review.Adapter, review.Bucket, review.Engine, capabilityauthor.CapabilityProposalsBucket)
	}
	if review.Protected {
		t.Error("capabilityProposals must NOT be protected (trusted-tool posture, mirrors augurProposals)")
	}
	if review.ProjectionKind != "" || review.Output != nil {
		t.Error("capabilityProposals is a flat one-row-per-proposal projection, no actorAggregate Output descriptor")
	}
	if capabilityauthor.CapabilityProposalsBucket != "capability-proposals" {
		t.Errorf("CapabilityProposalsBucket = %q, want capability-proposals", capabilityauthor.CapabilityProposalsBucket)
	}

	catalog := lenses[2]
	if catalog.CanonicalName != "capabilityAuthorContext" {
		t.Errorf("lenses[2].CanonicalName = %q, want capabilityAuthorContext", catalog.CanonicalName)
	}
	if catalog.Adapter != "nats-kv" || catalog.Bucket != capabilityauthor.CapabilityAuthorContextBucket || catalog.Engine != "full" {
		t.Errorf("catalog lens adapter/bucket/engine = %q/%q/%q, want nats-kv/%q/full", catalog.Adapter, catalog.Bucket, catalog.Engine, capabilityauthor.CapabilityAuthorContextBucket)
	}
	if catalog.Protected {
		t.Error("capabilityAuthorContext must NOT be protected (read-model over installed platform metadata, not PII)")
	}
	if catalog.ProjectionKind != "" || catalog.Output != nil {
		t.Error("capabilityAuthorContext is a flat scan, no actorAggregate Output descriptor")
	}
	if capabilityauthor.CapabilityAuthorContextBucket != "capability-author-context" {
		t.Errorf("CapabilityAuthorContextBucket = %q, want capability-author-context", capabilityauthor.CapabilityAuthorContextBucket)
	}

	if got := len(capabilityauthor.Package.Lenses); got != 3 {
		t.Fatalf("Package.Lenses count = %d, want 3", got)
	}
}
