package processor

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Tests for the per-execution read record (script_read_record.go): what a
// Starlark script ACTUALLY read through the `kv` builtins, split by whether the
// step-4 snapshot answered the read (declared) or the lazy live fallthrough did
// (undeclared by construction).
//
// The record is observation only — nothing here gates an execution. What these
// tests pin is that it is COMPLETE and HONEST: every serving point of kv.Read
// classifies its key, kv.Links records the walk it completed plus the endpoint
// vertices it exposed, and the snapshot is deterministic.

const (
	readRecDeclaredID = "Rd4kPmRtw9nbCxz5vQ2y"
	readRecLiveID     = "Rv6mP3qBn4rT8wYxK7Vc"
	readRecStateID    = "Rs7kQmBtw4nbCxz5vP2y"
)

// assertRecordedKeys compares one classification of a record against the exact
// key set the script read — equality, not membership, so a recorder that
// over-records (e.g. every hydrated key rather than every key the script named)
// fails here.
func assertRecordedKeys(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

// TestScriptReadRecord_DeclaredReadRecordedAndNotLive is the positive vector: a
// key the operation DECLARED (hydrated at step 4) lands in DeclaredReads and
// nowhere near LiveReads. Without this, a recorder that classified everything as
// a live read would satisfy every drift assertion below while making the record
// meaningless.
//
// Non-vacuous on both sides: the live reader holds the same key, so a
// misclassification would have something to serve; the assertion that it was
// never called proves the declared branch is what recorded.
func TestScriptReadRecord_DeclaredReadRecordedAndNotLive(t *testing.T) {
	key := "vtx.identity." + readRecDeclaredID
	rec := &scriptReadRecorder{}
	reader := &fakeKVReader{docs: map[string]*VertexDoc{key: {Key: key, Class: "identity"}}}
	sc := ScriptContext{
		Hydrated:     map[string]VertexDoc{key: {Key: key, Class: "identity", Revision: 4}},
		KVReader:     reader,
		ReadRecorder: rec,
	}
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+key+`")
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := rec.record()
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, []string{key})
	assertRecordedKeys(t, "LiveReads", got.LiveReads, nil)
	if len(reader.calls) != 0 {
		t.Fatalf("declared read must not reach the live reader, calls: %v", reader.calls)
	}
}

// TestScriptReadRecord_KnownAbsentReadRecordedAsDeclared — an `optionalReads`
// key that was absent at the step-4 snapshot is served as None from that
// snapshot. The script named a DECLARED key, so the read is declared even though
// no document came back.
func TestScriptReadRecord_KnownAbsentReadRecordedAsDeclared(t *testing.T) {
	key := "vtx.identity." + readRecDeclaredID
	rec := &scriptReadRecorder{}
	// The reader WOULD serve the key: if the known-absent branch stopped
	// recording, the read would fall through and be classified live instead.
	reader := &fakeKVReader{docs: map[string]*VertexDoc{key: {Key: key, Class: "identity"}}}
	sc := ScriptContext{
		KnownAbsent:  map[string]struct{}{key: {}},
		KVReader:     reader,
		ReadRecorder: rec,
	}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+key+`")
    return {"mutations": [], "events": [{"class": "none" if v == None else "present"}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Events[0].Class != "none" {
		t.Fatalf("known-absent must serve None, got %q", res.Events[0].Class)
	}
	got := rec.record()
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, []string{key})
	assertRecordedKeys(t, "LiveReads", got.LiveReads, nil)
}

// TestScriptReadRecord_RequiredAbsentReadRecordedBeforeFault — a fail-closed
// declared read that was absent at the snapshot aborts the execution with the
// deferred HydrationMiss. The script still NAMED the key, so the record must
// carry it: the read the operation depends on is exactly the one a drift check
// needs to see.
func TestScriptReadRecord_RequiredAbsentReadRecordedBeforeFault(t *testing.T) {
	key := "vtx.identity." + readRecDeclaredID
	rec := &scriptReadRecorder{}
	sc, _ := requiredAbsentContext(key)
	sc.ReadRecorder = rec

	_, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+key+`")
    return {"mutations": [], "events": []}
`)
	assertDeferredMiss(t, err, key)
	got := rec.record()
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, []string{key})
	assertRecordedKeys(t, "LiveReads", got.LiveReads, nil)
}

