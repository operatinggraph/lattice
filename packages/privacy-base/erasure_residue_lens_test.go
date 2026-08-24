package privacybase

// Rule-engine proof of the identityErasureResidue lens
// (erasure-orchestration-design.md §7.1): drives the spec through the `full`
// engine — the engine selected at activation via engine:"full" — against an
// embedded NATS Core/Adjacency KV, the same harness the shredStatus cypher
// test and clinic-domain / lease-signing use.
//
// The lens is the SCHEDULER for the erasure's convergent tail: the
// identityErasureComplete weaverTarget dispatches a sweep op per open gap
// until every residue count reaches zero, then the seal. So the properties
// that matter here are the ones a wrong row would break silently:
//
//   - every direction BOTH sweep ops actually sweep is counted (an omitted arm
//     is residue no gap reports, under a seal written over it);
//   - a wide subject PROJECTS AT ALL — the staged fan-out exists because the
//     single-stage form is refused by the binding cap on exactly the
//     well-connected subjects this design exists to be able to erase;
//   - the seal gap opens last, and reopens on a re-shred.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type residueFixture struct {
	adjKV, coreKV *substrate.KV
}

func newResidueFixture(t *testing.T) *residueFixture {
	t.Helper()
	adjKV, coreKV := lenstest.KVs(t)
	return &residueFixture{adjKV: adjKV, coreKV: coreKV}
}

func (f *residueFixture) vtx(t *testing.T, typ, id string) string {
	t.Helper()
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *residueFixture) aspect(t *testing.T, ownerKey, local string, data map[string]any) {
	t.Helper()
	k := ownerKey + "." + local
	body := map[string]any{
		"key": k, "class": local, "vertexKey": ownerKey, "localName": local,
		"isDeleted": false, "data": data,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = f.coreKV.Put(context.Background(), k, raw)
	require.NoError(t, err)
}

// edge mirrors identity-domain's lens-cypher fixture: adjacency carries BOTH
// directions of every link, which is what lets one relation be counted from
// either endpoint.
func (f *residueFixture) edge(t *testing.T, name, fromType, fromID, toType, toID string) {
	t.Helper()
	ctx := context.Background()
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound",
		NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound",
		NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// project runs the lens the way the Refractor does for an actorAggregate lens:
// once per anchor, with $actorKey bound to that identity. Passing an actorKey
// the lens must NOT project (a non-erased identity) is how the anchor-predicate
// test proves the filter, since the keyed anchor already narrows to one vertex.
func (f *residueFixture) project(t *testing.T, actorKeys ...string) map[string]map[string]any {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(identityErasureResidueSpec)
	require.NoError(t, err, "identityErasureResidue cypher must parse on the full engine")
	byKey := map[string]map[string]any{}
	for _, ak := range actorKeys {
		rows, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
			"actorKey": ak, "now": now, "projectedAt": now,
		}}, f.adjKV, f.coreKV)
		require.NoError(t, err)
		for _, r := range rows {
			k, _ := r.Values["entityKey"].(string)
			byKey[k] = r.Values
		}
	}
	return byKey
}

// residueSubject seeds an erasure-requested identity with the named fan-out in
// each of the five swept directions.
type fanOut struct{ boundIn, boundOut, indexes, dupOut, dupIn int }

