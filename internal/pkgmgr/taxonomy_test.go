package pkgmgr

import (
	"errors"
	"testing"
)

// TestWalkTaxonomyNoCycle_MultipleParentsAccepted proves the graph algorithm
// itself supports §3.4's "multiple parents: allowed" — a leaf ("room") with
// TWO parent edges converging on a shared ancestor ("entity") is a diamond,
// not a cycle, so the walk must not reject it. Exercised directly against the
// graph function rather than through Install: DDLSpec.SubtypeOfRef is a
// single string (§3.5), and a leaf's canonicalName belongs to exactly one
// package (checkCanonicalNameCollision), so no sequence of installs under
// the current declaration surface can actually put two live parent edges on
// one leaf — the algorithm is proven correct against a shape the write path
// cannot yet produce, ahead of whatever surface eventually lets it.
func TestWalkTaxonomyNoCycle_MultipleParentsAccepted(t *testing.T) {
	adj := map[string][]string{
		"room":     {"location", "billable"},
		"location": {"entity"},
		"billable": {"entity"},
	}
	if err := walkTaxonomyNoCycle("room", adj, map[string]bool{"room": true}, 0); err != nil {
		t.Fatalf("a multi-parent diamond must not be flagged as a cycle: %v", err)
	}
}

func TestWalkTaxonomyNoCycle_CycleRejected(t *testing.T) {
	adj := map[string][]string{"a": {"b"}, "b": {"a"}}
	err := walkTaxonomyNoCycle("a", adj, map[string]bool{"a": true}, 0)
	if !errors.Is(err, ErrTaxonomyCycle) {
		t.Fatalf("expected ErrTaxonomyCycle, got %v", err)
	}
}

func TestWalkTaxonomyNoCycle_DepthBound(t *testing.T) {
	// a -> b -> c -> d -> e: exactly 4 hops from a, at the bound.
	within := map[string][]string{
		"a": {"b"}, "b": {"c"}, "c": {"d"}, "d": {"e"},
	}
	if err := walkTaxonomyNoCycle("a", within, map[string]bool{"a": true}, 0); err != nil {
		t.Fatalf("a 4-hop chain (at maxTaxonomyDepth) must be accepted: %v", err)
	}

	// a -> b -> c -> d -> e -> f: 5 hops from a, exceeding the bound.
	exceeding := map[string][]string{
		"a": {"b"}, "b": {"c"}, "c": {"d"}, "d": {"e"}, "e": {"f"},
	}
	err := walkTaxonomyNoCycle("a", exceeding, map[string]bool{"a": true}, 0)
	if !errors.Is(err, ErrTaxonomyCycle) {
		t.Fatalf("a 5-hop chain must exceed maxTaxonomyDepth, got %v", err)
	}
}

// TestWalkTaxonomyNoCycle_SelfLoopRejected is the degenerate 1-node cycle: a
// node naming itself as its own parent.
func TestWalkTaxonomyNoCycle_SelfLoopRejected(t *testing.T) {
	adj := map[string][]string{"a": {"a"}}
	err := walkTaxonomyNoCycle("a", adj, map[string]bool{"a": true}, 0)
	if !errors.Is(err, ErrTaxonomyCycle) {
		t.Fatalf("expected ErrTaxonomyCycle for a self-loop, got %v", err)
	}
}
