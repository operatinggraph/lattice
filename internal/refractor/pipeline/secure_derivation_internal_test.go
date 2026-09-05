package pipeline

// Increment 2 of secure-plain-lens-retraction-and-audit-design.md — "the licence
// admits a Secure Lens": the licence carries no Secure-column conjunct, so a
// Secure Lens that satisfies every OTHER conjunct reaches
// evaluatePlainDerivedAnchors, and the double-decrypt risk such a conjunct
// would guard against is closed in the wiring instead: the re-entry runs
// through evaluatePlainFromVertexRaw, and the OUTER evaluateForEntry wrapper is
// the one and only thing that decrypts.
//
// That wiring is invisible to every other test. A second decrypt of an
// already-decrypted column is Terminal, so Apply REDACTS the column to null and
// keeps the row (fork F2) — no error, no dropped event, a stored row that simply
// lost its PII. The decrypt COUNT does not move under that mutation: the inner,
// wrongly-decrypting call succeeds and increments it, and the outer wrapper's
// own call then fails secure.go's ciphertext-envelope type assertion before its
// own increment — one success, one no-op failure, the same total. So the pin
// is the count (exactly one vault call per secure column of each derived
// anchor's row) AND, separately, the stored plaintext and the zero-redaction
// assertion — those two are what actually fail under the mutation — measured
// on a licensed Secure Lens through the production handle path, on both
// producers of the re-entry.

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// secureNeighbourSpec is the neighbour-event producer's shape: anchor `unit`
// carrying a secure column, one REQUIRED hop to a landlord whose name the row
// projects. A landlord event names no unit, so it reaches
// evaluatePlainNeighbourEvent and the derived anchor set is what answers it.
const secureNeighbourSpec = `
MATCH (u:unit)-[:managedBy]->(l:landlord)
RETURN u.key AS key, u.name AS name, l.name AS landlordName, u.ssn.data AS ssn
`

// secureMultiPositionSpec is the seeded-multi-position producer's shape: the
// anchor label `unit` binds BOTH pattern positions, so an event on a vertex
// playing the far position seeds (its own type is the anchor label) and reaches
// evaluateSeededMultiPosition rather than the neighbour branch. Its projected
// secure column belongs to the ANCHOR, which is what makes a derived anchor's
// row — not the event vertex's — the one whose decrypt is being counted.
const secureMultiPositionSpec = `
MATCH (b:unit)-[:duplicateOf]->(a:unit)
RETURN b.key AS key, a.name AS dupName, b.ssn.data AS ssn
`

const (
	secureUnitA     = "SecureunitAAAAAAAAAA"
	secureUnitB     = "SecureunitBBBBBBBBBB"
	secureLandlordA = "SecureownerAAAAAAAAA"
)

// secureDerivationFixture is a licensed Secure Lens in `act` mode over a graph
// whose anchors each carry one encrypted column.
type secureDerivationFixture struct {
	*auditFixture
	vault     *vault.LocalBackend
	calls     *atomic.Uint64
	plaintext string
}

// newSecureDerivationFixture builds that lens: every unit in units gets a name,
// an `ssn` aspect holding a real ciphertext envelope and the piiKey aspect that
// opens it, and the pipeline gets a SecureDecryptor whose Vault.Decrypt calls
// are counted.
//
// It deliberately does NOT arm the audit or the derivation mode — the callers
// seed their own edges first, because the licence's staleness conjunct reads a
// verdict clock that only a pass over a comparable anchor stamps, and an anchor
// with no edge produces no row to compare.
func newSecureDerivationFixture(t *testing.T, spec string, units ...string) *secureDerivationFixture {
	t.Helper()
	f := &secureDerivationFixture{
		auditFixture: newAuditFixture(t, spec, nil),
		vault:        newTestVault(t),
		calls:        &atomic.Uint64{},
		plaintext:    "123-45-6789",
	}
	for i, id := range units {
		key := "vtx.unit." + id
		seedVertexBody(t, f.coreKV, key, "unit", map[string]any{"name": "unit " + id})
		ctMap, piiKeyDoc := mintIdentityPII(t, f.vault, key, map[string]any{"value": f.plaintext})
		_, err := f.coreKV.Put(context.Background(), key+".piiKey", piiKeyDoc)
		require.NoError(t, err, "unit %d", i)
		putBody(t, f.coreKV, key+".ssn", aspectBody(key, "ssn", ctMap, false))
	}
	dec, err := NewSecureDecryptor(f.vault, f.coreKV, []SecureColumn{
		{Column: "ssn", HolderTypes: []string{"unit"}, Field: "value"},
	}, f.calls)
	require.NoError(t, err)
	f.p.SetSecureDecryptor(dec)
	return f
}

