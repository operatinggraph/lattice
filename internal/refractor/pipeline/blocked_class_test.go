package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// TestBlockedClass_Ordering pins the one order the fold, the sweep's governing
// reason and the health severity all read. Retraction and content sit above the
// rest because neither has an observed ordinary producer; unknown sits ABOVE
// provenance because a row whose class could not be proven must never be
// treated as the benign one.
func TestBlockedClass_Ordering(t *testing.T) {
	require.Greater(t, BlockedRetraction.severity(), BlockedContent.severity(),
		"a revoked grant left live outranks a content divergence")
	require.Greater(t, BlockedContent.severity(), BlockedUnknown.severity(),
		"a proven content divergence outranks one that could not be classified")
	require.Greater(t, BlockedUnknown.severity(), BlockedProvenance.severity(),
		"an unprovable class must not be demoted to the benign one")

	require.Equal(t, "retraction", BlockedRetraction.String())
	require.Equal(t, "content", BlockedContent.String())
	require.Equal(t, "unknown", BlockedUnknown.String())
	require.Equal(t, "provenance", BlockedProvenance.String())
}

// TestBlockedClass_ZeroValueIsUnknown pins the fail-closed default by the TYPE,
// exactly as VerdictUnverified is pinned. A blocked result that reaches a
// consumer unstamped reports "I cannot prove which kind" — never the benign
// class, which would silence the loudest condition on the plane by omission.
func TestBlockedClass_ZeroValueIsUnknown(t *testing.T) {
	var zero BlockedClass
	require.Equal(t, BlockedUnknown, zero)
	require.Equal(t, "unknown", zero.String())

	var r Reprojection
	require.Equal(t, BlockedUnknown, r.BlockedClass)
}

// TestVerdictFold_BlockedTieResolvesByClass is the fold-level statement of the
// defect. Two blocked results TIE on verdict severity, so the previous fold kept
// whichever the loop reached first: a perEntry actor holding one provenance-only
// blocked row and one content-divergence blocked row reported either, depending
// on iteration order.
//
// Both orders are driven, because a tie-break that only works in one of them is
// the bug wearing a passing test.
func TestVerdictFold_BlockedTieResolvesByClass(t *testing.T) {
	t.Run("provenance arrives first", func(t *testing.T) {
		var f verdictFold
		f.addBlocked(VerdictBlocked, BlockedProvenance, "provenance reason")
		f.addBlocked(VerdictBlocked, BlockedContent, "content reason")
		v, class, reason := f.resolve("nothing concluded")
		require.Equal(t, VerdictBlocked, v)
		require.Equal(t, BlockedContent, class)
		require.Equal(t, "content reason", reason, "the reason must travel with the winning class")
	})

	t.Run("content arrives first", func(t *testing.T) {
		var f verdictFold
		f.addBlocked(VerdictBlocked, BlockedContent, "content reason")
		f.addBlocked(VerdictBlocked, BlockedProvenance, "provenance reason")
		v, class, reason := f.resolve("nothing concluded")
		require.Equal(t, VerdictBlocked, v)
		require.Equal(t, BlockedContent, class)
		require.Equal(t, "content reason", reason)
	})

	t.Run("retraction outranks every divergence class", func(t *testing.T) {
		var f verdictFold
		f.addBlocked(VerdictBlocked, BlockedContent, "content reason")
		f.addBlocked(VerdictBlocked, BlockedRetraction, "retraction reason")
		f.addBlocked(VerdictBlocked, BlockedProvenance, "provenance reason")
		_, class, reason := f.resolve("nothing concluded")
		require.Equal(t, BlockedRetraction, class)
		require.Equal(t, "retraction reason", reason)
	})

	t.Run("an unprovable class outranks a provenance one", func(t *testing.T) {
		var f verdictFold
		f.addBlocked(VerdictBlocked, BlockedProvenance, "provenance reason")
		f.addBlocked(VerdictBlocked, BlockedUnknown, "unknown reason")
		_, class, reason := f.resolve("nothing concluded")
		require.Equal(t, BlockedUnknown, class)
		require.Equal(t, "unknown reason", reason)
	})
}

