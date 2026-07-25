package pkgmgr

import (
	"context"
	"fmt"
)

// ApplyOptions tunes the upgrade-aware install/upgrade dispatch
// (Contract #8 §8.6, F-004 Fire 2). The zero value reproduces today's plain
// install: create-if-absent, skip-if-same-version, submit-for-real.
type ApplyOptions struct {
	// Force makes a same-version target diff-apply changed entity bodies in
	// place (a dev refresh) instead of skipping. It has no effect on a
	// cross-version target (which always diff-applies) or a fresh install.
	Force bool
	// DryRun computes the create/update/tombstone delta and returns it in the
	// ApplyResult without submitting any op (preview only).
	DryRun bool
	// RequireInstalled gives the explicit `upgrade` command its semantics: a
	// missing base install is ErrNotInstalled rather than a fresh create. The
	// `install` command leaves it false (create-if-absent).
	RequireInstalled bool
}

// ApplyResult is the unified outcome of Apply across the fresh-install,
// in-place-upgrade, and skip paths, including dry-run previews. Action is one
// of "install", "upgrade", or "skip"; FromVersion is empty on a fresh install.
type ApplyResult struct {
	PackageName string
	PackageKey  string
	Action      string
	FromVersion string
	ToVersion   string
	Created     int
	Updated     int
	Tombstoned  int
	Skipped     bool
	DryRun      bool
	Reason      string

	// CreatedKeys / UpdatedKeys / TombstonedKeys are populated only for a
	// DryRun preview so the operator sees exactly which keys would change.
	CreatedKeys    []string
	UpdatedKeys    []string
	TombstonedKeys []string

	DependencyWarnings []string
}

// Apply is the upgrade-aware entry point for `lattice-pkg install` / `upgrade`
// (Contract #8 §8.6, F-004 Fire 2). It inspects install state and dispatches:
//
//   - not installed                    → fresh create (Install), unless
//     opts.RequireInstalled → ErrNotInstalled (the explicit `upgrade` command)
//   - installed, same version, !Force  → skip (preserve install idempotency)
//   - installed, same version, Force   → in-place diff-apply (dev refresh)
//   - installed, different version     → auto-upgrade (in-place diff-apply)
//
// opts.DryRun computes and returns the delta without submitting any op. Apply
// is P2-clean: every mutating path routes through the Processor (Install's
// InstallPackage op or the UpgradePackage op); it never writes Core KV directly.
func (i *Installer) Apply(ctx context.Context, def Definition, opts ApplyOptions) (*ApplyResult, error) {
	def, err := i.preflight(def)
	if err != nil {
		return nil, err
	}
	if err := i.checkCoreBucketExists(ctx); err != nil {
		return nil, err
	}

	existing, err := i.findInstalledPackage(ctx, def.Name)
	if err != nil {
		return nil, err
	}

	// Fresh install (or ErrNotInstalled under the explicit upgrade command).
	if existing == nil {
		if opts.RequireInstalled {
			return nil, fmt.Errorf("%w: %q", ErrNotInstalled, def.Name)
		}
		return i.applyFreshInstall(ctx, def, opts)
	}

	// Same version, no force, install semantics → preserve today's skip.
	if existing.Version == def.Version && !opts.Force && !opts.RequireInstalled {
		return &ApplyResult{
			PackageName: def.Name,
			PackageKey:  existing.Key,
			Action:      "skip",
			FromVersion: existing.Version,
			ToVersion:   def.Version,
			Skipped:     true,
			Reason:      fmt.Sprintf("package %q version %q already installed", def.Name, def.Version),
		}, nil
	}

	// In-place diff-apply: cross-version auto-upgrade, or same-version force /
	// the explicit upgrade command.
	mutations, sum, err := i.computeDeltaAgainst(ctx, existing, def)
	if err != nil {
		return nil, err
	}
	res := &ApplyResult{
		PackageName: def.Name,
		PackageKey:  existing.Key,
		Action:      "upgrade",
		FromVersion: existing.Version,
		ToVersion:   def.Version,
		Created:     sum.created,
		Updated:     sum.updated,
		Tombstoned:  sum.tombstoned,
	}
	if len(mutations) == 0 {
		res.Action = "skip"
		res.Skipped = true
		res.Reason = fmt.Sprintf("package %q already matches the requested definition (no changes)", def.Name)
		return res, nil
	}
	if opts.DryRun {
		res.DryRun = true
		res.partitionKeys(mutations)
		return res, nil
	}
	if err := i.submitUpgradeOp(ctx, def, existing.Version, mutations); err != nil {
		return nil, err
	}
	return res, nil
}

// applyFreshInstall handles the no-base branch of Apply: a dry-run previews the
// full create batch; a real run delegates to Install (which re-checks state and
// the canonical-name collision guard) and adapts its result.
func (i *Installer) applyFreshInstall(ctx context.Context, def Definition, opts ApplyOptions) (*ApplyResult, error) {
	if opts.DryRun {
		ops, _, pkgKey, err := i.buildManifestBatch(def)
		if err != nil {
			return nil, err
		}
		res := &ApplyResult{
			PackageName: def.Name,
			PackageKey:  pkgKey,
			Action:      "install",
			ToVersion:   def.Version,
			Created:     len(ops),
			DryRun:      true,
		}
		for _, op := range ops {
			res.CreatedKeys = append(res.CreatedKeys, op.Key)
		}
		return res, nil
	}

	r, err := i.Install(ctx, def)
	if err != nil {
		return nil, err
	}
	res := &ApplyResult{
		PackageName:        r.PackageName,
		PackageKey:         r.PackageKey,
		Action:             "install",
		ToVersion:          r.PackageVersion,
		Created:            len(r.DeclaredKeys),
		DependencyWarnings: r.DependencyWarnings,
	}
	// Defensive: a fresh-branch install should never skip (existing == nil),
	// but mirror the reason if it ever does so the CLI reports it faithfully.
	if r.Skipped {
		res.Action = "skip"
		res.Skipped = true
		res.Reason = r.Reason
	}
	return res, nil
}

// partitionKeys fills the dry-run key lists from the computed mutation batch.
func (r *ApplyResult) partitionKeys(mutations []installMutation) {
	for _, m := range mutations {
		switch m.Op {
		case "create":
			r.CreatedKeys = append(r.CreatedKeys, m.Key)
		case "update":
			r.UpdatedKeys = append(r.UpdatedKeys, m.Key)
		case "tombstone":
			r.TombstonedKeys = append(r.TombstonedKeys, m.Key)
		}
	}
}
