package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// CoreBucket is the bucket all Capability Package writes target. Phase
// 1 supports only single-bucket atomic batches; cross-bucket batches
// aren't available from NATS atomic-batch (Story 1.1).
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

	// Step 1 — dependency warnings (Phase 1: warn-and-proceed).
	for _, dep := range def.Depends {
		if dep == "" {
			continue
		}
		// No discovery loop in Phase 1; we just record the declared deps.
		res.DependencyWarnings = append(res.DependencyWarnings,
			fmt.Sprintf("declared dependency %q not verified (Phase 1 warn-and-proceed)", dep))
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

// InstallResult summarises an install attempt.
type InstallResult struct {
	PackageName        string
	PackageVersion     string
	PackageKey         string
	DeclaredKeys       []string
	Skipped            bool
	Reason             string
	DependencyWarnings []string
}

// ErrVersionMismatch is returned by Install when a different version of
// the same package is already installed. Phase 2 will replace this
// with an upgrade path.
var ErrVersionMismatch = errors.New("pkgmgr: installed package version differs from requested")

// installedPackage is the partial deserialization of `vtx.package.<id>.manifest`.
type installedPackage struct {
	Name    string
	Version string
	Key     string // package vertex key
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
