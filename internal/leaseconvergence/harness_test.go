//go:build leaseshortwindow

// Package leaseconvergence_test is the Epic-14 capstone end-to-end harness: the
// first test that boots Processor + Refractor + Loom + Weaver + the LIVE bridge
// together against one embedded-NATS server, installs the real package chain
// (rbac → identity → orchestration-base → service-domain → lease-signing), drives
// one lease application, and observes the full vertical converge to a stable
// steady state through the live bridge.
//
// It uses the real leaseapp/service types because it is the real vertical's e2e
// (invariant a is preserved by the type-neutral applyMatch unit test in
// internal/refractor/ruleengine/full, not by this harness). The Processor runs
// in AuthModeStub: the convergence proof is orchestration mechanics (Weaver →
// triggerLoom/assignTask → Loom externalTask → live bridge → replyOp → reproject
// → temporal freshness), not capability auth — the auth boundary is Gate 2/3.
//
// The WHOLE package is gated behind the `leaseshortwindow` build tag (M3): it
// compiles + runs ONLY under `make test-lease-convergence` (which passes
// -tags leaseshortwindow), never in the untagged `go test ./...`. That keeps the
// heavy all-engines e2e to its single dedicated gate, and it pairs with the
// short bgcheck freshness window (lease-signing/freshness_window_short.go) so a
// lapse is watchable in bounded wall-clock (the eager-reopen leg).
package leaseconvergence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/bridge"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/processor/outbox"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/weaver"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	leasesigning "github.com/asolgan/lattice/packages/lease-signing"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
)

const replyInboxHeader = "Lattice-Reply-Inbox"

// harness holds the running stack + the handles the tests assert against.
type harness struct {
	t      *testing.T
	ctx    context.Context
	conn   *substrate.Conn
	logger *slog.Logger

	coreKV  *substrate.KV
	convKV  *substrate.KV // weaver-targets
	bgFake  *bridge.FakeBackgroundCheck
	bgAsync *bridge.FakeAsyncCheck // registered for backgroundCheck when the async variant is selected
	stripe  *bridge.FakeStripe
	convRID string // the leaseApplicationComplete lens rule ID
}

// harnessConfig carries the optional horizon/adapter overrides the async
// convergence variant needs. A zero value reproduces the production-faithful
// synchronous harness (every duration left 0 → the engine's withDefaults applies
// the production default; bgcheckAsync nil → the synchronous FakeBackgroundCheck
// is registered for backgroundCheck), so the sync convergence tests pass no
// options and are unaffected.
type harnessConfig struct {
	// bgcheckAsync, when non-nil, is registered for the backgroundCheck adapter
	// in place of the synchronous FakeBackgroundCheck — the test-only async
	// vendor (production stays synchronous; cmd/bridge is unchanged).
	bgcheckAsync *bridge.FakeAsyncCheck
	// Weaver knobs: a short MarkLease + SweepInterval make the reconciler sweep
	// actually tick (and a mark's lease actually expire) within the test, so the
	// no-double-dispatch-across-a-sweep-tick assertion exercises skip site 2.
	weaverMarkLease     time.Duration
	weaverSweepInterval time.Duration
	weaverSweepWarmup   time.Duration
	// Bridge knobs: a short PollInterval makes the @at poll chain advance fast; a
	// short CallDeadline makes the timeout fire within the test (the failed →
	// retry leg).
	bridgePollInterval time.Duration
	bridgeCallDeadline time.Duration
}

// harnessOpt mutates the harnessConfig before the stack boots.
type harnessOpt func(*harnessConfig)

