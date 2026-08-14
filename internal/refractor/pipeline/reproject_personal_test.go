package pipeline

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// personalFrame is one keyset frame a fake personal target observed.
type personalFrame struct {
	actorID  string
	keys     []map[string]any
	revision uint64
}

// fakePersonalTarget is an adapter.Adapter that also publishes keyset frames —
// the KeySetPublisher shape that makes a target a personal one. gate, when
// non-nil, blocks inside PublishKeySet so a test can hold one publisher there
// and observe what a second publisher is or is not allowed to do meanwhile.
type fakePersonalTarget struct {
	mu      sync.Mutex
	frames  []personalFrame
	upserts []map[string]any
	deletes []map[string]any

	entered chan string // signalled with the actorID on entry to PublishKeySet
	release chan struct{}
}

func (f *fakePersonalTarget) Upsert(_ context.Context, keys, _ map[string]any, _ uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, keys)
	return nil
}

func (f *fakePersonalTarget) Delete(_ context.Context, keys map[string]any, _ uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, keys)
	return nil
}

func (f *fakePersonalTarget) Probe(context.Context) error { return nil }
func (f *fakePersonalTarget) Close() error                { return nil }

func (f *fakePersonalTarget) PublishKeySet(_ context.Context, actorID string, keys []map[string]any, revision uint64) error {
	if f.entered != nil {
		f.entered <- actorID
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, personalFrame{actorID: actorID, keys: keys, revision: revision})
	return nil
}

func (f *fakePersonalTarget) PublishHydrationComplete(context.Context, string, uint64) error {
	return nil
}

func (f *fakePersonalTarget) snapshot() []personalFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]personalFrame(nil), f.frames...)
}

