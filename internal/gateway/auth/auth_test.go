package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	testKID  = "idp-key-1"
	testSub  = "Hj4kPmRtw9nbCxz5vQ2y"
	testIss  = "https://idp.example.test"
	testAud  = "lattice-read"
	testJTI  = "tok-abc123"
	otherKID = "idp-key-2"
)

// fixedNow anchors every time-based assertion so skew math is deterministic.
var fixedNow = time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)

type rsaKeypair struct {
	priv *rsa.PrivateKey
	pub  *rsa.PublicKey
}

func newRSA(t *testing.T) rsaKeypair {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	return rsaKeypair{priv: k, pub: &k.PublicKey}
}

func newECDSA(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa keygen: %v", err)
	}
	return k
}

// claims builds a standard claim set anchored on fixedNow with a valid window.
func claims() jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		Subject:   testSub,
		Issuer:    testIss,
		Audience:  jwt.ClaimStrings{testAud},
		ID:        testJTI,
		IssuedAt:  jwt.NewNumericDate(fixedNow.Add(-1 * time.Minute)),
		NotBefore: jwt.NewNumericDate(fixedNow.Add(-1 * time.Minute)),
		ExpiresAt: jwt.NewNumericDate(fixedNow.Add(5 * time.Minute)),
	}
}

func signRS256(t *testing.T, priv *rsa.PrivateKey, kid string, c jwt.RegisteredClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign RS256: %v", err)
	}
	return s
}

func signES256(t *testing.T, priv *ecdsa.PrivateKey, kid string, c jwt.RegisteredClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign ES256: %v", err)
	}
	return s
}

// nanoidKeyInfo builds a KeyInfo map binding every kid in keys as ModeNanoID
// — the test default: fixture subjects (testSub and friends) are already
// valid NanoIDs, so nanoid mode passes them through unchanged and every
// existing assertion (ActorID == IdentityKeyPrefix+testSub, etc.) still
// holds. Tests exercising the opaque/derivation path build their own KeyInfo
// explicitly.
func nanoidKeyInfo(keys map[string]crypto.PublicKey) map[string]KeyInfo {
	info := make(map[string]KeyInfo, len(keys))
	for kid := range keys {
		info[kid] = KeyInfo{Spec: BindingSpec{Mode: ModeNanoID}}
	}
	return info
}

// verifierFor builds a Verifier trusting the given keys (nanoid-mode, see
// nanoidKeyInfo), clocked at fixedNow, with issuer + audience checks enabled.
func verifierFor(t *testing.T, keys map[string]crypto.PublicKey) *Verifier {
	t.Helper()
	v, err := NewVerifier(Config{
		Keys:     keys,
		KeyInfo:  nanoidKeyInfo(keys),
		Issuer:   testIss,
		Audience: testAud,
		now:      func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestVerify_ValidRS256(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	got, err := v.Verify(signRS256(t, kp.priv, testKID, claims()))
	if err != nil {
		t.Fatalf("Verify: unexpected error %v", err)
	}
	if got.ActorID != IdentityKeyPrefix+testSub {
		t.Errorf("ActorID = %q, want %q", got.ActorID, IdentityKeyPrefix+testSub)
	}
	if got.Subject != testSub {
		t.Errorf("Subject = %q, want %q", got.Subject, testSub)
	}
	if got.TokenID != testJTI {
		t.Errorf("TokenID = %q, want %q", got.TokenID, testJTI)
	}
	if !got.ExpiresAt.Equal(fixedNow.Add(5 * time.Minute)) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, fixedNow.Add(5*time.Minute))
	}
}

func TestVerify_ValidES256(t *testing.T) {
	priv := newECDSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: &priv.PublicKey})

	got, err := v.Verify(signES256(t, priv, testKID, claims()))
	if err != nil {
		t.Fatalf("Verify: unexpected error %v", err)
	}
	if got.ActorID != IdentityKeyPrefix+testSub {
		t.Errorf("ActorID = %q, want %q", got.ActorID, IdentityKeyPrefix+testSub)
	}
}

