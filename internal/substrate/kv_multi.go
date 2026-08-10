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

	// KV tombstone markers: KV-Operation: DEL|PURGE
	// (nats.go/jetstream/kv.go:494-496) or, for a TTL/rollup marker with no
	// KV-Operation header, Nats-Marker-Reason: MaxAge|Purge|Remove
	// (nats-server/server/stream.go:641,687-689).
	directGetKVOpHdr         = "KV-Operation"
	directGetKVOpDelete      = "DEL"
	directGetKVOpPurge       = "PURGE"
	directGetMarkerReasonHdr = "Nats-Marker-Reason"

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
	// directGetFallbackConsumerTTL cleans up an ephemeral fallback consumer
	// server-side even if this process dies mid-drain before its own
	// best-effort delete runs.
	directGetFallbackConsumerTTL = 30 * time.Second
)

var (
	// errDirectGetShortRead is the internal retry-whole signal: the response
	// stream ended before a clean EOB. Never returned to a KVGetMulti caller
	// — directGetMulti retries on it, and only its final, attempts-exhausted
	// wrap ever surfaces.
	errDirectGetShortRead = errors.New("substrate: KV get-multi: short read")
	// errDirectGetTooManyResults is the internal 413 signal that routes
	// KVGetMulti to the stability-verified fallback. Never returned to callers.
	errDirectGetTooManyResults = errors.New("substrate: KV get-multi: too many results")
	// errDirectGetFallbackUnstable means the fallback's double-drain never
	// agreed within directGetFallbackRetries attempts.
	errDirectGetFallbackUnstable = errors.New("substrate: KV get-multi: fallback snapshot did not stabilize")
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
// snapshot, never a torn one). An empty keys returns an empty map, nil
// error.
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
		return c.kvGetMultiFallback(ctx, bucket, subjects, pre)
	}
	return nil, err
}

