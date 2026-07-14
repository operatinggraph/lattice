package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Tests for the Contract #2 §2.5 lazy on-demand `kv.Read()` Starlark builtin
// (starlark_kv.go): the read-before-create idempotency seam.
//
// The unit tests drive a real StarlarkRunner with a fake ScriptKVReader and
// observe the read result through the script's returned events. The final test
// exercises the production connKVReader adapter against a real embedded Core KV.

// fakeKVReader is an in-memory ScriptKVReader. A key present in docs returns its
// doc; a key absent returns (nil, nil) (the absent/tombstoned signal); a non-nil
// err returns that error for every read. It records every key it was asked for
// so a test can prove the cache-first path skipped it.
type fakeKVReader struct {
	docs  map[string]*VertexDoc
	err   error
	calls []string
}

func (f *fakeKVReader) ReadVertex(_ context.Context, key string) (*VertexDoc, error) {
	f.calls = append(f.calls, key)
	if f.err != nil {
		return nil, f.err
	}
	if d, ok := f.docs[key]; ok {
		return d, nil
	}
	return nil, nil
}

// runKVScript runs source against sc with a default-budget runner, supplying a
// minimal operation envelope when the test left one unset.
func runKVScript(t *testing.T, sc ScriptContext, source string) (ScriptResult, error) {
	t.Helper()
	sc.ScriptSource = source
	if sc.Operation == nil {
		sc.Operation = &OperationEnvelope{
			RequestID: "req-kv-test", Lane: LaneDefault, OperationType: "X",
			Actor: "a", SubmittedAt: "t", Payload: []byte("{}"),
		}
	}
	return NewStarlarkRunner(0, 0).Run(context.Background(), sc)
}

