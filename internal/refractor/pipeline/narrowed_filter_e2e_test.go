package pipeline_test

// D1 (refractor-footprint-reduction-design.md): end-to-end proof, over a real
// embedded NATS server, that a narrowed Core KV consumer actually narrows
// server-side delivery (not just the client-side plainReactsTo/
// plainVertexRelevant gates), that a MATCH hot-reload's referenced-label
// change survives a Rebuild instead of riding a stale filter forward, and
// that a registration failure on the narrowed filter falls back to the broad
// one with a health signal recorded rather than leaving the lens dark.
//
// The delivery proofs read the supervised consumer's own JetStream counters
// (NumPending, Delivered.Consumer) rather than sleeping: NumPending is
// "messages that match the consumer's filter, but have not been delivered
// yet" and Delivered.Consumer is a per-consumer sequence that only advances
// when the SERVER actually delivers a message — a foreign-type write the
// filter excludes never gets a consumer sequence at all, so a settled
// (NumPending == 0) narrowed consumer's Delivered.Consumer count is an exact,
// race-free tally of how many (and which) writes it was ever handed.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// narrowedID pads base into a valid Contract #1 20-char NanoID (limited
// alphabet, no I/O/l/0), keeping these fixtures visually distinct from other
// suites sharing the embedded NATS alphabet rules.
func narrowedID(t *testing.T, base string) string {
	t.Helper()
	id := "Nrw" + base
	require.LessOrEqual(t, len(id), 20, "base %q too long for a 20-char NanoID", base)
	for len(id) < 20 {
		id += "9"
	}
	require.Len(t, id, 20)
	return id
}

// waitConsumerSettled polls the named durable's ConsumerInfo until
// NumPending reaches 0 (every message the filter currently admits has been
// delivered) and returns the settled info — the race-free "nothing left to
// deliver" checkpoint the Delivered.Consumer assertions below are read at.
func waitConsumerSettled(t *testing.T, env *pipelineEnv, name string) *jetstream.ConsumerInfo {
	t.Helper()
	var info *jetstream.ConsumerInfo
	pollUntil(t, 5*time.Second, func() bool {
		cons, err := env.js.Consumer(context.Background(), subjects.CoreKVStream(coreKVBucket), name)
		if err != nil {
			return false
		}
		i, err := cons.Info(context.Background())
		if err != nil {
			return false
		}
		if i.NumPending != 0 {
			return false
		}
		info = i
		return true
	})
	return info
}

func putBookOrAuthor(t *testing.T, kv *substrate.KV, key, label, id string) {
	t.Helper()
	body := map[string]any{
		"key": key, "class": label, "isDeleted": false,
		"createdAt": "2026-08-01T00:00:00Z", "lastModifiedAt": "2026-08-01T00:00:00Z",
		"title": label + "-" + id,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// TestNarrowedFilter_DeliversOwnTypeNeverForeignType proves both halves of
// D1's delivery contract for a plain, single-label, full-engine lens: the
// DeliverLastPerSubject initial snapshot over a narrowed filter replays only
// the lens's own corpus (never a pre-existing foreign type), and ongoing
// writes of a foreign type are never delivered either.
func TestNarrowedFilter_DeliversOwnTypeNeverForeignType(t *testing.T) {
	env := startPipelineEnv(t)

	// Pre-seed 3 "author" vertices (a type the lens's query never references)
	// and 2 "book" vertices BEFORE the pipeline starts — the DeliverLastPerSubject
	// initial-snapshot scenario.
	authorKey := func(id string) string { return "vtx.author." + narrowedID(t, id) }
	bookKey := func(id string) string { return "vtx.book." + narrowedID(t, id) }
	for _, id := range []string{"Auth1", "Auth2", "Auth3"} {
		putBookOrAuthor(t, env.coreKV, authorKey(id), "author", id)
	}
	for _, id := range []string{"Book1", "Book2"} {
		putBookOrAuthor(t, env.coreKV, bookKey(id), "book", id)
	}

	eng, cr := compileFullRule(t, "MATCH (b:book) RETURN b.key AS key, b.title AS title", []string{"key"})
	targetKV, adpt := newTargetKV(t, env, "narrowed-book-target", []string{"key"})

	const ruleID = "narrowed-book-lens"
	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)

	filterSubjects, filterSubject := p.ConsumerFilter()
	require.Empty(t, filterSubject, "a single exhaustive label must narrow, not fall back to broad")
	require.Len(t, filterSubjects, 3, "one label expands to 3 forms (vertex, link-source, link-target)")

	spec := specFor(ruleID)
	spec.FilterSubject = filterSubject
	spec.FilterSubjects = filterSubjects
	p.RunOn(env.conn, spec)

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })

	// Both pre-existing books must project; neither pre-existing author ever
	// does, even after the consumer has fully drained its initial backlog.
	pollUntil(t, 5*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), bookKey("Book1"))
		return err == nil
	})
	pollUntil(t, 5*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), bookKey("Book2"))
		return err == nil
	})
	info := waitConsumerSettled(t, env, "refractor-"+ruleID)
	require.EqualValues(t, 2, info.Delivered.Consumer,
		"a settled narrowed consumer must have been delivered exactly the 2 pre-existing books — the 3 pre-existing authors must never have counted")

	for _, id := range []string{"Auth1", "Auth2", "Auth3"} {
		_, err := targetKV.Get(context.Background(), authorKey(id))
		require.ErrorIs(t, err, substrate.ErrKeyNotFound)
	}

	// Ongoing proof: a NEW author write (published before a new book write)
	// must never be delivered either — proven by the exact delivered count
	// once the new book has landed and the consumer has re-settled.
	putBookOrAuthor(t, env.coreKV, authorKey("Auth4"), "author", "Auth4")
	putBookOrAuthor(t, env.coreKV, bookKey("Book3"), "book", "Book3")
	pollUntil(t, 5*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), bookKey("Book3"))
		return err == nil
	})
	info = waitConsumerSettled(t, env, "refractor-"+ruleID)
	require.EqualValues(t, 3, info.Delivered.Consumer,
		"the 4th author write must never have been delivered — only the 3rd book counts")
	_, err = targetKV.Get(context.Background(), authorKey("Auth4"))
	require.ErrorIs(t, err, substrate.ErrKeyNotFound)
}

