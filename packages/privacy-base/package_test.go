package privacybase

import (
	"testing"
)

// TestPackage_StructurePins pins every declared element by count and canonical
// name (Vertical Package Standard S6, loftspace-domain/package_test.go idiom). A
// declaration added or dropped without a deliberate edit here reds this test
// rather than reaching an install, where the same change is a silent capability
// or read-model shift.
//
// This package is load-bearing for crypto-shred. `piiKey` is minted and read
// by the Processor's own commit path, and its class is registered here only so
// the DDL cache and Loupe can see it — a DDL quietly vanishing from this list
// would break encrypt-on-write with no failing operation to point at. The
// erasure declarations are different in kind: `sealIdentityForErasure` and
// `purgeIdentityDedupFootprint` ARE script-dispatched, and `erasureRequested`'s
// permittedCommands is the write gate on the marker every identity write-path
// guard reads — including the two erasure verbs' own preconditions.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 10; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 3; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 4; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 0; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}
	if got, want := len(Package.Roles), 0; got != want {
		t.Errorf("Roles: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 1; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if got, want := len(Package.Depends), 0; got != want {
		t.Errorf("Depends: got %d, want %d — this package sits at the bottom of the stack", got, want)
	}

	wantDDLs := []struct{ name, class string }{
		{"piiKey", "meta.ddl.aspectType"},
		{"shredIdentityKey", "meta.ddl.vertexType"},
		{"privacy.keyShredded", "meta.ddl.eventType"},
		{"erasureRequested", "meta.ddl.aspectType"},
		{"sealIdentityForErasure", "meta.ddl.vertexType"},
		{"privacy.erasureRequested", "meta.ddl.eventType"},
		{"purgeIdentityDedupFootprint", "meta.ddl.vertexType"},
		{"erasure", "meta.ddl.aspectType"},
		{"sealIdentityForErasureComplete", "meta.ddl.vertexType"},
		{"privacy.erasureCompleted", "meta.ddl.eventType"},
	}
	if len(wantDDLs) != len(Package.DDLs) {
		t.Fatalf("wantDDLs pins %d of %d declarations — the loop below is bounded by the want-slice, so an unpinned tail is invisible", len(wantDDLs), len(Package.DDLs))
	}
	for i, want := range wantDDLs {
		if i >= len(Package.DDLs) {
			break
		}
		got := Package.DDLs[i]
		if got.CanonicalName != want.name || got.Class != want.class {
			t.Errorf("DDLs[%d]: got %s/%s, want %s/%s", i, got.CanonicalName, got.Class, want.name, want.class)
		}
	}

	wantLenses := []struct{ name, bucket string }{
		{"shredStatus", ShredStatusBucket},
		{"piiKeyEnvelope", PiiKeyEnvelopeBucket},
		{"identityErasureResidue", "weaver-targets"},
	}
	if len(wantLenses) != len(Package.Lenses) {
		t.Fatalf("wantLenses pins %d of %d lenses — the loop below is bounded by the want-slice, so an unpinned tail is invisible", len(wantLenses), len(Package.Lenses))
	}
	for i, want := range wantLenses {
		if i >= len(Package.Lenses) {
			break
		}
		got := Package.Lenses[i]
		if got.CanonicalName != want.name || got.Bucket != want.bucket {
			t.Errorf("Lenses[%d]: got %s→%s, want %s→%s", i, got.CanonicalName, got.Bucket, want.name, want.bucket)
		}
	}

	wantWeaverTargets := []string{ErasureCompleteTarget}
	if len(wantWeaverTargets) != len(Package.WeaverTargets) {
		t.Fatalf("wantWeaverTargets pins %d of %d targets — the loop below is bounded by the want-slice, so an unpinned tail is invisible", len(wantWeaverTargets), len(Package.WeaverTargets))
	}
	for i, want := range wantWeaverTargets {
		if i >= len(Package.WeaverTargets) {
			break
		}
		if got := Package.WeaverTargets[i].TargetID; got != want {
			t.Errorf("WeaverTargets[%d]: got TargetID %s, want %s", i, got, want)
		}
	}

	wantPerms := []struct{ op, scope string }{
		{"RecordShredFinalization", "any"},
		{"SealIdentityForErasure", "any"},
		{"PurgeIdentityDedupFootprint", "any"},
		{"SealIdentityForErasureComplete", "any"},
	}
	if len(wantPerms) != len(Package.Permissions) {
		t.Fatalf("wantPerms pins %d of %d grants — an unpinned grant could change scope silently", len(wantPerms), len(Package.Permissions))
	}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}
}
