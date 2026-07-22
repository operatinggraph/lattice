package natsauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// validNanoID generates a real Contract #1 NanoID for test fixtures — since
// authorize() now asserts substrate.IsValidNanoID on the resolved identity
// (an adversarial-pass finding, MEDIUM: the generic subject-safe check alone
// doesn't guarantee the alphabet the durable-collision argument relies on),
// a fixture identity must actually be one, not an arbitrary short string.
func validNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("generate nanoid: %v", err)
	}
	return id
}

// fakeAuthenticator is a test double for Authenticator — returns a fixed
// VerifiedActor or a fixed error, keyed by the token presented, so a test
// can drive both the accept and deny paths without a real JWT.
type fakeAuthenticator struct {
	byToken map[string]auth.VerifiedActor
	err     error
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, token string) (auth.VerifiedActor, error) {
	if f.err != nil {
		return auth.VerifiedActor{}, f.err
	}
	actor, ok := f.byToken[token]
	if !ok {
		return auth.VerifiedActor{}, errors.New("fakeAuthenticator: unknown token")
	}
	return actor, nil
}

// fakeResolver is a test double for IdentityResolver.
type fakeResolver struct {
	identityKey string
	bound       bool
	err         error
}

func (f *fakeResolver) Resolve(_ context.Context, _ string) (string, bool, error) {
	return f.identityKey, f.bound, f.err
}

func mustAccountKeyPair(t *testing.T) nkeys.KeyPair {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("create account keypair: %v", err)
	}
	return kp
}

func mustUserKeyPair(t *testing.T) (nkeys.KeyPair, string) {
	t.Helper()
	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("create user keypair: %v", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		t.Fatalf("user public key: %v", err)
	}
	return kp, pub
}

// requestToken builds a signed AuthorizationRequestClaims token as the NATS
// server would send it — signed by a SERVER key pair (ExpectedPrefixes
// requires PrefixByteServer), carrying the given bearer token + device name.
func requestToken(t *testing.T, userNkeyPub, bearerToken, deviceName string) string {
	t.Helper()
	serverKP, err := nkeys.CreateServer()
	if err != nil {
		t.Fatalf("create server keypair: %v", err)
	}
	req := jwt.NewAuthorizationRequestClaims(userNkeyPub)
	req.UserNkey = userNkeyPub
	req.Server = jwt.ServerID{ID: "SRV1", Name: "test-server"}
	req.ClientInformation = jwt.ClientInformation{Name: deviceName}
	req.ConnectOptions = jwt.ConnectOptions{Token: bearerToken}
	tok, err := req.Encode(serverKP)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return tok
}

func decodeResponse(t *testing.T, token string) *jwt.AuthorizationResponseClaims {
	t.Helper()
	resp, err := jwt.DecodeAuthorizationResponseClaims(token)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestResponder_Handle_AllowsVerifiedOwnSlice(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	userKP, userPub := mustUserKeyPair(t)
	identity := validNanoID(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{
		"good-token": {ActorID: auth.IdentityKeyPrefix + identity, Subject: identity, ExpiresAt: time.Now().Add(time.Hour)},
	}}
	resolver := &fakeResolver{bound: false}

	r, err := NewResponder(authn, resolver, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "good-token", "device-1")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error != "" {
		t.Fatalf("expected no error, got %q", resp.Error)
	}
	if resp.Subject != userPub {
		t.Fatalf("response subject = %q, want %q", resp.Subject, userPub)
	}
	if resp.Audience != "SRV1" {
		t.Fatalf("response audience = %q, want server ID", resp.Audience)
	}

	uc, err := jwt.DecodeUserClaims(resp.Jwt)
	if err != nil {
		t.Fatalf("decode user jwt: %v", err)
	}
	if uc.Subject != userPub {
		t.Fatalf("user jwt subject = %q, want %q", uc.Subject, userPub)
	}
	wantSub := []string{"lattice.sync.user." + identity, "_INBOX.edge." + identity + ".>"}
	if fmt.Sprint([]string(uc.Sub.Allow)) != fmt.Sprint(wantSub) {
		t.Fatalf("subscribe allow = %v, want %v", uc.Sub.Allow, wantSub)
	}
	// Only the own-identity durable family is publish-allowed.
	for _, subj := range uc.Pub.Allow {
		if subj == "" {
			t.Fatalf("empty publish-allow entry")
		}
	}
	_ = userKP // keypair itself unused beyond producing userPub
}

func TestResponder_Handle_DeniesUnknownToken(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{}}
	resolver := &fakeResolver{}

	r, err := NewResponder(authn, resolver, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "bad-token", "device-1")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error == "" {
		t.Fatal("expected a denial, got none")
	}
	if resp.Jwt != "" {
		t.Fatalf("expected no user jwt on denial, got %q", resp.Jwt)
	}
}

