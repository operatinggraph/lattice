//go:build js

package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall/js"

	"github.com/operatinggraph/lattice/internal/edge/transport"
)

// jsTransport satisfies the Edge engine's transport seam
// (internal/edge/transport, edge-browser-node-design.md §3.2) over a JS shell
// object, so the browser host feeds the same sync.Manager the trusted Go
// hosts feed through natstransport.
//
// The direction of each half follows who owns the connection. The shell owns
// it, so the inbound half is a PUSH: the shell hands each JetStream message
// to Deliver, and RunDurableConsumer only asks the shell to start the feed
// and then parks. The outbound half is a plain call out to the shell.
//
// The shell object must supply:
//
//	startConsumer({stream, durable, filterSubject}) -> Promise<void>
//	stopConsumer()                                  -> Promise<void> (optional)
//	request(subject, Uint8Array, actor)             -> Promise<Uint8Array>
//
// Everything the shell needs to know beyond that — the durable's
// InactiveThreshold, leader election, token-refresh reconnect — is transport
// policy this seam deliberately does not model: it is the shell's to own, and
// modelling it here would put browser-only concerns inside the semantics.
type jsTransport struct {
	shell js.Value

	mu      sync.Mutex
	handler transport.Handler
}

var _ transport.DeltaSource = (*jsTransport)(nil)
var _ transport.ControlClient = (*jsTransport)(nil)

func newJSTransport(shell js.Value) (*jsTransport, error) {
	for _, m := range []string{"startConsumer", "request"} {
		if shell.Get(m).Type() != js.TypeFunction {
			return nil, fmt.Errorf("edge/browser: shell is missing the %q function", m)
		}
	}
	return &jsTransport{shell: shell}, nil
}

// RunDurableConsumer asks the shell to start the durable feed named by cfg,
// then blocks until ctx is cancelled while Deliver dispatches to h. It does
// not itself receive: the shell pushes.
func (t *jsTransport) RunDurableConsumer(ctx context.Context, cfg transport.ConsumerConfig, h transport.Handler) error {
	t.mu.Lock()
	if t.handler != nil {
		t.mu.Unlock()
		return errors.New("edge/browser: a durable consumer is already running on this transport")
	}
	t.handler = h
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.handler = nil
		t.mu.Unlock()
	}()

	arg := map[string]any{
		"stream":        cfg.Stream,
		"durable":       cfg.Durable,
		"filterSubject": cfg.FilterSubject,
	}
	if _, err := await(ctx, t.shell.Call("startConsumer", arg)); err != nil {
		return fmt.Errorf("edge/browser: start consumer %q: %w", cfg.Durable, err)
	}

	<-ctx.Done()
	if t.shell.Get("stopConsumer").Type() == js.TypeFunction {
		// ctx is already done, so this cleanup gets its own context — passing
		// the cancelled one would abandon the call before the shell could
		// close the consumer.
		stopCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if _, err := await(stopCtx, t.shell.Call("stopConsumer")); err != nil {
			return fmt.Errorf("edge/browser: stop consumer %q: %w", cfg.Durable, err)
		}
	}
	return ctx.Err()
}

// Request issues one control-plane request-reply through the shell, carrying
// actor as the identity the control plane authorizes against.
func (t *jsTransport) Request(ctx context.Context, subject string, data []byte, actor string) ([]byte, error) {
	v, err := await(ctx, t.shell.Call("request", subject, toUint8Array(data), actor))
	if err != nil {
		return nil, fmt.Errorf("edge/browser: control request %q: %w", subject, err)
	}
	reply, err := toBytes(v)
	if err != nil {
		return nil, fmt.Errorf("edge/browser: control request %q: reply: %w", subject, err)
	}
	return reply, nil
}

// Deliver hands one message from the shell to the running handler and returns
// its disposition. The three verdicts are the transport package's, unchanged:
// "ack" advances the consumer, "nak" asks for redelivery, "term" drops the
// message permanently.
//
// A message arriving while no consumer is running is "nak"ed rather than
// dropped: the shell can push during the window between its own feed starting
// and RunDurableConsumer registering, and a dropped delta there is a silent
// hole in the mirror, while a redelivered one is free.
func (t *jsTransport) Deliver(ctx context.Context, d transport.Delta) string {
	t.mu.Lock()
	h := t.handler
	t.mu.Unlock()
	if h == nil {
		return "nak"
	}
	switch h(ctx, d) {
	case transport.Ack:
		return "ack"
	case transport.Term:
		return "term"
	default:
		return "nak"
	}
}
