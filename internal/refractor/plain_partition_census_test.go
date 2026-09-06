// Plain-lens anchor-partition census —
// anchor-partitioned-plain-lens-retraction-design.md §2.1, §5.1.
//
// plain_with_alias_closure_census_test.go asks the CLOSURE question of the whole
// plain corpus — can every key column be derived read-free from the anchor. This
// file asks the weaker one the partition-scoped retraction rests on: do the
// lens's rows PARTITION by its anchor — every row belongs to exactly one anchor
// and is computed from that anchor's own bindings — even when the key carries a
// column bound to a neighbour the walk reached.
//
// It runs the real predicates (ProjectsOneRowPerAnchor, PartitionsByAnchor) over
// the real installed corpus through forEachCorpusCypher, with each lens's
// declared Into.Key threaded exactly as activation threads it — the same
// enumeration and the same closureKeyColumns the with-alias census uses, never a
// second sweep and never a grep of cypher text.
//
// THE THREE MEMBERSHIPS, and what each is for:
//
//   - partitions ⊇ oneRowPerAnchor. The partition predicate is the closure
//     predicate minus one conjunct, so a lens the shipped conjunct admits and
//     the new one refuses is a REGRESSION in the licence every closed lens
//     already runs behind — it fails here by name.
//   - the partition-ONLY set: the eight lenses this mechanism is for. Each of
//     them gains per-anchor seeding, a narrowing licence and a partition-scoped
//     target diff at once, so a lens arriving or leaving is a lens whose whole
//     retraction posture just moved.
//   - every partition-only lens declares DiffRetraction. That is what makes the
//     set safe to arm: the partition transport replaces a whole diff that was
//     already running, rather than giving a lens with no diff at all a new
//     Delete path.
package refractor_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// partitionVerdict is one plain lens's pinned partition classification.
type partitionVerdict struct {
	// oneRowPerAnchor is full.CompiledRule.ProjectsOneRowPerAnchor — the
	// shipped closure conjunct the narrowing licence asks today.
	oneRowPerAnchor bool
	// partitions is full.CompiledRule.PartitionsByAnchor's ok, and identifying
	// the key columns it names as saying WHICH anchor a row belongs to.
	partitions  bool
	identifying []string
	// diffRetraction is the lens's own declaration — read off the rule the
	// install switch dispatches on, never off the cypher.
	diffRetraction bool
}

