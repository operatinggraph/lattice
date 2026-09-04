package pipeline

// The personal arm's narrowing licence (personal-lens-derivation-licence-
// design.md §4.4c): six fail-closed conjuncts, four asserted by the host at
// registration and two read live off the standing healer's own pass verdict.
//
// Every conjunct gets a negative case here, and the POSITIVE vector sits at the
// top: without it a green refusal could equally come from a predicate that
// refuses everything, which is the failure mode nobody would notice — and which
// is exactly what this licence would look like if the host's assertion were
// silently dropped.
//
// The last two subtests are about the GATE rather than the predicate, and both
// state their claim as behaviour of derivationIndexForAct: a refactor that keeps
// the predicate and stops consulting it still fails.

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// personalLicenceSpec is a personal lens's shape: one constant head binding the
// actor, one typed hop out to the anchor it publishes.
const personalLicenceSpec = `
MATCH (identity:identity {key: $actorKey})-[:mayRead]->(x:unit)
RETURN x.key AS anchor, x.name AS name
`

// personalLicenceNowSpec is conjunct 4's negative vector, and it has to be
// authored: no shipped personal lens references either clock parameter, which is
// precisely why the conjunct must exist before one does.
const personalLicenceNowSpec = `
MATCH (identity:identity {key: $actorKey})-[:mayRead]->(x:unit)
RETURN x.key AS anchor, $now AS asOf
`

// licensedWiring is the host assertion a correctly wired production process
// makes. Every test below starts from it and knocks one field out, so a
// refusal's cause is the field the subtest named and nothing else.
func licensedWiring() PersonalDerivationWiring {
	return PersonalDerivationWiring{
		PersonalLens:             true,
		ReadGateWired:            true,
		GrantReprojectorWired:    true,
		SinklessCapReadProducers: func() []string { return nil },
		InterestFilterInstalled:  true,
		InterestEdgeArmed:        func() bool { return true },
		// The zero time: every verdict below carries a StartedAt of `now`, so a
		// lens "registered" at the zero time is one the healer has swept since.
		RegisteredAt: time.Time{},
	}
}

// cleanVerdict is a healer that has just completed a pass over a readable
// population, repaired everything it walked, and found one live Refractor.
func cleanVerdict() PersonalHealerVerdict {
	return PersonalHealerVerdict{
		StartedAt:             time.Now(),
		CompletedAt:           time.Now(),
		Interval:              DefaultPersonalHealerInterval,
		Attempted:             5,
		Failed:                0,
		PopulationReadable:    true,
		InstanceCount:         1,
		InstanceCountReadable: true,
	}
}

// personalLicenceFixture builds a pipeline carrying a real compiled personal
// cypher and the host's assertion, so the licence is asked of the state a real
// registration leaves behind rather than of a hand-built struct.
func personalLicenceFixture(t *testing.T, spec string) *Pipeline {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	p := &Pipeline{ruleID: "personal-licence-rule", engineKind: ruleengine.EngineFull}
	require.NoError(t, p.UseFullEngine(eng, cr))
	p.SetPersonalDerivationLicence(licensedWiring(), cleanVerdict)
	return p
}

// verdictOf installs a fixed verdict as the licence's live accessor.
func verdictOf(p *Pipeline, w PersonalDerivationWiring, v PersonalHealerVerdict) {
	p.SetPersonalDerivationLicence(w, func() PersonalHealerVerdict { return v })
}

