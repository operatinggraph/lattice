package pkgmgr

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Contract #8 §8.1 — entity NanoIDs are derived from package name + entity tag
// and are version-independent, so the same logical entity keeps its key across
// versions (the in-place upgrade in §8.6). These tests pin that property and
// the permission-identity tag's stability, which the upgrade diff relies on.

func TestEntityNanoID_Properties(t *testing.T) {
	id := entityNanoID("lattice-demo", "lens:demoLens")

	if got := len(id); got != substrate.NanoIDLength {
		t.Fatalf("entityNanoID length = %d, want %d", got, substrate.NanoIDLength)
	}
	if strings.Contains(id, ".") {
		t.Fatalf("entityNanoID %q contains a dot — would break the Contract #1 key segmentation", id)
	}
	for _, r := range id {
		if !strings.ContainsRune(substrate.Alphabet, r) {
			t.Fatalf("entityNanoID %q contains non-alphabet rune %q", id, r)
		}
	}
	if !substrate.IsValidNanoID(id) {
		t.Fatalf("entityNanoID %q is not a valid NanoID", id)
	}
	// Determinism.
	if again := entityNanoID("lattice-demo", "lens:demoLens"); again != id {
		t.Fatalf("entityNanoID not deterministic: %q != %q", id, again)
	}
	// Golden anchor — a fixed (name, tag) maps to a fixed id. Guards a silent
	// shift in the alphabet order / 6-bit masking / salt format that would
	// re-key every installed entity and break cross-references on upgrade.
	const golden = "88ynzntPCqfeUx6888yn"
	if id != golden {
		t.Fatalf("entityNanoID golden drift: got %q, want %q", id, golden)
	}
}

func TestEntityNanoID_VersionIndependent(t *testing.T) {
	// The load-bearing §8.1 property: the entity key does NOT depend on the
	// package version, so a v0.1.0 lens and a v0.2.0 lens share a key.
	v1 := entityNanoID("clinic-domain", "lens:clinicProviders")
	v2 := entityNanoID("clinic-domain", "lens:clinicProviders")
	if v1 != v2 {
		t.Fatalf("entityNanoID should be version-independent, got %q vs %q", v1, v2)
	}

	// And it must differ from the old version-salted derivation — proof the
	// version was actually dropped from the entity salt (a regression that
	// re-introduced it would silently re-key every entity on a version bump).
	salted := deterministicNanoID("clinic-domain", "0.1.0", "lens:clinicProviders")
	if v1 == salted {
		t.Fatalf("entityNanoID still matches the version-salted derivation %q — version not dropped", salted)
	}

	// The op requestId derivation (deterministicNanoID) MUST keep the version,
	// so two versions of the same install dedup independently.
	r1 := deterministicNanoID("clinic-domain", "0.1.0", "install-op")
	r2 := deterministicNanoID("clinic-domain", "0.2.0", "install-op")
	if r1 == r2 {
		t.Fatalf("install-op requestId must be version-scoped, but v0.1.0 and v0.2.0 collide: %q", r1)
	}
}

func TestPermTag_DistinguishesScopeNotIndex(t *testing.T) {
	// Same operationType, different scope → distinct identity (so an `any` and
	// a `self` grant of the same op keep separate keys).
	if permTag("ClaimIdentity", "any") == permTag("ClaimIdentity", "self") {
		t.Fatal("permTag must distinguish scope")
	}
	// The tag carries no list index, so it is identical for the same
	// (operationType, scope) — the property the reorder-stability test exercises
	// end-to-end below. The resulting entity key tracks that identity.
	a := entityNanoID("p", permTag("SignLease", "any"))
	b := entityNanoID("p", permTag("SignLease", "self"))
	if a == b {
		t.Fatal("permission entity keys must differ when scope differs")
	}
}

func TestValidatePermissionIdentityUniqueness(t *testing.T) {
	tests := []struct {
		name    string
		perms   []PermissionSpec
		wantErr bool
	}{
		{
			name: "distinct operationTypes ok",
			perms: []PermissionSpec{
				{OperationType: "CreatePatient", Scope: "any"},
				{OperationType: "UpdatePatient", Scope: "any"},
			},
		},
		{
			name: "same operationType, different scope ok",
			perms: []PermissionSpec{
				{OperationType: "ClaimIdentity", Scope: "any"},
				{OperationType: "ClaimIdentity", Scope: "self"},
			},
		},
		{
			name: "duplicate (operationType, scope) rejected",
			perms: []PermissionSpec{
				{OperationType: "SignLease", Scope: "any"},
				{OperationType: "SignLease", Scope: "any"},
			},
			wantErr: true,
		},
		{name: "empty ok"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Definition{Permissions: tc.perms}.validatePermissionIdentityUniqueness()
			if tc.wantErr && err == nil {
				t.Fatal("expected a duplicate-permission error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestBuildInstallBatch_PermissionKeysStableUnderReorder(t *testing.T) {
	permA := PermissionSpec{OperationType: "CreatePatient", Scope: "any"}
	permB := PermissionSpec{OperationType: "DischargePatient", Scope: "self"}

	forward := Definition{Name: "demo", Version: "0.1.0", Permissions: []PermissionSpec{permA, permB}}
	reversed := Definition{Name: "demo", Version: "0.1.0", Permissions: []PermissionSpec{permB, permA}}

	keysOf := func(def Definition) map[string]struct{} {
		ops, _, err := BuildInstallBatchForTest(def)
		if err != nil {
			t.Fatalf("BuildInstallBatchForTest: %v", err)
		}
		out := map[string]struct{}{}
		for _, op := range ops {
			if strings.HasPrefix(op.Key, "vtx.permission.") {
				out[op.Key] = struct{}{}
			}
		}
		if len(out) != 2 {
			t.Fatalf("expected 2 permission vertex keys, got %d (%v)", len(out), out)
		}
		return out
	}

	fwd, rev := keysOf(forward), keysOf(reversed)
	for k := range fwd {
		if _, ok := rev[k]; !ok {
			t.Fatalf("permission key %q present forward but missing when reordered — keys are position-dependent", k)
		}
	}
}
