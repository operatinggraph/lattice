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

	"github.com/jackc/pgx/v5/pgxpool"
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
// It then seeds ONE anchor and runs one real pass, because a licensed lens is
// one whose audit is actually TICKING and the staleness conjunct reads that off
// the verdict clock. Both halves are load-bearing: a pass that compares no anchor
// reaches no verdict and leaves LastPassAt at zero (audit.go's record), so an
// unseeded fixture would be stale by construction and every licence below would
// refuse for a reason the test never named. The pass is driven rather than the
// timestamp written, for the same reason the suppression vector below drives a
// pause: the clock the licence reads must be the one the auditor itself stamps.
func licenceFixture(t *testing.T, spec string) *auditFixture {
	t.Helper()
	f := newAuditFixture(t, spec, nil)
	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled, "the fixture lens must enrol; refusal: %s", refusal)
	seedLicenceAnchor(t, f)
	f.p.Auditor().pass(context.Background())
	require.False(t, f.p.Auditor().Status().LastPassAt.IsZero(),
		"the fixture's audit must have reached a verdict, or every licence below refuses as stale")
	return f
}

// The fixture's own anchor and its landlord, distinct from auditUnitA so a test
// that seeds that one for its own purposes adds to the corpus rather than
// colliding with the fixture's.
const (
	licenceAnchor   = "LicenceanchorAAAAAAA"
	licenceLandlord = "LicenceLandLordAAAAA"
)

