package pipeline

// Audit enrolment (lens-projection-divergence-audit-design.md §4.4): six
// fail-closed conjuncts, each a correctness requirement rather than a heuristic.
// Every one gets a negative case here, and the positive case sits at the top —
// without it a green refusal could equally come from a gate that refuses
// everything, which is the failure mode nobody would notice.

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/stretchr/testify/require"
)

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
	plan, refusal := auditEnrolment(f.p)
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

		plan, refusal := auditEnrolment(f.p)
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
		plan, refusal := auditEnrolment(f.p)
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
		plan, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "expands to several concrete types")
		require.Zero(t, plan)
	})

	t.Run("actor-aggregate: an envelope is installed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.SetEnvelopeFn(func(row, keys, _ map[string]any) (map[string]any, map[string]any, error) {
			return row, keys, nil
		})
		_, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "actor-aggregate or personal")
	})

	t.Run("actor-aggregate: a perEntry envelope is installed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.SetMultiEnvelopeFn(func(row, keys, _ map[string]any) ([]Envelope, error) {
			return nil, nil
		})
		_, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "actor-aggregate or personal")
	})

	t.Run("actor-aware: a fan-out enumerator is installed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.SetActorEnumerator(NewActorEnumerator(f.p.adjKV, f.coreKV, "identity"))
		_, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "actor-aggregate or personal")
	})

	t.Run("target-diff retraction", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		require.NoError(t, f.p.SetDiffRetraction(true))
		_, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "target-diff retraction")
	})

	t.Run("a target that cannot read a row back", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, func(a adapter.Adapter) adapter.Adapter {
			return notARowReader{inner: a}
		})
		_, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "cannot read a row back")
	})

	t.Run("a Secure Lens", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.SetSecureDecryptor(&SecureDecryptor{})
		_, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "Secure Lens")
	})

	t.Run("a query referencing $now", func(t *testing.T) {
		// $now is wall-clock, so a recomputation legitimately differs from the
		// stored row and the lens would read divergent on every pass forever.
		f := newAuditFixture(t, `
MATCH (u:unit)
RETURN u.key AS key, $now AS observedAt
`, nil)
		_, refusal := auditEnrolment(f.p)
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
		_, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "returns $projectedAt")
	})

	t.Run("a query shape the parameter walk cannot rule the parameter out of", func(t *testing.T) {
		// (referenced=false, exhaustive=false) is the accessor saying "I could
		// not tell". Reading that as an absence is the exact
		// read-the-declaration-not-the-matcher mistake the flag exists to
		// prevent, so it must refuse rather than pass.
		f := newAuditFixture(t, seedUnitsSpec, nil)
		f.p.fullCR = &full.CompiledRule{}
		_, refusal := auditEnrolment(f.p)
		require.Contains(t, refusal, "could not be proven free of $now")
	})
}

// TestAuditEnrolment_RefusalInstallsAPublishedVerdict pins the shape a refused
// lens carries: a non-nil auditor holding Enrolled=false plus the reason, and no
// cadence — so it publishes its refusal, runs no pass, and can never read as
// audit-stalled.
func TestAuditEnrolment_RefusalInstallsAPublishedVerdict(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	require.NoError(t, f.p.SetDiffRetraction(true))

	enrolled, refusal := f.p.InstallAudit()
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