// plainPartitionCorpusVerdicts pins the four answers for every plain lens the
// corpus ships. A lens whose row moves has changed retraction posture; read the
// column that moved before touching this table.
var plainPartitionCorpusVerdicts = map[string]partitionVerdict{
	"applicantRosterRead":            {oneRowPerAnchor: true, partitions: true, identifying: []string{"identity_id"}, diffRetraction: false},
	"augurProposals":                 {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"availableListings":              {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"cafeIdentitiesRead":             {oneRowPerAnchor: true, partitions: true, identifying: []string{"identity_id"}, diffRetraction: false},
	"cafeLeaseAccounts":              {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"cafeLeaseWorkplaces":            {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"cafeLedgerHistory":              {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"capabilityAuthorContext":        {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"capabilityAuthorPackages":       {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"capabilityProposals":            {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"capabilityReadGrants":           {oneRowPerAnchor: true, partitions: true, identifying: []string{"actor_id", "anchor_id"}, diffRetraction: false},
	"capabilityReadWildcardGrants":   {oneRowPerAnchor: true, partitions: true, identifying: []string{"actor_id"}, diffRetraction: false},
	"capabilityRoleIndex":            {oneRowPerAnchor: false, partitions: false, identifying: nil, diffRetraction: false},
	"clinicAppointments":             {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"clinicAppointmentsRead":         {oneRowPerAnchor: true, partitions: true, identifying: []string{"appointment_id"}, diffRetraction: false},
	"clinicEncountersRead":           {oneRowPerAnchor: true, partitions: true, identifying: []string{"appointment_id"}, diffRetraction: false},
	"clinicIdentitiesRead":           {oneRowPerAnchor: true, partitions: true, identifying: []string{"identity_id"}, diffRetraction: false},
	"clinicLedgerHistory":            {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"clinicPatientAccounts":          {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"clinicPatientReadGrants":        {oneRowPerAnchor: true, partitions: true, identifying: []string{"actor_id", "anchor_id"}, diffRetraction: false},
	"clinicPatients":                 {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"clinicPatientsRead":             {oneRowPerAnchor: true, partitions: true, identifying: []string{"patient_id"}, diffRetraction: true},
	"clinicProviderReadGrants":       {oneRowPerAnchor: true, partitions: true, identifying: []string{"actor_id", "anchor_id"}, diffRetraction: false},
	"clinicProviders":                {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"clinicSites":                    {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"consoleOperatorReadGrants":      {oneRowPerAnchor: true, partitions: true, identifying: []string{"actor_id"}, diffRetraction: true},
	"demoOperatorReadGrants":         {oneRowPerAnchor: true, partitions: true, identifying: []string{"actor_id"}, diffRetraction: true},
	"duplicateCandidates":            {oneRowPerAnchor: false, partitions: true, identifying: []string{"secondaryId"}, diffRetraction: true},
	"frontDeskBookingHistory":        {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"frontDeskBookings":              {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"frontDeskLeaseDetails":          {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"frontDeskVisits":                {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"identityCredentialBindingsRead": {oneRowPerAnchor: true, partitions: true, identifying: []string{"binding_id"}, diffRetraction: false},
	"identityCredentialsRead":        {oneRowPerAnchor: true, partitions: true, identifying: []string{"identity_id"}, diffRetraction: false},
	"identityIndexHint":              {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"landlordLeaseApplicationsRead":  {oneRowPerAnchor: false, partitions: true, identifying: []string{"app_id"}, diffRetraction: true},
	"landlordUnitsRead":              {oneRowPerAnchor: false, partitions: true, identifying: []string{"unit_id"}, diffRetraction: true},
	"leaseAccounts":                  {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"leaseApplicationsRead":          {oneRowPerAnchor: true, partitions: true, identifying: []string{"app_id"}, diffRetraction: false},
	"ledgerHistory":                  {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"menuCatalog":                    {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"objectIdentityAttachmentsRead":  {oneRowPerAnchor: false, partitions: true, identifying: []string{"oid_id"}, diffRetraction: true},
	"oneBillCafeEntries":             {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"oneBillClinicEntries":           {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"oneBillRentEntries":             {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"oneBillWellnessEntries":         {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"opCatalog":                      {oneRowPerAnchor: false, partitions: false, identifying: nil, diffRetraction: false},
	"patientIdentityReadGrants":      {oneRowPerAnchor: false, partitions: true, identifying: []string{"anchor_id"}, diffRetraction: true},
	"piiKeyEnvelope":                 {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"providerAppointmentsRead":       {oneRowPerAnchor: true, partitions: true, identifying: []string{"appointment_id"}, diffRetraction: false},
	"providerIdentityReadGrants":     {oneRowPerAnchor: false, partitions: true, identifying: []string{"actor_id"}, diffRetraction: true},
	"providerSites":                  {oneRowPerAnchor: false, partitions: true, identifying: []string{"provider_id"}, diffRetraction: true},
	"renewalsRead":                   {oneRowPerAnchor: true, partitions: true, identifying: []string{"renewal_id"}, diffRetraction: false},
	"retentionKeyStatus":             {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"shredStatus":                    {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"staffReadGrants":                {oneRowPerAnchor: false, partitions: true, identifying: []string{"actor_id"}, diffRetraction: true},
	"visitSeriesRead":                {oneRowPerAnchor: true, partitions: true, identifying: []string{"series_id"}, diffRetraction: false},
	"wellnessBookers":                {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"wellnessBookings":               {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"wellnessIdentitiesRead":         {oneRowPerAnchor: true, partitions: true, identifying: []string{"identity_id"}, diffRetraction: false},
	"wellnessInstructors":            {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"wellnessLedgerHistory":          {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"wellnessMemberAccounts":         {oneRowPerAnchor: false, partitions: false, identifying: nil, diffRetraction: true},
	"wellnessMembers":                {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"wellnessSessions":               {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
	"wellnessStudios":                {oneRowPerAnchor: true, partitions: true, identifying: []string{"key"}, diffRetraction: false},
}

// partitionOnlyLenses is the payoff set: the lenses PartitionsByAnchor admits
// and ProjectsOneRowPerAnchor refuses. Pinned by name and derived live, both
// ways — a lens joining gains a partition-scoped Delete path, a lens leaving
// loses one and falls back to a whole-corpus rescan on every event.
var partitionOnlyLenses = []string{
	"duplicateCandidates",
	"landlordLeaseApplicationsRead",
	"landlordUnitsRead",
	"objectIdentityAttachmentsRead",
	"patientIdentityReadGrants",
	"providerIdentityReadGrants",
	"providerSites",
	"staffReadGrants",
}

// partitionRefusedLenses is the set neither predicate admits, and the reason
// each is refused is a property of its cypher rather than of this mechanism:
// wellnessMemberAccounts keys on the identity its booking anchor reaches;
// capabilityRoleIndex keys on an operation type that binds the permission, with
// no column naming the role; opCatalog keys on the anchor's own root field
// rather than on its key.
var partitionRefusedLenses = []string{
	"capabilityRoleIndex",
	"opCatalog",
	"wellnessMemberAccounts",
}

// plainPartitionCensusFloor is the number of plain lenses that must reach the
// classifier — the same floor the with-alias census pins. It guards the shape an
// emptied enumeration takes: a census that swept nothing would report every
// membership below as satisfied.
//
// THE LIVE CORPUS IS 66, and the design's §2.1 table says 65 / 54 / 8 / 3. The
// difference is one plain lens that landed after the design's snapshot at
// `34ce301c` and closes-and-identifies, so it lands in the bucket the new
// conjunct does not move: 55 closed rather than 54. The two memberships the
// design actually asserts — the 8 partition-only names and the 3 refusals — are
// unmoved, and the superset claim is asserted below BY NAME rather than by a
// count, so the drift cannot hide a regression in either.
const plainPartitionCensusFloor = 66

// derivePartitionVerdict is the live classification for one plain lens, run
// through the SHIPPED predicates rather than a restatement of them.
func derivePartitionVerdict(t *testing.T, eng *full.Engine, name, spec string, rule *lens.Rule) partitionVerdict {
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

	identifying, partitions := fullCR.PartitionsByAnchor()
	return partitionVerdict{
		oneRowPerAnchor: fullCR.ProjectsOneRowPerAnchor(),
		partitions:      partitions,
		identifying:     identifying,
		diffRetraction:  rule.Into.DiffRetraction,
	}
}

// TestPlainPartitionCensus classifies every plain lens the corpus ships, holds
// the result to the pinned table in both directions, and asserts the three
// memberships §2.1 rests on.
func TestPlainPartitionCensus(t *testing.T) {
	eng := full.New()
	got := map[string]partitionVerdict{}

	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, declaredAggregate, declaredPersonal bool) {
		if declaredAggregate || declaredPersonal {
			return
		}
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		if fullCR.AnchorHopIndex().Incomplete != noAnchorPosition {
			return // anchored — this census's population is the plain corpus
		}
		_, dup := got[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		got[name] = derivePartitionVerdict(t, eng, name, spec, rule)
	})

	require.GreaterOrEqualf(t, len(got), plainPartitionCensusFloor,
		"only %d plain lenses reached the classifier — the corpus enumeration or the plain filter moved, "+
			"and every membership below would read as satisfied on an emptied sweep", len(got))

	var closed, partitionOnly, refused []string
	for name, v := range got {
		switch {
		case v.oneRowPerAnchor:
			closed = append(closed, name)
		case v.partitions:
			partitionOnly = append(partitionOnly, name)
		default:
			refused = append(refused, name)
		}
	}
	sort.Strings(closed)
	sort.Strings(partitionOnly)
	sort.Strings(refused)
	t.Logf("closed and identifying (unchanged by the new conjunct): %d", len(closed))
	t.Logf("partition-only (the payoff set): %d %v", len(partitionOnly), partitionOnly)
	t.Logf("refused by both: %d %v", len(refused), refused)
	t.Logf("TOTAL: %d", len(got))

	// Membership 1: the new conjunct is a SUPERSET of the shipped one. A lens
	// landing here has lost the narrowing licence it already runs behind.
	var regressed []string
	for name, v := range got {
		if v.oneRowPerAnchor && !v.partitions {
			regressed = append(regressed, name)
		}
	}
	sort.Strings(regressed)
	require.Emptyf(t, regressed,
		"these lenses close per anchor but the partition predicate refuses them: %v.\n"+
			"PartitionsByAnchor is ProjectsOneRowPerAnchor minus one conjunct and can never refuse what closure admits — "+
			"the shared resolver body has diverged", regressed)

	// Membership 2: the payoff set, by name.
	require.Equal(t, partitionOnlyLenses, partitionOnly,
		"the partition-only set is what this mechanism arms — a lens arriving gains per-anchor seeding, a narrowing "+
			"licence and a partition-scoped target diff at once; a lens leaving loses all three")

	// Membership 3: the refusals, by name.
	require.Equal(t, partitionRefusedLenses, refused,
		"a lens leaving this set has acquired a key column that identifies its anchor, which is the whole question")

	// Membership 4: every partition-only lens already runs a whole target diff.
	// That is what makes arming the set a NARROWING of a Delete path rather
	// than a new one.
	for _, name := range partitionOnly {
		require.Truef(t, got[name].diffRetraction,
			"%s partitions by its anchor but declares no DiffRetraction — the partition transport scopes a diff that "+
				"is already running, and arming it for a lens with no diff would hand a new Delete path to a lens "+
				"nothing ever asked to retract this way", name)
	}

	for name, want := range plainPartitionCorpusVerdicts {
		have, present := got[name]
		if !present {
			t.Errorf("pinned plain lens %q no longer reaches this census — remove its row if the lens was retired", name)
			continue
		}
		require.Equalf(t, want.oneRowPerAnchor, have.oneRowPerAnchor, "%s's closure verdict moved", name)
		require.Equalf(t, want.partitions, have.partitions, "%s's partition verdict moved", name)
		require.Equalf(t, want.identifying, have.identifying, "%s's identifying key columns moved", name)
		require.Equalf(t, want.diffRetraction, have.diffRetraction, "%s's DiffRetraction declaration moved", name)
	}
	for name := range got {
		_, pinned := plainPartitionCorpusVerdicts[name]
		require.Truef(t, pinned,
			"plain lens %q ships with no pinned partition verdict (derived: %+v) — review it, then record it",
			name, got[name])
	}
}
