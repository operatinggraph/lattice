package processor

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// applyDescriptorFloor enforces Contract #2 §2.5's "the descriptor's
// disposition is a floor the envelope cannot raise": where an operation's
// op-meta descriptor declares a key under `optionalReads`, an envelope naming
// that same key under `reads` MUST NOT harden it — the merged disposition is
// the weaker of the two.
//
// # Why this is the enforcement point and the dispatchers were not
//
// contextHint is client-supplied end to end. The Gateway copies `reads`
// verbatim out of the request body (internal/gateway/gateway.go) and step 3
// authorizes on operationType + actor + authContext without inspecting it, so
// converting our own dispatchers to `optionalReads` closed our clients and
// nobody else's. On an anti-enumeration path that gap is the whole exposure:
// an operation whose script adjudicates an absent target generically — one
// indistinguishable rejection for "no such entity" and "wrong secret" — is
// re-opened as an existence oracle by any caller who declares the target
// `reads` and reads `HydrationMiss`'s `details.missingKey`, one guess per
// request. This is the seam where the submitter's declaration stops being the
// last word.
//
// # What a template names
//
// A descriptor's read templates are written in the vocabulary a CLIENT
// resolves them with (cmd/facet/web/app.js's substituteTemplate). Half of it
// is server-resolvable — `{actor}`, `{service}`, `{payload.<field>}`, and
// `{scopedTo}` where step 3 proved the target — and substitutes to one
// concrete key. The other half, `{me.<type>}`, names a vertex out of the
// caller's own projected view, which the Processor does not have.
//
// A client-only placeholder compiles to a whole-segment WILDCARD constrained
// to a NanoID (compileDescriptorTemplate), so the template still names a
// determinate key SHAPE. Shape-matching is not an approximation of the
// client's resolution — for this vocabulary it is the correct semantics. The
// key whose absence is observable is the key the SCRIPT touches, and a script
// commonly recovers its own anchor from state the caller never supplied (a
// tab's `.status` naming the lease it was charged to), so the key it touches
// need not be the one the caller's own `me.<type>` would name. A submitter
// hardening a same-shaped probe for someone else's entity is exactly the
// decoupled case the pattern covers and a client-faithful resolution would
// miss.
//
// # Precedence
//
// The floor is applied to the ENVELOPE's declaration, at the head of step 4,
// BEFORE `derive_reads` runs and therefore before mergeDerivedReads. Four
// consequences, in the order they matter:
//
//  1. mergeDerivedReads' "envelope's disposition stands" rule then sees the
//     POST-demotion envelope. A derived `reads` entry colliding with a demoted
//     key finds it already declared and skips it, so the key keeps the weaker
//     disposition — the same weakest-wins answer the derived-vs-envelope rule
//     already gives, reached without a second rule.
//
//     A key the envelope NEVER declared is outside that rule, so this arm never
//     sees it, and the floor reaches it at the merge instead: mergeDerivedReads
//     asks the same resolver and REFUSES a derived required key the descriptor
//     calls absence-tolerant. Refuses rather than demotes — a derivation and a
//     descriptor from the SAME package disagreeing about one key is a
//     package-internal contradiction, and silently softening the derivation's
//     requirement is the dangerous direction for a read the DDL's own author
//     demanded fail-closed.
//
//  2. The demotion MOVES the key rather than copying it, and moves EVERY
//     match rather than the first. step4_hydrate's "a key in both lists keeps
//     fail-closed reads semantics" would otherwise re-harden on the very next
//     loop, silently undoing this.
//
//  3. `egressReads` is floored too, but by a DIFFERENT action, because the
//     obvious one is a plaintext leak. Moving an egress key into optionalReads
//     would hand the script the decrypted body where the submitter asked for a
//     `$sensitiveRef` the bridge opens — a disposition change in the dangerous
//     direction. What the oracle actually rides on is narrower: an absent
//     egress key calls the same markRequiredAbsent as an absent `reads` key
//     (step4_hydrate), and RequiredAbsent is a FLAT map that does not record
//     which list put a key there, so `contextHint.egressReads: [<guess>]`
//     reproduces HydrationMiss + details.missingKey verbatim. So a floored
//     egress key STAYS in egressReads — presence still authors the ref,
//     unchanged — and only its ABSENCE is made tolerant, via
//     EgressAbsenceTolerant. Nothing about a present document moves.
//
//     The collision rules are unaffected either way: ParseEnvelope rejects a
//     key declared under egressReads AND either read list, the Reads→
//     OptionalReads move keeps both sides inside "either read list", and the
//     egress arm moves nothing at all — so a demotion can neither create such
//     a collision nor mask one, and mergeDerivedReads' re-check over the
//     merged set sees the egress set it would have seen.
//
//  4. REQUIRED WINS, over an exclusion set the SUBMITTER CANNOT STEER. Where
//     one key is named by both of a descriptor's lists, the required reading
//     stands: wrong-demotion is the dangerous direction, turning the
//     HydrationMiss the script's author expected into a silent None.
//
//     The exclusion is therefore computed from the descriptor plus the
//     authenticated identity ALONE — `{actor}`, `{service}`, a `{scopedTo}`
//     step 3 validated, and literal text. A required template carrying
//     `{payload.<field>}` contributes nothing (resolveDescriptorRequired).
//     That restriction is the control's integrity, not tidiness: a
//     payload-derived exclusion is written by the same hostile party the floor
//     is defending against, so a submitter who put their probe key into that
//     payload field would exclude it from the floor and hand themselves back
//     the HydrationMiss oracle — with a demotion this file would otherwise
//     perform. An exclusion set the attacker can address is not a precedence
//     rule, it is a bypass.
//
// # Direction of failure
//
// An operation with NO descriptor demotes nothing. That is the ordinary case
// — most of the corpus carries no Dispatch block — and it is not a silent
// hardening: the rule has no subject, because there is no descriptor declaring
// the key optional. The same holds for a template this Processor cannot
// compile at all: an unresolvable server-side placeholder (a payload field the
// envelope omits), a `{scopedTo}` on a path step 3 did not validate, a
// placeholder outside the vocabulary, or a client-only placeholder sharing a
// segment with literal text. Those are logged, not guessed at.
//
// Demoting on doubt is deliberately NOT the fallback, and the asymmetry is
// worth stating: demotion is the weaker disposition for the oracle but the
// DANGEROUS one for correctness — softening a read the operation genuinely
// depends on turns a HydrationMiss into a silent None, which is the failure
// the §2.5 authoring rule exists to prevent. So the floor demotes exactly the
// keys a compilable descriptor names, and nothing else.
//
// The envelope and the logger are the resolver's, never the caller's. Taking
// them as separate parameters would let a caller log one envelope's requestId
// beside a demotion resolved against another's payload; there is nothing to
// keep in step when there is only one source. A nil resolver is an operation
// with no descriptor and demotes nothing, which is also what makes the
// resolver's fields safe to read at the end of this function.
func applyDescriptorFloor(base declaredReads, resolver *descriptorFloorResolver) declaredReads {
	if resolver == nil || (len(base.Reads) == 0 && len(base.EgressReads) == 0) {
		return base
	}
	floored := resolver.floored

	// The egress arm: mark, never move. See the header's precedence note 3 —
	// relocating an egress key would swap a bridge-opened ref for plaintext.
	for _, k := range base.EgressReads {
		if !floored(k) {
			continue
		}
		if base.EgressAbsenceTolerant == nil {
			base.EgressAbsenceTolerant = map[string]struct{}{}
		}
		base.EgressAbsenceTolerant[k] = struct{}{}
	}

	optional := make(map[string]struct{}, len(base.OptionalReads))
	for _, k := range base.OptionalReads {
		optional[k] = struct{}{}
	}

	// Both lists are rebuilt rather than sliced in place: base.Reads is the
	// envelope's own JSON-decoded backing array, and mergeDerivedReads states
	// the same invariant for the same reason — nothing here may write into
	// storage the envelope owns.
	kept := make([]string, 0, len(base.Reads))
	demoted := make([]string, 0, len(base.Reads))
	grown := append([]string{}, base.OptionalReads...)
	for _, k := range base.Reads {
		if !floored(k) {
			kept = append(kept, k)
			continue
		}
		demoted = append(demoted, k)
		if _, dup := optional[k]; dup {
			// Already declared optional too. Dropping it from Reads is the
			// whole demotion: leaving it would let step4_hydrate's
			// both-lists rule keep the fail-closed reading.
			continue
		}
		optional[k] = struct{}{}
		grown = append(grown, k)
	}
	if len(demoted) == 0 {
		return base
	}
	base.OptionalReads = grown
	base.Reads = kept
	resolver.logger.Info("step4: descriptor floor demoted declared reads to optional",
		"operationType", resolver.env.OperationType, "requestId", resolver.env.RequestID, "keys", demoted)
	return base
}

