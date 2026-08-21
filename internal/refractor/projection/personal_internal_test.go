package projection

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const personalTestActorKey = "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"

func newPersonalTestBucket(t *testing.T, bucket string) *substrate.KV {
	t.Helper()
	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	t.Cleanup(conn.Close)

	ctx := context.Background()
	if _, err := conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
		t.Fatalf("create kv: %v", err)
	}
	kv, err := conn.OpenKV(ctx, bucket)
	if err != nil {
		t.Fatalf("open kv: %v", err)
	}
	return kv
}

// putPerAnchorEntry seeds one cap-read-per-anchor-grant-keys-design.md §3.2
// per-anchor key: "cap-read.<actorSuffix>.<anchorID>", body {"isDeleted":
// false}. anchorId lives in the key, not the body.
func putPerAnchorEntry(t *testing.T, kv *substrate.KV, actorSuffix, anchorID string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"isDeleted": false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := kv.Put(context.Background(), "cap-read."+actorSuffix+"."+anchorID, raw); err != nil {
		t.Fatalf("put: %v", err)
	}
}

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardTestWriter{}, nil))
}

type discardTestWriter struct{}

func (discardTestWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- IsPersonalLens ---

func TestIsPersonalLens(t *testing.T) {
	if IsPersonalLens(nil) {
		t.Fatalf("nil rule must not be a personal lens")
	}
	if IsPersonalLens(&lens.Rule{Into: lens.IntoConfig{Target: "nats_kv", Personal: true}}) {
		t.Fatalf("a non-nats_subject target must not be a personal lens")
	}
	if IsPersonalLens(&lens.Rule{Into: lens.IntoConfig{Target: "nats_subject", Personal: false}}) {
		t.Fatalf("nats_subject without Personal must not be a personal lens")
	}
	if !IsPersonalLens(&lens.Rule{Into: lens.IntoConfig{Target: "nats_subject", Personal: true}}) {
		t.Fatalf("nats_subject + Personal must be a personal lens")
	}
}

// --- InstallPersonalLens ---

func personalTestRule(t *testing.T, match string, keyFields lens.KeyField) *lens.Rule {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(match)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &lens.Rule{
		ID:             "personal-lens-test",
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into:           lens.IntoConfig{Target: "nats_subject", Personal: true, Key: keyFields},
	}
}

const personalMatch = `
MATCH (identity:identity {key: $actorKey})<-[:assignedTo]-(task:task)
RETURN task.key AS anchor, "task" AS kind
`

func newPersonalPipeline(t *testing.T) *pipeline.Pipeline {
	t.Helper()
	adpt, err := adapter.New(nil, []string{"__actor", "anchor"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter.New: %v", err)
	}
	p, err := pipeline.New("personal-lens-test", "nats_subject", "CORE", nil, nil, adpt, nil)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}
	return p
}

func TestInstallPersonalLens_NotFullEngine_Refuses(t *testing.T) {
	r := personalTestRule(t, personalMatch, lens.KeyField{adapter.PersonalActorKeyField, "anchor"})
	r.CompiledRule = fakeCompiledRule{}
	p := newPersonalPipeline(t)

	ok := InstallPersonalLens(p, r, nil, nil, nil, nil, false, discardTestLogger())
	if ok {
		t.Fatalf("a non-full-engine compiled rule must refuse installation")
	}
}

func TestInstallPersonalLens_KeyColumnNotAReturnAlias_Refuses(t *testing.T) {
	r := personalTestRule(t, personalMatch, lens.KeyField{adapter.PersonalActorKeyField, "anchor", "bogusColumn"})
	p := newPersonalPipeline(t)

	ok := InstallPersonalLens(p, r, nil, nil, nil, nil, false, discardTestLogger())
	if ok {
		t.Fatalf("a business key column absent from the RETURN aliases must refuse installation")
	}
}

func TestInstallPersonalLens_WellFormed_Installs(t *testing.T) {
	r := personalTestRule(t, personalMatch, lens.KeyField{adapter.PersonalActorKeyField, "anchor"})
	p := newPersonalPipeline(t)

	ok := InstallPersonalLens(p, r, nil, nil, nil, nil, false, discardTestLogger())
	if !ok {
		t.Fatalf("a well-formed personal lens must install")
	}
}

// RR-3 (edge-lattice-full-design.md §8.1): with requireReadGate=true (the
// production posture), a nil capKV must REFUSE registration rather than
// install the read-grant gate open.
func TestInstallPersonalLens_RequireReadGate_NoCapKV_Refuses(t *testing.T) {
	r := personalTestRule(t, personalMatch, lens.KeyField{adapter.PersonalActorKeyField, "anchor"})
	p := newPersonalPipeline(t)

	ok := InstallPersonalLens(p, r, nil, nil, nil, nil /*capKV*/, true /*requireReadGate*/, discardTestLogger())
	if ok {
		t.Fatalf("requireReadGate=true with a nil capKV must refuse installation (a personal lens must never run open in production)")
	}
}

// TestInstallPersonalLens_MultiBranch_ThreadsKeyColumnsOnAllBranches
// reproduces the live 2026-07-30 edgeCatalog/edgeTasks/edgeEntitySessions
// bug: a multi-branch Personal lens (refractor-shared-keyspace-arbitration-
// design.md §13.2, one *full.CompiledRule per pkgmgr Walks entry) must have
// its business KeyColumns threaded onto EVERY branch, not just branch 0 —
// else a branch beyond 0 falls back to keying by its first RETURN alias
// alone and drops every other declared business key from the keys map the
// adapter receives.
func TestInstallPersonalLens_MultiBranch_ThreadsKeyColumnsOnAllBranches(t *testing.T) {
	r := personalTestRule(t, personalMatch, lens.KeyField{adapter.PersonalActorKeyField, "anchor", "kind"})

	eng := full.New()
	branch2, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})<-[:ownedBy]-(task:task)
