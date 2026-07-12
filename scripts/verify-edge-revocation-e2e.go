//go:build ignore

// verify-edge-revocation-e2e.go — per-identity-nats-subscribe-acl-design.md
// Fire 3, design §8 vector 4 ("Revocation") proved against the LIVE dev
// stack's real production wiring: a RevokeActor op -> the Gateway's own
// outbox-driven revocation materializer -> the token-revocation KV bucket ->
// the real cmd/gateway auth-callout responder (internal/gateway/auth.
// Authenticator + internal/gateway/revocation.Checker). internal/natsperm's
// TestAuthCallout_Revocation already proves the mechanism deterministically
// against an embedded server + a fakeRevocationChecker; this script is the
// "real wiring, real stack" half the design's §10 Fire-3 decomposition names
// ("Revocation e2e — vector 4 end-to-end against the live dev stack").
//
// Requires the shared dev stack up (make up-full) with cmd/gateway running
// (GATEWAY_DEV_MODE=true so the checked-in dev key verifies the minted
// token) — the auth-callout responder is wired unconditionally since Fire 1.
//
// Proof:
//  1. mint a fresh EDGE_TOKEN (dev-signed, nanoid binding) for a
//     never-before-seen identity NanoID
//  2. connect to NATS as that edge BEFORE revocation -> want: success
//  3. submit RevokeActor{actor} as the bootstrap actor over core-operations
//     (identity-domain's actorRevocation DDL — already installed)
//  4. poll token-revocation until the Gateway's materializer folds it
//  5. connect to NATS as that SAME edge again -> want: denial (the live
//     responder's real Authenticator now sees the revoked actor)
//
// Exit 0: all assertions pass. Exit 1: one or more assertions failed.
//
// Run via: make test-edge-revocation-e2e (== go run ./scripts/verify-edge-revocation-e2e.go)
package main

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/gateway/revocation"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

var (
	okCount   int
	failCount int
)

func ok(format string, args ...any) {
	okCount++
	fmt.Printf("OK   "+format+"\n", args...)
}

func fail(format string, args ...any) {
	failCount++
	fmt.Printf("FAIL "+format+"\n", args...)
}

func must(err error, context string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL %s: %v\n", context, err)
		os.Exit(1)
	}
}

func mustAccepted(reply *processor.OperationReply, context string) {
	if reply.Status == processor.ReplyStatusAccepted {
		return
	}
	if reply.Error != nil {
		fmt.Fprintf(os.Stderr, "FATAL %s: rejected code=%s message=%s\n", context, reply.Error.Code, reply.Error.Message)
	} else {
		fmt.Fprintf(os.Stderr, "FATAL %s: status=%s (no error detail)\n", context, reply.Status)
	}
	os.Exit(1)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	natsURL := envOrDefault("NATS_URL", "nats://localhost:4222")
	bootstrapPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	devKeyPath := envOrDefault("GATEWAY_DEV_PRIVATE_KEY_PATH", "deploy/gateway-dev-key/dev-private.pem")

	must(bootstrap.Load(bootstrapPath), "load bootstrap JSON")

	conn, err := output.Connect(ctx, natsURL)
	must(err, "connect to NATS as lattice-cli")
	defer conn.Close()

	devKey, err := auth.LoadDevSigningKey(devKeyPath)
	must(err, "load dev signing key")

	adminKey := bootstrap.BootstrapIdentityKey

	identityID, err := substrate.NewNanoID()
	must(err, "generate edge identity NanoID")
	identityKey := auth.IdentityKeyPrefix + identityID
	token := mintDevToken(devKey, identityID)

	// --- 1. pre-revocation connect: want success -----------------------

	preNC, err := connectEdge(ctx, natsURL, token, identityID)
	if err != nil {
		fail("pre-revocation edge connect (identity %s): want success, got %v", identityID, err)
	} else {
		ok("pre-revocation edge connect (identity %s) succeeded", identityID)
		preNC.Close()
	}

	// --- 2. revoke, over the real op -> outbox -> materializer chain ----

	revokeReply := submitOp(ctx, conn, adminKey, "RevokeActor", "", map[string]any{
		"actor": identityKey, "reason": "verify-edge-revocation-e2e",
	}, nil)
	mustAccepted(revokeReply, "RevokeActor")
	ok("RevokeActor accepted for %s", identityKey)

	// --- 3. wait for the Gateway's own materializer to fold it ----------

	waitForRevocationFold(ctx, conn, identityKey)

	// --- 4. post-revocation connect: want denial -------------------------

	postNC, err := connectEdge(ctx, natsURL, token, identityID)
	if err == nil {
		postNC.Close()
		fail("post-revocation edge connect (identity %s): want denial, got success", identityID)
	} else {
		ok("post-revocation edge connect (identity %s) denied: %v", identityID, err)
	}

	// --- 5. cleanup: reverse the revocation ------------------------------
	// A repeat run against a persistent dev stack must not accumulate one
	// stale token-revocation entry per invocation forever; the throwaway
	// identity was never real, so reversing it is pure hygiene, not part
	// of the proof (unchecked — a failure here doesn't flip the exit code).
	unrevokeReply := submitOp(ctx, conn, adminKey, "UnrevokeActor", "", map[string]any{
		"actor": identityKey,
	}, nil)
	if unrevokeReply.Status == processor.ReplyStatusAccepted {
		ok("UnrevokeActor accepted for %s (cleanup)", identityKey)
	} else {
		fmt.Printf("WARN cleanup UnrevokeActor for %s did not accept (status=%s) — stale revocation entry left behind\n", identityKey, unrevokeReply.Status)
	}

	fmt.Printf("\n%d OK, %d FAIL\n", okCount, failCount)
	if failCount > 0 {
		os.Exit(1)
	}
}

