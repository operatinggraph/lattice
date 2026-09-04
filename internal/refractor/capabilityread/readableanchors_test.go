package capabilityread_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/capabilityread"
)

// grantLayout is one seeded per-anchor key: which actor holds it, which
// domain wrote it ("" = the base lens, which omits the domain segment), which
// anchor it names, and whether it is soft-tombstoned.
type grantLayout struct {
	actorID  string
	domain   string
	anchorID string
	deleted  bool
}

func (g grantLayout) key() string {
	if g.domain == "" {
		return "cap-read.identity." + g.actorID + "." + g.anchorID
	}
	return "cap-read." + g.domain + ".identity." + g.actorID + "." + g.anchorID
}

// TestReadableAnchors_EqualsIsReadable_OverLayouts is the equivalence pin the
// whole optimization rests on: the set answers the same admission IsReadable
// does, for EVERY candidate anchor, over grant layouts that exercise each way
// the two key shapes can combine — base only, one domain, two domains, base
// tombstoned with a domain live, every key tombstoned, and an anchor with no
// key at all. A random layout over the same alphabet is folded in so the
// table's own choices are not the whole census.
//
// It is written as an equality assertion rather than a list of expected
// verdicts on purpose: IsReadable is the shipped security boundary, so the
// property that matters is that the set never disagrees with it — in either
// direction.
func TestReadableAnchors_EqualsIsReadable_OverLayouts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)
	ctx := context.Background()

	const (
		actorA = "Ax7kPmRtw9nbCxz5vQ2y"
		actorB = "Bx7kPmRtw9nbCxz5vQ2y"
	)
	actors := []string{actorA, actorB}

	layouts := []grantLayout{
		// Base key only, live and tombstoned.
		{actorA, "", "anchorBaseLive", false},
		{actorA, "", "anchorBaseDead", true},
		// Domain keys, one and two domains over the same anchor.
		{actorA, "residence", "anchorDomainLive", false},
		{actorA, "residence", "anchorTwoDomains", false},
		{actorA, "clinic", "anchorTwoDomains", true},
		{actorA, "clinic", "anchorDomainDead", true},
		// The union arms: one live source is enough, whichever it is.
		{actorA, "", "anchorBaseDeadDomainLive", true},
		{actorA, "residence", "anchorBaseDeadDomainLive", false},
		{actorA, "", "anchorBaseLiveDomainDead", false},
		{actorA, "residence", "anchorBaseLiveDomainDead", true},
		// Every source tombstoned across both shapes.
		{actorA, "", "anchorAllDead", true},
		{actorA, "residence", "anchorAllDead", true},
		{actorA, "clinic", "anchorAllDead", true},
		// A second actor holding overlapping anchor ids.
		{actorB, "", "anchorBaseLive", false},
		{actorB, "clinic", "anchorOnlyB", false},
		{actorB, "", "anchorBaseDeadDomainLive", true},
	}

	// A random layout over the same alphabet, so the table's hand-picked
	// combinations are not the only population the equivalence is proved on.
	rng := rand.New(rand.NewSource(20260903))
	domains := []string{"", "residence", "clinic", "cafe"}
	for i := 0; i < 40; i++ {
		layouts = append(layouts, grantLayout{
			actorID:  actors[rng.Intn(len(actors))],
			domain:   domains[rng.Intn(len(domains))],
			anchorID: fmt.Sprintf("anchorRand%02d", rng.Intn(12)),
			deleted:  rng.Intn(3) == 0,
		})
	}

	candidates := map[string]struct{}{
		// Anchors nothing ever wrote a key for.
		"anchorNeverGranted": {},
		"anchorRand99":       {},
	}
	for _, l := range layouts {
		putPerAnchorEntry(t, kv, l.key(), l.deleted)
		candidates[l.anchorID] = struct{}{}
	}

	for _, actorID := range actors {
		set, err := capabilityread.ReadableAnchors(ctx, kv, "identity", actorID)
		require.NoError(t, err)
		for anchorID := range candidates {
			want, err := capabilityread.IsReadable(ctx, kv, "identity", actorID, anchorID)
			require.NoError(t, err)
			require.Equal(t, want, set.Admits(anchorID),
				"actor %q anchor %q: the anchor set must answer exactly what IsReadable answers", actorID, anchorID)
		}
	}

	// The TWO places the two readers deliberately DISAGREE. Both are
	// fail-closed — the set denies where IsReadable raises — and both are
	// pinned here rather than left implicit, because "identical membership"
	// is the contract the whole optimization rests on and an unstated
	// exception is how such a contract stops being true.
	t.Run("asymmetry: an unparseable body contributes nothing where IsReadable errors", func(t *testing.T) {
		const (
			corrupt      = "anchorCorruptBody"
			corruptRescd = "anchorCorruptBaseDomainLive"
		)
		_, perr := kv.Put(ctx, "cap-read.identity."+actorA+"."+corrupt, []byte("not-json"))
		require.NoError(t, perr, "a raw Put stores genuinely malformed bytes")
		// The same anchor with a corrupt BASE key and a live DOMAIN key: the
		// union still has a live source, so the set admits while the per-row
		// read never gets past the base key it cannot parse.
		_, perr = kv.Put(ctx, "cap-read.identity."+actorA+"."+corruptRescd, []byte("not-json"))
		require.NoError(t, perr)
		putPerAnchorEntry(t, kv, "cap-read.residence.identity."+actorA+"."+corruptRescd, false)

		_, ierr := capabilityread.IsReadable(ctx, kv, "identity", actorA, corrupt)
		require.Error(t, ierr, "the per-anchor gate errors: its read is scoped to this one anchor")

		set, serr := capabilityread.ReadableAnchors(ctx, kv, "identity", actorA)
		require.NoError(t, serr,
			"the whole-actor read must NOT error: one corrupt key would otherwise wedge every evaluation of the actor")
		require.False(t, set.Admits(corrupt), "with no other key for the anchor, an unparseable body denies")
		require.True(t, set.Admits("anchorBaseLive"), "every other anchor the actor holds is unaffected")

		// The third asymmetry, and the only one where the set is the more
		// permissive of the two: a corrupt body contributes nothing rather
		// than poisoning the anchor, so a live sibling key still grants —
		// which is the same answer the union rule gives for a TOMBSTONED base
		// key beside a live domain one. The per-row read cannot reach that
		// answer because it errors on the base key first, so the set is more
		// permissive only where IsReadable returns no verdict at all, never
		// where it returns a denial.
		_, ierr = capabilityread.IsReadable(ctx, kv, "identity", actorA, corruptRescd)
		require.Error(t, ierr, "IsReadable errors on the unparseable base key before it ever reads the live domain key")
		require.True(t, set.Admits(corruptRescd),
			"a live domain key still grants: the corrupt base key contributes nothing, it does not veto")
	})

	t.Run("asymmetry: a metacharacter anchorID denies where IsReadable errors", func(t *testing.T) {
		set, serr := capabilityread.ReadableAnchors(ctx, kv, "identity", actorA)
		require.NoError(t, serr)
		for _, bad := range []string{"a.b", "a*b", "a>b"} {
			_, ierr := capabilityread.IsReadable(ctx, kv, "identity", actorA, bad)
			require.Error(t, ierr, "IsReadable refuses %q loudly: it would template the string into a filter", bad)
			require.False(t, set.Admits(bad),
				"Admits denies %q: a stored anchor segment is one key token, so such a string is never a member", bad)
		}
	})
}

