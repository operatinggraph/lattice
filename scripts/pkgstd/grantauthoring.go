// Package pkgstd carries the bodies of lint-package-standard rules that must be
// TESTED, not merely run.
//
// `scripts/lint-package-standard.go` is `//go:build ignore` — a script `go run`
// compiles on demand — so nothing declared in it can hold a Go test. That is
// tolerable for a rule whose failure is a missing descriptor and whose evidence
// is the whole corpus walking through it every CI run. It is not tolerable for
// a rule that IS a security boundary: the grant-authoring gate's entire value
// is that it fires, and a corpus in which zero packages violate it looks
// identical whether the rule works or was never wired up. So its body lives
// here, where a fixture can prove it fires, and the script calls it inside the
// same `pkgregistry` walk as every other rule.
package pkgstd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// bodyRewritingOps are the operationTypes that can rewrite an EXISTING
// permission vertex's `data` — its operationType, its scope, and the
// `data.origin` provenance stamp Contract #6 §6.1 keys the reserved-operation
// refusal on. Each maps to what it rewrites, so a failure says why rather than
// just naming a forbidden string.
//
// The reservation is on authoring, not on dispatch: rbac-domain still ships the
// `UpdatePermission` Starlark branch and still admits it in permittedCommands.
// What no package may do is confer it, because an op that can re-target a
// permission vertex after authoring makes every origin marker forgeable and
// rule 1's write-once precondition false.
//
// This gate covers the package-declared grant channel only. `UpgradePackage`'s
// bootstrap DDL takes client-supplied mutations against any `vtx.*` key, which
// reaches the same bodies without any PermissionSpec — a separate gap, filed
// separately, and not something this rule can see.
var bodyRewritingOps = map[string]string{
	"UpdatePermission": "rewrites an existing permission vertex's operationType/scope — and, once provenance lands, its data.origin — after authoring",
}

// sanctionMarker is what a PermissionSpec's Note carries to declare that this
// grant is a deliberate, reviewed exception:
// `[grant-authoring-sanctioned: <code> — <prose>]`.
//
// It mirrors the `[no-op-meta: …]` convention this gate's sibling rules use
// (the Standard's own device: the exception is stated where the declaration
// is, so the next reader can tell a decision from an oversight) rather than an
// allowlist file keyed by package name, which would put the reason somewhere
// nobody editing the grant has open.
const sanctionMarker = "[grant-authoring-sanctioned:"

// sanctionShape extracts the code and the prose. The code is captured openly
// (`[a-z-]+`) rather than by a closed alternation so an unrecognized one can be
// NAMED in the failure — the same reason `exemptionShape` does it, and what
// lets the gate tell "you wrote a code I don't know" apart from "you wrote no
// code at all".
//
// The prose class excludes `[` as well as `]`. Excluding only `]` lets an
// UNTERMINATED marker reach forward — across newlines, across a whole
// paragraph — and borrow the `]` of some unrelated bracketed reference later in
// the Note, so a marker that was never closed parses as a valid sanction and
// the grant is admitted. Refusing to cross a bracket in either direction makes
// that case fail closed: the match does not happen, and the marker is reported
// as the malformed declaration it is.
var sanctionShape = regexp.MustCompile(`\[grant-authoring-sanctioned:\s*([a-z-]+)\s*—\s*([^\[\]]*)\]`)

// sanctionCodes is the closed vocabulary a sanction may claim. Each names the
// MISSING MECHANISM that leaves a body rewrite as the only route, so the
// sanction expires when that mechanism ships instead of outliving its reason:
// retire a code here the moment its mechanism lands, and every Note still
// claiming it reds this gate.
//
// The corpus claims neither today. They exist so the hatch is usable at all —
// a gate whose only outcome is "denied" is a gate someone deletes under
// deadline rather than declares against.
var sanctionCodes = map[string]string{
	"no-remint-path":           "the entity has no tombstone-and-remint route, so a correction can only rewrite the body in place",
	"pre-provenance-migration": "a one-time correction of vertices authored before origin stamping, until the pre-existing-vertex sweep ships",
}

