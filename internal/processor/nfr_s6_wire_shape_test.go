package processor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// ---- commit-path fixtures ----

// claimKeyInvalidErr is the error the real path produces: classifyScriptError
// (starlark_runner.go) mints exactly this from the script's
// fail("ClaimKeyInvalid: " + outcome) (identity-domain/ddls.go).
func claimKeyInvalidErr(rid, detail string) error {
	return &ScriptError{
		Code:               "ClaimKeyInvalid",
		Message:            "ClaimKeyInvalid",
		Detail:             detail,
		OperationRequestID: rid,
	}
}

// stubHydrator faults at step 4 when err is set, and otherwise passes through.
type stubHydrator struct{ err error }

func (h stubHydrator) Hydrate(_ context.Context, _ *OperationEnvelope) (HydratedState, error) {
	return HydratedState{}, h.err
}

// stubExecutor fails step 5 with err.
//
// This is the callsite that matters: a real ClaimKeyInvalid is never produced at
// step 4. It is minted by classifyScriptError from the Starlark script's own
// fail(), which runs inside Executor.Execute — so commit_path.go's step-5
// handleStubFailure is the line production actually takes, and a test that only
// drives a stub Hydrator cannot see a regression there.
type stubExecutor struct{ err error }

func (e stubExecutor) Execute(_ context.Context, _ *OperationEnvelope, _ HydratedState) (ScriptResult, error) {
	return ScriptResult{}, e.err
}

// noopCommitter satisfies the non-nil Committer requirement for pipelines whose
// operations always fail before step 8.
type noopCommitter struct{}

func (noopCommitter) Commit(context.Context, *OperationEnvelope, ScriptResult, Tracker) (CommitAck, error) {
	return CommitAck{}, errors.New("noopCommitter: step 8 is unreachable in this fixture")
}

// rejectPipelineOpts configures a CommitPath that rejects one envelope at a
// chosen stage.
type rejectPipelineOpts struct {
	// hydrateErr, when set, rejects at step 4 instead of reaching step 5.
	hydrateErr error
	// executeErr defaults to a ClaimKeyInvalid ScriptError.
	executeErr error
	authorizer Authorizer
}

func rejectPipeline(t *testing.T, conn *substrate.Conn, o rejectPipelineOpts) *CommitPath {
	t.Helper()
	authz := o.authorizer
	if authz == nil {
		authz = stubAuthorizerAllow{}
	}
	execErr := o.executeErr
	if execErr == nil {
		execErr = claimKeyInvalidErr("fixture", "invalid-key")
	}
	return NewCommitPath(Deps{
		Conn:       conn,
		CoreBucket: testCoreBucket,
		HealthKV:   testHealthBucket,
		Authorizer: authz,
		Hydrator:   stubHydrator{err: o.hydrateErr},
		Executor:   stubExecutor{err: execErr},
		Committer:  noopCommitter{},
		Metrics:    &Metrics{},
		Logger:     testLogger(),
	})
}

// stubAuthorizerAllow authorizes every operation, so the rejection under test is
// the one the pipeline's own stage produces.
type stubAuthorizerAllow struct{}

func (stubAuthorizerAllow) Authorize(context.Context, *OperationEnvelope) (Decision, error) {
	return Decision{Authorized: true, Stub: true}, nil
}

// nfrS6ReplyWait bounds the wait for a reply the commit path has already been
// driven to produce. A ceiling, not a timing assumption — hitting it means the
// reply was never published.
const nfrS6ReplyWait = 30 * time.Second

// dispatchAndReply drives one envelope through cp.dispatch and returns the
// branch outcome plus the reply the caller received.
func dispatchAndReply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *CommitPath, env *OperationEnvelope) (MessageOutcome, *OperationReply) {
	t.Helper()
	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe %s: %v", inbox, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := conn.NATS().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	msg := messageFromEnvelope(t, env)
	msg.ReplySubject = inbox

	outcome, _ := cp.dispatch(ctx, msg)
	replyMsg, err := sub.NextMsg(nfrS6ReplyWait)
	if err != nil {
		t.Fatalf("no reply on %s within %s (outcome %q): %v", inbox, nfrS6ReplyWait, outcome, err)
	}
	var reply OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	return outcome, &reply
}

