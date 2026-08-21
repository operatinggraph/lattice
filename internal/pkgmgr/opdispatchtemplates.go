package pkgmgr

import (
	"fmt"
	"regexp"
	"strings"
)

// readTemplatePlaceholderRe matches one `{...}` placeholder token anywhere in
// an OpDispatchSpec.Reads/OptionalReads template entry — the same general
// shape lint-package-standard.go's placeholderRe and the client-side resolver
// (cmd/facet/web/app.js substituteTemplate) use, unanchored (unlike
// capabilitymaterializer_starlark.go's readPlaceholderRe) because a
// mid-segment fragment — `bkr{actor:id}` — is a live, legal read-template
// shape here.
var readTemplatePlaceholderRe = regexp.MustCompile(`\{([^{}]+)\}`)

// ValidateOpDispatchTemplates refuses at install any Dispatch.Reads /
// Dispatch.OptionalReads entry whose placeholders fall outside the read-
// template vocabulary: {actor}, {scopedTo}, {service}, {payload.<field>},
// {me.<type>} — each with an optional trailing `:id` modifier. The set is
// closed by default-deny: an unrecognized placeholder is refused rather than
// silently carrying no floor, the same posture the `# read-posture:`
// annotation takes for Starlark reads (declaring is cheap, forgetting fails
// closed).
//
// Three sharper rules narrow the vocabulary further
// (descriptor-floor-template-coverage-design.md §3.3), each tied to a
// concrete Processor-side consequence:
//
//   - {entity.<column>} and a trailing `?` OPTIONAL marker are
//     ContextParams-only vocabulary. In a read template the client's
//     wholeKey silently drops either, so a declared entry using one is
//     already dead weight — refused so the author hears about it instead of
//     shipping a key that never resolves as intended.
//   - {me.<type>} is client-only — the Processor has no server-side value to
//     resolve it from — and is OptionalReads-only. A required-side
//     {me.<type>} would force the Processor to compile a required PATTERN
//     whose exclusion from demotion blankets every declared root of that
//     type out of demotion, quietly preserving the very existence oracle the
//     descriptor floor exists to close.
//   - a {me.<type>} placeholder must occupy a whole dot-delimited segment of
//     the template — the character before `{` must be start-of-string or
//     `.`, and the character after `}` must be end-of-string or `.`. A
//     fragment wildcard (`bkr{me.instructor:id}`) is inexpressible in the
//     Processor's whole-segment matcher, whose fallback for it is no floor
//     at all. This constrains only client-only placeholders — a
//     server-resolvable mid-segment fragment (`bkr{actor:id}`,
//     `{payload.session:id}.bkr…`) resolves concretely and stays legal.
//
// It also refuses two template well-formedness defects that a vocabulary
// check alone would miss:
//
//   - an unbalanced brace — a `{` with no matching `}` (or vice versa), or a
//     `}` with no opener. An unterminated `{payload.entityKey` contains no
//     `{...}` match at all, so a vocabulary check that only classifies
//     matched placeholders would let it sail through as though it were
//     literal text.
//   - an empty dot-delimited segment — a leading `.`, a trailing `.`, or a
//     doubled `..`, INCLUDING inside a placeholder body (`{payload.a..b}`).
//     A hole is not a shorter key, it is a different and invalid one — the
//     client-side resolver (cmd/facet/web/app.js's wholeKey) drops such an
//     entry for the same reason, so a descriptor declaring one never
//     resolves as the author intended.
//
// What it does NOT guarantee: it is a placeholder-vocabulary and
// template-well-formedness gate, not a full Contract #1 key-grammar
// validator. It does not refuse a syntactically well-formed template that
// would still compile to an invalid or too-short key (`vtx.<type>` alone),
// and it does not refuse a wildcard placeholder occupying a localName
// position (`vtx.<type>.{payload.x}.{me.y:id}`) — that shape's coverage is
// a named residual on the Processor side, not an authoring rule this gate
// enforces.
//
// EXPORTED as a deliberate deviation from this package's other validate*
// helpers, which are unexported and reached only through validateAll:
// internal/pkgregistry's corpus census (registry_test.go) must run this
// exact rule over every shipped package as the install-time "gate breaks
// nothing" proof, and pkgregistry imports pkgmgr — an unexported method
// would be unreachable from there, and the census would have to
// re-implement the rule rather than exercise it, which is the
// guard-diverges-from-oracle defect this component's dossier has already
// minted five times. Definition.ExpandReadGrantWalks
// (internal/pkgregistry/registry_test.go:70) is the existing precedent for
// an exported-method driven by the corpus test.
func (def Definition) ValidateOpDispatchTemplates() error {
	for _, o := range def.OpMetas {
		if o.Dispatch == nil {
			continue
		}
		if err := validateReadTemplateList(def.Name, o.OperationType, "Reads", o.Dispatch.Reads, true); err != nil {
			return err
		}
		if err := validateReadTemplateList(def.Name, o.OperationType, "OptionalReads", o.Dispatch.OptionalReads, false); err != nil {
			return err
		}
	}
	return nil
}

