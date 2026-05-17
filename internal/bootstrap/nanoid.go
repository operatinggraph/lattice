// Package bootstrap — primordial NanoID inventory.
//
// Primordial IDs are generated AT RUNTIME per deployment (not hard-coded
// constants). This ensures every Lattice environment has a unique
// primordial key space — a property the architecture relies on for
// cell-agnostic key design (NFR-SC2) and proper multi-deployment
// isolation (FR48).
//
// Lifecycle:
//   - First `make up` from a clean state: cmd/bootstrap calls
//     LoadOrGenerate(path). The file does not exist, so 12 fresh
//     NanoIDs are generated via substrate.NewNanoID(), the package-level
//     ID variables are populated, and the resulting set is persisted
//     to lattice.bootstrap.json.
//   - Subsequent `make up` within the same deployment (file still
//     present, e.g. on retry of a failed run): LoadOrGenerate sees
//     the file, calls Load, and the same IDs are reused — idempotent.
//   - `make down` deletes lattice.bootstrap.json (Makefile); the next
//     `make up` generates a fresh primordial key space.
//   - Read-only callers (cmd/refractor-stub via count-only gate;
//     scripts/verify-bootstrap.go): call Load(path) explicitly.
//
// All generated IDs are Contract #1-compliant by construction (20-char,
// custom 58-char alphabet, no I/l/O/0) since they come from
// substrate.NewNanoID() which is the canonical generator.
//
// Link directionality convention for primordial entities:
// Since all primordial entries share the same createdAt timestamp
// (BootstrapTime), Contract #1's "younger = later createdAt" rule
// needs a tiebreaker. The convention adopted here is category-based:
// identities and permissions are conventionally "younger" than roles.
// This is a primordial-specific rule; real entities will have distinct
// createdAt timestamps and won't need a tiebreaker.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Alphabet re-exports substrate.Alphabet as the canonical Lattice NanoID
// alphabet. Provided so callers reading from this package see the single
// source of truth.
const Alphabet = substrate.Alphabet

// HealthBootstrapCompleteKey is the Health KV key written by refractor-stub
// once all primordial Core KV keys are present. This is constant across
// deployments — it's an addressing convention, not a NanoID.
const HealthBootstrapCompleteKey = "health.bootstrap.complete"

// Primordial NanoIDs and derived keys.
//
// MUST be populated via LoadOrGenerate (cmd/bootstrap) or Load
// (read-only callers) BEFORE any consumer accesses these variables.
// Accessing them before population yields empty strings — typically
// caught immediately by NATS errors on `vtx.identity.` (with no ID).
//
// Variables are package-level (not constants) precisely because they
// are populated at runtime — each Lattice deployment generates its own
// primordial ID set on first boot.
var (
	BootstrapOpID                  string
	BootstrapOpKey                 string
	BootstrapIdentityID            string
	BootstrapIdentityKey           string
	PlatformActorID                string
	PlatformActorKey               string
	MetaRootID                     string
	MetaRootKey                    string
	CapabilityLensID               string
	CapabilityLensKey              string
	CapabilityRoleIndexLensID      string
	CapabilityRoleIndexLensKey     string
	RoleConsumerID                 string
	RoleConsumerKey                string
	RoleFrontOfHouseID             string
	RoleFrontOfHouseKey            string
	RoleBackOfHouseID              string
	RoleBackOfHouseKey             string
	RoleOperatorID                 string
	RoleOperatorKey                string
	RolePlatformIntlID             string
	RolePlatformIntlKey            string
	PermPlatformAnyID              string
	PermPlatformAnyKey             string

	// Story 3.6: DDL meta-vertex IDs + keys for the role/permission domain.
	DDLRoleID                string // vtx.meta.<NanoID>, canonicalName="role"
	DDLRoleKey               string
	DDLPermissionID          string // vtx.meta.<NanoID>, canonicalName="permission"
	DDLPermissionKey         string
	DDLHoldsRoleID           string // vtx.meta.<NanoID>, canonicalName="holdsRole"
	DDLHoldsRoleKey          string
	DDLGrantsPermissionID    string // vtx.meta.<NanoID>, canonicalName="grantsPermission"
	DDLGrantsPermissionKey   string
	DDLReportsToID           string // vtx.meta.<NanoID>, canonicalName="reportsTo"
	DDLReportsToKey          string

	// Story 3.6: 12 per-op permission vertex IDs + keys for operator grants.
	PermCreateRoleID                string
	PermCreateRoleKey               string
	PermUpdateRoleID                string
	PermUpdateRoleKey               string
	PermTombstoneRoleID             string
	PermTombstoneRoleKey            string
	PermCreatePermissionID          string
	PermCreatePermissionKey         string
	PermUpdatePermissionID          string
	PermUpdatePermissionKey         string
	PermTombstonePermissionID       string
	PermTombstonePermissionKey      string
	PermAssignRoleID                string
	PermAssignRoleKey               string
	PermRevokeRoleID                string
	PermRevokeRoleKey               string
	PermGrantPermissionID           string
	PermGrantPermissionKey          string
	PermRevokePermissionID          string
	PermRevokePermissionKey         string
	PermAssignReportingChainID      string
	PermAssignReportingChainKey     string
	PermRemoveReportingChainID      string
	PermRemoveReportingChainKey     string

	// Story 4.1: Identity domain DDL meta-vertex.
	DDLIdentityID  string // vtx.meta.<NanoID>, canonicalName="identity"
	DDLIdentityKey string

	// Story 4.1: Five new permission vertex IDs + keys for identity ops.
	PermCreateUnclaimedIdentityID  string
	PermCreateUnclaimedIdentityKey string
	PermClaimIdentityID            string
	PermClaimIdentityKey           string
	PermFlagIdentityForReviewID    string
	PermFlagIdentityForReviewKey   string
	PermApproveIdentityMergeID     string
	PermApproveIdentityMergeKey    string
	PermScanIdentityDuplicatesID   string
	PermScanIdentityDuplicatesKey  string

	// Derived link keys.
	BootstrapHoldsRoleLinkKey string
	PlatformHoldsRoleLinkKey  string
	GrantsPermissionLinkKey   string
)

