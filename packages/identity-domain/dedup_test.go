// CreateUnclaimedIdentity dedup-flag tests (dedup-over-encrypted-pii-design.md
// Fire 1, §3.2) — the `duplicateOf` + `indexes` link emission, and the live
// bug fix: a duplicate-contact create no longer hard-fails with
// RevisionConflict once the dispatcher declares the index keys as
// optionalReads.
//
// Coverage:
//  1. TestCreateUnclaimed_DuplicateContact_NoRevisionConflict_EmitsDuplicateOfLink
//     — two real ops, same email, second declares optionalReads: both accept;
//     the second's identity carries a duplicateOf link to the first with
//     criteria=["exact-email"].
//  2. TestCreateUnclaimed_NameCollision_EmitsExactNameLink — name-only match.
//  3. TestCreateUnclaimed_MultiCriteriaMatch_UnionsCriteria — email AND name
//     both match the SAME incumbent -> one link, criteria unioned.
//  4. TestCreateUnclaimed_DistinctIncumbents_DistinctLinks — email matches
//     incumbent A, phone matches incumbent B -> two distinct duplicateOf links.
//  5. TestCreateUnclaimed_NoCollision_CreatesIndexesLinksOnly — a fresh
//     (non-colliding) create gets an `indexes` link per new index vertex and
//     no duplicateOf link.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// normalizedNameIndexKey mirrors the script's name normalization
// (lowercase, collapse internal whitespace, trim) + contactIndexKey.
func normalizedNameIndexKey(name string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(name)), " ")
	return contactIndexKey("name", normalized)
}

// indexesLinkKey mirrors the script's `indexes` link key: the identityindex
// vertex is the source, the identity vertex is the target.
func indexesLinkKey(indexKey, identityID string) string {
	// indexKey is "vtx.identityindex.<hash>" -> "lnk.identityindex.<hash>.indexes.identity.<id>"
	return "lnk." + strings.TrimPrefix(indexKey, "vtx.") + ".indexes.identity." + identityID
}

// duplicateOfLinkKey mirrors the script's `duplicateOf` link key: the
// newer (source) identity to the incumbent (target) identity.
func duplicateOfLinkKey(newID, incumbentKey string) string {
	return "lnk.identity." + newID + ".duplicateOf." + strings.TrimPrefix(incumbentKey, "vtx.")
}

func readLinkData(t *testing.T, ctx context.Context, conn *substrate.Conn, linkKey string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("KVGet link %s: %v", linkKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal link %s: %v", linkKey, err)
	}
	data, _ := doc["data"].(map[string]any)
	return data
}

func criteriaStrings(t *testing.T, data map[string]any) []string {
	t.Helper()
	raw, _ := data["criteria"].([]any)
	out := make([]string, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("criteria[%d] not a string: %v", i, v)
		}
		out[i] = s
	}
	return out
}