func TestPersonalDerivationLicence_Conjuncts(t *testing.T) {
	t.Run("positive vector: a fully wired personal lens behind a clean recent healer is licensed", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.True(t, licensed, "refusal: %s", refusal)
		require.Empty(t, refusal)
	})

	t.Run("a host that asserted nothing at all refuses — the zero value is the refusal", func(t *testing.T) {
		eng := full.New()
		cr, err := eng.Parse(personalLicenceSpec)
		require.NoError(t, err)
		p := &Pipeline{ruleID: "unasserted", engineKind: ruleengine.EngineFull}
		require.NoError(t, p.UseFullEngine(eng, cr))

		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "not a personal lens")
	})

	t.Run("conjunct 0: a lens the host did not declare personal refuses", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		w := licensedWiring()
		w.PersonalLens = false
		verdictOf(p, w, cleanVerdict())
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "not a personal lens")
	})

	t.Run("conjunct 1: an unwired D1 read gate refuses", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		w := licensedWiring()
		w.ReadGateWired = false
		verdictOf(p, w, cleanVerdict())
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "the D1 read gate is not wired")
	})

	t.Run("conjunct 1: no grant-change reprojector in this process refuses", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		w := licensedWiring()
		w.GrantReprojectorWired = false
		verdictOf(p, w, cleanVerdict())
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "no grant-change reprojector is wired")
	})

	t.Run("conjunct 1: a cap-read producer installed with no sink refuses", func(t *testing.T) {
		// The §4.3(d) amendment's consumer half. A sink-less producer INSTALLS —
		// refusing it would turn a host omission into an auth-plane outage — and
		// warns. This is where that warning becomes a refusal, because a
		// producer whose withdrawals push nothing leaves one of this lens's two
		// out-of-pattern inputs announcing through nothing at all.
		p := personalLicenceFixture(t, personalLicenceSpec)
		w := licensedWiring()
		w.SinklessCapReadProducers = func() []string { return []string{"billingReadGrants"} }
		verdictOf(p, w, cleanVerdict())
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "cap-read producer is installed with no grant-change sink")
	})

	t.Run("conjunct 2: an interest filter with no change edge refuses", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		w := licensedWiring()
		w.InterestEdgeArmed = func() bool { return false }
		verdictOf(p, w, cleanVerdict())
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "the Interest Set has no change edge")
	})

	t.Run("conjunct 2: NO interest filter needs no edge — one out-of-pattern input, not two", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		w := licensedWiring()
		w.InterestFilterInstalled = false
		w.InterestEdgeArmed = nil
		verdictOf(p, w, cleanVerdict())
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.True(t, licensed, "refusal: %s", refusal)
	})

	t.Run("conjunct 3: a healer with no accessor at all refuses as never-passed", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		p.SetPersonalDerivationLicence(licensedWiring(), nil)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "has never completed a pass")
	})

	t.Run("conjunct 3: a healer that has never completed a pass refuses", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.CompletedAt = time.Time{}
		v.StartedAt = time.Time{}
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "has never completed a pass")
	})

	t.Run("conjunct 3: a pass that failed for any actor refuses — the verdict is not a stamp", func(t *testing.T) {
		// The vector that broke the first draft. A Capability-KV outage failing
		// every reprojection of every actor still advances a progress stamp,
		// because the per-lens failure path logs and continues. A predicate that
		// read the stamp would read healthy through the exact condition it
		// exists to detect.
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.Failed = 1
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "last pass failed")
	})

	t.Run("conjunct 3: a population the healer could not enumerate refuses", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.PopulationReadable = false
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "could not enumerate its population")
	})

	t.Run("conjunct 3: a stale verdict refuses, and the reason carries no elapsed", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.CompletedAt = time.Now().Add(-(PersonalHealerStaleCycles + 1) * DefaultPersonalHealerInterval)
		v.StartedAt = v.CompletedAt
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "has not completed a pass in")

		// STABILITY: the caller latches on the reason string to log a refusal at
		// most once, and a staleness window is minutes — so a reason carrying a
		// per-second elapsed would emit a line per CDC event precisely when the
		// plane is least healthy. Asked twice, a second apart in the verdict's
		// own clock, the string must not move.
		v.CompletedAt = v.CompletedAt.Add(-time.Second)
		verdictOf(p, licensedWiring(), v)
		_, again := p.personalDerivationLicence(p.ruleState())
		require.Equal(t, refusal, again, "the refusal string must be stable for as long as the state producing it holds")
	})

	t.Run("conjunct 3: a verdict just inside the window is licensed", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.CompletedAt = time.Now().Add(-(PersonalHealerStaleCycles - 1) * DefaultPersonalHealerInterval)
		v.StartedAt = v.CompletedAt
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.True(t, licensed, "refusal: %s", refusal)
	})

	t.Run("conjunct 1: a nil sink census refuses — uncheckable is not satisfied", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		w := licensedWiring()
		w.SinklessCapReadProducers = nil
		verdictOf(p, w, cleanVerdict())
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "nothing in this process can report whether the cap-read producers")
	})

	t.Run("conjunct 2: a nil interest accessor refuses while a filter is installed", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		w := licensedWiring()
		w.InterestEdgeArmed = nil
		verdictOf(p, w, cleanVerdict())
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "nothing in this process can report whether the Interest Set's writers announce")
	})

	t.Run("conjuncts 1 and 2 are read LIVE, not sampled at registration", func(t *testing.T) {
		// The failure this pins: a cap-read producer can install after a personal
		// lens registered (a hot lens install), and cmd/refractor builds the
		// InterestReconciler inside the very activation arm that registers the
		// first personal lens. A boolean captured at registration would answer
		// about a process that no longer exists, in the fail-OPEN direction.
		p := personalLicenceFixture(t, personalLicenceSpec)
		sinkless := []string{}
		armed := true
		w := licensedWiring()
		w.SinklessCapReadProducers = func() []string { return sinkless }
		w.InterestEdgeArmed = func() bool { return armed }
		verdictOf(p, w, cleanVerdict())

		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.True(t, licensed, "refusal: %s", refusal)

		sinkless = []string{"latecomerReadGrants"}
		licensed, refusal = p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed, "a producer installed AFTER registration must revoke the licence on the next evaluation")
		require.Contains(t, refusal, "cap-read producer is installed with no grant-change sink")

		sinkless = nil
		armed = false
		licensed, refusal = p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed, "a reconciler built AFTER registration with no sink must revoke it too")
		require.Contains(t, refusal, "the Interest Set has no change edge")
	})

	t.Run("conjunct 3: a pass that BEGAN before this lens registered does not license it", func(t *testing.T) {
		// A lens registering into an already-swept plane would otherwise inherit
		// a clean verdict from a pass that never drove it — the healer's
		// guarantee is per-lens, so the evidence for it must be too.
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.StartedAt = time.Now().Add(-time.Minute)
		v.CompletedAt = v.StartedAt.Add(time.Second)
		w := licensedWiring()
		w.RegisteredAt = time.Now().Add(-30 * time.Second) // joined mid-pass
		verdictOf(p, w, v)

		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "has not completed a pass begun after this lens registered")

		// And the next pass, which began after the registration, licenses it.
		v.StartedAt = time.Now()
		v.CompletedAt = time.Now()
		verdictOf(p, w, v)
		licensed, refusal = p.personalDerivationLicence(p.ruleState())
		require.True(t, licensed, "refusal: %s", refusal)
	})

	t.Run("conjunct 5: a readable count of ZERO refuses — zero is self-refuting", func(t *testing.T) {
		// The process asking the question is itself a live Refractor, so a
		// census that finds none has found a broken census, not an empty
		// deployment — and two instances whose heartbeats are not landing read
		// exactly this on BOTH of them. The sweeper already fails closed on an
		// empty listing; this is the second assertion, so an edit there cannot
		// reopen the fail-open alone.
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.InstanceCount = 0
		v.InstanceCountReadable = true
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "reads zero, which is self-refuting")
		require.Equal(t, PersonalHealerVerdictInstancesImpossible, v.Summary(),
			"and it gets its own token, because a broken census and an unreadable one send an operator to different places")
	})

	t.Run("conjunct 5: two live Refractor instances refuse", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.InstanceCount = 2
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "more than one Refractor instance is live")
	})

	t.Run("conjunct 5: an unreadable instance count refuses — fail CLOSED", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.InstanceCountReadable = false
		v.InstanceCount = 0
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "instance count is unreadable")
	})

	t.Run("conjunct 5: a stale entry from a CRASHED instance refuses, and that is CORRECT", func(t *testing.T) {
		// This vector is here to stand in front of a future "optimisation" that
		// filters the instance census by freshness. The two staleness directions
		// are not symmetric: a crashed instance whose heartbeat has not yet
		// expired OVER-counts and pessimises, which is safe; a newly started
		// instance that has not yet written one UNDER-counts and fails OPEN,
		// which is the hazard the conjunct exists for. Trading the safe direction
		// away to remove a false refusal removes the wrong one.
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.InstanceCount = 2 // one live, one crashed and not yet expired
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed,
			"a stale heartbeat over-counts and must refuse: pessimisation is the safe direction, and this test is what keeps a freshness filter from being added without an argument")
		require.Contains(t, refusal, "more than one Refractor instance is live")
	})

	t.Run("conjunct 5 stops being asked once the edge spans the deployment", func(t *testing.T) {
		// The one field that says the cardinality is no longer the question. It
		// mirrors grantchange.GrantChangeEdgeSpansDeployment, which the durable
		// grant-change signal flips — and flipping it is a build, not an edit.
		p := personalLicenceFixture(t, personalLicenceSpec)
		v := cleanVerdict()
		v.InstanceCount, v.InstanceCountReadable = 7, false
		v.EdgeSpansDeployment = true
		verdictOf(p, licensedWiring(), v)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.True(t, licensed, "refusal: %s", refusal)
	})

	t.Run("conjunct 4: a lens whose row references $now refuses", func(t *testing.T) {
		p := personalLicenceFixture(t, personalLicenceNowSpec)
		licensed, refusal := p.personalDerivationLicence(p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "depends on $now")
	})

	t.Run("conjunct 4 is READ from the published rule state, not re-walked per event", func(t *testing.T) {
		// The pin for M1(b). The clock conjunct is two exhaustive walks of the
		// compiled rule's clauses answering a question that cannot change until
		// the body does, so useFullEngineBranches derives it once at publication
		// and the licence reads the result. Posing a verdict the COMPILED RULE
		// does not carry is what proves which of the two the licence consults: a
		// predicate still walking the AST would answer "licensed" here.
		p := personalLicenceFixture(t, personalLicenceSpec)
		rs := p.ruleState()
		require.Empty(t, rs.personalClockRefusal, "precondition: this cypher is clock-free")
		rs.personalClockRefusal = "sentinel: the published verdict, not the AST"

		licensed, refusal := p.personalDerivationLicence(rs)
		require.False(t, licensed)
		require.Equal(t, "sentinel: the published verdict, not the AST", refusal)
	})
}

