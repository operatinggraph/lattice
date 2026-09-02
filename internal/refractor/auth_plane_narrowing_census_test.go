// Auth-plane narrowing census — auth-plane-projection-latency-design.md §4.2.
//
// The unit suite in internal/refractor/pipeline hand-assigns a label set to
// prove the predicate. This file closes the other half: it drives the REAL
// shipped cypher of every auth-plane actor-aggregate lens through the same
// UseFullEngineBranches derivation production uses, and pins the resulting
// (eligible, labels) verdict.
//
// It exists because Increment 1's gate arms far wider than the design's own
// corpus paragraph (§4.6) discusses. A cypher edit that quietly changes a
// lens's verdict — narrowing one that must stay broad, or dropping a type out
// of a set the fan-out arms judge against — is an authorization change, and it
// should fail here rather than in Capability KV.
//
// The tables below are hand-written, so what they cover is a claim; the
// population they must cover is DERIVED, from the live registry through the same
// predicate production uses (projection.IsAuthPlane over an actorAggregate lens),
// and TestAuthPlaneCensus_CoversEveryAuthPlaneActorAggregate asserts the two
// agree in both directions. Without that closure an auth-plane lens could ship
// with no pinned verdict at all and every test in this file would still pass —
// which is exactly how three of the six went uncovered.
package refractor_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// narrowingVerdict derives the gate's verdict for a cypher + anchor type the way
// activation does: UseFullEngine for the label set, then the two install-time
// declarations InstallActorAggregate makes (pattern-closure and the sweep plan).
// Anything it reports ineligible is ineligible for a reason the label set alone
// cannot express.
func narrowingVerdict(t *testing.T, lensID, cypher, anchorType string) (map[string]struct{}, bool) {
	t.Helper()
	return narrowingPipeline(t, lensID, cypher, anchorType).ActorAwareNarrowingLabels()
}

