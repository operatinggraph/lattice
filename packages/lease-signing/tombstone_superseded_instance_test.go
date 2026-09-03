// TombstoneSupersededLeaseServiceInstance op tests through the real install +
// Processor pipeline (bgcheck-runaway-and-broad-filter-design.md §6). External
// test package, mirroring lease_signing_test.go's shape and helpers.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seedServiceInstance submits CreateLeaseServiceInstance (as Loom's relay
// actor) for handle/family/subjectKey, then — when status is non-empty —
// RecordLeaseServiceOutcome(status) at submittedAt. RecordLeaseServiceOutcome
// derives completedAt = canonical-UTC(op.submittedAt), so two calls with
// distinct FIXED submittedAt values give two instances a deterministic,
// strictly-ordered completedAt with no real-clock dependency (CLAUDE.md's
// determinism rule). status == "" leaves the instance outcome-less (not yet
// complete). Returns the full vtx.service.<handle> key.
func seedServiceInstance(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, handle, family, subjectKey, submittedAt, status string) string {
	t.Helper()
	instKey := "vtx.service." + handle
	createEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(tag + "c"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseServiceInstance",
		Actor:         bootstrap.LoomIdentityKey,
		SubmittedAt:   submittedAt,
		Class:         "leaseServiceInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + subjectKey +
			`","adapter":"` + family + `","replyOp":"RecordLeaseServiceOutcome","params":{"family":"` + family + `"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{subjectKey}},
	}
	testutil.PublishOp(t, conn, createEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if status == "" {
		return instKey
	}
	replyEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(tag + "r"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   submittedAt,
		Class:         "leaseServiceReply",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `","status":"` + status + `","result":"test"}`),
	}
	testutil.PublishOp(t, conn, replyEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return instKey
}

// seedTombstonedServiceInstance seeds a service-instance root that is already
// tombstoned (isDeleted=true), writing the KV document directly — bypassing
// every op, since no sanctioned op tombstones a vtx.service.* root other than
// the very one under test here. This is the realistic "UnknownInstance" case a
// REQUIRED root read (contextHint.reads) can actually reach at the script
// layer: a genuinely never-created key is RequiredAbsent, and the Processor
// faults HydrationMiss before the script ever runs (Contract #2 §2.5) — the
// same split every other REQUIRED read in this op takes (B1/B2 review). A
// tombstoned root is a plausible real case too: a prior (possibly concurrent)
// supersession already retired it.
func seedTombstonedServiceInstance(t *testing.T, ctx context.Context, conn *substrate.Conn, handle, class string) string {
	t.Helper()
	key := "vtx.service." + handle
	doc := map[string]any{"class": class, "isDeleted": true, "data": map[string]any{}}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal tombstoned service instance %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed tombstoned service instance %s: %v", key, err)
	}
	return key
}

