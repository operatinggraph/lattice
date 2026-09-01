package auth

import (
	"context"
	"crypto"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// maxJWKSBodyBytes bounds a fetched JWKS document. A real IdP's key set is a
// handful of RSA/EC keys — a few KiB. 1 MiB matches the Gateway's own request
// body ceiling (internal/gateway.maxBodyBytes) as a generous, still-bounded
// backstop against a misbehaving or compromised JWKS endpoint.
const maxJWKSBodyBytes = 1 << 20

// MinJWKSPollInterval is the floor JWKSPoller enforces on its poll interval —
// a misconfigured near-zero interval must not turn into a request storm
// against the IdP's JWKS endpoint.
const MinJWKSPollInterval = 30 * time.Second

// DefaultJWKSPollInterval is used when the caller passes interval <= 0.
const DefaultJWKSPollInterval = 5 * time.Minute

// Logger is the minimal logging surface JWKSPoller needs. *slog.Logger and
// internal/gateway.Logger both satisfy it structurally.
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// JWKSPoller periodically fetches a JWKS document and hot-swaps the trusted
// key set on a Verifier via SetKeys, so a rotated IdP signing key is picked
// up without a Gateway restart (design gateway-external-trust-boundary
// §8 Fire 2 remainder). staticKeys (e.g. a dev/break-glass key) are ALWAYS
// merged into every swap, layered under the JWKS-fetched keys — a rotating
// JWKS response can add or retire IdP keys but can never un-trust a key the
// operator configured directly.
//
// Fail-safe, not fail-closed, on a live poll: a transient fetch/parse
// failure (network blip, IdP outage, momentarily-empty response) logs and
// KEEPS the last-known-good key set rather than swapping in nothing — a live
// session must not be locked out by a passing IdP hiccup. The fail-CLOSED
// gate (no trusted keys at all) is enforced once, at startup, by the
// caller's use of FetchOnce before serving traffic (mirrors the existing
// "no trusted keys configured — refusing to start" posture in cmd/gateway).
type JWKSPoller struct {
	url         string
	client      *http.Client
	verifier    *Verifier
	staticKeys  map[string]crypto.PublicKey
	staticSpecs map[string]BindingSpec
	issuer      string
	interval    time.Duration
	logger      Logger

	// lastPollAt/swaps back the Gateway's jwks health block — lastPollAt is
	// the last SUCCESSFUL fetch (UnixNano; 0 = never), mirroring the
	// revocation block's lastSyncAt semantics; swaps counts fetches whose
	// resulting trusted kid set differed from the prior one (a kid added or
	// removed), not every poll tick.
	lastPollAt atomic.Int64
	swaps      atomic.Uint64
}

// NewJWKSPoller builds a poller for url, hot-swapping verifier's trusted keys
// on each successful fetch. Every kid this poller FETCHES is opaque-mode,
// pinned to issuer (a live JWKS endpoint is by definition an external IdP —
// Contract #11 §11.3 — issuer is required, not optional: a live key source
// with no declared issuer would allow cross-issuer subject replay, finding
// A8). staticKeys/staticSpecs (both may be nil) are operator-configured keys
// (dev/break-glass) ALWAYS merged into every swap, layered under the
// JWKS-fetched keys — every kid in staticKeys must have a valid entry in
// staticSpecs (the same fail-closed rule NewVerifier enforces). interval <= 0
// uses DefaultJWKSPollInterval; a positive interval below MinJWKSPollInterval
// is clamped up to it. logger may be nil (discards).
func NewJWKSPoller(url string, verifier *Verifier, staticKeys map[string]crypto.PublicKey, staticSpecs map[string]BindingSpec, issuer string, interval time.Duration, logger Logger) (*JWKSPoller, error) {
	if issuer == "" {
		return nil, fmt.Errorf("auth: JWKS poller for %q requires a declared issuer — a live external key source "+
			"is always opaque-mode and MUST pin an expected iss", url)
	}
	for kid := range staticKeys {
		if err := validateSpec(kid, staticSpecs[kid]); err != nil {
			return nil, err
		}
	}
	if interval <= 0 {
		interval = DefaultJWKSPollInterval
	} else if interval < MinJWKSPollInterval {
		interval = MinJWKSPollInterval
	}
	if logger == nil {
		logger = nopLogger{}
	}
	return &JWKSPoller{
		url:         url,
		client:      &http.Client{Timeout: 10 * time.Second},
		verifier:    verifier,
		staticKeys:  staticKeys,
		staticSpecs: staticSpecs,
		issuer:      issuer,
		interval:    interval,
		logger:      logger,
	}, nil
}

// FetchOnce fetches and parses the JWKS document and, on success, swaps it
// (merged with staticKeys) into the Verifier. It returns an error on any
// fetch/parse/empty-result failure WITHOUT touching the Verifier's current
// key set — the caller decides whether that failure is fatal (fail-closed at
// startup) or tolerable (a background Run tick, which logs and continues).
func (p *JWKSPoller) FetchOnce(ctx context.Context) error {
	keys, algs, skipped, err := p.fetch(ctx)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		p.logger.Warn("gateway: JWKS entry skipped", "reason", s)
	}
	merged := make(map[string]crypto.PublicKey, len(keys)+len(p.staticKeys))
	for kid, k := range keys {
		merged[kid] = k
	}
	// staticKeys layered last: an operator-configured key (dev/break-glass)
	// always wins over a same-kid collision from the JWKS response, though a
	// real IdP has no reason to publish the "dev" kid.
	for kid, k := range p.staticKeys {
		merged[kid] = k
	}

	prevInfo := p.verifier.Info()
	now := time.Now()
	info := make(map[string]KeyInfo, len(merged))
	for kid := range merged {
		spec := BindingSpec{Mode: ModeOpaque, Issuer: p.issuer}
		source := "jwks"
		if _, isStatic := p.staticKeys[kid]; isStatic {
			source = "static"
			spec = p.staticSpecs[kid]
		}
		addedAt := now
		if pi, ok := prevInfo[kid]; ok && !pi.AddedAt.IsZero() {
			addedAt = pi.AddedAt
		}
		info[kid] = KeyInfo{Source: source, Alg: algs[kid], AddedAt: addedAt, Spec: spec}
	}
	if keySetChanged(prevInfo, merged) {
		p.swaps.Add(1)
	}
	if err := p.verifier.SetKeysWithInfo(merged, info); err != nil {
		return fmt.Errorf("gateway: JWKS refresh produced an invalid binding spec: %w", err)
	}
	p.lastPollAt.Store(now.UnixNano())
	return nil
}

