// Label-derivation corpus census — lens-label-key-type-binding-design.md §9.
//
// auth_plane_narrowing_census_test.go pins the narrowing verdict for the two
// auth-plane lenses the gate is about. This file closes the rest of the corpus:
// it derives (labels, exhaustive) from the REAL shipped cypher of EVERY
// installed lens and pins each verdict, so a cypher edit that moves one fails
// here rather than in Capability KV.
//
// `exhaustive` is what licenses the callers that skip work — the plain
// reproject gate, the client relevance gate, the actor-aware narrowed filter
// and its JetStream subject set. Moving a lens from broad to narrow is an
// authorization change, and on a read-grant lens the direction that hurts is
// silent: a withheld event is a row that stops updating, which on the auth
// plane is a grant that never retracts.
//
// Two things the census has to do that a naive sweep does not, both learned:
//
//   - It expands READ-GRANT WALKS first. The pkgregistry snapshot holds
//     un-expanded specs, so the generated cap-read producers — the very lenses
//     whose walk chains compile entirely to OPTIONAL MATCH — are absent from
//     what a raw sweep enumerates.
//   - It enumerates SpecBranches, not just Spec. A multi-walk lens composes to
//     one cypher per branch and carries an EMPTY Spec, so a sweep reading Spec
//     alone silently covers none of them — edgeCatalog included, which is the
//     one live lens whose narrowing depends on a WITH carrying a label its
//     OPTIONAL clause applied.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: the verdict is the thing to
// review, not the table. Read the reported (want → got), satisfy yourself the
// new verdict is what the cypher really earns — a `narrow` verdict claims the
// executor can bind NO type outside the listed set — then record it here.
package refractor_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

type labelVerdict struct {
	exhaustive bool
	labels     string // space-separated, sorted
}

const (
	narrow = true
	broad  = false
)