// TestVerdictFold_QuieterVerdictsDoNotCarryAClass is the discrimination twin:
// the class must not leak across a verdict boundary. A converged or healed
// result folded in after a blocked one may neither displace the blocked class
// nor introduce one of its own.
func TestVerdictFold_QuieterVerdictsDoNotCarryAClass(t *testing.T) {
	var f verdictFold
	f.add(VerdictConverged, "")
	f.addBlocked(VerdictBlocked, BlockedContent, "content reason")
	f.add(VerdictHealed, "")
	f.add(VerdictConverged, "")
	v, class, reason := f.resolve("nothing concluded")
	require.Equal(t, VerdictBlocked, v)
	require.Equal(t, BlockedContent, class)
	require.Equal(t, "content reason", reason)

	var healed verdictFold
	healed.add(VerdictHealed, "")
	_, class, _ = healed.resolve("nothing concluded")
	require.Equal(t, BlockedUnknown, class, "a non-blocked conclusion carries no class")
}

// perKeyOutcomeAdapter serves a DIFFERENT stored row per target key and declines
// every guarded write, so one Reproject call over a perEntry actor can carry two
// blocked results of two different classes — the shape the fold's tie-break
// exists for, and the one a single canned row cannot pose.
//
// It overrides BOTH the plain and the outcome-reporting form of each verb, per
// the Go-embedding trap adapter.OutcomeUpserter documents: overriding only one
// leaves the other promoted, and Reproject prefers the outcome form.
type perKeyOutcomeAdapter struct {
	recordingAdapter
	rows          map[string]map[string]any
	children      []string
	declineUpsert bool
	declineDelete bool
}

func (a *perKeyOutcomeAdapter) GetRow(_ context.Context, keys map[string]any) (map[string]any, bool, error) {
	if a.getErr != nil {
		return nil, false, a.getErr
	}
	k, _ := keys["key"].(string)
	row, ok := a.rows[k]
	return row, ok, nil
}

func (a *perKeyOutcomeAdapter) ListKeysPrefix(_ context.Context, prefix string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(a.children))
	for _, k := range a.children {
		if strings.HasPrefix(k, prefix) {
			out = append(out, map[string]any{"key": k})
		}
	}
	return out, nil
}

func (a *perKeyOutcomeAdapter) upsertOnce(keys, row map[string]any, seq uint64) adapter.UpsertOutcome {
	a.upserts = append(a.upserts, recordedWrite{keys: keys, row: row, seq: seq})
	return adapter.UpsertOutcome{
		Wrote:               true,
		Committed:           !a.declineUpsert,
		DeclinedByWatermark: a.declineUpsert,
	}
}

func (a *perKeyOutcomeAdapter) Upsert(_ context.Context, keys, row map[string]any, seq uint64) error {
	a.upsertOnce(keys, row, seq)
	return a.writeErr
}

func (a *perKeyOutcomeAdapter) UpsertWithOutcome(_ context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	if a.writeErr != nil {
		return adapter.UpsertOutcome{}, a.writeErr
	}
	return a.upsertOnce(keys, row, seq), nil
}

func (a *perKeyOutcomeAdapter) deleteOnce(keys map[string]any, seq uint64) adapter.DeleteOutcome {
	a.deletes = append(a.deletes, recordedWrite{keys: keys, seq: seq})
	return adapter.DeleteOutcome{Wrote: !a.declineDelete, DeclinedByWatermark: a.declineDelete}
}

func (a *perKeyOutcomeAdapter) Delete(_ context.Context, keys map[string]any, seq uint64) error {
	a.deleteOnce(keys, seq)
	return a.writeErr
}

func (a *perKeyOutcomeAdapter) DeleteWithOutcome(_ context.Context, keys map[string]any, seq uint64) (adapter.DeleteOutcome, error) {
	if a.writeErr != nil {
		return adapter.DeleteOutcome{}, a.writeErr
	}
	return a.deleteOnce(keys, seq), nil
}

// entryKeys are one perEntry actor's two child keys under its parent doc key.
const (
	perEntryParentKey  = "cap.identity.x"
	perEntryChildDrift = perEntryParentKey + ".driftEntry"
	perEntryChildMoved = perEntryParentKey + ".movedEntry"
)

