//go:build ignore

// lint-manifest-entity-type — structural guard for the edge-manifest
// entityKey/entityType pairing (dynamic-type-taxonomy-design.md §8's
// `manifest.ent` row, §14, §17.19 — the STATIC AUTHORING half of that item;
// it needs no data migration). Every `manifest.ent`-shaped lens tail projects
// `<var>.key AS entityKey` alongside a `"<literal>" AS entityType`, and
// cmd/facet's renderer trusts the literal to equal the entityKey expression's
// OWN vertex-type segment: op-attach and payload-resolve compare the two with
// `===` (cmd/facet/web/app.js around :36, :293, :1207). Nothing enforced that
// pairing before this gate — it was convention only, so a typo'd or
// stale-after-a-rename literal desyncs silently from the walk that actually
// reaches the row, and op-attach breaks for that one entity kind alone,
// discoverable only by clicking through Facet's UI.
//
// Two rules, over every lens edge-manifest declares (pkgregistry.Lookup
// returns the RAW pre-expansion Definition — Go has already resolved every
// named Chain constant by the time Lenses() runs, so a Walk's Chain entries
// here are the walk's own MATCH pattern verbatim, not a string to re-derive):
//
//   - PAIRING — a `"<literal>" AS entityType` literal must equal the label
//     the SAME lens's Walk binds its entityKey variable to, read off the
//     walk's own Chain pattern (e.g. `(sess:session)` -> `session`). This
//     gate resolves the label itself from Chain text rather than trusting the
//     separately-declared Walk.AnchorType field — pkgmgr's own
//     ExpandReadGrantWalks (internal/pkgmgr/anchorwalk.go's parseOneWalk)
//     already cross-checks AnchorType against the Chain elsewhere, but this
//     gate does not lean on that separate mechanism holding.
//   - ABSTRACT — an entityType literal must not name a type any package
//     declares `Abstract: true` (packages/*/ddls.go, corpus-wide: edge-
//     manifest's walks reach vertices other packages own). An abstract type
//     names no instance (dynamic-type-taxonomy-design.md §3.2) — it is usable
//     only as a lens pattern label or a subtypeOf ancestor, never as a live
//     vertex's own type segment — so stamping one as entityType would desync
//     from entityKey's real runtime type by construction, not by accident.
//
// An `AS entityType` occurrence this gate cannot pair with a `<var>.key AS
// entityKey` in the one shape the corpus uses, or whose entityKey variable it
// cannot resolve to a label anywhere in the lens's own Walk Chain(s) or Spec,
// is reported as its own finding rather than silently skipped. That is a
// harder failure mode than lint-lens-anchors' unverifiable warn class: the
// pairing here is a promise to a `===` comparison in a renderer, and a
// promise this gate cannot check is not one anyone should trust either.
//
// STRICT=1 exits non-zero on any issue.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// targetPackage is the one package this gate audits. The entityKey/entityType
// pairing convention is edge-manifest's own (the `manifest.ent` namespace it
// mints) — no other registered package projects an `AS entityType` column, so
// there is nothing corpus-wide to widen this to yet.
const targetPackage = "edge-manifest"

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
		}
	}

	def, ok := pkgregistry.Lookup(targetPackage)
	if !ok {
		fmt.Printf("lint-manifest-entity-type: package %q is not registered in internal/pkgregistry\n", targetPackage)
		os.Exit(1)
	}

	abstractTypes := collectAbstractTypes()

	var issues []string
	for _, lens := range def.Lenses {
		issues = append(issues, checkLens(lens, abstractTypes)...)
	}
	sort.Strings(issues)

	if len(issues) == 0 {
		fmt.Printf("lint-manifest-entity-type: 0 issues — every entityType literal in %s matches its "+
			"entityKey's own vertex-type label and names no abstract type\n", targetPackage)
		return
	}
	fmt.Printf("lint-manifest-entity-type: %d issue(s)\n", len(issues))
	for _, s := range issues {
		fmt.Println(s)
	}
	if strict {
		os.Exit(1)
	}
}

// collectAbstractTypes enumerates every DDL canonicalName declared
// `Abstract: true` across the whole registered corpus. An abstract type is a
// taxonomy-wide fact, not a per-package one — edge-manifest's own walks reach
// vertices other packages own — so this reads every package, not just
// targetPackage.
func collectAbstractTypes() map[string]bool {
	out := map[string]bool{}
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			continue
		}
		for _, d := range def.DDLs {
			if d.Abstract {
				out[d.CanonicalName] = true
			}
		}
	}
	return out
}

// reEntityPair matches the one shape every entityKey/entityType pair in the
// corpus takes today: `<var>.key AS entityKey` immediately followed (nothing
// between them but a comma and whitespace, including a newline) by
// `"<literal>" AS entityType`. (?s) lets `.` cross that newline.
var reEntityPair = regexp.MustCompile(`(?s)(\w+)\.key\s+AS\s+entityKey\s*,\s*"([^"]*)"\s+AS\s+entityType\b`)

