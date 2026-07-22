package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

const (
	// eventStreamBuffer is the per-client channel depth between the JetStream
	// consumer callback and the HTTP writer. A full buffer drops (the FE ring
	// buffer makes loss non-fatal); a dropped counter rides the next message.
	eventStreamBuffer = 256
	// eventStreamHeartbeat keeps proxies/idle timers from killing the stream.
	eventStreamHeartbeat = 15 * time.Second
)

// feedEvent is the SSE wire shape: the processor.Event envelope minus payload.
// The feed links to the entities; the payload is readable at the op tracker /
// target. Dropped counts messages lost to a full buffer, surfaced on the next
// message the writer dequeues (which may predate the gap by up to the buffer
// depth — the count is exact, the position is approximate).
type feedEvent struct {
	EventID   string `json:"eventId"`
	RequestID string `json:"requestId"`
	EventType string `json:"eventType"`
	Domain    string `json:"domain"`
	TargetKey string `json:"targetKey,omitempty"`
	Timestamp string `json:"timestamp"`
	Dropped   uint64 `json:"dropped,omitempty"`
}

// sseEventData shapes one raw core-events message into the feed's wire JSON.
// An unparseable message is surfaced honestly as eventType "(unparseable)"
// rather than silently skipped — the operator sees that something flowed.
func sseEventData(raw []byte, dropped uint64) []byte {
	var ev feedEvent
	if err := json.Unmarshal(raw, &ev); err != nil || ev.EventType == "" {
		ev = feedEvent{EventType: "(unparseable)"}
	}
	ev.Dropped = dropped
	b, err := json.Marshal(ev)
	if err != nil {
		return []byte(`{"eventType":"(unparseable)"}`)
	}
	return b
}

// formatSSE frames one server-sent event: an optional event name plus a single
// data line (the payloads here are one-line JSON, never multi-line).
func formatSSE(event string, data []byte) []byte {
	var b bytes.Buffer
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteByte('\n')
	}
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes()
}

// eventStreamCap is the concurrent-tail bound this server runs with, resolved
// at boot (eventStreamMax, publicorigin.go). An unset field means a server
// value that never went through boot — only a test fixture — so it falls back
// to the ordinary default rather than denying every tail.
func (s *server) eventStreamCap() int32 {
	if s.maxEventStreamClients <= 0 {
		return defaultEventStreamClients
	}
	return int32(s.maxEventStreamClients)
}

// atCapacityMessage is the terminal streamError text for a refused tail. An
// operator is told what to do about it; a demo visitor, who has no other tab to
// close and no control over how many strangers are watching, is told to wait.
func (s *server) atCapacityMessage() string {
	if s.demoMode {
		return "the live feed is at capacity — try again in a moment"
	}
	return fmt.Sprintf("too many event stream clients (max %d) — close another Loupe tab", s.eventStreamCap())
}

// acquireEventClient reserves an SSE client slot, false when at capacity.
func (s *server) acquireEventClient() bool {
	limit := s.eventStreamCap()
	for {
		cur := s.eventClients.Load()
		if cur >= limit {
			return false
		}
		if s.eventClients.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// handleEventStream implements GET /api/events/stream: a Server-Sent Events
// tail of core-events (events.>) via a per-client ephemeral ordered consumer,
// deliver-new (a tail, not history — replay is the orchestration-history
// design's job). This is the one long-lived handler in Loupe: it bounds
// concurrent clients, heartbeats so the EventSource stays alive through idle
// periods, and rolls its own write deadline per write (the server-wide
// WriteTimeout would cut the stream; no deadline at all would let a stalled
// peer hold a client slot forever). Consumer cleanup is the ordered consumer's
// own inactivity reaping plus the explicit Stop on disconnect. Graceful
// shutdown does not cancel this handler — main.go's 5s Shutdown timeout (then
// process exit) bounds it instead.
//
// Terminal conditions (capacity, subscribe failure, consumer death) are
// reported as a named streamError event and the stream closes; liveness is
// confirmed with a named hello event, so the FE flips "live" only on a
// working tail, never on the bare 200 handshake.
func (s *server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	rc := http.NewResponseController(w)
	writeFrame := func(frame []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(3 * eventStreamHeartbeat))
		if _, err := w.Write(frame); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	streamError := func(msg string) {
		writeFrame(formatSSE("streamError", mustJSON(map[string]string{"error": msg})))
	}

	sseHeaders(w)
	if !s.acquireEventClient() {
		streamError(s.atCapacityMessage())
		return
	}
	defer s.eventClients.Add(-1)

	cons, err := conn.JetStream().OrderedConsumer(r.Context(), bootstrap.CoreEventsStreamName,
		jetstream.OrderedConsumerConfig{
			FilterSubjects: []string{"events.>"},
			DeliverPolicy:  jetstream.DeliverNewPolicy,
		})
	if err != nil {
		streamError("no event stream available — " + err.Error())
		return
	}

	msgs := make(chan []byte, eventStreamBuffer)
	consumeErrs := make(chan error, 1)
	var dropped atomic.Uint64
	cc, err := cons.Consume(func(m jetstream.Msg) {
		data := make([]byte, len(m.Data()))
		copy(data, m.Data())
		select {
		case msgs <- data:
		default:
			dropped.Add(1)
		}
	}, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		select {
		case consumeErrs <- err:
		default:
		}
	}))
	if err != nil {
		streamError("no event stream available — " + err.Error())
		return
	}
	defer cc.Stop()

	if !writeFrame(formatSSE("hello", []byte("{}"))) {
		return
	}

	hb := time.NewTicker(eventStreamHeartbeat)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case err := <-consumeErrs:
			// A consumer-level failure under a healthy TCP stream would
			// otherwise render as a deaf green "live" — surface it and close;
			// the FE reconnects and a fresh tail re-creates the consumer.
			s.logger.Warn("event stream consumer error; closing tail", "error", err)
			streamError("event tail failed — " + err.Error())
			return
		case raw := <-msgs:
			if !writeFrame(formatSSE("", sseEventData(raw, dropped.Swap(0)))) {
				return
			}
		case <-hb.C:
			if !writeFrame([]byte(": hb\n\n")) {
				return
			}
		}
	}
}

// sseHeaders marks the response as an event stream and disables buffering.
func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}
