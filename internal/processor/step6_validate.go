package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// DDLViolation is the typed step-6 failure. Carries the violated
// constraint name, the offending mutation key, and the operation's
// requestId so the commit path can construct a rejection reply.
type DDLViolation struct {
	ViolatedConstraint string // e.g., "permittedCommands", "sensitiveAspectScope", "keyPattern"
	MutationKey        string
	OperationRequestID string
	Detail             string
}

func (e *DDLViolation) Error() string {
	return fmt.Sprintf("DDLViolation[%s]: requestId=%s mutationKey=%s: %s",
		e.ViolatedConstraint, e.OperationRequestID, e.MutationKey, e.Detail)
}

// ValidatorImpl is the step-6 DDL validator. Step 6 enforces:
//   - Key pattern validity (Contract #1 §1.1 — must parse via the
//     substrate parsers).
//   - permittedCommands when the affected DDL declares the constraint.
//   - Sensitive aspect write-scope — sensitive aspects may attach ONLY
//     to identity-typed vertices (NFR-S3).
//   - mutation.op ∈ {create, update, tombstone}.
//
// Per Contract #1 §1.5/§1.6 the permissive-by-default rule applies:
// when no DDL is found for a mutation's class, the corresponding
// schema/permittedCommands/sensitive checks are skipped (a permissive
// pass-through). Other checks (key pattern, op enum) apply regardless.
type ValidatorImpl struct {
	// *ddlResolver carries DDLs + linkReader and the shared governing-DDL /
	// instanceOf-chain resolution (step6_resolve_ddl.go) — the same
	// resolution step 6.5's encrypt and decrypt-on-read reuse via their own
	// ddlResolver, so a sensitive class resolvable only via the chain is
	// treated identically by every path that needs to know it's sensitive.
	*ddlResolver
	Logger *slog.Logger
}

// NewValidator wires a real Validator backed by the DDL cache. conn/coreBucket
// back the on-demand instanceOf reader used by the step-6 governing-DDL walk; a
// nil conn (test affordance) leaves on-demand discovery disabled.
func NewValidator(cache *DDLCache, conn *substrate.Conn, coreBucket string, logger *slog.Logger) *ValidatorImpl {
	if cache == nil {
		panic("processor: NewValidator requires DDLCache")
	}
	if logger == nil {
		logger = slog.Default()
	}
	v := &ValidatorImpl{ddlResolver: &ddlResolver{DDLs: cache, Logger: logger}, Logger: logger}
	if conn != nil && coreBucket != "" {
		v.linkReader = &connInstanceOfReader{conn: conn, coreBucket: coreBucket}
		v.classReader = &connVertexClassReader{conn: conn, coreBucket: coreBucket}
	}
	return v
}

// Validate implements Validator. Walks each mutation in result and
// returns the first DDLViolation encountered (commit path semantics:
// "any DDL violation terminates the commit path"). state carries the hydrated
// working set + on-demand KV reader so the step-6 governing-DDL walk
// (Contract #1 §1.5) can resolve a fine-grained-class vertex's type authority
// via its instanceOf chain.
func (v *ValidatorImpl) Validate(ctx context.Context, env *OperationEnvelope, result ScriptResult, state HydratedState) error {
	rid := env.RequestID
	if err := validateExternalEgressGuard(result, state, rid); err != nil {
		return err
	}
	for _, m := range result.Mutations {
		if err := v.validateOne(ctx, env, m, result, state, rid); err != nil {
			return err
		}
	}
	for _, ev := range result.Events {
		if err := v.validateEventClass(ctx, ev, result, state, rid); err != nil {
			return err
		}
	}
	v.Logger.Info("step 6: validated",
		"requestId", rid,
		"mutations", len(result.Mutations))
	return nil
}

