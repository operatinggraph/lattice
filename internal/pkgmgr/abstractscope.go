package pkgmgr

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// ddlClassVertexType is buildInstallBatch's own default Class when a DDLSpec
// leaves it empty (build.go:122-124). Abstract/SubtypeOfRef are meaningful
// only on this class, mirroring validateSensitiveClassScope's sibling
// ddlClassAspectType constant for the aspect side.
const ddlClassVertexType = "meta.ddl.vertexType"

// validateAbstractDDLScope enforces the dynamic-type-taxonomy-design.md
// §3.2/§3.5 install-time rules for a DDLSpec declaring Abstract, SubtypeOfRef,
// or LeafBudget, PLUS Contract #1 §1.2's general reserved-vertex-type-name
// rule for every vertexType DDL (abstract or concrete) — this is the single
// authority pkgmgr has for a DDL's CanonicalName, so the general rule lives
// beside the abstract-specific ones rather than as a second, separate check.
// Every branch fails CLOSED: an abstract type names no instance, so a
// malformed declaration that installed anyway would either silently admit a
// fake instance or leave a leaf's taxonomy membership unresolved at commit
// time, with no install-time signal. Pure (no I/O), same doctrine as
// validateSensitiveClassScope/validateCustodyScope, whose sibling it is.
func (def Definition) validateAbstractDDLScope() error {
	for idx, d := range def.DDLs {
		class := d.Class
		if class == "" {
			class = ddlClassVertexType
		}

		// Contract #1 §1.2: "Operator-defined DDL must not register vertex
		// types named meta or op." Checked for every vertexType DDL, not only
		// an abstract one. An aspectType / linkType / eventType DDL's
		// CanonicalName is deliberately NOT checked: an aspect DDL's name lands
		// in a key's localName slot (§1.1's 4th segment) and a link DDL's in
		// the relation slot (segment 3 of 6) — neither position is the type
		// segment §1.2 reserves, whatever string it holds.
		//
		// §1.2's own named enforcement point is the Processor at meta-DDL
		// commit time (internal/processor/step6_validate.go's
		// reservedVertexTypeName gate), which is the one a raw core-operations
		// submit cannot route around; both gates read the SAME reserved set
		// (keys.IsReservedTypeName) so they cannot disagree. This install-time
		// check earns its place by failing the package where the mistake was
		// AUTHORED, naming the offending DDL index, instead of surfacing as a
		// rejected install operation with only a mutation key to go on.
		//
		// The check is exact-name only, and that is not an oversight: a
		// vertexType DDL's CanonicalName is not always the literal type segment
		// its install writes into a key — a census over pkgregistry.All() found
		// 27 of 59 shipping vertexType DDLs whose CanonicalName differs from
		// (or, for an op-only DDL like "shredIdentityKey", never becomes) that
		// segment, e.g. "workOrder" writes vtx.workorder.<id>. So
		// keys.IsValidTypeSegment is enforced below only on a
		// TAXONOMY-PARTICIPATING DDL (Abstract, or SubtypeOfRef declared), never
		// on an ordinary concrete vertexType DDL, which would reject those 27
		// packages; a non-participating DDL's CanonicalName can be any shape at
		// all, and this check closes only the exact-name collision §1.2 names.
		if class == ddlClassVertexType {
			if keys.IsReservedTypeName(d.CanonicalName) {
				return fmt.Errorf(
					"pkgmgr: DDL[%d]: vertexType CanonicalName %q is reserved (Contract #1 §1.2)",
					idx, d.CanonicalName)
			}
		}

		// A taxonomy-PARTICIPATING DDL's own CanonicalName must itself be
		// usable as a vertex key-type segment (dynamic-type-taxonomy-
		// design.md §14 Fire A item 5). "Participating" here is Abstract, or
		// SubtypeOfRef declared (a concrete leaf or a concrete/abstract mid
		// type naming its own ancestor) — the third participation mode, "is
		// the TARGET of some other DDL's subtypeOf edge," needs no separate
		// check here: reaching a target at all requires another DDL's own
		// SubtypeOfRef to equal that target's CanonicalName exactly
		// (resolveExternalSubtypeTarget/batch-local resolution both match by
		// literal string), and that referencing DDL's SubtypeOfRef is
		// checked against this same authority below — so a target's
		// CanonicalName format is proven by induction through whichever DDL
		// names it, never left unchecked.
		//
		// The resolver (internal/refractor/taxonomy) expands a `*` label by
		// comparing canonicalNames against vertex KEY-TYPE segments. A
		// taxonomy participant whose own CanonicalName cannot even BE a
		// key-type segment can never be reached by (or contribute a
		// concrete instance to) any abstract label's expansion, so
		// admitting one here would let an install succeed while silently
		// guaranteeing the resolver can never see it.
		if d.Abstract || d.SubtypeOfRef != "" {
			if !keys.IsValidTypeSegment(d.CanonicalName) {
				return fmt.Errorf(
					"pkgmgr: DDL[%d]: taxonomy-participating CanonicalName %q is not a valid Contract #1 type segment ([a-z][a-z0-9]*)",
					idx, d.CanonicalName)
			}
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
			// An UNDECLARED budget (zero) is legal and is not checked here —
			// it is warned about instead, by undeclaredLeafBudgetWarnings
			// below, because the default it takes is a trap rather than a
			// mistake.
			if d.LeafBudget < 0 {
				return fmt.Errorf(
					"pkgmgr: DDL[%d] %q: LeafBudget is negative (%d)",
					idx, d.CanonicalName, d.LeafBudget)
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
			if keys.IsReservedTypeName(d.SubtypeOfRef) {
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

// undeclaredLeafBudgetWarnings names every abstract vertexType DDL in def that
// declares no LeafBudget, and states the consequence rather than the omission
// (dynamic-type-taxonomy-design.md §10.2).
//
// Omitting LeafBudget is LEGAL and the default it takes is the right semantics:
// an owner who makes no promise about how far their abstract type will grow
// cannot be relied on to leave a consuming lens any room, so the type takes the
// whole narrowed-filter label cap as its budget. What the warning supplies is
// VISIBILITY of the consequence, which the omission itself carries no trace of.
// leafBudgetDefault IS the cap, so an omission
// forces every consuming lens to K + cap ≤ cap, i.e. K == 0: the first author
// to write `(t:<type>*)` beside ANY other concrete label in an exhaustive lens
// is refused at THEIR install (checkLensLabelCap), by a default set in a
// package they do not own. Saying so at the declaring package's own install is
// what gives the type's owner the chance to declare a real number first.
//
// A WARNING, never a rejection, and it rides the SAME advisory channel as the
// leaf-count overrun resolveTaxonomy computes (Install/Upgrade/Apply's
// LeafBudgetWarnings, printed by cmd/lattice-pkg): both are §10.2 signals about
// an abstract type's growth promise, aimed at the same operator, and refusing
// an install over an omissible field would block a package that has done
// nothing wrong.
//
// Pure (no I/O) and declaration-ordered, the same doctrine as
// validateAbstractDDLScope whose sibling it is.
func (def Definition) undeclaredLeafBudgetWarnings() []string {
	var out []string
	for idx, d := range def.DDLs {
		if !d.Abstract || d.LeafBudget > 0 {
			continue
		}
		class := d.Class
		if class == "" {
			class = ddlClassVertexType
		}
		if class != ddlClassVertexType {
			continue
		}
		out = append(out, fmt.Sprintf(
			"DDL[%d] %q: abstract type declares no LeafBudget, so it takes the whole narrowed-filter label cap (%d) as its budget — "+
				"no exhaustive lens can name ANY other concrete label alongside (%s*) and still narrow its Core KV consumer; "+
				"declare a LeafBudget here to leave a consuming lens room",
			idx, d.CanonicalName, leafBudgetDefault, d.CanonicalName))
	}
	return out
}
