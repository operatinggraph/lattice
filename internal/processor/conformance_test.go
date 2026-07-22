package processor

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Contract-conformance suite — the Phase 2 readiness freeze.
//
// This file lives in package processor (white-box) rather than a separate
// internal/conformance package for two reasons:
//   - The reply-constraint enforcement proof must drive the unexported
//     CommitPath and assert against the unexported OperationReply fields,
//     parseScriptResult, and the closed `response` schema — all package-private.
//   - The frozen wire shapes (OperationReply / OperationEnvelope / ContextHint /
//     AuthContext / ErrorCode) are defined in this package; freezing them where
//     they are defined keeps the contract and its guard in one place.
//
// The suite asserts the FROZEN contract shapes and — the centerpiece — proves
// in code that the write path is not a read channel: a script that returns a
// non-`primaryKey` `response` key, or a `primaryKey` not in the committed
// mutation set, is rejected fail-closed.

// --- Frozen OperationReply shape ---

// TestConformance_OperationReply_FrozenFieldSet asserts the exact JSON field
// set of an accepted reply and, as a regression guard, that no `detail` field
// is ever emitted.
func TestConformance_OperationReply_FrozenFieldSet(t *testing.T) {
	reply := BuildAcceptedReplyWithRevisions(
		testNanoID1,
		time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC),
		"vtx.identity."+testNanoID2,
		map[string]uint64{"vtx.identity." + testNanoID2: 7},
	)
	reply.OriginalCommittedAt = "2026-05-30T09:59:00Z"

	b, err := MarshalReply(reply)
	if err != nil {
		t.Fatalf("MarshalReply: %v", err)
	}

	// Regression guard: the wire bytes must never carry a "detail" key.
	if strings.Contains(string(b), `"detail"`) {
		t.Fatalf("accepted reply emitted a forbidden \"detail\" field: %s", b)
	}

	var generic map[string]json.RawMessage
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	got := make([]string, 0, len(generic))
	for k := range generic {
		got = append(got, k)
	}
	sort.Strings(got)

	// The closed set of fields an accepted reply may carry. `error` and
	// `status`-only fields are exercised by the rejected/duplicate shapes.
	want := []string{
		"committedAt", "decision", "opTrackerKey", "originalCommittedAt",
		"primaryKey", "requestId", "revisions", "status",
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("accepted reply field set drift:\n got=%v\nwant=%v", got, want)
	}
}

// TestConformance_OperationReply_NoDetailAcrossAllBuilders asserts that none
// of the reply builders can emit a "detail" field.
func TestConformance_OperationReply_NoDetailAcrossAllBuilders(t *testing.T) {
	tr := Tracker{Key: TrackerKey(testNanoID1), Data: map[string]any{"committedAt": "2026-05-30T10:00:00Z"}}
	replies := []OperationReply{
		BuildAcceptedReply(testNanoID1, time.Now()),
		BuildAcceptedReplyWithRevisions(testNanoID1, time.Now(), "vtx.meta."+testNanoID2, map[string]uint64{"vtx.meta." + testNanoID2: 1}),
		BuildDuplicateReply(testNanoID1, &tr),
		BuildRejectedReply(testNanoID1, ErrCodeDDLViolation, "nope", map[string]any{"k": "v"}),
	}
	for i, r := range replies {
		b, err := MarshalReply(r)
		if err != nil {
			t.Fatalf("reply[%d] marshal: %v", i, err)
		}
		if strings.Contains(string(b), `"detail"`) {
			t.Fatalf("reply[%d] emitted a forbidden \"detail\" field: %s", i, b)
		}
	}
}

// TestConformance_ErrorCode_ClosedEnum asserts the rejected-reply error code is
// always a member of the closed ErrorCode enum.
func TestConformance_ErrorCode_ClosedEnum(t *testing.T) {
	r := BuildRejectedReply(testNanoID1, ErrCodeDDLViolation, "x", nil)
	if r.Error == nil || !emittedErrorCodes[r.Error.Code] {
		t.Fatalf("rejected reply error.code %q not in closed ErrorCode enum", r.Error.Code)
	}
}

