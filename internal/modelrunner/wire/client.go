package wire

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// DefaultDispatchTimeout bounds the request/reply that carries the ack — the
// runner answers before it touches the vendor, so this covers a NATS round
// trip and nothing else. A model turn takes minutes; it is never waited on
// here.
const DefaultDispatchTimeout = 5 * time.Second

// Client dispatches generate requests to the runner queue group and reads the
// results the runner writes back. It holds no vendor credential and knows no
// model semantics — it is transport only.
type Client struct {
	nc      *nats.Conn
	subject string
	timeout time.Duration
}

// ClientOption adjusts a Client at construction.
type ClientOption func(*Client)

// WithSubject overrides the request subject (tests, or a future second
// endpoint).
func WithSubject(subject string) ClientOption {
	return func(c *Client) { c.subject = subject }
}

// WithDispatchTimeout overrides the ack round-trip bound.
func WithDispatchTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// NewClient builds a Client over an existing connection.
func NewClient(nc *nats.Conn, opts ...ClientOption) *Client {
	c := &Client{nc: nc, subject: GenerateSubject, timeout: DefaultDispatchTimeout}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Dispatch sends req and returns the runner's ack. A non-nil error means the
// fleet could not be reached or answered unintelligibly (nats.ErrNoResponders
// when no runner is deployed) — both transient from the caller's side. A nil
// error with a non-accepted Ack is the runner's own verdict; use Ack.Err to
// branch on it.
func (c *Client) Dispatch(ctx context.Context, req Request) (Ack, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Ack{}, fmt.Errorf("modelrunner: marshal request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	msg, err := c.nc.RequestWithContext(ctx, c.subject, body)
	if err != nil {
		return Ack{}, fmt.Errorf("modelrunner: dispatch %s: %w", c.subject, err)
	}
	var ack Ack
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		return Ack{}, fmt.Errorf("modelrunner: decode ack: %w", err)
	}
	return ack, nil
}