// seedForeignShapedInstance seeds a service-instance root that LOOKS exactly
// like a real lease-signing background check (same envelope class, a
// completed .outcome, a providedTo link to subjectKey) but whose instanceOf
// link targets a DIFFERENT type authority — service.<foreignTplID> (the
// shape service-domain's own generic template-instanceOf mechanism produces),
// never this DDL's meta.<ourId> — the B1/B2 forgery vector this test proves
// is refused: nothing about the instance's READABLE shape distinguishes it
// from a real lease-signing instance, only its actual instanceOf target does.
// Written directly via KV, bypassing every op (no sanctioned op mints this
// shape on purpose — it stands in for a hypothetical or future foreign
// minter, not a real lease-signing code path).
func seedForeignShapedInstance(t *testing.T, ctx context.Context, conn *substrate.Conn, handle, foreignTplID, subjectKey, completedAt string) string {
	t.Helper()
	instKey := "vtx.service." + handle
	seedVertex(t, ctx, conn, instKey, "service.backgroundCheck.instance", map[string]any{})

	_, subjID, _ := substrate.ParseVertexKey(subjectKey)
	providedTo := "lnk.service." + handle + ".providedTo.identity." + subjID
	linkDoc := map[string]any{
		"class": "providedTo", "isDeleted": false,
		"sourceVertex": instKey, "targetVertex": subjectKey,
		"localName": "providedTo", "data": map[string]any{},
	}
	lb, err := json.Marshal(linkDoc)
	if err != nil {
		t.Fatalf("marshal foreign providedTo: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, providedTo, lb); err != nil {
		t.Fatalf("seed foreign providedTo %s: %v", providedTo, err)
	}

	foreignTemplate := "vtx.service." + foreignTplID
	foreignInstanceOf := "lnk.service." + handle + ".instanceOf.service." + foreignTplID
	foreignDoc := map[string]any{
		"class": "instanceOf", "isDeleted": false,
		"sourceVertex": instKey, "targetVertex": foreignTemplate,
		"localName": "instanceOf", "data": map[string]any{},
	}
	fb, err := json.Marshal(foreignDoc)
	if err != nil {
		t.Fatalf("marshal foreign instanceOf: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, foreignInstanceOf, fb); err != nil {
		t.Fatalf("seed foreign instanceOf %s: %v", foreignInstanceOf, err)
	}

	outcomeDoc := map[string]any{
		"class": "leaseServiceOutcome", "isDeleted": false,
		"vertexKey": instKey, "localName": "outcome",
		"data": map[string]any{"status": "completed", "completedAt": completedAt, "validUntil": completedAt},
	}
	ob, err := json.Marshal(outcomeDoc)
	if err != nil {
		t.Fatalf("marshal foreign outcome: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, instKey+".outcome", ob); err != nil {
		t.Fatalf("seed foreign outcome %s: %v", instKey+".outcome", err)
	}
	return instKey
}

// instanceOfLinkKey scans Core KV for the sole lnk.service.<handle>.instanceOf.meta.*
// key (mirrors TestLeaseServiceInstance_MintsClaimVertex_EmitsExternalEvent's
// own technique — the meta NanoID is install-time-derived, never hardcoded).
func instanceOfLinkKey(t *testing.T, ctx context.Context, conn *substrate.Conn, handle string) string {
	t.Helper()
	prefix := "lnk.service." + handle + ".instanceOf.meta."
	allKeys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}
	for _, k := range allKeys {
		if strings.HasPrefix(k, prefix) {
			return k
		}
	}
	t.Fatalf("no instanceOf link found with prefix %q", prefix)
	return ""
}

// resolveLeaseServiceInstanceMetaID discovers the leaseServiceInstance DDL's
// installed meta-vertex NanoID for the CURRENT test environment (freshly
// installed per setupLeaseEnv — NOT a cross-test constant, since every test
// re-installs the package) by creating one throwaway completed instance and
// reading its own real instanceOf link. TombstoneSupersededLeaseServiceInstance's
// SEVENTH declared read (the B1/B2 ownership link) is keyed on this same id
// for every instance in a given test's environment.
func resolveLeaseServiceInstanceMetaID(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer) string {
	t.Helper()
	subject := seedApplicant(t, ctx, conn, "BBbaNpMQRJzFSxajuvkF")
	inst := seedServiceInstance(t, ctx, conn, cp, cons, "tsMetaDisc1", "BBqhNcxqoP292Z4a7uPK", "backgroundCheck", subject, "2026-01-01T00:00:00Z", "")
	_, h, _ := substrate.ParseVertexKey(inst)
	link := instanceOfLinkKey(t, ctx, conn, h)
	parts := strings.Split(link, ".")
	return parts[len(parts)-1]
}

// readRevision returns the raw NATS KV revision of key — used to prove a
// tombstone mutated the PRE-EXISTING record (revision advanced) rather than
// materializing a fresh phantom one (B1/B2).
func readRevision(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) uint64 {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	return entry.Revision
}

// submitTombstoneSuperseded builds + submits TombstoneSupersededLeaseServiceInstance
// as actor. The DDL's documented contract (ddls.go) declares all SEVEN reads
// required (contextHint.reads): instanceKey's root, instanceKey's ownership
// instanceOf link (B1/B2 — keyed on metaID, the leaseServiceInstance DDL's
// installed meta-vertex id), both .outcome aspects, supersededBy's root, and
// both providedTo links. The roots + outcomes + ownership link are always
// safe to declare required here because every guard that reads them runs
// only once an earlier vertex_alive/ownership check has already confirmed
// the read is meaningful (state[key] is never subscripted, and kv.Read is
// never called, on a key still known to be absent) — but a providedTo link
// keyed on a WRONG subject genuinely never exists in KV, so linksOptional
// lets a wrong-subject test declare the two providedTo link keys as
// optionalReads instead (mirroring lease_signing_test.go's withdrawReason: a
// probing envelope that makes absence a script-visible branch rather than a
// HydrationMiss fault — a technique, not the DDL's real (required) contract,
// which every other test in this file exercises unmodified).
func submitTombstoneSuperseded(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, tag, actor, instanceKey, supersededBy, subjectKey, metaID string, linksOptional bool) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	_, instHandle, _ := substrate.ParseVertexKey(instanceKey)
	_, succHandle, _ := substrate.ParseVertexKey(supersededBy)
	_, subjID, _ := substrate.ParseVertexKey(subjectKey)
	instOwnership := "lnk.service." + instHandle + ".instanceOf.meta." + metaID
	instProvidedTo := "lnk.service." + instHandle + ".providedTo.identity." + subjID
	succProvidedTo := "lnk.service." + succHandle + ".providedTo.identity." + subjID

	hint := &processor.ContextHint{
		Reads: []string{instanceKey, instOwnership, instanceKey + ".outcome", supersededBy, supersededBy + ".outcome"},
	}
	if linksOptional {
		hint.OptionalReads = []string{instProvidedTo, succProvidedTo}
	} else {
		hint.Reads = append(hint.Reads, instProvidedTo, succProvidedTo)
	}

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(tag),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSupersededLeaseServiceInstance",
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseServiceInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + instanceKey + `","supersededBy":"` + supersededBy +
			`","subjectKey":"` + subjectKey + `"}`),
		ContextHint: hint,
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// TestTombstoneSupersededLeaseServiceInstance_Success: the happy path. Two
// completed background-check instances on the same subject, older strictly
// before newer. Tombstoning older-superseded-by-newer tombstones older's root
// + its instanceOf link + its providedTo link, leaves older's .outcome aspect
// dangling alive (non-cascading tombstone — the WithdrawLeaseApplication
// precedent: readers filter on the ROOT's isDeleted), leaves newer entirely
// untouched, and emits lease.serviceInstanceSuperseded. B1/B2: additionally
// proves the tombstoned instanceOf link is the PRE-EXISTING record (its
// revision advances by the tombstone, and its sourceVertex/targetVertex
// provenance survives unchanged), not a freshly materialized phantom.
func TestTombstoneSupersededLeaseServiceInstance_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-success")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBftM6nXFwju1FgWbCTm")
	older := seedServiceInstance(t, ctx, conn, cp, cons, "tsSuOld1", "BBAdPZkFfj9NpfDYJPwL", "backgroundCheck", subject, "2026-02-01T00:00:00Z", "completed")
	newer := seedServiceInstance(t, ctx, conn, cp, cons, "tsSuNew1", "BBy2epMitMCHBLgu4cFd", "backgroundCheck", subject, "2026-02-02T00:00:00Z", "completed")

	olderInstOf := instanceOfLinkKey(t, ctx, conn, "BBAdPZkFfj9NpfDYJPwL")
	newerInstOf := instanceOfLinkKey(t, ctx, conn, "BBy2epMitMCHBLgu4cFd")
	olderInstOfBefore := readDoc(t, ctx, conn, olderInstOf)
	olderInstOfRevBefore := readRevision(t, ctx, conn, olderInstOf)
	_, subjID, _ := substrate.ParseVertexKey(subject)
	olderProvidedTo := "lnk.service.BBAdPZkFfj9NpfDYJPwL.providedTo.identity." + subjID
	newerProvidedTo := "lnk.service.BBy2epMitMCHBLgu4cFd.providedTo.identity." + subjID

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsSuccessOp1", lsActorKey, older, newer, subject, metaID, false)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}

	if d, _ := readDoc(t, ctx, conn, older)["isDeleted"].(bool); !d {
		t.Fatalf("older instance root must be tombstoned")
	}
	olderInstOfAfter := readDoc(t, ctx, conn, olderInstOf)
	if d, _ := olderInstOfAfter["isDeleted"].(bool); !d {
		t.Fatalf("older instance's instanceOf link must be tombstoned")
	}
	// B1/B2: the pre-existing record, not a phantom — provenance survives and
	// the revision advances (a fresh key would start at revision 1).
	if got, want := olderInstOfAfter["sourceVertex"], olderInstOfBefore["sourceVertex"]; got != want {
		t.Fatalf("instanceOf sourceVertex changed across the tombstone: got %v, want %v (a phantom record would drop it)", got, want)
	}
	if got, want := olderInstOfAfter["targetVertex"], olderInstOfBefore["targetVertex"]; got != want {
		t.Fatalf("instanceOf targetVertex changed across the tombstone: got %v, want %v (a phantom record would drop it)", got, want)
	}
	olderInstOfRevAfter := readRevision(t, ctx, conn, olderInstOf)
	if olderInstOfRevAfter <= olderInstOfRevBefore {
		t.Fatalf("instanceOf link revision did not advance (before=%d after=%d) — a phantom tombstone would instead start a fresh key",
			olderInstOfRevBefore, olderInstOfRevAfter)
	}
	if d, _ := readDoc(t, ctx, conn, olderProvidedTo)["isDeleted"].(bool); !d {
		t.Fatalf("older instance's providedTo link must be tombstoned")
	}
	// Non-cascading: the dangling .outcome aspect is left alone.
	if !keyExists(t, ctx, conn, older+".outcome") {
		t.Fatalf("older instance's .outcome aspect must be left alive (non-cascading tombstone)")
	}

	if d, _ := readDoc(t, ctx, conn, newer)["isDeleted"].(bool); d {
		t.Fatalf("newer (supersededBy) instance root must NOT be touched")
	}
	if d, _ := readDoc(t, ctx, conn, newerInstOf)["isDeleted"].(bool); d {
		t.Fatalf("newer instance's instanceOf link must NOT be touched")
	}
	if d, _ := readDoc(t, ctx, conn, newerProvidedTo)["isDeleted"].(bool); d {
		t.Fatalf("newer instance's providedTo link must NOT be touched")
	}

	ev := findEmittedEvent(t, ctx, conn, testutil.GenReqID("tsSuccessOp1"), "lease.serviceInstanceSuperseded")
	if got, _ := ev["instanceKey"].(string); got != older {
		t.Fatalf("event instanceKey = %q, want %q", got, older)
	}
	if got, _ := ev["supersededBy"].(string); got != newer {
		t.Fatalf("event supersededBy = %q, want %q", got, newer)
	}
	if got, _ := ev["subjectKey"].(string); got != subject {
		t.Fatalf("event subjectKey = %q, want %q", got, subject)
	}
}

