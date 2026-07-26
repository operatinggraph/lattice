//go:build ignore

// lint-package-standard — enforcement for the mechanically-checkable subset of
// the Vertical Package Standard (_bmad-output/implementation-artifacts/
// vertical-package-standard.md). Normative text does not bind the next author;
// a blocking gate does.
//
// Three rules, over every package in internal/pkgregistry (so a package cannot
// escape the gate by not being registered — the registry IS how a package is
// installable at all):
//
//   - S1 — every USER-FACING op is self-describing. An op granted at
//     scope=self, or granted to any role outside the trusted-tool set, needs a
//     full OpMetaSpec in its OWN package: Presentation (with a Title),
//     InputSchema, FieldDescriptions, and Dispatch — plus Dispatch.TargetType
//     whenever a TargetField is named, since without the type a client cannot
//     resolve the field from context. Granted-but-descriptor-less ops are
//     invisible to a descriptor-driven client, which is the discoverability
//     hole the census measured (27% of ops resolvable, 12% renderable).
//   - S6 — the verification floor. A package declaring lenses ships a
//     `lens_cypher_test.go` executing them; every package pins its structure
//     (a `len(Package.…)` assertion in some test), so a silently added or
//     dropped DDL / op / permission / lens fails a test rather than shipping.
//   - S7 — manifest hygiene. `manifest.yaml` must agree with the composed Go
//     Definition, field by field. This runs the same VerifyAgainstDefinition
//     the installer runs, over the whole corpus, with no install and no
//     per-package test needed.
//
// Two escape hatches, both explicit, neither silent:
//
//   - EXEMPT BY NATURE — an engine / reply / lifecycle op no human triggers
//     states so in its permission Note with an `[no-op-meta: <reason>]`
//     marker (the Standard's own rule: the exemption is stated in the Note, so
//     the next reader can tell a decision from an oversight).
//   - KNOWN DEBT — the s1Debt / s6Debt baselines below. These are the gaps the
//     Standard's convergence program (§3) is scheduled to close, listed one by
//     one so they are visible in CI instead of invisible in the corpus. The
//     baselines are SHRINK-ONLY: an entry that no longer violates fails the
//     gate too, with an instruction to delete it, so the ledger cannot rot into
//     a permanent amnesty.
//
// STRICT=1 exits non-zero on any issue.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// trustedToolRoles are the roles whose holder is an ADMIN TOOL rather than a
// person filling in a form — Loupe's console, the demo operator, the
// control-plane and provisioning identities. Such a tool hardcodes its own
// dispatch (clinic-domain's op-meta comment states this for the operator hat:
// "the trusted admin tool hardcodes its own status transitions"), so an op
// reachable only by them owes no descriptor. Every other role is an archetype
// a real person wears, and their ops must be self-describing.
var trustedToolRoles = map[string]bool{
	"operator":            true,
	"consoleOperator":     true,
	"demoOperator":        true,
	"control-operator":    true,
	"identityProvisioner": true,
}

// exemptionMarker is what a permission Note carries to declare its op needs no
// descriptor — `[no-op-meta: engine control-plane, no human dispatch]`.
const exemptionMarker = "[no-op-meta:"

// opRef identifies one permission across the corpus. Scope is part of the
// identity because a permission IS its (operationType, scope) pair
// (Contract #8 §8.1), so the two grants on one op are two separate rows.
type opRef struct {
	pkg   string
	op    string
	scope string
}

func (r opRef) String() string { return fmt.Sprintf("%s %s (scope=%s)", r.pkg, r.op, r.scope) }

// s1Debt carried the shipped user-facing surface that predated this gate. It is
// EMPTY: identity-domain was the last holdout, conforming last on purpose
// (Standard §3.4) because it is the idiom source every other package copies, so
// its descriptors were written once the vocabulary had settled against the
// corpus rather than ahead of it. S1 now binds with no exemptions — a granted,
// user-facing op ships a full descriptor or reds this gate.
//
// Never add a row here for a NEW op — write its descriptor, or state the
// exemption in its permission Note.
var s1Debt = map[opRef]bool{}

