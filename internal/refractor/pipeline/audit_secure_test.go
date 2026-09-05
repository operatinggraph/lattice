package pipeline

// Increment 1 of secure-plain-lens-retraction-and-audit-design.md — "the
// masked audit": a Secure Lens and a DiffRetraction lens both enrol in the
// divergence audit (audit_enrolment_test.go's TestAuditEnrolment_RefusesEachConjunct
// no longer refuses either), a Secure Lens's declared secure columns are
// excluded from the comparison rather than read as permanently `stale`, and a
// DiffRetraction lens's should-not-exist direction is checked only where its
// own closure supports it.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// seedUnitsWithSecureColumnSpec is seedUnitsSpec plus a secure column: `ssn`
// projects a sensitive aspect's ciphertext envelope (node.<aspect>.data), the
// shape a Secure Lens's declared column always takes before decryption. The
// filtering WHERE is unchanged, so this shape closureHolds exactly as
// seedUnitsSpec does (audit_test.go's own "retained" tests run on it).
const seedUnitsWithSecureColumnSpec = `
MATCH (u:unit)
WHERE u.listing.data.status <> null
RETURN u.key AS key, u.name AS name, u.ssn.data AS ssn
`

// auditDiffRetractionSpec is a DiffRetraction-shaped lens whose KEY COLUMN
// needs an aspect read (u.listing.data.slug) — the same shape
// TestAnchorProjectionKey_Contract's "aspect-dependent key column falls
// through" pins — so AnchorProjectionKey declines for every anchor of this
// lens: its should-not-exist direction (the audit's `retained` class) can
// never fire, exactly as §4.1 describes for most of this corpus's
// DiffRetraction lenses.
const auditDiffRetractionSpec = `
MATCH (u:unit)
WHERE u.listing.data.status <> null
RETURN u.listing.data.slug AS key, u.name AS name, u.listing.data.status AS status
`

// countingKeyLister wraps an adapter by DELEGATION (never embedding, for the
// same reason notARowReader does not: an embedded adapter would promote
// ListKeys and defeat the very thing under test), counting ListKeys calls —
// the one read applyDiffRetraction makes against the target. A test uses it to
// prove the audit's own recompute never reaches that function.
type countingKeyLister struct {
	inner adapter.Adapter
	calls *int
}

func (a countingKeyLister) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	return a.inner.Upsert(ctx, keys, row, seq)
}
func (a countingKeyLister) Delete(ctx context.Context, keys map[string]any, seq uint64) error {
	return a.inner.Delete(ctx, keys, seq)
}
func (a countingKeyLister) Probe(ctx context.Context) error { return a.inner.Probe(ctx) }
func (a countingKeyLister) Close() error                    { return a.inner.Close() }
func (a countingKeyLister) GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error) {
	return a.inner.(adapter.RowReader).GetRow(ctx, keys)
}
func (a countingKeyLister) ListKeys(ctx context.Context) ([]map[string]any, error) {
	*a.calls++
	return a.inner.(adapter.KeyLister).ListKeys(ctx)
}

// newSecureAuditFixture wires a SecureDecryptor for column "ssn" (holder type
// "unit", minted under the anchor's own vertex key — the audit never cares
// which real identity a column's ciphertext names, only that the column is
// declared) onto a fresh audit fixture built from spec.
func newSecureAuditFixture(t *testing.T, spec string) (f *auditFixture, dec *SecureDecryptor, ssnPlaintext string, ssnCiphertext map[string]any) {
	t.Helper()
	f = newAuditFixture(t, spec, nil)
	v := newTestVault(t)
	unitKey := "vtx.unit." + auditUnitA
	ssnPlaintext = "123-45-6789"
	ctMap, piiKeyDoc := mintIdentityPII(t, v, unitKey, map[string]any{"value": ssnPlaintext})
	_, err := f.coreKV.Put(context.Background(), unitKey+".piiKey", piiKeyDoc)
	require.NoError(t, err)
	dec, err = NewSecureDecryptor(v, f.coreKV, []SecureColumn{
		{Column: "ssn", HolderTypes: []string{"unit"}, Field: "value"},
	}, nil)
	require.NoError(t, err)
	f.p.SetSecureDecryptor(dec)
	return f, dec, ssnPlaintext, ctMap
}