// TestTombstoneSupersededLeaseServiceInstance_SameKey_Rejected: instanceKey ==
// supersededBy is rejected (InvalidArgument) before any state is even read —
// the two keys naming the same instance is never a legitimate supersession.
func TestTombstoneSupersededLeaseServiceInstance_SameKey_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-samekey")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBrFKVqAaXJjbN92DrPe")
	same := "vtx.service.BBSkb4zJYvgDJy7wp1yK"

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsSameKeyOp1", lsActorKey, same, same, subject, metaID, false)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Fatalf("want InvalidArgument, got %+v", reply.Error)
	}
}

// TestTombstoneSupersededLeaseServiceInstance_UnknownInstance_Rejected: an
// already-tombstoned instanceKey is rejected (UnknownInstance); a real,
// completed supersededBy makes this isolate the instanceKey-side check
// specifically. Pre-tombstoned rather than never-created: instanceKey is a
// REQUIRED read, so a genuinely never-created key is RequiredAbsent and
// faults HydrationMiss before the script runs at all
// (seedTombstonedServiceInstance's doc comment) — a prior supersession having
// already retired it is the realistic case the script's own guard reaches.
func TestTombstoneSupersededLeaseServiceInstance_UnknownInstance_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-unknowninst")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBabX43bac4ZkFM8CZx6")
	successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsUnkSucc1", "BBb1eQV6sQsxVG7vxWPz", "backgroundCheck", subject, "2026-02-03T00:00:00Z", "completed")
	unknown := seedTombstonedServiceInstance(t, ctx, conn, "BB3wuwRaUezF8ecSEiUu", "service.backgroundCheck.instance")

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsUnkInstOp1", lsActorKey, unknown, successor, subject, metaID, false)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "UnknownInstance") {
		t.Fatalf("want UnknownInstance, got %+v", reply.Error)
	}
}