// TestScriptReadRecord_LivePresentReadRecordedAsLive — a key in none of
// Hydrated/RequiredAbsent/KnownAbsent falls through to the live GET and comes
// back present. Undeclared by construction, so it lands in LiveReads.
func TestScriptReadRecord_LivePresentReadRecordedAsLive(t *testing.T) {
	key := "vtx.identity." + readRecLiveID
	rec := &scriptReadRecorder{}
	sc := ScriptContext{
		KVReader:     &fakeKVReader{docs: map[string]*VertexDoc{key: {Key: key, Class: "identity"}}},
		ReadRecorder: rec,
	}
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+key+`")
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := rec.record()
	assertRecordedKeys(t, "LiveReads", got.LiveReads, []string{key})
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, nil)
}

// TestScriptReadRecord_LiveAbsentReadRecordedAsLive — the same fallthrough when
// the key is absent. The script learned the key does not exist, which is a read;
// a record that only counted returned documents would miss the entire
// read-before-create pattern, the commonest undeclared read there is.
func TestScriptReadRecord_LiveAbsentReadRecordedAsLive(t *testing.T) {
	key := "vtx.identity." + readRecLiveID
	rec := &scriptReadRecorder{}
	sc := ScriptContext{
		KVReader:     &fakeKVReader{docs: map[string]*VertexDoc{}},
		ReadRecorder: rec,
	}
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+key+`")
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := rec.record()
	assertRecordedKeys(t, "LiveReads", got.LiveReads, []string{key})
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, nil)
}

// stateSnapshotContext builds a context whose step-4 snapshot holds two
// hydrated documents, with a live reader that would serve both. The reader is
// the non-vacuity control for the `state` tests: nothing they record may come
// from a live read.
func stateSnapshotContext(rec *scriptReadRecorder) (ScriptContext, string, string) {
	k1 := "vtx.identity." + readRecDeclaredID
	k2 := "vtx.identity." + readRecStateID
	return ScriptContext{
		Hydrated: map[string]VertexDoc{
			k1: {Key: k1, Class: "identity", Data: map[string]interface{}{"n": "one"}},
			k2: {Key: k2, Class: "identity", Data: map[string]interface{}{"n": "two"}},
		},
		KVReader: &fakeKVReader{docs: map[string]*VertexDoc{
			k1: {Key: k1, Class: "identity"},
			k2: {Key: k2, Class: "identity"},
		}},
		ReadRecorder: rec,
	}, k1, k2
}

// TestScriptReadRecord_StateSubscriptRecordsOnlyThatKey is the positive vector
// for the second read path: `state` is a read of Core KV that never touches
// kv.Read, and a record that missed it would under-report every script that
// consumes its contextHint through the snapshot. Naming ONE of two hydrated keys
// records that key ALONE — a seam that recorded the whole snapshot on any
// subscript would pass a mere non-emptiness check while making the record
// useless for drift.
func TestScriptReadRecord_StateSubscriptRecordsOnlyThatKey(t *testing.T) {
	rec := &scriptReadRecorder{}
	sc, k1, _ := stateSnapshotContext(rec)
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    v = state["`+k1+`"]
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := rec.record()
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, []string{k1})
	assertRecordedKeys(t, "LiveReads", got.LiveReads, nil)
}

// TestScriptReadRecord_StateGetRecordsNamedKey — `state.get(K)` is re-bound onto
// the wrapper's Get, so it records exactly as a subscript does.
func TestScriptReadRecord_StateGetRecordsNamedKey(t *testing.T) {
	rec := &scriptReadRecorder{}
	sc, _, k2 := stateSnapshotContext(rec)
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    v = state.get("`+k2+`")
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := rec.record()
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, []string{k2})
}

