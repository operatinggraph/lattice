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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"reflect"
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
	"github.com/operatinggraph/lattice/internal/refractor/lens"
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
	// operator-visible consequence of the columns beside it and of the lens's
	// INSTALL: exhaustive decides whether narrowing is derivable at all, the
	// label COUNT decides whether the cap keeps it narrow, the relation set
	// decides which of the two narrowed modes it lands in — a lens can keep an
	// unchanged label set and still double its delivery volume by losing a
	// relation type — and the installer decides whether §4.2's conjuncts are
	// satisfied in the first place.
	//
	// That last input is why `{narrow, "<labels>", modeBroad}` is a real and
	// correct row rather than a contradiction, and every one of them is a
	// PERSONAL lens: its cypher derives an exhaustive label set, and it still
	// takes the broad filter because InstallPersonalLens supplies neither
	// pattern-closure nor a sweep plan. A Personal Lens reads the D1 read gate
	// (cap-read.<domain>.<actor>) and the Interest Set outside its compiled
	// pattern, so an event no label names can still change what it projects.
	//
	// "Installed the way production installs it" is load-bearing, and it is what
	// consumerFilterMode below spends its extra lines on. Asking a BARE pipeline
	// what filter an actor-aware lens earns is not a conservative reading of that
	// lens — it is a reading of a lens no deployment runs. The actor-aware
	// conjunction has conjuncts the plain branch never evaluates, so the plain
	// answer can be strictly NARROWER than the truth. ConsumerFilter refuses that
	// question outright
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
	// applicantRosterRead converted 2026-08-22: its sole exhaustiveness
	// blocker was the `containedIn*1..` variable-length hop
	// (typed-relation-signatures-design.md §9.2), rewritten to a fixed
	// single hop now every live wiring is verified unit->building at depth 1.
	"applicantRosterRead":            {narrow, "building identity leaseapp unit", modeLabel},
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
	"capabilityAuthorPackages":       {narrow, "package", modeRelation},
	"capabilityAuthorPending":        {narrow, "capabilityproposal", modeRelation},
	"capabilityProposals":            {narrow, "capabilityproposal", modeRelation},
	"capabilityRead":                 {narrow, "identity", modeRelation},
	"capabilityReadGrants":           {narrow, "identity", modeRelation},
	"capabilityReadWildcardGrants":   {narrow, "identity role", modeRelation},
	"capabilityRoleIndex":            {narrow, "permission role", modeRelation},
	"capabilityRoles":                {narrow, "identity permission role", modeRelation},
	"clinicAppointments":             {narrow, "appointment building patient provider", modeLabel},
	"clinicAppointmentsRead":         {narrow, "appointment building identity patient provider", modeLabel},
	"clinicIdentitiesRead":           {narrow, "identity", modeRelation},
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
	"edgeCatalog#1":                  {narrow, "identity meta permission role service", modeBroad},
	"edgeCatalog#2":                  {narrow, "identity meta service task", modeBroad},
	"edgeEntityBookings":             {narrow, "booking identity instructor session studio", modeBroad},
	"edgeEntitySessions#1":           {narrow, "identity instructor session studio", modeBroad},
	"edgeEntityTabs":                 {narrow, "identity leaseapp tab unit", modeBroad},
	"edgeInstances":                  {narrow, "identity service", modeBroad},
	"edgeManifestProviderReadGrants": {narrow, "appointment identity instructor provider service serviceprovider session", modeLabel},
	"edgeProviderQueue":              {narrow, "identity service serviceprovider", modeBroad},
	"edgeProviderSchedule":           {narrow, "appointment identity provider", modeBroad},
	"edgeStaffPanes":                 {narrow, "identity meta role", modeBroad},
	"followUpReminders":              {narrow, "appointment patient provider", modeRelation},
	"frontDeskBookings":              {narrow, "booking leaseapp session", modeRelation},
	"frontDeskLeaseDetails":          {narrow, "leaseapp unit", modeRelation},
	"frontDeskVisits":                {narrow, "appointment leaseapp", modeRelation},
	"identityCredentialBindingsRead": {narrow, "identity", modeRelation},
	"identityCredentialsRead":        {narrow, "identity", modeRelation},
	"identityIndexHint":              {narrow, "identityindex", modeRelation},
	// The landlord* pair converted alongside applicantRosterRead (same fire,
	// same `containedIn*1..` -> single-hop rewrite, both verified live).
	"landlordLeaseApplicationsRead": {narrow, "building identity leaseapp service unit", modeLabel},
	"landlordUnitsRead":             {narrow, "building identity unit", modeRelation},
	"leaseAccounts":                 {narrow, "account leaseapp", modeRelation},
	"leaseApplicationsRead":         {narrow, "augurproposal identity leaseapp object service unit", modeLabel},
	"leaseExpiry":                   {narrow, "identity leaseapp renewal unit", modeLabel},
	"leaseRentSettlement":           {narrow, "clause leaseapp", modeRelation},
	"ledgerHistory":                 {narrow, "account clause leaseapp transaction", modeLabel},
	"oneBillCafeEntries":            {narrow, "cafeaccount cafetransaction leaseapp", modeRelation},
	"oneBillClinicEntries":          {narrow, "clinicaccount clinictransaction identity leaseapp patient", modeLabel},
	"oneBillRentEntries":            {narrow, "account leaseapp transaction", modeRelation},
	"oneBillWellnessEntries":        {narrow, "identity leaseapp wellnessaccount wellnesstransaction", modeLabel},
	"opCatalog":                     {narrow, "meta permission role", modeRelation},
	"pastDueAppointments":           {narrow, "appointment patient provider", modeRelation},
	"pastDueBookings":               {narrow, "booking identity session", modeRelation},
	"patientIdentityReadGrants":     {narrow, "identity patient", modeRelation},
	"piiKeyEnvelope":                {narrow, "identity", modeRelation},
	"providerAppointmentsRead":      {narrow, "appointment building identity patient provider", modeLabel},
	"providerIdentityReadGrants":    {narrow, "identity provider role", modeRelation},
	"providerSites":                 {narrow, "building provider", modeRelation},
	"renewalComplete":               {narrow, "identity leaseapp renewal service unit", modeLabel},
	"renewalsRead":                  {narrow, "identity leaseapp renewal unit", modeLabel},
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
	"staleAssignedTasks":                {narrow, "identity task", modeRelation},
	"unroutedTasks":                     {narrow, "role task", modeRelation},
	"visitSeriesDue":                    {narrow, "patient provider visitseries", modeRelation},
	"visitSeriesRead":                   {narrow, "building identity patient provider visitseries", modeLabel},
	"wellnessBookingReminders":          {narrow, "booking identity session", modeRelation},
	"wellnessBookings":                  {narrow, "booking identity session studio", modeLabel},
	"wellnessClassPriceSettlement":      {narrow, "booking identity session wellnessaccount wellnesstransaction", modeLabel},
	"wellnessInstructors":               {narrow, "instructor studio", modeRelation},
	"wellnessLedgerHistory":             {narrow, "booking identity session wellnessaccount wellnesstransaction", modeLabel},
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
	"leaseApplicationComplete":      {broad, "identity leaseapp object service task unit", modeBroad},
	"menuCatalog":                   {broad, "menuitem", modeBroad},
	"myTasks":                       {broad, "identity role task", modeBroad},
	"objectAttachments":             {broad, "object", modeBroad},
	"objectIdentityAttachmentsRead": {narrow, "identity object", modeLabel},
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
// It installs through the PRODUCTION installers — projection.InstallActorAggregate
// and projection.InstallPersonalLens, dispatched by projection.IsActorAggregate /
// projection.IsPersonalLens, which is cmd/refractor's own switch (main.go). That
// is load-bearing rather than tidy. The two installers supply DIFFERENT §4.2
// conjuncts: the actor-aggregate one declares pattern-closure and enrols a sweep
// plan, the personal one supplies neither, because a Personal Lens consults the
// D1 read gate and the Interest Set outside its compiled pattern. A census that
// hand-rolled one install for both shapes would report every Personal lens
// narrowed while production runs it broad — and would go on reporting it narrowed
// if a later change made that narrowing real, which is the direction no revert
// recovers.
//
// A refused install is a lens that does not RUN, so it is a failure here rather
// than a mode: cmd/refractor registers no consumer for it at all, and pinning
// whatever filter a half-installed pipeline would derive is exactly the fiction
// the install-completeness guard exists to refuse.
func consumerFilterMode(t *testing.T, name string, eng *full.Engine, cr ruleengine.CompiledRule, rule *lens.Rule) string {
	t.Helper()
	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoErrorf(t, err, "%s census adapter", name)
	p, err := pipeline.New(rule.ID, "nats_kv", bootstrap.CoreKVBucket, nil, nil, adpt, nil)
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

	// cmd/refractor's install order and its install SWITCH, both taken from the
	// production sources rather than restated: UseFullEngine, then whichever
	// installer the lens definition selects, then ConsumerFilter.
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	switch {
	case projection.IsActorAggregate(rule):
		require.Truef(t, projection.InstallActorAggregate(
			p, adpt, rule, func(string) uint64 { return 0 }, nil, nil, logger),
			"%s is declared actorAggregate but its own installer refuses it — the lens would never register", name)
	case projection.IsPersonalLens(rule):
		// requireReadGate is false because no census can thread a live
		// Capability KV handle; it gates registration on the D1 read gate and
		// nothing that ConsumerFilter reads. Every conjunct that DOES decide the
		// filter comes from the installer itself.
		require.Truef(t, projection.InstallPersonalLens(
			p, rule, nil, nil, nil, nil, false, logger),
			"%s is declared Personal but its own installer refuses it — the lens would never register", name)
	}
	_, _, dec := p.ConsumerFilter()
	return dec.Mode
}