// TestNarrowedFilter_RebuildRecomputesLabelSet proves the D1 §D1/§5 hazard a
// stale filter creates: a MATCH hot-reload (cmd/refractor/reload.go's
// lens.MatchChange path calls UseFullEngineBranches then Pipeline.Rebuild)
// that changes the referenced-label set must have the REBUILT consumer's
// filter reflect the NEW rule, not the filter captured at activation — a
// JetStream filter update never resets the cursor (nats-server v2.14.0), so
// "recompute, then Reset" is the only correct sequence.
func TestNarrowedFilter_RebuildRecomputesLabelSet(t *testing.T) {
	env := startPipelineEnv(t)

	engA, crA := compileFullRule(t, "MATCH (b:book) RETURN b.key AS key, b.title AS title", []string{"key"})
	targetKV, adpt := newTargetKV(t, env, "narrowed-rebuild-target", []string{"key"})

	const ruleID = "narrowed-rebuild-lens"
	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(engA, crA)

	filterSubjects, filterSubject := p.ConsumerFilter()
	require.Empty(t, filterSubject)
	spec := specFor(ruleID)
	spec.FilterSubject = filterSubject
	spec.FilterSubjects = filterSubjects
	p.RunOn(env.conn, spec)

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })

	// Settle the fresh (empty) consumer before touching anything, so the
	// widget write below is unambiguously a POST-activation, PRE-rebuild
	// event.
	waitConsumerSettled(t, env, "refractor-"+ruleID)

	widgetKey := "vtx.widget." + narrowedID(t, "Widg1")
	putBookOrAuthor(t, env.coreKV, widgetKey, "widget", "Widg1")
	info := waitConsumerSettled(t, env, "refractor-"+ruleID)
	require.EqualValuesf(t, 0, info.Delivered.Consumer,
		"before the rebuild, the book-only filter must never deliver a widget event")

	// A MATCH hot-reload that swaps the referenced label entirely (book ->
	// widget) — mirrors reload.go's lens.MatchChange path: UseFullEngine
	// installs the new compiled rule, then Rebuild resets the durable.
	engB, crB := compileFullRule(t, "MATCH (w:widget) RETURN w.key AS key, w.title AS title", []string{"key"})
	p.UseFullEngine(engB, crB)
	require.NoError(t, p.Rebuild(context.Background(), false))

	// The rebuilt consumer's own JetStream config must show the recomputed
	// (widget) filter, not the stale (book) one.
	pollUntil(t, 5*time.Second, func() bool {
		cons, err := env.js.Consumer(context.Background(), subjects.CoreKVStream(coreKVBucket), "refractor-"+ruleID)
		if err != nil {
			return false
		}
		i, err := cons.Info(context.Background())
		if err != nil {
			return false
		}
		return len(i.Config.FilterSubjects) == 3 && i.Config.FilterSubjects[0] != "" &&
			containsFilter(i.Config.FilterSubjects, "$KV."+coreKVBucket+".vtx.widget.>")
	})

	// A widget write after the rebuild must now project — proving the
	// rebuilt consumer's filter actually admits it, not just that its Config
	// looks right.
	widgetKey2 := "vtx.widget." + narrowedID(t, "Widg2")
	putBookOrAuthor(t, env.coreKV, widgetKey2, "widget", "Widg2")
	pollUntil(t, 5*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), widgetKey2)
		return err == nil
	})

	// And a book write must no longer project — the OLD label was dropped,
	// not merely unioned in.
	bookKey := "vtx.book." + narrowedID(t, "BookX")
	putBookOrAuthor(t, env.coreKV, bookKey, "book", "BookX")
	waitConsumerSettled(t, env, "refractor-"+ruleID)
	_, err = targetKV.Get(context.Background(), bookKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "the rebuilt lens no longer references book at all")
}

