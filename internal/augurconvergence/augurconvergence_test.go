//go:build augurconvergence

// Package augurconvergence_test is the Augur (Weaver's L3 reasoning tier)
// end-to-end harness: it boots Processor + outbox + Weaver + the LIVE bridge
// (with the deterministic FakeAugur reasoning adapter) together against one
// embedded-NATS server, installs the real chain (rbac → identity →
// orchestration-base → augur), registers an augur-enabled convergence target
// with an UNPLANNABLE gap, and observes the full Option-F escalation loop
// converge: Weaver detects the gap-without-playbook, escalates it to the Augur
// tier as a directOp(CreateAugurReasoningClaim), the op mints the claim vertex +
// emits external.augur, the bridge's FakeAugur reasons (no real model call), and
// RecordProposal records a human-reviewable proposal vertex with a deterministic
// §5 verdict.
//
// It proves the two ends of the design's safety contract on the live stack:
//
//   - HAPPY PATH — a benign, in-scope proposal lands `pending` (dispatchable,
//     awaiting a human), and the reasoning adapter is billed at most once per
//     escalation episode (the cost-control invariant).
//   - ADVERSARIAL — a crafted scope-escaping proposal (a directOp targeting a
//     DIFFERENT entity than the escalated candidate) is caught by the §5
//     record-time validator and stored `invalid` (auditable, NEVER dispatchable).
//     The model gains no new authority; the verdict comes from the TRUSTED claim
//     context, never the model's reply. This is the Gate-3-style "DEFENDED"
//     assertion for the AI surface.
//
// Processor runs in AuthModeStub: the convergence proof is the escalation
// mechanics + the deterministic validator boundary, not capability auth (the auth
// boundary is Gate 2/3). The augur-proposals review LENS field-shape is pinned by
// packages/augur unit tests + exercised on the install path; this harness asserts
// the verdict on the Core-KV proposal vertex the lens projects.
//
// Gated behind the `augurconvergence` build tag — runs only via
// `make test-augur-convergence`, never the untagged `go test ./...`, keeping the
// all-engines e2e to its single dedicated gate.
package augurconvergence_test

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
	"github.com/asolgan/lattice/internal/bridge"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/processor/outbox"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/weaver"
	augurpkg "github.com/asolgan/lattice/packages/augur"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
)

const replyInboxHeader = "Lattice-Reply-Inbox"

type harness struct {
	t      *testing.T
	ctx    context.Context
	conn   *substrate.Conn
	logger *slog.Logger
	coreKV *substrate.KV
	augur  *bridge.FakeAugur
}

// newHarness boots the full Augur convergence stack. The caller may install a
// FakeAugur proposal override BEFORE the first escalation by passing prepare —
// it runs after the adapter is constructed but before the bridge starts, so the
// override is in place before any reasoning call.
func newHarness(t *testing.T, prepare func(*bridge.FakeAugur)) *harness {
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

	// Real bootstrap substrate (buckets + streams + primordials).
	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err = bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)

	h := &harness{t: t, ctx: ctx, conn: conn, logger: logger, coreKV: coreKV}

	// Processor (all lanes, AuthModeStub) + the transactional outbox publisher
	// (relays the augur op's external.augur event to the bridge).
	cp, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "ac-processor")
	require.NoError(t, err)
	procCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "ac-processor",
		FilterSubjects: []string{"ops.default", "ops.urgent", "ops.system", "ops.meta"},
		AckWait:        10 * time.Second,
	}, logger)
	require.NoError(t, err)
	procCC, err := procCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(ctx, m) })
	require.NoError(t, err)
	t.Cleanup(procCC.Stop)
	go func() { _ = outbox.New(conn, bootstrap.CoreKVBucket, logger).Run(ctx) }()

	// Install rbac → identity → orchestration-base → augur via the real
	// InstallPackage op path (registers the CreateAugurReasoningClaim +
	// RecordProposal op-meta the Processor's operationType→class index resolves,
	// and the augur capability vertices).
	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	for _, pkg := range []pkgmgr.Definition{
		rbacdomain.Package, identitydomain.Package, orchestrationbase.Package, augurpkg.Package,
	} {
		_, err := installer.Install(ctx, pkg)
		require.NoErrorf(t, err, "install %s", pkg.Name)
	}

	// Weaver: lane-1 gap dispatch (drives the directOp(CreateAugurReasoningClaim)
	// escalation for the unplannable gap).
	go func() {
		_ = weaver.NewEngine(conn, weaver.Config{
			CoreKVBucket:        bootstrap.CoreKVBucket,
			WeaverTargetsBucket: bootstrap.WeaverTargetsBucket,
			WeaverStateBucket:   bootstrap.WeaverStateBucket,
			HealthKVBucket:      bootstrap.HealthKVBucket,
			CoreSchedulesStream: bootstrap.CoreSchedulesStreamName,
			ActorKey:            bootstrap.WeaverIdentityKey,
			Lane:                "system",
			Instance:            "ac-weaver",
			HeartbeatEvery:      200 * time.Millisecond,
			Logger:              logger,
		}).Start(ctx)
	}()

	// The LIVE bridge with the deterministic FakeAugur reasoning adapter — no
	// real model call, no network, no spend. A short heartbeat/redelivery keeps
	// the loop brisk in the test window.
	h.augur = bridge.NewFakeAugur()
	if prepare != nil {
		prepare(h.augur)
	}
	bridgeEng := bridge.NewEngine(conn, bridge.Config{
		CoreKVBucket:    bootstrap.CoreKVBucket,
		EventsStream:    bootstrap.CoreEventsStreamName,
		HealthKVBucket:  bootstrap.HealthKVBucket,
		ActorKey:        bootstrap.BridgeIdentityKey,
		Lane:            "system",
		Instance:        "ac-bridge",
		HeartbeatEvery:  150 * time.Millisecond,
		RedeliveryDelay: 300 * time.Millisecond,
	})
	require.NoError(t, bridgeEng.RegisterAdapter("augur", h.augur))
	go func() { _ = bridgeEng.Start(ctx) }()

	return h
}