// descriptorFloorResolver answers one question — "does this operation's own
// descriptor make this key absence-tolerant?" — for the two arms that must
// give the same answer to it: applyDescriptorFloor, over the keys the ENVELOPE
// declared, and mergeDerivedReads, over the keys the DDL's `derive_reads`
// produced. One resolver per Hydrate call, built by step 4 and handed to both,
// so the descriptor is compiled against the envelope exactly once and the two
// arms cannot drift.
//
// Resolution is deferred to the first question and memoized. Deferred because
// an uncompilable template's Warn asserts that the control did not apply TO A
// KEY, and an envelope declaring nothing whose derivation returns nothing has
// no key for it to be about — compiling eagerly would log that claim on every
// operation carrying a descriptor. Memoized because both arms ask, and the
// Warn belongs to the descriptor, not to the arm that happened to ask first.
//
// Lifetime is the Hydrate call: it holds that call's envelope and the
// descriptor as the cache had it at that instant, and step 4 rebuilds it on
// every OCC retry from the same two inputs.
type descriptorFloorResolver struct {
	templates DispatchTemplates
	env       *OperationEnvelope
	logger    *slog.Logger

	resolved bool
	floor    descriptorFloor
	required map[string]struct{}

	admitResolved bool
	admitted      map[string]struct{}

	payloadParsed bool
	payload       map[string]interface{}
}

