// Command gen-dev-nkeys mints the per-component NATS NKey seeds and renders the
// Lattice transport-authorization config (deploy/nats-server.conf) that enforces
// the NATS account-level write restriction (Path A: static config + per-component
// NKey users).
//
// The permission matrix itself lives in internal/natsperm.Matrix (the single
// source of truth for each component's publish allow/deny set); this tool is a
// thin renderer — it mints/reuses the seed files (deploy/nkeys/<component>.nk)
// and writes the server config that references their public keys via
// natsperm.RenderConf. Run it after editing the matrix (e.g. adding a
// component):
//
//	go run ./deploy/gen-dev-nkeys
//
// An existing seed file is REUSED, not rotated — the run is idempotent per
// component, so adding one new entry does not churn every other component's
// dev identity. Delete a component's deploy/nkeys/<name>.nk first to force a
// deliberate rotation of just that seed.
//
// The seeds it writes are DEV-ONLY, committed like POSTGRES_PASSWORD: lattice_dev;
// production injects real seeds via mounted secrets / Vault and never commits them.
//
// This tool also mints/reuses the auth-callout responder's two ACCOUNT/CURVE
// key pairs (per-identity-nats-subscribe-acl-design.md §3.1/§7 — xkey payload
// encryption is enabled from day one, not a deferred hardening pass):
// deploy/nkeys/auth-callout-issuer.nk (signs issued user JWTs + the outer
// AuthorizationResponseClaims envelope) and deploy/nkeys/auth-callout-xkey.nk
// (seals every request/response, internal/gateway/natsauth). Both are
// structurally different NKey kinds from the per-component USER seeds above,
// so they are minted by their own idempotent-load functions rather than
// folded into the matrix loop.
//
// The rendered config + committed seeds are exercised end-to-end by
// internal/natsperm (the offline conformance proof of the matrix).
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nats-io/nkeys"

	"github.com/operatinggraph/lattice/internal/natsperm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-dev-nkeys:", err)
		os.Exit(1)
	}
}

func run() error {
	deployDir, err := deployRoot()
	if err != nil {
		return err
	}
	nkeysDir := filepath.Join(deployDir, "nkeys")
	if err := os.MkdirAll(nkeysDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", nkeysDir, err)
	}

	pubKeys := make(map[string]string, len(natsperm.Matrix))
	for _, c := range natsperm.Matrix {
		seedPath := filepath.Join(nkeysDir, c.Name+".nk")

		// Idempotent by component: an existing seed file is REUSED, not
		// rotated. Minting a fresh keypair for every component on every run
		// (the original behavior) rotates every OTHER component's dev
		// identity as a side effect of adding one new component — a
		// disruptive, unreviewable diff. Delete the seed file to force a
		// deliberate rotation for that one component.
		if existing, err := os.ReadFile(seedPath); err == nil {
			kp, err := nkeys.FromSeed(bytes.TrimSpace(existing))
			if err != nil {
				return fmt.Errorf("parse existing seed %s: %w", seedPath, err)
			}
			pub, err := kp.PublicKey()
			if err != nil {
				return fmt.Errorf("public key for existing %s: %w", c.Name, err)
			}
			pubKeys[c.Name] = pub
			continue
		}

		kp, err := nkeys.CreateUser()
		if err != nil {
			return fmt.Errorf("create nkey for %s: %w", c.Name, err)
		}
		seed, err := kp.Seed()
		if err != nil {
			return fmt.Errorf("seed for %s: %w", c.Name, err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			return fmt.Errorf("public key for %s: %w", c.Name, err)
		}
		if err := os.WriteFile(seedPath, append(seed, '\n'), 0o600); err != nil {
			return fmt.Errorf("write seed %s: %w", seedPath, err)
		}
		pubKeys[c.Name] = pub
	}

	calloutIssuerPub, err := loadOrCreateAccountSeed(filepath.Join(nkeysDir, authCalloutIssuerSeedFile))
	if err != nil {
		return fmt.Errorf("auth-callout issuer key: %w", err)
	}
	calloutXkeyPub, err := loadOrCreateXkeySeed(filepath.Join(nkeysDir, authCalloutXkeySeedFile))
	if err != nil {
		return fmt.Errorf("auth-callout xkey: %w", err)
	}

	conf := natsperm.RenderConf(pubKeys, calloutIssuerPub, calloutXkeyPub)
	confPath := filepath.Join(deployDir, "nats-server.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		return fmt.Errorf("write conf %s: %w", confPath, err)
	}
	fmt.Printf("wrote %d seeds to %s and %s\n", len(natsperm.Matrix), nkeysDir, confPath)
	return nil
}

// deployRoot locates the deploy/ directory relative to this source file so the
// tool works regardless of the caller's working directory.
func deployRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Walk up to the repo root (the dir containing go.mod), then into deploy/.
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "deploy"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod above %s", wd)
		}
		dir = parent
	}
}

