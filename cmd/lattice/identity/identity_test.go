package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	testActorID  = "JstffActHJKMNPQRSTUV"
	testActorKey = "vtx.identity." + testActorID
	testCapKey   = "cap.identity." + testActorID

	testConsumerActorID  = "CnsmrActHJKMNPQRSTUV"
	testConsumerActorKey = "vtx.identity." + testConsumerActorID
	testConsumerCapKey   = "cap.identity." + testConsumerActorID
)

// runCmd executes cmd with args, capturing stdout. Returns stdout and the
// command error.
func runCmd(t *testing.T, cmd *cobra.Command, args []string) (string, error) {
	t.Helper()
	cmd.SetArgs(args)

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	cmdErr := cmd.Execute()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), cmdErr
}

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

// TestIdentityClaim_CLI_HappyPath drives the actual `identity create-unclaimed`
// and `identity claim` cobra commands end to end against a real, package-
// installed ClaimIdentity script. Regression coverage for the claim
// subcommand never declaring ContextHint.Reads: without it, state[] hydrates
// none of the target identity's key/.state/.claimKey aspects, and the
// script's own absence checks reject every claim — valid secret or not —
// with the same generic ClaimKeyInvalid code a real anti-enumeration
// rejection uses, so a broken CLI and a broken claim were indistinguishable
// from the caller's side.
func TestIdentityClaim_CLI_HappyPath(t *testing.T) {
	ctx, conn, cp, cons := setupIdentityEnv(t)
	natsURL := conn.NATS().ConnectedUrl()

	doneC := make(chan processor.MessageOutcome, 4)
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

	outputFmt := "json"
	defaultActor := ""

	createOut, err := runCmd(t, NewCommand(&natsURL, &outputFmt, &defaultActor), []string{
		"create-unclaimed",
		"--actor", testActorKey,
		"--payload", `{"name":"CLI Regression Probe","email":"cli-regression-probe@example.invalid"}`,
	})
	if err != nil {
		t.Fatalf("create-unclaimed: %v (output: %s)", err, createOut)
	}
	select {
	case <-doneC:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for create-unclaimed to process")
	}

	var created struct {
		PrimaryKey string `json:"primaryKey"`
		ClaimKey   string `json:"claimKey"`
	}
	var createEnv struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(createOut), &createEnv); err != nil {
		t.Fatalf("unmarshal create-unclaimed envelope: %v (output: %s)", err, createOut)
	}
	if !createEnv.OK {
		t.Fatalf("create-unclaimed rejected: %s", createOut)
	}
	if err := json.Unmarshal(createEnv.Data, &created); err != nil {
		t.Fatalf("unmarshal create-unclaimed data: %v (output: %s)", err, createOut)
	}
	if created.PrimaryKey == "" || created.ClaimKey == "" {
		t.Fatalf("create-unclaimed output missing primaryKey/claimKey: %s", createOut)
	}

	claimPayload, err := json.Marshal(map[string]string{
		"claimKey":          created.ClaimKey,
		"targetIdentityKey": created.PrimaryKey,
	})
	if err != nil {
		t.Fatalf("marshal claim payload: %v", err)
	}

	// A claim by an unprovisioned credential is refused, and refused
	// generically: the CLI reaches the Processor without the Gateway's
	// first-touch pre-flight, so the credential actor has no vertex, and the
	// boundTo edge the claim would emit would name an endpoint that does not
	// exist. This is the exact rejection `provision` exists to prevent, so it
	// is asserted before the happy path rather than in a sibling test — the
	// two halves are one story.
	if unprovisionedOut, err := runCmd(t, NewCommand(&natsURL, &outputFmt, &defaultActor), []string{
		"claim",
		"--actor", testConsumerActorKey,
		"--payload", string(claimPayload),
	}); err == nil {
		t.Fatalf("claim by an unprovisioned credential succeeded, want rejection (output: %s)", unprovisionedOut)
	}
	select {
	case <-doneC:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the unprovisioned claim to process")
	}

	// The credential actor must exist before it can claim. The Gateway runs
	// this pre-flight for every actor it authenticates; a CLI credential
	// reaches the Processor without one, which is what `provision` is for.
	provisionOut, err := runCmd(t, NewCommand(&natsURL, &outputFmt, &defaultActor), []string{
		"provision",
		"--actor", testActorKey,
		"--target-actor", testConsumerActorKey,
	})
	if err != nil {
		t.Fatalf("provision: %v (output: %s)", err, provisionOut)
	}
	select {
	case <-doneC:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for provision to process")
	}

	claimOut, err := runCmd(t, NewCommand(&natsURL, &outputFmt, &defaultActor), []string{
		"claim",
		"--actor", testConsumerActorKey,
		"--payload", string(claimPayload),
	})
	if err != nil {
		t.Fatalf("claim: %v (output: %s)", err, claimOut)
	}
	select {
	case <-doneC:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for claim to process")
	}

	var claimEnv struct {
		OK   bool                     `json:"ok"`
		Data processor.OperationReply `json:"data"`
	}
	if err := json.Unmarshal([]byte(claimOut), &claimEnv); err != nil {
		t.Fatalf("unmarshal claim envelope: %v (output: %s)", err, claimOut)
	}
	if !claimEnv.OK || claimEnv.Data.Status != processor.ReplyStatusAccepted {
		t.Fatalf("claim not accepted (output: %s)", claimOut)
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
			// The same operator authority the CLI credential carries for
			// create-unclaimed. `identity provision` mirrors the Gateway's
			// first-touch pre-flight, and minting an identity vertex is
			// privileged for the reason ClaimIdentity refuses to do it itself.
			{OperationType: "ProvisionConsumerIdentity", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	})
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    testConsumerCapKey,
		Actor:                  testConsumerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{testConsumerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	})

	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        "identity-cmd-test",
		Instance:       "identity-cmd",
		FilterSubjects: []string{"ops.default"},
	})
	return ctx, conn, cp, cons
}
