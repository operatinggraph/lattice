// Provenance stamp on the runtime mint channel — Contract #6 §6.1 /
// grant-provenance-runtime-permission-minting-design.md Move 2.
//
// rbac-domain's CreatePermission is the ONLY operation that authors a
// permission vertex outside the installer, so its stamp is half of what makes
// origin a real discriminator rather than a field nobody writes. The other
// half (the installer's `package` stamp) is proven in
// internal/pkgmgr/grantprovenance_test.go; the consuming refusal is proven in
// internal/processor/step3_auth_capability_test.go.
package rbacdomain_test

import (
	"context"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
)

// permissionData pulls the minted permission vertex's data map out of a script
// result, failing the test if the mutation shape isn't the one CreatePermission
// is documented to produce.
func permissionData(t *testing.T, result processor.ScriptResult) map[string]any {
	t.Helper()
	if len(result.Mutations) == 0 {
		t.Fatalf("expected a permission mutation, got none")
	}
	m := result.Mutations[0]
	if !strings.HasPrefix(m.Key, "vtx.permission.") {
		t.Fatalf("mutations[0].key = %q, want vtx.permission.*", m.Key)
	}
	data, _ := m.Document["data"].(map[string]any)
	if data == nil {
		t.Fatalf("minted permission carries no data map: %+v", m.Document)
	}
	return data
}

// TestStarlark_Rbac_CreatePermission_StampsRuntimeOrigin is the positive vector
// for the whole provenance mechanism: before any refusal can be trusted, the
// mint has to actually write the marker the refusal keys on. A CreatePermission
// that stamped nothing would leave every runtime mint indistinguishable from a
// package declaration at the projection, and the reserved-set check downstream
// would be reading a field that is always empty.
func TestStarlark_Rbac_CreatePermission_StampsRuntimeOrigin(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	sc := makeRbacScriptContext("CreatePermission",
		`{"operationType":"CreateRole","scope":"any","note":"test"}`, nil)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data := permissionData(t, result)

	if got, _ := data["origin"].(string); got != "runtime" {
		t.Fatalf("data.origin = %q, want \"runtime\" — an unstamped runtime mint is "+
			"indistinguishable from a package-declared grant once projected", got)
	}
	// The stamp is additive, not a replacement: the fields the grant walk and
	// the scope switch read must survive it.
	if got, _ := data["operationType"].(string); got != "CreateRole" {
		t.Errorf("data.operationType = %q, want \"CreateRole\"", got)
	}
	if got, _ := data["scope"].(string); got != "any" {
		t.Errorf("data.scope = %q, want \"any\"", got)
	}
	if got, _ := data["note"].(string); got != "test" {
		t.Errorf("data.note = %q, want \"test\"", got)
	}
	// `declaredBy` is the installer's field alone — a runtime mint has no
	// declaring package, and inventing one would launder a self-mint into
	// something an auditor would read as manifest-recorded.
	if _, present := data["declaredBy"]; present {
		t.Errorf("a runtime mint must not carry declaredBy; got %v", data["declaredBy"])
	}
}

// TestStarlark_Rbac_CreatePermission_StampsReservedOpAsRuntime drives the exact
// verb the v1 reserved set names. The mint itself is NOT refused — the design
// enforces at consumption, not at mint, so a future second grant channel
// inherits the refusal for free — but the stamp it writes is what the step-3
// refusal reads. If this branch ever special-cased a reserved verb at mint,
// the refusal would silently become unreachable through this channel and the
// step-3 gate would stop being exercised by anything real.
func TestStarlark_Rbac_CreatePermission_StampsReservedOpAsRuntime(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	sc := makeRbacScriptContext("CreatePermission",
		`{"operationType":"ShredRetentionClassKey","scope":"any"}`, nil)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data := permissionData(t, result)
	if got, _ := data["origin"].(string); got != "runtime" {
		t.Fatalf("data.origin = %q, want \"runtime\" for a reserved-op mint", got)
	}
}
