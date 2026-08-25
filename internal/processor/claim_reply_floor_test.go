package processor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// ---- unit-tier fixtures for claimReplyFloor itself ----

// recordingPublisher captures every publish the quantizer performs, stamping the
// instant each one landed so a test can assert on release ordering and offsets.
type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
	at       []time.Time
	count    atomic.Int64
}

func (p *recordingPublisher) Publish(subject string, _ []byte) error {
	p.mu.Lock()
	p.subjects = append(p.subjects, subject)
	p.at = append(p.at, time.Now())
	p.mu.Unlock()
	p.count.Add(1)
	return nil
}

func (p *recordingPublisher) snapshot() ([]string, []time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.subjects...), append([]time.Time(nil), p.at...)
}

// countingHandler is a slog.Handler that counts WARN-and-above records whose
// message contains a substring, so a test can assert the drop path logged
// without scraping stderr.
type countingHandler struct {
	needle string
	hits   atomic.Int64
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn && strings.Contains(r.Message, h.needle) {
		h.hits.Add(1)
	}
	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

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

// workFor spends a fixed, deliberate interval inside a pipeline stage.
//
// It is not synchronising anything. Its DURATION is the quantity under test —
// it stands in for the per-cause work whose visibility in the reply timing is
// the whole subject of this file — which is exactly why it is a fixed sleep and
// not a condition wait.
func workFor(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

// slowAuthorizer authorizes every operation after spending `work`. It models the
// segment of dispatch BEFORE commitPipeline (parse, dedup, auth), which is the
// segment a receipt captured too late would silently exclude.
type slowAuthorizer struct{ work time.Duration }

func (a slowAuthorizer) Authorize(context.Context, *OperationEnvelope) (Decision, error) {
	workFor(a.work)
	return Decision{Authorized: true, Stub: true}, nil
}

// slowHydrator spends `work` in step 4 and then either faults or passes through,
// per `err`.
type slowHydrator struct {
	work time.Duration
	err  error
}

func (h slowHydrator) Hydrate(_ context.Context, _ *OperationEnvelope) (HydratedState, error) {
	workFor(h.work)
	return HydratedState{}, h.err
}

// slowExecutor spends `work` in step 5 and then fails with `err`.
//
// This is the callsite that matters: a real ClaimKeyInvalid is never produced at
// step 4. It is minted by classifyScriptError from the Starlark script's own
// fail(), which runs inside Executor.Execute — so commit_path.go's step-5
// handleStubFailure is the line production actually takes, and a test that only
// drives a stub Hydrator cannot see a regression there.
type slowExecutor struct {
	work time.Duration
	err  error
}

func (e slowExecutor) Execute(_ context.Context, _ *OperationEnvelope, _ HydratedState) (ScriptResult, error) {
	workFor(e.work)
	return ScriptResult{}, e.err
}

// noopCommitter satisfies the non-nil Committer requirement for pipelines whose
// operations always fail before step 8.
type noopCommitter struct{}

func (noopCommitter) Commit(context.Context, *OperationEnvelope, ScriptResult, Tracker) (CommitAck, error) {
	return CommitAck{}, errors.New("noopCommitter: step 8 is unreachable in this fixture")
}

// floorPipelineOpts configures a CommitPath for one anchoring/quantization arm.
type floorPipelineOpts struct {
	quantum time.Duration
	// Work injected into each of the three segments, so a test can place the
	// delay before the receipt-capture point, between it and step 5, or at
	// step 5 itself.
	authWork    time.Duration
	hydrateWork time.Duration
	executeWork time.Duration
	// hydrateErr, when set, rejects at step 4 instead of reaching step 5.
	hydrateErr error
	// executeErr defaults to a ClaimKeyInvalid ScriptError.
	executeErr error
	authorizer Authorizer
	metrics    *Metrics
}

func floorPipeline(t *testing.T, conn *substrate.Conn, o floorPipelineOpts) (*CommitPath, *Metrics) {
	t.Helper()
	metrics := o.metrics
	if metrics == nil {
		metrics = &Metrics{}
	}
	authz := o.authorizer
	if authz == nil {
		authz = slowAuthorizer{work: o.authWork}
	}
	execErr := o.executeErr
	if execErr == nil {
		execErr = claimKeyInvalidErr("fixture", "invalid-key")
	}
	cp := NewCommitPath(Deps{
		Conn:                conn,
		CoreBucket:          testCoreBucket,
		HealthKV:            testHealthBucket,
		Authorizer:          authz,
		Hydrator:            slowHydrator{work: o.hydrateWork, err: o.hydrateErr},
		Executor:            slowExecutor{work: o.executeWork, err: execErr},
		Committer:           noopCommitter{},
		Metrics:             metrics,
		Logger:              testLogger(),
		ClaimRejectionFloor: o.quantum,
	})
	return cp, metrics
}

// claimFloorReplyWait bounds the wait for a reply the commit path has already
// been driven to produce. A ceiling, not a timing assumption — hitting it is a
// dropped or never-published reply, not a slow runner.
const claimFloorReplyWait = 30 * time.Second

// dispatchAndTimeReply drives one envelope through cp.dispatch and returns the
// interval between the instant BEFORE dispatch was entered and the arrival of
// the reply, plus the reply itself.
//
// Measuring from before dispatch (rather than from the receipt instant dispatch
// captures internally) is the conservative direction: t0 <= receipt, so an
// observed offset that clears the quantum bounds the real receipt-to-publish
// offset from below by at least the quantum minus one function call.
func dispatchAndTimeReply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *CommitPath, env *OperationEnvelope) (time.Duration, MessageOutcome, *OperationReply) {
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

	t0 := time.Now()
	outcome, _ := cp.dispatch(ctx, msg)
	replyMsg, err := sub.NextMsg(claimFloorReplyWait)
	if err != nil {
		t.Fatalf("no reply on %s within %s (outcome %q): %v", inbox, claimFloorReplyWait, outcome, err)
	}
	elapsed := time.Since(t0)
	var reply OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	return elapsed, outcome, &reply
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

// ---- the release lattice ----

// TestReleaseAt_QuantizesRatherThanFloors pins the arithmetic the whole
// mechanism rests on, exactly, without any scheduler in the way.
//
// The property that matters is the LAST row: a plain floor answers
// max(receipt+Q, done), so work that outruns the quantum is answered at its own
// service time and leaks it in full. A caller can reach that in one request by
// padding contextHint.reads (opwire.MaxDeclaredReads allows 1000, the Gateway
// copies the hint verbatim, and every declared read resolves inside the
// window). Quantizing means the answer always lands on the lattice instead.
func TestReleaseAt_QuantizesRatherThanFloors(t *testing.T) {
	t.Parallel()
	const q = 50 * time.Millisecond
	receipt := time.Now()

	cases := []struct {
		name string
		work time.Duration
		want time.Duration // offset from receipt
	}{
		{"instant", 0, q},
		{"well inside the first quantum", 3 * time.Millisecond, q},
		{"just inside the first quantum", q - time.Nanosecond, q},
		{"exactly on the boundary", q, q},
		{"just past the boundary", q + time.Nanosecond, 2 * q},
		{"inside the second quantum", 70 * time.Millisecond, 2 * q},
		{"padded work that a floor would leak", 317 * time.Millisecond, 7 * q},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := releaseAt(receipt, receipt.Add(tc.work), q).Sub(receipt)
			if got != tc.want {
				t.Fatalf("releaseAt(work=%s) = receipt+%s, want receipt+%s", tc.work, got, tc.want)
			}
		})
	}

	t.Run("a done instant before receipt still waits a full quantum", func(t *testing.T) {
		got := releaseAt(receipt, receipt.Add(-time.Second), q).Sub(receipt)
		if got != q {
			t.Fatalf("got receipt+%s, want receipt+%s", got, q)
		}
	})
}

