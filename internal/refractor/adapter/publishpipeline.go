package adapter

import (
	"context"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// PublishPipelineOpener is an optional interface for adapters whose writes are
// JetStream publishes and can therefore be pipelined: the caller opens a
// pipeline, puts it on the context it writes under (WithPublishPipeline), runs
// its write loop, and flushes once — paying one ack round trip for the loop
// instead of one per row. Implemented by NatsSubjectAdapter.
//
// The pipeline MUST come from the adapter that will publish into it, which is
// why this is an adapter method rather than something a caller builds from any
// connection it happens to hold: a substrate.PublishPipeline publishes on the
// connection it was opened from, so one built elsewhere would silently send the
// adapter's messages down a different connection.
//
// A pipeline carries no atomicity — it orders publishes and defers their acks,
// nothing more. Conn.PublishBatch is the all-or-nothing primitive it is not.
//
// Adapters that write anywhere but a JetStream stream — NatsKVAdapter and the
// Postgres adapter, whose writes are guarded per-row revision operations, not
// appends — do not implement it, and a pipeline on the context is inert for
// them.
type PublishPipelineOpener interface {
	NewPublishPipeline() *substrate.PublishPipeline
}

// publishPipelineKey is the unexported context key WithPublishPipeline stores
// the pipeline under. The type is unexported so no other package can forge or
// read the same context entry.
type publishPipelineKey struct{}

// WithPublishPipeline returns a context under which a PublishPipelineOpener
// adapter's publishes join pl instead of awaiting their own store ack. The
// caller owns pl: it decides where the loop's acks are awaited by calling
// pl.Flush, and until that flush returns nil none of the writes made under the
// returned context is known to have landed.
//
// The pipeline rides the context rather than the adapter so the adapter stays
// stateless and its concurrent callers — the consumer goroutine, a device's
// hydrate, a grant-change reprojection — each own one of their own without any
// of them seeing another's futures.
//
// Scope it to the write loop, not the whole call: a context carrying a row
// pipeline must never reach the keyset frame, whose contract is that it is
// published only after the rows it describes have cleanly applied.
func WithPublishPipeline(ctx context.Context, pl *substrate.PublishPipeline) context.Context {
	return context.WithValue(ctx, publishPipelineKey{}, pl)
}

// publishPipelineFrom reads back the pipeline WithPublishPipeline installed on
// ctx, or nil when none was installed — in which case a publish awaits its own
// ack.
func publishPipelineFrom(ctx context.Context) *substrate.PublishPipeline {
	pl, _ := ctx.Value(publishPipelineKey{}).(*substrate.PublishPipeline)
	return pl
}
