package main

import (
	"testing"
)

// The Weaver Target Studio's view-logic tier (F25.1): roster ordering, the
// headline, gap/entity badge vocabulary, the map layout geometry, and the
// entity drill's per-gap lines — asserted against the shipped embedded asset
// through the goja harness.
//
// The recurring assertion mirrors the Go tier's: a signal the platform does not
// carry must never render as a clean value. An unsampled contraction class is
// no chip at all (not "steady"); an unobserved gap column is called out rather
// than shown as a green zero; a capped scan says its counts are partial.

func TestWeaverContractionLabel(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	// Absent / unrecognized reads as no chip — the Weaver omits a target from
	// the trajectory map until its ring has samples, and "steady" would be a
	// fabricated all-clear.
	for _, v := range []any{"", "wobbling", nil} {
		if got := call(t, vm, "contractionLabel", v); got != nil {
			t.Errorf("contractionLabel(%v) = %v, want nil", v, got)
		}
	}
	div := call(t, vm, "contractionLabel", "diverging").(map[string]any)
	if div["cls"] != "bad" {
		t.Errorf("diverging cls = %v, want bad", div["cls"])
	}
	mixed := call(t, vm, "contractionLabel", "mixed").(map[string]any)
	if mixed["cls"] != "warn" {
		t.Errorf("mixed cls = %v, want warn", mixed["cls"])
	}
}

func weaverRosterBody() map[string]any {
	return map[string]any{
		"targets": []any{
			map[string]any{"targetId": "quiet", "state": "active", "gaps": 2},
			map[string]any{"targetId": "frozen", "state": "active", "gaps": 1, "frozen": true},
			map[string]any{"targetId": "growing", "state": "active", "gaps": 1, "contraction": "diverging"},
			map[string]any{"targetId": "off", "state": "disabled", "gaps": 1},
			map[string]any{"targetId": "noisy", "state": "active", "gaps": 1, "issues": 2},
		},
		"orphanControl": []any{"ghost"},
	}
}

func TestWeaverTargetRowsOrdering(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	rows := call(t, vm, "targetRows", weaverRosterBody()).([]any)
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.(map[string]any)["targetId"].(string))
	}
	// Frozen (the engine stopped remediating) → diverging → the disabled band
	// (which the orphan marker joins, alphabetically) → issue-carrying → quiet.
	want := []string{"frozen", "growing", "ghost", "off", "noisy", "quiet"}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row order = %v, want %v", got, want)
		}
	}
	// An orphan marker is not a registered target and must say so — a card
	// that offered a drill-in would 404 on a target that does not exist.
	last := rows[2].(map[string]any)
	if last["orphan"] != true || last["registered"] != false {
		t.Errorf("orphan row = %v, want orphan + unregistered", last)
	}
}

func TestWeaverRosterHeadline(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	body := weaverRosterBody()
	rows := call(t, vm, "targetRows", body)
	line := call(t, vm, "rosterHeadline", body, rows).(string)
	for _, want := range []string{"5 targets", "1 frozen", "1 diverging", "1 disabled", "orphan __control marker"} {
		if !containsSub(line, want) {
			t.Errorf("headline %q missing %q", line, want)
		}
	}
	// The orphan count must not be folded into the registered-target count:
	// a marker is not a target.
	if containsSub(line, "6 targets") {
		t.Errorf("headline %q counted the orphan marker as a target", line)
	}
	// A control plane that did not answer is said outright — an empty roster
	// and a silent engine are different facts.
	dead := map[string]any{"listError": "no responders"}
	if got := call(t, vm, "rosterHeadline", dead, []any{}).(string); !containsSub(got, "did not answer") {
		t.Errorf("headline for a dead control plane = %q", got)
	}
	// An orphan scan that failed says so rather than reporting zero orphans.
	partial := map[string]any{"targets": []any{}, "stateError": "list weaver-state: timeout"}
	if got := call(t, vm, "rosterHeadline", partial, []any{}).(string); !containsSub(got, "orphan scan unavailable") {
		t.Errorf("headline with a failed state scan = %q", got)
	}
}

func TestWeaverGapBadges(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	// A converged gap shows its zero-open (the number being watched) and
	// nothing else — no quiet "0 in flight" on every healthy gap.
	clean := call(t, vm, "gapBadges", map[string]any{
		"open": 0, "inflight": 0, "exhausted": 0, "observed": true,
	}).([]any)
	if len(clean) != 1 || clean[0].(map[string]any)["cls"] != "ok" {
		t.Errorf("clean gap badges = %v, want a single ok chip", clean)
	}
	bad := call(t, vm, "gapBadges", map[string]any{
		"open": 3, "inflight": 1, "exhausted": 2, "observed": false,
	}).([]any)
	if len(bad) != 4 {
		t.Fatalf("badges = %v, want open + in-flight + exhausted + unobserved", bad)
	}
	// The unobserved chip is a warning, not an error: a lens with no candidate
	// entities legitimately has no rows to observe the column in.
	last := bad[3].(map[string]any)
	if last["cls"] != "warn" || !containsSub(last["text"].(string), "never observed") {
		t.Errorf("unobserved chip = %v, want a warn-level 'never observed'", last)
	}
}

