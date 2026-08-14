package pipeline

// The plain arm's narrowing licence (§5 of
// plain-lens-neighbour-anchor-derivation-design.md): five fail-closed
// conjuncts, three of them shared verbatim with the divergence audit's
// enrolment and two — the enrolled Auditor and per-anchor closure — its own.
// Every conjunct gets a negative case here, and the positive vector sits at the
// top: without it a green refusal could equally come from a gate that refuses
// everything, which is the failure mode nobody would notice.
//
// The lenses are built by newAuditFixture (audit_test.go) rather than by hand,
// because two conjuncts read state only a real installation produces: the
// Auditor comes from InstallAudit's own enrolment run, and the RowReader
// conjunct reads the live adapter.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// licenceNeighbourKeyedSpec is the closure conjunct's negative vector: a lens
// whose key column binds the NEIGHBOUR variable, so its row set partitions by
// landlord rather than by its own anchor and a per-anchor evaluation would
// compute a truncated row. Everything else about it matches seedUnitsSpec, the
// positive twin.
const licenceNeighbourKeyedSpec = `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN l.key AS key, u.name AS name
`

// licenceCollapsedKeySpec is closure's OTHER negative vector, and the one the
// key-column walk alone cannot see: the key column is a literal, so it
// references no non-anchor variable and passes anchor-only by vacuity — but
// every unit lands in the same group, so the row's collect() spans all of them
// and a per-anchor evaluation would compute it from one.
const licenceCollapsedKeySpec = `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN 'all' AS key, collect(u.name) AS names
`

// licenceFixture builds an audited plain lens and arms its auditor through the
// production enrolment path, so the licence is asked of a pipeline in the state
// a real activation leaves behind.
//
// It then runs one real pass, because a licensed lens is one whose audit is
// actually TICKING and the staleness conjunct reads that off the verdict clock:
// an auditor that has never completed a pass carries a zero LastPassAt and is
// stale by construction. The pass is driven rather than the timestamp written,
// for the same reason the suppression vector below drives a pause: the clock the
// licence reads must be the one the auditor itself stamps.
func licenceFixture(t *testing.T, spec string) *auditFixture {
	t.Helper()
	f := newAuditFixture(t, spec, nil)
	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled, "the fixture lens must enrol; refusal: %s", refusal)
	f.p.Auditor().pass(context.Background())
	require.False(t, f.p.Auditor().Status().LastPassAt.IsZero(),
		"the fixture's audit must have reached a verdict, or every licence below refuses as stale")
	return f
}

// ageLastPass backdates the auditor's verdict clock by d. Written directly under
// the auditor's own lock because the alternative — waiting for a real cadence to
// lapse — is a fixed sleep, and the value under test is a duration nothing else
// can move. The FIELD is the mechanism's whole input (AuditStatus.LastPassAt is
// the only thing that ages when the tick loop stops), so posing it is posing the
// real state a wedged loop leaves behind, not a proxy for it.
func ageLastPass(a *Auditor, d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status.LastPassAt = a.status.LastPassAt.Add(-d)
}

// parseLicenceRule compiles a rule to swap onto an ALREADY-ENROLLED pipeline's
// rule snapshot — the shape a MATCH hot-reload leaves behind, and the only way
// to reach the conjuncts the licence shares with the audit's enrolment: a lens
// carrying $now cannot enrol in the first place, so the licence's own copy of
// that conjunct is exactly what guards a rule that arrives AFTER the auditor
// did. InstallAudit runs once at activation and its published Enrolled verdict
// does not fall back to false on a later rule swap, so the licence re-deriving
// the parameters per event is the live check, not a redundant one (§13's own
// risk row: the two predicates must not depend on each other).
func parseLicenceRule(t *testing.T, spec string) *full.CompiledRule {
	t.Helper()
	cr, err := full.New().Parse(spec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, fullCR.ValidateKeyColumns())
	return fullCR
}

