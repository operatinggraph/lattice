package main

// The activation-time half of the business plane's neighbour-retraction rule
// (secure-plain-lens-retraction-and-audit-design.md §4.4): the gate that refuses
// a plain business lens whose rows a neighbour can drop and nothing can retract,
// and the shared-target check that keeps a target diff from deleting a sibling
// lens's rows.
//
// The corpus-wide statement of the same rule is
// internal/refractor/plain_retraction_transport_corpus_census_test.go, which is
// where an author meets it. These are the runtime backstop's own vectors.

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// untransportedNeighbourSpec is the shape the gate exists for: a REQUIRED hop
// to a neighbour (so a `manages` unwire drops the row) whose key columns bind
// that neighbour (so the rows do not partition by anchor and the derivation
// licence's closure conjunct refuses), with no target diff declared.
const untransportedNeighbourSpec = `
MATCH (app:leaseapp)
MATCH (app)-[:appliesToUnit]->(u:unit)
MATCH (u)<-[:manages]-(landlord:identity)
RETURN nanoIdFromKey(app.key) AS app_id, nanoIdFromKey(landlord.key) AS landlord_id
`

// anchorClosedNeighbourSpec is the same required hop keyed by the ANCHOR alone,
// so the derivation licence admits it. It is the positive vector: without it a
// green refusal above could equally come from a gate that refuses every lens
// with a hop in it.
const anchorClosedNeighbourSpec = `
MATCH (app:leaseapp)
MATCH (app)-[:appliesToUnit]->(u:unit)
RETURN app.key AS key, u.name AS unit_name
`

// anchorOnlySpec has no neighbour at all — the gate must pass it with no
// transport, because a lens no neighbour can orphan is owed none.
const anchorOnlySpec = `
MATCH (app:leaseapp)
RETURN app.key AS key, app.data.status AS status
`

func businessRule(id, name string, key ...string) *lens.Rule {
	if len(key) == 0 {
		key = []string{"key"}
	}
	return &lens.Rule{
		ID:             id,
		CanonicalName:  name,
		ResolvedEngine: ruleengine.EngineFull,
		Into:           lens.IntoConfig{Target: "nats_kv", Bucket: "retraction-gate-test", Key: lens.KeyField(key)},
	}
}

