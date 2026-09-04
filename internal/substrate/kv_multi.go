package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Headers that tell apart the shapes a KV subject can carry once its value is
// gone. A removal the client asked for carries KVOperationHeader — DEL for a
// plain delete, PURGE for a purge; a marker the SERVER minted carries no
// KV-Operation at all and instead names why in MarkerReasonHeader —
// MarkerReasonMaxAge when a per-message TTL (or a stream age limit) expired the
// subject's last message.
//
// ADR-43 also declares "Purge" and "Remove" as marker reasons for a stream-admin
// removal, but at nats-server@v2.14.0 they are declared and never written: the
// only sites that mint a limit marker (filestore.go:6954, memstore.go:1374) set
// MaxAge, so MaxAge is the one reason a reader can actually observe at our pin.
// Vendor-grounded against nats-server@v2.14.0 (server/stream.go:641,688-689 for
// the declarations; filestore.go:6948-6963; ADR-43 Limit Markers) and
// nats.go@v1.52.0 (jetstream/kv.go:494-496, :1153-1161).
//
// Two readers key on them: this file's direct-get marker parse, and Loom's
// deadline watcher, which acts only on the server's expiry marker — a removal
// of the key is not an expiry (Contract #10 §10.3/§10.6).
const (
	KVOperationHeader  = "KV-Operation"
	KVOperationDelete  = "DEL"
	KVOperationPurge   = "PURGE"
	MarkerReasonHeader = "Nats-Marker-Reason"
	MarkerReasonMaxAge = "MaxAge"
)

// Raw JetStream Direct Get "multi_last" protocol constants, vendor-grounded
// live against nats-server@v2.14.0 / nats.go@v1.52.0 (docs/vendors.md) — not
// exposed by nats.go's client (grep of the pinned module: zero hits), so
// this hand-rolls the wire protocol the way KVPutWithTTL/KVUpdateWithTTL
// already do for other raw JS API shapes.
const (
	// directGetMultiAPIT is the request subject; %s is the KV bucket's
	// backing stream name ("KV_<bucket>").
	// nats-server/server/jetstream_api.go:106-115 (JSDirectMsgGetT).
	directGetMultiAPIT = "$JS.API.DIRECT.GET.%s"

	// Synthetic headers nats.go's own wire parser folds a response's inline
	// "NATS/1.0 <code> <description>" status line into
	// (nats.go/nats.go:4282-4327) — every message on the response
	// subscription goes through the same parser as any other subscription,
	// so no manual status-line parsing is needed here.
	directGetStatusHdr = "Status"
	directGetDescrHdr  = "Description"

	directGetStatusEOB            = "204"
	directGetStatusNoResults      = "404"
	directGetDescrNoResults       = "No Results"
	directGetStatusTooManyResults = "413"

	// Data-message headers (nats-server/server/stream.go:669-677).
	directGetSubjectHdr    = "Nats-Subject"
	directGetSequenceHdr   = "Nats-Sequence"
	directGetTimeStampHdr  = "Nats-Time-Stamp"
	directGetNumPendingHdr = "Nats-Num-Pending"

	// directGetRetries bounds the fast-path retry-whole loop. A short read
	// (a mid-stream "404 Message Not Found", or a lost/dropped response) is
	// abnormal by construction — the request never sets batch/max_bytes, so
	// the legal response is always well under the server's send budget — a
	// small bounded retry absorbs a transient blip without masking a
	// persistent fault. Each attempt is independently atomic; a retry never
	// merges partial results across attempts.
	directGetRetries      = 3
	directGetRetryBackoff = 50 * time.Millisecond

	// directGetChanBuffer sizes the raw response subscription's channel. The
	// server's largest legal single burst is 1,024 DATA messages PLUS one
	// EOB — 1,025 total (nats-server's getDirectMulti sends one message per
	// matched subject, then an EOB status message). ChanSubscribe does not
	// block a full channel; nats.go does a non-blocking send and DROPS the
	// message on a full buffer (raising a slow-consumer async error) — so
	// this must clear the real burst size with real headroom, not just meet
	// it exactly (a buffer sized to precisely 1,024 loses the EOB itself on
	// the very burst it was sized for).
	directGetChanBuffer = 2048

	// directGetMultiDefaultTimeout bounds ONE fast-path attempt when the
	// caller's ctx carries no deadline of its own. Every OTHER substrate KV
	// read gets an equivalent bound for free from nats.go's high-level
	// client (jetstream's defaultAPITimeout, applied by
	// wrapContextWithoutDeadline underneath KVGet) — this hand-rolled raw
	// protocol bypasses that safety net entirely, so a lost response (a
	// dropped connection, a server restart, a dropped EOB despite the
	// buffer above) would otherwise block the caller forever on a
	// deadline-free ctx instead of surfacing as a retryable short read.
	// Matches nats.go's own default so behavior stays consistent with every
	// other KV call in this package.
	directGetMultiDefaultTimeout = 5 * time.Second

	// directGetFallbackRetries bounds the stability-verified double-drain
	// retry loop: two consecutive drains disagreeing means a write raced the
	// snapshot, not that the read failed.
	directGetFallbackRetries      = 3
	directGetFallbackRetryBackoff = 50 * time.Millisecond
	// directGetFallbackFetchWait bounds each ephemeral consumer's Fetch call.
	directGetFallbackFetchWait = 10 * time.Second
	// directGetFallbackDrainRounds bounds how many fetch rounds one drain
	// takes to empty the consumer's pending set. A round drains everything
	// pending when it started, so each round leaves only what was written
	// during the previous one and the backlog shrinks fast; more rounds than
	// this means a writer is outpacing the reader indefinitely, which is a
	// loud error rather than a partial answer.
	directGetFallbackDrainRounds = 8
	// directGetFallbackConsumerTTL cleans up an ephemeral fallback consumer
	// server-side even if this process dies mid-drain before its own
	// best-effort delete runs.
	directGetFallbackConsumerTTL = 30 * time.Second
)

