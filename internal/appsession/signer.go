package appsession

import (
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
)

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
}

// NewSigner builds a Signer over an explicit key — the seam tests use to sign
// with a throwaway key instead of the shared dev one.
func NewSigner(priv *rsa.PrivateKey, kid string, ttl time.Duration, now func() time.Time) *Signer {
	if now == nil {
		now = time.Now
	}
	return &Signer{priv: priv, kid: kid, ttl: ttl, now: now}
}

// Mint signs a bearer token for subject and reports when it expires.
func (s *Signer) Mint(subject string) (string, time.Time, error) {
	now := s.now()
	exp := now.Add(s.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	})
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
	return NewSigner(priv, auth.DevKeyID, DevTokenTTL, time.Now), nil
}

// NewAuthenticators builds the two verifiers session cookies are checked
// against: strict enforces auth.DefaultClockSkew and backs every ordinary
// per-request check, while refresh additionally tolerates RefreshGrace past a
// token's exp and backs ONLY POST /api/session/refresh. Both are nil when
// signer is nil — verifying a cookie is meaningless with no minter to have
// issued one.
func NewAuthenticators(logger *slog.Logger, envPrefix string, signer *Signer) (strict, refresh *auth.Authenticator, err error) {
	if signer == nil {
		return nil, nil, nil
	}
	trustedKeys, trustedSpecs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
		DevMode:    true,
		DevKeyPath: os.Getenv(envPrefix + "_DEV_PUBLIC_KEY_PATH"),
	}, func(msg string) { logger.Warn(msg) })
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: load shared dev trust key: %w", err)
	}
	keyInfo := auth.KeyInfoFromSpecs(trustedSpecs)
	verifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: keyInfo})
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: build session verifier: %w", err)
	}
	refreshVerifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: keyInfo, ClockSkew: RefreshGrace})
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: build session refresh verifier: %w", err)
	}
	return auth.NewAuthenticator(verifier, nil), auth.NewAuthenticator(refreshVerifier, nil), nil
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
