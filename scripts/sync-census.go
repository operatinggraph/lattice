//go:build ignore

// sync-census.go — personal-lens-delta-publication-design.md §10 "T7" live
// acceptance and §11 row 3 (Inc 3), reproducing the fire's two scratchpad
// probes (per-subject composition and stream-wide attribution) as one
// repo tool so the T7 numbers are re-runnable by anyone.
//
// Stream-wide mode (default, no -subject): SYNC and REFRACTOR_AUDIT stream
// composition (first-sequence age, messages, bytes, whether the byte cap
// binds), per-lens bytes normalised to a 12h window, and the stream-wide
// whole-actor/live/hydrate attribution by (actor, revision) group.
//
// Per-subject mode (-subject lattice.sync.user.<actor>): one actor's SYNC
// subject bucketed by op/lens/revision, plus the fraction of distinct
// revisions carrying <=2 messages and upserts normalised per 12h.
//
// Read-only throughout: an ordered consumer with DeliverAll, never a
// durable consumer, never a write.
//
// Run via: make sync-census (== go run ./scripts/sync-census.go), reading
// NATS_URL / NATS_NKEY from the environment.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const syncStream = "SYNC"

var auditStream = health.AuditStreamName

// envelope is the subset of a SYNC message's JSON body every census
// needs. Keys is populated on a "keyset" (frame) message only.
type envelope struct {
	Op       string   `json:"op"`
	Lens     string   `json:"lens"`
	Revision uint64   `json:"revision"`
	Keys     []string `json:"keys"`
}