// arm enrols the audit, drives one pass so the licence's verdict clock is
// stamped, flips the pipeline into `act` mode, and asserts the lens really is
// licensed — the positive vector without which every count below could equally
// come from a lens that never derived at all.
func (f *secureDerivationFixture) arm(t *testing.T) {
	t.Helper()
	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled, "a Secure Lens must enrol under the mask; refusal: %s", refusal)
	f.p.Auditor().pass(context.Background())
	require.False(t, f.p.Auditor().Status().LastPassAt.IsZero(),
		"the audit must have reached a verdict, or the licence refuses as stale")

	f.p.SetAnchorDerivationMode(DerivationModeAct)
	licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
	require.True(t, licensed, "the Secure Lens must be licensed; refusal: %s", refusal)
}

// storedRow reads one projected row back off the target.
func (f *secureDerivationFixture) storedRow(t *testing.T, key string) map[string]any {
	t.Helper()
	entry, err := f.targetKV.Get(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, entry, "no row stored at %s", key)
	var row map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	return row
}

// redactions reports how many secure columns this lens has projected as null
// because it could not resolve them — read off the health entry the pipeline
// itself writes, so the assertion is against what an operator would see.
func (f *secureDerivationFixture) redactions(t *testing.T) uint64 {
	t.Helper()
	entry, err := f.reporter.GetStatus(context.Background())
	require.NoError(t, err)
	return entry.SecureRedactions
}