// TestPlainDerivationLicence_Conjuncts walks the licence one conjunct at a
// time. A refusal must always carry a reason: this predicate decides whether a
// lens's writes get narrower, and "refused" with no cause is indistinguishable
// from a gate nobody can debug.
func TestPlainDerivationLicence_Conjuncts(t *testing.T) {
	t.Run("positive vector: an enrolled, closed, row-readable plain lens is licensed", func(t *testing.T) {
		f := licenceFixture(t, seedUnitsSpec)
		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.True(t, licensed, "refusal: %s", refusal)
		require.Empty(t, refusal)
	})

	t.Run("the auth plane refuses", func(t *testing.T) {
		// This package cannot import projection (which imports it), so the
		// plane's DERIVATION and its recording at activation are pinned where
		// they happen — cmd/refractor's TestActivationRecordsTheLensPlane,
		// which activates a lens from its rule and reads p.AuthPlane() back,
		// per IsAuthPlane arm. What this pins is the other half: that the
		// licence reads that field and refuses on it.
		f := licenceFixture(t, seedUnitsSpec)
		f.p.SetAuthPlane(true)
		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "auth plane")
	})

	t.Run("no audit installed at all refuses", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		require.Nil(t, f.p.Auditor(), "the vector for this case is a pipeline InstallAudit never ran on")
		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "no divergence audit is enrolled")
	})

	t.Run("a REFUSED audit refuses — a published non-verdict is not a watcher", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		// Target-diff retraction refuses enrolment, and is deliberately NOT a
		// licence conjunct of its own (plainDerivationIndex carries it), so the
		// refusal below can only be the auditor's.
		require.NoError(t, f.p.SetDiffRetraction(true))
		enrolled, _ := f.p.InstallAudit(AuditOptions{})
		require.False(t, enrolled)
		require.NotNil(t, f.p.Auditor(), "a refusal is a published verdict, not an absence")

		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "no divergence audit is enrolled")
	})

	t.Run("a compiled rule that is not a full-engine rule refuses", func(t *testing.T) {
		f := licenceFixture(t, seedUnitsSpec)
		rs := f.p.ruleState()
		rs.cr = nil
		licensed, refusal := f.p.plainDerivationLicence(rs)
		require.False(t, licensed)
		require.Contains(t, refusal, "not a full-engine rule")
	})

	t.Run("a target that cannot read a row back refuses", func(t *testing.T) {
		// Swapped in AFTER enrolment, so the auditor conjunct above still
		// passes and this refusal is the adapter and nothing else — the same
		// adapter swap the audit's own enrolment re-check exists for.
		f := licenceFixture(t, seedUnitsSpec)
		require.NoError(t, f.p.HotReloadInto(notARowReader{inner: f.p.currentAdapter()}))
		_, isReader := f.p.currentAdapter().(adapter.RowReader)
		require.False(t, isReader)

		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "cannot read a row back")
	})

	t.Run("a Secure Lens refuses", func(t *testing.T) {
		f := licenceFixture(t, seedUnitsSpec)
		f.p.SetSecureDecryptor(&SecureDecryptor{})
		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "Secure Lens")
	})

	t.Run("a query returning $now refuses", func(t *testing.T) {
		f := licenceFixture(t, seedUnitsSpec)
		rs := f.p.ruleState()
		rs.cr = parseLicenceRule(t, `
MATCH (u:unit)
RETURN u.key AS key, $now AS observedAt
`)
		licensed, refusal := f.p.plainDerivationLicence(rs)
		require.False(t, licensed)
		require.Contains(t, refusal, "returns $now")
	})

	t.Run("a query returning $projectedAt refuses", func(t *testing.T) {
		f := licenceFixture(t, seedUnitsSpec)
		rs := f.p.ruleState()
		rs.cr = parseLicenceRule(t, `
MATCH (u:unit)
RETURN u.key AS key, $projectedAt AS at
`)
		licensed, refusal := f.p.plainDerivationLicence(rs)
		require.False(t, licensed)
		require.Contains(t, refusal, "returns $projectedAt")
	})

	t.Run("a shape the parameter walk cannot rule the parameter out of refuses", func(t *testing.T) {
		// (referenced=false, exhaustive=false) is the accessor saying "I could
		// not tell". Reading that as an absence is the exact
		// read-the-declaration-not-the-matcher mistake the flag exists to
		// prevent, so it must refuse rather than pass.
		f := licenceFixture(t, seedUnitsSpec)
		rs := f.p.ruleState()
		rs.cr = &full.CompiledRule{}
		licensed, refusal := f.p.plainDerivationLicence(rs)
		require.False(t, licensed)
		require.Contains(t, refusal, "could not be proven free of $now")
	})

	t.Run("a lens keyed on a neighbour variable refuses — closure", func(t *testing.T) {
		f := licenceFixture(t, licenceNeighbourKeyedSpec)
		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "do not partition by anchor")

		// The same lens enrols for the AUDIT, which has no closure conjunct —
		// the concrete reason this predicate cannot be the audit's enrolment
		// under another name (§5.1).
		require.True(t, f.p.Auditor().Status().Enrolled,
			"the audit tolerates a shape the write licence must refuse")
	})

	t.Run("a literal-keyed lens aggregating across roots refuses — closure's second half", func(t *testing.T) {
		// Anchor-only by VACUITY: a literal key column references no variable
		// at all, so the key-column walk alone admits it — while every anchor's
		// bindings land in ONE group whose collect() a per-anchor evaluation
		// would compute from a single unit's worth of inputs.
		f := licenceFixture(t, licenceCollapsedKeySpec)
		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.False(t, licensed)
		require.Contains(t, refusal, "do not partition by anchor")
	})
}