// TestTombstoneSupersededLeaseServiceInstance_UnknownSuccessor_Rejected: the
// complement — a real, completed instanceKey with an already-tombstoned
// supersededBy (see seedTombstonedServiceInstance's doc comment for why
// pre-tombstoned rather than never-created).
func TestTombstoneSupersededLeaseServiceInstance_UnknownSuccessor_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-unknownsucc")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBU6N9ttL2rStcyz7wQc")
	instance := seedServiceInstance(t, ctx, conn, cp, cons, "tsUnkInst2", "BB77YtbAHikNAjVuyLi2", "backgroundCheck", subject, "2026-02-03T00:00:00Z", "completed")
	unknown := seedTombstonedServiceInstance(t, ctx, conn, "BB3ePSy6ycfmjrq1r5zp", "service.backgroundCheck.instance")

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsUnkSuccOp1", lsActorKey, instance, unknown, subject, metaID, false)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "UnknownInstance") {
		t.Fatalf("want UnknownInstance, got %+v", reply.Error)
	}
}

// TestTombstoneSupersededLeaseServiceInstance_ForeignInstance_Rejected
// (B1/B2, blocking): instanceKey looks exactly like a real lease-signing
// background check (envelope class, a completed .outcome, a providedTo link)
// but its REAL instanceOf link targets a different type authority
// (service.<tplId>, never this DDL's meta.<ourId>) — the forgery vector a
// pure shape/class/outcome check cannot catch. The DDL declares the
// ownership link a REQUIRED read, so for a genuinely foreign instance the
// derived lnk...instanceOf.meta.<ourId> key never exists at all and the
// submission is rejected as HydrationMiss AT DISPATCH — the real production
// posture (S2). Either way nothing is written: the foreign root stays alive
// and untouched, and the derived (never-real) ownership key still does not
// exist afterwards — proving no phantom tombstone record was written for it.
func TestTombstoneSupersededLeaseServiceInstance_ForeignInstance_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-foreign")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BB9e76vvacmwMZSPTfn6")
	foreign := seedForeignShapedInstance(t, ctx, conn, "BB8vaM4gn9r8gtLEkRfj", "BBDTF1cTG597CakmXX6D", subject, "2026-05-01T00:00:00Z")
	successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsForSucc1", "BBAHX2hEXsxAnukTrAoW", "backgroundCheck", subject, "2026-05-02T00:00:00Z", "completed")

	phantomOwnership := "lnk.service.BB8vaM4gn9r8gtLEkRfj.instanceOf.meta." + metaID

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsForeignOp1", lsActorKey, foreign, successor, subject, metaID, false)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected (reply=%+v)", outcome, reply)
	}
	if d, _ := readDoc(t, ctx, conn, foreign)["isDeleted"].(bool); d {
		t.Fatalf("a rejected tombstone of a foreign instance must not tombstone its root")
	}
	if keyExists(t, ctx, conn, phantomOwnership) {
		t.Fatalf("no phantom ownership record must be written for a foreign instance's derived (never-real) instanceOf.meta key")
	}
}