// TestActivation_RefusesUntransportedNeighbourLens drives the gate's own
// predicate over the three shapes that decide it, and proves the refusal
// reaches the lens's health entry — the only record a lens that never
// activates leaves behind.
func TestActivation_RefusesUntransportedNeighbourLens(t *testing.T) {
	t.Run("a required hop with no transport is refused", func(t *testing.T) {
		r := businessRule("gate-untransported", "gateUntransported", "app_id", "landlord_id")
		require.False(t, projection.IsAuthPlane(r), "precondition: the gate is scoped to the business plane")
		require.True(t, isPlainProjectionLens(r), "precondition: the gate is scoped to plain projection lenses")

		p := activateLens(t, r, untransportedNeighbourSpec, newKVAdapter(t))
		refusal := retractionTransportRefusal(p, r)
		require.NotEmpty(t, refusal, "a lens a neighbour can orphan, with no transport, must not activate")
		assert.Contains(t, refusal, "landlord", "the refusal names the variable carrying the dependency")
		assert.Contains(t, refusal, "partition by anchor",
			"the refusal carries the static predicate's own reason, so an author is told which conjunct to move")
	})

	t.Run("the same hop, keyed by the anchor, is admitted", func(t *testing.T) {
		r := businessRule("gate-anchor-closed", "gateAnchorClosed")
		p := activateLens(t, r, anchorClosedNeighbourSpec, newKVAdapter(t))
		v := p.PlainRetractionTransport(projection.IsAuthPlane(r))
		require.True(t, v.DependsOnNeighbour, "the positive vector must carry the same obligation the refused one does")
		require.Equal(t, pipeline.RetractionTransportDerivation, v.Transport)
		assert.Empty(t, retractionTransportRefusal(p, r))
	})

	t.Run("a lens no neighbour can orphan is admitted with no transport", func(t *testing.T) {
		r := businessRule("gate-anchor-only", "gateAnchorOnly")
		p := activateLens(t, r, anchorOnlySpec, newKVAdapter(t))
		v := p.PlainRetractionTransport(projection.IsAuthPlane(r))
		require.False(t, v.DependsOnNeighbour)
		assert.Empty(t, retractionTransportRefusal(p, r),
			"a lens whose rows no neighbour can drop owes no transport")
	})

	t.Run("a classifier answer that is not exhaustive is refused", func(t *testing.T) {
		// The fail-open direction the exhaustive flag exists to close: the
		// WHERE reads an alias whose provenance the resolver does not model, so
		// whether a neighbour can drop this lens's rows is UNKNOWN. Read as
		// "it cannot", the lens activates with no transport and no obligation
		// anyone can see.
		const unresolvableAliasSpec = `
MATCH (app:leaseapp)
WITH app, CASE WHEN app.data.status = 'open' THEN 1 ELSE 0 END AS flag
WHERE flag = 1
RETURN app.key AS key, flag AS flag
`
		r := businessRule("gate-unresolvable", "gateUnresolvable")
		p := activateLens(t, r, unresolvableAliasSpec, newKVAdapter(t))
		v := p.PlainRetractionTransport(projection.IsAuthPlane(r))
		require.False(t, v.Exhaustive, "precondition: this shape must be one the classifier cannot answer")

		refusal := retractionTransportRefusal(p, r)
		require.NotEmpty(t, refusal)
		assert.Contains(t, refusal, "could not be derived")
	})

	t.Run("the refusal is recorded on the lens's health entry", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "gate-untransported")
		r := businessRule("gate-untransported", "gateUntransported", "app_id", "landlord_id")
		p := activateLens(t, r, untransportedNeighbourSpec, newKVAdapter(t))

		refuseLens(context.Background(), discardLogger(), reporter, r,
			"neighbour-retraction transport", retractionTransportRefusal(p, r))

		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(1), status.ErrorCount)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "neighbour-retraction transport REFUSED activation",
			"a lens that never activates has no heartbeat status, so the entry is the only account of why it is dark")
		assert.Contains(t, *status.LastError, "landlord")
	})
}

