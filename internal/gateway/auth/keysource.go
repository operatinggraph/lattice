package auth

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultDevPublicKeyPath is the checked-in dev signing key's public half
// (deploy/gateway-dev-key/), shared by every process that trusts it in dev
// mode — the Gateway's read path and, from Fire 2, the Weaver/Loom/Refractor
// control planes (control-plane-capability-authz-design.md).
const defaultDevPublicKeyPath = "deploy/gateway-dev-key/dev-public.pem"

// devKeyID is the dev key's kid, matching the "dev" header `gateway
// dev-token` stamps.
const devKeyID = "dev"

// KeySourceConfig configures LoadTrustedKeys — the static/dev-key trust-root
// loader every JWT-verifying process shares: one IdP trust root (Gateway's
// read path and the control planes verify the SAME signed actor JWT, "same
// JWT, same trust model" per the control-plane-capability-authz-design.md),
// loaded independently per binary since each is its own process.
type KeySourceConfig struct {
	// KeysDir, if non-empty, is a directory of <kid>.pem trusted public keys
	// (a static IdP snapshot).
	KeysDir string
	// DevMode additionally trusts the checked-in dev key at DevKeyPath (or
	// defaultDevPublicKeyPath) — local dev/CI only, never production.
	DevMode bool
	// DevKeyPath overrides the dev public-key path when DevMode is set.
	// Empty uses defaultDevPublicKeyPath.
	DevKeyPath string
}

// LoadTrustedKeys builds the kid→public-key map cfg describes: every <kid>.pem
// under KeysDir, plus the checked-in dev key under devKeyID when DevMode is
// set. warn receives the dev-mode advisory message (nil-safe: a nil warn is a
// no-op) — callers pass e.g. func(msg string) { logger.Warn(msg) }.
//
// An explicitly-configured KeysDir (cfg.KeysDir != "") that scans to ZERO
// <kid>.pem files is a startup ERROR, never a silent empty result: a caller
// who set KeysDir clearly intends a trust root to load from it, so a wrong
// extension, an empty/not-yet-synced directory, or a typo'd path must fail
// loudly — silently returning an empty map here would let a caller's
// "configured but got nothing" collapse into the same shape as "never
// configured," which for a JWT-verification trust root means quietly falling
// back to no verification at all (a 3-layer review finding, Fire 2).
func LoadTrustedKeys(cfg KeySourceConfig, warn func(msg string)) (map[string]crypto.PublicKey, error) {
	keys := make(map[string]crypto.PublicKey)

	if cfg.KeysDir != "" {
		entries, err := os.ReadDir(cfg.KeysDir)
		if err != nil {
			return nil, fmt.Errorf("read keys dir %q: %w", cfg.KeysDir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
				continue
			}
			kid := strings.TrimSuffix(e.Name(), ".pem")
			pub, err := loadPublicKeyPEM(filepath.Join(cfg.KeysDir, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("load key %q: %w", e.Name(), err)
			}
			keys[kid] = pub
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("keys dir %q contains no <kid>.pem files — refusing to silently trust nothing "+
				"(an explicitly configured trust-root directory must yield at least one key)", cfg.KeysDir)
		}
	}

	if cfg.DevMode {
		path := cfg.DevKeyPath
		if path == "" {
			path = defaultDevPublicKeyPath
		}
		pub, err := loadPublicKeyPEM(path)
		if err != nil {
			return nil, fmt.Errorf("load dev key %q: %w", path, err)
		}
		// A <kid>.pem in KeysDir named "dev.pem" would otherwise be silently
		// shadowed by the checked-in dev key below — refuse instead of
		// substituting an unexpected trust key under a name the caller
		// picked for something else.
		if _, collides := keys[devKeyID]; collides {
			return nil, fmt.Errorf("keys dir %q already defines kid %q — this collides with the reserved dev-mode "+
				"kid; rename the file or disable dev mode", cfg.KeysDir, devKeyID)
		}
		keys[devKeyID] = pub
		if warn != nil {
			warn(fmt.Sprintf("dev mode: the checked-in dev signing key (%s, kid %q) is trusted; NEVER set this in production", path, devKeyID))
		}
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
