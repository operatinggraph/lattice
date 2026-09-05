package pipeline

// Audit enrolment (lens-projection-divergence-audit-design.md §4.4;
// secure-plain-lens-retraction-and-audit-design.md §4.1): a series of
// fail-closed conjuncts, each a correctness requirement rather than a
// heuristic. Every one gets a negative case here, and the positive case sits
// at the top — without it a green refusal could equally come from a gate that
// refuses everything, which is the failure mode nobody would notice. A Secure
// Lens and a DiffRetraction lens both enrol (audit_secure_test.go's own
// positive cases) rather than refuse: neither conjunct this file once pinned
// survives the comparison actually going through executeFullForAudit's read
// path.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/stretchr/testify/require"
)

// enrolAudit runs the enrolment gate against a pipeline as it now stands — the
// same one-snapshot call InstallAudit and every pass make.
func enrolAudit(p *Pipeline, authPlane bool) (AuditPlan, string) {
	return auditEnrolment(p, p.ruleState(), p.currentAdapter(), authPlane)
}

// notARowReader wraps an adapter by DELEGATION rather than embedding, so the
// wrapped adapter's GetRow is not promoted through it — an embedded
// *NatsKVAdapter would hand the audit the real read-back and the conjunct under
// test would never fire (the trap adapter.OutcomeUpserter's own doc records).
type notARowReader struct{ inner adapter.Adapter }

func (a notARowReader) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	return a.inner.Upsert(ctx, keys, row, seq)
}
func (a notARowReader) Delete(ctx context.Context, keys map[string]any, seq uint64) error {
	return a.inner.Delete(ctx, keys, seq)
}
func (a notARowReader) Probe(ctx context.Context) error { return a.inner.Probe(ctx) }
func (a notARowReader) Close() error                    { return a.inner.Close() }

// TestAuditEnrolment_PositiveCase is the vector every refusal below is measured
// against: an ordinary plain, full-engine, NATS-KV lens enrols, and names its
// anchor.
func TestAuditEnrolment_PositiveCase(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	plan, refusal := enrolAudit(f.p, false)
	require.Empty(t, refusal)
	require.Equal(t, "unit", plan.AnchorLabel)
}

// TestAuditEnrolment_PostgresAdapterRowReader_Enrols is
// TestAuditEnrolment_PositiveCase's Postgres twin: the same lens shape enrols
// when its target is a plain (unguarded) *adapter.PostgresAdapter, not only
// the NATS-KV adapter above — the RowReader conjunct this fixture would
// otherwise refuse on (see "a target that cannot read a row back" below) now
// passes for both adapter families
// (refractor-hub-walk-and-periodic-load-design.md §5.2).
//
// The pool is built against a syntactically-valid but unreachable DSN:
// pgxpool.New never dials until a query runs, and this gate only
// type-asserts adapter.RowReader — it never calls GetRow — so no live
// Postgres is needed.
func TestAuditEnrolment_PostgresAdapterRowReader_Enrols(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	pg, err := adapter.NewPostgresAdapter(pool, "audit_pg_test", []string{"key"}, 0, adapter.DeleteModeHard)
	require.NoError(t, err)

	f := newAuditFixture(t, seedUnitsSpec, func(adapter.Adapter) adapter.Adapter { return pg })
	plan, refusal := enrolAudit(f.p, false)
	require.Empty(t, refusal)
	require.Equal(t, "unit", plan.AnchorLabel)
}

// TestAuditEnrolment_RefusesEachConjunct walks the gate one conjunct at a time.
// A refusal must always carry a REASON: a lens that is silently not audited is
// indistinguishable from one whose audit keeps finding nothing, which is the
// same silence the whole design exists to end.
func TestAuditEnrolment_RefusesEachConjunct(t *testing.T) {
	t.Run("disabled by deployment", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		prev := auditArmed
		auditArmed = false
		t.Cleanup(func() { auditArmed = prev })

		plan, refusal := enrolAudit(f.p, false)
		require.Equal(t, "disabled by deployment", refusal,
			"the kill switch routes through enrolment so the disabled state is PUBLISHED per lens rather than looking like a clean audit")
		require.Zero(t, plan)
	})

	t.Run("no single derivable anchor pattern", func(t *testing.T) {
		// An unlabeled anchor: no event type identifies it, so there is no key
		// type to enumerate and no seed to constrain an evaluation with.
		f := newAuditFixture(t, `
MATCH (u)
WHERE u.listing.data.status <> null
RETURN u.key AS key
`, nil)
		plan, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "no single derivable anchor pattern")
		require.Zero(t, plan)
	})

	t.Run("an anchor expanding to several concrete types", func(t *testing.T) {
		// A `*` taxonomy anchor resolves to a SET of concrete subtypes. One
		// key-type listing enumerates one of them, so auditing under a plan
		// naming a single label would silently cover a fraction of the corpus
		// while publishing a whole-lens verdict.
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.seedAnchorLabels = map[string]struct{}{"unit": {}, "dwelling": {}}
		plan, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "expands to several concrete types")
		require.Zero(t, plan)
	})

	t.Run("actor-aggregate: an envelope is installed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.SetEnvelopeFn(func(row, keys, _ map[string]any) (map[string]any, map[string]any, error) {
			return row, keys, nil
		})
		_, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "actor-aggregate or personal")
	})

	t.Run("actor-aggregate: a perEntry envelope is installed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.SetMultiEnvelopeFn(func(row, keys, _ map[string]any) ([]Envelope, error) {
			return nil, nil
		})
		_, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "actor-aggregate or personal")
	})

	t.Run("actor-aware: a fan-out enumerator is installed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.SetActorEnumerator(NewActorEnumerator(f.p.adjKV, f.coreKV, "identity"))
		_, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "actor-aggregate or personal")
	})

	t.Run("a target that cannot read a row back", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, func(a adapter.Adapter) adapter.Adapter {
			return notARowReader{inner: a}
		})
		_, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "cannot read a row back")
	})

	t.Run("a query referencing $now", func(t *testing.T) {
		// $now is wall-clock, so a recomputation legitimately differs from the
		// stored row and the lens would read divergent on every pass forever.
		f := newAuditFixture(t, `
MATCH (u:unit)
RETURN u.key AS key, $now AS observedAt
`, nil)
		_, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "returns $now")
	})

	t.Run("a query referencing $projectedAt", func(t *testing.T) {
		// $projectedAt derives from the EVENT vertex's provenance — a NEIGHBOR
		// of the anchor on the plain CDC path — which a seeded recompute
		// supplying the anchor's own props can never reproduce.
		f := newAuditFixture(t, `
MATCH (u:unit)
RETURN u.key AS key, $projectedAt AS at
`, nil)
		_, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "returns $projectedAt")
	})

	t.Run("a query shape the parameter walk cannot rule the parameter out of", func(t *testing.T) {
		// (referenced=false, exhaustive=false) is the accessor saying "I could
		// not tell". Reading that as an absence is the exact
		// read-the-declaration-not-the-matcher mistake the flag exists to
		// prevent, so it must refuse rather than pass.
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.fullCR = &full.CompiledRule{}
		_, refusal := enrolAudit(f.p, false)
		require.Contains(t, refusal, "could not be proven free of $now")
	})
}

