package pkgmgr

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/operatinggraph/lattice/internal/processor"
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
// Dispatch.OptionalReads entry whose placeholders fall outside the
// read-template vocabulary: {actor}, {scopedTo}, {service}, {payload.<field>},
// {me.<type>} — each with an optional trailing `:id` modifier. It also holds
// each declared enumeration to the shape the Processor's envelope parse
// enforces, and its hub to a NARROWER vocabulary of its own
// (validateDispatchEnumerations, enumerationHubRules). The set is closed by
// default-deny: an unrecognized placeholder is refused rather than silently
// carrying no floor, the same posture the `# read-posture:` annotation takes
// for Starlark reads (declaring is cheap, forgetting fails closed).
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
//     declaration exists to make static (enumerationHubRules).
//   - a {me.<type>} placeholder must occupy a whole dot-delimited segment of
//     the template — the character before `{` must be start-of-string or
//     `.`, and the character after `}` must be end-of-string or `.`. A
//     fragment wildcard (`bkr{me.instructor:id}`) is inexpressible in the
//     Processor's whole-segment matcher, whose fallback for it is no floor
//     at all. In a read list this constrains only client-only placeholders —
//     a server-resolvable mid-segment fragment (`bkr{actor:id}`,
//     `{payload.session:id}.bkr…`) resolves concretely and stays legal there.
//     An enumeration hub holds EVERY placeholder to a whole segment, on its
//     own grounds: a hub is one whole vertex key rather than a fragment
//     assembled into one (enumerationHubRules).
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
		if err := validateReadTemplateList(def.Name, o.OperationType, "Reads", o.Dispatch.Reads, readsRules); err != nil {
			return err
		}
		if err := validateReadTemplateList(def.Name, o.OperationType, "OptionalReads", o.Dispatch.OptionalReads, optionalReadsRules); err != nil {
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
// The hub runs through a NARROWER placeholder vocabulary than Dispatch.Reads —
// {actor}, {payload.<field>}, or a literal key, each placeholder occupying a
// whole dot-delimited segment and carrying no `:id` modifier
// (enumerationHubRules).
//
// An NFR-S6 operation may declare no enumeration at all, whatever its shape:
// the Processor closes those operations' declared read set and refuses every
// contextHint enumeration outright (refuseUndeclaredContextHint,
// internal/processor/descriptor_floor.go), so a walk declared here would ship
// onto the `.dispatch` aspect, be substituted onto every envelope a
// descriptor-driven client submits, and fault the operation on arrival —
// terminally, identically on every redelivery, and collapsed to the generic
// ClaimKeyInvalid with nil details, which leaves the outage with no
// attributable cause on any channel the caller can see. The operation set is
// the Processor's own predicate rather than a copy of it: a second copy of a
// security-relevant membership list is a copy that drifts.
func validateDispatchEnumerations(pkgName, opType string, ens []EnumerationSpec) error {
	if len(ens) > 0 && processor.IsNFRS6Operation(opType) {
		return fmt.Errorf(
			"pkgmgr: package %q op %q declares Dispatch.Enumerations, which this operation can never carry: %q is an NFR-S6 equalized operation (processor.IsNFRS6Operation), whose declared read set the Processor closes over the keys its descriptor names and whose contextHint enumerations it refuses without exception — a declared walk would reach every submitted envelope and fault the operation terminally on arrival, collapsed to the generic ClaimKeyInvalid with no cause the caller or the dispatcher can attribute; drop the declaration, and leave the walk as the live bounded class-(e) read the script already runs",
			pkgName, opType, opType)
	}
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
		if err := validateReadTemplateList(pkgName, opType, field, []string{en.Hub}, enumerationHubRules); err != nil {
			return err
		}
	}
	return nil
}

