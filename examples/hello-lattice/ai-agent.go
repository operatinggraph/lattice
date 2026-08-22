// ai-agent.go — Hello Lattice Milestone 5: AI agent cold-start traversal demo.
//
// Demonstrates the FR19 cold-start traversal algorithm:
//  1. Reads the agent's capability set from Capability KV.
//  2. Confirms CreateBook is in platformPermissions[].
//  3. Calls DiscoverDDL("book") to find the book DDL meta-vertex.
//  4. Reads the DDL's inputSchema aspect.
//  5. Constructs a CreateBook payload.
//  6. Submits the operation via ops.default using the agent's actor key.
//  7. Prints the bookKey from the operation reply.
//
// Prerequisites:
//   - make up (NATS + Postgres + Refractor running)
//   - AGENT_ACTOR_KEY env var set to a vtx.identity.<NanoID> that has
//     CreateBook permission granted (via CreatePermission + AssignRole)
//   - the deployment's lattice.bootstrap.json readable — step 1's Capability-KV
//     keys are chosen by actor class, and the class predicate is keyed on a
//     primordial NanoID that lives only in that file. BOOTSTRAP_JSON_PATH
//     overrides its location; the default resolves from the repo root whether
//     this program is run from here or from examples/hello-lattice.
//
// Usage:
//
//	AGENT_ACTOR_KEY=vtx.identity.<NanoID> go run examples/hello-lattice/ai-agent.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/aiagent"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func main() {
	natsURL := getEnv("NATS_URL", "nats://localhost:4222")
	actorKey := mustGetEnv("AGENT_ACTOR_KEY")

	// The primordial identifier table, loaded once at start the way every
	// platform binary does (cmd/processor/main.go:71). bootstrap.SystemActorKeys
	// below matches holdsRole links against the roleOperator NanoID, which lives
	// in this file and nowhere else.
	if err := bootstrap.Load(bootstrapJSONPath()); err != nil {
		log.Fatalf("load bootstrap identifiers from %s: %v\n"+
			"Set BOOTSTRAP_JSON_PATH to the deployment's lattice.bootstrap.json.",
			bootstrapJSONPath(), err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:  natsURL,
		Name: "hello-lattice-agent",
	})
	if err != nil {
		log.Fatalf("connect to NATS at %s: %v", natsURL, err)
	}
	defer conn.Close()

	// The system-actor set decides which Capability-KV keys the agent's own
	// grants live at, so it is resolved once here and handed to the traverser
	// rather than re-listed per read.
	systemActorKeys, err := bootstrap.SystemActorKeys(ctx, conn)
	if err != nil {
		log.Fatalf("discover system actor keys: %v", err)
	}
	t := aiagent.NewTraverser(conn, bootstrap.CoreKVBucket, bootstrap.CapabilityKVBucket, systemActorKeys)

	// Step 1: read capability set.
	cap, err := t.ReadCapability(ctx, actorKey)
	if err != nil {
		log.Fatalf("ReadCapability for actor %s: %v\n"+
			"Ensure the agent has a capability doc (run make up, then grant CreateBook to the agent identity).",
			actorKey, err)
	}
	fmt.Printf("Agent has %d platform permission(s)\n", len(cap.PlatformPermissions))

	// Step 2: confirm CreateBook is in the capability set.
	hasCreateBook := false
	for _, p := range cap.PlatformPermissions {
		if p.OperationType == "CreateBook" {
			hasCreateBook = true
			break
		}
	}
	if !hasCreateBook {
		log.Fatalf("agent %s lacks CreateBook permission — grant it via rbac-domain AssignRole first", actorKey)
	}
	fmt.Println("CreateBook permission confirmed in capability set")

	// Step 3: discover the book DDL by canonical name.
	// DiscoverDDL matches against the DDL's canonicalName ("book"), not the
	// operation type ("CreateBook"). The DDL's permittedCommands list carries
	// the operation type — see Step 3b below.
	ddlKey, err := t.DiscoverDDL(ctx, "book")
	if err != nil {
		log.Fatalf("DiscoverDDL(\"book\"): %v\n"+
			"Ensure the book DDL has been submitted (Milestone 2 of the tutorial).", err)
	}
	fmt.Printf("Book DDL key: %s\n", ddlKey)

	// Step 3b: verify DDL permits CreateBook.
	if err := verifyPermittedCommands(ctx, conn, ddlKey, "CreateBook"); err != nil {
		log.Fatalf("permittedCommands check: %v", err)
	}
	fmt.Println("Verified: DDL permittedCommands includes CreateBook")

	// Step 4: read the DDL's self-description aspects.
	aspects, err := t.ReadDDLAspects(ctx, ddlKey)
	if err != nil {
		log.Fatalf("ReadDDLAspects: %v", err)
	}
	fmt.Printf("DDL inputSchema: %s\n", aspects.InputSchema)

	// Step 5: construct a CreateBook payload.
	bookTitle := getEnv("BOOK_TITLE", "Hello Lattice (AI Agent)")
	payloadBytes, err := json.Marshal(map[string]string{"title": bookTitle})
	if err != nil {
		log.Fatalf("marshal payload: %v", err)
	}

	reqID, err := substrate.NewNanoID()
	if err != nil {
		log.Fatalf("generate requestId: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBook",
		Actor:         actorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       json.RawMessage(payloadBytes),
	}

	// Step 6: submit via ops.default using the reply-inbox pattern.
	reply, err := submitOp(ctx, conn.NATS(), env)
	if err != nil {
		log.Fatalf("submit CreateBook: %v", err)
	}

	if reply.Status == processor.ReplyStatusRejected {
		log.Fatalf("CreateBook rejected: %s — %s", reply.Error.Code, reply.Error.Message)
	}

	// Step 7: print the bookKey from the reply (the committed primaryKey).
	bookKey := reply.PrimaryKey
	fmt.Printf("CreateBook accepted!\n")
	fmt.Printf("  requestId:   %s\n", reply.RequestID)
	fmt.Printf("  opTracker:   %s\n", reply.OpTrackerKey)
	if bookKey != "" {
		fmt.Printf("  bookKey:     %s\n", bookKey)
	}
	fmt.Printf("\nVerify the projection:\n")
	fmt.Printf("  lattice query postgres \"SELECT * FROM books WHERE title = '%s'\"\n", bookTitle)
	fmt.Println("\nDone.")
}