func (f *residueFixture) residueSubject(t *testing.T, name string, fan fanOut) string {
	t.Helper()
	sid := lenstest.NanoID(name)
	skey := f.vtx(t, "identity", sid)
	f.aspect(t, skey, "erasureRequested", map[string]any{
		"requestedAt": "2026-08-07T00:00:00Z", "shreddedAt": "2026-08-07T00:00:00Z",
	})
	f.aspect(t, skey, "piiKey", map[string]any{
		"shredded": true, "shreddedAt": "2026-08-07T00:00:00Z",
	})
	seed := func(n int, tag string, fn func(otherID string)) {
		for i := 0; i < n; i++ {
			fn(lenstest.NanoID(fmt.Sprintf("%s%s%d", name, tag, i)))
		}
	}
	// A credential bound TO the subject: lnk.identity.<credId>.boundTo.identity.<subject>.
	seed(fan.boundIn, "bi", func(o string) {
		f.vtx(t, "identity", o)
		f.edge(t, "boundTo", "identity", o, "identity", sid)
	})
	// The subject IS someone else's credential — the direction §7.1 omitted.
	seed(fan.boundOut, "bo", func(o string) {
		f.vtx(t, "identity", o)
		f.edge(t, "boundTo", "identity", sid, "identity", o)
	})
	seed(fan.indexes, "ix", func(o string) {
		f.vtx(t, "identityindex", o)
		f.edge(t, "indexes", "identityindex", o, "identity", sid)
	})
	// The subject arrived later and matched an incumbent.
	seed(fan.dupOut, "do", func(o string) {
		f.vtx(t, "identity", o)
		f.edge(t, "duplicateOf", "identity", sid, "identity", o)
	})
	// The subject IS the incumbent others matched against.
	seed(fan.dupIn, "di", func(o string) {
		f.vtx(t, "identity", o)
		f.edge(t, "duplicateOf", "identity", o, "identity", sid)
	})
	return skey
}

// tombstone is what a sweep op's mutation does to a link once the Refractor
// folds it: adjacency.Build with IsDeleted removes the edge outright
// (adjacency/builder.go upsertEdge → removeEdge), which is the mechanism the
// whole convergence rests on.
func (f *residueFixture) tombstone(t *testing.T, name, fromType, fromID, toType, toID string) {
	t.Helper()
	ctx := context.Background()
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound",
		NodeID: fromID, OtherNodeID: toID, OtherType: toType, IsDeleted: true}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound",
		NodeID: toID, OtherNodeID: fromID, OtherType: fromType, IsDeleted: true}))
}

func (f *residueFixture) seal(t *testing.T, subjectKey, sealedForShreddedAt string) {
	t.Helper()
	f.aspect(t, subjectKey, "erasure", map[string]any{
		"sealedAt": "2026-08-07T02:00:00Z", "sealedForShreddedAt": sealedForShreddedAt,
	})
}

func (f *residueFixture) asyncHalvesDone(t *testing.T, subjectKey string) {
	t.Helper()
	f.aspect(t, subjectKey, "piiKey", map[string]any{
		"shredded": true, "shreddedAt": "2026-08-07T00:00:00Z",
		"vaultKeyDestroyed": true, "vaultKeyDestroyedAt": "2026-08-07T01:00:00Z",
		"projectionsNullified": true, "projectionsNullifiedAt": "2026-08-07T01:05:00Z",
	})
}

// ---------------------------------------------------------------- the arms

// TestErasureResidueLens_CountsEveryDirectionBothSweepsSweep pins that
// UnbindIdentityCredentials sweeps boundTo in BOTH directions and
// PurgeIdentityDedupFootprint sweeps duplicateOf in both — §7.1's ratified spec
// counted boundTo inbound only and folded duplicateOf away entirely. Each arm
// carries a DISTINCT cardinality here so a build that cross-wires two of them
// cannot pass.
func TestErasureResidueLens_CountsEveryDirectionBothSweepsSweep(t *testing.T) {
	f := newResidueFixture(t)
	key := f.residueSubject(t, "residueAllArms", fanOut{boundIn: 3, boundOut: 2, indexes: 5, dupOut: 4, dupIn: 6})

	row := f.project(t, key)[key]
	require.NotNil(t, row, "an erasure-requested identity must project a row")

	require.EqualValues(t, 3, row["boundInResidue"])
	require.EqualValues(t, 2, row["boundOutResidue"], "the subject as someone else's credential is residue UnbindIdentityCredentials sweeps and §7.1 did not count")
	require.EqualValues(t, 5, row["indexResidue"])
	require.EqualValues(t, 4, row["duplicateOutResidue"])
	require.EqualValues(t, 6, row["duplicateInResidue"], "the subject as dedup INCUMBENT is live pair evidence naming an erased person")

	require.Equal(t, true, row["missing_credentialResidue"])
	require.Equal(t, true, row["missing_dedupResidue"])
	require.Equal(t, true, row["violating"])
}

