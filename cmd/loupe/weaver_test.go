package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// The Weaver Target Studio's model tier (F25.1): the weaver-state key split,
// the row scan, the retry-budget rule, the target/entity joins and the
// artifact-id derivations.
//
// The assertion that recurs here is the honesty rule the design pins: a signal
// the platform does not carry must not read as a clean value. A gap column no
// row carries is "not observed", not "false"; a retry budget nothing declares
// is unknown, not zero; a truncated scan's counts are not totals.

func TestSplitWeaverStateKeys(t *testing.T) {
	got := splitWeaverStateKeys("leaseComplete", []string{
		"leaseComplete.Lk2Pn6mQrtwzKbcXvP3T.missing_bgcheck",
		"leaseComplete.Lk2Pn6mQrtwzKbcXvP3T.missing_bgcheck.__count",
		"leaseComplete.Lk2Pn6mQrtwzKbcXvP3T.missing_payment",
		"leaseComplete.__control",
		"leaseComplete.__effect.missing_bgcheck.runCheck",
		// A different target's keys share the bucket and must not leak in.
		"otherTarget.Lk2Pn6mQrtwzKbcXvP3T.missing_x",
		"otherTarget.__control",
	})
	if !got.Control {
		t.Error("__control marker not detected")
	}
	marks := got.Marks["Lk2Pn6mQrtwzKbcXvP3T"]
	if len(marks) != 2 || marks["missing_bgcheck"] == "" || marks["missing_payment"] == "" {
		t.Errorf("marks = %v, want the two gap marks", marks)
	}
	counts := got.Counts["Lk2Pn6mQrtwzKbcXvP3T"]
	if len(counts) != 1 || counts["missing_bgcheck"] == "" {
		t.Errorf("counts = %v, want the one __count key", counts)
	}
	// The __effect key is per-(gap, action) bookkeeping and shares the mark's
	// segment count with nothing — it must never be mistaken for entity state.
	if _, leaked := got.Marks["__effect"]; leaked {
		t.Error("__effect key parsed as a mark")
	}
	if _, leaked := got.Counts["__effect"]; leaked {
		t.Error("__effect key parsed as a count")
	}
}

func TestOrphanControlMarkers(t *testing.T) {
	keys := []string{
		"live.__control",
		"ghost.__control",
		"live.Lk2Pn6mQrtwzKbcXvP3T.missing_x",
		// A mark whose gap column happens to be named __control cannot exist
		// (columns carry the missing_ prefix), but a 3-segment key must not
		// be read as an orphan regardless.
		"live.Lk2Pn6mQrtwzKbcXvP3T.__control",
	}
	got := orphanControlMarkers(keys, map[string]bool{"live": true})
	want := []string{"ghost"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orphanControlMarkers = %v, want %v", got, want)
	}
}

func TestScanWeaverRowsCountsAndColumns(t *testing.T) {
	docs := map[string]map[string]any{
		"aaaaaaaaaaaaaaaaaaaa": {
			"entityKey": "vtx.leaseApp.aaaaaaaaaaaaaaaaaaaa",
			"violating": true,
			// json.Unmarshal yields float64, which is what the engine's own
			// rows decode to — the budget reader must accept it.
			"maxretries_bgcheck": float64(2),
			"missing_bgcheck":    true,
			"missing_payment":    false,
			"applicant":          "vtx.identity.bbbbbbbbbbbbbbbbbbbb",
		},
		"cccccccccccccccccccc": {
			"entityKey":       "vtx.leaseApp.cccccccccccccccccccc",
			"violating":       false,
			"missing_bgcheck": false,
			"missing_payment": false,
		},
	}
	scan := scanWeaverRows(docs, []string{"aaaaaaaaaaaaaaaaaaaa", "cccccccccccccccccccc"}, false)
	if scan.Rows != 2 || scan.Violating != 1 {
		t.Errorf("rows/violating = %d/%d, want 2/1", scan.Rows, scan.Violating)
	}
	if scan.OpenByGap["missing_bgcheck"] != 1 || scan.OpenByGap["missing_payment"] != 0 {
		t.Errorf("OpenByGap = %v", scan.OpenByGap)
	}
	// maxretries_ is a dispatch-suppression companion, not a gap: it must
	// never enter the gap scan (it carries no missing_ prefix) but must be
	// readable as this entity's declared budget.
	if _, isGap := scan.OpenByGap["maxretries_bgcheck"]; isGap {
		t.Error("maxretries_ column counted as a gap")
	}
	if got := scan.Budgets["aaaaaaaaaaaaaaaaaaaa"]["missing_bgcheck"]; got != 2 {
		t.Errorf("declared budget = %d, want 2", got)
	}
	if !scan.Columns["applicant"] || !scan.Columns["violating"] {
		t.Errorf("columns = %v, want the param + flag columns observed", scan.Columns)
	}
}