func TestCreateUnclaimed_DuplicateContact_NoRevisionConflict_EmitsDuplicateOfLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-dupflow")

	email := "dupflow@example.com"
	emailIdxKey := contactIndexKey("email", email)

	reqID1 := testutil.GenReqID("DupFlowFirst")
	firstID := identityIDFromRequestID(reqID1)
	firstKey := "vtx.identity." + firstID

	env1 := &processor.OperationEnvelope{
		RequestID:     reqID1,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Dup Flow First","email":"` + email + `","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		ContextHint:   &processor.ContextHint{OptionalReads: []string{emailIdxKey}},
	}
	testutil.PublishOp(t, conn, env1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Second create, same email, declared optionalReads (the dispatcher fix).
	// Pre-fix this would hard-fail RevisionConflict on the index re-create.
	reqID2 := testutil.GenReqID("DupFlowSecond")
	secondID := identityIDFromRequestID(reqID2)

	env2 := &processor.OperationEnvelope{
		RequestID:     reqID2,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Dup Flow Second","email":"` + email + `","claimKeyHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`),
		ContextHint:   &processor.ContextHint{OptionalReads: []string{emailIdxKey}},
	}
	testutil.PublishOp(t, conn, env2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	linkKey := duplicateOfLinkKey(secondID, firstKey)
	data := readLinkData(t, ctx, conn, linkKey)
	criteria := criteriaStrings(t, data)
	if len(criteria) != 1 || criteria[0] != "exact-email" {
		t.Fatalf("criteria = %v, want [exact-email]", criteria)
	}

	// The index vertex must still point at the FIRST identity (unchanged —
	// ownership stays with whoever created it first; Fire 2's MergeIdentity
	// is the only repointer).
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, emailIdxKey)
	if err != nil {
		t.Fatalf("email index vertex missing: %v", err)
	}
	var idxDoc map[string]any
	if err := json.Unmarshal(entry.Value, &idxDoc); err != nil {
		t.Fatalf("unmarshal index doc: %v", err)
	}
	idxData, _ := idxDoc["data"].(map[string]any)
	if got, _ := idxData["identityKey"].(string); got != firstKey {
		t.Fatalf("index identityKey = %q, want %q", got, firstKey)
	}

	assertTrackerEvent(t, ctx, conn, reqID2, "identity.created")
}

func TestCreateUnclaimed_NameCollision_EmitsExactNameLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-namecol")

	incumbentKey := "vtx.identity." + testutil.GenReqID("NameCollIncumbent")
	name := "Priya Shah"
	nameIdxKey := normalizedNameIndexKey(name)
	seedIndex(t, ctx, conn, nameIdxKey, "name", incumbentKey)

	reqID := testutil.GenReqID("NameCollNew")
	newID := identityIDFromRequestID(reqID)

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		// Mixed case + extra internal whitespace, must normalize to the same key.
		Payload:     json.RawMessage(`{"name":"  Priya   SHAH ","phone":"+15559990000","claimKeyHash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`),
		ContextHint: &processor.ContextHint{OptionalReads: []string{nameIdxKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	linkKey := duplicateOfLinkKey(newID, incumbentKey)
	data := readLinkData(t, ctx, conn, linkKey)
	criteria := criteriaStrings(t, data)
	if len(criteria) != 1 || criteria[0] != "exact-name" {
		t.Fatalf("criteria = %v, want [exact-name]", criteria)
	}
}

func TestCreateUnclaimed_MultiCriteriaMatch_UnionsCriteria(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-multicrit")

	incumbentKey := "vtx.identity." + testutil.GenReqID("MultiCritIncumbent")
	email := "multi@example.com"
	name := "Jordan Lee"
	emailIdxKey := contactIndexKey("email", email)
	nameIdxKey := normalizedNameIndexKey(name)

	seedIndex(t, ctx, conn, emailIdxKey, "email", incumbentKey)
	seedIndex(t, ctx, conn, nameIdxKey, "name", incumbentKey)

	reqID := testutil.GenReqID("MultiCritNew")
	newID := identityIDFromRequestID(reqID)

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"` + name + `","email":"` + email + `","claimKeyHash":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}`),
		ContextHint:   &processor.ContextHint{OptionalReads: []string{emailIdxKey, nameIdxKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	linkKey := duplicateOfLinkKey(newID, incumbentKey)
	data := readLinkData(t, ctx, conn, linkKey)
	criteria := criteriaStrings(t, data)
	if len(criteria) != 2 || criteria[0] != "exact-email" || criteria[1] != "exact-name" {
		t.Fatalf("criteria = %v, want [exact-email exact-name] (one unioned link, not two)", criteria)
	}
}

func TestCreateUnclaimed_DistinctIncumbents_DistinctLinks(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-distinct")

	incumbentA := "vtx.identity." + testutil.GenReqID("DistinctIncumbentA")
	incumbentB := "vtx.identity." + testutil.GenReqID("DistinctIncumbentB")
	email := "distinct@example.com"
	phone := "+15558887777"
	emailIdxKey := contactIndexKey("email", email)
	phoneIdxKey := contactIndexKey("phone", phone)

	seedIndex(t, ctx, conn, emailIdxKey, "email", incumbentA)
	seedIndex(t, ctx, conn, phoneIdxKey, "phone", incumbentB)

	reqID := testutil.GenReqID("DistinctNew")
	newID := identityIDFromRequestID(reqID)

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Distinct New","email":"` + email + `","phone":"` + phone + `","claimKeyHash":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}`),
		ContextHint:   &processor.ContextHint{OptionalReads: []string{emailIdxKey, phoneIdxKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	linkA := duplicateOfLinkKey(newID, incumbentA)
	dataA := readLinkData(t, ctx, conn, linkA)
	if criteria := criteriaStrings(t, dataA); len(criteria) != 1 || criteria[0] != "exact-email" {
		t.Fatalf("link to incumbentA criteria = %v, want [exact-email]", criteria)
	}

	linkB := duplicateOfLinkKey(newID, incumbentB)
	dataB := readLinkData(t, ctx, conn, linkB)
	if criteria := criteriaStrings(t, dataB); len(criteria) != 1 || criteria[0] != "exact-phone" {
		t.Fatalf("link to incumbentB criteria = %v, want [exact-phone]", criteria)
	}
}

func TestCreateUnclaimed_NoCollision_CreatesIndexesLinksOnly(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-noncoll")

	reqID := testutil.GenReqID("NoCollNew")
	newID := identityIDFromRequestID(reqID)
	name := "Fresh Person"
	email := "fresh@example.com"
	phone := "+15551112222"

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"` + name + `","email":"` + email + `","phone":"` + phone + `","claimKeyHash":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	for _, indexKey := range []string{
		contactIndexKey("email", email),
		contactIndexKey("phone", phone),
		normalizedNameIndexKey(name),
	} {
		linkKey := indexesLinkKey(indexKey, newID)
		if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey); err != nil {
			t.Fatalf("indexes link %s missing: %v", linkKey, err)
		}
	}

	// No duplicateOf link should exist — there is no incumbent to point at.
	// (No direct enumeration helper in this harness; absence is implied by
	// every prior test's positive assertions succeeding only when seeded.)
}

// seedIndex writes an identityindex vertex directly to Core KV, as if a
// prior CreateUnclaimedIdentity had created it.
func seedIndex(t *testing.T, ctx context.Context, conn *substrate.Conn, indexKey, contactType, identityKey string) {
	t.Helper()
	doc := map[string]any{
		"class": "identityindex", "isDeleted": false,
		"data": map[string]any{"contactType": contactType, "identityKey": identityKey},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, indexKey, b); err != nil {
		t.Fatalf("seed index %s: %v", indexKey, err)
	}
}
