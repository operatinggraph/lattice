//go:build js

package browser

import (
	"context"
	"encoding/json"
	"syscall/js"
	"testing"

	"github.com/asolgan/lattice/internal/edge/transport"
	"github.com/asolgan/lattice/internal/refractor/control/controlwire"
)

// These run the browser host against a fake JS shell in the same real headless
// Chrome the store conformance suite uses — the store underneath is a real
// IndexedDB. They prove the wasm host does what the design's §3.3 "Semantics
// core" column promises: a delta pushed in through Deliver lands in the mirror
// under last-writer-wins, a snapshot reads it back through the overlay, and an
// enqueue produces a durable intent — all over the exported JS boundary, not
// the Go API.

// fakeShell is a minimal transport shell: it captures startConsumer's config,
// answers firstSequence and the personal.register/hydrate control RPCs, and
// otherwise stays out of the way. Everything it does is synchronous, and await
// tolerates a non-thenable return, so the tests do not need a JS Promise.
type fakeShell struct {
	started      chan map[string]any
	firstSeq     float64
	registerResp controlwire.ControlResponse
	hydrateResp  controlwire.ControlResponse
	funcs        []js.Func
}

func newFakeShell() *fakeShell {
	return &fakeShell{
		started:      make(chan map[string]any, 1),
		firstSeq:     0,
		registerResp: controlwire.ControlResponse{PersonalRegister: &controlwire.PersonalRegisterResult{Registered: true}},
		hydrateResp:  controlwire.ControlResponse{PersonalHydrate: &controlwire.PersonalHydrateResult{Hydrated: true, Revision: 1}},
	}
}

func (s *fakeShell) value() js.Value {
	fn := func(f func(this js.Value, args []js.Value) any) js.Func {
		jf := js.FuncOf(f)
		s.funcs = append(s.funcs, jf)
		return jf
	}
	obj := js.Global().Get("Object").New()
	obj.Set("startConsumer", fn(func(_ js.Value, args []js.Value) any {
		cfg := map[string]any{
			"stream":        args[0].Get("stream").String(),
			"durable":       args[0].Get("durable").String(),
			"filterSubject": args[0].Get("filterSubject").String(),
		}
		select {
		case s.started <- cfg:
		default:
		}
		return js.Undefined() // resolves await immediately; the test pushes deltas itself
	}))
	obj.Set("firstSequence", fn(func(_ js.Value, _ []js.Value) any {
		return s.firstSeq
	}))
	obj.Set("request", fn(func(_ js.Value, args []js.Value) any {
		subject := args[0].String()
		var resp controlwire.ControlResponse
		switch subject {
		case controlwire.ControlSubject("personal", "register"):
			resp = s.registerResp
		case controlwire.ControlSubject("personal", "hydrate"):
			resp = s.hydrateResp
		default:
			resp = controlwire.ControlResponse{Error: "unexpected control subject " + subject}
		}
		b, _ := json.Marshal(resp)
		return toUint8Array(b)
	}))
	return obj
}

func (s *fakeShell) release() {
	for _, f := range s.funcs {
		f.Release()
	}
}

