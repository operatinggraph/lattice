package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// The goja logic tier (loupe-fe-test-strategy-design.md Fire 1): the pure
// web/js/logic/*.js modules are loaded from the same embed.FS the server
// serves — so these tests assert the SHIPPED assets — via the strip-export
// transform (goja has no ES-module support; a logic file is declarations plus
// one trailing export statement). Assertions are Go-authored tables; a syntax
// gap outside goja's ES6 subset fails loudly at RunString, never ships.

// stripExport applies the strip-export transform and ENFORCES the logic-file
// convention while doing so: no import lines at all, and exactly one
// single-line `export { … };` statement (goja has no module support). Any
// other module syntax fails the test loudly instead of being silently
// stripped into a semantically different file.
func stripExport(t *testing.T, name, src string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	exports := 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "import") {
			t.Fatalf("logic/%s contains an import (%q) — logic files must be dependency-free declarations", name, trimmed)
		}
		if strings.HasPrefix(trimmed, "export") {
			if !strings.HasPrefix(trimmed, "export {") || !strings.HasSuffix(trimmed, ";") {
				t.Fatalf("logic/%s export %q is not a single-line `export { … };` — the strip-export convention requires it", name, trimmed)
			}
			exports++
			continue
		}
		out = append(out, l)
	}
	if exports != 1 {
		t.Fatalf("logic/%s has %d export statements, want exactly 1 trailing `export { … };`", name, exports)
	}
	return strings.Join(out, "\n")
}

// logicVM evaluates web/js/logic/<name> in a fresh runtime.
func logicVM(t *testing.T, name string) *goja.Runtime {
	t.Helper()
	src, err := fs.ReadFile(webFS, "web/js/logic/"+name)
	if err != nil {
		t.Fatalf("read embedded logic/%s: %v", name, err)
	}
	vm := goja.New()
	if _, err := vm.RunString(stripExport(t, name, string(src))); err != nil {
		t.Fatalf("goja eval logic/%s (ES6-conservative gate): %v", name, err)
	}
	return vm
}

// call invokes a declared function by name and returns its exported result.
func call(t *testing.T, vm *goja.Runtime, fn string, args ...any) any {
	t.Helper()
	f, ok := goja.AssertFunction(vm.Get(fn))
	if !ok {
		t.Fatalf("%s is not a function in the logic module", fn)
	}
	vals := make([]goja.Value, len(args))
	for i, a := range args {
		vals[i] = vm.ToValue(a)
	}
	res, err := f(goja.Undefined(), vals...)
	if err != nil {
		t.Fatalf("%s(%v) threw: %v", fn, args, err)
	}
	return res.Export()
}

// callErr invokes a declared function expecting a throw; it returns the
// thrown message ("" when the call succeeded).
func callErr(t *testing.T, vm *goja.Runtime, fn string, args ...any) string {
	t.Helper()
	f, ok := goja.AssertFunction(vm.Get(fn))
	if !ok {
		t.Fatalf("%s is not a function in the logic module", fn)
	}
	vals := make([]goja.Value, len(args))
	for i, a := range args {
		vals[i] = vm.ToValue(a)
	}
	if _, err := f(goja.Undefined(), vals...); err != nil {
		return err.Error()
	}
	return ""
}

// TestLogicModulesParseInGoja is the loud ES6-conservative gate: every shipped
// logic module must evaluate in goja after the strip-export transform. A
// later fire adding logic/*.js gets this gate for free.
func TestLogicModulesParseInGoja(t *testing.T) {
	entries, err := fs.ReadDir(webFS, "web/js/logic")
	if err != nil {
		t.Fatalf("read embedded web/js/logic: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no logic modules embedded — the logic tier is missing")
	}
	for _, e := range entries {
		logicVM(t, e.Name())
	}
}

func TestParseRouteJS(t *testing.T) {
	vm := logicVM(t, "route.js")
	cases := []struct {
		hash   string
		view   string
		arg    string
		params map[string]string
	}{
		{"#/map", "map", "", map[string]string{}},
		{"#/graph/vtx.role.abc?view=hood", "graph", "vtx.role.abc", map[string]string{"view": "hood"}},
		{"#/corekv/lnk.identity.a1.holdsRole.role.r1", "corekv", "lnk.identity.a1.holdsRole.role.r1", map[string]string{}},
		{"#/corekv/vtx.identity.a1?aspect=profile", "corekv", "vtx.identity.a1", map[string]string{"aspect": "profile"}},
		{"#/corekv?prefix=vtx.role.&limit=10", "corekv", "", map[string]string{"prefix": "vtx.role.", "limit": "10"}},
		{"#/op?type=CreateRole", "op", "", map[string]string{"type": "CreateRole"}},
		{"", "", "", map[string]string{}},
		{"#/", "", "", map[string]string{}},
		{"#garbage", "garbage", "", map[string]string{}},
		{"#/corekv?prefix=vtx.svc%2Eclass.", "corekv", "", map[string]string{"prefix": "vtx.svc.class."}},
	}
	for _, tc := range cases {
		got, ok := call(t, vm, "parseRoute", tc.hash).(map[string]any)
		if !ok {
			t.Fatalf("parseRoute(%q) did not return an object", tc.hash)
		}
		if got["view"] != tc.view || got["arg"] != tc.arg {
			t.Errorf("parseRoute(%q) = view %q arg %q, want %q %q", tc.hash, got["view"], got["arg"], tc.view, tc.arg)
		}
		params, _ := got["params"].(map[string]any)
		if len(params) != len(tc.params) {
			t.Errorf("parseRoute(%q) params = %v, want %v", tc.hash, params, tc.params)
			continue
		}
		for k, want := range tc.params {
			if params[k] != want {
				t.Errorf("parseRoute(%q) params[%q] = %v, want %q", tc.hash, k, params[k], want)
			}
		}
	}
}

