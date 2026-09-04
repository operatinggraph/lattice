package pipeline

// T3 of personal-lens-delta-publication-design.md §10: the CDC write loop's
// publication scope — the write loop's own rule, the four producers that build
// a scope, the two eligibility conjuncts that refuse one, the five call sites
// the scope has to survive being threaded through, and the plain-lens vector
// that must be untouched by all of it.

import (
	"bytes"
	"context"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// scopedRowFor builds one personal upsert result carrying the provenance a
// scoped write loop judges it on — the shape the engine hands the write loop.
func scopedRowFor(actorID, entityID string, provenance ...string) ruleengine.EvalResult {
	return ruleengine.EvalResult{
		Keys:       map[string]any{adapter.PersonalActorKeyField: actorID, "entityId": entityID},
		Row:        map[string]any{"anchor": substrate.VertexKey("lease", scopedLeaseIDs[0]), "entityId": entityID},
		Provenance: provenance,
	}
}

// --- the write loop's rule ---

// TestWriteResults_ScopeVerticesWritesTheAdmittedAndFramesEverything is the
// headline: a row the scope withholds is unchanged on the device, so nothing is
// written, audited or counted for it — and the frame still names it, which is
// the only thing keeping the client from pruning the copy it holds.
func TestWriteResults_ScopeVerticesWritesTheAdmittedAndFramesEverything(t *testing.T) {
	target := &fakePersonalTarget{}
	p, _ := newPersonalTestPipeline(t, target)
	ctx := context.Background()

	touched := substrate.VertexKey("lease", scopedLeaseIDs[1])
	untouched := substrate.VertexKey("lease", scopedLeaseIDs[2])
	results := []ruleengine.EvalResult{
		scopedRowFor(personalActorA, "lease-a", untouched),
		scopedRowFor(personalActorA, "lease-b", touched),
		scopedRowFor(personalActorA, "lease-c", untouched, touched),
	}
	writesBefore := p.ProjectionWrites()

	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42},
		touched, results, []string{substrate.VertexKey("identity", personalActorA)},
		ScopeVertices([]string{touched}))

	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)
	assert.Equal(t, []string{"lease-b", "lease-c"}, target.upsertKeys(),
		"exactly the rows whose provenance meets the event's vertices are written")
	assert.Equal(t, uint64(2), p.ProjectionWrites()-writesBefore,
		"a withheld row is not even an attempted write")

	frames := target.snapshot()
	require.Len(t, frames, 1, "one authoritative frame per enumerated actor, scope or no scope")
	assert.ElementsMatch(t, []string{"lease-a", "lease-b", "lease-c"}, frameEntityIDs(t, frames[0]),
		"every non-delete result is framed — the withheld row is named or the device prunes it")
}

// TestWriteResults_ScopeVerticesAllDeclinedStillAcksAndFrames is the batch that
// changed nothing. It is the common case once scoping is on, and the one where
// a disposition mistake would be silent: an empty write loop must still be an
// Ack with a frame, never a Nak and never a frameless ack.
func TestWriteResults_ScopeVerticesAllDeclinedStillAcksAndFrames(t *testing.T) {
	target := &fakePersonalTarget{}
	p, _ := newPersonalTestPipeline(t, target)

	untouched := substrate.VertexKey("lease", scopedLeaseIDs[2])
	results := []ruleengine.EvalResult{
		scopedRowFor(personalActorA, "lease-a", untouched),
		scopedRowFor(personalActorA, "lease-b", untouched),
	}

	decision, err := p.writeResults(context.Background(), p.ruleState(), substrate.Message{Sequence: 42},
		"vtx.lease."+scopedLeaseIDs[1], results,
		[]string{substrate.VertexKey("identity", personalActorA)},
		ScopeVertices([]string{substrate.VertexKey("lease", scopedLeaseIDs[1])}))

	require.NoError(t, err)
	assert.Equal(t, substrate.Ack, decision, "a batch that changed nothing is disposed of, not redelivered")
	assert.Empty(t, target.upsertKeys())
	require.Len(t, target.snapshot(), 1)
	assert.ElementsMatch(t, []string{"lease-a", "lease-b"}, frameEntityIDs(t, target.snapshot()[0]),
		"the frame is the whole output, and it still names the actor's complete key set")
}