// corpusLensRule builds the *lens.Rule the production installers take, from the
// package's own declared LensSpec.
//
// It is the ONE hop this census cannot borrow from production. pkgmgr serializes
// a LensSpec into the lens meta-vertex's `spec` aspect (pkgmgr's lensSpecBody)
// and Refractor's CoreKVSource parses it back (lens.translateSpec); both are
// unexported, and neither is reachable without a live Core KV. So the fields the
// install switch and the installers actually read are set here, and the residual
// gap is asserted directly in TestCorpusLensRule_MatchesTheInstallSwitch rather
// than argued.
//
// The OUTPUT DESCRIPTOR is not copied field by field: it is carried across by the
// same JSON the real transport uses, so a field pkgmgr declares and Refractor
// does not read (or a tag that drifts between them) fails here instead of
// silently dropping a descriptor field the installer would have acted on.
func corpusLensRule(t *testing.T, name string, l pkgmgr.LensSpec) *lens.Rule {
	t.Helper()
	// The authored adapter name to the runtime target type, the mapping
	// pkgmgr.lensSpecBody performs on its way into the lens meta-vertex. An empty
	// adapter is nats-kv, which is the same default that switch's bare arm takes.
	var target string
	switch l.Adapter {
	case "postgres":
		target = "postgres"
	case "nats-subject":
		target = "nats_subject"
	default:
		target = "nats_kv"
	}
	keyFields := l.IntoKey
	if len(keyFields) == 0 {
		keyFields = []string{"key"}
	}
	return &lens.Rule{
		ID:             "census-" + name,
		CanonicalName:  l.CanonicalName,
		ProjectionKind: l.ProjectionKind,
		ResolvedEngine: ruleengine.EngineFull,
		Into: lens.IntoConfig{
			Target:   target,
			Bucket:   l.Bucket,
			Key:      lens.KeyField(keyFields),
			Personal: l.Personal,
		},
		Output: descriptorAcrossTheWire(t, name, l.Output),
	}
}