// TestTombstoneSupersededLeaseServiceInstance_DifferentClass_Rejected: a
// completed payment instance can never supersede a completed background check,
// even for the same subject (WrongClass) — a successor supersedes only its own
// family.
func TestTombstoneSupersededLeaseServiceInstance_DifferentClass_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-diffclass")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBmMvYs4kFyuHxJ4JKkF")
	instance := seedServiceInstance(t, ctx, conn, cp, cons, "tsClsInst1", "BBCzisaLY72wQqpzqLX1", "backgroundCheck", subject, "2026-02-04T00:00:00Z", "completed")
	successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsClsSucc1", "BBD9X3rdNVMyHKhsjxAQ", "payment", subject, "2026-02-05T00:00:00Z", "completed")

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsDiffClsOp1", lsActorKey, instance, successor, subject, metaID, false)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "WrongClass") {
		t.Fatalf("want WrongClass, got %+v", reply.Error)
	}
}

// TestTombstoneSupersededLeaseServiceInstance_InstanceNotCompleted_Rejected:
// instanceKey carries a PRESENT outcome that is not completed (status=failed) —
// NotSuperseded. A present-but-failed outcome (not an absent one) is the
// faithful shape: the purge planner only ever names completed instances, so a
// genuinely absent .outcome under this DDL's declared-required read is a
// HydrationMiss correctness fault, not a script-level branch (the same posture
// WithdrawLeaseApplication's own required validation-link reads take).
func TestTombstoneSupersededLeaseServiceInstance_InstanceNotCompleted_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-instnotdone")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BB8U3mUUHhd3ECYLpuW6")
	instance := seedServiceInstance(t, ctx, conn, cp, cons, "tsIncInst1", "BBXtW35Ca4TqQmKvuBz7", "backgroundCheck", subject, "2026-02-06T00:00:00Z", "failed")
	successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsIncSucc1", "BBUybciw91QMjcDPRsSL", "backgroundCheck", subject, "2026-02-07T00:00:00Z", "completed")

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsInstIncOp1", lsActorKey, instance, successor, subject, metaID, false)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "NotSuperseded") {
		t.Fatalf("want NotSuperseded, got %+v", reply.Error)
	}
}

