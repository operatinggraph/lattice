package privacyworker_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/privacyworker"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
)

// fakeVault records ShredKey calls. The other Vault methods are unused by
// privacyworker and panic if ever called, so a test that hits them fails loudly
// rather than silently passing on the wrong path.
type fakeVault struct {
	mu         sync.Mutex
	shredded   []string
	failNTimes int // ShredKey fails this many times before succeeding, per identityKey
	failCounts map[string]int
}

func (f *fakeVault) CreateIdentityKey(context.Context, string) (vault.Envelope, error) {
	panic("fakeVault: CreateIdentityKey not used by privacyworker")
}

func (f *fakeVault) Encrypt(context.Context, string, vault.Envelope, []byte) (vault.Ciphertext, error) {
	panic("fakeVault: Encrypt not used by privacyworker")
}

func (f *fakeVault) Decrypt(context.Context, string, vault.Envelope, vault.Ciphertext) ([]byte, error) {
	panic("fakeVault: Decrypt not used by privacyworker")
}

func (f *fakeVault) WrapKey(context.Context, string, vault.Envelope, []byte) (vault.Ciphertext, error) {
	panic("fakeVault: WrapKey not used by privacyworker")
}

func (f *fakeVault) UnwrapKey(context.Context, string, vault.Envelope, vault.Ciphertext) ([]byte, error) {
	panic("fakeVault: UnwrapKey not used by privacyworker")
}

func (f *fakeVault) ShredKey(_ context.Context, identityKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCounts == nil {
		f.failCounts = map[string]int{}
	}
	if f.failCounts[identityKey] < f.failNTimes {
		f.failCounts[identityKey]++
		return errors.New("fakeVault: injected ShredKey failure")
	}
	f.shredded = append(f.shredded, identityKey)
	return nil
}

func (f *fakeVault) shreddedCount(identityKey string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, k := range f.shredded {
		if k == identityKey {
			n++
		}
	}
	return n
}

// newTestConn starts an embedded NATS server + JetStream, wraps it in a
// substrate.Conn, and provisions a core-events-shaped stream. Mirrors the
// jsstore.Dir(t) StoreDir convention required for embedded fixtures to
// survive parallel test teardown.
func newTestConn(t *testing.T) (*substrate.Conn, context.Context) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("substrate.Wrap: %v", err)
	}
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	const eventsStream = "core-events"
	if _, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     eventsStream,
		Subjects: []string{"events.>"},
	}); err != nil {
		t.Fatalf("create %s stream: %v", eventsStream, err)
	}
	return conn, ctx
}

func publishKeyShredded(t *testing.T, ctx context.Context, conn *substrate.Conn, body string) {
	t.Helper()
	if err := conn.Publish(ctx, privacyworker.KeyShreddedFilterSubject, []byte(body), nil); err != nil {
		t.Fatalf("publish %s: %v", privacyworker.KeyShreddedFilterSubject, err)
	}
}

