//go:build ignore

// verify-claim-ceremony.go — assertion tool for `make test-claim-ceremony`.
//
// Board row "Nothing walks the real claim ceremony" (verticals.md, backing
// note facet-staff-worlds-design.md §13): every showcase persona is minted
// unclaimed and has its .state flipped directly via UpdateIdentityState — no
// seeded world, and no e2e proof, ever drives the REAL two-actor
// ClaimIdentity ceremony (a staff-minted identity U claimed by a separate,
// first-touch device credential D naming itself in authContext.target)
// through the real Gateway, against the real Processor under
// LATTICE_AUTH_MODE=capability. Mirrors verify-real-actor-write-auth.go's
// shape: mint an unclaimed identity as setup, mint a brand-new RS256 dev
// token for a never-before-seen device subject, then drive the ceremony as
// real HTTP calls through the Gateway's POST /v1/operations:
//
//  1. D claims U (ClaimIdentity, payload targetIdentityKey=U, AuthContext
//     .Target=D) on D's own first-ever Gateway touch -> ACCEPTED, after the
//     SAME bounded isTransientAuthLag retry cmd/facet/claim.go's handleClaim
//     already carries: D's first-touch pre-flight (ProvisionConsumerIdentity)
//     commits synchronously, but the CapabilityAuthorizer's scope=self check
//     reads Refractor's async-projected Capability Lens, so D's own
//     ClaimIdentity submission — its very next request — routinely races
//     ahead of that projection and must retry through the transient
//     AuthDenied. This proof hits that race live (unbounded retry=0 fails
//     every time against this stack) — the exact interleaving a package unit
//     test (which pre-seeds both actors' roles and never touches the
//     Gateway) cannot exercise.
//  2. A second, also-never-before-seen device D2 retries the identical
//     targetIdentityKey/claimKey (through the same retry helper, so its OWN
//     transient auth lag doesn't mask the assertion) -> DENIED, generically
//     (U is no longer unclaimed; NFR-S6 anti-enumeration collapses the
//     reason to ClaimKeyInvalid, never re-surfacing AuthDenied).
//
// Post-conditions are asserted directly against Core KV / the capability
// projection — no decrypt needed (credentialBinding is a sensitive aspect,
// out of scope here; the state transition, the tombstoned claimKey, and the
// consumer-role capability projection are all plaintext/derived):
//   - U.state: unclaimed -> claimed
//   - U.claimKey aspect tombstoned (isDeleted)
//   - U holds consumer (R2's own-target grant — InitiateCredentialLink
//     projected into U's cap.roles doc)
//   - D was auto-provisioned by the Gateway pre-flight and independently
//     holds consumer too (D's cap.roles doc)
//
// Requires `make up-full-capability` (Processor under LATTICE_AUTH_MODE=
// capability, Gateway identity-provisioner role assigned) already running
// against the shared stack. Not self-contained (like verify-real-actor-
// write-auth.go, it targets the shared stack's NATS_URL/Gateway, not an
// embedded fixture).
//
// Exit 0: all assertions pass. Exit 1: one or more assertions failed.
//
// Run via: make test-claim-ceremony (== go run ./scripts/verify-claim-ceremony.go)
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

	// --- Setup: mint a staff-minted unclaimed identity ("U") — never
	// state-flipped, so the ceremony below is the ONLY thing that claims it.

	salt, err := substrate.NewNanoID()
	must(err, "generate claim-key salt")
	claimKeyPlaintext := "claim-ceremony-e2e-" + salt
	claimKeyHash := mustSHA256Hex(claimKeyPlaintext)
	createReply := submitOp(ctx, conn, adminKey, "CreateUnclaimedIdentity", "identity", map[string]any{
		"name": "Claim Ceremony E2E " + salt[:8], "email": "claim-e2e-" + salt[:8] + "@dev.lattice.local", "claimKeyHash": claimKeyHash,
	}, nil)
	mustAccepted(createReply, "seed unclaimed identity")
	targetKey := createReply.PrimaryKey
	ok("seeded unclaimed identity %s (never state-flipped)", targetKey)

	// --- Mint a brand-new, never-before-seen device credential ("D"). ------

	deviceID, err := substrate.NewNanoID()
	must(err, "generate device NanoID")
	deviceKey := "vtx.identity." + deviceID
	deviceToken := mintDevToken(devKey, deviceID)

	client := &http.Client{Timeout: 10 * time.Second}

	if _, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, deviceKey); errors.Is(err, substrate.ErrKeyNotFound) {
		ok("device identity %s does not exist before first touch", deviceKey)
	} else if err != nil {
		fail("unexpected error checking device identity pre-condition: %v", err)
	} else {
		fail("device identity %s already exists before first touch (NanoID collision or stale state)", deviceKey)
	}

	// --- 1. D claims U, on D's own first-ever Gateway touch ---------------
	// The pre-flight (auto-provision D as consumer) and ClaimIdentity's own
	// grant to U run as two sequential Processor submissions inside this ONE
	// http request — the live interleaving no unit test exercises.

	claimReply := submitClaimWithRetry(ctx, client, gatewayURL, deviceToken, deviceKey, targetKey, claimKeyPlaintext)
	assertAccepted(claimReply, "device ClaimIdentity (first touch, self-target)")

	// --- Post-conditions: the state machine + tombstone + both grants -----

	waitForState(ctx, conn, targetKey, "claimed")
	waitForTombstoned(ctx, conn, targetKey+".claimKey")
	waitForRoleGrant(ctx, conn, targetKey, "InitiateCredentialLink")
	waitForRoleGrant(ctx, conn, deviceKey, "InitiateCredentialLink")
	ok("device %s was auto-provisioned by the pre-flight and independently holds consumer", deviceKey)

	// --- 2. A second, also-never-before-seen device retries the identical
	// claim -> DENIED generically (U is no longer unclaimed). --------------

	device2ID, err := substrate.NewNanoID()
	must(err, "generate second device NanoID")
	device2Key := "vtx.identity." + device2ID
	device2Token := mintDevToken(devKey, device2ID)

	replayReply := submitClaimWithRetry(ctx, client, gatewayURL, device2Token, device2Key, targetKey, claimKeyPlaintext)
	assertClaimKeyInvalid(replayReply, "second device re-claiming an already-claimed identity")

	fmt.Printf("\n%d OK, %d FAIL\n", okCount, failCount)
	if failCount > 0 {
		os.Exit(1)
	}
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