// TestErasureResidueLens_EachArmAloneOpensItsGap is the mutation guard: a build
// that drops any ONE of the five arms still passes the all-arms test above for
// four of them, because the gap is an OR. Driving each direction alone proves
// every arm can open its own gap by itself.
func TestErasureResidueLens_EachArmAloneOpensItsGap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fan     fanOut
		gap     string
		countIn string
	}{
		{"boundTo inbound", fanOut{boundIn: 1}, "missing_credentialResidue", "boundInResidue"},
		{"boundTo outbound", fanOut{boundOut: 1}, "missing_credentialResidue", "boundOutResidue"},
		{"indexes inbound", fanOut{indexes: 1}, "missing_dedupResidue", "indexResidue"},
		{"duplicateOf outbound", fanOut{dupOut: 1}, "missing_dedupResidue", "duplicateOutResidue"},
		{"duplicateOf inbound", fanOut{dupIn: 1}, "missing_dedupResidue", "duplicateInResidue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResidueFixture(t)
			key := f.residueSubject(t, "residueArm", tc.fan)
			// Both async halves done and NO attestation written, so the seal's
			// field-diff term is independently TRUE. The only thing that can
			// hold missing_erasureSeal shut is the residue conjunction itself —
			// without this the assertion below passes for the wrong reason and
			// the whole residue block could be deleted from the spec unnoticed.
			f.asyncHalvesDone(t, key)

			row := f.project(t, key)[key]
			require.NotNil(t, row)
			require.EqualValues(t, 1, row[tc.countIn], "the arm's own count")
			require.Equal(t, true, row[tc.gap], "one live link in this direction alone must open its gap")
			require.Equal(t, true, row["violating"])
			require.Equal(t, false, row["missing_erasureSeal"],
				"the seal gap stays shut while any residue is live — the seal is the last gap")
		})
	}
}

// TestErasureResidueLens_WideSubjectProjectsAtAll is the reason the spec is
// staged. Written as five sibling OPTIONAL MATCH clauses in one stage — §7.1's
// ratified shape — the engine builds the cross product of every arm and this
// fixture reaches 5.76M bindings against the 1M cap, so the evaluation is
// REFUSED: no row, no gap, no dispatch, and the erasure never converges. The
// failure is silent and lands on exactly the well-connected subjects the whole
// design exists to be able to erase. The numbers are load-bearing — 64 is
// UnbindIdentityCredentials' SWEEP_LIMIT and 300 is past
// PurgeIdentityDedupFootprint's 256-key read page, both taken from the
// fixtures those ops' own convergence proofs use.
func TestErasureResidueLens_WideSubjectProjectsAtAll(t *testing.T) {
	f := newResidueFixture(t)
	key := f.residueSubject(t, "residueWide", fanOut{boundIn: 64, indexes: 300, dupOut: 300})

	row := f.project(t, key)[key]
	require.NotNil(t, row, "a wide subject must project — a refused evaluation is a silent non-erasure")
	require.EqualValues(t, 64, row["boundInResidue"])
	require.EqualValues(t, 300, row["indexResidue"])
	require.EqualValues(t, 300, row["duplicateOutResidue"])
	require.Equal(t, true, row["violating"])
}