// directGetMulti runs the fast multi_last path with a bounded retry-whole
// loop absorbing short reads.
func (c *Conn) directGetMulti(ctx context.Context, bucket string, subjects []string, pre string) (map[string]*KVEntry, error) {
	var lastErr error
	for attempt := 1; attempt <= directGetRetries; attempt++ {
		entries, err := c.directGetMultiOnce(ctx, bucket, subjects, pre)
		if err == nil {
			return entries, nil
		}
		if !errors.Is(err, errDirectGetShortRead) {
			return nil, err
		}
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
	return nil, fmt.Errorf("substrate: KV get-multi %s: %d attempts exhausted: %w", bucket, directGetRetries, lastErr)
}

// directGetMultiOnce issues a single multi_last request-response cycle. The
// subscription is created before the request is published (never the
// reverse), so no response can arrive before this process is listening.
//
// When ctx carries no deadline, this attempt is bounded by
// directGetMultiDefaultTimeout — a self-imposed budget, never the caller's
// own cancellation. Losing the race against that synthetic budget (the
// caller's ctx is still live) is reported as errDirectGetShortRead so the
// retry-whole loop absorbs it; the caller's OWN ctx ending is always
// reported and respected as-is, never retried.
func (c *Conn) directGetMultiOnce(ctx context.Context, bucket string, subjects []string, pre string) (map[string]*KVEntry, error) {
	streamName := "KV_" + bucket
	reqSubj := fmt.Sprintf(directGetMultiAPIT, streamName)
	body, err := json.Marshal(directGetMultiRequest{MultiLastFor: subjects})
	if err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: encode request: %w", bucket, err)
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
		return nil, fmt.Errorf("substrate: KV get-multi %s: subscribe: %w", bucket, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	req := nats.NewMsg(reqSubj)
	req.Reply = inbox
	req.Data = body
	if err := c.nc.PublishMsg(req); err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: publish request: %w", bucket, err)
	}

	entries := make(map[string]*KVEntry, len(subjects))
	for {
		select {
		case <-attemptCtx.Done():
			if cerr := ctx.Err(); cerr != nil {
				return nil, fmt.Errorf("substrate: KV get-multi %s: %w", bucket, cerr)
			}
			return nil, fmt.Errorf("substrate: KV get-multi %s: %w: no response within %s", bucket, errDirectGetShortRead, directGetMultiDefaultTimeout)
		case m, ok := <-ch:
			if !ok {
				return nil, fmt.Errorf("substrate: KV get-multi %s: %w: response subscription closed before EOB", bucket, errDirectGetShortRead)
			}
			switch status := m.Header.Get(directGetStatusHdr); status {
			case "":
				entry, isMarker, perr := parseDirectGetEntry(m, bucket, pre)
				if perr != nil {
					return nil, fmt.Errorf("substrate: KV get-multi %s: %w", bucket, perr)
				}
				if !isMarker {
					entries[entry.Key] = entry
				}
			case directGetStatusEOB:
				if pending := m.Header.Get(directGetNumPendingHdr); pending != "0" {
					return nil, fmt.Errorf("substrate: KV get-multi %s: %w: Nats-Num-Pending=%s at EOB", bucket, errDirectGetShortRead, pending)
				}
				return entries, nil
			case directGetStatusNoResults:
				if m.Header.Get(directGetDescrHdr) == directGetDescrNoResults {
					return entries, nil
				}
				// A mid-stream "404 Message Not Found": the matched-subject
				// set was computed, but loading one of its messages then
				// failed (a concurrent delete/purge racing the read).
				return nil, fmt.Errorf("substrate: KV get-multi %s: %w: %s", bucket, errDirectGetShortRead, m.Header.Get(directGetDescrHdr))
			case directGetStatusTooManyResults:
				return nil, errDirectGetTooManyResults
			default:
				return nil, fmt.Errorf("substrate: KV get-multi %s: unexpected response status %s %s", bucket, status, m.Header.Get(directGetDescrHdr))
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
	if op := m.Header.Get(directGetKVOpHdr); op == directGetKVOpDelete || op == directGetKVOpPurge {
		return nil, true, nil
	}
	if m.Header.Get(directGetMarkerReasonHdr) != "" {
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

	info, err := cons.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: fallback consumer info: %w", bucket, err)
	}

	entries := make(map[string]*KVEntry, info.NumPending)
	if info.NumPending == 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, err)
		}
		return entries, nil
	}

	batch, err := cons.Fetch(int(info.NumPending), jetstream.FetchMaxWait(directGetFallbackFetchWait))
	if err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: fallback fetch: %w", bucket, err)
	}
	received := 0
	for msg := range batch.Messages() {
		received++
		entry, isMarker, perr := parseDirectGetFallbackEntry(msg, bucket, pre)
		if perr != nil {
			return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, perr)
		}
		if !isMarker {
			entries[entry.Key] = entry
		}
	}
	if err := batch.Error(); err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: fallback drain: %w", bucket, err)
	}
	// InitialConsumerPending hardening, mirroring the in-repo key-lister
	// guard (kv.go:204-213): jetstream's Fetch reports no error on a batch
	// that falls short of the requested count (FetchMaxWait expiry maps to
	// ErrNoMessages/ErrTimeout, both explicitly excluded from batch.Error())
	// — so received must be checked against the count NumPending promised,
	// not merely against zero. Anything short of that count is a silent
	// partial the caller must never read as "confirmed complete".
	if received != int(info.NumPending) {
		return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: expected %d pending, received %d", bucket, info.NumPending, received)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("substrate: KV get-multi %s: fallback: %w", bucket, err)
	}
	return entries, nil
}

// parseDirectGetFallbackEntry mirrors parseDirectGetEntry for the fallback
// drain's jetstream.Msg shape (Fetch delivers jetstream.Msg, not *nats.Msg).
func parseDirectGetFallbackEntry(msg jetstream.Msg, bucket, pre string) (entry *KVEntry, isMarker bool, err error) {
	hdr := msg.Headers()
	if op := hdr.Get(directGetKVOpHdr); op == directGetKVOpDelete || op == directGetKVOpPurge {
		return nil, true, nil
	}
	if hdr.Get(directGetMarkerReasonHdr) != "" {
		return nil, true, nil
	}
	meta, err := msg.Metadata()
	if err != nil {
		return nil, false, fmt.Errorf("fallback message metadata: %w", err)
	}
	return &KVEntry{
		Bucket:    bucket,
		Key:       strings.TrimPrefix(msg.Subject(), pre),
		Value:     msg.Data(),
		Revision:  meta.Sequence.Stream,
		Timestamp: meta.Timestamp,
	}, false, nil
}