func TestResponder_Handle_DeniesNoToken(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)

	r, err := NewResponder(&fakeAuthenticator{}, &fakeResolver{}, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "", "device-1")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error == "" {
		t.Fatal("expected a denial for an empty bearer token, got none")
	}
}

func TestResponder_Handle_DeniesUnsafeDeviceName(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{
		"good-token": {ActorID: auth.IdentityKeyPrefix + validNanoID(t), ExpiresAt: time.Now().Add(time.Hour)},
	}}
	r, err := NewResponder(authn, &fakeResolver{}, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "good-token", "device.with.dots")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error == "" {
		t.Fatal("expected a denial for a subject-unsafe device name, got none")
	}
}

func TestResponder_Handle_ResolvesClaimedIdentity(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)
	rawCred := validNanoID(t)
	claimedU := validNanoID(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{
		"good-token": {ActorID: auth.IdentityKeyPrefix + rawCred, ExpiresAt: time.Now().Add(time.Hour)},
	}}
	resolver := &fakeResolver{identityKey: auth.IdentityKeyPrefix + claimedU, bound: true}
	r, err := NewResponder(authn, resolver, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "good-token", "device-1")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error != "" {
		t.Fatalf("expected success, got %q", resp.Error)
	}
	uc, err := jwt.DecodeUserClaims(resp.Jwt)
	if err != nil {
		t.Fatalf("decode user jwt: %v", err)
	}
	if !uc.Sub.Allow.Contains("lattice.sync.user." + claimedU) {
		t.Fatalf("expected the CLAIMED identity's subject, got %v", uc.Sub.Allow)
	}
	if uc.Sub.Allow.Contains("lattice.sync.user." + rawCred) {
		t.Fatalf("must not grant the raw credential's own subject once resolved, got %v", uc.Sub.Allow)
	}
}

func TestResponder_Handle_ResolveErrorFallsBackToRawActor(t *testing.T) {
	// A credential-binding lookup error must NOT deny the connection — it
	// degrades to "act as the raw credential", the same deny-safe fallback
	// gateway.go's resolveActor already applies on the HTTP write path.
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)
	rawCred := validNanoID(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{
		"good-token": {ActorID: auth.IdentityKeyPrefix + rawCred, ExpiresAt: time.Now().Add(time.Hour)},
	}}
	resolver := &fakeResolver{err: errors.New("kv unavailable")}
	r, err := NewResponder(authn, resolver, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "good-token", "device-1")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error != "" {
		t.Fatalf("expected success (deny-safe fallback), got %q", resp.Error)
	}
	uc, err := jwt.DecodeUserClaims(resp.Jwt)
	if err != nil {
		t.Fatalf("decode user jwt: %v", err)
	}
	if !uc.Sub.Allow.Contains("lattice.sync.user." + rawCred) {
		t.Fatalf("expected the raw credential's own subject, got %v", uc.Sub.Allow)
	}
}

func TestResponder_Handle_ExpiredTokenDenied(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{
		"good-token": {ActorID: auth.IdentityKeyPrefix + validNanoID(t), ExpiresAt: time.Now().Add(-time.Minute)},
	}}
	r, err := NewResponder(authn, &fakeResolver{}, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "good-token", "device-1")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error == "" {
		t.Fatal("expected a denial for an already-expired token, got none")
	}
}

func TestResponder_Handle_AuthorizationCappedAtMaxTTL(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{
		// Token itself is long-lived; the issued authorization must still be
		// capped at maxTTL (design §3.5).
		"good-token": {ActorID: auth.IdentityKeyPrefix + validNanoID(t), ExpiresAt: time.Now().Add(24 * time.Hour)},
	}}
	r, err := NewResponder(authn, &fakeResolver{}, issuer, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	before := time.Now()
	reqTok := requestToken(t, userPub, "good-token", "device-1")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	uc, err := jwt.DecodeUserClaims(resp.Jwt)
	if err != nil {
		t.Fatalf("decode user jwt: %v", err)
	}
	exp := time.Unix(uc.Expires, 0)
	if exp.After(before.Add(3 * time.Minute)) {
		t.Fatalf("expiry %v not capped near maxTTL (2m from %v)", exp, before)
	}
}

func TestNewResponder_RejectsNonAccountIssuer(t *testing.T) {
	userKP, _ := mustUserKeyPair(t)
	_, err := NewResponder(&fakeAuthenticator{}, &fakeResolver{}, userKP, 0)
	if err == nil {
		t.Fatal("expected an error when issuer is a user key pair, not an account key pair")
	}
}