// narrowingPipeline is narrowingVerdict's pipeline, for the assertions that need
// the DELIVERY side of the gate rather than its label verdict.
func narrowingPipeline(t *testing.T, lensID, cypher, anchorType string) *pipeline.Pipeline {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(cypher)
	require.NoError(t, err, "%s must parse", lensID)

	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(lensID, "nats_kv", bootstrap.CoreKVBucket, nil, nil, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	p.SetActorEnumerator(pipeline.NewActorEnumerator(nil, nil, anchorType))
	p.SetPatternClosedOutput(true)
	p.SetSweepPlan(pipeline.SweepPlan{AnchorType: anchorType, KeyPrefix: lensID + "."})
	return p
}

// TestAuthPlaneLenses_NarrowingVerdict pins the verdict for every auth-plane
// actor-aggregate lens the gate reaches. Each expected label set was checked by
// hand against the lens's cypher: every pattern node carries a label, every
// traversed edge has both endpoint types in the set, every property read is a
// root field or an aspect whose parent type is in the set, and the anchor type
// is in the set — which is what makes the anchor's soft-delete undroppable.
func TestAuthPlaneLenses_NarrowingVerdict(t *testing.T) {
	for _, tc := range narrowingCases(t) {
		t.Run(tc.lensID, func(t *testing.T) {
			labels, eligible := narrowingVerdict(t, tc.lensID, tc.cypher, tc.anchorType)
			require.True(t, eligible,
				"this lens's shipped cypher must still derive an exhaustive label set containing its anchor")
			require.Equal(t, tc.wantLabels, labels,
				"the fan-out arms judge every event against this set — a change here is an authorization change")
		})
	}
}

// narrowingCase is one lens's pinned (eligible, labels) verdict. lensName is the
// canonical name it carries in the registry, or "" for a lens the package
// registry does not hold; the coverage closure below reads it.
type narrowingCase struct {
	lensID     string
	lensName   string
	cypher     string
	anchorType string
	wantLabels map[string]struct{}
}

// narrowingCases is the ELIGIBLE half of this file's coverage: every auth-plane
// actor-aggregate lens whose pattern derives an exhaustive label set, plus the
// kernel capability lens.
func narrowingCases(t *testing.T) []narrowingCase {
	t.Helper()
	return []narrowingCase{
		{
			// rbac-domain's role-derived grant projection — the lens the design
			// is about.
			lensID:     "CensusRbacRoLes99999",
			lensName:   "capabilityRoles",
			cypher:     rbacCapabilityRolesSpec(t),
			anchorType: "identity",
			wantLabels: map[string]struct{}{"identity": {}, "role": {}, "permission": {}},
		},
		{
			// The kernel cap.<actor> doc. Its grant set is a RETURN literal, not
			// a grantedBy/permission walk, so `permission` is legitimately absent
			// — core references no rbac permission vocabulary (Contract #7 §7.7).
			//
			// lensName is empty on purpose: this lens is declared by the kernel
			// seeder, not by a package, and its declaration carries the bucket
			// name `capability` rather than the auth-plane bucket, so the derived
			// population does not contain it. It is covered here anyway, because
			// what it projects IS the write-authorization surface.
			lensID:     "CensusKerneLCap99999",
			lensName:   "",
			cypher:     bootstrap.CapabilityLensDefinition().CypherRule,
			anchorType: "identity",
			wantLabels: map[string]struct{}{"identity": {}, "role": {}},
		},
		{
			// edge-manifest's PROVIDER read-grant producer, generated from the
			// package's ReadGrantDomain walks. Every node in its three walks
			// carries a label — (identity)<-[:identifiedBy]-(instructor)<-[:ledBy]-(session),
			// (identity)<-[:identifiedBy]-(provider)<-[:withProvider]-(appointment)
			// and (identity)<-[:identifiedBy]-(serviceprovider)<-[:providedBy]-(service)<-[:instanceOf]-(service)
			// — so the derivation is exhaustive and the set is the seven types
			// those patterns name. Its two siblings reach containment through
			// UNLABELED nodes and stay broad; they are pinned in
			// TestNonExhaustiveAuthPlaneLenses_StayBroad.
			lensID:     "CensusEdgeProVider99",
			lensName:   "edgeManifestProviderReadGrants",
			cypher:     authPlaneActorAggregates(t)["edgeManifestProviderReadGrants"].cypher,
			anchorType: "identity",
			wantLabels: map[string]struct{}{
				"identity": {}, "instructor": {}, "session": {}, "provider": {},
				"appointment": {}, "serviceprovider": {}, "service": {},
			},
		},
	}
}

// TestAuthPlaneLenses_ConsumerFilterVerdict is the delivery half of the census:
// the label verdict above decides whether the fan-out ARMS may skip an event,
// and Increment 2 makes the same verdict decide whether the SERVER delivers it
// at all. The two must stay one decision, so the filter set is pinned against
// the real shipped cypher and not only against a hand-built fixture.
//
// capabilityRoles types every traversed edge, so its relation set is exhaustive
// and its link subjects pin the relation segment in both directions.
func TestAuthPlaneLenses_ConsumerFilterVerdict(t *testing.T) {
	p := narrowingPipeline(t, "CensusRbacFiLter9999", rbacCapabilityRolesSpec(t), "identity")

	filterSubjects, filterSubject, decision := p.ConsumerFilter()
	require.Empty(t, filterSubject,
		"the shipped capabilityRoles must narrow, not fall back to the broad filter")
	require.Equal(t, health.FilterModeNarrowedRelation, decision.Mode)
	require.Equal(t, 3, decision.LabelCount)

	bucket := bootstrap.CoreKVBucket
	require.ElementsMatch(t, []string{
		"$KV." + bucket + ".vtx.identity.>",
		"$KV." + bucket + ".lnk.identity.*.holdsRole.>",
		"$KV." + bucket + ".lnk.*.*.holdsRole.identity.>",
		"$KV." + bucket + ".lnk.identity.*.grantedBy.>",
		"$KV." + bucket + ".lnk.*.*.grantedBy.identity.>",
		"$KV." + bucket + ".vtx.role.>",
		"$KV." + bucket + ".lnk.role.*.holdsRole.>",
		"$KV." + bucket + ".lnk.*.*.holdsRole.role.>",
		"$KV." + bucket + ".lnk.role.*.grantedBy.>",
		"$KV." + bucket + ".lnk.*.*.grantedBy.role.>",
		"$KV." + bucket + ".vtx.permission.>",
		"$KV." + bucket + ".lnk.permission.*.holdsRole.>",
		"$KV." + bucket + ".lnk.*.*.holdsRole.permission.>",
		"$KV." + bucket + ".lnk.permission.*.grantedBy.>",
		"$KV." + bucket + ".lnk.*.*.grantedBy.permission.>",
	}, filterSubjects,
		"three labels expand to the vertex form plus each traversed relation in both link directions, and nothing else")
}

// TestAuthPlaneLenses_LinkAdmissionIsSetEqualToTheLinkArm is the invariant the
// narrowing rests on, over the real shipped capabilityRoles cypher: for every
// link CDC key, the SERVER-side narrowed filter admits it if and only if the
// pipeline's own link arm would do work for it.
//
// Only one of the two directions is unrecoverable, and it is the reason this is
// asserted rather than reasoned about. A link the arm keeps but the filter
// withholds is an event the lens never sees — on a grant lens a retraction that
// never lands — and no revert recovers it, because a JetStream filter update
// never rewinds the consumer's cursor (nats-server v2.14.0). The opposite
// direction merely costs a queue slot. Equality is asserted anyway, because it
// is what makes the pair maintainable: an edit that widens the client gate
// without widening the subjects is exactly the unrecoverable direction, and
// only an equality catches it from the safe side.
//
// The candidate space is the cross product that discriminates the two
// dimensions independently: an endpoint type in the label set against one
// outside it, in EACH position, over a traversed relation against an untraversed
// one. `want` is hand-derived from the cypher — the pattern is
// (identity)-[:holdsRole]->(role)<-[:grantedBy]-(permission), so a link matters
// only when its relation is one of those two AND at least one endpoint is one of
// the three labels — so neither side of the equality is pinned against the
// other's implementation, and the subject-matching oracle below is exercised in
// both directions rather than trusted.
func TestAuthPlaneLenses_LinkAdmissionIsSetEqualToTheLinkArm(t *testing.T) {
	p := narrowingPipeline(t, "CensusRbacSetEq99999", rbacCapabilityRolesSpec(t), "identity")
	filterSubjects, filterSubject, _ := p.ConsumerFilter()
	require.Empty(t, filterSubject, "this proof is only about a NARROWED consumer")

	const inA, inB, out = "identity", "role", "booking"
	const traversed, untraversed = "holdsRole", "bookedBy"

	cases := []struct {
		typeA, relation, typeB string
		want                   bool
		why                    string
	}{
		{inA, traversed, inB, true, "both endpoints in the label set, over a relation the pattern walks"},
		{inA, "grantedBy", inB, true, "the pattern's other traversed relation"},
		{out, traversed, inB, true, "the TARGET endpoint binds — the target-pinned link form is why both positions are emitted"},
		{inA, traversed, out, true, "the SOURCE endpoint binds"},
		{inA, untraversed, inB, false, "no traversal can consume a relation the pattern never walks, whatever binds"},
		{inA, untraversed, out, false, "a bindable SOURCE endpoint does not rescue an untraversed relation"},
		{out, untraversed, inB, false, "the same, in the target position"},
		{out, traversed, out, false, "a traversed relation between two types the pattern cannot bind"},
		{out, untraversed, out, false, "neither dimension matches"},
	}

	var admitted, withheld int
	for _, tc := range cases {
		name := tc.typeA + "-" + tc.relation + "-" + tc.typeB
		t.Run(name, func(t *testing.T) {
			key := "lnk." + tc.typeA + ".AAAAAAAAAAAAAAAAAAAA." + tc.relation + "." + tc.typeB + ".BBBBBBBBBBBBBBBBBBBB"
			subject := "$KV." + bootstrap.CoreKVBucket + "." + key

			serverAdmits := false
			for _, f := range filterSubjects {
				if natsSubjectMatches(f, subject) {
					serverAdmits = true
					break
				}
			}
			clientKeeps := p.LinkEventRelevant(tc.relation, tc.typeA, tc.typeB)

			require.Equalf(t, tc.want, serverAdmits,
				"the narrowed filter must %s this link: %s", admitWord(tc.want), tc.why)
			require.Equalf(t, tc.want, clientKeeps,
				"the link arm must %s this link: %s", keepWord(tc.want), tc.why)
			require.Equalf(t, clientKeeps, serverAdmits,
				"server admission and the client link arm must be SET-EQUAL — a link the arm keeps and the filter withholds is an event no revert recovers")
		})
		if tc.want {
			admitted++
		} else {
			withheld++
		}
	}
	require.Positive(t, admitted, "a set-equality over an empty admitted set proves nothing")
	require.Positive(t, withheld, "a set-equality over an empty withheld set proves nothing")
}

func admitWord(want bool) string {
	if want {
		return "admit"
	}
	return "withhold"
}

func keepWord(want bool) string {
	if want {
		return "keep"
	}
	return "skip"
}

// natsSubjectMatches reports whether a concrete subject matches a filter subject
// under NATS wildcard semantics: `*` matches exactly one token, `>` matches one
// or more trailing tokens, and everything else is literal (nats.io subject
// wildcards; our pin is NATS 2.14).
//
// It is a test ORACLE, deliberately independent of the filter builder, so the
// equality above is a claim about what a server would deliver rather than a
// restatement of what subjects.CoreKVRelationNarrowedFilters emitted. What keeps
// the oracle honest is that the table drives it to both answers against
// hand-derived expectations, and that the live-server counterpart of the same
// discrimination runs in
// TestRefractor_CapabilityLens_NarrowedFilter_UnrelatedWriteNeverDelivered_E2E.
func natsSubjectMatches(filter, subject string) bool {
	f := strings.Split(filter, ".")
	s := strings.Split(subject, ".")
	for i, tok := range f {
		if tok == ">" {
			return len(s) > i
		}
		if i >= len(s) {
			return false
		}
		if tok != "*" && tok != s[i] {
			return false
		}
	}
	return len(f) == len(s)
}

// stayBroadCases is the INELIGIBLE half of this file's coverage: lenses whose
// patterns carry an unlabeled node and must never narrow.
//
// authPlane records whether the lens is one the derived population contains, so
// the coverage closure can tell an auth-plane row from a companion. myTasks and
// orphanedTaskGrants are companions: they project ordinary business buckets, not
// the authorization surface, and they are exercised here because they carry the
// same unlabeled-position shape and would regress the same way.
var stayBroadCases = []struct {
	name      string
	authPlane bool
	why       string
}{
	{"capabilityEphemeral", true, "leaves its operation/target positions unlabeled"},
	{"capabilityServiceAccess", true, "reaches instanceOf/unavailableAt only through a negated WHERE"},
	{"edgeManifestReadGrants", true, "walks residence containment through unlabeled (home)/(container) nodes"},
	{"edgeManifestStaffReadGrants", true, "walks workplace containment through unlabeled (work)/(place) nodes"},
	{"myTasks", false, "not auth-plane — a companion carrying the same unlabeled-position shape"},
	{"orphanedTaskGrants", false, "not auth-plane — a companion carrying the same unlabeled-position shape"},
}

// TestNonExhaustiveAuthPlaneLenses_StayBroad is the negative half: a lens whose
// pattern carries an unlabeled node must never narrow, whatever else its
// installation declares — any type may bind there, so the fan-out arms cannot
// skip an event on the strength of a label set.
func TestNonExhaustiveAuthPlaneLenses_StayBroad(t *testing.T) {
	for _, tc := range stayBroadCases {
		t.Run(tc.name, func(t *testing.T) {
			cypher, anchor, found := authPlaneOrCorpusSpec(t, tc.name)
			if !found {
				t.Skipf("%s is not declared in the registry snapshot this test reads", tc.name)
			}
			_, eligible := narrowingVerdict(t, "CensusBroad999999999", cypher, anchor)
			require.Falsef(t, eligible,
				"a non-exhaustive pattern must keep the unconditional fan-out (%s)", tc.why)
		})
	}
}

// authPlaneLens is one enumerated lens's shipped cypher and anchor type.
type authPlaneLens struct {
	cypher     string
	anchorType string
}

// authPlaneActorAggregates DERIVES the population this file must cover, from the
// live registry through the same two conditions activation applies: the lens is
// an actorAggregate, and projection.IsAuthPlane classifies its target as the
// authorization surface. It enumerates through forEachCorpusCypher so a
// read-grant producer generated at install time — which no package declares
// literally, and which the earlier hand-written tables therefore missed — is in
// the population the day its walk is declared.
func authPlaneActorAggregates(t *testing.T) map[string]authPlaneLens {
	t.Helper()
	out := map[string]authPlaneLens{}
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, aggregate, _ bool) {
		if !aggregate || !projection.IsAuthPlane(rule) {
			return
		}
		anchor := ""
		if rule.Output != nil {
			anchor = rule.Output.AnchorType
		}
		out[name] = authPlaneLens{cypher: spec, anchorType: anchor}
	})
	return out
}

