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
//     LoadOrGenerate(path). The file does not exist, so a fresh
//     primordial set is generated via substrate.NewNanoID(), the
//     package-level ID variables are populated, and the resulting set
//     is persisted to lattice.bootstrap.json.
//   - Subsequent `make up` within the same deployment (file still
//     present, e.g. on retry of a failed run): LoadOrGenerate sees
//     the file, calls Load, and the same IDs are reused — idempotent.
//   - `make down` deletes lattice.bootstrap.json (Makefile); the next
//     `make up` generates a fresh primordial key space.
//   - Read-only callers (cmd/refractor-stub via count-only gate;
//     scripts/verify-kernel.go): call Load(path) explicitly.
//
// All generated IDs are Contract #1-compliant by construction (20-char,
// custom 58-char alphabet, no I/l/O/0) since they come from
// substrate.NewNanoID() which is the canonical generator.
//
// This file keeps only kernel NanoIDs — the minimal primordial set.
// Identity-domain and rbac-domain NanoID surfaces live in their
// respective Capability Packages.
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

// CompensationAspectClass is the class and localName value for the
// .compensation self-description aspect. It is a key suffix token,
// not a NanoID — no primordial ID is required.
const CompensationAspectClass = "compensation"

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
	BootstrapOpID              string
	BootstrapOpKey             string
	BootstrapIdentityID        string
	BootstrapIdentityKey       string
	MetaRootID                 string
	MetaRootKey                string
	CapabilityLensID           string
	CapabilityLensKey          string
	CapabilityRoleIndexLensID  string
	CapabilityRoleIndexLensKey string
	RoleOperatorID             string
	RoleOperatorKey            string

	// Three kernel-seeded meta-permission NanoIDs authorizing the operator
	// to mutate vtx.meta.* vertices (CreateMetaVertex, UpdateMetaVertex,
	// TombstoneMetaVertex). These are the entry point for package-installed
	// DDLs and Lenses.
	PermCreateMetaVertexID     string
	PermCreateMetaVertexKey    string
	PermUpdateMetaVertexID     string
	PermUpdateMetaVertexKey    string
	PermTombstoneMetaVertexID  string
	PermTombstoneMetaVertexKey string

	// InstallPackage / UninstallPackage primordial DDL meta-vertices.
	// These route capability-package install/uninstall through the
	// Processor (Story 1.5.5). Each is a meta.ddl.vertexType keyed by
	// NanoID with canonicalName "InstallPackage" / "UninstallPackage".
	InstallPackageDDLID    string
	InstallPackageDDLKey   string
	UninstallPackageDDLID  string
	UninstallPackageDDLKey string

	// Two meta-permission NanoIDs authorizing the operator to submit the
	// InstallPackage / UninstallPackage ops. Granted to the operator role,
	// which the primordial admin holds.
	PermInstallPackageID      string
	PermInstallPackageKey     string
	PermUninstallPackageID    string
	PermUninstallPackageKey   string

	// Five aspect-type meta-vertex NanoIDs — the primordial DDLs for the
	// self-description aspect classes (description, inputSchema,
	// outputSchema, fieldDescription, examples).
	AspectTypeDescriptionID       string
	AspectTypeDescriptionKey      string
	AspectTypeInputSchemaID       string
	AspectTypeInputSchemaKey      string
	AspectTypeOutputSchemaID      string
	AspectTypeOutputSchemaKey     string
	AspectTypeFieldDescriptionID  string
	AspectTypeFieldDescriptionKey string
	AspectTypeExamplesID          string
	AspectTypeExamplesKey         string

	// Derived link keys.
	BootstrapHoldsRoleLinkKey string
)

// PrimordialIDsRaw is the persisted form (matches lattice.bootstrap.json).
// Identity-domain and rbac-domain NanoID fields are not present in this
// struct; encoding/json ignores unknown fields so older files that include
// extra fields still parse correctly.
type PrimordialIDsRaw struct {
	BootstrapOp             string `json:"bootstrapOp"`
	BootstrapIdentity       string `json:"bootstrapIdentity"`
	MetaRoot                string `json:"metaRoot"`
	CapabilityLens          string `json:"capabilityLens"`
	CapabilityRoleIndexLens string `json:"capabilityRoleIndexLens"`
	RoleOperator            string `json:"roleOperator"`

	// Meta-permission NanoIDs.
	PermCreateMetaVertex    string `json:"permCreateMetaVertex"`
	PermUpdateMetaVertex    string `json:"permUpdateMetaVertex"`
	PermTombstoneMetaVertex string `json:"permTombstoneMetaVertex"`

	// InstallPackage / UninstallPackage DDL + permission NanoIDs (Story 1.5.5).
	InstallPackageDDL    string `json:"installPackageDDL"`
	UninstallPackageDDL  string `json:"uninstallPackageDDL"`
	PermInstallPackage   string `json:"permInstallPackage"`
	PermUninstallPackage string `json:"permUninstallPackage"`

	// Aspect-type meta-vertex NanoIDs.
	AspectTypeDescription       string `json:"aspectTypeDescription"`
	AspectTypeInputSchema        string `json:"aspectTypeInputSchema"`
	AspectTypeOutputSchema       string `json:"aspectTypeOutputSchema"`
	AspectTypeFieldDescription   string `json:"aspectTypeFieldDescription"`
	AspectTypeExamples           string `json:"aspectTypeExamples"`
}

