// Env-gated timing instrument for ClaimIdentity's three rejection causes.
//
// NFR-S6 collapses every ClaimIdentity rejection to one wire shape
// (ErrCodeClaimKeyInvalid, no details) so a caller cannot enumerate which
// cause it hit from the reply alone (claim_test.go's
// TestClaimIdentity_RejectionCausesIndistinguishable is the residual proving
// that on the wire). What the wire shape cannot hide is TIME: the three
// causes do genuinely different amounts of work before answering.
//
//   - absent-target: the target vertex/state aspect are simply not present —
//     no crypto, the cheapest branch (ddls.go's `fail_claim("no-target")`
//     arms).
//   - already-claimed: a live, claimed identity whose `.claimKey` aspect is
//     tombstoned. The script's own state gate answers
//     (`fail_claim("wrong-state")`, ddls.go's `current_state == "claimed"`
//     arm) before ever touching the claim secret, but `.claimKey` is still
//     declared in the op's floored read set, and step 4 hydrates every
//     declared key up front regardless of which script branch eventually
//     fires (starlark_kv.go's kvModule doc: "a key declared in
//     contextHint.reads is hydrated at step 4"). For a TOMBSTONED sensitive
//     aspect, decryptSensitiveDoc's `doc.IsDeleted` arm
//     (sensitive_decrypt.go) scrubs the body and returns WITHOUT ever calling
//     readPiiKeyEnvelope or Vault.Decrypt — so this cause pays hydration for
//     the aspect but not a real decrypt.
//   - wrong-key: a live, unclaimed identity with a LIVE `.claimKey` aspect.
//     Step 4 hydrates it via the real envelope KVGet + AEAD decrypt path
//     (decryptSensitiveDoc's non-tombstone branch), and the script goes on to
//     hash the submitted secret and run `crypto.constant_time_equal` against
//     the stored hash before answering.
//
// A prior fire measured these three sequentially (n=40) and found no
// resolvable gap. This instrument re-measures under CONCURRENT submission —
// the shape a real prober would actually use — at a sample size large enough
// to extract a mean-difference bias smaller than the per-sample noise via
// bootstrap resampling (Phase C below).
//
// It also separates a confound discovered while building this instrument:
// EVERY ClaimIdentity rejection additionally pays a synchronous Health-KV
// read-modify-write on a PER-CAUSE key before the reply is sent
// (commit_path.go's handleStubFailure calls
// cp.deps.ClaimEmitter.RecordClaimAttempt at line ~976, then cp.replyTo;
// health_alerts.go's RecordClaimAttempt does a KVGet followed by a
// KVPutWithTTL, both synchronous, at health.processor.<instance>.
// claim-attempts.<outcome>). That term is per-cause and easily larger than
// the one KVGet + AEAD decrypt the tombstoned arm skips, so a measured gap
// under the deployed (emitter-on) shape alone cannot be attributed to the
// decrypt asymmetry with any confidence — it could just as well be emitter
// key contention. LATTICE_PROBE_EMITTER selects which pipeline shape(s) to
// measure so the two terms can be told apart.
//
// Gate: skips unless LATTICE_CLAIM_TIMING_PROBE is set — this file is inert
// in CI and every other normal `go test` invocation.
//
// Run it with:
//
//	LATTICE_CLAIM_TIMING_PROBE=1 go test ./packages/identity-domain/ \
//	  -run TestClaimRejectionTimingProbe -v -timeout 15m
//
// Tunables (all optional, parsed with strconv):
//
//	LATTICE_PROBE_WORKERS     concurrent cp.HandleMessage workers (default 2 —
//	                          the deployed `default`-lane worker count,
//	                          internal/processor/lanes.go's LaneConsumerDefaults)
//	LATTICE_PROBE_SUBMITTERS  concurrent client submitter goroutines (default 8)
//	LATTICE_PROBE_SAMPLES     samples PER CAUSE, per emitter arm (default 900)
//	LATTICE_PROBE_EMITTER     "on" | "off" | "both" (default "both") — which
//	                          ClaimEmitter wiring(s) to measure
//	LATTICE_PROBE_FLOOR       "off" (default) | "on" — whether the ClaimIdentity
//	                          rejection reply floor
//	                          (internal/processor/claim_reply_floor.go) is
//	                          engaged. OFF is the default because this
//	                          instrument's job is to expose the RAW per-cause
//	                          service-time gap; with the floor on, the gap it
//	                          would otherwise report is the very thing the floor
//	                          erases. Run it "on" to confirm the erasure: the
//	                          three causes' means collapse together and the
//	                          pairwise CIs stop excluding zero.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// probeReplyInboxHeader mirrors testutil's unexported replyInboxHeader
// (internal/testutil/pipeline.go) — the header the Processor reads to find a
// caller's reply subject for a JetStream-delivered op. Reproduced here by
// literal because it is unexported and this probe hand-rolls the publish
// SubmitAndAwaitReply otherwise does, to keep the subscribe→publish→reply
// window free of any helper overhead outside what is being timed.
const probeReplyInboxHeader = "Lattice-Reply-Inbox"

