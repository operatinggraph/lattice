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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// Empty: every lens-declaring package executes its cypher. The rule now
	// binds with no exemptions, so a new lens ships with a test that runs it
	// or reds the gate.
	"lens-cypher-test": {},
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

	defs := map[string]pkgmgr.Definition{}
	for _, name := range pkgregistry.Names() {
		def, _ := pkgregistry.Lookup(name)
		defs[name] = def
		dir := filepath.Join("packages", name)
		checkS1(rep, name, def, seenS1Debt)
		checkReadTemplates(rep, name, def, seenReadTemplateDebt)
		checkS6(rep, name, dir, def, seenS6Debt)
		checkS7(rep, name, dir, def)
	}
	// S9 is corpus-wide: the collision only exists BETWEEN packages, so it
	// cannot be decided while walking one.
	checkS9(rep, defs)
	// S10 is corpus-wide for the same reason and one more: the helpers it pins
	// are pasted per SCRIPT, so the copies do not line up with packages at all.
	checkS10(rep)

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

// checkS9 requires that an operationType granted beyond the trusted-tool roles
// is claimed by exactly ONE package.
//
// A standing grant is matched by operationType string equality alone
// (Contract #6; processor.matchPlatformPermission), and the envelope's `class`
// — which selects the DDL that will run — is a client-supplied hint step 3
// never reads. operationType is therefore a GLOBAL namespace: if two packages
// each admit the name in some DDL's permittedCommands, a grant issued by one
// authorizes the caller against the other's script too.
//
// While every claimant grants the name to `operator` alone that is harmless —
// the operator is unconfined everywhere by design. The moment ONE claimant
// widens to a person-held role, that role reaches every other claimant's
// script, and those scripts were written expecting operator-only callers, so
// they typically carry no confinement at all. That is a cross-vertical
// privilege escalation produced by a one-word permission edit, invisible in the
// diff of the package making it.
//
// The fix is to give the widened op a vertical-unique name (cafe-ledger's
// CreditCafeAccount), which is the same prefixing device packages already use
// for colliding DDL canonical names and guard aspects.
func checkS9(rep *report, defs map[string]pkgmgr.Definition) {
	claimants := map[string]map[string]bool{}
	for name, def := range defs {
		for _, d := range def.DDLs {
			for _, op := range d.PermittedCommands {
				if claimants[op] == nil {
					claimants[op] = map[string]bool{}
				}
				claimants[op][name] = true
			}
		}
	}
	for _, name := range sortedKeys(defs) {
		for _, p := range defs[name].Permissions {
			if !userFacing(p) {
				continue
			}
			others := make([]string, 0, len(claimants[p.OperationType]))
			for c := range claimants[p.OperationType] {
				if c != name {
					others = append(others, c)
				}
			}
			if len(others) == 0 {
				continue
			}
			sort.Strings(others)
			rep.issuef("%s: S9 — %s is granted to %s, but the operationType %q is also admitted by %s. A standing grant matches on operationType alone (the envelope `class` picks the DDL and step 3 never reads it), so that role reaches those packages' scripts too — where no such grant was ever intended. Give this op a vertical-unique name (cafe-ledger's CreditCafeAccount idiom).",
				name, opRef{name, p.OperationType, p.Scope}, strings.Join(humanRoles(p), "+"),
				p.OperationType, strings.Join(others, ", "))
		}
	}
}

