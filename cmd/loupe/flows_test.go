package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestComputeFlows(t *testing.T) {
	store := map[string][]byte{
		// A completed flow.
		"complete0000000000": []byte(`{"instance_id":"complete0000000000","pattern_ref":"vtx.meta.onboarding0000","subject_key":"vtx.identity.id1","status":"complete","started_at":"2026-07-05T10:00:00Z","ended_at":"2026-07-05T10:05:00Z","last_event_seq":42}`),
		// A failed flow.
		"failed00000000000": []byte(`{"instance_id":"failed00000000000","pattern_ref":"vtx.meta.onboarding0000","subject_key":"vtx.identity.id2","status":"failed","started_at":"2026-07-05T09:00:00Z","ended_at":"2026-07-05T09:02:00Z","failure_reason":"adapter timeout","last_event_seq":10}`),
		// A running flow Loom is genuinely running.
		"running0000000000": []byte(`{"instance_id":"running0000000000","pattern_ref":"vtx.meta.onboarding0000","subject_key":"vtx.identity.id3","status":"running","started_at":"2026-07-05T11:00:00Z","last_event_seq":5}`),
		// A running flow Loom has no record of at all — orphaned.
		"orphan00000000000": []byte(`{"instance_id":"orphan00000000000","pattern_ref":"vtx.meta.onboarding0000","subject_key":"vtx.identity.id4","status":"running","started_at":"2026-07-05T08:00:00Z","last_event_seq":3}`),
		// A running row Loom considers FINISHED — the case the old
		// membership-only badge waved through as live.
		"stale00000000000": []byte(`{"instance_id":"stale00000000000","pattern_ref":"vtx.meta.onboarding0000","subject_key":"vtx.identity.id5","status":"running","started_at":"2026-07-05T07:00:00Z","ended_at":"2026-07-05T06:00:00Z","last_event_seq":2}`),
		// A poison entry that fails to decode — must be skipped, not fatal.
		"poison00000000000": []byte(`not json`),
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
	keys := make([]string, 0, len(store))
	for k := range store {
		keys = append(keys, k)
	}
	// Loom keeps a record after an instance finishes, so its list carries
	// terminal instances alongside the running one.
	engine := map[string]string{
		"running0000000000":  "running",
		"stale00000000000":   "complete",
		"complete0000000000": "complete",
	}
	names := func(ref string) string {
		if ref == "vtx.meta.onboarding0000" {
			return "identityOnboarding"
		}
		return ""
	}

	t.Run("all rows, poison skipped, newest-started first", func(t *testing.T) {
		rows := computeFlows(keys, get, engine, true, "", names)
		if len(rows) != 5 {
			t.Fatalf("want 5 flows (poison entry skipped), got %d: %+v", len(rows), rows)
		}
		if rows[0].InstanceID != "running0000000000" {
			t.Errorf("newest-started flow should sort first, got %q", rows[0].InstanceID)
		}
	})

	t.Run("status filter limits to one status", func(t *testing.T) {
		rows := computeFlows(keys, get, engine, true, "failed", names)
		if len(rows) != 1 {
			t.Fatalf("want 1 failed flow, got %d", len(rows))
		}
		if rows[0].FailureReason != "adapter timeout" {
			t.Errorf("failure reason not decoded: %q", rows[0].FailureReason)
		}
	})

	t.Run("the pattern ref resolves to its canonical name", func(t *testing.T) {
		rows := computeFlows(keys, get, engine, true, "failed", names)
		if rows[0].PatternName != "identityOnboarding" {
			t.Errorf("patternName = %q, want the resolved canonicalName", rows[0].PatternName)
		}
		// An unresolvable ref leaves the name empty so the card can fall back
		// to the raw ref rather than rendering a blank title.
		blank := computeFlows(keys, get, engine, true, "failed", func(string) string { return "" })
		if blank[0].PatternName != "" || blank[0].PatternRef == "" {
			t.Errorf("unresolved = %+v, want an empty name over a preserved ref", blank[0])
		}
	})

	t.Run("liveness reads the engine's status, not its memory of the id", func(t *testing.T) {
		rows := computeFlows(keys, get, engine, true, "running", names)
		byID := map[string]flowRow{}
		for _, r := range rows {
			byID[r.InstanceID] = r
		}
		if len(byID) != 3 {
			t.Fatalf("want 3 running rows, got %d", len(byID))
		}
		if got := byID["running0000000000"].Liveness; got != livenessLive {
			t.Errorf("Loom is running this instance: liveness = %q, want %q", got, livenessLive)
		}
		if got := byID["orphan00000000000"].Liveness; got != livenessOrphaned {
			t.Errorf("Loom has no record of this instance: liveness = %q, want %q", got, livenessOrphaned)
		}
		// The defect this asserts against: the id IS in Loom's list, so a
		// membership-only badge called this live while Loom called it done.
		if got := byID["stale00000000000"].Liveness; got != livenessStaleHistory {
			t.Errorf("Loom considers this instance finished: liveness = %q, want %q", got, livenessStaleHistory)
		}
		if got := byID["stale00000000000"].EngineStatus; got != "complete" {
			t.Errorf("engineStatus = %q, want Loom's own answer carried through for the card", got)
		}
	})

	t.Run("terminal row is never badged even though Loom still lists it", func(t *testing.T) {
		rows := computeFlows(keys, get, engine, true, "complete", names)
		if len(rows) != 1 || rows[0].Liveness != "" {
			t.Fatalf("a terminal row must never be badged, got %+v", rows)
		}
	})

	t.Run("running row stays unbadged, not falsely orphaned, when the control read failed", func(t *testing.T) {
		rows := computeFlows(keys, get, nil, false, "running", names)
		for _, r := range rows {
			if r.Liveness != "" {
				t.Errorf("row %q should be unbadged when the engine's answer is unknown, got %q", r.InstanceID, r.Liveness)
			}
		}
	})
}

func TestFlowLiveness(t *testing.T) {
	cases := []struct {
		name                    string
		rowStatus, engineStatus string
		engineKnown, engineHas  bool
		want                    string
	}{
		{"running + engine running", "running", "running", true, true, livenessLive},
		{"running + engine complete", "running", "complete", true, true, livenessStaleHistory},
		{"running + engine failed", "running", "failed", true, true, livenessStaleHistory},
		{"running + engine has no record", "running", "", true, false, livenessOrphaned},
		{"running + engine unknown", "running", "", false, false, ""},
		{"terminal row is never badged", "complete", "running", true, true, ""},
		{"failed row is never badged", "failed", "running", true, true, ""},
		// A status Loom does not define is not treated as terminal: guessing
		// "done" from an unrecognized value would resurrect the very
		// false-negative this classification exists to remove.
		{"running + unrecognized engine status", "running", "wat", true, true, livenessLive},
	}
	for _, c := range cases {
		if got := flowLiveness(c.rowStatus, c.engineStatus, c.engineKnown, c.engineHas); got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestComputeTimeline(t *testing.T) {
	rfc := func(s string) time.Time { tm, _ := time.Parse(time.RFC3339, s); return tm }
	store := map[string][]byte{
		// Fully inside the window.
		"inside000000000000": []byte(`{"instance_id":"inside000000000000","pattern_ref":"onboarding","status":"complete","started_at":"2026-07-05T10:10:00Z","ended_at":"2026-07-05T10:20:00Z"}`),
		// Ends before the window starts — no overlap.
		"before0000000000000": []byte(`{"instance_id":"before0000000000000","pattern_ref":"onboarding","status":"complete","started_at":"2026-07-05T09:00:00Z","ended_at":"2026-07-05T09:30:00Z"}`),
		// Starts after the window ends — no overlap.
		"after00000000000000": []byte(`{"instance_id":"after00000000000000","pattern_ref":"onboarding","status":"complete","started_at":"2026-07-05T11:30:00Z","ended_at":"2026-07-05T11:45:00Z"}`),
		// Still running (no ended_at) and started before the window — live
		// through the window's own end (treated as open).
		"running0000000000000": []byte(`{"instance_id":"running0000000000000","pattern_ref":"onboarding","status":"running","started_at":"2026-07-05T10:55:00Z"}`),
		// Unparsable started_at — skipped, never fatal to the rest.
		"badstart000000000000": []byte(`{"instance_id":"badstart000000000000","pattern_ref":"onboarding","status":"complete","started_at":"not-a-time","ended_at":"2026-07-05T10:15:00Z"}`),
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
	keys := make([]string, 0, len(store))
	for k := range store {
		keys = append(keys, k)
	}
	from, to := rfc("2026-07-05T10:00:00Z"), rfc("2026-07-05T11:00:00Z")

	rows := computeTimeline(keys, get, from, to)
	if len(rows) != 2 {
		t.Fatalf("want 2 overlapping flows, got %d: %+v", len(rows), rows)
	}
	byID := map[string]timelineFlow{}
	for _, r := range rows {
		byID[r.InstanceID] = r
	}
	if _, ok := byID["inside000000000000"]; !ok {
		t.Error("fully-inside flow missing from timeline")
	}
	if _, ok := byID["running0000000000000"]; !ok {
		t.Error("still-running flow missing from timeline")
	}
	if _, ok := byID["before0000000000000"]; ok {
		t.Error("flow that ended before the window should not overlap")
	}
	if _, ok := byID["after00000000000000"]; ok {
		t.Error("flow that started after the window should not overlap")
	}
	if _, ok := byID["badstart000000000000"]; ok {
		t.Error("unparsable started_at should be skipped")
	}
}

// TestHandleHistoryTimelineValidation pins that query validation runs BEFORE
// the requireConn guard: a malformed/inverted window answers 400 even with no
// NATS connection (testServer's nil-conn posture), never the misleading 502
// a conn-first check would give.
func TestHandleHistoryTimelineValidation(t *testing.T) {
	mux := testServer()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/history/timeline?from=garbage&to=2026-07-05T11:00:00Z", nil))
	if rec.Code != 400 {
		t.Errorf("malformed from = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/history/timeline?from=2026-07-05T11:00:00Z&to=2026-07-05T10:00:00Z", nil))
	if rec.Code != 400 {
		t.Errorf("to before from = %d, want 400", rec.Code)
	}

	// Well-formed params fall through to requireConn — nil conn = 502.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/history/timeline?from=2026-07-05T10:00:00Z&to=2026-07-05T11:00:00Z", nil))
	if rec.Code != 502 {
		t.Errorf("valid params, nil conn = %d, want 502", rec.Code)
	}
}

// A Loom pattern's name lives in `patternId` on its `.spec` aspect. The lens
// family's `.canonicalName` aspect does not exist on a pattern vertex at all,
// so a resolver that reads only that one returns "" for every flow on the
// stack — which is what shipped until a live check caught it.
func TestPatternNameFrom(t *testing.T) {
	store := map[string][]byte{
		"vtx.meta.pat1.spec": []byte(`{"data":{"patternId":"onboarding","subjectType":"identity"}}`),
		// A meta-vertex carrying only the lens-family shape still resolves,
		// so the fallback is real rather than decorative.
		"vtx.meta.pat2.canonicalName": []byte(`{"data":{"value":"legacyNamed"}}`),
		// A pattern vertex with a spec that names nothing.
		"vtx.meta.pat3.spec": []byte(`{"data":{"subjectType":"identity"}}`),
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }

	if got := patternNameFrom(get, "vtx.meta.pat1"); got != "onboarding" {
		t.Errorf("spec patternId = %q, want onboarding", got)
	}
	if got := patternNameFrom(get, "vtx.meta.pat2"); got != "legacyNamed" {
		t.Errorf("canonicalName fallback = %q", got)
	}
	if got := patternNameFrom(get, "vtx.meta.pat3"); got != "" {
		t.Errorf("a spec naming nothing = %q, want empty so the card falls back to the ref", got)
	}
	if got := patternNameFrom(get, "vtx.meta.missing"); got != "" {
		t.Errorf("absent vertex = %q, want empty", got)
	}
}

func TestLoomInstanceStatuses(t *testing.T) {
	t.Run("decodes instanceId to status, terminal instances included", func(t *testing.T) {
		// Loom's list carries finished instances alongside running ones —
		// which is why the status has to come across with the id.
		raw := []byte(`{"instances":[{"instanceId":"a","status":"running"},{"instanceId":"b","status":"complete"}]}`)
		got := loomInstanceStatuses(raw)
		if len(got) != 2 || got["a"] != "running" || got["b"] != "complete" {
			t.Fatalf("unexpected statuses: %+v", got)
		}
	})

	t.Run("an entry with no id is dropped, never keyed on empty", func(t *testing.T) {
		got := loomInstanceStatuses([]byte(`{"instances":[{"status":"running"},{"instanceId":"a","status":"running"}]}`))
		if len(got) != 1 || got["a"] != "running" {
			t.Fatalf("unexpected statuses: %+v", got)
		}
	})

	t.Run("malformed or empty reply yields an empty map, never a panic", func(t *testing.T) {
		if got := loomInstanceStatuses(nil); len(got) != 0 {
			t.Errorf("nil reply should yield empty map, got %+v", got)
		}
		if got := loomInstanceStatuses([]byte(`not json`)); len(got) != 0 {
			t.Errorf("malformed reply should yield empty map, got %+v", got)
		}
	})
}

func TestReadLoomPatternSpec(t *testing.T) {
	store := map[string][]byte{
		"vtx.meta.pat1.spec": []byte(`{"data":{"patternId":"onboarding","subjectType":"identity","completionDomains":["orchestration"],"steps":[{"kind":"systemOp","operation":"CreateTask"},{"kind":"externalTask","adapter":"email"}]}}`),
		"vtx.meta.bad.spec":  []byte(`not json`),
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }

	spec := readLoomPatternSpec(get, "vtx.meta.pat1")
	if spec == nil {
		t.Fatal("pat1 spec did not decode")
	}
	if spec.PatternID != "onboarding" || spec.SubjectType != "identity" || len(spec.Steps) != 2 {
		t.Errorf("spec = %+v", spec)
	}
	if spec.Steps[1]["adapter"] != "email" {
		t.Errorf("step 2 adapter = %v — the step list is the half inspect cannot give", spec.Steps[1]["adapter"])
	}
	// A missing or malformed spec is not fatal: the engine's answer is the more
	// important half of the panel and does not depend on this read.
	if readLoomPatternSpec(get, "vtx.meta.missing") != nil {
		t.Error("absent spec should yield nil")
	}
	if readLoomPatternSpec(get, "vtx.meta.bad") != nil {
		t.Error("malformed spec should yield nil, never a panic")
	}
}

func TestHandleFlowDetail_Validation(t *testing.T) {
	mux := testServer()

	cases := []struct {
		method, path string
		want         int
	}{
		// Validation runs BEFORE requireConn (testServer has a nil conn), so a
		// malformed request answers 400 rather than the misleading 502 a
		// conn-first check would give.
		{"GET", "/api/flows/a.b", http.StatusBadRequest},
		{"GET", "/api/flows/", http.StatusBadRequest},
		{"GET", "/api/flows/a/b", http.StatusBadRequest},
		{"POST", "/api/flows/abc", http.StatusBadRequest},
		// A well-formed id with no NATS gets the honest upstream answer.
		{"GET", "/api/flows/abcdefghijklmnopqrst", http.StatusBadGateway},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}