// TestReadableAnchors_DoesNotLeakAcrossActors mirrors
// TestIsReadable_DoesNotLeakAcrossActors for the whole-actor read: both
// filters pin the actor suffix as a literal token pair, so a sibling actor's
// keys — base or domain — are never matched.
func TestReadableAnchors_DoesNotLeakAcrossActors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)
	putPerAnchorEntry(t, kv, "cap-read.identity.B1.unitNanoB", false)
	putPerAnchorEntry(t, kv, "cap-read.residence.identity.B1.leaseNanoB", false)
	putPerAnchorEntry(t, kv, "cap-read.identity.A1.unitNanoA", false)
	putPerAnchorEntry(t, kv, "cap-read.service.A1.unitNanoService", false)

	set, err := capabilityread.ReadableAnchors(context.Background(), kv, "identity", "A1")
	require.NoError(t, err)
	require.True(t, set.Admits("unitNanoA"), "the actor's own base grant must be admitted")
	require.False(t, set.Admits("unitNanoB"), "actor A1 must not inherit actor B1's base grant")
	require.False(t, set.Admits("leaseNanoB"), "actor A1 must not inherit actor B1's domain grant")
	require.False(t, set.Admits("unitNanoService"),
		"a base grant to a DIFFERENT actor type sharing this id must not be read as this actor's grant")
}

