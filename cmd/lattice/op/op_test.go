package op

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/opstatus"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// staffActorID and staffActorKey are test actor credentials with
// CreateUnclaimedIdentity permission (via identity-domain package).
const (
	staffActorID  = "JstffActHJKMNPQRSTUV"
	staffActorKey = "vtx.identity." + staffActorID
	staffCapKey   = "cap.identity." + staffActorID
)

// TestOpSubmit_HappyPath verifies that submitOp successfully sends an
// OperationEnvelope via NATS request-reply and receives an accepted reply.
func TestOpSubmit_HappyPath(t *testing.T) {
	ctx, conn, cp, cons := setupOpEnv(t)

	requestID := testutil.GenReqID("CUISubmit")
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Submit Test","email":"submit@example.com","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}

	// Drive the processor inline — RequestWithContext in submitOp returns
	// only after HandleMessage has sent the reply.
	cc, err := cons.Consume(func(m jetstream.Msg) {
		cp.HandleMessage(ctx, m)
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	reply, err := submitOp(ctx, conn, env)
	if err != nil {
		t.Fatalf("submitOp: %v", err)
	}

	if reply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("status = %q, want accepted (error: %+v)", reply.Status, reply.Error)
	}
	if reply.RequestID != requestID {
		t.Errorf("requestId = %q, want %q", reply.RequestID, requestID)
	}
	if reply.OpTrackerKey != processor.TrackerKey(requestID) {
		t.Errorf("opTrackerKey = %q, want %q", reply.OpTrackerKey, processor.TrackerKey(requestID))
	}
}