// probeFetchWait bounds one worker's pull. Small relative to driveFetchWait
// (embedded_nats.go) so idle workers notice shutdown promptly; large enough
// that a worker isn't churning empty round trips against the embedded server
// under load.
const probeFetchWait = 500 * time.Millisecond

// probeReplyWait bounds one submitter's wait for its reply. Generous: with
// SUBMITTERS > WORKERS the run is intentionally a closed queueing system
// (every submitter blocks on its own reply before sending its next request,
// so at most SUBMITTERS requests are ever in flight) and backlog against a
// small worker pool can add real queueing delay on top of service time.
const probeReplyWait = 30 * time.Second

// probeRunBudget bounds the whole instrument's own context.
//
// testutil.SetupPackageTestEnv hands back a 90-second context — a sane ceiling
// for an ordinary package test, and far too short for this one: a run is
// SAMPLES * 3 operations, and with the ClaimIdentity rejection floor engaged
// each of those costs at least processor.DefaultClaimRejectionFloor, so even a
// modest run outlives 90s and every subsequent JetStream publish fails with
// "context deadline exceeded" rather than producing a sample. The probe
// therefore runs on its own budget and leaves the real ceiling where the
// operator sets it, on `go test -timeout`.
const probeRunBudget = 2 * time.Hour

// probeBootstrapIterations is the resample count for the mean-difference
// confidence intervals in Phase C.
const probeBootstrapIterations = 5000

// probeBootstrapSeed fixes the resampling RNG so a report is reproducible
// given the same sample data.
const probeBootstrapSeed = 42

// probeSubjectOn is the physical NATS subject the emitter-ON arm publishes
// to and its consumer filters on — the plain default-lane subject every
// other identity-domain test uses, so createIdentityAndGetKeys (which
// publishes through testutil.PublishOp, hardcoding "ops."+Lane) can drive
// the wrong-key fixture's setup op through this same pipeline.
const probeSubjectOn = "ops." + string(processor.LaneDefault)

// probeSubjectOff is a DISTINCT physical subject (still under the
// "ops.>" wildcard the core-operations stream subscribes to, so the
// message is still captured; internal/testutil/pipeline.go's
// ProvisionHarness) for the emitter-OFF arm.
//
// A freshly created JetStream durable consumer defaults to delivering the
// WHOLE retained backlog on its filter subject, not just messages published
// after its own creation. Filtering the ON and OFF arms on the same
// "ops.default" subject would mean the OFF arm's brand-new consumer starts
// by replaying every message the ON arm already published (each landing as
// a step-2 dedup "duplicate" against Core KV, since the ON arm already
// committed/rejected that requestId) before ever reaching a genuinely new
// OFF-arm submission — starving the OFF arm's own workers and timing out its
// submitters. Lane authorization (step 3) reads `env.Lane` from the envelope
// body, not the physical NATS subject a message arrived on
// (commit_path.go's messageFromJetstream only uses msg.Subject() for
// diagnostics), so every OFF-arm envelope still declares Lane: LaneDefault
// and authorizes exactly as the ON arm's does; only the transport subject
// differs, keeping the two arms' backlogs from ever mixing.
const probeSubjectOff = probeSubjectOn + ".probe-off"