// corpusLabelVerdicts is the pinned verdict for every executable cypher the
// installed corpus ships, keyed by canonical name (`name#N` for one branch of a
// multi-walk lens, in Walks declaration order).
var corpusLabelVerdicts = map[string]labelVerdict{
	"appointmentReminders":              {narrow, "appointment patient provider"},
	"augurDispatchPending":              {narrow, "augurproposal"},
	"augurProposals":                    {narrow, "augurproposal"},
	"availableListings":                 {narrow, "unit"},
	"cafeLeaseAccounts":                 {narrow, "cafeaccount leaseapp"},
	"cafeLedgerHistory":                 {narrow, "cafeaccount cafetransaction leaseapp"},
	"cafeStaleTabSettlement":            {narrow, "tab"},
	"cafeTabSettlement":                 {narrow, "cafetransaction leaseapp tab"},
	"capability":                        {narrow, "identity role"},
	"capabilityAuthorContext":           {narrow, "meta"},
	"capabilityAuthorPending":           {narrow, "capabilityproposal"},
	"capabilityProposals":               {narrow, "capabilityproposal"},
	"capabilityRead":                    {narrow, "identity"},
	"capabilityReadGrants":              {narrow, "identity"},
	"capabilityReadWildcardGrants":      {narrow, "identity role"},
	"capabilityRoleIndex":               {narrow, "permission role"},
	"capabilityRoles":                   {narrow, "identity permission role"},
	"clinicAppointments":                {narrow, "appointment building patient provider"},
	"clinicAppointmentsRead":            {narrow, "appointment building identity patient provider"},
	"clinicLedgerHistory":               {narrow, "appointment clinicaccount clinictransaction patient"},
	"clinicNoShowSettlement":            {narrow, "appointment clinicaccount clinictransaction patient"},
	"clinicPatientAccounts":             {narrow, "clinicaccount patient"},
	"clinicPatientReadGrants":           {narrow, "patient"},
	"clinicEncountersRead":              {narrow, "appointment patient provider"},
	"clinicPatients":                    {narrow, "patient"},
	"clinicPatientsRead":                {narrow, "appointment building identity patient provider"},
	"clinicProviderReadGrants":          {narrow, "provider"},
	"clinicProviders":                   {narrow, "identity provider"},
	"clinicSiteBackfill":                {narrow, "appointment building"},
	"clinicSites":                       {narrow, "building"},
	"consoleOperatorReadGrants":         {narrow, "identity role"},
	"demoOperatorReadGrants":            {narrow, "identity role"},
	"duplicateCandidates":               {narrow, "identity"},
	"edgeCatalog#1":                     {narrow, "identity meta permission role service"},
	"edgeEntityBookings":                {narrow, "booking identity instructor session studio"},
	"edgeEntitySessions#1":              {narrow, "identity instructor session studio"},
	"edgeEntityTabs":                    {narrow, "identity leaseapp tab unit"},
	"edgeInstances":                     {narrow, "identity service"},
	"edgeManifestProviderReadGrants":    {narrow, "appointment identity instructor provider service serviceprovider session"},
	"edgeProviderQueue":                 {narrow, "identity service serviceprovider"},
	"edgeProviderSchedule":              {narrow, "appointment identity provider"},
	"edgeStaffPanes":                    {narrow, "identity meta role"},
	"followUpReminders":                 {narrow, "appointment patient provider"},
	"frontDeskBookings":                 {narrow, "booking leaseapp session"},
	"frontDeskLeaseDetails":             {narrow, "leaseapp unit"},
	"frontDeskVisits":                   {narrow, "appointment leaseapp"},
	"identityCredentialBindingsRead":    {narrow, "identity"},
	"identityCredentialsRead":           {narrow, "identity"},
	"identityIndexHint":                 {narrow, "identityindex"},
	"leaseAccounts":                     {narrow, "account leaseapp"},
	"leaseApplicationsRead":             {narrow, "augurproposal identity leaseapp object service unit"},
	"leaseExpiry":                       {narrow, "identity leaseapp renewal unit"},
	"leaseRentSettlement":               {narrow, "clause leaseapp"},
	"ledgerHistory":                     {narrow, "account clause leaseapp transaction"},
	"oneBillCafeEntries":                {narrow, "cafeaccount cafetransaction leaseapp"},
	"oneBillClinicEntries":              {narrow, "clinicaccount clinictransaction identity leaseapp patient"},
	"oneBillRentEntries":                {narrow, "account leaseapp transaction"},
	"oneBillWellnessEntries":            {narrow, "identity leaseapp wellnessaccount wellnesstransaction"},
	"pastDueAppointments":               {narrow, "appointment patient provider"},
	"pastDueBookings":                   {narrow, "booking identity session"},
	"patientIdentityReadGrants":         {narrow, "identity patient"},
	"piiKeyEnvelope":                    {narrow, "identity"},
	"providerAppointmentsRead":          {narrow, "appointment building identity patient provider"},
	"providerIdentityReadGrants":        {narrow, "identity provider role"},
	"providerSites":                     {narrow, "building provider"},
	"renewalComplete":                   {narrow, "identity leaseapp renewal service unit"},
	"renewalsRead":                      {narrow, "identity leaseapp renewal unit"},
	// retentionKeyStatus narrows to the holder type alone, and that single
	// label is what makes the lens self-updating: a shred writes
	// vtx.retentionclass.<H>.piiKey, which matches the one subject this
	// narrowed filter subscribes, so a destruction reaches the operator view
	// without waiting for an unrelated event. Same in-band property
	// shredStatus has for identities — and precisely the property a SECURE
	// lens anchored elsewhere cannot have, because a class holder is not a
	// vertex such a lens binds (retention-class-key-custody-design.md §6.3,
	// which is why the destruction THERE needs a driven rebuild).
	"retentionKeyStatus":                {narrow, "retentionclass"},
	"shredStatus":                       {narrow, "identity"},
	"staffReadGrants":                   {narrow, "building identity role"},
	"unroutedTasks":                     {narrow, "role task"},
	"visitSeriesDue":                    {narrow, "patient provider visitseries"},
	"visitSeriesRead":                   {narrow, "building identity patient provider visitseries"},
	"wellnessBookingReminders":          {narrow, "booking identity session"},
	"wellnessBookings":                  {narrow, "booking identity session studio"},
	"wellnessClassPriceSettlement":      {narrow, "booking identity session wellnessaccount wellnesstransaction"},
	"wellnessInstructors":               {narrow, "instructor studio"},
	"wellnessLedgerHistory":             {narrow, "identity wellnessaccount wellnesstransaction"},
	"wellnessMemberAccounts":            {narrow, "booking identity wellnessaccount"},
	"wellnessNoShowSettlement":          {narrow, "booking identity wellnessaccount wellnesstransaction"},
	"wellnessOrphanedBookingSettlement": {narrow, "booking session"},
	"wellnessRefundSettlement":          {narrow, "wellnessrefund wellnesstransaction"},
	"wellnessStudios":                   {narrow, "studio"},

	// Broad: something in the cypher binds a type no label names — an unlabeled
	// node, a variable-length hop, or a name re-seeded after the clause that
	// labeled it went out of scope. These reproject on every event and their
	// consumers take the unconditional fan-out. The label set is still pinned:
	// widening it is safe, but a label DROPPING out is how a broad lens quietly
	// stops being judged against a type it reads.
	"applicantRosterRead":         {broad, "building identity leaseapp unit"},
	"cafeIdentitiesRead":          {broad, "identity leaseapp"},
	"cafeLeaseWorkplaces":         {broad, "leaseapp"},
	"capabilityEphemeral":         {broad, "identity role task"},
	"capabilityServiceAccess":     {broad, "identity service"},
	"clauseSatisfaction":          {broad, "account clause identity transaction"},
	"edgeCatalog#0":               {broad, "identity meta service"},
	"edgeEntityMenuItems":         {broad, "identity menuitem"},
	"edgeEntityProviders":         {broad, "identity provider"},
	"edgeEntitySessions#0":        {broad, "identity instructor session studio"},
	"edgeEntityStudios":           {broad, "identity studio"},
	"edgeIdentity":                {broad, "identity instructor leaseapp patient provider role serviceprovider"},
	"edgeManifestReadGrants":      {broad, "booking identity leaseapp menuitem meta provider service session studio tab task"},
	"edgeManifestStaffReadGrants": {broad, "identity meta permission role studio task workorder"},
	"edgeServices":                {broad, "identity service"},
	"edgeStaffWorkOrders":         {broad, "identity workorder"},
	"edgeTasks#0":                 {broad, "identity task unit"},
	"edgeTasks#1":                 {broad, "identity role task unit"},
	"identityAnchors":             {broad, "identity"},
	// Deliberately broad, and it must stay that way. identityErasureResidue's
	// five fan-out arms bind UNLABELED nodes on purpose: each mirrors a
	// kv.Links(subject, relation, direction, …) enumeration in
	// UnbindIdentityCredentials / PurgeIdentityDedupFootprint, and those filter
	// by relation and direction with no type filter at all — an `indexes` link's
	// source type is a wildcard in the server filter, which is the confinement
	// hazard that op's own build had to close. Labelling an arm would make the
	// lens count a SUBSET of what the op sweeps, so residue could read zero while
	// a live link naming the erased person remains, and the completion seal would
	// be written over it. The reprojection cost is bounded by the anchor
	// predicate: only erasure-requested identities have a row at all.
	"identityErasureResidue":        {broad, "identity"},
	"landlordLeaseApplicationsRead": {broad, "building identity leaseapp service unit"},
	"landlordUnitsRead":             {broad, "building identity unit"},
	"leaseApplicationComplete":      {broad, "identity leaseapp object service task unit"},
	"menuCatalog":                   {broad, "menuitem"},
	"myTasks":                       {broad, "identity role task"},
	"objectAttachments":             {broad, "object"},
	"objectLiveness":                {broad, "object"},
	"orphanedTaskGrants":            {broad, "task"},
	"wellnessIdentitiesRead":        {broad, "identity leaseapp"},
	"wellnessMembers":               {broad, "identity leaseapp"},
	"wellnessSessions":              {broad, "instructor session studio"},
}