// TestWriteResults_ScopeNeverWithholdsADelete pins that a retraction is not a
// content change: the Delete arm never consults the scope.
//
// It is asserted against ScopeNone as well as ScopeVertices, because
// ScopeVertices alone cannot tell a Delete arm that skips the check from one
// that makes it — a Delete carries no provenance, which ScopeVertices admits
// anyway. ScopeNone answers no to everything, so it is the value that
// distinguishes them, and the scope owes an answer for every value it can be
// handed whether or not a live caller passes it.
func TestWriteResults_ScopeNeverWithholdsADelete(t *testing.T) {
	untouched := substrate.VertexKey("lease", scopedLeaseIDs[2])
	scopes := map[string]PublishScope{
		"ScopeVertices": ScopeVertices([]string{substrate.VertexKey("lease", scopedLeaseIDs[1])}),
		"ScopeNone":     ScopeNone(),
	}
	for name, scope := range scopes {
		t.Run(name, func(t *testing.T) {
			target := &fakePersonalTarget{}
			p, _ := newPersonalTestPipeline(t, target)
			results := []ruleengine.EvalResult{
				{Delete: true, Keys: map[string]any{adapter.PersonalActorKeyField: personalActorA, "entityId": "lease-gone"}},
				scopedRowFor(personalActorA, "lease-a", untouched),
			}

			_, err := p.writeResults(context.Background(), p.ruleState(), substrate.Message{Sequence: 42},
				"vtx.lease."+scopedLeaseIDs[1], results,
				[]string{substrate.VertexKey("identity", personalActorA)}, scope)

			require.NoError(t, err)
			assert.Empty(t, target.upsertKeys(), "the upsert was withheld")
			require.Len(t, target.deletes, 1, "the Delete was not")
		})
	}
}

// TestWriteResults_FrameAdvancesTheFreshnessClock covers the write loop's own
// recordProjected: the clock counts what the lens PUBLISHED, and an event whose
// rows were all unchanged still delivers a frame. This is the live event path,
// so it is exactly the clock LensProjectionStalled reads — a lens still framing
// its actors is a lens still projecting.
func TestWriteResults_FrameAdvancesTheFreshnessClock(t *testing.T) {
	target := &fakePersonalTarget{}
	p, _ := newPersonalTestPipeline(t, target)
	require.True(t, p.Progress().LastProjectedAt.IsZero(), "nothing has been published yet")

	untouched := substrate.VertexKey("lease", scopedLeaseIDs[2])
	_, err := p.writeResults(context.Background(), p.ruleState(), substrate.Message{Sequence: 42},
		"vtx.lease."+scopedLeaseIDs[1],
		[]ruleengine.EvalResult{scopedRowFor(personalActorA, "lease-a", untouched)},
		[]string{substrate.VertexKey("identity", personalActorA)},
		ScopeVertices([]string{substrate.VertexKey("lease", scopedLeaseIDs[1])}))

	require.NoError(t, err)
	require.Empty(t, target.upsertKeys(), "no row landed, so only the frame can have stamped the clock")
	assert.False(t, p.Progress().LastProjectedAt.IsZero())
}

// --- the non-personal target is untouched ---

// TestWriteResults_NonKeySetPublisherIgnoresTheScope runs one plain-lens vector
// twice — once under ScopeAll, once under a scope that would withhold every row
// — and compares the adapter's whole call list. A plain target publishes no
// keyset frame, so a row it withheld would be a row nothing ever names again;
// the scope must not reach it at all.
func TestWriteResults_NonKeySetPublisherIgnoresTheScope(t *testing.T) {
	untouched := substrate.VertexKey("lease", scopedLeaseIDs[2])
	results := func() []ruleengine.EvalResult {
		return []ruleengine.EvalResult{
			{Keys: map[string]any{"key": "row.a"}, Row: map[string]any{"v": 1.0}, Provenance: []string{untouched}},
			{Keys: map[string]any{"key": "row.b"}, Row: map[string]any{"v": 2.0}, Provenance: []string{untouched}},
			{Delete: true, Keys: map[string]any{"key": "row.c"}},
		}
	}
	withholding := ScopeVertices([]string{substrate.VertexKey("lease", scopedLeaseIDs[1])})
	require.False(t, withholding.Admits(results()[0]),
		"the vector is only meaningful if this scope really would withhold on a personal target")

	run := func(scope PublishScope) *recordingAdapter {
		adpt := &recordingAdapter{}
		p := &Pipeline{ruleID: "plain-scope-vector", adapterName: "nats_kv", adpt: adpt}
		decision, err := p.writeResults(context.Background(), p.ruleState(), substrate.Message{Sequence: 42},
			"vtx.lease."+scopedLeaseIDs[1], results(), nil, scope)
		require.NoError(t, err)
		require.Equal(t, substrate.Ack, decision)
		return adpt
	}

	all := run(ScopeAll())
	scoped := run(withholding)

	assert.Equal(t, all.upserts, scoped.upserts, "the plain target's upserts must be identical")
	assert.Equal(t, all.deletes, scoped.deletes, "and its deletes")
	assert.Len(t, all.upserts, 2, "the vector really did write rows in both runs")
}

