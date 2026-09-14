package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// leaseTermUIDecls lifts the shipped fmtUTCDate/applicationBannerFor/
// unitTenancyEnded declarations (plus fmtUTCDate's UTC_MONTH_ABBR dependency)
// out of the embedded app.js — the rotate_offer_test.go / renewal_ready_test.go
// pattern: the REAL shipped source runs here, not a copy, so these pins are a
// statement about what ships. All four are self-contained (no DOM/state), so
// goja can evaluate them directly.
var leaseTermUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nconst UTC_MONTH_ABBR = \[.*?\];\n`),
	regexp.MustCompile(`(?s)\nfunction fmtUTCDate\(s\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction applicationBannerFor\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction unitTenancyEnded\(apps\) \{\n.*?\n\}\n`),
}

func leaseTermUIVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	for _, re := range leaseTermUIDecls {
		decl := re.FindString(string(src))
		if decl == "" {
			t.Fatalf("app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped declaration %s: %v", re, err)
		}
	}
	return vm
}

// TestFmtUTCDate_ReadsTheUTCCalendarDateSlice pins fmtUTCDate against the
// design's own worked example ("2026-09-15T00:00:00Z" -> "Sep 15, 2026") — the
// midnight-UTC stamp EndTenancy's own NotYetEnded refusal names by the same
// YYYY-MM-DD slice. It must never go through a timezone-sensitive Date parse
// (a west-of-Greenwich reader would otherwise see Sep 14), so this test is run
// under TZ=America/Los_Angeles too (see the package's TestMain / the CI
// invocation) to prove process-local time.Local never leaks into it.
func TestFmtUTCDate_ReadsTheUTCCalendarDateSlice(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("fmtUTCDate"))
	if !ok {
		t.Fatal("fmtUTCDate is not a function after evaluating its declaration")
	}
	for _, tc := range []struct {
		name string
		in   interface{}
		want string
	}{
		{"midnight UTC instant", "2026-09-15T00:00:00Z", "Sep 15, 2026"},
		{"a different month/day, no zero-pad on day", "2026-01-05T00:00:00Z", "Jan 5, 2026"},
		{"bare calendar date (no time component)", "2026-12-31", "Dec 31, 2026"},
		{"end of year rolls the month table correctly", "2026-12-01T00:00:00Z", "Dec 1, 2026"},
		{"empty string", "", ""},
		{"null", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var arg goja.Value
			if tc.in == nil {
				arg = goja.Null()
			} else {
				arg = vm.ToValue(tc.in)
			}
			res, err := fn(goja.Undefined(), arg)
			if err != nil {
				t.Fatalf("fmtUTCDate(%v) threw: %v", tc.in, err)
			}
			if got := res.String(); got != tc.want {
				t.Errorf("fmtUTCDate(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestUnitTenancyEnded_TruthTable pins the landlord unit card's Relist gate
// against the design's own truth table (loftspace-lease-term-and-tenancy-end-
// design.md §2.4): a leased unit offers the manual Relist fallback only when
// NOTHING on it still holds a live tenancy.
func TestUnitTenancyEnded_TruthTable(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("unitTenancyEnded"))
	if !ok {
		t.Fatal("unitTenancyEnded is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, apps []map[string]interface{}) bool {
		t.Helper()
		var arg goja.Value
		if apps == nil {
			arg = goja.Undefined()
		} else {
			arr := make([]interface{}, len(apps))
			for i, a := range apps {
				arr[i] = a
			}
			arg = vm.ToValue(arr)
		}
		res, err := fn(goja.Undefined(), arg)
		if err != nil {
			t.Fatalf("unitTenancyEnded(%v) threw: %v", apps, err)
		}
		return res.ToBoolean()
	}

	approvedLive := map[string]interface{}{"landlordApproved": true, "tenancyEndedAt": nil}
	approvedEnded := map[string]interface{}{"landlordApproved": true, "tenancyEndedAt": "2026-09-15T00:00:00Z"}
	unapproved := map[string]interface{}{"landlordApproved": false, "tenancyEndedAt": nil}

	for _, tc := range []struct {
		name string
		apps []map[string]interface{}
		want bool
	}{
		{"no apps", nil, true},
		{"no apps (empty slice)", []map[string]interface{}{}, true},
		{"approved live", []map[string]interface{}{approvedLive}, false},
		{"approved ended", []map[string]interface{}{approvedEnded}, true},
		{"approved ended + approved live", []map[string]interface{}{approvedEnded, approvedLive}, false},
		{"only unapproved", []map[string]interface{}{unapproved, unapproved}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.apps); got != tc.want {
				t.Errorf("unitTenancyEnded(%v) = %v, want %v", tc.apps, got, tc.want)
			}
		})
	}
}

// TestApplicationBannerFor_TenancyEndedWinsOverEveryOtherBranch pins the
// banner-selection precedence (design §2.4 item 2): tenancyEndedAt is a
// TERMINAL fact and must win over landlordApproved/unitStatus, missing_decision
// and even a standing decline — evaluated before every other branch.
func TestApplicationBannerFor_TenancyEndedWinsOverEveryOtherBranch(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("applicationBannerFor"))
	if !ok {
		t.Fatal("applicationBannerFor is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, row map[string]interface{}) (string, string) {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(row))
		if err != nil {
			t.Fatalf("applicationBannerFor(%v) threw: %v", row, err)
		}
		obj := res.ToObject(vm)
		return obj.Get("cls").String(), obj.Get("text").String()
	}

	t.Run("tenancyEndedAt wins over landlordApproved+leased", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{
			"tenancyEndedAt": "2026-09-15T00:00:00Z", "landlordApproved": true, "unitStatus": "leased",
		})
		if text != "Lease ended Sep 15, 2026" {
			t.Errorf("text = %q, want the ended banner", text)
		}
	})

	t.Run("tenancyEndedAt wins over a standing decline", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{
			"tenancyEndedAt": "2026-09-15T00:00:00Z", "declined": true, "landlordDeclined": true, "declineReason": "moved",
		})
		if text != "Lease ended Sep 15, 2026" {
			t.Errorf("an ended tenancy must win over a standing decline, got %q", text)
		}
	})

	t.Run("declined (no tenancyEndedAt) still reads declined", func(t *testing.T) {
		cls, text := run(t, map[string]interface{}{"declined": true})
		if cls != "decision declined" || text != "Application declined." {
			t.Errorf("cls=%q text=%q, want declined banner", cls, text)
		}
	})

	t.Run("landlordApproved + leased (no tenancy end) reads complete", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{"landlordApproved": true, "unitStatus": "leased"})
		if text != "Application complete — all steps done." {
			t.Errorf("text = %q, want the complete banner", text)
		}
	})

	t.Run("missing_decision reads awaiting review", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{"missing_decision": true})
		if text != "Qualified — awaiting landlord review." {
			t.Errorf("text = %q, want the awaiting-review banner", text)
		}
	})

	t.Run("bare in-review default", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{})
		if text != "In review — complete the open steps below." {
			t.Errorf("text = %q, want the in-review default", text)
		}
	})
}
