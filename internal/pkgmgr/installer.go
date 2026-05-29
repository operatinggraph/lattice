package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// CoreBucket is the bucket all Capability Package writes target. The installer
// uses single-bucket atomic batches; cross-bucket batches are not supported
// by the NATS atomic-batch protocol.
const CoreBucket = "core-kv"

// PackageVertexPrefix is the Contract #1 vertex prefix the installer
// uses to record an installed package. The full vertex key shape is
// `vtx.package.<NanoID>`; the canonical name is recorded as an aspect
// so list / uninstall can resolve canonical-name → NanoID.
const PackageVertexPrefix = "vtx.package."

// DefaultBatchTimeout is the wall budget for a single install / uninstall
// atomic-batch round-trip.
const DefaultBatchTimeout = 30 * time.Second

// Installer drives package install / uninstall / list. The caller wires
// it with a substrate connection + the admin actor key read from
// `lattice.bootstrap.json`.
type Installer struct {
	Conn       *substrate.Conn
	AdminActor string // The provenance `createdBy` for every aspect written.
	Now        func() time.Time

	// RoleIDs maps role canonical names to NanoIDs for grant-link
	// resolution. Callers (cmd/lattice-pkg) populate this from
	// lattice.bootstrap.json so packages whose `GrantsTo` references
	// canonical names (e.g. "operator") get the right link target.
	// Additional roles minted by a package's PreInstall hook are
	// merged in at install time. The map may be unset (nil) for tests
	// that hard-code NanoIDs in GrantsTo.
	RoleIDs map[string]string
}

// NewInstaller builds a default-configured installer.
func NewInstaller(conn *substrate.Conn, adminActor string) *Installer {
	return &Installer{
		Conn:       conn,
		AdminActor: adminActor,
		Now:        func() time.Time { return time.Now().UTC() },
	}
}

// Install applies a package Definition to Core KV.
//
// Steps:
//  1. Dependency check — Phase 1 logs/returns a warning slice (not an
//     error).
//  2. Idempotency check — read any existing package vertex with the
//     same canonical name. Same version → no-op. Different version →
//     return ErrVersionMismatch.
//  3. Construct the full op list (DDLs + aspects, Lenses + aspects,
//     Permissions + grants, package vertex + manifest aspect).
//  4. Submit one atomic batch.
//
// Returns a Result describing what happened (or what was skipped).
func (i *Installer) Install(ctx context.Context, def Definition) (*InstallResult, error) {
	if def.Name == "" {
		return nil, fmt.Errorf("pkgmgr: Definition.Name is required")
	}
	if def.Version == "" {
		return nil, fmt.Errorf("pkgmgr: Definition.Version is required")
	}
	if i.AdminActor == "" {
		return nil, fmt.Errorf("pkgmgr: AdminActor is required")
	}

	res := &InstallResult{PackageName: def.Name, PackageVersion: def.Version}

	// Pre-flight: confirm core-kv bucket exists before any KV operation.
	// If bootstrap has not run, the bucket is absent and we return a clear
	// actionable error instead of a raw NATS stream-not-found message.
	if err := i.checkCoreBucketExists(ctx); err != nil {
		return nil, err
	}

	// Step 1 — dependency warnings (warn-and-proceed; install order is the
	// operator's responsibility).
	for _, dep := range def.Depends {
		if dep == "" {
			continue
		}
		res.DependencyWarnings = append(res.DependencyWarnings,
			fmt.Sprintf("declared dependency %q not verified at install time", dep))
	}

	// Step 2 — idempotency check via the package vertex aspect.
	existing, err := i.findInstalledPackage(ctx, def.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Version == def.Version {
			res.Skipped = true
			res.Reason = fmt.Sprintf("package %q version %q already installed", def.Name, def.Version)
			res.PackageKey = existing.Key
			return res, nil
		}
		return nil, fmt.Errorf("%w: installed=%s requested=%s", ErrVersionMismatch, existing.Version, def.Version)
	}

	// Step 2.5 — optional PreInstall hook (per-package Go-side seeding
	// that must complete before the atomic batch — e.g. identity-domain
	// seeds its 3 user-facing roles so subsequent grant links can
	// reference them). Hooks must be idempotent.
	if def.PreInstall != nil {
		extraRoles, err := def.PreInstall(ctx, i.Conn, i.AdminActor)
		if err != nil {
			return nil, fmt.Errorf("pkgmgr: PreInstall %s: %w", def.Name, err)
		}
		if len(extraRoles) > 0 {
			if i.RoleIDs == nil {
				i.RoleIDs = map[string]string{}
			}
			for k, v := range extraRoles {
				i.RoleIDs[k] = v
			}
		}
		res.PreInstallRan = true
	}

	// Resolve any unresolved canonical names in GrantsTo via i.RoleIDs.
	def = i.resolveGrants(def)

	// Validate all GrantsTo entries resolved to valid NanoIDs. Any
	// remaining canonical name (non-NanoID) means the bootstrap JSON is
	// missing the role's primordialID or the PreInstall hook did not seed
	// it. A dangling grant link would be written silently and cause
	// PermissionDenied at runtime with no helpful diagnostic.
	for idx, p := range def.Permissions {
		for _, g := range p.GrantsTo {
			if !substrate.IsValidNanoID(g) {
				return nil, fmt.Errorf("pkgmgr: Permission[%d] %q: GrantsTo entry %q is not a valid NanoID — role may not be installed or bootstrap JSON is missing the role ID", idx, p.OperationType, g)
			}
		}
	}

	// Step 3 — build the atomic batch.
	pkgNanoID, err := substrate.NewNanoID()
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: nanoid: %w", err)
	}
	pkgKey := PackageVertexPrefix + pkgNanoID
	res.PackageKey = pkgKey

	ddlNanoIDs := make([]string, len(def.DDLs))
	lensNanoIDs := make([]string, len(def.Lenses))
	permNanoIDs := make([]string, len(def.Permissions))
	for idx := range def.DDLs {
		id, err := substrate.NewNanoID()
		if err != nil {
			return nil, fmt.Errorf("pkgmgr: nanoid for DDL[%d]: %w", idx, err)
		}
		ddlNanoIDs[idx] = id
	}
	for idx := range def.Lenses {
		id, err := substrate.NewNanoID()
		if err != nil {
			return nil, fmt.Errorf("pkgmgr: nanoid for Lens[%d]: %w", idx, err)
		}
		lensNanoIDs[idx] = id
	}
	for idx := range def.Permissions {
		id, err := substrate.NewNanoID()
		if err != nil {
			return nil, fmt.Errorf("pkgmgr: nanoid for Permission[%d]: %w", idx, err)
		}
		permNanoIDs[idx] = id
	}

	now := i.Now()
	ops, declared, err := i.buildInstallBatch(def, pkgKey, ddlNanoIDs, lensNanoIDs, permNanoIDs, now)
	if err != nil {
		return nil, err
	}

	// Step 4 — single atomic batch.
	if _, err := i.Conn.AtomicBatch(ops, DefaultBatchTimeout); err != nil {
		return nil, fmt.Errorf("pkgmgr: install atomic batch: %w", err)
	}
	res.DeclaredKeys = declared
	return res, nil
}