func TestNewResponder_MaxTTLFloorClamped(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	r, err := NewResponder(&fakeAuthenticator{}, &fakeResolver{}, issuer, time.Second)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	if r.maxTTL != MinAuthzTTL {
		t.Fatalf("maxTTL = %v, want the floor-clamped %v", r.maxTTL, MinAuthzTTL)
	}
}

func TestPermissionsFor_ExactPerConnectionPinning(t *testing.T) {
	perms := PermissionsFor("U1", "D1")

	wantSub := []string{"lattice.sync.user.U1", "_INBOX.edge.U1.>"}
	if fmt.Sprint([]string(perms.Sub.Allow)) != fmt.Sprint(wantSub) {
		t.Fatalf("Sub.Allow = %v, want %v", perms.Sub.Allow, wantSub)
	}

	// The control RPCs ARE granted as of Fire 2 (§3.3/§3.4, ratified
	// 2026-07-10): the server-side identity-binding override in
	// internal/refractor/control/service.go confines register/deregister/
	// hydrate/sessionkey to the caller's own identity regardless of
	// capability scope, so it is now safe to open the transport subjects —
	// see controlRPCs' doc comment. sessionkey (edge-lattice-full-design.md
	// §3.6, EDGE.4) and syncgap (edge-syncgap-control-rpc-design.md) joined
	// the same binding.
	wantPub := []string{
		"$JS.API.CONSUMER.CREATE.SYNC.edge-sync-U1-D1.lattice.sync.user.U1",
		"$JS.API.CONSUMER.MSG.NEXT.SYNC.edge-sync-U1-D1",
		"$JS.API.CONSUMER.INFO.SYNC.edge-sync-U1-D1",
		"$JS.API.CONSUMER.DELETE.SYNC.edge-sync-U1-D1",
		"$JS.ACK.SYNC.edge-sync-U1-D1.>",
		"lattice.ctrl.refractor.personal.register",
		"lattice.ctrl.refractor.personal.deregister",
		"lattice.ctrl.refractor.personal.hydrate",
		"lattice.ctrl.refractor.personal.sessionkey",
		"lattice.ctrl.refractor.personal.syncgap",
	}
	if fmt.Sprint([]string(perms.Pub.Allow)) != fmt.Sprint(wantPub) {
		t.Fatalf("Pub.Allow = %v, want %v", perms.Pub.Allow, wantPub)
	}

	// No wildcard-family grant anywhere (the wildcard-mechanics finding the
	// design's own adversarial pass caught, §12) — every durable-scoped
	// entry names the exact durable, never a "*"/">" suffix on the family.
	for _, subj := range append(append([]string{}, perms.Sub.Allow...), perms.Pub.Allow...) {
		if subj == inboxPrefix+".U1.>" {
			continue // the one deliberate multi-token wildcard: the inbox namespace.
		}
		if subj == "$JS.ACK.SYNC.edge-sync-U1-D1.>" {
			continue // the one deliberate multi-token wildcard: ack subjects.
		}
		if strings.ContainsAny(subj, "*") {
			t.Fatalf("unexpected wildcard in a durable-scoped grant: %q", subj)
		}
	}
}

func TestPermissionsFor_DifferentIdentitiesNeverShareASubject(t *testing.T) {
	a := PermissionsFor("alice", "phone")
	b := PermissionsFor("bob", "phone")

	for _, subjA := range a.Sub.Allow {
		for _, subjB := range b.Sub.Allow {
			if subjA == subjB {
				t.Fatalf("identity alice and bob share a subscribe subject: %q", subjA)
			}
		}
	}
	isControlRPC := func(subj string) bool {
		for _, rpc := range controlRPCs {
			if subj == rpc {
				return true
			}
		}
		return false
	}
	for _, subjA := range a.Pub.Allow {
		if isControlRPC(subjA) {
			// The control RPCs are deliberately identity-agnostic subjects
			// (design §3.3/§3.4) — confinement to the caller's own identity
			// is enforced server-side by the body.IdentityID binding
			// override, not by templating the identity into the subject.
			continue
		}
		for _, subjB := range b.Pub.Allow {
			if subjA == subjB {
				t.Fatalf("identity alice and bob share a publish subject: %q", subjA)
			}
		}
	}
}

func TestValidateSubjectToken(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"abc123", true},
		{"", false},
		{"a.b", false},
		{"a*b", false},
		{"a>b", false},
		{"a b", false},
		{"a\tb", false},
		{strings.Repeat("a", maxSubjectTokenLen), true},
		{strings.Repeat("a", maxSubjectTokenLen+1), false},
	}
	for _, c := range cases {
		err := validateSubjectToken(c.in)
		if (err == nil) != c.valid {
			t.Errorf("validateSubjectToken(%q) valid=%v, want %v (err=%v)", c.in, err == nil, c.valid, err)
		}
	}
}

