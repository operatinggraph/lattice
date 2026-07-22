// Package auth is the read-path actor-authentication seam (D1 increment 2).
//
// A reader presents a JWT signed by an external IdP/KMS. Lattice holds only the
// IdP's PUBLIC key(s) and never signs — the actor signing keys live outside the
// platform (lattice-architecture.md "External IdP for actor signing keys";
// brainstorm #118 "does NOT own actor signing keys"). The Verifier checks the
// signature, validates the standard time/issuer/audience claims, and extracts
// the Identity vertex id from the `sub` claim, returning the full vertex key
// `vtx.identity.<sub>` as the actor id — the read analog of write-path
// `Lattice-Actor` stamping: it AUTHENTICATES, it does NOT filter rows
// (filtering is read-path authorization, D1.3).
//
// Security posture (enforced + tested):
//   - asymmetric verification only — RS256 / ES256; the symmetric HS* family and
//     the unsigned `none` algorithm are refused before any key is consulted, so
//     the classic alg-confusion and alg-none bypasses cannot land;
//   - the JWT header `kid` selects the trusted public key; an unknown/absent kid
//     is rejected (no implicit single-key fallback that a forged kid could dodge);
//   - `exp` is required and enforced with a bounded clock-skew allowance; `nbf`
//     and `iat` (when present) are enforced under the same skew;
//   - `sub` is required and non-empty.
//
// The Authenticator composes the Verifier with a revocation kill-switch
// (internal/gateway/revocation) so a compromised actor can be cut off
// out-of-band even while holding a structurally-valid, unexpired token.
package auth

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// IdentityKeyPrefix is the canonical prefix of an identity vertex key
// (Contract #1 §1.1 — `vtx.identity.<id>`). The verified `sub` claim carries the
// bare identity id; the actor id surfaced to the read boundary is the full key,
// consistent with the cap-read doc's `actor` field (§6.14) and the write-path
// actor (`vtx.identity.<id>`). This is distinct from §6.14's
// `readableAnchors[].anchorId`, which is the resource's bare NanoID (the opaque
// match token via `nanoIdFromKey`), not a full key.
const IdentityKeyPrefix = "vtx.identity."

// allowedMethods is the closed set of accepted signing algorithms. Asymmetric
// only: Lattice verifies with a public key it does not own the private half of.
// HS* (symmetric) and `none` are absent by construction — a token presenting any
// other alg is rejected by jwt.WithValidMethods before the keyfunc runs.
var allowedMethods = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}

// Sentinel errors. Callers branch on these to map an authentication failure to a
// read-boundary response; all of them mean "deny" — none should ever serve data.
var (
	// ErrMalformedToken — the token is not a well-formed, parseable JWT.
	ErrMalformedToken = errors.New("auth: malformed token")
	// ErrUnsupportedAlgorithm — the token's alg is outside the asymmetric
	// allow-list (HS*, none, or anything unexpected).
	ErrUnsupportedAlgorithm = errors.New("auth: unsupported signing algorithm")
	// ErrUnknownKey — the token's `kid` does not match any trusted public key.
	ErrUnknownKey = errors.New("auth: unknown signing key")
	// ErrInvalidSignature — the signature did not verify against the trusted key.
	ErrInvalidSignature = errors.New("auth: invalid signature")
	// ErrTokenExpired — `exp` is in the past (beyond the skew allowance).
	ErrTokenExpired = errors.New("auth: token expired")
	// ErrTokenNotYetValid — `nbf`/`iat` is in the future (beyond the skew allowance).
	ErrTokenNotYetValid = errors.New("auth: token not yet valid")
	// ErrMissingSubject — the `sub` claim is absent or empty.
	ErrMissingSubject = errors.New("auth: missing subject claim")
	// ErrUntrustedIssuer — `iss` does not match the configured issuer.
	ErrUntrustedIssuer = errors.New("auth: untrusted issuer")
	// ErrWrongAudience — `aud` does not include the configured audience.
	ErrWrongAudience = errors.New("auth: wrong audience")
	// ErrTokenRevoked — the actor's identity is on the revocation kill-switch.
	ErrTokenRevoked = errors.New("auth: token revoked")
	// ErrNoTrustedKeys — the Verifier was constructed with no public keys; it
	// fails closed (every token is rejected) rather than trusting nothing.
	ErrNoTrustedKeys = errors.New("auth: no trusted keys configured")
	// ErrMissingIssuer — an opaque-mode kid's token carries no `iss` claim.
	// Contract #11 §3: the per-source issuer pin is what confines an opaque
	// source to its own derived subject subspace, so a missing issuer is
	// refused rather than treated as "unpinned" (finding A8).
	ErrMissingIssuer = errors.New("auth: opaque-mode token is missing the required iss claim")
	// ErrIssuerMismatch — an opaque-mode token's `iss` does not equal the
	// verifying kid's declared BindingSpec.Issuer. This is the cross-issuer
	// sub-replay guard (Contract #11 §3, finding A8): without it, a
	// hostile-but-trusted IdP-A could sign iss=<IdP-B> and derive IdP-B's
	// users' identities.
	ErrIssuerMismatch = errors.New("auth: token iss does not match the verifying key's declared issuer")
	// ErrInvalidSubject — a nanoid-mode token's `sub` is not a well-formed
	// Contract #1 NanoID. Closes the residual where a non-NanoID subject
	// silently became a garbage key, failing late at provisioning instead of
	// at the trust boundary.
	ErrInvalidSubject = errors.New("auth: nanoid-mode token subject is not a valid NanoID")
)

