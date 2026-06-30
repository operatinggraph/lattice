package substrate

import (
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// TestScheduleHeaders_PinnedToServerConstants pins the exported §10.4 schedule
// header names to the nats-server constants the scheduler actually reads. A
// drifted constant fails here at build/test time instead of silently
// misrouting every scheduled publish.
func TestScheduleHeaders_PinnedToServerConstants(t *testing.T) {
	if ScheduleHeader != natsserver.JSSchedulePattern {
		t.Fatalf("ScheduleHeader = %q, want the server's %q", ScheduleHeader, natsserver.JSSchedulePattern)
	}
	if ScheduleTargetHeader != natsserver.JSScheduleTarget {
		t.Fatalf("ScheduleTargetHeader = %q, want the server's %q", ScheduleTargetHeader, natsserver.JSScheduleTarget)
	}
	if ScheduleTTLHeader != natsserver.JSScheduleTTL {
		t.Fatalf("ScheduleTTLHeader = %q, want the server's %q", ScheduleTTLHeader, natsserver.JSScheduleTTL)
	}
	if SchedulerHeader != natsserver.JSScheduler {
		t.Fatalf("SchedulerHeader = %q, want the server's %q", SchedulerHeader, natsserver.JSScheduler)
	}
	if ScheduleNextHeader != natsserver.JSScheduleNext {
		t.Fatalf("ScheduleNextHeader = %q, want the server's %q", ScheduleNextHeader, natsserver.JSScheduleNext)
	}
}
