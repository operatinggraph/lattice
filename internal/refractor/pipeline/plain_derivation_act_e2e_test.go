// The plain arm's affected-anchor derivation DECIDING a neighbour event, end to
// end over a real embedded NATS server — plain-lens-neighbour-anchor-derivation
// -design.md §11's e2e set.
//
// Every test here is a pair. A plain lens's neighbour event has exactly two
// possible outcomes — today's unseeded whole-corpus rescan, or one seeded
// evaluation per derived anchor — and the §5 licence is what chooses between
// them. So the SUBJECT lens and the CONTROL lens are built from the same cypher,
// over the same graph, off the same CDC events, and differ in one conjunct: the
// subject carries an enrolled, ticking divergence Auditor and the control does
// not. Without that control, "the bystander's row never moved" is equally
// satisfied by an event that moved nothing anywhere, and the tests would pin
// nothing at all.
//
// The adjacency index the walk reads is maintained here by the lenses' own plain
// link arm (evalPlainLinkReprojection self-applies every link event it is handed
// before evaluating), so a projected row carrying a TRAVERSED value is also the
// barrier proving the walk has an edge to step. No separate bootstrapper is
// needed for a plain lens.
package pipeline_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// plainActProviderSpec is the clinicProviders shape: anchor `provider`, one
// OPTIONAL hop to a neighbour `identity` whose DATA the row projects. The
// projected value has to come from the neighbour, or a neighbour event moves no
// row at all and a narrowing test passes over an empty ground truth.
const plainActProviderSpec = `
MATCH (pr:provider)
OPTIONAL MATCH (pr)-[:identifiedBy]->(id:identity)
RETURN pr.key AS key, id.data.name AS identityName
`

// plainActEmploymentSpec is §6's retraction shape: a REQUIRED two-hop chain
// whose middle link is incident on neither the anchor nor the row's value
// source. Removing `org -locatedIn-> location` drops the provider out of the
// matched set without any event ever naming the provider.
const plainActEmploymentSpec = `
MATCH (pr:provider)-[:employedBy]->(org:org)-[:locatedIn]->(loc:location)
RETURN pr.key AS key, loc.data.city AS city
`

// plainActFilteredSpec is the §6 probe's shape: a WHERE the walk does not model.
// walkToAnchors steps the pattern's hops, so it derives every provider joined to
// the event identity — including one this predicate has always excluded, whose
// row therefore has never existed.
const plainActFilteredSpec = `
MATCH (pr:provider)-[:identifiedBy]->(id:identity)
WHERE id.data.status = 'active'
RETURN pr.key AS key, id.data.name AS holderName
`

// plainActDuplicateSpec is a SYNTHETIC shape built to exercise §4.4's gap
// directly — it does not mirror any live lens. (identity-hygiene's real
// duplicateCandidates sets DiffRetraction, so seedAnchorFor never seeds it
// at all; identity-domain's real identityCredentialBindingsRead binds the
// identity label at two positions the same way, but every one of its
// columns is key-derived, so no property of the far position can ever make
// its row stale — see the design doc's build note.) The gap this proves is
// general, over the SAME label bound at two pattern positions — `b`, the
// anchor position seedAnchorFor's engine-level seed can narrow to, and `a`,
// a position no engine-level seed can ever reach. The row's own value comes
// from `a`, so an event on the vertex playing that role is the shape that
// exposes the gap: seedAnchorFor sees the event vertex's own type
// (identity) IS the anchor label and seeds it, but the engine can only ever
// narrow the ANCHOR pattern position (`b`) — so an event on the vertex
// playing `a` asks "does this vertex, as `b`, have an outgoing
// duplicateOf edge" and finds none. The engine-level seed on its own
// therefore never carries `a`'s new value to the row keyed on `b`; the
// plain derivation's derived set is what closes that, which is exactly
// what the seeded multi-position cases below hold it to.
const plainActDuplicateSpec = `
MATCH (b:identity)-[:duplicateOf]->(a:identity)
RETURN b.key AS key, a.key AS dupOf, a.data.name AS dupName
`

// plainActAuditInterval is the subject lens's audit cadence, compressed from the
// production default so the licence's staleness conjunct is satisfied by a real
// ticking auditor within the test's own lifetime rather than by a hand-written
// timestamp.
//
// It is deliberately not compressed as far as it could be, and the floor is a
// flake floor rather than a speed one. The conjunct reads LastPassAt against
// auditorStaleCycles (10) of this interval, so the interval IS the licence's
// tolerance divided by ten: at 250ms the subject silently unlicenses if no pass
// lands within 2.5s, which `-p 4` host contention can reach — and what that
// produces is an INVERTED narrowing assertion rather than a recognizable
// contention signature, the most expensive kind of failure to read. At one
// second the first verdict still lands within two (startOffset spreads the start
// over one interval, then the first tick), and the licence survives a ten-second
// stall.
const plainActAuditInterval = time.Second

