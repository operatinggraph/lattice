package pipeline

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// recordingAdapter captures every write the reconciler makes and serves a
// canned stored row, so a test can assert both the write decision and the
// ordering token without a live target store.
type recordingAdapter struct {
	stored   map[string]any
	present  bool
	getErr   error
	writeErr error
	// unguarded makes the adapter ignore the ordering token, the way a target
	// with no projection guard does. The zero value is guarded.
	unguarded bool
	upserts   []recordedWrite
	deletes   []recordedWrite
	getCalled int
	// onGetRow, when set, runs at the start of every read-back. It is the
	// interleaving hook: a case that needs state to move BETWEEN a Reproject's
	// token capture and its write phase drives it from here, in the one place
	// the loop is guaranteed to pass through per result.
	onGetRow func()
}

type recordedWrite struct {
	keys map[string]any
	row  map[string]any
	seq  uint64
}

func (a *recordingAdapter) Upsert(_ context.Context, keys, row map[string]any, seq uint64) error {
	a.upserts = append(a.upserts, recordedWrite{keys: keys, row: row, seq: seq})
	return a.writeErr
}

func (a *recordingAdapter) Delete(_ context.Context, keys map[string]any, seq uint64) error {
	a.deletes = append(a.deletes, recordedWrite{keys: keys, seq: seq})
	return a.writeErr
}

func (a *recordingAdapter) Probe(context.Context) error { return nil }
func (a *recordingAdapter) Close() error                { return nil }
func (a *recordingAdapter) Guarded() bool               { return !a.unguarded }

func (a *recordingAdapter) GetRow(context.Context, map[string]any) (map[string]any, bool, error) {
	if a.onGetRow != nil {
		a.onGetRow()
	}
	a.getCalled++
	if a.getErr != nil {
		return nil, false, a.getErr
	}
	return a.stored, a.present, nil
}

// newReprojectPipeline builds a pipeline whose reprojectActors resolves the
// missing-actor branch (empty Core KV), with an envelope installed so the
// lens reads as actor-aggregate.
func newReprojectPipeline(t *testing.T, adpt *recordingAdapter) *Pipeline {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	p := &Pipeline{
		ruleID:          "reproject-rule",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      &full.Engine{},
		fullCR:          &full.CompiledRule{},
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
		adpt:            adpt,
	}
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	return p
}

const reprojectActor = "vtx.identity.Trep1JdentityAaaaaaa"

func TestReproject_RefusesNonActorAggregateLens(t *testing.T) {
	adpt := &recordingAdapter{}
	p := newReprojectPipeline(t, adpt)
	// A plain lens has no envelope wrapper: per-actor reconciliation is not
	// defined for it and must be refused structurally, not attempted.
	p.SetEnvelopeFn(nil)

	_, err := p.Reproject(context.Background(), reprojectActor)
	require.ErrorIs(t, err, ErrNotActorAggregate)
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)
}

func TestReproject_RequiresActorKey(t *testing.T) {
	p := newReprojectPipeline(t, &recordingAdapter{})
	_, err := p.Reproject(context.Background(), "")
	require.Error(t, err)
}

// TestReproject_RefusesNonVertexActorKey proves a malformed actorKey (an
// aspect or link key, e.g. passed by mistake) is refused rather than
// evaluated as an anchor that resolves to zero rows and reads back as a
// clean, converged no-op — which would silently drop whatever this call was
// actually meant to satisfy.
func TestReproject_RefusesNonVertexActorKey(t *testing.T) {
	adpt := &recordingAdapter{}
	p := newReprojectPipeline(t, adpt)
	_, err := p.Reproject(context.Background(), "vtx.identity.x.someAspect")
	require.Error(t, err)
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)
}

func TestReproject_MissingActor_DeletesRow(t *testing.T) {
	// Actor absent from Core KV and a row still stored → the reconciler
	// retracts it, carrying the captured sequence as the ordering token.
	adpt := &recordingAdapter{stored: map[string]any{"key": "cap.identity.x"}, present: true}
	p := newReprojectPipeline(t, adpt)
	p.recordAppliedSeq(4242)

	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)
	require.True(t, res.Deleted)
	require.True(t, res.Wrote)
	require.False(t, res.Converged)
	require.Len(t, adpt.deletes, 1)
	require.Equal(t, uint64(4242), adpt.deletes[0].seq)
	require.Equal(t, uint64(4242), res.ProjectionSeq)
}