// TestScriptReadRecord_StateProbeOfUnansweredKeyRecordsNothing — a membership
// test or defaulted get for a key the snapshot does NOT hold answers from the
// script's own declared set, not from Core KV. Recording it would be the
// fail-open direction for a drift check: an undeclared key would appear as a
// declared read.
func TestScriptReadRecord_StateProbeOfUnansweredKeyRecordsNothing(t *testing.T) {
	rec := &scriptReadRecorder{}
	sc, _, _ := stateSnapshotContext(rec)
	undeclared := "vtx.identity." + readRecLiveID
	res, err := runKVScript(t, sc, `
def execute(state, op):
    present = "`+undeclared+`" in state
    v = state.get("`+undeclared+`", None)
    return {"mutations": [], "events": [{"class": "probe", "data": {"present": present, "none": v == None}}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	d := res.Events[0].Data
	if d["present"] != false || d["none"] != true {
		t.Fatalf("probe of an unheld key: %+v, want absent", d)
	}
	got := rec.record()
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, nil)
	assertRecordedKeys(t, "LiveReads", got.LiveReads, nil)
}

// TestScriptReadRecord_StateRequiredAbsentSubscriptRecorded — the `state` half of
// the fail-closed declared read. The subscript faults with the deferred
// HydrationMiss, and the key the operation depends on is in the record.
func TestScriptReadRecord_StateRequiredAbsentSubscriptRecorded(t *testing.T) {
	key := "vtx.identity." + readRecDeclaredID
	rec := &scriptReadRecorder{}
	sc, _ := requiredAbsentContext(key)
	sc.ReadRecorder = rec

	_, err := runKVScript(t, sc, `
def execute(state, op):
    v = state["`+key+`"]
    return {"mutations": [], "events": []}
`)
	assertDeferredMiss(t, err, key)
	got := rec.record()
	assertRecordedKeys(t, "DeclaredReads", got.DeclaredReads, []string{key})
	assertRecordedKeys(t, "LiveReads", got.LiveReads, nil)
}

// TestScriptReadRecord_StateWholeSetExposuresRecordEverything — `items()`,
// `values()` and every rendering path through String() hand the script EVERY
// hydrated document without naming a key, so each records the whole snapshot.
// A record that only counted named keys would read as "this script touched
// nothing" for a script that dumped the lot.
func TestScriptReadRecord_StateWholeSetExposuresRecordEverything(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"items", `    for k, v in state.items():
        pass`},
		{"values", `    for v in state.values():
        pass`},
		{"str", `    s = str(state)`},
		{"format", `    s = "{}".format(state)`},
		{"percent", `    s = "%s" % state`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scriptReadRecorder{}
			sc, k1, k2 := stateSnapshotContext(rec)
			if _, err := runKVScript(t, sc, `
def execute(state, op):
`+tc.body+`
    return {"mutations": [], "events": []}
`); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := []string{k1, k2}
			slices.Sort(want)
			assertRecordedKeys(t, "DeclaredReads", rec.record().DeclaredReads, want)
		})
	}
}

// TestScriptReadRecord_StateKeyOnlySurfacesRecordNothing — `keys()` and iterating
// `state` yield key NAMES the operation already declared, never a document, so
// neither is a read. This is the asymmetry with items/values, and recording here
// would report every script that loops over its own contextHint as having read
// the whole snapshot.
func TestScriptReadRecord_StateKeyOnlySurfacesRecordNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"keys", `    ks = state.keys()`},
		{"iterate", `    for k in state:
        pass`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scriptReadRecorder{}
			sc, _, _ := stateSnapshotContext(rec)
			if _, err := runKVScript(t, sc, `
def execute(state, op):
`+tc.body+`
    return {"mutations": [], "events": []}
`); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertRecordedKeys(t, "DeclaredReads", rec.record().DeclaredReads, nil)
		})
	}
}

// TestScriptReadRecord_EnumerationAndEndpointsRecorded — a completed kv.Links
// walk records the (hub, relation, direction) triple in the same terms a
// contextHint enumeration declares it, plus every endpoint vertex key the page
// exposed to the script.
func TestScriptReadRecord_EnumerationAndEndpointsRecorded(t *testing.T) {
	hub := "vtx.provider." + linkProvID
	rec := &scriptReadRecorder{}
	lister := &fakeLinkLister{links: []LinkDoc{
		{
			Key:          "lnk.provider." + linkProvID + ".hasBooking.appointment." + linkApptID1,
			Class:        "hasBooking",
			SourceVertex: hub,
			TargetVertex: "vtx.appointment." + linkApptID1,
		},
		{
			Key:          "lnk.provider." + linkProvID + ".hasBooking.appointment." + linkApptID2,
			Class:        "hasBooking",
			SourceVertex: hub,
			TargetVertex: "vtx.appointment." + linkApptID2,
		},
	}}
	sc := ScriptContext{LinkLister: lister, ReadRecorder: rec}
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("`+hub+`", "hasBooking", "out")
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := rec.record()
	want := []ScriptEnumeration{{Hub: hub, Relation: "hasBooking", Direction: "out"}}
	if !slices.Equal(got.Enumerations, want) {
		t.Fatalf("Enumerations = %+v, want %+v", got.Enumerations, want)
	}
	assertRecordedKeys(t, "EnumeratedVertices", got.EnumeratedVertices, []string{
		"vtx.appointment." + linkApptID1,
		"vtx.appointment." + linkApptID2,
		hub,
	})
}

// TestScriptReadRecord_FailedEnumerationNotRecorded — a kv.Links call whose
// lister errored never reached the script, so recording it would claim a read
// that did not happen. The recording sits at the page return for exactly this
// reason.
func TestScriptReadRecord_FailedEnumerationNotRecorded(t *testing.T) {
	hub := "vtx.provider." + linkProvID
	rec := &scriptReadRecorder{}
	sc := ScriptContext{LinkLister: &fakeLinkLister{err: errors.New("boom-list")}, ReadRecorder: rec}
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("`+hub+`", "hasBooking", "out")
    return {"mutations": [], "events": []}
`); err == nil {
		t.Fatal("expected the lister error to abort the script")
	}
	got := rec.record()
	if len(got.Enumerations) != 0 {
		t.Fatalf("Enumerations = %+v, want none for a failed walk", got.Enumerations)
	}
	if len(got.EnumeratedVertices) != 0 {
		t.Fatalf("EnumeratedVertices = %v, want none for a failed walk", got.EnumeratedVertices)
	}
}