// plainActLens is one installed plain lens in `act` mode plus the handles the
// assertions read.
type plainActLens struct {
	ruleID   string
	pipeline *pipeline.Pipeline
	targetKV *substrate.KV
}

// installPlainActLens builds and runs a plain lens over env's graph in `act`
// mode. audited is the single conjunct that separates every subject from its
// control: with an enrolled, ticking Auditor §5's licence admits the lens and
// its neighbour events narrow to the derived anchors; without one the licence
// refuses and the lens keeps the unseeded whole-corpus rescan.
//
// It enrols and STARTS the audit but does not wait for its verdict — see
// awaitPlainActVerdict, which each test calls once its corpus exists.
func installPlainActLens(t *testing.T, env *pipelineEnv, ruleID, bucket, spec string,
	mode adapter.DeleteMode, audited bool) *plainActLens {
	t.Helper()
	eng, cr := compileFullRule(t, spec, []string{"key"})
	targetKV, adpt := newTargetKVMode(t, env, bucket, []string{"key"}, mode)
	reporter := newHealthReporter(t, env, ruleID)

	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, reporter)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, cr))
	p.SetAnchorDerivationMode(pipeline.DerivationModeAct)

	if audited {
		enrolled, refusal := p.InstallAudit(pipeline.AuditOptions{Interval: plainActAuditInterval})
		require.True(t, enrolled, "the subject lens must enrol, or its licence can never hold; refusal: %s", refusal)
		runPlainActAudit(t, p)
	} else {
		require.Nil(t, p.Auditor(), "the control lens must carry no auditor at all")
	}

	startPipeline(t, env, p, ruleID)
	return &plainActLens{ruleID: ruleID, pipeline: p, targetKV: targetKV}
}

// runPlainActAudit starts the lens's divergence audit for the test's lifetime.
func runPlainActAudit(t *testing.T, p *pipeline.Pipeline) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() { defer wg.Done(); p.RunAudit(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })
}

// awaitPlainActVerdict blocks until the lens's audit has COMPARED an anchor and
// published the verdict — the licence's own precondition, and the reason it
// cannot be waited out at install time.
//
// The staleness conjunct reads AuditStatus.LastPassAt, and a pass that compared
// no anchor never stamps it (audit.go's record): a pass over an empty anchor
// listing, or one where every anchor's read-back failed, verified nothing and
// must leave the clock ageing. Every lens here is installed BEFORE its corpus
// exists, so its first ticks are exactly that empty-listing case. Until an
// anchor has actually been compared the subject is no more licensed than the
// control, and every narrowing assertion would be measuring the wrong thing —
// so each test calls this once its graph is in place and its rows have landed.
func (l *plainActLens) awaitPlainActVerdict(t *testing.T) {
	t.Helper()
	a := l.pipeline.Auditor()
	require.NotNil(t, a, "only an audited lens has a verdict to wait for")
	pollUntil(t, 30*time.Second, func() bool {
		st := a.Status()
		return !st.LastPassAt.IsZero() && st.Audited > 0
	})
	require.Empty(t, a.Status().Suppression,
		"a suppressed audit refuses the licence just as a missing one does")
}

// row returns the lens's stored row for key, or nil when the target holds none.
func (l *plainActLens) row(t *testing.T, key string) map[string]any {
	t.Helper()
	entry, err := l.targetKV.Get(context.Background(), key)
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return nil
	}
	var row map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	return row
}

// name reads the projected identityName, or "" when the row is absent — the
// value a narrowing test watches move.
func (l *plainActLens) name(t *testing.T, key string) string {
	t.Helper()
	row := l.row(t, key)
	if row == nil {
		return ""
	}
	s, _ := row["identityName"].(string)
	return s
}

// purgeRow removes a row from the target behind the lens's back — the hole no
// CDC event will refill, and the ONLY way to make "this anchor was reprojected"
// observable on this lens.
//
// A revision cannot answer it: NatsKVAdapter's unguarded upsert reads before it
// writes and skips a Put whose row marshals to the bytes already stored
// (adapter/natskv.go), so a whole-corpus rescan that faithfully re-derives an
// unaffected anchor's identical row moves no revision at all. Against an ABSENT
// key that comparison cannot hold, so the rescan writes and the narrowed lens's
// silence stays silent — which is exactly the difference under test.
func (l *plainActLens) purgeRow(t *testing.T, key string) {
	t.Helper()
	require.NoError(t, l.targetKV.Purge(context.Background(), key))
	require.False(t, l.held(t, key), "the hole must be real before the reprojection question is asked")
}

