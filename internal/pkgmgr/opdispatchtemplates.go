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
// Dispatch.OptionalReads entry — and any Dispatch.Enumerations hub, which is a
// key template drawn from the same vocabulary — whose placeholders fall outside
// the read-template vocabulary: {actor}, {scopedTo}, {service},
// {payload.<field>}, {me.<type>} — each with an optional trailing `:id`
// modifier. It also holds each declared enumeration to the shape the
// Processor's envelope parse enforces (validateDispatchEnumerations). The set is
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
//     descriptor floor exists to close. An Enumerations hub refuses it for a
//     second reason of its own — a caller-dependent hub makes the op's
//     declared read posture caller-dependent, which is the one thing a
//     declaration exists to make static (validateDispatchEnumerations).
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
		if err := validateReadTemplateList(def.Name, o.OperationType, "Reads", o.Dispatch.Reads, readsClientOnlyRefusal); err != nil {
			return err
		}
		if err := validateReadTemplateList(def.Name, o.OperationType, "OptionalReads", o.Dispatch.OptionalReads, ""); err != nil {
			return err
		}
		if err := validateDispatchEnumerations(def.Name, o.OperationType, o.Dispatch.Enumerations); err != nil {
			return err
		}
	}
	return nil
}

// validateDispatchEnumerations refuses at install a Dispatch.Enumerations entry
// the Processor's envelope parse would refuse: hub and relation non-empty,
// direction exactly "out" or "in" (opwire.ParseEnvelope). Install is the loud
// failure point because the refusal downstream is not a degradation — the
// Processor rejects the WHOLE envelope on a malformed enumeration, terminally,
// so the op does not run with one bad declaration dropped, it never runs at
// all, and every redelivery reproduces the identical dead envelope. The same
// doctrine the loom-step and weaver-gap surfaces already state
// (validateStepEnumerations, validateGapEnumerations).
//
// The hub additionally runs through the same closed placeholder vocabulary
// Dispatch.Reads uses, and refuses the client-only {me.<type>} form: what
// Contract #2 §2.5 buys with a declaration is a STATIC read posture for the op,
// and a {me.<type>} hub resolves only for a caller whose context supplies that
// type, so the same op would declare the walk for some callers and silently
// omit it for others — the walk still runs, now undeclared, for exactly the
// callers the declaration was meant to cover. A server-resolvable hub declares
// the same walk for every caller, or fails here.
func validateDispatchEnumerations(pkgName, opType string, ens []EnumerationSpec) error {
	for i, en := range ens {
		field := fmt.Sprintf("Enumerations[%d].Hub", i)
		if strings.TrimSpace(en.Hub) == "" {
			return fmt.Errorf(
				"pkgmgr: package %q op %q Dispatch.Enumerations[%d] requires a Hub — a walk with no hub vertex names nothing to enumerate from",
				pkgName, opType, i)
		}
		if strings.TrimSpace(en.Relation) == "" {
			return fmt.Errorf(
				"pkgmgr: package %q op %q Dispatch.Enumerations[%d] requires a Relation — a walk with no relation names nothing to enumerate",
				pkgName, opType, i)
		}
		if en.Direction != enumerationDirectionOut && en.Direction != enumerationDirectionIn {
			return fmt.Errorf(
				"pkgmgr: package %q op %q Dispatch.Enumerations[%d] Direction must be %q or %q, got %q",
				pkgName, opType, i, enumerationDirectionOut, enumerationDirectionIn, en.Direction)
		}
		if err := validateReadTemplateList(pkgName, opType, field, []string{en.Hub}, enumerationHubClientOnlyRefusal); err != nil {
			return err
		}
	}
	return nil
}

// The clause a client-only ({me.<type>}) placeholder is refused with, one per
// list that refuses one. The lists refuse it for DIFFERENT reasons and the fix
// differs with the reason, so the sentence travels with the caller rather than
// being written once at the refusal: an author told to move an enumeration
// hub to Dispatch.OptionalReads would be following advice into a field that
// does not exist on that declaration.
const (
	readsClientOnlyRefusal = "which is OptionalReads-only — a required-side {me.<type>} would compile to a required PATTERN whose exclusion blankets every declared root of that type out of demotion; move this read to Dispatch.OptionalReads, or replace it with a server-resolvable placeholder ({actor}/{scopedTo}/{service}/{payload.<field>}) if the op genuinely requires it"

	enumerationHubClientOnlyRefusal = "which an enumeration hub may not carry — {me.<type>} resolves only for a caller whose context supplies that type, so the same op would declare the walk for some callers and omit it for others, leaving the walk running undeclared for exactly the callers the declaration was meant to cover; give the hub a server-resolvable placeholder ({actor}/{scopedTo}/{service}/{payload.<field>}) or a literal key, which declares the same walk for every caller"
)

// validateReadTemplateList classifies every placeholder in every entry of one
// Dispatch template list against the closed vocabulary. clientOnlyRefusal
// carries the one rule that differs between lists: non-empty refuses a
// client-only {me.<type>} placeholder here and states why and what to do
// instead (the Reads list and an Enumerations hub each supply their own), while
// empty admits one — the OptionalReads case, where a client-only placeholder is
// the whole point of the list.
func validateReadTemplateList(pkgName, opType, listName string, entries []string, clientOnlyRefusal string) error {
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

			if clientOnlyRefusal != "" {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q is client-only ({me.<type>}) vocabulary, %s",
					pkgName, opType, listName, entry, placeholder, clientOnlyRefusal)
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