// signRS256WithEmail signs an idpClaims token (multi-credential-identity-
// linking-design.md §3.4) — same signing path as signRS256, extended with
// the optional `email`/`email_verified` claims.
func signRS256WithEmail(t *testing.T, priv *rsa.PrivateKey, kid string, c jwt.RegisteredClaims, email string, verified bool) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, idpClaims{
		RegisteredClaims: c,
		Email:            email,
		EmailVerified:    verified,
	})
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign RS256 with email: %v", err)
	}
	return s
}

func TestVerify_EmailVerified_CapturesVerifiedEmail(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	got, err := v.Verify(signRS256WithEmail(t, kp.priv, testKID, claims(), "Person@Example.Test", true))
	if err != nil {
		t.Fatalf("Verify: unexpected error %v", err)
	}
	if got.VerifiedEmail != "Person@Example.Test" {
		t.Errorf("VerifiedEmail = %q, want %q (Verify carries the raw claim; normalization is the caller's job)",
			got.VerifiedEmail, "Person@Example.Test")
	}
}

func TestVerify_EmailUnverified_OmitsVerifiedEmail(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	got, err := v.Verify(signRS256WithEmail(t, kp.priv, testKID, claims(), "person@example.test", false))
	if err != nil {
		t.Fatalf("Verify: unexpected error %v", err)
	}
	if got.VerifiedEmail != "" {
		t.Errorf("VerifiedEmail = %q, want empty (email_verified=false must never be trusted)", got.VerifiedEmail)
	}
}

func TestVerify_NoEmailClaim_OmitsVerifiedEmail(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	got, err := v.Verify(signRS256(t, kp.priv, testKID, claims()))
	if err != nil {
		t.Fatalf("Verify: unexpected error %v", err)
	}
	if got.VerifiedEmail != "" {
		t.Errorf("VerifiedEmail = %q, want empty (no email claim present)", got.VerifiedEmail)
	}
}

// TestVerify_RejectAlgNone is the alg-none bypass: an unsigned token must never
// authenticate.
func TestVerify_RejectAlgNone(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims())
	tok.Header["kid"] = testKID
	noneToken, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}

	_, err = v.Verify(noneToken)
	if !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("Verify(none) error = %v, want ErrUnsupportedAlgorithm", err)
	}
}

// TestVerify_RejectHS256Confusion is the alg-confusion attack: a token signed
// HS256 using the RSA public key as the HMAC secret must be rejected (a naive
// verifier that fed the public key to an HMAC check would accept it).
func TestVerify_RejectHS256Confusion(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	pubDER, err := x509.MarshalPKIXPublicKey(kp.pub)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims())
	tok.Header["kid"] = testKID
	forged, err := tok.SignedString(pubPEM)
	if err != nil {
		t.Fatalf("sign HS256: %v", err)
	}

	_, err = v.Verify(forged)
	if !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("Verify(HS256-confusion) error = %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestVerify_Expired(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	c := claims()
	c.ExpiresAt = jwt.NewNumericDate(fixedNow.Add(-5 * time.Minute)) // well past skew
	_, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Verify(expired) error = %v, want ErrTokenExpired", err)
	}
}

// TestVerify_ExpiredWithinSkew — a token just past exp but inside the skew
// allowance still authenticates (clock-skew tolerance, design §3.4).
func TestVerify_ExpiredWithinSkew(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	c := claims()
	c.ExpiresAt = jwt.NewNumericDate(fixedNow.Add(-30 * time.Second)) // within DefaultClockSkew (60s)
	got, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if err != nil {
		t.Fatalf("Verify(within-skew) error = %v, want success", err)
	}
	if got.Subject != testSub {
		t.Errorf("Subject = %q, want %q", got.Subject, testSub)
	}
}