// held reports whether the target holds ANY entry for key — a live row or a
// soft-delete tombstone alike. It is how the §6 probe's negative arm is asked:
// the hazard the probe exists to prevent is a durable marker appearing at a key
// no row has ever occupied, and only a key-level question can see one.
func (l *plainActLens) held(t *testing.T, key string) bool {
	t.Helper()
	entry, err := l.targetKV.Get(context.Background(), key)
	return err == nil && entry != nil && len(entry.Value) > 0
}

// putIdentity writes an identity vertex whose name and status the lenses read.
// stamp is the provenance timestamp, distinct per write so each is a real CDC
// event rather than a no-op rewrite.
func putIdentity(t *testing.T, env *pipelineEnv, key, name, status, stamp string) {
	t.Helper()
	putNode(t, env.coreKV, key, map[string]any{
		"key": key, "class": "identity",
		"data":           map[string]any{"name": name, "status": status},
		"lastModifiedAt": stamp,
	})
}

// TestPlainDerivationAct_NarrowsToTheAffectedAnchor_E2E is the fire's named
// payoff (§11's e2e (a)): on the clinicProviders shape, a neighbour event
// reprojects the ONE provider it can affect, and the bystander provider is not
// evaluated at all.
//
// The bystander's row is removed out of band first, so "was it reprojected" has
// an observable answer (see purgeRow). Two positives make the subject's silence
// mean something, and both are asserted: the control lens's bystander row comes
// BACK off the same event — so this is an event that reprojects bystanders when
// nothing stops it — and the subject's own affected row still moves, so the
// subject is narrowed rather than deaf.
//
// The triggering event is an identity VERTEX write, so what this proves is
// vertex-neighbour narrowing: the path through deriveAnchorsForPlainVertex, which
// is the one every plain neighbour event reaches (an aspect arrives through its
// owner vertex, a link through each endpoint vertex). The LINK arm's own
// narrowing is proven by the retraction test below, whose whole trigger is a link
// tombstone reaching the derivation through its org endpoint.
func TestPlainDerivationAct_NarrowsToTheAffectedAnchor_E2E(t *testing.T) {
	env := startPipelineEnv(t)

	subject := installPlainActLens(t, env, "plain-act-narrow", "plain-act-narrow-target",
		plainActProviderSpec, adapter.DeleteModeHard, true)
	control := installPlainActLens(t, env, "plain-act-narrow-control", "plain-act-narrow-control-target",
		plainActProviderSpec, adapter.DeleteModeHard, false)
	lenses := []*plainActLens{subject, control}

	pr1, pr2 := narrowedID(t, "ActPrA"), narrowedID(t, "ActPrB")
	id1, id2 := narrowedID(t, "ActJdA"), narrowedID(t, "ActJdB")
	pr1Key, pr2Key := substrate.VertexKey("provider", pr1), substrate.VertexKey("provider", pr2)
	id1Key, id2Key := substrate.VertexKey("identity", id1), substrate.VertexKey("identity", id2)

	putNode(t, env.coreKV, pr1Key, map[string]any{"key": pr1Key, "class": "provider"})
	putNode(t, env.coreKV, pr2Key, map[string]any{"key": pr2Key, "class": "provider"})
	putIdentity(t, env, id1Key, "alice", "active", "2026-01-01T00:00:00Z")
	putIdentity(t, env, id2Key, "bob", "active", "2026-01-01T00:00:00Z")
	putLink(t, env.coreKV, "provider", pr1, "identifiedBy", "identity", id1)
	putLink(t, env.coreKV, "provider", pr2, "identifiedBy", "identity", id2)

	// A row carrying the TRAVERSED name is the adjacency barrier: the walk the
	// narrowing depends on has an edge to step only once the link event has been
	// applied, and only a traversed value proves it was.
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return l.name(t, pr1Key) == "alice" })
		pollUntil(t, 30*time.Second, func() bool { return l.name(t, pr2Key) == "bob" })
	}

	// The corpus now exists, so the subject's audit can reach a verdict over it —
	// which is what licenses the narrowing every assertion below reads.
	subject.awaitPlainActVerdict(t)

	// A settling event on the anchor-affecting neighbour, waited out on both
	// lenses, is what makes the purge below safe: one consumer applies its
	// messages in order and writes every result of a message before acking it,
	// so a row carrying this value proves every setup event is already applied
	// and no in-flight write can race the hole back shut.
	putIdentity(t, env, id1Key, "alice-settled", "active", "2026-01-02T00:00:00Z")
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return l.name(t, pr1Key) == "alice-settled" })
		l.purgeRow(t, pr2Key)
	}

	putIdentity(t, env, id1Key, "alice-renamed", "active", "2026-01-03T00:00:00Z")
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return l.name(t, pr1Key) == "alice-renamed" })
	}

	// The control's bystander row came back. That is the positive vector the
	// whole test rests on: this event really does reproject a provider it cannot
	// affect, unless something stops it.
	pollUntil(t, 30*time.Second, func() bool { return control.name(t, pr2Key) == "bob" })

	// A second event on the same neighbour, waited out, is the fence the
	// subject's negative is read behind.
	putIdentity(t, env, id1Key, "alice-fenced", "active", "2026-01-04T00:00:00Z")
	pollUntil(t, 30*time.Second, func() bool { return subject.name(t, pr1Key) == "alice-fenced" })

	require.False(t, subject.held(t, pr2Key),
		"the licensed lens must never have evaluated a provider no identifiedBy edge reaches from the event")

	acted := subject.pipeline.AnchorDerivationShadow()
	require.Greater(t, acted.Acted, int64(0), "the subject's events must have been decided by the derived set")
	require.Zero(t, control.pipeline.AnchorDerivationShadow().Acted,
		"the control's licence refusal is static, so it never acts and never counts a fall-back either")
}