// PrimordialIDsRaw is the persisted form (matches lattice.bootstrap.json).
type PrimordialIDsRaw struct {
	BootstrapOp             string `json:"bootstrapOp"`
	BootstrapIdentity       string `json:"bootstrapIdentity"`
	PlatformActor           string `json:"platformActor"`
	MetaRoot                string `json:"metaRoot"`
	CapabilityLens          string `json:"capabilityLens"`
	CapabilityRoleIndexLens string `json:"capabilityRoleIndexLens"`
	RoleConsumer            string `json:"roleConsumer"`
	RoleFrontOfHouse        string `json:"roleFrontOfHouse"`
	RoleBackOfHouse         string `json:"roleBackOfHouse"`
	RoleOperator            string `json:"roleOperator"`
	RolePlatformIntl        string `json:"rolePlatformIntl"`
	PermPlatformAny         string `json:"permPlatformAny"`

	// Story 3.6: DDL meta-vertex IDs for the role/permission domain.
	DDLRole             string `json:"ddlRole"`
	DDLPermission       string `json:"ddlPermission"`
	DDLHoldsRole        string `json:"ddlHoldsRole"`
	DDLGrantsPermission string `json:"ddlGrantsPermission"`
	DDLReportsTo        string `json:"ddlReportsTo"`

	// Story 3.6: 12 per-op permission vertex IDs.
	PermCreateRole           string `json:"permCreateRole"`
	PermUpdateRole           string `json:"permUpdateRole"`
	PermTombstoneRole        string `json:"permTombstoneRole"`
	PermCreatePermission     string `json:"permCreatePermission"`
	PermUpdatePermission     string `json:"permUpdatePermission"`
	PermTombstonePermission  string `json:"permTombstonePermission"`
	PermAssignRole           string `json:"permAssignRole"`
	PermRevokeRole           string `json:"permRevokeRole"`
	PermGrantPermission      string `json:"permGrantPermission"`
	PermRevokePermission     string `json:"permRevokePermission"`
	PermAssignReportingChain string `json:"permAssignReportingChain"`
	PermRemoveReportingChain string `json:"permRemoveReportingChain"`

	// Story 4.1: Identity domain DDL meta-vertex + 5 identity permission IDs.
	DDLIdentity                string `json:"ddlIdentity"`
	PermCreateUnclaimedIdentity string `json:"permCreateUnclaimedIdentity"`
	PermClaimIdentity          string `json:"permClaimIdentity"`
	PermFlagIdentityForReview  string `json:"permFlagIdentityForReview"`
	PermApproveIdentityMerge   string `json:"permApproveIdentityMerge"`
	PermScanIdentityDuplicates string `json:"permScanIdentityDuplicates"`
}