// TestManager_ShredsOnKeyShreddedEvent is the happy path: a well-formed
// privacy.keyShredded event drives exactly one Vault.ShredKey call for its
// identityKey.
func TestManager_ShredsOnKeyShreddedEvent(t *testing.T) {
	conn, ctx := newTestConn(t)
	fv := &fakeVault{}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-happy", Vault: fv,
		}).Run(runCtx)
	}()

	const identityKey = "vtx.identity.ManagerHappyPathKMNPQ"
	publishKeyShredded(t, ctx, conn, `{"payload":{"identityKey":"`+identityKey+`"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for fv.shreddedCount(identityKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Vault.ShredKey(%s) was not called within 5s", identityKey)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestManager_MalformedBody_DoesNotJamTheConsumer proves a poison message
// (unparseable JSON) is terminated rather than nak-looped forever — a
// well-formed message published AFTER it still gets processed.
func TestManager_MalformedBody_DoesNotJamTheConsumer(t *testing.T) {
	conn, ctx := newTestConn(t)
	fv := &fakeVault{}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-poison", Vault: fv,
		}).Run(runCtx)
	}()

	publishKeyShredded(t, ctx, conn, `{not valid json`)

	const identityKey = "vtx.identity.ManagerPoisonKMNPQRS"
	publishKeyShredded(t, ctx, conn, `{"payload":{"identityKey":"`+identityKey+`"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for fv.shreddedCount(identityKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("consumer jammed on the poison message: the well-formed message that followed it was never processed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestManager_MissingIdentityKey_Terminated proves an event missing
// payload.identityKey is terminated (never calls ShredKey, never redelivers
// forever) — the malformed-schema sibling of the unparseable-JSON case.
func TestManager_MissingIdentityKey_Terminated(t *testing.T) {
	conn, ctx := newTestConn(t)
	fv := &fakeVault{}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-missingkey", Vault: fv,
		}).Run(runCtx)
	}()

	publishKeyShredded(t, ctx, conn, `{"payload":{}}`)

	const identityKey = "vtx.identity.ManagerMissingKeyKMNPQ"
	publishKeyShredded(t, ctx, conn, `{"payload":{"identityKey":"`+identityKey+`"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for fv.shreddedCount(identityKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("consumer jammed on the missing-identityKey event")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestManager_ShredKeyError_Retries proves a transient Vault.ShredKey failure
// is retried (NakWithDelay), not dropped — the redelivery loop that backstops
// a crash/KMS-blip until the shred confirms.
func TestManager_ShredKeyError_Retries(t *testing.T) {
	conn, ctx := newTestConn(t)
	// One injected failure: the Manager's redelivery floor is several seconds
	// (matching the outbox/object-manager convention of a multi-second nak
	// delay), so this test's deadline must clear ONE wait, not chase a tight
	// race against the exact floor.
	fv := &fakeVault{failNTimes: 1}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-retry", Vault: fv,
		}).Run(runCtx)
	}()

	const identityKey = "vtx.identity.ManagerRetryKMNPQRSTU"
	publishKeyShredded(t, ctx, conn, `{"payload":{"identityKey":"`+identityKey+`"}}`)

	deadline := time.Now().Add(15 * time.Second)
	for fv.shreddedCount(identityKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Vault.ShredKey(%s) never succeeded despite retries", identityKey)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// opsStreamFor provisions a core-operations-shaped stream so the manager's
// RecordShredFinalization publish (Fire 4b) has somewhere to land, and
// returns a consumer over it for assertions.
func opsStreamFor(t *testing.T, ctx context.Context, conn *substrate.Conn, name string) jetstream.Consumer {
	t.Helper()
	stream, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     name,
		Subjects: []string{"ops.>"},
	})
	if err != nil {
		t.Fatalf("create %s stream: %v", name, err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{Durable: "ops-observer"})
	if err != nil {
		t.Fatalf("create ops observer consumer: %v", err)
	}
	return cons
}

// TestManager_RecordsFinalizationAfterShred proves Fire 4b's publish-then-ack:
// with an ActorKey configured, a successful ShredKey is followed by exactly
// one RecordShredFinalization{vaultKeyDestroyed} op on ops.system, carrying
// the privacy actor and the shredded identityKey.
func TestManager_RecordsFinalizationAfterShred(t *testing.T) {
	conn, ctx := newTestConn(t)
	opsCons := opsStreamFor(t, ctx, conn, "core-operations")
	fv := &fakeVault{}
	const actorKey = "vtx.identity.PrivacyActorKMNPQRST"
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-record", Vault: fv,
			ActorKey: actorKey,
		}).Run(runCtx)
	}()

	const identityKey = "vtx.identity.ManagerRecordKMNPQRS"
	publishKeyShredded(t, ctx, conn, `{"payload":{"identityKey":"`+identityKey+`"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for fv.shreddedCount(identityKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Vault.ShredKey(%s) was not called within 5s", identityKey)
		}
		time.Sleep(20 * time.Millisecond)
	}

	msgs, err := opsCons.Fetch(1, jetstream.FetchMaxWait(4*time.Second))
	if err != nil {
		t.Fatalf("fetch from ops stream: %v", err)
	}
	var got jetstream.Msg
	for m := range msgs.Messages() {
		got = m
		_ = m.Ack()
	}
	if got == nil {
		t.Fatal("no RecordShredFinalization op published to ops.system")
	}
	if got.Subject() != "ops.system" {
		t.Errorf("op published to %q, want ops.system", got.Subject())
	}
	var env struct {
		RequestID     string `json:"requestId"`
		Lane          string `json:"lane"`
		OperationType string `json:"operationType"`
		Actor         string `json:"actor"`
		Payload       struct {
			IdentityKey string `json:"identityKey"`
			Step        string `json:"step"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(got.Data(), &env); err != nil {
		t.Fatalf("unmarshal op envelope: %v", err)
	}
	if env.OperationType != "RecordShredFinalization" || env.Lane != "system" || env.Actor != actorKey {
		t.Errorf("envelope = %+v, want RecordShredFinalization/system/%s", env, actorKey)
	}
	if env.Payload.IdentityKey != identityKey || env.Payload.Step != privacyworker.StepVaultKeyDestroyed {
		t.Errorf("payload = %+v, want {%s %s}", env.Payload, identityKey, privacyworker.StepVaultKeyDestroyed)
	}
	if !substrate.IsValidNanoID(env.RequestID) {
		t.Errorf("requestId %q is not a Contract #1 NanoID", env.RequestID)
	}
}
