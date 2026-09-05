package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// recordingEntryAdapter is a real guarded multi-entry target adapter that
// records what the write loop asks of it: which key sets the batched read-back
// requested, and every key written. It embeds the CONCRETE *NatsKVAdapter so
// GetRow / ListKeysPrefix / Guarded promote — multiEntryRetractions needs all
// three — and overrides both spellings of each write, because the pipeline
// prefers the outcome-returning form and overriding only the plain one would
// leave the recording silently bypassed.
type recordingEntryAdapter struct {
	*adapter.NatsKVAdapter

	mu        sync.Mutex
	rowsAsked [][]string
	upserts   []string
	deletes   []string

	// rowsErr, when set, fails the batched read-back — the fixture for "a read
	// failure marks nothing and every entry is written".
	rowsErr error

	// onListKeysPrefix, when set, runs at the start of every prefix listing.
	// It is the interleaving hook the ordering pins drive: the listing is the
	// first thing a perEntry evaluation does AFTER its engine walk and BEFORE
	// any write, so parking there holds one caller between the view it read
	// and the writes it derived from it.
	onListKeysPrefix func()
}

func (a *recordingEntryAdapter) GetRows(ctx context.Context, keys []string) (map[string]map[string]any, error) {
	a.mu.Lock()
	a.rowsAsked = append(a.rowsAsked, append([]string(nil), keys...))
	rowsErr := a.rowsErr
	a.mu.Unlock()
	if rowsErr != nil {
		return nil, rowsErr
	}
	return a.NatsKVAdapter.GetRows(ctx, keys)
}

func (a *recordingEntryAdapter) ListKeysPrefix(ctx context.Context, prefix string) ([]map[string]any, error) {
	a.mu.Lock()
	hook := a.onListKeysPrefix
	a.mu.Unlock()
	if hook != nil {
		hook()
	}
	return a.NatsKVAdapter.ListKeysPrefix(ctx, prefix)
}

func (a *recordingEntryAdapter) Upsert(ctx context.Context, keys map[string]any, row map[string]any, seq uint64) error {
	a.recordUpsert(keys)
	return a.NatsKVAdapter.Upsert(ctx, keys, row, seq)
}

func (a *recordingEntryAdapter) UpsertWithOutcome(ctx context.Context, keys map[string]any, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	a.recordUpsert(keys)
	return a.NatsKVAdapter.UpsertWithOutcome(ctx, keys, row, seq)
}

func (a *recordingEntryAdapter) Delete(ctx context.Context, keys map[string]any, seq uint64) error {
	a.recordDelete(keys)
	return a.NatsKVAdapter.Delete(ctx, keys, seq)
}

func (a *recordingEntryAdapter) DeleteWithOutcome(ctx context.Context, keys map[string]any, seq uint64) (adapter.DeleteOutcome, error) {
	a.recordDelete(keys)
	return a.NatsKVAdapter.DeleteWithOutcome(ctx, keys, seq)
}

func (a *recordingEntryAdapter) recordUpsert(keys map[string]any) {
	k, _ := keys["key"].(string)
	a.mu.Lock()
	a.upserts = append(a.upserts, k)
	a.mu.Unlock()
}

func (a *recordingEntryAdapter) recordDelete(keys map[string]any) {
	k, _ := keys["key"].(string)
	a.mu.Lock()
	a.deletes = append(a.deletes, k)
	a.mu.Unlock()
}

func (a *recordingEntryAdapter) snapshot() (rowsAsked [][]string, upserts, deletes []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]string(nil), a.rowsAsked...),
		append([]string(nil), a.upserts...),
		append([]string(nil), a.deletes...)
}

func (a *recordingEntryAdapter) reset() {
	a.mu.Lock()
	a.rowsAsked, a.upserts, a.deletes = nil, nil, nil
	a.mu.Unlock()
}

// noRowsReaderAdapter is a target that reads ONE row back but not many: every
// method multiEntryRetractions needs, forwarded explicitly, and no GetRows. It
// cannot embed the concrete adapter, because embedding would promote GetRows
// and the type would satisfy adapter.RowsReader after all — which is exactly
// the conjunct this fixture exists to negate.
type noRowsReaderAdapter struct{ inner *adapter.NatsKVAdapter }

