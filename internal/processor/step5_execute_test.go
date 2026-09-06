package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// buildContext builds a ScriptContext for the unit-level executor tests.
// No NATS round-trips — purely in-memory.
func buildContext(script string) ScriptContext {
	return ScriptContext{
		Operation: &OperationEnvelope{
			RequestID:     "Rm7q3pntwzkfbcxv5p9j",
			Lane:          LaneDefault,
			OperationType: "CreateIdentity",
			Actor:         "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
			SubmittedAt:   "2026-05-13T10:00:00Z",
			Class:         "identity",
			Payload:       json.RawMessage(`{"name":"Andrew","email":"andrew@lattice.example"}`),
		},
		Hydrated: map[string]VertexDoc{
			"vtx.identity.St6mP3qBn4rT8wYxK7Vc": {
				Key:       "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
				Class:     "identity",
				IsDeleted: false,
				Data:      map[string]interface{}{"name": "System"},
			},
		},
		DDLLookup: map[string]MetaVertex{
			"identity": {Key: "vtx.meta.identity", CanonicalName: "identity",
				PermittedCommands: []string{"CreateIdentity"}},
		},
		ScriptClass:  "identity",
		ScriptSource: script,
	}
}

func TestExecute_CleanExecution(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    actor = state[op.actor]
    if actor == None:
        fail("missing actor")
    new_id = nanoid.new()
    return {
        "mutations": [
            {
                "op": "create",
                "key": "vtx.identity." + new_id,
                "document": {"class": "identity", "isDeleted": False, "data": {"name": op.payload.name}},
            }
        ],
        "events": [
            {"class": "identityCreated", "data": {"name": op.payload.name}}
        ],
    }
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Mutations) != 1 || res.Mutations[0].Op != "create" {
		t.Fatalf("mutations: %+v", res.Mutations)
	}
	if !strings.HasPrefix(res.Mutations[0].Key, "vtx.identity.") {
		t.Fatalf("key: %q", res.Mutations[0].Key)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "identityCreated" {
		t.Fatalf("events: %+v", res.Events)
	}
}

// TestExecute_DDLMetaKeyExposed_EnablesInstanceOfLink proves the script can read
// its own type-DDL meta key off the `ddl` global and write an instanceOf link to
// it — the producer of the Contract #1 §1.5 instanceOf terminal the step-6
// write-gate resolver consumes (the instanceOf-template lift, Fire E). Before
// this, ddl entries exposed only canonicalName + permittedCommands, so a
// fine-grained-class vertex had no way to declare its type authority and fell to
// the permissive default.
func TestExecute_DDLMetaKeyExposed_EnablesInstanceOfLink(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    meta = ddl["identity"].metaKey          # the script's own type-DDL meta key
    meta_id = meta.split(".")[2]            # vtx.meta.<id> → <id>
    new_id = nanoid.new()
    inst = "vtx.service." + new_id
    link = "lnk.service." + new_id + ".instanceOf.meta." + meta_id
    return {
        "mutations": [
            {"op": "create", "key": inst, "document": {"class": "service.bgCheck.instance", "isDeleted": False, "data": {}}},
            {"op": "create", "key": link, "document": {"class": "instanceOf", "isDeleted": False, "data": {}}},
        ],
        "events": [],
    }
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var linkKey string
	for _, m := range res.Mutations {
		if strings.Contains(m.Key, ".instanceOf.meta.") {
			linkKey = m.Key
		}
	}
	if linkKey == "" {
		t.Fatalf("no instanceOf→meta link produced (metaKey unreadable?); mutations: %+v", res.Mutations)
	}
	// The link must target the DDL's meta-vertex — proving the script obtained
	// vtx.meta.identity from ddl["identity"].metaKey.
	if !strings.HasSuffix(linkKey, ".instanceOf.meta.identity") {
		t.Fatalf("instanceOf link did not target the DDL meta key: %q", linkKey)
	}
}