// TestScriptReadRecord_SnapshotIsSorted — the record is deterministic regardless
// of the order the script read in (and of Go's map iteration order), so it can
// be compared, diffed and logged as a value. Reads are issued in deliberately
// reverse-sorted order.
func TestScriptReadRecord_SnapshotIsSorted(t *testing.T) {
	hubA := "vtx.provider." + linkProvID
	hubB := "vtx.appointment." + linkApptID1
	rec := &scriptReadRecorder{}
	sc := ScriptContext{
		KVReader:     &fakeKVReader{docs: map[string]*VertexDoc{}},
		LinkLister:   &fakeLinkLister{},
		ReadRecorder: rec,
	}
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    kv.Read("vtx.task.zzz")
    kv.Read("vtx.task.mmm")
    kv.Read("vtx.task.aaa")
    kv.Links("`+hubA+`", "hasBooking", "out")
    kv.Links("`+hubB+`", "withProvider", "in")
    kv.Links("`+hubA+`", "hasBooking", "in")
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := rec.record()
	assertRecordedKeys(t, "LiveReads", got.LiveReads,
		[]string{"vtx.task.aaa", "vtx.task.mmm", "vtx.task.zzz"})
	// Sorted by Hub, then Relation, then Direction — the appointment hub sorts
	// ahead of the provider hub, and the two provider walks split on direction.
	want := []ScriptEnumeration{
		{Hub: hubB, Relation: "withProvider", Direction: "in"},
		{Hub: hubA, Relation: "hasBooking", Direction: "in"},
		{Hub: hubA, Relation: "hasBooking", Direction: "out"},
	}
	if !slices.Equal(got.Enumerations, want) {
		t.Fatalf("Enumerations = %+v, want %+v (sorted)", got.Enumerations, want)
	}
	// Repeat the snapshot: it must be a pure projection of the same state.
	if again := rec.record(); !slices.Equal(again.LiveReads, got.LiveReads) || !slices.Equal(again.Enumerations, got.Enumerations) {
		t.Fatalf("record() is not stable across calls: %+v then %+v", got, again)
	}
}

// TestScriptReadRecord_NilRecorderIsNoOp — every recording seam is nil-safe, so
// a ScriptContext built without a recorder (every harness that does not care,
// and any future caller that skips step 4) executes exactly as it would with the
// seam absent.
func TestScriptReadRecord_NilRecorderIsNoOp(t *testing.T) {
	var rec *scriptReadRecorder
	rec.recordDeclaredRead("vtx.task.a")
	rec.recordLiveRead("vtx.task.b")
	rec.recordEnumeration("vtx.task.c", "rel", "out")
	rec.recordEnumeratedVertex("vtx.task.d")
	got := rec.record()
	if len(got.DeclaredReads) != 0 || len(got.LiveReads) != 0 ||
		len(got.Enumerations) != 0 || len(got.EnumeratedVertices) != 0 {
		t.Fatalf("nil recorder must yield a zero record, got %+v", got)
	}

	hub := "vtx.provider." + linkProvID
	sc := ScriptContext{
		Hydrated:   map[string]VertexDoc{"vtx.task.hydrated": {Key: "vtx.task.hydrated", Class: "task"}},
		KVReader:   &fakeKVReader{docs: map[string]*VertexDoc{}},
		LinkLister: &fakeLinkLister{},
	}
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    kv.Read("vtx.task.hydrated")
    kv.Read("vtx.task.undeclared")
    kv.Links("`+hub+`", "hasBooking", "out")
    v = state["vtx.task.hydrated"]
    for k, e in state.items():
        pass
    s = str(state)
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("an unwired recorder must not affect execution: %v", err)
	}
}

// capturingScriptReadObserver records every ScriptReadRecord the commit path
// hands it, with the request id it was handed alongside.
type capturingScriptReadObserver struct {
	mu         sync.Mutex
	records    []ScriptReadRecord
	requestIDs []string
}

