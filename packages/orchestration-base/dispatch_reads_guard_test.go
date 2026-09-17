package orchestrationbase

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// engineCreateTaskReadsByArm is the BARE-key read-set the engines declare in
// ContextHint.Reads when they dispatch CreateTask, per routing arm. Weaver's
// assignTask (internal/weaver/strategist.go) dispatches either arm — the
// "assignee" arm names a concrete identity, the "queue" arm a role queue
// (buildQueuedTaskPlan); Loom's submitUserTask dispatches the assignee arm
// only. Each set is keyed by PAYLOAD FIELD (the engines emit the field's
// value); the drift guard maps these to the script to prove they match what
// the DDL hydrates.
//
// This is the engine↔DDL read-set contract for CreateTask. If a future task-DDL
// edit adds or drops a vertex_alive check, this test fails — forcing the engine
// dispatch read-set to be updated in lock-step rather than silently failing
// closed (a HydrationMiss) or wastefully over-hydrating (L2).
var engineCreateTaskReadsByArm = map[string][]string{
	"assignee": {"assignee", "forOperation", "scopedTo"},
	"queue":    {"queue", "forOperation", "scopedTo"},
}

// TestCreateTaskReads_MatchDDLScript asserts the engine-dispatched CreateTask
// read-sets cover exactly the set of payload fields the task DDL's CreateTask
// branch validates with vertex_alive: the UNION over the arms equals the
// script's set (no field the script checks goes undeclared by every arm, and
// no arm declares a field the script never checks), and each arm declares its
// own endpoint plus the two link endpoints common to every CreateTask — no
// more (the engine would over-hydrate), no fewer (the op would HydrationMiss
// and fail closed).
func TestCreateTaskReads_MatchDDLScript(t *testing.T) {
	ddl := vertexAlivePayloadFields(t, taskDDLScript, "CreateTask")

	union := map[string]struct{}{}
	for arm, reads := range engineCreateTaskReadsByArm {
		want := []string{arm, "forOperation", "scopedTo"}
		assertSameStringSet(t, "CreateTask "+arm+" arm", want, reads)
		for _, r := range reads {
			union[r] = struct{}{}
		}
	}
	got := make([]string, 0, len(union))
	for f := range union {
		got = append(got, f)
	}
	assertSameStringSet(t, "CreateTask (union over arms)", ddl, got)
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
