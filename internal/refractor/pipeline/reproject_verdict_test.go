package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// outcomeAdapter is a recordingAdapter that reports write outcomes, so a test
// can drive the branch where the ordering guard accepts the call and declines
// the write — the state a real target only reaches by racing a CDC event
// against a reconciliation.
//
// It overrides BOTH the plain and the outcome-reporting form of each verb, per
// the Go-embedding trap adapter.OutcomeUpserter documents: overriding only one
// leaves the other promoted, and Reproject prefers the outcome form.
type outcomeAdapter struct {
	recordingAdapter
	declineUpsert bool
	declineDelete bool
	// dropUpsertNoToken reports the guard's OTHER no-commit exit: a write with
	// no ordering token, dropped before any stored watermark was consulted, so
	// it is not a watermark decline. adapter.NatsKVAdapter reports exactly this
	// shape — Wrote true, Committed false, DeclinedByWatermark false.
	dropUpsertNoToken bool
}

func (a *outcomeAdapter) upsertOnce(keys, row map[string]any, seq uint64) adapter.UpsertOutcome {
	a.upserts = append(a.upserts, recordedWrite{keys: keys, row: row, seq: seq})
	// Wrote stays true through every guarded no-commit branch, mirroring the
	// real guarded adapter: maintaining the watermark is its job on every call.
	// A double that tied Wrote to the outcome would be a fixture no caller can
	// go wrong against, and the branch under test is exactly the one a caller
	// gets wrong by reading Wrote.
	return adapter.UpsertOutcome{
		Wrote:               true,
		Committed:           !a.declineUpsert && !a.dropUpsertNoToken,
		DeclinedByWatermark: a.declineUpsert,
	}
}

func (a *outcomeAdapter) deleteOnce(keys map[string]any, seq uint64) adapter.DeleteOutcome {
	a.deletes = append(a.deletes, recordedWrite{keys: keys, seq: seq})
	return adapter.DeleteOutcome{Wrote: !a.declineDelete, DeclinedByWatermark: a.declineDelete}
}

func (a *outcomeAdapter) Upsert(_ context.Context, keys, row map[string]any, seq uint64) error {
	a.upsertOnce(keys, row, seq)
	return a.writeErr
}

func (a *outcomeAdapter) UpsertWithOutcome(_ context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	if a.writeErr != nil {
		return adapter.UpsertOutcome{}, a.writeErr
	}
	return a.upsertOnce(keys, row, seq), nil
}

func (a *outcomeAdapter) Delete(_ context.Context, keys map[string]any, seq uint64) error {
	a.deleteOnce(keys, seq)
	return a.writeErr
}

func (a *outcomeAdapter) DeleteWithOutcome(_ context.Context, keys map[string]any, seq uint64) (adapter.DeleteOutcome, error) {
	if a.writeErr != nil {
		return adapter.DeleteOutcome{}, a.writeErr
	}
	return a.deleteOnce(keys, seq), nil
}

func newOutcomePipeline(t *testing.T, adpt *outcomeAdapter) *Pipeline {
	t.Helper()
	p := newReprojectPipeline(t, &adpt.recordingAdapter)
	p.adpt = adpt
	return p
}

// TestReprojection_ZeroValueVerdictIsUnverified pins the fail-closed default by
// the TYPE rather than by review: a branch added later that forgets to conclude
// reports "I do not know", never "converged". Every prior version of this
// accounting collapsed toward health, which is the whole defect.
func TestReprojection_ZeroValueVerdictIsUnverified(t *testing.T) {
	var zero Reprojection
	require.Equal(t, VerdictUnverified, zero.Verdict)
	require.Equal(t, "unverified", zero.Verdict.String())
}

// TestVerdict_SeverityOrdering pins the worst-wins fold. One Reproject call can
// carry many results for one actor (a perEntry lens), so an actor with one
// blocked row and nine converged ones must not report converged.
func TestVerdict_SeverityOrdering(t *testing.T) {
	require.Greater(t, VerdictBlocked.severity(), VerdictUnverified.severity(),
		"a confirmed unrepairable row outranks not knowing")
	require.Greater(t, VerdictUnverified.severity(), VerdictHealed.severity(),
		"not knowing outranks a repaired divergence")
	require.Greater(t, VerdictHealed.severity(), VerdictConverged.severity())

	var f verdictFold
	f.add(VerdictConverged, "")
	f.add(VerdictHealed, "")
	f.add(VerdictBlocked, "the reason")
	f.add(VerdictConverged, "")
	v, reason := f.resolve("nothing concluded")
	require.Equal(t, VerdictBlocked, v, "the worst verdict survives later, quieter ones")
	require.Equal(t, "the reason", reason)
}