// TestPlainDerivationLicence_SuppressedAuditRefuses closes the fail-open the
// Enrolled bool alone leaves: it is the INSTALL-TIME verdict and is never
// revised, so an auditor held indefinitely — an operator pause, a rebuild, the
// deployment kill switch thrown after activation — still reads enrolled while
// nothing re-tests a single row. That is precisely the state §5.2's conjunct
// ("something standing will re-test this row") exists to exclude.
//
// Driven through a real pass rather than by writing the status field: the
// suppression must be the one the auditor itself publishes, from the same pause
// path an operator uses (mirrors TestAudit_SuppressedWhilePausedOrRebuilding).
func TestPlainDerivationLicence_SuppressedAuditRefuses(t *testing.T) {
	f := licenceFixture(t, seedUnitsSpec)
	a := f.p.Auditor()
	ctx := context.Background()
	f.project(t, auditUnitA, "Loft A", 1)

	// The positive vector first: a running audit licenses this lens.
	a.pass(ctx)
	require.Empty(t, a.Status().Suppression)
	licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
	require.True(t, licensed, "refusal: %s", refusal)

	require.NoError(t, f.reporter.SetPaused(ctx, "operator", "held for investigation"))
	a.pass(ctx)
	require.Contains(t, a.Status().Suppression, "lens status is paused")
	require.True(t, a.Status().Enrolled, "the install-time verdict does not move — which is the point")

	licensed, refusal = f.p.plainDerivationLicence(f.p.ruleState())
	require.False(t, licensed, "a suppressed audit is not a standing re-test")
	require.Contains(t, refusal, "suppressed")
	require.Contains(t, refusal, "paused", "the refusal carries the audit's own cause, not a generic one")

	// And it lifts with the suppression, so this is a live read and not a
	// latch: resuming the lens restores the licence.
	require.NoError(t, f.reporter.SetActive(ctx))
	a.pass(ctx)
	require.Empty(t, a.Status().Suppression)
	licensed, _ = f.p.plainDerivationLicence(f.p.ruleState())
	require.True(t, licensed)
}

// TestPlainDerivationLicence_StaleAuditRefuses closes the fail-open that
// survives BOTH of the fields above. Enrolled is the install-time verdict and
// Suppression is the last tick's reason — and both are written by the tick loop
// itself, so a loop that has stopped running altogether (crashed, deadlocked,
// blocked forever inside a pass) leaves Enrolled true and Suppression empty for
// as long as the process lives. Neither field can ever report that state,
// because reporting it would require the very loop that is gone. LastPassAt is
// the only one that ages on its own, and this is the test that it is read.
//
// The wedged audit is posed by backdating that clock rather than by pausing or
// refusing anything: the whole point is a lens whose every other audit field
// reads healthy, which is what an operator (and the previous licence) would see.
func TestPlainDerivationLicence_StaleAuditRefuses(t *testing.T) {
	f := licenceFixture(t, seedUnitsSpec)
	a := f.p.Auditor()

	// The positive twin first, so a refusal below cannot be a gate that refuses
	// everything: a lens whose audit reached a verdict one interval ago — well
	// inside the window — is licensed.
	ageLastPass(a, a.Interval())
	licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
	require.True(t, licensed, "an audit one interval old is running, not stale; refusal: %s", refusal)

	// And it stays licensed at nine intervals, so the refusal that follows is
	// the window closing and not a cliff somewhere short of it. The exact
	// boundary is pinned in TestAuditorStale, which supplies `now` and so can
	// name an elapsed to the nanosecond; here `now` is the wall clock the licence
	// itself reads, and a test that leaned on the last nanosecond of the window
	// would be asserting that no time passes between two statements.
	ageLastPass(a, (auditorStaleCycles-2)*a.Interval())
	licensed, refusal = f.p.plainDerivationLicence(f.p.ruleState())
	require.True(t, licensed, "nine intervals is inside the window; refusal: %s", refusal)

	ageLastPass(a, 2*a.Interval())
	licensed, refusal = f.p.plainDerivationLicence(f.p.ruleState())
	require.False(t, licensed, "an audit that has not reached a verdict in %d intervals is not a standing re-test", auditorStaleCycles)
	require.Contains(t, refusal, "has not reached a verdict")
	require.True(t, a.Status().Enrolled, "the install-time verdict still reads healthy — which is the point")
	require.Empty(t, a.Status().Suppression, "and so does the suppression reason — neither field can see a wedged loop")

	// A resumed audit restores the licence, so this is a live read of the clock
	// and not a latch.
	a.pass(context.Background())
	licensed, refusal = f.p.plainDerivationLicence(f.p.ruleState())
	require.True(t, licensed, "a fresh verdict must restore the licence; refusal: %s", refusal)
}

