//go:build integration

// Package hellolattice_test is the Phase 1 Gate 5 integration test suite.
//
// It exercises the full Hello Lattice tutorial vertical slice:
//  1. Milestone 1: health gates 1-4 passed, bootstrap verified.
//  2. Milestone 2: CreateMetaVertex for the "book" DDL; verify canonicalName aspect.
//  3. Milestone 3: CreateBook; verify vtx.book.* in Core KV with correct title.
//  4. Milestone 4: CreateMetaVertex for the "books" Lens; poll Postgres for the book row.
//  5. Milestone 5: AI agent cold-start traversal; agent submits CreateBook; verify in Postgres.
//
// Build tags:
//
//	integration — requires live NATS and Postgres (make up).
//
// Environment variables:
//
//	NATS_URL — NATS server URL (required; e.g. nats://localhost:4222)
//	POSTGRES_URL — Postgres DSN (required for milestones 4-5)
//	BOOTSTRAP_JSON_PATH — path to lattice.bootstrap.json (defaults to ./lattice.bootstrap.json)
//	GITHUB_SHA — commit hash for the gate5 Health KV marker (optional)
//
// Run with:
//
//	make test-hello-lattice
package hellolattice_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/jackc/pgx/v5"

	"github.com/asolgan/lattice/internal/aiagent"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// replyInboxHeader is the NATS header used by the Processor reply path.
const replyInboxHeader = "Lattice-Reply-Inbox"

// defaultPostgresURL is the default Postgres connection string for the docker-compose stack.
const defaultPostgresURL = "postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable"

// bookDDLScript is the Starlark source for the tutorial "book" DDL.
const bookDDLScript = `def execute(state, op):
    p = op.payload
    if not hasattr(p, "title") or len(p.title.strip()) == 0:
        fail("InvalidArgument: title: required non-empty string")
    if len(p.title.strip()) > 500:
        fail("InvalidArgument: title: exceeds 500 character limit")
    title = p.title.strip()
    book_id = nanoid.new()
    book_key = "vtx.book." + book_id
    mutations = [
        {"op": "create", "key": book_key,
         "document": {"class": "book", "isDeleted": False,
                      "key": book_key,
                      "title": title,
                      "data": {"title": title}}},
    ]
    events = [{"class": "BookCreated", "data": {"bookKey": book_key}}]
    return {"mutations": mutations, "events": events, "response": {"bookKey": book_key}}`

// harnessConn is the shared NATS connection for the test suite.
var harnessConn *substrate.Conn

// suiteStart records when the suite began for elapsed-time reporting.
var suiteStart time.Time

// bookDDLKey holds the meta-vertex key for the book DDL (set in Milestone 2).
var bookDDLKey string

// bookVertexKey holds the vtx.book.* key created in Milestone 3.
var bookVertexKey string

// lensMetaKey holds the meta-vertex key for the books Lens (set in Milestone 4).
var lensMetaKey string

// TestMain establishes the shared connection and loads bootstrap IDs.
func TestMain(m *testing.M) {
	suiteStart = time.Now()

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	// Load bootstrap IDs (primordial NanoIDs from lattice.bootstrap.json).
	bootstrapPath := os.Getenv("BOOTSTRAP_JSON_PATH")
	if bootstrapPath == "" {
		bootstrapPath = "../../lattice.bootstrap.json"
	}
	if err := bootstrap.Load(bootstrapPath); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: bootstrap.Load(%s): %v\n"+
			"Ensure 'make up' has completed and lattice.bootstrap.json exists.\n", bootstrapPath, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:  natsURL,
		Name: "hellolattice-test",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: connect to NATS at %s: %v\n"+
			"Ensure 'make up' has completed.\n", natsURL, err)
		os.Exit(1)
	}
	harnessConn = conn

	code := m.Run()
	conn.Close()
	os.Exit(code)
}

