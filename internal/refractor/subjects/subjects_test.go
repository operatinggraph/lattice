package subjects

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDLQ(t *testing.T) {
	tests := []struct {
		lensID, want string
	}{
		{"agreement-summary", "lattice.refractor.dlq.agreement-summary"},
		{"occupancy-view", "lattice.refractor.dlq.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, DLQ(tt.lensID))
	}
}

func TestDLQ_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { DLQ("") })
	assert.Panics(t, func() { DLQ("lens.id") })
}

func TestMetrics_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { Metrics("") })
	assert.Panics(t, func() { Metrics("lens.id") })
}

func TestAudit_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { Audit("") })
	assert.Panics(t, func() { Audit("rule>") })
}

func TestMetrics(t *testing.T) {
	tests := []struct {
		lensID, want string
	}{
		{"agreement-summary", "lattice.refractor.metrics.agreement-summary"},
		{"occupancy-view", "lattice.refractor.metrics.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Metrics(tt.lensID))
	}
}

func TestAdjKey(t *testing.T) {
	tests := []struct {
		nodeID, want string
	}{
		{"nodeA", "adj.nodeA"},
		{"agreement-123", "adj.agreement-123"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, AdjKey(tt.nodeID))
	}
}

func TestAdjKey_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { AdjKey("") })
	assert.Panics(t, func() { AdjKey("node.id") })
	assert.Panics(t, func() { AdjKey("node*") })
	assert.Panics(t, func() { AdjKey("node>") })
	assert.Panics(t, func() { AdjKey("node id") })
}

func TestAdjMarkKey(t *testing.T) {
	tests := []struct {
		nodeID, want string
	}{
		{"nodeA", "adjmark.nodeA"},
		{"agreement-123", "adjmark.agreement-123"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, AdjMarkKey(tt.nodeID))
	}
}

func TestAdjMarkKey_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { AdjMarkKey("") })
	assert.Panics(t, func() { AdjMarkKey("node.id") })
	assert.Panics(t, func() { AdjMarkKey("node*") })
	assert.Panics(t, func() { AdjMarkKey("node>") })
	assert.Panics(t, func() { AdjMarkKey("node id") })
}

// TestAdjMarkKey_DisjointFromAdjKeyPrefix pins the property the two keys'
// coexistence in one bucket rests on: no mark key is ever picked up by a scan
// of the document keyspace, and no document key by a scan of the mark
// keyspace. A shared first segment (e.g. "adj.mark.<id>") would break both
// directions silently — the bootstrapper's document scan would read marks as
// nodes named "mark".
func TestAdjMarkKey_DisjointFromAdjKeyPrefix(t *testing.T) {
	const nodeID = "nodeA"
	require.False(t, strings.HasPrefix(AdjMarkKey(nodeID), "adj."))
	require.False(t, strings.HasPrefix(AdjKey(nodeID), "adjmark."))
}

func TestAudit(t *testing.T) {
	tests := []struct {
		lensID, want string
	}{
		{"agreement-summary", "lattice.refractor.audit.agreement-summary"},
		{"occupancy-view", "lattice.refractor.audit.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Audit(tt.lensID))
	}
}

func TestAuditFilter(t *testing.T) {
	assert.Equal(t, "lattice.refractor.audit.>", AuditFilter())
}

func TestPersonalSync(t *testing.T) {
	tests := []struct {
		prefix, actorID, want string
	}{
		{"lattice.sync.user", "op4Nb2mPq6rTwzKxVyP7", "lattice.sync.user.op4Nb2mPq6rTwzKxVyP7"},
		{"lattice.sync.user", "identityA", "lattice.sync.user.identityA"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, PersonalSync(tt.prefix, tt.actorID))
	}
}

func TestPersonalSync_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { PersonalSync("", "identityA") })
	assert.Panics(t, func() { PersonalSync("lattice.sync.user", "") })
	assert.Panics(t, func() { PersonalSync("lattice.sync.user", "actor.id") })
	assert.Panics(t, func() { PersonalSync("lattice.sync.user", "actor*") })
}

func TestCoreKVStream(t *testing.T) {
	assert.Equal(t, "KV_core", CoreKVStream("core"))
	assert.Equal(t, "KV_my-bucket", CoreKVStream("my-bucket"))
}

func TestCoreKVFilter(t *testing.T) {
	assert.Equal(t, "$KV.core.>", CoreKVFilter("core"))
	assert.Equal(t, "$KV.my-bucket.>", CoreKVFilter("my-bucket"))
}

func TestCoreKVVertexFilter(t *testing.T) {
	assert.Equal(t, "$KV.core-kv.vtx.book.>", CoreKVVertexFilter("core-kv", "book"))
}

func TestCoreKVVertexFilter_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { CoreKVVertexFilter("core-kv", "") })
	assert.Panics(t, func() { CoreKVVertexFilter("core-kv", "book.thing") })
	assert.Panics(t, func() { CoreKVVertexFilter("core-kv", "book*") })
	assert.Panics(t, func() { CoreKVVertexFilter("core-kv", "book>") })
}