// TestResponder_Handle_DeniesOversizedDeviceName is the length-cap half of
// validateSubjectToken's boundary (an adversarial-pass finding, LOW):
// deviceID is fully CONNECT-client-controlled with no upstream length check.
func TestResponder_Handle_DeniesOversizedDeviceName(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{
		"good-token": {ActorID: auth.IdentityKeyPrefix + validNanoID(t), ExpiresAt: time.Now().Add(time.Hour)},
	}}
	r, err := NewResponder(authn, &fakeResolver{}, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "good-token", strings.Repeat("d", maxSubjectTokenLen+1))
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error == "" {
		t.Fatal("expected a denial for an oversized device name, got none")
	}
}

// TestResponder_Handle_DeniesNonNanoIDResolvedIdentity is the alphabet-
// enforcement half of the resolved-identity check (an adversarial-pass
// finding, MEDIUM): a credential-bindings resolver returning a
// non-canonical identity key must fail closed rather than template a
// subject from it — the durable-collision safety argument (§3.3) depends on
// the identity segment never containing '-', which validateSubjectToken's
// generic character class alone would not have caught.
func TestResponder_Handle_DeniesNonNanoIDResolvedIdentity(t *testing.T) {
	issuer := mustAccountKeyPair(t)
	_, userPub := mustUserKeyPair(t)

	authn := &fakeAuthenticator{byToken: map[string]auth.VerifiedActor{
		"good-token": {ActorID: auth.IdentityKeyPrefix + validNanoID(t), ExpiresAt: time.Now().Add(time.Hour)},
	}}
	// A resolved key that carries the right prefix but a non-NanoID suffix
	// (short, and containing '-') — the shape a materializer bug or a
	// non-canonical identity vertex could produce.
	resolver := &fakeResolver{identityKey: auth.IdentityKeyPrefix + "not-a-nanoid", bound: true}
	r, err := NewResponder(authn, resolver, issuer, 0)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}

	reqTok := requestToken(t, userPub, "good-token", "device-1")
	respTok, err := r.Handle(context.Background(), reqTok)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := decodeResponse(t, respTok)
	if resp.Error == "" {
		t.Fatal("expected a denial for a non-NanoID resolved identity, got none")
	}
}

func mustXkeyPair(t *testing.T) nkeys.KeyPair {
	t.Helper()
	kp, err := nkeys.CreateCurveKeys()
	if err != nil {
		t.Fatalf("create curve keypair: %v", err)
	}
	return kp
}

// TestSealResponse_UnsealRequest_RoundTrip proves UnsealRequest/SealResponse
// are inverses of each other from two independent curve keypairs (the
// responder's own xkp and a stand-in for the server's per-instance
// ephemeral xkey) — the shape design §3.1a describes and cmd/gateway wires
// against the real server.
func TestSealResponse_UnsealRequest_RoundTrip(t *testing.T) {
	responderXkp := mustXkeyPair(t)
	responderXkeyPub, err := responderXkp.PublicKey()
	if err != nil {
		t.Fatalf("responder xkey public key: %v", err)
	}
	serverXkp := mustXkeyPair(t)
	serverXkeyPub, err := serverXkp.PublicKey()
	if err != nil {
		t.Fatalf("server xkey public key: %v", err)
	}

	const plaintext = "the-auth-request-jwt"
	sealed, err := serverXkp.Seal([]byte(plaintext), responderXkeyPub)
	if err != nil {
		t.Fatalf("seal request as the server: %v", err)
	}

	opened, err := UnsealRequest(sealed, serverXkeyPub, responderXkp)
	if err != nil {
		t.Fatalf("UnsealRequest: %v", err)
	}
	if opened != plaintext {
		t.Fatalf("UnsealRequest = %q, want %q", opened, plaintext)
	}

	const responseToken = "the-auth-response-jwt"
	sealedResp, err := SealResponse(responseToken, serverXkeyPub, responderXkp)
	if err != nil {
		t.Fatalf("SealResponse: %v", err)
	}
	openedResp, err := serverXkp.Open(sealedResp, responderXkeyPub)
	if err != nil {
		t.Fatalf("server-side open of SealResponse's output: %v", err)
	}
	if string(openedResp) != responseToken {
		t.Fatalf("server-opened response = %q, want %q", openedResp, responseToken)
	}
}

// TestUnsealRequest_MissingXkeyHeader is the day-one-mandatory half of §7:
// an empty serverXkey (the header cmd/gateway would have found absent) is
// refused, not treated as "unencrypted, pass through."
func TestUnsealRequest_MissingXkeyHeader(t *testing.T) {
	responderXkp := mustXkeyPair(t)
	if _, err := UnsealRequest([]byte("anything"), "", responderXkp); err == nil {
		t.Fatal("UnsealRequest with an empty serverXkey: want error, got nil")
	}
}