// TestKVRead_AbsentReturnsNone — a read of a key the reader does not have yields
// None, so the script can branch into its create path. The load-bearing case
// for idempotent create: absence is graceful, not a fatal HydrationMiss.
func TestKVRead_AbsentReturnsNone(t *testing.T) {
	sc := ScriptContext{KVReader: &fakeKVReader{docs: map[string]*VertexDoc{}}}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("vtx.task.missing")
    cls = "none" if v == None else "present"
    return {"mutations": [], "events": [{"class": cls}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "none" {
		t.Fatalf("expected one 'none' event, got %+v", res.Events)
	}
}

// TestKVRead_PresentReturnsProjectedDoc — a present key returns a struct with the
// same shape as a `state` entry: .class, .isDeleted, .revision, .data[...].
func TestKVRead_PresentReturnsProjectedDoc(t *testing.T) {
	sc := ScriptContext{KVReader: &fakeKVReader{docs: map[string]*VertexDoc{
		"vtx.task.t1": {
			Key: "vtx.task.t1", Class: "task", IsDeleted: false, Revision: 7,
			Data: map[string]interface{}{"status": "open"},
		},
	}}}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("vtx.task.t1")
    return {"mutations": [], "events": [{"class": "read", "data": {
        "cls": getattr(v, "class"), "del": v.isDeleted, "rev": v.revision, "status": v.data["status"],
    }}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	d := res.Events[0].Data
	if d["cls"] != "task" {
		t.Errorf("class = %v, want task", d["cls"])
	}
	if d["del"] != false {
		t.Errorf("isDeleted = %v, want false", d["del"])
	}
	if d["rev"] != int64(7) {
		t.Errorf("revision = %v (%T), want int64(7)", d["rev"], d["rev"])
	}
	if d["status"] != "open" {
		t.Errorf("data.status = %v, want open", d["status"])
	}
}

// TestKVRead_CacheFirstSkipsReader — a key already in the hydrated working set
// (declared via contextHint.reads, pre-fetched at step 4) is served from the
// cache with NO reader round-trip (§2.5 "Starlark reads hit the cache"). Proven
// two ways: the reader is never called, and the value returned is the hydrated
// one, not the (deliberately divergent) value the reader holds.
func TestKVRead_CacheFirstSkipsReader(t *testing.T) {
	reader := &fakeKVReader{docs: map[string]*VertexDoc{
		"vtx.task.cached": {Key: "vtx.task.cached", Class: "from-reader-WRONG"},
	}}
	sc := ScriptContext{
		Hydrated: map[string]VertexDoc{
			"vtx.task.cached": {
				Key: "vtx.task.cached", Class: "task", Revision: 3,
				Data: map[string]interface{}{"v": "hydrated"},
			},
		},
		KVReader: reader,
	}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("vtx.task.cached")
    return {"mutations": [], "events": [{"class": "read", "data": {"cls": getattr(v, "class"), "v": v.data["v"]}}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("cache hit must skip the reader, but it was called for: %v", reader.calls)
	}
	d := res.Events[0].Data
	if d["cls"] != "task" || d["v"] != "hydrated" {
		t.Fatalf("expected hydrated value, got %+v", d)
	}
}

// TestKVRead_MixedCachedAndOnDemand — the two §2.5 paths coexist correctly in
// ONE execution: a contextHint-hydrated key is served from the cache (reader
// untouched), an undeclared present key falls through to the reader, and an
// undeclared absent key falls through and yields None. Guards against a
// cache-first short-circuit accidentally suppressing later on-demand reads.
func TestKVRead_MixedCachedAndOnDemand(t *testing.T) {
	reader := &fakeKVReader{docs: map[string]*VertexDoc{
		"vtx.task.live": {Key: "vtx.task.live", Class: "task", Revision: 5, Data: map[string]interface{}{"src": "reader"}},
	}}
	sc := ScriptContext{
		Hydrated: map[string]VertexDoc{
			"vtx.task.cached": {Key: "vtx.task.cached", Class: "task", Revision: 2, Data: map[string]interface{}{"src": "cache"}},
		},
		KVReader: reader,
	}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    c = kv.Read("vtx.task.cached")
    l = kv.Read("vtx.task.live")
    m = kv.Read("vtx.task.missing")
    return {"mutations": [], "events": [{"class": "read", "data": {
        "cached": c.data["src"], "live": l.data["src"], "missing": m == None,
    }}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	d := res.Events[0].Data
	if d["cached"] != "cache" {
		t.Errorf("cached key: got %v, want the hydrated value 'cache'", d["cached"])
	}
	if d["live"] != "reader" {
		t.Errorf("on-demand present key: got %v, want 'reader'", d["live"])
	}
	if d["missing"] != true {
		t.Errorf("on-demand absent key: got %v, want None", d["missing"])
	}
	// The reader is consulted for exactly the two undeclared keys — never the
	// hydrated one.
	if len(reader.calls) != 2 {
		t.Fatalf("reader calls = %v, want exactly the two undeclared keys", reader.calls)
	}
	for _, c := range reader.calls {
		if c == "vtx.task.cached" {
			t.Fatalf("reader was called for the cached key: %v", reader.calls)
		}
	}
}

// blockingKVReader blocks until its context is cancelled, then returns the
// context error — a stand-in for a hung Core KV read.
type blockingKVReader struct{}

func (blockingKVReader) ReadVertex(ctx context.Context, _ string) (*VertexDoc, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestKVRead_SlowReadHitsWallBudget — a hung on-demand read is bounded by the
// script wall budget and classified as ScriptTimeout. The elapsed-time guard
// also proves the wall-budget context is actually threaded into kv.Read: if it
// were not, the read would block on the (longer) parent ctx and overrun.
func TestKVRead_SlowReadHitsWallBudget(t *testing.T) {
	sc := ScriptContext{KVReader: blockingKVReader{}}
	sc.ScriptSource = `
def execute(state, op):
    v = kv.Read("vtx.task.slow")
    return {"mutations": [], "events": []}
`
	sc.Operation = &OperationEnvelope{
		RequestID: "req-slow", Lane: LaneDefault, OperationType: "X",
		Actor: "a", SubmittedAt: "t", Payload: []byte("{}"),
	}
	// Parent deadline well above the 50ms budget so a broken (un-threaded) ctx
	// would visibly overrun rather than hang forever.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	_, err := NewStarlarkRunner(50*time.Millisecond, 0).Run(ctx, sc)
	elapsed := time.Since(start)

	se, ok := err.(*ScriptError)
	if !ok {
		t.Fatalf("want *ScriptError, got %T: %v", err, err)
	}
	if se.Code != "ScriptTimeout" {
		t.Fatalf("Code = %q, want ScriptTimeout", se.Code)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("kv.Read took %s — the 50ms wall-budget ctx is not threaded into the read", elapsed)
	}
}

// TestKVRead_LogicallyDeletedReturnsDocWithFlag — a logically-deleted vertex
// (isDeleted=true, still a live KV envelope) returns a non-nil doc carrying the
// flag, NOT None. Mirrors how `state` surfaces deletes; the script — not the
// primitive — decides whether a deleted record counts as "present".
func TestKVRead_LogicallyDeletedReturnsDocWithFlag(t *testing.T) {
	sc := ScriptContext{KVReader: &fakeKVReader{docs: map[string]*VertexDoc{
		"vtx.task.del": {Key: "vtx.task.del", Class: "task", IsDeleted: true, Revision: 9, Data: map[string]interface{}{}},
	}}}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("vtx.task.del")
    if v == None:
        return {"mutations": [], "events": [{"class": "none"}]}
    return {"mutations": [], "events": [{"class": "present", "data": {"del": v.isDeleted}}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Events[0].Class != "present" || res.Events[0].Data["del"] != true {
		t.Fatalf("logical delete must surface as a present doc with isDeleted=true, got %+v", res.Events[0])
	}
}

// TestKVRead_NoReaderWiredErrors — an on-demand read (cache miss) with no reader
// wired is a script error, not a silent None. Guards against a misconfigured
// pipeline masquerading as "absent" and wrongly triggering a create.
func TestKVRead_NoReaderWiredErrors(t *testing.T) {
	sc := ScriptContext{} // no KVReader, no Hydrated
	_, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("vtx.task.x")
    return {"mutations": [], "events": [{"class": "x"}]}
`)
	if err == nil {
		t.Fatalf("expected error for on-demand read with no reader wired")
	}
	se, ok := err.(*ScriptError)
	if !ok {
		t.Fatalf("want *ScriptError, got %T: %v", err, err)
	}
	if !strings.Contains(se.Message, "no Core KV reader") {
		t.Fatalf("error message = %q, want it to mention the missing reader", se.Message)
	}
}

// TestKVRead_ReaderErrorPropagates — a substrate-level read failure (not a
// not-found) surfaces as a ScriptError rather than being swallowed as None.
func TestKVRead_ReaderErrorPropagates(t *testing.T) {
	sc := ScriptContext{KVReader: &fakeKVReader{err: errors.New("boom-substrate")}}
	_, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("vtx.task.x")
    return {"mutations": [], "events": [{"class": "x"}]}
`)
	if err == nil {
		t.Fatalf("expected the reader error to propagate")
	}
	if !strings.Contains(err.Error(), "boom-substrate") {
		t.Fatalf("error = %q, want it to carry the underlying cause", err.Error())
	}
}

// TestKVRead_ArgValidation — arity/type/empty-key misuse fails fast. A fake
// reader is supplied so failures are argument validation, not a missing reader.
func TestKVRead_ArgValidation(t *testing.T) {
	cases := []struct{ name, body string }{
		{"no args", `kv.Read()`},
		{"too many args", `kv.Read("a", "b")`},
		{"keyword arg", `kv.Read(key="a")`},
		{"non-string", `kv.Read(42)`},
		{"empty string", `kv.Read("")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := ScriptContext{KVReader: &fakeKVReader{docs: map[string]*VertexDoc{}}}
			_, err := runKVScript(t, sc, "def execute(state, op):\n    "+tc.body+"\n    return {\"mutations\": [], \"events\": []}")
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
		})
	}
}

// TestConnKVReader_AgainstCoreKV exercises the production connKVReader adapter
// against a real embedded Core KV: absent → (nil,nil); a live vertex → a parsed
// doc with the read revision; a logically-deleted vertex → a non-nil doc with
// isDeleted=true (NOT not-found — Conn.KVGet returns logical deletes normally).
func TestConnKVReader_AgainstCoreKV(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	r := connKVReader{conn: conn, bucket: testCoreBucket}

	// Absent → (nil, nil).
	doc, err := r.ReadVertex(ctx, "vtx.task.nope")
	if err != nil || doc != nil {
		t.Fatalf("absent: got (doc=%v, err=%v), want (nil, nil)", doc, err)
	}

	// Live vertex → parsed doc, revision threaded.
	rev, err := conn.KVCreate(ctx, testCoreBucket, "vtx.task.live",
		[]byte(`{"class":"task","isDeleted":false,"data":{"status":"open"}}`))
	if err != nil {
		t.Fatalf("seed live: %v", err)
	}
	doc, err = r.ReadVertex(ctx, "vtx.task.live")
	if err != nil || doc == nil {
		t.Fatalf("live: got (doc=%v, err=%v), want a doc", doc, err)
	}
	if doc.Class != "task" || doc.IsDeleted || doc.Data["status"] != "open" || doc.Revision != rev {
		t.Fatalf("live doc mismatch: %+v (rev want %d)", doc, rev)
	}

	// Logically-deleted vertex → non-nil doc with isDeleted=true.
	if _, err := conn.KVCreate(ctx, testCoreBucket, "vtx.task.del",
		[]byte(`{"class":"task","isDeleted":true,"data":{}}`)); err != nil {
		t.Fatalf("seed deleted: %v", err)
	}
	doc, err = r.ReadVertex(ctx, "vtx.task.del")
	if err != nil || doc == nil {
		t.Fatalf("logically-deleted: got (doc=%v, err=%v), want a non-nil doc", doc, err)
	}
	if !doc.IsDeleted {
		t.Fatalf("logically-deleted: isDeleted = false, want true (must surface, not nil)")
	}
}

// ---------------------------------------------------------------------------
// kv.Links (Contract #2 §2.5.1) — the bounded, paged op-time link enumeration.
// ---------------------------------------------------------------------------

// Valid 20-char NanoIDs for link-key construction in these tests.
const (
	linkProvID  = "Pv4kPmRtw9nbCxz5vQ2y"
	linkApptID1 = "Aa6mP3qBn4rT8wYxK7Vc"
	linkApptID2 = "Ab2Pn6mQrtwzKbcXvP3T"
	linkApptID3 = "Ac8Qm5rDp2sV7uXyL4Wt"
	linkApptID4 = "Ad3Nk7tFq9wZ6bcMv1Pr"
)

// fakeLinkLister is an in-memory ScriptLinkLister. It records the (filter,
// cursor, limit) of every call so a test can assert the builtin constructed the
// right server-side subject filter, and returns a canned page + nextCursor.
type fakeLinkLister struct {
	links      []LinkDoc
	nextCursor string
	err        error
	calls      []linkCall
}

type linkCall struct {
	filter string
	cursor string
	limit  int
}

func (f *fakeLinkLister) ListLinks(_ context.Context, filter, cursor string, limit int) ([]LinkDoc, string, error) {
	f.calls = append(f.calls, linkCall{filter, cursor, limit})
	if f.err != nil {
		return nil, "", f.err
	}
	return f.links, f.nextCursor, nil
}

// TestKVLinks_OutFilterConstruction — direction "out" puts the hub id in the
// prefix: lnk.<hubType>.<hubId>.<relation>.> — bounded by the hub's out-degree.
func TestKVLinks_OutFilterConstruction(t *testing.T) {
	lister := &fakeLinkLister{}
	sc := ScriptContext{LinkLister: lister}
	_, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "hasBooking", "out")
    return {"mutations": [], "events": [{"class": "ok", "data": {"n": len(page)}}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "lnk.provider." + linkProvID + ".hasBooking.>"
	if len(lister.calls) != 1 || lister.calls[0].filter != want {
		t.Fatalf("filter = %+v, want %q", lister.calls, want)
	}
	if lister.calls[0].cursor != "" || lister.calls[0].limit != defaultLinkPageLimit {
		t.Fatalf("cursor/limit = %q/%d, want \"\"/%d", lister.calls[0].cursor, lister.calls[0].limit, defaultLinkPageLimit)
	}
}

// TestKVLinks_InFilterConstruction — direction "in" puts the hub id in the
// suffix and wildcards the source: lnk.*.*.<relation>.<hubType>.<hubId> —
// bounded by the hub's in-degree. This is the load-bearing mid-subject-wildcard
// case the ratification revision corrected the original draft on.
func TestKVLinks_InFilterConstruction(t *testing.T) {
	lister := &fakeLinkLister{}
	sc := ScriptContext{LinkLister: lister}
	_, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "withProvider", "in")
    return {"mutations": [], "events": [{"class": "ok", "data": {"n": len(page)}}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "lnk.*.*.withProvider.provider." + linkProvID
	if len(lister.calls) != 1 || lister.calls[0].filter != want {
		t.Fatalf("filter = %+v, want %q", lister.calls, want)
	}
}

// TestKVLinks_ReturnsProjectedLinkDocs — a page surfaces each link as a struct
// with the full link envelope projection: .key/.class/.isDeleted/.data/.revision
// plus the link-only .sourceVertex/.targetVertex.
func TestKVLinks_ReturnsProjectedLinkDocs(t *testing.T) {
	lister := &fakeLinkLister{links: []LinkDoc{{
		Key:          "lnk.provider." + linkProvID + ".hasBooking.appointment." + linkApptID1,
		Class:        "hasBooking",
		IsDeleted:    false,
		Revision:     11,
		Data:         map[string]interface{}{"note": "first"},
		SourceVertex: "vtx.provider." + linkProvID,
		TargetVertex: "vtx.appointment." + linkApptID1,
	}}}
	sc := ScriptContext{LinkLister: lister}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "hasBooking", "out")
    l = page[0]
    return {"mutations": [], "events": [{"class": "links", "data": {
        "n": len(page), "cls": getattr(l, "class"), "del": l.isDeleted, "rev": l.revision,
        "src": l.sourceVertex, "tgt": l.targetVertex, "note": l.data["note"], "more": nxt != None,
    }}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	d := res.Events[0].Data
	if d["n"] != int64(1) {
		t.Errorf("len(page) = %v, want 1", d["n"])
	}
	if d["cls"] != "hasBooking" {
		t.Errorf("class = %v, want hasBooking", d["cls"])
	}
	if d["del"] != false {
		t.Errorf("isDeleted = %v, want false", d["del"])
	}
	if d["rev"] != int64(11) {
		t.Errorf("revision = %v, want 11", d["rev"])
	}
	if d["src"] != "vtx.provider."+linkProvID {
		t.Errorf("sourceVertex = %v", d["src"])
	}
	if d["tgt"] != "vtx.appointment."+linkApptID1 {
		t.Errorf("targetVertex = %v", d["tgt"])
	}
	if d["note"] != "first" {
		t.Errorf("data.note = %v, want first", d["note"])
	}
	if d["more"] != false {
		t.Errorf("more = %v, want false (no nextCursor)", d["more"])
	}
}

// TestKVLinks_TombstonedReturned — a logically-deleted link is RETURNED carrying
// isDeleted (the guard decides), mirroring kv.Read — never silently dropped.
func TestKVLinks_TombstonedReturned(t *testing.T) {
	lister := &fakeLinkLister{links: []LinkDoc{{
		Key:          "lnk.provider." + linkProvID + ".hasBooking.appointment." + linkApptID1,
		Class:        "hasBooking",
		IsDeleted:    true,
		SourceVertex: "vtx.provider." + linkProvID,
		TargetVertex: "vtx.appointment." + linkApptID1,
	}}}
	sc := ScriptContext{LinkLister: lister}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "hasBooking", "out")
    return {"mutations": [], "events": [{"class": "links", "data": {"n": len(page), "del": page[0].isDeleted}}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Events[0].Data["n"] != int64(1) || res.Events[0].Data["del"] != true {
		t.Fatalf("tombstoned link must be returned with isDeleted=true, got %+v", res.Events[0].Data)
	}
}

// TestKVLinks_Paging — nextCursor surfaces as a string when more remains and as
// None when exhausted; a caller-supplied cursor + limit thread through to the
// lister verbatim.
func TestKVLinks_Paging(t *testing.T) {
	// Page 1: a non-empty nextCursor surfaces as a string the script pages on.
	l1 := &fakeLinkLister{
		links:      []LinkDoc{{Key: "lnk.provider." + linkProvID + ".hasBooking.appointment." + linkApptID1, SourceVertex: "vtx.provider." + linkProvID, TargetVertex: "vtx.appointment." + linkApptID1}},
		nextCursor: "lnk.provider." + linkProvID + ".hasBooking.appointment." + linkApptID1,
	}
	sc := ScriptContext{LinkLister: l1}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "hasBooking", "out", limit=1)
    return {"mutations": [], "events": [{"class": "p", "data": {"more": nxt != None, "nxt": nxt}}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Events[0].Data["more"] != true || res.Events[0].Data["nxt"] != l1.nextCursor {
		t.Fatalf("page1: got %+v, want more=true nxt=%q", res.Events[0].Data, l1.nextCursor)
	}
	if l1.calls[0].limit != 1 {
		t.Errorf("limit passthrough = %d, want 1", l1.calls[0].limit)
	}

	// Page 2: the caller passes the cursor back; an empty nextCursor → None.
	l2 := &fakeLinkLister{links: []LinkDoc{{Key: "lnk.provider." + linkProvID + ".hasBooking.appointment." + linkApptID2, SourceVertex: "vtx.provider." + linkProvID, TargetVertex: "vtx.appointment." + linkApptID2}}}
	sc2 := ScriptContext{LinkLister: l2}
	res2, err := runKVScript(t, sc2, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "hasBooking", "out", cursor="`+l1.nextCursor+`", limit=1)
    return {"mutations": [], "events": [{"class": "p", "data": {"more": nxt != None}}]}
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Events[0].Data["more"] != false {
		t.Errorf("page2: more = %v, want false (exhausted → None)", res2.Events[0].Data["more"])
	}
	if l2.calls[0].cursor != l1.nextCursor {
		t.Errorf("cursor passthrough = %q, want %q", l2.calls[0].cursor, l1.nextCursor)
	}
}

// TestKVLinks_LimitClamp — a non-positive limit defaults; an over-large limit is
// capped at maxLinkPageLimit so one page is never unbounded.
func TestKVLinks_LimitClamp(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		want int
	}{
		{"zero defaults", "limit=0", defaultLinkPageLimit},
		{"negative defaults", "limit=-5", defaultLinkPageLimit},
		{"over-large capped", "limit=999999", maxLinkPageLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lister := &fakeLinkLister{}
			sc := ScriptContext{LinkLister: lister}
			_, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "hasBooking", "out", `+tc.arg+`)
    return {"mutations": [], "events": []}
`)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if lister.calls[0].limit != tc.want {
				t.Fatalf("limit = %d, want %d", lister.calls[0].limit, tc.want)
			}
		})
	}
}

// TestKVLinks_ArgValidation — arity/type/grammar misuse fails fast, before the
// lister is touched. A fake lister is supplied so failures are argument
// validation, not a missing lister.
func TestKVLinks_ArgValidation(t *testing.T) {
	cases := []struct{ name, body string }{
		{"no args", `kv.Links()`},
		{"missing direction", `kv.Links("vtx.provider.` + linkProvID + `", "hasBooking")`},
		{"hubKey not a vertex key", `kv.Links("lnk.provider.` + linkProvID + `", "hasBooking", "out")`},
		{"hubKey aspect key", `kv.Links("vtx.provider.` + linkProvID + `.bookings", "hasBooking", "out")`},
		{"relation uppercase first", `kv.Links("vtx.provider.` + linkProvID + `", "HasBooking", "out")`},
		{"relation with dot", `kv.Links("vtx.provider.` + linkProvID + `", "has.booking", "out")`},
		{"relation empty", `kv.Links("vtx.provider.` + linkProvID + `", "", "out")`},
		{"bad direction", `kv.Links("vtx.provider.` + linkProvID + `", "hasBooking", "sideways")`},
		{"cursor wrong type", `kv.Links("vtx.provider.` + linkProvID + `", "hasBooking", "out", cursor=42)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := ScriptContext{LinkLister: &fakeLinkLister{}}
			_, err := runKVScript(t, sc, "def execute(state, op):\n    "+tc.body+"\n    return {\"mutations\": [], \"events\": []}")
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
		})
	}
}

// TestKVLinks_NoListerWiredErrors — an enumeration with no lister wired is a
// script error, not a silent empty page (which would make a set guard pass a
// constraint it never actually checked).
func TestKVLinks_NoListerWiredErrors(t *testing.T) {
	sc := ScriptContext{} // no LinkLister
	_, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "hasBooking", "out")
    return {"mutations": [], "events": []}
`)
	if err == nil {
		t.Fatalf("expected error for enumeration with no lister wired")
	}
	se, ok := err.(*ScriptError)
	if !ok {
		t.Fatalf("want *ScriptError, got %T: %v", err, err)
	}
	if !strings.Contains(se.Message, "no Core KV link lister") {
		t.Fatalf("error message = %q, want it to mention the missing lister", se.Message)
	}
}

// TestKVLinks_ListerErrorPropagates — a substrate-level enumeration failure
// surfaces as a ScriptError rather than being swallowed as an empty page.
func TestKVLinks_ListerErrorPropagates(t *testing.T) {
	sc := ScriptContext{LinkLister: &fakeLinkLister{err: errors.New("boom-list")}}
	_, err := runKVScript(t, sc, `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.`+linkProvID+`", "hasBooking", "out")
    return {"mutations": [], "events": []}
`)
	if err == nil {
		t.Fatalf("expected the lister error to propagate")
	}
	if !strings.Contains(err.Error(), "boom-list") {
		t.Fatalf("error = %q, want it to carry the underlying cause", err.Error())
	}
}

// blockingLinkLister blocks until its context is cancelled — a stand-in for a
// hung Core KV enumeration.
type blockingLinkLister struct{}

func (blockingLinkLister) ListLinks(ctx context.Context, _, _ string, _ int) ([]LinkDoc, string, error) {
	<-ctx.Done()
	return nil, "", ctx.Err()
}

// TestKVLinks_SlowListHitsWallBudget — a hung enumeration is bounded by the
// script wall budget and classified as ScriptTimeout. The elapsed-time guard
// proves the wall-budget context is threaded into kv.Links (not the longer
// parent ctx), mirroring the kv.Read budget test.
func TestKVLinks_SlowListHitsWallBudget(t *testing.T) {
	sc := ScriptContext{LinkLister: blockingLinkLister{}}
	sc.ScriptSource = `
def execute(state, op):
    page, nxt = kv.Links("vtx.provider.` + linkProvID + `", "hasBooking", "out")
    return {"mutations": [], "events": []}
`
	sc.Operation = &OperationEnvelope{
		RequestID: "req-slow-links", Lane: LaneDefault, OperationType: "X",
		Actor: "a", SubmittedAt: "t", Payload: []byte("{}"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	_, err := NewStarlarkRunner(50*time.Millisecond, 0).Run(ctx, sc)
	elapsed := time.Since(start)

	se, ok := err.(*ScriptError)
	if !ok {
		t.Fatalf("want *ScriptError, got %T: %v", err, err)
	}
	if se.Code != "ScriptTimeout" {
		t.Fatalf("Code = %q, want ScriptTimeout", se.Code)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("kv.Links took %s — the 50ms wall-budget ctx is not threaded into the enumeration", elapsed)
	}
}

// TestConnLinkLister_AgainstCoreKV exercises the production connLinkLister +
// substrate KVListKeysFilter against a real embedded Core KV: it proves the
// server-side subject filter works in BOTH directions (incl. the mid-subject
// wildcard the "in" filter relies on), returns tombstoned links, respects the
// key-token boundary (hasBooking ≠ hasBookingExtra), and pages deterministically.
func TestConnLinkLister_AgainstCoreKV(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	lister := connLinkLister{conn: conn, bucket: testCoreBucket}

	provV := "vtx.provider." + linkProvID
	seed := func(key, body string) {
		t.Helper()
		if _, err := conn.KVCreate(ctx, testCoreBucket, key, []byte(body)); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	// Outbound: provider hasBooking appointment (provider is the source).
	seed("lnk.provider."+linkProvID+".hasBooking.appointment."+linkApptID1, `{"class":"hasBooking","isDeleted":false,"data":{"slot":"a"}}`)
	seed("lnk.provider."+linkProvID+".hasBooking.appointment."+linkApptID2, `{"class":"hasBooking","isDeleted":false,"data":{"slot":"b"}}`)
	// A tombstoned outbound link — must be RETURNED carrying isDeleted.
	seed("lnk.provider."+linkProvID+".hasBooking.appointment."+linkApptID3, `{"class":"hasBooking","isDeleted":true,"data":{}}`)
	// A different relation sharing the hasBooking prefix — must NOT match the
	// hasBooking filter (the trailing-dot token boundary).
	seed("lnk.provider."+linkProvID+".hasBookingExtra.appointment."+linkApptID4, `{"class":"hasBookingExtra","isDeleted":false,"data":{}}`)
	// Inbound: appointment withProvider provider (appointment is the source) —
	// the provider sits in the suffix, enumerated via the mid-subject wildcard.
	seed("lnk.appointment."+linkApptID1+".withProvider.provider."+linkProvID, `{"class":"withProvider","isDeleted":false,"data":{}}`)
	seed("lnk.appointment."+linkApptID2+".withProvider.provider."+linkProvID, `{"class":"withProvider","isDeleted":false,"data":{}}`)

	// --- Outbound enumeration ---
	outFilter := "lnk.provider." + linkProvID + ".hasBooking.>"
	links, next, err := lister.ListLinks(ctx, outFilter, "", 10)
	if err != nil {
		t.Fatalf("out list: %v", err)
	}
	if next != "" {
		t.Errorf("out: nextCursor = %q, want \"\" (all in one page)", next)
	}
	gotOut := map[string]bool{}
	var sawTombstone bool
	for _, l := range links {
		gotOut[l.TargetVertex] = true
		if l.SourceVertex != provV {
			t.Errorf("out: sourceVertex = %q, want %q", l.SourceVertex, provV)
		}
		if l.Class != "hasBooking" {
			t.Errorf("out: class = %q, want hasBooking (Extra leaked past the token boundary?)", l.Class)
		}
		if l.TargetVertex == "vtx.appointment."+linkApptID3 {
			sawTombstone = l.IsDeleted
		}
	}
	if len(links) != 3 {
		t.Fatalf("out: %d links, want 3 (2 live + 1 tombstoned, NOT hasBookingExtra)", len(links))
	}
	for _, id := range []string{linkApptID1, linkApptID2, linkApptID3} {
		if !gotOut["vtx.appointment."+id] {
			t.Errorf("out: missing target appointment.%s", id)
		}
	}
	if gotOut["vtx.appointment."+linkApptID4] {
		t.Errorf("out: hasBookingExtra link leaked past the hasBooking token boundary")
	}
	if !sawTombstone {
		t.Errorf("out: tombstoned link not returned with isDeleted=true")
	}

	// --- Inbound enumeration (mid-subject wildcard) ---
	inFilter := "lnk.*.*.withProvider.provider." + linkProvID
	inLinks, _, err := lister.ListLinks(ctx, inFilter, "", 10)
	if err != nil {
		t.Fatalf("in list (mid-subject wildcard): %v", err)
	}
	if len(inLinks) != 2 {
		t.Fatalf("in: %d links, want 2", len(inLinks))
	}
	gotIn := map[string]bool{}
	for _, l := range inLinks {
		gotIn[l.SourceVertex] = true
		if l.TargetVertex != provV {
			t.Errorf("in: targetVertex = %q, want %q", l.TargetVertex, provV)
		}
	}
	for _, id := range []string{linkApptID1, linkApptID2} {
		if !gotIn["vtx.appointment."+id] {
			t.Errorf("in: missing source appointment.%s", id)
		}
	}

	// --- Deterministic paging across the 3 outbound links (limit 2) ---
	p1, c1, err := lister.ListLinks(ctx, outFilter, "", 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(p1) != 2 || c1 == "" {
		t.Fatalf("page1: %d links, cursor=%q, want 2 links + a non-empty cursor", len(p1), c1)
	}
	p2, c2, err := lister.ListLinks(ctx, outFilter, c1, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(p2) != 1 || c2 != "" {
		t.Fatalf("page2: %d links, cursor=%q, want 1 link + exhausted cursor", len(p2), c2)
	}
	// The two pages must partition the set with no overlap and no gap.
	seen := map[string]bool{}
	for _, l := range append(append([]LinkDoc{}, p1...), p2...) {
		if seen[l.Key] {
			t.Errorf("paging returned %q twice", l.Key)
		}
		seen[l.Key] = true
	}
	if len(seen) != 3 {
		t.Fatalf("paging covered %d distinct links, want 3", len(seen))
	}
}