// TestVerdictFold_DefaultDoesNotSwallowRealConclusions pins the trap the
// severity order sets: VerdictUnverified is BOTH the fail-closed default and a
// verdict a result can reach, and it outranks Healed. Folding onto the zero
// value directly would let the default outrank every real conclusion after it,
// so a healed actor would report unverified forever.
func TestVerdictFold_DefaultDoesNotSwallowRealConclusions(t *testing.T) {
	var healed verdictFold
	healed.add(VerdictHealed, "")
	v, _ := healed.resolve("nothing concluded")
	require.Equal(t, VerdictHealed, v, "a single heal is a heal, not the unverified default")

	var empty verdictFold
	v, reason := empty.resolve("nothing concluded")
	require.Equal(t, VerdictUnverified, v, "nothing concluded fails closed")
	require.Equal(t, "nothing concluded", reason)

	// And a real unverified result still outranks a heal that followed it.
	var mixed verdictFold
	mixed.add(VerdictHealed, "")
	mixed.add(VerdictUnverified, "a real reason")
	v, reason = mixed.resolve("nothing concluded")
	require.Equal(t, VerdictUnverified, v)
	require.Equal(t, "a real reason", reason)
}

// TestReprojection_ReportsBlockedNotHealed is the fire's central assertion. The
// guard accepts the call and declines the write, returning nil — which the
// previous accounting booked as Wrote:true and the sweep logged as "healed a
// divergent projection", once per pass, forever, for a row it provably could
// not touch.
//
// Fails without the fix: with Reproject setting out.Wrote unconditionally, the
// verdict reads healed and Wrote is true.
func TestReprojection_ReportsBlockedNotHealed(t *testing.T) {
	adpt := &outcomeAdapter{
		recordingAdapter: recordingAdapter{
			stored:  map[string]any{"key": "cap.identity.x", "grant": "stale"},
			present: true,
		},
		declineDelete: true,
	}
	p := newOutcomePipeline(t, adpt)
	p.recordAppliedSeq(4242)

	// The actor is absent from Core KV, so reconciliation retracts the row —
	// and the guard declines the retraction at a tied watermark.
	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)

	require.Equal(t, VerdictBlocked, res.Verdict)
	require.False(t, res.Wrote, "a declined retraction healed nothing")
	require.False(t, res.Deleted, "the row is still there")
	require.False(t, res.Converged)
	require.Contains(t, res.VerdictReason, "stored watermark")
	require.Len(t, adpt.deletes, 1, "the write is still ATTEMPTED — only the report changes")
}

// TestReprojection_CommittedRetractionIsHealed is the negative twin: the same
// fixture with the guard committing must report a heal, so the test above
// cannot pass because the verdict is always blocked.
func TestReprojection_CommittedRetractionIsHealed(t *testing.T) {
	adpt := &outcomeAdapter{
		recordingAdapter: recordingAdapter{
			stored:  map[string]any{"key": "cap.identity.x"},
			present: true,
		},
	}
	p := newOutcomePipeline(t, adpt)
	p.recordAppliedSeq(4242)

	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)

	require.Equal(t, VerdictHealed, res.Verdict)
	require.True(t, res.Wrote)
	require.True(t, res.Deleted)
	require.Empty(t, res.VerdictReason)
}

