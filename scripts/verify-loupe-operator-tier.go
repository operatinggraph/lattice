//go:build ignore

// verify-loupe-operator-tier.go — assertion tool for `make test-loupe-operator-tier`.
//
// loupe-operator-auth-lift-design.md §7 item 6: the operator-tier analog of
// the verticals' allow/deny proof (verify-real-actor-write-auth.go). Requires
// `make up-full-capability` already running (NATS_URL reachable, Gateway on
// :8080 in GATEWAY_DEV_MODE, Processor under capability auth).
//
// Seeds its own fresh consoleOperator identity (CreateUnclaimedIdentity +
// AssignRole consoleOperator, mirroring `make dev-seed-console-operator`) and
// a disposable throwaway identity to revoke, all submitted directly over NATS
// as the bootstrap actor (setup, not part of the proof). Mints an RS256 dev
// token in-process for the consoleOperator subject, then drives two real HTTP
// calls through the Gateway's POST /v1/operations:
//
//  1. RevokeActor{actor: throwaway}  → allowed  (default-lane, consoleOperator grant)
//  2. InstallPackage                 → DENIED   (AuthDenied — meta-lane stays anchor-only;
//     mechanism B's core safety property, packages/console-operator's own
//     TestPackage_NoPrivilegedLaneOpsGranted pins the grant side of this,
//     this script pins the Processor's enforcement side)
//
// Does NOT re-test the control plane (lattice.ctrl.>) — that surface was
// already lifted and proven by control-plane-capability-authz-design.md
// Fire 1a-2 (CLOSED); this script only proves the two NEW surfaces items 5-6
// add. Does NOT exercise Loupe's own /api/packages/* HTTP gate
// (cmd/loupe/pkg.go's requireRootAdmin) — that is covered directly by
// cmd/loupe/pkg_test.go, since it needs no live Gateway/Processor.
//
// Exit 0: all assertions pass. Exit 1: one or more assertions failed.
//
// Run via: make test-loupe-operator-tier (== go run ./scripts/verify-loupe-operator-tier.go)
package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/capabilitykv"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/pkgmgr"
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
	gatewayURL := envOrDefault("GATEWAY_URL", "http://127.0.0.1:8080")
	bootstrapPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	devKeyPath := envOrDefault("GATEWAY_DEV_PRIVATE_KEY_PATH", "deploy/gateway-dev-key/dev-private.pem")

	must(bootstrap.Load(bootstrapPath), "load bootstrap JSON")

	conn, err := output.Connect(ctx, natsURL)
	must(err, "connect to NATS")
	defer conn.Close()

	devKey, err := auth.LoadDevSigningKey(devKeyPath)
	must(err, "load dev signing key")

	adminKey := bootstrap.BootstrapIdentityKey
	consoleOperatorRoleKey := "vtx.role." + pkgmgr.RoleID("console-operator", "consoleOperator")

	// --- Setup (bootstrap actor; not part of the proof) ---------------------

	operatorKey := seedConsoleOperator(ctx, conn, adminKey, consoleOperatorRoleKey)
	operatorID := operatorKey[len("vtx.identity."):]
	waitForRoleGrant(ctx, conn, operatorKey, "RevokeActor")

	throwawayKey := seedThrowawayIdentity(ctx, conn, adminKey)

	operatorToken := mintDevToken(devKey, operatorID)

	client := &http.Client{Timeout: 10 * time.Second}

	// --- 1. consoleOperator RevokeActor -> allowed (default-lane grant) -----

	revokeReply := submitViaGateway(ctx, client, gatewayURL, operatorToken, gatewayOpRequest{
		OperationType: "RevokeActor",
		Payload:       map[string]any{"actor": throwawayKey, "reason": "loupe-operator-tier-e2e throwaway"},
	})
	assertAccepted(revokeReply, "consoleOperator RevokeActor (default-lane)")

	// --- 2. consoleOperator InstallPackage -> DENIED (meta-lane stays root) -

	installReply := submitViaGateway(ctx, client, gatewayURL, operatorToken, gatewayOpRequest{
		OperationType: "InstallPackage",
		Payload:       map[string]any{},
	})
	assertDenied(installReply, "consoleOperator InstallPackage (meta-lane)")

	fmt.Printf("\n%d OK, %d FAIL\n", okCount, failCount)
	if failCount > 0 {
		os.Exit(1)
	}
}

// seedConsoleOperator creates a fresh identity + AssignRole(consoleOperator),
// mirroring `make dev-seed-console-operator` (loupe-operator-auth-lift-
// design.md mechanism B) — the non-root operator identity the proof needs.
func seedConsoleOperator(ctx context.Context, conn *substrate.Conn, adminKey, consoleOperatorRoleKey string) string {
	salt, err := substrate.NewNanoID()
	must(err, "generate operator email salt")
	claimSum := mustSHA256Hex("loupe-operator-tier-e2e-" + salt)
	idReply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity", map[string]any{
		"name": "Loupe Operator-Tier E2E", "email": "loupe-op-e2e-" + salt[:8] + "@dev.lattice.local", "claimKeyHash": claimSum,
	}, nil)
	mustAccepted(idReply, "seed console-operator identity")
	operatorKey := idReply.PrimaryKey
	roleReply := submitOp(ctx, conn, adminKey, "AssignRole", "", map[string]any{
		"actorKey": operatorKey, "roleKey": consoleOperatorRoleKey,
	}, &processor.ContextHint{Reads: []string{operatorKey, consoleOperatorRoleKey}})
	mustAccepted(roleReply, "assign consoleOperator")
	ok("seeded console-operator identity %s holding consoleOperator (NOT root)", operatorKey)
	return operatorKey
}

