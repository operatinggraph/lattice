package pkgmgr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
	"github.com/operatinggraph/lattice/internal/processor"
)

// EnabledArtifactKinds is the artifact-kind allow-list for the capability-author
// package (ai-authored-capabilities-design.md §3.2). The kinds are ordered by the
// deterministic-validatability spine: "lens" (Fire 1), "grant" (Fire 2 fast-
// follow), "weaverTarget"/"loomPattern" (Fire 3), and "vertexTypeDDL"/"opMeta"
// (Fire 4, Starlark-bearing — gated on the verified-pure Starlark sandbox dry-run,
// internal/starlarksandbox, plus the sensitive-ref-mac-provenance-design.md §7
// condition-2 lint) are all enabled here. A kind outside this set is never valid,
// regardless of content.
var EnabledArtifactKinds = map[string]bool{
	"lens":          true,
	"grant":         true,
	"weaverTarget":  true,
	"loomPattern":   true,
	"vertexTypeDDL": true,
	"opMeta":        true,
}

// enabledArtifactKindsList returns EnabledArtifactKinds' keys sorted, for a
// deterministic "disabled kind" error message.
func enabledArtifactKindsList() []string {
	kinds := make([]string, 0, len(EnabledArtifactKinds))
	for k := range EnabledArtifactKinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// SpecLabels is what a static parse of one lens spec tells the installer about
// the label set the runtime would derive that lens's narrowed Core KV consumer
// filter from — a field-for-field mirror of
// internal/refractor/ruleengine/full.LabelFacts, carried as pkgmgr's own type
// for the same reason LensSpec.Source mirrors chronicler's config by JSON shape
// rather than by Go type: pkgmgr must not import the engine (see CypherParser).
//
// The three fields are the inputs the runtime folds together
// (pipeline.useFullEngineBranches): the referenced set, minus each expansion
// label's raw text, plus that label's resolved concrete closure. Referenced
// therefore CONTAINS the raw text of every Expansion member — the engine's
// ReferencedLabels collects label text blind to the `*` sigil — which is why
// every count derived from these two subtracts before it adds.
type SpecLabels struct {
	// Referenced is every vertex-type label the spec's patterns can bind.
	Referenced map[string]struct{}

	// Exhaustive reports whether Referenced is authoritative. False means the
	// runtime must treat every type as relevant — an unlabeled node pattern
	// that is not a re-reference, or any variable-length relationship — and the
	// consumer filter goes broad however few labels the set holds.
	Exhaustive bool

	// Expansion is the subset of labels written with the trailing `*`
	// taxonomy-expansion sigil, each of which the runtime replaces with the
	// concrete types the taxonomy resolves it to.
	Expansion map[string]struct{}
}

// CypherParser abstracts the static openCypher parse pkgmgr needs in two
// places: the "lens" kind's §5 capability-artifact validation, and the
// install-time narrowed-filter budget gate (dynamic-type-taxonomy-design.md
// §10.2), which needs a spec's label sets and cannot get them from a
// parse-only answer.
//
// Injected rather than pkgmgr importing internal/refractor/ruleengine/full
// directly: that package's own test binary transitively imports pkgmgr
// (parse_test.go → packages/identity-hygiene → pkgmgr), so a direct production
// import here would be a cycle. A caller (cmd/lattice, cmd/loupe, tests)
// supplies a full.SpecLabels-backed implementation.
//
// ONE METHOD, WIDENED, rather than a second optional interface the callers
// type-assert. An optional interface makes a missing implementation a silent
// capability gap — an adapter that never grew the second method still satisfies
// CypherParser, still compiles, and the budget gate simply never runs for
// whichever caller wired it. That is precisely the quiet fail-open §10.2 exists
// to replace with a decidable answer. Widening the one method makes the
// compiler the enforcement point instead: every implementer must answer, and
// each adapter's `var _ pkgmgr.CypherParser` assertion fails loudly the day one
// does not.
type CypherParser interface {
	// Parse statically parses ruleBody, returning a non-nil error if it does
	// not parse and the spec's label facts if it does. The label facts are
	// derived from the SAME parse as the error, so a caller can never pair a
	// successful parse with label sets compiled from a different body.
	Parse(ruleBody string) (SpecLabels, error)
}

// ArtifactValidationReport is the §5 record-time deterministic-validation
// verdict for one proposed capability artifact: Valid decides pending vs invalid
// (RecordCapabilityProposal never fails the op on a bad artifact — the proposal
// is always recorded, auditable — it just never becomes dispatchable). Errors is
// the human-readable per-check failure list (stored as the proposal's
// .validation.report for the reviewer).
type ArtifactValidationReport struct {
	Valid  bool
	Errors []string
}

// LensArtifactContent is the JSON shape of a "lens"-kind proposal's
// artifact.content — the constrained subset of pkgmgr.LensSpec an AI-authored
// lens proposal may carry: a plain nats-kv or postgres projection (no
// actor-aggregate Output, no Protected/SecureColumns/GrantTable postures —
// those need a richer scope-check the materializer does not build; see the
// design's §3.2 phase-by-kind boundary). Field names are the wire shape the
// capabilityAuthor adapter's structured output (and the tests exercising it)
// use.
type LensArtifactContent struct {
	CanonicalName string `json:"canonicalName"`
	Adapter       string `json:"adapter"`
	Bucket        string `json:"bucket"`
	Table         string `json:"table"`
	Spec          string `json:"spec"`
}

// GrantArtifactContent is the JSON shape of a "grant"-kind proposal's
// artifact.content — a single Contract #6 permission grant, mirroring
// pkgmgr.PermissionSpec field-for-field: an operationType gated at a scope,
// granted to one or more already-existing roles by canonical name. A "grant"
// artifact never declares a new Role (§3.2 keeps the grant kind to widening an
// existing role's permissions, not minting new roles) — GrantsTo entries must
// name a role the installer's live catalog already knows, checked at apply time
// exactly as a hand-authored package's GrantsTo is.
type GrantArtifactContent struct {
	OperationType string   `json:"operationType"`
	Scope         string   `json:"scope"`
	GrantsTo      []string `json:"grantsTo"`
	Note          string   `json:"note"`
}

// GapActionArtifact is the JSON shape of one entry in a "weaverTarget"-kind
// artifact's `gaps` map — the subset of pkgmgr.GapActionSpec (§10.8's action
// table) the authoring surface exposes, shaped so an AI-authored gap action
// can never carry a shape the engine wouldn't already accept from a
// hand-authored package.
//
// One field is deliberately NOT exposed: OptionalReads. Declaring a key there
// changes the Processor's commit semantics for it (a step-4 snapshot serve, a
// CreateOnly-conditioned create on its observed absence — see
// GapActionSpec.OptionalReads), and the authoring surface stays minimal until
// a real authored consumer needs that power. StepArtifact below does carry
// the field, because Loom's authored steps have one.
type GapActionArtifact struct {
	Action        string            `json:"action"`
	Pattern       string            `json:"pattern,omitempty"`
	Subject       string            `json:"subject,omitempty"`
	Adapter       string            `json:"adapter,omitempty"`
	Operation     string            `json:"operation,omitempty"`
	Assignee      string            `json:"assignee,omitempty"`
	Target        string            `json:"target,omitempty"`
	Params        map[string]string `json:"params,omitempty"`
	Reads         []string          `json:"reads,omitempty"`
	Enumerations  []EnumerationSpec `json:"enumerations,omitempty"`
	IssueCode     string            `json:"issueCode,omitempty"`
	IssueSeverity string            `json:"issueSeverity,omitempty"`
}

// WeaverTargetArtifactContent is the JSON shape of a "weaverTarget"-kind
// proposal's artifact.content (design §3.2, Fire 3) — the constrained subset
// of pkgmgr.WeaverTargetSpec an AI-authored target proposal may carry: the
// base `{targetId, lensRef, gaps}` §10.8 shape, plus the optional prose
// `description` that installs as the target's sibling `.description` aspect.
// The `augur` escalation-policy block is deliberately NOT exposed here — it
// configures AI-reasoning escalation (and, via autoApply, the one standing
// autonomy boundary Andrew has not ratified for even hand-authored packages,
// design §For-Andrew #1) — so an AI proposing its OWN escalation policy is
// out of scope here, same posture as the lens kind's excluded protected/secure
// postures (§3.2).
type WeaverTargetArtifactContent struct {
	TargetID string                       `json:"targetId"`
	LensRef  string                       `json:"lensRef"`
	Gaps     map[string]GapActionArtifact `json:"gaps"`
	// Description carries no authority of its own — it is prose an operator
	// reads on the roster — so it is exposed to the AI path unlike the
	// posture-bearing fields above. Optional, hence omitempty: a
	// description-less artifact stays byte-identical on the wire.
	Description string `json:"description,omitempty"`
}

// StepArtifact is the JSON shape of one entry in a "loomPattern"-kind
// artifact's `steps` list — a field-for-field mirror of pkgmgr.StepSpec
// (§10.5), reused verbatim so an AI-authored step can never carry a shape the
// engine wouldn't already accept from a hand-authored package.
type StepArtifact struct {
	Kind          string            `json:"kind"`
	Operation     string            `json:"operation,omitempty"`
	Guard         map[string]any    `json:"guard,omitempty"`
	Adapter       string            `json:"adapter,omitempty"`
	Params        map[string]any    `json:"params,omitempty"`
	ReplyOp       string            `json:"replyOp,omitempty"`
	InstanceOp    string            `json:"instanceOp,omitempty"`
	Reads         []string          `json:"reads,omitempty"`
	OptionalReads []string          `json:"optionalReads,omitempty"`
	Enumerations  []EnumerationSpec `json:"enumerations,omitempty"`
}

// LoomPatternArtifactContent is the JSON shape of a "loomPattern"-kind
// proposal's artifact.content (design §3.2, Fire 3) — the full
// pkgmgr.LoomPatternSpec §10.5 shape: `{patternId, subjectType,
// completionDomains?, steps}`.
type LoomPatternArtifactContent struct {
	PatternID         string         `json:"patternId"`
	SubjectType       string         `json:"subjectType"`
	CompletionDomains []string       `json:"completionDomains,omitempty"`
	Steps             []StepArtifact `json:"steps"`
}

// HeldPermission is one permission the requesting operator currently holds
// (operationType + scope), as read from their live capability projection
// (Contract #6 §6.x capabilityRoles). It is the caller-supplied basis for the
// "grant" kind's §5 scope check (ai-authored-capabilities-design.md §5, point
// 2): "a grant artifact's conferred authority must be a subset of the
// requesting operator's own held scope". The caller (the bridge at record
// time; the operator's Loupe/CLI submission path at approve time — same
// compute-client-side-then-submit-a-trusted-verdict split as the rest of §5)
// reads this projection fresh and passes it in; ValidateCapabilityArtifact
// never reads a live substrate itself, and takes the slice on faith — it is
// NOT bound to any actor identity here. The grant kind raises the stakes on
// that trust versus the lens kind: whoever builds the real caller (neither
// the bridge's capabilityAuthor adapter nor a Loupe/CLI review path exists
// yet — see package.go's "deliberately not yet built" list) MUST read this
// projection for the actual requesting/approving actor (op.actor), fresh,
// every time — a stale or wrong-actor slice defeats the entire scope check.
type HeldPermission struct {
	OperationType string
	Scope         string

	// Origin is the projected entry's provenance (Contract #6 §6.1), carried
	// through from platformPermissions[].origin rather than re-derived here.
	//
	// It has to travel with the entry because an entry step 3 would REFUSE must
	// not count as held when proposing a new grant of the same op. Absence
	// reads as `runtime` — the fail-closed direction, exactly as at step 3.
	Origin string
}

// covers reports whether a held permission's scope authorizes granting the
// given requested scope. "any" is the broader posture — an operator holding
// "any" for an operationType may grant either "any" or "self"; an operator
// holding only "self" may grant "self" but never "any" (that would let a
// self-scoped operator mint a broader grant than their own).
//
// An entry the Processor would refuse at step 3 covers NOTHING, whatever its
// scope says. The projection is not an authority list: a reserved
// operationType held at runtime origin is refused at submission (Contract #6
// §6.1 rule 3), and letting it satisfy the no-escalation check here would be a
// laundering channel rather than a scope widening. The requester proposes a
// `grant` artifact for that same op; approval runs it through
// CapabilityApplyPlanForProposal → DefinitionForCapabilityArtifact →
// Installer.Apply → build.go's permission mint, which stamps
// `origin: "package"` on everything it applies — repo package and approved
// proposal alike. The re-minted vertex then authorizes at step 3, and its
// `declaredBy` names a package that exists in no manifest. Refusing here is
// what keeps "the installer's stamp means a declared deployment decision"
// true. The predicate is imported from the Processor, never restated, so the
// two sides cannot drift.
func (h HeldPermission) covers(operationType, requestedScope string) bool {
	if h.OperationType != operationType {
		return false
	}
	if processor.WouldRefuseReservedGrant(h.OperationType, h.Origin) {
		return false
	}
	if h.Scope == "any" {
		return true
	}
	return h.Scope == requestedScope
}

// requesterHolds reports whether held contains a permission covering
// (operationType, requestedScope) — the "subset of the requester's own held
// scope" test the grant kind's §5 scope check applies.
func requesterHolds(held []HeldPermission, operationType, requestedScope string) bool {
	for _, h := range held {
		if h.covers(operationType, requestedScope) {
			return true
		}
	}
	return false
}

// ValidateCapabilityArtifact runs the §5 record-time deterministic-validation
// boundary for one proposed artifact (ai-authored-capabilities-design.md §5,
// point 2): a kind outside EnabledArtifactKinds is always invalid; a "lens" kind
// is parsed with the caller-supplied openCypher parser (rejecting unparseable
// cypher) and run through the existing pkgmgr lens validators (validateLensAdapters
// / validateLensBuckets / validateLensReadPath — reused verbatim, not
// reimplemented) via a throwaway single-lens Definition. It never mutates
// anything and never touches a live substrate (no sandbox dry-run / delta
// preview yet — that lands with the bridge-adapter increment that calls this
// against a real Refractor sandbox); it is pure and unit-testable in isolation.
//
// err is non-nil only for a caller contract violation (malformed content JSON
// for an enabled kind) — never for a model-authored defect, which always comes
// back as a non-valid report (auditable, never dispatchable), per §5's "the
// proposal is ALWAYS stored; the verdict decides only pending vs invalid".
//
// requesterHeld is the requesting operator's currently-held permissions (read
// fresh by the caller from their live Contract #6 capability projection) — the
// basis for the "grant" kind's scope check (a grant may never exceed what the
// requester themselves holds). Ignored by every other kind; a caller validating
// a non-grant artifact may pass nil.
//
// sensitiveAspects is the condition-2 rule-2 basis (sensitive-ref-mac-
// provenance-design.md §7) for the "opMeta" kind's declared-read check —
// ignored by every other kind; a caller validating a non-opMeta artifact (or
// one with no live catalog to verify against) may pass nil, though passing
// nil for an opMeta artifact that declares any aspect-naming read fails that
// artifact closed (see validateOpMetaArtifact).
func ValidateCapabilityArtifact(kind string, content json.RawMessage, parser CypherParser, requesterHeld []HeldPermission, sensitiveAspects SensitiveAspectResolver) (ArtifactValidationReport, error) {
	if !EnabledArtifactKinds[kind] {
		return ArtifactValidationReport{
			Valid:  false,
			Errors: []string{fmt.Sprintf("artifact kind %q is not enabled (enabled: %v)", kind, enabledArtifactKindsList())},
		}, nil
	}

	// Condition-2 rule 1 (sensitive-ref-mac-provenance-design.md §7): the
	// literal "$sensitiveRef" marker is Processor-authored — no legitimate
	// artifact of ANY kind ever spells it. Checked once, ahead of the
	// per-kind switch, so it can never be missed by a future kind that
	// forgets to call it.
	if containsSensitiveRefLiteral(content) {
		return ArtifactValidationReport{
			Valid:  false,
			Errors: []string{fmt.Sprintf("artifact content declares the literal %q — sensitive refs are Processor-minted, never AI-authored", sensitiveRefLiteral)},
		}, nil
	}

	switch kind {
	case "lens":
		var lc LensArtifactContent
		if err := json.Unmarshal(content, &lc); err != nil {
			return ArtifactValidationReport{}, fmt.Errorf("pkgmgr: capability materializer: malformed lens artifact content: %w", err)
		}
		// A known-fields check catches an artifact trying to smuggle a field
		// LensArtifactContent doesn't expose (e.g. "protected"/"public"/"grantTable"/
		// "columns"/"secureColumns" — the postures
		// explicitly out of scope, §3.2). Without this, json.Unmarshal above would
		// SILENTLY DROP the unrecognized field and materialize a plain lens anyway —
		// a scope-widening intent quietly downgraded rather than rejected. Treated
		// as a validation FAILURE (stored invalid, auditable), not a caller error:
		// the model may plausibly attempt an out-of-scope posture; §5 wants that
		// visible on the .validation.report, not silently swallowed.
		if extra := unknownLensFields(content); len(extra) > 0 {
			return ArtifactValidationReport{
				Valid: false,
				Errors: []string{fmt.Sprintf(
					"lens artifact content declares out-of-scope field(s) %v — this increment enables only canonicalName/adapter/bucket/table/spec (no protected/public/grantTable/columns/secureColumns postures)",
					extra)},
			}, nil
		}
		return validateLensArtifact(lc, parser), nil
	case "grant":
		var gc GrantArtifactContent
		if err := json.Unmarshal(content, &gc); err != nil {
			return ArtifactValidationReport{}, fmt.Errorf("pkgmgr: capability materializer: malformed grant artifact content: %w", err)
		}
		// Same scope-widening defense as the lens kind's unknownLensFields: a
		// field GrantArtifactContent doesn't expose would otherwise be
		// silently dropped by json.Unmarshal rather than rejected.
		if extra := unknownGrantFields(content); len(extra) > 0 {
			return ArtifactValidationReport{
				Valid: false,
				Errors: []string{fmt.Sprintf(
					"grant artifact content declares out-of-scope field(s) %v — this increment enables only operationType/scope/grantsTo/note",
					extra)},
			}, nil
		}
		return validateGrantArtifact(gc, requesterHeld), nil
	case "weaverTarget":
		var wc WeaverTargetArtifactContent
		if err := json.Unmarshal(content, &wc); err != nil {
			return ArtifactValidationReport{}, fmt.Errorf("pkgmgr: capability materializer: malformed weaverTarget artifact content: %w", err)
		}
		// Same scope-widening defense as the lens kind's unknownLensFields: a field
		// WeaverTargetArtifactContent doesn't expose (namely "augur") would otherwise be
		// silently dropped by json.Unmarshal rather than rejected.
		if extra := unknownWeaverTargetFields(content); len(extra) > 0 {
			return ArtifactValidationReport{
				Valid: false,
				Errors: []string{fmt.Sprintf(
					"weaverTarget artifact content declares out-of-scope field(s) %v — this increment enables only targetId/lensRef/gaps (no augur escalation policy)",
					extra)},
			}, nil
		}
		return validateWeaverTargetArtifact(wc), nil
	case "loomPattern":
		var lp LoomPatternArtifactContent
		if err := json.Unmarshal(content, &lp); err != nil {
			return ArtifactValidationReport{}, fmt.Errorf("pkgmgr: capability materializer: malformed loomPattern artifact content: %w", err)
		}
		// Same scope-widening defense as the lens/weaverTarget kinds: every
		// pkgmgr.LoomPatternSpec top-level field is already exposed by
		// LoomPatternArtifactContent today, so there is no CURRENTLY live
		// out-of-scope posture to smuggle — but the check is kept anyway (not
		// merely asserted in a comment) so a future LoomPatternSpec field added
		// without a matching LoomPatternArtifactContent field fails loudly
		// instead of being silently dropped by json.Unmarshal.
		if extra := unknownLoomPatternFields(content); len(extra) > 0 {
			return ArtifactValidationReport{
				Valid: false,
				Errors: []string{fmt.Sprintf(
					"loomPattern artifact content declares out-of-scope field(s) %v — this increment enables only patternId/subjectType/completionDomains/steps",
					extra)},
			}, nil
		}
		return validateLoomPatternArtifact(lp), nil
	case "vertexTypeDDL":
		var vc VertexTypeDDLArtifactContent
		if err := json.Unmarshal(content, &vc); err != nil {
			return ArtifactValidationReport{}, fmt.Errorf("pkgmgr: capability materializer: malformed vertexTypeDDL artifact content: %w", err)
		}
		// Same scope-widening defense as the other kinds: a field
		// VertexTypeDDLArtifactContent doesn't expose (namely
		// "class"/"sensitive"/"effects") would otherwise be silently dropped
		// by json.Unmarshal rather than rejected.
		if extra := unknownVertexTypeDDLFields(content); len(extra) > 0 {
			return ArtifactValidationReport{
				Valid: false,
				Errors: []string{fmt.Sprintf(
					"vertexTypeDDL artifact content declares out-of-scope field(s) %v — this increment enables only canonicalName/permittedCommands/description/script/inputSchema/outputSchema/fieldDescription/examples (Class is fixed meta.ddl.vertexType; no aspectType/sensitive/effects postures)",
					extra)},
			}, nil
		}
		return validateVertexTypeDDLArtifact(vc), nil
	case "opMeta":
		var oc OpMetaArtifactContent
		if err := json.Unmarshal(content, &oc); err != nil {
			return ArtifactValidationReport{}, fmt.Errorf("pkgmgr: capability materializer: malformed opMeta artifact content: %w", err)
		}
		// Same scope-widening defense as the other kinds: a field
		// OpMetaArtifactContent doesn't expose (namely "sensitive") would
		// otherwise be silently dropped by json.Unmarshal rather than
		// rejected.
		if extra := unknownOpMetaFields(content); len(extra) > 0 {
			return ArtifactValidationReport{
				Valid: false,
				Errors: []string{fmt.Sprintf(
					"opMeta artifact content declares out-of-scope field(s) %v — this increment enables only operationType/presentation/inputSchema/fieldDescriptions/dispatch (no sensitive posture)",
					extra)},
			}, nil
		}
		return validateOpMetaArtifact(oc, sensitiveAspects), nil
	default:
		// Unreachable: EnabledArtifactKinds gates every case above.
		return ArtifactValidationReport{Valid: false, Errors: []string{"unhandled enabled kind " + kind}}, nil
	}
}

// knownWeaverTargetFields are the JSON keys WeaverTargetArtifactContent
// exposes for the "weaverTarget" kind. Mirrors knownLensFields' explicit-
// allow-list rationale.
var knownWeaverTargetFields = map[string]bool{
	"targetId":    true,
	"lensRef":     true,
	"gaps":        true,
	"description": true,
}

// knownGapActionFields are the JSON keys GapActionArtifact exposes for one
// entry in a weaverTarget's `gaps` map. An out-of-scope key on a gap entry
// (e.g. "goal"/"candidates"/"mode"/"augur" — planner-extension surfaces the AI
// path does not enable) is silently DROPPED by json.Unmarshal into
// GapActionArtifact and would materialize as a plain gap, bypassing §5's
// stored-invalid audit trail. Mirrors knownWeaverTargetFields' explicit
// allow-list rationale, one level down.
var knownGapActionFields = map[string]bool{
	"action":        true,
	"pattern":       true,
	"subject":       true,
	"adapter":       true,
	"operation":     true,
	"assignee":      true,
	"target":        true,
	"params":        true,
	"reads":         true,
	"enumerations":  true,
	"issueCode":     true,
	"issueSeverity": true,
}

// unknownWeaverTargetFields decodes content as a generic JSON object and returns
// any key — at the top level (outside knownWeaverTargetFields) OR inside a `gaps`
// entry (outside knownGapActionFields) — that the AI weaverTarget path does not
// expose, sorted for a deterministic report. A per-gap smuggled key is reported
// as `gaps.<col>.<key>`. The nested scan matters because json.Unmarshal into
// GapActionArtifact silently drops an unknown gap key, so without it a
// scope-widening posture buried in a gap (not at the top level) would
// materialize as a plain gap rather than land on the §5 stored-invalid audit
// trail. Mirrors unknownLensFields, extended one level down.
func unknownWeaverTargetFields(content json.RawMessage) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil
	}
	var extra []string
	for k := range raw {
		if !knownWeaverTargetFields[k] {
			extra = append(extra, k)
		}
	}
	if gapsRaw, ok := raw["gaps"]; ok {
		var gaps map[string]json.RawMessage
		if err := json.Unmarshal(gapsRaw, &gaps); err == nil {
			for col, gapRaw := range gaps {
				var gap map[string]json.RawMessage
				if err := json.Unmarshal(gapRaw, &gap); err != nil {
					continue
				}
				for k := range gap {
					if !knownGapActionFields[k] {
						extra = append(extra, "gaps."+col+"."+k)
					}
				}
			}
		}
	}
	sort.Strings(extra)
	return extra
}