// TestSharedTargetDiffRefusal covers the shared-NATS-KV-bucket rule as
// activation asks it, siblings described the way the registry describes them:
// by what their RUNNING pipeline lists and writes.
//
// The rule itself lives in internal/refractor/projection because the corpus
// census holds the installed corpus to it, and a census restating a rule agrees
// with a broken one. These are the vectors that decide a live activation.
func TestSharedTargetDiffRefusal(t *testing.T) {
	sharedBucket := func(id, name string, diff bool) *lens.Rule {
		r := businessRule(id, name)
		r.Into.Bucket = "shared-bucket-test"
		r.Into.DiffRetraction = diff
		return r
	}
	withPattern := func(r *lens.Rule, pattern string) *lens.Rule {
		r.Output = &lens.OutputDescriptorSpec{
			AnchorType:       "leaseapp",
			OutputKeyPattern: pattern,
			BodyColumns:      []string{"amountCents"},
			EmptyBehavior:    "delete",
		}
		return r
	}
	// sibling describes an already-running lens the way siblingLensOf reads one
	// off the registry: its installed diff posture, and the descriptor its
	// pipeline was activated from.
	sibling := func(name string, diff bool, installedPrefix, pattern string) projection.SiblingLens {
		s := projection.SiblingLens{
			CanonicalName:        name,
			DiffRetraction:       diff,
			DiffRetractionPrefix: installedPrefix,
		}
		if pattern != "" {
			s.Output = withPattern(sharedBucket("x", name, diff), pattern).Output
		}
		return s
	}

	t.Run("two disjoint prefixes share the bucket", func(t *testing.T) {
		// The positive vector, first: without it every refusal below could
		// equally come from a rule that refuses all sharing.
		newcomer := withPattern(sharedBucket("rent", "oneBillRentEntries", true), "onebill.rent.{actorSuffix}")
		prefix, refusal := projection.SharedTargetDiffRefusal(newcomer, []projection.SiblingLens{
			sibling("oneBillCafeEntries", true, "onebill.cafe.", "onebill.cafe.{actorSuffix}"),
		})
		require.Empty(t, refusal, "two lenses whose key spaces cannot contain each other's rows may share one bucket")
		assert.Equal(t, "onebill.rent.", prefix)
	})

	t.Run("a diff lens loading first on an empty bucket is scoped anyway", func(t *testing.T) {
		// The order-independence the rule rests on. Deriving the prefix only
		// once a sibling is present leaves this pipeline listing the whole
		// bucket for the life of the process — nothing re-scopes a running
		// pipeline when the sibling arrives, so the sibling's own activation
		// check would be deciding about a diff that is already unscoped.
		prefix, refusal := projection.SharedTargetDiffRefusal(
			withPattern(sharedBucket("rent", "oneBillRentEntries", true), "onebill.rent.{actorSuffix}"), nil)
		require.Empty(t, refusal)
		assert.Equal(t, "onebill.rent.", prefix,
			"the scoping is installed whether or not a sibling is there to need it yet")
	})

	t.Run("an unshared target with no derivable prefix diffs the whole listing", func(t *testing.T) {
		prefix, refusal := projection.SharedTargetDiffRefusal(sharedBucket("solo", "solo", true), nil)
		assert.Empty(t, refusal, "a lens that owns its target diffs the whole listing, exactly as it always has")
		assert.Empty(t, prefix)
	})

	t.Run("nesting prefixes refuse the newcomer", func(t *testing.T) {
		// KeyPrefix admits a prefix that CONTAINS another lens's key space —
		// its own doc's `cap.` over `cap.roles.` — so a scoped diff is only as
		// sound as the disjointness of the prefixes on the bucket.
		newcomer := withPattern(sharedBucket("caps", "capsLens", true), "cap.{actorSuffix}")
		prefix, refusal := projection.SharedTargetDiffRefusal(newcomer, []projection.SiblingLens{
			sibling("capRolesLens", true, "cap.roles.", "cap.roles.{actorSuffix}"),
		})
		require.NotEmpty(t, refusal)
		assert.Contains(t, refusal, "nests")
		assert.Contains(t, refusal, "capRolesLens")
		assert.Empty(t, prefix)
	})

	t.Run("a sibling with no locatable key space refuses a prefixed diff", func(t *testing.T) {
		// Disjointness unprovable is the same answer as disjointness disproved:
		// "we cannot tell where its rows are" read as "not where ours are" is
		// the fail-open direction.
		newcomer := withPattern(sharedBucket("rent", "oneBillRentEntries", true), "onebill.rent.{actorSuffix}")
		prefix, refusal := projection.SharedTargetDiffRefusal(newcomer, []projection.SiblingLens{
			sibling("plainSibling", false, "", ""),
		})
		require.NotEmpty(t, refusal)
		assert.Contains(t, refusal, "plainSibling")
		assert.Empty(t, prefix)
	})

	t.Run("a live unscoped diff refuses the lens arriving second", func(t *testing.T) {
		// The sibling's INSTALLED scoping is the fact, not what its descriptor
		// would admit: this sibling carries a pattern a prefix is derivable
		// from, and its running diff still lists the whole bucket.
		newcomer := withPattern(sharedBucket("newcomer", "newcomerLens", false), "onebill.rent.{actorSuffix}")
		prefix, refusal := projection.SharedTargetDiffRefusal(newcomer, []projection.SiblingLens{
			sibling("liveDiffLens", true, "", "onebill.cafe.{actorSuffix}"),
		})
		require.NotEmpty(t, refusal)
		assert.Contains(t, refusal, "liveDiffLens")
		assert.Contains(t, refusal, "unscoped")
		assert.Empty(t, prefix)
	})

	t.Run("a shared target with a single-column key and no descriptor is refused", func(t *testing.T) {
		diff := sharedBucket("diff-lens", "diffLens", true)
		require.Nil(t, diff.Output, "precondition: no output descriptor, so no key prefix is derivable")

		prefix, refusal := projection.SharedTargetDiffRefusal(diff, []projection.SiblingLens{
			sibling("siblingLens", false, "", "onebill.cafe.{actorSuffix}"),
		})
		require.NotEmpty(t, refusal)
		assert.Contains(t, refusal, "siblingLens", "the refusal names what it collides with")
		assert.Empty(t, prefix)
	})

	t.Run("a lens with no descriptor joining a scoped diff's bucket is refused", func(t *testing.T) {
		// The mirror of the case above: the newcomer carries no diff at all,
		// and nothing can establish that the live diff's listing excludes its
		// rows.
		newcomer := sharedBucket("newcomer", "newcomerLens", false)
		prefix, refusal := projection.SharedTargetDiffRefusal(newcomer, []projection.SiblingLens{
			sibling("liveDiffLens", true, "onebill.cafe.", "onebill.cafe.{actorSuffix}"),
		})
		require.NotEmpty(t, refusal)
		assert.Contains(t, refusal, "liveDiffLens")
		assert.Empty(t, prefix)
	})

	t.Run("a plain lens with a disjoint descriptor joins a scoped diff's bucket in either order", func(t *testing.T) {
		// The verdict is symmetric: the plain lens's own descriptor is the
		// key space the disjointness rule reads, whether it loads before the
		// diff (as the diff's sibling) or after it (as the newcomer). It is
		// admitted with no scoping of its own — nothing it runs lists the
		// bucket.
		plain := withPattern(sharedBucket("plain", "plainLens", false), "onebill.rent.{actorSuffix}")
		prefix, refusal := projection.SharedTargetDiffRefusal(plain, []projection.SiblingLens{
			sibling("liveDiffLens", true, "onebill.cafe.", "onebill.cafe.{actorSuffix}"),
		})
		require.Empty(t, refusal)
		assert.Empty(t, prefix)

		diff := withPattern(sharedBucket("cafe", "liveDiffLens", true), "onebill.cafe.{actorSuffix}")
		prefix, refusal = projection.SharedTargetDiffRefusal(diff, []projection.SiblingLens{
			sibling("plainLens", false, "", "onebill.rent.{actorSuffix}"),
		})
		require.Empty(t, refusal)
		assert.Equal(t, "onebill.cafe.", prefix)

		nested := withPattern(sharedBucket("nested", "nestedPlain", false), "onebill.cafe.rent.{actorSuffix}")
		_, refusal = projection.SharedTargetDiffRefusal(nested, []projection.SiblingLens{
			sibling("liveDiffLens", true, "onebill.cafe.", "onebill.cafe.{actorSuffix}"),
		})
		require.NotEmpty(t, refusal, "a plain lens whose rows sit under the live diff's prefix is refused")
	})

	t.Run("lenses that merely share a bucket need no prefixes at all", func(t *testing.T) {
		// Disjointness is owed only where a diff enumerates the bucket. Two
		// lenses that never list it can key their rows however they like.
		newcomer := sharedBucket("plain-a", "plainA", false)
		prefix, refusal := projection.SharedTargetDiffRefusal(newcomer, []projection.SiblingLens{
			sibling("plainB", false, "", ""),
		})
		assert.Empty(t, refusal)
		assert.Empty(t, prefix)
	})

	t.Run("a Postgres target is out of scope", func(t *testing.T) {
		r := sharedBucket("pg", "pgLens", true)
		r.Into.Target = "postgres"
		prefix, refusal := projection.SharedTargetDiffRefusal(r, []projection.SiblingLens{
			sibling("siblingLens", false, "", ""),
		})
		assert.Empty(t, refusal, "a protected table is the lens's own, and a grant table's listing is grant_source-scoped")
		assert.Empty(t, prefix)
	})
}