// TestReadableAnchors_TombstonedExcluded pins the soft-tombstone rule on its
// own, in both key shapes: a revoked grant that is still a live KV entry must
// never be admitted.
func TestReadableAnchors_TombstonedExcluded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)
	putPerAnchorEntry(t, kv, "cap-read.identity.A1.baseDead", true)
	putPerAnchorEntry(t, kv, "cap-read.residence.identity.A1.domainDead", true)
	putPerAnchorEntry(t, kv, "cap-read.identity.A1.baseLive", false)

	set, err := capabilityread.ReadableAnchors(context.Background(), kv, "identity", "A1")
	require.NoError(t, err)
	require.False(t, set.Admits("baseDead"), "a soft-tombstoned base key must be treated as absent")
	require.False(t, set.Admits("domainDead"), "a soft-tombstoned domain key must be treated as absent")
	require.True(t, set.Admits("baseLive"))
	require.False(t, set.Admits("neverWritten"), "an anchor with no key at all must be denied")
}

func TestReadableAnchors_RejectsEmptyActorFields(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)

	_, err := capabilityread.ReadableAnchors(context.Background(), kv, "", "A1")
	require.Error(t, err, "empty actorType must be rejected, not silently denied")

	_, err = capabilityread.ReadableAnchors(context.Background(), kv, "identity", "")
	require.Error(t, err, "empty actorID must be rejected, not silently denied")
}

func TestReadableAnchors_RejectsMetacharacters(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)

	for _, bad := range []string{"a.b", "a*b", "a>b"} {
		_, err := capabilityread.ReadableAnchors(context.Background(), kv, bad, "A1")
		require.Error(t, err, "actorType %q containing a NATS subject metacharacter must be rejected", bad)

		_, err = capabilityread.ReadableAnchors(context.Background(), kv, "identity", bad)
		require.Error(t, err, "actorID %q containing a NATS subject metacharacter must be rejected", bad)
	}
}

// TestReadableAnchors_MalformedBodyDeniesOnlyThatAnchor pins the blast radius
// of a corrupt grant key. The whole-actor read touches every key the actor
// holds, so erroring on one unparseable body would fail the actor's whole
// evaluation — identically on every redelivery, since the corruption is
// durable — and a lens wedged behind one bad key is a worse outcome than the
// single missing grant. The anchor is simply not admitted (it can only
// under-grant) and the key is named at Warn.
func TestReadableAnchors_MalformedBodyDeniesOnlyThatAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)
	ctx := context.Background()
	putPerAnchorEntry(t, kv, "cap-read.identity.A1.goodBase", false)
	putPerAnchorEntry(t, kv, "cap-read.residence.identity.A1.goodDomain", false)
	_, err := kv.Put(ctx, "cap-read.identity.A1.corruptBase", []byte("not-json"))
	require.NoError(t, err, "raw Put bypasses putPerAnchorEntry's marshal so the stored bytes are genuinely malformed")
	_, err = kv.Put(ctx, "cap-read.clinic.identity.A1.corruptDomain", []byte("{"))
	require.NoError(t, err)

	set, err := capabilityread.ReadableAnchors(ctx, kv, "identity", "A1")
	require.NoError(t, err, "one corrupt key must not fail the whole actor's read")
	require.True(t, set.Admits("goodBase"), "an unrelated live base grant must survive a sibling's corruption")
	require.True(t, set.Admits("goodDomain"), "an unrelated live domain grant must survive it too")
	require.False(t, set.Admits("corruptBase"), "an unparseable base key must not be admitted")
	require.False(t, set.Admits("corruptDomain"), "an unparseable domain key must not be admitted")
}

// TestReadableAnchors_CorruptDoesNotResurrectATombstone pins the direction of
// the corrupt-body rule where it could go wrong: an anchor whose ONLY live
// key is corrupt, and whose other keys are tombstoned, stays denied — the
// unparseable body is skipped, never read as a live grant.
func TestReadableAnchors_CorruptDoesNotResurrectATombstone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)
	ctx := context.Background()
	putPerAnchorEntry(t, kv, "cap-read.identity.A1.revoked", true)
	_, err := kv.Put(ctx, "cap-read.residence.identity.A1.revoked", []byte("not-json"))
	require.NoError(t, err)

	set, err := capabilityread.ReadableAnchors(ctx, kv, "identity", "A1")
	require.NoError(t, err)
	require.False(t, set.Admits("revoked"), "a corrupt body must never stand in for a live grant")
}

