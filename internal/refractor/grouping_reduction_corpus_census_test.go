// Grouping-reduction corpus census — full-engine-grouping-key-reduction-design.md §3.
//
// The design stated this reduction's population by reading cypher by eye: "the
// three generated producers are the entire census of this shape; every other
// multi-`WITH` lens in the corpus carries only `*nodeRef` values". Both halves
// were wrong. privacy-base's identityErasureResidue chains FIVE aggregating
// clauses carrying int64 counts forward — the carried-accumulator shape exactly,
// outside edge-manifest — and cafe-domain's cafeTabSettlement is a second
// multi-`WITH` the eye-census never saw. The first of those had its grouping key
// silently changed by the build, with no equivalence test anywhere.
//
// That is the Refractor dossier's "site censuses derived from key shapes
// undercount — derive the census from the MATCHER, not the key grammar" class,
// seen twice now, so it gets mechanized: this file asks the analysis itself what
// every shipped cypher earns, and pins the answer. A lens that starts or stops
// arming a reduction, or whose effective grouping key moves, fails here and
// forces a deliberate re-reading rather than a silent semantic change.
//
// It shares forEachCorpusCypher with the label census on purpose — read that
// file's note on why a second sweep of its own would quietly cover a different
// corpus and pin a different thing.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: the verdict is what to review,
// not the table. `key(a b)` is the clause's effective grouping key; `+N` is how
// many carried items it stops rendering into that key (which requires each to be
// a bare carry the analysis already proved determined); `refused(x)` is a clause
// the walk would not reason about, naming the column that ended the chain. A
// lens gaining a `+N` is a lens whose grouping key this engine now reduces —
// satisfy yourself the rows cannot move (they may not), then record it.
package refractor_test

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// groupingVerdict is one executable cypher's pinned grouping-reduction answer.
type groupingVerdict struct {
	// stages is the per-projecting-clause verdict in clause order, space-joined
	// — see stageVerdict for the vocabulary.
	stages string
	// armed is how many clauses carry a stored redundancy mask, i.e. how many
	// the executor actually renders a shortened key for. It must equal the
	// number of stages showing a `+N`, which the test asserts rather than
	// assumes.
	armed int
}

