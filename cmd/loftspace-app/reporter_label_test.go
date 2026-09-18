package main

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"
)

// Goja proof of reporterLabel and reportStateLabel (docs/reviews/loftspace-maintenance-loop-closes-2026-09-18.md
// decision 6/7) — the same lift-the-shipped-declaration-out-of-app.js
// harness work_order_state_test.go / lease_term_ui_test.go use. Fixtures are
// built by json.Marshal-ing the app's own wire structs (landlordWorkOrderRow,
// reporterWorkOrderRow, protectedLandlordRow) rather than hand-typed maps, so
// a field renamed on the Go struct breaks this test instead of the fixture
// silently drifting from what the server actually serves.
var reporterLabelDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction localDateTime\(iso\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction workOrderState\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction reporterLabel\(row, applications\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction resolveOffered\(row, me\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction reportStateLabel\(row\) \{\n.*?\n\}\n`),
}

func reporterLabelVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	require.NoError(t, err)
	vm := goja.New()
	for _, re := range reporterLabelDecls {
		decl := re.FindString(string(src))
		require.NotEmptyf(t, decl, "app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		_, err := vm.RunString(decl)
		require.NoErrorf(t, err, "goja eval of the shipped declaration %s", re)
	}
	return vm
}

// toGojaValue round-trips v through JSON (mirroring wellness-app's
// changeBadgeVM) so the fixture carries exactly the keys the wire struct's
// own json tags serve, never a hand-typed key that could drift from them.
func toGojaValue(t *testing.T, vm *goja.Runtime, v any) goja.Value {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m any
	require.NoError(t, json.Unmarshal(raw, &m))
	return vm.ToValue(m)
}

// TestReporterLabel_ResidentWithApprovedApplicationNames pins the name path:
// a resident reporter with a matching APPROVED application at the same unit
// is named by that application's own applicantName.
func TestReporterLabel_ResidentWithApprovedApplicationNames(t *testing.T) {
	vm := reporterLabelVM(t)
	fn, ok := goja.AssertFunction(vm.Get("reporterLabel"))
	require.True(t, ok)

	row := landlordWorkOrderRow{
		WorkOrderKey:       "vtx.workorder.wo1",
		UnitKey:            "vtx.unit.u1",
		ReportedBy:         "vtx.identity.jordan",
		ReportedByResident: true,
	}
	name := "Jordan Ellis"
	apps := []protectedLandlordRow{
		{Applicant: "vtx.identity.jordan", UnitKey: strp("vtx.unit.u1"), ApplicantName: &name, LandlordApproved: true},
	}
	res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), toGojaValue(t, vm, apps))
	require.NoError(t, err)
	require.Equal(t, "Jordan Ellis", res.String())
}

// TestReporterLabel_ResidentWithNoApplicationReadsTheResident pins the
// fallback: a resident reporter with NO matching application record at all
// (an older report predating the applications window) is "the resident",
// never nobody.
func TestReporterLabel_ResidentWithNoApplicationReadsTheResident(t *testing.T) {
	vm := reporterLabelVM(t)
	fn, ok := goja.AssertFunction(vm.Get("reporterLabel"))
	require.True(t, ok)

	row := landlordWorkOrderRow{
		WorkOrderKey:       "vtx.workorder.wo1",
		UnitKey:            "vtx.unit.u1",
		ReportedBy:         "vtx.identity.jordan",
		ReportedByResident: true,
	}
	res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), toGojaValue(t, vm, []protectedLandlordRow{}))
	require.NoError(t, err)
	require.Equal(t, "the resident", res.String())
}

// TestReporterLabel_MatchesAnyApplicationStatus pins reporterLabel's own
// ordering (docs/reviews/loftspace-maintenance-loop-closes-2026-09-18.md
// decision 6 fix round): the application match runs FIRST and unconditional
// on status —
// approved, still PENDING, or already ENDED (tenancyEndedAt set) — because
// reportedByResident answers "resides in the unit NOW" and residesIn is
// unwired the instant a tenancy ends (lease-signing's UnwireResidesIn), so a
// former resident's own closed-out report must still be named by their
// application record rather than falling to reportedByResident and reading
// "staff".
func TestReporterLabel_MatchesAnyApplicationStatus(t *testing.T) {
	vm := reporterLabelVM(t)
	fn, ok := goja.AssertFunction(vm.Get("reporterLabel"))
	require.True(t, ok)
	name := "Jordan Ellis"
	call := func(row landlordWorkOrderRow, apps []protectedLandlordRow) string {
		res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), toGojaValue(t, vm, apps))
		require.NoError(t, err)
		return res.String()
	}

	// A still-PENDING application names the reporter too.
	pendingRow := landlordWorkOrderRow{UnitKey: "vtx.unit.u1", ReportedBy: "vtx.identity.jordan", ReportedByResident: true}
	pending := []protectedLandlordRow{
		{Applicant: "vtx.identity.jordan", UnitKey: strp("vtx.unit.u1"), ApplicantName: &name, LandlordApproved: false},
	}
	require.Equal(t, "Jordan Ellis", call(pendingRow, pending))

	// A FORMER resident (residesIn already unwired, so reportedByResident now
	// reads false) is still named by their ENDED application record.
	endedRow := landlordWorkOrderRow{UnitKey: "vtx.unit.u1", ReportedBy: "vtx.identity.jordan", ReportedByResident: false}
	ended := []protectedLandlordRow{
		{Applicant: "vtx.identity.jordan", UnitKey: strp("vtx.unit.u1"), ApplicantName: &name, LandlordApproved: true, TenancyEndedAt: strp("2026-08-01T00:00:00Z")},
	}
	require.Equal(t, "Jordan Ellis", call(endedRow, ended))
}

// TestReporterLabel_NonResidentIsStaff pins the staff path: a reporter with
// no residesIn link at all (reportedByResident false) is staff, whatever the
// applications list carries.
func TestReporterLabel_NonResidentIsStaff(t *testing.T) {
	vm := reporterLabelVM(t)
	fn, ok := goja.AssertFunction(vm.Get("reporterLabel"))
	require.True(t, ok)

	row := landlordWorkOrderRow{
		WorkOrderKey:       "vtx.workorder.wo1",
		UnitKey:            "vtx.unit.u1",
		ReportedBy:         "vtx.identity.priya",
		ReportedByResident: false,
	}
	res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), toGojaValue(t, vm, []protectedLandlordRow{}))
	require.NoError(t, err)
	require.Equal(t, "staff", res.String())
}

// TestResolveOffered_OnlyTheRowsOwnManagingLandlord pins the Resolve-button
// gate (docs/reviews/loftspace-maintenance-loop-closes-2026-09-18.md
// decision 6 fix round): true only when row.landlordKey names the
// signed-in identity — a co-landlord on the same unit, a building-worksAt
// staffer who also happens to be a landlord elsewhere, and a signed-out
// session (me empty) all read false.
func TestResolveOffered_OnlyTheRowsOwnManagingLandlord(t *testing.T) {
	vm := reporterLabelVM(t)
	fn, ok := goja.AssertFunction(vm.Get("resolveOffered"))
	require.True(t, ok)
	call := func(row landlordWorkOrderRow, me string) bool {
		res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), vm.ToValue(me))
		require.NoError(t, err)
		return res.ToBoolean()
	}

	row := landlordWorkOrderRow{WorkOrderKey: "vtx.workorder.wo1", LandlordKey: "vtx.identity.larry"}
	require.True(t, call(row, "vtx.identity.larry"), "the row's own managing landlord")
	require.False(t, call(row, "vtx.identity.linda"), "a co-landlord on the same unit is not THIS row's landlord")
	require.False(t, call(row, ""), "a signed-out session names nobody")
}

// TestReportStateLabel_CoversEveryTextualState pins all four states the
// tenant's My-reports line can render: a bare report, a queued one, a
// resolved one (with its notes), and one where RecordWorkOrderResolvedNotice
// has told the reporter — independent of resolved/queued.
func TestReportStateLabel_CoversEveryTextualState(t *testing.T) {
	vm := reporterLabelVM(t)
	fn, ok := goja.AssertFunction(vm.Get("reportStateLabel"))
	require.True(t, ok)
	call := func(row reporterWorkOrderRow) string {
		res, err := fn(goja.Undefined(), toGojaValue(t, vm, row))
		require.NoError(t, err)
		return res.String()
	}

	reported := call(reporterWorkOrderRow{ReportedAt: "2026-09-16T12:00:00Z"})
	require.Contains(t, reported, "reported")
	require.NotContains(t, reported, "queued")
	require.NotContains(t, reported, "resolved")
	require.NotContains(t, reported, "told")

	queued := call(reporterWorkOrderRow{ReportedAt: "2026-09-16T12:00:00Z", OpenTaskCount: 1})
	require.Contains(t, queued, "queued")
	require.NotContains(t, queued, "resolved")

	resolved := call(reporterWorkOrderRow{
		ReportedAt: "2026-09-16T12:00:00Z", ResolvedAt: "2026-09-17T09:00:00Z", ResolutionNotes: "Replaced the washer.",
	})
	require.Contains(t, resolved, "resolved")
	require.Contains(t, resolved, "Replaced the washer.")

	told := call(reporterWorkOrderRow{
		ReportedAt: "2026-09-16T12:00:00Z", ResolvedAt: "2026-09-17T09:00:00Z", NoticeSentAt: "2026-09-17T09:05:00Z",
	})
	require.Contains(t, told, "you were told")
}
