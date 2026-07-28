package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// fanOutEntryFn is a minimal MultiEnvelopeFn: it splits row["ids"] (a []any
// of strings) into one Envelope per entry, keyed "child.<id>". It stands in
// for projection.OutputDescriptor.EntryEnvelopeFn without importing the
// projection package (which would form an import cycle with pipeline).
func fanOutEntryFn(row, _, _ map[string]any) ([]Envelope, error) {
	ids, _ := row["ids"].([]any)
	entries := make([]Envelope, 0, len(ids))
	for _, raw := range ids {
		id, _ := raw.(string)
		if id == "" {
			return nil, errors.New("fanOutEntryFn: empty id")
		}
		entries = append(entries, Envelope{
			Keys: map[string]any{"key": "child." + id},
			Row:  map[string]any{"key": "child." + id, "id": id},
		})
	}
	return entries, nil
}

func TestSetMultiEnvelopeFn_MutuallyExclusiveWithEnvelopeFn(t *testing.T) {
	p := &Pipeline{}

	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	require.NotNil(t, p.envelopeFn)

	p.SetMultiEnvelopeFn(fanOutEntryFn)
	require.NotNil(t, p.multiEnvelopeFn)
	require.Nil(t, p.envelopeFn, "installing a MultiEnvelopeFn must clear any installed EnvelopeFn")

	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	require.NotNil(t, p.envelopeFn)
	require.Nil(t, p.multiEnvelopeFn, "installing an EnvelopeFn must clear any installed MultiEnvelopeFn")
}

// singleRowEngine parses a cypher that returns exactly one row per actor
// carrying a fixed "ids" list — enough to drive executeFullForActor's
// multiEnvelopeFn dispatch without a live graph.
func singleRowEngine(t *testing.T) (*full.Engine, ruleengine.CompiledRule) {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(`MATCH (identity:identity {key: $actorKey}) RETURN identity.key AS actorKey`)
	require.NoError(t, err)
	return eng, cr
}

// newMultiEntryTargetAdapter builds a guarded, soft-delete NatsKVAdapter over
// a fresh embedded-NATS KV bucket, keyed on the single "key" field —
// perEntry lenses' own IntoKey shape — so multiEntryRetractions' ListKeysPrefix
// / GetRow round trip runs against the real substrate, not a fake.
func newMultiEntryTargetAdapter(t *testing.T) *adapter.NatsKVAdapter {
	t.Helper()
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "MULTITARGET"})
	require.NoError(t, err)
	kv, err := conn.OpenKV(ctx, "MULTITARGET")
	require.NoError(t, err)
	adpt, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeSoft)
	require.NoError(t, err)
	adpt.SetGuarded(true)
	return adpt
}

func TestExecuteFullForActor_MultiEnvelopeFn_EmitsPerEntryResults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityDddddddd"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(row, keys, params map[string]any) ([]Envelope, error) {
		return fanOutEntryFn(map[string]any{"ids": []any{"a1", "a2", "a3"}}, keys, params)
	}

	p := &Pipeline{
		ruleID:          "rule-multi",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            newMultiEntryTargetAdapter(t),
		actorDeleteKey:  func(string) string { return "child" },
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Len(t, results, 3)
	got := map[string]bool{}
	for _, r := range results {
		require.False(t, r.Delete)
		got[r.Keys["key"].(string)] = true
	}
	require.True(t, got["child.a1"])
	require.True(t, got["child.a2"])
	require.True(t, got["child.a3"])
}

func TestExecuteFullForActor_MultiEnvelopeFn_ZeroEntries_NoResultsNoError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityEeeeeeee"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return nil, nil
	}

	p := &Pipeline{
		ruleID:          "rule-multi-empty",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            newMultiEntryTargetAdapter(t),
		actorDeleteKey:  func(string) string { return "child" },
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Empty(t, results, "zero real entries against no prior children must write nothing and delete nothing")
}

func TestExecuteFullForActor_MultiEnvelopeFn_SkipProjection_DropsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityFfffffff"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return nil, ErrSkipProjection
	}

	p := &Pipeline{
		ruleID:          "rule-multi-skip",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            newMultiEntryTargetAdapter(t),
		actorDeleteKey:  func(string) string { return "child" },
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestExecuteFullForActor_MultiEnvelopeFn_Error_FailsActorClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityGggggggg"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	wantErr := errors.New("boom: malformed key token")
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return nil, wantErr
	}

	p := &Pipeline{
		ruleID:          "rule-multi-err",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	_, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.Error(t, err)
	require.ErrorContains(t, err, "boom: malformed key token")
}

