// Plain-lens retraction-transport corpus census —
// secure-plain-lens-retraction-and-audit-design.md §4.4, §10.
//
// plain_scanroot_corpus_census_test.go pins, per plain lens, whether the
// scan-root pattern graph can answer "which anchors can a neighbour event
// affect" and whether the rows close per anchor. This file pins what that
// answer is FOR: whether the lens's rows can be dropped by a neighbour at all,
// and — when they can — what carries the retraction to the target.
//
// The population is the scan-root pin's, read off scanRootCorpusVerdicts rather
// than hand-counted: two censuses over one corpus that enumerate it differently
// are two censuses about two deployments.
//
// THE ASSERTION THAT MATTERS: no BUSINESS-plane lens whose row existence
// depends on a required neighbour carries transport `none`. That is the
// invariant the activation gate enforces at runtime
// (cmd/refractor/main.go's retraction-transport guard), and this pin is where an
// author meets it — a runtime refusal is the backstop, not the place to learn.
//
// THE AUTH PLANE IS OUT OF THE GATE'S SCOPE, NOT OUT OF THIS CENSUS. The
// derivation licence refuses that plane outright (an authorization surface is
// narrowed only behind a repair-capable healer), so its members are pinned here
// by name with their transport story, as named debt on a plane that has its own
// severity ladder — never as an absence a reader could mistake for coverage.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: read the column that moved. A
// `dependsOnNeighbour` turning true is a lens that just acquired a retraction
// obligation — it needs a transport before it can land. A `transport` moving to
// none is a lens LOSING one, which is the direction that orphans rows. A
// transport moving from none to something is the direction that only needs the
// new transport to be the true one.
//
// DERIVATION COMMAND (re-run this, do not trust a number in a build note):
//
//	go test ./internal/refractor/ -run TestPlainRetractionTransportCensus -count=1 -v
package refractor_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// The planes a lens's rows can sit on, as this census names them.
const (
	planeBusiness = "business"
	planeAuth     = "auth"
)

// retractionVerdict is one plain lens's pinned classification.
type retractionVerdict struct {
	// plane is planeBusiness or planeAuth — projection.IsAuthPlane's answer,
	// the one canonical derivation both the gate and the audit's enrolment
	// read.
	plane string
	// dependsOnNeighbour is full.CompiledRule.ExistenceDependsOnNeighbour's
	// answer: can a NEIGHBOUR event drop this lens's row.
	dependsOnNeighbour bool
	// transport is one of the pipeline.RetractionTransport* constants —
	// "" (none), "derivation", "diffRetraction", "diffRetraction-prefix",
	// "diffRetraction-partition". The audit-disarmed spelling cannot appear
	// here: this census runs with the audit armed, which is the deployment
	// default.
	transport string
}

