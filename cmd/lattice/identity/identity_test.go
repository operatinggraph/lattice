package identity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	testActorID  = "JstffActHJKMNPQRSTUV"
	testActorKey = "vtx.identity." + testActorID
	testCapKey   = "cap.identity." + testActorID
)

// TestIdentityCreateUnclaimed_HappyPath verifies that a CreateUnclaimedIdentity
// operation is submitted and accepted via NATS request-reply.
func TestIdentityCreateUnclaimed_HappyPath(t *testing.T) {
	ctx, conn, cp, cons := setupIdentityEnv(t)

	requestID := testutil.GenReqID("IDCreate")
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         testActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Test User","email":"test@example.com","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}

	doneC := make(chan processor.MessageOutcome, 1)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		out := cp.HandleMessage(ctx, m)
		select {
		case doneC <- out:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	reply, err := submitOp(ctx, conn, env)
	if err != nil {
		t.Fatalf("submitOp: %v", err)
	}

	select {
	case <-doneC:
	case <-time.After(5 * time.Second):
		t.Error("timed out waiting for pipeline")
	}

	if reply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("status = %q, want accepted (error: %+v)", reply.Status, reply.Error)
	}
}

// TestIdentityClaim_EnvelopeShape verifies that a ClaimIdentity operation
// payload is correctly marshalled into the expected envelope shape.
func TestIdentityClaim_EnvelopeShape(t *testing.T) {
	requestID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         testActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       json.RawMessage(`{"identityKey":"vtx.identity.test","claimKey":"abc"}`),
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "ClaimIdentity") {
		t.Error("marshalled envelope missing operationType")
	}
	if !strings.Contains(string(data), requestID) {
		t.Error("marshalled envelope missing requestId")
	}
}

func setupIdentityEnv(t *testing.T) (context.Context, *substrate.Conn, *processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)

	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    testCapKey,
		Actor:                  testActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{testActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	})

	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        "identity-cmd-test",
		Instance:       "identity-cmd",
		FilterSubjects: []string{"ops.default"},
	})
	return ctx, conn, cp, cons
}
