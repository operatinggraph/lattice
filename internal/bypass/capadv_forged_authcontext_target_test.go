// Forged authContext.target — the outcome-level residual for the
// validated-target primitive.
//
// Attack: `authContext.target` is a client field the Gateway forwards verbatim,
// and step 3's scope=any path authorizes without ever inspecting it. An op
// script that exempts a caller from confinement on target PRESENCE therefore
// trusts a value any scope=any holder can fabricate. The platform's answer is a
// derived bit — true only where the auth path actually checked the target.
//
// The assembly this residual owns is the one no single mechanism's test covers:
// the REAL CapabilityAuthorizer reading a REAL Capability-KV doc, wired through
// the REAL commit path, delivering the bit to the script executor. The
// primitive's own colocated test drives the derivation as a function; the
// commit-path test drives the wiring against a fake authorizer; the four
// migrated packages drive their guards. Only here do all three meet, which is
// where a forger's value would have to survive.
//
//	Phase A: assert the bit directly on the wire → must not reach the executor
//	Phase B: fabricate a target under scope=any  → executor must see false
//	Control: the genuine scope=self path         → executor must see true
//
// DEFENDED when: no envelope reaching execution carries a true bit that the
// authorizer did not itself derive, and the legitimate self path still does.
package bypass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	forgedActorID  = "BBFQRGEDTARGETACTQRS"
	forgedActorKey = "vtx.identity." + forgedActorID
	forgedCapKey   = "cap.identity." + forgedActorID
	forgedVictim   = "vtx.leaseapp." + "BBFQRGEDVCTMSLEASEHJ"
)

// buildForgedTargetCapDoc gives one actor BOTH shapes: a scope=any grant (the
// forger's standing authority, which never validates a target) and a scope=self
// grant on a different operation (the genuine validated path), so one fixture
// carries the attack and its control.
func buildForgedTargetCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    forgedCapKey,
		Actor:                  forgedActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{forgedActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "VoidCharge", Scope: "any"},
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
	}
}

func setupForgedTargetHarness(t *testing.T) (context.Context, *substrate.Conn, *processor.CapabilityAuthorizer) { //nolint:unparam
	t.Helper()
	ctx, conn := setupCapAdvHarness(t)

	doc := buildForgedTargetCapDoc()
	raw, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, capadvCapBucket, doc.Key, raw); err != nil {
		t.Fatalf("seed forged-target cap doc: %v", err)
	}

	cfg := processor.DefaultCapabilityAuthorizerConfig()
	authz, err := processor.NewCapabilityAuthorizer(conn, capadvCapBucket, nil, cfg, bypassLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}
	return ctx, conn, authz
}

// TestCapAdv_ForgedAuthTargetValidated_NotAcceptedFromWire is Phase A: the
// attacker asserts the platform's own derived bit in the envelope JSON.
func TestCapAdv_ForgedAuthTargetValidated_NotAcceptedFromWire(t *testing.T) {
	raw := []byte(`{
		"requestId": "` + forgedActorID + `",
		"lane": "default",
		"operationType": "VoidCharge",
		"actor": "` + forgedActorKey + `",
		"submittedAt": "2026-07-20T12:00:00Z",
		"authTargetValidated": true,
		"authContext": {"target": "` + forgedVictim + `"},
		"payload": {}
	}`)
	env, err := processor.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.AuthTargetValidated {
		t.Fatal("EXPOSED — a client-asserted authTargetValidated landed on the envelope; " +
			"the platform's own auth verdict must not be settable from the wire")
	}
	// The target itself is still carried: the fix surfaces a verdict, it does
	// not blank a legitimately-forwarded target.
	if env.AuthContext == nil || env.AuthContext.Target != forgedVictim {
		t.Fatalf("authContext.target must survive parse; got %+v", env.AuthContext)
	}
	t.Log("Phase A: DEFENDED — wire-supplied authTargetValidated dropped at parse ✓")
}

// --- the real commit path, so the bit is observed where scripts read it ------

type forgedCapturingExecutor struct{ seen []bool }

func (e *forgedCapturingExecutor) Execute(_ context.Context, env *processor.OperationEnvelope,
	_ processor.HydratedState) (processor.ScriptResult, error) {
	e.seen = append(e.seen, env.AuthTargetValidated)
	return processor.ScriptResult{}, nil
}

type forgedNoopHydrator struct{}

func (forgedNoopHydrator) Hydrate(_ context.Context, _ *processor.OperationEnvelope) (processor.HydratedState, error) {
	return processor.HydratedState{}, nil
}

type forgedNoopValidator struct{}

func (forgedNoopValidator) Validate(_ context.Context, _ *processor.OperationEnvelope,
	_ processor.ScriptResult, _ processor.HydratedState) error {
	return nil
}

type forgedNoopCommitter struct{}

func (forgedNoopCommitter) Commit(_ context.Context, _ *processor.OperationEnvelope,
	_ processor.ScriptResult, _ processor.Tracker) (processor.CommitAck, error) {
	return processor.CommitAck{}, nil
}

