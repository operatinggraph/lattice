//go:build ignore

// verify-real-actor-write-auth.go — assertion tool for `make test-real-actor-auth`.
//
// real-actor-write-auth-e2e-design.md Phase 1 item 4: the core proof that
// scoped capability write-auth works end-to-end through the real Gateway,
// against the real Processor under LATTICE_AUTH_MODE=capability, with real
// projected cap.roles docs — not the stub. Requires `make up-full-capability`
// already running (NATS_URL reachable, Gateway on :8080 in GATEWAY_DEV_MODE,
// Processor under capability auth).
//
// Seeds its own fresh staff identity (CreateUnclaimedIdentity + AssignRole
// operator, mirroring `make dev-seed-staff`) and a fresh unit+listing, all
// submitted directly over NATS as the bootstrap actor (setup, not part of the
// proof). Mints RS256 dev tokens in-process (the same checked-in dev key
// `gateway dev-token` uses) for the staff subject and for a brand-new,
// never-before-seen consumer subject, then drives three real HTTP calls
// through the Gateway's POST /v1/operations:
//
//  1. staff  SetListingStatus        → allowed  (operator grant)
//  2. consumer SetListingStatus      → DENIED   (AuthDenied — the scoped deny)
//  3. consumer CreateLeaseApplication → allowed (consumer scope=self grant,
//     authContext.target == the consumer's own actor key)
//
// Also asserts the claim-flow ProvisionConsumerIdentity pre-flight: the
// consumer's identity does not exist before call 2 (its first authenticated
// touch), exists after with .state=claimed, and a second touch (call 3)
// leaves its creation revision unchanged (idempotent re-provisioning).
//
// Exit 0: all assertions pass. Exit 1: one or more assertions failed.
//
// Run via: make test-real-actor-auth (== go run ./scripts/verify-real-actor-write-auth.go)
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

// mustAccepted fatals with the reply's error code/message (not a raw pointer
// dump — Go's fmt does not dereference nested struct-pointer fields) unless
// reply is accepted.
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
	roleOperatorKey := bootstrap.RoleOperatorKey

	// --- Setup (bootstrap actor; not part of the proof) ---------------------

	staffKey := seedStaff(ctx, conn, adminKey, roleOperatorKey)
	staffID := staffKey[len("vtx.identity."):]
	waitForRoleGrant(ctx, conn, staffKey, "SetListingStatus")

	unitKey := seedListing(ctx, conn, adminKey)

	consumerID, err := substrate.NewNanoID()
	must(err, "generate consumer NanoID")
	consumerKey := "vtx.identity." + consumerID

	// --- Mint tokens ---------------------------------------------------------

	staffToken := mintDevToken(devKey, staffID)
	consumerToken := mintDevToken(devKey, consumerID)

	// --- Pre-condition: the consumer identity does not exist yet ------------

	client := &http.Client{Timeout: 10 * time.Second}

	if _, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, consumerKey); errors.Is(err, substrate.ErrKeyNotFound) {
		ok("consumer identity %s does not exist before first touch", consumerKey)
	} else if err != nil {
		fail("unexpected error checking consumer identity pre-condition: %v", err)
	} else {
		fail("consumer identity %s already exists before first touch (NanoID collision or stale state)", consumerKey)
	}

	// --- 1. staff SetListingStatus -> allowed -------------------------------

	staffReply := submitViaGateway(ctx, client, gatewayURL, staffToken, gatewayOpRequest{
		OperationType: "SetListingStatus",
		Payload:       map[string]any{"unit": unitKey, "status": "leased"},
		ContextHint:   &contextHint{Reads: []string{unitKey}},
	})
	assertAccepted(staffReply, "staff SetListingStatus")

	// --- 2. consumer SetListingStatus -> DENIED (the real, scoped deny) -----
	// Also the consumer's first authenticated touch: proves the
	// ProvisionConsumerIdentity pre-flight fires regardless of whether the
	// caller's OWN op is ultimately allowed or denied.

	consumerDenyReply := submitViaGateway(ctx, client, gatewayURL, consumerToken, gatewayOpRequest{
		OperationType: "SetListingStatus",
		Payload:       map[string]any{"unit": unitKey, "status": "available"},
		ContextHint:   &contextHint{Reads: []string{unitKey}},
	})
	assertDenied(consumerDenyReply, "consumer SetListingStatus")

	// --- Post-condition: first touch auto-provisioned the consumer ---------

	firstRev := waitForConsumerProvisioned(ctx, conn, consumerKey)
	waitForRoleGrant(ctx, conn, consumerKey, "CreateLeaseApplication")

	// --- 3. consumer CreateLeaseApplication -> allowed (scoped allow) -------

	consumerAllowReply := submitViaGateway(ctx, client, gatewayURL, consumerToken, gatewayOpRequest{
		OperationType: "CreateLeaseApplication",
		Class:         "leaseapp",
		Payload:       map[string]any{"applicant": consumerKey, "unit": unitKey},
		ContextHint:   &contextHint{Reads: []string{consumerKey, unitKey}},
		AuthContext:   &processor.AuthContext{Target: consumerKey},
	})
	assertAccepted(consumerAllowReply, "consumer CreateLeaseApplication (self)")

	// --- Idempotent re-provisioning: the SECOND touch didn't re-create -----

	secondEntry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, consumerKey)
	must(err, "re-read consumer identity after second touch")
	if secondEntry.Revision == firstRev {
		ok("consumer identity revision unchanged across second touch (idempotent provisioning, rev=%d)", secondEntry.Revision)
	} else {
		fail("consumer identity revision changed across second touch: %d -> %d (re-provisioned, not idempotent)", firstRev, secondEntry.Revision)
	}

	fmt.Printf("\n%d OK, %d FAIL\n", okCount, failCount)
	if failCount > 0 {
		os.Exit(1)
	}
}

