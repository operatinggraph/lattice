package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// noticeBoundsDecls lifts the shipped noticeDateBounds declaration out of
// the embedded app.js — the lease_term_ui_test.go pattern: the REAL source
// runs here, not a copy, so this pin is a statement about what ships.
// noticeDateBounds is self-contained (no DOM/state), so goja can evaluate
// it directly.
var noticeBoundsDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction noticeDateBounds\(row, nowIso\) \{\n.*?\n\}\n`),
}

func noticeBoundsVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	for _, re := range noticeBoundsDecls {
		decl := re.FindString(string(src))
		if decl == "" {
			t.Fatalf("app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped declaration %s: %v", re, err)
		}
	}
	fn, ok := goja.AssertFunction(vm.Get("noticeDateBounds"))
	if !ok {
		t.Fatal("noticeDateBounds is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestNoticeDateBounds_MirrorsGiveNoticesOwnRefusals pins the Give-notice
// date input's [min, max] against the exact bounds GiveNotice's own script
// refusals enforce (packages/lease-signing scripts.go): MoveOutBeforeToday
// (min is never before today's UTC calendar day), MoveOutBeforeStart (the
// script refuses moveOutAt <= leaseStart, so min floors at the day AFTER a
// leaseStart that is today or later), and MoveOutAfterEnd (max is the day
// BEFORE leaseEnd, since the script refuses moveOutAt >= leaseEnd). The
// positive vector (a mid-term lease) proves the ordinary case before the
// boundary vectors below.
func TestNoticeDateBounds_MirrorsGiveNoticesOwnRefusals(t *testing.T) {
	const now = "2026-09-15T08:00:00Z" // any time of day — bounds are calendar-day only.
	for _, tc := range []struct {
		name    string
		row     map[string]interface{}
		wantMin string
		wantMax string
		wantNil bool
	}{
		{
			name:    "positive vector: a mid-term lease",
			row:     map[string]interface{}{"tenancyLeaseStart": "2026-01-01T00:00:00Z", "tenancyLeaseEnd": "2027-01-01T00:00:00Z"},
			wantMin: "2026-09-15", wantMax: "2026-12-31",
		},
		{
			name:    "no tenancyLeaseEnd at all — no lease to bound",
			row:     map[string]interface{}{"tenancyLeaseStart": "2026-01-01T00:00:00Z"},
			wantNil: true,
		},
		{
			name:    "lease starts AFTER today — min floors at the day after leaseStart, never leaseStart itself (MoveOutBeforeStart is <=)",
			row:     map[string]interface{}{"tenancyLeaseStart": "2026-09-20T00:00:00Z", "tenancyLeaseEnd": "2027-01-01T00:00:00Z"},
			wantMin: "2026-09-21", wantMax: "2026-12-31",
		},
		{
			name:    "lease ends today — no date is both >= today and < leaseEnd",
			row:     map[string]interface{}{"tenancyLeaseEnd": "2026-09-15T00:00:00Z"},
			wantNil: true,
		},
		{
			name:    "lease ends tomorrow — exactly one valid day, min == max",
			row:     map[string]interface{}{"tenancyLeaseEnd": "2026-09-16T00:00:00Z"},
			wantMin: "2026-09-15", wantMax: "2026-09-15",
		},
		{
			name:    "no tenancyLeaseStart — min is just today",
			row:     map[string]interface{}{"tenancyLeaseEnd": "2027-01-01T00:00:00Z"},
			wantMin: "2026-09-15", wantMax: "2026-12-31",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm, fn := noticeBoundsVM(t)
			res, err := fn(goja.Undefined(), vm.ToValue(tc.row), vm.ToValue(now))
			if err != nil {
				t.Fatalf("noticeDateBounds(%v, %q) threw: %v", tc.row, now, err)
			}
			if tc.wantNil {
				if !goja.IsNull(res) {
					t.Fatalf("noticeDateBounds(%v, %q) = %v, want null", tc.row, now, res)
				}
				return
			}
			if goja.IsNull(res) {
				t.Fatalf("noticeDateBounds(%v, %q) = null, want {min:%q, max:%q}", tc.row, now, tc.wantMin, tc.wantMax)
			}
			obj := res.ToObject(vm)
			if gotMin := obj.Get("min").String(); gotMin != tc.wantMin {
				t.Errorf("min = %q, want %q", gotMin, tc.wantMin)
			}
			if gotMax := obj.Get("max").String(); gotMax != tc.wantMax {
				t.Errorf("max = %q, want %q", gotMax, tc.wantMax)
			}
		})
	}
}

// TestNoticeDateBounds_NoRowOrNoDate pins the degenerate inputs the caller
// (renderGiveNoticeControl) can plausibly pass: a row that hasn't loaded its
// tenancy yet, and a clock string goja's Date cannot parse.
func TestNoticeDateBounds_NoRowOrNoDate(t *testing.T) {
	vm, fn := noticeBoundsVM(t)
	for _, tc := range []struct {
		name string
		row  interface{}
		now  string
	}{
		{"nil row", nil, "2026-09-15T00:00:00Z"},
		{"empty row", map[string]interface{}{}, "2026-09-15T00:00:00Z"},
		{"unparseable now", map[string]interface{}{"tenancyLeaseEnd": "2027-01-01T00:00:00Z"}, "not-a-date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rowArg goja.Value
			if tc.row == nil {
				rowArg = goja.Null()
			} else {
				rowArg = vm.ToValue(tc.row)
			}
			res, err := fn(goja.Undefined(), rowArg, vm.ToValue(tc.now))
			if err != nil {
				t.Fatalf("noticeDateBounds threw: %v", err)
			}
			if !goja.IsNull(res) {
				t.Errorf("noticeDateBounds(%v, %q) = %v, want null", tc.row, tc.now, res)
			}
		})
	}
}