// ErrDirectGetAttemptsExhausted marks the one multi-get failure a SMALLER
// request can fix: the fast path retried a whole request to exhaustion and
// every attempt ended short.
//
// It is the signature of an over-size response. The fast path is bounded by the
// connection's negotiated MaxPending as well as by the subject count, and that
// byte ceiling is deterministic — a request over it ends short on every
// attempt, so the retry loop runs out. A transient short read is absorbed by
// that same loop and never surfaces; an API or transport failure returns
// without entering it, carrying a different error. So a caller sizing its own
// requests (ChunkedMultiGet) can treat this, and only this, as "try a smaller
// one".
var ErrDirectGetAttemptsExhausted = errors.New("substrate: KV get-multi: attempts exhausted")

// directGetSizeSignatureFloor is the least aggregate payload an exhausted
// attempt must already have received before its failure is read as an OVER-SIZE
// one, and so as ErrDirectGetAttemptsExhausted.
//
// Two different failures exhaust the retry loop, and the error alone does not
// tell them apart. A response larger than the ceiling the response subscription
// buffers against ends short deterministically and exhausts every attempt
// identically — that one a SMALLER request fixes. A mid-stream "404 Message Not
// Found", a concurrent delete racing the read of a hot subject, exhausts the
// same way and no smaller request fixes it: splitting it would multiply one
// caller's wait by the depth of the descent.
//
// The bytes separate them. A response near the ceiling has already delivered
// most of the ceiling before ending short; a racing 404 ends after almost
// nothing. So the floor is DERIVED from that ceiling rather than written down:
// an eighth of it is far above what a racing failure delivers and far below any
// request that could genuinely be over it, and the derivation tracks the client
// if the default ever moves.
//
// The ceiling is the client's own subscription pending-BYTES default, which is
// what this read's channel subscription is given (nats.go v1.52.0 nats.go:4929
// sets pBytesLimit = DefaultSubPendingBytesLimit for every subscription, chan
// ones included). It is deliberately not probed at runtime: PendingLimits()
// refuses a ChanSubscription outright (nats.go:5657-5659), so a probe here
// could only ever fall back to this value — an adaptive-looking branch that can
// never fire. A caller that starts setting per-subscription limits is what would
// make a probe meaningful, and there is none.
const directGetSizeSignatureFloor = nats.DefaultSubPendingBytesLimit / 8

var (
	// errDirectGetShortRead is the internal retry-whole signal: the response
	// stream ended before a clean EOB. Never returned to a KVGetMulti caller
	// — directGetMulti retries on it, and only its final, attempts-exhausted
	// wrap ever surfaces.
	errDirectGetShortRead = errors.New("substrate: KV get-multi: short read")
	// errDirectGetTooManyResults is the internal 413 signal that routes
	// KVGetMulti to the stability-verified fallback. Never returned to callers.
	errDirectGetTooManyResults = errors.New("substrate: KV get-multi: too many results")
	// errDirectGetFallbackUnstable means the stability-verified fallback's
	// double drain never agreed within directGetFallbackRetries attempts. It
	// belongs to KVGetMulti alone — KVGetMultiNoSnapshot does not drain, and so
	// has nothing to stabilize.
	errDirectGetFallbackUnstable = errors.New("substrate: KV get-multi: fallback snapshot did not stabilize")
	// errDirectGetFallbackNeverDrained means one drain never emptied the
	// consumer's pending set within directGetFallbackDrainRounds rounds.
	errDirectGetFallbackNeverDrained = errors.New("substrate: KV get-multi: fallback consumer never drained")
)

// directGetMultiRequest is the raw JetStream Direct Get API request body for
// a multi-subject read. Field/JSON-tag confirmed live against
// nats-server@v2.14.0/server/jetstream_api.go:688 (JSApiMsgGetRequest).
type directGetMultiRequest struct {
	MultiLastFor []string `json:"multi_last,omitempty"`
}