// A row that failed to decode still counts toward the total: dropping it
// silently would understate the candidate set the counts are taken over.
func TestScanWeaverRowsCountsUndecodableRows(t *testing.T) {
	scan := scanWeaverRows(map[string]map[string]any{"aaaaaaaaaaaaaaaaaaaa": nil},
		[]string{"aaaaaaaaaaaaaaaaaaaa"}, true)
	if scan.Rows != 1 || scan.Violating != 0 || !scan.Truncated {
		t.Errorf("scan = %+v, want 1 row, 0 violating, truncated", scan)
	}
}

func TestGapBudget(t *testing.T) {
	// A declared ceiling always wins, however small.
	if n, ok := gapBudget(map[string]int{"missing_x": 1}, "missing_x", "triggerLoom"); !ok || n != 1 {
		t.Errorf("declared budget = %d/%v, want 1/true", n, ok)
	}
	// A directOp gap with no declared companion falls back to the engine's
	// default (Contract #10 §10.2) — a loud stop, never a silent park.
	if n, ok := gapBudget(nil, "missing_x", "directOp"); !ok || n != weaverDefaultDirectOpBudget {
		t.Errorf("directOp fallback = %d/%v, want %d/true", n, ok, weaverDefaultDirectOpBudget)
	}
	// Every other undeclared gap is unbounded, and must read as UNKNOWN.
	// Reading it as a zero ceiling would mark every dispatched gap exhausted.
	if n, ok := gapBudget(nil, "missing_x", "triggerLoom"); ok {
		t.Errorf("undeclared non-directOp budget = %d/%v, want unknown", n, ok)
	}
	if _, ok := gapBudget(map[string]int{"missing_x": 0}, "missing_x", "assignTask"); ok {
		t.Error("a non-positive declared maxretries must read as undeclared, not as a zero ceiling")
	}
}

func TestMessageNamesToken(t *testing.T) {
	cases := []struct {
		msg, token string
		want       bool
	}{
		{"target leaseComplete gap missing_x: boom", "leaseComplete", true},
		{"target leaseComplete: row column missing_x is true", "leaseComplete", true},
		// A prefix collision must NOT claim a sibling target's issue.
		{"target leaseCompleteRenewal gap missing_x: boom", "leaseComplete", false},
		{"target xleaseComplete gap missing_x: boom", "leaseComplete", false},
		{"unrelated message", "leaseComplete", false},
		{"target leaseComplete", "leaseComplete", true},
		{"", "leaseComplete", false},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := messageNamesToken(c.msg, c.token); got != c.want {
			t.Errorf("messageNamesToken(%q, %q) = %v, want %v", c.msg, c.token, got, c.want)
		}
	}
}

func TestReadWeaverHeartbeatsAndContraction(t *testing.T) {
	docs := map[string]map[string]any{
		"health.weaver.inst-a": {
			"metrics": map[string]any{"contractionTrajectory": map[string]any{
				"t1": "shrinking", "t2": "diverging",
			}},
			"issues": []any{
				map[string]any{"code": "TargetOscillation", "severity": "error",
					"message": "target t2 and target t3 assert the same path", "since": "2026-08-01T00:00:00Z"},
			},
		},
		"health.weaver.inst-b": {
			"metrics": map[string]any{"contractionTrajectory": map[string]any{"t1": "diverging"}},
		},
		"health.loom.inst-a": {"metrics": map[string]any{"contractionTrajectory": map[string]any{"t1": "steady"}}},
	}
	keys := []string{"health.weaver.inst-a", "health.weaver.inst-b", "health.loom.inst-a"}
	hbs := readWeaverHeartbeats(keys, func(k string) (map[string]any, bool) {
		d, ok := docs[k]
		return d, ok
	})
	if len(hbs) != 2 {
		t.Fatalf("heartbeats = %d, want the 2 weaver instances only", len(hbs))
	}
	// Two live instances disagreeing is its own class — the ring is
	// per-process, so merging them would report a class no process holds.
	if got := contractionFor(hbs, "t1"); got != "mixed" {
		t.Errorf("contractionFor(t1) = %q, want mixed", got)
	}
	if got := contractionFor(hbs, "t2"); got != "diverging" {
		t.Errorf("contractionFor(t2) = %q, want diverging", got)
	}
	// A target no instance has sampled reads as absent, never as steady.
	if got := contractionFor(hbs, "t9"); got != "" {
		t.Errorf("contractionFor(unsampled) = %q, want \"\"", got)
	}
	if !frozenBy(issuesNaming(hbs, "t2")) {
		t.Error("t2 should read as oscillation-frozen")
	}
	if frozenBy(issuesNaming(hbs, "t1")) {
		t.Error("t1 is not named by the oscillation issue and must not read as frozen")
	}
}

