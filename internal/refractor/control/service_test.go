package control_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/control"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// mockResumer records Resume calls so tests can assert the pipeline was told to resume.
type mockResumer struct {
	mu      sync.Mutex
	resumed bool
}

func (m *mockResumer) Resume(_ context.Context) {
	m.mu.Lock()
	m.resumed = true
	m.mu.Unlock()
}

func (m *mockResumer) wasResumed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resumed
}

// startControlTestServer starts an in-memory NATS server for health reporter use.
// Returns only JetStream; use startControlTestServerConn when nc is also required.
func startControlTestServer(t *testing.T) jetstream.JetStream {
	t.Helper()
	_, js := startControlTestServerConn(t)
	return js
}

// startControlTestServerConn starts an in-memory NATS server and returns both
// the raw *nats.Conn (for nc.Request calls) and a JetStream handle.
func startControlTestServerConn(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return nc, js
}

// ── Existing tests ────────────────────────────────────────────────────────────

// TestControl_ResumeRule_CallsResumer verifies that ResumeRule calls the registered Resumer.
func TestControl_ResumeRule_CallsResumer(t *testing.T) {
	svc := control.NewService()
	mr := &mockResumer{}
	js := startControlTestServer(t)

	ctx := context.Background()
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "materializer-health-ctrl"})
	require.NoError(t, err)
	reporter := health.New(kv, "rule-ctrl", "team-a")

	svc.Register("rule-ctrl", mr, reporter)

	err = svc.ResumeRule(ctx, "rule-ctrl")
	require.NoError(t, err)
	assert.True(t, mr.wasResumed(), "ResumeRule must call Resumer.Resume")
}

// TestControl_ResumeRule_NotRegistered verifies that ResumeRule returns an error for unknown rules.
func TestControl_ResumeRule_NotRegistered(t *testing.T) {
	svc := control.NewService()
	err := svc.ResumeRule(context.Background(), "nonexistent-rule")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-rule")
}

// TestControl_Unregister verifies that after Unregister, ResumeRule returns an error.
func TestControl_Unregister(t *testing.T) {
	svc := control.NewService()
	mr := &mockResumer{}
	svc.Register("rule-unreg", mr, nil)
	svc.Unregister("rule-unreg")

	err := svc.ResumeRule(context.Background(), "rule-unreg")
	require.Error(t, err, "ResumeRule must error after Unregister")
}

// ── NATS listener tests ───────────────────────────────────────────────────────

// sendControlRequest marshals req and sends it via NATS request-reply, returning the decoded response.
func sendControlRequest(t *testing.T, nc *nats.Conn, req control.ControlRequest) control.ControlResponse {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	reply, err := nc.Request(subjects.Control(), data, 2*time.Second)
	require.NoError(t, err, "NATS request to control endpoint must succeed")
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

// TestControl_Health_ReturnsEntry verifies that the "health" op returns the current
// health KV entry for a registered rule (AC1).
func TestControl_Health_ReturnsEntry(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "materializer-health-5-1"})
	require.NoError(t, err)
	reporter := health.New(kv, "rule-5-1", "team-5-1")
	require.NoError(t, reporter.SetActive(ctx))

	svc := control.NewService()
	mr := &mockResumer{}
	svc.Register("rule-5-1", mr, reporter)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "health", RuleID: "rule-5-1"})

	require.Empty(t, resp.Error, "error field must be empty on success")
	require.NotNil(t, resp.Entry, "Entry must be non-nil on health success")
	assert.Equal(t, "rule-5-1", resp.RuleID)
	assert.Equal(t, "active", resp.Status)
	assert.Equal(t, "team-5-1", resp.Team)
}

// TestControl_Health_UnknownRule verifies that requesting health for an unregistered
// rule returns an error response containing the rule ID (AC1 error path).
func TestControl_Health_UnknownRule(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "health", RuleID: "ghost-rule"})

	assert.NotEmpty(t, resp.Error, "error field must be set for unknown rule")
	assert.Contains(t, resp.Error, "ghost-rule")
}

// TestControl_UnknownOp verifies that an unrecognized op returns the correct error (AC2).
func TestControl_UnknownOp(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "bogus", RuleID: "any"})

	assert.NotEmpty(t, resp.Error, "error field must be set for unknown op")
	assert.Contains(t, resp.Error, "unknown operation: bogus")
}