func TestClaimRejectionTimingProbe(t *testing.T) {
	if os.Getenv("LATTICE_CLAIM_TIMING_PROBE") == "" {
		t.Skip("set LATTICE_CLAIM_TIMING_PROBE=1 to run this instrument (env-gated, not part of CI); " +
			"see LATTICE_PROBE_WORKERS / LATTICE_PROBE_SUBMITTERS / LATTICE_PROBE_SAMPLES / LATTICE_PROBE_EMITTER")
	}

	workers := probeIntEnv(t, "LATTICE_PROBE_WORKERS", 2)
	submitters := probeIntEnv(t, "LATTICE_PROBE_SUBMITTERS", 8)
	samplesPerCause := probeIntEnv(t, "LATTICE_PROBE_SAMPLES", 900)
	arms := probeEmitterArms(t, "LATTICE_PROBE_EMITTER")
	floor := probeFloorSetting(t, "LATTICE_PROBE_FLOOR")

	// The harness seeds under its own bounded context and is done with it by the
	// time it returns; everything this instrument drives runs on probeRunBudget
	// instead (see the constant).
	_, conn := setupTestEnv(t)
	ctx, cancelRun := context.WithTimeout(context.Background(), probeRunBudget)
	t.Cleanup(cancelRun)

	// The emitter-ON pipeline is built first: Phase A's branch proof reads
	// Health KV counters that only exist when a ClaimEmitter is wired, and the
	// wrong-key fixture's own setup (a real CreateUnclaimedIdentity op) needs
	// some working pipeline to drive it through, so this one does double duty.
	cpOn, consOn, instanceOn := probePipeline(t, ctx, conn, "probeon", true, probeSubjectOn, floor)

	// ---- fixtures — the deployed shapes, not a synthetic shortcut ----

	absentTarget := "vtx.identity." + probeNanoID(t)

	// Mirrors TestClaimIdentity_AlreadyClaimed_GenericError (claim_test.go)
	// exactly: state=claimed AND a tombstoned .claimKey. Rejects via the
	// script's own current_state=="claimed" gate (ddls.go), which answers
	// before the secret is ever compared — the wrong-state counter is what
	// proves that below.
	alreadyClaimedTarget := "vtx.identity." + probeNanoID(t)
	seedDirectIdentity(t, ctx, conn, alreadyClaimedTarget, "claimed", "")
	seedSpentClaimKeyAspect(t, ctx, conn, alreadyClaimedTarget, sha256HexOf("probe-already-claimed-seed-secret"))

	// A live, unclaimed identity with a LIVE claimKey aspect — created through
	// a real CreateUnclaimedIdentity op (createIdentityAndGetKeys), not seeded
	// directly, so the aspect's ciphertext shape is exactly what step 6.5
	// produces on the write path.
	wrongKeyCreateReqID := testutil.GenReqID("ProbeWrKeyCrt0")
	wrongKeyTarget, _ := createIdentityAndGetKeys(t, ctx, conn, cpOn, consOn, wrongKeyCreateReqID)

	fixtures := []*probeFixture{
		{name: "absent-target", target: absentTarget, secret: "irrelevant-secret-absent-target", wantOutcome: "no-target"},
		{name: "already-claimed", target: alreadyClaimedTarget, secret: "irrelevant-secret-already-claimed", wantOutcome: "wrong-state"},
		{name: "wrong-key", target: wrongKeyTarget, secret: "definitely-the-wrong-secret-000001", wantOutcome: "invalid-key"},
	}
	for _, f := range fixtures {
		// The hand-rolled/descriptor-floored hint (fail-closed `reads`, not the
		// shipped identityceremony.ClaimContextHint's absence-tolerant
		// `optionalReads`) — the hostile envelope every hostile-arm test in
		// claim_test.go uses, built once and reused for every sample so the
		// same declared read set is timed sample to sample.
		f.hint = hardenedClaimHint(t, f.target)
	}

	// ---- Phase A — prove each fixture rejects for its own reason ----
	//
	// Emitter-ON only. The discriminator is the per-cause Health-KV counter
	// (readClaimHealthCounter, the same mechanism claim_test.go's own
	// AlreadyClaimed/WrongKey/RejectionCausesIndistinguishable tests use) —
	// those counters do not exist on the emitter-OFF pipeline, so this phase
	// cannot run there. That is not a gap: which script branch a fixture
	// takes is a property of the fixture's KV shape and the op's declared
	// reads, neither of which depends on whether a ClaimEmitter is wired, so
	// proving the branch once (here, emitter-ON) and reusing the SAME fixture
	// definitions for the emitter-OFF arm is sound.
	for _, f := range fixtures {
		before, _ := readClaimHealthCounter(t, ctx, conn, instanceOn, f.wantOutcome)
		env := probeClaimEnvelope(t, testutil.GenReqID("ProbePhaseA0000"), f.target, f.secret, f.hint)
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cpOn, consOn, env)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("phase A %s: outcome = %q, want rejected", f.name, outcome)
		}
		assertGenericClaimRejection(t, reply)
		after, ok := readClaimHealthCounter(t, ctx, conn, instanceOn, f.wantOutcome)
		if !ok || after <= before {
			t.Fatalf("phase A %s: claim-attempts.%s did not increase (before=%d after=%d found=%v) — "+
				"the fixture never reached its expected branch, so timing it would measure the wrong thing",
				f.name, f.wantOutcome, before, after, ok)
		}
		t.Logf("phase A: %s confirmed via claim-attempts.%s (%d -> %d)", f.name, f.wantOutcome, before, after)
	}

	// Emitter-OFF pipeline, built only if an OFF arm was requested. Its own
	// durable + instance keep it from interfering with the ON pipeline's
	// consumer or counters.
	var cpOff *processor.CommitPath
	var consOff jetstream.Consumer
	for _, withEmitter := range arms {
		if !withEmitter {
			cpOff, consOff, _ = probePipeline(t, ctx, conn, "probeoff", false, probeSubjectOff, floor)
			break
		}
	}

	floorLabel := "floor=off"
	if floor == 0 {
		floorLabel = fmt.Sprintf("floor=on(%s)", processor.DefaultClaimRejectionFloor)
	}
	for _, withEmitter := range arms {
		label := "emitter=on " + floorLabel
		cp, cons := cpOn, consOn
		subject := probeSubjectOn
		if !withEmitter {
			label = "emitter=off " + floorLabel
			cp, cons = cpOff, consOff
			subject = probeSubjectOff
		}

		probeWarmup(t, ctx, conn, cp, cons, fixtures, 30, subject)
		results := probeRunConcurrent(t, ctx, conn, cp, cons, fixtures, workers, submitters, samplesPerCause, subject)
		probeReport(t, label, fixtures, results)
	}
}