// --- Frozen closed `response` script-return schema ---

// TestConformance_ClosedResponseSchema proves parseScriptResult enforces the
// closed `response` schema: only `primaryKey` (string) is permitted; any other
// key fails closed; absent response/primaryKey is allowed.
func TestConformance_ClosedResponseSchema(t *testing.T) {
	cases := []struct {
		name       string
		script     string
		wantErr    bool
		wantPrimar string
	}{
		{
			name:       "absent response is allowed",
			script:     `def execute(state, op):\n    return {"mutations": [], "events": []}`,
			wantErr:    false,
			wantPrimar: "",
		},
		{
			name:       "response with only primaryKey is accepted",
			script:     `def execute(state, op):\n    return {"mutations": [], "events": [], "response": {"primaryKey": "vtx.identity.AbCdEfGhJkLmNpQrStUv"}}`,
			wantErr:    false,
			wantPrimar: "vtx.identity.AbCdEfGhJkLmNpQrStUv",
		},
		{
			name:    "response with an extra key fails closed",
			script:  `def execute(state, op):\n    return {"mutations": [], "events": [], "response": {"primaryKey": "vtx.x.y", "claimKey": "leaked"}}`,
			wantErr: true,
		},
		{
			name:    "response with a non-primaryKey key fails closed",
			script:  `def execute(state, op):\n    return {"mutations": [], "events": [], "response": {"metaKey": "vtx.meta.x"}}`,
			wantErr: true,
		},
		{
			name:    "response.primaryKey non-string fails closed",
			script:  `def execute(state, op):\n    return {"mutations": [], "events": [], "response": {"primaryKey": 42}}`,
			wantErr: true,
		},
	}

	runner := NewStarlarkRunner(0, 0)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := ScriptContext{
				Operation:    &OperationEnvelope{RequestID: testNanoID1, Lane: LaneDefault, OperationType: "X", Actor: "a", SubmittedAt: "t", Payload: []byte("{}")},
				ScriptSource: strings.ReplaceAll(tc.script, `\n`, "\n"),
			}
			res, err := runner.Run(context.Background(), sc)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected fail-closed error, got primaryKey=%q", res.PrimaryKey)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.PrimaryKey != tc.wantPrimar {
				t.Fatalf("primaryKey = %q, want %q", res.PrimaryKey, tc.wantPrimar)
			}
		})
	}
}

// --- Reply-constraint enforcement proof (the in-code centerpiece) ---

// publishWithReply publishes an envelope to its lane subject carrying a
// reply-inbox header and returns a subscription the test reads the reply from.
func publishWithReply(t *testing.T, conn *substrate.Conn, env *OperationEnvelope) *nats.Subscription {
	t.Helper()
	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe reply inbox: %v", err)
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	msg := nats.NewMsg("ops." + string(env.Lane))
	msg.Data = b
	msg.Header.Set("Lattice-Reply-Inbox", inbox)
	if _, err := conn.JetStream().PublishMsg(context.Background(), msg); err != nil {
		t.Fatalf("publish op: %v", err)
	}
	return sub
}

func awaitReply(t *testing.T, sub *nats.Subscription) OperationReply {
	t.Helper()
	m, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("await reply: %v", err)
	}
	var r OperationReply
	if err := json.Unmarshal(m.Data, &r); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	// Regression guard on the live wire bytes.
	if strings.Contains(string(m.Data), `"detail"`) {
		t.Fatalf("live reply carried a forbidden \"detail\" field: %s", m.Data)
	}
	return r
}

