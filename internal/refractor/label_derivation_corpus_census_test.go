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
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
)

type labelVerdict struct {
	exhaustive bool
	labels     string // space-separated, sorted
	// mode is the Core KV consumer filter this cypher earns on a pipeline
	// installed the way production installs it —
	// health.FilterMode{NarrowedRelation,NarrowedLabel,Broad}, taken from the
	// real pipeline.ConsumerFilter, not recomputed here
	// (dynamic-type-taxonomy-design.md §10.3's footprint vocabulary). It is the
	// operator-visible consequence of the two columns beside it: exhaustive
	// decides narrow-vs-broad, the label COUNT decides whether the cap keeps it
	// narrow, and the relation set decides which of the two narrowed modes it
	// lands in — a lens can keep an unchanged label set and still double its
	// delivery volume by losing a relation type.
	//
	// "Installed the way production installs it" is load-bearing, and it is what
	// consumerFilterMode below spends its extra lines on. Asking a BARE pipeline
	// what filter an actor-aware lens earns is not a conservative reading of that
	// lens — it is a reading of a lens no deployment runs. The actor-aware
	// conjunction has conjuncts the plain branch never evaluates, and the
	// relation dimension is gated OFF for it, so the plain answer can be strictly
	// NARROWER than the truth. ConsumerFilter refuses that question outright
	// (health.FilterBroadReasonInstallIncomplete), which is why the census
	// supplies the install rather than pinning a fiction.
	//
	// ONE LIMIT ON WHAT THIS PIN CLAIMS, structural to a census that reads
	// compiled cypher rather than a running Refractor: it is PER EXECUTABLE
	// CYPHER, like every other column here. A multi-walk lens's real filter
	// comes from the UNION over its branches, so its `name#N` rows pin each
	// branch's own mode, not the lens's.
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
	"appointmentReminders":           {narrow, "appointment patient provider", modeLabel},
	"augurDispatchPending":           {narrow, "augurproposal", modeLabel},
	"augurProposals":                 {narrow, "augurproposal", modeRelation},
	"availableListings":              {narrow, "unit", modeRelation},
	"cafeLeaseAccounts":              {narrow, "cafeaccount leaseapp", modeRelation},
	"cafeLedgerHistory":              {narrow, "cafeaccount cafetransaction leaseapp", modeRelation},
	"cafeStaleTabSettlement":         {narrow, "tab", modeLabel},
	"cafeTabSettlement":              {narrow, "cafetransaction leaseapp tab", modeLabel},
	"capability":                     {narrow, "identity role", modeLabel},
	"capabilityAuthorContext":        {narrow, "meta", modeRelation},
	"capabilityAuthorPending":        {narrow, "capabilityproposal", modeLabel},
	"capabilityProposals":            {narrow, "capabilityproposal", modeRelation},
	"capabilityRead":                 {narrow, "identity", modeLabel},
	"capabilityReadGrants":           {narrow, "identity", modeRelation},
	"capabilityReadWildcardGrants":   {narrow, "identity role", modeRelation},
	"capabilityRoleIndex":            {narrow, "permission role", modeRelation},
	"capabilityRoles":                {narrow, "identity permission role", modeLabel},
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
	"clinicSiteBackfill":             {narrow, "appointment building", modeLabel},
	"clinicSites":                    {narrow, "building", modeRelation},
	"consoleOperatorReadGrants":      {narrow, "identity role", modeRelation},
	"demoOperatorReadGrants":         {narrow, "identity role", modeRelation},
	"duplicateCandidates":            {narrow, "identity", modeRelation},
	"edgeCatalog#1":                  {narrow, "identity meta permission role service", modeLabel},
	"edgeEntityBookings":             {narrow, "booking identity instructor session studio", modeLabel},
	"edgeEntitySessions#1":           {narrow, "identity instructor session studio", modeLabel},
	"edgeEntityTabs":                 {narrow, "identity leaseapp tab unit", modeLabel},
	"edgeInstances":                  {narrow, "identity service", modeLabel},
	"edgeManifestProviderReadGrants": {narrow, "appointment identity instructor provider service serviceprovider session", modeLabel},
	"edgeProviderQueue":              {narrow, "identity service serviceprovider", modeLabel},
	"edgeProviderSchedule":           {narrow, "appointment identity provider", modeLabel},
	"edgeStaffPanes":                 {narrow, "identity meta role", modeLabel},
	"followUpReminders":              {narrow, "appointment patient provider", modeLabel},
	"frontDeskBookings":              {narrow, "booking leaseapp session", modeRelation},
	"frontDeskLeaseDetails":          {narrow, "leaseapp unit", modeRelation},
	"frontDeskVisits":                {narrow, "appointment leaseapp", modeRelation},
	"identityCredentialBindingsRead": {narrow, "identity", modeRelation},
	"identityCredentialsRead":        {narrow, "identity", modeRelation},
	"identityIndexHint":              {narrow, "identityindex", modeRelation},
	"leaseAccounts":                  {narrow, "account leaseapp", modeRelation},
	"leaseApplicationsRead":          {narrow, "augurproposal identity leaseapp object service unit", modeLabel},
	"leaseExpiry":                    {narrow, "identity leaseapp renewal unit", modeLabel},
	"leaseRentSettlement":            {narrow, "clause leaseapp", modeLabel},
	"ledgerHistory":                  {narrow, "account clause leaseapp transaction", modeLabel},
	"oneBillCafeEntries":             {narrow, "cafeaccount cafetransaction leaseapp", modeRelation},
	"oneBillClinicEntries":           {narrow, "clinicaccount clinictransaction identity leaseapp patient", modeLabel},
	"oneBillRentEntries":             {narrow, "account leaseapp transaction", modeRelation},
	"oneBillWellnessEntries":         {narrow, "identity leaseapp wellnessaccount wellnesstransaction", modeLabel},
	"pastDueAppointments":            {narrow, "appointment patient provider", modeLabel},
	"pastDueBookings":                {narrow, "booking identity session", modeLabel},
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
	"unroutedTasks":                     {narrow, "role task", modeLabel},
	"visitSeriesDue":                    {narrow, "patient provider visitseries", modeLabel},
	"visitSeriesRead":                   {narrow, "building identity patient provider visitseries", modeLabel},
	"wellnessBookingReminders":          {narrow, "booking identity session", modeLabel},
	"wellnessBookings":                  {narrow, "booking identity session studio", modeLabel},
	"wellnessClassPriceSettlement":      {narrow, "booking identity session wellnessaccount wellnesstransaction", modeLabel},
	"wellnessInstructors":               {narrow, "instructor studio", modeRelation},
	"wellnessLedgerHistory":             {narrow, "identity wellnessaccount wellnesstransaction", modeRelation},
	"wellnessMemberAccounts":            {narrow, "booking identity wellnessaccount", modeRelation},
	"wellnessNoShowSettlement":          {narrow, "booking identity wellnessaccount wellnesstransaction", modeLabel},
	"wellnessOrphanedBookingSettlement": {narrow, "booking session", modeLabel},
	"wellnessRefundSettlement":          {narrow, "wellnessrefund wellnesstransaction", modeLabel},
	"wellnessStudios":                   {narrow, "studio", modeRelation},

	// Broad: something in the cypher binds a type no label names — an unlabeled
	// node, a variable-length hop, or a name re-seeded after the clause that
	// labeled it went out of scope. These reproject on every event and their
	// consumers take the unconditional fan-out. The label set is still pinned:
	// widening it is safe, but a label DROPPING out is how a broad lens quietly
	// stops being judged against a type it reads.
	"applicantRosterRead": {broad, "building identity leaseapp unit", modeBroad},
	"cafeIdentitiesRead":  {broad, "identity leaseapp", modeBroad},
	"cafeLeaseWorkplaces": {broad, "leaseapp", modeBroad},
	"capabilityEphemeral": {broad, "identity role task", modeBroad},
	// `location` is the ABSTRACT label the lens carries with the `*` sigil; the
	// column is the cypher's own referenced-label set, pre-expansion. The
	// verdict stays BROAD, and not because of the label count: two
	// `containedIn*0..` hops and the unlabeled `op` inside the pattern
	// comprehension each clear exhaustiveness on their own, independent of
	// every label (ruleengine/full/labels.go). Labelling the three location
	// positions constrains the BINDER, not the delivery footprint.
	"capabilityServiceAccess":     {broad, "identity location service", modeBroad},
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
// actorAware comes from the LENS DEFINITION (forEachCorpusCypher's doc), which
// is the only source cmd/refractor's install switch consults. Deciding it here
// from the cypher instead would make the census install components a deployment
// would not, and the mode it pinned would then be a mode no deployment can
// produce.
func consumerFilterMode(t *testing.T, name string, eng *full.Engine, cr ruleengine.CompiledRule, actorAware bool) string {
	t.Helper()
	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoErrorf(t, err, "%s census adapter", name)
	p, err := pipeline.New("census-"+name, "nats_kv", bootstrap.CoreKVBucket, nil, nil, adpt, nil)
	require.NoErrorf(t, err, "%s census pipeline", name)
	// The corpus DOES carry taxonomy sigils now (capabilityServiceAccess's
	// `:location*`), and a `*`-carrying cypher refuses activation outright when
	// the expansion is unknown (§4.2's third tier). So the census installs a
	// resolver armed with the corpus's OWN declared taxonomy — derived from the
	// same pkgregistry DDL specs the installer emits subtypeOf links from, never
	// a hand-written leaf list. A lens's verdict is therefore a function of the
	// declared taxonomy, which is exactly what a taxonomy-driven expansion makes
	// it in production: declare a fourth location leaf and this census moves.
	p.SetTaxonomyResolver(corpusTaxonomyResolver(t))
	require.NoErrorf(t, p.UseFullEngine(eng, cr), "%s must activate", name)
	// The install stages an actor-aware lens gets, in cmd/refractor's own order
	// (UseFullEngine → InstallActorAggregate → ConsumerFilter). The three
	// components arrive together in production and so they do here; supplying
	// only some of them would pin a footprint no deployment can produce.
	//
	// The anchor TYPE is the one input taken off the pattern, because
	// pkgmgr.LensSpec's descriptor and the cypher must agree on it anyway: the
	// descriptor's anchor is the vertex the pattern pins with `{key: $actorKey}`,
	// so that position's own label IS the anchor type. An actor-aware lens whose
	// pattern pins nothing has no anchor position to read, and the census fails
	// it in TestCorpusLenses_DeclaredAnchorMatchesInstalledKind rather than
	// guessing one here.
	if fullCR, isFull := cr.(*full.CompiledRule); isFull && actorAware {
		ix := fullCR.AnchorHopIndex()
		require.GreaterOrEqualf(t, ix.Anchor, 0,
			"%s is installed actor-aware but its pattern pins no $actorKey position", name)
		anchorType := ix.Labels[ix.Anchor]
		p.SetActorEnumerator(pipeline.NewActorEnumerator(nil, nil, anchorType))
		p.SetPatternClosedOutput(true)
		p.SetSweepPlan(pipeline.SweepPlan{AnchorType: anchorType, KeyPrefix: "census-" + name + "."})
	}
	_, _, dec := p.ConsumerFilter()
	return dec.Mode
}

// corpusTaxonomyResolver builds an armed taxonomy resolver from every
// vertexType DDL the installed corpus declares — its Abstract flag and its
// SubtypeOfRef parent, the two fields the installer turns into the
// `data.abstract` marker and the `lnk.meta.<leaf>.subtypeOf.meta.<parent>`
// edge (dynamic-type-taxonomy-design.md §3.2/§3.3). Reading the declarations
// rather than restating their consequences is what keeps this census honest:
// a package that adds, removes, or re-parents a leaf moves the verdicts here
// with no edit to this file.
func corpusTaxonomyResolver(t *testing.T) *taxonomy.Resolver {
	t.Helper()
	var snap []taxonomy.TypeSnapshot
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		require.Truef(t, ok, "registered package %q must resolve", name)
		for _, d := range def.DDLs {
			class := d.Class
			if class == "" {
				class = "meta.ddl.vertexType"
			}
			if class != "meta.ddl.vertexType" {
				continue
			}
			ts := taxonomy.TypeSnapshot{
				ID:            pkgmgr.DDLID(name, d.CanonicalName),
				CanonicalName: d.CanonicalName,
				Abstract:      d.Abstract,
			}
			if d.SubtypeOfRef != "" {
				ts.SubtypeOf = []string{d.SubtypeOfRef}
			}
			snap = append(snap, ts)
		}
	}
	r := taxonomy.New()
	r.InstallSnapshot(snap)
	r.SetArmed(true)
	return r
}