func TestExecute_DeterministicNanoID(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    return {"mutations": [{"op": "create", "key": "vtx.identity." + nanoid.new(), "document": {"class":"identity","isDeleted":False,"data":{}}}], "events": []}
`
	sc := buildContext(script)
	res1, err1 := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err1 != nil {
		t.Fatalf("run 1: %v", err1)
	}
	res2, err2 := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err2 != nil {
		t.Fatalf("run 2: %v", err2)
	}
	if res1.Mutations[0].Key != res2.Mutations[0].Key {
		t.Fatalf("nanoid not deterministic across runs: %q vs %q",
			res1.Mutations[0].Key, res2.Mutations[0].Key)
	}
}

func TestExecute_FailCallProducesScriptError(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    fail("business rule violation: " + op.payload.name)
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %T: %v", err, err)
	}
	if sErr.Code != "ScriptError" {
		t.Fatalf("Code = %q, want ScriptError", sErr.Code)
	}
}

// ---- Sandbox-violation vectors (the four AC-required tests) ----

func TestSandbox_ForbidsLoad(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
load("net/http", "get")
def execute(state, op):
    return {"mutations": [], "events": []}
`
	_, err := exec.Execute(context.Background(), buildContext(script).Operation, HydratedState{Context: buildContext(script)})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if sErr.Code != "SandboxViolation" && sErr.Code != "ScriptError" {
		t.Fatalf("Code = %q, expected SandboxViolation or ScriptError", sErr.Code)
	}
	// The key signal: the script failed at all (didn't reach the empty return).
}

func TestSandbox_ForbidsOpen(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    f = open("/etc/passwd")
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if sErr.Code != "SandboxViolation" {
		t.Fatalf("open(): expected SandboxViolation, got %q (%s)", sErr.Code, sErr.Message)
	}
}

func TestSandbox_ForbidsOsGetenv(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    secret = os.getenv("SECRET")
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if sErr.Code != "SandboxViolation" {
		t.Fatalf("os.getenv: expected SandboxViolation, got %q (%s)", sErr.Code, sErr.Message)
	}
}

// TestSandbox_ForbidsWallClock proves a script cannot read the host wall
// clock. The `time` module is a sandboxed builtin that exposes ONLY the pure
// `rfc3339_utc(s)` normalizer (a deterministic function of its argument, like
// crypto.sha256) — it deliberately does NOT expose `now()` or any other
// wall-clock surface. Probing `time.now()` therefore fails: the module has no
// such attribute. The security property (no non-deterministic clock read)
// holds; only the error classification differs from an entirely-unbound name.
func TestSandbox_ForbidsWallClock(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    now = time.now()
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if !strings.Contains(sErr.Message, "no .now") && !strings.Contains(sErr.Message, "now") {
		t.Fatalf("time.now(): expected a no-such-attribute error (wall clock unreachable), got %q (%s)", sErr.Code, sErr.Message)
	}
}

// TestSandbox_TimeNormalizerOnly confirms the one pure function the `time`
// module DOES expose works (validates + normalizes RFC3339) — so legitimate
// timestamp normalization is available without exposing the wall clock.
func TestSandbox_TimeNormalizerOnly(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    norm = time.rfc3339_utc("2026-06-04T23:00:00+09:00")
    return {"mutations": [], "events": [{"class": "health.probe", "data": {"norm": norm}}]}
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("time.rfc3339_utc must work: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Data["norm"] != "2026-06-04T14:00:00Z" {
		t.Fatalf("time.rfc3339_utc normalize = %v, want 2026-06-04T14:00:00Z", res.Events)
	}
}

func TestSandbox_PermittedOpsWork(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    x = 1 + 2
    s = "hello " + "world"
    items = [i for i in range(3)]
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("permitted ops should not error: %v", err)
	}
}

// ---- Timeout ----