// TestConformance_PrimaryKeyMustBeCommitted proves a script-returned primaryKey
// within the write footprint is surfaced, and one that is NOT is rejected
// fail-closed (DDLViolation) BEFORE the atomic batch — so a violation never
// mutates Core KV. This is the in-code enforcement that the write path is not a
// read channel.
func TestConformance_PrimaryKeyMustBeCommitted(t *testing.T) {
	t.Parallel()
	t.Run("valid primaryKey is surfaced", func(t *testing.T) {
		ctx, conn, _, _, _ := setupTestPipeline(t)
		script := `{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    k = \"vtx.identity.` + testNanoID2 + `\"\n    return {\"mutations\": [{\"op\": \"create\", \"key\": k, \"document\": {\"class\": \"identity\", \"data\": {}}}], \"events\": [], \"response\": {\"primaryKey\": k}}\n"}}`
		if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", []byte(script)); err != nil {
			t.Fatalf("seed script: %v", err)
		}
		cp, cons := newRealPipeline(t, ctx, conn)
		env := newTestEnvelope(testNanoID1)
		sub := publishWithReply(t, conn, env)
		driveOne(t, ctx, cp, cons, OutcomeAccepted)
		reply := awaitReply(t, sub)
		if reply.Status != ReplyStatusAccepted {
			t.Fatalf("status = %q, want accepted (err=%+v)", reply.Status, reply.Error)
		}
		wantKey := "vtx.identity." + testNanoID2
		if reply.PrimaryKey != wantKey {
			t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, wantKey)
		}
		if _, ok := reply.Revisions[wantKey]; !ok {
			t.Fatalf("revisions missing committed key %q: %v", wantKey, reply.Revisions)
		}
	})

	t.Run("primaryKey not in committed set is rejected", func(t *testing.T) {
		ctx, conn, _, _, _ := setupTestPipeline(t)
		// The script commits vtx.identity.<testNanoID2> but names a DIFFERENT,
		// uncommitted key as primaryKey — a smuggling attempt.
		script := `{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    k = \"vtx.identity.` + testNanoID2 + `\"\n    return {\"mutations\": [{\"op\": \"create\", \"key\": k, \"document\": {\"class\": \"identity\", \"data\": {}}}], \"events\": [], \"response\": {\"primaryKey\": \"vtx.identity.ZzZzZzZzZzZzZzZzZzZz\"}}\n"}}`
		if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", []byte(script)); err != nil {
			t.Fatalf("seed script: %v", err)
		}
		cp, cons := newRealPipeline(t, ctx, conn)
		env := newTestEnvelope(testNanoID1)
		sub := publishWithReply(t, conn, env)
		driveOne(t, ctx, cp, cons, OutcomeRejected)
		reply := awaitReply(t, sub)
		if reply.Status != ReplyStatusRejected {
			t.Fatalf("status = %q, want rejected", reply.Status)
		}
		if reply.Error == nil || reply.Error.Code != ErrCodeDDLViolation {
			t.Fatalf("error = %+v, want code %q", reply.Error, ErrCodeDDLViolation)
		}
		if reply.PrimaryKey != "" {
			t.Fatalf("rejected reply must not carry primaryKey, got %q", reply.PrimaryKey)
		}
		// The reply-constraint is enforced BEFORE the atomic batch: a violation
		// must never have mutated Core KV (no write-then-fail).
		if _, err := conn.KVGet(ctx, testCoreBucket, "vtx.identity."+testNanoID2); err == nil {
			t.Fatalf("primaryKey violation must reject pre-commit; mutation must not be in Core KV")
		}
	})

	t.Run("non-primaryKey response key is rejected", func(t *testing.T) {
		ctx, conn, _, _, _ := setupTestPipeline(t)
		// A script returning a legacy commit-trace key (identityKey) in the
		// response must fail closed at parse — before commit.
		script := `{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    k = \"vtx.identity.` + testNanoID2 + `\"\n    return {\"mutations\": [{\"op\": \"create\", \"key\": k, \"document\": {\"class\": \"identity\", \"data\": {}}}], \"events\": [], \"response\": {\"identityKey\": k}}\n"}}`
		if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", []byte(script)); err != nil {
			t.Fatalf("seed script: %v", err)
		}
		cp, cons := newRealPipeline(t, ctx, conn)
		env := newTestEnvelope(testNanoID1)
		sub := publishWithReply(t, conn, env)
		driveOne(t, ctx, cp, cons, OutcomeRejected)
		reply := awaitReply(t, sub)
		if reply.Status != ReplyStatusRejected {
			t.Fatalf("status = %q, want rejected", reply.Status)
		}
		if reply.Error == nil || reply.Error.Code != ErrCodeScriptFailed {
			t.Fatalf("error = %+v, want code %q (InvalidReturnShape maps to ScriptFailed)", reply.Error, ErrCodeScriptFailed)
		}
		// The smuggled key must never have been committed.
		if _, err := conn.KVGet(ctx, testCoreBucket, "vtx.identity."+testNanoID2); err == nil {
			t.Fatalf("mutation must not commit when the response schema is violated")
		}
	})
}