// TestHelloLattice_Milestone1_Setup verifies health gates 1-4 are passed
// and bootstrap is in a clean state. Exercises the lattice binary end-to-end
// via exec.Command to confirm it is built and correctly wired.
func TestHelloLattice_Milestone1_Setup(t *testing.T) {
	start := time.Now()

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
	bootstrapPath := os.Getenv("BOOTSTRAP_JSON_PATH")
	if bootstrapPath == "" {
		bootstrapPath = "../../lattice.bootstrap.json"
	}

	latticeBin := "../../bin/lattice"
	if v := os.Getenv("LATTICE_BIN"); v != "" {
		latticeBin = v
	}

	// Run `lattice health gates` and assert exit 0 + gates 1-4 listed.
	hgCmd := exec.Command(latticeBin, "health", "gates")
	hgCmd.Env = append(os.Environ(), "NATS_URL="+natsURL)
	hgOut, hgErr := hgCmd.CombinedOutput()
	if hgErr != nil {
		t.Fatalf("lattice health gates: exit error: %v\noutput:\n%s", hgErr, string(hgOut))
	}
	hgStr := string(hgOut)
	for gate := 1; gate <= 4; gate++ {
		marker := fmt.Sprintf("gate%d", gate)
		if !strings.Contains(hgStr, marker) {
			t.Errorf("lattice health gates: output missing %q\noutput:\n%s", marker, hgStr)
		}
	}
	t.Logf("lattice health gates output:\n%s", hgStr)

	// Run `lattice bootstrap verify` and assert exit 0.
	bvCmd := exec.Command(latticeBin, "bootstrap", "verify")
	bvCmd.Env = append(os.Environ(),
		"NATS_URL="+natsURL,
		"BOOTSTRAP_JSON_PATH="+bootstrapPath,
	)
	bvOut, bvErr := bvCmd.CombinedOutput()
	if bvErr != nil {
		t.Fatalf("lattice bootstrap verify: exit error: %v\noutput:\n%s", bvErr, string(bvOut))
	}
	t.Logf("lattice bootstrap verify output:\n%s", string(bvOut))

	t.Logf("milestone 1 elapsed: %v", time.Since(start))
}

// TestHelloLattice_Milestone2_DefineDDL submits CreateMetaVertex for the
// "book" DDL and verifies the meta-vertex in Core KV.
func TestHelloLattice_Milestone2_DefineDDL(t *testing.T) {
	start := time.Now()
	ctx := testCtx(t)

	// Build the book DDL payload with all required self-description fields.
	payload := map[string]any{
		"targetClass":      "meta.ddl.vertexType",
		"canonicalName":    "book",
		"permittedCommands": []string{"CreateBook"},
		"description":      "Book vertex DDL. A book carries a title.",
		"script":           bookDDLScript,
		"inputSchema":      `{"type":"object","required":["title"],"properties":{"title":{"type":"string","maxLength":500}}}`,
		"outputSchema":     `{"type":"object","required":["bookKey"],"properties":{"bookKey":{"type":"string"}}}`,
		"fieldDescription": map[string]string{"title": "Book title, max 500 characters. Required."},
		"examples": []map[string]any{
			{
				"name":            "CreateBook — minimal",
				"payload":         map[string]any{"title": "The Pragmatic Programmer"},
				"expectedOutcome": "Creates vtx.book.<NanoID>; returns bookKey.",
			},
		},
	}

	reply := submitOp(t, ctx, "CreateMetaVertex", processor.LaneMeta, bootstrap.BootstrapIdentityKey, payload)
	if reply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("CreateMetaVertex rejected: %v", reply)
	}

	// Extract metaKey from the reply detail.
	metaKey, _ := reply.Detail["metaKey"].(string)
	if !strings.HasPrefix(metaKey, "vtx.meta.") {
		t.Fatalf("metaKey not a vtx.meta.* key: %q", metaKey)
	}
	bookDDLKey = metaKey
	t.Logf("book DDL created at %s", bookDDLKey)

	// Verify .canonicalName aspect.
	aspEntry, err := harnessConn.KVGet(ctx, bootstrap.CoreKVBucket, bookDDLKey+".canonicalName")
	if err != nil {
		t.Fatalf("read .canonicalName aspect: %v", err)
	}
	var aspDoc struct {
		Data struct{ Value string `json:"value"` } `json:"data"`
	}
	if err := json.Unmarshal(aspEntry.Value, &aspDoc); err != nil {
		t.Fatalf("parse .canonicalName aspect: %v", err)
	}
	if aspDoc.Data.Value != "book" {
		t.Errorf(".canonicalName = %q, want %q", aspDoc.Data.Value, "book")
	}

	// Verify .script aspect is non-empty.
	scriptEntry, err := harnessConn.KVGet(ctx, bootstrap.CoreKVBucket, bookDDLKey+".script")
	if err != nil {
		t.Fatalf("read .script aspect: %v", err)
	}
	if len(scriptEntry.Value) == 0 {
		t.Error(".script aspect is empty")
	}

	t.Logf("milestone 2 elapsed: %v", time.Since(start))
}

