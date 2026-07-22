//go:build objectgc

// Package objectgc_test is the v1b object-GC end-to-end harness: it boots
// Processor + outbox + Refractor + Weaver + the object-store-manager together
// against one embedded-NATS server, installs the real chain (rbac → identity →
// objects-base), attaches an object to an identity, detaches it, and observes
// the FULL Loop A+B chain converge — the orphan is detected by the objectLiveness
// lens, dispatched by Weaver's directOp(TombstoneObject), soft-deleted by the
// Processor (epoch-CAS), and its bytes reclaimed by the manager off the
// object.tombstoned event.
//
// This closes the integration seams the unit tests cannot: the lens → bucket →
// Weaver directOp dispatch (objects-base is the first directOp consumer), the
// linkEpoch round-trip across the JSON bucket boundary, and the live
// object.tombstoned publish → manager durable-consume. Processor runs in
// AuthModeStub (the GC mechanics are the proof, not capability auth).
//
// Gated behind the `objectgc` build tag — runs only via `make test-object-gc`,
// never the untagged `go test ./...`, keeping the all-engines e2e to its gate.
package objectgc_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/objectmanager"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/processor/outbox"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/weaver"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
	objectsbase "github.com/operatinggraph/lattice/packages/objects-base"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

const replyInboxHeader = "Lattice-Reply-Inbox"

type harness struct {
	t      *testing.T
	ctx    context.Context
	conn   *substrate.Conn
	logger *slog.Logger
	coreKV *substrate.KV
	convKV *substrate.KV // weaver-targets
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

	// Real bootstrap substrate (buckets incl. core-objects + streams + primordials).
	testutil.EnsurePrimordials(t)
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

	// Processor (all lanes, AuthModeStub) + the transactional outbox publisher.
	cp, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "og-processor")
	require.NoError(t, err)
	procCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "og-processor",
		FilterSubjects: []string{"ops.default", "ops.urgent", "ops.system", "ops.meta"},
		AckWait:        10 * time.Second,
	}, logger)
	require.NoError(t, err)
	procCC, err := procCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(ctx, m) })
	require.NoError(t, err)
	t.Cleanup(procCC.Stop)
	go func() { _ = outbox.New(conn, bootstrap.CoreKVBucket, logger).Run(ctx) }()

	// Install rbac → identity → objects-base via the real InstallPackage op path.
	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	for _, pkg := range []pkgmgr.Definition{rbacdomain.Package, identitydomain.Package, objectsbase.Package} {
		_, err := installer.Install(ctx, pkg)
		require.NoErrorf(t, err, "install %s", pkg.Name)
	}

	// Refractor: adjacency CDC + the objectLiveness actorAggregate projection.
	h.startAdjacency(ctx, adjKV)
	h.startRefractor(ctx, adjKV, coreKV, convKV)

	// Weaver: lane-1 gap dispatch (drives the directOp(TombstoneObject)).
	go func() {
		_ = weaver.NewEngine(conn, weaver.Config{
			CoreKVBucket:        bootstrap.CoreKVBucket,
			WeaverTargetsBucket: bootstrap.WeaverTargetsBucket,
			WeaverStateBucket:   bootstrap.WeaverStateBucket,
			HealthKVBucket:      bootstrap.HealthKVBucket,
			CoreSchedulesStream: bootstrap.CoreSchedulesStreamName,
			ActorKey:            bootstrap.WeaverIdentityKey,
			Lane:                "system",
			Instance:            "og-weaver",
			HeartbeatEvery:      200 * time.Millisecond,
			Logger:              logger,
		}).Start(ctx)
	}()

	// The object-store-manager: Loop B byte-janitor + the §22 owner-tombstone-
	// cascade (ActorKey set → the cascade consumer runs and submits DetachObject
	// on owner death). Reconcile interval long so only the event paths run in the
	// test window.
	go func() {
		_ = objectmanager.New(objectmanager.Config{
			Conn:              conn,
			CoreKVBucket:      bootstrap.CoreKVBucket,
			ObjectsBucket:     bootstrap.CoreObjectsBucket,
			EventsStream:      bootstrap.CoreEventsStreamName,
			HealthKVBucket:    bootstrap.HealthKVBucket,
			ActorKey:          bootstrap.ObjmgrIdentityKey,
			OpLane:            "system",
			Instance:          "og-manager",
			ReconcileInterval: time.Hour,
			Logger:            logger,
		}).Run(ctx)
	}()

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