// newDescriptorFloorResolver holds a descriptor's templates against one
// envelope. An operation with NO descriptor has no resolver at all, and a nil
// resolver floors nothing — the ordinary case, and the safe one: the rule has
// no subject, so there is nothing to demote and nothing to refuse.
func newDescriptorFloorResolver(templates DispatchTemplates, env *OperationEnvelope, logger *slog.Logger) *descriptorFloorResolver {
	return &descriptorFloorResolver{templates: templates, env: env, logger: logger}
}

// floored reports whether the descriptor declares this key absence-tolerant:
// covered by an `optionalReads` template and not pinned by a `reads` one.
//
// The predicate is a function of the descriptor's own templates and the
// step-3-authenticated identity, and of nothing a request body can address —
// resolveDescriptorRequired is what holds that line, refusing to build an
// exclusion out of a `{payload.<field>}` template or a key SHAPE. See the
// header's precedence note 4.
func (r *descriptorFloorResolver) floored(key string) bool {
	if r == nil {
		return false
	}
	r.resolve()
	if _, isRequired := r.required[key]; isRequired {
		return false
	}
	return r.floor.covers(key)
}

func (r *descriptorFloorResolver) resolve() {
	if r.resolved {
		return
	}
	r.resolved = true
	if len(r.templates.OptionalReads) == 0 {
		return
	}
	payload := r.payloadMap()
	r.floor = resolveDescriptorFloor(r.templates.OptionalReads, r.env, payload, r.logger)
	if r.floor.empty() {
		// No floor means no key can be excluded FROM one, so the required
		// templates are never compiled and never logged about.
		return
	}
	r.required = resolveDescriptorRequired(r.templates.Reads, r.env, payload, r.logger)
}

// payloadMap decodes the envelope's payload once for every arm that compiles a
// template against it. A malformed payload decodes to nil, and every
// `{payload.<field>}` template then fails to compile — the "names no key here"
// answer each arm already has a rule for.
func (r *descriptorFloorResolver) payloadMap() map[string]interface{} {
	if r.payloadParsed {
		return r.payload
	}
	r.payloadParsed = true
	if len(r.env.Payload) > 0 {
		r.payload, _ = jsonToGenericMap(r.env.Payload)
	}
	return r.payload
}

// admits reports whether this operation's own descriptor NAMES this key. It is
// the membership test the CLOSED declared-read set is decided by
// (refuseUndeclaredContextHint): the admitted set is the descriptor's `reads`
// and `optionalReads` templates compiled against this envelope, CONCRETE KEYS
// ONLY.
//
// A template that compiles to a key SHAPE contributes nothing, for the same
// reason resolveDescriptorRequired refuses one: `{me.<type>}` covers every
// vertex of its type, so admitting a shape would admit an unbounded set of keys
// and hand back the very padding channel the closure exists to remove. A
// template this Processor cannot compile against this envelope contributes
// nothing either — it names no key here, so it admits none.
//
// A template whose compiled text is not a Contract #1 KEY contributes nothing
// either, and that rule belongs to this membership test rather than to a gate
// further down step 4 that happens to reach the same outcome. The
// segment matcher is grammar-blind: a `{payload.targetIdentityKey}` the
// submitter fills with `vtx.identity.*` compiles to three literal segments, so
// concrete() reports a key and the wildcard would be admitted as a declaration.
// step4_hydrate's substrate.ClassifyKey check would then reject it — but that
// runs AFTER this membership decision, and a closure that admits what it cannot
// name is only accidentally right. So the compiled text is classified here,
// with the same classifier the hydrate path uses, and a non-key admits nothing.
//
// A `{payload.<field>}` template DOES contribute, which is the opposite of
// resolveDescriptorRequired's rule, and the polarity is the point rather than
// an oversight. There the subject is an EXCLUSION from the floor: a submitter
// who can steer a payload field could lift their own probe key out of the
// control's reach, so a submitter-derived template must not build one. Here the
// subject is the COUNT — how many keys an envelope may declare — and that comes
// from the descriptor alone, one admitted key per template. Steering the
// payload field steers WHICH key is admitted, never HOW MANY, and the key it
// steers to is the key this operation's own script is about to adjudicate
// anyway. Inheriting the exclusion would empty the admitted set for every
// descriptor whose templates are payload-rooted — which both NFR-S6 descriptors
// are, entirely — and refuse every legitimate submission.
//
// A nil resolver is an operation with no descriptor and admits nothing.
func (r *descriptorFloorResolver) admits(key string) bool {
	if r == nil {
		return false
	}
	r.resolveAdmitted()
	_, ok := r.admitted[key]
	return ok
}