// newPersonalTestPipeline builds a personal pipeline over a real compiled
// cypher and a real Core KV, wired the way InstallPersonalLens wires one: the
// actor is injected into the reserved __actor key field by the envelope.
func newPersonalTestPipeline(t *testing.T, adpt adapter.Adapter) (*Pipeline, *substrate.KV) {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (identity {key: $actorKey})-[:holds]->(l:lease)
RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId
`)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"entityId"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	p := &Pipeline{
		ruleID:      "personal-test-rule",
		adapterName: "nats_subject",
		coreKV:      coreKV,
		adjKV:       adjKV,
		engineKind:  ruleengine.EngineFull,
		fullEngine:  eng,
		fullCR:      fullCR,
		adpt:        adpt,
	}
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		actorKey, _ := params["actorKey"].(string)
		_, actorID, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, nil, ErrSkipProjection
		}
		out := make(map[string]any, len(keys)+1)
		for k, v := range keys {
			out[k] = v
		}
		out[adapter.PersonalActorKeyField] = actorID
		return row, out, nil
	})
	p.SetActorEnumerator(NewActorEnumerator(adjKV, coreKV, "identity"))
	return p, coreKV
}

func putPersonalVertex(t *testing.T, kv *substrate.KV, key, class string, fields map[string]any) {
	t.Helper()
	body := map[string]any{
		"key": key, "class": class, "isDeleted": false,
		"createdAt": "2026-08-14T00:00:00Z", "lastModifiedAt": "2026-08-14T00:00:00Z",
	}
	for k, v := range fields {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

const (
	personalActorA = "Hj4kPmRtw9nbCxz5vQ2y"
	personalActorB = "Kx3TmZpq7RvwNsY2Hc9L"
)

// TestReprojectPersonalActor_MissingIdentityPublishesEmptyFrame is T3 of
// personal-lens-grant-change-trigger-design.md §10 — the deliberate divergence
// from Hydrate, which errors "no such identity" here.
//
// A tombstoned identity is the expected companion of a grant retraction, so
// erroring would drop precisely the case that most needs retracting: the empty
// frame IS the "you may now read nothing" signal, and the client prunes every
// key it holds for the lens on receiving one.
func TestReprojectPersonalActor_MissingIdentityPublishesEmptyFrame(t *testing.T) {
	target := &fakePersonalTarget{}
	p, _ := newPersonalTestPipeline(t, target)

	err := p.ReprojectPersonalActor(context.Background(), personalActorA)

	require.NoError(t, err, "a missing actor is the retraction case, not an error")
	frames := target.snapshot()
	require.Len(t, frames, 1, "the empty frame must still be published")
	assert.Equal(t, personalActorA, frames[0].actorID)
	assert.Empty(t, frames[0].keys, "an actor that reads nothing is framed with no keys")

	// Contrast with Hydrate, whose refusal is correct for ITS caller: a device
	// asking to hydrate an identity that does not exist deserves to be told.
	_, hErr := p.Hydrate(context.Background(), personalActorA)
	require.Error(t, hErr, "Hydrate keeps its own missing-actor refusal")
}

// TestReprojectPersonalActor_RefusesATargetThatCannotFrame pins the fail-closed
// half: a target that publishes no keyset frame cannot retract anything, so a
// reprojection against one is an error rather than a silent success that leaves
// a revoked row on the device.
func TestReprojectPersonalActor_RefusesATargetThatCannotFrame(t *testing.T) {
	p, _ := newPersonalTestPipeline(t, &noFrameTarget{})

	err := p.ReprojectPersonalActor(context.Background(), personalActorA)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publishes no keyset frame")
}

type noFrameTarget struct{}

func (noFrameTarget) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (noFrameTarget) Delete(context.Context, map[string]any, uint64) error { return nil }
func (noFrameTarget) Probe(context.Context) error                          { return nil }
func (noFrameTarget) Close() error                                         { return nil }

// TestPersonalPublishLock_SerializesTwoPublishersForOneActor is T5b.
//
// It asserts the property that makes the revision posture sound, not merely
// that a mutex exists: while one publisher holds a (lens, actor), a second
// publisher for that SAME actor cannot reach its own publish — and a publisher
// for a DIFFERENT actor can, so the lock is keyed rather than global.
//
// Without it the drain worker, a live hydrate, and (later) the sweeper each
// capture their own revision and publish their own authoritative frame, and the
// client keeps whichever frame ARRIVED with the highest number rather than
// whichever is freshest. Interleaving two of them therefore lets a stale frame
// win by arriving second.
func TestPersonalPublishLock_SerializesTwoPublishersForOneActor(t *testing.T) {
	target := &fakePersonalTarget{
		entered: make(chan string, 4),
		release: make(chan struct{}),
	}
	p, coreKV := newPersonalTestPipeline(t, target)

	// Both identities must EXIST. Hydrate refuses a missing actor before it
	// ever reaches its publish, so without real vertices here the "a second
	// publisher cannot get through" assertion would hold because Hydrate
	// errored out, not because the lock stopped it — the test would pass with
	// the lock deleted.
	putPersonalVertex(t, coreKV, substrate.VertexKey("identity", personalActorA), "identity", nil)
	putPersonalVertex(t, coreKV, substrate.VertexKey("identity", personalActorB), "identity", nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = p.ReprojectPersonalActor(context.Background(), personalActorA)
	}()

	// The drain worker is now parked inside its publish for actor A, holding
	// A's slot.
	select {
	case got := <-target.entered:
		require.Equal(t, personalActorA, got)
	case <-time.After(10 * time.Second):
		t.Fatal("the first publisher never reached its publish")
	}

	// A concurrent Hydrate for the SAME actor must not get through. Hydrate is
	// the second of the three publishers the design names, and the one that
	// exists today.
	hydrateDone := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(hydrateDone)
		_, _ = p.Hydrate(context.Background(), personalActorA)
	}()
	select {
	case got := <-target.entered:
		t.Fatalf("a second publisher reached its publish for actor %q while the first still held it — the two frames can interleave", got)
	case <-time.After(500 * time.Millisecond):
	}

	// A DIFFERENT actor is unaffected: the lock is keyed on (lens, actor), so
	// one slow actor cannot serialize the whole drain.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = p.ReprojectPersonalActor(context.Background(), personalActorB)
	}()
	select {
	case got := <-target.entered:
		require.Equal(t, personalActorB, got, "a different actor must not queue behind actor A")
	case <-time.After(10 * time.Second):
		t.Fatal("a publisher for a different actor was blocked — the lock is global, not keyed")
	}

	close(target.release)
	select {
	case <-hydrateDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the queued publisher never ran after the holder released")
	}
	wg.Wait()
}

// TestPersonalPublishLock_ReleasesItsSlot pins the lifetime: a slot is dropped
// once nobody holds or wants it, so the map is bounded by concurrent
// reprojections rather than by the identity population.
func TestPersonalPublishLock_ReleasesItsSlot(t *testing.T) {
	p := &Pipeline{ruleID: "lock-lifetime"}

	unlock := p.lockPersonalActor(personalActorA)
	p.personalPublishMu.Lock()
	require.Len(t, p.personalPublishLocks, 1, "a held slot is present")
	p.personalPublishMu.Unlock()

	unlock()
	p.personalPublishMu.Lock()
	require.Empty(t, p.personalPublishLocks, "a released slot nobody wants is dropped")
	p.personalPublishMu.Unlock()
}

// TestPersonalTarget_ProducesNoDeleteShapedResult is T9 — an invariant that
// holds today only by scattered convention, pinned while the reasoning is
// fresh.
//
// If a KeySetPublisher-targeted pipeline ever produced a Delete-shaped
// EvalResult, the client's ApplyDelete clears EVERY lens's attribution for that
// key — one lens's tombstone wiping a sibling lens's live grant on-device,
// which is the exact shape the grant-change edge exists to prevent. Retraction
// on this plane is the frame's job and only the frame's.
func TestPersonalTarget_ProducesNoDeleteShapedResult(t *testing.T) {
	target := &fakePersonalTarget{}
	p, coreKV := newPersonalTestPipeline(t, target)

	present := substrate.VertexKey("identity", personalActorA)
	putPersonalVertex(t, coreKV, present, "identity", map[string]any{"name": "present"})
	missing := substrate.VertexKey("identity", personalActorB)

	// Both arms in one call: an actor that exists but matches nothing, and an
	// actor that does not exist at all — the missing-actor arm is the one that
	// produces a Delete for an actor-aggregate target.
	results, err := p.reprojectActors(context.Background(), p.ruleState(), []string{present, missing})
	require.NoError(t, err)

	for _, r := range results {
		assert.False(t, r.Delete,
			"a personal target retracts through the authoritative frame; a Delete-shaped result would clear every lens's attribution for the key on-device")
	}
	assert.Nil(t, p.actorDeleteKey,
		"a personal lens installs no actor-delete-key derivation, which is what the missing-actor Delete arm needs")

	// And the pipeline is genuinely the personal shape, so the assertions above
	// are about the family they claim to be about.
	_, isPersonal := p.currentAdapter().(adapter.KeySetPublisher)
	require.True(t, isPersonal)
}
