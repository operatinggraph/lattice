package substrate

import (
	"context"
	"errors"
)

// ChunkedMultiGet reads items through read in requests of at most chunk items,
// handing each request's entries to visit as they arrive rather than
// accumulating a whole answer in memory.
//
// It exists because the multi-get fast path is bounded TWO ways and a caller
// can only predict one of them. The 1,024-matched-subject cap is a count, and
// chunking by count is what keeps a request on that path at all; the
// connection's negotiated MaxPending is a BYTE ceiling on the response (64 MiB
// by default), which no count can predict — a request of few but large values
// crosses it, and because the ceiling is deterministic it exhausts every
// internal retry identically and surfaces as a loud error rather than a short
// read (see KVGetMulti). Halving and retrying is how a caller discovers the
// size that fits without having to know the values' sizes up front.
//
// So a request that fails with the OVER-SIZE signature —
// ErrDirectGetAttemptsExhausted, the one failure a smaller request can fix — is
// split in half and each half retried, down to floor items; only a failure at
// the floor propagates. Every OTHER error propagates from the first request
// unchanged: splitting a stalled or refused read would multiply one caller's
// wait by the depth of the descent while it was never going to succeed at any
// size. A cancelled context short-circuits the splitting for the same reason.
//
// An ITEM is the unit of splitting, and need not be one subject: read is what
// expands an item into the subjects it needs, so a caller whose unit is several
// keys that must be read together — a document and the mark that explains it —
// keeps them together through every split. visit is handed the items of the
// request that just succeeded together with its entries, so it can resolve each
// item's own subjects itself.
//
// A chunk of zero or less is one request for everything; a floor below 1 is
// treated as 1. An empty items reads nothing.
func ChunkedMultiGet(
	ctx context.Context,
	items []string,
	chunk, floor int,
	read func(context.Context, []string) (map[string]*KVEntry, error),
	visit func(items []string, entries map[string]*KVEntry) error,
) error {
	if len(items) == 0 {
		return nil
	}
	if chunk <= 0 {
		chunk = len(items)
	}
	if floor < 1 {
		floor = 1
	}
	for start := 0; start < len(items); start += chunk {
		end := start + chunk
		if end > len(items) {
			end = len(items)
		}
		if err := readSplitting(ctx, items[start:end], floor, read, visit); err != nil {
			return err
		}
	}
	return nil
}

// readSplitting reads one request's items, halving and retrying on failure
// until the halves reach floor.
func readSplitting(
	ctx context.Context,
	items []string,
	floor int,
	read func(context.Context, []string) (map[string]*KVEntry, error),
	visit func(items []string, entries map[string]*KVEntry) error,
) error {
	entries, err := read(ctx, items)
	if err == nil {
		return visit(items, entries)
	}
	if !errors.Is(err, ErrDirectGetAttemptsExhausted) || len(items) <= floor || ctx.Err() != nil {
		return err
	}
	mid := len(items) / 2
	if half := readSplitting(ctx, items[:mid], floor, read, visit); half != nil {
		return half
	}
	return readSplitting(ctx, items[mid:], floor, read, visit)
}