// TestReadableAnchors_KVFailurePropagates pins the fail-closed error arm: a
// KV failure (here an already-canceled context) must surface, never read as
// an empty grant set — an empty set denies every row silently.
func TestReadableAnchors_KVFailurePropagates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	set, err := capabilityread.ReadableAnchors(ctx, kv, "identity", "A1")
	require.Error(t, err, "a KV failure must error, not silently deny as if the actor holds no grants")
	require.Nil(t, set)
	require.Contains(t, err.Error(), "capabilityread:")
}

// TestAnchorSet_NilAdmitsNothing pins the zero value's posture — a caller
// holding no set must not be able to read one as admitting everything.
func TestAnchorSet_NilAdmitsNothing(t *testing.T) {
	var set *capabilityread.AnchorSet
	require.False(t, set.Admits("anyAnchor"))
	require.False(t, set.Admits(""))
	require.False(t, (&capabilityread.AnchorSet{}).Admits("anyAnchor"))
}

// nanoIDFor builds a deterministic, valid 20-character Contract #1 NanoID for
// index i. The alphabet excludes I, l, O and 0, so the counter is written in
// digits 1-9 and every stem here carries none of the four.
func nanoIDFor(stem string, i int) string {
	digits := make([]byte, 5) // 9^5 = 59,049 distinct ids, injective for every i used here
	n := i
	for d := 4; d >= 0; d-- {
		digits[d] = byte('1' + n%9)
		n /= 9
	}
	id := stem + string(digits)
	for len(id) < 20 {
		id += "a"
	}
	return id[:20]
}

// classKey is one grant key an anchorClass writes: which domain wrote it
// ("" = the base lens, which omits the domain segment) and whether it is
// soft-tombstoned.
type classKey struct {
	domain  string
	deleted bool
}

// anchorClass is one seeded shape of grant keys for a single anchor — every
// combination the union rule has to resolve, with the verdict a sample is
// checked against.
type anchorClass struct {
	name string
	keys []classKey
	live bool
}

var pastCapClasses = []anchorClass{
	{name: "baseLive", keys: []classKey{{"", false}}, live: true},
	{name: "baseDead", keys: []classKey{{"", true}}, live: false},
	{name: "domainLive", keys: []classKey{{"residence", false}}, live: true},
	{name: "domainDead", keys: []classKey{{"residence", true}}, live: false},
	{name: "baseDeadDomainLive", keys: []classKey{{"", true}, {"residence", false}}, live: true},
	{name: "baseLiveDomainDead", keys: []classKey{{"", false}, {"clinic", true}}, live: true},
	{name: "allDead", keys: []classKey{{"", true}, {"residence", true}, {"clinic", true}}, live: false},
}

// grantKey spells one per-anchor grant key in whichever of the two shapes the
// domain selects.
func grantKey(domain, actorSuffix, anchorID string) string {
	if domain == "" {
		return "cap-read." + actorSuffix + "." + anchorID
	}
	return "cap-read." + domain + "." + actorSuffix + "." + anchorID
}