// nfrS6Envelope builds an envelope for one of the operations whose rejections
// must be indistinguishable, with a fresh requestId so each submission is a
// genuine first delivery rather than a step-2 dedup hit.
func nfrS6Envelope(t *testing.T, operationType string) *OperationEnvelope {
	t.Helper()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("nanoid: %v", err)
	}
	env := newTestEnvelope(id)
	env.OperationType = operationType
	return env
}

// ---- scope: which rejections are collapsed ----

// TestNFRS6_CoversTheOperationSet pins that membership is keyed on the
// OPERATION, not on the error code the failure happened to produce.
//
// The step-4 arms are the ones that were broken: `.claimKey` is sensitive, so
// step 4 decrypts it during hydration, and a decrypt or parse fault there
// returns a bare fmt.Errorf (step4_hydrate.go) that classifies as
// ErrCodeInternalError. Under a code-keyed predicate that answer took the fast
// path with a distinct wire code, leaving a sealed-but-unclaimed identity
// distinguishable from a non-existent one on the wire.
func TestNFRS6_CoversTheOperationSet(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// A fault whose text quotes the probed key, exactly as step4_hydrate.go's
	// wrap does. Nothing of it may reach the caller.
	const probedKey = "vtx.identity.AbCdEfGhJjKmNpQrStUv.claimKey"
	hydrateFault := errors.New("step4: decrypt " + probedKey + ": vault: decrypt failed")
	// The closed declared-read set's refusal (descriptor_floor.go), minted by
	// the production function rather than hand-rolled: a submitter naming a key
	// its operation's descriptor does not is refused at the head of step 4, and
	// that refusal owes the SAME answer as every other cause. A distinct wire
	// code here would tell a caller its probe was a probe — a new oracle, and a
	// Contract #9 §9.3 violation, not a new feature.
	closedSetRefusal := refuseUndeclaredContextHint(&OperationEnvelope{
		RequestID:     testNanoID1,
		OperationType: "ClaimIdentity",
		ContextHint:   &ContextHint{OptionalReads: []string{probedKey}},
	}, nil, testLogger())
	if closedSetRefusal == nil {
		t.Fatal("the closed declared-read set admitted a key no descriptor names")
	}

	arms := []struct {
		name string
		op   string
		opts rejectPipelineOpts
	}{
		{"ClaimIdentity, script refusal at step 5", "ClaimIdentity", rejectPipelineOpts{}},
		{"ClaimIdentity, step-4 decrypt fault", "ClaimIdentity", rejectPipelineOpts{hydrateErr: hydrateFault}},
		{"ClaimIdentity, step-4 closed-set refusal", "ClaimIdentity", rejectPipelineOpts{hydrateErr: closedSetRefusal}},
		{"CompleteCredentialLink, script refusal at step 5", "CompleteCredentialLink", rejectPipelineOpts{}},
		{"CompleteCredentialLink, step-4 decrypt fault", "CompleteCredentialLink", rejectPipelineOpts{hydrateErr: hydrateFault}},
		{"CompleteCredentialLink, step-4 closed-set refusal", "CompleteCredentialLink", rejectPipelineOpts{hydrateErr: closedSetRefusal}},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			cp := rejectPipeline(t, conn, arm.opts)
			outcome, reply := dispatchAndReply(t, ctx, conn, cp, nfrS6Envelope(t, arm.op))
			if outcome != OutcomeRejected {
				t.Fatalf("outcome = %q, want rejected", outcome)
			}
			// Contract #9 §9.3: "All failure modes collapse to the generic
			// ClaimKeyInvalid reply code ... specific outcomes surface only via
			// Health KV."
			if reply.Error == nil || reply.Error.Code != ErrCodeClaimKeyInvalid {
				t.Fatalf("error = %+v, want code %q", reply.Error, ErrCodeClaimKeyInvalid)
			}
			if len(reply.Error.Details) != 0 {
				t.Fatalf("error details = %+v, want none", reply.Error.Details)
			}
			// One fixed message for every cause: a message that varied with the
			// cause would be the same oracle the code collapse closes.
			if reply.Error.Message != claimRejectionMessage {
				t.Fatalf("error message = %q, want the one fixed message %q", reply.Error.Message, claimRejectionMessage)
			}
			if strings.Contains(reply.Error.Message, probedKey) {
				t.Fatalf("the reply message quotes the probed key: %q", reply.Error.Message)
			}
			if strings.Contains(reply.Error.Message, "step4") || strings.Contains(reply.Error.Message, "hydrate") ||
				strings.Contains(reply.Error.Message, "execute") {
				t.Fatalf("the reply message names the failing step, which separates a fault from a refusal: %q",
					reply.Error.Message)
			}
		})
	}
}