// knownLoomPatternFields are the JSON keys LoomPatternArtifactContent exposes
// for the "loomPattern" kind. Mirrors knownLensFields' explicit-allow-list
// rationale.
var knownLoomPatternFields = map[string]bool{
	"patternId":         true,
	"subjectType":       true,
	"completionDomains": true,
	"steps":             true,
}

// knownStepFields are the JSON keys StepArtifact exposes for one entry in a
// loomPattern's `steps` array. A smuggled key on a step is silently dropped by
// json.Unmarshal into StepArtifact and would bypass §5's stored-invalid audit
// trail — the same class as knownGapActionFields one level down from a
// weaverTarget's gaps.
var knownStepFields = map[string]bool{
	"kind":          true,
	"operation":     true,
	"guard":         true,
	"adapter":       true,
	"params":        true,
	"replyOp":       true,
	"instanceOp":    true,
	"reads":         true,
	"optionalReads": true,
	"enumerations":  true,
}

// unknownLoomPatternFields decodes content as a generic JSON object and returns
// any key — at the top level (outside knownLoomPatternFields) OR inside a
// `steps[]` entry (outside knownStepFields) — the AI loomPattern path does not
// expose, sorted for a deterministic report. A per-step smuggled key is
// reported as steps[<i>].<key>. The nested scan matters for the same reason as
// unknownWeaverTargetFields' gaps recursion: json.Unmarshal into StepArtifact
// silently drops an unknown step key, so a scope-widening posture buried in a
// step would bypass §5's stored-invalid audit trail. Mirrors unknownLensFields,
// extended one level down.
func unknownLoomPatternFields(content json.RawMessage) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil
	}
	var extra []string
	for k := range raw {
		if !knownLoomPatternFields[k] {
			extra = append(extra, k)
		}
	}
	if stepsRaw, ok := raw["steps"]; ok {
		var steps []json.RawMessage
		if err := json.Unmarshal(stepsRaw, &steps); err == nil {
			for i, stepRaw := range steps {
				var step map[string]json.RawMessage
				if err := json.Unmarshal(stepRaw, &step); err != nil {
					continue
				}
				for k := range step {
					if !knownStepFields[k] {
						extra = append(extra, fmt.Sprintf("steps[%d].%s", i, k))
					}
				}
			}
		}
	}
	sort.Strings(extra)
	return extra
}