// readTemplateDebt is the shipped set of read declarations that build a key
// around a payload field nothing guarantees is present (checkReadTemplates).
// Both entries are conditionally-supplied by design — lease-signing's `unit` is
// named on a first approve but not on a decline, and service-domain's
// `template`/`serviceprovider` are carried only by the instance path — so on the
// other branch each template resolves to a malformed key.
//
// Facet drops such a key before submitting (its wholeKey filter), so today this
// costs a worse diagnostic rather than a rejected operation: the script falls to
// its own read instead of receiving the declared one. Fixing it properly means
// deciding, per op, whether the field should be guaranteed or the read should be
// bare — a semantics call in each owning package, not a mechanical edit.
//
// Shrink only, same discipline as s1Debt: an entry that stops violating fails
// the gate too. Never add a row here for a NEW descriptor.
var readTemplateDebt = map[opRef]bool{
	{"lease-signing", "DecideLeaseApplication", ""}: true,
	{"service-domain", "RecordServiceOutcome", ""}:  true,
}

// s6Debt lists packages below the verification floor, per rule. Same
// shrink-only discipline as s1Debt.
var s6Debt = map[string]map[string]bool{
	"lens-cypher-test": {
		"augur":            true,
		"console-operator": true,
		"demo-operator":    true,
		"identity-domain":  true,
		"rbac-domain":      true,
	},
	// Empty: every registered package pins its structure. The rule now binds
	// with no exemptions, so a new package ships a pin or reds the gate.
	"structure-pins": {},
}

type report struct {
	issues []string
	// satisfied records a debt entry the corpus no longer violates, so the
	// baseline shrinks instead of rotting.
	staleDebt []string
}

func (r *report) issuef(format string, a ...any) {
	r.issues = append(r.issues, fmt.Sprintf(format, a...))
}
func (r *report) stalef(format string, a ...any) {
	r.staleDebt = append(r.staleDebt, fmt.Sprintf(format, a...))
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
		}
	}

	rep := &report{}
	seenS1Debt := map[opRef]bool{}
	seenS6Debt := map[string]map[string]bool{"lens-cypher-test": {}, "structure-pins": {}}
	seenReadTemplateDebt := map[opRef]bool{}

	for _, name := range pkgregistry.Names() {
		def, _ := pkgregistry.Lookup(name)
		dir := filepath.Join("packages", name)
		checkS1(rep, name, def, seenS1Debt)
		checkReadTemplates(rep, name, def, seenReadTemplateDebt)
		checkS6(rep, name, dir, def, seenS6Debt)
		checkS7(rep, name, dir, def)
	}

	for ref := range s1Debt {
		if !seenS1Debt[ref] {
			rep.stalef("s1Debt lists %s, which no longer violates S1 (or no longer exists) — delete the entry", ref)
		}
	}
	for ref := range readTemplateDebt {
		if !seenReadTemplateDebt[ref] {
			rep.stalef("readTemplateDebt lists %s %s, whose read templates no longer wrap an unguaranteed field — delete the entry", ref.pkg, ref.op)
		}
	}
	for rule, pkgs := range s6Debt {
		for pkg := range pkgs {
			if !seenS6Debt[rule][pkg] {
				rep.stalef("s6Debt[%s] lists %s, which now conforms (or no longer exists) — delete the entry", rule, pkg)
			}
		}
	}

	sort.Strings(rep.issues)
	sort.Strings(rep.staleDebt)
	for _, s := range rep.issues {
		fmt.Println(s)
	}
	for _, s := range rep.staleDebt {
		fmt.Println(s)
	}

	total := len(rep.issues) + len(rep.staleDebt)
	if total == 0 {
		fmt.Printf("lint-package-standard: 0 issues across %d packages (%d S1 + %d S6 known-debt entries remain)\n",
			len(pkgregistry.Names()), len(s1Debt), len(s6Debt["lens-cypher-test"])+len(s6Debt["structure-pins"]))
		return
	}
	fmt.Printf("lint-package-standard: %d issue(s)\n", total)
	if strict {
		os.Exit(1)
	}
}