// authCalloutIssuerSeedFile is the auth-callout responder's ACCOUNT key pair
// (per-identity-nats-subscribe-acl-design.md §3.1) — signs both the issued
// per-connection user JWT and the outer AuthorizationResponseClaims envelope
// (internal/gateway/natsauth). Distinct from the per-component USER nkeys
// above: `nkeys.IsValidPublicAccountKey` requires an "A"-prefixed key, which
// an ordinary component seed is not.
const authCalloutIssuerSeedFile = "auth-callout-issuer.nk"

// authCalloutXkeySeedFile is the auth-callout responder's CURVE key pair
// (design §3.1a/§7) — seals every callout request/response so the bearer
// token never crosses the server→responder leg in cleartext, enabled from
// day one rather than deferred. A structurally different NKey kind again
// (nkeys.CreateCurveKeys, X25519 — not the Ed25519 ACCOUNT/USER kinds above).
const authCalloutXkeySeedFile = "auth-callout-xkey.nk"

// loadOrCreateAccountSeed mirrors the per-component idempotent-load loop in
// run() (an existing seed is REUSED, never rotated) for the one ACCOUNT-type
// key this tool also mints. Kept as its own function rather than folded into
// the matrix loop above: an account key pair is a structurally different
// NKey kind (nkeys.CreateAccount, not CreateUser) with no `component` /
// desc / permission fields to render as a `users[]` entry.
func loadOrCreateAccountSeed(seedPath string) (pub string, err error) {
	if existing, err := os.ReadFile(seedPath); err == nil {
		kp, err := nkeys.FromSeed(bytes.TrimSpace(existing))
		if err != nil {
			return "", fmt.Errorf("parse existing seed %s: %w", seedPath, err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			return "", fmt.Errorf("public key for existing %s: %w", seedPath, err)
		}
		// A parseable-but-wrong-kind seed (e.g. a component USER seed
		// mistakenly copied to this path) would otherwise render a broken
		// auth_callout.issuer into the conf silently — the server itself
		// rejects it at parse time (parseAuthCallout requires a public
		// ACCOUNT key), but that failure surfaces two layers downstream of
		// where it's actually actionable. Catch it here instead (an
		// adversarial-pass finding, LOW).
		if !nkeys.IsValidPublicAccountKey(pub) {
			return "", fmt.Errorf("existing seed %s is not an ACCOUNT key (got a key of a different kind) — "+
				"delete it to mint a fresh one", seedPath)
		}
		return pub, nil
	}
	kp, err := nkeys.CreateAccount()
	if err != nil {
		return "", fmt.Errorf("create account nkey: %w", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		return "", fmt.Errorf("seed: %w", err)
	}
	pub, err = kp.PublicKey()
	if err != nil {
		return "", fmt.Errorf("public key: %w", err)
	}
	if err := os.WriteFile(seedPath, append(seed, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write seed %s: %w", seedPath, err)
	}
	return pub, nil
}

// loadOrCreateXkeySeed is loadOrCreateAccountSeed's CURVE-key analogue — same
// idempotent-load/reuse contract, but nkeys.CreateCurveKeys/IsValidPublicCurveKey
// (X25519) rather than CreateAccount/IsValidPublicAccountKey (Ed25519): the
// two key kinds are neither interchangeable nor cross-parseable, so this
// cannot share loadOrCreateAccountSeed's body.
func loadOrCreateXkeySeed(seedPath string) (pub string, err error) {
	if existing, err := os.ReadFile(seedPath); err == nil {
		kp, err := nkeys.FromCurveSeed(bytes.TrimSpace(existing))
		if err != nil {
			return "", fmt.Errorf("parse existing seed %s: %w", seedPath, err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			return "", fmt.Errorf("public key for existing %s: %w", seedPath, err)
		}
		if !nkeys.IsValidPublicCurveKey(pub) {
			return "", fmt.Errorf("existing seed %s is not a CURVE key (got a key of a different kind) — "+
				"delete it to mint a fresh one", seedPath)
		}
		return pub, nil
	}
	kp, err := nkeys.CreateCurveKeys()
	if err != nil {
		return "", fmt.Errorf("create curve nkey: %w", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		return "", fmt.Errorf("seed: %w", err)
	}
	pub, err = kp.PublicKey()
	if err != nil {
		return "", fmt.Errorf("public key: %w", err)
	}
	if err := os.WriteFile(seedPath, append(seed, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write seed %s: %w", seedPath, err)
	}
	return pub, nil
}