func (o *capturingScriptReadObserver) ObserveScriptReads(_ context.Context, env *OperationEnvelope, record ScriptReadRecord) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, record)
	o.requestIDs = append(o.requestIDs, env.RequestID)
}

func (o *capturingScriptReadObserver) seen() ([]ScriptReadRecord, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.records), slices.Clone(o.requestIDs)
}

// seedReadingScript replaces the identity class script with one that makes a
// declared read and an undeclared one, optionally aborting afterwards.
func seedReadingScript(t *testing.T, ctx context.Context, conn *substrate.Conn, declaredKey, liveKey string, abort bool) {
	t.Helper()
	tail := `    return {\"mutations\": [], \"events\": []}\n`
	if abort {
		tail = `    fail(\"deliberate abort after reading\")\n`
	}
	src := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n` +
		`    a = kv.Read(\"` + declaredKey + `\")\n` +
		`    b = kv.Read(\"` + liveKey + `\")\n` + tail + `"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", src); err != nil {
		t.Fatalf("seed reading script: %v", err)
	}
}

// TestScriptReadRecord_ObserverSeesOneRecordPerExecution — through the whole
// commit path: the observer fires exactly once for one execution, is handed the
// executing operation's envelope, and receives a record that separates the
// contextHint-declared read from the undeclared live one.
func TestScriptReadRecord_ObserverSeesOneRecordPerExecution(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, cons, _ := setupTestPipeline(t)
	declaredKey := "vtx.identity." + readRecDeclaredID
	liveKey := "vtx.identity." + readRecLiveID
	if _, err := conn.KVPut(ctx, testCoreBucket, declaredKey, []byte(`{"class":"identity","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed declared key: %v", err)
	}
	seedReadingScript(t, ctx, conn, declaredKey, liveKey, false)

	obs := &capturingScriptReadObserver{}
	cp.deps.ScriptReadObserver = obs

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{declaredKey}}
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	records, ids := obs.seen()
	if len(records) != 1 {
		t.Fatalf("observer fired %d times, want exactly 1 per execution", len(records))
	}
	if ids[0] != env.RequestID {
		t.Fatalf("observer got requestId %q, want %q", ids[0], env.RequestID)
	}
	assertRecordedKeys(t, "DeclaredReads", records[0].DeclaredReads, []string{declaredKey})
	assertRecordedKeys(t, "LiveReads", records[0].LiveReads, []string{liveKey})
}

// TestScriptReadRecord_ObserverSeesAbortedExecution — a script that read and
// then failed still reported its reads. The observation sits ahead of step 5's
// error branch precisely so a drift check is not blind to the executions most
// likely to be drifting.
func TestScriptReadRecord_ObserverSeesAbortedExecution(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, cons, _ := setupTestPipeline(t)
	declaredKey := "vtx.identity." + readRecDeclaredID
	liveKey := "vtx.identity." + readRecLiveID
	if _, err := conn.KVPut(ctx, testCoreBucket, declaredKey, []byte(`{"class":"identity","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed declared key: %v", err)
	}
	seedReadingScript(t, ctx, conn, declaredKey, liveKey, true)

	obs := &capturingScriptReadObserver{}
	cp.deps.ScriptReadObserver = obs

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{declaredKey}}
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)

	records, _ := obs.seen()
	if len(records) != 1 {
		t.Fatalf("observer fired %d times for an aborted script, want 1", len(records))
	}
	assertRecordedKeys(t, "DeclaredReads", records[0].DeclaredReads, []string{declaredKey})
	assertRecordedKeys(t, "LiveReads", records[0].LiveReads, []string{liveKey})
}

// TestScriptReadRecord_NilObserverIsNoOp — production wires no observer, so the
// same reading operation must commit unchanged with the seam unwired.
func TestScriptReadRecord_NilObserverIsNoOp(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, cons, metrics := setupTestPipeline(t)
	declaredKey := "vtx.identity." + readRecDeclaredID
	liveKey := "vtx.identity." + readRecLiveID
	if _, err := conn.KVPut(ctx, testCoreBucket, declaredKey, []byte(`{"class":"identity","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed declared key: %v", err)
	}
	seedReadingScript(t, ctx, conn, declaredKey, liveKey, false)

	if cp.deps.ScriptReadObserver != nil {
		t.Fatal("the pipeline must wire no ScriptReadObserver by default")
	}
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{declaredKey}}
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)
	if metrics.OpsCommitted.Load() != 1 {
		t.Fatalf("OpsCommitted = %d, want 1", metrics.OpsCommitted.Load())
	}
}