// authPlaneOrCorpusSpec resolves a lens's shipped cypher by canonical name,
// preferring the auth-plane enumeration (which sees install-time-generated
// producers) and falling back to the package declarations for the companions.
func authPlaneOrCorpusSpec(t *testing.T, name string) (cypher, anchorType string, found bool) {
	t.Helper()
	if l, ok := authPlaneActorAggregates(t)[name]; ok {
		return l.cypher, l.anchorType, true
	}
	return actorAggregateSpecByName(t, name)
}

// TestAuthPlaneCensus_CoversEveryAuthPlaneActorAggregate is this file's coverage
// closure, and the reason its header can claim to cover "every auth-plane
// actor-aggregate lens" at all. The two tables above are hand-written; this
// derives the population they must cover from the registry through the real
// predicate and asserts set equality in BOTH directions.
//
// The uncovered direction is the dangerous one: a lens with no pinned verdict is
// a lens whose narrowing can change without any test noticing, and a narrowing
// that withholds an event is unrecoverable — no JetStream filter update rewinds a
// consumer's cursor. The other direction matters too, more quietly: a row naming
// a lens the predicate no longer classifies as auth-plane is a pin that has
// stopped asserting what its name says.
//
// The floor is what keeps the equality honest. An enumeration that returned
// nothing would satisfy set equality against an empty covered set on the day
// someone deleted both tables, and would satisfy it silently.
func TestAuthPlaneCensus_CoversEveryAuthPlaneActorAggregate(t *testing.T) {
	population := authPlaneActorAggregates(t)
	require.GreaterOrEqualf(t, len(population), 6,
		"the auth-plane enumeration collapsed to %d lenses — this census is only worth what it covers", len(population))

	covered := map[string]string{}
	for _, tc := range narrowingCases(t) {
		if tc.lensName != "" {
			covered[tc.lensName] = "pinned narrowing verdict"
		}
	}
	for _, tc := range stayBroadCases {
		if tc.authPlane {
			covered[tc.name] = "pinned as staying broad"
		}
	}

	for name := range population {
		_, ok := covered[name]
		require.Truef(t, ok,
			"auth-plane actor-aggregate lens %q has no pinned narrowing verdict in this file — "+
				"derive its verdict by hand from its cypher, then add it to narrowingCases (if it narrows) "+
				"or stayBroadCases (if a pattern node is unlabeled). An unpinned lens is one whose fan-out "+
				"can silently start skipping events.", name)
	}
	for name, how := range covered {
		_, ok := population[name]
		require.Truef(t, ok,
			"%q is %s here, but the registry no longer classifies it as an auth-plane actorAggregate — "+
				"the row has stopped asserting what its name says; move or remove it deliberately", name, how)
	}
}