RETURN task.key AS anchor, "otherKind" AS kind
`)
	if err != nil {
		t.Fatalf("parse branch2: %v", err)
	}
	r.CompiledBranches = []ruleengine.CompiledRule{r.CompiledRule, branch2}

	p := newPersonalPipeline(t)
	if ok := InstallPersonalLens(p, r, nil, nil, nil, nil, false, discardTestLogger()); !ok {
		t.Fatalf("a well-formed multi-branch personal lens must install")
	}

	want := []string{"anchor", "kind"}
	for i, b := range r.CompiledBranches {
		cr, ok := b.(*full.CompiledRule)
		if !ok {
			t.Fatalf("branch %d: not a *full.CompiledRule", i)
		}
		if !slices.Equal(cr.KeyColumns, want) {
			t.Fatalf("branch %d KeyColumns = %v, want %v (an unthreaded branch reproduces the live bug: its rows key by only the first RETURN alias, and the adapter rejects every write missing the other declared business keys)", i, cr.KeyColumns, want)
		}
	}
}

// TestInstallPersonalLens_MultiBranch_KeyColumnNotReturnAliasInSecondBranch_Refuses
// is the failure-mode mirror: a second branch whose RETURN clause lacks one
// of the declared business key columns must fail installation, not silently
// install with that branch's rows missing the key. This pins ValidateKeyColumns
// against no-op'ing on an unthreaded (nil) KeyColumns — a branch that could
// never produce a business key must not go unvalidated.
func TestInstallPersonalLens_MultiBranch_KeyColumnNotReturnAliasInSecondBranch_Refuses(t *testing.T) {
	r := personalTestRule(t, personalMatch, lens.KeyField{adapter.PersonalActorKeyField, "anchor", "kind"})

	eng := full.New()
	branch2, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})<-[:ownedBy]-(task:task)
RETURN task.key AS anchor
`)
	if err != nil {
		t.Fatalf("parse branch2: %v", err)
	}
	r.CompiledBranches = []ruleengine.CompiledRule{r.CompiledRule, branch2}

	p := newPersonalPipeline(t)
	if ok := InstallPersonalLens(p, r, nil, nil, nil, nil, false, discardTestLogger()); ok {
		t.Fatalf("a second branch missing a declared business key column's RETURN alias must refuse installation")
	}
}

type fakeCompiledRule struct{}

func (fakeCompiledRule) EngineName() string { return "fake" }

// --- personalEnvelopeFn ---

func TestPersonalEnvelopeFn_EmptyActorKey_Skips(t *testing.T) {
	fn := personalEnvelopeFn(nil, nil, discardTestLogger())
	_, _, err := fn(map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y"}, nil, map[string]any{"actorKey": ""})
	if !errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("expected ErrSkipProjection for an empty actorKey, got %v", err)
	}
}

