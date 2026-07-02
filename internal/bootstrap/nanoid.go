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
	BootstrapOpID        string
	BootstrapOpKey       string
	BootstrapIdentityID  string
	BootstrapIdentityKey string

	// Internal service-actor identities (arch §92). Loom, Weaver, the
	// Bridge, and the object-store-manager operate within the trust boundary
	// at root-equivalent capability, submitting ops directly to the ledger.
	// Root-equivalence is established purely by their holdsRole link to the
	// operator role (Contract #7 §7.7) — the class field never gates capability.
	LoomIdentityID    string
	LoomIdentityKey   string
	WeaverIdentityID  string
	WeaverIdentityKey string
	BridgeIdentityID  string
	BridgeIdentityKey string
	ObjmgrIdentityID  string
	ObjmgrIdentityKey string
	// PrivacyIdentity is the privacy-plane service actor (Contract #7 §7.2's
	// "additional internal service actor" pattern): the crypto-shred
	// finalization listeners — internal/privacyworker (Vault key destruction)
	// and internal/refractor/keyshredded (projection nullification) — submit
	// RecordShredFinalization ops under it so shred progress is durably
	// recorded in Core KV (vault-crypto-shredding-design.md §2.4, Fire 4b).
	PrivacyIdentityID  string
	PrivacyIdentityKey string

	MetaRootID        string
	MetaRootKey       string
	CapabilityLensID  string
	CapabilityLensKey string
	// CapabilityReadLens is the base read-path authorization lens (Contract #6
	// §6.14, D1). It projects each actor's self + primordial readable anchors
	// to cap-read.<actor> in the Capability KV bucket; package lenses contribute
	// the rest of the read-grant union (cap-read.roles, cap-read.residence, …).
	CapabilityReadLensID  string
	CapabilityReadLensKey string
	// CapabilityReadGrantsLens is the base read-grant PRODUCER (Contract #6
	// §6.14, D1.3) — the Postgres GrantTable twin of CapabilityReadLens. It
	// projects each actor's self-anchor as an (actor_id, anchor_id, grant_source)
	// row into the shared actor_read_grants table, the source of truth the
	// Postgres-RLS enforcement boundary reads. Without it the grant table is
	// empty and RLS denies every protected read.
	CapabilityReadGrantsLensID  string
	CapabilityReadGrantsLensKey string
	// CapabilityReadWildcardGrantsLens is the base ALL-ACCESS read-grant
	// PRODUCER (Contract #6 §6.14, D1 design §3.4 M5) — the wildcard sibling
	// of CapabilityReadGrantsLens. It grants the reserved WildcardAnchor ("*")
	// to the same fixed, kernel-seeded root-equivalent identities the
	// write-path CapabilityLens special-cases, so an all-access read (e.g. a
	// clinic staff/admin worklist) passes through the §6.14 set-membership
	// RLS policy rather than bypassing it.
	CapabilityReadWildcardGrantsLensID  string
	CapabilityReadWildcardGrantsLensKey string
	RoleOperatorID                      string
	RoleOperatorKey                     string

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
	UpgradePackageDDLID    string
	UpgradePackageDDLKey   string

	// Meta-permission NanoIDs authorizing the operator to submit the
	// InstallPackage / UninstallPackage / UpgradePackage ops. Granted to the
	// operator role, which the primordial admin holds.
	PermInstallPackageID    string
	PermInstallPackageKey   string
	PermUninstallPackageID  string
	PermUninstallPackageKey string
	PermUpgradePackageID    string
	PermUpgradePackageKey   string

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
	LoomHoldsRoleLinkKey      string
	WeaverHoldsRoleLinkKey    string
	BridgeHoldsRoleLinkKey    string
	ObjmgrHoldsRoleLinkKey    string
	PrivacyHoldsRoleLinkKey   string
)