// TestTombstoneSupersededLeaseServiceInstance_SuccessorNotCompleted_Rejected:
// the complement — supersededBy carries a present but failed outcome.
func TestTombstoneSupersededLeaseServiceInstance_SuccessorNotCompleted_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-succnotdone")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBqFZiLQxnhPw6GdqN93")
	instance := seedServiceInstance(t, ctx, conn, cp, cons, "tsScIncI1", "BBMJgy9RM5uSLsju3i76", "backgroundCheck", subject, "2026-02-08T00:00:00Z", "completed")
	successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsScIncS1", "BBkERRjQuwcbjmERSW1n", "backgroundCheck", subject, "2026-02-09T00:00:00Z", "failed")

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsSuccIncOp1", lsActorKey, instance, successor, subject, metaID, false)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "NotSuperseded") {
		t.Fatalf("want NotSuperseded, got %+v", reply.Error)
	}
}

// TestTombstoneSupersededLeaseServiceInstance_SuccessorNotStrictlyLater_Rejected:
// supersededBy's completedAt must be STRICTLY later than instanceKey's — an
// older successor and an equal-timestamp successor are both rejected
// (NotSuperseded).
func TestTombstoneSupersededLeaseServiceInstance_SuccessorNotStrictlyLater_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-notlater")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBNeDBYQKpF57aBYW8aR")

	cases := []struct {
		name       string
		tag        string
		instHandle string
		instAt     string
		succHandle string
		succAt     string
	}{
		{
			name:       "successor strictly older",
			tag:        "tsOlder1",
			instHandle: "BBkK1EBBMPQXqSzEsAdH",
			instAt:     "2026-03-02T00:00:00Z",
			succHandle: "BBtdFv6xZhdm8a1m9sSW",
			succAt:     "2026-03-01T00:00:00Z",
		},
		{
			name:       "successor equal timestamp",
			tag:        "tsEqual1",
			instHandle: "BBwY3HTjRECQTWqGT8Vq",
			instAt:     "2026-03-05T00:00:00Z",
			succHandle: "BBqRPetejBpySE1aHyBU",
			succAt:     "2026-03-05T00:00:00Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			instance := seedServiceInstance(t, ctx, conn, cp, cons, tc.tag+"i", tc.instHandle, "backgroundCheck", subject, tc.instAt, "completed")
			successor := seedServiceInstance(t, ctx, conn, cp, cons, tc.tag+"s", tc.succHandle, "backgroundCheck", subject, tc.succAt, "completed")

			outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, tc.tag+"op", lsActorKey, instance, successor, subject, metaID, false)
			if outcome != processor.OutcomeRejected {
				t.Fatalf("outcome = %v, want Rejected", outcome)
			}
			if reply.Error == nil || !strings.Contains(reply.Error.Message, "NotSuperseded") {
				t.Fatalf("want NotSuperseded, got %+v", reply.Error)
			}
		})
	}
}

