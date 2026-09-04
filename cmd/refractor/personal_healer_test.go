package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// corpusPersonalLens returns one Personal lens the installed corpus really
// ships, as the *lens.Rule main.go's install switch dispatches on plus its
// executable cypher.
//
// A shipped lens rather than a hand-written one, because the claim under test is
// about the corpus that runs: the personal plane's standing healer is what
// licenses the pattern-scoped walk for these lenses, and a synthetic cypher
// would pin the mechanism without pinning that the mechanism reaches them.
func corpusPersonalLens(t *testing.T) (*lens.Rule, string) {
	t.Helper()
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		require.Truef(t, ok, "registered package %q must resolve", name)
		expanded, err := def.ExpandReadGrantWalks()
		require.NoErrorf(t, err, "%s read-grant walks must compose", name)
		for _, l := range expanded.Lenses {
			if !l.Personal {
				continue
			}
			spec := l.Spec
			if spec == "" && len(l.SpecBranches) > 0 {
				spec = l.SpecBranches[0]
			}
			if spec == "" {
				continue
			}
			return personalRuleFor(l), spec
		}
	}
	t.Fatal("the installed corpus ships no Personal lens — this test's subject is gone, not merely unfound")
	return nil, ""
}

// personalRuleFor maps a package's declared LensSpec onto the *lens.Rule
// cmd/refractor's install switch reads, the same mapping pkgmgr performs on its
// way into the lens meta-vertex. Only the fields that switch and
// projection.InstallPersonalLens actually read are set.
func personalRuleFor(l pkgmgr.LensSpec) *lens.Rule {
	keyFields := l.IntoKey
	if len(keyFields) == 0 {
		keyFields = []string{"key"}
	}
	return &lens.Rule{
		ID:             "healer-" + l.CanonicalName,
		CanonicalName:  l.CanonicalName,
		ProjectionKind: l.ProjectionKind,
		ResolvedEngine: ruleengine.EngineFull,
		Into: lens.IntoConfig{
			Target:   "nats_subject",
			Bucket:   l.Bucket,
			Key:      lens.KeyField(keyFields),
			Personal: true,
		},
	}
}

// TestPersonalLensActivationArmsTheWalkScope pins the one production line that
// turns the pattern-scoped actor walk on for the whole personal corpus:
// registerPersonalHealer, the seam main's activation arm calls to register the
// lens with the grant-change edge and arm the pipeline in one step.
//
// Scoping the walk gives up the incidental reprojection that heals a row lost
// out of band, and auth-plane-projection-latency-design.md §4.2 licenses that
// only where a standing healer replaces it. A Personal Lens never receives a
// SweepPlan, so its healer is the personal plane's — grantchange.PersonalSweeper
// plus the D1 grant-change edge — and pipeline.walkScopeFor learns of it from
// that one call. Drop the line and every personal lens silently reverts to the
// relation-blind walk with nothing failing; hence this test.
//
// WHAT IT DOES AND DOES NOT CLAIM. It runs the personal stretch of
// startPipeline's activation sequence — compile, build the pipeline,
// projection.InstallPersonalLens, then RegisterPersonal and the arming call in
// main.go's own order — against a real pipeline and a real corpus lens. It is
// the longest run of that sequence a test can drive, for the same reason
// activateLens's doc gives: startPipeline is a closure inside main() that
// captures a booted process (a live NATS connection, the control service, the
// reprojector, durable consumer registration), so no test reaches it. What that
// leaves unpinned is that main() still MAKES the call; pinning that needs the
// registration-and-arming pair extracted into a named function both main() and a
// test can call.
func TestPersonalLensActivationArmsTheWalkScope(t *testing.T) {
	rule, spec := corpusPersonalLens(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoErrorf(t, err, "%s must parse", rule.CanonicalName)
	rule.CompiledRule = cr
	require.True(t, projection.IsPersonalLens(rule),
		"precondition: main.go's install switch must take the personal arm for this rule")

	// The adapter is the harness's, not the lens's: InstallPersonalLens takes no
	// adapter at all, and the real nats_subject one needs a live connection. What
	// this test reads — the walk scope and its refusal — is derived from the
	// compiled rule and the pipeline's healer state, neither of which any adapter
	// touches.
	p, err := pipeline.New(rule.ID, rule.Into.Target, "CORE", nil, nil, newKVAdapter(t), nil)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, cr))
	require.True(t,
		projection.InstallPersonalLens(p, rule, nil, nil, nil, nil, false, logger),
		"a corpus Personal lens must install through its own installer")

	// The negative, before the arming call. Without it this test would pass on a
	// pipeline that scopes for some other reason, and the line it exists to pin
	// could be deleted with nothing failing.
	require.Nil(t, p.Sweeper(), "a Personal Lens never receives a SweepPlan — the auth-plane arm cannot be what licenses it")
	_, _, scoped := p.WalkScope()
	require.False(t, scoped, "before registration the lens has no standing healer")
	require.Equal(t, "no standing healer", p.WalkScopeRefusal())

	// The production seam itself: main's activation arm calls exactly this.
	registerPersonalHealer(grantchange.New(), nil, nil, rule.ID, p, pipeline.PersonalDerivationWiring{PersonalLens: true})

	byType, _, scoped := p.WalkScope()
	require.Truef(t, scoped,
		"registration arms the personal plane's healer, which licenses the scope (refusal: %q)", p.WalkScopeRefusal())
	require.Empty(t, p.WalkScopeRefusal())
	require.NotEmpty(t, byType,
		"a corpus personal lens's pattern graph must yield a real relation scope, not an empty one")
}