const leaseTargetSpec = `{
  "targetId": "leaseComplete",
  "lensRef": "lensAAAAAAAAAAAAAAAA",
  "mode": "planned",
  "gaps": {
    "missing_bgcheck": {"action": "triggerLoom", "pattern": "backgroundCheck", "subject": "row.applicant"},
    "missing_signature": {"action": "assignTask", "operation": "SignLease",
                          "assignee": "row.applicant", "target": "row.entityKey"},
    "missing_notify": {"candidates": [{"action": "directOp", "operation": "Notify", "cost": 2}]}
  }
}`

func parseLeaseTarget(t *testing.T) *weaverTargetBody {
	t.Helper()
	var body weaverTargetBody
	if err := json.Unmarshal([]byte(leaseTargetSpec), &body); err != nil {
		t.Fatalf("parse target spec: %v", err)
	}
	return &body
}

func TestDispatchKindPrefersExplicitAction(t *testing.T) {
	body := parseLeaseTarget(t)
	if got := body.Gaps["missing_bgcheck"].dispatchKind(); got != "action" {
		t.Errorf("dispatchKind = %q, want action", got)
	}
	if got := body.Gaps["missing_notify"].dispatchKind(); got != "candidates" {
		t.Errorf("dispatchKind = %q, want candidates", got)
	}
	// An explicit action always wins over a candidates list — the engine
	// consults candidates only when Action is empty.
	both := weaverGapAction{
		weaverActionContract: weaverActionContract{Action: "directOp"},
		Candidates:           []weaverActionContract{{Action: "assignTask"}},
	}
	if got := both.dispatchKind(); got != "action" {
		t.Errorf("dispatchKind with both = %q, want action", got)
	}
	if got := (weaverGapAction{}).dispatchKind(); got != "none" {
		t.Errorf("dispatchKind of an empty gap = %q, want none", got)
	}
}

func TestRenderActionBindings(t *testing.T) {
	a := weaverActionContract{
		Action: "assignTask", Operation: "SignLease",
		Assignee: "row.applicant", Target: "row.entityKey",
		Params: map[string]string{"amount": "row.amountCents", "note": "literal text"},
	}
	got := renderAction(a, map[string]bool{"applicant": true, "entityKey": true}, nil)
	if len(got.Bindings) != 3 {
		t.Fatalf("bindings = %+v, want the three row.<column> refs (a literal is not a binding)", got.Bindings)
	}
	byCol := map[string]bool{}
	for _, b := range got.Bindings {
		byCol[b.Column] = b.Observed
	}
	if !byCol["applicant"] || !byCol["entityKey"] {
		t.Errorf("observed columns not marked: %+v", got.Bindings)
	}
	if byCol["amountCents"] {
		t.Error("amountCents is not in the observed set and must not read as bound")
	}
}

