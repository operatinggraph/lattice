package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
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
	registerPersonalHealer(grantchange.New(), rule.ID, p)

	byType, _, scoped := p.WalkScope()
	require.Truef(t, scoped,
		"registration arms the personal plane's healer, which licenses the scope (refusal: %q)", p.WalkScopeRefusal())
	require.Empty(t, p.WalkScopeRefusal())
	require.NotEmpty(t, byType,
		"a corpus personal lens's pattern graph must yield a real relation scope, not an empty one")
}