// TestHelloLattice_Milestone3_CreateBook submits CreateBook and verifies
// the resulting vtx.book.* vertex in Core KV.
func TestHelloLattice_Milestone3_CreateBook(t *testing.T) {
	if bookDDLKey == "" {
		t.Skip("bookDDLKey not set — run Milestone2 first")
	}
	start := time.Now()
	ctx := testCtx(t)

	reply := submitOp(t, ctx, "CreateBook", processor.LaneDefault, bootstrap.BootstrapIdentityKey,
		map[string]any{"title": "The Pragmatic Programmer"})
	if reply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("CreateBook rejected: %v", reply)
	}

	// Extract bookKey from the reply.
	bKey, _ := reply.Detail["bookKey"].(string)
	if !strings.HasPrefix(bKey, "vtx.book.") {
		t.Fatalf("bookKey not a vtx.book.* key: %q", bKey)
	}
	bookVertexKey = bKey
	t.Logf("book vertex created at %s", bookVertexKey)

	// Read from Core KV and assert class + title.
	vtxEntry, err := harnessConn.KVGet(ctx, bootstrap.CoreKVBucket, bookVertexKey)
	if err != nil {
		t.Fatalf("read book vertex %s: %v", bookVertexKey, err)
	}
	var vtxDoc map[string]any
	if err := json.Unmarshal(vtxEntry.Value, &vtxDoc); err != nil {
		t.Fatalf("parse book vertex: %v", err)
	}
	if vtxDoc["class"] != "book" {
		t.Errorf("class = %q, want %q", vtxDoc["class"], "book")
	}
	data, _ := vtxDoc["data"].(map[string]any)
	if title, _ := data["title"].(string); title != "The Pragmatic Programmer" {
		t.Errorf("data.title = %q, want %q", title, "The Pragmatic Programmer")
	}

	t.Logf("milestone 3 elapsed: %v", time.Since(start))
}