func TestPersonalEnvelopeFn_InvalidActorKey_Errors(t *testing.T) {
	fn := personalEnvelopeFn(nil, nil, discardTestLogger())
	_, _, err := fn(map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y"}, nil, map[string]any{"actorKey": "not-a-vertex-key"})
	if err == nil || errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("expected a hard error for a malformed actorKey, got %v", err)
	}
}

func TestPersonalEnvelopeFn_EmptyAnchor_Skips(t *testing.T) {
	fn := personalEnvelopeFn(nil, nil, discardTestLogger())
	_, _, err := fn(map[string]any{"anchor": ""}, nil, map[string]any{"actorKey": personalTestActorKey})
	if !errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("expected ErrSkipProjection for an empty anchor, got %v", err)
	}
}

func TestPersonalEnvelopeFn_NoGates_InjectsRecipient(t *testing.T) {
	fn := personalEnvelopeFn(nil, nil, discardTestLogger())
	row := map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y", "kind": "task"}
	newRow, newKeys, err := fn(row, map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y"}, map[string]any{"actorKey": personalTestActorKey})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newRow["anchor"] != "vtx.task.Aj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("row must pass through unchanged: %v", newRow)
	}
	if newKeys[adapter.PersonalActorKeyField] != "Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("expected the recipient's bare NanoID injected at %q, got %v", adapter.PersonalActorKeyField, newKeys)
	}
}

func TestPersonalEnvelopeFn_CapKV_InvalidAnchor_Errors(t *testing.T) {
	capKV := newPersonalTestBucket(t, "capability-kv")
	fn := personalEnvelopeFn(nil, capKV, discardTestLogger())
	row := map[string]any{"anchor": "not-a-vertex-key", "kind": "task"}
	_, _, err := fn(row, nil, map[string]any{"actorKey": personalTestActorKey})
	if err == nil || errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("expected a hard error for a malformed anchor, got %v", err)
	}
}

func TestPersonalEnvelopeFn_CapKV_NoGrant_Skips(t *testing.T) {
	capKV := newPersonalTestBucket(t, "capability-kv")
	fn := personalEnvelopeFn(nil, capKV, discardTestLogger())
	row := map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y", "kind": "task"}
	_, _, err := fn(row, nil, map[string]any{"actorKey": personalTestActorKey})
	if !errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("no read-grant slice at all must deny (skip), got %v", err)
	}
}

func TestPersonalEnvelopeFn_CapKV_RealGrant_Proceeds(t *testing.T) {
	capKV := newPersonalTestBucket(t, "capability-kv")
	putPerAnchorEntry(t, capKV, "identity.Hj4kPmRtw9nbCxz5vQ2y", "Aj4kPmRtw9nbCxz5vQ2y")
	fn := personalEnvelopeFn(nil, capKV, discardTestLogger())
	row := map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y", "kind": "task"}
	newRow, newKeys, err := fn(row, nil, map[string]any{"actorKey": personalTestActorKey})
	if err != nil {
		t.Fatalf("a real grant must project, got err %v", err)
	}
	if newRow["anchor"] != "vtx.task.Aj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("row must pass through: %v", newRow)
	}
	if newKeys[adapter.PersonalActorKeyField] != "Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("recipient must be injected: %v", newKeys)
	}
}

func TestPersonalEnvelopeFn_InterestSet_NoDevices_Proceeds(t *testing.T) {
	interestKV := newPersonalTestBucket(t, "personal-lens-interest")
	fn := personalEnvelopeFn(interestKV, nil, discardTestLogger())
	row := map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y", "kind": "task"}
	_, _, err := fn(row, nil, map[string]any{"actorKey": personalTestActorKey})
	if err != nil {
		t.Fatalf("no registered device must default to admit-everything, got err %v", err)
	}
}

func TestPersonalEnvelopeFn_InterestSet_IrrelevantType_Skips(t *testing.T) {
	interestKV := newPersonalTestBucket(t, "personal-lens-interest")
	if err := personalinterest.Register(context.Background(), interestKV, "Hj4kPmRtw9nbCxz5vQ2y", "device1",
		[]string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("register: %v", err)
	}
	fn := personalEnvelopeFn(interestKV, nil, discardTestLogger())
	row := map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y", "kind": "task"}
	_, _, err := fn(row, nil, map[string]any{"actorKey": personalTestActorKey})
	if !errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("a device filtering to a different type must decline this delta, got %v", err)
	}
}