// TestPersonalLensActivationAssertsTheDerivationLicence pins the OTHER half of
// the same production seam (personal-lens-derivation-licence-design.md §4.4c).
//
// The licence's wiring conjuncts are facts only the host holds, and its live
// conjuncts read the sweeper through an accessor only the host can inject —
// pipeline cannot import grantchange. So registerPersonalHealer is the one place
// they arrive, and if the assertion is dropped the pipeline reads as
// "not a personal lens" and every personal lens silently keeps the enumerator,
// with nothing failing.
//
// It also pins the two directions of the sink census that conjunct 1 reads,
// because the census is a PROCESS-level fact this function samples at the
// registration call rather than a value main passes in.
func TestPersonalLensActivationAssertsTheDerivationLicence(t *testing.T) {
	rule, spec := corpusPersonalLens(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	rule.CompiledRule = cr

	newPipeline := func(t *testing.T) *pipeline.Pipeline {
		t.Helper()
		p, err := pipeline.New(rule.ID, rule.Into.Target, "CORE", nil, nil, newKVAdapter(t), nil)
		require.NoError(t, err)
		require.NoError(t, p.UseFullEngine(eng, cr))
		require.True(t, projection.InstallPersonalLens(p, rule, nil, nil, nil, nil, false, logger))
		return p
	}

	fullyWired := pipeline.PersonalDerivationWiring{
		PersonalLens:            true,
		ReadGateWired:           true,
		GrantReprojectorWired:   true,
		InterestFilterInstalled: true,
		InterestEdgeArmed:       func() bool { return true },
	}

	t.Run("the host's assertion reaches the pipeline, and a live sweeper is its verdict accessor", func(t *testing.T) {
		p := newPipeline(t)
		require.Equal(t, pipeline.PersonalHealerVerdict{}, p.PersonalHealerVerdictNow(),
			"before registration the licence has nothing to read, which is the refusing answer")

		r := grantchange.New()
		r.RegisterPersonal(rule.ID, &noopPersonalPipeline{})
		sweeper := grantchange.NewPersonalSweeper(r, &staticKeyLister{}, &staticKeyLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}})
		sweeper.Sweep(context.Background())

		registerPersonalHealer(r, sweeper, nil, rule.ID, p, fullyWired)

		v := p.PersonalHealerVerdictNow()
		require.False(t, v.CompletedAt.IsZero(),
			"the accessor must read the SWEEPER's own verdict, not a copy taken at registration")
		require.Equal(t, 1, v.InstanceCount)
		require.Equal(t, pipeline.PersonalHealerVerdictClean, v.Summary())
	})

	t.Run("registration earns a pass begun after it, so the licence grants without waiting a tick", func(t *testing.T) {
		// The seam the sweeper's own TestPersonalSweep_RunSweepsImmediately pins
		// from below, driven here through the production sequence: main starts
		// the sweep loop BEFORE the lens source activates anything, so that
		// loop's immediate pass runs over an empty registry and records nothing,
		// and conjunct 3 additionally requires a pass BEGUN after this lens's
		// RegisteredAt. Both are why registration nudges the healer — once from
		// the registry insert, once from registerPersonalHealer after the stamp,
		// since the stamp lands after the insert and a pass starting between the
		// two would be refused for the very lens that kicked it off.
		//
		// The interval is an hour, so a licence granted inside this test cannot
		// have come from a tick. Without the nudge it waits that hour, and every
		// personal lens spends the wait on the relation-blind enumerator —
		// precisely when the backlog is deepest.
		p := newPipeline(t)
		r := grantchange.New()
		sweeper := grantchange.NewPersonalSweeper(r, &staticKeyLister{}, &staticKeyLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}})
		sweeper.SetBounds(0, time.Hour)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go sweeper.Run(ctx)

		registerPersonalHealer(r, sweeper, nil, rule.ID, p, fullyWired)

		require.Eventually(t, func() bool {
			licensed, _, _, _ := p.PersonalDerivationStatus()
			return licensed
		}, 5*time.Second, time.Millisecond,
			"the licence must grant on a pass the registration itself kicked off, not on the next tick")
	})

	t.Run("a nil sweeper leaves the live conjuncts refusing", func(t *testing.T) {
		p := newPipeline(t)
		registerPersonalHealer(grantchange.New(), nil, nil, rule.ID, p, fullyWired)
		require.Equal(t, pipeline.PersonalHealerVerdictNeverPassed, p.PersonalHealerVerdictNow().Summary())
	})

	t.Run("the sink census is READ LIVE by the licence, in both directions", func(t *testing.T) {
		// The §4.3(d) amendment's consumer half, end to end. The census itself is
		// pinned where it is written (projection's
		// TestReadGrantSinkCensus_CountsTheSinkLessProducers); what this pins is
		// that registerPersonalHealer hands the licence a live ACCESSOR onto it
		// rather than a boolean sampled here — a producer can install after this
		// lens registered (a hot lens install), and a sample would answer about
		// a process that no longer exists, in the fail-open direction.
		require.Empty(t, projection.ReadGrantProducersWithoutSink(),
			"precondition: this process has no sink-less producer standing")

		p := newPipeline(t)
		registerPersonalHealer(grantchange.New(), nil, nil, rule.ID, p, fullyWired)
		_, refusal := p.PersonalDerivationLicence()
		require.NotContains(t, refusal, "grant-change sink",
			"with a clean census, conjunct 1's sink arm must not be the refusal")

		// The refusing direction, driven by INSTALLING a real cap-read producer
		// with no sink through the production installer rather than by handing
		// the wiring struct a false — and installed AFTER the registration, which
		// is the case a sampled boolean gets wrong. If registerPersonalHealer
		// hardcoded this conjunct true, or sampled it, the clean direction above
		// would pass exactly as it does now.
		installSinklessCapReadProducer(t, "sinkless-producer")

		licensed, refusal := p.PersonalDerivationLicence()
		require.False(t, licensed,
			"a producer installed after this lens registered must revoke the licence on the SAME pipeline, with no re-registration")
		require.Contains(t, refusal, "cap-read producer is installed with no grant-change sink")
	})

	t.Run("conjunct 2 speaks for the InterestReconciler as well as the control plane", func(t *testing.T) {
		// The fourth Interest Set writer. cmd/refractor constructs it inside the
		// very activation arm that registers the first personal lens, so it
		// cannot be sampled at registration — the licence reads the process
		// census live, and a reconciler built afterwards with no sink revokes.
		p := newPipeline(t)
		wiring := fullyWired
		wiring.InterestEdgeArmed = func() bool {
			return health.InterestReconcilersWithoutSink() == 0
		}
		registerPersonalHealer(grantchange.New(), nil, nil, rule.ID, p, wiring)
		_, refusal := p.PersonalDerivationLicence()
		require.NotContains(t, refusal, "Interest Set has no change edge")

		reconciler := health.NewInterestReconciler(nil, nil, "SYNC-late", nil, nil)
		t.Cleanup(func() { reconciler.SetInterestChangeSink(func(string) {}) })
		licensed, refusal := p.PersonalDerivationLicence()
		require.False(t, licensed,
			"a reconciler CONSTRUCTED after registration and never armed must revoke the licence — its reap widens IsRelevant and announces nothing")
		require.Contains(t, refusal, "the Interest Set has no change edge")

		reconciler.SetInterestChangeSink(func(string) {})
		_, refusal = p.PersonalDerivationLicence()
		require.NotContains(t, refusal, "Interest Set has no change edge")
	})
}

