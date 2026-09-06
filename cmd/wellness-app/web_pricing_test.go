package main

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// The shipped app.js cannot be evaluated whole in goja — it is a browser
// script whose top level touches `document` — so each self-contained helper is
// lifted out by its own declaration and run as it ships. That is what makes
// these assertions statements about the page rather than about a copy living
// in this file, the same extraction cmd/cafe-app/web_escape_test.go uses.
func webDeclRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)\nfunction ` + name + `\(.*?\n\}\n`)
}

// webHelperVM evaluates the named top-level app.js declarations, in order, into
// one runtime — so a helper may call one lifted before it.
func webHelperVM(t *testing.T, names ...string) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	for _, name := range names {
		decl := webDeclRe(name).FindString(string(src))
		if decl == "" {
			t.Fatalf("app.js: no top-level `function %s(…) {…}` declaration found — the extraction regex no longer matches this file", name)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped %s: %v", name, err)
		}
	}
	return vm
}

// TestCardPriceCents_AppliesTheBookingOpsOwnRateRule pins the schedule card's
// price against the rule the booking op itself applies
// (prepare_booking_common, packages/wellness-domain/ddls.go, and the
// class-price settlement that bills it): a booker whose lease is APPROVED
// books at rate=resident and is charged residentPriceCents when the class
// declares one. A card quoting the walk-in price to that member promises a
// price the seat then does not charge — and the same card quoting the resident
// price to a merely-applied or declined applicant promises one they will not
// get, which is why mere lease PRESENCE is not the test.
func TestCardPriceCents_AppliesTheBookingOpsOwnRateRule(t *testing.T) {
	vm := webHelperVM(t, "cardPriceCents")
	fn, ok := goja.AssertFunction(vm.Get("cardPriceCents"))
	if !ok {
		t.Fatal("cardPriceCents is not a function after evaluating its declaration")
	}

	call := func(t *testing.T, priceCents any, residentPriceCents any, approved bool) int64 {
		t.Helper()
		se := map[string]any{"priceCents": priceCents}
		if residentPriceCents != nil {
			se["residentPriceCents"] = residentPriceCents
		}
		res, err := fn(goja.Undefined(), vm.ToValue(se), vm.ToValue(approved))
		if err != nil {
			t.Fatalf("cardPriceCents threw: %v", err)
		}
		return res.ToInteger()
	}

	for _, tc := range []struct {
		name     string
		price    any
		resident any
		approved bool
		want     int64
	}{
		{"approved lease, class declares a resident price", 1500, 0, true, 0},
		{"approved lease, resident price merely lower", 1500, 500, true, 500},
		{"approved lease, class declares no resident price", 1500, nil, true, 1500},
		{"no approved lease, class declares a resident price", 1500, 0, false, 1500},
		{"no approved lease, no resident price", 1500, nil, false, 1500},
		{"free class stays free either way", 0, nil, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(t, tc.price, tc.resident, tc.approved); got != tc.want {
				t.Fatalf("cardPriceCents = %d, want %d", got, tc.want)
			}
		})
	}
}

// hatGatingVM evaluates the shipped hat helpers plus applyHatGating into one
// runtime over a minimal element/document stub — enough for the gating
// decisions, which only ever read `state` and write `.hidden`. refreshMeBar and
// showView are stubbed rather than lifted: neither decides which tabs a hat
// reaches, and both reach far more of the page than this is asking about.
func hatGatingVM(t *testing.T, st string) *goja.Runtime {
	t.Helper()
	vm := webHelperVM(t, "anchorKey", "isStaff", "isOperatorHat", "instructorKey", "applyHatGating")
	const stub = `