// `reads` alone accepts the strategist's derived-aspect form
// `row.<column>.<aspect>` — the column resolves to a vertex root key and the
// aspect is joined onto it (strategist.go resolveReadKey). Params and the
// subject/assignee/target slots stay EXACT column lookups, because a param
// value is not necessarily a composable key. Without that split, the live
// loftspace target's working read `row.unitKey.listing` painted a false
// "unobserved binding" red on a healthy gap (caught live, 2026-08-02).
func TestRenderActionReadsAcceptDerivedAspect(t *testing.T) {
	observed := map[string]bool{"unitKey": true}
	got := renderAction(weaverActionContract{
		Action:    "directOp",
		Operation: "SetListingStatus",
		Params:    map[string]string{"unit": "row.unitKey", "status": "leased"},
		Reads:     []string{"row.unitKey", "row.unitKey.listing"},
	}, observed, nil)

	byParam := map[string]weaverBinding{}
	for _, b := range got.Bindings {
		byParam[b.Param] = b
	}
	// The literal "leased" is not a binding at all.
	if len(byParam) != 3 {
		t.Fatalf("bindings = %+v, want the three row.<…> refs only", got.Bindings)
	}
	derived := byParam["reads.row.unitKey.listing"]
	if !derived.Observed || derived.Column != "unitKey" || derived.Aspect != "listing" {
		t.Errorf("derived read binding = %+v, want observed on column unitKey, aspect listing", derived)
	}
	if !byParam["params.unit"].Observed {
		t.Errorf("exact param binding = %+v, want observed", byParam["params.unit"])
	}

	// The same dotted text in a PARAM is a column that genuinely does not
	// exist — the fallback must not leak across the slot boundary.
	param := renderAction(weaverActionContract{
		Action: "directOp",
		Params: map[string]string{"unit": "row.unitKey.listing"},
	}, observed, nil)
	if len(param.Bindings) != 1 || param.Bindings[0].Observed || param.Bindings[0].Aspect != "" {
		t.Errorf("param binding = %+v, want an unobserved exact-column lookup", param.Bindings)
	}
}

func TestRenderActionResolvesPattern(t *testing.T) {
	patterns := map[string]string{"backgroundCheck": "vtx.meta.pAAAAAAAAAAAAAAAAAAA"}
	got := renderAction(weaverActionContract{Action: "triggerLoom", Pattern: "backgroundCheck"}, nil, patterns)
	if !got.PatternKnown || got.PatternRef != patterns["backgroundCheck"] {
		t.Errorf("pattern not resolved: %+v", got)
	}
	// An uninstalled pattern must read as unknown rather than fabricating a
	// meta key that does not exist.
	missing := renderAction(weaverActionContract{Action: "triggerLoom", Pattern: "ghost"}, nil, patterns)
	if missing.PatternKnown || missing.PatternRef != "" {
		t.Errorf("uninstalled pattern read as known: %+v", missing)
	}
}

func TestBuildTargetDetailJoin(t *testing.T) {
	body := parseLeaseTarget(t)
	docs := map[string]map[string]any{
		"aaaaaaaaaaaaaaaaaaaa": {
			"entityKey": "vtx.leaseApp.aaaaaaaaaaaaaaaaaaaa", "violating": true,
			"missing_bgcheck": true, "missing_signature": true, "applicant": "vtx.identity.b",
			"maxretries_bgcheck": float64(2),
			// A live column with no gaps entry — the engine's own
			// GapWithoutPlaybook condition, made visible statically.
			"missing_orphaned": true,
		},
		"cccccccccccccccccccc": {
			"entityKey": "vtx.leaseApp.cccccccccccccccccccc", "violating": false,
			"missing_bgcheck": false, "missing_signature": false,
		},
	}
	scan := scanWeaverRows(docs, []string{"aaaaaaaaaaaaaaaaaaaa", "cccccccccccccccccccc"}, false)
	state := splitWeaverStateKeys("leaseComplete", []string{
		"leaseComplete.aaaaaaaaaaaaaaaaaaaa.missing_signature",
		"leaseComplete.aaaaaaaaaaaaaaaaaaaa.missing_bgcheck.__count",
	})
	counts := map[string]map[string]int{"aaaaaaaaaaaaaaaaaaaa": {"missing_bgcheck": 2}}
	summary := &weaverTargetSummary{TargetID: "leaseComplete", LensRef: "lensAAAAAAAAAAAAAAAA", State: "active",
		Gaps: []string{"missing_bgcheck", "missing_notify", "missing_signature"}}

	d := buildTargetDetail("leaseComplete", body, "vtx.meta.tAAAAAAAAAAAAAAAAAAA", "leaseViolations", "",
		summary, scan, state, counts, map[string]string{"backgroundCheck": "vtx.meta.pAAA"}, nil, nil)

	if !d.Registered || d.State != "active" || d.Mode != "planned" {
		t.Errorf("header = %+v", d)
	}
	if len(d.Gaps) != 3 {
		t.Fatalf("gaps = %d, want 3", len(d.Gaps))
	}
	byCol := map[string]weaverGap{}
	for _, g := range d.Gaps {
		byCol[g.Column] = g
	}
	if g := byCol["missing_bgcheck"]; g.Open != 1 || g.Inflight != 0 || g.Exhausted != 1 {
		t.Errorf("missing_bgcheck = open %d / inflight %d / exhausted %d, want 1/0/1", g.Open, g.Inflight, g.Exhausted)
	}
	if g := byCol["missing_signature"]; g.Open != 1 || g.Inflight != 1 {
		t.Errorf("missing_signature = open %d / inflight %d, want 1/1", g.Open, g.Inflight)
	}
	// A gap column no row carries reads as unobserved — not as a zero-open
	// green, which is the false comfort the design forbids.
	if g := byCol["missing_notify"]; g.Observed {
		t.Error("missing_notify has no live column and must read as unobserved")
	}
	if len(d.Unhandled) != 1 || d.Unhandled[0] != "missing_orphaned" {
		t.Errorf("unhandled = %v, want [missing_orphaned]", d.Unhandled)
	}
	// Worst-first: the entity with an exhausted budget leads.
	if d.Entities[0].EntityID != "aaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("entity order = %+v, want the exhausted one first", d.Entities)
	}
	if len(d.Entities[0].Exhausted) != 1 || d.Entities[0].Exhausted[0] != "missing_bgcheck" {
		t.Errorf("entity exhausted = %v", d.Entities[0].Exhausted)
	}
}

