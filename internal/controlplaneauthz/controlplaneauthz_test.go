//go:build controlplaneauthz

// Package controlplaneauthz_test is the FR30 Fire 1b e2e + Gate-3 control-
// plane bypass proof (control-plane-capability-authz-design.md §5). It boots
// the REAL Processor under LATTICE_AUTH_MODE=capability, the REAL Refractor
// projecting both the core `capability` anchor lens and rbac-domain's
// `capabilityRoles` lens into capability-kv, installs rbac-domain +
// control-authz, seeds an "operator" identity granted `control-operator`
// (via the real AssignRole op) and an "intruder" identity with no grant, then
// drives a real internal/weaver/control.Service wired with
// internal/controlauth.CapabilityKVChecker over a real NATS request/reply
// round-trip:
//
//   - the operator's `disable` request succeeds (real grant, real lens)
//   - the intruder's `disable` request is denied (no grant)
//   - an anonymous (no Lattice-Actor header) `disable` request is denied
//
// This is the Gate-3 "control-plane bypass vector": an un-granted actor and
// an anonymous request attempting a mutation must be DEFENDED. Loom and
// Refractor share the identical CapabilityKVChecker code path (only the
// component name + op table differ, both already unit-tested in
// internal/controlauth) — this harness exercises one component end to end as
// the shared-mechanism proof, mirroring the systemactorcapability precedent
// of keeping heavy multi-package e2e out of the untagged `go test ./...`.
//
// Gated behind the `controlplaneauthz` build tag — runs via
// `go test -tags controlplaneauthz ./internal/controlplaneauthz/...`.
package controlplaneauthz_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/controlauth"
	"github.com/asolgan/lattice/internal/jsstore"
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
	"github.com/asolgan/lattice/internal/testutil"
	"github.com/asolgan/lattice/internal/weaver"
	weavercontrol "github.com/asolgan/lattice/internal/weaver/control"
	controlauthz "github.com/asolgan/lattice/packages/control-authz"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
)

const replyInboxHeader = "Lattice-Reply-Inbox"

type harness struct {
	t                      *testing.T
	ctx                    context.Context
	conn                   *substrate.Conn
	logger                 *slog.Logger
	coreKV                 *substrate.KV
	capKV                  *substrate.KV
	wired                  map[string]chan struct{}
	controlOperatorRoleKey string
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

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	h := &harness{t: t, ctx: ctx, conn: conn, logger: logger, coreKV: coreKV, capKV: capKV}

	systemActorKeys, err := bootstrap.SystemActorKeys(ctx, conn)
	require.NoError(t, err)
	require.NotEmpty(t, systemActorKeys)

	cp, _, err := processor.MakePipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, bootstrap.CapabilityKVBucket,
		processor.AuthModeCapability, false, logger, "cpa-processor",
		processor.AuthWiring{RbacRolesActive: true, SystemActorKeys: systemActorKeys}, nil)
	require.NoError(t, err)
	procCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "cpa-processor",
		FilterSubjects: []string{"ops.default", "ops.urgent", "ops.system", "ops.meta"},
		AckWait:        10 * time.Second,
	}, logger)
	require.NoError(t, err)
	procCC, err := procCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(ctx, m) })
	require.NoError(t, err)
	t.Cleanup(procCC.Stop)
	go func() { _ = outbox.New(conn, bootstrap.CoreKVBucket, logger).Run(ctx) }()

	h.startAdjacency(ctx, adjKV)
	h.startCapabilitySource(ctx, adjKV, coreKV)
	h.awaitLensWired("capability", 20*time.Second)
	h.awaitCapDoc("cap.identity."+bootstrap.BootstrapIdentityID, 20*time.Second)

	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	for _, pkg := range []pkgmgr.Definition{rbacdomain.Package, identitydomain.Package, controlauthz.Package} {
		_, err := installer.Install(ctx, pkg)
		require.NoErrorf(t, err, "install %s", pkg.Name)
	}
	h.awaitLensWired("capabilityRoles", 20*time.Second)

	controlOperatorRoleKey := "vtx.role." + installer.RoleIDs["control-operator"]
	require.NotEqual(t, "vtx.role.", controlOperatorRoleKey, "control-authz install must mint the control-operator role id")
	h.controlOperatorRoleKey = controlOperatorRoleKey

	return h
}

func (h *harness) startAdjacency(ctx context.Context, adjKV *substrate.KV) {
	boots := consumer.NewBootstrapper(h.conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(15 * time.Second):
		h.t.Fatal("adjacency bootstrapper did not reach Ready within 15s")
	}
}