// connectEdge dials NATS exactly as cmd/edge does (substrate.ConnectOpts.Token
// + the per-identity InboxPrefix, per-identity-nats-subscribe-acl-design.md
// §3.3) — the untrusted Edge connect shape, delegated to the live
// auth-callout responder. A short connect timeout (the ctx deadline) is
// enough: a callout denial is a synchronous "Authorization Violation" from
// the server, not a hang.
func connectEdge(ctx context.Context, natsURL, token, identityID string) (*substrate.Conn, error) {
	return substrate.Connect(ctx, substrate.ConnectOpts{
		URL:         natsURL,
		Name:        "verify-edge-revocation-e2e-" + identityID,
		Token:       token,
		InboxPrefix: "_INBOX.edge." + identityID,
	})
}

// waitForRevocationFold polls the token-revocation bucket (the same bucket
// internal/gateway/revocation.Checker consults) for identityKey, giving the
// Gateway's asynchronous outbox-driven materializer (internal/gateway.
// StartRevocationMaterializer) time to fold the just-committed
// gateway.actorRevoked event.
func waitForRevocationFold(ctx context.Context, conn *substrate.Conn, identityKey string) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := conn.KVGet(ctx, revocation.BucketName, identityKey); err == nil {
			ok("token-revocation bucket folded %s (Gateway materializer)", identityKey)
			return
		} else if !errors.Is(err, substrate.ErrKeyNotFound) {
			fmt.Fprintf(os.Stderr, "FATAL poll token-revocation for %s: %v\n", identityKey, err)
			os.Exit(1)
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "FATAL token-revocation for %s never folded within 10s\n", identityKey)
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// submitOp submits an operation as actorKey directly over NATS (the
// bootstrap-actor setup/driver path, not the Gateway) and fatals on a
// transport error — mirrors verify-real-actor-write-auth.go's helper of the
// same name.
func submitOp(ctx context.Context, conn *substrate.Conn, actorKey, operationType, class string, payload map[string]any, hint *processor.ContextHint) *processor.OperationReply {
	reqID, err := substrate.NewNanoID()
	must(err, "generate requestId")
	payloadBytes, err := json.Marshal(payload)
	must(err, "marshal payload")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: operationType,
		Actor:         actorKey,
		Class:         class,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       payloadBytes,
		ContextHint:   hint,
	}
	reply, err := output.SubmitOp(ctx, conn, env)
	must(err, "submit "+operationType)
	return reply
}

// mintDevToken signs an RS256 JWT for sub, exactly as `gateway dev-token`
// does (same key, same kid) — the credential every dev/CI edge connect uses.
func mintDevToken(key *rsa.PrivateKey, sub string) string {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   sub,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = auth.DevKeyID
	signed, err := tok.SignedString(key)
	must(err, "sign dev token for "+sub)
	return signed
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
