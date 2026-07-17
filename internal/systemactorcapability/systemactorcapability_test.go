//go:build systemactorcapability

// Package systemactorcapability_test is the system-actor-package-op-grants
// Fire 2 e2e proof (system-actor-package-op-grants-design.md §8): boots the
// REAL Processor under LATTICE_AUTH_MODE=capability (the Fire 1 union read,
// a6cfbfc) and the REAL Refractor projecting both the core `capability`
// anchor lens and rbac-domain's `capabilityRoles` lens into capability-kv,
// installs the real package chain (rbac -> identity -> orchestration-base ->
// objects-base -> privacy-base), and submits each of the four
// system-actor-submitted engine ops the design names — Weaver's MarkExpired,
// Loom's CreateTask, object-store-manager's DetachObject, the privacy actor's
// RecordShredFinalization — as the REAL kernel-seeded system actor on the
// privileged `system` lane, asserting each authorizes (not AuthDenied /
// LaneUnauthorized) with the stub OFF.
//
// This proves the auth BOUNDARY only: each op is submitted directly (not via
// a running Weaver/Loom/objmgr/privacy-worker engine), because those engines'
// dispatch mechanics are already e2e-proven elsewhere under AuthModeStub
// (internal/leaseconvergence, internal/objectgc, internal/cryptoshred) — the
// one thing NONE of those harnesses exercise is the real capability-auth
// decision for a system actor's package op, which is exactly Fire 1/2's gap.
//
// Gated behind the `systemactorcapability` build tag — runs only via `make
// test-system-actor-capability`, mirroring the objectgc/cryptoshred precedent
// of keeping heavy multi-package e2e out of the untagged `go test ./...`.
package systemactorcapability_test

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
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	objectsbase "github.com/asolgan/lattice/packages/objects-base"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	privacybase "github.com/asolgan/lattice/packages/privacy-base"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
)

const replyInboxHeader = "Lattice-Reply-Inbox"

type harness struct {
	t      *testing.T
	ctx    context.Context
	conn   *substrate.Conn
	logger *slog.Logger
	coreKV *substrate.KV
	capKV  *substrate.KV
	wired  map[string]chan struct{}
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

	// --- real bootstrap substrate (buckets + streams + primordial identities,
	// incl. the holdsRole->operator links for admin + every kernel service
	// actor) ---
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

	// --- the real Processor, AuthModeCapability, RbacRolesActive wired with
	// the REAL kernel system-actor set (the Fire 1 union read under test) ---
	systemActorKeys, err := bootstrap.SystemActorKeys(ctx, conn)
	require.NoError(t, err)
	require.NotEmpty(t, systemActorKeys, "SeedPrimordial must have seeded holdsRole->operator for the kernel service actors")

	cp, _, err := processor.MakePipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, bootstrap.CapabilityKVBucket,
		processor.AuthModeCapability, false, logger, "sac-processor",
		processor.AuthWiring{RbacRolesActive: true, SystemActorKeys: systemActorKeys}, nil)
	require.NoError(t, err)
	procCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "sac-processor",
		FilterSubjects: []string{"ops.default", "ops.urgent", "ops.system", "ops.meta"},
		AckWait:        10 * time.Second,
	}, logger)
	require.NoError(t, err)
	procCC, err := procCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(ctx, m) })
	require.NoError(t, err)
	t.Cleanup(procCC.Stop)
	go func() { _ = outbox.New(conn, bootstrap.CoreKVBucket, logger).Run(ctx) }()

	// --- Refractor: adjacency CDC + the capability-projecting lens source.
	// The core `capability` anchor is kernel-seeded and activates immediately;
	// wait for it BEFORE installing anything, since InstallPackage itself
	// needs the admin's cap.<admin> floor grant to authorize under real
	// capability auth (the stub-off proof starts at the very first install).
	h.startAdjacency(ctx, adjKV)
	h.startCapabilitySource(ctx, adjKV, coreKV)
	h.awaitLensWired("capability", 20*time.Second)
	h.awaitCapDoc("cap.identity."+bootstrap.BootstrapIdentityID, 20*time.Second)

	// --- install the real chain via the real InstallPackage op path. rbac-domain
	// makes rbac-domain's `capabilityRoles` lens discoverable mid-loop;
	// startCapabilitySource's watch picks it up and wires it the moment it
	// appears (awaitLensWired below blocks on that separately). ---
	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	for _, pkg := range []pkgmgr.Definition{
		rbacdomain.Package, identitydomain.Package, orchestrationbase.Package, objectsbase.Package, privacybase.Package,
	} {
		_, err := installer.Install(ctx, pkg)
		require.NoErrorf(t, err, "install %s", pkg.Name)
	}
	h.awaitLensWired("capabilityRoles", 20*time.Second)

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

