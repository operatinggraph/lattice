package pipeline

// The neighbour-retraction transport verdict and the static predicate the
// activation gate and the licence share
// (secure-plain-lens-retraction-and-audit-design.md §4.4, §5).
//
// The corpus-wide half of these questions is pinned in internal/refractor
// (plain_retraction_transport_corpus_census_test.go); what belongs here is the
// two facts that need live pipeline state a census cannot build: that the
// licence's own static tail IS the shared predicate, asked of a fixture whose
// audit is enrolled, unsuppressed and fresh; and that the deployment's audit
// kill switch turns a T1 lens's declared transport into a published
// "disarmed" reading rather than into silence.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlainDerivationStaticallyEligible_IsTheLicencesOwnTail proves the two
// consumers are one derivation: with every DYNAMIC conjunct satisfied — off the
// auth plane, an enrolled and unsuppressed auditor that has reached a verdict —
// the licence's verdict and the shared static predicate's are the same verdict
// and the same reason, admission and refusal alike.
//
// The refusal vectors are the two closure negatives, because closure is the
// conjunct that has no counterpart in the audit's enrolment: if the licence
// ever stopped delegating, an admission there would be the gate admitting a
// lens the licence will never license, which is exactly the defect the shared
// function exists to make impossible.
func TestPlainDerivationStaticallyEligible_IsTheLicencesOwnTail(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec string
		want bool
	}{
		{name: "an anchor-closed lens is admitted by both", spec: seedUnitsSpec, want: true},
		{name: "a neighbour-keyed lens is refused by both", spec: licenceNeighbourKeyedSpec, want: false},
		{name: "a collapsed-key lens is refused by both", spec: licenceCollapsedKeySpec, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := licenceFixture(t, tc.spec)
			rs := f.p.ruleState()

			licensed, licenceRefusal := f.p.plainDerivationLicence(rs)
			eligible, staticRefusal := f.p.plainDerivationStaticallyEligible(rs)

			require.Equal(t, tc.want, licensed, "licence refusal: %s", licenceRefusal)
			require.Equal(t, licensed, eligible,
				"the licence's verdict and the shared static predicate's disagree with every dynamic conjunct satisfied — "+
					"licence %v (%q), static %v (%q). The activation gate reads the static one, so a disagreement is a lens "+
					"the gate admits and the licence will never license", licensed, licenceRefusal, eligible, staticRefusal)
			require.Equal(t, licenceRefusal, staticRefusal,
				"the two consumers must publish the same reason for the same lens")

			// The exported accessor the gate and the census call must answer
			// off the pipeline's own snapshot, not off a rule state a caller
			// happens to hand it.
			exportedEligible, exportedRefusal := f.p.PlainDerivationStaticallyEligible()
			assert.Equal(t, eligible, exportedEligible)
			assert.Equal(t, staticRefusal, exportedRefusal)
		})
	}
}

// TestPlainRetractionTransport_AuditDisarmedIsPublishedNotSilent pins the
// deployment kill switch's effect on a lens whose only transport is the
// derivation.
//
// SetAuditEnabled(false) makes auditEnrolment refuse every lens, so the
// licence's first conjunct — an ENROLLED auditor — can never hold: the shape
// still supports the transport and nothing is carrying it. Publishing
// "derivation" there would claim a transport that is off; publishing nothing
// would read as a lens that needs none. The value says which.
func TestPlainRetractionTransport_AuditDisarmedIsPublishedNotSilent(t *testing.T) {
	armed := licenceFixture(t, seedUnitsSpec)
	armedVerdict := armed.p.PlainRetractionTransport(false)
	require.True(t, armedVerdict.Classified)
	require.Equal(t, RetractionTransportDerivation, armedVerdict.Transport,
		"the positive vector must hold, or the disarmed reading below is just a lens with no transport")
	require.True(t, armed.p.PlainDerivationStatus().Armed)

	prev := auditArmed
	auditArmed = false
	t.Cleanup(func() { auditArmed = prev })

	disarmed := newAuditFixture(t, seedUnitsSpec, nil)
	enrolled, refusal := disarmed.p.InstallAudit(AuditOptions{})
	require.False(t, enrolled)
	require.Equal(t, "disabled by deployment", refusal)

	v := disarmed.p.PlainRetractionTransport(false)
	require.True(t, v.Classified)
	require.Equal(t, RetractionTransportDerivationAuditDisarmed, v.Transport)
	require.Empty(t, v.Refusal, "a declared-but-voided transport is not a refusal to have one")

	st := disarmed.p.PlainDerivationStatus()
	assert.True(t, st.Eligible, "the shape still supports the transport")
	assert.False(t, st.Armed, "nothing is carrying it while the audit is disarmed")
}