// perEntryRow renders one child entry's document. revision drives the freshness
// record only; grant drives the row's meaning.
func perEntryRow(key, grant string, revision float64) map[string]any {
	return map[string]any{
		"key":                    key,
		"grant":                  grant,
		"projectedFromRevisions": map[string]any{"vtx.identity.x": revision},
	}
}

// TestReprojection_PerEntryActorReportsTheWorstBlockedClass is the green bar's
// first line at the fold's own level: one actor, two entries, one of each class,
// and the actor must report CONTENT.
//
// Fails without the class tie-break: the actor reports whichever entry the write
// loop reached first, so the provenance-first ordering below names
// provenance-only drift for an actor holding a live grant divergence.
func TestReprojection_PerEntryActorReportsTheWorstBlockedClass(t *testing.T) {
	orders := []struct {
		name  string
		first string
		last  string
	}{
		{"provenance entry first", perEntryChildDrift, perEntryChildMoved},
		{"content entry first", perEntryChildMoved, perEntryChildDrift},
	}
	for _, order := range orders {
		t.Run(order.name, func(t *testing.T) {
			adpt := &perKeyOutcomeAdapter{
				rows: map[string]map[string]any{
					// Differs from the recomputed entry ONLY in the freshness record.
					perEntryChildDrift: perEntryRow(perEntryChildDrift, "read", 7),
					// Differs in the row's meaning: a grant the graph no longer says.
					perEntryChildMoved: perEntryRow(perEntryChildMoved, "write", 7),
				},
				children:      []string{perEntryChildDrift, perEntryChildMoved},
				declineUpsert: true,
			}
			p := newReprojectPipeline(t, &adpt.recordingAdapter)
			p.adpt = adpt
			p.recordAppliedSeq(4242)
			writeLiveAnchor(t, p, reprojectActor)
			useAnchorRule(t, p)
			p.SetActorDeleteKey(func(string) string { return perEntryParentKey })
			p.SetEnvelopeFn(nil)
			// Both entries recompute to grant "read" at a fresher revision: the
			// drift entry's stored row already says "read", so only its freshness
			// record moved, while the moved entry's says "write" — a grant the
			// graph no longer makes.
			p.SetMultiEnvelopeFn(func(row, keys, params map[string]any) ([]Envelope, error) {
				return []Envelope{
					{Keys: map[string]any{"key": order.first}, Row: perEntryRow(order.first, "read", 9)},
					{Keys: map[string]any{"key": order.last}, Row: perEntryRow(order.last, "read", 9)},
				}, nil
			})

			res, err := p.Reproject(context.Background(), reprojectActor)
			require.NoError(t, err)

			require.Equal(t, VerdictBlocked, res.Verdict)
			require.Equal(t, BlockedContent, res.BlockedClass,
				"an actor holding one content divergence is not a provenance-drift actor, whatever order the entries came in")
			require.Contains(t, res.VerdictReason, "content divergence",
				"the reason must name the class the actor reports")
			require.Len(t, adpt.upserts, 2, "both entries are still attempted; only the report changes")
		})
	}
}

// TestReprojection_DeclinedRetractionIsClassifiedAsRetraction pins the fourth,
// worse case that used to hide inside the same counter: a declined Delete is the
// OVER-GRANT direction — a revoked grant stays live and honoured — and it is not
// a divergence class at all.
//
// Fails without the stamp: the retraction reports BlockedUnknown, which is the
// class that escalates on a streak rather than on sight.
func TestReprojection_DeclinedRetractionIsClassifiedAsRetraction(t *testing.T) {
	adpt := &perKeyOutcomeAdapter{
		rows:          map[string]map[string]any{perEntryParentKey: {"key": perEntryParentKey, "grant": "stale"}},
		declineDelete: true,
	}
	p := newReprojectPipeline(t, &adpt.recordingAdapter)
	p.adpt = adpt
	p.recordAppliedSeq(4242)
	p.SetActorDeleteKey(func(string) string { return perEntryParentKey })

	// The actor is absent from Core KV, so reconciliation retracts the row — and
	// the guard declines the retraction at a tied watermark.
	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)

	require.Equal(t, VerdictBlocked, res.Verdict)
	require.Equal(t, BlockedRetraction, res.BlockedClass)
	require.Contains(t, res.VerdictReason, "retraction unrepairable")
	require.NotContains(t, res.VerdictReason, "divergence",
		"a declined retraction is not a divergence class and must not be reported as one")
}