// newHarness boots embedded NATS, the real bootstrap substrate (all buckets +
// streams + primordial identities/roles via the real Seeder), installs the real
// package chain, then starts all five engines as goroutines under a test-scoped
// context. It returns once the convergence lens is activated and the engines'
// consumers are up. Options override adapter/horizon defaults for the async
// variant; with no options it is the production-faithful synchronous harness.
func newHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()

	var hc harnessConfig
	for _, opt := range opts {
		opt(&hc)
	}

	srvOpts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(srvOpts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	// The harness context must outlive the worst-case test: the multi-cycle eager
	// test (TestLeaseConvergence_BgcheckFreshness_EagerReopen) runs TWO real
	// freshness windows back-to-back (~2*freshnessWindow of pure lapse), plus boot
	// + install + the initial converge, plus a re-dispatch + re-converge after each
	// lapse. A flat 180s was a razor-thin margin against just the two 150s of
	// windows — the per-cycle 240s Eventually budgets were unreachable. Derive the
	// deadline from the window so it tracks the lens (boot/converge slack +
	// 2*window + generous per-cycle re-converge slack), landing ~8-9m: comfortably
	// above the eager worst case yet safely under the 10m make test-lease-convergence
	// gate. The steady-state / single-cycle tests finish in well under a minute, so
	// the larger ceiling never affects them — it is a deadline, not a sleep.
	harnessTimeout := 90*time.Second + 2*freshnessWindow + 4*60*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), harnessTimeout)
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	js := conn.JetStream()

	// --- real bootstrap substrate (buckets + streams + primordial identities) ---
	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err = bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	convKV, err := conn.OpenKV(ctx, bootstrap.WeaverTargetsBucket)
	require.NoError(t, err)

	h := &harness{t: t, ctx: ctx, conn: conn, logger: logger, coreKV: coreKV, convKV: convKV}

	// --- the Processor: one commit path consuming ALL lanes (ops.>), AuthModeStub.
	// It runs every DDL (kernel + the installed packages). The DDL cache refreshes
	// on meta-lane commits, so InstallPackage's meta-lane writes land before any
	// package op is submitted.
	cp, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "lc-processor")
	require.NoError(t, err)
	procCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "lc-processor",
		FilterSubjects: []string{"ops.default", "ops.urgent", "ops.system", "ops.meta"},
		AckWait:        10 * time.Second,
	}, logger)
	require.NoError(t, err)
	// The engines (Loom relay + Weaver actuator) now publish Processor-faithful
	// dispatch envelopes: they omit `class` and rely on the Processor's
	// operationType→class reverse index, and they declare
	// ContextHint.Reads for every op whose DDL hydrates OCC reads. So the
	// harness consumes the ops VERBATIM — no shim — and the convergence loop
	// commits through the REAL Processor exactly as production would.
	procCC, err := procCons.Consume(func(m jetstream.Msg) {
		cp.HandleMessage(ctx, m)
	})
	require.NoError(t, err)
	t.Cleanup(procCC.Stop)

	// The transactional-outbox publisher: the Processor commits events as a
	// vtx.op.<id>.events aspect in the atomic batch; this durable consumer
	// publishes them to core-events (events.<class>) and tombstones the aspect.
	// Without it, committed events (loom.patternStarted, orchestration.*,
	// external.<adapter>) never leave the outbox — the orchestration loop stalls.
	outboxC := outbox.New(conn, bootstrap.CoreKVBucket, logger)
	go func() { _ = outboxC.Run(ctx) }()

	// --- install the real chain via the real InstallPackage op path (ops.meta).
	h.installChain()

	// --- Refractor: activate the leaseApplicationComplete lens + its actorAggregate
	// projection through the production wiring (CoreKVSource watch +
	// projection.InstallActorAggregate), exactly as the scalar e2e + cmd/refractor.
	h.startAdjacencyBootstrapper(ctx, adjKV)
	h.startRefractor(ctx, adjKV, coreKV, convKV)

	// --- Loom: trigger/relay/deadline + per-domain completion consumers.
	loomEng := loom.NewEngine(conn, loom.Config{
		CoreKVBucket:    bootstrap.CoreKVBucket,
		LoomStateBucket: bootstrap.LoomStateBucket,
		EventsStream:    bootstrap.CoreEventsStreamName,
		HealthKVBucket:  bootstrap.HealthKVBucket,
		ActorKey:        bootstrap.LoomIdentityKey,
		Lane:            "system",
		HeartbeatEvery:  200 * time.Millisecond,
		Instance:        "lc-loom",
		Logger:          logger,
	})
	go func() { _ = loomEng.Start(ctx) }()

	// --- Weaver: lane-1 (gap dispatch) + lane-3 (temporal freshness @at). The
	// MarkLease/SweepInterval/SweepOrphanWarmup left zero take the production
	// defaults (withDefaults); the async variant shrinks them so the reconciler
	// sweep ticks and a mark's lease expires within the test (exercising the
	// sweep's re-dispatch path — skip site 2).
	weaverEng := weaver.NewEngine(conn, weaver.Config{
		CoreKVBucket:        bootstrap.CoreKVBucket,
		WeaverTargetsBucket: bootstrap.WeaverTargetsBucket,
		WeaverStateBucket:   bootstrap.WeaverStateBucket,
		HealthKVBucket:      bootstrap.HealthKVBucket,
		CoreSchedulesStream: bootstrap.CoreSchedulesStreamName,
		ActorKey:            bootstrap.WeaverIdentityKey,
		Lane:                "system",
		Instance:            "lc-weaver",
		HeartbeatEvery:      200 * time.Millisecond,
		MarkLease:           hc.weaverMarkLease,
		SweepInterval:       hc.weaverSweepInterval,
		SweepOrphanWarmup:   hc.weaverSweepWarmup,
		Logger:              logger,
	})
	go func() { _ = weaverEng.Start(ctx) }()

	// --- the LIVE bridge. The backgroundCheck adapter is the synchronous
	// FakeBackgroundCheck by default; the async variant swaps in a test-only
	// FakeAsyncCheck (production stays synchronous — cmd/bridge is unchanged). The
	// PollInterval/CallDeadline left zero take the production defaults; the async
	// variant shrinks them so the poll chain advances and the give-up timeout
	// fires within the test.
	h.bgFake = bridge.NewFakeBackgroundCheck()
	h.stripe = bridge.NewFakeStripe()
	bridgeEng := bridge.NewEngine(conn, bridge.Config{
		CoreKVBucket:    bootstrap.CoreKVBucket,
		EventsStream:    bootstrap.CoreEventsStreamName,
		HealthKVBucket:  bootstrap.HealthKVBucket,
		ActorKey:        bootstrap.BridgeIdentityKey,
		Lane:            "system",
		Instance:        "lc-bridge",
		HeartbeatEvery:  150 * time.Millisecond,
		RedeliveryDelay: 300 * time.Millisecond,
		PollInterval:    hc.bridgePollInterval,
		CallDeadline:    hc.bridgeCallDeadline,
	})
	if hc.bgcheckAsync != nil {
		h.bgAsync = hc.bgcheckAsync
		require.NoError(t, bridgeEng.RegisterAdapter("backgroundCheck", h.bgAsync))
	} else {
		require.NoError(t, bridgeEng.RegisterAdapter("backgroundCheck", h.bgFake))
	}
	require.NoError(t, bridgeEng.RegisterAdapter("stripe", h.stripe))
	go func() { _ = bridgeEng.Start(ctx) }()

	return h
}

