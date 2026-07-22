package pkgmgr

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	starlarkjson "go.starlark.net/lib/json"
	starlarklib "go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
)

// SensitiveAspectResolver lets the §5 validator resolve whether a declared
// read surface (today: an opMeta's Dispatch.Reads entry) names a
// sensitive-classed aspect, per the live installed DDL catalog
// (sensitive-ref-mac-provenance-design.md §7 condition 2). Injected — like
// CypherParser and HeldPermission — because ValidateCapabilityArtifact never
// touches a live substrate itself. A caller backed by the Processor's
// internal/processor.DDLCache implements this by calling Lookup(aspectLocalName)
// and returning ref.Sensitive (an aspectType DDL's CanonicalName IS the bare
// aspect local name, e.g. "ssn"/"dob" — packages/identity-domain/ddls.go).
type SensitiveAspectResolver interface {
	// IsSensitiveAspect reports whether aspectLocalName is declared
	// sensitive:true by its owning aspect-type DDL. An unknown aspect name
	// returns false (fail-closed is the CALLER's responsibility — see
	// sensitiveReadAspect's nil-resolver handling below, which rejects
	// rather than calling this with a resolver it doesn't have).
	IsSensitiveAspect(aspectLocalName string) bool
}

// sensitiveRefLiteral is the marker string a Processor-minted sensitive ref
// carries (vault.RefMACPurpose family, internal/vault/refmac.go). No
// legitimate artifact content ever spells it — refs are Processor-authored,
// never AI-authored.
const sensitiveRefLiteral = "$sensitiveRef"

// containsSensitiveRefLiteral is the Fire-4 condition-2 rule 1 check
// (sensitive-ref-mac-provenance-design.md §7): the literal string
// "$sensitiveRef" appearing ANYWHERE in an artifact's raw content — script
// source, pattern params, target templates — regardless of kind. Refs are
// Processor-authored; no legitimate artifact ever spells the marker, so a
// raw substring scan (no JSON-shape awareness needed) is both sufficient and
// robust to smuggling the literal inside any nested field.
//
// This is a literal-text scan, not a semantic one: a vertexTypeDDL script
// that builds the string at runtime via concatenation (e.g. `"$sensitive" +
// "Ref"`) never contains the contiguous literal here and is NOT caught by
// this check. That is a deliberate, documented boundary, not an oversight —
// §7's own closing note states the lint stays "advisory pre-flight for what
// it can't see" precisely BECAUSE a computed value can defeat any static
// text scan; the MAC (internal/vault.RefMACPurpose, verified by the bridge's
// decryptref RPC — sensitive-ref-mac-provenance-design.md) is what actually
// rejects a forged/fabricated ref downstream, regardless of how the marker
// text was assembled. Do not "harden" this into a smarter parser — the
// design explicitly warns against mistaking this pre-flight lint for the
// real enforcement.
func containsSensitiveRefLiteral(content json.RawMessage) bool {
	return strings.Contains(string(content), sensitiveRefLiteral)
}

// readPlaceholderRe matches OpDispatchSpec.Reads' one recognized placeholder
// token at the START of an entry (definition.go's documented vocabulary:
// templates substituted from {actor}/{scopedTo}/{service}/{payload.<field>})
// — a bare {actor}/{scopedTo}/{service}, or a bracketed {payload.<field>}
// reference (itself containing a dot, so it must be matched as ONE token,
// not naively split on "."). Capture group 2 is whatever trails the
// placeholder in the entry.
var readPlaceholderRe = regexp.MustCompile(`^(\{actor\}|\{scopedTo\}|\{service\}|\{payload\.[^{}]+\})(.*)$`)

