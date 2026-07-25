package processor

import (
	"errors"
	"testing"
)

// The security proof for deferred-miss hydration (design
// contexthint-existence-oracle §5).
//
// `contextHint` is client-supplied and step 3 authorizes without inspecting it,
// so a declared-but-absent read that faulted during hydration would answer
// "does this key exist?" for any actor holding any operation grant, over any
// key, before a script runs. Step 4 records the absence instead and the
// HydrationMiss is raised where the operation DEPENDS on the key. Two
// properties must hold together, and each of these tests pins one side:
//
//   - an operation that touches the key fails closed, through every path that
//     can touch it;
//   - an operation that does not touch it behaves as if the key had never been
//     declared — which is what leaves nothing to learn.

// requiredAbsentContext builds a ScriptContext whose declared read of key was
// absent at the step-4 snapshot, with a live reader that WOULD serve the key.
// The reader is the non-vacuity control: if the RequiredAbsent check were
// removed, the read would fall through and succeed, so a passing fail-closed
// assertion cannot be an accident of there being nothing to read.
func requiredAbsentContext(key string) (ScriptContext, *fakeKVReader) {
	reader := &fakeKVReader{docs: map[string]*VertexDoc{
		key: {Key: key, Class: "task"},
	}}
	return ScriptContext{
		RequiredAbsent: map[string]struct{}{key: {}},
		DeferredMiss:   &deferredMissTracker{},
		KVReader:       reader,
	}, reader
}

func assertDeferredMiss(t *testing.T, err error, wantKey string) {
	t.Helper()
	var hErr *HydrationError
	if !errors.As(err, &hErr) {
		t.Fatalf("want *HydrationError, got %T: %v", err, err)
	}
	if hErr.Code != "HydrationMiss" {
		t.Fatalf("Code = %q, want HydrationMiss", hErr.Code)
	}
	if hErr.MissingKey != wantKey {
		t.Fatalf("MissingKey = %q, want %q", hErr.MissingKey, wantKey)
	}
}

// TestRequiredAbsent_KVReadFaults — the declared read is consumed via kv.Read.
// It must NOT fall through to the lazy live reader (which would serve the doc
// and silently soften a read the operation declared it depends on).
func TestRequiredAbsent_KVReadFaults(t *testing.T) {
	key := "vtx.task.AbsentTaskKeyAbcdefg"
	sc, reader := requiredAbsentContext(key)

	_, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+key+`")
    return {"mutations": [], "events": [{"class": "reached", "data": {}}]}
`)
	assertDeferredMiss(t, err, key)
	if len(reader.calls) != 0 {
		t.Fatalf("a required-absent key must never degrade into a live re-read, calls: %v", reader.calls)
	}
}

