package augur

// Rule-engine proof of the package's two lenses, driven through the `full`
// engine — the one activation selects via engine:"full" — against an embedded
// NATS Core/Adjacency KV. Same harness shape as edge-manifest / clinic-domain's
// lens cypher tests.
//
// The two lenses read the same proposal vertex for opposite audiences, and each
// has one property structure tests cannot see:
//
//   - augurDispatchPending's `violating` column is what Weaver's lane-1 sweep
//     picks up, so it is a dispatch trigger wearing a boolean's clothes. It
//     must be DEFAULT-DENY: true for exactly the approved state, false for
//     pending / rejected / invalid / dispatched / superseded, and — the case
//     that is easy to get wrong — false for a claim whose reasoning is still
//     in flight, whose review state is not a value at all but absent. The
//     `full` engine's `=` compares null false rather than erroring, which is
//     precisely the behaviour being relied on and precisely the kind of thing
//     that changes underneath a spec silently.
//   - augurProposals is the operator's review surface, and it must render a
//     claim-in-flight rather than hide it: a proposal with no .proposed and no
//     .review aspect yet still projects, with null model columns. A spec whose
//     aspect hops were not null-safe would drop exactly the rows a human is
//     waiting on.
//
// candidateKey / targetMetaKey come from the TRUSTED .gap aspect the instanceOp
// minted write-ahead, never from the model's own reply — pinned below, because
// it is what gives the dispatch-time scope re-check a trustworthy anchor.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func augurCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-augur-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-augur-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-augur-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-augur-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// augurNanoID returns a deterministic 20-char Contract #1 NanoID from a logical
// name (the edge-manifest / wellness-domain helper's derivation).
func augurNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

type augurFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
}

func newAugurFixture(t *testing.T) *augurFixture {
	adjKV, coreKV := augurCypherKVs(t)
	return &augurFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}}
}

func (f *augurFixture) key(name string) string {
	return "vtx.augurproposal." + f.ids[name]
}

func (f *augurFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := f.key(ownerName)
	k := owner + "." + local
	body := map[string]any{"key": k, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), k, raw)
	require.NoError(t, err)
}

// claim mints a proposal vertex carrying only its .gap aspect — the state
// CreateAugurReasoningClaim leaves behind while the model is still reasoning.
func (f *augurFixture) claim(t *testing.T, name, candidate, targetMeta, gapColumn string) {
	t.Helper()
	id := augurNanoID(name)
	f.ids[name] = id
	key := "vtx.augurproposal." + id
	body := map[string]any{"key": key, "class": "augurproposal", "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	f.aspect(t, name, "gap", "gap", map[string]any{
		"entityId": candidate, "targetId": targetMeta, "gapColumn": gapColumn, "trigger": "convergence"})
}

// reasoned adds the model's remediation + provenance — what RecordProposal
// lands on top of a claim.
func (f *augurFixture) reasoned(t *testing.T, name, action string, params map[string]any) {
	t.Helper()
	f.aspect(t, name, "proposed", "proposed", map[string]any{"action": action, "params": params})
	f.aspect(t, name, "rationale", "rationale", map[string]any{"text": "the unit has no listing"})
	f.aspect(t, name, "confidence", "confidence", map[string]any{"score": 0.82})
	f.aspect(t, name, "provenance", "provenance", map[string]any{"model": "test-model", "reasonedAt": "2026-07-26T00:00:00Z"})
}

// reviewed stamps the verdict.
func (f *augurFixture) reviewed(t *testing.T, name, state string) {
	t.Helper()
	f.aspect(t, name, "review", "review", map[string]any{
		"state": state, "reviewedAt": "2026-07-26T01:00:00Z"})
}