func main() {
	subject := flag.String("subject", "", "SYNC subject to census in per-subject mode (e.g. lattice.sync.user.<actor>); default is stream-wide mode")
	timeout := flag.Duration("timeout", 15*time.Minute, "overall census timeout")
	since := flag.Duration("since", 0, "census only messages published within this window before now (0 = the whole stream); an interim read after a deploy uses it to exclude the older mechanism's messages")
	flag.Parse()
	deliver := jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverAllPolicy}
	if *since > 0 {
		start := time.Now().Add(-*since)
		deliver = jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverByStartTimePolicy, OptStartTime: &start}
	}

	natsURL := envOrDefault("NATS_URL", "nats://localhost:4222")

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	conn, err := output.Connect(ctx, natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: connect to NATS: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	js := conn.JetStream()
	resolver := newLensResolver(conn)

	if *subject != "" {
		err = runSubjectCensus(ctx, js, resolver, *subject, deliver)
	} else {
		err = runStreamCensus(ctx, js, resolver, deliver)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// lensResolver best-effort resolves a SYNC envelope's "lens" field (a
// vtx.meta.<NanoID> id or a bare NanoID) to "name (id)" by reading the
// meta-vertex's .canonicalName aspect from core-kv. Any read or parse
// failure falls back to the bare id — this is a reporting aid, not a
// correctness dependency of the census.
type lensResolver struct {
	conn  *substrate.Conn
	cache map[string]string
}

func newLensResolver(conn *substrate.Conn) *lensResolver {
	return &lensResolver{conn: conn, cache: map[string]string{}}
}

func (r *lensResolver) resolve(ctx context.Context, lens string) string {
	if lens == "" {
		return lens
	}
	id := strings.TrimPrefix(lens, "vtx.meta.")
	if name, ok := r.cache[id]; ok {
		if name == "" {
			return id
		}
		return fmt.Sprintf("%s (%s)", name, id)
	}
	name := r.lookupCanonicalName(ctx, id)
	r.cache[id] = name
	if name == "" {
		return id
	}
	return fmt.Sprintf("%s (%s)", name, id)
}

func (r *lensResolver) lookupCanonicalName(ctx context.Context, id string) string {
	entry, err := r.conn.KVGet(ctx, bootstrap.CoreKVBucket, "vtx.meta."+id+".canonicalName")
	if err != nil {
		return ""
	}
	var env struct {
		IsDeleted bool           `json:"isDeleted"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &env); err != nil || env.IsDeleted {
		return ""
	}
	return dataString(env.Data, "value", "name", "canonicalName")
}

func dataString(d map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := d[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// runSubjectCensus buckets one actor's SYNC subject by op/lens/revision
// (design §3 "C3") and prints T7's per-revision-message-count and
// normalised-upserts numbers alongside it.
func runSubjectCensus(ctx context.Context, js jetstream.JetStream, resolver *lensResolver, subject string, deliver jetstream.OrderedConsumerConfig) error {
	deliver.FilterSubjects = []string{subject}
	cons, err := js.OrderedConsumer(ctx, syncStream, deliver)
	if err != nil {
		return fmt.Errorf("open ordered consumer on %s for %s: %w", syncStream, subject, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return fmt.Errorf("consumer info: %w", err)
	}
	pending := info.NumPending
	fmt.Printf("subject=%s pending=%d\n", subject, pending)
	if pending == 0 {
		return nil
	}

	ops := map[string]int{}
	lensMsgs := map[string]int{}
	lensFrames := map[string]int{}
	revMsgs := map[uint64]int{}
	revLens := map[uint64]map[string]struct{}{}
	frameSizes := map[string][]int{}
	var first, last time.Time
	var totalBytes, upserts int
	n := 0

	msgs, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("open message iterator: %w", err)
	}
	defer msgs.Stop()

	for uint64(n) < pending {
		m, err := msgs.Next()
		if err != nil {
			return fmt.Errorf("read message %d/%d: %w", n, pending, err)
		}
		n++
		md, _ := m.Metadata()
		if md != nil {
			if first.IsZero() {
				first = md.Timestamp
			}
			last = md.Timestamp
		}
		totalBytes += len(m.Data())
		var e envelope
		if err := json.Unmarshal(m.Data(), &e); err != nil {
			ops["<unparsed>"]++
			continue
		}
		ops[e.Op]++
		if e.Op == "upsert" {
			upserts++
		}
		lensMsgs[e.Lens]++
		if e.Op == "keyset" {
			lensFrames[e.Lens]++
			frameSizes[e.Lens] = append(frameSizes[e.Lens], len(e.Keys))
		}
		revMsgs[e.Revision]++
		if revLens[e.Revision] == nil {
			revLens[e.Revision] = map[string]struct{}{}
		}
		revLens[e.Revision][e.Lens] = struct{}{}
	}

	span := last.Sub(first)
	fmt.Printf("read=%d bytes=%d span=%s (%s -> %s)\n", n, totalBytes, span.Round(time.Second), first.Format(time.RFC3339), last.Format(time.RFC3339))
	fmt.Println("ops:", ops)

	type lensCount struct {
		lens string
		n    int
	}
	var ls []lensCount
	for k, v := range lensMsgs {
		ls = append(ls, lensCount{k, v})
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].n > ls[j].n })
	for _, e := range ls {
		sizes := frameSizes[e.lens]
		maxF := 0
		for _, s := range sizes {
			if s > maxF {
				maxF = s
			}
		}
		fmt.Printf("lens %s msgs=%d frames=%d maxFrameKeys=%d\n", resolver.resolve(ctx, e.lens), e.n, lensFrames[e.lens], maxF)
	}

	// Distinct revisions = distinct (lens-pipeline) events/publications in
	// the window.
	var revs []uint64
	for r := range revMsgs {
		revs = append(revs, r)
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i] < revs[j] })
	fmt.Printf("distinct revisions=%d\n", len(revs))

	hist := map[string]int{}
	atMost2 := 0
	for _, r := range revs {
		c := revMsgs[r]
		if c <= 2 {
			atMost2++
		}
		switch {
		case c <= 2:
			hist["1-2"]++
		case c <= 10:
			hist["3-10"]++
		case c <= 100:
			hist["11-100"]++
		case c <= 1000:
			hist["101-1000"]++
		default:
			hist[">1000"]++
		}
	}
	fmt.Println("msgs-per-revision histogram:", hist)

	// Top 8 revisions by message count.
	sort.Slice(revs, func(i, j int) bool { return revMsgs[revs[i]] > revMsgs[revs[j]] })
	for i := 0; i < len(revs) && i < 8; i++ {
		r := revs[i]
		var lenses []string
		for l := range revLens[r] {
			lenses = append(lenses, resolver.resolve(ctx, l))
		}
		fmt.Printf("rev %d msgs=%d lenses=%v\n", r, revMsgs[r], lenses)
	}

	fmt.Println()
	fmt.Println("T7 acceptance numbers (personal-lens-delta-publication-design.md §10 T7):")
	frac := 0.0
	if len(revs) > 0 {
		frac = float64(atMost2) / float64(len(revs)) * 100
	}
	fmt.Printf("  revisions carrying <=2 msgs: %d/%d (%.1f%%)  [T7 wants >=95%%]\n", atMost2, len(revs), frac)
	// A whole-actor pass (a sweep, a drain, a hydrate) publishes one frame per lens at
	// one revision, so it can never carry <=2 messages; the per-event bound T7 states
	// is a property of the CDC path alone, measured over the revisions with fewer
	// than ten lenses.
	liveRevs, liveAtMost2 := 0, 0
	for _, r := range revs {
		if len(revLens[r]) >= 10 {
			continue
		}
		liveRevs++
		if revMsgs[r] <= 2 {
			liveAtMost2++
		}
	}
	liveFrac := 0.0
	if liveRevs > 0 {
		liveFrac = float64(liveAtMost2) / float64(liveRevs) * 100
	}
	fmt.Printf("  live (CDC, <10 lenses) revisions carrying <=2 msgs: %d/%d (%.1f%%); whole-actor passes: %d\n", liveAtMost2, liveRevs, liveFrac, len(revs)-liveRevs)
	upsertsPer12h := 0.0
	if span > 0 {
		upsertsPer12h = float64(upserts) * float64(12*time.Hour) / float64(span)
	}
	fmt.Printf("  upserts=%d over span=%s, normalised to /12h = %.1f  [T7 wants <= the subject's row count, on a quiet actor]\n", upserts, span.Round(time.Second), upsertsPer12h)

	return nil
}

type groupKey struct {
	actor string
	rev   uint64
}

type group struct {
	msgs, bytes int
	upserts     int
	frames      int
	lenses      map[string]struct{}
	hydrate     bool
}

type classAgg struct{ groups, msgs, bytes, upserts, frames int }

// runStreamCensus attributes SYNC's messages and bytes stream-wide to the
// path that wrote them, keyed on (actor, revision) (design §3 "C4"), then
// prints the T7 stream-composition numbers: SYNC/REFRACTOR_AUDIT
// first-sequence age and whether SYNC's byte cap binds, and per-lens bytes
// normalised to a 12h window.
func runStreamCensus(ctx context.Context, js jetstream.JetStream, resolver *lensResolver, deliver jetstream.OrderedConsumerConfig) error {
	cons, err := js.OrderedConsumer(ctx, syncStream, deliver)
	if err != nil {
		return fmt.Errorf("open ordered consumer on %s: %w", syncStream, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return fmt.Errorf("consumer info: %w", err)
	}
	pending := info.NumPending
	fmt.Printf("pending=%d\n", pending)

	groups := map[groupKey]*group{}
	lensBytes := map[string]int{}
	lensMsgs := map[string]int{}
	var first, last time.Time

	msgs, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("open message iterator: %w", err)
	}
	defer msgs.Stop()

	n := 0
	for uint64(n) < pending {
		m, err := msgs.Next()
		if err != nil {
			return fmt.Errorf("read message %d/%d: %w", n, pending, err)
		}
		n++
		md, _ := m.Metadata()
		if md != nil {
			if first.IsZero() {
				first = md.Timestamp
			}
			last = md.Timestamp
		}
		actor := m.Subject()[strings.LastIndex(m.Subject(), ".")+1:]
		var e envelope
		_ = json.Unmarshal(m.Data(), &e)
		k := groupKey{actor, e.Revision}
		g := groups[k]
		if g == nil {
			g = &group{lenses: map[string]struct{}{}}
			groups[k] = g
		}
		g.msgs++
		g.bytes += len(m.Data())
		switch e.Op {
		case "upsert":
			g.upserts++
		case "keyset":
			g.frames++
		case "hydrationComplete":
			g.hydrate = true
		}
		if e.Lens != "" {
			g.lenses[e.Lens] = struct{}{}
			lensBytes[e.Lens] += len(m.Data())
			lensMsgs[e.Lens]++
		}
	}

	span := last.Sub(first)
	fmt.Printf("read=%d span=%s (%s -> %s)\n", n, span.Round(time.Minute), first.Format(time.RFC3339), last.Format(time.RFC3339))

	cls := map[string]*classAgg{}
	actorsWhole := map[string]int{}
	for k, g := range groups {
		c := "live"
		switch {
		case g.hydrate:
			c = "hydrate"
		case len(g.lenses) >= 10:
			c = "whole-actor"
			actorsWhole[k.actor]++
		}
		a := cls[c]
		if a == nil {
			a = &classAgg{}
			cls[c] = a
		}
		a.groups++
		a.msgs += g.msgs
		a.bytes += g.bytes
		a.upserts += g.upserts
		a.frames += g.frames
	}
	for _, c := range []string{"whole-actor", "live", "hydrate"} {
		a := cls[c]
		if a == nil {
			continue
		}
		fmt.Printf("%-12s groups=%d msgs=%d bytes=%d upserts=%d frames=%d\n", c, a.groups, a.msgs, a.bytes, a.upserts, a.frames)
	}

	var passes []int
	for _, v := range actorsWhole {
		passes = append(passes, v)
	}
	sort.Ints(passes)
	if len(passes) > 0 {
		fmt.Printf("whole-actor passes per actor: n=%d min=%d median=%d max=%d\n", len(passes), passes[0], passes[len(passes)/2], passes[len(passes)-1])
	}

	type lensRow struct {
		lens string
		b, m int
	}
	var ls []lensRow
	for k, b := range lensBytes {
		ls = append(ls, lensRow{k, b, lensMsgs[k]})
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].b > ls[j].b })

	fmt.Println()
	fmt.Println("per-lens bytes, normalised to a 12h window:")
	for _, e := range ls {
		normalised := 0.0
		if span > 0 {
			normalised = float64(e.b) * float64(12*time.Hour) / float64(span)
		}
		fmt.Printf("  lens %s bytes=%d msgs=%d bytes/12h=%.0f\n", resolver.resolve(ctx, e.lens), e.b, e.m, normalised)
	}

	fmt.Println()
	fmt.Println("T7 acceptance numbers (personal-lens-delta-publication-design.md §10 T7):")

	syncHandle, err := js.Stream(ctx, syncStream)
	if err != nil {
		return fmt.Errorf("open stream %s: %w", syncStream, err)
	}
	syncInfo, err := syncHandle.Info(ctx)
	if err != nil {
		return fmt.Errorf("%s stream info: %w", syncStream, err)
	}
	syncAge := time.Since(syncInfo.State.FirstTime)
	capBinds := syncInfo.Config.MaxBytes > 0 && int64(syncInfo.State.Bytes) >= syncInfo.Config.MaxBytes
	fmt.Printf("  %s: first=%s age=%s msgs=%d bytes=%d maxBytes=%d capBinds=%v  [T7 wants age>=24h, capBinds=false]\n",
		syncStream, syncInfo.State.FirstTime.Format(time.RFC3339), syncAge.Round(time.Minute), syncInfo.State.Msgs, syncInfo.State.Bytes, syncInfo.Config.MaxBytes, capBinds)

	auditHandle, err := js.Stream(ctx, auditStream)
	if err != nil {
		return fmt.Errorf("open stream %s: %w", auditStream, err)
	}
	auditInfo, err := auditHandle.Info(ctx)
	if err != nil {
		return fmt.Errorf("%s stream info: %w", auditStream, err)
	}
	auditAge := time.Since(auditInfo.State.FirstTime)
	fmt.Printf("  %s: first=%s age=%s  [T7 wants age>=12h]\n", auditStream, auditInfo.State.FirstTime.Format(time.RFC3339), auditAge.Round(time.Minute))

	return nil
}