// TestErasureResidueLens_ResidueFallsToZeroAsTheSweepsRun is the convergence
// property the whole design rests on, and nothing else here proves it: every
// other test reads a STATIC world, so a lens that could count live links but
// never notice them leaving would pass all of them while the erasure ran
// forever. It walks the real sweep order — UnbindIdentityCredentials inbound
// then outbound, then PurgeIdentityDedupFootprint's indexes → duplicateOf out →
// duplicateOf in, one class at a time as those ops commit them — and requires
// the count to fall, the gap to close only when its LAST class is drained, and
// the seal gap to open exactly once.
//
// The mechanism under test is adjacency's, not the lens's: a tombstoned link is
// REMOVED from the adjacency entry (adjacency/builder.go upsertEdge →
// removeEdge), so the arm stops binding it. If that ever changed to a
// soft-marked edge the traversal still followed, every count here would stick
// above zero and no erasure could ever seal — which is why this asserts the
// whole ladder rather than a single drop.
func TestErasureResidueLens_ResidueFallsToZeroAsTheSweepsRun(t *testing.T) {
	f := newResidueFixture(t)
	const name = "residueConverge"
	fan := fanOut{boundIn: 2, boundOut: 1, indexes: 2, dupOut: 1, dupIn: 2}
	key := f.residueSubject(t, name, fan)
	sid := lenstest.NanoID(name)
	f.asyncHalvesDone(t, key)

	other := func(tag string, i int) string {
		return lenstest.NanoID(fmt.Sprintf("%s%s%d", name, tag, i))
	}
	row := f.project(t, key)[key]
	require.Equal(t, true, row["missing_credentialResidue"])
	require.Equal(t, true, row["missing_dedupResidue"])
	require.Equal(t, false, row["missing_erasureSeal"], "the seal waits behind live residue")

	// UnbindIdentityCredentials: inbound first, outbound only once inbound is
	// exhausted (unbind_identity_credentials.go's sweep order).
	for i := 0; i < fan.boundIn; i++ {
		f.tombstone(t, "boundTo", "identity", other("bi", i), "identity", sid)
	}
	row = f.project(t, key)[key]
	require.EqualValues(t, 0, row["boundInResidue"])
	require.EqualValues(t, 1, row["boundOutResidue"])
	require.Equal(t, true, row["missing_credentialResidue"],
		"draining ONE direction must not close the credential gap — the other is still live")

	f.tombstone(t, "boundTo", "identity", sid, "identity", other("bo", 0))
	row = f.project(t, key)[key]
	require.Equal(t, false, row["missing_credentialResidue"], "both directions drained closes it")
	require.Equal(t, true, row["missing_dedupResidue"], "the dedup plane is untouched so far")

	// PurgeIdentityDedupFootprint: indexes → duplicateOf out → duplicateOf in.
	for i := 0; i < fan.indexes; i++ {
		f.tombstone(t, "indexes", "identityindex", other("ix", i), "identity", sid)
	}
	row = f.project(t, key)[key]
	require.EqualValues(t, 0, row["indexResidue"])
	require.Equal(t, true, row["missing_dedupResidue"],
		"this is increment 4's correction in executable form: indexes clearing first must NOT close the dedup gap while duplicateOf links still name an erased person")

	f.tombstone(t, "duplicateOf", "identity", sid, "identity", other("do", 0))
	row = f.project(t, key)[key]
	require.EqualValues(t, 2, row["duplicateInResidue"])
	require.Equal(t, true, row["missing_dedupResidue"], "the incumbent direction is still live")

	for i := 0; i < fan.dupIn; i++ {
		f.tombstone(t, "duplicateOf", "identity", other("di", i), "identity", sid)
	}
	row = f.project(t, key)[key]
	for _, c := range []string{"boundInResidue", "boundOutResidue", "indexResidue", "duplicateOutResidue", "duplicateInResidue"} {
		require.EqualValues(t, 0, row[c], "%s must reach zero", c)
	}
	require.Equal(t, false, row["missing_credentialResidue"])
	require.Equal(t, false, row["missing_dedupResidue"])
	require.Equal(t, true, row["missing_erasureSeal"], "with the residue drained the seal is finally schedulable")
	require.Equal(t, true, row["violating"])

	// The seal lands and the row goes quiet — the erasure is over.
	f.seal(t, key, "2026-08-07T00:00:00Z")
	row = f.project(t, key)[key]
	require.Equal(t, false, row["missing_erasureSeal"])
	require.Equal(t, false, row["violating"], "a completed erasure must stop the Weaver")
}