// TestTombstoneSupersededLeaseServiceInstance_WrongSubject_Rejected: the
// submitted subjectKey must be the ACTUAL subject of both instances —
// SubjectMismatch either way. Declares the two providedTo link reads
// OPTIONAL for this test only (mirroring withdrawReason's probing envelope,
// submitTombstoneSuperseded's own doc comment): the link keyed on the wrong
// subject genuinely never exists in KV, and a REQUIRED-but-genuinely-absent
// read faults HydrationMiss rather than reaching the script's own guard — the
// DDL's real (required) contract is exercised by every other test in this
// file, where the declared keys are always genuinely present.
func TestTombstoneSupersededLeaseServiceInstance_WrongSubject_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-wrongsubj")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	realSubject := seedApplicant(t, ctx, conn, "BBrJ7cNd8ttooBBH3WPP")
	otherSubject := seedApplicant(t, ctx, conn, "BBtrGAd58UM5J41vUMwc")

	t.Run("instance providedTo a different subject", func(t *testing.T) {
		instance := seedServiceInstance(t, ctx, conn, cp, cons, "tsWsI1", "BBGV5MzcarGETjVS6xLz", "backgroundCheck", otherSubject, "2026-04-01T00:00:00Z", "completed")
		successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsWsI2", "BBTxHCyp5ESgbJ8UpgP6", "backgroundCheck", realSubject, "2026-04-02T00:00:00Z", "completed")

		outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsWsIOp1", lsActorKey, instance, successor, realSubject, metaID, true)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("outcome = %v, want Rejected", outcome)
		}
		if reply.Error == nil || !strings.Contains(reply.Error.Message, "SubjectMismatch") {
			t.Fatalf("want SubjectMismatch, got %+v", reply.Error)
		}
	})

	t.Run("successor providedTo a different subject", func(t *testing.T) {
		instance := seedServiceInstance(t, ctx, conn, cp, cons, "tsWsS1", "BB71iV5eSiSrMfUKNXiJ", "backgroundCheck", realSubject, "2026-04-03T00:00:00Z", "completed")
		successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsWsS2", "BB3iQqYpjEQmd2KnAXx2", "backgroundCheck", otherSubject, "2026-04-04T00:00:00Z", "completed")

		outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsWsSOp1", lsActorKey, instance, successor, realSubject, metaID, true)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("outcome = %v, want Rejected", outcome)
		}
		if reply.Error == nil || !strings.Contains(reply.Error.Message, "SubjectMismatch") {
			t.Fatalf("want SubjectMismatch, got %+v", reply.Error)
		}
	})
}