// TestAuditEnrolment_SecureLensEnrolsUnderMask is §10's first Increment-1
// test: a Secure Lens enrols (the secureDecryptor refusal is gone),
// AuditPlan.MaskedColumns equals the decryptor's declared columns, and the
// mask is published on an enrolled lens's status — and on NOTHING for a
// refused one, since absence must keep meaning "not enrolled".
func TestAuditEnrolment_SecureLensEnrolsUnderMask(t *testing.T) {
	f, dec, _, _ := newSecureAuditFixture(t, seedUnitsWithSecureColumnSpec)

	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled, "a Secure Lens must enrol; refusal: %s", refusal)
	require.Equal(t, dec.Columns(), f.p.Auditor().MaskedColumns())
	require.Equal(t, dec.Columns(), f.p.Auditor().Status().MaskedColumns)

	// A lens refused on an unrelated conjunct (here: the auth plane) must
	// publish no masked columns at all, mirroring auditCoverageBasis and every
	// other field the enrolled branch alone carries.
	f2, _, _, _ := newSecureAuditFixture(t, seedUnitsWithSecureColumnSpec)
	enrolled2, refusal2 := f2.p.InstallAudit(AuditOptions{AuthPlane: true})
	require.False(t, enrolled2)
	require.Contains(t, refusal2, "auth plane")
	require.Empty(t, f2.p.Auditor().Status().MaskedColumns)
}

// TestAuditAnchor_MaskedColumnNeverReadsStale is §10's second Increment-1
// test. The row is projected through the REAL write path (SecureDecryptor.Apply
// runs inside evaluateForEntry, exactly as production does), so the stored
// row genuinely carries decrypted plaintext while a fresh audit recompute
// — which never decrypts — carries the raw ciphertext envelope for the SAME
// column. Unmasked, every row of a Secure Lens would read `stale` forever;
// masked, it must not.
func TestAuditAnchor_MaskedColumnNeverReadsStale(t *testing.T) {
	ctx := context.Background()
	f, _, ssnPlaintext, ssnCiphertext := newSecureAuditFixture(t, seedUnitsWithSecureColumnSpec)

	enrolled, refusal := f.p.InstallAudit(AuditOptions{Batch: 10})
	require.True(t, enrolled, "refusal: %s", refusal)
	a := f.p.Auditor()

	unitKey := "vtx.unit." + auditUnitA
	body := seedVertexBody(t, f.coreKV, unitKey, "unit", map[string]any{"name": "Unit A"})
	putBody(t, f.coreKV, unitKey+".listing", aspectBody(unitKey, "listing", map[string]any{"status": "active"}, false))
	putBody(t, f.coreKV, unitKey+".ssn", aspectBody(unitKey, "ssn", ssnCiphertext, false))
	handleVertexEvent(t, f.p, unitKey, body, 1)

	stored := targetRow(t, f.targetKV, unitKey)
	require.Equal(t, ssnPlaintext, stored["ssn"],
		"the write path decrypts before the row lands in target — this is production behaviour, not a hand-built fixture")

	a.pass(ctx)
	st := a.Status()
	require.Empty(t, st.Divergent, "the masked secure column must never read stale against its own plaintext")
	require.Zero(t, st.Unverified)
	require.Equal(t, 1, st.Audited)

	// A non-secure column divergence on the SAME row must still be caught —
	// the mask excludes only the declared secure columns, nothing else.
	corruptStoredRow(t, f.targetKV, unitKey)
	a.pass(ctx)
	st = a.Status()
	require.Equal(t, map[string]int{AuditClassStale: 1}, st.Divergent)

	// rowsComparable (unmasked — the sweep's own comparator) must still
	// disagree on this exact pair: proof the shared function is untouched.
	storedNow := targetRow(t, f.targetKV, unitKey)
	computed := map[string]any{"name": "Unit A", "ssn": ssnCiphertext}
	equal, comparable := rowsComparable(storedNow, computed)
	require.True(t, comparable)
	require.False(t, equal, "rowsComparable must still disagree — the audit's mask is a sibling, not an edit")
}

