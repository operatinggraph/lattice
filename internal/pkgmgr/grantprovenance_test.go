package pkgmgr

import (
	"strings"
	"testing"
)

// The installer is one of exactly two writers of a permission vertex body (the
// other is rbac-domain's CreatePermission DDL, which stamps `runtime`). Its
// stamp is what makes a package-declared grant distinguishable, at the point
// step 3 consumes it, from authority an actor conferred on itself — Contract #6
// §6.1 / grant-provenance-runtime-permission-minting-design.md Move 2.
//
// The assertion that matters is not that the field is present but that it is
// `package` specifically: absence and every other value read as `runtime` on
// the consuming side (fail-closed), so a stamp that regressed to empty would
// silently reclassify every package-declared grant as a self-mint and start
// denying reserved ops the platform means to allow.
func TestBuildInstallBatch_PermissionCarriesPackageProvenance(t *testing.T) {
	def := Definition{
		Name:    "provenance-test-pkg",
		Version: "0.0.1",
		Permissions: []PermissionSpec{
			{OperationType: "SignLease", Scope: "any", GrantsTo: []string{"operator"}},
			// A reserved operationType, declared by a package: the SANCTIONED
			// authoring act. It must carry the same `package` stamp as any
			// ordinary verb — the reservation constrains the runtime channel,
			// not the declaration channel.
			{OperationType: "ShredRetentionClassKey", Scope: "any", Note: "deliberate",
				GrantsTo: []string{"operator"}},
		},
	}

	ops, _, err := BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}

	seen := map[string]map[string]any{}
	for _, op := range ops {
		if !strings.HasPrefix(op.Key, "vtx.permission.") {
			continue
		}
		data, _ := op.Document["data"].(map[string]any)
		if data == nil {
			t.Fatalf("permission %q has no data map: %+v", op.Key, op.Document)
		}
		opType, _ := data["operationType"].(string)
		seen[opType] = data
	}

	if len(seen) != 2 {
		t.Fatalf("expected 2 permission vertices, got %d (%v)", len(seen), seen)
	}
	for opType, data := range seen {
		if got, _ := data["origin"].(string); got != "package" {
			t.Errorf("permission[%s].data.origin = %q, want \"package\" — an unstamped or "+
				"differently-stamped installer mint reads as `runtime` at step 3 and loses "+
				"the reserved-operationType exemption a declared grant is entitled to", opType, got)
		}
		if got, _ := data["declaredBy"].(string); got != def.Name {
			t.Errorf("permission[%s].data.declaredBy = %q, want %q", opType, got, def.Name)
		}
	}

	// The stamp is additive: it must not have displaced the fields the grant
	// walk and the lane gate already read.
	if got, _ := seen["ShredRetentionClassKey"]["scope"].(string); got != "any" {
		t.Errorf("scope = %q, want \"any\" — the provenance stamp must be additive", got)
	}
	if got, _ := seen["ShredRetentionClassKey"]["note"].(string); got != "deliberate" {
		t.Errorf("note = %q, want \"deliberate\" — the provenance stamp must be additive", got)
	}
}

// The `grantedBy` link the installer writes alongside the permission vertex
// is the edge Step 3's capability walk actually authorizes on
// (grant-edge-provenance-design.md §1) — a stamped vertex that no live edge
// points at confers nothing, and a forged edge onto an existing permission
// confers everything that permission names regardless of the vertex's own
// stamp. So the link needs its own provenance, independently of the vertex
// test above.
func TestBuildInstallBatch_GrantLinkCarriesPackageProvenance(t *testing.T) {
	def := Definition{
		Name:    "provenance-link-test-pkg",
		Version: "0.0.1",
		Permissions: []PermissionSpec{
			{OperationType: "SignLease", Scope: "any", GrantsTo: []string{"operator"}},
		},
	}

	ops, _, err := BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}

	var linkData map[string]any
	found := 0
	for _, op := range ops {
		if !strings.HasPrefix(op.Key, "lnk.permission.") || !strings.Contains(op.Key, ".grantedBy.role.") {
			continue
		}
		found++
		linkData, _ = op.Document["data"].(map[string]any)
	}
	if found != 1 {
		t.Fatalf("expected 1 grantedBy grant link, got %d", found)
	}
	if linkData == nil {
		t.Fatalf("grantedBy link has no data map")
	}
	if got, _ := linkData["origin"].(string); got != "package" {
		t.Errorf("grantedBy link data.origin = %q, want \"package\" — an unstamped grant edge is "+
			"indistinguishable from a forged one (grant-edge-provenance-design.md §1)", got)
	}
	if got, _ := linkData["declaredBy"].(string); got != def.Name {
		t.Errorf("grantedBy link data.declaredBy = %q, want %q", got, def.Name)
	}
}