// BindingMode is how a trusted key's verified `sub` claim binds to a Lattice
// actor id (Contract #11 §3.2) — a property of the KEY SOURCE, fixed at load
// time, never inferred from token content.
type BindingMode string

const (
	// ModeOpaque is every configured external IdP source (a static <kid>.pem
	// dir or a JWKS endpoint): sub is IdP-native, so the actor id is derived
	// as SHA256NanoID("idpsub:"+len(iss)+":"+iss+":"+sub) — confined to that
	// source's declared issuer (BindingSpec.Issuer is mandatory in this mode).
	ModeOpaque BindingMode = "opaque"
	// ModeNanoID is the in-code dev key only, never operator-selectable from
	// config: sub IS the bare identity NanoID, passed through after an
	// IsValidNanoID shape gate. An arbitrary-identity assertion grant — exactly
	// right for the checked-in dev key, exactly wrong for a third-party IdP.
	ModeNanoID BindingMode = "nanoid"
)

// BindingSpec is a trusted key's subject-binding rule (Contract #11 §3.2). A
// kid with no declared spec is a construction error — a trusted key never
// binds by silent default (finding A2).
type BindingSpec struct {
	Mode BindingMode
	// Issuer is the source's declared `iss`, required and enforced exact-match
	// when Mode == ModeOpaque; unused under ModeNanoID.
	Issuer string
}

// validateSpec enforces that kid declares a well-formed BindingSpec: a known
// Mode, and — for ModeOpaque — a non-empty Issuer (the mandatory per-source
// pin that confinement depends on, finding A8). Called at every point a
// trusted set is installed (construction and hot-swap alike) so a kid can
// never enter the trusted set half-configured.
func validateSpec(kid string, spec BindingSpec) error {
	switch spec.Mode {
	case ModeOpaque:
		if spec.Issuer == "" {
			return fmt.Errorf("auth: kid %q is opaque-mode but declares no issuer — a configured external "+
				"source MUST pin an expected iss (an unpinned issuer allows cross-issuer subject replay)", kid)
		}
	case ModeNanoID:
		// No issuer needed — sub passes through after the IsValidNanoID gate.
	default:
		return fmt.Errorf("auth: kid %q has no binding spec declared (mode %q) — a trusted key must declare "+
			"how its subject binds to an actor id", kid, spec.Mode)
	}
	return nil
}

// KeyInfoFromSpecs wraps a kid→BindingSpec map (as returned by
// LoadTrustedKeys) into the kid→KeyInfo map Config.KeyInfo / SetKeysWithInfo
// expect, leaving Source/Alg/AddedAt zero (observability-only — callers that
// track provenance separately can overwrite those fields). A convenience for
// the common case of a caller with no separate provenance to attach.
func KeyInfoFromSpecs(specs map[string]BindingSpec) map[string]KeyInfo {
	info := make(map[string]KeyInfo, len(specs))
	for kid, spec := range specs {
		info[kid] = KeyInfo{Spec: spec}
	}
	return info
}