// TestPersonalDerivationLicence_ClockConjunctIsDerivedAtPublication pins the
// other half of M1(b): the value the licence reads is put there by the rule
// publication, and a reload replaces it with the body it describes.
//
// The round trip goes through the Pipeline's own hand-maintained field lists
// (publishRuleState / ruleState), where a field added without a line in BOTH
// reads as its zero value on every event with nothing failing anywhere — and for
// this field the zero value is "", the LICENSING answer, so the omission is
// fail-open. That is why the trip is pinned rather than the derivation alone.
func TestPersonalDerivationLicence_ClockConjunctIsDerivedAtPublication(t *testing.T) {
	eng := full.New()
	parse := func(spec string) ruleengine.CompiledRule {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		return cr
	}

	p := &Pipeline{ruleID: "clock-publication"}
	require.NoError(t, p.UseFullEngine(eng, parse(personalLicenceNowSpec)))
	require.Contains(t, p.ruleState().personalClockRefusal, "depends on $now",
		"publication must carry the verdict onto the rule state — an empty one is the LICENSING answer, so a missing publish line fails open")

	// A reload to a clock-free body clears it, so a lens edited out of the
	// refusal is not held by a verdict about a rule it no longer runs.
	require.NoError(t, p.UseFullEngine(eng, parse(personalLicenceSpec)))
	require.Empty(t, p.ruleState().personalClockRefusal)

	// And back, so the clearing is not simply a publication that never writes.
	require.NoError(t, p.UseFullEngine(eng, parse(personalLicenceNowSpec)))
	require.Contains(t, p.ruleState().personalClockRefusal, "depends on $now")
}