// TestClaimRejectionFloor_ReleaseIsAnchoredAtReceipt is the test that proves the
// fix rather than merely the delay, and it is parametrized over the three
// segments a receipt could be captured too late to cover.
//
// The discriminator is ALIGNMENT, not magnitude. Under correct receipt
// anchoring the answer lands on the lattice — elapsed is a whole number of
// quanta past arrival, so `elapsed mod quantum` is just the publish and delivery
// overhead. Under an anchor taken W later, elapsed becomes W + k*quantum and the
// modulus jumps to W. A lower-bound assertion cannot see this: anchoring LATE
// makes the reply later, not earlier.
//
// Each arm places its work so that exactly one mutation is fatal to it:
//
//	before-receipt  — kills moving `receipt := time.Now()` below dispatch's
//	                  parse/dedup/auth prologue.
//	step-4          — kills anchoring the hydrate callsite at time.Now().
//	step-5          — kills anchoring the execute callsite at time.Now(), which
//	                  is THE production callsite: a real ClaimKeyInvalid is
//	                  minted by classifyScriptError from the script's own fail(),
//	                  inside Executor.Execute, never at step 4.
func TestClaimRejectionFloor_ReleaseIsAnchoredAtReceipt(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// Work at half the quantum puts a mis-anchored reply as far from a lattice
	// boundary as it can get, which is the widest possible separation.
	const quantum = 200 * time.Millisecond
	const work = quantum / 2
	// Comfortably below `work` (so a mis-anchored release cannot slip through)
	// and comfortably above goroutine wakeup plus one embedded-NATS round trip.
	const tolerance = 60 * time.Millisecond

	arms := []struct {
		name string
		opts floorPipelineOpts
	}{
		{"work before the receipt anchor (dispatch prologue)", floorPipelineOpts{quantum: quantum, authWork: work}},
		{"work at step 4 (hydrate)", floorPipelineOpts{quantum: quantum, hydrateWork: work}},
		{"work at step 5 (execute) — the production callsite", floorPipelineOpts{quantum: quantum, executeWork: work}},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			cp, _ := floorPipeline(t, conn, arm.opts)
			elapsed, outcome, _ := dispatchAndTimeReply(t, ctx, conn, cp, nfrS6Envelope(t, "ClaimIdentity"))
			if outcome != OutcomeRejected {
				t.Fatalf("outcome = %q, want rejected", outcome)
			}
			if elapsed < quantum {
				t.Fatalf("answered after %s, before the first %s boundary", elapsed, quantum)
			}
			if offset := elapsed % quantum; offset > tolerance {
				t.Fatalf("reply landed %s past a %s lattice boundary (elapsed %s), tolerance %s — "+
					"%s of work is visible in the reply timing, so the release is anchored after that "+
					"work rather than at message receipt",
					offset, quantum, elapsed, tolerance, work)
			}
		})
	}
}