func TestCoreKVLinkSourceFilter(t *testing.T) {
	assert.Equal(t, "$KV.core-kv.lnk.book.>", CoreKVLinkSourceFilter("core-kv", "book"))
}

func TestCoreKVLinkSourceFilter_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { CoreKVLinkSourceFilter("core-kv", "") })
	assert.Panics(t, func() { CoreKVLinkSourceFilter("core-kv", "a.b") })
}

func TestCoreKVLinkTargetFilter(t *testing.T) {
	assert.Equal(t, "$KV.core-kv.lnk.*.*.*.book.>", CoreKVLinkTargetFilter("core-kv", "book"))
}

func TestCoreKVLinkTargetFilter_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { CoreKVLinkTargetFilter("core-kv", "") })
	assert.Panics(t, func() { CoreKVLinkTargetFilter("core-kv", "a b") })
}

func TestCoreKVNarrowedFilters(t *testing.T) {
	got := CoreKVNarrowedFilters("core-kv", []string{"author", "book"})
	want := []string{
		"$KV.core-kv.vtx.author.>", "$KV.core-kv.lnk.author.>", "$KV.core-kv.lnk.*.*.*.author.>",
		"$KV.core-kv.vtx.book.>", "$KV.core-kv.lnk.book.>", "$KV.core-kv.lnk.*.*.*.book.>",
	}
	assert.Equal(t, want, got, "labels are sorted and each expands to its 3 forms in a stable order")
}

func TestCoreKVNarrowedFilters_DedupesLabels(t *testing.T) {
	got := CoreKVNarrowedFilters("core-kv", []string{"book", "book", "book"})
	want := []string{"$KV.core-kv.vtx.book.>", "$KV.core-kv.lnk.book.>", "$KV.core-kv.lnk.*.*.*.book.>"}
	assert.Equal(t, want, got)
}

func TestCoreKVNarrowedFilters_Empty(t *testing.T) {
	assert.Empty(t, CoreKVNarrowedFilters("core-kv", nil))
}

// isSubsetMatch mirrors nats-server v2.14.0's isSubsetMatchTokenized
// (github.com/nats-io/nats-server/v2@v2.14.0, server/sublist.go:1457 — the
// pinned version per docs/vendors.md), the predicate the server's consumer
// FilterSubjects overlap check (server/consumer.go:876-886, calling
// subjectIsSubsetMatch at :882) uses to reject a filter set: subject is a
// "subset" of test when every concrete subject "subject" can select is also
// selected by "test". Reimplemented here (not imported — nats-server is not
// a production dependency of this module, only nats.go the client is) so
// this test proves CoreKVNarrowedFilters' output against the server's own
// rejection rule rather than merely against string equality.
func isSubsetMatch(subject, test []string) bool {
	for i, t2 := range test {
		if i >= len(subject) {
			return false
		}
		if t2 == ">" {
			return true
		}
		t1 := subject[i]
		if t1 == ">" {
			return false
		}
		if t1 == "*" {
			if t2 != "*" {
				return false
			}
			continue
		}
		if t2 != "*" && t1 != t2 {
			return false
		}
	}
	return len(subject) == len(test)
}

func tokenize(subject string) []string {
	return strings.Split(subject, ".")
}

// TestCoreKVNarrowedFilters_PairwiseNonSubset proves the claim
// CoreKVNarrowedFilters' doc makes: across 2+ labels, no two of the derived
// filter forms are ever a subset of one another (in either direction) —
// exactly the shape nats-server's consumer-creation overlap check would
// reject. Covers same-label pairs (vertex/source/target forms of ONE label)
// and cross-label pairs alike, since the doc claims both are safe.
func TestCoreKVNarrowedFilters_PairwiseNonSubset(t *testing.T) {
	filters := CoreKVNarrowedFilters("core-kv", []string{"book", "author", "leaseapp"})
	require.Len(t, filters, 9, "3 labels x 3 forms")

	for i, a := range filters {
		for j, b := range filters {
			if i == j {
				continue
			}
			if isSubsetMatch(tokenize(a), tokenize(b)) {
				t.Fatalf("filter[%d]=%q is a subset of filter[%d]=%q — nats-server would reject this pair as overlapping",
					i, a, j, b)
			}
		}
	}
}

