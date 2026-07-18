package cafedomain

import "testing"

// TestOpMetas_OpenTabDeclaresLeaseKeyRead guards the fix found live during
// the Facet second-renderer spike (edge-showcase-app-design.md §7.11): a
// descriptor-form submission that omits payload.leaseAppKey from
// ContextHint.Reads comes back UnknownLeaseApplication, because
// tabDDLScript's liveness check (ddls.go) reads the lease vertex itself via
// state[lease_key], not just the applicationFor link. Dispatch.Reads is the
// vocabulary a generic client derives that required read from.
func TestOpMetas_OpenTabDeclaresLeaseKeyRead(t *testing.T) {
	for _, m := range OpMetas() {
		if m.OperationType != "OpenTab" {
			continue
		}
		if m.Dispatch == nil {
			t.Fatal("OpenTab: Dispatch is nil")
		}
		want := "{payload.leaseAppKey}"
		for _, r := range m.Dispatch.Reads {
			if r == want {
				return
			}
		}
		t.Errorf("OpenTab: Dispatch.Reads = %v, want to contain %q", m.Dispatch.Reads, want)
	}
}