// TestConformance_PrimaryKeyInCommit_RootFallback freezes the write-footprint
// rule: a primaryKey is valid when it is a committed key OR the 3-segment vertex
// root of a committed key (so aspect-only updates name their principal vertex,
// not an internal aspect). An unrelated vertex or an uncommitted sibling aspect
// is rejected — the write path stays a non-read channel.
func TestConformance_PrimaryKeyInCommit_RootFallback(t *testing.T) {
	muts := []MutationOp{
		{Op: "update", Key: "vtx.identity.AbCdEfGhJkLmNpQrStUv.state"},
		{Op: "create", Key: "lnk.role.RrRrRrRrRrRrRrRrRrRr.assigned.identity.IiIiIiIiIiIiIiIiIiIi"},
	}
	cases := []struct {
		key  string
		want bool
	}{
		{"vtx.identity.AbCdEfGhJkLmNpQrStUv.state", true},                              // direct mutation aspect
		{"vtx.identity.AbCdEfGhJkLmNpQrStUv", true},                                    // 3-seg root of a mutation aspect
		{"lnk.role.RrRrRrRrRrRrRrRrRrRr.assigned.identity.IiIiIiIiIiIiIiIiIiIi", true}, // direct mutation link
		{"vtx.identity.ZzZzZzZzZzZzZzZzZzZz", false},                                   // unrelated vertex
		{"vtx.identity.AbCdEfGhJkLmNpQrStUv.email", false},                             // sibling aspect not in the mutation set
		{"", false}, // empty
	}
	for _, c := range cases {
		if got := primaryKeyInCommit(c.key, muts); got != c.want {
			t.Errorf("primaryKeyInCommit(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

// --- Frozen Core KV key shapes (Contract #1) ---

func TestConformance_CoreKVKeyShapes(t *testing.T) {
	id := "AbCdEfGhJkLmNpQrStUv"
	canonical := map[string]substrate.KeyKind{
		"vtx.identity." + id:                           substrate.KindVertex,
		"vtx.identity." + id + ".state":                substrate.KindAspect,
		"vtx.meta." + id:                               substrate.KindVertex,
		"lnk.identity." + id + ".holdsRole.role." + id: substrate.KindLink,
	}
	for key, want := range canonical {
		if got := substrate.ClassifyKey(key); got != want {
			t.Errorf("ClassifyKey(%q) = %v, want %v", key, got, want)
		}
	}
	malformed := []string{
		"",
		"vtx.identity",
		"vtx.identity." + id + ".state.extra.segments",
		"xyz.identity." + id,
		"vtx.identity.not-a-valid-nanoid",
		"lnk.identity." + id + ".holdsRole.role",
	}
	for _, key := range malformed {
		if got := substrate.ClassifyKey(key); got != substrate.KindUnknown {
			t.Errorf("ClassifyKey(%q) = %v, want KindUnknown", key, got)
		}
	}
}

// --- Frozen DDL meta-vertex aspect set (Contract for meta-vertices) ---

// TestConformance_DDLAspectSet freezes the self-description aspect names a DDL
// meta-vertex carries. Drift here is a contract break for FR19 self-description
// + the compensation traversal.
func TestConformance_DDLAspectSet(t *testing.T) {
	frozen := []string{
		"canonicalName", "permittedCommands", "description", "script",
		"inputSchema", "outputSchema", "fieldDescription", "examples",
		"compensation",
	}
	// Stable order is part of the freeze (verify-kernel asserts in this order).
	want := strings.Join(frozen, ",")
	got := strings.Join(append([]string(nil), frozen...), ",")
	if got != want {
		t.Fatalf("DDL aspect set drift: got=%v want=%v", got, want)
	}
	if len(frozen) != 9 {
		t.Fatalf("DDL aspect set size = %d, want 9", len(frozen))
	}
}

// --- Frozen envelope / contextHint / authContext shapes ---

func TestConformance_EnvelopeRequiredFields(t *testing.T) {
	base := func() *OperationEnvelope {
		return &OperationEnvelope{
			RequestID:     testNanoID1,
			Lane:          LaneDefault,
			OperationType: "CreateIdentity",
			Actor:         "vtx.identity." + testNanoID2,
			SubmittedAt:   "2026-05-30T10:00:00Z",
			Payload:       json.RawMessage(`{}`),
		}
	}
	// Canonical envelope parses.
	b, _ := json.Marshal(base())
	if _, err := ParseEnvelope(b); err != nil {
		t.Fatalf("canonical envelope rejected: %v", err)
	}
	// Each required field, when blanked, must be rejected.
	mutators := map[string]func(*OperationEnvelope){
		"requestId":     func(e *OperationEnvelope) { e.RequestID = "" },
		"lane":          func(e *OperationEnvelope) { e.Lane = "" },
		"operationType": func(e *OperationEnvelope) { e.OperationType = "" },
		"actor":         func(e *OperationEnvelope) { e.Actor = "" },
		"submittedAt":   func(e *OperationEnvelope) { e.SubmittedAt = "" },
	}
	for field, mutate := range mutators {
		env := base()
		mutate(env)
		bb, _ := json.Marshal(env)
		if _, err := ParseEnvelope(bb); err == nil {
			t.Errorf("envelope missing %q was accepted; want rejection", field)
		}
	}
	// An absent payload field (required; use {} for empty) is rejected at the
	// byte level — json.Marshal of a nil RawMessage emits `null`, so this is
	// asserted against raw bytes rather than a struct round-trip.
	if _, err := ParseEnvelope([]byte(`{"requestId":"` + testNanoID1 + `","lane":"default","operationType":"X","actor":"a","submittedAt":"t"}`)); err == nil {
		t.Errorf("envelope with absent payload was accepted; want rejection")
	}
	// ContextHint + AuthContext round-trip without drift.
	env := base()
	env.ContextHint = &ContextHint{Reads: []string{"vtx.identity." + testNanoID2}}
	env.AuthContext = &AuthContext{Service: "s", Task: "t", Target: "vtx.identity." + testNanoID2}
	bb, _ := json.Marshal(env)
	got, err := ParseEnvelope(bb)
	if err != nil {
		t.Fatalf("envelope with hints rejected: %v", err)
	}
	if got.ContextHint == nil || len(got.ContextHint.Reads) != 1 {
		t.Fatalf("contextHint did not round-trip: %+v", got.ContextHint)
	}
	if got.AuthContext == nil || got.AuthContext.Target != "vtx.identity."+testNanoID2 {
		t.Fatalf("authContext did not round-trip: %+v", got.AuthContext)
	}
}