// TestPersonalDerivationLicence_TheGateConsultsIt states the two §4.4(a)/(b)
// edits as behaviour of derivationIndexForAct rather than of the predicate, so a
// refactor that keeps the licence and stops consulting it still fails.
//
// The fixture is the actor-aware flip fixture with patternClosedOutput turned
// OFF and the auth-plane sweep plan removed — which is exactly the shape a
// personal lens has: an out-of-pattern input, and no SweepPlan, because a
// Personal Lens never receives one.
func TestPersonalDerivationLicence_TheGateConsultsIt(t *testing.T) {
	t.Run("an unlicensed lens with an out-of-pattern input keeps the enumerator", func(t *testing.T) {
		f := newCoHolderFixture(t)
		f.p.SetPatternClosedOutput(false)
		f.p.sweeper = nil
		f.p.SetPersonalPlaneHealer(true)

		_, ready, refusal := f.p.derivationIndexForAct(f.p.ruleState())
		require.False(t, ready)
		require.Contains(t, refusal, "not a personal lens")
	})

	t.Run("the licence lifts the pattern-closure refusal, and the PERSONAL healer arm satisfies the healer conjunct", func(t *testing.T) {
		f := newCoHolderFixture(t)
		f.p.SetPatternClosedOutput(false)
		// No SweepPlan: a Personal Lens never receives one, so reading p.sweeper
		// alone here made this the one consumer of "has this lens a standing
		// healer" that could never see the personal arm.
		f.p.sweeper = nil
		f.p.SetPersonalPlaneHealer(true)
		verdictOf(f.p, licensedWiring(), cleanVerdict())

		_, ready, refusal := f.p.derivationIndexForAct(f.p.ruleState())
		require.True(t, ready, "refusal: %s", refusal)
		require.Empty(t, refusal)

		// patternClosedOutput itself must stay FALSE. It is a claim about the
		// lens read by two predicates with different tolerances and different
		// rollback shapes, and this narrowing is entitled to change one of them.
		require.False(t, f.p.PatternClosedOutput())
	})

	t.Run("a licensed lens with NO standing healer still refuses", func(t *testing.T) {
		f := newCoHolderFixture(t)
		f.p.SetPatternClosedOutput(false)
		f.p.sweeper = nil
		f.p.SetPersonalPlaneHealer(false)
		verdictOf(f.p, licensedWiring(), cleanVerdict())

		_, ready, _ := f.p.derivationIndexForAct(f.p.ruleState())
		require.False(t, ready, "acting removes the incidental reprojection; something must still repair the row")
	})

	t.Run("a licensed lens acts on its derived anchor set, and a revoked one goes back to the enumerator", func(t *testing.T) {
		f := newCoHolderFixture(t)
		f.p.SetPatternClosedOutput(false)
		f.p.sweeper = nil
		f.p.SetPersonalPlaneHealer(true)
		verdictOf(f.p, licensedWiring(), cleanVerdict())

		f.handleLink("holdsRole", "dave", "admin", false, 1)
		require.Equal(t, []string{capKeyFor(f.key("dave"))}, f.writtenActors(),
			"the licensed lens reprojects the one actor the link touches, not every co-holder")

		// Revoking the licence mid-life must put it straight back on the
		// enumerator's breadth: the licence is read LIVE at every evaluation and
		// never snapshotted onto the rule state.
		f.adpt.upserts = nil
		v := cleanVerdict()
		v.Failed = 3
		verdictOf(f.p, licensedWiring(), v)
		f.handleLink("holdsRole", "dave", "admin", true, 2)
		require.Len(t, f.writtenActors(), 4,
			"a lens whose healer stopped repairing keeps the enumerator's breadth, incidental heals included")
	})
}

// TestPersonalDerivationLicence_TheGrantIsAudible pins the POSITIVE verdict.
//
// A granted licence otherwise emits nothing: the refusal note prints a reason
// and a grant prints its absence, so "the refusal is gone" — this narrowing's
// whole payoff claim, and §11's live acceptance criterion — would be provable
// only by a line that stopped appearing. That reads identically to a lens that
// stopped receiving events, to the mode knob being off, and to the log level
// having moved. This is the dossier's own entry, third sighting: a lifted
// refusal reveals the next conjunct, and a granted licence logs nothing.
func TestPersonalDerivationLicence_TheGrantIsAudible(t *testing.T) {
	buf := captureDefaultLogger(t)
	f := newCoHolderFixture(t)
	f.p.SetPatternClosedOutput(false)
	f.p.sweeper = nil
	f.p.SetPersonalPlaneHealer(true)
	verdictOf(f.p, licensedWiring(), cleanVerdict())

	f.handleLink("holdsRole", "dave", "admin", false, 1)
	require.Contains(t, buf.String(), "personal-lens derivation licensed",
		"the grant must announce itself, or the payoff is only provable by an absence")
	require.Contains(t, buf.String(), "healerVerdict=clean",
		"and it must carry the verdict it was granted on, so a live read says WHY it holds")

	// Latched: a line per CDC event would drown the plane it is reporting on.
	buf.Reset()
	f.handleLink("holdsRole", "carol", "admin", false, 2)
	require.NotContains(t, buf.String(), "personal-lens derivation licensed")

	// And the latch clears on revocation, so a grant → revoke → grant cycle logs
	// all three edges rather than swallowing the second grant.
	v := cleanVerdict()
	v.Failed = 1
	verdictOf(f.p, licensedWiring(), v)
	f.handleLink("holdsRole", "bob", "admin", false, 3)
	require.Contains(t, buf.String(), "anchor derivation cannot act on this lens")
	require.Contains(t, buf.String(), "last pass failed",
		"a licensed-but-refused personal lens must print its REAL conjunct, never the generic out-of-pattern sentence")

	buf.Reset()
	verdictOf(f.p, licensedWiring(), cleanVerdict())
	f.handleLink("holdsRole", "alice", "admin", false, 4)
	require.Contains(t, buf.String(), "personal-lens derivation licensed",
		"the second grant is as newsworthy as the first")
}

