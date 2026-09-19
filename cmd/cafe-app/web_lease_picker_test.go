package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// rankLeaseOptions is the POS/front-desk lease picker's pure ranking rule,
// lifted out of the embedded app.js the way web_house_limit_test.go lifts
// tabLimitRemaining: a lease with an open tab first, then every other
// billable lease by resident name (case-insensitive), then every disabled
// lease trailing — the shape fillLeaseSelect renders (an "● " marker on an
// open-tab option, a trailing "Not billable" optgroup).
var rankLeaseOptionsDecl = regexp.MustCompile(`(?s)\nfunction rankLeaseOptions\(rows\) \{\n.*?\n\}\n`)

func rankLeaseOptionsVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := rankLeaseOptionsDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function rankLeaseOptions(rows) {…}` declaration found — the extraction regex no longer matches this file")
	}
	if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
		t.Fatalf("rankLeaseOptions reaches the DOM; it must stay a pure function of its argument:\n%s", decl)
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped rankLeaseOptions: %v", err)
	}
	return vm
}

func rankedKeys(t *testing.T, vm *goja.Runtime, rowsJS string) []string {
	t.Helper()
	v, err := vm.RunString(`rankLeaseOptions(` + rowsJS + `).map((r) => r.leaseAppKey)`)
	if err != nil {
		t.Fatalf("rankLeaseOptions(%s) threw: %v", rowsJS, err)
	}
	obj := v.ToObject(vm)
	n := obj.Get("length").ToInteger()
	out := make([]string, 0, n)
	for i := int64(0); i < n; i++ {
		out = append(out, obj.Get(strconv.FormatInt(i, 10)).String())
	}
	return out
}

// TestRankLeaseOptions_OpenTabFirstThenBillableThenDisabled pins the three
// groups fillLeaseSelect renders in order: open-tab leases (money on the
// counter right now), then every other billable lease, then every disabled
// (not-billable) lease trailing — regardless of the input's own order.
func TestRankLeaseOptions_OpenTabFirstThenBillableThenDisabled(t *testing.T) {
	vm := rankLeaseOptionsVM(t)
	rows := `[
		{ leaseAppKey: "vtx.leaseapp.disabled", who: "Amy Adams", disabled: true, open: false },
		{ leaseAppKey: "vtx.leaseapp.billable", who: "Bob Baker", disabled: false, open: false },
		{ leaseAppKey: "vtx.leaseapp.opentab", who: "Zed Zephyr", disabled: false, open: true }
	]`
	got := rankedKeys(t, vm, rows)
	want := []string{"vtx.leaseapp.opentab", "vtx.leaseapp.billable", "vtx.leaseapp.disabled"}
	if !equalStrings(got, want) {
		t.Errorf("rankLeaseOptions(%s) = %v, want %v (open-tab first, then billable, then disabled trailing — even though Zed sorts last by name within an unranked list)", rows, got, want)
	}
}

// TestRankLeaseOptions_NameOrderWithinGroups pins the within-group sort:
// case-insensitive by the rendered resident name, then by lease key when
// two rows share a name, so two residents named the same still land in a
// stable order.
func TestRankLeaseOptions_NameOrderWithinGroups(t *testing.T) {
	vm := rankLeaseOptionsVM(t)
	rows := `[
		{ leaseAppKey: "vtx.leaseapp.b", who: "riley chen", disabled: false, open: false },
		{ leaseAppKey: "vtx.leaseapp.a", who: "Amy Adams", disabled: false, open: false },
		{ leaseAppKey: "vtx.leaseapp.c2", who: "Sam Lee", disabled: false, open: false },
		{ leaseAppKey: "vtx.leaseapp.c1", who: "sam lee", disabled: false, open: false }
	]`
	got := rankedKeys(t, vm, rows)
	want := []string{"vtx.leaseapp.a", "vtx.leaseapp.b", "vtx.leaseapp.c1", "vtx.leaseapp.c2"}
	if !equalStrings(got, want) {
		t.Errorf("rankLeaseOptions(%s) = %v, want %v (case-insensitive name order, tied names broken by lease key)", rows, got, want)
	}
}

// TestRankLeaseOptions_DisabledGroupSortsByNameToo pins that the trailing
// disabled group is not merely appended in input order — it gets the same
// name sort as the billable group, so a long "Not billable" list is still
// scannable.
func TestRankLeaseOptions_DisabledGroupSortsByNameToo(t *testing.T) {
	vm := rankLeaseOptionsVM(t)
	rows := `[
		{ leaseAppKey: "vtx.leaseapp.z", who: "Zed Zephyr", disabled: true, open: false },
		{ leaseAppKey: "vtx.leaseapp.a", who: "Amy Adams", disabled: true, open: false }
	]`
	got := rankedKeys(t, vm, rows)
	want := []string{"vtx.leaseapp.a", "vtx.leaseapp.z"}
	if !equalStrings(got, want) {
		t.Errorf("rankLeaseOptions(%s) = %v, want %v", rows, got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// leaseSearchQuery is the type-to-find picker's pure filter — a
// case-insensitive substring match against each row's own rendered label
// (name and unit both land there) — lifted the same way rankLeaseOptions
// is above. Rebuilding <option> elements from its result (renderLeaseOptions,
// applyLeaseSearch) rather than toggling `.hidden` is what makes the picker
// work inside a native <select> popup on WebKit, where `option.hidden` /
// `optgroup.hidden` are a no-op; this function is what the rebuild filters
// by, so it carries the correctness the DOM plumbing can't be pinned for
// here.
var leaseSearchQueryDecl = regexp.MustCompile(`(?s)\nfunction leaseSearchQuery\(rows, query\) \{\n.*?\n\}\n`)

func leaseSearchQueryVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := leaseSearchQueryDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function leaseSearchQuery(rows, query) {…}` declaration found — the extraction regex no longer matches this file")
	}
	if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
		t.Fatalf("leaseSearchQuery reaches the DOM; it must stay a pure function of its arguments:\n%s", decl)
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped leaseSearchQuery: %v", err)
	}
	return vm
}

