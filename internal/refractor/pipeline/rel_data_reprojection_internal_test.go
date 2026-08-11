package pipeline

// Link-payload projection liveness — the guard against a converged-but-wrong
// generator.
//
// A lens may project a fact that lives on the LINK (`r.data.<field>`: an
// attachment's filename, a binding's provenance). A projection is only worth
// having if the row FOLLOWS its source, so the question these tests answer is
// not "does the engine resolve the field" — the engine tests cover that — but
// "does a mutation of the link alone move the projected row". A feature whose
// re-trigger path does not exist converges on a stale answer and reports
// success while doing it.
//
// The mutation here is a data-only link update: no vertex is touched, no
// aspect is touched, and the link is neither created nor tombstoned. That is
// the event the KindLink dispatch arm has to carry, and the assertion is on
// the PROJECTED ROW's value — proving the event arrived would prove the
// delivery axis, not the projection axis.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// attachedFilesSpec projects a fact that exists ONLY on the relationship: the
// slot the object is attached under and the name it was uploaded as. The
// OPTIONAL MATCH keeps the anchor's row alive with null columns when the link
// goes, which is what makes a tombstone observable as a value change rather
// than as a retraction.
const attachedFilesSpec = `
MATCH (o:object)
OPTIONAL MATCH (o)-[r:attachedTo]->(owner:identity)
RETURN o.key AS key, type(r) AS slot, r.data.filename AS filename
`

// linkBodyWithData is the Contract #1 link envelope a Processor commit writes,
// carrying the payload a lens dereferences.
func linkBodyWithData(linkKey, sourceVertex, targetVertex, localName string, data map[string]any, deleted bool) map[string]any {
	return map[string]any{
		"key": linkKey, "class": "link", "isDeleted": deleted,
		"sourceVertex": sourceVertex, "targetVertex": targetVertex,
		"localName": localName, "data": data,
	}
}

// seedAttachedFile stands up one object attached to one identity, with the
// adjacency edge the walk crosses and the link document the payload lives in,
// and returns the object key, the link key, and the object's vertex body.
func seedAttachedFile(t *testing.T, coreKV, adjKV *substrate.KV, filename string) (string, string, []byte) {
	t.Helper()
	ctx := context.Background()
	const objID = "RDobjectAAAAAAAAAAAA"
	const idID = "RDidentityAAAAAAAAAA"
	objKey := "vtx.object." + objID
	idKey := "vtx.identity." + idID
	objBody := putBody(t, coreKV, objKey, map[string]any{
		"key": objKey, "class": "object", "isDeleted": false,
		"createdAt": "2026-08-11T10:00:00Z", "lastModifiedAt": "2026-08-11T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, idKey, map[string]any{
		"key": idKey, "class": "identity", "isDeleted": false,
		"createdAt": "2026-08-11T10:00:00Z", "lastModifiedAt": "2026-08-11T10:00:00Z",
		"data": map[string]any{},
	})
	linkKey := "lnk.object." + objID + ".attachedTo.identity." + idID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: linkKey, Name: "attachedTo",
		Direction: "outbound", NodeID: objID, OtherNodeID: idID, OtherType: "identity",
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: linkKey, Name: "attachedTo",
		Direction: "inbound", NodeID: idID, OtherNodeID: objID, OtherType: "object",
	}))
	putBody(t, coreKV, linkKey, linkBodyWithData(linkKey, objKey, idKey, "attachedTo",
		map[string]any{"filename": filename}, false))
	return objKey, linkKey, objBody
}

func projectedRow(t *testing.T, targetKV *substrate.KV, key string) map[string]any {
	t.Helper()
	entry, err := targetKV.Get(context.Background(), key)
	require.NoError(t, err)
	var row map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	return row
}

// TestPlainLens_LinkDataUpdate_MovesTheProjectedRow is the liveness proof: a
// write that touches ONLY the link's payload must move the projected value.
// Nothing else in the graph changes — the object's vertex body, its aspects
// and the adjacency edge are all untouched, and the link is neither created
// nor tombstoned, so a re-trigger path that keys on anything but the link
// itself leaves the row asserting yesterday's filename forever.
func TestPlainLens_LinkDataUpdate_MovesTheProjectedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, attachedFilesSpec, []string{"key"})
	objKey, linkKey, objBody := seedAttachedFile(t, coreKV, adjKV, "first.pdf")

	// 1. The anchor's own event projects the row, payload and all.
	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + objKey, Body: objBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	row := projectedRow(t, targetKV, objKey)
	require.Equal(t, "attachedTo", row["slot"])
	require.Equal(t, "first.pdf", row["filename"])

	// 2. A data-only link update: same key, same endpoints, same adjacency
	// entry, not a create and not a tombstone — only the payload moves.
	updated := linkBodyWithData(linkKey, objKey, "vtx.identity.RDidentityAAAAAAAAAA",
		"attachedTo", map[string]any{"filename": "second.pdf"}, false)
	body := putBody(t, coreKV, linkKey, updated)
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + linkKey, Body: body, Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	row = projectedRow(t, targetKV, objKey)
	require.Equal(t, "second.pdf", row["filename"],
		"a link-payload update must move the PROJECTED value — an arriving event that leaves the row "+
			"stale is a converged-but-wrong generator")
	require.Equal(t, "attachedTo", row["slot"], "the slot is unchanged by a payload write")
}