// TestPersonalDerivationLicence_ANonPersonalLensKeepsItsOwnRefusal is the
// other half of the reason-switch reorder. The licence refuses every lens the
// host did not declare personal, at its class conjunct — and printing "it is not
// a personal lens" for a plain or actor-aggregate lens whose real refusal is
// that its row depends on an unbound input would send an operator somewhere
// there is nothing to fix.
func TestPersonalDerivationLicence_ANonPersonalLensKeepsItsOwnRefusal(t *testing.T) {
	buf := captureDefaultLogger(t)
	f := newCoHolderFixture(t)
	f.p.SetPatternClosedOutput(false)

	f.handleLink("holdsRole", "dave", "admin", false, 1)
	require.Contains(t, buf.String(), "the lens's row depends on inputs outside its compiled pattern")
	require.NotContains(t, buf.String(), "not a personal lens")
}

// TestPersonalDerivationLicence_AMultiWalkLensLogsANamedReason pins the arm the
// licence made reachable for the first time.
//
// A multi-walk lens's single-walk anchorHops is the ZERO HopIndex — `Complete`
// false and `Incomplete` EMPTY — and the refusal latch's own zero value is also
// the empty string, so an unnamed reason is not merely unreadable: the FIRST
// report reads as a repeat and nothing is logged at all. The biggest personal
// lenses would sit silently on the enumerator while the operator log read as
// though every personal lens had been licensed. That is §15.1's green bar going
// green for the wrong reason.
//
// The reason such a lens is refused is a conjunct of its own WALKS, and the same
// rule holds of every one of them: the multi-walk arm reports from
// multiWalkDerivationRefusal, which is total.
func TestPersonalDerivationLicence_AMultiWalkLensLogsANamedReason(t *testing.T) {
	buf := captureDefaultLogger(t)

	// A genuinely multi-walk rule, published through the SAME installer
	// cmd/refractor uses, so the graphs under test are the ones ruleinstall.go
	// really leaves behind rather than ones a test hand-built. One walk carries
	// a ranged hop whose lower bound exceeds one, which is what its graph
	// refuses on.
	eng := full.New()
	branch := func(spec string) ruleengine.CompiledRule {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		return cr
	}
	head := branch("MATCH (identity:identity {key: $actorKey})-[:mayRead]->(x:unit)\nRETURN x.key AS anchor, x.name AS name")
	unreadable := branch("MATCH (identity:identity {key: $actorKey})-[:mayRead*2..3]->(x:unit)\nRETURN x.key AS anchor, x.name AS name")
	p := &Pipeline{ruleID: "multi-walk-personal"}
	require.NoError(t, p.UseFullEngineBranches(eng, head, []ruleengine.CompiledRule{head, unreadable}))
	p.SetPersonalPlaneHealer(true)
	verdictOf(p, licensedWiring(), cleanVerdict())

	rs := p.ruleState()
	require.NotNil(t, rs.branches, "precondition: this rule really compiled to several walks")
	require.False(t, rs.anchorHops.Complete)
	require.Empty(t, rs.anchorHops.Incomplete,
		"precondition: the single-walk index carries no reason of its own — that absence is what this test is about")

	p.noteStaticDerivationRefusal(rs, "")
	require.Contains(t, buf.String(), DerivationBranchIncompleteRefusal,
		"a licensed multi-walk lens must log a NAMED reason; an empty one is swallowed by the latch and reads as silence")
	require.Contains(t, buf.String(), "lower bound exceeds one hop",
		"and the refused walk's own reason must reach the operator")
	require.NotContains(t, buf.String(), `reason=""`)

	// And it is latched like every other reason: once, not per event.
	buf.Reset()
	p.noteStaticDerivationRefusal(rs, "")
	require.NotContains(t, buf.String(), DerivationBranchIncompleteRefusal)

	// The positive counterpart, and what makes the assertion above a refusal
	// rather than the arm's only behaviour: the same lens with walks that both
	// answer is not refused at all — it derives.
	buf.Reset()
	licensed := &Pipeline{ruleID: "multi-walk-personal-licensed"}
	require.NoError(t, licensed.UseFullEngineBranches(eng, head, []ruleengine.CompiledRule{
		head, branch("MATCH (identity:identity {key: $actorKey})-[:mayBook]->(x:unit)\nRETURN x.key AS anchor, x.name AS name"),
	}))
	licensed.SetPersonalPlaneHealer(true)
	licensed.SetActorEnumerator(NewActorEnumerator(nil, nil, "identity"))
	verdictOf(licensed, licensedWiring(), cleanVerdict())
	_, ready, refusal := licensed.derivationIndexForAct(licensed.ruleState())
	require.True(t, ready, "refusal: %s", refusal)
	require.Empty(t, licensed.multiWalkDerivationRefusal(licensed.ruleState()))
}