// TestControl_InvalidJSON verifies that malformed request bytes return a parse error (AC2).
func TestControl_InvalidJSON(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	// Send raw non-JSON bytes directly (bypass helper).
	reply, err := nc.Request(subjects.Control(), []byte("not-json{{"), 2*time.Second)
	require.NoError(t, err)

	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	assert.NotEmpty(t, resp.Error, "error field must be set for invalid JSON")
	assert.Contains(t, resp.Error, "invalid request")
}

// ── Validate op tests ─────────────────────────────────────────────────────────

// mockRuleGetter satisfies control.RuleGetter for testing the validate op.
type mockRuleGetter struct {
	rules map[string]*lens.Rule
}

func (m *mockRuleGetter) Get(ruleID string) (*lens.Rule, bool) {
	r, ok := m.rules[ruleID]
	return r, ok
}

// validateTestLens builds a *lens.Rule with the given match clause, bypassing YAML parsing.
// The Match field is used directly by validateRule → engine.Parse + engine.Compile.
func validateTestLens(id, match string) *lens.Rule {
	return &lens.Rule{
		ID:    id,
		Team:  "test-team",
		Match: match,
		Into: lens.IntoConfig{
			Target: "nats_kv",
			Bucket: "test-bucket",
			Key:    lens.KeyField{"id"},
		},
	}
}

// TestControl_Validate_FieldsPresent verifies that when Core KV entries contain
// the referenced fields, all FieldReports have present=true and no warnings (AC1, AC2).
func TestControl_Validate_FieldsPresent(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-validate-present"})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, "entity-1", []byte(`{"id": "1", "name": "Foo"}`))
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, "entity-2", []byte(`{"id": "2", "name": "Bar"}`))
	require.NoError(t, err)

	testRule := validateTestLens("validate-rule-present", `MATCH (a:agreement) RETURN a.id AS id, a.name AS name`)
	svc := control.NewService()
	svc.SetRuleGetter(&mockRuleGetter{rules: map[string]*lens.Rule{"validate-rule-present": testRule}})
	svc.SetCoreKV(coreKV)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "validate", RuleID: "validate-rule-present"})

	require.Empty(t, resp.Error, "error field must be empty on validate success")
	require.NotNil(t, resp.Validate, "Validate must be non-nil on validate success")
	assert.GreaterOrEqual(t, resp.Validate.SampleSize, 1)
	assert.Len(t, resp.Validate.FieldReports, 2)
	for _, fr := range resp.Validate.FieldReports {
		assert.True(t, fr.Present, "field %q should be present", fr.Field)
		assert.Greater(t, fr.FoundIn, 0)
	}
	assert.Empty(t, resp.Validate.Warnings, "no warnings expected when all fields are present")
}

// TestControl_Validate_FieldAbsent verifies that missing fields are flagged as
// warnings with present=false (AC2, AC3).
func TestControl_Validate_FieldAbsent(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Entries contain "id" but NOT "email"
	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-validate-absent"})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, "entity-1", []byte(`{"id": "1"}`))
	require.NoError(t, err)

	testRule := validateTestLens("validate-rule-absent", `MATCH (a:agreement) RETURN a.id AS id, a.email AS email`)
	svc := control.NewService()
	svc.SetRuleGetter(&mockRuleGetter{rules: map[string]*lens.Rule{"validate-rule-absent": testRule}})
	svc.SetCoreKV(coreKV)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "validate", RuleID: "validate-rule-absent"})

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Validate)
	assert.Equal(t, 1, resp.Validate.SampleSize)

	byField := make(map[string]control.FieldReport)
	for _, fr := range resp.Validate.FieldReports {
		byField[fr.Field] = fr
	}
	idReport, ok := byField["a.id"]
	require.True(t, ok, "a.id report expected")
	assert.True(t, idReport.Present)

	emailReport, ok := byField["a.email"]
	require.True(t, ok, "a.email report expected")
	assert.False(t, emailReport.Present, "a.email should be absent")
	assert.Equal(t, 0, emailReport.FoundIn)

	require.NotEmpty(t, resp.Validate.Warnings, "warnings expected for absent field")
	assert.Contains(t, resp.Validate.Warnings[0], "a.email")
}