// KVGetMulti reads every LIVE entry among keys — each either an exact key
// ("vtx.identity.<id>") or a NATS wildcard filter ("lnk.permission.<id>.>")
// — from bucket in one atomic, point-in-time snapshot: the batched,
// multi-subject counterpart to KVGet. The whole response is computed under
// the stream's read lock (nats-server's getDirectMulti), so distinct keys
// never straddle a concurrent write the way N sequential KVGet calls can.
//
// KVGetMulti does NOT validate keys the way KVGet incidentally does (via
// nats.go's own key-charset check, which happens to reject "*"/">"):
// because filters are a first-class part of this primitive's contract, an
// unrecognized string is a FILTER, not a rejected input — "*"/">" match
// broadly rather than erroring. A caller that must not let a wildcard
// through (e.g. treating client-supplied, untrusted strings as declared
// exact keys) is responsible for validating each key's shape itself before
// calling — see internal/processor/step4_hydrate.go's ClassifyKey guard for
// the pattern. Callers should also not mix a wildcard filter with a literal
// key it would itself match: the fast path dedups such overlaps
// server-side, but the 1,024+ fallback's consumer FilterSubjects rejects
// genuine overlaps outright (exact duplicates are dedupe'd for the caller;
// a filter subsuming another entry is not).
//
// The returned map is keyed by the resolved bare key (bucket prefix
// stripped, same shape as KVGet's KVEntry.Key) and holds only keys that
// matched and are live. A key absent from the map is either genuinely
// absent or NATS-hard-deleted/purged — the batched analog of KVGet's
// errors.Is(err, ErrKeyNotFound): no per-key error, only per-key map
// membership. An entry soft-tombstoned in its own envelope ("isDeleted":
// true) is still live at the JetStream level and IS included, mirroring
// KVGet's documented behavior — inspect the envelope yourself.
//
// keys may match at most 1,024 combined subjects on the fast path; beyond
// that, KVGetMulti transparently falls back to a stability-verified
// ephemeral-consumer drain (slower; still returns a verified point-in-time
// snapshot, never a torn one). KVGetMultiNoSnapshot, which does not owe that
// snapshot, resolves the request to exact keys against the stream's own subject
// state and reads them in fast-path chunks instead. An empty keys returns an
// empty map, nil error.
//
// LATENCY, and why a caller on a latency-sensitive path must pass a deadline:
// the fast path is bounded by directGetMultiDefaultTimeout per attempt. The
// fallback is not comparably cheap. One drain runs up to
// directGetFallbackDrainRounds rounds, each bounded by a fetch wait that the
// pinned jetstream client cannot interrupt once it starts, and the
// stability check runs two drains per attempt over directGetFallbackRetries
// attempts. At the constants above that is a worst case of 8 x 10s = 80s per
// drain and 6 drains = 480s for the whole call — reached only when every
// round's pull is starved, but reachable. A ctx WITH a deadline caps all of
// it: each round's wait is clamped to the time remaining, so the call cannot
// outlive the deadline by more than one round's setup. That ceiling is
// KVGetMulti's alone: KVGetMultiNoSnapshot does not drain at all, and states
// its own bound.
//
// The fast path's response is also bounded by the connection's negotiated
// MaxPending (a send-budget ceiling independent of the 1,024-subject cap —
// 64 MiB by default). A matched set whose AGGREGATE VALUE SIZE exceeds it
// (e.g. very few subjects but very large values) triggers the same
// short-read handling as a transient partial response and, since the
// ceiling is deterministic, exhausts every retry identically — surfaced as
// a loud "attempts exhausted" error, never a silent partial read. Every
// caller in this codebase today reads small, bounded sets well under this
// ceiling; a future caller of large-valued keys should chunk its request.
func (c *Conn) KVGetMulti(ctx context.Context, bucket string, keys []string) (map[string]*KVEntry, error) {
	return c.kvGetMulti(ctx, bucket, keys, true)
}