// TestPlainDerivationAct_RetractsOnANonIncidentLinkRemoval_E2E is §6's
// retraction class end to end (§11's e2e (b)): removing a link incident on
// NEITHER the anchor nor its own row's value source retracts the anchor's row.
//
// A rescan cannot reach that outcome, and the control lens is what shows why.
// Its answer to the event is an unseeded whole-corpus scan whose result set
// simply omits the provider, and the filter-retraction presence check that would
// turn an omission into a Delete cannot fire on it: that check derives the row
// key from the EVENT vertex, and the event vertex is an org. So the control's
// orphan row survives, and the subject's retraction is the derived path's alone.
func TestPlainDerivationAct_RetractsOnANonIncidentLinkRemoval_E2E(t *testing.T) {
	env := startPipelineEnv(t)

	subject := installPlainActLens(t, env, "plain-act-retract", "plain-act-retract-target",
		plainActEmploymentSpec, adapter.DeleteModeHard, true)
	control := installPlainActLens(t, env, "plain-act-retract-control", "plain-act-retract-control-target",
		plainActEmploymentSpec, adapter.DeleteModeHard, false)
	lenses := []*plainActLens{subject, control}

	pr1, org1, loc1 := narrowedID(t, "RtcPrA"), narrowedID(t, "RtcFirmA"), narrowedID(t, "RtcSiteA")
	pr2, org2, loc2 := narrowedID(t, "RtcPrB"), narrowedID(t, "RtcFirmB"), narrowedID(t, "RtcSiteB")
	pr1Key, pr2Key := substrate.VertexKey("provider", pr1), substrate.VertexKey("provider", pr2)
	loc2Key := substrate.VertexKey("location", loc2)

	for _, v := range []struct{ id, class string }{
		{pr1, "provider"}, {pr2, "provider"}, {org1, "org"}, {org2, "org"},
	} {
		key := substrate.VertexKey(v.class, v.id)
		putNode(t, env.coreKV, key, map[string]any{"key": key, "class": v.class})
	}
	for _, l := range []struct{ id, city string }{{loc1, "Bristol"}, {loc2, "Leeds"}} {
		key := substrate.VertexKey("location", l.id)
		putNode(t, env.coreKV, key, map[string]any{
			"key": key, "class": "location", "data": map[string]any{"city": l.city},
		})
	}
	putLink(t, env.coreKV, "provider", pr1, "employedBy", "org", org1)
	putLink(t, env.coreKV, "provider", pr2, "employedBy", "org", org2)
	putLink(t, env.coreKV, "org", org1, "locatedIn", "location", loc1)
	putLink(t, env.coreKV, "org", org2, "locatedIn", "location", loc2)

	city := func(l *plainActLens, key string) string {
		row := l.row(t, key)
		if row == nil {
			return ""
		}
		s, _ := row["city"].(string)
		return s
	}
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return city(l, pr1Key) == "Bristol" })
		pollUntil(t, 30*time.Second, func() bool { return city(l, pr2Key) == "Leeds" })
	}
	subject.awaitPlainActVerdict(t)

	// The removal: a soft tombstone on the org→location link. No event in this
	// step names a provider, and neither endpoint of the link is one.
	linkKey := "lnk.org." + org1 + ".locatedIn.location." + loc1
	raw, err := json.Marshal(map[string]any{
		"key": linkKey, "isDeleted": true,
		"createdAt": "2026-08-01T00:00:00Z", "lastModifiedAt": "2026-08-02T00:00:00Z",
	})
	require.NoError(t, err)
	_, err = env.coreKV.Put(context.Background(), linkKey, raw)
	require.NoError(t, err)

	pollUntil(t, 30*time.Second, func() bool { return !subject.held(t, pr1Key) })

	// The fence for the control's negative: an event on the OTHER chain, which
	// both lenses reproject observably. Once its value has landed, the link
	// removal has been fully applied — so the control's surviving row is a
	// verdict, not a message still in flight.
	putNode(t, env.coreKV, loc2Key, map[string]any{
		"key": loc2Key, "class": "location", "data": map[string]any{"city": "Leeds Central"},
		"lastModifiedAt": "2026-08-03T00:00:00Z",
	})
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return city(l, pr2Key) == "Leeds Central" })
	}

	require.Equal(t, "Bristol", city(control, pr1Key),
		"the unlicensed lens keeps the orphan row — its rescan omits the provider and nothing turns that omission into a Delete")
	require.False(t, subject.held(t, pr1Key),
		"the licensed lens retracts it: the derived anchor's own seeded evaluation is what makes the omission nameable")
	require.Greater(t, subject.pipeline.AnchorDerivationShadow().Acted, int64(0))
}

