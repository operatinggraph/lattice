package leasesigning

import (
	"regexp"
	"sort"
	"testing"
)

// engineInstanceOpReads is the BARE-key read-set Loom (submitExternalTask)
// declares in ContextHint.Reads when it dispatches the externalTask instanceOp
// (CreateLeaseServiceInstance). Keyed by PAYLOAD FIELD (Loom emits the field's
// value). The drift guard maps these to the script to prove they match what the
// DDL hydrates.
//
// This is the engine↔DDL read-set contract for the instanceOp. If a future
// instanceOp-DDL edit adds or drops a vertex_alive check, this test fails —
// forcing Loom's read-set to be updated rather than silently HydrationMiss-ing
// (fail closed) or over-hydrating (L2).
var engineInstanceOpReads = []string{"subjectKey"}

// TestInstanceOpReads_MatchDDLScript asserts Loom's dispatched instanceOp
// read-set equals exactly the payload fields the CreateLeaseServiceInstance DDL
// branch validates with vertex_alive.
func TestInstanceOpReads_MatchDDLScript(t *testing.T) {
	got := vertexAlivePayloadFields(t, leaseServiceInstanceDDLScript, "CreateLeaseServiceInstance")
	assertSameStringSet(t, "CreateLeaseServiceInstance", engineInstanceOpReads, got)
}

// TestDocInstanceOpReads_MatchDDLScript is the docGen twin: the leaseDocument
// pattern's externalTask dispatches CreateLeaseDocInstance with the same
// [subjectKey] read-set (the .signature gate and every document field resolve
// via kv.Read/kv.Links, never hydration).
func TestDocInstanceOpReads_MatchDDLScript(t *testing.T) {
	got := vertexAlivePayloadFields(t, leaseDocInstanceDDLScript, "CreateLeaseDocInstance")
	assertSameStringSet(t, "CreateLeaseDocInstance", engineInstanceOpReads, got)
}

// vertexAlivePayloadFields parses one op branch and returns the set of PAYLOAD
// FIELDS whose values flow into a vertex_alive(state, <var>) check, mapping a
// var to its payload field via the `<var> = required_string(p, "<field>")`
// binding (the idiom every DDL uses). See the orchestration-base twin for the
// full rationale (the two are intentionally independent so each package owns its
// own engine↔DDL contract without a cross-package test dependency).
func vertexAlivePayloadFields(t *testing.T, script, branchOp string) []string {
	t.Helper()
	branch := opBranch(t, script, branchOp)

	bindRe := regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:required_string|required_bare_handle|optional_string)\(p,\s*"([^"]+)"\)`)
	varToField := map[string]string{}
	for _, m := range bindRe.FindAllStringSubmatch(branch, -1) {
		varToField[m[1]] = m[2]
	}

	aliveRe := regexp.MustCompile(`vertex_alive\(state,\s*([A-Za-z_][A-Za-z0-9_]*)\)`)
	seen := map[string]struct{}{}
	for _, m := range aliveRe.FindAllStringSubmatch(branch, -1) {
		if field, ok := varToField[m[1]]; ok {
			seen[field] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func opBranch(t *testing.T, script, op string) string {
	t.Helper()
	start := regexp.MustCompile(`if ot == "` + regexp.QuoteMeta(op) + `":`)
	loc := start.FindStringIndex(script)
	if loc == nil {
		t.Fatalf("op branch %q not found in script", op)
	}
	rest := script[loc[1]:]
	next := regexp.MustCompile(`\n    if ot == "`)
	if nl := next.FindStringIndex(rest); nl != nil {
		return rest[:nl[0]]
	}
	return rest
}

func assertSameStringSet(t *testing.T, label string, want, got []string) {
	t.Helper()
	w := append([]string(nil), want...)
	g := append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)
	if len(w) != len(g) {
		t.Fatalf("%s read-set mismatch:\n  engine declares: %v\n  DDL validates:   %v", label, w, g)
	}
	for i := range w {
		if w[i] != g[i] {
			t.Fatalf("%s read-set mismatch:\n  engine declares: %v\n  DDL validates:   %v", label, w, g)
		}
	}
}