func TestExecute_Timeout(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(50*time.Millisecond, 1_000_000_000), testLogger())
	script := `
def execute(state, op):
    n = 0
    for i in range(10000000):
        n = n + i
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) {
		t.Fatalf("expected *ScriptError, got %v", err)
	}
	if sErr.Code != "ScriptTimeout" && sErr.Code != "ScriptError" {
		t.Fatalf("expected timeout, got %q (%s)", sErr.Code, sErr.Message)
	}
}

// ---- Return shape validation ----

func TestExecute_InvalidReturnShape_NotDict(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    return [1, 2, 3]
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) || sErr.Code != "InvalidReturnShape" {
		t.Fatalf("expected InvalidReturnShape, got %v", err)
	}
}

func TestExecute_InvalidMutationOp(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    return {"mutations": [{"op": "delete", "key": "vtx.x.AAAAAAAAAAAAAAAAAAAA"}], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) || sErr.Code != "InvalidReturnShape" {
		t.Fatalf("expected InvalidReturnShape for bad op, got %v", err)
	}
}

func TestExecute_NoExecuteFunction(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def something_else(state, op):
    return {"mutations": [], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) || sErr.Code != "InvalidReturnShape" {
		t.Fatalf("expected InvalidReturnShape for missing execute, got %v", err)
	}
}

// TestParseMutations_ExpectedRevision verifies that a mutation dict containing
// an integer "expectedRevision" field is correctly parsed into a MutationOp
// with a non-nil ExpectedRevision (MF-1, Story 5.3).
func TestParseMutations_ExpectedRevision(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    m = {"op": "tombstone", "key": "vtx.meta.AAAAAAAAAAAAAAAAAAAA"}
    m["expectedRevision"] = 42
    return {"mutations": [m], "events": []}
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(res.Mutations))
	}
	m := res.Mutations[0]
	if m.ExpectedRevision == nil {
		t.Fatal("ExpectedRevision is nil — parseMutations failed to extract expectedRevision from Starlark dict")
	}
	if *m.ExpectedRevision != 42 {
		t.Fatalf("ExpectedRevision: got %d want 42", *m.ExpectedRevision)
	}
}

// TestParseMutations_TombstoneBare verifies a huskless tombstone (no
// "document" key at all — the shape every in-repo emitter produces post
// tombstone-body-preservation-design.md Fire 1) parses cleanly with a nil
// Document.
func TestParseMutations_TombstoneBare(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	script := `
def execute(state, op):
    return {"mutations": [{"op": "tombstone", "key": "vtx.meta.AAAAAAAAAAAAAAAAAAAA"}], "events": []}
`
	sc := buildContext(script)
	res, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(res.Mutations))
	}
	if m := res.Mutations[0]; m.Document != nil {
		t.Fatalf("expected nil Document on a bare tombstone, got %+v", m.Document)
	}
}

// TestParseMutations_TombstoneWithDocumentRejects verifies the Fire 2
// posture (tombstone-body-preservation-design.md §5/§6): a tombstone
// mutation carrying a "document" is rejected with InvalidReturnShape rather
// than silently dropped.
func TestParseMutations_TombstoneWithDocumentRejects(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	const tombKey = "vtx.meta.BBBBBBBBBBBBBBBBBBBB"
	script := `
def execute(state, op):
    return {"mutations": [{"op": "tombstone", "key": "` + tombKey + `",
        "document": {"isDeleted": True, "data": {}}}], "events": []}
`
	sc := buildContext(script)
	_, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc})
	if err == nil {
		t.Fatal("expected a rejection, got nil error")
	}
	var scriptErr *ScriptError
	if !errors.As(err, &scriptErr) {
		t.Fatalf("expected a *ScriptError, got %T: %v", err, err)
	}
	if scriptErr.Code != "InvalidReturnShape" {
		t.Fatalf("expected InvalidReturnShape, got %q", scriptErr.Code)
	}
	if !strings.Contains(scriptErr.Message, "tombstone") {
		t.Fatalf("expected message to name the tombstone violation, got: %s", scriptErr.Message)
	}
}

// ---- Step-5 wall observation (the ring, the timeout counter, the log line) ----

// step5LogCapture returns a logger writing every Info-and-above record into the
// returned buffer, so a test can assert on the attributes of the step-5 lines.
func step5LogCapture() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})), buf
}

// step5ReadingContext builds an executor context whose script issues one live
// read and one listing, wired to fakes that serve both and to a read recorder.
func step5ReadingContext() ScriptContext {
	liveKey := "vtx.identity." + readRecLiveID
	hub := "vtx.provider." + linkProvID
	sc := buildContext(`
