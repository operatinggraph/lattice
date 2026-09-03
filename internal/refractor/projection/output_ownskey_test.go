package projection_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// capReadPerEntryDescriptor is the shipped perEntry read-grant shape: one key
// per (actor, anchor) under `cap-read.`, with the anchor id appended after the
// rendered pattern.
func capReadPerEntryDescriptor(t *testing.T) projection.OutputDescriptor {
	t.Helper()
	desc, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "cap-read.{actorSuffix}",
		BodyColumns:      []string{"readableAnchors"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "anchorId",
		EntryKeyColumn:   "anchorId",
		Freshness:        "auto",
	})
	if err != nil {
		t.Fatalf("parse descriptor: %v", err)
	}
	return desc
}

// TestOwnsKey_PerEntryLensClaimsItsOwnLegacyParentDocument is the one shape
// AnchorFromKey cannot answer for. A lens that flipped to per-entry keys wrote
// the parent document at the bare parent key itself, before the flip; its
// migration transport still tombstones that document on an actor's first
// post-flip evaluation, and no sweep claims an orphaned one. A purge that
// skipped it would leave a live read grant nothing can ever reach.
func TestOwnsKey_PerEntryLensClaimsItsOwnLegacyParentDocument(t *testing.T) {
	desc := capReadPerEntryDescriptor(t)
	const actor = "ZwqPmRtw9nbCxz5vQ2yH"
	const entry = "Kx3TmZpq7RvwNsY2Hc9L"

	parent := "cap-read.identity." + actor
	child := parent + "." + entry

	if _, ok := desc.AnchorFromKey(parent); ok {
		t.Fatal("precondition: the narrower inverse must NOT claim the parent key, or this test proves nothing")
	}
	if !desc.OwnsKey(parent) {
		t.Fatal("a perEntry lens owns the pre-flip parent document it wrote itself")
	}
	if !desc.OwnsKey(child) {
		t.Fatal("and it still owns its per-entry keys")
	}
}

// The widening is confined to this lens's own key space: a sibling producer's
// key under a DIFFERENT literal prefix is claimed by neither arm, and neither is
// a key whose recovered anchor is of the wrong type or is not a vertex key at
// all.
func TestOwnsKey_ClaimsNoSiblingProducersKey(t *testing.T) {
	desc := capReadPerEntryDescriptor(t)
	const actor = "ZwqPmRtw9nbCxz5vQ2yH"

	for _, key := range []string{
		"cap-read-x.identity." + actor,
		"cap-read.svc.identity." + actor,
		"cap-read.provider." + actor,
		"cap-read.identity." + actor + ".notananoid",
		"cap.identity." + actor,
		"cap-read.",
	} {
		if desc.OwnsKey(key) {
			t.Fatalf("a purge must not claim %q — no lens re-derives what it removes there", key)
		}
	}
}

// A doc-mode descriptor's ownership is exactly AnchorFromKey: the widening arm
// is gated on EntryKeyColumn, so nothing about the non-perEntry corpus moves.
func TestOwnsKey_DocModeOwnershipIsUnchanged(t *testing.T) {
	desc, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "cap.{actorSuffix}",
		BodyColumns:      []string{"platformPermissions"},
		EmptyBehavior:    "delete",
		Freshness:        "auto",
	})
	if err != nil {
		t.Fatalf("parse descriptor: %v", err)
	}
	const actor = "ZwqPmRtw9nbCxz5vQ2yH"
	for _, tc := range []struct {
		key  string
		owns bool
	}{
		{"cap.identity." + actor, true},
		{"cap.roles.identity." + actor, false},
		{"cap.ephemeral.identity." + actor, false},
		{"cap.svc.identity." + actor, false},
		{"cap.role-by-operation.CreatePatient", false},
		{"cap.identity." + actor + ".Kx3TmZpq7RvwNsY2Hc9L", false},
	} {
		if got := desc.OwnsKey(tc.key); got != tc.owns {
			t.Fatalf("OwnsKey(%q) = %v, want %v", tc.key, got, tc.owns)
		}
		_, viaInverse := desc.AnchorFromKey(tc.key)
		if viaInverse != tc.owns {
			t.Fatalf("a doc-mode descriptor's ownership must stay exactly its inverse; AnchorFromKey(%q) = %v", tc.key, viaInverse)
		}
	}
}