// The clause each refusal appends after the placeholder it names, one set per
// Dispatch template list. The lists refuse a placeholder for DIFFERENT reasons
// and the fix differs with the reason, so the sentence travels with the caller
// rather than being written once at the refusal: an author told to move an
// enumeration hub to Dispatch.OptionalReads, or to file it under
// Dispatch.ContextParams, would be following advice into a field that does not
// exist on that declaration. Every remedy an enumeration author is shown names
// a move Dispatch.Enumerations actually has.
const (
	readsClientOnlyClause = "is client-only ({me.<type>}) vocabulary, which is OptionalReads-only — a required-side {me.<type>} would compile to a required PATTERN whose exclusion blankets every declared root of that type out of demotion; move this read to Dispatch.OptionalReads, or replace it with a server-resolvable placeholder ({actor}/{scopedTo}/{service}/{payload.<field>}) if the op genuinely requires it"

	readContextParamsClause = "uses {entity.<column>}, which is ContextParams-only vocabulary — a read template's client wholeKey silently drops it, so declare it under Dispatch.ContextParams instead, or drop it if the read does not need it"

	readOptionalMarkerClause = "carries a `?` OPTIONAL marker, which is ContextParams-only vocabulary — a read template's client wholeKey silently drops a ?-marked entry, so file this key under Dispatch.OptionalReads unmarked instead of marking it optional inline"

	enumerationHubClientOnlyClause = "is client-only ({me.<type>}) vocabulary, which an enumeration hub may not carry — {me.<type>} resolves only for a caller whose context supplies that type, so the same op would declare the walk for some callers and omit it for others, leaving the walk running undeclared for exactly the callers the declaration was meant to cover; give the hub a server-resolvable placeholder ({actor}/{payload.<field>}) or a literal key, which declares the same walk for every caller"

	enumerationHubContextParamsClause = "uses {entity.<column>}, which is ContextParams-only vocabulary — a hub read off whichever companion row the caller happens to be viewing is caller-dependent, and a static read posture for the op is what declaring the walk buys; fill a payload field from a Dispatch.ContextParams entry and name the hub {payload.<field>}, or name a literal key"

	enumerationHubOptionalMarkerClause = "carries a `?` OPTIONAL marker, which is ContextParams-only vocabulary — no descriptor-driven client's resolver closes a placeholder on it (internal/descriptorform/form.mjs refuses the template outright), so the hub never resolves and the walk reaches the envelope undeclared; drop the marker — Dispatch.Enumerations has no absence-tolerant half, and a hub that resolves for only some callers is what an enumeration declaration exists to refuse"
)

// templateListRules is the half of the template check that differs per Dispatch
// template list: the vocabulary the list admits, and the clause each refusal
// carries.
type templateListRules struct {
	// hub narrows the admitted vocabulary to a whole vertex key a
	// descriptor-driven client can resolve — {actor}, {payload.<field>}, or a
	// literal key — and holds every placeholder to a whole dot-delimited
	// segment with no `:id` modifier. See enumerationHubRules for why each
	// exclusion is there.
	hub bool

	// clientOnlyClause is the refusal a client-only {me.<type>} placeholder
	// carries here; empty ADMITS one, which is the OptionalReads case, where a
	// client-only placeholder is the whole point of the list.
	clientOnlyClause string
	// contextParamsClause is the refusal an {entity.<column>} placeholder
	// carries here.
	contextParamsClause string
	// optionalMarkerClause is the refusal a `?`-marked placeholder carries
	// here.
	optionalMarkerClause string
}

// The three lists' rules.
//
// enumerationHubRules is the narrow one, and each exclusion answers a defect a
// wider hub vocabulary ships:
//
//   - {scopedTo} and {service} are refused because the two shipped
//     descriptor-driven resolvers do not agree on them:
//     internal/descriptorform/form.mjs throws "unrecognized read template" —
//     which escapes submit() and makes the op unsubmittable from every
//     descriptorform app, showing a developer string to a person — while
//     cmd/facet/web/app.js resolves them. A hub a descriptor-driven client
//     cannot resolve is a declaration that never reaches an envelope, so
//     admitting one installs a walk that is declared on paper and undeclared
//     in fact, which is the one outcome the declaration exists to prevent.
//   - {entity.<column>} is ContextParams-only vocabulary, and a hub read off
//     the caller's companion row is caller-dependent for the same reason
//     {me.<type>} is.
//   - the `:id` modifier is refused because a hub is a WHOLE vertex key:
//     kv.Links walks from one, so a hub truncated to a bare NanoID resolves,
//     passes the client's wholeKey check, lands on the envelope, and can never
//     match the walk it declares.
//   - a mid-segment placeholder is refused for the same reason: the hub is one
//     key, not a fragment assembled into one.
var (
	readsRules = templateListRules{
		clientOnlyClause:     readsClientOnlyClause,
		contextParamsClause:  readContextParamsClause,
		optionalMarkerClause: readOptionalMarkerClause,
	}

	optionalReadsRules = templateListRules{
		contextParamsClause:  readContextParamsClause,
		optionalMarkerClause: readOptionalMarkerClause,
	}

	enumerationHubRules = templateListRules{
		hub:                  true,
		clientOnlyClause:     enumerationHubClientOnlyClause,
		contextParamsClause:  enumerationHubContextParamsClause,
		optionalMarkerClause: enumerationHubOptionalMarkerClause,
	}
)