// An unregistered target that still carries rows and a __control marker is a
// real state (a package uninstalled out from under its projections, or an
// orphan trial marker) — the map must render it, and must report the disabled
// marker's consequence rather than an empty "active".
func TestBuildTargetDetailUnregisteredWithControlMarker(t *testing.T) {
	scan := scanWeaverRows(nil, nil, false)
	state := splitWeaverStateKeys("ghost", []string{"ghost.__control"})
	d := buildTargetDetail("ghost", nil, "", "", "", nil, scan, state, nil, nil, nil, nil)
	if d.Registered {
		t.Error("Registered must be false with no control-plane summary")
	}
	if d.State != "disabled" {
		t.Errorf("state = %q, want disabled (the durable marker's meaning)", d.State)
	}
}

// With no readable body the control plane still knows the gap columns, so the
// map degrades to structure-less gap nodes rather than to an empty page.
func TestBuildTargetDetailFallsBackToSummaryGaps(t *testing.T) {
	docs := map[string]map[string]any{"aaaaaaaaaaaaaaaaaaaa": {"missing_x": true}}
	scan := scanWeaverRows(docs, []string{"aaaaaaaaaaaaaaaaaaaa"}, false)
	summary := &weaverTargetSummary{TargetID: "t", State: "active", Gaps: []string{"missing_x"}}
	d := buildTargetDetail("t", nil, "", "", "", summary, scan, weaverStateKeys{}, nil, nil, nil, nil)
	if len(d.Gaps) != 1 || d.Gaps[0].Column != "missing_x" || d.Gaps[0].Dispatch != "none" {
		t.Errorf("gaps = %+v, want one structure-less node", d.Gaps)
	}
	if len(d.Unhandled) != 0 {
		t.Errorf("unhandled = %v — a column the summary declares is handled", d.Unhandled)
	}
}

func TestBuildEntityDetailStates(t *testing.T) {
	body := parseLeaseTarget(t)
	row := map[string]any{
		"entityKey": "vtx.leaseApp.aaaaaaaaaaaaaaaaaaaa", "violating": true,
		"missing_bgcheck": true, "missing_signature": true, "missing_notify": false,
		"maxretries_bgcheck": float64(2),
	}
	marks := map[string]*weaverMark{
		"missing_signature": {Action: "assignTask", ClaimID: "clmAAAAAAAAAAAAAAAAA", LeaseExpiresAt: "2026-08-02T01:00:00Z"},
	}
	counts := map[string]int{"missing_bgcheck": 2}
	d := buildEntityDetail("leaseComplete", "aaaaaaaaaaaaaaaaaaaa", row, body, nil, marks, counts, nil, nil)
	if !d.Found || !d.Violating {
		t.Fatalf("detail = %+v", d)
	}
	byCol := map[string]weaverEntityGap{}
	for _, g := range d.Gaps {
		byCol[g.Column] = g
	}
	if g := byCol["missing_bgcheck"]; g.State != "exhausted" || g.Budget != 2 || !g.BudgetKnown {
		t.Errorf("missing_bgcheck = %+v, want exhausted at 2/2", g)
	}
	if g := byCol["missing_signature"]; g.State != "inflight" || g.Artifact == nil || g.Artifact.Kind != "task" {
		t.Errorf("missing_signature = %+v, want inflight with a task artifact", g)
	}
	if g := byCol["missing_notify"]; g.State != "closed" {
		t.Errorf("missing_notify = %+v, want closed", g)
	}
	// An assignTask gap declares no maxretries and is not a directOp, so its
	// budget is unbounded — unknown, never a ceiling the console invented.
	if g := byCol["missing_signature"]; g.BudgetKnown {
		t.Errorf("missing_signature budget read as known: %+v", g)
	}
}

