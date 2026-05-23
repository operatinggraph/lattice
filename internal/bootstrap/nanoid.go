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
// Story 4.7 trim: the identity-domain and rbac-domain NanoID surfaces
// (DDLRole*, PermCreateRole, PermCreateUnclaimedIdentity, etc.) moved
// to their respective Capability Packages. This file now keeps only
// kernel NanoIDs — the post-4.7 minimal primordial set.
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

	// Story 4.7: meta-permission NanoIDs + keys. Three kernel-seeded
	// permissions authorizing the operator to mutate vtx.meta.* vertices
	// (the entry point for package-installed DDLs + Lenses once Story 5.3
	// routes installs through CreateMetaVertex ops).
	PermCreateMetaVertexID     string
	PermCreateMetaVertexKey    string
	PermUpdateMetaVertexID     string
	PermUpdateMetaVertexKey    string
	PermTombstoneMetaVertexID  string
	PermTombstoneMetaVertexKey string

	// Derived link keys.
	BootstrapHoldsRoleLinkKey string
)

// PrimordialIDsRaw is the persisted form (matches lattice.bootstrap.json).
//
// Story 4.7 trim: identity-domain and rbac-domain NanoID fields are
// retired. Existing lattice.bootstrap.json files that include those
// extra fields parse fine — encoding/json ignores unknown fields by
// default — but fresh generations no longer emit them.
type PrimordialIDsRaw struct {
	BootstrapOp             string `json:"bootstrapOp"`
	BootstrapIdentity       string `json:"bootstrapIdentity"`
	MetaRoot                string `json:"metaRoot"`
	CapabilityLens          string `json:"capabilityLens"`
	CapabilityRoleIndexLens string `json:"capabilityRoleIndexLens"`
	RoleOperator            string `json:"roleOperator"`

	// Story 4.7 meta-permission NanoIDs.
	PermCreateMetaVertex    string `json:"permCreateMetaVertex"`
	PermUpdateMetaVertex    string `json:"permUpdateMetaVertex"`
	PermTombstoneMetaVertex string `json:"permTombstoneMetaVertex"`
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
		MetaRoot:                MetaRootID,
		CapabilityLens:          CapabilityLensID,
		CapabilityRoleIndexLens: CapabilityRoleIndexLensID,
		RoleOperator:            RoleOperatorID,
		PermCreateMetaVertex:    PermCreateMetaVertexID,
		PermUpdateMetaVertex:    PermUpdateMetaVertexID,
		PermTombstoneMetaVertex: PermTombstoneMetaVertexID,
	}
}

// Load reads existing IDs from path. Used by read-only callers
// (cmd/refractor-stub, scripts/verify-kernel).
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
		&raw.MetaRoot,
		&raw.CapabilityLens,
		&raw.CapabilityRoleIndexLens,
		&raw.RoleOperator,
		&raw.PermCreateMetaVertex,
		&raw.PermUpdateMetaVertex,
		&raw.PermTombstoneMetaVertex,
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

	// Derive keys.
	BootstrapOpKey = "vtx.op." + BootstrapOpID
	BootstrapIdentityKey = "vtx.identity." + BootstrapIdentityID
	MetaRootKey = "vtx.meta." + MetaRootID
	CapabilityLensKey = "vtx.meta." + CapabilityLensID
	CapabilityRoleIndexLensKey = "vtx.meta." + CapabilityRoleIndexLensID
	RoleOperatorKey = "vtx.role." + RoleOperatorID

	// Story 4.7: admin's primordial holdsRole link targets the operator
	// role (formerly platformInternal — the latter retired).
	BootstrapHoldsRoleLinkKey = "lnk.identity." + BootstrapIdentityID + ".holdsRole.role." + RoleOperatorID

	return nil
}

func persist(path string, raw PrimordialIDsRaw) error {
	f := BootstrapFile{
		Version:       "3",
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

// PrimordialVertexKeys returns the post-Story-4.7 kernel's vertex keys —
// only those entries the kernel itself seeds. After 4.7 this is a small
// fixed set; package-installed DDLs/Lenses/permissions are addressed
// separately by `verify-package-*` gates.
func PrimordialVertexKeys() []string {
	return []string{
		// bootstrap op tracker
		BootstrapOpKey,
		// admin identity
		BootstrapIdentityKey,
		// meta-meta DDL
		MetaRootKey,
		// 2 Lens definitions
		CapabilityLensKey,
		CapabilityRoleIndexLensKey,
		// operator role
		RoleOperatorKey,
		// 3 kernel meta-permissions
		PermCreateMetaVertexKey,
		PermUpdateMetaVertexKey,
		PermTombstoneMetaVertexKey,
		// 3 grantedBy links (meta-perm → operator) + admin holdsRole link
		"lnk.permission." + PermCreateMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermUpdateMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		"lnk.permission." + PermTombstoneMetaVertexID + ".grantedBy.role." + RoleOperatorID,
		BootstrapHoldsRoleLinkKey,
	}
}

// PrimordialVertexKeyCount is the count of TOP-LEVEL kernel keys (the
// ones in PrimordialVertexKeys()). Used as a count-only readiness gate
// where loading lattice.bootstrap.json would race startup. After Story
// 4.7 this is just the kernel set (13 entries — 1 op + 1 admin + 1
// meta-DDL + 2 lenses + 1 role + 3 meta-perms + 4 links).
const PrimordialVertexKeyCount = 13
