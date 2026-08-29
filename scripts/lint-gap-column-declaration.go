//go:build ignore

// lint-gap-column-declaration — every `missing_*` column a weaver-target-bound
// lens projects is declared in that target's `gaps` map
// (Contract #10 §10.8; weaver-decline-retry-substrate-native-design.md §3.2
// row 8).
//
// THE HAZARD. `internal/pkgmgr/definition.go`'s WeaverTargetSpec states the
// contract in prose — "Gaps maps each `missing_<gap>` violation column to the
// remediation action the engine runs when that column is set" — and nothing
// enforces it. Weaver's dispatchGap (internal/weaver/evaluator.go) looks the
// open column up in `target.Gaps`; on a miss with no Augur escalation it takes
// the config-error arm and returns substrate.NakWithLongDelay. That is not a
// drop: the row holds a MaxAckPending slot and re-runs the whole
// clearClosedMarks preamble on every 5-minute redelivery floor, forever, for as
// long as the column stays true. The long floor is deliberate — a package
// re-author IS the fix, and it projects no new row, so an Ack would strand the
// entity violating with nothing to re-deliver it — but it only pays for itself
// if the miss is genuinely an authoring omission.
//
// The engine cannot tell an authoring omission from a column a package projects
// DELIBERATELY with no remediation (an operator-closed condition, say). Both
// arrive as "column true, no gaps entry". So the deliberate case is made
// declarable instead — the sanctioned form is a `surface` gap, which raises a
// per-(target, entity, column) Health issue and Acks — and this gate asserts the
// resulting invariant: lens-projected `missing_*` ⊆ declared gaps. With it, a
// long-Nak means what it says.
//
// THE RULE. For every package Definition in internal/pkgregistry, for every
// WeaverTargetSpec it declares: resolve LensRef to a LensSpec in the SAME
// Definition by CanonicalName, take every `missing_`-prefixed entry of that
// lens's Output.BodyColumns, and require each to be a key of the target's Gaps.
//
// KEYED ON BodyColumns, NOT ON THE CYPHER. internal/refractor/projection/
// driver.go builds the projected document by iterating BodyColumns, so that
// list IS the row Weaver's openGapColumns (internal/weaver/evaluator.go)
// enumerates for `missing_` — it is the hazard itself, not a stand-in for it.
// The cypher is only a proxy: a RETURN column can be composed, aliased, or
// carried through a WITH, and a cypher-regex census attributes it to the wrong
// lens (`scripts/lint-lens-anchors.go` takes the regex route; it is the shape
// deliberately not copied here). Over-declaration in BodyColumns fails SAFE —
// the author writes a gaps entry nothing fires; under-declaration in
// BodyColumns cannot reach Weaver at all, because the column is never in the
// projected row.
//
// READ FROM THE COMPILED DEFINITIONS. The corpus is walked through
// pkgregistry.Names() / Lookup, as scripts/lint-package-standard.go does, so the
// Go compiler resolves every constant, helper closure and fmt.Sprintf
// composition a package uses to build a column name or a gaps key. A hand list
// or a text scan rots against exactly those idioms. And a package cannot escape
// the gate by not being registered — the registry IS how a package is
// installable at all (internal/pkgregistry/registry.go).
//
// TARGETS, NOT LENSES. The loop is over targets so that only a lens some target
// BINDS is in scope. Lenses that project `missing_*` columns and have no weaver
// target — packages/lease-signing's two protected Postgres read models, for one
// pair — are read models a client queries, never Weaver rows, so no gaps map
// governs them and there is nothing for them to declare. Inverting the loop
// (walk lenses, look for a target) would flag them.
//
// WHAT IS EXEMPT, AND WHAT IS REPORTED RATHER THAN ASSUMED.
//
//   - EXEMPT: a target whose Augur policy escalates `unplannable`. dispatchGap
//     routes an undeclared column to the AI reasoning tier when that trigger is
//     present and never reaches the long-Nak arm, so such a target legitimately
//     declares no Gaps entry for a column. This is the ONLY exemption, and it is
//     not an allowlist — it is a property of the target's own declared policy.
//   - REPORTED: an empty LensRef, a LensRef this Definition does not resolve,
//     and a resolved lens with a nil Output. internal/pkgmgr/build.go admits a
//     bare NanoID naming a lens in an ALREADY-INSTALLED package, which cannot be
//     resolved statically; no target uses that form today. A check that silently
//     passed what it could not resolve would be the lint-gates dossier's
//     "resolved, not counted" failure — a target naming nothing would read
//     exactly like one whose columns are all declared. So the gate says so out
//     loud instead.
//
// STRICT=1 exits non-zero on any finding; unset, it reports and exits 0.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// gapColumnPrefix is Contract #10 §10.2's gap-column naming convention: a gap
// column, a §10.8 gaps key and a mark key segment are all `missing_<gap>`.
// Restated here rather than imported — internal/weaver's own constant
// (state.go) is unexported, and this gate needs no other part of the engine.
const gapColumnPrefix = "missing_"