func (h *harness) startRefractor(ctx context.Context, adjKV, coreKV, convKV *substrate.KV) {
	fullEngine := full.New()
	projRev := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	src := lens.NewCoreKVSource(h.conn, bootstrap.CoreKVBucket, "test", h.logger)
	loaded := make(chan *lens.Rule, 32)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(h.t, src.Start(ctx))

	var rule *lens.Rule
	deadline := time.Now().Add(25 * time.Second)
	for rule == nil {
		if time.Now().After(deadline) {
			h.t.Fatal("did not activate the objectLiveness lens within 25s")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "objectLiveness" {
				rule = r
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	require.Equal(h.t, "actorAggregate", rule.ProjectionKind)
	require.NotNil(h.t, rule.CompiledRule)

	adpt, err := adapter.New(convKV, rule.Into.Key, adapter.DeleteModeHard)
	require.NoError(h.t, err)
	p, err := pipeline.New(rule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(h.t, err)
	p.UseFullEngine(fullEngine, rule.CompiledRule)
	require.True(h.t, projection.InstallActorAggregate(p, adpt, rule, projRev, adjKV, coreKV, h.logger),
		"objectLiveness lens must install through projection.InstallActorAggregate")
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
}

func (h *harness) submitOp(operationType, class, actor string, payload map[string]any, reads []string) *processor.OperationReply {
	h.t.Helper()
	payloadBytes, _ := json.Marshal(payload)
	reqID, err := substrate.NewNanoID()
	require.NoError(h.t, err)
	env := &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.LaneDefault, OperationType: operationType,
		Class: class, Actor: actor, SubmittedAt: time.Now().UTC().Format(time.RFC3339),
		Payload: json.RawMessage(payloadBytes), ContextHint: &processor.ContextHint{Reads: reads},
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

// seedIdentity creates an applicant identity (the object owner).
func (h *harness) seedIdentity() string {
	h.t.Helper()
	claimSum := sha256.Sum256([]byte("owner-" + mustNanoID(h.t)))
	r := h.submitOp("CreateUnclaimedIdentity", "identity", bootstrap.BootstrapIdentityKey, map[string]any{
		"name": "Owner", "email": mustNanoID(h.t) + "@loftspace.example",
		"claimKeyHash": hex.EncodeToString(claimSum[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, r.Status, "CreateUnclaimedIdentity: %+v", r.Error)
	return r.PrimaryKey
}

// attach uploads bytes + submits AttachObject; returns the object id + storeName.
func (h *harness) attach(targetKey, content string, reads []string) (oid, storeName string) {
	h.t.Helper()
	storeName = mustNanoID(h.t)
	info, err := h.conn.ObjectPut(h.ctx, bootstrap.CoreObjectsBucket, storeName, bytes.NewReader([]byte(content)), 1<<20)
	require.NoError(h.t, err)
	oid = substrate.SHA256NanoID("object:" + info.Digest)
	r := h.submitOp("AttachObject", "object", bootstrap.BootstrapIdentityKey, map[string]any{
		"digest": info.Digest, "size": info.Size, "contentType": "text/plain",
		"storeName": storeName, "targetKey": targetKey, "linkName": "photoOf",
	}, reads)
	require.Equalf(h.t, processor.ReplyStatusAccepted, r.Status, "AttachObject: %+v", r.Error)
	return oid, storeName
}

func (h *harness) readRow(oid string) map[string]any {
	entry, err := h.convKV.Get(h.ctx, "objectLiveness."+oid)
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return nil
	}
	var row map[string]any
	if json.Unmarshal(entry.Value, &row) != nil {
		return nil
	}
	return row
}

func (h *harness) objectIsDeleted(oid string) bool {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, "vtx.object."+oid)
	if err != nil {
		return false
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	_ = json.Unmarshal(entry.Value, &doc)
	return doc.IsDeleted
}

func (h *harness) bytesGone(storeName string) bool {
	_, err := h.conn.ObjectGetInfo(h.ctx, bootstrap.CoreObjectsBucket, storeName)
	return err != nil
}

func (h *harness) eventually(desc string, d time.Duration, cond func() bool) {
	h.t.Helper()
	cut := time.Now().Add(d)
	for time.Now().Before(cut) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	h.t.Fatalf("condition not met within %s: %s", d, desc)
}

func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

// TestObjectGC_AttachDetachReclaims is the full Loop A+B convergence proof:
// attach an object to an identity, detach it (orphaning it), and observe the
// objectLiveness lens → Weaver directOp(TombstoneObject) → object.tombstoned →
// object-store-manager chain reclaim the bytes. It closes the directOp-dispatch
// + linkEpoch-round-trip + live-event-consume seams in one pass.
func TestObjectGC_AttachDetachReclaims(t *testing.T) {
	h := newHarness(t)
	idKey := h.seedIdentity()
	idID := idKey[len("vtx.identity."):]

	oid, storeName := h.attach(idKey, "the-bytes", []string{idKey})
	objKey := "vtx.object." + oid
	contentKey := objKey + ".content"
	link := "lnk.object." + oid + ".photoOf.identity." + idID

	// The lens projects the object as NOT orphaned while the link is live.
	h.eventually("objectLiveness row projects (not orphaned)", 20*time.Second, func() bool {
		row := h.readRow(oid)
		return row != nil && row["missing_owner"] == false
	})

	// Detach the only link → orphaned → Weaver dispatches TombstoneObject.
	dr := h.submitOp("DetachObject", "object", bootstrap.BootstrapIdentityKey, map[string]any{
		"oid": oid, "targetKey": idKey, "linkName": "photoOf",
	}, []string{link, objKey})
	require.Equalf(t, processor.ReplyStatusAccepted, dr.Status, "DetachObject: %+v", dr.Error)

	// The full chain converges: the object is soft-deleted (Weaver→directOp→
	// TombstoneObject) AND its bytes are reclaimed (manager off object.tombstoned).
	h.eventually("object vertex soft-deleted by the GC", 30*time.Second, func() bool {
		return h.objectIsDeleted(oid)
	})
	h.eventually("object .content soft-deleted", 10*time.Second, func() bool {
		entry, err := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, contentKey)
		if err != nil {
			return false
		}
		var doc struct {
			IsDeleted bool `json:"isDeleted"`
		}
		_ = json.Unmarshal(entry.Value, &doc)
		return doc.IsDeleted
	})
	h.eventually("object bytes reclaimed by the manager", 20*time.Second, func() bool {
		return h.bytesGone(storeName)
	})
}

// softDeleteOwner rewrites an owner vertex root with isDeleted=true — the
// authoritative post-tombstone core-kv state, which fires the KV-stream CDC the
// §22 cascade consumer reacts to. (The cascade keys off the owner's core-kv
// isDeleted state, independent of which op produced it; the unit tests prove the
// handler logic, this e2e proves the live wiring.)
func (h *harness) softDeleteOwner(ownerKey string) {
	h.t.Helper()
	entry, err := h.conn.KVGet(h.ctx, bootstrap.CoreKVBucket, ownerKey)
	require.NoError(h.t, err)
	var doc map[string]any
	require.NoError(h.t, json.Unmarshal(entry.Value, &doc))
	doc["isDeleted"] = true
	b, _ := json.Marshal(doc)
	_, err = h.conn.KVUpdate(h.ctx, bootstrap.CoreKVBucket, ownerKey, b, entry.Revision)
	require.NoError(h.t, err)
}

// TestObjectGC_OwnerTombstoneCascadeReclaims is the §22 owner-tombstone-cascade
// convergence proof: attach an object to an identity, then tombstone the OWNER
// (not the object, and with no explicit detach). The cascade consumer must react
// to the owner's core-kv tombstone, submit DetachObject for the now-dangling
// link, and thereby drive the SAME Loop A+B that reclaims the orphan — closing
// the §21.2 dead-target byte LEAK. Without the cascade the object's liveLinks
// would stay stale ≥1 and the bytes would leak forever.
func TestObjectGC_OwnerTombstoneCascadeReclaims(t *testing.T) {
	h := newHarness(t)
	idKey := h.seedIdentity()

	oid, storeName := h.attach(idKey, "cascade-bytes", []string{idKey})

	// The lens projects the object as NOT orphaned while the owner is alive.
	h.eventually("objectLiveness row projects (not orphaned)", 20*time.Second, func() bool {
		row := h.readRow(oid)
		return row != nil && row["missing_owner"] == false
	})

	// Tombstone the OWNER — no explicit DetachObject. The cascade must do the rest.
	h.softDeleteOwner(idKey)

	// The cascade detaches the dangling link → liveLinks 0 → Loop A dispatches
	// TombstoneObject → the object is soft-deleted → Loop B reclaims the bytes.
	h.eventually("object vertex soft-deleted via the owner-tombstone-cascade", 40*time.Second, func() bool {
		return h.objectIsDeleted(oid)
	})
	h.eventually("object bytes reclaimed after the cascade", 20*time.Second, func() bool {
		return h.bytesGone(storeName)
	})
}