// TestReprojection_DeclinedUpsertCarriesTheComparatorsClass drives the upsert
// branch's two classes end to end, so the class an operator acts on is the one
// the comparator actually computed rather than a string another layer parsed.
func TestReprojection_DeclinedUpsertCarriesTheComparatorsClass(t *testing.T) {
	cases := []struct {
		name      string
		computed  map[string]any
		wantClass BlockedClass
	}{
		{
			name:      "only the freshness record moved",
			computed:  perEntryRow(perEntryParentKey, "read", 9),
			wantClass: BlockedProvenance,
		},
		{
			name:      "the granted role moved",
			computed:  perEntryRow(perEntryParentKey, "write", 7),
			wantClass: BlockedContent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adpt := &perKeyOutcomeAdapter{
				rows:          map[string]map[string]any{perEntryParentKey: perEntryRow(perEntryParentKey, "read", 7)},
				declineUpsert: true,
			}
			p := newReprojectPipeline(t, &adpt.recordingAdapter)
			p.adpt = adpt
			p.recordAppliedSeq(4242)
			writeLiveAnchor(t, p, reprojectActor)
			useAnchorRule(t, p)
			p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
				return tc.computed, map[string]any{"key": perEntryParentKey}, nil
			})

			res, err := p.Reproject(context.Background(), reprojectActor)
			require.NoError(t, err)

			require.Equal(t, VerdictBlocked, res.Verdict)
			require.Equal(t, tc.wantClass, res.BlockedClass)
		})
	}
}

// TestReprojection_TokenlessDropIsUnknownNotTheComparatorsClass pins the one
// place the comparator's evidence is deliberately NOT carried through. The
// token-less drop happens BEFORE any stored watermark is consulted, so this path
// never established that a guard conflict stands between the row and its repair;
// reporting the read-back's class here would name a condition the branch did not
// observe.
func TestReprojection_TokenlessDropIsUnknownNotTheComparatorsClass(t *testing.T) {
	adpt := &outcomeAdapter{
		recordingAdapter: recordingAdapter{
			stored:  perEntryRow(perEntryParentKey, "read", 7),
			present: true,
		},
		dropUpsertNoToken: true,
	}
	p := newOutcomePipeline(t, adpt)
	p.recordAppliedSeq(4242)
	writeLiveAnchor(t, p, reprojectActor)
	useAnchorRule(t, p)
	// A CONTENT divergence, which the comparator does classify — and which this
	// branch must still report as unknown.
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return perEntryRow(perEntryParentKey, "write", 7), map[string]any{"key": perEntryParentKey}, nil
	})

	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)

	require.Equal(t, VerdictBlocked, res.Verdict)
	require.Equal(t, BlockedUnknown, res.BlockedClass,
		"no watermark was consulted, so no guard conflict was observed to classify")
	require.Contains(t, res.VerdictReason, "no ordering token")
}

// mixedClassSweepAdapter poses one sweep pass holding all three blocked classes
// at once: two live anchors whose declined upserts differ in kind, and one
// orphan target key whose declined retraction leaves a row the graph says should
// not exist.
type mixedClassSweepAdapter struct {
	listingAdapter
	rows map[string]map[string]any
}

func (a *mixedClassSweepAdapter) GetRow(_ context.Context, keys map[string]any) (map[string]any, bool, error) {
	k, _ := keys["key"].(string)
	row, ok := a.rows[k]
	return row, ok, nil
}

func (a *mixedClassSweepAdapter) UpsertWithOutcome(_ context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	a.upserts = append(a.upserts, recordedWrite{keys: keys, row: row, seq: seq})
	return adapter.UpsertOutcome{Wrote: true, DeclinedByWatermark: true}, nil
}

func (a *mixedClassSweepAdapter) Upsert(_ context.Context, keys, row map[string]any, seq uint64) error {
	a.upserts = append(a.upserts, recordedWrite{keys: keys, row: row, seq: seq})
	return nil
}