// escalateUnplannable is the Contract #10 §10.8 Augur trigger for "a missing_*
// column with no gaps[col] entry". A target escalating it routes the undeclared
// column to the reasoning tier instead of the long-Nak arm, so it is exempt from
// this gate. Restated for the same reason as gapColumnPrefix
// (internal/weaver/registry.go's constant is unexported).
const escalateUnplannable = "unplannable"

func main() {
	strict := os.Getenv("STRICT") == "1"

	var findings []string
	targets, pkgsWithTargets, exempt := 0, 0, 0

	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			findings = append(findings, fmt.Sprintf("%s: pkgregistry.Names() lists this package but Lookup does not resolve it — the corpus enumeration and the registry disagree, so this run cannot claim to have checked it", name))
			continue
		}
		if len(def.WeaverTargets) == 0 {
			continue
		}
		pkgsWithTargets++
		for _, t := range def.WeaverTargets {
			targets++
			if escalatesUnplannable(t) {
				exempt++
				continue
			}
			findings = append(findings, checkTarget(name, def, t)...)
		}
	}

	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-gap-column-declaration: clean — %d target(s) across %d package(s); every missing_* column a bound lens projects is declared in its target's gaps map (%d target(s) exempt via augur.escalate %q)\n",
			targets, pkgsWithTargets, exempt, escalateUnplannable)
		return
	}
	fmt.Printf("lint-gap-column-declaration: %d issue(s) across %d target(s) in %d package(s)\n", len(findings), targets, pkgsWithTargets)
	if strict {
		os.Exit(1)
	}
}

// escalatesUnplannable reports whether the target's Augur policy redirects an
// undeclared gap column to the AI reasoning tier. Nil-safe: the field is
// default-absent, and the overwhelming majority of targets declare no policy.
func escalatesUnplannable(t pkgmgr.WeaverTargetSpec) bool {
	if t.Augur == nil {
		return false
	}
	for _, trigger := range t.Augur.Escalate {
		if trigger == escalateUnplannable {
			return true
		}
	}
	return false
}

// checkTarget returns one finding per `missing_*` BodyColumn of the target's
// bound lens that the target's Gaps map does not name, plus a finding for a
// LensRef or Output this gate cannot resolve to a column list at all.
func checkTarget(pkg string, def pkgmgr.Definition, t pkgmgr.WeaverTargetSpec) []string {
	where := fmt.Sprintf("%s: target %s", pkg, t.TargetID)

	if strings.TrimSpace(t.LensRef) == "" {
		return []string{fmt.Sprintf("%s: declares no LensRef — the target dispatches over no lens, so which missing_* columns it must declare cannot be determined. Name the violation lens's CanonicalName.", where)}
	}

	lens, found := lookupLens(def, t.LensRef)
	if !found {
		return []string{fmt.Sprintf("%s: LensRef %q resolves to no lens in package %s. A bare NanoID naming an already-installed lens is admitted by the installer but is not statically resolvable, so this gate cannot check the target's gap columns — declare the lens in this package, or the invariant (projected missing_* ⊆ declared gaps) goes unverified for this target.", where, t.LensRef, pkg)}
	}
	if lens.Output == nil {
		return []string{fmt.Sprintf("%s: bound lens %q declares no Output descriptor, so it projects no BodyColumns list for this gate to read. A weaver-target-bound violation lens is an actor-aggregate lens with an Output whose BodyColumns are the row Weaver evaluates (internal/refractor/projection/driver.go); without it the target's gap columns cannot be verified.", where, lens.CanonicalName)}
	}

	var out []string
	for _, col := range lens.Output.BodyColumns {
		if !strings.HasPrefix(col, gapColumnPrefix) {
			continue
		}
		if _, declared := t.Gaps[col]; declared {
			continue
		}
		out = append(out, fmt.Sprintf("%s: lens %q projects gap column %q, which the target's gaps map does not declare (declared: %s). Weaver's dispatchGap holds such a row on the long redelivery floor indefinitely. Add a gaps entry naming the remediation action — or, if the column is deliberately not remediated, declare it `surface` so it raises a standing Health issue and Acks.",
			where, lens.CanonicalName, col, declaredKeys(t.Gaps)))
	}
	return out
}

// lookupLens resolves a target's LensRef against the lenses declared in the SAME
// Definition, by CanonicalName — the form a package author writes (the installer
// rewrites it to the lens's in-batch NanoID).
func lookupLens(def pkgmgr.Definition, ref string) (pkgmgr.LensSpec, bool) {
	for _, l := range def.Lenses {
		if l.CanonicalName == ref {
			return l, true
		}
	}
	return pkgmgr.LensSpec{}, false
}

// declaredKeys renders a target's declared gap keys in sorted order, so the
// finding names what the author DID declare beside what they did not.
func declaredKeys(gaps map[string]pkgmgr.GapActionSpec) string {
	if len(gaps) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(gaps))
	for k := range gaps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