// Config configures a Verifier.
type Config struct {
	// Keys maps a key id (`kid`) to the trusted IdP public key (*rsa.PublicKey
	// or *ecdsa.PublicKey). At least one entry is required.
	Keys map[string]crypto.PublicKey
	// ClockSkew is the symmetric tolerance applied to exp/nbf/iat. A short JWT
	// TTL plus a small skew is the D1 freshness backstop (design §3.4 / M6).
	// Defaults to DefaultClockSkew when zero.
	ClockSkew time.Duration
	// Issuer, when non-empty, is required to match the token `iss` claim.
	Issuer string
	// Audience, when non-empty, is required to be present in the token `aud`.
	Audience string
	// KeyInfo carries per-kid provenance (source/alg/addedAt) AND the
	// mandatory BindingSpec for the keys in Keys. Every kid in Keys MUST have
	// an entry here with a valid Spec (NewVerifier rejects a spec-less kid —
	// a trusted key never binds by silent default, Contract #11 §3.2); the
	// Source/Alg/AddedAt fields may be left zero (observability-only, surfaced
	// via Verifier.Info() for the Gateway's jwks health block, never consulted
	// on the Verify hot path).
	KeyInfo map[string]KeyInfo
	// now overrides the clock for tests; nil uses time.Now.
	now func() time.Time
}

// KeyInfo is a trusted key's provenance plus its mandatory BindingSpec.
// Source/Alg/AddedAt are observability only (the Gateway's jwks health block,
// Contract #5) and never consulted on the Verify hot path; Spec IS consulted
// — it is how Verify turns a verified `sub` into an actor id (Contract #11
// §3.2).
type KeyInfo struct {
	// Source is "jwks" (fetched from the configured JWKS endpoint) or
	// "static" (an operator-configured KeysDir/dev-mode PEM).
	Source string
	// Alg is the JWK's advisory `alg` member when known ("" for static keys,
	// or a JWKS entry that omitted it — RFC 7517 §4.4 makes it optional).
	Alg string
	// AddedAt is when this kid first entered the trusted set. Preserved
	// across JWKS polls that re-fetch an already-trusted kid (only a
	// genuinely new kid gets a fresh timestamp).
	AddedAt time.Time
	// Spec is the mandatory subject-binding rule for this kid (Contract #11
	// §3.2). Required — see the Config.KeyInfo doc.
	Spec BindingSpec
}

// DefaultClockSkew is the time tolerance applied when Config.ClockSkew is zero.
const DefaultClockSkew = 60 * time.Second

// VerifiedActor is the outcome of a successful verification — an authenticated,
// non-filtered actor identity.
type VerifiedActor struct {
	// ActorID is the full identity vertex key (`vtx.identity.<sub>`).
	ActorID string
	// Subject is the raw `sub` claim (the bare identity id).
	Subject string
	// TokenID is the `jti` claim if present (used by per-token revocation; empty
	// when the IdP omits it).
	TokenID string
	// ExpiresAt is the token `exp`.
	ExpiresAt time.Time
	// Issuer is the raw `iss` claim, when present — provenance (Contract #11
	// §3.2/§3.3), captured regardless of binding mode. Never itself a trust
	// decision (the opaque-mode issuer check already ran inside Verify).
	Issuer string
	// RawSubject is the raw `sub` claim as the IdP sent it — under ModeOpaque
	// this differs from Subject (which carries the DERIVED id); under
	// ModeNanoID the two are identical. Provenance only (§3.3's .idpBinding
	// aspect), never itself an actor id.
	RawSubject string
	// VerifiedEmail is the `email` claim, populated only when the IdP also
	// asserted `email_verified: true` — never trusted otherwise. Optional;
	// empty when absent or unverified. Not part of Contract #11's accepted
	// token profile (§11.2, which governs authentication itself) — an
	// orthogonal, best-effort claim carried through the same way `jti`
	// becomes TokenID, consumed by the provision-time identityindex probe
	// (multi-credential-identity-linking-design.md §3.4).
	VerifiedEmail string
}