func (h *harness) startCapabilitySource(ctx context.Context, adjKV, coreKV *substrate.KV) {
	fullEngine := full.New()
	projRev := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	h.wired = map[string]chan struct{}{
		"capability":      make(chan struct{}),
		"capabilityRoles": make(chan struct{}),
	}

	src := lens.NewCoreKVSource(h.conn, bootstrap.CoreKVBucket, "test", h.logger)
	src.SetLoadCallback(func(rule *lens.Rule) {
		ready, want := h.wired[rule.CanonicalName]
		if !want {
			return
		}
		require.Equal(h.t, "actorAggregate", rule.ProjectionKind, rule.CanonicalName)
		require.NotNil(h.t, rule.CompiledRule, rule.CanonicalName)

		targetKV, err := h.conn.OpenKV(ctx, rule.Into.Bucket)
		require.NoError(h.t, err, rule.CanonicalName)
		adpt, err := adapter.New(targetKV, rule.Into.Key, adapter.DeleteModeHard)
		require.NoError(h.t, err, rule.CanonicalName)
		p, err := pipeline.New(rule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
		require.NoError(h.t, err, rule.CanonicalName)
		p.UseFullEngine(fullEngine, rule.CompiledRule)
		require.True(h.t, projection.InstallActorAggregate(p, adpt, rule, projRev, adjKV, coreKV, h.logger),
			rule.CanonicalName+" lens must install through projection.InstallActorAggregate")
		p.RunOn(h.conn, substrate.ConsumerSpec{
			Name:          "refractor-" + rule.ID,
			Stream:        subjects.CoreKVStream(bootstrap.CoreKVBucket),
			FilterSubject: subjects.CoreKVFilter(bootstrap.CoreKVBucket),
			DeliverPolicy: substrate.DeliverLastPerSubject,
			DeliverGroup:  "refractor-" + rule.ID,
		})
		pctx, pcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); p.Run(pctx) }()
		h.t.Cleanup(func() { pcancel(); <-done })
		close(ready)
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(h.t, src.Start(ctx))
}

func (h *harness) awaitLensWired(name string, deadline time.Duration) {
	h.t.Helper()
	select {
	case <-h.wired[name]:
	case <-time.After(deadline):
		h.t.Fatalf("lens %q did not activate within %s", name, deadline)
	}
}

func (h *harness) submitOp(operationType, lane, actor string, payload map[string]any, reads []string) *processor.OperationReply {
	h.t.Helper()
	payloadBytes, err := json.Marshal(payload)
	require.NoError(h.t, err)
	reqID, err := substrate.NewNanoID()
	require.NoError(h.t, err)
	env := &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.Lane(lane), OperationType: operationType,
		Actor: actor, SubmittedAt: time.Now().UTC().Format(time.RFC3339),
		Payload: json.RawMessage(payloadBytes), ContextHint: &processor.ContextHint{Reads: reads},
	}
	envBytes, err := json.Marshal(env)
	require.NoError(h.t, err)

	inbox := nats.NewInbox()
	sub, err := h.conn.NATS().SubscribeSync(inbox)
	require.NoError(h.t, err)
	defer func() { _ = sub.Unsubscribe() }()

	_, err = h.conn.JetStream().PublishMsg(h.ctx, &nats.Msg{
		Subject: "ops." + lane, Data: envBytes, Header: nats.Header{replyInboxHeader: []string{inbox}},
	})
	require.NoError(h.t, err)
	replyMsg, err := sub.NextMsgWithContext(h.ctx)
	require.NoError(h.t, err)
	var reply processor.OperationReply
	require.NoError(h.t, json.Unmarshal(replyMsg.Data, &reply))
	return &reply
}