// sensitiveReadAspect classifies one Dispatch.Reads/Dispatch.OptionalReads
// template entry against OpDispatchSpec's documented vocabulary.
//
// It deliberately recognizes only the ANCHORED subset of that vocabulary — a
// placeholder at the START of the entry. A human-authored package may also
// write a key FRAGMENT form (a placeholder carrying `:id`, appearing mid-entry
// to build a 6-segment link key); those are reported unrecognized here and so
// fail an AI-authored opMeta closed. That asymmetry is the intended posture,
// not an oversight: a mid-entry placeholder means the entry's leading segments
// are literal text, and this classifier's whole basis for calling a read safe
// is knowing which segment is the aspect. Widening the shape it accepts would
// widen what it must claim to have checked. An AI capability that genuinely
// needs a link read routes to human authoring, exactly like a sensitive one.
//
// recognized is false for anything
// that does not match that vocabulary at all — a bare aspect name with no
// placeholder ("ssn"), a fully-qualified vtx.* key (that is the DIFFERENT
// ContextHint.Reads wire format, not this field's documented shape), a
// doubled separator ("{actor}..ssn"), or an empty string — and the caller
// (validateOpMetaArtifact) fails an unrecognized entry CLOSED rather than
// silently letting an out-of-vocabulary shape slide through unchecked.
//
// aspect is empty with recognized=true for a bare placeholder ("{actor}") or
// a bare payload-field reference ("{payload.targetActor}") — both read only
// the vertex root / the AI's own proposed payload field, which per D5 never
// carries Vault-classified aspect data, so neither is flagged. A placeholder
// followed by ".<aspect>" (optionally with a further ".<field>" tail, e.g.
// "{actor}.ssn.value" or "{payload.targetActor}.ssn" — the latter naming
// another vertex's aspect via a payload-supplied reference, not just the
// caller's own) extracts "<aspect>" as the read-declaration surface the
// condition-2 rule-2 sensitivity check applies to.
func sensitiveReadAspect(entry string) (aspect string, recognized bool) {
	m := readPlaceholderRe.FindStringSubmatch(entry)
	if m == nil {
		return "", false
	}
	rest := m[2]
	if rest == "" {
		return "", true
	}
	rest, ok := strings.CutPrefix(rest, ".")
	if !ok {
		// Trailing content that isn't a "." continuation (e.g. a stray
		// character glued onto the placeholder) — not a shape this
		// vocabulary defines.
		return "", false
	}
	aspect, _, _ = strings.Cut(rest, ".")
	if aspect == "" {
		// A doubled separator ("{actor}..ssn") or a bare trailing dot
		// ("{actor}.") — malformed, not a real aspect reference.
		return "", false
	}
	return aspect, true
}

// ---- vertexTypeDDL kind ----

// VertexTypeDDLArtifactContent is the JSON shape of a "vertexTypeDDL"-kind
// proposal's artifact.content (design §3.2/§8 Fire 4) — the constrained
// subset of pkgmgr.DDLSpec an AI-authored DDL proposal may carry: a
// vertexType DDL only (Class is fixed "meta.ddl.vertexType", not exposed as
// a field — mirrors the lens kind hardcoding Class="meta.lens"). The
// aspectType Class + its Sensitive flag are deliberately NOT exposed: an
// aspect-type DDL is where the platform's PII-classification boundary lives
// (lattice-architecture Item 6, NFR-S3), and Sensitive is meaningful only
// for that class — out of scope for this increment, same posture as the
// lens kind excluding protected/secure postures and weaverTarget excluding
// the augur block. Effects (the §10.8 Weaver-planner extension) is also
// excluded: every Effects entry requires a matching OpMetaSpec in the SAME
// Definition (validateEffects), but a vertexTypeDDL artifact never carries
// op-metas of its own (those are the separate "opMeta" kind proposed
// independently) — an Effects entry could never validate here regardless.
type VertexTypeDDLArtifactContent struct {
	CanonicalName     string            `json:"canonicalName"`
	PermittedCommands []string          `json:"permittedCommands"`
	Description       string            `json:"description"`
	Script            string            `json:"script"`
	InputSchema       string            `json:"inputSchema,omitempty"`
	OutputSchema      string            `json:"outputSchema,omitempty"`
	FieldDescription  map[string]string `json:"fieldDescription,omitempty"`
	Examples          []ExampleArtifact `json:"examples,omitempty"`
}

// ExampleArtifact mirrors pkgmgr.ExampleSpec field-for-field — one named
// usage example for a vertexTypeDDL artifact's operation.
type ExampleArtifact struct {
	Name            string         `json:"name"`
	Payload         map[string]any `json:"payload,omitempty"`
	ExpectedOutcome string         `json:"expectedOutcome"`
}

