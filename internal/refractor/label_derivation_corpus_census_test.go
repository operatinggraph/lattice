// Label-derivation corpus census — lens-label-key-type-binding-design.md §9.
//
// auth_plane_narrowing_census_test.go pins the narrowing verdict for the two
// auth-plane lenses the gate is about. This file closes the rest of the corpus:
// it derives (labels, exhaustive, filter mode) from the REAL shipped cypher of
// EVERY installed lens and pins each verdict, so a cypher edit that moves one
// fails here rather than in Capability KV.
//
// `mode` is the same derivation stated as the lens's DELIVERY footprint —
// exactly what its health entry now reports (dynamic-type-taxonomy-design.md
// §10.3). It is pinned here rather than left implicit because it moves on
// changes the other two columns cannot see: dropping a relation type demotes a
// lens from narrowed-relation to narrowed-label with its label set untouched.
// Read labelVerdict.mode for the two things this column deliberately does not
// claim.
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
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

type labelVerdict struct {
	exhaustive bool
	labels     string // space-separated, sorted
	// mode is the Core KV consumer filter this cypher's OWN derivation earns —
	// health.FilterMode{NarrowedRelation,NarrowedLabel,Broad}, taken from the
	// real pipeline.ConsumerFilter, not recomputed here
	// (dynamic-type-taxonomy-design.md §10.3's footprint vocabulary). It is the
	// operator-visible consequence of the two columns beside it: exhaustive
	// decides narrow-vs-broad, the label COUNT decides whether the cap keeps it
	// narrow, and the relation set decides which of the two narrowed modes it
	// lands in — a lens can keep an unchanged label set and still double its
	// delivery volume by losing a relation type.
	//
	// TWO LIMITS ON WHAT THIS PIN CLAIMS, both structural to a census that
	// reads compiled cypher rather than a running Refractor:
	//
	//   - It is derived on a PLAIN pipeline. An actor-aware lens (an
	//     actorAggregate or Personal lens) has more conjuncts than its cypher —
	//     pattern-closure, a sweep plan, its anchor type in the label set — and
	//     narrows by label only even when its relation set is exhaustive. For
	//     those lenses this column is the cypher's own verdict and an UPPER
	//     bound on the runtime one; auth_plane_narrowing_census_test.go pins the
	//     runtime verdict for the auth-plane lenses that matter most.
	//   - It is PER EXECUTABLE CYPHER, like every other column here. A
	//     multi-walk lens's real filter comes from the UNION over its branches,
	//     so its `name#N` rows pin each branch's own mode, not the lens's.
	mode string
}

const (
	narrow = true
	broad  = false
)

// The consumer-filter modes, in the health entry's own vocabulary — the same
// constants the lens writes to Health KV, so a row here and an operator's view
// of that lens read as the same word.
const (
	modeRelation = health.FilterModeNarrowedRelation
	modeLabel    = health.FilterModeNarrowedLabel
	modeBroad    = health.FilterModeBroad
)