// claimRetryBackoffs mirrors cmd/facet/claim.go's bounded backoff for the
// fresh device credential's own capability-grant projection race (~3s
// total): the pre-flight's ProvisionConsumerIdentity commits synchronously,
// but the CapabilityAuthorizer reads an async-projected Capability Lens, so
// the device's own very-next ClaimIdentity submission routinely races ahead
// of that projection.
var claimRetryBackoffs = []time.Duration{
	200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond,
}

// isTransientAuthLag mirrors cmd/facet/claim.go's helper of the same name: a
// rejection is the known, architecturally-expected async-projection race —
// not a genuine, persistent denial — only for these two AuthDenied reasons.
func isTransientAuthLag(reply *processor.OperationReply) bool {
	if reply == nil || reply.Status != processor.ReplyStatusRejected || reply.Error == nil {
		return false
	}
	if reply.Error.Code != processor.ErrCodeAuthDenied {
		return false
	}
	reason, _ := reply.Error.Details["reason"].(string)
	return reason == "NoCapabilityEntry" || reason == "OperationNotPermitted"
}

// submitClaimWithRetry drives ClaimIdentity as deviceToken, retrying through
// the transient auth-lag race exactly as cmd/facet/claim.go's handleClaim
// does, so a genuine, settled rejection (e.g. ClaimKeyInvalid) is never
// masked by the fresh device credential's own projection race.
func submitClaimWithRetry(ctx context.Context, client *http.Client, gatewayURL, deviceToken, deviceKey, targetKey, claimKeyPlaintext string) gatewayResult {
	req := gatewayOpRequest{
		OperationType: "ClaimIdentity",
		Class:         "identity",
		Payload:       map[string]any{"claimKey": claimKeyPlaintext, "targetIdentityKey": targetKey},
		AuthContext:   &processor.AuthContext{Target: deviceKey},
		ContextHint: &contextHint{Reads: []string{
			targetKey, targetKey + ".state", targetKey + ".claimKey",
		}},
	}
	var result gatewayResult
	for attempt := 0; ; attempt++ {
		result = submitViaGateway(ctx, client, gatewayURL, deviceToken, req)
		if !isTransientAuthLag(&result.reply) || attempt >= len(claimRetryBackoffs) {
			return result
		}
		time.Sleep(claimRetryBackoffs[attempt])
	}
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

// assertClaimKeyInvalid checks for the NFR-S6 anti-enumeration generic
// rejection ClaimIdentity gives every failure mode (wrong key, already
// claimed, tombstoned, merged, flagged) — HTTP 400, ErrCodeClaimKeyInvalid.
func assertClaimKeyInvalid(r gatewayResult, label string) {
	if r.httpStatus != http.StatusBadRequest {
		fail("%s: want HTTP 400 (Bad Request), got HTTP %d", label, r.httpStatus)
		return
	}
	if r.reply.Status != processor.ReplyStatusRejected || r.reply.Error == nil {
		fail("%s: want a rejected reply with an error code, got status=%s", label, r.reply.Status)
		return
	}
	if r.reply.Error.Code != processor.ErrCodeClaimKeyInvalid {
		fail("%s: want the generic ClaimKeyInvalid (not %s) — a green run here would prove nothing", label, r.reply.Error.Code)
		return
	}
	ok("%s: denied with the generic ClaimKeyInvalid (HTTP 400)", label)
}

// aspectDoc mirrors the Core KV aspect envelope: {"data":{...},"isDeleted":bool}.
type aspectDoc struct {
	Data      map[string]any `json:"data"`
	IsDeleted bool           `json:"isDeleted"`
}

func readAspectDoc(ctx context.Context, conn *substrate.Conn, key string) (aspectDoc, error) {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
	if err != nil {
		return aspectDoc{}, err
	}
	var doc aspectDoc
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return aspectDoc{}, err
	}
	return doc, nil
}

