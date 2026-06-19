package failure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
)

// DLQMessage is the diagnostic payload written to a rule's DLQ stream.
// All field names are camelCase per architecture.md.
type DLQMessage struct {
	RuleID       string `json:"ruleId"`
	EntityID     string `json:"entityId"`
	FailedStage  string `json:"failedStage"` // "traversal" | "projection" | "write"
	ErrorClass   string `json:"errorClass"`  // "TRANSIENT" | "TERMINAL"
	ErrorMessage string `json:"errorMessage"`
	RetryCount   int    `json:"retryCount"`
	RuleSequence string `json:"ruleSequence"` // NATS stream sequence of the active rule version; "" if reporter not configured
	Timestamp    string `json:"timestamp"`    // RFC3339 UTC
	RawPayload   string `json:"rawPayload"`   // original NATS message body as string
}

// Publish writes msg to the DLQ stream for the given ruleID.
// The stream is created (idempotent) if absent. Subject: subjects.DLQ(ruleID).
func Publish(ctx context.Context, conn *substrate.Conn, ruleID string, msg DLQMessage) error {
	subject := subjects.DLQ(ruleID)
	streamName := "REFRACTOR_DLQ_" + strings.ToUpper(ruleID)
	if err := conn.EnsureStream(ctx, substrate.StreamSpec{
		Name:     streamName,
		Subjects: []string{subject},
	}); err != nil {
		return fmt.Errorf("failure: create DLQ stream: %w", err)
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failure: marshal DLQ message: %w", err)
	}
	return conn.Publish(ctx, subject, payload, nil)
}