// resolveAdmitted compiles both descriptor lists into the admitted key set,
// once per Hydrate call. Deferred and memoized for the reasons the header
// gives: the Warn below asserts that a template named no key for THIS envelope,
// and that claim belongs to the descriptor rather than to the arm that asked.
//
// Two whole-descriptor conditions admit NOTHING, and each carries its own Warn
// because the alternative is an over-deny with no per-template Warn to explain
// it — an operator watching a closed-set operation refuse every submission
// otherwise cannot tell "the submitter declared an extra key" from "this
// descriptor names nothing to declare".
//
//   - MORE THAN ONE op-meta root claims this operationType. floorsByOpType
//     UNIONS their templates, which is the safe direction for a floor (more
//     demotion) and the dangerous one for a closed set (more admission): a
//     second claimant carrying a thousand `{payload.padN}` templates would hand
//     the padding channel straight back. pkgmgr refuses a duplicate claimant at
//     install, so reaching here takes install privilege — the closure holds its
//     own line anyway, and over-denies visibly rather than admitting a union it
//     cannot attribute.
//   - The descriptor names NO read template at all. That is reachable with no
//     operator error: loadOpMetaDispatch reports an op-meta whose `.dispatch` is
//     absent or tombstoned as found-with-empty-lists, so an identity-domain
//     version predating the `Dispatch` block leaves hasDescriptor true and every
//     template list empty.
func (r *descriptorFloorResolver) resolveAdmitted() {
	if r.admitResolved {
		return
	}
	r.admitResolved = true
	if r.templates.Claimants > 1 {
		r.logger.Warn("step4: more than one op-meta descriptor claims this operationType; their union is not attributable, so the closed declared-read set admits NO key",
			"operationType", r.env.OperationType, "requestId", r.env.RequestID, "claimants", r.templates.Claimants)
		return
	}
	total := len(r.templates.Reads) + len(r.templates.OptionalReads)
	if total == 0 {
		r.logger.Warn("step4: this operation's descriptor names NO read template; the closed declared-read set admits NO key, so every declared key is refused",
			"operationType", r.env.OperationType, "requestId", r.env.RequestID)
		return
	}
	payload := r.payloadMap()
	admitted := make(map[string]struct{}, total)
	admit := func(list []string, class string) {
		for _, tpl := range list {
			pattern, ok := compileDescriptorTemplate(tpl, r.env, payload)
			if !ok {
				r.logger.Warn("step4: descriptor "+class+" template does not compile against this envelope; it admits no key into the closed declared-read set",
					"operationType", r.env.OperationType, "requestId", r.env.RequestID, "template", tpl)
				continue
			}
			key, isConcrete := pattern.concrete()
			if !isConcrete {
				r.logger.Warn("step4: descriptor "+class+" template compiles to a key SHAPE, not a key; it admits no key into the closed declared-read set",
					"operationType", r.env.OperationType, "requestId", r.env.RequestID, "template", tpl)
				continue
			}
			if substrate.ClassifyKey(key) == substrate.KindUnknown {
				r.logger.Warn("step4: descriptor "+class+" template compiles to text the Contract #1 key grammar rejects; it admits no key into the closed declared-read set",
					"operationType", r.env.OperationType, "requestId", r.env.RequestID, "template", tpl)
				continue
			}
			admitted[key] = struct{}{}
		}
	}
	admit(r.templates.Reads, "read")
	admit(r.templates.OptionalReads, "optionalRead")
	r.admitted = admitted
}

// admittedCount reports how many concrete keys this operation's descriptor
// names for this envelope. It exists for the refusal Warn: the fault carries no
// key, so the log is the operator's only copy, and the SIZE of the admitted set
// is what separates "this submitter declared one key too many" from "this
// descriptor admits nothing at all". A nil resolver is an operation with no
// descriptor, which admits nothing.
func (r *descriptorFloorResolver) admittedCount() int {
	if r == nil {
		return 0
	}
	r.resolveAdmitted()
	return len(r.admitted)
}