// noopPersonalPipeline is a registry member the sweeper can walk without a real
// pipeline behind it — hasPersonal is what gates the pass, and the verdict this
// test reads is about the pass, not about any one lens's reprojection.
type noopPersonalPipeline struct{}

func (noopPersonalPipeline) ReprojectPersonalActor(context.Context, string, pipeline.PublishScope) error {
	return nil
}
func (noopPersonalPipeline) OrderingTokenSeeded() bool { return true }
func (noopPersonalPipeline) RecordGrantReprojectIssue(context.Context, string, string) error {
	return nil
}
func (noopPersonalPipeline) SetPersonalSweepProgress(context.Context, string, time.Time, uint64, string) error {
	return nil
}

// staticKeyLister answers one fixed key set for both the identity population and
// the Health-KV instance census.
type staticKeyLister struct{ keys []string }

func (l *staticKeyLister) ListKeysFilter(_ context.Context, _, _ string, _ int) ([]string, string, error) {
	return append([]string(nil), l.keys...), "", nil
}

// installSinklessCapReadProducer installs one D1 read-grant producer through the
// production installer with NO grant-change sink offered — the shape the §4.3(d)
// amendment deliberately admits (refusing it would turn a host omission into an
// auth-plane outage on the primordial capabilityRead lens) and which the
// personal derivation licence is where the narrowing refuses instead.
func installSinklessCapReadProducer(t *testing.T, ruleID string) {
	t.Helper()
	t.Cleanup(func() { projection.ForgetReadGrantProducer(ruleID) })

	eng := full.New()
	cr, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:mayRead]->(anchor:task)