func TestWeaverGapNodeCls(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	cases := []struct {
		gap  map[string]any
		want string
	}{
		// Exhaustion outranks open count: an open gap is converging, an
		// exhausted one has stopped.
		{map[string]any{"exhausted": 1, "open": 5, "observed": true}, "bad"},
		{map[string]any{"exhausted": 0, "open": 0, "observed": false}, "unbound"},
		{map[string]any{"exhausted": 0, "open": 2, "observed": true}, "warn"},
		{map[string]any{"exhausted": 0, "open": 0, "observed": true}, "ok"},
	}
	for _, c := range cases {
		if got := call(t, vm, "gapNodeCls", c.gap); got != c.want {
			t.Errorf("gapNodeCls(%v) = %v, want %v", c.gap, got, c.want)
		}
	}
}

func TestWeaverDispatchLabelAndSummary(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	explicit := map[string]any{
		"dispatch":   "action",
		"action":     map[string]any{"action": "triggerLoom", "pattern": "onboarding", "subject": "row.applicant"},
		"candidates": []any{map[string]any{"action": "directOp"}},
	}
	// The explicit action wins over a candidates list — that is the engine's
	// own resolution order, and the label must not imply a planner choice.
	if got := call(t, vm, "dispatchLabel", explicit); got != "triggerLoom" {
		t.Errorf("dispatchLabel = %v, want triggerLoom", got)
	}
	planner := map[string]any{"dispatch": "candidates", "candidates": []any{map[string]any{}, map[string]any{}}}
	if got := call(t, vm, "dispatchLabel", planner).(string); !containsSub(got, "2 candidates") {
		t.Errorf("dispatchLabel = %v, want the candidate count", got)
	}
	if got := call(t, vm, "dispatchLabel", map[string]any{"dispatch": "none"}).(string); !containsSub(got, "no dispatch") {
		t.Errorf("dispatchLabel(none) = %v", got)
	}
	if got := call(t, vm, "actionSummary",
		map[string]any{"action": "surface", "issueCode": "UnroutedTasks"}).(string); !containsSub(got, "UnroutedTasks") {
		t.Errorf("actionSummary(surface) = %v", got)
	}
}

func TestWeaverUnboundBindings(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	got := call(t, vm, "unboundBindings", map[string]any{"bindings": []any{
		map[string]any{"param": "subject", "column": "applicant", "observed": true},
		map[string]any{"param": "params.amount", "column": "amountCents", "observed": false},
	}}).([]any)
	if len(got) != 1 || got[0].(map[string]any)["column"] != "amountCents" {
		t.Errorf("unboundBindings = %v, want only the unobserved one", got)
	}
	if n := len(call(t, vm, "unboundBindings", map[string]any{}).([]any)); n != 0 {
		t.Errorf("unboundBindings(no bindings) = %d, want 0", n)
	}
}

