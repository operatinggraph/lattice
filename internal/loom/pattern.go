package loom

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Step kinds (Contract #10 §10.5). A systemOp step submits its bound op
// directly; a userTask step submits CreateTask and waits for the user to
// perform the bound op (auto-completing the task); an externalTask step
// submits its instanceOp (which mints a claim vertex and emits the
// external.<adapter> event off its own outbox) and parks for the bridge's
// replyOp.
const (
	StepKindSystemOp     = "systemOp"
	StepKindUserTask     = "userTask"
	StepKindExternalTask = "externalTask"
)

// Step is one entry in a pattern's linear step list (Contract #10 §10.5).
// systemOp/userTask carry the `{kind, operation, guard?}` shape — `operation`
// names the bound op. externalTask carries the `{kind, adapter, params,
// replyOp, instanceOp, guard?}` shape and leaves `operation` unused: its op
// vocabulary is instanceOp (the op the engine submits, which mints the claim
// vertex) and replyOp (the result-op the bridge posts back). Guards apply to
// any kind.
type Step struct {
	Kind      string          `json:"kind"`
	Operation string          `json:"operation,omitempty"`
	Guard     json.RawMessage `json:"guard,omitempty"`

	// Adapter is the external adapter name an externalTask dispatches to. It is
	// carried into the instanceOp payload and rides the external.<adapter> event
	// the instanceOp's outbox emits; the bridge selects its adapter from it.
	Adapter string `json:"adapter,omitempty"`
	// Params are the externalTask's adapter parameters — free-form templates
	// opaque to the engine, passed through verbatim into the instanceOp payload.
	Params json.RawMessage `json:"params,omitempty"`
	// ReplyOp is the result-op type the bridge posts back for an externalTask
	// (carrying payload.externalRef); its DDL records the external outcome as
	// aspect(s) on the claim vertex (D5). Carried into the instanceOp payload.
	ReplyOp string `json:"replyOp,omitempty"`
	// InstanceOp is the op an externalTask step submits: its DDL mints the claim
	// vertex (with the caller-supplied instance handle) and emits the
	// external.<adapter> event via its own transactional outbox.
	InstanceOp string `json:"instanceOp,omitempty"`

	// Reads and OptionalReads are the Contract #2 §2.5 read-sets a systemOp
	// step's bound op needs hydrated, declared as subject-relative templates
	// (`subject` or `subject.<aspect>`) because a pattern is authored long before
	// any subject exists. submitSystemOp resolves them against inst.SubjectKey.
	//
	// systemOp-only, enforced by validate: a userTask's read-set is derived by
	// the engine from the CreateTask invariant (userTaskReads) and an
	// externalTask's from its declared params (inferExternalTaskReads), so a
	// declared set on either kind would be silently ignored — the pattern is
	// rejected instead, the same doctrine as a kind carrying a foreign field.
	Reads         []string `json:"reads,omitempty"`
	OptionalReads []string `json:"optionalReads,omitempty"`

	// Enumerations are the Contract #2 §2.5 class-(e) link walks a systemOp
	// step's bound op runs (`kv.Links`), declared so the envelope carries the
	// walk as metadata. Each Hub is a subject-relative template (`subject` or
	// `subject.<aspect>`), resolved against inst.SubjectKey at submit time
	// exactly like Reads.
	//
	// Metadata, not a hydration directive: the enumeration stays a bounded
	// paged live read inside the script, and the Processor validates the
	// declaration's shape and otherwise ignores it. What the declaration buys
	// is that the walk is visible on the envelope rather than knowable only by
	// reading the script.
	//
	// systemOp-only, enforced by validate, on the same doctrine as Reads: a
	// userTask's and an externalTask's op are engine-chosen, so the engine —
	// not the pattern — knows what they enumerate.
	Enumerations []Enumeration `json:"enumerations,omitempty"`
}

// Enumeration is one declared kv.Links link-enumeration (Contract #2 §2.5 —
// `contextHint.enumerations`): the hub vertex the walk starts from, the link
// relation walked, and the direction the hub sits in the link ("out" = hub is
// the link source, "in" = hub is the target). Hub carries a subject-relative
// template on a Step and the resolved concrete key on an outbox record and on
// the wire, the same dual life Reads' plain strings have.
type Enumeration struct {
	Hub       string `json:"hub"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
}

// Link directions an Enumeration may declare (Contract #2 §2.5): the hub is
// either the link's source or its target. The Processor rejects any other
// value at envelope parse, so pattern load holds the same two.
const (
	enumerationOut = "out"
	enumerationIn  = "in"
)

// subjectToken is the root of a step's declared-read template grammar: the bare
// token names the instance subject's vertex, `subject.<aspect>` one of its
// aspects.
const subjectToken = "subject"

// resolveSubjectTemplate renders one declared-read template against a concrete
// subject key. `subject` → the root key; `subject.<aspect>` → the 4-segment
// aspect key `vtx.<type>.<id>.<aspect>` (Contract #1) — the same key shape
// userTaskOptionalReads builds for the assignee's `.availability`. Templates are
// validated at pattern load, so an unparseable one never reaches here.
func resolveSubjectTemplate(tmpl, subjectKey string) string {
	if tmpl == subjectToken {
		return subjectKey
	}
	return subjectKey + strings.TrimPrefix(tmpl, subjectToken)
}

// rejectDeclaredReads refuses a declared read-set on a kind whose read-set the
// engine derives for itself. Silently ignoring the declaration would be the
// worse outcome: an author who believes a key is hydrated writes a script that
// reads it, and the miss surfaces as a hydration failure at run time rather
// than a pattern that never loaded.
func rejectDeclaredReads(patternID string, idx int, s Step, because string) error {
	for _, f := range []struct {
		name    string
		entries []string
	}{{"reads", s.Reads}, {"optionalReads", s.OptionalReads}} {
		if len(f.entries) != 0 {
			return fmt.Errorf("pattern %q step %d: %s is a systemOp-only field, not permitted on a %s step (%s)",
				patternID, idx, f.name, s.Kind, because)
		}
	}
	if len(s.Enumerations) != 0 {
		return fmt.Errorf("pattern %q step %d: enumerations is a systemOp-only field, not permitted on a %s step (%s)",
			patternID, idx, s.Kind, because)
	}
	return nil
}

// validateEnumerations checks a systemOp step's declared link walks. Hub runs
// through the same subject-relative grammar as a declared read (the hub is a
// key, and the same charset argument applies once it is rendered), and the
// relation/direction pair is held to the shape the Processor's envelope parse
// enforces — hub and relation non-empty, direction one of "out"/"in". Holding
// it here means a pattern whose declaration the Processor would reject
// terminally on every redelivery never loads.
func validateEnumerations(patternID string, idx int, entries []Enumeration) error {
	for i, e := range entries {
		if err := validateSubjectTemplates(patternID, idx, fmt.Sprintf("enumerations[%d].hub", i), []string{e.Hub}); err != nil {
			return err
		}
		if strings.TrimSpace(e.Relation) == "" {
			return fmt.Errorf("pattern %q step %d: enumerations[%d] requires a relation", patternID, idx, i)
		}
		if e.Direction != enumerationOut && e.Direction != enumerationIn {
			return fmt.Errorf("pattern %q step %d: enumerations[%d] direction must be %q or %q, got %q",
				patternID, idx, i, enumerationOut, enumerationIn, e.Direction)
		}
	}
	return nil
}

// validateSubjectTemplates checks one declared read-set against the grammar
// resolveSubjectTemplate implements. Every entry must be subject-relative: the
// subject is the only key a pattern definition can name at authoring time, so a
// literal key is an authoring error rather than a shortcut — it would pin one
// instance's data into the definition every instance of the pattern shares.
//
// The aspect segment is held to the full Contract #1 localName, not merely
// "non-empty and dot-free". A rendered key travels to the Processor as a
// ContextHint read and is fetched with a NATS KV GET, whose key charset is
// narrower than "any string without a dot": a space, `*`, `>` or a non-ASCII
// byte yields ErrInvalidKey rather than ErrKeyNotFound, which is not the
// absence branch — it is a hard hydration error that every redelivery
// reproduces, so the step wedges and the instance rides its deadline to a
// failed terminal. Catching the charset here is the difference between a
// pattern that never installs and a pattern that installs and runs dark.
func validateSubjectTemplates(patternID string, idx int, field string, entries []string) error {
	for _, e := range entries {
		if e == subjectToken {
			continue
		}
		aspect, ok := strings.CutPrefix(e, subjectToken+".")
		if !ok {
			return fmt.Errorf("pattern %q step %d: %s entry %q must be %q or %q — a step's reads are subject-relative templates",
				patternID, idx, field, e, subjectToken, subjectToken+".<aspect>")
		}
		if !substrate.IsValidLocalName(aspect) {
			return fmt.Errorf("pattern %q step %d: %s entry %q names %q, which is not a Contract #1 aspect localName",
				patternID, idx, field, e, aspect)
		}
	}
	return nil
}

// Pattern is the in-engine view of a meta.loomPattern definition. A pattern
// declares a single subjectType (the vertex type an instance runs for) and a
// linear list of steps. PatternID is the spec-declared identifier — a
// human-readable name when the spec supplies one, otherwise the source vertex's
// NanoID. MetaKey is the real vtx.meta.<NanoID> vertex key, carried separately
// so a dispatched op references the meta-vertex by its canonical key rather than
// reconstructing it from the (possibly human-named) PatternID.
//
// completionDomains is the set of events.<domain>.> the engine reconciles a
// durable per-domain completion consumer for (D2). It defaults to the pattern's
// subjectType: a pattern over `identity` subjects completes on
// `events.identity.>`. A flow whose steps complete in a domain other than the
// subject's lists it explicitly. The engine reads completionDomains — it does
// not infer domains from operation names; correlation is domain-independent
// (Contract #10 §10.6), so the SET of domains is sufficient.
type Pattern struct {
	PatternID string `json:"patternId"`
	// MetaKey is the source meta-vertex's canonical key (vtx.meta.<NanoID>), set
	// by the pattern source at load time. Dispatched step ops carry it as
	// authContext.target so they never reference the meta-vertex by the
	// human-named PatternID (a forbidden vtx.meta.<canonicalName> shape).
	MetaKey           string   `json:"metaKey,omitempty"`
	SubjectType       string   `json:"subjectType"`
	Steps             []Step   `json:"steps"`
	CompletionDomains []string `json:"completionDomains,omitempty"`
}

// Domains returns the deduped set of completion domains this pattern's systemOp
// steps complete on. A domain is the FIRST segment of an event class (the
// `<domain>` in `events.<domain>.>`), so it is always a single dot-free token
// — the per-domain consumer's durable name (loom-<domain>) requires this.
// Defaults to {subjectType} when completionDomains is omitted; otherwise the
// declared set (each reduced to its first segment) is used verbatim.
func (p *Pattern) Domains() []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(d string) {
		d = firstSegment(strings.TrimSpace(d))
		if d == "" {
			return
		}
		if _, ok := seen[d]; ok {
			return
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	if len(p.CompletionDomains) == 0 {
		add(p.SubjectType)
		return out
	}
	for _, d := range p.CompletionDomains {
		add(d)
	}
	return out
}

// firstSegment returns the part of s before the first dot.
func firstSegment(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i]
	}
	return s
}

// orchestrationCompletionDomain is the event domain both async-completer step
// kinds complete on: a userTask completes via orchestration.taskCompleted and an
// externalTask via orchestration.externalTaskCompleted, both subjected
// events.orchestration.> (domain `orchestration`). A pattern with either step
// kind whose effective completionDomains omits it will never observe those
// completions.
const orchestrationCompletionDomain = "orchestration"

// hasUserTaskStep reports whether any step is a userTask.
func (p *Pattern) hasUserTaskStep() bool {
	for _, s := range p.Steps {
		if s.Kind == StepKindUserTask {
			return true
		}
	}
	return false
}

// hasExternalTaskStep reports whether any step is an externalTask.
func (p *Pattern) hasExternalTaskStep() bool {
	for _, s := range p.Steps {
		if s.Kind == StepKindExternalTask {
			return true
		}
	}
	return false
}

// userTaskCompletionUnobservable reports whether the pattern has a userTask step
// but its effective completion domains (after the [subjectType] default) omit
// the orchestration domain — the almost-certain misconfiguration where userTask
// completions can never be observed.
func (p *Pattern) userTaskCompletionUnobservable() bool {
	if !p.hasUserTaskStep() {
		return false
	}
	for _, d := range p.Domains() {
		if d == orchestrationCompletionDomain {
			return false
		}
	}
	return true
}

// externalTaskCompletionUnobservable reports whether the pattern has an
// externalTask step but its effective completion domains (after the [subjectType]
// default) omit the orchestration domain — the almost-certain misconfiguration
// where externalTask completions (orchestration.externalTaskCompleted) can never
// be observed. The externalTask analog of userTaskCompletionUnobservable.
func (p *Pattern) externalTaskCompletionUnobservable() bool {
	if !p.hasExternalTaskStep() {
		return false
	}
	for _, d := range p.Domains() {
		if d == orchestrationCompletionDomain {
			return false
		}
	}
	return true
}

// validate rejects a pattern the engine cannot run. systemOp, userTask, and
// externalTask steps are interpreted; any other kind is rejected so a
// half-understood pattern never partially executes. Each kind's §10.5 shape is
// enforced exactly — required fields present AND foreign fields absent — so a
// step that confuses the two shapes (e.g. a systemOp carrying adapter/params,
// or an externalTask carrying operation) is rejected rather than silently
// running with the foreign field ignored. systemOp/userTask require a non-empty
// operation and forbid adapter/instanceOp/replyOp/params; externalTask requires
// non-empty adapter/instanceOp/replyOp (params optional) and forbids operation.
// A guarded step's guard
// must parse as a §10.5 declarative shape (atoms/composition); a malformed
// guard or the reserved Starlark escape hatch rejects the whole pattern, the
// same doctrine as an unknown kind — a half-understood pattern never partially
// executes. Guards apply to any kind.
func (p *Pattern) validate() error {
	if strings.TrimSpace(p.SubjectType) == "" {
		return fmt.Errorf("pattern %q: subjectType required", p.PatternID)
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("pattern %q: at least one step required", p.PatternID)
	}
	for i, s := range p.Steps {
		switch s.Kind {
		case StepKindSystemOp, StepKindUserTask:
			if strings.TrimSpace(s.Operation) == "" {
				return fmt.Errorf("pattern %q step %d: operation required", p.PatternID, i)
			}
			if strings.TrimSpace(s.Adapter) != "" {
				return fmt.Errorf("pattern %q step %d: adapter is an externalTask-only field, not permitted on a %s step", p.PatternID, i, s.Kind)
			}
			if strings.TrimSpace(s.InstanceOp) != "" {
				return fmt.Errorf("pattern %q step %d: instanceOp is an externalTask-only field, not permitted on a %s step", p.PatternID, i, s.Kind)
			}
			if strings.TrimSpace(s.ReplyOp) != "" {
				return fmt.Errorf("pattern %q step %d: replyOp is an externalTask-only field, not permitted on a %s step", p.PatternID, i, s.Kind)
			}
			if len(s.Params) != 0 {
				return fmt.Errorf("pattern %q step %d: params is an externalTask-only field, not permitted on a %s step", p.PatternID, i, s.Kind)
			}
			if s.Kind == StepKindUserTask {
				if err := rejectDeclaredReads(p.PatternID, i, s, "the engine derives a userTask's read-set from the CreateTask invariant"); err != nil {
					return err
				}
			} else {
				if err := validateSubjectTemplates(p.PatternID, i, "reads", s.Reads); err != nil {
					return err
				}
				if err := validateSubjectTemplates(p.PatternID, i, "optionalReads", s.OptionalReads); err != nil {
					return err
				}
				if err := validateEnumerations(p.PatternID, i, s.Enumerations); err != nil {
					return err
				}
			}
		case StepKindExternalTask:
			if strings.TrimSpace(s.Adapter) == "" {
				return fmt.Errorf("pattern %q step %d: adapter required for externalTask", p.PatternID, i)
			}
			if strings.TrimSpace(s.InstanceOp) == "" {
				return fmt.Errorf("pattern %q step %d: instanceOp required for externalTask", p.PatternID, i)
			}
			if strings.TrimSpace(s.ReplyOp) == "" {
				return fmt.Errorf("pattern %q step %d: replyOp required for externalTask", p.PatternID, i)
			}
			if strings.TrimSpace(s.Operation) != "" {
				return fmt.Errorf("pattern %q step %d: operation is a systemOp/userTask-only field, not permitted on an externalTask step", p.PatternID, i)
			}
			if err := rejectDeclaredReads(p.PatternID, i, s, "an externalTask's read-set is inferred from its declared params"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("pattern %q step %d: kind %q unsupported (systemOp | userTask | externalTask)",
				p.PatternID, i, s.Kind)
		}
		if len(s.Guard) != 0 {
			if _, err := parseGuard(s.Guard); err != nil {
				return fmt.Errorf("pattern %q step %d: %w", p.PatternID, i, err)
			}
		}
	}
	return nil
}

// StartLoomPattern is the payload of the op that triggers a new instance
// (Contract #10 §10.5). subjectKey must be a vertex of the pattern's
// subjectType; patternRef is the meta.loomPattern vertex key
// (vtx.meta.<patternId>) or the bare patternId.
type StartLoomPattern struct {
	PatternRef string `json:"patternRef"`
	SubjectKey string `json:"subjectKey"`
}

// patternIDFromRef accepts either a bare patternId or a vtx.meta.<id> key and
// returns the patternId.
func patternIDFromRef(ref string) string {
	if id, ok := strings.CutPrefix(ref, "vtx.meta."); ok {
		return id
	}
	return ref
}
