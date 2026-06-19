package substrate

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// StreamSpec describes a JetStream stream substrate provisions on a caller's
// behalf. It is substrate-owned (no jetstream type on the surface) and minimal:
// it covers the durable, file-backed, limits-retention diagnostic streams
// Lattice creates per rule (the Refractor DLQ and audit streams). Storage is
// always file and retention is always limits — the only combination Lattice
// uses for these — so neither is exposed; extend the spec when a caller needs a
// different policy.
type StreamSpec struct {
	// Name is the JetStream stream name.
	Name string
	// Subjects are the subjects the stream captures.
	Subjects []string
	// MaxAge bounds message retention by age. Zero means no age limit.
	MaxAge time.Duration
}

// EnsureStream creates or updates the stream described by spec (idempotent —
// safe to call on every startup). It is the substrate-blessed replacement for a
// caller reaching for js.CreateOrUpdateStream, so per-rule diagnostic streams
// can be provisioned without importing jetstream. The stream is file-backed with
// limits-based retention.
func (c *Conn) EnsureStream(ctx context.Context, spec StreamSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("substrate: EnsureStream: Name required")
	}
	cfg := jetstream.StreamConfig{
		Name:      spec.Name,
		Subjects:  spec.Subjects,
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
		MaxAge:    spec.MaxAge,
	}
	if _, err := c.js.CreateOrUpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("substrate: EnsureStream %q: %w", spec.Name, err)
	}
	return nil
}