// validateWeaverTargetArtifact is the "weaverTarget" kind's deterministic
// check (design §3.2, §5, Fire 3): materialize the single-target Definition
// and run it through the same validateAll the human package-authoring path
// runs (validateWeaverTargets — TargetID shape/uniqueness, the missing_<gap>
// column convention, the reserved expectedRevision param, and
// validateGapAction's per-action required-field check) — reused, not
// duplicated, so an AI-authored target can never pass a check a hand-authored
// one would fail. LensRef resolution (must name an already-installed lens) is
// a build-time concern (build.go's resolveLensRef), not checked here — same
// posture as a hand-authored package referencing a sibling package's lens by
// NanoID.
func validateWeaverTargetArtifact(wc WeaverTargetArtifactContent) ArtifactValidationReport {
	var errs []string

	if wc.TargetID == "" {
		errs = append(errs, "targetId is required")
	}
	if wc.LensRef == "" {
		errs = append(errs, "lensRef is required")
	}

	def := weaverTargetArtifactDefinition(wc, "", "")
	if err := def.validateAll(); err != nil {
		errs = append(errs, err.Error())
	}

	return ArtifactValidationReport{Valid: len(errs) == 0, Errors: errs}
}

// weaverTargetArtifactDefinition is the single shape both record-time
// validation (validateWeaverTargetArtifact, a throwaway unnamed Definition)
// and apply-time materialization (DefinitionForCapabilityArtifact) build from
// a WeaverTargetArtifactContent — mirrors lensArtifactDefinition's
// byte-for-byte validated-equals-materialized guarantee.
func weaverTargetArtifactDefinition(wc WeaverTargetArtifactContent, name, version string) Definition {
	gaps := make(map[string]GapActionSpec, len(wc.Gaps))
	for col, ga := range wc.Gaps {
		// Field-by-field, not a type conversion: GapActionArtifact is a
		// deliberately restricted subset of GapActionSpec (no goal-authoring
		// surface — same posture as WeaverTargetArtifactContent excluding
		// Augur below), so the two types no longer share an identical
		// underlying field sequence.
		gaps[col] = GapActionSpec{
			Action:        ga.Action,
			Pattern:       ga.Pattern,
			Subject:       ga.Subject,
			Adapter:       ga.Adapter,
			Operation:     ga.Operation,
			Assignee:      ga.Assignee,
			Target:        ga.Target,
			Params:        ga.Params,
			Reads:         ga.Reads,
			Enumerations:  ga.Enumerations,
			IssueCode:     ga.IssueCode,
			IssueSeverity: ga.IssueSeverity,
		}
	}
	return Definition{
		Name:    name,
		Version: version,
		WeaverTargets: []WeaverTargetSpec{{
			TargetID:    wc.TargetID,
			LensRef:     wc.LensRef,
			Gaps:        gaps,
			Description: wc.Description,
		}},
	}
}

