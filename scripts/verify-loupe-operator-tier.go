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
// token in-process for the consoleOperator subject, then drives four real
// HTTP calls through the Gateway's POST /v1/operations
// (scoped-privileged-lane-grants-design.md §7 item 3 / §9 e2e triad):
//
//  1. RevokeActor{actor: throwaway}  → allowed  (default-lane, consoleOperator grant)
//  2. InstallPackage @ meta lane     → auth ALLOWED (not AuthDenied/LaneUnauthorized —
//     the pkg-lifecycle trio is now allowlisted at meta for consoleOperator, mechanism C;
//     an empty payload still fails later validation, so this only proves step-3 passed)
//  3. InstallPackage @ default lane  → LaneUnauthorized (the grant's own Lanes:["meta"]
//     is the authority — it does NOT also fall through to the doc-level default)
//  4. CreateMetaVertex @ meta lane   → AuthDenied (never granted — the allowlist bounds
//     WHICH ops may ride meta, it doesn't widen consoleOperator's op set)
//
// Does NOT re-test the control plane (lattice.ctrl.>) — that surface was
// already lifted and proven by control-plane-capability-authz-design.md
// Fire 1a-2 (CLOSED); this script only proves the NEW surfaces items 5-6 (B)
// and item 3 (C, this fire) add. Does NOT exercise Loupe's own
// /api/packages/* HTTP handlers — mechanism C retired their root-admin front
// gate (cmd/loupe/pkg.go), so authorization for that surface is entirely the
// Processor-side proof this script drives.
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

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
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

	// --- 2. consoleOperator InstallPackage@meta -> auth ALLOWED (mechanism C) -

	installMetaReply := submitViaGateway(ctx, client, gatewayURL, operatorToken, gatewayOpRequest{
		OperationType: "InstallPackage",
		Lane:          "meta",
		Payload:       map[string]any{},
	})
	assertAuthPassed(installMetaReply, "consoleOperator InstallPackage@meta (allowlisted, mechanism C)")

	// --- 3. consoleOperator InstallPackage@default -> LaneUnauthorized ------

	installDefaultReply := submitViaGateway(ctx, client, gatewayURL, operatorToken, gatewayOpRequest{
		OperationType: "InstallPackage",
		Payload:       map[string]any{},
	})
	assertLaneUnauthorized(installDefaultReply, "consoleOperator InstallPackage@default (grant is meta-only)")

	// --- 4. consoleOperator CreateMetaVertex@meta -> DENIED (never granted) -

	createMetaReply := submitViaGateway(ctx, client, gatewayURL, operatorToken, gatewayOpRequest{
		OperationType: "CreateMetaVertex",
		Lane:          "meta",
		Payload:       map[string]any{},
	})
	assertDenied(createMetaReply, "consoleOperator CreateMetaVertex@meta (ungranted)")

	fmt.Printf("\n%d OK, %d FAIL\n", okCount, failCount)
	if failCount > 0 {
		os.Exit(1)
	}
}

// seedConsoleOperator creates a fresh identity + AssignRole(consoleOperator),
// mirroring `make dev-seed-console-operator` (loupe-operator-auth-lift-
// design.md mechanism B) — the non-root operator identity the proof needs.
// The salt suffixes both name and email: identity-domain's name-based dedup
// index (dedup-over-encrypted-pii-design.md) rejects a second live create
// under an already-indexed exact name, and this script does not declare the
// name-index optionalRead — a repeat run against a persistent dev stack
// needs a fresh name, not just a fresh email.
func seedConsoleOperator(ctx context.Context, conn *substrate.Conn, adminKey, consoleOperatorRoleKey string) string {
	salt, err := substrate.NewNanoID()
	must(err, "generate operator email salt")
	claimSum := mustSHA256Hex("loupe-operator-tier-e2e-" + salt)
	idReply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity", map[string]any{
		"name": "Loupe Operator-Tier E2E " + salt[:8], "email": "loupe-op-e2e-" + salt[:8] + "@dev.lattice.local", "claimKeyHash": claimSum,
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
		"name": "Loupe Operator-Tier E2E Throwaway " + salt[:8], "email": "loupe-op-e2e-throwaway-" + salt[:8] + "@dev.lattice.local", "claimKeyHash": claimSum,
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
	Lane          string                 `json:"lane,omitempty"`
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

// assertLaneUnauthorized proves a specific denial code — LaneUnauthorized —
// distinct from AuthDenied: the op IS granted, just not at the lane
// submitted (scoped-privileged-lane-grants-design.md §3.2's per-matched-
// permission gate).
func assertLaneUnauthorized(r gatewayResult, label string) {
	if r.httpStatus != http.StatusForbidden {
		fail("%s: want HTTP 403 (Forbidden), got HTTP %d", label, r.httpStatus)
		return
	}
	if r.reply.Status != processor.ReplyStatusRejected || r.reply.Error == nil {
		fail("%s: want a rejected reply with an error code, got status=%s", label, r.reply.Status)
		return
	}
	if r.reply.Error.Code != processor.ErrCodeLaneUnauthorized {
		fail("%s: want LaneUnauthorized (not %s) — a green run here would prove nothing", label, r.reply.Error.Code)
		return
	}
	ok("%s: denied with LaneUnauthorized (HTTP 403)", label)
}

// assertAuthPassed proves step-3 authorized the op WITHOUT asserting the
// whole operation succeeded — InstallPackage with an empty payload still
// fails later validation, so the only claim this script can make live
// (without fabricating a real manifest) is "this did not fail on
// AuthDenied or LaneUnauthorized", i.e. the allowlisted meta-lane grant was
// honored and the request proceeded past step 3.
func assertAuthPassed(r gatewayResult, label string) {
	if r.reply.Status == processor.ReplyStatusAccepted {
		ok("%s: accepted outright (HTTP %d)", label, r.httpStatus)
		return
	}
	if r.reply.Error != nil && (r.reply.Error.Code == processor.ErrCodeAuthDenied || r.reply.Error.Code == processor.ErrCodeLaneUnauthorized) {
		fail("%s: auth-layer denial %s — the allowlisted meta-lane grant was not honored", label, r.reply.Error.Code)
		return
	}
	errCode := ""
	if r.reply.Error != nil {
		errCode = string(r.reply.Error.Code)
	}
	ok("%s: passed step-3 auth (later-stage code=%s, HTTP %d — expected past an empty payload)", label, errCode, r.httpStatus)
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