// TestStaticDerivationRefusal_EveryArmNamesItself sweeps the reason switch and
// pins the property M2 is actually about: no reachable arm may report an empty
// reason.
//
// The latch keys on the reason string and its zero value is "", so an empty
// reason is not merely unreadable — it reads as a repeat of a report that never
// happened and is swallowed on its first and every later occurrence. The lens
// then sits on the enumerator with the operator log saying nothing at all, which
// is what a licensed lens looks like.
//
// Driving every arm rather than asserting the one that was broken: the
// multi-walk arm is the one that WAS empty, and the reason it went unnoticed for
// so long is that nothing held the others to the same rule.
func TestStaticDerivationRefusal_EveryArmNamesItself(t *testing.T) {
	eng := full.New()
	parse := func(spec string) ruleengine.CompiledRule {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		return cr
	}
	single := parse("MATCH (identity:identity {key: $actorKey})-[:mayRead]->(x:unit)\nRETURN x.key AS anchor")

	// names is the substring that identifies THIS arm's reason. Asserting
	// non-emptiness alone would let any arm pass on any other arm's sentence —
	// remove one and the switch simply falls through to the next, which is
	// exactly how the multi-walk arm went unnoticed while every other arm
	// "passed".
	arm := func(t *testing.T, name, names string, build func() (*Pipeline, ruleState, string)) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			buf := captureDefaultLogger(t)
			p, rs, licenceRefusal := build()
			p.noteStaticDerivationRefusal(rs, licenceRefusal)
			out := buf.String()
			require.Contains(t, out, "anchor derivation cannot act on this lens",
				"every arm must REPORT; an empty reason is swallowed by the latch and reads as silence")
			require.NotContains(t, out, `reason=""`,
				"every arm must NAME itself — the latch keys on the reason, and the empty string is its zero value")
			require.Contains(t, out, names,
				"and it must name ITS OWN conjunct, not fall through to a neighbour's")
		})
	}

	arm(t, "a licensed personal lens refused by the licence prints its own conjunct", "last pass failed for at least one actor", func() (*Pipeline, ruleState, string) {
		p := &Pipeline{ruleID: "licence-refused"}
		require.NoError(t, p.UseFullEngine(eng, single))
		p.SetPersonalDerivationLicence(licensedWiring(), cleanVerdict)
		return p, p.ruleState(), "the personal-plane healer's last pass failed for at least one actor"
	})

	arm(t, "a non-personal lens with an out-of-pattern input prints the generic sentence", "depends on inputs outside its compiled pattern", func() (*Pipeline, ruleState, string) {
		p := &Pipeline{ruleID: "not-personal"}
		require.NoError(t, p.UseFullEngine(eng, single))
		return p, p.ruleState(), "it is not a personal lens, and this licence speaks for the personal plane alone"
	})

	arm(t, "a lens with no standing healer names the healer", "no standing healer is installed", func() (*Pipeline, ruleState, string) {
		p := &Pipeline{ruleID: "no-healer"}
		require.NoError(t, p.UseFullEngine(eng, single))
		p.SetPatternClosedOutput(true)
		return p, p.ruleState(), ""
	})

	// The four multi-walk arms. The class was the one that WAS empty, and it is
	// now four conjuncts rather than one: each must name itself, because the
	// switch reports whichever the gate refused on and a fall-through would send
	// an operator to fix the wrong walk.
	multiWalk := func(t *testing.T, id string, branches []ruleengine.CompiledRule) (*Pipeline, ruleState, string) {
		t.Helper()
		p := &Pipeline{ruleID: id}
		require.NoError(t, p.UseFullEngineBranches(eng, branches[0], branches))
		p.SetPatternClosedOutput(true)
		p.SetPersonalPlaneHealer(true)
		require.False(t, p.ruleState().anchorHops.Complete)
		require.Empty(t, p.ruleState().anchorHops.Incomplete,
			"precondition: the single-walk index this arm does NOT read carries no reason — that absence is the defect")
		return p, p.ruleState(), ""
	}

	arm(t, "a MULTI-WALK lens with a walk that cannot answer names that conjunct", DerivationBranchIncompleteRefusal, func() (*Pipeline, ruleState, string) {
		return multiWalk(t, "multi-walk-incomplete", []ruleengine.CompiledRule{
			single, parse("MATCH (identity:identity {key: $actorKey})-[:mayRead*2..3]->(x:unit)\nRETURN x.key AS anchor"),
		})
	})

	arm(t, "a MULTI-WALK lens whose walks anchor differently names the disagreement", DerivationBranchAnchorDisagreementRefusal, func() (*Pipeline, ruleState, string) {
		return multiWalk(t, "multi-walk-disagree", []ruleengine.CompiledRule{
			single, parse("MATCH (org:org {key: $actorKey})-[:owns]->(x:unit)\nRETURN x.key AS anchor"),
		})
	})

	arm(t, "a MULTI-WALK lens holding no per-branch graph at all names that", DerivationNoBranchIndexRefusal, func() (*Pipeline, ruleState, string) {
		p, rs, _ := multiWalk(t, "multi-walk-no-graphs", []ruleengine.CompiledRule{
			single, parse("MATCH (identity:identity {key: $actorKey})-[:mayBook]->(x:unit)\nRETURN x.key AS anchor"),
		})
		// The pair's fail-open shape: graphs gone with nothing refusing them,
		// which is what a ruleState field losing its round-trip line looks like.
		rs.anchorHopsPerBranch = nil
		rs.anchorHopsPerBranchRefusal = ""
		return p, rs, ""
	})

	arm(t, "a MULTI-WALK lens whose walks answer names the anchor label", derivationAnchorLabelRefusal, func() (*Pipeline, ruleState, string) {
		// Every branch conjunct clears, so what is left is the one only a live
		// read can answer: this pipeline has no enumerator, so no anchor label
		// can be the enumerator's actor type.
		return multiWalk(t, "multi-walk-label", []ruleengine.CompiledRule{
			single, parse("MATCH (identity:identity {key: $actorKey})-[:mayBook]->(x:unit)\nRETURN x.key AS anchor"),
		})
	})

	arm(t, "a single-walk graph that declines without naming a conjunct is still named", derivationUnnamedIndexRefusal, func() (*Pipeline, ruleState, string) {
		// The belt to every named conjunct, and the reason it is its own
		// constant: the multi-walk sentence would be a WRONG reason here — this
		// lens compiles to one walk. No arm of AnchorHopIndex produces a graph
		// that is incomplete and silent, so the vector is authored.
		p := &Pipeline{ruleID: "unnamed-index"}
		require.NoError(t, p.UseFullEngine(eng, single))
		p.SetPatternClosedOutput(true)
		p.SetPersonalPlaneHealer(true)
		rs := p.ruleState()
		rs.anchorHops = full.HopIndex{}
		require.False(t, rs.anchorHops.Complete)
		require.Empty(t, rs.anchorHops.Incomplete)
		return p, rs, ""
	})

	arm(t, "a cypher-level refusal carries the index's own reason", "lower bound exceeds one hop", func() (*Pipeline, ruleState, string) {
		// A ranged hop the seeding cannot cover: the index refuses and names
		// itself, so this arm is the control — it proves the sweep above is not
		// passing because every arm happens to take the same branch.
		p := &Pipeline{ruleID: "unseedable-ranged-hop"}
		unreadable := parse("MATCH (identity:identity {key: $actorKey})-[:mayRead*2..3]->(x:unit)\nRETURN x.key AS anchor")
		require.NoError(t, p.UseFullEngine(eng, unreadable))
		p.SetPatternClosedOutput(true)
		p.SetPersonalPlaneHealer(true)
		require.NotEmpty(t, p.ruleState().anchorHops.Incomplete,
			"precondition: this arm's source DOES carry a reason, unlike the multi-walk one above")
		return p, p.ruleState(), ""
	})

	arm(t, "an unresolved `*` expansion names the position it refused on", "taxonomy-expansion sigil", func() (*Pipeline, ruleState, string) {
		// Posed the way the taxonomy tests pose it: a COMPLETE index whose `*`
		// position carries no resolved concrete set, which is what a rule state
		// published while the resolver could not answer carries. The arm is
		// reached only once the completeness arm above has passed, so it needs a
		// graph that is otherwise fine.
		p := &Pipeline{ruleID: "unresolved-expansion"}
		require.NoError(t, p.UseFullEngine(eng, single))
		p.SetPatternClosedOutput(true)
		p.SetPersonalPlaneHealer(true)
		rs := p.ruleState()
		require.True(t, rs.anchorHops.Complete, "precondition: the completeness arm must not claim this one")
		rs.anchorHops.LabelExpand = make([]bool, len(rs.anchorHops.Labels))
		rs.anchorHops.LabelExpand[rs.anchorHops.Anchor] = true
		rs.anchorHops.Expanded = nil
		require.GreaterOrEqual(t, rs.anchorHops.UnresolvedExpansionPosition(), 0,
			"precondition: this state really carries an unresolved `*` position")
		return p, rs, ""
	})

	arm(t, "the default arm names the anchor label", "is not the enumerator's actor type", func() (*Pipeline, ruleState, string) {
		p := &Pipeline{ruleID: "wrong-anchor-label"}
		require.NoError(t, p.UseFullEngine(eng, single))
		p.SetPatternClosedOutput(true)
		p.SetPersonalPlaneHealer(true)
		return p, p.ruleState(), ""
	})
}