// TestRetractionTransport_VocabularyMatchesThePipeline holds the two spellings
// of the audit-disarmed transport together.
//
// internal/refractor/health does not import internal/refractor/pipeline — the
// health entry's shape is its own contract regardless of who produces it — so
// the value the heartbeat compares against is a literal of its own. This
// package imports both, and is therefore the one place the two can be held to
// each other: a drift would make the warning stop firing on the exact
// deployment it exists for, with every test on either side still green.
func TestRetractionTransport_VocabularyMatchesThePipeline(t *testing.T) {
	assert.Equal(t, pipeline.RetractionTransportDerivationAuditDisarmed, health.RetractionTransportAuditDisarmed,
		"the heartbeat's disarmed-transport literal must match the value the pipeline publishes, or the "+
			"LensRetractionTransportDisarmed warning silently never fires")

	// The two values that describe an obligation NOT met mirror no pipeline
	// constant — the pipeline answers a verdict, and turning "depends on a
	// neighbour, carries nothing" into a token is the wire's decision. So they
	// are held to the MAPPING rather than to a literal: what copyLensRetractionTransport
	// puts on a snapshot for each verdict shape is what the heartbeat keys its
	// alert off.
	for _, tc := range []struct {
		name    string
		verdict pipeline.PlainRetractionVerdict
		want    string
	}{
		{
			name:    "a neighbour-dependent lens with no transport publishes none",
			verdict: pipeline.PlainRetractionVerdict{Classified: true, Exhaustive: true, DependsOnNeighbour: true},
			want:    health.RetractionTransportNone,
		},
		{
			name:    "an unanswerable shape publishes unclassified",
			verdict: pipeline.PlainRetractionVerdict{Classified: true, DependsOnNeighbour: true, Transport: pipeline.RetractionTransportDerivation},
			want:    health.RetractionTransportUnclassified,
		},
		{
			name:    "a carrying transport passes through verbatim",
			verdict: pipeline.PlainRetractionVerdict{Classified: true, Exhaustive: true, DependsOnNeighbour: true, Transport: pipeline.RetractionTransportDiffRetractionPrefix},
			want:    pipeline.RetractionTransportDiffRetractionPrefix,
		},
		{
			name:    "a lens no neighbour can orphan publishes nothing",
			verdict: pipeline.PlainRetractionVerdict{Classified: true, Exhaustive: true, Transport: pipeline.RetractionTransportDerivation},
			want:    "",
		},
		{
			name:    "an unclassified pipeline publishes nothing",
			verdict: pipeline.PlainRetractionVerdict{},
			want:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var snap health.LensLivenessStatus
			copyLensRetractionTransport(&snap, tc.verdict)
			assert.Equal(t, tc.want, snap.RetractionTransport)
		})
	}
}