// descriptorAcrossTheWire re-reads an authored output descriptor as Refractor
// reads it: through the JSON both sides carry it in. nil in, nil out — a lens
// with no descriptor declares none.
//
// The round-trip is asserted LOSSLESS in the field-set sense: re-marshalling the
// Refractor-side struct must reproduce the authored JSON. A descriptor field
// pkgmgr emits that lens.OutputDescriptorSpec has no tag for would otherwise
// vanish here, and the installer would then act on a descriptor the package did
// not write.
func descriptorAcrossTheWire(t *testing.T, name string, authored any) *lens.OutputDescriptorSpec {
	t.Helper()
	if authored == nil || reflect.ValueOf(authored).IsNil() {
		return nil
	}
	raw, err := json.Marshal(authored)
	require.NoErrorf(t, err, "%s output descriptor must serialize", name)
	var desc lens.OutputDescriptorSpec
	require.NoErrorf(t, json.Unmarshal(raw, &desc), "%s output descriptor must parse as Refractor reads it", name)
	back, err := json.Marshal(&desc)
	require.NoErrorf(t, err, "%s output descriptor must re-serialize", name)
	var authoredFields, parsedFields map[string]any
	require.NoError(t, json.Unmarshal(raw, &authoredFields))
	require.NoError(t, json.Unmarshal(back, &parsedFields))
	for k, v := range authoredFields {
		require.Containsf(t, parsedFields, k,
			"%s declares output descriptor field %q that Refractor's own struct drops — the installer would act on a descriptor the package did not write", name, k)
		require.Equalf(t, v, parsedFields[k], "%s output descriptor field %q changed value across the wire", name, k)
	}
	return &desc
}