def execute(state, op):
    v = kv.Read("` + liveKey + `")
    page, nxt = kv.Links("` + hub + `", "hasBooking", "out")
    return {"mutations": [], "events": []}
`)
	sc.KVReader = &fakeKVReader{docs: map[string]*VertexDoc{
		liveKey: {Key: liveKey, Class: "identity", Data: map[string]interface{}{}},
	}}
	sc.LinkLister = &fakeLinkLister{links: []LinkDoc{{
		Key:          "lnk.provider." + linkProvID + ".hasBooking.appointment." + linkApptID1,
		Class:        "hasBooking",
		SourceVertex: hub,
		TargetVertex: "vtx.appointment." + linkApptID1,
	}}}
	sc.ReadRecorder = &scriptReadRecorder{}
	return sc
}

// TestStep5Stats_FreshExecutorHasNoSamples — the zero-sample state the heartbeat
// renders as null means. It also pins that Step5Stats on a never-run executor
// does not panic on an uninitialised ring.
func TestStep5Stats_FreshExecutorHasNoSamples(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	got := exec.Step5Stats()
	if got.Count != 0 || got.TimeoutsTotal != 0 {
		t.Fatalf("fresh executor stats = %+v, want zero count and zero timeouts", got)
	}
	// A struct-literal executor must observe too — the ring initialises lazily.
	lit := &ExecutorImpl{Runner: NewStarlarkRunner(0, 0), Logger: testLogger()}
	if s := lit.Step5Stats(); s.Count != 0 {
		t.Fatalf("literal executor stats = %+v, want zero count", s)
	}
	sc := buildContext(`
def execute(state, op):
    return {"mutations": [], "events": []}
`)
	if _, err := lit.Execute(context.Background(), sc.Operation, HydratedState{Context: sc}); err != nil {
		t.Fatalf("Execute on a literal executor: %v", err)
	}
	if s := lit.Step5Stats(); s.Count != 1 {
		t.Fatalf("literal executor recorded %d samples, want 1 — the ring did not initialise lazily", s.Count)
	}
}

// TestExecute_RecordsWallAndReadCounts — a successful execution is one sample
// carrying its wall time and the round trips the script paid for inside it,
// read off the recorder the ScriptContext carries.
func TestExecute_RecordsWallAndReadCounts(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	sc := step5ReadingContext()
	if _, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := exec.Step5Stats()
	if got.Count != 1 {
		t.Fatalf("Count = %d, want 1 sample per execution", got.Count)
	}
	if got.MeanLiveReads != 1 {
		t.Fatalf("MeanLiveReads = %v, want 1 (the script issued one live GET)", got.MeanLiveReads)
	}
	if got.MeanListings != 1 {
		t.Fatalf("MeanListings = %v, want 1 (the script issued one listing)", got.MeanListings)
	}
	if got.Mean <= 0 {
		t.Fatalf("Mean = %v, want a positive wall time", got.Mean)
	}
	if got.TimeoutsTotal != 0 {
		t.Fatalf("TimeoutsTotal = %d, want 0 for a clean execution", got.TimeoutsTotal)
	}
}

// TestExecute_NoScriptSourceIsNotASample — the pre-Run early return never
// reached the runner, so it has no wall time to report. Counting it would
// dilute the mean with zeros that describe no execution.
func TestExecute_NoScriptSourceIsNotASample(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
	sc := buildContext("")
	if _, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc}); err == nil {
		t.Fatal("expected an error for a hydrated state with no script source")
	}
	if got := exec.Step5Stats(); got.Count != 0 {
		t.Fatalf("Count = %d, want 0 — the pre-Run return is not a sample", got.Count)
	}
}

// TestExecute_TimeoutIsASampleAndIncrementsTimeouts — a ScriptTimeout is the
// most expensive execution there is, so it must be in the latency window; and
// it bumps the cumulative counter the heartbeat exports. Driven through a hung
// kv.Read against the executor's own 50ms wall budget (CI's default wall is
// 5s, which would mask it).
func TestExecute_TimeoutIsASampleAndIncrementsTimeouts(t *testing.T) {
	exec := NewExecutor(NewStarlarkRunner(50*time.Millisecond, 0), testLogger())
	sc := buildContext(`