// TestRequiredAbsent_StateLookupFaults — `state[K]`, `K in state` and
// `state.get(K)` all route through the mapping, so all three must fail closed.
// Without the mapping check `state[K]` would raise a plain Starlark key error
// and `K in state` would quietly answer False, either of which reports the
// key's absence under a different name.
func TestRequiredAbsent_StateLookupFaults(t *testing.T) {
	key := "vtx.task.AbsentTaskKeyAbcdefg"
	for _, tc := range []struct {
		name string
		expr string
	}{
		{"subscript", `v = state["` + key + `"]`},
		{"membership", `v = "` + key + `" in state`},
		{"get", `v = state.get("` + key + `")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, _ := requiredAbsentContext(key)
			_, err := runKVScript(t, sc, `
def execute(state, op):
    `+tc.expr+`
    return {"mutations": [], "events": [{"class": "reached", "data": {}}]}
`)
			assertDeferredMiss(t, err, key)
		})
	}
}

// TestRequiredAbsent_EnumerationDoesNotFault — enumeration names no key, so it
// is not a dependence on any particular one and must NOT fault. Faulting on the
// mere existence of a required-absent key would reject on "some declared read
// was absent", which is precisely the caller-visible answer to "does that key
// exist?" — the oracle, re-opened for every op whose script walks `state`, and
// reachable without ever naming the probe in the payload.
//
// A prefix scan that finds nothing is the script's own to handle:
// orchestration-base's `find_assigned_link` returns None and its caller fails
// closed with `UnknownAssignedLink`.
func TestRequiredAbsent_EnumerationDoesNotFault(t *testing.T) {
	key := "vtx.task.AbsentTaskKeyAbcdefg"
	present := "vtx.identity.PeerActorKeyAbcdefgh"
	for _, tc := range []struct{ name, expr string }{
		{"for-in", "for k in state:\n        n = n + 1"},
		{"keys", "for k in state.keys():\n        n = n + 1"},
		{"items", "for k, v in state.items():\n        n = n + 1"},
		{"values", "for v in state.values():\n        n = n + 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, _ := requiredAbsentContext(key)
			sc.Hydrated = map[string]VertexDoc{present: {Key: present, Class: "identity"}}
			res, err := runKVScript(t, sc, `
def execute(state, op):
    n = 0
    `+tc.expr+`
    return {"mutations": [], "events": [{"class": "counted", "data": {}}]}
`)
			if err != nil {
				t.Fatalf("enumeration must not fault on an unnamed absent key, got: %v", err)
			}
			if len(res.Events) != 1 || res.Events[0].Class != "counted" {
				t.Fatalf("script result = %+v, want the script's own outcome", res.Events)
			}
		})
	}
}

// TestRequiredAbsent_MutationFaults — the write side. applyHydratedRevisions
// defaults an update/tombstone's expectedRevision from the step-4 snapshot and
// SKIPS keys it never hydrated, leaving them unconditioned; so without this
// check, deferring the read would convert a step-4 rejection into an
// unconditioned write to a key that does not exist.
func TestRequiredAbsent_MutationFaults(t *testing.T) {
	key := "vtx.task.AbsentTaskKeyAbcdefg"
	for _, op := range []string{"update", "tombstone"} {
		t.Run(op, func(t *testing.T) {
			sc, _ := requiredAbsentContext(key)
			_, err := runKVScript(t, sc, `
def execute(state, op):
    return {"mutations": [{"op": "`+op+`", "key": "`+key+`", "document": {"class": "task"}}], "events": []}
`)
			assertDeferredMiss(t, err, key)
		})
	}
}

// TestRequiredAbsent_DerivedKeyMutationFaults — an aspect of a required-absent
// vertex, and a link with a required-absent endpoint, both DEPEND on that
// vertex existing even though neither names it exactly. Step 6 resolves an
// aspect's governing DDL through its parent vertex and falls back to the
// permissive default when the vertex resolves to nothing, so without this the
// op would write an aspect onto a vertex it declared it would read and that is
// not there.
func TestRequiredAbsent_DerivedKeyMutationFaults(t *testing.T) {
	vertex := "vtx.task.AbsentTaskKeyAbcdefg"
	other := "vtx.identity.PeerActorKeyAbcdefgh"
	for _, tc := range []struct{ name, key string }{
		{"aspect of absent vertex", vertex + ".status"},
		{"link from absent vertex", "lnk.task.AbsentTaskKeyAbcdefg.assignedTo.identity.PeerActorKeyAbcdefgh"},
		{"link into absent vertex", "lnk.identity.PeerActorKeyAbcdefgh.assignedTo.task.AbsentTaskKeyAbcdefg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := ScriptContext{
				RequiredAbsent: map[string]struct{}{vertex: {}},
				DeferredMiss:   &deferredMissTracker{},
				Hydrated:       map[string]VertexDoc{other: {Key: other, Class: "identity"}},
			}
			_, err := runKVScript(t, sc, `
def execute(state, op):
    return {"mutations": [{"op": "create", "key": "`+tc.key+`", "document": {"class": "task"}}], "events": []}
`)
			// The reported key is the VERTEX the mutation depends on, not the
			// derived key — that names the actual missing dependence.
			assertDeferredMiss(t, err, vertex)
		})
	}
}

// TestRequiredAbsent_UnrelatedDerivedKeyCommits — the converse bound: an aspect
// or link that does NOT involve the required-absent vertex must still commit,
// or the derived-key check would reject on mere key-shape resemblance.
func TestRequiredAbsent_UnrelatedDerivedKeyCommits(t *testing.T) {
	absent := "vtx.task.AbsentTaskKeyAbcdefg"
	sc := ScriptContext{
		RequiredAbsent: map[string]struct{}{absent: {}},
		DeferredMiss:   &deferredMissTracker{},
	}
	_, err := runKVScript(t, sc, `
def execute(state, op):
    return {"mutations": [
        {"op": "create", "key": "vtx.identity.PeerActorKeyAbcdefgh.email", "document": {"class": "identity"}},
        {"op": "create", "key": "lnk.identity.PeerActorKeyAbcdefgh.holdsRole.role.ConsumerRoAbcdefghij", "document": {"class": "holdsRole"}}
    ], "events": []}
`)
	if err != nil {
		t.Fatalf("a mutation unrelated to the absent vertex must not fault, got: %v", err)
	}
}

// TestRequiredAbsent_UntouchedKeyDoesNotFault is the oracle-closed property
// itself: a declared read the script never names cannot change the operation's
// outcome, so it must not reject. This is the assertion the whole mechanism
// exists to make true.
func TestRequiredAbsent_UntouchedKeyDoesNotFault(t *testing.T) {
	key := "vtx.task.AbsentTaskKeyAbcdefg"
	sc, _ := requiredAbsentContext(key)

	res, err := runKVScript(t, sc, `
def execute(state, op):
    return {"mutations": [], "events": [{"class": "ok", "data": {}}]}
`)
	if err != nil {
		t.Fatalf("an untouched declared read must not decide the operation, got: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "ok" {
		t.Fatalf("script result = %+v, want the script's own outcome", res.Events)
	}
	if got := sc.DeferredMiss.missed(); got != "" {
		t.Fatalf("missed() = %q, want \"\" — nothing touched the key", got)
	}
}

// TestRequiredAbsent_OutcomeIndependentOfExistence is the indistinguishability
// statement the oracle turns on. Two executions differing in NOTHING but
// whether the probed key exists — required-absent vs. hydrated, same context
// shape either way — must produce the same result for a script that never
// names it. If these diverge, the caller reads the key's existence off the
// outcome, which is the leak whatever the reply details say.
//
// The enumerating script is the load-bearing case: a scan touches `state`
// without naming any key, so it is where a membership-wide fault would
// reintroduce the leak while every name-a-key test stayed green.
func TestRequiredAbsent_OutcomeIndependentOfExistence(t *testing.T) {
	probe := "vtx.task.ProbeTargetKeyAbcdef"
	other := "vtx.identity.PeerActorKeyAbcdefgh"
	for _, tc := range []struct{ name, body string }{
		{"never touches state", `    pass`},
		{"enumerates state", "    n = 0\n    for k in state:\n        n = n + 1"},
		{"enumerates via keys()", "    n = 0\n    for k in state.keys():\n        n = n + 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := `
def execute(state, op):
` + tc.body + `
    return {"mutations": [], "events": [{"class": "ok", "data": {}}]}
`
			// Identical but for the probe's disposition.
			base := func() ScriptContext {
				return ScriptContext{
					Hydrated:     map[string]VertexDoc{other: {Key: other, Class: "identity"}},
					DeferredMiss: &deferredMissTracker{},
				}
			}
			absent := base()
			absent.RequiredAbsent = map[string]struct{}{probe: {}}
			resAbsent, errAbsent := runKVScript(t, absent, script)

			present := base()
			present.Hydrated[probe] = VertexDoc{Key: probe, Class: "task"}
			resPresent, errPresent := runKVScript(t, present, script)

			if errAbsent != nil || errPresent != nil {
				t.Fatalf("outcome leaks key existence; absent=%v present=%v", errAbsent, errPresent)
			}
			if len(resAbsent.Events) != len(resPresent.Events) ||
				resAbsent.Events[0].Class != resPresent.Events[0].Class {
				t.Fatalf("outcome leaks key existence: absent=%+v present=%+v", resAbsent.Events, resPresent.Events)
			}
		})
	}
}

// TestRequiredAbsent_CreateFaults — declaring a key in `reads` asserts its
// absence is a correctness error, so creating it is not a legitimate branch;
// §2.5 reserves read-before-create for `optionalReads`. Pinned because the
// mutation check deliberately does not discriminate on the op verb.
func TestRequiredAbsent_CreateFaults(t *testing.T) {
	key := "vtx.task.AbsentTaskKeyAbcdefg"
	sc, _ := requiredAbsentContext(key)
	_, err := runKVScript(t, sc, `
def execute(state, op):
    return {"mutations": [{"op": "create", "key": "`+key+`", "document": {"class": "task"}}], "events": []}
`)
	assertDeferredMiss(t, err, key)
}

// TestRequiredAbsent_OCCRetryRebuildsTracker — the commit path re-hydrates on
// every attempt, so each pass gets its own RequiredAbsent set and a fresh
// one-shot tracker. A sticky tracker would carry a fault from an attempt where
// the key was absent into one where it has since been created.
func TestRequiredAbsent_OCCRetryRebuildsTracker(t *testing.T) {
	key := "vtx.task.AbsentTaskKeyAbcdefg"
	script := `
def execute(state, op):
    v = kv.Read("` + key + `")
    return {"mutations": [], "events": [{"class": "ok", "data": {}}]}
`
	// Attempt 1: absent → faults.
	first, _ := requiredAbsentContext(key)
	_, err := runKVScript(t, first, script)
	assertDeferredMiss(t, err, key)

	// Attempt 2 re-hydrates: the key now exists, so it hydrates and the fresh
	// tracker carries nothing from attempt 1.
	second := ScriptContext{
		Hydrated:     map[string]VertexDoc{key: {Key: key, Class: "task"}},
		DeferredMiss: &deferredMissTracker{},
	}
	if _, err := runKVScript(t, second, script); err != nil {
		t.Fatalf("a re-hydrated attempt must not inherit the prior fault: %v", err)
	}
}

// TestRequiredAbsent_FirstTouchWins — the reported key is the one the operation
// reached first, so the diagnostic names the actual dependence rather than
// whichever declaration happened to sort first.
func TestRequiredAbsent_FirstTouchWins(t *testing.T) {
	first := "vtx.task.FirstAbsentKeyAbcdef"
	second := "vtx.task.SecondAbsentKeyAbcde"
	sc := ScriptContext{
		RequiredAbsent: map[string]struct{}{first: {}, second: {}},
		DeferredMiss:   &deferredMissTracker{},
	}

	_, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+first+`")
    w = kv.Read("`+second+`")
    return {"mutations": [], "events": []}
`)
	assertDeferredMiss(t, err, first)
}

// TestRequiredAbsent_NilTrackerStillFailsClosed — Run owns the tracker, so a
// ScriptContext built without one (any harness constructing it directly) still
// raises the HydrationMiss rather than degrading to whatever error carried the
// abort out of Starlark. Pins the fail-closed direction of that defaulting.
func TestRequiredAbsent_NilTrackerStillFailsClosed(t *testing.T) {
	key := "vtx.task.AbsentTaskKeyAbcdefg"
	sc := ScriptContext{RequiredAbsent: map[string]struct{}{key: {}}}

	_, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+key+`")
    return {"mutations": [], "events": []}
`)
	assertDeferredMiss(t, err, key)
}