// probeFixture is one ClaimIdentity rejection cause: the target it claims
// against, the secret it submits, the hint it declares, and the Health-KV
// outcome word Phase A expects to see increment.
type probeFixture struct {
	name        string
	target      string
	secret      string
	wantOutcome string
	hint        *processor.ContextHint
}

// probeIntEnv reads a positive-integer env var, defaulting when unset.
func probeIntEnv(t *testing.T, name string, def int) int {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		t.Fatalf("%s=%q: want a positive integer", name, v)
	}
	return n
}

// probeEmitterArms resolves LATTICE_PROBE_EMITTER to the emitter-wiring(s) to
// measure: true = emitter on, false = emitter off.
func probeEmitterArms(t *testing.T, name string) []bool {
	t.Helper()
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(name))); v {
	case "", "both":
		return []bool{true, false}
	case "on":
		return []bool{true}
	case "off":
		return []bool{false}
	default:
		t.Fatalf("%s=%q: want one of on|off|both", name, v)
		return nil
	}
}

// probeFloorSetting resolves LATTICE_PROBE_FLOOR to the
// processor.Deps.ClaimRejectionFloor value the probe's pipelines are built
// with: "off" (the default) yields the negative disable sentinel, "on" yields
// zero, which NewCommitPath resolves to processor.DefaultClaimRejectionFloor.
//
// Defaulting to OFF is what makes the instrument measure what it claims to:
// the floor's whole purpose is to make the three causes' completion times
// indistinguishable, so a probe run with it engaged reports the floor's
// effectiveness, not the underlying service-time gap.
func probeFloorSetting(t *testing.T, name string) time.Duration {
	t.Helper()
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(name))); v {
	case "", "off":
		return -1
	case "on":
		return 0
	default:
		t.Fatalf("%s=%q: want one of on|off", name, v)
		return 0
	}
}

// probeNanoID generates a fresh 20-char NanoID on the main test goroutine,
// failing the test on entropy-source error.
func probeNanoID(t *testing.T) string {
	t.Helper()
	id, err := keys.NewNanoID()
	if err != nil {
		t.Fatalf("keys.NewNanoID: %v", err)
	}
	return id
}

