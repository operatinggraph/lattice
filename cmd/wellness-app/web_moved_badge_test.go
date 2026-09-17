package main

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"
)

// TestMovedBadge_OnlyForTheCurrentTime pins movedBadge to the design's rule:
// the badge says the member was told about THIS seat's current time, so it
// only renders when movedFor still equals the row's own startsAt. A booking
// moved again after the notice went out carries a movedFor that no longer
// matches startsAt — a new notice is pending, and the badge must say
// nothing rather than claim a notice for a time that has since changed.
func TestMovedBadge_OnlyForTheCurrentTime(t *testing.T) {
	vm := webHelperVM(t, "esc", "fmtTime", "fmtDay", "movedBadge")
	fn, ok := goja.AssertFunction(vm.Get("movedBadge"))
	if !ok {
		t.Fatal("movedBadge is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, b map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(b))
		if err != nil {
			t.Fatalf("movedBadge threw: %v", err)
		}
		return res.String()
	}

	require.Equal(t, "", call(t, map[string]any{
		"startsAt": "2026-07-08T09:00:00Z",
	}), "no movedFor at all: never told of a move")

	told := call(t, map[string]any{
		"startsAt":           "2026-07-08T09:00:00Z",
		"movedFor":           "2026-07-08T09:00:00Z",
		"changeNoticeSentAt": "2026-07-06T12:00:00Z",
	})
	require.Contains(t, told, `class="badge moved"`)
	require.Contains(t, told, "Told of the move · ")

	require.Equal(t, "", call(t, map[string]any{
		"startsAt":           "2026-07-08T09:00:00Z",
		"movedFor":           "2026-07-08T08:00:00Z",
		"changeNoticeSentAt": "2026-07-06T12:00:00Z",
	}), "movedFor names a time that has since moved again: a new notice is pending")
}

// TestMovedBadge_InvalidSentAtStillLabelsWithoutATime mirrors
// promotedBadge's own guard on an unparseable timestamp: the badge still
// renders — movedFor already proved the notice was sent for this time — just
// without a time in the label, the same as an unparseable promotedAt on
// promotedBadge.
func TestMovedBadge_InvalidSentAtStillLabelsWithoutATime(t *testing.T) {
	vm := webHelperVM(t, "esc", "fmtTime", "fmtDay", "movedBadge")
	fn, ok := goja.AssertFunction(vm.Get("movedBadge"))
	if !ok {
		t.Fatal("movedBadge is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(map[string]any{
		"startsAt":           "2026-07-08T09:00:00Z",
		"movedFor":           "2026-07-08T09:00:00Z",
		"changeNoticeSentAt": "not-a-time",
	}))
	if err != nil {
		t.Fatalf("movedBadge threw: %v", err)
	}
	label := res.String()
	require.Contains(t, label, `class="badge moved"`)
	require.Contains(t, label, "Told of the move")
	require.NotContains(t, label, "Told of the move · ", "an unparseable changeNoticeSentAt carries no time suffix")
}

// TestMovedBadge_EscapesTheLabel proves movedBadge's output goes through
// esc() like every other badge helper (lint-markup-escaping) — a label built
// straight from a booking-supplied instant must not be raw-concatenated into
// the returned markup.
func TestMovedBadge_EscapesTheLabel(t *testing.T) {
	vm := webHelperVM(t, "esc", "fmtTime", "fmtDay", "movedBadge")
	fn, ok := goja.AssertFunction(vm.Get("movedBadge"))
	if !ok {
		t.Fatal("movedBadge is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(map[string]any{
		"startsAt":           "2026-07-08T09:00:00Z",
		"movedFor":           "2026-07-08T09:00:00Z",
		"changeNoticeSentAt": "2026-07-06T12:00:00Z",
	}))
	if err != nil {
		t.Fatalf("movedBadge threw: %v", err)
	}
	out := res.String()
	require.True(t, strings.HasPrefix(out, `<span class="badge moved">`), "the label must be wrapped by esc() inside the span, not concatenated raw: %s", out)
	require.True(t, strings.HasSuffix(out, "</span>"), out)
}