// ------------------------------------------------------- gaps and ordering

// TestErasureResidueLens_SealIsTheLastGap pins the ordering decision: the seal
// op's in-commit re-verification covers the residue classes, not the two async
// halves, so if the seal gap could open while vaultKeyDestroyed is still false
// an attestation could be written while the Vault still holds the key.
func TestErasureResidueLens_SealIsTheLastGap(t *testing.T) {
	t.Run("residue clear but async halves pending", func(t *testing.T) {
		f := newResidueFixture(t)
		key := f.residueSubject(t, "residueAsyncPending", fanOut{})

		row := f.project(t, key)[key]
		require.Equal(t, false, row["missing_credentialResidue"])
		require.Equal(t, false, row["missing_dedupResidue"])
		require.Equal(t, true, row["missing_vaultDestruction"])
		require.Equal(t, true, row["missing_projectionNullify"])
		require.Equal(t, false, row["missing_erasureSeal"],
			"no attestation may be scheduled while the Vault still holds the key")
		require.Equal(t, true, row["violating"])
	})

	t.Run("everything clear, seal not yet written", func(t *testing.T) {
		f := newResidueFixture(t)
		key := f.residueSubject(t, "residueReadyToSeal", fanOut{})
		f.asyncHalvesDone(t, key)

		row := f.project(t, key)[key]
		require.Equal(t, true, row["missing_erasureSeal"], "with every other gap closed the seal is what remains")
		require.Equal(t, true, row["violating"])
	})
}

// TestErasureResidueLens_FullyErasedRowIsQuiet is the positive vector for every
// negative above: a completely erased person must open NO gap, or the Weaver
// dispatches forever against a finished erasure.
func TestErasureResidueLens_FullyErasedRowIsQuiet(t *testing.T) {
	f := newResidueFixture(t)
	key := f.residueSubject(t, "residueComplete", fanOut{})
	f.asyncHalvesDone(t, key)
	f.seal(t, key, "2026-08-07T00:00:00Z")

	row := f.project(t, key)[key]
	require.NotNil(t, row)
	for _, gap := range []string{
		"missing_credentialResidue", "missing_dedupResidue",
		"missing_vaultDestruction", "missing_projectionNullify", "missing_erasureSeal",
	} {
		require.Equal(t, false, row[gap], "%s must be closed on a fully erased person", gap)
	}
	require.Equal(t, false, row["violating"], "a finished erasure must stop the Weaver, not loop it")
	require.Equal(t, "2026-08-07T02:00:00Z", row["sealedAt"])
}

