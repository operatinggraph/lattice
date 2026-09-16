package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// summarizeTodayDecl lifts the shipped summarizeToday declaration out of the
// embedded app.js — the same extract-and-run-the-real-source posture as
// escapeHTMLVM: the fold is pure by construction (no DOM), so the assertions
// below are about what the desk actually computes.
var summarizeTodayDecl = regexp.MustCompile(`(?s)\nfunction summarizeToday\(tabs, now\) \{\n.*?\n\}\n`)

// summarizeTodayVM evaluates the shipped fold plus a fixture built IN the VM
// from local-time constructors, so "today" means the same thing to the test as
// to the browser regardless of the host's zone: `now` is local noon on
// 2026-09-14, and every settledAt is minted from local wall-clock parts.
func summarizeTodayVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := summarizeTodayDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function summarizeToday(tabs, now) {…}` declaration found — the extraction regex no longer matches this file")
	}
	if strings.Contains(decl, "document") {
		t.Fatalf("summarizeToday reaches the DOM; it must stay a pure fold so the desk's numbers are testable:\n%s", decl)
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped summarizeToday: %v", err)
	}
	if _, err := vm.RunString(`
const now = new Date(2026, 8, 14, 12, 0, 0);
const at = (d, h) => new Date(2026, 8, d, h, 0, 0).toISOString();
const line = (id, description, amountCents, voided) => ({ id, description, amountCents, voided });
const tabs = [
  // Two tabs settled today: $10.00 (Latte 4.50 + Croissant 3.50 + off-menu 2.00; one Latte voided)
  // and $7.00 (two Croissants) — gross $17.00, 3 Croissants, 1 Latte, 1 off-menu, 1 void.
  { tabKey: "vtx.tab.A", status: "settled", settledAt: at(14, 9), totalCents: 1000,
    lines: [line("line-1", "Latte", 450, false), line("line-2", "Latte", 450, true), line("line-3", "Croissant", 350, false), line("line-4", "PO probe cookie", 200, false)] },
  { tabKey: "vtx.tab.B", status: "settled", settledAt: at(14, 23), totalCents: 700,
    lines: [line("line-1", "Croissant", 350, false), line("line-2", "Croissant", 350, false)] },
  // A legacy amount-only void: total 900 against 1250 of live lines → -350 unitemized.
  { tabKey: "vtx.tab.C", status: "settled", settledAt: at(14, 10), totalCents: 900,
    lines: [line("line-1", "Latte", 450, false), line("line-2", "Latte", 450, false), line("line-3", "Croissant", 350, false)] },
  // Settled today with nothing ever rung up: not a sale, not counted.
  { tabKey: "vtx.tab.D", status: "settled", settledAt: at(14, 8), totalCents: 0, lines: [] },
  // Yesterday 23:00 and tomorrow 00:00 local: outside the day on both sides.
  { tabKey: "vtx.tab.E", status: "settled", settledAt: at(13, 23), totalCents: 5000, lines: [line("line-1", "Feast", 5000, false)] },
  { tabKey: "vtx.tab.F", status: "settled", settledAt: at(15, 0), totalCents: 5000, lines: [line("line-1", "Feast", 5000, false)] },
  // The first instant of today is today.
  { tabKey: "vtx.tab.I", status: "settled", settledAt: at(14, 0), totalCents: 100, lines: [line("line-1", "Espresso", 100, false)] },
  // Open right now with a running total: the grid's, not today's — the
  // status conjunct binds even on a row that somehow carries a settledAt.
  { tabKey: "vtx.tab.G", status: "open", openedAt: at(14, 11), settledAt: at(14, 11), totalCents: 9999, lines: [line("line-1", "Feast", 9999, false)] },
  // A settled tab whose settledAt is unparseable is skipped, not thrown on.
  { tabKey: "vtx.tab.H", status: "settled", settledAt: "not a date", totalCents: 5000, lines: [] },
];
const s = summarizeToday(tabs, now);
`); err != nil {
		t.Fatalf("fixture eval: %v", err)
	}
	return vm
}

func jsInt(t *testing.T, vm *goja.Runtime, expr string) int64 {
	t.Helper()
	v, err := vm.RunString(expr)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return v.ToInteger()
}

func jsString(t *testing.T, vm *goja.Runtime, expr string) string {
	t.Helper()
	v, err := vm.RunString(expr)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return v.String()
}

// TestSummarizeToday_FoldsSettledTabsOfTheLocalDay pins the desk's Today
// numbers: the tab count and gross come from the tabs settled on now's local
// calendar day (inclusive of 00:00, exclusive of the next), the item rows
// from their non-voided lines, the void line from their voided ones, and the
// unitemized remainder from a legacy amount-only void that moved the total
// without marking a line.
func TestSummarizeToday_FoldsSettledTabsOfTheLocalDay(t *testing.T) {
	vm := summarizeTodayVM(t)

	if got := jsInt(t, vm, "s.tabs"); got != 4 {
		t.Fatalf("tabs settled today = %d, want 4 (A, B, C, I — not the empty D, not yesterday/tomorrow, not the open tab)", got)
	}
	if got := jsInt(t, vm, "s.grossCents"); got != 2700 {
		t.Fatalf("grossCents = %d, want 2700 (1000 + 700 + 900 + 100, each tab's frozen total)", got)
	}
	// Items rank by cents, then count, then name: Croissant 4×1400, Latte 3×1350, cookie 1×200, Espresso 1×100.
	if got := jsString(t, vm, `s.items.map((i) => i.description + ":" + i.count + ":" + i.cents).join(",")`); got != "Croissant:4:1400,Latte:3:1350,PO probe cookie:1:200,Espresso:1:100" {
		t.Fatalf("items = %q, want Croissant:4:1400,Latte:3:1350,PO probe cookie:1:200,Espresso:1:100", got)
	}
	if got := jsInt(t, vm, "s.voidCount"); got != 1 {
		t.Fatalf("voidCount = %d, want 1 (A's voided Latte)", got)
	}
	if got := jsInt(t, vm, "s.voidCents"); got != 450 {
		t.Fatalf("voidCents = %d, want 450", got)
	}
	// Live lines total 3050 against a gross of 2700: C's amount-only void of 350.
	if got := jsInt(t, vm, "s.unitemizedCents"); got != -350 {
		t.Fatalf("unitemizedCents = %d, want -350 (C's legacy amount-only void)", got)
	}
}

// TestSummarizeToday_CounterPaidSumsOnlyTabsCarryingTheField pins
// counterPaidCents: summed only from today's settled tabs that carry
// paidAtSettleCents — a tab settled with no counter payment (the field
// absent) contributes nothing, not a zero counted toward the sum.
func TestSummarizeToday_CounterPaidSumsOnlyTabsCarryingTheField(t *testing.T) {
	vm := summarizeTodayVM(t)
	got := jsInt(t, vm, `