// KVGetMultiNoSnapshot is KVGetMulti with the point-in-time guarantee
// dropped, for a caller that needs the whole matched set but never needed it
// to come from one instant.
//
// What it KEEPS, in full:
//
//   - The fast path, unchanged. At or under 1,024 matched subjects the
//     response is still computed under the stream's read lock, so a small
//     read is atomic here exactly as in KVGetMulti — the two differ only past
//     the cap.
//   - Completeness. The drain runs until the server reports nothing pending
//     (drainDirectGetFallback), so every subject live across the read is
//     observed at one of its revisions, and a drain that cannot reach that
//     point is a loud error rather than a silently short map. That property,
//     not the stability comparison, is what proves the answer whole — which
//     is why dropping the comparison does not weaken it.
//   - Every other behavior of the primitive: filters, marker dropping,
//     soft-tombstone inclusion, key shape, error wrapping.
//
// What it DROPS: past the 1,024-subject cap, the stability verification.
// KVGetMulti drains twice and compares the two (key -> revision) maps,
// retrying until they agree, so the set it returns provably existed
// simultaneously. This does not drain at all. Past the cap it RESOLVES the
// request to exact keys — every wildcard's subjects taken from the stream's own
// state in one request, every literal passed through, the union de-duplicated —
// and reads those keys on the fast path in chunks of at most 1,024
// (resolveThenGetFallback). The result can therefore blend instants: one entry
// carrying a revision from before a write while another carries one from after,
// and a key created after the resolution not appearing at all.
//
// WHY that is the right trade for some callers, and why it is not a general
// upgrade: the double drain fails, hard, whenever ANY matched key moves
// between the passes. Over a large set on a busy subject space that is not an
// edge case but the normal condition, and the failure is total — no partial
// answer, just an error the caller turns into a retry, on a read that was
// expensive to begin with. A caller whose read set is a collection of
// INDEPENDENT facts, each valid on its own, and which re-validates its work
// by another route (re-reading and comparing a footprint, say), gains nothing
// from the simultaneity and pays for it in availability. A caller assembling
// a read set that BACKS A DECISION — a balance, a quorum, a set-membership
// test whose answer changes if two members disagree about when they were read
// — needs KVGetMulti and must not reach for this.
//
// The first caller is the Refractor's overflow-marked adjacency read: some
// thousands of independent edges on the single busiest node in the graph,
// re-validated per evaluation by the pipeline's footprint comparison. Under
// KVGetMulti that read fails whenever the node is taking writes, which for
// the node that overflowed is essentially always.
//
// LATENCY: one stream-info request for the stream handle plus one per DISTINCT
// wildcard in the request, then ceil(N/1024) fast-path requests over the
// resolved keys, each bounded by directGetMultiDefaultTimeout. When the caller
// passes no deadline the client bounds each stream-info call by its default API
// timeout — and that one budget covers the WHOLE paged loop, so a filter
// matching more subjects than fit in that budget FAILS rather than taking
// longer (the client's own note puts the paging threshold around 100k
// subjects). A fast-path request refused for its aggregate SIZE is retried over
// halves down to a floor, so the read side's worst case is that descent's
// requests rather than one; a failure that is NOT the size signature is never
// descended on and costs one request. There is no drain, and so no 80s
// round-starved ceiling — which is the difference that matters at this size: a
// drain of a few thousand subjects costs about a millisecond per key, where
// resolving the same set and reading it in chunks costs a fraction of that.
func (c *Conn) KVGetMultiNoSnapshot(ctx context.Context, bucket string, keys []string) (map[string]*KVEntry, error) {
	return c.kvGetMulti(ctx, bucket, keys, false)
}

// kvGetMulti is the shared body of both entry points. verifyStable selects
// what happens past the fast path's 1,024-subject cap: the stability-verified
// double drain, or a single one.
func (c *Conn) kvGetMulti(ctx context.Context, bucket string, keys []string, verifyStable bool) (map[string]*KVEntry, error) {
	if len(keys) == 0 {
		return map[string]*KVEntry{}, nil
	}
	pre := "$KV." + bucket + "."
	subjects := make([]string, len(keys))
	for i, k := range keys {
		subjects[i] = pre + k
	}
	entries, err := c.directGetMulti(ctx, bucket, subjects, pre)
	if err == nil {
		return entries, nil
	}
	if errors.Is(err, errDirectGetTooManyResults) {
		if verifyStable {
			return c.kvGetMultiFallback(ctx, bucket, subjects, pre)
		}
		return c.resolveThenGetFallback(ctx, bucket, subjects, pre)
	}
	return nil, err
}

// directGetSubjectCap is the fast path's cap on MATCHED SUBJECTS: at or under
// it the whole response is computed under the stream's read lock in one round
// trip, and past it the server answers 413 and the request needs another
// strategy. It is also the chunk size the resolved-key read below asks in, so
// every one of those requests stays on that path.
const directGetSubjectCap = 1024

// directGetChunkFloor bounds the split-on-failure descent ChunkedMultiGet takes
// when a chunk of exact keys is refused for its aggregate SIZE rather than its
// count. Sixteen entries is far under any response ceiling, so a failure there
// is about something other than size and is the caller's.
const directGetChunkFloor = 16

