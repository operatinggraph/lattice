package pkgmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// CapabilityApplyPlan is what an operator needs to apply an APPROVED
// capability proposal (ai-authored-capabilities-design.md §3.5): the package
// Definition to submit through the existing, unmodified F-004
// Installer.Apply (InstallPackage on a fresh target / UpgradePackage on an
// existing one), plus the proposal id the subsequent
// MarkCapabilityProposalApplied op needs to close the loop.
// The Definition is unexported so that ApplyCapabilityPlan is the only way to
// APPLY one. A capability Definition describes the proposal's own artifact and
// nothing else about the package it names, and Installer.Apply's in-place
// branch is a convergence operator — handing that Definition to Apply directly
// retires every key the proposal did not mention. MaterializedDefinition
// re-opens the value for inspection, which is a different act.
type CapabilityApplyPlan struct {
	ProposalID  string
	PackageName string
	definition  Definition
	// mode is the proposal's declared target.mode ("newPackage" /
	// "upgradeExisting"), carried because it is what ApplyCapabilityPlan
	// resolves RequireInstalled from. Unexported for the same reason the
	// Definition is: the decision it feeds belongs to ApplyCapabilityPlan, not
	// to a caller assembling its own ApplyOptions.
	mode string
}

// MaterializedDefinition returns the package Definition this plan materialized
// from the proposal's stored artifact, for INSPECTION — logging it, diffing it,
// asserting over it in a test, showing an operator what is about to land.
//
// It is not a way to apply the plan. Apply the plan with ApplyCapabilityPlan,
// which sets the options a partial, AI-authored Definition requires; passing
// this value to Installer.Apply directly discards them and converges the target
// package onto a Definition that describes only one artifact of it.
func (p *CapabilityApplyPlan) MaterializedDefinition() Definition { return p.definition }

// ApplyCapabilityPlan is the sanctioned way to apply an APPROVED capability
// proposal's plan. It exists because the correct ApplyOptions for a plan are a
// property of the plan rather than of the caller, and an option a caller must
// remember is an option a caller will forget.
func (i *Installer) ApplyCapabilityPlan(ctx context.Context, plan *CapabilityApplyPlan) (*ApplyResult, error) {
	opts := ApplyOptions{
		// Always, in both modes. A capability Definition is never authorized to
		// remove: it describes its own artifact, so every key it omits is a key
		// it has nothing to say about rather than one it retires. On the
		// newPackage arm the option is inert by construction — applyFreshInstall
		// runs a create-only batch with no diff — and that inertness is the
		// point: it closes the window in which the name becomes installed
		// between the plan build's IsPackageInstalled check and Apply's own
		// findInstalledPackage, where Apply would otherwise silently take the
		// in-place branch and tombstone the occupant's keys.
		RefuseRemovals: true,
	}
	if plan.mode == "upgradeExisting" {
		// Two effects, both wanted. (i) If the package is uninstalled between
		// the plan build and here, the honest outcome is ErrNotInstalled, not a
		// fresh install landing on the uninstall's tombstones and failing later
		// as an occupancy refusal. (ii) It is a conjunct of Apply's same-version
		// skip branch, so setting it defeats that skip — which matters because a
		// skipped apply returns success having committed nothing, and the
		// callers here go straight on to stamp MarkCapabilityProposalApplied
		// over an artifact that never landed.
		//
		// Force would defeat the same skip and is deliberately NOT set: it would
		// additionally re-open the same-version diff path that
		// CapabilityApplyPlanForProposal refuses for an independent reason.
		opts.RequireInstalled = true
	}
	res, err := i.Apply(ctx, plan.definition, opts)
	if err != nil {
		return nil, err
	}
	// A newPackage plan that comes back skipped installed nothing, and the
	// caller is about to record that it did.
	//
	// The check is on the RESULT rather than on a branch, because two different
	// branches produce it and both mean the same thing: Apply's same-version
	// skip (the name was already installed when Apply looked) and Install's own
	// idempotency skip, which applyFreshInstall mirrors (the name was free when
	// Apply looked and taken by the time Install re-checked). A newPackage plan
	// sets neither Force nor RequireInstalled, so no other skip is reachable
	// for it: the remaining one is the in-place empty-delta return, which
	// requires a version difference, and a version difference always makes the
	// package vertex and its manifest aspect updates. So for this mode, skipped
	// means claimed.
	//
	// The upgradeExisting arm is deliberately not covered here. Its skips are
	// already closed upstream — RequireInstalled defeats the same-version
	// branch, and the plan builder refuses a newVersion equal to the installed
	// one, which is what keeps the empty-delta return out of reach.
	if plan.mode == "newPackage" && res.Skipped {
		return nil, fmt.Errorf("%w: package %q was installed before this apply ran, so the apply matched what was already there and installed nothing (%s). This proposal's artifact did NOT land: do not mark it applied. Re-review it against the package now holding the name, or propose it under a name that is free",
			ErrPackageNameClaimed, plan.PackageName, res.Reason)
	}
	return res, nil
}