// TestSecureDecryptor_DecryptCallsPerEvaluation is §10's mutation test: on a
// licensed Secure Lens, a re-entrant derived-anchor evaluation decrypts each
// derived anchor's secure columns EXACTLY ONCE, by the outer wrapper, and raises
// no redaction. Both producers of the re-entry are covered, because the swap
// that closes the seam is one line shared by both and either could be reached
// alone by a future dispatch change.
//
// Swapping evaluatePlainFromVertexRaw for evaluatePlainFromVertex at
// evaluatePlainDerivedAnchors' one re-entry does NOT move the decrypt-count
// assertion below: the inner call now decrypts and succeeds, and the outer
// wrapper's own decrypt then fails secure.go's ciphertext-envelope type
// assertion before it can increment the count — one success, one no-op
// failure, the same total. What actually fails under that mutation is the
// stored row's plaintext (redacted to null instead) and the zero-redaction
// assertion (a redaction is booked where none should be).
func TestSecureDecryptor_DecryptCallsPerEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}

	t.Run("a neighbour event decrypts each derived anchor's column once", func(t *testing.T) {
		f := newSecureDerivationFixture(t, secureNeighbourSpec, secureUnitA, secureUnitB)

		// Both units are managed by the one landlord, so a landlord event
		// derives both — the count under test is per DERIVED ANCHOR, and a
		// single-anchor fixture could not tell one decrypt per anchor from one
		// decrypt per event.
		landlordKey := "vtx.landlord." + secureLandlordA
		landlordBody := seedVertexBody(t, f.coreKV, landlordKey, "landlord",
			map[string]any{"name": "Secure landlord"})
		for _, id := range []string{secureUnitA, secureUnitB} {
			buildCollisionEdge(t, f.adjKV, "managedBy", "unit", id, "landlord", secureLandlordA)
		}

		// The rows are projected by each unit's OWN anchor event first: the
		// audit needs something to compare, and the re-entry's decrypt count is
		// only meaningful against a target the ordinary path has already
		// written correctly.
		for i, id := range []string{secureUnitA, secureUnitB} {
			key := "vtx.unit." + id
			body := seedVertexBody(t, f.coreKV, key, "unit", map[string]any{"name": "unit " + id})
			handleVertexEvent(t, f.p, key, body, uint64(10+i))
			require.Equal(t, f.plaintext, f.storedRow(t, key)["ssn"],
				"the ordinary anchor path must project plaintext before the derived path is asked")
		}
		f.arm(t)

		before := f.calls.Load()
		handleVertexEvent(t, f.p, landlordKey, landlordBody, 20)

		acted := f.p.AnchorDerivationShadow()
		require.Equal(t, int64(1), acted.Acted, "the landlord event must have been decided by the derived set")
		require.Equal(t, int64(2), acted.ActedAnchors, "both managed units are derived anchors")

		// Two derived anchors, one declared secure column each: two vault
		// calls total. This count PINS "one vault call per secure column of
		// each derived anchor's row" but does not itself DETECT a re-introduced
		// double decrypt: swapping the re-entry back to evaluatePlainFromVertex
		// still lands on two — the inner call decrypts and increments, the
		// outer wrapper's own call then fails the ciphertext-envelope type
		// assertion before it would have incremented. The two assertions below
		// are the detectors: the mutation redacts both rows to null and books
		// two redactions where zero are expected.
		require.Equal(t, uint64(2), f.calls.Load()-before,
			"one decrypt per derived anchor's secure column, and no more")
		for _, id := range []string{secureUnitA, secureUnitB} {
			require.Equal(t, f.plaintext, f.storedRow(t, "vtx.unit."+id)["ssn"],
				"a derived anchor's row must keep its plaintext through the re-entry")
		}
		require.Zero(t, f.redactions(t), "no secure column may be redacted by a re-entrant evaluation")
	})

	t.Run("a seeded multi-position event decrypts the derived anchor's column once", func(t *testing.T) {
		f := newSecureDerivationFixture(t, secureMultiPositionSpec, secureUnitA, secureUnitB)

		// unit B duplicates unit A: B is the row's anchor, A the far position no
		// engine-level seed reaches. An event on A is therefore SEEDED (its own
		// type is the anchor label) and takes evaluateSeededMultiPosition.
		buildCollisionEdge(t, f.adjKV, "duplicateOf", "unit", secureUnitB, "unit", secureUnitA)
		bKey := "vtx.unit." + secureUnitB
		aKey := "vtx.unit." + secureUnitA

		bBody := seedVertexBody(t, f.coreKV, bKey, "unit", map[string]any{"name": "unit " + secureUnitB})
		handleVertexEvent(t, f.p, bKey, bBody, 10)
		require.Equal(t, f.plaintext, f.storedRow(t, bKey)["ssn"])
		f.arm(t)
		require.True(t, f.p.seedMultiPosition(f.p.ruleState(), "unit"),
			"the fixture must take the seeded-multi-position branch, not the neighbour one")

		before := f.calls.Load()
		aBody := seedVertexBody(t, f.coreKV, aKey, "unit", map[string]any{"name": "renamed " + secureUnitA})
		handleVertexEvent(t, f.p, aKey, aBody, 20)

		require.Equal(t, int64(1), f.p.AnchorDerivationShadow().Acted,
			"the event on the far position must have been decided by the derived set")

		// The derived set is {A, B}: A seeded at the anchor position as a
		// zero-hop terminus, B by walking the duplicateOf hop from it. Only B
		// projects a row — A duplicates nothing — so exactly one row carries a
		// secure column and exactly one vault call is owed. As above, this
		// count alone would not catch a re-introduced double decrypt (the
		// outer wrapper's own call fails before it increments); the plaintext
		// and zero-redaction assertions below are what would.
		require.Equal(t, uint64(1), f.calls.Load()-before,
			"one decrypt for the one derived anchor that projects a row")
		row := f.storedRow(t, bKey)
		require.Equal(t, f.plaintext, row["ssn"],
			"the derived anchor's row must keep its plaintext through the re-entry")
		require.Equal(t, "renamed "+secureUnitA, row["dupName"],
			"and must carry the far position's new value, which is what made the event matter")
		require.Zero(t, f.redactions(t), "no secure column may be redacted by a re-entrant evaluation")
	})
}

// secureGatedNeighbourSpec is the drop-out shape: the row exists only while the
// neighbour's own status says so, so an ordinary landlord event — no tombstone,
// no link change — removes every managed unit from the matched set at once. The
// adjacency is untouched, which is what keeps the derived set the full size the
// cap has to be measured against.
const secureGatedNeighbourSpec = `
MATCH (u:unit)-[:managedBy]->(l:landlord)
WHERE l.status = 'active'
RETURN u.key AS key, l.name AS landlordName, u.ssn.data AS ssn
`

