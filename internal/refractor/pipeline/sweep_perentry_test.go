package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// perEntryBuildKey / perEntryAnchorFromKey mirror a §4.1 perEntry descriptor's
// key shape (cap-read.roles.<type>.<id>.<entryId>) — the cap-read-per-anchor
// grant-keys design's own family, distinct from the doc-mode
// sweepBuildKey/sweepAnchorFromKey pair above: a perEntry actor owns MANY
// target keys, never one, so BuildKey alone is only ever a prefix, never a
// real key.
func perEntryBuildKey(actorKey string) string {
	return "cap-read.roles." + strings.TrimPrefix(actorKey, "vtx.")
}

func perEntryKey(actorKey, entryID string) string {
	return perEntryBuildKey(actorKey) + "." + entryID
}

func perEntryAnchorFromKey(targetKey string) (string, bool) {
	rest, ok := strings.CutPrefix(targetKey, "cap-read.roles.")
	if !ok {
		return "", false
	}
	idx := strings.LastIndexByte(rest, '.')
	if idx < 0 {
		return "", false
	}
	actorPart, entryID := rest[:idx], rest[idx+1:]
	if !substrate.IsValidNanoID(entryID) {
		return "", false
	}
	actorKey := "vtx." + actorPart
	vtxType, _, parsed := substrate.ParseVertexKey(actorKey)
	if !parsed || vtxType != "identity" {
		return "", false
	}
	return actorKey, true
}

const (
	perEntryIDOne = "Entr1AaaaaaaaaaaaaaZ"
	perEntryIDTwo = "Entr2BbbbbbbbbbbbbbZ"
)

func newPerEntrySweepPipeline(t *testing.T, adpt *listingAdapter, batch int) *Pipeline {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	p := &Pipeline{
		ruleID:          "sweep-rule-perentry",
		coreKV:          coreKV,
		adjKV:           adjKV,
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
		adpt:            adpt,
	}
	p.SetActorDeleteKey(perEntryBuildKey)
	p.SetSweepPlan(SweepPlan{
		AnchorType:    "identity",
		AnchorFromKey: perEntryAnchorFromKey,
		KeyPrefix:     "cap-read.roles.",
		Interval:      time.Hour,
		Batch:         batch,
	})
	return p
}

func TestSweepCandidates_PerEntry_LiveActorWithChildKeysIsNotDivergent(t *testing.T) {
	// A perEntry actor's coverage presence test is "≥1 target key under its
	// prefix" (§4.4), not "one key equal to BuildKey(actor)" — no real key is
	// ever that alone. An actor with two live grant keys must not be picked as
	// a coverage divergence.
	adpt := &listingAdapter{keys: []string{
		perEntryKey(sweepActorA, perEntryIDOne),
		perEntryKey(sweepActorA, perEntryIDTwo),
	}}
	p := newPerEntrySweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{sweepActorA}, anchors)

	sel := p.Sweeper().candidates(context.Background(), anchors, targets)
	require.Empty(t, sel.fromCoverage,
		"an actor with live child keys must not read as a coverage divergence")
}

func TestSweepCandidates_PerEntry_LiveActorWithNoChildKeyIsDivergent(t *testing.T) {
	// The mirror case: a live actor with zero child keys under its prefix is
	// exactly the divergence the coverage direction exists to catch, perEntry
	// or not.
	adpt := &listingAdapter{}
	p := newPerEntrySweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)

	sel := p.Sweeper().candidates(context.Background(), anchors, targets)
	require.Equal(t, []string{sweepActorA}, sel.actors)
	require.Contains(t, sel.fromCoverage, sweepActorA,
		"a live, childless actor must be picked via the coverage direction specifically, "+
			"not merely land in the batch through the round-robin deep verify")
}