// --- the four scope producers ---

// newScopeProducerFixture is newScopedPersonalFixture with the compiled rule's
// own pattern graph published, which is what the eligibility conjunct reads.
// Without it the lens carries a never-derived graph and every event is refused
// a scope — the fixture would then pin the refusal rather than the producer.
func newScopeProducerFixture(t *testing.T) (*Pipeline, *fakePersonalTarget) {
	t.Helper()
	p, target := newScopedPersonalFixture(t)
	fullCR, ok := p.fullCR.(*full.CompiledRule)
	require.True(t, ok)
	p.anchorHops = fullCR.AnchorHopIndex()
	require.NoError(t, nil)
	require.Empty(t, p.ruleState().publishScopeRefusal(),
		"the fixture lens must be scopeable, or every vector below asserts the refusal instead")
	return p, target
}

// TestPublishScopeProducers pins each CDC arm's scope against ITS OWN SOURCE —
// the exact key set the event names — rather than merely against "not All". A
// producer that handed the wrong vertices would still read as scoped.
func TestPublishScopeProducers(t *testing.T) {
	actorKey := substrate.VertexKey("identity", personalActorA)
	leaseKey := substrate.VertexKey("lease", scopedLeaseIDs[0])
	ctx := context.Background()

	t.Run("the vertex arm names the event vertex", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		_, _, scope, err := p.evaluateForEntry(ctx, p.ruleState(), ruleengine.NodeEntry{
			CoreKVKey: actorKey, NodeLabel: "identity",
			Properties: map[string]any{"key": actorKey, "lastModifiedAt": "2026-08-14T00:00:00Z"},
		})
		require.NoError(t, err)
		assert.Equal(t, ScopeVertices([]string{actorKey}).String(), scope.String())
	})

	t.Run("the vertex arm on a NON-actor vertex names that vertex", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		_, _, scope, err := p.evaluateForEntry(ctx, p.ruleState(), ruleengine.NodeEntry{
			CoreKVKey: leaseKey, NodeLabel: "lease",
			Properties: map[string]any{"key": leaseKey, "lastModifiedAt": "2026-08-14T00:00:00Z"},
		})
		require.NoError(t, err)
		assert.Equal(t, ScopeVertices([]string{leaseKey}).String(), scope.String())
	})

	t.Run("the aspect arm names the aspect's PARENT vertex", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		_, _, scope, err := p.evaluateAspectFanOut(ctx, p.ruleState(), leaseKey+".detail")
		require.NoError(t, err)
		assert.Equal(t, ScopeVertices([]string{leaseKey}).String(), scope.String(),
			"an aspect key folds to its parent — the granularity provenance is recorded at")
	})

	t.Run("the link arm names BOTH endpoints", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		linkKey := substrate.LinkKey("identity", personalActorA, "holds", "lease", scopedLeaseIDs[0])
		_, _, scope, err := p.evaluateLinkFanOut(ctx, p.ruleState(), linkKey, false)
		require.NoError(t, err)
		assert.Equal(t, ScopeVertices([]string{actorKey, leaseKey}).String(), scope.String(),
			"a link create binds both endpoints; a link tombstone leaves the near one bound on the row that lost its far side")
	})

	t.Run("the actor's own path admits every row of that actor", func(t *testing.T) {
		// The no-peer case of §4.2: the actor is bound in every binding, so a
		// scope naming its vertex withholds nothing — byte-identical to today.
		p, _ := newScopeProducerFixture(t)
		scope := p.eventPublishScope(p.ruleState(), []string{actorKey})
		for _, entityID := range []string{"lease-a", "lease-b", "lease-c"} {
			assert.True(t, scope.Admits(scopedRowFor(personalActorA, entityID, actorKey)),
				"every row of this actor binds it")
		}
	})

	t.Run("with peers, the actor's own path admits only the rows binding it", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		p.SetActorPeerAnchorMode(PeerAnchorModeOn)
		scope := p.eventPublishScope(p.ruleState(), []string{actorKey})
		peerKey := substrate.VertexKey("identity", personalActorB)
		assert.True(t, scope.Admits(scopedRowFor(personalActorB, "peer-row", actorKey)),
			"a peer's row that read the event identity moved")
		assert.False(t, scope.Admits(scopedRowFor(personalActorB, "other-row", peerKey)),
			"a peer's row that did not read it is unchanged — and its frame still names it")
	})
}