// knownVertexTypeDDLFields are the JSON keys VertexTypeDDLArtifactContent
// exposes for the "vertexTypeDDL" kind. Mirrors knownLensFields' explicit
// allow-list rationale — notably excludes "class"/"sensitive"/"effects".
var knownVertexTypeDDLFields = map[string]bool{
	"canonicalName":     true,
	"permittedCommands": true,
	"description":       true,
	"script":            true,
	"inputSchema":       true,
	"outputSchema":      true,
	"fieldDescription":  true,
	"examples":          true,
}

// knownExampleFields are the JSON keys ExampleArtifact exposes for one entry
// in a vertexTypeDDL's `examples` array.
var knownExampleFields = map[string]bool{
	"name":            true,
	"payload":         true,
	"expectedOutcome": true,
}

// unknownVertexTypeDDLFields decodes content as a generic JSON object and
// returns any key — at the top level (outside knownVertexTypeDDLFields) OR
// inside an `examples[]` entry (outside knownExampleFields) — the AI
// vertexTypeDDL path does not expose, sorted for a deterministic report.
// Mirrors unknownWeaverTargetFields' nested-scan rationale: a smuggled
// "class"/"sensitive"/"effects" key would otherwise be silently dropped by
// json.Unmarshal rather than rejected.
func unknownVertexTypeDDLFields(content json.RawMessage) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil
	}
	var extra []string
	for k := range raw {
		if !knownVertexTypeDDLFields[k] {
			extra = append(extra, k)
		}
	}
	if examplesRaw, ok := raw["examples"]; ok {
		var examples []json.RawMessage
		if err := json.Unmarshal(examplesRaw, &examples); err == nil {
			for i, exRaw := range examples {
				var ex map[string]json.RawMessage
				if err := json.Unmarshal(exRaw, &ex); err != nil {
					continue
				}
				for k := range ex {
					if !knownExampleFields[k] {
						extra = append(extra, fmt.Sprintf("examples[%d].%s", i, k))
					}
				}
			}
		}
	}
	sort.Strings(extra)
	return extra
}

// ddlScriptSandboxWallBudget/MaxSteps bound the record-time Starlark
// dry-run (starlarksandbox.Validate) of a vertexTypeDDL artifact's Script.
// Validate only compiles the source and runs its top-level statements (a
// script's own def bodies are never invoked) — generous enough for the
// def-only top level every real DDL script has, tight enough that a
// pathological top-level statement (an infinite loop outside any def) fails
// fast rather than hanging record-time validation. Mirrors Loom's
// parseStarlarkGuard budget shape (internal/loom/guard_starlark.go).
const (
	ddlScriptSandboxWallBudget = 100 * time.Millisecond
	ddlScriptSandboxMaxSteps   = int64(200_000)
)

// ddlScriptSandboxGlobals is the predeclared-name set a vertexTypeDDL
// artifact's Script is resolved against at record time — the union of every
// global a real DDL script may reference in production
// (internal/processor/starlark_runner.go's Run: state/op/ddl/nanoid/crypto/
// time/json/kv), so a legitimate script's name references resolve
// identically here and at dispatch time (a script that record-time-validates
// clean never later hits an "undefined:" SandboxViolation it couldn't have
// been warned about). Values are inert placeholders, NOT the Processor's
// live modules: Validate never calls the script's entrypoint (only Init —
// top-level statements, normally just `def execute(...): ...`), so what a
// name is BOUND TO cannot matter for a well-formed script; giving every
// impure name (kv, nanoid, op, state, ddl) an empty/no-op stand-in keeps this
// dry-run genuinely pure regardless. crypto/time/json are the sandbox's real
// pure builtins (safe to actually be functional even at Init time).
func ddlScriptSandboxGlobals() starlarklib.StringDict {
	empty := starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{})
	return starlarklib.StringDict{
		"state":  new(starlarklib.Dict),
		"op":     empty,
		"ddl":    new(starlarklib.Dict),
		"nanoid": empty,
		"crypto": starlarkstruct.FromStringDict(starlarkstruct.Default, starlarksandbox.CryptoBuiltins()),
		"time":   starlarkstruct.FromStringDict(starlarkstruct.Default, starlarksandbox.TimeBuiltins()),
		"json":   starlarkjson.Module,
		"kv":     empty,
	}
}