// refuseUndeclaredContextHint closes the declared-read set of an NFR-S6
// operation (nfr_s6_wire_shape.go's nfrS6Operations): the SUBMITTER's own
// contextHint may name only what this operation's descriptor names, and
// anything else faults the operation before hydration instead of being demoted.
//
// # Why the set is closed here and nowhere else
//
// These operations' rejection causes are equalized rather than masked: the
// package script and the hydrate path do the SAME work on every cause, over the
// keys the DESCRIPTOR names. That equality is a property of a FIXED key set, and
// only of a fixed one. An extra key a submitter names is work nothing has
// equalized — its hydration cost turns on whether the key exists, whether it is
// sensitive, and whether it is tombstoned, which are precisely the three facts
// the equalization removes from the descriptor-named set — so admitting it
// re-introduces per-cause divergence directly, in the caller's own choice of
// declaration. Closing the set is what keeps "the same work on every cause" a
// property of the OPERATION rather than of whoever submitted it.
//
// It is also what keeps the state-dependent egress arm out of reach.
// decryptSensitiveDoc refuses a tombstoned sensitive aspect outright under the
// egress disposition (a capability over a dead aspect must not leave the
// Processor) while serving a live one — a refusal whose reachability depends on
// the target's state. Refusing every declared `egressReads` key on these two
// operations is what makes that arm unreachable for them.
//
// The floor above bounds a declared key's DISPOSITION. Nothing bounds the
// COUNT: opwire.MaxDeclaredReads admits 1000 declared keys, the Gateway copies
// contextHint into the envelope verbatim and step 3 authorizes without
// inspecting it, so every declared key resolves inside step-4 hydration. For
// these two operations the descriptor already names the entire legitimate set —
// the shipped dispatchers build their hint from exactly it
// (internal/identityceremony) — so the set can be closed outright rather than
// merely floored.
//
// Refusal, not demotion, and the difference is the whole mechanism: a demoted
// extra key is still hydrated, still enlarges the batched step-4 snapshot, and
// still costs whatever its own target's state costs.
//
// # What is refused
//
//   - a `reads` or `optionalReads` key the descriptor does not name (admits).
//   - EVERY `egressReads` key. OpDispatchSpec carries no EgressReads field
//     (internal/pkgmgr/definition.go), so no descriptor can name one and the
//     admitted egress set is empty by construction — which is also what keeps
//     the tombstone-dependent egress refusal above unreachable here.
//   - EVERY `enumerations` entry, for the same structural reason. An
//     enumeration is metadata: the Processor shape-validates it at parse and
//     never hydrates it (Contract #2 §2.5 class (e)), so it buys no work at all.
//     It is refused because the rule is CLOSED, not because it costs anything.
//
// Repetition is not this rule's subject: MaxDeclaredReads still admits 1000
// copies of an admitted key, each costing one map lookup, and distinctKeys
// collapses them before any KV work — and a repeat names no key the descriptor
// did not already name, so it adds no state-dependent work. MEMBERSHIP is what
// the rule closes.
//
// A nil resolver is an operation with no `Dispatch` descriptor: it admits
// nothing, so every declared key is refused. An NFR-S6 operation whose
// descriptor is missing over-denies visibly rather than silently reverting to
// the open envelope surface this closes. resolveAdmitted names the two other
// whole-descriptor conditions that admit nothing — a multi-claimant
// operationType and a descriptor with no read template at all — and each is
// audible at Warn on its own, because an over-deny an operator cannot attribute
// is one nobody fixes.
//
// # What reaches the caller
//
// A *HydrationError carrying NO key, mirroring DeriveReadsFloorContradiction
// (derive_reads.go). The refused key is the submitter's own probe, so echoing
// it back in details.missingKey would answer the very question these operations
// collapse their replies to refuse; the Warn here is the operator's only copy,
// and it carries the size of the admitted set beside the refused declaration —
// an over-deny reads as "admitted=0" where an over-declaring submitter reads as
// a positive count.
// On an NFR-S6 operation the fault is then collapsed by replyRejection into the
// generic ClaimKeyInvalid with nil details, exactly like every other rejection
// of these operations.
//
// The logger is the caller's rather than the resolver's because the
// no-descriptor case has no resolver, and that is the case an operator most
// needs to be able to see.
func refuseUndeclaredContextHint(env *OperationEnvelope, resolver *descriptorFloorResolver, logger *slog.Logger) error {
	hint := env.ContextHint
	if hint == nil {
		return nil
	}
	refuse := func(class, declared string) error {
		logger.Warn("step4: contextHint declares a read this operation's descriptor does not name; the closed declared-read set refuses the operation",
			"operationType", env.OperationType, "requestId", env.RequestID, "class", class, "declared", declared,
			"admitted", resolver.admittedCount())
		return &HydrationError{
			Code: "UndeclaredContextHintKey", OperationRequestID: env.RequestID,
			Cause: errors.New("contextHint declares a read outside the set this operation's own descriptor names; the declared read set for this operation is closed (the Processor log names what was refused)"),
		}
	}
	for _, key := range hint.Reads {
		if !resolver.admits(key) {
			return refuse("reads", key)
		}
	}
	for _, key := range hint.OptionalReads {
		if !resolver.admits(key) {
			return refuse("optionalReads", key)
		}
	}
	if len(hint.EgressReads) > 0 {
		return refuse("egressReads", hint.EgressReads[0])
	}
	if len(hint.Enumerations) > 0 {
		e := hint.Enumerations[0]
		return refuse("enumerations", e.Hub+" "+e.Relation+" "+e.Direction)
	}
	return nil
}

// warnContradiction records the derived required key the floor refuses
// (mergeDerivedReads). It lives here because the resolver owns the envelope and
// the logger, and because this Warn is the ONLY copy of that key an operator
// gets: the fault deliberately carries no key to the caller, since a derived
// key is one the submitter could not have expressed.
func (r *descriptorFloorResolver) warnContradiction(key string) {
	r.logger.Warn("step4: derive_reads returned a required read this operation's own descriptor declares absence-tolerant; the operation faults",
		"operationType", r.env.OperationType, "requestId", r.env.RequestID, "key", key)
}