// corpusLabelVerdicts is the pinned verdict for every executable cypher the
// installed corpus ships, keyed by canonical name (`name#N` for one branch of a
// multi-walk lens, in Walks declaration order).
var corpusLabelVerdicts = map[string]labelVerdict{
	"appointmentReminders":           {narrow, "appointment patient provider", modeRelation},
	"augurDispatchPending":           {narrow, "augurproposal", modeRelation},
	"augurProposals":                 {narrow, "augurproposal", modeRelation},
	"availableListings":              {narrow, "unit", modeRelation},
	"cafeLeaseAccounts":              {narrow, "cafeaccount leaseapp", modeRelation},
	"cafeLedgerHistory":              {narrow, "cafeaccount cafetransaction leaseapp", modeRelation},
	"cafeStaleTabSettlement":         {narrow, "tab", modeRelation},
	"cafeTabSettlement":              {narrow, "cafetransaction leaseapp tab", modeRelation},
	"capability":                     {narrow, "identity role", modeRelation},
	"capabilityAuthorContext":        {narrow, "meta", modeRelation},
	"capabilityAuthorPending":        {narrow, "capabilityproposal", modeRelation},
	"capabilityProposals":            {narrow, "capabilityproposal", modeRelation},
	"capabilityRead":                 {narrow, "identity", modeRelation},
	"capabilityReadGrants":           {narrow, "identity", modeRelation},
	"capabilityReadWildcardGrants":   {narrow, "identity role", modeRelation},
	"capabilityRoleIndex":            {narrow, "permission role", modeRelation},
	"capabilityRoles":                {narrow, "identity permission role", modeRelation},
	"clinicAppointments":             {narrow, "appointment building patient provider", modeLabel},
	"clinicAppointmentsRead":         {narrow, "appointment building identity patient provider", modeLabel},
	"clinicLedgerHistory":            {narrow, "appointment clinicaccount clinictransaction patient", modeLabel},
	"clinicNoShowSettlement":         {narrow, "appointment clinicaccount clinictransaction patient", modeLabel},
	"clinicPatientAccounts":          {narrow, "clinicaccount patient", modeRelation},
	"clinicPatientReadGrants":        {narrow, "patient", modeRelation},
	"clinicEncountersRead":           {narrow, "appointment patient provider", modeRelation},
	"clinicPatients":                 {narrow, "patient", modeRelation},
	"clinicPatientsRead":             {narrow, "appointment building identity patient provider", modeLabel},
	"clinicProviderReadGrants":       {narrow, "provider", modeRelation},
	"clinicProviders":                {narrow, "identity provider", modeRelation},
	"clinicSiteBackfill":             {narrow, "appointment building", modeRelation},
	"clinicSites":                    {narrow, "building", modeRelation},
	"consoleOperatorReadGrants":      {narrow, "identity role", modeRelation},
	"demoOperatorReadGrants":         {narrow, "identity role", modeRelation},
	"duplicateCandidates":            {narrow, "identity", modeRelation},
	"edgeCatalog#1":                  {narrow, "identity meta permission role service", modeLabel},
	"edgeEntityBookings":             {narrow, "booking identity instructor session studio", modeLabel},
	"edgeEntitySessions#1":           {narrow, "identity instructor session studio", modeLabel},
	"edgeEntityTabs":                 {narrow, "identity leaseapp tab unit", modeLabel},
	"edgeInstances":                  {narrow, "identity service", modeRelation},
	"edgeManifestProviderReadGrants": {narrow, "appointment identity instructor provider service serviceprovider session", modeLabel},
	"edgeProviderQueue":              {narrow, "identity service serviceprovider", modeRelation},
	"edgeProviderSchedule":           {narrow, "appointment identity provider", modeRelation},
	"edgeStaffPanes":                 {narrow, "identity meta role", modeRelation},
	"followUpReminders":              {narrow, "appointment patient provider", modeRelation},
	"frontDeskBookings":              {narrow, "booking leaseapp session", modeRelation},
	"frontDeskLeaseDetails":          {narrow, "leaseapp unit", modeRelation},
	"frontDeskVisits":                {narrow, "appointment leaseapp", modeRelation},
	"identityCredentialBindingsRead": {narrow, "identity", modeRelation},
	"identityCredentialsRead":        {narrow, "identity", modeRelation},
	"identityIndexHint":              {narrow, "identityindex", modeRelation},
	"leaseAccounts":                  {narrow, "account leaseapp", modeRelation},
	"leaseApplicationsRead":          {narrow, "augurproposal identity leaseapp object service unit", modeLabel},
	"leaseExpiry":                    {narrow, "identity leaseapp renewal unit", modeLabel},
	"leaseRentSettlement":            {narrow, "clause leaseapp", modeRelation},
	"ledgerHistory":                  {narrow, "account clause leaseapp transaction", modeLabel},
	"oneBillCafeEntries":             {narrow, "cafeaccount cafetransaction leaseapp", modeRelation},
	"oneBillClinicEntries":           {narrow, "clinicaccount clinictransaction identity leaseapp patient", modeLabel},
	"oneBillRentEntries":             {narrow, "account leaseapp transaction", modeRelation},
	"oneBillWellnessEntries":         {narrow, "identity leaseapp wellnessaccount wellnesstransaction", modeLabel},
	"pastDueAppointments":            {narrow, "appointment patient provider", modeRelation},
	"pastDueBookings":                {narrow, "booking identity session", modeRelation},
	"patientIdentityReadGrants":      {narrow, "identity patient", modeRelation},
	"piiKeyEnvelope":                 {narrow, "identity", modeRelation},
	"providerAppointmentsRead":       {narrow, "appointment building identity patient provider", modeLabel},
	"providerIdentityReadGrants":     {narrow, "identity provider role", modeRelation},
	"providerSites":                  {narrow, "building provider", modeRelation},
	"renewalComplete":                {narrow, "identity leaseapp renewal service unit", modeLabel},
	"renewalsRead":                   {narrow, "identity leaseapp renewal unit", modeLabel},
	// retentionKeyStatus narrows to the holder type alone, and that single
	// label is what makes the lens self-updating: a shred writes
	// vtx.retentionclass.<H>.piiKey, which matches the one subject this
	// narrowed filter subscribes, so a destruction reaches the operator view
	// without waiting for an unrelated event. Same in-band property
	// shredStatus has for identities — and precisely the property a SECURE
	// lens anchored elsewhere cannot have, because a class holder is not a
	// vertex such a lens binds (retention-class-key-custody-design.md §6.3,
	// which is why the destruction THERE needs a driven rebuild).
	"retentionKeyStatus":                {narrow, "retentionclass", modeRelation},
	"shredStatus":                       {narrow, "identity", modeRelation},
	"staffReadGrants":                   {narrow, "building identity role", modeRelation},
	"unroutedTasks":                     {narrow, "role task", modeRelation},
	"visitSeriesDue":                    {narrow, "patient provider visitseries", modeRelation},
	"visitSeriesRead":                   {narrow, "building identity patient provider visitseries", modeLabel},
	"wellnessBookingReminders":          {narrow, "booking identity session", modeRelation},
	"wellnessBookings":                  {narrow, "booking identity session studio", modeLabel},
	"wellnessClassPriceSettlement":      {narrow, "booking identity session wellnessaccount wellnesstransaction", modeLabel},
	"wellnessInstructors":               {narrow, "instructor studio", modeRelation},
	"wellnessLedgerHistory":             {narrow, "identity wellnessaccount wellnesstransaction", modeRelation},
	"wellnessMemberAccounts":            {narrow, "booking identity wellnessaccount", modeRelation},
	"wellnessNoShowSettlement":          {narrow, "booking identity wellnessaccount wellnesstransaction", modeLabel},
	"wellnessOrphanedBookingSettlement": {narrow, "booking session", modeRelation},
	"wellnessRefundSettlement":          {narrow, "wellnessrefund wellnesstransaction", modeRelation},
	"wellnessStudios":                   {narrow, "studio", modeRelation},

	// Broad: something in the cypher binds a type no label names — an unlabeled
	// node, a variable-length hop, or a name re-seeded after the clause that
	// labeled it went out of scope. These reproject on every event and their
	// consumers take the unconditional fan-out. The label set is still pinned:
	// widening it is safe, but a label DROPPING out is how a broad lens quietly
	// stops being judged against a type it reads.
	"applicantRosterRead":         {broad, "building identity leaseapp unit", modeBroad},
	"cafeIdentitiesRead":          {broad, "identity leaseapp", modeBroad},
	"cafeLeaseWorkplaces":         {broad, "leaseapp", modeBroad},
	"capabilityEphemeral":         {broad, "identity role task", modeBroad},
	"capabilityServiceAccess":     {broad, "identity service", modeBroad},
	"clauseSatisfaction":          {broad, "account clause identity transaction", modeBroad},
	"edgeCatalog#0":               {broad, "identity meta service", modeBroad},
	"edgeEntityMenuItems":         {broad, "identity menuitem", modeBroad},
	"edgeEntityProviders":         {broad, "identity provider", modeBroad},
	"edgeEntitySessions#0":        {broad, "identity instructor session studio", modeBroad},
	"edgeEntityStudios":           {broad, "identity studio", modeBroad},
	"edgeIdentity":                {broad, "identity instructor leaseapp patient provider role serviceprovider", modeBroad},
	"edgeManifestReadGrants":      {broad, "booking identity leaseapp menuitem meta provider service session studio tab task", modeBroad},
	"edgeManifestStaffReadGrants": {broad, "identity meta permission role studio task workorder", modeBroad},
	"edgeServices":                {broad, "identity service", modeBroad},
	"edgeStaffWorkOrders":         {broad, "identity workorder", modeBroad},
	"edgeTasks#0":                 {broad, "identity task unit", modeBroad},
	"edgeTasks#1":                 {broad, "identity role task unit", modeBroad},
	"identityAnchors":             {broad, "identity", modeBroad},
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
	"identityErasureResidue":        {broad, "identity", modeBroad},
	"landlordLeaseApplicationsRead": {broad, "building identity leaseapp service unit", modeBroad},
	"landlordUnitsRead":             {broad, "building identity unit", modeBroad},
	"leaseApplicationComplete":      {broad, "identity leaseapp object service task unit", modeBroad},
	"menuCatalog":                   {broad, "menuitem", modeBroad},
	"myTasks":                       {broad, "identity role task", modeBroad},
	"objectAttachments":             {broad, "object", modeBroad},
	"objectLiveness":                {broad, "object", modeBroad},
	"orphanedTaskGrants":            {broad, "task", modeBroad},
	"wellnessIdentitiesRead":        {broad, "identity leaseapp", modeBroad},
	"wellnessMembers":               {broad, "identity leaseapp", modeBroad},
	"wellnessSessions":              {broad, "instructor session studio", modeBroad},
}