// TestAuditEnrolment_RefusalInstallsAPublishedVerdict pins the shape a refused
// lens carries: a non-nil auditor holding Enrolled=false plus the reason, and no
// cadence — so it publishes its refusal, runs no pass, and can never read as
// audit-stalled.
func TestAuditEnrolment_RefusalInstallsAPublishedVerdict(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	f.p.SetActorEnumerator(NewActorEnumerator(f.p.adjKV, f.coreKV, "identity"))

	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.False(t, enrolled)
	require.NotEmpty(t, refusal)

	a := f.p.Auditor()
	require.NotNil(t, a, "a refusal is a published verdict, not an absence")
	st := a.Status()
	require.False(t, st.Enrolled)
	require.Equal(t, refusal, st.Refusal)
	require.Zero(t, a.Interval(), "no cadence means nothing to be late against, so no audit-stalled issue")

	// And RunAudit is inert for it: a cancelled context would stop a running
	// loop, but a refused lens must never reach the loop at all.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.p.RunAudit(ctx)
	require.True(t, a.Status().LastPassAt.IsZero())
}

// TestAuditEnrolment_RefusesTheAuthPlane is the conjunct with the sharpest
// consequence, and the one the other five do NOT cover between them.
//
// A plain-kind lens declaring `into: {target: nats_kv, bucket: capability-kv}`
// is not actor-aggregate (so the envelope conjunct passes it) and its target is
// NATS KV (so the RowReader conjunct passes it). Without this refusal it enrols,
// and a proven divergence on the authorization read model is then published by a
// detector built for business read models — which the capability heartbeat path
// would render as a bare metric. `capabilityRoleIndex` is exactly this shape and
// is excluded today only by two accidents of its own definition; either one
// changing would make the gap live.
//
// The verdict is passed IN (projection.IsAuthPlane is the one canonical
// derivation, and this package cannot import projection) — the derivation itself
// is pinned in the projection package's own test.
func TestAuditEnrolment_RefusesTheAuthPlane(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)

	// The positive control first: the same lens off the auth plane enrols, so
	// the refusal below is the plane and nothing else.
	plan, refusal := enrolAudit(f.p, false)
	require.Empty(t, refusal)
	require.Equal(t, "unit", plan.AnchorLabel)

	plan, refusal = enrolAudit(f.p, true)
	require.Contains(t, refusal, "auth plane")
	require.Zero(t, plan)

	// And it holds through the install path, so the lens carries a published
	// refusal rather than an auditor.
	enrolled, refusal := f.p.InstallAudit(AuditOptions{AuthPlane: true})
	require.False(t, enrolled)
	require.Contains(t, refusal, "auth plane")
	require.False(t, f.p.Auditor().Status().Enrolled)
}

// TestAuditEnrolment_DisarmedByDeployment pins the kill switch as a mechanism
// rather than a constant nobody can reach: SetAuditEnabled(false) makes every
// lens refuse with a published reason, and re-arming restores enrolment.
func TestAuditEnrolment_DisarmedByDeployment(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	t.Cleanup(func() { SetAuditEnabled(AuditEnabledByDefault) })

	SetAuditEnabled(false)
	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.False(t, enrolled)
	require.Equal(t, "disabled by deployment", refusal)

	SetAuditEnabled(true)
	enrolled, _ = f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled)
}

// TestInstallAudit_HonoursIntervalAndBatchOverrides pins the deployment levers
// §6.3 promises: a zero selects the default, a value overrides it.
func TestInstallAudit_HonoursIntervalAndBatchOverrides(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)

	enrolled, _ := f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled)
	require.Equal(t, DefaultAuditInterval, f.p.Auditor().Interval())

	enrolled, _ = f.p.InstallAudit(AuditOptions{Interval: 90 * time.Second, Batch: 3})
	require.True(t, enrolled)
	require.Equal(t, 90*time.Second, f.p.Auditor().Interval())
	require.Equal(t, 3, f.p.Auditor().batch)
}
