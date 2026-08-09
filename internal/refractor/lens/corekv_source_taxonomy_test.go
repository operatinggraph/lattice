package lens

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// These tests exercise the taxonomy accumulation added to CoreKVSource's
// meta watch (dynamic-type-taxonomy-design.md §14 Fire A item 4, amendment
// A1): a vtx.meta.> vertexType root, its canonicalName aspect, and a
// lnk.meta.*.subtypeOf.> edge are tracked into a []taxonomy.TypeSnapshot,
// recomputed and re-emitted only when it actually changes. Events are fed
// directly to handle() — no NATS/JetStream involved — so these run fully
// deterministically and exercise CoreKVSource's own logic, not the
// substrate transport (which internal/substrate/subscribe_test.go and
// TestCoreKVSource_LoadsLensFromAspect already cover end to end).

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testTaxonomySource(t *testing.T) (*CoreKVSource, *[][]taxonomy.TypeSnapshot) {
	t.Helper()
	src := NewCoreKVSource(nil, "core-kv", "test", discardTestLogger())
	var received [][]taxonomy.TypeSnapshot
	src.SetTaxonomyCallback(func(snap []taxonomy.TypeSnapshot) {
		// Copy defensively — recomputeTaxonomy hands out the slice it just
		// stored as lastTaxonomySnapshot, and a later rebuild replaces that
		// field wholesale rather than mutating in place, but copying keeps
		// this test's captured history immune to that implementation detail.
		cp := append([]taxonomy.TypeSnapshot(nil), snap...)
		received = append(received, cp)
	})
	return src, &received
}

func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

func vertexTypeBody(t *testing.T, abstract bool) []byte {
	t.Helper()
	data := map[string]any{}
	if abstract {
		data["abstract"] = true
	}
	body, err := json.Marshal(map[string]any{
		"class":     vertexTypeClassValue,
		"isDeleted": false,
		"data":      data,
	})
	require.NoError(t, err)
	return body
}

func canonicalNameBody(t *testing.T, name string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"class":     "canonicalName",
		"isDeleted": false,
		"data":      map[string]any{"value": name},
	})
	require.NoError(t, err)
	return body
}

func subtypeOfLinkBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"class":     "subtypeOf",
		"isDeleted": false,
		"data":      map[string]any{},
	})
	require.NoError(t, err)
	return body
}

// TestCoreKVSource_Taxonomy_AssemblesFromVertexAspectAndLink writes a
// vertexType vertex + its canonicalName aspect + a subtypeOf link, in the
// natural (parent-then-child-then-edge) order, and asserts the final
// snapshot matches the expected []taxonomy.TypeSnapshot.
func TestCoreKVSource_Taxonomy_AssemblesFromVertexAspectAndLink(t *testing.T) {
	src, received := testTaxonomySource(t)

	parentID := mustNanoID(t)
	leafID := mustNanoID(t)

	src.handle(substrate.KVEvent{Key: "vtx.meta." + parentID, Value: vertexTypeBody(t, true)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + parentID + ".canonicalName", Value: canonicalNameBody(t, "location")})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID, Value: vertexTypeBody(t, false)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	src.handle(substrate.KVEvent{Key: "lnk.meta." + leafID + ".subtypeOf.meta." + parentID, Value: subtypeOfLinkBody(t)})

	require.NotEmpty(t, *received, "taxonomyCB should have fired at least once")
	final := (*received)[len(*received)-1]
	expected := sortedByID([]taxonomy.TypeSnapshot{
		{ID: leafID, CanonicalName: "unit", Abstract: false, SubtypeOf: []string{"location"}},
		{ID: parentID, CanonicalName: "location", Abstract: true, SubtypeOf: nil},
	})
	require.Equal(t, expected, sortedByID(final))
}

// TestCoreKVSource_Taxonomy_OutOfOrderReplayConverges feeds the exact same
// five events in a different order (the link before either meta, echoing
// CDC's no-ordering-guarantee) and asserts the taxonomy converges to the
// SAME final snapshot as the ordered case.
func TestCoreKVSource_Taxonomy_OutOfOrderReplayConverges(t *testing.T) {
	src, received := testTaxonomySource(t)

	parentID := mustNanoID(t)
	leafID := mustNanoID(t)

	src.handle(substrate.KVEvent{Key: "lnk.meta." + leafID + ".subtypeOf.meta." + parentID, Value: subtypeOfLinkBody(t)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + parentID, Value: vertexTypeBody(t, true)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID, Value: vertexTypeBody(t, false)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + parentID + ".canonicalName", Value: canonicalNameBody(t, "location")})

	require.NotEmpty(t, *received)
	final := (*received)[len(*received)-1]
	expected := sortedByID([]taxonomy.TypeSnapshot{
		{ID: leafID, CanonicalName: "unit", Abstract: false, SubtypeOf: []string{"location"}},
		{ID: parentID, CanonicalName: "location", Abstract: true, SubtypeOf: nil},
	})
	require.Equal(t, expected, sortedByID(final))
}