// corpusGroupingVerdicts pins every executable cypher the installed corpus
// ships. The three generated read-grant producers and privacy-base's erasure
// residue are the only lenses that reduce anything; every other row records a
// cypher that earns nothing, which is the half of the census the design's
// eye-reading got wrong.
var corpusGroupingVerdicts = map[string]groupingVerdict{
	"applicantRosterRead":               {"p", 0},
	"appointmentReminders":              {"p", 0},
	"augurDispatchPending":              {"p", 0},
	"augurProposals":                    {"p", 0},
	"availableListings":                 {"p", 0},
	"cafeIdentitiesRead":                {"p", 0},
	"cafeLeaseAccounts":                 {"p", 0},
	"cafeLeaseWorkplaces":               {"p", 0},
	"cafeLedgerHistory":                 {"p", 0},
	"cafeStaleTabSettlement":            {"p", 0},
	"cafeTabSettlement":                 {"key(entityKey itemsMemo l lines openedAt settledAt status totalCents) p!l p!txCount", 0},
	"capability":                        {"p", 0},
	"capabilityAuthorContext":           {"p", 0},
	"capabilityAuthorPackages":          {"p", 0},
	"capabilityAuthorPending":           {"p", 0},
	"capabilityEphemeral":               {"key(actorKey)", 0},
	"capabilityProposals":               {"p", 0},
	"capabilityRead":                    {"p", 0},
	"capabilityReadGrants":              {"p", 0},
	"capabilityReadWildcardGrants":      {"p", 0},
	"capabilityRoleIndex":               {"key(operationType projectedAt)", 0},
	"capabilityRoles":                   {"key(actorKey)", 0},
	"capabilityServiceAccess":           {"key(actorKey)", 0},
	"clauseSatisfaction":                {"key(accountKey amountCents chargeValidUntil condKey conditioned entityKey inspectionCompleted inspectorKey period) p!condKey", 0},
	"clinicAppointments":                {"p", 0},
	"clinicAppointmentsRead":            {"p", 0},
	"clinicEncountersRead":              {"p", 0},
	"clinicIdentitiesRead":              {"p", 0},
	"clinicLedgerHistory":               {"p", 0},
	"clinicNoShowSettlement":            {"key(accountKey entityKey feeCents patientKey status) p", 0},
	"clinicPatientAccounts":             {"p", 0},
	"clinicPatientReadGrants":           {"p", 0},
	"clinicPatients":                    {"p", 0},
	"clinicPatientsRead":                {"key(id p) p!id", 0},
	"clinicProviderReadGrants":          {"p", 0},
	"clinicProviders":                   {"p", 0},
	"clinicSiteBackfill":                {"p", 0},
	"clinicSites":                       {"p", 0},
	"consoleOperatorReadGrants":         {"p", 0},
	"demoOperatorReadGrants":            {"p", 0},
	"duplicateCandidates":               {"p", 0},
	"edgeCatalog#0":                     {"p p!op", 0},
	"edgeCatalog#1":                     {"p p!op", 0},
	"edgeCatalog#2":                     {"p p!op", 0},
	"edgeEntityBookings":                {"p p!bk", 0},
	"edgeEntityMenuItems":               {"p p!container", 0},
	"edgeEntityProviders":               {"p p!prov", 0},
	"edgeEntitySessions#0":              {"p p!instr", 0},
	"edgeEntitySessions#1":              {"p p!instr", 0},
	"edgeEntityStudios":                 {"p p!place", 0},
	"edgeEntityTabs":                    {"p p!tab", 0},
	"edgeIdentity":                      {"key(anchor claimed displayName identityKey ns sealedName)", 0},
	"edgeInstances":                     {"p p!inst", 0},
	"edgeManifestProviderReadGrants":    {"key(identity) key(identity)+1 key(identity)+2 p!identity", 2},
	"edgeManifestReadGrants":            {"key(identity) key(identity)+1 key(identity)+2 key(identity)+3 key(identity)+4 key(identity)+5 key(identity)+6 key(identity)+7 key(identity)+8 key(identity)+9 p!identity", 9},
	"edgeManifestStaffReadGrants":       {"key(identity) key(identity)+1 key(identity)+2 key(identity)+3 key(identity)+4 p!identity", 4},
	"edgeProviderQueue":                 {"p p!inst", 0},
	"edgeProviderSchedule":              {"p p!appt", 0},
	"edgeServices":                      {"p p!provider", 0},
	"edgeStaffPanes":                    {"p p!pane", 0},
	"edgeStaffWorkOrders":               {"p p!place", 0},
	"edgeTasks#0":                       {"p p!assignee", 0},
	"edgeTasks#1":                       {"p p!assignee", 0},
	"followUpReminders":                 {"p", 0},
	"frontDeskBookings":                 {"p", 0},
	"frontDeskLeaseDetails":             {"p", 0},
	"frontDeskVisits":                   {"p", 0},
	"identityAnchors":                   {"key(actorKey)", 0},
	"identityCredentialBindingsRead":    {"p", 0},
	"identityCredentialsRead":           {"p", 0},
	"identityErasureResidue":            {"key(i) key(i)+1 key(i)+2 key(i)+3 key(i)+4 p!i", 4},
	"identityIndexHint":                 {"p", 0},
	"landlordLeaseApplicationsRead":     {"key(applicantEmailEnv applicantKey applicantNameEnv applicantPhoneEnv declineReason employmentVerified entityKey guarantorIncomeToRentMet hasCoApplicant hasGuarantor incomeToRentMet landlordDecision landlordKey profileSubmitted referenceCount signedAt ssnVal termsLeaseTermMonths termsMoveInDate termsRequestedRent u unitAddress unitCity unitCurrency unitKey unitRegion unitRent unitStatus) p!applicantEmailEnv", 0},
	"landlordUnitsRead":                 {"p", 0},
	"leaseAccounts":                     {"p", 0},
	"leaseApplicationComplete":          {"key(applicant declineReason employmentVerified entityKey guarantorIncomeToRentMet hasCoApplicant hasGuarantor incomeToRentMet landlordDecision profileSubmittedAt referenceCount signedAt ssnVal termsLeaseTermMonths termsMoveInDate termsRequestedRent unitAddress unitAvailableFrom unitBathrooms unitBedrooms unitCity unitCurrency unitKey unitLeaseTermMonths unitRegion unitRent unitStatus) p!profileSubmittedAt", 0},
	"leaseApplicationsRead":             {"key(applicantKey declineReason employmentVerified entityKey guarantorIncomeToRentMet hasCoApplicant hasGuarantor incomeToRentMet landlordDecision profileSubmitted referenceCount signedAt ssnVal termsLeaseTermMonths termsMoveInDate termsRequestedRent unitAddress unitAvailableFrom unitBathrooms unitBedrooms unitCity unitCurrency unitKey unitRegion unitRent unitStatus) p!applicantKey", 0},
	"leaseExpiry":                       {"key(entityKey landlordDecision leaseEnd renewalOpensAt signedAt unitKey) p!landlordDecision", 0},
	"leaseRentSettlement":               {"key(accountKey decision entityKey requestedRent) p!decision", 0},
	"ledgerHistory":                     {"p", 0},
	"menuCatalog":                       {"p", 0},
	"myTasks":                           {"key(actorKey)", 0},
	"objectAttachments":                 {"key(contentType digest encryption entityKey governingIdentity sensitive size storeName) p", 0},
	"objectLiveness":                    {"key(entityKey linkEpoch liveLinks storeName) p!liveLinks", 0},
	"oneBillCafeEntries":                {"p", 0},
	"oneBillClinicEntries":              {"p", 0},
	"oneBillRentEntries":                {"p", 0},
	"oneBillWellnessEntries":            {"p", 0},
	"opCatalog":                         {"key(description dispatchAuthContext dispatchClass dispatchContextParams dispatchOptionalReads dispatchReads dispatchTargetField dispatchTargetType dispatchVisibleWhen fieldDescriptions group icon inputSchema opMetaKey operationType sensitive shortLabel submitLabel title tone)", 0},
	"orphanedTaskGrants":                {"p p!opKey", 0},
	"pastDueAppointments":               {"p", 0},
	"pastDueBookings":                   {"p", 0},
	"patientIdentityReadGrants":         {"p", 0},
	"piiKeyEnvelope":                    {"p", 0},
	"providerAppointmentsRead":          {"p", 0},
	"providerIdentityReadGrants":        {"p", 0},
	"providerSites":                     {"p", 0},
	"renewalComplete":                   {"key(entityKey guarantorVerifiedAt hasGuarantor leaseAppKey leaseappAlive signedAt status tenant termsSetAt termsTermMonths) p!leaseAppKey", 0},
	"renewalsRead":                      {"key(cancelReason cycleEnd entityKey guarantorMethod guarantorVerifiedAt hasGuarantor leaseAppKey rentAmount signedAt status tenantKey tenantNameEnv termMonths termsSetAt unitAddress) p!cancelReason", 0},
	"retentionKeyStatus":                {"p", 0},
	"shredStatus":                       {"p", 0},
	"staffReadGrants":                   {"p", 0},
	"unroutedTasks":                     {"p", 0},
	"visitSeriesDue":                    {"p", 0},
	"visitSeriesRead":                   {"p", 0},
	"wellnessBookingReminders":          {"p", 0},
	"wellnessBookings":                  {"p", 0},
	"wellnessClassPriceSettlement":      {"key(accountKey entityKey identityKey priceCents sessionName) p", 0},
	"wellnessIdentitiesRead":            {"p", 0},
	"wellnessInstructors":               {"p", 0},
	"wellnessLedgerHistory":             {"p", 0},
	"wellnessMemberAccounts":            {"p p!id", 0},
	"wellnessMembers":                   {"p", 0},
	"wellnessNoShowSettlement":          {"key(accountKey entityKey feeCents identityKey status) p", 0},
	"wellnessOrphanedBookingSettlement": {"p p!liveSessionKey", 0},
	"wellnessRefundSettlement":          {"key(accountKey amountCents entityKey) p", 0},
	"wellnessSessions":                  {"p", 0},
	"wellnessStudios":                   {"p", 0},
}

