package appsession

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
)

// defaultIdPKeyID is the key id an external-IdP deployment's public key is
// filed under when <envPrefix>_JWT_KID names none.
const defaultIdPKeyID = "idp-key-1"

// DevTokenTTL is how long a dev-minted session token stays valid. The
// browser renews well before this through POST /api/session/refresh.
const DevTokenTTL = 30 * time.Minute

// Signer mints the demo-posture JWTs an app's session cookie carries — the
// shared checked-in dev key every other dev-auth surface in the platform
// already signs with, so a token minted here verifies against the Gateway,
// the vertical apps' read boundaries, and this kit's own verifiers alike. A
// nil Signer is the production posture: every minting endpoint reports 404
// and only externally-issued tokens can open a session.
type Signer struct {
	priv *rsa.PrivateKey
	kid  string
	ttl  time.Duration
	now  func() time.Time
	// aud is embedded as the minted token's `aud` claim when non-empty — the
	// minting app's own envPrefix (NewDevSigner). Every dev-auth adopter
	// shares one signing key and verifier config, so nothing else stops a
	// token minted for one app's cookie from also verifying as another's;
	// binding mint and verify (NewAuthenticators) to the same envPrefix
	// closes that cross-app cookie-planting path (appsession.md's
	// co-hosted-page session-fixation gap).
	aud string
}

// NewSigner builds a Signer over an explicit key — the seam tests use to sign
// with a throwaway key instead of the shared dev one. aud is the app-scoping
// audience Mint embeds; empty omits the claim.
func NewSigner(priv *rsa.PrivateKey, kid string, ttl time.Duration, now func() time.Time, aud string) *Signer {
	if now == nil {
		now = time.Now
	}
	return &Signer{priv: priv, kid: kid, ttl: ttl, now: now, aud: aud}
}

// sessionClaims is what Mint/MintWithCredential sign — jwt.RegisteredClaims
// plus the optional `cred_id` claim auth.VerifiedActor.CredentialID reads back.
type sessionClaims struct {
	jwt.RegisteredClaims
	CredID string `json:"cred_id,omitempty"`
}

// Mint signs a bearer token for subject and reports when it expires.
func (s *Signer) Mint(subject string) (string, time.Time, error) {
	return s.mint(subject, "")
}

// MintWithCredential signs a bearer token for subject, additionally recording
// which credential authenticated it (the `cred_id` claim) — for the one case
// where the two differ: a login that resolved a credential, via its `boundTo`
// link, onto a different identity's session. Every other mint (a direct
// sign-in, a session refresh, a throwaway device credential) needs no
// distinct value and calls Mint.
func (s *Signer) MintWithCredential(subject, credentialID string) (string, time.Time, error) {
	return s.mint(subject, credentialID)
}

