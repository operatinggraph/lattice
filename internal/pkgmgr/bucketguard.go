package pkgmgr

import (
	"fmt"
	"slices"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// personalActorKeyField mirrors internal/refractor/adapter.PersonalActorKeyField
// ("__actor") — the reserved nats-subject key field every Personal Lens
// IntoKey must include. Not imported: pkgmgr avoids a dependency on
// internal/refractor internals by convention (see LensSpec.Source's doc
// comment in definition.go).
const personalActorKeyField = "__actor"

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

// reservedBucketNames are canonical bucket names a package lens must never
// declare as its own Bucket. Each is a platform-private store (Core-KV
// itself, Health-KV self-reporting, an engine's own cursor/adjacency state,
// the Gateway's revocation set and credential-bindings set) that the
// Refractor nats-kv adapter auto-creates verbatim and a rebuild Truncate
// purges wholesale — unlike the shared platform-projection buckets packages
// legitimately target
// (weaver-targets, capability-kv, orchestration-history), these are never
// lens targets, so a mis-authored lens naming one would silently wipe
// platform state on the next rebuild. Derived from bootstrap's platform-
// bucket registry (every !LensTarget row) so a new platform-private bucket
// cannot ship without this guard picking it up automatically.
var reservedBucketNames = bootstrap.ReservedBuckets()

// validateLensBuckets rejects any lens whose declared Bucket is a reserved
// short alias of a provisioned bucket (directing the author to the canonical
// name), or the canonical name of a platform-private bucket that is never a
// lens target. It is a pure function (no I/O) so it runs before any KV
// operation and is unit-testable without a live substrate.
func (def Definition) validateLensBuckets() error {
	for idx, l := range def.Lenses {
		if canonical, reserved := reservedBucketAliases[l.Bucket]; reserved {
			return fmt.Errorf(
				"pkgmgr: Lens[%d] %q declares Bucket %q, which is a reserved alias of the provisioned bucket %q — use %q so the lens targets the real auth-plane bucket (the alias auto-creates a phantom bucket no reader consults)",
				idx, l.CanonicalName, l.Bucket, canonical, canonical)
		}
		if _, reserved := reservedBucketNames[l.Bucket]; reserved {
			return fmt.Errorf(
				"pkgmgr: Lens[%d] %q declares Bucket %q, which is a platform-private bucket, never a lens target — the nats-kv adapter would auto-create/truncate it verbatim, wiping platform state on the next rebuild",
				idx, l.CanonicalName, l.Bucket)
		}
	}
	return nil
}

// validateLensAdapters checks that each lens carries the fields required by
// its declared adapter. It is a pure function and runs before any KV
// operation.
func (def Definition) validateLensAdapters() error {
	for idx, l := range def.Lenses {
		// build.go's addCreate writes the vertex-root envelope's class as
		// l.Class, defaulting empty to "meta.lens" — and BOTH sides of the
		// destruction-readiness oracle key discovery on that exact literal:
		// Refractor's registry-probe declaredLensIDs (internal/refractor/health/
		// registry_probe.go) skips any root whose class != "meta.lens", and the
		// lens registry itself discovers lenses the identical way. A census of
		// the whole packages/ corpus found all 105 LensSpec.Class values are
		// exactly "meta.lens" or unset — nothing else is used — so any other
		// value is refused outright: it would silently drop the lens (and any
		// secure-column ciphertext it still projects) from the oracle's view
		// while changing nothing Refractor's own activation checks.
		if l.Class != "" && l.Class != "meta.lens" {
			return fmt.Errorf(
				"pkgmgr: Lens[%d] %q declares Class %q — the destruction-readiness oracle and the lens registry both key discovery on the exact literal \"meta.lens\" (empty defaults to it); any other value silently drops this lens from both while its projected data stays live",
				idx, l.CanonicalName, l.Class)
		}
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
		case "nats-subject":
			if l.SubjectPrefix == "" || l.Stream == "" {
				return fmt.Errorf("pkgmgr: Lens[%d] %q (nats-subject): SubjectPrefix and Stream are required", idx, l.CanonicalName)
			}
			if !slices.Contains(l.IntoKey, personalActorKeyField) {
				return fmt.Errorf("pkgmgr: Lens[%d] %q (nats-subject): IntoKey must include %q (the reserved actor key field)", idx, l.CanonicalName, personalActorKeyField)
			}
		default:
			return fmt.Errorf("pkgmgr: Lens[%d] %q: unknown Adapter %q (must be \"nats-kv\", \"postgres\", or \"nats-subject\")", idx, l.CanonicalName, l.Adapter)
		}
	}
	return nil
}