func TestPersonalEnvelopeFn_InterestSet_RelevantType_Proceeds(t *testing.T) {
	interestKV := newPersonalTestBucket(t, "personal-lens-interest")
	if err := personalinterest.Register(context.Background(), interestKV, "Hj4kPmRtw9nbCxz5vQ2y", "device1",
		[]string{"task"}, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("register: %v", err)
	}
	fn := personalEnvelopeFn(interestKV, nil, discardTestLogger())
	row := map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y", "kind": "task"}
	newRow, newKeys, err := fn(row, nil, map[string]any{"actorKey": personalTestActorKey})
	if err != nil {
		t.Fatalf("a device filtering to a matching type must admit this delta, got %v", err)
	}
	if newRow["anchor"] != "vtx.task.Aj4kPmRtw9nbCxz5vQ2y" || newKeys[adapter.PersonalActorKeyField] != "Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("relevant delta must project with recipient injected: row=%v keys=%v", newRow, newKeys)
	}
}

// TestPersonalEnvelopeFn_SecurityWinsOverRelevance asserts the D1 read-grant
// gate runs before, and wins over, the Interest Set relevance filter — a
// delta an actor may not read is denied even when a device declares it
// relevant (personal-secure-lens-design.md §3.4).
func TestPersonalEnvelopeFn_SecurityWinsOverRelevance(t *testing.T) {
	capKV := newPersonalTestBucket(t, "capability-kv") // empty: no grant at all
	interestKV := newPersonalTestBucket(t, "personal-lens-interest")
	if err := personalinterest.Register(context.Background(), interestKV, "Hj4kPmRtw9nbCxz5vQ2y", "device1",
		[]string{"task"}, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("register: %v", err)
	}
	fn := personalEnvelopeFn(interestKV, capKV, discardTestLogger())
	row := map[string]any{"anchor": "vtx.task.Aj4kPmRtw9nbCxz5vQ2y", "kind": "task"}
	_, _, err := fn(row, nil, map[string]any{"actorKey": personalTestActorKey})
	if !errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("an unreadable anchor must be denied even when the device's Interest Set finds it relevant, got %v", err)
	}
}

// TestPatternClosure_OnlyActorAggregateAssertsIt pins the wiring seam the
// actor-aware relevance gate rests on
// (auth-plane-projection-latency-design.md §4.2/§4.4): an actor-aggregate lens
// declares its output pattern-closed, and a Personal Lens never does, because it
// also consults the D1 read gate (cap-read.<domain>.<actor>) and the Interest
// Set. Without this a role event that widens an actor's read grants could be
// filtered away from the very lens whose rows it widens.
func TestPatternClosure_OnlyActorAggregateAssertsIt(t *testing.T) {
	personalRule := personalTestRule(t, personalMatch,
		lens.KeyField{adapter.PersonalActorKeyField, "anchor"})
	personalPipe := newPersonalPipeline(t)
	if !InstallPersonalLens(personalPipe, personalRule, nil, nil, nil, nil, false, discardTestLogger()) {
		t.Fatalf("the personal fixture must install, or the assertion below is vacuous")
	}
	if personalPipe.PatternClosedOutput() {
		t.Fatalf("a personal lens must never be narrowed — its read gate and Interest Set are outside the compiled pattern")
	}

	eng := full.New()
	cr, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey, collect(role.key) AS roles
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	aggRule := &lens.Rule{
		ID:             "pattern-closure-test",
		CanonicalName:  "patternClosure",
		ProjectionKind: ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into:           lens.IntoConfig{Target: "nats_kv", Bucket: "capability-kv", Key: lens.KeyField{"key"}},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "patternClosure.{actorSuffix}",
			BodyColumns:      []string{"roles"},
			EmptyBehavior:    string(EmptyDelete),
			Freshness:        "auto",
		},
	}
	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter.New: %v", err)
	}
	aggPipe, err := pipeline.New(aggRule.ID, "nats_kv", "CORE", nil, nil, adpt, nil)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}
	if !InstallActorAggregate(aggPipe, adpt, aggRule, func(string) uint64 { return 0 }, nil, nil, discardTestLogger()) {
		t.Fatalf("the actor-aggregate fixture must install, or the assertion below is vacuous")
	}
	if !aggPipe.PatternClosedOutput() {
		t.Fatalf("an actor-aggregate lens's row is a function of its compiled pattern alone")
	}
}
