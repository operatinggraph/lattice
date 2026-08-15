package main

import "testing"

// logic/oplog.js — the Submit-Op reply shaping, session-log mechanics, and
// follow-through filter (design §10), driven through the goja harness.

func TestShapeReplyJS(t *testing.T) {
	vm := logicVM(t, "oplog.js")

	// An accepted reply: structured, primaryKey ordered first + flagged, the
	// remaining committed keys lexicographic.
	shaped, ok := call(t, vm, "shapeReply", map[string]any{
		"requestId":    "r1",
		"opTrackerKey": "vtx.op.r1",
		"status":       "accepted",
		"decision":     "committed",
		"committedAt":  "2026-07-03T10:00:00Z",
		"primaryKey":   "vtx.listing.L1",
		"revisions": map[string]any{
			"vtx.listing.L1":                     7,
			"vtx.listing.L1.title":               3,
			"lnk.listing.L1.ownedBy.identity.I1": 1,
		},
	}).(map[string]any)
	if !ok {
		t.Fatal("shapeReply did not return an object")
	}
	if shaped["structured"] != true || shaped["status"] != "accepted" {
		t.Fatalf("accepted reply not structured: %v", shaped)
	}
	if shaped["statusLine"] != "accepted · committed" {
		t.Fatalf("statusLine = %v", shaped["statusLine"])
	}
	keys := shaped["keys"].([]any)
	if len(keys) != 3 {
		t.Fatalf("want 3 keys, got %d", len(keys))
	}
	first := keys[0].(map[string]any)
	if first["key"] != "vtx.listing.L1" || first["primary"] != true {
		t.Fatalf("primaryKey not first/flagged: %v", first)
	}
	second := keys[1].(map[string]any)
	if second["key"] != "lnk.listing.L1.ownedBy.identity.I1" || second["primary"] == true {
		t.Fatalf("remaining keys not lexicographic: %v", second)
	}

	// A duplicate reply (step-2 short-circuit): structured, no revisions,
	// committedAt falls back to originalCommittedAt.
	dup := call(t, vm, "shapeReply", map[string]any{
		"requestId":           "r2",
		"opTrackerKey":        "vtx.op.r2",
		"status":              "duplicate",
		"decision":            "duplicate",
		"originalCommittedAt": "2026-07-01T09:00:00Z",
	}).(map[string]any)
	if dup["structured"] != true || dup["committedAt"] != "2026-07-01T09:00:00Z" {
		t.Fatalf("duplicate shaping wrong: %v", dup)
	}
	if len(dup["keys"].([]any)) != 0 {
		t.Fatalf("duplicate reply should have no keys: %v", dup["keys"])
	}

	// Rejected and transport-error replies keep the verbatim rendering.
	rej := call(t, vm, "shapeReply", map[string]any{"status": "rejected", "error": map[string]any{"code": "X"}}).(map[string]any)
	if rej["structured"] != false || rej["status"] != "rejected" {
		t.Fatalf("rejected must not be structured: %v", rej)
	}
	terr := call(t, vm, "shapeReply", map[string]any{"error": "submit op: no responders"}).(map[string]any)
	if terr["structured"] != false || terr["status"] != "error" {
		t.Fatalf("transport error must shape as error: %v", terr)
	}
	if nilRep := call(t, vm, "shapeReply", nil).(map[string]any); nilRep["structured"] != false {
		t.Fatalf("nil reply must not be structured: %v", nilRep)
	}
}

func TestOpLogEntryAndPushJS(t *testing.T) {
	vm := logicVM(t, "oplog.js")

	e := call(t, vm, "logEntry", map[string]any{
		"status": "accepted", "opTrackerKey": "vtx.op.r1", "primaryKey": "vtx.listing.L1",
	}, "CreateListing", "10:00:00").(map[string]any)
	if e["operationType"] != "CreateListing" || e["status"] != "accepted" ||
		e["opTrackerKey"] != "vtx.op.r1" || e["time"] != "10:00:00" {
		t.Fatalf("logEntry = %v", e)
	}
	// A transport error logs status "error" with no links.
	te := call(t, vm, "logEntry", map[string]any{"error": "boom"}, "X", "10:00:01").(map[string]any)
	if te["status"] != "error" || te["opTrackerKey"] != "" {
		t.Fatalf("transport-error entry = %v", te)
	}

	// pushLog prepends newest-first and caps.
	log := []any{map[string]any{"time": "a"}, map[string]any{"time": "b"}}
	out := call(t, vm, "pushLog", log, map[string]any{"time": "c"}, 2).([]any)
	if len(out) != 2 || out[0].(map[string]any)["time"] != "c" || out[1].(map[string]any)["time"] != "a" {
		t.Fatalf("pushLog = %v", out)
	}
	if got := call(t, vm, "pushLog", nil, map[string]any{"time": "c"}, 50).([]any); len(got) != 1 {
		t.Fatalf("pushLog on nil log = %v", got)
	}
}

func TestFollowMatchJS(t *testing.T) {
	vm := logicVM(t, "oplog.js")
	cases := []struct {
		name  string
		row   map[string]any
		opKey string
		want  bool
	}{
		{"matching event", map[string]any{"kind": "event", "opKey": "vtx.op.r1"}, "vtx.op.r1", true},
		{"other op's event", map[string]any{"kind": "event", "opKey": "vtx.op.r2"}, "vtx.op.r1", false},
		{"event with no opKey", map[string]any{"kind": "event", "opKey": ""}, "vtx.op.r1", false},
		{"derived row always shown in-window", map[string]any{"kind": "derived", "text": "x"}, "vtx.op.r1", true},
		{"empty opKey matches nothing event-side", map[string]any{"kind": "event", "opKey": ""}, "", false},
	}
	for _, c := range cases {
		if got := call(t, vm, "followMatch", c.row, c.opKey); got != c.want {
			t.Errorf("%s: followMatch = %v, want %v", c.name, got, c.want)
		}
	}
	if got := call(t, vm, "followMatch", nil, "vtx.op.r1"); got != false {
		t.Errorf("nil row must not match, got %v", got)
	}
}