// waitForState polls identityKey.state until its data.value equals want (the
// Processor commits asynchronously relative to the Gateway's HTTP reply on
// the 202 fallback path; on the normal synchronous-accept path this resolves
// on the first poll).
func waitForState(ctx context.Context, conn *substrate.Conn, identityKey, want string) {
	stateKey := identityKey + ".state"
	deadline := time.Now().Add(5 * time.Second)
	for {
		doc, err := readAspectDoc(ctx, conn, stateKey)
		if err == nil {
			if got, _ := doc.Data["value"].(string); got == want {
				ok("%s: state = %q", identityKey, want)
				return
			}
		} else if !errors.Is(err, substrate.ErrKeyNotFound) {
			fail("poll %s: %v", stateKey, err)
			return
		}
		if time.Now().After(deadline) {
			fail("%s never reached state=%q within 5s", identityKey, want)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitForTombstoned polls key until its aspect envelope carries isDeleted.
func waitForTombstoned(ctx context.Context, conn *substrate.Conn, key string) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		doc, err := readAspectDoc(ctx, conn, key)
		if err == nil && doc.IsDeleted {
			ok("%s: tombstoned", key)
			return
		}
		if err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
			fail("poll %s: %v", key, err)
			return
		}
		if time.Now().After(deadline) {
			fail("%s never tombstoned within 5s", key)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitForRoleGrant polls actorKey's cap.roles.<actor> projection (Refractor's
// capabilityRoles lens re-projects asynchronously after a holdsRole change,
// per Contract #6 — there is no synchronous "projection done" signal) until
// it carries operationType.
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
			fail("poll %s: %v", rolesKey, err)
			return
		}
		if time.Now().After(deadline) {
			fail("%s never appeared in %s within 5s", operationType, rolesKey)
			return
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