// TestControl_Validate_EmptyBucket verifies that an empty Core KV bucket returns
// sampleSize=0 and all fields absent — not an error (AC1, AC3).
func TestControl_Validate_EmptyBucket(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Empty bucket — no entries published
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-validate-empty"})
	require.NoError(t, err)
	coreKV, err := js.KeyValue(ctx, "core-validate-empty")
	require.NoError(t, err)

	testRule := validateTestLens("validate-rule-empty", `MATCH (a:agreement) RETURN a.id AS id`)
	svc := control.NewService()
	svc.SetRuleGetter(&mockRuleGetter{rules: map[string]*lens.Rule{"validate-rule-empty": testRule}})
	svc.SetCoreKV(coreKV)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "validate", RuleID: "validate-rule-empty"})

	require.Empty(t, resp.Error, "empty bucket must not return an error")
	require.NotNil(t, resp.Validate)
	assert.Equal(t, 0, resp.Validate.SampleSize)
	require.Len(t, resp.Validate.FieldReports, 1)
	assert.False(t, resp.Validate.FieldReports[0].Present)
	assert.NotEmpty(t, resp.Validate.Warnings)
}

// TestControl_Validate_RuleNotLoaded verifies that a validate request for an
// unregistered ruleId returns an error (AC1 error path).
func TestControl_Validate_RuleNotLoaded(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-validate-norule"})
	require.NoError(t, err)

	svc := control.NewService()
	// SetRuleGetter with empty map — rule not found
	svc.SetRuleGetter(&mockRuleGetter{rules: map[string]*lens.Rule{}})
	svc.SetCoreKV(coreKV)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "validate", RuleID: "ghost-rule"})

	assert.NotEmpty(t, resp.Error, "error field must be set for unregistered rule")
	assert.Contains(t, resp.Error, "ghost-rule")
	assert.Nil(t, resp.Validate)
}

// ── Rebuild op tests ──────────────────────────────────────────────────────────

// mockRebuilder records Rebuild calls so tests can assert the pipeline was told to rebuild.
type mockRebuilder struct {
	mu          sync.Mutex
	rebuilt     bool
	truncateArg bool
	err         error // non-nil → Rebuild returns this error
}

func (m *mockRebuilder) Rebuild(_ context.Context, truncate bool) error {
	m.mu.Lock()
	m.rebuilt = true
	m.truncateArg = truncate
	m.mu.Unlock()
	return m.err
}

func (m *mockRebuilder) wasRebuilt() (rebuilt bool, truncate bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rebuilt, m.truncateArg
}

// TestControl_Rebuild_ReturnsAck verifies that a "rebuild" op for a registered
// rule returns an ack with rebuild.started=true and no error (AC6).
func TestControl_Rebuild_ReturnsAck(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	mr := &mockRebuilder{}
	svc.RegisterRebuilder("rebuild-rule-1", mr)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "rebuild", RuleID: "rebuild-rule-1"})

	require.Empty(t, resp.Error, "error field must be empty on rebuild ack")
	require.NotNil(t, resp.Rebuild, "rebuild field must be non-nil on success")
	assert.True(t, resp.Rebuild.Started, "rebuild.started must be true")
	assert.Nil(t, resp.Validate, "validate field must be absent")
	assert.Nil(t, resp.Entry, "Entry must be absent on rebuild ack")

	// Give the goroutine time to fire.
	require.Eventually(t, func() bool {
		rebuilt, _ := mr.wasRebuilt()
		return rebuilt
	}, 500*time.Millisecond, 5*time.Millisecond, "Rebuild must be called on the registered Rebuilder")
}

// TestControl_Rebuild_TruncateFlag verifies that truncate=true in the request is
// forwarded to the Rebuilder (AC2).
func TestControl_Rebuild_TruncateFlag(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	mr := &mockRebuilder{}
	svc.RegisterRebuilder("rebuild-rule-trunc", mr)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{
		Op:       "rebuild",
		RuleID:   "rebuild-rule-trunc",
		Truncate: true,
	})

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Rebuild)
	assert.True(t, resp.Rebuild.Started)

	require.Eventually(t, func() bool {
		rebuilt, _ := mr.wasRebuilt()
		return rebuilt
	}, 500*time.Millisecond, 5*time.Millisecond, "Rebuild must be called")

	_, truncateArg := mr.wasRebuilt()
	assert.True(t, truncateArg, "Rebuilder must receive truncate=true")
}