// TestReadableAnchors_PastTheFastPathCap_ResolvesThenChunks exercises the
// only shape production ever runs. The widest live actor holds ~3,644 grant
// keys, so the multi-get's 1,024-matched-subject fast path always answers
// 413 and the read falls through to the resolve-then-get fallback: each of
// ReadableAnchors' TWO wildcards is resolved to exact keys against the
// stream's subject state, the two resolved sets are unioned, and the union
// is read in atomic chunks. A fixture that stays under the cap proves
// nothing about either the two filters resolving together or the fallback's
// completeness, so this one seeds past it.
//
// Three things are asserted at once: the two filters are accepted together,
// the resolved read returns the COMPLETE seeded set, and the membership it
// yields still equals IsReadable's answer anchor by anchor across every
// class.
func TestReadableAnchors_PastTheFastPathCap_ResolvesThenChunks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)
	ctx := context.Background()

	const (
		// fastPathCap is substrate's 1,024-matched-subject ceiling: at or
		// under it the read is one atomic multi_last, past it a
		// resolve-then-get fallback (a STREAM.INFO resolution per wildcard,
		// then the resolved keys read in atomic chunks). The seeded key
		// count must exceed it, or this degenerates into the fast-path case
		// the other tests already cover.
		fastPathCap = 1024
		groups      = 110 // 110 x 11 keys = 1,210 matched subjects
		wideActorID = "wideactorNanoidaaaaa"
		nextActorID = "nextactorNanoidaaaaa"
		sampleSize  = 210
	)
	wideSuffix := "identity." + wideActorID
	nextSuffix := "identity." + nextActorID

	type expectation struct {
		anchorID string
		class    string
		live     bool
	}
	universe := make([]expectation, 0, groups*len(pastCapClasses))
	seededKeys := 0
	wantLive := 0
	for g := 0; g < groups; g++ {
		for ci, c := range pastCapClasses {
			anchorID := nanoIDFor("capAnchor", g*len(pastCapClasses)+ci)
			for _, k := range c.keys {
				putPerAnchorEntry(t, kv, grantKey(k.domain, wideSuffix, anchorID), k.deleted)
				seededKeys++
			}
			universe = append(universe, expectation{anchorID: anchorID, class: c.name, live: c.live})
			if c.live {
				wantLive++
			}
		}
	}
	require.Greater(t, seededKeys, fastPathCap,
		"the seeded matched-subject count must exceed the multi-get fast-path cap, or the resolve-then-get fallback is never exercised")

	// A second actor holding some of the SAME anchor ids, in both key shapes.
	// The resolved filters pin the actor suffix as a literal token pair, so
	// none of these may be collected.
	nextOnlyAnchor := nanoIDFor("capAnchor", 50000)
	putPerAnchorEntry(t, kv, grantKey("", nextSuffix, nextOnlyAnchor), false)
	for i := 0; i < 10; i++ {
		shared := universe[i*len(pastCapClasses)+1].anchorID // a baseDead anchor for the wide actor
		putPerAnchorEntry(t, kv, grantKey("", nextSuffix, shared), false)
		putPerAnchorEntry(t, kv, grantKey("clinic", nextSuffix, shared), false)
	}

	// The primitive itself, on the two filters this reader passes: either
	// filter's resolution failing, or the resolved read coming back short,
	// surfaces here rather than as a membership difference nobody can
	// attribute.
	entries, err := kv.GetMultiNoSnapshot(ctx, []string{
		"cap-read." + wideSuffix + ".*",
		"cap-read.*." + wideSuffix + ".*",
	})
	require.NoError(t, err, "the two whole-actor filters must be accepted together past the fast-path cap")
	require.Len(t, entries, seededKeys,
		"the resolved read must return the actor's complete key set, tombstones included, and nothing of the sibling actor's")

	set, err := capabilityread.ReadableAnchors(ctx, kv, "identity", wideActorID)
	require.NoError(t, err)

	// The set's size, over a universe that is closed by construction: every
	// key seeded for this actor names one of these anchors, so counting
	// admissions across it counts the whole set.
	gotLive := 0
	for _, e := range universe {
		if set.Admits(e.anchorID) {
			gotLive++
		}
	}
	require.Equal(t, wantLive, gotLive, "the set must hold exactly the anchors with at least one live key")
	require.False(t, set.Admits(nextOnlyAnchor), "the sibling actor's own anchor must not be admitted")

	// Equivalence against the shipped per-anchor gate, sampled across every
	// class. The stride is coprime with the class count, so the sample spans
	// all seven; the absent and sibling-only anchors are appended so the
	// denial arms are covered too.
	const stride = 3
	seenClasses := map[string]int{}
	checked := 0
	for i := 0; checked < sampleSize && i*stride < len(universe); i++ {
		e := universe[i*stride]
		want, ierr := capabilityread.IsReadable(ctx, kv, "identity", wideActorID, e.anchorID)
		require.NoError(t, ierr)
		require.Equal(t, want, set.Admits(e.anchorID),
			"anchor %q (class %s): the resolved set must answer exactly what IsReadable answers", e.anchorID, e.class)
		require.Equal(t, e.live, want, "anchor %q (class %s): the class's own verdict", e.anchorID, e.class)
		seenClasses[e.class]++
		checked++
	}
	require.Equal(t, sampleSize, checked, "the sample must reach its declared size")
	require.Len(t, seenClasses, len(pastCapClasses), "the sample must span every seeded class: %v", seenClasses)

	for _, anchorID := range []string{nanoIDFor("capAnchor", 55555), nextOnlyAnchor} {
		want, ierr := capabilityread.IsReadable(ctx, kv, "identity", wideActorID, anchorID)
		require.NoError(t, ierr)
		require.False(t, want)
		require.Equal(t, want, set.Admits(anchorID), "anchor %q: an anchor this actor holds no key for must be denied by both", anchorID)
	}
}