// corpusLabelDerivation derives every installed cypher's verdict the way
// activation does, walk-expanded and branch-inclusive.
func corpusLabelDerivation(t *testing.T) map[string]labelVerdict {
	t.Helper()
	eng := full.New()
	got := make(map[string]labelVerdict)

	derive := func(name, spec string, actorAware bool) {
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
		got[name] = labelVerdict{exhaustive, strings.Join(sorted, " "), consumerFilterMode(t, name, eng, cr, actorAware)}
	}
	forEachCorpusCypher(t, derive)
	return got
}

// forEachCorpusCypher calls visit once per EXECUTABLE cypher the installed
// corpus ships, under the canonical name every census in this package keys its
// pins by (`name#N` for one branch of a multi-walk lens, in Walks declaration
// order).
//
// It is the enumeration every corpus census in this package has to agree on —
// read-grant walks expanded first, SpecBranches enumerated rather than Spec
// alone — for the two reasons this file's header gives. A second census that
// swept the registry its own way would quietly cover a different corpus and
// pin a different thing.
//
// actorAware is the lens DEFINITION's answer to "does activation install the
// cross-vertex fan-out for this lens", read off the same two aspects
// cmd/refractor's install switch reads (projection.IsActorAggregate /
// projection.IsPersonalLens, main.go) and off nothing else — notably NOT off
// the cypher. A census that decided it from the cypher would be deciding by a
// different rule than the deployment it claims to model, and would absorb the
// exact disagreement between the two that
// TestCorpusLenses_DeclaredAnchorMatchesInstalledKind exists to catch.
func forEachCorpusCypher(t *testing.T, visit func(name, spec string, actorAware bool)) {
	t.Helper()
	addLens := func(l pkgmgr.LensSpec) {
		actorAware := l.ProjectionKind == projection.ActorAggregateKind || l.Personal
		if len(l.SpecBranches) > 0 {
			for i, b := range l.SpecBranches {
				visit(fmt.Sprintf("%s#%d", l.CanonicalName, i), b, actorAware)
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
		visit(l.CanonicalName, l.Spec, actorAware)
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
		// bootstrap.LensDefinition carries projectionKind and no Personal flag —
		// the kernel ships no Personal lens, and the seeder writes the same
		// projectionKind aspect a package lens declares, so activation switches
		// on it identically.
		visit(l.CanonicalName, l.CypherRule, l.ProjectionKind == projection.ActorAggregateKind)
	}
}

// TestCorpusLenses_DeclaredAnchorMatchesInstalledKind pins the bi-conditional
// two independent mechanisms rest on, across every executable cypher the corpus
// ships: a lens's CYPHER pins `{key: $actorKey}` if and only if its DEFINITION
// makes activation install the cross-vertex fan-out.
//
// The two mechanisms read different sources and can only agree by this
// invariant holding. cmd/refractor installs off the definition
// (projection.IsActorAggregate / IsPersonalLens); pipeline.ConsumerFilter's
// install-completeness guard reads the cypher (declaresActorAnchor). A lens
// that satisfies one and not the other is the defect, in both directions:
//
//   - Cypher anchored, definition plain. Activation installs no enumerator, the
//     guard sees a declared anchor with none installed, and the lens takes the
//     BROAD filter for the life of the process — one log line and no other
//     signal. The head is easy to write by accident: `MATCH (t:tab {key:
//     $actorKey})` is what cafeStaleTabSettlement already opens with, so a new
//     nats-kv lens copied from it and missing `ProjectionKind` lands here.
//   - Definition actor-aware, cypher unanchored. The enumerator is installed and
//     the guard stays silent, but nothing pins an anchor position, so the
//     affected-anchor derivation can never index the lens and every event falls
//     back to the BFS — correct, permanently un-narrowed, and invisible.
//
// It is a test of its own rather than an assertion inside the label census
// because the census would otherwise ABSORB the disagreement: it would install
// components production does not, and report a narrowed mode for a lens that is
// broad on the wire.
func TestCorpusLenses_DeclaredAnchorMatchesInstalledKind(t *testing.T) {
	eng := full.New()
	checked := 0
	forEachCorpusCypher(t, func(name, spec string, actorAware bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		require.Equalf(t, actorAware, fullCR.DeclaresActorAnchor(),
			"%s: its lens definition says activation installs the actor fan-out = %v, but its cypher pins {key: $actorKey} = %v. "+
				"Anchored-but-plain takes the broad filter forever (the install-completeness guard refuses to narrow); "+
				"actor-aware-but-unanchored can never index its affected anchors. Fix the lens, not this assertion",
			name, actorAware, fullCR.DeclaresActorAnchor())
		checked++
	})
	require.Greaterf(t, checked, 100,
		"the corpus enumeration collapsed to %d cyphers — this invariant is only worth what it covers", checked)
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
