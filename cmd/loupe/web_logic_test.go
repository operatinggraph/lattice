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
