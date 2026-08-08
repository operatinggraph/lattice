package privacyworker_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/privacyworker"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// fakeVault records ShredKey calls. The other Vault methods are unused by
// privacyworker and panic if ever called, so a test that hits them fails loudly
// rather than silently passing on the wrong path.
type fakeVault struct {
	mu         sync.Mutex
	shredded   []string
	failNTimes int // ShredKey fails this many times before succeeding, per keyHolderKey
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

func (f *fakeVault) IssueSessionKey(context.Context, string, vault.Envelope, string, time.Duration) (vault.SessionKey, error) {
	panic("fakeVault: IssueSessionKey not used by privacyworker")
}

func (f *fakeVault) MAC(context.Context, string, []byte) ([]byte, error) {
	panic("fakeVault: MAC not used by privacyworker")
}

func (f *fakeVault) ShredKey(_ context.Context, keyHolderKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCounts == nil {
		f.failCounts = map[string]int{}
	}
	if f.failCounts[keyHolderKey] < f.failNTimes {
		f.failCounts[keyHolderKey]++
		return errors.New("fakeVault: injected ShredKey failure")
	}
	f.shredded = append(f.shredded, keyHolderKey)
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
	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
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

	const identityKey = "vtx.identity.ManagerHappyPathKMNP"
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

	const identityKey = "vtx.identity.ManagerMissngKeyKMNP"
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

	const identityKey = "vtx.identity.ManagerRetryKMNPQRST"
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

func publishRetentionClassKeyShredded(t *testing.T, ctx context.Context, conn *substrate.Conn, body string) {
	t.Helper()
	if err := conn.Publish(ctx, privacyworker.RetentionClassKeyShreddedFilterSubject, []byte(body), nil); err != nil {
		t.Fatalf("publish %s: %v", privacyworker.RetentionClassKeyShreddedFilterSubject, err)
	}
}

// TestManager_ShredsOnRetentionClassKeyShreddedEvent — the erase-on-expiry
// happy path: a well-formed privacy.retentionClassKeyShredded event drives
// exactly one Vault.ShredKey call for its retentionClassKey.
func TestManager_ShredsOnRetentionClassKeyShreddedEvent(t *testing.T) {
	conn, ctx := newTestConn(t)
	fv := &fakeVault{}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-rc-happy", Vault: fv,
		}).Run(runCtx)
	}()

	const holderKey = "vtx.retentionclass.RCwrkerHappyKMNPQRST"
	publishRetentionClassKeyShredded(t, ctx, conn, `{"payload":{"retentionClassKey":"`+holderKey+`"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for fv.shreddedCount(holderKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Vault.ShredKey(%s) was not called within 5s", holderKey)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestManager_BothConsumersRunIndependently proves ONE Manager.Run serves both
// holder kinds AND that it does so on two SEPARATE durables.
//
// The second half is the part worth asserting, and the reason the durable
// names are checked rather than inferred: a single consumer on a widened
// `events.privacy.>` filter with a subject-dispatching handler would satisfy
// the "both kinds get shredded" assertion identically. What it would NOT give
// is an independent cursor per kind — which is the whole reason the two exist,
// since a retention-class destruction stuck on its Vault call would otherwise
// park a person's right-to-erasure behind it.
func TestManager_BothConsumersRunIndependently(t *testing.T) {
	conn, ctx := newTestConn(t)
	fv := &fakeVault{}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-both", Vault: fv,
		}).Run(runCtx)
	}()

	const identityKey = "vtx.identity.BthKindsdentKMNPQRST"
	const holderKey = "vtx.retentionclass.BthKindsCassKMNPQRST"
	publishRetentionClassKeyShredded(t, ctx, conn, `{"payload":{"retentionClassKey":"`+holderKey+`"}}`)
	publishKeyShredded(t, ctx, conn, `{"payload":{"identityKey":"`+identityKey+`"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for fv.shreddedCount(identityKey) == 0 || fv.shreddedCount(holderKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("both kinds not shredded within 5s: identity=%d class=%d",
				fv.shreddedCount(identityKey), fv.shreddedCount(holderKey))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Two durables, not one widened filter — the cursor independence itself.
	stream, err := conn.JetStream().Stream(ctx, "core-events")
	if err != nil {
		t.Fatalf("open core-events stream: %v", err)
	}
	want := map[string]bool{"pw-both": false, privacyworker.DefaultRetentionClassDurable: false}
	for name := range stream.ConsumerNames(ctx).Name() {
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("durable %q not created — the two kinds are not on independent cursors", name)
		}
	}
}

// TestManager_MissingRetentionClassKey_DoesNotJamTheConsumer — an event with no
// subject has nothing to destroy, so it must not park the retention consumer:
// a well-formed event published AFTER it is still processed.
//
// This asserts non-jamming, NOT the Term disposition specifically — a
// NakWithDelay would also let the later message through, since JetStream's
// AckExplicit does not block the cursor behind a nak'd message. The handler
// does Term (nothing about a subject-less event improves on redelivery); what
// is proven here is the property that would actually hurt if it broke.
func TestManager_MissingRetentionClassKey_DoesNotJamTheConsumer(t *testing.T) {
	conn, ctx := newTestConn(t)
	fv := &fakeVault{}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-rc-missing", Vault: fv,
		}).Run(runCtx)
	}()

	publishRetentionClassKeyShredded(t, ctx, conn, `{"payload":{}}`)

	const holderKey = "vtx.retentionclass.RCafterMissingKMNPQR"
	publishRetentionClassKeyShredded(t, ctx, conn, `{"payload":{"retentionClassKey":"`+holderKey+`"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for fv.shreddedCount(holderKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a subject-less event jammed the retention consumer: %s never shredded", holderKey)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestManager_RecordsRetentionClassFinalizationAfterShred — the class half's
// publish-then-ack: a successful ShredKey is followed by exactly one
// RecordRetentionClassShredFinalization{vaultKeyDestroyed} on ops.system,
// naming its subject retentionClassKey (NOT identityKey — the two verbs carry
// different subject vocabularies, and submitting the wrong one would be
// rejected by the receiving script).
func TestManager_RecordsRetentionClassFinalizationAfterShred(t *testing.T) {
	conn, ctx := newTestConn(t)
	opsCons := opsStreamFor(t, ctx, conn, "core-operations")
	fv := &fakeVault{}
	const actorKey = "vtx.identity.PrivacyActorKMNPQRST"
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		_ = privacyworker.New(privacyworker.Config{
			Conn: conn, EventsStream: "core-events", Durable: "pw-rc-record", Vault: fv,
			ActorKey: actorKey,
		}).Run(runCtx)
	}()

	const holderKey = "vtx.retentionclass.RCrecrdFinaKMNPQRSTU"
	publishRetentionClassKeyShredded(t, ctx, conn, `{"payload":{"retentionClassKey":"`+holderKey+`"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for fv.shreddedCount(holderKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Vault.ShredKey(%s) was not called within 5s", holderKey)
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
		t.Fatal("no RecordRetentionClassShredFinalization op published to ops.system")
	}
	var env struct {
		RequestID     string `json:"requestId"`
		Lane          string `json:"lane"`
		OperationType string `json:"operationType"`
		Actor         string `json:"actor"`
		Payload       struct {
			RetentionClassKey string `json:"retentionClassKey"`
			IdentityKey       string `json:"identityKey"`
			Step              string `json:"step"`
		} `json:"payload"`
		ContextHint struct {
			Reads []string `json:"reads"`
		} `json:"contextHint"`
	}
	if err := json.Unmarshal(got.Data(), &env); err != nil {
		t.Fatalf("unmarshal op envelope: %v", err)
	}
	if env.OperationType != "RecordRetentionClassShredFinalization" || env.Lane != "system" || env.Actor != actorKey {
		t.Errorf("envelope = %+v, want RecordRetentionClassShredFinalization/system/%s", env, actorKey)
	}
	if env.Payload.RetentionClassKey != holderKey || env.Payload.Step != privacyworker.StepVaultKeyDestroyed {
		t.Errorf("payload = %+v, want {%s %s}", env.Payload, holderKey, privacyworker.StepVaultKeyDestroyed)
	}
	if env.Payload.IdentityKey != "" {
		t.Errorf("the class verb must not carry an identityKey, got %q", env.Payload.IdentityKey)
	}
	// Two declared reads, each load-bearing for a different reason: the piiKey is
	// the OCC condition the racing sibling record needs, and the ACTOR's own
	// vertex is what the script reads to refuse an attestation written by anyone
	// but the privacy service actor — an undeclared actor fails the op closed.
	if len(env.ContextHint.Reads) != 2 ||
		env.ContextHint.Reads[0] != holderKey+".piiKey" ||
		env.ContextHint.Reads[1] != actorKey {
		t.Errorf("contextHint.reads = %v, want [%s.piiKey <actor>] — the OCC condition plus the actor pin",
			env.ContextHint.Reads, holderKey)
	}
	if !substrate.IsValidNanoID(env.RequestID) {
		t.Errorf("requestId %q is not a Contract #1 NanoID", env.RequestID)
	}
}