// reAnyEntityType finds every `AS entityType` projection regardless of shape,
// so a literal reEntityPair did not capture is still visible as an unhandled
// shape instead of silently passing by omission.
var reAnyEntityType = regexp.MustCompile(`"[^"]*"\s+AS\s+entityType\b`)

// reNodeLabel matches `(<var>:<label>` — one variable's own node-pattern
// declaration. The colon must follow the variable name directly or after
// whitespace only, so a longer identifier sharing the prefix (`prov` inside
// `provXYZ`) can never match.
func reNodeLabel(v string) *regexp.Regexp {
	return regexp.MustCompile(`\(\s*` + regexp.QuoteMeta(v) + `\s*:\s*([A-Za-z_][A-Za-z0-9_]*)`)
}

// checkLens audits one lens's entityKey/entityType pairs.
func checkLens(lens pkgmgr.LensSpec, abstractTypes map[string]bool) []string {
	var out []string

	pairs := reEntityPair.FindAllStringSubmatch(lens.Spec, -1)
	if all := reAnyEntityType.FindAllString(lens.Spec, -1); len(all) != len(pairs) {
		out = append(out, fmt.Sprintf(
			"lens %s: found %d `AS entityType` occurrence(s) but could only pair %d with a `<var>.key AS "+
				"entityKey` in the shape this gate knows (`<var>.key AS entityKey,\\n  \"<literal>\" AS "+
				"entityType`) — an entityType this gate cannot pair is a promise it cannot check either; give "+
				"it that shape, or teach reEntityPair the new one",
			lens.CanonicalName, len(all), len(pairs)))
	}

	for _, m := range pairs {
		entityKeyVar, literal := m[1], m[2]
		label, resolvedFrom, conflict := resolveLabel(lens, entityKeyVar)
		switch {
		case conflict != "":
			out = append(out, fmt.Sprintf(
				"lens %s: entityKey variable %q is bound to disagreeing labels across this lens's own "+
					"Walks (%s) — cannot verify entityType %q against an ambiguous entityKey",
				lens.CanonicalName, entityKeyVar, conflict, literal))
		case label == "":
			out = append(out, fmt.Sprintf(
				"lens %s: entityType %q is paired with `%s.key AS entityKey`, but this gate could not resolve "+
					"%q to a node label anywhere in the lens's own Walk Chain(s) or Spec — cannot verify the pairing",
				lens.CanonicalName, literal, entityKeyVar, entityKeyVar))
		case label != literal:
			out = append(out, fmt.Sprintf(
				"lens %s: entityType is stamped %q but entityKey's variable %q is bound `(%s:%s)` in %s — the "+
					"literal must equal the vertex-type label entityKey's own expression resolves to, or cmd/facet's "+
					"op-attach + payload-resolve `===` comparisons (web/app.js ~:36, :293, :1207) desync silently",
				lens.CanonicalName, literal, entityKeyVar, entityKeyVar, label, resolvedFrom))
		}
		if abstractTypes[literal] {
			out = append(out, fmt.Sprintf(
				"lens %s: entityType is stamped %q, which packages/*/ddls.go declares Abstract: true — an "+
					"abstract type names no instance, so it can never be a live vertex's own type segment and "+
					"desyncs from entityKey's real runtime type by construction",
				lens.CanonicalName, literal))
		}
	}
	return out
}

// resolveLabel resolves entityKeyVar to the node label its own vertex is
// bound with. It prefers every Walk whose AnchorVar matches (not just the
// first — two walks disagreeing on the same variable's label is itself
// surfaced as a conflict rather than silently resolved by declaration order),
// falling back to the lens's own Spec text for a self-anchored lens or a
// tail-local binding.
func resolveLabel(lens pkgmgr.LensSpec, v string) (label, resolvedFrom, conflict string) {
	re := reNodeLabel(v)
	for wi, w := range lens.Walks {
		if w.AnchorVar != v {
			continue
		}
		for _, clause := range w.Chain {
			m := re.FindStringSubmatch(clause)
			if m == nil {
				continue
			}
			switch {
			case label == "":
				label = m[1]
				resolvedFrom = fmt.Sprintf("Walks[%d].Chain", wi)
			case label != m[1]:
				return "", "", fmt.Sprintf("%q (%s) vs %q (Walks[%d].Chain)", label, resolvedFrom, m[1], wi)
			}
		}
	}
	if label != "" {
		return label, resolvedFrom, ""
	}
	if m := re.FindStringSubmatch(lens.Spec); m != nil {
		return m[1], "Spec", ""
	}
	return "", "", ""
}