const (
	secureUnitC = "SecureunitCCCCCCCCCC"
	secureUnitD = "SecureunitDDDDDDDDDD"
)

// TestPlainDerivation_OverCapFallsBackVisibly is §10's over-cap pin. A derived
// set larger than the cap is a FALLBACK, not a truncation: the event takes the
// unseeded whole-corpus rescan, whose upsert-only result set names no key that
// dropped out — so every affected row is left standing as an orphan.
//
// That outcome is correct and is also invisible, which is the whole reason the
// two health fields exist. The pins are therefore in three parts: the counters
// an operator reads (a fall-back happened, and the derived set was this big),
// the orphan itself, and the audit verdict that is the orphan's only detector.
// The positive vector closes it — the same event under a cap that admits the
// set retracts all three rows, so what stood them up was the fall-back and not
// the lens's shape.
func TestPlainDerivation_OverCapFallsBackVisibly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	units := []string{secureUnitA, secureUnitC, secureUnitD}
	f := newSecureDerivationFixture(t, secureGatedNeighbourSpec, units...)

	landlordKey := "vtx.landlord." + secureLandlordA
	seedVertexBody(t, f.coreKV, landlordKey, "landlord",
		map[string]any{"name": "Secure landlord", "status": "active"})
	for _, id := range units {
		buildCollisionEdge(t, f.adjKV, "managedBy", "unit", id, "landlord", secureLandlordA)
	}
	for i, id := range units {
		key := "vtx.unit." + id
		body := seedVertexBody(t, f.coreKV, key, "unit", map[string]any{"name": "unit " + id})
		handleVertexEvent(t, f.p, key, body, uint64(10+i))
		require.Equal(t, f.plaintext, f.storedRow(t, key)["ssn"])
	}
	f.arm(t)

	// Two is below the three units the landlord manages, so the derived set is
	// genuinely computed and genuinely refused for size. plainDerivedAnchorCap
	// folds any override <= 0 to the package default
	// (DefaultPlainDerivedAnchorCap, 64), so "a cap of zero" names no distinct
	// refused branch — it is simply "no override" — which is why the positive
	// vector below sets its own explicit cap rather than leaning on that
	// default.
	f.p.SetPlainDerivedAnchorCap(2)

	deactivated := seedVertexBody(t, f.coreKV, landlordKey, "landlord",
		map[string]any{"name": "Secure landlord", "status": "dormant"})
	handleVertexEvent(t, f.p, landlordKey, deactivated, 30)

	status := f.p.PlainDerivationStatus()
	require.True(t, status.Armed, "an act-mode plain lens publishes its tally")
	require.Equal(t, uint64(1), status.FellBack, "the over-cap event is a fall-back, counted")
	require.Equal(t, 3, status.OverCapSize, "and the size that was refused is what an operator sizes the cap from")
	require.Zero(t, f.p.AnchorDerivationShadow().Acted, "nothing was decided by the derived set")

	for _, id := range units {
		require.Equal(t, f.plaintext, f.storedRow(t, "vtx.unit."+id)["ssn"],
			"the rescan retracts nothing, so every row is left standing as an orphan")
	}

	// The audit is the orphan's only detector: the anchor is live and its seeded
	// recompute produces no row, while the target still holds one. Re-armed
	// through InstallAudit rather than a hand-built AuditPlan, so its
	// MaskedColumns is the one auditEnrolment actually derives for a Secure
	// Lens (SecureDecryptor.Columns()) — a hand-built plan that leaves it nil
	// would compare the row's own ssn against a recompute that never decrypts
	// it, so this pass would run unmasked and not the production shape.
	enrolled, refusal := f.p.InstallAudit(AuditOptions{Batch: 10, Interval: time.Hour})
	require.True(t, enrolled, "refusal: %s", refusal)
	f.p.Auditor().pass(context.Background())
	require.Equal(t, len(units), f.p.Auditor().Status().Divergent[AuditClassRetained],
		"every orphan the fall-back left must be reported retained")

	// The positive vector: the same drop-out under an explicit cap comfortably
	// above the three units — never the bare package default, which a future
	// change to DefaultPlainDerivedAnchorCap could move out from under this
	// assertion — retracts all three, so the orphans above are the fall-back's
	// doing.
	f.p.SetPlainDerivedAnchorCap(10)
	t.Cleanup(func() { f.p.SetPlainDerivedAnchorCap(0) })
	settled := seedVertexBody(t, f.coreKV, landlordKey, "landlord",
		map[string]any{"name": "Secure landlord", "status": "closed"})
	handleVertexEvent(t, f.p, landlordKey, settled, 40)
	require.Equal(t, int64(1), f.p.AnchorDerivationShadow().Acted)
	for _, id := range units {
		entry, err := f.targetKV.Get(context.Background(), "vtx.unit."+id)
		require.ErrorIs(t, err, substrate.ErrKeyNotFound, "row %s must be retracted; entry=%v", id, entry)
	}
}