func (a *noRowsReaderAdapter) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	return a.inner.Upsert(ctx, keys, row, seq)
}
func (a *noRowsReaderAdapter) Delete(ctx context.Context, keys map[string]any, seq uint64) error {
	return a.inner.Delete(ctx, keys, seq)
}
func (a *noRowsReaderAdapter) Probe(ctx context.Context) error { return a.inner.Probe(ctx) }
func (a *noRowsReaderAdapter) Close() error                    { return a.inner.Close() }
func (a *noRowsReaderAdapter) GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error) {
	return a.inner.GetRow(ctx, keys)
}
func (a *noRowsReaderAdapter) ListKeysPrefix(ctx context.Context, prefix string) ([]map[string]any, error) {
	return a.inner.ListKeysPrefix(ctx, prefix)
}
func (a *noRowsReaderAdapter) Guarded() bool { return a.inner.Guarded() }

const withholdActor = "vtx.identity.TwithAaaaaaaaaaaaaaa"

// newWithholdFixture builds a perEntry pipeline in the shape the CDC write
// loop runs in — a guarded, soft-delete NATS-KV target under a
// multiEnvelopeFn, over a real Core KV holding the actor vertex — with
// entryIDs as the evaluation's entry set. The target's own KV handle comes
// back so a case can plant a stored body no adapter would write.
func newWithholdFixture(t *testing.T, ruleID string, entryIDs ...string) (*Pipeline, *recordingEntryAdapter, *substrate.KV) {
	t.Helper()
	kvs := newTestKVs(t, "CORE", "ADJ", "TARGET")
	coreKV, adjKV, targetKV := kvs[0], kvs[1], kvs[2]
	writeCollisionVertex(t, coreKV, withholdActor, "identity", map[string]any{})

	inner, err := adapter.New(targetKV, []string{"key"}, adapter.DeleteModeSoft)
	require.NoError(t, err)
	inner.SetGuarded(true)
	adpt := &recordingEntryAdapter{NatsKVAdapter: inner}

	eng, cr := singleRowEngine(t)
	ids := make([]any, 0, len(entryIDs))
	for _, id := range entryIDs {
		ids = append(ids, id)
	}
	p := &Pipeline{
		ruleID:         ruleID,
		coreKV:         coreKV,
		adjKV:          adjKV,
		adpt:           adpt,
		actorDeleteKey: func(string) string { return "child" },
		engineKind:     ruleengine.EngineFull,
		fullEngine:     eng,
		fullCR:         cr,
		multiEnvelopeFn: func(row, keys, params map[string]any) ([]Envelope, error) {
			return fanOutEntryFn(map[string]any{"ids": ids}, keys, params)
		},
	}
	return p, adpt, targetKV
}

// evaluateWithhold runs one costed evaluation of the actor — the same call the
// CDC write path makes — and returns its results. modifiedAt is the actor
// vertex's lastModifiedAt, which is what the envelope stamps as projectedAt.
func evaluateWithhold(t *testing.T, p *Pipeline, modifiedAt string) []ruleengine.EvalResult {
	t.Helper()
	results, err := p.executeFullForActor(context.Background(), p.ruleState(), withholdActor,
		map[string]any{"lastModifiedAt": modifiedAt}, "")
	require.NoError(t, err)
	return results
}

