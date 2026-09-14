package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// hasLaterVisitDecl lifts the shipped hasLaterVisit declaration out of the
// embedded app.js. It is the one predicate deciding whether a requested
// follow-up reads as addressed on the worklist — self-contained by
// construction (no reference to state/DOM/other app functions), so extracting
// and running the REAL source is what makes the assertions below a statement
// about what ships rather than about a copy in this file. Mirrors
// timeOffConflictDecl / timeoff_conflict_test.go.
var hasLaterVisitDecl = regexp.MustCompile(`(?s)\nfunction hasLaterVisit\(f, all\) \{\n.*?\n\}\n`)

func hasLaterVisitVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := hasLaterVisitDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function hasLaterVisit(f, all) {…}` declaration found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped hasLaterVisit: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("hasLaterVisit"))
	if !ok {
		t.Fatal("hasLaterVisit is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestHasLaterVisit pins the worklist's addressing rule to the one the
// followUpReminders lens applies (packages/clinic-reminders/followups.go): a
// requested follow-up is addressed by the same patient's later booked-or-
// attended visit at or after followUpDate WITH THE SAME PROVIDER — the
// treating provider asked for it. A follow-up whose visit carries no provider
// is addressed by any provider's visit. A cancelled or no-show visit never
// addresses anything, a visit before the target date does not, and a
// follow-up with no target date is never addressed. The fixtures mirror the
// lens's own cypher pins one for one so the two copies of the rule cannot
// drift apart unnoticed.
func TestHasLaterVisit(t *testing.T) {
	followUp := map[string]interface{}{
		"appointmentKey": "vtx.appointment.aaaaaaaaaaaaaaaaaaaa",
		"patientKey":     "vtx.patient.pppppppppppppppppppp",
		"providerKey":    "vtx.provider.oooooooooooooooooooo",
		"startsAt":       "2026-09-01T15:00:00Z",
		"followUpDate":   "2026-09-29T09:00:00Z",
	}
	visit := func(key, provider, startsAt, status string) map[string]interface{} {
		return map[string]interface{}{
			"appointmentKey": key,
			"patientKey":     "vtx.patient.pppppppppppppppppppp",
			"providerKey":    provider,
			"startsAt":       startsAt,
			"status":         status,
		}
	}
	const osei = "vtx.provider.oooooooooooooooooooo"
	const demo = "vtx.provider.dddddddddddddddddddd"

	run := func(t *testing.T, f map[string]interface{}, all []map[string]interface{}) bool {
		t.Helper()
		vm, fn := hasLaterVisitVM(t)
		list := make([]interface{}, 0, len(all))
		for _, a := range all {
			list = append(list, a)
		}
		v, err := fn(goja.Undefined(), vm.ToValue(f), vm.ToValue(list))
		if err != nil {
			t.Fatalf("hasLaterVisit threw: %v", err)
		}
		return v.ToBoolean()
	}

	cases := []struct {
		name string
		all  []map[string]interface{}
		want bool
	}{
		{"same provider at the date addresses it", []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", osei, "2026-09-29T10:00:00Z", "scheduled")}, true},
		{"same provider after the date addresses it", []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", osei, "2026-10-03T10:00:00Z", "confirmed")}, true},
		{"another provider's visit does not", []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", demo, "2026-09-29T10:00:00Z", "scheduled")}, false},
		{"a cancelled same-provider visit does not", []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", osei, "2026-09-29T10:00:00Z", "cancelled")}, false},
		{"a no-show same-provider visit does not", []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", osei, "2026-09-29T10:00:00Z", "noShow")}, false},
		{"a same-provider visit before the date does not", []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", osei, "2026-09-20T10:00:00Z", "scheduled")}, false},
		{"the follow-up's own appointment never addresses itself", []map[string]interface{}{visit("vtx.appointment.aaaaaaaaaaaaaaaaaaaa", osei, "2026-09-29T10:00:00Z", "completed")}, false},
		{"no later visit at all", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, followUp, tc.all); got != tc.want {
				t.Fatalf("hasLaterVisit = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("a follow-up with no provider is addressed by any provider", func(t *testing.T) {
		f := map[string]interface{}{}
		for k, v := range followUp {
			f[k] = v
		}
		delete(f, "providerKey")
		if !run(t, f, []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", demo, "2026-09-29T10:00:00Z", "scheduled")}) {
			t.Fatal("a provider-less follow-up must be addressed by any provider's qualifying visit")
		}
	})

	t.Run("a visit between a followUpDate set before the visit itself and the visit does not address it", func(t *testing.T) {
		f := map[string]interface{}{}
		for k, v := range followUp {
			f[k] = v
		}
		f["followUpDate"] = "2026-08-10T09:00:00Z"
		if run(t, f, []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", osei, "2026-08-20T10:00:00Z", "completed")}) {
			t.Fatal("a visit that predates the documented visit cannot have answered its follow-up")
		}
	})

	t.Run("a follow-up with no target date is never addressed", func(t *testing.T) {
		f := map[string]interface{}{}
		for k, v := range followUp {
			f[k] = v
		}
		delete(f, "followUpDate")
		if run(t, f, []map[string]interface{}{visit("vtx.appointment.bbbbbbbbbbbbbbbbbbbb", osei, "2026-09-29T10:00:00Z", "scheduled")}) {
			t.Fatal("a follow-up with no date has no due point a visit can satisfy")
		}
	})
}
