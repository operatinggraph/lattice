package ruleengine_test

import (
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// Full-only engine-selection tests, post simple-engine retirement.
//
// 1) "full"    Lens, parse succeeds → resolved=full
// 2) "full"    Lens, parse fails    → SelectionError (full error)
// 3) absent    Lens, parse succeeds → resolved=full
// 4) "simple"  Lens (or any other unknown value) → SelectionError, no engine attempted
//
// These tests intentionally exercise ruleengine.Registry.SelectForLens
// directly so the selection contract is independent of lens.Parse plumbing.

const validMatch = "MATCH (a:agreement) RETURN a.id AS agreement_id"
const malformedMatch = "MATCH @@@ this is not a valid cypher rule"

func newRegistry() ruleengine.Registry {
	return ruleengine.NewRegistry(full.New())
}

// Test 1: explicit full, valid cypher succeeds.
func TestSelectForLens_ExplicitFull_Succeeds(t *testing.T) {
	reg := newRegistry()
	body := `MATCH (a) OPTIONAL MATCH (a)-[:r]->(b) WITH a, b RETURN a, b`
	eng, cr, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:         "lens-1",
		RuleBody:   body,
		RuleEngine: ruleengine.EngineFull,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if eng == nil || eng.Name() != ruleengine.EngineFull {
		t.Fatalf("expected resolved engine=full, got %#v", eng)
	}
	if cr == nil || cr.EngineName() != ruleengine.EngineFull {
		t.Fatalf("expected compiled rule from full, got %#v", cr)
	}
	if len(attempted) != 1 || attempted[0] != ruleengine.EngineFull {
		t.Fatalf("expected attempted=[full], got %v", attempted)
	}
}

// Test 2: explicit full, parse fails on invalid cypher.
func TestSelectForLens_ExplicitFull_FailsOnInvalidCypher(t *testing.T) {
	reg := newRegistry()
	_, _, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:         "lens-2",
		RuleBody:   malformedMatch,
		RuleEngine: ruleengine.EngineFull,
	})
	if err == nil {
		t.Fatalf("expected parse failure, got success")
	}
	var se *ruleengine.SelectionError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SelectionError, got %T: %v", err, err)
	}
	if len(se.Errors) != 1 || se.Errors[0].Engine != ruleengine.EngineFull {
		t.Fatalf("expected one full ParseError, got %v", se.Errors)
	}
	if len(attempted) != 1 || attempted[0] != ruleengine.EngineFull {
		t.Fatalf("expected attempted=[full], got %v", attempted)
	}
}

// Test 3: absent ruleEngine resolves to full and succeeds.
func TestSelectForLens_Absent_ResolvesToFull(t *testing.T) {
	reg := newRegistry()
	eng, cr, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:       "lens-3",
		RuleBody: validMatch,
		// RuleEngine intentionally absent.
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if eng == nil || eng.Name() != ruleengine.EngineFull {
		t.Fatalf("expected resolved engine=full, got %#v", eng)
	}
	if cr == nil || cr.EngineName() != ruleengine.EngineFull {
		t.Fatalf("expected compiled rule from full, got %#v", cr)
	}
	if len(attempted) != 1 || attempted[0] != ruleengine.EngineFull {
		t.Fatalf("expected attempted=[full], got %v", attempted)
	}
}

// Test 4: any unknown ruleEngine value (including the retired "simple")
// fails selection immediately, with no engine attempted.
func TestSelectForLens_UnknownEngine_Rejected(t *testing.T) {
	reg := newRegistry()
	eng, cr, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:         "lens-4",
		RuleBody:   validMatch,
		RuleEngine: "simple",
	})
	if err == nil {
		t.Fatalf("expected error, got success eng=%v cr=%v", eng, cr)
	}
	var se *ruleengine.SelectionError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SelectionError, got %T: %v", err, err)
	}
	if len(se.Errors) != 1 || se.Errors[0].Engine != "simple" {
		t.Fatalf("expected one unknown-engine ParseError, got %v", se.Errors)
	}
	if len(attempted) != 0 {
		t.Fatalf("expected no engine attempted, got %v", attempted)
	}
}

// Bonus: Registry.List returns engines in sorted order.
func TestRegistry_List(t *testing.T) {
	reg := newRegistry()
	names := reg.List()
	if len(names) != 1 || names[0] != ruleengine.EngineFull {
		t.Fatalf("expected [full], got %v", names)
	}
}