// probePipeline builds a CapabilityPipeline with the ClaimEmitter wired or
// not, per withEmitter. testutil.PipelineConfig only sets deps.ClaimEmitter
// when cfg.ClaimEmitter is non-nil (internal/testutil/pipeline.go), and
// commit_path.go's handleStubFailure only calls it when non-nil, so leaving
// cfg.ClaimEmitter unset is a genuine emitter-off path — no separate
// CommitPath wiring is needed to isolate the two arms.
//
// floor is threaded straight to processor.Deps.ClaimRejectionFloor: negative
// disables the rejection reply floor, zero takes its production default.
func probePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string, withEmitter bool, filterSubject string, floor time.Duration) (*processor.CommitPath, jetstream.Consumer, string) {
	t.Helper()
	instance := claimInstance + "-" + durable
	cfg := testutil.PipelineConfig{
		Durable:             durable,
		Instance:            instance,
		FilterSubjects:      []string{filterSubject},
		ClaimRejectionFloor: floor,
	}
	if withEmitter {
		cfg.ClaimEmitter = processor.NewClaimAttemptEmitter(conn, testutil.HarnessHealthBucket, instance, testutil.TestLogger())
	}
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, cfg)
	return cp, cons, instance
}

// buildClaimEnvelope constructs a ClaimIdentity envelope. Returns an error
// instead of calling t.Fatalf so it is safe to call from submitter
// goroutines, which are not the test goroutine.
func buildClaimEnvelope(reqID, target, secret string, hint *processor.ContextHint) (*processor.OperationEnvelope, error) {
	payload, err := json.Marshal(map[string]string{"claimKey": secret, "targetIdentityKey": target})
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		Payload:       payload,
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint:   hint,
	}, nil
}

// probeClaimEnvelope is buildClaimEnvelope for the main test goroutine —
// t.Fatalf on the (never expected) marshal error rather than threading one
// more error return through single-shot call sites.
func probeClaimEnvelope(t *testing.T, reqID, target, secret string, hint *processor.ContextHint) *processor.OperationEnvelope {
	t.Helper()
	env, err := buildClaimEnvelope(reqID, target, secret, hint)
	if err != nil {
		t.Fatalf("build claim envelope: %v", err)
	}
	return env
}

// probeSubmitAndAwaitReply is testutil.SubmitAndAwaitReply
// (internal/testutil/pipeline.go) with an explicit publish subject instead of
// one derived from env.Lane — needed because the emitter-OFF arm publishes on
// probeSubjectOff (see its doc comment), not the plain default-lane subject
// that helper hardcodes. Drives exactly one message through cp via
// testutil.DriveOne, which is itself subject-agnostic (it only pulls from the
// consumer passed to it, whose FilterSubjects was fixed at construction).
func probeSubmitAndAwaitReply(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope, subject string) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe %s: %v", inbox, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := conn.NATS().Flush(); err != nil {
		t.Fatalf("flush subscription: %v", err)
	}

	msg := &nats.Msg{Subject: subject, Data: b, Header: nats.Header{probeReplyInboxHeader: []string{inbox}}}
	if _, err := conn.JetStream().PublishMsg(ctx, msg); err != nil {
		t.Fatalf("publish to %s: %v", subject, err)
	}

	outcome := testutil.DriveOne(t, ctx, cp, cons, "")

	replyMsg, err := sub.NextMsg(probeReplyWait)
	if err != nil {
		t.Fatalf("no reply on %s within %s (outcome %q): %v", inbox, probeReplyWait, outcome, err)
	}
	var reply processor.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	return outcome, &reply
}

// probeWarmup runs perCause discarded submissions per fixture, sequentially
// on the main goroutine, before the timed concurrent run — so DDL-cache
// population, vault key fetch, and Starlark JIT effects land here rather than
// in the measured samples.
func probeWarmup(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, fixtures []*probeFixture, perCause int, subject string) {
	t.Helper()
	for i := 0; i < perCause; i++ {
		for _, f := range fixtures {
			env := probeClaimEnvelope(t, probeNanoID(t), f.target, f.secret, f.hint)
			probeSubmitAndAwaitReply(t, ctx, conn, cp, cons, env, subject)
		}
	}
}

