package bridge

// The bridge's envelope-source table is two literals and a map; the values
// it must agree with live in packages/privacy-base (the lens buckets) and
// internal/vault (the closed set of key-holder kinds). Non-test bridge code
// imports neither — the literals exist precisely so internal/bridge stays out
// of packages/ (P5, egress.go's own comment) — so the agreement is pinned here,
// in a test-only import that introduces no package cycle (privacy-base imports
// nothing under internal/bridge).

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/vault"
	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
)

// TestEnvelopeBucketLiterals_MatchTheLensesThatProjectThem: each literal is the
// bucket its lens actually writes, so a bucket rename in privacy-base cannot
// leave the bridge reading a bucket nothing projects (five transient attempts,
// then a terminal dispatch, with no compile error anywhere).
func TestEnvelopeBucketLiterals_MatchTheLensesThatProjectThem(t *testing.T) {
	t.Parallel()

	if identityEnvelopeBucket != privacybase.PiiKeyEnvelopeBucket {
		t.Errorf("identityEnvelopeBucket = %q, want privacy-base's PiiKeyEnvelopeBucket %q",
			identityEnvelopeBucket, privacybase.PiiKeyEnvelopeBucket)
	}
	if retentionClassEnvelopeBucket != privacybase.RetentionKeyEnvelopeBucket {
		t.Errorf("retentionClassEnvelopeBucket = %q, want privacy-base's RetentionKeyEnvelopeBucket %q",
			retentionClassEnvelopeBucket, privacybase.RetentionKeyEnvelopeBucket)
	}
}

// TestEnvelopeBucketFor_ServesExactlyTheKeyHolderKinds pins the table's key set
// equal to vault.KeyHolderKinds() in BOTH directions, so the Processor's mint
// gate and this boundary cannot drift apart: a kind added to the set with no
// bucket entry would be admitted at mint and then refused here (five retries
// and a terminal dispatch instead of one authoring error), and a bucket entry
// for a kind outside the set would serve a holder no commit path can write.
func TestEnvelopeBucketFor_ServesExactlyTheKeyHolderKinds(t *testing.T) {
	t.Parallel()

	for _, kind := range vault.KeyHolderKinds() {
		bucket, ok := envelopeBucketFor(kind)
		if !ok {
			t.Errorf("envelopeBucketFor(%q) = not served, want a bucket for every kind in vault.KeyHolderKinds()", kind)
			continue
		}
		if bucket == "" {
			t.Errorf("envelopeBucketFor(%q) served an empty bucket", kind)
		}
	}

	// The other direction, enumerated off the table itself: every kind it
	// serves must be a kind custody can name.
	for kind, bucket := range envelopeBucketByHolderKind {
		if !vault.IsKeyHolderKind(kind) {
			t.Errorf("envelopeBucketFor serves %q from %q, but vault.KeyHolderKinds() %v does not carry that kind",
				kind, bucket, vault.KeyHolderKinds())
		}
	}
	if len(envelopeBucketByHolderKind) != len(vault.KeyHolderKinds()) {
		t.Errorf("the envelope table serves %d kinds, vault.KeyHolderKinds() carries %d — the two sets have drifted",
			len(envelopeBucketByHolderKind), len(vault.KeyHolderKinds()))
	}
}