// TestClassifyDivergence_ProvenanceIsDistinctFromContent pins the comparator
// requirement the held Contract #6 §6.2 amendment left behind. Provenance drift
// at a resting watermark is reachable by an ordinary operation — a lens-
// definition write that leaves the MATCH unchanged reprojects nothing yet
// diverges every row — whereas a CONTENT divergence at a tied token has no
// observed producer. Reporting both identically would either bury the real
// finding in provenance noise or alarm on every benign tick.
func TestClassifyDivergence_ProvenanceIsDistinctFromContent(t *testing.T) {
	base := map[string]any{
		"grant":                  "read",
		"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(7)},
	}

	t.Run("identical rows do not diverge", func(t *testing.T) {
		same := map[string]any{
			"grant":                  "read",
			"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(7)},
		}
		require.Equal(t, divergenceNone, classifyDivergence(base, same))
	})

	t.Run("only the freshness record moved", func(t *testing.T) {
		drifted := map[string]any{
			"grant":                  "read",
			"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(9)},
		}
		require.Equal(t, divergenceProvenance, classifyDivergence(base, drifted))
	})

	t.Run("the row's meaning moved", func(t *testing.T) {
		changed := map[string]any{
			"grant":                  "write",
			"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(7)},
		}
		require.Equal(t, divergenceContent, classifyDivergence(base, changed))
	})

	t.Run("both moved — content wins, the louder classification", func(t *testing.T) {
		both := map[string]any{
			"grant":                  "write",
			"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(9)},
		}
		require.Equal(t, divergenceContent, classifyDivergence(base, both))
	})

	t.Run("volatile projectedAt is still not a divergence", func(t *testing.T) {
		stamped := map[string]any{
			"grant":                  "read",
			"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(7)},
			"projectedAt":            "2026-08-07T00:00:00Z",
		}
		require.Equal(t, divergenceNone, classifyDivergence(base, stamped))
	})
}

// swapOnReadAdapter publishes a new rule the first time the pipeline reads a
// row back. That read happens inside multiEntryRetractions, which runs DURING
// reprojectActors — so the swap lands in the exact window the guard covers:
// after this call took its rule snapshot, before it reaches its write loop.
// This is the real race staged deterministically, not a hook bolted onto
// production code.
type swapOnReadAdapter struct {
	recordingAdapter
	swap  func()
	fired bool
}

func (a *swapOnReadAdapter) GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error) {
	if !a.fired && a.swap != nil {
		a.fired = true
		a.swap()
	}
	return a.recordingAdapter.GetRow(ctx, keys)
}

func (a *swapOnReadAdapter) ListKeysPrefix(context.Context, string) ([]map[string]any, error) {
	return nil, nil
}

// TestReproject_RefusesWriteUnderSupersededRule is the sweep-path sibling of
// TestWriteResults_SupersededRuleIsNakedNotWritten. Reproject takes the same
// coherent rule snapshot the CDC path does, but then writes through its own
// loop — so it never consulted supersededRule at all.
//
// The end state that refusal prevents: a MATCH reload swaps the rule
// synchronously and truncates on a NEW goroutine, so a still-running pass lands
// an old-rule row into the emptied target — where the absent-key branch takes
// it unconditionally — stamped at the consumer HEAD, which outranks every
// sequence the rebuild is about to replay. The rebuild is then locked out of
// the key it was racing, and if the MATCH edit was a revocation the frozen row
// is the pre-edit permission set.
//
// Fails without the fix: the old-rule delete lands (one recorded delete) and
// the call returns a nil error.
func TestReproject_RefusesWriteUnderSupersededRule(t *testing.T) {
	eng := full.New()
	crA, err := eng.Parse(raceSwapSpecA)
	require.NoError(t, err)
	crB, err := eng.Parse(raceSwapSpecB)
	require.NoError(t, err)

	adpt := &swapOnReadAdapter{
		recordingAdapter: recordingAdapter{
			stored:  map[string]any{"key": "cap.identity.x"},
			present: true,
		},
	}
	p := newReprojectPipeline(t, &adpt.recordingAdapter)
	p.adpt = adpt
	p.recordAppliedSeq(4242)
	p.SetMultiEnvelopeFn(func(row, keys, params map[string]any) ([]Envelope, error) {
		return nil, nil
	})
	p.UseFullEngine(eng, crA)

	// The hot-reload lands mid-evaluation, on the pipeline's own read.
	adpt.swap = func() { p.UseFullEngineBranches(eng, crB, nil) }

	_, rerr := p.Reproject(context.Background(), reprojectActor)

	require.ErrorIs(t, rerr, ErrRuleSuperseded)
	require.Empty(t, adpt.deletes, "no write derived from a replaced rule may reach the target")
	require.Empty(t, adpt.upserts)

	// The next attempt — snapshot taken after the swap has settled — writes.
	adpt.swap = nil
	res, rerr := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, rerr)
	require.Equal(t, VerdictHealed, res.Verdict)
	require.Len(t, adpt.deletes, 1, "the re-evaluation under the current rule writes")
}