def execute(state, op):
    v = kv.Read("vtx.task.slow")
    return {"mutations": [], "events": []}
`)
	sc.KVReader = blockingKVReader{}
	sc.ReadRecorder = &scriptReadRecorder{}
	// Parent deadline well above the budget so a broken wall ctx overruns
	// visibly rather than hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := exec.Execute(ctx, sc.Operation, HydratedState{Context: sc})
	var sErr *ScriptError
	if !errors.As(err, &sErr) || sErr.Code != "ScriptTimeout" {
		t.Fatalf("want a ScriptTimeout, got %T: %v", err, err)
	}
	got := exec.Step5Stats()
	if got.Count != 1 {
		t.Fatalf("Count = %d, want 1 — an aborted execution still ran", got.Count)
	}
	if got.TimeoutsTotal != 1 {
		t.Fatalf("TimeoutsTotal = %d, want 1", got.TimeoutsTotal)
	}
	if got.Mean < 50*time.Millisecond {
		t.Fatalf("Mean = %v, want at least the 50ms wall budget the run burned", got.Mean)
	}

	// A non-timeout abort is a sample but NOT a timeout: the counter must
	// separate the two, else "timeouts" would just re-count failures.
	failing := buildContext(`
def execute(state, op):
    fail("deliberate")
`)
	if _, err := exec.Execute(context.Background(), failing.Operation, HydratedState{Context: failing}); err == nil {
		t.Fatal("expected the script to fail")
	}
	got = exec.Step5Stats()
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2 — a failing script still ran", got.Count)
	}
	if got.TimeoutsTotal != 1 {
		t.Fatalf("TimeoutsTotal = %d, want 1 — a plain ScriptError is not a timeout", got.TimeoutsTotal)
	}
}

// TestExecute_LogsWallAndReadCountsOnBothPaths — the step-5 line is where an
// operator reads a single operation's cost, so both the executed line and the
// aborted line must carry the wall time and the two round-trip counts.
func TestExecute_LogsWallAndReadCountsOnBothPaths(t *testing.T) {
	logger, buf := step5LogCapture()
	exec := NewExecutor(NewStarlarkRunner(0, 0), logger)

	sc := step5ReadingContext()
	if _, err := exec.Execute(context.Background(), sc.Operation, HydratedState{Context: sc}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "step 5: executed") {
		t.Fatalf("no executed line logged: %q", line)
	}
	for _, attr := range []string{"wallMs=", "liveReads=1", "listings=1"} {
		if !strings.Contains(line, attr) {
			t.Errorf("executed line missing %q: %s", attr, line)
		}
	}

	buf.Reset()
	failing := buildContext(`
def execute(state, op):
    fail("deliberate")
`)
	failing.ReadRecorder = &scriptReadRecorder{}
	if _, err := exec.Execute(context.Background(), failing.Operation, HydratedState{Context: failing}); err == nil {
		t.Fatal("expected the script to fail")
	}
	line = buf.String()
	if !strings.Contains(line, "step 5: aborted") {
		t.Fatalf("no aborted line logged: %q", line)
	}
	for _, attr := range []string{"wallMs=", "liveReads=0", "listings=0", "code=ScriptError"} {
		if !strings.Contains(line, attr) {
			t.Errorf("aborted line missing %q: %s", attr, line)
		}
	}
}
