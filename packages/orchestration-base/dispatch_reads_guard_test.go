package orchestrationbase

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// engineCreateTaskReads is the BARE-key read-set the engines (Weaver
// strategist.go actionAssignTask; Loom submitUserTask) declare in
// ContextHint.Reads when they dispatch CreateTask. It is keyed by PAYLOAD FIELD
// (the engines emit the field's value); the drift guard maps these to the script
// to prove they match what the DDL hydrates.
//
// This is the engine↔DDL read-set contract for CreateTask. If a future task-DDL
// edit adds or drops a vertex_alive check, this test fails — forcing the engine
// dispatch read-set to be updated in lock-step rather than silently failing
// closed (a HydrationMiss) or wastefully over-hydrating (L2).
var engineCreateTaskReads = []string{"assignee", "forOperation", "scopedTo"}

// optionalCreateTaskFields are CreateTask payload fields the script
// vertex_alive-checks but that are NOT dispatched by either internal engine
// today (FR28's `queue`: Weaver's actionAssignTask and Loom's submitUserTask
// always name a concrete `assignee`, never a role-queue fallback). Exempted
// from the exact-match below so a genuinely optional, caller-provided
// alternative endpoint doesn't force a phantom always-declared read onto
// every engine dispatch. A future engine that DOES dispatch a queue-targeted
// CreateTask must declare "queue" in its own ContextHint.Reads (the script's
// vertex_alive(state, queue) check is real — kv.Read's HydrationMiss-if-absent
// applies exactly as it does for `assignee`); it just isn't proven here until
// one exists.
var optionalCreateTaskFields = map[string]struct{}{"queue": {}}

// TestCreateTaskReads_MatchDDLScript asserts the engine-dispatched CreateTask
// read-set equals exactly the set of payload fields the task DDL's CreateTask
// branch validates with vertex_alive — no more (the engine would over-hydrate),
// no fewer (the op would HydrationMiss and fail closed) — modulo
// optionalCreateTaskFields (§ above).
func TestCreateTaskReads_MatchDDLScript(t *testing.T) {
	all := vertexAlivePayloadFields(t, taskDDLScript, "CreateTask")
	got := make([]string, 0, len(all))
	for _, f := range all {
		if _, exempt := optionalCreateTaskFields[f]; exempt {
			continue
		}
		got = append(got, f)
	}
	assertSameStringSet(t, "CreateTask", engineCreateTaskReads, got)
}

// engineMarkExpiredReads is the BARE-key read-set Weaver's temporal lane declares
// in ContextHint.Reads when it dispatches MarkExpired (internal/weaver/temporal.go:
// reads = []string{p.EntityKey}). Keyed by the payload field the engine emits.
var engineMarkExpiredReads = []string{"entityKey"}

// TestMarkExpiredReads_MatchDDLScript is the C1 drift guard: the engine-dispatched
// MarkExpired read-set equals exactly the payload fields the freshnessMarker DDL's
// MarkExpired branch validates with vertex_alive (the target-existence guard on the
// entity root). If a future DDL edit adds or drops a vertex_alive check, this fails
// — forcing the temporal-lane read-set to track it rather than silently
// HydrationMiss-ing (too few) or over-hydrating (too many).
func TestMarkExpiredReads_MatchDDLScript(t *testing.T) {
	got := vertexAlivePayloadFields(t, markExpiredDDLScript, "MarkExpired")
	assertSameStringSet(t, "MarkExpired", engineMarkExpiredReads, got)
}

// TestMarkExpiredOptionalReads_ConsultTheHydratedMarker is the merge half of the
// drift guard: the declared absence-tolerant read has to be USED. A script that
// declared the marker and then ignored it would write a whole-document
// overwrite — dropping every sibling target's entry — with the read hydrated for
// nothing and no gate the poorer.
//
// The key's SPELLING is pinned where the two sides can be related to each other
// (internal/weaver's TestFreshnessMarkerAspectSuffix_MatchesTheDDLItDeclares,
// which reads the dispatcher's own constant and this script together); a second
// copy of the suffix here would only restate this side of it.
func TestMarkExpiredOptionalReads_ConsultTheHydratedMarker(t *testing.T) {
	branch := opBranch(t, markExpiredDDLScript, "MarkExpired")
	for _, needed := range []string{"marker_key in state", "state[marker_key]"} {
		if !strings.Contains(branch, needed) {
			t.Fatalf("the MarkExpired branch must consult the hydrated marker (%q missing) — "+
				"without it the write is a whole-document overwrite that drops a sibling target's entry", needed)
		}
	}
}

// vertexAlivePayloadFields parses one op branch of a DDL script and returns the
// set of PAYLOAD FIELDS whose values flow into a vertex_alive(state, <var>)
// check. It maps a local var to its payload field via the
// `<var> = required_string(p, "<field>")` binding (the idiom every DDL in this
// repo uses), so a check on a var bound to a non-payload value (a derived key,
// a constant) is correctly excluded.
//
// branchOp is the operationType whose `if ot == "<branchOp>":` block to scan.
// Scanning is bounded to that block (up to the next `if ot ==` or the trailing
// `fail(`) so a multi-branch script's other ops don't leak in.
func vertexAlivePayloadFields(t *testing.T, script, branchOp string) []string {
	t.Helper()
	branch := opBranch(t, script, branchOp)

	// var -> payload field, from `<var> = required_string(p, "<field>")` and the
	// `required_bare_handle` variant (a bare-handle field is still a payload
	// field, though handles are not vertex_alive-checked in practice).
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

// opBranch returns the substring of script covering the `if ot == "<op>":`
// branch, bounded to the next `if ot ==` (or end of script). Fails the test if
// the branch is absent (a guard against a renamed op silently skipping the
// check).
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

// assertSameStringSet fails if want and got differ as sets.
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