// PrimordialIDsRaw is the persisted form (matches lattice.bootstrap.json).
// Identity-domain and rbac-domain NanoID fields are not present in this
// struct; encoding/json ignores unknown fields so older files that include
// extra fields still parse correctly.
type PrimordialIDsRaw struct {
	BootstrapOp                  string `json:"bootstrapOp"`
	BootstrapIdentity            string `json:"bootstrapIdentity"`
	LoomIdentity                 string `json:"loomIdentity"`
	WeaverIdentity               string `json:"weaverIdentity"`
	BridgeIdentity               string `json:"bridgeIdentity"`
	ObjmgrIdentity               string `json:"objmgrIdentity"`
	PrivacyIdentity              string `json:"privacyIdentity"`
	MetaRoot                     string `json:"metaRoot"`
	CapabilityLens               string `json:"capabilityLens"`
	CapabilityReadLens           string `json:"capabilityReadLens"`
	CapabilityReadGrants         string `json:"capabilityReadGrants"`
	CapabilityReadWildcardGrants string `json:"capabilityReadWildcardGrants"`
	RoleOperator                 string `json:"roleOperator"`

	// Meta-permission NanoIDs.
	PermCreateMetaVertex    string `json:"permCreateMetaVertex"`
	PermUpdateMetaVertex    string `json:"permUpdateMetaVertex"`
	PermTombstoneMetaVertex string `json:"permTombstoneMetaVertex"`

	// InstallPackage / UninstallPackage DDL + permission NanoIDs (Story 1.5.5).
	InstallPackageDDL    string `json:"installPackageDDL"`
	UninstallPackageDDL  string `json:"uninstallPackageDDL"`
	UpgradePackageDDL    string `json:"upgradePackageDDL"`
	PermInstallPackage   string `json:"permInstallPackage"`
	PermUninstallPackage string `json:"permUninstallPackage"`
	PermUpgradePackage   string `json:"permUpgradePackage"`

	// Aspect-type meta-vertex NanoIDs.
	AspectTypeDescription      string `json:"aspectTypeDescription"`
	AspectTypeInputSchema      string `json:"aspectTypeInputSchema"`
	AspectTypeOutputSchema     string `json:"aspectTypeOutputSchema"`
	AspectTypeFieldDescription string `json:"aspectTypeFieldDescription"`
	AspectTypeExamples         string `json:"aspectTypeExamples"`
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
//   - "6": Loom + Weaver internal service-actor identity NanoIDs added
//     (arch §92 — root-equivalent service actors seeded at bootstrap).
//   - "7": capabilityRoleIndex Lens NanoID removed — the role-by-operation
//     index moved to the rbac-domain package (it is rbac vocabulary), so core
//     no longer seeds or addresses it.
//   - "8": Bridge internal service-actor identity NanoID added (Epic 13 —
//     External I/O Bridge service actor).
//   - "9": one Weaver operational KV bucket retired (Epic 13 — external I/O
//     re-homed to Loom's externalTask + the bridge). Dropping a provisioned
//     bucket changes the kernel topology a stale file would not match.
//   - "10": core-objects Object Store added (the off-graph blob plane).
//     Provisioning a new store changes kernel topology a stale file would not
//     match, so the version is bumped to force `make down && make up`. No
//     primordial NanoIDs or Core-KV keys are added (the store holds bytes, not
//     vertices), so PrimordialVertexKeyCount is unchanged.
//   - "11": object-store-manager internal service-actor identity NanoID added
//     (the v1b GC owner-tombstone-cascade trigger — §22 — submits DetachObject,
//     so the byte-janitor gains a root-equivalent actor via holdsRole→operator,
//     mirroring Loom/Weaver/Bridge). Adds 2 Core-KV keys (the identity vertex +
//     its holdsRole link), so PrimordialVertexKeyCount moves 29 → 31.
//   - "12": UpgradePackage primordial DDL + its operator permission added
//     (Contract #8 §8.6 — in-place package upgrade). Adds 3 top-level Core-KV
//     keys (the UpgradePackage DDL meta-vertex, the UpgradePackage permission,
//     and the permission→operator grant link), so PrimordialVertexKeyCount
//     moves 31 → 34. A stale file lacks the two new NanoID fields, so the
//     version bump forces regeneration.
//   - "13": capabilityReadGrants primordial lens added — the base read-grant
//     PRODUCER (Contract #6 §6.14, D1.3) that projects each actor's self-anchor
//     into the Postgres actor_read_grants table for RLS enforcement. Like its
//     NATS-KV twin capabilityReadLens it is seeded + aspect-verified but kept
//     OUT of the PrimordialVertexKeys() count-only readiness list, so
//     PrimordialVertexKeyCount is unchanged (34). A stale file lacks the new
//     NanoID field, so the version bump forces regeneration.
//   - "14": capabilityReadWildcardGrants primordial lens added — the base
//     ALL-ACCESS read-grant PRODUCER (Contract #6 §6.14, D1 design §3.4 M5)
//     that grants the reserved WildcardAnchor ("*") to the fixed,
//     kernel-seeded root-equivalent identities (admin + Loom/Weaver/Bridge/
//     object-store-manager), so an all-access read passes through RLS rather
//     than bypassing it. Like capabilityReadGrants it is seeded +
//     aspect-verified but kept OUT of the PrimordialVertexKeys() count-only
//     readiness list, so PrimordialVertexKeyCount is unchanged (34). A stale
//     file lacks the new NanoID field, so the version bump forces
//     regeneration.
//   - "15": privacy internal service-actor identity NanoID added (the
//     crypto-shred finalization actor, vault-crypto-shredding-design.md Fire
//     4b — internal/privacyworker + internal/refractor/keyshredded submit
//     RecordShredFinalization under it, mirroring Loom/Weaver/Bridge/objmgr).
//     Adds 2 Core-KV keys (the identity vertex + its holdsRole link), so
//     PrimordialVertexKeyCount moves 34 → 36.
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
		BootstrapOp:                  BootstrapOpID,
		BootstrapIdentity:            BootstrapIdentityID,
		LoomIdentity:                 LoomIdentityID,
		WeaverIdentity:               WeaverIdentityID,
		BridgeIdentity:               BridgeIdentityID,
		ObjmgrIdentity:               ObjmgrIdentityID,
		PrivacyIdentity:              PrivacyIdentityID,
		MetaRoot:                     MetaRootID,
		CapabilityLens:               CapabilityLensID,
		CapabilityReadLens:           CapabilityReadLensID,
		CapabilityReadGrants:         CapabilityReadGrantsLensID,
		CapabilityReadWildcardGrants: CapabilityReadWildcardGrantsLensID,
		RoleOperator:                 RoleOperatorID,
		PermCreateMetaVertex:         PermCreateMetaVertexID,
		PermUpdateMetaVertex:         PermUpdateMetaVertexID,
		PermTombstoneMetaVertex:      PermTombstoneMetaVertexID,
		InstallPackageDDL:            InstallPackageDDLID,
		UninstallPackageDDL:          UninstallPackageDDLID,
		UpgradePackageDDL:            UpgradePackageDDLID,
		PermInstallPackage:           PermInstallPackageID,
		PermUninstallPackage:         PermUninstallPackageID,
		PermUpgradePackage:           PermUpgradePackageID,
		AspectTypeDescription:        AspectTypeDescriptionID,
		AspectTypeInputSchema:        AspectTypeInputSchemaID,
		AspectTypeOutputSchema:       AspectTypeOutputSchemaID,
		AspectTypeFieldDescription:   AspectTypeFieldDescriptionID,
		AspectTypeExamples:           AspectTypeExamplesID,
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
// upgrades Lattice without running `make down` first. Version 15 adds the
// privacyIdentity service actor — the crypto-shred finalization actor
// (vault-crypto-shredding-design.md Fire 4b); older files lack its NanoID
// field and must be regenerated so the kernel topology matches.
func checkVersion(f BootstrapFile) error {
	switch f.Version {
	case "15":
		return nil
	default:
		return fmt.Errorf(
			"bootstrap file version mismatch: got %q, want \"15\" — run `make down && make up`",
			f.Version,
		)
	}
}

func generate() (PrimordialIDsRaw, error) {
	var raw PrimordialIDsRaw
	targets := []*string{
		&raw.BootstrapOp,
		&raw.BootstrapIdentity,
		&raw.LoomIdentity,
		&raw.WeaverIdentity,
		&raw.BridgeIdentity,
		&raw.ObjmgrIdentity,
		&raw.PrivacyIdentity,
		&raw.MetaRoot,
		&raw.CapabilityLens,
		&raw.CapabilityReadLens,
		&raw.CapabilityReadGrants,
		&raw.CapabilityReadWildcardGrants,
		&raw.RoleOperator,
		&raw.PermCreateMetaVertex,
		&raw.PermUpdateMetaVertex,
		&raw.PermTombstoneMetaVertex,
		&raw.InstallPackageDDL,
		&raw.UninstallPackageDDL,
		&raw.UpgradePackageDDL,
		&raw.PermInstallPackage,
		&raw.PermUninstallPackage,
		&raw.PermUpgradePackage,
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
		{"loomIdentity", raw.LoomIdentity},
		{"weaverIdentity", raw.WeaverIdentity},
		{"bridgeIdentity", raw.BridgeIdentity},
		{"objmgrIdentity", raw.ObjmgrIdentity},
		{"privacyIdentity", raw.PrivacyIdentity},
		{"metaRoot", raw.MetaRoot},
		{"capabilityLens", raw.CapabilityLens},
		{"capabilityReadLens", raw.CapabilityReadLens},
		{"capabilityReadGrants", raw.CapabilityReadGrants},
		{"capabilityReadWildcardGrants", raw.CapabilityReadWildcardGrants},
		{"roleOperator", raw.RoleOperator},
		{"permCreateMetaVertex", raw.PermCreateMetaVertex},
		{"permUpdateMetaVertex", raw.PermUpdateMetaVertex},
		{"permTombstoneMetaVertex", raw.PermTombstoneMetaVertex},
		{"installPackageDDL", raw.InstallPackageDDL},
		{"uninstallPackageDDL", raw.UninstallPackageDDL},
		{"upgradePackageDDL", raw.UpgradePackageDDL},
		{"permInstallPackage", raw.PermInstallPackage},
		{"permUninstallPackage", raw.PermUninstallPackage},
		{"permUpgradePackage", raw.PermUpgradePackage},
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
	LoomIdentityID = raw.LoomIdentity
	WeaverIdentityID = raw.WeaverIdentity
	BridgeIdentityID = raw.BridgeIdentity
	ObjmgrIdentityID = raw.ObjmgrIdentity
	PrivacyIdentityID = raw.PrivacyIdentity
	MetaRootID = raw.MetaRoot
	CapabilityLensID = raw.CapabilityLens
	CapabilityReadLensID = raw.CapabilityReadLens
	CapabilityReadGrantsLensID = raw.CapabilityReadGrants
	CapabilityReadWildcardGrantsLensID = raw.CapabilityReadWildcardGrants
	RoleOperatorID = raw.RoleOperator

	PermCreateMetaVertexID = raw.PermCreateMetaVertex
	PermCreateMetaVertexKey = substrate.VertexKey("permission", PermCreateMetaVertexID)
	PermUpdateMetaVertexID = raw.PermUpdateMetaVertex
	PermUpdateMetaVertexKey = substrate.VertexKey("permission", PermUpdateMetaVertexID)
	PermTombstoneMetaVertexID = raw.PermTombstoneMetaVertex
	PermTombstoneMetaVertexKey = substrate.VertexKey("permission", PermTombstoneMetaVertexID)

	InstallPackageDDLID = raw.InstallPackageDDL
	InstallPackageDDLKey = substrate.VertexKey("meta", InstallPackageDDLID)
	UninstallPackageDDLID = raw.UninstallPackageDDL
	UninstallPackageDDLKey = substrate.VertexKey("meta", UninstallPackageDDLID)
	UpgradePackageDDLID = raw.UpgradePackageDDL
	UpgradePackageDDLKey = substrate.VertexKey("meta", UpgradePackageDDLID)
	PermInstallPackageID = raw.PermInstallPackage
	PermInstallPackageKey = substrate.VertexKey("permission", PermInstallPackageID)
	PermUninstallPackageID = raw.PermUninstallPackage
	PermUninstallPackageKey = substrate.VertexKey("permission", PermUninstallPackageID)
	PermUpgradePackageID = raw.PermUpgradePackage
	PermUpgradePackageKey = substrate.VertexKey("permission", PermUpgradePackageID)

	AspectTypeDescriptionID = raw.AspectTypeDescription
	AspectTypeDescriptionKey = substrate.VertexKey("meta", AspectTypeDescriptionID)
	AspectTypeInputSchemaID = raw.AspectTypeInputSchema
	AspectTypeInputSchemaKey = substrate.VertexKey("meta", AspectTypeInputSchemaID)
	AspectTypeOutputSchemaID = raw.AspectTypeOutputSchema
	AspectTypeOutputSchemaKey = substrate.VertexKey("meta", AspectTypeOutputSchemaID)
	AspectTypeFieldDescriptionID = raw.AspectTypeFieldDescription
	AspectTypeFieldDescriptionKey = substrate.VertexKey("meta", AspectTypeFieldDescriptionID)
	AspectTypeExamplesID = raw.AspectTypeExamples
	AspectTypeExamplesKey = substrate.VertexKey("meta", AspectTypeExamplesID)

	// Derive keys.
	BootstrapOpKey = substrate.VertexKey("op", BootstrapOpID)
	BootstrapIdentityKey = substrate.VertexKey("identity", BootstrapIdentityID)
	LoomIdentityKey = substrate.VertexKey("identity", LoomIdentityID)
	WeaverIdentityKey = substrate.VertexKey("identity", WeaverIdentityID)
	BridgeIdentityKey = substrate.VertexKey("identity", BridgeIdentityID)
	ObjmgrIdentityKey = substrate.VertexKey("identity", ObjmgrIdentityID)
	PrivacyIdentityKey = substrate.VertexKey("identity", PrivacyIdentityID)
	MetaRootKey = substrate.VertexKey("meta", MetaRootID)
	CapabilityLensKey = substrate.VertexKey("meta", CapabilityLensID)
	CapabilityReadLensKey = substrate.VertexKey("meta", CapabilityReadLensID)
	CapabilityReadGrantsLensKey = substrate.VertexKey("meta", CapabilityReadGrantsLensID)
	CapabilityReadWildcardGrantsLensKey = substrate.VertexKey("meta", CapabilityReadWildcardGrantsLensID)
	RoleOperatorKey = substrate.VertexKey("role", RoleOperatorID)

	// Admin + service-actor primordial holdsRole links target the operator
	// role. Identity is the link source (later-arriving vertex per Contract
	// #1 §1.1); the role pre-exists as the target.
	BootstrapHoldsRoleLinkKey = substrate.LinkKey("identity", BootstrapIdentityID, "holdsRole", "role", RoleOperatorID)
	LoomHoldsRoleLinkKey = substrate.LinkKey("identity", LoomIdentityID, "holdsRole", "role", RoleOperatorID)
	WeaverHoldsRoleLinkKey = substrate.LinkKey("identity", WeaverIdentityID, "holdsRole", "role", RoleOperatorID)
	BridgeHoldsRoleLinkKey = substrate.LinkKey("identity", BridgeIdentityID, "holdsRole", "role", RoleOperatorID)
	ObjmgrHoldsRoleLinkKey = substrate.LinkKey("identity", ObjmgrIdentityID, "holdsRole", "role", RoleOperatorID)
	PrivacyHoldsRoleLinkKey = substrate.LinkKey("identity", PrivacyIdentityID, "holdsRole", "role", RoleOperatorID)

	return nil
}

func persistWithStatus(path string, raw PrimordialIDsRaw, status string) error {
	f := BootstrapFile{
		Version:       "15",
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
		// internal service-actor identities (Loom + Weaver + Bridge + objmgr +
		// privacy, arch §92)
		LoomIdentityKey,
		WeaverIdentityKey,
		BridgeIdentityKey,
		ObjmgrIdentityKey,
		PrivacyIdentityKey,
		// meta-meta DDL
		MetaRootKey,
		// InstallPackage / UninstallPackage / UpgradePackage primordial DDLs
		InstallPackageDDLKey,
		UninstallPackageDDLKey,
		UpgradePackageDDLKey,
		// Capability Lens definition (primordial-identity anchor)
		CapabilityLensKey,
		// operator role
		RoleOperatorKey,
		// 3 kernel meta-permissions + 3 package-install/upgrade permissions
		PermCreateMetaVertexKey,
		PermUpdateMetaVertexKey,
		PermTombstoneMetaVertexKey,
		PermInstallPackageKey,
		PermUninstallPackageKey,
		PermUpgradePackageKey,
		// 6 grantedBy links (meta-perm + install/upgrade perms → operator) + admin holdsRole link
		"lnk.permission." + PermCreateMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermUpdateMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermTombstoneMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermInstallPackageID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermUninstallPackageID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermUpgradePackageID + ".grantedBy.role." + RoleOperatorID,
		BootstrapHoldsRoleLinkKey,
		// service-actor holdsRole links (Loom + Weaver + Bridge + objmgr +
		// privacy → operator)
		LoomHoldsRoleLinkKey,
		WeaverHoldsRoleLinkKey,
		BridgeHoldsRoleLinkKey,
		ObjmgrHoldsRoleLinkKey,
		PrivacyHoldsRoleLinkKey,
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
// is 36 entries: 1 op + 1 admin + 5 service actors (Loom + Weaver +
// Bridge + object-store-manager + privacy) + 1 meta-DDL + 3
// install/uninstall/upgrade DDLs + 1 lens (the capability primordial-identity
// anchor) + 1 role + 6 perms (3 meta + 3 install/uninstall/upgrade) + 12 links
// (6 grantedBy + 6 holdsRole) + 5 aspect-type meta-vertices.
const PrimordialVertexKeyCount = 36
