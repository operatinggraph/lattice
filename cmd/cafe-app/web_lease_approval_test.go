package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// residentOpenTabAllowed is the resident self-service mirror of
// fillLeaseSelect's own `approved === false` AND `tenancyEnded` disabled-
// option gates — lifted out of the embedded app.js the same way
// web_hold_test.go lifts openTabGate (money rides along there because
// arrearsBadge formats through it; tenancyEnded rides along here because
// residentOpenTabAllowed calls it), so the pin is about the rule that
// ships, not a copy of it.
var residentOpenTabAllowedDecl = regexp.MustCompile(`(?s)\nfunction residentOpenTabAllowed\(row, now\) \{\n.*?\n\}\n`)
var tenancyEndedDecl = regexp.MustCompile(`(?s)\nfunction tenancyEnded\(detail, now\) \{\n.*?\n\}\n`)

func residentOpenTabAllowedVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	for _, d := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"tenancyEnded", tenancyEndedDecl},
		{"residentOpenTabAllowed", residentOpenTabAllowedDecl},
	} {
		decl := d.re.FindString(text)
		if decl == "" {
			t.Fatalf("app.js: no top-level `function %s(…) {…}` declaration found — the extraction regex no longer matches this file", d.name)
		}
		if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
			t.Fatalf("%s reaches the DOM; it must stay a pure function so the gate is testable:\n%s", d.name, decl)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped %s: %v", d.name, err)
		}
	}
	return vm
}

// TestResidentOpenTabAllowed_BlocksOnlyOnPositiveEvidence pins the same
// only-block-on-positive-evidence posture fillLeaseSelect's own
// `approved === false` check takes: an approved row and a row with no
// `approved` field at all (a roster that predates the field, or one the
// resident session cannot yet join) both allow the Open Tab button; only a
// row that positively says approved:false withholds it. No row at all (the
// roster hasn't resolved, or the fetch failed) allows it too — a resident's
// own tab is never wrongly withheld by a transient read failure.
func TestResidentOpenTabAllowed_BlocksOnlyOnPositiveEvidence(t *testing.T) {
	vm := residentOpenTabAllowedVM(t)
	if _, err := vm.RunString(`
const now = new Date("2026-09-15T00:00:00Z");
const approved = { leaseAppKey: "vtx.leaseapp.a", approved: true };
const notApproved = { leaseAppKey: "vtx.leaseapp.a", approved: false };
const noField = { leaseAppKey: "vtx.leaseapp.a" };
`); err != nil {
		t.Fatalf("fixture eval: %v", err)
	}
	for expr, want := range map[string]bool{
		"residentOpenTabAllowed(approved, now)":    true,
		"residentOpenTabAllowed(notApproved, now)": false,
		"residentOpenTabAllowed(noField, now)":     true,
		"residentOpenTabAllowed(undefined, now)":   true,
		"residentOpenTabAllowed(null, now)":        true,
	} {
		v, err := vm.RunString(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got := v.ToBoolean(); got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
}

// TestResidentOpenTabAllowed_TenancyEndedVectors proves the second gate
// folded into residentOpenTabAllowed: a lease's own leaseEnd column
// (carried on the /api/residents row from cafe-domain's cafeLeaseWorkplaces
// lens, residents.go's leaseTenancyEnds) withholds the resident's own Open Tab button on the
// same inclusive boundary OpenTab's TenancyEnded guard applies — a
// positive ended lease is blocked, a lease with no projected term is never
// wrongly withheld, and the boundary instant itself (leaseEnd == now) is
// already past, mirroring TestOpenTab_RejectsEndedTenancy's own "AT
// leaseEnd is refused" vector (packages/cafe-domain/integration_test.go).
func TestResidentOpenTabAllowed_TenancyEndedVectors(t *testing.T) {
	vm := residentOpenTabAllowedVM(t)
	if _, err := vm.RunString(`
const now = new Date("2026-09-15T00:00:00Z");
const ended = { leaseAppKey: "vtx.leaseapp.a", approved: true, leaseEnd: "2026-09-01T00:00:00Z" };
const noTerm = { leaseAppKey: "vtx.leaseapp.a", approved: true };
const atBoundary = { leaseAppKey: "vtx.leaseapp.a", approved: true, leaseEnd: "2026-09-15T00:00:00Z" };
const notYetEnded = { leaseAppKey: "vtx.leaseapp.a", approved: true, leaseEnd: "2026-09-16T00:00:00Z" };
`); err != nil {
		t.Fatalf("fixture eval: %v", err)
	}
	for expr, want := range map[string]bool{
		"residentOpenTabAllowed(ended, now)":       false,
		"residentOpenTabAllowed(noTerm, now)":      true,
		"residentOpenTabAllowed(atBoundary, now)":  false,
		"residentOpenTabAllowed(notYetEnded, now)": true,
	} {
		v, err := vm.RunString(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got := v.ToBoolean(); got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
}