// TestErasureResidueLens_ReshredReopensTheSeal is §5.5's cycle semantics, and
// the reason the field-diff reads the LIVE piiKey rather than the marker's copy
// of it.
//
// §5.5 specifies the reopen as `seal.sealedForShreddedAt <> piiKey.shreddedAt`.
// Diffing the marker's copy instead looks equivalent — SealIdentityForErasure
// refreshes it — but only if a re-seal actually runs, and §5.1's ratified step-2
// guard skips step 2 whenever the marker already carries a requestedAt. Nothing
// else refreshes the marker. So on a genuine re-shred of an already-completed
// erasure the marker still names cycle 1, a marker-diff reads equal, the row
// goes quiet, and the second cycle is never attested while the first cycle's
// sealedAt sits on it as though it were.
//
// This test therefore re-shreds for real — bumping piiKey.shreddedAt and
// clearing the two finalization booleans exactly as ShredIdentityKey does —
// and never touches the marker. Against a marker-diff build it fails.
func TestErasureResidueLens_ReshredReopensTheSeal(t *testing.T) {
	f := newResidueFixture(t)
	key := f.residueSubject(t, "residueReshred", fanOut{})
	f.asyncHalvesDone(t, key)
	f.seal(t, key, "2026-08-07T00:00:00Z") // cycle 1's attestation
	require.Equal(t, false, f.project(t, key)[key]["violating"], "cycle 1 is complete and quiet")

	// A second "forget me". ShredIdentityKey bumps the envelope's shreddedAt and
	// resets the finalization cycle; the marker is NOT rewritten.
	f.aspect(t, key, "piiKey", map[string]any{
		"shredded": true, "shreddedAt": "2026-08-07T09:00:00Z",
	})
	row := f.project(t, key)[key]
	require.Equal(t, true, row["violating"], "a re-shred must reopen the row")
	require.Equal(t, true, row["missing_vaultDestruction"], "the finalization cycle restarted")

	// Once the new cycle's async halves land, the stale attestation is what
	// remains — and it must be visible as a seal gap, not read as complete.
	f.aspect(t, key, "piiKey", map[string]any{
		"shredded": true, "shreddedAt": "2026-08-07T09:00:00Z",
		"vaultKeyDestroyed": true, "projectionsNullified": true,
	})
	row = f.project(t, key)[key]
	require.Equal(t, true, row["missing_erasureSeal"],
		"cycle 2 has no attestation — the seal gap must reopen off the LIVE piiKey.shreddedAt, not the marker's stale copy")
	require.Equal(t, true, row["violating"])
	require.Equal(t, "2026-08-07T09:00:00Z", row["shreddedAt"], "the live cycle discriminator must be projected")
	require.Equal(t, "2026-08-07T00:00:00Z", row["sealedForShreddedAt"])

	// Re-sealing for the new cycle closes it again.
	f.seal(t, key, "2026-08-07T09:00:00Z")
	require.Equal(t, false, f.project(t, key)[key]["violating"], "cycle 2 attested — quiet again")
}

// ------------------------------------------------------- the anchor predicate

// TestErasureResidueLens_AnchorsOnTheMarkerOnly proves the read model is an
// ERASURE ledger, not an identity inventory: only a person who invoked the
// right to be forgotten projects. A shredded-but-not-erased identity is the
// one that matters — retention-class key shredding will one day shred keys for
// non-erasure reasons, and those people are not being forgotten (§6).
func TestErasureResidueLens_AnchorsOnTheMarkerOnly(t *testing.T) {
	f := newResidueFixture(t)
	erased := f.residueSubject(t, "residueAnchored", fanOut{boundIn: 1})

	shreddedOnly := f.vtx(t, "identity", lenstest.NanoID("residueShredOnly"))
	f.aspect(t, shreddedOnly, "piiKey", map[string]any{"shredded": true, "shreddedAt": "2026-08-07T00:00:00Z"})
	plain := f.vtx(t, "identity", lenstest.NanoID("residuePlainIdent"))

	rows := f.project(t, erased, shreddedOnly, plain)
	require.Len(t, rows, 1, "only the erasure-requested identity may project; got %v", rows)
	require.NotNil(t, rows[erased])
	require.Nil(t, rows[shreddedOnly], "a shredded key is not an erasure request")
	require.Nil(t, rows[plain])
}

// ------------------------------------------- the Weaver's dispatch contract