func TestSweepCandidates_PerEntry_LiveActorsChildKeysNeverFloodTheOrphanSetWhileADepartedActorsDo(t *testing.T) {
	// The bug named in §4.4: under perEntry, BuildKey(actor) is a prefix equal
	// to no real key, so a naive "key ∉ expected" test would call every live
	// child key of every live actor an orphan. Mixing live and departed actors
	// under the SAME survey — both carrying structurally identical perEntry
	// child keys — proves the orphan direction discriminates by the actor's
	// LIVENESS, not incidentally by a coverage pass claiming the same actor
	// via seen-dedup first: the coverage hint is floored to a share of exactly
	// 1 BEFORE this tick, so a naive coverage probe (one that, like the old
	// exact-match test, wrongly treats every perEntry actor as uncovered)
	// could claim at most ONE of the two live actors this tick — leaving the
	// other exposed to the orphan direction with nothing but the orphan
	// classification itself to keep it out of fromOrphan.
	adpt := &listingAdapter{keys: []string{
		perEntryKey(sweepActorA, perEntryIDOne),
		perEntryKey(sweepActorA, perEntryIDTwo),
		perEntryKey(sweepActorB, perEntryIDOne),
		perEntryKey(sweepActorC, perEntryIDOne), // sweepActorC has departed — no writeAnchor call
	}}
	p := newPerEntrySweepPipeline(t, adpt, 5) // batch=5 -> quota=1, so a floored coverage cap seats only one of {A,B}
	writeAnchor(t, p, sweepActorA, false)
	writeAnchor(t, p, sweepActorB, false)
	sw := p.Sweeper()
	floorHint(t, sw, "coverage", &sw.coverage)

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{sweepActorA, sweepActorB}, anchors)

	sel := sw.candidates(context.Background(), anchors, targets)
	require.Empty(t, sel.fromCoverage,
		"both live actors carry live child keys, so neither is a coverage divergence "+
			"regardless of the floored cap — the presence test must never even reach it")
	require.Equal(t, map[string]struct{}{sweepActorC: {}}, sel.fromOrphan,
		"only the departed actor's child key is an orphan; a live actor's own child "+
			"keys must never be claimed as one, even when the coverage direction's "+
			"floored cap could not have absorbed both live actors first")
}

func TestSweepCandidates_PerEntry_ADepartedActorsChildKeysAreOrphanedOnce(t *testing.T) {
	// A dead actor's child keys are the real over-grant case — every one of
	// them must retract, but the actor (not the key) is the reprojection unit,
	// so a departed actor with two stale child keys is exactly one candidate,
	// deduplicated.
	adpt := &listingAdapter{keys: []string{
		perEntryKey(sweepActorC, perEntryIDOne),
		perEntryKey(sweepActorC, perEntryIDTwo),
	}}
	p := newPerEntrySweepPipeline(t, adpt, 10)
	// sweepActorC never gets a Core KV vertex: it has departed the graph
	// entirely, the ordinary orphan shape.

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.Empty(t, anchors)

	got := p.Sweeper().candidates(context.Background(), anchors, targets).actors
	require.Equal(t, []string{sweepActorC}, got,
		"two stale child keys must dedupe to one reprojection candidate")
}

func TestSweepCandidates_PerEntry_ForeignKeysInASharedBucketAreNotClaimed(t *testing.T) {
	// The shared-bucket exactness guarantee must survive the perEntry parse: a
	// row sharing this lens's own prefix but a different anchor type, and a
	// malformed trailing entry token, must not be misread as this lens's own
	// child key.
	adpt := &listingAdapter{keys: []string{
		"cap-read.roles.service." + strings.TrimPrefix(sweepActorC, "vtx.identity.") + "." + perEntryIDOne, // right prefix, wrong anchor type
		perEntryBuildKey(sweepActorC) + ".not-a-nanoid",                                                    // malformed entry token
	}}
	p := newPerEntrySweepPipeline(t, adpt, 10)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.Empty(t, anchors)

	got := p.Sweeper().candidates(context.Background(), anchors, targets).actors
	require.Empty(t, got, "neither foreign key must be claimed as an orphan")
}