// --- the two eligibility conjuncts ---

// TestPublishScopeRefusal walks the conjuncts on the ruleState predicate the
// producers actually call, so a conjunct added there is pinned here for free.
func TestPublishScopeRefusal(t *testing.T) {
	// A derived single-walk graph whose anchor position exists: the shape a
	// scopeable lens carries.
	seeded := full.HopIndex{Labels: []string{"identity", "lease"}, Anchor: 0}
	scanned := full.HopIndex{Labels: []string{"lease"}, Anchor: -1}

	cases := []struct {
		name string
		rs   ruleState
		want string
	}{
		{"a clock-free, point-seeded single-walk lens is scopeable",
			ruleState{anchorHops: seeded}, ""},
		{"a row referencing the wall clock is refused",
			ruleState{anchorHops: seeded, personalClockRefusal: "the row references $now"}, PublishScopeClockRefusal},
		{"a scan-seeded anchor is refused",
			ruleState{anchorHops: scanned}, PublishScopeScanSeededAnchor},
		// The sentinel test alone would pass a never-derived index: Anchor's
		// zero value is 0, a real position number.
		{"a never-derived pattern graph is refused",
			ruleState{}, PublishScopeScanSeededAnchor},
		{"a multi-walk lens is scopeable when EVERY branch is point-seeded",
			ruleState{anchorHopsPerBranch: []full.HopIndex{seeded, seeded}}, ""},
		{"one scan-seeded branch refuses the whole lens",
			ruleState{anchorHopsPerBranch: []full.HopIndex{seeded, scanned}}, PublishScopeScanSeededAnchor},
		{"a refused branch set is no answer at all",
			ruleState{anchorHops: seeded, anchorHopsPerBranch: []full.HopIndex{seeded}, anchorHopsPerBranchRefusal: "unreadable"},
			PublishScopeBranchSetRefused},
		{"the clock conjunct is asked first, and refuses a point-seeded lens too",
			ruleState{anchorHopsPerBranch: []full.HopIndex{seeded, seeded}, personalClockRefusal: "the row references $projectedAt"},
			PublishScopeClockRefusal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.rs.publishScopeRefusal())

			scope := tc.rs.publishScopeFor([]string{substrate.VertexKey("lease", scopedLeaseIDs[0])})
			if tc.want == "" {
				assert.Equal(t, ScopeKindVertices, scope.Kind())
				return
			}
			assert.Equal(t, ScopeKindAll, scope.Kind(),
				"every refusal widens to ScopeAll — bytes, never a withheld row")
		})
	}
}

// TestEventPublishScope_RefusedLensPublishesWhole is the same two conjuncts on
// the pipeline-level producer, where the refusal also has to survive the
// personal-target check in front of it.
func TestEventPublishScope_RefusedLensPublishesWhole(t *testing.T) {
	actorKey := substrate.VertexKey("identity", personalActorA)

	t.Run("a scopeable personal lens scopes", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		assert.Equal(t, ScopeKindVertices, p.eventPublishScope(p.ruleState(), []string{actorKey}).Kind())
	})

	t.Run("a clock-refused personal lens publishes whole", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		rs := p.ruleState()
		rs.personalClockRefusal = "the row references $now"
		assert.Equal(t, ScopeKindAll, p.eventPublishScope(rs, []string{actorKey}).Kind())
	})

	t.Run("a scan-seeded personal lens publishes whole", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		rs := p.ruleState()
		rs.anchorHops = full.HopIndex{Labels: []string{"lease"}, Anchor: -1}
		assert.Equal(t, ScopeKindAll, p.eventPublishScope(rs, []string{actorKey}).Kind())
	})

	t.Run("a non-personal target is never scoped, however eligible the rule", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		p.adpt = &recordingAdapter{}
		require.Empty(t, p.ruleState().publishScopeRefusal(), "the RULE still clears both conjuncts")
		assert.Equal(t, ScopeKindAll, p.eventPublishScope(p.ruleState(), []string{actorKey}).Kind(),
			"the target decides, not the rule: a lens that publishes no frame can never withhold a row")
	})
}