// TestPlainDerivationAct_NeverProjectedAnchorGetsNoTombstone_E2E is §6's
// presence probe end to end (§11's e2e (c)), on a soft-delete lens where a
// wrong Delete is a DURABLE marker rather than a no-op against an absent key.
//
// walkToAnchors models the pattern's hops and not its WHERE, so an identity
// event derives every provider joined to it — including one the predicate has
// always excluded, whose seeded evaluation returns no rows and whose retraction
// the presence check would therefore emit. The probe drops that one.
//
// Both arms are the same lens, the same predicate and the same derived-anchor
// re-entry; the only difference is whether a row was ever written. The live
// anchor's tombstone is the positive vector, and it is load-bearing twice over:
// it proves the Delete path is reachable in this harness at all, and it is
// itself behaviour only the derived path has (an identity event's unseeded
// rescan can name no provider's row key to retract).
func TestPlainDerivationAct_NeverProjectedAnchorGetsNoTombstone_E2E(t *testing.T) {
	env := startPipelineEnv(t)

	subject := installPlainActLens(t, env, "plain-act-probe", "plain-act-probe-target",
		plainActFilteredSpec, adapter.DeleteModeSoft, true)

	prLive, prNever := narrowedID(t, "PrbPrA"), narrowedID(t, "PrbPrB")
	idLive, idNever := narrowedID(t, "PrbJdA"), narrowedID(t, "PrbJdB")
	prLiveKey := substrate.VertexKey("provider", prLive)
	prNeverKey := substrate.VertexKey("provider", prNever)
	idLiveKey := substrate.VertexKey("identity", idLive)
	idNeverKey := substrate.VertexKey("identity", idNever)

	putNode(t, env.coreKV, prLiveKey, map[string]any{"key": prLiveKey, "class": "provider"})
	putNode(t, env.coreKV, prNeverKey, map[string]any{"key": prNeverKey, "class": "provider"})
	putIdentity(t, env, idLiveKey, "alice", "active", "2026-01-01T00:00:00Z")
	// Inactive from birth: this provider's row has never once existed.
	putIdentity(t, env, idNeverKey, "bob", "suspended", "2026-01-01T00:00:00Z")
	putLink(t, env.coreKV, "provider", prLive, "identifiedBy", "identity", idLive)
	putLink(t, env.coreKV, "provider", prNever, "identifiedBy", "identity", idNever)

	holder := func(key string) string {
		row := subject.row(t, key)
		if row == nil {
			return ""
		}
		s, _ := row["holderName"].(string)
		return s
	}
	pollUntil(t, 30*time.Second, func() bool { return holder(prLiveKey) == "alice" })
	subject.awaitPlainActVerdict(t)

	// The excluded provider's OWN vertex event already left a marker: an
	// anchor-typed event on a never-matched anchor emits an idempotent Delete
	// (evaluate.go's filter-retraction presence check), which a soft-delete
	// target records as a tombstone. That path is deliberately un-probed —
	// TestEvaluatePlainDerivedAnchors_ZeroRowDeleteProbe's first half pins it —
	// and the question here is the different one, about the WALK-DERIVED path,
	// so the field is cleared before it is asked.
	require.True(t, subject.held(t, prNeverKey))
	subject.purgeRow(t, prNeverKey)

	// The negative arm: an event on the excluded provider's own identity. The
	// walk derives that provider, its seeded evaluation returns no rows, and the
	// retraction it would emit is dropped because no row is there.
	putIdentity(t, env, idNeverKey, "bob-renamed", "suspended", "2026-01-02T00:00:00Z")

	// Fenced on the OTHER identity, so "still no entry" is read after the event
	// above has been fully applied rather than before it arrived.
	putIdentity(t, env, idLiveKey, "alice-fenced", "active", "2026-01-03T00:00:00Z")
	pollUntil(t, 30*time.Second, func() bool { return holder(prLiveKey) == "alice-fenced" })

	require.False(t, subject.held(t, prNeverKey),
		"a walk-derived anchor whose row never existed must not acquire a soft-delete tombstone")

	// The positive arm: the SAME mechanism on an anchor whose row is live. Its
	// identity leaves the predicate, and the retraction lands.
	putIdentity(t, env, idLiveKey, "alice-fenced", "suspended", "2026-01-04T00:00:00Z")
	pollUntil(t, 30*time.Second, func() bool {
		row := subject.row(t, prLiveKey)
		if row == nil {
			return false
		}
		deleted, _ := row["isDeleted"].(bool)
		return deleted
	})

	require.False(t, subject.held(t, prNeverKey),
		"and the never-projected anchor still holds nothing — the probe is not merely deferring the marker")
	require.Greater(t, subject.pipeline.AnchorDerivationShadow().Acted, int64(0))
}