// A mark or count for a column the playbook no longer declares is real residue
// (a package upgrade dropped the gap while an episode was open) — it must be
// rendered, not silently dropped.
func TestBuildEntityDetailSurfacesUndeclaredResidue(t *testing.T) {
	marks := map[string]*weaverMark{"missing_retired": {Action: "triggerLoom", ClaimID: "c"}}
	d := buildEntityDetail("t", "aaaaaaaaaaaaaaaaaaaa", nil, nil, []string{"missing_live"}, marks, nil, nil, nil)
	cols := map[string]bool{}
	for _, g := range d.Gaps {
		cols[g.Column] = true
	}
	if !cols["missing_retired"] || !cols["missing_live"] {
		t.Errorf("gaps = %v, want both the declared column and the residue", cols)
	}
	// A row-less entity with a live mark is still found: the mark is evidence
	// the entity exists, and a 404 would hide an in-flight remediation.
	if !d.Found {
		t.Error("an entity with marks but no row must still be found")
	}
}

// The id derivations mirror internal/weaver's. Pin their SHAPE and their
// namespace disjointness here; the runtime check that the derived id resolves
// against the engine's own state is what catches a real drift.
func TestDeriveWeaverIDs(t *testing.T) {
	task := deriveWeaverTaskID("t", "e", "missing_x", "claim")
	inst := deriveWeaverInstanceID("t", "e", "missing_x", "claim")
	if len(task) != 20 || len(inst) != 20 {
		t.Errorf("derived ids must be 20-char NanoIDs, got %q / %q", task, inst)
	}
	if task == inst {
		t.Error("task and instance derivations must be namespaced disjoint")
	}
	if deriveWeaverTaskID("t", "e", "missing_x", "claim2") == task {
		t.Error("a fresh claimId must derive a fresh id — a reopened gap is a new artifact")
	}
	// The alphabet comes from substrate, never a local copy: a hand-written
	// superset (one that kept 0/O/I/l) would let a bad character through and
	// the assertion would be decorative.
	for _, c := range task + inst {
		if !strings.ContainsRune(substrate.Alphabet, c) {
			t.Fatalf("derived id uses %q, outside the Contract #1 alphabet", c)
		}
	}
}

func TestMarkArtifactPrefersTheMarksOwnAction(t *testing.T) {
	// The mark records the actionRef chosen at CAS-create, which for a
	// planner-resolved gap need not match the playbook's first entry — so the
	// mark wins over the playbook when both are present.
	a := markArtifact("t", "e", "missing_x", &weaverMark{Action: "triggerLoom", ClaimID: "c"}, "assignTask")
	if a == nil || a.Kind != "flow" {
		t.Fatalf("artifact = %+v, want the mark's own triggerLoom", a)
	}
	// A legacy mark with no recorded action falls back to the playbook's.
	b := markArtifact("t", "e", "missing_x", &weaverMark{ClaimID: "c"}, "assignTask")
	if b == nil || b.Kind != "task" {
		t.Fatalf("artifact = %+v, want the playbook fallback", b)
	}
	// directOp and surface produce no addressable artifact.
	for _, action := range []string{"directOp", "surface", ""} {
		if got := markArtifact("t", "e", "missing_x", &weaverMark{Action: action}, ""); got != nil {
			t.Errorf("action %q produced artifact %+v, want none", action, got)
		}
	}
	if markArtifact("t", "e", "missing_x", nil, "triggerLoom") != nil {
		t.Error("no mark means no in-flight dispatch, so no artifact")
	}
}