// submitOp publishes an OperationEnvelope to ops.<lane> via JetStream and
// waits for the Processor's reply on a NATS core inbox. Mirrors the pattern
// in cmd/lattice/output/submit.go.
func submitOp(ctx context.Context, nc *natsgo.Conn, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	// Subscribe to a reply inbox before publishing so no reply is missed.
	inbox := natsgo.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("subscribe inbox: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("JetStream: %w", err)
	}

	subject := "ops." + string(env.Lane)
	msg := &natsgo.Msg{
		Subject: subject,
		Data:    data,
		Header:  natsgo.Header{"Lattice-Reply-Inbox": []string{inbox}},
	}
	if _, err := js.PublishMsg(msg); err != nil {
		return nil, fmt.Errorf("publish to %s: %w", subject, err)
	}

	replyMsg, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for reply: %w", err)
	}

	var reply processor.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	return &reply, nil
}

// verifyPermittedCommands reads the .permittedCommands aspect of a DDL
// meta-vertex and confirms operationType is in the list.
func verifyPermittedCommands(ctx context.Context, conn *substrate.Conn, ddlKey, operationType string) error {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, ddlKey+".permittedCommands")
	if err != nil {
		return fmt.Errorf("read .permittedCommands at %s: %w", ddlKey, err)
	}
	var aspDoc struct {
		Data struct {
			Commands []string `json:"commands"`
		} `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &aspDoc); err != nil {
		return fmt.Errorf("parse .permittedCommands: %w", err)
	}
	for _, cmd := range aspDoc.Data.Commands {
		if cmd == operationType {
			return nil
		}
	}
	return fmt.Errorf("operationType %q not in permittedCommands %v", operationType, aspDoc.Data.Commands)
}

// bootstrapJSONPath resolves the deployment's lattice.bootstrap.json.
// BOOTSTRAP_JSON_PATH wins, as it does for every daemon. The fallback tries
// the repo root both from the repo root itself and from this example's own
// directory, because the tutorial runs this program from
// examples/hello-lattice (its Makefile's milestone-5) while a direct
// `go run examples/hello-lattice/ai-agent.go` runs it from the root.
func bootstrapJSONPath() string {
	if p := os.Getenv("BOOTSTRAP_JSON_PATH"); p != "" {
		return p
	}
	for _, candidate := range []string{"lattice.bootstrap.json", "../../lattice.bootstrap.json"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "lattice.bootstrap.json"
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("environment variable %s is required", key)
	}
	return v
}
