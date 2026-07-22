package projection

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/jsstore"
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
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
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

func putReadDoc(t *testing.T, kv *substrate.KV, key string, anchors ...string) {
	t.Helper()
	type readableAnchor struct {
		AnchorType string `json:"anchorType"`
		AnchorID   string `json:"anchorId"`
	}
	body := struct {
		IsDeleted       bool             `json:"isDeleted"`
		ReadableAnchors []readableAnchor `json:"readableAnchors"`
	}{}
	for _, a := range anchors {
		body.ReadableAnchors = append(body.ReadableAnchors, readableAnchor{AnchorType: "task", AnchorID: a})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := kv.Put(context.Background(), key, raw); err != nil {
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
	putReadDoc(t, capKV, "cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y", "Aj4kPmRtw9nbCxz5vQ2y")
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
	capKV := newPersonalTestBucket(t, "capability-kv")     // empty: no grant at all
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