// TestVerify_NoExpiry — an unbounded token (no exp) is rejected; the design
// rests on short TTLs as the revocation backstop.
func TestVerify_NoExpiry(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	c := claims()
	c.ExpiresAt = nil
	_, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Verify(no-exp) error = %v, want ErrTokenExpired", err)
	}
}

func TestVerify_NotYetValid(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	c := claims()
	c.NotBefore = jwt.NewNumericDate(fixedNow.Add(5 * time.Minute)) // beyond skew
	_, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if !errors.Is(err, ErrTokenNotYetValid) {
		t.Fatalf("Verify(nbf-future) error = %v, want ErrTokenNotYetValid", err)
	}
}

func TestVerify_IssuedInFuture(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	c := claims()
	c.IssuedAt = jwt.NewNumericDate(fixedNow.Add(5 * time.Minute)) // beyond skew
	c.NotBefore = nil
	_, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if !errors.Is(err, ErrTokenNotYetValid) {
		t.Fatalf("Verify(iat-future) error = %v, want ErrTokenNotYetValid", err)
	}
}

func TestVerify_MissingSubject(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	c := claims()
	c.Subject = ""
	_, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if !errors.Is(err, ErrMissingSubject) {
		t.Fatalf("Verify(no-sub) error = %v, want ErrMissingSubject", err)
	}
}

func TestVerify_UntrustedIssuer(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	c := claims()
	c.Issuer = "https://evil.example.test"
	_, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if !errors.Is(err, ErrUntrustedIssuer) {
		t.Fatalf("Verify(bad-iss) error = %v, want ErrUntrustedIssuer", err)
	}
}

func TestVerify_WrongAudience(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	c := claims()
	c.Audience = jwt.ClaimStrings{"some-other-service"}
	_, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if !errors.Is(err, ErrWrongAudience) {
		t.Fatalf("Verify(bad-aud) error = %v, want ErrWrongAudience", err)
	}
}

// TestVerify_IssuerAudienceOptional — when issuer/audience are unset on the
// Verifier, the corresponding claims are not checked.
func TestVerify_IssuerAudienceOptional(t *testing.T) {
	kp := newRSA(t)
	keys := map[string]crypto.PublicKey{testKID: kp.pub}
	v, err := NewVerifier(Config{
		Keys:    keys,
		KeyInfo: nanoidKeyInfo(keys),
		now:     func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	c := claims()
	c.Issuer = "anything"
	c.Audience = jwt.ClaimStrings{"anything"}
	if _, err := v.Verify(signRS256(t, kp.priv, testKID, c)); err != nil {
		t.Fatalf("Verify(no-iss/aud-config) error = %v, want success", err)
	}
}

func TestVerify_UnknownKID(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	_, err := v.Verify(signRS256(t, kp.priv, otherKID, claims()))
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Verify(unknown-kid) error = %v, want ErrUnknownKey", err)
	}
}

func TestVerify_MissingKID(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	_, err := v.Verify(signRS256(t, kp.priv, "", claims()))
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Verify(no-kid) error = %v, want ErrUnknownKey", err)
	}
}

// TestVerify_KIDPointsToWrongKey — the kid resolves to a trusted key, but the
// token was signed by a different key: signature verification fails.
func TestVerify_KIDPointsToWrongKey(t *testing.T) {
	kpA := newRSA(t)
	kpB := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kpA.pub})

	// Signed by B's private key, but the header claims kid=testKID (=A's key).
	forged := signRS256(t, kpB.priv, testKID, claims())
	_, err := v.Verify(forged)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("Verify(kid-key-mismatch) error = %v, want ErrInvalidSignature", err)
	}
}

func TestVerify_Malformed(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})

	for _, tok := range []string{"", "not.a.jwt", "abc", "a.b"} {
		if _, err := v.Verify(tok); !errors.Is(err, ErrMalformedToken) {
			t.Errorf("Verify(%q) error = %v, want ErrMalformedToken", tok, err)
		}
	}
}