// TestClaimRejectionFloor_PaddedWorkCannotEscapeTheLattice is the availability
// half of the same property, at the commit-path level: a caller who inflates its
// own service time past the quantum does not get answered at its service time,
// it gets answered at the next boundary. The inline "already late, publish now"
// branch a plain floor needs is exactly the escape hatch this removes.
func TestClaimRejectionFloor_PaddedWorkCannotEscapeTheLattice(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	const quantum = 150 * time.Millisecond
	// Deliberately outruns the quantum, the way a padded contextHint.reads
	// would: a floor would answer at ~185ms and hand back the service time.
	const work = 185 * time.Millisecond
	const tolerance = 60 * time.Millisecond

	cp, metrics := floorPipeline(t, conn, floorPipelineOpts{quantum: quantum, executeWork: work})
	elapsed, _, _ := dispatchAndTimeReply(t, ctx, conn, cp, nfrS6Envelope(t, "ClaimIdentity"))

	if elapsed < 2*quantum {
		t.Fatalf("elapsed %s — work of %s must be answered in the SECOND quantum (%s), not at its own service time",
			elapsed, work, 2*quantum)
	}
	if offset := elapsed % quantum; offset > tolerance {
		t.Fatalf("reply landed %s past a %s boundary (elapsed %s) — padded work escaped the lattice",
			offset, quantum, elapsed)
	}
	// The operator-visible signal for exactly this condition.
	if got := metrics.ClaimFloorLate.Load(); got != 1 {
		t.Fatalf("ClaimFloorLate = %d, want 1 — work outran the quantum and nothing recorded it", got)
	}
}

// ---- scope: which rejections are covered ----

