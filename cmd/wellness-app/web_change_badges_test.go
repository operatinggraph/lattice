package main

import (
	"encoding/json"
	"testing"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"
)

// The two stamp badges and the led-by line, pinned over the app's own wire
// struct (bookingRow) so the fixture carries exactly the keys /api/bookings
// serves — a key that reaches the lens and the FE but not the row struct is
// dead on the wire, and a fixture built from the struct says so.

func changeBadgeVM(t *testing.T) (*goja.Runtime, func(t *testing.T, fn string, row bookingRow) string) {
	t.Helper()
	vm := webHelperVM(t, "esc", "fmtTime", "fmtDay", "toldBadge", "movedBadge", "instructorChangeBadge", "roomChangeBadge", "ledByLine")
	call := func(t *testing.T, name string, row bookingRow) string {
		t.Helper()
		fn, ok := goja.AssertFunction(vm.Get(name))
		require.Truef(t, ok, "%s is not a function after evaluating its declaration", name)
		raw, err := json.Marshal(row)
		require.NoError(t, err)
		var b map[string]any
		require.NoError(t, json.Unmarshal(raw, &b))
		res, err := fn(goja.Undefined(), vm.ToValue(b))
		require.NoErrorf(t, err, "%s threw", name)
		return res.String()
	}
	return vm, call
}

func strp(s string) *string { return &s }

// TestChangeBadges_OnlyWhileTheToldStampIsCurrent mirrors movedBadge's rule
// for the two stamp pairs: told of THIS change (instructorFor =
// instructorChangedAt / roomFor = studioChangedAt) renders; a later change
// makes the told stamp stale and the badge says nothing.
func TestChangeBadges_OnlyWhileTheToldStampIsCurrent(t *testing.T) {
	_, call := changeBadgeVM(t)
	sent := "2026-09-18T20:00:05Z"

	require.Equal(t, "", call(t, "instructorChangeBadge", bookingRow{InstructorChangedAt: strp("2026-09-18T20:00:00Z")}), "never told")
	told := call(t, "instructorChangeBadge", bookingRow{InstructorChangedAt: strp("2026-09-18T20:00:00Z"), InstructorFor: strp("2026-09-18T20:00:00Z"), ChangeNoticeSentAt: strp(sent)})
	require.Contains(t, told, `class="badge moved"`)
	require.Contains(t, told, "Told of the instructor change · ")
	require.Equal(t, "", call(t, "instructorChangeBadge", bookingRow{InstructorChangedAt: strp("2026-09-19T20:00:00Z"), InstructorFor: strp("2026-09-18T20:00:00Z"), ChangeNoticeSentAt: strp(sent)}), "a second swap: a new notice is pending")

	require.Equal(t, "", call(t, "roomChangeBadge", bookingRow{StudioChangedAt: strp("2026-09-18T20:00:00Z")}), "never told")
	toldRoom := call(t, "roomChangeBadge", bookingRow{StudioChangedAt: strp("2026-09-18T20:00:00Z"), RoomFor: strp("2026-09-18T20:00:00Z"), ChangeNoticeSentAt: strp(sent)})
	require.Contains(t, toldRoom, "Told of the room change · ")
	require.Equal(t, "", call(t, "roomChangeBadge", bookingRow{StudioChangedAt: strp("2026-09-19T20:00:00Z"), RoomFor: strp("2026-09-18T20:00:00Z"), ChangeNoticeSentAt: strp(sent)}), "a second room move: a new notice is pending")

	// The two pairs are independent: an instructor notice does not badge a room change.
	require.Equal(t, "", call(t, "roomChangeBadge", bookingRow{StudioChangedAt: strp("2026-09-18T20:00:00Z"), InstructorFor: strp("2026-09-18T20:00:00Z"), ChangeNoticeSentAt: strp(sent)}))
}

// TestLedByLine says who leads, or that nobody does yet — and escapes the name.
func TestLedByLine(t *testing.T) {
	_, call := changeBadgeVM(t)
	require.Equal(t, `<div class="meta">No instructor yet</div>`, call(t, "ledByLine", bookingRow{}))
	require.Equal(t, `<div class="meta">Led by Sam Okafor</div>`, call(t, "ledByLine", bookingRow{InstructorKey: "vtx.instructor.x", InstructorName: "Sam Okafor"}))
	require.Equal(t, `<div class="meta">Led by &lt;b&gt;</div>`, call(t, "ledByLine", bookingRow{InstructorName: "<b>"}))
}
