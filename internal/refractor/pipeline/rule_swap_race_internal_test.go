package pipeline

// A MATCH hot-reload (cmd/refractor/reload.go) calls UseFullEngineBranches on a
// LIVE pipeline from CoreKVSource's dispatch goroutine while the consumer
// goroutine is inside handle reading the same fields. These tests pin the two
// properties ruleMu buys — no data race, and an ATOMIC swap — against a writer
// and readers running concurrently. Run under -race, which is where the
// unsynchronized version fails.

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The two specs a reload alternates between. They differ in BOTH narrowing
// dimensions at once — label set and relation set — which is what lets the
// coherence assertion below catch a torn read: any observed pairing of one
// spec's labels with the other's relations is a rule no MATCH ever declared.
const (
	raceSwapSpecA = `
MATCH (u:unit)-[:managedBy]->(o:owner)
RETURN u.key AS key, o.name AS ownerName
`
	raceSwapSpecB = `
MATCH (b:booking)-[:bookedBy]->(g:guest)
RETURN b.key AS key, g.name AS guestName
`
)

// TestRuleSwap_ConcurrentHotReload_NoRace drives UseFullEngineBranches against
// concurrent readers of every gate that consults the compiled rule. Its value
// is the -race verdict; it asserts only that the readers keep returning
// well-formed answers throughout.
func TestRuleSwap_ConcurrentHotReload_NoRace(t *testing.T) {
	eng := full.New()
	crA, err := eng.Parse(raceSwapSpecA)
	require.NoError(t, err)
	crB, err := eng.Parse(raceSwapSpecB)
	require.NoError(t, err)

	p, err := New("rule-swap-race", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, crA)

	const rounds = 500
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			cr := crA
			if i%2 == 1 {
				cr = crB
			}
			p.UseFullEngineBranches(eng, cr, nil)
		}
	}()

	// One reader per gate the consumer goroutine actually reaches, so the race
	// detector sees every read path, not just the cheapest one.
	readers := []func(){
		func() { p.ConsumerFilter() },
		func() { p.NarrowedFilterEligible() },
		func() { p.ActorAwareNarrowingLabels() },
		func() { p.ruleState().plainReactsTo("unit") },
		func() { p.ruleState().plainLinkReactsTo("managedBy") },
		func() { p.ruleState().plainVertexRelevant("booking") },
		func() { p.seedAnchorFor(p.ruleState(), "unit", "vtx.unit.RACEswapAAAAAAAAAAAA") },
	}
	for _, read := range readers {
		wg.Add(1)
		go func(read func()) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				read()
			}
		}(read)
	}
	wg.Wait()

	// The writer's last swap wins and is fully published.
	subjects, broad, _ := p.ConsumerFilter()
	require.Empty(t, broad, "a two-label narrowed lens must not fall back to the broad filter")
	require.NotEmpty(t, subjects)
}

// TestRuleSwap_ObservedRuleIsNeverTorn is the assertion the race detector alone
// cannot make: every snapshot a reader takes must be a rule that ACTUALLY
// EXISTS. Before ruleMu the label set, the relation set and their two
// exhaustiveness flags were four independent stores, so a reader could see spec
// A's labels against spec B's relations — or, worse for the relation pair,
// relationsExhaustive already true against a nil map, which reads as "this lens
// traverses no relation at all" and skips every link event.
func TestRuleSwap_ObservedRuleIsNeverTorn(t *testing.T) {
	eng := full.New()
	crA, err := eng.Parse(raceSwapSpecA)
	require.NoError(t, err)
	crB, err := eng.Parse(raceSwapSpecB)
	require.NoError(t, err)

	p, err := New("rule-swap-coherence", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, crA)

	// What each spec's snapshot must look like, whole. A snapshot matching
	// neither entry is torn.
	// Both specs are exhaustive, so neither publishes a narrowing-blocked
	// reason. Carrying it in the shape anyway is what makes the net cover the
	// field rather than the two values it happens to take here: a reason left
	// over from another rule is exactly the half-published state this test
	// exists to catch, and it would otherwise be invisible.
	want := []ruleShape{
		{labels: []string{"owner", "unit"}, relations: []string{"managedBy"}},
		{labels: []string{"booking", "guest"}, relations: []string{"bookedBy"}},
	}

	const rounds = 2000
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			cr := crA
			if i%2 == 1 {
				cr = crB
			}
			p.UseFullEngineBranches(eng, cr, nil)
		}
	}()

	var torn []ruleShape
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			rs := p.ruleState()
			// Both narrowings are exhaustive for both specs, so a reader that
			// sees otherwise has caught a half-published rule.
			if rs.reprojectAll || !rs.relationsExhaustive {
				torn = append(torn, ruleShape{})
				continue
			}
			got := ruleShape{
				labels:    sortedRuleKeys(rs.reprojectLabels),
				relations: sortedRuleKeys(rs.reprojectRelations),
				blocked:   rs.narrowingBlocked,
			}
			if !got.matchesAny(want) {
				torn = append(torn, got)
			}
		}
	}()
	wg.Wait()

	require.Empty(t, torn, "every snapshot must be one whole compiled rule, never a mix of two")
}