// submitOpAccepted retries until Accepted or deadline — the role-derived
// grant projects asynchronously off CDC (mirrors systemactorcapability's
// identical retry rationale).
func (h *harness) submitOpAccepted(operationType, lane, actor string, payload map[string]any, reads []string, deadline time.Duration) *processor.OperationReply {
	h.t.Helper()
	cut := time.Now().Add(deadline)
	var last *processor.OperationReply
	for time.Now().Before(cut) {
		last = h.submitOp(operationType, lane, actor, payload, reads)
		if last.Status == processor.ReplyStatusAccepted {
			return last
		}
		if last.Error == nil || last.Error.Code != processor.ErrCodeAuthDenied {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

func (h *harness) awaitCapDoc(key string, deadline time.Duration) map[string]any {
	h.t.Helper()
	cut := time.Now().Add(deadline)
	for time.Now().Before(cut) {
		entry, err := h.capKV.Get(h.ctx, key)
		if err == nil && entry != nil && len(entry.Value) > 0 {
			var doc map[string]any
			if json.Unmarshal(entry.Value, &doc) == nil {
				return doc
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	keys, _ := h.conn.KVListKeys(h.ctx, bootstrap.CapabilityKVBucket)
	h.t.Fatalf("capability doc %s did not project within %s; capability-kv keys=%v", key, deadline, keys)
	return nil
}

// awaitCtrlGrant polls until actor's cap.roles doc carries a ctrl.weaver.*
// grant — the control-authz install's grant projects asynchronously, same as
// every other role-derived permission.
func (h *harness) awaitCtrlGrant(actorKey string, deadline time.Duration) {
	h.t.Helper()
	sub := strings.TrimPrefix(actorKey, "vtx.")
	key := "cap.roles." + sub
	cut := time.Now().Add(deadline)
	for time.Now().Before(cut) {
		entry, err := h.capKV.Get(h.ctx, key)
		if err == nil && entry != nil {
			var doc struct {
				PlatformPermissions []struct {
					OperationType string `json:"operationType"`
				} `json:"platformPermissions"`
			}
			if json.Unmarshal(entry.Value, &doc) == nil {
				for _, p := range doc.PlatformPermissions {
					if strings.HasPrefix(p.OperationType, "ctrl.weaver.") {
						return
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("ctrl.weaver.* grant did not project to %s within %s", key, deadline)
}

var _ = weaver.TargetSummary{} // keep the weaver import honest against drift

// fakeEngine is the minimal weaver control engine stub — this harness proves
// AUTHORIZATION, not weaver's dispatch mechanics (already e2e-proven
// elsewhere), so Disable just records the call.
type fakeEngine struct {
	disabled chan string
}

func (f *fakeEngine) ListTargets(context.Context) ([]weaver.TargetSummary, error) { return nil, nil }
func (f *fakeEngine) Disable(_ context.Context, targetID string) error {
	f.disabled <- targetID
	return nil
}
func (f *fakeEngine) Enable(context.Context, string) error { return nil }
func (f *fakeEngine) Revoke(context.Context, string) error { return nil }

// TestControlPlaneAuthz_OperatorAllowedIntruderDeniedAnonymousDenied is the
// FR30 Fire 1b Gate-3 proof: a real weaver control.Service wired with the
// real CapabilityKVChecker, exercised over a real NATS round-trip, against
// grants projected by the real capabilityRoles lens.
func TestControlPlaneAuthz_OperatorAllowedIntruderDeniedAnonymousDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping control-plane authz e2e in -short mode")
	}
	h := newHarness(t)

	opReply := h.submitOpAccepted("CreateUnclaimedIdentity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name": "Control Operator", "email": "control-operator@lattice.example", "claimKeyHash": strings.Repeat("a", 64),
	}, nil, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, opReply.Status, "create operator identity: %+v", opReply.Error)
	operatorActor := opReply.PrimaryKey

	inReply := h.submitOpAccepted("CreateUnclaimedIdentity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name": "Intruder", "email": "intruder@lattice.example", "claimKeyHash": strings.Repeat("b", 64),
	}, nil, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, inReply.Status, "create intruder identity: %+v", inReply.Error)
	intruderActor := inReply.PrimaryKey

	assignReply := h.submitOpAccepted("AssignRole", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"actorKey": operatorActor, "roleKey": h.controlOperatorRoleKey,
	}, []string{operatorActor, h.controlOperatorRoleKey}, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, assignReply.Status, "AssignRole control-operator: %+v", assignReply.Error)

	h.awaitCtrlGrant(operatorActor, 20*time.Second)

	systemActorKeys, err := bootstrap.SystemActorKeys(h.ctx, h.conn)
	require.NoError(t, err)
	checker := controlauth.NewCapabilityKVChecker("weaver", controlauth.WeaverOps, h.conn, bootstrap.CapabilityKVBucket,
		systemActorKeys, true, controlauth.AuthModeCapability, nil, h.logger)

	engine := &fakeEngine{disabled: make(chan string, 4)}
	svc := weavercontrol.NewService(engine, checker, h.logger)
	require.NoError(t, svc.StartNATSListener(h.ctx, h.conn.NATS()))

	subject := weavercontrol.TargetSubject("some-target", "disable")

	// --- operator: allowed ---
	reply, err := h.conn.NATS().RequestMsg(controlauth.NewActorRequestMsg(subject, operatorActor), 5*time.Second)
	require.NoError(t, err)
	var resp weavercontrol.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.Emptyf(t, resp.Error, "operator disable must succeed, got error: %s", resp.Error)
	select {
	case got := <-engine.disabled:
		require.Equal(t, "some-target", got)
	case <-time.After(2 * time.Second):
		t.Fatal("operator's disable did not reach the engine")
	}

	// --- intruder: denied (no control grant) ---
	reply, err = h.conn.NATS().RequestMsg(controlauth.NewActorRequestMsg(subject, intruderActor), 5*time.Second)
	require.NoError(t, err)
	resp = weavercontrol.ControlResponse{}
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.NotEmpty(t, resp.Error, "intruder's disable must be denied")

	// --- anonymous (no Lattice-Actor header): denied ---
	reply, err = h.conn.NATS().RequestMsg(controlauth.NewActorRequestMsg(subject, ""), 5*time.Second)
	require.NoError(t, err)
	resp = weavercontrol.ControlResponse{}
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.NotEmpty(t, resp.Error, "an anonymous request must be denied under capability mode")

	select {
	case got := <-engine.disabled:
		t.Fatalf("a denied request must never reach the engine, but got disable(%q)", got)
	default:
	}
}