// TestCoreKVNarrowedFilters_SameLabelFormsNonSubset isolates the same-label
// case: a link from a label to ITSELF matches both that label's source and
// target forms, so the non-overlap proof must hold even when L1 == L2 — the
// general pairwise test already covers this (three of its nine filters share
// "book"), this pins it as an explicit, minimal example.
func TestCoreKVNarrowedFilters_SameLabelFormsNonSubset(t *testing.T) {
	v := tokenize(CoreKVVertexFilter("core-kv", "book"))
	s := tokenize(CoreKVLinkSourceFilter("core-kv", "book"))
	tgt := tokenize(CoreKVLinkTargetFilter("core-kv", "book"))
	pairs := [][2][]string{{v, s}, {v, tgt}, {s, tgt}}
	for _, p := range pairs {
		assert.False(t, isSubsetMatch(p[0], p[1]), "%v must not be a subset of %v", p[0], p[1])
		assert.False(t, isSubsetMatch(p[1], p[0]), "%v must not be a subset of %v", p[1], p[0])
	}
}

func TestCoreKVLinkSourceRelationFilter(t *testing.T) {
	assert.Equal(t, "$KV.core-kv.lnk.provider.*.identifiedBy.>",
		CoreKVLinkSourceRelationFilter("core-kv", "provider", "identifiedBy"))
}

func TestCoreKVLinkTargetRelationFilter(t *testing.T) {
	assert.Equal(t, "$KV.core-kv.lnk.*.*.identifiedBy.identity.>",
		CoreKVLinkTargetRelationFilter("core-kv", "identity", "identifiedBy"))
}

func TestCoreKVLinkRelationFilters_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { CoreKVLinkSourceRelationFilter("core-kv", "pro.vider", "identifiedBy") })
	assert.Panics(t, func() { CoreKVLinkSourceRelationFilter("core-kv", "provider", "identified.By") })
	assert.Panics(t, func() { CoreKVLinkTargetRelationFilter("core-kv", "identity", "") })
	assert.Panics(t, func() { CoreKVLinkTargetRelationFilter("core-kv", "", "identifiedBy") })
}

func TestCoreKVRelationNarrowedFilters(t *testing.T) {
	got := CoreKVRelationNarrowedFilters("core-kv", []string{"provider", "identity"}, []string{"identifiedBy"})
	want := []string{
		"$KV.core-kv.vtx.identity.>",
		"$KV.core-kv.lnk.identity.*.identifiedBy.>",
		"$KV.core-kv.lnk.*.*.identifiedBy.identity.>",
		"$KV.core-kv.vtx.provider.>",
		"$KV.core-kv.lnk.provider.*.identifiedBy.>",
		"$KV.core-kv.lnk.*.*.identifiedBy.provider.>",
	}
	assert.Equal(t, want, got, "labels and relations are sorted; each label expands to vertex + per-relation source/target")
}

// TestCoreKVRelationNarrowedFilters_NoRelationsIsVertexOnly pins the case the
// whole narrowing turns on: a lens with NO relationship pattern subscribes to
// no link form at all, because no link can change its rows. This is a
// narrowing, not a "no data" fallback — a caller without an exhaustive
// relation set must call CoreKVNarrowedFilters instead.
func TestCoreKVRelationNarrowedFilters_NoRelationsIsVertexOnly(t *testing.T) {
	got := CoreKVRelationNarrowedFilters("core-kv", []string{"patient"}, nil)
	assert.Equal(t, []string{"$KV.core-kv.vtx.patient.>"}, got)
}

func TestCoreKVRelationNarrowedFilters_Dedupes(t *testing.T) {
	got := CoreKVRelationNarrowedFilters("core-kv",
		[]string{"book", "book"}, []string{"wrote", "wrote"})
	want := []string{
		"$KV.core-kv.vtx.book.>",
		"$KV.core-kv.lnk.book.*.wrote.>",
		"$KV.core-kv.lnk.*.*.wrote.book.>",
	}
	assert.Equal(t, want, got)
}

func TestCoreKVRelationNarrowedFilters_Empty(t *testing.T) {
	assert.Empty(t, CoreKVRelationNarrowedFilters("core-kv", nil, []string{"wrote"}))
}

// TestCoreKVRelationNarrowedFilters_PairwiseNonSubset proves for the
// relation-narrowed forms exactly what TestCoreKVNarrowedFilters_PairwiseNonSubset
// proves for the relation-blind ones: nats-server's consumer-creation overlap
// check would reject a filter set in which any subject is a subset of another,
// and none of these are, in either direction.
//
// The label set deliberately includes a label that is ALSO a relation name
// ("manages"), and a self-relation (book -wrote-> book), because those are the
// two ways a token could collide across positions and turn one form into a
// subset of another.
func TestCoreKVRelationNarrowedFilters_PairwiseNonSubset(t *testing.T) {
	filters := CoreKVRelationNarrowedFilters("core-kv",
		[]string{"book", "author", "manages"},
		[]string{"wrote", "manages"})
	require.Len(t, filters, 15, "3 labels x (1 vertex + 2 relations x 2 link forms)")

	for i, a := range filters {
		for j, b := range filters {
			if i == j {
				continue
			}
			if isSubsetMatch(tokenize(a), tokenize(b)) {
				t.Fatalf("filter[%d]=%q is a subset of filter[%d]=%q — nats-server would reject this pair as overlapping",
					i, a, j, b)
			}
		}
	}
}