// TestOpSubmit_Rejected verifies that when the Processor rejects an operation
// (unknown operationType), the reply is received promptly with a rejection status
// rather than timing out after 10 seconds.
func TestOpSubmit_Rejected(t *testing.T) {
	ctx, conn, cp, cons := setupOpEnv(t)

	requestID := testutil.GenReqID("IDReject")
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "UnknownOperationType",
		Actor:         staffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		Payload:       json.RawMessage(`{}`),
	}

	cc, err := cons.Consume(func(m jetstream.Msg) {
		cp.HandleMessage(ctx, m)
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	replyC := make(chan *processor.OperationReply, 1)
	errC := make(chan error, 1)
	go func() {
		reply, err := submitOp(ctx, conn, env)
		if err != nil {
			errC <- err
			return
		}
		replyC <- reply
	}()

	select {
	case reply := <-replyC:
		if reply.Status != processor.ReplyStatusRejected {
			t.Errorf("status = %q, want rejected", reply.Status)
		}
		if reply.Error == nil {
			t.Error("expected non-nil error in rejection reply")
		}
	case err := <-errC:
		t.Fatalf("submitOp error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for rejection reply — request-reply transport may not be working")
	}
}

// TestOpStatus_HappyPath verifies that TrackerKey produces the correct key
// shape and that tracker entries can be read from Core KV.
func TestOpStatus_HappyPath(t *testing.T) {
	requestID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	key := processor.TrackerKey(requestID)
	expected := "vtx.op." + requestID
	if key != expected {
		t.Errorf("TrackerKey(%q) = %q, want %q", requestID, key, expected)
	}
}

// TestRequestOpStatus_HappyPath verifies the `lattice op status` migration
// (op-status-read-surface-design.md Fire 4): requestOpStatus asks the
// lattice.op.status RPC — never a direct Core-KV read — and reports the
// committed verdict for a landed op and Found:false for an unknown
// requestId.
func TestRequestOpStatus_HappyPath(t *testing.T) {
	ctx, conn, cp, cons := setupOpEnv(t)

	svc := opstatus.NewService(conn, bootstrap.CoreKVBucket, nil)
	if err := svc.StartNATSListener(ctx, conn.NATS()); err != nil {
		t.Fatalf("StartNATSListener: %v", err)
	}

	requestID := testutil.GenReqID("OpStatusCLI")
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Status Test","email":"status@example.com","claimKeyHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`),
	}

	cc, err := cons.Consume(func(m jetstream.Msg) {
		cp.HandleMessage(ctx, m)
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	reply, err := submitOp(ctx, conn, env)
	if err != nil {
		t.Fatalf("submitOp: %v", err)
	}
	if reply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("status = %q, want accepted (error: %+v)", reply.Status, reply.Error)
	}

	resp, err := requestOpStatus(ctx, conn, requestID)
	if err != nil {
		t.Fatalf("requestOpStatus: %v", err)
	}
	if !resp.Found || !resp.Committed {
		t.Errorf("requestOpStatus(%q) = %+v, want found+committed", requestID, resp)
	}

	unknownID, _ := substrate.NewNanoID()
	missing, err := requestOpStatus(ctx, conn, unknownID)
	if err != nil {
		t.Fatalf("requestOpStatus (unknown): %v", err)
	}
	if missing.Found {
		t.Errorf("requestOpStatus(%q) = %+v, want not found", unknownID, missing)
	}
}

// TestOpTrace_HappyPath verifies that an auth-trace record can be read
// from Health KV using the correct key pattern.
func TestOpTrace_HappyPath(t *testing.T) {
	ctx, conn := setupBucketsOnly(t)

	requestID, _ := substrate.NewNanoID()
	traceKey := "health.processor.default.auth-trace." + requestID

	record := processor.AuthTraceRecord{
		Key:         traceKey,
		Class:       "meta.healthRecord",
		RequestID:   requestID,
		AuthOutcome: "denied",
	}
	data, _ := json.Marshal(record)
	if _, err := conn.KVPut(ctx, bootstrap.HealthKVBucket, traceKey, data); err != nil {
		t.Fatalf("KVPut trace: %v", err)
	}

	entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, traceKey)
	if err != nil {
		t.Fatalf("KVGet trace: %v", err)
	}

	var got processor.AuthTraceRecord
	if err := json.Unmarshal(entry.Value, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.RequestID != requestID {
		t.Errorf("RequestID = %q, want %q", got.RequestID, requestID)
	}
	if got.AuthOutcome != "denied" {
		t.Errorf("AuthOutcome = %q, want denied", got.AuthOutcome)
	}
}

// TestOpTrace_Missing verifies that a missing trace record returns an error
// from KVGet. The CLI treats this as a non-error exit 0 with a message.
func TestOpTrace_Missing(t *testing.T) {
	ctx, conn := setupBucketsOnly(t)

	requestID, _ := substrate.NewNanoID()
	traceKey := "health.processor.default.auth-trace." + requestID

	_, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, traceKey)
	if err == nil {
		t.Error("expected not-found error for missing trace key, got nil")
	}
}

// TestReadPayload verifies the payload reader handles file, stdin, and inline sources.
func TestReadPayload(t *testing.T) {
	t.Run("empty returns empty object", func(t *testing.T) {
		b, err := readPayload("")
		if err != nil {
			t.Fatalf("readPayload empty: %v", err)
		}
		if string(b) != "{}" {
			t.Errorf("got %q, want {}", string(b))
		}
	})

	t.Run("inline JSON passthrough", func(t *testing.T) {
		b, err := readPayload(`{"foo":"bar"}`)
		if err != nil {
			t.Fatalf("readPayload inline: %v", err)
		}
		if !strings.Contains(string(b), "foo") {
			t.Errorf("got %q, want JSON containing 'foo'", string(b))
		}
	})

	t.Run("@file reads file", func(t *testing.T) {
		tmp := t.TempDir() + "/payload.json"
		if err := os.WriteFile(tmp, []byte(`{"key":"value"}`), 0600); err != nil {
			t.Fatal(err)
		}
		b, err := readPayload("@" + tmp)
		if err != nil {
			t.Fatalf("readPayload @file: %v", err)
		}
		if string(b) != `{"key":"value"}` {
			t.Errorf("got %q", string(b))
		}
	})
}

// setupOpEnv builds a full Phase 1 harness with a running Processor pipeline.
func setupOpEnv(t *testing.T) (context.Context, *substrate.Conn, *processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)

	// Seed a staff cap doc granting CreateUnclaimedIdentity.
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    staffCapKey,
		Actor:                  staffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{staffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	})

	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        "op-cmd-test",
		Instance:       "op-cmd",
		FilterSubjects: []string{"ops.default"},
	})
	return ctx, conn, cp, cons
}

// setupBucketsOnly builds a minimal harness with KV buckets only.
func setupBucketsOnly(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "op-test-min"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}