// --- the five writeResults call sites ---

// captureLogs redirects the default logger into a buffer for the duration of
// one test and returns the reader.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// TestPublishScopeReachesEveryWriteResultsCallSite is the plumbing vector.
//
// A scope threaded through five signatures is exactly the shape that passes its
// producer test and its write-loop test while some arm quietly hands
// writeResults ScopeAll — a failure that is invisible on the wire, because
// ScopeAll is today's publication. So each path is driven end to end through
// the real dispatch and asserted on what writeResults itself reports for the
// event: the scope it was handed, by value.
//
// The three personal paths carry the event's own vertices. The two plain paths
// are on a plain lens, whose target publishes no frame and is therefore never
// scoped at all — an unscoped line is that arm's assertion, not an omission.
func TestPublishScopeReachesEveryWriteResultsCallSite(t *testing.T) {
	actorKey := substrate.VertexKey("identity", personalActorA)
	leaseKey := substrate.VertexKey("lease", scopedLeaseIDs[0])
	linkKey := substrate.LinkKey("identity", personalActorA, "holds", "lease", scopedLeaseIDs[0])

	personalSites := []struct {
		name string
		key  string
		want PublishScope
	}{
		{"handle, vertex event", actorKey, ScopeVertices([]string{actorKey})},
		{"evalLinkFanOut", linkKey, ScopeVertices([]string{actorKey, leaseKey})},
		{"evalAspectFanOut", leaseKey + ".detail", ScopeVertices([]string{leaseKey})},
	}
	for _, site := range personalSites {
		t.Run(site.name, func(t *testing.T) {
			p, _ := newScopeProducerFixture(t)
			logs := captureLogs(t)

			decision, err := p.handle(context.Background(), substrate.Message{
				Subject:  "$KV." + p.coreKVBucket + "." + site.key,
				Sequence: 42,
				Body:     []byte(`{"key":"` + site.key + `","isDeleted":false,"lastModifiedAt":"2026-08-14T00:00:00Z"}`),
			})

			require.NoError(t, err)
			require.Equal(t, substrate.Ack, decision)
			assert.Contains(t, logs(), "publishScope="+site.want.String(),
				"the arm's own scope must be what reaches the write loop, not ScopeAll")
		})
	}

	plainSites := []struct {
		name string
		key  string
	}{
		{"evalPlainAspectReprojection", "vtx.unit." + scopedLeaseIDs[0] + ".listing"},
		{"evalPlainLinkReprojection", substrate.LinkKey("unit", scopedLeaseIDs[0], "inBuilding", "building", scopedLeaseIDs[1])},
	}
	for _, site := range plainSites {
		t.Run(site.name, func(t *testing.T) {
			p, _ := newPlainScopeVectorPipeline(t)
			logs := captureLogs(t)

			decision, err := p.handle(context.Background(), substrate.Message{
				Subject:  "$KV." + p.coreKVBucket + "." + site.key,
				Sequence: 42,
				Body:     []byte(`{"key":"` + site.key + `","isDeleted":false,"lastModifiedAt":"2026-08-14T00:00:00Z"}`),
			})

			require.NoError(t, err)
			require.Equal(t, substrate.Ack, decision)
			require.Contains(t, logs(), "pipeline: processed", "the arm must have reached the write loop")
			assert.NotContains(t, logs(), "publishScope=",
				"a plain target publishes no frame, so no row of it is ever scoped")
		})
	}
}