// TestCoreKVSource_Taxonomy_LinkTombstoneRemovesEdge establishes an edge,
// then tombstones the link, and asserts the leaf's SubtypeOf drops it while
// both type entries remain.
func TestCoreKVSource_Taxonomy_LinkTombstoneRemovesEdge(t *testing.T) {
	src, received := testTaxonomySource(t)

	parentID := mustNanoID(t)
	leafID := mustNanoID(t)
	linkKey := "lnk.meta." + leafID + ".subtypeOf.meta." + parentID

	src.handle(substrate.KVEvent{Key: "vtx.meta." + parentID, Value: vertexTypeBody(t, true)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + parentID + ".canonicalName", Value: canonicalNameBody(t, "location")})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID, Value: vertexTypeBody(t, false)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	src.handle(substrate.KVEvent{Key: linkKey, Value: subtypeOfLinkBody(t)})

	before := (*received)[len(*received)-1]
	require.Contains(t, subtypeOfOf(before, leafID), "location")

	*received = nil
	src.handle(substrate.KVEvent{Key: linkKey, IsDeleted: true})

	require.NotEmpty(t, *received, "the tombstone must re-invoke the callback (the snapshot changed)")
	after := (*received)[len(*received)-1]
	require.Empty(t, subtypeOfOf(after, leafID), "the edge must be gone after the link tombstone")
	require.Len(t, after, 2, "both type entries stay — only the edge between them is retracted")
}

// TestCoreKVSource_Taxonomy_TypeMetaTombstoneRemovesEntry tombstones the
// leaf's root vertex and asserts its entry is gone from the next snapshot,
// while the parent's entry (and its own lack of any SubtypeOf) is
// unaffected.
func TestCoreKVSource_Taxonomy_TypeMetaTombstoneRemovesEntry(t *testing.T) {
	src, received := testTaxonomySource(t)

	parentID := mustNanoID(t)
	leafID := mustNanoID(t)

	src.handle(substrate.KVEvent{Key: "vtx.meta." + parentID, Value: vertexTypeBody(t, true)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + parentID + ".canonicalName", Value: canonicalNameBody(t, "location")})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID, Value: vertexTypeBody(t, false)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	src.handle(substrate.KVEvent{Key: "lnk.meta." + leafID + ".subtypeOf.meta." + parentID, Value: subtypeOfLinkBody(t)})

	*received = nil
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID, IsDeleted: true})

	require.NotEmpty(t, *received, "the type-meta tombstone must re-invoke the callback")
	after := (*received)[len(*received)-1]
	require.Len(t, after, 1)
	require.Equal(t, parentID, after[0].ID)
}

// TestCoreKVSource_Taxonomy_NonSubtypeOfLinkChangesNothing feeds a
// lnk.meta.* link whose linkName is NOT subtypeOf, and separately one whose
// second endpoint is not typed meta and one whose first is not, and asserts
// none of them is ever recorded as an edge — the "Unknown / malformed →
// ignore" posture handle()'s KindLink arm still falls through to for
// anything that doesn't shape-match a subtypeOf edge between two meta
// vertices. Asserted against the internal subtypeOf map directly (not just
// "no callback fired"), because with no vertexType ever declared for these
// ids the rebuilt snapshot would stay empty either way — a callback-only
// assertion here would not actually catch handleSubtypeOfLink's guard being
// deleted.
func TestCoreKVSource_Taxonomy_NonSubtypeOfLinkChangesNothing(t *testing.T) {
	src, received := testTaxonomySource(t)

	leafID := mustNanoID(t)
	parentID := mustNanoID(t)
	otherID := mustNanoID(t)

	// Different linkName.
	src.handle(substrate.KVEvent{Key: "lnk.meta." + leafID + ".parentOf.meta." + parentID, Value: subtypeOfLinkBody(t)})
	// Right linkName, wrong second-endpoint type.
	src.handle(substrate.KVEvent{Key: "lnk.meta." + leafID + ".subtypeOf.unit." + otherID, Value: subtypeOfLinkBody(t)})
	// Right linkName, wrong first-endpoint type.
	src.handle(substrate.KVEvent{Key: "lnk.unit." + leafID + ".subtypeOf.meta." + parentID, Value: subtypeOfLinkBody(t)})

	require.Empty(t, *received, "no taxonomy-shaped link was ever written — the callback must never fire")

	src.mu.RLock()
	defer src.mu.RUnlock()
	require.Empty(t, src.subtypeOf, "none of these shapes should have been recorded as a subtypeOf edge")
}