// probeSubmitOnce publishes one ClaimIdentity op for fixture f on subject and
// returns the publish→reply latency. It hand-rolls
// testutil.SubmitAndAwaitReply's subscribe-before-publish sequencing
// (internal/testutil/pipeline.go) rather than calling it, because that helper
// also drives the consumer itself (DriveOne) — here the WORKER goroutines own
// that role, and the timed region must be exactly publish→reply, not
// publish→drive→reply.
func probeSubmitOnce(ctx context.Context, conn *substrate.Conn, f *probeFixture, subject string) (time.Duration, error) {
	reqID, err := keys.NewNanoID()
	if err != nil {
		return 0, fmt.Errorf("nanoid: %w", err)
	}
	env, err := buildClaimEnvelope(reqID, f.target, f.secret, f.hint)
	if err != nil {
		return 0, err
	}
	b, err := json.Marshal(env)
	if err != nil {
		return 0, fmt.Errorf("marshal envelope: %w", err)
	}

	// Subscribe before publish so the reply cannot be missed — mirrors
	// SubmitAndAwaitReply. This part is deliberately NOT timed.
	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		return 0, fmt.Errorf("subscribe: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := conn.NATS().Flush(); err != nil {
		return 0, fmt.Errorf("flush subscription: %w", err)
	}

	msg := &nats.Msg{
		Subject: subject,
		Data:    b,
		Header:  nats.Header{probeReplyInboxHeader: []string{inbox}},
	}

	t0 := time.Now()
	if _, err := conn.JetStream().PublishMsg(ctx, msg); err != nil {
		return 0, fmt.Errorf("publish: %w", err)
	}
	if _, err := sub.NextMsg(probeReplyWait); err != nil {
		return 0, fmt.Errorf("await reply: %w", err)
	}
	return time.Since(t0), nil
}

// probeRunConcurrent drives WORKERS cp.HandleMessage pumps against SUBMITTERS
// client goroutines publishing samplesPerCause*len(fixtures) ClaimIdentity
// ops, causes interleaved round-robin, and returns each fixture's collected
// latencies (indexed the same as fixtures).
func probeRunConcurrent(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	fixtures []*probeFixture, workerCount, submitterCount, samplesPerCause int, subject string) [][]time.Duration {
	t.Helper()
	n := len(fixtures)
	total := int64(samplesPerCause * n)

	var errMu sync.Mutex
	var probeErrs []error
	recordErr := func(err error) {
		errMu.Lock()
		probeErrs = append(probeErrs, err)
		errMu.Unlock()
	}

	done := make(chan struct{})
	var workerWG sync.WaitGroup
	workerWG.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workerWG.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				batch, err := cons.Fetch(1, jetstream.FetchMaxWait(probeFetchWait))
				if err != nil {
					recordErr(fmt.Errorf("worker fetch: %w", err))
					continue
				}
				for m := range batch.Messages() {
					cp.HandleMessage(ctx, m)
				}
				if err := batch.Error(); err != nil {
					recordErr(fmt.Errorf("worker fetch batch: %w", err))
				}
			}
		}()
	}

	var next int64 // shared round-robin cursor: causes[i%n] for the i-th claimed slot
	perGoroutine := make([][][]time.Duration, submitterCount)
	var submitWG sync.WaitGroup
	submitWG.Add(submitterCount)
	for s := 0; s < submitterCount; s++ {
		local := make([][]time.Duration, n)
		perGoroutine[s] = local
		go func(local [][]time.Duration) {
			defer submitWG.Done()
			for {
				i := atomic.AddInt64(&next, 1) - 1
				if i >= total {
					return
				}
				causeIdx := int(i % int64(n))
				lat, err := probeSubmitOnce(ctx, conn, fixtures[causeIdx], subject)
				if err != nil {
					recordErr(fmt.Errorf("%s: %w", fixtures[causeIdx].name, err))
					continue
				}
				local[causeIdx] = append(local[causeIdx], lat)
			}
		}(local)
	}

	// Every submission is synchronously reply-gated (a submitter cannot start
	// its next iteration until the current one's reply arrives), so every
	// published message is already fully processed and replied to by the time
	// every submitter goroutine has returned — it is safe to stop the workers
	// the instant this Wait returns.
	submitWG.Wait()
	close(done)
	workerWG.Wait()

	if len(probeErrs) > 0 {
		t.Fatalf("probe run: %d error(s), first: %v", len(probeErrs), probeErrs[0])
	}

	results := make([][]time.Duration, n)
	for s := 0; s < submitterCount; s++ {
		for c := 0; c < n; c++ {
			results[c] = append(results[c], perGoroutine[s][c]...)
		}
	}
	return results
}

