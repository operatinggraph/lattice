package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// ordersQueueDecl and orderedAgoLabelDecl lift the shipped ordersQueue/
// orderedAgoLabel declarations out of the embedded app.js — both are pure
// (no DOM/window/document), so running the REAL source is what makes the
// assertions below statements about what ships rather than about a copy in
// this file.
var ordersQueueDecl = regexp.MustCompile(`(?s)\nfunction ordersQueue\(tabs\) \{\n.*?\n\}\n`)
var orderedAgoLabelDecl = regexp.MustCompile(`(?s)\nfunction orderedAgoLabel\(orderedAt, now\) \{\n.*?\n\}\n`)

func ordersQueueVM(t *testing.T) (*goja.Runtime, goja.Callable, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)

	queueDecl := ordersQueueDecl.FindString(text)
	if queueDecl == "" {
		t.Fatal("app.js: no top-level `function ordersQueue(tabs) {…}` declaration found — the extraction regex no longer matches this file")
	}
	if strings.Contains(queueDecl, "document") || strings.Contains(queueDecl, "window") {
		t.Fatalf("ordersQueue reaches the DOM; it must stay a pure function of its argument:\n%s", queueDecl)
	}
	agoDecl := orderedAgoLabelDecl.FindString(text)
	if agoDecl == "" {
		t.Fatal("app.js: no top-level `function orderedAgoLabel(orderedAt, now) {…}` declaration found — the extraction regex no longer matches this file")
	}
	if strings.Contains(agoDecl, "document") || strings.Contains(agoDecl, "window") {
		t.Fatalf("orderedAgoLabel reaches the DOM; it must stay a pure function of its arguments:\n%s", agoDecl)
	}

	vm := goja.New()
	if _, err := vm.RunString(queueDecl); err != nil {
		t.Fatalf("goja eval of the shipped ordersQueue: %v", err)
	}
	if _, err := vm.RunString(agoDecl); err != nil {
		t.Fatalf("goja eval of the shipped orderedAgoLabel: %v", err)
	}
	queueFn, ok := goja.AssertFunction(vm.Get("ordersQueue"))
	if !ok {
		t.Fatal("ordersQueue is not a function after evaluating its declaration")
	}
	agoFn, ok := goja.AssertFunction(vm.Get("orderedAgoLabel"))
	if !ok {
		t.Fatal("orderedAgoLabel is not a function after evaluating its declaration")
	}
	return vm, queueFn, agoFn
}

// TestOrdersQueue_OldestFirstAcrossOpenTabs proves the queue spans every open
// tab, oldest orderedAt first, and excludes a served line, a voided line, a
// legacy line with no orderedAt, and every line of a settled tab.
func TestOrdersQueue_OldestFirstAcrossOpenTabs(t *testing.T) {
	vm, fn, _ := ordersQueueVM(t)
	tabs := []map[string]any{
		{
			"tabKey": "vtx.tab.a", "leaseAppKey": "vtx.leaseapp.a", "status": "open",
			"lines": []map[string]any{
				{"id": "line-1", "description": "Latte", "amountCents": 450, "voided": false, "orderedBy": "vtx.identity.riley", "orderedAt": "2026-09-16T12:10:00Z"},
				{"id": "line-2", "description": "Croissant", "amountCents": 350, "voided": false, "orderedBy": "vtx.identity.riley", "orderedAt": "2026-09-16T12:05:00Z", "servedAt": "2026-09-16T12:06:00Z", "servedBy": "vtx.identity.dana"},
			},
		},
		{
			"tabKey": "vtx.tab.b", "leaseAppKey": "vtx.leaseapp.b", "status": "open",
			"lines": []map[string]any{
				{"id": "line-1", "description": "Muffin", "amountCents": 300, "voided": false, "orderedBy": "vtx.identity.jamie", "orderedAt": "2026-09-16T12:00:00Z"},
				{"id": "line-2", "description": "Cortado", "amountCents": 400, "voided": true, "orderedBy": "vtx.identity.jamie", "orderedAt": "2026-09-16T11:00:00Z"},
				{"id": "line-3", "description": "Bagel", "amountCents": 300, "voided": false, "orderedBy": "vtx.identity.jamie"},
			},
		},
		{
			"tabKey": "vtx.tab.c", "leaseAppKey": "vtx.leaseapp.c", "status": "settled",
			"lines": []map[string]any{
				{"id": "line-1", "description": "Tea", "amountCents": 300, "voided": false, "orderedBy": "vtx.identity.sam", "orderedAt": "2026-09-16T09:00:00Z"},
			},
		},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(tabs))
	if err != nil {
		t.Fatalf("ordersQueue threw: %v", err)
	}
	obj := res.ToObject(vm)
	if length := obj.Get("length").ToInteger(); length != 2 {
		t.Fatalf("want exactly 2 queued rows (tab B line-1, tab A line-1), got %d (%v)", length, res)
	}
	first := obj.Get("0").ToObject(vm)
	second := obj.Get("1").ToObject(vm)
	if got := first.Get("tabKey").String(); got != "vtx.tab.b" {
		t.Errorf("row 0 tabKey = %q, want vtx.tab.b (its line is ordered earliest)", got)
	}
	if got := first.Get("lineId").String(); got != "line-1" {
		t.Errorf("row 0 lineId = %q, want line-1", got)
	}
	if got := second.Get("tabKey").String(); got != "vtx.tab.a" {
		t.Errorf("row 1 tabKey = %q, want vtx.tab.a", got)
	}
	if got := second.Get("lineId").String(); got != "line-1" {
		t.Errorf("row 1 lineId = %q, want line-1", got)
	}
}