func TestNewVerifier_NoKeys(t *testing.T) {
	_, err := NewVerifier(Config{})
	if !errors.Is(err, ErrNoTrustedKeys) {
		t.Fatalf("NewVerifier(no keys) error = %v, want ErrNoTrustedKeys", err)
	}
}

func TestNewVerifier_DefaultSkew(t *testing.T) {
	kp := newRSA(t)
	keys := map[string]crypto.PublicKey{testKID: kp.pub}
	v, err := NewVerifier(Config{Keys: keys, KeyInfo: nanoidKeyInfo(keys)})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if v.clockSkew != DefaultClockSkew {
		t.Errorf("clockSkew = %v, want default %v", v.clockSkew, DefaultClockSkew)
	}
}

// --- Authenticator (verify + revocation kill-switch) ---

type fakeRevocation struct {
	revoked map[string]bool
	err     error
	gotID   string
}

func (f *fakeRevocation) IsRevoked(_ context.Context, actorID string) (bool, error) {
	f.gotID = actorID
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[actorID], nil
}

func TestAuthenticate_NoRevocationChecker(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})
	a := NewAuthenticator(v, nil)

	got, err := a.Authenticate(context.Background(), signRS256(t, kp.priv, testKID, claims()))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ActorID != IdentityKeyPrefix+testSub {
		t.Errorf("ActorID = %q, want %q", got.ActorID, IdentityKeyPrefix+testSub)
	}
}

func TestAuthenticate_NotRevoked(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})
	rc := &fakeRevocation{revoked: map[string]bool{}}
	a := NewAuthenticator(v, rc)

	got, err := a.Authenticate(context.Background(), signRS256(t, kp.priv, testKID, claims()))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if rc.gotID != IdentityKeyPrefix+testSub {
		t.Errorf("revocation checked id = %q, want %q", rc.gotID, IdentityKeyPrefix+testSub)
	}
	if got.Subject != testSub {
		t.Errorf("Subject = %q, want %q", got.Subject, testSub)
	}
}

func TestAuthenticate_Revoked(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})
	rc := &fakeRevocation{revoked: map[string]bool{IdentityKeyPrefix + testSub: true}}
	a := NewAuthenticator(v, rc)

	_, err := a.Authenticate(context.Background(), signRS256(t, kp.priv, testKID, claims()))
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("Authenticate(revoked) error = %v, want ErrTokenRevoked", err)
	}
}

// TestAuthenticate_RevocationError — a failing kill-switch check must fail
// closed (deny), never serve.
func TestAuthenticate_RevocationError(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})
	rc := &fakeRevocation{err: errors.New("kv down")}
	a := NewAuthenticator(v, rc)

	_, err := a.Authenticate(context.Background(), signRS256(t, kp.priv, testKID, claims()))
	if err == nil {
		t.Fatal("Authenticate(revocation-error): want error, got nil")
	}
	if errors.Is(err, ErrTokenRevoked) {
		t.Errorf("error = %v, want a wrapped check failure (not ErrTokenRevoked)", err)
	}
}

// TestAuthenticate_BadTokenSkipsRevocation — a verification failure short-
// circuits before the kill-switch is consulted.
func TestAuthenticate_BadTokenSkipsRevocation(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub})
	rc := &fakeRevocation{revoked: map[string]bool{}}
	a := NewAuthenticator(v, rc)

	_, err := a.Authenticate(context.Background(), "not.a.jwt")
	if !errors.Is(err, ErrMalformedToken) {
		t.Fatalf("Authenticate(bad) error = %v, want ErrMalformedToken", err)
	}
	if rc.gotID != "" {
		t.Errorf("revocation consulted (%q) on a bad token; want skipped", rc.gotID)
	}
}