// useAnchorRule installs a minimal identity-anchored rule so a LIVE anchor
// actually evaluates. newReprojectPipeline's empty CompiledRule only carries
// the missing-actor branch, which never reaches the engine.
func useAnchorRule(t *testing.T, p *Pipeline) {
	t.Helper()
	useRule(t, p, `MATCH (identity:identity {key: $actorKey}) RETURN identity.key AS anchor`)
}

// useFilteringAnchorRule installs a rule whose WHERE excludes the live anchor,
// so the evaluation legitimately yields ZERO rows for a vertex that exists.
// That is the doc-mode empty-behaviour shape — not a missing actor, which takes
// the retraction branch instead.
func useFilteringAnchorRule(t *testing.T, p *Pipeline) {
	t.Helper()
	useRule(t, p, `MATCH (identity:identity {key: $actorKey}) `+
		`WHERE identity.class = 'a-class-no-vertex-carries' RETURN identity.key AS anchor`)
}

func useRule(t *testing.T, p *Pipeline, spec string) {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
}

// writeLiveAnchor seeds a Core KV anchor vertex carrying commit provenance, so
// the evaluation's $projectedAt derivation resolves. The sweep tests' own
// writeAnchor omits it because their lenses never reach that derivation.
func writeLiveAnchor(t *testing.T, p *Pipeline, actorKey string) {
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

// TestReprojection_BlockedDistinguishesProvenanceFromContent drives the UPSERT
// declined branch — the one that renders the divergence class into the reason —
// end to end, rather than testing classifyDivergence in isolation.
//
// The distinction is what the held Contract #6 §6.2 amendment left behind:
// provenance drift at a resting watermark is reachable by an ordinary operation
// (a lens-definition write that leaves the MATCH unchanged reprojects nothing
// yet diverges every row), so alarming on it identically to a content
// divergence would either bury the real finding in noise or raise on every
// benign tick.
func TestReprojection_BlockedDistinguishesProvenanceFromContent(t *testing.T) {
	storedRow := map[string]any{
		"key":                    "cap.identity.x",
		"roles":                  []any{"admin"},
		"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(7)},
	}

	cases := []struct {
		name     string
		computed map[string]any
		wantIn   string
	}{
		{
			name: "only the freshness record moved",
			computed: map[string]any{
				"key":                    "cap.identity.x",
				"roles":                  []any{"admin"},
				"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(9)},
			},
			wantIn: "provenance-only divergence",
		},
		{
			name: "the granted roles moved",
			computed: map[string]any{
				"key":                    "cap.identity.x",
				"roles":                  []any{},
				"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(7)},
			},
			wantIn: "content divergence",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adpt := &outcomeAdapter{
				recordingAdapter: recordingAdapter{stored: storedRow, present: true},
				declineUpsert:    true,
			}
			p := newOutcomePipeline(t, adpt)
			p.recordAppliedSeq(4242)
			// A live actor whose recomputed row is tc.computed: the envelope
			// substitutes it, so the reconciler reads back storedRow, sees a
			// divergence, writes, and the guard declines at the tie.
			writeLiveAnchor(t, p, reprojectActor)
			useAnchorRule(t, p)
			p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
				return tc.computed, map[string]any{"key": "cap.identity.x"}, nil
			})

			res, err := p.Reproject(context.Background(), reprojectActor)
			require.NoError(t, err)

			require.Equal(t, VerdictBlocked, res.Verdict)
			require.False(t, res.Wrote, "the guard declined, so nothing was repaired")
			require.False(t, res.Converged, "a blocked actor is not a converged one")
			require.Contains(t, res.VerdictReason, tc.wantIn,
				"the reason must name WHICH KIND of divergence went unrepaired")
			require.Len(t, adpt.upserts, 1, "the write is still attempted")
		})
	}
}

