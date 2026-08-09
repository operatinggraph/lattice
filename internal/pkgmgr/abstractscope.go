package pkgmgr

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// reservedTypeNames are the two Contract #1 §1.2 reserved type names
// ("meta", "op") a declared type name must not collide with. Checked here
// against an abstract DDL's own CanonicalName and against a SubtypeOfRef
// target name — the taxonomy declaration surfaces (§3.2/§3.5) that need it.
// A CONCRETE DDL's own CanonicalName is not validated against this list: no
// code anywhere in this platform currently enforces Contract #1 §1.2's
// reservation against an ordinary (non-abstract) DDL declaration.
var reservedTypeNames = map[string]struct{}{
	"meta": {},
	"op":   {},
}

// ddlClassVertexType is buildInstallBatch's own default Class when a DDLSpec
// leaves it empty (build.go:122-124). Abstract/SubtypeOfRef are meaningful
// only on this class, mirroring validateSensitiveClassScope's sibling
// ddlClassAspectType constant for the aspect side.
const ddlClassVertexType = "meta.ddl.vertexType"

// validateAbstractDDLScope enforces the dynamic-type-taxonomy-design.md
// §3.2/§3.5 install-time rules for a DDLSpec declaring Abstract, SubtypeOfRef,
// or LeafBudget. Every branch fails CLOSED: an abstract type names no
// instance, so a malformed declaration that installed anyway would either
// silently admit a fake instance or leave a leaf's taxonomy membership
// unresolved at commit time, with no install-time signal. Pure (no I/O),
// same doctrine as validateSensitiveClassScope/validateCustodyScope, whose
// sibling it is.
func (def Definition) validateAbstractDDLScope() error {
	for idx, d := range def.DDLs {
		class := d.Class
		if class == "" {
			class = ddlClassVertexType
		}

		if d.Abstract {
			if class != ddlClassVertexType {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: Abstract is true but Class is %q — abstract is meaningful only for Class %q",
					idx, d.CanonicalName, class, ddlClassVertexType)
			}
			if d.Script != "" {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: Abstract is true but Script is set — an abstract type declares no script",
					idx, d.CanonicalName)
			}
			if len(d.PermittedCommands) > 0 {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: Abstract is true but PermittedCommands is set — an abstract type declares no permitted commands",
					idx, d.CanonicalName)
			}
			if d.LeafBudget < 0 {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: LeafBudget is negative (%d)",
					idx, d.CanonicalName, d.LeafBudget)
			}
			if _, reserved := reservedTypeNames[d.CanonicalName]; reserved {
				return fmt.Errorf(
					"pkgmgr: DDL[%d]: Abstract CanonicalName %q is reserved (Contract #1 §1.2)",
					idx, d.CanonicalName)
			}
			if !keys.IsValidTypeSegment(d.CanonicalName) {
				return fmt.Errorf(
					"pkgmgr: DDL[%d]: Abstract CanonicalName %q is not a valid Contract #1 type segment ([a-z][a-z0-9]*)",
					idx, d.CanonicalName)
			}
		} else if d.LeafBudget != 0 {
			return fmt.Errorf(
				"pkgmgr: DDL[%d] %q: LeafBudget is set (%d) but Abstract is false — LeafBudget is meaningful only on an abstract type",
				idx, d.CanonicalName, d.LeafBudget)
		}

		if d.SubtypeOfRef != "" {
			if class != ddlClassVertexType {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: SubtypeOfRef is set but Class is %q — subtypeOf is meaningful only for Class %q",
					idx, d.CanonicalName, class, ddlClassVertexType)
			}
			if d.SubtypeOfRef == d.CanonicalName {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: SubtypeOfRef equals this DDL's own CanonicalName — a type cannot be its own subtypeOf ancestor",
					idx, d.CanonicalName)
			}
			// A malformed SubtypeOfRef (wrong case, a hyphen, a leading digit,
			// or a reserved name) must fail with a message naming the
			// authoring mistake, not surface later as "not found in the
			// installed kernel" — the same check CanonicalName itself already
			// gets, applied to the name a SubtypeOfRef points AT.
			if _, reserved := reservedTypeNames[d.SubtypeOfRef]; reserved {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: SubtypeOfRef %q is reserved (Contract #1 §1.2)",
					idx, d.CanonicalName, d.SubtypeOfRef)
			}
			if !keys.IsValidTypeSegment(d.SubtypeOfRef) {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: SubtypeOfRef %q is not a valid Contract #1 type segment ([a-z][a-z0-9]*)",
					idx, d.CanonicalName, d.SubtypeOfRef)
			}
		}
	}
	return nil
}