// platformProtectedPackages is the package-name deny-list an AI-authored
// capability proposal may never target, in either mode
// (ai-authored-capabilities-design.md §8 Fire 2). §5's deterministic content
// validator bounds an ARTIFACT (a lens parses, a grant is a subset of the
// requester's own held scope); it has no notion of the TARGET's blast radius,
// so a perfectly well-formed one-artifact Definition diff-applied into a
// platform-trust package is exactly the shape review is least able to catch.
// Keys are canonical lowercase (every package name in this repo is
// lowercase-with-hyphens); look a name up through PlatformProtectedPackage,
// which normalizes case and surrounding whitespace, never by direct index.
// Manually maintained, deliberately NOT derived from the Makefile — an
// install-order refactor must never silently unprotect a package:
//
//   - the platform's own authz/identity/privacy trust base: the
//     `make install-packages` core set (rbac-domain, control-authz,
//     privacy-base, privacy-operator-grant, identity-domain, objects-base,
//     console-operator) plus demo-operator, console-operator's structural twin
//     — same Depends, its own demoOperator role, and a GrantTable read-grant
//     producer — which no Makefile target installs;
//   - identity-hygiene, part of the identity trust surface: it shares
//     identity-domain's KV subtree and carries the credential-repoint /
//     reconciliation machinery. It is NOT in `make install-packages` (only the
//     standalone verify-package-identity-hygiene target installs it) — it is
//     here on that trust-surface reasoning alone, with no Makefile parity;
//   - the capability-authoring machinery itself — capability-author, augur —
//     the sharpest privilege-escalation shape there is (a proposal that
//     rewrites the machinery reviewing it);
//   - the shared cross-vertical primitives — orchestration-base,
//     semantic-contracts — whose blast radius spans every vertical.
//
// A vertical business-domain package (cafe-domain, clinic-domain, …) is
// deliberately absent, on trust grounds: this list is about which packages an
// AI-authored proposal may never name at all, and a vertical package is not
// part of the platform's trust base. "Widely depended on" is not by itself a
// reason to add one — lease-signing, location-domain and service-domain are
// each depended on across verticals, yet every operation their Permissions()
// grant is an ordinary business-domain write over the existing
// operator/consumer/provider role vocabulary, with no authz-plane, identity or
// capability-grant primitive among them. They are shared vertical packages,
// not platform-trust ones, and their absence here is a decision rather than an
// oversight.
//
// Absence from this list is not permission to reshape such a package. An
// upgradeExisting proposal still has to describe the package it targets: the
// apply seam refuses one whose Definition would retire declared keys it says
// nothing about (ApplyCapabilityPlan / ErrApplyWouldRemove), whatever the name.
// The two guards answer different questions — this one asks whether the name
// may be touched, that one asks whether the submitted Definition covers what
// it would converge.
var platformProtectedPackages = map[string]bool{
	"rbac-domain":            true,
	"control-authz":          true,
	"privacy-base":           true,
	"privacy-operator-grant": true,
	"identity-domain":        true,
	"identity-hygiene":       true,
	"objects-base":           true,
	"console-operator":       true,
	"demo-operator":          true,
	"capability-author":      true,
	"augur":                  true,
	"orchestration-base":     true,
	"semantic-contracts":     true,
}

