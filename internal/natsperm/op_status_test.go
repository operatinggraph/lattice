package natsperm

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// opStatusSubject is the op-status RPC subject (internal/opstatus.Subject).
// Hardcoded here — like the other conformance tests — so the assertion is
// against the committed deploy/nats-server.conf, not a shared Go constant.
const opStatusSubject = "lattice.op.status"

// TestOpStatusReachability proves the transport gate for the op-status RPC
// (op-status-read-surface-design.md Fire 1). The responder does NO
// caller-level authorization, so this publish allow-list IS the boundary:
// the bridge — the Fire 1 migrated consumer — may reach lattice.op.status,
// while an ordinary vertical app may not.
//
// The Processor hosts the responder in production (the sole sanctioned
// Core-KV reader). Here a processor-seed connection stands in as the
// responder: subscribe is unrestricted under the write-only-restriction
// model, and it replies over _INBOX.> (which the processor's publish
// allow-list carries — the same posture TestVaultDecryptReachability relies
// on).
func TestOpStatusReachability(t *testing.T) {
	url := startServerFromConf(t)

	resp := connectAs(t, url, "processor")
	sub, err := resp.NATS().Subscribe(opStatusSubject, func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"found":false}`))
	})
	if err != nil {
		t.Fatalf("processor subscribe %q: %v", opStatusSubject, err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := resp.NATS().Flush(); err != nil {
		t.Fatalf("flush responder: %v", err)
	}

	// The bridge is authorized to publish the request — it gets a reply promptly.
	bridge := connectAs(t, url, "bridge")
	reply, err := bridge.NATS().Request(opStatusSubject, []byte(`{"requestId":"x"}`), 3*time.Second)
	if err != nil {
		t.Fatalf("bridge request %q: want reply, got %v", opStatusSubject, err)
	}
	if len(reply.Data) == 0 {
		t.Fatalf("bridge request %q: empty reply", opStatusSubject)
	}

	// An ordinary vertical app is NOT authorized: its publish is rejected at the
	// transport, so the request never reaches the responder and the call times
	// out (the denied-publish signal for a plain request — no reply ever comes).
	rogue := connectAs(t, url, "clinic-app")
	rctx, rcancel := context.WithTimeout(context.Background(), deniedTimeout)
	defer rcancel()
	if _, err := rogue.NATS().RequestWithContext(rctx, opStatusSubject, []byte(`{"requestId":"x"}`)); err == nil {
		t.Errorf("clinic-app request %q: want transport denial (timeout), got a reply", opStatusSubject)
	}
}