// BootstrapFile is the wire format of lattice.bootstrap.json.
type BootstrapFile struct {
	Version       string           `json:"version"`
	GeneratedAt   string           `json:"generatedAt"`
	PrimordialIDs PrimordialIDsRaw `json:"primordialIDs"`
}

// LoadOrGenerate is called by cmd/bootstrap. If the file exists, load
// IDs from it (idempotent re-run after partial failure). If not, generate
// fresh IDs **in memory only** — the caller MUST invoke Persist(path)
// after SeedPrimordial succeeds. This separation prevents a poisoned
// state in which the JSON exists but Core KV was never seeded (e.g.,
// when cmd/bootstrap is killed between LoadOrGenerate and SeedPrimordial).
//
// The "generate" path produces a unique primordial key space PER
// DEPLOYMENT — this is the architectural requirement.
//
// Returns true if IDs were freshly generated (caller must Persist),
// false if loaded from existing file (caller should NOT re-Persist).
func LoadOrGenerate(path string) (freshlyGenerated bool, err error) {
	if _, statErr := os.Stat(path); statErr == nil {
		return false, Load(path)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", path, statErr)
	}
	raw, err := generate()
	if err != nil {
		return false, fmt.Errorf("generate primordial IDs: %w", err)
	}
	if err := populate(raw); err != nil {
		return false, fmt.Errorf("populate primordial IDs: %w", err)
	}
	// In-memory only. Caller must Persist after seeding.
	return true, nil
}

// Persist writes the currently-loaded primordial IDs to path. Call this
// only AFTER seeding succeeds. The JSON file is the artifact of a
// successful bootstrap — its presence implies seeded Core KV.
func Persist(path string) error {
	return persist(path, currentRaw())
}

// currentRaw rebuilds a PrimordialIDsRaw from the populated package vars.
func currentRaw() PrimordialIDsRaw {
	return PrimordialIDsRaw{
		BootstrapOp:             BootstrapOpID,
		BootstrapIdentity:       BootstrapIdentityID,
		PlatformActor:           PlatformActorID,
		MetaRoot:                MetaRootID,
		CapabilityLens:          CapabilityLensID,
		CapabilityRoleIndexLens: CapabilityRoleIndexLensID,
		RoleConsumer:            RoleConsumerID,
		RoleFrontOfHouse:        RoleFrontOfHouseID,
		RoleBackOfHouse:         RoleBackOfHouseID,
		RoleOperator:            RoleOperatorID,
		RolePlatformIntl:        RolePlatformIntlID,
		PermPlatformAny:         PermPlatformAnyID,
		// Story 3.6.
		DDLRole:                  DDLRoleID,
		DDLPermission:            DDLPermissionID,
		DDLHoldsRole:             DDLHoldsRoleID,
		DDLGrantsPermission:      DDLGrantsPermissionID,
		DDLReportsTo:             DDLReportsToID,
		PermCreateRole:           PermCreateRoleID,
		PermUpdateRole:           PermUpdateRoleID,
		PermTombstoneRole:        PermTombstoneRoleID,
		PermCreatePermission:     PermCreatePermissionID,
		PermUpdatePermission:     PermUpdatePermissionID,
		PermTombstonePermission:  PermTombstonePermissionID,
		PermAssignRole:           PermAssignRoleID,
		PermRevokeRole:           PermRevokeRoleID,
		PermGrantPermission:      PermGrantPermissionID,
		PermRevokePermission:     PermRevokePermissionID,
		PermAssignReportingChain: PermAssignReportingChainID,
		PermRemoveReportingChain: PermRemoveReportingChainID,
		// Story 4.1.
		DDLIdentity:                 DDLIdentityID,
		PermCreateUnclaimedIdentity: PermCreateUnclaimedIdentityID,
		PermClaimIdentity:           PermClaimIdentityID,
		PermFlagIdentityForReview:   PermFlagIdentityForReviewID,
		PermApproveIdentityMerge:    PermApproveIdentityMergeID,
		PermScanIdentityDuplicates:  PermScanIdentityDuplicatesID,
	}
}