// forgedConsumer creates a durable on the ops stream, named per vector so two
// vectors in one package never drain each other's message.
func forgedConsumer(t *testing.T, ctx context.Context, conn *substrate.Conn, name string) jetstream.Consumer {
	t.Helper()
	stream, err := conn.JetStream().Stream(ctx, capadvOpsStream)
	if err != nil {
		t.Fatalf("open ops stream: %v", err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:   "forged-" + name,
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	return cons
}

// driveForged publishes an envelope through the real commit path and returns the
// bit the executor saw. The wire body always ASSERTS `authTargetValidated:true`
// — the attacker's free move — so every case doubles as Phase A.
func driveForged(t *testing.T, ctx context.Context, conn *substrate.Conn,
	authz *processor.CapabilityAuthorizer, opType, class string, ac *processor.AuthContext) bool {
	t.Helper()
	exec := &forgedCapturingExecutor{}
	cp := processor.NewCommitPath(processor.Deps{
		Conn:       conn,
		CoreBucket: capadvCoreBucket,
		HealthKV:   capadvHealthBucket,
		Authorizer: authz,
		Hydrator:   forgedNoopHydrator{},
		Executor:   exec,
		Validator:  forgedNoopValidator{},
		Committer:  forgedNoopCommitter{},
		Metrics:    &processor.Metrics{},
		Logger:     bypassLogger(),
	})

	body := map[string]any{
		"requestId":           forgedActorID,
		"lane":                "default",
		"operationType":       opType,
		"actor":               forgedActorKey,
		"submittedAt":         time.Now().UTC().Format(time.RFC3339),
		"class":               class,
		"payload":             map[string]any{},
		"authTargetValidated": true, // the forgery, on every vector
	}
	if ac != nil {
		body["authContext"] = ac
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	// Publish the RAW body (not a marshalled envelope) so the forged field
	// actually rides the wire, then drive it through the real consumer path.
	cons := forgedConsumer(t, ctx, conn, opType)
	if _, err := conn.JetStream().Publish(ctx, "ops.default", raw); err != nil {
		t.Fatalf("publish: %v", err)
	}
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if len(exec.seen) != 1 {
		t.Fatalf("%s: expected exactly one execution; got %d", opType, len(exec.seen))
	}
	return exec.seen[0]
}

// TestCapAdv_ForgedTarget_ScopeAnyReachesExecutorUnvalidated is Phase B: the
// attacker fabricates a target under a standing scope=any grant AND asserts the
// bit on the wire. The op authorizes — it always did — but the script must see
// an unvalidated target, or every confinement exemption keyed on it is open.
func TestCapAdv_ForgedTarget_ScopeAnyReachesExecutorUnvalidated(t *testing.T) {
	ctx, conn, authz := setupForgedTargetHarness(t)

	if got := driveForged(t, ctx, conn, authz, "VoidCharge", "tab",
		&processor.AuthContext{Target: forgedVictim}); got {
		t.Fatal("EXPOSED — a scope=any caller who fabricated authContext.target reached the " +
			"executor with authTargetValidated=true; every guard exempting on it is bypassed")
	}
	t.Log("Phase A+B: DEFENDED — forged target + wire-asserted bit reach the executor as false ✓")
}

// TestCapAdv_ForgedTarget_GenuineSelfPathStillValidates is the control: the same
// actor's real scope=self path must reach the executor with the bit TRUE.
// Without it, Phase B could be passing because the bit is false everywhere,
// which would prove nothing about the distinction the primitive draws.
func TestCapAdv_ForgedTarget_GenuineSelfPathStillValidates(t *testing.T) {
	ctx, conn, authz := setupForgedTargetHarness(t)

	if got := driveForged(t, ctx, conn, authz, "ClaimIdentity", "identity",
		&processor.AuthContext{Target: forgedActorKey}); !got {
		t.Fatal("the genuine scope=self path must reach the executor with authTargetValidated=true; " +
			"a bit that is false everywhere would make Phase B vacuous")
	}
	t.Log("Control: DEFENDED — the validated self path reaches the executor as true ✓")
}

// TestCapAdv_ForgedTarget_SelfPathDeniesForeignTarget pins where the platform
// actually checks: scope=self denies a target that is not the actor, so a
// forger cannot reach the true branch by borrowing the self grant.
func TestCapAdv_ForgedTarget_SelfPathDeniesForeignTarget(t *testing.T) {
	ctx, _, authz := setupForgedTargetHarness(t)

	env := &processor.OperationEnvelope{
		RequestID:     forgedActorID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         forgedActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		AuthContext:   &processor.AuthContext{Target: forgedVictim},
	}
	dec, err := authz.Authorize(ctx, env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatal("EXPOSED — scope=self authorized a target that is not the actor")
	}
	t.Log("Control: DEFENDED — scope=self denies a foreign target ✓")
}