// checkS1 requires a full descriptor for every user-facing op the package
// grants, unless the permission Note declares the exemption or the pair is
// carried in the debt baseline.
func checkS1(rep *report, pkg string, def pkgmgr.Definition, seen map[opRef]bool) {
	metas := make(map[string]pkgmgr.OpMetaSpec, len(def.OpMetas))
	for _, m := range def.OpMetas {
		metas[m.OperationType] = m
	}
	for _, p := range def.Permissions {
		if !userFacing(p) {
			continue
		}
		if strings.Contains(p.Note, exemptionMarker) {
			continue
		}
		ref := opRef{pkg, p.OperationType, p.Scope}
		gaps := descriptorGaps(metas, p.OperationType)
		if len(gaps) == 0 {
			continue
		}
		if s1Debt[ref] {
			seen[ref] = true
			continue
		}
		rep.issuef("%s: S1 — %s is granted to %s but its op-meta %s. A granted op with no descriptor is invisible to a descriptor-driven client. Give it a full OpMetaSpec (clinic-domain/opmetas.go idiom), or state the exemption in the permission Note as %s <reason>].",
			pkg, ref, strings.Join(humanRoles(p), "+"), strings.Join(gaps, " and "), exemptionMarker)
	}
}

// userFacing reports whether a person — not an admin tool — may trigger this
// op. A scope=self grant is user-facing by construction: self-scope exists so
// an end user may act on their own entity.
func userFacing(p pkgmgr.PermissionSpec) bool {
	return p.Scope == "self" || len(humanRoles(p)) > 0
}

func humanRoles(p pkgmgr.PermissionSpec) []string {
	var out []string
	for _, r := range p.GrantsTo {
		if !trustedToolRoles[r] {
			out = append(out, r)
		}
	}
	return out
}

// descriptorGaps names what a user-facing op's meta is missing, in the
// vocabulary of OpMetaSpec's own fields. Empty means fully self-describing.
func descriptorGaps(metas map[string]pkgmgr.OpMetaSpec, op string) []string {
	m, ok := metas[op]
	if !ok {
		return []string{"is absent from this package's OpMetas"}
	}
	var gaps []string
	if m.Presentation == nil || m.Presentation.Title == "" {
		gaps = append(gaps, "declares no Presentation.Title")
	}
	if m.InputSchema == "" {
		gaps = append(gaps, "declares no InputSchema")
	}
	if len(m.FieldDescriptions) == 0 {
		gaps = append(gaps, "declares no FieldDescriptions")
	}
	switch {
	case m.Dispatch == nil:
		gaps = append(gaps, "declares no Dispatch")
	case m.Dispatch.TargetField != "" && m.Dispatch.TargetType == "":
		gaps = append(gaps, fmt.Sprintf("names Dispatch.TargetField %q with no TargetType, so a client cannot resolve it from context", m.Dispatch.TargetField))
	}
	return gaps
}

// checkReadTemplates rejects a read declaration that builds a key AROUND a
// payload placeholder whose field is optional.
//
// A template is substituted, not evaluated: an absent field becomes "", and the
// literal text wrapped around it survives. `{payload.identityKey}.patientClaim`
// with no identityKey is therefore not a shorter key, it is ".patientClaim" — a
// key NATS rejects as malformed rather than reporting absent, which turns the
// Processor's hydrate step into a rejected operation instead of the script's
// own absent branch. The client drops such a key defensively (Facet's
// wholeKey), but a declaration that can only ever resolve to garbage is an
// authoring error, and the author is who should hear about it.
//
// A placeholder that IS the whole entry is fine at any required-ness: it
// resolves to "" and is dropped cleanly, which is exactly what an optional read
// of an unsupplied field should do.
func checkReadTemplates(rep *report, pkg string, def pkgmgr.Definition, seen map[opRef]bool) {
	for _, m := range def.OpMetas {
		if m.Dispatch == nil {
			continue
		}
		guaranteed := requiredFields(m.InputSchema)
		// A field the descriptor fills itself is as present as a required one:
		// contextParams substitute BEFORE reads (the client resolves them in
		// that order), and a client that cannot resolve one refuses to offer the
		// op at all rather than submitting a hole. The visitor never sees such a
		// field, so "optional in the input schema" says nothing about whether it
		// will be there.
		for field := range m.Dispatch.ContextParams {
			guaranteed[field] = true
		}
		if m.Dispatch.TargetField != "" {
			guaranteed[m.Dispatch.TargetField] = true
		}
		for _, entry := range append(append([]string{}, m.Dispatch.Reads...), m.Dispatch.OptionalReads...) {
			for _, field := range payloadPlaceholders(entry) {
				whole := entry == "{payload."+field+"}"
				if whole || guaranteed[field] {
					continue
				}
				if ref := (opRef{pkg, m.OperationType, ""}); readTemplateDebt[ref] {
					seen[ref] = true
					continue
				}
				rep.issuef("%s: S1 — %s declares the read %q, which builds a key around payload field %q that nothing guarantees is present: it is not required, not a contextParam, and not the targetField. An omitted %s substitutes empty and leaves a malformed key (a leading, interior, or trailing dot), which NATS rejects outright rather than reporting absent, so the Processor fails the whole operation instead of letting the script take its absent branch. Declare the bare {payload.%s} and let the script's own read decide, or guarantee the field.",
					pkg, m.OperationType, entry, field, field, field)
			}
		}
	}
}