// TestVerifier_Info_DefaultsAndOverride — NewVerifier's Config.KeyInfo seeds
// Info(); a kid present in Keys but whose KeyInfo entry declares only a Spec
// (no Source/Alg/AddedAt) reads those observability fields back zero-valued
// — the Spec itself is mandatory (a kid with NO entry at all is a
// construction error, covered by TestNewVerifier_SpecLessKidErrors).
func TestVerifier_Info_DefaultsAndOverride(t *testing.T) {
	kp := newRSA(t)
	other := newRSA(t)
	v, err := NewVerifier(Config{
		Keys: map[string]crypto.PublicKey{"known": kp.pub, "unlabeled": other.pub},
		KeyInfo: map[string]KeyInfo{
			"known":     {Source: "static", Alg: "RS256", AddedAt: fixedNow, Spec: BindingSpec{Mode: ModeNanoID}},
			"unlabeled": {Spec: BindingSpec{Mode: ModeNanoID}},
		},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	info := v.Info()
	if got := info["known"]; got.Source != "static" || got.Alg != "RS256" || !got.AddedAt.Equal(fixedNow) {
		t.Errorf("info[known] = %+v, want the seeded KeyInfo", got)
	}
	if got := info["unlabeled"]; got.Source != "" || got.Alg != "" || !got.AddedAt.IsZero() {
		t.Errorf("info[unlabeled] = %+v, want zero-value observability fields (only Spec given)", got)
	}
}

// TestNewVerifier_SpecLessKidErrors — a kid in Keys with no KeyInfo entry (or
// an entry with a zero-value Spec) is a construction error, never a silent
// default (Contract #11 §3.2, finding A2).
func TestNewVerifier_SpecLessKidErrors(t *testing.T) {
	kp := newRSA(t)
	_, err := NewVerifier(Config{Keys: map[string]crypto.PublicKey{testKID: kp.pub}})
	if err == nil {
		t.Fatal("NewVerifier with no KeyInfo at all: want a spec-less-kid error, got nil")
	}
}

// TestNewVerifier_OpaqueWithNoIssuerErrors — an opaque-mode kid with no
// declared issuer is a construction error: the per-source issuer pin is
// load-bearing for confinement (finding A8), not optional.
func TestNewVerifier_OpaqueWithNoIssuerErrors(t *testing.T) {
	kp := newRSA(t)
	_, err := NewVerifier(Config{
		Keys:    map[string]crypto.PublicKey{testKID: kp.pub},
		KeyInfo: map[string]KeyInfo{testKID: {Spec: BindingSpec{Mode: ModeOpaque}}},
	})
	if err == nil {
		t.Fatal("NewVerifier(opaque, no issuer): want error, got nil")
	}
}

// TestVerifier_SetKeysWithInfo_ReplacesBoth — a fresh SetKeysWithInfo call
// fully replaces both the trusted set and its provenance; a kid dropped from
// keys must not linger in Info(). A kid with no info entry (or a spec-less
// one) makes the WHOLE swap fail, atomically — see
// TestVerifier_SetKeysWithInfo_SpecLessKidRejectedAtomically.
func TestVerifier_SetKeysWithInfo_ReplacesBoth(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{"old": kp.pub})

	newKP := newRSA(t)
	err := v.SetKeysWithInfo(
		map[string]crypto.PublicKey{"new": newKP.pub, "bare": kp.pub},
		map[string]KeyInfo{
			"new":  {Source: "jwks", Alg: "ES256", AddedAt: fixedNow, Spec: BindingSpec{Mode: ModeNanoID}},
			"bare": {Spec: BindingSpec{Mode: ModeNanoID}},
		},
	)
	if err != nil {
		t.Fatalf("SetKeysWithInfo: %v", err)
	}

	info := v.Info()
	if _, ok := info["old"]; ok {
		t.Error(`info["old"] present after SetKeysWithInfo dropped it`)
	}
	if got := info["new"]; got.Source != "jwks" || got.Alg != "ES256" {
		t.Errorf(`info["new"] = %+v, want the passed KeyInfo`, got)
	}
	if got := info["bare"]; got.Source != "" || got.Alg != "" {
		t.Errorf(`info["bare"] = %+v, want zero-value observability fields (only Spec given)`, got)
	}
}

// TestVerifier_SetKeysWithInfo_SpecLessKidRejectedAtomically — a swap where
// ANY kid lacks a valid spec is rejected in full, leaving the prior trusted
// set (and its specs) untouched — a hot-reload must never partially apply.
func TestVerifier_SetKeysWithInfo_SpecLessKidRejectedAtomically(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{"old": kp.pub})

	newKP := newRSA(t)
	err := v.SetKeysWithInfo(
		map[string]crypto.PublicKey{"new": newKP.pub},
		nil, // "new" gets a zero-value KeyInfo -> zero-value (invalid) Spec
	)
	if err == nil {
		t.Fatal("SetKeysWithInfo(spec-less kid): want error, got nil")
	}
	if _, ok := v.Info()["old"]; !ok {
		t.Error(`info["old"] missing after a REJECTED swap — the prior trusted set must survive unchanged`)
	}
	if _, ok := v.Info()["new"]; ok {
		t.Error(`info["new"] present after a REJECTED swap — a partially-applied swap is not allowed`)
	}
}