// PlatformProtectedPackage reports whether name is on the platform-protected
// deny-list — every caller that can reach InstallPackage/UpgradePackage or
// MarkCapabilityProposalApplied for an AI-authored proposal must refuse
// before doing so, not just CapabilityApplyPlanForProposal. Loupe's apply
// endpoint can short-circuit into its resumable-recovery branch before the
// plan builder ever runs, and its mark-applied endpoint never calls the plan
// builder at all, so each consults this directly.
func PlatformProtectedPackage(name string) bool {
	return platformProtectedPackages[normalizePackageName(name)]
}

// normalizePackageName folds a package name to its canonical matching form:
// surrounding whitespace trimmed, then lowercased.
//
// Its only RESOLVING use is PlatformProtectedPackage's deny-list lookup, and
// that is deliberate: widening a deny-list's match set can only deny more
// names, never select one for a destructive operation, so folding a near-miss
// spelling ("Rbac-Domain", " rbac-domain ") into a hit there is strictly
// safer than missing it.
//
// Installer.findInstalledPackage also calls this, but NEVER to decide a
// match — only to detect that an exact miss has a fold-equal near-miss on
// record, so it can refuse loudly instead of returning silent absence. A
// find is a destructive resolution target (diff-apply, tombstone): folding a
// near-miss into a hit there would let a `mode: upgradeExisting` proposal
// targeting a fold-equal-but-not-exact packageName resolve to, and diff-apply
// into, an unrelated manifest — the widened match set widens what gets
// mutated. Do not add a resolving (match-deciding) call site for this
// function without re-deriving that argument for it.
func normalizePackageName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// CapabilityApplyPlanForProposal reads an APPROVED vtx.capabilityproposal.<id>
// vertex's stored artifact + target and materializes the SAME Definition §5
// already validated (DefinitionForCapabilityArtifact — byte-for-byte what
// RecordCapabilityProposal validated), ready for the operator's own
// Installer.Apply. It is read-only: no mutation, no op submission — a caller
// can preview it (log/diff) before committing to the real F-004 apply, and
// the F-004 apply itself stays the existing, untouched InstallPackage/
// UpgradePackage path every human package install already runs (this
// increment does not special-case it).
//
// Returns an error for anything short of "approved with a well-formed
// target" — that boundary was already crossed by RecordCapabilityProposal +
// ReviewCapabilityProposal (design §5 points 2-3); a proposal that hasn't
// crossed it yet (still pending/invalid/rejected, or somehow missing its
// target) is a caller-contract violation (applying out of order), never a
// model-authored defect. Also binds target.mode to the LIVE install catalog
// (newPackage requires packageName NOT already installed; upgradeExisting
// requires it IS) — Installer.Apply's own name-based dispatch has no notion
// of "this AI-authored def is a different lineage" from an unrelated
// same-named package, so that check belongs here, before a Definition is
// ever built. Ahead of both, target.packageName is refused outright when
// PlatformProtectedPackage accepts it, in either mode.
func CapabilityApplyPlanForProposal(ctx context.Context, conn *substrate.Conn, proposalKey string) (*CapabilityApplyPlan, error) {
	proposalID, err := proposalIDFromKey(proposalKey)
	if err != nil {
		return nil, err
	}

	review, err := readAspectData(ctx, conn, proposalKey+".review")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: read %s.review: %w", proposalKey, err)
	}
	if state, _ := review["state"].(string); state != "approved" {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s is %q, not approved", proposalKey, state)
	}

	artifact, err := readAspectData(ctx, conn, proposalKey+".artifact")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: read %s.artifact: %w", proposalKey, err)
	}
	target, err := readAspectData(ctx, conn, proposalKey+".target")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: read %s.target: %w", proposalKey, err)
	}

	kind, err := typedStringField(artifact, "kind")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s .artifact.%w", proposalKey, err)
	}
	content, err := typedStringField(artifact, "content")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s .artifact.%w", proposalKey, err)
	}
	packageName, err := typedStringField(target, "packageName")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s .target.%w", proposalKey, err)
	}
	if packageName == "" {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s has no target.packageName", proposalKey)
	}
	// Refuse a platform-protected target before mode is even considered: the
	// deny-list binds BOTH modes, so an uninstall leaves no newPackage window
	// through which a protected name could be re-created AI-authored.
	if PlatformProtectedPackage(packageName) {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s targets packageName %q, a platform-protected package that no AI-authored proposal may install or upgrade", proposalKey, packageName)
	}
	mode, err := typedStringField(target, "mode")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s .target.%w", proposalKey, err)
	}
	version, err := typedStringField(target, "newVersion")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s .target.%w", proposalKey, err)
	}
	baseVersion, err := typedStringField(target, "baseVersion")
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s .target.%w", proposalKey, err)
	}

	// Bind the proposal's declared intent (newPackage vs upgradeExisting) to
	// the LIVE install catalog before ever building a Definition — an AI-
	// authored target.packageName colliding with an unrelated already-
	// installed package must never silently diff-apply into it (Apply's own
	// name-based dispatch has no notion of "this is a different lineage").
	installed, err := IsPackageInstalled(ctx, conn, packageName)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: check %q installed: %w", packageName, err)
	}
	switch mode {
	case "newPackage":
		if installed {
			return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s targets packageName %q as newPackage, but a package by that name is already installed", proposalKey, packageName)
		}
		// A first version an author did not state is a version nobody needs to
		// state: the package is new, so any starting point is as true as the
		// next. The same silence over an upgrade is refused below.
		if version == "" {
			version = "0.1.0"
		}
	case "upgradeExisting":
		if !installed {
			return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s targets packageName %q as upgradeExisting, but no package by that name is installed", proposalKey, packageName)
		}
		if err := checkUpgradeExistingVersions(ctx, conn, proposalKey, packageName, version, baseVersion); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s has unrecognized target.mode %q", proposalKey, mode)
	}

	def, err := DefinitionForCapabilityArtifact(kind, json.RawMessage(content), packageName, version)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: capability apply: %w", err)
	}

	// Dispatch-side privilege gate, the complement to the protected-PACKAGE
	// check above: an authored weaverTarget's gaps run under the Weaver's
	// operator@any authority, so refuse any gap that would dispatch a
	// platform-privileged operation or loom pattern, or bind to a protected/
	// secure lens (authored_dispatch_scope.go). Runs only for a weaverTarget
	// artifact — def.WeaverTargets is empty for every other kind.
	if err := enforceAuthoredWeaverTargetScope(ctx, conn, proposalKey, def.WeaverTargets); err != nil {
		return nil, err
	}

	return &CapabilityApplyPlan{ProposalID: proposalID, PackageName: packageName, definition: def, mode: mode}, nil
}