(() => {
  const now2 = new Date(2026, 8, 14, 12, 0, 0);
  const at2 = (h) => new Date(2026, 8, 14, h, 0, 0).toISOString();
  const mixed = [
    { tabKey: "vtx.tab.P1", status: "settled", settledAt: at2(9), totalCents: 1949, paidAtSettleCents: 1949,
      lines: [line("line-1", "Latte", 1949, false)] },
    { tabKey: "vtx.tab.P2", status: "settled", settledAt: at2(10), totalCents: 700,
      lines: [line("line-1", "Croissant", 700, false)] },
  ];
  return summarizeToday(mixed, now2).counterPaidCents;
})()`)
	if got != 1949 {
		t.Fatalf("counterPaidCents = %d, want 1949 (only P1 carries paidAtSettleCents; P2's absence contributes nothing)", got)
	}
}

// TestSummarizeToday_EmptyDayIsZero: a day with no settled tab folds to zero
// counts and no items, which renderFrontDeskToday hides rather than paints.
func TestSummarizeToday_EmptyDayIsZero(t *testing.T) {
	vm := summarizeTodayVM(t)
	if got := jsString(t, vm, `JSON.stringify(summarizeToday(tabs, new Date(2026, 8, 20, 12, 0, 0)))`); got != `{"tabs":0,"grossCents":0,"items":[],"voidCount":0,"voidCents":0,"unitemizedCents":0,"counterPaidCents":0}` {
		t.Fatalf("empty day = %s", got)
	}
	if got := jsString(t, vm, `JSON.stringify(summarizeToday([], now))`); !strings.HasPrefix(got, `{"tabs":0`) {
		t.Fatalf("no tabs = %s", got)
	}
	if got := jsString(t, vm, `JSON.stringify(summarizeToday(undefined, now))`); !strings.HasPrefix(got, `{"tabs":0`) {
		t.Fatalf("undefined tabs = %s", got)
	}
}

// TestSummarizeToday_DSTDayKeepsItsLastHour pins the day end as the next
// local midnight rather than start+24h: on the 25-hour fall-back day a fixed
// span ends at 23:00, dropping every tab settled in the day's last hour.
// goja's Date uses Go's time.Local, so the zone is pinned for the test's
// duration (the package's tests do not run in parallel).
func TestSummarizeToday_DSTDayKeepsItsLastHour(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("tzdata unavailable:", err)
	}
	prev := time.Local
	time.Local = la
	t.Cleanup(func() { time.Local = prev })

	vm := summarizeTodayVM(t)
	got := jsString(t, vm, `
(() => {
  const dst = new Date(2026, 10, 1, 12, 0, 0);
  if (new Date(2026, 10, 2).getTime() - new Date(2026, 10, 1).getTime() !== 25 * 3600 * 1000) return "not-a-25h-day";
  const late = { tabKey: "vtx.tab.L", status: "settled", settledAt: new Date(2026, 10, 1, 23, 30, 0).toISOString(), totalCents: 350, lines: [line("line-1", "Croissant", 350, false)] };
  const next = { tabKey: "vtx.tab.N", status: "settled", settledAt: new Date(2026, 10, 2, 0, 0, 0).toISOString(), totalCents: 999, lines: [line("line-1", "Feast", 999, false)] };
  const s = summarizeToday([late, next], dst);
  return s.tabs + ":" + s.grossCents;
})()`)
	if got != "1:350" {
		t.Fatalf("fall-back day fold = %q, want 1:350 (the 23:30 tab kept, the next-midnight tab excluded)", got)
	}
}