// TestPlainDerivationStatus_ArmedOnlyWhereTheDerivationDecides is the presence
// gate's own pin, and the reason Eligible is not just "act mode and plain"
// while Armed is not simply Eligible restated: a static licence refusal is
// deliberately NOT counted as a fall-back (plainDerivationDecide), so an
// unlicensed lens's tally sits at zero for ever — the same reading a licensed
// lens gives when the transport has held every time. Only Armed tracks the
// licence; Eligible tracks the index alone and must stay true exactly while
// the AST-derived shape holds, whatever the licence is doing — and the tally
// must stay readable through that split, because a count that has already
// accrued does not belong to the licence.
//
// The vectors are walked in the order a lens actually passes through them, and
// the licensed one is asserted first so a green refusal below cannot come from
// a fixture that was never eligible.
func TestPlainDerivationStatus_ArmedOnlyWhereTheDerivationDecides(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	f := newSecureDerivationFixture(t, secureNeighbourSpec, secureUnitA)
	landlordKey := "vtx.landlord." + secureLandlordA
	seedVertexBody(t, f.coreKV, landlordKey, "landlord", map[string]any{"name": "Secure landlord"})
	buildCollisionEdge(t, f.adjKV, "managedBy", "unit", secureUnitA, "landlord", secureLandlordA)
	key := "vtx.unit." + secureUnitA
	handleVertexEvent(t, f.p, key, seedVertexBody(t, f.coreKV, key, "unit", map[string]any{"name": "unit A"}), 10)
	f.arm(t)

	status := f.p.PlainDerivationStatus()
	require.True(t, status.Eligible, "a licensed act-mode Secure Lens is eligible")
	require.True(t, status.Armed, "and, with the licence holding, armed")

	// A fall-back recorded before the licence moves must still read back
	// afterward: the counters are not the licence's to hide.
	f.p.recordDerivationFellBack(false)
	require.Equal(t, uint64(1), f.p.PlainDerivationStatus().FellBack)

	// The licence alone moves: the mode stays `act` and the index stays ready,
	// so what Armed must follow is the conjunct that actually decided, while
	// Eligible — the index's own verdict — does not move with it.
	f.p.SetAuthPlane(true)
	licensed, _ := f.p.plainDerivationLicence(f.p.ruleState())
	require.False(t, licensed)
	status = f.p.PlainDerivationStatus()
	require.True(t, status.Eligible,
		"the index half is untouched by an auth-plane licence refusal — this lens's shape can still support the transport")
	require.False(t, status.Armed,
		"but an unlicensed lens must not read armed — the transport is declared, currently off")
	require.Equal(t, uint64(1), status.FellBack,
		"the count already accrued must stay readable while the licence refuses the lens")
	f.p.SetAuthPlane(false)

	// And the mode, with the licence restored, for the other end of the gate:
	// outside act mode there is no index to be ready, so Eligible follows the
	// mode too.
	f.p.SetAnchorDerivationMode(DerivationModeOff)
	status = f.p.PlainDerivationStatus()
	require.False(t, status.Eligible, "a lens outside act mode never consults a derived set")
	require.False(t, status.Armed)
	f.p.SetAnchorDerivationMode(DerivationModeAct)
	status = f.p.PlainDerivationStatus()
	require.True(t, status.Eligible)
	require.True(t, status.Armed, "restoring the mode restores the tally, so neither half latches")
}
