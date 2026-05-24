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
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/aiagent"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

func main() {
	natsURL := getEnv("NATS_URL", "nats://localhost:4222")
	actorKey := mustGetEnv("AGENT_ACTOR_KEY")

	// Extract actorID from "vtx.identity.<NanoID>" → "<NanoID>".
	actorID := extractActorID(actorKey)

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

	t := aiagent.NewTraverser(conn, bootstrap.CoreKVBucket, bootstrap.CapabilityKVBucket)

	// Step 1: read capability set.
	cap, err := t.ReadCapability(ctx, actorID)
	if err != nil {
		log.Fatalf("ReadCapability for actor %s: %v\n"+
			"Ensure the agent has a capability doc (run make up, then grant CreateBook to the agent identity).",
			actorID, err)
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

	// Step 7: print the bookKey from the reply.
	bookKey, _ := reply.Detail["bookKey"].(string)
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

// extractActorID strips the "vtx.identity." prefix from an actor key.
func extractActorID(actorKey string) string {
	const prefix = "vtx.identity."
	if strings.HasPrefix(actorKey, prefix) {
		return actorKey[len(prefix):]
	}
	return actorKey
}