// TestErasureResidueLens_DeclaresPagedSweepRetryCaps pins the dispatch-bounding
// columns §7.2 depends on. All three dispatched gaps are directOp, which the
// engine classifies as external outright, so §10.3 leaves the declared
// maxretries_<g> column as their ONLY bound — and the bound has to be sized to
// the op behind it. The engine's default budget of 3 is too small for either
// sweep: both page one bounded slice of residue per commit and re-open, so a
// wide subject legitimately needs many more than three dispatches to drain, and
// a budget of 3 would suppress the sweep partway through and strand the
// erasure. The caps here are sized to each op's own reach instead
// (retry_budget.go, and retry_budget_pin_test.go re-derives them from those
// ops' own sources), which is what keeps §10.8's promise reachable: a sweep
// that has genuinely stopped draining stops LOUDLY, as a GapBudgetExhausted
// standing issue, rather than re-dispatching unseen forever.
//
// inflight_<g> must stay absent. It is the wrong instrument for that job: it
// suppresses a dispatch while a remediation is genuinely in flight, and each of
// these ops is one synchronous commit with no such window — so as a constant
// false it would suppress nothing while making gapSuppressed
// (weaver/evaluator.go) decline the budget, leaving the gap with no bound of
// any kind. An absent column reads to exactly the same false through
// boolColumn, without the claim.
func TestErasureResidueLens_DeclaresPagedSweepRetryCaps(t *testing.T) {
	f := newResidueFixture(t)
	key := f.residueSubject(t, "residueDispatch", fanOut{boundIn: 1, indexes: 1})

	row := f.project(t, key)[key]
	for col, want := range map[string]int{
		"maxretries_credentialResidue": maxCredentialResidueRetries,
		"maxretries_dedupResidue":      maxDedupResidueRetries,
		"maxretries_erasureSeal":       maxErasureSealRetries,
	} {
		v, ok := row[col]
		require.Truef(t, ok, "%s must be DECLARED — an external gap's cap is its only bound", col)
		require.EqualValuesf(t, want, v, "%s must project the cap sized to its own op", col)
	}
	for _, col := range []string{"inflight_credentialResidue", "inflight_dedupResidue", "inflight_erasureSeal"} {
		_, ok := row[col]
		require.Falsef(t, ok, "%s must NOT be declared — a synchronous commit has no in-flight window to suppress on, "+
			"and the marker would decline the retry budget", col)
	}
}

// TestErasureResidueLens_ArmsSeedFromTheAnchorNotTheCorpus is the guard for the
// second way this lens could have failed silently at scale. matchPath seeds a
// path from its FIRST node and only walks adjacency when that node is already
// bound; an unbound, unlabeled head falls through to a whole-bucket ListKeys
// plus a point read per vertex. Written the natural way — `(c)-[:boundTo]->(i)`
// — the three inbound arms lead with the unbound neighbour, so each one reads
// the entire corpus on every reprojection and eventually refuses the evaluation
// on the binding cap. That is a silent non-erasure whose trigger is how big the
// deployment is, which nothing about the subject bounds.
//
// The proof shrinks the cap instead of growing the corpus: with a cap just above
// the subject's own fan-out, an anchor-seeded query is unaffected while a
// corpus-seeded one is refused by the bystanders alone. Every bystander here is
// an ordinary unrelated identity — none is linked to the subject — so a lens
// that only ever walks the anchor's adjacency cannot see them at all.
func TestErasureResidueLens_ArmsSeedFromTheAnchorNotTheCorpus(t *testing.T) {
	f := newResidueFixture(t)
	key := f.residueSubject(t, "residueSeeding", fanOut{boundIn: 2, indexes: 2, dupIn: 2})
	for i := 0; i < 400; i++ {
		f.vtx(t, "identity", lenstest.NanoID(fmt.Sprintf("residueBystander%d", i)))
	}

	eng := full.New().WithMaxBindings(64)
	cr, err := eng.Parse(identityErasureResidueSpec)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": key, "now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err,
		"the arms must seed from the bound anchor — a corpus-seeded arm blows the binding cap on unrelated vertices")
	require.Len(t, rows, 1)
	require.EqualValues(t, 2, rows[0].Values["boundInResidue"])
	require.EqualValues(t, 2, rows[0].Values["indexResidue"])
	require.EqualValues(t, 2, rows[0].Values["duplicateInResidue"])
}