// activateThroughGate runs the stretch of startPipeline's activation the
// retraction guards sit in, in PRODUCTION'S OWN ORDER, and reports whether the
// lens reached the registry.
//
// The order is the point. activateLens (main_test.go) installs the lens plane
// before anything else because the question it serves is what activation
// RECORDS; startPipeline installs it long after these guards, which is why the
// transport verdict takes the plane as an argument rather than reading it off
// the pipeline. A fixture that installed it first would let a conjunct depending
// on an earlier stage read as satisfied for the very lens the gate must refuse.
//
// The registry insert is here for the same reason: a refused lens must leave no
// entry, and a helper that returned only the verdict could not say whether one
// was written.
func activateThroughGate(
	t *testing.T,
	mu *sync.Mutex,
	registry map[string]*pipelineEntry,
	reporter *health.Reporter,
	r *lens.Rule,
	spec string,
) (*pipeline.Pipeline, bool) {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	if threadsKeyColumns(r) {
		fullCR, isFull := cr.(*full.CompiledRule)
		require.True(t, isFull)
		require.NoError(t, projection.ThreadKeyColumns(fullCR, nil, r.Into.Key))
	}
	adpt := newKVAdapter(t)
	p, err := pipeline.New(r.ID, r.Into.Target, "CORE", nil, nil, adpt, nil)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngineBranches(eng, cr, nil))
	if r.Into.DiffRetraction {
		require.NoError(t, p.SetDiffRetraction(true))
	}
	if !admitRetractionTransport(context.Background(), discardLogger(), reporter, r, p,
		registeredSiblingsOnTarget(mu, registry, r.ID, r.Into.Target, r.Into.Bucket)) {
		return p, false
	}
	installLensPlane(p, r)
	mu.Lock()
	registry[r.ID] = newPipelineEntry(r, adpt, p, reporter, func() {}, make(chan struct{}), nil)
	mu.Unlock()
	return p, true
}

