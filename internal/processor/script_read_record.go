package processor

import (
	"cmp"
	"context"
	"slices"
)

// scriptReadRecorder records what one Starlark execution ACTUALLY read — through
// the `state` global and through the `kv` builtins, the two paths a script has
// into Core KV — so the record can be compared against what the operation's
// `contextHint` DECLARED (Contract #2 §2.5 / §2.5.1).
//
// It separates the two dispositions structurally rather than by diffing sets:
//   - declaredReads — keys the step-4 snapshot answered: kv.Read served from
//     Hydrated / RequiredAbsent / KnownAbsent, and every `state` exposure that
//     hands the script a document (`state[K]`, `state.get(K)`, and the whole-set
//     `items`/`values`/`str`). The snapshot is built from the DECLARED read set
//     — `contextHint`'s three lists PLUS the class-(g) keys step 4's
//     `derive_reads` pre-pass resolves from the script itself (derive_reads.go).
//     So a key the snapshot answers was declared through one channel or the
//     other, and a consumer that wants to attribute it to `contextHint`
//     specifically has to consult the envelope: this record does not separate
//     the two;
//   - liveReads — keys kv.Read served through the lazy on-demand fallthrough.
//     That branch is reached only when the key is in none of Hydrated /
//     RequiredAbsent / KnownAbsent, so every key recorded there was declared by
//     NEITHER channel — undeclared by construction;
//   - enumerations / enumeratedVertices — the kv.Links walks the script actually
//     performed and the FAR endpoint each walk yielded, the §2.5.1 set-valued
//     counterpart to a named key read.
//
// OBSERVATION ONLY — never a runtime control. `contextHint` is submitter-supplied
// and step 3 authorizes without inspecting it, so a Processor that rejected an
// undeclared read would only be enforcing a constraint the submitter writes for
// itself: any caller drifting from its declaration can widen the declaration.
// The record is therefore a TEST-TIME drift detector over our own corpus (the
// `packages/` scripts and the ops that dispatch them), where both halves are
// ours to keep honest — it must never gate an execution, decide a reply, or
// change a commit outcome.
//
// Lifetime: one recorder per step-4 hydrate (step4_hydrate.go), which makes it
// one recorder per commit ATTEMPT — the commit path's retry loop re-enters
// Hydrate, so a re-executed operation records the attempt that ran, not a union
// across attempts. It dies with the execution and is never persisted.
//
// Nil-safe throughout: a ScriptContext built without one (every harness that
// does not care) records nothing and yields a zero record.
type scriptReadRecorder struct {
	declaredReads      map[string]struct{}
	liveReads          map[string]struct{}
	enumerations       map[ScriptEnumeration]struct{}
	enumeratedVertices map[string]struct{}
}

// recordDeclaredRead records that the script read key and the step-4 snapshot
// answered it — present, known-absent, or required-absent.
func (r *scriptReadRecorder) recordDeclaredRead(key string) {
	if r == nil {
		return
	}
	if r.declaredReads == nil {
		r.declaredReads = make(map[string]struct{})
	}
	r.declaredReads[key] = struct{}{}
}

// recordAllDeclaredReads records an exposure that hands the script every
// hydrated document without naming a key — `state.items()` / `state.values()`,
// and `String()`, through which `str`/`repr`/`%`/`.format`/`+` render every
// document. The whole snapshot reached the script, so the whole snapshot is
// read, on the same footing as sensitiveReadTracker.consumeAll.
func (r *scriptReadRecorder) recordAllDeclaredReads(keys ...string) {
	if r == nil || len(keys) == 0 {
		return
	}
	if r.declaredReads == nil {
		r.declaredReads = make(map[string]struct{}, len(keys))
	}
	for _, k := range keys {
		r.declaredReads[k] = struct{}{}
	}
}

// recordLiveRead records that the script read key through the lazy on-demand
// Core KV fallthrough, which by construction the operation did not declare.
func (r *scriptReadRecorder) recordLiveRead(key string) {
	if r == nil {
		return
	}
	if r.liveReads == nil {
		r.liveReads = make(map[string]struct{})
	}
	r.liveReads[key] = struct{}{}
}