// plainRetractionCorpusVerdicts pins today's verdict for every plain lens the
// installed corpus ships. Measured live by
// TestPlainRetractionTransportCensus_PinnedVerdicts, whose population is taken
// from scanRootCorpusVerdicts.
//
// The AUTH-PLANE members with no transport are pinned here rather than left to
// be inferred from an absence, and the set is derived from these rows by
// TestPlainRetractionTransportCensus_Partition rather than counted in this
// prose — a hand count is a second enumeration of the same table, and it is the
// half that goes stale.
//
// The derivation licence refuses that plane outright, so T1 is unavailable to
// all of them by design. Two carry a neighbour dependency and no target diff,
// which is real debt on the plane that has its own severity ladder for it:
//
//   - capabilityRoleIndex — its projection key is minted Go-side by the
//     operation-role-index envelope wrapper (cmd/refractor's
//     isOperationRoleIndexLens arm) rather than by an Output descriptor, so it
//     has no key prefix to scope a diff to in the capability bucket it shares. A
//     stale entry names a role in a denial message.
//   - capabilityReadWildcardGrants — the kernel's root read-grant producer. The
//     kernel declaration surface carries no DiffRetraction flag at all, so the
//     shape console-operator and demo-operator use below is not expressible for
//     it. What it DOES retract is the unwiring of the operator's holdsRole: the
//     link arm re-evaluates the anchor endpoint and runs the presence probe, and
//     AnchorProjectionKey resolves for it (the scan-root pin has it
//     closureHolds). What has no transport is a `role` VERTEX tombstone, or an
//     edit to the operator role's canonicalName aspect — the aspect arm seeds
//     from the role, which is not the anchor, so no event names the grant row
//     those two drop.
//
// The rest — capabilityReadGrants and the clinic read-grant producers — depend
// on no neighbour (each row is the anchor identity's own self-grant), so they
// need no neighbour transport and their `none` is not debt.
var plainRetractionCorpusVerdicts = map[string]retractionVerdict{
	"applicantRosterRead":            {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"augurProposals":                 {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"availableListings":              {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"cafeIdentitiesRead":             {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"cafeLeaseAccounts":              {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"cafeLeaseWorkplaces":            {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"cafeLedgerHistory":              {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"capabilityAuthorContext":        {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"capabilityAuthorPackages":       {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"capabilityProposals":            {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"capabilityReadGrants":           {plane: planeAuth, dependsOnNeighbour: false, transport: pipeline.RetractionTransportNone},
	"capabilityReadWildcardGrants":   {plane: planeAuth, dependsOnNeighbour: true, transport: pipeline.RetractionTransportNone},
	"capabilityRoleIndex":            {plane: planeAuth, dependsOnNeighbour: true, transport: pipeline.RetractionTransportNone},
	"clinicAppointments":             {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"clinicAppointmentsRead":         {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"clinicEncountersRead":           {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"clinicIdentitiesRead":           {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"clinicLedgerHistory":            {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"clinicPatientAccounts":          {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"clinicPatientReadGrants":        {plane: planeAuth, dependsOnNeighbour: false, transport: pipeline.RetractionTransportNone},
	"clinicPatients":                 {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"clinicPatientsRead":             {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDiffRetraction},
	"clinicProviderReadGrants":       {plane: planeAuth, dependsOnNeighbour: false, transport: pipeline.RetractionTransportNone},
	"clinicProviders":                {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"clinicSites":                    {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"consoleOperatorReadGrants":      {plane: planeAuth, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetraction},
	"demoOperatorReadGrants":         {plane: planeAuth, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetraction},
	"duplicateCandidates":            {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetractionPartition},
	"frontDeskBookingHistory":        {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"frontDeskBookings":              {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"frontDeskLeaseDetails":          {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"frontDeskVisits":                {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"identityCredentialBindingsRead": {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"identityCredentialsRead":        {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"identityIndexHint":              {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"landlordLeaseApplicationsRead":  {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetractionPartition},
	"landlordUnitsRead":              {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetractionPartition},
	"leaseAccounts":                  {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"leaseApplicationsRead":          {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"ledgerHistory":                  {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"menuCatalog":                    {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"objectIdentityAttachmentsRead":  {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetractionPartition},
	"oneBillCafeEntries":             {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"oneBillClinicEntries":           {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"oneBillRentEntries":             {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"oneBillWellnessEntries":         {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"opCatalog":                      {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportNone},
	"patientIdentityReadGrants":      {plane: planeAuth, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetraction},
	"piiKeyEnvelope":                 {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"providerAppointmentsRead":       {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"providerIdentityReadGrants":     {plane: planeAuth, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetraction},
	"providerSites":                  {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetractionPartition},
	"renewalsRead":                   {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"retentionKeyStatus":             {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"shredStatus":                    {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"staffReadGrants":                {plane: planeAuth, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetraction},
	"visitSeriesRead":                {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"wellnessBookers":                {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"wellnessBookings":               {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"wellnessIdentitiesRead":         {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"wellnessInstructors":            {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"wellnessLedgerHistory":          {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"wellnessMemberAccounts":         {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDiffRetraction},
	"wellnessMembers":                {plane: planeBusiness, dependsOnNeighbour: true, transport: pipeline.RetractionTransportDerivation},
	"wellnessSessions":               {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
	"wellnessStudios":                {plane: planeBusiness, dependsOnNeighbour: false, transport: pipeline.RetractionTransportDerivation},
}

// deriveRetractionVerdict is the live classification for one plain lens, run
// through the SHIPPED predicate (pipeline.Pipeline.PlainRetractionTransport)
// rather than a restatement of it — the standing rule for a per-lens analysis:
// a census that re-implemented the predicate would agree with a broken gate.
//
// The pipeline is the census install every sibling census here uses
// (corpusInstalledPipeline), with the two activation steps the transport
// question depends on carried out in cmd/refractor's own order: the key columns
// threaded exactly as deriveScanRootVerdict threads them (without them
// ProjectsOneRowPerAnchor falls back to the first RETURN item and a
// composite-key lens answers about a key it does not have), and SetDiffRetraction
// for a lens that declares it.
//
// The census adapter is a NATS-KV one for every lens, including the Postgres
// ones. That over-approximates exactly two conjuncts — adapter.RowReader and
// adapter.PartitionKeyLister — and neither is a live over-approximation: both
// adapters a lens can activate against (NatsKVAdapter, and PostgresAdapter
// through the ProtectedAdapter wrapper every protected lens activates behind)
// implement both, so no corpus lens's verdict turns on the substitution. The
// adapter that implements NEITHER is the shared grant writer, and its lenses are
// held off this transport by their plane before the adapter is ever asked.
func deriveRetractionVerdict(t *testing.T, eng *full.Engine, name, spec string, rule *lens.Rule) retractionVerdict {
	t.Helper()
	cr, err := eng.Parse(spec)
	require.NoErrorf(t, err, "%s must parse", name)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.Truef(t, isFull, "%s must compile to the full engine", name)

	if cols := closureKeyColumns(fullCR, rule); cols != nil {
		require.NoErrorf(t, projection.ThreadKeyColumns(fullCR, nil, cols), "%s", name)
	} else {
		fullCR.KeyColumns = nil
	}

	p := corpusInstalledPipeline(t, name, eng, cr, rule)
	if rule.Into.DiffRetraction {
		require.NoErrorf(t, p.SetDiffRetraction(true),
			"%s declares DiffRetraction but its own activation step refuses it", name)
		// The shared-target scoping, installed the way activation installs it
		// (cmd/refractor's admitRetractionTransport): unconditionally, for any
		// diff lens with a derivable key prefix. Without this step the
		// diffRetraction-prefix transport is unreachable from this census — it
		// would report every scoped diff as an unscoped one, which is a verdict
		// about a deployment nobody runs and the one value the census could
		// never move to.
		if prefix, ok := projection.DiffRetractionPrefix(rule); ok {
			require.NoErrorf(t, p.SetDiffRetractionPrefix(prefix), "%s", name)
		}
	}
	// The partition arming, installed the way activation installs it
	// (cmd/refractor's admitRetractionTransport): after the shared-target
	// scoping, on every lens, with the plane passed in. Without this step the
	// diffRetraction-partition transport would be unreachable from this census
	// — it would report every armed lens's diff as the whole one, which is a
	// verdict about a deployment nobody runs.
	require.NoErrorf(t, p.SetPartitionRetraction(projection.IsAuthPlane(rule)), "%s", name)

	// The divergence audit, installed because the transport an OPERATOR reads
	// is the one the heartbeat publishes — and the heartbeat runs long after
	// activation's InstallAudit (cmd/refractor's startPipeline installs the
	// audit several stages past the retraction gate). The partition transport's
	// arming has an audit half (Pipeline.partitionArmed), so a census that
	// never enrolled one would report every armed lens as running the whole
	// diff: a verdict about the first few milliseconds of a process rather than
	// about the deployment.
	p.InstallAudit(pipeline.AuditOptions{AuthPlane: projection.IsAuthPlane(rule)})

	plane := planeBusiness
	if projection.IsAuthPlane(rule) {
		plane = planeAuth
	}
	v := p.PlainRetractionTransport(projection.IsAuthPlane(rule))
	require.Truef(t, v.Classified,
		"%s is a plain full-engine lens but the transport predicate declined to classify it", name)
	require.Truef(t, v.Exhaustive,
		"%s: the neighbour-dependency classifier could not answer exhaustively, which the activation gate reads as a "+
			"refusal — the lens would not activate. Read the shape it could not model before pinning anything", name)

	return retractionVerdict{
		plane:              plane,
		dependsOnNeighbour: v.DependsOnNeighbour,
		transport:          v.Transport,
	}
}

// derivePlainRetractionCensus classifies every plain lens the corpus ships,
// keyed by the canonical name every census in this package pins by.
func derivePlainRetractionCensus(t *testing.T) map[string]retractionVerdict {
	t.Helper()
	eng := full.New()
	got := map[string]retractionVerdict{}
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		if fullCR.AnchorHopIndex().Incomplete != noAnchorPosition {
			return // anchored — not this census's corpus, exactly as the scan-root pin reads it
		}
		_, dup := got[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		got[name] = deriveRetractionVerdict(t, eng, name, spec,
			rule)
	})
	return got
}

// TestPlainRetractionTransportCensus_PinnedVerdicts pins the
// (plane, dependsOnNeighbour, transport) triple for every plain lens the corpus
// ships, and holds the population to the scan-root pin's.
func TestPlainRetractionTransportCensus_PinnedVerdicts(t *testing.T) {
	got := derivePlainRetractionCensus(t)

	// The population is the scan-root pin's, both ways. Taking it from there
	// rather than from a second enumeration is what stops the two censuses
	// silently covering different corpora — and the floor stops an emptied
	// enumeration reading as a table of unchanged rows.
	require.Greaterf(t, len(scanRootCorpusVerdicts), 40,
		"the scan-root pin this census takes its population from holds only %d lenses — the enumeration moved",
		len(scanRootCorpusVerdicts))
	for name := range scanRootCorpusVerdicts {
		_, present := got[name]
		require.Truef(t, present,
			"plain lens %q is in the scan-root pin's population but did not reach this census — the two enumerations have diverged", name)
	}
	for name := range got {
		_, present := scanRootCorpusVerdicts[name]
		require.Truef(t, present,
			"plain lens %q reached this census but is not in the scan-root pin's population — the two enumerations have diverged", name)
	}

	for name, want := range plainRetractionCorpusVerdicts {
		have, present := got[name]
		if !present {
			t.Errorf("pinned plain lens %q no longer reaches the census — remove its row if the lens was retired", name)
			continue
		}
		require.Equalf(t, want.plane, have.plane, "%s's plane moved", name)
		require.Equalf(t, want.dependsOnNeighbour, have.dependsOnNeighbour,
			"%s: whether a NEIGHBOUR event can drop its row moved — true is a lens acquiring a retraction obligation", name)
		require.Equalf(t, want.transport, have.transport,
			"%s's retraction transport moved — a move to none is a lens LOSING the only thing that retracts a neighbour drop-out", name)
	}
	for name := range got {
		_, pinned := plainRetractionCorpusVerdicts[name]
		require.Truef(t, pinned,
			"plain lens %q ships with no pinned retraction-transport verdict (derived: %+v) — review it, then record it",
			name, got[name])
	}
}

// TestPlainRetractionTransportCensus_NoBusinessPlaneGap is the invariant the
// activation gate enforces, asserted where an author meets it: on the business
// plane, a lens whose row existence depends on a required neighbour carries a
// transport. A lens landing here with `none` would activate DARK at runtime.
func TestPlainRetractionTransportCensus_NoBusinessPlaneGap(t *testing.T) {
	got := derivePlainRetractionCensus(t)
	require.Greaterf(t, len(got), 40, "only %d plain lenses reached this gate — the enumeration moved", len(got))

	var gap []string
	for name, v := range got {
		if v.plane == planeBusiness && v.dependsOnNeighbour && v.transport == pipeline.RetractionTransportNone {
			gap = append(gap, name)
		}
	}
	require.Emptyf(t, gap,
		"these business-plane lenses can be orphaned by a neighbour event and carry no retraction transport: %v.\n\n"+
			"cmd/refractor's activation gate refuses each of them, so they would not activate at all. Give the lens a "+
			"transport — a shape the derivation licence admits (T1), or DiffRetraction on a target it owns (T2) — "+
			"rather than relaxing this assertion.", gap)
}

// TestPlainRetractionTransportCensus_Partition reports the live figures rather
// than asserting a number a build note recorded, and holds the corpus to what
// the partition MEANS.
//
// "Every lens lands in exactly one bucket" is true of any input — a lens has one
// transport value — so the assertion is the two facts that are not: every
// neighbour-dependent lens carries a transport that is not `none`, except for a
// named set; and that set is exactly the auth-plane debt the pin above records,
// derived from those rows rather than counted in prose. A hand count in a
// comment is a second enumeration of the same table, and it is the half that
// goes stale.
func TestPlainRetractionTransportCensus_Partition(t *testing.T) {
	got := derivePlainRetractionCensus(t)

	byPlane := map[string]int{}
	byTransport := map[string]int{}
	depends := 0
	var liveDebt []string
	for name, v := range got {
		byPlane[v.plane]++
		if !v.dependsOnNeighbour {
			continue
		}
		depends++
		byTransport[v.transport]++
		if v.transport == pipeline.RetractionTransportNone {
			liveDebt = append(liveDebt, name)
		}
	}
	t.Logf("plain lenses: %d (business %d, auth %d)", len(got), byPlane[planeBusiness], byPlane[planeAuth])
	t.Logf("row existence depends on a required neighbour: %d", depends)
	for _, transport := range []string{
		pipeline.RetractionTransportNone,
		pipeline.RetractionTransportDerivation,
		pipeline.RetractionTransportDiffRetraction,
		pipeline.RetractionTransportDiffRetractionPrefix,
		pipeline.RetractionTransportDiffRetractionPartition,
	} {
		label := transport
		if label == pipeline.RetractionTransportNone {
			label = "none"
		}
		t.Logf("  transport %s: %d", label, byTransport[transport])
	}

	carrying := 0
	for transport, n := range byTransport {
		if transport != pipeline.RetractionTransportNone {
			carrying += n
		}
	}
	require.Equalf(t, depends-len(liveDebt), carrying,
		"every neighbour-dependent lens outside the named debt set must carry exactly one transport (%d dependent, %d in debt, %d carrying)",
		depends, len(liveDebt), carrying)

	// The debt set, derived from the pin's own rows. This is the list the
	// prose above deliberately does not count.
	var pinnedDebt []string
	for name, v := range plainRetractionCorpusVerdicts {
		if v.dependsOnNeighbour && v.transport == pipeline.RetractionTransportNone {
			pinnedDebt = append(pinnedDebt, name)
		}
	}
	require.ElementsMatchf(t, pinnedDebt, liveDebt,
		"the lenses running with a neighbour obligation and no transport must be exactly the ones pinned as debt — "+
			"a name appearing live and not in the pin is a lens that just lost its transport")

	for _, name := range liveDebt {
		require.Equalf(t, planeAuth, got[name].plane,
			"%s carries a neighbour obligation and no transport on the BUSINESS plane, where the activation gate refuses it — "+
				"debt is only nameable on the plane the gate does not cover", name)
	}
}

// TestPlainRetractionTransportCensus_SharedBucketsAreDisjoint holds every
// NATS-KV bucket the corpus writes to the shared-target rule, from every load
// order.
//
// A target diff enumerates its bucket and Deletes every key the fresh row set
// does not contain, so on a bucket two lenses share the scoping is the only
// thing standing between one lens's event and the other's rows. Activation
// decides that per lens, against whichever siblings happen to be live; this
// asks it of EVERY member against every other, which is the same question with
// the load order removed.
//
// A refusal here is a corpus that cannot boot: whichever of the colliding pair
// arrives second never activates.
func TestPlainRetractionTransportCensus_SharedBucketsAreDisjoint(t *testing.T) {
	// Every lens on the bucket counts, not only the plain ones this file's
	// other censuses classify: an actor-aggregate lens's rows are keys in the
	// same bucket, and a diff that lists them deletes them just the same.
	byBucket := map[string][]*lens.Rule{}
	claimedBy := map[string]string{}
	forEachCorpusCypher(t, func(name string, _ string, rule *lens.Rule, _, _ bool) {
		if rule.Into.Target != "nats_kv" || rule.Into.Bucket == "" {
			return
		}
		// A multi-walk lens visits once per BRANCH, under "<canonicalName>#<i>",
		// and every branch writes the one bucket the lens declares — so a repeat
		// under a branch name is one lens seen again, not two lenses. Any other
		// repeat is two declarations claiming one canonical name, which is the
		// silent overwrite every census keyed by that name inherits: one lens
		// reported twice and the other never.
		isBranch := func(visit string) bool { return strings.Contains(visit, "#") }
		if prior, dup := claimedBy[rule.CanonicalName]; dup {
			require.Truef(t, isBranch(prior) && isBranch(name),
				"two installed lenses share the canonical name %q (seen as %q and %q) — one declaration surface is "+
					"silently overwriting the other, and every census keyed by canonical name then reports one of them "+
					"twice and the other never", rule.CanonicalName, prior, name)
			return
		}
		claimedBy[rule.CanonicalName] = name
		byBucket[rule.Into.Bucket] = append(byBucket[rule.Into.Bucket], rule)
	})
	require.Greaterf(t, len(byBucket), 3, "only %d NATS-KV buckets reached this census — the enumeration moved", len(byBucket))

	shared := 0
	for bucket, rules := range byBucket {
		if len(rules) < 2 {
			continue
		}
		shared++
		for _, r := range rules {
			var siblings []projection.SiblingLens
			for _, other := range rules {
				if other == r {
					continue
				}
				siblings = append(siblings, corpusSibling(other))
			}
			_, refusal := projection.SharedTargetDiffRefusal(r, siblings)
			require.Emptyf(t, refusal, "%s cannot activate onto %q: %s", r.CanonicalName, bucket, refusal)
		}
	}
	t.Logf("NATS-KV buckets: %d, of which shared by more than one lens: %d", len(byBucket), shared)
}

// corpusSibling describes a declared lens the way the registry would once it is
// running: its diff posture, and the scoping activation installs for it —
// unconditionally, for any diff lens with a derivable prefix
// (cmd/refractor's admitRetractionTransport). Reading the DECLARED prefix as the
// installed one is exactly what makes this census order-independent: it is the
// prefix every load order ends up with.
func corpusSibling(r *lens.Rule) projection.SiblingLens {
	s := projection.SiblingLens{
		CanonicalName:  r.CanonicalName,
		DiffRetraction: r.Into.DiffRetraction,
		Output:         r.Output,
	}
	if s.DiffRetraction {
		if prefix, ok := projection.DiffRetractionPrefix(r); ok {
			s.DiffRetractionPrefix = prefix
		}
	}
	return s
}

// TestPlainDerivationStaticallyEligible_IsTheLicencePrefix holds the shared
// static predicate to the three shipped predicates it is composed of, for every
// plain lens the corpus ships.
//
// The gate and the licence call ONE function, which makes the licence's own
// tests the gate's too; this is the corpus-wide half of that proof. The shared
// predicate admits a lens exactly when the derivation index is ready AND the
// divergence audit's own enrolment admits it AND its rows close per anchor. Each
// of those three is an independently shipped predicate with its own consumers,
// so a conjunct silently dropped from the shared one shows up here as a
// disagreement rather than as a lens the gate waves through and the licence
// never licenses.
func TestPlainDerivationStaticallyEligible_IsTheLicencePrefix(t *testing.T) {
	eng := full.New()
	checked := 0
	agreed := 0
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		if fullCR.AnchorHopIndex().Incomplete != noAnchorPosition {
			return
		}
		checked++

		if cols := closureKeyColumns(fullCR, rule); cols != nil {
			require.NoErrorf(t, projection.ThreadKeyColumns(fullCR, nil, cols), "%s", name)
		} else {
			fullCR.KeyColumns = nil
		}
		p := corpusInstalledPipeline(t, name, eng, cr, rule)
		if rule.Into.DiffRetraction {
			require.NoErrorf(t, p.SetDiffRetraction(true), "%s", name)
		}

		eligible, refusal := p.PlainDerivationStaticallyEligible()
		require.Equalf(t, eligible, refusal == "",
			"%s: the static predicate must carry a reason with every refusal and none with an admission", name)

		// The derivation index's own answer, read through the surface that
		// publishes it: in act mode PlainDerivationStatus.Eligible IS
		// plainDerivationIndex's verdict. The mode is set on the pipeline
		// rather than left to the package default, so a host that has moved the
		// default cannot make this comparison read a permanent false.
		p.SetAnchorDerivationMode(pipeline.DerivationModeAct)
		indexReady := p.PlainDerivationStatus().Eligible

		// The audit's own enrolment, run through InstallAudit rather than
		// restated. The plane is passed as the audit reads it, so an auth-plane
		// lens refuses here exactly as it does live — and the static predicate
		// carries no plane conjunct, which is why the comparison below excludes
		// that one refusal.
		enrolled, auditRefusal := p.InstallAudit(pipeline.AuditOptions{AuthPlane: projection.IsAuthPlane(rule)})
		auditAdmits := enrolled || auditRefusal == auditPlaneRefusal

		closes := fullCR.ProjectsOneRowPerAnchor()

		require.Equalf(t, indexReady && auditAdmits && closes, eligible,
			"%s: the shared static predicate disagrees with the three shipped predicates it composes — "+
				"index ready=%v, audit admits=%v (refusal %q), closes per anchor=%v, static eligible=%v (refusal %q). "+
				"The gate and the licence both read the static predicate, so a conjunct it has dropped or gained "+
				"is a lens the gate admits and the licence will never license, or the reverse",
			name, indexReady, auditAdmits, auditRefusal, closes, eligible, refusal)
		if eligible {
			agreed++
		}
	})
	require.Greaterf(t, checked, 40, "only %d plain lenses reached this gate — the enumeration moved", checked)
	t.Logf("plain lenses statically eligible for the derivation transport: %d of %d", agreed, checked)
}

// auditPlaneRefusal is auditEnrolment's auth-plane reason verbatim. The static
// predicate carries no plane conjunct — the plane is the licence's own first
// conjunct and the gate's scope — so an auth-plane lens's audit refusal is the
// one the comparison above must look past rather than count as a shape refusal.
const auditPlaneRefusal = "it projects onto the auth plane, whose per-row verdicts are the convergence sweep's"