// resolveThenGetFallback answers a NoSnapshot request the fast path refused for
// its matched-subject count, by RESOLVING the request to exact subjects and
// then reading those on the fast path in chunks.
//
// It replaces a consumer drain with two cheap primitives, and the reason is
// measured: a drain of a few thousand subjects costs about a millisecond per
// key (seconds for one wide read), while exact-key multi-gets run about ten
// milliseconds per thousand keys and the resolution is a single API round trip
// per filter. The drain's expense buys completeness under a writer, and this
// variant's callers do not need the SNAPSHOT that expense also buys — they hold
// sets of independent facts and re-validate by another route.
//
// The resolution comes from the STREAM's own subject state, not from an
// enumeration:
//
//   - a requested subject carrying `*` or `>` is a FILTER, and its matches are
//     taken from a stream-info request scoped to it (jetstream.WithSubjectFilter),
//     whose subject set the server computes under the same stream lock that
//     serves the fast path itself. It has no count-bounded stop condition, so it
//     cannot come back short because a subject was being rewritten while it ran
//     — which is exactly what a consumer-backed key enumeration can do, and why
//     one is not used here (see KVListKeysFilter, whose own callers tolerate a
//     hint-grade answer);
//   - a requested subject naming one key passes through untouched, including
//     when that key does not exist — the read below simply returns nothing for
//     it, exactly as the fast path does;
//   - the request is de-duplicated before resolution, so a repeated filter costs
//     one round trip, and the union is de-duplicated after it, so a key matched
//     by two filters, or by a filter and a literal it also matches, is asked for
//     once. That is stricter than the drain could be: a consumer's FilterSubjects
//     rejects overlapping filters outright, and here overlap is just a set union.
//
// What the subject set does NOT decide is existence. A subject whose last
// message is a delete or purge MARKER is still a subject of the stream and is
// still resolved, and the exact-key read below is the arbiter: it drops markers,
// so such a key is absent from the answer — the same absence the fast path
// reports for it, and the batched analog of KVGet's ErrKeyNotFound. A soft
// tombstone ("isDeleted" in its own envelope) is an ordinary live entry to both
// and IS returned. Revisions are the read's own.
//
// The answer is therefore NOT from one instant, which is this variant's whole
// contract. The subject set is a point in time; each chunk is read at a later
// one. A key deleted between the two is absent, a key written between them is
// read at its later revision, and a key created after the resolution is not in
// the answer at all. KVGetMultiNoSnapshot permits exactly that; a caller that
// needs simultaneity needs KVGetMulti, whose double drain is the comparison
// that provides it.
//
// A chunk that fails after ChunkedMultiGet has split it to the floor is
// returned as an error, and so is a resolution that fails. There is no partial
// answer: an incomplete set from a primitive whose contract is completeness is
// the silent-wrong answer this fallback exists to avoid.
func (c *Conn) resolveThenGetFallback(ctx context.Context, bucket string, subjects []string, pre string) (map[string]*KVEntry, error) {
	infos, requests := 0, 0
	if hook := kvResolveThenGetHook(ctx); hook != nil {
		defer func() { hook(infos, requests) }()
	}
	streamName := "KV_" + bucket

	// The stream handle is fetched ONCE for the whole resolution: js.Stream is
	// itself a stream-info round trip, so fetching it per filter would double
	// the resolution's cost.
	var stream jetstream.Stream
	resolve := func(filter string) ([]string, error) {
		if stream == nil {
			infos++
			handle, err := c.js.Stream(ctx, streamName)
			if err != nil {
				return nil, fmt.Errorf("substrate: KV get-multi %s: resolve filter %q: %w", bucket, filter, err)
			}
			stream = handle
		}
		infos++
		return resolveFilterSubjects(ctx, bucket, stream, filter)
	}

	keys := make([]string, 0, len(subjects))
	seen := make(map[string]struct{}, len(subjects))
	add := func(key string) {
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for _, subject := range dedupeStrings(subjects) {
		if !strings.ContainsAny(subject, "*>") {
			add(strings.TrimPrefix(subject, pre))
			continue
		}
		matched, err := resolve(subject)
		if err != nil {
			return nil, err
		}
		for _, resolved := range matched {
			add(strings.TrimPrefix(resolved, pre))
		}
	}

	if hook := kvResolvedKeysHook(ctx); hook != nil {
		hook(keys)
	}

	out := make(map[string]*KVEntry, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	read := func(ctx context.Context, chunk []string) (map[string]*KVEntry, error) {
		chunkSubjects := make([]string, len(chunk))
		for i, key := range chunk {
			chunkSubjects[i] = pre + key
		}
		requests++
		return c.directGetMulti(ctx, bucket, chunkSubjects, pre)
	}
	visit := func(_ []string, entries map[string]*KVEntry) error {
		for key, entry := range entries {
			out[key] = entry
		}
		return nil
	}
	if err := ChunkedMultiGet(ctx, keys, directGetSubjectCap, directGetChunkFloor, read, visit); err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: read %d resolved keys: %w", bucket, len(keys), err)
	}
	return out, nil
}

// resolveFilterSubjects returns every subject of stream matching filter, read
// from the stream's own state in ONE request.
//
// jetstream.WithSubjectFilter scopes a stream-info request to the filter, and
// the server computes State.Subjects under the stream lock — the same lock that
// serves the fast path's multi_last — so the set is a point-in-time answer with
// no stop condition to fire early. The pinned client pages the request itself
// when the filter matches more subjects than fit one response, looping until it
// has read every page (nats.go v1.52.0 jetstream/stream.go's Info), and bounds
// the WHOLE loop by its default API timeout when the caller's ctx carries no
// deadline (wrapContextWithoutDeadline).
//
// filter is a NATS subject filter and is validated by nothing on this side:
// WithSubjectFilter passes it through, and the SERVER's own subject rules are
// what an unspellable filter fails against.
func resolveFilterSubjects(ctx context.Context, bucket string, stream jetstream.Stream, filter string) ([]string, error) {
	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(filter))
	if err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: resolve filter %q: %w", bucket, filter, err)
	}
	out := make([]string, 0, len(info.State.Subjects))
	for subject := range info.State.Subjects {
		out = append(out, subject)
	}
	return out, nil
}