// TestCoreKVSource_Taxonomy_UnchangedRedeliveryDoesNotReinvoke establishes a
// REAL entry (vertexType + its canonicalName — not merely a vertexType event
// on its own, which after B5's fix contributes an EMPTY snapshot both before
// and after, so a callback-count assertion against it alone would pass even
// if taxonomySnapshotsEqual compared only len()), then redelivers each event
// unchanged and asserts neither re-invokes the callback. Also pins the
// established snapshot's exact content, not just its length, so reverting
// taxonomySnapshotsEqual to a length-only comparison fails this test. The
// vertexType-only event fires no callback (empty snapshot both before and
// after it, per B5) — only the canonicalName event, which completes a real
// entry, does.
func TestCoreKVSource_Taxonomy_UnchangedRedeliveryDoesNotReinvoke(t *testing.T) {
	src, received := testTaxonomySource(t)

	id := mustNanoID(t)
	vertexEvt := substrate.KVEvent{Key: "vtx.meta." + id, Value: vertexTypeBody(t, true)}
	nameEvt := substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", Value: canonicalNameBody(t, "location")}

	src.handle(vertexEvt)
	require.Empty(t, *received, "the vertexType event alone has no known canonicalName yet — empty snapshot, no callback (B5)")

	src.handle(nameEvt)
	require.Len(t, *received, 1, "the canonicalName event completes a real entry — this is the one change")
	final := (*received)[len(*received)-1]
	require.Equal(t, []taxonomy.TypeSnapshot{
		{ID: id, CanonicalName: "location", Abstract: true, SubtypeOf: nil},
	}, final, "the established entry must carry real content")

	src.handle(vertexEvt)
	require.Len(t, *received, 1, "a redelivered, unchanged vertexType event must not re-invoke the callback")

	src.handle(nameEvt)
	require.Len(t, *received, 1, "a redelivered, unchanged canonicalName event must not re-invoke the callback")
}

// TestCoreKVSource_Taxonomy_NonBoolAbstractFailsClosedToConcrete asserts
// abstractFlagFromData's THIS-consumer fail-closed direction (B4): a present
// but non-bool `data.abstract` must resolve to CONCRETE (false) — the
// opposite of internal/processor/ddl_cache.go's own fail-closed direction
// for the identical field, because abstract=true is what silently NARROWS a
// live expansion set here.
func TestCoreKVSource_Taxonomy_NonBoolAbstractFailsClosedToConcrete(t *testing.T) {
	src, received := testTaxonomySource(t)
	id := mustNanoID(t)

	body, err := json.Marshal(map[string]any{
		"class":     vertexTypeClassValue,
		"isDeleted": false,
		"data":      map[string]any{"abstract": "yes"}, // present, not a JSON bool
	})
	require.NoError(t, err)
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id, Value: body})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", Value: canonicalNameBody(t, "mystery")})

	require.NotEmpty(t, *received)
	final := (*received)[len(*received)-1]
	require.Equal(t, []taxonomy.TypeSnapshot{
		{ID: id, CanonicalName: "mystery", Abstract: false, SubtypeOf: nil},
	}, final, "a non-bool data.abstract must fail closed to CONCRETE for this accumulator")
}