// GrantAuthoringIssues reports every PermissionSpec in `def` that confers a
// body-rewriting operationType without a well-formed sanction. It reads the
// COMPILED Definition, so a spec built by a helper closure, a loop over
// components, or a named constant resolves to its real OperationType — the
// idiom a source scan for `PermissionSpec{OperationType: "…"}` would miss, and
// the idiom rbac-domain itself uses.
//
// GrantsTo is not consulted. A spec with an empty GrantsTo still mints the
// permission vertex, leaving a runtime `GrantPermission` one link away from
// conferring it, so the declaration is the thing that is denied — not the link
// it happens to ship with.
func GrantAuthoringIssues(pkg string, def pkgmgr.Definition) []string {
	var out []string
	for _, p := range def.Permissions {
		rewrites, reserved := bodyRewritingOps[p.OperationType]
		if !reserved {
			continue
		}
		if !strings.Contains(p.Note, sanctionMarker) {
			out = append(out, fmt.Sprintf("%s: grant-authoring — %s must not be granted: it %s, and Contract #6 §6.1 rule 1 requires that body to be write-once (an origin nobody can rewrite is what makes the reserved-operation refusal enforceable). The op stays dispatchable; an ungranted op is denied at step 3 by absence. If this grant is genuinely required, declare it in the spec's Note as %s <code> — <reason>], with a code from: %s.",
				pkg, p.OperationType, rewrites, sanctionMarker, strings.Join(sortedSanctionCodes(), ", ")))
			continue
		}
		if err := sanctionError(p.Note); err != "" {
			out = append(out, fmt.Sprintf("%s: grant-authoring — %s claims a sanction that does not hold: %s Valid codes: %s.",
				pkg, p.OperationType, err, strings.Join(sortedSanctionCodes(), ", ")))
		}
	}
	return out
}

// sanctionError reports why a Note's marker is not a valid sanction, or "" when
// it is. It is deliberately unforgiving about the shape: a marker that does not
// parse is a malformed declaration, not an absent one, and passing it through
// would restore exactly the arbitrary-prose amnesty a closed vocabulary
// replaces — on the one gate where the amnesty is a capability-plane grant.
func sanctionError(note string) string {
	// One spec, one declaration. Only the FIRST marker is parsed below, so a
	// Note carrying several has its later ones validated by nobody — and a
	// reviewer reading the grant cannot tell which declaration the gate
	// honoured. An ambiguous declaration is refused, not resolved.
	if n := strings.Count(note, sanctionMarker); n > 1 {
		return fmt.Sprintf("the Note carries %d `%s …]` markers — a spec declares its sanction once, or nobody can tell which declaration the gate read.", n, sanctionMarker)
	}
	m := sanctionShape.FindStringSubmatch(note)
	if m == nil {
		return "it does not parse as `" + sanctionMarker + " <code> — <prose>]` (an em dash separates the code from the reason)."
	}
	code, prose := m[1], strings.TrimSpace(m[2])
	if _, ok := sanctionCodes[code]; !ok {
		return "unknown sanction code (" + code + ") — a sanction has to name the mechanism whose absence leaves a body rewrite as the only route, so it can be re-checked when that mechanism ships."
	}
	if prose == "" {
		return "sanction code (" + code + ") carries no reason — the code says WHICH mechanism is missing, the prose says why it forces THIS grant."
	}
	return ""
}

// sortedSanctionCodes renders the vocabulary deterministically for a failure
// message, each code followed by what it claims.
func sortedSanctionCodes() []string {
	out := make([]string, 0, len(sanctionCodes))
	for code, meaning := range sanctionCodes {
		out = append(out, code+" ("+meaning+")")
	}
	sort.Strings(out)
	return out
}