// validateLoomPatternArtifact is the "loomPattern" kind's deterministic check
// (design §3.2, §5, Fire 3): materialize the single-pattern Definition and run
// it through the same validateAll the human package-authoring path runs
// (validateLoomPatterns — patternId/subjectType presence + uniqueness, ≥1
// step, and each step kind's exact §10.5 shape) — reused, not duplicated, so
// an AI-authored pattern can never pass a check a hand-authored one would
// fail — PLUS one check stronger than the hand-authored path: every step's
// optional Guard is run through the shared §10.5 grammar parser
// (guardStarlarkFree, below). validateLoomPatterns' own doc comment disclaims
// interpreting Guard bodies at all (deferred to the engine at CDC load, same
// as a hand-authored package) — but §3.2's taxonomy table states this KIND's
// validation guarantee as "no Starlark", and the reserved Starlark escape
// hatch ({reads, starlark}, Contract #10 §10.5) is otherwise well-formed JSON
// that would sail through validateLoomPatterns unnoticed and record as
// pending/valid. Failing closed at record time (never weaker than the human
// path, and here deliberately stronger for the higher-scrutiny AI path) beats
// deferring the rejection to CDC load or dispatch-time guard evaluation.
func validateLoomPatternArtifact(lp LoomPatternArtifactContent) ArtifactValidationReport {
	var errs []string

	def := loomPatternArtifactDefinition(lp, "", "")
	if err := def.validateAll(); err != nil {
		errs = append(errs, err.Error())
	}
	for i, s := range lp.Steps {
		if err := guardStarlarkFree(s.Guard); err != nil {
			errs = append(errs, fmt.Sprintf("step %d: guard: %v", i, err))
		}
	}

	return ArtifactValidationReport{Valid: len(errs) == 0, Errors: errs}
}