// stageVerdict renders one projecting clause's answer.
//
// A GROUPING clause reads `key(a b)` — its effective grouping key — with `+N`
// for the carried items it stops rendering into that key, or `refused(x)`
// naming the column the walk would not reason past. A clause with no aggregator
// renders no key at all, so it reads `p`, or `p!x` where it is the clause that
// ENDS the dependence chain for everything after it — which is how the corpus's
// second multi-`WITH` lens, cafeTabSettlement, is visible here at all.
func stageVerdict(c full.GroupingClauseReduction) string {
	if !c.Grouping {
		if c.Refusal != "" {
			return "p!" + refusalSubject(c.Refusal)
		}
		return "p"
	}
	if c.Refusal != "" {
		return "refused(" + refusalSubject(c.Refusal) + ")"
	}
	shed := 0
	for _, r := range c.Redundant {
		if r {
			shed++
		}
	}
	out := "key(" + strings.Join(c.Key, " ") + ")"
	if shed > 0 {
		out += fmt.Sprintf("+%d", shed)
	}
	return out
}

// refusalSubject pulls the quoted column a refusal names, so the pinned verdict
// identifies WHICH column ended the chain without being brittle about the
// sentence around it.
var refusalQuoted = regexp.MustCompile(`"([^"]*)"`)