// TestWithholding_UnchangedEntryIsNotWritten is T1: the whole shape of a
// withheld write, and the positive vector beside it.
//
// Pass 1 creates the actor's three entries. Pass 2 evaluates the identical
// state: every entry's stored body already equals the fresh one, so the write
// loop writes nothing, marks no freshness clock, counts no projection write,
// and reports the three as withheld. Pass 3 changes one entry's body and
// proves that one — and only that one — is written, which is what makes pass
// 2's silence evidence of the predicate rather than of a broken loop.
//
// It also pins the read's SCOPE: the only keys the batched read-back ever asks
// for are this actor's own children.
func TestWithholding_UnchangedEntryIsNotWritten(t *testing.T) {
	ctx := context.Background()
	p, adpt, _ := newWithholdFixture(t, "withhold-rule", "a1", "a2", "a3")

	// Pass 1 — the entries do not exist yet, so every one is written.
	first := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	require.Len(t, first, 3)
	for _, r := range first {
		require.False(t, r.Unchanged, "an entry the target does not hold yet is never withheld")
	}
	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 10}, withholdActor, first, nil, ScopeAll())
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)
	_, upserts, _ := adpt.snapshot()
	require.Len(t, upserts, 3, "the creating pass writes every entry")

	// Pass 2 — identical state. Every entry is withheld.
	adpt.reset()
	writesBefore := p.ProjectionWrites()
	projectedBefore := p.Progress().LastProjectedAt
	withheldBefore := p.EntriesWithheld()

	second := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	require.Len(t, second, 3)
	for _, r := range second {
		require.True(t, r.Unchanged, "an entry whose stored body equals the fresh one is withheld")
	}
	decision, err = p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 11}, withholdActor, second, nil, ScopeAll())
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)

	rowsAsked, upserts, deletes := adpt.snapshot()
	require.Empty(t, upserts, "a withheld entry reaches no adapter write")
	require.Empty(t, deletes)
	require.Equal(t, writesBefore, p.ProjectionWrites(), "a withheld entry is not counted as a projection write")
	// The freshness clock and the audit entry are emitted from ONE loop over
	// the committed results, so an unmoved clock is also the proof that no
	// audit entry was appended for a row that never landed.
	require.Equal(t, projectedBefore, p.Progress().LastProjectedAt,
		"a withheld entry marks no freshness clock, and so appends no audit entry")
	require.Equal(t, withheldBefore+3, p.EntriesWithheld())

	require.Len(t, rowsAsked, 1, "one batched read-back per costed evaluation, not one per entry")
	for _, k := range rowsAsked[0] {
		require.Contains(t, k, "child.", "the read requests only this actor's own children")
	}
	require.ElementsMatch(t, []string{"child.a1", "child.a2", "child.a3"}, rowsAsked[0])

	// Pass 3 — one entry's body changes. Exactly that entry is written.
	adpt.reset()
	require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": "child.a2"},
		map[string]any{"key": "child.a2", "id": "a2", "via": "a-different-role"}, 12))
	adpt.reset()

	third := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	require.Len(t, third, 3)
	for _, r := range third {
		if r.Keys["key"].(string) == "child.a2" {
			require.False(t, r.Unchanged, "an entry whose stored body differs is written")
			continue
		}
		require.True(t, r.Unchanged)
	}
	decision, err = p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 13}, withholdActor, third, nil, ScopeAll())
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)
	_, upserts, _ = adpt.snapshot()
	require.Equal(t, []string{"child.a2"}, upserts, "only the entry that changed is written")
}

// TestWithholding_TheProcessedLineReportsTheCount is T8's operator half. A
// lens that has stopped writing because its grants stopped changing looks
// exactly like one that has stopped working, and the processed line is where an
// operator asking "why did this event write so little" is already looking — so
// the count goes there, and only when there is one to report.
func TestWithholding_TheProcessedLineReportsTheCount(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newWithholdFixture(t, "withhold-log-rule", "a1", "a2")

	logs := captureLogs(t, slogJSONHandler)

	first := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	_, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 60}, withholdActor, first, nil, ScopeAll())
	require.NoError(t, err)
	require.NotContains(t, logs(), "entriesWithheld",
		"a pass that withheld nothing says nothing about withholding")

	second := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	_, err = p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 61}, withholdActor, second, nil, ScopeAll())
	require.NoError(t, err)
	require.Contains(t, logs(), `"entriesWithheld":2`,
		"the processed line carries the count of entries this event did not write")
}

// TestWithholding_ARetractionIsNeverWithheld is T1's other half: a Delete and
// a FailClosed result must never carry Unchanged, whatever the store holds —
// the tombstones-first ordering and the FailClosed batch abort both depend on
// their writes being unskippable.
func TestWithholding_ARetractionIsNeverWithheld(t *testing.T) {
	ctx := context.Background()
	p, adpt, _ := newWithholdFixture(t, "withhold-retraction-rule", "a1")

	// A legacy parent document and a child the fresh set no longer carries:
	// both become FailClosed tombstones from the prefix diff.
	require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": "child"},
		map[string]any{"key": "child", "readableAnchors": []any{}}, 1))
	require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": "child.dropped"},
		map[string]any{"key": "child.dropped", "id": "dropped"}, 2))

	results := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	require.Len(t, results, 3)
	tombstones := 0
	for _, r := range results {
		if !r.Delete {
			continue
		}
		tombstones++
		require.True(t, r.FailClosed)
		require.False(t, r.Unchanged, "a retraction is always written")
	}
	require.Equal(t, 2, tombstones)

	adpt.reset()
	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 20}, withholdActor, results, nil, ScopeAll())
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)
	_, upserts, deletes := adpt.snapshot()
	require.ElementsMatch(t, []string{"child", "child.dropped"}, deletes, "both tombstones land")
	require.Equal(t, []string{"child.a1"}, upserts)
}