// TestExecuteFullForActor_MultiEnvelopeFn_Retraction_DropsAnchor is §4.2's
// load-bearing case: an actor previously held child.a1 and child.a2; this
// evaluation's fresh set only re-earns a1. The dropped anchor must be
// tombstoned, ordered ahead of the fresh upsert (deny-closed).
func TestExecuteFullForActor_MultiEnvelopeFn_Retraction_DropsAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityHhhhhhhh"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	adpt := newMultiEntryTargetAdapter(t)
	require.NoError(t, adpt.Upsert(ctx, map[string]any{"key": "child.a1"}, map[string]any{"key": "child.a1", "id": "a1"}, 1))
	require.NoError(t, adpt.Upsert(ctx, map[string]any{"key": "child.a2"}, map[string]any{"key": "child.a2", "id": "a2"}, 1))

	eng, cr := singleRowEngine(t)
	entryFn := func(row, keys, params map[string]any) ([]Envelope, error) {
		return fanOutEntryFn(map[string]any{"ids": []any{"a1"}}, keys, params)
	}

	p := &Pipeline{
		ruleID:          "rule-multi-retract",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            adpt,
		actorDeleteKey:  func(string) string { return "child" },
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.True(t, results[0].Delete, "the dropped anchor's tombstone must be ordered ahead of the fresh upsert")
	require.Equal(t, "child.a2", results[0].Keys["key"])
	require.False(t, results[1].Delete)
	require.Equal(t, "child.a1", results[1].Keys["key"])
}

// TestExecuteFullForActor_MultiEnvelopeFn_Retraction_AlreadyTombstonedSkipped
// pins §4.2's tombstone-skip semantics: a candidate the listing surfaces that
// is already soft-deleted must not be rewritten — its stored watermark
// already outranks any grant-era replay, so re-stamping it is pure churn.
func TestExecuteFullForActor_MultiEnvelopeFn_Retraction_AlreadyTombstonedSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityIiiiiiii"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	adpt := newMultiEntryTargetAdapter(t)
	require.NoError(t, adpt.Upsert(ctx, map[string]any{"key": "child.a1"}, map[string]any{"key": "child.a1", "id": "a1"}, 1))
	require.NoError(t, adpt.Delete(ctx, map[string]any{"key": "child.a2"}, 5))

	eng, cr := singleRowEngine(t)
	entryFn := func(row, keys, params map[string]any) ([]Envelope, error) {
		return fanOutEntryFn(map[string]any{"ids": []any{"a1"}}, keys, params)
	}

	p := &Pipeline{
		ruleID:          "rule-multi-retract-skip",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            adpt,
		actorDeleteKey:  func(string) string { return "child" },
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Len(t, results, 1, "an already-tombstoned candidate must not be rewritten")
	require.False(t, results[0].Delete)
	require.Equal(t, "child.a1", results[0].Keys["key"])
}

// TestExecuteFullForActor_MultiEnvelopeFn_Retraction_MalformedFreshKey_FailsClosed
// pins that a fresh entry carrying no usable "key" field fails the whole
// actor evaluation rather than silently vanishing from the fresh set — the
// silent-drop shape would otherwise durably tombstone a still-live sibling
// key with no error (a future MultiEnvelopeFn implementation could produce
// this; EntryEnvelopeFn itself never does).
func TestExecuteFullForActor_MultiEnvelopeFn_Retraction_MalformedFreshKey_FailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityJjjjjjjj"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	adpt := newMultiEntryTargetAdapter(t)
	require.NoError(t, adpt.Upsert(ctx, map[string]any{"key": "child.a1"}, map[string]any{"key": "child.a1", "id": "a1"}, 1))

	eng, cr := singleRowEngine(t)
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return []Envelope{{Keys: map[string]any{"notKey": "oops"}, Row: map[string]any{}}}, nil
	}

	p := &Pipeline{
		ruleID:          "rule-multi-retract-malformed",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            adpt,
		actorDeleteKey:  func(string) string { return "child" },
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	_, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.Error(t, err)
	require.ErrorContains(t, err, `no usable "key" field`)
}

// fakeBareAdapter satisfies adapter.Adapter and nothing else — used to pin
// multiEntryRetractions' fail-closed refusal when the target can't support
// the diff.
type fakeBareAdapter struct{}

func (fakeBareAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (fakeBareAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (fakeBareAdapter) Probe(context.Context) error                         { return nil }
func (fakeBareAdapter) Close() error                                        { return nil }

// fakePrefixOnlyAdapter adds PrefixKeyLister but still can't read back a row.
type fakePrefixOnlyAdapter struct {
	fakeBareAdapter
}

func (fakePrefixOnlyAdapter) ListKeysPrefix(context.Context, string) ([]map[string]any, error) {
	return []map[string]any{{"key": "child.a2"}}, nil
}

func TestMultiEntryRetractions_AdapterNotPrefixKeyLister_ErrorsClosed(t *testing.T) {
	p := &Pipeline{adpt: fakeBareAdapter{}, actorDeleteKey: func(string) string { return "child" }}
	_, err := p.multiEntryRetractions(context.Background(), "vtx.identity.x", nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot enumerate keys by prefix")
}

func TestMultiEntryRetractions_AdapterNotRowReader_ErrorsClosed(t *testing.T) {
	p := &Pipeline{adpt: fakePrefixOnlyAdapter{}, actorDeleteKey: func(string) string { return "child" }}
	_, err := p.multiEntryRetractions(context.Background(), "vtx.identity.x", nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot read back a row")
}