// TestTombstoneSupersededLeaseServiceInstance_NonOperatorDenied: an actor with
// no TombstoneSupersededLeaseServiceInstance grant (a plain identity that was
// never issued a CapabilityDoc) is denied at step 3 — AuthDenied — before the
// script ever runs; the two real instances are left untouched. A REPEAT
// submission of the very same request after a successful purge is covered by
// TestTombstoneSupersededLeaseServiceInstance_UnknownInstance_Rejected's shape
// (S6): the instance is by then tombstoned, so the retry is refused
// UnknownInstance — the runbook's signal that item is already done.
func TestTombstoneSupersededLeaseServiceInstance_NonOperatorDenied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-nonoperator")

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBB5PVbXiCesSgkqYPYJ")
	stranger := seedApplicant(t, ctx, conn, "BBCvWvmuQ6P33rjpdwgL")
	instance := seedServiceInstance(t, ctx, conn, cp, cons, "tsNoOpI1", "BBpYmMxj63kVBBpoYKJd", "backgroundCheck", subject, "2026-02-10T00:00:00Z", "completed")
	successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsNoOpS1", "BBCEkWumU5ZMmXv1vGxm", "backgroundCheck", subject, "2026-02-11T00:00:00Z", "completed")

	outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, "tsNonOpOp1", stranger, instance, successor, subject, metaID, false)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeAuthDenied {
		t.Fatalf("want ErrCodeAuthDenied, got %+v", reply.Error)
	}
	if d, _ := readDoc(t, ctx, conn, instance)["isDeleted"].(bool); d {
		t.Fatalf("a denied tombstone must not tombstone the instance")
	}
}

// lsWeaverCapDoc grants Weaver's primordial relay actor the op under test —
// mirroring lsLoomCapDoc's shape — so
// TestTombstoneSupersededLeaseServiceInstance_PlatformEngineDenied proves the
// SCRIPT's own actor-guard is what refuses it (S3), not a missing platform
// grant (the same isolation TestLeaseServiceInstance_NonLoomOperatorDenied
// uses for lsActorKey against CreateLeaseServiceInstance's own guard).
func lsWeaverCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + bootstrap.WeaverIdentityID,
		Actor:                  bootstrap.WeaverIdentityKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bootstrap.WeaverIdentityKey: 1},
		Lanes:                  []string{"default", "urgent"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "TombstoneSupersededLeaseServiceInstance", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// TestTombstoneSupersededLeaseServiceInstance_PlatformEngineDenied (S3): the
// operator/Scope:"any" grant behind this op is broad enough to admit Loom and
// Weaver (both hold the operator role for their own unrelated ops), so the
// script's own actor-guard refuses them outright even though each holds an
// EXPLICIT grant for this exact op here (lsLoomCapDoc / lsWeaverCapDoc) —
// isolating the script's guard from a missing-permission denial. Two real
// instances prove nothing is written either.
func TestTombstoneSupersededLeaseServiceInstance_PlatformEngineDenied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-platformengine")
	testutil.SeedCapDoc(t, ctx, conn, lsWeaverCapDoc())

	metaID := resolveLeaseServiceInstanceMetaID(t, ctx, conn, cp, cons)
	subject := seedApplicant(t, ctx, conn, "BBaWoRoHptUTc9JJt85J")
	instance := seedServiceInstance(t, ctx, conn, cp, cons, "tsPeInst1", "BBUvt216BmikSKRcjmJS", "backgroundCheck", subject, "2026-02-12T00:00:00Z", "completed")
	successor := seedServiceInstance(t, ctx, conn, cp, cons, "tsPeSucc1", "BBVdyaF5YB9hQgsEh8VR", "backgroundCheck", subject, "2026-02-13T00:00:00Z", "completed")

	cases := []struct {
		name  string
		tag   string
		actor string
	}{
		{"loom", "tsPeLoomOp1", bootstrap.LoomIdentityKey},
		{"weaver", "tsPeWeavOp1", bootstrap.WeaverIdentityKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, reply := submitTombstoneSuperseded(t, ctx, conn, cp, cons, tc.tag, tc.actor, instance, successor, subject, metaID, false)
			if outcome != processor.OutcomeRejected {
				t.Fatalf("outcome = %v, want Rejected", outcome)
			}
			if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
				t.Fatalf("want AuthDenied, got %+v", reply.Error)
			}
			if !strings.Contains(reply.Error.Message, "a platform engine never supersedes a check") {
				t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
			}
			if d, _ := readDoc(t, ctx, conn, instance)["isDeleted"].(bool); d {
				t.Fatalf("a denied tombstone must not tombstone the instance")
			}
		})
	}
}