// TestWithholding_PredicateIsRowsEquivalent is T2: the table over §5's rows
// 1–4 and 8. Each case seeds one stored body for a single-entry actor and
// asserts whether the evaluation withholds — the predicate is rowsEquivalent,
// and nothing else.
func TestWithholding_PredicateIsRowsEquivalent(t *testing.T) {
	ctx := context.Background()
	fresh := map[string]any{"key": "child.a1", "id": "a1"}

	cases := []struct {
		name     string
		seed     func(t *testing.T, a *recordingEntryAdapter)
		withheld bool
		modAt    string
		explains string
	}{{
		name:     "absent stored entry is written",
		seed:     func(*testing.T, *recordingEntryAdapter) {},
		withheld: false,
		modAt:    "2026-05-15T10:00:00Z",
		explains: "§5 row 1 — nothing to compare against",
	}, {
		name: "identical stored body is withheld",
		seed: func(t *testing.T, a *recordingEntryAdapter) {
			require.NoError(t, a.NatsKVAdapter.Upsert(ctx, map[string]any{"key": "child.a1"},
				map[string]any{"key": "child.a1", "id": "a1", "projectedAt": "2026-05-15T10:00:00Z"}, 3))
		},
		withheld: true,
		modAt:    "2026-05-15T10:00:00Z",
		explains: "§5 row 2",
	}, {
		name: "a projectedAt-only difference is withheld",
		seed: func(t *testing.T, a *recordingEntryAdapter) {
			require.NoError(t, a.NatsKVAdapter.Upsert(ctx, map[string]any{"key": "child.a1"},
				map[string]any{"key": "child.a1", "id": "a1", "projectedAt": "2001-01-01T00:00:00Z"}, 3))
		},
		withheld: true,
		modAt:    "2026-05-15T10:00:00Z",
		explains: "§5 row 2 — projectedAt is restamped every evaluation and carries no meaning",
	}, {
		name: "a differing field is written",
		seed: func(t *testing.T, a *recordingEntryAdapter) {
			require.NoError(t, a.NatsKVAdapter.Upsert(ctx, map[string]any{"key": "child.a1"},
				map[string]any{"key": "child.a1", "id": "a1", "via": "some-role"}, 3))
		},
		withheld: false,
		modAt:    "2026-05-15T10:00:00Z",
		explains: "§5 row 3",
	}, {
		name: "a tombstoned stored entry is written",
		seed: func(t *testing.T, a *recordingEntryAdapter) {
			require.NoError(t, a.NatsKVAdapter.Upsert(ctx, map[string]any{"key": "child.a1"},
				map[string]any{"key": "child.a1", "id": "a1"}, 3))
			require.NoError(t, a.NatsKVAdapter.Delete(ctx, map[string]any{"key": "child.a1"}, 4))
		},
		withheld: false,
		modAt:    "2026-05-15T10:00:00Z",
		explains: "§5 row 4 — a tombstone reads as absent, so the entry is resurrected",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, adpt, _ := newWithholdFixture(t, "withhold-table-rule", "a1")
			tc.seed(t, adpt)

			results := evaluateWithhold(t, p, tc.modAt)
			require.Len(t, results, 1, tc.explains)
			require.Equal(t, fresh["key"], results[0].Keys["key"])
			require.Equal(t, tc.withheld, results[0].Unchanged, tc.explains)
		})
	}
}

// TestWithholding_ACorruptStoredMemberIsRewrittenAndItsSiblingsWithheld is T5:
// the dossier's mandated shape for any set read. One unparseable stored body
// among three costs its own entry a write and nothing else — the batch is not
// failed, and the two readable siblings are still withheld.
func TestWithholding_ACorruptStoredMemberIsRewrittenAndItsSiblingsWithheld(t *testing.T) {
	ctx := context.Background()
	p, _, targetKV := newWithholdFixture(t, "withhold-corrupt-rule", "a1", "a2", "a3")

	first := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	_, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 30}, withholdActor, first, nil, ScopeAll())
	require.NoError(t, err)

	// A stored body no JSON decoder will accept, planted under the adapter so
	// the read-back has to answer for it.
	_, err = targetKV.Put(ctx, "child.a2", []byte("{not json"))
	require.NoError(t, err)

	second := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	require.Len(t, second, 3)
	for _, r := range second {
		if r.Keys["key"].(string) == "child.a2" {
			require.False(t, r.Unchanged, "an unparseable stored body is 'different', so its entry is rewritten")
			continue
		}
		require.True(t, r.Unchanged, "a corrupt member must not cost its siblings their withholding")
	}
}