var elements = {};
function el(id) { if (!elements[id]) elements[id] = { id: id, hidden: false, className: "" }; return elements[id]; }
var document = {
  getElementById: function (id) { return el(id); },
  querySelector: function (_sel) { return null; },
};
var bounced = false;
function refreshMeBar() {}
function showView(_v) { bounced = true; }
`
	if _, err := vm.RunString(stub); err != nil {
		t.Fatalf("goja eval of the document stub: %v", err)
	}
	if _, err := vm.RunString("var state = " + st + ";"); err != nil {
		t.Fatalf("goja eval of the session state: %v", err)
	}
	if _, err := vm.RunString("applyHatGating();"); err != nil {
		t.Fatalf("goja eval of the shipped applyHatGating: %v", err)
	}
	return vm
}

func tabHidden(t *testing.T, vm *goja.Runtime, id string) bool {
	t.Helper()
	v, err := vm.RunString(`elements[` + fmt.Sprintf("%q", id) + `].hidden`)
	if err != nil {
		t.Fatalf("read %s.hidden: %v", id, err)
	}
	return v.ToBoolean()
}

// TestApplyHatGating_OperatorReachesTheRoster pins the Roster tab against the
// server rules it is an affordance over. mayReadRoster (bookings.go) and
// handleRosterSessions (sessions.go) each answer an operator TRUE before they
// ask about a workplace or a ledBy binding, and the roster is where the studio
// repair for a retired-studio class lives — the one edit only an operator may
// make. Gating the tab on worksAt+frontOfHouse alone told an operator holding
// neither, through a note on a roster they could not open, that only they could
// fix the class.
//
// The negatives are the other half: a plain member still reaches neither
// staff tab, so admitting the operator hat widened one gate and not the rest.
func TestApplyHatGating_OperatorReachesTheRoster(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		state                               string
		wantRosterHidden, wantStudiosHidden bool
		wantInstructorNewHidden             bool
	}{
		{
			name:                    "operator with no workplace and no instructor binding",
			state:                   `{ anchors: [], isOperator: true, frontOfHouse: false }`,
			wantRosterHidden:        false,
			wantStudiosHidden:       false,
			wantInstructorNewHidden: false,
		},
		{
			name:                    "front-desk staff",
			state:                   `{ anchors: [{ relation: "worksAt", key: "vtx.building.BBWELLHATGATEBLDGHJK" }], isOperator: false, frontOfHouse: true }`,
			wantRosterHidden:        false,
			wantStudiosHidden:       false,
			wantInstructorNewHidden: true,
		},
		{
			name:                    "bound instructor, no front-desk role",
			state:                   `{ anchors: [{ relation: "identifiedBy", key: "vtx.instructor.BBWELLHATGATEANCHRKM" }], isOperator: false, frontOfHouse: false }`,
			wantRosterHidden:        false,
			wantStudiosHidden:       true,
			wantInstructorNewHidden: true,
		},
		{
			name:                    "plain member",
			state:                   `{ anchors: [], isOperator: false, frontOfHouse: false }`,
			wantRosterHidden:        true,
			wantStudiosHidden:       true,
			wantInstructorNewHidden: true,
		},
		{
			name:                    "worksAt but no frontOfHouse role is not staff",
			state:                   `{ anchors: [{ relation: "worksAt", key: "vtx.building.BBWELLHATGATEBLDGHJK" }], isOperator: false, frontOfHouse: false }`,
			wantRosterHidden:        true,
			wantStudiosHidden:       true,
			wantInstructorNewHidden: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm := hatGatingVM(t, tc.state)
			if got := tabHidden(t, vm, "tab-roster"); got != tc.wantRosterHidden {
				t.Errorf("tab-roster hidden = %v, want %v", got, tc.wantRosterHidden)
			}
			if got := tabHidden(t, vm, "tab-studios"); got != tc.wantStudiosHidden {
				t.Errorf("tab-studios hidden = %v, want %v", got, tc.wantStudiosHidden)
			}
			if got := tabHidden(t, vm, "instructor-new-toggle"); got != tc.wantInstructorNewHidden {
				t.Errorf("instructor-new-toggle hidden = %v, want %v", got, tc.wantInstructorNewHidden)
			}
		})
	}
}

// TestCardPriceLabel_NamesTheResidentRateOnlyWhenItDiffers pins the schedule
// card's price COPY, the half cardPriceCents does not decide. The suffix is
// there so a resident reading a number the board does not advertise can tell a
// perk from a bug — which means it must appear exactly when the two prices
// differ. Printed on a class whose resident price equals the walk-in one, it
// claims a discount that is not there; withheld when they differ, it leaves the
// member to guess. "Free" is the same rule at zero: a class the resident pays
// nothing for still says why, and a class free to everyone does not.
func TestCardPriceLabel_NamesTheResidentRateOnlyWhenItDiffers(t *testing.T) {
	vm := webHelperVM(t, "money", "priceLabel", "cardPriceCents", "cardPriceLabel")
	fn, ok := goja.AssertFunction(vm.Get("cardPriceLabel"))
	if !ok {
		t.Fatal("cardPriceLabel is not a function after evaluating its declaration")
	}

	call := func(t *testing.T, priceCents any, residentPriceCents any, approved bool) string {
		t.Helper()
		se := map[string]any{"priceCents": priceCents}
		if residentPriceCents != nil {
			se["residentPriceCents"] = residentPriceCents
		}
		res, err := fn(goja.Undefined(), vm.ToValue(se), vm.ToValue(approved))
		if err != nil {
			t.Fatalf("cardPriceLabel threw: %v", err)
		}
		return res.String()
	}

	for _, tc := range []struct {
		name     string
		price    any
		resident any
		approved bool
		want     string
	}{
		{"resident rate is lower, so the card says why", 1500, 500, true, "$5.00 · resident rate"},
		{"resident rate is free, so the card says why", 1500, 0, true, "Free · resident rate"},
		{"resident rate equals the walk-in price", 1500, 1500, true, "$15.00"},
		{"class declares no resident price", 1500, nil, true, "$15.00"},
		{"no approved lease sees the walk-in price alone", 1500, 500, false, "$15.00"},
		{"a class free to everyone claims no perk", 0, nil, true, "Free"},
		{"a class free to everyone, resident price declared equal", 0, 0, true, "Free"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(t, tc.price, tc.resident, tc.approved); got != tc.want {
				t.Fatalf("cardPriceLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRosterSessionOrder_LeadsWithWhatIsStillToRun pins the desk's class
// picker. The roster is where attendance is marked, so a class that ended an
// hour ago has to stay REACHABLE — it is grouped, never dropped — while the
// classes still to run lead, soonest first.
func TestRosterSessionOrder_LeadsWithWhatIsStillToRun(t *testing.T) {
	vm := webHelperVM(t, "rosterSessionIsPast", "rosterSessionOrder")
	fn, ok := goja.AssertFunction(vm.Get("rosterSessionOrder"))
	if !ok {
		t.Fatal("rosterSessionOrder is not a function after evaluating its declaration")
	}

	now, err := time.Parse(time.RFC3339, "2026-08-01T12:00:00Z")
	if err != nil {
		t.Fatalf("parse the fixture's clock: %v", err)
	}
	nowMs := now.UnixMilli()
	sessions := []map[string]any{
		{"sessionKey": "old", "startsAt": "2026-06-01T09:00:00Z", "endsAt": "2026-06-01T10:00:00Z"},
		{"sessionKey": "tonight", "startsAt": "2026-08-01T18:00:00Z", "endsAt": "2026-08-01T19:00:00Z"},
		{"sessionKey": "justEnded", "startsAt": "2026-08-01T10:00:00Z", "endsAt": "2026-08-01T11:00:00Z"},
		{"sessionKey": "running", "startsAt": "2026-08-01T11:30:00Z", "endsAt": "2026-08-01T12:30:00Z"},
		{"sessionKey": "tomorrow", "startsAt": "2026-08-02T09:00:00Z", "endsAt": "2026-08-02T10:00:00Z"},
		{"sessionKey": "undated"},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(sessions), vm.ToValue(nowMs))
	if err != nil {
		t.Fatalf("rosterSessionOrder threw: %v", err)
	}
	out := res.Export().(map[string]any)

	keys := func(group string) []string {
		var got []string
		for _, row := range out[group].([]any) {
			got = append(got, fmt.Sprint(row.(map[string]any)["sessionKey"]))
		}
		return got
	}

	// A class that has begun but not ended is still to run: attendance is
	// marked while it runs, so it must not sink into the past group.
	wantUpcoming := []string{"running", "tonight", "tomorrow", "undated"}
	if got := keys("upcoming"); fmt.Sprint(got) != fmt.Sprint(wantUpcoming) {
		t.Errorf("upcoming = %v, want %v", got, wantUpcoming)
	}
	// Most recent first: the class whose attendance the desk is still marking
	// leads its group rather than sitting behind every class ever run.
	wantPast := []string{"justEnded", "old"}
	if got := keys("past"); fmt.Sprint(got) != fmt.Sprint(wantPast) {
		t.Errorf("past = %v, want %v", got, wantPast)
	}
	if len(keys("upcoming"))+len(keys("past")) != len(sessions) {
		t.Errorf("the partition dropped a class; every one must stay selectable")
	}
}
