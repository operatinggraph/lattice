package bootstrap

// The grant half of the kernel's topology is pinned against the seeder by
// kernel_grant_links_test.go. This file pins the OTHER half — the six
// `identity holdsRole role` edges — and the twelve-key whole, because the
// Processor holds that whole as an exact-key protected set: a key this
// composition names that the seeder never emits protects a phantom, and a key
// the seeder emits that this composition misses leaves a root-equivalence edge
// revocable by one ordinary RevokeRole.
//
// The composition deliberately does not reuse the *HoldsRoleLinkKey package
// variables, which are derived at load time and so are empty on an unloaded
// table; byte-equality against what the seeder actually emits is what keeps the
// two derivations honest about the same twelve edges.

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestKernelTopologyLinkKeys_MatchesSeededEntries(t *testing.T) {
	populateForTest(t)

	entries, err := buildPrimordialEntries()
	if err != nil {
		t.Fatalf("buildPrimordialEntries: %v", err)
	}

	var seededHoldsRole []string
	for _, e := range entries {
		if strings.HasPrefix(e.key, "lnk.identity.") && strings.Contains(e.key, ".holdsRole.role.") {
			seededHoldsRole = append(seededHoldsRole, e.key)
		}
	}

	declared, err := KernelTopologyLinkKeys()
	if err != nil {
		t.Fatalf("KernelTopologyLinkKeys: %v", err)
	}

	var declaredHoldsRole []string
	for _, k := range declared {
		if strings.Contains(k, ".holdsRole.role.") {
			declaredHoldsRole = append(declaredHoldsRole, k)
		}
	}

	sort.Strings(seededHoldsRole)
	sort.Strings(declaredHoldsRole)

	if len(seededHoldsRole) != len(declaredHoldsRole) {
		t.Fatalf("seeder emits %d holdsRole edges, KernelTopologyLinkKeys names %d\n seeded:   %v\n declared: %v",
			len(seededHoldsRole), len(declaredHoldsRole), seededHoldsRole, declaredHoldsRole)
	}
	for i := range seededHoldsRole {
		if seededHoldsRole[i] != declaredHoldsRole[i] {
			t.Errorf("holdsRole edge %d: seeder emits %s, KernelTopologyLinkKeys names %s",
				i, seededHoldsRole[i], declaredHoldsRole[i])
		}
	}

	// The whole set is twelve — the six grant edges plus the six above — and
	// every one of them is an entry the seeder writes. A key named here that
	// the seeder does not emit would sit in the Processor's protected set
	// guarding nothing.
	if len(declared) != 12 {
		t.Fatalf("KernelTopologyLinkKeys returns %d keys, want 12 (6 grantedBy + 6 holdsRole): %v", len(declared), declared)
	}
	idx := entriesByKey(t)
	seen := map[string]bool{}
	for _, k := range declared {
		if seen[k] {
			t.Errorf("%s is named twice", k)
		}
		seen[k] = true
		if _, ok := idx[k]; !ok {
			t.Errorf("%s is named by KernelTopologyLinkKeys but the seeder writes no entry at it", k)
		}
	}
}

// The Gateway identity is seeded without a holdsRole edge (the narrow-role
// fork), so the set must not name one for it: a phantom key in the protected
// set would refuse a legitimate future grant to the Gateway with a kernel
// refusal that names an edge nothing ever seeded.
func TestKernelTopologyLinkKeys_OmitsTheGatewayIdentity(t *testing.T) {
	populateForTest(t)

	keys, err := KernelTopologyLinkKeys()
	if err != nil {
		t.Fatalf("KernelTopologyLinkKeys: %v", err)
	}
	for _, k := range keys {
		if strings.Contains(k, "."+GatewayIdentityID+".") {
			t.Errorf("%s names the Gateway identity, which the kernel seeds no holdsRole edge for", k)
		}
	}
}

// An unloaded table must refuse rather than compose. Composing would hand back
// twelve well-shaped keys whose id segments are empty — a set that matches no
// mutation, so the Processor wiring it would report a full protected set and
// protect nothing. The positive vector is the pin above, so this refusal cannot
// be masking a permanently broken composition.
//
// The twelve keys are composed from THIRTEEN identifiers, and a check that
// covers only some of them refuses on those and composes an empty-segmented key
// for the rest, with a nil error — the exact failure the refusal exists to
// prevent, in the half nobody looked at. So the loop covers all thirteen: the
// operator role, the six holdsRole identities, and the six permissions whose
// grant edges KernelTopologyLinkKeys composes through KernelGrantLinkKeys.
//
// This is an internal test because it drives package vars. No test in this
// package runs in parallel and the originals are restored, so the mutation is
// invisible to the rest of the run.
func TestKernelTopologyLinkKeys_UnloadedIdentifiersError(t *testing.T) {
	populateForTest(t)

	// Each identifier is addressed through a pointer to its package variable,
	// so blanking one exercises the same global the composition reads.
	identifiers := []struct {
		name string
		id   *string
	}{
		{"roleOperator", &RoleOperatorID},
		{"bootstrapIdentity", &BootstrapIdentityID},
		{"loomIdentity", &LoomIdentityID},
		{"weaverIdentity", &WeaverIdentityID},
		{"bridgeIdentity", &BridgeIdentityID},
		{"objmgrIdentity", &ObjmgrIdentityID},
		{"privacyIdentity", &PrivacyIdentityID},
		{"permCreateMetaVertex", &PermCreateMetaVertexID},
		{"permUpdateMetaVertex", &PermUpdateMetaVertexID},
		{"permTombstoneMetaVertex", &PermTombstoneMetaVertexID},
		{"permInstallPackage", &PermInstallPackageID},
		{"permUninstallPackage", &PermUninstallPackageID},
		{"permUpgradePackage", &PermUpgradePackageID},
	}

	// A sanity floor on the enumeration itself: thirteen identifiers compose
	// twelve keys, and a row dropped from the list above would silently narrow
	// what this test covers.
	if len(identifiers) != 13 {
		t.Fatalf("the enumeration names %d identifiers, want 13 (1 role + 6 identities + 6 permissions)", len(identifiers))
	}

	for _, ident := range identifiers {
		t.Run(ident.name, func(t *testing.T) {
			saved := *ident.id
			if saved == "" {
				t.Fatalf("%s is empty on a populated table — the fixture, not the subject, is broken", ident.name)
			}
			defer func() { *ident.id = saved }()

			*ident.id = ""
			keys, err := KernelTopologyLinkKeys()
			if !errors.Is(err, ErrPrimordialIDsUnloaded) {
				t.Fatalf("err = %v, want it to wrap ErrPrimordialIDsUnloaded", err)
			}
			if keys != nil {
				t.Errorf("keys = %v, want nil alongside the error", keys)
			}
			if !strings.Contains(err.Error(), ident.name) {
				t.Errorf("err = %q, want it to name %s", err, ident.name)
			}
		})
	}
}