// idpClaims extends jwt.RegisteredClaims with the optional, non-authenticating
// `email`/`email_verified` claims (multi-credential-identity-linking-design.md
// §3.4). Absence of either is not an error — VerifiedEmail simply stays empty.
type idpClaims struct {
	jwt.RegisteredClaims
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
}

// trustedKey is one trusted kid's key + provenance/binding, held together so
// a hot-swap can never pair a new key with a stale spec (finding MINOR-5) —
// the single atomically-swapped map below is the fix; there is deliberately
// no second parallel atomic.Pointer.
type trustedKey struct {
	Key  crypto.PublicKey
	Info KeyInfo
}

// Verifier verifies IdP-signed JWTs and extracts the actor. It is safe for
// concurrent use — the trusted key set is held behind a single atomic pointer
// (key + provenance + binding spec together) so a JWKS poller (see jwks.go)
// can hot-swap it (kid-keyed rotation) without a lock on the hot Verify path
// and without a key/spec pairing ever going torn across the swap.
type Verifier struct {
	trusted   atomic.Pointer[map[string]trustedKey]
	clockSkew time.Duration
	issuer    string
	audience  string
	now       func() time.Time
	parser    *jwt.Parser
}

// NewVerifier builds a Verifier from cfg. It returns ErrNoTrustedKeys if no
// public keys are configured (fail closed — a keyless verifier would reject
// every token, which is correct, but the misconfiguration is worth surfacing at
// construction rather than silently denying all reads). It returns an error if
// any kid in cfg.Keys has no valid cfg.KeyInfo[kid].Spec — a trusted key never
// binds by silent default (Contract #11 §3.2, finding A2).
func NewVerifier(cfg Config) (*Verifier, error) {
	if len(cfg.Keys) == 0 {
		return nil, ErrNoTrustedKeys
	}
	trusted := make(map[string]trustedKey, len(cfg.Keys))
	for kid, k := range cfg.Keys {
		info := cfg.KeyInfo[kid]
		if err := validateSpec(kid, info.Spec); err != nil {
			return nil, err
		}
		trusted[kid] = trustedKey{Key: k, Info: info}
	}
	skew := cfg.ClockSkew
	if skew == 0 {
		skew = DefaultClockSkew
	}
	nowFn := cfg.now
	if nowFn == nil {
		nowFn = time.Now
	}
	// jwt.WithValidMethods rejects any token whose alg is outside the allow-list
	// before the keyfunc is invoked — the structural guard against alg confusion
	// (a forged HS256 that tries to verify the public key as an HMAC secret) and
	// the `none` bypass. WithoutClaimsValidation hands time validation to the
	// explicit skew-aware checks below (the library's default leeway is 0).
	parser := jwt.NewParser(
		jwt.WithValidMethods(allowedMethods),
		jwt.WithoutClaimsValidation(),
	)
	v := &Verifier{
		clockSkew: skew,
		issuer:    cfg.Issuer,
		audience:  cfg.Audience,
		now:       nowFn,
		parser:    parser,
	}
	v.trusted.Store(&trusted)
	return v, nil
}

// SetKeysWithInfo atomically replaces the trusted kid→public-key set and its
// per-kid provenance/binding-spec in one swap. A concurrent Verify call in
// flight completes against whichever key set it already loaded (no lock, no
// torn reads) — the standard atomic-pointer swap pattern. Called by a JWKS
// poller (jwks.go) on each successful refresh; never called with an empty
// keys map (a poller keeps the last-known-good set on a failed/empty fetch
// rather than swapping in nothing — see JWKSPoller.FetchOnce). It returns an
// error, WITHOUT swapping anything in, if any kid in keys has no valid
// info[kid].Spec — the same fail-closed rule NewVerifier enforces at
// construction applies at every hot-swap too.
func (v *Verifier) SetKeysWithInfo(keys map[string]crypto.PublicKey, info map[string]KeyInfo) error {
	trusted := make(map[string]trustedKey, len(keys))
	for kid, k := range keys {
		ki := info[kid]
		if err := validateSpec(kid, ki.Spec); err != nil {
			return err
		}
		trusted[kid] = trustedKey{Key: k, Info: ki}
	}
	v.trusted.Store(&trusted)
	return nil
}

