package pkgmgr

import (
	"fmt"
)

// reservedBucketAliases maps a short, provision-time alias to the canonical
// NATS KV bucket a package lens must target. A provisioned bucket is keyed
// by its canonical name (e.g. the auth plane's "capability-kv"); the short
// alias ("capability") names the same plane in operator-facing copy but is
// NOT a real bucket. Bootstrap translates the alias to the canonical name for
// the primordial lenses, but a package lens's declared Bucket is consumed
// verbatim by the Refractor nats-kv adapter, which auto-creates whatever name
// it is given. A lens declaring the alias would therefore project into a
// phantom bucket that no reader (the capability authorizer, the auth-plane
// resurrection guard) consults — silent mis-targeting of the authorization
// surface. Install rejects the alias so the footgun fails closed.
var reservedBucketAliases = map[string]string{
	"capability": "capability-kv",
}

// validateLensBuckets rejects any lens whose declared Bucket is a reserved
// short alias of a provisioned bucket, directing the author to the canonical
// name. It is a pure function (no I/O) so it runs before any KV operation and
// is unit-testable without a live substrate.
func (def Definition) validateLensBuckets() error {
	for idx, l := range def.Lenses {
		if canonical, reserved := reservedBucketAliases[l.Bucket]; reserved {
			return fmt.Errorf(
				"pkgmgr: Lens[%d] %q declares Bucket %q, which is a reserved alias of the provisioned bucket %q — use %q so the lens targets the real auth-plane bucket (the alias auto-creates a phantom bucket no reader consults)",
				idx, l.CanonicalName, l.Bucket, canonical, canonical)
		}
	}
	return nil
}

// validateLensAdapters checks that each lens carries the fields required by
// its declared adapter. It is a pure function and runs before any KV
// operation.
func (def Definition) validateLensAdapters() error {
	for idx, l := range def.Lenses {
		switch l.Adapter {
		case "", "nats-kv":
			if l.Bucket == "" {
				return fmt.Errorf("pkgmgr: Lens[%d] %q (nats-kv): Bucket is required", idx, l.CanonicalName)
			}
		case "postgres":
			// DSN is no longer required: a package declares posture + columns,
			// and Refractor resolves an empty DSN from REFRACTOR_PG_DSN at
			// activation (mirroring the bootstrap contract_view lens). Table is
			// required for a plain/protected lens, but a GrantTable lens defaults
			// it to the shared actor_read_grants table at activation.
			if l.Table == "" && !l.GrantTable {
				return fmt.Errorf("pkgmgr: Lens[%d] %q (postgres): Table required", idx, l.CanonicalName)
			}
		default:
			return fmt.Errorf("pkgmgr: Lens[%d] %q: unknown Adapter %q (must be \"nats-kv\" or \"postgres\")", idx, l.CanonicalName, l.Adapter)
		}
	}
	return nil
}