// TestLeaseSearchQuery_FiltersByLabelSubstring pins the filter every
// keystroke runs: case-insensitive, against the row's full rendered label
// (so a query narrows by unit address or an arrears badge too, not just the
// bare name), an empty or whitespace query returns every row, and a query
// matching nothing returns an empty list rather than throwing.
func TestLeaseSearchQuery_FiltersByLabelSubstring(t *testing.T) {
	vm := leaseSearchQueryVM(t)
	if _, err := vm.RunString(`
const rows = [
  { leaseAppKey: "vtx.leaseapp.a", label: "Riley Chen — Unit 4B" },
  { leaseAppKey: "vtx.leaseapp.b", label: "Dana Whitfield — Unit 2A (past due)" },
];`); err != nil {
		t.Fatal(err)
	}
	for expr, wantLen := range map[string]int{
		`leaseSearchQuery(rows, "riley")`:    1,
		`leaseSearchQuery(rows, "RILEY")`:    1,
		`leaseSearchQuery(rows, "4B")`:       1,
		`leaseSearchQuery(rows, "past due")`: 1,
		`leaseSearchQuery(rows, "")`:         2,
		`leaseSearchQuery(rows, "   ")`:      2,
		`leaseSearchQuery(rows, "nobody")`:   0,
	} {
		v, err := vm.RunString(expr)
		if err != nil {
			t.Fatalf("%s threw: %v", expr, err)
		}
		if got := v.ToObject(vm).Get("length").ToInteger(); got != int64(wantLen) {
			t.Errorf("%s: length = %d, want %d", expr, got, wantLen)
		}
	}
	first := evalString(t, vm, `leaseSearchQuery(rows, "riley")[0].leaseAppKey`)
	if first != "vtx.leaseapp.a" {
		t.Errorf(`leaseSearchQuery(rows, "riley")[0].leaseAppKey = %q, want "vtx.leaseapp.a"`, first)
	}
}