// seedThrowawayIdentity creates a disposable identity with no purpose beyond
// being the RevokeActor target — revoking it touches nothing else on the
// shared dev stack.
func seedThrowawayIdentity(ctx context.Context, conn *substrate.Conn, adminKey string) string {
	salt, err := substrate.NewNanoID()
	must(err, "generate throwaway email salt")
	claimSum := mustSHA256Hex("loupe-operator-tier-e2e-throwaway-" + salt)
	idReply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity", map[string]any{
		"name": "Loupe Operator-Tier E2E Throwaway", "email": "loupe-op-e2e-throwaway-" + salt[:8] + "@dev.lattice.local", "claimKeyHash": claimSum,
	}, nil)
	mustAccepted(idReply, "seed throwaway identity")
	ok("seeded disposable throwaway identity %s (RevokeActor target)", idReply.PrimaryKey)
	return idReply.PrimaryKey
}

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

type contextHint struct {
	Reads []string `json:"reads,omitempty"`
}

type gatewayOpRequest struct {
	OperationType string                 `json:"operationType"`
	Class         string                 `json:"class,omitempty"`
	Payload       map[string]any         `json:"payload,omitempty"`
	ContextHint   *contextHint           `json:"contextHint,omitempty"`
	AuthContext   *processor.AuthContext `json:"authContext,omitempty"`
}

type gatewayResult struct {
	httpStatus int
	reply      processor.OperationReply
}

func submitViaGateway(ctx context.Context, client *http.Client, gatewayURL, bearerToken string, req gatewayOpRequest) gatewayResult {
	body, err := json.Marshal(req)
	must(err, "marshal gateway request")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/operations", bytes.NewReader(body))
	must(err, "build gateway request")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := client.Do(httpReq)
	must(err, "call gateway "+req.OperationType)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	must(err, "read gateway response")

	var reply processor.OperationReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL decode gateway response for %s (status %d): %v\nbody: %s\n", req.OperationType, resp.StatusCode, err, raw)
		os.Exit(1)
	}
	return gatewayResult{httpStatus: resp.StatusCode, reply: reply}
}

func assertAccepted(r gatewayResult, label string) {
	if r.httpStatus == http.StatusOK && r.reply.Status == processor.ReplyStatusAccepted {
		ok("%s: accepted (HTTP %d)", label, r.httpStatus)
		return
	}
	errCode := ""
	if r.reply.Error != nil {
		errCode = string(r.reply.Error.Code)
	}
	fail("%s: want accepted, got HTTP %d status=%s error=%s", label, r.httpStatus, r.reply.Status, errCode)
}

func assertDenied(r gatewayResult, label string) {
	if r.httpStatus != http.StatusForbidden {
		fail("%s: want HTTP 403 (Forbidden), got HTTP %d", label, r.httpStatus)
		return
	}
	if r.reply.Status != processor.ReplyStatusRejected || r.reply.Error == nil {
		fail("%s: want a rejected reply with an error code, got status=%s", label, r.reply.Status)
		return
	}
	if r.reply.Error.Code != processor.ErrCodeAuthDenied {
		fail("%s: want the SCOPED AuthDenied (not %s) — a green run here would prove nothing", label, r.reply.Error.Code)
		return
	}
	ok("%s: denied with the scoped AuthDenied (HTTP 403)", label)
}

// waitForRoleGrant polls actorKey's cap.roles.<actor> projection (Refractor's
// capabilityRoles lens re-projects asynchronously after a holdsRole change,
// per Contract #6 — there is no synchronous "projection done" signal) until
// it carries operationType, so the immediately-following Gateway call doesn't
// race a projection that hasn't caught up with the AssignRole this script
// just submitted.
func waitForRoleGrant(ctx context.Context, conn *substrate.Conn, actorKey, operationType string) {
	rolesKey, err := capabilitykv.RolesKeyFromActor(actorKey)
	must(err, "derive roles key")
	deadline := time.Now().Add(5 * time.Second)
	for {
		entry, err := conn.KVGet(ctx, bootstrap.CapabilityKVBucket, rolesKey)
		if err == nil {
			doc, perr := capabilitykv.ParseCapabilityDoc(entry.Value)
			must(perr, "parse "+rolesKey)
			for _, p := range doc.PlatformPermissions {
				if p.OperationType == operationType {
					ok("%s: %s projected into %s (rev=%d)", actorKey, operationType, rolesKey, entry.Revision)
					return
				}
			}
		} else if !errors.Is(err, substrate.ErrKeyNotFound) {
			fmt.Fprintf(os.Stderr, "FATAL poll %s: %v\n", rolesKey, err)
			os.Exit(1)
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "FATAL %s never appeared in %s within 5s\n", operationType, rolesKey)
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func mustSHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