// descriptorFloor is a descriptor's optionalReads list compiled against one
// envelope: the concrete keys it names, plus the key SHAPES it names for the
// placeholders only a client can resolve.
//
// Exact keys are held apart from patterns because they answer in one map
// lookup, and because a template carrying no client-only vocabulary must reach
// the same answer it reaches with no pattern machinery in the file at all.
type descriptorFloor struct {
	exact    map[string]struct{}
	patterns []keyPattern
}

func (f descriptorFloor) empty() bool { return len(f.exact) == 0 && len(f.patterns) == 0 }

// covers reports whether a declared key falls under the floor. Cost is
// O(patterns × segments) per declared key, over at most MaxDeclaredReads keys
// and a descriptor's own template count — no budget is spent on it.
func (f descriptorFloor) covers(key string) bool {
	if _, ok := f.exact[key]; ok {
		return true
	}
	for _, p := range f.patterns {
		if p.matches(key) {
			return true
		}
	}
	return false
}

// resolveDescriptorFloor compiles a descriptor's optionalReads templates
// against this envelope.
//
// A template this Processor cannot compile yields NO key and NO shape — the
// descriptor is not naming a determinate key or shape for this envelope, so
// there is nothing for the floor to be about.
func resolveDescriptorFloor(templates []string, env *OperationEnvelope, payload map[string]interface{}, logger *slog.Logger) descriptorFloor {
	floor := descriptorFloor{exact: make(map[string]struct{}, len(templates))}
	for _, tpl := range templates {
		pattern, ok := compileDescriptorTemplate(tpl, env, payload)
		if !ok {
			// WARN, not Debug, and the level is the point: "the control did
			// not apply" has to be at least as visible as "the control fired",
			// or the only thing production shows is the enforced case and the
			// gap is invisible at exactly the levels operators run.
			//
			// Not necessarily a defect in the descriptor — an envelope that
			// omits an optional payload field reaches here legitimately — but
			// it does mean this key has no floor, so the envelope's own
			// disposition is the last word for it.
			logger.Warn("step4: descriptor optionalRead template does not compile against this envelope; NO floor applied for this key",
				"operationType", env.OperationType, "requestId", env.RequestID, "template", tpl)
			continue
		}
		if key, isConcrete := pattern.concrete(); isConcrete {
			floor.exact[key] = struct{}{}
			continue
		}
		floor.patterns = append(floor.patterns, pattern)
	}
	return floor
}

// resolveDescriptorRequired compiles a descriptor's `reads` templates and
// returns the keys the floor must leave fail-closed however the optional side
// matches them. Two rules narrow which templates contribute one, and both are
// the same "no contribution + Warn" posture an uncompilable optional template
// gets — failing toward the floor still applying rather than toward the
// oracle.
//
// A template naming `{payload.<field>}` contributes NOTHING. Everything the
// exclusion set is built from must come from the descriptor and the
// authenticated identity: `{actor}` and `{service}` are step-3 facts,
// `{scopedTo}` resolves only where step 3 proved the target, and literal text
// is the descriptor's own. Payload is not — it is the hostile submitter's own
// field, and an exclusion addressable from it lets that submitter name the key
// they are probing and take it out of the floor's reach, converting the
// precedence rule into a per-request bypass of the demotion this file would
// otherwise perform.
//
// A template that compiles to a PATTERN contributes nothing either. A loose
// required shape would blanket every key of that shape out of demotion — one
// `{me.<type>}` on the required side preserves the oracle for every declared
// root of the type, which is the outcome this whole file exists to close. The
// pkgmgr install gate refuses to author that shape, but
// `InstallPackage`/`UpgradePackage` submitted directly reach the kernel install
// script without that preflight, so the runtime carries its own rule rather
// than inheriting one.
//
// What survives both rules is the exclusion a descriptor can state about ITS
// OWN operation and this actor — the shape of a genuine self-contradiction
// between the two lists — and nothing a request can address.
func resolveDescriptorRequired(templates []string, env *OperationEnvelope, payload map[string]interface{}, logger *slog.Logger) map[string]struct{} {
	if len(templates) == 0 {
		return nil
	}
	required := make(map[string]struct{}, len(templates))
	for _, tpl := range templates {
		pattern, ok := compileDescriptorTemplate(tpl, env, payload)
		if !ok {
			logger.Warn("step4: descriptor read template does not compile against this envelope; it excludes no key from the floor",
				"operationType", env.OperationType, "requestId", env.RequestID, "template", tpl)
			continue
		}
		if pattern.submitterDerived {
			logger.Warn("step4: descriptor read template resolves from submitter-supplied payload; it excludes no key from the floor",
				"operationType", env.OperationType, "requestId", env.RequestID, "template", tpl)
			continue
		}
		key, isConcrete := pattern.concrete()
		if !isConcrete {
			logger.Warn("step4: descriptor read template compiles to a key SHAPE, not a key; it excludes no key from the floor",
				"operationType", env.OperationType, "requestId", env.RequestID, "template", tpl)
			continue
		}
		required[key] = struct{}{}
	}
	return required
}

