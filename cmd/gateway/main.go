// cmd/gateway — the external write-path translator (Gateway Fire 1).
//
// The Gateway terminates external HTTP requests, verifies the caller's
// IdP-signed JWT with the already-built internal/gateway/auth Authenticator,
// and stamps the verified actor into the operation envelope before
// publishing to core-operations. It is the authentication seam that closes
// actor impersonation — see
// _bmad-output/implementation-artifacts/gateway-external-trust-boundary-design.md
// and docs/components/gateway.md.
//
// FAIL-CLOSED KEY LOADING (design §6 / F3): the external write surface
// refuses to start unless at least one trusted JWT public key is configured
// — from a static snapshot (GATEWAY_JWT_KEYS_DIR), a live JWKS endpoint
// (GATEWAY_JWKS_URL), or the dev key (GATEWAY_DEV_MODE), in any combination.
// GATEWAY_DEV_MODE=true ADDITIONALLY trusts the checked-in dev key
// (deploy/gateway-dev-key/, kid "dev") for local development only — mint a
// token with `gateway dev-token -sub <identityNanoID>`. A prod deployment
// never sets GATEWAY_DEV_MODE.
//
// JWKS LIVE ROTATION (design §8 Fire 2 remainder): when GATEWAY_JWKS_URL is
// set, the Gateway fetches it once at startup (fail-closed: a failed initial
// fetch with no other keys configured refuses to start) and then polls it in
// the background (auth.JWKSPoller), hot-swapping the trusted kid→key set on
// each successful fetch — a rotated IdP signing key is picked up without a
// restart. A JWKS URL must be https:// unless GATEWAY_DEV_MODE=true (mirrors
// the dev-key profile gate: a plaintext-HTTP key source is a dev-only
// posture). Static-dir/dev keys are always merged into every poll — a JWKS
// response can add/retire IdP keys but can never un-trust an
// operator-configured key.
//
// TOKEN-REVOCATION KILL-SWITCH (gateway-token-revocation-activation-design.md
// Fire 1): the Gateway requires the token-revocation bucket to open AND its
// own events.gateway.> materializer consumer to attach before the HTTP
// listener binds — a failure at either refuses to start (no more silent
// verification-only downgrade). RevokeActor/UnrevokeActor ops (identity-domain)
// outbox gateway.actorRevoked/actorUnrevoked, which the materializer folds
// into the local bucket revocation.Checker reads per request.
//
// Environment:
//
//	GATEWAY_ADDR              HTTP listen address (default: :8080)
//	BOOTSTRAP_JSON_PATH       primordial-ID file (default: ./lattice.bootstrap.json) — supplies the
//	                          Gateway's own service-actor identity for the auto-provisioning pre-flight
//	NATS_URL                  NATS server URL (default: nats://localhost:4222)
//	NATS_NKEY / NATS_CREDS    Gateway's own NATS credential (the #75 "gateway" user)
//	GATEWAY_JWT_KEYS_DIR      directory of <kid>.pem trusted public keys (static IdP snapshot)
//	GATEWAY_JWKS_URL          IdP JWKS endpoint (https://…) — polled for kid-keyed key rotation
//	GATEWAY_JWKS_POLL_INTERVAL poll interval (default 5m, floor 30s; Go duration syntax e.g. "2m")
//	GATEWAY_JWT_ISSUER        optional; required `iss` claim value
//	GATEWAY_JWT_AUDIENCE      optional; required `aud` claim member
//	GATEWAY_DEV_MODE          "true" to additionally trust the checked-in dev key + allow a non-https JWKS URL (dev/CI only)
//	GATEWAY_DEV_KEY_PATH      override the dev public-key path (default: deploy/gateway-dev-key/dev-public.pem)
//	HEALTH_KV_BUCKET          Health KV bucket name (default: health-kv)
//	GATEWAY_PG_DSN            read-path front (Fire 3): a non-superuser, SELECT-only Postgres DSN
//	                          (`make provision-gateway-role`); unset ⇒ every GET /v1/<name> 502s
//	GATEWAY_READ_MODELS_DIR   directory of <name>.sql files, each a fixed SELECT with no
//	                          caller-supplied predicate (RLS scopes rows); name becomes the
//	                          GET /v1/<name> path segment. Unset/empty ⇒ no read-model routes.
//	GATEWAY_CORS_ORIGINS      comma-separated exact Origin values (scheme+host+port) allowed to
//	                          call POST /v1/operations cross-origin — the browser-direct write
//	                          topology (real-actor-write-auth-e2e-design.md §3.1). Unset/empty ⇒
//	                          CORS off; a cross-origin browser call is refused by the browser.
//
// Logs to stderr in slog text format.
package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/gateway"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/gateway/revocation"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	defaultAddr              = ":8080"
	defaultHealthBucket      = "health-kv"
	defaultDevPublicKeyPath  = "deploy/gateway-dev-key/dev-public.pem"
	defaultDevPrivateKeyPath = "deploy/gateway-dev-key/dev-private.pem"
	initialJWKSFetchTimeout  = 15 * time.Second
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "dev-token" {
		if err := runDevToken(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "gateway dev-token:", err)
			os.Exit(1)
		}
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("gateway exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := envOrDefault("GATEWAY_ADDR", defaultAddr)
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	devMode := envOrDefault("GATEWAY_DEV_MODE", "false") == "true"
	jwksURL := os.Getenv("GATEWAY_JWKS_URL")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	staticKeys, err := loadTrustedKeys(devMode, logger)
	if err != nil {
		return fmt.Errorf("load trusted JWT keys: %w", err)
	}

	keys := make(map[string]crypto.PublicKey, len(staticKeys))
	for kid, k := range staticKeys {
		keys[kid] = k
	}

	if jwksURL != "" {
		if err := validateJWKSURL(jwksURL, devMode); err != nil {
			return err
		}
		fetchCtx, cancel := context.WithTimeout(ctx, initialJWKSFetchTimeout)
		jwksKeys, skipped, ferr := auth.FetchJWKS(fetchCtx, jwksURL, nil)
		cancel()
		for _, s := range skipped {
			logger.Warn("gateway: JWKS entry skipped", "reason", s)
		}
		if ferr != nil {
			if len(staticKeys) == 0 {
				return fmt.Errorf("initial JWKS fetch from %q failed and no static/dev keys are configured: %w", jwksURL, ferr)
			}
			logger.Warn("initial JWKS fetch failed; starting with static/dev keys only, will retry on the poll interval",
				"url", jwksURL, "error", ferr)
		} else {
			for kid, k := range jwksKeys {
				keys[kid] = k
			}
		}
	}

	if len(keys) == 0 {
		return errors.New("no trusted JWT keys configured — refusing to start the external write surface " +
			"(set GATEWAY_JWT_KEYS_DIR to an IdP public-key snapshot, GATEWAY_JWKS_URL to a live IdP JWKS endpoint, " +
			"or GATEWAY_DEV_MODE=true for local dev)")
	}

	verifier, err := auth.NewVerifier(auth.Config{
		Keys:     keys,
		Issuer:   os.Getenv("GATEWAY_JWT_ISSUER"),
		Audience: os.Getenv("GATEWAY_JWT_AUDIENCE"),
	})
	if err != nil {
		return fmt.Errorf("build JWT verifier: %w", err)
	}

	if jwksURL != "" {
		interval, ierr := parsePollInterval(os.Getenv("GATEWAY_JWKS_POLL_INTERVAL"))
		if ierr != nil {
			return fmt.Errorf("parse GATEWAY_JWKS_POLL_INTERVAL: %w", ierr)
		}
		poller := auth.NewJWKSPoller(jwksURL, verifier, staticKeys, interval, logger)
		go poller.Run(ctx)
		logger.Info("JWKS polling enabled", "url", jwksURL, "interval", interval)
	}

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "gateway",
		MaxReconnects: -1,
		ReconnectWait: 2 * time.Second,
		NKeySeedFile:  envOrDefault("NATS_NKEY", ""),
		CredsFile:     envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer conn.Close()
	logger.Info("connected to NATS", "natsURL", natsURL)

	// The revocation kill-switch is now REQUIRED, fail-closed bring-up (design
	// §2.4): a deployment that cannot open its own read handle on the bucket
	// refuses to start rather than silently downgrading to verification-only.
	revKV, err := conn.OpenKV(context.Background(), revocation.BucketName)
	if err != nil {
		return fmt.Errorf("open token-revocation bucket: %w", err)
	}
	authn := auth.NewAuthenticator(verifier, revocation.New(revKV))

	rawInstance, err := substrate.NewNanoID()
	if err != nil {
		return fmt.Errorf("generate instance id: %w", err)
	}
	instance := "gw-" + rawInstance

	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		return fmt.Errorf("load primordial IDs from %s: %w", bootstrapJSONPath, err)
	}

	metrics := &gateway.Metrics{}
	srv := gateway.NewServer(authn, conn, metrics, logger)
	srv.ConfigureProvisioning(bootstrap.GatewayIdentityKey, "vtx.role."+pkgmgr.RoleID("identity-domain", "consumer"))
	if origins := os.Getenv("GATEWAY_CORS_ORIGINS"); origins != "" {
		srv.ConfigureCORS(strings.Split(origins, ","))
	}

	readModels, err := loadReadModels(os.Getenv("GATEWAY_READ_MODELS_DIR"))
	if err != nil {
		return fmt.Errorf("load read models: %w", err)
	}
	pgPool, err := connectReadModelPool(os.Getenv("GATEWAY_PG_DSN"), logger)
	if err != nil {
		return fmt.Errorf("connect read-model Postgres pool: %w", err)
	}
	if pgPool != nil {
		defer pgPool.Close()
		srv.ConfigureReadModels(pgPool, readModels)
	} else {
		srv.ConfigureReadModels(nil, readModels)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	hb := gateway.NewHeartbeater(conn, envOrDefault("HEALTH_KV_BUCKET", defaultHealthBucket), instance, metrics, logger)

	// Attach the revocation materializer before the HTTP listener binds — a
	// failure here refuses to start (design §2.4); it never leaves the
	// checker running against an unpopulated/unbuilt bucket.
	revSup, err := gateway.StartRevocationMaterializer(ctx, conn, hb, logger)
	if err != nil {
		return fmt.Errorf("start revocation materializer: %w", err)
	}
	defer revSup.Stop()

	go hb.Run(ctx)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("gateway listening", "addr", addr, "devMode", devMode, "instance", instance)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("signal received; shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// validateJWKSURL enforces the JWKS transport profile gate: a live key
// source must be https:// (an IdP's JWKS is precisely the thing establishing
// trust — fetching it over plaintext HTTP is a MITM-key-injection surface),
// unless devMode explicitly opts into a local/plaintext JWKS fixture (mirrors
// the dev-key profile gate in loadTrustedKeys).
func validateJWKSURL(rawURL string, devMode bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse GATEWAY_JWKS_URL %q: %w", rawURL, err)
	}
	if u.Scheme != "https" && !devMode {
		return fmt.Errorf("GATEWAY_JWKS_URL %q must be https:// in prod (set GATEWAY_DEV_MODE=true to allow %q for local dev)",
			rawURL, u.Scheme)
	}
	return nil
}

// parsePollInterval parses GATEWAY_JWKS_POLL_INTERVAL. An empty string
// yields 0, which auth.NewJWKSPoller treats as "use the default."
func parsePollInterval(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return time.ParseDuration(raw)
}

// loadTrustedKeys builds the kid→public-key map the Verifier trusts. See the
// package doc for the fail-closed profile-gating rule.
func loadTrustedKeys(devMode bool, logger *slog.Logger) (map[string]crypto.PublicKey, error) {
	keys := make(map[string]crypto.PublicKey)

	if dir := os.Getenv("GATEWAY_JWT_KEYS_DIR"); dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read GATEWAY_JWT_KEYS_DIR %q: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
				continue
			}
			kid := strings.TrimSuffix(e.Name(), ".pem")
			pub, err := loadPublicKeyPEM(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("load key %q: %w", e.Name(), err)
			}
			keys[kid] = pub
		}
	}

	if devMode {
		devKeyPath := envOrDefault("GATEWAY_DEV_KEY_PATH", defaultDevPublicKeyPath)
		pub, err := loadPublicKeyPEM(devKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load dev key %q: %w", devKeyPath, err)
		}
		keys[auth.DevKeyID] = pub
		logger.Warn("GATEWAY_DEV_MODE=true — the checked-in dev signing key is trusted; NEVER set this in production",
			"kid", auth.DevKeyID, "path", devKeyPath)
	}

	return keys, nil
}