// TestPersonalSweepVerdict_VocabularyIsClosed default-denies the verdict
// vocabulary, the way the corpus censuses default-deny their refusal conjuncts.
//
// The token is written onto every personal lens's health entry, declared in
// healthwire.Entry's doc, documented in the component doc, and read by an
// operator and by Loupe. A value Summary produces that nobody declared reaches
// all four as a string nobody reviewed; a constant nobody produces is a claim
// about a state that cannot happen. Driving every arm and comparing the SET is
// what catches both directions — a table of expected strings would only catch
// the first.
func TestPersonalSweepVerdict_VocabularyIsClosed(t *testing.T) {
	clean := PersonalHealerVerdict{
		StartedAt: time.Now(), CompletedAt: time.Now(),
		PopulationReadable: true, InstanceCount: 1, InstanceCountReadable: true,
	}
	with := func(mutate func(*PersonalHealerVerdict)) PersonalHealerVerdict {
		v := clean
		mutate(&v)
		return v
	}

	// Every arm of Summary, driven — including the two the fix round added, and
	// the edge-spans-deployment shortcut, which must NOT mint a token of its own.
	produced := map[string]struct{}{}
	for _, v := range []PersonalHealerVerdict{
		clean,
		with(func(v *PersonalHealerVerdict) { v.CompletedAt = time.Time{} }),
		with(func(v *PersonalHealerVerdict) { v.PopulationReadable = false }),
		with(func(v *PersonalHealerVerdict) { v.Failed = 1 }),
		with(func(v *PersonalHealerVerdict) { v.InstanceCountReadable = false }),
		with(func(v *PersonalHealerVerdict) { v.InstanceCount = 0 }),
		with(func(v *PersonalHealerVerdict) { v.InstanceCount = 2 }),
		with(func(v *PersonalHealerVerdict) {
			v.EdgeSpansDeployment = true
			v.InstanceCount, v.InstanceCountReadable = 9, false
		}),
	} {
		produced[v.Summary()] = struct{}{}
	}
	// The one token Summary can never produce: a stalled sweeper writes nothing
	// at all, so health.LagPoller is its producer. It belongs to the same closed
	// set — the field has one vocabulary, not one per writer.
	produced[PersonalHealerVerdictStale] = struct{}{}

	declared := map[string]struct{}{
		PersonalHealerVerdictClean:                {},
		PersonalHealerVerdictNeverPassed:          {},
		PersonalHealerVerdictFailed:               {},
		PersonalHealerVerdictPopulationUnreadable: {},
		PersonalHealerVerdictInstancesUnreadable:  {},
		PersonalHealerVerdictInstancesImpossible:  {},
		PersonalHealerVerdictMultipleInstances:    {},
		PersonalHealerVerdictStale:                {},
	}

	sorted := func(m map[string]struct{}) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	require.Equal(t, sorted(declared), sorted(produced),
		"the verdict vocabulary is written onto every personal lens's health entry and declared in healthwire.Entry's doc and the component doc — a token produced but not declared reaches all three unreviewed, and one declared but never produced is a claim about a state that cannot happen")

	// And every declared token must be non-empty, since "" already means "this
	// lens has no sweep verdict at all" on the entry.
	for token := range declared {
		require.NotEmpty(t, token, "the empty string already means 'no verdict on this lens'")
	}
}