// requiredFields reads the `required` array out of an op-meta's InputSchema. An
// absent or unparseable schema yields no required fields, which makes the check
// above strictly more suspicious rather than less.
func requiredFields(schema string) map[string]bool {
	out := map[string]bool{}
	if schema == "" {
		return out
	}
	var parsed struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		return out
	}
	for _, f := range parsed.Required {
		out[f] = true
	}
	return out
}

// payloadPlaceholders names every payload field a template entry substitutes,
// with any trailing `:id` modifier stripped.
func payloadPlaceholders(entry string) []string {
	var out []string
	for _, m := range placeholderRe.FindAllStringSubmatch(entry, -1) {
		expr := strings.TrimSuffix(m[1], ":id")
		if f, ok := strings.CutPrefix(expr, "payload."); ok {
			out = append(out, f)
		}
	}
	return out
}

// placeholderRe matches one `{...}` template placeholder, mirroring the
// client-side resolver's own pattern (cmd/facet/web/app.js substituteTemplate).
var placeholderRe = regexp.MustCompile(`\{([^}]+)\}`)

// checkS6 enforces the verification floor: lenses are executed by a cypher
// test, and the package's structure is pinned by a count assertion.
func checkS6(rep *report, pkg, dir string, def pkgmgr.Definition, seen map[string]map[string]bool) {
	if len(def.Lenses) > 0 {
		_, err := os.Stat(filepath.Join(dir, "lens_cypher_test.go"))
		switch {
		case err == nil:
		case s6Debt["lens-cypher-test"][pkg]:
			seen["lens-cypher-test"][pkg] = true
		default:
			rep.issuef("%s: S6 — declares %d lens(es) but ships no lens_cypher_test.go. A lens whose cypher no one executes is only proven by the read model going empty in production (clinic-domain/lens_cypher_test.go idiom).",
				pkg, len(def.Lenses))
		}
	}

	pinned, err := hasStructurePins(dir)
	if err != nil {
		rep.issuef("%s: S6 — cannot read the package directory to check structure pins: %v", pkg, err)
		return
	}
	switch {
	case pinned:
	case s6Debt["structure-pins"][pkg]:
		seen["structure-pins"][pkg] = true
	default:
		rep.issuef("%s: S6 — no test pins this package's structure. Assert the DDL / op / permission / lens counts (a `len(Package.…)` check, loftspace-domain/package_test.go idiom) so a silently added or dropped declaration reds a test instead of shipping.", pkg)
	}
}

// hasStructurePins reports whether some test in the package asserts on the
// Definition's own collection lengths.
func hasStructurePins(dir string) (bool, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return false, err
		}
		if strings.Contains(string(b), "len(Package.") {
			return true, nil
		}
	}
	return false, nil
}

// checkS7 runs the installer's own manifest cross-check over the package,
// which is what makes manifest hygiene corpus-wide rather than dependent on
// each package remembering to write a drift test.
func checkS7(rep *report, pkg, dir string, def pkgmgr.Definition) {
	manifest, err := pkgmgr.ParseManifest(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		rep.issuef("%s: S7 — manifest.yaml does not parse: %v", pkg, err)
		return
	}
	if err := manifest.VerifyAgainstDefinition(def); err != nil {
		rep.issuef("%s: S7 — manifest.yaml drifts from the Go Definition: %v", pkg, err)
	}
}