// resolveGrants returns a copy of def with each PermissionSpec.GrantsTo
// entry translated through i.RoleIDs. Entries already shaped as a
// vtx.role.<NanoID> prefix or as a raw NanoID are passed through
// unchanged. Unrecognized canonical names are passed through unchanged
// so callers can choose to fail or warn downstream. Defensive against
// i.RoleIDs being nil.
func (i *Installer) resolveGrants(def Definition) Definition {
	if len(def.Permissions) == 0 {
		return def
	}
	out := def
	out.Permissions = make([]PermissionSpec, len(def.Permissions))
	for idx, p := range def.Permissions {
		newGrants := make([]string, 0, len(p.GrantsTo))
		for _, g := range p.GrantsTo {
			if len(g) > len("vtx.role.") && g[:len("vtx.role.")] == "vtx.role." {
				newGrants = append(newGrants, g[len("vtx.role."):])
				continue
			}
			if i.RoleIDs != nil {
				if id, ok := i.RoleIDs[g]; ok && id != "" {
					newGrants = append(newGrants, id)
					continue
				}
			}
			newGrants = append(newGrants, g)
		}
		p.GrantsTo = newGrants
		out.Permissions[idx] = p
	}
	return out
}

// InstallResult summarises an install attempt.
type InstallResult struct {
	PackageName        string
	PackageVersion     string
	PackageKey         string
	DeclaredKeys       []string
	Skipped            bool
	Reason             string
	DependencyWarnings []string
	PreInstallRan      bool
}

// ErrVersionMismatch is returned by Install when a different version of
// the same package is already installed. Use `lattice-pkg uninstall <name>`
// followed by `lattice-pkg install` to upgrade.
var ErrVersionMismatch = errors.New("pkgmgr: installed package version differs from requested")

// ErrBootstrapRequired is returned when the core-kv bucket is absent,
// indicating bootstrap has not been run.
var ErrBootstrapRequired = errors.New("pkgmgr: core-kv bucket not found — run bootstrap (or make up) before installing packages")

// installedPackage is the partial deserialization of `vtx.package.<id>.manifest`.
type installedPackage struct {
	Name    string
	Version string
	Key     string // package vertex key
}

// checkCoreBucketExists probes the core-kv bucket and returns
// ErrBootstrapRequired if it is absent (bootstrap has not been run).
// The probe is a lightweight KVListKeys call that fails fast if the
// underlying NATS stream does not exist.
func (i *Installer) checkCoreBucketExists(ctx context.Context) error {
	_, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		// Any error opening the bucket means it doesn't exist yet.
		return fmt.Errorf("%w", ErrBootstrapRequired)
	}
	return nil
}