func TestWeaverMapLayout(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	detail := map[string]any{
		"targetId": "leaseComplete", "state": "active", "registered": true,
		"lensRef": "lensAAA", "lensName": "leaseViolations",
		"gaps": []any{
			map[string]any{"column": "missing_bgcheck", "dispatch": "action", "open": 2, "inflight": 1,
				"observed": true, "exhausted": 0,
				"action": map[string]any{"action": "triggerLoom", "pattern": "bg", "patternKnown": true,
					"patternRef": "vtx.meta.pAAA", "subject": "row.applicant"}},
			map[string]any{"column": "missing_sig", "dispatch": "none", "open": 0, "inflight": 0,
				"observed": false, "exhausted": 0},
		},
	}
	l := call(t, vm, "mapLayout", detail).(map[string]any)
	nodes := l["nodes"].([]any)
	// lens + target + one gap node and one action node per gap.
	if len(nodes) != 2+2*2 {
		t.Fatalf("nodes = %d, want 6", len(nodes))
	}
	edges := l["edges"].([]any)
	if len(edges) != 1+2*2 {
		t.Fatalf("edges = %d, want 5", len(edges))
	}
	byID := map[string]map[string]any{}
	for _, n := range nodes {
		m := n.(map[string]any)
		byID[m["id"].(string)] = m
	}
	// Every edge endpoint must name a real node — a dangling edge would paint
	// a line to nowhere. Edges carry no label: the inter-column gap is 20-30px,
	// so a centred label is clipped by both boxes.
	for _, e := range edges {
		m := e.(map[string]any)
		if byID[m["from"].(string)] == nil || byID[m["to"].(string)] == nil {
			t.Fatalf("edge %v names a node that does not exist", m)
		}
		if _, labelled := m["label"]; labelled {
			t.Errorf("edge %v carries a label; there is no room to draw one", m)
		}
	}
	if byID["lens"]["label"] != "leaseViolations" {
		t.Errorf("lens node label = %v, want the resolved name", byID["lens"]["label"])
	}
	if byID["gap:missing_sig"]["cls"] != "unbound" {
		t.Errorf("unobserved gap node cls = %v, want unbound", byID["gap:missing_sig"]["cls"])
	}
	// Only a resolved pattern gets a click target — a link to an uninstalled
	// pattern's meta key would go nowhere.
	if byID["act:missing_bgcheck"]["href"] != "#/graph/vtx.meta.pAAA" {
		t.Errorf("action node href = %v", byID["act:missing_bgcheck"]["href"])
	}
	if byID["act:missing_sig"]["href"] != "" {
		t.Errorf("unbound action node href = %v, want empty", byID["act:missing_sig"]["href"])
	}
	// A gapless target still lays out (one row of vertical space), so the map
	// never collapses to a zero-height SVG.
	empty := call(t, vm, "mapLayout", map[string]any{"targetId": "t", "gaps": []any{}}).(map[string]any)
	if numVal(t, empty["height"]) <= 0 {
		t.Errorf("empty layout height = %v, want > 0", empty["height"])
	}
	if l0 := call(t, vm, "mapLayout", nil).(map[string]any); len(l0["nodes"].([]any)) != 2 {
		t.Errorf("nil detail layout = %v, want the lens + target nodes", l0["nodes"])
	}
}

// SVG text neither wraps nor clips to its box, so a label wider than its node
// runs straight over the next column — a target and its like-named violation
// lens collided on the first live render. The layout ellipses instead.
func TestWeaverFitLabel(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	// Fits → untouched.
	if got := call(t, vm, "fitLabel", "short", 200, 6.9); got != "short" {
		t.Errorf("fitLabel(short) = %v, want it untouched", got)
	}
	long := call(t, vm, "fitLabel", "leaseApplicationCompleteAndThenSome", 160, 6.9).(string)
	if len(long) >= len("leaseApplicationCompleteAndThenSome") {
		t.Errorf("fitLabel did not truncate: %q", long)
	}
	if !containsSub(long, "\u2026") {
		t.Errorf("truncated label %q carries no ellipsis", long)
	}
	// A box too narrow for any glyph yields empty text, never a crash or a
	// negative slice.
	if got := call(t, vm, "fitLabel", "anything", 10, 6.9); got != "" {
		t.Errorf("fitLabel(narrow) = %v, want \"\"", got)
	}
	if got := call(t, vm, "fitLabel", nil, 200, 6.9); got != "" {
		t.Errorf("fitLabel(nil) = %v, want \"\"", got)
	}
}

func TestWeaverMapLayoutTruncatesAndKeepsFullText(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	l := call(t, vm, "mapLayout", map[string]any{
		"targetId": "aTargetIdFarTooLongForItsOwnBox",
		"lensName": "aLensNameFarTooLongForItsOwnBox",
		"lensRef":  "lensAAA",
		"gaps":     []any{},
	}).(map[string]any)
	for _, n := range l["nodes"].([]any) {
		m := n.(map[string]any)
		label := m["label"].(string)
		full, _ := m["full"].(string)
		if full == "" {
			t.Errorf("node %v carries no full text for its tooltip", m["id"])
		}
		if len(label) >= len(full) && label != full {
			t.Errorf("node %v label %q is not a truncation of %q", m["id"], label, full)
		}
	}
}