// validateVertexTypeDDLArtifact is the "vertexTypeDDL" kind's deterministic
// check (design §3.2/§8 Fire 4): required fields, the verified-pure Starlark
// sandbox dry-run of Script (compiles + defines a 2-parameter `execute`
// entrypoint — starlarksandbox.Validate, this package's first caller at
// package-install/record time per the design's "Piece 1 builds WITH this
// fire"), and the same validateAll the human package-authoring path runs
// (validateOpMetas/validateEffects trivially pass — a throwaway
// single-DDL Definition carries no OpMetas/Effects of its own — plus
// validateCanonicalNameUniqueness) — reused, not duplicated.
func validateVertexTypeDDLArtifact(vc VertexTypeDDLArtifactContent) ArtifactValidationReport {
	var errs []string

	if strings.TrimSpace(vc.CanonicalName) == "" {
		errs = append(errs, "canonicalName is required")
	}
	if len(vc.PermittedCommands) == 0 {
		errs = append(errs, "permittedCommands must name at least one operationType")
	}
	seenCommands := make(map[string]bool, len(vc.PermittedCommands))
	for i, c := range vc.PermittedCommands {
		if !singleTokenPattern.MatchString(c) {
			errs = append(errs, fmt.Sprintf("permittedCommands[%d]: %q is not a valid single token (must match %s)", i, c, singleTokenPattern.String()))
			continue
		}
		if seenCommands[c] {
			errs = append(errs, fmt.Sprintf("permittedCommands[%d]: duplicate operationType %q", i, c))
			continue
		}
		seenCommands[c] = true
	}
	if strings.TrimSpace(vc.Script) == "" {
		errs = append(errs, "script is required")
	} else if sErr := starlarksandbox.Validate(vc.Script, "execute", 2, ddlScriptSandboxGlobals(),
		starlarksandbox.Budget{Wall: ddlScriptSandboxWallBudget, MaxSteps: ddlScriptSandboxMaxSteps}); sErr != nil {
		errs = append(errs, fmt.Sprintf("script: %s: %s", sErr.Code, sErr.Message))
	}

	def := vertexTypeDDLArtifactDefinition(vc, "", "")
	if err := def.validateAll(); err != nil {
		errs = append(errs, err.Error())
	}

	return ArtifactValidationReport{Valid: len(errs) == 0, Errors: errs}
}

// vertexTypeDDLArtifactDefinition is the single shape both record-time
// validation (validateVertexTypeDDLArtifact, a throwaway unnamed Definition)
// and apply-time materialization (DefinitionForCapabilityArtifact) build
// from a VertexTypeDDLArtifactContent — mirrors lensArtifactDefinition's
// byte-for-byte validated-equals-materialized guarantee. Class is hardcoded
// "meta.ddl.vertexType" (never exposed as a field — see the type doc);
// Sensitive/Effects are always their zero value.
func vertexTypeDDLArtifactDefinition(vc VertexTypeDDLArtifactContent, name, version string) Definition {
	examples := make([]ExampleSpec, len(vc.Examples))
	for i, e := range vc.Examples {
		examples[i] = ExampleSpec(e)
	}
	return Definition{
		Name:    name,
		Version: version,
		DDLs: []DDLSpec{{
			CanonicalName:     vc.CanonicalName,
			Class:             "meta.ddl.vertexType",
			PermittedCommands: vc.PermittedCommands,
			Description:       vc.Description,
			Script:            vc.Script,
			InputSchema:       vc.InputSchema,
			OutputSchema:      vc.OutputSchema,
			FieldDescription:  vc.FieldDescription,
			Examples:          examples,
		}},
	}
}

// ---- opMeta kind ----

// OpMetaArtifactContent is the JSON shape of an "opMeta"-kind proposal's
// artifact.content (design §3.2/§8 Fire 4) — the constrained subset of
// pkgmgr.OpMetaSpec an AI-authored op-meta proposal may carry. Sensitive is
// deliberately NOT exposed: it marks the op's OWN payload as needing
// sensitive-param-egress guarding, a privacy-relevant posture an AI should
// never self-declare (same narrowing rationale as vertexTypeDDL excluding
// the aspect-type Sensitive flag). Dispatch.Reads and Dispatch.OptionalReads —
// the fields this kind exposes that can name a sensitive aspect — are the
// condition-2 rule-2 surfaces validateOpMetaArtifact checks against the
// injected SensitiveAspectResolver.
type OpMetaArtifactContent struct {
	OperationType     string                  `json:"operationType"`
	Presentation      *OpPresentationArtifact `json:"presentation,omitempty"`
	InputSchema       string                  `json:"inputSchema,omitempty"`
	FieldDescriptions map[string]string       `json:"fieldDescriptions,omitempty"`
	Dispatch          *OpDispatchArtifact     `json:"dispatch,omitempty"`
}