func refusalSubject(reason string) string {
	if m := refusalQuoted.FindStringSubmatch(reason); m != nil {
		return m[1]
	}
	return "?"
}

// corpusGroupingReduction derives the verdict for every executable cypher the
// corpus ships.
func corpusGroupingReduction(t *testing.T) map[string]groupingVerdict {
	t.Helper()
	eng := full.New()
	got := map[string]groupingVerdict{}
	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)

		stages := []string{}
		armed := 0
		for _, c := range fullCR.GroupingReduction() {
			stages = append(stages, stageVerdict(c))
			if !c.Grouping {
				continue
			}
			for _, r := range c.Redundant {
				if r {
					armed++
					break
				}
			}
		}
		_, duplicate := got[name]
		require.Falsef(t, duplicate, "two corpus cyphers share the name %q", name)
		got[name] = groupingVerdict{stages: strings.Join(stages, " "), armed: armed}
	})
	return got
}

// TestCorpusGroupingReduction_PinnedVerdicts is the census. Every executable
// cypher in the corpus is pinned, both the ones that reduce and the ones that
// do not — an unpinned lens fails, and so does a pinned one whose answer moved.
func TestCorpusGroupingReduction_PinnedVerdicts(t *testing.T) {
	got := corpusGroupingReduction(t)
	require.Greaterf(t, len(got), 100,
		"the corpus enumeration collapsed to %d cyphers — this census is only worth what it covers", len(got))

	for name, want := range corpusGroupingVerdicts {
		have, present := got[name]
		if !assert.Truef(t, present,
			"pinned lens %q is no longer installed — remove its row if the lens was retired", name) {
			continue
		}
		require.Equalf(t, want.stages, have.stages,
			"%s's grouping-reduction verdict moved. The engine now partitions this lens's rows "+
				"differently than the pin records, which is a change to what it projects, not a refactor", name)
		require.Equalf(t, want.armed, have.armed,
			"%s arms a redundancy mask on a different number of clauses than pinned", name)
	}
	for name, have := range got {
		_, pinned := corpusGroupingVerdicts[name]
		require.Truef(t, pinned,
			"lens %q ships with no pinned grouping-reduction verdict (derived: stages=%q armed=%d) — "+
				"review it, then record it in corpusGroupingVerdicts",
			name, have.stages, have.armed)
	}
}

// TestCorpusGroupingReduction_ArmedLensesAreTheKnownPopulation names the lenses
// this reduction actually changes anything for, as a list rather than as a
// property of the table above. Losing one would leave every other row in that
// table untouched: the regression reads as an unchanged census with one fewer
// reducing lens, which is exactly how the design's original census went wrong.
//
// Each name here must also carry an equivalence test proving its rows do not
// move — the generated producers in ruleengine/full's
// grouping_equivalence_test.go, identityErasureResidue in
// grouping_corpus_lens_test.go.
func TestCorpusGroupingReduction_ArmedLensesAreTheKnownPopulation(t *testing.T) {
	got := corpusGroupingReduction(t)
	armed := []string{}
	for name, v := range got {
		if v.armed > 0 {
			armed = append(armed, name)
		}
	}
	sort.Strings(armed)
	require.Equal(t, []string{
		"edgeManifestProviderReadGrants",
		"edgeManifestReadGrants",
		"edgeManifestStaffReadGrants",
		"identityErasureResidue",
	}, armed,
		"the population of lenses whose grouping key this engine reduces has changed. "+
			"A lens joining this list needs an equivalence test before it ships; one leaving it "+
			"means the reduction stopped applying where the design says it does")
}
