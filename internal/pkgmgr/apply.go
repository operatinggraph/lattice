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

	// ReactivationRequired names lens spec edits this apply committed that a
	// running Refractor cannot hot-reload. The apply succeeded and the stored
	// spec is now the new one; these lenses go on serving their ACTIVATED spec
	// until re-activated, which is the difference between an upgrade that
	// applied and one that merely landed.
	ReactivationRequired []string

	// RevocationsRespected counts surviving declared GRANT/ROLE topology keys
	// (never vtx.meta.* definitions, see diffManifest) this apply left
	// tombstoned because an out-of-band op had already revoked them. The apply
	// succeeded; these grants stay revoked rather than being silently
	// un-tombstoned by the body-diff update path.
	RevocationsRespected int

	// RetentionHoldersPreserved counts LIVE vtx.retentionclass.* keys this
	// apply left live-but-undeclared instead of tombstoning them off the
	// removal path, because only ShredRetentionClassKey may destroy a class's
	// DEK and it refuses a tombstoned holder forever. The apply succeeded;
	// these holders remain shreddable on the controller's retention schedule.
	RetentionHoldersPreserved int

	// RetentionHoldersAlreadyStranded counts vtx.retentionclass.* keys the
	// removal path found ALREADY tombstoned — untouched by this apply, but not
	// preserved either: ShredRetentionClassKey refuses them, so their DEK is
	// past every destruction path. Reported so an operator can escalate the
	// pre-existing damage instead of reading a reassuring "preserved" count.
	RetentionHoldersAlreadyStranded int

	// SecureColumnsWidened counts lens secure columns whose declared
	// holderTypes this apply refused to narrow, writing the union with the
	// committed spec instead (retention-class-key-custody-design.md §24.6). The
	// apply succeeded; the package's narrowed declaration did not take effect,
	// because ciphertext already written under a dropped holder type would
	// otherwise become invisible to every destruction-readiness reader.
	SecureColumnsWidened int

	// SecureColumnsRetired counts the secure COLUMNS whose committed
	// targetConfig.secureColumns entry this apply erased because the package
	// declared the retirement in Definition.RetiredSecureColumns — the same
	// unit SecureColumnsWidened counts, so a removed lens that took twenty
	// secure columns with it reports twenty, not one. Each is an author's
	// attestation that the ciphertext those holder types encrypted is safe to
	// stop tracking; the platform verified nothing. Reported so an operator
	// sees custody history leaving the system rather than only its arrival.
	SecureColumnsRetired int

	// SecureColumnRetirementsUnused labels every
	// Definition.RetiredSecureColumns entry this apply matched to no actual
	// erasure, as "<lens> / <column selector>". An unused entry excused
	// nothing here, but it sits in the package file looking load-bearing, and
	// a retirement that has outlived the edit it was written for is exactly
	// what a later author would otherwise mistake for coverage of theirs.
	SecureColumnRetirementsUnused []string

	// LeafBudgetWarnings names every subtypeOf target (dynamic-type-taxonomy-
	// design.md §10.2) whose resolved leaf count this apply pushed past its
	// declared LeafBudget. Advisory only — the apply still succeeded. This is
	// the operator-visible surface for `lattice-pkg install`/`upgrade`, which
	// route through Apply rather than Install directly.
	LeafBudgetWarnings []string
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
	mutations, sum, leafBudgetWarnings, err := i.computeDeltaAgainst(ctx, existing, def)
	if err != nil {
		return nil, err
	}
	res := &ApplyResult{
		PackageName: def.Name,
		PackageKey:  existing.Key,
		Action:      "upgrade",
		FromVersion: existing.Version,
		ToVersion:   def.Version,
		Created:     sum.created + sum.revived,
		Updated:     sum.updated,
		Tombstoned:  sum.tombstoned,

		ReactivationRequired:            sum.reactivation,
		RevocationsRespected:            sum.revocationsRespected,
		RetentionHoldersPreserved:       sum.retentionHoldersPreserved,
		RetentionHoldersAlreadyStranded: sum.retentionHoldersAlreadyStranded,
		SecureColumnsWidened:            sum.secureColumnsWidened,
		LeafBudgetWarnings:              leafBudgetWarnings,
	}

	// The Secure-Lens key-custody retirement guard (retention-class-key-
	// custody-design.md §30). computeDeltaAgainst is shared with Upgrade, so
	// wiring the guard only there would leave it bypassable through this path
	// — `lattice-pkg install`/`upgrade` and Loupe's POST /api/packages/apply
	// both land here.
	//
	// Unlike the op-meta guard below it runs before the empty-delta AND the
	// dry-run returns: it is pure, so a preview can honestly report that the
	// real apply would refuse, instead of previewing a delta that cannot
	// commit.
	retired, unusedRetirements, err := enforceSecureColumnRetirement(def, sum.droppedSecureColumns)
	if err != nil {
		return nil, err
	}
	res.SecureColumnsRetired = retired
	res.SecureColumnRetirementsUnused = unusedRetirements

	if len(mutations) == 0 {
		res.Action = "skip"
		res.Skipped = true
		res.Reason = noChangesReason(def.Name, sum.revocationsRespected, sum.secureColumnsWidened)
		return res, nil
	}
	if opts.DryRun {
		res.DryRun = true
		res.partitionKeys(mutations)
		return res, nil
	}
	// The op-meta retirement guard (opmeta-retirement-open-task-guard-
	// design.md §2) — skipped on DryRun above since a preview must never
	// cancel a live task as a side effect.
	if len(sum.tombstonedOpMetas) > 0 {
		if err := i.enforceOpMetaDisposition(ctx, def, sum.tombstonedOpMetas); err != nil {
			return nil, err
		}
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
		ops, declared, pkgKey, leafBudgetWarnings, err := i.buildManifestBatch(ctx, def, metaScanResult{})
		if err != nil {
			return nil, err
		}
		// The occupancy gate, run on the preview for the same reason the
		// Secure-Lens retirement guard runs before Apply's dry-run return: a
		// preview whose real run would be refused must say so, not describe the
		// batch it would have submitted. Without it a dry-run over an
		// UNINSTALLED package reports "install, N keys created" for an install
		// that cannot commit a single one of them — the very false green the
		// gate exists to kill, one layer up.
		//
		// It refuses with the sentinel rather than annotating the ApplyResult
		// because there is no honest ApplyResult here: Created/CreatedKeys are
		// the fields a caller reads to learn what an apply would do, and any
		// value in them describes writes that will not happen. An error has no
		// clean-preview reading.
		//
		// A read, never a write, so a preview stays a preview.
		tombstoned, liveOccupants, err := i.declaredKeyOccupants(ctx, declared)
		if err != nil {
			return nil, err
		}
		if len(tombstoned) > 0 || len(liveOccupants) > 0 {
			return nil, occupiedDeclaredKeysError(def.Name, tombstoned, liveOccupants)
		}
		res := &ApplyResult{
			PackageName:        def.Name,
			PackageKey:         pkgKey,
			Action:             "install",
			ToVersion:          def.Version,
			Created:            len(ops),
			DryRun:             true,
			LeafBudgetWarnings: leafBudgetWarnings,
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
		LeafBudgetWarnings: r.LeafBudgetWarnings,
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