// TestPlainDerivationAct_UnenrolledLensStillRescans_E2E is §11's e2e (d): the
// licence's enrolled-Auditor conjunct gating at the outermost level. An
// un-enrolled lens under `act` — the mode a live Refractor runs with no env var
// set — keeps the whole-corpus rescan, reprojecting every anchor in the lens on
// a neighbour event that can only affect one.
//
// "Whole-corpus" is asserted as such rather than inferred from one bystander:
// every one of the three providers is watched, and every one of them moves.
func TestPlainDerivationAct_UnenrolledLensStillRescans_E2E(t *testing.T) {
	env := startPipelineEnv(t)
	require.Equal(t, pipeline.DerivationModeAct, pipeline.DefaultAnchorDerivationMode(),
		"the built-in mode is what makes this gate the only thing standing between an un-enrolled lens and a narrowed write")

	subject := installPlainActLens(t, env, "plain-act-licence", "plain-act-licence-target",
		plainActProviderSpec, adapter.DeleteModeHard, true)
	control := installPlainActLens(t, env, "plain-act-licence-control", "plain-act-licence-control-target",
		plainActProviderSpec, adapter.DeleteModeHard, false)
	lenses := []*plainActLens{subject, control}

	require.Nil(t, control.pipeline.Auditor(), "the un-enrolled lens has no auditor to license it")
	require.True(t, subject.pipeline.Auditor().Status().Enrolled, "and the subject has one that is running")

	providers := []string{narrowedID(t, "LicPrA"), narrowedID(t, "LicPrB"), narrowedID(t, "LicPrC")}
	identities := []string{narrowedID(t, "LicJdA"), narrowedID(t, "LicJdB"), narrowedID(t, "LicJdC")}
	names := []string{"alice", "bob", "carol"}
	providerKeys := make([]string, len(providers))

	for i, pr := range providers {
		providerKeys[i] = substrate.VertexKey("provider", pr)
		putNode(t, env.coreKV, providerKeys[i], map[string]any{"key": providerKeys[i], "class": "provider"})
		idKey := substrate.VertexKey("identity", identities[i])
		putIdentity(t, env, idKey, names[i], "active", "2026-01-01T00:00:00Z")
		putLink(t, env.coreKV, "provider", pr, "identifiedBy", "identity", identities[i])
	}
	for _, l := range lenses {
		for i, key := range providerKeys {
			pollUntil(t, 30*time.Second, func() bool { return l.name(t, key) == names[i] })
		}
	}
	subject.awaitPlainActVerdict(t)

	// Both bystanders' rows are removed out of band, behind a settled fence, so
	// "was this anchor evaluated" has an observable answer on each lens.
	affectedIdentity := substrate.VertexKey("identity", identities[0])
	putIdentity(t, env, affectedIdentity, "alice-settled", "active", "2026-01-02T00:00:00Z")
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return l.name(t, providerKeys[0]) == "alice-settled" })
		for _, key := range providerKeys[1:] {
			l.purgeRow(t, key)
		}
	}

	// One neighbour event, able to affect exactly one of the three providers.
	putIdentity(t, env, affectedIdentity, "alice-renamed", "active", "2026-01-03T00:00:00Z")
	for _, l := range lenses {
		pollUntil(t, 30*time.Second, func() bool { return l.name(t, providerKeys[0]) == "alice-renamed" })
	}

	// The un-enrolled lens rewrote EVERY provider, the two it cannot have
	// affected included — the shipped whole-corpus behaviour the licence keeps
	// in place while nothing is standing to re-test a row a narrowed write would
	// leave behind.
	for i, key := range providerKeys[1:] {
		want := names[i+1]
		pollUntil(t, 30*time.Second, func() bool { return control.name(t, key) == want })
	}

	putIdentity(t, env, affectedIdentity, "alice-fenced", "active", "2026-01-04T00:00:00Z")
	pollUntil(t, 30*time.Second, func() bool { return subject.name(t, providerKeys[0]) == "alice-fenced" })

	for _, key := range providerKeys[1:] {
		require.False(t, subject.held(t, key),
			"the licensed lens narrowed to the one affected anchor and evaluated no other")
	}
	require.Zero(t, control.pipeline.AnchorDerivationShadow().Acted,
		"the un-enrolled lens's derived set never decided one of its events")
	require.Greater(t, subject.pipeline.AnchorDerivationShadow().Acted, int64(0))
}