// findInstalledPackage scans `vtx.package.>` and returns the first
// package vertex whose manifest aspect's `name` matches.
func (i *Installer) findInstalledPackage(ctx context.Context, name string) (*installedPackage, error) {
	keys, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: list keys: %w", err)
	}
	for _, k := range keys {
		// Match `vtx.package.<NanoID>.manifest`.
		if len(k) < len(PackageVertexPrefix)+len(".manifest") {
			continue
		}
		if k[:len(PackageVertexPrefix)] != PackageVertexPrefix {
			continue
		}
		if k[len(k)-len(".manifest"):] != ".manifest" {
			continue
		}
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("pkgmgr: get %s: %w", k, err)
		}
		var env struct {
			IsDeleted bool           `json:"isDeleted"`
			Data      map[string]any `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			continue
		}
		if env.IsDeleted {
			continue
		}
		gotName, _ := env.Data["name"].(string)
		if gotName != name {
			continue
		}
		gotVersion, _ := env.Data["version"].(string)
		pkgVertexKey := k[:len(k)-len(".manifest")]
		return &installedPackage{Name: gotName, Version: gotVersion, Key: pkgVertexKey}, nil
	}
	return nil, nil
}

// List returns every currently-installed package summary (one entry per
// non-tombstoned `vtx.package.<id>.manifest` aspect).
func (i *Installer) List(ctx context.Context) ([]*installedPackage, error) {
	keys, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: list keys: %w", err)
	}
	var out []*installedPackage
	for _, k := range keys {
		if len(k) < len(PackageVertexPrefix)+len(".manifest") {
			continue
		}
		if k[:len(PackageVertexPrefix)] != PackageVertexPrefix {
			continue
		}
		if k[len(k)-len(".manifest"):] != ".manifest" {
			continue
		}
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			continue
		}
		var env struct {
			IsDeleted bool           `json:"isDeleted"`
			Data      map[string]any `json:"data"`
		}
		if json.Unmarshal(entry.Value, &env) != nil || env.IsDeleted {
			continue
		}
		name, _ := env.Data["name"].(string)
		version, _ := env.Data["version"].(string)
		out = append(out, &installedPackage{
			Name:    name,
			Version: version,
			Key:     k[:len(k)-len(".manifest")],
		})
	}
	return out, nil
}

// PackageName returns the installed package's canonical name.
func (p *installedPackage) PackageName() string { return p.Name }

// PackageVersion returns the installed package's version.
func (p *installedPackage) PackageVersion() string { return p.Version }

// PackageKey returns the installed package's vertex key.
func (p *installedPackage) PackageKey() string { return p.Key }

// Uninstall soft-deletes every Core-KV key recorded in a package's
// manifest aspect. The aspect's `declaredKeys` field lists everything
// the install wrote (DDL + lens + permission + grant + aspect keys);
// the installer enumerates from there.
//
// Soft-delete only — vertices remain queryable for audit.
func (i *Installer) Uninstall(ctx context.Context, packageName string) (*UninstallResult, error) {
	ip, err := i.findInstalledPackage(ctx, packageName)
	if err != nil {
		return nil, err
	}
	if ip == nil {
		return nil, fmt.Errorf("pkgmgr: package %q not installed", packageName)
	}
	manifestKey := ip.Key + ".manifest"
	entry, err := i.Conn.KVGet(ctx, CoreBucket, manifestKey)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: read %s: %w", manifestKey, err)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &env); err != nil {
		return nil, fmt.Errorf("pkgmgr: parse %s: %w", manifestKey, err)
	}
	declaredRaw, _ := env.Data["declaredKeys"].([]any)
	keys := make([]string, 0, len(declaredRaw)+1)
	for _, dk := range declaredRaw {
		if s, ok := dk.(string); ok && s != "" {
			keys = append(keys, s)
		}
	}
	// Manifest aspect (not in declaredKeys — captured before its own key
	// was added during install) + package vertex itself, soft-deleted
	// last in order.
	keys = append(keys, manifestKey, ip.Key)

	ops, err := i.buildTombstoneBatch(ctx, keys)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return &UninstallResult{PackageName: packageName, Note: "nothing to uninstall"}, nil
	}
	if _, err := i.Conn.AtomicBatch(ops, DefaultBatchTimeout); err != nil {
		return nil, fmt.Errorf("pkgmgr: uninstall atomic batch: %w", err)
	}
	return &UninstallResult{
		PackageName: packageName,
		Tombstoned:  keys,
	}, nil
}

// UninstallResult summarises an uninstall.
type UninstallResult struct {
	PackageName string
	Tombstoned  []string
	Note        string
}
