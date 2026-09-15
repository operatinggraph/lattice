package main

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// TestStudioCard_RetireDisabledWhileAClassIsUpcoming pins the Studios tab's
// Retire courtesy against TombstoneStudio's own HasUpcomingClasses guard
// (packages/wellness-domain/ddls.go): a studio with a class still ahead of now
// renders the Retire button disabled and captioned with the count, a studio
// whose classes have all started renders it enabled, and the same
// upcomingSessionsAt filter is the one studioGridWarning measures its horizon
// over — one filter, so the courtesy and the warning cannot disagree about
// which classes count.
func TestStudioCard_RetireDisabledWhileAClassIsUpcoming(t *testing.T) {
	vm := webHelperVM(t, "esc", "shortKey", "domId", "upcomingSessionsAt", "studioGridWarning", "retireCaption", "studioCard")
	// GRID_HORIZON_DAYS is a top-level `const`, not a function, so the
	// function-shaped extraction cannot lift it.
	if _, err := vm.RunString(`const GRID_HORIZON_DAYS = 7;`); err != nil {
		t.Fatalf("goja eval of the GRID_HORIZON_DAYS stub: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("studioCard"))
	if !ok {
		t.Fatal("studioCard is not a function after evaluating its declaration")
	}
	studio := map[string]any{"studioKey": "vtx.studio.BBWELLRETRESTUDHJKMN", "name": "Flow Room"}
	call := func(t *testing.T, sessions []map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(studio), vm.ToValue(sessions))
		if err != nil {
			t.Fatalf("studioCard threw: %v", err)
		}
		return res.String()
	}
	at := func(studioKey, startsAt string) map[string]any {
		return map[string]any{"studioKey": studioKey, "startsAt": startsAt, "endsAt": startsAt}
	}

	// Two classes ahead at this studio; one behind it; one ahead elsewhere.
	upcoming := call(t, []map[string]any{
		at("vtx.studio.BBWELLRETRESTUDHJKMN", "2999-01-01T09:00:00Z"),
		at("vtx.studio.BBWELLRETRESTUDHJKMN", "2999-01-02T09:00:00Z"),
		at("vtx.studio.BBWELLRETRESTUDHJKMN", "2000-01-01T09:00:00Z"),
		at("vtx.studio.BBWELLQTHERSTUDHJKMN", "2999-01-03T09:00:00Z"),
	})
	if !strings.Contains(upcoming, `id="retire-`) || !strings.Contains(upcoming, " disabled ") {
		t.Errorf("studioCard with upcoming classes renders Retire enabled, want disabled:\n%s", upcoming)
	}
	if !strings.Contains(upcoming, "Call off its 2 upcoming classes first") {
		t.Errorf("studioCard with 2 upcoming classes lacks the count caption:\n%s", upcoming)
	}

	one := call(t, []map[string]any{at("vtx.studio.BBWELLRETRESTUDHJKMN", "2999-01-01T09:00:00Z")})
	if !strings.Contains(one, "Call off its 1 upcoming class first") {
		t.Errorf("studioCard with 1 upcoming class lacks the singular caption:\n%s", one)
	}

	// Every class has started: history never blocks a retire, so the button
	// is offered.
	history := call(t, []map[string]any{
		at("vtx.studio.BBWELLRETRESTUDHJKMN", "2000-01-01T09:00:00Z"),
		at("vtx.studio.BBWELLQTHERSTUDHJKMN", "2999-01-03T09:00:00Z"),
	})
	if strings.Contains(history, " disabled ") || strings.Contains(history, "Call off its") {
		t.Errorf("studioCard with only started classes disables Retire, want enabled:\n%s", history)
	}
	if !strings.Contains(history, `id="retire-`) {
		t.Errorf("studioCard with only started classes renders no Retire button:\n%s", history)
	}

	// No sessions at all (the fetch failed, renderStudiosAdmin passes []).
	none := call(t, nil)
	if strings.Contains(none, " disabled ") {
		t.Errorf("studioCard with no session rows disables Retire, want enabled:\n%s", none)
	}
}