// seedLicenceAnchor puts one auditable anchor of the audited type into the
// corpus, so the auditor's pass has something to compare and can reach a
// verdict.
//
// It seeds the LANDLORD and the managedBy edge too, which every spec here binds
// even though only licenceNeighbourKeyedSpec keys on it. That spec's row key IS
// the landlord's key, so without the edge the audit's read-back is asked for a
// nil key, fails, and books the anchor unverified — leaving the pass with nothing
// verified and the verdict clock unstamped, which would refuse the licence as
// stale long before the closure conjunct that subtest exists to reach.
//
// The graph is written directly rather than through auditFixture.project: project
// drives a CDC event through the pipeline's write path, and two of these specs
// exist precisely because their rows do not key by this anchor. What the audit
// needs is an anchor it can enumerate and compare, which is a graph fact rather
// than a projected one.
func seedLicenceAnchor(t *testing.T, f *auditFixture) {
	t.Helper()
	key := "vtx.unit." + licenceAnchor
	seedVertexBody(t, f.coreKV, key, "unit", map[string]any{"name": "Licence anchor"})
	putBody(t, f.coreKV, key+".listing", aspectBody(key, "listing", map[string]any{"status": "active"}, false))

	landlordKey := "vtx.landlord." + licenceLandlord
	seedVertexBody(t, f.coreKV, landlordKey, "landlord", map[string]any{"name": "Licence landlord"})
	buildCollisionEdge(t, f.adjKV, "managedBy", "unit", licenceAnchor, "landlord", licenceLandlord)
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
		// The audit's OWN auth-plane refusal, passed only to InstallAudit and
		// deliberately never to SetAuthPlane — the licence's direct authPlane
		// conjunct (p.authPlane) therefore stays false, so the refusal below
		// can only be the auditor's.
		enrolled, _ := f.p.InstallAudit(AuditOptions{AuthPlane: true})
		require.False(t, enrolled)
		require.False(t, f.p.AuthPlane(), "the licence's own authPlane conjunct must stay unset")
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

	t.Run("a Protected Postgres adapter is licensed", func(t *testing.T) {
		// Swapped in the same way the negative case above does — AFTER
		// enrolment, so the auditor conjunct still passes and only the
		// RowReader check changes. leaseApplicationsRead
		// (packages/lease-signing/lenses.go) activates through exactly this
		// wrapper — Protected: true and plain (non-actorAggregate) — so its
		// RowReader status comes from ProtectedAdapter.GetRow delegating to
		// the inner *adapter.PostgresAdapter, not from a bare PostgresAdapter
		// directly (adapter.ProtectedAdapter wraps by a named field, not
		// embedding, so this is not implied by the base adapter's own
		// GetRow and needs its own coverage).
		//
		// The pool is a syntactically-valid but unreachable DSN — this gate
		// only type-asserts adapter.RowReader, never calls GetRow — so no
		// live Postgres is needed.
		f := licenceFixture(t, seedUnitsSpec)
		pool, err := pgxpool.New(context.Background(), "host=fake user=test")
		require.NoError(t, err)
		t.Cleanup(pool.Close)
		base, err := adapter.NewPostgresAdapter(pool, "licence_pg_test", []string{"key"}, 0, adapter.DeleteModeHard)
		require.NoError(t, err)
		protected, err := adapter.NewProtectedAdapter(base, nil, nil)
		require.NoError(t, err)
		require.NoError(t, f.p.HotReloadInto(protected))
		_, isReader := f.p.currentAdapter().(adapter.RowReader)
		require.True(t, isReader, "adapter.ProtectedAdapter must satisfy adapter.RowReader")

		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.True(t, licensed, "refusal: %s", refusal)
		require.Empty(t, refusal)
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

	// The positive twin: one pass over an anchor it can actually compare, and the
	// same lens is licensed.
	seedLicenceAnchor(t, f)
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

// TestPlainDerivationLicence_StaleRefusalIsStableAcrossTheWindow pins the
// precondition the caller's dedup latch rests on, in the one place it is easy to
// lose: a refusal string must be the SAME string at every moment of the
// condition it reports.
//
// noteStaticPlainDerivationRefusal logs at most once per DISTINCT reason, keyed
// on the string. A staleness window lasts hours by construction (auditorStaleCycles
// intervals), so a reason carrying an elapsed duration would be a new reason on
// every tick of whatever unit it rendered, and the latch would emit a line per
// neighbour event for as long as the condition persisted — the case it exists to
// prevent. The window is walked here at two wildly different depths and the
// string compared byte for byte.
func TestPlainDerivationLicence_StaleRefusalIsStableAcrossTheWindow(t *testing.T) {
	f := licenceFixture(t, seedUnitsSpec)
	a := f.p.Auditor()
	rs := f.p.ruleState()

	// The positive twin first: inside the window this lens is licensed, so the
	// refusals compared below are the staleness conjunct's own and not a gate
	// that refuses everything.
	licensed, refusal := f.p.plainDerivationLicence(rs)
	require.True(t, licensed, "refusal: %s", refusal)

	ageLastPass(a, (auditorStaleCycles+1)*a.Interval())
	licensed, justStale := f.p.plainDerivationLicence(rs)
	require.False(t, licensed)
	require.Contains(t, justStale, "has not reached a verdict")

	ageLastPass(a, 3*time.Hour)
	licensed, longStale := f.p.plainDerivationLicence(rs)
	require.False(t, licensed)
	require.Equal(t, justStale, longStale,
		"three hours deeper into the same window is the same refusal, or the once-per-reason latch fires on every event")

	// And the gate hands that same stable string to the note, so the latch really
	// does see one reason rather than a new one each time.
	_, _, gateRefusal := f.p.plainDerivationIndexForAct(rs)
	require.Equal(t, longStale, gateRefusal)
}

// TestPlainDerivationLicence_NeverAuditedRefusalCarriesNoDuration is the zero
// LastPassAt arm of the same rule. An auditor that has never completed a pass has
// no elapsed anyone can read — measured from the zero time it renders as a span
// of decades — so its refusal states what is true in words instead of computing
// one.
func TestPlainDerivationLicence_NeverAuditedRefusalCarriesNoDuration(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled, "refusal: %s", refusal)
	require.True(t, f.p.Auditor().Status().LastPassAt.IsZero(), "no pass has run")

	_, refusal = f.p.plainDerivationLicence(f.p.ruleState())
	require.Contains(t, refusal, "has not reached a verdict since it was installed")
	require.NotRegexp(t, `\d+h\d+m`, refusal,
		"a duration measured from the zero time must never reach an operator")

	// The positive twin: one pass over an anchor it can actually compare, and this
	// arm is no longer the one answering.
	seedLicenceAnchor(t, f)
	f.p.Auditor().pass(context.Background())
	licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
	require.True(t, licensed, "refusal: %s", refusal)
}

// TestPlainDerivationLicence_GatesTheActPath is the licence's load-bearing
// consequence stated as a test: the act gate admits a lens exactly while the
// licence does. The INDEX half is held ready across both halves of the pair and
// asserted at each, so the only thing that moves between "admitted" and
// "declined" is one licence conjunct — and the one chosen, the auth plane, is
// the licence's alone (plainDerivationIndex has no opinion about the plane).
func TestPlainDerivationLicence_GatesTheActPath(t *testing.T) {
	f := licenceFixture(t, seedUnitsSpec)
	rs := f.p.ruleState()

	licensed, refusal := f.p.plainDerivationLicence(rs)
	require.True(t, licensed, "positive vector: this lens satisfies every conjunct; refusal: %s", refusal)
	_, ready, gateRefusal := f.p.plainDerivationIndexForAct(rs)
	require.True(t, ready, "a licensed, indexable lens is what the act gate exists to admit")
	require.Empty(t, gateRefusal)

	f.p.SetAuthPlane(true)
	licensed, refusal = f.p.plainDerivationLicence(rs)
	require.False(t, licensed)
	require.Contains(t, refusal, "auth plane")
	_, indexReady := f.p.plainDerivationIndex(rs)
	require.True(t, indexReady, "the index half is untouched, so the gate's refusal below is the licence's")
	_, ready, gateRefusal = f.p.plainDerivationIndexForAct(rs)
	require.False(t, ready, "an auth-plane lens keeps today's unseeded whole-corpus rescan")
	require.Equal(t, refusal, gateRefusal, "and the gate reports the licence's own reason")

	// And the refusal lifts with the conjunct: the gate reads the licence per
	// event off live fields, never a verdict latched at the first answer.
	f.p.SetAuthPlane(false)
	_, ready, _ = f.p.plainDerivationIndexForAct(rs)
	require.True(t, ready)
}