// TestControl_Rebuild_RuleNotRegistered verifies that a rebuild request for an
// unregistered ruleId returns a descriptive error (AC1 error path).
func TestControl_Rebuild_RuleNotRegistered(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	// No Rebuilder registered.
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "rebuild", RuleID: "ghost-rebuild-rule"})

	assert.NotEmpty(t, resp.Error, "error field must be set for unregistered rule")
	assert.Contains(t, resp.Error, "ghost-rebuild-rule")
	assert.Nil(t, resp.Rebuild)
}

// ── Pause op tests ────────────────────────────────────────────────────────────

// mockPauser records Pause calls so tests can assert the pipeline was told to pause.
type mockPauser struct {
	mu     sync.Mutex
	paused bool
}

func (m *mockPauser) Pause(_ context.Context) {
	m.mu.Lock()
	m.paused = true
	m.mu.Unlock()
}

func (m *mockPauser) wasPaused() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paused
}

// TestControl_Pause_ReturnsAck verifies that a "pause" op for a registered rule
// returns a synchronous ack with pause.paused=true and no error (AC1, AC5).
func TestControl_Pause_ReturnsAck(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	mp := &mockPauser{}
	svc.RegisterPauser("pause-rule-1", mp)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "pause", RuleID: "pause-rule-1"})

	require.Empty(t, resp.Error, "error field must be empty on pause ack")
	require.NotNil(t, resp.Pause, "pause field must be non-nil on success")
	assert.True(t, resp.Pause.Paused, "pause.paused must be true")
	assert.Nil(t, resp.Entry, "Entry must be absent on pause ack")
	assert.Nil(t, resp.Rebuild, "rebuild field must be absent on pause ack")
	assert.True(t, mp.wasPaused(), "Pauser.Pause must have been called")
}

// TestControl_Pause_RuleNotRegistered verifies that a pause request for an
// unregistered ruleId returns a descriptive error (AC1 error path).
func TestControl_Pause_RuleNotRegistered(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	// No Pauser registered.
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "pause", RuleID: "ghost-pause-rule"})

	assert.NotEmpty(t, resp.Error, "error field must be set for unregistered rule")
	assert.Contains(t, resp.Error, "ghost-pause-rule")
	assert.Nil(t, resp.Pause)
}

// ── Resume op (via NATS) tests ────────────────────────────────────────────────

// TestControl_Resume_ReturnsAck verifies that a "resume" op for a registered rule
// returns a synchronous ack with resume.resumed=true and no error (AC2, AC6).
func TestControl_Resume_ReturnsAck(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "materializer-health-resume-ack"})
	require.NoError(t, err)
	reporter := health.New(kv, "resume-rule-1", "team-a")

	svc := control.NewService()
	mr := &mockResumer{}
	svc.Register("resume-rule-1", mr, reporter)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "resume", RuleID: "resume-rule-1"})

	require.Empty(t, resp.Error, "error field must be empty on resume ack")
	require.NotNil(t, resp.Resume, "resume field must be non-nil on success")
	assert.True(t, resp.Resume.Resumed, "resume.resumed must be true")
	assert.Nil(t, resp.Entry, "Entry must be absent on resume ack")
	assert.True(t, mr.wasResumed(), "Resumer.Resume must have been called")
}

// TestControl_Resume_RuleNotRegistered verifies that a resume request for an
// unregistered ruleId returns a descriptive error.
func TestControl_Resume_RuleNotRegistered(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "resume", RuleID: "ghost-resume-rule"})

	assert.NotEmpty(t, resp.Error, "error field must be set for unregistered rule")
	assert.Contains(t, resp.Error, "ghost-resume-rule")
	assert.Nil(t, resp.Resume)
}

// ── Delete op tests ───────────────────────────────────────────────────────────

// mockDeleter records Delete calls so tests can assert the rule teardown was triggered.
type mockDeleter struct {
	mu      sync.Mutex
	deleted bool
	err     error // non-nil → Delete returns this error
}

func (m *mockDeleter) Delete(_ context.Context) error {
	m.mu.Lock()
	m.deleted = true
	m.mu.Unlock()
	return m.err
}

func (m *mockDeleter) wasDeleted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleted
}