// directGetMulti runs the fast multi_last path with a bounded retry-whole
// loop absorbing short reads.
func (c *Conn) directGetMulti(ctx context.Context, bucket string, subjects []string, pre string) (map[string]*KVEntry, error) {
	var lastErr error
	var receivedPerAttempt []int
	for attempt := 1; attempt <= directGetRetries; attempt++ {
		got, err := c.directGetMultiOnce(ctx, bucket, subjects, pre)
		if err == nil {
			return got.entries, nil
		}
		if !errors.Is(err, errDirectGetShortRead) {
			return nil, err
		}
		receivedPerAttempt = append(receivedPerAttempt, got.received)
		lastErr = err
		if attempt == directGetRetries {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("substrate: KV get-multi %s: %w", bucket, ctx.Err())
		case <-time.After(directGetRetryBackoff):
		}
	}
	if exhaustionIsOverSize(receivedPerAttempt, directGetSizeSignatureFloor) {
		return nil, fmt.Errorf("substrate: KV get-multi %s: %d attempts: %w: %w",
			bucket, directGetRetries, ErrDirectGetAttemptsExhausted, lastErr)
	}
	return nil, fmt.Errorf("substrate: KV get-multi %s: %d attempts exhausted: %w",
		bucket, directGetRetries, lastErr)
}

// exhaustionIsOverSize decides which of the two exhausting failures a run of
// short reads was — the whole basis on which ChunkedMultiGet does or does not
// retry over smaller halves.
//
// EVERY attempt must have received at least floor bytes. A response over the
// subscription's pending-byte ceiling has already delivered most of that
// ceiling before ending short, on every attempt, because the ceiling is
// deterministic; a mid-stream 404 racing a delete ends after almost nothing,
// and no smaller request fixes it. One small attempt is therefore enough to say
// the run was not about size. No attempts at all is not an over-size failure
// either: nothing was observed to classify.
func exhaustionIsOverSize(receivedPerAttempt []int, floor int) bool {
	if len(receivedPerAttempt) == 0 {
		return false
	}
	for _, received := range receivedPerAttempt {
		if received < floor {
			return false
		}
	}
	return true
}

// directGetAttempt is one fast-path attempt's outcome: what it collected, and
// how many payload bytes it received before it ended — the quantity that says
// which of the two exhausting failures a run of short reads was.
type directGetAttempt struct {
	entries  map[string]*KVEntry
	received int
}

func (c *Conn) directGetMultiOnce(ctx context.Context, bucket string, subjects []string, pre string) (directGetAttempt, error) {
	// received accumulates the payload bytes this attempt actually got before
	// it ended, which is what tells an over-size failure from a racing one.
	received := 0
	streamName := "KV_" + bucket
	reqSubj := fmt.Sprintf(directGetMultiAPIT, streamName)
	body, err := json.Marshal(directGetMultiRequest{MultiLastFor: subjects})
	if err != nil {
		return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: encode request: %w", bucket, err)
	}

	attemptCtx := ctx
	cancel := func() {}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		attemptCtx, cancel = context.WithTimeout(ctx, directGetMultiDefaultTimeout)
	}
	defer cancel()

	// c.nc.NewInbox (not the package-level nats.NewInbox) honors this
	// connection's CustomInboxPrefix, required for the response to be
	// delivered at all on a per-identity subscribe-ACL-scoped connection.
	inbox := c.nc.NewInbox()
	ch := make(chan *nats.Msg, directGetChanBuffer)
	sub, err := c.nc.ChanSubscribe(inbox, ch)
	if err != nil {
		return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: subscribe: %w", bucket, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	req := nats.NewMsg(reqSubj)
	req.Reply = inbox
	req.Data = body
	if err := c.nc.PublishMsg(req); err != nil {
		return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: publish request: %w", bucket, err)
	}

	entries := make(map[string]*KVEntry, len(subjects))
	for {
		select {
		case <-attemptCtx.Done():
			if cerr := ctx.Err(); cerr != nil {
				return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: %w", bucket, cerr)
			}
			return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: %w: no response within %s", bucket, errDirectGetShortRead, directGetMultiDefaultTimeout)
		case m, ok := <-ch:
			if !ok {
				return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: %w: response subscription closed before EOB", bucket, errDirectGetShortRead)
			}
			switch status := m.Header.Get(directGetStatusHdr); status {
			case "":
				received += len(m.Data)
				entry, isMarker, perr := parseDirectGetEntry(m, bucket, pre)
				if perr != nil {
					return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: %w", bucket, perr)
				}
				if !isMarker {
					entries[entry.Key] = entry
				}
			case directGetStatusEOB:
				if pending := m.Header.Get(directGetNumPendingHdr); pending != "0" {
					return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: %w: Nats-Num-Pending=%s at EOB", bucket, errDirectGetShortRead, pending)
				}
				return directGetAttempt{entries: entries, received: received}, nil
			case directGetStatusNoResults:
				if m.Header.Get(directGetDescrHdr) == directGetDescrNoResults {
					return directGetAttempt{entries: entries, received: received}, nil
				}
				// A mid-stream "404 Message Not Found": the matched-subject
				// set was computed, but loading one of its messages then
				// failed (a concurrent delete/purge racing the read).
				return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: %w: %s", bucket, errDirectGetShortRead, m.Header.Get(directGetDescrHdr))
			case directGetStatusTooManyResults:
				return directGetAttempt{received: received}, errDirectGetTooManyResults
			default:
				return directGetAttempt{received: received}, fmt.Errorf("substrate: KV get-multi %s: unexpected response status %s %s", bucket, status, m.Header.Get(directGetDescrHdr))
			}
		}
	}
}