// TestConsumerFilter_RefusesToNarrowBeforeInstallCompletes drives cmd/refractor's
// real activation ORDER against the real capabilityRoles cypher, and stops it one
// stage short. Every other test here builds a fully-installed pipeline, which is
// precisely the state in which the ordering hazard is invisible.
//
// The hazard is inverted, which is why it needs its own test rather than an
// assertion bolted onto an existing one: with the enumerator not yet installed,
// the pipeline is shaped exactly like a plain lens, so the derivation would take
// the PLAIN branch — whose conditions UseFullEngineBranches has already met — and
// hand back the MOST aggressive filter with none of §4.2's actor-aware conjuncts
// evaluated. Nothing widens a registered filter back, so the mistake would be
// permanent.
//
// The second half is what stops the guard from degenerating into "always broad":
// the SAME pipeline, one install stage later, must narrow to the same three
// labels the census above pins.
func TestConsumerFilter_RefusesToNarrowBeforeInstallCompletes(t *testing.T) {
	const anchorType = "identity"
	eng := full.New()
	cr, err := eng.Parse(rbacCapabilityRolesSpec(t))
	require.NoError(t, err)
	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New("CensusOrderIng9999999", "nats_kv", bootstrap.CoreKVBucket, nil, nil, adpt, nil)
	require.NoError(t, err)

	// Stage 1 of the install order, and only stage 1. The enumerator, the
	// pattern-closure declaration and the sweep plan all arrive together with
	// projection.InstallActorAggregate, which has not run yet.
	require.NoError(t, p.UseFullEngine(eng, cr))

	filterSubjects, filterSubject, decision := p.ConsumerFilter()
	require.Empty(t, filterSubjects,
		"an unfinished install must not yield a narrowed subject set — no filter update rewinds a JetStream cursor")
	require.Equal(t, "$KV."+bootstrap.CoreKVBucket+".>", filterSubject,
		"the refusal is the BROAD filter, not a refusal to activate: a caller-ordering bug must not take a healthy lens down")
	require.Equal(t, health.FilterModeBroad, decision.Mode)
	require.Equal(t, health.FilterBroadReasonInstallIncomplete, decision.BroadReason,
		"the health entry must name the incompleteness, not report it as an ordinary not-eligible lens")

	labels, narrowed := p.ConsumerFilterLabels()
	require.False(t, narrowed,
		"the label view must agree with the filter about whether this lens is narrowed")
	require.Nil(t, labels)

	// Finish the install. The identical call must now narrow, or the guard is
	// merely a permanent broad filter wearing a reason.
	p.SetActorEnumerator(pipeline.NewActorEnumerator(nil, nil, anchorType))
	p.SetPatternClosedOutput(true)
	p.SetSweepPlan(pipeline.SweepPlan{AnchorType: anchorType, KeyPrefix: "CensusOrderIng9999999."})

	filterSubjects, filterSubject, decision = p.ConsumerFilter()
	require.Empty(t, filterSubject)
	require.Equal(t, health.FilterModeNarrowedRelation, decision.Mode)
	require.Equal(t, 3, decision.LabelCount)
	require.ElementsMatch(t, []string{
		"$KV." + bootstrap.CoreKVBucket + ".vtx.identity.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.identity.*.holdsRole.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.*.*.holdsRole.identity.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.identity.*.grantedBy.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.*.*.grantedBy.identity.>",
		"$KV." + bootstrap.CoreKVBucket + ".vtx.role.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.role.*.holdsRole.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.*.*.holdsRole.role.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.role.*.grantedBy.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.*.*.grantedBy.role.>",
		"$KV." + bootstrap.CoreKVBucket + ".vtx.permission.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.permission.*.holdsRole.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.*.*.holdsRole.permission.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.permission.*.grantedBy.>",
		"$KV." + bootstrap.CoreKVBucket + ".lnk.*.*.grantedBy.permission.>",
	}, filterSubjects)

	labels, narrowed = p.ConsumerFilterLabels()
	require.True(t, narrowed)
	require.Equal(t, map[string]struct{}{"identity": {}, "role": {}, "permission": {}}, labels)
}