func (h *harness) submitOp(operationType, actor string, payload map[string]any) *processor.OperationReply {
	h.t.Helper()
	payloadBytes, _ := json.Marshal(payload)
	reqID, err := substrate.NewNanoID()
	require.NoError(h.t, err)
	env := &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.LaneDefault, OperationType: operationType,
		Class: "identity", Actor: actor, SubmittedAt: time.Now().UTC().Format(time.RFC3339),
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

// seedEntity creates an identity to stand in as the escalated candidate (the
// gap's entity). Returns its vertex key.
func (h *harness) seedEntity() string {
	h.t.Helper()
	r := h.submitOp("CreateUnclaimedIdentity", bootstrap.BootstrapIdentityKey, map[string]any{
		"name": "Candidate", "email": mustNanoID(h.t) + "@augur.example",
		"claimKeyHash": "0000000000000000000000000000000000000000000000000000000000000000",
	})
	require.Equalf(h.t, processor.ReplyStatusAccepted, r.Status, "CreateUnclaimedIdentity: %+v", r.Error)
	return r.PrimaryKey
}

// installAugurTarget registers a meta.weaverTarget carrying an `augur` block that
// escalates unplannable gaps. The target's gaps map deliberately has NO entry for
// the gap column the row will raise, so Weaver routes it to the Augur tier rather
// than a playbook. Written the way the Processor write path lands a meta vertex
// (vertex envelope + spec aspect envelope) so the registry CDC source loads it as
// in production. Returns the target id.
func (h *harness) installAugurTarget(targetID string) {
	h.t.Helper()
	vtxKey := "vtx.meta." + mustNanoID(h.t)
	vtxBody, _ := json.Marshal(map[string]any{"class": "meta.weaverTarget", "data": map[string]any{}})
	_, err := h.conn.KVPut(h.ctx, bootstrap.CoreKVBucket, vtxKey, vtxBody)
	require.NoError(h.t, err)
	spec := map[string]any{
		"targetId": targetID,
		"lensRef":  "augurFixtureLens",
		"gaps":     map[string]any{}, // no playbook for missing_approval → unplannable
		"augur":    map[string]any{"escalate": []any{"unplannable"}},
	}
	specEnvelope, _ := json.Marshal(map[string]any{"class": "weaverTargetSpec", "data": spec})
	_, err = h.conn.KVPut(h.ctx, bootstrap.CoreKVBucket, vtxKey+".spec", specEnvelope)
	require.NoError(h.t, err)
}

// waitTargetConsumer blocks until the weaver-target CDC consumer is live (the
// target is registered) so the row we write next is evaluated against it.
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

// putGapRow writes a §10.2-shaped row with a true missing_* column that has no
// playbook entry — the unplannable gap.
func (h *harness) putGapRow(targetID, entityID, entityKey, gapColumn string) {
	h.t.Helper()
	row := map[string]any{
		"entityKey":   entityKey,
		"violating":   true,
		gapColumn:     true,
		"projectedAt": substrate.FormatTimestamp(time.Now()),
	}
	body, _ := json.Marshal(row)
	_, err := h.conn.KVPut(h.ctx, bootstrap.WeaverTargetsBucket, targetID+"."+entityID, body)
	require.NoError(h.t, err)
}

// proposal is the verdict surface this harness asserts on: the augurproposal
// vertex's review + proposed aspects, plus its bare handle (the reasoning
// episode's idempotencyKey, for the cost-control assertion).
type proposal struct {
	handle    string
	reviewKey string
	state     string
	reason    string
	action    string
}

// awaitProposal polls Core KV for the single augurproposal vertex's review aspect
// to reach a terminal verdict (pending or invalid).
func (h *harness) awaitProposal(timeout time.Duration) *proposal {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		keys, err := h.conn.KVListKeysPrefix(h.ctx, bootstrap.CoreKVBucket, "vtx.augurproposal.")
		if err == nil {
			for _, k := range keys {
				if !strings.HasSuffix(k, ".review") {
					continue
				}
				review := readAspect(h, k)
				state, _ := review["state"].(string)
				if state != "pending" && state != "invalid" {
					continue // claim minted, RecordProposal not landed yet
				}
				p := &proposal{
					handle:    strings.TrimSuffix(strings.TrimPrefix(k, "vtx.augurproposal."), ".review"),
					reviewKey: k,
					state:     state,
				}
				p.reason, _ = review["invalidReason"].(string)
				if proposed := readAspect(h, strings.TrimSuffix(k, ".review")+".proposed"); proposed != nil {
					p.action, _ = proposed["action"].(string)
				}
				return p
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}

// readAspect reads an aspect envelope ({class, data}) and returns its data map
// (nil if absent/unset).
func readAspect(h *harness, key string) map[string]any {
	entry, err := h.coreKV.Get(h.ctx, key)
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return nil
	}
	var env map[string]any
	if json.Unmarshal(entry.Value, &env) != nil {
		return nil
	}
	if data, ok := env["data"].(map[string]any); ok {
		return data
	}
	return nil
}

func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

// TestAugurConvergence_HappyPath drives an unplannable gap through the full
// escalation loop and proves a benign, in-scope proposal lands `pending`
// (dispatchable, awaiting a human), billed at most once.
func TestAugurConvergence_HappyPath(t *testing.T) {
	h := newHarness(t, nil) // default FakeAugur → benign in-scope assignTask

	entityKey := h.seedEntity()
	entityID := strings.TrimPrefix(entityKey, "vtx.identity.")
	const targetID = "augurHappyTarget"
	h.installAugurTarget(targetID)
	h.waitTargetConsumer(targetID)
	h.putGapRow(targetID, entityID, entityKey, "missing_approval")

	p := h.awaitProposal(45 * time.Second)
	require.NotNil(t, p, "no augurproposal reached a terminal verdict — the escalation loop did not converge")
	require.Equalf(t, "pending", p.state, "benign in-scope proposal must be pending (dispatchable); reason=%q", p.reason)
	require.Equal(t, "assignTask", p.action, "the benign proposal is an assignTask scoped to the escalated candidate")

	// Cost control: the reasoning adapter is billed at most once per episode.
	require.Equalf(t, 1, h.augur.SideEffects(p.handle),
		"FakeAugur must perform exactly one reasoning call for handle %q", p.handle)
}

// TestAugurConvergence_MaliciousProposalInvalid is the DEFENDED assertion: a
// crafted scope-escaping proposal (a directOp targeting a DIFFERENT entity than
// the escalated candidate) is caught by the §5 record-time validator and stored
// `invalid` — auditable, never dispatchable. The verdict comes from the TRUSTED
// claim context, never the model's reply.
func TestAugurConvergence_MaliciousProposalInvalid(t *testing.T) {
	h := newHarness(t, func(fa *bridge.FakeAugur) {
		fa.SetProposal(bridge.AugurProposal{
			Action:     "directOp",
			Params:     map[string]any{"scopedTo": "vtx.identity.someOtherCandidateXY"},
			Rationale:  "crafted scope escape: act on a different entity than the escalated candidate",
			Confidence: 0.95,
			Model:      "claude-opus-4-8",
		})
	})

	entityKey := h.seedEntity()
	entityID := strings.TrimPrefix(entityKey, "vtx.identity.")
	const targetID = "augurAdversarialTarget"
	h.installAugurTarget(targetID)
	h.waitTargetConsumer(targetID)
	h.putGapRow(targetID, entityID, entityKey, "missing_approval")

	p := h.awaitProposal(45 * time.Second)
	require.NotNil(t, p, "no augurproposal reached a terminal verdict — the escalation loop did not converge")
	require.Equalf(t, "invalid", p.state, "a scope-escaping proposal must be stored invalid; reason=%q", p.reason)
	require.Contains(t, p.reason, "scope escape",
		"the invalid verdict must name the scope-escape class")

	// The proposal IS stored (auditability) even though it is invalid: its review
	// aspect exists and the claim vertex is live.
	require.NotEmpty(t, p.handle)
	claim := readAspect(h, "vtx.augurproposal."+p.handle+".gap")
	require.NotNil(t, claim, "the claim vertex's trusted .gap context must be retained (audit trail)")
	require.Equal(t, entityKey, claim["entityId"],
		"the verdict was decided against the TRUSTED claim entity, not the model's foreign param")
}