// TestNFRS6_LeavesOtherOperationsAlone is the other side of the same predicate:
// an operation outside the NFR-S6 set keeps its real error code, details and
// message. Collapsing every operation's rejection would cost debuggability
// everywhere to protect two operations.
func TestNFRS6_LeavesOtherOperationsAlone(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	t.Run("a ClaimKeyInvalid script error on a non-member operation", func(t *testing.T) {
		// This is the exact error the old code-keyed predicate matched on. The
		// operation is not in the set, so it must NOT be collapsed — this is
		// what pins the predicate to the op rather than to the code.
		cp := rejectPipeline(t, conn, rejectPipelineOpts{})
		outcome, reply := dispatchAndReply(t, ctx, conn, cp, nfrS6Envelope(t, "CreateIdentity"))
		if outcome != OutcomeRejected {
			t.Fatalf("outcome = %q, want rejected", outcome)
		}
		// classifyStepError still maps this ScriptError to the generic code for
		// every operation; what must NOT happen is the collapse replacing the
		// classifier's own honest message with the fixed one.
		if reply.Error == nil || reply.Error.Code != ErrCodeClaimKeyInvalid {
			t.Fatalf("error = %+v, want the classifier's own mapping preserved", reply.Error)
		}
		if reply.Error.Message == claimRejectionMessage {
			t.Fatalf("a non-NFR-S6 operation was answered with the collapsed message %q", reply.Error.Message)
		}
		if !strings.Contains(reply.Error.Message, "step execute failed") {
			t.Fatalf("error message = %q, want the classifier's own step-naming message", reply.Error.Message)
		}
	})

	t.Run("a non-collapsed script error keeps its code and details", func(t *testing.T) {
		scriptErr := &ScriptError{
			Code:               "DomainRefusal",
			Message:            "the script refused",
			OperationRequestID: "fixture",
			Line:               12,
			Column:             3,
		}
		cp := rejectPipeline(t, conn, rejectPipelineOpts{executeErr: scriptErr})
		outcome, reply := dispatchAndReply(t, ctx, conn, cp, nfrS6Envelope(t, "CreateIdentity"))
		if outcome != OutcomeRejected {
			t.Fatalf("outcome = %q, want rejected", outcome)
		}
		if reply.Error == nil || reply.Error.Code != ErrCodeScriptFailed {
			t.Fatalf("error = %+v, want code %q", reply.Error, ErrCodeScriptFailed)
		}
		if got := reply.Error.Details["code"]; got != "DomainRefusal" {
			t.Fatalf("details[code] = %v, want the script's own code — details were stripped", got)
		}
		if got := reply.Error.Details["message"]; got != "the script refused" {
			t.Fatalf("details[message] = %v, want the script's own message", got)
		}
	})
}

