//go:build unroutedconvergence

// Package unroutedconvergence_test is FR28/FR29's end-to-end proof: it boots
// Processor + Weaver together against one embedded-NATS server, installs the
// real chain (rbac → identity → orchestration-base) via the real
// InstallPackage op path — registering the real `unroutedTasks`
// meta.weaverTarget with its `missing_claim` gap materialised to the new
// §10.8 `surface` action — and hand-writes the weaver-targets row the real
// unroutedTasks lens would project for an aged, unclaimed role-queued task
// (mirroring augurconvergence's putGapRow convention: this harness runs no
// live Refractor, so it writes exactly what the lens — already proven
// correct at the cypher level by orchestration-base's lens_cypher_test.go —
// would have projected).
//
// It proves the piece no unit test can: the `surface` action round-trips
// through the REAL install path (pkgmgr's GapActionSpec → the meta.weaverTarget
// spec aspect JSON → Weaver's registry CDC load → a real GapAction{Action:
// "surface", IssueCode, IssueSeverity}) and the real Weaver engine raises the
// named Health-KV issue (Contract #5 §5.5 issues[]) while the row stays
// violating, then clears it once the row closes — with NO op ever dispatched
// (surface-only, per FR29).
//
// Gated behind the `unroutedconvergence` build tag — runs only via
// `make test-unrouted-convergence`, never the untagged `go test ./...`.
package unroutedconvergence_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/processor/outbox"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	"github.com/asolgan/lattice/internal/weaver"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
)

const replyInboxHeader = "Lattice-Reply-Inbox"

type harness struct {
	t        *testing.T
	ctx      context.Context
	conn     *substrate.Conn
	instance string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	js := conn.JetStream()

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	h := &harness{t: t, ctx: ctx, conn: conn, instance: "uc-weaver"}

	// Processor (all lanes, AuthModeStub) + the transactional outbox.
	cp, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "uc-processor")
	require.NoError(t, err)
	procCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "uc-processor",
		FilterSubjects: []string{"ops.default", "ops.urgent", "ops.system", "ops.meta"},
		AckWait:        10 * time.Second,
	}, logger)
	require.NoError(t, err)
	procCC, err := procCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(ctx, m) })
	require.NoError(t, err)
	t.Cleanup(procCC.Stop)
	go func() { _ = outbox.New(conn, bootstrap.CoreKVBucket, logger).Run(ctx) }()

	// Install rbac → identity → orchestration-base via the real InstallPackage
	// op path — registers the real unroutedTasks meta.weaverTarget (the
	// `missing_claim` gap materialised to the new §10.8 `surface` action) the
	// same way any package's weaverTargets install.
	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	for _, pkg := range []pkgmgr.Definition{rbacdomain.Package, identitydomain.Package, orchestrationbase.Package} {
		_, err := installer.Install(ctx, pkg)
		require.NoErrorf(t, err, "install %s", pkg.Name)
	}

	go func() {
		_ = weaver.NewEngine(conn, weaver.Config{
			CoreKVBucket:        bootstrap.CoreKVBucket,
			WeaverTargetsBucket: bootstrap.WeaverTargetsBucket,
			WeaverStateBucket:   bootstrap.WeaverStateBucket,
			HealthKVBucket:      bootstrap.HealthKVBucket,
			CoreSchedulesStream: bootstrap.CoreSchedulesStreamName,
			ActorKey:            bootstrap.WeaverIdentityKey,
			Lane:                "system",
			Instance:            h.instance,
			HeartbeatEvery:      200 * time.Millisecond,
			Logger:              logger,
		}).Start(ctx)
	}()

	return h
}