// TestPlainDerivationSeeded_MultiPositionUnlicensedKeepsTodaysNarrowSeed_E2E
// is Increment 4b's declined path (§4.4) end to end, built UNLICENSED (no
// Auditor): a seeded event whose vertex plays the pattern's SECOND identity
// position (`a`) must NOT update the row that depends on it — the SAME gap
// §4.4 names, left exactly as it is until a licence admits the correction.
//
// This is deliberately the mirror of what an earlier version of this test
// asserted. evaluateSeededMultiPosition's declined answer is the NARROW
// single-seed call (executeFullForActor with the event vertex as the seed)
// — NOT the unseeded whole-corpus rescan evaluatePlainNeighbourEvent's own
// declined path uses — because a seeded event's shipped answer is already
// that narrow call: an operator running `off` mode, or any lens without a
// fresh licence, must pay exactly that narrow-call cost, never a rescan it
// never asked for (a real per-event cost regression on any high-volume
// identity-typed lens — see the design doc's build note). The correction is
// licensed-`act`-only; its own twin test below proves that half.
func TestPlainDerivationSeeded_MultiPositionUnlicensedKeepsTodaysNarrowSeed_E2E(t *testing.T) {
	env := startPipelineEnv(t)

	lens := installPlainActLens(t, env, "plain-act-seeded-multipos", "plain-act-seeded-multipos-target",
		plainActDuplicateSpec, adapter.DeleteModeHard, false)

	bID, aID := narrowedID(t, "SmpDupB"), narrowedID(t, "SmpDupA")
	bKey := substrate.VertexKey("identity", bID)
	aKey := substrate.VertexKey("identity", aID)

	putIdentity(t, env, bKey, "duplicate-of-someone", "active", "2026-01-01T00:00:00Z")
	putIdentity(t, env, aKey, "alice", "active", "2026-01-01T00:00:00Z")
	putLink(t, env.coreKV, "identity", bID, "duplicateOf", "identity", aID)

	dupName := func(key string) string {
		row := lens.row(t, key)
		if row == nil {
			return ""
		}
		s, _ := row["dupName"].(string)
		return s
	}
	pollUntil(t, 30*time.Second, func() bool { return dupName(bKey) == "alice" })

	// The seeded event: a property write on `a` itself. `a`'s own type IS
	// the lens's anchor label ("identity"), so seedAnchorFor narrows to it —
	// exactly the shape §4.4 names, and the one the narrow engine-level seed
	// alone cannot answer.
	putIdentity(t, env, aKey, "alice-renamed", "active", "2026-01-02T00:00:00Z")

	// The fence: a BRAND NEW pair, created after the rename event above. Its
	// own initial population goes through the LINK arm's normal endpoint
	// evaluation (evalPlainLinkReprojection), which narrows correctly on the
	// b-position endpoint independent of the declined-path correction above
	// — so waiting for it proves every prior event on this single in-order
	// consumer, the rename included, has already been fully applied. A poll
	// on bKey's OWN value could not serve as this fence: under the declined
	// path it is EXPECTED never to move, which is exactly what is under test.
	fenceB, fenceA := narrowedID(t, "SmpFenceB"), narrowedID(t, "SmpFenceA")
	fenceBKey := substrate.VertexKey("identity", fenceB)
	putIdentity(t, env, fenceBKey, "fence-b", "active", "2026-01-03T00:00:00Z")
	putIdentity(t, env, substrate.VertexKey("identity", fenceA), "fence-a", "active", "2026-01-03T00:00:00Z")
	putLink(t, env.coreKV, "identity", fenceB, "duplicateOf", "identity", fenceA)
	pollUntil(t, 30*time.Second, func() bool { return dupName(fenceBKey) == "fence-a" })

	require.Equal(t, "alice", dupName(bKey),
		"unlicensed, the seeded multi-position event must keep today's narrow single-seed answer — never a whole-corpus rescan it never asked for")
}