// seedStaff creates a fresh staff identity + AssignRole(operator), mirroring
// `make dev-seed-staff` (design §3.3) — the allow side of the proof. The
// identity's own vtx.identity.<NanoID> is minted internally by the script
// (nanoid.new()), so the caller cannot choose it — the returned primaryKey
// is the real staff key.
//
// The salt suffixes both name and email: identity-domain's name-based dedup
// index (dedup-over-encrypted-pii-design.md) rejects a second live create
// under an already-indexed exact name, and this script does not declare the
// name-index optionalRead — a repeat run against a persistent dev stack
// needs a fresh name, not just a fresh email.
func seedStaff(ctx context.Context, conn *substrate.Conn, adminKey, roleOperatorKey string) string {
	salt, err := substrate.NewNanoID()
	must(err, "generate staff email salt")
	claimSum := mustSHA256Hex("real-actor-e2e-staff-" + salt)
	idReply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity", map[string]any{
		"name": "Real-Actor E2E Staff " + salt[:8], "email": "staff-" + salt[:8] + "@dev.lattice.local", "claimKeyHash": claimSum,
	}, nil)
	mustAccepted(idReply, "seed staff identity")
	staffKey := idReply.PrimaryKey
	roleReply := submitOp(ctx, conn, adminKey, "AssignRole", "", map[string]any{
		"actorKey": staffKey, "roleKey": roleOperatorKey,
	}, &processor.ContextHint{Reads: []string{staffKey, roleOperatorKey}})
	mustAccepted(roleReply, "assign operator to staff")
	ok("seeded staff identity %s holding operator", staffKey)
	return staffKey
}

// seedListing mints a fresh location-domain unit + loftspace-domain listing
// (available), so SetListingStatus has a real listing to transition.
func seedListing(ctx context.Context, conn *substrate.Conn, adminKey string) (unitKey string) {
	locReply := submitOp(ctx, conn, adminKey, "CreateLocation", "location", map[string]any{"locationType": "unit"}, nil)
	mustAccepted(locReply, "seed unit")
	unitKey = locReply.PrimaryKey

	addrReply := submitOp(ctx, conn, adminKey, "SetUnitAddress", "loftspaceListing", map[string]any{
		"unit": unitKey, "line1": "1 Real Actor Way", "city": "Springfield", "region": "OR", "postal": "97477",
	}, &processor.ContextHint{Reads: []string{unitKey}})
	mustAccepted(addrReply, "seed unit address")

	listingReply := submitOp(ctx, conn, adminKey, "SetListing", "loftspaceListing", map[string]any{
		"unit": unitKey, "rentAmount": 2500, "rentCurrency": "USD", "bedrooms": 2,
		"availableFrom": "2026-09-01T00:00:00Z", "leaseTermMonths": 12, "status": "available",
	}, &processor.ContextHint{Reads: []string{unitKey}})
	mustAccepted(listingReply, "seed listing")
	ok("seeded unit %s with an available listing", unitKey)
	return unitKey
}

// submitOp submits an operation as actorKey over NATS (the bootstrap-actor
// setup path, not the Gateway) and fatals on a transport error (a REJECTED
// reply is returned to the caller to inspect — setup ops are expected to
// succeed, but a rejection reason is more useful surfaced by the caller).
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
// does (same key, same kid) — one token that satisfies the Gateway's dev-mode
// trust root.
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

// contextHint mirrors the Gateway's operationRequestContext wire shape.
type contextHint struct {
	Reads []string `json:"reads,omitempty"`
}

// gatewayOpRequest mirrors the Gateway's POST /v1/operations body (there is
// deliberately no actor field — the Gateway stamps the verified actor).
type gatewayOpRequest struct {
	OperationType string                 `json:"operationType"`
	Class         string                 `json:"class,omitempty"`
	Payload       map[string]any         `json:"payload,omitempty"`
	ContextHint   *contextHint           `json:"contextHint,omitempty"`
	AuthContext   *processor.AuthContext `json:"authContext,omitempty"`
}

// gatewayResult pairs the HTTP status with the decoded OperationReply body.
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

// waitForConsumerProvisioned polls for the consumer identity the Gateway's
// ProvisionConsumerIdentity pre-flight creates asynchronously (best-effort,
// fire-and-forget per gateway.go's provisionActorIfNeeded) and returns its
// revision for the later idempotency check.
func waitForConsumerProvisioned(ctx context.Context, conn *substrate.Conn, consumerKey string) uint64 {
	deadline := time.Now().Add(5 * time.Second)
	for {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, consumerKey)
		if err == nil {
			ok("consumer identity %s auto-provisioned on first touch (rev=%d)", consumerKey, entry.Revision)
			return entry.Revision
		}
		if !errors.Is(err, substrate.ErrKeyNotFound) {
			fmt.Fprintf(os.Stderr, "FATAL poll consumer identity: %v\n", err)
			os.Exit(1)
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "FATAL consumer identity %s never provisioned within 5s\n", consumerKey)
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}
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