// sharedGuardHelpers are the Starlark guard helpers that carry NO
// package-specific policy: each takes plain arguments and answers a question
// about the graph, so every copy of one should compute the same answer. They are
// pasted per SCRIPT rather than per package (there is no Starlark prelude yet),
// so the corpus holds many copies and a fix applied to one silently leaves the
// rest defective.
//
// That is not hypothetical. `worksAt_covers` shipped a walk that followed only
// the LAST containedIn parent per level while the read-side lenses unioned every
// branch, so staff wired to a discarded branch were denied writes they held. The
// lane row naming the defect named three packages; the corpus held NINE copies
// across seven. Whoever fixes the next one needs the gate to tell them how many
// there are.
//
// `actor_holds_operator` is here BECAUSE it is the operator escape — the single
// branch that lets a caller past every workplace guard in the corpus, and so the
// last helper that should be trusted to drift. It reached this list the hard way:
// it was first excluded on the claim that identity-domain and service-domain
// "resolve the operator differently", which was inferred from a digest count and
// was false. All twelve copies walk `holdsRole` → `.canonicalName == "operator"`
// identically; the only difference was the NAME of the page-limit constant, five
// spellings of the same 50. The constants are now one name and the exclusion is
// gone. An exclusion justified by an unverified premise is worse than no gate,
// because the recorded reason stops the next reader from re-checking.
//
// `parts_of` is here because it is where a key stops being a string and becomes
// a TYPED vertex reference: every guard downstream spends the type and id it
// hands back, so a lax copy is a type check that silently did not happen. It was
// the most-copied helper in the corpus (31 copies, more than the other three
// combined) and held seven implementations. Six differences were cosmetic or
// unreachable; the seventh was not — orchestration-base tested `len(parts) < 3`
// where every other copy tested `!= 3`, which ACCEPTS a four-segment ASPECT key
// as a vertex reference and truncates it to (parts[1], parts[2]).
//
// The laxity is the whole defect on its own, and it reached ALL ELEVEN callers
// of that copy, not just the untyped one: `want_type` is compared against
// parts[1], which a four-segment aspect key still fills correctly, so
// `parts_of("vtx.meta.<id>.canonicalName", "forOperation", "meta")` passes the
// type check and returns ("meta", <id>). A first draft of this comment blamed
// the combination of `< 3` with the empty want_type at ddls.go:274 and called
// that the corpus's only untyped caller. Both halves were wrong — thirteen call
// sites across seven packages passed an empty want_type, and the type check
// never protected anyone from this — which is why the fix is stated here as the
// arity test alone. Two reviewers caught it independently; see the paragraph
// above on what a wrong recorded reason costs.
//
// Deliberately NOT listed: `require_workplace`, whose copies genuinely differ on
// POLICY, each documented at its site — clinic-reminders omits the
// validated-target exemption (its ops have no consumer self path, so a self
// exemption would let a front-desk actor forge target == actor), and
// maintenance-domain factors the non-exempt half into `enforce_workplace`. Both
// were read before being excluded. A variant that genuinely differs gets a
// different NAME, which is what `enforce_workplace` already does; this list binds
// only helpers where variation has no legitimate meaning.
var sharedGuardHelpers = []string{"worksAt_covers", "workplace_exempt", "actor_holds_operator", "parts_of"}

// guardConstants are the module-level Starlark constants the pinned helpers read.
// Pinning helper BODIES is not enough on its own: a body references its bounds by
// NAME, so the digest is identical whether the constant beside it says 50 or 10.
// Lowering `ROLE_PAGE_LIMIT` in one script would silently cost an identity with
// more roles than that its operator escape — a guard weakened without touching a
// single line the body-digest can see. Each of these must hold ONE value across
// the corpus.
var guardConstants = []string{
	"ROLE_PAGE_LIMIT",
	"WORKPLACE_PARENT_PAGE_LIMIT",
	"WORKPLACE_MAX_DEPTH",
	"WORKPLACE_MAX_NODES",
}