func TestParseCountValue(t *testing.T) {
	if n, ok := parseCountValue([]byte("3")); !ok || n != 3 {
		t.Errorf("bare integer = %d/%v", n, ok)
	}
	if n, ok := parseCountValue([]byte(`{"count":4}`)); !ok || n != 4 {
		t.Errorf("object form = %d/%v", n, ok)
	}
	// An unreadable body must report "unknown", not a zero that would read as
	// "no dispatches yet".
	if _, ok := parseCountValue([]byte(`"nonsense"`)); ok {
		t.Error("an unparseable __count must not read as a count")
	}
}

// The index is keyed off the SPEC BODY's own id field, exactly as the engine's
// registry keys it. Two live facts make canonicalName the wrong key: a
// weaverTarget meta carries no canonicalName aspect at all, and the violation
// lens a target binds routinely carries the TARGET's name — on the dev stack
// the lens `leaseApplicationComplete` shares the target id verbatim. A
// name-keyed index resolved the target to the lens vertex, the playbook
// silently failed to parse, and every gap read as an unhandled column. Caught
// live, 2026-08-02.
func TestBuildWeaverMetaIndexKeysOffTheSpecBody(t *testing.T) {
	envelopes := map[string]string{
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.spec": `{"data":` + leaseTargetSpec + `}`,
		"vtx.meta.pAAAAAAAAAAAAAAAAAAA.spec": `{"data":{"patternId":"backgroundCheck","steps":[]}}`,
		// The lens sharing the target's name — indexed by neither, because a
		// lens spec carries no targetId and no patternId.
		"vtx.meta.vAAAAAAAAAAAAAAAAAAA.spec":          `{"data":{"engine":"cypher","cypherRule":"MATCH …"}}`,
		"vtx.meta.vAAAAAAAAAAAAAAAAAAA.canonicalName": `{"data":{"value":"leaseComplete"}}`,
	}
	get := func(k string) ([]byte, bool) {
		v, ok := envelopes[k]
		return []byte(v), ok
	}
	index := buildWeaverMetaIndex([]string{
		// The lens is listed FIRST, so a name-keyed index would have resolved
		// the target id to it.
		"vtx.meta.vAAAAAAAAAAAAAAAAAAA.spec",
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.spec",
		"vtx.meta.pAAAAAAAAAAAAAAAAAAA.spec",
		// A meta with no spec aspect never costs a read.
		"vtx.meta.nAAAAAAAAAAAAAAAAAAA.permittedCommands",
		// A non-meta key under the listing must be ignored outright.
		"vtx.leaseApp.aaaaaaaaaaaaaaaaaaaa.spec",
	}, get)

	if index.Targets["leaseComplete"] != "vtx.meta.tAAAAAAAAAAAAAAAAAAA" || len(index.Targets) != 1 {
		t.Errorf("target index = %v, want only the weaverTarget vertex", index.Targets)
	}
	// The body is parsed from the same read — the playbook opens with no
	// second GET, and an empty Gaps map here is exactly the bug above.
	body := index.Bodies["leaseComplete"]
	if body == nil || len(body.Gaps) != 3 {
		t.Fatalf("indexed body = %+v, want the parsed 3-gap playbook", body)
	}
	// The engine resolves a playbook's pattern ref by patternId OR by the
	// vertex NanoID, so both must resolve — otherwise a playbook authored the
	// second way reads as "pattern not installed".
	if index.Patterns["backgroundCheck"] != "vtx.meta.pAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("pattern index by id = %v", index.Patterns)
	}
	if index.Patterns["pAAAAAAAAAAAAAAAAAAA"] != "vtx.meta.pAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("pattern index by vertex NanoID = %v", index.Patterns)
	}
}

