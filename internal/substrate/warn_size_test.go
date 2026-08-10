package substrate

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// captureDefaultLogger swaps slog's default logger for one writing to a
// buffer, for the duration of the test, restoring the previous default on
// cleanup. warnIfApproachingSizeLimit writes through the package-level slog
// default — there is no injectable logger to substitute instead.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// resetSizeWarnThrottle clears the per-key throttle map so one test's
// warnings cannot silence the next's.
func resetSizeWarnThrottle(t *testing.T) {
	t.Helper()
	sizeWarnState.mu.Lock()
	sizeWarnState.last = map[string]time.Time{}
	sizeWarnState.mu.Unlock()
	t.Cleanup(func() {
		sizeWarnState.mu.Lock()
		sizeWarnState.last = map[string]time.Time{}
		sizeWarnState.mu.Unlock()
	})
}

func TestWarnIfApproachingSizeLimit_SilentUnderHalfway(t *testing.T) {
	resetSizeWarnThrottle(t)
	buf := captureDefaultLogger(t)

	const limit = 1000
	warnIfApproachingSizeLimit("bucket1", "key1", limit/2, limit)
	if buf.Len() != 0 {
		t.Fatalf("a value at exactly half the limit must not warn, got: %q", buf.String())
	}
}

func TestWarnIfApproachingSizeLimit_LogsPastHalfway(t *testing.T) {
	resetSizeWarnThrottle(t)
	buf := captureDefaultLogger(t)

	const limit = 1000
	warnIfApproachingSizeLimit("bucket1", "key1", limit/2+1, limit)
	out := buf.String()
	if !strings.Contains(out, "approaching the payload size ceiling") {
		t.Fatalf("expected an approaching-limit warning, got: %q", out)
	}
	if !strings.Contains(out, "bucket=bucket1") || !strings.Contains(out, "key=key1") {
		t.Fatalf("warning must identify the bucket and key, got: %q", out)
	}
}

// TestWarnIfApproachingSizeLimit_NonPositiveLimitIsSilent proves the guard
// against an unconnected or closed Conn: MaxPayload()==0 drives limit
// negative once ValueHeadroomBytes is subtracted, and a negative limit must
// not be read as "every size is past half of it".
func TestWarnIfApproachingSizeLimit_NonPositiveLimitIsSilent(t *testing.T) {
	resetSizeWarnThrottle(t)
	buf := captureDefaultLogger(t)

	warnIfApproachingSizeLimit("bucket1", "key1", 500, -4096)
	warnIfApproachingSizeLimit("bucket1", "key2", 0, 0)
	if buf.Len() != 0 {
		t.Fatalf("a non-positive limit must never warn, got: %q", buf.String())
	}
}

// TestWarnIfApproachingSizeLimit_ThrottlesRepeatedCalls proves the spam
// guard: a key rewritten on every event (the exact shape of a growing hub
// document) must not log once per write once it crosses the halfway mark —
// only the first call in the interval logs.
func TestWarnIfApproachingSizeLimit_ThrottlesRepeatedCalls(t *testing.T) {
	resetSizeWarnThrottle(t)
	buf := captureDefaultLogger(t)

	const limit = 1000
	for i := 0; i < 50; i++ {
		warnIfApproachingSizeLimit("hub-bucket", "hub-key", limit/2+1, limit)
	}
	out := buf.String()
	if got := strings.Count(out, "approaching the payload size ceiling"); got != 1 {
		t.Fatalf("expected exactly one warning across 50 approaching-limit writes inside the throttle interval, got %d: %q", got, out)
	}
}

// TestWarnIfApproachingSizeLimit_ThrottleIsPerKey pins the property: a single key
// that stays past the halfway mark on every write (an adjacency hub
// document rewritten on every link event, say) must not claim every
// interval's one warning for the whole process — a DIFFERENT key crossing
// the halfway mark for the first time must still get its own first warning.
// A process-global throttle fails this: the hub's repeated calls would have
// already claimed the interval, silencing the new key indefinitely.
func TestWarnIfApproachingSizeLimit_ThrottleIsPerKey(t *testing.T) {
	resetSizeWarnThrottle(t)
	buf := captureDefaultLogger(t)

	const limit = 1000
	for i := 0; i < 50; i++ {
		warnIfApproachingSizeLimit("hub-bucket", "hub-key", limit/2+1, limit)
	}
	warnIfApproachingSizeLimit("other-bucket", "other-key", limit/2+1, limit)

	out := buf.String()
	if got := strings.Count(out, "bucket=hub-bucket"); got != 1 {
		t.Fatalf("expected exactly one warning for the repeatedly-written key, got %d: %q", got, out)
	}
	if got := strings.Count(out, "bucket=other-bucket"); got != 1 {
		t.Fatalf("a different key's first approach to the ceiling must still warn even while another key is inside its own throttle interval, got %d: %q", got, out)
	}
}

// TestWarnIfApproachingSizeLimit_MapIsBounded proves the throttle map does
// not grow past sizeWarnMaxTracked: once at capacity, the next new key
// clears the map rather than growing it further, and — because that clears
// every previously tracked key's throttle too — that same next call must
// still warn (a cleared entry is indistinguishable from a never-seen one).
func TestWarnIfApproachingSizeLimit_MapIsBounded(t *testing.T) {
	resetSizeWarnThrottle(t)
	buf := captureDefaultLogger(t)

	const limit = 1000
	for i := 0; i < sizeWarnMaxTracked; i++ {
		warnIfApproachingSizeLimit("bucket", fmt.Sprintf("key%d", i), limit/2+1, limit)
	}

	sizeWarnState.mu.Lock()
	tracked := len(sizeWarnState.last)
	sizeWarnState.mu.Unlock()
	if tracked > sizeWarnMaxTracked {
		t.Fatalf("throttle map must never exceed sizeWarnMaxTracked (%d), got %d", sizeWarnMaxTracked, tracked)
	}

	buf.Reset()
	// This key pushes the map to its cap+1th distinct entry, clearing it.
	warnIfApproachingSizeLimit("bucket", "key-overflow", limit/2+1, limit)
	if !strings.Contains(buf.String(), "bucket=bucket") {
		t.Fatalf("the key that triggers the cap-driven clear must itself still warn, got: %q", buf.String())
	}

	sizeWarnState.mu.Lock()
	trackedAfter := len(sizeWarnState.last)
	sizeWarnState.mu.Unlock()
	if trackedAfter > sizeWarnMaxTracked {
		t.Fatalf("throttle map must stay bounded after the clear, got %d entries", trackedAfter)
	}
}