// startHost boots a Host over the fake shell and blocks until its sync manager
// has started the durable consumer, so a test can push deltas knowing the
// handler is registered.
func startHost(t *testing.T, shell *fakeShell) *Host {
	t.Helper()
	h, err := Start(context.Background(), Config{
		IdentityID: "u_test",
		DeviceID:   "d_test",
		GatewayURL: "http://127.0.0.1:0", // never dialed: these tests do not drain
		StoreName:  "edge-browser-host-" + t.Name(),
		Shell:      shell.value(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(h.Stop)
	<-shell.started // the consumer is running; Deliver will find the handler
	return h
}

func pushDelta(t *testing.T, h *Host, env deltaEnvelope, seq uint64) string {
	t.Helper()
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal delta: %v", err)
	}
	return h.tr.Deliver(context.Background(), transport.Delta{
		Subject:  "lattice.sync.user.u_test",
		Body:     body,
		Sequence: seq,
	})
}

// deltaEnvelope is the wire shape sync publishes (its own unexported type,
// re-declared here for the test the same way sync re-declares it from the
// Refractor's).
type deltaEnvelope struct {
	Op       string          `json:"op"`
	Key      string          `json:"key,omitempty"`
	Revision uint64          `json:"revision"`
	Data     json.RawMessage `json:"data,omitempty"`
}

func TestHost_DeliverAppliesToMirror(t *testing.T) {
	shell := newFakeShell()
	defer shell.release()
	h := startHost(t, shell)

	const key = "manifest.services.svc1"
	if got := pushDelta(t, h, deltaEnvelope{Op: "upsert", Key: key, Revision: 5, Data: json.RawMessage(`{"name":"Laundry"}`)}, 10); got != "ack" {
		t.Fatalf("first upsert: got %q, want ack", got)
	}

	e, ok, err := h.store.Get(key)
	if err != nil || !ok {
		t.Fatalf("Get after upsert: ok=%v err=%v", ok, err)
	}
	if e.Revision != 5 {
		t.Fatalf("stored revision: got %d, want 5", e.Revision)
	}

	// A lower-revision delta is dropped by last-writer-wins, but still acked
	// (the message was handled; redelivering it changes nothing).
	if got := pushDelta(t, h, deltaEnvelope{Op: "upsert", Key: key, Revision: 3, Data: json.RawMessage(`{"name":"stale"}`)}, 11); got != "ack" {
		t.Fatalf("stale upsert: got %q, want ack", got)
	}
	e, _, _ = h.store.Get(key)
	if e.Revision != 5 {
		t.Fatalf("stale delta must not overwrite: revision %d, want 5", e.Revision)
	}

	cur, ok, err := h.store.Cursor()
	if err != nil || !ok || cur != 11 {
		t.Fatalf("cursor after two deltas: cur=%d ok=%v err=%v, want 11", cur, ok, err)
	}
}

func TestHost_MalformedDeltaTerminates(t *testing.T) {
	shell := newFakeShell()
	defer shell.release()
	h := startHost(t, shell)

	got := h.tr.Deliver(context.Background(), transport.Delta{
		Subject:  "lattice.sync.user.u_test",
		Body:     []byte("{not json"),
		Sequence: 7,
	})
	if got != "term" {
		t.Fatalf("malformed delta: got %q, want term", got)
	}
}

func TestHost_SnapshotReadsMirrorThroughOverlay(t *testing.T) {
	shell := newFakeShell()
	defer shell.release()
	h := startHost(t, shell)

	pushDelta(t, h, deltaEnvelope{Op: "upsert", Key: "manifest.me.profile", Revision: 1, Data: json.RawMessage(`{"name":"Ada"}`)}, 1)
	pushDelta(t, h, deltaEnvelope{Op: "upsert", Key: "manifest.tasks.t1", Revision: 1, Data: json.RawMessage(`{"title":"pickup"}`)}, 2)

	frames, err := h.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	manifest := map[string]bool{}
	for _, f := range frames {
		if f.Kind == "manifest" {
			manifest[f.Key] = true
		}
	}
	if !manifest["manifest.me.profile"] || !manifest["manifest.tasks.t1"] {
		t.Fatalf("snapshot missing rows: %v", manifest)
	}
}

func TestHost_EnqueueQueuesDurableIntentWithOverlay(t *testing.T) {
	shell := newFakeShell()
	defer shell.release()
	h := startHost(t, shell)

	const key = "manifest.tasks.t9"
	if err := h.Enqueue(enqueueRequest{
		OperationType: "clinic.bookSlot",
		Payload:       json.RawMessage(`{"slot":"9am"}`),
		TouchedKey:    key,
	}, "req_abc"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	intents, err := h.store.ListIntents()
	if err != nil || len(intents) != 1 {
		t.Fatalf("ListIntents: len=%d err=%v, want 1", len(intents), err)
	}

	// The optimistic overlay is visible immediately, before any submit.
	v, ok, err := h.overlay.Read(key)
	if err != nil || !ok {
		t.Fatalf("overlay.Read: ok=%v err=%v", ok, err)
	}
	if !v.Pending {
		t.Fatalf("overlay value should be pending before confirmation")
	}
}