// corpusLabelDerivation derives every installed cypher's verdict the way
// activation does, walk-expanded and branch-inclusive.
func corpusLabelDerivation(t *testing.T) map[string]labelVerdict {
	t.Helper()
	eng := full.New()
	got := make(map[string]labelVerdict)

	derive := func(name, spec string) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		set, exhaustive := fullCR.ReferencedLabels()
		sorted := make([]string, 0, len(set))
		for l := range set {
			sorted = append(sorted, l)
		}
		sort.Strings(sorted)
		_, dup := got[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		got[name] = labelVerdict{exhaustive, strings.Join(sorted, " ")}
	}
	addLens := func(l pkgmgr.LensSpec) {
		if len(l.SpecBranches) > 0 {
			for i, b := range l.SpecBranches {
				derive(fmt.Sprintf("%s#%d", l.CanonicalName, i), b)
			}
			return
		}
		if l.Spec == "" {
			// The only lens with no cypher at all is an eventStream lens: its
			// rows come from the event payload, so there is no pattern to derive
			// a label set from. Anything else reaching here compiled to nothing
			// and would leave the corpus silently — the shape this census exists
			// to make impossible.
			require.NotNilf(t, l.Source,
				"lens %q carries neither a Spec nor SpecBranches and is not event-sourced", l.CanonicalName)
			require.Equalf(t, "eventStream", l.Source.Kind,
				"lens %q has no cypher but sources %q, not an event stream", l.CanonicalName, l.Source.Kind)
			return
		}
		derive(l.CanonicalName, l.Spec)
	}

	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		require.Truef(t, ok, "registered package %q must resolve", name)
		expanded, err := def.ExpandReadGrantWalks()
		require.NoErrorf(t, err, "%s read-grant walks must compose", name)
		for _, l := range expanded.Lenses {
			addLens(l)
		}
	}
	for _, l := range []bootstrap.LensDefinition{
		bootstrap.CapabilityLensDefinition(),
		bootstrap.CapabilityReadLensDefinition(),
		bootstrap.CapabilityReadGrantsLensDefinition(),
		bootstrap.CapabilityReadWildcardGrantsLensDefinition(),
	} {
		derive(l.CanonicalName, l.CypherRule)
	}
	return got
}