// checkUpgradeExistingVersions enforces the three preconditions an
// upgradeExisting proposal must satisfy before a Definition is built for it.
// All three read fields the proposal already carries, so none needs a package
// version bump or a new op field.
//
// It resolves the installed version itself rather than threading it down from
// the caller's IsPackageInstalled check: that check answers a different
// question (does the declared mode match the live catalog) and its fold-equal
// near-miss refusal is what makes the mode binding safe, so it stays. This one
// runs only on the upgradeExisting arm, after that check has already passed.
func checkUpgradeExistingVersions(ctx context.Context, conn *substrate.Conn, proposalKey, packageName, newVersion, baseVersion string) error {
	// The shared newVersion default is meaningful for a new package and
	// meaningless for an upgrade: defaulting here would record cafe-domain@1.3.0
	// as 0.1.0. An upgrade's target version must be declared, not inferred.
	if newVersion == "" {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s targets %q as upgradeExisting with no target.newVersion — an upgrade's target version must be declared, since the shared default would record the package at 0.1.0", proposalKey, packageName)
	}
	existing, err := NewInstaller(conn, "").findInstalledPackage(ctx, packageName)
	if err != nil {
		return fmt.Errorf("pkgmgr: capability apply: resolve installed version of %q: %w", packageName, err)
	}
	if existing == nil {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s targets %q as upgradeExisting, but no package by that name is installed", proposalKey, packageName)
	}
	// The console answers "did this proposal's apply already commit?" by asking
	// whether the target package is live AT THE TARGET VERSION. If an upgrade's
	// newVersion equals the version already installed, that question has no
	// answer — "installed at newVersion" means both "applied" and "never
	// applied" — and every such proposal is 409'd as recoverable and closed over
	// an artifact that never landed. Refusing here makes the discriminator sound
	// by construction rather than by convention.
	if newVersion == existing.Version {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s targets %q as upgradeExisting with target.newVersion %q, which is the version already installed — an upgrade must move the version, so that a package live at newVersion can only mean this apply committed", proposalKey, packageName, newVersion)
	}
	// The mode's optimistic-concurrency check. A proposal authored against
	// 1.0.0 and applied over 1.1.0 is a stale apply: the artifacts it DOES
	// describe overwrite whatever 1.1.0 changed to them. The proposal already
	// records the version it was authored against, so absence is refused rather
	// than tolerated.
	if baseVersion == "" {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s targets %q as upgradeExisting with no target.baseVersion — an upgrade must declare the version it was authored against, or it cannot be told from a stale apply", proposalKey, packageName)
	}
	if baseVersion != existing.Version {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s targets %q as upgradeExisting with target.baseVersion %q, but %q is installed — this proposal was authored against a version that is no longer there, so applying it would overwrite whatever changed since", proposalKey, packageName, baseVersion, existing.Version)
	}
	return nil
}