// TestClaimRejectionFloor_CoversTheNFRS6OperationSet pins that membership is
// keyed on the OPERATION, not on the error code the failure happened to produce.
//
// The step-4 arms are the ones that were broken: `.claimKey` is sensitive, so
// step 4 decrypts it during hydration, and a decrypt or parse fault there
// returns a bare fmt.Errorf (step4_hydrate.go) that classifies as
// ErrCodeInternalError. Under a code-keyed predicate that answer took the fast
// path with a distinct wire code, leaving a sealed-but-unclaimed identity
// distinguishable from a non-existent one by BOTH halves of NFR-S6.
func TestClaimRejectionFloor_CoversTheNFRS6OperationSet(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	const quantum = 200 * time.Millisecond
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
		opts floorPipelineOpts
	}{
		{"ClaimIdentity, script refusal at step 5", "ClaimIdentity", floorPipelineOpts{quantum: quantum}},
		{"ClaimIdentity, step-4 decrypt fault", "ClaimIdentity", floorPipelineOpts{quantum: quantum, hydrateErr: hydrateFault}},
		{"ClaimIdentity, step-4 closed-set refusal", "ClaimIdentity", floorPipelineOpts{quantum: quantum, hydrateErr: closedSetRefusal}},
		{"CompleteCredentialLink, script refusal at step 5", "CompleteCredentialLink", floorPipelineOpts{quantum: quantum}},
		{"CompleteCredentialLink, step-4 decrypt fault", "CompleteCredentialLink", floorPipelineOpts{quantum: quantum, hydrateErr: hydrateFault}},
		{"CompleteCredentialLink, step-4 closed-set refusal", "CompleteCredentialLink", floorPipelineOpts{quantum: quantum, hydrateErr: closedSetRefusal}},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			cp, _ := floorPipeline(t, conn, arm.opts)
			elapsed, outcome, reply := dispatchAndTimeReply(t, ctx, conn, cp, nfrS6Envelope(t, arm.op))
			if outcome != OutcomeRejected {
				t.Fatalf("outcome = %q, want rejected", outcome)
			}
			// Contract #9: "All failure modes collapse to the generic
			// ClaimKeyInvalid reply code ... specific outcomes surface only via
			// Health KV."
			if reply.Error == nil || reply.Error.Code != ErrCodeClaimKeyInvalid {
				t.Fatalf("error = %+v, want code %q", reply.Error, ErrCodeClaimKeyInvalid)
			}
			if len(reply.Error.Details) != 0 {
				t.Fatalf("error details = %+v, want none", reply.Error.Details)
			}
			if strings.Contains(reply.Error.Message, probedKey) {
				t.Fatalf("the reply message quotes the probed key: %q", reply.Error.Message)
			}
			if strings.Contains(reply.Error.Message, "step4") || strings.Contains(reply.Error.Message, "hydrate") ||
				strings.Contains(reply.Error.Message, "execute") {
				t.Fatalf("the reply message names the failing step, which separates a fault from a refusal: %q",
					reply.Error.Message)
			}
			if elapsed < quantum {
				t.Fatalf("answered after %s, before the %s quantum — this rejection is not covered", elapsed, quantum)
			}
		})
	}
}

// TestClaimRejectionFloor_LeavesOtherOperationsAlone is the other side of the
// same predicate: an operation outside the NFR-S6 set keeps its real error code
// and details, and is answered immediately. Collapsing every operation's
// rejection would cost debuggability everywhere to protect two operations.
func TestClaimRejectionFloor_LeavesOtherOperationsAlone(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// Large enough that any accidental coverage is unmissable.
	const quantum = 3 * time.Second
	const fastCeiling = 500 * time.Millisecond

	// Note the error is a ClaimKeyInvalid ScriptError, i.e. the exact error the
	// old code-keyed predicate matched on. The operation is not in the set, so
	// it must NOT be covered — this is what pins the predicate to the op.
	cp, metrics := floorPipeline(t, conn, floorPipelineOpts{quantum: quantum})
	elapsed, outcome, reply := dispatchAndTimeReply(t, ctx, conn, cp, nfrS6Envelope(t, "CreateIdentity"))
	if outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	if elapsed > fastCeiling {
		t.Fatalf("CreateIdentity answered after %s (ceiling %s) — a non-NFR-S6 operation is being quantized",
			elapsed, fastCeiling)
	}
	if reply.Error == nil || reply.Error.Code != ErrCodeClaimKeyInvalid {
		// classifyStepError still maps this ScriptError to the generic code; what
		// must NOT happen is the operation being delayed or its details stripped
		// by the NFR-S6 collapse.
		t.Fatalf("error = %+v, want the classifier's own mapping preserved", reply.Error)
	}
	if got := metrics.ClaimFloorApplied.Load(); got != 0 {
		t.Fatalf("ClaimFloorApplied = %d, want 0 for an operation outside the set", got)
	}
}