// TestOrdersQueue_TieBreaksByTabThenLine proves two lines sharing one
// orderedAt sort by tabKey, then by lineId's NUMBER within one tab (line-2
// before line-10 — rfc3339_utc is whole seconds, so one batch of taps shares
// a stamp and a lexicographic tie would misorder past nine lines).
func TestOrdersQueue_TieBreaksByTabThenLine(t *testing.T) {
	vm, fn, _ := ordersQueueVM(t)
	tabs := []map[string]any{
		{
			"tabKey": "vtx.tab.z", "leaseAppKey": "vtx.leaseapp.z", "status": "open",
			"lines": []map[string]any{
				{"id": "line-10", "description": "Bagel", "amountCents": 300, "voided": false, "orderedBy": "vtx.identity.x", "orderedAt": "2026-09-16T12:00:00Z"},
				{"id": "line-2", "description": "Scone", "amountCents": 300, "voided": false, "orderedBy": "vtx.identity.x", "orderedAt": "2026-09-16T12:00:00Z"},
				{"id": "line-1", "description": "Espresso", "amountCents": 250, "voided": false, "orderedBy": "vtx.identity.x", "orderedAt": "2026-09-16T12:00:00Z"},
			},
		},
		{
			"tabKey": "vtx.tab.a", "leaseAppKey": "vtx.leaseapp.a", "status": "open",
			"lines": []map[string]any{
				{"id": "line-1", "description": "Flat White", "amountCents": 400, "voided": false, "orderedBy": "vtx.identity.y", "orderedAt": "2026-09-16T12:00:00Z"},
			},
		},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(tabs))
	if err != nil {
		t.Fatalf("ordersQueue threw: %v", err)
	}
	obj := res.ToObject(vm)
	if length := obj.Get("length").ToInteger(); length != 4 {
		t.Fatalf("want 4 rows, got %d", length)
	}
	wantOrder := []struct{ tabKey, lineId string }{
		{"vtx.tab.a", "line-1"},
		{"vtx.tab.z", "line-1"},
		{"vtx.tab.z", "line-2"},
		{"vtx.tab.z", "line-10"},
	}
	for i, want := range wantOrder {
		row := obj.Get(strconv.Itoa(i)).ToObject(vm)
		if got := row.Get("tabKey").String(); got != want.tabKey {
			t.Errorf("row %d tabKey = %q, want %q", i, got, want.tabKey)
		}
		if got := row.Get("lineId").String(); got != want.lineId {
			t.Errorf("row %d lineId = %q, want %q", i, got, want.lineId)
		}
	}
}

// TestOrderedAgoLabel pins the three rendered bands plus the NaN guard.
func TestOrderedAgoLabel(t *testing.T) {
	vm, _, fn := ordersQueueVM(t)
	now, err := vm.RunString(`new Date("2026-09-16T12:30:00Z")`)
	if err != nil {
		t.Fatalf("construct now: %v", err)
	}
	call := func(t *testing.T, orderedAt string) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(orderedAt), now)
		if err != nil {
			t.Fatalf("orderedAgoLabel(%q) threw: %v", orderedAt, err)
		}
		return res.String()
	}
	if got := call(t, "2026-09-16T12:29:45Z"); got != "just now" {
		t.Errorf("15s ago = %q, want %q", got, "just now")
	}
	if got := call(t, "2026-09-16T12:27:00Z"); got != "3 min ago" {
		t.Errorf("3 min ago = %q, want %q", got, "3 min ago")
	}
	if got := call(t, "2026-09-16T10:30:00Z"); got != "2 h ago" {
		t.Errorf("2 h ago = %q, want %q", got, "2 h ago")
	}
	if got := call(t, "not-a-date"); got != "?" {
		t.Errorf("orderedAgoLabel(garbage) = %q, want %q", got, "?")
	}
}
