// The wiring half of the validated-target primitive: `authTargetValidated` is a
// pure function of the step-3 decision (proven exhaustively in
// auth_target_validated_test.go), but it only protects anything if the commit
// path actually stamps it onto the envelope the executor runs against — once,
// before the OCC retry loop, so a re-execution cannot see a different answer.
package processor

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// atvFakeAuthorizer authorizes every op with a caller-chosen resolved
// permission, so a wiring test can drive each auth path without standing up a
// Capability-KV fixture per shape.
type atvFakeAuthorizer struct{ resolved *ResolvedPermission }

func (a atvFakeAuthorizer) Authorize(_ context.Context, _ *OperationEnvelope) (Decision, error) {
	return Decision{Authorized: true, Resolved: a.resolved}, nil
}

// atvCapturingExecutor records the envelope bit as the executor sees it, once
// per execution attempt — so a retry appends a second observation.
type atvCapturingExecutor struct {
	result ScriptResult
	seen   []bool
	calls  atomic.Uint64
}

func (e *atvCapturingExecutor) Execute(_ context.Context, env *OperationEnvelope, _ HydratedState) (ScriptResult, error) {
	e.calls.Add(1)
	e.seen = append(e.seen, env.AuthTargetValidated)
	muts := make([]MutationOp, len(e.result.Mutations))
	copy(muts, e.result.Mutations)
	return ScriptResult{Mutations: muts, Events: e.result.Events, PrimaryKey: e.result.PrimaryKey}, nil
}

func atvDrive(t *testing.T, ctx context.Context, cp *CommitPath, requestID string, ac *AuthContext) {
	t.Helper()
	env := newTestEnvelope(requestID)
	env.AuthContext = ac
	// A client asserting the bit on the wire must not reach the executor with
	// it set — the same property auth_target_validated_test.go pins at parse,
	// asserted here through the real dispatch path.
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	asMap["authTargetValidated"] = true
	body, err := json.Marshal(asMap)
	if err != nil {
		t.Fatalf("re-marshal envelope: %v", err)
	}
	msg := substrate.Message{
		Subject: "ops.default",
		Body:    body,
		Header:  func(string) string { return "" },
	}
	if outcome, _ := cp.dispatch(ctx, msg); outcome != OutcomeAccepted {
		t.Fatalf("dispatch outcome = %v, want Accepted", outcome)
	}
}

// TestCommitPath_StampsAuthTargetValidated walks each auth path through the real
// dispatch and asserts the bit the EXECUTOR observes — the value every
// packages/* guard actually reads.
func TestCommitPath_StampsAuthTargetValidated(t *testing.T) {
	cases := []struct {
		name     string
		resolved *ResolvedPermission
		ac       *AuthContext
		want     bool
	}{
		{
			name:     "platform scope=self, target == actor",
			resolved: &ResolvedPermission{Path: "platform", PlatformPermission: &PlatformPermission{Scope: "self"}},
			ac:       &AuthContext{Target: "vtx.identity." + testNanoID2},
			want:     true,
		},
		{
			name:     "task grant naming a target",
			resolved: &ResolvedPermission{Path: "task", EphemeralGrant: &EphemeralGrant{Target: "vtx.workorder." + testNanoID2}},
			ac:       &AuthContext{Task: "vtx.task." + testNanoID2, Target: "vtx.workorder." + testNanoID2},
			want:     true,
		},
		{
			name:     "platform scope=any with a forged target",
			resolved: &ResolvedPermission{Path: "platform", PlatformPermission: &PlatformPermission{Scope: "any"}},
			ac:       &AuthContext{Target: "vtx.workorder." + testNanoID2},
			want:     false,
		},
		{
			name:     "service path",
			resolved: &ResolvedPermission{Path: "service", ServiceAccess: &ServiceAccessEntry{Service: "vtx.service." + testNanoID2}},
			ac:       &AuthContext{Service: "vtx.service." + testNanoID2, Target: "vtx.workorder." + testNanoID2},
			want:     false,
		},
		{
			name:     "stub authorizer — no resolved permission",
			resolved: nil,
			ac:       &AuthContext{Target: "vtx.workorder." + testNanoID2},
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			conn := occConn(t)
			provisionHarness(t, ctx, conn)

			key := "vtx.identity." + testNanoID2 + ".profile"
			state, result := occUpdateState(t, ctx, conn, key)
			exec := &atvCapturingExecutor{result: result}
			cp := newOCCPipelineAuth(t, conn, atvFakeAuthorizer{tc.resolved},
				occFakeHydrator{state}, exec, &occFakeCommitter{conn: conn}, &Metrics{})

			atvDrive(t, ctx, cp, testNanoID2, tc.ac)

			if len(exec.seen) != 1 {
				t.Fatalf("expected exactly one execution; got %d", len(exec.seen))
			}
			if exec.seen[0] != tc.want {
				t.Fatalf("executor saw op.authTargetValidated = %v, want %v", exec.seen[0], tc.want)
			}
		})
	}
}

// TestCommitPath_AuthTargetValidatedStableAcrossOCCRetry pins the §10
// implementation note: the bit is stamped ONCE before the retry loop, which
// re-executes the script without re-running auth. A per-attempt recomputation
// (or a set inside the loop) would let the second pass see a different answer
// than the one the operation was authorized under.
func TestCommitPath_AuthTargetValidatedStableAcrossOCCRetry(t *testing.T) {
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	key := "vtx.identity." + testNanoID2 + ".profile"
	state, result := occUpdateState(t, ctx, conn, key)
	exec := &atvCapturingExecutor{result: result}
	// Conflict once (bumping the conditioned key) so the pipeline re-executes.
	committer := &occFakeCommitter{conn: conn, failFor: 1, bumpKey: key}
	cp := newOCCPipelineAuth(t, conn,
		atvFakeAuthorizer{&ResolvedPermission{Path: "platform", PlatformPermission: &PlatformPermission{Scope: "self"}}},
		occFakeHydrator{state}, exec, committer, &Metrics{})

	atvDrive(t, ctx, cp, testNanoID2, &AuthContext{Target: "vtx.identity." + testNanoID2})

	if len(exec.seen) < 2 {
		t.Fatalf("expected the OCC conflict to force a re-execution; got %d execution(s)", len(exec.seen))
	}
	for i, got := range exec.seen {
		if !got {
			t.Fatalf("execution attempt %d saw op.authTargetValidated = false; the bit must be settled "+
				"once before the retry loop, so every attempt runs under the answer step 3 authorized", i)
		}
	}
}