// OpPresentationArtifact mirrors pkgmgr.OpPresentationSpec field-for-field.
type OpPresentationArtifact struct {
	Title       string `json:"title,omitempty"`
	ShortLabel  string `json:"shortLabel,omitempty"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Tone        string `json:"tone,omitempty"`
	SubmitLabel string `json:"submitLabel,omitempty"`
	Group       string `json:"group,omitempty"`
}

// OpDispatchArtifact mirrors pkgmgr.OpDispatchSpec field-for-field.
type OpDispatchArtifact struct {
	Class         string            `json:"class,omitempty"`
	AuthContext   string            `json:"authContext,omitempty"`
	TargetField   string            `json:"targetField,omitempty"`
	TargetType    string            `json:"targetType,omitempty"`
	ContextParams map[string]string `json:"contextParams,omitempty"`
	Reads         []string          `json:"reads,omitempty"`
	OptionalReads []string          `json:"optionalReads,omitempty"`
}

// knownOpMetaFields are the JSON keys OpMetaArtifactContent exposes for the
// "opMeta" kind. Mirrors knownLensFields' explicit allow-list rationale —
// notably excludes "sensitive".
var knownOpMetaFields = map[string]bool{
	"operationType":     true,
	"presentation":      true,
	"inputSchema":       true,
	"fieldDescriptions": true,
	"dispatch":          true,
}

var knownPresentationFields = map[string]bool{
	"title": true, "shortLabel": true, "description": true,
	"icon": true, "tone": true, "submitLabel": true, "group": true,
}

var knownDispatchFields = map[string]bool{
	"class": true, "authContext": true, "targetField": true,
	"targetType": true, "contextParams": true, "reads": true,
	"optionalReads": true,
}

// unknownOpMetaFields decodes content as a generic JSON object and returns
// any key — at the top level, inside `presentation`, or inside `dispatch` —
// the AI opMeta path does not expose, sorted for a deterministic report.
// Mirrors unknownWeaverTargetFields' nested-scan rationale: a smuggled
// "sensitive" key (top-level or nested) would otherwise be silently dropped
// by json.Unmarshal rather than rejected.
func unknownOpMetaFields(content json.RawMessage) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil
	}
	var extra []string
	for k := range raw {
		if !knownOpMetaFields[k] {
			extra = append(extra, k)
		}
	}
	scanNested := func(field string, known map[string]bool) {
		nestedRaw, ok := raw[field]
		if !ok {
			return
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(nestedRaw, &nested); err != nil {
			return
		}
		for k := range nested {
			if !known[k] {
				extra = append(extra, field+"."+k)
			}
		}
	}
	scanNested("presentation", knownPresentationFields)
	scanNested("dispatch", knownDispatchFields)
	sort.Strings(extra)
	return extra
}

// validateOpMetaArtifact is the "opMeta" kind's deterministic check (design
// §3.2/§8 Fire 4): a required operationType, the same validateAll the human
// package-authoring path runs (validateOpMetas — token shape + package-local
// uniqueness), and the condition-2 rule-2 check (sensitive-ref-mac-
// provenance-design.md §7): every Dispatch.Reads entry that names an aspect
// (sensitiveReadAspect) must resolve to a NON-sensitive aspect. A nil
// resolver fails CLOSED on any aspect-naming read — the caller has not (yet)
// wired a live catalog to verify against, and an AI-authored capability that
// might need PII egress is exactly the case that must route to human
// authoring rather than pass unverified.
func validateOpMetaArtifact(oc OpMetaArtifactContent, sensitive SensitiveAspectResolver) ArtifactValidationReport {
	var errs []string

	if oc.OperationType == "" {
		errs = append(errs, "operationType is required")
	}

	def := opMetaArtifactDefinition(oc, "", "")
	if err := def.validateAll(); err != nil {
		errs = append(errs, err.Error())
	}

	if oc.Dispatch != nil {
		errs = append(errs, sensitiveReadErrors("dispatch.reads", oc.Dispatch.Reads, sensitive)...)
		errs = append(errs, sensitiveReadErrors("dispatch.optionalReads", oc.Dispatch.OptionalReads, sensitive)...)
	}

	return ArtifactValidationReport{Valid: len(errs) == 0, Errors: errs}
}

// sensitiveReadErrors runs the condition-2 rule-2 check over one declared-read
// field, naming that field in every message. Both of an opMeta's read surfaces
// are equally capable of naming a sensitive aspect, so both run it: gating
// only the required half would leave `{actor}.ssn` declarable as an OPTIONAL
// read, which reaches the same PII exactly as cheaply.
func sensitiveReadErrors(field string, entries []string, sensitive SensitiveAspectResolver) []string {
	var errs []string
	for _, r := range entries {
		aspect, recognized := sensitiveReadAspect(r)
		if !recognized {
			// Fails closed rather than silently passing: an entry outside
			// OpDispatchSpec's documented template vocabulary could be a bare
			// aspect name with no placeholder prefix at all (e.g. "ssn"),
			// which this check has no basis to call safe.
			errs = append(errs, fmt.Sprintf(
				"%s entry %q does not match a recognized template shape ({actor}|{scopedTo}|{service}, optionally with a .<aspect> suffix, or {payload.<field>}) — an AI-authored opMeta may not declare an unrecognized read",
				field, r))
			continue
		}
		if aspect == "" {
			// A bare placeholder or bare payload-field reference — reads only
			// the vertex root / the AI's own proposed payload field, never
			// Vault-classified aspect data.
			continue
		}
		if sensitive == nil {
			errs = append(errs, fmt.Sprintf(
				"%s entry %q names aspect %q — no live sensitivity catalog was supplied to verify it is safe; an AI-authored opMeta may not declare an unverified aspect read",
				field, r, aspect))
			continue
		}
		if sensitive.IsSensitiveAspect(aspect) {
			errs = append(errs, fmt.Sprintf(
				"%s entry %q resolves to sensitive-classed aspect %q — an AI-authored opMeta may not declare a sensitive-key read (route to human authoring)",
				field, r, aspect))
		}
	}
	return errs
}

// opMetaArtifactDefinition is the single shape both record-time validation
// (validateOpMetaArtifact, a throwaway unnamed Definition) and apply-time
// materialization (DefinitionForCapabilityArtifact) build from an
// OpMetaArtifactContent — mirrors lensArtifactDefinition's byte-for-byte
// validated-equals-materialized guarantee. Sensitive is always false (see
// the type doc).
func opMetaArtifactDefinition(oc OpMetaArtifactContent, name, version string) Definition {
	var presentation *OpPresentationSpec
	if oc.Presentation != nil {
		presentation = &OpPresentationSpec{
			Title:       oc.Presentation.Title,
			ShortLabel:  oc.Presentation.ShortLabel,
			Description: oc.Presentation.Description,
			Icon:        oc.Presentation.Icon,
			Tone:        oc.Presentation.Tone,
			SubmitLabel: oc.Presentation.SubmitLabel,
			Group:       oc.Presentation.Group,
		}
	}
	var dispatch *OpDispatchSpec
	if oc.Dispatch != nil {
		dispatch = &OpDispatchSpec{
			Class:         oc.Dispatch.Class,
			AuthContext:   oc.Dispatch.AuthContext,
			TargetField:   oc.Dispatch.TargetField,
			TargetType:    oc.Dispatch.TargetType,
			ContextParams: oc.Dispatch.ContextParams,
			Reads:         oc.Dispatch.Reads,
			OptionalReads: oc.Dispatch.OptionalReads,
		}
	}
	return Definition{
		Name:    name,
		Version: version,
		OpMetas: []OpMetaSpec{{
			OperationType:     oc.OperationType,
			Presentation:      presentation,
			InputSchema:       oc.InputSchema,
			FieldDescriptions: oc.FieldDescriptions,
			Dispatch:          dispatch,
		}},
	}
}