// validateReadTemplateList classifies every placeholder in every entry of one
// Dispatch template list against the closed vocabulary rules carries: which
// roots the list admits, and the clause each refusal offers as the remedy.
func validateReadTemplateList(pkgName, opType, listName string, entries []string, rules templateListRules) error {
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
			hasIDModifier := strings.HasSuffix(base, ":id")
			base = strings.TrimSuffix(base, ":id")

			isClientOnly := false
			switch {
			case base == "actor":
				// resolvable root — legal in every list.
			case base == "scopedTo", base == "service":
				if rules.hub {
					return fmt.Errorf(
						"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q is outside the enumeration-hub vocabulary ({actor}, {payload.<field>}, or a literal key) — the shipped descriptor-driven resolvers disagree on it (internal/descriptorform/form.mjs throws \"unrecognized read template\", which escapes submit() and makes the op unsubmittable from every descriptorform app, while cmd/facet/web/app.js resolves it), and a hub a descriptor-driven client cannot resolve never reaches an envelope: the walk would be declared on paper and undeclared in fact",
						pkgName, opType, listName, entry, placeholder)
				}
			case strings.HasPrefix(base, "payload.") && base != "payload.":
				// resolvable root — legal in every list.
			case strings.HasPrefix(base, "me.") && base != "me.":
				isClientOnly = true
			case strings.HasPrefix(base, "entity."):
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q %s",
					pkgName, opType, listName, entry, placeholder, rules.contextParamsClause)
			default:
				if rules.hub {
					return fmt.Errorf(
						"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q is outside the enumeration-hub vocabulary ({actor}, {payload.<field>}, or a literal key — the forms every shipped descriptor-driven client resolves into a whole vertex key) — use one of those, since a hub no client resolves declares a walk that never reaches an envelope",
						pkgName, opType, listName, entry, placeholder)
				}
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q is outside the closed read-template vocabulary ({actor}, {scopedTo}, {service}, {payload.<field>}, {me.<type>}, each optionally suffixed :id) — use one of those placeholders, or extend the vocabulary deliberately together with its Processor-side floor semantics",
					pkgName, opType, listName, entry, placeholder)
			}

			if hasOptionalMarker {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q %s",
					pkgName, opType, listName, entry, placeholder, rules.optionalMarkerClause)
			}

			if isClientOnly {
				if rules.clientOnlyClause != "" {
					return fmt.Errorf(
						"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q %s",
						pkgName, opType, listName, entry, placeholder, rules.clientOnlyClause)
				}
				if !occupiesWholeSegment(entry, start, end) {
					return fmt.Errorf(
						"pkgmgr: package %q op %q Dispatch.%s entry %q: client-only placeholder %q does not occupy a whole dot-delimited segment of the template — the Processor's matcher only wildcards whole segments, and its fallback for a fragment wildcard is no floor at all; give the placeholder its own segment, or replace it with a server-resolvable placeholder",
						pkgName, opType, listName, entry, placeholder)
				}
				continue
			}

			if !rules.hub {
				continue
			}
			if hasIDModifier {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q carries the `:id` modifier, which truncates the substituted value to a bare NanoID — a hub is a WHOLE vertex key, because kv.Links walks from one, so an `:id` hub resolves, passes the client's wholeKey check, lands on the envelope and still names nothing the walk enumerates from; drop the `:id` and name the whole key",
					pkgName, opType, listName, entry, placeholder)
			}
			if !occupiesWholeSegment(entry, start, end) {
				return fmt.Errorf(
					"pkgmgr: package %q op %q Dispatch.%s entry %q: placeholder %q does not occupy a whole dot-delimited segment of the hub — a hub is a whole vertex key, not a fragment assembled into one; give the placeholder its own segment, or name a literal key",
					pkgName, opType, listName, entry, placeholder)
			}
		}
	}
	return nil
}

// occupiesWholeSegment reports whether the placeholder spanning [start, end) in
// entry fills a whole dot-delimited segment of it: the character before `{` is
// start-of-string or `.`, and the character after `}` is end-of-string or `.`.
func occupiesWholeSegment(entry string, start, end int) bool {
	before := byte(0)
	if start > 0 {
		before = entry[start-1]
	}
	after := byte(0)
	if end < len(entry) {
		after = entry[end]
	}
	return (start == 0 || before == '.') && (end == len(entry) || after == '.')
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