// BootstrapFile is the wire format of lattice.bootstrap.json.
//
// Version history:
//   - "1"-"2": retired; fields removed.
//   - "3": aspect-type meta-vertex NanoIDs added (current stable format).
//   - "4": status field added to survive crash between SeedPrimordial and
//     Persist. status="in-progress" means IDs are stable but seeding may
//     not have completed; status="committed" means seeding and persist both
//     succeeded.
//   - "5": InstallPackage/UninstallPackage primordial DDL + permission
//     NanoIDs added (Story 1.5.5 — package installs route through the
//     Processor).
type BootstrapFile struct {
	Version       string           `json:"version"`
	GeneratedAt   string           `json:"generatedAt"`
	Status        string           `json:"status,omitempty"` // "in-progress" | "committed"
	PrimordialIDs PrimordialIDsRaw `json:"primordialIDs"`
}

// LoadOrGenerate is called by cmd/bootstrap. It implements a two-phase
// commit protocol to survive crashes between SeedPrimordial and Persist:
//
//  1. If no file exists: generate fresh IDs, write the file with
//     status="in-progress" (so the IDs survive a crash), then return
//     freshlyGenerated=true. Caller MUST call PersistCommitted after
//     SeedPrimordial succeeds.
//
//  2. If file exists with status="in-progress": reuse the IDs already
//     written (crash recovery — SeedPrimordial may or may not have
//     completed; its own idempotency guard handles the case where it did).
//     Returns freshlyGenerated=true so the caller re-runs SeedPrimordial.
//
//  3. If file exists with status="committed" (or legacy v3 without status):
//     load IDs and return freshlyGenerated=false (seeding already done).
//
// The "generate" path produces a unique primordial key space PER
// DEPLOYMENT — this is the architectural requirement.
//
// Returns true if the caller must run SeedPrimordial + PersistCommitted.
func LoadOrGenerate(path string) (freshlyGenerated bool, err error) {
	if _, statErr := os.Stat(path); statErr == nil {
		// File exists — check its status.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return false, fmt.Errorf("read %s: %w", path, readErr)
		}
		var f BootstrapFile
		if parseErr := json.Unmarshal(data, &f); parseErr != nil {
			return false, fmt.Errorf("parse %s: %w", path, parseErr)
		}
		if err := checkVersion(f); err != nil {
			return false, err
		}
		if f.Status == "in-progress" {
			// Crash recovery: IDs are stable; re-run seeding.
			if popErr := populate(f.PrimordialIDs); popErr != nil {
				return false, fmt.Errorf("populate primordial IDs: %w", popErr)
			}
			return true, nil
		}
		// status="committed" or legacy v3 (treated as committed).
		if popErr := populate(f.PrimordialIDs); popErr != nil {
			return false, fmt.Errorf("populate primordial IDs: %w", popErr)
		}
		return false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", path, statErr)
	}

	// No file: generate fresh IDs and write in-progress immediately.
	raw, err := generate()
	if err != nil {
		return false, fmt.Errorf("generate primordial IDs: %w", err)
	}
	if err := populate(raw); err != nil {
		return false, fmt.Errorf("populate primordial IDs: %w", err)
	}
	if err := persistWithStatus(path, raw, "in-progress"); err != nil {
		return false, fmt.Errorf("write in-progress bootstrap file: %w", err)
	}
	return true, nil
}

// PersistCommitted rewrites lattice.bootstrap.json with status="committed"
// after SeedPrimordial has successfully committed all primordial keys.
// Call this instead of Persist when using the two-phase commit protocol.
func PersistCommitted(path string) error {
	return persistWithStatus(path, currentRaw(), "committed")
}

// Persist writes the currently-loaded primordial IDs to path with
// status="committed". Retained for callers that do not use the
// two-phase LoadOrGenerate / PersistCommitted protocol.
func Persist(path string) error {
	return persistWithStatus(path, currentRaw(), "committed")
}