// parseDirectGetEntry decodes one direct-get data message (a live KV entry
// or a NATS-level tombstone marker) into a KVEntry. isMarker reports a
// hard-delete/purge marker — mirrors KVGet's documented semantics: a
// NATS-level tombstone is absent, an in-envelope soft tombstone
// ("isDeleted": true) is still a live entry the caller inspects itself.
func parseDirectGetEntry(m *nats.Msg, bucket, pre string) (entry *KVEntry, isMarker bool, err error) {
	subj := m.Header.Get(directGetSubjectHdr)
	if subj == "" {
		return nil, false, fmt.Errorf("data message missing %s header", directGetSubjectHdr)
	}
	if op := m.Header.Get(KVOperationHeader); op == KVOperationDelete || op == KVOperationPurge {
		return nil, true, nil
	}
	if m.Header.Get(MarkerReasonHeader) != "" {
		return nil, true, nil
	}

	seqStr := m.Header.Get(directGetSequenceHdr)
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return nil, false, fmt.Errorf("data message %s header %q: %w", directGetSequenceHdr, seqStr, err)
	}
	tsStr := m.Header.Get(directGetTimeStampHdr)
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return nil, false, fmt.Errorf("data message %s header %q: %w", directGetTimeStampHdr, tsStr, err)
	}

	return &KVEntry{
		Bucket:    bucket,
		Key:       strings.TrimPrefix(subj, pre),
		Value:     m.Data,
		Revision:  seq,
		Timestamp: ts,
	}, false, nil
}

// kvGetMultiFallback serves a KVGetMulti request whose subjects matched over
// the 1,024-subject fast-path cap, via an ephemeral DeliverLastPerSubject
// consumer on the KV bucket's backing stream. Unlike the fast path (one
// atomic snapshot under the stream read lock), a pull consumer's initial
// delivery is NOT atomic at history 1 — no per-subject skip list is built
// for the MaxMsgsPer==1 short-circuit — so a concurrent write during the
// drain can both hide a live key and blend instants. This runs the drain
// TWICE and compares the two (key -> revision) maps: equal means no write
// raced either pass, so the set is a true point-in-time state; unequal
// retries (bounded, short backoff).
//
// Exact-duplicate subjects are collapsed before building the consumer's
// FilterSubjects: the fast path's multi_last dedups matches server-side, but
// a pull consumer's FilterSubjects rejects overlapping entries outright
// (err_code=10138) — an exact duplicate is the one overlap case cheap and
// safe to resolve here. A wildcard filter that itself SUBSUMES another
// listed entry (e.g. "vtx.identity.>" alongside "vtx.identity.<id>") is not
// collapsed — general NATS subject-subsumption detection is out of scope —
// and still hard-fails the fallback with the server's own overlap error;
// callers must not mix a filter with a literal it would itself match.
func (c *Conn) kvGetMultiFallback(ctx context.Context, bucket string, subjects []string, pre string) (map[string]*KVEntry, error) {
	streamName := "KV_" + bucket
	subjects = dedupeStrings(subjects)
	var lastErr error
	for attempt := 1; attempt <= directGetFallbackRetries; attempt++ {
		first, err := c.drainDirectGetFallback(ctx, bucket, streamName, subjects, pre)
		if err != nil {
			return nil, err
		}
		second, err := c.drainDirectGetFallback(ctx, bucket, streamName, subjects, pre)
		if err != nil {
			return nil, err
		}
		if directGetSnapshotsAgree(first, second) {
			return second, nil
		}
		lastErr = errDirectGetFallbackUnstable
		if attempt == directGetFallbackRetries {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, ctx.Err())
		case <-time.After(directGetFallbackRetryBackoff):
		}
	}
	return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %d attempts exhausted: %w", bucket, directGetFallbackRetries, lastErr)
}