// TestPlainLens_LinkTombstone_NullsTheProjectedPayload is the other half: when
// the relationship goes, the facts that lived on it go with it. Under an
// OPTIONAL MATCH the anchor survives, so the observable is the columns
// falling to null rather than the row being retracted (a required MATCH
// retracts instead — pinned in filter_retraction_internal_test.go).
func TestPlainLens_LinkTombstone_NullsTheProjectedPayload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, attachedFilesSpec, []string{"key"})
	objKey, linkKey, objBody := seedAttachedFile(t, coreKV, adjKV, "first.pdf")

	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + objKey, Body: objBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Equal(t, "first.pdf", projectedRow(t, targetKV, objKey)["filename"])

	tombstone, err := json.Marshal(map[string]any{"key": linkKey, "isDeleted": true})
	require.NoError(t, err)
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + linkKey, Body: tombstone, Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	row := projectedRow(t, targetKV, objKey)
	require.Equal(t, objKey, row["key"], "the anchor survives — the relationship was OPTIONAL")
	require.Nil(t, row["filename"], "the payload lived on the link and goes with it")
	require.Nil(t, row["slot"])
}

// attachedFilesActorSpec is the same projection anchored per actor — the
// actor-aggregate shape the sweep reconciles one actor at a time.
const attachedFilesActorSpec = `
MATCH (o:object {key: $actorKey})
OPTIONAL MATCH (o)-[r:attachedTo]->(owner:identity)
RETURN o.key AS key, type(r) AS slot, r.data.filename AS filename
`

// TestSweepRecompute_MatchesTheCDCProjectedRow pins sweep parity for a
// link-payload projection: the row a swept recompute writes is byte-identical
// to the one the CDC path wrote. Both entry points run the same engine, so a
// read the engine performs is performed identically on both — but "identical
// by construction" is exactly the kind of claim that stops being true when one
// path grows a shortcut, and a swept row that disagreed with a projected one
// would make the sweep a corruption source rather than a healer.
//
// The row is destroyed out of band before the sweep runs, so the recompute has
// to rebuild it rather than confirm it — a converged no-op would satisfy the
// comparison while proving nothing.
func TestSweepRecompute_MatchesTheCDCProjectedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, attachedFilesActorSpec, []string{"key"})
	p.SetEnvelopeFn(func(row, keys, _ map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	objKey, _, objBody := seedAttachedFile(t, coreKV, adjKV, "first.pdf")

	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + objKey, Body: objBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	cdcEntry, err := targetKV.Get(ctx, objKey)
	require.NoError(t, err)
	require.Contains(t, string(cdcEntry.Value), "first.pdf")

	// The row is lost out of band — a target truncation, an operator delete,
	// the divergence the convergence sweep exists to repair.
	require.NoError(t, targetKV.Delete(ctx, objKey))
	_, err = targetKV.Get(ctx, objKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound)

	res, err := p.Reproject(ctx, objKey)
	require.NoError(t, err)
	require.True(t, res.Wrote, "the swept recompute must rebuild the lost row, not report it converged")

	sweptEntry, err := targetKV.Get(ctx, objKey)
	require.NoError(t, err)
	require.Equal(t, string(cdcEntry.Value), string(sweptEntry.Value),
		"a swept row and a CDC-projected row cannot disagree — they run the same engine over the same reads")
}

// TestActorAwareLens_LinkDataUpdate_MovesTheProjectedRow is the liveness proof
// on the arm this feature's only consumer actually runs.
//
// The plain arm and the actor-aware arm are two different dispatches of the
// same KindLink event, chosen by whether the lens installs an actor
// enumerator. `objectAttachments` — the lens that reads a link's payload —
// declares ProjectionKind actorAggregate, so its events go to
// evalLinkFanOut, not to evalPlainLinkReprojection. Proving the re-trigger on
// the plain arm alone would leave the consumer's own path resting on an
// argument, which is what "a converged-but-wrong generator" is made of.
func TestActorAwareLens_LinkDataUpdate_MovesTheProjectedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, attachedFilesActorSpec, []string{"key"})
	p.SetEnvelopeFn(func(row, keys, _ map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	p.SetActorEnumerator(NewActorEnumerator(adjKV, coreKV, "object"))
	objKey, linkKey, objBody := seedAttachedFile(t, coreKV, adjKV, "first.pdf")

	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + objKey, Body: objBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Equal(t, "first.pdf", projectedRow(t, targetKV, objKey)["filename"])

	updated := linkBodyWithData(linkKey, objKey, "vtx.identity.RDidentityAAAAAAAAAA",
		"attachedTo", map[string]any{"filename": "second.pdf"}, false)
	body := putBody(t, coreKV, linkKey, updated)
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + linkKey, Body: body, Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	require.Equal(t, "second.pdf", projectedRow(t, targetKV, objKey)["filename"],
		"the fan-out arm must move the PROJECTED value on a payload-only link write")
}

// TestActorAwareLens_LinkHardDelete_NullsTheProjectedPayload covers the
// tombstone shape the soft-delete test does not: a NATS DEL/PURGE arrives with
// an EMPTY body, so the arms cannot read `isDeleted` off it and decide from the
// message alone. A payload projection that survived only the soft form would
// keep asserting a filename for a link the store no longer holds.
func TestActorAwareLens_LinkHardDelete_NullsTheProjectedPayload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, attachedFilesActorSpec, []string{"key"})
	p.SetEnvelopeFn(func(row, keys, _ map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	p.SetActorEnumerator(NewActorEnumerator(adjKV, coreKV, "object"))
	objKey, linkKey, objBody := seedAttachedFile(t, coreKV, adjKV, "first.pdf")

	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + objKey, Body: objBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Equal(t, "first.pdf", projectedRow(t, targetKV, objKey)["filename"])

	require.NoError(t, coreKV.Delete(ctx, linkKey))
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + linkKey, Body: nil, Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	row := projectedRow(t, targetKV, objKey)
	require.Equal(t, objKey, row["key"], "the anchor survives — the relationship was OPTIONAL")
	require.Nil(t, row["filename"], "a hard-deleted link carries no payload")
	require.Nil(t, row["slot"])
}