// newPlainScopeVectorPipeline is a plain lens over the same KV fixture shape —
// no enumerator, no envelope, a target that publishes no keyset frame.
func newPlainScopeVectorPipeline(t *testing.T) (*Pipeline, *recordingAdapter) {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (u:unit)
RETURN u.key AS key, u.name AS name
`)
	require.NoError(t, err)
	adpt := &recordingAdapter{}
	p := &Pipeline{
		ruleID:      "plain-scope-vector-rule",
		adapterName: "nats_kv",
		coreKV:      coreKV,
		adjKV:       adjKV,
		engineKind:  ruleengine.EngineFull,
		fullEngine:  eng,
		fullCR:      cr,
		adpt:        adpt,
	}
	p.plainReprojectAll = true
	p.plainRelationsExhaustive = false
	return p, adpt
}

// --- the branch-merge provenance union ---

// TestMergedProvenance covers §4.1 site 7 — including the case the adversarial
// pass found, where a branch that produced NO row for a key is the only thing
// that read the vertex whose change nulled a walk-owned column.
func TestMergedProvenance(t *testing.T) {
	vtxA := substrate.VertexKey("lease", scopedLeaseIDs[0])
	vtxB := substrate.VertexKey("role", scopedLeaseIDs[1])
	vtxC := substrate.VertexKey("identity", scopedLeaseIDs[2])

	row := func(key string, provenance ...string) ruleengine.ProjectionResult {
		return ruleengine.ProjectionResult{
			Key:        map[string]any{"entityId": key},
			Values:     map[string]any{"entityId": key},
			Provenance: provenance,
		}
	}

	t.Run("a key only one branch produced still takes the absent branch's read set", func(t *testing.T) {
		merged, err := mergeBranchRows(
			[][]ruleengine.ProjectionResult{{row("k1", vtxA)}, {}},
			[][]string{{vtxA}, {vtxB, vtxC}},
			nil)
		require.NoError(t, err)
		require.Len(t, merged, 1)
		assert.ElementsMatch(t, []string{vtxA, vtxB, vtxC}, merged[0].Provenance,
			"the branch that produced nothing for this key still READ the vertices that could make it produce one")
	})

	t.Run("two branches sharing a key union their own provenance", func(t *testing.T) {
		merged, err := mergeBranchRows(
			[][]ruleengine.ProjectionResult{{row("k1", vtxA)}, {row("k1", vtxB)}},
			[][]string{{vtxA, vtxC}, {vtxB, vtxC}},
			nil)
		require.NoError(t, err)
		require.Len(t, merged, 1)
		assert.ElementsMatch(t, []string{vtxA, vtxB}, merged[0].Provenance,
			"every branch spoke for itself, so no read set is substituted for one")
	})

	t.Run("a branch that recorded nothing contributes its whole read set", func(t *testing.T) {
		merged, err := mergeBranchRows(
			[][]ruleengine.ProjectionResult{{row("k1")}, {row("k1", vtxB)}},
			[][]string{{vtxA, vtxC}, {vtxB}},
			nil)
		require.NoError(t, err)
		require.Len(t, merged, 1)
		assert.ElementsMatch(t, []string{vtxA, vtxB, vtxC}, merged[0].Provenance,
			"a coarser answer over-publishes; reading 'recorded nothing' as 'read nothing' would withhold")
	})

	t.Run("nothing recorded anywhere reads as no provenance at all", func(t *testing.T) {
		merged, err := mergeBranchRows(
			[][]ruleengine.ProjectionResult{{row("k1")}, {row("k1")}},
			[][]string{nil, nil},
			nil)
		require.NoError(t, err)
		require.Len(t, merged, 1)
		assert.Nil(t, merged[0].Provenance,
			"nil is admitted by ScopeVertices; an empty non-nil set would withhold every row")
	})
}

// TestBranchReadSet pins the fold, with each key shape reachable through EXACTLY
// ONE path: an aspect through its parent, a link through both endpoints, an
// adjacency node id through the vertex key the same evaluation fetched. A
// fixture where two shapes named the same vertex would pass with either fold
// deleted.
func TestBranchReadSet(t *testing.T) {
	actorKey := substrate.VertexKey("identity", scopedLeaseIDs[0])
	leaseKey := substrate.VertexKey("lease", scopedLeaseIDs[1])
	unitKey := substrate.VertexKey("unit", scopedLeaseIDs[2])
	roleID := "Qrs7RvwKx3TmZpq2Hc9M"
	roleKey := substrate.VertexKey("role", roleID)
	// An adjacency node this evaluation read but fetched no vertex body for.
	orphanID := "Trs7RvwKx3TmZpq2Hc9N"

	got := branchReadSet(ruleengine.EvalFootprint{
		NodeRevisions: map[string]uint64{
			// Reached only as an aspect's parent.
			unitKey + ".listing": 3,
			// Reached only as a link's two endpoints.
			substrate.LinkKey("identity", scopedLeaseIDs[0], "holds", "lease", scopedLeaseIDs[1]): 1,
			// Reached as itself, and named again below as an adjacency id.
			roleKey: 0,
		},
		EdgeRevisions: map[string]uint64{roleID: 9},
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{orphanID: {}},
	})

	assert.ElementsMatch(t, []string{actorKey, leaseKey, unitKey, roleKey, orphanID}, got,
		"an aspect folds to its parent, a link to both endpoints, an adjacency id to the vertex that was fetched — and one nothing fetched stays itself")
}

// --- the hydrate race ---

// TestHydrateRace_TheDeviceEndsWithEveryRow walks the three interleavings of
// §4.6 against a scripted in-flight event.
//
// Scoping is what makes this a race at all: before it, the live event
// republished the whole actor and masked every ordering it could lose. The
// invariant each vector asserts is the same one — after both publishers have
// finished, the device has been sent every row of the actor at or above the
// frame that describes it.
func TestHydrateRace_TheDeviceEndsWithEveryRow(t *testing.T) {
	actorKey := substrate.VertexKey("identity", personalActorA)
	leaseKey := substrate.VertexKey("lease", scopedLeaseIDs[1])
	untouched := substrate.VertexKey("lease", scopedLeaseIDs[2])
	liveResults := func() []ruleengine.EvalResult {
		return []ruleengine.EvalResult{
			scopedRowFor(personalActorA, "lease-a", untouched),
			scopedRowFor(personalActorA, "lease-b", leaseKey),
			scopedRowFor(personalActorA, "lease-c", untouched),
		}
	}
	liveScope := ScopeVertices([]string{leaseKey})

	t.Run("an event applied BEFORE the capture leaves the hydrate above it", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		ctx := context.Background()

		leave := p.enterHandling(9)
		_, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 9},
			leaseKey, liveResults(), []string{actorKey}, liveScope)
		require.NoError(t, err)
		p.recordAppliedSeq(9)
		leave()

		high, err := p.Hydrate(ctx, personalActorA)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, high, uint64(9),
			"the hydrate's frame must not sit below the live frame the device already applied")
	})

	t.Run("an event IN FLIGHT at the capture makes the hydrate wait for it", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		ctx := context.Background()
		require.Equal(t, uint64(1), p.Progress().LastAppliedSeq,
			"the fixture's cursor must start below the in-flight event, or the capture cannot be observed to move")

		leave := p.enterHandling(11)
		hydrated := make(chan uint64, 1)
		go func() {
			high, herr := p.Hydrate(ctx, personalActorA)
			require.NoError(t, herr)
			hydrated <- high
		}()

		// The hydrate marks the slot BEFORE it captures, so this barrier puts
		// it at or inside the wait. From here it cannot reach the capture while
		// the handler is still inside the event — which is the whole claim, and
		// what makes the cursor advance below observable in the result.
		waitForHydrateMark(t, p, personalActorA)
		p.recordAppliedSeq(11)
		leave()

		assert.GreaterOrEqual(t, <-hydrated, uint64(11),
			"the capture must cover the event that was in flight when the hydrate arrived")
	})

	t.Run("an event that STARTS after the capture publishes the actor whole", func(t *testing.T) {
		p, target := newScopeProducerFixture(t)
		ctx := context.Background()
		p.recordAppliedSeq(3)

		// The hydrate holds the slot for the duration of the write below.
		unlock, err := p.lockPersonalActor(ctx, personalActorA)
		require.NoError(t, err)
		unmark := p.markHydrating(personalActorA)

		require.True(t, p.hydrateInFlight(personalActorA))
		_, err = p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 12},
			leaseKey, liveResults(), []string{actorKey}, liveScope)
		require.NoError(t, err)

		unmark()
		unlock()

		assert.ElementsMatch(t, []string{"lease-a", "lease-b", "lease-c"}, target.upsertKeys(),
			"a scoped publish here would advance the device's frame past the hydrate's and cost it every row it does not already hold")
	})

	t.Run("with no hydrate in flight the same event publishes only what it touched", func(t *testing.T) {
		// The positive vector for the guard above: without it the assertion
		// there would hold for a write loop that never scopes anything.
		p, target := newScopeProducerFixture(t)
		p.recordAppliedSeq(3)

		require.False(t, p.hydrateInFlight(personalActorA))
		_, err := p.writeResults(context.Background(), p.ruleState(), substrate.Message{Sequence: 12},
			leaseKey, liveResults(), []string{actorKey}, liveScope)
		require.NoError(t, err)

		assert.Equal(t, []string{"lease-b"}, target.upsertKeys())
	})

	t.Run("the guard is per actor", func(t *testing.T) {
		p, target := newScopeProducerFixture(t)
		p.recordAppliedSeq(3)
		unlock, err := p.lockPersonalActor(context.Background(), personalActorB)
		require.NoError(t, err)
		unmark := p.markHydrating(personalActorB)

		_, err = p.writeResults(context.Background(), p.ruleState(), substrate.Message{Sequence: 12},
			leaseKey, liveResults(), []string{actorKey}, liveScope)
		require.NoError(t, err)

		unmark()
		unlock()
		assert.Equal(t, []string{"lease-b"}, target.upsertKeys(),
			"a hydrate of a DIFFERENT actor must not widen this one's publication")
	})
}

// TestAwaitHandlerLeft_ReleasesOnANakedEvent pins the predicate the wait is
// written against: the handler LEAVING, not the applied cursor reaching the
// sequence. A Naked event never advances the cursor, and a hydrate waiting on
// the cursor would burn its whole RPC deadline on one.
func TestAwaitHandlerLeft_ReleasesOnANakedEvent(t *testing.T) {
	p := &Pipeline{ruleID: "await-vector"}
	ctx := context.Background()

	require.NoError(t, p.awaitHandlerLeft(ctx, 0), "nothing in flight returns at once")

	leave := p.enterHandling(5)
	released := make(chan struct{})
	go func() {
		require.NoError(t, p.awaitHandlerLeft(ctx, 5))
		close(released)
	}()
	select {
	case <-released:
		t.Fatal("the wait returned while the handler was still inside the event")
	default:
	}
	leave() // a Nak: the cursor never moves
	<-released
}

// TestAwaitHandlerLeft_AbandonsOnContext keeps the wait bounded by the caller's
// own deadline — an attach RPC must report a timeout, never block behind a
// stalled handler.
func TestAwaitHandlerLeft_AbandonsOnContext(t *testing.T) {
	p := &Pipeline{ruleID: "await-vector"}
	leave := p.enterHandling(5)
	defer leave()

	ctx, cancel := context.WithCancel(context.Background())
	failed := make(chan error, 1)
	go func() { failed <- p.awaitHandlerLeft(ctx, 5) }()
	cancel()

	require.ErrorIs(t, <-failed, context.Canceled)
}

// TestHydrate_AbandonsWhenTheInFlightEventOutlastsTheRPC is the same bound at
// the Hydrate seam, where the caller sees it.
func TestHydrate_AbandonsWhenTheInFlightEventOutlastsTheRPC(t *testing.T) {
	p, _ := newScopeProducerFixture(t)
	leave := p.enterHandling(7)
	defer leave()

	ctx, cancel := context.WithCancel(context.Background())
	failed := make(chan error, 1)
	go func() {
		_, err := p.Hydrate(ctx, personalActorA)
		failed <- err
	}()
	cancel()

	err := <-failed
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "in flight") || strings.Contains(err.Error(), "publish slot"),
		"the hydrate must report which wait it abandoned, got %v", err)
}

// waitForHydrateMark blocks until a Hydrate holds actorID's publish slot on p.
// It polls the condition rather than waiting a duration: the mark is set inside
// another goroutine, and what a caller needs is the fact, not a delay long
// enough to make the fact likely.
func waitForHydrateMark(t *testing.T, p *Pipeline, actorID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !p.hydrateInFlight(actorID) {
		if time.Now().After(deadline) {
			t.Fatalf("no hydrate took %q's publish slot", actorID)
		}
		runtime.Gosched()
	}
}