// TestPlainDerivationLicence_NeverAuditedRefuses is the staleness conjunct's
// fail-closed direction on a lens whose auditor was installed a moment ago and
// has never completed a pass. It is licensed by every other conjunct, and the
// zero LastPassAt is doing all the work: for a WRITE licence, "no verdict yet"
// must read as not-licensed, which is the opposite of the heartbeat's own
// stall detector (where a fresh install must not alarm).
func TestPlainDerivationLicence_NeverAuditedRefuses(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled, "refusal: %s", refusal)
	require.True(t, f.p.Auditor().Status().LastPassAt.IsZero(), "no pass has run")

	licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
	require.False(t, licensed, "an audit that has proven nothing yet licenses nothing yet")
	require.Contains(t, refusal, "has not reached a verdict")

	// The positive twin: one pass, and the same lens is licensed.
	f.p.Auditor().pass(context.Background())
	licensed, refusal = f.p.plainDerivationLicence(f.p.ruleState())
	require.True(t, licensed, "refusal: %s", refusal)
}

// TestPlainDerivationLicence_IsReadOffLiveFields pins §4.3's own requirement:
// the licence is a per-event read of live pipeline fields, never a verdict
// snapshotted at install. Activation installs components in stages, so a
// snapshot would read a later stage's component as absent — and for a licence,
// absent reads as satisfied.
func TestPlainDerivationLicence_IsReadOffLiveFields(t *testing.T) {
	f := licenceFixture(t, seedUnitsSpec)
	rs := f.p.ruleState()

	licensed, _ := f.p.plainDerivationLicence(rs)
	require.True(t, licensed)

	f.p.SetSecureDecryptor(&SecureDecryptor{})
	licensed, refusal := f.p.plainDerivationLicence(rs)
	require.False(t, licensed, "a component installed after the first answer must change the next one")
	require.Contains(t, refusal, "Secure Lens")

	f.p.SetSecureDecryptor(nil)
	licensed, _ = f.p.plainDerivationLicence(rs)
	require.True(t, licensed, "and removing it must restore the licence, not leave a latched refusal")
}

// TestPlainDerivationLicence_DoesNotReachTheActGate is the load-bearing
// invariant stated as a test: the licence exists, is answerable, and answers
// TRUE for the fixture lens — and the act gate still declines that same lens.
// Acting is the posture-changing increment (§12, Inc 4a), which owns the flip
// together with the zero-row Delete probe and the e2es; a licence wired in
// ahead of them would change write behaviour with none of that proof, and
// builtinDerivationMode is `act`, so nothing an operator does would be needed
// to reach it.
func TestPlainDerivationLicence_DoesNotReachTheActGate(t *testing.T) {
	f := licenceFixture(t, seedUnitsSpec)
	rs := f.p.ruleState()

	licensed, refusal := f.p.plainDerivationLicence(rs)
	require.True(t, licensed, "positive vector: this lens satisfies every conjunct; refusal: %s", refusal)

	_, ready := f.p.plainDerivationIndexForAct(rs)
	require.False(t, ready, "a licensed lens must STILL be declined by the act gate")
}