// TestWeaverTargetDescriptionsReadsOnlyDescribedTargets pins both the mapping
// and the scan's cost bound. The roster is the Weaver landing page, so the scan
// opens ONLY metas carrying both a `.spec` and a `.description` — a DDL's
// description (there are hundreds) and an undescribed target must each cost
// zero reads. The `targetId` check is what actually decides identity, so a
// described lens or pattern is excluded on its body, not on its key shape.
func TestWeaverTargetDescriptionsReadsOnlyDescribedTargets(t *testing.T) {
	envelopes := map[string]string{
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.spec":        `{"data":` + leaseTargetSpec + `}`,
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.description": `{"data":{"text":"Every lease application reaches a signed lease."}}`,
		"vtx.meta.uAAAAAAAAAAAAAAAAAAA.spec":        `{"data":{"targetId":"quietTarget","gaps":{}}}`,
		"vtx.meta.pAAAAAAAAAAAAAAAAAAA.spec":        `{"data":{"patternId":"backgroundCheck","steps":[]}}`,
		"vtx.meta.pAAAAAAAAAAAAAAAAAAA.description": `{"data":{"text":"A pattern, not a target."}}`,
		"vtx.meta.dAAAAAAAAAAAAAAAAAAA.description": `{"data":{"text":"A DDL, which carries no spec at all."}}`,
	}
	reads := map[string]int{}
	get := func(k string) ([]byte, bool) {
		reads[k]++
		v, ok := envelopes[k]
		return []byte(v), ok
	}

	got := weaverTargetDescriptions([]string{
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.spec",
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.description",
		"vtx.meta.uAAAAAAAAAAAAAAAAAAA.spec",
		"vtx.meta.pAAAAAAAAAAAAAAAAAAA.spec",
		"vtx.meta.pAAAAAAAAAAAAAAAAAAA.description",
		"vtx.meta.dAAAAAAAAAAAAAAAAAAA.description",
		// A non-meta key under the listing must be ignored outright.
		"vtx.leaseApp.aaaaaaaaaaaaaaaaaaaa.description",
	}, get)

	if len(got) != 1 || got["leaseComplete"] != "Every lease application reaches a signed lease." {
		t.Fatalf("descriptions = %v, want only the described target's prose", got)
	}
	if reads["vtx.meta.uAAAAAAAAAAAAAAAAAAA.spec"] != 0 {
		t.Errorf("an undescribed target's spec was read %d times, want 0", reads["vtx.meta.uAAAAAAAAAAAAAAAAAAA.spec"])
	}
	if reads["vtx.meta.dAAAAAAAAAAAAAAAAAAA.description"] != 0 {
		t.Errorf("a spec-less meta's description was read %d times, want 0", reads["vtx.meta.dAAAAAAAAAAAAAAAAAAA.description"])
	}
	if reads["vtx.meta.pAAAAAAAAAAAAAAAAAAA.description"] != 0 {
		t.Errorf("a pattern's description was read %d times — the targetId check should stop before it", reads["vtx.meta.pAAAAAAAAAAAAAAAAAAA.description"])
	}
}

// TestWeaverTargetDescriptionsSkipsTombstonedAndEmpty: a withdrawn description
// must read as absent, not as the prose it used to carry (metaData's tombstone
// rule, pinned here on the path the roster takes), and a blank body is the same
// as none rather than an empty line on the card.
func TestWeaverTargetDescriptionsSkipsTombstonedAndEmpty(t *testing.T) {
	envelopes := map[string]string{
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.spec":        `{"data":` + leaseTargetSpec + `}`,
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.description": `{"isDeleted":true,"data":{"text":"withdrawn prose"}}`,
		"vtx.meta.uAAAAAAAAAAAAAAAAAAA.spec":        `{"data":{"targetId":"blankTarget","gaps":{}}}`,
		"vtx.meta.uAAAAAAAAAAAAAAAAAAA.description": `{"data":{"text":""}}`,
	}
	get := func(k string) ([]byte, bool) {
		v, ok := envelopes[k]
		return []byte(v), ok
	}
	got := weaverTargetDescriptions([]string{
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.spec",
		"vtx.meta.tAAAAAAAAAAAAAAAAAAA.description",
		"vtx.meta.uAAAAAAAAAAAAAAAAAAA.spec",
		"vtx.meta.uAAAAAAAAAAAAAAAAAAA.description",
	}, get)
	if len(got) != 0 {
		t.Errorf("descriptions = %v, want none (one tombstoned, one blank)", got)
	}
}

// TestBuildTargetDetailCarriesDescription pins the prose onto the detail
// payload — the same text the roster card shows, so the two surfaces never
// disagree about what a target is for.
func TestBuildTargetDetailCarriesDescription(t *testing.T) {
	d := buildTargetDetail("t", nil, "vtx.meta.tAAAAAAAAAAAAAAAAAAA", "", "Every tab settles.",
		nil, weaverRowScan{}, weaverStateKeys{}, nil, nil, nil, nil)
	if d.Description != "Every tab settles." {
		t.Errorf("detail description = %q", d.Description)
	}
}