// validateLensReadPath rejects an incoherent read-path-authorization posture on
// a lens before any KV operation, mirroring the fail-closed checks Refractor's
// translateSpec applies at activation (Contract #6 §6.14, D1.3) so a malformed
// declaration is caught at build/install time rather than silently dropped — a
// dropped posture would world-publish a model the author believed protected, or
// scatter the read-auth source of truth onto a regular bucket. Pure (no I/O).
func (def Definition) validateLensReadPath() error {
	for idx, l := range def.Lenses {
		hasPosture := l.Protected || l.Public || l.GrantTable || len(l.Columns) > 0 || len(l.SecureColumns) > 0
		if hasPosture && l.Adapter != "postgres" {
			return fmt.Errorf(
				"pkgmgr: Lens[%d] %q declares a read-path posture (protected/public/grantTable/columns/secureColumns) but its Adapter is %q — RLS and the shared actor_read_grants table are Postgres concepts; a NATS-KV target has no row-level enforcement (Contract #6 §6.14)",
				idx, l.CanonicalName, l.Adapter)
		}
		if l.Protected && l.Public {
			return fmt.Errorf("pkgmgr: Lens[%d] %q cannot be both Protected and Public (Contract #6 §6.14)", idx, l.CanonicalName)
		}
		if l.Protected && l.GrantTable {
			return fmt.Errorf("pkgmgr: Lens[%d] %q: a GrantTable lens is not a protected business model — set neither Protected nor Public (Contract #6 §6.14)", idx, l.CanonicalName)
		}
		if l.Public && l.GrantTable {
			return fmt.Errorf("pkgmgr: Lens[%d] %q: a GrantTable lens is not a public business model — set neither Protected nor Public (Contract #6 §6.14)", idx, l.CanonicalName)
		}
		if l.Adapter == "postgres" && !l.Protected && !l.Public && !l.GrantTable {
			return fmt.Errorf("pkgmgr: Lens[%d] %q: a postgres lens must declare Protected, Public, or GrantTable — a postgres business read model is protected by default and undeclared posture fails closed (Contract #6 §6.14)", idx, l.CanonicalName)
		}
		if l.DiffRetraction && l.Adapter != "postgres" {
			return fmt.Errorf("pkgmgr: Lens[%d] %q: DiffRetraction is postgres-only — Refractor's translateSpec never threads it onto a nats-kv targetConfig, so it would silently no-op", idx, l.CanonicalName)
		}
		if len(l.SecureColumns) > 0 {
			// Mirror Refractor's validateSecureColumns (Contract #3 §3.10) so a
			// Secure Lens that could never activate is rejected at install time.
			// The reserved names are the platform RLS columns (the Refractor-side
			// adapter.AuthzAnchorsColumn / adapter.ProjectionSeqColumn).
			if !l.Protected {
				return fmt.Errorf("pkgmgr: Lens[%d] %q: SecureColumns require Protected — a Secure Lens projects plaintext PII and may only target an RLS-protected model (Contract #3 §3.10)", idx, l.CanonicalName)
			}
			if l.ProjectionKind != "" {
				return fmt.Errorf("pkgmgr: Lens[%d] %q: SecureColumns are supported on plain projection lenses only, not ProjectionKind %q", idx, l.CanonicalName, l.ProjectionKind)
			}
			reserved := map[string]struct{}{"authz_anchors": {}, "projection_seq": {}}
			declared := make(map[string]struct{}, len(l.Columns))
			for _, c := range l.Columns {
				declared[c.Name] = struct{}{}
			}
			keyCols := make(map[string]struct{}, len(l.IntoKey))
			for _, k := range l.IntoKey {
				keyCols[k] = struct{}{}
			}
			seen := make(map[string]struct{}, len(l.SecureColumns))
			for _, sc := range l.SecureColumns {
				if sc.Column == "" || sc.IdentityKeyColumn == "" {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: each SecureColumns entry needs both Column and IdentityKeyColumn", idx, l.CanonicalName)
				}
				if _, dup := seen[sc.Column]; dup {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: SecureColumns declares column %q twice", idx, l.CanonicalName, sc.Column)
				}
				seen[sc.Column] = struct{}{}
				if _, bad := reserved[sc.Column]; bad {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: secure column %q is a platform RLS column — decrypted data must never drive read authorization or the write guard", idx, l.CanonicalName, sc.Column)
				}
				if _, isKey := keyCols[sc.Column]; isKey {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: secure column %q is an IntoKey column — the projection key cannot be a ciphertext envelope", idx, l.CanonicalName, sc.Column)
				}
				if _, ok := declared[sc.Column]; !ok {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: secure column %q is not among the declared Columns", idx, l.CanonicalName, sc.Column)
				}
				if _, bad := reserved[sc.IdentityKeyColumn]; bad {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: IdentityKeyColumn %q is a platform RLS column", idx, l.CanonicalName, sc.IdentityKeyColumn)
				}
				if _, ok := declared[sc.IdentityKeyColumn]; !ok {
					if _, isKey := keyCols[sc.IdentityKeyColumn]; !isKey {
						return fmt.Errorf("pkgmgr: Lens[%d] %q: IdentityKeyColumn %q is not among the declared Columns or IntoKey — the adapter writes every row field as a table column", idx, l.CanonicalName, sc.IdentityKeyColumn)
					}
				}
			}
		}
	}
	return nil
}