// startCapabilitySource watches CoreKVSource and wires EACH capability-
// projecting lens through projection.InstallActorAggregate onto capability-kv
// as it is discovered — the production actor-aggregate path, mirroring
// internal/leaseconvergence / internal/objectgc's single-lens activation loop
// but async over an open-ended set. The core `capability` anchor is
// kernel-seeded and activates immediately; rbac-domain's `capabilityRoles`
// only exists once rbac-domain installs, so wiring cannot wait for both up
// front — InstallPackage itself needs the anchor's floor grant to authorize
// the very first install. wired[name] closes once that lens's pipeline is
// running, for awaitLensWired to block on.
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

// awaitLensWired blocks until startCapabilitySource has wired the named
// lens's pipeline.
func (h *harness) awaitLensWired(name string, deadline time.Duration) {
	h.t.Helper()
	select {
	case <-h.wired[name]:
	case <-time.After(deadline):
		h.t.Fatalf("lens %q did not activate within %s", name, deadline)
	}
}

// submitOp publishes an OperationEnvelope on ops.<lane> and waits for the
// Processor reply. Class is left empty on every call: the real engines
// (Loom/Weaver) submit Processor-faithful envelopes that omit class and rely
// on the operationType->class reverse index (RF#1, Story 14.6) — this harness
// mirrors that, not a test-only shortcut.
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

// submitOpAccepted retries submitOp until it observes ReplyStatusAccepted or
// deadline elapses, failing with the last denial otherwise. The role-derived
// side of the union (cap.roles.<actor>) settles asynchronously off CDC as
// each package's permission grants land — a system actor's package op can
// transiently deny while that projection is still converging (the design's
// own residual-risk note: self-healing, engine submitters retry), so the
// PROOF here is "authorizes once settled," not "authorizes on the very first
// attempt racing the projector."
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
			return last // a non-auth rejection is a real failure; don't mask it by retrying
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

// awaitCapDoc polls capability-kv for key, returning the decoded envelope.
// The real Refractor lenses project asynchronously off CDC, so the four
// system actors' cap.<actor> / cap.roles.<actor> docs land on their own
// schedule after SeedPrimordial + package install.
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

// findOpMetaKey scans core-kv for the op-meta vertex (vtx.meta.<id>, class
// meta.ddl.vertexType, data.operationType == operationType) a package's
// OpMetas declared at install (internal/pkgmgr/build.go) — the forOperation
// link target CreateTask requires. DDL declaration vertices share the same
// class but carry no operationType field, so the field match disambiguates.
func (h *harness) findOpMetaKey(operationType string) string {
	h.t.Helper()
	keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
	require.NoError(h.t, err)
	for _, k := range keys {
		rest := strings.TrimPrefix(k, "vtx.meta.")
		if rest == k || strings.Contains(rest, ".") {
			continue // not a vtx.meta.<id> root (either unrelated or an aspect)
		}
		entry, gErr := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, k)
		if gErr != nil {
			continue
		}
		var env struct {
			Class string `json:"class"`
			Data  struct {
				OperationType string `json:"operationType"`
			} `json:"data"`
		}
		if json.Unmarshal(entry.Value, &env) != nil {
			continue
		}
		if env.Class == "meta.ddl.vertexType" && env.Data.OperationType == operationType {
			return k
		}
	}
	h.t.Fatalf("op-meta vertex for %s not found in core-kv", operationType)
	return ""
}