// guardConstantRe matches a module-level Starlark constant assignment in column 0
// — `WORKPLACE_MAX_DEPTH = 8`.
var guardConstantRe = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*) = (.+)$`)

// checkGuardConstants requires each guardConstant to have a single value
// corpus-wide. WORKPLACE_MAX_DEPTH additionally has to match the read side's hop
// range: `range(N)` tests depths 0..N-1, and the lenses union `[:containedIn*0..7]`,
// so 8 is the value that makes the two walks agree (a mismatch here was a real
// over-grant once — facet-staff-worlds-design.md §9.3).
func checkGuardConstants(rep *report, sources []string) {
	values := map[string]map[string][]string{} // const -> value -> sites
	for _, path := range sources {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, m := range guardConstantRe.FindAllStringSubmatch(string(data), -1) {
			name, val := m[1], strings.TrimSpace(m[2])
			for _, want := range guardConstants {
				if name != want {
					continue
				}
				if values[name] == nil {
					values[name] = map[string][]string{}
				}
				values[name][val] = append(values[name][val], path)
			}
		}
	}
	for _, name := range guardConstants {
		byVal := values[name]
		if len(byVal) < 2 {
			continue
		}
		vals := make([]string, 0, len(byVal))
		for v := range byVal {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		var parts []string
		for _, v := range vals {
			sites := byVal[v]
			sort.Strings(sites)
			parts = append(parts, fmt.Sprintf("%s in %s", v, strings.Join(sites, ", ")))
		}
		rep.issuef("S10 — the guard constant %s has %d different values across the corpus (%s). The pinned helpers reference their bounds by NAME, so a body-digest cannot see this: one script quietly enforcing a smaller bound is a guard weakened without any line the digest compares having changed. Give every copy the same value, or give the outlier a different name if it is genuinely a different bound.",
			name, len(byVal), strings.Join(parts, "; "))
	}
}

// minGuardHelperCopies is the floor below which S10 assumes it is looking at the
// wrong corpus rather than at a clean one. The rule's whole premise is that these
// helpers are pasted MANY times; finding one copy, or none, means the scan broke
// (wrong cwd, a moved corpus) or the shared prelude finally landed — and either
// way a silent pass would be a gate reporting success for work it never did.
const minGuardHelperCopies = 2

// checkS10 requires every copy of a sharedGuardHelper to have the same CODE.
// Comments are excluded: each package's copy legitimately explains itself in
// terms of its own resolver, and prose drift is not a defect. Only the
// statements have to agree.
func checkS10(rep *report) {
	sources := packageSources()
	checkGuardConstants(rep, sources)
	for _, helper := range sharedGuardHelpers {
		bodies := map[string][]string{} // code digest -> sites
		for _, path := range sources {
			for _, site := range starlarkFuncBodies(path, helper) {
				bodies[site.code] = append(bodies[site.code], site.where)
			}
		}
		total := 0
		for _, sites := range bodies {
			total += len(sites)
		}
		if total < minGuardHelperCopies {
			rep.issuef("S10 — found %d cop%s of %s under packages/, expected at least %d. Either the scan is looking at the wrong tree (S10 walks packages/ relative to the working directory) or these helpers moved into a shared prelude — in which case point this rule at the prelude instead of deleting it. A consistency gate that silently finds nothing reports success for work it never did.",
				total, map[bool]string{true: "y", false: "ies"}[total == 1], helper, minGuardHelperCopies)
			continue
		}
		if len(bodies) < 2 {
			continue
		}
		digests := make([]string, 0, len(bodies))
		for d := range bodies {
			digests = append(digests, d)
		}
		// Report against the majority implementation; the odd ones out are what
		// an author needs pointed at.
		sort.Slice(digests, func(i, j int) bool {
			if len(bodies[digests[i]]) != len(bodies[digests[j]]) {
				return len(bodies[digests[i]]) > len(bodies[digests[j]])
			}
			return digests[i] < digests[j]
		})
		majority := bodies[digests[0]]
		sort.Strings(majority)
		for _, d := range digests[1:] {
			sites := bodies[d]
			sort.Strings(sites)
			rep.issuef("S10 — %s at %s does not match the %d copies that agree (e.g. %s); %s has %d distinct implementations across %d copies. This helper carries no package-specific policy, so every copy must compute the same answer, indentation included; a divergent one is a guard that disagrees with itself. Apply the fix to ALL %d copies — not just the one you were looking at — or give the variant a different NAME if the difference is deliberate (maintenance-domain's enforce_workplace is the worked example).",
				helper, strings.Join(sites, ", "), len(majority), majority[0],
				helper, len(bodies), total, total)
		}
	}
}

// starlarkFuncSite is one `def <name>(...)` occurrence: where it is, and a
// digest of its statements with comments and blank lines removed.
type starlarkFuncSite struct {
	where string
	code  string
}

// starlarkFuncBodies finds every top-level `def <name>(` in a Go file's embedded
// Starlark. A body runs until the next line that starts in column 0 with
// content — the next def, a module constant, or the raw-string terminator.
func starlarkFuncBodies(path, name string) []starlarkFuncSite {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	prefix := "def " + name + "("
	var out []starlarkFuncSite
	for i, ln := range lines {
		if !strings.HasPrefix(ln, prefix) {
			continue
		}
		// The `def` line is part of the digest, not just the scan anchor. A body
		// can only be pinned against a fixed SIGNATURE: two copies agreeing
		// statement-for-statement still disagree if one declares a default
		// (`def parts_of(key, name, want_type=""):`), because every call in that
		// script may then omit the argument and skip the check the statements
		// perform. That is not hypothetical — it is the shape two copies of
		// parts_of were in before they were converged.
		code := []string{ln}
		for j := i + 1; j < len(lines); j++ {
			s := lines[j]
			// A body ends at the first line with content in column 0. Decode a
			// full rune: s[0] on a multibyte leading character is a 0xC2–0xF4
			// byte, which is not a space, so a byte-wise test would misread an
			// exotic indent (NBSP, ideographic space) as content and truncate.
			if r, _ := utf8.DecodeRuneInString(s); strings.TrimSpace(s) != "" && !unicode.IsSpace(r) {
				break
			}
			t := strings.TrimSpace(s)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			// Indentation is SEMANTICS in Starlark, so it is hashed, not
			// stripped. Flattening would let two copies digest alike while one
			// nests a statement inside the loop above it — which is precisely
			// how the walk this rule exists to protect went wrong (a
			// `nxt = lk.targetVertex` at the wrong depth kept the wrong parent).
			// The depth is normalized to a count of leading whitespace runes so
			// a tabs-vs-spaces reindent is not reported as a behaviour change,
			// while a change in nesting is.
			depth := 0
			for _, r := range s {
				if !unicode.IsSpace(r) {
					break
				}
				depth++
			}
			code = append(code, fmt.Sprintf("%d\x00%s", depth, t))
		}
		sum := sha256.Sum256([]byte(strings.Join(code, "\n")))
		out = append(out, starlarkFuncSite{
			where: fmt.Sprintf("%s:%d", path, i+1),
			code:  hex.EncodeToString(sum[:8]),
		})
	}
	return out
}

// packageSources lists every non-test Go file under packages/ — where the
// embedded Starlark lives.
func packageSources() []string {
	var out []string
	_ = filepath.WalkDir("packages", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, filepath.ToSlash(path))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]pkgmgr.Definition) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
