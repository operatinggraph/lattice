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
// refuses to start unless at least one trusted JWT public key is configured.
// GATEWAY_JWT_KEYS_DIR points at a directory of "<kid>.pem" SubjectPublicKeyInfo
// files (a static snapshot of the deployment's IdP JWKS — full JWKS HTTP
// polling with kid-keyed rotation is a follow-up). GATEWAY_DEV_MODE=true
// ADDITIONALLY trusts the checked-in dev key (deploy/gateway-dev-key/,
// kid "dev") for local development only — mint a token with
// `gateway dev-token -sub <identityNanoID>`. A prod deployment never sets
// GATEWAY_DEV_MODE.
//
// Environment:
//
//	GATEWAY_ADDR           HTTP listen address (default: :8080)
//	NATS_URL               NATS server URL (default: nats://localhost:4222)
//	NATS_NKEY / NATS_CREDS Gateway's own NATS credential (the #75 "gateway" user)
//	GATEWAY_JWT_KEYS_DIR   directory of <kid>.pem trusted public keys (prod IdP snapshot)
//	GATEWAY_JWT_ISSUER     optional; required `iss` claim value
//	GATEWAY_JWT_AUDIENCE   optional; required `aud` claim member
//	GATEWAY_DEV_MODE       "true" to additionally trust the checked-in dev key (dev/CI only)
//	GATEWAY_DEV_KEY_PATH   override the dev public-key path (default: deploy/gateway-dev-key/dev-public.pem)
//	HEALTH_KV_BUCKET       Health KV bucket name (default: health-kv)
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
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/gateway"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/gateway/revocation"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	defaultAddr              = ":8080"
	defaultHealthBucket      = "health-kv"
	defaultDevPublicKeyPath  = "deploy/gateway-dev-key/dev-public.pem"
	defaultDevPrivateKeyPath = "deploy/gateway-dev-key/dev-private.pem"
	devKeyID                 = "dev"
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

	keys, err := loadTrustedKeys(devMode, logger)
	if err != nil {
		return fmt.Errorf("load trusted JWT keys: %w", err)
	}
	if len(keys) == 0 {
		return errors.New("no trusted JWT keys configured — refusing to start the external write surface " +
			"(set GATEWAY_JWT_KEYS_DIR to an IdP public-key snapshot, or GATEWAY_DEV_MODE=true for local dev)")
	}

	verifier, err := auth.NewVerifier(auth.Config{
		Keys:     keys,
		Issuer:   os.Getenv("GATEWAY_JWT_ISSUER"),
		Audience: os.Getenv("GATEWAY_JWT_AUDIENCE"),
	})
	if err != nil {
		return fmt.Errorf("build JWT verifier: %w", err)
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

	// The revocation kill-switch is best-effort: a deployment that has not yet
	// provisioned the token-revocation bucket runs with verification-only auth
	// (auth.NewAuthenticator tolerates a nil checker), same posture as D1.2's
	// read boundary.
	var revChecker auth.RevocationChecker
	if revKV, err := conn.OpenKV(context.Background(), revocation.BucketName); err != nil {
		logger.Warn("token-revocation bucket not available; revocation kill-switch disabled", "error", err)
	} else {
		revChecker = revocation.New(revKV)
	}
	authn := auth.NewAuthenticator(verifier, revChecker)

	rawInstance, err := substrate.NewNanoID()
	if err != nil {
		return fmt.Errorf("generate instance id: %w", err)
	}
	instance := "gw-" + rawInstance

	metrics := &gateway.Metrics{}
	srv := gateway.NewServer(authn, conn, metrics, logger)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hb := gateway.NewHeartbeater(conn, envOrDefault("HEALTH_KV_BUCKET", defaultHealthBucket), instance, metrics, logger)
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
		keys[devKeyID] = pub
		logger.Warn("GATEWAY_DEV_MODE=true — the checked-in dev signing key is trusted; NEVER set this in production",
			"kid", devKeyID, "path", devKeyPath)
	}

	return keys, nil
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

	raw, err := os.ReadFile(*keyPath)
	if err != nil {
		return fmt.Errorf("read dev key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return fmt.Errorf("no PEM block in %s", *keyPath)
	}
	privAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse dev private key: %w", err)
	}

	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   *sub,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(*ttl)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = devKeyID
	signed, err := tok.SignedString(privAny)
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