// installChain installs rbac → identity → orchestration-base → service-domain →
// lease-signing via the real InstallPackage op path (the installer publishes to
// ops.meta; the meta-lane Processor commits each atomic batch).
func (h *harness) installChain() {
	installer := pkgmgr.NewInstaller(h.conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	for _, pkg := range []pkgmgr.Definition{
		rbacdomain.Package,
		identitydomain.Package,
		orchestrationbase.Package,
		servicedomain.Package,
		leasesigning.Package,
	} {
		res, err := installer.Install(h.ctx, pkg)
		require.NoErrorf(h.t, err, "install %s", pkg.Name)
		require.NotNil(h.t, res)
	}
}

// startAdjacencyBootstrapper runs the Refractor adjacency CDC consumer so the
// lens cypher's link hops (applicationFor / providedTo) resolve.
func (h *harness) startAdjacencyBootstrapper(ctx context.Context, adjKV *substrate.KV) {
	boots := consumer.NewBootstrapper(h.conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(15 * time.Second):
		h.t.Fatal("adjacency bootstrapper did not reach Ready within 15s")
	}
}

// startRefractor discovers the installed leaseApplicationComplete lens via the
// live CoreKVSource watch and wires it through projection.InstallActorAggregate
// (the production actor-aggregate path) onto the weaver-targets bucket.
func (h *harness) startRefractor(ctx context.Context, adjKV, coreKV, convKV *substrate.KV) {
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	src := lens.NewCoreKVSource(h.conn, bootstrap.CoreKVBucket, h.logger)
	loaded := make(chan *lens.Rule, 16)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(h.t, src.Start(ctx))

	var convRule *lens.Rule
	deadline := time.Now().Add(25 * time.Second)
	for convRule == nil {
		if time.Now().After(deadline) {
			h.t.Fatal("did not activate the leaseApplicationComplete lens within 25s")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "leaseApplicationComplete" {
				convRule = r
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	require.Equal(h.t, "actorAggregate", convRule.ProjectionKind)
	require.NotNil(h.t, convRule.CompiledRule)
	h.convRID = convRule.ID

	convAdpt, err := adapter.New(convKV, convRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(h.t, err)
	p, err := pipeline.New(convRule.ID, "nats_kv", nil, bootstrap.CoreKVBucket, adjKV, coreKV, convAdpt, nil)
	require.NoError(h.t, err)
	p.UseFullEngine(fullEngine, convRule.CompiledRule)
	require.True(h.t, projection.InstallActorAggregate(p, convAdpt, convRule, projectionRevision, adjKV, coreKV, h.logger),
		"leaseApplicationComplete lens must install through projection.InstallActorAggregate")

	p.RunOn(h.conn, refractorSpec(convRule.ID))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); p.Run(pipelineCtx) }()
	h.t.Cleanup(func() { pipelineCancel(); <-doneCh })
}

// refractorSpec mirrors the production supervised-consumer spec for the
// actor-aggregate pipeline (durable refractor-<ruleID>, DeliverLastPerSubject).
func refractorSpec(ruleID string) substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:          "refractor-" + ruleID,
		Stream:        subjects.CoreKVStream(bootstrap.CoreKVBucket),
		FilterSubject: subjects.CoreKVFilter(bootstrap.CoreKVBucket),
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-" + ruleID,
	}
}

// --- op submission ----------------------------------------------------------

// submitOp publishes an OperationEnvelope to ops.<lane> and waits for the
// Processor reply. It is how the harness drives the applicant-facing ops
// (CreateLeaseApplication, RecordIdentityPII, SignLease).
func (h *harness) submitOp(operationType, class, lane, actor string, payload map[string]any, hint *processor.ContextHint) *processor.OperationReply {
	h.t.Helper()
	payloadBytes, err := json.Marshal(payload)
	require.NoError(h.t, err)
	reqID, err := substrate.NewNanoID()
	require.NoError(h.t, err)

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.Lane(lane),
		OperationType: operationType,
		Class:         class,
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       json.RawMessage(payloadBytes),
		ContextHint:   hint,
	}
	envBytes, err := json.Marshal(env)
	require.NoError(h.t, err)

	inbox := nats.NewInbox()
	sub, err := h.conn.NATS().SubscribeSync(inbox)
	require.NoError(h.t, err)
	defer func() { _ = sub.Unsubscribe() }()

	msg := &nats.Msg{Subject: "ops." + lane, Data: envBytes, Header: nats.Header{replyInboxHeader: []string{inbox}}}
	_, err = h.conn.JetStream().PublishMsg(h.ctx, msg)
	require.NoError(h.t, err)

	replyMsg, err := sub.NextMsgWithContext(h.ctx)
	require.NoError(h.t, err)
	var reply processor.OperationReply
	require.NoError(h.t, json.Unmarshal(replyMsg.Data, &reply))
	return &reply
}