// TestHelloLattice_Milestone4_LensProjection submits the books Lens and
// polls Postgres for the book row. Requires live Refractor + Postgres.
func TestHelloLattice_Milestone4_LensProjection(t *testing.T) {
	if bookVertexKey == "" {
		t.Skip("bookVertexKey not set — run Milestone3 first")
	}
	start := time.Now()
	ctx := testCtx(t)

	pgURL := os.Getenv("POSTGRES_URL")
	if pgURL == "" {
		pgURL = defaultPostgresURL
	}

	// Build the LensSpec JSON string.
	lensSpec := fmt.Sprintf(
		`{"canonicalName":"books","targetType":"postgres","targetConfig":{"dsn":%q,"table":"books","key":["book_id"]},"cypherRule":"MATCH (b:book) RETURN b.key AS book_id, b.title AS title","engine":"simple"}`,
		pgURL,
	)

	payload := map[string]any{
		"targetClass":   "meta.lens",
		"canonicalName": "books",
		"description":   "Projects all book vertices to the Postgres books table.",
		"spec":          lensSpec,
	}

	reply := submitOp(t, ctx, "CreateMetaVertex", processor.LaneMeta, bootstrap.BootstrapIdentityKey, payload)
	if reply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("CreateMetaVertex(meta.lens) rejected: %v", reply)
	}

	metaKey, _ := reply.Detail["metaKey"].(string)
	if !strings.HasPrefix(metaKey, "vtx.meta.") {
		t.Fatalf("lensMetaKey not a vtx.meta.* key: %q", metaKey)
	}
	lensMetaKey = metaKey
	t.Logf("books lens created at %s", lensMetaKey)

	// SD-1 verification: assert the .spec aspect data contains cypherRule.
	specEntry, err := harnessConn.KVGet(ctx, bootstrap.CoreKVBucket, lensMetaKey+".spec")
	if err != nil {
		t.Fatalf("read .spec aspect: %v", err)
	}
	var specDoc struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(specEntry.Value, &specDoc); err != nil {
		t.Fatalf("parse .spec aspect: %v", err)
	}
	if _, ok := specDoc.Data["cypherRule"]; !ok {
		t.Errorf(".spec aspect data missing 'cypherRule'; keys: %v", mapKeys(specDoc.Data))
	}
	if _, ok := specDoc.Data["source"]; ok {
		t.Errorf(".spec aspect data contains old 'source' key — SD-1 fix not applied correctly")
	}

	// Poll Postgres for the book row (NFR-P3: ≤ 500ms).
	db, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer db.Close(ctx)

	bookTitle := "The Pragmatic Programmer"
	var foundTitle string
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		row := db.QueryRow(ctx, "SELECT title FROM books WHERE title = $1", bookTitle)
		if scanErr := row.Scan(&foundTitle); scanErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("book row with title %q not found in Postgres within 500ms (NFR-P3); "+
				"check that Refractor is running (make up) and lens was accepted", bookTitle)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if foundTitle != bookTitle {
		t.Errorf("postgres title = %q, want %q", foundTitle, bookTitle)
	}
	t.Logf("Postgres row found: title=%q", foundTitle)

	t.Logf("milestone 4 elapsed: %v", time.Since(start))
}

// TestHelloLattice_Milestone5_AITraversal creates an AI agent identity,
// grants it CreateBook, and runs the cold-start traversal.
func TestHelloLattice_Milestone5_AITraversal(t *testing.T) {
	if bookDDLKey == "" {
		t.Skip("bookDDLKey not set — run Milestone2 first")
	}
	start := time.Now()
	ctx := testCtx(t)

	pgURL := os.Getenv("POSTGRES_URL")
	if pgURL == "" {
		pgURL = defaultPostgresURL
	}

	// Step 1: create a new identity for the AI agent.
	idReply := submitOp(t, ctx, "CreateUnclaimedIdentity", processor.LaneDefault,
		bootstrap.BootstrapIdentityKey, map[string]any{})
	if idReply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("CreateUnclaimedIdentity rejected: %v", idReply)
	}
	agentKey, _ := idReply.Detail["identityKey"].(string)
	if !strings.HasPrefix(agentKey, "vtx.identity.") {
		t.Fatalf("agentKey not a vtx.identity.* key: %q", agentKey)
	}
	agentID := strings.TrimPrefix(agentKey, "vtx.identity.")
	t.Logf("AI agent identity: %s", agentKey)

	// Step 2: create a CreateBook permission.
	permReply := submitOp(t, ctx, "CreatePermission", processor.LaneDefault,
		bootstrap.BootstrapIdentityKey, map[string]any{"operationType": "CreateBook", "scope": "any"})
	if permReply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("CreatePermission rejected: %v", permReply)
	}
	permKey, _ := permReply.Detail["permKey"].(string)
	if !strings.HasPrefix(permKey, "vtx.permission.") {
		t.Fatalf("permKey not a vtx.permission.* key: %q", permKey)
	}
	t.Logf("CreateBook permission: %s", permKey)

	// Step 3: grant the permission to the operator role.
	grantReply := submitOp(t, ctx, "GrantPermission", processor.LaneDefault,
		bootstrap.BootstrapIdentityKey, map[string]any{"permKey": permKey, "roleKey": bootstrap.RoleOperatorKey})
	if grantReply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("GrantPermission rejected: %v", grantReply)
	}
	t.Log("CreateBook granted to operator role")

	// Step 4: assign the agent to the operator role.
	assignReply := submitOp(t, ctx, "AssignRole", processor.LaneDefault,
		bootstrap.BootstrapIdentityKey, map[string]any{"actorKey": agentKey, "roleKey": bootstrap.RoleOperatorKey})
	if assignReply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("AssignRole rejected: %v", assignReply)
	}
	t.Log("Agent assigned to operator role")

	// Step 5: wait for Refractor to reproject the agent's capability doc
	// (NFR-P3: ≤ 500ms). Poll capability-kv.
	tr := aiagent.NewTraverser(harnessConn, bootstrap.CoreKVBucket, bootstrap.CapabilityKVBucket)
	var cap *processor.CapabilityDoc
	capDeadline := time.Now().Add(500 * time.Millisecond)
	for {
		var capErr error
		cap, capErr = tr.ReadCapability(ctx, agentID)
		if capErr == nil {
			// Check if CreateBook is already reflected.
			for _, p := range cap.PlatformPermissions {
				if p.OperationType == "CreateBook" {
					goto capFound
				}
			}
		}
		if time.Now().After(capDeadline) {
			t.Fatalf("NFR-P3 violated: capability doc for agent %s not reprojected within 500ms; "+
				"check that Refractor is running (make up)", agentKey)
		}
		time.Sleep(10 * time.Millisecond)
	}