// TestErasureResidueLens_IsShapedAsAConvergenceLens is the guard for the thing
// §7.1 got wrong. It specified this row into a private `privacy-erasure`
// bucket while §7.2 has the Weaver dispatch on its gaps — and the Weaver
// consumes exactly ONE bucket, resolving a target by the row-key PREFIX
// (weaver/engine.go WeaverTargetsBucket; weaver/registry.go's Target doc). A
// row anywhere else is a row the Weaver cannot see, so the whole gap table
// would have been undispatchable and nothing would have failed loudly — the
// erasure would simply never converge. This pins the quartet every shipped
// convergence lens carries, so a future edit cannot quietly re-privatise it.
func TestErasureResidueLens_IsShapedAsAConvergenceLens(t *testing.T) {
	var spec *pkgmgr.LensSpec
	for i := range Package.Lenses {
		if Package.Lenses[i].CanonicalName == "identityErasureResidue" {
			spec = &Package.Lenses[i]
		}
	}
	require.NotNil(t, spec, "the lens must be registered on the package")

	require.Equal(t, "weaver-targets", spec.Bucket, "the Weaver reads one bucket and this row has to be in it")
	require.Equal(t, "actorAggregate", spec.ProjectionKind)
	require.NotNil(t, spec.Output, "a convergence lens needs an Output descriptor or it projects no weaver row")
	require.Equal(t, ErasureCompleteTarget+".{actorSuffix}", spec.Output.OutputKeyPattern,
		"the row-key prefix IS the targetId the Weaver resolves the playbook through")
	require.Equal(t, "entityId", spec.Output.KeyColumn)
	require.Equal(t, "identity", spec.Output.AnchorType)

	// The cypher has to supply what that descriptor and the Weaver read.
	for _, projected := range []string{"AS actorKey", "AS entityKey", "nanoIdFromKey(i.key) AS entityId"} {
		require.Containsf(t, identityErasureResidueSpec, projected,
			"an actorAggregate convergence lens must project %s", projected)
	}

	// Every gap §7.2's table names, plus violating and each dispatched gap's
	// retry cap, must survive into the row body — a gap column the descriptor
	// drops is a gap the Weaver never sees, and a cap it drops is a cap that
	// never bounds anything: the Weaver reads the BODY, so a maxretries_<g>
	// projected by the cypher but absent from BodyColumns reads as uncapped.
	for _, col := range []string{
		"violating", "entityKey",
		"missing_credentialResidue", "missing_dedupResidue",
		"missing_vaultDestruction", "missing_projectionNullify", "missing_erasureSeal",
		"maxretries_credentialResidue", "maxretries_dedupResidue", "maxretries_erasureSeal",
	} {
		require.Containsf(t, spec.Output.BodyColumns, col,
			"BodyColumns must carry %s — §7.2's gap table dispatches on it", col)
	}

	// And the suppression marker must be absent from the BODY, not merely from
	// the cypher. Weaver's staleMark tests DECLAREDNESS, so a body column the
	// cypher never aliases is not inert — it lands as a declared, nil-valued
	// inflight_<g>, which is the uncapped-external shape the caps above exist
	// to keep this target out of.
	for _, col := range []string{
		"inflight_credentialResidue", "inflight_dedupResidue", "inflight_erasureSeal",
	} {
		require.NotContainsf(t, spec.Output.BodyColumns, col,
			"BodyColumns must NOT carry %s — declaring the marker declines the retry budget", col)
	}
}

// TestErasureResidueLens_ProjectsTheOperatorSurface keeps the per-class counts
// individually visible. The gaps fold them, but an operator (and the Loupe pane
// in §12 step 4) reading a stuck erasure needs to know WHICH class is stuck —
// the folded boolean cannot say.
func TestErasureResidueLens_ProjectsTheOperatorSurface(t *testing.T) {
	f := newResidueFixture(t)
	key := f.residueSubject(t, "residueSurface", fanOut{boundIn: 1, dupIn: 2})

	row := f.project(t, key)[key]
	require.Equal(t, key, row["entityKey"], "the weaverTarget's row.entityKey subject")
	require.Equal(t, "2026-08-07T00:00:00Z", row["requestedAt"])
	require.EqualValues(t, 1, row["boundInResidue"])
	require.EqualValues(t, 0, row["indexResidue"])
	require.EqualValues(t, 2, row["duplicateInResidue"])
	require.Nil(t, row["sealedAt"], "an unsealed erasure projects null, not a zero value")
}