// validateExternalEgressGuard enforces the design sensitive-param-egress
// §3.6 commit-path guard: an op that emits an `external.*`-domain event AND
// decrypted any sensitive aspect as plaintext this execution (via `reads`,
// `optionalReads`, or a lazy kv.Read not under `egressReads`) is rejected —
// sensitive data may reach an external event only as a ref via
// `contextHint.egressReads`. Scope is deliberately the external-egress plane
// only: an op emitting no external.* event may still decrypt and derive a
// value into an ordinary domain event, today's DDL-trust surface, unchanged.
func validateExternalEgressGuard(result ScriptResult, state HydratedState, rid string) error {
	tracker := state.Context.SensitiveReads
	if tracker == nil || !tracker.plaintextRead {
		return nil
	}
	for _, ev := range result.Events {
		if eventDomain(ev.Class) != "external" {
			continue
		}
		return &DDLViolation{
			ViolatedConstraint: "externalEgressSensitivePlaintext",
			OperationRequestID: rid,
			Detail: fmt.Sprintf(
				"event class %q (external-egress domain) rejected: this execution decrypted a sensitive aspect as plaintext; sensitive data may reach an external event only as a contextHint.egressReads ref",
				ev.Class),
		}
	}
	return nil
}

// validateOne enforces the per-mutation rules. Public-shape returned
// error is always *DDLViolation when violation; nil on success.
func (v *ValidatorImpl) validateOne(ctx context.Context, env *OperationEnvelope, m MutationOp, result ScriptResult, state HydratedState, rid string) error {
	// 1. op enum.
	switch m.Op {
	case "create", "update", "tombstone":
	default:
		return &DDLViolation{
			ViolatedConstraint: "opEnum",
			MutationKey:        m.Key,
			OperationRequestID: rid,
			Detail:             fmt.Sprintf("op %q not in {create, update, tombstone}", m.Op),
		}
	}

	// 2. Key pattern — must parse as vertex, aspect, or link.
	kind := substrate.ClassifyKey(m.Key)
	if kind == substrate.KindUnknown {
		return &DDLViolation{
			ViolatedConstraint: "keyPattern",
			MutationKey:        m.Key,
			OperationRequestID: rid,
			Detail:             "key does not match Contract #1 vertex/aspect/link patterns",
		}
	}

	// 2.5. Abstract type segment gate (dynamic-type-taxonomy-design.md §8 rows
	// 2-3): an abstract type names no instance, so it may not appear as a
	// key's type segment in ANY position — the vertex root, an aspect's owner,
	// or either link endpoint (an endpoint's type is a restatement of that
	// endpoint's own vertex key, so an abstract there names no vertex either).
	// Independent of the document's `class` — this checks the KEY itself.
	//
	// Exempt on tombstone: removing an instance is exactly the corrective
	// action that must stay possible once a type is declared abstract — a
	// live concrete type flipped to abstract must still be able to shed its
	// existing vertices/aspects/links. A tombstone can never CREATE an
	// instance, so exempting it preserves §8's invariant ("an abstract type
	// names no instance") rather than weakening it: the set of abstract-typed
	// instances can only ever shrink through this path, never grow.
	if m.Op != "tombstone" {
		if err := v.validateAbstractKeySegments(m.Key, kind, rid); err != nil {
			return err
		}
	}

	// 2.6. Reserved type-name gate (Contract #1 §1.2): the platform reserves
	// the type names `meta` and `op`, and no operator-defined registration may
	// claim either. §1.2 names THIS as the enforcement point ("rejected by
	// Processor at meta-DDL commit time") because it is the only one a
	// submitter cannot route around: pkgmgr's install-time check covers every
	// package-declared DDL, but a raw `core-operations` submit writing the
	// meta-vertex directly never passes through pkgmgr at all, and every write
	// to Core KV passes through here (P2 — the Processor is the sole writer).
	//
	// Exempt on tombstone, for the same reason as the abstract gates above:
	// removing a registration must stay possible — it is exactly the corrective
	// action for one that should never have existed — and a tombstone can never
	// CREATE a registration, so exempting it can only shrink the set of
	// reserved-name registrations, never grow it.
	if m.Op != "tombstone" {
		if err := validateReservedTypeRegistration(m, kind, rid); err != nil {
			return err
		}
	}

	// 3. Class derivation from document. For tombstones the document is
	// optional — if absent, skip DDL lookups.
	class := ""
	if m.Document != nil {
		if v, ok := m.Document["class"].(string); ok {
			class = v
		}
	}

	// 4. DDL-driven checks (only when a governing DDL resolves — permissive
	// default per Contract #1 §1.5/§1.6). Resolution is exact class→DDL first
	// (today's fast path), then the bounded instanceOf-chain walk to the type
	// authority for a fine-grained discriminator class that has no direct DDL.
	if class != "" {
		if ref, ok := v.resolveGoverningDDL(ctx, class, m.Key, kind, result, state); ok {
			// Abstract class gate (§8 row 4): a document's class must not
			// resolve to an abstract DDL. This is an ADDITION to Contract #1
			// §1.5's permissive default, not a contradiction — §1.5 covers "no
			// DDL found"; here one IS found and is structurally unusable (no
			// instance may ever carry it).
			//
			// Exempt on tombstone for the same reason as the segment gate
			// above: removing an instance whose class was flipped to abstract
			// is the corrective path, and a tombstone cannot create an
			// abstract-classed instance, so exempting it never weakens the
			// invariant.
			if ref.Abstract && m.Op != "tombstone" {
				return &DDLViolation{
					ViolatedConstraint: "abstractClass",
					MutationKey:        m.Key,
					OperationRequestID: rid,
					Detail: fmt.Sprintf("class %q resolves to abstract DDL meta-vertex %q — an abstract type names no instance and may not be written",
						class, ref.MetaVertexKey),
				}
			}

			// permittedCommands enforcement: when the DDL declares a
			// non-empty list, the operation envelope's operationType
			// must appear in it.
			if len(ref.PermittedCommands) > 0 {
				if !stringInSlice(env.OperationType, ref.PermittedCommands) {
					return &DDLViolation{
						ViolatedConstraint: "permittedCommands",
						MutationKey:        m.Key,
						OperationRequestID: rid,
						Detail: fmt.Sprintf("operationType %q not permitted by DDL meta-vertex %q (permittedCommands %v)",
							env.OperationType, ref.MetaVertexKey, ref.PermittedCommands),
					}
				}
			}

			// Sensitive aspect write-scope (NFR-S3), CONDITIONAL ON THE
			// DECLARED CUSTODY KIND (retention-class-key-custody-design.md
			// §4.1). Only meaningful for aspect mutations.
			if ref.Sensitive && kind == substrate.KindAspect {
				_, parentType, _, _, ok := substrate.ParseAspectKey(m.Key)
				if !ok {
					return &DDLViolation{
						ViolatedConstraint: "keyPattern",
						MutationKey:        m.Key,
						OperationRequestID: rid,
						Detail:             "aspect key failed to parse",
					}
				}
				if err := validateSensitiveCustody(ref, m.Key, parentType, rid); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// validateSensitiveCustody enforces the sensitive-aspect anchoring rule for
// the DDL's declared custody kind (retention-class-key-custody-design.md
// §4.1). The fail-closed default is the whole point of the shape: a package
// that flips Sensitive:true on an appointment-anchored aspect and forgets to
// declare custody is rejected, NOT granted plaintext at rest.
func validateSensitiveCustody(ref MetaVertexRef, mutationKey, parentType, rid string) error {
	switch ref.CustodyKind {
	case "", CustodyKindIdentity:
		// The DEK belongs to the anchoring identity, so the anchor must BE an
		// identity or there is no holder to custody it.
		if parentType != "identity" {
			return &DDLViolation{
				ViolatedConstraint: "sensitiveAspectScope",
				MutationKey:        mutationKey,
				OperationRequestID: rid,
				Detail: fmt.Sprintf("sensitive aspect %q may only attach to identity vertices; parent type is %q",
					ref.CanonicalName, parentType),
			}
		}
		return nil

	case CustodyKindRetentionClass:
		// Any anchor is permitted — the holder is the declared class, not the
		// parent. What IS checked is that the install actually resolved a
		// holder of the right type. This validates a value the INSTALL
		// produced, never caller input, so it can only fail if a DDL was
		// written or migrated wrong; failing closed keeps a malformed custody
		// declaration from committing plaintext-adjacent state under a key
		// step 6.5 could not mint.
		holder := ref.CustodyHolderKey
		holderType, _, ok := substrate.ParseVertexKey(holder)
		if !ok || holderType != RetentionClassVertexType {
			return &DDLViolation{
				ViolatedConstraint: "sensitiveAspectScope",
				MutationKey:        mutationKey,
				OperationRequestID: rid,
				Detail: fmt.Sprintf("sensitive aspect %q declares custody kind %q but its holder key %q is not a vtx.%s.<NanoID> vertex",
					ref.CanonicalName, CustodyKindRetentionClass, holder, RetentionClassVertexType),
			}
		}
		return nil

	default:
		return &DDLViolation{
			ViolatedConstraint: "sensitiveAspectScope",
			MutationKey:        mutationKey,
			OperationRequestID: rid,
			Detail: fmt.Sprintf("sensitive aspect %q declares unknown custody kind %q",
				ref.CanonicalName, ref.CustodyKind),
		}
	}
}

// validateAbstractKeySegments rejects a mutation whose key uses an abstract
// type (dynamic-type-taxonomy-design.md §8 rows 2-3) as a type segment in any
// of the four positions a key can carry one: a vertex root, an aspect's owner
// type, or either link endpoint. Uses the substrate key parsers rather than
// hand-splitting, per the design's own instruction. A type segment that
// resolves to no DDL at all (byName miss) is not an abstract-type violation —
// that is Contract #1 §1.5's ordinary permissive default, unrelated to this
// gate.
//
// UNREACHABILITY CAVEAT: each switch case below only appends to segments
// when its Parse* call reports ok == true; on ok == false, segments stays
// empty and this function falls through to ALLOW. That fall-through is safe
// today only because the caller already ran substrate.ClassifyKey (rejecting
// KindUnknown) before calling here, and ClassifyKey enforces the IDENTICAL
// segment predicates ParseVertexKey/ParseAspectKey/ParseLinkKey use — so a
// key already classified as KindVertex/KindAspect/KindLink cannot fail its
// matching Parse* call as the code stands. If ClassifyKey and the Parse*
// functions are ever allowed to diverge, this fall-through silently REOPENS
// the gate instead of rejecting — unlike the sensitive-aspect check below
// (validateOne's ParseAspectKey call), which returns a *DDLViolation* on the
// same shape of parse failure rather than falling through to permit.
func (v *ValidatorImpl) validateAbstractKeySegments(key string, kind substrate.KeyKind, rid string) error {
	var segments []string
	switch kind {
	case substrate.KindVertex:
		if t, _, ok := substrate.ParseVertexKey(key); ok {
			segments = append(segments, t)
		}
	case substrate.KindAspect:
		if _, t, _, _, ok := substrate.ParseAspectKey(key); ok {
			segments = append(segments, t)
		}
	case substrate.KindLink:
		if t1, _, _, t2, _, ok := substrate.ParseLinkKey(key); ok {
			segments = append(segments, t1, t2)
		}
	}
	for _, seg := range segments {
		if ref, ok := v.DDLs.Lookup(seg); ok && ref.Abstract {
			return &DDLViolation{
				ViolatedConstraint: "abstractTypeSegment",
				MutationKey:        key,
				OperationRequestID: rid,
				Detail: fmt.Sprintf("key type segment %q resolves to abstract DDL meta-vertex %q — an abstract type names no instance and may not appear in a key",
					seg, ref.MetaVertexKey),
			}
		}
	}
	return nil
}

// metaTypeSegment is the type segment every meta-vertex key carries
// (Contract #1 §1.2) — the position under which every registered name lives.
const metaTypeSegment = "meta"

// metaKeyPrefix leads every meta-vertex key and every aspect hanging off one.
// The reserved-name gate tests it first so the overwhelming majority of
// mutations — business vertices, which never touch a meta-vertex — cost one
// byte compare instead of a key parse.
const metaKeyPrefix = substrate.VertexPrefix + "." + metaTypeSegment + "."

// canonicalNameLocalName is the aspect local name carrying a meta-vertex's
// lookup name (Contract #1 §1.7). The DDL cache reads the registration by
// this KEY (`<root>.canonicalName`), never by the aspect document's own
// class, so the gate below keys off the same segment the reader does.
const canonicalNameLocalName = "canonicalName"

// validateReservedTypeRegistration rejects a mutation that would make a live
// meta-vertex's canonical name read as one of Contract #1 §1.2's reserved type
// names (`meta`, `op`).
//
// The reservation is on the NAME AS INDEXED, not on any particular flavor of
// meta-vertex, and that is what the readers demand. DDLCache.Refresh indexes
// EVERY meta-vertex carrying a canonicalName into `byName` with no filter on
// class, and validateAbstractKeySegments resolves a key's type segment through
// DDLs.Lookup(segment) with no filter on Kind — so a lens, a weaverTarget or a
// pane named "meta" is every bit as much a type-segment authority as a
// vertexType DDL is. A carve-out for "it's only a lens" would hand back the
// whole rule: name a lens `meta`, mark it abstract, and the abstract
// key-segment gate then refuses every non-tombstone mutation on every
// `vtx.meta.*` key, which is every package install there will ever be.
//
// Both shapes that can carry the name are covered, because loadMetaVertex reads
// both and either one alone is a complete registration:
//
//	(1) the ASPECT `vtx.meta.<NanoID>.canonicalName`, whose `data.value` is
//	    the name — the shape pkgmgr's install batch writes, and the one the
//	    reader PREFERS; and
//	(2) the ROOT `vtx.meta.<NanoID>` itself, whose `data.canonicalName` the
//	    reader falls back to when no live aspect supplies a name.
//
// Gating only the shape pkgmgr happens to emit would leave the other as a
// one-mutation bypass. Shape (2) is gated unconditionally, never "only when no
// canonicalName aspect exists": the aspect that outranks it today can be
// tombstoned tomorrow — and a tombstone is exempt here — at which point the
// reader's fallback promotes the root's field to the live registration with no
// remaining mutation to gate.
func validateReservedTypeRegistration(m MutationOp, kind substrate.KeyKind, rid string) error {
	if !strings.HasPrefix(m.Key, metaKeyPrefix) || m.Document == nil {
		return nil
	}
	// The prefix test above already fixes the type segment as `meta`; what each
	// arm still needs from the key is which registration FIELD the document
	// carries the name in.
	var name, field string
	switch kind {
	case substrate.KindVertex:
		name, field = documentDataString(m.Document, canonicalNameLocalName), "data."+canonicalNameLocalName
	case substrate.KindAspect:
		_, _, _, localName, ok := substrate.ParseAspectKey(m.Key)
		if !ok || localName != canonicalNameLocalName {
			return nil
		}
		name, field = documentDataString(m.Document, "value"), "data.value"
	default:
		return nil
	}
	if !substrate.IsReservedTypeName(name) {
		return nil
	}
	return &DDLViolation{
		ViolatedConstraint: "reservedVertexTypeName",
		MutationKey:        m.Key,
		OperationRequestID: rid,
		Detail: fmt.Sprintf("meta-vertex registration declares %s %q — that is a reserved platform type name and no meta-vertex may be registered under it (Contract #1 §1.2)",
			field, name),
	}
}

// documentDataString reads a string field out of a mutation document's `data`
// map, returning "" when the document carries no `data` object, no such field,
// or a non-string value. A non-string is the same as absent here on purpose:
// every reader of these fields (ddl_cache.go's loadMetaVertex) type-asserts to
// string and ignores anything else, so a value no reader will ever treat as a
// registered name is not one this gate should reject on either.
func documentDataString(doc map[string]interface{}, field string) string {
	data, ok := doc["data"].(map[string]interface{})
	if !ok {
		return ""
	}
	s, _ := data[field].(string)
	return s
}

// validateEventClass rejects an event whose class resolves to an abstract DDL
// (dynamic-type-taxonomy-design.md §8 row 4, extended to events): an abstract
// type names no instance, and an event's `class` names a type exactly the way
// a document's `class` does, so it is gated by the same reasoning.
//
// Resolution goes through resolveGoverningDDL, but only its exact
// class→DDL fast path (DDLCache.Lookup) ever runs for an event: an event
// carries no key, so kind is passed as substrate.KindUnknown, and
// vertexRootForResolve has no case for it — it falls through to `return ""`.
// resolveWithFault then bails at its `root == ""` check
// (step6_resolve_ddl.go) before the instanceOf-chain walk that lets a
// fine-grained discriminator class resolve to its type authority ever runs.
// That degradation is sound on its own terms — no key means nothing to walk,
// no live read is attempted, no fault is recorded — but it is a REAL
// asymmetry, not a detail to gloss over: a class that resolves to an
// abstract DDL only via the chain (not the exact lookup) is rejected on the
// mutation-side gate above and ACCEPTED here.
//
// The gate is additionally unreachable today, independent of the above: an
// abstract DDL's CanonicalName must pass keys.IsValidTypeSegment
// (abstractscope.go) — always dot-free — while BuildEventList
// (step7_events.go) rejects any dot-free event class outright (Contract #3
// §3.4 requires `<domain>.<eventName>`). A census of every event class under
// packages/** found all of them dotted, none dot-free, so no event class
// today can equal a registered abstract DDL's name. It stays as a
// fail-closed defense-in-depth latch, not a live gate: were a dot-free event
// class ever authored that collided with an abstract DDL's name, this
// terminates the op cleanly here (a terminal DDLViolation reject) rather
// than letting it fall through into BuildEventList's generic
// "not <domain>.<eventName>" error, which the commit path's genuine-failure
// fallback treats as retryable (Nak + redelivery) — a hot-loop against a
// script that will never stop producing the same malformed class.
//
// No tombstone exemption arises here: the mutation-side exemptions exist
// because a tombstone (MutationOp.Op == "tombstone") can only ever REMOVE an
// instance, never create one, so exempting it can't grow the abstract-typed
// instance set. An EventSpec carries no op and creates no instance at all —
// publishing it is a pure announcement, not a write — so there is no
// corrective-removal case for an exemption to preserve; every event class is
// checked, unconditionally.
func (v *ValidatorImpl) validateEventClass(ctx context.Context, ev EventSpec, result ScriptResult, state HydratedState, rid string) error {
	if ev.Class == "" {
		return nil
	}
	ref, ok := v.resolveGoverningDDL(ctx, ev.Class, "", substrate.KindUnknown, result, state)
	if !ok || !ref.Abstract {
		return nil
	}
	return &DDLViolation{
		ViolatedConstraint: "abstractEventClass",
		OperationRequestID: rid,
		Detail: fmt.Sprintf("event class %q resolves to abstract DDL meta-vertex %q — an abstract type names no instance and may not be published as an event class",
			ev.Class, ref.MetaVertexKey),
	}
}

func stringInSlice(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// hasMetaVertexMutation returns true when any mutation in the batch
// targets a `vtx.meta.*` key. Used by the Committer to decide whether
// to invalidate the DDL cache after a successful commit. Exported for
// the Committer's use.
func hasMetaVertexMutation(muts []MutationOp) bool {
	for _, m := range muts {
		if strings.HasPrefix(m.Key, "vtx.meta.") {
			return true
		}
	}
	return false
}