// patternSegment is one dot-delimited segment of a compiled template: either
// literal text a matching key must carry byte for byte, or a wildcard standing
// for exactly one NanoID.
type patternSegment struct {
	literal  string
	wildcard bool
}

// keyPattern is one descriptor read template compiled against one envelope.
// Its lifetime is that Hydrate call: it is derived from the envelope plus the
// DDL cache's descriptor snapshot, dropped with declaredReads, and re-derived
// on an OCC retry.
//
// A wildcard is always a WHOLE segment, never a fragment of one, which is what
// keeps matching to segment equality with no prefix/suffix arithmetic. Segment
// count is already the key-shape discriminator (Contract #1: 3 segments for a
// vertex, 4 for an aspect, 6 for a link), so a pattern cannot match across
// shapes.
//
// Two shapes this matcher does not express, each with zero live instances and
// each failing toward NO floor, which is the safe direction:
//
//   - A wildcard segment is a NanoID at every position, including a localName
//     position, where real local names are not NanoIDs
//     (substrate.IsValidLocalName is a different alphabet and length). So a
//     pattern like `vtx.<type>.{payload.x}.{me.y:id}` under-covers the real
//     aspects of that vertex: they simply do not match, and their disposition
//     stands as the envelope declared it.
//   - The whole-key `{me.<type>}` form covers `vtx.<type>.<any NanoID>`, i.e.
//     any declared root of that type. On an EGRESS key that reaches further
//     than a silent None: step4_hydrate routes a floored absent key into
//     KnownAbsent instead of RequiredAbsent, firstRequiredAbsentMutation
//     (starlark_runner.go) guards only RequiredAbsent, and
//     applyHydratedRevisions (commit_path.go) leaves an update or tombstone on
//     a step-4-absent key unconditioned — so an over-wide floor of that form
//     can drop a write-side guard, not merely soften a read. Every live
//     client-only template is the `{me.<type>:id}` FRAGMENT form inside a link
//     key, which fixes every other segment.
type keyPattern struct {
	segments []patternSegment
	// submitterDerived records that at least one segment's text came from
	// `{payload.<field>}` — a value the requester wrote. The floor arm ignores
	// this (a floor over a key the submitter named is still a floor); the
	// required arm refuses to build an exclusion from it, per
	// resolveDescriptorRequired.
	submitterDerived bool
}

// concrete returns the single key this pattern names, or ok=false when it
// carries a wildcard and therefore names a shape instead.
func (p keyPattern) concrete() (string, bool) {
	out := make([]string, len(p.segments))
	for i, seg := range p.segments {
		if seg.wildcard {
			return "", false
		}
		out[i] = seg.literal
	}
	return strings.Join(out, "."), true
}

// matches reports whether a concrete key falls under this pattern: same
// segment count, every literal segment byte-equal, every wildcard segment a
// canonical NanoID.
func (p keyPattern) matches(key string) bool {
	segments := strings.Split(key, ".")
	if len(segments) != len(p.segments) {
		return false
	}
	for i, want := range p.segments {
		if want.wildcard {
			if !substrate.IsValidNanoID(segments[i]) {
				return false
			}
			continue
		}
		if segments[i] != want.literal {
			return false
		}
	}
	return true
}

// templatePart is one piece of an expanded template before it is cut into
// segments: either text (literal, or a placeholder's substituted value) or an
// atomic wildcard. Wildcards are carried as their own part rather than as a
// marker inside the text, so that a payload value spelling the marker cannot
// pass itself off as one.
type templatePart struct {
	text     string
	wildcard bool
	// submitterDerived marks text that came out of the request payload, so the
	// provenance survives the cut into segments and reaches keyPattern.
	submitterDerived bool
}