// TestCoreKVSource_Taxonomy_CanonicalNameTombstoneClearsEntry writes a
// vertexType + its canonicalName, then tombstones only the canonicalName
// aspect (the root vertex stays live), and asserts the entry disappears
// (B5: an id with no known canonicalName is omitted, not merely blanked).
func TestCoreKVSource_Taxonomy_CanonicalNameTombstoneClearsEntry(t *testing.T) {
	src, received := testTaxonomySource(t)
	id := mustNanoID(t)

	src.handle(substrate.KVEvent{Key: "vtx.meta." + id, Value: vertexTypeBody(t, false)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	require.Len(t, (*received)[len(*received)-1], 1, "the name is established")

	*received = nil
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", IsDeleted: true})

	require.NotEmpty(t, *received, "the canonicalName tombstone must re-invoke the callback")
	require.Empty(t, (*received)[len(*received)-1], "the entry is omitted once its canonicalName is gone — the vertexType root is still live")
}

// TestCoreKVSource_Taxonomy_MalformedOrEmptyCanonicalNameClearsThePreviousName
// covers B1: once a name is established, a malformed rewrite (bad JSON) and
// an empty data.value must each CLEAR the previously-tracked name, never
// silently retain it — this map is incrementally accumulated, so "do
// nothing" here is not the harmless default it is in ddl_cache.go's
// from-scratch rebuild.
func TestCoreKVSource_Taxonomy_MalformedOrEmptyCanonicalNameClearsThePreviousName(t *testing.T) {
	src, received := testTaxonomySource(t)
	id := mustNanoID(t)

	src.handle(substrate.KVEvent{Key: "vtx.meta." + id, Value: vertexTypeBody(t, false)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	require.Len(t, (*received)[len(*received)-1], 1, "the name is established")

	*received = nil
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", Value: []byte(`not json`)})
	require.NotEmpty(t, *received, "a malformed rewrite must clear the previous name")
	require.Empty(t, (*received)[len(*received)-1], "the entry disappears once its name is cleared")

	// Re-establish, then clear via an explicit empty data.value.
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	require.Len(t, (*received)[len(*received)-1], 1)
	*received = nil
	emptyBody, err := json.Marshal(map[string]any{
		"class": "canonicalName", "isDeleted": false, "data": map[string]any{"value": ""},
	})
	require.NoError(t, err)
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", Value: emptyBody})
	require.NotEmpty(t, *received, "an empty data.value must clear the previous name too")
	require.Empty(t, (*received)[len(*received)-1])
}

// TestCoreKVSource_Taxonomy_ClassChangeRetractsVertexType covers B2: a LIVE
// rewrite of a tracked vertexType's root — not a tombstone — to a different
// class must retract it from the taxonomy just as a tombstone would.
func TestCoreKVSource_Taxonomy_ClassChangeRetractsVertexType(t *testing.T) {
	src, received := testTaxonomySource(t)
	id := mustNanoID(t)

	src.handle(substrate.KVEvent{Key: "vtx.meta." + id, Value: vertexTypeBody(t, false)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	require.Len(t, (*received)[len(*received)-1], 1, "the vertexType is tracked")

	*received = nil
	otherClassBody, err := json.Marshal(map[string]any{
		"class": "meta.ddl.linkType", "isDeleted": false, "data": map[string]any{},
	})
	require.NoError(t, err)
	src.handle(substrate.KVEvent{Key: "vtx.meta." + id, Value: otherClassBody})

	require.NotEmpty(t, *received, "a live class change away from vertexType must retract the entry")
	require.Empty(t, (*received)[len(*received)-1], "id is no longer resolvable as a type once its root's class changed")
}

// TestCoreKVSource_Taxonomy_ParentNotAVertexTypeContributesNoEdge covers
// B3(a): a subtypeOf link's parent id that never became a tracked vertexType
// — e.g. a role or lens meta that happens to share a canonicalName with an
// unrelated real vertexType (pkgmgr writes canonicalName aspects for those
// too, not only vertexType roots) — must contribute NO edge, even though its
// canonicalName alone would otherwise resolve.
func TestCoreKVSource_Taxonomy_ParentNotAVertexTypeContributesNoEdge(t *testing.T) {
	src, received := testTaxonomySource(t)

	leafID := mustNanoID(t)
	impostorID := mustNanoID(t)

	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID, Value: vertexTypeBody(t, false)})
	src.handle(substrate.KVEvent{Key: "vtx.meta." + leafID + ".canonicalName", Value: canonicalNameBody(t, "unit")})
	// impostorID gets a canonicalName aspect but NEVER a vtx.meta.<id>
	// vertexType event — it is never tracked in vertexTypes.
	src.handle(substrate.KVEvent{Key: "vtx.meta." + impostorID + ".canonicalName", Value: canonicalNameBody(t, "booking")})
	src.handle(substrate.KVEvent{Key: "lnk.meta." + leafID + ".subtypeOf.meta." + impostorID, Value: subtypeOfLinkBody(t)})

	require.NotEmpty(t, *received)
	final := (*received)[len(*received)-1]
	require.Equal(t, []taxonomy.TypeSnapshot{
		{ID: leafID, CanonicalName: "unit", Abstract: false, SubtypeOf: nil},
	}, final, "a subtypeOf parent that never became a vertexType must contribute NO edge")
}

// sortedByID returns snap sorted by ID for order-independent assertions —
// buildTaxonomySnapshotLocked already sorts, but this keeps the test
// resilient to that detail rather than depending on it.
func sortedByID(snap []taxonomy.TypeSnapshot) []taxonomy.TypeSnapshot {
	out := append([]taxonomy.TypeSnapshot(nil), snap...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func subtypeOfOf(snap []taxonomy.TypeSnapshot, id string) []string {
	for _, s := range snap {
		if s.ID == id {
			return s.SubtypeOf
		}
	}
	return nil
}
