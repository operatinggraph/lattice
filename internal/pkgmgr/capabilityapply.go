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
// ever built.
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