// guardStarlarkFree parses a step's optional Guard under the shared §10.5
// guardgrammar (the same parser Loom step guards and op-DDL Effects guards
// use — internal/guardgrammar, already imported by this package's
// validateEffects) and rejects anything that fails to parse, INCLUDING the
// well-formed-but-reserved Starlark escape hatch ({reads, starlark}) —
// AI-authored Starlark is Fire-4-gated (§3.2), so a guard using that shape is
// rejected here rather than left to fail later at CDC load or guard
// evaluation. A nil Guard (the common case — most steps carry none) is
// always valid.
func guardStarlarkFree(guard map[string]any) error {
	if guard == nil {
		return nil
	}
	raw, err := json.Marshal(guard)
	if err != nil {
		return fmt.Errorf("guard is not JSON-marshalable: %w", err)
	}
	if _, err := guardgrammar.Parse(raw); err != nil {
		return err
	}
	return nil
}

// loomPatternArtifactDefinition is the single shape both record-time
// validation (validateLoomPatternArtifact, a throwaway unnamed Definition)
// and apply-time materialization (DefinitionForCapabilityArtifact) build from
// a LoomPatternArtifactContent — mirrors lensArtifactDefinition's
// byte-for-byte validated-equals-materialized guarantee.
func loomPatternArtifactDefinition(lp LoomPatternArtifactContent, name, version string) Definition {
	steps := make([]StepSpec, len(lp.Steps))
	for i, s := range lp.Steps {
		steps[i] = StepSpec(s)
	}
	return Definition{
		Name:    name,
		Version: version,
		LoomPatterns: []LoomPatternSpec{{
			PatternID:         lp.PatternID,
			SubjectType:       lp.SubjectType,
			CompletionDomains: lp.CompletionDomains,
			Steps:             steps,
		}},
	}
}