// ruleShape is the observable narrowing a snapshot carries — the label set, the
// relation set, and the reason the labels are not exhaustive, all of which must
// come from one compiled rule.
type ruleShape struct {
	labels    []string
	relations []string
	blocked   string
}

func (s ruleShape) matchesAny(want []ruleShape) bool {
	for _, w := range want {
		if slices.Equal(s.labels, w.labels) && slices.Equal(s.relations, w.relations) && s.blocked == w.blocked {
			return true
		}
	}
	return false
}

func sortedRuleKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// countingAdapter records every write it is asked to perform, so a test can
// assert that a write did NOT happen.
type countingAdapter struct{ writes int }

func (a *countingAdapter) Upsert(_ context.Context, _ map[string]any, _ map[string]any, _ uint64) error {
	a.writes++
	return nil
}

func (a *countingAdapter) Delete(_ context.Context, _ map[string]any, _ uint64) error {
	a.writes++
	return nil
}
func (a *countingAdapter) Probe(context.Context) error { return nil }
func (a *countingAdapter) Close() error                { return nil }

// TestWriteResults_SupersededRuleIsNakedNotWritten pins that results derived
// under a rule a hot-reload has since replaced are refused rather than written.
//
// This is not a cosmetic freshness preference. Results carry THIS message's
// stream sequence, and the rebuild a MATCH reload triggers replays the same
// messages with the same sequences — which the guarded adapter drops as an
// idempotent no-op (storedSeq >= incomingSeq). So a stale row written after the
// rebuild's truncate swallows its own correction, and on the auth plane that
// row is the pre-edit permission set: a MATCH edit made to revoke something
// would be silently defeated. Naking hands the message back for re-evaluation
// under the rule actually in force.
func TestWriteResults_SupersededRuleIsNakedNotWritten(t *testing.T) {
	eng := full.New()
	crA, err := eng.Parse(raceSwapSpecA)
	require.NoError(t, err)
	crB, err := eng.Parse(raceSwapSpecB)
	require.NoError(t, err)

	ad := &countingAdapter{}
	p, err := New("superseded-rule", "nats_kv", "CORE", nil, nil, ad, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, crA)

	ctx := context.Background()
	msg := substrate.Message{Subject: "$KV.CORE.vtx.unit.x", Body: []byte(`{"id":"x"}`), Sequence: 42}
	results := []ruleengine.EvalResult{{Keys: map[string]any{"k": "a"}, Row: map[string]any{"k": "a"}}}

	// The snapshot an in-flight event would have taken at handle entry.
	rs := p.ruleState()

	// A MATCH hot-reload lands while that event is still evaluating.
	p.UseFullEngineBranches(eng, crB, nil)

	dec, werr := p.writeResults(ctx, rs, msg, "vtx.unit.x", results, nil)
	require.NoError(t, werr)
	require.Equal(t, substrate.Nak, dec, "a superseded evaluation must be handed back, not written")
	require.Zero(t, ad.writes, "no row derived from a replaced rule may reach the target")

	// The redelivery — snapshot taken after the swap — writes normally.
	dec, werr = p.writeResults(ctx, p.ruleState(), msg, "vtx.unit.x", results, nil)
	require.NoError(t, werr)
	require.Equal(t, substrate.Ack, dec)
	require.Equal(t, 1, ad.writes, "the re-evaluation under the current rule writes")
}