// Load reads existing IDs from path. Used by read-only callers
// (cmd/refractor-stub, scripts/verify-bootstrap).
func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var f BootstrapFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return populate(f.PrimordialIDs)
}

func generate() (PrimordialIDsRaw, error) {
	var raw PrimordialIDsRaw
	targets := []*string{
		&raw.BootstrapOp,
		&raw.BootstrapIdentity,
		&raw.PlatformActor,
		&raw.MetaRoot,
		&raw.CapabilityLens,
		&raw.CapabilityRoleIndexLens,
		&raw.RoleConsumer,
		&raw.RoleFrontOfHouse,
		&raw.RoleBackOfHouse,
		&raw.RoleOperator,
		&raw.RolePlatformIntl,
		&raw.PermPlatformAny,
		// Story 3.6: DDL meta-vertices.
		&raw.DDLRole,
		&raw.DDLPermission,
		&raw.DDLHoldsRole,
		&raw.DDLGrantsPermission,
		&raw.DDLReportsTo,
		// Story 3.6: per-op permission vertices.
		&raw.PermCreateRole,
		&raw.PermUpdateRole,
		&raw.PermTombstoneRole,
		&raw.PermCreatePermission,
		&raw.PermUpdatePermission,
		&raw.PermTombstonePermission,
		&raw.PermAssignRole,
		&raw.PermRevokeRole,
		&raw.PermGrantPermission,
		&raw.PermRevokePermission,
		&raw.PermAssignReportingChain,
		&raw.PermRemoveReportingChain,
		// Story 4.1: identity DDL + 5 identity permissions.
		&raw.DDLIdentity,
		&raw.PermCreateUnclaimedIdentity,
		&raw.PermClaimIdentity,
		&raw.PermFlagIdentityForReview,
		&raw.PermApproveIdentityMerge,
		&raw.PermScanIdentityDuplicates,
	}
	for _, dst := range targets {
		id, err := substrate.NewNanoID()
		if err != nil {
			return raw, fmt.Errorf("substrate.NewNanoID: %w", err)
		}
		*dst = id
	}
	return raw, nil
}