// TestNFRS6_NonCollapsedClassesKeepTheirRealCodes pins the classes answered
// before any target-derived work happens at all. An auth denial and a malformed
// envelope reflect properties of the submitter's own request — its capability
// grant, its syntax — not of any claimed target, so they carry no identity
// information and keep their real wire codes even on an NFR-S6 operation.
func TestNFRS6_NonCollapsedClassesKeepTheirRealCodes(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	t.Run("auth denied", func(t *testing.T) {
		cp := rejectPipeline(t, conn, rejectPipelineOpts{authorizer: denyAuthorizer{}})
		outcome, reply := dispatchAndReply(t, ctx, conn, cp, nfrS6Envelope(t, "ClaimIdentity"))
		if outcome != OutcomeRejected {
			t.Fatalf("outcome = %q, want rejected", outcome)
		}
		if reply.Error == nil || reply.Error.Code != ErrCodeAuthDenied {
			t.Fatalf("error = %+v, want code %q — a pre-step-4 denial is not collapsed", reply.Error, ErrCodeAuthDenied)
		}
	})

	t.Run("malformed envelope", func(t *testing.T) {
		cp := rejectPipeline(t, conn, rejectPipelineOpts{})
		inbox := nats.NewInbox()
		sub, err := conn.NATS().SubscribeSync(inbox)
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		defer func() { _ = sub.Unsubscribe() }()
		if err := conn.NATS().Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		outcome, _ := cp.dispatch(ctx, substrate.Message{Body: []byte(`{"lane":"banana"}`), ReplySubject: inbox})
		replyMsg, err := sub.NextMsg(nfrS6ReplyWait)
		if err != nil {
			t.Fatalf("no reply (outcome %q): %v", outcome, err)
		}
		var reply OperationReply
		if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
			t.Fatalf("unmarshal reply: %v", err)
		}
		if reply.Error == nil || reply.Error.Code != ErrCodeEnvelopeMalformed {
			t.Fatalf("error = %+v, want code %q — a step-1 parse failure is not collapsed",
				reply.Error, ErrCodeEnvelopeMalformed)
		}
	})
}

// TestNFRS6_ClaimAttemptEmissionIsSymmetric pins that both legs of the
// claim-attempts counter are keyed on the equalized SET, never on one operation.
//
// The counter is the operator's only view of what an NFR-S6 operation did: the
// caller gets one fixed wire shape whatever happened, by construction. So an
// operation whose rejections are counted while its successes are not does not
// merely under-report — it reads as a flow that never succeeds, and a genuine
// failure spike is invisible against a baseline that is already total failure.
// The rejection leg (handleStubFailure) has always used isNFRS6Operation; the
// success leg is asserted here to match it, over the whole set rather than over
// the one operation a literal used to name.
func TestNFRS6_ClaimAttemptEmissionIsSymmetric(t *testing.T) {
	src, err := os.ReadFile("commit_path.go")
	if err != nil {
		t.Fatalf("read commit_path.go: %v", err)
	}
	text := string(src)

	// The positive vector first: both emission legs must actually be present,
	// or the assertions below pass over code that no longer exists.
	if n := strings.Count(text, "ClaimEmitter.RecordClaimAttempt("); n < 3 {
		t.Fatalf("found %d ClaimEmitter.RecordClaimAttempt call(s) in commit_path.go, want the 3 "+
			"emission legs (one success, two rejection) — this test is asserting over code that moved", n)
	}

	for _, op := range []string{"ClaimIdentity", "CompleteCredentialLink"} {
		if !isNFRS6Operation(op) {
			t.Fatalf("%s must be in the equalized set for this assertion to mean anything", op)
		}
		if strings.Contains(text, `env.OperationType == "`+op+`"`) {
			t.Errorf("commit_path.go gates a claim-attempt emission on the literal operation %q "+
				"instead of on isNFRS6Operation(env.OperationType).\n"+
				"Every operation in the equalized set answers its caller with one fixed wire shape, so "+
				"health.processor.<instance>.claim-attempts.<outcome> is the ONLY place its real outcome "+
				"becomes visible. Keying one leg on a single operation and the other on the set means the "+
				"set's other members have their failures counted and their successes dropped — the counter "+
				"then reports a working flow as totally failing, and hides a real spike behind that "+
				"baseline. Key both legs on isNFRS6Operation.", op)
		}
	}
}
