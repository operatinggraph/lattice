package controlauth

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/gateway/revocation"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// preflightTimeout bounds the revocation-bucket open at control-plane startup.
const wireActorVerifierTimeout = 10 * time.Second

// WireActorVerifierFromEnv builds a Fire 2 *ActorVerifier from the same
// env-var contract for every control-plane binary (Weaver/Loom/Refractor —
// component-agnostic; the actor JWT + revocation kill-switch are not
// component-scoped). Reused, not re-derived: LATTICE_CONTROL_JWT_KEYS_DIR /
// LATTICE_CONTROL_JWT_DEV_MODE / LATTICE_CONTROL_JWT_DEV_KEY_PATH load the
// SAME kind of trust root the Gateway's read path loads
// (internal/gateway/auth.LoadTrustedKeys) — "same JWT, same trust model"
// (control-plane-capability-authz-design.md §3.4(d)).
//
// Returns (nil, nil) — verification NOT configured — when no keys resolve
// (LATTICE_CONTROL_JWT_KEYS_DIR unset and dev mode off). This is the Fire 1
// default: no flag-day lockout, existing self-asserted-header deployments and
// e2e fixtures are unaffected until an operator opts in. Any other failure
// (malformed key, dev-mode enabled but the checked-in dev key won't load, or
// the token-revocation bucket won't open) is a startup error — once an
// operator opts into JWT mode it must come up correctly or not at all,
// mirroring the Gateway's own fail-closed bring-up.
//
// RR-5 (edge-lattice-full-design.md §8.1): an Edge-facing control plane serves
// an untrusted device, where a nil verifier degrades body.IdentityID to
// self-asserted. That is correct for dev but MUST NOT hold in an untrusted-Edge
// deployment. LATTICE_CONTROL_REQUIRE_ACTOR_VERIFIER=true turns the (nil, nil)
// "not configured" return into a hard startup error, so an operator running the
// untrusted-Edge posture cannot silently come up with self-asserted identity.
func WireActorVerifierFromEnv(ctx context.Context, conn *substrate.Conn, logger *slog.Logger) (*ActorVerifier, error) {
	if logger == nil {
		logger = slog.Default()
	}
	requireVerifier := os.Getenv("LATTICE_CONTROL_REQUIRE_ACTOR_VERIFIER") == "true"
	keysDir := os.Getenv("LATTICE_CONTROL_JWT_KEYS_DIR")
	devMode := os.Getenv("LATTICE_CONTROL_JWT_DEV_MODE") == "true"

	keys, specs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
		KeysDir:       keysDir,
		KeysDirIssuer: os.Getenv("LATTICE_CONTROL_JWT_ISSUER"),
		DevMode:       devMode,
		DevKeyPath:    os.Getenv("LATTICE_CONTROL_JWT_DEV_KEY_PATH"),
	}, func(msg string) { logger.Warn("controlauth: " + msg) })
	if err != nil {
		return nil, fmt.Errorf("controlauth: load trusted JWT keys: %w", err)
	}
	if len(keys) == 0 {
		if requireVerifier {
			// Untrusted-Edge posture: refuse to start self-asserted.
			return nil, fmt.Errorf("controlauth: LATTICE_CONTROL_REQUIRE_ACTOR_VERIFIER=true but no JWT trust root resolved — an untrusted-Edge control plane must not start with self-asserted identity (set LATTICE_CONTROL_JWT_KEYS_DIR or LATTICE_CONTROL_JWT_DEV_MODE)")
		}
		// Not configured — Fire 1 self-asserted-header behavior stands.
		return nil, nil
	}

	verifier, err := auth.NewVerifier(auth.Config{
		Keys:     keys,
		KeyInfo:  auth.KeyInfoFromSpecs(specs),
		Audience: os.Getenv("LATTICE_CONTROL_JWT_AUDIENCE"),
	})
	if err != nil {
		return nil, fmt.Errorf("controlauth: build JWT verifier: %w", err)
	}

	revCtx, cancel := context.WithTimeout(ctx, wireActorVerifierTimeout)
	defer cancel()
	revKV, err := conn.OpenKV(revCtx, revocation.BucketName)
	if err != nil {
		return nil, fmt.Errorf("controlauth: open token-revocation bucket: %w", err)
	}

	logger.Info("controlauth: JWT actor verification enabled", "devMode", devMode, "keysDir", keysDir != "")
	return NewActorVerifier(auth.NewAuthenticator(verifier, revocation.New(revKV))), nil
}