// TestClassifyKeyJS drives the JS mirror with the SAME case table as the Go
// TestClassifyKey — the cross-language drift pin: FE and server must never
// disagree on what a key is.
func TestClassifyKeyJS(t *testing.T) {
	vm := logicVM(t, "keys.js")
	for _, tt := range classifyKeyCases {
		if got := call(t, vm, "classifyKey", tt.key); got != string(tt.want) {
			t.Errorf("js classifyKey(%q) = %v, want %q", tt.key, got, tt.want)
		}
	}
}

func TestKeyHelpersJS(t *testing.T) {
	vm := logicVM(t, "keys.js")

	if got := call(t, vm, "isEntityKey", "vtx.role.r1"); got != true {
		t.Errorf("isEntityKey(vtx.role.r1) = %v, want true", got)
	}
	if got := call(t, vm, "isEntityKey", "not a key"); got != false {
		t.Errorf("isEntityKey(not a key) = %v, want false", got)
	}
	if got := call(t, vm, "isEntityKey", 42); got != false {
		t.Errorf("isEntityKey(42) = %v, want false", got)
	}

	if got := call(t, vm, "shortId", "vtx.identity.abc123"); got != "abc123" {
		t.Errorf("shortId = %v, want abc123", got)
	}
	if got := call(t, vm, "shortId", "vtx.identity.abc123.profile"); got != "abc123.profile" {
		t.Errorf("shortId aspect = %v, want abc123.profile", got)
	}

	targets := []struct {
		key  string
		want any // nil for non-entities
	}{
		{"vtx.identity.a1", "#/graph/vtx.identity.a1"},
		{"vtx.meta.m1", "#/graph/vtx.meta.m1"},
		{"lnk.identity.a1.holdsRole.role.r1", "#/graph/lnk.identity.a1.holdsRole.role.r1"},
		{"vtx.identity.a1.profile", "#/graph/vtx.identity.a1?aspect=profile"},
		{"vtx.meta.m1.canonicalName", "#/graph/vtx.meta.m1?aspect=canonicalName"},
		// A package vertex owns its detail page; its aspects stay on Graph.
		{"vtx.package.p1", "#/package/vtx.package.p1"},
		{"vtx.package.p1.manifest", "#/graph/vtx.package.p1?aspect=manifest"},
		{"lnk.too.short", nil},
		{"random", nil},
	}
	for _, tc := range targets {
		if got := call(t, vm, "keyTarget", tc.key); got != tc.want {
			t.Errorf("keyTarget(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestDeriveReadsJS(t *testing.T) {
	vm := logicVM(t, "reads.js")
	payload := map[string]any{
		"target": "vtx.role.r1",
		"nested": map[string]any{"k": "lnk.identity.a1.holdsRole.role.r1"},
		"list":   []any{"vtx.identity.a1", "plain", 3},
		"n":      3,
		"skip":   "role.r1.note", // not a vtx./lnk. prefix — never collected
	}
	got, ok := call(t, vm, "deriveReads", payload).([]any)
	if !ok {
		t.Fatal("deriveReads did not return an array")
	}
	gotKeys := make([]string, 0, len(got))
	for _, k := range got {
		s, _ := k.(string)
		gotKeys = append(gotKeys, s)
	}
	sort.Strings(gotKeys)
	want := []string{"lnk.identity.a1.holdsRole.role.r1", "vtx.identity.a1", "vtx.role.r1"}
	if !slices.Equal(gotKeys, want) {
		t.Errorf("deriveReads = %v, want %v", gotKeys, want)
	}
}

func TestCoerceFieldJS(t *testing.T) {
	vm := logicVM(t, "reads.js")

	if got := call(t, vm, "coerceField", "age", "integer", "42", true).(map[string]any); got["value"] != int64(42) {
		t.Errorf("coerceField integer = %v, want 42", got["value"])
	}
	if got := call(t, vm, "coerceField", "name", "string", "  hi  ", false).(map[string]any); got["value"] != "hi" {
		t.Errorf("coerceField string = %v, want trimmed hi", got["value"])
	}
	if got := call(t, vm, "coerceField", "tags", "array", `["a","b"]`, false).(map[string]any); got["value"] == nil {
		t.Error("coerceField array JSON returned nil")
	}
	if got := call(t, vm, "coerceField", "opt", "string", "", false).(map[string]any); got["omit"] != true {
		t.Errorf("empty optional = %v, want omit:true", got)
	}

	if msg := callErr(t, vm, "coerceField", "age", "integer", "x", true); !strings.Contains(msg, "not a number") {
		t.Errorf("bad number threw %q, want 'not a number'", msg)
	}
	if msg := callErr(t, vm, "coerceField", "req", "string", "", true); !strings.Contains(msg, "required") {
		t.Errorf("missing required threw %q, want 'required'", msg)
	}
	if msg := callErr(t, vm, "coerceField", "cfg", "object", "{bad", true); !strings.Contains(msg, "invalid JSON") {
		t.Errorf("bad JSON threw %q, want 'invalid JSON'", msg)
	}
}

func TestSchemaTypeLabelJS(t *testing.T) {
	vm := logicVM(t, "reads.js")
	if got := call(t, vm, "schemaTypeLabel", map[string]any{"enum": []any{"a"}}); got != "enum" {
		t.Errorf("enum label = %v", got)
	}
	if got := call(t, vm, "schemaTypeLabel", map[string]any{"type": []any{"string", "null"}}); got != "string|null" {
		t.Errorf("union label = %v", got)
	}
	if got := call(t, vm, "schemaTypeLabel", map[string]any{"type": "integer"}); got != "integer" {
		t.Errorf("scalar label = %v", got)
	}
	if got := call(t, vm, "schemaTypeLabel", map[string]any{}); got != "any" {
		t.Errorf("absent label = %v", got)
	}
}

func TestStatusLogicJS(t *testing.T) {
	vm := logicVM(t, "status.js")

	tiers := []struct {
		node map[string]any
		want int64
	}{
		{map[string]any{"kind": "lens", "id": "L1"}, 4},
		{map[string]any{"kind": "infra", "id": "core-operations"}, 0},
		{map[string]any{"kind": "infra", "id": "core-kv"}, 2},
		{map[string]any{"kind": "component", "id": "processor"}, 1},
		{map[string]any{"kind": "component", "id": "weaver"}, 3},
	}
	for _, tc := range tiers {
		if got := call(t, vm, "sysmapTier", tc.node); got != tc.want {
			t.Errorf("sysmapTier(%v) = %v, want %d", tc.node, got, tc.want)
		}
	}

	if got := call(t, vm, "issueClass", "[error] boom"); got != "card-issue bad" {
		t.Errorf("issueClass error = %v", got)
	}
	if got := call(t, vm, "issueClass", "[warning] meh"); got != "card-issue" {
		t.Errorf("issueClass warning = %v", got)
	}
}

// TestHoodLayoutJS pins the pure ego-graph layout math (logic/hood.js).
func TestHoodLayoutJS(t *testing.T) {
	vm := logicVM(t, "hood.js")

	// adaptiveRadius: few chips keep the base; many chips grow it.
	if got := call(t, vm, "adaptiveRadius", 4, 150, 190); got != int64(190) {
		t.Errorf("adaptiveRadius(4) = %v, want base 190", got)
	}
	small := call(t, vm, "adaptiveRadius", 10, 150, 190)
	large := call(t, vm, "adaptiveRadius", 40, 150, 190)
	if toFloat(small) >= toFloat(large) {
		t.Errorf("adaptiveRadius not monotonic: 10 chips %v vs 40 chips %v", small, large)
	}

	// ringPositions: n points on the circle, first at 12 o'clock.
	pts, ok := call(t, vm, "ringPositions", 4, 100, 100, 50).([]any)
	if !ok || len(pts) != 4 {
		t.Fatalf("ringPositions returned %v", pts)
	}
	p0 := pts[0].(map[string]any)
	if x, y := toFloat(p0["x"]), toFloat(p0["y"]); !near(x, 100) || !near(y, 50) {
		t.Errorf("ring point 0 = (%v,%v), want (100,50) — 12 o'clock", x, y)
	}
	for _, p := range pts {
		m := p.(map[string]any)
		dx, dy := toFloat(m["x"])-100, toFloat(m["y"])-100
		if r := dx*dx + dy*dy; !near(r, 2500) {
			t.Errorf("ring point %v not on radius 50 (r²=%v)", m, r)
		}
	}

	// sectorPositions: n=1 sits exactly on the anchor angle.
	one, _ := call(t, vm, "sectorPositions", 1, 0, 0, 0.0, 100, 1.0).([]any)
	if m := one[0].(map[string]any); !near(toFloat(m["x"]), 100) || !near(toFloat(m["y"]), 0) {
		t.Errorf("sector n=1 = %v, want (100,0) on the anchor angle", m)
	}
	three, _ := call(t, vm, "sectorPositions", 3, 0, 0, 0.0, 100, 1.0).([]any)
	first := three[0].(map[string]any)
	last := three[2].(map[string]any)
	if !near(toFloat(first["angle"]), -0.5) || !near(toFloat(last["angle"]), 0.5) {
		t.Errorf("sector spread = %v..%v, want -0.5..0.5", first["angle"], last["angle"])
	}
}

// TestGroupLinkItemsJS pins the same-relation grouping that keeps a
// 30-identity role walkable.
func TestGroupLinkItemsJS(t *testing.T) {
	vm := logicVM(t, "hood.js")
	links := make([]map[string]any, 0, 12)
	for i := 0; i < 10; i++ {
		links = append(links, map[string]any{
			"key": "lnk.identity.i" + string(rune('a'+i)) + ".holdsRole.role.r1", "relation": "holdsRole",
			"direction": "in", "otherKey": "vtx.identity.i" + string(rune('a'+i)), "otherType": "identity",
		})
	}
	links = append(links, map[string]any{
		"key": "lnk.permission.p1.grantedBy.role.r1", "relation": "grantedBy",
		"direction": "in", "otherKey": "vtx.permission.p1", "otherType": "permission",
	})
	items, ok := call(t, vm, "groupLinkItems", links, 8).([]any)
	if !ok {
		t.Fatal("groupLinkItems did not return an array")
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (1 single + 1 group); %v", len(items), items)
	}
	single := items[0].(map[string]any)
	if single["kind"] != "single" {
		t.Errorf("item 0 kind = %v, want single (the permission link)", single["kind"])
	}
	group := items[1].(map[string]any)
	if group["kind"] != "group" || group["relation"] != "holdsRole" || group["otherType"] != "identity" {
		t.Errorf("group item = %v", group)
	}
	if members := group["links"].([]any); len(members) != 10 {
		t.Errorf("group size = %d, want 10", len(members))
	}

	// At or under the threshold nothing groups.
	items, _ = call(t, vm, "groupLinkItems", links[:8], 8).([]any)
	for _, it := range items {
		if it.(map[string]any)["kind"] != "single" {
			t.Errorf("under-threshold bucket grouped: %v", it)
		}
	}
}

// TestEvictForBudgetJS pins the hairball guard: oldest unprotected batches go
// first; batch 0 and protected batches never do.
func TestEvictForBudgetJS(t *testing.T) {
	vm := logicVM(t, "hood.js")

	// Under budget: nothing evicted.
	if got := call(t, vm, "evictForBudget", []int{10, 5}, []int{1}, 60).([]any); len(got) != 0 {
		t.Errorf("under budget evicted %v", got)
	}
	// Over budget: batch 1 (oldest unprotected) goes; 0 and the protected 3 stay.
	got := call(t, vm, "evictForBudget", []int{20, 20, 20, 20}, []int{3}, 60).([]any)
	if len(got) != 1 || got[0] != int64(1) {
		t.Errorf("evicted %v, want [1]", got)
	}
	// Everything protected: may exceed budget, evicts nothing else.
	got = call(t, vm, "evictForBudget", []int{50, 30}, []int{1}, 60).([]any)
	if len(got) != 0 {
		t.Errorf("protected batch evicted: %v", got)
	}
}

// TestHoodSentenceJS pins the Contract #1 §1.1 sentence rendering the edge
// tips teach with: source <relation> target.
func TestHoodSentenceJS(t *testing.T) {
	vm := logicVM(t, "hood.js")
	out := map[string]any{"relation": "holdsRole", "direction": "out"}
	if got := call(t, vm, "hoodSentence", "identity · a1", out, "role · r1"); got != "identity · a1 holdsRole role · r1" {
		t.Errorf("out sentence = %v", got)
	}
	in := map[string]any{"relation": "holdsRole", "direction": "in"}
	if got := call(t, vm, "hoodSentence", "role · r1", in, "identity · a1"); got != "identity · a1 holdsRole role · r1" {
		t.Errorf("in sentence = %v", got)
	}
}

// toFloat widens goja's int64/float64 exports for numeric assertions.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}

func near(got, want float64) bool {
	d := got - want
	return d < 1e-6 && d > -1e-6
}

// TestStaticUIServed pins the go:embed static mount: the served index.html
// boots the module entrypoint, and the module tree itself is reachable.
func TestStaticUIServed(t *testing.T) {
	mux := testServer()
	for path, mustContain := range map[string]string{
		"/":                 `src="js/main.js"`,
		"/js/main.js":       "startRouter",
		"/js/logic/keys.js": "keyTarget",
		"/style.css":        "--bg",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), mustContain) {
			t.Errorf("GET %s body does not contain %q", path, mustContain)
		}
	}
}

// TestComponentLogicJS pins the component-page shaping (logic/component.js):
// per-engine metrics lines, the events summary, and the control-surface
// selector.
func TestComponentLogicJS(t *testing.T) {
	vm := logicVM(t, "component.js")

	procDoc := map[string]any{"metrics": map[string]any{
		"ops_consumed_total": 12, "ops_committed_total": 10, "ops_rejected_total": 2, "lane_lag_total": 0,
	}}
	if got := call(t, vm, "metricsLine", "processor", procDoc); got != "consumed 12 · committed 10 · rejected 2 · lane lag 0" {
		t.Errorf("processor metrics line = %q", got)
	}
	// A null lane_lag_total (no lane readable) renders "?" — never a fabricated 0.
	procNull := map[string]any{"metrics": map[string]any{
		"ops_consumed_total": 1, "ops_committed_total": 1, "ops_rejected_total": 0, "lane_lag_total": nil,
	}}
	if got := call(t, vm, "metricsLine", "processor", procNull).(string); !strings.HasSuffix(got, "lane lag ?") {
		t.Errorf("null lane lag rendered %q, want trailing 'lane lag ?'", got)
	}

	weaverDoc := map[string]any{"metrics": map[string]any{
		"targets": 5, "marksInFlight": 1, "timersScheduled": 3, "timersFired": 2, "sweepReclaims": 4,
	}}
	if got := call(t, vm, "metricsLine", "weaver", weaverDoc); got != "targets 5 · marks in flight 1 · timers 3 scheduled / 2 fired · sweep reclaims 4" {
		t.Errorf("weaver metrics line = %q", got)
	}

	if got := call(t, vm, "metricsLine", "loom", map[string]any{"metrics": map[string]any{"runningInstances": 2}}); got != "running instances 2" {
		t.Errorf("loom metrics line = %q", got)
	}

	rfxDoc := map[string]any{"metrics": map[string]any{"lensLags": map[string]any{"a": 0, "b": 3, "c": 0}}}
	if got := call(t, vm, "metricsLine", "refractor", rfxDoc); got != "1/3 lenses lagging" {
		t.Errorf("refractor metrics line = %q", got)
	}
	// No metrics at all → empty line, not a throw.
	if got := call(t, vm, "metricsLine", "bridge", map[string]any{}); got != "" {
		t.Errorf("bridge metrics line = %q, want empty", got)
	}

	summary, ok := call(t, vm, "eventSummary", []any{
		map[string]any{"kind": "malformed-operation", "tail": "p1.malformed-operation.r1"},
		map[string]any{"kind": "claim-attempts", "tail": "p1.claim-attempts.won"},
		map[string]any{"kind": "claim-attempts", "tail": "p1.claim-attempts.lost"},
		map[string]any{"kind": "malformed-operation", "tail": "p1.malformed-operation.r2"},
	}).([]any)
	if !ok || len(summary) != 3 {
		t.Fatalf("eventSummary = %v", summary)
	}
	first := summary[0].(map[string]any)
	if first["kind"] != "malformed-operation" || toFloat(first["count"]) != 2 {
		t.Errorf("eventSummary[0] = %v, want malformed-operation ×2 first", first)
	}
	// claim-attempts break down BY OUTCOME (never collapsed into one bucket);
	// malformed-operation request-id qualifiers do NOT split its count.
	if second := summary[1].(map[string]any); second["kind"] != "claim-attempts · lost" {
		t.Errorf("eventSummary[1] = %v, want claim-attempts · lost", second)
	}
	// A kind named like an Object.prototype member still counts correctly.
	proto, _ := call(t, vm, "eventSummary", []any{map[string]any{"kind": "constructor"}}).([]any)
	if len(proto) != 1 || toFloat(proto[0].(map[string]any)["count"]) != 1 {
		t.Errorf("prototype-named kind mis-counted: %v", proto)
	}

	surfaces := map[string]string{
		"loom": "loom", "weaver": "weaver", "refractor": "refractor",
		"processor": "events", "bridge": "none", "object-store-manager": "none", "loftspace-app": "none",
	}
	for comp, want := range surfaces {
		if got := call(t, vm, "controlSurface", comp); got != want {
			t.Errorf("controlSurface(%s) = %v, want %s", comp, got, want)
		}
	}
}

// TestLensLogicJS pins the lens page's pure decision logic (logic/lens.js):
// the §6.3 control enablement table, the typed-delete confirm rules, and the
// heartbeat latency formatting.
func TestLensLogicJS(t *testing.T) {
	vm := logicVM(t, "lens.js")

	// Enablement by renderedState: resume only when paused; pause only while
	// projecting/lagging; rebuild disabled while pending-readpath; validate
	// always on. Confirm rides rebuild only on a protected lens.
	cases := []struct {
		status      string
		isProtected bool
		enabled     map[string]bool
		confirm     map[string]bool
	}{
		{"projecting", false,
			map[string]bool{"validate": true, "pause": true, "resume": false, "rebuild": true},
			map[string]bool{"rebuild": false}},
		{"lagging", false,
			map[string]bool{"validate": true, "pause": true, "resume": false, "rebuild": true},
			map[string]bool{}},
		{"paused", false,
			map[string]bool{"validate": true, "pause": false, "resume": true, "rebuild": true},
			map[string]bool{}},
		{"pending-readpath", true,
			map[string]bool{"validate": true, "pause": false, "resume": false, "rebuild": false},
			map[string]bool{"rebuild": false}},
		{"fault", true,
			map[string]bool{"validate": true, "pause": false, "resume": false, "rebuild": true},
			map[string]bool{"rebuild": true}},
		{"rebuilding", false,
			map[string]bool{"pause": false, "resume": false, "rebuild": true},
			map[string]bool{}},
	}
	for _, tc := range cases {
		rows, ok := call(t, vm, "lensControls", tc.status, tc.isProtected).([]any)
		if !ok || len(rows) != 4 {
			t.Fatalf("lensControls(%q) = %v, want 4 rows", tc.status, rows)
		}
		byOp := map[string]map[string]any{}
		for _, r := range rows {
			m := r.(map[string]any)
			byOp[m["op"].(string)] = m
		}
		for op, want := range tc.enabled {
			if got := byOp[op]["enabled"]; got != want {
				t.Errorf("%s/%s enabled = %v, want %v", tc.status, op, got, want)
			}
		}
		for op, want := range tc.confirm {
			if got := byOp[op]["confirm"]; got != want {
				t.Errorf("%s/%s confirm = %v, want %v", tc.status, op, got, want)
			}
		}
		// A disabled row always explains itself.
		for op, m := range byOp {
			if m["enabled"] == false && m["note"] == "" {
				t.Errorf("%s/%s disabled without a note", tc.status, op)
			}
		}
	}

	// Typed-delete confirm: exact match on canonicalName, id fallback, never
	// an empty token.
	if got := call(t, vm, "deleteConfirmToken", "applicantRoster", "L1"); got != "applicantRoster" {
		t.Errorf("token = %v", got)
	}
	if got := call(t, vm, "deleteConfirmToken", "", "L1"); got != "L1" {
		t.Errorf("fallback token = %v", got)
	}
	if got := call(t, vm, "deleteConfirmReady", "applicantRoster", "applicantRoster"); got != true {
		t.Error("exact match not ready")
	}
	for _, bad := range []string{"applicantroster", " applicantRoster", "applicantRoster ", ""} {
		if got := call(t, vm, "deleteConfirmReady", bad, "applicantRoster"); got != false {
			t.Errorf("deleteConfirmReady(%q) = %v, want false", bad, got)
		}
	}
	if got := call(t, vm, "deleteConfirmReady", "", ""); got != false {
		t.Error("empty token must never be ready")
	}

	// Latency line: ns → ms with sub-10ms keeping one decimal; empty for a
	// missing/zero-count entry.
	line := call(t, vm, "latencyLine", map[string]any{
		"count": 4, "meanNs": 1.5e6, "p95Ns": 12e6, "p99Ns": 30e6,
	})
	if line != "count 4 · mean 1.5ms · p95 12ms · p99 30ms" {
		t.Errorf("latencyLine = %q", line)
	}
	if got := call(t, vm, "latencyLine", nil); got != "" {
		t.Errorf("nil latency = %q", got)
	}
	if got := call(t, vm, "latencyLine", map[string]any{"count": 0}); got != "" {
		t.Errorf("zero-count latency = %q", got)
	}
}

// TestFeedLogicJS pins the pulse feed's pure tier (logic/feed.js): event→row
// shaping, the capped ring buffer, the poll-diff derivation, the rows/min
// rate, and the LED vocabulary.
func TestFeedLogicJS(t *testing.T) {
	vm := logicVM(t, "feed.js")

	// feedTime: the HH:MM:SS slice of an RFC3339 stamp; malformed → empty.
	if got := call(t, vm, "feedTime", "2026-07-03T12:04:31Z"); got != "12:04:31" {
		t.Errorf("feedTime = %v", got)
	}
	for _, bad := range []any{"garbage", "2026-07-03T12:0", nil, 42} {
		if got := call(t, vm, "feedTime", bad); got != "" {
			t.Errorf("feedTime(%v) = %v, want empty", bad, got)
		}
	}

	// shapeEventRow: the SSE envelope → feed row; requestId resolves to the
	// op tracker key (vtx.op.<requestId>).
	row, ok := call(t, vm, "shapeEventRow", map[string]any{
		"eventId": "e1", "requestId": "r1", "eventType": "clinic.appointmentCreated",
		"domain": "clinic", "targetKey": "vtx.appointment.a1", "timestamp": "2026-07-03T12:04:31Z",
	}).(map[string]any)
	if !ok {
		t.Fatal("shapeEventRow did not return an object")
	}
	if row["kind"] != "event" || row["time"] != "12:04:31" ||
		row["eventType"] != "clinic.appointmentCreated" ||
		row["targetKey"] != "vtx.appointment.a1" || row["opKey"] != "vtx.op.r1" {
		t.Errorf("shaped row = %v", row)
	}
	// A missing requestId never fabricates an op link; nil input never throws.
	sparse := call(t, vm, "shapeEventRow", map[string]any{"eventType": "x.y"}).(map[string]any)
	if sparse["opKey"] != "" || sparse["targetKey"] != "" {
		t.Errorf("sparse row fabricated links: %v", sparse)
	}
	if got := call(t, vm, "shapeEventRow", nil).(map[string]any); got["kind"] != "event" {
		t.Errorf("nil event row = %v", got)
	}

	// pushRows: newest first, capped.
	buf := []any{}
	for i := 0; i < 5; i++ {
		buf, _ = call(t, vm, "pushRows", buf, []any{map[string]any{"n": float64(i)}}, 3).([]any)
	}
	if len(buf) != 3 || toFloat(buf[0].(map[string]any)["n"]) != 4 || toFloat(buf[2].(map[string]any)["n"]) != 2 {
		t.Errorf("ring buffer = %v", buf)
	}

	// deriveTransitions: status changes + lens rule updates; new nodes and
	// infra derive nothing; an empty previous poll derives nothing.
	prev := []any{
		map[string]any{"id": "L1", "kind": "lens", "label": "roster", "status": "projecting", "activeSequence": 41},
		map[string]any{"id": "weaver", "kind": "component", "label": "Weaver", "status": "green"},
		map[string]any{"id": "core-kv", "kind": "infra", "status": "present"},
	}
	next := []any{
		map[string]any{"id": "L1", "kind": "lens", "label": "roster", "status": "rebuilding", "activeSequence": 43},
		map[string]any{"id": "weaver", "kind": "component", "label": "Weaver", "status": "stale"},
		map[string]any{"id": "core-kv", "kind": "infra", "status": "absent"},
		map[string]any{"id": "L2", "kind": "lens", "label": "new", "status": "projecting"},
	}
	rows, ok := call(t, vm, "deriveTransitions", prev, next).([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("deriveTransitions = %v, want 3 rows (lens status + rule update + weaver)", rows)
	}
	texts := make([]string, 0, len(rows))
	for _, r := range rows {
		m := r.(map[string]any)
		texts = append(texts, m["text"].(string))
		if m["kind"] != "derived" {
			t.Errorf("derived row kind = %v", m["kind"])
		}
	}
	joined := strings.Join(texts, " | ")
	for _, want := range []string{
		"roster projecting → rebuilding",
		"roster rule updated (seq 41 → 43)",
		"Weaver green → stale",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("derived rows missing %q: %v", want, texts)
		}
	}
	lensRow := rows[0].(map[string]any)
	if lensRow["href"] != "#/lens/L1" {
		t.Errorf("lens derived href = %v", lensRow["href"])
	}
	if got, _ := call(t, vm, "deriveTransitions", []any{}, next).([]any); len(got) != 0 {
		t.Errorf("first poll derived rows: %v", got)
	}

	// rowsPerMin / pruneTimes: only the trailing minute counts.
	times := []any{1000, 30000, 70000}
	if got := call(t, vm, "rowsPerMin", times, 75000); toFloat(got) != 2 {
		t.Errorf("rowsPerMin = %v, want 2", got)
	}
	pruned, _ := call(t, vm, "pruneTimes", times, 75000).([]any)
	if len(pruned) != 2 {
		t.Errorf("pruneTimes = %v", pruned)
	}

	// ledClass: the §8.4 vocabulary.
	for status, want := range map[string]string{"live": "green", "retry": "yellow", "error": "red", "off": "dim"} {
		if got := call(t, vm, "ledClass", status); got != want {
			t.Errorf("ledClass(%s) = %v, want %s", status, got, want)
		}
	}
}

// TestPkgLogicJS pins the package-view logic (logic/pkg.js): the manifest
// pick (the JS twin of the Go manifestFromUpload rule), the apply-reply
// summary line, and the uninstall-confirm summary.
func TestPkgLogicJS(t *testing.T) {
	vm := logicVM(t, "pkg.js")

	// manifestCandidate mirrors manifestFromUpload: named manifest wins
	// case-insensitively; single file accepted; multi-file ambiguity → null.
	if got := call(t, vm, "manifestCandidate", []any{"README.md", "Manifest.YAML"}); got != "Manifest.YAML" {
		t.Errorf("manifestCandidate named = %v", got)
	}
	if got := call(t, vm, "manifestCandidate", []any{"whatever.yaml"}); got != "whatever.yaml" {
		t.Errorf("manifestCandidate single = %v", got)
	}
	if got := call(t, vm, "manifestCandidate", []any{"a.yaml", "b.yaml"}); got != nil {
		t.Errorf("manifestCandidate ambiguous = %v, want null", got)
	}
	if got := call(t, vm, "manifestCandidate", []any{}); got != nil {
		t.Errorf("manifestCandidate empty = %v, want null", got)
	}

	for _, tc := range []struct {
		res  map[string]any
		want string
	}{
		{map[string]any{"action": "install", "toVersion": "1.0.0", "created": 12},
			"install v1.0.0 — 12 created"},
		{map[string]any{"action": "upgrade", "fromVersion": "1.0.0", "toVersion": "1.1.0", "created": 2, "updated": 1, "dryRun": true},
			"preview — upgrade 1.0.0 → 1.1.0 — 2 created · 1 updated"},
		{map[string]any{"action": "skip", "skipped": true, "reason": "same version"},
			"skipped — same version"},
		{map[string]any{"action": "upgrade", "fromVersion": "1.0.0", "toVersion": "1.0.0"},
			"upgrade 1.0.0 → 1.0.0 — no changes"},
		// The apply reply's retention-holder fields are COUNTS — the *Count
		// suffix is the whole point, since the uninstall reply's same concept
		// is a key list — and the two buckets read differently on purpose: a
		// preserved holder is intact custody, an already-tombstoned one is
		// custody no verb can reach any more.
		{map[string]any{"action": "upgrade", "fromVersion": "1.0.0", "toVersion": "1.1.0",
			"updated": 1, "retentionHoldersPreservedCount": 2},
			"upgrade 1.0.0 → 1.1.0 — 1 updated · 2 retention-class holder key(s) preserved"},
		{map[string]any{"action": "upgrade", "fromVersion": "1.0.0", "toVersion": "1.1.0",
			"updated": 1, "retentionHoldersPreservedCount": 2, "retentionHoldersAlreadyStrandedCount": 1},
			"upgrade 1.0.0 → 1.1.0 — 1 updated · 2 retention-class holder key(s) preserved · 1 retention-class holder key(s) ALREADY tombstoned"},
		// A refused secure-column narrowing, and the case that needs the line
		// most: the narrowing is the whole diff, so the apply commits nothing
		// and this counter is the only thing separating "your edit was
		// declined" from "your edit was already in place".
		{map[string]any{"action": "upgrade", "fromVersion": "1.0.0", "toVersion": "1.1.0",
			"secureColumnsWidened": 1},
			"upgrade 1.0.0 → 1.1.0 — no changes · 1 secure column(s) kept their committed holderTypes"},
	} {
		if got := call(t, vm, "applySummaryLine", tc.res); got != tc.want {
			t.Errorf("applySummaryLine(%v) = %q, want %q", tc.res, got, tc.want)
		}
	}

	// The tombstone scope = declared − unresolved + the manifest aspect + the
	// package vertex; the breakdown counts declared items per kind.
	pkg := map[string]any{
		"declaredCount": 170,
		"unresolved":    3,
		"sections": []any{
			map[string]any{"kind": "entities", "count": 3},
			map[string]any{"kind": "lenses", "count": 2},
			map[string]any{"kind": "grants", "count": 0},
		},
	}
	if got := call(t, vm, "uninstallSummary", pkg); got != "tombstones up to 169 key(s) incl. the manifest + package vertex — 3 entities · 2 lenses; 3 unresolved skipped" {
		t.Errorf("uninstallSummary = %q", got)
	}
	if got := call(t, vm, "uninstallSummary", map[string]any{}); got != "tombstones up to 2 key(s) incl. the manifest + package vertex" {
		t.Errorf("uninstallSummary empty = %q", got)
	}

	// A retention-class holder never enters the tombstone set (Contract #8
	// §8.3), so the preview must not count it — neither in the total nor in
	// its section's per-kind breakdown, which would otherwise show the class
	// as something this uninstall is about to soft-delete. The holder here is
	// one resolved root carrying one folded .retentionPolicy aspect = 2
	// declared keys, in an "other" section it shares with one ordinary item.
	withHolder := map[string]any{
		"declaredCount": 10,
		"unresolved":    0,
		"sections": []any{
			map[string]any{"kind": "entities", "count": 3, "items": []any{
				map[string]any{"key": "vtx.meta.aaaaaaaaaaaaaaaaaaaa", "found": true},
				map[string]any{"key": "vtx.meta.bbbbbbbbbbbbbbbbbbbb", "found": true},
				map[string]any{"key": "vtx.meta.cccccccccccccccccccc", "found": true},
			}},
			map[string]any{"kind": "other", "count": 2, "items": []any{
				map[string]any{"key": "vtx.retentionclass.dddddddddddddddddddd", "found": true, "aspects": 1},
				map[string]any{"key": "vtx.role.eeeeeeeeeeeeeeeeeeee", "found": true},
			}},
		},
	}
	if got := call(t, vm, "uninstallSummary", withHolder); got != "tombstones up to 10 key(s) incl. the manifest + package vertex — 3 entities · 1 other; 2 retention-class holder key(s) left untouched (only ShredRetentionClassKey may destroy them)" {
		t.Errorf("uninstallSummary with a retention holder = %q", got)
	}

	// A holder root the graph could not resolve is already netted out by
	// `unresolved`, so it must not be subtracted twice; only its folded aspect
	// keys still count as held back.
	unresolvedHolder := map[string]any{
		"declaredCount": 4,
		"unresolved":    1,
		"sections": []any{
			map[string]any{"kind": "other", "count": 1, "items": []any{
				map[string]any{"key": "vtx.retentionclass.dddddddddddddddddddd", "found": false, "aspects": 1},
			}},
		},
	}
	if got := call(t, vm, "uninstallSummary", unresolvedHolder); got != "tombstones up to 4 key(s) incl. the manifest + package vertex; 1 unresolved skipped; 1 retention-class holder key(s) left untouched (only ShredRetentionClassKey may destroy them)" {
		t.Errorf("uninstallSummary with an unresolved holder = %q", got)
	}
}

// TestScrubberLogicJS pins the map scrubber's pure frame math (logic/scrubber.js,
// F13 §4.2 v1): which flows are live at a given instant, the sampled frame
// track built from that, the playhead clock label, and the default window.
func TestScrubberLogicJS(t *testing.T) {
	vm := logicVM(t, "scrubber.js")

	flows := []any{
		// Live the whole window.
		map[string]any{"instanceId": "a", "startedAt": "2026-07-05T10:00:00Z", "endedAt": "2026-07-05T10:30:00Z"},
		// Starts mid-window, still running (no endedAt) — open on the right.
		map[string]any{"instanceId": "b", "startedAt": "2026-07-05T10:15:00Z"},
		// Ends before the window starts — never live in it.
		map[string]any{"instanceId": "c", "startedAt": "2026-07-05T09:00:00Z", "endedAt": "2026-07-05T09:30:00Z"},
		// Unparsable startedAt — never counts live, never throws.
		map[string]any{"instanceId": "d", "startedAt": "not-a-time"},
	}
	t0 := float64(1783245600000) // 2026-07-05T10:00:00Z in ms

	live, ok := call(t, vm, "liveAt", flows, t0).([]any)
	if !ok || len(live) != 1 || live[0] != "a" {
		t.Fatalf("liveAt(t0) = %v, want [a]", live)
	}
	live, _ = call(t, vm, "liveAt", flows, t0+16*60*1000).([]any) // t0+16min: a still running, b started
	ids := map[string]bool{}
	for _, id := range live {
		ids[id.(string)] = true
	}
	if !ids["a"] || !ids["b"] || len(ids) != 2 {
		t.Errorf("liveAt(t0+16m) = %v, want [a b]", live)
	}

	frames, ok := call(t, vm, "framesFromFlows", flows, t0, t0+30*60*1000, 15*60*1000).([]any)
	if !ok || len(frames) != 3 {
		t.Fatalf("framesFromFlows = %v, want 3 frames (0/15/30 min)", frames)
	}
	f0 := frames[0].(map[string]any)
	if toFloat(f0["rollup"]) != 1 {
		t.Errorf("frame 0 rollup = %v, want 1", f0["rollup"])
	}
	f2 := frames[2].(map[string]any)
	if toFloat(f2["rollup"]) != 1 { // a ends exactly at t0+30m — not live at the boundary
		t.Errorf("frame 2 (a's end boundary) rollup = %v, want 1 (only b)", f2["rollup"])
	}

	// A non-positive step or inverted window yields no frames, not a hang.
	if got, _ := call(t, vm, "framesFromFlows", flows, t0, t0+1000, 0).([]any); len(got) != 0 {
		t.Errorf("step=0 frames = %v, want none", got)
	}
	if got, _ := call(t, vm, "framesFromFlows", flows, t0+1000, t0, 1000).([]any); len(got) != 0 {
		t.Errorf("inverted window frames = %v, want none", got)
	}

	if got := call(t, vm, "clockLabel", t0); got != "10:00:00" {
		t.Errorf("clockLabel(t0) = %v, want 10:00:00", got)
	}
	if got := call(t, vm, "clockLabel", "garbage"); got != "" {
		t.Errorf("clockLabel(garbage) = %v, want empty", got)
	}

	win, ok := call(t, vm, "timelineWindow", t0, 60*60*1000).(map[string]any)
	if !ok || toFloat(win["from"]) != t0-3600000 || toFloat(win["to"]) != t0 {
		t.Errorf("timelineWindow = %v", win)
	}
	win, _ = call(t, vm, "timelineWindow", t0, -5).(map[string]any)
	if toFloat(win["from"]) != t0 {
		t.Errorf("timelineWindow negative span = %v, want zero-width at now", win)
	}

	// A step far smaller than the window (a plausible unit-mismatch bug —
	// seconds passed where ms was meant) must clamp to MAX_FRAMES rather than
	// building a multi-million-entry array synchronously.
	huge, ok := call(t, vm, "framesFromFlows", flows, t0, t0+24*60*60*1000, 1).([]any)
	if !ok || len(huge) > 2001 {
		t.Errorf("framesFromFlows with step=1 over a 24h window = %d frames, want clamped to <=2001", len(huge))
	}
}

// TestRetentionLogicJS pins the Vault page's retention-key shaping
// (logic/retention.js): a live (never-shredded) class shows its declared
// policy/period with no finalization progress; a shredded class shows
// finalization progress instead, and stays "in flight" until BOTH async
// halves land.
func TestRetentionLogicJS(t *testing.T) {
	vm := logicVM(t, "retention.js")

	live := map[string]any{"canonicalName": "seven-year", "policy": "eraseOnExpiry", "retentionPeriod": "P7Y"}
	shredding := map[string]any{"shredded": true, "vaultKeyDestroyed": true, "projectionsRebuilt": false}
	done := map[string]any{"shredded": true, "vaultKeyDestroyed": true, "projectionsRebuilt": true}

	if got := call(t, vm, "retentionKeyShredded", live); got != false {
		t.Errorf("retentionKeyShredded(live) = %v, want false", got)
	}
	if got := call(t, vm, "retentionKeyInFlight", live); got != false {
		t.Errorf("retentionKeyInFlight(live) = %v, want false (never shredded, nothing pending)", got)
	}
	if got := call(t, vm, "retentionKeyInFlight", shredding); got != true {
		t.Errorf("retentionKeyInFlight(shredding) = %v, want true (projectionsRebuilt still pending)", got)
	}
	if got := call(t, vm, "retentionKeyInFlight", done); got != false {
		t.Errorf("retentionKeyInFlight(done) = %v, want false (both halves landed)", got)
	}

	if got := call(t, vm, "retentionKeyStatusLine", live); got != "eraseOnExpiry · P7Y" {
		t.Errorf("retentionKeyStatusLine(live) = %v, want the declared policy/period", got)
	}
	if got := call(t, vm, "retentionKeyStatusLine", shredding); got != "vaultKeyDestroyed ✓ · projectionsRebuilt …" {
		t.Errorf("retentionKeyStatusLine(shredding) = %v, want in-flight progress", got)
	}

	summary := call(t, vm, "retentionFleetSummary", []any{live, shredding, done})
	if summary != "3 retention classes declared · 2 shredded · 1 finalization in flight" {
		t.Errorf("retentionFleetSummary = %v", summary)
	}
	if got := call(t, vm, "retentionFleetSummary", []any{live}); got != "1 retention class declared · 0 shredded · 0 finalizations in flight" {
		t.Errorf("retentionFleetSummary singular = %v", got)
	}
}
