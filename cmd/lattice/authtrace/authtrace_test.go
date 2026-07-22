package authtrace

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestAuthTrace_HappyPath verifies that an auth-trace record can be read
// from Health KV using the key pattern health.processor.<instance>.auth-trace.<requestId>.
func TestAuthTrace_HappyPath(t *testing.T) {
	ctx, conn := setupAuthTraceEnv(t)

	requestID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	instance := "default"
	traceKey := "health.processor." + instance + ".auth-trace." + requestID

	record := processor.AuthTraceRecord{
		Key:         traceKey,
		Class:       "meta.healthRecord",
		RequestID:   requestID,
		Actor:       "vtx.identity.testActor000000001",
		Operation:   "CreateUnclaimedIdentity",
		AuthOutcome: "denied",
		AuthCode:    string(processor.ErrCodeAuthDenied),
		AuthReason:  "no matching permission",
		ObservedAt:  time.Now().UTC().Format(time.RFC3339),
		Plane1: processor.AuthTracePlane1{
			CapabilityKVKey: "cap.identity.testActor000000001",
			Result:          "no-match",
		},
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.HealthKVBucket, traceKey, data); err != nil {
		t.Fatalf("KVPut trace: %v", err)
	}

	// Read it back.
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
	if got.Plane1.Result != "no-match" {
		t.Errorf("Plane1.Result = %q, want no-match", got.Plane1.Result)
	}
}

func setupAuthTraceEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "authtrace-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}