// seedApplicant creates an applicant identity (alive, no PII) and one lease
// application for them (all four gaps open). Returns the leaseapp key + bare id
// and the applicant identity key.
func (h *harness) seedApplicant() (appKey, appID, applicantKey string) {
	h.t.Helper()
	claimSum := sha256.Sum256([]byte("lease-applicant-claim-" + mustNanoID(h.t)))
	idReply := h.submitOp("CreateUnclaimedIdentity", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name":         "Lease Applicant",
		"email":        "applicant@loftspace.example",
		"claimKeyHash": hex.EncodeToString(claimSum[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, idReply.Status, "CreateUnclaimedIdentity: %+v", idReply.Error)
	applicantKey = idReply.PrimaryKey

	appReply := h.submitOp("CreateLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"applicant": applicantKey,
	}, &processor.ContextHint{Reads: []string{applicantKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, appReply.Status, "CreateLeaseApplication: %+v", appReply.Error)
	appKey = appReply.PrimaryKey
	appID = appKey[len("vtx.leaseapp."):]
	return appKey, appID, applicantKey
}

// --- weaver-targets row observation -----------------------------------------

func (h *harness) convKey(appID string) string { return "leaseApplicationComplete." + appID }

// readRow reads the convergence row, returning nil when absent/empty.
func (h *harness) readRow(appID string) map[string]any {
	entry, err := h.convKV.Get(h.ctx, h.convKey(appID))
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return nil
	}
	var row map[string]any
	if json.Unmarshal(entry.Value, &row) != nil {
		return nil
	}
	return row
}

// rowBool reads a Go bool column off the row (false when absent/non-bool).
func rowBool(row map[string]any, col string) bool {
	b, _ := row[col].(bool)
	return b
}

// dumpDiagnostics prints the last-seen row + each engine's Health issues — the
// loud-failure aid on a drain timeout.
func (h *harness) dumpDiagnostics(appID string) {
	h.t.Helper()
	if row := h.readRow(appID); row != nil {
		raw, _ := json.MarshalIndent(row, "", "  ")
		h.t.Logf("last-seen convergence row %s:\n%s", h.convKey(appID), string(raw))
	} else {
		h.t.Logf("convergence row %s is absent", h.convKey(appID))
	}
	keys, err := h.conn.KVListKeys(h.ctx, bootstrap.HealthKVBucket)
	if err != nil {
		return
	}
	for _, k := range keys {
		entry, err := h.conn.KVGet(h.ctx, bootstrap.HealthKVBucket, k)
		if err != nil {
			continue
		}
		var doc struct {
			Issues []map[string]any `json:"issues"`
		}
		if json.Unmarshal(entry.Value, &doc) == nil && len(doc.Issues) > 0 {
			raw, _ := json.Marshal(doc.Issues)
			h.t.Logf("health %s issues: %s", k, string(raw))
		}
	}
	// Weaver marks (which gaps got dispatched), Loom instances, and service vertices.
	if mk, err := h.conn.KVListKeys(h.ctx, bootstrap.WeaverStateBucket); err == nil {
		h.t.Logf("weaver-state keys: %v", mk)
	}
	if lk, err := h.conn.KVListKeys(h.ctx, bootstrap.LoomStateBucket); err == nil {
		h.t.Logf("loom-state keys: %v", lk)
	}
	ck, _ := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
	for _, k := range ck {
		if len(k) > len("vtx.service.") && k[:len("vtx.service.")] == "vtx.service." {
			h.t.Logf("core-kv service key: %s", k)
		}
	}
	// Weaver health (dispatch publish failures surface here).
	if e, err := h.conn.KVGet(h.ctx, bootstrap.HealthKVBucket, "health.weaver.lc-weaver"); err == nil {
		h.t.Logf("weaver health: %s", string(e.Value))
	}
}

// --- aspect / vertex reads (D5 assertions) ----------------------------------

// vertexRootData reads a vertex's root `data` object (nil if absent).
func (h *harness) vertexRootData(key string) (map[string]any, bool) {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, key)
	if err != nil {
		return nil, false
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(entry.Value, &env) != nil {
		return nil, false
	}
	return env.Data, true
}

// aspectData reads an aspect's `data` object (nil if absent).
func (h *harness) aspectData(ownerKey, local string) map[string]any {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, ownerKey+"."+local)
	if err != nil {
		return nil
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(entry.Value, &env) != nil {
		return nil
	}
	return env.Data
}

// serviceHandlesFor returns the vtx.service.<handle> keys providedTo the
// applicant, discriminated by family. It scans Core KV for the providedTo links.
//
// A require.Never/Eventually goroutine can outlive the test's cleanup and poll
// this helper one last time after teardown begins: the harness cleanups run LIFO
// (context cancel, then conn.Close, then nc.Close, then server Shutdown), so a
// late read returns context.Canceled or, in the narrow window after the
// connection closes, a connection-closed error. Treat BOTH as "no keys" (return
// empty) rather than failing — a require.NoError fired from a goroutine after the
// test completed runs t.FailNow() out of band and misattributes the failure to the
// NEXT test in the package (the CI-flaky failure). Any other error is still a real
// fault.
func (h *harness) serviceOutcomes(applicantID string) (handles []string) {
	keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
	if errors.Is(err, context.Canceled) || substrate.IsConnectionError(err) {
		return nil
	}
	require.NoError(h.t, err)
	seen := map[string]bool{}
	for _, k := range keys {
		// providedTo link: lnk.service.<handle>.providedTo.identity.<applicantID>
		_, srcID, name, _, dstID, ok := substrate.ParseLinkKey(k)
		if !ok || name != "providedTo" || dstID != applicantID {
			continue
		}
		svcKey := "vtx.service." + srcID
		if !seen[svcKey] {
			seen[svcKey] = true
			handles = append(handles, svcKey)
		}
	}
	return handles
}

// countOutcomeAspects returns how many vtx.service.<handle>.outcome aspects exist
// for services providedTo the applicant — the at-most-once external-effect witness.
func (h *harness) countOutcomeAspects(applicantID string) int {
	n := 0
	for _, svcKey := range h.serviceOutcomes(applicantID) {
		if h.aspectData(svcKey, "outcome") != nil {
			n++
		}
	}
	return n
}

// awaitDispatchedTask waits until a task vertex scopedTo the given target id
// exists in Core KV (its scopedTo link committed) and returns the task key. It is
// the H3 witness: a committed task proves the dispatched CreateTask actually ran
// THROUGH the real Processor (Weaver's assignTask CreateTask and Loom's userTask
// CreateTask both produce Processor-acceptable envelopes — RF#2), rather than the
// e2e converging solely via the harness's direct ops. Returns "" on timeout.
func (h *harness) awaitDispatchedTask(scopedToID string, deadline time.Duration) string {
	h.t.Helper()
	cut := time.Now().Add(deadline)
	for time.Now().Before(cut) {
		keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
		if err == nil {
			for _, k := range keys {
				// scopedTo link: lnk.task.<taskId>.scopedTo.<type>.<scopedToID>
				t1, taskID, name, _, dstID, ok := substrate.ParseLinkKey(k)
				if !ok || t1 != "task" || name != "scopedTo" || dstID != scopedToID {
					continue
				}
				taskKey := "vtx.task." + taskID
				if data, ok := h.vertexRootData(taskKey); ok && data != nil {
					return taskKey
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return ""
}

// countBgcheckOutcomes returns how many backgroundCheck-family service instances
// providedTo the applicant carry an .outcome aspect. Each eager freshness cycle
// re-dispatches the bgcheck externalTask, minting a NEW service instance + a NEW
// outcome — so this count increments by exactly ONE per cycle (the eager-reopen
// "exactly one new external call per cycle" witness, C2). Payment instances are
// excluded (only bgcheck re-opens on freshness lapse).
func (h *harness) countBgcheckOutcomes(applicantID string) int {
	n := 0
	for _, svcKey := range h.serviceOutcomes(applicantID) {
		fam := h.aspectData(svcKey, "family")
		if fam == nil || fam["value"] != "backgroundCheck" {
			continue
		}
		if h.aspectData(svcKey, "outcome") != nil {
			n++
		}
	}
	return n
}

// totalBgcheckSideEffects sums the Fake bgcheck adapter's side-effects across all
// bgcheck instances providedTo the applicant — the real external-call counter
// (one charge per genuine call). It is keyed by the per-instance bare handle, so
// it captures the new instance each eager cycle mints.
func (h *harness) totalBgcheckSideEffects(applicantID string) int {
	total := 0
	for _, svcKey := range h.serviceOutcomes(applicantID) {
		fam := h.aspectData(svcKey, "family")
		if fam == nil || fam["value"] != "backgroundCheck" {
			continue
		}
		total += h.bgFake.SideEffects(svcKey[len("vtx.service."):])
	}
	return total
}

// scheduleArmed reports whether a pending @at schedule message is armed at the
// given core-schedules subject (the freshness timer Weaver published from
// freshUntil).
func (h *harness) scheduleArmed(subject string) bool {
	stream, err := h.conn.JetStream().Stream(h.ctx, bootstrap.CoreSchedulesStreamName)
	if err != nil {
		return false
	}
	msg, err := stream.GetLastMsgForSubject(h.ctx, subject)
	if err != nil || msg == nil {
		return false
	}
	return msg.Header.Get(substrate.ScheduleHeader) != ""
}

// markExpiredCounter is a persistent observer of MarkExpired ops for one
// application. A fresh, per-cycle non-durable subscription could miss an op
// published while it is not subscribed (fatal across multiple freshness cycles);
// this subscribes ONCE and counts every MarkExpired for the app for the lifetime
// of the harness context, so a firing between two cycle assertions is never lost.
type markExpiredCounter struct {
	sub   *nats.Subscription
	count *int64
}

// startMarkExpiredCounter subscribes to ops.system and counts MarkExpired ops
// targeting appKey from now until ctx ends. The count is read with .seen().
func (h *harness) startMarkExpiredCounter(appKey string) *markExpiredCounter {
	h.t.Helper()
	var n int64
	c := &markExpiredCounter{count: &n}
	sub, err := h.conn.NATS().Subscribe("ops.system", func(msg *nats.Msg) {
		var env struct {
			OperationType string         `json:"operationType"`
			Payload       map[string]any `json:"payload"`
		}
		if json.Unmarshal(msg.Data, &env) != nil {
			return
		}
		if env.OperationType == "MarkExpired" && env.Payload["entityKey"] == appKey {
			atomic.AddInt64(c.count, 1)
		}
	})
	require.NoError(h.t, err)
	c.sub = sub
	h.t.Cleanup(func() { _ = sub.Unsubscribe() })
	return c
}

func (c *markExpiredCounter) seen() int { return int(atomic.LoadInt64(c.count)) }

// freshnessMarker reads the freshnessExpiry marker aspect written on the
// application by MarkExpired (vtx.leaseapp.<appID>.freshnessExpiry). It returns
// the marker's data.expiredAt (the per-fire instant the marker carries) and the
// aspect's KV revision — the two causal witnesses the eager-reopen test uses:
//
//   - expiredAt ADVANCES only when a NEW firing's MarkExpired COMMITS (each
//     cycle's @at fires one window later, so a later fireAt → a later expiredAt
//     on the overwritten marker);
//   - the KV revision bumps on every committed marker write.
//
// A LAZY re-open (an incidental CDC touch re-running the cypher) would NOT bump
// either — it never submits MarkExpired — so an advance here is causal proof that
// THIS cycle's MarkExpired→commit→reproject chain ran, not merely that the
// bgcheck count incremented (which a lazy path could also produce). Returns
// ("", 0) when the marker is absent (no MarkExpired has committed yet).
func (h *harness) freshnessMarker(appKey string) (expiredAt string, revision uint64) {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, appKey+".freshnessExpiry")
	if err != nil {
		return "", 0
	}
	var env struct {
		Data struct {
			ExpiredAt string `json:"expiredAt"`
		} `json:"data"`
	}
	if json.Unmarshal(entry.Value, &env) != nil {
		return "", 0
	}
	return env.Data.ExpiredAt, entry.Revision
}

// bgcheckHandle returns the bare handle of the bgcheck service instance providedTo
// the applicant (the service whose .family aspect value is backgroundCheck).
func (h *harness) bgcheckHandle(applicantID string) string {
	for _, svcKey := range h.serviceOutcomes(applicantID) {
		fam := h.aspectData(svcKey, "family")
		if fam != nil && fam["value"] == "backgroundCheck" {
			return svcKey[len("vtx.service."):]
		}
	}
	return ""
}

// bridgeSkipped reads the bridge's Contract #5 heartbeat (health.bridge.lc-bridge)
// and returns its metrics.skipped counter — how many redelivered external events
// the bridge OBSERVED and short-circuited via the skip-on-redelivery probe
// (deriveReplyRequestID already landed → Ack without re-calling the adapter). It
// is the M4 positive control for the FR58 redelivery: a well-formed redelivery
// that the bridge consumed + deduped INCREMENTS this; an event dropped as garbage
// (unparseable / missing adapter → the event:* Ack path) does NOT — so the FR58
// require.Never cannot pass vacuously on a silently-dropped event. Returns 0 when
// the heartbeat or the metric is absent.
func (h *harness) bridgeSkipped() int64 {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.HealthKVBucket, "health.bridge.lc-bridge")
	if err != nil {
		return 0
	}
	var doc struct {
		Metrics struct {
			Skipped int64 `json:"skipped"`
		} `json:"metrics"`
	}
	if json.Unmarshal(entry.Value, &doc) != nil {
		return 0
	}
	return doc.Metrics.Skipped
}

// republishExternalEvent re-emits an external.<adapter> event in the FULL Event
// envelope shape the instanceOp's outbox produces (a top-level requestId plus a
// payload object the bridge reads). The instanceKey == externalRef by construction,
// so the bridge re-derives the SAME deriveReplyRequestID and the redelivery
// collapses on the Contract #4 tracker (FR58). The replyOp is the lease replyOp.
func (h *harness) republishExternalEvent(adapter, instanceKey string) {
	h.t.Helper()
	payload := map[string]any{
		"instanceKey":    instanceKey,
		"adapter":        adapter,
		"replyOp":        "RecordLeaseServiceOutcome",
		"externalRef":    instanceKey,
		"idempotencyKey": instanceKey,
		"params":         map[string]any{},
	}
	ev := map[string]any{
		"eventId":   mustNanoID(h.t),
		"requestId": mustNanoID(h.t),
		"eventType": "external." + adapter,
		"payload":   payload,
		"timestamp": substrate.FormatTimestamp(time.Now()),
	}
	data, err := json.Marshal(ev)
	require.NoError(h.t, err)
	_, err = h.conn.JetStream().Publish(h.ctx, "events.external."+adapter, data)
	require.NoError(h.t, err)
}

func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}