// knownLensFields are the JSON keys LensArtifactContent exposes for the
// "lens" kind. Kept as an explicit set (rather than deriving it via
// reflection) so the allow-list is the obviously-correct source of truth
// unknownLensFields checks raw content against.
var knownLensFields = map[string]bool{
	"canonicalName": true,
	"adapter":       true,
	"bucket":        true,
	"table":         true,
	"spec":          true,
}

// unknownLensFields decodes content as a generic JSON object and returns any
// top-level key outside knownLensFields, sorted for a deterministic report. A
// non-object content (or malformed JSON) returns nil — json.Unmarshal into
// LensArtifactContent already caught that as a caller-contract error before
// this runs.
func unknownLensFields(content json.RawMessage) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil
	}
	var extra []string
	for k := range raw {
		if !knownLensFields[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return extra
}

// knownGrantFields are the JSON keys GrantArtifactContent exposes for the
// "grant" kind. Mirrors knownLensFields' explicit-allow-list rationale.
var knownGrantFields = map[string]bool{
	"operationType": true,
	"scope":         true,
	"grantsTo":      true,
	"note":          true,
}

// unknownGrantFields decodes content as a generic JSON object and returns any
// top-level key outside knownGrantFields, sorted for a deterministic report.
// Mirrors unknownLensFields.
func unknownGrantFields(content json.RawMessage) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil
	}
	var extra []string
	for k := range raw {
		if !knownGrantFields[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return extra
}

// validateGrantArtifact is the "grant" kind's deterministic check (design §3.2,
// §5): a well-formed operationType + scope + non-empty GrantsTo, the shared
// validatePermissionIdentityUniqueness/validateAll pre-flight every package
// (hand- or AI-authored) runs, and — the property that makes this kind safe to
// enable at all — the scope check: the artifact's (operationType, scope) must
// be a subset of what the requesting operator already holds. Without this
// check an operator with only narrow authority could route an AI request that
// mints a package granting arbitrarily broad authority to any role (including
// their own) — the exact privilege-escalation path §5 exists to close. Reused,
// not duplicated: an AI-authored grant can never pass a check a hand-authored
// one would fail.
func validateGrantArtifact(gc GrantArtifactContent, requesterHeld []HeldPermission) ArtifactValidationReport {
	var errs []string

	if gc.OperationType == "" {
		errs = append(errs, "operationType is required")
	}
	if gc.Scope != "any" && gc.Scope != "self" {
		errs = append(errs, fmt.Sprintf("scope must be \"any\" or \"self\", got %q", gc.Scope))
	}
	if len(gc.GrantsTo) == 0 {
		errs = append(errs, "grantsTo must name at least one role")
	}
	seenRoles := make(map[string]bool, len(gc.GrantsTo))
	for _, role := range gc.GrantsTo {
		if strings.TrimSpace(role) == "" {
			errs = append(errs, "grantsTo entries must be non-empty role names")
			continue
		}
		if seenRoles[role] {
			errs = append(errs, fmt.Sprintf("grantsTo names role %q more than once", role))
		}
		seenRoles[role] = true
	}

	wellFormed := gc.OperationType != "" && (gc.Scope == "any" || gc.Scope == "self") && len(gc.GrantsTo) > 0
	if wellFormed {
		def := grantArtifactDefinition(gc, "", "")
		if err := def.validateAll(); err != nil {
			errs = append(errs, err.Error())
		}
		// The scope check runs only once the artifact is otherwise well-formed —
		// an empty/invalid scope has nothing meaningful to compare against the
		// requester's held permissions.
		if !requesterHolds(requesterHeld, gc.OperationType, gc.Scope) {
			errs = append(errs, fmt.Sprintf(
				"requesting operator does not hold %q at scope %q or broader — a grant cannot exceed the requester's own held scope",
				gc.OperationType, gc.Scope))
		}
	}

	return ArtifactValidationReport{Valid: len(errs) == 0, Errors: errs}
}

// grantArtifactDefinition is the single shape both record-time validation
// (validateGrantArtifact, a throwaway unnamed Definition) and apply-time
// materialization (DefinitionForCapabilityArtifact) build from a
// GrantArtifactContent — mirrors lensArtifactDefinition's byte-for-byte
// validated-equals-materialized guarantee.
func grantArtifactDefinition(gc GrantArtifactContent, name, version string) Definition {
	return Definition{
		Name:    name,
		Version: version,
		Permissions: []PermissionSpec{{
			OperationType: gc.OperationType,
			Scope:         gc.Scope,
			GrantsTo:      gc.GrantsTo,
			Note:          gc.Note,
		}},
	}
}

// validateLensArtifact is the "lens" kind's deterministic check: the openCypher
// parser must accept the spec (statically, without executing it), and the
// materialized single-lens Definition must pass the same validateAll the human
// package-authoring path runs (bucket/adapter/read-path posture) — reused, not
// duplicated, so an AI-authored lens can never pass a check a hand-authored one
// would fail.
func validateLensArtifact(lc LensArtifactContent, parser CypherParser) ArtifactValidationReport {
	var errs []string

	if lc.CanonicalName == "" {
		errs = append(errs, "canonicalName is required")
	}
	if lc.Spec == "" {
		errs = append(errs, "spec is required")
	} else if _, err := parser.Parse(lc.Spec); err != nil {
		errs = append(errs, fmt.Sprintf("cypher spec does not parse: %v", err))
	}

	def := lensArtifactDefinition(lc, "", "")
	if err := def.validateAll(); err != nil {
		errs = append(errs, err.Error())
	}

	return ArtifactValidationReport{Valid: len(errs) == 0, Errors: errs}
}

// lensArtifactDefinition is the single shape both record-time validation
// (validateLensArtifact, a throwaway unnamed Definition) and apply-time
// materialization (DefinitionForCapabilityArtifact, a real named/versioned
// Definition — Fire 2, design §3.5) build from a LensArtifactContent — the
// reason an installed lens is guaranteed byte-for-byte identical to what §5
// validated.
func lensArtifactDefinition(lc LensArtifactContent, name, version string) Definition {
	return Definition{
		Name:    name,
		Version: version,
		Lenses: []LensSpec{{
			CanonicalName: lc.CanonicalName,
			Class:         "meta.lens",
			Adapter:       lc.Adapter,
			Bucket:        lc.Bucket,
			Table:         lc.Table,
			Engine:        "full",
			Spec:          lc.Spec,
		}},
	}
}

// DefinitionForCapabilityArtifact builds the pkgmgr.Definition an APPROVED
// proposal's artifact materializes to (design §3.5, the Fire 2 apply step) —
// named/versioned for a real package Install/Upgrade, unlike
// ValidateCapabilityArtifact's throwaway unnamed check. kind must already be
// one of EnabledArtifactKinds: by construction a proposal can only reach
// review.state=approved if RecordCapabilityProposal's §5 gate already
// accepted its kind, so an unrecognized kind here is a caller-contract
// violation (a proposal applied out of order), never a model-authored
// defect.
func DefinitionForCapabilityArtifact(kind string, content json.RawMessage, name, version string) (Definition, error) {
	if !EnabledArtifactKinds[kind] {
		return Definition{}, fmt.Errorf("pkgmgr: capability apply: artifact kind %q is not enabled", kind)
	}
	switch kind {
	case "lens":
		var lc LensArtifactContent
		if err := json.Unmarshal(content, &lc); err != nil {
			return Definition{}, fmt.Errorf("pkgmgr: capability apply: malformed lens artifact content: %w", err)
		}
		return lensArtifactDefinition(lc, name, version), nil
	case "grant":
		var gc GrantArtifactContent
		if err := json.Unmarshal(content, &gc); err != nil {
			return Definition{}, fmt.Errorf("pkgmgr: capability apply: malformed grant artifact content: %w", err)
		}
		return grantArtifactDefinition(gc, name, version), nil
	case "weaverTarget":
		var wc WeaverTargetArtifactContent
		if err := json.Unmarshal(content, &wc); err != nil {
			return Definition{}, fmt.Errorf("pkgmgr: capability apply: malformed weaverTarget artifact content: %w", err)
		}
		return weaverTargetArtifactDefinition(wc, name, version), nil
	case "loomPattern":
		var lp LoomPatternArtifactContent
		if err := json.Unmarshal(content, &lp); err != nil {
			return Definition{}, fmt.Errorf("pkgmgr: capability apply: malformed loomPattern artifact content: %w", err)
		}
		return loomPatternArtifactDefinition(lp, name, version), nil
	case "vertexTypeDDL":
		var vc VertexTypeDDLArtifactContent
		if err := json.Unmarshal(content, &vc); err != nil {
			return Definition{}, fmt.Errorf("pkgmgr: capability apply: malformed vertexTypeDDL artifact content: %w", err)
		}
		return vertexTypeDDLArtifactDefinition(vc, name, version), nil
	case "opMeta":
		var oc OpMetaArtifactContent
		if err := json.Unmarshal(content, &oc); err != nil {
			return Definition{}, fmt.Errorf("pkgmgr: capability apply: malformed opMeta artifact content: %w", err)
		}
		return opMetaArtifactDefinition(oc, name, version), nil
	default:
		// Unreachable: EnabledArtifactKinds gates every case above.
		return Definition{}, fmt.Errorf("pkgmgr: capability apply: unhandled enabled kind %q", kind)
	}
}
