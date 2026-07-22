package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestSSEEventData(t *testing.T) {
	raw := []byte(`{"eventId":"e1","requestId":"r1","eventType":"clinic.appointmentCreated",` +
		`"domain":"clinic","targetKey":"vtx.appointment.a1","payload":{"secret":"never-forwarded"},` +
		`"timestamp":"2026-07-03T08:00:00Z"}`)
	var got feedEvent
	if err := json.Unmarshal(sseEventData(raw, 0), &got); err != nil {
		t.Fatalf("unmarshal shaped event: %v", err)
	}
	want := feedEvent{
		EventID: "e1", RequestID: "r1", EventType: "clinic.appointmentCreated",
		Domain: "clinic", TargetKey: "vtx.appointment.a1", Timestamp: "2026-07-03T08:00:00Z",
	}
	if got != want {
		t.Errorf("shaped event = %+v, want %+v", got, want)
	}
	// The payload never rides the feed — the feed links to entities instead.
	if strings.Contains(string(sseEventData(raw, 0)), "never-forwarded") {
		t.Error("payload leaked into the SSE wire shape")
	}

	// A dropped counter rides the next message; zero is omitted.
	if s := string(sseEventData(raw, 3)); !strings.Contains(s, `"dropped":3`) {
		t.Errorf("dropped counter missing: %s", s)
	}
	if s := string(sseEventData(raw, 0)); strings.Contains(s, "dropped") {
		t.Errorf("zero dropped serialized: %s", s)
	}

	// Unparseable bytes surface honestly, never crash or vanish.
	var bad feedEvent
	if err := json.Unmarshal(sseEventData([]byte("not json"), 1), &bad); err != nil {
		t.Fatalf("unmarshal unparseable shape: %v", err)
	}
	if bad.EventType != "(unparseable)" || bad.Dropped != 1 {
		t.Errorf("unparseable shape = %+v", bad)
	}
}

func TestFormatSSE(t *testing.T) {
	if got := string(formatSSE("", []byte(`{"a":1}`))); got != "data: {\"a\":1}\n\n" {
		t.Errorf("plain frame = %q", got)
	}
	if got := string(formatSSE("error", []byte(`{"e":1}`))); got != "event: error\ndata: {\"e\":1}\n\n" {
		t.Errorf("named frame = %q", got)
	}
}

// TestEventStream_ClientLimit pins the concurrent-tail bound: the slot past the
// configured cap is refused and a released slot is reusable. The limiter is
// driven directly — the handler path around it needs a live NATS connection
// (covered by TestEventStream_TailsCoreEvents).
func TestEventStream_ClientLimit(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), maxEventStreamClients: 3}
	for i := 0; i < s.maxEventStreamClients; i++ {
		if !s.acquireEventClient() {
			t.Fatalf("client %d rejected under the limit", i)
		}
	}
	if s.acquireEventClient() {
		t.Fatal("client above the limit acquired a slot")
	}
	s.eventClients.Add(-1)
	if !s.acquireEventClient() {
		t.Fatal("released slot not reusable")
	}
}

// TestEventStream_TailsCoreEvents is the end-to-end pin: an embedded
// NATS+JetStream server with a core-events stream, the real handler serving
// over a live HTTP connection, and a published event arriving shaped —
// deliver-new (the pre-connect event never replays), payload stripped.
func TestEventStream_TailsCoreEvents(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	defer ns.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	if err := conn.EnsureStream(ctx, substrate.StreamSpec{
		Name: bootstrap.CoreEventsStreamName, Subjects: []string{"events.>"},
	}); err != nil {
		t.Fatalf("ensure core-events: %v", err)
	}

	// An event published BEFORE the tail opens must never replay (deliver-new).
	if _, err := conn.JetStream().Publish(ctx, "events.test.before",
		[]byte(`{"eventId":"e0","requestId":"r0","eventType":"test.before","domain":"test","timestamp":"2026-07-03T08:00:00Z"}`)); err != nil {
		t.Fatalf("publish pre-connect event: %v", err)
	}

	srv := &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/api/events/stream", nil)
	res, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	reader := bufio.NewReader(res.Body)
	// The hello event confirms the consumer is live before publishing — and
	// pins the FE's liveness gate (pulse.js flips "live" on hello, not on the
	// bare 200 handshake).
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "event: hello" {
		t.Fatalf("expected the hello event, got %q (%v)", line, err)
	}
	// Drain the rest of the hello frame (its data line + the blank terminator)
	// so the data-line scan below only sees real event frames.
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("draining hello frame: %v", err)
		}
		if strings.TrimSpace(l) == "" {
			break
		}
	}

	if _, err := conn.JetStream().Publish(ctx, "events.clinic.appointmentCreated",
		[]byte(`{"eventId":"e1","requestId":"r1","eventType":"clinic.appointmentCreated","domain":"clinic",`+
			`"targetKey":"vtx.appointment.a1","payload":{"k":"v"},"timestamp":"2026-07-03T08:00:01Z"}`)); err != nil {
		t.Fatalf("publish event: %v", err)
	}

	// Read frames until the data line arrives (heartbeat comments interleave).
	deadline := time.After(10 * time.Second)
	dataCh := make(chan string, 1)
	go func() {
		for {
			l, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(l, "data: ") {
				dataCh <- strings.TrimSpace(strings.TrimPrefix(l, "data: "))
				return
			}
		}
	}()
	var data string
	select {
	case data = <-dataCh:
	case <-deadline:
		t.Fatal("no event arrived on the SSE tail")
	}

	var ev feedEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		t.Fatalf("unmarshal SSE data %q: %v", data, err)
	}
	if ev.EventType == "test.before" {
		t.Fatal("pre-connect event replayed — the tail must be deliver-new")
	}
	if ev.EventType != "clinic.appointmentCreated" || ev.TargetKey != "vtx.appointment.a1" || ev.RequestID != "r1" {
		t.Errorf("shaped event = %+v", ev)
	}
	if strings.Contains(data, `"payload"`) {
		t.Errorf("payload leaked onto the wire: %s", data)
	}
}
