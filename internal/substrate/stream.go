package substrate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// StreamSpec describes a JetStream stream substrate provisions on a caller's
// behalf. It is substrate-owned (no jetstream type on the surface) and minimal:
// it covers the durable, file-backed, limits-retention diagnostic streams
// Lattice creates (the Refractor DLQ and audit streams). Storage is
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
	// MaxMsgsPerSubject bounds retained messages per subject. Zero means no
	// per-subject limit.
	MaxMsgsPerSubject int64
	// MaxBytes bounds total stream storage by size. Zero means no byte limit.
	MaxBytes int64
}

// EnsureStream creates or updates the stream described by spec (idempotent —
// safe to call on every startup). It is the substrate-blessed replacement for a
// caller reaching for js.CreateOrUpdateStream, so diagnostic streams
// can be provisioned without importing jetstream. The stream is file-backed with
// limits-based retention.
func (c *Conn) EnsureStream(ctx context.Context, spec StreamSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("substrate: EnsureStream: Name required")
	}
	cfg := jetstream.StreamConfig{
		Name:              spec.Name,
		Subjects:          spec.Subjects,
		Storage:           jetstream.FileStorage,
		Retention:         jetstream.LimitsPolicy,
		MaxAge:            spec.MaxAge,
		MaxMsgsPerSubject: spec.MaxMsgsPerSubject,
		MaxBytes:          spec.MaxBytes,
	}
	if _, err := c.js.CreateOrUpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("substrate: EnsureStream %q: %w", spec.Name, err)
	}
	return nil
}

// StreamNames returns the name of every JetStream stream currently
// provisioned on the account. Used by callers that enumerate streams by
// naming convention rather than tracking names themselves — e.g. retiring a
// superseded stream layout at boot.
func (c *Conn) StreamNames(ctx context.Context) ([]string, error) {
	lister := c.js.StreamNames(ctx)
	var names []string
	for name := range lister.Name() {
		names = append(names, name)
	}
	if err := lister.Err(); err != nil {
		return nil, fmt.Errorf("substrate: StreamNames: %w", err)
	}
	return names, nil
}

// DeleteStream removes the named JetStream stream. Idempotent: deleting a
// stream that does not exist is a no-op, not an error, so callers can retire
// a stream unconditionally on every startup.
func (c *Conn) DeleteStream(ctx context.Context, name string) error {
	if err := c.js.DeleteStream(ctx, name); err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil
		}
		return fmt.Errorf("substrate: DeleteStream %q: %w", name, err)
	}
	return nil
}