func (f *augurFixture) project(t *testing.T, spec, actorKey string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "augur lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": actorKey, "now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

const (
	augurCandidate  = "vtx.leaseapp.NPmiaGL3sYCzk8qMTQAf"
	augurTargetMeta = "vtx.meta.Zt6dRxCq2VfEbkjWaHM9"
)

// ------------------------------------------------------------ dispatchPending

// dispatchStateFor projects augurDispatchPending for a single-proposal graph in
// the given review state, and returns the row's violating column. A nil state
// means the claim is still in flight — no .review aspect at all.
func dispatchStateFor(t *testing.T, state *string) map[string]any {
	t.Helper()
	f := newAugurFixture(t)
	f.claim(t, "p", augurCandidate, augurTargetMeta, "listingKey")
	f.reasoned(t, "p", "CreateListing", map[string]any{"unit": "vtx.unit.HgWq7RtYbn3ZmVcXpLdK"})
	if state != nil {
		f.reviewed(t, "p", *state)
	}
	rows := f.project(t, augurDispatchPendingSpec, f.key("p"))
	require.Len(t, rows, 1, "the convergence row is 1:1 with the proposal vertex; got %v", rows)
	return rows[0].Values
}

func TestAugurDispatchPending_ApprovedIsTheOnlyDispatchingState(t *testing.T) {
	approved := "approved"
	row := dispatchStateFor(t, &approved)

	require.Equal(t, true, row["violating"],
		"an approved proposal is the one thing Weaver's lane-1 sweep may pick up")
	require.Equal(t, true, row["missing_dispatch"])
}

func TestAugurDispatchPending_EveryOtherStateIsDefaultDeny(t *testing.T) {
	for _, state := range []string{"pending", "rejected", "invalid", "dispatched", "superseded"} {
		t.Run(state, func(t *testing.T) {
			s := state
			row := dispatchStateFor(t, &s)
			require.Equal(t, false, row["violating"],
				"%q must not dispatch — violating is a trigger, and anything but approved firing it hands the model's remediation to the platform unreviewed", state)
			require.Equal(t, false, row["missing_dispatch"])
		})
	}
}

func TestAugurDispatchPending_ClaimInFlightDoesNotDispatch(t *testing.T) {
	row := dispatchStateFor(t, nil)

	require.Equal(t, false, row["violating"],
		"a claim whose reasoning has not landed has no review state at all — the `full` engine's `=` must compare that absence false, not error and not match")
	require.Equal(t, false, row["missing_dispatch"])
}

func TestAugurDispatchPending_AnchorsTheDispatchOnTheTrustedGapAspect(t *testing.T) {
	approved := "approved"
	row := dispatchStateFor(t, &approved)

	require.Equal(t, augurCandidate, row["candidateKey"],
		"candidateKey comes from the instanceOp-minted .gap aspect, never the model's reply — it is the trusted anchor the dispatch-time scope re-check runs against")
	require.Equal(t, augurTargetMeta, row["targetMetaKey"])
	require.Equal(t, "listingKey", row["originGap"])

	require.Equal(t, "CreateListing", row["proposedAction"])
	require.Equal(t, map[string]any{"unit": "vtx.unit.HgWq7RtYbn3ZmVcXpLdK"}, row["proposedParams"],
		"the remediation projects verbatim as a map column — the reviewer approves exactly what would be dispatched")
}

func TestAugurDispatchPending_AnchorKeepsOtherProposalsOut(t *testing.T) {
	f := newAugurFixture(t)
	f.claim(t, "mine", augurCandidate, augurTargetMeta, "listingKey")
	f.reasoned(t, "mine", "CreateListing", map[string]any{})
	f.reviewed(t, "mine", "pending")

	f.claim(t, "other", "vtx.leaseapp.QRstuVWXyz12345abcde", augurTargetMeta, "listingKey")
	f.reasoned(t, "other", "CreateListing", map[string]any{})
	f.reviewed(t, "other", "approved")

	rows := f.project(t, augurDispatchPendingSpec, f.key("mine"))
	require.Len(t, rows, 1)
	require.Equal(t, f.key("mine"), rows[0].Values["entityKey"])
	require.Equal(t, false, rows[0].Values["violating"],
		"the {key: $actorKey} anchor must confine the row to its own proposal — a neighbouring approval must not dispatch this one")
}

// ---------------------------------------------------------------- proposals

func TestAugurProposals_ProjectsOneRowPerProposalWithTheFullAudit(t *testing.T) {
	f := newAugurFixture(t)
	f.claim(t, "reviewed", augurCandidate, augurTargetMeta, "listingKey")
	f.reasoned(t, "reviewed", "CreateListing", map[string]any{"unit": "vtx.unit.HgWq7RtYbn3ZmVcXpLdK"})
	f.reviewed(t, "reviewed", "pending")

	rows := f.project(t, augurProposalsSpec, "")
	require.Len(t, rows, 1, "flat one-row-per-proposal; got %v", rows)

	row := rows[0].Values
	require.Equal(t, f.key("reviewed"), row["key"])
	require.Equal(t, f.key("reviewed"), row["proposalKey"])
	require.Equal(t, augurCandidate, row["entityId"])
	require.Equal(t, augurTargetMeta, row["targetId"])
	require.Equal(t, "listingKey", row["gapColumn"])
	require.Equal(t, "convergence", row["trigger"])
	require.Equal(t, "CreateListing", row["proposedAction"])
	require.Equal(t, "the unit has no listing", row["rationale"])
	require.EqualValues(t, 0.82, row["confidence"])
	require.Equal(t, "test-model", row["model"])
	require.Equal(t, "pending", row["reviewState"])
}

func TestAugurProposals_ClaimInFlightProjectsWithNullModelColumns(t *testing.T) {
	f := newAugurFixture(t)
	// Only the .gap aspect exists — the model has not replied yet.
	f.claim(t, "inflight", augurCandidate, augurTargetMeta, "listingKey")

	rows := f.project(t, augurProposalsSpec, "")
	require.Len(t, rows, 1,
		"a claim still reasoning must still project — this is the row the operator watches; dropping it makes an in-flight claim indistinguishable from one that never existed")

	row := rows[0].Values
	require.Equal(t, augurCandidate, row["entityId"],
		"the trusted escalation context is written ahead of the reasoning, so it is readable throughout")
	for _, col := range []string{"proposedAction", "proposedParams", "rationale", "confidence", "model", "reasonedAt", "reviewState", "invalidReason", "reviewedAt", "dispatchedAt"} {
		require.Nil(t, row[col],
			"%s must project null on a not-yet-written aspect, not drop the row", col)
	}
}

func TestAugurProposals_ActivatesWithItsDefaultKeyColumn(t *testing.T) {
	eng := full.New()
	compiled, err := eng.Parse(augurProposalsSpec)
	require.NoError(t, err)
	cr, ok := compiled.(*full.CompiledRule)
	require.True(t, ok)
	cr.KeyColumns = []string{"key"}
	require.NoError(t, cr.ValidateKeyColumns(),
		"the lens must activate against its key column — the activation-time gate a mis-declared key dies on")
}