// TestPlainDerivationSeeded_MultiPositionNarrowsViaTheDerivedSet_E2E is the
// licensed twin: with an enrolled, ticking Auditor, the SAME seeded event is
// decided by the derived anchor set — deriveAnchorsForPlainVertex seeds BOTH
// positions the identity label binds, `a` itself (a self-only derivation
// that finds no outgoing duplicateOf edge, so no row for it and no bearing
// on the assertions below) and `b` by walking the incoming duplicateOf edge
// — rather than the unseeded rescan, and a bystander pair is left untouched.
//
// It is also the reentrancy seam's own positive vector, though not by an
// assertion: `a` and `b` are both identity-typed and so both otherwise
// eligible for this SAME branch, and evaluatePlainDerivedAnchors re-enters
// evaluateForEntryRaw for each. Without plainDerivedAnchorReentryKey that
// re-entry would re-derive the identical {a, b} set and recurse forever; the
// test's own completion, not a hang or a stack overflow, is what proves the
// guard held.
func TestPlainDerivationSeeded_MultiPositionNarrowsViaTheDerivedSet_E2E(t *testing.T) {
	env := startPipelineEnv(t)

	subject := installPlainActLens(t, env, "plain-act-seeded-multipos-narrow", "plain-act-seeded-multipos-narrow-target",
		plainActDuplicateSpec, adapter.DeleteModeSoft, true)

	b1, a1 := narrowedID(t, "SmnDupB1"), narrowedID(t, "SmnDupA1")
	b2, a2 := narrowedID(t, "SmnDupB2"), narrowedID(t, "SmnDupA2")
	b1Key, b2Key := substrate.VertexKey("identity", b1), substrate.VertexKey("identity", b2)
	a1Key, a2Key := substrate.VertexKey("identity", a1), substrate.VertexKey("identity", a2)

	putIdentity(t, env, b1Key, "dup-b1", "active", "2026-01-01T00:00:00Z")
	putIdentity(t, env, b2Key, "dup-b2", "active", "2026-01-01T00:00:00Z")
	putIdentity(t, env, a1Key, "alice", "active", "2026-01-01T00:00:00Z")
	putIdentity(t, env, a2Key, "carol", "active", "2026-01-01T00:00:00Z")
	putLink(t, env.coreKV, "identity", b1, "duplicateOf", "identity", a1)
	putLink(t, env.coreKV, "identity", b2, "duplicateOf", "identity", a2)

	dupName := func(key string) string {
		row := subject.row(t, key)
		if row == nil {
			return ""
		}
		s, _ := row["dupName"].(string)
		return s
	}
	pollUntil(t, 30*time.Second, func() bool { return dupName(b1Key) == "alice" })
	pollUntil(t, 30*time.Second, func() bool { return dupName(b2Key) == "carol" })
	subject.awaitPlainActVerdict(t)

	// `a1` is itself identity-typed — the lens's own anchor label — so its
	// initial write already left the SAME idempotent no-op marker any
	// never-matched anchor does (evaluateForEntryRaw's filter-retraction
	// check, unconditional and pre-dating this design entirely; pinned by
	// TestPlainDerivationAct_NeverProjectedAnchorGetsNoTombstone_E2E). held()
	// alone cannot tell that marker apart from a genuine row — both leave a
	// non-empty entry under DeleteModeSoft — so what this test's own event
	// must not do is asked directly instead: a1's key must never carry
	// CONTENT, i.e. dupName must stay empty. A real row there (dupName
	// non-empty) would mean the a-position's self-derivation was mistaken
	// for a genuine anchor.
	require.Empty(t, dupName(a1Key), "a1's own key must never carry content before this test's event")
	acted := subject.pipeline.AnchorDerivationShadow().Acted

	putIdentity(t, env, a1Key, "alice-renamed", "active", "2026-01-02T00:00:00Z")
	pollUntil(t, 30*time.Second, func() bool { return dupName(b1Key) == "alice-renamed" })

	require.Equal(t, "carol", dupName(b2Key), "the derived set must not touch the bystander pair")
	require.Empty(t, dupName(a1Key),
		"the a-position's own self-derivation finds no row of its own and must never acquire content")
	require.Greater(t, subject.pipeline.AnchorDerivationShadow().Acted, acted,
		"the seeded multi-position event must be decided by the derived set, not the unseeded rescan")
}