func containsFilter(filters []string, want string) bool {
	for _, s := range filters {
		if s == want {
			return true
		}
	}
	return false
}

// TestNarrowedFilter_RegistrationFailureFallsBackToBroadWithHealthSignal
// proves the D1 fallback contract end-to-end against a REAL nats-server
// rejection: a deliberately overlapping FilterSubjects pair (one subject a
// subset of the other — nats-server v2.14.0's consumer-creation overlap
// check, server/consumer.go:876-886) is injected directly (bypassing
// ConsumerFilter's own provably-safe derivation, to prove the fallback
// recovers from ANY narrowed-filter registration error, not just ones this
// package could itself produce). The lens must still come up — on the broad
// filter — and the failure must be recorded on its own health entry via the
// existing RecordError/errorCount surface (docs/observability/
// health-kv-schema.md's per-lens reporter-status entry), never leaving it
// dark.
func TestNarrowedFilter_RegistrationFailureFallsBackToBroadWithHealthSignal(t *testing.T) {
	env := startPipelineEnv(t)

	eng, cr := compileFullRule(t, "MATCH (b:book) RETURN b.key AS key, b.title AS title", []string{"key"})
	targetKV, adpt := newTargetKV(t, env, "narrowed-fallback-target", []string{"key"})

	const ruleID = "narrowed-fallback-lens"
	reporter := newHealthReporter(t, env, ruleID)
	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, reporter)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)

	spec := specFor(ruleID)
	spec.FilterSubject = ""
	// $KV.CORE.vtx.book.> is a SUBSET of $KV.CORE.> — nats-server rejects
	// this pair as overlapping (JSConsumerOverlappingSubjectFiltersError).
	spec.FilterSubjects = []string{
		subjects.CoreKVFilter(coreKVBucket),
		subjects.CoreKVVertexFilter(coreKVBucket, "book"),
	}
	p.RunOn(env.conn, spec)

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })

	// The lens must come up anyway — on the broad fallback filter — proven by
	// a normal write actually projecting.
	bookKey := "vtx.book." + narrowedID(t, "FbBook1")
	putBookOrAuthor(t, env.coreKV, bookKey, "book", "FbBook1")
	pollUntil(t, 5*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), bookKey)
		return err == nil
	})

	cons, err := env.js.Consumer(context.Background(), subjects.CoreKVStream(coreKVBucket), "refractor-"+ruleID)
	require.NoError(t, err)
	info, err := cons.Info(context.Background())
	require.NoError(t, err)
	require.Empty(t, info.Config.FilterSubjects, "fallback must land on the single broad filter, not the rejected FilterSubjects set")
	require.Equal(t, subjects.CoreKVFilter(coreKVBucket), info.Config.FilterSubject)

	// The failure must be recorded on the lens's OWN health entry — the
	// existing errorCount/lastError surface, not a new mechanism.
	pollUntil(t, 5*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.ErrorCount > 0 && entry.LastError != nil
	})
	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, "active", entry.Status, "the fallback recovery must leave the lens active, not paused")
	require.NotNil(t, entry.LastError)
	require.Contains(t, *entry.LastError, "narrowed")
}
