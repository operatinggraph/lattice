package loom

import (
	"sort"
	"testing"
)

// TestUserTaskReads_CoverEndpoints is the H1/U2 Loom-side drift guard for the
// userTask CreateTask read-set. orchestration-base's TestCreateTaskReads_
// MatchDDLScript guards Weaver's un-deduped 3-key set against the DDL script; it
// does NOT cover Loom's DEDUPED 2-key set. This test locks the dedup: the read-set
// userTaskReads derives must still cover ALL THREE CreateTask link endpoints the
// task DDL validates with vertex_alive (assignee, forOperation, scopedTo), so the
// op never HydrationMisses a key the DDL reads.
//
// It encodes the §10.5 userTask invariant explicitly: submitUserTask sets
// assignee == scopedTo == subjectKey in the payload, so the three endpoints
// collapse to two distinct keys. If a future change scopes a userTask to anything
// other than the subject without updating userTaskReads, the coverage assertion
// fails here rather than silently failing closed in production.
func TestUserTaskReads_CoverEndpoints(t *testing.T) {
	const subjectKey = "vtx.identity.BBsubjectHJKMNPQRST"
	const forOperation = "vtx.meta.BBforOpJKMNPQRSTUVW"

	// The three CreateTask link endpoints exactly as submitUserTask builds the
	// payload (assignee == scopedTo == subjectKey is the userTask invariant).
	endpoints := map[string]string{
		"assignee":     subjectKey,
		"forOperation": forOperation,
		"scopedTo":     subjectKey,
	}

	reads := userTaskReads(subjectKey, forOperation)
	readSet := map[string]bool{}
	for _, r := range reads {
		readSet[r] = true
	}

	// Coverage: every endpoint value the DDL vertex_alive-checks must be hydrated.
	for field, key := range endpoints {
		if !readSet[key] {
			t.Fatalf("userTask read-set does not cover the %q endpoint %q (would HydrationMiss); reads=%v", field, key, reads)
		}
	}

	// No over-hydration: the deduped set is EXACTLY the distinct endpoint keys.
	distinct := map[string]bool{}
	for _, key := range endpoints {
		distinct[key] = true
	}
	if len(reads) != len(distinct) {
		t.Fatalf("userTask read-set has %d keys, want %d distinct endpoint keys (over/under-hydration); reads=%v",
			len(reads), len(distinct), reads)
	}

	// The invariant the dedup relies on: assignee and scopedTo are the SAME key
	// (the subject). If this ever stops holding, the 2-key dedup is unsound and
	// userTaskReads must change.
	if endpoints["assignee"] != endpoints["scopedTo"] {
		t.Fatalf("userTask invariant broken: assignee (%q) != scopedTo (%q) — the 2-key dedup is unsound",
			endpoints["assignee"], endpoints["scopedTo"])
	}

	// And the deduped set is the subject + forOperation, in some order.
	want := []string{forOperation, subjectKey}
	got := append([]string(nil), reads...)
	sort.Strings(got)
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("userTask read-set = %v, want %v (subject + forOperation, deduped)", reads, want)
		}
	}
}