// loadReadModels builds the Fire 3 read-model registry from a directory of
// <name>.sql files (mirrors loadTrustedKeys' <kid>.pem idiom): each file's
// base name (minus ".sql") becomes the GET /v1/<name> path segment, and its
// trimmed content is the fixed SELECT that model runs — no caller-supplied
// predicate; Postgres RLS scopes every row (Contract #6 §6.14). dir == ""
// yields an empty registry (no read-model routes), not an error — the
// write-path surface has no dependency on this configuration existing.
func loadReadModels(dir string) (map[string]gateway.ReadModel, error) {
	models := make(map[string]gateway.ReadModel)
	if dir == "" {
		return models, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read GATEWAY_READ_MODELS_DIR %q: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".sql")
		if !gateway.ValidReadModelName(name) {
			return nil, fmt.Errorf("read-model file %q: %q is not a valid read-model name", e.Name(), name)
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", e.Name(), err)
		}
		query := strings.TrimSpace(string(raw))
		if query == "" {
			return nil, fmt.Errorf("read-model file %q is empty", e.Name())
		}
		models[name] = gateway.ReadModel{Query: query}
	}
	return models, nil
}

// connectReadModelPool opens the Fire 3 read-model Postgres pool. dsn == ""
// returns (nil, nil) — every GET /v1/<name> then 502s "read model
// unavailable" rather than the Gateway refusing to start (the read front is
// additive; the write path has no Postgres dependency). pgxpool.New is lazy
// (no connection yet); a ping failure is logged, not fatal, so the pool can
// recover once Postgres becomes reachable — mirrors loftspace-app's startup
// posture for the same read-model DSN pattern.
func connectReadModelPool(dsn string, logger *slog.Logger) (*pgxpool.Pool, error) {
	if strings.TrimSpace(dsn) == "" {
		logger.Warn("GATEWAY_PG_DSN unset; GET /v1/<readmodel> will report the read model as unavailable")
		return nil, nil
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		logger.Warn("read-model Postgres pool configured but unreachable at startup; GET /v1/<readmodel> will 502 until Postgres is reachable", "error", err)
	} else {
		logger.Info("read-model Postgres pool configured")
	}
	return pool, nil
}