// Info returns a snapshot of the current trusted set's per-kid provenance
// (including its BindingSpec), keyed by kid — read by the Gateway's jwks
// health block. Never consulted on the Verify hot path (Verify reads its own
// atomic snapshot directly).
func (v *Verifier) Info() map[string]KeyInfo {
	p := v.trusted.Load()
	if p == nil {
		return nil
	}
	cp := make(map[string]KeyInfo, len(*p))
	for kid, t := range *p {
		cp[kid] = t.Info
	}
	return cp
}

// Verify checks tokenString and returns the authenticated actor, or one of the
// sentinel errors above on any failure. It never returns a VerifiedActor on
// error.
func (v *Verifier) Verify(tokenString string) (VerifiedActor, error) {
	// keyfunc and the binding-spec lookup below share ONE map load (captured
	// into matched here) so the key that verified the signature and the spec
	// that binds its subject always come from the same atomic snapshot — a
	// second independent load could observe an intervening hot-swap and pair
	// the verified key with a DIFFERENT kid's (or no) spec (finding MINOR-5).
	var matched trustedKey
	keyfunc := func(token *jwt.Token) (any, error) {
		switch token.Method.(type) {
		case *jwt.SigningMethodRSA, *jwt.SigningMethodECDSA:
			// asymmetric — expected.
		default:
			return nil, ErrUnsupportedAlgorithm
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, ErrUnknownKey
		}
		trusted := *v.trusted.Load()
		entry, ok := trusted[kid]
		if !ok {
			return nil, ErrUnknownKey
		}
		matched = entry
		return entry.Key, nil
	}

	claims := idpClaims{}
	_, err := v.parser.ParseWithClaims(tokenString, &claims, keyfunc)
	if err != nil {
		return VerifiedActor{}, mapParseError(err)
	}

	now := v.now()
	if err := v.checkTime(&claims.RegisteredClaims, now); err != nil {
		return VerifiedActor{}, err
	}
	if v.issuer != "" && claims.Issuer != v.issuer {
		return VerifiedActor{}, ErrUntrustedIssuer
	}
	if v.audience != "" && !containsString(claims.Audience, v.audience) {
		return VerifiedActor{}, ErrWrongAudience
	}

	sub := strings.TrimSpace(claims.Subject)
	if sub == "" {
		return VerifiedActor{}, ErrMissingSubject
	}
	iss := strings.TrimSpace(claims.Issuer)

	// Subject binding (Contract #11 §3.2) — a property of the trust source,
	// never inferred from token content (rejected Option C, design §4).
	var actorSubject string
	switch matched.Info.Spec.Mode {
	case ModeOpaque:
		if iss == "" {
			return VerifiedActor{}, ErrMissingIssuer
		}
		if iss != matched.Info.Spec.Issuer {
			// Cross-issuer sub-replay guard (finding A8): without this, a
			// trusted-but-hostile source could sign iss=<peer's issuer> and
			// derive the peer's users' identities.
			return VerifiedActor{}, ErrIssuerMismatch
		}
		// Length-framed so no (iss', sub') != (iss, sub) can produce the same
		// input string, even with ':' inside either value.
		actorSubject = substrate.SHA256NanoID(fmt.Sprintf("idpsub:%d:%s:%s", len(iss), iss, sub))
	case ModeNanoID:
		if !substrate.IsValidNanoID(sub) {
			return VerifiedActor{}, ErrInvalidSubject
		}
		actorSubject = sub
	default:
		// Unreachable: validateSpec (construction + every SetKeysWithInfo
		// swap) already rejects any kid with an invalid/absent Spec.Mode.
		return VerifiedActor{}, fmt.Errorf("auth: kid has no binding spec (mode %q)", matched.Info.Spec.Mode)
	}

	var exp time.Time
	if claims.ExpiresAt != nil {
		exp = claims.ExpiresAt.Time
	}
	var verifiedEmail string
	if claims.EmailVerified {
		verifiedEmail = strings.TrimSpace(claims.Email)
	}
	return VerifiedActor{
		ActorID:       IdentityKeyPrefix + actorSubject,
		Subject:       actorSubject,
		TokenID:       claims.ID,
		ExpiresAt:     exp,
		Issuer:        iss,
		RawSubject:    sub,
		VerifiedEmail: verifiedEmail,
	}, nil
}