// TestPlainRetractionTransport_AuthPlaneClaimsNoDerivation pins the plane
// boundary at the verdict rather than only at the gate that reads it. The
// licence refuses the auth plane outright, so a shape that would otherwise be
// T1 must not be reported as carrying it — an auth-plane lens's only transport
// is a target diff it owns.
func TestPlainRetractionTransport_AuthPlaneClaimsNoDerivation(t *testing.T) {
	f := licenceFixture(t, seedUnitsSpec)
	require.Equal(t, RetractionTransportDerivation, f.p.PlainRetractionTransport(false).Transport)

	v := f.p.PlainRetractionTransport(true)
	require.True(t, v.Classified)
	require.Equal(t, RetractionTransportNone, v.Transport)
	require.Contains(t, v.Refusal, "auth plane")
}

// TestPlainRetractionTransport_DiffRetractionIsReportedScopedOrNot pins the two
// T2 spellings apart. They are not cosmetic: the scoped one says the lens's
// diff enumerates its own key prefix in a target it shares, and an operator
// reading "diffRetraction" on a shared bucket is reading a cross-delete.
func TestPlainRetractionTransport_DiffRetractionIsReportedScopedOrNot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	p, _, _, _ := newRetractionPipeline(t, sharedBucketShapeSpec, []string{"key"})
	require.NoError(t, p.SetDiffRetraction(true))
	require.Equal(t, RetractionTransportDiffRetraction, p.PlainRetractionTransport(false).Transport)

	require.NoError(t, p.SetDiffRetractionPrefix("vtx.leaseapp."))
	require.Equal(t, RetractionTransportDiffRetractionPrefix, p.PlainRetractionTransport(false).Transport)
}

// TestPlainDerivationStaticallyEligible_SeedAnchorLabelConjunct is the one
// conjunct of the shared predicate the corpus cannot falsify: no shipped plain
// lens has an unlabeled anchor or one that expands to several concrete types
// while otherwise index-ready, so deleting this conjunct leaves the corpus
// census green. It is also the conjunct a gate restating the licence's
// conditions by hand is likeliest to drop — a taxonomy-expanded lens then passes
// the gate and is never licensed — so it gets its own vector rather than resting
// on a population that happens not to contain one.
//
// The rule state is posed directly because that is where the fact lives:
// ruleinstall derives seedAnchorLabels from the compiled rule's anchor label
// and the resolved taxonomy expansion, and a fixture carrying an unresolvable
// `*` anchor would refuse earlier, at the index's own expansion conjunct,
// never reaching this one.
func TestPlainDerivationStaticallyEligible_SeedAnchorLabelConjunct(t *testing.T) {
	f := licenceFixture(t, seedUnitsSpec)
	base := f.p.ruleState()
	eligible, refusal := f.p.plainDerivationStaticallyEligible(base)
	require.True(t, eligible, "the positive vector must hold: %s", refusal)
	require.Len(t, base.seedAnchorLabels, 1, "the fixture must carry exactly one seed label for the vectors below to move it")

	t.Run("no derivable seed label refuses", func(t *testing.T) {
		rs := base
		rs.seedAnchorLabels = nil
		eligible, refusal := f.p.plainDerivationStaticallyEligible(rs)
		require.False(t, eligible)
		require.Contains(t, refusal, "no single derivable anchor pattern")
	})

	t.Run("an anchor expanding to several concrete types refuses", func(t *testing.T) {
		rs := base
		rs.seedAnchorLabels = map[string]struct{}{"unit": {}, "storageunit": {}}
		eligible, refusal := f.p.plainDerivationStaticallyEligible(rs)
		require.False(t, eligible)
		require.Contains(t, refusal, "several concrete types")
	})
}