// TestClaimRejectionFloor_NonFlooredClassesStayFast pins the classes that are
// answered before any target-derived work happens at all. An auth denial and a
// malformed envelope reflect properties of the submitter's own request — its
// capability grant, its syntax — not of any claimed target, so they carry no
// identity information and must not pay the quantum.
func TestClaimRejectionFloor_NonFlooredClassesStayFast(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	const quantum = 3 * time.Second
	const fastCeiling = 500 * time.Millisecond

	t.Run("auth denied", func(t *testing.T) {
		cp, _ := floorPipeline(t, conn, floorPipelineOpts{quantum: quantum, authorizer: denyAuthorizer{}})
		elapsed, outcome, _ := dispatchAndTimeReply(t, ctx, conn, cp, nfrS6Envelope(t, "ClaimIdentity"))
		if outcome != OutcomeRejected {
			t.Fatalf("outcome = %q, want rejected", outcome)
		}
		if elapsed > fastCeiling {
			t.Fatalf("auth denial answered after %s (ceiling %s) — it is paying the quantum", elapsed, fastCeiling)
		}
	})

	t.Run("malformed envelope", func(t *testing.T) {
		cp, _ := floorPipeline(t, conn, floorPipelineOpts{quantum: quantum})
		inbox := nats.NewInbox()
		sub, err := conn.NATS().SubscribeSync(inbox)
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		defer func() { _ = sub.Unsubscribe() }()
		if err := conn.NATS().Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		t0 := time.Now()
		outcome, _ := cp.dispatch(ctx, substrate.Message{Body: []byte(`{"lane":"banana"}`), ReplySubject: inbox})
		if _, err := sub.NextMsg(claimFloorReplyWait); err != nil {
			t.Fatalf("no reply (outcome %q): %v", outcome, err)
		}
		if elapsed := time.Since(t0); elapsed > fastCeiling {
			t.Fatalf("malformed rejection answered after %s (ceiling %s) — it is paying the quantum", elapsed, fastCeiling)
		}
	})
}

// ---- bound, drain, config, counters ----