func (s *Signer) mint(subject, credentialID string) (string, time.Time, error) {
	now := s.now()
	exp := now.Add(s.ttl)
	claims := sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		CredID: credentialID,
	}
	if s.aud != "" {
		claims.Audience = jwt.ClaimStrings{s.aud}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.kid
	signed, err := tok.SignedString(s.priv)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// NewDevSigner builds the dev-posture minter from <envPrefix>_DEV_AUTH and
// <envPrefix>_DEV_PRIVATE_KEY_PATH, returning a nil Signer when dev auth is
// off. An in-process minter trusts whatever subject its caller names, so it
// is refused outright off a loopback bind — defence in depth behind the
// Gateway's own verification of every write.
func NewDevSigner(logger *slog.Logger, envPrefix string, loopback bool) (*Signer, error) {
	if !Truthy(os.Getenv(envPrefix + "_DEV_AUTH")) {
		return nil, nil
	}
	if !loopback {
		return nil, fmt.Errorf("%s_DEV_AUTH is only permitted on a loopback bind (the in-process minter trusts any subject)", envPrefix)
	}
	priv, err := auth.LoadDevSigningKey(os.Getenv(envPrefix + "_DEV_PRIVATE_KEY_PATH"))
	if err != nil {
		return nil, fmt.Errorf("dev-auth: load shared dev signing key: %w", err)
	}
	logger.Warn(envPrefix + "_DEV_AUTH ENABLED: this process mints demo JWTs in-process (NOT for production)")
	return NewSigner(priv, auth.DevKeyID, DevTokenTTL, time.Now, envPrefix), nil
}

// NewAuthenticators builds the verifiers session cookies are checked against,
// in whichever of the two postures the environment configures. revocationChecker
// gates every verified token against the kill-switch bucket and may be nil (no
// kill-switch, the short token TTL as the only backstop).
//
// DEMO (signer non-nil): two verifiers over the shared dev key. strict enforces
// auth.DefaultClockSkew and backs every ordinary per-request check; refresh
// additionally tolerates RefreshGrace past a token's exp and backs ONLY POST
// /api/session/refresh.
//
// PRODUCTION (signer nil, <envPrefix>_JWT_PUBLIC_KEY set): one verify-only
// authenticator over the external IdP's PEM public key, pinned to the issuer
// <envPrefix>_JWT_ISSUER names. There is no refresh verifier — nothing in this
// process minted the cookie, so nothing here can renew it, and handleRefresh
// reports 404.
//
// Neither configured: all nil. A read gated on a session then fails closed,
// which is the correct default for an unprovisioned boundary.
func NewAuthenticators(logger *slog.Logger, envPrefix string, signer *Signer, revocationChecker auth.RevocationChecker) (strict, refresh *auth.Authenticator, err error) {
	if signer == nil {
		strict, err = idpAuthenticator(logger, envPrefix, revocationChecker)
		return strict, nil, err
	}
	if strings.TrimSpace(os.Getenv(envPrefix+"_JWT_PUBLIC_KEY")) != "" {
		logger.Warn("both " + envPrefix + "_DEV_AUTH and " + envPrefix + "_JWT_PUBLIC_KEY are set; dev auth wins and the configured IdP public key is IGNORED")
	}
	trustedKeys, trustedSpecs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
		DevMode:    true,
		DevKeyPath: os.Getenv(envPrefix + "_DEV_PUBLIC_KEY_PATH"),
	}, func(msg string) { logger.Warn(msg) })
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: load shared dev trust key: %w", err)
	}
	keyInfo := auth.KeyInfoFromSpecs(trustedSpecs)
	// Audience: envPrefix pins verification to the same app the Signer minted
	// for (NewDevSigner passes the identical envPrefix into NewSigner's aud).
	// Every dev-auth adopter trusts the same checked-in key, so without this a
	// token minted by any one of them would also verify for every other — the
	// gap that lets a co-hosted sibling page plant an absent session cookie
	// (document.cookie, HttpOnly blocks overwrite but not create) that then
	// verifies when the victim later visits this app.
	verifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: keyInfo, Audience: envPrefix})
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: build session verifier: %w", err)
	}
	refreshVerifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: keyInfo, ClockSkew: RefreshGrace, Audience: envPrefix})
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: build session refresh verifier: %w", err)
	}
	return auth.NewAuthenticator(verifier, revocationChecker), auth.NewAuthenticator(refreshVerifier, revocationChecker), nil
}

// idpAuthenticator builds the verify-only authenticator an external IdP's
// public key configures, or nil when <envPrefix>_JWT_PUBLIC_KEY names none.
func idpAuthenticator(logger *slog.Logger, envPrefix string, revocationChecker auth.RevocationChecker) (*auth.Authenticator, error) {
	pemKey := os.Getenv(envPrefix + "_JWT_PUBLIC_KEY")
	if strings.TrimSpace(pemKey) == "" {
		return nil, nil
	}
	pub, err := parsePublicKeyPEM(pemKey)
	if err != nil {
		return nil, fmt.Errorf("%s_JWT_PUBLIC_KEY: %w", envPrefix, err)
	}
	issuer := os.Getenv(envPrefix + "_JWT_ISSUER")
	if strings.TrimSpace(issuer) == "" {
		return nil, fmt.Errorf("%s_JWT_ISSUER is required alongside %s_JWT_PUBLIC_KEY "+
			"(a configured external IdP source MUST pin an expected iss — Contract #11 §11.3)", envPrefix, envPrefix)
	}
	kid := os.Getenv(envPrefix + "_JWT_KID")
	if strings.TrimSpace(kid) == "" {
		kid = defaultIdPKeyID
	}
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:     map[string]crypto.PublicKey{kid: pub},
		KeyInfo:  map[string]auth.KeyInfo{kid: {Spec: auth.BindingSpec{Mode: auth.ModeOpaque, Issuer: issuer}}},
		Audience: strings.TrimSpace(os.Getenv(envPrefix + "_JWT_AUDIENCE")),
	})
	if err != nil {
		return nil, fmt.Errorf("build session verifier: %w", err)
	}
	logger.Info("session boundary configured with external IdP public key", "kid", kid)
	return auth.NewAuthenticator(verifier, revocationChecker), nil
}

// parsePublicKeyPEM decodes a PEM-encoded RSA or ECDSA public key (PKIX). Any
// other key type is refused: the verifier accepts only RS*/ES* signatures (the
// auth.allowedMethods closed set), so an Ed25519 or other PKIX key would parse
// cleanly here yet fail every verification at runtime with no startup signal.
func parsePublicKeyPEM(pemStr string) (crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX public key: %w", err)
	}
	switch pub.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey:
		return pub, nil
	default:
		return nil, fmt.Errorf("unsupported public key type %T (only RSA and ECDSA are accepted)", pub)
	}
}

// Truthy reads the platform's env-flag vocabulary.
func Truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// IsLoopbackHost reports whether host names only this machine. An empty host
// (the bare ":7810" form) means all interfaces and is NOT loopback — fail safe.
func IsLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// HostOf extracts the host from a listen address, empty when it has none.
func HostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return host
}