// TestConsumerFilter_PlainLensNarrowsWithNoEnumerator is the guard's regression
// axis, stated on the real corpus rather than on a fixture: a lens the host
// installs as plain declares no `$actorKey` anchor, so the install-completeness
// guard does not apply to it and it narrows on its own terms. A failure here
// means the guard has started reading plain lenses as half-installed ones, which
// puts every plain narrowing in the corpus on the broad filter.
func TestConsumerFilter_PlainLensNarrowsWithNoEnumerator(t *testing.T) {
	cypher, found := plainNarrowingSpec(t)
	if !found {
		t.Skip("no plain full-engine lens in the registry snapshot derives an exhaustive label set")
	}
	eng := full.New()
	cr, err := eng.Parse(cypher)
	require.NoError(t, err)
	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New("CensusPlaiNarrow99999", "nats_kv", bootstrap.CoreKVBucket, nil, nil, adpt, nil)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, cr))

	filterSubjects, filterSubject, decision := p.ConsumerFilter()
	require.Empty(t, filterSubject,
		"a genuinely plain lens has no enumerator by design — the install-completeness guard must not read that as unfinished")
	require.NotEmpty(t, filterSubjects)
	require.NotEqual(t, health.FilterModeBroad, decision.Mode)
	require.Empty(t, decision.BroadReason)
}

