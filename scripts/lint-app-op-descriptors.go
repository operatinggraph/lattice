//go:build ignore

// lint-app-op-descriptors — the APP seam of the Standard's S1 (vertical-package-standard.md):
// an application may wire UI to an operation only if the operation's OWNING
// PACKAGE describes it. S1 already forces the supply side (every user-facing
// op ships a full OpMetaSpec or a coded [no-op-meta: …] exemption, blocking in
// lint-package-standard); this gate binds the consumption side, over every
// `cmd/*-app` vertical application.
//
// WHY THE APP SEAM NEEDS ITS OWN GATE. The vertical FEs hardcode their op
// surfaces in JS (audited 2026-08-20: ~70 submission sites across the four
// apps, hand-built forms, hand-assembled payloads/reads). That debt is
// tolerated — the Standard's own dual-grant rule concedes "a staff FE
// hardcodes its own dispatch" — but two things about it must not be possible
// silently:
//
//	R1 — an app op literal that names an operation NO package registers.
//	     A package renaming or withdrawing an op today breaks the app only at
//	     RUNTIME (a person clicks, the Processor rejects). The op universe is
//	     compiled into pkgregistry; the app's literals can be checked against
//	     it at CI time instead.
//	R2 — an app referencing an op whose package says nobody does that.
//	     The S1 exemption vocabulary is machine-readable and splits into a
//	     machinery family (engine-op / reply-op / lifecycle-op — "no person
//	     dispatches this") and a client-mechanism family (client-agent-op /
//	     raw-credential-actor / paired-code / unprojected-input — "a client
//	     does exactly this, bespoke"). An app wiring UI to a machinery-family
//	     op falsifies the exemption's own premise with no gate the wiser:
//	     lint-package-standard stays green because the NOTE is well-formed.
//	     The fix is never on the app side alone — either the op is genuinely
//	     human-triggered (describe it; the exemption was wrong) or the UI
//	     should not exist.
//	     The same rule fails an app referencing a registered op that carries
//	     NEITHER a full descriptor NOR any exemption — an op reachable from a
//	     screen but invisible to every descriptor-driven client.
//
// The gate also PRINTS the per-app hardcoded-op-literal count on every run
// (not a failure): the covenant-erosion lesson from cmd/facet
// (lint-facet-discovery) is that individually-reasonable widenings need an
// aggregate measure, or nothing ever notices the trend.
//
// WHAT THIS GATE DOES NOT DO. It does not forbid hand-built forms — the
// descriptor-driven render path for staff apps (an op-catalog read model; the
// P5-legal successor to loftspace app.js's COMPLETIONS map) does not exist
// yet, and a gate must not ban an idiom before the sanctioned alternative
// ships. When that lands, the migration's own design carries the shrink-only
// baseline; this gate is the floor beneath it.
//
// Scope: cmd/*-app non-test sources (.go, web/ .js/.mjs/.html). cmd/facet has
// its own, far stricter covenant gate (lint-facet-discovery: op literals are
// BANNED there beyond five ceremony ops); cmd/loupe is the console inspector
// and submits ops as admin tooling, not a vertical app surface.
//
// STRICT=1 (CI) exits non-zero on any violation; unset, it warns.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// exemptionShape mirrors lint-package-standard.go's parser for
// `[no-op-meta: <code> — <prose>]` permission-Note markers. The code names
// the mechanism whose absence blocks a descriptor; this gate classifies by it.
var exemptionShape = regexp.MustCompile(`\[no-op-meta:\s*([a-z-]+)\s*—\s*[^\]]*\]`)

// machineryCodes are the exemption codes that claim NO PERSON dispatches the
// op. An application source referencing such an op contradicts the claim —
// that is R2's violation, not an allowed hardcoding.
var machineryCodes = map[string]bool{
	"engine-op":    true,
	"reply-op":     true,
	"lifecycle-op": true,
}

// clientMechanismCodes are the exemption codes that describe a CLIENT-side
// action a rendered form cannot express — a ceremony the app implements
// bespoke, or a verb the client's own agent submits programmatically. An app
// referencing these is the expected consumer, not a violation.
var clientMechanismCodes = map[string]bool{
	"client-agent-op":      true,
	"raw-credential-actor": true,
	"paired-code":          true,
	"unprojected-input":    true,
}

// opStatus is everything this gate needs to know about one registered
// operationType, folded across the whole package corpus (S9/S11 guarantee at
// most one package declares its op-meta).
type opStatus struct {
	pkg            string          // a package that registers it (meta owner preferred)
	fullDescriptor bool            // some package ships a full OpMetaSpec
	exemptions     map[string]bool // every [no-op-meta: <code>] any grant Note claims
}