// compileDescriptorTemplate compiles one descriptor read template against this
// envelope, into either a concrete key or a key pattern.
//
// The vocabulary, and what each element compiles to:
//
//   - literal text — itself.
//   - `{actor}`, `{service}`, `{payload.<field>}`, each ±`:id` — the concrete
//     value. `:id` asks for the Contract #1 bare id instead of the whole vertex
//     key, and is refused rather than truncated over a value that is not a
//     vertex key: a bare id taken off a malformed key addresses something else.
//   - `{scopedTo}` ±`:id` — `authContext.target`, but ONLY where step 3 proved
//     it (AuthTargetValidated: the platform scope=self path and the task path).
//     An unvalidated target is a client-supplied hint, and resolving one would
//     let a lying submitter steer the floor away from the key they are probing.
//     It never becomes a wildcard either: the target's TYPE is unknown, so the
//     shape would be `vtx.<any>.<nanoid>` and would demote every declared
//     vertex root the envelope carries.
//   - `{me.<type>:id}` — a one-segment NanoID wildcard.
//   - `{me.<type>}` — the caller's own vertex of that type in whole-key form:
//     the literal segments `vtx` and `<type>`, then a NanoID wildcard. `<type>`
//     must be a bare Contract #1 type token; anything else in that position
//     (an optional-marker `?`, a stray modifier, a dotted path) is a
//     placeholder outside this vocabulary and is refused rather than folded
//     into a literal segment, so an unrecognized spelling is always audible at
//     Warn instead of compiling to a pattern that quietly matches nothing.
//
// Anything else is refused (ok=false): an unknown placeholder, an unclosed
// `{`, a placeholder that resolves to nothing, an empty segment, or a client-
// only placeholder sharing a segment with literal text. That last one is the
// mid-segment fragment idiom — legal and live with a SERVER-resolvable
// placeholder, where it substitutes concretely and needs no wildcard at all
// (`vtx.session.{payload.session:id}.bkr{actor:id}`), and inexpressible in a
// whole-segment matcher with a client-only one. Refusing it costs a floor;
// widening the wildcard to swallow the literal text around it would demote
// keys no template named.
func compileDescriptorTemplate(tpl string, env *OperationEnvelope, payload map[string]interface{}) (keyPattern, bool) {
	parts, ok := expandDescriptorTemplate(tpl, env, payload)
	if !ok {
		return keyPattern{}, false
	}
	return segmentTemplateParts(parts)
}

func expandDescriptorTemplate(tpl string, env *OperationEnvelope, payload map[string]interface{}) ([]templatePart, bool) {
	var parts []templatePart
	rest := tpl
	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			parts = append(parts, templatePart{text: rest})
			break
		}
		closed := strings.Index(rest[open:], "}")
		if closed < 0 {
			return nil, false
		}
		closed += open
		parts = append(parts, templatePart{text: rest[:open]})
		expr := rest[open+1 : closed]
		rest = rest[closed+1:]

		bareID := strings.HasSuffix(expr, ":id")
		expr = strings.TrimSuffix(expr, ":id")

		if vertexType, isSelf := strings.CutPrefix(expr, "me."); isSelf {
			if !keys.IsValidTypeSegment(vertexType) {
				return nil, false
			}
			if !bareID {
				parts = append(parts, templatePart{text: "vtx." + vertexType + "."})
			}
			parts = append(parts, templatePart{wildcard: true})
			continue
		}

		var val string
		fromPayload := false
		switch {
		case expr == "actor":
			val = env.Actor
		case expr == "service":
			if env.AuthContext != nil {
				val = env.AuthContext.Service
			}
		case expr == "scopedTo":
			if !env.AuthTargetValidated {
				return nil, false
			}
			if env.AuthContext != nil {
				val = env.AuthContext.Target
			}
		case strings.HasPrefix(expr, "payload."):
			s, isString := payload[strings.TrimPrefix(expr, "payload.")].(string)
			if !isString {
				return nil, false
			}
			val = s
			fromPayload = true
		default:
			return nil, false
		}
		if val == "" {
			return nil, false
		}
		if bareID {
			_, id, parsed := substrate.ParseVertexKey(val)
			if !parsed {
				return nil, false
			}
			val = id
		}
		parts = append(parts, templatePart{text: val, submitterDerived: fromPayload})
	}
	return parts, true
}

// segmentTemplateParts cuts the expanded parts into whole dot-delimited
// segments. The cut runs over the SUBSTITUTED text, not the raw template,
// because a substituted value carries its own dots — `{actor}` is a whole
// three-segment vertex key.
//
// Refused: a segment holding both a wildcard and literal text (in either
// order), two wildcards with no separator between them, an empty segment (a
// hole is not a shorter key, it is a different and invalid one — the client-
// side resolver, cmd/facet/web/app.js's wholeKey, drops it for the same
// reason), and a substituted value carrying a `{` of its own.
func segmentTemplateParts(parts []templatePart) (keyPattern, bool) {
	var segments []patternSegment
	submitterDerived := false
	current := patternSegment{}
	for _, part := range parts {
		if part.submitterDerived {
			submitterDerived = true
		}
		if part.wildcard {
			if current.wildcard || current.literal != "" {
				return keyPattern{}, false
			}
			current.wildcard = true
			continue
		}
		if part.text == "" {
			continue
		}
		for i, chunk := range strings.Split(part.text, ".") {
			if i > 0 {
				segments = append(segments, current)
				current = patternSegment{}
			}
			if chunk == "" {
				continue
			}
			if current.wildcard || strings.Contains(chunk, "{") {
				return keyPattern{}, false
			}
			current.literal += chunk
		}
	}
	segments = append(segments, current)
	for _, seg := range segments {
		if !seg.wildcard && seg.literal == "" {
			return keyPattern{}, false
		}
	}
	return keyPattern{segments: segments, submitterDerived: submitterDerived}, true
}