// TestControl_Delete_ReturnsAck verifies that a "delete" op for a registered rule
// returns a synchronous ack with delete.deleted=true and no error (AC4).
func TestControl_Delete_ReturnsAck(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	md := &mockDeleter{}
	svc.RegisterDeleter("delete-rule-1", md)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "delete", RuleID: "delete-rule-1"})

	require.Empty(t, resp.Error, "error field must be empty on delete ack")
	require.NotNil(t, resp.Delete, "delete field must be non-nil on success")
	assert.True(t, resp.Delete.Deleted, "delete.deleted must be true")
	assert.Nil(t, resp.Entry, "Entry must be absent on delete ack")
	assert.Nil(t, resp.Rebuild, "rebuild field must be absent on delete ack")
	assert.Nil(t, resp.Pause, "pause field must be absent on delete ack")
}

// TestControl_Delete_RuleNotRegistered verifies that a delete request for an
// unregistered ruleId returns a descriptive error (AC5).
func TestControl_Delete_RuleNotRegistered(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	// No Deleter registered.
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "delete", RuleID: "ghost-delete-rule"})

	assert.NotEmpty(t, resp.Error, "error field must be set for unregistered rule")
	assert.Contains(t, resp.Error, "ghost-delete-rule")
	assert.Nil(t, resp.Delete)
}

// TestControl_Delete_CallsDeleter verifies that the mockDeleter.Delete() is actually
// called when the delete op is processed (AC1).
func TestControl_Delete_CallsDeleter(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	md := &mockDeleter{}
	svc.RegisterDeleter("delete-rule-calls", md)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "delete", RuleID: "delete-rule-calls"})

	require.Empty(t, resp.Error)
	assert.True(t, md.wasDeleted(), "Deleter.Delete must have been called")
}

// ── Zero-downtime migration (two-rule) tests ─────────────────────────────────

// TestControl_TwoRules_BothRegistered verifies that two rules with different IDs can
// be registered simultaneously and the health op returns distinct entries for each (AC1, FR32).
func TestControl_TwoRules_BothRegistered(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Create health KV entries for both rules.
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "materializer-health-two-rules"})
	require.NoError(t, err)
	reporterV1 := health.New(kv, "agreement-summary-v1", "team-a")
	reporterV2 := health.New(kv, "agreement-summary-v2", "team-a")
	require.NoError(t, reporterV1.SetActive(ctx))
	require.NoError(t, reporterV2.SetActive(ctx))

	svc := control.NewService()
	mrV1 := &mockResumer{}
	mrV2 := &mockResumer{}
	svc.Register("agreement-summary-v1", mrV1, reporterV1)
	svc.Register("agreement-summary-v2", mrV2, reporterV2)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	// Both health ops must succeed and return the correct ruleId.
	respV1 := sendControlRequest(t, nc, control.ControlRequest{Op: "health", RuleID: "agreement-summary-v1"})
	require.Empty(t, respV1.Error, "v1 health must succeed")
	require.NotNil(t, respV1.Entry)
	assert.Equal(t, "agreement-summary-v1", respV1.RuleID)
	assert.Equal(t, "active", respV1.Status)

	respV2 := sendControlRequest(t, nc, control.ControlRequest{Op: "health", RuleID: "agreement-summary-v2"})
	require.Empty(t, respV2.Error, "v2 health must succeed")
	require.NotNil(t, respV2.Entry)
	assert.Equal(t, "agreement-summary-v2", respV2.RuleID)
	assert.Equal(t, "active", respV2.Status)
}