// TestWithholding_ABatchReadFailureWritesEverythingAndCounts is the failure
// direction: a read-back that errors marks nothing, so this actor's entries
// are all written — byte-identical to a disarmed pipeline — and the failure
// moves a counter rather than latching anything off.
func TestWithholding_ABatchReadFailureWritesEverythingAndCounts(t *testing.T) {
	ctx := context.Background()
	p, adpt, _ := newWithholdFixture(t, "withhold-readfail-rule", "a1", "a2")

	first := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	_, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 40}, withholdActor, first, nil, ScopeAll())
	require.NoError(t, err)

	adpt.mu.Lock()
	adpt.rowsErr = errors.New("injected: batched read-back unavailable")
	adpt.mu.Unlock()

	failuresBefore := p.WithholdReadFailures()
	second := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	require.Len(t, second, 2)
	for _, r := range second {
		require.False(t, r.Unchanged, "a failed read marks nothing — every entry is written")
	}
	require.Equal(t, failuresBefore+1, p.WithholdReadFailures(), "the failure is counted once for the actor")

	// Nothing is remembered: the very next evaluation withholds again.
	adpt.mu.Lock()
	adpt.rowsErr = nil
	adpt.mu.Unlock()
	third := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	for _, r := range third {
		require.True(t, r.Unchanged, "a read failure is a rate, never a latch that disables the mechanism")
	}
}

// TestWithholding_ArmingConjuncts is T6: each conjunct's negation disarms, and
// the positive vector beside them proves the table is not simply always false.
func TestWithholding_ArmingConjuncts(t *testing.T) {
	kvs := newTestKVs(t, "GUARDED", "UNGUARDED")
	guarded, err := adapter.New(kvs[0], []string{"key"}, adapter.DeleteModeSoft)
	require.NoError(t, err)
	guarded.SetGuarded(true)
	unguarded, err := adapter.New(kvs[1], []string{"key"}, adapter.DeleteModeSoft)
	require.NoError(t, err)

	armed := &Pipeline{ruleID: "armed", multiEnvelopeFn: fanOutEntryFn}
	require.True(t, armed.withholdingArmed(guarded), "the positive vector: a perEntry lens on a guarded, readable target")

	docMode := &Pipeline{ruleID: "doc-mode"}
	require.False(t, docMode.withholdingArmed(guarded), "conjunct 1: no multiEnvelopeFn, so not the perEntry family")

	require.False(t, armed.withholdingArmed(&noRowsReaderAdapter{inner: guarded}),
		"conjunct 2: a target that cannot be read back in batch has no stored body to compare")
	require.False(t, armed.withholdingArmed(unguarded),
		"conjunct 2: an unguarded target's ordering is not the guard's, and it already skips identical rows itself")

	secure := &Pipeline{ruleID: "secure", multiEnvelopeFn: fanOutEntryFn, secureDecryptor: &SecureDecryptor{}}
	require.False(t, secure.withholdingArmed(guarded),
		"conjunct 3: a Secure lens compares before decryption, so it would compare ciphertext against plaintext")
}

// TestWithholding_TheAuditPathIssuesNoReadBack is T8's cost half: the
// background divergence audit writes nothing, so a read that decides what to
// write would be pure cost. It evaluates through the uncosted entry point and
// must never reach the batched read-back.
func TestWithholding_TheAuditPathIssuesNoReadBack(t *testing.T) {
	ctx := context.Background()
	p, adpt, _ := newWithholdFixture(t, "withhold-audit-rule", "a1", "a2")

	first := evaluateWithhold(t, p, "2026-05-15T10:00:00Z")
	_, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 50}, withholdActor, first, nil, ScopeAll())
	require.NoError(t, err)

	adpt.reset()
	results, err := p.executeFullForAudit(ctx, p.ruleState(), withholdActor,
		map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}, "")
	require.NoError(t, err)
	require.Len(t, results, 2)
	rowsAsked, _, _ := adpt.snapshot()
	require.Empty(t, rowsAsked, "an uncosted evaluation issues no batched read-back")
	for _, r := range results {
		require.False(t, r.Unchanged, "an uncosted evaluation marks nothing")
	}
}