func (a *mixedClassSweepAdapter) DeleteWithOutcome(_ context.Context, keys map[string]any, seq uint64) (adapter.DeleteOutcome, error) {
	a.deletes = append(a.deletes, recordedWrite{keys: keys, seq: seq})
	return adapter.DeleteOutcome{DeclinedByWatermark: true}, nil
}

func (a *mixedClassSweepAdapter) Delete(_ context.Context, keys map[string]any, seq uint64) error {
	a.deletes = append(a.deletes, recordedWrite{keys: keys, seq: seq})
	return nil
}

// writeSweepAnchor seeds a live Core KV anchor carrying the commit provenance
// the $projectedAt derivation needs, so a real rule can evaluate against it.
func writeSweepAnchor(t *testing.T, p *Pipeline, actorKey string) {
	t.Helper()
	body := map[string]any{
		"key":            actorKey,
		"class":          "identity",
		"data":           map[string]any{},
		"createdAt":      "2026-08-07T00:00:00Z",
		"lastModifiedAt": "2026-08-07T00:00:00Z",
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = p.coreKV.Put(context.Background(), actorKey, data)
	require.NoError(t, err)
}

// TestSweep_BlockedCensusIsPerClassAndGovernedByClassOrder is the sweep-level
// green bar. One pass holds a provenance-only blocked row, a content-divergence
// blocked row and a declined retraction, and the lens must report all three
// separately, sum them to the total, and name the RETRACTION as governing.
//
// The governing choice is the decisive part. Sorted by text the three reasons
// order content < provenance-only < retraction, so the previous lexicographic
// pick names the CONTENT one and a revoked grant left live never reaches the
// operator at all.
//
// Fails without the fix: BlockedByClass is nil, WorstBlockedClass is empty, and
// LastBlocked names the content divergence.
func TestSweep_BlockedCensusIsPerClassAndGovernedByClassOrder(t *testing.T) {
	driftKey, movedKey, orphanKey := sweepBuildKey(sweepActorA), sweepBuildKey(sweepActorB), sweepBuildKey(sweepActorC)
	adpt := &mixedClassSweepAdapter{
		listingAdapter: listingAdapter{keys: []string{driftKey, movedKey, orphanKey}},
		rows: map[string]map[string]any{
			driftKey:  perEntryRow(driftKey, "read", 7),
			movedKey:  perEntryRow(movedKey, "write", 7),
			orphanKey: perEntryRow(orphanKey, "read", 7),
		},
	}
	p := newSweepPipeline(t, &adpt.listingAdapter, 10)
	p.adpt = adpt
	p.recordAppliedSeq(4242)
	// A and B have live anchors, so the deep verify reprojects them and their
	// declined upserts carry the comparator's class. C has none, so it is an
	// orphan whose declined retraction is the over-grant direction.
	writeSweepAnchor(t, p, sweepActorA)
	writeSweepAnchor(t, p, sweepActorB)
	useAnchorRule(t, p)
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		actor, _ := row["anchor"].(string)
		key := sweepBuildKey(actor)
		// Both recomputed rows carry a fresher revision; only B's meaning moved.
		return perEntryRow(key, "read", 9), map[string]any{"key": key}, nil
	})

	sw := p.Sweeper()
	sw.pass(context.Background())

	st := sw.Status()
	require.Equal(t, 3, st.Blocked, "all three rows are unrepairable")
	require.Equal(t, 1, st.BlockedStreak, "the streak counts passes with a blocked row of ANY class")
	require.Equal(t, 1, st.BlockedByClass[BlockedProvenance])
	require.Equal(t, 1, st.BlockedByClass[BlockedContent])
	require.Equal(t, 1, st.BlockedByClass[BlockedRetraction])
	require.Zero(t, st.BlockedByClass[BlockedUnknown])

	total := 0
	for _, n := range st.BlockedByClass {
		total += n
	}
	require.Equal(t, st.Blocked, total, "every census is a premise: the per-class counts must sum to the total")

	require.Equal(t, "retraction", st.WorstBlockedClass,
		"a revoked grant left live outranks a content divergence, whatever the reasons sort like")
	require.Contains(t, st.LastBlocked, "retraction unrepairable",
		"the governing text must belong to the governing class")
	require.Zero(t, st.Reconciled, "nothing was healed, so nothing may be counted as healed")
}