// recordEnumeration records one kv.Links walk the script completed, in the same
// (hub, relation, direction) terms a contextHint enumeration declares it.
func (r *scriptReadRecorder) recordEnumeration(hub, relation, direction string) {
	if r == nil {
		return
	}
	if r.enumerations == nil {
		r.enumerations = make(map[ScriptEnumeration]struct{})
	}
	r.enumerations[ScriptEnumeration{Hub: hub, Relation: relation, Direction: direction}] = struct{}{}
}

// recordEnumeratedVertex records a vertex an enumeration DISCOVERED — the far
// end of one returned link, never the hub. See starlark_kv.go's kv.Links loop
// for why the hub is excluded: it is pinned to every link by the subject filter
// and was named by the script to start the walk, so recording it would make
// "an enumeration surfaced this vertex" vacuously true of the hub.
func (r *scriptReadRecorder) recordEnumeratedVertex(key string) {
	if r == nil {
		return
	}
	if r.enumeratedVertices == nil {
		r.enumeratedVertices = make(map[string]struct{})
	}
	r.enumeratedVertices[key] = struct{}{}
}

// record returns the execution's accumulated reads in stable sorted order, so
// two runs of the same script produce byte-identical records for an assertion
// or a log line. A nil recorder yields the zero record.
func (r *scriptReadRecorder) record() ScriptReadRecord {
	if r == nil {
		return ScriptReadRecord{}
	}
	enums := make([]ScriptEnumeration, 0, len(r.enumerations))
	for e := range r.enumerations {
		enums = append(enums, e)
	}
	slices.SortFunc(enums, func(a, b ScriptEnumeration) int {
		if c := cmp.Compare(a.Hub, b.Hub); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Relation, b.Relation); c != 0 {
			return c
		}
		return cmp.Compare(a.Direction, b.Direction)
	})
	return ScriptReadRecord{
		DeclaredReads:      sortedKeySet(r.declaredReads),
		LiveReads:          sortedKeySet(r.liveReads),
		Enumerations:       enums,
		EnumeratedVertices: sortedKeySet(r.enumeratedVertices),
	}
}

// sortedKeySet flattens a key set into a sorted slice, nil for an empty set.
func sortedKeySet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ScriptEnumeration is one kv.Links walk a script performed, in the terms
// opwire.EnumerationHint declares one: the hub vertex key, the link relation,
// and the direction the hub sits in the link ("out" = hub is source, "in" = hub
// is target). Field names mirror the hint so a drift check reads as a
// set comparison rather than a translation.
type ScriptEnumeration struct {
	Hub       string
	Relation  string
	Direction string
}

// ScriptReadRecord is the sorted snapshot of one execution's reads (see
// scriptReadRecorder for what each field means and why the record is
// observation only). Slices are sorted and nil when empty.
type ScriptReadRecord struct {
	DeclaredReads []string
	LiveReads     []string
	Enumerations  []ScriptEnumeration
	// EnumeratedVertices are the vertices the enumerations DISCOVERED: the far
	// end of each returned link, excluding every walk's own hub. A consumer may
	// therefore read membership here as "this key was surfaced by a walk rather
	// than named by the script" — which it could not if the hub were included,
	// since the subject filter puts the hub on every link a walk returns.
	EnumeratedVertices []string
}

// ScriptReadObserver receives the ScriptReadRecord of every step-5 execution,
// together with the envelope whose contextHint the record can be compared
// against. Invoked once per execution, on the failure path as well as the
// success path — a script that aborted still performed the reads it performed.
//
// Nil in production (commit_path.go): the record exists so a test harness can
// detect a script drifting from its declaration, and an observer must never
// influence the operation's outcome.
type ScriptReadObserver interface {
	ObserveScriptReads(ctx context.Context, env *OperationEnvelope, record ScriptReadRecord)
}