RETURN identity.key AS actorKey, anchor.key AS anchorId, collect(anchor.key) AS readableAnchors
`)
	require.NoError(t, err)
	r := &lens.Rule{
		ID:             ruleID,
		CanonicalName:  ruleID,
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into:           lens.IntoConfig{Target: "nats_kv", Bucket: projection.AuthPlaneBucket, Key: lens.KeyField{"key"}},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    string(projection.EmptyDelete),
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	}
	adpt := newKVAdapter(t)
	p, err := pipeline.New(r.ID, r.Into.Target, "CORE", nil, nil, adpt, nil)
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	require.True(t,
		projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, logger),
		"the sink-less producer must INSTALL — that is the amendment's whole shape")
	require.False(t, p.HasGrantChangeSink())
}

// TestNewInstanceToken_CannotEscapeTheInstanceCensus pins the assertion beside
// the instance id's construction.
//
// The heartbeat lands at health.refractor.<instance> and the personal derivation
// licence counts live Refractors with health.refractor.* — a single-token
// wildcard. A token carrying a `.` writes a key that filter does not match, so
// the instance is invisible to its own census, and the direction is the bad one:
// a second such Refractor UNDER-counts and every personal lens on the first one
// keeps narrowing on an edge that no longer spans the deployment.
func TestNewInstanceToken_CannotEscapeTheInstanceCensus(t *testing.T) {
	for range 64 {
		token := newInstanceToken()
		require.NotContains(t, token, ".",
			"a dotted instance token writes a heartbeat key health.refractor.* cannot see")
		require.NotContains(t, token, "*")
		require.NotContains(t, token, ">")
		require.True(t, strings.HasPrefix(health.InstanceKeyPrefix+token, health.InstanceKeyPrefix))
		// The property stated where it actually bites: the key this token
		// produces must have exactly one token after the prefix, which is what
		// the census filter matches.
		require.NotContains(t, strings.TrimPrefix(health.InstanceKeyPrefix+token, health.InstanceKeyPrefix), ".",
			"the segment after health.refractor. must be one subject token")
	}
}