// plainNarrowingSpec returns a shipped cypher that is full-engine, declares no
// actor anchor, and derives an exhaustive label set — the shape the regression
// test needs. Found by walking the registry rather than named, so the test keeps
// working when the corpus moves.
func plainNarrowingSpec(t *testing.T) (cypher string, found bool) {
	t.Helper()
	eng := full.New()
	for _, def := range pkgregistry.All() {
		for _, l := range def.Lenses {
			if l.Spec == "" || l.ProjectionKind != "" || l.Personal {
				continue
			}
			cr, err := eng.Parse(l.Spec)
			if err != nil {
				continue
			}
			fullCR, isFull := cr.(*full.CompiledRule)
			if !isFull || fullCR.DeclaresActorAnchor() {
				continue
			}
			if ls, ok := fullCR.ReferencedLabels(); !ok || len(ls) == 0 || len(ls) > 6 {
				continue
			}
			if len(fullCR.ExpansionLabels()) > 0 {
				continue
			}
			return l.Spec, true
		}
	}
	return "", false
}

// rbacCapabilityRolesSpec returns rbac-domain's shipped capabilityRoles cypher.
func rbacCapabilityRolesSpec(t *testing.T) string {
	t.Helper()
	for _, l := range rbacdomain.Lenses() {
		if l.CanonicalName == "capabilityRoles" {
			return l.Spec
		}
	}
	require.FailNow(t, "rbac-domain must declare a capabilityRoles lens")
	return ""
}

// actorAggregateSpecByName finds a shipped actor-aggregate lens's cypher and
// anchor type by canonical name, across every registered package.
func actorAggregateSpecByName(t *testing.T, name string) (cypher, anchorType string, found bool) {
	t.Helper()
	for _, def := range pkgregistry.All() {
		for _, l := range def.Lenses {
			if l.CanonicalName != name || l.Output == nil || l.Spec == "" {
				continue
			}
			return l.Spec, l.Output.AnchorType, true
		}
	}
	return "", "", false
}