// typedStringField returns m[key] as a string. Absence is not an error
// (returns "" — the caller decides whether the field is required); PRESENCE
// with the wrong JSON type IS always an error — silently defaulting a
// type-assertion failure to "" would discard a real (corrupted or
// schema-drifted) value instead of failing loudly.
func typedStringField(m map[string]any, key string) (string, error) {
	v, exists := m[key]
	if !exists || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s is %T, want string", key, v)
	}
	return s, nil
}

// proposalIDFromKey extracts the bare id from a vtx.capabilityproposal.<id>
// key, rejecting anything else (malformed key, or a different vertex type).
func proposalIDFromKey(key string) (string, error) {
	const prefix = "vtx.capabilityproposal."
	if !strings.HasPrefix(key, prefix) {
		return "", fmt.Errorf("pkgmgr: capability apply: %q is not a vtx.capabilityproposal.<id> key", key)
	}
	id := strings.TrimPrefix(key, prefix)
	if id == "" || strings.Contains(id, ".") {
		return "", fmt.Errorf("pkgmgr: capability apply: %q is not a bare capabilityproposal id", key)
	}
	return id, nil
}

// readAspectData KVGets one aspect key and returns its `data` object,
// erroring on a missing or tombstoned aspect (mirrors the isDeleted check
// Installer.findInstalledPackage already applies to package manifests).
func readAspectData(ctx context.Context, conn *substrate.Conn, key string) (map[string]any, error) {
	entry, err := conn.KVGet(ctx, CoreBucket, key)
	if err != nil {
		return nil, err
	}
	var env struct {
		IsDeleted bool           `json:"isDeleted"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &env); err != nil {
		return nil, err
	}
	if env.IsDeleted {
		return nil, fmt.Errorf("pkgmgr: %s is deleted", key)
	}
	return env.Data, nil
}