// currentRaw rebuilds a PrimordialIDsRaw from the populated package vars.
func currentRaw() PrimordialIDsRaw {
	return PrimordialIDsRaw{
		BootstrapOp:                BootstrapOpID,
		BootstrapIdentity:          BootstrapIdentityID,
		MetaRoot:                   MetaRootID,
		CapabilityLens:             CapabilityLensID,
		CapabilityRoleIndexLens:    CapabilityRoleIndexLensID,
		RoleOperator:               RoleOperatorID,
		PermCreateMetaVertex:       PermCreateMetaVertexID,
		PermUpdateMetaVertex:       PermUpdateMetaVertexID,
		PermTombstoneMetaVertex:    PermTombstoneMetaVertexID,
		InstallPackageDDL:          InstallPackageDDLID,
		UninstallPackageDDL:        UninstallPackageDDLID,
		PermInstallPackage:         PermInstallPackageID,
		PermUninstallPackage:       PermUninstallPackageID,
		AspectTypeDescription:      AspectTypeDescriptionID,
		AspectTypeInputSchema:      AspectTypeInputSchemaID,
		AspectTypeOutputSchema:     AspectTypeOutputSchemaID,
		AspectTypeFieldDescription: AspectTypeFieldDescriptionID,
		AspectTypeExamples:         AspectTypeExamplesID,
	}
}

// Load reads existing IDs from path. Used by read-only callers
// (scripts/verify-kernel).
func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var f BootstrapFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := checkVersion(f); err != nil {
		return err
	}
	return populate(f.PrimordialIDs)
}

// checkVersion returns a clear error when the bootstrap file's version is
// not one of the supported versions. This surfaces a meaningful message
// instead of a confusing NanoID validation failure when an operator
// upgrades Lattice without running `make down` first. Version 5 adds the
// InstallPackage/UninstallPackage primordial NanoIDs; older files lack
// them and must be regenerated.
func checkVersion(f BootstrapFile) error {
	switch f.Version {
	case "5":
		return nil
	default:
		return fmt.Errorf(
			"bootstrap file version mismatch: got %q, want \"5\" — run `make down && make up`",
			f.Version,
		)
	}
}

func generate() (PrimordialIDsRaw, error) {
	var raw PrimordialIDsRaw
	targets := []*string{
		&raw.BootstrapOp,
		&raw.BootstrapIdentity,
		&raw.MetaRoot,
		&raw.CapabilityLens,
		&raw.CapabilityRoleIndexLens,
		&raw.RoleOperator,
		&raw.PermCreateMetaVertex,
		&raw.PermUpdateMetaVertex,
		&raw.PermTombstoneMetaVertex,
		&raw.InstallPackageDDL,
		&raw.UninstallPackageDDL,
		&raw.PermInstallPackage,
		&raw.PermUninstallPackage,
		&raw.AspectTypeDescription,
		&raw.AspectTypeInputSchema,
		&raw.AspectTypeOutputSchema,
		&raw.AspectTypeFieldDescription,
		&raw.AspectTypeExamples,
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
		{"metaRoot", raw.MetaRoot},
		{"capabilityLens", raw.CapabilityLens},
		{"capabilityRoleIndexLens", raw.CapabilityRoleIndexLens},
		{"roleOperator", raw.RoleOperator},
		{"permCreateMetaVertex", raw.PermCreateMetaVertex},
		{"permUpdateMetaVertex", raw.PermUpdateMetaVertex},
		{"permTombstoneMetaVertex", raw.PermTombstoneMetaVertex},
		{"installPackageDDL", raw.InstallPackageDDL},
		{"uninstallPackageDDL", raw.UninstallPackageDDL},
		{"permInstallPackage", raw.PermInstallPackage},
		{"permUninstallPackage", raw.PermUninstallPackage},
		{"aspectTypeDescription", raw.AspectTypeDescription},
		{"aspectTypeInputSchema", raw.AspectTypeInputSchema},
		{"aspectTypeOutputSchema", raw.AspectTypeOutputSchema},
		{"aspectTypeFieldDescription", raw.AspectTypeFieldDescription},
		{"aspectTypeExamples", raw.AspectTypeExamples},
	}
	for _, f := range fields {
		if !substrate.IsValidNanoID(f.val) {
			return fmt.Errorf("primordial ID %q is not Contract #1-compliant: %q", f.name, f.val)
		}
	}

	BootstrapOpID = raw.BootstrapOp
	BootstrapIdentityID = raw.BootstrapIdentity
	MetaRootID = raw.MetaRoot
	CapabilityLensID = raw.CapabilityLens
	CapabilityRoleIndexLensID = raw.CapabilityRoleIndexLens
	RoleOperatorID = raw.RoleOperator

	PermCreateMetaVertexID = raw.PermCreateMetaVertex
	PermCreateMetaVertexKey = "vtx.permission." + PermCreateMetaVertexID
	PermUpdateMetaVertexID = raw.PermUpdateMetaVertex
	PermUpdateMetaVertexKey = "vtx.permission." + PermUpdateMetaVertexID
	PermTombstoneMetaVertexID = raw.PermTombstoneMetaVertex
	PermTombstoneMetaVertexKey = "vtx.permission." + PermTombstoneMetaVertexID

	InstallPackageDDLID = raw.InstallPackageDDL
	InstallPackageDDLKey = "vtx.meta." + InstallPackageDDLID
	UninstallPackageDDLID = raw.UninstallPackageDDL
	UninstallPackageDDLKey = "vtx.meta." + UninstallPackageDDLID
	PermInstallPackageID = raw.PermInstallPackage
	PermInstallPackageKey = "vtx.permission." + PermInstallPackageID
	PermUninstallPackageID = raw.PermUninstallPackage
	PermUninstallPackageKey = "vtx.permission." + PermUninstallPackageID

	AspectTypeDescriptionID = raw.AspectTypeDescription
	AspectTypeDescriptionKey = "vtx.meta." + AspectTypeDescriptionID
	AspectTypeInputSchemaID = raw.AspectTypeInputSchema
	AspectTypeInputSchemaKey = "vtx.meta." + AspectTypeInputSchemaID
	AspectTypeOutputSchemaID = raw.AspectTypeOutputSchema
	AspectTypeOutputSchemaKey = "vtx.meta." + AspectTypeOutputSchemaID
	AspectTypeFieldDescriptionID = raw.AspectTypeFieldDescription
	AspectTypeFieldDescriptionKey = "vtx.meta." + AspectTypeFieldDescriptionID
	AspectTypeExamplesID = raw.AspectTypeExamples
	AspectTypeExamplesKey = "vtx.meta." + AspectTypeExamplesID

	// Derive keys.
	BootstrapOpKey = "vtx.op." + BootstrapOpID
	BootstrapIdentityKey = "vtx.identity." + BootstrapIdentityID
	MetaRootKey = "vtx.meta." + MetaRootID
	CapabilityLensKey = "vtx.meta." + CapabilityLensID
	CapabilityRoleIndexLensKey = "vtx.meta." + CapabilityRoleIndexLensID
	RoleOperatorKey = "vtx.role." + RoleOperatorID

	// Admin's primordial holdsRole link targets the operator role.
	BootstrapHoldsRoleLinkKey = "lnk.identity." + BootstrapIdentityID + ".holdsRole.role." + RoleOperatorID

	return nil
}