// TestActivation_RetractionGuardsDecideWhetherTheLensRegisters drives the
// guards through the activation sequence rather than by calling each predicate,
// so what it pins is the EFFECT of the gate: which lenses reach the registry,
// what a refused one leaves behind, and what a scoped diff carries once it is
// running.
//
// Calling the predicates directly proves only that they answer; it leaves the
// activation path free to ignore them.
func TestActivation_RetractionGuardsDecideWhetherTheLensRegisters(t *testing.T) {
	newRegistry := func() (*sync.Mutex, map[string]*pipelineEntry) {
		return &sync.Mutex{}, map[string]*pipelineEntry{}
	}

	t.Run("a first-loading diff lens is scoped to its own key prefix", func(t *testing.T) {
		// The positive vector, and the one that fails if the scoping is derived
		// only for a bucket that already holds a sibling: this lens is alone on
		// its bucket, and nothing will re-scope its pipeline once one arrives.
		kv := startHealthKV(t)
		mu, registry := newRegistry()
		r := businessRule("gate-first-diff", "gateFirstDiff")
		r.Into.Bucket = "one-bill-history"
		r.Into.DiffRetraction = true
		r.Output = &lens.OutputDescriptorSpec{
			AnchorType:       "leaseapp",
			OutputKeyPattern: "onebill.rent.{actorSuffix}",
			BodyColumns:      []string{"amountCents"},
			EmptyBehavior:    "delete",
		}

		p, admitted := activateThroughGate(t, mu, registry, health.New(kv, r.ID), r, anchorClosedNeighbourSpec)
		require.True(t, admitted, "a lens whose diff can be scoped must activate")
		assert.Equal(t, "onebill.rent.", p.DiffRetractionPrefix(),
			"the running pipeline must carry the scoping, or its first event lists the whole shared bucket")
		assert.Contains(t, registry, r.ID)
	})

	t.Run("an untransported neighbour-dependent lens leaves no registry entry", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "gate-untransported")
		mu, registry := newRegistry()
		r := businessRule("gate-untransported", "gateUntransported", "app_id", "landlord_id")

		_, admitted := activateThroughGate(t, mu, registry, reporter, r, untransportedNeighbourSpec)
		require.False(t, admitted, "a lens a neighbour can orphan, with no transport, must not activate")
		assert.NotContains(t, registry, r.ID,
			"a refused lens must leave no entry: the heartbeat reads the registry, and an entry there is a lens presenting as live")

		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "neighbour-retraction transport REFUSED activation",
			"a lens that never activates has no heartbeat status, so the entry is the only account of why it is dark")
	})

	t.Run("a lens joining a bucket a live unscoped diff reads is refused", func(t *testing.T) {
		// Both guards through one sequence: the first lens registers, and its
		// running posture is what refuses the second.
		kv := startHealthKV(t)
		mu, registry := newRegistry()

		first := businessRule("gate-live-diff", "gateLiveDiff")
		first.Into.Bucket = "weaver-targets-test"
		first.Into.DiffRetraction = true
		_, admitted := activateThroughGate(t, mu, registry, health.New(kv, first.ID), first, anchorClosedNeighbourSpec)
		require.True(t, admitted, "a diff lens alone on its bucket activates unscoped, which is what makes it the hazard")

		second := businessRule("gate-newcomer", "gateNewcomer")
		second.Into.Bucket = "weaver-targets-test"
		reporter := health.New(kv, second.ID)
		_, admitted = activateThroughGate(t, mu, registry, reporter, second, anchorOnlySpec)
		require.False(t, admitted)
		assert.NotContains(t, registry, second.ID)

		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "shared-target diff retraction REFUSED activation")
		assert.Contains(t, *status.LastError, "gateLiveDiff")
	})
}

