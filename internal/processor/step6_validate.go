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

// ResolveFaultError is the step-6 failure raised when a governing-DDL
// resolution could not complete because a live Core KV read faulted — the
// resolution is DEGRADED, not empty, and the two must not be confused where a
// gate depends on the answer.
//
// It is retryable, never a refusal: a transient blip must not permanently
// reject a valid operation, and it must not fall through to the permissive
// default either, which would let a fault decide that an entity is ungoverned.
// The commit path redelivers on the backoff floor, exactly as it does for a
// step-8 read fault. DDLViolation stays reserved for a verdict — a DDL
// resolved and its permittedCommands omits the operation, or the stored body
// is corrupt.
type ResolveFaultError struct {
	MutationKey        string
	Class              string
	OperationRequestID string
	Cause              error
}

func (e *ResolveFaultError) Error() string {
	return fmt.Sprintf("step 6: governing-DDL resolution for class %q faulted: requestId=%s mutationKey=%s: %v",
		e.Class, e.OperationRequestID, e.MutationKey, e.Cause)
}

func (e *ResolveFaultError) Unwrap() error { return e.Cause }

// ValidatorImpl is the step-6 DDL validator. Step 6 enforces:
//   - Key pattern validity (Contract #1 §1.1 — must parse via the
//     substrate parsers).
//   - `class`, wherever a written document carries one, is a JSON string.
//   - permittedCommands against the class a document DECLARES, and — for an
//     update or a tombstone — against the class STORED at the key, which is
//     the class of the entity the mutation rewrites or removes.
//   - Sensitive aspect write-scope — sensitive aspects may attach ONLY
//     to identity-typed vertices (NFR-S3).
//   - mutation.op ∈ {create, update, tombstone}.
//
// Per Contract #1 §1.5/§1.6 the permissive-by-default rule applies:
// when no DDL is found for a mutation's class, the corresponding
// schema/permittedCommands/sensitive checks are skipped (a permissive
// pass-through). Other checks (key pattern, op enum) apply regardless.
//
// Two failure shapes leave Validate, and the commit path disposes them
// differently. A *DDLViolation is a VERDICT the world cannot change — a DDL
// resolved and refused the mutation, a key does not parse, a written class is
// not a string, or an update is rewriting a stored body whose class the gate
// cannot read — and terminates the operation. A *ResolveFaultError is a
// DEGRADED resolution: a live Core KV read faulted, so the answer is unknown
// rather than empty, and the operation is redelivered.
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
// via its instanceOf chain. prior carries the documents stored at the mutation
// keys, read at step 5.5, from which an update's or tombstone's STORED class —
// the one that governs the entity being rewritten or removed — is taken.
func (v *ValidatorImpl) Validate(ctx context.Context, env *OperationEnvelope, result ScriptResult, state HydratedState, prior PriorDocs) error {
	rid := env.RequestID
	if verr := validateMutationKeyShapes(result.Mutations, rid); verr != nil {
		return verr
	}
	if err := validateMutationBooleanFields(result.Mutations, rid); err != nil {
		return err
	}
	if err := validateExternalEgressGuard(result, state, rid); err != nil {
		return err
	}
	for _, m := range result.Mutations {
		if err := v.validateOne(ctx, env, m, result, state, prior, rid); err != nil {
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

// validateMutationKeyShapes is the batch-wide pre-pass the commit path runs at
// step 5.5, BEFORE the prior-document read and therefore before any Core KV
// round trip the mutation set can provoke. Two rules:
//
//   - every mutation key must parse as a vertex, aspect, or link key
//     (Contract #1 §1.1), refused `keyPattern` exactly as the per-mutation
//     check below refuses it. Step 6 is the only mutation-key gate — the
//     Starlark runner builds a MutationOp with no shape check — and the prior
//     read hands a key straight to KVGet, which answers a malformed one with an
//     error. Read as a read fault that would turn a terminal refusal into an
//     unbounded redelivery loop, so the shape verdict must be reached first.
//   - a `class` field, when present, must be a JSON string. A Go type assertion
//     reads any other JSON type as absent, so `{"class": 7}` would otherwise
//     commit and then read as classless forever — ungoverned by the very gate
//     that reads the stored class back (§2.1), with no later mutation able to
//     restore the governance.
//
// Batch-wide rather than per-mutation for the same reason as the boolean
// pre-pass below: a later mutation's malformed value is observable to an
// earlier mutation's DDL resolution before the loop reaches its own turn.
//
// The return type is the concrete *DDLViolation, not error: every failure this
// pre-pass can reach is a terminal verdict, so the step-5.5 caller disposes it
// as a rejection with no type switch and no fallback branch to keep correct.
func validateMutationKeyShapes(mutations []MutationOp, rid string) *DDLViolation {
	for _, m := range mutations {
		if substrate.ClassifyKey(m.Key) == substrate.KindUnknown {
			return &DDLViolation{
				ViolatedConstraint: "keyPattern",
				MutationKey:        m.Key,
				OperationRequestID: rid,
				Detail:             "key does not match Contract #1 vertex/aspect/link patterns",
			}
		}
		if m.Document == nil {
			continue
		}
		if cv, present := m.Document["class"]; present {
			if _, ok := cv.(string); !ok {
				return &DDLViolation{
					ViolatedConstraint: "classType",
					MutationKey:        m.Key,
					OperationRequestID: rid,
					Detail:             fmt.Sprintf("class must be a JSON string, got %T", cv),
				}
			}
		}
	}
	return nil
}

// validateMutationBooleanFields is a batch-wide pre-pass, run before any
// per-mutation resolution: every mutation's isDeleted, and every mutation's
// data.protected / data.sensitive, if present, must each be a JSON bool. A Go
// type assertion silently reads any other JSON type as false (ok=false), so a
// malformed value would otherwise commit and read as "live" to
// mutationTombstoned (step6_resolve_ddl.go, for isDeleted), docIsProtected
// (step8_commit.go, for data.protected), and the DDL-root sensitive fallback
// (ddl_cache.go, for data.sensitive) — the last of which skips step 6.5
// encryption and commits plaintext. Meanwhile internal/refractor's strict
// typed decoders DLQ or hard-fail on the same stored isDeleted value: a worse
// outcome than any revival this closes. rejectPermissionRoleRewrites does not
// read isDeleted at all (it explicitly excludes it) and is not covered by
// this gate.
//
// This runs as a batch-wide PRE-pass, not folded into validateOne's per-
// mutation loop, because step 6's own DDL resolution (classOf,
// reconcileBatchInstanceOf) reads OTHER mutations in the same batch — a
// later mutation's malformed field could otherwise be observed by an
// earlier mutation's resolution before its own turn in the loop reaches it.
func validateMutationBooleanFields(mutations []MutationOp, rid string) error {
	for _, m := range mutations {
		if m.Document == nil {
			continue
		}
		if v, present := m.Document["isDeleted"]; present {
			if _, ok := v.(bool); !ok {
				return &DDLViolation{
					ViolatedConstraint: "isDeletedType",
					MutationKey:        m.Key,
					OperationRequestID: rid,
					Detail:             fmt.Sprintf("isDeleted must be a JSON bool, got %T", v),
				}
			}
		}
		data, ok := m.Document["data"].(map[string]interface{})
		if !ok {
			continue
		}
		for _, field := range [...]string{"protected", "sensitive"} {
			fv, present := data[field]
			if !present {
				continue
			}
			if _, ok := fv.(bool); !ok {
				return &DDLViolation{
					ViolatedConstraint: field + "Type",
					MutationKey:        m.Key,
					OperationRequestID: rid,
					Detail:             fmt.Sprintf("data.%s must be a JSON bool, got %T", field, fv),
				}
			}
		}
	}
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

// validateOne enforces the per-mutation rules. It returns nil on success, a
// *DDLViolation for a verdict (the mutation is refused and the operation
// terminates), or a *ResolveFaultError when a governing-DDL resolution the
// verdict depends on was degraded by a live-read fault (the operation is
// redelivered — the answer is unknown, not empty).
func (v *ValidatorImpl) validateOne(ctx context.Context, env *OperationEnvelope, m MutationOp, result ScriptResult, state HydratedState, prior PriorDocs, rid string) error {
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

	// 3. Stored-class governance: an update or a tombstone is governed by the
	// DDL of the class stored at the key — the entity it rewrites or removes —
	// whatever the script's own document declares, or declares nothing at all.
	if m.Op == "update" || m.Op == "tombstone" {
		if err := v.validateStoredClass(ctx, env, m, kind, result, state, prior, rid); err != nil {
			return err
		}
	}

	// 4. Class derivation from document. For tombstones the document is
	// optional — if absent, skip DDL lookups.
	class := ""
	if m.Document != nil {
		if v, ok := m.Document["class"].(string); ok {
			class = v
		}
	}

	// 5. DDL-driven checks on the DECLARED class (only when a governing DDL
	// resolves — permissive default per Contract #1 §1.5/§1.6). Resolution is
	// exact class→DDL first (today's fast path), then the bounded
	// instanceOf-chain walk to the type authority for a fine-grained
	// discriminator class that has no direct DDL.
	if class != "" {
		ref, ok, err := v.resolveDeclaredClass(ctx, m, class, kind, result, state, rid)
		if err != nil {
			return err
		}
		if ok {
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

// validateStoredClass enforces permittedCommands against the class of the
// document STORED at an update's or tombstone's key (Contract #1 §1.5). The
// class a script proposes governs what it writes; the class already there
// governs what it rewrites or removes — and a bare tombstone proposes nothing
// at all, so the stored class is the only class it has.
//
// Absent and corrupt are different states, and only absent is permissive:
//
//   - the key holds nothing → resolves nothing, permissive. This is today's
//     behaviour for a tombstone of an absent key, which the meta-vertex
//     tombstone cascade relies on.
//   - the entry exists but did not decode, or its `class` is present and not a
//     string → an UPDATE of it is refused; a TOMBSTONE of it is admitted. See
//     refuseUnreadableStoredClass.
//   - `class` absent or "" → resolves nothing, permissive (Contract #1 §1.5,
//     "No default class").
//
// Only permittedCommands is enforced on the stored class. The abstract-class
// and sensitive-custody gates read what is being WRITTEN and stay exempt on a
// tombstone for the reasons the key-segment gates give above.
func (v *ValidatorImpl) validateStoredClass(ctx context.Context, env *OperationEnvelope, m MutationOp, kind substrate.KeyKind, result ScriptResult, state HydratedState, prior PriorDocs, rid string) error {
	doc, found, decoded := prior.lookup(m.Key)
	if !found {
		return nil
	}
	if !decoded {
		return refuseUnreadableStoredClass(m, rid, "the document stored at this key did not decode")
	}
	raw, present := doc["class"]
	if !present {
		return nil
	}
	stored, ok := raw.(string)
	if !ok {
		return refuseUnreadableStoredClass(m, rid,
			fmt.Sprintf("the document stored at this key carries a non-string class (%T)", raw))
	}
	if stored == "" {
		return nil
	}

	// The stored class's type authority is a fact about the committed graph:
	// resolveCommittedOnly keeps this batch from un-typing the entity by
	// tombstoning its own instanceOf link, and the nil live-read budget keeps
	// the walk off the script's own allowance — a gate whose subject is
	// computed from submitter-supplied input is not a gate if the submitter can
	// exhaust it.
	ref, ok, fault := v.resolveGoverningDDLCommitted(ctx, stored, m.Key, kind, result, offBudget(state))
	if fault != nil && !ok {
		// Both conditions are required. A fault that still ended in a definitive
		// DDL — a later hop of the walk resolved one — told the gate exactly what
		// it needed, so it is immaterial and must not fail an otherwise valid
		// write. Only an EMPTY resolution behind a fault is no answer at all, and
		// that one must not reach the permissive default.
		return &ResolveFaultError{MutationKey: m.Key, Class: stored, OperationRequestID: rid, Cause: fault}
	}
	if !ok || len(ref.PermittedCommands) == 0 {
		return nil
	}
	if !stringInSlice(env.OperationType, ref.PermittedCommands) {
		return &DDLViolation{
			ViolatedConstraint: "permittedCommands",
			MutationKey:        m.Key,
			OperationRequestID: rid,
			Detail: fmt.Sprintf("operationType %q not permitted by DDL meta-vertex %q (permittedCommands %v) governing the stored class %q at this key",
				env.OperationType, ref.MetaVertexKey, ref.PermittedCommands, stored),
		}
	}
	return nil
}

// refuseUnreadableStoredClass disposes a mutation over a stored body whose
// class the gate cannot read — an entry that did not decode, or one whose
// `class` is present and not a string.
//
// An UPDATE is refused: rewriting content the gate cannot read is not a write
// anything governs, and admitting it would let one unreadable body launder
// arbitrary rewrites of the entity forever.
//
// A TOMBSTONE is admitted, and that is the heal path. It carries no document,
// and the batch builder writes no readable content forward for it, so there is
// nothing to launder through the removal; refusing it instead would make an
// already-corrupt key permanently unremovable through the operation plane —
// the only write plane there is. This is the same doctrine that keeps the
// kernel's own protected links revivable rather than frozen.
//
// The constraint is `storedClass`, distinct from `permittedCommands`, so a
// consumer can tell "the DDL refused this operation" from "the gate could not
// read the entity" on the wire.
func refuseUnreadableStoredClass(m MutationOp, rid, what string) error {
	if m.Op != "update" {
		return nil
	}
	return &DDLViolation{
		ViolatedConstraint: "storedClass",
		MutationKey:        m.Key,
		OperationRequestID: rid,
		Detail:             what + ", so the class governing a rewrite of it cannot be read",
	}
}

// resolveDeclaredClass resolves the governing DDL of the class a mutation's own
// document declares.
//
// On an `update` the walk runs off the script's live-read budget and reports a
// read fault instead of swallowing it: the class an update declares may DIFFER
// from the one stored (Contract #1 §1.3 makes `class` mutable), so this is the
// conjunct that keeps a re-typing inside the new class's own permittedCommands
// — and a conjunct a submitter can switch off by spending its own budget, or by
// making a read fault, is not one. A `create` keeps the budgeted, fail-open
// walk: its type authority lives in the batch it is submitting, and the
// exact-miss read is its own cost.
func (v *ValidatorImpl) resolveDeclaredClass(ctx context.Context, m MutationOp, class string, kind substrate.KeyKind, result ScriptResult, state HydratedState, rid string) (MetaVertexRef, bool, error) {
	if m.Op != "update" {
		ref, ok := v.resolveGoverningDDL(ctx, class, m.Key, kind, result, state)
		return ref, ok, nil
	}
	ref, ok, fault := v.resolveGoverningDDLChecked(ctx, class, m.Key, kind, result, offBudget(state))
	if fault != nil && !ok {
		// A fault behind a DEFINITIVE resolution is immaterial: a later hop
		// answered the question, so failing the write would reject a valid
		// operation over a read the gate did not end up needing.
		return MetaVertexRef{}, false, &ResolveFaultError{MutationKey: m.Key, Class: class, OperationRequestID: rid, Cause: fault}
	}
	return ref, ok, nil
}

// offBudget returns state with the script's live-read budget detached, so a
// resolution the gate performs on its own behalf neither charges the script's
// allowance nor can be starved by it (a nil tracker is unlimited). Everything
// else — the hydrated working set, the resolution memo — is shared, so the
// off-budget walk still warms and reuses the same per-execution answers.
func offBudget(state HydratedState) HydratedState {
	state.Context.LiveReads = nil
	return state
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