// dedupeStrings returns subjects with exact-duplicate entries collapsed,
// order preserved from the first occurrence.
func dedupeStrings(subjects []string) []string {
	seen := make(map[string]struct{}, len(subjects))
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// directGetSnapshotsAgree reports whether two fallback drains observed the
// identical (key -> revision) state — the stability check that promotes a
// non-atomic double-read into a verified point-in-time snapshot.
func directGetSnapshotsAgree(a, b map[string]*KVEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for k, ea := range a {
		eb, ok := b[k]
		if !ok || ea.Revision != eb.Revision {
			return false
		}
	}
	return true
}

// drainDirectGetFallback runs one full drain of an ephemeral
// DeliverLastPerSubject consumer filtered to subjects and returns the live
// entries it observed. The consumer is deleted (best-effort) when the drain
// finishes; InactiveThreshold also reclaims it server-side if this process
// dies first.
//
// "Full" means drained until the SERVER reports nothing pending, not until a
// count taken at the start has been met, and the difference is the whole
// correctness of this function under a bucket taking writes. The count is not
// a reliable stopping target on a history-1 stream: overwriting a key erases
// the message the initial pending set was counting and appends a new one at a
// later sequence. The rewritten subject IS still delivered — from its new
// sequence, later in the stream — but a fetch bounded by the initial count can
// stop before reaching it, while the counts balance (one message erased ahead
// of the cursor, one appended behind it) and the drain looks complete. Two
// rewrites of one undelivered subject are enough to lose it that way. Draining
// until the server reports nothing pending is what actually establishes that
// every subject live across the read was observed, at one of its revisions.
//
// Because the map accumulates across rounds, a NATS-level tombstone delivered
// in a later round must REMOVE the live entry an earlier round collected for
// the same subject, not merely be skipped. A key hard-deleted mid-drain is
// deleted, and any read that treats this map as a live set — an authorization
// walk over a hub's links, say — must never be handed the revoked entry.
//
// The rounds are bounded. Under a writer fast enough to keep the consumer
// permanently behind, the pending set never empties, and this returns a loud
// error rather than the silent partial the count-bounded fetch would have
// returned. Each round's fetch is bounded by the caller's deadline when there
// is one, so a caller on a latency-sensitive path can cap the whole drain; see
// KVGetMulti's doc for the ceiling without one.
func (c *Conn) drainDirectGetFallback(ctx context.Context, bucket, streamName string, subjects []string, pre string) (map[string]*KVEntry, error) {
	cons, err := c.js.CreateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		DeliverPolicy:     jetstream.DeliverLastPerSubjectPolicy,
		FilterSubjects:    subjects,
		AckPolicy:         jetstream.AckNonePolicy,
		InactiveThreshold: directGetFallbackConsumerTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: fallback consumer: %w", bucket, err)
	}
	name := cons.CachedInfo().Name
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = c.js.DeleteConsumer(dctx, streamName, name)
	}()

	entries := map[string]*KVEntry{}
	for round := 0; round < directGetFallbackDrainRounds; round++ {
		info, err := cons.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("substrate: KV get-multi %s: fallback consumer info: %w", bucket, err)
		}
		if info.NumPending == 0 {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, err)
			}
			return entries, nil
		}

		wait, werr := directGetFetchWait(ctx)
		if werr != nil {
			return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, werr)
		}
		batch, err := cons.Fetch(int(info.NumPending), jetstream.FetchMaxWait(wait))
		if err != nil {
			return nil, fmt.Errorf("substrate: KV get-multi %s: fallback fetch: %w", bucket, err)
		}
		for msg := range batch.Messages() {
			key, entry, perr := parseDirectGetFallbackEntry(msg, bucket, pre)
			if perr != nil {
				return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, perr)
			}
			if entry == nil {
				// A NATS-level tombstone. It may be retracting an entry a
				// previous round already collected as live, so deleting is the
				// only correct handling — skipping would return a hard-deleted
				// key as present.
				delete(entries, key)
				continue
			}
			entries[key] = entry
		}
		// jetstream's Fetch reports no error on a batch that falls short of
		// the requested count (FetchMaxWait expiry maps to
		// ErrNoMessages/ErrTimeout, both explicitly excluded from
		// batch.Error()), which is why a short round is never read as
		// completion here — only the server's own pending count is.
		if err := batch.Error(); err != nil {
			return nil, fmt.Errorf("substrate: KV get-multi %s: fallback drain: %w", bucket, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, err)
		}
		if hook := kvDrainRoundHook(ctx); hook != nil {
			hook(round)
		}
	}
	return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, errDirectGetFallbackNeverDrained)
}

// parseDirectGetFallbackEntry mirrors parseDirectGetEntry for the fallback
// drain's jetstream.Msg shape (Fetch delivers jetstream.Msg, not *nats.Msg).
//
// It reports the resolved key for EVERY message, tombstone or not, and a nil
// entry for a NATS-level tombstone. The key has to survive the marker case
// because the drain accumulates across rounds: naming the dead subject is what
// lets the caller retract an entry an earlier round collected.
func parseDirectGetFallbackEntry(msg jetstream.Msg, bucket, pre string) (key string, entry *KVEntry, err error) {
	key = strings.TrimPrefix(msg.Subject(), pre)
	hdr := msg.Headers()
	if op := hdr.Get(KVOperationHeader); op == KVOperationDelete || op == KVOperationPurge {
		return key, nil, nil
	}
	if hdr.Get(MarkerReasonHeader) != "" {
		return key, nil, nil
	}
	meta, err := msg.Metadata()
	if err != nil {
		return key, nil, fmt.Errorf("fallback message metadata: %w", err)
	}
	return key, &KVEntry{
		Bucket:    bucket,
		Key:       key,
		Value:     msg.Data(),
		Revision:  meta.Sequence.Stream,
		Timestamp: meta.Timestamp,
	}, nil
}

// directGetFetchWait bounds one fetch round. jetstream's Fetch takes no
// context in the pinned client, so the caller's deadline cannot interrupt a
// round once it starts — only the wait it was given can. A round whose pending
// count included messages erased before the pull filled blocks for the whole
// wait, and the drain multiplies that by its round budget, so a caller with a
// deadline must have it honored inside the round and not merely between
// rounds. With no deadline the standing wait applies.
func directGetFetchWait(ctx context.Context) (time.Duration, error) {
	wait := directGetFallbackFetchWait
	deadline, ok := ctx.Deadline()
	if !ok {
		return wait, nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, context.DeadlineExceeded
	}
	if remaining < wait {
		return remaining, nil
	}
	return wait, nil
}