// partitionShapeSpec is a partition-only lens: its `(app_id, landlord_id)` key
// carries a column bound to the neighbour a `manages` walk reached, so the rows
// partition by the leaseapp anchor without being keyed on it alone.
const partitionShapeSpec = `
MATCH (app:leaseapp)
MATCH (app)-[:appliesToUnit]->(u:unit)
MATCH (u)<-[:manages]-(landlord:identity)
RETURN nanoIdFromKey(app.key) AS app_id, nanoIdFromKey(landlord.key) AS landlord_id
`

// TestActivation_PartitionOnlyLensWithoutDiffRetractionIsRefused pins the
// conjunct the transport verdict grew when the narrowing licence's shape test
// widened from closure to PARTITIONING.
//
// The licence and the retraction answer two different questions, and only the
// widened one moved. A rule whose rows partition by anchor may be narrowed —
// that is what makes a per-anchor evaluation exact — but T1 delivers its
// retraction through the read-free presence check, which needs the row's KEY
// derivable from the anchor alone. On a lens that partitions WITHOUT closing and
// declares no target diff, the derived path re-evaluates every affected anchor
// and emits no Delete for the row a neighbour just dropped: the lens would have
// activated announcing a transport that cannot carry a row off the target.
//
// A lens of this exact shape that DOES declare DiffRetraction is the five this
// design arms, and it activates — which is why the vector below is the
// declaration-less twin rather than the shape.
func TestActivation_PartitionOnlyLensWithoutDiffRetractionIsRefused(t *testing.T) {
	r := businessRule("gate-partition-nodiff", "gatePartitionNoDiff", "app_id", "landlord_id")
	require.False(t, projection.IsAuthPlane(r), "precondition: the gate is scoped to the business plane")

	p := activateLens(t, r, partitionShapeSpec, newKVAdapter(t))
	v := p.PlainRetractionTransport(projection.IsAuthPlane(r))
	require.True(t, v.DependsOnNeighbour,
		"precondition: a `manages` hop this lens requires means a neighbour event really can drop its rows")

	refusal := retractionTransportRefusal(p, r)
	require.NotEmpty(t, refusal,
		"a lens that narrows correctly and retracts nothing must not activate — its rows would be orphaned silently")
	assert.Contains(t, refusal, "partition by anchor",
		"and the reason names the shape, so an author is told which conjunct they are on the wrong side of")
	assert.Contains(t, refusal, "declare target-diff retraction",
		"with the move that fixes it: the partition-scoped diff is what carries the Delete for this shape")
}

// TestActivation_AuthPlaneLensIsNeverOfferedThePartitionTransport pins §3.7's
// THIRD exclusion — the gate itself.
//
// The grant tables are held off the partition transport by three independent
// things: the plane conjunct inside SetPartitionRetraction, the shared grant
// writer implementing no adapter.PartitionKeyLister, and this — the gate never
// offering it. Each is meant to hold on its own, and a test that only exercised
// the setter would let the gate quietly become the single point of failure.
func TestActivation_AuthPlaneLensIsNeverOfferedThePartitionTransport(t *testing.T) {
	r := businessRule("gate-authplane-partition", "gateAuthPlanePartition", "app_id", "landlord_id")
	r.Into.Bucket = projection.AuthPlaneBucket
	require.True(t, projection.IsAuthPlane(r), "precondition: this rule really is on the auth plane")

	p := activateLens(t, r, partitionShapeSpec, newKVAdapter(t))
	require.NoError(t, p.SetDiffRetraction(true))

	require.True(t, admitRetractionTransport(context.Background(), discardLogger(), nil, r, p, nil),
		"the lens still activates — the auth plane is a hold-out from this transport, not a refusal")
	assert.False(t, p.PartitionRetraction(),
		"but the gate must never arm it: the whole diff on every event is the only shrink path an un-truncatable grant table "+
			"has on a rebuild, and a partition-scoped diff would remove it")
}