func persistWithStatus(path string, raw PrimordialIDsRaw, status string) error {
	f := BootstrapFile{
		Version:       "5",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Status:        status,
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

// PrimordialVertexKeys returns the kernel's top-level vertex keys — only
// those entries the kernel itself seeds. Package-installed DDLs/Lenses/
// permissions are addressed separately by `verify-package-*` gates.
func PrimordialVertexKeys() []string {
	return []string{
		// bootstrap op tracker
		BootstrapOpKey,
		// admin identity
		BootstrapIdentityKey,
		// meta-meta DDL
		MetaRootKey,
		// InstallPackage / UninstallPackage primordial DDLs (Story 1.5.5)
		InstallPackageDDLKey,
		UninstallPackageDDLKey,
		// 2 Lens definitions
		CapabilityLensKey,
		CapabilityRoleIndexLensKey,
		// operator role
		RoleOperatorKey,
		// 3 kernel meta-permissions + 2 package-install permissions
		PermCreateMetaVertexKey,
		PermUpdateMetaVertexKey,
		PermTombstoneMetaVertexKey,
		PermInstallPackageKey,
		PermUninstallPackageKey,
		// 5 grantedBy links (meta-perm + install perms → operator) + admin holdsRole link
		"lnk.permission." + PermCreateMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermUpdateMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermTombstoneMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermInstallPackageID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermUninstallPackageID + ".grantedBy.role." + RoleOperatorID,
		BootstrapHoldsRoleLinkKey,
		// 5 aspect-type meta-vertices (self-description DDLs)
		AspectTypeDescriptionKey,
		AspectTypeInputSchemaKey,
		AspectTypeOutputSchemaKey,
		AspectTypeFieldDescriptionKey,
		AspectTypeExamplesKey,
	}
}

// PrimordialVertexKeyCount is the count of TOP-LEVEL kernel keys (the
// ones in PrimordialVertexKeys()). Used as a count-only readiness gate
// where loading lattice.bootstrap.json would race startup. Current count
// is 25 entries: 1 op + 1 admin + 1 meta-DDL + 2 install/uninstall DDLs +
// 2 lenses + 1 role + 5 perms (3 meta + 2 install) + 6 links + 5
// aspect-type meta-vertices.
const PrimordialVertexKeyCount = 25