func populate(raw PrimordialIDsRaw) error {
	// Validate every ID against Contract #1 before assigning.
	fields := []struct {
		name string
		val  string
	}{
		{"bootstrapOp", raw.BootstrapOp},
		{"bootstrapIdentity", raw.BootstrapIdentity},
		{"platformActor", raw.PlatformActor},
		{"metaRoot", raw.MetaRoot},
		{"capabilityLens", raw.CapabilityLens},
		{"capabilityRoleIndexLens", raw.CapabilityRoleIndexLens},
		{"roleConsumer", raw.RoleConsumer},
		{"roleFrontOfHouse", raw.RoleFrontOfHouse},
		{"roleBackOfHouse", raw.RoleBackOfHouse},
		{"roleOperator", raw.RoleOperator},
		{"rolePlatformIntl", raw.RolePlatformIntl},
		{"permPlatformAny", raw.PermPlatformAny},
		// Story 3.6 DDL meta-vertices.
		{"ddlRole", raw.DDLRole},
		{"ddlPermission", raw.DDLPermission},
		{"ddlHoldsRole", raw.DDLHoldsRole},
		{"ddlGrantsPermission", raw.DDLGrantsPermission},
		{"ddlReportsTo", raw.DDLReportsTo},
		// Story 3.6 per-op permission vertices.
		{"permCreateRole", raw.PermCreateRole},
		{"permUpdateRole", raw.PermUpdateRole},
		{"permTombstoneRole", raw.PermTombstoneRole},
		{"permCreatePermission", raw.PermCreatePermission},
		{"permUpdatePermission", raw.PermUpdatePermission},
		{"permTombstonePermission", raw.PermTombstonePermission},
		{"permAssignRole", raw.PermAssignRole},
		{"permRevokeRole", raw.PermRevokeRole},
		{"permGrantPermission", raw.PermGrantPermission},
		{"permRevokePermission", raw.PermRevokePermission},
		{"permAssignReportingChain", raw.PermAssignReportingChain},
		{"permRemoveReportingChain", raw.PermRemoveReportingChain},
		// Story 4.1: identity DDL + 5 identity permissions.
		{"ddlIdentity", raw.DDLIdentity},
		{"permCreateUnclaimedIdentity", raw.PermCreateUnclaimedIdentity},
		{"permClaimIdentity", raw.PermClaimIdentity},
		{"permFlagIdentityForReview", raw.PermFlagIdentityForReview},
		{"permApproveIdentityMerge", raw.PermApproveIdentityMerge},
		{"permScanIdentityDuplicates", raw.PermScanIdentityDuplicates},
	}
	for _, f := range fields {
		if !substrate.IsValidNanoID(f.val) {
			return fmt.Errorf("primordial ID %q is not Contract #1-compliant: %q", f.name, f.val)
		}
	}

	BootstrapOpID = raw.BootstrapOp
	BootstrapIdentityID = raw.BootstrapIdentity
	PlatformActorID = raw.PlatformActor
	MetaRootID = raw.MetaRoot
	CapabilityLensID = raw.CapabilityLens
	CapabilityRoleIndexLensID = raw.CapabilityRoleIndexLens
	RoleConsumerID = raw.RoleConsumer
	RoleFrontOfHouseID = raw.RoleFrontOfHouse
	RoleBackOfHouseID = raw.RoleBackOfHouse
	RoleOperatorID = raw.RoleOperator
	RolePlatformIntlID = raw.RolePlatformIntl
	PermPlatformAnyID = raw.PermPlatformAny

	// Derive keys.
	BootstrapOpKey = "vtx.op." + BootstrapOpID
	BootstrapIdentityKey = "vtx.identity." + BootstrapIdentityID
	PlatformActorKey = "vtx.identity." + PlatformActorID
	MetaRootKey = "vtx.meta." + MetaRootID
	CapabilityLensKey = "vtx.meta." + CapabilityLensID
	CapabilityRoleIndexLensKey = "vtx.meta." + CapabilityRoleIndexLensID
	RoleConsumerKey = "vtx.role." + RoleConsumerID
	RoleFrontOfHouseKey = "vtx.role." + RoleFrontOfHouseID
	RoleBackOfHouseKey = "vtx.role." + RoleBackOfHouseID
	RoleOperatorKey = "vtx.role." + RoleOperatorID
	RolePlatformIntlKey = "vtx.role." + RolePlatformIntlID
	PermPlatformAnyKey = "vtx.permission." + PermPlatformAnyID

	BootstrapHoldsRoleLinkKey = "lnk.identity." + BootstrapIdentityID + ".holdsRole.role." + RolePlatformIntlID
	PlatformHoldsRoleLinkKey = "lnk.identity." + PlatformActorID + ".holdsRole.role." + RolePlatformIntlID
	GrantsPermissionLinkKey = "lnk.permission." + PermPlatformAnyID + ".grantsPermission.role." + RolePlatformIntlID

	// Story 3.6: populate DDL meta-vertex IDs + keys.
	DDLRoleID = raw.DDLRole
	DDLRoleKey = "vtx.meta." + DDLRoleID
	DDLPermissionID = raw.DDLPermission
	DDLPermissionKey = "vtx.meta." + DDLPermissionID
	DDLHoldsRoleID = raw.DDLHoldsRole
	DDLHoldsRoleKey = "vtx.meta." + DDLHoldsRoleID
	DDLGrantsPermissionID = raw.DDLGrantsPermission
	DDLGrantsPermissionKey = "vtx.meta." + DDLGrantsPermissionID
	DDLReportsToID = raw.DDLReportsTo
	DDLReportsToKey = "vtx.meta." + DDLReportsToID

	// Story 3.6: populate per-op permission vertex IDs + keys.
	PermCreateRoleID = raw.PermCreateRole
	PermCreateRoleKey = "vtx.permission." + PermCreateRoleID
	PermUpdateRoleID = raw.PermUpdateRole
	PermUpdateRoleKey = "vtx.permission." + PermUpdateRoleID
	PermTombstoneRoleID = raw.PermTombstoneRole
	PermTombstoneRoleKey = "vtx.permission." + PermTombstoneRoleID
	PermCreatePermissionID = raw.PermCreatePermission
	PermCreatePermissionKey = "vtx.permission." + PermCreatePermissionID
	PermUpdatePermissionID = raw.PermUpdatePermission
	PermUpdatePermissionKey = "vtx.permission." + PermUpdatePermissionID
	PermTombstonePermissionID = raw.PermTombstonePermission
	PermTombstonePermissionKey = "vtx.permission." + PermTombstonePermissionID
	PermAssignRoleID = raw.PermAssignRole
	PermAssignRoleKey = "vtx.permission." + PermAssignRoleID
	PermRevokeRoleID = raw.PermRevokeRole
	PermRevokeRoleKey = "vtx.permission." + PermRevokeRoleID
	PermGrantPermissionID = raw.PermGrantPermission
	PermGrantPermissionKey = "vtx.permission." + PermGrantPermissionID
	PermRevokePermissionID = raw.PermRevokePermission
	PermRevokePermissionKey = "vtx.permission." + PermRevokePermissionID
	PermAssignReportingChainID = raw.PermAssignReportingChain
	PermAssignReportingChainKey = "vtx.permission." + PermAssignReportingChainID
	PermRemoveReportingChainID = raw.PermRemoveReportingChain
	PermRemoveReportingChainKey = "vtx.permission." + PermRemoveReportingChainID

	// Story 4.1: populate identity DDL meta-vertex + 5 permission vertices.
	DDLIdentityID = raw.DDLIdentity
	DDLIdentityKey = "vtx.meta." + DDLIdentityID
	PermCreateUnclaimedIdentityID = raw.PermCreateUnclaimedIdentity
	PermCreateUnclaimedIdentityKey = "vtx.permission." + PermCreateUnclaimedIdentityID
	PermClaimIdentityID = raw.PermClaimIdentity
	PermClaimIdentityKey = "vtx.permission." + PermClaimIdentityID
	PermFlagIdentityForReviewID = raw.PermFlagIdentityForReview
	PermFlagIdentityForReviewKey = "vtx.permission." + PermFlagIdentityForReviewID
	PermApproveIdentityMergeID = raw.PermApproveIdentityMerge
	PermApproveIdentityMergeKey = "vtx.permission." + PermApproveIdentityMergeID
	PermScanIdentityDuplicatesID = raw.PermScanIdentityDuplicates
	PermScanIdentityDuplicatesKey = "vtx.permission." + PermScanIdentityDuplicatesID

	return nil
}