// fullDescriptor mirrors lint-package-standard.go's descriptorGaps: the fields
// a client must hold to render and submit the op from the descriptor alone.
func fullDescriptor(m pkgmgr.OpMetaSpec) bool {
	if m.Presentation == nil || m.Presentation.Title == "" {
		return false
	}
	if m.InputSchema == "" || len(m.FieldDescriptions) == 0 {
		return false
	}
	if m.Dispatch == nil {
		return false
	}
	return true
}

func registeredOps() map[string]*opStatus {
	ops := map[string]*opStatus{}
	ensure := func(op, pkg string) *opStatus {
		s := ops[op]
		if s == nil {
			s = &opStatus{pkg: pkg, exemptions: map[string]bool{}}
			ops[op] = s
		}
		return s
	}
	for name, def := range pkgregistry.All() {
		for _, m := range def.OpMetas {
			s := ensure(m.OperationType, name)
			s.pkg = name // the meta owner is the authoritative describer
			if fullDescriptor(m) {
				s.fullDescriptor = true
			}
		}
		for _, p := range def.Permissions {
			s := ensure(p.OperationType, name)
			for _, m := range exemptionShape.FindAllStringSubmatch(p.Note, -1) {
				s.exemptions[m[1]] = true
			}
		}
	}
	return ops
}

// appOpDebt is the shrink-only baseline: ops the vertical apps already wire
// screens to that carry no descriptor and no exemption, enumerated at the
// gate's birth (2026-08-20 audit). Each is real descriptor work in the OWNING
// package — the descriptor sweep closes an entry by shipping the OpMetaSpec
// (or a coded exemption where a mechanism is genuinely missing, e.g. the
// object-attach pair's client-minted object id) and DELETING it here. Same
// discipline as lint-package-standard's s1Debt: an entry that stops violating
// fails the gate too, so the baseline cannot become an amnesty. S1's own
// census could not see these — they are operator-granted, and S1 treats
// `operator` as a trusted admin tool that hardcodes its own dispatch — but a
// shipped staff FORM is proof a person triggers the op, and the staff-worlds
// catalog (edgeCatalog's held-role walk) cannot render what nothing describes.
var appOpDebt = map[string]string{
	"AttachObject":   "objects-base",    // inputs are the byte-plane upload response (digest/size/contentType), producible by no template
	"CreateLocation": "location-domain", // one op on THREE leaf DDLs — a single static Dispatch.Class cannot express the class choice
	"DetachObject":   "objects-base",    // oid names an entity no owner-anchored lens projects; the fix is a read surface, not a marker
}

// quotedOpLike matches a quoted PascalCase identifier — the shape every
// Lattice operationType has. Lowercase strings (class names, field names)
// never match, which is what keeps the whole-file scan quiet.
var quotedOpLike = regexp.MustCompile(`["']([A-Z][A-Za-z]+)["']`)

// keyedOpLike matches a bare PascalCase identifier in object-key position
// (`SignLease: {…}`). A form registry keyed by op name (loftspace's
// COMPLETIONS idiom) references ops through UNQUOTED JS object keys, which the
// quoted scan cannot see — that is exactly how SignLease escaped the gate's
// original census. Membership in the registered-op set is the filter, so a
// Go struct field or a JS label that merely LOOKS PascalCase never reports.
var keyedOpLike = regexp.MustCompile(`\b([A-Z][A-Za-z]+)\s*:`)

// submissionContext marks a line as an op-submission site: an envelope's
// operationType field being assigned (JS or Go spelling), or the apps' shared
// submit helpers taking the op name as their first argument.
var submissionContext = regexp.MustCompile(`(?i)operationtype["']?\s*[:=]|(?:submitOp|opOrThrow)\s*\(`)

type ref struct {
	file string
	line int
	op   string
	// definitive marks a submission-context hit (R1 applies); a bare quoted
	// match of a registered op name elsewhere is R2-only evidence.
	definitive bool
}

// scanFile extracts op references from one source file. Definitive refs come
// from submission-context lines, where every quoted PascalCase string in the
// segment from the context marker to the argument/field boundary is taken (a
// ternary's both arms included); R2-only refs are any quoted string equal to
// a registered op name, anywhere in the file.
func scanFile(path string, known map[string]*opStatus) []ref {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []ref
	for i, line := range strings.Split(string(data), "\n") {
		if loc := submissionContext.FindStringIndex(line); loc != nil {
			segment := line[loc[1]:]
			// The op name lives before the next argument/field boundary: the
			// first top-level comma or closing brace. A ternary uses ? and :
			// only, so both arms survive the cut.
			if cut := strings.IndexAny(segment, ",}"); cut >= 0 {
				segment = segment[:cut]
			}
			for _, m := range quotedOpLike.FindAllStringSubmatch(segment, -1) {
				out = append(out, ref{path, i + 1, m[1], true})
			}
		}
		for _, m := range quotedOpLike.FindAllStringSubmatch(line, -1) {
			if _, ok := known[m[1]]; ok {
				out = append(out, ref{path, i + 1, m[1], false})
			}
		}
		for _, m := range keyedOpLike.FindAllStringSubmatch(line, -1) {
			if _, ok := known[m[1]]; ok {
				out = append(out, ref{path, i + 1, m[1], false})
			}
		}
	}
	return out
}