// checkTime enforces exp (required) and nbf/iat (when present) under the
// configured clock skew.
func (v *Verifier) checkTime(c *jwt.RegisteredClaims, now time.Time) error {
	if c.ExpiresAt == nil {
		// No expiry = an unbounded token. Reject: the design rests on short TTLs
		// as the revocation backstop (M6).
		return ErrTokenExpired
	}
	if now.After(c.ExpiresAt.Add(v.clockSkew)) {
		return ErrTokenExpired
	}
	if c.NotBefore != nil && now.Before(c.NotBefore.Add(-v.clockSkew)) {
		return ErrTokenNotYetValid
	}
	if c.IssuedAt != nil && now.Before(c.IssuedAt.Add(-v.clockSkew)) {
		return ErrTokenNotYetValid
	}
	return nil
}

// mapParseError collapses golang-jwt's error tree into our sentinel set so
// callers branch on a stable surface, never on the library's internal types.
// Ordered most-specific first: the keyfunc-wrapped sentinels (joined via %w, so
// errors.Is reaches them) precede the broad library categories.
func mapParseError(err error) error {
	switch {
	case errors.Is(err, ErrUnknownKey):
		return ErrUnknownKey
	case errors.Is(err, ErrUnsupportedAlgorithm):
		return ErrUnsupportedAlgorithm
	case errors.Is(err, jwt.ErrTokenMalformed):
		return ErrMalformedToken
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		// Either the WithValidMethods alg rejection ("signing method <alg> is
		// invalid" — an HS*/none/unexpected token) or a genuine signature
		// mismatch. Both deny; distinguish so the caller gets a precise sentinel.
		if strings.Contains(err.Error(), "signing method") {
			return ErrUnsupportedAlgorithm
		}
		return ErrInvalidSignature
	case errors.Is(err, jwt.ErrTokenUnverifiable):
		// alg unspecified (no `alg` header) or unavailable (an unknown method
		// name the library cannot resolve) — both are an unusable algorithm.
		return ErrUnsupportedAlgorithm
	default:
		return fmt.Errorf("%w: %v", ErrMalformedToken, err)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// RevocationChecker is the kill-switch surface the Authenticator consults after a
// token verifies. internal/gateway/revocation provides the substrate-backed
// implementation; the interface keeps this package free of a substrate import
// and lets the Authenticator be tested with a fake.
type RevocationChecker interface {
	// IsRevoked reports whether actorID (the full identity vertex key) has been
	// revoked. A transport/KV error is returned as-is so the caller can fail
	// closed.
	IsRevoked(ctx context.Context, actorID string) (bool, error)
}

// Authenticator is the full read-actor seam: verify the JWT, then consult the
// revocation kill-switch. It is the entry point a read boundary calls (D1.3).
type Authenticator struct {
	verifier   *Verifier
	revocation RevocationChecker
}

// NewAuthenticator composes a Verifier with a RevocationChecker. A nil checker
// is allowed (verification only, no kill-switch) for deployments that have not
// provisioned the revocation bucket yet.
func NewAuthenticator(v *Verifier, rc RevocationChecker) *Authenticator {
	return &Authenticator{verifier: v, revocation: rc}
}

// Authenticate verifies tokenString and, on success, checks the revocation
// kill-switch. It returns ErrTokenRevoked if the actor is revoked, the
// verifier's sentinel on a verification failure, or a wrapped error if the
// revocation check itself fails (fail closed — a read boundary must deny when it
// cannot confirm the actor is live).
func (a *Authenticator) Authenticate(ctx context.Context, tokenString string) (VerifiedActor, error) {
	actor, err := a.verifier.Verify(tokenString)
	if err != nil {
		return VerifiedActor{}, err
	}
	if a.revocation == nil {
		return actor, nil
	}
	revoked, err := a.revocation.IsRevoked(ctx, actor.ActorID)
	if err != nil {
		return VerifiedActor{}, fmt.Errorf("auth: revocation check failed: %w", err)
	}
	if revoked {
		return VerifiedActor{}, ErrTokenRevoked
	}
	return actor, nil
}