func persist(path string, raw PrimordialIDsRaw) error {
	f := BootstrapFile{
		Version:       "2",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		PrimordialIDs: raw,
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bootstrap file: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// PrimordialVertexKeys returns the complete ordered list of Core KV keys that
// the verify-bootstrap command checks for. MUST be called only after Load
// or LoadOrGenerate has populated the package variables.
func PrimordialVertexKeys() []string {
	keys := []string{
		// bootstrap op tracker
		BootstrapOpKey,
		// system identities
		BootstrapIdentityKey,
		PlatformActorKey,
		// meta vertices
		MetaRootKey,
		CapabilityLensKey,
		CapabilityRoleIndexLensKey,
		// roles
		RoleConsumerKey,
		RoleFrontOfHouseKey,
		RoleBackOfHouseKey,
		RoleOperatorKey,
		RolePlatformIntlKey,
		// permission
		PermPlatformAnyKey,
		// links
		BootstrapHoldsRoleLinkKey,
		PlatformHoldsRoleLinkKey,
		GrantsPermissionLinkKey,
		// Story 3.6: DDL meta-vertices for the role/permission domain.
		DDLRoleKey,
		DDLPermissionKey,
		DDLHoldsRoleKey,
		DDLGrantsPermissionKey,
		DDLReportsToKey,
		// Story 4.1: Identity domain DDL meta-vertex.
		DDLIdentityKey,
	}
	// Story 3.6: 12 per-op permission vertices.
	keys = append(keys, RoleMgmtPermissionKeys()...)
	// Story 3.6: 12 grantsPermission links to operator role.
	keys = append(keys, RoleMgmtGrantLinkKeys()...)
	// Story 4.1: 5 identity-domain permission vertices.
	keys = append(keys, IdentityPermissionKeys()...)
	// Story 4.1: 10 grantsPermission links to per-role grants.
	keys = append(keys, IdentityGrantLinkKeys()...)
	return keys
}

// IdentityPermissionKeys returns the 5 per-op permission vertex keys
// for the identity domain (Story 4.1).
func IdentityPermissionKeys() []string {
	return []string{
		PermCreateUnclaimedIdentityKey,
		PermClaimIdentityKey,
		PermFlagIdentityForReviewKey,
		PermApproveIdentityMergeKey,
		PermScanIdentityDuplicatesKey,
	}
}

// IdentityGrantLinkKeys returns the 10 grantsPermission link keys for
// the identity domain (Story 4.1).
//
// Grant matrix:
//   - CreateUnclaimedIdentity → frontOfHouse, backOfHouse, operator (3)
//   - ClaimIdentity           → consumer (1)
//   - FlagIdentityForReview   → frontOfHouse, backOfHouse, operator (3)
//   - ApproveIdentityMerge    → operator (1)
//   - ScanIdentityDuplicates  → backOfHouse, operator (2)
//
// Link key: lnk.permission.<permID>.grantsPermission.role.<roleID>
// "permission" < "role" alphabetically → permission is younger vertex.
func IdentityGrantLinkKeys() []string {
	mkLink := func(permID, roleID string) string {
		return "lnk.permission." + permID + ".grantsPermission.role." + roleID
	}
	return []string{
		// CreateUnclaimedIdentity → frontOfHouse, backOfHouse, operator.
		mkLink(PermCreateUnclaimedIdentityID, RoleFrontOfHouseID),
		mkLink(PermCreateUnclaimedIdentityID, RoleBackOfHouseID),
		mkLink(PermCreateUnclaimedIdentityID, RoleOperatorID),
		// ClaimIdentity → consumer.
		mkLink(PermClaimIdentityID, RoleConsumerID),
		// FlagIdentityForReview → frontOfHouse, backOfHouse, operator.
		mkLink(PermFlagIdentityForReviewID, RoleFrontOfHouseID),
		mkLink(PermFlagIdentityForReviewID, RoleBackOfHouseID),
		mkLink(PermFlagIdentityForReviewID, RoleOperatorID),
		// ApproveIdentityMerge → operator.
		mkLink(PermApproveIdentityMergeID, RoleOperatorID),
		// ScanIdentityDuplicates → backOfHouse, operator.
		mkLink(PermScanIdentityDuplicatesID, RoleBackOfHouseID),
		mkLink(PermScanIdentityDuplicatesID, RoleOperatorID),
	}
}

// RoleMgmtPermissionKeys returns the 12 per-op permission vertex keys
// for the role-management domain (Story 3.6).
func RoleMgmtPermissionKeys() []string {
	return []string{
		PermCreateRoleKey,
		PermUpdateRoleKey,
		PermTombstoneRoleKey,
		PermCreatePermissionKey,
		PermUpdatePermissionKey,
		PermTombstonePermissionKey,
		PermAssignRoleKey,
		PermRevokeRoleKey,
		PermGrantPermissionKey,
		PermRevokePermissionKey,
		PermAssignReportingChainKey,
		PermRemoveReportingChainKey,
	}
}

// RoleMgmtGrantLinkKeys returns the 12 grantsPermission link keys from each
// per-op permission to the operator role (Story 3.6).
// Link key: lnk.permission.<permID>.grantsPermission.role.<operatorID>
// "permission" < "role" alphabetically → permission is younger vertex (first segment).
func RoleMgmtGrantLinkKeys() []string {
	mkLink := func(permID string) string {
		return "lnk.permission." + permID + ".grantsPermission.role." + RoleOperatorID
	}
	return []string{
		mkLink(PermCreateRoleID),
		mkLink(PermUpdateRoleID),
		mkLink(PermTombstoneRoleID),
		mkLink(PermCreatePermissionID),
		mkLink(PermUpdatePermissionID),
		mkLink(PermTombstonePermissionID),
		mkLink(PermAssignRoleID),
		mkLink(PermRevokeRoleID),
		mkLink(PermGrantPermissionID),
		mkLink(PermRevokePermissionID),
		mkLink(PermAssignReportingChainID),
		mkLink(PermRemoveReportingChainID),
	}
}

// PrimordialVertexKeyCount is the total number of primordial Core KV keys
// (vertices + links). Used by cmd/refractor-stub as a count-only readiness
// gate (avoids needing to load lattice.bootstrap.json before the file is
// written by cmd/bootstrap — which would create a startup race).
// Story 3.6 adds 5 DDL meta-vertex keys + 12 permission keys + 12 link keys = 29
// Story 4.1 adds 1 DDL meta-vertex key + 5 permission keys + 10 link keys = 16
// Total: 15 (prior) + 5 + 12 + 12 + 1 + 5 + 10 = 60
const PrimordialVertexKeyCount = 60
