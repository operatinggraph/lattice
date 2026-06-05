package orchestrationbase_test

import (
	"context"
	"testing"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// assertTrackerEvent asserts the idempotency tracker for reqID recorded
// eventClass in its eventClasses list (Contract #4 §4.2).
func assertTrackerEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found for %s: %v", reqID, err)
	}
	tr, err := processor.ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	for _, ec := range ecs {
		if ec == eventClass {
			return
		}
	}
	t.Fatalf("%s not in tracker eventClasses: %v", eventClass, ecs)
}

// assertTrackerNotEvent asserts eventClass is NOT in the tracker's
// eventClasses (used to prove a redelivery did not double-emit).
func assertTrackerNotEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		return
	}
	tr, _ := processor.ParseTracker(entry.Value)
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	for _, ec := range ecs {
		if ec == eventClass {
			t.Fatalf("%s should NOT be in eventClasses: %v", eventClass, ecs)
		}
	}
}

// trackerEventCount returns how many times eventClass appears in the tracker's
// eventClasses list — used to prove a redelivery did not append a second copy.
func trackerEventCount(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) int {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found for %s: %v", reqID, err)
	}
	tr, err := processor.ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	n := 0
	for _, ec := range ecs {
		if ec == eventClass {
			n++
		}
	}
	return n
}