// probeMS converts a duration to float64 milliseconds.
func probeMS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// probeMeanMS returns the arithmetic mean of samples in milliseconds.
func probeMeanMS(samples []time.Duration) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += probeMS(s)
	}
	return sum / float64(len(samples))
}

// probePercentile returns the q-quantile (0..1) of sorted (ascending) using
// the same ceiling nearest-rank convention as internal/processor/
// latency_ring.go's ringPercentile.
func probePercentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := int(float64(len(sorted))*q + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// probeResampleMeanMS draws len(samples) values from samples with
// replacement and returns their mean in milliseconds.
func probeResampleMeanMS(samples []time.Duration, rng *rand.Rand) float64 {
	n := len(samples)
	var sum float64
	for i := 0; i < n; i++ {
		sum += probeMS(samples[rng.Intn(n)])
	}
	return sum / float64(n)
}

// probeBootstrapMeanDiffCI returns a 95% bootstrap confidence interval (in
// milliseconds) on mean(a) - mean(b), resampling each side independently with
// replacement `iterations` times.
func probeBootstrapMeanDiffCI(a, b []time.Duration, iterations int, rng *rand.Rand) (lo, hi float64) {
	if len(a) == 0 || len(b) == 0 {
		return 0, 0
	}
	diffs := make([]float64, iterations)
	for i := 0; i < iterations; i++ {
		diffs[i] = probeResampleMeanMS(a, rng) - probeResampleMeanMS(b, rng)
	}
	sort.Float64s(diffs)
	loIdx := int(0.025 * float64(iterations))
	hiIdx := int(0.975*float64(iterations)) - 1
	if hiIdx >= iterations {
		hiIdx = iterations - 1
	}
	if hiIdx < loIdx {
		hiIdx = loIdx
	}
	return diffs[loIdx], diffs[hiIdx]
}

// probeReport prints the per-cause latency table and the pairwise
// bootstrap mean-difference confidence intervals for one emitter arm.
func probeReport(t *testing.T, label string, fixtures []*probeFixture, results [][]time.Duration) {
	t.Helper()
	t.Logf("==== claim rejection timing probe: %s ====", label)
	t.Logf("%-16s %8s %10s %10s %10s %10s %10s %10s", "cause", "n", "min(ms)", "mean(ms)", "p50(ms)", "p90(ms)", "p99(ms)", "max(ms)")

	sortedByCause := make([][]time.Duration, len(fixtures))
	for i, f := range fixtures {
		s := append([]time.Duration(nil), results[i]...)
		sort.Slice(s, func(a, b int) bool { return s[a] < s[b] })
		sortedByCause[i] = s
		if len(s) == 0 {
			t.Logf("%-16s %8d (no samples)", f.name, 0)
			continue
		}
		t.Logf("%-16s %8d %10.4f %10.4f %10.4f %10.4f %10.4f %10.4f",
			f.name, len(s),
			probeMS(s[0]), probeMeanMS(s),
			probeMS(probePercentile(s, 0.50)), probeMS(probePercentile(s, 0.90)), probeMS(probePercentile(s, 0.99)),
			probeMS(s[len(s)-1]))
	}

	byName := make(map[string]int, len(fixtures))
	for i, f := range fixtures {
		byName[f.name] = i
	}
	pairs := [][2]string{
		{"already-claimed", "wrong-key"},
		{"absent-target", "wrong-key"},
		{"absent-target", "already-claimed"},
	}
	rng := rand.New(rand.NewSource(probeBootstrapSeed))
	for _, pair := range pairs {
		i, iOK := byName[pair[0]]
		j, jOK := byName[pair[1]]
		if !iOK || !jOK {
			continue
		}
		a, b := sortedByCause[i], sortedByCause[j]
		if len(a) == 0 || len(b) == 0 {
			t.Logf("%s vs %s: insufficient samples for a bootstrap CI", pair[0], pair[1])
			continue
		}
		diff := probeMeanMS(a) - probeMeanMS(b)
		lo, hi := probeBootstrapMeanDiffCI(a, b, probeBootstrapIterations, rng)
		excludesZero := lo > 0 || hi < 0
		t.Logf("%s: mean(%s) - mean(%s) = %.4f ms, 95%% bootstrap CI [%.4f, %.4f] ms, excludes zero: %v",
			label, pair[0], pair[1], diff, lo, hi, excludesZero)
	}
}
