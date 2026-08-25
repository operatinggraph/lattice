package bootstrap

// The kernel's grant topology — which permissions reach the operator role —
// is derived in two places that cannot be collapsed into one. The seeder
// (buildPrimordialEntries) walks metaPerms/installPerms building a link
// ENVELOPE per edge, needing each edge's endpoints as well as its key;
// KernelGrantLinkKeys returns just the keys, for consumers that ask which
// edges are kernel-authored (PrimordialVertexKeys, internal/pkgmgr's
// grant-link reconciler). Neither can be expressed in terms of the other
// without contorting the seeding loops.
//
// So the agreement between them is asserted here instead. Without this a
// permission added to or removed from the kernel's grant set would leave
// KernelGrantLinkKeys naming an edge nothing seeds, or missing one that is
// seeded — and downstream that reads as a permanent kernelMissing drift
// finding, or as a kernel edge silently reclassified as an unaccountable
// forgery.

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestKernelGrantLinkKeys_MatchesWhatTheSeederEmits(t *testing.T) {
	populateForTest(t)

	entries, err := buildPrimordialEntries()
	if err != nil {
		t.Fatalf("buildPrimordialEntries: %v", err)
	}

	var seeded []string
	for _, e := range entries {
		if !strings.HasPrefix(e.key, "lnk.permission.") || !strings.Contains(e.key, ".grantedBy.role.") {
			continue
		}
		seeded = append(seeded, e.key)
	}

	declared := KernelGrantLinkKeys()

	sort.Strings(seeded)
	sortedDeclared := append([]string(nil), declared...)
	sort.Strings(sortedDeclared)

	if len(seeded) != len(sortedDeclared) {
		t.Fatalf("seeder emits %d grantedBy edges, KernelGrantLinkKeys names %d\n seeded:   %v\n declared: %v",
			len(seeded), len(sortedDeclared), seeded, sortedDeclared)
	}
	for i := range seeded {
		if seeded[i] != sortedDeclared[i] {
			t.Errorf("grant edge %d: seeder emits %s, KernelGrantLinkKeys names %s",
				i, seeded[i], sortedDeclared[i])
		}
	}
}

// Every key KernelGrantLinkKeys names must be reachable as a LIVE grant edge,
// not a tombstone and not some other class that happens to sit at a grant-edge
// key: internal/pkgmgr classifies by key membership BEFORE reading the
// document, so a kernel key holding a non-grant or deleted envelope would be
// waved through as kernel-clean.
func TestKernelGrantLinkKeys_NameLiveGrantEdges(t *testing.T) {
	populateForTest(t)

	idx := entriesByKey(t)

	for _, k := range KernelGrantLinkKeys() {
		raw, ok := idx[k]
		if !ok {
			t.Errorf("%s is named by KernelGrantLinkKeys but the seeder writes no entry at it", k)
			continue
		}
		var env struct {
			Class        string `json:"class"`
			IsDeleted    bool   `json:"isDeleted"`
			SourceVertex string `json:"sourceVertex"`
			TargetVertex string `json:"targetVertex"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Errorf("%s does not decode as a link envelope: %v", k, err)
			continue
		}
		if env.Class != "grantedBy" {
			t.Errorf("%s has class %q, want \"grantedBy\"", k, env.Class)
		}
		if env.IsDeleted {
			t.Errorf("%s is seeded tombstoned — a kernel grant edge the reconciler would count as present while it confers nothing", k)
		}
		if want := "vtx.role." + RoleOperatorID; env.TargetVertex != want {
			t.Errorf("%s targets %q, want %q", k, env.TargetVertex, want)
		}
		if !strings.HasPrefix(env.SourceVertex, "vtx.permission.") {
			t.Errorf("%s sources %q, want a vtx.permission.* key", k, env.SourceVertex)
		}
	}
}