func appSources(appDir string) []string {
	var files []string
	filepath.WalkDir(appDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".test.mjs") {
			return nil
		}
		switch filepath.Ext(name) {
		case ".go", ".js", ".mjs", ".html":
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
		}
	}

	known := registeredOps()

	appDirs, _ := filepath.Glob("cmd/*-app")
	sort.Strings(appDirs)

	var issues []string
	debtSeen := map[string]bool{}
	for _, appDir := range appDirs {
		distinct := map[string]bool{}
		sites := 0
		// reported de-duplicates R2 findings per (app, op): the violation is
		// the relationship, not each mention, and one line per op keeps the
		// failure readable.
		reported := map[string]bool{}
		for _, f := range appSources(appDir) {
			for _, r := range scanFile(f, known) {
				status, registered := known[r.op]
				if r.definitive {
					sites++
					distinct[r.op] = true
					if !registered {
						issues = append(issues, fmt.Sprintf(
							"%s:%d: R1 — submits operationType %q, which no registered package declares (a rename/typo that today fails only when a person clicks). Fix the literal, or register the op in its owning package.",
							r.file, r.line, r.op))
						continue
					}
				}
				if !registered || reported[r.op] {
					continue
				}
				switch {
				case status.fullDescriptor:
					// Described — the hardcoded form is tolerated debt.
				case anyIn(status.exemptions, clientMechanismCodes):
					// A client-side ceremony/agent verb: the app IS the
					// sanctioned consumer.
				case anyIn(status.exemptions, machineryCodes):
					reported[r.op] = true
					issues = append(issues, fmt.Sprintf(
						"%s:%d: R2 — references %q, but %s exempts it as machinery (%s: \"no person dispatches this\"). This UI falsifies that premise: either the op is human-triggered (give it a full OpMetaSpec; the exemption was wrong) or this surface should not exist.",
						r.file, r.line, r.op, status.pkg, codesOf(status.exemptions)))
				case len(status.exemptions) > 0:
					// An exemption code outside both families (a future
					// vocabulary addition): unknown posture — surface it.
					reported[r.op] = true
					issues = append(issues, fmt.Sprintf(
						"%s:%d: R2 — references %q, whose exemption code(s) %s are not classified by this gate. Add the code to machineryCodes or clientMechanismCodes here, with its citation.",
						r.file, r.line, r.op, codesOf(status.exemptions)))
				default:
					reported[r.op] = true
					if _, baselined := appOpDebt[r.op]; baselined {
						debtSeen[r.op] = true
						continue
					}
					issues = append(issues, fmt.Sprintf(
						"%s:%d: R2 — references %q (registered by %s), which carries no full OpMetaSpec and no [no-op-meta: …] exemption. An op reachable from a screen is user-facing by demonstration: describe it in %s (clinic-domain/opmetas.go idiom).",
						r.file, r.line, r.op, status.pkg, status.pkg))
				}
			}
		}
		fmt.Printf("lint-app-op-descriptors: %s — %d distinct op(s) across %d submission site(s)\n",
			appDir, len(distinct), sites)
	}

	// Shrink-only: a baseline entry that no app still violates — the
	// descriptor shipped, an exemption landed, or the UI is gone — must be
	// deleted, or the ledger rots into an amnesty.
	for _, op := range sortedKeys(appOpDebt) {
		if !debtSeen[op] {
			issues = append(issues, fmt.Sprintf(
				"appOpDebt lists %q (%s), which no app still violates — delete the entry.", op, appOpDebt[op]))
		}
	}

	if len(issues) == 0 {
		fmt.Println("lint-app-op-descriptors: 0 issues")
		return
	}
	for _, s := range issues {
		fmt.Println(s)
	}
	fmt.Printf("lint-app-op-descriptors: %d issue(s)\n", len(issues))
	if strict {
		os.Exit(1)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func anyIn(have map[string]bool, family map[string]bool) bool {
	for c := range have {
		if family[c] {
			return true
		}
	}
	return false
}

func codesOf(have map[string]bool) string {
	var out []string
	for c := range have {
		out = append(out, c)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