func loadPublicKeyPEM(path string) (crypto.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}

// runDevToken implements the `gateway dev-token` subcommand: mints an RS256
// JWT signed by the checked-in DEV-ONLY private key (deploy/gateway-dev-key/),
// for exercising a Gateway running with GATEWAY_DEV_MODE=true. Never usable
// against a prod Gateway (the dev key never loads there — see loadTrustedKeys).
func runDevToken(args []string) error {
	fs := flag.NewFlagSet("dev-token", flag.ExitOnError)
	sub := fs.String("sub", "", "identity NanoID to mint a token for (required; becomes vtx.identity.<sub>)")
	ttl := fs.Duration("ttl", 15*time.Minute, "token time-to-live")
	keyPath := fs.String("key", defaultDevPrivateKeyPath, "path to the dev RSA private key (PKCS8 PEM)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sub) == "" {
		return errors.New("-sub is required")
	}

	priv, err := auth.LoadDevSigningKey(*keyPath)
	if err != nil {
		return fmt.Errorf("load dev private key: %w", err)
	}

	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   *sub,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(*ttl)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = auth.DevKeyID
	signed, err := tok.SignedString(priv)
	if err != nil {
		return fmt.Errorf("sign dev token: %w", err)
	}
	fmt.Println(signed)
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