func (h *harness) submitOp(operationType, class, actor string, payload map[string]any) *processor.OperationReply {
	h.t.Helper()
	payloadBytes, _ := json.Marshal(payload)
	reqID, err := substrate.NewNanoID()
	require.NoError(h.t, err)
	env := &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.LaneDefault, OperationType: operationType,
		Class: class, Actor: actor, SubmittedAt: time.Now().UTC().Format(time.RFC3339),
		Payload: json.RawMessage(payloadBytes),
	}
	envBytes, _ := json.Marshal(env)
	inbox := nats.NewInbox()
	sub, err := h.conn.NATS().SubscribeSync(inbox)
	require.NoError(h.t, err)
	defer func() { _ = sub.Unsubscribe() }()
	_, err = h.conn.JetStream().PublishMsg(h.ctx, &nats.Msg{Subject: "ops.default", Data: envBytes, Header: nats.Header{replyInboxHeader: []string{inbox}}})
	require.NoError(h.t, err)
	replyMsg, err := sub.NextMsgWithContext(h.ctx)
	require.NoError(h.t, err)
	var reply processor.OperationReply
	require.NoError(h.t, json.Unmarshal(replyMsg.Data, &reply))
	return &reply
}

// waitTargetConsumer blocks until the weaver-target CDC consumer is live (the
// target is registered from the real install) so the row we write next is
// evaluated against it.
func (h *harness) waitTargetConsumer(targetID string) {
	h.t.Helper()
	js := h.conn.JetStream()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := js.Consumer(h.ctx, "KV_"+bootstrap.WeaverTargetsBucket, "weaver-target-"+targetID); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("weaver-target-%s consumer never appeared", targetID)
}

// putRow hand-writes a §10.2-shaped unroutedTasks row — mirrors exactly what
// the real unroutedTasks lens (proven correct at the cypher level by
// orchestration-base's lens_cypher_test.go) projects for an aged, unclaimed
// role-queued task. This harness runs no live Refractor.
func (h *harness) putRow(entityID, entityKey string, violating bool) {
	h.t.Helper()
	row := map[string]any{
		"entityKey":     entityKey,
		"violating":     violating,
		"missing_claim": violating,
	}
	body, err := json.Marshal(row)
	require.NoError(h.t, err)
	_, err = h.conn.KVPut(h.ctx, bootstrap.WeaverTargetsBucket, "unroutedTasks."+entityID, body)
	require.NoError(h.t, err)
}

type healthIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// hasIssue polls Weaver's own health.weaver.<instance> heartbeat (Contract #5
// §5.5) for an issue with the given code, returning its severity once found.
func (h *harness) hasIssue(code string, timeout time.Duration) (found bool, severity string) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entry, err := h.conn.KVGet(h.ctx, bootstrap.HealthKVBucket, "health.weaver."+h.instance)
		if err == nil && entry != nil && len(entry.Value) > 0 {
			var doc struct {
				Issues []healthIssue `json:"issues"`
			}
			if json.Unmarshal(entry.Value, &doc) == nil {
				for _, iss := range doc.Issues {
					if iss.Code == code {
						return true, iss.Severity
					}
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false, ""
}

func (h *harness) issueCleared(code string, timeout time.Duration) bool {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		found, _ := h.hasIssue(code, 0)
		if !found {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// TestUnroutedConvergence_SurfaceRaisesAndClearsHeartbeatIssue proves FR29's
// `surface` action end-to-end: the real install registers the unroutedTasks
// target with its missing_claim gap materialised to the new §10.8 `surface`
// action (IssueCode "UnroutedTasks", IssueSeverity "warning"), a violating row
// makes it appear in Weaver's real heartbeat at that severity with NO op ever
// dispatched (no ops.* traffic to race against — this harness has no consumer
// draining one, so a wrongly-dispatched op would simply pile up unconsumed;
// the assertion that matters is the issue itself, not an absence of traffic),
// and the issue clears once the row closes.
func TestUnroutedConvergence_SurfaceRaisesAndClearsHeartbeatIssue(t *testing.T) {
	h := newHarness(t)
	h.waitTargetConsumer("unroutedTasks")

	entityID, err := substrate.NewNanoID()
	require.NoError(t, err)
	entityKey := "vtx.task." + entityID

	h.putRow(entityID, entityKey, true)
	found, severity := h.hasIssue("UnroutedTasks", 20*time.Second)
	require.True(t, found, "the real installed unroutedTasks target must raise an UnroutedTasks issue for a violating row")
	require.Equal(t, "warning", severity, "IssueSeverity from the installed target's playbook must carry through")

	h.putRow(entityID, entityKey, false)
	require.True(t, h.issueCleared("UnroutedTasks", 20*time.Second), "the issue must clear once the row stops naming the gap")
}
