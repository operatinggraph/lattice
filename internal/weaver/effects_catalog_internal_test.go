package weaver

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// opMetaVertexClass mirrors pkgmgr's opMetaClass constant: an op-meta vertex
// is a non-routed meta.ddl.vertexType carrying operationType on its envelope
// data (registry.go's handle() routes anything not weaverTargetClass/
// loomPatternClass to indexOpMeta regardless of the exact class string).
const opMetaVertexClass = "meta.ddl.vertexType"

func opMetaVertexEvent(t *testing.T, id, operationType string) substrate.KVEvent {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"class": opMetaVertexClass,
		"data":  map[string]any{"operationType": operationType},
	})
	if err != nil {
		t.Fatalf("marshal op-meta vertex: %v", err)
	}
	return substrate.KVEvent{Key: "vtx.meta." + id, Value: body}
}

// opEffectsAspectEvent builds the real envelope shape pkgmgr's docAspect
// writes (class/isDeleted/data/vertexKey/localName) so the test proves
// indexOpEffects unwraps it exactly like a live CDC delivery would.
func opEffectsAspectEvent(t *testing.T, id string, guards []string) substrate.KVEvent {
	t.Helper()
	raw := make([]json.RawMessage, len(guards))
	for i, g := range guards {
		raw[i] = json.RawMessage(g)
	}
	body, err := json.Marshal(map[string]any{
		"class":     "effects",
		"isDeleted": false,
		"data":      map[string]any{"guards": raw},
		"vertexKey": "vtx.meta." + id,
		"localName": "effects",
	})
	if err != nil {
		t.Fatalf("marshal effects aspect: %v", err)
	}
	return substrate.KVEvent{Key: "vtx.meta." + id + ".effects", Value: body}
}

func TestEffectsCatalog_VertexThenEffectsAspect(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	s.handle(opMetaVertexEvent(t, id, "SignLease"))
	s.handle(opEffectsAspectEvent(t, id, []string{`{"present":"subject.signature.data.signedAt"}`}))

	catalog := s.effectsCatalog()
	if len(catalog) != 1 || catalog[0].Ref != "SignLease" {
		t.Fatalf("effectsCatalog = %+v, want one action Ref=SignLease", catalog)
	}
	if catalog[0].Cost != 1 {
		t.Errorf("Cost = %d, want 1", catalog[0].Cost)
	}
	if len(catalog[0].Effects) != 1 {
		t.Errorf("Effects = %v, want 1 guard", catalog[0].Effects)
	}
}

// TestEffectsCatalog_EffectsAspectThenVertex_OrderIndependent proves the
// join is order-independent: CDC may deliver an op-meta vertex's `.effects`
// aspect before its envelope (replay ordering is per-key, not per-vertex).
func TestEffectsCatalog_EffectsAspectThenVertex_OrderIndependent(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	s.handle(opEffectsAspectEvent(t, id, []string{`{"present":"subject.signature.data.signedAt"}`}))
	s.handle(opMetaVertexEvent(t, id, "SignLease"))

	catalog := s.effectsCatalog()
	if len(catalog) != 1 || catalog[0].Ref != "SignLease" {
		t.Fatalf("effectsCatalog = %+v, want one action Ref=SignLease (order-independent join)", catalog)
	}
}

func TestEffectsCatalog_OpWithNoEffectsExcluded(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	s.handle(opMetaVertexEvent(t, id, "CreateLeaseApplication"))

	if catalog := s.effectsCatalog(); len(catalog) != 0 {
		t.Fatalf("effectsCatalog = %+v, want empty for an op with no declared Effects", catalog)
	}
}

func TestEffectsCatalog_MalformedGuardDropped(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	s.handle(opMetaVertexEvent(t, id, "SignLease"))
	s.handle(opEffectsAspectEvent(t, id, []string{`{"exists":"subject.signature.data.signedAt"}`}))

	if catalog := s.effectsCatalog(); len(catalog) != 0 {
		t.Fatalf("effectsCatalog = %+v, want empty — a malformed guard must never enter the catalog "+
			"(pkgmgr's install-time validateEffects should already have rejected it; this is defense-in-depth)", catalog)
	}
}

func TestEffectsCatalog_VertexDeleteRemovesEffects(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	s.handle(opMetaVertexEvent(t, id, "SignLease"))
	s.handle(opEffectsAspectEvent(t, id, []string{`{"present":"subject.signature.data.signedAt"}`}))
	if len(s.effectsCatalog()) != 1 {
		t.Fatalf("setup: expected 1 catalog entry before delete")
	}

	s.handle(substrate.KVEvent{Key: "vtx.meta." + id, IsDeleted: true})

	if catalog := s.effectsCatalog(); len(catalog) != 0 {
		t.Fatalf("effectsCatalog = %+v, want empty after the op-meta vertex is deleted", catalog)
	}
}

func TestEffectsCatalog_EffectsAspectDeleteRemovesEntry(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	s.handle(opMetaVertexEvent(t, id, "SignLease"))
	s.handle(opEffectsAspectEvent(t, id, []string{`{"present":"subject.signature.data.signedAt"}`}))
	if len(s.effectsCatalog()) != 1 {
		t.Fatalf("setup: expected 1 catalog entry before delete")
	}

	s.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".effects", IsDeleted: true})

	if catalog := s.effectsCatalog(); len(catalog) != 0 {
		t.Fatalf("effectsCatalog = %+v, want empty once the .effects aspect alone is deleted", catalog)
	}
}

func TestEffectsCatalog_DeterministicOrderAcrossMultipleOps(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	idB := testNanoID(t)
	idA := testNanoID(t)
	s.handle(opMetaVertexEvent(t, idB, "SignLease"))
	s.handle(opEffectsAspectEvent(t, idB, []string{`{"present":"subject.signature.data.signedAt"}`}))
	s.handle(opMetaVertexEvent(t, idA, "DecideLeaseApplication"))
	s.handle(opEffectsAspectEvent(t, idA, []string{`{"present":"subject.decision.data.value"}`}))

	catalog := s.effectsCatalog()
	if len(catalog) != 2 || catalog[0].Ref != "DecideLeaseApplication" || catalog[1].Ref != "SignLease" {
		t.Fatalf("effectsCatalog = %+v, want [DecideLeaseApplication, SignLease] lexicographic", catalog)
	}
}
