package clinicdomain

import "testing"

// TestOpMetas_DispatchClassMatchesOwningDDL guards against the Fire 5 Inc 1
// regression (cd8696d shipped Dispatch.Class: "clinic" — the vertical name —
// instead of "appointment", the actual owning DDL's CanonicalName; a Facet
// client authoring an envelope from the descriptor would have sent an
// unresolvable class and gotten NoDDLForClass, per step4_hydrate.go's
// resolveClass + DDLCache.Lookup, both keyed on CanonicalName, exactly as
// service-domain's RequestService op-meta doc comment already specifies).
func TestOpMetas_DispatchClassMatchesOwningDDL(t *testing.T) {
	classForOp := map[string]string{}
	for _, d := range DDLs() {
		if d.Class != "meta.ddl.vertexType" {
			continue // only a vertexType DDL carries the Script step4 resolves by class
		}
		for _, op := range d.PermittedCommands {
			classForOp[op] = d.CanonicalName
		}
	}
	for _, m := range OpMetas() {
		if m.Dispatch == nil {
			continue
		}
		want := classForOp[m.OperationType]
		if want == "" {
			t.Fatalf("%s: no owning DDL found in PermittedCommands", m.OperationType)
		}
		if m.Dispatch.Class != want {
			t.Errorf("%s: Dispatch.Class = %q, want %q (owning DDL's CanonicalName)", m.OperationType, m.Dispatch.Class, want)
		}
	}
}