// reservedRLSColumnNames are the platform's own RLS columns that
// adapter.BuildProtectedTableDDL (internal/refractor/adapter/rls.go) always
// appends to a Protected read-model table, alongside whatever a package
// declares in Columns AND whatever it declares in IntoKey — BuildProtectedTableDDL
// emits the lens's key columns as real columns first, then appends these four
// with no dedup against either set: the authz set-membership column, the
// incremental-rebuild watermark, and the two soft-tombstone columns. Only a
// Protected lens's Columns/IntoKey ever reach that DDL — translatePostgresColumns
// drops a non-Protected lens's Columns before they reach a ColumnDef, and a
// GrantTable lens's fixed schema never reads either field — so a name collision
// here is unreachable for any other posture. A package that declares one of
// these as an ordinary business column or a key column installs cleanly
// (bucketguard runs before any KV write) and fails only at Refractor activation
// with Postgres 42701 duplicate column; this check moves that failure to
// install time and names the reason.
var reservedRLSColumnNames = map[string]struct{}{
	"authz_anchors":  {},
	"projection_seq": {},
	"is_deleted":     {},
	"deleted_at":     {},
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
		if l.DiffRetraction && l.Adapter != "postgres" && l.Adapter != "nats-kv" && l.Adapter != "" {
			return fmt.Errorf("pkgmgr: Lens[%d] %q: DiffRetraction requires Adapter \"postgres\" or \"nats-kv\" (got %q) — Refractor's translateSpec only threads it onto those two targetConfig shapes", idx, l.CanonicalName, l.Adapter)
		}
		if l.GrantSource != "" && !l.GrantTable {
			return fmt.Errorf("pkgmgr: Lens[%d] %q: GrantSource is meaningful only on a GrantTable lens — it names the grant_source that lens owns in the shared actor_read_grants table (Contract #6 §6.14)", idx, l.CanonicalName)
		}
		// Retraction on a grant lens reads back the SHARED actor_read_grants
		// table, so only a declared GrantSource can confine the diff to this
		// producer's own rows; an unscoped one would retract every other
		// package's grants. Mirror Refractor's translateSpec guard here so the
		// misdeclaration is caught at install time rather than at activation.
		if l.GrantTable && l.DiffRetraction && l.GrantSource == "" {
			return fmt.Errorf("pkgmgr: Lens[%d] %q: a GrantTable lens with DiffRetraction must declare GrantSource — actor_read_grants is shared across producers and retraction must be scoped to this lens's own rows (Contract #6 §6.14)", idx, l.CanonicalName)
		}
		if l.Protected {
			// A plain business Columns entry reaches BuildProtectedTableDDL exactly
			// like a SecureColumns column does — the reservation is on the CREATE
			// TABLE's column NAME, not on whether that column happens to be
			// encrypted. Checked for every Protected lens, independent of whether
			// it also declares SecureColumns.
			for _, c := range l.Columns {
				if _, bad := reservedRLSColumnNames[c.Name]; bad {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: Columns declares %q, which is a platform RLS column — Refractor's BuildProtectedTableDDL always appends authz_anchors/projection_seq/is_deleted/deleted_at to a Protected table, so this collides at Postgres activation (42701 duplicate column); rename the column", idx, l.CanonicalName, c.Name)
				}
			}
			// IntoKey collides the identical way: BuildProtectedTableDDL emits the
			// lens's key columns as real columns before appending the four platform
			// columns, with no dedup against either set.
			for _, k := range l.IntoKey {
				if _, bad := reservedRLSColumnNames[k]; bad {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: IntoKey declares %q, which is a platform RLS column — Refractor's BuildProtectedTableDDL emits key columns as real columns before appending authz_anchors/projection_seq/is_deleted/deleted_at to a Protected table, so this collides at Postgres activation (42701 duplicate column); rename the key column", idx, l.CanonicalName, k)
				}
			}
		}
		if len(l.SecureColumns) > 0 {
			// Mirror Refractor's validateSecureColumns (Contract #3 §3.10) so a
			// Secure Lens that could never activate is rejected at install time.
			// The reserved names are the platform RLS columns (the Refractor-side
			// adapter.AuthzAnchorsColumn / adapter.ProjectionSeqColumn /
			// adapter.IsDeletedColumn / adapter.DeletedAtColumn).
			if !l.Protected {
				return fmt.Errorf("pkgmgr: Lens[%d] %q: SecureColumns require Protected — a Secure Lens projects plaintext PII and may only target an RLS-protected model (Contract #3 §3.10)", idx, l.CanonicalName)
			}
			if l.ProjectionKind != "" {
				return fmt.Errorf("pkgmgr: Lens[%d] %q: SecureColumns are supported on plain projection lenses only, not ProjectionKind %q", idx, l.CanonicalName, l.ProjectionKind)
			}
			// An eventStream Source has no Core-KV vertex to project from
			// (Spec must be left empty — LensSpec.Source's own doc comment), so
			// SecureColumns is incoherent on it regardless of any oracle. It is
			// also independently invisible to key-custody tracking: Refractor's
			// CoreKVSource discovery skips an eventStream spec exactly as the
			// destruction-readiness oracle's declaredLensIDs does (both check
			// isEventStream before anything else), so an upgrade that kept
			// SecureColumns while adding this Source would strand its ciphertext
			// with neither side ever raising a signal.
			if l.Source != nil && l.Source.Kind == "eventStream" {
				return fmt.Errorf("pkgmgr: Lens[%d] %q: SecureColumns cannot combine with an eventStream Source — an event lens has no Core-KV vertex to decrypt against, and both Refractor's discovery and the destruction-readiness oracle skip an eventStream spec outright, so any secure column here would be invisible to key-custody tracking", idx, l.CanonicalName)
			}
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
				if sc.Column == "" || len(sc.HolderTypes) == 0 {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: each SecureColumns entry needs both Column and a non-empty HolderTypes — a column that names no holder type would decrypt under whatever holder a ciphertext happened to name", idx, l.CanonicalName)
				}
				if _, dup := seen[sc.Column]; dup {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: SecureColumns declares column %q twice", idx, l.CanonicalName, sc.Column)
				}
				seen[sc.Column] = struct{}{}
				if _, bad := reservedRLSColumnNames[sc.Column]; bad {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: secure column %q is a platform RLS column — decrypted data must never drive read authorization or the write guard", idx, l.CanonicalName, sc.Column)
				}
				if _, isKey := keyCols[sc.Column]; isKey {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: secure column %q is an IntoKey column — the projection key cannot be a ciphertext envelope", idx, l.CanonicalName, sc.Column)
				}
				if _, ok := declared[sc.Column]; !ok {
					return fmt.Errorf("pkgmgr: Lens[%d] %q: secure column %q is not among the declared Columns", idx, l.CanonicalName, sc.Column)
				}
				seenHolder := make(map[string]struct{}, len(sc.HolderTypes))
				for _, ht := range sc.HolderTypes {
					if !keys.IsValidTypeSegment(ht) {
						return fmt.Errorf("pkgmgr: Lens[%d] %q: secure column %q declares holder type %q, which is not a Contract #1 vertex type segment ([a-z][a-z0-9]*) — no key holder could ever match it", idx, l.CanonicalName, sc.Column, ht)
					}
					if _, dup := seenHolder[ht]; dup {
						return fmt.Errorf("pkgmgr: Lens[%d] %q: secure column %q declares holder type %q twice", idx, l.CanonicalName, sc.Column, ht)
					}
					seenHolder[ht] = struct{}{}
				}
			}
		}
	}
	return nil
}