// TestCorpusLensRule_MatchesTheInstallSwitch closes the one hop corpusLensRule
// has to hand-build. The census is only as honest as its answer to "which
// installer does this lens get", so that answer is asserted against the two
// production predicates for every executable cypher the corpus ships, in both
// directions — a rule that satisfies neither predicate must be a lens the
// declaration really does leave plain.
//
// It is a test rather than an assertion inside the label census for the reason
// TestCorpusLenses_DeclaredAnchorMatchesInstalledKind is: the census would
// ABSORB a disagreement, reporting a mode for an install no deployment performs.
func TestCorpusLensRule_MatchesTheInstallSwitch(t *testing.T) {
	checked := 0
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, declaredActorAggregate, declaredPersonal bool) {
		require.Equalf(t, declaredActorAggregate, projection.IsActorAggregate(rule),
			"%s: the declaration says actorAggregate=%v, but the rule the census installs from reads %v",
			name, declaredActorAggregate, projection.IsActorAggregate(rule))
		require.Equalf(t, declaredPersonal, projection.IsPersonalLens(rule),
			"%s: the declaration says Personal=%v, but the rule the census installs from reads %v — a Personal lens read as plain gets pattern-closure and a sweep plan it never has in production",
			name, declaredPersonal, projection.IsPersonalLens(rule))
		require.Falsef(t, declaredActorAggregate && declaredPersonal,
			"%s declares both projectionKind=actorAggregate and Personal — cmd/refractor's switch would silently pick the first arm", name)
		checked++
	})
	require.Greaterf(t, checked, 100,
		"the corpus enumeration collapsed to %d cyphers — this invariant is only worth what it covers", checked)
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

	derive := func(name, spec string, rule *lens.Rule, _, _ bool) {
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
		rule.CompiledRule = cr
		got[name] = labelVerdict{exhaustive, strings.Join(sorted, " "), consumerFilterMode(t, name, eng, cr, rule)}
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
// rule is the *lens.Rule cmd/refractor's install switch dispatches on, built
// from the lens DEFINITION and off nothing else — notably NOT off the cypher. A
// census that decided the install shape from the cypher would be deciding by a
// different rule than the deployment it claims to model, and would absorb the
// exact disagreement between the two that
// TestCorpusLenses_DeclaredAnchorMatchesInstalledKind exists to catch.
//
// declaredActorAggregate and declaredPersonal are the same two answers read
// straight off the declaration, handed over beside the rule so
// TestCorpusLensRule_MatchesTheInstallSwitch can hold the rule to them. They are
// mutually exclusive by that test's assertion, not by construction — the
// declaration surface permits both flags and cmd/refractor's switch would
// silently take the first arm.
func forEachCorpusCypher(t *testing.T, visit func(name, spec string, rule *lens.Rule, declaredActorAggregate, declaredPersonal bool)) {
	t.Helper()
	addLens := func(l pkgmgr.LensSpec) {
		aggregate := l.ProjectionKind == projection.ActorAggregateKind
		if len(l.SpecBranches) > 0 {
			for i, b := range l.SpecBranches {
				branchName := fmt.Sprintf("%s#%d", l.CanonicalName, i)
				visit(branchName, b, corpusLensRule(t, branchName, l), aggregate, l.Personal)
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
		visit(l.CanonicalName, l.Spec, corpusLensRule(t, l.CanonicalName, l), aggregate, l.Personal)
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
		// on it identically. It reaches the same builder through the same
		// LensSpec shape rather than a second one.
		spec := pkgmgr.LensSpec{
			CanonicalName:  l.CanonicalName,
			Adapter:        l.Adapter,
			Bucket:         l.TargetBucket,
			ProjectionKind: l.ProjectionKind,
			Output:         bootstrapDescriptorAsPkgmgr(t, l),
		}
		visit(l.CanonicalName, l.CypherRule, corpusLensRule(t, l.CanonicalName, spec),
			l.ProjectionKind == projection.ActorAggregateKind, false)
	}
}

// bootstrapDescriptorAsPkgmgr re-reads a kernel lens's output descriptor as a
// package one, through the same JSON both declaration surfaces serialize into
// the lens meta-vertex. The kernel and the packages declare descriptors in two
// structs precisely because neither package may import the other; the wire shape
// is what they share, so it is what this census crosses on.
func bootstrapDescriptorAsPkgmgr(t *testing.T, l bootstrap.LensDefinition) *pkgmgr.OutputDescriptorSpec {
	t.Helper()
	if l.Output == nil {
		return nil
	}
	raw, err := json.Marshal(l.Output)
	require.NoErrorf(t, err, "%s kernel output descriptor must serialize", l.CanonicalName)
	var out pkgmgr.OutputDescriptorSpec
	require.NoErrorf(t, json.Unmarshal(raw, &out),
		"%s kernel output descriptor must parse into the package descriptor shape", l.CanonicalName)
	return &out
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
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, declaredActorAggregate, declaredPersonal bool) {
		actorAware := declaredActorAggregate || declaredPersonal
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

// TestPersonalLenses_NeverAcquireTheNarrowedFilter states at corpus level the
// consequence the pinned table above only carries as data: every Personal lens
// the corpus ships takes the BROAD filter, and it does so because its own
// installer withholds §4.2's conjuncts.
//
// It is worth its own name because of the direction the mistake runs. A Personal
// Lens's row depends on the D1 read gate (cap-read.<domain>.<actor>) and the
// Interest Set, neither of which its compiled pattern binds — so a role event
// that widens an actor's grants is exactly the event a narrowed filter would
// withhold, and withholding it is a grant that never appears or never retracts.
// Threading a sweep plan through InstallPersonalLens is a plausible convergence
// change that would hand every one of these lenses a narrowed filter, relation
// segment included, as a side effect nobody asked for; no filter update rewinds
// a JetStream cursor, so it is not a change a revert undoes.
//
// The pattern-closure seam itself is pinned on a fixture in
// projection.TestPatternClosure_OnlyActorAggregateAssertsIt. This is the corpus
// half: that the seam holds for every lens actually installed.
func TestPersonalLenses_NeverAcquireTheNarrowedFilter(t *testing.T) {
	eng := full.New()
	checked := 0
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, _, declaredPersonal bool) {
		if !declaredPersonal {
			return
		}
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		rule.CompiledRule = cr
		require.Equalf(t, health.FilterModeBroad, consumerFilterMode(t, name, eng, cr, rule),
			"%s is a Personal lens: its row depends on the D1 read gate and the Interest Set, both outside its compiled pattern, so no narrowed filter may ever be derived for it. "+
				"If this failed because InstallPersonalLens now supplies a sweep plan or pattern-closure, that install change is the thing to review", name)
		checked++
	})
	require.Positivef(t, checked, "no Personal lens was reached — this invariant is only worth what it covers")
}

// TestCorpusInstallers_OnlyReachAdaptersTheCensusModels states the census's
// remaining limit rather than quietly exceeding it: it builds ONE adapter shape
// (the NATS-KV one) for every lens, so its verdicts are only honest while every
// lens whose installer consults an adapter declares that target.
//
// The install decisions that read the adapter are both capability type
// assertions, and both fall the generous way here:
//
//   - sweepEnrolment refuses a lens whose target cannot enumerate keys under a
//     prefix (adapter.PrefixKeyLister). A refusal means NO sweep plan, which
//     fails §4.2's healer conjunct and puts the lens on the broad filter —
//     while this census, holding a NATS-KV adapter, would report it narrowed.
//   - a perEntry descriptor (entryKeyColumn) additionally requires
//     adapter.RowReader, and InstallActorAggregate REFUSES registration without
//     it, so such a lens would not run at all.
//
// Today every actor-aggregate lens in the corpus declares nats-kv, so the shape
// the census builds is the shape production builds. The Postgres adapters
// (PostgresAdapter, GrantWriterAdapter, ProtectedAdapter) implement neither
// optional interface, so a Postgres actor-aggregate lens would take the broad
// filter in production and a perEntry one would be refused outright — which is
// why this is asserted rather than left as a note. The corpus's Postgres lenses
// are all PLAIN, and §4.2 never consults an adapter for a plain lens.
//
// A Personal lens needs no arm here: InstallPersonalLens takes no adapter at
// all, so its verdict cannot depend on one.
//
// The fix when this fails is to give the census the adapter the lens declares,
// not to widen this assertion. A capability-shaped double would be a second
// place that claims to know which optional interfaces each production adapter
// satisfies, and that claim going stale is this whole class of defect.
func TestCorpusInstallers_OnlyReachAdaptersTheCensusModels(t *testing.T) {
	checked := 0
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, _, _ bool) {
		if !projection.IsActorAggregate(rule) {
			return
		}
		require.Equalf(t, "nats_kv", rule.Into.Target,
			"%s is an actor-aggregate lens declaring target %q, but this census builds a NATS-KV adapter for every lens. "+
				"sweepEnrolment and the perEntry install both branch on adapter capability, so the verdict pinned for this lens is not the one production derives — give the census this lens's real adapter",
			name, rule.Into.Target)
		checked++
	})
	require.Positivef(t, checked, "no actor-aggregate lens was reached — this limit is only worth what it covers")
}