// TestSweep_AProvenanceOnlySetGovernsAsProvenance is the discrimination twin: a
// lens whose whole blocked set is benign must SAY so, or the class is a label
// that only ever reads "worst" and pins nothing.
func TestSweep_AProvenanceOnlySetGovernsAsProvenance(t *testing.T) {
	driftKey, alsoDriftKey := sweepBuildKey(sweepActorA), sweepBuildKey(sweepActorB)
	adpt := &mixedClassSweepAdapter{
		listingAdapter: listingAdapter{keys: []string{driftKey, alsoDriftKey}},
		rows: map[string]map[string]any{
			driftKey:     perEntryRow(driftKey, "read", 7),
			alsoDriftKey: perEntryRow(alsoDriftKey, "read", 7),
		},
	}
	p := newSweepPipeline(t, &adpt.listingAdapter, 10)
	p.adpt = adpt
	p.recordAppliedSeq(4242)
	writeSweepAnchor(t, p, sweepActorA)
	writeSweepAnchor(t, p, sweepActorB)
	useAnchorRule(t, p)
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		actor, _ := row["anchor"].(string)
		key := sweepBuildKey(actor)
		return perEntryRow(key, "read", 9), map[string]any{"key": key}, nil
	})

	sw := p.Sweeper()
	sw.pass(context.Background())

	st := sw.Status()
	require.Equal(t, 2, st.Blocked)
	require.Equal(t, 2, st.BlockedByClass[BlockedProvenance])
	require.Equal(t, "provenance", st.WorstBlockedClass)
	require.Contains(t, st.LastBlocked, "provenance-only divergence")
}

// TestSweep_ACleanPassClearsTheCensus pins §3's lifetime table at the census:
// the per-class counts are DERIVED from the standing set at publish, so they
// depart with it rather than accumulating a history of their own.
func TestSweep_ACleanPassClearsTheCensus(t *testing.T) {
	orphanKey := sweepBuildKey(sweepActorC)
	adpt := &mixedClassSweepAdapter{
		listingAdapter: listingAdapter{keys: []string{orphanKey}},
		rows:           map[string]map[string]any{orphanKey: perEntryRow(orphanKey, "read", 7)},
	}
	p := newSweepPipeline(t, &adpt.listingAdapter, 10)
	p.adpt = adpt
	p.recordAppliedSeq(4242)

	sw := p.Sweeper()
	sw.pass(context.Background())
	require.Equal(t, 1, sw.Status().BlockedByClass[BlockedRetraction])
	require.Equal(t, "retraction", sw.Status().WorstBlockedClass)

	// The orphan leaves the corpus entirely — no anchor, no target key — so its
	// standing verdict departs with it (reapDepartedFailures).
	adpt.keys = nil
	sw.pass(context.Background())

	st := sw.Status()
	require.Zero(t, st.Blocked)
	require.Empty(t, st.BlockedByClass)
	require.Empty(t, st.WorstBlockedClass)
	require.Empty(t, st.LastBlocked)
}

// TestSweep_StatusSnapshotsDoNotShareACensusMap pins the one hazard a map field
// on a value-copied snapshot carries: Status() hands out a copy of the struct,
// which SHARES the map. A census mutated in place would rewrite a snapshot the
// heartbeat is already reading, so every publish builds a fresh one.
func TestSweep_StatusSnapshotsDoNotShareACensusMap(t *testing.T) {
	orphanKey := sweepBuildKey(sweepActorC)
	adpt := &mixedClassSweepAdapter{
		listingAdapter: listingAdapter{keys: []string{orphanKey}},
		rows:           map[string]map[string]any{orphanKey: perEntryRow(orphanKey, "read", 7)},
	}
	p := newSweepPipeline(t, &adpt.listingAdapter, 10)
	p.adpt = adpt
	p.recordAppliedSeq(4242)

	sw := p.Sweeper()
	sw.pass(context.Background())
	first := sw.Status()
	require.Equal(t, 1, first.BlockedByClass[BlockedRetraction])

	adpt.keys = nil
	sw.pass(context.Background())

	require.Equal(t, 1, first.BlockedByClass[BlockedRetraction],
		"an earlier snapshot must keep describing the pass it was taken from")
}