func TestReproject_MissingActor_AlreadyAbsent_WritesNothing(t *testing.T) {
	// The row is already gone: a converged actor must cost zero writes, so
	// the sweep in Fire 1b stays churn-free at rest.
	adpt := &recordingAdapter{present: false}
	p := newReprojectPipeline(t, adpt)

	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)
	require.True(t, res.Converged)
	require.False(t, res.Wrote)
	require.Empty(t, adpt.deletes)
	require.Empty(t, adpt.upserts)
}

func TestReproject_TokenIsCapturedBeforeEvaluation(t *testing.T) {
	// The ordering token is the pipeline's forward progress at entry. It must
	// never be MaxInt64: that stamp is the shred nullifier's terminal
	// authority and would freeze the key against all future CDC.
	adpt := &recordingAdapter{present: true}
	p := newReprojectPipeline(t, adpt)
	p.recordAppliedSeq(77)

	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)
	require.Equal(t, uint64(77), res.ProjectionSeq)
	require.NotEqual(t, uint64(1<<63-1), res.ProjectionSeq)
}

func TestReproject_ReadErrorSurfaces(t *testing.T) {
	// A target-store read failure must not be mistaken for "row absent" —
	// that would turn a transient outage into a spurious heal.
	adpt := &recordingAdapter{getErr: errors.New("boom"), present: true}
	p := newReprojectPipeline(t, adpt)
	// A retraction over a stored row needs a token that outranks its watermark;
	// without one the write is refused before the read error is even reached.
	p.recordAppliedSeq(55)

	// The delete branch tolerates a read error and falls through to the
	// delete; assert it still writes rather than silently converging.
	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)
	require.True(t, res.Wrote)
}

func TestRowsEquivalent_IgnoresVolatileProjectedAt(t *testing.T) {
	// projectedAt is restamped on every evaluation. Comparing it would make
	// every reconciliation look divergent and defeat skip-if-identical.
	stored := map[string]any{
		"key":         "cap.roles.identity.abc",
		"roles":       []any{"vtx.role.a"},
		"projectedAt": "2026-07-21T00:00:00Z",
	}
	computed := map[string]any{
		"key":         "cap.roles.identity.abc",
		"roles":       []any{"vtx.role.a"},
		"projectedAt": "2026-07-22T09:30:00Z",
	}
	require.True(t, rowsEquivalent(stored, computed))
}

func TestRowsEquivalent_NormalizesJSONRoundTripTypes(t *testing.T) {
	// The stored row has been through JSON (numbers decode as float64); the
	// computed row still carries the engine's Go types. Byte-identical
	// documents must compare equal despite that.
	stored := map[string]any{"key": "cap.roles.identity.abc", "count": float64(3)}
	computed := map[string]any{"key": "cap.roles.identity.abc", "count": 3}
	require.True(t, rowsEquivalent(stored, computed))
}

func TestRowsEquivalent_DetectsRealDivergence(t *testing.T) {
	base := map[string]any{"key": "cap.roles.identity.abc", "roles": []any{"vtx.role.a"}}

	t.Run("changed grant", func(t *testing.T) {
		other := map[string]any{"key": "cap.roles.identity.abc", "roles": []any{"vtx.role.b"}}
		require.False(t, rowsEquivalent(base, other))
	})
	t.Run("revoked grant", func(t *testing.T) {
		other := map[string]any{"key": "cap.roles.identity.abc", "roles": []any{}}
		require.False(t, rowsEquivalent(base, other))
	})
	t.Run("added field", func(t *testing.T) {
		other := map[string]any{"key": "cap.roles.identity.abc", "roles": []any{"vtx.role.a"}, "lanes": []any{"x"}}
		require.False(t, rowsEquivalent(base, other))
	})
	t.Run("source revision moved", func(t *testing.T) {
		// projectedFromRevisions is NOT volatile: a source-revision change is
		// genuine divergence and must still trigger a heal.
		a := map[string]any{"key": "k", "projectedFromRevisions": map[string]any{"vtx.a": float64(1)}}
		b := map[string]any{"key": "k", "projectedFromRevisions": map[string]any{"vtx.a": float64(2)}}
		require.False(t, rowsEquivalent(a, b))
	})
}