// keySetChanged reports whether merged's kid set differs from prevInfo's —
// an add or a remove, not a same-kid value refresh (a rotated key under the
// same kid isn't a "swap" by this definition; a genuinely different kid set
// is).
func keySetChanged(prevInfo map[string]KeyInfo, merged map[string]crypto.PublicKey) bool {
	if len(prevInfo) != len(merged) {
		return true
	}
	for kid := range merged {
		if _, ok := prevInfo[kid]; !ok {
			return true
		}
	}
	return false
}

// LastPollAt returns the last successful JWKS fetch time, or the zero Time if
// none has succeeded yet.
func (p *JWKSPoller) LastPollAt() time.Time {
	ns := p.lastPollAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Swaps returns the count of fetches whose resulting trusted kid set
// differed from the prior one (see keySetChanged).
func (p *JWKSPoller) Swaps() uint64 {
	return p.swaps.Load()
}

// Run polls at the configured interval until ctx is done. A failed poll is
// logged and does not stop the loop (fail-safe — see the type doc). Call
// FetchOnce once, synchronously, before Run for the fail-closed startup gate;
// Run itself never blocks the caller past its first tick.
func (p *JWKSPoller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.FetchOnce(ctx); err != nil {
				p.logger.Error("gateway: JWKS refresh failed; keeping the last trusted key set", "url", p.url, "error", err)
			}
		}
	}
}

func (p *JWKSPoller) fetch(ctx context.Context) (keys map[string]crypto.PublicKey, algs map[string]string, skipped []string, err error) {
	return FetchJWKS(ctx, p.url, p.client)
}

// defaultJWKSClient is used by FetchJWKS when client is nil — a bounded
// timeout so a one-shot startup fetch (e.g. cmd/gateway's fail-closed initial
// load) cannot hang indefinitely against an unresponsive endpoint.
var defaultJWKSClient = &http.Client{Timeout: 10 * time.Second}

// FetchJWKS fetches url and parses it as a JWKS document (ParseJWKS), bounded
// to maxJWKSBodyBytes. client may be nil to use a default 10s-timeout client.
// algs carries each surviving kid's advisory `alg` member (jwksAlgs) for the
// jwks health block's provenance display — best-effort, never a trust input.
// Exposed at package level so both JWKSPoller and a one-shot caller (e.g. an
// initial fail-closed startup fetch) share one fetch implementation.
func FetchJWKS(ctx context.Context, url string, client *http.Client) (keys map[string]crypto.PublicKey, algs map[string]string, skipped []string, err error) {
	if client == nil {
		client = defaultJWKSClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build JWKS request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("fetch JWKS: unexpected status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBodyBytes+1))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read JWKS response: %w", err)
	}
	if len(body) > maxJWKSBodyBytes {
		return nil, nil, nil, fmt.Errorf("JWKS response exceeds %d bytes", maxJWKSBodyBytes)
	}

	keys, skipped, err = ParseJWKS(body)
	if err != nil {
		return nil, nil, skipped, err
	}
	return keys, jwksAlgs(body), skipped, nil
}