// validateReadTemplateList classifies every placeholder in every entry of one
// Dispatch read list against the closed vocabulary. isRequired marks the
// Reads list (as opposed to OptionalReads), the only distinction that changes
// which placeholders are legal.
func validateReadTemplateList(pkgName, opType, listName string, entries []string, isRequired bool) error {
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		if err := validateBraceBalance(pkgName, opType, listName, entry); err != nil {
			return err
		}
		if err := validateNoEmptySegments(pkgName, opType, listName, entry); err != nil {
			return err
		}
		for _, loc := range readTemplatePlaceholderRe.FindAllStringSubmatchIndex(entry, -1) {
			start, end := loc[0], loc[1]
			body := entry[loc[2]:loc[3]]
			placeholder := entry[start:end]

			hasOptionalMarker := strings.HasSuffix(body, "?")
			base := strings.TrimSuffix(body, "?")
			base = strings.TrimSuffix(base, ":id")

			isClientOnly := false
			switch {
			case base == "actor", base == "scopedTo", base == "service":
				// resolvable root — legal.
			case strings.HasPrefix(base, "payload.") && base != "payload.":
				// resolvable root — legal.
			case strings.HasPrefix(base, "me.") && base != "me.":
				isClientOnly = true
			case strings.HasPrefix(base, "entity."):
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q uses {entity.<column>}, which is ContextParams-only vocabulary — a read template's client wholeKey silently drops it, so declare it under Dispatch.ContextParams instead, or drop it if the read does not need it",
					pkgName, opType, listName, entry, placeholder)
			default:
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q is outside the closed read-template vocabulary ({actor}, {scopedTo}, {service}, {payload.<field>}, {me.<type>}, each optionally suffixed :id) — use one of those placeholders, or extend the vocabulary deliberately together with its Processor-side floor semantics",
					pkgName, opType, listName, entry, placeholder)
			}

			if hasOptionalMarker {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q carries a `?` OPTIONAL marker, which is ContextParams-only vocabulary — a read template's client wholeKey silently drops a ?-marked entry, so file this key under Dispatch.OptionalReads unmarked instead of marking it optional inline",
					pkgName, opType, listName, entry, placeholder)
			}

			if !isClientOnly {
				continue
			}

			if isRequired {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.Reads entry %q: placeholder %q is client-only ({me.<type>}) vocabulary, which is OptionalReads-only — a required-side {me.<type>} would compile to a required PATTERN whose exclusion blankets every declared root of that type out of demotion; move this read to Dispatch.OptionalReads, or replace it with a server-resolvable placeholder ({actor}/{scopedTo}/{service}/{payload.<field>}) if the op genuinely requires it",
					pkgName, opType, entry, placeholder)
			}

			before := byte(0)
			if start > 0 {
				before = entry[start-1]
			}
			after := byte(0)
			if end < len(entry) {
				after = entry[end]
			}
			wholeSegment := (start == 0 || before == '.') && (end == len(entry) || after == '.')
			if !wholeSegment {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: client-only placeholder %q does not occupy a whole dot-delimited segment of the template — the Processor's matcher only wildcards whole segments, and its fallback for a fragment wildcard is no floor at all; give the placeholder its own segment, or replace it with a server-resolvable placeholder",
					pkgName, opType, listName, entry, placeholder)
			}
		}
	}
	return nil
}

// validateBraceBalance refuses a template entry carrying an unbalanced
// brace: a `{` with no matching `}`, or a `}` with no opener. Placeholders
// in this vocabulary never nest, so a second `{` before the first one
// closes is refused the same way as one that never closes at all. An
// unbalanced brace is never a legal literal — it means an author dropped a
// character mid-placeholder — and it is invisible to a vocabulary check
// that only classifies matched `{...}` groups: an unterminated
// `{payload.entityKey` contains no match, so it would otherwise sail
// through as though it were literal text.
func validateBraceBalance(pkgName, opType, listName, entry string) error {
	open := false
	for _, r := range entry {
		switch r {
		case '{':
			if open {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: a second '{' opens before the first one closes — a placeholder never nests; close it or remove the extra '{'",
					pkgName, opType, listName, entry)
			}
			open = true
		case '}':
			if !open {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: a '}' appears with no matching '{' — remove the stray '}' or open a placeholder before it",
					pkgName, opType, listName, entry)
			}
			open = false
		}
	}
	if open {
		return fmt.Errorf(
			"pkgmgr: package %q op %q Dispatch.%s entry %q: an unterminated '{' never closes — every placeholder must end with '}'",
			pkgName, opType, listName, entry)
	}
	return nil
}

// validateNoEmptySegments refuses a template entry that splits into an
// empty dot-delimited segment — a leading `.`, a trailing `.`, or a doubled
// `..`, wherever it occurs, including inside a placeholder body
// (`{payload.a..b}`). A hole is not a shorter key, it is a different and
// invalid one — the client-side resolver (cmd/facet/web/app.js's wholeKey)
// drops such an entry for the same reason, so a descriptor declaring one
// never resolves as the author intended.
//
// The split runs over the RAW entry, unmasked: no legal placeholder body
// puts a bare `.` where splitting it would produce a false empty segment —
// `{payload.<field>}` splits to `{payload` and `<field>}`, both non-empty,
// same as `{me.<type>}` splits to `{me` and `<type>}` — so masking buys
// nothing a legal template needs, and splitting raw is strictly stronger:
// it also catches a malformed placeholder body such as `{payload.a..b}`
// that a masked split would hide entirely (the whole `{...}` collapses to
// one opaque token before the split ever sees it).
func validateNoEmptySegments(pkgName, opType, listName, entry string) error {
	for _, seg := range strings.Split(entry, ".") {
		if seg == "" {
			return fmt.Errorf(
				"pkgmgr: package %q op %q Dispatch.%s entry %q: contains an empty dot-delimited segment (a leading '.', a trailing '.', or '..') — a hole is not a shorter key, it is a different one, and the client wholeKey drops such an entry rather than resolving it",
				pkgName, opType, listName, entry)
		}
	}
	return nil
}