func TestRowsEquivalent_EmptyAndNil(t *testing.T) {
	require.True(t, rowsEquivalent(map[string]any{}, map[string]any{}))
	require.True(t, rowsEquivalent(nil, map[string]any{}))
	require.False(t, rowsEquivalent(nil, map[string]any{"key": "x"}))
}

// TestRowsComparable_SeparatesUnrenderableFromDifferent pins the one distinction
// rowsEquivalent deliberately collapses. For a REPAIR path, a row that cannot be
// rendered is a row to rewrite, so folding it into "differs" is right. For the
// divergence audit, which writes nothing, it is the difference between a proven
// divergence and a failed comparison — and §4.3 requires the second to be
// counted as unverified, never as a finding.
func TestRowsComparable_SeparatesUnrenderableFromDifferent(t *testing.T) {
	same := map[string]any{"key": "vtx.unit.x", "name": "Loft"}
	other := map[string]any{"key": "vtx.unit.x", "name": "Loft renamed"}

	equal, comparable := rowsComparable(same, map[string]any{"key": "vtx.unit.x", "name": "Loft"})
	require.True(t, comparable)
	require.True(t, equal)

	equal, comparable = rowsComparable(same, other)
	require.True(t, comparable, "two renderable rows that differ ARE comparable — they simply differ")
	require.False(t, equal)

	// A value JSON cannot express. rowsEquivalent reports these as "not equal",
	// which is a claim; rowsComparable reports that no claim can be made.
	unrenderable := map[string]any{"key": "vtx.unit.x", "score": math.NaN()}
	_, comparable = rowsComparable(same, unrenderable)
	require.False(t, comparable)
	require.False(t, rowsEquivalent(same, unrenderable),
		"the repair path's collapse is unchanged: an unrenderable row is one it should rewrite")
}

// TestRowsComparableMasked_ExcludesGivenColumns pins the Inc 1 premise
// (secure-plain-lens-retraction-and-audit-design.md §15.1, §2.4): a freshly
// computed row (ruleengine.EvalResult.Row) carries every RETURN alias, its own
// key columns included, because the engine has no notion of "this alias is
// also the key" — while adapter.RowReader.GetRow's contract excludes them (a
// Postgres GetRow scopes its SELECT by key and never returns them as content).
// Comparing them unmasked reports a mismatch no recomputation could ever
// resolve — the exact signature that read every anchor of leaseApplicationsRead
// `stale` on the shared stack, reproduced live against the real full engine and
// a real Postgres GetRow (see the Increment-1 build note). rowsComparable
// itself must keep disagreeing: it is the sweep's own comparator, and a mask is
// never threaded there.
func TestRowsComparableMasked_ExcludesGivenColumns(t *testing.T) {
	stored := map[string]any{"name": "Loft"}
	computed := map[string]any{"app_id": "abc", "name": "Loft"}

	equalUnmasked, comparable := rowsComparable(stored, computed)
	require.True(t, comparable)
	require.False(t, equalUnmasked, "rowsComparable is untouched and must still disagree")

	equalMasked, comparable := rowsComparableMasked(stored, computed, []string{"app_id"})
	require.True(t, comparable)
	require.True(t, equalMasked, "excluding the row's own key column must make the comparison agree")

	// A genuine content divergence on a non-excluded column must still be
	// caught — the mask excludes exactly the columns it is given, nothing more.
	equalMasked, comparable = rowsComparableMasked(stored, map[string]any{"app_id": "abc", "name": "Loft renamed"}, []string{"app_id"})
	require.True(t, comparable)
	require.False(t, equalMasked)
}