// consumerFilterMode runs a compiled cypher through the SAME
// pipeline.ConsumerFilter production registers its consumer from, and reports
// which filter it chose. Recomputing the choice here from the label and
// relation sets would pin this file's arithmetic rather than the platform's, and
// the caps are the pipeline package's own unexported constants precisely so
// there is one place that knows them.
//
// The pipeline is plain and bare — no actor enumerator, no taxonomy resolver, no
// supervisor — which is what makes the pin reproducible from compiled cypher
// alone. See labelVerdict.mode for exactly what that costs.
func consumerFilterMode(t *testing.T, name string, eng *full.Engine, cr ruleengine.CompiledRule) string {
	t.Helper()
	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoErrorf(t, err, "%s census adapter", name)
	p, err := pipeline.New("census-"+name, "nats_kv", bootstrap.CoreKVBucket, nil, nil, adpt, nil)
	require.NoErrorf(t, err, "%s census pipeline", name)
	// A `*`-carrying cypher would refuse here (no resolver installed answers
	// StatusUnknown, and §4.2 refuses activation rather than guess an
	// expansion), so this require is also the census's assertion that the
	// shipped corpus carries no taxonomy sigil yet. When one lands, this is the
	// line that says so, and its mode becomes a function of the live resolver
	// rather than of the cypher.
	require.NoErrorf(t, p.UseFullEngine(eng, cr), "%s must activate against a plain pipeline", name)
	_, _, dec := p.ConsumerFilter()
	return dec.Mode
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
		got[name] = labelVerdict{exhaustive, strings.Join(sorted, " "), consumerFilterMode(t, name, eng, cr)}
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
		require.Equalf(t, want.mode, have.mode,
			"%s's consumer filter mode moved — its server-side delivery footprint changed, which is what the lens's health entry now reports", name)
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
