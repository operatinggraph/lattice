package ruleengine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
)

// Story 3.1a Decision #7 — mandatory engine-selection tests.
//
// 1) simple-only Lens, simple parse succeeds → resolved=simple
// 2) simple-only Lens, simple parse fails    → InvalidRule (simple error)
// 3) full-only   Lens, stub fails by design  → InvalidRule (stub message)
// 4) absent ruleEngine, simple succeeds      → resolved=simple
// 5) absent ruleEngine, simple fails         → InvalidRule with BOTH errors
//
// These tests intentionally exercise ruleengine.Registry.SelectForLens
// directly so the selection contract is independent of lens.Parse plumbing.

const validMatch = "MATCH (a:agreement) RETURN a.id AS agreement_id"
const malformedMatch = "MATCH @@@ this is not a valid cypher rule"

func newRegistry() ruleengine.Registry {
	return ruleengine.NewRegistry(simple.New(), full.New())
}

// Test 1: explicit simple, succeeds.
func TestSelectForLens_ExplicitSimple_Succeeds(t *testing.T) {
	reg := newRegistry()
	eng, cr, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:         "lens-1",
		RuleBody:   validMatch,
		RuleEngine: ruleengine.EngineSimple,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if eng == nil || eng.Name() != ruleengine.EngineSimple {
		t.Fatalf("expected resolved engine=simple, got %#v", eng)
	}
	if cr == nil || cr.EngineName() != ruleengine.EngineSimple {
		t.Fatalf("expected compiled rule from simple, got %#v", cr)
	}
	if len(attempted) != 1 || attempted[0] != ruleengine.EngineSimple {
		t.Fatalf("expected attempted=[simple], got %v", attempted)
	}
}

// Test 2: explicit simple, parse fails → InvalidRule carrying simple error.
func TestSelectForLens_ExplicitSimple_Fails(t *testing.T) {
	reg := newRegistry()
	eng, cr, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:         "lens-2",
		RuleBody:   malformedMatch,
		RuleEngine: ruleengine.EngineSimple,
	})
	if err == nil {
		t.Fatalf("expected error, got success eng=%v cr=%v", eng, cr)
	}
	var se *ruleengine.SelectionError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SelectionError, got %T: %v", err, err)
	}
	if len(se.Errors) != 1 || se.Errors[0].Engine != ruleengine.EngineSimple {
		t.Fatalf("expected one simple ParseError, got %v", se.Errors)
	}
	if len(attempted) != 1 || attempted[0] != ruleengine.EngineSimple {
		t.Fatalf("expected attempted=[simple], got %v", attempted)
	}
}

// Test 3: explicit full, stub always fails.
func TestSelectForLens_ExplicitFull_StubFailsByDesign(t *testing.T) {
	reg := newRegistry()
	_, _, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:         "lens-3",
		RuleBody:   validMatch, // even valid bodies are rejected by the stub
		RuleEngine: ruleengine.EngineFull,
	})
	if err == nil {
		t.Fatalf("expected stub failure, got success")
	}
	var se *ruleengine.SelectionError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SelectionError, got %T: %v", err, err)
	}
	if len(se.Errors) != 1 || se.Errors[0].Engine != ruleengine.EngineFull {
		t.Fatalf("expected one full ParseError, got %v", se.Errors)
	}
	if !strings.Contains(se.Errors[0].Message, "not yet implemented") {
		t.Fatalf("expected stub message containing 'not yet implemented', got %q", se.Errors[0].Message)
	}
	if len(attempted) != 1 || attempted[0] != ruleengine.EngineFull {
		t.Fatalf("expected attempted=[full], got %v", attempted)
	}
}

// Test 4: absent ruleEngine, simple succeeds → resolved=simple, only simple attempted.
func TestSelectForLens_AbsentFallback_SimpleSucceeds(t *testing.T) {
	reg := newRegistry()
	eng, _, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:       "lens-4",
		RuleBody: validMatch,
		// RuleEngine intentionally absent.
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if eng == nil || eng.Name() != ruleengine.EngineSimple {
		t.Fatalf("expected resolved engine=simple, got %#v", eng)
	}
	if len(attempted) != 1 || attempted[0] != ruleengine.EngineSimple {
		t.Fatalf("expected attempted=[simple] (full not consulted on simple success), got %v", attempted)
	}
}

// Test 5: absent ruleEngine, simple AND full both fail → InvalidRule with BOTH errors.
func TestSelectForLens_AbsentFallback_BothFail(t *testing.T) {
	reg := newRegistry()
	_, _, attempted, err := reg.SelectForLens(ruleengine.LensDefinition{
		ID:       "lens-5",
		RuleBody: malformedMatch,
	})
	if err == nil {
		t.Fatalf("expected error, got success")
	}
	var se *ruleengine.SelectionError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SelectionError, got %T: %v", err, err)
	}
	if len(se.Errors) != 2 {
		t.Fatalf("expected 2 ParseErrors (simple + full), got %d: %v", len(se.Errors), se.Errors)
	}
	if se.Errors[0].Engine != ruleengine.EngineSimple {
		t.Fatalf("expected first error from simple, got %q", se.Errors[0].Engine)
	}
	if se.Errors[1].Engine != ruleengine.EngineFull {
		t.Fatalf("expected second error from full, got %q", se.Errors[1].Engine)
	}
	if !strings.Contains(se.Errors[1].Message, "not yet implemented") {
		t.Fatalf("expected full stub message, got %q", se.Errors[1].Message)
	}
	if len(attempted) != 2 || attempted[0] != ruleengine.EngineSimple || attempted[1] != ruleengine.EngineFull {
		t.Fatalf("expected attempted=[simple, full], got %v", attempted)
	}
}

// Bonus: Registry.List returns engines in sorted order.
func TestRegistry_List(t *testing.T) {
	reg := newRegistry()
	names := reg.List()
	if len(names) != 2 || names[0] != ruleengine.EngineFull || names[1] != ruleengine.EngineSimple {
		t.Fatalf("expected [full simple] (sorted), got %v", names)
	}
}