func TestWeaverEntityBadgesAndRosterNote(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	worst := call(t, vm, "entityBadges", map[string]any{
		"exhausted": []any{"missing_a"}, "open": []any{"missing_a", "missing_b"}, "inflight": []any{"missing_c"},
	}).([]any)
	if len(worst) != 3 || worst[0].(map[string]any)["cls"] != "bad" {
		t.Errorf("badges = %v, want exhausted first", worst)
	}
	// A violating row with no open gap is a real and odd state (the lens flag
	// is not an implicit OR of the gaps) — it must not read as converged.
	odd := call(t, vm, "entityBadges", map[string]any{"violating": true}).([]any)
	if len(odd) != 1 || odd[0].(map[string]any)["cls"] != "warn" {
		t.Errorf("violating-with-no-gap badges = %v, want a warn chip", odd)
	}
	done := call(t, vm, "entityBadges", map[string]any{"violating": false}).([]any)
	if done[0].(map[string]any)["text"] != "converged" {
		t.Errorf("converged badges = %v", done)
	}

	full := call(t, vm, "rosterNote", map[string]any{
		"entities": []any{map[string]any{}}, "rows": 1, "violating": 1, "truncated": false,
	}).(string)
	if containsSub(full, "capped") {
		t.Errorf("uncapped note %q must not claim truncation", full)
	}
	// A capped scan must say its counts are not totals — a partial count read
	// as a total is exactly the false green this surface exists to avoid.
	capped := call(t, vm, "rosterNote", map[string]any{
		"entities": []any{map[string]any{}}, "rows": 2000, "violating": 40, "truncated": true,
	}).(string)
	if !containsSub(capped, "scan capped") || !containsSub(capped, "not the whole target") {
		t.Errorf("capped note = %q", capped)
	}
}

func TestWeaverGapStateLine(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	ex := call(t, vm, "gapStateLine", map[string]any{
		"state": "exhausted", "dispatches": 3, "budget": 3, "budgetKnown": true,
	}).(map[string]any)
	if ex["cls"] != "bad" || ex["budget"] != "3 / 3 dispatches" {
		t.Errorf("exhausted line = %v", ex)
	}
	// An unbounded gap reports its dispatch count with NO denominator — an
	// invented ceiling would turn ordinary retries into a red.
	un := call(t, vm, "gapStateLine", map[string]any{
		"state": "inflight", "dispatches": 7, "budgetKnown": false,
	}).(map[string]any)
	if un["budget"] != "7 dispatches (no declared budget)" {
		t.Errorf("unbounded line = %v", un)
	}
	quiet := call(t, vm, "gapStateLine", map[string]any{"state": "closed", "budgetKnown": false}).(map[string]any)
	if quiet["budget"] != "" || quiet["cls"] != "ok" {
		t.Errorf("closed line = %v, want no budget text", quiet)
	}
	// A CLOSED gap that never dispatched has no episode to report against —
	// its mark and count are deleted on close, so "0 / 3" would be a
	// denominator against nothing rather than a fact about this entity.
	closedKnown := call(t, vm, "gapStateLine", map[string]any{
		"state": "closed", "dispatches": 0, "budget": 3, "budgetKnown": true,
	}).(map[string]any)
	if closedKnown["budget"] != "" {
		t.Errorf("closed-with-no-dispatch line = %v, want no denominator", closedKnown)
	}
	// A closed gap that DID dispatch still reports what it spent.
	closedSpent := call(t, vm, "gapStateLine", map[string]any{
		"state": "closed", "dispatches": 2, "budget": 3, "budgetKnown": true,
	}).(map[string]any)
	if closedSpent["budget"] != "2 / 3 dispatches" {
		t.Errorf("closed-after-dispatch line = %v", closedSpent)
	}
}

func TestWeaverArtifactLine(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	if got := call(t, vm, "artifactLine", nil); got != nil {
		t.Errorf("artifactLine(nil) = %v, want nil", got)
	}
	live := call(t, vm, "artifactLine", map[string]any{
		"kind": "flow", "id": "iAAA", "href": "#/flows/iAAA", "live": true,
	}).(map[string]any)
	if live["kind"] != "Loom instance" || !containsSub(live["note"].(string), "live in the engine") {
		t.Errorf("live artifact = %v", live)
	}
	// A dead-in-the-engine artifact keeps its link: the Chronicler's flow
	// history outlives Loom's live record, so suppressing the link would hide
	// history that exists. The label carries the caveat instead.
	dead := call(t, vm, "artifactLine", map[string]any{
		"kind": "flow", "id": "iAAA", "href": "#/flows/iAAA", "live": false,
	}).(map[string]any)
	if dead["href"] != "#/flows/iAAA" {
		t.Errorf("dead artifact lost its link: %v", dead)
	}
	if !containsSub(dead["note"].(string), "terminated") {
		t.Errorf("dead artifact note = %v, want the terminated/drift caveat", dead["note"])
	}
}

func TestWeaverMarkLine(t *testing.T) {
	vm := logicVM(t, "weaver.js")
	if got := call(t, vm, "markLine", nil); got != nil {
		t.Errorf("markLine(nil) = %v, want nil", got)
	}
	// A legacy mark carrying no recorded action says so rather than rendering
	// a blank where a dispatch kind belongs.
	m := call(t, vm, "markLine", map[string]any{"claimId": "c"}).(map[string]any)
	if m["action"] != "(unrecorded)" {
		t.Errorf("markLine action = %v, want (unrecorded)", m["action"])
	}
}

func containsSub(s, sub string) bool {
	return len(sub) == 0 || indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