// TestDerivationIndexForAct_IndexIsAskedBeforeTheLicence pins the ORDER, which
// is both a cost property and a legibility one.
//
// Cost: the licence is far the dearer of the two — four mutex-guarded process
// censuses, a verdict snapshot and a clock read — while the index is a handful
// of field reads off the published rule state. Asked licence-first, a multi-walk
// personal lens paid the whole predicate on every CDC event before being refused
// on a zero HopIndex, and that is seven of the corpus's nineteen personal
// cyphers including the deepest backlog of the lot.
//
// Legibility: a lens whose index refuses has no derived set for a licence to
// speak about, so reporting a licence conjunct for it sends an operator to fix
// process wiring that would change nothing. The observable is the refusal handed
// back — empty means the index refused first.
func TestDerivationIndexForAct_IndexIsAskedBeforeTheLicence(t *testing.T) {
	eng := full.New()
	parse := func(spec string) ruleengine.CompiledRule {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		return cr
	}
	head := parse(personalLicenceSpec)

	// A multi-walk personal lens one of whose walks carries a ranged hop the
	// seeding cannot cover — so its per-branch graph set refuses — and whose
	// licence would ALSO refuse. Both conjuncts are live; only one may be
	// reported, and it must be the index's.
	p := &Pipeline{ruleID: "order-multi-walk"}
	require.NoError(t, p.UseFullEngineBranches(eng, head, []ruleengine.CompiledRule{
		head,
		parse("MATCH (identity:identity {key: $actorKey})-[:mayRead*2..3]->(x:unit)\nRETURN x.key AS anchor"),
	}))
	p.SetPersonalPlaneHealer(true)
	failing := cleanVerdict()
	failing.Failed = 3
	verdictOf(p, licensedWiring(), failing)

	rs := p.ruleState()
	require.NotEmpty(t, rs.anchorHopsPerBranchRefusal, "precondition: the index really refuses this lens")
	licensed, licRefusal := p.personalDerivationLicence(rs)
	require.False(t, licensed, "precondition: the LICENCE would refuse it too")
	require.Contains(t, licRefusal, "last pass failed")

	_, ready, refusal := p.derivationIndexForAct(rs)
	require.False(t, ready)
	require.Empty(t, refusal,
		"the index is asked first, so no licence conjunct is computed or reported for a lens that has no derived set at all")

	// And the reason an operator reads is the index's, not the licence's.
	buf := captureDefaultLogger(t)
	p.noteStaticDerivationRefusal(rs, refusal)
	require.Contains(t, buf.String(), DerivationBranchIncompleteRefusal)
	require.NotContains(t, buf.String(), "last pass failed",
		"a licence conjunct must not mask the walk that cannot answer — fixing the healer would change nothing for this lens")

	// The control: a SINGLE-walk lens in the same state does reach the licence,
	// so the empty refusal above is the ordering and not a predicate that stopped
	// answering.
	single := &Pipeline{ruleID: "order-single-walk"}
	require.NoError(t, single.UseFullEngine(eng, head))
	single.SetPersonalPlaneHealer(true)
	verdictOf(single, licensedWiring(), failing)
	single.SetActorEnumerator(NewActorEnumerator(nil, nil, "identity"))

	rs = single.ruleState()
	require.True(t, rs.anchorHops.Complete, "precondition: this one's index answers")
	_, ready, refusal = single.derivationIndexForAct(rs)
	require.False(t, ready)
	require.Contains(t, refusal, "last pass failed",
		"once the index answers, the licence is asked and its conjunct IS the reportable reason")
}