// TestSystemActorCapability_FourEnginePathsAuthorize is the Fire 2 e2e proof:
// with the stub OFF, each system-actor-submitted engine op from
// system-actor-package-op-grants-design.md's table authorizes under the real
// CapabilityAuthorizer + the Fire 1 union read (cap.<actor> U cap.roles.<actor>).
func TestSystemActorCapability_FourEnginePathsAuthorize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system-actor capability e2e in -short mode")
	}
	h := newHarness(t)

	// --- fixture: one identity, alive, for MarkExpired/CreateTask/RecordShredFinalization.
	// The role-derived side of the union (cap.roles.<actor>) settles
	// asynchronously as each installed package's permission grants land, so
	// even this ordinary-actor op can transiently deny right after install —
	// retry-tolerant like the four gated submissions below. ---
	idReply := h.submitOpAccepted("CreateUnclaimedIdentity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name": "Fixture Actor", "email": "fixture@lattice.example", "claimKeyHash": strings.Repeat("a", 64),
	}, nil, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, idReply.Status, "CreateUnclaimedIdentity: %+v", idReply.Error)
	identityKey := idReply.PrimaryKey

	// --- 1. Weaver: MarkExpired (temporal lane, system-class) ---
	mr := h.submitOpAccepted("MarkExpired", "system", bootstrap.WeaverIdentityKey, map[string]any{
		"entityKey": identityKey, "expiredAt": time.Now().UTC().Format(time.RFC3339),
	}, []string{identityKey}, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, mr.Status, "Weaver MarkExpired: %+v", mr.Error)

	// --- 2. Loom: CreateTask (system lane; Loom submits under its
	// operator-equivalent service actor) ---
	forOp := h.findOpMetaKey("AttachObject")
	tr := h.submitOpAccepted("CreateTask", "system", bootstrap.LoomIdentityKey, map[string]any{
		"assignee": identityKey, "forOperation": forOp, "scopedTo": identityKey,
		"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}, []string{identityKey, forOp}, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, tr.Status, "Loom CreateTask: %+v", tr.Error)

	// --- 3. object-store-manager: DetachObject (system lane) — attach for
	// real first (as admin, itself proving the roles-derived AttachObject
	// grant), then detach as the objmgr system actor (the gated path) ---
	info, err := h.conn.ObjectPut(h.ctx, bootstrap.CoreObjectsBucket, "sac-store", strings.NewReader("bytes"), 1<<20)
	require.NoError(t, err)
	oid := substrate.SHA256NanoID("object:" + info.Digest)
	ar := h.submitOpAccepted("AttachObject", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"digest": info.Digest, "size": info.Size, "contentType": "text/plain",
		"storeName": "sac-store", "targetKey": identityKey, "linkName": "photoOf",
	}, []string{identityKey}, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, ar.Status, "AttachObject: %+v", ar.Error)
	objKey := "vtx.object." + oid
	idID := strings.TrimPrefix(identityKey, "vtx.identity.")
	link := "lnk.object." + oid + ".photoOf.identity." + idID

	dr := h.submitOpAccepted("DetachObject", "system", bootstrap.ObjmgrIdentityKey, map[string]any{
		"oid": oid, "targetKey": identityKey, "linkName": "photoOf",
	}, []string{link, objKey}, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, dr.Status, "objmgr DetachObject: %+v", dr.Error)

	// --- 4. privacy actor: RecordShredFinalization (system lane). Fixture the
	// already-shredded piiKey aspect directly (ShredIdentityKey's own grant
	// posture is deployment-owned per privacy-base's Permissions() doc comment
	// and is not one of the four gated paths under test here) ---
	piiKeyKey := identityKey + ".piiKey"
	piiBody, err := bootstrap.MakeAspectEnvelope(piiKeyKey, identityKey, "piiKey", "piiKey", map[string]any{
		"wrappedDEK": "", "keyId": identityKey, "kekVersion": "", "alg": "",
		"createdAt": time.Now().UTC().Format(time.RFC3339), "shredded": true,
		"shreddedAt": time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	_, err = h.coreKV.Put(h.ctx, piiKeyKey, piiBody)
	require.NoError(t, err)

	rr := h.submitOpAccepted("RecordShredFinalization", "system", bootstrap.PrivacyIdentityKey, map[string]any{
		"identityKey": identityKey, "step": "vaultKeyDestroyed",
	}, []string{piiKeyKey}, 15*time.Second)
	require.Equalf(t, processor.ReplyStatusAccepted, rr.Status, "privacy RecordShredFinalization: %+v", rr.Error)
}