// TestClaimRejectionFloor_BoundDropsRatherThanAnsweringEarly pins the fail-safe
// direction at saturation. Answering early instead would hand back the timing
// signal at exactly the moment an attacker is generating the load that saturated
// the bound — which is when they are measuring.
func TestClaimRejectionFloor_BoundDropsRatherThanAnsweringEarly(t *testing.T) {
	t.Parallel()

	const quantum = 500 * time.Millisecond
	const excess = 200
	total := maxPendingDeferredReplies + excess

	handler := &countingHandler{needle: "dropping reply"}
	metrics := &Metrics{}
	f := newClaimReplyFloor(quantum, metrics, slog.New(handler))
	pub := &recordingPublisher{}

	start := time.Now()
	for i := 0; i < total; i++ {
		f.publishNoEarlierThan(pub, "inbox.bound", []byte(`{}`), start)
	}

	// Every submission reaches a terminal disposition — published or dropped —
	// so wait on that condition rather than on a duration.
	deadline := time.Now().Add(claimFloorReplyWait)
	for int(pub.count.Load())+int(metrics.ClaimFloorDropped.Load()) < total {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d deferred replies settled (published=%d dropped=%d)",
				int(pub.count.Load())+int(metrics.ClaimFloorDropped.Load()), total,
				pub.count.Load(), metrics.ClaimFloorDropped.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The exact split depends on how many waiting goroutines happened to drain a
	// pending slot while the loop was still submitting, so the invariants are
	// bounds, not equalities: nothing beyond the bound may be in flight at once,
	// and nothing may be silently lost.
	published, dropped := int(pub.count.Load()), int(metrics.ClaimFloorDropped.Load())
	if published+dropped != total {
		t.Fatalf("published %d + dropped %d = %d, want %d — a reply was neither published nor accounted",
			published, dropped, published+dropped, total)
	}
	if dropped < 1 {
		t.Fatalf("nothing was dropped submitting %d against a bound of %d", total, maxPendingDeferredReplies)
	}
	if published > total-excess {
		t.Fatalf("published %d, which is more than the bound (%d) could hold", published, maxPendingDeferredReplies)
	}
	if got := metrics.ClaimFloorApplied.Load(); int(got) != total {
		t.Fatalf("ClaimFloorApplied = %d, want %d (every submission is counted, dropped or not)", got, total)
	}
	// Rate-limited: a warning per dropped reply would be attacker-driven log
	// amplification, since nothing rate-limits the endpoint that produces them.
	hits := int(handler.hits.Load())
	if hits < 1 {
		t.Fatalf("%d drops logged nothing at all", dropped)
	}
	if hits > dropped/claimFloorDropLogEvery+1 {
		t.Fatalf("logged %d warnings for %d drops, want at most one per %d", hits, dropped, claimFloorDropLogEvery)
	}

	// The replies that WERE published must still have honoured the lattice: the
	// bound must not become a shortcut past it.
	_, at := pub.snapshot()
	firstBoundary := start.Add(quantum)
	for i, ts := range at {
		if ts.Before(firstBoundary) {
			t.Fatalf("reply %d published %s before the first boundary", i, firstBoundary.Sub(ts))
		}
	}
	if f.pending.Load() != 0 {
		t.Fatalf("pending = %d after every reply settled, want 0", f.pending.Load())
	}
}

// TestClaimReplyFloor_DrainFlushesAcceptedReplies pins the shutdown contract.
// Deferred replies are created per rejection, carried only in their waiting
// goroutine, and flushed here; without the flush a SIGTERM or rolling deploy
// discards every reply still inside its quantum.
func TestClaimReplyFloor_DrainFlushesAcceptedReplies(t *testing.T) {
	t.Parallel()

	const quantum = 150 * time.Millisecond
	const n = 32

	f := newClaimReplyFloor(quantum, &Metrics{}, testLogger())
	pub := &recordingPublisher{}
	start := time.Now()
	for i := 0; i < n; i++ {
		f.publishNoEarlierThan(pub, "inbox.drain", []byte(`{}`), start)
	}
	if got := pub.count.Load(); got != 0 {
		t.Fatalf("published %d replies before the quantum elapsed", got)
	}

	if !f.Drain(claimFloorReplyWait) {
		t.Fatalf("Drain reported an incomplete flush within %s", claimFloorReplyWait)
	}
	if got := int(pub.count.Load()); got != n {
		t.Fatalf("after Drain, published %d of %d accepted replies", got, n)
	}
	if elapsed := time.Since(start); elapsed < quantum {
		t.Fatalf("Drain returned after %s, before the %s quantum — it published early rather than waiting", elapsed, quantum)
	}

	t.Run("an expired budget reports incomplete rather than blocking", func(t *testing.T) {
		slow := newClaimReplyFloor(10*time.Second, &Metrics{}, testLogger())
		slow.publishNoEarlierThan(&recordingPublisher{}, "inbox.slow", []byte(`{}`), time.Now())
		if slow.Drain(50 * time.Millisecond) {
			t.Fatalf("Drain reported a complete flush with a reply still 10s from release")
		}
	})

	t.Run("a disabled quantizer drains instantly", func(t *testing.T) {
		if !newClaimReplyFloor(-1, &Metrics{}, testLogger()).Drain(0) {
			t.Fatalf("a disabled quantizer must report a complete drain")
		}
	})
}

// TestClaimRejectionFloor_ConfigPaths pins the two documented Deps values: zero
// takes the production default, negative disables the mechanism outright (the
// posture the timing instrument needs to observe the raw per-cause gap).
func TestClaimRejectionFloor_ConfigPaths(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	t.Run("zero takes the default", func(t *testing.T) {
		cp, metrics := floorPipeline(t, conn, floorPipelineOpts{quantum: 0})
		if cp.deps.ClaimRejectionFloor != DefaultClaimRejectionFloor {
			t.Fatalf("deps.ClaimRejectionFloor = %s, want the default %s",
				cp.deps.ClaimRejectionFloor, DefaultClaimRejectionFloor)
		}
		if cp.claimFloor.quantum != DefaultClaimRejectionFloor {
			t.Fatalf("claimFloor.quantum = %s, want %s", cp.claimFloor.quantum, DefaultClaimRejectionFloor)
		}
		elapsed, _, _ := dispatchAndTimeReply(t, ctx, conn, cp, nfrS6Envelope(t, "ClaimIdentity"))
		if elapsed < DefaultClaimRejectionFloor {
			t.Fatalf("answered after %s, before the default %s quantum", elapsed, DefaultClaimRejectionFloor)
		}
		if got := metrics.ClaimFloorApplied.Load(); got != 1 {
			t.Fatalf("ClaimFloorApplied = %d, want 1", got)
		}
		if got := metrics.ClaimFloorLate.Load(); got != 0 {
			t.Fatalf("ClaimFloorLate = %d, want 0 — trivial work must land in the first quantum", got)
		}
	})

	t.Run("negative disables", func(t *testing.T) {
		// A large quantum, so "disabled" is measured against something that
		// would otherwise be unmissable rather than against the 50ms default.
		const wouldBe = 3 * time.Second
		const fastCeiling = 500 * time.Millisecond
		cp, metrics := floorPipeline(t, conn, floorPipelineOpts{quantum: -wouldBe})
		if cp.deps.ClaimRejectionFloor != -wouldBe {
			t.Fatalf("deps.ClaimRejectionFloor = %s, want the negative disable sentinel preserved",
				cp.deps.ClaimRejectionFloor)
		}
		elapsed, _, _ := dispatchAndTimeReply(t, ctx, conn, cp, nfrS6Envelope(t, "ClaimIdentity"))
		if elapsed > fastCeiling {
			t.Fatalf("answered after %s (ceiling %s) with the quantizer disabled", elapsed, fastCeiling)
		}
		if got := metrics.ClaimFloorApplied.Load(); got != 0 {
			t.Fatalf("ClaimFloorApplied = %d, want 0 while disabled", got)
		}
	})
}

// TestMakePipeline_LeavesClaimRejectionFloorEnabled pins the composition root.
// The quantizer is a security mechanism with no production knob, so the property
// that matters is that the deployed wiring cannot silently regress to off:
// MakePipeline is what cmd/processor/main.go calls, and it must yield a
// CommitPath whose quantum is the ratified default.
func TestMakePipeline_LeavesClaimRejectionFloorEnabled(t *testing.T) {
	t.Parallel()
	_, conn, _, _, _ := setupTestPipeline(t)

	cp, _, err := MakePipeline(conn, testCoreBucket, testHealthBucket, "", AuthModeStub, false,
		testLogger(), "proc-floor-pin", AuthWiring{}, nil)
	if err != nil {
		t.Fatalf("MakePipeline: %v", err)
	}
	if cp.deps.ClaimRejectionFloor != DefaultClaimRejectionFloor {
		t.Fatalf("production wiring built a CommitPath with ClaimRejectionFloor = %s, want %s",
			cp.deps.ClaimRejectionFloor, DefaultClaimRejectionFloor)
	}
	if cp.claimFloor == nil || cp.claimFloor.quantum <= 0 {
		t.Fatalf("production wiring left the release quantizer disabled")
	}
	// Shutdown calls this; it must be safe on a path that has deferred nothing.
	if !cp.DrainClaimReplies(time.Second) {
		t.Fatalf("DrainClaimReplies reported an incomplete flush on an idle path")
	}
}

// TestClaimReplyFloor_NilReceiverIsInert guards the struct-literal CommitPaths
// in this package (custody_test.go, autocomplete_integration_test.go,
// step65_encrypt_test.go) that bypass NewCommitPath and so carry no quantizer.
// None of them drives an NFR-S6 rejection today; if one ever does, it must not
// panic, and it must not answer at the un-quantized offset either.
func TestClaimReplyFloor_NilReceiverIsInert(t *testing.T) {
	t.Parallel()
	var f *claimReplyFloor
	f.publishNoEarlierThan(&recordingPublisher{}, "inbox.nil", []byte(`{}`), time.Now())
	if !f.Drain(time.Second) {
		t.Fatalf("Drain on a nil quantizer must report complete")
	}

	cp := &CommitPath{deps: Deps{Logger: testLogger(), Metrics: &Metrics{}}}
	cp.replyToNoEarlierThan(substrate.Message{ReplySubject: "inbox.nil"},
		BuildRejectedReply("req", ErrCodeClaimKeyInvalid, claimRejectionMessage, nil), time.Now())
}
