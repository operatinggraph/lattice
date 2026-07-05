package main

import "testing"

func TestComputeFlows(t *testing.T) {
	store := map[string][]byte{
		// A completed flow.
		"complete0000000000": []byte(`{"instance_id":"complete0000000000","pattern_ref":"onboarding","subject_key":"vtx.identity.id1","status":"complete","started_at":"2026-07-05T10:00:00Z","ended_at":"2026-07-05T10:05:00Z","last_event_seq":42}`),
		// A failed flow.
		"failed00000000000": []byte(`{"instance_id":"failed00000000000","pattern_ref":"onboarding","subject_key":"vtx.identity.id2","status":"failed","started_at":"2026-07-05T09:00:00Z","ended_at":"2026-07-05T09:02:00Z","failure_reason":"adapter timeout","last_event_seq":10}`),
		// A running flow the live control read still reports.
		"running0000000000": []byte(`{"instance_id":"running0000000000","pattern_ref":"onboarding","subject_key":"vtx.identity.id3","status":"running","started_at":"2026-07-05T11:00:00Z","last_event_seq":5}`),
		// A running flow with NO matching live instance — orphaned.
		"orphan00000000000": []byte(`{"instance_id":"orphan00000000000","pattern_ref":"onboarding","subject_key":"vtx.identity.id4","status":"running","started_at":"2026-07-05T08:00:00Z","last_event_seq":3}`),
		// A poison entry that fails to decode — must be skipped, not fatal.
		"poison00000000000": []byte(`not json`),
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
	keys := make([]string, 0, len(store))
	for k := range store {
		keys = append(keys, k)
	}
	liveIDs := map[string]bool{"running0000000000": true}

	t.Run("all rows, poison skipped, newest-started first", func(t *testing.T) {
		rows := computeFlows(keys, get, liveIDs, true, "")
		if len(rows) != 4 {
			t.Fatalf("want 4 flows (poison entry skipped), got %d: %+v", len(rows), rows)
		}
		if rows[0].InstanceID != "running0000000000" {
			t.Errorf("newest-started flow should sort first, got %q", rows[0].InstanceID)
		}
	})

	t.Run("status filter limits to one status", func(t *testing.T) {
		rows := computeFlows(keys, get, liveIDs, true, "failed")
		if len(rows) != 1 {
			t.Fatalf("want 1 failed flow, got %d", len(rows))
		}
		if rows[0].FailureReason != "adapter timeout" {
			t.Errorf("failure reason not decoded: %q", rows[0].FailureReason)
		}
	})

	t.Run("running row badged live when the control read reports it", func(t *testing.T) {
		rows := computeFlows(keys, get, liveIDs, true, "running")
		if len(rows) != 2 {
			t.Fatalf("want 2 running flows, got %d", len(rows))
		}
		byID := map[string]flowRow{}
		for _, r := range rows {
			byID[r.InstanceID] = r
		}
		if byID["running0000000000"].Live == nil || !*byID["running0000000000"].Live {
			t.Errorf("running0000000000 should be badged live")
		}
		if byID["orphan00000000000"].Live == nil || *byID["orphan00000000000"].Live {
			t.Errorf("orphan00000000000 has no matching live instance; should be badged confirmed-not-live")
		}
	})

	t.Run("terminal row is never badged live even if liveIDs somehow names it", func(t *testing.T) {
		rows := computeFlows(keys, get, map[string]bool{"complete0000000000": true}, true, "complete")
		if len(rows) != 1 || rows[0].Live != nil {
			t.Fatalf("a terminal row must never be badged live, got %+v", rows)
		}
	})

	t.Run("running row stays unbadged (nil), not falsely orphaned, when the control read failed", func(t *testing.T) {
		rows := computeFlows(keys, get, nil, false, "running")
		for _, r := range rows {
			if r.Live != nil {
				t.Errorf("row %q should be unbadged when liveKnown=false, got %+v", r.InstanceID, *r.Live)
			}
		}
	})
}

func TestLiveLoomInstances(t *testing.T) {
	t.Run("decodes instanceId set from a raw list reply", func(t *testing.T) {
		raw := []byte(`{"instances":[{"instanceId":"a"},{"instanceId":"b"}]}`)
		ids := liveLoomInstances(raw)
		if !ids["a"] || !ids["b"] || len(ids) != 2 {
			t.Fatalf("unexpected ids: %+v", ids)
		}
	})

	t.Run("malformed or empty reply yields an empty set, never a panic", func(t *testing.T) {
		if ids := liveLoomInstances(nil); len(ids) != 0 {
			t.Errorf("nil reply should yield empty set, got %+v", ids)
		}
		if ids := liveLoomInstances([]byte(`not json`)); len(ids) != 0 {
			t.Errorf("malformed reply should yield empty set, got %+v", ids)
		}
	})
}