// TestAuditAnchor_SecureLensRetained is §10's third Increment-1 test: a
// Secure Lens's `retained` direction is unaffected by the mask — it is a KEY
// question (AnchorProjectionKey), never a content one, so it fires exactly as
// it does for a lens with no secure columns at all.
func TestAuditAnchor_SecureLensRetained(t *testing.T) {
	ctx := context.Background()
	f, _, _, ssnCiphertext := newSecureAuditFixture(t, seedUnitsWithSecureColumnSpec)

	enrolled, refusal := f.p.InstallAudit(AuditOptions{Batch: 10})
	require.True(t, enrolled, "refusal: %s", refusal)
	a := f.p.Auditor()

	unitKey := "vtx.unit." + auditUnitA
	body := seedVertexBody(t, f.coreKV, unitKey, "unit", map[string]any{"name": "Unit A"})
	putBody(t, f.coreKV, unitKey+".listing", aspectBody(unitKey, "listing", map[string]any{"status": "active"}, false))
	putBody(t, f.coreKV, unitKey+".ssn", aspectBody(unitKey, "ssn", ssnCiphertext, false))
	handleVertexEvent(t, f.p, unitKey, body, 1)
	before := f.revisions(t, unitKey)

	// Tombstone the filtering aspect WITHOUT the CDC event that would retract
	// the row — the lost-retraction shape audit_test.go's own "retained: the
	// anchor stopped matching" case drives — so the anchor is live but its
	// seeded evaluation now produces nothing.
	putBody(t, f.coreKV, unitKey+".listing", aspectBody(unitKey, "listing", map[string]any{"status": "active"}, true))

	a.pass(ctx)
	st := a.Status()
	require.Equal(t, map[string]int{AuditClassRetained: 1}, st.Divergent)
	require.Equal(t, before, f.revisions(t, unitKey), "the audit must not retract what it finds retained")
}

// TestAuditEnrolment_DiffRetractionLensEnrols is §10's fourth Increment-1
// test: a DiffRetraction lens enrols, its should-exist direction (missing /
// stale) works exactly as any other plain lens's, its should-not-exist
// direction (retained) is checked only where its own closure supports it —
// here it never does, because the key column needs an aspect read — and the
// audit's own recompute never reaches applyDiffRetraction.
func TestAuditEnrolment_DiffRetractionLensEnrols(t *testing.T) {
	ctx := context.Background()

	newFixture := func(t *testing.T) (*auditFixture, *Auditor, *int) {
		t.Helper()
		listKeysCalls := 0
		f := newAuditFixture(t, auditDiffRetractionSpec, func(a adapter.Adapter) adapter.Adapter {
			return countingKeyLister{inner: a, calls: &listKeysCalls}
		})
		require.NoError(t, f.p.SetDiffRetraction(true))
		enrolled, refusal := f.p.InstallAudit(AuditOptions{Batch: 10})
		require.True(t, enrolled, "a DiffRetraction lens must enrol; refusal: %s", refusal)
		return f, f.p.Auditor(), &listKeysCalls
	}
	seedOne := func(t *testing.T, f *auditFixture) {
		t.Helper()
		unitKey := "vtx.unit." + auditUnitA
		body := seedVertexBody(t, f.coreKV, unitKey, "unit", map[string]any{"name": "Unit A"})
		putBody(t, f.coreKV, unitKey+".listing",
			aspectBody(unitKey, "listing", map[string]any{"status": "active", "slug": "unit-a"}, false))
		handleVertexEvent(t, f.p, unitKey, body, 1)
	}

	t.Run("enrols, and the audit's own recompute never reaches applyDiffRetraction", func(t *testing.T) {
		f, a, listKeysCalls := newFixture(t)
		seedOne(t, f)

		*listKeysCalls = 0
		a.pass(ctx)
		require.Equal(t, 0, *listKeysCalls, "auditAnchor's recompute must never call applyDiffRetraction")
		require.Equal(t, 1, a.Status().Audited)
		require.Empty(t, a.Status().Divergent)
	})

	t.Run("stale: the should-exist direction still catches a content divergence", func(t *testing.T) {
		f, a, _ := newFixture(t)
		seedOne(t, f)

		corruptStoredRow(t, f.targetKV, "unit-a")
		a.pass(ctx)
		require.Equal(t, map[string]int{AuditClassStale: 1}, a.Status().Divergent)
	})

	t.Run("missing: the should-exist direction still catches an absent row", func(t *testing.T) {
		f, a, _ := newFixture(t)
		seedOne(t, f)

		require.NoError(t, f.targetKV.Delete(ctx, "unit-a"))
		a.pass(ctx)
		require.Equal(t, map[string]int{AuditClassMissing: 1}, a.Status().Divergent)
	})

	t.Run("retained never fires: the closure declines the should-not-exist direction", func(t *testing.T) {
		f, a, _ := newFixture(t)
		seedOne(t, f)
		before := f.revisions(t, "unit-a")

		// Tombstone the filtering aspect WITHOUT the CDC event that would
		// retract the row: the anchor is live, its seeded evaluation now
		// produces nothing, and the row it once owned is orphaned. This
		// lens's key column needs an aspect read (slug), so
		// AnchorProjectionKey declines and the should-not-exist direction is
		// never reached — the orphan is real and undetectable in this
		// direction, exactly as §4.1 describes.
		putBody(t, f.coreKV, "vtx.unit."+auditUnitA+".listing",
			aspectBody("vtx.unit."+auditUnitA, "listing", map[string]any{"status": "active", "slug": "unit-a"}, true))

		a.pass(ctx)
		st := a.Status()
		require.Empty(t, st.Divergent, "a DiffRetraction lens whose key needs an aspect read can never report retained")
		require.Equal(t, 1, st.Audited)
		require.Equal(t, before, f.revisions(t, "unit-a"))
	})
}