// --- Subject binding (Contract #11 §3.2) ---

// opaqueVerifier builds a single-kid Verifier with kid pinned opaque-mode to
// issuer, clocked at fixedNow, with no Config.Issuer/Audience (those are the
// orthogonal general claim gate, unrelated to binding).
func opaqueVerifier(t *testing.T, kid string, pub crypto.PublicKey, issuer string) *Verifier {
	t.Helper()
	v, err := NewVerifier(Config{
		Keys:    map[string]crypto.PublicKey{kid: pub},
		KeyInfo: map[string]KeyInfo{kid: {Spec: BindingSpec{Mode: ModeOpaque, Issuer: issuer}}},
		now:     func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewVerifier(opaque): %v", err)
	}
	return v
}

// TestVerify_OpaqueDerivesGoldenVector is the frozen Contract #11 §3.2 golden
// vector: iss=https://accounts.google.com, sub=110169484474386276334 derives
// a specific ActorID. expected is computed via the SAME primitive Verify
// uses (substrate.SHA256NanoID) rather than hardcoded — the test is
// authoritative (finding MINOR-7) — and cross-checked against the frozen
// literal the contract publishes, so the two can never silently diverge.
func TestVerify_OpaqueDerivesGoldenVector(t *testing.T) {
	const iss = "https://accounts.google.com"
	const sub = "110169484474386276334"
	const frozenActorID = "vtx.identity.1FF5tdoN7GEGfDedQZ95"

	expected := IdentityKeyPrefix + substrate.SHA256NanoID(fmt.Sprintf("idpsub:%d:%s:%s", len(iss), iss, sub))
	if expected != frozenActorID {
		t.Fatalf("golden vector drifted: substrate.SHA256NanoID derivation = %q, contract's frozen literal = %q", expected, frozenActorID)
	}

	kp := newRSA(t)
	v := opaqueVerifier(t, testKID, kp.pub, iss)
	c := claims()
	c.Issuer = iss
	c.Subject = sub
	got, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.ActorID != frozenActorID {
		t.Errorf("ActorID = %q, want the frozen golden vector %q", got.ActorID, frozenActorID)
	}
	if got.Issuer != iss || got.RawSubject != sub {
		t.Errorf("Issuer/RawSubject = %q/%q, want %q/%q (raw provenance)", got.Issuer, got.RawSubject, iss, sub)
	}
}

// TestVerify_OpaqueMissingIssuerRejected — an opaque-mode token with no iss
// claim is rejected, never treated as unpinned/confined-by-default.
func TestVerify_OpaqueMissingIssuerRejected(t *testing.T) {
	kp := newRSA(t)
	v := opaqueVerifier(t, testKID, kp.pub, testIss)
	c := claims()
	c.Issuer = ""
	if _, err := v.Verify(signRS256(t, kp.priv, testKID, c)); !errors.Is(err, ErrMissingIssuer) {
		t.Fatalf("Verify(opaque, no iss) error = %v, want ErrMissingIssuer", err)
	}
}

// TestVerify_OpaqueIssuerMismatchRejected is finding A8's regression test:
// a token whose iss does not equal the verifying kid's declared issuer is
// rejected — the cross-issuer sub-replay guard.
func TestVerify_OpaqueIssuerMismatchRejected(t *testing.T) {
	kp := newRSA(t)
	v := opaqueVerifier(t, testKID, kp.pub, testIss)
	c := claims()
	c.Issuer = "https://a-different-idp.example.test"
	if _, err := v.Verify(signRS256(t, kp.priv, testKID, c)); !errors.Is(err, ErrIssuerMismatch) {
		t.Fatalf("Verify(opaque, iss mismatch) error = %v, want ErrIssuerMismatch", err)
	}
}

// TestVerify_OpaqueDerivationIsInjectionProof — the length-framed derivation
// input makes (iss, sub) pairs with ':'-laden values produce DISTINCT
// derived ids even when naive concatenation would collide
// ("a"+"|"+"bc" == "a|b"+"c"). Two sources, each pinned to its own iss so
// both verify, must derive different subjects for these adversarial pairs.
func TestVerify_OpaqueDerivationIsInjectionProof(t *testing.T) {
	issA, subA := "a", "bc"
	issB, subB := "a|b", "c" // naive "iss|sub" concatenation would collide with the pair above

	kpA := newRSA(t)
	vA := opaqueVerifier(t, testKID, kpA.pub, issA)
	cA := claims()
	cA.Issuer, cA.Subject = issA, subA
	gotA, err := vA.Verify(signRS256(t, kpA.priv, testKID, cA))
	if err != nil {
		t.Fatalf("Verify(A): %v", err)
	}

	kpB := newRSA(t)
	vB := opaqueVerifier(t, testKID, kpB.pub, issB)
	cB := claims()
	cB.Issuer, cB.Subject = issB, subB
	gotB, err := vB.Verify(signRS256(t, kpB.priv, testKID, cB))
	if err != nil {
		t.Fatalf("Verify(B): %v", err)
	}

	if gotA.ActorID == gotB.ActorID {
		t.Fatalf("(iss=%q,sub=%q) and (iss=%q,sub=%q) derived the SAME ActorID %q — length framing failed to prevent injection",
			issA, subA, issB, subB, gotA.ActorID)
	}
}

// TestVerify_Opaque255CharSubject — OIDC Core 1.0 bounds sub at 255
// case-sensitive ASCII chars; the derivation must handle the boundary.
func TestVerify_Opaque255CharSubject(t *testing.T) {
	sub := ""
	for len(sub) < 255 {
		sub += "x"
	}
	kp := newRSA(t)
	v := opaqueVerifier(t, testKID, kp.pub, testIss)
	c := claims()
	c.Subject = sub
	if _, err := v.Verify(signRS256(t, kp.priv, testKID, c)); err != nil {
		t.Fatalf("Verify(255-char sub): %v", err)
	}
}

// TestVerify_NanoIDMalformedSubjectRejected closes the residual: a
// nanoid-mode token whose sub is not a valid Contract #1 NanoID is rejected
// at the trust boundary rather than silently becoming a garbage key that
// fails late at provisioning.
func TestVerify_NanoIDMalformedSubjectRejected(t *testing.T) {
	kp := newRSA(t)
	v := verifierFor(t, map[string]crypto.PublicKey{testKID: kp.pub}) // nanoid mode
	c := claims()
	c.Subject = "not-a-valid-nanoid!!"
	if _, err := v.Verify(signRS256(t, kp.priv, testKID, c)); !errors.Is(err, ErrInvalidSubject) {
		t.Fatalf("Verify(nanoid, malformed sub) error = %v, want ErrInvalidSubject", err)
	}
}

// TestVerify_NanoIDSystemActorPassesThrough documents the trust grade: a
// nanoid-mode token for a system-actor-shaped NanoID is allowed through — the
// dev key is an arbitrary-identity assertion grant by design (finding A5),
// unreachable from config (never operator-selectable).
func TestVerify_NanoIDSystemActorPassesThrough(t *testing.T) {
	kp := newRSA(t)
	keys := map[string]crypto.PublicKey{testKID: kp.pub}
	// No Config.Issuer/Audience here (unlike verifierFor) — this test proves
	// the BINDING itself never consults iss under nanoid mode, unmuddied by
	// the orthogonal general claim gate.
	v, err := NewVerifier(Config{
		Keys:    keys,
		KeyInfo: nanoidKeyInfo(keys),
		now:     func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	c := claims()
	c.Issuer = "" // nanoid mode never checks/derives from iss — prove it's absent, not just unchecked
	c.Subject = testSub // already a valid NanoID (fixture convention)
	got, err := v.Verify(signRS256(t, kp.priv, testKID, c))
	if err != nil {
		t.Fatalf("Verify(nanoid, system-actor-shaped sub): %v", err)
	}
	if got.Subject != testSub || got.RawSubject != testSub {
		t.Errorf("Subject/RawSubject = %q/%q, want both %q (nanoid passthrough)", got.Subject, got.RawSubject, testSub)
	}
	if got.Issuer != "" {
		t.Errorf("Issuer = %q, want empty (no iss claim was sent)", got.Issuer)
	}
}

// TestVerify_BindingResolvesByVerifyingKid — two kids trusted under
// different modes; the spec that applies is the one belonging to the kid
// that actually verified the signature, never a global default.
func TestVerify_BindingResolvesByVerifyingKid(t *testing.T) {
	nanoidKP := newRSA(t)
	opaqueKP := newRSA(t)
	v, err := NewVerifier(Config{
		Keys: map[string]crypto.PublicKey{
			"nanoid-kid": nanoidKP.pub,
			"opaque-kid": opaqueKP.pub,
		},
		KeyInfo: map[string]KeyInfo{
			"nanoid-kid": {Spec: BindingSpec{Mode: ModeNanoID}},
			"opaque-kid": {Spec: BindingSpec{Mode: ModeOpaque, Issuer: testIss}},
		},
		now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	nanoidClaims := claims()
	nanoidClaims.Issuer = "" // nanoid mode never checks iss
	gotNanoid, err := v.Verify(signRS256(t, nanoidKP.priv, "nanoid-kid", nanoidClaims))
	if err != nil {
		t.Fatalf("Verify(nanoid-kid): %v", err)
	}
	if gotNanoid.Subject != testSub {
		t.Errorf("nanoid-kid Subject = %q, want passthrough %q", gotNanoid.Subject, testSub)
	}

	opaqueClaims := claims()
	opaqueClaims.Issuer = testIss
	gotOpaque, err := v.Verify(signRS256(t, opaqueKP.priv, "opaque-kid", opaqueClaims))
	if err != nil {
		t.Fatalf("Verify(opaque-kid): %v", err)
	}
	wantOpaqueSubject := substrate.SHA256NanoID(fmt.Sprintf("idpsub:%d:%s:%s", len(testIss), testIss, testSub))
	if gotOpaque.Subject != wantOpaqueSubject {
		t.Errorf("opaque-kid Subject = %q, want derived %q", gotOpaque.Subject, wantOpaqueSubject)
	}
	if gotOpaque.Subject == gotNanoid.Subject {
		t.Error("opaque-kid and nanoid-kid derived the same Subject for the same raw sub — binding did not resolve per-kid")
	}
}