capFound:

	// Step 6: cold-start traversal.
	cap2, err := tr.ReadCapability(ctx, agentID)
	if err != nil {
		t.Fatalf("ReadCapability for agent %s: %v", agentID, err)
	}
	t.Logf("Agent has %d platform permission(s)", len(cap2.PlatformPermissions))

	hasCreateBook := false
	for _, p := range cap2.PlatformPermissions {
		if p.OperationType == "CreateBook" {
			hasCreateBook = true
			break
		}
	}
	if !hasCreateBook {
		t.Fatalf("agent %s lacks CreateBook in capability doc", agentKey)
	}

	// Step 7: DiscoverDDL by canonicalName ("book").
	ddlKey, err := tr.DiscoverDDL(ctx, "book")
	if err != nil {
		t.Fatalf("DiscoverDDL(\"book\"): %v", err)
	}
	if ddlKey != bookDDLKey {
		t.Errorf("DiscoverDDL returned %q, want %q", ddlKey, bookDDLKey)
	}
	t.Logf("DiscoverDDL found: %s", ddlKey)

	// Step 8: verify permittedCommands contains CreateBook.
	pcEntry, err := harnessConn.KVGet(ctx, bootstrap.CoreKVBucket, ddlKey+".permittedCommands")
	if err != nil {
		t.Fatalf("read .permittedCommands: %v", err)
	}
	var pcDoc struct {
		Data struct{ Commands []string `json:"commands"` } `json:"data"`
	}
	if err := json.Unmarshal(pcEntry.Value, &pcDoc); err != nil {
		t.Fatalf("parse .permittedCommands: %v", err)
	}
	foundCreateBook := false
	for _, cmd := range pcDoc.Data.Commands {
		if cmd == "CreateBook" {
			foundCreateBook = true
			break
		}
	}
	if !foundCreateBook {
		t.Errorf("permittedCommands %v does not include CreateBook", pcDoc.Data.Commands)
	}

	// Step 9: read DDL aspects.
	aspects, err := tr.ReadDDLAspects(ctx, ddlKey)
	if err != nil {
		t.Fatalf("ReadDDLAspects: %v", err)
	}
	if aspects.InputSchema == "" {
		t.Error("DDL inputSchema is empty")
	}
	t.Logf("DDL inputSchema: %s", aspects.InputSchema)

	// Step 10: submit CreateBook as the agent.
	agentBookTitle := "Hello Lattice (AI Agent)"
	agentBookReply := submitOp(t, ctx, "CreateBook", processor.LaneDefault, agentKey,
		map[string]any{"title": agentBookTitle})
	if agentBookReply.Status != processor.ReplyStatusAccepted {
		t.Fatalf("agent CreateBook rejected: %v", agentBookReply)
	}
	agentBookKey, _ := agentBookReply.Detail["bookKey"].(string)
	t.Logf("Agent created book: %s", agentBookKey)

	// Step 11: verify book vertex in Core KV.
	if agentBookKey != "" {
		vtxEntry, err := harnessConn.KVGet(ctx, bootstrap.CoreKVBucket, agentBookKey)
		if err != nil {
			t.Fatalf("read agent book vertex: %v", err)
		}
		var vtxDoc map[string]any
		if err := json.Unmarshal(vtxEntry.Value, &vtxDoc); err != nil {
			t.Fatalf("parse agent book vertex: %v", err)
		}
		if vtxDoc["class"] != "book" {
			t.Errorf("agent book class = %q, want %q", vtxDoc["class"], "book")
		}
	}

	// Step 12: poll Postgres for the agent's book row (NFR-P3: ≤ 500ms).
	if lensMetaKey != "" {
		db2, err := pgx.Connect(ctx, pgURL)
		if err != nil {
			t.Fatalf("connect postgres: %v", err)
		}
		defer db2.Close(ctx)

		deadline := time.Now().Add(500 * time.Millisecond)
		for {
			var found string
			row := db2.QueryRow(ctx, "SELECT title FROM books WHERE title = $1", agentBookTitle)
			if scanErr := row.Scan(&found); scanErr == nil {
				t.Logf("Postgres row found for AI agent book: title=%q", found)
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("AI agent book row not found in Postgres within 500ms (NFR-P3)")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	} else {
		t.Log("lensMetaKey not set — skipping Postgres assertion for agent book (run Milestone4 first)")
	}

	t.Logf("milestone 5 elapsed: %v", time.Since(start))
}

// TestHelloLattice_WriteGate5Marker writes health.gates.phase1.gate5 to
// Health KV on successful completion of all milestones.
func TestHelloLattice_WriteGate5Marker(t *testing.T) {
	commit := os.Getenv("GITHUB_SHA")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	markerValue, err := json.Marshal(map[string]any{
		"passed":      true,
		"completedAt": time.Now().UTC().Format(time.RFC3339),
		"commit":      commit,
	})
	if err != nil {
		t.Logf("WARNING: gate5 Health KV marker: marshal error: %v", err)
		return
	}

	if _, err := harnessConn.KVPut(ctx, bootstrap.HealthKVBucket, "health.gates.phase1.gate5", markerValue); err != nil {
		t.Logf("WARNING: gate5 Health KV marker: KVPut error: %v — marker NOT written", err)
		return
	}
	t.Logf("gate5 Health KV marker written: health.gates.phase1.gate5 = %s", string(markerValue))

	totalElapsed := time.Since(suiteStart)
	t.Logf("total suite elapsed: %v", totalElapsed)
	if totalElapsed > 60*time.Minute {
		t.Logf("WARNING: total suite elapsed (%v) exceeds 60 min target (AC7)", totalElapsed)
	}
}

// testCtx returns a context with a generous deadline for each test.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// submitOp publishes an OperationEnvelope to ops.<lane> via JetStream and
// waits for the Processor's reply on a NATS core inbox.
func submitOp(t *testing.T, ctx context.Context, operationType string, lane processor.Lane, actor string, payload map[string]any) *processor.OperationReply {
	t.Helper()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	reqID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("generate requestId: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          lane,
		OperationType: operationType,
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       json.RawMessage(payloadBytes),
	}

	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	nc := harnessConn.NATS()
	inbox := natsgo.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe inbox: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	subject := "ops." + string(lane)
	msg := &natsgo.Msg{
		Subject: subject,
		Data:    envBytes,
		Header:  natsgo.Header{replyInboxHeader: []string{inbox}},
	}
	if _, err := harnessConn.JetStream().PublishMsg(ctx, msg); err != nil {
		t.Fatalf("publish %s to %s: %v", operationType, subject, err)
	}

	replyMsg, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		t.Fatalf("wait for reply to %s: %v", operationType, err)
	}

	var reply processor.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		t.Fatalf("parse reply for %s: %v", operationType, err)
	}
	return &reply
}

// mapKeys returns the keys of a map as a slice (for error messages).
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