func TestCorpusLabelDerivation_PinnedVerdicts(t *testing.T) {
	got := corpusLabelDerivation(t)

	for name, want := range corpusLabelVerdicts {
		have, present := got[name]
		if !assert.Truef(t, present,
			"pinned lens %q is no longer installed — remove its row if the lens was retired", name) {
			continue
		}
		require.Equalf(t, want.exhaustive, have.exhaustive,
			"%s moved between narrow and broad — that is an authorization change, not a refactor", name)
		require.Equalf(t, want.labels, have.labels,
			"%s's label set moved; the fan-out arms and the JetStream filter both judge events against it", name)
	}
	for name := range got {
		_, pinned := corpusLabelVerdicts[name]
		require.Truef(t, pinned,
			"lens %q ships with no pinned narrowing verdict (derived: exhaustive=%v labels=%q) — "+
				"review the verdict, then record it in corpusLabelVerdicts",
			name, got[name].exhaustive, got[name].labels)
	}
}

// TestCorpusLabelDerivation_WalkComposedLensesStillNarrow names the generated
// walk-composed lenses whose narrowing the derivation must keep. pkgmgr compiles
// every walk chain as OPTIONAL MATCH under a single required head, so these
// carry their labels in exactly the position a derivation that only trusted
// required MATCHes would discard — and edgeCatalog#1 stages its walk behind a
// WITH as well, so it narrows only while the carry honors an optional label too.
// Named here rather than left to the table above because losing them would leave
// every broad row in that table untouched: the regression reads as a table of
// unchanged verdicts with three fewer narrow ones.
func TestCorpusLabelDerivation_WalkComposedLensesStillNarrow(t *testing.T) {
	got := corpusLabelDerivation(t)
	for _, name := range []string{
		"edgeCatalog#1",
		"edgeManifestProviderReadGrants",
		"edgeEntitySessions#1",
	} {
		have, present := got[name]
		require.Truef(t, present, "%s must still be installed", name)
		require.Truef(t, have.exhaustive,
			"%s labels its walk variables inside OPTIONAL clauses — it must still narrow", name)
	}
}