// TestAudit_PostgresKeyColumnNeverReadsStale_Integration is the Inc 1 premise
// pin (§15.1 row 1, §2.4): reproduced live against the shared dev stack's
// Postgres and the real full engine in the build note, pinned here end to end
// against a real *adapter.PostgresAdapter. adapter.PostgresAdapter.GetRow
// excludes the row's own key columns by contract (postgres.go's
// getRowPlatformColumns doc), while ruleengine.EvalResult.Row — the audit's
// freshly computed row — always carries every RETURN alias, key columns
// included. Before rowsComparableMasked excluded the key, this was a
// comparator artifact indistinguishable from a real divergence: it read every
// audited anchor of the shared stack's leaseApplicationsRead `stale`, pass
// after pass, though the graph and the stored row agreed on every column.
func TestAudit_PostgresKeyColumnNeverReadsStale_Integration(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if testing.Short() || dsn == "" {
		t.Skip("skipping Postgres integration test (POSTGRES_TEST_DSN not set, or -short)")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE audit_key_column_test ("key" TEXT PRIMARY KEY, name TEXT)`)
	require.NoError(t, err)
	pg, err := adapter.NewPostgresAdapter(pool, "audit_key_column_test", []string{"key"}, 5*time.Second, adapter.DeleteModeHard)
	require.NoError(t, err)

	f := newAuditFixture(t, `
MATCH (u:unit)
RETURN u.key AS key, u.name AS name
`, func(adapter.Adapter) adapter.Adapter { return pg })
	a := f.installAudit(t, 10)

	unitKey := "vtx.unit." + auditUnitA
	body := seedVertexBody(t, f.coreKV, unitKey, "unit", map[string]any{"name": "Loft A"})
	handleVertexEvent(t, f.p, unitKey, body, 1)

	row, present, err := pg.GetRow(ctx, map[string]any{"key": unitKey})
	require.NoError(t, err)
	require.True(t, present)
	_, hasKey := row["key"]
	require.False(t, hasKey,
		"Postgres GetRow excludes the key column by contract — this is the asymmetry the mask must close")

	a.pass(ctx)
	st := a.Status()
	require.Empty(t, st.Divergent,
		"the key column's presence in the computed row and its absence in the stored one must never read as content divergence")
	require.Zero(t, st.Unverified)
	require.Equal(t, 1, st.Audited)
}