// TestControl_TwoRules_DeleteV1_LeavesV2 verifies that deleting v1 does not affect v2:
// after the delete, v1 returns "not registered" while v2 still returns its health entry (AC3, FR32).
func TestControl_TwoRules_DeleteV1_LeavesV2(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "materializer-health-del-v1"})
	require.NoError(t, err)
	reporterV1 := health.New(kv, "agreement-summary-v1", "team-a")
	reporterV2 := health.New(kv, "agreement-summary-v2", "team-a")
	require.NoError(t, reporterV1.SetActive(ctx))
	require.NoError(t, reporterV2.SetActive(ctx))

	svc := control.NewService()
	mrV1 := &mockResumer{}
	mrV2 := &mockResumer{}
	mpV1 := &mockPauser{}
	mdV1 := &mockDeleter{}
	mdV2 := &mockDeleter{}
	svc.Register("agreement-summary-v1", mrV1, reporterV1)
	svc.Register("agreement-summary-v2", mrV2, reporterV2)
	svc.RegisterPauser("agreement-summary-v1", mpV1)
	svc.RegisterDeleter("agreement-summary-v1", mdV1)
	svc.RegisterDeleter("agreement-summary-v2", mdV2)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	// Delete v1.
	delResp := sendControlRequest(t, nc, control.ControlRequest{Op: "delete", RuleID: "agreement-summary-v1"})
	require.Empty(t, delResp.Error, "delete v1 must succeed")
	require.NotNil(t, delResp.Delete)
	assert.True(t, delResp.Delete.Deleted)

	// v1 Deleter must have been called; v2 Deleter must NOT have been called.
	assert.True(t, mdV1.wasDeleted(), "v1 Deleter.Delete must have been called")
	assert.False(t, mdV2.wasDeleted(), "v2 Deleter.Delete must NOT have been called")

	// v1 health must now return "not registered" — confirms reporters map is cleared.
	v1Health := sendControlRequest(t, nc, control.ControlRequest{Op: "health", RuleID: "agreement-summary-v1"})
	assert.NotEmpty(t, v1Health.Error, "v1 health after delete must return error")
	assert.Contains(t, v1Health.Error, "agreement-summary-v1")

	// v1 pause must also return "not registered" — confirms pauserByRuleID is cleared (AC3).
	v1Pause := sendControlRequest(t, nc, control.ControlRequest{Op: "pause", RuleID: "agreement-summary-v1"})
	assert.NotEmpty(t, v1Pause.Error, "v1 pause after delete must return error")
	assert.Contains(t, v1Pause.Error, "agreement-summary-v1")

	// v2 health must still succeed.
	v2Health := sendControlRequest(t, nc, control.ControlRequest{Op: "health", RuleID: "agreement-summary-v2"})
	require.Empty(t, v2Health.Error, "v2 health must still succeed after v1 delete")
	require.NotNil(t, v2Health.Entry)
	assert.Equal(t, "agreement-summary-v2", v2Health.RuleID)
}

// TestControl_TwoRules_PauseV1_DoesNotAffectV2 verifies that pausing v1 does not
// trigger the pause on v2's registered Pauser (AC1, FR32).
func TestControl_TwoRules_PauseV1_DoesNotAffectV2(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	mpV1 := &mockPauser{}
	mpV2 := &mockPauser{}
	svc.RegisterPauser("agreement-summary-v1", mpV1)
	svc.RegisterPauser("agreement-summary-v2", mpV2)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	// Pause only v1.
	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "pause", RuleID: "agreement-summary-v1"})
	require.Empty(t, resp.Error, "pause v1 must succeed")
	require.NotNil(t, resp.Pause)
	assert.True(t, resp.Pause.Paused)

	// v1 Pauser must be paused; v2 Pauser must NOT be paused.
	assert.True(t, mpV1.wasPaused(), "v1 Pauser.Pause must have been called")
	assert.False(t, mpV2.wasPaused(), "v2 Pauser.Pause must NOT have been called")
}

// TestControl_Delete_UnregistersRule verifies that after a successful delete, subsequent
// control requests for the same ruleId return an error (AC2).
func TestControl_Delete_UnregistersRule(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Register a full set: resumer + reporter + deleter.
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "materializer-health-del-unreg"})
	require.NoError(t, err)
	reporter := health.New(kv, "delete-unreg-rule", "team-a")
	require.NoError(t, reporter.SetActive(ctx))

	svc := control.NewService()
	mr := &mockResumer{}
	md := &mockDeleter{}
	svc.Register("delete-unreg-rule", mr, reporter)
	svc.RegisterDeleter("delete-unreg-rule", md)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	// Issue the delete.
	resp := sendControlRequest(t, nc, control.ControlRequest{Op: "delete", RuleID: "delete-unreg-rule"})
	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Delete)

	// Subsequent "health" op must return "not registered" error (AC2).
	resp2 := sendControlRequest(t, nc, control.ControlRequest{Op: "health", RuleID: "delete-unreg-rule"})
	assert.NotEmpty(t, resp2.Error, "health op after delete must return error")
	assert.Contains(t, resp2.Error, "delete-unreg-rule")
}
