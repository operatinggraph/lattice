package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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

func (noopCommitter) ReadPrior(context.Context, []MutationOp) (PriorDocs, error) {
	return PriorDocs{}, nil
}

func (noopCommitter) Commit(context.Context, *OperationEnvelope, ScriptResult, Tracker, PriorDocs) (CommitAck, error) {
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

// recordingClaimEmitter captures the outcome words the commit path records, so
// a test can assert the counter's content rather than only its wire reply.
type recordingClaimEmitter struct {
	mu       sync.Mutex
	outcomes []string
}

func (r *recordingClaimEmitter) RecordClaimAttempt(_ context.Context, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
}

func (r *recordingClaimEmitter) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.outcomes...)
}

// TestNFRS6_EveryCollapsedRejectionIsAccounted pins that a rejection hidden
// behind the generic wire shape is never also hidden from the operator.
//
// The two are the same act. replyRejection is where an NFR-S6 cause stops being
// visible to the caller, so it is where the cause has to become visible to
// Health KV instead — Contract #9 §9.3 moves specifics to the counter, it does
// not delete them. A rejection that collapses the reply and records nothing is
// invisible on every channel at once: the caller is told a fixed sentence, and
// the operator sees neither a success nor a failure, so the documented
// brute-force signature (a climbing invalid-key against a flat success) reads
// exactly as it does when nothing is wrong.
//
// The arms cover both kinds of cause. A script refusal carries its own
// adjudicated word. A platform refusal — the operation never reached the script,
// or lost a commit race after it — has no such word and is counted as
// platform-refused rather than not at all.
func TestNFRS6_EveryCollapsedRejectionIsAccounted(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	arms := []struct {
		name    string
		op      string
		opts    rejectPipelineOpts
		want    string
		wantAny bool
	}{
		{
			name: "a script refusal is counted under the word the script adjudicated",
			op:   "ClaimIdentity",
			opts: rejectPipelineOpts{executeErr: claimKeyInvalidErr("fixture", "wrong-state")},
			want: "wrong-state",
		},
		{
			name: "the same holds for the set's other member, not just ClaimIdentity",
			op:   "CompleteCredentialLink",
			opts: rejectPipelineOpts{executeErr: claimKeyInvalidErr("fixture", "invalid-key")},
			want: "invalid-key",
		},
		{
			name: "a fault the script never reached is counted as an internal fault",
			op:   "ClaimIdentity",
			opts: rejectPipelineOpts{hydrateErr: errors.New("step4: decrypt: vault: decrypt failed")},
			want: claimOutcomeInternalFault,
		},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			rec := &recordingClaimEmitter{}
			cp := rejectPipeline(t, conn, arm.opts)
			cp.deps.ClaimEmitter = rec

			outcome, reply := dispatchAndReply(t, ctx, conn, cp, nfrS6Envelope(t, arm.op))
			if outcome != OutcomeRejected {
				t.Fatalf("outcome = %q, want Rejected — this arm must reach the collapse to mean anything", outcome)
			}
			// The positive vector: the reply really did collapse, so the counter
			// is genuinely the only remaining channel for the cause.
			if reply.Error == nil || reply.Error.Code != ErrCodeClaimKeyInvalid {
				t.Fatalf("reply code = %+v, want the collapsed %s — the arm did not exercise the NFR-S6 path",
					reply.Error, ErrCodeClaimKeyInvalid)
			}

			seen := rec.seen()
			if len(seen) != 1 {
				t.Fatalf("recorded %v to claim-attempts, want exactly one outcome: a collapsed rejection "+
					"that records nothing is invisible to the caller AND the operator, and one that "+
					"records twice double-counts the plane an operator reads for a brute-force signature", seen)
			}
			if seen[0] != arm.want {
				t.Errorf("recorded outcome %q, want %q", seen[0], arm.want)
			}
		})
	}
}

// TestNFRS6_PlatformRefusalIsCounted pins the leg with no script word of its own.
//
// A DDL violation, protected-key or package-scope refusal, an oversized batch or
// an exhausted revision conflict all collapse to the same generic reply as a
// wrong claim key, and none of them carries an adjudicated outcome. Without a
// bucket they vanish: a conflict storm on the claim plane would move no number
// at all while every counter an operator watches stayed flat.
func TestNFRS6_PlatformRefusalIsCounted(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	rec := &recordingClaimEmitter{}
	cp := rejectPipeline(t, conn, rejectPipelineOpts{})
	cp.deps.ClaimEmitter = rec

	env := nfrS6Envelope(t, "ClaimIdentity")
	msg := messageFromEnvelope(t, env)
	cp.replyRejection(ctx, msg, env, ErrCodeRevisionConflict, "conflict on a hot key", nil, "")

	seen := rec.seen()
	if len(seen) != 1 || seen[0] != claimOutcomePlatformRefused {
		t.Fatalf("recorded %v, want exactly [%q]: a platform refusal of an NFR-S6 operation is "+
			"collapsed on the wire like every other cause, so leaving it out of the counter hides "+
			"it from the operator too", seen, claimOutcomePlatformRefused)
	}

	// A non-NFR-S6 operation keeps its real code and is not a claim attempt, so
	// it must not touch this counter at all.
	rec2 := &recordingClaimEmitter{}
	cp.deps.ClaimEmitter = rec2
	other := nfrS6Envelope(t, "CreateVertex")
	cp.replyRejection(ctx, messageFromEnvelope(t, other), other, ErrCodeRevisionConflict, "conflict", nil, "")
	if got := rec2.seen(); len(got) != 0 {
		t.Errorf("recorded %v for a non-NFR-S6 operation, want nothing — the counter is the claim "+
			"plane's, and padding it with unrelated rejections destroys the ratio it exists for", got)
	}
}