// TestReprojection_TokenlessDropIsBlockedNotHealed covers the guard's second
// no-commit exit: a write dropped for want of an ordering token, which is not a
// watermark decline and so passes the branch above.
//
// The reconciler reads Committed rather than Wrote for this reason. Wrote stays
// true through every guarded no-commit branch — maintaining the watermark is
// that path's job on every call — so a reconciler reading it books a repair
// that stored nothing, on the surface where that reads as a divergent grant row
// having been fixed while it sits exactly as it was.
//
// No shipped adapter pairing reaches this through Reproject today, and the test
// says so rather than implying coverage it does not have: the token-less drop
// belongs to adapter.NatsKVAdapter, which also implements adapter.RowReader, so
// a seq-0 reconciliation against it is refused by the ErrNoOrderingToken check
// upstream (reproject.go, inside the canRead block) before any write is
// attempted. The Postgres family, which is what makes canRead false, declines
// by watermark instead and never reports this shape. The branch is therefore
// correct-by-construction rather than currently-exercised, and it is pinned at
// the level that does reach it — the outcome an adapter reports.
func TestReprojection_TokenlessDropIsBlockedNotHealed(t *testing.T) {
	storedRow := map[string]any{
		"key":                    "cap.identity.x",
		"roles":                  []any{"admin"},
		"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(7)},
	}
	adpt := &outcomeAdapter{
		recordingAdapter:  recordingAdapter{stored: storedRow, present: true},
		dropUpsertNoToken: true,
	}
	p := newOutcomePipeline(t, adpt)
	p.recordAppliedSeq(4242)
	writeLiveAnchor(t, p, reprojectActor)
	useAnchorRule(t, p)
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return map[string]any{
			"key":                    "cap.identity.x",
			"roles":                  []any{},
			"projectedFromRevisions": map[string]any{"vtx.identity.x": float64(7)},
		}, map[string]any{"key": "cap.identity.x"}, nil
	})

	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)

	require.Equal(t, VerdictBlocked, res.Verdict,
		"a write that stored nothing repaired nothing, whatever Wrote says")
	require.False(t, res.Wrote, "nothing landed, so nothing may be booked as repaired")
	require.False(t, res.Converged)
	require.Contains(t, res.VerdictReason, "no ordering token",
		"the reason must name the missing token, not a watermark conflict — different fault, different fix")
	require.NotContains(t, res.VerdictReason, "stored watermark",
		"reusing the watermark wording would misreport the cause")
	require.Len(t, adpt.upserts, 1, "the write is still attempted")
}

// TestReproject_ZeroRowsWithoutRetractionTransportIsUnverified is the
// regression the design names as "the test that would have caught the
// incident": twelve orphanedTaskGrants rows sat stale for twelve days while the
// lens card rendered green, because an anchor whose cypher returned zero rows
// produced no write, and no write meant no heal, no error, and a cleared
// divergent streak.
//
// The armed twin is what proves the counter DISCRIMINATES rather than merely
// fires: the same fixture with zero-row retraction armed must converge.
func TestReproject_ZeroRowsWithoutRetractionTransportIsUnverified(t *testing.T) {
	newZeroRowPipeline := func(t *testing.T, armed bool) (*Pipeline, *recordingAdapter) {
		t.Helper()
		adpt := &recordingAdapter{present: false}
		p := newReprojectPipeline(t, adpt)
		p.recordAppliedSeq(4242)
		writeLiveAnchor(t, p, reprojectActor)
		useFilteringAnchorRule(t, p)
		p.SetActorDeleteKey(func(actorKey string) string { return "cap.identity.x" })
		p.SetZeroRowRetraction(armed)
		return p, adpt
	}

	t.Run("disarmed: the silence is reported, not read as convergence", func(t *testing.T) {
		p, adpt := newZeroRowPipeline(t, false)

		res, err := p.Reproject(context.Background(), reprojectActor)
		require.NoError(t, err)

		require.Equal(t, VerdictUnverified, res.Verdict,
			"zero rows with no retraction transport proves nothing about the stored row")
		require.False(t, res.Converged, "this is exactly the reading that hid the incident")
		require.False(t, res.Wrote)
		require.Contains(t, res.VerdictReason, "no retraction transport")
		require.Empty(t, adpt.upserts)
		require.Empty(t, adpt.deletes)
	})

	t.Run("armed: absence is proven, so the actor converges", func(t *testing.T) {
		p, _ := newZeroRowPipeline(t, true)

		res, err := p.Reproject(context.Background(), reprojectActor)
		require.NoError(t, err)

		require.Equal(t, VerdictConverged, res.Verdict,
			"the counter must discriminate, not fire on every zero-row actor")
		require.True(t, res.Converged)
		require.Empty(t, res.VerdictReason)
	})
}
