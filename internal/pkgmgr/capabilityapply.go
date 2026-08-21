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
type CapabilityApplyPlan struct {
	ProposalID  string
	PackageName string
	Definition  Definition
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
// deliberately absent: upgradeExisting there is precisely what this fire
// exists to allow. "Widely depended on" is not by itself a reason to add one —
// lease-signing, location-domain and service-domain are each depended on
// across verticals, yet every operation their Permissions() grant is an
// ordinary business-domain write over the existing operator/consumer/provider
// role vocabulary, with no authz-plane, identity or capability-grant
// primitive among them. They are shared vertical packages, not platform-trust
// ones, and their absence here is a decision rather than an oversight.
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
	if version == "" {
		version = "0.1.0"
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
	case "upgradeExisting":
		if !installed {
			return nil, fmt.Errorf("pkgmgr: capability apply: proposal %s targets packageName %q as upgradeExisting, but no package by that name is installed", proposalKey, packageName)
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

	return &CapabilityApplyPlan{ProposalID: proposalID, PackageName: packageName, Definition: def}, nil
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
